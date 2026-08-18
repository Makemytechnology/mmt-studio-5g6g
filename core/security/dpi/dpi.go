// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package dpi — Deep Packet Inspection / Application Detection.
//
// Spec anchors (verifiable against local PDFs):
//
//   - TS 23.501 §5.8           User Plane Management — umbrella that
//                               places traffic detection and PFD inside
//                               the SMF→UPF pipeline.
//   - TS 23.501 §5.8.2.4       Traffic Detection — the operator surface
//                               this package realises (per-application
//                               classification of UP traffic).
//   - TS 23.501 §5.8.2.4.1     General — Application identifier matching
//                               against PFDs is the model implemented in
//                               DetectAppBy{SNI,DNS,IP}.
//   - TS 23.501 §5.8.2.4.2     Traffic Detection Information — what the
//                               SMF provisions to the UPF (PDR + PFD set).
//   - TS 23.501 §5.8.2.6       Charging and Usage Monitoring Handling —
//                               drives LogDetection() (per-app UL/DL
//                               byte counters).
//   - TS 23.501 §5.8.2.8.4     Support of PFD Management — NEF (PFDF) →
//                               SMF → UPF push/pull of PFDs. The
//                               dpi_pfd_rules table is the local cache.
//   - TS 23.502 §4.4.3.5       N4 PFD management Procedure — Stage 2
//                               procedure used by SMF to push PFDs over
//                               PFCP.
//   - TS 29.244 §6.2.5         PFCP PFD Management Procedure — Stage 3.
//   - TS 29.244 §6.2.5.1       General — PFCP-PFD-Management high-level.
//   - TS 29.244 §6.2.5.2       CP Function Behaviour.
//   - TS 29.244 §6.2.5.3       UP Function Behaviour.
//   - TS 29.244 §7.4.3         PFCP PFD Management messages.
//   - TS 29.244 §7.4.3.1       PFCP PFD Management Request.
//   - TS 29.244 §7.4.3.2       PFCP PFD Management Response.
//   - TS 29.244 §5.4.5         DL Flow Level Marking for Application
//                               Detection — DSCP marking of detected app
//                               traffic; not yet applied here.
//
// Deferred surfaces (PDFs not loaded; cited as TODO(spec:) prose so
// speccheck won't try to ground them):
//
//   - TODO(spec: TS 23.503, "Policy and Charging Control framework
//     for the 5G System") — the PFD lifecycle (push from NEF/PFDF
//     into SMF, NWDAF "Determination analytics") lives here. We
//     accept locally-supplied PFD rules but do not consume the
//     Stage 3 PFD management API.
//   - TODO(spec: TS 29.122, "T8 reference point") — the AF→NEF
//     northbound for posting PFDs into the network. Not implemented;
//     the seed catalogue is hard-coded for now.
//
// Implementation notes:
//
//   - All timestamp columns are TEXT with datetime('now') defaults; we
//     write ISO datetime strings everywhere (was: float64 epoch
//     seconds) so the dedup window in LogDetection compares correctly
//     and last_seen sorts lexicographically.
//   - PFD detection types match the schema CHECK constraint:
//     'sni', 'dns', 'ip_range', 'host', 'port_range'.
package dpi

import (
	"encoding/binary"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Detection-type constants (mirror schema CHECK).
const (
	DetectionSNI       = "sni"
	DetectionDNS       = "dns"
	DetectionIPRange   = "ip_range"
	DetectionHost      = "host"
	DetectionPortRange = "port_range"
)

func validDetectionType(t string) bool {
	switch t {
	case DetectionSNI, DetectionDNS, DetectionIPRange, DetectionHost, DetectionPortRange:
		return true
	}
	return false
}

// nowISO matches the lexicographic form written by datetime('now').
func nowISO() string { return time.Now().UTC().Format("2006-01-02 15:04:05") }

// minutesAgoISO returns an ISO datetime string mins minutes in the past.
func minutesAgoISO(mins int) string {
	return time.Now().UTC().Add(-time.Duration(mins) * time.Minute).Format("2006-01-02 15:04:05")
}

// ─────────────────────────── Application CRUD ────────────────────
//
// Spec anchor: TS 23.501 §5.8.2.4.1 — applications are the identifier
// space the SMF uses to drive PDR matching at the UPF.

// CreateApp inserts or updates an application definition (upsert by app_id).
func CreateApp(appID, appName, category, qosProfile, chargingProfile string, priority int) error {
	if appID == "" || appName == "" {
		return fmt.Errorf("app_id and app_name required")
	}
	if category == "" {
		category = "general"
	}
	if priority <= 0 {
		priority = 100
	}
	_, err := engine.Exec(`INSERT INTO dpi_applications
		(app_id, app_name, category, qos_profile, charging_profile, priority, enabled)
		VALUES (?,?,?,?,?,?,1)
		ON CONFLICT(app_id) DO UPDATE SET
			app_name=excluded.app_name,
			category=excluded.category,
			qos_profile=excluded.qos_profile,
			charging_profile=excluded.charging_profile,
			priority=excluded.priority`,
		appID, appName, category, nilStr(qosProfile), nilStr(chargingProfile), priority)
	return err
}

// DeleteApp removes an application + its PFD rules (FK cascade).
func DeleteApp(appID string) error {
	_, err := engine.Exec("DELETE FROM dpi_applications WHERE app_id=?", appID)
	return err
}

// ListApps returns all application definitions.
func ListApps() ([]map[string]interface{}, error) {
	return queryRows("SELECT * FROM dpi_applications ORDER BY priority, app_id")
}

// GetApp returns a single application.
func GetApp(appID string) (map[string]interface{}, error) {
	return queryRow("SELECT * FROM dpi_applications WHERE app_id=?", appID)
}

// SetEnabled toggles enabled flag for an app. Returns whether a row changed.
func SetEnabled(appID string, enabled bool) (bool, error) {
	v := 0
	if enabled {
		v = 1
	}
	res, err := engine.Exec("UPDATE dpi_applications SET enabled=? WHERE app_id=?", v, appID)
	if err != nil || res == nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ─────────────────────────── PFD Rule CRUD ───────────────────────
//
// Spec anchors:
//   - TS 23.501 §5.8.2.8.4 — PFD lifecycle.
//   - TS 23.502 §4.4.3.5   — N4 PFD management procedure.
//   - TS 29.244 §6.2.5     — PFCP-side procedure on the wire.
//
// AddPFDRule is the local cache primitive — a real deployment receives
// PFDs from NEF (PFDF) per TS 23.501 §5.8.2.8.4; we accept locally-
// authored rules and treat them as the canonical set.

// AddPFDRule appends a PFD rule for an application.
func AddPFDRule(appID, detectionType, pattern string) error {
	if appID == "" {
		return fmt.Errorf("app_id required")
	}
	if !validDetectionType(detectionType) {
		return fmt.Errorf("invalid detection_type %q (want sni|dns|ip_range|host|port_range)", detectionType)
	}
	if pattern == "" {
		return fmt.Errorf("pattern required")
	}
	_, err := engine.Exec(
		"INSERT INTO dpi_pfd_rules (app_id, detection_type, pattern) VALUES (?,?,?)",
		appID, detectionType, pattern)
	return err
}

// DeletePFDRule removes a PFD rule by id. Returns whether a row was removed.
func DeletePFDRule(ruleID int64) (bool, error) {
	res, err := engine.Exec("DELETE FROM dpi_pfd_rules WHERE id=?", ruleID)
	if err != nil || res == nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetPFDRules returns PFD rules, optionally filtered by app_id.
func GetPFDRules(appID string) ([]map[string]interface{}, error) {
	if appID != "" {
		return queryRows("SELECT * FROM dpi_pfd_rules WHERE app_id=? ORDER BY detection_type, id", appID)
	}
	return queryRows("SELECT * FROM dpi_pfd_rules ORDER BY app_id, detection_type, id")
}

// GetAllRulesByType returns all PFD rules grouped by detection_type.
func GetAllRulesByType() map[string][]map[string]interface{} {
	rules, _ := GetPFDRules("")
	byType := make(map[string][]map[string]interface{})
	for _, r := range rules {
		dt := getString(r, "detection_type")
		byType[dt] = append(byType[dt], r)
	}
	return byType
}

// ─────────────────────────── App Detection ───────────────────────
//
// Spec anchor: TS 23.501 §5.8.2.4 — at the UPF, traffic is matched
// against PDR → SDF → ApplicationID. We expose three classifier entry
// points; production UPF wires them into the PDR fast path.
//
// TODO(spec: TS 29.244 §5.4.5) DL Flow Level Marking — once an app is
// detected we should DSCP-mark the DL flow per the URR / FAR. Not done.

// DetectAppBySNI matches TLS SNI against the 'sni' PFD rules.
func DetectAppBySNI(sni string) string {
	if sni == "" {
		return ""
	}
	rules := GetAllRulesByType()
	for _, r := range rules[DetectionSNI] {
		if matchGlob(sni, getString(r, "pattern")) {
			return getString(r, "app_id")
		}
	}
	return ""
}

// DetectAppByIP matches a destination IP against the DNS cache and
// 'ip_range' PFDs.
func DetectAppByIP(ipAddr string) string {
	if ipAddr == "" {
		return ""
	}
	// DNS-cache fast path: a previously resolved IP keeps the app_id.
	row, _ := queryRow(
		"SELECT app_id FROM dpi_dns_cache WHERE resolved_ip=? AND app_id IS NOT NULL ORDER BY cached_at DESC LIMIT 1",
		ipAddr)
	if row != nil {
		if id := getString(row, "app_id"); id != "" {
			return id
		}
	}
	// Walk ip_range rules. Patterns are CIDR strings.
	parsed := net.ParseIP(ipAddr)
	if parsed == nil {
		return ""
	}
	rules := GetAllRulesByType()
	for _, r := range rules[DetectionIPRange] {
		_, cidr, err := net.ParseCIDR(getString(r, "pattern"))
		if err != nil {
			continue
		}
		if cidr.Contains(parsed) {
			return getString(r, "app_id")
		}
	}
	return ""
}

// DetectAppByDNS matches a DNS query domain against 'dns' rules first
// (suffix match) and 'sni' rules second (glob fallback).
func DetectAppByDNS(domain string) string {
	if domain == "" {
		return ""
	}
	rules := GetAllRulesByType()
	domLower := strings.ToLower(strings.TrimRight(domain, "."))
	for _, r := range rules[DetectionDNS] {
		pat := strings.ToLower(getString(r, "pattern"))
		if domLower == pat || strings.HasSuffix(domLower, "."+pat) {
			return getString(r, "app_id")
		}
	}
	for _, r := range rules[DetectionSNI] {
		if matchGlob(domLower, getString(r, "pattern")) {
			return getString(r, "app_id")
		}
	}
	return ""
}

// DetectAppByPort matches a TCP/UDP destination port against 'port_range'
// rules. Patterns are "low-high" or single ports.
func DetectAppByPort(port int) string {
	if port <= 0 || port > 65535 {
		return ""
	}
	rules := GetAllRulesByType()
	for _, r := range rules[DetectionPortRange] {
		lo, hi, ok := parsePortRange(getString(r, "pattern"))
		if !ok {
			continue
		}
		if port >= lo && port <= hi {
			return getString(r, "app_id")
		}
	}
	return ""
}

// ─────────────────────────── DNS Cache ───────────────────────────
//
// Caching DNS resolutions means subsequent IP-only flows can be tied
// back to the original domain (and therefore to an app_id). This is the
// classic "DNS-pinning" trick used by 5GC DPI implementations.

// CacheDNSResolution stores the (domain → IP) mapping and pins the
// matching app_id (if any). ttl is in seconds.
func CacheDNSResolution(domain string, resolvedIPs []string, ttl int) {
	if ttl <= 0 {
		ttl = 300
	}
	appID := DetectAppByDNS(domain)
	for _, ip := range resolvedIPs {
		_, _ = engine.Exec(`INSERT INTO dpi_dns_cache
			(domain, resolved_ip, app_id, cached_at, ttl_sec)
			VALUES (?,?,?, datetime('now'), ?)
			ON CONFLICT(domain, resolved_ip) DO UPDATE SET
				app_id=excluded.app_id,
				cached_at=datetime('now'),
				ttl_sec=excluded.ttl_sec`,
			domain, ip, nilStr(appID), ttl)
	}
}

// GetDNSCacheStats returns DNS cache statistics.
func GetDNSCacheStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{"total_entries": 0, "app_matched": 0}
	}
	var total, matched int
	_ = db.QueryRow("SELECT COUNT(*) FROM dpi_dns_cache").Scan(&total)
	_ = db.QueryRow("SELECT COUNT(*) FROM dpi_dns_cache WHERE app_id IS NOT NULL").Scan(&matched)
	return map[string]interface{}{"total_entries": total, "app_matched": matched}
}

// PurgeExpiredDNS removes cache rows whose (cached_at + ttl_sec) has
// passed. Returns the number of rows pruned.
func PurgeExpiredDNS() int64 {
	res, err := engine.Exec(
		`DELETE FROM dpi_dns_cache
		 WHERE datetime(cached_at, '+' || ttl_sec || ' seconds') < datetime('now')`)
	if err != nil || res == nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// ─────────────────────────── Detection Logging ───────────────────
//
// Spec anchor: TS 23.501 §5.8.2.6 (Charging and Usage Monitoring) —
// per-app byte counters drive the URR-style usage reports the SMF
// would otherwise forward to CHF. The 1-hour dedup window collapses
// repeated detections of the same app for the same UE into one row
// rather than spamming a new row per packet.

// LogDetection records one detection event. UL/DL byte counters are
// merged into a row from the past 60 minutes if one exists, so a long-
// running flow accumulates rather than producing one row per detection.
func LogDetection(imsi, appID string, pduSessionID int, bytesUL, bytesDL int64) {
	if imsi == "" || appID == "" {
		return
	}
	if bytesUL < 0 {
		bytesUL = 0
	}
	if bytesDL < 0 {
		bytesDL = 0
	}
	db, err := engine.Open()
	if err != nil {
		return
	}
	cutoff := minutesAgoISO(60)

	var id int64
	var existUL, existDL int64
	err = db.QueryRow(
		"SELECT id, bytes_ul, bytes_dl FROM dpi_detection_log WHERE imsi=? AND app_id=? AND last_seen > ? ORDER BY last_seen DESC LIMIT 1",
		imsi, appID, cutoff).Scan(&id, &existUL, &existDL)
	if err == nil {
		_, _ = db.Exec(
			"UPDATE dpi_detection_log SET bytes_ul=?, bytes_dl=?, last_seen=datetime('now') WHERE id=?",
			existUL+bytesUL, existDL+bytesDL, id)
		return
	}
	_, _ = db.Exec(
		"INSERT INTO dpi_detection_log (imsi, app_id, pdu_session_id, bytes_ul, bytes_dl) VALUES (?,?,?,?,?)",
		imsi, appID, pduSessionID, bytesUL, bytesDL)
}

// GetDetectionLog returns detection log entries, filterable by IMSI / app.
func GetDetectionLog(imsi, appID string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	q := "SELECT * FROM dpi_detection_log WHERE 1=1"
	var args []interface{}
	if imsi != "" {
		q += " AND imsi=?"
		args = append(args, imsi)
	}
	if appID != "" {
		q += " AND app_id=?"
		args = append(args, appID)
	}
	q += " ORDER BY last_seen DESC, id DESC LIMIT ?"
	args = append(args, limit)
	return queryRows(q, args...)
}

// GetAppUsageSummary aggregates per-app DL/UL byte volume.
func GetAppUsageSummary() ([]map[string]interface{}, error) {
	return queryRows(`SELECT app_id,
		COUNT(DISTINCT imsi) AS users,
		SUM(bytes_dl) AS total_dl,
		SUM(bytes_ul) AS total_ul
		FROM dpi_detection_log GROUP BY app_id ORDER BY total_dl IS NULL, total_dl DESC`)
}

// ─────────────────────────── Seed Defaults ───────────────────────

// SeedDefaultApps seeds a small catalogue of default applications.
//
// TODO(spec: TS 29.122) — in production this catalogue arrives via the
// AF→NEF T8 reference point; the seed list here is dev-only.
func SeedDefaultApps() {
	defaults := []struct {
		id, name, cat string
		rules         [][2]string
	}{
		{"youtube", "YouTube", "video", [][2]string{{DetectionSNI, "*.youtube.com"}, {DetectionSNI, "*.googlevideo.com"}, {DetectionDNS, "youtube.com"}}},
		{"netflix", "Netflix", "video", [][2]string{{DetectionSNI, "*.netflix.com"}, {DetectionSNI, "*.nflxvideo.net"}, {DetectionDNS, "netflix.com"}}},
		{"whatsapp", "WhatsApp", "voip", [][2]string{{DetectionSNI, "*.whatsapp.net"}, {DetectionSNI, "*.whatsapp.com"}, {DetectionDNS, "whatsapp.com"}}},
		{"instagram", "Instagram", "social", [][2]string{{DetectionSNI, "*.instagram.com"}, {DetectionDNS, "instagram.com"}}},
		{"tiktok", "TikTok", "social", [][2]string{{DetectionSNI, "*.tiktok.com"}, {DetectionDNS, "tiktok.com"}}},
		{"facebook", "Facebook", "social", [][2]string{{DetectionSNI, "*.facebook.com"}, {DetectionDNS, "facebook.com"}}},
		{"google", "Google", "general", [][2]string{{DetectionSNI, "*.google.com"}, {DetectionSNI, "*.googleapis.com"}}},
		{"teams", "Microsoft Teams", "voip", [][2]string{{DetectionSNI, "*.teams.microsoft.com"}, {DetectionDNS, "teams.microsoft.com"}}},
	}
	for _, d := range defaults {
		_ = CreateApp(d.id, d.name, d.cat, "", "", 100)
		for _, r := range d.rules {
			_ = AddPFDRule(d.id, r[0], r[1])
		}
	}
	logger.Get("dpi").Infof("DPI: seeded %d default applications", len(defaults))
}

// ─────────────────────────── GUI / OAM ───────────────────────────

// List returns rows from dpi_applications.
func List() ([]map[string]any, error) { return ListApps() }

// Status returns counters for the OAM panel.
func Status() map[string]any {
	apps, _ := ListApps()
	rules, _ := GetPFDRules("")
	dns := GetDNSCacheStats()
	return map[string]any{
		"apps":      len(apps),
		"pfd_rules": len(rules),
		"dns":       dns,
	}
}

// ─────────────────────────── helpers ─────────────────────────────

func matchGlob(s, pattern string) bool {
	matched, _ := filepath.Match(strings.ToLower(pattern), strings.ToLower(s))
	return matched
}

// parsePortRange parses "lo-hi" or "lo" forms into [lo, hi].
func parsePortRange(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(s, "-", 2)
	lo, err := atoi(parts[0])
	if err != nil || lo < 0 || lo > 65535 {
		return 0, 0, false
	}
	if len(parts) == 1 {
		return lo, lo, true
	}
	hi, err := atoi(parts[1])
	if err != nil || hi < lo || hi > 65535 {
		return 0, 0, false
	}
	return lo, hi, true
}

func atoi(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		switch vv := v.(type) {
		case string:
			return vv
		case []byte:
			return string(vv)
		}
	}
	return ""
}

func nilStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func queryRow(q string, args ...interface{}) (map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		return nil, nil
	}
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	m := make(map[string]interface{}, len(cols))
	for i, c := range cols {
		m[c] = vals[i]
	}
	return m, nil
}

func queryRows(q string, args ...interface{}) ([]map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, nil
}

// keep encoding/binary import alive — used by future PFCP encoders.
var _ = binary.BigEndian

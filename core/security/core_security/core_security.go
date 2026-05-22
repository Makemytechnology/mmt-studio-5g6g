// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package core_security — 5G core network security: signalling firewall,
// per-interface guards, rate limiting, intrusion detection, and the
// immutable security audit log.
//
// Spec anchors (all verified against local PDFs):
//
//   - TS 33.501 §5.9 Core network security — top-level requirements.
//   - TS 33.501 §5.9.1 Trust boundaries — drives the known-gNB / blocked-IP
//     split: anything outside the gNB trust set is treated as untrusted
//     and must be authorised before any NGAP/NAS processing.
//   - TS 33.501 §5.9.4 Requirements for monitoring 5GC signaling traffic —
//     this is the spec basis for the IDS surface (signature evaluation,
//     audit-log streaming, replay/flood detection).
//   - TS 33.501 §9.2 Security mechanisms for the N2 interface — drives the
//     "ngap" firewall protocol class and CheckNGAPSource.
//   - TS 33.501 §9.3 Security requirements and procedures on N3 — drives
//     the "gtpu" firewall protocol class and CheckGTPUPacket.
//   - TS 33.501 §9.5 Interfaces based on DIAMETER or GTP — backstop for
//     legacy GTP-C / S-GW interconnect rules.
//   - TS 33.501 §9.9 Security mechanisms for non-SBA interfaces internal
//     to the 5GC and between PLMNs — drives N4/N9 inter-PLMN rules.
//   - TS 23.501 §5.10 Security aspects — architecture-side framing.
//   - TS 23.501 §5.10.3 PDU Session User Plane Security — drives the
//     UP-integrity/UP-confidentiality decisions enforced at the GTP-U
//     guard (not yet wired here; see TODO).
//
// Deferred surfaces (no local PDF available, so cited as TODO(spec:)
// prose-only — these strings are invisible to speccheck and document
// what would be done if the PDF were loaded):
//
//   - TODO(spec: TS 33.117 clause 4.2.3) Audit log schema — currently we
//     keep event_type / severity / source_ip / imsi / detail / extra_json,
//     which covers the catalogue but does not yet emit cryptographically
//     chained / signed records as TS 33.117 envisions for compliance.
//   - TODO(spec: TS 33.117 clause 4.3) Hardening checklist — interface
//     lockdown, default-deny posture, password policy: handled at
//     deployment, not in this package.
//
// All firewall rules, IDS signatures, blocked IPs, and known-gNB roster
// are persisted to db/schemas/security.go tables; the in-memory caches
// here are warmed from those tables at process start.
package core_security

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ───────────────────────── Audit Log ─────────────────────────────
//
// Spec anchor: TS 33.501 §5.9.4 — the spec requires that signalling
// monitoring data be collected at well-defined points; we keep it in
// security_audit_log so the IDS surface, the rate limiter, and the
// firewall guards all share one append-only stream.

// LogEvent appends a security event to the audit trail.
//
// severity must be one of DEBUG/INFO/WARNING/ERROR/CRITICAL (enforced by
// the DB CHECK constraint). Empty severity defaults to INFO.
func LogEvent(eventType, detail, sourceIP, imsi, severity string, extra map[string]interface{}) {
	if severity == "" {
		severity = "INFO"
	}
	extraJSON := ""
	if extra != nil {
		b, _ := json.Marshal(extra)
		extraJSON = string(b)
	}
	_, _ = engine.Exec(`INSERT INTO security_audit_log
		(event_type, severity, source_ip, imsi, detail, extra_json)
		VALUES (?,?,?,?,?,?)`,
		eventType, severity, sourceIP, imsi, detail, extraJSON)
}

// GetAuditLog returns the most recent audit events (newest first).
func GetAuditLog(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	return qRows("SELECT * FROM security_audit_log ORDER BY created_at DESC, id DESC LIMIT ?", limit)
}

// ───────────────────────── Rate Limiter ──────────────────────────
//
// Spec anchor: TS 33.501 §5.9.4 — flood detection is a §5.9.4 use case;
// the buckets here are the in-memory state used by DetectIntrusion and
// every interface guard that opts into rate limiting.

type rateBucket struct {
	count   int
	resetAt time.Time
}

var (
	rlMu      sync.Mutex
	rlBuckets = make(map[string]*rateBucket)
)

// CheckRateLimit returns true if the key is still within budget for the
// current window. A `false` return causes a RATE_LIMITED audit event.
func CheckRateLimit(key string, maxPerWindow int, windowSec int) bool {
	rlMu.Lock()
	defer rlMu.Unlock()
	now := time.Now()
	b := rlBuckets[key]
	if b == nil || now.After(b.resetAt) {
		rlBuckets[key] = &rateBucket{count: 1, resetAt: now.Add(time.Duration(windowSec) * time.Second)}
		return true
	}
	b.count++
	if b.count > maxPerWindow {
		LogEvent("RATE_LIMITED", fmt.Sprintf("key=%s count=%d", key, b.count), key, "", "WARNING", nil)
		return false
	}
	return true
}

// ResetRateLimits clears every bucket — used by tests and by ops when
// rolling out a new policy.
func ResetRateLimits() {
	rlMu.Lock()
	rlBuckets = make(map[string]*rateBucket)
	rlMu.Unlock()
}

// ───────────────────────── Firewall ──────────────────────────────
//
// Spec anchors:
//   - TS 33.501 §9.2 (N2/NGAP) — gNB→AMF authorisation requires that the
//     source IP be in the known-gNB roster.
//   - TS 33.501 §9.3 (N3/GTP-U) — UP traffic is integrity/replay protected
//     end-to-end; the firewall here enforces the IP-perimeter slice of that
//     (oversize, TEID-zero, and explicit deny rules).
//   - TS 33.501 §9.9 — non-SBA inter-PLMN interfaces (N4/N9) follow the
//     same allow-list model.

var (
	sfMu        sync.Mutex
	knownGnBIPs = make(map[string]string) // ip -> gnb_id
	blockedIPs  = make(map[string]string) // ip -> reason
)

// FirewallRule mirrors a row from security_firewall_rules.
//
// Protocol must be one of ngap/nas/gtpu/sbi/any; Action one of
// allow/deny/rate_limit (enforced by DB CHECK constraints).
type FirewallRule struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Protocol  string `json:"protocol"`
	Action    string `json:"action"`
	SrcCIDR   string `json:"src_cidr"`
	DstCIDR   string `json:"dst_cidr"`
	PortRange string `json:"port_range"`
	RateLimit int    `json:"rate_limit"`
	WindowS   int    `json:"window_s"`
	Enabled   bool   `json:"enabled"`
	Priority  int    `json:"priority"`
}

// UpsertFirewallRule persists a rule (insert or update by name).
func UpsertFirewallRule(r FirewallRule) error {
	if r.Name == "" {
		return fmt.Errorf("firewall rule name required")
	}
	if !validProtocol(r.Protocol) {
		return fmt.Errorf("invalid protocol %q (want ngap|nas|gtpu|sbi|any)", r.Protocol)
	}
	if !validAction(r.Action) {
		return fmt.Errorf("invalid action %q (want allow|deny|rate_limit)", r.Action)
	}
	if err := ValidateCIDR(r.SrcCIDR); err != nil {
		return fmt.Errorf("src_cidr: %w", err)
	}
	if err := ValidateCIDR(r.DstCIDR); err != nil {
		return fmt.Errorf("dst_cidr: %w", err)
	}
	if err := ValidatePortRange(r.PortRange); err != nil {
		return fmt.Errorf("port_range: %w", err)
	}
	if r.RateLimit < 0 || r.WindowS < 0 {
		return fmt.Errorf("rate_limit and window_s must be >= 0")
	}
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	_, err := engine.Exec(`INSERT INTO security_firewall_rules
		(name, protocol, action, src_cidr, dst_cidr, port_range, rate_limit, window_s, enabled, priority, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?, datetime('now'))
		ON CONFLICT(name) DO UPDATE SET
			protocol=excluded.protocol,
			action=excluded.action,
			src_cidr=excluded.src_cidr,
			dst_cidr=excluded.dst_cidr,
			port_range=excluded.port_range,
			rate_limit=excluded.rate_limit,
			window_s=excluded.window_s,
			enabled=excluded.enabled,
			priority=excluded.priority,
			updated_at=datetime('now')`,
		r.Name, r.Protocol, r.Action, r.SrcCIDR, r.DstCIDR, r.PortRange,
		r.RateLimit, r.WindowS, enabled, r.Priority)
	return err
}

// ListFirewallRules returns rules ordered by priority then name.
func ListFirewallRules() ([]FirewallRule, error) {
	rows, err := qRows("SELECT id, name, protocol, action, src_cidr, dst_cidr, port_range, rate_limit, window_s, enabled, priority FROM security_firewall_rules ORDER BY priority ASC, name ASC")
	if err != nil {
		return nil, err
	}
	out := make([]FirewallRule, 0, len(rows))
	for _, m := range rows {
		out = append(out, FirewallRule{
			ID:        asInt64(m["id"]),
			Name:      asString(m["name"]),
			Protocol:  asString(m["protocol"]),
			Action:    asString(m["action"]),
			SrcCIDR:   asString(m["src_cidr"]),
			DstCIDR:   asString(m["dst_cidr"]),
			PortRange: asString(m["port_range"]),
			RateLimit: int(asInt64(m["rate_limit"])),
			WindowS:   int(asInt64(m["window_s"])),
			Enabled:   asInt64(m["enabled"]) == 1,
			Priority:  int(asInt64(m["priority"])),
		})
	}
	return out, nil
}

// DeleteFirewallRule removes a rule by name. Returns true if removed.
func DeleteFirewallRule(name string) bool {
	res, err := engine.Exec("DELETE FROM security_firewall_rules WHERE name=?", name)
	if err != nil || res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// RegisterKnownGnB adds (or refreshes) a gNB to the known-source roster.
// Persisted to security_known_gnbs.
func RegisterKnownGnB(ip, gnbID string) {
	if ip == "" {
		return
	}
	sfMu.Lock()
	knownGnBIPs[ip] = gnbID
	sfMu.Unlock()
	_, _ = engine.Exec(`INSERT INTO security_known_gnbs (ip, gnb_id, added_at, added_by)
		VALUES (?,?, datetime('now'), 'system')
		ON CONFLICT(ip) DO UPDATE SET gnb_id=excluded.gnb_id`, ip, gnbID)
}

// UnregisterGnB removes a gNB from the roster.
func UnregisterGnB(ip string) {
	sfMu.Lock()
	delete(knownGnBIPs, ip)
	sfMu.Unlock()
	_, _ = engine.Exec("DELETE FROM security_known_gnbs WHERE ip=?", ip)
}

// BlockIP adds an IP to the deny list with a reason. Persisted.
func BlockIP(ip, reason string) {
	if ip == "" {
		return
	}
	sfMu.Lock()
	blockedIPs[ip] = reason
	sfMu.Unlock()
	_, _ = engine.Exec(`INSERT INTO security_blocked_ips (ip, reason, added_at, added_by)
		VALUES (?,?, datetime('now'), 'system')
		ON CONFLICT(ip) DO UPDATE SET reason=excluded.reason`, ip, reason)
	LogEvent("IP_BLOCKED", reason, ip, "", "WARNING", nil)
}

// UnblockIP removes an IP from the deny list.
func UnblockIP(ip string) {
	sfMu.Lock()
	delete(blockedIPs, ip)
	sfMu.Unlock()
	_, _ = engine.Exec("DELETE FROM security_blocked_ips WHERE ip=?", ip)
	LogEvent("IP_UNBLOCKED", "", ip, "", "INFO", nil)
}

// IsBlocked reports whether an IP is on the deny list. TTL'd entries
// whose expires_at has passed are pruned lazily here so the next
// caller sees the correct state even if SweepBlockedIPs() hasn't
// run since the deadline.
func IsBlocked(ip string) bool {
	sfMu.Lock()
	defer sfMu.Unlock()
	if _, ok := blockedIPs[ip]; !ok {
		return false
	}
	// Hot-path check against expires_at — keep the read off the DB
	// when no TTL is set on the row.
	row, _ := qRows("SELECT expires_at FROM security_blocked_ips WHERE ip=?", ip)
	if len(row) == 0 {
		// Row vanished out from under us — the in-memory cache is stale.
		delete(blockedIPs, ip)
		return false
	}
	exp := asString(row[0]["expires_at"])
	if exp == "" {
		return true // permanent block
	}
	t, err := time.Parse(time.RFC3339, exp)
	if err != nil {
		return true // bad format → treat as permanent (don't accidentally unblock)
	}
	if time.Now().UTC().After(t) {
		delete(blockedIPs, ip)
		_, _ = engine.Exec("DELETE FROM security_blocked_ips WHERE ip=?", ip)
		LogEvent("IP_UNBLOCKED", "ttl expired", ip, "", "INFO", nil)
		return false
	}
	return true
}

// CheckSignallingAccess is the single entry point for signalling-plane
// IP authorisation. Returns (allowed, reason).
//
// Spec anchor: TS 33.501 §5.9.1 — "trust boundary" check.
func CheckSignallingAccess(sourceIP string) (bool, string) {
	sfMu.Lock()
	reason, blocked := blockedIPs[sourceIP]
	sfMu.Unlock()
	if blocked {
		LogEvent("BLOCKED_IP", "Connection from blocked IP: "+reason, sourceIP, "", "WARNING", nil)
		return false, "IP is blocked: " + reason
	}
	return true, ""
}

// LoadPersistedState rehydrates the in-memory caches from the database.
// Called at process start and by tests after wiping the DB.
func LoadPersistedState() error {
	gnbRows, err := qRows("SELECT ip, gnb_id FROM security_known_gnbs")
	if err != nil {
		return err
	}
	blockRows, err := qRows("SELECT ip, reason FROM security_blocked_ips")
	if err != nil {
		return err
	}
	sfMu.Lock()
	knownGnBIPs = make(map[string]string, len(gnbRows))
	for _, r := range gnbRows {
		knownGnBIPs[asString(r["ip"])] = asString(r["gnb_id"])
	}
	blockedIPs = make(map[string]string, len(blockRows))
	for _, r := range blockRows {
		blockedIPs[asString(r["ip"])] = asString(r["reason"])
	}
	sfMu.Unlock()
	return nil
}

// ───────────────────────── NAS Guard ─────────────────────────────
//
// Spec anchor: TS 33.501 §5.9.4 — replay / integrity anomalies are
// signalling-monitoring events. Full per-UE NAS sequence tracking lives
// in nf/amf/security; this surface only exposes the IDS-side hook.

// CheckNASReplay is a stub in this package — the authoritative per-UE
// NAS COUNT tracking lives in nf/amf/security (RxNAS / TxDL); the IDS
// surface only consumes the event when AMF reports a replay.
//
// TODO(spec: TS 33.501 §6.4) Wire DetectIntrusion("NAS_REPLAY", ...) at
// the AMF rejection site once the AMF surfaces replay rejects on a bus.
func CheckNASReplay(imsi string, seqNum int, direction string) bool {
	return true
}

// ValidateNASIntegrity is a placeholder — actual MAC-I check lives at
// nf/amf/security.RxNAS. The hook here lets the IDS escalate when AMF
// reports an integrity failure.
func ValidateNASIntegrity(nasPDU []byte, knasint []byte) bool {
	if len(knasint) == 0 {
		return true
	}
	return true
}

// ───────────────────────── NGAP Guard ────────────────────────────
//
// Spec anchor: TS 33.501 §9.2 — N2/NGAP traffic must come from an
// authorised gNB (this surface is the IP-perimeter check; mutual TLS /
// IPsec is the cryptographic layer below).

// CheckNGAPSource returns true if the source IP is a known gNB.
func CheckNGAPSource(sourceIP string) bool {
	sfMu.Lock()
	_, ok := knownGnBIPs[sourceIP]
	sfMu.Unlock()
	if !ok {
		LogEvent("UNKNOWN_GNB", "NGAP from unknown source", sourceIP, "", "WARNING", nil)
	}
	return ok
}

// ───────────────────────── GTP-U Guard ───────────────────────────
//
// Spec anchor: TS 33.501 §9.3 — N3 user-plane integrity and replay
// protection. We enforce the perimeter slice (TEID validity, oversize
// guard); cryptographic UP protection per TS 23.501 §5.10.3 is enforced
// at the UPF dataplane.
//
// TODO(spec: TS 23.501 §5.10.3) Pull per-PDU-session UP-security policy
// (integrity-required / confidentiality-required) into this guard so an
// oversize frame on an integrity-required session is treated as CRITICAL
// rather than WARNING.

// CheckGTPUPacket validates a GTP-U frame at the IP perimeter.
func CheckGTPUPacket(teid uint32, sourceIP, destIP string, packetSize int) bool {
	if teid == 0 {
		LogEvent("GTPU_BAD_TEID", "TEID=0", sourceIP, "", "WARNING", nil)
		return false
	}
	if packetSize > 9000 {
		LogEvent("GTPU_OVERSIZED", fmt.Sprintf("teid=0x%08X size=%d", teid, packetSize), sourceIP, "", "WARNING", nil)
		return false
	}
	return true
}

// ───────────────────────── IDS ───────────────────────────────────
//
// Spec anchor: TS 33.501 §5.9.4 — signalling monitoring requirements.
// Signatures are persisted in security_ids_signatures so the operator
// can extend the catalogue without redeploying.

// IDSSignature mirrors a row from security_ids_signatures.
//
// AutoBlockTTLS > 0 turns a signature trip into a deny-list entry
// for the source IP, expiring after the TTL — TS 33.501 §5.9.4
// "escalated event" behaviour.
type IDSSignature struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Pattern        string `json:"pattern"`
	Severity       string `json:"severity"`
	Threshold      int    `json:"threshold"`
	WindowS        int    `json:"window_s"`
	Enabled        bool   `json:"enabled"`
	AutoBlockTTLS  int    `json:"auto_block_ttl_s"`
}

// UpsertIDSSignature inserts or updates a signature by name.
func UpsertIDSSignature(s IDSSignature) error {
	if s.Name == "" || s.Pattern == "" {
		return fmt.Errorf("name and pattern required")
	}
	if s.Severity == "" {
		s.Severity = "WARNING"
	}
	if !validIDSSeverity(s.Severity) {
		return fmt.Errorf("invalid severity %q", s.Severity)
	}
	if s.Threshold <= 0 {
		s.Threshold = 1
	}
	if s.WindowS <= 0 {
		s.WindowS = 60
	}
	enabled := 0
	if s.Enabled {
		enabled = 1
	}
	if s.AutoBlockTTLS < 0 {
		return fmt.Errorf("auto_block_ttl_s must be >= 0")
	}
	_, err := engine.Exec(`INSERT INTO security_ids_signatures
		(name, pattern, severity, threshold, window_s, enabled,
		 auto_block_ttl_s, updated_at)
		VALUES (?,?,?,?,?,?,?, datetime('now'))
		ON CONFLICT(name) DO UPDATE SET
			pattern=excluded.pattern,
			severity=excluded.severity,
			threshold=excluded.threshold,
			window_s=excluded.window_s,
			enabled=excluded.enabled,
			auto_block_ttl_s=excluded.auto_block_ttl_s,
			updated_at=datetime('now')`,
		s.Name, s.Pattern, s.Severity, s.Threshold, s.WindowS, enabled,
		s.AutoBlockTTLS)
	return err
}

// ListIDSSignatures returns enabled-first, then by name.
func ListIDSSignatures() ([]IDSSignature, error) {
	rows, err := qRows(`SELECT id, name, pattern, severity, threshold,
		window_s, enabled, auto_block_ttl_s
		FROM security_ids_signatures
		ORDER BY enabled DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	out := make([]IDSSignature, 0, len(rows))
	for _, m := range rows {
		out = append(out, IDSSignature{
			ID:            asInt64(m["id"]),
			Name:          asString(m["name"]),
			Pattern:       asString(m["pattern"]),
			Severity:      asString(m["severity"]),
			Threshold:     int(asInt64(m["threshold"])),
			WindowS:       int(asInt64(m["window_s"])),
			Enabled:       asInt64(m["enabled"]) == 1,
			AutoBlockTTLS: int(asInt64(m["auto_block_ttl_s"])),
		})
	}
	return out, nil
}

// DeleteIDSSignature removes a signature by name. Returns true if removed.
func DeleteIDSSignature(name string) bool {
	res, err := engine.Exec("DELETE FROM security_ids_signatures WHERE name=?", name)
	if err != nil || res == nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// ListBlockedIPs returns the persisted blocked-IP list (newest first).
// Sweeps expired TTL entries before returning so the panel never
// shows a row that should already be gone.
func ListBlockedIPs() ([]map[string]interface{}, error) {
	SweepBlockedIPs()
	return qRows(`SELECT ip, reason, added_at, added_by, expires_at
		FROM security_blocked_ips ORDER BY added_at DESC`)
}

// ListKnownGnBs returns the persisted gNB allow-list (newest first).
func ListKnownGnBs() ([]map[string]interface{}, error) {
	return qRows("SELECT ip, gnb_id, added_at, added_by FROM security_known_gnbs ORDER BY added_at DESC")
}

// DetectIntrusion checks for known attack patterns and returns true
// if a signature has been *promoted to an alert* on this call (i.e.
// its sliding-window event count reached the configured threshold).
//
// Two channels:
//
//  1. Built-in classes (AUTH_FAILURE_BURST, SIGNALLING_FLOOD) keep
//     their rate-limit-style budget for backward compatibility with
//     existing call sites.
//  2. Persisted signatures (security_ids_signatures): each matching
//     event records into a per-(signature, sourceIP) sliding window;
//     when the count inside the window meets or exceeds threshold,
//     we emit the audit event AND (when auto_block_ttl_s > 0) add
//     the source IP to the deny list with that TTL.
//
// Every match — whether promoted or suppressed — bumps a per-
// signature raw hit counter so the panel can show "5/10 in window".
func DetectIntrusion(eventType, sourceIP, detail string) bool {
	switch eventType {
	case "AUTH_FAILURE_BURST":
		if !CheckRateLimit("auth_fail:"+sourceIP, 5, 60) {
			LogEvent("INTRUSION_DETECTED", "Authentication failure burst", sourceIP, "", "CRITICAL",
				map[string]interface{}{"type": "brute_force"})
			return true
		}
	case "SIGNALLING_FLOOD":
		if !CheckRateLimit("signal:"+sourceIP, 100, 10) {
			LogEvent("INTRUSION_DETECTED", "Signalling flood", sourceIP, "", "CRITICAL",
				map[string]interface{}{"type": "dos"})
			return true
		}
	}
	sigs, _ := ListIDSSignatures()
	promoted := false
	for _, s := range sigs {
		if !s.Enabled {
			continue
		}
		if !SignatureMatches(s, eventType, detail) {
			continue
		}
		bumpIDSHit(s.Name, sourceIP)
		window := time.Duration(s.WindowS) * time.Second
		if window <= 0 {
			window = 60 * time.Second
		}
		count := recordIDSEvent(s.Name, sourceIP, window)
		if count < s.Threshold {
			continue
		}
		bumpIDSAlert(s.Name, sourceIP)
		LogEvent("INTRUSION_DETECTED", "Signature hit: "+s.Name, sourceIP, "", s.Severity,
			map[string]interface{}{
				"signature":  s.Name,
				"count":      count,
				"threshold":  s.Threshold,
				"window_s":   s.WindowS,
			})
		if s.AutoBlockTTLS > 0 {
			BlockIPWithTTL(sourceIP,
				"IDS auto-block: "+s.Name,
				time.Duration(s.AutoBlockTTLS)*time.Second)
		}
		promoted = true
	}
	return promoted
}

// ───────────────────────── Default Policies ──────────────────────

// SecurityPolicy holds a per-protocol rate-limit policy.
type SecurityPolicy struct {
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	RateLimitReq    int    `json:"rate_limit_req_per_sec"`
	RateLimitWindow int    `json:"rate_limit_window_sec"`
	BlockOnFailure  int    `json:"block_on_failure_count"`
}

// DefaultPolicies are the conservative defaults applied when the rules
// table is empty. They mirror the TS 33.501 §9.x interface partition
// (NGAP/NAS on signalling, GTP-U on user plane).
func DefaultPolicies() []SecurityPolicy {
	return []SecurityPolicy{
		{Name: "ngap_signalling", Enabled: true, RateLimitReq: 100, RateLimitWindow: 10, BlockOnFailure: 10},
		{Name: "nas_auth", Enabled: true, RateLimitReq: 5, RateLimitWindow: 60, BlockOnFailure: 5},
		{Name: "gtpu_traffic", Enabled: true, RateLimitReq: 10000, RateLimitWindow: 1, BlockOnFailure: 0},
		{Name: "s1ap_signalling", Enabled: true, RateLimitReq: 100, RateLimitWindow: 10, BlockOnFailure: 10},
	}
}

// ───────────────────────── Status ────────────────────────────────

// Status returns counters for the GUI/OAM panel.
func Status() map[string]any {
	log := logger.Get("core_security")
	_ = log
	sfMu.Lock()
	gnbCount := len(knownGnBIPs)
	blockedCount := len(blockedIPs)
	sfMu.Unlock()
	rlMu.Lock()
	rlCount := len(rlBuckets)
	rlMu.Unlock()
	rules, _ := ListFirewallRules()
	sigs, _ := ListIDSSignatures()
	fwHits := FirewallHits()
	rawHits, alertHits := IDSHits()
	var fwHitTotal, idsHitTotal, idsAlertTotal int64
	for _, h := range fwHits {
		fwHitTotal += h.Count
	}
	for _, h := range rawHits {
		idsHitTotal += h.Count
	}
	for _, h := range alertHits {
		idsAlertTotal += h.Count
	}
	return map[string]any{
		"status":           "ready",
		"known_gnbs":       gnbCount,
		"blocked_ips":      blockedCount,
		"rate_buckets":     rlCount,
		"rules":            len(rules),
		"signatures":       len(sigs),
		"policies":         len(DefaultPolicies()),
		"firewall_hits":    fwHitTotal,
		"ids_hits":         idsHitTotal,
		"ids_alerts":       idsAlertTotal,
	}
}

// ───────────────────────── helpers ───────────────────────────────

func validProtocol(p string) bool {
	switch p {
	case "ngap", "nas", "gtpu", "sbi", "any":
		return true
	}
	return false
}

func validAction(a string) bool {
	switch a {
	case "allow", "deny", "rate_limit":
		return true
	}
	return false
}

func validIDSSeverity(s string) bool {
	switch s {
	case "INFO", "WARNING", "ERROR", "CRITICAL":
		return true
	}
	return false
}

func asString(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	}
	return fmt.Sprintf("%v", v)
}

func asInt64(v interface{}) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case []byte:
		var n int64
		fmt.Sscanf(string(x), "%d", &n)
		return n
	case string:
		var n int64
		fmt.Sscanf(x, "%d", &n)
		return n
	}
	return 0
}

func qRows(q string, args ...interface{}) ([]map[string]interface{}, error) {
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

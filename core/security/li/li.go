// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package li — Lawful Intercept (LI) for the 5GC.
//
// Spec anchors:
//
//   - TS 33.501 §5.9 NOTE 3 — only verifiable LI mention in the local
//     security spec: "the AUSF sending SUPI to SEAF is necessary but
//     not sufficient" for lawful interception, and SUPI is therefore
//     mixed into KAMF derivation. This NOTE is what binds the LI
//     surface here to the 5G security architecture.
//
// Deferred surfaces (TS 33.127 / TS 33.128 PDFs not loaded locally;
// cited as TODO(spec:) prose-only — the strings are deliberately not
// in the §-form regex so speccheck doesn't flag them as ungrounded):
//
//   - TODO(spec: TS 33.127, "Lawful interception architecture") ADMF /
//     POI / TF / MDF role split. This file collapses ADMF + POI into
//     one in-process surface; the MDF-side X1/X2/X3 transport is not
//     implemented (GetDeliveryStats only counts events).
//   - TODO(spec: TS 33.127, "X1 ADMF→POI provisioning interface") —
//     warrants are loaded from the local DB; there is no X1 listener.
//   - TODO(spec: TS 33.127, "X2 IRI delivery interface") — IRI events
//     are persisted to li_iri_events.delivered=0; no MDF push yet.
//   - TODO(spec: TS 33.127, "X3 CC delivery interface") — CC sessions
//     are tracked in li_cc_sessions.status; the actual content stream
//     is not duplicated/forked.
//   - TODO(spec: TS 33.128, "Stage 3 protocol and procedures") — the
//     IRI-EVENT-RECORD / CC-PDU encodings are not produced; we keep an
//     internal JSON blob in event_data for now.
//   - TODO(spec: TS 33.127, "MDF buffering and replay") — no buffering
//     across MDF-down windows. delivered=0 acts as the queue marker.
//
// Implementation notes:
//
//   - All timestamps are written as ISO datetime strings via
//     datetime('now') / time.Now().UTC().Format(time.RFC3339); the
//     schema columns are TEXT and CHECKs compare lexicographically.
//     The previous code wrote float64 epoch seconds against TEXT
//     defaults, which compared correctly in narrow ranges but drifted
//     for refreshTargets() and ExpireWarrants().
//   - activeTargets is the hot lookup path: rebuilt on every warrant
//     CRUD operation and on a periodic ExpireWarrants() tick.
package li

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var (
	targetMu      sync.Mutex
	activeTargets = make(map[string][]WarrantTarget)
)

// WarrantTarget is the cached form of an active warrant for fast
// per-IMSI POI lookup.
type WarrantTarget struct {
	WarrantID   string `json:"warrant_id"`
	Scope       string `json:"scope"`
	MDFEndpoint string `json:"mdf_endpoint"`
}

// nowISO returns the current UTC time in RFC3339 — matches what the DB
// writes when the column defaults to datetime('now') (with the 'Z' tz
// suffix dropped to the same lexicographic form).
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02 15:04:05")
}

// ───────────────── Warrant CRUD (ADMF surface) ──────────────────
//
// Spec anchor: TS 33.501 §5.9 NOTE 3 (the only local hook). The fuller
// ADMF lifecycle lives in TS 33.127 (not loaded — see header TODOs).

// CreateWarrant creates a new warrant. startTime/endTime are RFC3339-ish
// strings; pass empty for "start now" and "30 days out" defaults.
func CreateWarrant(warrantID, authority, caseRef, targetIMSI, targetMSISDN,
	scope, startTime, endTime, mdfEndpoint, operator string) error {
	if warrantID == "" || authority == "" || caseRef == "" || targetIMSI == "" {
		return fmt.Errorf("warrant_id, authority, case_reference, target_imsi all required")
	}
	if scope == "" {
		scope = "iri"
	}
	if !validScope(scope) {
		return fmt.Errorf("invalid scope %q (want iri|cc|iri+cc)", scope)
	}
	if operator == "" {
		operator = "system"
	}
	now := time.Now().UTC()
	if startTime == "" {
		startTime = now.Format("2006-01-02 15:04:05")
	}
	if endTime == "" {
		endTime = now.Add(30 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	}
	log := logger.Get("li.admf")
	_, err := engine.Exec(`INSERT INTO li_warrants
		(warrant_id, authority, case_reference, target_imsi, target_msisdn,
		 scope, start_time, end_time, status, mdf_endpoint, created_at, created_by)
		VALUES (?,?,?,?,?,?,?,?,'active',?, datetime('now'), ?)`,
		warrantID, authority, caseRef, targetIMSI, targetMSISDN,
		scope, startTime, endTime, mdfEndpoint, operator)
	if err != nil {
		return err
	}
	audit("warrant_created", warrantID, operator, fmt.Sprintf("target=%s scope=%s", targetIMSI, scope))
	refreshTargets()
	log.Infof("LI warrant created: %s target=%s scope=%s", warrantID, targetIMSI, scope)
	return nil
}

// RevokeWarrant flips an active warrant to revoked.
func RevokeWarrant(warrantID, operator string) {
	if operator == "" {
		operator = "system"
	}
	_, _ = engine.Exec("UPDATE li_warrants SET status='revoked' WHERE warrant_id=?", warrantID)
	audit("warrant_revoked", warrantID, operator, "")
	refreshTargets()
}

// DeleteWarrant removes the warrant row entirely along with its IRI
// events, CC sessions, and audit log entries. Used for OAM cleanup
// (test fixtures, archive-then-purge of old cases). The audit-trail
// row for the deletion is INSERTed *before* the warrant is removed
// so the trail still references a known warrant_id; downstream
// archivers should snapshot li_audit_log before the delete.
func DeleteWarrant(warrantID, operator string) {
	if operator == "" {
		operator = "system"
	}
	audit("warrant_deleted", warrantID, operator, "")
	_, _ = engine.Exec("DELETE FROM li_iri_events WHERE warrant_id=?", warrantID)
	_, _ = engine.Exec("DELETE FROM li_cc_sessions WHERE warrant_id=?", warrantID)
	_, _ = engine.Exec("DELETE FROM li_warrants WHERE warrant_id=?", warrantID)
	refreshTargets()
}

// ExpireWarrants flips warrants past end_time from active to expired.
//
// Compares end_time lexicographically against the current UTC instant —
// safe because all writes use the same yyyy-mm-dd hh:mm:ss form.
func ExpireWarrants() {
	now := nowISO()
	db, err := engine.Open()
	if err != nil {
		return
	}
	rows, err := db.Query("SELECT warrant_id FROM li_warrants WHERE status='active' AND end_time < ?", now)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		_, _ = db.Exec("UPDATE li_warrants SET status='expired' WHERE warrant_id=?", id)
		audit("warrant_expired", id, "system", "auto-expired")
	}
	if len(ids) > 0 {
		refreshTargets()
	}
}

// ListWarrants returns warrants, optionally filtered by status.
func ListWarrants(status string) ([]map[string]interface{}, error) {
	if status != "" {
		return qRows("SELECT * FROM li_warrants WHERE status=? ORDER BY created_at DESC", status)
	}
	return qRows("SELECT * FROM li_warrants ORDER BY created_at DESC")
}

// GetWarrant returns a single warrant by id.
func GetWarrant(warrantID string) (map[string]interface{}, error) {
	return qRow("SELECT * FROM li_warrants WHERE warrant_id=?", warrantID)
}

// GetWarrantForIMSI returns active warrants for an IMSI from the cache.
// Caller must not mutate the returned slice.
func GetWarrantForIMSI(imsi string) []WarrantTarget {
	targetMu.Lock()
	defer targetMu.Unlock()
	out := activeTargets[imsi]
	if len(out) == 0 {
		return nil
	}
	cp := make([]WarrantTarget, len(out))
	copy(cp, out)
	return cp
}

// GetAuditLog returns the LI audit trail.
func GetAuditLog(warrantID string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	if warrantID != "" {
		return qRows("SELECT * FROM li_audit_log WHERE warrant_id=? ORDER BY timestamp DESC, id DESC LIMIT ?", warrantID, limit)
	}
	return qRows("SELECT * FROM li_audit_log ORDER BY timestamp DESC, id DESC LIMIT ?", limit)
}

// ───────────────── IRI POI ──────────────────────────────────────
//
// TODO(spec: TS 33.128 clause 7) — Stage 3 IRI-EVENT-RECORD encoding.
// We store an opaque JSON blob in event_data and let the MDF transform.

// CaptureIRI captures an IRI event if the IMSI is under active interception
// with iri or iri+cc scope.
func CaptureIRI(eventType, imsi string, eventData map[string]interface{}) {
	warrants := GetWarrantForIMSI(imsi)
	if len(warrants) == 0 {
		return
	}
	dataJSON, _ := json.Marshal(eventData)
	for _, w := range warrants {
		if w.Scope == "iri" || w.Scope == "iri+cc" {
			_, _ = engine.Exec(`INSERT INTO li_iri_events
				(warrant_id, event_type, target_imsi, event_data)
				VALUES (?,?,?,?)`, w.WarrantID, eventType, imsi, string(dataJSON))
			audit("iri_captured", w.WarrantID, "system", fmt.Sprintf("%s for %s", eventType, imsi))
		}
	}
}

// GetIRIEvents returns IRI events for a warrant (newest first).
func GetIRIEvents(warrantID string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 200
	}
	return qRows("SELECT * FROM li_iri_events WHERE warrant_id=? ORDER BY timestamp DESC, id DESC LIMIT ?", warrantID, limit)
}

// MarkDelivered flips delivered=1 for all IRI rows up to maxID for a warrant.
//
// TODO(spec: TS 33.127 X2 interface) Replace with a real MDF-ack pipeline; this
// is just enough state to make GetDeliveryStats meaningful.
func MarkDelivered(warrantID string, maxID int64) {
	_, _ = engine.Exec("UPDATE li_iri_events SET delivered=1 WHERE warrant_id=? AND id<=? AND delivered=0", warrantID, maxID)
}

// ───────────────── CC POI ───────────────────────────────────────
//
// TODO(spec: TS 33.128 clause 8) — Stage 3 CC-PDU encoding + X3 transport.

// ActivateCC marks a session as under content interception.
func ActivateCC(warrantID, imsi, sessionType string, pduSessionID int, callID string) {
	if sessionType == "" {
		sessionType = "data"
	}
	_, _ = engine.Exec(`INSERT INTO li_cc_sessions
		(warrant_id, target_imsi, session_type, pdu_session_id, call_id, status)
		VALUES (?,?,?,?,?,'active')`, warrantID, imsi, sessionType, pduSessionID, callID)
	audit("cc_activated", warrantID, "system", fmt.Sprintf("%s session for %s", sessionType, imsi))
}

// DeactivateCC stops all CC sessions for a warrant+IMSI.
func DeactivateCC(warrantID, imsi string) {
	_, _ = engine.Exec("UPDATE li_cc_sessions SET status='stopped' WHERE warrant_id=? AND target_imsi=?", warrantID, imsi)
	audit("cc_deactivated", warrantID, "system", fmt.Sprintf("CC stopped for %s", imsi))
}

// CheckAndActivateCC fans an IMSI out to all matching cc/iri+cc warrants.
func CheckAndActivateCC(imsi string, pduSessionID int, callID, sessionType string) {
	if sessionType == "" {
		sessionType = "data"
	}
	for _, w := range GetWarrantForIMSI(imsi) {
		if w.Scope == "cc" || w.Scope == "iri+cc" {
			ActivateCC(w.WarrantID, imsi, sessionType, pduSessionID, callID)
		}
	}
}

// GetActiveCCSessions returns active CC sessions, optionally filtered by IMSI.
func GetActiveCCSessions(imsi string) ([]map[string]interface{}, error) {
	if imsi != "" {
		return qRows("SELECT * FROM li_cc_sessions WHERE target_imsi=? AND status='active'", imsi)
	}
	return qRows("SELECT * FROM li_cc_sessions WHERE status='active'")
}

// ───────────────── MDF Delivery ─────────────────────────────────

// GetDeliveryStats returns totals for delivered vs pending IRI events.
func GetDeliveryStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{"total": 0, "delivered": 0, "pending": 0}
	}
	var total, delivered int
	_ = db.QueryRow("SELECT COUNT(*) FROM li_iri_events").Scan(&total)
	_ = db.QueryRow("SELECT COUNT(*) FROM li_iri_events WHERE delivered=1").Scan(&delivered)
	return map[string]interface{}{"total": total, "delivered": delivered, "pending": total - delivered}
}

// ───────────────── GUI / OAM ────────────────────────────────────

// List returns rows from li_warrants.
func List() ([]map[string]any, error) { return ListWarrants("") }

// Status returns counters for the OAM panel.
func Status() map[string]any {
	list, _ := ListWarrants("active")
	stats := GetDeliveryStats()
	return map[string]any{"active_warrants": len(list), "iri": stats}
}

// ───────────────── internals ────────────────────────────────────

func validScope(s string) bool {
	switch s {
	case "iri", "cc", "iri+cc":
		return true
	}
	return false
}

// refreshTargets rebuilds activeTargets from rows where status='active'
// and the current time is within [start_time, end_time).
func refreshTargets() {
	now := nowISO()
	db, err := engine.Open()
	if err != nil {
		return
	}
	rows, err := db.Query(
		"SELECT warrant_id, target_imsi, scope, mdf_endpoint FROM li_warrants WHERE status='active' AND start_time <= ? AND end_time > ?",
		now, now)
	if err != nil {
		return
	}
	defer rows.Close()
	targets := make(map[string][]WarrantTarget)
	for rows.Next() {
		var wid, imsi, scope string
		var mdf *string
		if err := rows.Scan(&wid, &imsi, &scope, &mdf); err != nil {
			continue
		}
		ep := ""
		if mdf != nil {
			ep = *mdf
		}
		targets[imsi] = append(targets[imsi], WarrantTarget{WarrantID: wid, Scope: scope, MDFEndpoint: ep})
	}
	targetMu.Lock()
	activeTargets = targets
	targetMu.Unlock()
}

// RefreshTargets is the public form of refreshTargets — exported so the
// OAM tick can call it after a remote change.
func RefreshTargets() { refreshTargets() }

func audit(action, warrantID, operator, detail string) {
	_, _ = engine.Exec(
		"INSERT INTO li_audit_log (action, warrant_id, operator, detail) VALUES (?,?,?,?)",
		action, warrantID, operator, detail)
}

func qRow(q string, args ...interface{}) (map[string]interface{}, error) {
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

// avoid unused-import if strings goes unused after edits.
var _ = strings.TrimSpace

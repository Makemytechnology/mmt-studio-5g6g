// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package mbs — 5G Multicast / Broadcast Services control plane.
//
// Operator surface for MBS sessions (multicast/broadcast), MBS service
// areas, member management, and the content-delivery audit log. The
// AMF / SMF / MB-SMF wire fan-out lives elsewhere; this package owns
// the operator-of-record data + lifecycle gating.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.247 §4.1   5G MBS architecture (umbrella).
//   - TS 23.247 §4.2   MBS reference points (N6mb, MBSF, MBSU, MB-UPF).
//   - TS 23.247 §7     MBS Session Procedures — Create / Activate /
//                      Deactivate / Release lifecycle.
//   - TS 23.247 §7.2   MBS service-area handling (TAI-list scoping).
//   - TS 22.146 / 22.246  MBMS / MBS service requirements (umbrella).
//
// Deferred:
//
//   - TS 23.247 §6     MB-SMF / MBSF / MBSTF / MB-UPF wire-protocol
//                      details — out of scope for this CRUD surface.
//   - TS 23.247 §8     Charging — MBS-CDR is an SBI concern, not here.
package mbs

import (
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Status / session_type vocabularies — mirror the CHECK constraints
// in db/schemas/domains.go::MbsDDL.
const (
	StatusCreated     = "created"
	StatusActivated   = "activated"
	StatusDeactivated = "deactivated"

	TypeMulticast = "multicast"
	TypeBroadcast = "broadcast"
)

var (
	validStatuses = map[string]bool{
		StatusCreated: true, StatusActivated: true, StatusDeactivated: true,
	}
	validTypes = map[string]bool{TypeMulticast: true, TypeBroadcast: true}
)

// ─── Service Areas (TS 23.247 §7.2) ──────────────────────────────

// CreateArea registers an MBS service area with a TAI list (comma-
// separated string). UNIQUE(name) on the schema makes this a
// no-conflict UPSERT path; we surface the duplicate as an error.
func CreateArea(name, trackingAreas, description string) (map[string]interface{}, error) {
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if trackingAreas == "" {
		return nil, fmt.Errorf("tracking_areas is required")
	}
	if err := ValidateTAIList(trackingAreas); err != nil {
		return nil, err
	}
	res, err := engine.Exec(
		`INSERT INTO mbs_areas (name, tracking_areas, description)
		 VALUES (?,?,?)`, name, trackingAreas, description)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	logger.Get("mbs").Infof("MBS area created: id=%d name=%s", id, name)
	return getArea(id)
}

func getArea(id int64) (map[string]interface{}, error) {
	return qRow("SELECT * FROM mbs_areas WHERE id=?", id)
}

// ListAreas returns all MBS service areas (newest last by id ASC).
func ListAreas() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM mbs_areas ORDER BY id")
}

// DeleteArea removes an MBS service area. The FK on mbs_sessions is
// ON DELETE SET NULL so existing sessions stay intact (their area_id
// becomes NULL).
func DeleteArea(id int64) error {
	_, err := engine.Exec("DELETE FROM mbs_areas WHERE id=?", id)
	return err
}

// ─── Session CRUD (TS 23.247 §7) ─────────────────────────────────

// CreateSession creates an MBS session in the 'created' state. AreaID
// is an optional integer FK to mbs_areas.id; pass nil for unscoped.
func CreateSession(tmgi, name, sessionType string, qos5QI int,
	areaID *int64, maxBitrateKbps int) (map[string]interface{}, error) {

	if tmgi == "" {
		return nil, fmt.Errorf("tmgi is required")
	}
	if err := ValidateTMGI(tmgi); err != nil {
		return nil, err
	}
	if sessionType == "" {
		sessionType = TypeMulticast
	}
	if !validTypes[sessionType] {
		return nil, fmt.Errorf("invalid session_type %q (must be multicast|broadcast)", sessionType)
	}
	if qos5QI <= 0 {
		qos5QI = 9
	}
	if err := Validate5QI(qos5QI); err != nil {
		return nil, err
	}
	if err := ValidateBitrate(maxBitrateKbps); err != nil {
		return nil, err
	}
	res, err := engine.Exec(
		`INSERT INTO mbs_sessions
		 (tmgi, name, session_type, qos_5qi, area_id, max_bitrate_kbps)
		 VALUES (?,?,?,?,?,?)`,
		tmgi, name, sessionType, qos5QI, areaID, maxBitrateKbps)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	logger.Get("mbs").Infof(
		"MBS session created: id=%d tmgi=%s type=%s",
		id, tmgi, sessionType)
	return getSession(id)
}

func getSession(id int64) (map[string]interface{}, error) {
	return qRow("SELECT * FROM mbs_sessions WHERE id=?", id)
}

// GetSession returns one session row, or nil if absent.
func GetSession(id int64) (map[string]interface{}, error) { return getSession(id) }

// ActivateSession flips created → activated and stamps activated_at.
// TS 23.247 §7 — only sessions in 'created' may be activated.
func ActivateSession(sessionID int64) (map[string]interface{}, error) {
	s, _ := getSession(sessionID)
	if s == nil {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	if fmt.Sprintf("%v", s["status"]) != StatusCreated {
		return nil, fmt.Errorf("cannot activate session in state '%v'", s["status"])
	}
	if _, err := engine.Exec(
		"UPDATE mbs_sessions SET status='activated', activated_at=datetime('now') WHERE id=?",
		sessionID); err != nil {
		return nil, err
	}
	return getSession(sessionID)
}

// DeactivateSession flips activated → deactivated.
func DeactivateSession(sessionID int64) (map[string]interface{}, error) {
	s, _ := getSession(sessionID)
	if s == nil {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	if fmt.Sprintf("%v", s["status"]) != StatusActivated {
		return nil, fmt.Errorf("cannot deactivate session in state '%v'", s["status"])
	}
	if _, err := engine.Exec(
		"UPDATE mbs_sessions SET status='deactivated' WHERE id=?",
		sessionID); err != nil {
		return nil, err
	}
	return getSession(sessionID)
}

// ListSessions returns all MBS sessions, optionally filtered by
// session_type and/or status. Each row carries a `member_count`
// computed via correlated subquery so the GUI can render counts
// without a second round-trip.
func ListSessions(sessionType, status string) ([]map[string]interface{}, error) {
	q := `SELECT s.*,
	          (SELECT COUNT(*) FROM mbs_members m
	           WHERE m.session_id=s.id AND m.left_at IS NULL) AS member_count
	      FROM mbs_sessions s WHERE 1=1`
	var args []interface{}
	if sessionType != "" {
		q += " AND s.session_type=?"
		args = append(args, sessionType)
	}
	if status != "" {
		q += " AND s.status=?"
		args = append(args, status)
	}
	q += " ORDER BY s.id DESC"
	return qRows(q, args...)
}

// DeleteSession removes an MBS session and (via FK ON DELETE CASCADE)
// its members and content-log rows.
func DeleteSession(sessionID int64) error {
	_, err := engine.Exec("DELETE FROM mbs_sessions WHERE id=?", sessionID)
	return err
}

// ─── Member Management (multicast) ───────────────────────────────

// JoinSession adds a UE to a multicast session. Idempotent — re-
// joining the same IMSI is a no-op (UNIQUE(session_id, imsi)).
func JoinSession(sessionID int64, imsi string) error {
	if imsi == "" {
		return fmt.Errorf("imsi is required")
	}
	s, _ := getSession(sessionID)
	if s == nil {
		return fmt.Errorf("session %d not found", sessionID)
	}
	_, err := engine.Exec(
		`INSERT OR IGNORE INTO mbs_members (session_id, imsi)
		 VALUES (?, ?)`, sessionID, imsi)
	return err
}

// LeaveSession marks a UE as having left (sets left_at to now);
// the row stays for audit. Idempotent across repeat calls.
func LeaveSession(sessionID int64, imsi string) error {
	_, err := engine.Exec(
		`UPDATE mbs_members SET left_at=datetime('now')
		 WHERE session_id=? AND imsi=? AND left_at IS NULL`,
		sessionID, imsi)
	return err
}

// ListMembers returns members of an MBS session (active first by
// joined_at).
func ListMembers(sessionID int64) ([]map[string]interface{}, error) {
	return qRows(
		`SELECT * FROM mbs_members WHERE session_id=?
		 ORDER BY (left_at IS NULL) DESC, joined_at`, sessionID)
}

// ─── Content Delivery (TS 23.247 §7) ─────────────────────────────

// SendContent records an immediate delivery to all currently-joined
// members. The payload itself is a wire-level concern (MB-UPF /
// MBSTF); we just persist the content metadata + recipient count.
func SendContent(sessionID int64, contentType string, contentSize int) (map[string]interface{}, error) {
	s, _ := getSession(sessionID)
	if s == nil {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	if fmt.Sprintf("%v", s["status"]) != StatusActivated {
		return nil, fmt.Errorf("session must be activated to send content (current: %v)", s["status"])
	}
	var recipients int
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	_ = db.QueryRow(
		`SELECT COUNT(*) FROM mbs_members
		 WHERE session_id=? AND left_at IS NULL`, sessionID).Scan(&recipients)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := engine.Exec(
		`INSERT INTO mbs_content_log
		 (session_id, content_type, content_size, scheduled_at,
		  delivered_at, recipients_count, status)
		 VALUES (?, ?, ?, ?, ?, ?, 'delivered')`,
		sessionID, contentType, contentSize, now, now, recipients)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return qRow("SELECT * FROM mbs_content_log WHERE id=?", id)
}

// ScheduleContent records a deferred delivery. The MBSTF would
// honour the `scheduled_at` clock and flip status='delivering' →
// 'delivered'; today the row is just an audit-log entry.
func ScheduleContent(sessionID int64, contentType string, contentSize int,
	deliverAt string) (map[string]interface{}, error) {
	s, _ := getSession(sessionID)
	if s == nil {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	if deliverAt == "" {
		return nil, fmt.Errorf("deliver_at is required")
	}
	res, err := engine.Exec(
		`INSERT INTO mbs_content_log
		 (session_id, content_type, content_size, scheduled_at, status)
		 VALUES (?, ?, ?, ?, 'pending')`,
		sessionID, contentType, contentSize, deliverAt)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return qRow("SELECT * FROM mbs_content_log WHERE id=?", id)
}

// ListContentLog returns the latest content-delivery rows for the
// global panel (most recent first).
func ListContentLog(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 200
	}
	return qRows(
		`SELECT * FROM mbs_content_log ORDER BY id DESC LIMIT ?`, limit)
}

// ─── Stats ───────────────────────────────────────────────────────

// GetStats returns the operator-dashboard counter set the GUI panel
// expects (templates/mbs.html).
func GetStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{}
	}
	var total, active, multi, broad, members, delivered int
	_ = db.QueryRow("SELECT COUNT(*) FROM mbs_sessions").Scan(&total)
	_ = db.QueryRow("SELECT COUNT(*) FROM mbs_sessions WHERE status='activated'").Scan(&active)
	_ = db.QueryRow("SELECT COUNT(*) FROM mbs_sessions WHERE session_type='multicast'").Scan(&multi)
	_ = db.QueryRow("SELECT COUNT(*) FROM mbs_sessions WHERE session_type='broadcast'").Scan(&broad)
	_ = db.QueryRow("SELECT COUNT(*) FROM mbs_members WHERE left_at IS NULL").Scan(&members)
	_ = db.QueryRow("SELECT COUNT(*) FROM mbs_content_log WHERE status='delivered'").Scan(&delivered)
	return map[string]interface{}{
		"total_sessions":      total,
		"active_sessions":     active,
		"multicast_sessions":  multi,
		"broadcast_sessions":  broad,
		"active_members":      members,
		"delivered_content":   delivered,
	}
}

// ─── GUI panel adapters ──────────────────────────────────────────

func List() ([]map[string]any, error) {
	all, err := ListSessions("", "")
	return toAny(all), err
}

func Status() map[string]any { return GetStats() }

// ─── helpers ─────────────────────────────────────────────────────

func toAny(in []map[string]interface{}) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, m := range in {
		out[i] = m
	}
	return out
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
	rows.Scan(ptrs...)
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
		rows.Scan(ptrs...)
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, nil
}

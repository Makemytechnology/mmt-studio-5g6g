// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// api.go — operator-API wrappers around the CHF charging-session
// lifecycle. The SMF-trigger surface (OnPDUSessionCreated /
// OnPDUSessionReleased) keys sessions by (imsi, pdu_session_id);
// the operator surface uses the synthetic `session_id` strings
// (`<imsi>-<pdu_session_id>`) the package already writes into
// `charging_sessions.session_id`.
//
// Spec anchors:
//
//   - TS 32.290 §6.2 — Nchf charging-data service (Convergent
//     Charging service operations).
//   - TS 32.291 §6.1   — Online charging session lifecycle
//     (Initial / Update / Termination correspond to Create /
//     Interim / Release here).
//   - TS 32.291 §6.1.3 — Charging-Data Request / Response with
//     Multiple Unit Information (volume / time / event metering).
//
// Returned shapes are panel-friendly map[string]any so the route
// layer can pass them straight through `jsonReply`.
package chf

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ── Charging session lifecycle ──────────────────────────────────

// CreateChargingSession opens a session and returns the synthetic
// `session_id` the rest of the API uses to look it up.
//
// `pduSessionID` is optional; when 0 we synthesise a unique value
// from the current Unix nanos so the session_id stays distinct
// across operator-driven smoke runs that share an IMSI.
//
// `chargingMethod` ∈ {online, offline}; default offline.
func CreateChargingSession(imsi, serviceName, chargingMethod string,
	pduSessionID int) (map[string]any, error) {
	if imsi == "" {
		return nil, fmt.Errorf("imsi required")
	}
	if chargingMethod == "" {
		chargingMethod = "offline"
	}
	if chargingMethod != "online" && chargingMethod != "offline" {
		return nil, fmt.Errorf(
			"charging_method must be online or offline (TS 32.291 §6.1)")
	}
	if pduSessionID == 0 {
		pduSessionID = int(time.Now().UnixNano() % 1_000_000_000)
	}
	if serviceName == "" {
		serviceName = "default"
	}

	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	sessionID := fmt.Sprintf("%s-%d", imsi, pduSessionID)
	now := time.Now().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT OR REPLACE INTO charging_sessions
		(imsi, session_id, service_name, charging_method, status,
		 created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?)`,
		imsi, sessionID, serviceName, chargingMethod, now, now,
	); err != nil {
		return nil, err
	}

	if chargingMethod == "online" {
		if allowed, balance := CheckBalance(imsi, 0); !allowed {
			// Roll back so the session doesn't linger as 'active'.
			_, _ = db.Exec(
				`UPDATE charging_sessions SET status='released',
				 released_at=?, updated_at=? WHERE session_id=?`,
				now, now, sessionID)
			return nil, fmt.Errorf(
				"online charging rejected: insufficient balance (%.2f)",
				balance)
		}
	}

	return GetChargingSession(sessionID)
}

// UpdateChargingSession applies an interim update (TS 32.291 §6.1
// "Update Charging Data Request"). usageVolUL/DL are accumulated
// onto the cumulative totals; durationSec is added; usedUnits is
// stamped (overwrite — represents the running used_units).
//
// Returns the updated row so the caller can reflect cumulative
// totals back to the requester immediately.
func UpdateChargingSession(sessionID string, usageVolUL, usageVolDL int64,
	durationSec int, usedUnits int64) (map[string]any, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	now := time.Now().Format(time.RFC3339)
	res, err := db.Exec(`UPDATE charging_sessions SET
		total_volume_ul = total_volume_ul + ?,
		total_volume_dl = total_volume_dl + ?,
		total_duration  = total_duration + ?,
		used_units      = COALESCE(?, used_units),
		status          = 'interim',
		updated_at      = ?
		WHERE session_id=? AND status IN ('active','interim')`,
		usageVolUL, usageVolDL, durationSec, sql.NullInt64{
			Int64: usedUnits, Valid: usedUnits > 0,
		}, now, sessionID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("session %q not active", sessionID)
	}
	return GetChargingSession(sessionID)
}

// ReleaseChargingSession closes the session and emits the final
// CDR (TS 32.291 §6.1 "Termination Charging Data Request"). It
// looks up the (imsi, pdu_session_id) tuple from the synthetic
// session_id and dispatches to OnPDUSessionReleased so the same
// CDR-emission path is shared with the SMF trigger.
//
// `finalVolUL/DL/duration` are added before the final CDR is cut.
func ReleaseChargingSession(sessionID string,
	finalVolUL, finalVolDL int64, finalDuration int) (map[string]any, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	// Apply the final-usage delta on the way out so the CDR's volume
	// counters reflect the close-of-session report.
	if finalVolUL > 0 || finalVolDL > 0 || finalDuration > 0 {
		_, _ = UpdateChargingSession(sessionID,
			finalVolUL, finalVolDL, finalDuration, 0)
	}
	imsi, pduID, err := splitSessionID(sessionID)
	if err != nil {
		return nil, err
	}
	OnPDUSessionReleased(imsi, pduID)
	row, _ := GetChargingSession(sessionID)
	if row == nil {
		// Session row was already released or never existed.
		return map[string]any{
			"session_id": sessionID, "status": "released",
		}, nil
	}
	return row, nil
}

// GetChargingSession returns one charging-session row.
func GetChargingSession(sessionID string) (map[string]any, error) {
	rows, err := qChargingRows(`SELECT * FROM charging_sessions
		WHERE session_id=? LIMIT 1`, sessionID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// ListChargingSessions returns active + recent sessions, optionally
// filtered by status. status="" returns all; limit ≤ 0 → 200.
func ListChargingSessions(status string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT * FROM charging_sessions`
	args := []any{}
	if status != "" {
		q += ` WHERE status=?`
		args = append(args, status)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	return qChargingRows(q, args...)
}

// ── Stats ───────────────────────────────────────────────────────

// GetStats returns the panel header counters for /api/chf/stats.
func GetStats() map[string]any {
	out := map[string]any{
		"active_sessions": 0, "total_cdrs": 0, "active_quotas": 0,
		"rated_cdrs": 0, "pending_cdrs": 0,
	}
	db, err := engine.Open()
	if err != nil {
		return out
	}
	scan := func(q string, args ...any) int64 {
		var n int64
		_ = db.QueryRow(q, args...).Scan(&n)
		return n
	}
	out["active_sessions"] = scan(
		`SELECT COUNT(*) FROM charging_sessions WHERE status IN ('active','interim')`)
	out["total_cdrs"] = scan(`SELECT COUNT(*) FROM cdrs`)
	out["pending_cdrs"] = scan(
		`SELECT COUNT(*) FROM cdrs WHERE rating_status='pending'`)
	out["rated_cdrs"] = scan(`SELECT COUNT(*) FROM rated_cdrs`)
	out["active_quotas"] = scan(
		`SELECT COUNT(*) FROM quota_grants WHERE status='active'`)
	return out
}

// ── helpers ─────────────────────────────────────────────────────

// splitSessionID parses "<imsi>-<pdu_session_id>" back into its
// parts. CHF synthesises this format inside OnPDUSessionCreated so
// we round-trip it the same way.
func splitSessionID(sessionID string) (string, int, error) {
	idx := strings.LastIndex(sessionID, "-")
	if idx <= 0 || idx == len(sessionID)-1 {
		return "", 0, fmt.Errorf(
			"session_id %q: expected '<imsi>-<pdu_session_id>'", sessionID)
	}
	imsi := sessionID[:idx]
	pduID, err := strconv.Atoi(sessionID[idx+1:])
	if err != nil {
		return "", 0, fmt.Errorf(
			"session_id %q: pdu_session_id not integer: %v",
			sessionID, err)
	}
	return imsi, pduID, nil
}

func qChargingRows(q string, args ...any) ([]map[string]any, error) {
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
	var out []map[string]any
	for rows.Next() {
		scan := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range scan {
			ptrs[i] = &scan[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		m := make(map[string]any, len(cols))
		for i, name := range cols {
			m[name] = scan[i]
		}
		out = append(out, m)
	}
	return out, nil
}

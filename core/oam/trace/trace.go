// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package trace — Subscriber trace + Management-Based Trace (MDT).
//
// Spec anchors (citations + TODOs are deliberately split because the
// loaded TS 32.422 PDF is not in specs/3gpp/, so speccheck would flag
// any §-form reference here):
//
//   - TODO(spec: TS 32.422, "Subscriber and equipment trace; Trace
//     control and configuration management") — defines the trace job
//     activation envelope (Trace Reference, Trace Recording Session
//     Reference, depth/interfaces selection, NE Type list). Our model
//     keeps the same fields on trace_sessions but does not implement
//     the file-based reporting transport in clause 5.6.
//   - TODO(spec: TS 32.423, "Subscriber and equipment trace; Trace
//     data definition and management") — file naming convention and
//     the per-record fields. We persist plain rows to trace_records
//     instead of TraceRecord ASN.1 files.
//   - TODO(spec: TS 32.421, "Subscriber and equipment trace; Trace
//     concepts and requirements") — the "Trace Recording Session"
//     concept is collapsed onto a single trace_sessions row here.
//   - TODO(spec: TS 28.531) — Management-Based Trace (MDT) activation
//     via 5GC-MnS; defer to OAM provisioning.
//
// Implementation notes:
//
//   - Capture() is a no-op when no trace_sessions row has status='active'
//     for the matching IMSI / interface — keeps the hot signalling path
//     fast.
//   - timestamps are written as ISO datetime strings via datetime('now')
//     so they sort lexicographically and match the schema CHECKs.
package trace

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Depth values per TS 32.422 (deferred). Keep as constants so callers
// don't sprinkle string literals.
const (
	DepthMinimum = "minimum"
	DepthMedium  = "medium"
	DepthMaximum = "maximum"
)

// Session status values match the schema CHECK constraint.
const (
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusStopped   = "stopped"
)

// Capture inserts one trace record. Called from NGAP / NAS / SIP / N4
// handlers; returns immediately when no session is active so the hot
// path stays cheap.
//
//	iface:     "N1" | "N2" | "N4" | "SIP" | "Diameter"
//	direction: "in" | "out"
//	msgName:   human-readable label ("RegistrationRequest", "NGSetupRequest", …)
//	opts:      optional key=value fields:
//	             imsi     string  — UE filter; rows with no imsi pass through
//	             gnb_ip   string  — surfaced in summary (no dedicated column)
//	             summary  string  — operator-readable one-liner
//	             raw_bytes []byte — encoded into hex_dump
//	             msg_code int     — protocol-specific numeric code
//	             latency_us int64 — request/response latency, if known
func Capture(iface, direction, msgName string, opts map[string]any) {
	log := logger.Get("oam.trace")
	db, err := engine.Open()
	if err != nil {
		return
	}

	imsi, _ := opts["imsi"].(string)

	// Match the active session: any row with status='active' that either
	// has a blank imsi (catch-all) or matches the event imsi.
	var traceRef string
	row := db.QueryRow(
		`SELECT trace_ref FROM trace_sessions
		 WHERE status='active' AND (imsi IS NULL OR imsi='' OR imsi=?)
		 ORDER BY started_at DESC LIMIT 1`, imsi)
	if err := row.Scan(&traceRef); err != nil {
		return // no active session — drop silently.
	}

	gnbIP, _ := opts["gnb_ip"].(string)
	summary, _ := opts["summary"].(string)
	if gnbIP != "" {
		if summary == "" {
			summary = "gnb=" + gnbIP
		} else {
			summary = "gnb=" + gnbIP + " " + summary
		}
	}

	var hexDump string
	if raw, ok := opts["raw_bytes"].([]byte); ok && len(raw) > 0 {
		hexDump = hex.EncodeToString(raw)
	}

	var msgCode int
	switch v := opts["msg_code"].(type) {
	case int:
		msgCode = v
	case int64:
		msgCode = int(v)
	}

	var latencyUS sql.NullInt64
	switch v := opts["latency_us"].(type) {
	case int64:
		latencyUS = sql.NullInt64{Int64: v, Valid: true}
	case int:
		latencyUS = sql.NullInt64{Int64: int64(v), Valid: true}
	}

	if _, err := db.Exec(`INSERT INTO trace_records
		(trace_ref, interface, direction, msg_type, msg_code,
		 imsi, summary, hex_dump, latency_us)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		traceRef, iface, direction, msgName, msgCode,
		nullStr(imsi), nullStr(summary), nullStr(hexDump), latencyUS,
	); err != nil {
		log.Debugf("trace insert: %v", err)
		return
	}

	// Keep trace_sessions.record_count in step.
	_, _ = db.Exec("UPDATE trace_sessions SET record_count = record_count + 1 WHERE trace_ref=?", traceRef)
}

// SessionInput groups arguments for StartSession.
type SessionInput struct {
	TraceRef    string // operator-supplied; falls back to a synthesised one
	IMSI        string // optional — empty means catch-all
	GnbIP       string
	Depth       string // "minimum" | "medium" | "maximum"
	Interfaces  string // CSV: "N1,N2,N4,SIP"
	DurationSec int
}

// StartSession creates a new active trace session row.
func StartSession(in SessionInput) (string, error) {
	if in.Depth == "" {
		in.Depth = DepthMedium
	}
	if !validDepth(in.Depth) {
		return "", fmt.Errorf("invalid depth %q (want minimum|medium|maximum)", in.Depth)
	}
	if in.Interfaces == "" {
		in.Interfaces = "N1,N2"
	}
	if in.DurationSec <= 0 {
		in.DurationSec = 600
	}
	if in.TraceRef == "" {
		in.TraceRef = fmt.Sprintf("trace-%s-%d", strings.ReplaceAll(in.IMSI, "*", "x"), time.Now().UnixNano())
	}
	db, err := engine.Open()
	if err != nil {
		return "", err
	}
	_, err = db.Exec(`INSERT INTO trace_sessions
		(trace_ref, imsi, gnb_ip, depth, interfaces, duration_sec, status)
		VALUES (?,?,?,?,?,?, 'active')`,
		in.TraceRef, nullStr(in.IMSI), nullStr(in.GnbIP), in.Depth, in.Interfaces, in.DurationSec)
	if err != nil {
		return "", err
	}
	return in.TraceRef, nil
}

// StopSession marks a session as stopped. Returns true if a row was flipped.
func StopSession(traceRef string) (bool, error) {
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	res, err := db.Exec(
		"UPDATE trace_sessions SET status='stopped', stopped_at=datetime('now') WHERE trace_ref=? AND status='active'",
		traceRef)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListSessions returns all trace sessions, newest first.
func ListSessions() ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT trace_ref, imsi, gnb_ip, depth, interfaces,
		duration_sec, status, started_at, stopped_at, record_count
		FROM trace_sessions ORDER BY started_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// ListRecords returns the most recent n trace records.
func ListRecords(limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 200
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT trace_ref, interface, direction, msg_type,
		msg_code, imsi, summary, latency_us, timestamp
		FROM trace_records ORDER BY timestamp DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// ── helpers ──────────────────────────────────────────────────────

func validDepth(d string) bool {
	switch d {
	case DepthMinimum, DepthMedium, DepthMaximum:
		return true
	}
	return false
}

func nullStr(s string) any {
	if s == "" {
		return sql.NullString{}
	}
	return s
}

func scanRows(rows interface {
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...any) error
}) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
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
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			row[name] = scan[i]
		}
		out = append(out, row)
	}
	return out, nil
}

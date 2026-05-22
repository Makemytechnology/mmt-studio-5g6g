// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package n26 — AMF-side N26 inter-system handover panel + audit log.
//
// N26 is the optional EPC↔5GC interface that lets a UE move between
// MME and AMF without a full re-attach (mapped contexts replace
// fresh registration). This package owns the AMF-side audit + status
// surface; the actual signalling rides the existing GMM / SMC paths
// in nf/amf/gmm. The MME-side mirror is access/epc/mme/n26.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.501 §5.17.2       Interworking with EPC — umbrella for the
//                             N26 / non-N26 interworking architecture.
//   - TS 23.501 §5.17.2.2     Interworking Procedures with N26 interface
//                             — the only mode with mapped contexts.
//   - TS 23.501 §5.17.2.2.1   General — applies to UEs in single-
//                             registration mode.
//   - TS 23.501 §5.17.2.2.2   Mobility for UEs in single-registration
//                             mode (the audit-log row vocabulary
//                             matches the procedures listed here).
//   - TS 23.501 §5.17.5.2.1   Interworking with N26 interface for
//                             Monitoring Events — motivates the audit
//                             log we expose.
//   - TS 23.502 §4.11         System interworking procedures with EPC
//                             — where the actual N26 step-by-step flows
//                             live (referenced by the GMM/SMC handlers
//                             that ride this package).
//
// Deferred (TODO at unimplemented surfaces):
//
//   - TS 23.501 §5.17.2.3     Interworking without N26 — the "no
//                             mapped context" path; out of scope here
//                             since this package's whole reason to
//                             exist is the N26 audit trail.
//   - TS 23.501 §5.17.2.4     Mobility between 5GS and GERAN/UTRAN —
//                             not supported (no GERAN/UTRAN core).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/access_mobility.py.
package n26

import (
	"errors"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Source / target RAT vocabulary — matches the schema CHECK on
// n26_handover_log (db/schemas/domains.go).
const (
	RAT4G = "4G"
	RAT5G = "5G"
)

// Status vocabulary — matches the schema CHECK on n26_handover_log.
const (
	StatusInitiated = "initiated"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

var validRAT = map[string]bool{RAT4G: true, RAT5G: true}
var validStatus = map[string]bool{
	StatusInitiated: true, StatusCompleted: true, StatusFailed: true,
}

// LogHandover records one N26 handover step in the audit log. A
// successful handover should produce two rows: the 'initiated' row at
// the AMF-side decision point, and the 'completed' (or 'failed') row
// after the MME ACK comes back through the N26 socket.
//
// Per TS 23.501 §5.17.2.2.2 the AMF is the single source of truth
// for single-registration UE mobility; the MME echoes the outcome
// back so both sides agree.
func LogHandover(imsi, sourceRAT, targetRAT, status string) (int64, error) {
	if imsi == "" {
		return 0, errors.New("imsi is required")
	}
	if !validRAT[sourceRAT] || !validRAT[targetRAT] {
		return 0, errors.New("source_rat / target_rat must be 4G or 5G")
	}
	if !validStatus[status] {
		return 0, errors.New("status must be initiated|completed|failed")
	}
	res, err := engine.Exec(
		`INSERT INTO n26_handover_log (imsi, source_rat, target_rat, status)
		 VALUES (?,?,?,?)`,
		imsi, sourceRAT, targetRAT, status)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	logger.Get("amf.n26").Infof(
		"N26 handover logged: id=%d imsi=%s %s→%s status=%s",
		id, imsi, sourceRAT, targetRAT, status)
	return id, nil
}

// MarkCompleted advances an 'initiated' row to 'completed' once the
// MME ACK lands. Returns whether a row was actually updated.
func MarkCompleted(rowID int64) (bool, error) {
	res, err := engine.Exec(
		`UPDATE n26_handover_log SET status='completed'
		 WHERE id=? AND status='initiated'`, rowID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// MarkFailed advances 'initiated' → 'failed' (e.g. MME timeout).
func MarkFailed(rowID int64) (bool, error) {
	res, err := engine.Exec(
		`UPDATE n26_handover_log SET status='failed'
		 WHERE id=? AND status='initiated'`, rowID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// List returns the most recent N26 handover audit rows (newest first
// per timestamp, capped to 1000).
func List() ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		"SELECT * FROM n26_handover_log ORDER BY timestamp DESC, id DESC LIMIT 1000")
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
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			row[name] = scan[i]
		}
		out = append(out, row)
	}
	return out, nil
}

// ListForIMSI returns the handover history for one UE.
func ListForIMSI(imsi string) ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		"SELECT * FROM n26_handover_log WHERE imsi=? ORDER BY timestamp DESC, id DESC",
		imsi)
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
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			row[name] = scan[i]
		}
		out = append(out, row)
	}
	return out, nil
}

// Stats returns counter shape for the operator dashboard. Splits
// per-direction so an operator can spot one-sided drift.
func Stats() map[string]any {
	db, err := engine.Open()
	if err != nil {
		return map[string]any{}
	}
	var total, initiated, completed, failed, n4to5, n5to4 int
	_ = db.QueryRow("SELECT COUNT(*) FROM n26_handover_log").Scan(&total)
	_ = db.QueryRow("SELECT COUNT(*) FROM n26_handover_log WHERE status='initiated'").Scan(&initiated)
	_ = db.QueryRow("SELECT COUNT(*) FROM n26_handover_log WHERE status='completed'").Scan(&completed)
	_ = db.QueryRow("SELECT COUNT(*) FROM n26_handover_log WHERE status='failed'").Scan(&failed)
	_ = db.QueryRow("SELECT COUNT(*) FROM n26_handover_log WHERE source_rat='4G' AND target_rat='5G'").Scan(&n4to5)
	_ = db.QueryRow("SELECT COUNT(*) FROM n26_handover_log WHERE source_rat='5G' AND target_rat='4G'").Scan(&n5to4)
	return map[string]any{
		"total":     total,
		"initiated": initiated,
		"completed": completed,
		"failed":    failed,
		"4g_to_5g":  n4to5,
		"5g_to_4g":  n5to4,
	}
}

// Status returns current state for the GUI panel.
func Status() map[string]any { return Stats() }

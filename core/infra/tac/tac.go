// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package tac — Tracking Area management (TS 23.501 §5.4).
//
// Go port of infra/tac/. TAs are the coarsest-grained mobility concept
// in 5G — each tracks a set of cells (NR-CGI) + a PLMN identity. gNBs
// report their served TACs in NG Setup; the AMF validates those against
// the configured set and optionally auto-binds the gNB to a TA row.
//
// TAC is stored as a hex string (typically 6 hex chars = 3 octets per
// TS 23.003 §19.4.2.3). Comparisons are case-insensitive via strings.EqualFold.
package tac

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// TA is the tracking_areas row shape.
type TA struct {
	TAC            string // uppercase hex
	PLMNMCC, PLMNMNC string
	Name           string
	PagingPriority int // 1..10 (CHECK constraint)
	Enabled        bool
}

// Create adds or updates a TA row. Idempotent on the TAC primary key.
func Create(tac, mcc, mnc, name string, pagingPriority int) error {
	if pagingPriority < 1 || pagingPriority > 10 {
		pagingPriority = 5
	}
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`
        INSERT INTO tracking_areas (tac, plmn_mcc, plmn_mnc, name, paging_priority)
        VALUES (?, ?, ?, ?, ?)
        ON CONFLICT(tac) DO UPDATE SET
            plmn_mcc=excluded.plmn_mcc, plmn_mnc=excluded.plmn_mnc,
            name=excluded.name, paging_priority=excluded.paging_priority`,
		strings.ToUpper(tac), mcc, mnc, name, pagingPriority,
	)
	return err
}

// Delete removes a TA (cascades through ta_cell_map / ta_gnb_map / ta_nssai_policy).
func Delete(tac string) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`DELETE FROM tracking_areas WHERE tac=?`, strings.ToUpper(tac))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Get returns a single TA, or nil when absent.
func Get(tac string) (*TA, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT tac, plmn_mcc, plmn_mnc, name, paging_priority, enabled
        FROM tracking_areas WHERE tac=?`, strings.ToUpper(tac))
	return scanTA(row)
}

// List returns every TA, sorted by TAC.
func List(enabledOnly bool) ([]TA, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := `SELECT tac, plmn_mcc, plmn_mnc, name, paging_priority, enabled
          FROM tracking_areas`
	if enabledOnly {
		q += ` WHERE enabled=1`
	}
	q += ` ORDER BY tac`
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TA
	for rows.Next() {
		t, err := scanTA(rows)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out = append(out, *t)
		}
	}
	return out, rows.Err()
}

// SetEnabled flips the enabled flag on an existing row.
func SetEnabled(tac string, enabled bool) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	v := 0
	if enabled {
		v = 1
	}
	_, err = db.Exec(`UPDATE tracking_areas SET enabled=? WHERE tac=?`,
		v, strings.ToUpper(tac))
	return err
}

type scanner interface {
	Scan(...any) error
}

func scanTA(r scanner) (*TA, error) {
	var t TA
	var enabled int
	err := r.Scan(&t.TAC, &t.PLMNMCC, &t.PLMNMNC, &t.Name, &t.PagingPriority, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	return &t, nil
}

// ── gNB ↔ TA mapping ────────────────────────────────────────────────────

// MapGnbToTA binds a gNB identifier (IP or global gNB ID) to a TA.
// Idempotent.
func MapGnbToTA(gnbID, tac string) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO ta_gnb_map (gnb_id, tac) VALUES (?, ?)`,
		gnbID, strings.ToUpper(tac))
	return err
}

// ── Validation used by NG Setup ─────────────────────────────────────────

// ValidateResult reports the outcome of gNB-reported TAC validation.
type ValidateResult struct {
	Accepted   []string // TACs (upper-hex) that matched a configured TA
	Rejected   []string // TACs we don't know about (logged as warnings)
	AutoMapped []string // TACs the caller should store via MapGnbToTA
}

// ValidateGnbTACs cross-references a list of reported TAC strings against
// configured tracking_areas. Matching rows are accepted + auto-mapped,
// non-matching rows are rejected (warning only — TS 38.413 §8.7.1.4 says
// this is NOT a fatal NG Setup condition unless ALL TACs are unknown).
//
// Comparison is numeric: both sides are hex-decoded with strconv so leading
// zeros don't cause spurious mismatches.
func ValidateGnbTACs(gnbID string, reportedTACs []string) (ValidateResult, error) {
	out := ValidateResult{}
	configured, err := List(true)
	if err != nil {
		return out, err
	}
	if len(configured) == 0 {
		// No TAs configured yet — accept all so the operator can boot
		// and add TAs from the GUI without NG Setup failing first.
		for _, t := range reportedTACs {
			out.Accepted = append(out.Accepted, strings.ToUpper(t))
		}
		return out, nil
	}
	numeric := make(map[int64]string, len(configured))
	for _, ta := range configured {
		n, err := strconv.ParseInt(ta.TAC, 16, 64)
		if err != nil {
			continue
		}
		numeric[n] = ta.TAC
	}
	for _, tac := range reportedTACs {
		n, err := strconv.ParseInt(tac, 16, 64)
		if err != nil {
			out.Rejected = append(out.Rejected, tac)
			continue
		}
		if db, ok := numeric[n]; ok {
			out.Accepted = append(out.Accepted, db)
			if gnbID != "" {
				_ = MapGnbToTA(gnbID, db)
				out.AutoMapped = append(out.AutoMapped, db)
			}
		} else {
			out.Rejected = append(out.Rejected, strings.ToUpper(tac))
		}
	}
	return out, nil
}

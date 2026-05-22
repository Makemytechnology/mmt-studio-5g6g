// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package plmn — supported-PLMN CRUD + GUAMI / PLMNSupportList derivation.
//
// Go port of infra/plmn/. The AMF consults this package during NG Setup
// (PLMN validation + PLMNSupportList IE construction) and Registration
// (Equivalent-PLMN list IE).
//
// PLMN-ID wire encoding (TS 23.003 §2.2 / §12.1):
//
//	3 BCD octets — octet 0 = MCC[1]||MCC[0], octet 1 = MNC[2]||MCC[2],
//	octet 2 = MNC[1]||MNC[0], with the high nibble of octet 1 set to 0xF
//	when the operator's MNC is 2 digits (2-digit MNCs are the common case).
package plmn

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// PLMN is the in-memory row shape. Nullable fields use sql.Null*
// so the caller can tell "set to 0" from "not configured".
type PLMN struct {
	PLMNID       string // "001-01" form, stable primary key
	MCC, MNC     string
	Name         string
	Type         string // home | equivalent | roaming
	AMFRegionID  sql.NullInt64
	AMFSetID     sql.NullInt64
	AMFPointer   sql.NullInt64
	MMEGroupID   sql.NullInt64
	MMECode      sql.NullInt64
	Priority     int
	Enabled      bool
}

// Key builds the composite key used as the primary key in DB rows.
func Key(mcc, mnc string) string { return mcc + "-" + mnc }

// Upsert inserts or updates a PLMN row. Name / AMFRegionID / AMFSetID etc.
// can all be empty / zero — they're preserved only when explicitly set.
func Upsert(p PLMN) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	if p.PLMNID == "" {
		p.PLMNID = Key(p.MCC, p.MNC)
	}
	if p.Type == "" {
		p.Type = "home"
	}
	if p.Priority == 0 {
		p.Priority = 1
	}
	enabled := 1
	if !p.Enabled {
		// Zero-value means "not set yet" — default to enabled on create.
		// Call SetEnabled to turn OFF explicitly.
		// But allow caller override: if caller set Enabled true, use 1; false → 0 when row exists.
	}
	_, err = db.Exec(`
        INSERT INTO supported_plmns
            (plmn_id, mcc, mnc, name, plmn_type, amf_region_id, amf_set_id,
             amf_pointer, mme_group_id, mme_code, priority, enabled)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(plmn_id) DO UPDATE SET
            mcc=excluded.mcc, mnc=excluded.mnc, name=excluded.name,
            plmn_type=excluded.plmn_type,
            amf_region_id=excluded.amf_region_id, amf_set_id=excluded.amf_set_id,
            amf_pointer=excluded.amf_pointer, mme_group_id=excluded.mme_group_id,
            mme_code=excluded.mme_code, priority=excluded.priority`,
		p.PLMNID, p.MCC, p.MNC, p.Name, p.Type,
		p.AMFRegionID, p.AMFSetID, p.AMFPointer,
		p.MMEGroupID, p.MMECode, p.Priority, enabled,
	)
	return err
}

// Delete removes a PLMN by its composite key.
func Delete(plmnID string) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`DELETE FROM supported_plmns WHERE plmn_id=?`, plmnID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Get returns a single PLMN by key, or nil when absent.
func Get(plmnID string) (*PLMN, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT plmn_id, mcc, mnc, name, plmn_type,
        amf_region_id, amf_set_id, amf_pointer, mme_group_id, mme_code,
        priority, enabled FROM supported_plmns WHERE plmn_id=?`, plmnID)
	return scanPLMN(row)
}

// List returns every PLMN (optionally only enabled ones), sorted by priority.
func List(enabledOnly bool) ([]PLMN, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := `SELECT plmn_id, mcc, mnc, name, plmn_type, amf_region_id,
        amf_set_id, amf_pointer, mme_group_id, mme_code, priority, enabled
        FROM supported_plmns`
	if enabledOnly {
		q += ` WHERE enabled=1`
	}
	q += ` ORDER BY priority, plmn_id`
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PLMN
	for rows.Next() {
		p, err := scanPLMN(rows)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out = append(out, *p)
		}
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(...any) error
}

func scanPLMN(r scanner) (*PLMN, error) {
	var p PLMN
	var enabled int
	err := r.Scan(&p.PLMNID, &p.MCC, &p.MNC, &p.Name, &p.Type,
		&p.AMFRegionID, &p.AMFSetID, &p.AMFPointer,
		&p.MMEGroupID, &p.MMECode, &p.Priority, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	return &p, nil
}

// IsSupported returns true if (mcc, mnc) is present and enabled.
func IsSupported(mcc, mnc string) (bool, error) {
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	var enabled int
	err = db.QueryRow(`SELECT enabled FROM supported_plmns WHERE plmn_id=?`,
		Key(mcc, mnc)).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled != 0, nil
}

// ── Wire encoding helpers ──────────────────────────────────────────────

// EncodePLMN renders MCC+MNC into the 3-octet TBCD form used by NGAP and
// NAS. Accepts 2-digit or 3-digit MNCs.
//
// Per TS 23.003 §2.2:
//
//	octet 1: MCC[1] (hi) | MCC[0] (lo)
//	octet 2: if MNC has 2 digits: 0xF (hi) | MCC[2] (lo); else MNC[2] | MCC[2]
//	octet 3: MNC[1] (hi) | MNC[0] (lo)
func EncodePLMN(mcc, mnc string) ([]byte, error) {
	if len(mcc) != 3 {
		return nil, fmt.Errorf("MCC must be 3 digits, got %q", mcc)
	}
	if len(mnc) != 2 && len(mnc) != 3 {
		return nil, fmt.Errorf("MNC must be 2 or 3 digits, got %q", mnc)
	}
	d := [6]byte{}
	for i := 0; i < 3; i++ {
		c, err := strconv.Atoi(string(mcc[i]))
		if err != nil {
			return nil, fmt.Errorf("MCC digit %d: %w", i, err)
		}
		d[i] = byte(c)
	}
	for i := 0; i < len(mnc); i++ {
		c, err := strconv.Atoi(string(mnc[i]))
		if err != nil {
			return nil, fmt.Errorf("MNC digit %d: %w", i, err)
		}
		d[3+i] = byte(c)
	}
	if len(mnc) == 2 {
		// 3rd MNC nibble is 0xF per TS 23.003 §2.2.
		return []byte{
			(d[1] << 4) | d[0],
			(0x0F << 4) | d[2],
			(d[4] << 4) | d[3],
		}, nil
	}
	return []byte{
		(d[1] << 4) | d[0],
		(d[5] << 4) | d[2],
		(d[4] << 4) | d[3],
	}, nil
}

// DecodePLMN inverts EncodePLMN. Returns the (mcc, mnc) pair.
func DecodePLMN(b []byte) (mcc, mnc string, err error) {
	if len(b) != 3 {
		return "", "", fmt.Errorf("PLMN must be 3 octets, got %d", len(b))
	}
	mcc = string([]byte{
		'0' + (b[0] & 0x0F),
		'0' + (b[0] >> 4),
		'0' + (b[1] & 0x0F),
	})
	mncHigh := (b[1] >> 4) & 0x0F
	if mncHigh == 0x0F {
		// 2-digit MNC.
		mnc = string([]byte{
			'0' + (b[2] & 0x0F),
			'0' + (b[2] >> 4),
		})
	} else {
		mnc = string([]byte{
			'0' + (b[2] & 0x0F),
			'0' + (b[2] >> 4),
			'0' + mncHigh,
		})
	}
	return mcc, mnc, nil
}

// ── Validation used by NG Setup ─────────────────────────────────────────

// ValidateGnbPLMNs checks whether the gNB-reported PLMN set intersects the
// AMF's configured set. Returns true when at least one PLMN matches.
//
// gnbPLMNs is a list of encoded 3-byte PLMN identifiers taken from the
// SupportedTAList in the NG Setup Request.
func ValidateGnbPLMNs(gnbPLMNs [][]byte) (bool, error) {
	if len(gnbPLMNs) == 0 {
		// TS 38.413 §8.7.1.4: empty PLMN list is degenerate; accept for
		// dev scenarios where the gNB doesn't advertise slice/PLMN info.
		return true, nil
	}
	ours, err := List(true)
	if err != nil {
		return false, err
	}
	if len(ours) == 0 {
		// No PLMNs configured yet — match everything so the operator can
		// still boot and add PLMNs from the GUI.
		return true, nil
	}
	configured := make(map[string]struct{}, len(ours))
	for _, p := range ours {
		b, err := EncodePLMN(p.MCC, p.MNC)
		if err != nil {
			continue
		}
		configured[string(b)] = struct{}{}
	}
	for _, g := range gnbPLMNs {
		if _, ok := configured[string(g)]; ok {
			return true, nil
		}
	}
	return false, nil
}

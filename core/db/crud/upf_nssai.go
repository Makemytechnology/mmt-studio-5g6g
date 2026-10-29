// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/upf_nssai.go — UPF anchor ↔ S-NSSAI normalised mapping.
//
// The legacy upf_instances.supported_sst CSV column is being phased
// out in favour of the upf_supported_nssai join table (TS 23.501
// v19.7.0 §6.3.3 — SMF UPF selection per (S-NSSAI, DNN)). This module
// owns the join-table CRUD; engine/schema.go::backfillUPFSupportedNSSAI
// keeps it consistent with any pre-migration CSV rows on boot.

package crud

import (
	"github.com/mmt/mmt-studio-core/db/engine"
)

// UPFSupportedNSSAIList returns the nssai_id values bound to a UPF
// anchor. Used by the SMF UPF selector at runtime to decide whether
// a given session's S-NSSAI is anchored on this UPF.
func UPFSupportedNSSAIList(upfID string) ([]int64, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT nssai_id FROM upf_supported_nssai WHERE upf_id=? ORDER BY nssai_id`, upfID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UPFSupportedNSSAISet replaces the entire set of slices bound to a
// UPF anchor with the given nssai_ids. Atomic: wraps DELETE + INSERTs
// in a single transaction so a partial failure leaves the previous
// set intact.
func UPFSupportedNSSAISet(upfID string, nssaiIDs []int64) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM upf_supported_nssai WHERE upf_id=?`, upfID); err != nil {
		return err
	}
	for _, id := range nssaiIDs {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO upf_supported_nssai (upf_id, nssai_id) VALUES (?, ?)`, upfID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UPFSupportedNSSAIAdd inserts a single (upf_id, nssai_id) pair.
// Idempotent via INSERT OR IGNORE.
func UPFSupportedNSSAIAdd(upfID string, nssaiID int64) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO upf_supported_nssai (upf_id, nssai_id) VALUES (?, ?)`, upfID, nssaiID)
	return err
}

// UPFSupportedNSSAIRemove deletes a single (upf_id, nssai_id) pair.
// Returns the number of rows actually removed (0 if absent).
func UPFSupportedNSSAIRemove(upfID string, nssaiID int64) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`DELETE FROM upf_supported_nssai WHERE upf_id=? AND nssai_id=?`, upfID, nssaiID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UPFsSupportingNSSAI returns the upf_id list for a given nssai_id —
// the inverse direction used by the SMF UPF selector ("which UPFs
// anchor this slice?"). Sorted by upf_id for stable output.
func UPFsSupportingNSSAI(nssaiID int64) ([]string, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT upf_id FROM upf_supported_nssai WHERE nssai_id=? ORDER BY upf_id`, nssaiID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package crud — per-domain database helpers.
//
// Go port of db/crud/. Each file mirrors one Python module:
//
//	ue.go             → crud/ue.py
//	auth.go           → crud/auth.py
//	subscription.go   → crud/subscription.py
//	services.go       → crud/services.py
//	charging.go       → crud/charging.py
//	bindings.go       → crud/bindings.py
//	nssai.go          → crud/nssai.py
//	infra_config.go   → crud/infra_config.py
//	infra_history.go  → crud/infra_history.py
//
// All functions operate on the global *sql.DB from engine.Open().
package crud

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// UE mirrors the subset of the ue table surfaced by the Python ue_get_by_imsi.
type UE struct {
	ID         int64
	IMSI       string
	MSISDN     sql.NullString
	Enabled    bool
	AMBRDLKbps int64
	AMBRULKbps int64
	HasAuth    bool
}

// UESummary is the dashboard row returned by UEList.
type UESummary struct {
	IMSI            string `json:"imsi"`
	MSISDN          string `json:"msisdn"`
	HasAuth         bool   `json:"has_auth"`
	HasSubscription bool   `json:"has_subscription"`
	Bindings        int    `json:"bindings"`
}

// UEGetOrCreateByIMSI returns the id of the ue row, creating it if missing.
// msisdn is updated when non-empty. Mirrors ue_get_or_create_by_imsi.
func UEGetOrCreateByIMSI(imsi string, msisdn *string) (int64, error) {
	imsi = strings.TrimSpace(imsi)
	if imsi == "" {
		return 0, errors.New("IMSI required")
	}
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}

	var id int64
	err = db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&id)
	switch {
	case err == nil:
		if msisdn != nil {
			if _, err := db.Exec(`UPDATE ue SET msisdn=? WHERE id=?`, *msisdn, id); err != nil {
				return 0, err
			}
		}
		return id, nil
	case errors.Is(err, sql.ErrNoRows):
		// fall through to insert
	default:
		return 0, err
	}

	var ms any
	if msisdn != nil {
		ms = *msisdn
	}
	res, err := db.Exec(`INSERT OR IGNORE INTO ue (imsi, msisdn) VALUES (?, ?)`, imsi, ms)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Race: re-fetch
		if err := db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	id, err = res.LastInsertId()
	return id, err
}

// UECloneDeep copies EVERY UE-scoped row (auth, subscription AMBR,
// subscribed NSSAI, slice-DNN authorisations, and service bindings)
// from srcIMSI to dstIMSI inside a single transaction. Re-running
// against an existing dstIMSI is safe: each INSERT OR IGNORE respects
// the (ue_id, nssai_id) / (subscribed_nssai_id, dnn) / (slice_dnn_id,
// service_name) UNIQUE keys so a partial clone retries cleanly.
//
// Returns the new ue.id on success. dstMSISDN is optional — pass ""
// to reuse the source MSISDN.
func UECloneDeep(srcIMSI, dstIMSI, dstMSISDN string) (int64, error) {
	srcIMSI = strings.TrimSpace(srcIMSI)
	dstIMSI = strings.TrimSpace(dstIMSI)
	if srcIMSI == "" || dstIMSI == "" {
		return 0, errors.New("src + dst IMSI required")
	}

	db, err := engine.Open()
	if err != nil {
		return 0, err
	}

	// 1) Look up source UE row (for AMBR + fallback MSISDN).
	src, err := UEGetByIMSI(srcIMSI)
	if err != nil {
		return 0, err
	}
	if src == nil {
		return 0, errors.New("source IMSI not found")
	}
	if dstMSISDN == "" && src.MSISDN.Valid {
		dstMSISDN = src.MSISDN.String
	}

	// 2) Source auth (OP/K/AMF/OPType). Required — clone without auth
	// has no practical use (can't register).
	auth, err := AuthGetByIMSI(srcIMSI)
	if err != nil {
		return 0, err
	}
	if auth == nil {
		return 0, errors.New("source IMSI has no auth data")
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// 3) Insert / update ue row with AMBR carried from the source.
	res, err := tx.Exec(
		`INSERT INTO ue (imsi, msisdn, enabled, ambr_dl_kbps, ambr_ul_kbps)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(imsi) DO UPDATE SET
		   msisdn       = excluded.msisdn,
		   enabled      = excluded.enabled,
		   ambr_dl_kbps = excluded.ambr_dl_kbps,
		   ambr_ul_kbps = excluded.ambr_ul_kbps`,
		dstIMSI, dstMSISDN, src.Enabled, src.AMBRDLKbps, src.AMBRULKbps,
	)
	if err != nil {
		return 0, err
	}
	_ = res
	var dstUEID int64
	if err := tx.QueryRow(`SELECT id FROM ue WHERE imsi=?`, dstIMSI).Scan(&dstUEID); err != nil {
		return 0, err
	}

	// 4) Auth data (OP/K/AMF/SQN reset to 0 so the new UE starts fresh).
	if _, err := tx.Exec(
		`INSERT INTO ue_auth_data (ue_id, op_type, op, k, amf, sqn,
		                           suci_profile, hn_private_key)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?)
		 ON CONFLICT(ue_id) DO UPDATE SET
		   op_type        = excluded.op_type,
		   op             = excluded.op,
		   k              = excluded.k,
		   amf            = excluded.amf,
		   sqn            = 0,
		   suci_profile   = excluded.suci_profile,
		   hn_private_key = excluded.hn_private_key`,
		dstUEID, auth.OpType, auth.OPHex, auth.KHex, auth.AMFHex,
		nullableStr(auth.SUCIProfile), nullableStr(auth.HNPrivateKey),
	); err != nil {
		return 0, err
	}

	// 5) Subscribed NSSAI rows → same nssai_id + is_default for dst.
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO ue_subscribed_nssai (ue_id, nssai_id, is_default)
		 SELECT ?, nssai_id, is_default
		 FROM ue_subscribed_nssai WHERE ue_id = ?`,
		dstUEID, src.ID,
	); err != nil {
		return 0, err
	}

	// 6) Slice-DNN authorisations — join back via (ue_id, nssai_id)
	// because ue_slice_dnn.subscribed_nssai_id points at a row we just
	// minted for the destination UE.
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO ue_slice_dnn (subscribed_nssai_id, dnn, is_default)
		 SELECT dst_usn.id, src_usd.dnn, src_usd.is_default
		 FROM ue_slice_dnn src_usd
		 JOIN ue_subscribed_nssai src_usn ON src_usn.id = src_usd.subscribed_nssai_id
		 JOIN ue_subscribed_nssai dst_usn ON dst_usn.ue_id = ?
		                                 AND dst_usn.nssai_id = src_usn.nssai_id
		 WHERE src_usn.ue_id = ?`,
		dstUEID, src.ID,
	); err != nil {
		return 0, err
	}

	// 7) Service bindings — join via (dst_ue, nssai, dnn).
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO service_bindings (slice_dnn_id, service_name, is_default)
		 SELECT dst_usd.id, src_sb.service_name, src_sb.is_default
		 FROM service_bindings src_sb
		 JOIN ue_slice_dnn src_usd ON src_usd.id = src_sb.slice_dnn_id
		 JOIN ue_subscribed_nssai src_usn ON src_usn.id = src_usd.subscribed_nssai_id
		 JOIN ue_subscribed_nssai dst_usn ON dst_usn.ue_id = ?
		                                 AND dst_usn.nssai_id = src_usn.nssai_id
		 JOIN ue_slice_dnn dst_usd ON dst_usd.subscribed_nssai_id = dst_usn.id
		                          AND dst_usd.dnn = src_usd.dnn
		 WHERE src_usn.ue_id = ?`,
		dstUEID, src.ID,
	); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return dstUEID, nil
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// UECloneRange deep-clones `count` UEs from srcIMSI, starting IMSI
// numbering at startIMSI. MSISDN for each clone is the last 10 digits
// of the new IMSI (i.e. IMSI minus the MCC+MNC prefix) — matches the
// seed-data convention.
//
// Both IMSIs must be the same width (typically 15 digits). Uses
// big-int arithmetic so wrap-around past 999…999 is caught instead of
// producing a shorter/misshapen IMSI.
//
// Returns the number of clones successfully created. An error short-
// circuits the loop — earlier clones in the range remain in the DB.
func UECloneRange(srcIMSI, startIMSI string, count int) (int, error) {
	srcIMSI = strings.TrimSpace(srcIMSI)
	startIMSI = strings.TrimSpace(startIMSI)
	if srcIMSI == "" || startIMSI == "" {
		return 0, errors.New("source_imsi and start_imsi required")
	}
	if count <= 0 {
		return 0, errors.New("count must be > 0")
	}
	width := len(startIMSI)
	for _, c := range startIMSI {
		if c < '0' || c > '9' {
			return 0, errors.New("start_imsi must be numeric")
		}
	}
	created := 0
	cur := []byte(startIMSI)
	for i := 0; i < count; i++ {
		newIMSI := string(cur)
		var msisdn string
		if len(newIMSI) > 5 {
			msisdn = newIMSI[5:] // strip MCC(3) + MNC(2)
		} else {
			msisdn = newIMSI
		}
		if _, err := UECloneDeep(srcIMSI, newIMSI, msisdn); err != nil {
			return created, err
		}
		created++
		if err := incDecStr(cur); err != nil {
			return created, err
		}
		if len(cur) != width {
			return created, errors.New("IMSI increment overflowed width")
		}
	}
	return created, nil
}

// incDecStr increments a decimal-digit byte slice in place (big-endian
// ASCII). Returns an error if it overflows the slice's width.
func incDecStr(b []byte) error {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < '9' {
			b[i]++
			return nil
		}
		b[i] = '0'
	}
	return errors.New("overflow")
}

// UEGetByIMSI returns the UE row + has_auth flag, or nil if not found.
func UEGetByIMSI(imsi string) (*UE, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	u := &UE{IMSI: imsi}
	var hasAuth int
	err = db.QueryRow(
		`SELECT u.id, u.msisdn, u.enabled, u.ambr_dl_kbps, u.ambr_ul_kbps,
            (SELECT COUNT(*) FROM ue_auth_data a WHERE a.ue_id = u.id) AS has_auth
         FROM ue u WHERE u.imsi=?`, imsi,
	).Scan(&u.ID, &u.MSISDN, &u.Enabled, &u.AMBRDLKbps, &u.AMBRULKbps, &hasAuth)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.HasAuth = hasAuth > 0
	return u, nil
}

// UEDeleteByIMSI cascades into auth / nssai / slice_dnn / bindings.
func UEDeleteByIMSI(imsi string) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`DELETE FROM ue WHERE imsi=?`, imsi)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UEDeleteRange deletes `count` UEs starting at startIMSI, walking
// the IMSI numerically (same incDecStr increment as UECloneRange).
// Per-IMSI cascade delete is the same single-UE path (UEDeleteByIMSI),
// so foreign-key cleanup of auth / nssai / slice_dnn / bindings is
// identical row-by-row.
//
// Returns the count of UEs actually deleted (rows present at delete
// time). A non-existent IMSI in the middle of the range is silently
// skipped — bulk delete is idempotent so re-running cleanly handles
// partial prior runs.
//
// IMSI width is preserved by incDecStr; an overflow past the chosen
// width returns an error and stops the loop.
func UEDeleteRange(startIMSI string, count int) (int, error) {
	startIMSI = strings.TrimSpace(startIMSI)
	if startIMSI == "" {
		return 0, errors.New("start_imsi required")
	}
	if count <= 0 {
		return 0, errors.New("count must be > 0")
	}
	width := len(startIMSI)
	for _, c := range startIMSI {
		if c < '0' || c > '9' {
			return 0, errors.New("start_imsi must be numeric")
		}
	}
	deleted := 0
	cur := []byte(startIMSI)
	for i := 0; i < count; i++ {
		imsi := string(cur)
		n, err := UEDeleteByIMSI(imsi)
		if err != nil {
			return deleted, err
		}
		if n > 0 {
			deleted++
		}
		if err := incDecStr(cur); err != nil {
			return deleted, err
		}
		if len(cur) != width {
			return deleted, errors.New("IMSI increment overflowed width")
		}
	}
	return deleted, nil
}

// UEList returns the dashboard rows (imsi, msisdn, has_auth, has_subscription, bindings).
func UEList() ([]UESummary, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
        SELECT u.imsi,
               COALESCE(u.msisdn,'') AS msisdn,
               (SELECT COUNT(*) FROM ue_auth_data a WHERE a.ue_id = u.id) AS has_auth,
               (SELECT COUNT(*) FROM ue_subscribed_nssai usn WHERE usn.ue_id = u.id) AS nssai_count,
               (SELECT COUNT(*) FROM service_bindings sb
                JOIN ue_slice_dnn usd ON usd.id = sb.slice_dnn_id
                JOIN ue_subscribed_nssai usn2 ON usn2.id = usd.subscribed_nssai_id
                WHERE usn2.ue_id = u.id) AS bindings
        FROM ue u
        ORDER BY u.imsi`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UESummary
	for rows.Next() {
		var s UESummary
		var ha, nssaiCnt, bindings int
		if err := rows.Scan(&s.IMSI, &s.MSISDN, &ha, &nssaiCnt, &bindings); err != nil {
			return nil, err
		}
		s.HasAuth = ha > 0
		s.HasSubscription = nssaiCnt > 0
		s.Bindings = bindings
		out = append(out, s)
	}
	return out, rows.Err()
}

// HNKeyLookup finds the (suci_profile, hn_private_key_hex) for a PLMN.
// Mirrors hn_key_lookup — used by the SUCI decryption path.
func HNKeyLookup(mcc, mnc string, hnpkid int) (profile, keyHex string, ok bool, err error) {
	db, err := engine.Open()
	if err != nil {
		return "", "", false, err
	}
	prefix := mcc + mnc + "%"
	row := db.QueryRow(
		`SELECT a.suci_profile, a.hn_private_key
         FROM ue_auth_data a
         JOIN ue u ON u.id = a.ue_id
         WHERE u.imsi LIKE ? AND a.suci_profile IS NOT NULL AND a.hn_private_key IS NOT NULL
         LIMIT 1`, prefix,
	)
	var prof, key sql.NullString
	err = row.Scan(&prof, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return prof.String, key.String, true, nil
}

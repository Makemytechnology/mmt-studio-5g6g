// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/subscriber.go — Legacy turnkey-demo subscriber.
//
// Seeds one fully-provisioned subscriber with authentication, subscribed
// NSSAI, DNN authorizations, QoS service bindings, and IMS registration.
// Uses an IMSI outside the baseline-manifest bucket range so the 128
// roster UEs (db/seed/baseline.yaml: 001011234560001..001011234560128)
// keep their KDF-derived K/OPc and stay aligned with tester-side
// baseline.k()/opc().
//
//	IMSI:   001011234569001
//	MSISDN: 1234569001
//	K:      000102030405060708090a0b0c0d0e0f
//	OPc:    202122232425262728292a2b2c2d2e2f
//	AMF:    8000
package seed

import "database/sql"

// SeedDefaultSubscriber creates the legacy turnkey-demo subscriber with
// full provisioning. Idempotent — skips if the IMSI already exists.
func SeedDefaultSubscriber(db *sql.DB) error {
	const (
		imsi   = "001011234569001"
		msisdn = "1234569001"
		opType = "OPC"
		opc    = "202122232425262728292a2b2c2d2e2f"
		k      = "000102030405060708090a0b0c0d0e0f"
		amf    = "8000"
	)

	// Skip if already exists
	var existing int64
	if db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&existing) == nil {
		return nil
	}

	// ── 1. UE record ─────────────────────────────────────────────────
	res, err := db.Exec(
		`INSERT INTO ue (imsi, msisdn, ambr_dl_kbps, ambr_ul_kbps) VALUES (?,?,?,?)`,
		imsi, msisdn, 1_000_000, 1_000_000)
	if err != nil {
		return err
	}
	ueID, _ := res.LastInsertId()

	// ── 2. Authentication data (TS 33.501) ───────────────────────────
	if _, err := db.Exec(
		`INSERT INTO ue_auth_data (ue_id, op_type, op, k, sqn, amf) VALUES (?,?,?,?,?,?)`,
		ueID, opType, opc, k, 0, amf); err != nil {
		return err
	}

	// ── 3. NSSAI catalog entry (SST=1, SD=000001 "eMBB") ────────────
	_, _ = db.Exec(`INSERT OR IGNORE INTO nssai_catalog (sst, sd, name) VALUES (1, '000001', 'eMBB')`)
	var nssaiID int64
	if err := db.QueryRow(
		`SELECT id FROM nssai_catalog WHERE sst=1 AND IFNULL(sd,'')='000001'`).Scan(&nssaiID); err != nil {
		return err
	}

	// ── 4. Subscribed NSSAI ──────────────────────────────────────────
	if _, err := db.Exec(
		`INSERT INTO ue_subscribed_nssai (ue_id, nssai_id, is_default) VALUES (?,?,1)`,
		ueID, nssaiID); err != nil {
		return err
	}
	var usnID int64
	if err := db.QueryRow(
		`SELECT id FROM ue_subscribed_nssai WHERE ue_id=? AND nssai_id=?`,
		ueID, nssaiID).Scan(&usnID); err != nil {
		return err
	}

	// ── 5. Slice/DNN authorizations ──────────────────────────────────
	sliceDNNIDs := map[string]int64{}
	for _, sd := range []struct {
		dnn       string
		isDefault int
	}{
		{"internet", 1},
		{"ims", 0},
	} {
		if !apnExists(db, sd.dnn) {
			continue
		}
		_, _ = db.Exec(
			`INSERT OR IGNORE INTO ue_slice_dnn (subscribed_nssai_id, dnn, is_default) VALUES (?,?,?)`,
			usnID, sd.dnn, sd.isDefault)
		var id int64
		if db.QueryRow(
			`SELECT id FROM ue_slice_dnn WHERE subscribed_nssai_id=? AND dnn=?`,
			usnID, sd.dnn).Scan(&id) == nil {
			sliceDNNIDs[sd.dnn] = id
		}
	}

	// ── 6. QoS service bindings ──────────────────────────────────────
	for _, b := range []struct {
		dnn       string
		service   string
		isDefault int
	}{
		{"internet", "default_data", 1},
		{"ims", "ims_signalling", 1},
		{"ims", "conv_voice", 0},
		{"ims", "conv_video", 0},
	} {
		sdID, ok := sliceDNNIDs[b.dnn]
		if !ok || !serviceExists(db, b.service) {
			continue
		}
		_, _ = db.Exec(
			`INSERT OR IGNORE INTO service_bindings (slice_dnn_id, service_name, is_default) VALUES (?,?,?)`,
			sdID, b.service, b.isDefault)
	}

	// ── 7. IMS subscriber (TS 23.228) ────────────────────────────────
	impi := imsi + "@ims.mnc001.mcc001.3gppnetwork.org"
	impu := "sip:" + imsi + "@ims.mnc001.mcc001.3gppnetwork.org"
	_, _ = db.Exec(
		`INSERT OR IGNORE INTO ims_subscribers (ue_id, impi, impu) VALUES (?,?,?)`,
		ueID, impi, impu)

	return nil
}

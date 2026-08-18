// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/ue.go — UE roster seeder driven by db/seed/baseline.yaml.
//
// Reads the manifest (LoadBaseline) and walks every ue_bucket, inserting:
//
//   • ue                       (imsi, msisdn, ambr_dl_kbps, ambr_ul_kbps)
//   • ue_auth_data             (K, OPc derived per-IMSI from the manifest)
//   • ue_subscribed_nssai      (one row per S-NSSAI in the bucket's slices)
//   • ue_slice_dnn             (one row per DNN the bucket allows; default flagged)
//   • service_bindings         (default services per DNN: data, voice, video, ...)
//
// Idempotent: every step uses INSERT OR IGNORE or skips on existence
// check, so a re-seed after restart leaves the DB unchanged.
package seed

import (
	"database/sql"
	"fmt"
)

// SeedDefaultUEs replaces the legacy hard-coded 128-UE loop. The bucket
// structure, IMSI range, AMBR values, slice membership, and DNN list all
// come from db/seed/baseline.yaml. To add a UE bucket or change AMBRs,
// edit the YAML — no Go code change needed.
func SeedDefaultUEs(db *sql.DB) error {
	m, err := LoadBaseline()
	if err != nil {
		return fmt.Errorf("seed ues: %w", err)
	}

	// Build the slice-id lookup once (sst,sd → nssai_catalog.id), creating
	// any missing rows so the manifest's declared slice set is authoritative.
	sliceIDBySST, err := ensureNSSAICatalog(db, m.Slices)
	if err != nil {
		return err
	}

	// Default service bindings per DNN. Same set the previous hard-coded
	// seeder applied; gives every UE sensible defaults without per-bucket
	// configuration. Will move to manifest in a future migration step.
	bindings := []struct {
		DNN, Service string
		IsDefault    int
	}{
		{"internet", "default_data", 1},
		{"ims", "ims_signalling", 1},
		{"ims", "conv_voice", 0},
		{"ims", "conv_video", 0},
		{"mcx", "mcx_signalling", 1},
		{"mcx", "mcptt_voice", 0},
		{"mcx", "mcvideo", 0},
		{"mcx", "mcdata", 0},
	}

	for _, bucket := range m.UEBuckets {
		for i := 0; i < bucket.Count; i++ {
			imsi := bucket.IMSI(i)
			msisdn := bucket.MSISDN(i)

			if err := seedOneUE(db, m, &bucket, imsi, msisdn, sliceIDBySST, bindings); err != nil {
				return fmt.Errorf("seed UE %s (bucket %s): %w", imsi, bucket.Name, err)
			}
		}
	}
	return nil
}

func seedOneUE(
	db *sql.DB,
	m *Manifest,
	bucket *UEBucket,
	imsi, msisdn string,
	sliceIDBySST map[int]int64,
	bindings []struct {
		DNN, Service string
		IsDefault    int
	},
) error {
	// Idempotency: skip if this IMSI already has a row.
	var ueID int64
	if err := db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID); err == nil {
		return nil
	}

	res, err := db.Exec(
		`INSERT INTO ue (imsi, msisdn, ambr_dl_kbps, ambr_ul_kbps) VALUES (?,?,?,?)`,
		imsi, msisdn, bucket.UEAmbrDLKbps, bucket.UEAmbrULKbps,
	)
	if err != nil {
		return err
	}
	ueID, _ = res.LastInsertId()

	// Per-IMSI K / OPc from the manifest's KDF (or static, depending on mode).
	k := m.DeriveK(imsi)
	opc := m.DeriveOPc(imsi)
	if _, err := db.Exec(
		`INSERT INTO ue_auth_data (ue_id, op_type, op, k, sqn, amf) VALUES (?,?,?,?,?,?)`,
		ueID, "OPC", opc, k, m.UECredentials.InitialSQN, m.UECredentials.AMF,
	); err != nil {
		return err
	}

	// One ue_subscribed_nssai row per slice in the bucket; first slice is default.
	for idx, sst := range bucket.Slices {
		nssaiID, ok := sliceIDBySST[sst]
		if !ok {
			continue
		}
		isDefault := 0
		if idx == 0 {
			isDefault = 1
		}
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO ue_subscribed_nssai (ue_id, nssai_id, is_default) VALUES (?,?,?)`,
			ueID, nssaiID, isDefault,
		); err != nil {
			return err
		}

		var usnID int64
		if err := db.QueryRow(
			`SELECT id FROM ue_subscribed_nssai WHERE ue_id=? AND nssai_id=?`,
			ueID, nssaiID).Scan(&usnID); err != nil {
			continue
		}

		// DNN authorizations for this (UE, slice) pair.
		sliceDNNIDs := map[string]int64{}
		for _, dnn := range bucket.DNNs {
			if !apnExists(db, dnn) {
				continue
			}
			isDefaultDNN := 0
			if dnn == bucket.DefaultDNN {
				isDefaultDNN = 1
			}
			if _, err := db.Exec(
				`INSERT OR IGNORE INTO ue_slice_dnn (subscribed_nssai_id, dnn, is_default) VALUES (?,?,?)`,
				usnID, dnn, isDefaultDNN,
			); err != nil {
				return err
			}
			var id int64
			if err := db.QueryRow(
				`SELECT id FROM ue_slice_dnn WHERE subscribed_nssai_id=? AND dnn=?`,
				usnID, dnn).Scan(&id); err == nil {
				sliceDNNIDs[dnn] = id
			}
		}

		// Service bindings per authorized DNN.
		for _, b := range bindings {
			if !serviceExists(db, b.Service) {
				continue
			}
			usdID, ok := sliceDNNIDs[b.DNN]
			if !ok {
				continue
			}
			if _, err := db.Exec(
				`INSERT OR IGNORE INTO service_bindings (slice_dnn_id, service_name, is_default) VALUES (?,?,?)`,
				usdID, b.Service, b.IsDefault,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureNSSAICatalog inserts (or finds) every slice declared in the manifest
// into nssai_catalog and returns SST → id. The map is keyed by SST alone
// because every manifest bucket references slices by SST; the SD is filled
// in from the manifest's slice list (UNIQUE(sst,sd) holds in the schema).
func ensureNSSAICatalog(db *sql.DB, slices []Slice) (map[int]int64, error) {
	result := make(map[int]int64, len(slices))
	for _, s := range slices {
		var sdArg interface{}
		if s.SD == "" {
			sdArg = nil
		} else {
			sdArg = s.SD
		}
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO nssai_catalog (sst, sd, name) VALUES (?,?,?)`,
			s.SST, sdArg, s.Name,
		); err != nil {
			return nil, fmt.Errorf("ensure nssai (%d:%s): %w", s.SST, s.SD, err)
		}
		var id int64
		if s.SD == "" {
			err := db.QueryRow(
				`SELECT id FROM nssai_catalog WHERE sst=? AND (sd IS NULL OR sd='')`,
				s.SST).Scan(&id)
			if err != nil {
				return nil, fmt.Errorf("lookup nssai (%d:<none>): %w", s.SST, err)
			}
		} else {
			err := db.QueryRow(
				`SELECT id FROM nssai_catalog WHERE sst=? AND sd=?`,
				s.SST, s.SD).Scan(&id)
			if err != nil {
				return nil, fmt.Errorf("lookup nssai (%d:%s): %w", s.SST, s.SD, err)
			}
		}
		result[s.SST] = id
	}
	return result, nil
}

func apnExists(db *sql.DB, apn string) bool {
	var n int
	return db.QueryRow(`SELECT 1 FROM apn_config WHERE apn_name=?`, apn).Scan(&n) == nil
}

func serviceExists(db *sql.DB, name string) bool {
	var n int
	return db.QueryRow(`SELECT 1 FROM services WHERE name=?`, name).Scan(&n) == nil
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/bulk.go — Bulk UE provisioning in a single transaction.
//
// Replaces the 3-round-trip-per-UE flow (auth + subscription-tree + AMBR)
// that satester used to drive when re-baselining the core. A 128-UE
// bucket used to be 384 separate API calls; with this endpoint it's one.
//
// The template carries every field that's identical across a baseline
// bucket: AMBR, KDF settings (so K / OPc derive server-side from IMSI),
// and the subscription tree (slices + DNNs + service bindings). Only
// the IMSI and MSISDN vary per row.
package crud

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// UEBulkTemplate is the per-bucket shared subscriber profile. KDFVersion
// drives server-side K / OPc derivation — clients never send K/OPc
// (the tester mirrors db/seed/manifest.go::DeriveK so the result is
// byte-for-byte identical).
//
// AuthOPType is "OPC" (default) or "OP" — same as crud.AuthUpsertIn.
//
// Tree is the same shape as SubscriptionTree.Slices (passed in by the
// route handler) — we re-use the existing per-UE walker on each IMSI
// instead of re-implementing the slice/dnn/service insertions here.
type UEBulkTemplate struct {
	KDFVersion       string      `json:"kdf_version"`
	AuthOPType       string      `json:"op_type"`
	AMF              string      `json:"amf"`
	InitialSQN       int64       `json:"initial_sqn"`
	SUCIProfile      string      `json:"suci_profile,omitempty"`
	HNPrivateKey     string      `json:"hn_private_key,omitempty"`
	AMBRDLKbps       int64       `json:"ambr_dl_kbps"`
	AMBRULKbps       int64       `json:"ambr_ul_kbps"`
	SubscriptionTree []TreeSlice `json:"subscription_tree"`
}

// UEBulkEntry is the per-UE varying inputs. K/OPc are computed from
// IMSI + template.KDFVersion server-side.
type UEBulkEntry struct {
	IMSI   string `json:"imsi"`
	MSISDN string `json:"msisdn"`
}

// UEBulkRange — sugar that the route handler expands to a slice of
// UEBulkEntry. IMSI and MSISDN both increment by 1 across `count`.
type UEBulkRange struct {
	IMSIStart   string `json:"imsi_start"`
	MSISDNStart string `json:"msisdn_start"`
	Count       int    `json:"count"`
}

// UEBulkResult counts each row class created (UEs upserted, auth rows
// written, subscription slices / dnns / service-bindings linked).
type UEBulkResult struct {
	UEsCreated int `json:"ues_created"`
	AuthRows   int `json:"auth_rows"`
	Slices     int `json:"slices"`
	DNNs       int `json:"dnns"`
	Bindings   int `json:"bindings"`
}

// deriveK / deriveOPc mirror db/seed/manifest.go's DeriveK/DeriveOPc
// byte-for-byte. Kept private here so this package doesn't import
// db/seed (which would create an import cycle).
func deriveK(imsi, kdfVersion string) string {
	h := sha256.Sum256([]byte(imsi + "MMT-K-" + kdfVersion))
	return hex.EncodeToString(h[:16])
}

func deriveOPc(imsi, kdfVersion string) string {
	h := sha256.Sum256([]byte(imsi + "MMT-OPc-" + kdfVersion))
	return hex.EncodeToString(h[:16])
}

// UEBulkProvision inserts N UEs under one shared template in a single
// transaction. Rolls everything back on the first per-UE failure so a
// half-provisioned bucket can't end up in the DB.
func UEBulkProvision(template UEBulkTemplate, ues []UEBulkEntry) (UEBulkResult, error) {
	res := UEBulkResult{}
	if len(ues) == 0 {
		return res, errors.New("ues list is empty")
	}
	if template.KDFVersion == "" {
		return res, errors.New("template.kdf_version is required (no K/OPc fallback)")
	}
	opType := template.AuthOPType
	if opType == "" {
		opType = "OPC"
	}
	if opType != "OP" && opType != "OPC" {
		return res, errors.New("template.op_type must be OP or OPC")
	}
	if template.AMF != "" && !re4Hex.MatchString(template.AMF) {
		return res, errors.New("template.amf must be 4 hex")
	}
	if template.SUCIProfile != "" && template.SUCIProfile != "A" && template.SUCIProfile != "B" {
		return res, errors.New("template.suci_profile must be '' / 'A' / 'B'")
	}
	if template.HNPrivateKey != "" && !re64Hex.MatchString(template.HNPrivateKey) {
		return res, errors.New("template.hn_private_key must be 64 hex (32 bytes)")
	}

	db, err := engine.Open()
	if err != nil {
		return res, err
	}
	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	amfArg := sql.NullString{String: strings.ToLower(template.AMF), Valid: template.AMF != ""}
	profArg := sql.NullString{String: template.SUCIProfile, Valid: template.SUCIProfile != ""}
	hnArg := sql.NullString{String: strings.ToLower(template.HNPrivateKey), Valid: template.HNPrivateKey != ""}

	for _, u := range ues {
		imsi := strings.TrimSpace(u.IMSI)
		if imsi == "" {
			err = errors.New("each ue must carry a non-empty imsi")
			return res, err
		}

		// 1. ue row (upsert with AMBR)
		var ueID int64
		err = tx.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID)
		switch {
		case err == nil:
			if _, err = tx.Exec(`UPDATE ue SET msisdn=?, ambr_dl_kbps=?, ambr_ul_kbps=? WHERE id=?`,
				u.MSISDN, template.AMBRDLKbps, template.AMBRULKbps, ueID); err != nil {
				return res, fmt.Errorf("update ue %s: %w", imsi, err)
			}
		case errors.Is(err, sql.ErrNoRows):
			var ins sql.Result
			ins, err = tx.Exec(
				`INSERT INTO ue (imsi, msisdn, ambr_dl_kbps, ambr_ul_kbps) VALUES (?,?,?,?)`,
				imsi, u.MSISDN, template.AMBRDLKbps, template.AMBRULKbps,
			)
			if err != nil {
				return res, fmt.Errorf("insert ue %s: %w", imsi, err)
			}
			ueID, err = ins.LastInsertId()
			if err != nil {
				return res, err
			}
			res.UEsCreated++
		default:
			return res, err
		}

		// 2. ue_auth_data — derive K/OPc from IMSI + template kdf_version
		k := deriveK(imsi, template.KDFVersion)
		opc := deriveOPc(imsi, template.KDFVersion)
		var existing int64
		err = tx.QueryRow(`SELECT id FROM ue_auth_data WHERE ue_id=?`, ueID).Scan(&existing)
		switch {
		case err == nil:
			_, err = tx.Exec(
				`UPDATE ue_auth_data
				 SET op_type=?, op=?, k=?, sqn=?, amf=?, suci_profile=?, hn_private_key=?
				 WHERE ue_id=?`,
				opType, opc, k, template.InitialSQN, amfArg, profArg, hnArg, ueID,
			)
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.Exec(
				`INSERT INTO ue_auth_data
				 (ue_id, op_type, op, k, sqn, amf, suci_profile, hn_private_key)
				 VALUES (?,?,?,?,?,?,?,?)`,
				ueID, opType, opc, k, template.InitialSQN, amfArg, profArg, hnArg,
			)
		default:
			return res, err
		}
		if err != nil {
			return res, fmt.Errorf("auth upsert %s: %w", imsi, err)
		}
		res.AuthRows++

		// 3. Subscription tree — wipe + re-insert from template.
		if _, err = tx.Exec(`DELETE FROM ue_subscribed_nssai WHERE ue_id=?`, ueID); err != nil {
			return res, err
		}
		for _, sl := range template.SubscriptionTree {
			nssaiID := sl.NSSAIID
			if nssaiID == 0 {
				nssaiID, err = getOrCreateNSSAICatalog(txWrap{tx}, sl.SST, sl.SD)
				if err != nil {
					return res, err
				}
			}
			isDef := 0
			if sl.IsDefault {
				isDef = 1
			}
			if _, err = tx.Exec(
				`INSERT OR IGNORE INTO ue_subscribed_nssai (ue_id, nssai_id, is_default) VALUES (?,?,?)`,
				ueID, nssaiID, isDef,
			); err != nil {
				return res, err
			}
			var usnID int64
			if err = tx.QueryRow(
				`SELECT id FROM ue_subscribed_nssai WHERE ue_id=? AND nssai_id=?`, ueID, nssaiID,
			).Scan(&usnID); err != nil {
				return res, err
			}
			res.Slices++

			for _, dn := range sl.DNNs {
				if dn.DNN == "" {
					continue
				}
				dDef := 0
				if dn.IsDefault {
					dDef = 1
				}
				if _, err = tx.Exec(
					`INSERT OR IGNORE INTO ue_slice_dnn (subscribed_nssai_id, dnn, is_default) VALUES (?,?,?)`,
					usnID, dn.DNN, dDef,
				); err != nil {
					return res, err
				}
				var usdID int64
				if err = tx.QueryRow(
					`SELECT id FROM ue_slice_dnn WHERE subscribed_nssai_id=? AND dnn=?`, usnID, dn.DNN,
				).Scan(&usdID); err != nil {
					return res, err
				}
				res.DNNs++

				for _, sv := range dn.Services {
					if sv.ServiceName == "" {
						continue
					}
					sDef := 0
					if sv.IsDefault {
						sDef = 1
					}
					if _, err = tx.Exec(
						`INSERT OR IGNORE INTO service_bindings (slice_dnn_id, service_name, is_default) VALUES (?,?,?)`,
						usdID, sv.ServiceName, sDef,
					); err != nil {
						return res, err
					}
					res.Bindings++
				}
			}
		}
	}

	if err = tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}

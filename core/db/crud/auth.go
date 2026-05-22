// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/auth.go — UE authentication CRUD (TS 33.501 §6.1)
package crud

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// AuthData is the Python dict returned by auth_get_by_imsi.
type AuthData struct {
	IMSI         string `json:"imsi"`
	MSISDN       string `json:"msisdn"`
	OpType       string `json:"op_type"`
	OPHex        string `json:"op_hex"`
	KHex         string `json:"k_hex"`
	SQN          int64  `json:"sqn"`
	AMFHex       string `json:"amf_hex"`
	SUCIProfile  string `json:"suci_profile"`
	HNPrivateKey string `json:"hn_private_key"`
}

var (
	re32Hex = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	re4Hex  = regexp.MustCompile(`^[0-9a-fA-F]{4}$`)
	re64Hex = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// AuthGetByIMSI returns the auth row via the ue_auth_data.ue_id FK.
func AuthGetByIMSI(imsi string) (*AuthData, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var (
		opType, op, k, amf, profile, hnKey, msisdn sql.NullString
		sqn                                        sql.NullInt64
	)
	err = db.QueryRow(
		`SELECT a.op_type, a.op, a.k, a.sqn, a.amf, a.suci_profile, a.hn_private_key, u.msisdn
         FROM ue_auth_data a
         JOIN ue u ON u.id = a.ue_id
         WHERE u.imsi = ?`, imsi,
	).Scan(&opType, &op, &k, &sqn, &amf, &profile, &hnKey, &msisdn)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &AuthData{
		IMSI:         imsi,
		MSISDN:       msisdn.String,
		OpType:       opType.String,
		OPHex:        strings.ToLower(op.String),
		KHex:         strings.ToLower(k.String),
		SQN:          sqn.Int64,
		AMFHex:       strings.ToLower(amf.String),
		SUCIProfile:  profile.String,
		HNPrivateKey: strings.ToLower(hnKey.String),
	}, nil
}

// AuthUpsertIn groups the inputs to AuthUpsert (Python accepted **kwargs).
type AuthUpsertIn struct {
	IMSI         string
	MSISDN       string // may be empty
	OpType       string // OP | OPC
	OP           string // 32 hex
	K            string // 32 hex
	AMF          string // 4 hex (optional)
	SQN          int64
	SUCIProfile  string // "", "A", or "B"
	HNPrivateKey string // 64 hex (optional)
}

// AuthUpsert validates + persists the authentication data. Raises ValueError-
// equivalent errors (returned as error) on invalid formats.
func AuthUpsert(in AuthUpsertIn) error {
	if in.OpType != "OP" && in.OpType != "OPC" {
		return errors.New("op_type must be OP or OPC")
	}
	if !re32Hex.MatchString(in.OP) {
		return errors.New("op must be 32 hex")
	}
	if !re32Hex.MatchString(in.K) {
		return errors.New("k must be 32 hex")
	}
	if in.AMF != "" && !re4Hex.MatchString(in.AMF) {
		return errors.New("amf must be 4 hex")
	}
	if in.SUCIProfile != "" && in.SUCIProfile != "A" && in.SUCIProfile != "B" {
		return errors.New("suci_profile must be 'A' or 'B'")
	}
	if in.HNPrivateKey != "" && !re64Hex.MatchString(in.HNPrivateKey) {
		return errors.New("hn_private_key must be 64 hex (32 bytes)")
	}

	var msisdn *string
	if in.MSISDN != "" {
		msisdn = &in.MSISDN
	}
	ueID, err := UEGetOrCreateByIMSI(in.IMSI, msisdn)
	if err != nil {
		return err
	}

	db, err := engine.Open()
	if err != nil {
		return err
	}

	var existing int64
	err = db.QueryRow(`SELECT id FROM ue_auth_data WHERE ue_id=?`, ueID).Scan(&existing)
	amf := sql.NullString{String: strings.ToLower(in.AMF), Valid: in.AMF != ""}
	profile := sql.NullString{String: in.SUCIProfile, Valid: in.SUCIProfile != ""}
	hnKey := sql.NullString{String: strings.ToLower(in.HNPrivateKey), Valid: in.HNPrivateKey != ""}

	switch {
	case err == nil:
		_, err = db.Exec(
			`UPDATE ue_auth_data
             SET op_type=?, op=?, k=?, sqn=?, amf=?, suci_profile=?, hn_private_key=?
             WHERE ue_id=?`,
			in.OpType, strings.ToLower(in.OP), strings.ToLower(in.K), in.SQN,
			amf, profile, hnKey, ueID,
		)
	case errors.Is(err, sql.ErrNoRows):
		_, err = db.Exec(
			`INSERT INTO ue_auth_data (ue_id, op_type, op, k, sqn, amf, suci_profile, hn_private_key)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ueID, in.OpType, strings.ToLower(in.OP), strings.ToLower(in.K), in.SQN,
			amf, profile, hnKey,
		)
	}
	if err != nil {
		return fmt.Errorf("auth upsert: %w", err)
	}
	return nil
}

// AuthDelete removes the auth row for a UE. Reports whether a row was deleted.
func AuthDelete(imsi string) (bool, error) {
	u, err := UEGetByIMSI(imsi)
	if err != nil || u == nil {
		return false, err
	}
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	res, err := db.Exec(`DELETE FROM ue_auth_data WHERE ue_id=?`, u.ID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

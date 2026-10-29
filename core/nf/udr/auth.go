// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package udr — Unified Data Repository (TS 29.504 / §5.2.3).
//
// Go port of nf/udr/. The UDR is the raw subscriber data store — every
// read/write against the ue_auth_data table happens here. UDM sits on top
// of it and exposes the 3GPP-shaped service interfaces; NFs never talk
// to the UDR directly per TS 33.501 §6.1.2.
//
// This file covers the Nudr_DataRepository slice used by UE authentication:
//   - GetUeAuthData
//   - GetAllUeAuthData
//   - UpdateUeAuthData (SQN / K / OPc / AMF patches)
//   - IncrementSQN helper
package udr

import (
	"encoding/hex"
	"fmt"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/db/engine"
)

// UEAuthData is the "raw bytes" view of ue_auth_data rows used by UDM/AUSF.
// The DB layer stores hex strings; UDR decodes those to fixed-length byte
// slices so the crypto layer doesn't have to know about hex transport.
type UEAuthData struct {
	K     []byte // 16 bytes
	SQN   int64  // 48-bit counter
	OpType string // "OP" | "OPC"
	OP    []byte // 16 bytes (interpreted per OpType)
	AMF   []byte // 2 bytes, defaults to 0x8000
}

// GetUeAuthData returns the raw credentials for a SUPI, or nil when the
// subscriber isn't provisioned.
func GetUeAuthData(imsi string) (*UEAuthData, error) {
	auth, err := crud.AuthGetByIMSI(imsi)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, nil
	}
	return fromCRUD(auth)
}

// GetAllUeAuthData walks every provisioned subscriber. Ordered by IMSI.
// Used by the "UE Dashboard" web panel and by AUSF for bulk AV pre-generation.
func GetAllUeAuthData() ([]struct {
	IMSI string
	UEAuthData
}, error) {
	all, err := crud.UEList()
	if err != nil {
		return nil, err
	}
	var out []struct {
		IMSI string
		UEAuthData
	}
	for _, u := range all {
		if !u.HasAuth {
			continue
		}
		auth, err := crud.AuthGetByIMSI(u.IMSI)
		if err != nil || auth == nil {
			continue
		}
		ad, err := fromCRUD(auth)
		if err != nil {
			continue
		}
		out = append(out, struct {
			IMSI string
			UEAuthData
		}{u.IMSI, *ad})
	}
	return out, nil
}

// UpdateUeAuthData patches the auth row. Only fields set on the struct
// (non-nil / non-zero) are applied — matches the Python kwargs pattern.
func UpdateUeAuthData(imsi string, patch UEAuthData) error {
	existing, err := crud.AuthGetByIMSI(imsi)
	if err != nil {
		return err
	}
	in := crud.AuthUpsertIn{
		IMSI:   imsi,
		OpType: patch.OpType,
		OP:     hex.EncodeToString(patch.OP),
		K:      hex.EncodeToString(patch.K),
		AMF:    hex.EncodeToString(patch.AMF),
		SQN:    patch.SQN,
	}
	// Preserve existing values for fields not supplied.
	if existing != nil {
		if in.OpType == "" {
			in.OpType = existing.OpType
		}
		if len(patch.K) == 0 {
			in.K = existing.KHex
		}
		if len(patch.OP) == 0 {
			in.OP = existing.OPHex
		}
		if len(patch.AMF) == 0 {
			in.AMF = existing.AMFHex
		}
		if patch.SQN == 0 {
			in.SQN = existing.SQN
		}
	}
	if in.AMF == "" {
		in.AMF = "8000" // Milenage test-vector default
	}
	return crud.AuthUpsert(in)
}

// IncrementSQN is TS 33.102 §C.3.2 — bump the 48-bit sequence counter.
// SQN = SEQ (43 bits) || IND (5 bits).  We increment SEQ only, leaving IND=0.
func IncrementSQN(sqn int64) int64 {
	const indBits = 5
	const mask48 = (int64(1) << 48) - 1
	step := int64(1) << indBits
	return (sqn + step) & mask48
}

// DeleteUeAuthData removes the auth row for a subscriber.
func DeleteUeAuthData(imsi string) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`DELETE FROM ue_auth_data WHERE ue_id IN (SELECT id FROM ue WHERE imsi=?)`,
		imsi,
	)
	return err
}

func fromCRUD(auth *crud.AuthData) (*UEAuthData, error) {
	ad := &UEAuthData{OpType: auth.OpType, SQN: auth.SQN}
	var err error
	if ad.K, err = hex.DecodeString(auth.KHex); err != nil {
		return nil, fmt.Errorf("K decode: %w", err)
	}
	if ad.OP, err = hex.DecodeString(auth.OPHex); err != nil {
		return nil, fmt.Errorf("OP decode: %w", err)
	}
	if auth.AMFHex != "" {
		if ad.AMF, err = hex.DecodeString(auth.AMFHex); err != nil {
			return nil, fmt.Errorf("AMF decode: %w", err)
		}
	} else {
		ad.AMF = []byte{0x80, 0x00}
	}
	if len(ad.AMF) != 2 {
		return nil, fmt.Errorf("AMF must be 2 bytes, got %d", len(ad.AMF))
	}
	return ad, nil
}

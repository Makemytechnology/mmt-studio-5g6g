// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ausf — Authentication Server Function (TS 29.509 / TS 33.501 §6.1).
//
// Go port of nf/ausf/auc.py. Generates 5G-AKA authentication vectors for
// the AMF/SEAF by combining Milenage with the 5G key derivation ladder.
//
// Flow (TS 33.501 §6.1.3.2 5G-AKA):
//
//	AMF → AUSF.GenerateAV(imsi, snName)
//	  AUSF →  UDM.GetAuthData(imsi)   (which goes to UDR)
//	  AUSF →  Milenage.f1 / f2345     (MAC-A, RES, CK, IK, AK)
//	  AUSF →  ConvA2 (K_AUSF), A4 (RES*), A6 (K_SEAF), A7 (K_AMF)
//	  AUSF →  UDM.UpdateAuthSQN(imsi, SQN+1)
//	AMF ← {RAND, AUTN, XRES*, KAUSF, KSEAF, KAMF}
package ausf

import (
	"crypto/rand"
	"fmt"

	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/nf/udr"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// AuthVector is the 5G-AKA output handed to the AMF (minus the UE-side
// secrets that never cross the Nausf interface in real deployments).
//
// SEAF and AMF both live in the AMF process in this build, so we collapse
// the Nausf_UEAuthentication Boundary and return K_SEAF + K_AMF alongside
// the vector. The values are still derived in this package (= AUSF role),
// just the SEAF/AMF ownership is in-process.
type AuthVector struct {
	RAND     []byte // 16 bytes
	AUTN     []byte // 16 bytes
	XRESStar []byte // 16 bytes (RES* for HN→SN proof)
	KAUSF    []byte // 32 bytes
	KSEAF    []byte // 32 bytes
	KAMF     []byte // 32 bytes
}

// GenerateAV is the full "Nausf_UEAuthentication_Authenticate" happy path
// — pull credentials from UDM, run Milenage + 5G KDF chain, bump the SQN
// back through UDM, return the vector.
//
// `snName` is the Serving-Network-Name string the SEAF supplied
// ("5G:mnc<MNC>.mcc<MCC>.3gppnetwork.org"). `abba` is the 2-byte ABBA
// parameter (0x0000 in R15/R16).
func GenerateAV(imsi, snName string, abba []byte) (*AuthVector, error) {
	log := logger.Get("ausf")

	creds, err := udm.GetAuthData(imsi)
	if err != nil {
		return nil, err
	}
	if creds == nil {
		return nil, fmt.Errorf("ausf: no credentials for IMSI %s", imsi)
	}

	sqn := sqnBytes(creds.SQN)
	randBuf := make([]byte, 16)
	if _, err := rand.Read(randBuf); err != nil {
		return nil, fmt.Errorf("ausf: rand: %w", err)
	}

	m := sacrypto.NewMilenage(creds.OP)
	if creds.OpType == "OPC" {
		m.SetOPc(creds.OP)
	}
	mac, err := m.F1(creds.K, randBuf, sqn, creds.AMF)
	if err != nil {
		return nil, fmt.Errorf("ausf: f1: %w", err)
	}
	res, ck, ik, ak, err := m.F2345(creds.K, randBuf)
	if err != nil {
		return nil, fmt.Errorf("ausf: f2345: %w", err)
	}

	sqnXorAK := make([]byte, 6)
	for i := 0; i < 6; i++ {
		sqnXorAK[i] = sqn[i] ^ ak[i]
	}
	autn := append(append(append([]byte{}, sqnXorAK...), creds.AMF...), mac...)

	kausf, err := sacrypto.ConvA2(ck, ik, snName, sqnXorAK)
	if err != nil {
		return nil, fmt.Errorf("ausf: A2: %w", err)
	}
	xresStar, err := sacrypto.ConvA4(ck, ik, snName, randBuf, res)
	if err != nil {
		return nil, fmt.Errorf("ausf: A4: %w", err)
	}
	kseaf, err := sacrypto.ConvA6(kausf, snName)
	if err != nil {
		return nil, fmt.Errorf("ausf: A6: %w", err)
	}
	kamf, err := sacrypto.ConvA7(kseaf, []byte(imsi), abba)
	if err != nil {
		return nil, fmt.Errorf("ausf: A7: %w", err)
	}

	// Persist SQN+1 through UDM→UDR.
	if err := udm.UpdateAuthSQN(imsi, udr.IncrementSQN(creds.SQN)); err != nil {
		log.WithIMSI(imsi).Warnf("update_auth_sqn: %v", err)
	}

	return &AuthVector{
		RAND:     randBuf,
		AUTN:     autn,
		XRESStar: xresStar,
		KAUSF:    kausf,
		KSEAF:    kseaf,
		KAMF:     kamf,
	}, nil
}

// UpdateSQNOnSyncFailure is the re-sync path (TS 33.102 §6.3.5 / §C.3.2):
// the UE sends AUTS = (SQN_ms XOR AK*) ‖ MAC-S. Extract SQN_ms with
// Milenage f5*, then persist the MAX(SQN_ms, stored_SQN)+1 so the next
// GenerateAV picks up where the UE expects it.
func UpdateSQNOnSyncFailure(imsi string, auts, randBuf []byte) error {
	log := logger.Get("ausf")
	if len(auts) < 14 || len(randBuf) != 16 {
		return fmt.Errorf("ausf: bad AUTS / RAND lengths")
	}
	creds, err := udm.GetAuthData(imsi)
	if err != nil || creds == nil {
		return fmt.Errorf("ausf: sync creds for %s: %v", imsi, err)
	}
	m := sacrypto.NewMilenage(creds.OP)
	if creds.OpType == "OPC" {
		m.SetOPc(creds.OP)
	}
	ak, err := m.F5Star(creds.K, randBuf)
	if err != nil {
		return fmt.Errorf("ausf: f5*: %w", err)
	}
	// SQN_ms = AUTS[0..5] XOR AK[0..5]
	sqnMs := make([]byte, 6)
	for i := 0; i < 6; i++ {
		sqnMs[i] = auts[i] ^ ak[i]
	}
	// Pack 6 bytes big-endian into int64.
	var v int64
	for _, b := range sqnMs {
		v = (v << 8) | int64(b)
	}
	next := udr.IncrementSQN(v)
	if next < creds.SQN {
		next = creds.SQN + 1
	}
	log.WithIMSI(imsi).Infof("SQN re-sync old=%d new=%d", creds.SQN, next)
	return udm.UpdateAuthSQN(imsi, next)
}

// sqnBytes packs a 48-bit big-endian integer SQN into 6 bytes.
func sqnBytes(v int64) []byte {
	b := make([]byte, 6)
	u := uint64(v)
	for i := 5; i >= 0; i-- {
		b[i] = byte(u)
		u >>= 8
	}
	return b
}

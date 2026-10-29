// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5G key derivation functions per TS 33.501 Annex A. Go port of the subset
// used by the AMF's registration + authentication path. Additional
// helpers (A10..A23) land incrementally as they're needed by handover and
// SRVCC procedures.
package sacrypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// KDF is the generic HMAC-SHA-256 key derivation (TS 33.220 §B.2.0).
func KDF(key, s []byte) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(s)
	return h.Sum(nil)
}

// ConvA2 derives K_AUSF from (CK, IK, SN name, SQN xor AK). TS 33.501 §A.2.
// sn_name is the 3GPP "5G:mnc<MNC>.mcc<MCC>.3gppnetwork.org" string.
func ConvA2(ck, ik []byte, snName string, sqnXorAK []byte) ([]byte, error) {
	if len(ck) != 16 || len(ik) != 16 || len(snName) < 32 || len(sqnXorAK) != 6 {
		return nil, ErrInvalidArg
	}
	p0 := []byte(snName)
	s := []byte{0x6a}
	s = append(s, p0...)
	s = append(s, be16(uint16(len(p0)))...)
	s = append(s, sqnXorAK...)
	s = append(s, be16(6)...)
	return KDF(concat(ck, ik), s), nil
}

// ConvA4 derives RES* from (CK, IK, SN name, RAND, RES). TS 33.501 §A.4.
// Returns 16 bytes; callers typically use the last 16 as RES*.
func ConvA4(ck, ik []byte, snName string, rand, res []byte) ([]byte, error) {
	if len(ck) != 16 || len(ik) != 16 || len(snName) < 32 ||
		len(rand) != 16 || len(res) < 4 || len(res) > 16 {
		return nil, ErrInvalidArg
	}
	p0 := []byte(snName)
	s := []byte{0x6b}
	s = append(s, p0...)
	s = append(s, be16(uint16(len(p0)))...)
	s = append(s, rand...)
	s = append(s, be16(0x10)...)
	s = append(s, res...)
	s = append(s, be16(uint16(len(res)))...)
	out := KDF(concat(ck, ik), s)
	return out[16:], nil
}

// ConvA6 derives K_SEAF from (K_AUSF, SN name). TS 33.501 §A.6.
func ConvA6(kausf []byte, snName string) ([]byte, error) {
	if len(kausf) != 32 || len(snName) < 32 {
		return nil, ErrInvalidArg
	}
	p0 := []byte(snName)
	s := []byte{0x6c}
	s = append(s, p0...)
	s = append(s, be16(uint16(len(p0)))...)
	return KDF(kausf, s), nil
}

// ConvA7 derives K_AMF from (K_SEAF, SUPI, ABBA). TS 33.501 §A.7.
func ConvA7(kseaf []byte, supi []byte, abba []byte) ([]byte, error) {
	if len(kseaf) != 32 || len(supi) < 12 || len(abba) != 2 {
		return nil, ErrInvalidArg
	}
	s := []byte{0x6d}
	s = append(s, supi...)
	s = append(s, be16(uint16(len(supi)))...)
	s = append(s, abba...)
	s = append(s, be16(2)...)
	return KDF(kseaf, s), nil
}

// AlgType values passed to ConvA8 (TS 33.501 Annex A Table A.8-1).
const (
	AlgTypeNASEnc uint8 = 1 // N-NAS-enc-alg
	AlgTypeNASInt uint8 = 2 // N-NAS-int-alg
	AlgTypeRRCEnc uint8 = 3
	AlgTypeRRCInt uint8 = 4
	AlgTypeUPEnc  uint8 = 5
	AlgTypeUPInt  uint8 = 6
)

// ConvA8 derives a per-algorithm NAS/RRC/UP key from K_AMF (or K_gNB).
// TS 33.501 §A.8. algType is one of AlgType*.
func ConvA8(k []byte, algType, algID uint8) ([]byte, error) {
	if len(k) != 32 {
		return nil, ErrInvalidArg
	}
	s := []byte{0x69}
	s = append(s, algType)
	s = append(s, be16(1)...)
	s = append(s, algID)
	s = append(s, be16(1)...)
	// TS 33.501 A.8 returns 32 bytes; algorithm-specific keys use the low 16.
	return KDF(k, s), nil
}

// ConvA9 derives K_gNB (or K_N3IWF) from K_AMF + UL NAS count + access type.
// TS 33.501 §A.9. accTypeDist is 1 for 3GPP, 2 for non-3GPP.
func ConvA9(kamf []byte, ulNasCount uint32, accTypeDist uint8) ([]byte, error) {
	if len(kamf) != 32 || (accTypeDist != 1 && accTypeDist != 2) {
		return nil, ErrInvalidArg
	}
	s := []byte{0x6e}
	s = append(s, be32(ulNasCount)...)
	s = append(s, be16(4)...)
	s = append(s, accTypeDist)
	s = append(s, be16(1)...)
	return KDF(kamf, s), nil
}

// ServingNetworkName builds the TS 33.501 §6.1.1.4 serving network name string
// "5G:mnc<MNC3>.mcc<MCC3>.3gppnetwork.org".
func ServingNetworkName(mcc, mnc string) string {
	// Pad MNC to 3 digits per 3GPP TS 23.003.
	if len(mnc) == 2 {
		mnc = "0" + mnc
	}
	return "5G:mnc" + mnc + ".mcc" + mcc + ".3gppnetwork.org"
}

// Errors / helpers ───────────────────────────────────────────────────────

// ErrBadConv is returned by ConvA* when sanity checks fail that aren't
// length-related (e.g. extension-only branches).
var ErrBadConv = errors.New("sacrypto: conversion failed")

func be16(v uint16) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return b[:]
}
func be32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}
func concat(a, b []byte) []byte {
	out := make([]byte, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

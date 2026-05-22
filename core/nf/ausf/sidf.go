// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SIDF — Subscription Identifier De-concealing Function (TS 33.501 section 6.12).
//
// Go port of nf/ausf/sidf.py. Decrypts SUCI (concealed SUPI) to recover
// the SUPI (IMSI). The SIDF is co-located with the UDM in production but
// lives in the AUSF package for this deployment.
//
// SUCI structure:
//   - PLMN (MCC+MNC)
//   - Routing Indicator
//   - Protection Scheme ID: 0=Null, 1=Profile A (X25519), 2=Profile B (secp256r1)
//   - Home Network Public Key ID (HNPKID)
//   - Scheme Output: encrypted MSIN (for ECIES) or plaintext MSIN (for Null)
//
// ECIES Profile A: Curve25519 / X25519
//   - Ephemeral public key: 32 bytes
//   - Ciphertext: variable (encrypted BCD MSIN)
//   - MAC tag: 8 bytes (HMAC-SHA256 truncated)
//
// ECIES Profile B: NIST secp256r1
//   - Ephemeral public key: 33 bytes (compressed point)
//   - Ciphertext: variable
//   - MAC tag: 8 bytes
package ausf

import (
	"encoding/hex"
	"fmt"

	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// SIDFProfile identifies the ECIES profile: 'A' = X25519, 'B' = secp256r1.
type SIDFProfile byte

const (
	ProfileA SIDFProfile = 'A'
	ProfileB SIDFProfile = 'B'
)

// HNKeyLookupFunc is called to retrieve the home network private key for
// SUCI decryption. Returns (profile, privateKeyHex) or error.
type HNKeyLookupFunc func(mcc, mnc string, hnpkid uint8) (SIDFProfile, string, error)

// DeconcealSUCI decrypts SUCI scheme output to recover plaintext MSIN bytes.
//
// TS 33.501 section 6.12.2 — ECIES decryption.
//
// Args:
//
//	hnPrivKeyHex: Home network private key as hex string.
//	profile: ProfileA (X25519) or ProfileB (secp256r1).
//	uePubKey: UE ephemeral public key (bytes).
//	ciphertext: Encrypted MSIN (bytes).
//	macTag: 8-byte MAC tag (bytes).
//
// Returns plaintext MSIN bytes, or error if decryption/MAC fails.
func DeconcealSUCI(hnPrivKeyHex string, profile SIDFProfile, uePubKey, ciphertext, macTag []byte) ([]byte, error) {
	log := logger.Get("ausf.sidf")

	hnPrivKey, err := hex.DecodeString(hnPrivKeyHex)
	if err != nil {
		return nil, fmt.Errorf("sidf: bad HN private key hex: %w", err)
	}

	plaintext, err := sacrypto.ECIESDecrypt(hnPrivKey, string(profile), uePubKey, ciphertext, macTag)
	if err != nil {
		log.Errorf("SUCI decryption failed: %v", err)
		return nil, fmt.Errorf("sidf: ECIES decrypt: %w", err)
	}

	log.Debugf("SUCI decrypted MSIN: %s", hex.EncodeToString(plaintext))
	return plaintext, nil
}

// ExtractECIESParams splits the raw scheme output into (uePubKey, ciphertext, macTag).
//
// Profile A: ECCEphemPK=32B, CipherText=variable, MAC=8B
// Profile B: ECCEphemPK=33B, CipherText=variable, MAC=8B
func ExtractECIESParams(raw []byte, profile SIDFProfile) (uePubKey, ciphertext, macTag []byte, err error) {
	pkLen := 32
	if profile == ProfileB {
		pkLen = 33
	}
	macLen := 8

	if len(raw) < pkLen+macLen+1 {
		return nil, nil, nil, fmt.Errorf("sidf: ECIES output too short: %d bytes (need >= %d)",
			len(raw), pkLen+macLen+1)
	}

	uePubKey = raw[:pkLen]
	macTag = raw[len(raw)-macLen:]
	ciphertext = raw[pkLen : len(raw)-macLen]
	return
}

// DecodeBCDMSIN decodes BCD-encoded MSIN bytes to a digit string.
// Each byte contains two BCD digits: low nibble first, high nibble second.
// Nibble values >= 0xA are padding (typically 0xF) and are skipped.
func DecodeBCDMSIN(msinBytes []byte) string {
	digits := make([]byte, 0, len(msinBytes)*2)
	for _, b := range msinBytes {
		lo := b & 0x0F
		hi := (b >> 4) & 0x0F
		if lo < 0x0A {
			digits = append(digits, '0'+lo)
		}
		if hi < 0x0A {
			digits = append(digits, '0'+hi)
		}
	}
	return string(digits)
}

// DecryptSUCIFromNAS handles the full SUCI decryption flow from NAS-decoded
// identity parameters. Mirrors Python decrypt_suci_from_pycrate.
//
// Args:
//
//	mcc, mnc: PLMN from the SUCI.
//	protSchemeID: 0=Null, 1=Profile A, 2=Profile B.
//	hnpkid: Home Network Public Key Identifier.
//	schemeOutput: Raw scheme output bytes.
//	keyLookup: Function to retrieve HN private key.
//
// Returns (mcc, mnc, msinString) on success.
func DecryptSUCIFromNAS(mcc, mnc string, protSchemeID, hnpkid uint8, schemeOutput []byte, keyLookup HNKeyLookupFunc) (string, string, string, error) {
	log := logger.Get("ausf.sidf")

	log.Infof("SUCI: MCC=%s MNC=%s scheme=%d HNPKID=%d", mcc, mnc, protSchemeID, hnpkid)

	// Null scheme — plaintext MSIN (TS 33.501 section 6.12.2)
	if protSchemeID == 0 {
		msin := DecodeBCDMSIN(schemeOutput)
		log.WithIMSI(mcc + mnc + msin).Info("SUCI resolved (null scheme)")
		return mcc, mnc, msin, nil
	}

	// ECIES Profile A or B
	if protSchemeID != 1 && protSchemeID != 2 {
		return "", "", "", fmt.Errorf("sidf: unsupported protection scheme %d", protSchemeID)
	}

	profile := ProfileA
	if protSchemeID == 2 {
		profile = ProfileB
	}

	// Lookup home network private key
	if keyLookup == nil {
		return "", "", "", fmt.Errorf("sidf: no HN key lookup function provided")
	}
	_, hnPrivKeyHex, err := keyLookup(mcc, mnc, hnpkid)
	if err != nil {
		return "", "", "", fmt.Errorf("sidf: HN key lookup MCC=%s MNC=%s HNPKID=%d: %w",
			mcc, mnc, hnpkid, err)
	}

	// Extract ECIES parameters
	uePubKey, ciphertext, macTag, err := ExtractECIESParams(schemeOutput, profile)
	if err != nil {
		return "", "", "", err
	}

	// Decrypt
	msinBytes, err := DeconcealSUCI(hnPrivKeyHex, profile, uePubKey, ciphertext, macTag)
	if err != nil {
		return "", "", "", err
	}

	msin := DecodeBCDMSIN(msinBytes)
	log.WithIMSI(mcc + mnc + msin).Info("SUCI decrypted")
	return mcc, mnc, msin, nil
}

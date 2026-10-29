// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package profile — eSIM profile lifecycle helpers: ICCID
// allocation + Luhn checksum, Activation Code, AES-CBC + HMAC
// session-key crypto, USIM profile shape.
//
// Spec anchors:
//
//   - TS 31.102 §4.2.2     EF_IMSI on the USIM ADF — the file the
//                          profile populates when installed.
//   - TS 31.102 §4.2.18    EF_AD (Administrative Data) — encodes
//                          MNC length; ef_ad in BuildUSIMProfile
//                          carries the byte tuple.
//   - TS 31.102 §4.2       USIM ADF file structure (umbrella).
//   - TS 33.501 §6.1.3     5G AKA — the authentication procedure
//                          the K / OPc carried in the profile feed.
//
// Non-3GPP references (not §-cited because they're not in the
// speccheck DOC_MAP):
//
//   - ITU-T E.118          ICCID structure + Luhn checksum.
//   - GSMA SGP.22 §4.1     Activation Code format
//                          ("LPA:1$<smdp>$<matchingID>").
//   - GSMA SGP.22 §2.5.3   BPP encryption envelope (AES-CBC + HMAC
//                          mirrored here at a structural level).
//
// TODO GSMA SGP.22 §2.5.3 — switch the BPP envelope from a JSON
//                           map to the spec-mandated ASN.1
//                           encoding once the wire codec lands.
// TODO TS 31.102 §5.2.1   — Milenage parameter blob layout (this
//                           module stores K + OPc as hex strings;
//                           SQN delta + AMF aren't part of the
//                           profile blob yet).
package profile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("esim.profile")

// ── ICCID Manager ── ITU-T E.118 (Industry Identifier "89" + Luhn).

var iccidMu sync.Mutex

func luhnChecksum(digits string) int {
	total := 0
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if (len(digits)-1-i)%2 == 0 {
			n *= 2; if n > 9 { n -= 9 }
		}
		total += n
	}
	return (10 - (total % 10)) % 10
}

// AllocateICCID allocates the next ICCID (19 digits).
func AllocateICCID(issuerID string) string {
	iccidMu.Lock()
	defer iccidMu.Unlock()
	engine.Exec(`INSERT OR IGNORE INTO esim_iccid_counter (id, issuer_id, next_sequence) VALUES (1, '8901', 1)`)
	row := engine.QueryRow(`SELECT issuer_id, next_sequence FROM esim_iccid_counter WHERE id=1`)
	var dbIssuer string; var seq int
	row.Scan(&dbIssuer, &seq)
	if issuerID == "" { issuerID = dbIssuer }
	engine.Exec(`UPDATE esim_iccid_counter SET next_sequence=? WHERE id=1`, seq+1)
	body := fmt.Sprintf("89%s%012d", issuerID, seq)
	check := luhnChecksum(body)
	return fmt.Sprintf("%s%d", body, check)
}

// ValidateICCID checks format and Luhn.
func ValidateICCID(iccid string) bool {
	if len(iccid) < 18 || len(iccid) > 20 { return false }
	for _, c := range iccid { if c < '0' || c > '9' { return false } }
	if iccid[:2] != "89" { return false }
	expected := luhnChecksum(iccid[:len(iccid)-1])
	actual, _ := strconv.Atoi(string(iccid[len(iccid)-1]))
	return actual == expected
}

// ── Activation Code ── GSMA SGP.22 §4.1 (informative; not §-checked).

// GenerateMatchingID returns a 32-char hex string.
func GenerateMatchingID() string {
	b := make([]byte, 16); rand.Read(b)
	return hex.EncodeToString(b)
}

// GenerateActivationCode builds LPA:1$<smdp>$<matchingID>.
func GenerateActivationCode(smdpAddress, matchingID string) string {
	return fmt.Sprintf("LPA:1$%s$%s", smdpAddress, matchingID)
}

// ParseActivationCode parses an activation code.
func ParseActivationCode(ac string) map[string]string {
	if len(ac) < 6 || ac[:6] != "LPA:1$" { return nil }
	parts := splitDollar(ac[6:])
	if len(parts) < 2 { return nil }
	return map[string]string{"smdp_address": parts[0], "matching_id": parts[1]}
}

func splitDollar(s string) []string {
	var parts []string; start := 0
	for i, c := range s { if c == '$' { parts = append(parts, s[start:i]); start = i + 1 } }
	return append(parts, s[start:])
}

// ── Profile Crypto ── GSMA SGP.22 §2.5.3 BPP envelope (informative).
// Real BPP is ASN.1; this module models the (IV ‖ ciphertext ‖ MAC)
// shape used at the structural level. See package-level TODO.

type SessionKeys struct {
	EncKey []byte `json:"enc_key"`
	MacKey []byte `json:"mac_key"`
	DEK    []byte `json:"dek"`
}

func GenerateSessionKeys() *SessionKeys {
	ek := make([]byte, 16); rand.Read(ek)
	mk := make([]byte, 16); rand.Read(mk)
	dk := make([]byte, 16); rand.Read(dk)
	return &SessionKeys{EncKey: ek, MacKey: mk, DEK: dk}
}

func EncryptProfile(data []byte, keys *SessionKeys) map[string]string {
	iv := make([]byte, 16); rand.Read(iv)
	block, _ := aes.NewCipher(keys.EncKey)
	// PKCS7 pad
	pad := aes.BlockSize - len(data)%aes.BlockSize
	padded := make([]byte, len(data)+pad)
	copy(padded, data)
	for i := len(data); i < len(padded); i++ { padded[i] = byte(pad) }
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)
	mac := hmac.New(sha256.New, keys.MacKey)
	mac.Write(iv); mac.Write(ct)
	return map[string]string{
		"iv": hex.EncodeToString(iv), "ciphertext": hex.EncodeToString(ct),
		"mac": hex.EncodeToString(mac.Sum(nil)),
	}
}

func DecryptProfile(enc map[string]string, keys *SessionKeys) []byte {
	iv, _ := hex.DecodeString(enc["iv"])
	ct, _ := hex.DecodeString(enc["ciphertext"])
	expectedMac, _ := hex.DecodeString(enc["mac"])
	mac := hmac.New(sha256.New, keys.MacKey); mac.Write(iv); mac.Write(ct)
	if !hmac.Equal(mac.Sum(nil), expectedMac) { return nil }
	block, _ := aes.NewCipher(keys.EncKey)
	plain := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, ct)
	// Remove PKCS7 padding
	if len(plain) > 0 { pad := int(plain[len(plain)-1]); if pad <= aes.BlockSize { plain = plain[:len(plain)-pad] } }
	return plain
}

// ── USIM Profile Builder ── TS 31.102 §4.2 (USIM ADF EF contents).
//
// The fields here populate the on-card EFs once the profile is
// installed (TS 31.102 §4.2.2 EF_IMSI, §4.2.18 EF_AD). K + OPc feed
// the AKA computation of TS 33.501 §6.1.3.

// BuildUSIMProfile creates a profile data structure. The ef_ad
// byte format follows TS 31.102 §4.2.18 — three reserved bytes
// then the MNC-length byte.
func BuildUSIMProfile(imsi, kHex, opcHex, iccid, mcc, mnc, opType string) map[string]interface{} {
	mncLen := 2; if len(mnc) == 3 { mncLen = 3 }
	return map[string]interface{}{
		"version": "2.3.1", "iccid": iccid, "imsi": imsi,
		"mcc": mcc, "mnc": mnc, "op_type": opType,
		"k": kHex, "opc": opcHex,
		// EF_AD per TS 31.102 §4.2.18: byte4 = MNC length.
		"ef_ad": fmt.Sprintf("000000%02x", mncLen),
		"algorithm": "milenage",
		"access_rules": map[string]interface{}{
			"rat_list":  []string{"e-utran", "nr"},
			"plmn_list": []map[string]string{{"mcc": mcc, "mnc": mnc}},
		},
	}
}

// GenerateQRData returns the data to encode in a QR code for eSIM activation.
func GenerateQRData(activationCode string) map[string]string {
	return map[string]string{"content": activationCode, "format": "SGP.22-v2.3.1"}
}

// DeserializeProfile deserializes JSON bytes to a profile map.
func DeserializeProfile(data []byte) map[string]interface{} {
	var m map[string]interface{}
	json.Unmarshal(data, &m)
	return m
}

// SerializeProfile serializes to JSON bytes.
func SerializeProfile(profile map[string]interface{}) []byte {
	b, _ := json.MarshalIndent(profile, "", "  "); return b
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// ECIES for SUCI concealment / de-concealment (TS 33.501 §6.12, Annex C).
//
// Profile A: Curve25519 / X25519
// Profile B: NIST secp256r1 / P-256
// KDF: ANSI X9.63 with SHA-256 (64 bytes: AES key + nonce + counter + HMAC key)
// Encryption: AES-128-CTR
// MAC: HMAC-SHA-256 (truncated to 8 bytes)
//
// Go implementation using crypto/ecdh (Go 1.20+) for X25519 + P-256.
// No external dependencies — stdlib only.
package sacrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
)

// ErrMACMismatch is returned when the MAC tag verification fails during
// SUCI de-concealment — the ciphertext was tampered with or the wrong
// home-network private key was used.
var ErrMACMismatch = errors.New("ecies: MAC verification failed")

// ── Profile A: X25519 ──────────────────────────────────────────────────

// ECIESProfileA handles X25519-based ECIES (TS 33.501 Annex C.3.4).
type ECIESProfileA struct {
	privKey *ecdh.PrivateKey
}

// NewProfileA creates a Profile A context. privKey is the 32-byte
// Curve25519 private key (HN side). Pass nil for UE side (ephemeral
// key is generated on Protect).
func NewProfileA(privKey []byte) (*ECIESProfileA, error) {
	e := &ECIESProfileA{}
	if privKey != nil {
		k, err := ecdh.X25519().NewPrivateKey(privKey)
		if err != nil {
			return nil, fmt.Errorf("ecies: X25519 private key: %w", err)
		}
		e.privKey = k
	}
	return e, nil
}

// Protect conceals plaintext MSIN using the HN public key (UE side).
// Returns (ue_ephemeral_pubkey, ciphertext, mac_tag_8bytes).
func (e *ECIESProfileA) Protect(hnPubKey, plaintext []byte) (pubKey, ciphertext, mac []byte, err error) {
	hnPub, err := ecdh.X25519().NewPublicKey(hnPubKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ecies: HN public key: %w", err)
	}
	ephPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	shared, err := ephPriv.ECDH(hnPub)
	if err != nil {
		return nil, nil, nil, err
	}
	ephPub := ephPriv.PublicKey().Bytes()
	sk := x963KDF(ephPub, shared, 64)
	ct, tag := eciesEncrypt(sk, plaintext)
	return ephPub, ct, tag, nil
}

// Unprotect recovers plaintext MSIN from SUCI (HN side).
// Returns nil if MAC verification fails.
func (e *ECIESProfileA) Unprotect(uePubKey, ciphertext, macTag []byte) ([]byte, error) {
	if e.privKey == nil {
		return nil, errors.New("ecies: no private key (HN side required)")
	}
	uePub, err := ecdh.X25519().NewPublicKey(uePubKey)
	if err != nil {
		return nil, fmt.Errorf("ecies: UE public key: %w", err)
	}
	shared, err := e.privKey.ECDH(uePub)
	if err != nil {
		return nil, err
	}
	sk := x963KDF(uePubKey, shared, 64)
	return eciesDecrypt(sk, ciphertext, macTag)
}

// ── Profile B: secp256r1 / P-256 ───────────────────────────────────────

// ECIESProfileB handles P-256-based ECIES (TS 33.501 Annex C.3.5).
type ECIESProfileB struct {
	privKey *ecdh.PrivateKey
}

// NewProfileB creates a Profile B context. privKey is the 32-byte
// P-256 scalar (big-endian). Pass nil for UE side.
func NewProfileB(privKey []byte) (*ECIESProfileB, error) {
	e := &ECIESProfileB{}
	if privKey != nil {
		k, err := ecdh.P256().NewPrivateKey(privKey)
		if err != nil {
			return nil, fmt.Errorf("ecies: P-256 private key: %w", err)
		}
		e.privKey = k
	}
	return e, nil
}

// Protect conceals plaintext MSIN using the HN public key (UE side).
// hnPubKey is the compressed X9.62 point (33 bytes).
func (e *ECIESProfileB) Protect(hnPubKey, plaintext []byte) (pubKey, ciphertext, mac []byte, err error) {
	hnPub, err := parseP256PubKey(hnPubKey)
	if err != nil {
		return nil, nil, nil, err
	}
	ephPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	shared, err := ephPriv.ECDH(hnPub)
	if err != nil {
		return nil, nil, nil, err
	}
	// Compressed point for the ephemeral public key.
	ephPub := compressP256(ephPriv.PublicKey())
	sk := x963KDF(ephPub, shared, 64)
	ct, tag := eciesEncrypt(sk, plaintext)
	return ephPub, ct, tag, nil
}

// Unprotect recovers plaintext MSIN from SUCI (HN side).
func (e *ECIESProfileB) Unprotect(uePubKey, ciphertext, macTag []byte) ([]byte, error) {
	if e.privKey == nil {
		return nil, errors.New("ecies: no private key (HN side required)")
	}
	uePub, err := parseP256PubKey(uePubKey)
	if err != nil {
		return nil, err
	}
	shared, err := e.privKey.ECDH(uePub)
	if err != nil {
		return nil, err
	}
	sk := x963KDF(uePubKey, shared, 64)
	return eciesDecrypt(sk, ciphertext, macTag)
}

// ECIESDecrypt is the top-level convenience function for SUCI de-concealment.
// profile is "A" (X25519) or "B" (P-256).
func ECIESDecrypt(hnPrivKey []byte, profile string, uePubKey, ciphertext, macTag []byte) ([]byte, error) {
	switch profile {
	case "A":
		ctx, err := NewProfileA(hnPrivKey)
		if err != nil {
			return nil, err
		}
		return ctx.Unprotect(uePubKey, ciphertext, macTag)
	case "B":
		ctx, err := NewProfileB(hnPrivKey)
		if err != nil {
			return nil, err
		}
		return ctx.Unprotect(uePubKey, ciphertext, macTag)
	default:
		return nil, fmt.Errorf("ecies: unknown profile %q", profile)
	}
}

// ── Common ECIES encrypt / decrypt ─────────────────────────────────────

// eciesEncrypt does AES-128-CTR + HMAC-SHA-256 truncated to 8 bytes.
// sk layout: [AES key (16)] [nonce (8)] [counter (8)] [HMAC key (32)]
func eciesEncrypt(sk, plaintext []byte) (ciphertext, mac []byte) {
	aesKey := sk[:16]
	nonce := sk[16:24]
	counter := binary.BigEndian.Uint64(sk[24:32])
	macKey := sk[32:64]

	// Build 16-byte AES-CTR IV: nonce(8) || counter(8)
	iv := make([]byte, aes.BlockSize)
	copy(iv[:8], nonce)
	binary.BigEndian.PutUint64(iv[8:], counter)

	block, _ := aes.NewCipher(aesKey)
	ct := make([]byte, len(plaintext))
	cipher.NewCTR(block, iv).XORKeyStream(ct, plaintext)

	h := hmac.New(sha256.New, macKey)
	h.Write(ct)
	tag := h.Sum(nil)[:8]
	return ct, tag
}

func eciesDecrypt(sk, ciphertext, macTag []byte) ([]byte, error) {
	aesKey := sk[:16]
	nonce := sk[16:24]
	counter := binary.BigEndian.Uint64(sk[24:32])
	macKey := sk[32:64]

	// Verify MAC first.
	h := hmac.New(sha256.New, macKey)
	h.Write(ciphertext)
	expected := h.Sum(nil)[:8]
	if !hmac.Equal(expected, macTag) {
		return nil, ErrMACMismatch
	}

	iv := make([]byte, aes.BlockSize)
	copy(iv[:8], nonce)
	binary.BigEndian.PutUint64(iv[8:], counter)

	block, _ := aes.NewCipher(aesKey)
	pt := make([]byte, len(ciphertext))
	cipher.NewCTR(block, iv).XORKeyStream(pt, ciphertext)
	return pt, nil
}

// ── ANSI X9.63 KDF ─────────────────────────────────────────────────────

// x963KDF derives `length` bytes from sharedKey using SHA-256.
// sharedInfo is the UE ephemeral public key (TS 33.501 Annex C).
func x963KDF(sharedInfo, sharedKey []byte, length int) []byte {
	var out []byte
	counter := uint32(1)
	for len(out) < length {
		h := sha256.New()
		h.Write(sharedKey)
		var ctr [4]byte
		binary.BigEndian.PutUint32(ctr[:], counter)
		h.Write(ctr[:])
		h.Write(sharedInfo)
		out = append(out, h.Sum(nil)...)
		counter++
	}
	return out[:length]
}

// ── P-256 point helpers ────────────────────────────────────────────────

// parseP256PubKey parses a compressed (33 bytes) or uncompressed (65 bytes)
// X9.62 point into an *ecdh.PublicKey.
func parseP256PubKey(data []byte) (*ecdh.PublicKey, error) {
	if len(data) == 33 {
		// Compressed → decompress to uncompressed for ecdh.P256().NewPublicKey
		data = decompressP256(data)
		if data == nil {
			return nil, errors.New("ecies: P-256 point decompression failed")
		}
	}
	return ecdh.P256().NewPublicKey(data)
}

// compressP256 returns the 33-byte compressed X9.62 form of a P-256 point.
func compressP256(pub *ecdh.PublicKey) []byte {
	raw := pub.Bytes() // 65 bytes: 04 || X(32) || Y(32)
	x := raw[1:33]
	y := raw[33:65]
	prefix := byte(0x02)
	if y[31]&1 != 0 {
		prefix = 0x03
	}
	out := make([]byte, 33)
	out[0] = prefix
	copy(out[1:], x)
	return out
}

// decompressP256 recovers the uncompressed 65-byte form from a 33-byte
// compressed point using the P-256 curve equation y^2 = x^3 - 3x + b.
func decompressP256(compressed []byte) []byte {
	if len(compressed) != 33 || (compressed[0] != 0x02 && compressed[0] != 0x03) {
		return nil
	}
	p256 := ecdh.P256()
	_ = p256 // curve params accessed via math/big below

	// P-256 parameters
	P, _ := new(big.Int).SetString("FFFFFFFF00000001000000000000000000000000FFFFFFFFFFFFFFFFFFFFFFFF", 16)
	B, _ := new(big.Int).SetString("5AC635D8AA3A93E7B3EBBD55769886BC651D06B0CC53B0F63BCE3C3E27D2604B", 16)

	x := new(big.Int).SetBytes(compressed[1:33])

	// y^2 = x^3 - 3x + B mod P
	x3 := new(big.Int).Mul(x, x)
	x3.Mul(x3, x)
	x3.Mod(x3, P)

	threeX := new(big.Int).Mul(big.NewInt(3), x)
	threeX.Mod(threeX, P)

	y2 := new(big.Int).Sub(x3, threeX)
	y2.Add(y2, B)
	y2.Mod(y2, P)

	// y = sqrt(y2) mod P
	// P ≡ 3 mod 4, so y = y2^((P+1)/4) mod P
	exp := new(big.Int).Add(P, big.NewInt(1))
	exp.Rsh(exp, 2)
	y := new(big.Int).Exp(y2, exp, P)

	// Check parity
	if y.Bit(0) != uint(compressed[0]&1) {
		y.Sub(P, y)
	}

	// Verify
	yy := new(big.Int).Mul(y, y)
	yy.Mod(yy, P)
	if yy.Cmp(y2) != 0 {
		return nil
	}

	out := make([]byte, 65)
	out[0] = 0x04
	xBytes := x.Bytes()
	yBytes := y.Bytes()
	copy(out[1+32-len(xBytes):33], xBytes)
	copy(out[33+32-len(yBytes):65], yBytes)
	return out
}

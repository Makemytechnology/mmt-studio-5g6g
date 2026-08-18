// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package sacrypto — 3GPP cryptographic primitives used by the 5G core.
//
// Go port of libs/sa_crypto/. Clean-room implementation per 3GPP TS 35.205,
// 35.206, 35.207 (Milenage) and TS 33.501 Annex A (5G key derivation).
//
// Milenage is the AES-based authentication algorithm set used by every USIM
// shipping today. f1 produces MAC-A for network authentication, f1* is the
// re-sync variant, and f2345 produces (RES, CK, IK, AK) in one AES pass.
package sacrypto

import (
	"crypto/aes"
	"encoding/binary"
	"errors"
)

// ErrInvalidArg is returned when the caller hands in a buffer of the
// wrong length (K/RAND/SQN/AMF/RES lengths are fixed by the standard).
var ErrInvalidArg = errors.New("sacrypto: invalid argument length")

// Milenage carries the operator constants and optional cached OPc.
//
// The five recommended rotation/XOR constants (c1..c5, r1..r5) are fixed by
// TS 35.206 §4.1 — callers never tweak them in practice.
type Milenage struct {
	OP  []byte // 16 bytes
	OPc []byte // 16 bytes, optional; computed lazily from K+OP
}

// NewMilenage constructs a Milenage instance for the given OP.
func NewMilenage(op []byte) *Milenage {
	cp := make([]byte, len(op))
	copy(cp, op)
	return &Milenage{OP: cp}
}

// SetOPc caches an operator-specific OPc so repeated f1/f2345 calls avoid
// the extra AES round that derives OPc from K+OP.
func (m *Milenage) SetOPc(opc []byte) {
	m.OPc = append(m.OPc[:0], opc...)
}

// UnsetOPc clears the cached OPc.
func (m *Milenage) UnsetOPc() { m.OPc = nil }

func (m *Milenage) getOPc(k, op []byte) ([]byte, error) {
	if m.OPc != nil {
		return m.OPc, nil
	}
	if op == nil {
		op = m.OP
	}
	return MakeOPc(k, op)
}

// MakeOPc returns OPc = AES_K(OP) XOR OP. TS 35.206 §4.1.
func MakeOPc(k, op []byte) ([]byte, error) {
	if len(k) != 16 || len(op) != 16 {
		return nil, ErrInvalidArg
	}
	c, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	c.Encrypt(out, op)
	for i := 0; i < 16; i++ {
		out[i] ^= op[i]
	}
	return out, nil
}

// f1 computes MAC-A (8 bytes) — the network authentication token carried
// inside AUTN (TS 35.206 §4.1, TS 33.501 §6.1.3.1).
//
//	K   : 16 bytes (subscriber key)
//	RAND: 16 bytes (HN-chosen random)
//	SQN : 6  bytes (sequence number)
//	AMF : 2  bytes (authentication management field)
func (m *Milenage) F1(k, rand, sqn, amf []byte) ([]byte, error) {
	return m.f1pair(k, rand, sqn, amf, false)
}

// F1Star computes MAC-S (8 bytes) — the re-sync MAC used when the UE reports
// a synchronisation failure (TS 35.206 §4.1, TS 33.102 §6.3.5).
func (m *Milenage) F1Star(k, rand, sqn, amf []byte) ([]byte, error) {
	return m.f1pair(k, rand, sqn, amf, true)
}

func (m *Milenage) f1pair(k, rand, sqn, amf []byte, wantStar bool) ([]byte, error) {
	if len(k) != 16 || len(rand) != 16 || len(sqn) != 6 || len(amf) != 2 {
		return nil, ErrInvalidArg
	}
	opc, err := m.getOPc(k, nil)
	if err != nil {
		return nil, err
	}
	cipher, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	// TEMP = E_K(RAND XOR OPc)
	tmp := make([]byte, 16)
	xorInto(tmp, rand, opc)
	cipher.Encrypt(tmp, tmp)

	// IN1 = SQN ‖ AMF ‖ SQN ‖ AMF  (16 bytes)
	in1 := make([]byte, 16)
	copy(in1, sqn)
	copy(in1[6:], amf)
	copy(in1[8:], sqn)
	copy(in1[14:], amf)

	// rot1 = rotate_left(IN1 XOR OPc, r1=0x40) XOR c1 (zeros)
	rotBuf := xorNew(in1, opc)
	rotBuf = rotLeft16(rotBuf, 0x40)
	// c1 = all zero, no XOR needed.

	// OUT1 = E_K(rot1 XOR TEMP) XOR OPc
	xorBuf := xorNew(rotBuf, tmp)
	out1 := make([]byte, 16)
	cipher.Encrypt(out1, xorBuf)
	for i := range out1 {
		out1[i] ^= opc[i]
	}

	if wantStar {
		return append([]byte(nil), out1[8:16]...), nil
	}
	return append([]byte(nil), out1[0:8]...), nil
}

// F2345 returns (RES[8], CK[16], IK[16], AK[6]) per TS 35.206 §4.1.
func (m *Milenage) F2345(k, rand []byte) (res, ck, ik, ak []byte, err error) {
	if len(k) != 16 || len(rand) != 16 {
		return nil, nil, nil, nil, ErrInvalidArg
	}
	opc, err := m.getOPc(k, nil)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	cipher, err := aes.NewCipher(k)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	// TEMP = E_K(OPc XOR RAND) XOR OPc
	tmp := xorNew(opc, rand)
	cipher.Encrypt(tmp, tmp)
	for i := range tmp {
		tmp[i] ^= opc[i]
	}

	out2 := aesXORRot(cipher, tmp, 0x00, c2, opc)
	out3 := aesXORRot(cipher, tmp, 0x20, c3, opc)
	out4 := aesXORRot(cipher, tmp, 0x40, c4, opc)

	// RES = low 8 bytes of OUT2 (bits 64..127); AK = high 6 bytes of OUT2.
	res = append([]byte(nil), out2[8:16]...)
	ck = append([]byte(nil), out3...)
	ik = append([]byte(nil), out4...)
	ak = append([]byte(nil), out2[:6]...)
	return res, ck, ik, ak, nil
}

// F5Star computes AK for the re-sync branch (6 bytes). TS 35.206 §4.1.
func (m *Milenage) F5Star(k, rand []byte) ([]byte, error) {
	if len(k) != 16 || len(rand) != 16 {
		return nil, ErrInvalidArg
	}
	opc, err := m.getOPc(k, nil)
	if err != nil {
		return nil, err
	}
	cipher, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	tmp := xorNew(opc, rand)
	cipher.Encrypt(tmp, tmp)
	for i := range tmp {
		tmp[i] ^= opc[i]
	}
	out5 := aesXORRot(cipher, tmp, 0x60, c5, opc)
	return append([]byte(nil), out5[:6]...), nil
}

// Constants from TS 35.206 Table 5.1.
var (
	c1 = [16]byte{}
	c2 = [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	c3 = [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}
	c4 = [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4}
	c5 = [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 8}
)

// aesXORRot computes E_K(rot_r(tmp XOR nothing) XOR c) XOR OPc. Helper used
// by f2345 / f5*; "tmp XOR nothing" reflects the fact that f2345 does NOT
// XOR the per-function constant before rotation — see TS 35.206 §4.1 OUT2..5.
func aesXORRot(cipher interface{ Encrypt(dst, src []byte) }, tmp []byte, r uint, c [16]byte, opc []byte) []byte {
	rot := rotLeft16(append([]byte(nil), tmp...), r)
	for i := 0; i < 16; i++ {
		rot[i] ^= c[i]
	}
	out := make([]byte, 16)
	cipher.Encrypt(out, rot)
	for i := range out {
		out[i] ^= opc[i]
	}
	return out
}

// rotLeft16 rotates a 16-byte buffer left by r bits (r ∈ [0, 128)).
// TS 35.206 "rotate(X, r)" helper.
func rotLeft16(b []byte, r uint) []byte {
	ro, rb := int(r>>3), int(r%8)
	// byte-rotate first
	br := make([]byte, 16)
	copy(br, b[ro:])
	copy(br[16-ro:], b[:ro])
	if rb == 0 {
		return br
	}
	// Then bit-rotate within 128 bits (two uint64s).
	hi := binary.BigEndian.Uint64(br[:8])
	lo := binary.BigEndian.Uint64(br[8:])
	nh := (hi << uint(rb)) | (lo >> uint(64-rb))
	nl := (lo << uint(rb)) | (hi >> uint(64-rb))
	out := make([]byte, 16)
	binary.BigEndian.PutUint64(out[:8], nh)
	binary.BigEndian.PutUint64(out[8:], nl)
	return out
}

func xorInto(dst, a, b []byte) {
	for i := range dst {
		dst[i] = a[i] ^ b[i]
	}
}

func xorNew(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

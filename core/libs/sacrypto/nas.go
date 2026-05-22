// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 3GPP NAS ciphering + integrity algorithms for 5GC (TS 33.501 §D.2/D.3).
//
// Implemented:
//   - NEA0 / NIA0: null (TS 33.501 §D.1) — passthrough / zero MAC
//   - NEA2       : 128-EEA2 — AES-CTR (TS 33.401 Annex B.1)
//   - NIA2       : 128-EIA2 — AES-CMAC (TS 33.401 Annex B.2)
//
// NEA1/NIA1 (SNOW 3G) and NEA3/NIA3 (ZUC) are optional and need the 3GPP
// reference C implementations — tracked as Phase-2 work.
package sacrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
)

// NASDirection values (TS 33.501 §6.4.4).
const (
	NASDirUplink   = 0
	NASDirDownlink = 1
)

// NASBearer values (TS 33.501 §6.4.4) — NAS uses bearer 1 for 3GPP access.
const NASBearerDefault = 1

// NEA2 encrypts (or decrypts — AES-CTR is symmetric) a NAS payload.
// TS 33.401 Annex B.1: AES-CTR with nonce = COUNT(32) ‖ BEARER(5) ‖ DIR(1) ‖ 0^26.
func NEA2(key []byte, count uint32, bearer uint8, dir uint8, data []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("NEA2: key must be 16 bytes, got %d", len(key))
	}
	if bearer > 0x1F || dir > 1 {
		return nil, fmt.Errorf("NEA2: bearer/dir out of range")
	}
	nonce := make([]byte, 16)
	binary.BigEndian.PutUint32(nonce[:4], count)
	// bearer (5 bits) << 27 | dir (1 bit) << 26 = high 6 bits of the next byte.
	nonce[4] = (bearer&0x1F)<<3 | (dir&0x01)<<2
	// remaining bytes are zero.

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	stream := cipher.NewCTR(block, nonce)
	out := make([]byte, len(data))
	stream.XORKeyStream(out, data)
	return out, nil
}

// NIA2 computes the 32-bit NAS MAC (TS 33.401 Annex B.2).
// M = COUNT(32) ‖ BEARER(5) ‖ DIR(1) ‖ 0^26 ‖ message
// MAC = AES-CMAC(K_int, M)[0..3].
func NIA2(key []byte, count uint32, bearer uint8, dir uint8, data []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("NIA2: key must be 16 bytes, got %d", len(key))
	}
	if bearer > 0x1F || dir > 1 {
		return nil, fmt.Errorf("NIA2: bearer/dir out of range")
	}
	m := make([]byte, 0, 8+len(data))
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], count)
	hdr[4] = (bearer&0x1F)<<3 | (dir&0x01)<<2
	m = append(m, hdr[:]...)
	m = append(m, data...)
	return CMAC(key, m, 4)
}

// NEA0 is the null cipher — the data is returned unchanged.
// Used for emergency sessions and during initial registration before a
// NAS security context is established.
func NEA0(data []byte) []byte {
	return append([]byte(nil), data...)
}

// NIA0 is the null MAC — always 4 zero bytes (TS 33.501 §D.1).
// Never appears on the wire after SMC: the AMF mandates NIA2+ via SMC.
func NIA0() []byte {
	return []byte{0x00, 0x00, 0x00, 0x00}
}

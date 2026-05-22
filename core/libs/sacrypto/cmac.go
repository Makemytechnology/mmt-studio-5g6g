// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// AES-CMAC (NIST SP 800-38B / RFC 4493). Used by NIA2 for NAS integrity.
//
// Verified in cmac_test.go against the four canonical RFC 4493 test
// vectors (empty / 16-byte / 40-byte / 64-byte messages).
package sacrypto

import (
	"crypto/aes"
	"encoding/binary"
)

const aesBlockSize = 16

// CMAC computes AES-CMAC over msg with a fresh subkey schedule.
// Returns exactly tagLen bytes (clamped to 16). tagLen=4 gets the top
// 32 bits used by NIA2.
func CMAC(key, msg []byte, tagLen int) ([]byte, error) {
	if tagLen <= 0 || tagLen > aesBlockSize {
		tagLen = aesBlockSize
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	k1, k2 := cmacSubkeys(c)

	n := (len(msg) + aesBlockSize - 1) / aesBlockSize
	var mLast []byte
	if n == 0 {
		// Empty message: pad with 0x80 then zeros and XOR with K2.
		mLast = padBlock(nil)
		xorSlice(mLast, k2)
		n = 1
	} else if len(msg)%aesBlockSize == 0 {
		// Exact multiple of 16: last block XOR K1 directly.
		mLast = append([]byte(nil), msg[(n-1)*aesBlockSize:]...)
		xorSlice(mLast, k1)
	} else {
		// Tail partial block: pad then XOR K2.
		mLast = padBlock(msg[(n-1)*aesBlockSize:])
		xorSlice(mLast, k2)
	}

	// Iteratively AES-encrypt (prev XOR Mi).
	x := make([]byte, aesBlockSize)
	for i := 0; i < n-1; i++ {
		block := msg[i*aesBlockSize : (i+1)*aesBlockSize]
		for j := range x {
			x[j] ^= block[j]
		}
		c.Encrypt(x, x)
	}
	for j := range x {
		x[j] ^= mLast[j]
	}
	t := make([]byte, aesBlockSize)
	c.Encrypt(t, x)

	return t[:tagLen], nil
}

// cmacSubkeys derives K1 and K2 per RFC 4493 §2.3.
func cmacSubkeys(c interface{ Encrypt(dst, src []byte) }) (k1, k2 []byte) {
	zero := make([]byte, aesBlockSize)
	l := make([]byte, aesBlockSize)
	c.Encrypt(l, zero)
	k1 = dbl(l)
	k2 = dbl(k1)
	return
}

// dbl is the "times-two" operation in GF(2^128) with the AES polynomial
// x^128 + x^7 + x^2 + x + 1 (Rb = 0x87). TS 33.501 / SP 800-38B.
func dbl(x []byte) []byte {
	out := make([]byte, aesBlockSize)
	hi := binary.BigEndian.Uint64(x[:8])
	lo := binary.BigEndian.Uint64(x[8:])
	msb := hi >> 63
	hi = (hi << 1) | (lo >> 63)
	lo = lo << 1
	binary.BigEndian.PutUint64(out[:8], hi)
	binary.BigEndian.PutUint64(out[8:], lo)
	if msb == 1 {
		out[15] ^= 0x87
	}
	return out
}

// padBlock pads b up to 16 bytes with 0x80 0x00*n per RFC 4493 §2.4.
func padBlock(b []byte) []byte {
	out := make([]byte, aesBlockSize)
	copy(out, b)
	out[len(b)] = 0x80
	// remaining zeros already zero
	return out
}

func xorSlice(dst, src []byte) {
	for i := range dst {
		dst[i] ^= src[i]
	}
}

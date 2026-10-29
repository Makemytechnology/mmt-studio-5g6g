// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ikev2

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// DH performs the §2.10 Diffie-Hellman exchange over a MODP group
// from the IANA "Internet Key Exchange Version 2 (IKEv2) Parameters"
// registry.
//
// MODP-2048 (group 14) is the operator-mandated minimum here per
// RFC 7296 §3.3.2 (verbatim "2048-bit MODP Group 14 [ADDGROUP]"). The
// modulus is the verbatim p from RFC 3526 §3.
//
// "g^ir is represented as a string of octets in big endian order
// padded with zeros if necessary to make it the length of the
// modulus." — RFC 7296 §2.14
type DH struct {
	GroupID  uint16
	Prime    *big.Int
	Generator *big.Int
	primeLen  int // octets in the prime modulus, used for left-pad
}

// NewDH constructs a DH context for one of the supported groups.
// Returns an error for unsupported / unsafe groups (we deliberately
// limit to MODP-2048 / 3072 — RFC 7296 §3.3.2 lists 768/1024-bit as
// historic; modern operator policy bans them).
func NewDH(groupID uint16) (*DH, error) {
	switch groupID {
	case DH_MODP_2048:
		p := mustParseHexBig(modp2048HexP)
		return &DH{
			GroupID:   groupID,
			Prime:     p,
			Generator: big.NewInt(2),
			primeLen:  (p.BitLen() + 7) / 8,
		}, nil
	case DH_MODP_3072:
		p := mustParseHexBig(modp3072HexP)
		return &DH{
			GroupID:   groupID,
			Prime:     p,
			Generator: big.NewInt(2),
			primeLen:  (p.BitLen() + 7) / 8,
		}, nil
	}
	return nil, fmt.Errorf("ikev2 DH: unsupported group %d (RFC 7296 §3.3.2)", groupID)
}

// GenerateLocal returns (private, public) — the private exponent and
// the public value g^x mod p. Both are big-endian zero-padded to
// the prime length per RFC 7296 §2.14.
//
// Private exponent length: per RFC 3526 §8 "the size of the private
// exponent should be at least twice the security level of the group"
// — for MODP-2048 ⇒ 256 bits is well above the 112-bit security floor
// the group provides. We use the group bit length for simplicity
// (matches strongSwan / Linux kernel default).
func (d *DH) GenerateLocal() (private, public []byte, err error) {
	priv, err := rand.Int(rand.Reader, d.Prime)
	if err != nil {
		return nil, nil, err
	}
	if priv.Sign() == 0 {
		// extraordinarily unlikely; retry rather than ship a zero key.
		return d.GenerateLocal()
	}
	pub := new(big.Int).Exp(d.Generator, priv, d.Prime)
	return d.padBE(priv), d.padBE(pub), nil
}

// SharedSecret computes g^xy mod p given our private exponent and
// the peer's public value, then left-pads to the prime length.
//
// "g^ir is represented as a string of octets in big endian order
// padded with zeros if necessary to make it the length of the
// modulus." — RFC 7296 §2.14 (verbatim).
func (d *DH) SharedSecret(myPriv, peerPub []byte) ([]byte, error) {
	if len(myPriv) == 0 {
		return nil, errors.New("ikev2 DH: empty private exponent")
	}
	if len(peerPub) == 0 {
		return nil, errors.New("ikev2 DH: empty peer public value")
	}
	priv := new(big.Int).SetBytes(myPriv)
	pub := new(big.Int).SetBytes(peerPub)
	// Reject trivial subgroup attacks: 1, p-1, 0 are not legitimate
	// public values (per RFC 6989 — referenced from RFC 7296 §3.3.2
	// "other types of groups ... need to have some additional tests
	// performed on them"; for safety we apply the basic test even on
	// MODP groups).
	if pub.Sign() <= 0 || pub.Cmp(d.Prime) >= 0 {
		return nil, errors.New("ikev2 DH: peer public value out of range [1, p-1]")
	}
	one := big.NewInt(1)
	pminus1 := new(big.Int).Sub(d.Prime, one)
	if pub.Cmp(one) == 0 || pub.Cmp(pminus1) == 0 {
		return nil, errors.New("ikev2 DH: peer public value in trivial subgroup (RFC 6989)")
	}
	shared := new(big.Int).Exp(pub, priv, d.Prime)
	return d.padBE(shared), nil
}

// padBE returns x as a big-endian byte slice of exactly d.primeLen.
func (d *DH) padBE(x *big.Int) []byte {
	out := make([]byte, d.primeLen)
	xb := x.Bytes()
	copy(out[d.primeLen-len(xb):], xb)
	return out
}

func mustParseHexBig(h string) *big.Int {
	cleaned := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			return -1
		}
		return r
	}, h)
	n, ok := new(big.Int).SetString(cleaned, 16)
	if !ok {
		panic("ikev2 DH: malformed prime hex")
	}
	return n
}

// MODP-2048 prime (group 14) — RFC 3526 §3 (verbatim hex).
//
// "The hexadecimal value of the prime is:
//
//	  FFFFFFFF FFFFFFFF C90FDAA2 2168C234 C4C6628B 80DC1CD1
//	  29024E08 8A67CC74 020BBEA6 3B139B22 514A0879 8E3404DD
//	  EF9519B3 CD3A431B 302B0A6D F25F1437 4FE1356D 6D51C245
//	  E485B576 625E7EC6 F44C42E9 A637ED6B 0BFF5CB6 F406B7ED
//	  EE386BFB 5A899FA5 AE9F2411 7C4B1FE6 49286651 ECE45B3D
//	  C2007CB8 A163BF05 98DA4836 1C55D39A 69163FA8 FD24CF5F
//	  83655D23 DCA3AD96 1C62F356 208552BB 9ED52907 7096966D
//	  670C354E 4ABC9804 F1746C08 CA18217C 32905E46 2E36CE3B
//	  E39E772C 180E8603 9B2783A2 EC07A28F B5C55DF0 6F4C52C9
//	  DE2BCBF6 95581718 3995497C EA956AE5 15D22618 98FA0510
//	  15728E5A 8AACAA68 FFFFFFFF FFFFFFFF
//
//	  The generator is: 2." (verbatim)
const modp2048HexP = `
	FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1
	29024E088A67CC74020BBEA63B139B22514A08798E3404DD
	EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245
	E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED
	EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3D
	C2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F
	83655D23DCA3AD961C62F356208552BB9ED529077096966D
	670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B
	E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9
	DE2BCBF6955817183995497CEA956AE515D2261898FA0510
	15728E5A8AACAA68FFFFFFFFFFFFFFFF
`

// MODP-3072 prime (group 15) — RFC 3526 §4 (verbatim hex).
const modp3072HexP = `
	FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1
	29024E088A67CC74020BBEA63B139B22514A08798E3404DD
	EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245
	E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED
	EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3D
	C2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F
	83655D23DCA3AD961C62F356208552BB9ED529077096966D
	670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B
	E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9
	DE2BCBF6955817183995497CEA956AE515D2261898FA0510
	15728E5A8AAAC42DAD33170D04507A33A85521ABDF1CBA64
	ECFB850458DBEF0A8AEA71575D060C7DB3970F85A6E1E4C7
	ABF5AE8CDB0933D71E8C94E04A25619DCEE3D2261AD2EE6B
	F12FFA06D98A0864D87602733EC86A64521F2B18177B200C
	BBE117577A615D6C770988C0BAD946E208E24FA074E5AB31
	43DB5BFCE0FD108E4B82D120A93AD2CAFFFFFFFFFFFFFFFF
`

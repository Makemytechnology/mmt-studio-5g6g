// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ikev2

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
)

// PRF is one of the negotiated PRF algorithms from RFC 7296 §3.3.2
// (Transform Type 2). Only PRF_HMAC_SHA1 (id=2) and PRF_HMAC_SHA256
// (id=5, RFC 4868) are supported here — the operator policy bans
// the older HMAC-MD5 / TIGER values from §3.3.2.
type PRF struct {
	ID  uint16
	new func() hash.Hash // creates a fresh underlying hash for HMAC
	out int              // PRF output length in octets (key-size = same)
}

// NewPRF resolves a PRF transform ID to a usable PRF context.
func NewPRF(id uint16) (*PRF, error) {
	switch id {
	case PRF_HMAC_SHA1:
		return &PRF{ID: id, new: sha1.New, out: sha1.Size}, nil
	case PRF_HMAC_SHA256:
		return &PRF{ID: id, new: sha256.New, out: sha256.Size}, nil
	}
	return nil, fmt.Errorf("ikev2 PRF: unsupported id %d (RFC 7296 §3.3.2)", id)
}

// Out returns the PRF output length in octets — equal to the
// preferred key length (RFC 7296 §2.13: "For PRFs based on the HMAC
// construction, the preferred key size is equal to the length of
// the output of the underlying hash function.")
func (p *PRF) Out() int { return p.out }

// PRF computes prf(key, data) — a single HMAC over the negotiated
// hash. RFC 7296 §2.13: "the PRF is used iteratively. The term
// 'prf+' describes a function that outputs a pseudorandom stream
// based on the inputs to a pseudorandom function called 'prf'."
func (p *PRF) PRF(key, data []byte) []byte {
	mac := hmac.New(p.new, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// PRFPlus computes prf+(key, seed) and returns the first n octets.
//
// Verbatim from RFC 7296 §2.13:
//
//	prf+ (K,S) = T1 | T2 | T3 | T4 | ...
//	T1 = prf (K, S | 0x01)
//	T2 = prf (K, T1 | S | 0x02)
//	T3 = prf (K, T2 | S | 0x03)
//	T4 = prf (K, T3 | S | 0x04)
//
// "The prf+ function is not defined beyond 255 times the size of
// the prf function output." — §2.13. We enforce that bound.
func (p *PRF) PRFPlus(key, seed []byte, n int) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	if n > 255*p.out {
		return nil, fmt.Errorf("ikev2 PRF+: requested %d > 255 * %d output limit (RFC 7296 §2.13)",
			n, p.out)
	}
	out := make([]byte, 0, n)
	var T []byte
	for i := byte(1); ; i++ {
		mac := hmac.New(p.new, key)
		mac.Write(T)
		mac.Write(seed)
		mac.Write([]byte{i})
		T = mac.Sum(nil)
		out = append(out, T...)
		if len(out) >= n {
			break
		}
	}
	return out[:n], nil
}

// IKESAKeys holds the seven derived keys for an IKE SA, per RFC 7296
// §2.14.
//
//	{SK_d | SK_ai | SK_ar | SK_ei | SK_er | SK_pi | SK_pr}
//	  = prf+ (SKEYSEED, Ni | Nr | SPIi | SPIr)
//
// Lengths come from the negotiated transforms:
//
//	SK_d, SK_pi, SK_pr           ⟵ PRF.Out() (preferred PRF key size)
//	SK_ai, SK_ar                 ⟵ INTEG key size
//	SK_ei, SK_er                 ⟵ ENCR key size (variable for AES)
type IKESAKeys struct {
	SK_d, SK_ai, SK_ar, SK_ei, SK_er, SK_pi, SK_pr []byte
}

// DeriveIKESAKeys implements RFC 7296 §2.14 in full:
//
//	SKEYSEED = prf(Ni | Nr, g^ir)
//	{SK_d | SK_ai | SK_ar | SK_ei | SK_er | SK_pi | SK_pr}
//	  = prf+ (SKEYSEED, Ni | Nr | SPIi | SPIr)
//
// gIR is the shared secret from §2.10 — left-padded to the prime
// modulus length per §2.14. ni / nr are the IKE_SA_INIT nonces
// stripped of their generic payload header (§3.9). spiI / spiR are
// the 8-octet SPIs from the IKE header.
//
// integKeyLen is the §3.3.2 INTEG transform's key length in octets
// (e.g. 32 for AUTH_HMAC_SHA256_128). encrKeyLen is the ENCR key
// length in octets (e.g. 32 for ENCR_AES_CBC with KeyLength=256).
func (p *PRF) DeriveIKESAKeys(
	ni, nr, gIR []byte,
	spiI, spiR [8]byte,
	integKeyLen, encrKeyLen int,
) (*IKESAKeys, error) {
	if len(ni) == 0 || len(nr) == 0 || len(gIR) == 0 {
		return nil, errors.New("ikev2 keying: nonces and shared secret required")
	}
	// SKEYSEED = prf(Ni | Nr, g^ir)
	skKey := append(append([]byte(nil), ni...), nr...)
	skeyseed := p.PRF(skKey, gIR)

	// seed for prf+ = Ni | Nr | SPIi | SPIr
	seed := make([]byte, 0, len(ni)+len(nr)+16)
	seed = append(seed, ni...)
	seed = append(seed, nr...)
	seed = append(seed, spiI[:]...)
	seed = append(seed, spiR[:]...)

	total := p.out + 2*integKeyLen + 2*encrKeyLen + 2*p.out
	stream, err := p.PRFPlus(skeyseed, seed, total)
	if err != nil {
		return nil, err
	}
	off := 0
	take := func(n int) []byte {
		out := append([]byte(nil), stream[off:off+n]...)
		off += n
		return out
	}
	keys := &IKESAKeys{
		SK_d:  take(p.out),
		SK_ai: take(integKeyLen),
		SK_ar: take(integKeyLen),
		SK_ei: take(encrKeyLen),
		SK_er: take(encrKeyLen),
		SK_pi: take(p.out),
		SK_pr: take(p.out),
	}
	return keys, nil
}

// ChildSAKeys holds the four ESP/AH KEYMAT slices for one direction
// pair: encryption + integrity in each direction (initiator → other,
// responder → other). Empty integ keys when the cipher is AEAD
// (RFC 5282); empty encr keys when AH-only.
//
//	{SK_ei | SK_ai | SK_er | SK_ar} per RFC 7296 §2.17
//
// SK_e* feeds the ESP/AH cipher; SK_a* feeds the integrity protection.
// "i" suffixed keys are used for traffic the initiator sends; "r"
// suffixed keys for traffic the responder sends — i.e. each side
// encrypts with its OWN direction key and decrypts with the peer's.
type ChildSAKeys struct {
	SK_ei, SK_ai []byte // initiator → responder (initiator sends with these)
	SK_er, SK_ar []byte // responder → initiator (responder sends with these)
}

// DeriveChildSAKeys derives the §2.17 KEYMAT for a freshly-negotiated
// IPsec child SA. The §2.17 verbatim KDF:
//
//	KEYMAT = prf+(SK_d, Ni | Nr)
//
// or, when the CREATE_CHILD_SA exchange included a Diffie-Hellman
// (PFS keys per §1.3.1), the KDF is:
//
//	KEYMAT = prf+(SK_d, g^ir (new) | Ni | Nr)
//
// gIRNew is the new DH shared secret (nil when no PFS DH happened).
// ni / nr are the fresh CREATE_CHILD_SA nonces (NOT the IKE_SA_INIT
// nonces). The first encrKeyLen bytes are SK_ei, then integKeyLen
// SK_ai, then encrKeyLen SK_er, then integKeyLen SK_ar — per §2.17:
//
//	"All keys for SAs carrying data from the initiator to the
//	 responder are taken before SAs going from the responder to the
//	 initiator. ... For each algorithm, the keys are taken in the
//	 order in which they are needed."
//
// Caller must pass encrKeyLen=0 for AH-only proposals; integKeyLen=0
// for AEAD proposals (per RFC 5282).
func (p *PRF) DeriveChildSAKeys(
	skD, ni, nr, gIRNew []byte,
	encrKeyLen, integKeyLen int,
) (*ChildSAKeys, error) {
	if len(skD) == 0 {
		return nil, errors.New("ikev2 child SA: SK_d required")
	}
	if len(ni) == 0 || len(nr) == 0 {
		return nil, errors.New("ikev2 child SA: nonces required (CREATE_CHILD_SA Ni/Nr)")
	}
	if encrKeyLen == 0 && integKeyLen == 0 {
		return nil, errors.New("ikev2 child SA: at least one of encr/integ key lengths must be non-zero")
	}
	seed := make([]byte, 0, len(gIRNew)+len(ni)+len(nr))
	if len(gIRNew) > 0 {
		seed = append(seed, gIRNew...)
	}
	seed = append(seed, ni...)
	seed = append(seed, nr...)
	total := 2*encrKeyLen + 2*integKeyLen
	stream, err := p.PRFPlus(skD, seed, total)
	if err != nil {
		return nil, err
	}
	off := 0
	take := func(n int) []byte {
		if n == 0 {
			return nil
		}
		out := append([]byte(nil), stream[off:off+n]...)
		off += n
		return out
	}
	return &ChildSAKeys{
		SK_ei: take(encrKeyLen),
		SK_ai: take(integKeyLen),
		SK_er: take(encrKeyLen),
		SK_ar: take(integKeyLen),
	}, nil
}

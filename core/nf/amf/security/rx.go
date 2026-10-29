// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package security

import (
	"errors"

	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
)

// RxMeta carries metadata from a successfully-received 5GMM NAS PDU.
// Handlers receive this alongside the plain inner bytes so they can
// make count-dependent decisions (e.g. ICS K_gNB freshness) without
// ever reading ue.Security.* directly.
type RxMeta struct {
	// SHT is the Security header type from the wire (TS 24.501 §9.3).
	// 0 → plain; 1..4 → security protected.
	SHT uint8
	// ULCount is the 32-bit NAS COUNT that protected this PDU. Undefined
	// when Plain == true. Callers who derive K_gNB should NOT read this;
	// they call security.DeriveKgNB(ue) which reads it via ULNasCount-1
	// (invariant I3 / I4 in security/doc.go).
	ULCount uint32
	// Plain is true when SHT == 0 (no security wrapper present). Keys
	// were never touched; counts did not advance.
	Plain bool
	// Verified is true when we ran a MAC check and it passed. False
	// when SHT > 0 but the UE ctx had no K_NASInt loaded yet (the
	// "length-only shortcut" path — caller must run primary auth to
	// establish trust).
	Verified bool
}

// RxNAS takes a raw 5GMM NAS PDU just received from NGAP and returns:
//
//   - plain: the inner NAS bytes starting with EPD (0x7E) followed by
//     SHT octet + message type + body, suitable to hand straight into
//     nasgen.DecodeNASMessage. For SHT==0 the input is returned
//     unchanged; for SHT∈{1,2,3,4} it is the deciphered+integrity-
//     verified inner.
//   - meta: RxMeta as documented above.
//   - err:
//     ErrMACVerify  → MAC check failed (TS 24.501 §4.4.3.3); count
//     NOT advanced per §4.4.3.2 replay protection.
//     other errors → malformed PDU / unknown algorithm.
//
// TS 24.501 §4.4.3.1 paragraph 7-9 count reconstruction is delegated
// to unwrap(). Invariants I1, I2, I5 (security/doc.go) are enforced
// here — handlers receive plaintext + metadata and cannot re-verify
// or advance counts themselves.
//
// The "length-only shortcut" branch (secured PDU but no keys loaded
// yet) preserves the prior dispatch.stripSecurity behaviour: fresh
// initialUE allocates an empty ctx, a secured RR arrives from the UE,
// we can't verify, but we still need to peek at msgType for routing.
// Handler discovers the unverified state via meta.Verified == false
// and either runs primary auth (Registration) or rejects.
func RxNAS(ue *uectx.AmfUeCtx, pdu []byte) (plain []byte, meta RxMeta, err error) {
	if len(pdu) < 3 {
		return nil, RxMeta{}, errors.New("security: NAS PDU too short")
	}
	if pdu[0] != 0x7E {
		return nil, RxMeta{}, errors.New("security: not a 5GMM PDU (EPD != 0x7E)")
	}

	sht := pdu[1]

	// SHT==0: plain. Return as-is, no count or key operation (§9.3
	// Table 9.3.1 + §4.4.3.1: plain messages do not advance NAS COUNT).
	if sht == 0 {
		return pdu, RxMeta{SHT: 0, Plain: true}, nil
	}

	if len(pdu) < 8 {
		return nil, RxMeta{}, errors.New("security: secured 5GMM PDU truncated")
	}

	// SHT>0 with keys loaded: real verify+decipher+advance path.
	// Selection of the key slot per TS 24.501 v19.6.2 §4.4.2.1 +
	// §5.4.2.4 (SMC takes the non-current ctx into use):
	//   SHT=4 (SMC COMPLETE UL — wraps the new ctx) → Pending* keys.
	//   SHT∈{1,2} (post-SMC traffic under the current ctx) → operative
	//                 K_NASInt per §4.4.4.3 verification rules.
	hasOperative := ue.Security != nil && len(ue.Security.KNASInt) == 16
	hasPending := ue.Security != nil && ue.Security.Pending && len(ue.Security.PendingKNASInt) == 16
	if (sht == 4 && hasPending) || (sht != 4 && hasOperative) {
		decoded, used, verr := unwrap(ue, pdu, sht)
		if verr != nil {
			return nil, RxMeta{}, verr
		}
		if len(decoded) < 3 || decoded[0] != 0x7E {
			return nil, RxMeta{}, errors.New("security: unwrapped inner EPD != 0x7E")
		}
		return decoded, RxMeta{SHT: sht, ULCount: used, Verified: true}, nil
	}

	// SHT>0 without keys: length-only shortcut. The bytes starting at
	// offset 7 are treated as the inner. This is safe only when the
	// sender used NEA0 (no cipher) — which is the typical pre-SMC
	// state. Handler MUST treat this as an unverified message and is
	// responsible for running primary auth before accepting state
	// changes.
	inner := pdu[7:]
	if len(inner) < 3 {
		return nil, RxMeta{}, errors.New("security: secured 5GMM PDU inner too short")
	}
	if inner[0] != 0x7E {
		return nil, RxMeta{}, errors.New("security: secured 5GMM PDU inner EPD != 0x7E")
	}
	return inner, RxMeta{SHT: sht, Verified: false}, nil
}

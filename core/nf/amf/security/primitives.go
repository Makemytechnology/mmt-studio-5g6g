// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Internal NAS crypto primitives — moved here from nf/amf/gmm/nas_security.go
// so the package named in security/doc.go is the single owner. Unexported;
// callers use RxNAS / TxDL / TxSMC / TxPlain / Reuse / DecipherContainer.
//
// Wire format (TS 24.501 v19.6.2 §9.3 + §4.4):
//
//	+------+------+----------+-----+------------------+
//	| EPD  | SHT  | MAC (4B) | SQN |  inner NAS PDU   |
//	+------+------+----------+-----+------------------+
//	  0x7E  1..4    4 bytes    1B   starts at offset 7
//
// SHT table (TS 24.501 §9.3 Table 9.3.1):
//
//	0  plain
//	1  integrity protected
//	2  integrity protected and ciphered
//	3  integrity protected with new 5G NAS security context (SMC DL only)
//	4  integrity protected and ciphered with new 5G NAS security context
//	   (SMC Complete UL only)

package security

import (
	"errors"
	"fmt"

	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
)

// ErrMACVerify is returned when the received MAC doesn't match the one
// we computed locally for the reconstructed NAS COUNT + keys. Exported
// so callers can distinguish a MAC failure from transport / state errors.
var ErrMACVerify = errors.New("security: MAC verification failed")

// ErrNoIntegrityKey is returned when an operation needs K_NASInt but
// the UE security context doesn't have one (pre-SMC).
var ErrNoIntegrityKey = errors.New("security: NAS integrity key not set")

// ErrNoCipherKey is returned when an operation needs K_NASEnc but the
// UE security context doesn't have one (pre-SMC or NEA=0 path should
// have skipped it).
var ErrNoCipherKey = errors.New("security: NAS ciphering key not set")

// unwrap verifies the MAC on a security-protected uplink 5GMM PDU and
// returns (plain_inner_starting_with_EPD+SHT0+msgType+body,
// count_that_protected_this_pdu, err). On success ue.Security.ULNasCount
// has been advanced to count+1 per TS 24.501 §4.4.3.1 para 6.
//
// TS 24.501 §4.4.3.1 AMF-side storage semantics:
//
//	"The value of the uplink NAS COUNT stored in the AMF is the largest
//	 uplink NAS COUNT used in a successfully integrity checked NAS
//	 message."
//
// On MAC mismatch we return ErrMACVerify WITHOUT advancing the count.
// This is the contract that invariants I2 and I5 in security/doc.go
// depend on.
//
// Count reconstruction (§4.4.3.1 para 7-9): estimated count's low byte
// is the received SQN; the high 24 bits are kept from the last seen
// count, bumped by 0x100 when received SQN < stored SQN low byte. This
// is unchanged from the prior nas_security.go:173-178 behaviour so the
// refactor is byte-identical on the wire.
func unwrap(ue *uectx.AmfUeCtx, pdu []byte, sht byte) (plain []byte, usedCount uint32, err error) {
	// Pick the key set per TS 24.501 v19.6.2 §4.4.2.1 + §5.4.2.4:
	// SECURITY MODE COMPLETE (SHT=4) is the message that triggers the
	// new (non-current) NAS security context to be taken into use; it
	// is therefore protected by the pending* keys. Every other SHT>0
	// UL is protected by the operative (current) context per §4.4.4.2
	// "Integrity checking of NAS signalling messages in the UE" and
	// §4.4.4.3 "Integrity checking … in the AMF". With Pending=true
	// the new keys are populated; without it, SHT=4 is unexpected and
	// MUST fail rather than silently fall back to old keys.
	usePending := sht == 4 && ue.Security != nil && ue.Security.Pending
	var knasint, knasenc []byte
	var eia, eea uint8
	var ulNasCount uint32
	if usePending {
		knasint = ue.Security.PendingKNASInt
		knasenc = ue.Security.PendingKNASEnc
		eia = ue.Security.PendingEIA
		eea = ue.Security.PendingEEA
		ulNasCount = ue.Security.PendingULNasCount
	} else if ue.Security != nil {
		knasint = ue.Security.KNASInt
		knasenc = ue.Security.KNASEnc
		eia = ue.Security.EIA
		eea = ue.Security.EEA
		ulNasCount = ue.Security.ULNasCount
	}
	if len(knasint) != 16 {
		return nil, 0, ErrNoIntegrityKey
	}
	if len(pdu) < 7 {
		return nil, 0, errors.New("security: secured PDU truncated")
	}
	macRecv := pdu[2:6]
	sqn := pdu[6]
	payload := pdu[7:]

	upper := ulNasCount & 0xFFFFFF00
	prev := byte(ulNasCount & 0xFF)
	count := upper | uint32(sqn)
	if sqn < prev {
		count = (upper + 0x100) | uint32(sqn)
	}

	macInput := append([]byte{sqn}, payload...)
	macCalc, err := nasMAC(eia, knasint, count,
		sacrypto.NASBearerDefault, sacrypto.NASDirUplink, macInput)
	if err != nil {
		return nil, 0, err
	}
	if !constantTimeEqual(macCalc, macRecv) {
		return nil, 0, ErrMACVerify
	}

	// TS 24.501 §4.4.5 — cipher applies only when SHT indicates it AND
	// the negotiated NEA is not NEA0. NEA0 is a no-op; skip the call
	// so we don't need KNASEnc loaded.
	if (sht == 2 || sht == 4) && eea != 0 {
		if len(knasenc) != 16 {
			return nil, 0, ErrNoCipherKey
		}
		dec, err := nasEncrypt(eea, knasenc, count,
			sacrypto.NASBearerDefault, sacrypto.NASDirUplink, payload)
		if err != nil {
			return nil, 0, err
		}
		payload = dec
	}

	// Invariant I2 (security/doc.go): advance UL count ONLY on success,
	// on the same slot we read.
	if usePending {
		ue.Security.PendingULNasCount = count + 1
	} else {
		ue.Security.ULNasCount = count + 1
	}
	return payload, count, nil
}

// wrap emits a security-protected downlink 5GMM PDU per TS 24.501 §9.3.
// The SHT is chosen from (newCtx, ciphered):
//
//	newCtx=true,  ciphered=true  → SHT=4 (SMC Complete UL only — unused here)
//	newCtx=true,  ciphered=false → SHT=3 (SMC DL)
//	newCtx=false, ciphered=true  → SHT=2 (normal post-SMC)
//	newCtx=false, ciphered=false → SHT=1 (integrity only)
//
// Bumps ue.Security.DLNasCount by 1 on emission per §4.4.3.1 para 6
// (invariant I2 in security/doc.go: DL count advances ONLY for
// SECURITY PROTECTED messages).
func wrap(ue *uectx.AmfUeCtx, inner []byte, newCtx, ciphered bool) ([]byte, error) {
	// Pick keys/count slot per TS 24.501 v19.6.2 §5.4.2 (SMC
	// procedure) + §4.4.2.1 (NAS ctx maintenance):
	//   newCtx=true (SHT=3 SMC DL — sent under the new ctx that the
	//     SMC procedure is "taking into use") ⇒ wrap with the pending
	//     keys, advance pending DL count.
	//   newCtx=false (SHT=1, SHT=2 — post-SMC DL traffic per §5.4.2.4
	//     "From this time onward the AMF shall integrity protect and
	//     encipher all signalling messages …") ⇒ wrap with operative
	//     keys, advance operative DL count.
	usePending := newCtx && ue.Security != nil && ue.Security.Pending
	var knasint, knasenc []byte
	var eia, eea uint8
	var dlCount uint32
	if usePending {
		knasint = ue.Security.PendingKNASInt
		knasenc = ue.Security.PendingKNASEnc
		eia = ue.Security.PendingEIA
		eea = ue.Security.PendingEEA
		dlCount = ue.Security.PendingDLNasCount
	} else if ue.Security != nil {
		knasint = ue.Security.KNASInt
		knasenc = ue.Security.KNASEnc
		eia = ue.Security.EIA
		eea = ue.Security.EEA
		dlCount = ue.Security.DLNasCount
	}
	if len(knasint) != 16 {
		return nil, ErrNoIntegrityKey
	}

	count := dlCount
	sqn := byte(count & 0xFF)

	payload := append([]byte(nil), inner...)
	if ciphered && eea != 0 {
		if len(knasenc) != 16 {
			return nil, ErrNoCipherKey
		}
		enc, err := nasEncrypt(eea, knasenc, count,
			sacrypto.NASBearerDefault, sacrypto.NASDirDownlink, payload)
		if err != nil {
			return nil, fmt.Errorf("security: NEA%d: %w", eea, err)
		}
		payload = enc
	}

	macInput := append([]byte{sqn}, payload...)
	mac, err := nasMAC(eia, knasint, count,
		sacrypto.NASBearerDefault, sacrypto.NASDirDownlink, macInput)
	if err != nil {
		return nil, fmt.Errorf("security: NIA%d: %w", eia, err)
	}

	var sht byte
	switch {
	case ciphered && newCtx:
		sht = 4
	case ciphered:
		sht = 2
	case newCtx:
		sht = 3
	default:
		sht = 1
	}

	out := make([]byte, 0, 7+len(payload))
	out = append(out, 0x7E)
	out = append(out, sht)
	out = append(out, mac...)
	out = append(out, sqn)
	out = append(out, payload...)

	if usePending {
		ue.Security.PendingDLNasCount = count + 1
	} else {
		ue.Security.DLNasCount = count + 1
	}
	return out, nil
}

// nasEncrypt dispatches to the negotiated NEA algorithm. NEA0 is a
// no-op; NEA1 (SNOW3G) and NEA3 (ZUC) are not implemented.
func nasEncrypt(eea uint8, key []byte, count uint32, bearer, dir uint8, data []byte) ([]byte, error) {
	switch eea {
	case 0:
		return sacrypto.NEA0(data), nil
	case 1:
		return nil, fmt.Errorf("security: NEA1 (SNOW3G) not implemented")
	case 2:
		return sacrypto.NEA2(key, count, bearer, dir, data)
	case 3:
		return nil, fmt.Errorf("security: NEA3 (ZUC) not implemented")
	}
	return nil, fmt.Errorf("security: unknown ciphering algorithm NEA%d", eea)
}

// nasMAC dispatches to the negotiated NIA algorithm. NIA0 returns a
// zero MAC per TS 33.501 §D.3.1.1 (used only in emergency / NULL
// integrity scenarios); NIA1/NIA3 are not implemented.
func nasMAC(eia uint8, key []byte, count uint32, bearer, dir uint8, data []byte) ([]byte, error) {
	switch eia {
	case 0:
		return sacrypto.NIA0(), nil
	case 1:
		return nil, fmt.Errorf("security: NIA1 (SNOW3G) not implemented")
	case 2:
		return sacrypto.NIA2(key, count, bearer, dir, data)
	case 3:
		return nil, fmt.Errorf("security: NIA3 (ZUC) not implemented")
	}
	return nil, fmt.Errorf("security: unknown integrity algorithm NIA%d", eia)
}

// constantTimeEqual compares two MAC byte slices in constant time.
func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

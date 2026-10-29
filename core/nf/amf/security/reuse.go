// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package security

import (
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
)

// Reuse implements the TS 24.501 §4.4 cached-context skip-auth MAC
// verification for the cross-ctx case (case i in
// nf/amf/gmm/registration.go: `existing != ue`).
//
// On an InitialUEMessage the AMF may find:
//
//	case ii: the prior *uectx.AmfUeCtx is resolved directly by
//	         5G-S-TMSI (initialue.go:130-157). RxNAS at dispatch-time
//	         already ran the MAC verify against that ctx's keys — no
//	         second verify is needed (this is what commit 8d7ffa4
//	         established, and security/doc.go invariant I1 enforces).
//
//	case i:  the UE ctx handed into RxNAS is freshly allocated with no
//	         keys (initialue.go:159-163 path — e.g. first attach with
//	         5G-GUTI resolvable only via SUPI lookup). RxNAS returned
//	         Verified=false via the length-only shortcut. The §4.4
//	         reuse path then discovers an `existing` ctx by SUPI and
//	         wants to verify the RR's MAC against that ctx's keys
//	         before promoting it onto the fresh `ue`.
//
// This function handles case i. It MUST NOT be called from case ii —
// that would be the double-verify bug commit 8d7ffa4 patched.
//
// TS 24.501 §4.4.4.3 "Integrity checking of NAS signalling messages
// in the AMF" governs the verify itself. On success the existing
// ctx's ULNasCount has advanced by 1 (to next-expected, per §4.4.3.1
// para 6). Caller migrates the full security state onto the target
// ctx; the ULNasCount carries over so K_gNB derivation gets the
// RR's count as freshness (§6.8.1.2.2 / §A.9).
//
// TS 24.501 §9.3 Table 9.3.1: pdu[1] is the Security header type.
// Reuse only verifies when SHT ∈ {1,2,3,4}. A §4.4 RR that arrived
// with SHT=0 is treated per §4.4.4.3 as unprotected and this
// function returns nil without touching counts — caller's business
// to decide whether to accept plain (it usually doesn't; falls
// through to primary auth per §4.4.4.3 "discard" abnormal case).
func Reuse(existing *uectx.AmfUeCtx, pdu []byte) error {
	if len(pdu) < 2 || pdu[0] != 0x7E {
		return ErrMACVerify
	}
	sht := pdu[1]
	if sht == 0 {
		return nil
	}
	if _, _, err := unwrap(existing, pdu, sht); err != nil {
		return err
	}
	return nil
}

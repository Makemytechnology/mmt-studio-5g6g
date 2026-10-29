// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package security

import (
	"errors"

	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
)

// ErrKAMFMissing indicates the 5G NAS security context has no K_AMF
// loaded, so any key derivation that depends on it (K_gNB, K_NASint,
// K_NASenc) is impossible. Typically means primary authentication
// has not completed.
var ErrKAMFMissing = errors.New("security: K_AMF not established")

// DeriveKgNB returns a freshly computed K_gNB (256 bits / 32 bytes)
// for the UE, using the UL NAS COUNT of the most-recently successfully
// integrity-checked uplink NAS message as freshness parameter.
//
// TS 33.501 v19.6.0 §A.9 (specs/3gpp/ts_133501v190600p.pdf) defines
// the KDF inputs:
//
//	FC = 0x6E
//	P0 = Uplink NAS COUNT    L0 = 0x00 0x04
//	P1 = Access distinguisher L1 = 0x00 0x01
//	KEY = 256-bit K_AMF
//
// Table A.9-1 sets the 3GPP-access distinguisher to 0x01. Non-3GPP
// (N3IWF / TNGF / TWIF / WAGF) would switch to 0x02; out of scope.
//
// TS 33.501 §6.8.1.2.2 "Establishment of keys for cryptographically
// protected radio bearers in 3GPP access" dictates the freshness:
//
//	"Upon receipt of the NAS message, if the AMF does not require a
//	 NAS SMC procedure before initiating the NGAP procedure INITIAL
//	 CONTEXT SETUP, the AMF shall derive key KgNB/KeNB as specified
//	 in Annex A using the uplink NAS COUNT (see TS 24.501) corresponding
//	 to the NAS message that initiated transition from CM-IDLE to
//	 CM-CONNECTED state and the KAMF of the current 5G NAS security
//	 context."
//
// The same clause handles the with-SMC case: if the CM-IDLE→CM-CONNECTED
// transition ran a fresh SMC, freshness is the SMC Complete's UL count.
// Either way, "the most recent successfully integrity checked UL NAS
// message" is the right answer, which is what ULNasCount-1 gives us:
// secureUnwrap (nas_security.go:203) advances ULNasCount to count+1 on
// success, so the last verified count is ULNasCount-1.
//
// No caching. Callers invoke this at the moment of encoding the
// InitialContextSetupRequest's SecurityKey IE; there is no stashed
// value to go stale. This is the invariant (I4) from security/doc.go.
func DeriveKgNB(ue *uectx.AmfUeCtx) ([]byte, error) {
	if ue == nil || ue.Security == nil {
		return nil, errors.New("security: UE context missing")
	}
	if len(ue.Security.KAMF) != 32 {
		return nil, ErrKAMFMissing
	}
	ulCount := (ue.Security.ULNasCount - 1) & 0xFFFFFFFF
	return sacrypto.ConvA9(ue.Security.KAMF, ulCount, 1)
}

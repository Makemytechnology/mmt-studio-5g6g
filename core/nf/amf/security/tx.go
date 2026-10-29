// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package security

import "github.com/mmt/mmt-studio-core/nf/amf/uectx"

// TxPlain returns the inner bytes unchanged. Use for DL messages that
// MUST be sent plain (SHT=0) — per TS 24.501 §4.4.4.2 these include
// AUTHENTICATION REQUEST, AUTHENTICATION RESULT, AUTHENTICATION
// REJECT, IDENTITY REQUEST, SERVICE REJECT when no security context
// exists, REGISTRATION REJECT when no security context exists,
// DEREGISTRATION ACCEPT for deregistration type "switch off", and
// the SECURITY MODE COMMAND itself (which uses SHT=3 via TxSMC).
//
// No MAC, no cipher, no count advance — the caller hands us plain
// bytes and gets them back.
func TxPlain(_ *uectx.AmfUeCtx, inner []byte) []byte {
	return inner
}

// TxDL wraps a plain outbound 5GMM NAS message with the UE's current
// security context when one is active, or returns the input unchanged
// when not. This is the hook that dlnas and pdusetup install at
// init-time so every DL NAS PDU gets the correct SHT wrapper.
//
// Always emits SHT=2 ("integrity protected and ciphered") — verbatim
// TS 24.501 v19.6.2 §4.4.5 (specs/.../ts_124501v190602p.pdf):
//
//	"For setting the security header type in outbound NAS messages,
//	 the UE and the AMF shall apply the same rules irrespective of
//	 whether the 'null ciphering algorithm' or any other ciphering
//	 algorithm is indicated in the 5G NAS security context."
//
//	"If the 'null ciphering algorithm' 5G-EA0 has been selected as a
//	 ciphering algorithm, the NAS messages with the security header
//	 indicating ciphering are regarded as ciphered."
//
// So SHT=2 is the rule regardless of the negotiated NEA. With NEA0
// the cipher operation inside wrap() is a no-op (sacrypto.NEA0 is the
// identity function) so the bytes on the wire are byte-identical to
// SHT=1 except for the SHT field — but the protocol-correct field
// value is 2, not 1. SHT=1 (integrity-only) is reserved for the few
// DL messages that genuinely must NOT be ciphered (e.g. SECURITY
// MODE COMMAND uses SHT=3 via TxSMC; all other post-SMC DL NAS goes
// through here and gets SHT=2).
//
// If the input already carries a non-zero SHT (e.g. caller went
// through TxSMC and passed the wrapped bytes through here), we leave
// it untouched — never double-wrap.
//
// Invariant I2 (security/doc.go): DL count advances exactly once per
// security-protected message. Plain pass-through does not advance.
func TxDL(ue *uectx.AmfUeCtx, plain []byte) ([]byte, error) {
	if ue == nil || ue.Security == nil || !ue.Security.Activated {
		return plain, nil
	}
	if len(ue.Security.KNASInt) != 16 {
		return plain, nil
	}
	// Already wrapped — pass-through guard.
	if len(plain) >= 2 && plain[0] == 0x7E && plain[1] != 0x00 {
		return plain, nil
	}
	return wrap(ue, plain, false /*newCtx*/, true /*ciphered → SHT=2 per §4.4.5*/)
}

// TxSMC emits the SECURITY MODE COMMAND (TS 24.501 §5.4.2.2) with
// Security header type = 3 "Integrity protected with new 5G NAS
// security context" per §9.3 Table 9.3.1 NOTE 1.
//
// TS 33.501 v19.6.0 §6.7.2 step 1b (specs/3gpp/ts_133501v190600p.pdf):
//
//	"This message shall be integrity protected (but not ciphered) with
//	 NAS integrity key based on the KAMF indicated by the ngKSI in the
//	 NAS Security Mode Command message."
//
// Caller must have loaded the NEW K_NASInt + NGKSI into ue.Security
// before calling (ActivateCtx in a future phase will own this; today
// smc.go sets them inline). DL count is NOT reset here — the outer
// procedure owns that; we pick up whatever DLNasCount is and wrap.
// Advances DLNasCount by 1.
func TxSMC(ue *uectx.AmfUeCtx, inner []byte) ([]byte, error) {
	return wrap(ue, inner, true /*newCtx*/, false /*ciphered*/)
}

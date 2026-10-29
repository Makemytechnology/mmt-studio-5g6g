// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package security

import (
	"errors"

	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
)

// ActivateCtx prepares a newly-established 5G NAS security context as
// the non-current (pending) context per TS 24.501 v19.6.2 §4.4.2.1.
// Called after primary authentication succeeds and the AMF has decided
// which NEA / NIA algorithms to use. Derives K_NASEnc and K_NASInt per
// TS 33.501 v19.6.0 §A.8 (KDF FC=0x69, AlgType=N-NAS-enc-alg /
// N-NAS-int-alg), resets both pending NAS COUNTs to zero per
// TS 24.501 §4.4.3.1, and stamps the chosen algorithm IDs + ngKSI
// into the Pending* fields. The operative (current) ue.Security.KNAS*
// / EEA / EIA / ULNasCount / DLNasCount fields are NOT touched —
// they remain in use to protect signalling until PromoteContext()
// runs on receipt of SECURITY MODE COMPLETE per §5.4.2.4 verbatim:
//
//	"The AMF shall, upon receipt of the SECURITY MODE COMPLETE
//	 message, stop timer T3560. From this time onward the AMF shall
//	 integrity protect and encipher all signalling messages with the
//	 selected 5GS integrity and ciphering algorithms."
//
// The UE's K_AMF must already be loaded (set earlier in the primary
// auth flow); we read it from ue.Security.KAMF.
//
// After this, the caller should TxSMC(ue, innerSMC) to ship the
// SECURITY MODE COMMAND under the new context (SHT=3 per §9.3
// Table 9.3.1 NOTE 1 + §6.7.2 step 1b "integrity protected but not
// ciphered with NAS integrity key based on the KAMF indicated by the
// ngKSI"). The context's Activated flag stays false until SMC
// Complete lands (caller flips it there).
//
// TS 24.501 §4.4.3.1 (UL/DL NAS COUNT reset semantics):
//
//	"If the NAS procedure establishing radio bearers contains a
//	 primary authentication run … the NAS uplink and downlink COUNT
//	 for the new KAMF shall be set to the start values (i.e. zero)."
//	(via TS 33.501 §6.8.1.2.2)
//
// TS 33.501 §A.8 key lengths:
//
//	"The returned key K is 32 octets. The 16 lowest significant
//	 bytes of K are used as the 128-bit NAS ciphering / integrity key."
func ActivateCtx(ue *uectx.AmfUeCtx, ngksi, eea, eia uint8) error {
	if ue == nil || ue.Security == nil {
		return errors.New("security: UE context missing")
	}
	if len(ue.Security.KAMF) != 32 {
		return ErrKAMFMissing
	}

	knasenc, err := sacrypto.ConvA8(ue.Security.KAMF, sacrypto.AlgTypeNASEnc, eea)
	if err != nil {
		return err
	}
	knasint, err := sacrypto.ConvA8(ue.Security.KAMF, sacrypto.AlgTypeNASInt, eia)
	if err != nil {
		return err
	}

	// TS 33.501 §A.8 — 128-bit NAS keys are the low 16 bytes of the
	// 32-byte KDF output. Written to the pending slot.
	ue.Security.PendingKNASEnc = knasenc[16:]
	ue.Security.PendingKNASInt = knasint[16:]
	ue.Security.PendingEEA = eea
	ue.Security.PendingEIA = eia
	ue.Security.PendingNGKSI = ngksi

	// TS 24.501 §4.4.3.1 + TS 33.501 §6.7.2: counts reset on new ctx.
	ue.Security.PendingULNasCount = 0
	ue.Security.PendingDLNasCount = 0

	// Mark the pending slot as live. promoteContext() (called from the
	// SECURITY MODE COMPLETE handler on a verified SMC Complete) is
	// the only path that copies these into the operative fields.
	ue.Security.Pending = true

	return nil
}

// PromoteContext copies the Pending* fields into the operative slots
// and clears the pending slot. Called from handleSecurityModeComplete
// AFTER MAC verification of the SECURITY MODE COMPLETE has passed.
// Per TS 24.501 v19.6.2 §5.4.2.4 — "From this time onward the AMF
// shall integrity protect and encipher all signalling messages with
// the selected 5GS integrity and ciphering algorithms" — this is the
// precise moment the new context replaces the prior one as the
// "current 5G NAS security context" (§4.4.2.1) on the AMF side.
//
// Idempotent w.r.t. the operative-flag side-effects: the caller is
// expected to set Activated=true on the operative ctx after this
// returns.
func PromoteContext(ue *uectx.AmfUeCtx) {
	if ue == nil || ue.Security == nil || !ue.Security.Pending {
		return
	}
	s := ue.Security
	s.KNASEnc = s.PendingKNASEnc
	s.KNASInt = s.PendingKNASInt
	s.EEA = s.PendingEEA
	s.EIA = s.PendingEIA
	s.NGKSI = s.PendingNGKSI
	s.NGKSIAssigned = true
	s.ULNasCount = s.PendingULNasCount
	s.DLNasCount = s.PendingDLNasCount
	clearPending(s)
}

// DiscardPending tears down a non-current (pending) context without
// promoting it. Called from SMC reject / lower-layer-failure /
// procedure abort paths. Per TS 24.501 v19.6.2 §5.4.2.5 verbatim:
//
//	"Both the UE and the AMF shall apply the 5G NAS security context
//	 in use before the initiation of the security mode control
//	 procedure, if any, to protect the SECURITY MODE REJECT message
//	 and any other subsequent messages …"
//
// The pending context is forgotten and the operative (pre-SMC) ctx
// stays in use to protect subsequent signalling.
func DiscardPending(ue *uectx.AmfUeCtx) {
	if ue == nil || ue.Security == nil {
		return
	}
	clearPending(ue.Security)
}

func clearPending(s *uectx.SecurityCtx) {
	s.Pending = false
	s.PendingKNASEnc = nil
	s.PendingKNASInt = nil
	s.PendingEEA = 0
	s.PendingEIA = 0
	s.PendingNGKSI = 0
	s.PendingULNasCount = 0
	s.PendingDLNasCount = 0
}

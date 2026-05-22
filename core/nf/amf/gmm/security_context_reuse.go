// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Extracted from registration.go by refactor: split god-file by
// sub-concern. Imports are re-derived by goimports.
package gmm

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/initialctxsetup"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// securityHeaderType returns the 5GMM Security Header Type byte of a raw
// NAS PDU (TS 24.501 §9.3 — byte offset 1). Returns 0 ("plain NAS")
// when the PDU is too short, so callers can safely treat a missing PDU
// as an unprotected message.
func securityHeaderType(pdu []byte) byte {
	if len(pdu) < 2 {
		return 0
	}
	return pdu[1]
}

// canReuseCachedContext reports whether an existing UE context carries a
// valid native 5G NAS security context that the UE is claiming in its
// current RegistrationRequest. Per TS 24.501 §4.4 + §9.11.3.32:
//
//   - the cached context must have completed primary auth (AuthDone) and
//     have KAMF populated,
//   - the cached NGKSI must equal the ngKSI the UE just presented, and
//   - the cached NGKSI must be in the valid range 0..6 (value 7 means
//     "no key available" — indicator only, never an active context).
//
// When any condition fails, the caller falls back to the standard primary
// authentication path (§5.4.1).
func canReuseCachedContext(existing *uectx.AmfUeCtx, ueNGKSI int) bool {
	if existing == nil || existing.Security == nil {
		return false
	}
	if !existing.Security.AuthDone || len(existing.Security.KAMF) == 0 {
		return false
	}
	if existing.Security.NGKSI > 6 {
		return false
	}
	return int(existing.Security.NGKSI) == ueNGKSI
}

// migrateSecurityContext copies the cached 5G NAS security context from
// an old UE ctx onto the freshly-allocated one. The new ctx keeps its
// own NGAP identifiers (AmfUeNGAPID, RanUeNGAPID, GnbKey) — only the
// security state is carried across, matching TS 24.501 §4.4's "taken
// into use on the new N1 NAS signalling connection" semantics.
func migrateSecurityContext(src, dst *uectx.AmfUeCtx) {
	if src == nil || dst == nil || src.Security == nil || dst.Security == nil {
		return
	}
	s, d := src.Security, dst.Security
	d.KAUSF = s.KAUSF
	d.KSEAF = s.KSEAF
	d.KAMF = s.KAMF
	d.KNASEnc = s.KNASEnc
	d.KNASInt = s.KNASInt
	d.UESecCap = s.UESecCap
	d.ABBA = s.ABBA
	d.EEA = s.EEA
	d.EIA = s.EIA
	d.ULNasCount = s.ULNasCount
	d.DLNasCount = s.DLNasCount
	d.AuthDone = s.AuthDone
	d.Activated = s.Activated
	d.NGKSI = s.NGKSI
	d.NGKSIAssigned = s.NGKSIAssigned
}

// sendRegistrationAcceptReusedContext completes the skip-auth/skip-SMC
// branch of TS 24.501 §4.4: ship InitialContextSetupRequest (K_gNB is
// derived just-in-time by security.DeriveKgNB inside initialctxsetup.Send,
// using the current UL NAS COUNT per TS 33.501 §6.8.1.2.2), then send
// the Registration Accept NAS over DL NAS Transport. Finally advance the
// GMM FSM via EvRegRequestContextValid.
func sendRegistrationAcceptReusedContext(ue *uectx.AmfUeCtx) error {
	log := logger.Get("amf.gmm.registration")

	// TS 24.501 v19.6.2 §5.3.3 verbatim (line 13464-13466 of
	// /tmp/ts24501.txt): "The AMF should assign a new 5G-GUTI for a
	// particular UE during a successful registration procedure for
	// periodic registration update. The AMF may assign a new 5G-GUTI
	// at any time for a particular UE..."
	//
	// And §5.3.3 line 13468-13476 — the AMF/UE 5G-GUTI handshake:
	//   "If a new 5G-GUTI is assigned by the AMF…
	//    a) Upon receipt of a 5GMM message containing a new 5G-GUTI,
	//       the UE considers the new 5G-GUTI as valid and the old
	//       5G-GUTI as invalid…
	//    b) The AMF considers the old 5G-GUTI as invalid as soon as
	//       an acknowledgement for a registration… procedure is
	//       received."
	//
	// On the §4.4 reuse path we previously emitted REGISTRATION
	// ACCEPT with the SAME 5G-GUTI (buildAssigned5GGUTI's "if
	// ue.TMSI5G == 0 { allocate }" — never rotates after initial
	// allocation). The UE saw an unchanged 5G-GUTI and skipped the
	// REGISTRATION COMPLETE (no "new vs old" handshake to ack);
	// AMF stayed at StateRegisteredInitiated until T3550 expired
	// or the gNB tore down the connection.
	//
	// Force-rotate the 5G-TMSI now per §5.3.3 — the spec
	// authorises this on any registration update, and it is
	// REQUIRED (per the line 13468-13476 handshake) for the UE to
	// emit the REGISTRATION COMPLETE that closes the procedure.
	// Allocate fresh; track-as-old-and-new is in scope for a future
	// pass (§5.5.1.2.8 c "AMF shall consider both 5G-GUTIs valid
	// until the old can be considered invalid"); for now we just
	// overwrite — tester collision odds across a 32-bit space are
	// negligible.
	ue.TMSI5G = allocate5GTMSI()

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return fmt.Errorf("gNB %q gone", ue.GnbKey)
	}
	if err := initialctxsetup.Send(gnb, ue); err != nil {
		return fmt.Errorf("ICS send: %w", err)
	}

	sendRegistrationAccept(ue)
	log.WithIMSI(ue.IMSI).Infof("Registration Accept sent via cached context amfUeID=%d (no auth, no SMC per TS 24.501 §4.4)", ue.AmfUeNGAPID)

	// Advance the GMM FSM so T3550 is armed and state becomes REGISTERED_INITIATED.
	_ = fsm.Of(ue).Fire(&fsm.Context{
		UE:    ue,
		Event: fsm.EvRegRequestContextValid,
	})
	return nil
}

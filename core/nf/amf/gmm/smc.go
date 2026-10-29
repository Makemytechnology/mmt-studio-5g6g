// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5G Security Mode Control (TS 24.501 §5.4.2).
//
// Port of nf/amf/gmm/gmm_security_mode.py. The AMF sends Security Mode
// Command integrity-protected-only (SecHdr=3) to activate ciphering +
// integrity on the NAS link, replaying the UE's reported security
// capabilities so the UE can detect tampering by an intermediate attacker.
//
// ── Spec-compliance gaps (§5.4.2) ───────────────────────────────────
//
// TODO(spec: TS 24.501 §5.4.2.2) — SMC IE completeness:
//   - IMEISVRequest — we hardcode the request; spec allows operator
//     policy to decide.
//   - Additional 5G security information IE (RINMR bit) shall be set
//     when the AMF wants the UE to retransmit the initial NAS message
//     (§5.4.2.2 "Retransmission of the initial NAS message requested").
//     We hardcode 0x02 today; the bit should be policy-driven.
//   - Selected EPS NAS security algorithms IE shall be set when the
//     UE supports N1 mode in EPS and N26 interworking is configured
//     (§5.4.2.2).
//   - Replayed S1 UE security capabilities — same N26-interworking
//     condition; mirrors UESecurityCapability for EUTRA bits.
//   - ABBA (Anti-Bidding down Between Architectures) — we echo from
//     Security ctx; spec permits upgrading ABBA during SMC.
//
// TODO(spec: TS 24.501 §5.4.2.4 + §4.4.6) — SMC Complete processing:
//
//	When the UE's initial RR was cleartext-only (§4.4.6 case a), the
//	UE includes a NAS Message Container IE in the SECURITY MODE
//	COMPLETE carrying the FULL inner RR. The AMF shall extract the
//	inner and re-process the Registration using its non-cleartext
//	IEs. Our handleSecurityModeComplete does not do the container
//	unwrap — the non-cleartext IEs that came through case (a) are
//	lost.
//
// TODO(spec: TS 24.501 §5.4.2.7 a) — Lower layer failure before SMC
//
//	Complete/Reject received: AMF shall abort the SMC procedure.
//	We rely on the gNB-disconnect cascade; explicit abort-on-lower-
//	layer-failure is not wired here.
//
// TODO(spec: TS 24.501 §5.4.2.7 b) — T3560 expiry final:
//
//	5th expiry shall abort the SMC procedure. FSM TimerSpec handles
//	the retransmit count; the "abort + release N1 NAS" final step is
//	not distinguished from a mid-procedure timeout.
//
// TODO(spec: TS 24.501 §5.4.2.7 c) — Collision with registration,
//
//	service request, or de-registration (not switch-off): AMF shall
//	abort SMC and proceed with the UE-initiated procedure. Today the
//	collision guard in dispatch.go:checkCollision drops the new
//	request instead.
//
// TODO(spec: TS 24.501 §5.4.2.7 e) — Non-delivered NAS PDU due to
//
//	intra-AMF handover: AMF shall retransmit SMC on handover complete.
//	Not wired — handover path doesn't re-trigger pending NAS PDUs.
//
// ── Spec-compliance gaps (TS 33.501 — security) ──────────────────────
//
// TODO(spec: TS 33.501 §6.7.2 step 1b) — K_AMF_change_flag:
//
//	"In the case of horizontal derivation of KAMF during mobility
//	 registration update or during multiple registration in same
//	 PLMN, K_AMF_change_flag shall be included in the NAS Security
//	 Mode Command message as described in clause 6.9.3."
//	We don't yet rekey KAMF horizontally (covered under TS 24.501
//	mobility/periodic RR TODOs), so the flag is always off. When
//	mobility auth is implemented, set the flag.
//
// TODO(spec: TS 33.501 §6.7.2 step 1b + §8.5.2) — EPS NAS algos on SMC:
//
//	"In case the network supports interworking using the N26 interface
//	 between MME and AMF, the AMF shall also include the selected EPS
//	 NAS algorithms ... in the NAS Security Mode Command message."
//	We never include them because N26 interworking isn't wired. Depends
//	on TS 23.501 §5.17.2 + TS 23.502 §4.11.x interworking procedures.
//
// TODO(spec: TS 33.501 §6.7.2 step 1d / §6.4.5) — NAS COUNT wrap-around
//
//	ciphering: the AMF activates NAS downlink ciphering only after
//	receiving SMC Complete. Our secureWrap activates on first post-SMC
//	send. Spec also requires: "If the uplink NAS COUNT will wrap around
//	by sending the NAS Security Mode Reject message, the UE releases
//	the NAS connection" (§6.7.2 NOTE 3). AMF symmetric behaviour on DL
//	COUNT wrap is not wired.
//
// TODO(spec: TS 33.501 §6.8.1.1.2.3) — "The NAS SMC complete message
//
//	shall include the start value of the uplink NAS COUNT that is used
//	as freshness parameter in the KgNB/KeNB derivation." This is UE
//	side (SMC Complete carries a count value), but the AMF shall verify
//	the included value matches what was used for KgNB. Today we derive
//	KgNB from ue.Security.ULNasCount and trust the UE's count — we do
//	not cross-check an explicit "start value" IE in SMC Complete
//	because the codec does not surface one.
//
// TODO(spec: TS 33.501 §5.2.3 + §10.2.2) — NIA0 restriction:
//
//	§5.2.3: "The UE shall implement NIA0 for integrity protection of
//	 NAS and RRC signalling. NIA0 is only allowed for unauthenticated
//	 emergency session as specified in clause 10.2.2."
//	§10.2.2 is the Unauthenticated IMS Emergency Sessions clause.
//	negotiateAlgorithms rejects NIA0 unconditionally (returns an error
//	when eia == 0), so the current restriction is stricter than
//	required. Emergency registration is not supported yet; when added,
//	relax NIA0 selection inside that path only and keep NIA0
//	forbidden elsewhere.
package gmm

import (
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	amfctx "github.com/mmt/mmt-studio-core/nf/amf/ctx"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/initialctxsetup"
	"github.com/mmt/mmt-studio-core/nf/amf/security"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	nas "github.com/mmt/nasgen/generated"
)

func init() {
	Register(MsgSecurityModeComplete, handleSecurityModeComplete)
	Register(MsgSecurityModeReject, handleSecurityModeReject)
}

// startSecurityMode is entered from auth.go after RES* validation succeeds.
// Derives the NAS keys and sends Security Mode Command.
func startSecurityMode(ue *uectx.AmfUeCtx) {
	log := logger.Get("amf.gmm.smc")

	// TS 33.501 §6.7.2 — negotiate NAS security algorithms based on
	// network priority (security_algorithms table) and UE capabilities.
	//
	// negotiateAlgorithms errors when the UE either:
	//   (a) didn't include a UE Security Capability IE in the
	//       Registration Request (the IE is Mandatory per TS 24.501
	//       §8.2.6 Table 8.2.6.1.1), or
	//   (b) shares no integrity algorithm with the AMF's priority
	//       list (NIA0 is forbidden for NAS per TS 33.501 §5.3.2,
	//       so eia=0 means no usable choice exists).
	//
	// Both are protocol errors — the AMF cannot establish NAS
	// security and must abort the registration. Per TS 24.501
	// §5.5.1.2.8(b) the cause is #96 "invalid mandatory information"
	// when a Mandatory IE was absent/malformed; for the no-shared-
	// algorithm case the closest spec-listed cause is also #96
	// (the UE Security Capability content failed validation).
	// Silently logging and returning leaves the UE waiting for an
	// SMC that never comes — must reject so the UE knows.
	eea, eia, err := negotiateAlgorithms(ue.Security.UESecCap, log)
	if err != nil {
		log.WithIMSI(ue.IMSI).Errorf("Algorithm negotiation failed amfUeID=%d: %v — RegReject cause #96 per TS 24.501 §5.5.1.2.8(b)",
			ue.AmfUeNGAPID, err)
		abortRegistrationAndReleaseN1(ue, CauseInvalidMandatoryInfo, genngap.CauseNasUnspecified)
		return
	}

	// Install the new (non-current) NAS security context per
	// TS 24.501 v19.6.2 §4.4.2.1: derive K_NASEnc / K_NASInt from K_AMF
	// (TS 33.501 §A.8), stamp algorithm IDs, reset UL/DL NAS COUNTs to
	// zero (TS 24.501 §4.4.3.1). The ngKSI used is the one
	// startAuthentication chose under §5.4.1.3.2 + §5.4.1.3.4 and
	// parked on ue.Security.PendingNGKSI; the SECURITY MODE COMMAND
	// must carry the same value per §5.4.2.2.
	if err := security.ActivateCtx(ue, ue.Security.PendingNGKSI, eea, eia); err != nil {
		log.Errorf("security.ActivateCtx amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	// Replay the UE's own reported capability octets verbatim (TS 24.501
	// §9.11.3.54A). Sending a shorter/hardcoded value makes the UE
	// compare-and-reject because the replay ≠ what it advertised. Fallback
	// to NEA0/NIA0-only if the Registration Request never carried one
	// (unusual — tester bug or out-of-order message), so the encoder
	// doesn't produce a zero-length IE.
	replay := ue.Security.UESecCap
	if len(replay) < 2 {
		// Safety fallback: UE didn't advertise any caps (tester bug or
		// truncated RR). Advertise NEA0 + NIA0 bits only (null cipher +
		// null integrity — mandatory in every UE per TS 33.501 §5.3.1
		// / §5.3.2) so the codec doesn't encode a zero-length IE.
		replay = []byte{algoBitNxA0, algoBitNxA0}
	}
	// TS 24.501 §9.11.3.28 IMEISV request — ask the UE to include its
	// IMEISV in SECURITY MODE COMPLETE. Policy today is "always ask on
	// first registration"; production builds would gate on UDM
	// subscription (if IMEISV is already known and valid, skip asking).
	// TODO(spec: TS 24.501 §5.4.2.2 "IMEISV request") — make this
	//   policy-driven from operator config / UDM Device Data.
	imeisvReq := &nas.IMEISVRequest{Value: nas.IMEISVRequestImeisvRequested}

	// TS 24.501 §9.11.3.12 Additional 5G Security Information:
	//   bit 1 (RINMR) — Retransmission of Initial NAS Message Requested
	//   bit 2 (HDP)   — Horizontal Derivation Parameter
	//
	// RINMR=1 asks the UE to include its full RR in SMC Complete's
	// NASMessageContainer — required by §4.4.6 case (a) when the UE's
	// initial RR arrived cleartext-only. When the initial RR already
	// carried non-cleartext IEs (case (b.1), handled by re-dispatch in
	// the dispatch layer), we don't need retransmission.
	var add5GVal byte = 0x00
	if ue.InitialRRCleartextOnly {
		add5GVal |= 0x02 // RINMR (bit 2 in octet 3; value 0x02)
	}
	add5G := &nas.Additional5GSecurityInformation{Value: []byte{add5GVal}}

	smc := &nas.SecurityModeCommand{
		SelectedNASSecurityAlgorithms: nas.NASSecurityAlgorithms{
			CipheringAlgorithm: eea,
			IntegrityAlgorithm: eia,
		},
		// TS 24.501 v19.6.2 §5.4.2.2 — the SMC's ngKSI identifies the
		// new (non-current) 5G NAS security context that's about to be
		// taken into use. This is the value startAuthentication picked
		// for the AUTHENTICATION REQUEST (per §5.4.1.3.2 + §5.4.1.3.4),
		// parked on the pending slot.
		NgKSI:                           nas.NASKeySetIdentifier{TSC: 0, KeySetIdentifier: ue.Security.PendingNGKSI},
		Spare:                           nas.NASKeySetIdentifier{},
		ReplayedUESecurityCapabilities:  nas.ReplayedUESecurityCapabilities{Value: replay},
		IMEISVRequest:                   imeisvReq,
		Additional5GSecurityInformation: add5G,
	}
	encoded, err := smc.Encode()
	if err != nil {
		log.Errorf("SecurityModeCommand encode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	// TS 24.501 §4.4.4.2 / §8.2.25 + TS 33.501 §6.7.2 step 1b: SMC must
	// be sent integrity-protected (but not ciphered) with Security
	// Header Type = 3 (new security context). security.TxDL is a no-op
	// here because ue.Security.Activated=false until Security Mode
	// Complete arrives; TxSMC is the dedicated SHT=3 entry point.
	wrapped, err := security.TxSMC(ue, encoded)
	if err != nil {
		log.Errorf("SecurityModeCommand SHT=3 wrap amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.Errorf("startSecurityMode amfUeID=%d: gNB %q gone", ue.AmfUeNGAPID, ue.GnbKey)
		return
	}
	// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
	if err := dlnas.Send(gnb, ue, wrapped); err != nil {
		log.Errorf("DL SecurityModeCommand amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	// Cache the wrapped bytes so T3560's retransmit hook (up to
	// NASMaxRetransmit per TS 24.501 §10.2) re-emits the same
	// integrity-protected PDU instead of rebuilding and risking a
	// different NAS count.
	ue.RetxNASPDU = wrapped
	ue.GMMSub = uectx.GMMSubSecurityMode
	pm.Inc(pm.SecAtt, 1)

	// T3560 (TS 24.501 §5.4.2.2) is started declaratively by the GMM
	// FSM on entry to StateSecurityMode — see fsm_transitions.go.

	log.WithIMSI(ue.IMSI).Infof("Security Mode Command sent amfUeID=%d NEA%d/NIA%d",
		ue.AmfUeNGAPID, eea, eia)
}

func handleSecurityModeComplete(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.smc")
	if !allowedIn(ue, "SECURITY MODE COMPLETE", fsm.StateSecurityMode) {
		return
	}
	// T3560 is cancelled by the FSM on leaving StateSecurityMode.

	// TS 24.501 §5.4.2.4 (NAS security mode control completion by
	// the network) only mandates "stop timer T3560" and activate
	// security on receipt; duplicate SMC Complete handling isn't
	// specified. Drop duplicates as an implementation-defined
	// idempotency choice: re-running the post-SMC work would ship a
	// second Registration Accept with a fresh DL NAS COUNT
	// (§4.4.3.1), drifting from the UE's expected count and
	// breaking subsequent UL integrity checks.
	//
	// Note: dedup is keyed on Pending=false here (not on Activated)
	// because a re-registration in 5GMM-REGISTERED state runs a fresh
	// SMC procedure while Activated is still true from the prior
	// registration. Pending=false means "no SMC in flight" — which is
	// the right duplicate condition.
	if ue.Security != nil && !ue.Security.Pending {
		log.WithIMSI(ue.IMSI).Debugf("SecurityModeComplete (dup) amfUeID=%d — no pending SMC, ignoring",
			ue.AmfUeNGAPID)
		return
	}

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("SecurityModeComplete decode: %v", err)
		pm.Inc(pm.SecFail, 1)
		return
	}
	smc, ok := msg.(*nas.SecurityModeComplete)
	if !ok {
		log.Errorf("SecurityModeComplete: unexpected type %T", msg)
		pm.Inc(pm.SecFail, 1)
		return
	}

	pm.Inc(pm.SecSucc, 1)
	// TS 24.501 v19.6.2 §5.4.2.4 verbatim: "The AMF shall, upon
	// receipt of the SECURITY MODE COMPLETE message, stop timer
	// T3560. From this time onward the AMF shall integrity protect
	// and encipher all signalling messages with the selected 5GS
	// integrity and ciphering algorithms." Promote the non-current
	// (pending) ctx into the operative algorithm/key/count slots —
	// after this, §4.4.2.1's "current 5G NAS security context" is
	// the post-SMC one.
	security.PromoteContext(ue)
	ue.Security.AuthDone = true
	ue.Security.Activated = true // §5.4.2.4 — DL NAS now secured (SHT=2)
	ue.GMMSub = uectx.GMMSubNone
	log.WithIMSI(ue.IMSI).Infof("Security Mode Complete amfUeID=%d — NAS context established",
		ue.AmfUeNGAPID)

	// TS 24.501 v19.6.2 §4.4.6 case (a) — verbatim from line 4723-4733:
	//   "If the UE does not have a valid 5G NAS security context, the
	//    UE sends a REGISTRATION REQUEST message including cleartext
	//    IEs only. After activating a 5G NAS security context resulting
	//    from a security mode control procedure:
	//      1) if the UE needs to send non-cleartext IEs, the UE shall
	//         include the entire REGISTRATION REQUEST message ... in
	//         the NAS message container IE and shall include the NAS
	//         message container IE in the SECURITY MODE COMPLETE
	//         message; or
	//      2) if the UE does not need to send non-cleartext IEs, the
	//         UE shall include the entire REGISTRATION REQUEST message
	//         (i.e. containing cleartext IEs only) in the NAS message
	//         container IE and shall include the NAS message container
	//         IE in the SECURITY MODE COMPLETE message."
	//
	// Both 1) and 2) say "shall include" — when InitialRRCleartextOnly
	// drove RINMR=1 in the SMC, the SMC Complete MUST carry the
	// container. A UE that omits it has violated §4.4.6(a); abort the
	// registration with cause #100 (conditional IE error: container is
	// conditional on RINMR=1).
	if ue.InitialRRCleartextOnly && (smc.NASMessageContainer == nil || len(smc.NASMessageContainer.Value) == 0) {
		log.WithIMSI(ue.IMSI).Errorf("SMC Complete missing NASMessageContainer although AMF set RINMR=1 — aborting per TS 24.501 §4.4.6(a) cause #100")
		pm.Inc(pm.SecFail, 1)
		ue.GMMProc = uectx.GMMProcRegistration
		abortRegistrationAndReleaseN1(ue, CauseConditionalIEError,
			genngap.CauseNasUnspecified)
		return
	}
	if smc.NASMessageContainer != nil && len(smc.NASMessageContainer.Value) > 0 {
		applyContainerRRToUE(ue, smc.NASMessageContainer.Value, log)
	}

	// Ask the gNB to establish the UE context — Registration Accept follows
	// as a separate DL NAS Transport once the gNB ACKs with
	// InitialContextSetupResponse. See sendRegistrationAccept in registration.go.
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb != nil {
		// TODO(arch: event: ICS to NGAP — see gmm/doc.go)
		if err := initialctxsetup.Send(gnb, ue); err != nil {
			log.Errorf("InitialContextSetup send amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		}
	}

	// Nudm_UECM_Registration (TS 29.503 §5.3.2.2) per TS 23.502
	// §4.2.2.2.2 step 14: after successful primary authentication the
	// new AMF MUST register with the UDM as the serving AMF for this
	// UE. Returns any prior registration so we can log the override
	// (maps to §5.3.2.3 DeregistrationNotification in a multi-AMF
	// deployment; single-AMF binary just self-supersedes).
	if prev, err := udm.RegisterAMF(ue.IMSI, ue.AmfUeNGAPID, ""); err != nil {
		log.WithIMSI(ue.IMSI).Warnf("udm.RegisterAMF failed amfUeID=%d: %v", ue.AmfUeNGAPID, err)
	} else if prev != nil {
		log.WithIMSI(ue.IMSI).Infof("udm.RegisterAMF superseded prior amf_ue_id=%d (§5.3.2.3 DeregistrationNotification)",
			prev.AmfUeNgapID)
	}

	// Send Registration Accept via DL NAS Transport. In a full deployment
	// we'd wait for InitialContextSetupResponse, but for the skeleton
	// (and the happy-path tests) shipping it immediately lets the UE
	// complete registration without the extra round trip.
	sendRegistrationAccept(ue)

	// Advance the FSM after the post-SMC NAS work is done — T3550 is
	// armed by the SECURITY_MODE → REGISTERED_INITIATED transition, and
	// RetxNASPDU now holds the Reg Accept bytes so the T3550 retransmit
	// callback will ship the right PDU.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvSecurityModeComplete, Inner: inner})
}

// Bit positions inside each byte of the UE Security Capability IE
// (TS 24.501 §9.11.3.54) — shared between negotiateAlgorithms and
// the UESecCap fallback above.
//
//	Byte 0 (5G-EA): bit7=NEA0, bit6=NEA1, bit5=NEA2, bit4=NEA3, bits3..0 reserved
//	Byte 1 (5G-IA): bit7=NIA0, bit6=NIA1, bit5=NIA2, bit4=NIA3, bits3..0 reserved
const (
	algoBitNxA0 uint8 = 0x80 // NEA0 / NIA0
	algoBitNxA1 uint8 = 0x40 // NEA1 (SNOW3G) / NIA1 (SNOW3G)
	algoBitNxA2 uint8 = 0x20 // NEA2 (AES)    / NIA2 (AES)
	algoBitNxA3 uint8 = 0x10 // NEA3 (ZUC)    / NIA3 (ZUC)
)

// negotiateAlgorithms selects NAS ciphering + integrity algorithms per
// TS 33.501 §6.7.2: highest-priority algorithm from AMF context (loaded
// from security_algorithms at startup) that the UE also supports.
func negotiateAlgorithms(ueSecCap []byte, log *logger.Logger) (eea, eia uint8, err error) {
	// Algorithm name → bit mask in UESecCap byte (TS 24.501 §9.11.3.54).
	algoBit := map[string]uint8{
		"NEA0": algoBitNxA0, "NEA1": algoBitNxA1, "NEA2": algoBitNxA2, "NEA3": algoBitNxA3,
		"NIA0": algoBitNxA0, "NIA1": algoBitNxA1, "NIA2": algoBitNxA2, "NIA3": algoBitNxA3,
	}

	ueSupports := func(name string, byteIdx int) bool {
		bit, ok := algoBit[name]
		if !ok || byteIdx >= len(ueSecCap) {
			return false
		}
		return ueSecCap[byteIdx]&bit != 0
	}

	// Negotiate ciphering (byte 0 of UESecCap) from AMF context priority list
	eea = 0 // fallback NEA0 (null ciphering — TS 33.501 §5.3.1 mandatory)
	for _, a := range amfctx.Default.CipheringAlgos() {
		if ueSupports(a.Algorithm, 0) {
			eea = a.AlgoID
			log.Debugf("Ciphering: selected %s (priority=%d)", a.Algorithm, a.Priority)
			break
		}
	}

	// Negotiate integrity (byte 1 of UESecCap) from AMF context priority list
	eia = 0 // fallback NIA0 (null integrity — forbidden for NAS per TS 33.501 §5.3.2)
	for _, a := range amfctx.Default.IntegrityAlgos() {
		if ueSupports(a.Algorithm, 1) {
			eia = a.AlgoID
			log.Debugf("Integrity: selected %s (priority=%d)", a.Algorithm, a.Priority)
			break
		}
	}

	if eia == 0 {
		return 0, 0, fmt.Errorf("no integrity algorithm negotiated — NIA0 forbidden for NAS")
	}

	return eea, eia, nil
}

// handleSecurityModeReject processes SECURITY MODE REJECT (TS 24.501
// §8.2.26). Per §5.4.2.5 "NAS security mode command not accepted by the UE":
//
//	"Upon receipt of the SECURITY MODE REJECT message, the AMF shall stop
//	 timer T3560. The AMF shall also abort the ongoing procedure that
//	 triggered the initiation of the NAS security mode control procedure.
//	 Both the UE and the AMF shall apply the 5G NAS security context in
//	 use before the initiation of the security mode control procedure."
//
// Our implementation:
//   - T3560 stopped by the FSM on leaving StateSecurityMode.
//   - Derived keys from the just-failed SMC attempt (KNASEnc/KNASInt)
//     are wiped; KAMF/KSEAF/KAUSF from the preceding authentication
//     stay so a subsequent SMC retry can re-derive cleanly. AuthDone
//     stays true (the auth itself succeeded, only the SMC failed).
//   - If the trigger was a registration procedure, abort it with
//     REGISTRATION REJECT cause #24 "Security mode rejected,
//     unspecified" (§5.5.1.2.5 + Table 9.11.3.2.1 cause 24).
//   - Release the N1 NAS signalling connection so the UE can
//     re-register cleanly.
func handleSecurityModeReject(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.smc")
	if !allowedIn(ue, "SECURITY MODE REJECT", fsm.StateSecurityMode) {
		return
	}
	// T3560 is cancelled by the FSM on leaving StateSecurityMode.
	pm.Inc(pm.SecFail, 1)
	pm.Inc(pm.RegFail, 1)

	cause := uint8(0x18) // default = cause #24 "Security mode rejected, unspecified"
	if msg, err := nas.DecodeNASMessage(inner); err == nil {
		if sr, ok := msg.(*nas.SecurityModeReject); ok {
			cause = sr.Cause5GMM.Value
			log.Warnf("SecurityModeReject amfUeID=%d cause=0x%02X (%d)",
				ue.AmfUeNGAPID, cause, cause)
		} else {
			log.Warnf("SecurityModeReject amfUeID=%d: unexpected decode type %T", ue.AmfUeNGAPID, msg)
		}
	} else {
		log.Warnf("SecurityModeReject amfUeID=%d: decode failed: %v", ue.AmfUeNGAPID, err)
	}

	// TS 24.501 v19.6.2 §5.4.2.5 verbatim: "Both the UE and the AMF
	// shall apply the 5G NAS security context in use before the
	// initiation of the security mode control procedure, if any, to
	// protect the SECURITY MODE REJECT message and any other
	// subsequent messages …" The non-current (rejected) ctx lives in
	// the Pending slot; the operative ctx is unchanged. Discarding
	// Pending IS the revert. KAMF/KSEAF survive on the operative ctx
	// so a fresh SMC retry can run without re-authenticating.
	security.DiscardPending(ue)

	// Record the state transition before the abort helper removes the
	// ctx: SECURITY_MODE → DEREGISTERED on SecurityModeReject.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvSecurityModeReject, Inner: inner})

	// Full abort + N1 release. Sends REGISTRATION REJECT cause #24 if
	// the trigger was a registration procedure (GMMProc check inside).
	abortRegistrationAndReleaseN1(ue, cause, genngap.CauseNasUnspecified)
}

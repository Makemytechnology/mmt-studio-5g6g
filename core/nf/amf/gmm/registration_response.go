// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Extracted from registration.go by refactor: split god-file by
// sub-concern. Imports are re-derived by goimports.
package gmm

import (
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/kpi"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/uectxrelease"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	nas "github.com/mmt/nasgen/generated"
)

// sendRegistrationAccept ships the Registration Accept NAS message.
// Called from smc.go after Security Mode Complete. dlnas.Send will
// security-wrap with SHT=2 via SecureWrapDL once Activated flipped.
func sendRegistrationAccept(ue *uectx.AmfUeCtx) {
	log := logger.Get("amf.gmm.registration")

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.Errorf("sendRegistrationAccept amfUeID=%d: gNB %q gone", ue.AmfUeNGAPID, ue.GnbKey)
		return
	}

	// Registration Accept IEs — TS 24.501 Table 8.2.7.1.1.
	// Only the 5GS registration result is mandatory (M); everything else
	// is optional and conditional on UE request / AMF policy per the
	// "shall include" clauses in §5.5.1.2.4. Items marked TODO(spec:…)
	// below are optional IEs we do not yet populate; the spec clause on
	// each TODO identifies exactly when the AMF shall include it.
	accept := &nas.RegistrationAccept{
		// TS 24.501 §9.11.3.6 Registration Result (M) — driven from state.
		RegistrationResult:    buildRegistrationResult(ue),
		GUTI5G:                buildAssigned5GGUTI(ue),
		TAIList:               buildTAIList(gnb),
		NetworkFeatureSupport: buildNetworkFeatureSupport(),
		// TS 24.501 §9.11.2.5 GPRS Timer 3 — UE periodic registration
		// update timer. Encoded from timers.T3512 (TS 24.008
		// §10.5.7.4a). Value flows from DB/config → timers package →
		// NAS IE — no hex literal in the AMF procedure code.
		T3512Value: &nas.GPRSTimer3{Value: timers.EncodeGPRSTimer3(timers.T3512)},

		// TS 24.501 §5.5.1.2.4 "allowed NSSAI" — MUST be included when
		// NSSF selection produced allowed S-NSSAIs. Encoding per
		// §9.11.3.37 (NSSAI IE) + §9.11.2.8 (S-NSSAI).
		AllowedNSSAI: buildNASAllowedNSSAI(ue),

		// TS 24.501 §5.5.1.2.4 + §4.6.2.5 "rejected NSSAI" — included
		// when NSSF rejected one or more requested S-NSSAIs. Encoding
		// per §9.11.3.46.
		RejectedNSSAI: buildNASRejectedNSSAI(ue),

		// TS 24.501 §5.5.1.2.4 "MICO indication" — echoed only when the
		// UE requested MICO in its RR and AMF policy accepts.
		MICOIndication: buildMICOResponse(ue),

		// TODO(spec: TS 24.501 §5.5.1.2.4 "Configured NSSAI") — when the
		//   UE needs an updated Configured NSSAI (subscription change /
		//   first provisioning) the AMF shall include it.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "equivalent PLMNs") — include
		//   the Equivalent PLMNs IE when an EPLMN list is configured for
		//   this subscriber's HPLMN.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "service area list") — when
		//   mobility restrictions apply (forbidden TAIs, allowed TAIs),
		//   the AMF shall include the Service area list IE.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 + §5.5.1.3.4 "PDU session status") —
		//   for re-registration / mobility update, include PDU session
		//   status IE if the AMF-side view differs from the UE's request.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "PDU session reactivation result") —
		//   include when resuming PDU sessions that were inactive at the UE.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "MICO indication") — when the UE
		//   requested MICO in the RR and the AMF accepts, include the
		//   MICO indication IE. Today we always ignore the UE's MICO request.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "Network slicing indication") —
		//   indicate default slice usage / pending slice when applicable.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "emergency number list") —
		//   include local emergency numbers (112, 911, operator-specific).
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "SOR transparent container") —
		//   include the HPLMN-provided SoR container when steering is
		//   configured. SoR is HPLMN→UDM→AMF opaque forwarding.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "EAP message") — when primary
		//   authentication used EAP-AKA' / EAP-TLS, the EAP-Success
		//   (if not already sent in a prior DL NAS) is included here.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "NSSAI inclusion mode") —
		//   when the UE supports NSSAI inclusion mode, the AMF shall
		//   include the NSSAI inclusion mode IE per operator policy.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "Negotiated DRX parameters") —
		//   when the UE requested specific DRX values, the AMF shall
		//   echo the negotiated values.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "T3502/T3447/T3448/T3324") —
		//   include T3502 (non-3GPP dereg), T3447, T3448 (CIoT congestion)
		//   and T3324 (MICO active time) when operator policy mandates
		//   non-default values.
		//
		// TODO(spec: TS 24.501 §5.5.1.2.4 "LADN information") — when the UE
		//   requested LADN DNNs and subscription allows, include them.
	}
	encoded, err := accept.Encode()
	if err != nil {
		log.Errorf("RegistrationAccept encode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
	if err := dlnas.Send(gnb, ue, encoded); err != nil {
		log.Errorf("DL RegistrationAccept amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	// Stash for T3550 retransmit (TS 24.501 §10.2 Table 10.2.1, N3550=4).
	// dlnas.Send has already wrapped with SHT=2 once Security is active,
	// so the cached bytes are ready to go back out verbatim.
	ue.RetxNASPDU = encoded
	log.WithIMSI(ue.IMSI).Infof("Registration Accept sent amfUeID=%d", ue.AmfUeNGAPID)

	// T3550 (TS 24.501 §5.5.1.2.2) is started declaratively by the
	// GMM FSM on entry to StateRegisteredInitiated — see
	// fsm_transitions.go. Expiry surfaces as EvT3550Expired.
}

// Identity type values per TS 24.501 §9.11.3.3 Table 9.11.3.3.1.
const (
	IdentityTypeNone    = 0x00 // "No identity"
	IdentityTypeSUCI    = 0x01 // SUCI
	IdentityType5GGUTI  = 0x02 // 5G-GUTI
	IdentityTypeIMEI    = 0x03
	IdentityType5GSTMSI = 0x04
	IdentityTypeIMEISV  = 0x05
	IdentityTypeMAC     = 0x06
	IdentityTypeEUI64   = 0x07
)

// sendIdentityRequest triggers the Identification procedure (TS 24.501
// §5.4.3). idType selects which identity the network wants back; pass
// IdentityTypeSUCI when the initial NAS message couldn't resolve to a
// SUPI (the common registration-triggered case) or IdentityTypeIMEISV
// for IMEISV lookup after SMC if not captured via SecurityModeCommand.
func sendIdentityRequest(ue *uectx.AmfUeCtx, idType uint8) {
	log := logger.Get("amf.gmm.identity")

	// TS 24.501 §9.11.3.3: IdentityType half-octet IE. The codec packs
	// it via the ServiceType wrapper — low 3 bits carry the value.
	req := &nas.IdentityRequest{
		IdentityType: nas.ServiceType{Value: idType & 0x07},
	}
	encoded, err := req.Encode()
	if err != nil {
		log.Errorf("IdentityRequest encode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return
	}
	// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
	_ = dlnas.Send(gnb, ue, encoded)
	// T3570 retransmits this exact PDU up to NASMaxRetransmit per
	// TS 24.501 §10.2 (N3570=4).
	ue.RetxNASPDU = encoded

	// GMM FSM advances DEREGISTERED → IDENTIFICATION and arms T3570.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvIdentityRequestSent})

	log.Infof("Identity Request sent amfUeID=%d", ue.AmfUeNGAPID)
}

// abortRegistrationAndReleaseN1 is the unified teardown used by every
// failure path in the registration/auth/SMC/identity sequence:
//
//   - If a registration procedure is in progress (GMMProc == Registration),
//     ship REGISTRATION REJECT with the supplied cause (TS 24.501 §5.5.1.2.5).
//   - Release any active PDU session (TS 24.501 §5.5.2.1: "If the
//     de-registration procedure for 5GS services is performed, a local
//     release of the PDU sessions … for this particular UE is performed.")
//     — applies because the function ends with RM=Deregistered below; SMF
//     drives PFCP §7.5.6 Session Deletion before the gNB tears down RAN.
//   - Fire NGAP UE Context Release Command to the gNB with CauseNas,
//     releasing the N1 NAS signalling connection (TS 38.413 §8.3.3).
//   - Cancel any remaining per-UE NAS timers and remove the UE context.
//
// Callers use the 5GMM cause (§Table 9.11.3.2.1) that matches the failure
// (e.g. CauseIllegalUE=3 for unknown subscriber, CauseUEIdentityCannotBeDerived=9
// for unresolved SUCI, CauseUESecCapMismatch=23 / CauseSecurityModeRejected=24
// for SMC failure).  gnbCause is the NGAP CauseNas to carry on
// UEContextReleaseCommand (TS 38.413 §9.3.1.2): authentication-failure
// for #3/#20/#26/etc., normal-release for clean dereg, unspecified for
// the generic case.
func abortRegistrationAndReleaseN1(ue *uectx.AmfUeCtx, cause5GMM uint8, gnbCause genngap.CauseNas) {
	log := logger.Get("amf.gmm.registration")
	if ue == nil {
		return
	}

	if ue.GMMProc == uectx.GMMProcRegistration {
		sendRegistrationReject(ue, cause5GMM)
	}

	releaseAllPDUSessions(ue, log)

	// TS 38.413 v19.2.0 §8.3.3.2 (Successful Operation) — after the AMF
	// sends UE CONTEXT RELEASE COMMAND, the NG-RAN node "shall release
	// all related signalling and user data transport resources and reply
	// with the UE CONTEXT RELEASE COMPLETE message". The Complete is
	// allowed to carry PDU Session Resource List, User Location
	// Information, Recommended Cells / RAN Nodes for Paging, and Paging
	// Assistance Data for CE Capable UE — all of which the spec requires
	// the AMF to process ("the AMF shall handle this information…").
	// Therefore the UE context must remain locatable from amfUeID +
	// ranUeID until handleComplete runs; otherwise locateUE returns nil
	// and the AMF emits an §10.x Error Indication for an unknown UE-
	// associated logical NG-connection.
	//
	// Per TS 33.501 v19.2.0 §6.8.1.1.1 case 1 (registration reject), the
	// AMF "shall remove all the 5G NAS security parameters" for this UE.
	// We arm that hard-remove via HardRemoveOnComplete; the actual ctx
	// Remove happens in uectxrelease.handleComplete after §8.3.3.2 IEs
	// are processed (replacing the ClearVolatile path that other §8.3.3
	// trigger types use per §6.8.1.1.1 cases 2.a / 2.b).
	ue.HardRemoveOnComplete = true

	if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
		log.WithIMSI(ue.IMSI).Infof("releasing N1 NAS connection amfUeID=%d cause5GMM=%d NGAP-cause=nas(%d)",
			ue.AmfUeNGAPID, cause5GMM, int64(gnbCause))
		// TODO(arch: event: UE-Context-Release to NGAP — see gmm/doc.go)
		_ = uectxrelease.SendCommand(gnb, ue, uectxrelease.CauseNAS(gnbCause))
	} else {
		// No gNB to send the Command to → no Complete will ever arrive,
		// so the deferred Remove in handleComplete won't fire. Remove
		// here so the ctx doesn't leak. (gnb == nil typically means the
		// SCTP association already dropped, in which case the SCTP
		// cascade in ngap/server has already cleaned up; this is a
		// defensive fallback.)
		uectx.Default.Remove(ue)
	}

	// Cancel any per-UE NAS timers (T3560 / T3550 / T3570 / T3522 / T3555)
	// still armed on this ctx — the FSM drops to StateDeregistered below.
	timers.M.CancelAllForUE(fmt.Sprintf("%d", ue.AmfUeNGAPID))

	ue.GMMProc = uectx.GMMProcNone
	ue.GMMSub = uectx.GMMSubNone
	ue.RM = uectx.RMDeregistered
	ue.CM = uectx.CMIdle
}

const (
	CauseIllegalUE                 = 3
	CausePEINotAccepted            = 5
	CauseIllegalME                 = 6
	Cause5GServicesNotAllowed      = 7
	CauseUEIdentityCannotBeDerived = 9
	CauseImplicitlyDeregistered    = 10
	CausePLMNNotAllowed            = 11
	CauseTANotAllowed              = 12
	CauseRoamingNotAllowed         = 13
	CauseNoSuitableCells           = 15
	CauseCongestion                = 22
	CauseUESecCapMismatch          = 23
	CauseSecurityModeRejected      = 24
	CauseNoNetworkSlicesAvailable  = 62
	CauseNgKSIAlreadyInUse         = 71
	// §5.5.1.2.8 b) protocol-error causes. Mapping uses #111 today;
	// finer #96/#99/#100 require the NAS codec to surface the specific
	// decode failure (which IE was invalid / missing / unimplemented).
	CauseInvalidMandatoryInfo   = 96
	CauseIENonExistentOrNotImpl = 99
	CauseConditionalIEError     = 100
	CauseProtocolError          = 111 // "protocol error, unspecified"
)

// sendRegistrationReject builds and sends a Registration Reject NAS message
// (type 0x44, TS 24.501 §8.2.9). Cause IE populated per TS 24.501
// Table 9.11.3.2.1 (5GMM cause).
//
// TODO(spec: TS 24.501 §5.5.1.2.5 + Table 8.2.9.1.1) — several optional
//
//	IEs on Registration Reject are not populated:
//	• T3346 value — congestion back-off (GPRS Timer 2, included with
//	  cause #22 "congestion").
//	• T3502 value — included with cause #11 "PLMN not allowed" etc.
//	• EAP message — when authentication ran via EAP and ended in
//	  failure, carry EAP-Failure here.
//	• Rejected NSSAI — with cause #62 "no network slices available",
//	  carry the slices that were rejected.
//	• CAG information list / Extended CAG information list — with
//	  cause #76 "no allowed CAG cells selected".
//	• Extended rejected NSSAI — paired with "max number of slices"
//	  trigger; required with cause #73.
//	• Service-level-AA container — when UUAA-MM auth failed.
//	• Disaster return wait range / List of PLMNs to be used in
//	  disaster condition — with cause #80 "Disaster roaming for the
//	  determined PLMN not allowed".
//	Each needs a guarded branch: only included when the paired cause
//	value actually fires.
func sendRegistrationReject(ue *uectx.AmfUeCtx, cause uint8) {
	log := logger.Get("amf.gmm.registration")
	pm.Inc(pm.RegFail, 1)

	reject := &nas.RegistrationReject{
		Cause5GMM: nas.FiveGMMCause{Value: cause},
	}
	encoded, err := reject.Encode()
	if err != nil {
		log.Errorf("RegistrationReject encode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.Errorf("sendRegistrationReject amfUeID=%d: gNB %q gone", ue.AmfUeNGAPID, ue.GnbKey)
		return
	}
	// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
	if err := dlnas.Send(gnb, ue, encoded); err != nil {
		log.Errorf("DL RegistrationReject amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	ue.GMMProc = uectx.GMMProcNone
	ue.GMMSub = uectx.GMMSubNone
	log.Warnf("Registration Reject sent amfUeID=%d cause=%d", ue.AmfUeNGAPID, cause)

	// KPI: terminal failure transition. Records latency for visibility
	// into fast-vs-slow rejects (auth failures trip fast, NSSAI
	// rejections trip after AKA, T3550 timeouts are slow).
	kpi.RecordFailure(ue.AmfUeNGAPID)
}

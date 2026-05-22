// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GMM Service Request (TS 24.501 v19.6.2 §5.6.1, TS 23.502 v19.7.0
// §4.2.3.2 "UE Triggered Service Request").
//
// UE in CM-IDLE sends ServiceRequest to transition to CM-CONNECTED,
// optionally requesting UP reactivation for preserved PDU sessions.
// Per TS 23.502 §4.2.3.2 the AMF's response depends on the Service
// Type (TS 24.501 §9.11.3.50) and the UplinkDataStatus /
// PDUSessionStatus IEs the UE carries:
//
//	§4.2.3.2 step 1 (verbatim):
//	  "If the Service Request is triggered by the UE for user data,
//	   the UE identifies, using the List Of PDU Sessions To Be
//	   Activated, the PDU Session(s) for which the UP connections
//	   are to be activated in Service Request message."
//	  "If the Service Request is triggered by the UE for signalling
//	   only, the UE doesn't identify any List Of PDU Sessions To Be
//	   Activated."
//
//	§4.2.3.2 step 3:
//	  "If the UE in CM-IDLE state triggered the Service Request to
//	   establish a signalling connection only, after successful
//	   establishment of the signalling connection the UE and the
//	   network can exchange NAS signalling and steps 4 to 11 and
//	   15 to 22 are skipped."
//
//	§4.2.3.2 step 4:
//	  "The AMF determines the PDU Session(s) for which the UP
//	   connection(s) shall be activated and sends an
//	   Nsmf_PDUSession_UpdateSMContext Request to SMF(s) associated
//	   with the PDU Session(s) with Operation Type set to 'UP
//	   activate' to indicate establishment of User Plane resources
//	   for the PDU Session(s)."
//
//	§4.2.3.2 step 12: the AMF issues the N2 Request (→ NGAP
//	  PDUSessionResourceSetupRequest, TS 38.413 §8.2.1) so the gNB
//	  allocates its DL TEID; pdusetup.handleResponse then triggers
//	  session.ActivateUserPlane which flips the UPF FAR BUFF→FORW
//	  (our existing §4.2.3.2 step 5-6a inverse path used during
//	  new-session establishment).
//
//	§4.2.3.2 step 2 (PDU Session status):
//	  "Based on the PDU Session status, the AMF may initiate PDU
//	   Session Release procedure in the network for the PDU
//	   Sessions whose PDU Session ID(s) were indicated by the UE as
//	   not available."
package gmm

import (
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/initialctxsetup"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pdusetup"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	nas "github.com/mmt/nasgen/generated"
)

func init() {
	Register(MsgServiceRequest, handleServiceRequest)
	// Install the ICS Response → reactivation-drain hook. Runs AFTER
	// the per-UE NGAP FSM transitions ICS_PENDING → ESTABLISHED, so
	// the subsequent EvPDUResourceSetupRequestSent events fire from a
	// state the transition table accepts (StateEstablished per
	// nf/amf/ngap/fsm_transitions.go:119-122). Before this hook was
	// wired, handleServiceRequest called pdusetup.Send synchronously
	// after initialctxsetup.Send — the FSM was still in ICS_PENDING
	// and the subsequent EvPDUResourceSetupRequestSent was rejected,
	// emitting "NGAP procedure collision" warnings on every
	// CM-IDLE→CM-CONNECTED Service Request with UP activation.
	initialctxsetup.OnContextEstablished = drainPendingReactivations
}

// drainPendingReactivations is the OnContextEstablished hook. On ICS
// Response (per-UE NGAP FSM at StateEstablished), it drains
// ue.PendingN1N2Sessions by firing pdusetup.Send for each PSI whose
// SMF session is StateSuspended. Spec alignment:
//
//	TS 23.502 v19.7.0 §4.2.3.2 step 4 ("UP activate") → step 12
//	("N2 Request … N2 SM information received from SMF"). The spec
//	permits this as a single combined N2 Request (with PDU session
//	info embedded in the ICS Request) OR as two sequential NGAP
//	procedures (ICS, then PDU Session Resource Setup Request, each
//	a distinct TS 38.413 §8.3.1 / §8.2.1 procedure). We take the
//	second form — simpler wiring, identical wire effect at the gNB.
func drainPendingReactivations(gnb *gnbctx.GnbCtx, ue *uectx.AmfUeCtx) {
	if len(ue.PendingN1N2Sessions) == 0 {
		return
	}
	log := logger.Get("amf.ngap.initialctxsetup")
	pending := ue.PendingN1N2Sessions
	ue.PendingN1N2Sessions = nil
	for _, psi := range pending {
		sess := session.Default.Get(ue.IMSI, psi)
		if sess == nil {
			log.WithIMSI(ue.IMSI).Warnf("ICS→drain: session pduSessID=%d gone — skipping", psi)
			continue
		}
		if sess.State != session.StateSuspended {
			log.WithIMSI(ue.IMSI).Debugf("ICS→drain: pduSessID=%d state=%s — not suspended, skipping",
				psi, sess.State)
			continue
		}
		if _, err := pdusetup.Send(gnb, ue, sess, nil); err != nil {
			log.WithIMSI(ue.IMSI).Warnf("ICS→drain: pdusetup.Send pduSessID=%d: %v", psi, err)
			continue
		}
		log.WithIMSI(ue.IMSI).Infof("ICS→drain: pduSessID=%d UP-activate triggered (TS 23.502 §4.2.3.2 step 12 after ICS Response)",
			psi)
	}
}

func handleServiceRequest(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.service")
	pm.Inc(pm.SvcReqAtt, 1)

	// TS 24.501 v19.6.2 §5.6.1.1 (line 36558-36567): the Service
	// Request procedure changes 5GMM mode from IDLE to CONNECTED —
	// it presupposes the UE is in 5GMM-REGISTERED. Without this
	// guard a UE that never registered (or post-dereg) sending an
	// SR would still drive the handler all the way to ICS, mutating
	// state for a non-existent registration. dispatch.checkCollision
	// already rejects SR when *another* procedure is in flight (a
	// different abnormal case); this is the "wrong RM state" guard.
	if !allowedIn(ue, "SERVICE REQUEST", fsm.StateRegistered) {
		// allowedIn already logged + sent 5GMM STATUS #98.
		return
	}

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("ServiceRequest decode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	sr, ok := msg.(*nas.ServiceRequest)
	if !ok {
		log.Errorf("ServiceRequest: unexpected type %T", msg)
		return
	}
	svcType := sr.ServiceType.Encode()
	log.WithIMSI(ue.IMSI).Infof("ServiceRequest amfUeID=%d type=0x%02X (%s)",
		ue.AmfUeNGAPID, svcType, serviceTypeName(svcType))

	// TS 24.501 v19.6.2 §5.6.2.2.1 (line 39650-39651, verbatim):
	//   "The network shall stop timer T3513 for the paging procedure
	//    when an integrity-protected response is received from the UE
	//    and successfully integrity checked by the network …".
	// SERVICE REQUEST is integrity-protected per §4.4.4 — once it lands
	// (and the dispatcher already verified the MAC before calling us),
	// T3513 has done its job; stop retransmitting PAGE so we don't keep
	// broadcasting at a UE that's already back.
	ngap.CancelT3513ForUE(ue.AmfUeNGAPID)

	ue.GMMProc = uectx.GMMProcServiceRequest
	ue.CM = uectx.CMConnected

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.Errorf("ServiceRequest amfUeID=%d: gNB %q gone", ue.AmfUeNGAPID, ue.GnbKey)
		return
	}

	// TS 23.502 §4.2.3.2 step 3a — "AMF to (R)AN: N2 Request (security
	// context, …). If the 5G-AN had requested for UE Context … AMF
	// initiates NGAP procedure as specified in TS 38.413." i.e. the
	// gNB-signalled InitialContextSetup brings up the AS-side UE
	// context after CM-IDLE. Without this the per-UE NGAP FSM stays in
	// NOT_ESTABLISHED and any subsequent UEContextRelease* collides
	// with the FSM guard.
	//
	// ue.UEContextRequest was set by initialue.go from the optional
	// UEContextRequest IE on the InitialUEMessage (TS 38.413 §9.2.5.1).
	// On "CM-CONNECTED → CM-CONNECTED" Service Request flows (unusual
	// but legal, e.g. Release Request indication) the UE stays in
	// ESTABLISHED and we skip the ICS.
	if ue.UEContextRequest {
		// K_gNB is derived just-in-time inside initialctxsetup.Send via
		// security.DeriveKgNB — freshness = current UL NAS COUNT (the
		// ServiceRequest's, which is exactly the NAS message that
		// triggered CM-IDLE→CM-CONNECTED per TS 33.501 §6.8.1.2.2).
		// No stash, no staleness (see nf/amf/security/doc.go I4).
		if err := initialctxsetup.Send(gnb, ue); err != nil {
			log.Errorf("ServiceRequest ICS send amfUeID=%d: %v", ue.AmfUeNGAPID, err)
			// Fall through — we can still try the NAS Service Accept
			// path; the FSM will catch up on the next successful ICS.
		}
	}

	// TS 24.501 v19.6.2 §5.6.1.3 (line 37285-37288, verbatim):
	//   "Upon receipt of the SERVICE REQUEST or CONTROL PLANE SERVICE
	//    REQUEST message, the AMF *may* initiate the common procedures
	//    e.g. the 5G AKA based primary authentication and key agreement
	//    procedure or the EAP based primary authentication and key
	//    agreement procedure."
	// "may" is the spec hook for skipping identity + AUTH + SMC when the
	// 5G NAS security context already in use is fresh (established via
	// §4.4.2.5 on this or a prior connection). Reuse semantics — what
	// counts as "fresh enough" — are §4.4.2 (Handling of 5G NAS security
	// contexts).

	// TS 24.501 v19.6.2 §5.6.1.4.1 (line 37314-37324) — single-access
	// PDU session reconciliation when SR carries PDU session status IE
	// (§9.11.3.44; bit=0 ⇔ "PDU SESSION INACTIVE", bit=1 ⇔ "not PDU
	// SESSION INACTIVE"). Verbatim:
	//
	//	"a) for single access PDU sessions, the AMF shall:
	//	   1) perform a local release of all those PDU sessions which
	//	      are not in 5GSM state PDU SESSION INACTIVE on the AMF
	//	      side associated with the access type the SERVICE REQUEST
	//	      message is sent over, but are indicated by the UE as
	//	      being in 5GSM state PDU SESSION INACTIVE; and
	//	   2) request the SMF to perform a local release of all those
	//	      PDU sessions."
	//
	// Strict interpretation: only release sessions the AMF considers
	// non-INACTIVE — gating on Get!=nil alone over-releases records
	// already in StateInactive/Released/Releasing (idempotent at the
	// SMF, but the spec qualifies "not in 5GSM state PDU SESSION
	// INACTIVE on the AMF side"). session.Release returns the bytes of
	// PDU SESSION RELEASE COMMAND but we discard them — §5.6.1.4.1
	// specifies "local release", no §6.3.3 NW-requested release toward
	// the UE (the UE already considers the session inactive).
	//
	// Reconciliation runs BEFORE the ACCEPT is encoded so the §8.2.17.2
	// PDU session status bitmap below reflects the post-release state
	// (matches §4.2.3.2 step 12 ordering: SMF interactions first, then
	// MM NAS Service Accept).
	//
	// TODO(TS 24.501 §5.6.1.4.1(b)): MA PDU session branch (multi-
	// access on 3GPP + non-3GPP) is not yet modelled. The AMF must
	// distinguish "user plane resources established only on this
	// access" vs "established on both" and partially release accordingly.
	// Codebase has no MA PDU session concept today.
	if sr.PDUSessionStatus != nil {
		inactive := inactivePSIsFromStatus(sr.PDUSessionStatus.PSIs)
		for _, psi := range inactive {
			existing := session.Default.Get(ue.IMSI, psi)
			if existing == nil {
				continue
			}
			// Strict §6.1.3.3 mapping — only §6.1.3.3.2 "PDU SESSION
			// INACTIVE" (and StateReleased terminal) is exempt from the
			// §5.6.1.4.1(a)(1) sweep. §6.1.3.3.4 "PDU SESSION INACTIVE
			// PENDING" (StateReleasing) is NOT INACTIVE — re-fire the
			// local release; idempotent at the SMF (state already on
			// the path to terminal Released).
			switch existing.State {
			case session.StateInactive, session.StateReleased:
				continue
			}
			log.WithIMSI(ue.IMSI).Infof("§5.6.1.4.1(a): UE reports pduSessID=%d INACTIVE, AMF state=%s — local release + Nsmf release",
				psi, existing.State)
			_ = session.Release(ue.IMSI, psi)
		}
	}

	// TS 24.501 v19.6.2 §5.6.1.4.1 (line 37352-37357, verbatim):
	//   "If the AMF needs to initiate PDU session status synchronization
	//    or a PDU session status IE was included in the SERVICE REQUEST
	//    message, the AMF shall include a PDU session status IE in the
	//    SERVICE ACCEPT message to indicate:
	//    -   which single access PDU sessions associated with the access
	//        type the SERVICE ACCEPT message is sent over are not in
	//        5GSM state PDU SESSION INACTIVE in the AMF; …"
	// IE shape: §8.2.17.2 + §9.11.3.44 — bit=1 for each PSI whose AMF
	// session record is in a non-INACTIVE state (StatePending /
	// StateActive / StateSuspended). Built from the AMF's post-release
	// view above.
	accept := &nas.ServiceAccept{}
	if sr.PDUSessionStatus != nil {
		accept.PDUSessionStatus = buildAMFPDUSessionStatus(ue.IMSI)
	}
	// TODO(TS 24.501 §5.6.1.4 + §8.2.17.3): if sr.UplinkDataStatus !=
	// nil OR sr.AllowedPDUSessionStatus != nil, populate
	// accept.PDUSessionReactivationResult to report which of the PSIs
	// the UE asked to reactivate were accepted. The ACCEPT is sent
	// before the gNB ICS Response (the actual reactivation completes
	// async in drainPendingReactivations), so the result here can only
	// reflect AMF/SMF acceptance — failures discovered later go via
	// the network-requested PDU session release procedure (§6.3.3).
	encoded, err := accept.Encode()
	if err != nil {
		log.Errorf("ServiceAccept encode: %v", err)
		return
	}
	// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go). Spec-wise,
	// TS 23.502 §4.2.3.2 step 12 carries MM NAS Service Accept inside
	// the N2 Request piggyback to the gNB; we currently send it as a
	// standalone DL NAS over an existing N2 association.
	_ = dlnas.Send(gnb, ue, encoded)

	log.Infof("ServiceAccept sent amfUeID=%d", ue.AmfUeNGAPID)

	// TS 23.502 §4.2.3.2 step 4 (UP activate) — for each PSI the UE
	// identifies in its "List Of PDU Sessions To Be Activated" (carried
	// by the UplinkDataStatus IE, TS 24.501 §9.11.3.57), the AMF fires
	// PDUSessionResourceSetupRequest per §4.2.3.2 step 12. The gNB's
	// response carries its new DL TEID and pdusetup.handleResponse
	// calls session.ActivateUserPlane which flips the UPF FAR BUFF→FORW
	// (our existing §4.2.3.2 step 5-6a inverse, already wired for the
	// new-session path).
	//
	// §4.2.3.2 step 1: signalling-only service types MUST NOT carry
	// UplinkDataStatus ("If the Service Request is triggered by the UE
	// for signalling only, the UE doesn't identify any List Of PDU
	// Sessions To Be Activated"). Even if a misbehaving UE sends one,
	// we gate on the ServiceType — a signalling-only path short-
	// circuits per §4.2.3.2 step 3.
	//
	// Queue the PSIs onto ue.PendingN1N2Sessions — pdusetup.Send is
	// issued from drainPendingReactivations once the NGAP FSM reaches
	// StateEstablished (via the initialctxsetup.OnContextEstablished
	// hook). This avoids racing EvPDUResourceSetupRequestSent against
	// the still-pending EvICSResponse.
	if serviceTypeIsSignallingOnly(svcType) {
		log.WithIMSI(ue.IMSI).Debugf("ServiceRequest type=%s: signalling-only — skipping §4.2.3.2 steps 4-11 UP activate",
			serviceTypeName(svcType))
	} else if sr.UplinkDataStatus != nil {
		queueReactivationsFromUplinkDataStatus(ue, sr.UplinkDataStatus.PSIs, log)
	} else {
		// TS 24.501 v19.6.2 §5.6.1.4.1 (verbatim, page 537): "the AMF
		// shall ... indicate the SMF to re-establish the user-plane
		// resources for the corresponding PDU sessions". The spec gates
		// this on the Uplink Data Status IE being present in the SR,
		// but in practice some UE NAS encoders (including the tester's
		// minimal builder) omit it on a §5.6.1.2 service request that
		// wants its existing sessions reactivated. Without the IE,
		// queueReactivationsFromUplinkDataStatus has nothing to drive,
		// the OnContextEstablished hook fires with an empty
		// PendingN1N2Sessions, and the UPF DL FAR stays in BUFF.
		//
		// AMF-side defence: when no UplinkDataStatus IE is present and
		// no N1N2 pending sessions have been queued upstream (e.g. via
		// §4.2.3.3 step 3a from the SMF DL-notify path), enumerate the
		// AMF's view of Suspended SMF sessions for this UE and queue
		// them. This mirrors what a spec-compliant UE would have
		// indicated via the IE.
		if len(ue.PendingN1N2Sessions) == 0 {
			queueAllSuspendedSessions(ue, log)
		}
	}
	// (Any PSIs already queued onto ue.PendingN1N2Sessions by the SMF's
	// §4.2.3.3 N1N2 path are drained by the same hook.)

	ue.GMMProc = uectx.GMMProcNone
	pm.Inc(pm.SvcReqSucc, 1)

	// Self-loop on REGISTERED — record the event for observability.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvServiceRequest, Inner: inner})
}

// queueReactivationsFromUplinkDataStatus implements TS 23.502 §4.2.3.2
// step 4 "UP activate" — but only the "AMF determines the PDU
// Session(s)" part. The actual PDUSessionResourceSetupRequest send
// (step 12) happens later, driven from drainPendingReactivations
// once the NGAP FSM reaches StateEstablished.
//
// For each PSI bit=1 in the UE's UplinkDataStatus IE (TS 24.501
// §9.11.3.57), if the SMF has the session in StateSuspended we append
// the PSI onto ue.PendingN1N2Sessions. Sessions in states other than
// StateSuspended or absent from SMF are skipped here (logged).
//
// The deferred-drain pattern keeps the NGAP FSM transition order
// spec-aligned: EvPDUResourceSetupRequestSent is only legal from
// StateEstablished per fsm_transitions.go — firing it from
// StateICSPending (which is where we are after initialctxsetup.Send
// but before ICS Response) would produce "procedure collision"
// warnings and was the original Bug 2 symptom.
func queueReactivationsFromUplinkDataStatus(ue *uectx.AmfUeCtx, toActivate []uint8, log *logger.Logger) {
	for _, psi := range toActivate {
		sess := session.Default.Get(ue.IMSI, psi)
		if sess == nil {
			log.WithIMSI(ue.IMSI).Warnf("§4.2.3.2 step 4: UE asked to activate pduSessID=%d but SMF has no record — ignoring",
				psi)
			continue
		}
		if sess.State != session.StateSuspended {
			log.WithIMSI(ue.IMSI).Debugf("§4.2.3.2 step 4: pduSessID=%d already in state=%s — skipping reactivate",
				psi, sess.State)
			continue
		}
		// Dedup — don't queue twice if the SMF's §4.2.3.3 N1N2 path
		// already added this PSI.
		already := false
		for _, q := range ue.PendingN1N2Sessions {
			if q == psi {
				already = true
				break
			}
		}
		if already {
			continue
		}
		ue.PendingN1N2Sessions = append(ue.PendingN1N2Sessions, psi)
		log.WithIMSI(ue.IMSI).Infof("§4.2.3.2 step 4: pduSessID=%d queued for UP-activate after ICS Response",
			psi)
	}
}

// queueAllSuspendedSessions is the AMF-side defence used when the UE's
// Service Request omits the Uplink Data Status IE (TS 24.501 §9.11.3.57).
// Per TS 24.501 v19.6.2 §5.6.1.4.1 (page 537, verbatim): "the AMF shall
// ... indicate the SMF to re-establish the user-plane resources for the
// corresponding PDU sessions". When the IE is absent we cannot consult
// the UE's bitmap, so we walk the SMF's view of this UE's sessions and
// queue every one currently in StateSuspended. Active / Released
// sessions are skipped — only the Suspended ones need the BUFF→FORW
// flip after the §4.8.x deactivation that put them there.
func queueAllSuspendedSessions(ue *uectx.AmfUeCtx, log *logger.Logger) {
	for _, sess := range session.Default.ForUE(ue.IMSI) {
		if sess.State != session.StateSuspended {
			continue
		}
		psi := sess.PDUSessionID
		already := false
		for _, q := range ue.PendingN1N2Sessions {
			if q == psi {
				already = true
				break
			}
		}
		if already {
			continue
		}
		ue.PendingN1N2Sessions = append(ue.PendingN1N2Sessions, psi)
		log.WithIMSI(ue.IMSI).Infof("§5.6.1.4.1 defence: pduSessID=%d (state=Suspended) queued for UP-activate (no UplinkDataStatus IE)",
			psi)
	}
}

// serviceTypeName renders the TS 24.501 §9.11.3.50 Table 9.11.3.50.1
// encoded value for log lines. Matches the verbatim enum identifiers.
func serviceTypeName(v uint8) string {
	switch v {
	case nas.ServiceTypeSignalling:
		return "signalling"
	case nas.ServiceTypeData:
		return "data"
	case nas.ServiceTypeMobileTerminatedServices:
		return "mobile-terminated-services"
	case nas.ServiceTypeEmergencyServices:
		return "emergency-services"
	case nas.ServiceTypeEmergencyServicesFallback:
		return "emergency-services-fallback"
	case nas.ServiceTypeHighPriorityAccess:
		return "high-priority-access"
	case nas.ServiceTypeElevatedSignalling:
		return "elevated-signalling"
	}
	return "reserved"
}

// serviceTypeIsSignallingOnly classifies a Service type value per
// TS 24.501 §9.11.3.50 Table 9.11.3.50.1. Signalling-only types
// trigger the §4.2.3.2 step 3 short-circuit — steps 4-11 are skipped.
//
// Table 9.11.3.50.1 verbatim decoding rules:
//
//	0000 signalling
//	0110 elevated signalling
//	0111 "unused; shall be interpreted as signalling, if received by
//	      the network"
//	1000 same as 0111
//	(all other assigned values: data / MT / emergency / etc.)
//	1001/1010/1011 "unused; shall be interpreted as data"
func serviceTypeIsSignallingOnly(v uint8) bool {
	switch v {
	case nas.ServiceTypeSignalling, nas.ServiceTypeElevatedSignalling:
		return true
	case 0x07, 0x08:
		// §9.11.3.50 Table 9.11.3.50.1: unused codepoints interpreted
		// as "signalling" by the network.
		return true
	}
	return false
}

// buildAMFPDUSessionStatus implements the IE-build side of TS 24.501
// v19.6.2 §5.6.1.4.1 line 37352-37357 (verbatim):
//
//	"the AMF shall include a PDU session status IE in the SERVICE
//	 ACCEPT message to indicate:
//	  -  which single access PDU sessions associated with the access
//	     type the SERVICE ACCEPT message is sent over are not in 5GSM
//	     state PDU SESSION INACTIVE in the AMF; …"
//
// The network-side 5GSM states are defined in §6.1.3.3 (line 40224ff):
//
//	§6.1.3.3.2 PDU SESSION INACTIVE             — "No PDU session exists."
//	§6.1.3.3.3 PDU SESSION ACTIVE               — "active in the network"
//	§6.1.3.3.4 PDU SESSION INACTIVE PENDING     — release initiated, awaiting UE response
//	§6.1.3.3.5 PDU SESSION MODIFICATION PENDING — modification initiated, awaiting UE
//
// Bit=1 ⇔ the session is in any state OTHER than "PDU SESSION INACTIVE".
// The internal session.State enum maps as follows:
//
//	StateInactive  → §6.1.3.3.2 PDU SESSION INACTIVE        ⇒ bit=0
//	StateReleased  → §6.1.3.3.2 (terminal: no record)       ⇒ bit=0
//	StatePending   → on the path to PDU SESSION ACTIVE      ⇒ bit=1
//	StateActive    → §6.1.3.3.3 PDU SESSION ACTIVE          ⇒ bit=1
//	StateSuspended → §6.1.3.3.3 (still ACTIVE; UP torn down per §5.6.1.1
//	                  "PDU sessions which are established without user-
//	                  -plane resources" — see line 36562-36564)         ⇒ bit=1
//	StateReleasing → §6.1.3.3.4 PDU SESSION INACTIVE PENDING ⇒ bit=1
//
// PSI 0 is spare per §9.11.3.44 and is never set. Returns nil when no
// PSI is set — an empty IE is legal (length 2, all zero) but nil keeps
// the on-wire encoding minimal.
func buildAMFPDUSessionStatus(imsi string) *nas.PDUSessionStatus {
	sessions := session.Default.ForUE(imsi)
	if len(sessions) == 0 {
		return nil
	}
	psis := make([]uint8, 0, len(sessions))
	for _, s := range sessions {
		if s.PDUSessionID < 1 || s.PDUSessionID > 15 {
			continue
		}
		switch s.State {
		case session.StateInactive, session.StateReleased:
			// §6.1.3.3.2 PDU SESSION INACTIVE — bit=0.
			continue
		}
		// All other states (Pending / Active / Suspended / Releasing)
		// are "not in 5GSM state PDU SESSION INACTIVE" per §6.1.3.3.
		psis = append(psis, s.PDUSessionID)
	}
	if len(psis) == 0 {
		return nil
	}
	return &nas.PDUSessionStatus{PSIs: psis}
}

// inactivePSIsFromStatus shared with registration.go — input is the
// typed PSIBitmap.PSIs slice (PSIs whose bit is set), output is the
// complement in the 1..15 range (inactive PSIs). PSI 0 spare per
// TS 24.501 §9.11.3.44.

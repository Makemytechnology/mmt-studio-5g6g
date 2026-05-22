// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GMM Deregistration.
//
// Spec: TS 24.501 §5.5.2 "De-registration procedure" (PDF:
// specs/3gpp/ts_124501v190602p.pdf).
//
//	§5.5.2.1 General — lists the three UE-side triggers
//	  (switch-off, eCall inactivity, USIM removal) and the network-
//	  side triggers (AMF informs UE to re-register, UDM subscription
//	  withdrawn, operator policy).
//	§5.5.2.2 UE-initiated de-registration procedure:
//	  §5.5.2.2.1 UE → AMF: DEREGISTRATION REQUEST (MO).
//	  §5.5.2.2.2 "When the DEREGISTRATION REQUEST message is received
//	             by the AMF, the AMF shall send a DEREGISTRATION
//	             ACCEPT message to the UE, if the De-registration type
//	             IE does not indicate 'switch off'. Otherwise, the
//	             procedure is completed when the AMF receives the
//	             DEREGISTRATION REQUEST message."
//	  §5.5.2.2.3 AMF triggers SMF to release PDU sessions for 3GPP
//	             access; UE enters 5GMM-DEREGISTERED for 3GPP access.
//	§5.5.2.3 Network-initiated de-registration procedure:
//	  §5.5.2.3.1 AMF → UE: DEREGISTRATION REQUEST (MT) + T3522 guard.
//	  §5.5.2.3.2 UE replies with DEREGISTRATION ACCEPT; AMF stops
//	             T3522 and completes the procedure.
//
// Security-context lifecycle on RM-DEREGISTERED — TS 33.501
// §6.8.1.1.1 (PDF: specs/3gpp/ts_133501v190600p.pdf). See the
// verbatim excerpt in nf/amf/ngap/uectxrelease/uectxrelease.go file
// header for the full quote; the operative rule here is that both
// case 2.a.ii (UE-initiated non-switch-off) and case 2.b.ii
// (AMF-initiated implicit) keep the remaining security parameters
// — meaning handleDeregistrationRequestMO / handleDeregistrationAcceptFromUE
// below call uectx.Default.ClearVolatile(ue) rather than Remove(ue)
// so the cached 5G NAS security context survives for §4.4 reuse on
// the next registration.
//
// Port of nf/amf/gmm/gmm_deregistration.py.
package gmm

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/uectxrelease"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/nf/smf/session/pti"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	nas "github.com/mmt/nasgen/generated"
	runtime "github.com/mmt/nasgen/pkg/runtime"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
)

func init() {
	Register(MsgDeregistrationRequestMO, handleDeregistrationRequestMO)
	Register(MsgDeregistrationAcceptMT, handleDeregistrationAcceptFromUE)
}

// handleDeregistrationRequestMO implements the AMF side of TS 24.501
// §5.5.2.2 "UE-initiated de-registration procedure".
//
// §5.5.2.2.2 (verbatim): "When the DEREGISTRATION REQUEST message is
// received by the AMF, the AMF shall send a DEREGISTRATION ACCEPT
// message to the UE, if the De-registration type IE does not indicate
// 'switch off'. Otherwise, the procedure is completed when the AMF
// receives the DEREGISTRATION REQUEST message."
//
// §5.5.2.2.3 (verbatim, 3GPP access path): "the AMF shall trigger the
// SMF to perform a local release of the PDU session(s) established
// over 3GPP access, if any, for this UE. … The UE is marked as
// inactive in the AMF for 5GS services for 3GPP access. The AMF shall
// enter the state 5GMM-DEREGISTERED for 3GPP access."
//
// Security-context retention — TS 33.501 §6.8.1.1.1 case 2.a (both
// switch-off and non-switch-off variants): the current native 5G NAS
// security context stays on the AMF so §4.4 reuse works on the next
// registration. We call uectx.Default.ClearVolatile, NOT Remove.
func handleDeregistrationRequestMO(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.dereg")
	pm.Inc(pm.DeregAtt, 1)

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("DeregistrationRequest decode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	dr, ok := msg.(*nas.DeregistrationRequestUEOriginating)
	if !ok {
		log.Errorf("DeregistrationRequest: unexpected type %T", msg)
		return
	}

	// TS 24.501 v19.6.2 §5.5.2.2.2 (UE-initiated de-registration —
	// procedure initiation): "the UE shall send a DEREGISTRATION
	// REQUEST message with the 5G-GUTI of the UE included in the
	// 5GS mobile identity IE."
	//
	// TS 38.413 v19.2.0 §9.2.5.1 INITIAL UE MESSAGE IE list: the
	// NGAP-level 5G-S-TMSI IE is OPTIONAL ("Included if the UE has a
	// valid 5G-S-TMSI"). Commercial UEs in particular may omit the
	// NGAP-level IE on switch-off because the UE is going offline —
	// the 5G-GUTI inside the NAS payload is the authoritative
	// identity.
	//
	// Without reconciliation here: initialue.go's TMSI lookup fails
	// (no NGAP-level IE), a fresh AmfUeCtx is allocated, and this
	// handler runs on that fresh ctx — which has no IMSI, no
	// PDU sessions, no UDM UECM registration, no cached security
	// state. The real UE ctx (allocated during the earlier
	// registration) keeps its RM=REGISTERED plus any active/suspended
	// PDU sessions and UDM state, becoming an orphan.
	//
	// Reconciliation path below: when the DR's 5G-GUTI points to an
	// existing UE ctx, migrate the fresh ctx's current NGAP leg
	// (gNB + RAN-UE-NGAP-ID) onto the existing ctx and run the
	// dereg operations there. The fresh ctx is dropped after the
	// handoff.
	if ue.IMSI == "" {
		if g, ok := dr.MobileIdentity5GS.(*runtime.GUTI5G); ok && g.TMSI5G != 0 {
			if existing := uectx.Default.LookupByTMSI(g.TMSI5G); existing != nil && existing != ue {
				log.WithIMSI(existing.IMSI).Infof("MO Dereg on fresh ctx amfUeID=%d — 5G-GUTI TMSI=0x%08X resolves to existing amfUeID=%d (RM=%s); retargeting handler",
					ue.AmfUeNGAPID, g.TMSI5G, existing.AmfUeNGAPID, existing.RM)
				// Retarget the existing ctx to this handler's
				// current NGAP leg (the radio the UE is using to
				// deliver the DR). Later uectxrelease.SendCommand
				// will use these to route UEContextReleaseCommand
				// over the right SCTP stream.
				existing.RanUeNGAPID = ue.RanUeNGAPID
				existing.GnbKey = ue.GnbKey
				// The fresh ctx is never going to be used again.
				// Remove() fires the hook cascade (timers cancel,
				// FSM drop) for completeness even though the fresh
				// ctx didn't run any GMM procedures.
				freshID := ue.AmfUeNGAPID
				uectx.Default.Remove(ue)
				log.WithIMSI(existing.IMSI).Infof("fresh ctx amfUeID=%d released after retarget to amfUeID=%d",
					freshID, existing.AmfUeNGAPID)
				ue = existing
			}
		}
	}

	log.WithIMSI(ue.IMSI).Infof("UE deregistration amfUeID=%d type=0x%02X",
		ue.AmfUeNGAPID, dr.DeregistrationType.Encode())

	ue.GMMProc = uectx.GMMProcDeregistration
	ue.RM = uectx.RMDeregistered
	ue.CM = uectx.CMIdle

	// ── Release all PDU sessions (mirrors Python _release_all_pdu_sessions) ──
	// UPF delete + IP release for each session.
	releaseAllPDUSessions(ue, log)

	// Release every outstanding 5GSM PTI for this UE so the next
	// registration starts with a clean PTI space.
	if n := pti.Default.ReleaseAllForUE(ue.IMSI); n > 0 {
		log.WithIMSI(ue.IMSI).Infof("Released %d stale 5GSM PTI entries", n)
	}

	// Cancel any stale per-UE timers (mirrors Python _cleanup_ue_context).
	timers.M.CancelAllForUE(fmt.Sprintf("%d", ue.AmfUeNGAPID))

	// ── Parse switch-off bit ──
	// TS 24.501 §5.5.2.2.2: If NOT switch-off, send Deregistration Accept.
	switchOff := dr.DeregistrationType.Encode()&0x08 != 0

	if !switchOff {
		accept := &nas.DeregistrationAcceptUEOriginating{}
		encoded, err := accept.Encode()
		if err != nil {
			log.Errorf("DeregistrationAccept encode: %v", err)
			return
		}
		if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
			// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
			_ = dlnas.Send(gnb, ue, encoded)
		}
	} else {
		log.WithIMSI(ue.IMSI).Info("Switch-off deregistration — skipping Deregistration Accept")
	}

	// ── Send NGAP UE Context Release Command to gNB (mirrors Python) ──
	if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
		cause := genngap.CauseNasDeregister
		if err := uectxrelease.SendCommand(gnb, ue, uectxrelease.CauseNAS(cause)); err != nil {
			log.Warnf("UEContextReleaseCommand: %v", err)
		}
	}

	pm.Inc(pm.DeregSucc, 1)
	ue.GMMProc = uectx.GMMProcNone

	// Fire FSM: REGISTERED → DEREGISTRATION_INITIATED so the transition
	// is visible in logs. The ctx Remove() below cancels the Twait timer
	// via the remove-hook cascade (nf/amf/hooks.go) and drops the FSM
	// entry, so the intermediate state is short-lived — the subsequent
	// UEContextReleaseCommand path already handled the NGAP side.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvDeregistrationRequestMO, Inner: inner})

	// Retain the current native 5G NAS security context per TS 33.501
	// §6.8.1.1.1 case 2.a (both switch-off i. and non-switch-off ii.
	// permit keeping it — the non-switch-off case actually mandates
	// keeping ALL remaining security parameters). ClearVolatile fires
	// the same remove-hooks as Remove() — cancels per-UE timers,
	// drops GMM + NGAP FSM entries, releases PTI — but leaves the ctx
	// indexed by AmfUeNGAPID / IMSI / (internally) TMSI so the next
	// Registration Request (airplane-mode OFF) can find it via the
	// 5G-GUTI → LookupByTMSI path and take the cached context into
	// use without running primary authentication.
	// Nudm_UECM_Deregistration (TS 29.503 §5.3.2.4) — UE has
	// deregistered, tell UDM we no longer serve this UE.
	if ue.IMSI != "" {
		udm.DeregisterAMF(ue.IMSI)
	}
	uectx.Default.ClearVolatile(ue)
	log.WithIMSI(ue.IMSI).Infof("UE deregistered (UE-initiated) amfUeID=%d — security context retained (TS 33.501 §6.8.1.1.1 case 2.a)",
		ue.AmfUeNGAPID)
}

// handleDeregistrationAcceptFromUE processes DEREGISTRATION ACCEPT
// received from the UE in reply to our MT-initiated Deregistration
// Request. Implements TS 24.501 §5.5.2.3.2 "Network-initiated
// de-registration procedure completion by the UE":
//   - Stop T3522 (the retransmit guard armed in SendNetworkDeregistration
//     per §5.5.2.3.1).
//   - Release all PDU sessions for the subscriber (symmetric with the
//     MO path §5.5.2.2.3 — the UE is leaving the network so UPF rules
//     must go).
//   - Send NGAP UE Context Release Command to the serving gNB with
//     cause=deregister (CauseNas enum, TS 38.413 §9.3.1.2).
//   - Retain the 5G NAS security context — TS 33.501 §6.8.1.1.1
//     case 2.b (AMF-initiated, covers both "explicit with re-
//     registration required" and "implicit"): keep all remaining
//     security parameters. ClearVolatile, NOT Remove.
func handleDeregistrationAcceptFromUE(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.dereg")
	// T3522 is still handler-owned: the MT-dereg path (AMF-initiated dereg
	// with retransmits) is not yet modelled in the FSM graph, so the
	// cancel must stay here until EvDeregistrationAcceptMT + the
	// corresponding transitions land.
	// TODO(spec: TS 24.501 §5.5.2.3) — move T3522 lifecycle into the
	//   GMM FSM TimerSpec graph so the arm/cancel mirrors the other
	//   NAS-leg timers (T3560, T3550, T3570).
	timers.M.Cancel("T3522", fmt.Sprintf("%d", ue.AmfUeNGAPID))
	ue.RM = uectx.RMDeregistered
	ue.CM = uectx.CMIdle
	ue.GMMProc = uectx.GMMProcNone
	log.WithIMSI(ue.IMSI).Infof("UE accepted NW-initiated deregistration amfUeID=%d",
		ue.AmfUeNGAPID)

	// Release PDU sessions + 5GSM PTI state symmetric with the MO path.
	// Without this the UPF rules + SMF session contexts linger until GC.
	releaseAllPDUSessions(ue, log)
	if n := pti.Default.ReleaseAllForUE(ue.IMSI); n > 0 {
		log.WithIMSI(ue.IMSI).Infof("Released %d stale 5GSM PTI entries", n)
	}

	// ── Send NGAP UE Context Release Command to gNB (mirrors Python) ──
	if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
		cause := genngap.CauseNasDeregister
		// TODO(arch: event: UE-Context-Release to NGAP — see gmm/doc.go)
		if err := uectxrelease.SendCommand(gnb, ue, uectxrelease.CauseNAS(cause)); err != nil {
			log.Warnf("UEContextReleaseCommand: %v", err)
		}
	}

	// Fire FSM: MT_DEREG_PENDING → DEREGISTERED so the transition is
	// logged.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvDeregistrationAcceptMT, Inner: inner})

	// Nudm_UECM_Deregistration (TS 29.503 §5.3.2.4) — network-initiated
	// dereg confirmed by UE. Tell UDM we no longer serve this UE.
	if ue.IMSI != "" {
		udm.DeregisterAMF(ue.IMSI)
	}

	// Retain the 5G NAS security context per TS 33.501 §6.8.1.1.1
	// case 2.b (AMF-initiated dereg keeps all remaining security
	// parameters — the spec explicitly calls out both "re-
	// registration required" and "implicit" variants). ClearVolatile
	// fires the same hooks as Remove (timers cancelled, FSMs dropped,
	// PTI released) but leaves the ctx in the registry so the next
	// registration can reuse the cached NAS security context per
	// §4.4.
	uectx.Default.ClearVolatile(ue)
	pm.Inc(pm.DeregSucc, 1)
}

// SendNetworkDeregistration builds + ships a DEREGISTRATION REQUEST
// (network-initiated — TS 24.501 §5.5.2.3) to the UE. The FSM arms
// T3522 on the EvDeregistrationRequestSentMT event fired here; the
// timer manager retransmits the cached PDU up to NASMaxRetransmit
// times per TS 24.501 §10.2 Table 10.2.1 before declaring failure.
//
// DeregistrationType (TS 24.501 §9.11.3.20):
//
//	bit 0..2 = Access type (001 = 3GPP access)
//	bit 3    = Switch-off (0 for MT — we need the UE to ACK)
//	bit 4    = Re-registration required
//
// rereg=true pushes the UE back into the Registration procedure after
// Accept (operator workflow: force re-auth after subscription change).
// cause is optional; leave 0 for "unspecified".
func SendNetworkDeregistration(ue *uectx.AmfUeCtx, rereg bool, cause uint8) error {
	log := logger.Get("amf.gmm.dereg")

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return fmt.Errorf("SendNetworkDeregistration amfUeID=%d: gNB %q gone",
			ue.AmfUeNGAPID, ue.GnbKey)
	}

	// Network-init dereg: switch-off bit always 0, access type 3GPP.
	dtype := byte(0x01) // bit 0..2 = 001 (3GPP)
	if rereg {
		dtype |= 0x10 // bit 4
	}

	req := &nas.DeregistrationRequestUETerminated{
		DeregistrationType: nas.FiveGMMCause{Value: dtype},
	}
	if cause != 0 {
		c := nas.FiveGMMCause{Value: cause}
		req.Cause5GMM = &c
	}
	encoded, err := req.Encode()
	if err != nil {
		return fmt.Errorf("MT DeregistrationRequest encode amfUeID=%d: %w",
			ue.AmfUeNGAPID, err)
	}
	// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
	if err := dlnas.Send(gnb, ue, encoded); err != nil {
		return fmt.Errorf("DL MT DeregistrationRequest amfUeID=%d: %w",
			ue.AmfUeNGAPID, err)
	}
	// Cache for T3522 retransmit (TS 24.501 §10.2 N3522=4).
	ue.RetxNASPDU = encoded
	pm.Inc(pm.DeregAtt, 1)
	log.WithIMSI(ue.IMSI).Infof("MT Deregistration Request sent amfUeID=%d rereg=%v cause=%d",
		ue.AmfUeNGAPID, rereg, cause)
	return nil
}

// releaseAllPDUSessions releases all PDU sessions for a UE — IP return,
// UPF session delete, AMF context cleanup. Mirrors Python _release_all_pdu_sessions.
func releaseAllPDUSessions(ue *uectx.AmfUeCtx, log *logger.Logger) {
	if len(ue.PDUSessions) == 0 {
		return
	}
	count := 0
	for id := range ue.PDUSessions {
		// TODO(arch: sbi-N11: Nsmf_PDUSession_ReleaseSMContext — TS 29.502 §5.2.2.4) —
		//   DELETE on /sm-contexts/{smContextRef}. Today we call
		//   session.Release in-process because SMF lives alongside AMF;
		//   when SMF splits out this must become an SBI call.
		session.Release(ue.IMSI, uint8(id))
		delete(ue.PDUSessions, id)
		count++
	}
	// Also release any sessions in the SMF store that may not be tracked
	// in the AMF-side PDUSessions map.
	// TODO(arch: sbi-N11) — same as above for bulk release. A proper SBI
	//   design would query SMF for all sessions of a SUPI then release
	//   each; the "release all" semantic is AMF-local convenience only.
	smfCount := session.ReleaseAll(ue.IMSI)
	if smfCount > count {
		count = smfCount
	}
	log.WithIMSI(ue.IMSI).Infof("Released %d PDU sessions", count)
}

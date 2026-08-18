// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package uectxrelease — UE Context Release NGAP procedures.
//
// PDFs:
//   - specs/3gpp/ts_138413v190200p.pdf
//     §8.3.2  UE Context Release Request (NG-RAN node initiated)
//     §8.3.3  UE Context Release (AMF initiated)
//     §9.2.2.4/5/6  UE CONTEXT RELEASE REQUEST / COMMAND / COMPLETE
//     message IE tables
//   - specs/3gpp/ts_133501v190600p.pdf
//     §6.8.1.1.1  Transition from RM-REGISTERED to RM-DEREGISTERED
//     §6.8.1.2.4  Transition from CM-CONNECTED to CM-IDLE
//
// Two initiators (TS 38.413):
//   - AMF-initiated (§8.3.3): AMF sends UE CONTEXT RELEASE COMMAND
//     (InitiatingMessage of procedureCode 41); the gNB replies with
//     UE CONTEXT RELEASE COMPLETE (SuccessfulOutcome of 41).
//   - gNB-initiated (§8.3.2): gNB sends UE CONTEXT RELEASE REQUEST
//     (procedureCode 42); the AMF then issues the COMMAND above.
//
// Security-context lifecycle — the question the handleComplete branch
// below pivots on:
//
//	TS 33.501 §6.8.1.2.4 (verbatim):
//	  "In particular, on CM-CONNECTED to CM-IDLE transitions:
//	   - The gNB/ng-eNB and the UE shall release all radio bearers
//	     and delete the AS security context.
//	   - AMF and the UE shall keep the 5G NAS security context
//	     stored."
//
//	So a plain UE Context Release (UE stays RM-REGISTERED, just goes
//	CM-IDLE) MUST NOT wipe the AMF's 5G NAS security context — only
//	AS keys die, and those live at the gNB not the AMF.
//
//	TS 33.501 §6.8.1.1.1 (excerpted, governs what to do when the UE
//	actually transitions to RM-DEREGISTERED):
//	  "2. Deregistration:
//	     a. UE-initiated
//	        i.  If the reason is switch off then all the remaining
//	            security parameters shall be removed from the UE and
//	            AMF with the exception of the current native 5G NAS
//	            security context … which should remain stored in the
//	            AMF and UE.
//	        ii. If the reason is not switch off then AMF and UE shall
//	            keep all the remaining security parameters.
//	     b. AMF-initiated
//	        i.  Explicit: all the remaining security parameters shall
//	            be kept in the UE and AMF if the de-registration type
//	            is 're-registration required'.
//	        ii. Implicit: all the remaining security parameters shall
//	            be kept in the UE and AMF.
//	     c. UDM/ARPF-initiated: If the message is 'subscription
//	        withdrawn' then all the remaining security parameters
//	        shall be removed from the UE and AMF.
//	   3. Registration reject: All remaining security parameters shall
//	      be removed from the UE and AMF."
//
//	Net: only **registration-reject** and **UDM subscription-withdrawn**
//	justify fully removing the UE ctx's security state. Every other
//	RM-DEREGISTERED path should retain the current native 5G NAS
//	security context so §4.4 reuse works on the next registration.
package uectxrelease

import (
	"fmt"
	"strings"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/errind"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// OnNASLowerLayerFailure is a hook the gmm package registers at
// init() to perform the TS 24.501 §5.4.1.3.6(a) abort cleanup in
// its own package — drop the GMM FSM, cancel the 5GMM timers
// (T3550/T3560/T3570), clear sub-state. nil is a no-op (pre-gmm
// init tests, etc.). Hook pattern prevents a uectxrelease ↔ gmm
// import cycle.
var OnNASLowerLayerFailure func(ue *uectx.AmfUeCtx)

// Cause helpers — caller picks the category then the value.
func CauseNAS(v genngap.CauseNas) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentNas, Nas: &v}
}

func CauseRadioNetwork(v genngap.CauseRadioNetwork) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentRadioNetwork, RadioNetwork: &v}
}

func init() {
	// Wire the callback the NGAP FSM uses on Twait-ICS expiry (TS
	// 38.413 §8.3.1.3). The hook lives in the root ngap package; we
	// can assign it here because uectxrelease already depends on
	// ngap, so there's no cycle.
	ngap.SendCtxReleaseCmdHook = func(gnbIP string, amfUeID int64, cause uint8) error {
		gnb := gnbctx.Default.GetByIP(gnbIP)
		if gnb == nil {
			return fmt.Errorf("gNB %q gone", gnbIP)
		}
		ue := uectx.Default.LookupByAmfID(amfUeID)
		if ue == nil {
			return fmt.Errorf("AMF-UE-NGAP-ID=%d gone", amfUeID)
		}
		return SendCommand(gnb, ue, CauseRadioNetwork(genngap.CauseRadioNetwork(cause)))
	}
}

// SendCommand implements the AMF side of the UE Context Release
// procedure (TS 38.413 §8.3.3 "UE Context Release (AMF initiated)").
//
// §8.3.3.1 General: "The purpose of the UE Context Release procedure
// is to enable the AMF to order the release of the UE-associated
// logical NG-connection…"
//
// §8.3.3.2 Successful Operation: "The AMF initiates the procedure by
// sending the UE CONTEXT RELEASE COMMAND message to the NG-RAN node.
// The UE CONTEXT RELEASE COMMAND message shall contain both the AMF
// UE NGAP ID IE and the RAN UE NGAP ID IE if available, otherwise the
// message shall contain the AMF UE NGAP ID IE."
//
// We always have both IDs at this call-site (we're the AMF initiator
// after successful registration), so the message carries the
// UE NGAP IDs CHOICE with the UE-NGAP-ID-pair alternative per the
// §9.2.2.5 UE CONTEXT RELEASE COMMAND IE table.
//
// The gNB's UE CONTEXT RELEASE COMPLETE reply (SuccessfulOutcome of
// procedureCode 41) is processed by handleComplete below — it can
// carry PDU Session Resource List + User Location Information +
// Information on Recommended Cells and RAN Nodes for Paging + Paging
// Assistance Data for CE Capable UE per §8.3.3.2, all of which we
// currently ignore (§9.2.2.6 IEs — see TODOs in handleComplete).
func SendCommand(gnb *gnbctx.GnbCtx, ue *uectx.AmfUeCtx, cause *genngap.Cause) error {
	log := logger.Get("amf.ngap.uectxrelease")
	if gnb == nil || ue == nil {
		return fmt.Errorf("uectxrelease: nil gnb or ue")
	}
	if cause == nil {
		n := genngap.CauseNasNormalRelease
		cause = CauseNAS(n)
	}

	amfID := genngap.AMFUENGAPID(ue.AmfUeNGAPID)
	ranID := genngap.RANUENGAPID(ue.RanUeNGAPID)
	ueIDs := genngap.UENGAPIDs{
		Present: genngap.UENGAPIDsPresentUENGAPIDPair,
		UENGAPIDPair: &genngap.UENGAPIDPair{
			AMFUENGAPID: amfID,
			RANUENGAPID: ranID,
		},
	}

	msg := &genngap.UEContextReleaseCommand{
		ProtocolIEs: []genngap.UEContextReleaseCommandIEsEntry{
			{
				Id:          genngap.ProtocolIEID(genngap.IdUENGAPIDs),
				Criticality: genngap.CriticalityReject,
				Value: genngap.UEContextReleaseCommandIEsValue{
					Present:   genngap.UEContextReleaseCommandIEsValuePresentUENGAPIDs,
					UENGAPIDs: &ueIDs,
				},
			},
			{
				Id:          genngap.ProtocolIEID(genngap.IdCause),
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.UEContextReleaseCommandIEsValue{
					Present: genngap.UEContextReleaseCommandIEsValuePresentCause,
					Cause:   cause,
				},
			},
		},
	}
	inner, err := msg.MarshalAPER()
	if err != nil {
		return err
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ngap.ProcCodeUEContextRelease,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		return err
	}
	stream := gnb.UEStream(ue.AmfUeNGAPID)
	if err := gnb.Send(pdu, stream); err != nil {
		return err
	}
	ue.NGAPProc = uectx.NGAPProcUEContextRelease

	// NGAP per-UE FSM: arm Twait-ue-ctx-release.
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(fk).Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvUECtxReleaseCommand})

	log.WithIMSI(ue.IMSI).Infof("UEContextReleaseCommand sent amfUeID=%d", ue.AmfUeNGAPID)
	return nil
}

// handleRequest implements the AMF side of the gNB-initiated release
// procedure (TS 38.413 §8.3.2 "UE Context Release Request (NG-RAN
// node initiated)", procedureCode 42).
//
// §8.3.2.1 General (verbatim): "The purpose of the UE Context
// Release Request procedure is to enable the NG-RAN node to request
// the AMF to release the UE-associated logical NG-connection due to
// NG-RAN node generated reasons. The procedure uses UE-associated
// signalling."
//
// §8.3.2.2 Successful Operation (verbatim excerpts):
//
//	"The UE CONTEXT RELEASE REQUEST message shall indicate the
//	 appropriate cause value, e.g., 'TXnRELOCOverall Expiry',
//	 'Redirection', for the requested UE-associated logical NG-
//	 connection release."
//
//	"If the PDU Session Resource List IE is included in the UE
//	 CONTEXT RELEASE REQUEST message, the AMF shall handle this
//	 information as specified in TS 23.502 [10]."
//
//	"If the GW Context Release Indication IE is included in the UE
//	 CONTEXT RELEASE REQUEST message, the AMF shall, if supported,
//	 consider that the UE context may be released as specified in
//	 TS 38.300 [8]."
//
//	Interactions with UE Context Release procedure:
//	"The UE Context Release procedure should be initiated upon
//	 reception of a UE CONTEXT RELEASE REQUEST message with the
//	 Cause IE set to a value different than 'User inactivity'. The
//	 UE Context Release procedure should be initiated upon reception
//	 of a UE CONTEXT RELEASE REQUEST message with the Cause IE set
//	 to 'User inactivity' and there is no downlink signaling, as
//	 specified in TS 23.502 [10]."
//
// §8.3.2.3 Abnormal Conditions: "Void."
//
// IE handling status in this implementation:
//
//   - Cause: extracted + logged, passed through to SendCommand for
//     the §8.3.3.2 Command.
//   - PDUSessionResourceListCxtRelReq: logged (IDs); spec-strict
//     per-session deactivation is TODO — we blanket-deactivate via
//     suspendPDUSessions() which is safe (DeactivateUserPlane is
//     idempotent) but imprecise.
//   - GWContextReleaseIndication: logged; W-AGF / TNGF / TS 38.300
//     full handling is a separate tranche.
//   - User-inactivity cause: honoured unconditionally — pending-DL-
//     signalling-aware gating is TODO.
func handleRequest(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.uectxrelease")
	var req genngap.UEContextReleaseRequest
	if err := req.UnmarshalAPER(env.Value); err != nil {
		// Per TS 38.413 v19.2.0 §8.7.5.1, an inbound message that
		// can't be decoded → Error Indication. Cause: transferSyntax
		// since APER itself failed.
		log.Errorf("UEContextReleaseRequest decode from %s: %v", gnb.GnbIP, err)
		_ = errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolTransferSyntaxError))
		return
	}
	ies := extractReq(&req)
	ue := locateUE(gnb, ies.AMFUENGAPID, ies.RANUENGAPID)
	if ue == nil {
		// §8.7.5.2: "If one or both of the AMF UE NGAP ID IE and the
		// RAN UE NGAP ID IE are not correct, the cause shall be set
		// to an appropriate value, e.g., 'Unknown local UE NGAP ID'."
		log.Warnf("UEContextReleaseRequest for unknown UE amfUeID=%d ranUeID=%d cause=%s",
			ies.AMFUENGAPID, ies.RANUENGAPID, formatCause(ies.Cause))
		_ = errind.Send(gnb, ies.AMFUENGAPID, ies.RANUENGAPID,
			errind.CauseRadio(genngap.CauseRadioNetworkUnknownLocalUENGAPID))
		return
	}

	// §8.3.2.2 "shall indicate the appropriate cause value" — log it.
	// Per-session list (§9.2.2.4 IE 133) may be present; log the
	// affected IDs for ops visibility.
	log.WithIMSI(ue.IMSI).Infof("gNB-initiated release amfUeID=%d cause=%s activePDU=%s gwIndication=%v",
		ue.AmfUeNGAPID, formatCause(ies.Cause),
		formatPDUListReq(ies.PDUSessionResourceList),
		ies.GWContextReleaseIndication)

	// TS 38.413 §8.3.2.2 "Interactions" (verbatim): "…should be
	// initiated upon reception of a UE CONTEXT RELEASE REQUEST
	// message with the Cause IE set to 'User inactivity' and there
	// is no downlink signaling, as specified in TS 23.502 [10]."
	//
	// TS 23.502 §4.2.6 step 1 (verbatim): "If N2 Context Release
	// Request cause indicates the release is requested due to user
	// inactivity or AS RAI then the AMF continues with the AN
	// Release procedure unless the AMF is aware of pending MT
	// traffic or signalling."
	//
	// "Pending signalling" concretely: the AMF has armed a DL NAS
	// retransmit (RetxNASPDU is set immediately before the guard
	// timer fires — T3522 for MT-dereg, T3550 for Reg Accept, T3555
	// for Config Update, T3560 for Auth Request, T3570 for Identity
	// Request). When any of those is in flight, honouring the
	// release would drop the NAS signalling leg that's waiting on a
	// UE reply. Skip the release and keep the NG connection open.
	if isUserInactivityCause(ies.Cause) && len(ue.RetxNASPDU) > 0 {
		log.WithIMSI(ue.IMSI).Infof("cause=UserInactivity with pending DL signalling (retxNAS=%dB) — skipping release per §8.3.2.2 + TS 23.502 §4.2.6 step 1",
			len(ue.RetxNASPDU))
		return
	}

	// §8.3.2.2: "If the GW Context Release Indication IE is included
	// … the AMF shall, if supported, consider that the UE context may
	// be released as specified in TS 38.300." W-AGF / trusted-non-3GPP
	// deployments only. Log and proceed with the standard release.
	if ies.GWContextReleaseIndication {
		log.WithIMSI(ue.IMSI).Infof("GW Context Release Indication set — W-AGF/TNGF path (TS 38.300) not wired; proceeding with standard release")
	}

	// §8.3.2.2: "If the PDU Session Resource List IE is included …
	// the AMF shall handle this information as specified in TS 23.502
	// [10]." TS 23.502 §4.2.6 step 1 defines the list as "PDU Session
	// IDs with active N3 user plane". Stash it on the UE ctx so
	// suspendPDUSessions (running from handleComplete below — §4.2.6
	// step 5 is anchored to the Release Complete) can fall back to
	// this step-1b list when the Release Complete's step-4 list is
	// absent. Strict per-spec handling replaces the prior
	// blanket-deactivate-everything fallback.
	if ies.PDUSessionResourceList != nil && len(*ies.PDUSessionResourceList) > 0 {
		ids := make([]uint8, 0, len(*ies.PDUSessionResourceList))
		for _, item := range *ies.PDUSessionResourceList {
			ids = append(ids, uint8(item.PDUSessionID))
		}
		ue.PendingReleasePDUList = ids
		log.WithIMSI(ue.IMSI).Debugf("§4.2.6 step 1: stashed active-N3 PDU list (%d sessions) for step-5 fallback",
			len(ids))
	}

	// NGAP per-UE FSM: EvUECtxReleaseRequest (gNB → AMF) transitions to
	// CTX_RELEASE_PENDING and arms Twait-ue-ctx-release. The subsequent
	// SendCommand call fires EvUECtxReleaseCommand which the FSM treats
	// as a no-op here (already in CTX_RELEASE_PENDING).
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(fk).Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvUECtxReleaseRequest})

	// NB: PDU session user-plane deactivation (TS 23.502 §4.2.6 step
	// 5-6a) is NOT done here — the spec call-flow places it AFTER the
	// Release Complete arrives (step 5 is anchored to the Complete's
	// PDU Session Resource List IE). See suspendPDUSessions() in
	// handleComplete() below. An earlier implementation deactivated
	// here; that was a timing bug against §4.2.6.

	// ── Cancel all pending AMF timers (UE going idle → retransmissions pointless) ──
	timers.M.CancelAllForUE(fmt.Sprintf("%d", ue.AmfUeNGAPID))

	// ── Transition to CM-IDLE (UE stays REGISTERED for paging) ──
	ue.CM = uectx.CMIdle
	ue.GnbContextEstablished = false
	ue.NGAPProc = uectx.NGAPProcNone

	// The gNB-initiated UEContextReleaseRequest arriving here IS a
	// lower-layer-failure indication for the AMF. Any ongoing 5GMM
	// procedure must be aborted now — otherwise the AMF stays pinned
	// at StateAuthentication / StateIdentification / StateSecurityMode
	// / StateRegisteredInitiated and every subsequent RegistrationRequest
	// or ServiceRequest is rejected with "REGISTRATION in progress"
	// for the full retransmission window.
	//
	// TS 24.501 v19.6.2 carries the "abort on lower-layer failure"
	// rule per-procedure. The applicable §-clause depends on which
	// 5GMM procedure (and sub-step) is in flight — see
	// lowerLayerFailureClause for the table. All clauses are
	// network-side abnormal cases verbatim:
	//
	//   §5.4.1.3.6(a) — Authentication (verbatim "Upon receipt of a
	//                    lower layer failure indication from the N1
	//                    NAS signalling connection before the
	//                    AUTHENTICATION RESPONSE is received, the
	//                    network shall … abort any ongoing 5GMM
	//                    specific procedure.")
	//   §5.4.3.6      — Identification
	//   §5.4.2.7      — Security Mode Control
	//   §5.5.1.2.8(a) — Initial registration ("If a lower layer
	//                    failure occurs before the REGISTRATION
	//                    COMPLETE message has been received from the
	//                    UE and timer T3550 is running, the AMF shall
	//                    locally abort the registration procedure …")
	//   §5.5.1.3.8(a) — Mobility / periodic registration update
	//   §5.6.1.8      — Service Request
	//   §5.5.2.2.7    — UE-initiated de-registration, network-side
	//   §5.5.2.3.5    — Network-initiated de-registration, network-side
	//   §5.4.4.6      — Configuration Update Command
	//   §5.6.2.2.2    — Paging
	if ue.GMMProc != uectx.GMMProcNone {
		log.WithIMSI(ue.IMSI).Infof("%s: lower-layer failure during %s — aborting ongoing 5GMM procedure",
			lowerLayerFailureClause(ue), ue.GMMProc)
		if OnNASLowerLayerFailure != nil {
			OnNASLowerLayerFailure(ue)
		}
		ue.GMMProc = uectx.GMMProcNone
		ue.GMMSub = uectx.GMMSubNone
	}
	log.WithIMSI(ue.IMSI).Info("UE transitioned to CM-IDLE")

	// ── Respond with UE Context Release Command (§8.3.3.2) ──
	if err := SendCommand(gnb, ue, ies.Cause); err != nil {
		log.Errorf("SendCommand amfUeID=%d: %v", ue.AmfUeNGAPID, err)
	}
}

// handleComplete — SuccessfulOutcome of procedureCode=41. The gNB confirms
// it released radio resources; the AMF drops the UE context entirely.
//
// §8.3.3.2 (verbatim): "Upon reception of the UE CONTEXT RELEASE
// COMMAND message, the NG-RAN node shall release all related
// signalling and user data transport resources and reply with the UE
// CONTEXT RELEASE COMPLETE message."
//
// Optional IEs the gNB may carry (§9.2.2.6) — each has a spec-defined
// handling rule we log today and flag as TODO for full wiring:
//
//   - PDU Session Resource List CxtRelCpl (§8.3.3.2 + TS 23.502 §4.2.6
//     step 4: confirmed released sessions — should reconcile with SMF
//     in case any were not deactivated pre-Command).
//   - User Location Information (§8.3.3.2 + TS 23.502 §4.2.6: for
//     CHF billing / location reporting).
//   - Info on Recommended Cells and RAN Nodes for Paging (§8.3.3.2:
//     "the AMF shall, if supported, store it and may use it for
//     subsequent paging").
//   - Paging Assistance Data for CE Capable UE (§8.3.3.2: same,
//     CE / NB-IoT paging optimisation).
//   - Secondary RAT Usage Information (inside the PDU Session Resource
//     Release Response Transfer IE) — SMF billing.
func handleComplete(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.uectxrelease")
	if env.Type != wire.SuccessfulOutcome {
		log.Debugf("UEContextRelease outcome %s from %s", env.Type, gnb.GnbIP)
		return
	}
	var done genngap.UEContextReleaseComplete
	if err := done.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("UEContextReleaseComplete decode from %s: %v", gnb.GnbIP, err)
		_ = errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolTransferSyntaxError))
		return
	}
	ies := extractComplete(&done)
	ue := locateUE(gnb, ies.AMFUENGAPID, ies.RANUENGAPID)
	if ue == nil {
		log.Warnf("UEContextReleaseComplete for unknown UE amfUeID=%d ranUeID=%d",
			ies.AMFUENGAPID, ies.RANUENGAPID)
		_ = errind.Send(gnb, ies.AMFUENGAPID, ies.RANUENGAPID,
			errind.CauseRadio(genngap.CauseRadioNetworkUnknownLocalUENGAPID))
		return
	}

	// TS 38.413 v19.2.0 §8.4 verbatim:
	//   "A UE-associated logical NG-connection is used to convey
	//    signalling messages between an NG-RAN node and an AMF for
	//    a specific UE. The UE-associated logical NG-connection is
	//    identified by the AMF UE NGAP ID and RAN UE NGAP ID at the
	//    AMF and NG-RAN node respectively."
	//
	// When a UE bounces RRC and a fresh InitialUEMessage arrives on
	// the same AMF UE NGAP ID with a NEW RAN UE NGAP ID before the
	// prior association's UEContextReleaseComplete has returned
	// (TS 38.413 §8.3.3.1: "release of the old UE-associated logical
	// NG-connection when the UE has initiated the establishment of a
	// new UE-associated logical NG-connection"), the §8.4 reuse path
	// (initialue.go) updates ue.RanUeNGAPID to the new value. A
	// late-arriving Complete carrying the OLD RAN UE NGAP ID would
	// — without this check — terminate the new association via the
	// "NGAP ESTABLISHED → RELEASED on UEContextReleaseComplete" FSM
	// transition. Drop it as stale; the new association continues.
	//
	// locateUE above prefers AMF UE NGAP ID, so the stale Complete
	// still lands on the ctx — making the RAN ID compare here the
	// only reliable filter.
	if ies.RANUENGAPID != 0 && ies.RANUENGAPID != ue.RanUeNGAPID {
		log.WithIMSI(ue.IMSI).Infof("stale UEContextReleaseComplete amfUeID=%d ranUeID=%d (current ranUeID=%d) — dropping per TS 38.413 §8.4 (logical NG-connection identified by AMF+RAN UE NGAP ID pair)",
			ue.AmfUeNGAPID, ies.RANUENGAPID, ue.RanUeNGAPID)
		return
	}

	log.WithIMSI(ue.IMSI).Infof("UEContextReleaseComplete amfUeID=%d RM=%s CM=%s releasedPDU=%s",
		ue.AmfUeNGAPID, ue.RM, ue.CM, formatPDUListCpl(ies.PDUSessionResourceList))

	// §8.3.3.2 User Location Information (§9.3.1.16) — marshal and
	// persist. TS 23.502 §4.2.6 step 5 passes UL Info as a parameter
	// of the per-session Deactivate call; suspendPDUSessions below
	// reads ue.LastKnownLocation for each session it deactivates.
	if ies.UserLocationInformation != nil {
		if b, err := ies.UserLocationInformation.MarshalAPER(); err == nil {
			ue.LastKnownLocation = b
			log.WithIMSI(ue.IMSI).Infof("stored last-known location (%dB, TS 38.413 §9.3.1.16)", len(b))
		} else {
			log.WithIMSI(ue.IMSI).Warnf("UserLocationInformation MarshalAPER: %v", err)
		}
	}

	// §8.3.3.2 Info on Recommended Cells and RAN Nodes for Paging
	// (§9.3.1.100) — "the AMF shall, if supported, store it and may
	// use it for subsequent paging." Stored as opaque APER so the
	// paging builder (nf/amf/ngap/paging.go) re-emits it in the
	// §9.3.1.69 Assistance Data for Paging → §9.3.1.70 Assistance
	// Data for Recommended Cells chain on the next PAGING message.
	if ies.InfoOnRecommendedCellsAndRANNodesForPaging != nil {
		if b, err := ies.InfoOnRecommendedCellsAndRANNodesForPaging.MarshalAPER(); err == nil {
			ue.RecommendedCellsForPaging = b
			log.WithIMSI(ue.IMSI).Infof("stored Recommended Cells / RAN Nodes for Paging (%dB, TS 38.413 §8.3.3.2)", len(b))
		} else {
			log.WithIMSI(ue.IMSI).Warnf("Recommended Cells MarshalAPER: %v", err)
		}
	}

	// §8.3.3.2 Paging Assistance Data for CE Capable UE — "the AMF
	// shall, if supported, store it and use it for subsequent paging,
	// as specified in TS 23.502". CE / NB-IoT only.
	// TODO(spec: TS 38.413 §8.3.3.2 "Paging Assistance Data for CE
	//   Capable UE") — store on UE context for CE paging.
	if ies.PagingAssisDataforCEcapabUE != nil {
		log.WithIMSI(ue.IMSI).Debugf("Paging Assistance Data for CE Capable UE present — storage TODO")
	}

	// §8.3.3.2 PDU Session Resource List in Complete — gNB confirms
	// released sessions. TODO: reconcile with session.Default for any
	// sessions the gNB reports released but AMF didn't deactivate
	// pre-Command (defensive — shouldn't happen on a clean flow).
	if ies.PDUSessionResourceList != nil && len(*ies.PDUSessionResourceList) > 0 {
		log.WithIMSI(ue.IMSI).Debugf("§8.3.3.2 Complete lists %d released session(s) — SMF state assumed consistent",
			len(*ies.PDUSessionResourceList))
	}

	ue.NGAPProc = uectx.NGAPProcNone
	ue.GnbContextEstablished = false
	ue.CM = uectx.CMIdle

	// TS 23.502 §4.2.6 step 5 (verbatim, grep-verified in
	// specs/3gpp/ts_123502v190700p.pdf): "For each of the PDU
	// Sessions in the N2 UE Context Release Complete, the AMF
	// invokes Nsmf_PDUSession_UpdateSMContext Request (PDU Session
	// ID, PDU Session Deactivation, Cause, Operation Type, User
	// Location Information, Age of Location Information, N2 SM
	// Information (Secondary RAT usage data)). The Operation Type
	// is set to 'UP deactivate'…"
	//
	// Strict per-session iteration: if §9.2.2.6 PDUSessionResource
	// ListCxtRelCpl is present, deactivate EXACTLY those sessions.
	// If the IE is absent (it's Optional per the IE table), fall
	// back to blanket-deactivation of every PDU session the UE has
	// — safe because by this point the UE is going CM-IDLE and no
	// N3 tunnel survives.
	suspendPDUSessions(ue, log, ies.PDUSessionResourceList, ue.LastKnownLocation)

	// NGAP per-UE FSM: CTX_RELEASE_PENDING → RELEASED. Drop the FSM
	// entry so the association id slot is reusable if the UE reconnects.
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(fk).Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvUECtxReleaseComplete})
	ngapfsm.Drop(fk)

	// Keep-vs-remove decision per TS 33.501 §6.8.1.2.4 +
	// §6.8.1.1.1 (see package header for the verbatim text).
	//
	// RM=REGISTERED (the CM-CONNECTED → CM-IDLE path): §6.8.1.2.4
	// mandates the AMF "keep the 5G NAS security context stored" —
	// the UE ctx + security state stay put. AS keys at the gNB are
	// deleted by the gNB itself as part of releasing radio bearers.
	//
	// RM=DEREGISTERED + HardRemoveOnComplete: §6.8.1.1.1 case 1
	// (registration reject) and case 2.c (UDM "subscription
	// withdrawn") — AMF "shall remove all the 5G NAS security
	// parameters". The triggering call site (gmm/registration_
	// response.go:abortRegistrationAndReleaseN1 today; UDM-withdrawn
	// when implemented) sets HardRemoveOnComplete before SendCommand
	// so the ctx remains locatable across the §8.3.3.2 round-trip
	// (otherwise the gNB's COMPLETE lands on a stale ctx and the AMF
	// emits §10.x Error Indication "unknown-local-UE-NGAP-ID"); we
	// fully Remove here, after §8.3.3.2 IE processing above.
	//
	// RM=DEREGISTERED + !HardRemoveOnComplete: every other dereg
	// trigger (UE-initiated non-switch-off, AMF-initiated explicit
	// with re-reg required, AMF-initiated implicit, UE-initiated
	// switch-off with the current native context retained) — the
	// security context stays resident so §4.4 reuse works on the
	// next registration. ClearVolatile fires the same remove-hooks
	// (timers / FSM drop / PTI release) as Remove() but leaves the
	// ctx indexed so a subsequent RR finds the cached security state.
	switch {
	case ue.RM == uectx.RMDeregistered && ue.HardRemoveOnComplete:
		ue.HardRemoveOnComplete = false
		uectx.Default.Remove(ue)
		log.WithIMSI(ue.IMSI).Infof("UE context fully removed amfUeID=%d (RM=DEREGISTERED, hard-remove) — all 5G NAS security parameters erased (TS 33.501 §6.8.1.1.1 case 1 / 2.c)",
			ue.AmfUeNGAPID)
	case ue.RM == uectx.RMDeregistered:
		uectx.Default.ClearVolatile(ue)
		log.WithIMSI(ue.IMSI).Infof("UE context cleared amfUeID=%d (RM=DEREGISTERED) — 5G NAS security context retained (TS 33.501 §6.8.1.1.1 case 2.a.ii / 2.b)",
			ue.AmfUeNGAPID)
	default:
		log.WithIMSI(ue.IMSI).Info("UE RAN resources released, UE in CM-IDLE/RM-REGISTERED — 5G NAS security context retained (TS 33.501 §6.8.1.2.4)")
	}
}

// suspendPDUSessions deactivates the user-plane for each PDU session
// listed in the §8.3.3.2 UE CONTEXT RELEASE COMPLETE (or, when the
// list IE is absent, every PDU session the UE has). Implements
// TS 23.502 §4.2.6 "AN Release" step 5-6a.
//
// Step 5 (verbatim): "For each of the PDU Sessions in the N2 UE
//
//	Context Release Complete, the AMF invokes
//	Nsmf_PDUSession_UpdateSMContext Request (PDU Session ID, PDU
//	Session Deactivation, Cause, Operation Type, User Location
//	Information, Age of Location Information, N2 SM Information
//	(Secondary RAT usage data)). The Operation Type is set to
//	'UP deactivate'…"
//
// Step 6a (verbatim): "SMF to UPF: N4 Session Modification Request
//
//	(AN or N3 UPF Tunnel Info to be removed, Buffering on/off).
//	[…] the SMF initiates an N4 Session Modification procedure
//	indicating the need to remove Tunnel Info of AN or UPF
//	terminating N3. Buffering on/off indicates whether the UPF
//	shall buffer incoming DL PDU or not."
//
// In-process: session.DeactivateUserPlane drives UPF FAR FORW → BUFF
// (TS 29.244 §7.5.4 / §8.2.26) and passes userLocation through so
// the SMF's §4.2.6 step 5 parameter list is complete. The PDU
// session record stays — only the user-plane is torn down — so the
// later §4.2.3.2 Service Request reactivation (upCnxState=
// ACTIVATING) re-arms the tunnel without reprovisioning SM Policy,
// AMBR, PCC rules, or IP allocation.
//
// list is the §9.2.2.6 IE 60 PDUSessionResourceListCxtRelCpl carried
// in UE CONTEXT RELEASE COMPLETE; nil/empty means the gNB didn't
// enumerate sessions, so deactivate all the UE has. userLocation is
// the §9.3.1.16 UL Info also carried in the Complete (may be nil).
func suspendPDUSessions(ue *uectx.AmfUeCtx, log *logger.Logger,
	list *genngap.PDUSessionResourceListCxtRelCpl, userLocation []byte) {
	if len(ue.PDUSessions) == 0 {
		return
	}

	// Strict per-session path: the Complete enumerated exactly which
	// sessions had active N3 at the gNB. Deactivate those and no
	// others; unlisted PDU sessions were already without N3 from
	// prior signalling (e.g., prior suspend).
	if list != nil && len(*list) > 0 {
		n := 0
		ids := make([]string, 0, len(*list))
		for _, item := range *list {
			id := uint8(item.PDUSessionID)
			ids = append(ids, fmt.Sprintf("%d", id))
			pdu, ok := ue.PDUSessions[int(id)]
			if !ok {
				log.WithIMSI(ue.IMSI).Warnf("§4.2.6 step 5 per-session list names pduSessID=%d unknown to AMF — skipping", id)
				continue
			}
			pdu.State = "SUSPENDED"
			if dl := session.DeactivateUserPlane(ue.IMSI, id, userLocation); dl > 0 {
				n++
			}
		}
		log.WithIMSI(ue.IMSI).Infof("Deactivated %d/%d PDU session user-planes (§4.2.6 step 5 list=[%s]; AN Tunnel Info removed, BUFF mode)",
			n, len(*list), strings.Join(ids, ","))
		ue.PendingReleasePDUList = nil
		return
	}

	// TS 23.502 v19.7.0 §4.2.6 step 5 verbatim:
	//   "For each of the PDU Sessions in the N2 UE Context Release
	//    Complete, the AMF invokes Nsmf_PDUSession_UpdateSMContext
	//    Request…"
	// The spec's iteration is "for each of the PDU Sessions in the
	// N2 UE Context Release Complete". An empty or absent Complete
	// list is a zero-iteration set — DO NOTHING per spec. (An
	// earlier "blanket-deactivate-all" fallback was our invention,
	// not spec-mandated; it misfired when the gNB correctly reported
	// that no sessions had active N3 at release time, e.g. release
	// mid-PDU-Setup before any N3 tunnel existed.)
	//
	// Fallback chain when the Complete list is absent/empty:
	//   1. Use the step-1b Request list stashed on
	//      ue.PendingReleasePDUList (TS 23.502 §4.2.6 step 1 —
	//      "List of PDU Session ID(s) with active N3 user plane").
	//      This captures the gNB's view of active-N3 at an earlier
	//      point in the same release flow; step 1 explicitly
	//      authorizes "steps 5 to 7 are performed before step 2"
	//      when this list is present.
	//   2. If step-1b list is also absent/empty, DO NOTHING — there
	//      are no sessions to deactivate per spec.
	if len(ue.PendingReleasePDUList) > 0 {
		n := 0
		ids := make([]string, 0, len(ue.PendingReleasePDUList))
		for _, id := range ue.PendingReleasePDUList {
			ids = append(ids, fmt.Sprintf("%d", id))
			pdu, ok := ue.PDUSessions[int(id)]
			if !ok {
				log.WithIMSI(ue.IMSI).Warnf("§4.2.6 step 5 fallback: step-1b list names pduSessID=%d unknown to AMF — skipping", id)
				continue
			}
			pdu.State = "SUSPENDED"
			if dl := session.DeactivateUserPlane(ue.IMSI, id, userLocation); dl > 0 {
				n++
			}
		}
		log.WithIMSI(ue.IMSI).Infof("Deactivated %d/%d PDU session user-planes (§4.2.6 step 5 Complete list absent — using step-1b Request list=[%s])",
			n, len(ue.PendingReleasePDUList), strings.Join(ids, ","))
		ue.PendingReleasePDUList = nil
		return
	}

	// Neither list had active-N3 sessions enumerated. Per §4.2.6
	// step 5 "for each" semantics: no iteration → no-op. Common in
	// practice when the release races the PDU Session Resource
	// Setup exchange (gNB releases after receiving the Request but
	// before it allocated any N3 bearer to report). The SMF's
	// session record survives at ACTIVATION_PENDING and will be
	// reactivated by the next §4.2.3.2 Service Request once the UE
	// returns CM-CONNECTED.
	log.WithIMSI(ue.IMSI).Infof("§4.2.6 step 5: no active-N3 PDU sessions reported in Request or Complete — nothing to deactivate (spec: zero-iteration 'for each')")
}

// releaseRequestIEs bundles every IE the gNB may send in UE CONTEXT
// RELEASE REQUEST (TS 38.413 §9.2.2.4 IE table):
//
//	AMFUENGAPID                       M  reject
//	RANUENGAPID                       M  reject
//	PDUSessionResourceListCxtRelReq   O  reject — per-session list
//	                                              with active N3 UP
//	Cause                             M  ignore — release reason
//	GWContextReleaseIndication        O  ignore — W-AGF / trusted-
//	                                              non-3GPP hint per
//	                                              TS 38.300
type releaseRequestIEs struct {
	AMFUENGAPID                int64
	RANUENGAPID                int64
	PDUSessionResourceList     *genngap.PDUSessionResourceListCxtRelReq
	Cause                      *genngap.Cause
	GWContextReleaseIndication bool
}

func extractReq(r *genngap.UEContextReleaseRequest) releaseRequestIEs {
	out := releaseRequestIEs{}
	for i := range r.ProtocolIEs {
		ie := &r.ProtocolIEs[i]
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				out.AMFUENGAPID = int64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				out.RANUENGAPID = int64(*ie.Value.RANUENGAPID)
			}
		case int64(genngap.IdPDUSessionResourceListCxtRelReq):
			if ie.Value.PDUSessionResourceListCxtRelReq != nil {
				out.PDUSessionResourceList = ie.Value.PDUSessionResourceListCxtRelReq
			}
		case int64(genngap.IdCause):
			out.Cause = ie.Value.Cause
		case int64(genngap.IdGWContextReleaseIndication):
			out.GWContextReleaseIndication = true
		}
	}
	return out
}

// releaseCompleteIEs bundles every IE the gNB may send in UE CONTEXT
// RELEASE COMPLETE (TS 38.413 §9.2.2.6 IE table):
//
//	AMFUENGAPID                                   M  reject
//	RANUENGAPID                                   M  reject
//	UserLocationInformation                       O  ignore — for §4.2.6
//	                                                         location / billing
//	InfoOnRecommendedCellsAndRANNodesForPaging    O  ignore — reuse on next
//	                                                         paging (§8.4 CE)
//	PDUSessionResourceListCxtRelCpl               O  ignore — confirmed
//	                                                         released PDU
//	                                                         sessions
//	CriticalityDiagnostics                        O  ignore
//	PagingAssisDataforCEcapabUE                   O  ignore — CE paging hint
type releaseCompleteIEs struct {
	AMFUENGAPID                                int64
	RANUENGAPID                                int64
	UserLocationInformation                    *genngap.UserLocationInformation
	InfoOnRecommendedCellsAndRANNodesForPaging *genngap.InfoOnRecommendedCellsAndRANNodesForPaging
	PDUSessionResourceList                     *genngap.PDUSessionResourceListCxtRelCpl
	PagingAssisDataforCEcapabUE                *genngap.PagingAssisDataforCEcapabUE
}

func extractComplete(r *genngap.UEContextReleaseComplete) releaseCompleteIEs {
	out := releaseCompleteIEs{}
	for i := range r.ProtocolIEs {
		ie := &r.ProtocolIEs[i]
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				out.AMFUENGAPID = int64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				out.RANUENGAPID = int64(*ie.Value.RANUENGAPID)
			}
		case int64(genngap.IdUserLocationInformation):
			if ie.Value.UserLocationInformation != nil {
				out.UserLocationInformation = ie.Value.UserLocationInformation
			}
		case int64(genngap.IdInfoOnRecommendedCellsAndRANNodesForPaging):
			if ie.Value.InfoOnRecommendedCellsAndRANNodesForPaging != nil {
				out.InfoOnRecommendedCellsAndRANNodesForPaging = ie.Value.InfoOnRecommendedCellsAndRANNodesForPaging
			}
		case int64(genngap.IdPDUSessionResourceListCxtRelCpl):
			if ie.Value.PDUSessionResourceListCxtRelCpl != nil {
				out.PDUSessionResourceList = ie.Value.PDUSessionResourceListCxtRelCpl
			}
		case int64(genngap.IdPagingAssisDataforCEcapabUE):
			if ie.Value.PagingAssisDataforCEcapabUE != nil {
				out.PagingAssisDataforCEcapabUE = ie.Value.PagingAssisDataforCEcapabUE
			}
		}
	}
	return out
}

// formatCause renders the NGAP Cause CHOICE (TS 38.413 §9.3.1.2) to a
// short human-readable string for log output. Same shape as the helpers
// in sibling NGAP packages.
func formatCause(c *genngap.Cause) string {
	if c == nil {
		return "unspecified"
	}
	switch c.Present {
	case genngap.CausePresentRadioNetwork:
		if c.RadioNetwork != nil {
			return fmt.Sprintf("radioNetwork(%d)", int64(*c.RadioNetwork))
		}
	case genngap.CausePresentTransport:
		if c.Transport != nil {
			return fmt.Sprintf("transport(%d)", int64(*c.Transport))
		}
	case genngap.CausePresentNas:
		if c.Nas != nil {
			return fmt.Sprintf("nas(%d)", int64(*c.Nas))
		}
	case genngap.CausePresentProtocol:
		if c.Protocol != nil {
			return fmt.Sprintf("protocol(%d)", int64(*c.Protocol))
		}
	case genngap.CausePresentMisc:
		if c.Misc != nil {
			return fmt.Sprintf("misc(%d)", int64(*c.Misc))
		}
	}
	return "unspecified"
}

// isUserInactivityCause checks for radioNetwork cause #20 "user-
// inactivity" per TS 38.413 §9.3.1.2 CauseRadioNetwork. Used by the
// §8.3.2.2 "Interactions" rule: honour User-inactivity only when
// there is no pending downlink signalling.
func isUserInactivityCause(c *genngap.Cause) bool {
	if c == nil || c.Present != genngap.CausePresentRadioNetwork || c.RadioNetwork == nil {
		return false
	}
	return int64(*c.RadioNetwork) == int64(genngap.CauseRadioNetworkUserInactivity)
}

// formatPDUList renders a per-session list IE to "1,2,5" style log
// output. Empty slice → "none".
func formatPDUListReq(l *genngap.PDUSessionResourceListCxtRelReq) string {
	if l == nil || len(*l) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(*l))
	for _, it := range *l {
		ids = append(ids, fmt.Sprintf("%d", uint8(it.PDUSessionID)))
	}
	return strings.Join(ids, ",")
}

func formatPDUListCpl(l *genngap.PDUSessionResourceListCxtRelCpl) string {
	if l == nil || len(*l) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(*l))
	for _, it := range *l {
		ids = append(ids, fmt.Sprintf("%d", uint8(it.PDUSessionID)))
	}
	return strings.Join(ids, ",")
}

func locateUE(gnb *gnbctx.GnbCtx, amfUeID, ranUeID int64) *uectx.AmfUeCtx {
	if amfUeID != 0 {
		if ue := uectx.Default.LookupByAmfID(amfUeID); ue != nil {
			return ue
		}
	}
	if ranUeID != 0 {
		return uectx.Default.LookupByRanKey(gnb.GnbIP, ranUeID)
	}
	return nil
}

// Register installs handlers for both procedures this package covers:
// 41 (Command — AMF-initiated; we receive the SuccessfulOutcome here)
// 42 (Request — gNB-initiated; we respond by sending Command).
func Register() {
	ngap.Register(ngap.ProcCodeUEContextRelease, handleComplete)
	ngap.Register(ngap.ProcCodeUEContextReleaseReq, handleRequest)
}

// lowerLayerFailureClause returns the TS 24.501 v19.6.2 §-clause that
// authorises the AMF to abort the ongoing 5GMM procedure on a lower-
// layer failure. The clause depends on which procedure (and sub-step)
// is in flight at abort time. All citations resolve against the local
// PDF (specs/3gpp/ts_124501v190602p.pdf).
//
// Sub-procedure abnormal cases take precedence over the parent
// procedure's — they're more specific. E.g. a registration mid-auth
// is governed by the authentication abnormal case (§5.4.1.3.6) not
// by the registration-completion abnormal case (§5.5.1.x.8). The
// switch order below encodes that priority.
//
// Initial vs mobility/periodic registration is distinguished by
// ue.RegistrationType, populated by the registration handler from
// the 5GS registration type IE (TS 24.501 §9.11.3.7). Values are
// the strings "initial" / "mobility" / "periodic" / "emergency"
// per regTypeName in nf/amf/gmm/registration.go:961.
func lowerLayerFailureClause(ue *uectx.AmfUeCtx) string {
	switch ue.GMMSub {
	case uectx.GMMSubAuthentication:
		return "§5.4.1.3.6(a)"
	case uectx.GMMSubIdentification:
		return "§5.4.3.6"
	case uectx.GMMSubSecurityMode:
		return "§5.4.2.7"
	}
	switch ue.GMMProc {
	case uectx.GMMProcRegistration:
		if ue.RegistrationType == "initial" {
			return "§5.5.1.2.8(a)"
		}
		// mobility / periodic / emergency all flow through the
		// mobility-and-periodic abnormal cases per §5.5.1.3.8.
		return "§5.5.1.3.8(a)"
	case uectx.GMMProcServiceRequest:
		return "§5.6.1.8"
	case uectx.GMMProcDeregistration:
		// Direction (UE-initiated vs network-initiated) isn't
		// distinguished on uectx today; cite both clauses so an
		// operator scanning the log can pick the matching one.
		return "§5.5.2.2.7 / §5.5.2.3.5"
	case uectx.GMMProcConfigUpdate:
		return "§5.4.4.6"
	case uectx.GMMProcPaging:
		return "§5.6.2.2.2"
	}
	// Caller's guard ensures GMMProc != None when this fires; if a
	// new procedure type is added without updating this switch, the
	// initial-registration clause is the safest default (it's the
	// canonical "abort on lower-layer failure" rule and the speccheck
	// citation is verifiable).
	return "§5.5.1.2.8(a)"
}

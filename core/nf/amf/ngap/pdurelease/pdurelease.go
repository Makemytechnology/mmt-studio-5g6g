// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pdurelease — NGAP PDU Session Resource Release procedure.
//
// Authoritative spec: TS 38.413 §8.2.2 (PDF:
// specs/3gpp/ts_138413v190200p.pdf).
//
//	§8.2.2.1 General (verbatim): "The purpose of the PDU Session
//	  Resource Release procedure is to enable the release of already
//	  established PDU session resources for a given UE. The
//	  procedure uses UE-associated signalling."
//
//	§8.2.2.2 Successful Operation (verbatim excerpts):
//	  "The AMF initiates the procedure by sending a PDU SESSION
//	   RESOURCE RELEASE COMMAND message."
//	  "If a NAS-PDU IE is contained in the PDU SESSION RESOURCE
//	   RELEASE COMMAND message, the NG-RAN node shall pass it to
//	   the UE."
//	  "Upon reception of the PDU SESSION RESOURCE RELEASE COMMAND
//	   message the NG-RAN node shall execute the release of the
//	   requested PDU sessions."
//
//	§8.2.2.3 Unsuccessful Operation: "The unsuccessful operation is
//	  specified in the successful operation section." — i.e. no
//	  separate failure message; failed sessions are elided from the
//	  RELEASE RESPONSE.
//
// Procedure code = 28 (TS 38.413 §9.4). Message types per §9.2.1.4/5:
//
//	InitiatingMessage      = PDUSessionResourceReleaseCommand
//	SuccessfulOutcome      = PDUSessionResourceReleaseResponse
//
// Mandatory IE tables (generated code at
// codecs/asn1-go/protocols/ngap/generated/ngap_pdu_contents.go):
//
//	RELEASE COMMAND (§9.2.1.4):
//	  id-AMFUENGAPID                               M  reject
//	  id-RANUENGAPID                               M  reject
//	  id-RANPagingPriority                         O  ignore
//	  id-NASPDU                                    O  ignore   — piggyback 5GSM Release Command
//	  id-PDUSessionResourceToReleaseListRelCmd     M  reject
//
//	RELEASE RESPONSE (§9.2.1.5):
//	  id-AMFUENGAPID                               M  reject
//	  id-RANUENGAPID                               M  reject
//	  id-PDUSessionResourceReleasedListRelRes      O  ignore
//	  id-UserLocationInformation                   O  ignore
//	  id-CriticalityDiagnostics                    O  ignore
//
// Per-session Release Command Transfer (§9.3.4.2) carries only a
// mandatory Cause IE (§9.3.1.2). This is the bare-minimum
// implementation — operators that need finer RAN-side control
// (QoS flow delay critical, TL container) should extend the
// buildReleaseTransfer helper.
package pdurelease

import (
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/errind"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	sessionfsm "github.com/mmt/mmt-studio-core/nf/smf/session/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// Cause helpers — caller picks the category then the value. Mirrors
// the same pattern used in uectxrelease / errind so every NGAP-outbound
// package spells Cause construction identically.
func CauseNAS(v genngap.CauseNas) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentNas, Nas: &v}
}

func CauseRadioNetwork(v genngap.CauseRadioNetwork) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentRadioNetwork, RadioNetwork: &v}
}

// SendCommand ships a PDU SESSION RESOURCE RELEASE COMMAND to the
// gNB for the listed PDU Session IDs. Per-session Release Command
// Transfer carries only the mandatory Cause IE — pass
// CauseRadioNetwork(CauseRadioNetworkNormalRelease) for graceful
// release, CauseNAS(CauseNasDeregister) for dereg-driven release, etc.
//
// acceptNAS is the 5GSM PDU Session Release Command NAS PDU to
// piggyback via the Command's NAS-PDU IE. Pass nil to elide (the
// gNB will still tear down the DRB; the UE just learns via RRC
// rather than NAS).
func SendCommand(
	gnb *gnbctx.GnbCtx,
	ue *uectx.AmfUeCtx,
	pduSessionIDs []uint8,
	acceptNAS []byte,
	cause *genngap.Cause,
) error {
	log := logger.Get("amf.ngap.pdurelease")
	if gnb == nil || ue == nil {
		return fmt.Errorf("pdurelease: nil gnb / ue")
	}
	if len(pduSessionIDs) == 0 {
		return fmt.Errorf("pdurelease: no PDU sessions to release")
	}
	if cause == nil {
		return fmt.Errorf("pdurelease: cause IE is mandatory per §9.3.4.2")
	}

	// Procedure-collision guard (TS 38.413 §8.1 / §8.3.3.1). If the
	// UE-level release is already in progress, the per-PDU Release
	// Command is redundant — the UE Context Release takes all PDU
	// sessions with it.
	if ok, reason := uectx.CanStartNGAPProcedure(ue.NGAPProc, uectx.NGAPProcPDUSessionResourceRelease); !ok {
		log.WithIMSI(ue.IMSI).Warnf("PDUSessionResourceRelease skipped amfUeID=%d: %s",
			ue.AmfUeNGAPID, reason)
		return fmt.Errorf("pdurelease: blocked by NGAPProc=%s: %s", ue.NGAPProc, reason)
	}

	// Build per-session Release Command Transfer bytes (§9.3.4.2 —
	// one Cause IE each). Re-used for every session in this call.
	transfer := &genngap.PDUSessionResourceReleaseCommandTransfer{Cause: *cause}
	transferBytes, err := transfer.MarshalAPER()
	if err != nil {
		return fmt.Errorf("pdurelease: marshal Release Command Transfer: %w", err)
	}

	// List of sessions to release (§9.2.1.4 / §9.3.4.x item type).
	list := genngap.PDUSessionResourceToReleaseListRelCmd{}
	for _, id := range pduSessionIDs {
		list = append(list, genngap.PDUSessionResourceToReleaseItemRelCmd{
			PDUSessionID:                             genngap.PDUSessionID(id),
			PDUSessionResourceReleaseCommandTransfer: transferBytes,
		})
	}

	amfID := genngap.AMFUENGAPID(ue.AmfUeNGAPID)
	ranID := genngap.RANUENGAPID(ue.RanUeNGAPID)

	msg := &genngap.PDUSessionResourceReleaseCommand{}
	add := func(id int64, crit genngap.Criticality, v genngap.PDUSessionResourceReleaseCommandIEsValue) {
		msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.PDUSessionResourceReleaseCommandIEsEntry{
			Id:          genngap.ProtocolIEID(id),
			Criticality: crit,
			Value:       v,
		})
	}
	// IE emission order. APER SEQUENCE OF has no prescribed wire
	// order; UE-NGAP-IDs go first for capture-parity with reference
	// stacks, not because the spec requires it. Ran Paging Priority
	// is optional and not currently wired.
	add(int64(genngap.IdAMFUENGAPID), genngap.CriticalityReject,
		genngap.PDUSessionResourceReleaseCommandIEsValue{
			Present:     genngap.PDUSessionResourceReleaseCommandIEsValuePresentAMFUENGAPID,
			AMFUENGAPID: &amfID,
		})
	add(int64(genngap.IdRANUENGAPID), genngap.CriticalityReject,
		genngap.PDUSessionResourceReleaseCommandIEsValue{
			Present:     genngap.PDUSessionResourceReleaseCommandIEsValuePresentRANUENGAPID,
			RANUENGAPID: &ranID,
		})
	if len(acceptNAS) > 0 {
		nas := genngap.NASPDU(acceptNAS)
		add(int64(genngap.IdNASPDU), genngap.CriticalityIgnore,
			genngap.PDUSessionResourceReleaseCommandIEsValue{
				Present: genngap.PDUSessionResourceReleaseCommandIEsValuePresentNASPDU,
				NASPDU:  &nas,
			})
	}
	add(int64(genngap.IdPDUSessionResourceToReleaseListRelCmd), genngap.CriticalityReject,
		genngap.PDUSessionResourceReleaseCommandIEsValue{
			Present:                               genngap.PDUSessionResourceReleaseCommandIEsValuePresentPDUSessionResourceToReleaseListRelCmd,
			PDUSessionResourceToReleaseListRelCmd: &list,
		})

	inner, err := msg.MarshalAPER()
	if err != nil {
		return fmt.Errorf("pdurelease: marshal outer: %w", err)
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ngap.ProcCodePDUSessionResourceRelease,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		return fmt.Errorf("pdurelease: envelope: %w", err)
	}
	stream := gnb.UEStream(ue.AmfUeNGAPID)
	if err := gnb.Send(pdu, stream); err != nil {
		return fmt.Errorf("pdurelease: gnb send: %w", err)
	}
	ue.NGAPProc = uectx.NGAPProcPDUSessionResourceRelease

	log.WithIMSI(ue.IMSI).Infof("PDUSessionResourceReleaseCommand sent amfUeID=%d sessions=%v gNB=%s NAS=%dB",
		ue.AmfUeNGAPID, pduSessionIDs, gnb.GnbIP, len(acceptNAS))
	return nil
}

// handleResponse processes PDU SESSION RESOURCE RELEASE RESPONSE
// per §8.2.2.2. Each entry in PDUSessionResourceReleasedListRelRes
// confirms the gNB has torn down its DRB + NG-U tunnel for that
// session — the SMF can finalise state.
func handleResponse(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.pdurelease")
	var resp genngap.PDUSessionResourceReleaseResponse
	if err := resp.UnmarshalAPER(env.Value); err != nil {
		// TS 38.413 v19.2.0 §8.7.5.1 — Error Indication on
		// undecodable inbound message.
		log.Errorf("PDUSessionResourceReleaseResponse decode from %s: %v", gnb.GnbIP, err)
		_ = errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolTransferSyntaxError))
		return
	}
	amfUeID, ranUeID := extractUEIDs(resp.ProtocolIEs)
	ue := locateUE(gnb, amfUeID, ranUeID)
	if ue == nil {
		// §8.7.5.2 — Unknown local UE NGAP ID.
		log.Warnf("PDUSessionResourceReleaseResponse for unknown UE amfUeID=%d", amfUeID)
		_ = errind.Send(gnb, amfUeID, ranUeID,
			errind.CauseRadio(genngap.CauseRadioNetworkUnknownLocalUENGAPID))
		return
	}
	ue.NGAPProc = uectx.NGAPProcNone

	releasedCount := 0
	for _, ie := range resp.ProtocolIEs {
		if int64(ie.Id) != int64(genngap.IdPDUSessionResourceReleasedListRelRes) {
			continue
		}
		if ie.Value.PDUSessionResourceReleasedListRelRes == nil {
			continue
		}
		for _, item := range *ie.Value.PDUSessionResourceReleasedListRelRes {
			pduSessID := uint8(item.PDUSessionID)
			// Fire EvResourceReleaseResponse on the 5GSM FSM. The
			// handler (session.Release) has already torn down PFCP +
			// IP at its own call site; this event moves the FSM to
			// ReleasePending→Released for observability. If the
			// session is already gone (caller removed it pre-emptively)
			// the Fire is a no-op.
			sessKey := sessionfsm.Key{IMSI: ue.IMSI, PDUSessionID: pduSessID}
			_ = sessionfsm.Of(sessKey).Fire(&sessionfsm.Context{
				Key: sessKey, Event: sessionfsm.EvResourceReleaseResponse,
			})
			releasedCount++
			pm.Inc(pm.SMSessRel, 1)

			// §8.2.2.2: "For each PDU session for which the Secondary
			// RAT Usage Information IE is included in the PDU Session
			// Resource Release Response Transfer IE, the SMF shall
			// handle this information as specified in TS 23.502 [10]."
			// Not actioned — Secondary RAT billing is not wired yet.
			_ = item.PDUSessionResourceReleaseResponseTransfer // TODO(spec: TS 23.502 §4.3.4.3 Secondary RAT usage reporting)
		}
	}

	// TODO(spec: TS 38.413 §8.2.2.2 User Location Information) —
	//   "If the User Location Information IE is included in the PDU
	//    SESSION RESOURCE RELEASE RESPONSE message, the AMF shall
	//    handle this information as specified in TS 23.501 [9]."
	//   We skip it today; needed when location-based charging or
	//   mobility reporting lands.

	// NGAP per-UE FSM: ReleasePending → Established. Uses the
	// existing EvPDUResourceSetupResponse self-loop transition for
	// simplicity (the spec doesn't define separate states for
	// release progress — the UE stays in CM-CONNECTED throughout).
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(fk) // keep FSM entry alive for any subsequent procedure

	log.WithIMSI(ue.IMSI).Infof("PDUSessionResourceReleaseResponse amfUeID=%d released=%d",
		ue.AmfUeNGAPID, releasedCount)
	_ = session.Default // imported for future use via session.* release-finalise hooks
}

func extractUEIDs(ies []genngap.PDUSessionResourceReleaseResponseIEsEntry) (amf, ran int64) {
	for i := range ies {
		ie := &ies[i]
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				amf = int64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				ran = int64(*ie.Value.RANUENGAPID)
			}
		}
	}
	return
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

// Register installs the Response handler on the AMF dispatcher. Call
// from AMF bootstrap. SendCommand is AMF-initiated and invoked
// directly from callers (session.Release / MT-dereg path).
func Register() {
	ngap.Register(ngap.ProcCodePDUSessionResourceRelease, handleResponse)
}

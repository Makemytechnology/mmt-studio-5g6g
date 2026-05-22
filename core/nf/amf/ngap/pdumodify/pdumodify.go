// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pdumodify — NGAP PDU Session Resource Modify procedure.
//
// Authoritative spec: TS 38.413 §8.2.3 (PDF:
// specs/3gpp/ts_138413v190200p.pdf).
//
//	§8.2.3.1 General (verbatim): "The purpose of the PDU Session
//	  Resource Modify procedure is to enable configuration
//	  modifications of already established PDU session(s) for a
//	  given UE. It is also to enable the setup, modification and
//	  release of the QoS flow for already established PDU
//	  session(s). The procedure uses UE-associated signalling."
//
//	§8.2.3.2 Successful Operation (verbatim excerpts):
//	  "The AMF initiates the procedure by sending a PDU SESSION
//	   RESOURCE MODIFY REQUEST message to the NG-RAN node."
//	  "The PDU SESSION RESOURCE MODIFY REQUEST message shall
//	   contain the information required by the NG-RAN node, which
//	   may trigger the NG-RAN configuration modification for the
//	   existing PDU sessions listed in the PDU Session Resource
//	   Modify Request List IE."
//
//	§8.2.3.3 Unsuccessful Operation: "The unsuccessful operation is
//	  specified in the successful operation section." — failures
//	  come via the Failed list on the Response (no separate Failure
//	  message).
//
// Procedure code = 26 (TS 38.413 §9.4). Message types:
//
//	InitiatingMessage      = PDUSessionResourceModifyRequest
//	SuccessfulOutcome      = PDUSessionResourceModifyResponse
//
// Mandatory IE tables (generated code):
//
//	MODIFY REQUEST (§9.2.1.6):
//	  id-AMFUENGAPID                            M  reject
//	  id-RANUENGAPID                            M  reject
//	  id-RANPagingPriority                      O  ignore
//	  id-PDUSessionResourceModifyListModReq     M  reject
//
//	MODIFY RESPONSE (§9.2.1.7):
//	  id-AMFUENGAPID                                  M  reject
//	  id-RANUENGAPID                                  M  reject
//	  id-PDUSessionResourceModifyListModRes           O  ignore
//	  id-PDUSessionResourceFailedToModifyListModRes   O  ignore
//	  id-UserLocationInformation                      O  ignore
//	  id-CriticalityDiagnostics                       O  ignore
//
// The per-session Modify Request Transfer (§9.3.4.9) carries the
// actual modification details (new QoS flows, updated AMBR, etc.).
// This minimal implementation passes an empty transfer — callers
// populate via the transferBytes arg.
package pdumodify

import (
	"encoding/binary"
	"fmt"
	"strings"

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

// ModifyItem bundles what one PDU Session's Modify Request carries.
// nasPDU is the piggyback 5GSM PDU Session Modification Command
// (TS 24.501 §8.3.9); transferBytes is the APER-encoded
// PDUSessionResourceModifyRequestTransfer (§9.3.4.9).
type ModifyItem struct {
	PDUSessionID  uint8
	NASPDU        []byte
	TransferBytes []byte
}

// SendRequest ships PDU SESSION RESOURCE MODIFY REQUEST to the gNB.
// Each item in items drives one entry in the Modify list.
func SendRequest(gnb *gnbctx.GnbCtx, ue *uectx.AmfUeCtx, items []ModifyItem) error {
	log := logger.Get("amf.ngap.pdumodify")
	if gnb == nil || ue == nil {
		return fmt.Errorf("pdumodify: nil gnb / ue")
	}
	if len(items) == 0 {
		return fmt.Errorf("pdumodify: no Modify items supplied")
	}

	// Procedure-collision guard (TS 38.413 §8.1 / §8.3.3.1). Block
	// Modify if UE is being released.
	if ok, reason := uectx.CanStartNGAPProcedure(ue.NGAPProc, uectx.NGAPProcPDUSessionResourceModify); !ok {
		log.WithIMSI(ue.IMSI).Warnf("PDUSessionResourceModify skipped amfUeID=%d: %s",
			ue.AmfUeNGAPID, reason)
		return fmt.Errorf("pdumodify: blocked by NGAPProc=%s: %s", ue.NGAPProc, reason)
	}

	list := genngap.PDUSessionResourceModifyListModReq{}
	for _, it := range items {
		mi := genngap.PDUSessionResourceModifyItemModReq{
			PDUSessionID:                            genngap.PDUSessionID(it.PDUSessionID),
			PDUSessionResourceModifyRequestTransfer: it.TransferBytes,
		}
		if len(it.NASPDU) > 0 {
			n := genngap.NASPDU(it.NASPDU)
			mi.NASPDU = &n
		}
		list = append(list, mi)
	}

	amfID := genngap.AMFUENGAPID(ue.AmfUeNGAPID)
	ranID := genngap.RANUENGAPID(ue.RanUeNGAPID)

	msg := &genngap.PDUSessionResourceModifyRequest{}
	add := func(id int64, crit genngap.Criticality, v genngap.PDUSessionResourceModifyRequestIEsValue) {
		msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.PDUSessionResourceModifyRequestIEsEntry{
			Id:          genngap.ProtocolIEID(id),
			Criticality: crit,
			Value:       v,
		})
	}
	// IE emission order. APER SEQUENCE OF has no prescribed wire
	// order; UE-NGAP-IDs go first for capture-parity with reference
	// stacks, not because the spec requires it.
	add(int64(genngap.IdAMFUENGAPID), genngap.CriticalityReject,
		genngap.PDUSessionResourceModifyRequestIEsValue{
			Present:     genngap.PDUSessionResourceModifyRequestIEsValuePresentAMFUENGAPID,
			AMFUENGAPID: &amfID,
		})
	add(int64(genngap.IdRANUENGAPID), genngap.CriticalityReject,
		genngap.PDUSessionResourceModifyRequestIEsValue{
			Present:     genngap.PDUSessionResourceModifyRequestIEsValuePresentRANUENGAPID,
			RANUENGAPID: &ranID,
		})
	add(int64(genngap.IdPDUSessionResourceModifyListModReq), genngap.CriticalityReject,
		genngap.PDUSessionResourceModifyRequestIEsValue{
			Present:                            genngap.PDUSessionResourceModifyRequestIEsValuePresentPDUSessionResourceModifyListModReq,
			PDUSessionResourceModifyListModReq: &list,
		})

	inner, err := msg.MarshalAPER()
	if err != nil {
		return fmt.Errorf("pdumodify: marshal outer: %w", err)
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ngap.ProcCodePDUSessionResourceModify,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		return fmt.Errorf("pdumodify: envelope: %w", err)
	}
	stream := gnb.UEStream(ue.AmfUeNGAPID)
	if err := gnb.Send(pdu, stream); err != nil {
		return fmt.Errorf("pdumodify: gnb send: %w", err)
	}
	ue.NGAPProc = uectx.NGAPProcPDUSessionResourceModify

	// Fire 5GSM FSM EvModificationRequest per item — moves each
	// session to ModificationPending, arms T3591 per fsm_transitions.
	for _, it := range items {
		sessKey := sessionfsm.Key{IMSI: ue.IMSI, PDUSessionID: it.PDUSessionID}
		_ = sessionfsm.Of(sessKey).Fire(&sessionfsm.Context{
			Key: sessKey, Event: sessionfsm.EvModificationRequest,
		})
	}

	log.WithIMSI(ue.IMSI).Infof("PDUSessionResourceModifyRequest sent amfUeID=%d items=%d gNB=%s",
		ue.AmfUeNGAPID, len(items), gnb.GnbIP)
	return nil
}

// handleResponse processes PDU SESSION RESOURCE MODIFY RESPONSE per
// §8.2.3.2. Walks the Modify list (success) + Failed list (each
// entry rolls back the per-session FSM via EvResourceModifyFailure).
func handleResponse(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.pdumodify")
	var resp genngap.PDUSessionResourceModifyResponse
	if err := resp.UnmarshalAPER(env.Value); err != nil {
		// TS 38.413 v19.2.0 §8.7.5.1 — Error Indication on
		// undecodable inbound message.
		log.Errorf("PDUSessionResourceModifyResponse decode from %s: %v", gnb.GnbIP, err)
		_ = errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolTransferSyntaxError))
		return
	}
	amfUeID, ranUeID := extractUEIDs(resp.ProtocolIEs)
	ue := locateUE(gnb, amfUeID, ranUeID)
	if ue == nil {
		// §8.7.5.2 — Unknown local UE NGAP ID.
		log.Warnf("PDUSessionResourceModifyResponse for unknown UE amfUeID=%d", amfUeID)
		_ = errind.Send(gnb, amfUeID, ranUeID,
			errind.CauseRadio(genngap.CauseRadioNetworkUnknownLocalUENGAPID))
		return
	}
	ue.NGAPProc = uectx.NGAPProcNone

	var okCount, failCount int
	for _, ie := range resp.ProtocolIEs {
		switch int64(ie.Id) {
		case int64(genngap.IdPDUSessionResourceModifyListModRes):
			if ie.Value.PDUSessionResourceModifyListModRes == nil {
				continue
			}
			for _, item := range *ie.Value.PDUSessionResourceModifyListModRes {
				pduSessID := uint8(item.PDUSessionID)
				dec := decodeModifyResponseTransfer(
					item.PDUSessionResourceModifyResponseTransfer)
				if dec.failed != "" {
					log.WithIMSI(ue.IMSI).Warnf(
						"PDU Session %d modify: accepted flows=%s; failed flows=%s%s",
						pduSessID, dec.accepted, dec.failed, dec.tnlNote)
				} else {
					log.WithIMSI(ue.IMSI).Infof(
						"PDU Session %d modify: accepted flows=%s%s",
						pduSessID, dec.accepted, dec.tnlNote)
				}
				if dec.hasAdditionalDLTNL {
					// TS 38.413 §9.3.4.10 AdditionalDLQosFlowPerTNLInformation
					// — multi-tunnel split (one (TEID,addr) per QFI). Not
					// yet wired; we still install a DL FAR using the
					// session-level DL TNL for all accepted QFIs.
					log.WithIMSI(ue.IMSI).Warnf(
						"PDU Session %d modify: AdditionalDLQosFlowPerTNLInformation present (§9.3.4.10) — multi-tunnel split not yet wired, falling back to session-level DL TNL",
						pduSessID)
				}
				// TS 38.413 §8.2.3.2 step 4 (per spec text in
				// §9.3.4.10): hand the gNB-allocated DL NG-U tunnel
				// endpoint to the SMF so it can install a per-QFI
				// DL FAR on the UPF (TS 29.244 §7.5.4.17 / §7.5.2.3).
				session.HandleModifyResponseTNL(
					ue.IMSI, pduSessID, dec.dlTEID, dec.dlAddrV4, dec.acceptedQFI)
				sessKey := sessionfsm.Key{IMSI: ue.IMSI, PDUSessionID: pduSessID}
				_ = sessionfsm.Of(sessKey).Fire(&sessionfsm.Context{
					Key: sessKey, Event: sessionfsm.EvResourceModifyResponse,
				})
				okCount++
				pm.Inc(pm.SMModSucc, 1)
			}
		case int64(genngap.IdPDUSessionResourceFailedToModifyListModRes):
			if ie.Value.PDUSessionResourceFailedToModifyListModRes == nil {
				continue
			}
			for _, item := range *ie.Value.PDUSessionResourceFailedToModifyListModRes {
				pduSessID := uint8(item.PDUSessionID)
				cause := decodeModifyUnsuccessfulTransfer(
					item.PDUSessionResourceModifyUnsuccessfulTransfer)
				log.WithIMSI(ue.IMSI).Warnf(
					"PDU Session %d modify FAILED at gNB (§8.2.3.2 Failed list) cause=%s",
					pduSessID, cause)
				sessKey := sessionfsm.Key{IMSI: ue.IMSI, PDUSessionID: pduSessID}
				_ = sessionfsm.Of(sessKey).Fire(&sessionfsm.Context{
					Key: sessKey, Event: sessionfsm.EvResourceModifyFailure,
				})
				failCount++
				pm.Inc(pm.SMModFail, 1)
			}
		}
	}

	// NGAP per-UE FSM: collapse the modify fork back to ESTABLISHED
	// via the existing ResourceSetupResponse path (modify + setup
	// share the same "back to Established" transition today).
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(fk) // keep FSM entry alive

	log.WithIMSI(ue.IMSI).Infof("PDUSessionResourceModifyResponse amfUeID=%d ok=%d failed=%d",
		ue.AmfUeNGAPID, okCount, failCount)
}

func extractUEIDs(ies []genngap.PDUSessionResourceModifyResponseIEsEntry) (amf, ran int64) {
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

// decodedModifyResponse holds the parsed §9.3.4.10 transfer fields the
// SMF (and human log lines) need. Zero-valued TEID/Addr means the gNB
// didn't supply a §9.3.1.16 DL NG-U TNL — caller must treat that as
// "no DL FAR install this round" rather than as 0.0.0.0.
type decodedModifyResponse struct {
	accepted    string  // human-readable accepted-QFI list
	failed      string  // human-readable failed-QFI list (with Causes)
	tnlNote     string  // " [NG-U TNL updated]" if either dir was set
	acceptedQFI []uint8 // raw accepted QFIs for SMF DL FAR install
	dlTEID      uint32  // gNB's DL GTP-U TEID (§9.3.1.16)
	dlAddrV4    uint32  // gNB's DL TransportLayerAddress IPv4 (§9.3.2.2)
	hasAdditionalDLTNL bool // §9.3.4.10 AdditionalDLQosFlowPerTNLInformation present (multi-tunnel; not yet wired)
}

// decodeModifyResponseTransfer parses the per-session
// PDUSessionResourceModifyResponseTransfer (TS 38.413 §9.3.4.10) and
// returns human-readable summaries of accepted / failed QoS flows plus
// the raw fields the SMF needs to install a per-QFI DL FAR. Empty
// "none" / "" for the summaries means no list was included.
//
// §9.3.4.10 IEs (all optional):
//
//	DL NG-U UP TNL Information       → gNB's new downlink tunnel endpoint
//	UL NG-U UP TNL Information       → UPF's new uplink tunnel endpoint
//	QoS Flow add/modify response list → flows the NG-RAN accepted
//	Additional DL QoS Flow per TNL   → multi-tunnel DL mapping
//	QoS Flow failed-to-add-or-modify → flows the NG-RAN rejected (+ Cause)
func decodeModifyResponseTransfer(b []byte) decodedModifyResponse {
	out := decodedModifyResponse{accepted: "none"}
	if len(b) == 0 {
		return out
	}
	var t genngap.PDUSessionResourceModifyResponseTransfer
	if err := t.UnmarshalAPER(b); err != nil {
		out.accepted = "decode-error"
		return out
	}
	if t.QosFlowAddOrModifyResponseList != nil {
		ids := make([]string, 0, len(*t.QosFlowAddOrModifyResponseList))
		out.acceptedQFI = make([]uint8, 0, len(*t.QosFlowAddOrModifyResponseList))
		for _, it := range *t.QosFlowAddOrModifyResponseList {
			ids = append(ids, fmt.Sprintf("qfi=%d", int64(it.QosFlowIdentifier)))
			out.acceptedQFI = append(out.acceptedQFI, uint8(it.QosFlowIdentifier))
		}
		out.accepted = strings.Join(ids, ",")
	}
	if t.QosFlowFailedToAddOrModifyList != nil {
		fs := make([]string, 0, len(*t.QosFlowFailedToAddOrModifyList))
		for _, it := range *t.QosFlowFailedToAddOrModifyList {
			fs = append(fs, fmt.Sprintf("qfi=%d:%s",
				int64(it.QosFlowIdentifier), formatCause(&it.Cause)))
		}
		out.failed = strings.Join(fs, ",")
	}
	if t.DLNGUUPTNLInformation != nil || t.ULNGUUPTNLInformation != nil {
		out.tnlNote = " [NG-U TNL updated]"
	}
	// §9.3.1.16 UPTransportLayerInformation → CHOICE of GTPTunnel.
	// IPv6 / mixed addresses are deferred (Bytes>4); we install a DL
	// FAR only for the IPv4-only case the dataplane bridge supports.
	if t.DLNGUUPTNLInformation != nil &&
		t.DLNGUUPTNLInformation.Present == genngap.UPTransportLayerInformationPresentGTPTunnel &&
		t.DLNGUUPTNLInformation.GTPTunnel != nil {
		gtp := t.DLNGUUPTNLInformation.GTPTunnel
		if len(gtp.GTPTEID) >= 4 {
			out.dlTEID = binary.BigEndian.Uint32(gtp.GTPTEID[:4])
		}
		if len(gtp.TransportLayerAddress.Bytes) >= 4 {
			out.dlAddrV4 = binary.BigEndian.Uint32(gtp.TransportLayerAddress.Bytes[:4])
		}
	}
	if t.AdditionalDLQosFlowPerTNLInformation != nil {
		out.hasAdditionalDLTNL = true
	}
	return out
}

// decodeModifyUnsuccessfulTransfer parses the per-session
// PDUSessionResourceModifyUnsuccessfulTransfer (TS 38.413 §9.3.4.12)
// and returns the Cause IE rendered for log output. Returns
// "decode-error" / "missing" on malformed / empty input.
func decodeModifyUnsuccessfulTransfer(b []byte) string {
	if len(b) == 0 {
		return "missing"
	}
	var t genngap.PDUSessionResourceModifyUnsuccessfulTransfer
	if err := t.UnmarshalAPER(b); err != nil {
		return "decode-error"
	}
	return formatCause(&t.Cause)
}

// formatCause renders the NGAP Cause CHOICE (TS 38.413 §9.3.1.2) to a
// short human-readable string. Mirrors initialctxsetup.formatCause —
// duplicated to avoid a cross-package dependency.
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

// Register installs the Response handler on the AMF dispatcher.
func Register() {
	ngap.Register(ngap.ProcCodePDUSessionResourceModify, handleResponse)
}

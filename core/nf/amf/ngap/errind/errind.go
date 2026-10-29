// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package errind — NGAP Error Indication initiator (TS 38.413 v19.2.0
// §8.7.5). Earlier comments in this package cited §8.7.2 — wrong:
// that's RAN Configuration Update. §-clause numbers verified against
// /tmp/ts38413.txt line 7214.
//
// Per §8.7.5.1 (line 7217-7218):
//   "The Error Indication procedure is initiated by a node in order
//    to report detected errors in one incoming message, provided
//    they cannot be reported by an appropriate failure message."
//
// Per §8.7.5.2 (line 7273-7277):
//   "The ERROR INDICATION message shall contain at least either the
//    Cause IE or the Criticality Diagnostics IE. In case the Error
//    Indication procedure is triggered by utilising UE-associated
//    signalling the AMF UE NGAP ID IE and the RAN UE NGAP ID IE
//    shall be included in the ERROR INDICATION message. If one or
//    both of the AMF UE NGAP ID IE and the RAN UE NGAP ID IE are
//    not correct, the cause shall be set to an appropriate value,
//    e.g., 'Unknown local UE NGAP ID' or 'Inconsistent remote UE
//    NGAP ID'."
//
// Use cases in the AMF:
//   - Mandatory IE missing on an inbound NGAP PDU and no failure
//     message exists for that procedure (e.g. missing NAS-PDU on
//     Uplink NAS Transport — §8.6.3.3).
//   - UE-associated PDU references an AMF-UE-NGAP-ID that isn't in
//     our registry (stale reference after cleanup).
//   - Decode failure on an inbound PDU.
//
// The AMF MUST send on stream 0 (non-UE-associated signalling) when
// the trigger message was also non-UE-associated, or on the UE's
// assigned stream when UE-associated. The helper below picks based
// on whether caller supplied an amfUeID.
package errind

import (
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// procCodeErrorIndication mirrors ngap.ProcCodeErrorIndication (TS 38.413
// §9.3.3.2). Inlined here so package errind can be imported by package
// ngap (dispatch.go) without an import cycle.
const procCodeErrorIndication = 9

// Cause helpers matching the five NGAP Cause CHOICE alternatives
// (TS 38.413 §9.3.1.2). Imported into call sites by value.
func CauseRadio(v genngap.CauseRadioNetwork) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentRadioNetwork, RadioNetwork: &v}
}
func CauseTransport(v genngap.CauseTransport) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentTransport, Transport: &v}
}
func CauseNAS(v genngap.CauseNas) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentNas, Nas: &v}
}
func CauseProtocol(v genngap.CauseProtocol) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentProtocol, Protocol: &v}
}
func CauseMisc(v genngap.CauseMisc) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentMisc, Misc: &v}
}

// Send ships an ERROR INDICATION PDU to the gNB.
//
//   - When amfUeID != 0 OR ranUeID != 0 the error is UE-associated:
//     AMF-UE-NGAP-ID + RAN-UE-NGAP-ID IEs included when non-zero, and
//     the PDU rides the UE-associated stream derived from amfUeID via
//     gnb.UEStream().
//   - Otherwise the error is non-UE-associated: no ID IEs, PDU ships
//     on stream 0 (TS 38.412 §7 "common" pair).
//
// Per §8.7.5.2: "The ERROR INDICATION message shall contain at least
// either the Cause IE or the Criticality Diagnostics IE." Callers
// SHOULD pass a non-nil Cause; Send permits nil for the corner case
// of "couldn't determine cause" — spec allows the message to ride
// without a Cause IE provided Criticality Diagnostics IE is set
// instead (we don't populate that yet; TODO when needed).
func Send(gnb *gnbctx.GnbCtx, amfUeID, ranUeID int64, cause *genngap.Cause) error {
	log := logger.Get("amf.ngap.errind")
	if gnb == nil {
		return fmt.Errorf("errind.Send: nil gnb")
	}

	var ies []genngap.ErrorIndicationIEsEntry
	if amfUeID != 0 {
		id := genngap.AMFUENGAPID(amfUeID)
		ies = append(ies, genngap.ErrorIndicationIEsEntry{
			Id:          genngap.ProtocolIEID(genngap.IdAMFUENGAPID),
			Criticality: genngap.CriticalityIgnore,
			Value: genngap.ErrorIndicationIEsValue{
				Present:     genngap.ErrorIndicationIEsValuePresentAMFUENGAPID,
				AMFUENGAPID: &id,
			},
		})
	}
	if ranUeID != 0 {
		id := genngap.RANUENGAPID(ranUeID)
		ies = append(ies, genngap.ErrorIndicationIEsEntry{
			Id:          genngap.ProtocolIEID(genngap.IdRANUENGAPID),
			Criticality: genngap.CriticalityIgnore,
			Value: genngap.ErrorIndicationIEsValue{
				Present:     genngap.ErrorIndicationIEsValuePresentRANUENGAPID,
				RANUENGAPID: &id,
			},
		})
	}
	if cause != nil {
		ies = append(ies, genngap.ErrorIndicationIEsEntry{
			Id:          genngap.ProtocolIEID(genngap.IdCause),
			Criticality: genngap.CriticalityIgnore,
			Value: genngap.ErrorIndicationIEsValue{
				Present: genngap.ErrorIndicationIEsValuePresentCause,
				Cause:   cause,
			},
		})
	}
	// TODO(spec: TS 38.413 §8.7.2.2 "Criticality Diagnostics") —
	//   populate when we can identify which IE/criticality triggered.

	msg := &genngap.ErrorIndication{ProtocolIEs: ies}
	inner, err := msg.MarshalAPER()
	if err != nil {
		return fmt.Errorf("errind.Send: marshal: %w", err)
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: procCodeErrorIndication,
		Criticality:   wire.CriticalityIgnore,
		Value:         inner,
	})
	if err != nil {
		return fmt.Errorf("errind.Send: envelope: %w", err)
	}

	// Stream selection (TS 38.412 §7):
	//   non-UE-associated → stream 0 (the "common" reserved pair).
	//   UE-associated     → the UE's dedicated stream.
	stream := 0
	if amfUeID != 0 {
		stream = gnb.UEStream(amfUeID)
	}
	if err := gnb.Send(pdu, stream); err != nil {
		return fmt.Errorf("errind.Send: gnb.Send: %w", err)
	}
	log.Infof("NGAP Error Indication sent to %s amfUeID=%d ranUeID=%d stream=%d",
		gnb.GnbIP, amfUeID, ranUeID, stream)
	return nil
}

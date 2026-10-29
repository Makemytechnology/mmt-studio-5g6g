// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pwscancel — PWS Cancel procedure.
//
// Authoritative spec: TS 38.413 v19.2.0 §8.9.2 "PWS Cancel" (PDF:
// specs/3gpp/ts_138413v190200p.pdf). Message layouts at §9.2.8.3
// (Request) and §9.2.8.4 (Response).
//
// §8.9.2.1 General (verbatim line 7708-7710): "The purpose of the
//
//	PWS Cancel procedure is to cancel an already ongoing broadcast
//	of a warning message. The procedure uses non UE-associated
//	signalling."
//
// §8.9.2.2 Successful Operation (verbatim line 7719, 7730-7740):
//
//	"The AMF initiates the procedure by sending a PWS CANCEL REQUEST
//	 message to the NG-RAN node."
//	"The NG-RAN node shall acknowledge the PWS CANCEL REQUEST message
//	 by sending the PWS CANCEL RESPONSE message, with the Message
//	 Identifier IE and the Serial Number IE copied from the PWS
//	 CANCEL REQUEST message and shall, if there is an area to report
//	 where an ongoing broadcast was stopped successfully, include
//	 the Broadcast Cancelled Area List IE."
//	"If the Broadcast Cancelled Area List IE is not included in the
//	 PWS CANCEL RESPONSE message, the AMF shall consider that the
//	 NG-RAN node had no ongoing broadcast to stop for the same
//	 Message Identifier and Serial Number."
//
// §8.9.2.3 Unsuccessful Operation: "Not applicable."
// §8.9.2.4 Abnormal Conditions: "Void."
//
// Procedure code = 32 (TS 38.413 §9.4: "id-PWSCancel ProcedureCode
// ::= 32"). InitiatingMessage criticality reject.
//
// §9.2.8.3 IE table:
//
//	Message Identifier               M  reject  9.3.1.35
//	Serial Number                    M  reject  9.3.1.36
//	Warning Area List                O  ignore  9.3.1.37
//	Cancel-All Warning Messages Ind  O  reject  9.3.1.47
package pwscancel

import (
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	runtime "github.com/mmt/asn1go/pkg/runtime"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// Params for one PWS CANCEL REQUEST. MessageIdentifier + SerialNumber
// are the §9.2.8.3 mandatory IEs and identify the warning message
// to cancel. CancelAll⇒ §8.9.2.2 line 7742-7748 "stop broadcasting
// and discard all warning messages for the area".
type Params struct {
	MessageIdentifier uint16
	SerialNumber      uint16
	CancelAll         bool
}

// Send builds the §9.2.8.3 PWS CANCEL REQUEST and emits it on the
// gNB's non-UE-associated stream (stream 0).
func Send(gnb *gnbctx.GnbCtx, p Params) error {
	log := logger.Get("amf.ngap.pwscancel")

	msg := &genngap.PWSCancelRequest{}
	add := func(id int64, crit genngap.Criticality, v genngap.PWSCancelRequestIEsValue) {
		msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.PWSCancelRequestIEsEntry{
			Id:          genngap.ProtocolIEID(id),
			Criticality: crit,
			Value:       v,
		})
	}

	mid := genngap.MessageIdentifier(runtime.BitString{
		Bytes: []byte{byte(p.MessageIdentifier >> 8), byte(p.MessageIdentifier)},
		BitLength: 16,
	})
	sn := genngap.SerialNumber(runtime.BitString{
		Bytes: []byte{byte(p.SerialNumber >> 8), byte(p.SerialNumber)},
		BitLength: 16,
	})
	add(int64(genngap.IdMessageIdentifier), genngap.CriticalityReject,
		genngap.PWSCancelRequestIEsValue{
			Present:           genngap.PWSCancelRequestIEsValuePresentMessageIdentifier,
			MessageIdentifier: &mid,
		})
	add(int64(genngap.IdSerialNumber), genngap.CriticalityReject,
		genngap.PWSCancelRequestIEsValue{
			Present:      genngap.PWSCancelRequestIEsValuePresentSerialNumber,
			SerialNumber: &sn,
		})
	if p.CancelAll {
		// §9.3.1.47 CancelAllWarningMessages is an extensible
		// ENUMERATED with one base value "true". Presence ⇒ "yes".
		ind := genngap.CancelAllWarningMessages(0)
		add(int64(genngap.IdCancelAllWarningMessages), genngap.CriticalityReject,
			genngap.PWSCancelRequestIEsValue{
				Present:                  genngap.PWSCancelRequestIEsValuePresentCancelAllWarningMessages,
				CancelAllWarningMessages: &ind,
			})
	}

	inner, err := msg.MarshalAPER()
	if err != nil {
		return fmt.Errorf("PWSCancelRequest APER: %w", err)
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ngap.ProcCodePWSCancel,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		return err
	}
	if err := gnb.Send(pdu, 0); err != nil {
		return err
	}
	pm.Inc(pm.NGAPPWSCancelReq, 1)
	log.Infof("PWS CANCEL REQUEST sent gNB=%s msgID=%d serial=%d cancelAll=%v",
		gnb.GnbIP, p.MessageIdentifier, p.SerialNumber, p.CancelAll)
	return nil
}

// HandleResponse decodes a §9.2.8.4 PWS CANCEL RESPONSE.
// Per §8.9.2.2 line 7739-7740: "If the Broadcast Cancelled Area List
// IE is not included in the PWS CANCEL RESPONSE message, the AMF
// shall consider that the NG-RAN node had no ongoing broadcast to
// stop for the same Message Identifier and Serial Number."
func HandleResponse(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.pwscancel")
	pm.Inc(pm.NGAPPWSCancelResp, 1)

	if env.Type != wire.SuccessfulOutcome {
		log.Warnf("PWS Cancel Response from %s: unexpected envelope type %s",
			gnb.GnbIP, env.Type)
		return
	}

	var msg genngap.PWSCancelResponse
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("PWSCancelResponse decode from %s: %v", gnb.GnbIP, err)
		return
	}

	var msgID, serial uint16
	var cancelledPresent bool
	var cancelledSummary string
	for _, ie := range msg.ProtocolIEs {
		switch int64(ie.Id) {
		case int64(genngap.IdMessageIdentifier):
			if ie.Value.MessageIdentifier != nil {
				msgID = bitsToU16(*ie.Value.MessageIdentifier)
			}
		case int64(genngap.IdSerialNumber):
			if ie.Value.SerialNumber != nil {
				serial = bitsToU16(genngap.MessageIdentifier(*ie.Value.SerialNumber))
			}
		case int64(genngap.IdBroadcastCancelledAreaList):
			if ie.Value.BroadcastCancelledAreaList != nil {
				cancelledPresent = true
				cancelledSummary = formatCancelled(ie.Value.BroadcastCancelledAreaList)
			}
		}
	}

	if !cancelledPresent {
		// §8.9.2.2 line 7739-7740 verbatim — AMF shall consider no
		// ongoing broadcast was found for this msgID/serial.
		log.Infof("PWS Cancel Response from %s msgID=%d serial=%d: BroadcastCancelledAreaList absent — no ongoing broadcast for this Message Identifier/Serial Number (TS 38.413 §8.9.2.2)",
			gnb.GnbIP, msgID, serial)
		return
	}
	log.Infof("PWS Cancel Response from %s msgID=%d serial=%d cancelled=%s",
		gnb.GnbIP, msgID, serial, cancelledSummary)
}

// bitsToU16 unpacks a 16-bit BIT STRING to uint16.
func bitsToU16(m genngap.MessageIdentifier) uint16 {
	b := runtime.BitString(m).Bytes
	if len(b) < 2 {
		return 0
	}
	return uint16(b[0])<<8 | uint16(b[1])
}

// formatCancelled renders the §9.3.1.44 BroadcastCancelledAreaList CHOICE.
func formatCancelled(c *genngap.BroadcastCancelledAreaList) string {
	switch c.Present {
	case genngap.BroadcastCancelledAreaListPresentCellIDCancelledNR:
		if c.CellIDCancelledNR == nil {
			return "nr-cells:0"
		}
		return fmt.Sprintf("nr-cells:%d", len(*c.CellIDCancelledNR))
	case genngap.BroadcastCancelledAreaListPresentTAICancelledNR:
		if c.TAICancelledNR == nil {
			return "nr-tais:0"
		}
		return fmt.Sprintf("nr-tais:%d", len(*c.TAICancelledNR))
	case genngap.BroadcastCancelledAreaListPresentEmergencyAreaIDCancelledNR:
		if c.EmergencyAreaIDCancelledNR == nil {
			return "nr-emerg:0"
		}
		return fmt.Sprintf("nr-emerg:%d", len(*c.EmergencyAreaIDCancelledNR))
	case genngap.BroadcastCancelledAreaListPresentCellIDCancelledEUTRA:
		return "eutra-cells"
	case genngap.BroadcastCancelledAreaListPresentTAICancelledEUTRA:
		return "eutra-tais"
	case genngap.BroadcastCancelledAreaListPresentEmergencyAreaIDCancelledEUTRA:
		return "eutra-emerg"
	}
	return "unknown"
}

// Register installs HandleResponse on the AMF dispatcher.
func Register() {
	ngap.Register(ngap.ProcCodePWSCancel, HandleResponse)
}

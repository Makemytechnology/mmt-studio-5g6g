// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package writereplace — Write-Replace Warning procedure.
//
// Authoritative spec: TS 38.413 v19.2.0 §8.9.1 "Write-Replace
// Warning" (PDF: specs/3gpp/ts_138413v190200p.pdf). Message layouts
// at §9.2.8.1 (Request) and §9.2.8.2 (Response).
//
// §8.9.1.1 General (verbatim line 7614-7615): "The purpose of
//
//	Write-Replace Warning procedure is to start or overwrite the
//	broadcasting of warning messages. The procedure uses non
//	UE-associated signalling."
//
// §8.9.1.2 Successful Operation (verbatim line 7624-7625, 7678-7682):
//
//	"The AMF initiates the procedure by sending a WRITE-REPLACE
//	 WARNING REQUEST message to the NG-RAN node."
//	"The NG-RAN node acknowledges the WRITE-REPLACE WARNING REQUEST
//	 message by sending a WRITE-REPLACE WARNING RESPONSE message to
//	 the AMF."
//	"If the Broadcast Completed Area List IE is not included in the
//	 WRITE-REPLACE WARNING RESPONSE message, the AMF shall consider
//	 that the broadcast is unsuccessful in all the cells within the
//	 NG-RAN node."
//
// §8.9.1.3 Unsuccessful Operation: "Not applicable." (no Failure
// message — gNB always replies Successful).
//
// Procedure code = 51 (TS 38.413 §9.4: "id-WriteReplaceWarning
// ProcedureCode ::= 51"). InitiatingMessage criticality reject.
//
// §9.2.8.1 IE table (verbatim presence column):
//
//	Message Identifier              M  reject  9.3.1.35  BIT STRING(16)
//	Serial Number                   M  reject  9.3.1.36  BIT STRING(16)
//	Warning Area List               O  ignore  9.3.1.37
//	Repetition Period               M  reject  9.3.1.49  INTEGER 0..131071
//	Number of Broadcasts Requested  M  reject  9.3.1.38  INTEGER 0..65535
//	Warning Type                    O  ignore  9.3.1.39  OCTET STRING(2)
//	Warning Security Information    O  ignore  not used  (§9.2.8.1 NOTE)
//	Data Coding Scheme              O  ignore  9.3.1.41  BIT STRING(8)
//	Warning Message Contents        O  ignore  9.3.1.42  OCTET STRING 1..9600
//	Concurrent Warning Message Ind  O  reject  9.3.1.46
//	Warning Area Coordinates        O  ignore  9.3.1.112
package writereplace

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

// Params carries the application-level inputs for one Write-Replace
// Warning Request. Mandatory fields per §9.2.8.1 are non-pointer.
// Optional payload IEs are pointers — nil ⇒ omitted.
type Params struct {
	// MessageIdentifier (§9.3.1.35) — 16-bit value identifying the
	// warning-message type (CMAS, ETWS …); semantics defined in
	// TS 23.041 (Cell Broadcast Service).
	MessageIdentifier uint16
	// SerialNumber (§9.3.1.36) — 16-bit operator-assigned serial;
	// format defined in TS 23.041.
	SerialNumber uint16
	// RepetitionPeriod (§9.3.1.49) — 0..131071 in units defined in
	// TS 23.041. Value 0 with Concurrent indicator absent ⇒
	// "do not broadcast secondary notification" per §8.9.1.4.
	RepetitionPeriod uint32
	// NumberOfBroadcasts (§9.3.1.38) — 0..65535. With Concurrent
	// indicator absent and value 0 ⇒ §8.9.1.4 "shall not broadcast".
	NumberOfBroadcasts uint16

	// Optional payload (§9.2.8.1 IE table, presence O).
	WarningType            *[2]byte // 9.3.1.39
	DataCodingScheme       *byte    // 9.3.1.41 (8-bit BIT STRING; one octet)
	WarningMessageContents []byte   // 9.3.1.42, 1..9600 octets; nil ⇒ omit
	ConcurrentInd          bool     // §9.3.1.46 — present ⇔ true
}

// Send builds the §9.2.8.1 WRITE-REPLACE WARNING REQUEST and emits it
// on the gNB's non-UE-associated stream (stream 0). PWS uses non-
// UE-associated signalling per §8.9.1.1.
func Send(gnb *gnbctx.GnbCtx, p Params) error {
	log := logger.Get("amf.ngap.writereplace")

	msg := &genngap.WriteReplaceWarningRequest{}
	add := func(id int64, crit genngap.Criticality, v genngap.WriteReplaceWarningRequestIEsValue) {
		msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.WriteReplaceWarningRequestIEsEntry{
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
		genngap.WriteReplaceWarningRequestIEsValue{
			Present:           genngap.WriteReplaceWarningRequestIEsValuePresentMessageIdentifier,
			MessageIdentifier: &mid,
		})
	add(int64(genngap.IdSerialNumber), genngap.CriticalityReject,
		genngap.WriteReplaceWarningRequestIEsValue{
			Present:      genngap.WriteReplaceWarningRequestIEsValuePresentSerialNumber,
			SerialNumber: &sn,
		})

	rp := genngap.RepetitionPeriod(p.RepetitionPeriod)
	add(int64(genngap.IdRepetitionPeriod), genngap.CriticalityReject,
		genngap.WriteReplaceWarningRequestIEsValue{
			Present:          genngap.WriteReplaceWarningRequestIEsValuePresentRepetitionPeriod,
			RepetitionPeriod: &rp,
		})
	nb := genngap.NumberOfBroadcastsRequested(p.NumberOfBroadcasts)
	add(int64(genngap.IdNumberOfBroadcastsRequested), genngap.CriticalityReject,
		genngap.WriteReplaceWarningRequestIEsValue{
			Present:                     genngap.WriteReplaceWarningRequestIEsValuePresentNumberOfBroadcastsRequested,
			NumberOfBroadcastsRequested: &nb,
		})

	// Optional payload IEs. The codec carries criticality "ignore"
	// for these — gNB drops on decode failure but procedure continues.
	if p.WarningType != nil {
		wt := genngap.WarningType((*p.WarningType)[:])
		add(int64(genngap.IdWarningType), genngap.CriticalityIgnore,
			genngap.WriteReplaceWarningRequestIEsValue{
				Present:     genngap.WriteReplaceWarningRequestIEsValuePresentWarningType,
				WarningType: &wt,
			})
	}
	if p.DataCodingScheme != nil {
		dcs := genngap.DataCodingScheme(runtime.BitString{
			Bytes: []byte{*p.DataCodingScheme}, BitLength: 8,
		})
		add(int64(genngap.IdDataCodingScheme), genngap.CriticalityIgnore,
			genngap.WriteReplaceWarningRequestIEsValue{
				Present:          genngap.WriteReplaceWarningRequestIEsValuePresentDataCodingScheme,
				DataCodingScheme: &dcs,
			})
	}
	if len(p.WarningMessageContents) > 0 {
		wmc := genngap.WarningMessageContents(p.WarningMessageContents)
		add(int64(genngap.IdWarningMessageContents), genngap.CriticalityIgnore,
			genngap.WriteReplaceWarningRequestIEsValue{
				Present:                genngap.WriteReplaceWarningRequestIEsValuePresentWarningMessageContents,
				WarningMessageContents: &wmc,
			})
	}
	if p.ConcurrentInd {
		// §9.3.1.46 ConcurrentWarningMessageInd is an extensible
		// ENUMERATED with one base value "true". Presence ⇒ "yes".
		ind := genngap.ConcurrentWarningMessageInd(0)
		add(int64(genngap.IdConcurrentWarningMessageInd), genngap.CriticalityReject,
			genngap.WriteReplaceWarningRequestIEsValue{
				Present:                     genngap.WriteReplaceWarningRequestIEsValuePresentConcurrentWarningMessageInd,
				ConcurrentWarningMessageInd: &ind,
			})
	}

	inner, err := msg.MarshalAPER()
	if err != nil {
		return fmt.Errorf("WriteReplaceWarningRequest APER: %w", err)
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ngap.ProcCodeWriteReplaceWarning,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		return err
	}
	// Stream 0 — non-UE-associated per §8.9.1.1.
	if err := gnb.Send(pdu, 0); err != nil {
		return err
	}
	pm.Inc(pm.NGAPPWSWriteReplaceReq, 1)
	log.Infof("WRITE-REPLACE WARNING REQUEST sent gNB=%s msgID=%d serial=%d repPeriod=%d numBroadcasts=%d",
		gnb.GnbIP, p.MessageIdentifier, p.SerialNumber, p.RepetitionPeriod, p.NumberOfBroadcasts)
	return nil
}

// HandleResponse decodes a §9.2.8.2 WRITE-REPLACE WARNING RESPONSE.
// Per §8.9.1.2 line 7681-7682: "If the Broadcast Completed Area List
// IE is not included in the WRITE-REPLACE WARNING RESPONSE message,
// the AMF shall consider that the broadcast is unsuccessful in all
// the cells within the NG-RAN node."
func HandleResponse(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.writereplace")
	pm.Inc(pm.NGAPPWSWriteReplaceResp, 1)

	if env.Type != wire.SuccessfulOutcome {
		// §8.9.1.3 says "Not applicable" for unsuccessful operation —
		// any non-Successful outcome is a peer error.
		log.Warnf("Write-Replace Warning Response from %s: unexpected envelope type %s",
			gnb.GnbIP, env.Type)
		return
	}

	var msg genngap.WriteReplaceWarningResponse
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("WriteReplaceWarningResponse decode from %s: %v", gnb.GnbIP, err)
		return
	}

	var msgID, serial uint16
	var completedPresent bool
	var completedSummary string
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
		case int64(genngap.IdBroadcastCompletedAreaList):
			if ie.Value.BroadcastCompletedAreaList != nil {
				completedPresent = true
				completedSummary = formatCompleted(ie.Value.BroadcastCompletedAreaList)
			}
		}
	}

	if !completedPresent {
		// §8.9.1.2 line 7681-7682 verbatim: AMF shall consider the
		// broadcast unsuccessful in all cells within the NG-RAN node.
		log.Warnf("Write-Replace Warning Response from %s msgID=%d serial=%d: BroadcastCompletedAreaList absent — broadcast unsuccessful in all cells (TS 38.413 §8.9.1.2)",
			gnb.GnbIP, msgID, serial)
		return
	}
	log.Infof("Write-Replace Warning Response from %s msgID=%d serial=%d completed=%s",
		gnb.GnbIP, msgID, serial, completedSummary)
}

// bitsToU16 unpacks a 16-bit BIT STRING to uint16 (Message Identifier
// / Serial Number). Both are §9.3.1 fixed-length 16-bit fields.
func bitsToU16(m genngap.MessageIdentifier) uint16 {
	b := runtime.BitString(m).Bytes
	if len(b) < 2 {
		return 0
	}
	return uint16(b[0])<<8 | uint16(b[1])
}

// formatCompleted renders the §9.3.1.43 CHOICE for log output.
func formatCompleted(c *genngap.BroadcastCompletedAreaList) string {
	switch c.Present {
	case genngap.BroadcastCompletedAreaListPresentCellIDBroadcastNR:
		if c.CellIDBroadcastNR == nil {
			return "nr-cells:0"
		}
		return fmt.Sprintf("nr-cells:%d", len(*c.CellIDBroadcastNR))
	case genngap.BroadcastCompletedAreaListPresentTAIBroadcastNR:
		if c.TAIBroadcastNR == nil {
			return "nr-tais:0"
		}
		return fmt.Sprintf("nr-tais:%d", len(*c.TAIBroadcastNR))
	case genngap.BroadcastCompletedAreaListPresentEmergencyAreaIDBroadcastNR:
		if c.EmergencyAreaIDBroadcastNR == nil {
			return "nr-emerg:0"
		}
		return fmt.Sprintf("nr-emerg:%d", len(*c.EmergencyAreaIDBroadcastNR))
	case genngap.BroadcastCompletedAreaListPresentCellIDBroadcastEUTRA:
		return "eutra-cells"
	case genngap.BroadcastCompletedAreaListPresentTAIBroadcastEUTRA:
		return "eutra-tais"
	case genngap.BroadcastCompletedAreaListPresentEmergencyAreaIDBroadcastEUTRA:
		return "eutra-emerg"
	}
	return "unknown"
}

// Register installs HandleResponse on the AMF dispatcher.
func Register() {
	ngap.Register(ngap.ProcCodeWriteReplaceWarning, HandleResponse)
}

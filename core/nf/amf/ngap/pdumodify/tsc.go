// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Rel-19 TSC N2 signalling for network-initiated PDU session
// modification. When the PCF adds a TSC QoS flow after establishment
// (the CNC / TSCTSF path: establish plain → configure stream), the SMF
// derives TSCAI (TS 23.501 §5.27.2.4) and must deliver it to the gNB
// over the N2 leg of the modification (TS 23.502 §4.3.3 step 3 →
// TS 38.413 PDU Session Resource Modify Request). The N1 leg (NAS to
// the UE) already ships via session.DLNASByIMSI; this file adds the
// missing N2 leg carrying TSCTrafficCharacteristics on the
// QosFlowAddOrModifyRequestList (protocol extension id 196).
package pdumodify

import (
	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pdusetup"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// BuildTSCModifyTransfer builds a PDUSessionResourceModifyRequestTransfer
// (TS 38.413 §9.3.4.9) whose QosFlowAddOrModifyRequestList carries the
// TSCTrafficCharacteristics of every TSC QoS flow in tscai
// ([QFI] = [UL, DL]). Returns nil when no flow has usable TSCAI.
func BuildTSCModifyTransfer(tscai map[uint8][2]*session.TSCAI) []byte {
	list := genngap.QosFlowAddOrModifyRequestList{}
	for qfi, ai := range tscai {
		tc := pdusetup.BuildTSCTrafficCharacteristics(ai[0], ai[1])
		if tc == nil {
			continue
		}
		ext := []genngap.QosFlowAddOrModifyRequestItemExtIEsEntry{{
			Id:          genngap.ProtocolExtensionID(genngap.IdTSCTrafficCharacteristics),
			Criticality: genngap.CriticalityIgnore,
			Value: genngap.QosFlowAddOrModifyRequestItemExtIEsValue{
				Present:                   genngap.QosFlowAddOrModifyRequestItemExtIEsValuePresentTSCTrafficCharacteristics,
				TSCTrafficCharacteristics: tc,
			},
		}}
		list = append(list, genngap.QosFlowAddOrModifyRequestItem{
			QosFlowIdentifier: genngap.QosFlowIdentifier(int64(qfi)),
			IEExtensions:      &ext,
		})
	}
	if len(list) == 0 {
		return nil
	}
	transfer := &genngap.PDUSessionResourceModifyRequestTransfer{
		ProtocolIEs: []genngap.PDUSessionResourceModifyRequestTransferIEsEntry{{
			Id:          genngap.ProtocolIEID(genngap.IdQosFlowAddOrModifyRequestList),
			Criticality: genngap.CriticalityReject,
			Value: genngap.PDUSessionResourceModifyRequestTransferIEsValue{
				Present:                       genngap.PDUSessionResourceModifyRequestTransferIEsValuePresentQosFlowAddOrModifyRequestList,
				QosFlowAddOrModifyRequestList: &list,
			},
		}},
	}
	b, err := transfer.MarshalAPER()
	if err != nil {
		logger.Get("amf.ngap.pdumodify").Warnf("TSC modify transfer marshal: %v", err)
		return nil
	}
	return b
}

// SendTSCModify resolves the UE + serving gNB and sends a PDU Session
// Resource Modify Request carrying the TSCAI for the session's TSC
// flows. Wired to session.NGAPModifyTSCByIMSI from nf/amf/hooks.go.
// Best-effort: a missing UE/gNB or no-TSCAI is a no-op (nil).
func SendTSCModify(imsi string, pduSessionID uint8, tscai map[uint8][2]*session.TSCAI) error {
	log := logger.Get("amf.ngap.pdumodify").WithIMSI(imsi)
	transfer := BuildTSCModifyTransfer(tscai)
	if transfer == nil {
		return nil // nothing to signal
	}
	ue := uectx.Default.LookupByIMSI(imsi)
	if ue == nil {
		log.Warnf("SendTSCModify: no AMF UE context — UE not registered?")
		return nil
	}
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.Warnf("SendTSCModify: no gNB for GnbKey=%q", ue.GnbKey)
		return nil
	}
	if err := SendRequest(gnb, ue, []ModifyItem{{
		PDUSessionID: pduSessionID,
		TransferBytes: transfer,
	}}); err != nil {
		log.Warnf("SendTSCModify: %v", err)
		return err
	}
	log.Infof("N2 PDU Session Resource Modify sent with TSCAI pduSessID=%d flows=%d (TS 23.502 §4.3.3)",
		pduSessionID, len(tscai))
	return nil
}

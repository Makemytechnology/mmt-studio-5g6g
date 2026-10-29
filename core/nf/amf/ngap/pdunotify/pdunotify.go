// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// PDU Session Resource Notify (TS 38.413 §8.2.4) — NG-RAN → AMF.
//
// The gNB notifies per-QoS-flow events. For Rel-19 TSC the interesting
// payload is the TSCTrafficCharacteristicsFeedback protocol extension
// on QosFlowNotifyItem (id 393): the RAN's Burst Arrival Time offset
// (and optionally an adjusted periodicity) for a TSC flow — the
// EnTSCAC feedback of TS 23.501 §5.27.2.5. The AMF forwards it to the
// SMF, which reports BAT_OFFSET_INFO to the PCF (TS 29.512), which
// fans it out to the TSN AF so the CNC can realign the talker.
package pdunotify

import (
	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Register wires the §8.2.4 handler into the NGAP dispatcher. Called
// from nf/amf.StartNGAP alongside the other procedure registrations.
func Register() {
	ngap.Register(ngap.ProcCodePDUSessionResourceNotify, handleNotify)
}

func handleNotify(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.pdunotify")
	if env.Type != wire.InitiatingMessage {
		return
	}
	var msg genngap.PDUSessionResourceNotify
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("PDUSessionResourceNotify decode from %s: %v", gnb.GnbIP, err)
		return
	}

	var amfUeID int64
	var list *genngap.PDUSessionResourceNotifyList
	for i := range msg.ProtocolIEs {
		ie := &msg.ProtocolIEs[i]
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				amfUeID = int64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdPDUSessionResourceNotifyList):
			list = ie.Value.PDUSessionResourceNotifyList
		}
	}
	if list == nil {
		return
	}
	ue := uectx.Default.LookupByAmfID(amfUeID)
	if ue == nil || ue.IMSI == "" {
		log.Warnf("PDUSessionResourceNotify: no UE for amfUeID=%d", amfUeID)
		return
	}

	for _, item := range *list {
		var xfer genngap.PDUSessionResourceNotifyTransfer
		if err := xfer.UnmarshalAPER(item.PDUSessionResourceNotifyTransfer); err != nil {
			log.Warnf("Notify transfer decode pduSessID=%d: %v", item.PDUSessionID, err)
			continue
		}
		if xfer.QosFlowNotifyList == nil {
			continue
		}
		for _, fl := range *xfer.QosFlowNotifyList {
			fb := tscFeedbackOf(&fl)
			if fb == nil {
				continue
			}
			// TS 38.413 §9.3.1.x: BurstArrivalTimeOffset and
			// Periodicity are expressed in microseconds.
			report := func(fi *genngap.TSCFeedbackInformation) {
				if fi == nil {
					return
				}
				var adj uint64
				if fi.AdjustedPeriodicity != nil {
					adj = uint64(*fi.AdjustedPeriodicity)
				}
				session.ReportBATOffset(ue.IMSI, uint8(item.PDUSessionID),
					uint8(fl.QosFlowIdentifier), int64(fi.BurstArrivalTimeOffset)*1000, adj)
			}
			report(fb.TSCFeedbackInformationDL)
			report(fb.TSCFeedbackInformationUL)
			log.WithIMSI(ue.IMSI).Infof(
				"TSC feedback pduSessID=%d QFI=%d (TS 23.501 §5.27.2.5 EnTSCAC)",
				item.PDUSessionID, fl.QosFlowIdentifier)
		}
	}
}

// tscFeedbackOf extracts the id-393 TSCTrafficCharacteristicsFeedback
// extension from a QosFlowNotifyItem, or nil.
func tscFeedbackOf(fl *genngap.QosFlowNotifyItem) *genngap.TSCTrafficCharacteristicsFeedback {
	if fl.IEExtensions == nil {
		return nil
	}
	for i := range *fl.IEExtensions {
		ext := &(*fl.IEExtensions)[i]
		if int64(ext.Id) == int64(genngap.IdTSCTrafficCharacteristicsFeedback) {
			return ext.Value.TSCTrafficCharacteristicsFeedback
		}
	}
	return nil
}

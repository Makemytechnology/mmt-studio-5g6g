// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package dlnas — Downlink NAS Transport (TS 38.413 §8.6.2).
//
// This is the AMF's *outbound* NAS channel. The handler below is only
// installed to log PDUs the AMF occasionally receives back in corner cases;
// normal flow has the AMF calling Send from GMM handlers that need to ship
// a downlink NAS message.
//
// Send wraps the NAS PDU in a DownlinkNASTransport envelope and hands it
// to the gNB on the UE-associated SCTP stream derived from AMF-UE-NGAP-ID.
package dlnas

import (
	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// WrapDL is set by the gmm package at init() to the real NAS security
// wrapper (secureWrap). When no security context is active it returns
// the input unchanged. Leaving it nil (tests) is the identity function.
//
// Hook pattern avoids the import cycle: gmm imports dlnas for Send(),
// so dlnas cannot import gmm to call SecureWrapDL directly.
var WrapDL func(ue *uectx.AmfUeCtx, plain []byte) ([]byte, error)

// Send pushes a downlink NAS PDU to the gNB as a DownlinkNASTransport PDU
// (TS 38.413 §8.6.2).
//
// Mandatory IEs (Table 8.6.2.2-1): AMFUENGAPID (M), RANUENGAPID (M),
// NASPDU (M). The three IEs our build includes.
//
// Conditional / optional IEs we do NOT populate yet; each is guarded
// by the spec clause that says when it shall be included:
//
// TODO(spec: TS 38.413 §8.6.2.2 "UE Aggregate Maximum Bit Rate") —
//   "should be sent to the NG-RAN node if the AMF has not sent it
//   previously." Commonly attached to the first DL NAS after SMC so
//   the gNB has UE-AMBR before any PDU session resource setup. Needs
//   a per-UE "sent UE-AMBR?" flag + UDM subscription read.
//
// TODO(spec: TS 38.413 §8.6.2.2 "Mobility Restriction List") —
//   include when roaming / forbidden TAI / service area restriction
//   applies to the UE. Without it the gNB treats "no roaming and no
//   access restriction apply", which is fine for our dev config but
//   wrong in production.
//
// TODO(spec: TS 38.413 §8.6.2.2 "Index to RAT/Frequency Selection Priority") —
//   operator-configured priority steering for idle-mode cell selection.
//
// TODO(spec: TS 38.413 §8.6.2.2 "Old AMF" / "Extended Old AMF") —
//   include on N14 redirection after UE Context Transfer so the gNB
//   knows this logical NG-connection moved between AMFs.
//
// TODO(spec: TS 38.413 §8.6.2.2 "RAN Paging Priority") —
//   for MT-SMS / MT services with priority, drives RRC_INACTIVE paging
//   priority at the gNB.
//
// TODO(spec: TS 38.413 §8.6.2.2 "End Indication") —
//   set "no further data" for the final DL NAS of a burst (e.g. after
//   Registration Accept when no further follow-up is expected).
func Send(gnb *gnbctx.GnbCtx, ue *uectx.AmfUeCtx, nasPDU []byte) error {
	log := logger.Get("amf.ngap.dlnas")

	if WrapDL != nil {
		wrapped, err := WrapDL(ue, nasPDU)
		if err != nil {
			log.Errorf("DL NAS secure wrap amfUeID=%d: %v", ue.AmfUeNGAPID, err)
			return err
		}
		nasPDU = wrapped
	}

	amfID := genngap.AMFUENGAPID(ue.AmfUeNGAPID)
	ranID := genngap.RANUENGAPID(ue.RanUeNGAPID)
	nas := genngap.NASPDU(nasPDU)

	msg := genngap.DownlinkNASTransport{
		ProtocolIEs: []genngap.DownlinkNASTransportIEsEntry{
			{
				Id:          genngap.ProtocolIEID(genngap.IdAMFUENGAPID),
				Criticality: genngap.CriticalityReject,
				Value: genngap.DownlinkNASTransportIEsValue{
					Present:     genngap.DownlinkNASTransportIEsValuePresentAMFUENGAPID,
					AMFUENGAPID: &amfID,
				},
			},
			{
				Id:          genngap.ProtocolIEID(genngap.IdRANUENGAPID),
				Criticality: genngap.CriticalityReject,
				Value: genngap.DownlinkNASTransportIEsValue{
					Present:     genngap.DownlinkNASTransportIEsValuePresentRANUENGAPID,
					RANUENGAPID: &ranID,
				},
			},
			{
				Id:          genngap.ProtocolIEID(genngap.IdNASPDU),
				Criticality: genngap.CriticalityReject,
				Value: genngap.DownlinkNASTransportIEsValue{
					Present: genngap.DownlinkNASTransportIEsValuePresentNASPDU,
					NASPDU:  &nas,
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
		ProcedureCode: ngap.ProcCodeDownlinkNASTransport,
		Criticality:   wire.CriticalityIgnore,
		Value:         inner,
	})
	if err != nil {
		return err
	}
	stream := gnb.UEStream(ue.AmfUeNGAPID)
	if err := gnb.Send(pdu, stream); err != nil {
		log.Errorf("DL NAS send amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return err
	}
	log.Debugf("DL NAS sent amfUeID=%d stream=%d %dB", ue.AmfUeNGAPID, stream, len(nasPDU))
	return nil
}

// Handle is registered only so we log if a gNB ever echoes the PDU back.
func Handle(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	logger.Get("amf.ngap.dlnas").
		Warnf("DownlinkNASTransport received from gNB %s (%d bytes stream=%d) — unexpected on AMF side",
			gnb.GnbIP, len(env.Value), stream)
}

// Register installs the receive-side handler.
func Register() { ngap.Register(ngap.ProcCodeDownlinkNASTransport, Handle) }

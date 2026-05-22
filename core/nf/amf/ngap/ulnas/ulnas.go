// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ulnas — Uplink NAS Transport (TS 38.413 §8.6.3).
//
// Go port of nf/amf/ngap/ngap_uplink_nas_transport.py. The gNB sends this
// to deliver uplink NAS PDUs from the UE after the AMF-UE-NGAP-ID is known
// (i.e. after Initial UE Message).
//
// Dispatch path: NGAP → ulnas.Handle → gmm.Dispatch → gmm procedure handler.
package ulnas

import (
	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/errind"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Handle is registered for procedureCode=46 (TS 38.413 §8.6.3).
//
// Mandatory IEs per Table 8.6.3.2-1: AMFUENGAPID (M), RANUENGAPID (M),
// NASPDU (M), UserLocationInformation (M). Our current implementation
// extracts the first three; UserLocationInformation is captured onto
// the UE ctx so mobility / paging target the cell the UE last signalled
// from.
//
// Spec abnormal conditions (§8.6.3.3): "If the NAS-PDU IE is not
// included, the AMF shall consider the procedure as failed." — today
// we log-and-drop; TODO for NGAP Error Indication.
func Handle(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.ulnas")

	var msg genngap.UplinkNASTransport
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		// APER-level decode failure means we can't determine which
		// IEs were even present. Per TS 38.413 v19.2.0 §8.7.5.1 send
		// an Error Indication with cause protocol/transferSyntaxError;
		// AMF/RAN UE NGAP IDs unavailable so the message rides as
		// non-UE-associated on stream 0.
		log.Errorf("Uplink NAS Transport decode from %s: %v", gnb.GnbIP, err)
		_ = errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolTransferSyntaxError))
		return
	}

	ies := extractIEs(&msg)
	if ies.NASPDU == nil {
		// TS 38.413 §8.6.3.3 abnormal: NAS-PDU IE is mandatory; the
		// AMF "shall consider the procedure as failed". Spec-correct
		// response per §8.7.5 is an NGAP Error Indication with cause
		// = protocol/abstract-syntax-error-reject.
		log.Errorf("Uplink NAS Transport missing NAS-PDU from %s amfUeID=%d ranUeID=%d",
			gnb.GnbIP, ies.AMFUeID, ies.RANUeID)
		_ = errind.Send(gnb, ies.AMFUeID, ies.RANUeID,
			errind.CauseProtocol(genngap.CauseProtocolAbstractSyntaxErrorReject))
		return
	}

	var ue *uectx.AmfUeCtx
	if ies.AMFUeID != 0 {
		ue = uectx.Default.LookupByAmfID(ies.AMFUeID)
	}
	if ue == nil && ies.RANUeID != 0 {
		ue = uectx.Default.LookupByRanKey(gnb.GnbIP, ies.RANUeID)
	}
	if ue == nil {
		// TS 38.413 §8.7.5 — unknown AMF-UE-NGAP-ID on a UE-associated
		// inbound PDU → Error Indication with cause
		// radioNetwork/unknown-local-UE-NGAP-ID so the gNB drops its
		// half of the stale association and cleans up.
		log.Warnf("UplinkNASTransport for unknown UE amfUeID=%d ranUeID=%d gNB=%s — sending Error Indication",
			ies.AMFUeID, ies.RANUeID, gnb.GnbIP)
		_ = errind.Send(gnb, ies.AMFUeID, ies.RANUeID,
			errind.CauseRadio(genngap.CauseRadioNetworkUnknownLocalUENGAPID))
		return
	}

	// Fire the NGAP FSM event — self-loop in every non-RELEASED state.
	// A "no transition" warning from the FSM here means we received UL
	// NAS for a UE whose association is already gone; dispatcher should
	// drop rather than create a new context.
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(fk).Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvUplinkNASTransport})

	// TS 38.413 §9.2.2.2 UserLocationInformation — update cached
	// cell/TAC so mobility decisions (and paging target selection)
	// track the UE's current cell, not the stale one from the last
	// InitialUEMessage. The NR variant is what we support; N3IWF /
	// EUTRA variants fall through to leaving the cached value alone.
	if ies.UserLocationPLMN != nil {
		ue.UserLocationPLMN = ies.UserLocationPLMN
	}
	if ies.UserLocationTAC != nil {
		ue.UserLocationTAC = ies.UserLocationTAC
	}
	if ies.NRCellIdentity != nil {
		ue.UserLocationNRCGI = ies.NRCellIdentity
	}

	// UL NAS from a previously-paged UE = UE is reachable again. Cancel
	// T3513 retransmit so we stop broadcasting PAGE.
	ngap.CancelT3513ForUE(ue.AmfUeNGAPID)

	if err := gmm.Dispatch(ue, ies.NASPDU); err != nil {
		log.Warnf("5GMM dispatch error (amfUeID=%d): %v", ue.AmfUeNGAPID, err)
	}
}

// ulnasIEs aggregates the IEs extractIEs returns.
type ulnasIEs struct {
	AMFUeID, RANUeID  int64
	NASPDU            []byte
	UserLocationPLMN  []byte
	UserLocationTAC   []byte
	NRCellIdentity    []byte
}

func extractIEs(m *genngap.UplinkNASTransport) ulnasIEs {
	var r ulnasIEs
	for i := range m.ProtocolIEs {
		ie := &m.ProtocolIEs[i]
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				r.AMFUeID = int64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				r.RANUeID = int64(*ie.Value.RANUENGAPID)
			}
		case int64(genngap.IdNASPDU):
			if ie.Value.NASPDU != nil {
				r.NASPDU = []byte(*ie.Value.NASPDU)
			}
		case int64(genngap.IdUserLocationInformation):
			if uli := ie.Value.UserLocationInformation; uli != nil &&
				uli.Present == genngap.UserLocationInformationPresentUserLocationInformationNR &&
				uli.UserLocationInformationNR != nil {
				nr := uli.UserLocationInformationNR
				r.UserLocationPLMN = append([]byte(nil), []byte(nr.TAI.PLMNIdentity)...)
				r.UserLocationTAC = append([]byte(nil), []byte(nr.TAI.TAC)...)
				if nr.NRCGI.NRCellIdentity.Bytes != nil {
					r.NRCellIdentity = append([]byte(nil), nr.NRCGI.NRCellIdentity.Bytes...)
				}
			}
		}
	}
	return r
}

// Register installs Handle on the AMF-wide NGAP dispatcher.
func Register() { ngap.Register(ngap.ProcCodeUplinkNASTransport, Handle) }

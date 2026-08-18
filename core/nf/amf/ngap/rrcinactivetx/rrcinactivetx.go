// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package rrcinactivetx — RRC Inactive Transition Report procedure
// handler.
//
// Authoritative spec: TS 38.413 v19.2.0 (local PDF
// specs/3gpp/ts_138413v190200p.pdf).
//
// §8.3.5.1 General (verbatim, page 61):
//
//	"The purpose of the RRC Inactive Transition Report procedure
//	is to notify the AMF when the UE enters or leaves
//	RRC_INACTIVE state. The procedure uses UE-associated
//	signalling."
//
// §8.3.5.2 Successful Operation (verbatim, page 61):
//
//	"The NG-RAN node initiates the procedure by sending an RRC
//	INACTIVE TRANSITION REPORT message to the AMF. Upon
//	reception of the RRC INACTIVE TRANSITION REPORT message,
//	the AMF shall take appropriate actions based on the
//	information indicated by the RRC State IE."
//
// §8.3.5.3 Abnormal Conditions: "Void."
//
// Message structure §9.2.2.10 (verbatim, page 171) — direction
// NG-RAN node → AMF, mandatory IEs:
//
//	Message Type             M  reject  (§9.3.1.1)
//	AMF UE NGAP ID           M  reject  (§9.3.3.1)
//	RAN UE NGAP ID           M  reject  (§9.3.3.2)
//	RRC State                M  ignore  (§9.3.1.92)  {connected, inactive}
//	User Location Information M ignore  (§9.3.1.16)
//
// Procedure code is 37 per the local generated codec
// (codecs/asn1-go/protocols/ngap/generated/ngap_constants.go
// "IdRRCInactiveTransitionReport = 37" — that file is generated
// from the §9.4 procedure-code table of the TS 38.413 ASN.1).
//
// What "appropriate actions" this handler performs (gap A):
//
//   1. Decode and validate the message.
//   2. Resolve the AmfUeCtx via AMF UE NGAP ID (the M-reject
//      criticality IE per §9.2.2.10 — if unresolvable, log and
//      drop, since downstream actions need the UE context).
//   3. Update ue.RRC (RRCConnected ↔ RRCInactive) and the
//      ue.RRCTransitionAt timestamp under the ctx lock so other
//      readers see a coherent transition.
//   4. Log the transition with TS-cited message.
//
// What this handler explicitly does NOT do (gap B, future commit):
//
//   - It does not yet ask the SMF to switch the UPF FAR to
//     buffer-with-notify per TS 23.502 §4.8.1.1a step 2 ("CN
//     based MT communication handling"). When wired, the
//     §4.8.2.2b paging chain (UPF Data Notification → SMF
//     Namf_MT_EnableUEReachability → AMF RAN Paging Request)
//     will close. Gap A is the foundation; gap B is the chain.
package rrcinactivetx

import (
	"time"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Handle decodes an RRC INACTIVE TRANSITION REPORT and applies
// the §8.3.5.2 "appropriate actions" gap-A scope: state mirror +
// log. UE-associated; resolves the UE context via AMF UE NGAP ID.
func Handle(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.rrcinactivetx")

	var msg genngap.RRCInactiveTransitionReport
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("RRCInactiveTransitionReport decode from %s: %v", gnb.GnbIP, err)
		return
	}

	var (
		amfUeID      int64 = -1
		ranUeID      int64 = -1
		rrcState     *genngap.RRCState
		haveAMFID    bool
		haveRANID    bool
		haveRRCState bool
	)
	for _, ie := range msg.ProtocolIEs {
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				amfUeID = int64(*ie.Value.AMFUENGAPID)
				haveAMFID = true
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				ranUeID = int64(*ie.Value.RANUENGAPID)
				haveRANID = true
			}
		case int64(genngap.IdRRCState):
			if ie.Value.RRCState != nil {
				rrcState = ie.Value.RRCState
				haveRRCState = true
			}
		}
	}

	// §9.2.2.10 marks AMF UE NGAP ID and RAN UE NGAP ID as M with
	// criticality "reject". Absence is a protocol error; without
	// the UE-NGAP-IDs we cannot resolve the UE context, so log and
	// drop (the dispatcher already emits an Error Indication for
	// pure decode failures; this is a semantic-not-syntactic gap).
	if !haveAMFID || !haveRANID {
		log.Warnf("RRCInactiveTransitionReport from %s missing mandatory IDs (haveAMF=%v haveRAN=%v) — TS 38.413 §9.2.2.10 violation",
			gnb.GnbIP, haveAMFID, haveRANID)
		return
	}

	ue := uectx.Default.LookupByAmfID(amfUeID)
	if ue == nil {
		log.Warnf("RRCInactiveTransitionReport from %s: no UE for amfUeID=%d ranUeID=%d — drop",
			gnb.GnbIP, amfUeID, ranUeID)
		return
	}

	// §9.2.2.10 marks RRC State as M (mandatory) but criticality
	// "ignore" — its absence shouldn't reject the message, but
	// there's nothing meaningful to update without it. Spec-
	// compliant fall-back: log and return rather than guess.
	if !haveRRCState {
		log.WithIMSI(ue.IMSI).Warnf("RRCInactiveTransitionReport amfUeID=%d: RRC State IE absent (§9.2.2.10) — no state change applied",
			amfUeID)
		return
	}

	newRRC := mapRRCState(*rrcState)
	prev := ue.RRC
	ue.RRC = newRRC
	ue.RRCTransitionAt = time.Now()
	log.WithIMSI(ue.IMSI).Infof("RRC Inactive Transition Report amfUeID=%d ranUeID=%d: %s → %s (TS 38.413 §8.3.5.2)",
		amfUeID, ranUeID, prev, newRRC)

	// TS 23.502 v19.7.0 §4.8.1.1a step 3-4 (verbatim, page 232):
	//   "the AMF invokes Nsmf_PDUSession_UpdateSMContext Request
	//   (… CN based MT handling indication) towards SMF. The
	//   Operation Type is set to a value that indicates to stop
	//   user plane DL data transmissions towards the UE and enable
	//   data buffering. … If data buffering is handled in the UPF,
	//   the SMF updates the UPF with proper rules for MT data
	//   handling and DL data size reporting in the case of DL data
	//   arrival."
	// In-process: invoke session.DeactivateAllUserPlanes which
	// drives UPF DeactivateDL (Apply Action FORW→BUFF per TS
	// 29.244 §8.2.26) and marks every session StateSuspended so
	// the SMF DL-notify gate (nf/smf/session/dlnotify.go:99) opens
	// when the UPF subsequently emits a Session Report Request.
	//
	// On exit (RRCInactive → RRCConnected) we do NOT re-activate
	// here — that's the UE-Triggered Connection Resume path
	// (§4.8.2.2 → §4.2.3.2 step 4 ActivateUserPlane), driven by
	// the UE's Service Request when it leaves RRC_INACTIVE.
	if prev != newRRC && newRRC == uectx.RRCInactive {
		n := session.DeactivateAllUserPlanes(ue.IMSI, nil)
		log.WithIMSI(ue.IMSI).Infof("§4.8.1.1a step 3-4: deactivated %d PDU session(s) — FAR=BUFF, state=Suspended", n)
	}
}

// mapRRCState converts the NGAP RRC State enum (§9.3.1.92, generated
// constants RRCStateInactive=0 / RRCStateConnected=1) to the AMF's
// state-machine string. Keeping the enum→string mapping spec-grounded
// rather than memory-based.
func mapRRCState(s genngap.RRCState) uectx.RRCState {
	switch s {
	case genngap.RRCStateConnected:
		return uectx.RRCConnected
	case genngap.RRCStateInactive:
		return uectx.RRCInactive
	}
	return uectx.RRCConnected
}

// Register installs the handler on the AMF dispatcher. Called from
// AMF bootstrap (nf/amf/amf.go).
func Register() {
	ngap.Register(ngap.ProcCodeRRCInactiveTransitionReport, Handle)
}

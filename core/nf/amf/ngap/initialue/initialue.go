// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package initialue — Initial UE Message handler (TS 38.413 §8.6.1).
//
// Go port of nf/amf/ngap/ngap_initial_ue_message.py.
//
// InitialUEMessage is the first UE-associated NGAP message — it's how a
// gNB hands an uplink NAS PDU to the AMF (typically the UE's Registration
// Request, Service Request, or Deregistration Request). The handler:
//
//  1. Decodes the NGAP PDU via the generated codec.
//  2. Extracts the mandatory IEs: RAN-UE-NGAP-ID (85), NAS-PDU (38),
//     UserLocationInformation (121), RRCEstablishmentCause (90); and
//     the optional UEContextRequest (112) + 5G-S-TMSI (26).
//  3. Looks up an existing `AmfUeCtx` by (RAN-UE-NGAP-ID, gNB). If not
//     found (first contact from a fresh UE), allocates an AMF-UE-NGAP-ID
//     and creates a new context.
//  4. Hands the NAS PDU to `gmm.Dispatch` for 5GMM processing.
//
// The NGAP PDU arrives on a non-zero SCTP stream (UE-associated signalling,
// TS 38.412 §7). The downstream dispatcher maps AMF-UE-NGAP-ID → stream
// when responding with DownlinkNASTransport.
package initialue

import (
	"fmt"

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

// Handle is registered for procedureCode=15. See TS 38.413 §8.6.1.
//
// Mandatory IEs per Table 8.6.1.2-1:
//   RAN-UE-NGAP-ID (M), NAS-PDU (M), UserLocationInformation (M),
//   RRCEstablishmentCause (M).
// Optional IEs commonly seen in registration:
//   5G-S-TMSI, UEContextRequest, AMFSetID, AllowedNSSAI,
//   RequestedNSSAI, GUAMI, GUAMIType, PartiallyAllowedNSSAI,
//   SourceToTargetAMFInformationReroute (rerouting), CE-mode-B,
//   LTE-M, RedCap / eRedCap, IABNodeIndication, …
//
// Spec-mandated AMF behaviours (§8.6.1.2 / §8.6.1.3):
//   - "If the UE Context Request IE is included … the AMF shall
//     trigger an Initial Context Setup procedure towards the
//     NG-RAN node." — driven by the downstream GMM flow.
//   - "If the Allowed NSSAI IE is included … the AMF shall use the
//     IE as defined in TS 23.502 [10]." — TODO.
//   - §8.6.1.3: "If the 5G-S-TMSI is not received … whereas
//     expected, the AMF shall consider the procedure as failed." —
//     TODO: fire NGAP Error Indication + drop.
func Handle(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.initialue")

	var msg genngap.InitialUEMessage
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("Initial UE Message decode from %s: %v — sending Error Indication", gnb.GnbIP, err)
		_ = errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolTransferSyntaxError))
		return
	}

	ies := extractIEs(&msg)
	// Mandatory IEs per ASN.1 NGAP-PDU-Contents.asn InitialUEMessage-IEs
	// (criticality=reject / ignore, PRESENCE mandatory):
	//   RAN-UE-NGAP-ID, NAS-PDU, UserLocationInformation, RRCEstablishmentCause.
	// Missing any mandatory-reject IE is a protocol error per TS 38.413
	// §10; per §8.7.2 the response is NGAP Error Indication.
	if ies.RanUeID == 0 || len(ies.NASPDU) == 0 {
		log.Errorf("Initial UE Message missing mandatory IEs (ranUeID=%d nasPDU=%d) from %s — sending Error Indication",
			ies.RanUeID, len(ies.NASPDU), gnb.GnbIP)
		_ = errind.Send(gnb, 0, ies.RanUeID,
			errind.CauseProtocol(genngap.CauseProtocolAbstractSyntaxErrorReject))
		return
	}
	if len(ies.UserLocationTAC) == 0 {
		// UserLocationInformation is PRESENCE mandatory with
		// CRITICALITY reject. Log and continue — NR-only decode path
		// may be the gap (we silently skip N3IWF / EUTRA variants);
		// hard-reject when the live UE fleet has stabilised on NR.
		log.Warnf("Initial UE Message UserLocationInformation absent or non-NR from %s ranUeID=%d — proceeding; spec mandates rejection per §8.6.1.3",
			gnb.GnbIP, ies.RanUeID)
	}
	log.Infof("InitialUEMessage gNB=%s ranUeID=%d cause=%q ueCtxReq=%v nas=%dB tmsi=%s cell=%s",
		gnb.GnbIP, ies.RanUeID, ies.RRCCause, ies.UEContextReq, len(ies.NASPDU),
		fmt5GSTMSI(ies.FiveGSTMSI), fmtNRCellHex(ies.NRCellIdentity))

	// Find or allocate AMF-UE-NGAP-ID.
	//
	// Lookup priority:
	//   1. (gNB, RAN-UE-NGAP-ID) — hits when the RRC connection
	//      survived (rare for a new InitialUEMessage; gNB re-allocates
	//      RAN-UE-NGAP-ID on each new RRC).
	//   2. 5G-S-TMSI (TS 38.413 §9.2.5.1 Initial UE Message IE list:
	//      "5G-S-TMSI … Included if the UE has a valid 5G-S-TMSI"):
	//      decode the 32-bit 5G-TMSI per TS 23.003 §2.10.1 (5G-S-TMSI
	//      = AMFSetID(10) | AMFPointer(6) | 5G-TMSI(32); the 5G-TMSI
	//      alone is unique within the AMF Set) and look up the cached
	//      UE ctx. Hits when the UE is returning from CM-IDLE via
	//      Service Request (TS 24.501 §5.6.1) or mobility registration
	//      (TS 24.501 §5.5.1.3) with the same TMSI the AMF assigned on
	//      the prior registration.
	//   3. Allocate fresh — UE is unknown to this AMF (first Initial
	//      Registration, or stale/collision TMSI the registry has
	//      since purged).
	//
	// Without step 2, a Service Request flow creates a brand-new ctx
	// with RM=DEREGISTERED (the default on New()), even though the UE
	// is actually RM-REGISTERED on the network side — and the release
	// handler later misreads that as a silent dereg
	// (TS 33.501 §6.8.1.1.1 case 2.a.ii / 2.b branch) while in fact
	// no deregistration ever happened.
	ue := uectx.Default.LookupByRanKey(gnb.GnbIP, ies.RanUeID)
	if ue == nil && ies.FiveGSTMSI != nil && len(ies.FiveGSTMSI.FiveGTMSI) >= 4 {
		var tmsi uint32
		for _, b := range ies.FiveGSTMSI.FiveGTMSI[:4] {
			tmsi = (tmsi << 8) | uint32(b)
		}
		if found := uectx.Default.LookupByTMSI(tmsi); found != nil {
			ue = found
			// RAN re-allocated ranUeID + may have handed us off to a
			// different gNB cell. Retarget the cached ctx so NGAP
			// lookups and DL sends use the current radio leg.
			ue.RanUeNGAPID = ies.RanUeID
			ue.GnbKey = gnb.GnbIP

			// TS 38.413 v19.2.0 §8.4 "UE-associated NG signalling
			// connection" (specs/3gpp/ts_138413v190200p.pdf):
			//   "A UE-associated logical NG-connection is used to
			//    convey signalling messages between an NG-RAN node
			//    and an AMF for a specific UE. The UE-associated
			//    logical NG-connection is identified by the AMF UE
			//    NGAP ID and RAN UE NGAP ID at the AMF and NG-RAN
			//    node respectively."
			// Every InitialUEMessage brings a fresh RAN-UE-NGAP-ID
			// — i.e. a NEW logical NG-connection on the NG-RAN
			// side. The prior per-UE NGAP FSM (keyed on AMF-UE-
			// NGAP-ID, which we preserve) may have landed in
			// StateReleased on a prior ICS Failure / UE Context
			// Release; that terminal state isn't valid for the new
			// NG-associated connection. Reset the FSM to
			// NotEstablished so this fresh connection's transitions
			// (ICSRequestSent, Release, …) fire cleanly. Drop() is
			// safe — ngapfsm.Of() recreates on first access.
			fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
			ngapfsm.Drop(fk)

			// TS 38.413 v19.2.0 §8.3.3.1 verbatim:
			//   "The purpose of the UE Context Release procedure is
			//    to enable the AMF to order the release of the UE-
			//    associated logical NG-connection due to various
			//    reasons, e.g., completion of a transaction between
			//    the UE and the 5GC, or release of the old UE-
			//    associated logical NG-connection when the UE has
			//    initiated the establishment of a new UE-associated
			//    logical NG-connection, etc."
			// The spec explicitly anticipates the race we hit in
			// production: gNB sends UEContextReleaseRequest for the
			// old NG-connection (cause radioNetwork(21)) and within
			// a few ms the same UE re-establishes RRC and a new
			// InitialUEMessage arrives with a fresh RAN-UE-NGAP-ID
			// on the same AMF-UE-NGAP-ID. The new logical NG-
			// connection is INDEPENDENT of any procedure still in
			// flight on the old one — including the UE Context
			// Release procedure that prompted this re-attach. So
			// reset the per-UE procedure-collision flags
			// (NGAPProc / GMMProc / GMMSub) and the cached retx PDU
			// at the same time we drop the NGAP FSM. Without this,
			// the procedure-collision guard in initialctxsetup.Send
			// (NGAPProc==UE_CONTEXT_RELEASE) blocks the §4.4
			// cached-context reuse path's ICS Send and the
			// registration handler falls back to a needless full
			// primary auth.
			ue.NGAPProc = uectx.NGAPProcNone
			ue.GMMProc = uectx.GMMProcNone
			ue.GMMSub = uectx.GMMSubNone
			ue.RetxNASPDU = nil

			log.WithIMSI(ue.IMSI).Infof("Reused AmfUeCtx amfUeID=%d via 5G-S-TMSI=0x%08X (RM=%s, CM=%s) ranUeID=%d gNB=%s — NGAP FSM + procedure flags reset per §8.4 / §8.3.3.1",
				ue.AmfUeNGAPID, tmsi, ue.RM, ue.CM, ies.RanUeID, gnb.GnbIP)
		}
	}
	if ue == nil {
		amfID := uectx.Default.AllocateAmfID()
		ue = uectx.New(amfID, ies.RanUeID, gnb.GnbIP, "")
		uectx.Default.Insert(ue)
		log.Infof("New AmfUeCtx allocated: amfUeID=%d ranUeID=%d gNB=%s", amfID, ies.RanUeID, gnb.GnbIP)
	} else if ue.AmfUeNGAPID != 0 {
		log.Debugf("Reusing AmfUeCtx amfUeID=%d for gNB=%s ranUeID=%d", ue.AmfUeNGAPID, gnb.GnbIP, ies.RanUeID)
	}
	ue.CM = uectx.CMConnected
	ue.UEContextRequest = ies.UEContextReq

	// TS 38.413 §9.2.2.2 UserLocationInformation — stash for mobility
	// / paging / OAM. NR cell identity is 36 bits left-justified into
	// 5 bytes on the wire; keep the raw bytes for upstream reporting.
	if ies.UserLocationPLMN != nil {
		ue.UserLocationPLMN = ies.UserLocationPLMN
	}
	if ies.UserLocationTAC != nil {
		ue.UserLocationTAC = ies.UserLocationTAC
	}
	if ies.NRCellIdentity != nil {
		ue.UserLocationNRCGI = ies.NRCellIdentity
	}

	// TODO(spec: TS 38.413 §8.6.1.2 "Allowed NSSAI" / "Partially Allowed NSSAI") —
	//   if ies.AllowedNSSAI set, drive NSSF selection with it (the UE
	//   has already been selecting slice-compatible routing); today NSSF
	//   selection re-runs from subscription.
	// TODO(spec: TS 38.413 §8.6.1.2 "AMF Set ID" rerouting) —
	//   if ies.AMFSetID set, the message was rerouted to us; follow
	//   TS 23.502 rerouting rules.
	// TODO(spec: TS 38.413 §8.6.1.3 "5G-S-TMSI expected but missing") —
	//   when the registration type implies 5G-GUTI should have been
	//   provided (periodic / mobility), absence is a procedure-failed
	//   abnormal case per spec. Needs registration-type awareness at
	//   this layer.

	if err := gmm.Dispatch(ue, ies.NASPDU); err != nil {
		log.Warnf("5GMM dispatch error (amfUeID=%d): %v", ue.AmfUeNGAPID, err)
	}
}

// extractedIEs groups the IEs the AMF reads off an InitialUEMessage.
// Only the fields we actually use today are listed; additions land
// with additional logic.
type extractedIEs struct {
	RanUeID           int64
	NASPDU            []byte
	RRCCause          string
	UEContextReq      bool
	FiveGSTMSI        *genngap.FiveGSTMSI
	UserLocationPLMN  []byte
	UserLocationTAC   []byte
	NRCellIdentity    []byte
}

// extractIEs pulls the IEs the AMF cares about out of the decoded message.
func extractIEs(m *genngap.InitialUEMessage) extractedIEs {
	var r extractedIEs
	for i := range m.ProtocolIEs {
		ie := &m.ProtocolIEs[i]
		switch int64(ie.Id) {
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				r.RanUeID = int64(*ie.Value.RANUENGAPID)
			}
		case int64(genngap.IdNASPDU):
			if ie.Value.NASPDU != nil {
				r.NASPDU = []byte(*ie.Value.NASPDU)
			}
		case int64(genngap.IdRRCEstablishmentCause):
			if ie.Value.RRCEstablishmentCause != nil {
				r.RRCCause = ie.Value.RRCEstablishmentCause.String()
			}
		case int64(genngap.IdUEContextRequest):
			r.UEContextReq = ie.Value.UEContextRequest != nil
		case int64(genngap.IdFiveGSTMSI):
			r.FiveGSTMSI = ie.Value.FiveGSTMSI
		case int64(genngap.IdUserLocationInformation):
			if uli := ie.Value.UserLocationInformation; uli != nil {
				r.UserLocationPLMN, r.UserLocationTAC, r.NRCellIdentity =
					decodeUserLocationNR(uli)
			}
		}
	}
	return r
}

// decodeUserLocationNR extracts (PLMN, TAC, NR-CGI) from the NR variant
// of UserLocationInformation (TS 38.413 §9.2.2.2). Returns nil slices
// for non-NR variants (N3IWF / EUTRA) — we don't track those cell types
// on the AMF UE context yet.
func decodeUserLocationNR(uli *genngap.UserLocationInformation) (plmn, tac, nrCell []byte) {
	if uli == nil || uli.Present != genngap.UserLocationInformationPresentUserLocationInformationNR {
		return nil, nil, nil
	}
	nr := uli.UserLocationInformationNR
	if nr == nil {
		return nil, nil, nil
	}
	plmn = append([]byte(nil), []byte(nr.TAI.PLMNIdentity)...)
	tac = append([]byte(nil), []byte(nr.TAI.TAC)...)
	// NRCellIdentity is a 36-bit BIT STRING, encoded in 5 octets on the
	// wire with the 4 low bits of the 5th octet as padding.
	if nr.NRCGI.NRCellIdentity.Bytes != nil {
		nrCell = append([]byte(nil), nr.NRCGI.NRCellIdentity.Bytes...)
	}
	return
}

// fmt5GSTMSI formats the 5G-S-TMSI for log output as "AMFSet/AMFPointer/TMSI".
// Returns "(none)" when the IE was absent.
func fmt5GSTMSI(t *genngap.FiveGSTMSI) string {
	if t == nil {
		return "(none)"
	}
	var tmsi int64
	if len(t.FiveGTMSI) >= 4 {
		for _, b := range t.FiveGTMSI[:4] {
			tmsi = (tmsi << 8) | int64(b)
		}
	}
	return fmt.Sprintf("amfSet=%v/ptr=%v/tmsi=0x%08X",
		t.AMFSetID, t.AMFPointer, tmsi)
}

// fmtNRCellHex renders the 36-bit NR Cell Identity (left-justified 5B)
// as hex. "(none)" when absent.
func fmtNRCellHex(c []byte) string {
	if len(c) == 0 {
		return "(none)"
	}
	const hexChars = "0123456789abcdef"
	out := make([]byte, 0, len(c)*2)
	for _, b := range c {
		out = append(out, hexChars[b>>4], hexChars[b&0x0F])
	}
	return string(out)
}

// Register installs Handle on the AMF-wide NGAP dispatcher.
func Register() { ngap.Register(ngap.ProcCodeInitialUEMessage, Handle) }

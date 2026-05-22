// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package initialctxsetup — Initial Context Setup procedure (TS 38.413 §8.3.1).
//
// Go port of nf/amf/ngap/ngap_initial_context_setup.py. After Security Mode
// Complete the AMF sends InitialContextSetupRequest so the gNB provisions
// the radio-side UE context (security key KgNB, UE security capabilities,
// Allowed NSSAI, AMBR, GUAMI). The gNB replies with InitialContextSetup
// Response — this handler marks gnb_context_established, at which point
// the AMF sends Registration Accept via DownlinkNASTransport.
//
// The Python reference serialises Registration Accept separately (after the
// Response); this port matches that behaviour so we don't piggy-back NAS
// PDUs before the radio context is ready.
package initialctxsetup

import (
	"encoding/binary"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	runtime "github.com/mmt/asn1go/pkg/runtime"
	"github.com/mmt/mmt-studio-core/nf/amf/ctx"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/security"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/nssf"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Send issues an InitialContextSetupRequest toward the gNB (TS 38.413
// §8.3.1.2). Called from gmm/smc.go after Security Mode Complete, before
// Registration Accept.
//
// Mandatory IEs we populate (Table 8.3.1.2-1 + ASN.1 §1706-1762):
//   0   AllowedNSSAI            (mandatory)
//   10  AMFUENGAPID             (mandatory)
//   28  GUAMI                   (mandatory)
//   85  RANUENGAPID             (mandatory)
//   94  SecurityKey (KgNB)      (mandatory)
//   119 UESecurityCapabilities  (mandatory)
//
// NASPDU is intentionally omitted — Registration Accept goes via a
// separate DownlinkNASTransport after we receive InitialContextSetup
// Response. The Python reference and spec both permit either shape.
//
// TODO(spec: TS 38.413 §8.3.1.2 "UE Aggregate Maximum Bit Rate") —
//   conditional: mandatory when PDUSessionResourceSetupListCxtReq is
//   present (§8.3.1.2 "If the PDU Session Resource Setup List IE is
//   present … the UE Aggregate Maximum Bit Rate IE shall be present").
//   Should also be sent on the first ICS for a UE so the gNB has
//   UE-AMBR before the first PDU session setup. Today we never include
//   it because we never include PDU sessions either.
//
// TODO(spec: TS 38.413 §8.3.1.2 "Mobility Restriction List") —
//   roaming / service-area / CAG restrictions. Must be included when
//   UE has roaming restrictions.
//
// TODO(spec: TS 38.413 §8.3.1.2 "Masked IMEISV") —
//   populate from UE ctx after the IMEISV request in SMC Complete
//   (TS 24.501 §9.11.3.28 IMEISV request bit). Currently we request
//   IMEISV in SMC but don't bubble it into ICS.
//
// TODO(spec: TS 38.413 §8.3.1.2 "UE Radio Capability") —
//   if we stored it from a previous registration in the same AMF
//   (or received it via N26 handover), attach it here so the gNB
//   doesn't need UE Capability Enquiry. Today we force the gNB to
//   query UE caps every registration.
//
// TODO(spec: TS 38.413 §8.3.1.2 "Index to RFSP") —
//   operator-configured RAT/Frequency Selection Priority (§9.3.1.89).
//
// TODO(spec: TS 38.413 §8.3.1.2 "Emergency Fallback Indicator") —
//   set when registration is for emergency services (UE registration
//   type = 4 "emergency registration").
//
// TODO(spec: TS 38.413 §8.3.1.2 "Trace Activation") —
//   MDT / UE tracing from OAM. Not wired.
//
// TODO(spec: TS 38.413 §8.3.1.2 "UE Radio Capability ID") —
//   §9.3.1.161 capability-ID scheme; lets the gNB look up UE caps
//   from a local DB instead of receiving them on the wire.
//
// TODO(spec: TS 38.413 §8.3.1.2 "RRC Inactive Transition Report Request") —
//   operator policy to be notified when the UE goes RRC_INACTIVE.
//
// TODO(spec: TS 38.413 §8.3.1.2 "CE-mode-B Restricted" /
//   "UE Differentiation Information") — IoT-specific optionals.
func Send(gnb *gnbctx.GnbCtx, ue *uectx.AmfUeCtx) error {
	log := logger.Get("amf.ngap.initialctxsetup")

	// Procedure-collision guard (TS 38.413 §8.3.1.1 "one ICS per
	// connection" + §8.3.3.1 "release takes precedence"). Blocks a
	// second ICS while one is pending, and blocks ICS during an
	// ongoing UE Context Release.
	if ok, reason := uectx.CanStartNGAPProcedure(ue.NGAPProc, uectx.NGAPProcInitialContextSetup); !ok {
		log.WithIMSI(ue.IMSI).Warnf("InitialContextSetup skipped amfUeID=%d: %s",
			ue.AmfUeNGAPID, reason)
		return fmt.Errorf("ICS send: blocked by NGAPProc=%s: %s", ue.NGAPProc, reason)
	}

	amfID := genngap.AMFUENGAPID(ue.AmfUeNGAPID)
	ranID := genngap.RANUENGAPID(ue.RanUeNGAPID)

	// GUAMI from the AMF context (picks the first configured GUAMI when
	// the UE hasn't been associated with a specific PLMN yet).
	var guami *genngap.GUAMI
	if list := ctx.Default.GUAMIList(); len(list) > 0 {
		g := list[0]
		gv := genngap.GUAMI{
			PLMNIdentity: genngap.PLMNIdentity(g.PLMNID),
			AMFRegionID:  genngap.AMFRegionID(runtime.BitString{Bytes: []byte{g.AMFRegionID}, BitLength: 8}),
			AMFSetID:     genngap.AMFSetID(runtime.BitString{Bytes: pack10Bits(g.AMFSetID), BitLength: 10}),
			// 6-bit AMFPointer must be MSB-aligned inside the byte — the
			// BitString serializer reads bits from MSB→LSB, so a raw
			// integer value of e.g. 5 (= 0x05 = 00000101) would serialize
			// as "000001" = 1, not 5. Shift left 2. Only the low 6 bits
			// are significant. Wire format unchanged when AMFPointer=0
			// (which happened to be the seed DB value — the bug sat
			// latent until someone provisioned a non-zero pointer).
			AMFPointer: genngap.AMFPointer(runtime.BitString{Bytes: []byte{(g.AMFPointer & 0x3F) << 2}, BitLength: 6}),
		}
		guami = &gv
	}

	// Security key IE carries K_gNB (256 bits). Derived just-in-time
	// from K_AMF + the current UL NAS COUNT per TS 33.501 v19.6.0
	// §A.9 (KDF FC=0x6E, P0=UL NAS COUNT, P1=0x01 for 3GPP access)
	// and §6.8.1.2.2 (freshness = UL count of the NAS message that
	// triggered CM-IDLE→CM-CONNECTED). No caching by design — see
	// nf/amf/security/doc.go invariant I4: a stale K_gNB bug like
	// commit 170af94 is un-expressible when the key can't be stashed.
	var secKey *genngap.SecurityKey
	if kgnb, err := security.DeriveKgNB(ue); err == nil {
		sk := genngap.SecurityKey(runtime.BitString{Bytes: kgnb, BitLength: 256})
		secKey = &sk
	} else {
		log.Errorf("K_gNB derive amfUeID=%d: %v — SecurityKey IE omitted", ue.AmfUeNGAPID, err)
	}

	// UE Security Capabilities IE (TS 38.413 §9.3.1.86) — this IE
	// "indicates which security capabilities the UE supports", i.e. it
	// carries the UE's reported capabilities (from the 5GS NAS UE
	// Security Capability IE in Registration Request, TS 24.501
	// §9.11.3.54) — NOT the AMF's supported algorithms. The gNB uses
	// this to decide how to secure AS signalling / user plane.
	//
	// ue.Security.UESecCap holds the raw bytes as reported by the UE
	// in Registration Request (2 octets: [NEA0..3 bits | reserved,
	// NIA0..3 bits | reserved], with EEA/EIA mirroring when bytes
	// 3..4 exist). We split to NR vs EUTRA banks and send the UE's
	// view verbatim. Earlier builds sent the AMF's DB algorithm
	// priority list by mistake — spec-wrong, and could confuse the
	// gNB when it negotiates with the UE directly over AS.
	ueSec := ue.Security.UESecCap
	if len(ueSec) < 2 {
		return fmt.Errorf("ICS Send: UESecurityCapability not captured (Registration Request IE was missing/short)")
	}
	var neaBytes, niaBytes, eeaBytes, eiaBytes []byte
	neaBytes = []byte{ueSec[0], 0x00}
	niaBytes = []byte{ueSec[1], 0x00}
	// EUTRA algorithms may be reported in the optional octets 3..4 of
	// the UE Security Capability IE (TS 24.501 §9.11.3.54). When
	// absent, TS 38.413 §9.3.1.86 permits mirroring the NR bits
	// (SNOW3G/AES/ZUC map 1:1 between NR and EUTRA algorithm IDs).
	if len(ueSec) >= 4 {
		eeaBytes = []byte{ueSec[2], 0x00}
		eiaBytes = []byte{ueSec[3], 0x00}
	} else {
		eeaBytes = neaBytes
		eiaBytes = niaBytes
	}
	secCaps := &genngap.UESecurityCapabilities{
		NRencryptionAlgorithms:             genngap.NRencryptionAlgorithms(runtime.BitString{Bytes: neaBytes, BitLength: 16}),
		NRintegrityProtectionAlgorithms:    genngap.NRintegrityProtectionAlgorithms(runtime.BitString{Bytes: niaBytes, BitLength: 16}),
		EUTRAencryptionAlgorithms:          genngap.EUTRAencryptionAlgorithms(runtime.BitString{Bytes: eeaBytes, BitLength: 16}),
		EUTRAintegrityProtectionAlgorithms: genngap.EUTRAintegrityProtectionAlgorithms(runtime.BitString{Bytes: eiaBytes, BitLength: 16}),
	}

	// IE 0: AllowedNSSAI — mandatory per TS 38.413 §8.3.1.2 (local ASN.1:
	// codecs/asn1-go/protocols/ngap/asn.1/NGAP-PDU-Contents.asn §1757
	// "PRESENCE mandatory" with CRITICALITY reject). Populated by NSSF
	// selection during Registration Request handling (see gmm/registration.go
	// runNSSFSelection → ue.AllowedNSSAI = result.Allowed).
	allowed, _ := ue.AllowedNSSAI.([]nssf.SNSSAI)
	if len(allowed) == 0 {
		return fmt.Errorf("ICS Send: UE has no Allowed NSSAI (NSSF selection must precede ICS)")
	}
	allowedIE := buildAllowedNSSAI(allowed)

	msg := &genngap.InitialContextSetupRequest{}
	add := func(id int64, crit genngap.Criticality, v genngap.InitialContextSetupRequestIEsValue) {
		msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.InitialContextSetupRequestIEsEntry{
			Id:          genngap.ProtocolIEID(id),
			Criticality: crit,
			Value:       v,
		})
	}
	// IE emission order. APER SEQUENCE OF has no prescribed wire
	// order — any sequencing decodes on a compliant peer. UE-NGAP-
	// IDs go first (items 0 and 1) purely for capture-parity with
	// reference stacks; the rest (GUAMI, AllowedNSSAI,
	// UESecurityCapabilities, SecurityKey) follow in no particular
	// order.
	add(int64(genngap.IdAMFUENGAPID), genngap.CriticalityReject, genngap.InitialContextSetupRequestIEsValue{
		Present:     genngap.InitialContextSetupRequestIEsValuePresentAMFUENGAPID,
		AMFUENGAPID: &amfID,
	})
	add(int64(genngap.IdRANUENGAPID), genngap.CriticalityReject, genngap.InitialContextSetupRequestIEsValue{
		Present:     genngap.InitialContextSetupRequestIEsValuePresentRANUENGAPID,
		RANUENGAPID: &ranID,
	})
	if guami != nil {
		add(int64(genngap.IdGUAMI), genngap.CriticalityReject, genngap.InitialContextSetupRequestIEsValue{
			Present: genngap.InitialContextSetupRequestIEsValuePresentGUAMI,
			GUAMI:   guami,
		})
	}
	add(int64(genngap.IdAllowedNSSAI), genngap.CriticalityReject, genngap.InitialContextSetupRequestIEsValue{
		Present:      genngap.InitialContextSetupRequestIEsValuePresentAllowedNSSAI,
		AllowedNSSAI: allowedIE,
	})
	add(int64(genngap.IdUESecurityCapabilities), genngap.CriticalityReject, genngap.InitialContextSetupRequestIEsValue{
		Present:                genngap.InitialContextSetupRequestIEsValuePresentUESecurityCapabilities,
		UESecurityCapabilities: secCaps,
	})
	if secKey != nil {
		add(int64(genngap.IdSecurityKey), genngap.CriticalityReject, genngap.InitialContextSetupRequestIEsValue{
			Present:     genngap.InitialContextSetupRequestIEsValuePresentSecurityKey,
			SecurityKey: secKey,
		})
	}

	inner, err := msg.MarshalAPER()
	if err != nil {
		return err
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ngap.ProcCodeInitialContextSetup,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		return err
	}
	stream := gnb.UEStream(ue.AmfUeNGAPID)
	if err := gnb.Send(pdu, stream); err != nil {
		return err
	}
	ue.NGAPProc = uectx.NGAPProcInitialContextSetup

	// Advance the NGAP per-UE FSM — arms Twait-ICS declaratively.
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(fk).Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvICSRequestSent})

	log.WithIMSI(ue.IMSI).Infof("InitialContextSetupRequest sent amfUeID=%d gNB=%s",
		ue.AmfUeNGAPID, gnb.GnbIP)
	return nil
}

// OnContextEstablished is invoked by handleResponse immediately after
// the per-UE NGAP FSM transitions to ESTABLISHED (ICS_PENDING →
// ESTABLISHED on EvICSResponse). gmm/service.go registers it at init()
// to drain ue.PendingN1N2Sessions via pdusetup.Send — spec-aligned
// sequencing for TS 23.502 §4.2.3.2 where the §4.2.3.2 step 12 N2
// Request (PDU Session Resource Setup) must not fire before the AS
// UE context is up at the gNB. Deferring until ICS Response keeps
// the NGAP FSM transitions ordered NOT_ESTABLISHED → ICS_PENDING →
// ESTABLISHED → RESOURCE_SETUP_PENDING, avoiding "EvPDUResourceSetup
// RequestSent from state ICS_PENDING" collision warnings that would
// otherwise fire when both requests are issued back-to-back from the
// ServiceRequest handler. Nil is a no-op (keeps tests and pre-gmm
// boot-time paths clean).
//
// Hook pattern — avoids the initialctxsetup ↔ pdusetup ↔ gmm import
// cycle that would form if initialctxsetup called gmm directly.
var OnContextEstablished func(gnb *gnbctx.GnbCtx, ue *uectx.AmfUeCtx)

// handleResponse — SuccessfulOutcome of procedureCode=14.
func handleResponse(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.initialctxsetup")
	if env.Type == wire.UnsuccessfulOutcome {
		handleFailure(gnb, env, stream)
		return
	}

	var resp genngap.InitialContextSetupResponse
	if err := resp.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("ICS Response decode from %s: %v", gnb.GnbIP, err)
		return
	}
	amfUeID, ranUeID := extractUEIDs(resp.ProtocolIEs)
	ue := locateUE(gnb, amfUeID, ranUeID)
	if ue == nil {
		log.Warnf("ICS Response for unknown UE amfUeID=%d ranUeID=%d gNB=%s",
			amfUeID, ranUeID, gnb.GnbIP)
		return
	}
	ue.GnbContextEstablished = true
	ue.NGAPProc = uectx.NGAPProcNone

	// Advance NGAP per-UE FSM: ICS_PENDING → ESTABLISHED.
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(fk).Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvICSResponse})

	log.WithIMSI(ue.IMSI).Infof("InitialContextSetup succeeded amfUeID=%d", ue.AmfUeNGAPID)

	// TS 23.502 §4.2.3.2 step 12 — once the AS UE context is up, drain
	// any queued PDU Session UP-activate requests (queued by the gmm
	// ServiceRequest handler from the UE's UplinkDataStatus IE or
	// SMF-queued N1N2 pending reactivations). The hook lives here so
	// the FSM is in ESTABLISHED (not ICS_PENDING) when the
	// EvPDUResourceSetupRequestSent events fire, matching the
	// NOT_ESTABLISHED → ICS_PENDING → ESTABLISHED → RESOURCE_SETUP_
	// PENDING transition graph in fsm_transitions.go.
	if OnContextEstablished != nil {
		OnContextEstablished(gnb, ue)
	}
}

// handleFailure processes INITIAL CONTEXT SETUP FAILURE (TS 38.413
// §8.3.1.3). The gNB reports it couldn't establish the NG UE context;
// per spec the AMF shall:
//
//   "for each PDU session indicated in the PDU Session ID IE, transfer
//    transparently the PDU Session Resource Setup Unsuccessful
//    Transfer IE to the SMF associated with the concerned PDU session
//    and may consider that the NAS PDU included in the INITIAL
//    CONTEXT SETUP REQUEST message was not delivered."
//
// Today:
//   - NGAP per-UE FSM advances ICS_PENDING → RELEASED on EvICSFailure
//     (fsm_transitions.go).
//   - Cause IE is logged via formatCause() so the operator sees why
//     the gNB rejected.
//   - The failed NAS PDU we bundled in the request is treated as
//     undelivered — the GMM FSM's T3550 / T3560 retx handles this
//     implicitly by firing a retransmit on the next timer expiry.
//
// TODO(spec: TS 38.413 §8.3.1.3 "transfer PDU Session Resource Setup
//   Unsuccessful Transfer IE to the SMF") — we don't extract the
//   PDUSessionResourceFailedToSetupListCxtFail IE from the failure
//   message and relay per-session transfer blobs to SMF over N11
//   Nsmf_PDUSession_UpdateSMContext. Until that lands, SMF keeps
//   sessions in an inconsistent state after a gNB-side ICS failure.
func handleFailure(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.initialctxsetup")
	var fail genngap.InitialContextSetupFailure
	if err := fail.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("ICS Failure decode from %s: %v", gnb.GnbIP, err)
		return
	}
	amfUeID, ranUeID, cause := extractFailureIEs(fail.ProtocolIEs)
	ue := locateUE(gnb, amfUeID, ranUeID)
	if ue != nil {
		ue.GnbContextEstablished = false
		ue.NGAPProc = uectx.NGAPProcNone
		fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
		_ = ngapfsm.Of(fk).Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvICSFailure})
	}
	log.Warnf("InitialContextSetupFailure from %s amfUeID=%d ranUeID=%d cause=%s",
		gnb.GnbIP, amfUeID, ranUeID, formatCause(cause))
}

// formatCause renders the NGAP Cause CHOICE (TS 38.413 §9.3.1.2) to a
// human-readable string for log output. Returns "unspecified" when nil.
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
	return "unknown"
}

// extractUEIDs pulls AMF-UE-NGAP-ID + RAN-UE-NGAP-ID from a Response IE list.
func extractUEIDs(ies []genngap.InitialContextSetupResponseIEsEntry) (amf, ran int64) {
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

func extractFailureIEs(ies []genngap.InitialContextSetupFailureIEsEntry) (amf, ran int64, cause *genngap.Cause) {
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
		case int64(genngap.IdCause):
			cause = ie.Value.Cause
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

// buildAllowedNSSAI converts the NSSF-selected []SNSSAI into the codec's
// AllowedNSSAI (SEQUENCE SIZE(1..8) OF AllowedNSSAIItem). SD=0 or 0xFFFFFF
// is the wildcard → omit SD (TS 23.003 §28.4; Python reference behaviour).
func buildAllowedNSSAI(allowed []nssf.SNSSAI) *genngap.AllowedNSSAI {
	if len(allowed) > 8 {
		allowed = allowed[:8] // codec sizeUB:8
	}
	items := make(genngap.AllowedNSSAI, 0, len(allowed))
	for _, s := range allowed {
		item := genngap.AllowedNSSAIItem{
			SNSSAI: genngap.SNSSAI{SST: genngap.SST{s.SST}},
		}
		if s.SD != 0 && s.SD != 0xFFFFFF {
			sd := make([]byte, 4)
			binary.BigEndian.PutUint32(sd, s.SD)
			v := genngap.SD(sd[1:4]) // low 3 bytes, big-endian
			item.SNSSAI.SD = &v
		}
		items = append(items, item)
	}
	return &items
}

// pack10Bits left-justifies a 10-bit value in a 2-byte buffer (MSB first).
func pack10Bits(v uint16) []byte {
	v = (v & 0x03FF) << 6
	return []byte{byte(v >> 8), byte(v)}
}

// Register installs the Response/Failure handler on the AMF dispatcher.
// ICS itself is AMF-initiated via Send().
func Register() {
	ngap.Register(ngap.ProcCodeInitialContextSetup, handleResponse)
}

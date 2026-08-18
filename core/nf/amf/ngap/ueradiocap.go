// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NGAP UE Radio Capability Info Indication handler.
// PDF: specs/3gpp/ts_138413v190200p.pdf
// TS 38.413 §8.14.1 (pages 133-134). ASN.1 source:
// codecs/asn1-go/protocols/ngap/asn.1/NGAP-PDU-Contents.asn lines
// 7406-7457.
//
// Procedure shape (class 2 — unconfirmed, non-response, NG Application
// Protocol; TS 38.413 §8.14.1 Figure 8.14.1.2-1): the NG-RAN node
// sends one UE RADIO CAPABILITY INFO INDICATION to the AMF; the AMF
// stores the carried capabilities and emits NO response.
//
// §8.14.1.1 "General" (verbatim):
//   "The purpose of the UE Radio Capability Info Indication procedure
//    is to enable the NG-RAN node to provide to the AMF UE radio
//    capability-related information. The procedure uses UE-associated
//    signalling."
//
// §8.14.1.2 "Successful Operation" — four SHALL/MAY clauses governing
// AMF-side handling. Handled inline below (search the clause number):
//
//   §8.14.1.2 (a) — may also include UE Radio Capability for Paging;
//                   if paging IE contains NR and E-UTRA inner IEs the
//                   AMF shall, if supported, use it per TS 23.501.
//   §8.14.1.2 (b) — received info SHALL replace previously stored.
//   §8.14.1.2 (c) — if UE Radio Capability – E-UTRA Format is
//                   included, AMF shall, if supported, use it per
//                   TS 23.501.
//   §8.14.1.2 (d) — if XR Device with 2Rx IE is included, AMF shall,
//                   if supported, store this information and use it
//                   accordingly.
//
// §8.14.1.3 "Abnormal Conditions": "Void." — no spec-defined error
// reply. For the implementation-defined case of "UE ctx not known on
// the AMF side", we opt to log and drop (§8.14.1.3's Void is the
// closest guidance; an Error Indication via §8.7.5 would also be
// defensible if operators want a gNB-visible trace).
//
// Downstream usage (TS 23.501 §5.4.4.1 UE radio capability information
// storage in the AMF; PDF: specs/3gpp/ts_123501v190700p.pdf):
//
//   "the AMF shall store the UE Radio Capability information during
//    CM-IDLE state for the UE and RM-REGISTERED state for the UE and
//    the AMF shall if it is available, send its most up to date UE
//    Radio Capability information to the RAN in the N2 REQUEST
//    message, i.e. INITIAL CONTEXT SETUP REQUEST or UE RADIO
//    CAPABILITY CHECK REQUEST."
//
//   "The AMF deletes the UE radio capability when the UE RM state in
//    the AMF transitions to RM-DEREGISTERED."
//
//   "When the AMF receives Registration Request with the Registration
//    type set to Initial Registration ... the AMF deletes the UE
//    radio capability."
//
//   "When the AMF receives Mobility Registration Update Request with
//    'UE Radio Capability Update' requested by the UE, it shall
//    delete any UE Radio Capability information that it has stored."
//
// IEs per UERadioCapabilityInfoIndicationIEs object-set
// (NGAP-PDU-Contents.asn §7417-7454 / local Id* constants):
//
//   Id= 10 (id-AMF-UE-NGAP-ID)                  MANDATORY  crit=reject
//   Id= 85 (id-RAN-UE-NGAP-ID)                  MANDATORY  crit=reject
//   Id=117 (id-UERadioCapability)               MANDATORY  crit=ignore
//   Id=118 (id-UERadioCapabilityForPaging)      OPTIONAL   crit=ignore
//   Id=265 (id-UERadioCapability-EUTRA-Format)  OPTIONAL   crit=ignore
//   Id=428 (id-XrDeviceWith2Rx)                 OPTIONAL   crit=ignore
//
// Criticality "ignore" across all four capability IEs means the AMF
// must not reject the PDU if one decodes poorly — proceed with what
// parsed and log the gap.
package ngap

import (
	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

func init() {
	Register(ProcCodeUERadioCapabilityInfoIndication, handleUERadioCapabilityInfoIndication)
}

// handleUERadioCapabilityInfoIndication implements TS 38.413 §8.14.1.2
// Successful Operation on the AMF side. Class 2 procedure — no
// response PDU. The four §8.14.1.2 SHALLs are applied in sequence
// against the decoded IEs; caching lets later procedures
// (UERadioCapabilityCheckRequest / next INITIAL CONTEXT SETUP
// REQUEST) skip the over-the-air UE Capability Enquiry round-trip per
// TS 23.501 §5.4.4.1.
func handleUERadioCapabilityInfoIndication(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.ueradiocap")

	var msg genngap.UERadioCapabilityInfoIndication
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		// Criticality on all reject-grade IEs (AMF-UE-NGAP-ID /
		// RAN-UE-NGAP-ID) means a decode failure here is fatal for
		// this PDU; drop per §8.14.1.3 "Void" abnormal handling.
		log.Warnf("UERadioCapabilityInfoIndication decode from %s: %v", gnb.GnbIP, err)
		return
	}

	var amfUeID, ranUeID int64
	var nrCap, eutraCap, pagingCap []byte
	var xr2Rx *int64
	for i := range msg.ProtocolIEs {
		ie := &msg.ProtocolIEs[i]
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				amfUeID = int64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				ranUeID = int64(*ie.Value.RANUENGAPID)
			}
		case int64(genngap.IdUERadioCapability):
			// Id=117 — NR-format UE Radio Capability container. TS
			// 23.501 §5.4.4.1: "The UE Radio Capability information
			// is defined in TS 38.300 [27] and contains information
			// on RATs that the UE supports (e.g. power class,
			// frequency bands, etc)." TS 38.300 is a cross-ref —
			// NOT vendored locally, so the exact byte layout isn't
			// verifiable from in-tree PDFs. AMF treats the OCTET
			// STRING as opaque; stores + replays verbatim.
			if ie.Value.UERadioCapability != nil {
				nrCap = append(nrCap[:0], (*ie.Value.UERadioCapability)...)
			}
		case int64(genngap.IdUERadioCapabilityEUTRAFormat):
			// Id=265 — E-UTRA-format container. Same Go type
			// (UERadioCapability = OCTET STRING) as Id=117, so the
			// generated APERAlternativeForID table points both IDs
			// to the same IEsValue field. Discrimination is by
			// `ie.Id` here.
			//
			// §8.14.1.2 (c): "If the UE RADIO CAPABILITY INFO
			// INDICATION message includes the UE Radio Capability –
			// E-UTRA Format IE, the AMF shall, if supported, use it
			// as specified in TS 23.501." — stored for replay on
			// future N2 REQUEST per TS 23.501 §5.4.4.1.
			if ie.Value.UERadioCapability != nil {
				eutraCap = append(eutraCap[:0], (*ie.Value.UERadioCapability)...)
			}
		case int64(genngap.IdUERadioCapabilityForPaging):
			// Id=118 — SEQUENCE { ofNR OPTIONAL, ofEUTRA OPTIONAL,
			// ..., ext (ofNB-IoT) } per NGAP-IEs.asn §16787.
			//
			// §8.14.1.2 (a): "If the UE Radio Capability for Paging
			// IE includes the UE Radio Capability for Paging of NR
			// IE and the UE Radio Capability for Paging of E-UTRA
			// IE, the AMF shall, if supported, use it as specified
			// in TS 23.501." — used by the Paging procedure
			// (TS 38.413 §8.5.1) when building UE Radio Capability
			// for Paging into PAGING PDUs. We preserve the IE's
			// APER bytes so SendPaging can feed it back byte-
			// identical.
			if ie.Value.UERadioCapabilityForPaging != nil {
				if raw, err := ie.Value.UERadioCapabilityForPaging.MarshalAPER(); err == nil {
					pagingCap = raw
				} else {
					log.Debugf("UERadioCapabilityForPaging re-marshal failed: %v — dropping paging cap", err)
				}
			}
		case int64(genngap.IdXrDeviceWith2Rx):
			// Id=428 — ENUMERATED{true, ...} per NGAP-IEs.asn §17689.
			//
			// §8.14.1.2 (d): "If the UE RADIO CAPABILITY INFO
			// INDICATION message includes the XR Device with 2Rx IE,
			// the AMF shall, if supported, store this information
			// and use it accordingly." — stored; no consumer wired
			// yet (used by extended reality traffic optimisations
			// that aren't implemented).
			if ie.Value.XrDeviceWith2Rx != nil {
				v := int64(*ie.Value.XrDeviceWith2Rx)
				xr2Rx = &v
			}
		}
	}

	ue := locateUE(gnb, amfUeID, ranUeID)
	if ue == nil {
		// §8.14.1.3 "Abnormal Conditions: Void." Unknown UE — drop
		// silently (no Error Indication per-spec; logging-only so
		// operators can see a stale reference from the gNB).
		log.Warnf("UERadioCapabilityInfoIndication for unknown UE amfUeID=%d ranUeID=%d gNB=%s",
			amfUeID, ranUeID, gnb.GnbIP)
		return
	}

	// §8.14.1.2 (b) replace-not-merge semantics: "The UE radio
	// capability information received by the AMF shall replace
	// previously stored corresponding UE radio capability information
	// in the AMF for the UE."
	ue.UERadioCapability = nrCap
	ue.UERadioCapabilityForPaging = pagingCap
	ue.UERadioCapabilityEUTRA = eutraCap
	ue.XRDeviceWith2Rx = xr2Rx

	log.WithIMSI(ue.IMSI).Infof("UE Radio Capability stored amfUeID=%d nr=%dB eutra=%dB paging=%dB xr2Rx=%v (TS 38.413 §8.14.1.2)",
		ue.AmfUeNGAPID, len(nrCap), len(eutraCap), len(pagingCap), xr2Rx != nil)

	// TODO(spec: TS 23.501 §5.4.4.1) — "the AMF shall ... send its
	//   most up to date UE Radio Capability information to the RAN
	//   in the N2 REQUEST message, i.e. INITIAL CONTEXT SETUP
	//   REQUEST or UE RADIO CAPABILITY CHECK REQUEST." ICS sender
	//   (nf/amf/ngap/initialctxsetup) should include IE 117 from
	//   ue.UERadioCapability when non-empty. Currently omitted;
	//   benign (gNB falls back to UE Capability Enquiry) but costs
	//   a round-trip on every initial-reg.
	//
	// TODO(spec: TS 23.501 §5.4.4.1) — "The AMF deletes the UE
	//   radio capability when the UE RM state in the AMF transitions
	//   to RM-DEREGISTERED." / "When the AMF receives Registration
	//   Request with the Registration type set to Initial
	//   Registration ... the AMF deletes the UE radio capability." /
	//   "When the AMF receives Mobility Registration Update Request
	//   with 'UE Radio Capability Update' requested by the UE, it
	//   shall delete any UE Radio Capability information that it
	//   has stored." Attach these clears to handleDeregistration*
	//   and handleRegistrationRequest initial-reg / mob-with-update
	//   branches in nf/amf/gmm/. Not wired yet; the stored caps
	//   persist across re-registrations which is a conservative
	//   leak (older-than-spec caps served to gNB).
}

// locateUE finds the AMF UE ctx for an inbound UE-associated NGAP message.
// Prefers AMF-UE-NGAP-ID (our side), falls back to RAN-UE-NGAP-ID lookup
// keyed on the gNB identity. The RAN-UE-NGAP-ID fallback handles the
// rare case where the gNB populates only the peer-side ID (permitted
// per TS 38.413 §9.3.3.1 when the AMF-UE-NGAP-ID isn't yet known,
// e.g. pre-ICS), though §8.14.1 requires BOTH IEs mandatorily so this
// path mostly catches IE-order edge cases.
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

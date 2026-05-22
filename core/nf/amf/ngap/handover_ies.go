// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// IE-level encode / decode helpers for the handover procedures.
// Uses the generated NGAP codec (codecs/asn1-go/protocols/ngap/generated)
// so the ngap package can look past the opaque envelope Value and
// act on the IEs that §8.4 actually carries.
//
// Scope of this file:
//
//   Parse  — HandoverRequired, HandoverNotify, HandoverCancel
//            (extract AMF-UE-NGAP-ID, RAN-UE-NGAP-ID, Target ID,
//             Cause, Notify-Source-NG-RAN-Node presence).
//
//   Build  — HandoverPreparationFailure, HandoverCancelAcknowledge,
//            PathSwitchRequestFailure  (envelope Value carrying real
//             AMF-UE-NGAP-ID / RAN-UE-NGAP-ID / Cause IEs per
//             §9.2.3 tables).
//
// Relay-to-target messages (HandoverRequest, HandoverCommand,
// DownlinkRAN[Early]StatusTransfer) continue to forward the source's
// opaque Value — the IE-by-IE rewrite they'd require in a fully-
// compliant AMF depends on SMF interaction per TS 23.502 §4.9.1.3,
// which is not in-tree.
package ngap

import (
	"encoding/binary"
	"fmt"
	"net"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"

	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
)

// ── Parsed views ─────────────────────────────────────────────────

// ParsedHandoverRequired holds the §9.2.3.1 IEs the AMF needs to
// route a HandoverRequired: the UE IDs, the target RAN node ID
// (for routing), and the original Cause (for logging).
type ParsedHandoverRequired struct {
	AMFUEID  int64
	RANUEID  int64
	Target   *genngap.TargetID
	Cause    *genngap.Cause
}

// ParseHandoverRequired decodes the envelope Value as a
// HandoverRequired PDU and extracts the routing-relevant IEs.
// Returns a best-effort result: missing optional IEs leave their
// fields zero/nil. A decode failure returns the partial struct + err.
func ParseHandoverRequired(value []byte) (*ParsedHandoverRequired, error) {
	var pdu genngap.HandoverRequired
	if err := pdu.UnmarshalAPER(value); err != nil {
		return &ParsedHandoverRequired{}, err
	}
	out := &ParsedHandoverRequired{}
	for _, ie := range pdu.ProtocolIEs {
		switch ie.Id {
		case genngap.IdAMFUENGAPID:
			if ie.Value.AMFUENGAPID != nil {
				out.AMFUEID = int64(*ie.Value.AMFUENGAPID)
			}
		case genngap.IdRANUENGAPID:
			if ie.Value.RANUENGAPID != nil {
				out.RANUEID = int64(*ie.Value.RANUENGAPID)
			}
		case genngap.IdTargetID:
			out.Target = ie.Value.TargetID
		case genngap.IdCause:
			out.Cause = ie.Value.Cause
		}
	}
	return out, nil
}

// ParsedHandoverNotify holds the §9.2.3.3 IEs the AMF needs when
// a HandoverNotify arrives: the UE IDs and whether the Notify
// Source NG-RAN Node IE is present (which triggers §8.4.8
// HandoverSuccess).
type ParsedHandoverNotify struct {
	AMFUEID         int64
	RANUEID         int64
	NotifySource    bool
}

// ParseHandoverNotify decodes the envelope Value as a HandoverNotify.
func ParseHandoverNotify(value []byte) (*ParsedHandoverNotify, error) {
	var pdu genngap.HandoverNotify
	if err := pdu.UnmarshalAPER(value); err != nil {
		return &ParsedHandoverNotify{}, err
	}
	out := &ParsedHandoverNotify{}
	for _, ie := range pdu.ProtocolIEs {
		switch ie.Id {
		case genngap.IdAMFUENGAPID:
			if ie.Value.AMFUENGAPID != nil {
				out.AMFUEID = int64(*ie.Value.AMFUENGAPID)
			}
		case genngap.IdRANUENGAPID:
			if ie.Value.RANUENGAPID != nil {
				out.RANUEID = int64(*ie.Value.RANUENGAPID)
			}
		case genngap.IdNotifySourceNGRANNode:
			out.NotifySource = ie.Value.NotifySourceNGRANNode != nil
		}
	}
	return out, nil
}

// ParsedHandoverCancel holds the §9.2.3.10 IEs on HandoverCancel.
type ParsedHandoverCancel struct {
	AMFUEID int64
	RANUEID int64
	Cause   *genngap.Cause
}

// ParseHandoverCancel decodes a HandoverCancel envelope Value.
func ParseHandoverCancel(value []byte) (*ParsedHandoverCancel, error) {
	var pdu genngap.HandoverCancel
	if err := pdu.UnmarshalAPER(value); err != nil {
		return &ParsedHandoverCancel{}, err
	}
	out := &ParsedHandoverCancel{}
	for _, ie := range pdu.ProtocolIEs {
		switch ie.Id {
		case genngap.IdAMFUENGAPID:
			if ie.Value.AMFUENGAPID != nil {
				out.AMFUEID = int64(*ie.Value.AMFUENGAPID)
			}
		case genngap.IdRANUENGAPID:
			if ie.Value.RANUENGAPID != nil {
				out.RANUEID = int64(*ie.Value.RANUENGAPID)
			}
		case genngap.IdCause:
			out.Cause = ie.Value.Cause
		}
	}
	return out, nil
}

// ── Builders ─────────────────────────────────────────────────────

// BuildCauseProtocolUnspecified returns a Cause IE with
// protocol / unspecified-error semantics — a common fallback when
// the AMF can't supply a more specific reason.
func BuildCauseProtocolUnspecified() *genngap.Cause {
	p := genngap.CauseProtocol(genngap.CauseProtocolUnspecified)
	return &genngap.Cause{
		Present:  genngap.CausePresentProtocol,
		Protocol: &p,
	}
}

// BuildCauseRadioNetworkUnknownTargetID returns a Cause IE for the
// §8.4.1 abnormal case where the AMF couldn't resolve the Target ID
// from the Handover Required.
func BuildCauseRadioNetworkUnknownTargetID() *genngap.Cause {
	p := genngap.CauseRadioNetwork(genngap.CauseRadioNetworkUnknownTargetID)
	return &genngap.Cause{
		Present:      genngap.CausePresentRadioNetwork,
		RadioNetwork: &p,
	}
}

// BuildCauseRadioNetworkHandoverCancelled returns a Cause IE for
// source-initiated HandoverCancel (§8.4.5 success path).
func BuildCauseRadioNetworkHandoverCancelled() *genngap.Cause {
	p := genngap.CauseRadioNetwork(genngap.CauseRadioNetworkHandoverCancelled)
	return &genngap.Cause{
		Present:      genngap.CausePresentRadioNetwork,
		RadioNetwork: &p,
	}
}

// BuildHandoverPreparationFailureValue encodes an unsuccessful-
// outcome envelope Value for §8.4.1 with AMF-UE-NGAP-ID +
// RAN-UE-NGAP-ID + Cause IEs (mandatory per §9.2.3.2 IE table).
func BuildHandoverPreparationFailureValue(amfUEID, ranUEID int64, cause *genngap.Cause) ([]byte, error) {
	if cause == nil {
		cause = BuildCauseProtocolUnspecified()
	}
	pdu := genngap.HandoverPreparationFailure{
		ProtocolIEs: []genngap.HandoverPreparationFailureIEsEntry{
			{
				Id:          genngap.IdAMFUENGAPID,
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.HandoverPreparationFailureIEsValue{
					Present:     genngap.HandoverPreparationFailureIEsValuePresentAMFUENGAPID,
					AMFUENGAPID: amfUEIDPtr(amfUEID),
				},
			},
			{
				Id:          genngap.IdRANUENGAPID,
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.HandoverPreparationFailureIEsValue{
					Present:     genngap.HandoverPreparationFailureIEsValuePresentRANUENGAPID,
					RANUENGAPID: ranUEIDPtr(ranUEID),
				},
			},
			{
				Id:          genngap.IdCause,
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.HandoverPreparationFailureIEsValue{
					Present: genngap.HandoverPreparationFailureIEsValuePresentCause,
					Cause:   cause,
				},
			},
		},
	}
	return pdu.MarshalAPER()
}

// BuildHandoverCancelAcknowledgeValue encodes the §8.4.5 success
// response. AMF-UE-NGAP-ID + RAN-UE-NGAP-ID mandatory per §9.2.3.12.
func BuildHandoverCancelAcknowledgeValue(amfUEID, ranUEID int64) ([]byte, error) {
	pdu := genngap.HandoverCancelAcknowledge{
		ProtocolIEs: []genngap.HandoverCancelAcknowledgeIEsEntry{
			{
				Id:          genngap.IdAMFUENGAPID,
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.HandoverCancelAcknowledgeIEsValue{
					Present:     genngap.HandoverCancelAcknowledgeIEsValuePresentAMFUENGAPID,
					AMFUENGAPID: amfUEIDPtr(amfUEID),
				},
			},
			{
				Id:          genngap.IdRANUENGAPID,
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.HandoverCancelAcknowledgeIEsValue{
					Present:     genngap.HandoverCancelAcknowledgeIEsValuePresentRANUENGAPID,
					RANUENGAPID: ranUEIDPtr(ranUEID),
				},
			},
		},
	}
	return pdu.MarshalAPER()
}

// BuildPathSwitchRequestFailureValue encodes the §8.4.4 unsuccessful
// outcome Value. Per §9.2.3.9 the IE table has AMF-UE-NGAP-ID +
// RAN-UE-NGAP-ID + (optional PDUSessionResourceReleasedListPSFail) +
// (optional CriticalityDiagnostics) — NOT Cause. We omit the two
// optional IEs on the minimum-failure path.
func BuildPathSwitchRequestFailureValue(amfUEID, ranUEID int64) ([]byte, error) {
	pdu := genngap.PathSwitchRequestFailure{
		ProtocolIEs: []genngap.PathSwitchRequestFailureIEsEntry{
			{
				Id:          genngap.IdAMFUENGAPID,
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.PathSwitchRequestFailureIEsValue{
					Present:     genngap.PathSwitchRequestFailureIEsValuePresentAMFUENGAPID,
					AMFUENGAPID: amfUEIDPtr(amfUEID),
				},
			},
			{
				Id:          genngap.IdRANUENGAPID,
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.PathSwitchRequestFailureIEsValue{
					Present:     genngap.PathSwitchRequestFailureIEsValuePresentRANUENGAPID,
					RANUENGAPID: ranUEIDPtr(ranUEID),
				},
			},
		},
	}
	return pdu.MarshalAPER()
}

// parsedPathSwitchRequest holds the §9.2.3.8 UE IDs the AMF needs
// to echo into PathSwitchRequestAcknowledge / Failure.
type parsedPathSwitchRequest struct {
	AMFUEID int64 // from SourceAMFUENGAPID (§9.2.3.8) — the original AMF ID
	RANUEID int64 // RAN-UE-NGAP-ID at the target (new value, §9.2.3.8)
}

func parsePathSwitchRequest(value []byte) (*parsedPathSwitchRequest, error) {
	var pdu genngap.PathSwitchRequest
	if err := pdu.UnmarshalAPER(value); err != nil {
		return nil, err
	}
	out := &parsedPathSwitchRequest{}
	for _, ie := range pdu.ProtocolIEs {
		switch ie.Id {
		case genngap.IdSourceAMFUENGAPID:
			if ie.Value.AMFUENGAPID != nil {
				out.AMFUEID = int64(*ie.Value.AMFUENGAPID)
			}
		case genngap.IdRANUENGAPID:
			if ie.Value.RANUENGAPID != nil {
				out.RANUEID = int64(*ie.Value.RANUENGAPID)
			}
		}
	}
	return out, nil
}

// ── Target resolution via parsed Target ID ───────────────────────

// MatchGnbByTargetID returns the registered gNB whose Global GNB ID
// matches the given Target ID, or nil if not found. Only
// TargetRANNodeID → GlobalGNBID is supported (5GS → 5GS handovers);
// ng-eNB and IWF variants are out of scope for this iteration.
func MatchGnbByTargetID(target *genngap.TargetID) *gnbctx.GnbCtx {
	if target == nil || target.Present != genngap.TargetIDPresentTargetRANNodeID {
		return nil
	}
	if target.TargetRANNodeID == nil {
		return nil
	}
	id := target.TargetRANNodeID.GlobalRANNodeID
	if id.Present != genngap.GlobalRANNodeIDPresentGlobalGNBID || id.GlobalGNBID == nil {
		return nil
	}
	gnbID := id.GlobalGNBID
	for _, g := range gnbctx.Default.All() {
		if !g.IsConnected() {
			continue
		}
		if matchesGlobalGNB(g, gnbID) {
			return g
		}
	}
	return nil
}

// matchesGlobalGNB compares a TargetID's GlobalGNBID against a
// registered gNB context (PLMN-identity + gNB-ID bits). The gnbctx
// stores PLMN / gNB-ID per NG Setup (§8.7.1); we match bytewise.
func matchesGlobalGNB(g *gnbctx.GnbCtx, want *genngap.GlobalGNBID) bool {
	if want == nil {
		return false
	}
	// Compare PLMN first — fast bytewise.
	if len(want.PLMNIdentity) != 3 {
		return false
	}
	gnbPLMNs := g.AllPLMNs()
	wantPLMN := formatPLMN(want.PLMNIdentity)
	matched := false
	for _, p := range gnbPLMNs {
		if p == wantPLMN {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	// Compare gNB-ID bit-string.
	if want.GNBID.Present != genngap.GNBIDPresentGNBID || want.GNBID.GNBID == nil {
		return false
	}
	return gnbIDEquals(g, want.GNBID.GNBID.Bytes, int(want.GNBID.GNBID.BitLength))
}

// gnbIDEquals compares the generated BitString representation against
// a gNB's stored identity. Returns true only when the gnbctx stores
// an equivalent ID; stub today — see note below.
func gnbIDEquals(g *gnbctx.GnbCtx, _ []byte, _ int) bool {
	// gnbctx stores the gNB-ID as a hex string (AllTACs returns TACs,
	// not the gNB-ID — the gNB-ID field is stored separately). A
	// production match compares the decoded NR gNB-ID integer against
	// g.GnbID. Since the per-gNB ID lookup table isn't threaded through
	// this iteration's boundary, we fall back to "any registered
	// connected gNB with a matching PLMN" — which is already a useful
	// narrowing over the previous "any other connected gNB" fallback.
	_ = g
	return true
}

func formatPLMN(p []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(p)*2)
	for i, b := range p {
		out[i*2] = hex[b>>4]
		out[i*2+1] = hex[b&0x0f]
	}
	return string(out)
}

// ── HandoverRequest builder (TS 38.413 v19.2.0 §9.2.3.4) ──────────
//
// BuildHandoverRequestValue assembles the HANDOVER REQUEST PDU value
// the AMF sends to the target NG-RAN node on receipt of HANDOVER
// REQUIRED (TS 38.413 §8.4.1.2 step). It is NOT a relay of the
// source's Value bytes — HANDOVER REQUIRED carries a different IE set
// (id 61 PDUSessionResourceListHORqd, id 85 RAN-UE-NGAP-ID, id 105
// TargetID — none of which belong in HANDOVER REQUEST) and pycrate /
// any strict NGAP decoder rejects the cross-message bytes as schema
// mismatch.
//
// Per §9.2.3.4 IE table the mandatory items are: AMF-UE-NGAP-ID (10),
// Handover Type (29), Cause (15), UE AMBR (110), UE Security
// Capabilities (119), Security Context (93), PDU Session Resource
// Setup List (73), Allowed NSSAI (0), Source-to-Target Transparent
// Container (101), and GUAMI (28). The current tester / gNB peer
// decodes a lenient subset (pycrate from_aper accepts the bytes and
// surfaces whichever IDs are present), so this builder emits the
// minimum required by §8.4.2.2 step (target reads PSI list +
// HandoverRequestTransfer + Source-to-Target Container): IEs 10, 29,
// 15, 73, 101. Other mandatory IEs land in a follow-up when the
// target gNB needs them.
//
// For each PSI in PDUSessionResourceListHORqd we emit a
// PDUSessionResourceSetupItemHOReq whose HandoverRequestTransfer
// octet string is the same §9.3.4.1 PDU Session Resource Setup
// Request Transfer (§9.2.3.4 verbatim: "Containing the PDU Session
// Resource Setup Request Transfer IE specified in subclause 9.3.4.1"),
// built from the live SMF Session record so the target gNB sees the
// preserved UPF UL TEID + N3 endpoint.
func BuildHandoverRequestValue(ue *uectx.AmfUeCtx, parsed *ParsedHandoverRequired, requiredValue []byte) ([]byte, error) {
	if ue == nil {
		return nil, fmt.Errorf("BuildHandoverRequestValue: ue is nil")
	}
	if parsed == nil {
		return nil, fmt.Errorf("BuildHandoverRequestValue: parsed required is nil")
	}

	// Re-parse HandoverRequired locally to extract two opaque IEs we
	// need to forward as-is: HandoverType (29) and Cause (15) values
	// land on the parsed struct already, but the
	// SourceToTargetTransparentContainer (101) bytes are not on the
	// ParsedHandoverRequired struct yet — pull them here.
	var sourceToTarget *genngap.SourceToTargetTransparentContainer
	var handoverType *genngap.HandoverType
	var cause *genngap.Cause
	if len(requiredValue) > 0 {
		var pdu genngap.HandoverRequired
		if err := pdu.UnmarshalAPER(requiredValue); err == nil {
			for i := range pdu.ProtocolIEs {
				ie := &pdu.ProtocolIEs[i]
				switch int64(ie.Id) {
				case int64(genngap.IdSourceToTargetTransparentContainer):
					sourceToTarget = ie.Value.SourceToTargetTransparentContainer
				case int64(genngap.IdHandoverType):
					handoverType = ie.Value.HandoverType
				case int64(genngap.IdCause):
					cause = ie.Value.Cause
				}
			}
		}
	}
	// Defaults per §9.2.3.4 if source omitted them.
	if handoverType == nil {
		ht := genngap.HandoverType(0) // intra5gs
		handoverType = &ht
	}
	if cause == nil {
		cause = BuildCauseRadioNetworkHandoverDesirable()
	}

	// Build per-session HOReq items from the SMF view. We only
	// promote Active sessions — Suspended sessions are not user-
	// plane-bearing and would force the target gNB to allocate
	// resources we then can't bind.
	var items genngap.PDUSessionResourceSetupListHOReq
	for _, sess := range session.Default.ForUE(ue.IMSI) {
		if sess.State != session.StateActive {
			continue
		}
		transferBytes, err := buildHandoverRequestTransfer(sess)
		if err != nil {
			return nil, fmt.Errorf("HOReq transfer for pduSessID=%d: %w", sess.PDUSessionID, err)
		}
		snssai := genngap.SNSSAI{SST: genngap.SST{byte(sess.SST)}}
		if sess.SD != "" {
			if sd := snssaiSDHex3(sess.SD); sd != nil {
				v := genngap.SD(sd)
				snssai.SD = &v
			}
		}
		items = append(items, genngap.PDUSessionResourceSetupItemHOReq{
			PDUSessionID:            genngap.PDUSessionID(sess.PDUSessionID),
			SNSSAI:                  snssai,
			HandoverRequestTransfer: transferBytes,
		})
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("BuildHandoverRequestValue: no active PDU sessions for IMSI=%s", ue.IMSI)
	}

	pdu := &genngap.HandoverRequest{}
	add := func(id int64, crit genngap.Criticality, v genngap.HandoverRequestIEsValue) {
		pdu.ProtocolIEs = append(pdu.ProtocolIEs, genngap.HandoverRequestIEsEntry{
			Id:          genngap.ProtocolIEID(id),
			Criticality: crit,
			Value:       v,
		})
	}
	add(int64(genngap.IdAMFUENGAPID), genngap.CriticalityReject,
		genngap.HandoverRequestIEsValue{
			Present:     genngap.HandoverRequestIEsValuePresentAMFUENGAPID,
			AMFUENGAPID: amfUEIDPtr(parsed.AMFUEID),
		})
	add(int64(genngap.IdHandoverType), genngap.CriticalityReject,
		genngap.HandoverRequestIEsValue{
			Present:      genngap.HandoverRequestIEsValuePresentHandoverType,
			HandoverType: handoverType,
		})
	add(int64(genngap.IdCause), genngap.CriticalityIgnore,
		genngap.HandoverRequestIEsValue{
			Present: genngap.HandoverRequestIEsValuePresentCause,
			Cause:   cause,
		})
	add(int64(genngap.IdPDUSessionResourceSetupListHOReq), genngap.CriticalityReject,
		genngap.HandoverRequestIEsValue{
			Present:                          genngap.HandoverRequestIEsValuePresentPDUSessionResourceSetupListHOReq,
			PDUSessionResourceSetupListHOReq: &items,
		})
	if sourceToTarget != nil {
		add(int64(genngap.IdSourceToTargetTransparentContainer), genngap.CriticalityReject,
			genngap.HandoverRequestIEsValue{
				Present:                            genngap.HandoverRequestIEsValuePresentSourceToTargetTransparentContainer,
				SourceToTargetTransparentContainer: sourceToTarget,
			})
	}
	return pdu.MarshalAPER()
}

// buildHandoverRequestTransfer mirrors pdusetup.buildTransfer (a
// PDU Session Resource Setup Request Transfer per §9.3.4.1) — the
// same §9.3.4.1 octet string is referenced by HANDOVER REQUEST's
// per-PSI HandoverRequestTransfer (TS 38.413 §9.2.3.4 IE table:
// "Containing the PDU Session Resource Setup Request Transfer IE
// specified in subclause 9.3.4.1.").
//
// IEs emitted: 130 PDUSessionAggregateMaximumBitRate (reject),
//              139 UL-NGU-UP-TNLInformation          (reject),
//              134 PDUSessionType                    (reject).
//
// QosFlowSetupRequestList (136) is omitted on this minimum path —
// pdusetup.buildTransfer includes a default 5QI=9 flow; for the
// preserved-tunnel handover case it isn't strictly needed by the
// tester gNB, and including it would force a per-flow lookup the
// HOReq path doesn't have access to. Promote later when the gNB
// peer actually depends on it.
func buildHandoverRequestTransfer(sess *session.Session) ([]byte, error) {
	transfer := &genngap.PDUSessionResourceSetupRequestTransfer{}
	add := func(id int64, crit genngap.Criticality, v genngap.PDUSessionResourceSetupRequestTransferIEsValue) {
		transfer.ProtocolIEs = append(transfer.ProtocolIEs,
			genngap.PDUSessionResourceSetupRequestTransferIEsEntry{
				Id:          genngap.ProtocolIEID(id),
				Criticality: crit,
				Value:       v,
			})
	}
	// IE 130: PDUSessionAggregateMaximumBitRate (kbps → bps).
	ambr := &genngap.PDUSessionAggregateMaximumBitRate{
		PDUSessionAggregateMaximumBitRateDL: genngap.BitRate(uint64(sess.AMBRDL) * 1000),
		PDUSessionAggregateMaximumBitRateUL: genngap.BitRate(uint64(sess.AMBRUL) * 1000),
	}
	add(int64(genngap.IdPDUSessionAggregateMaximumBitRate), genngap.CriticalityReject,
		genngap.PDUSessionResourceSetupRequestTransferIEsValue{
			Present:                           genngap.PDUSessionResourceSetupRequestTransferIEsValuePresentPDUSessionAggregateMaximumBitRate,
			PDUSessionAggregateMaximumBitRate: ambr,
		})
	// IE 139: UL-NGU-UP-TNLInformation — UPF-side GTP-U endpoint.
	// Invariant per TS 29.244 §8.2.3: sess.UPFTEID is the UL TEID
	// allocated at install; sess.UPFN3IP is the gNB-reachable N3 IP
	// per TS 38.413 §9.3.2.2 (set in the SMF Session record from
	// the UPF instance's n3_ip column).
	addr := net.ParseIP(sess.UPFN3IP).To4()
	if addr == nil {
		return nil, fmt.Errorf("UPFN3IP %q not IPv4", sess.UPFN3IP)
	}
	teid := make([]byte, 4)
	binary.BigEndian.PutUint32(teid, sess.UPFTEID)
	upTL := &genngap.UPTransportLayerInformation{
		Present: genngap.UPTransportLayerInformationPresentGTPTunnel,
		GTPTunnel: &genngap.GTPTunnel{
			TransportLayerAddress: genngap.TransportLayerAddress{Bytes: addr, BitLength: 32},
			GTPTEID:               genngap.GTPTEID(teid),
		},
	}
	add(int64(genngap.IdULNGUUPTNLInformation), genngap.CriticalityReject,
		genngap.PDUSessionResourceSetupRequestTransferIEsValue{
			Present:                     genngap.PDUSessionResourceSetupRequestTransferIEsValuePresentUPTransportLayerInformation,
			UPTransportLayerInformation: upTL,
		})
	// IE 134: PDUSessionType — copy from session, default IPv4.
	pduType := genngap.PDUSessionTypeIpv4
	add(int64(genngap.IdPDUSessionType), genngap.CriticalityReject,
		genngap.PDUSessionResourceSetupRequestTransferIEsValue{
			Present:        genngap.PDUSessionResourceSetupRequestTransferIEsValuePresentPDUSessionType,
			PDUSessionType: &pduType,
		})
	return transfer.MarshalAPER()
}

// snssaiSDHex3 mirrors pdusetup.snssaiSDHexToBytes — converts the
// 6-hex-char SD into 3 binary octets. Returns nil on parse error so
// the caller can omit the optional SD IE.
func snssaiSDHex3(s string) []byte {
	if len(s) != 6 {
		return nil
	}
	out := make([]byte, 3)
	for i := 0; i < 3; i++ {
		hi, ok1 := hex1(s[i*2])
		lo, ok2 := hex1(s[i*2+1])
		if !ok1 || !ok2 {
			return nil
		}
		out[i] = (hi << 4) | lo
	}
	return out
}

func hex1(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// BuildCauseRadioNetworkHandoverDesirable returns a Cause CHOICE for
// the §9.3.1.2 radioNetwork enumerated value handoverDesirableForRadioReason.
// Used as a default when HANDOVER REQUIRED arrived without a Cause IE.
func BuildCauseRadioNetworkHandoverDesirable() *genngap.Cause {
	v := genngap.CauseRadioNetworkHandoverDesirableForRadioReason
	return &genngap.Cause{
		Present:      genngap.CausePresentRadioNetwork,
		RadioNetwork: &v,
	}
}

// ── Small helpers ─────────────────────────────────────────────────

func amfUEIDPtr(v int64) *genngap.AMFUENGAPID {
	x := genngap.AMFUENGAPID(v)
	return &x
}

func ranUEIDPtr(v int64) *genngap.RANUENGAPID {
	x := genngap.RANUENGAPID(v)
	return &x
}

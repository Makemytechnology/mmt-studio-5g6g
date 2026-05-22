// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ngsetup — NG Setup procedure (TS 38.413 §8.7.1).
//
// Go port of nf/amf/ngap/ngap_ng_setup.py. Uses the generated NGAP APER
// codec from codecs/asn1-go for the inner NGSetupRequest / NGSetupResponse
// messages, wrapped via the nf/amf/ngap/wire envelope package (which fills
// the InitiatingMessage/SuccessfulOutcome gap in the generated PDU wrapper).
//
// Procedure (TS 38.413 §8.7.1.2 happy path):
//
//	gNB --[NGSetupRequest / stream 0]--> AMF
//	gNB <--[NGSetupResponse / stream 0]-- AMF
//
// On validation failure (§8.7.1.3) an NGSetupFailure is returned instead.
package ngsetup

import (
	"encoding/hex"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	runtime "github.com/mmt/asn1go/pkg/runtime"
	"github.com/mmt/mmt-studio-core/nf/amf/ctx"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/oam/fm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// Handle is registered for procedureCode=21. Matches the Python reference
// in nf/amf/ngap/ngap_ng_setup.py.
func Handle(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.ngsetup")
	pm.Inc(pm.NGAPSetupAtt, 1)

	log.Debugf("NG Setup inner value (%d bytes): % x", len(env.Value), env.Value)

	// Use the codec's built-in pretty-print to decode and display the full PDU
	if decoded, err := runtime.DecodeAPERToJSON(env.Value, &genngap.NGSetupRequest{}); err == nil {
		log.Debugf("NG Setup Request (decoded):\n%s", decoded)
	} else {
		log.Debugf("NG Setup pretty-decode failed: %v", err)
	}

	var req genngap.NGSetupRequest
	if err := req.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("NG Setup Request decode failed from %s: %v", gnb.GnbIP, err)
		sendFailure(gnb, log, stream, causeProtocol(genngap.CauseProtocolTransferSyntaxError))
		return
	}

	log.Debugf("NG Setup decoded: %d protocolIEs", len(req.ProtocolIEs))
	for i, ie := range req.ProtocolIEs {
		log.Debugf("  IE[%d] id=%d present=%d SupportedTAList=%v",
			i, ie.Id, ie.Value.Present, ie.Value.SupportedTAList != nil)
	}

	// Extract IEs into the gNB context.
	name, id, pagingDRX, supportedTAs := extractIEs(&req)
	gnb.SetGnbInfo(name, id, pagingDRX, supportedTAs)

	log.Infof("NG Setup Request: gNB=%s id=%s TAs=%d PLMNs=%d",
		firstNonEmpty(name, gnb.GnbIP), id, len(supportedTAs), len(gnb.AllPLMNs()))

	// TS 38.413 §8.7.1.2 — SupportedTAList is MANDATORY on NG Setup
	// Request. Missing / empty is a protocol error on the wire (the
	// codec should have rejected decoding, but defense-in-depth).
	// §8.7.1.4 also implies rejection when we can't match any PLMN —
	// an empty TA list trivially fails that check, so fail explicitly
	// rather than let the PLMN loop silently accept.
	if len(supportedTAs) == 0 {
		log.Warnf("NG Setup from %s carried no SupportedTAList — rejecting", gnb.GnbIP)
		sendFailure(gnb, log, stream, causeMisc(genngap.CauseMiscUnknownPLMNOrSNPN))
		return
	}

	// TS 38.413 §8.7.1.4 "Abnormal Conditions":
	//   "If the AMF does not identify any of the PLMNs/SNPNs indicated
	//    in the NG SETUP REQUEST message, it shall reject the NG Setup
	//    procedure with an appropriate cause value."
	// Cause = misc/unknown-PLMN-or-SNPN per Table 9.3.1.2-x.
	var gnbPLMNs [][]byte
	for _, ta := range supportedTAs {
		for _, bp := range ta.BroadcastPLMNs {
			gnbPLMNs = append(gnbPLMNs, bp.PLMN)
		}
	}
	amfPLMNs := ctx.Default.PLMNSupportList()
	if len(amfPLMNs) > 0 {
		configured := make(map[string]struct{}, len(amfPLMNs))
		for _, p := range amfPLMNs {
			configured[string(p.PLMNID)] = struct{}{}
		}
		matched := false
		for _, g := range gnbPLMNs {
			if _, ok := configured[string(g)]; ok {
				matched = true
				break
			}
		}
		if !matched {
			log.Warnf("NG Setup rejected from %s: no matching PLMN (gNB PLMNs=%d, AMF PLMNs=%d)",
				gnb.GnbIP, len(gnbPLMNs), len(amfPLMNs))
			sendFailure(gnb, log, stream, causeMisc(genngap.CauseMiscUnknownPLMNOrSNPN))
			return
		}
	}
	// TODO(spec: TS 38.413 §8.7.1.4 "RATs indicated by the NG-RAN node") —
	//   SupportedTAItem doesn't carry an explicit RAT indicator in
	//   Rel-16; later releases introduce NB-IoT / NR-U suffixes. When
	//   AMF restricts RATs per operator policy, add a check here and
	//   reject with CauseMiscNotEnoughUserPlaneProcessingResources
	//   or similar. Skipped today: we accept any RAT the gNB claims.

	// Build NG Setup Response.
	resp := buildResponse(ctx.Default)
	inner, err := resp.MarshalAPER()
	if err != nil {
		log.Errorf("NG Setup Response encode: %v", err)
		sendFailure(gnb, log, stream, causeProtocol(genngap.CauseProtocolUnspecified))
		return
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.SuccessfulOutcome,
		ProcedureCode: ngap.ProcCodeNGSetup,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		log.Errorf("NG Setup Response envelope: %v", err)
		return
	}
	if err := gnb.Send(pdu, stream); err != nil {
		log.Errorf("NG Setup Response send to %s: %v", gnb.GnbIP, err)
		return
	}
	pm.Inc(pm.NGAPSetupSucc, 1)
	log.Infof("NG Setup accepted for gNB %s", firstNonEmpty(name, gnb.GnbIP))

	// Clear any correlated SCTP-loss alarm from a previous disconnect.
	_, _ = fm.Clear(
		"gNB/"+firstNonEmpty(name, gnb.GnbIP),
		fm.CauseLossOfSignal,
		"SCTP association lost",
		"gNB reconnected — NG Setup accepted",
	)
}

// extractIEs pulls the four IEs the AMF cares about out of the decoded request.
// The rest (R17+ extensions: AIoT, AdditionalULI, …) are ignored for now.
func extractIEs(req *genngap.NGSetupRequest) (name, id, pagingDRX string, tas []gnbctx.SupportedTAItem) {
	for i := range req.ProtocolIEs {
		ie := &req.ProtocolIEs[i]
		switch int64(ie.Id) {
		case int64(genngap.IdGlobalRANNodeID):
			id = formatGlobalRANNodeID(ie.Value.GlobalRANNodeID)
		case int64(genngap.IdRANNodeName):
			if ie.Value.RANNodeName != nil {
				name = string(*ie.Value.RANNodeName)
			}
		case int64(genngap.IdSupportedTAList):
			tas = convertSupportedTAList(ie.Value.SupportedTAList)
		case int64(genngap.IdDefaultPagingDRX):
			if ie.Value.PagingDRX != nil {
				pagingDRX = ie.Value.PagingDRX.String()
			}
		}
	}
	return
}

// formatGlobalRANNodeID renders GlobalGNB-ID as a hex string when present.
// Other branches (N3IWF / NgENB / W-AGF / TNGF) fall through to empty — this
// is only used for logging / display today.
func formatGlobalRANNodeID(g *genngap.GlobalRANNodeID) string {
	if g == nil || g.GlobalGNBID == nil || g.GlobalGNBID.GNBID.GNBID == nil {
		return ""
	}
	return hex.EncodeToString(g.GlobalGNBID.GNBID.GNBID.Bytes)
}

// convertSupportedTAList walks the codec-level SupportedTAList and hands
// back the flat structure used by gnbctx.
func convertSupportedTAList(sta *genngap.SupportedTAList) []gnbctx.SupportedTAItem {
	if sta == nil {
		return nil
	}
	out := make([]gnbctx.SupportedTAItem, 0, len(*sta))
	for _, it := range *sta {
		bcasts := make([]gnbctx.BroadcastPLMN, 0, len(it.BroadcastPLMNList))
		for _, bp := range it.BroadcastPLMNList {
			slices := make([]gnbctx.Slice, 0, len(bp.TAISliceSupportList))
			for _, sl := range bp.TAISliceSupportList {
				// SliceSupportItem → S-NSSAI raw octets (sst [+ sd]).
				var raw []byte
				raw = append(raw, []byte(sl.SNSSAI.SST)...)
				if sl.SNSSAI.SD != nil {
					raw = append(raw, []byte(*sl.SNSSAI.SD)...)
				}
				slices = append(slices, gnbctx.Slice{SNSSAIRaw: raw})
			}
			bcasts = append(bcasts, gnbctx.BroadcastPLMN{
				PLMN:   []byte(bp.PLMNIdentity),
				Slices: slices,
			})
		}
		out = append(out, gnbctx.SupportedTAItem{
			TAC:            []byte(it.TAC),
			BroadcastPLMNs: bcasts,
		})
	}
	return out
}

// buildResponse assembles an NGSetupResponse from the global AMF context.
// IEs per TS 38.413 §9.2.6.2 (all four mandatory when AMF serves any PLMN):
//
//	id=1   AMFName              (reject)
//	id=96  ServedGUAMIList      (reject)
//	id=86  RelativeAMFCapacity  (ignore)
//	id=80  PLMNSupportList      (reject)
//
// Emission order matches the Python reference (same order the spec table
// lists IEs — not strict ID-ascending, but both sides must match for
// diffable captures and for gNBs that trust the sender order).
func buildResponse(a *ctx.AMF) *genngap.NGSetupResponse {
	name := a.Name()
	rANName := genngap.AMFName(name)
	cap := genngap.RelativeAMFCapacity(a.Capacity())

	// ServedGUAMIList (id 96)
	servedGUAMIs := genngap.ServedGUAMIList{}
	for _, g := range a.GUAMIList() {
		servedGUAMIs = append(servedGUAMIs, genngap.ServedGUAMIItem{
			GUAMI: genngap.GUAMI{
				PLMNIdentity: genngap.PLMNIdentity(g.PLMNID),
				AMFRegionID:  genngap.AMFRegionID(runtime.BitString{Bytes: []byte{g.AMFRegionID}, BitLength: 8}),
				AMFSetID:     genngap.AMFSetID(runtime.BitString{Bytes: uint16ToBytes(g.AMFSetID, 10), BitLength: 10}),
				// 6-bit AMFPointer must be MSB-aligned in the byte so the
				// BitString serializer (reads MSB→LSB) emits the integer
				// value faithfully. Matching fix in initialctxsetup.go.
				AMFPointer: genngap.AMFPointer(runtime.BitString{Bytes: []byte{(g.AMFPointer & 0x3F) << 2}, BitLength: 6}),
			},
		})
	}

	// PLMNSupportList (id 80) — one item per PLMN, each with its slice
	// list (S-NSSAIs from supported_plmns). The gNB uses this to decide
	// which slices it may advertise to UEs camped on this AMF.
	plmnSupports := genngap.PLMNSupportList{}
	for _, p := range a.PLMNSupportList() {
		slices := genngap.SliceSupportList{}
		for _, sl := range p.Slices {
			s := genngap.SNSSAI{
				SST: genngap.SST{sl.SST},
			}
			if len(sl.SD) == 3 {
				sd := genngap.SD(append([]byte(nil), sl.SD...))
				s.SD = &sd
			}
			slices = append(slices, genngap.SliceSupportItem{SNSSAI: s})
		}
		plmnSupports = append(plmnSupports, genngap.PLMNSupportItem{
			PLMNIdentity:     genngap.PLMNIdentity(append([]byte(nil), p.PLMNID...)),
			SliceSupportList: slices,
		})
	}

	resp := &genngap.NGSetupResponse{}
	resp.ProtocolIEs = append(resp.ProtocolIEs, genngap.NGSetupResponseIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdAMFName),
		Criticality: genngap.CriticalityReject,
		Value: genngap.NGSetupResponseIEsValue{
			Present: genngap.NGSetupResponseIEsValuePresentAMFName,
			AMFName: &rANName,
		},
	})
	if len(servedGUAMIs) > 0 {
		resp.ProtocolIEs = append(resp.ProtocolIEs, genngap.NGSetupResponseIEsEntry{
			Id:          genngap.ProtocolIEID(genngap.IdServedGUAMIList),
			Criticality: genngap.CriticalityReject,
			Value: genngap.NGSetupResponseIEsValue{
				Present:         genngap.NGSetupResponseIEsValuePresentServedGUAMIList,
				ServedGUAMIList: &servedGUAMIs,
			},
		})
	}
	resp.ProtocolIEs = append(resp.ProtocolIEs, genngap.NGSetupResponseIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdRelativeAMFCapacity),
		Criticality: genngap.CriticalityIgnore,
		Value: genngap.NGSetupResponseIEsValue{
			Present:             genngap.NGSetupResponseIEsValuePresentRelativeAMFCapacity,
			RelativeAMFCapacity: &cap,
		},
	})
	if len(plmnSupports) > 0 {
		resp.ProtocolIEs = append(resp.ProtocolIEs, genngap.NGSetupResponseIEsEntry{
			Id:          genngap.ProtocolIEID(genngap.IdPLMNSupportList),
			Criticality: genngap.CriticalityReject,
			Value: genngap.NGSetupResponseIEsValue{
				Present:         genngap.NGSetupResponseIEsValuePresentPLMNSupportList,
				PLMNSupportList: &plmnSupports,
			},
		})
	}
	return resp
}

// sendFailure builds + ships an NGSetupFailure PDU per TS 38.413
// §9.2.6.3. Cause IE is MANDATORY (criticality=ignore). Per the spec
// abnormal-cases table §8.7.1.4:
//
//   cause=misc/unknown-PLMN-or-SNPN — when the gNB's broadcast PLMNs
//      intersect nothing in the AMF's served-PLMN list.
//   cause=protocol/transfer-syntax-error — decode failure on the
//      inbound NGSetupRequest.
//   cause=protocol/unspecified — catch-all encode / internal failure.
//
// Before this, sendFailure shipped an Unsuccessful-Outcome envelope
// with an empty value field — spec-wrong (Cause IE is mandatory) and
// some gNBs reject or log-drop the malformed failure. Now encoded
// properly with the requested cause.
func sendFailure(gnb *gnbctx.GnbCtx, log *logger.Logger, stream int, cause *genngap.Cause) {
	pm.Inc(pm.NGAPSetupFail, 1)

	fail := &genngap.NGSetupFailure{
		ProtocolIEs: []genngap.NGSetupFailureIEsEntry{
			{
				Id:          genngap.ProtocolIEID(genngap.IdCause),
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.NGSetupFailureIEsValue{
					Present: genngap.NGSetupFailureIEsValuePresentCause,
					Cause:   cause,
				},
			},
		},
	}
	inner, err := fail.MarshalAPER()
	if err != nil {
		log.Errorf("NGSetupFailure encode: %v — shipping empty envelope as fallback", err)
		inner = nil
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.UnsuccessfulOutcome,
		ProcedureCode: ngap.ProcCodeNGSetup,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		log.Errorf("NGSetupFailure envelope: %v", err)
		return
	}
	if err := gnb.Send(pdu, stream); err != nil {
		log.Errorf("NGSetupFailure send: %v", err)
	}
	log.Warnf("NGSetupFailure to %s: %s", gnb.GnbIP, formatCauseForLog(cause))
}

// Convenience constructors for the three causes NG Setup produces.
func causeMisc(v genngap.CauseMisc) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentMisc, Misc: &v}
}
func causeProtocol(v genngap.CauseProtocol) *genngap.Cause {
	return &genngap.Cause{Present: genngap.CausePresentProtocol, Protocol: &v}
}

// formatCauseForLog renders the Cause CHOICE to a short log-friendly
// string. Duplicated from initialctxsetup.formatCause to avoid adding
// a cross-package dependency for one helper.
func formatCauseForLog(c *genngap.Cause) string {
	if c == nil {
		return "(nil)"
	}
	switch c.Present {
	case genngap.CausePresentMisc:
		if c.Misc != nil {
			return fmt.Sprintf("misc(%d)", int64(*c.Misc))
		}
	case genngap.CausePresentProtocol:
		if c.Protocol != nil {
			return fmt.Sprintf("protocol(%d)", int64(*c.Protocol))
		}
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
	}
	return "unknown"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// uint16ToBytes packs a value of nBits (1..16) MSB-first, left-justified
// inside the minimum number of octets. Used for AMFSetID (10 bits) and
// AMFPointer (6 bits) where the codec expects partial-octet buffers.
func uint16ToBytes(v uint16, nBits int) []byte {
	oct := (nBits + 7) / 8
	out := make([]byte, oct)
	shift := uint(oct*8 - nBits)
	v <<= shift
	for i := 0; i < oct; i++ {
		out[i] = byte(v >> uint((oct-1-i)*8))
	}
	return out
}

// Register installs Handle on the dispatcher. Call from AMF bootstrap.
func Register() { ngap.Register(ngap.ProcCodeNGSetup, Handle) }

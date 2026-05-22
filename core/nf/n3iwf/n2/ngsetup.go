// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package n2

import (
	"errors"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	runtime "github.com/mmt/asn1go/pkg/runtime"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
)

// ProcCodeNGSetup is the NGAP elementary procedure code for the
// NG SETUP procedure (TS 38.413 §9.3.4 / §8.7.1). Re-stated here so
// the n2 package doesn't import the AMF dispatch table.
const ProcCodeNGSetup = 21

// NGSetupConfig holds the data the N3IWF embeds in its NG SETUP
// REQUEST per TS 38.413 §9.2.6.1.
//
//	GlobalRANNodeID  M  (CHOICE→GlobalN3IWFID per §9.3.1.5)
//	RANNodeName      O  (PrintableString, ≤150 octets)
//	SupportedTAList  M  (≥1 TAI, each with ≥1 BroadcastPLMN)
//	DefaultPagingDRX M  (the spec keeps this even on the N3IWF — the
//	                     AMF ignores DRX for non-3GPP access but the
//	                     IE is mandatory presence-wise)
//
// Multi-octet PLMN-Id is encoded MCC/MNC-BCD (3 octets) per
// TS 23.003 §2.3.
type NGSetupConfig struct {
	// PLMNID is the 3-octet BCD-encoded MCC+MNC pair. Caller is
	// responsible for the BCD packing — utility helpers live in
	// nf/amf/ctx/plmn.go.
	PLMNID []byte

	// N3IWFID is a 16-bit operator-assigned identifier (TS 38.413
	// §9.3.1.5). Distinct from a gNB-Id; range 0..65535.
	N3IWFID uint16

	// Name is the printable RAN node name (≤150 octets). Empty
	// omits the IE.
	Name string

	// TACs are the 3-octet Tracking Area Codes the N3IWF advertises.
	// Each becomes one SupportedTAItem with the full PLMN list as
	// BroadcastPLMNList. Most untrusted-non-3GPP deployments serve a
	// single TAC.
	TACs [][]byte

	// SupportedSNSSAIs is the slice list broadcast in every
	// BroadcastPLMNItem.TAISliceSupportList. At least one entry is
	// required per TS 38.413 §9.2.6.1.
	SupportedSNSSAIs []SNSSAI
}

// SNSSAI is one (SST, SD?) pair used as the slice key in NG SETUP
// and InitialUEMessage.
type SNSSAI struct {
	SST byte
	SD  []byte // 3 octets, optional
}

// EncodeNGSetupRequest builds and APER-encodes a complete NGAP-PDU
// envelope carrying an NG SETUP REQUEST per TS 38.413 §9.2.6.1.
// Returns the on-the-wire bytes ready for n2.Conn.Send on stream 0
// (TS 38.412 §7).
//
// IE criticalities follow §9.2.6.1 verbatim:
//
//	GlobalRANNodeID    reject
//	RANNodeName        ignore
//	SupportedTAList    reject
//	DefaultPagingDRX   ignore
func EncodeNGSetupRequest(cfg *NGSetupConfig) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("n2: NGSetupConfig nil")
	}
	if len(cfg.PLMNID) != 3 {
		return nil, fmt.Errorf("n2: PLMNID must be 3 octets BCD-encoded (TS 23.003 §2.3), got %d", len(cfg.PLMNID))
	}
	if len(cfg.TACs) == 0 {
		return nil, errors.New("n2: at least one TAC required (TS 38.413 §9.2.6.1)")
	}
	if len(cfg.SupportedSNSSAIs) == 0 {
		return nil, errors.New("n2: at least one S-NSSAI required (TS 38.413 §9.2.6.1)")
	}

	// GlobalRANNodeID = CHOICE.GlobalN3IWFID per §9.3.1.5
	n3iwfID := genngap.N3IWFID{
		Present: genngap.N3IWFIDPresentN3IWFID,
		N3IWFID: &runtime.BitString{
			Bytes:     []byte{byte(cfg.N3IWFID >> 8), byte(cfg.N3IWFID)},
			BitLength: 16,
		},
	}
	ranNode := genngap.GlobalRANNodeID{
		Present: genngap.GlobalRANNodeIDPresentGlobalN3IWFID,
		GlobalN3IWFID: &genngap.GlobalN3IWFID{
			PLMNIdentity: genngap.PLMNIdentity(append([]byte(nil), cfg.PLMNID...)),
			N3IWFID:      n3iwfID,
		},
	}

	// SupportedTAList — one item per TAC, each advertising the same
	// PLMN+slice list. This mirrors the gNB convention from
	// nf/amf/ngap/ngsetup; if multi-PLMN/N3IWF support is needed the
	// caller can extend this builder.
	slices := genngap.SliceSupportList{}
	for _, s := range cfg.SupportedSNSSAIs {
		item := genngap.SliceSupportItem{
			SNSSAI: genngap.SNSSAI{SST: genngap.SST{s.SST}},
		}
		if len(s.SD) == 3 {
			sd := genngap.SD(append([]byte(nil), s.SD...))
			item.SNSSAI.SD = &sd
		}
		slices = append(slices, item)
	}
	taList := genngap.SupportedTAList{}
	for _, tac := range cfg.TACs {
		if len(tac) != 3 {
			return nil, fmt.Errorf("n2: TAC must be 3 octets (TS 38.413 §9.3.3.10), got %d", len(tac))
		}
		taList = append(taList, genngap.SupportedTAItem{
			TAC: genngap.TAC(append([]byte(nil), tac...)),
			BroadcastPLMNList: genngap.BroadcastPLMNList{
				{
					PLMNIdentity:        genngap.PLMNIdentity(append([]byte(nil), cfg.PLMNID...)),
					TAISliceSupportList: slices,
				},
			},
		})
	}

	// DefaultPagingDRX — v32 (=2) is the spec's pre-Rel-15 default;
	// any concrete value is fine, the AMF ignores it for
	// non-3GPP access. The IE is mandatory regardless.
	drx := genngap.PagingDRX(2) // v32

	req := &genngap.NGSetupRequest{}
	req.ProtocolIEs = append(req.ProtocolIEs, genngap.NGSetupRequestIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdGlobalRANNodeID),
		Criticality: genngap.CriticalityReject,
		Value: genngap.NGSetupRequestIEsValue{
			Present:         genngap.NGSetupRequestIEsValuePresentGlobalRANNodeID,
			GlobalRANNodeID: &ranNode,
		},
	})
	if cfg.Name != "" {
		if len(cfg.Name) > 150 {
			return nil, fmt.Errorf("n2: RANNodeName too long (TS 38.413 §9.3.1.21 max 150): %d", len(cfg.Name))
		}
		ranName := genngap.RANNodeName(cfg.Name)
		req.ProtocolIEs = append(req.ProtocolIEs, genngap.NGSetupRequestIEsEntry{
			Id:          genngap.ProtocolIEID(genngap.IdRANNodeName),
			Criticality: genngap.CriticalityIgnore,
			Value: genngap.NGSetupRequestIEsValue{
				Present:     genngap.NGSetupRequestIEsValuePresentRANNodeName,
				RANNodeName: &ranName,
			},
		})
	}
	req.ProtocolIEs = append(req.ProtocolIEs, genngap.NGSetupRequestIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdSupportedTAList),
		Criticality: genngap.CriticalityReject,
		Value: genngap.NGSetupRequestIEsValue{
			Present:         genngap.NGSetupRequestIEsValuePresentSupportedTAList,
			SupportedTAList: &taList,
		},
	})
	req.ProtocolIEs = append(req.ProtocolIEs, genngap.NGSetupRequestIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdDefaultPagingDRX),
		Criticality: genngap.CriticalityIgnore,
		Value: genngap.NGSetupRequestIEsValue{
			Present:   genngap.NGSetupRequestIEsValuePresentPagingDRX,
			PagingDRX: &drx,
		},
	})

	inner, err := req.MarshalAPER()
	if err != nil {
		return nil, fmt.Errorf("n2: NGSetupRequest APER encode: %w", err)
	}

	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeNGSetup,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		return nil, fmt.Errorf("n2: NGSetupRequest envelope: %w", err)
	}
	return pdu, nil
}

// DecodeNGSetupResponse pulls an NGSetupResponse out of an
// NGAP-PDU/SuccessfulOutcome envelope. Returns the parsed message so
// the N3IWF can index into ServedGUAMIList / RelativeAMFCapacity etc.
func DecodeNGSetupResponse(pdu []byte) (*genngap.NGSetupResponse, error) {
	env, err := wire.Decode(pdu)
	if err != nil {
		return nil, fmt.Errorf("n2: NGAP-PDU decode: %w", err)
	}
	if env.Type != wire.SuccessfulOutcome {
		return nil, fmt.Errorf("n2: expected SuccessfulOutcome, got %s", env.Type)
	}
	if env.ProcedureCode != ProcCodeNGSetup {
		return nil, fmt.Errorf("n2: expected NG Setup procedure code (21), got %d", env.ProcedureCode)
	}
	resp := &genngap.NGSetupResponse{}
	if err := resp.UnmarshalAPER(env.Value); err != nil {
		return nil, fmt.Errorf("n2: NGSetupResponse decode: %w", err)
	}
	return resp, nil
}

// DecodeNGSetupFailure pulls an NGSetupFailure out of an
// NGAP-PDU/UnsuccessfulOutcome envelope.
func DecodeNGSetupFailure(pdu []byte) (*genngap.NGSetupFailure, error) {
	env, err := wire.Decode(pdu)
	if err != nil {
		return nil, fmt.Errorf("n2: NGAP-PDU decode: %w", err)
	}
	if env.Type != wire.UnsuccessfulOutcome {
		return nil, fmt.Errorf("n2: expected UnsuccessfulOutcome, got %s", env.Type)
	}
	if env.ProcedureCode != ProcCodeNGSetup {
		return nil, fmt.Errorf("n2: expected NG Setup procedure code (21), got %d", env.ProcedureCode)
	}
	fail := &genngap.NGSetupFailure{}
	if err := fail.UnmarshalAPER(env.Value); err != nil {
		return nil, fmt.Errorf("n2: NGSetupFailure decode: %w", err)
	}
	return fail, nil
}

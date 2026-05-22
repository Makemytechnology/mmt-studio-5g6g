// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package n2

import (
	"errors"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
)

// ProcCodeInitialContextSetup is the NGAP procedure code for the
// Initial Context Setup procedure (TS 38.413 §8.3.1 / §9.2.2.1).
const ProcCodeInitialContextSetup = 14

// InitialContextSetup is the parsed form of an
// InitialContextSetupRequest received by the N3IWF from the AMF.
//
// The interesting IEs for non-3GPP access (TS 24.502 §7.4 + TS 33.501
// §6.5.2):
//
//	AMFUENGAPID            M  reject — AMF's UE id (echo on every UL)
//	RANUENGAPID            M  reject — N3IWF's UE id (already known)
//	GUAMI                  M  reject — for AMF set tracking
//	AllowedNSSAI           M  reject — slices the UE may use
//	UESecurityCapabilities M  reject — feeds NAS SMC + IPsec
//	SecurityKey            M  reject — 256-bit Knh (TS 33.501 §6.5.2)
//	NASPDU                 O  ignore — usually the Registration Accept
//
// Knh is what the N3IWF feeds into TS 24.502 §7.4 to derive the
// child-SA encryption + integrity keys for the upcoming
// CREATE_CHILD_SA exchanges. Stored as the raw 32 octets here; the
// IPsec layer (task #20) does the KDF.
type InitialContextSetup struct {
	AMFUENGAPID uint64
	RANUENGAPID uint32
	GUAMI       *genngap.GUAMI

	// AllowedNSSAI is the per-UE slice list. Empty entries are
	// preserved as nil rather than mistaken for "no allowed slice".
	AllowedNSSAI []SNSSAI

	// UESecurityCaps is the four-mask UE capability bitfield.
	UESecurityCaps *genngap.UESecurityCapabilities

	// Knh is the 32-octet (256-bit) security key per TS 33.501
	// §6.5.2 — the N3IWF derives the IPsec child-SA encryption +
	// integrity keys from this via TS 24.502 §7.4.
	Knh []byte

	// NASPDU is optionally piggybacked on the Initial Context Setup
	// (typical case: Registration Accept). nil if absent.
	NASPDU []byte
}

// DecodeInitialContextSetupRequest unwraps an NGAP-PDU envelope
// containing an InitialContextSetupRequest (TS 38.413 §9.2.2.1) and
// returns the IEs the N3IWF needs. Mandatory-IE absence is reported
// as an error so the caller can ship an InitialContextSetupFailure.
func DecodeInitialContextSetupRequest(pdu []byte) (*InitialContextSetup, error) {
	env, err := wire.Decode(pdu)
	if err != nil {
		return nil, fmt.Errorf("n2: NGAP-PDU decode: %w", err)
	}
	if env.Type != wire.InitiatingMessage {
		return nil, fmt.Errorf("n2: ICS not InitiatingMessage (got %s)", env.Type)
	}
	if env.ProcedureCode != ProcCodeInitialContextSetup {
		return nil, fmt.Errorf("n2: expected Initial Context Setup (14), got procedureCode=%d", env.ProcedureCode)
	}
	req := &genngap.InitialContextSetupRequest{}
	if err := req.UnmarshalAPER(env.Value); err != nil {
		return nil, fmt.Errorf("n2: InitialContextSetupRequest APER decode: %w", err)
	}
	out := &InitialContextSetup{}
	for _, ie := range req.ProtocolIEs {
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				out.AMFUENGAPID = uint64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				out.RANUENGAPID = uint32(*ie.Value.RANUENGAPID)
			}
		case int64(genngap.IdGUAMI):
			out.GUAMI = ie.Value.GUAMI
		case int64(genngap.IdAllowedNSSAI):
			if ie.Value.AllowedNSSAI != nil {
				for _, item := range *ie.Value.AllowedNSSAI {
					s := SNSSAI{SST: item.SNSSAI.SST[0]}
					if item.SNSSAI.SD != nil {
						s.SD = append([]byte(nil), []byte(*item.SNSSAI.SD)...)
					}
					out.AllowedNSSAI = append(out.AllowedNSSAI, s)
				}
			}
		case int64(genngap.IdUESecurityCapabilities):
			out.UESecurityCaps = ie.Value.UESecurityCapabilities
		case int64(genngap.IdSecurityKey):
			if ie.Value.SecurityKey != nil {
				bs := *ie.Value.SecurityKey
				if bs.BitLength != 256 {
					return nil, fmt.Errorf("n2: SecurityKey bit-length %d != 256 (TS 33.501 §6.5.2)",
						bs.BitLength)
				}
				out.Knh = append([]byte(nil), bs.Bytes...)
			}
		case int64(genngap.IdNASPDU):
			if ie.Value.NASPDU != nil {
				out.NASPDU = append([]byte(nil), []byte(*ie.Value.NASPDU)...)
			}
		}
	}
	if out.AMFUENGAPID == 0 {
		return nil, errors.New("n2: ICS missing AMF-UE-NGAP-ID (M, TS 38.413 §9.2.2.1)")
	}
	if len(out.Knh) != 32 {
		return nil, errors.New("n2: ICS missing or short SecurityKey (M, 32 octets, TS 33.501 §6.5.2)")
	}
	if out.UESecurityCaps == nil {
		return nil, errors.New("n2: ICS missing UESecurityCapabilities (M, TS 38.413 §9.2.2.1)")
	}
	return out, nil
}

// EncodeInitialContextSetupResponse builds the success response per
// TS 38.413 §9.2.2.2. Minimal mandatory shape: AMFUENGAPID +
// RANUENGAPID. PDU-session IEs (CritList) only apply when the AMF
// included PDUSessionResourceSetupListCxtReq in the request — for the
// pure NAS-only path (Registration Complete with no PDU session) the
// response is just the two NGAP-IDs.
func EncodeInitialContextSetupResponse(amfUEID uint64, ranUEID uint32) ([]byte, error) {
	amfID := genngap.AMFUENGAPID(amfUEID)
	ranID := genngap.RANUENGAPID(ranUEID)
	resp := &genngap.InitialContextSetupResponse{
		ProtocolIEs: []genngap.InitialContextSetupResponseIEsEntry{
			{
				Id:          genngap.ProtocolIEID(genngap.IdAMFUENGAPID),
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.InitialContextSetupResponseIEsValue{
					Present:     genngap.InitialContextSetupResponseIEsValuePresentAMFUENGAPID,
					AMFUENGAPID: &amfID,
				},
			},
			{
				Id:          genngap.ProtocolIEID(genngap.IdRANUENGAPID),
				Criticality: genngap.CriticalityIgnore,
				Value: genngap.InitialContextSetupResponseIEsValue{
					Present:     genngap.InitialContextSetupResponseIEsValuePresentRANUENGAPID,
					RANUENGAPID: &ranID,
				},
			},
		},
	}
	inner, err := resp.MarshalAPER()
	if err != nil {
		return nil, fmt.Errorf("n2: ICS response encode: %w", err)
	}
	return wire.Encode(&wire.Envelope{
		Type:          wire.SuccessfulOutcome,
		ProcedureCode: ProcCodeInitialContextSetup,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
}

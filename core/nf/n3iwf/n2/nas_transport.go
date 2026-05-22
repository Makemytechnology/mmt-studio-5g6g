// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package n2

import (
	"errors"
	"fmt"
	"net"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	runtime "github.com/mmt/asn1go/pkg/runtime"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
)

// NGAP procedure codes used by the N3IWF on the NAS-transport plane.
// These mirror the constants in nf/amf/ngap/dispatch.go but are
// re-stated locally to keep n2 free of AMF imports.
const (
	ProcCodeDownlinkNASTransport = 4  // TS 38.413 §8.6.2 / §9.2.5.2
	ProcCodeInitialUEMessage     = 15 // TS 38.413 §8.6.3 / §9.2.5.3
	ProcCodeUplinkNASTransport   = 46 // TS 38.413 §8.6.1 / §9.2.5.1
)

// InitialUEMessageConfig parameterises the InitialUEMessage the N3IWF
// sends to the AMF after a UE completes IKE_AUTH and the first
// EAP-Response/5G-NAS arrives carrying the UE's Registration Request.
//
// Per TS 38.413 §9.2.5.3 / §9.2.5.3.1 the mandatory IEs for an N3IWF
// peer are:
//
//	RANUENGAPID              M  reject  — the N3IWF-allocated UE id
//	NASPDU                   M  reject  — the unwrapped NAS PDU from EAP-5G
//	UserLocationInformation  M  reject  — N3IWF variant: UE outer IP+port
//	RRCEstablishmentCause    M  ignore  — pinned to mo-Signalling for
//	                                       non-3GPP access (UE has no RRC)
//
// PLMNIdentity is included when the UE is multi-PLMN-capable (§9.2.5.3
// notes the IE applies to non-3GPP access too).
type InitialUEMessageConfig struct {
	RANUENGAPID uint32

	// NASPDU is the bytes the UE handed us inside the EAP-Response/
	// 5G-NAS Message-Id=2 message — typically a Registration Request.
	NASPDU []byte

	// UEOuterIP is the UE's IP address as seen on the N3IWF's NWu
	// (untrusted-non-3GPP) IPsec listener. IPv4 = 4 octets, IPv6 = 16.
	UEOuterIP net.IP

	// UEOuterPort is the UE's UDP/4500 source port as seen by the
	// N3IWF (TS 23.501 §4.4.2 — captured for AMF correlation).
	UEOuterPort uint16

	// PLMNID is the 3-octet BCD MCC+MNC the UE is attaching to. May
	// be empty in single-PLMN deployments — the IE is then omitted.
	PLMNID []byte
}

// EncodeInitialUEMessage builds and APER-encodes a complete NGAP-PDU
// envelope carrying an InitialUEMessage per TS 38.413 §9.2.5.3.
// Returned bytes go on stream > 0 (TS 38.412 §7 — UE-associated).
func EncodeInitialUEMessage(cfg *InitialUEMessageConfig) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("n2: InitialUEMessageConfig nil")
	}
	if len(cfg.NASPDU) == 0 {
		return nil, errors.New("n2: NAS-PDU empty (TS 38.413 §9.2.5.3)")
	}
	ip4 := cfg.UEOuterIP.To4()
	ip16 := cfg.UEOuterIP.To16()
	if ip4 == nil && ip16 == nil {
		return nil, errors.New("n2: UEOuterIP missing/invalid")
	}

	ranID := genngap.RANUENGAPID(cfg.RANUENGAPID)
	nas := genngap.NASPDU(append([]byte(nil), cfg.NASPDU...))

	// UserLocationInformation = CHOICE.UserLocationInformationN3IWFWithPortNumber
	// per TS 38.413 §9.3.1.16 / §9.3.1.16.x.
	//
	// TransportLayerAddress is a sized BIT STRING (1..160 bits) — RFC 1166
	// IPv4 ⇒ 32 bits, RFC 8200 IPv6 ⇒ 128 bits. PortNumber is a 2-octet
	// OCTET STRING.
	var addr runtime.BitString
	if ip4 != nil {
		addr = runtime.BitString{Bytes: append([]byte(nil), ip4...), BitLength: 32}
	} else {
		addr = runtime.BitString{Bytes: append([]byte(nil), ip16...), BitLength: 128}
	}
	uli := genngap.UserLocationInformation{
		Present: genngap.UserLocationInformationPresentUserLocationInformationN3IWFWithPortNumber,
		UserLocationInformationN3IWFWithPortNumber: &genngap.UserLocationInformationN3IWFWithPortNumber{
			IPAddress:  genngap.TransportLayerAddress(addr),
			PortNumber: genngap.PortNumber(append([]byte(nil), byte(cfg.UEOuterPort>>8), byte(cfg.UEOuterPort))),
		},
	}

	// RRCEstablishmentCause: non-3GPP UEs have no RRC. The §9.3.1.111
	// table doesn't define a "non-3GPP" value — the canonical
	// implementation choice is mo-Signalling (3) so the AMF treats
	// this as a signalling-driven attach. Emergency cases would use
	// emergency (0) at the policy layer.
	cause := genngap.RRCEstablishmentCauseMoSignalling

	msg := &genngap.InitialUEMessage{}
	msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.InitialUEMessageIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdRANUENGAPID),
		Criticality: genngap.CriticalityReject,
		Value: genngap.InitialUEMessageIEsValue{
			Present:     genngap.InitialUEMessageIEsValuePresentRANUENGAPID,
			RANUENGAPID: &ranID,
		},
	})
	msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.InitialUEMessageIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdNASPDU),
		Criticality: genngap.CriticalityReject,
		Value: genngap.InitialUEMessageIEsValue{
			Present: genngap.InitialUEMessageIEsValuePresentNASPDU,
			NASPDU:  &nas,
		},
	})
	msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.InitialUEMessageIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdUserLocationInformation),
		Criticality: genngap.CriticalityReject,
		Value: genngap.InitialUEMessageIEsValue{
			Present:                 genngap.InitialUEMessageIEsValuePresentUserLocationInformation,
			UserLocationInformation: &uli,
		},
	})
	msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.InitialUEMessageIEsEntry{
		Id:          genngap.ProtocolIEID(genngap.IdRRCEstablishmentCause),
		Criticality: genngap.CriticalityIgnore,
		Value: genngap.InitialUEMessageIEsValue{
			Present:               genngap.InitialUEMessageIEsValuePresentRRCEstablishmentCause,
			RRCEstablishmentCause: &cause,
		},
	})
	if len(cfg.PLMNID) == 3 {
		plmn := genngap.PLMNIdentity(append([]byte(nil), cfg.PLMNID...))
		msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.InitialUEMessageIEsEntry{
			Id:          genngap.ProtocolIEID(genngap.IdSelectedPLMNIdentity),
			Criticality: genngap.CriticalityIgnore,
			Value: genngap.InitialUEMessageIEsValue{
				Present:      genngap.InitialUEMessageIEsValuePresentPLMNIdentity,
				PLMNIdentity: &plmn,
			},
		})
	}

	inner, err := msg.MarshalAPER()
	if err != nil {
		return nil, fmt.Errorf("n2: InitialUEMessage encode: %w", err)
	}
	return wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeInitialUEMessage,
		Criticality:   wire.CriticalityIgnore,
		Value:         inner,
	})
}

// EncodeUplinkNASTransport builds the §9.2.5.1 message used for every
// subsequent NAS PDU (after the initial Registration Request) the
// N3IWF relays from the UE to the AMF. Mandatory IEs:
//
//	AMFUENGAPID              M  reject
//	RANUENGAPID              M  reject
//	NASPDU                   M  reject
//	UserLocationInformation  M  ignore
func EncodeUplinkNASTransport(amfUEID uint64, ranUEID uint32, nasPDU []byte, ueIP net.IP, uePort uint16) ([]byte, error) {
	if len(nasPDU) == 0 {
		return nil, errors.New("n2: NAS-PDU empty (TS 38.413 §9.2.5.1)")
	}
	ip4 := ueIP.To4()
	ip16 := ueIP.To16()
	if ip4 == nil && ip16 == nil {
		return nil, errors.New("n2: UEOuterIP missing/invalid")
	}
	amfID := genngap.AMFUENGAPID(amfUEID)
	ranID := genngap.RANUENGAPID(ranUEID)
	nas := genngap.NASPDU(append([]byte(nil), nasPDU...))
	var addr runtime.BitString
	if ip4 != nil {
		addr = runtime.BitString{Bytes: append([]byte(nil), ip4...), BitLength: 32}
	} else {
		addr = runtime.BitString{Bytes: append([]byte(nil), ip16...), BitLength: 128}
	}
	uli := genngap.UserLocationInformation{
		Present: genngap.UserLocationInformationPresentUserLocationInformationN3IWFWithPortNumber,
		UserLocationInformationN3IWFWithPortNumber: &genngap.UserLocationInformationN3IWFWithPortNumber{
			IPAddress:  genngap.TransportLayerAddress(addr),
			PortNumber: genngap.PortNumber(append([]byte(nil), byte(uePort>>8), byte(uePort))),
		},
	}

	msg := &genngap.UplinkNASTransport{}
	msg.ProtocolIEs = []genngap.UplinkNASTransportIEsEntry{
		{
			Id:          genngap.ProtocolIEID(genngap.IdAMFUENGAPID),
			Criticality: genngap.CriticalityReject,
			Value: genngap.UplinkNASTransportIEsValue{
				Present:     genngap.UplinkNASTransportIEsValuePresentAMFUENGAPID,
				AMFUENGAPID: &amfID,
			},
		},
		{
			Id:          genngap.ProtocolIEID(genngap.IdRANUENGAPID),
			Criticality: genngap.CriticalityReject,
			Value: genngap.UplinkNASTransportIEsValue{
				Present:     genngap.UplinkNASTransportIEsValuePresentRANUENGAPID,
				RANUENGAPID: &ranID,
			},
		},
		{
			Id:          genngap.ProtocolIEID(genngap.IdNASPDU),
			Criticality: genngap.CriticalityReject,
			Value: genngap.UplinkNASTransportIEsValue{
				Present: genngap.UplinkNASTransportIEsValuePresentNASPDU,
				NASPDU:  &nas,
			},
		},
		{
			Id:          genngap.ProtocolIEID(genngap.IdUserLocationInformation),
			Criticality: genngap.CriticalityIgnore,
			Value: genngap.UplinkNASTransportIEsValue{
				Present:                 genngap.UplinkNASTransportIEsValuePresentUserLocationInformation,
				UserLocationInformation: &uli,
			},
		},
	}

	inner, err := msg.MarshalAPER()
	if err != nil {
		return nil, fmt.Errorf("n2: UplinkNASTransport encode: %w", err)
	}
	return wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeUplinkNASTransport,
		Criticality:   wire.CriticalityIgnore,
		Value:         inner,
	})
}

// DecodeDownlinkNASTransport unwraps an incoming NGAP-PDU envelope
// containing a DownlinkNASTransport (§9.2.5.2) and returns the
// AMF-side NAS PDU + AMF-UE-NGAP-ID + RAN-UE-NGAP-ID. The N3IWF then
// wraps the NAS PDU into an EAP-Request/5G-NAS and ships it inside an
// IKE_AUTH SK to the UE.
type DownlinkNAS struct {
	AMFUENGAPID uint64
	RANUENGAPID uint32
	NASPDU      []byte
}

func DecodeDownlinkNASTransport(pdu []byte) (*DownlinkNAS, error) {
	env, err := wire.Decode(pdu)
	if err != nil {
		return nil, fmt.Errorf("n2: NGAP-PDU decode: %w", err)
	}
	if env.Type != wire.InitiatingMessage {
		return nil, fmt.Errorf("n2: DownlinkNASTransport not InitiatingMessage (got %s)", env.Type)
	}
	if env.ProcedureCode != ProcCodeDownlinkNASTransport {
		return nil, fmt.Errorf("n2: expected DL NAS Transport (4), got procedureCode=%d", env.ProcedureCode)
	}
	dl := &genngap.DownlinkNASTransport{}
	if err := dl.UnmarshalAPER(env.Value); err != nil {
		return nil, fmt.Errorf("n2: DownlinkNASTransport APER decode: %w", err)
	}
	out := &DownlinkNAS{}
	for _, ie := range dl.ProtocolIEs {
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				out.AMFUENGAPID = uint64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				out.RANUENGAPID = uint32(*ie.Value.RANUENGAPID)
			}
		case int64(genngap.IdNASPDU):
			if ie.Value.NASPDU != nil {
				out.NASPDU = append([]byte(nil), []byte(*ie.Value.NASPDU)...)
			}
		}
	}
	if len(out.NASPDU) == 0 {
		return nil, errors.New("n2: DownlinkNASTransport missing mandatory NAS-PDU IE")
	}
	return out, nil
}

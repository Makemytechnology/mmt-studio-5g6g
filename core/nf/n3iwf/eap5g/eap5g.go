// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package eap5g implements the EAP-5G expanded EAP method used to
// carry 5G NAS over IKE_AUTH on the NWu interface — i.e. the AMF-
// side N3IWF talking to a UE on untrusted non-3GPP access.
//
// Authoritative spec: TS 24.502 v19.3.0 §9.3.2 "EAP-5G method"
// (PDF: specs/3gpp/ts_124502v190300p.pdf).
//
// Wire format anchors (verbatim §9.3.2.1 / §9.3.2.2 + RFC 3748):
//
//   - Code is 1 (Request) or 2 (Response) per RFC 3748 §4.1.
//   - Type is 254 (Expanded) per RFC 3748 §5.7.
//   - Vendor-Id is the 3GPP IANA SMI Private Enterprise Code 10415
//     (decimal). The 24.502 §9.3.2.2.x tables are explicit about the
//     decimal value — encoded as 3 bytes big-endian = 0x00 0x28 0xAF.
//   - Vendor-Type is 3, the EAP-5G method identifier (TS 33.402
//     annex C).
//   - Message-Id values per the 24.502 §9.3.2.2.x tables:
//
//	5G-Start-Id        1   §9.3.2.2.1   (network → UE)
//	5G-NAS-Id          2   §9.3.2.2.2/3 (both directions)
//	5G-Notification-Id 3   §9.3.2.2.5/6 (both directions)
//	5G-Stop-Id         4   §9.3.2.2.4   (UE → network)
//
// All multi-octet integers are big-endian (RFC 3748 §3, §5).
package eap5g

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// EAP codes — RFC 3748 §4.1.
const (
	CodeRequest  uint8 = 1
	CodeResponse uint8 = 2
	CodeSuccess  uint8 = 3
	CodeFailure  uint8 = 4
)

// EAP type — RFC 3748 §5.7 expanded type.
const TypeExpanded uint8 = 254

// 3GPP Vendor-Id and EAP-5G Vendor-Type.
//
// Vendor-Id field is a 3-octet field (RFC 3748 §5.7); the value
// per TS 24.502 §9.3.2.2.x is the decimal 10415 (= 0x000028AF in
// 3-byte big-endian form). Vendor-Type is a 4-octet field set to
// the decimal EAP-5G method identifier 3.
const (
	VendorID3GPP    uint32 = 10415
	VendorTypeEAP5G uint32 = 3
)

// MessageID — TS 24.502 §9.3.2.2.x "Message-Id field is set to
// 5G-{Start,NAS,Notification,Stop}-Id of N (decimal)".
type MessageID uint8

const (
	MsgIDStart        MessageID = 1
	MsgIDNAS          MessageID = 2
	MsgIDNotification MessageID = 3
	MsgIDStop         MessageID = 4
)

// AN-parameter types in 5G-NAS Response (UE→N3IWF) per §9.3.2.2.2:
//
//	01H GUAMI
//	02H selected PLMN ID
//	03H requested NSSAI
//	04H establishment cause for non-3GPP access
//	05H selected NID
const (
	ANParamGUAMI                uint8 = 0x01
	ANParamSelectedPLMN         uint8 = 0x02
	ANParamRequestedNSSAI       uint8 = 0x03
	ANParamEstablishmentCause   uint8 = 0x04
	ANParamSelectedNID          uint8 = 0x05
)

// Extended-AN-parameter types per §9.3.2.2.2 (verbatim):
//
//	06H UE identity
const ExtANParamUEIdentity uint8 = 0x06

// AN-parameter types in 5G-Notification (network→UE) per §9.3.2.2.5:
//
//	01H TNGF IPv4 contact info
//	02H TNGF IPv6 contact info
const (
	NotifyANParamTNGFIPv4 uint8 = 0x01
	NotifyANParamTNGFIPv6 uint8 = 0x02
)

// expandedHeaderLen is the fixed 12-octet RFC 3748 §5.7 expanded
// header up to (and including) Vendor-Type:
//
//	Code (1) | Identifier (1) | Length (2) | Type (1) |
//	Vendor-Id (3) | Vendor-Type (4)
const expandedHeaderLen = 12

// fixedPrefix12 returns the 12-octet RFC 3748 §5.7 expanded header
// without the EAP "Length" yet filled in. Caller patches Length at
// offset [2:4] once the full message size is known.
//
// Vendor-Id is 3 octets big-endian; Vendor-Type is 4 octets
// big-endian per RFC 3748 §5.7.
func fixedPrefix12(code, identifier uint8) []byte {
	buf := make([]byte, expandedHeaderLen)
	buf[0] = code
	buf[1] = identifier
	// buf[2:4] = Length, patched by caller.
	buf[4] = TypeExpanded
	// Vendor-Id 3 octets BE.
	vid := VendorID3GPP
	buf[5] = byte(vid >> 16)
	buf[6] = byte(vid >> 8)
	buf[7] = byte(vid)
	// Vendor-Type 4 octets BE.
	binary.BigEndian.PutUint32(buf[8:12], VendorTypeEAP5G)
	return buf
}

// patchLen writes len(buf) into the EAP Length field at buf[2:4].
// Per RFC 3748 §4.1, Length covers the entire EAP packet.
func patchLen(buf []byte) {
	if len(buf) > 0xFFFF {
		panic(fmt.Sprintf("eap5g: packet too long (%d)", len(buf)))
	}
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(buf)))
}

// Build5GStart builds an EAP-Request/5G-Start message per
// §9.3.2.2.1. extensions may be nil.
//
// Wire layout (Figure 9.3.2.2.1-1):
//
//	Code | Identifier | Length | Type | Vendor-Id | Vendor-Type |
//	Message-Id | Spare (1 octet) | Extensions (optional)
func Build5GStart(identifier uint8, extensions []byte) []byte {
	buf := append(fixedPrefix12(CodeRequest, identifier), byte(MsgIDStart), 0x00)
	buf = append(buf, extensions...)
	patchLen(buf)
	return buf
}

// Build5GNASRequest builds an EAP-Request/5G-NAS (network→UE) per
// §9.3.2.2.3, carrying a NAS PDU.
//
// Wire layout (Figure 9.3.2.2.3-1):
//
//	... Vendor-Type | Message-Id | Spare (1) | NAS-PDU length (2) |
//	NAS-PDU (n) | Extensions (optional)
func Build5GNASRequest(identifier uint8, nasPDU []byte) []byte {
	if len(nasPDU) > 0xFFFF {
		panic(fmt.Sprintf("eap5g: NAS-PDU too long (%d)", len(nasPDU)))
	}
	buf := append(fixedPrefix12(CodeRequest, identifier), byte(MsgIDNAS), 0x00)
	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, uint16(len(nasPDU)))
	buf = append(buf, hdr...)
	buf = append(buf, nasPDU...)
	patchLen(buf)
	return buf
}

// Build5GNASResponse builds an EAP-Response/5G-NAS message (UE→network)
// per TS 24.502 §9.3.2.2.2 figure 9.3.2.2.2-1:
//
//	... Vendor-Type | Message-Id(2) | Spare(1) |
//	AN-params length(2) | AN-params(var) |
//	NAS-PDU length(2) | NAS-PDU(var) |
//	[ Extended-AN-params length(2) | Extended-AN-params(var) ]
//
// Used by tests that synthesise UE-side packets and by the future
// tester/UE-side simulator. Empty anParams omits the AN-parameters
// list (length=0); empty extAN omits the optional Extended block.
func Build5GNASResponse(identifier uint8, nasPDU, anParams, extAN []byte) []byte {
	if len(nasPDU) > 0xFFFF {
		panic(fmt.Sprintf("eap5g: NAS-PDU too long (%d)", len(nasPDU)))
	}
	if len(anParams) > 0xFFFF {
		panic(fmt.Sprintf("eap5g: AN-params too long (%d)", len(anParams)))
	}
	buf := append(fixedPrefix12(CodeResponse, identifier), byte(MsgIDNAS), 0x00)
	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, uint16(len(anParams)))
	buf = append(buf, hdr...)
	buf = append(buf, anParams...)
	binary.BigEndian.PutUint16(hdr, uint16(len(nasPDU)))
	buf = append(buf, hdr...)
	buf = append(buf, nasPDU...)
	if extAN != nil {
		if len(extAN) > 0xFFFF {
			panic(fmt.Sprintf("eap5g: ext-AN too long (%d)", len(extAN)))
		}
		binary.BigEndian.PutUint16(hdr, uint16(len(extAN)))
		buf = append(buf, hdr...)
		buf = append(buf, extAN...)
	}
	patchLen(buf)
	return buf
}

// BuildEAPSuccess builds the 4-octet EAP-Success packet per RFC 3748
// §4.2: Code (3=Success) | Identifier | Length=4. EAP-Success and
// EAP-Failure carry no Type field — the EAP method has already
// concluded by the time the server sends Success.
//
// The N3IWF emits this as the EAP payload of the IKE_AUTH response
// that follows the AMF's InitialContextSetupRequest (TS 24.502
// §7.3.2.2): the EAP-5G method has yielded a key (K_N3IWF / Knh)
// and the IKEv2 layer can move on to the AUTH exchange that
// establishes the signalling IPsec SA.
func BuildEAPSuccess(identifier uint8) []byte {
	return []byte{CodeSuccess, identifier, 0, 4}
}

// Build5GNotification builds an EAP-Request/5G-Notification message
// (network→UE) per §9.3.2.2.5, carrying AN-parameters (e.g. TNGF
// contact info — the §9.3.2.2.5 spec uses TNGF semantics; this
// builder is generic over the AN-parameter list).
func Build5GNotification(identifier uint8, anParams []byte) []byte {
	buf := append(fixedPrefix12(CodeRequest, identifier), byte(MsgIDNotification), 0x00)
	hdr := make([]byte, 2)
	binary.BigEndian.PutUint16(hdr, uint16(len(anParams)))
	buf = append(buf, hdr...)
	buf = append(buf, anParams...)
	patchLen(buf)
	return buf
}

// EncodeANParameters serializes a list of AN-parameter TLVs per
// §9.3.2.2.2 figure 9.3.2.2.2-3:
//
//	AN-parameter type (1) | AN-parameter length (1) | value (var)
func EncodeANParameters(params []ANParameter) []byte {
	var out []byte
	for _, p := range params {
		if len(p.Value) > 0xFF {
			panic(fmt.Sprintf("eap5g: AN-parameter value > 255 octets (type 0x%02x)", p.Type))
		}
		out = append(out, p.Type, byte(len(p.Value)))
		out = append(out, p.Value...)
	}
	return out
}

// ANParameter is one (type, value) pair per §9.3.2.2.2 figure
// 9.3.2.2.2-3 / §9.3.2.2.5 figure 9.3.2.2.5-3.
type ANParameter struct {
	Type  uint8
	Value []byte
}

// Response is the parsed form of an EAP-Response/5G-* message
// (UE → N3IWF). The 12-octet expanded EAP header is consumed; the
// remaining fields are surfaced in their per-§9.3.2.2.x slots.
type Response struct {
	Code        uint8
	Identifier  uint8
	MessageID   MessageID
	NASPDU      []byte // §9.3.2.2.2 NAS-PDU (5G-NAS only)
	ANParameters []ANParameter // §9.3.2.2.2 AN-parameters list
	ExtANParameters []ANParameter // §9.3.2.2.2 Extended-AN-parameters list
	Extensions  []byte
}

// Parse decodes a complete EAP-5G packet (Code|Id|Length|Type|...)
// from the wire. Validates Vendor-Id / Vendor-Type / Length. Only
// EAP-Response/5G-Start / 5G-NAS / 5G-Notification / 5G-Stop are
// recognised — anything else returns ErrUnknownMessage.
//
// Per §9.3.2.2.x: the AN-parameters length field is 16-bit; if zero,
// the AN-parameters field is absent. The Extended-AN-parameters
// length field is "present if the EAP-Response/5G-NAS message is at
// least (y+n+1) octets long" — i.e. optional, parsed only if there
// are bytes left after the NAS-PDU.
func Parse(buf []byte) (*Response, error) {
	if len(buf) < expandedHeaderLen+1 { // need at least Message-Id
		return nil, fmt.Errorf("eap5g: packet shorter than 13 octets (%d)", len(buf))
	}
	declaredLen := int(binary.BigEndian.Uint16(buf[2:4]))
	if declaredLen != len(buf) {
		return nil, fmt.Errorf("eap5g: EAP Length %d != actual %d (RFC 3748 §4.1)",
			declaredLen, len(buf))
	}
	if buf[4] != TypeExpanded {
		return nil, fmt.Errorf("eap5g: Type %d != 254 (RFC 3748 §5.7)", buf[4])
	}
	vid := uint32(buf[5])<<16 | uint32(buf[6])<<8 | uint32(buf[7])
	if vid != VendorID3GPP {
		return nil, fmt.Errorf("eap5g: Vendor-Id %d != %d (TS 24.502 §9.3.2.2.x)",
			vid, VendorID3GPP)
	}
	vt := binary.BigEndian.Uint32(buf[8:12])
	if vt != VendorTypeEAP5G {
		return nil, fmt.Errorf("eap5g: Vendor-Type %d != %d (TS 24.502 §9.3.2.2.x)",
			vt, VendorTypeEAP5G)
	}
	resp := &Response{
		Code:       buf[0],
		Identifier: buf[1],
		MessageID:  MessageID(buf[12]),
	}
	body := buf[13:] // after Message-Id
	switch resp.MessageID {
	case MsgIDStart:
		// §9.3.2.2.1 Spare (1) | Extensions (optional)
		if len(body) < 1 {
			return nil, errors.New("eap5g 5G-Start: missing Spare octet")
		}
		resp.Extensions = append([]byte(nil), body[1:]...)
	case MsgIDNAS:
		// Direction-aware: §9.3.2.2.2 (Response/UE→network) carries
		// AN-parameters before the NAS-PDU; §9.3.2.2.3 (Request/
		// network→UE) does not — just Spare | NAS-PDU length |
		// NAS-PDU. Code distinguishes per RFC 3748 §4.1.
		if resp.Code == CodeRequest {
			return parse5GNASRequest(resp, body)
		}
		return parse5GNAS(resp, body)
	case MsgIDNotification:
		return parse5GNotification(resp, body)
	case MsgIDStop:
		// §9.3.2.2.4 Spare (1) | Extensions (optional)
		if len(body) < 1 {
			return nil, errors.New("eap5g 5G-Stop: missing Spare octet")
		}
		resp.Extensions = append([]byte(nil), body[1:]...)
	default:
		return nil, ErrUnknownMessage
	}
	return resp, nil
}

// ErrUnknownMessage is returned for an EAP-5G Message-Id value not
// listed in TS 24.502 §9.3.2.2.x. Spare values are reserved.
var ErrUnknownMessage = errors.New("eap5g: unknown Message-Id (TS 24.502 §9.3.2.2.x)")

// parse5GNASRequest decodes the §9.3.2.2.3 (network→UE) layout:
//
//	Spare (1) | NAS-PDU length (2) | NAS-PDU (var) | Extensions
func parse5GNASRequest(resp *Response, body []byte) (*Response, error) {
	if len(body) < 1+2 {
		return nil, errors.New("eap5g 5G-NAS request: short header")
	}
	nasLen := int(binary.BigEndian.Uint16(body[1:3]))
	if 3+nasLen > len(body) {
		return nil, fmt.Errorf("eap5g 5G-NAS request: NAS-PDU len %d overruns", nasLen)
	}
	if nasLen > 0 {
		resp.NASPDU = append([]byte(nil), body[3:3+nasLen]...)
	}
	resp.Extensions = append([]byte(nil), body[3+nasLen:]...)
	return resp, nil
}

func parse5GNAS(resp *Response, body []byte) (*Response, error) {
	// §9.3.2.2.2 layout from octet 13 (Message-Id consumed):
	//   Spare (1) | AN-parameters length (2) | AN-parameters (var) |
	//   NAS-PDU length (2) | NAS-PDU (var) |
	//   [ Extended-AN-parameters length (2) | Extended-AN-parameters ]
	//   Extensions (optional spare bits at the tail)
	if len(body) < 1+2 {
		return nil, errors.New("eap5g 5G-NAS: short header")
	}
	off := 1 // skip Spare
	anLen := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2
	if off+anLen > len(body) {
		return nil, fmt.Errorf("eap5g 5G-NAS: AN-params len %d overruns packet", anLen)
	}
	if anLen > 0 {
		ans, err := decodeANParameters(body[off : off+anLen])
		if err != nil {
			return nil, fmt.Errorf("eap5g 5G-NAS: AN-params: %w", err)
		}
		resp.ANParameters = ans
	}
	off += anLen
	if off+2 > len(body) {
		return nil, errors.New("eap5g 5G-NAS: missing NAS-PDU length field")
	}
	nasLen := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2
	if off+nasLen > len(body) {
		return nil, fmt.Errorf("eap5g 5G-NAS: NAS-PDU len %d overruns packet", nasLen)
	}
	if nasLen > 0 {
		resp.NASPDU = append([]byte(nil), body[off:off+nasLen]...)
	}
	off += nasLen
	// Optional Extended-AN-parameters block per §9.3.2.2.2: "present
	// if the EAP-Response/5G-NAS message is at least (y+n+1) octets
	// long" — i.e. parse only if at least 2 more bytes remain.
	if off+2 <= len(body) {
		extLen := int(binary.BigEndian.Uint16(body[off : off+2]))
		off += 2
		if off+extLen > len(body) {
			return nil, fmt.Errorf("eap5g 5G-NAS: ext-AN-params len %d overruns",
				extLen)
		}
		if extLen > 0 {
			ext, err := decodeANParameters(body[off : off+extLen])
			if err != nil {
				return nil, fmt.Errorf("eap5g 5G-NAS: ext-AN-params: %w", err)
			}
			resp.ExtANParameters = ext
		}
		off += extLen
	}
	resp.Extensions = append([]byte(nil), body[off:]...)
	return resp, nil
}

func parse5GNotification(resp *Response, body []byte) (*Response, error) {
	// §9.3.2.2.6 mirrors §9.3.2.2.5 for the Response direction.
	if len(body) < 1+2 {
		return nil, errors.New("eap5g 5G-Notification: short header")
	}
	off := 1 // skip Spare
	anLen := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2
	if off+anLen > len(body) {
		return nil, fmt.Errorf("eap5g 5G-Notification: AN-params len %d overruns", anLen)
	}
	if anLen > 0 {
		ans, err := decodeANParameters(body[off : off+anLen])
		if err != nil {
			return nil, fmt.Errorf("eap5g 5G-Notification: AN-params: %w", err)
		}
		resp.ANParameters = ans
	}
	off += anLen
	resp.Extensions = append([]byte(nil), body[off:]...)
	return resp, nil
}

// decodeANParameters parses figure 9.3.2.2.2-2 / §9.3.2.2.5-2 list:
// concatenated (type | length | value) tuples.
func decodeANParameters(buf []byte) ([]ANParameter, error) {
	var out []ANParameter
	off := 0
	for off < len(buf) {
		if off+2 > len(buf) {
			return nil, fmt.Errorf("AN-parameter header truncated at %d", off)
		}
		t := buf[off]
		l := int(buf[off+1])
		if off+2+l > len(buf) {
			return nil, fmt.Errorf("AN-parameter type 0x%02x value of %d octets overruns",
				t, l)
		}
		out = append(out, ANParameter{
			Type:  t,
			Value: append([]byte(nil), buf[off+2:off+2+l]...),
		})
		off += 2 + l
	}
	return out, nil
}

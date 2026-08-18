// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SMS codec — *decoders*. Companion to smsf.go (encoders).
//
// SMS over NAS arrives as a nested TLV stack:
//
//	Payload Container (TS 24.501 §9.11.3.39, type=SMS=2)
//	└── CP-DATA / CP-ACK / CP-ERROR (TS 24.011 §8.1)
//	    └── RP-DATA / RP-ACK / RP-ERROR (TS 24.011 §8.2)
//	        └── SMS-SUBMIT / SMS-DELIVER TPDU (TS 23.040 §9.2.2)
//	            └── TP-User-Data (TS 23.040 §9.2.3.24,
//	                encoded per TS 23.038 §6.1.2 / §6.2)
//
// This file walks that stack inbound. Outbound encoders live in
// smsf.go. Every public type and function carries the §clause it
// implements; unimplemented branches are TODO'd against the §clause
// that defines them so a future audit can find them by spec number.
package smsf

import (
	"encoding/binary"
	"fmt"
	"unicode/utf16"
)

// ================================================================
// CP-Layer (TS 24.011 §8.1)
// ================================================================

// CPMessage is a parsed Connection-Management Sublayer PDU per
// TS 24.011 §8.1.
type CPMessage struct {
	TI       byte   // Transaction Identifier — TS 24.011 §8.1.2
	MsgType  byte   // CP-DATA / CP-ACK / CP-ERROR — TS 24.011 §8.1.3
	UserData []byte // CP-User data IE — TS 24.011 §8.1.4.1 (RP-PDU bytes)
	Cause    byte   // CP-Cause — TS 24.011 §8.1.4.2 (CP-ERROR only)
}

// DecodeCP parses a CP-layer PDU per TS 24.011 §8.1.
//
// Layout (TS 24.011 §7.2 / Figure 8.1):
//
//	octet 1: TI (high nibble) | PD=0x9 (low nibble, "SMS messages") — §8.1.2
//	octet 2: Message type — §8.1.3
//	         CP-DATA = 0x01, CP-ACK = 0x04, CP-ERROR = 0x10
//	octet 3..n: type-dependent body
//	         CP-DATA   → CP-User data IE (LV: len + RP-PDU bytes) — §8.1.4.1
//	         CP-ACK    → empty
//	         CP-ERROR  → CP-Cause octet — §8.1.4.2
//
// Protocol Discriminator value 0x9 = "SMS messages" per TS 24.007
// §11.2.3.1.1 Table 11.2.
func DecodeCP(data []byte) (*CPMessage, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("CP PDU too short: %d bytes", len(data))
	}
	pdTI := data[0]
	if pdTI&0x0F != 0x09 {
		// PD must be 9 ("SMS messages") per TS 24.007 §11.2.3.1.1.
		return nil, fmt.Errorf("CP: bad protocol discriminator 0x%X (want 0x9)", pdTI&0x0F)
	}
	msg := &CPMessage{
		TI:      (pdTI >> 4) & 0x0F,
		MsgType: data[1],
	}
	switch msg.MsgType {
	case CPData:
		// CP-User data IE per TS 24.011 §8.1.4.1: length(1) + RP-PDU.
		if len(data) < 3 {
			return nil, fmt.Errorf("CP-DATA: missing CP-User data IE")
		}
		udLen := int(data[2])
		if len(data) < 3+udLen {
			return nil, fmt.Errorf(
				"CP-DATA: declared CP-User data len %d exceeds remaining %d",
				udLen, len(data)-3)
		}
		msg.UserData = data[3 : 3+udLen]
	case CPAck:
		// No body — TS 24.011 §7.2.2.
	case CPError:
		// CP-Cause IE per TS 24.011 §8.1.4.2: a single octet (mandatory V).
		if len(data) < 3 {
			return nil, fmt.Errorf("CP-ERROR: missing CP-Cause")
		}
		msg.Cause = data[2]
	default:
		// TODO(spec: TS 24.011 §8.1.3): Tables 8.1/8.2 only define the
		// three values above; any other byte is a protocol violation
		// and should trigger a CP-ERROR(cause=97 "message type
		// non-existent or not implemented") per §6.4. Not implemented:
		// we currently treat it as a hard decode failure instead of
		// generating the spec-mandated CP-ERROR back to the UE.
		return nil, fmt.Errorf("CP: unknown message type 0x%02X", msg.MsgType)
	}
	return msg, nil
}

// ================================================================
// RP-Layer (TS 24.011 §8.2)
// ================================================================

// RPMessage is a parsed Relay-Layer PDU per TS 24.011 §8.2.
type RPMessage struct {
	MTI       byte   // Message Type Indicator — TS 24.011 §8.2.2
	Reference byte   // RP-Message Reference — TS 24.011 §8.2.3
	OAAddr    string // RP-Originator Address — TS 24.011 §8.2.5.1 (empty MS→Net)
	DAAddr    string // RP-Destination Address — TS 24.011 §8.2.5.2 (empty Net→MS)
	UserData  []byte // RP-User data — TS 24.011 §8.2.5.3 (the TPDU)
	Cause     byte   // RP-Cause — TS 24.011 §8.2.5.4 (RP-ERROR only)
}

// DecodeRP parses an RP-layer PDU per TS 24.011 §8.2.
//
// Layout for RP-DATA (Net→MS §7.3.1.1 / MS→Net §7.3.1.2):
//
//	octet 1     : MTI — §8.2.2 (RP-DATA Net→MS=0x01, MS→Net=0x00)
//	octet 2     : RP-Message Reference — §8.2.3
//	octet 3..   : RP-Originator Address element — §8.2.5.1
//	              octet 3 = length L1 of address contents
//	              octet 4 = address-type-of-number / NPI (only if L1>0)
//	              octets 5..3+L1 = BCD digits
//	              For MS→Net the originator length L1 shall be 0.
//	octet 4+L1 .. : RP-Destination Address element — §8.2.5.2
//	              same shape; for Net→MS this length L2 shall be 0.
//	octet (5+L1+L2)..: RP-User data IE (LV) — §8.2.5.3
//	              octet x  = length of TPDU
//	              octet x+1.. = TPDU bytes (TS 23.040)
//
// Layout for RP-ACK (§7.3.3): MTI | Ref | (no body in our impl).
// Layout for RP-ERROR (§7.3.4): MTI | Ref | RP-Cause LV (cause+optional diag).
func DecodeRP(data []byte) (*RPMessage, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("RP PDU too short: %d bytes", len(data))
	}
	msg := &RPMessage{
		MTI:       data[0],
		Reference: data[1],
	}
	switch msg.MTI {
	case RPDataMSToNet, RPDataNetToMS:
		off := 2
		// RP-OA — TS 24.011 §8.2.5.1.
		if off >= len(data) {
			return nil, fmt.Errorf("RP-DATA: truncated at RP-OA length")
		}
		oaLen := int(data[off])
		off++
		if off+oaLen > len(data) {
			return nil, fmt.Errorf("RP-DATA: RP-OA length %d exceeds buffer", oaLen)
		}
		if oaLen > 0 {
			msg.OAAddr = decodeRPAddress(data[off : off+oaLen])
		}
		off += oaLen

		// RP-DA — TS 24.011 §8.2.5.2.
		if off >= len(data) {
			return nil, fmt.Errorf("RP-DATA: truncated at RP-DA length")
		}
		daLen := int(data[off])
		off++
		if off+daLen > len(data) {
			return nil, fmt.Errorf("RP-DATA: RP-DA length %d exceeds buffer", daLen)
		}
		if daLen > 0 {
			msg.DAAddr = decodeRPAddress(data[off : off+daLen])
		}
		off += daLen

		// RP-User data — TS 24.011 §8.2.5.3.
		if off >= len(data) {
			return nil, fmt.Errorf("RP-DATA: truncated at RP-User-Data length")
		}
		udLen := int(data[off])
		off++
		if off+udLen > len(data) {
			return nil, fmt.Errorf("RP-DATA: RP-UD length %d exceeds buffer", udLen)
		}
		msg.UserData = data[off : off+udLen]
	case RPAckMSToNet, RPAckNetToMS:
		// TODO(spec: TS 24.011 §8.2.5.3): RP-ACK *may* include an
		// RP-User data IE carrying a Status-Report TPDU. We only
		// generate / consume bare ACKs today.
	case RPErrorMSToNet, RPErrorNetToMS:
		if len(data) < 4 {
			return nil, fmt.Errorf("RP-ERROR: truncated")
		}
		// RP-Cause IE per TS 24.011 §8.2.5.4: length(1) + cause(1) [+ diag].
		causeLen := int(data[2])
		if causeLen >= 1 && 3+causeLen <= len(data) {
			msg.Cause = data[3] & 0x7F
		}
	default:
		// TODO(spec: TS 24.011 §8.2.2): RP-SMMA (MTI=4) and reserved
		// MTIs are not yet handled here. RP-SMMA ("Memory Available")
		// would be relayed to the SMS-GMSC to trigger MT-SMS retry per
		// TS 23.040 §10.2.
		return nil, fmt.Errorf("RP: unknown MTI 0x%02X", msg.MTI)
	}
	return msg, nil
}

// decodeRPAddress decodes the *contents* of an RP address element
// (after the length octet has been stripped by the caller) per
// TS 24.011 §8.2.5.1: octet 1 is type-of-number/NPI, octets 2.. are
// BCD digits with the high nibble of the last octet = 0xF when the
// digit count is odd ("filled with an end mark coded as '1111'").
//
// Returns "" if the contents are empty (length-zero address element,
// which is the spec-mandated form for the unused direction).
func decodeRPAddress(contents []byte) string {
	if len(contents) == 0 {
		return ""
	}
	toa := contents[0]
	bcd := contents[1:]
	var digits []byte
	for _, b := range bcd {
		lo := b & 0x0F
		hi := (b >> 4) & 0x0F
		if lo <= 9 {
			digits = append(digits, '0'+lo)
		}
		if hi <= 9 {
			digits = append(digits, '0'+hi)
		}
	}
	out := string(digits)
	// TON=International (001) per TS 24.008 §10.5.4.7 → leading '+'.
	if (toa>>4)&0x07 == 0x01 {
		out = "+" + out
	}
	return out
}

// encodeRPAddress encodes an MSISDN/SMSC address as the *contents*
// of an RP address element per TS 24.011 §8.2.5.1 (the caller must
// prepend the length octet). The format is: TOA(1) + BCD digits,
// with the high nibble of the last BCD octet filled with 0xF when
// the digit count is odd.
//
// For an empty address the caller should emit a length-zero element
// and skip this helper.
func encodeRPAddress(msisdn string) []byte {
	if msisdn == "" {
		return nil
	}
	intl := false
	digits := msisdn
	if digits[0] == '+' {
		intl = true
		digits = digits[1:]
	}
	toa := byte(0x81) // TON=Unknown(000), NPI=ISDN/E.164(0001), bit 7 always 1.
	if intl {
		toa = 0x91 // TON=International(001), NPI=ISDN/E.164(0001).
	}
	out := []byte{toa}
	for i := 0; i < len(digits); i += 2 {
		d1 := digits[i] - '0'
		d2 := byte(0x0F) // odd-count fill per §8.2.5.1.
		if i+1 < len(digits) {
			d2 = digits[i+1] - '0'
		}
		out = append(out, (d2<<4)|d1)
	}
	return out
}

// ================================================================
// TPDU layer — SMS-SUBMIT (TS 23.040 §9.2.2.2)
// ================================================================

// SMSSubmitTPDU is a decoded SMS-SUBMIT TPDU per TS 23.040 §9.2.2.2.
//
// "An SMS-SUBMIT shall be sent in the direction MS to SC, and is
// used to convey a short message from the MS to the SC."
type SMSSubmitTPDU struct {
	UDHI      bool   // TP-UDHI — §9.2.3.23
	SRR       bool   // TP-Status-Report-Request — §9.2.3.5
	VPF       byte   // TP-Validity-Period-Format — §9.2.3.3 (00/10/01/11)
	Reference byte   // TP-Message-Reference — §9.2.3.6
	DAMSISDN  string // TP-Destination-Address — §9.2.3.8 / §9.1.2.5
	PID       byte   // TP-Protocol-Identifier — §9.2.3.9
	DCS       byte   // TP-Data-Coding-Scheme — §9.2.3.10 / TS 23.038 §4
	UDL       int    // TP-User-Data-Length — §9.2.3.16 (septets if GSM7, octets if 8bit/UCS2)
	UDH       []byte // TP-User-Data-Header (raw IEDs after UDHL, if UDHI=1) — §9.2.3.24
	UD        []byte // TP-User-Data body following the UDH (still packed for GSM7) — §9.2.3.24
	Encoding  string // "gsm7" | "8bit" | "ucs2" — derived from DCS per TS 23.038 §4
}

// DecodeSMSSubmit parses an SMS-SUBMIT TPDU per TS 23.040 §9.2.2.2
// Table 9.2.2.2-1.
//
// Layout (octets):
//
//	1   : first octet — §9.2.2.2 (TP-MTI/RD/VPF/SRR/UDHI/RP)
//	2   : TP-MR — §9.2.3.6
//	3..k: TP-DA — §9.2.3.8 (LV per §9.1.2.5: numDigits + TOA + BCD)
//	k+1 : TP-PID — §9.2.3.9
//	k+2 : TP-DCS — §9.2.3.10
//	k+3..: TP-VP — §9.2.3.12 (0 / 1 / 7 octets per VPF)
//	?   : TP-UDL — §9.2.3.16
//	?+1..: TP-UD — §9.2.3.24
func DecodeSMSSubmit(data []byte) (*SMSSubmitTPDU, error) {
	if len(data) < 7 {
		return nil, fmt.Errorf("SMS-SUBMIT too short: %d bytes", len(data))
	}
	off := 0
	first := data[off]
	off++
	mti := first & 0x03
	if mti != 0x01 {
		// TS 23.040 §9.2.3.1 Table 9.2.3.1: MTI=01 is SMS-SUBMIT in
		// the MS→SC direction. Anything else here is a protocol error.
		return nil, fmt.Errorf("SMS-SUBMIT: TP-MTI=%d (want 1)", mti)
	}
	msg := &SMSSubmitTPDU{
		UDHI: first&0x40 != 0,
		SRR:  first&0x20 != 0,
		VPF:  (first >> 3) & 0x03,
	}

	msg.Reference = data[off]
	off++

	// TP-DA per §9.2.3.8 (TS 23.040 address fields, §9.1.2.5).
	da, daBytes := DecodeAddress(data, off)
	if daBytes == 0 {
		return nil, fmt.Errorf("SMS-SUBMIT: malformed TP-DA")
	}
	msg.DAMSISDN = da
	off += daBytes

	if off+2 > len(data) {
		return nil, fmt.Errorf("SMS-SUBMIT: truncated at TP-PID/TP-DCS")
	}
	msg.PID = data[off]
	off++
	msg.DCS = data[off]
	off++

	// TP-VP per §9.2.3.12. Length depends on TP-VPF in the first octet:
	//   00 = VP not present (0 octets)
	//   10 = relative   (1 octet)
	//   01 = enhanced   (7 octets)
	//   11 = absolute   (7 octets)
	switch msg.VPF {
	case 0x00:
		// No VP field.
	case 0x02:
		if off >= len(data) {
			return nil, fmt.Errorf("SMS-SUBMIT: truncated at TP-VP (relative)")
		}
		// TODO(spec: TS 23.040 §9.2.3.12.1): decode the relative-format
		// validity-period octet into a duration so the message-expiry
		// path (smsf.go ExpireOldMessages) can honour the UE's request
		// instead of a global SMSExpirySeconds. We currently just skip
		// the octet.
		off++
	case 0x01, 0x03:
		if off+7 > len(data) {
			return nil, fmt.Errorf("SMS-SUBMIT: truncated at TP-VP")
		}
		// TODO(spec: TS 23.040 §9.2.3.12.2/§9.2.3.12.3): decode
		// absolute (semi-octet timestamp) and enhanced (functionality
		// indicator + 6 octets) VP forms.
		off += 7
	}

	if off >= len(data) {
		return nil, fmt.Errorf("SMS-SUBMIT: truncated at TP-UDL")
	}
	msg.UDL = int(data[off])
	off++

	// TP-DCS → encoding selector per TS 23.038 §4 Table 4-1.
	// Only the General Data Coding indication (bits 7..4 = 00xx) is
	// handled here; the message classes / message-waiting groups are
	// TODO below.
	switch (msg.DCS >> 2) & 0x03 {
	case 0x00:
		msg.Encoding = "gsm7"
	case 0x01:
		msg.Encoding = "8bit"
	case 0x02:
		msg.Encoding = "ucs2"
	default:
		// TODO(spec: TS 23.038 §4 Table 4-1): the reserved value 0b11
		// in the General Data Coding group is reserved; non-General
		// groups (Message Waiting Indication, Data coding/message
		// classes) need their own decode paths. Treated as 8bit for
		// now so the bytes still propagate.
		msg.Encoding = "8bit"
	}

	udBody := data[off:]
	if msg.UDHI && len(udBody) > 0 {
		// TP-UDH per §9.2.3.24: octet 1 = UDHL, octets 2..1+UDHL = IEs.
		udhl := int(udBody[0])
		if 1+udhl > len(udBody) {
			return nil, fmt.Errorf(
				"SMS-SUBMIT: TP-UDH length %d exceeds TP-UD remainder %d",
				udhl, len(udBody)-1)
		}
		msg.UDH = udBody[1 : 1+udhl]
		// Body sits immediately after the header for UCS2/8bit.
		// For GSM 7-bit the body is septet-aligned by 0..6 fill bits
		// after the header per §9.2.3.24 — see DecodeUserData.
		msg.UD = udBody[1+udhl:]
	} else {
		msg.UD = udBody
	}
	return msg, nil
}

// DecodeUserData turns a TP-UD body (already split out by
// DecodeSMSSubmit / a future DecodeSMSDeliver) into a Unicode string
// per TS 23.040 §9.2.3.24 + TS 23.038 §6.1.2 (GSM 7-bit) or §6.2.3
// (UCS2). Honours the UDH-induced fill-bit alignment for GSM 7-bit
// per §9.2.3.24 ("the SM ... starts on a septet boundary").
//
// udl is the TP-User-Data-Length (septets for GSM7, octets for 8bit/UCS2).
func DecodeUserData(encoding string, dcs byte, udl int, udh []byte, udBody []byte) (string, error) {
	_ = dcs // DCS already collapsed into encoding by the caller.
	switch encoding {
	case "gsm7":
		// The SM in 7-bit form starts on a septet boundary. When a
		// UDH is present, fill-bits pad the octets after the header
		// up to the next 7-bit boundary per §9.2.3.24.
		fillBits := 0
		bodySeptets := udl
		if len(udh) > 0 {
			udhOctets := 1 + len(udh) // UDHL byte + IEDs.
			udhSeptetLen := (udhOctets*8 + 6) / 7
			fillBits = (7 - ((udhOctets * 8) % 7)) % 7
			bodySeptets = udl - udhSeptetLen
			if bodySeptets < 0 {
				bodySeptets = 0
			}
		}
		_ = fillBits
		// TODO(spec: TS 23.040 §9.2.3.24): implement the fill-bit
		// shift before unpacking. For now we ignore fillBits and
		// unpack from the start of udBody, which is correct for
		// messages without a UDH and *almost* correct for UDH-bearing
		// messages whose UDHL satisfies (UDHL+1)*8 % 7 == 0
		// (the common case for the 6-byte concat-IED layout).
		return GSM7Decode(udBody, bodySeptets), nil
	case "ucs2":
		// UCS-2 BE per TS 23.038 §6.2.3. udl counts octets here.
		if udl > len(udBody) {
			udl = len(udBody)
		}
		if udl%2 != 0 {
			udl-- // §6.2.3 mandates an even byte count; trim a stray.
		}
		u16 := make([]uint16, udl/2)
		for i := 0; i < udl/2; i++ {
			u16[i] = binary.BigEndian.Uint16(udBody[2*i : 2*i+2])
		}
		return string(utf16.Decode(u16)), nil
	case "8bit":
		// TS 23.038 §4 — 8-bit data is opaque to the SMS layer.
		// Surfacing as Latin-1 so logs are at least eyeball-readable.
		runes := make([]rune, len(udBody))
		for i, b := range udBody {
			runes[i] = rune(b)
		}
		return string(runes), nil
	}
	return "", fmt.Errorf("DecodeUserData: unknown encoding %q", encoding)
}

// ================================================================
// SMS-DELIVER decoder — TODO
// ================================================================

// TODO(spec: TS 23.040 §9.2.2.1 Table 9.2.2.1-1): implement
// DecodeSMSDeliver. The MO path (UE→SMSF) only needs SMS-SUBMIT
// decoding; SMS-DELIVER is the reverse direction (SC→MS) and we
// only generate it via EncodeSMSDeliver. A round-trip decoder is
// however needed for:
//
//   - testing the encoder against the spec (decode(encode(x)) == x)
//   - relaying SMS-DELIVER from an external SMSC into our delivery
//     pipeline (currently we synthesize the TPDU ourselves)
//   - SMS-STATUS-REPORT delivery handling, which sits inside the
//     same decode skeleton (§9.2.2.3).

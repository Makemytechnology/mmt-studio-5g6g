// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import "fmt"

// PlmnId — PLMN identity (MCC + MNC).
// Encoded as 3 BCD-swapped bytes per TS 24.501 §9.11.3.54 (and TS 24.008 §10.5.1.3).
type PlmnId struct {
	MCC string // 3 digits
	MNC string // 2 or 3 digits
}

// EncodeBCD encodes the PLMN to 3 bytes, digit order per 3GPP.
//
//	Octet 1: MCC digit 2 | MCC digit 1
//	Octet 2: MNC digit 3 | MCC digit 3   (digit 3 = 0xF if MNC is 2 digits)
//	Octet 3: MNC digit 2 | MNC digit 1
func (p *PlmnId) EncodeBCD() []byte {
	mcc := normalizeDigits(p.MCC, 3)
	mnc := p.MNC
	var mncDigits [3]byte
	if len(mnc) == 2 {
		mncDigits[0] = mnc[0] - '0'
		mncDigits[1] = mnc[1] - '0'
		mncDigits[2] = 0x0F
	} else if len(mnc) == 3 {
		mncDigits[0] = mnc[0] - '0'
		mncDigits[1] = mnc[1] - '0'
		mncDigits[2] = mnc[2] - '0'
	}
	out := make([]byte, 3)
	out[0] = (mcc[1] << 4) | (mcc[0] & 0x0F)
	out[1] = (mncDigits[2] << 4) | (mcc[2] & 0x0F)
	out[2] = (mncDigits[1] << 4) | (mncDigits[0] & 0x0F)
	return out
}

// DecodePlmnBCD parses 3 bytes of BCD-swapped PLMN.
func DecodePlmnBCD(data []byte) (PlmnId, error) {
	if len(data) < 3 {
		return PlmnId{}, ErrBufferTooShort
	}
	mcc1 := data[0] & 0x0F
	mcc2 := (data[0] >> 4) & 0x0F
	mcc3 := data[1] & 0x0F
	mnc3 := (data[1] >> 4) & 0x0F
	mnc1 := data[2] & 0x0F
	mnc2 := (data[2] >> 4) & 0x0F

	mcc := fmt.Sprintf("%d%d%d", mcc1, mcc2, mcc3)
	var mnc string
	if mnc3 == 0x0F {
		mnc = fmt.Sprintf("%d%d", mnc1, mnc2)
	} else {
		mnc = fmt.Sprintf("%d%d%d", mnc1, mnc2, mnc3)
	}
	return PlmnId{MCC: mcc, MNC: mnc}, nil
}

func normalizeDigits(s string, n int) []byte {
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		if i < len(s) {
			out[i] = s[i] - '0'
		}
	}
	return out
}

// SNSSAI — Single Network Slice Selection Assistance Information (TS 24.501 §9.11.2.8).
type SNSSAI struct {
	SST            uint8
	SD             *uint32 // 24-bit
	MappedHplmnSST *uint8
	MappedHplmnSD  *uint32
}

// TAI — 5GS Tracking Area Identity (TS 24.501 §9.11.3.8).
type TAI struct {
	Plmn PlmnId
	TAC  [3]byte
}

// MobileIdentity5GS — interface over the various 5GS mobile identity subtypes
// (TS 24.501 §9.11.3.4). Implementations include SUCI, GUTI5G, IMEI, STMSI5G, None.
type MobileIdentity5GS interface {
	mobileIdentity5GS()
	Encode() []byte
}

type SUCI struct {
	ProtectionSchemeId uint8
	HomeNetworkId      PlmnId
	RoutingIndicator   string
	SchemeOutput       []byte
}

func (*SUCI) mobileIdentity5GS() {}

// Encode (stub — TS 24.501 §9.11.3.4 figure 9.11.3.4.4). Returns the raw
// SchemeOutput for now; callers needing full SUCI construction should build
// the byte layout themselves.
func (s *SUCI) Encode() []byte {
	out := make([]byte, 0, 8+len(s.SchemeOutput))
	out = append(out, 0x01) // type = SUCI
	out = append(out, s.HomeNetworkId.EncodeBCD()...)
	// Routing indicator (2 BCD bytes), Protection scheme id (1 byte), HNPKID (1 byte).
	out = append(out, 0x00, 0x00)
	out = append(out, s.ProtectionSchemeId&0x0F)
	out = append(out, 0x00)
	out = append(out, s.SchemeOutput...)
	return out
}

type GUTI5G struct {
	Plmn        PlmnId
	AmfRegionId uint8
	AmfSetId    uint16 // 10 bits
	AmfPointer  uint8  // 6 bits
	TMSI5G      uint32
}

func (*GUTI5G) mobileIdentity5GS() {}

// Encode per TS 24.501 §9.11.3.4 figure 9.11.3.4.3 (GUTI).
func (g *GUTI5G) Encode() []byte {
	out := make([]byte, 11)
	out[0] = 0xF2 // type-of-identity = 010 (GUTI), octet 3 framing: "1111 0 010"
	copy(out[1:4], g.Plmn.EncodeBCD())
	out[4] = g.AmfRegionId
	// AMF Set Id (10 bits) : AMF Pointer (6 bits), big-endian
	v := (uint16(g.AmfSetId&0x03FF) << 6) | uint16(g.AmfPointer&0x3F)
	out[5] = byte(v >> 8)
	out[6] = byte(v)
	out[7] = byte(g.TMSI5G >> 24)
	out[8] = byte(g.TMSI5G >> 16)
	out[9] = byte(g.TMSI5G >> 8)
	out[10] = byte(g.TMSI5G)
	return out
}

type IMEI struct{ Digits string }

func (*IMEI) mobileIdentity5GS() {}
func (i *IMEI) Encode() []byte {
	// Figure 9.11.3.4.2: first octet: digit1<<4 | odd/even<<3 | type(=011)
	digits := i.Digits
	n := len(digits)
	odd := byte(0)
	if n%2 == 1 {
		odd = 1
	}
	out := make([]byte, 0, (n+1)/2+1)
	firstDigit := byte(0xF)
	if n > 0 {
		firstDigit = digits[0] - '0'
	}
	out = append(out, (firstDigit<<4)|(odd<<3)|0x03)
	for i := 1; i < n; i += 2 {
		hi := byte(0xF)
		lo := digits[i] - '0'
		if i+1 < n {
			hi = digits[i+1] - '0'
		}
		out = append(out, (hi<<4)|lo)
	}
	return out
}

type STMSI5G struct {
	AmfSetId   uint16
	AmfPointer uint8
	TMSI5G     uint32
}

func (*STMSI5G) mobileIdentity5GS() {}
func (s *STMSI5G) Encode() []byte {
	out := make([]byte, 7)
	out[0] = 0xF4 // type-of-identity 100
	v := (uint16(s.AmfSetId&0x03FF) << 6) | uint16(s.AmfPointer&0x3F)
	out[1] = byte(v >> 8)
	out[2] = byte(v)
	out[3] = byte(s.TMSI5G >> 24)
	out[4] = byte(s.TMSI5G >> 16)
	out[5] = byte(s.TMSI5G >> 8)
	out[6] = byte(s.TMSI5G)
	return out
}

// NoIdentity (TS 24.501 §9.11.3.4 type 000).
type NoIdentity struct{}

func (*NoIdentity) mobileIdentity5GS() {}
func (*NoIdentity) Encode() []byte     { return []byte{0x00} }

// DecodeMobileIdentity5GS dispatches on the low 3 bits of the first octet.
func DecodeMobileIdentity5GS(value []byte) (MobileIdentity5GS, error) {
	if len(value) < 1 {
		return nil, ErrBufferTooShort
	}
	switch value[0] & 0x07 {
	case 0:
		return &NoIdentity{}, nil
	case 1:
		return decodeSUCI(value)
	case 2:
		return decodeGUTI5G(value)
	case 3:
		return decodeIMEI(value), nil
	case 4:
		return decodeSTMSI(value)
	case 5:
		// IMEISV — treat as IMEI for now (digits string)
		return decodeIMEI(value), nil
	}
	return nil, fmt.Errorf("nas: unsupported mobile identity type 0x%02X", value[0]&0x07)
}

func decodeGUTI5G(v []byte) (*GUTI5G, error) {
	if len(v) < 11 {
		return nil, ErrBufferTooShort
	}
	plmn, err := DecodePlmnBCD(v[1:4])
	if err != nil {
		return nil, err
	}
	g := &GUTI5G{
		Plmn:        plmn,
		AmfRegionId: v[4],
		AmfSetId:    (uint16(v[5])<<2 | uint16(v[6])>>6) & 0x03FF,
		AmfPointer:  v[6] & 0x3F,
		TMSI5G:      uint32(v[7])<<24 | uint32(v[8])<<16 | uint32(v[9])<<8 | uint32(v[10]),
	}
	return g, nil
}

func decodeSUCI(v []byte) (*SUCI, error) {
	// Minimal stub — full SUCI decode involves type-of-protection-scheme field and
	// scheme-specific output. Callers can extend.
	if len(v) < 8 {
		return nil, ErrBufferTooShort
	}
	plmn, err := DecodePlmnBCD(v[1:4])
	if err != nil {
		return nil, err
	}
	return &SUCI{
		HomeNetworkId:      plmn,
		ProtectionSchemeId: v[7] & 0x0F,
		SchemeOutput:       append([]byte(nil), v[8:]...),
	}, nil
}

func decodeIMEI(v []byte) *IMEI {
	odd := (v[0] >> 3) & 0x01
	var s []byte
	s = append(s, '0'+(v[0]>>4))
	for i := 1; i < len(v); i++ {
		s = append(s, '0'+(v[i]&0x0F))
		hi := (v[i] >> 4) & 0x0F
		if hi != 0x0F {
			s = append(s, '0'+hi)
		}
	}
	if odd == 0 && len(s) > 0 && s[len(s)-1] == '0'+0xF {
		s = s[:len(s)-1]
	}
	return &IMEI{Digits: string(s)}
}

func decodeSTMSI(v []byte) (*STMSI5G, error) {
	if len(v) < 7 {
		return nil, ErrBufferTooShort
	}
	return &STMSI5G{
		AmfSetId:   (uint16(v[1])<<2 | uint16(v[2])>>6) & 0x03FF,
		AmfPointer: v[2] & 0x3F,
		TMSI5G:     uint32(v[3])<<24 | uint32(v[4])<<16 | uint32(v[5])<<8 | uint32(v[6]),
	}, nil
}

// DNN (TS 24.501 v19.6.2 §9.11.2.1B) — Data Network Name.
//
// The DNN value field carries an APN as defined in TS 23.003 §9.1:
// a sequence of length-prefixed labels (DNS-style) terminated by
// the IE length, e.g.:
//
//	"ims"           →  0x03 'i' 'm' 's'
//	"ims.mnc001.mcc001.gprs"
//	                →  0x03 'i' 'm' 's'
//	                    0x06 'm' 'n' 'c' '0' '0' '1'
//	                    0x06 'm' 'c' 'c' '0' '0' '1'
//	                    0x04 'g' 'p' 'r' 's'
//
// Consumers see the typed Value as a dotted-label string ("ims" or
// "ims.mnc001.mcc001.gprs"). The runtime owns the wire encoding so
// nf/amf/gmm/ulnas.go's old decodeDNN helper can go away.
type DNN struct {
	Value string
}

func (d *DNN) Encode() []byte {
	out := make([]byte, 0, len(d.Value)+8)
	if d.Value == "" {
		return out
	}
	start := 0
	for i := 0; i <= len(d.Value); i++ {
		if i == len(d.Value) || d.Value[i] == '.' {
			labelLen := i - start
			if labelLen > 0 {
				out = append(out, byte(labelLen))
				out = append(out, d.Value[start:i]...)
			}
			start = i + 1
		}
	}
	return out
}

func (d *DNN) Decode(v []byte) error {
	if len(v) == 0 {
		d.Value = ""
		return nil
	}
	var parts []string
	for i := 0; i < len(v); {
		ln := int(v[i])
		i++
		if ln == 0 || i+ln > len(v) {
			return ErrInvalidLength
		}
		parts = append(parts, string(v[i:i+ln]))
		i += ln
	}
	if len(parts) == 0 {
		d.Value = ""
		return nil
	}
	// Join all labels with dots — matches the pre-existing
	// decodeDNN consumer expectation but more faithful to spec
	// when multi-label APNs (e.g. "ims.mnc.mcc.gprs") arrive.
	d.Value = parts[0]
	for _, p := range parts[1:] {
		d.Value += "." + p
	}
	return nil
}

func (d *DNN) EncodeBytes() []byte        { return d.Encode() }
func (d *DNN) DecodeBytes(v []byte) error { return d.Decode(v) }

// EncodeTBCD packs a digit string into TBCD bytes per TS 31.102 +
// TS 24.008 §10.5.1.4 (Mobile Identity IE encoding) — low nibble of
// each octet holds digit N, high nibble holds digit N+1; odd-digit
// strings get 0xF in the high nibble of the last octet.
//
// Same encoding the IMSI/MSIN/IMEI fields use across NAS, GTP-v2,
// PFCP and S-GW signaling. Also used by 5GS Mobile Identity IE
// SUCI / 5G-GUTI sub-structures (TS 24.501 §9.11.3.4).
//
// Non-digit characters return an error.
func EncodeTBCD(digits string) ([]byte, error) {
	out := make([]byte, 0, (len(digits)+1)/2)
	var pending byte
	half := false
	for _, c := range digits {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("EncodeTBCD: non-digit %q", c)
		}
		nib := byte(c - '0')
		if !half {
			pending = nib
			half = true
		} else {
			out = append(out, (nib<<4)|pending)
			half = false
		}
	}
	if half {
		out = append(out, 0xF0|pending)
	}
	return out, nil
}

// DecodeTBCD unpacks each octet's two digits in (low, high) order
// and stops at the first 0xF nibble (filler / end-of-digits).
func DecodeTBCD(b []byte) string {
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		lo := x & 0x0F
		hi := x >> 4
		if lo == 0xF {
			break
		}
		out = append(out, '0'+lo)
		if hi == 0xF {
			break
		}
		out = append(out, '0'+hi)
	}
	return string(out)
}

// PSIBitmap (TS 24.501 v19.6.2 §9.11.3.44 PDU session status +
// §9.11.3.57 Uplink data status). Both IEs share the same octet
// layout — octets 3..n are a bitmap where octet 3's bit N (1..8)
// means PSI (N-1), and octet 4's bit N means PSI (N+7), giving
// PSI 0..15 in the first two octets. PSI 0 (bit 1 of octet 3) is
// always spare per both clauses.
//
// Runtime exposes a typed PSIs slice; consumers walk that instead
// of unpacking the bytes themselves. Encode rebuilds the bitmap
// from the slice.
type PSIBitmap struct {
	PSIs []uint8 // sorted, deduplicated, 1..15 (PSI 0 always excluded)
}

func (p *PSIBitmap) Encode() []byte {
	out := make([]byte, 2)
	for _, psi := range p.PSIs {
		if psi == 0 || psi > 15 {
			continue // spec: PSI 0 spare, PSI > 15 not modelled here
		}
		if psi <= 7 {
			out[0] |= 1 << uint(psi)
		} else {
			out[1] |= 1 << uint(psi-8)
		}
	}
	return out
}

func (p *PSIBitmap) Decode(v []byte) error {
	if len(v) < 2 {
		return ErrBufferTooShort
	}
	p.PSIs = nil
	// Octet 3 (index 0): bit N (1..8) → PSI N-1. PSI 0 (bit 1) is
	// spare per spec — skip.
	for bit := 2; bit <= 8; bit++ {
		if v[0]&(1<<uint(bit-1)) != 0 {
			p.PSIs = append(p.PSIs, uint8(bit-1))
		}
	}
	// Octet 4 (index 1): bit N (1..8) → PSI 7+N (PSI 8..15).
	for bit := 1; bit <= 8; bit++ {
		if v[1]&(1<<uint(bit-1)) != 0 {
			p.PSIs = append(p.PSIs, uint8(7+bit))
		}
	}
	return nil
}

func (p *PSIBitmap) EncodeBytes() []byte        { return p.Encode() }
func (p *PSIBitmap) DecodeBytes(v []byte) error { return p.Decode(v) }

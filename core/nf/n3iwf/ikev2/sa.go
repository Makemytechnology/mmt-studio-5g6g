// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ikev2

import (
	"encoding/binary"
	"fmt"
)

// ProtocolID — RFC 7296 §3.3.1 "Protocol ID" table (verbatim):
//
//	IKE  1
//	AH   2
//	ESP  3
type ProtocolID uint8

const (
	ProtocolIKE ProtocolID = 1
	ProtocolAH  ProtocolID = 2
	ProtocolESP ProtocolID = 3
)

// TransformType — RFC 7296 §3.3.2 "Transform Type" table (verbatim):
//
//	Encryption Algorithm (ENCR)     1   IKE and ESP
//	Pseudorandom Function (PRF)     2   IKE
//	Integrity Algorithm (INTEG)     3   IKE*, AH, optional in ESP
//	Diffie-Hellman Group (D-H)      4   IKE, optional in AH & ESP
//	Extended Sequence Numbers (ESN) 5   AH and ESP
type TransformType uint8

const (
	TransformENCR  TransformType = 1
	TransformPRF   TransformType = 2
	TransformINTEG TransformType = 3
	TransformDH    TransformType = 4
	TransformESN   TransformType = 5
)

// Common Transform IDs (RFC 7296 §3.3.2 + IANA IKEv2 Parameters
// registry; see also RFC 4868 for SHA-256 PRF/INTEG and RFC 3526 for
// the MODP groups).
//
// ENCR (Type=1):
const (
	ENCR_NULL    uint16 = 11
	ENCR_AES_CBC uint16 = 12
	ENCR_AES_CTR uint16 = 13
)

// PRF (Type=2):
const (
	PRF_HMAC_SHA1   uint16 = 2
	PRF_HMAC_SHA256 uint16 = 5 // RFC 4868
)

// INTEG (Type=3):
const (
	INTEG_NONE              uint16 = 0
	INTEG_HMAC_SHA1_96      uint16 = 2
	INTEG_HMAC_SHA256_128   uint16 = 12 // RFC 4868
)

// D-H Group (Type=4) — RFC 7296 §3.3.2 (verbatim subset):
//
//	2048-bit MODP Group     14   [ADDGROUP]   ⟵ operator-mandated min
//	3072-bit MODP Group     15
const (
	DH_MODP_2048 uint16 = 14
	DH_MODP_3072 uint16 = 15
)

// ESN (Type=5):
const (
	ESN_NONE uint16 = 0
	ESN_USE  uint16 = 1
)

// Last Substruc constants — RFC 7296 §3.3.1 / §3.3.2 "Last Substruc":
//
//	0 ⇒ this is the last (Proposal | Transform) substructure
//	2 ⇒ another Proposal substructure follows
//	3 ⇒ another Transform substructure follows
const (
	lastSubstrucEnd           uint8 = 0
	lastSubstrucMoreProposal  uint8 = 2
	lastSubstrucMoreTransform uint8 = 3
)

// Attribute — RFC 7296 §3.3.5 transform attribute. Two formats:
//
//   - TV (high bit of the 16-bit Type field set): 4 bytes total,
//     Value is the next 2 octets as a uint16.
//   - TLV (high bit clear): 2-byte length precedes a variable-length
//     Value.
//
// In practice the only attribute the N3IWF emits is Key Length
// (Type=14, TV) on AES_CBC / AES_CTR transforms.
type Attribute struct {
	Type    uint16 // low 15 bits of the wire Type field
	IsTV    bool   // true ⇒ Value is encoded TV (TVValue used)
	Value   []byte // raw bytes when IsTV is false
	TVValue uint16
}

// AttrKeyLength is RFC 7296 §3.3.5 + IANA IKEv2 Transform Attribute
// Type 14, "Key Length (in bits)". Used with AES_CBC, AES_CTR.
const AttrKeyLength uint16 = 14

// Transform — RFC 7296 §3.3.2 Transform Substructure.
type Transform struct {
	Type       TransformType
	ID         uint16
	Attributes []Attribute
}

// Proposal — RFC 7296 §3.3.1 Proposal Substructure. SPI must be
// zero-length on initial IKE SA negotiation per §3.3.1.
type Proposal struct {
	Num        uint8
	ProtocolID ProtocolID
	SPI        []byte
	Transforms []Transform
}

// SA — RFC 7296 §3.3 Security Association payload body. The generic
// payload header is added by the caller via Payload{Type:PayloadSA,
// Data: sa.Marshal()}.
type SA struct {
	Proposals []Proposal
}

// Marshal encodes the SA payload body.
func (s *SA) Marshal() []byte {
	var out []byte
	for i, p := range s.Proposals {
		last := lastSubstrucEnd
		if i+1 < len(s.Proposals) {
			last = lastSubstrucMoreProposal
		}
		out = append(out, marshalProposal(p, last)...)
	}
	return out
}

// ParseSA decodes an SA payload body. Validates that proposal /
// transform / attribute lengths agree with their containers per
// §3.3 ("implementation MUST check that the total Payload Length is
// consistent with the payload's internal lengths and counts").
func ParseSA(buf []byte) (*SA, error) {
	sa := &SA{}
	off := 0
	for off < len(buf) {
		p, last, advance, err := parseProposal(buf[off:])
		if err != nil {
			return nil, fmt.Errorf("ikev2 SA: proposal at %d: %w", off, err)
		}
		sa.Proposals = append(sa.Proposals, p)
		off += advance
		if last == lastSubstrucEnd {
			break
		}
		if last != lastSubstrucMoreProposal {
			return nil, fmt.Errorf("ikev2 SA: proposal lastSubstruc must be 0 or 2, got %d", last)
		}
	}
	return sa, nil
}

// --- proposal / transform / attribute helpers ----------------------

func marshalProposal(p Proposal, last uint8) []byte {
	var trans []byte
	for i, t := range p.Transforms {
		tlast := lastSubstrucEnd
		if i+1 < len(p.Transforms) {
			tlast = lastSubstrucMoreTransform
		}
		trans = append(trans, marshalTransform(t, tlast)...)
	}
	plen := 8 + len(p.SPI) + len(trans)
	if plen > 0xFFFF {
		panic(fmt.Sprintf("ikev2: proposal too long (%d)", plen))
	}
	buf := make([]byte, 8+len(p.SPI))
	buf[0] = last
	// buf[1] = RESERVED, MUST be sent as zero per §3.3.1
	binary.BigEndian.PutUint16(buf[2:4], uint16(plen))
	buf[4] = p.Num
	buf[5] = byte(p.ProtocolID)
	buf[6] = byte(len(p.SPI))
	buf[7] = byte(len(p.Transforms))
	copy(buf[8:], p.SPI)
	return append(buf, trans...)
}

func parseProposal(buf []byte) (Proposal, uint8, int, error) {
	if len(buf) < 8 {
		return Proposal{}, 0, 0, fmt.Errorf("proposal header truncated (%d)", len(buf))
	}
	last := buf[0]
	plen := int(binary.BigEndian.Uint16(buf[2:4]))
	if plen < 8 || plen > len(buf) {
		return Proposal{}, 0, 0, fmt.Errorf("proposal length %d out of range", plen)
	}
	p := Proposal{
		Num:        buf[4],
		ProtocolID: ProtocolID(buf[5]),
	}
	spiSize := int(buf[6])
	numTrans := int(buf[7])
	off := 8
	if spiSize > 0 {
		if off+spiSize > plen {
			return Proposal{}, 0, 0, fmt.Errorf("proposal SPI of %d bytes overruns proposal length %d",
				spiSize, plen)
		}
		p.SPI = append([]byte(nil), buf[off:off+spiSize]...)
		off += spiSize
	}
	for i := 0; i < numTrans; i++ {
		if off >= plen {
			return Proposal{}, 0, 0, fmt.Errorf("proposal claimed %d transforms but body ran out at %d",
				numTrans, off)
		}
		t, tlast, adv, err := parseTransform(buf[off:plen])
		if err != nil {
			return Proposal{}, 0, 0, fmt.Errorf("transform %d: %w", i, err)
		}
		p.Transforms = append(p.Transforms, t)
		off += adv
		// Sanity: lastSubstruc on a non-final transform must be 3.
		if i+1 < numTrans && tlast != lastSubstrucMoreTransform {
			return Proposal{}, 0, 0, fmt.Errorf("transform %d/%d lastSubstruc=%d expected 3",
				i, numTrans, tlast)
		}
		if i+1 == numTrans && tlast != lastSubstrucEnd {
			return Proposal{}, 0, 0, fmt.Errorf("final transform lastSubstruc=%d expected 0", tlast)
		}
	}
	return p, last, plen, nil
}

func marshalTransform(t Transform, last uint8) []byte {
	var attrs []byte
	for _, a := range t.Attributes {
		attrs = append(attrs, marshalAttr(a)...)
	}
	tlen := 8 + len(attrs)
	if tlen > 0xFFFF {
		panic(fmt.Sprintf("ikev2: transform too long (%d)", tlen))
	}
	buf := make([]byte, 8)
	buf[0] = last
	// buf[1] = RESERVED, MUST be sent as zero per §3.3.2
	binary.BigEndian.PutUint16(buf[2:4], uint16(tlen))
	buf[4] = byte(t.Type)
	// buf[5] = RESERVED
	binary.BigEndian.PutUint16(buf[6:8], t.ID)
	return append(buf, attrs...)
}

func parseTransform(buf []byte) (Transform, uint8, int, error) {
	if len(buf) < 8 {
		return Transform{}, 0, 0, fmt.Errorf("transform header truncated (%d)", len(buf))
	}
	last := buf[0]
	tlen := int(binary.BigEndian.Uint16(buf[2:4]))
	if tlen < 8 || tlen > len(buf) {
		return Transform{}, 0, 0, fmt.Errorf("transform length %d out of range", tlen)
	}
	t := Transform{
		Type: TransformType(buf[4]),
		ID:   binary.BigEndian.Uint16(buf[6:8]),
	}
	off := 8
	for off < tlen {
		a, adv, err := parseAttr(buf[off:tlen])
		if err != nil {
			return Transform{}, 0, 0, fmt.Errorf("attribute at %d: %w", off, err)
		}
		t.Attributes = append(t.Attributes, a)
		off += adv
	}
	return t, last, tlen, nil
}

func marshalAttr(a Attribute) []byte {
	if a.IsTV {
		buf := make([]byte, 4)
		binary.BigEndian.PutUint16(buf[0:2], a.Type|0x8000)
		binary.BigEndian.PutUint16(buf[2:4], a.TVValue)
		return buf
	}
	buf := make([]byte, 4+len(a.Value))
	binary.BigEndian.PutUint16(buf[0:2], a.Type&0x7FFF)
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(a.Value)))
	copy(buf[4:], a.Value)
	return buf
}

func parseAttr(buf []byte) (Attribute, int, error) {
	if len(buf) < 4 {
		return Attribute{}, 0, fmt.Errorf("attribute header truncated (%d)", len(buf))
	}
	rawType := binary.BigEndian.Uint16(buf[0:2])
	a := Attribute{Type: rawType & 0x7FFF}
	if rawType&0x8000 != 0 {
		a.IsTV = true
		a.TVValue = binary.BigEndian.Uint16(buf[2:4])
		return a, 4, nil
	}
	vlen := int(binary.BigEndian.Uint16(buf[2:4]))
	if 4+vlen > len(buf) {
		return Attribute{}, 0, fmt.Errorf("TLV value len %d overruns buffer", vlen)
	}
	a.Value = append([]byte(nil), buf[4:4+vlen]...)
	return a, 4 + vlen, nil
}

// IKEDefaultProposal returns the operator-mandated minimum proposal
// for an IKE SA: AES-CBC-256 + HMAC-SHA256-128 + PRF-HMAC-SHA256 +
// MODP-2048 (RFC 7296 §3.3 + RFC 4868 + RFC 3526). The SPI is empty
// per §3.3.1 ("for an initial IKE SA negotiation, this field MUST
// be zero").
func IKEDefaultProposal() Proposal {
	return Proposal{
		Num:        1,
		ProtocolID: ProtocolIKE,
		Transforms: []Transform{
			{
				Type: TransformENCR, ID: ENCR_AES_CBC,
				Attributes: []Attribute{
					{Type: AttrKeyLength, IsTV: true, TVValue: 256},
				},
			},
			{Type: TransformPRF, ID: PRF_HMAC_SHA256},
			{Type: TransformINTEG, ID: INTEG_HMAC_SHA256_128},
			{Type: TransformDH, ID: DH_MODP_2048},
		},
	}
}

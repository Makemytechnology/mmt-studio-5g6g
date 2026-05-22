// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ikev2

import (
	"encoding/binary"
	"fmt"
)

// CFGType — RFC 7296 §3.15 "CFG Type" table (verbatim):
//
//	CFG_REQUEST  1
//	CFG_REPLY    2
//	CFG_SET      3
//	CFG_ACK      4
//
// "The type of exchange represented by the Configuration
// Attributes." — §3.15.
type CFGType uint8

const (
	CFGRequest CFGType = 1
	CFGReply   CFGType = 2
	CFGSet     CFGType = 3
	CFGAck     CFGType = 4
)

// CPAttrType — RFC 7296 §3.15.1 Configuration Attribute Types (subset
// covering what TS 24.502 §7.3.2.2 needs the N3IWF to return for
// non-3GPP UE inner-IP allocation):
//
//	INTERNAL_IP4_ADDRESS     1
//	INTERNAL_IP4_NETMASK     2
//	INTERNAL_IP4_DNS         3
//	INTERNAL_IP6_ADDRESS     8
//	INTERNAL_IP6_DNS         10
type CPAttrType uint16

const (
	CPInternalIP4Address CPAttrType = 1
	CPInternalIP4Netmask CPAttrType = 2
	CPInternalIP4DNS     CPAttrType = 3
	CPInternalIP6Address CPAttrType = 8
	CPInternalIP6DNS     CPAttrType = 10
)

// CPAttribute is one TLV inside a Configuration Payload (RFC 7296
// §3.15.1 Figure 23). The high bit of the wire 16-bit Type is
// reserved (MUST be zero) so the on-wire type is masked to 15 bits.
type CPAttribute struct {
	Type  CPAttrType
	Value []byte
}

// CP — RFC 7296 §3.15 Configuration Payload body. The generic
// payload header (§3.2) is added by the caller via Payload{Type:
// PayloadCP, Data: cp.Marshal()}.
//
//	CFG Type (1) | RESERVED (3) | Configuration Attributes (variable)
type CP struct {
	Type       CFGType
	Attributes []CPAttribute
}

// Marshal encodes the CP payload body. RESERVED is sent as zero per
// §3.15.
func (c *CP) Marshal() []byte {
	out := make([]byte, 4)
	out[0] = byte(c.Type)
	// out[1:4] = RESERVED zeros
	for _, a := range c.Attributes {
		buf := make([]byte, 4+len(a.Value))
		// §3.15.1: top bit of the 16-bit type field is the reserved
		// "R" bit (MUST be zero).
		binary.BigEndian.PutUint16(buf[0:2], uint16(a.Type)&0x7FFF)
		binary.BigEndian.PutUint16(buf[2:4], uint16(len(a.Value)))
		copy(buf[4:], a.Value)
		out = append(out, buf...)
	}
	return out
}

// ParseCP decodes a CP payload body, validating that each attribute's
// declared length stays inside the buffer.
//
// Per §3.15.1 the high bit of the wire type field is reserved and
// MUST be ignored on receipt — we mask it off rather than rejecting
// peers that incorrectly set it.
func ParseCP(buf []byte) (*CP, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("ikev2 CP: header truncated (%d)", len(buf))
	}
	c := &CP{Type: CFGType(buf[0])}
	off := 4
	for off < len(buf) {
		if off+4 > len(buf) {
			return nil, fmt.Errorf("ikev2 CP: attribute header truncated at %d", off)
		}
		raw := binary.BigEndian.Uint16(buf[off : off+2])
		alen := int(binary.BigEndian.Uint16(buf[off+2 : off+4]))
		if off+4+alen > len(buf) {
			return nil, fmt.Errorf("ikev2 CP: attribute %d value (len %d) overruns buffer",
				raw&0x7FFF, alen)
		}
		c.Attributes = append(c.Attributes, CPAttribute{
			Type:  CPAttrType(raw & 0x7FFF),
			Value: append([]byte(nil), buf[off+4:off+4+alen]...),
		})
		off += 4 + alen
	}
	return c, nil
}

// FindAttr returns the first attribute of the given type, or nil if
// none is present. Convenience for callers that only care about a
// single value (the IRAS/IRAC pattern from §3.15: server returns
// INTERNAL_IP4_ADDRESS with the assigned address).
func (c *CP) FindAttr(t CPAttrType) *CPAttribute {
	for i := range c.Attributes {
		if c.Attributes[i].Type == t {
			return &c.Attributes[i]
		}
	}
	return nil
}

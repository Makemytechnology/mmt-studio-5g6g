// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ikev2

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Payload is the parsed form of one generic payload from RFC 7296
// §3.2 — i.e. the 4-octet generic header (NextPayload | C/RESERVED |
// PayloadLength) plus the payload-specific bytes.
//
// Type is the type of THIS payload (not the next one). The Next
// Payload field on the wire is consumed by ParsePayloads to chain
// subsequent payloads; if you need it, look at the Type of the
// following Payload (or PayloadNone at the end).
type Payload struct {
	Type     PayloadType
	Critical bool
	Data     []byte // payload-specific octets, generic header stripped
}

// ParsePayloads decodes a chain of generic payloads starting at
// buf[0], where firstType identifies the type of the first payload
// (carried in the IKE header's NextPayload field per §3.1). The
// chain ends when a Next Payload field of zero is reached (§3.2).
//
// Returns an error if the on-wire Payload Length is < 4 (smaller
// than the generic header itself) or runs past the end of buf.
func ParsePayloads(buf []byte, firstType PayloadType) ([]Payload, error) {
	var out []Payload
	pt := firstType
	off := 0
	for pt != PayloadNone {
		if off+PayloadHeaderLen > len(buf) {
			return nil, fmt.Errorf("ikev2: payload header truncated at offset %d", off)
		}
		nextType := PayloadType(buf[off])
		critByte := buf[off+1]
		// §3.2: critical = high bit, low 7 bits RESERVED. Reject any
		// non-zero RESERVED to surface peer encoding bugs.
		if critByte&0x7F != 0 {
			return nil, fmt.Errorf("ikev2: reserved bits set in payload header (offset %d, type %d)",
				off, pt)
		}
		critical := critByte&0x80 != 0
		plLen := int(binary.BigEndian.Uint16(buf[off+2 : off+4]))
		if plLen < PayloadHeaderLen {
			return nil, fmt.Errorf("ikev2: payload length %d < generic header %d (offset %d)",
				plLen, PayloadHeaderLen, off)
		}
		if off+plLen > len(buf) {
			return nil, fmt.Errorf("ikev2: payload extends past buffer (offset %d, len %d, buf %d)",
				off, plLen, len(buf))
		}
		out = append(out, Payload{
			Type:     pt,
			Critical: critical,
			Data:     buf[off+PayloadHeaderLen : off+plLen],
		})
		off += plLen
		pt = nextType
	}
	return out, nil
}

// MarshalPayloads serialises a chain of payloads back to the wire
// form, computing each generic header's NextPayload from the
// following entry (or PayloadNone for the last).
//
// Returns the byte slice plus the type of the first payload — the
// caller writes that into the IKE header's NextPayload field per
// §3.1. If payloads is empty, returns (nil, PayloadNone).
func MarshalPayloads(payloads []Payload) ([]byte, PayloadType) {
	if len(payloads) == 0 {
		return nil, PayloadNone
	}
	var out []byte
	for i, p := range payloads {
		next := PayloadNone
		if i+1 < len(payloads) {
			next = payloads[i+1].Type
		}
		out = append(out, marshalOne(p, next)...)
	}
	return out, payloads[0].Type
}

func marshalOne(p Payload, next PayloadType) []byte {
	plLen := PayloadHeaderLen + len(p.Data)
	if plLen > 0xFFFF {
		// §3.2 Payload Length is 2 octets. Caller bug — fail loudly.
		panic(fmt.Sprintf("ikev2: payload type %d too large (%d bytes)", p.Type, plLen))
	}
	buf := make([]byte, plLen)
	buf[0] = byte(next)
	if p.Critical {
		buf[1] = 0x80
	}
	binary.BigEndian.PutUint16(buf[2:4], uint16(plLen))
	copy(buf[4:], p.Data)
	return buf
}

// Find returns the first payload of the given type, or nil if absent.
// Convenience for handlers that only care about a single payload.
func Find(payloads []Payload, t PayloadType) *Payload {
	for i := range payloads {
		if payloads[i].Type == t {
			return &payloads[i]
		}
	}
	return nil
}

// Validate returns nil if the inner buffer length matches what the
// payload's generic header claimed on the wire. Used by per-payload
// decoders before they index past their Data slice.
func (p *Payload) Validate(minLen int) error {
	if len(p.Data) < minLen {
		return errors.New("ikev2: payload too short for declared type")
	}
	return nil
}

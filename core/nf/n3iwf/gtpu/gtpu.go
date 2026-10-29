// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package gtpu implements the GTP-U (GTPv1-U) wire format for the
// N3 reference point between N3IWF and UPF, restricted to the user-
// plane scenario the N3IWF cares about: G-PDU encap/decap of an
// inner IP packet (TS 23.501 §6.3.1 places N3IWF as a RAN-like node
// and TS 24.502 §7.4 specifies that user-plane packets ride GTP-U
// to the UPF).
//
// Spec scope:
//
//	TS 29.281 §5.1     — Outline of the GTP-U Header (Figure 5.1-1)
//	TS 29.281 §5.2     — GTP-U Extension Header (deferred; we don't
//	                      generate or consume any here)
//	TS 29.281 §6.1     — Message types (Table 6.1-1) — only G-PDU
//	                      (255) and Echo Request/Response (1/2) are
//	                      meaningful for N3IWF; we implement G-PDU
//	                      and reject everything else for now.
//
// "Length: This field indicates the length in octets of the payload,
// i.e. the rest of the packet following the mandatory part of the
// GTP header (that is the first 8 octets). The Sequence Number, the
// N-PDU Number or any Extension headers shall be considered to be
// part of the payload, i.e. included in the length count." — §5.1
//
// We don't currently emit S/PN/E flags on outbound G-PDUs (per §5.1
// "the use of Sequence Numbers is optional for G-PDUs, ... should
// set the flag to '0'") but the parser accepts them and walks the
// optional 4-octet block when any flag is set.
package gtpu

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// MessageType — TS 29.281 §6.1 / Table 6.1-1 (verbatim subset).
type MessageType uint8

const (
	MsgEchoRequest  MessageType = 1
	MsgEchoResponse MessageType = 2
	MsgEndMarker    MessageType = 254
	MsgGPDU         MessageType = 255
)

// Mandatory header octets per §5.1: Flags(1) + MsgType(1) +
// Length(2) + TEID(4) = 8.
const HeaderLen = 8

// Flags in octet 1 of the header per §5.1 Figure 5.1-1.
//
// Bit layout (MSB ... LSB):  Version(3) | PT | (*spare) | E | S | PN
//
//	Version  ⟵ 0b001 for GTPv1 (§5.1: "shall be set to '1'")
//	PT       ⟵ 1 for GTP, 0 for GTP'
//	*        ⟵ spare ('0' on tx, ignored on rx — §5.1 NOTE 0)
//	E        ⟵ Extension Header present
//	S        ⟵ Sequence Number present
//	PN       ⟵ N-PDU Number present
const (
	flagVersion1 byte = 0b001 << 5 // GTPv1 (§5.1)
	flagPT       byte = 1 << 4     // PT=1 ⇒ GTP (vs GTP')
	flagE        byte = 1 << 2
	flagS        byte = 1 << 1
	flagPN       byte = 1 << 0

	// flagsBaseGPDU is the V=1, PT=1, no-extension flag byte for an
	// outbound G-PDU with no S/PN/E options.
	flagsBaseGPDU = flagVersion1 | flagPT
)

// Header is the parsed view of a GTP-U §5.1 header. All optional
// fields are zero when their flag bit was clear on the wire.
type Header struct {
	Version       uint8 // 1 for GTPv1
	ProtocolType  uint8 // 1 for GTP, 0 for GTP'
	Type          MessageType
	Length        uint16 // §5.1: length of payload AFTER the 8-octet mandatory header
	TEID          uint32

	HasSeq, HasNPDU, HasExt bool
	Seq                     uint16 // §5.1 NOTE 1
	NPDU                    uint8  // §5.1 NOTE 2
	NextExtType             uint8  // §5.1 NOTE 3
}

// EncapGPDU wraps an inner T-PDU (a complete IPv4 / IPv6 datagram)
// in a §5.1 G-PDU header with no optional fields.
//
// teid identifies the receiver's tunnel endpoint per §5.1 ("This
// field unambiguously identifies a tunnel endpoint in the receiving
// GTP-U protocol entity").
//
// "When setting up a GTP-U tunnel, the GTP-U entity shall not assign
// the value 'all zeros' to its own TEID." — §5.1. We accept teid==0
// here (callers might use it to forward to a backward-compat peer
// per §5.1 NOTE), but warn at higher layers.
func EncapGPDU(teid uint32, inner []byte) ([]byte, error) {
	if len(inner) == 0 {
		return nil, errors.New("gtpu: T-PDU empty")
	}
	if len(inner) > 0xFFFF {
		return nil, fmt.Errorf("gtpu: T-PDU length %d > 65535 (Length field is 16-bit)", len(inner))
	}
	out := make([]byte, HeaderLen+len(inner))
	out[0] = flagsBaseGPDU
	out[1] = byte(MsgGPDU)
	binary.BigEndian.PutUint16(out[2:4], uint16(len(inner)))
	binary.BigEndian.PutUint32(out[4:8], teid)
	copy(out[HeaderLen:], inner)
	return out, nil
}

// DecodeGPDU parses a wire-format GTP-U packet, expects type=G-PDU,
// and returns (Header, T-PDU). Walks the optional 4-octet S/PN/E
// block per §5.1 NOTE 4 ("This field shall be present if and only
// if any one or more of the S, PN and E flags are set") so peers
// that set S=1 still decode cleanly.
//
// Returns ErrNotGPDU if the message type isn't G-PDU — caller can
// branch off Echo Request/Response handling separately.
func DecodeGPDU(buf []byte) (*Header, []byte, error) {
	hdr, body, err := DecodeHeader(buf)
	if err != nil {
		return nil, nil, err
	}
	if hdr.Type != MsgGPDU {
		return hdr, body, fmt.Errorf("%w: got message type %d", ErrNotGPDU, hdr.Type)
	}
	return hdr, body, nil
}

// ErrNotGPDU is returned by DecodeGPDU when the parsed header is
// well-formed but isn't a G-PDU — caller can switch on the type to
// dispatch echo / signalling messages without re-parsing the header.
var ErrNotGPDU = errors.New("gtpu: not a G-PDU")

// DecodeHeader parses the §5.1 header (mandatory + optional block),
// validates §5.1 invariants (Version=1, PT=1, Length matches body),
// and returns the parsed header plus the T-PDU body (everything
// after the mandatory + optional headers).
func DecodeHeader(buf []byte) (*Header, []byte, error) {
	if len(buf) < HeaderLen {
		return nil, nil, fmt.Errorf("gtpu: packet too short for header (%d < %d)", len(buf), HeaderLen)
	}
	flags := buf[0]
	h := &Header{
		Version:      (flags >> 5) & 0x07,
		ProtocolType: (flags >> 4) & 0x01,
		Type:         MessageType(buf[1]),
		Length:       binary.BigEndian.Uint16(buf[2:4]),
		TEID:         binary.BigEndian.Uint32(buf[4:8]),
	}
	if h.Version != 1 {
		return nil, nil, fmt.Errorf("gtpu: version %d != 1 (TS 29.281 §5.1)", h.Version)
	}
	if h.ProtocolType != 1 {
		return nil, nil, fmt.Errorf("gtpu: PT %d — GTP' not supported (TS 29.281 §5.1)", h.ProtocolType)
	}

	// §5.1 NOTE 4: the optional 4-octet block is present iff any of
	// S/PN/E is set. Walk it, if so.
	hasOptional := flags&(flagS|flagPN|flagE) != 0
	off := HeaderLen
	if hasOptional {
		if len(buf) < HeaderLen+4 {
			return nil, nil, fmt.Errorf("gtpu: optional header block truncated (%d)", len(buf))
		}
		h.HasSeq = flags&flagS != 0
		h.HasNPDU = flags&flagPN != 0
		h.HasExt = flags&flagE != 0
		h.Seq = binary.BigEndian.Uint16(buf[8:10])
		h.NPDU = buf[10]
		h.NextExtType = buf[11]
		off += 4

		// §5.2 extension headers: walk if E=1 and NextExtType != 0.
		// We don't process specific extensions here — just skip past
		// each so the caller can find the T-PDU.
		nextType := h.NextExtType
		for h.HasExt && nextType != 0 {
			if off+1 > len(buf) {
				return nil, nil, fmt.Errorf("gtpu: extension header truncated at %d", off)
			}
			extLen := int(buf[off]) * 4 // §5.2: length in 4-octet units
			if extLen < 4 {
				return nil, nil, fmt.Errorf("gtpu: extension header length 0 (TS 29.281 §5.2.1)")
			}
			if off+extLen > len(buf) {
				return nil, nil, fmt.Errorf("gtpu: extension header overruns buffer (off=%d, len=%d)",
					off, extLen)
			}
			nextType = buf[off+extLen-1]
			off += extLen
		}
	}

	// §5.1 Length: payload (everything after the mandatory 8 octets)
	// must equal the Length field. Includes the optional block + any
	// extension headers per §5.1.
	want := int(h.Length)
	got := len(buf) - HeaderLen
	if got != want {
		return nil, nil, fmt.Errorf("gtpu: Length=%d != actual payload %d (TS 29.281 §5.1)",
			want, got)
	}
	return h, buf[off:], nil
}

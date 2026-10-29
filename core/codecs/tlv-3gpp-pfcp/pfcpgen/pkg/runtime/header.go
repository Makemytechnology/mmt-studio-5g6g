// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import "fmt"

// Header — PFCP message header per TS 29.244 §7.2.2.
//
// Octet 1: version(bits 6-8)=001 for v1, spare(4-5), FO(3), MP(2), S(1)
// Octet 2: Message Type
// Octet 3-4: Message Length (big-endian; bytes after the 4-octet basic header)
// [Octet 5-12 only if S=1]: SEID (uint64)
// Next 3 octets: Sequence Number (24-bit)
// Next 1 octet: Message Priority (high nibble, if MP=1) + spare
type Header struct {
	Version         uint8  // always 1 for this spec version
	FollowOn        bool   // FO flag
	MessagePriority bool   // MP flag
	HasSEID         bool   // S flag
	MessageType     uint8
	Length          uint16
	SEID            uint64 // valid only when HasSEID
	SequenceNumber  uint32 // 24-bit
	Priority        uint8  // 4-bit, valid only when MessagePriority
}

// Fixed size of the header (excluding the initial 4-octet basic header).
// HeaderSize returns 16 when S=1, 8 when S=0.
func (h *Header) HeaderSize() int {
	if h.HasSEID {
		return 16
	}
	return 8
}

// ParseHeader reads a PFCP header from data. Returns the header plus the
// offset at which the IEs begin.
func ParseHeader(data []byte) (*Header, int, error) {
	if len(data) < 8 {
		return nil, 0, ErrBufferTooShort
	}
	h := &Header{
		Version:         (data[0] >> 5) & 0x07,
		FollowOn:        data[0]&0x04 != 0,
		MessagePriority: data[0]&0x02 != 0,
		HasSEID:         data[0]&0x01 != 0,
		MessageType:     data[1],
		Length:          uint16(data[2])<<8 | uint16(data[3]),
	}
	if h.Version != 1 {
		return nil, 0, fmt.Errorf("%w: %d", ErrInvalidVersion, h.Version)
	}
	off := 4
	if h.HasSEID {
		if len(data) < off+8 {
			return nil, 0, ErrBufferTooShort
		}
		h.SEID = uint64(data[off])<<56 | uint64(data[off+1])<<48 |
			uint64(data[off+2])<<40 | uint64(data[off+3])<<32 |
			uint64(data[off+4])<<24 | uint64(data[off+5])<<16 |
			uint64(data[off+6])<<8 | uint64(data[off+7])
		off += 8
	}
	if len(data) < off+4 {
		return nil, 0, ErrBufferTooShort
	}
	h.SequenceNumber = uint32(data[off])<<16 | uint32(data[off+1])<<8 | uint32(data[off+2])
	h.Priority = (data[off+3] >> 4) & 0x0F
	off += 4
	return h, off, nil
}

// Encode builds the header bytes. Caller sets Length to the IE-payload byte
// count plus the header size minus 4 (PFCP spec: Length excludes the first
// four basic-header octets).
func (h *Header) Encode() []byte {
	b := byte((h.Version & 0x07) << 5)
	if h.FollowOn {
		b |= 0x04
	}
	if h.MessagePriority {
		b |= 0x02
	}
	if h.HasSEID {
		b |= 0x01
	}
	out := []byte{
		b,
		h.MessageType,
		byte(h.Length >> 8), byte(h.Length),
	}
	if h.HasSEID {
		out = append(out,
			byte(h.SEID>>56), byte(h.SEID>>48), byte(h.SEID>>40), byte(h.SEID>>32),
			byte(h.SEID>>24), byte(h.SEID>>16), byte(h.SEID>>8), byte(h.SEID),
		)
	}
	out = append(out,
		byte(h.SequenceNumber>>16), byte(h.SequenceNumber>>8), byte(h.SequenceNumber),
		(h.Priority&0x0F)<<4,
	)
	return out
}

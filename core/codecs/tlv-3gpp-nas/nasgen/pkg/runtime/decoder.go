// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import "encoding/binary"

// NASBuffer is a bounds-checked byte reader.
// Every read returns ErrBufferTooShort rather than panicking.
type NASBuffer struct {
	data   []byte
	offset int
}

func NewNASBuffer(data []byte) *NASBuffer { return &NASBuffer{data: data} }

func (b *NASBuffer) Remaining() int { return len(b.data) - b.offset }
func (b *NASBuffer) Offset() int    { return b.offset }
func (b *NASBuffer) EOF() bool      { return b.offset >= len(b.data) }

// Reset rewinds the buffer to offset 0.
func (b *NASBuffer) Reset() { b.offset = 0 }

// Rewind moves the offset back by n bytes (used to unread a peeked IEI).
func (b *NASBuffer) Rewind(n int) error {
	if n > b.offset {
		return ErrBufferTooShort
	}
	b.offset -= n
	return nil
}

func (b *NASBuffer) ReadByte() (byte, error) {
	if b.Remaining() < 1 {
		return 0, ErrBufferTooShort
	}
	v := b.data[b.offset]
	b.offset++
	return v, nil
}

func (b *NASBuffer) ReadBytes(n int) ([]byte, error) {
	if n < 0 || b.Remaining() < n {
		return nil, ErrBufferTooShort
	}
	out := make([]byte, n)
	copy(out, b.data[b.offset:b.offset+n])
	b.offset += n
	return out, nil
}

func (b *NASBuffer) ReadUint16() (uint16, error) {
	if b.Remaining() < 2 {
		return 0, ErrBufferTooShort
	}
	v := binary.BigEndian.Uint16(b.data[b.offset:])
	b.offset += 2
	return v, nil
}

func (b *NASBuffer) ReadUint24() (uint32, error) {
	if b.Remaining() < 3 {
		return 0, ErrBufferTooShort
	}
	v := uint32(b.data[b.offset])<<16 |
		uint32(b.data[b.offset+1])<<8 |
		uint32(b.data[b.offset+2])
	b.offset += 3
	return v, nil
}

func (b *NASBuffer) ReadUint32() (uint32, error) {
	if b.Remaining() < 4 {
		return 0, ErrBufferTooShort
	}
	v := binary.BigEndian.Uint32(b.data[b.offset:])
	b.offset += 4
	return v, nil
}

func (b *NASBuffer) PeekByte() (byte, error) {
	if b.Remaining() < 1 {
		return 0, ErrBufferTooShort
	}
	return b.data[b.offset], nil
}

// --- TLV decoders (TS 24.007 §11.2.4) ---

// DecodeTLV reads a Type 4 TLV: 1-byte IEI, 1-byte length, value.
// The caller must have already consumed (or peeked) the IEI byte — by convention
// the generated decoder peeks the IEI to dispatch, then calls DecodeTLV which
// re-reads IEI+L+V.
func (b *NASBuffer) DecodeTLV() (iei uint8, value []byte, err error) {
	iei, err = b.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length, err := b.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	value, err = b.ReadBytes(int(length))
	return iei, value, err
}

// DecodeTLVE reads a Type 6 TLV-E: 1-byte IEI, 2-byte big-endian length, value.
func (b *NASBuffer) DecodeTLVE() (iei uint8, value []byte, err error) {
	iei, err = b.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length, err := b.ReadUint16()
	if err != nil {
		return 0, nil, err
	}
	value, err = b.ReadBytes(int(length))
	return iei, value, err
}

// DecodeTV reads a Type 3 TV: 1-byte IEI + fixed-length value.
// Half-octet TV (Type 1) is handled separately — see ReadHalfOctetByte.
func (b *NASBuffer) DecodeTV(fixedLen int) (iei uint8, value []byte, err error) {
	iei, err = b.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	value, err = b.ReadBytes(fixedLen)
	return iei, value, err
}

// DecodeLV reads a Type 4 LV (mandatory, no tag): 1-byte length + value.
func (b *NASBuffer) DecodeLV() ([]byte, error) {
	length, err := b.ReadByte()
	if err != nil {
		return nil, err
	}
	return b.ReadBytes(int(length))
}

// DecodeLVE reads a Type 6 LV-E (mandatory, no tag): 2-byte length + value.
func (b *NASBuffer) DecodeLVE() ([]byte, error) {
	length, err := b.ReadUint16()
	if err != nil {
		return nil, err
	}
	return b.ReadBytes(int(length))
}

// ReadHalfOctetByte reads one byte that carries two half-octet values.
// Returns (low nibble, high nibble).
func (b *NASBuffer) ReadHalfOctetByte() (low, high byte, err error) {
	v, err := b.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	return v & 0x0F, (v >> 4) & 0x0F, nil
}

// SkipUnknownIE skips an unrecognized optional IE based on its IEI high nibble.
// Per TS 24.007 §11.2.4: high nibble 0-7 → Type 4 TLV (read 1-byte length, skip);
// high nibble 8-F → Type 1/2 half-octet or TV, skip 1 byte total (already read).
// Precondition: the IEI byte has been consumed.
func (b *NASBuffer) SkipUnknownIE(iei byte) error {
	if iei>>4 <= 0x7 {
		length, err := b.ReadByte()
		if err != nil {
			return err
		}
		_, err = b.ReadBytes(int(length))
		return err
	}
	// High-nibble 8-F: Type 1 TV — single byte (IEI already consumed).
	return nil
}

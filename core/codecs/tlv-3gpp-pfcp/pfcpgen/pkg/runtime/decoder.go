// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import "encoding/binary"

// Buffer is a bounds-checked byte reader. Never panics.
type Buffer struct {
	data []byte
	off  int
}

func NewBuffer(data []byte) *Buffer { return &Buffer{data: data} }

func (b *Buffer) Remaining() int { return len(b.data) - b.off }
func (b *Buffer) Offset() int    { return b.off }
func (b *Buffer) EOF() bool      { return b.off >= len(b.data) }

func (b *Buffer) ReadByte() (byte, error) {
	if b.Remaining() < 1 {
		return 0, ErrBufferTooShort
	}
	v := b.data[b.off]
	b.off++
	return v, nil
}

func (b *Buffer) ReadBytes(n int) ([]byte, error) {
	if n < 0 || b.Remaining() < n {
		return nil, ErrBufferTooShort
	}
	out := make([]byte, n)
	copy(out, b.data[b.off:b.off+n])
	b.off += n
	return out, nil
}

func (b *Buffer) ReadUint16() (uint16, error) {
	if b.Remaining() < 2 {
		return 0, ErrBufferTooShort
	}
	v := binary.BigEndian.Uint16(b.data[b.off:])
	b.off += 2
	return v, nil
}

func (b *Buffer) ReadUint24() (uint32, error) {
	if b.Remaining() < 3 {
		return 0, ErrBufferTooShort
	}
	v := uint32(b.data[b.off])<<16 | uint32(b.data[b.off+1])<<8 | uint32(b.data[b.off+2])
	b.off += 3
	return v, nil
}

func (b *Buffer) ReadUint32() (uint32, error) {
	if b.Remaining() < 4 {
		return 0, ErrBufferTooShort
	}
	v := binary.BigEndian.Uint32(b.data[b.off:])
	b.off += 4
	return v, nil
}

func (b *Buffer) ReadUint64() (uint64, error) {
	if b.Remaining() < 8 {
		return 0, ErrBufferTooShort
	}
	v := binary.BigEndian.Uint64(b.data[b.off:])
	b.off += 8
	return v, nil
}

// DecodedIE — one IE extracted from a byte stream.
// EnterpriseID is 0 for standard (non-enterprise) IEs.
type DecodedIE struct {
	Type         uint16
	EnterpriseID uint32
	Value        []byte
}

// DecodeIE reads one IE from the buffer (TS 29.244 §8.1.1):
//
//	Octet 1-2: Type (if bit 16 = 1, IE is enterprise-specific)
//	Octet 3-4: Length (big-endian, value part only)
//	Octet 5-8 (only if enterprise): Enterprise ID
//	Octet 5-(N+4) or 9-(N+8): Value
//
// Returns ErrBufferTooShort on truncation, ErrInvalidLength if declared length
// exceeds remaining bytes after the Type/Length header.
func (b *Buffer) DecodeIE() (*DecodedIE, error) {
	ieType, err := b.ReadUint16()
	if err != nil {
		return nil, err
	}
	length, err := b.ReadUint16()
	if err != nil {
		return nil, err
	}
	ie := &DecodedIE{}
	valueLen := int(length)
	if ieType&0x8000 != 0 {
		// Enterprise-specific: clear MSB from Type, read 4-byte Enterprise ID.
		ie.Type = ieType & 0x7FFF
		eid, err := b.ReadUint32()
		if err != nil {
			return nil, err
		}
		ie.EnterpriseID = eid
		valueLen -= 4
		if valueLen < 0 {
			return nil, ErrInvalidLength
		}
	} else {
		ie.Type = ieType
	}
	v, err := b.ReadBytes(valueLen)
	if err != nil {
		return nil, err
	}
	ie.Value = v
	return ie, nil
}

// ForEachIE iterates over every IE remaining in the buffer, invoking fn with
// each decoded entry. Iteration halts on the first error from fn or decode.
// Used for both message-level IE loops and grouped-IE recursion.
func (b *Buffer) ForEachIE(fn func(*DecodedIE) error) error {
	for !b.EOF() {
		ie, err := b.DecodeIE()
		if err != nil {
			return err
		}
		if err := fn(ie); err != nil {
			return err
		}
	}
	return nil
}

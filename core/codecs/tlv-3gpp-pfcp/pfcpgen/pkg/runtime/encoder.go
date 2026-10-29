// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Encoder builds a byte stream one field at a time.
type Encoder struct {
	buf bytes.Buffer
}

func NewEncoder() *Encoder { return &Encoder{} }

func (e *Encoder) Bytes() []byte { return e.buf.Bytes() }
func (e *Encoder) Len() int      { return e.buf.Len() }
func (e *Encoder) Reset()        { e.buf.Reset() }

func (e *Encoder) WriteByte(b byte) { _ = e.buf.WriteByte(b) }

func (e *Encoder) WriteBytes(data []byte) { _, _ = e.buf.Write(data) }

func (e *Encoder) WriteUint16(v uint16) {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	e.WriteBytes(b[:])
}

func (e *Encoder) WriteUint24(v uint32) {
	e.WriteBytes([]byte{byte(v >> 16), byte(v >> 8), byte(v)})
}

func (e *Encoder) WriteUint32(v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	e.WriteBytes(b[:])
}

func (e *Encoder) WriteUint64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	e.WriteBytes(b[:])
}

// EncodeIE writes one standard (non-enterprise) IE: T(2) + L(2) + V.
func (e *Encoder) EncodeIE(ieType uint16, value []byte) error {
	if len(value) > 0xFFFF {
		return fmt.Errorf("pfcp: IE value too long: %d", len(value))
	}
	if ieType&0x8000 != 0 {
		return fmt.Errorf("pfcp: IE type %d has enterprise bit set; use EncodeEnterpriseIE", ieType)
	}
	e.WriteUint16(ieType)
	e.WriteUint16(uint16(len(value)))
	e.WriteBytes(value)
	return nil
}

// EncodeEnterpriseIE writes an enterprise-specific IE.
// The high bit of Type is forced on. Length includes the 4-byte Enterprise ID.
func (e *Encoder) EncodeEnterpriseIE(ieType uint16, enterpriseID uint32, value []byte) error {
	if len(value)+4 > 0xFFFF {
		return fmt.Errorf("pfcp: enterprise IE value too long: %d", len(value))
	}
	e.WriteUint16(ieType | 0x8000)
	e.WriteUint16(uint16(len(value) + 4))
	e.WriteUint32(enterpriseID)
	e.WriteBytes(value)
	return nil
}

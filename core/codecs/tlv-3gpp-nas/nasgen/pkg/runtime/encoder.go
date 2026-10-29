// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// NASEncoder builds a NAS PDU byte-by-byte.
// Unlike the decoder, the encoder path is allowed to return errors for
// length-overflow in TLV-E (>65535 bytes) and similar constraints.
type NASEncoder struct {
	buf bytes.Buffer
}

func NewNASEncoder() *NASEncoder { return &NASEncoder{} }

func (e *NASEncoder) Bytes() []byte { return e.buf.Bytes() }
func (e *NASEncoder) Len() int      { return e.buf.Len() }
func (e *NASEncoder) Reset()        { e.buf.Reset() }

func (e *NASEncoder) WriteByte(b byte) error { return e.buf.WriteByte(b) }

func (e *NASEncoder) WriteBytes(data []byte) error {
	_, err := e.buf.Write(data)
	return err
}

func (e *NASEncoder) WriteUint16(v uint16) error {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	_, err := e.buf.Write(b[:])
	return err
}

func (e *NASEncoder) WriteUint24(v uint32) error {
	if v > 0xFFFFFF {
		return fmt.Errorf("nas: uint24 overflow: %d", v)
	}
	return e.WriteBytes([]byte{byte(v >> 16), byte(v >> 8), byte(v)})
}

func (e *NASEncoder) WriteUint32(v uint32) error {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, err := e.buf.Write(b[:])
	return err
}

// EncodeTLV writes IEI + 1-byte length + value (Type 4).
func (e *NASEncoder) EncodeTLV(iei uint8, value []byte) error {
	if len(value) > 0xFF {
		return fmt.Errorf("nas: TLV value too long: %d (max 255)", len(value))
	}
	_ = e.WriteByte(iei)
	_ = e.WriteByte(byte(len(value)))
	return e.WriteBytes(value)
}

// EncodeTLVE writes IEI + 2-byte length + value (Type 6).
func (e *NASEncoder) EncodeTLVE(iei uint8, value []byte) error {
	if len(value) > 0xFFFF {
		return fmt.Errorf("nas: TLV-E value too long: %d (max 65535)", len(value))
	}
	_ = e.WriteByte(iei)
	_ = e.WriteUint16(uint16(len(value)))
	return e.WriteBytes(value)
}

// EncodeTV writes IEI + fixed-length value (Type 3).
func (e *NASEncoder) EncodeTV(iei uint8, value []byte) error {
	_ = e.WriteByte(iei)
	return e.WriteBytes(value)
}

// EncodeTVHalfOctet writes one byte: IEI high nibble, value low nibble (Type 1 TV).
func (e *NASEncoder) EncodeTVHalfOctet(iei, value byte) error {
	return e.WriteByte((iei&0x0F)<<4 | (value & 0x0F))
}

// EncodeT writes a single IEI byte (Type 2: tag-only, presence indicator).
func (e *NASEncoder) EncodeT(iei uint8) error { return e.WriteByte(iei) }

// EncodeLV writes 1-byte length + value.
func (e *NASEncoder) EncodeLV(value []byte) error {
	if len(value) > 0xFF {
		return fmt.Errorf("nas: LV value too long: %d", len(value))
	}
	_ = e.WriteByte(byte(len(value)))
	return e.WriteBytes(value)
}

// EncodeLVE writes 2-byte length + value.
func (e *NASEncoder) EncodeLVE(value []byte) error {
	if len(value) > 0xFFFF {
		return fmt.Errorf("nas: LV-E value too long: %d", len(value))
	}
	_ = e.WriteUint16(uint16(len(value)))
	return e.WriteBytes(value)
}

// WriteHalfOctetByte packs two half-octet values into one byte.
// low → bits 1-4, high → bits 5-8.
func (e *NASEncoder) WriteHalfOctetByte(low, high byte) error {
	return e.WriteByte((high&0x0F)<<4 | (low & 0x0F))
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import (
	"errors"
	"fmt"
	"math/bits"
)

// PerBitData provides bit-level read/write over a byte buffer, shared by both
// APER (aligned PER) and UPER (unaligned PER) encoders. The only difference is
// whether alignment-to-octet boundaries is honoured; set Aligned=true for APER.
type PerBitData struct {
	// Buffer contains encoded bytes.
	Buffer []byte
	// NumBits is the number of bits currently used in Buffer (writer) or total (reader).
	NumBits uint64
	// Pos is the current bit position (for reading).
	Pos uint64
	// Aligned true = APER, false = UPER.
	Aligned bool
}

func NewWriter(aligned bool) *PerBitData {
	return &PerBitData{Aligned: aligned}
}

func NewReader(buf []byte, aligned bool) *PerBitData {
	return &PerBitData{Buffer: buf, NumBits: uint64(len(buf)) * 8, Aligned: aligned}
}

// Bytes returns the final encoded byte slice. Any trailing bits in the last byte are zero-padded.
func (p *PerBitData) Bytes() []byte {
	return p.Buffer
}

// PutBits writes value as numBits big-endian bits.
func (p *PerBitData) PutBits(value uint64, numBits uint) error {
	if numBits == 0 {
		return nil
	}
	if numBits > 64 {
		return fmt.Errorf("PutBits: numBits %d > 64", numBits)
	}
	remaining := numBits
	for remaining > 0 {
		byteOff := p.NumBits / 8
		bitOff := uint(p.NumBits % 8)
		if int(byteOff) >= len(p.Buffer) {
			p.Buffer = append(p.Buffer, 0)
		}
		free := 8 - bitOff
		take := remaining
		if uint(take) > free {
			take = uint(free)
		}
		// extract top `take` bits of the remaining value
		shift := remaining - take
		chunk := byte((value >> shift) & ((1 << take) - 1))
		chunk <<= (free - uint(take))
		p.Buffer[byteOff] |= chunk
		p.NumBits += uint64(take)
		remaining -= take
	}
	return nil
}

// GetBits reads numBits as an unsigned big-endian integer.
func (p *PerBitData) GetBits(numBits uint) (uint64, error) {
	if numBits == 0 {
		return 0, nil
	}
	if numBits > 64 {
		return 0, fmt.Errorf("GetBits: numBits %d > 64", numBits)
	}
	if p.Pos+uint64(numBits) > p.NumBits {
		return 0, errors.New("GetBits: out of data")
	}
	var out uint64
	remaining := numBits
	for remaining > 0 {
		byteOff := p.Pos / 8
		bitOff := uint(p.Pos % 8)
		free := 8 - bitOff
		take := remaining
		if uint(take) > free {
			take = uint(free)
		}
		b := p.Buffer[byteOff] >> (free - uint(take))
		b &= byte((1 << take) - 1)
		out = (out << take) | uint64(b)
		p.Pos += uint64(take)
		remaining -= take
	}
	return out, nil
}

// AlignToByte pads to an octet boundary (APER only). A no-op in UPER.
func (p *PerBitData) AlignToByte() error {
	if !p.Aligned {
		return nil
	}
	rem := uint(p.NumBits % 8)
	if rem == 0 {
		return nil
	}
	return p.PutBits(0, 8-rem)
}

// AlignReadToByte advances the read position to the next octet boundary (APER only).
func (p *PerBitData) AlignReadToByte() error {
	if !p.Aligned {
		return nil
	}
	rem := uint(p.Pos % 8)
	if rem == 0 {
		return nil
	}
	p.Pos += uint64(8 - rem)
	if p.Pos > p.NumBits {
		return errors.New("AlignReadToByte: out of data")
	}
	return nil
}

// --- Constrained whole number (X.691 §10.5) ---

// bitsNeeded returns the minimum number of bits needed to encode values 0..rangeVal-1.
// For a range of 1 (single legal value), returns 0 bits.
func bitsNeeded(rangeVal uint64) uint {
	if rangeVal <= 1 {
		return 0
	}
	return uint(bits.Len64(rangeVal - 1))
}

// PutConstrainedWhole encodes value in [lb..ub] per X.691 §10.5.6/§10.5.7.
func (p *PerBitData) PutConstrainedWhole(value, lb, ub int64) error {
	if value < lb || value > ub {
		return fmt.Errorf("INTEGER value %d violates constraint VALUE(%d..%d)", value, lb, ub)
	}
	rng := uint64(ub-lb) + 1
	offset := uint64(value - lb)
	switch {
	case rng == 1:
		return nil
	case rng <= 255:
		// Always bit-field; no alignment.
		return p.PutBits(offset, bitsNeeded(rng))
	case rng == 256:
		if p.Aligned {
			if err := p.AlignToByte(); err != nil {
				return err
			}
		}
		return p.PutBits(offset, 8)
	case rng <= 65536:
		if p.Aligned {
			if err := p.AlignToByte(); err != nil {
				return err
			}
		}
		return p.PutBits(offset, 16)
	default:
		// Indefinite-length case: length-of-octets (1..k) then value in minimum octets.
		octets := minOctets(offset)
		lenBits := bitsNeeded(uint64(maxOctets(rng)))
		if err := p.PutBits(uint64(octets-1), lenBits); err != nil {
			return err
		}
		if p.Aligned {
			if err := p.AlignToByte(); err != nil {
				return err
			}
		}
		return p.PutBits(offset, uint(octets)*8)
	}
}

func (p *PerBitData) GetConstrainedWhole(lb, ub int64) (int64, error) {
	rng := uint64(ub-lb) + 1
	switch {
	case rng == 1:
		return lb, nil
	case rng <= 255:
		v, err := p.GetBits(bitsNeeded(rng))
		if err != nil {
			return 0, err
		}
		return lb + int64(v), nil
	case rng == 256:
		if p.Aligned {
			if err := p.AlignReadToByte(); err != nil {
				return 0, err
			}
		}
		v, err := p.GetBits(8)
		if err != nil {
			return 0, err
		}
		return lb + int64(v), nil
	case rng <= 65536:
		if p.Aligned {
			if err := p.AlignReadToByte(); err != nil {
				return 0, err
			}
		}
		v, err := p.GetBits(16)
		if err != nil {
			return 0, err
		}
		return lb + int64(v), nil
	default:
		lenBits := bitsNeeded(uint64(maxOctets(rng)))
		k, err := p.GetBits(lenBits)
		if err != nil {
			return 0, err
		}
		octets := k + 1
		if p.Aligned {
			if err := p.AlignReadToByte(); err != nil {
				return 0, err
			}
		}
		v, err := p.GetBits(uint(octets) * 8)
		if err != nil {
			return 0, err
		}
		return lb + int64(v), nil
	}
}

func minOctets(v uint64) int {
	if v == 0 {
		return 1
	}
	b := bits.Len64(v)
	return (b + 7) / 8
}

func maxOctets(rng uint64) int {
	b := bits.Len64(rng - 1)
	return (b + 7) / 8
}

// --- Length determinant (X.691 §10.9) ---

// PutLengthDeterminant emits a length determinant. If sizeRange <= 65536 it is
// encoded as a constrained whole; otherwise it uses the normally-encoded form
// (short form <128, long form <16384, fragmentation otherwise).
func (p *PerBitData) PutLengthDeterminant(length uint64, sizeLB, sizeUB uint64, hasBounds bool) error {
	if hasBounds {
		rng := sizeUB - sizeLB + 1
		if rng == 1 {
			return nil
		}
		if rng <= 65536 {
			return p.PutConstrainedWhole(int64(length), int64(sizeLB), int64(sizeUB))
		}
	}
	// Unconstrained / semi-constrained form.
	if p.Aligned {
		if err := p.AlignToByte(); err != nil {
			return err
		}
	}
	switch {
	case length < 128:
		if err := p.PutBits(0, 1); err != nil {
			return err
		}
		return p.PutBits(length, 7)
	case length < 16384:
		if err := p.PutBits(0b10, 2); err != nil {
			return err
		}
		return p.PutBits(length, 14)
	default:
		// Fragmentation not yet supported for encode (rare in 3GPP PDUs).
		return fmt.Errorf("length %d requires fragmentation (not yet implemented)", length)
	}
}

func (p *PerBitData) GetLengthDeterminant(sizeLB, sizeUB uint64, hasBounds bool) (uint64, error) {
	if hasBounds {
		rng := sizeUB - sizeLB + 1
		if rng == 1 {
			return sizeLB, nil
		}
		if rng <= 65536 {
			v, err := p.GetConstrainedWhole(int64(sizeLB), int64(sizeUB))
			if err != nil {
				return 0, err
			}
			return uint64(v), nil
		}
	}
	if p.Aligned {
		if err := p.AlignReadToByte(); err != nil {
			return 0, err
		}
	}
	first, err := p.GetBits(1)
	if err != nil {
		return 0, err
	}
	if first == 0 {
		v, err := p.GetBits(7)
		return v, err
	}
	second, err := p.GetBits(1)
	if err != nil {
		return 0, err
	}
	if second == 0 {
		v, err := p.GetBits(14)
		return v, err
	}
	return 0, errors.New("fragmented length determinant (not yet implemented)")
}

// --- Normally small non-negative whole number (X.691 §10.6) ---
// Used for extension additions and extension enumerations.

func (p *PerBitData) PutNormallySmallNonNegative(v uint64) error {
	if v < 64 {
		// 1 bit "0" then 6-bit value
		if err := p.PutBits(0, 1); err != nil {
			return err
		}
		return p.PutBits(v, 6)
	}
	if err := p.PutBits(1, 1); err != nil {
		return err
	}
	// length determinant of the octets then value
	octets := minOctets(v)
	if err := p.PutLengthDeterminant(uint64(octets), 0, 0, false); err != nil {
		return err
	}
	if p.Aligned {
		if err := p.AlignToByte(); err != nil {
			return err
		}
	}
	return p.PutBits(v, uint(octets)*8)
}

func (p *PerBitData) GetNormallySmallNonNegative() (uint64, error) {
	bit, err := p.GetBits(1)
	if err != nil {
		return 0, err
	}
	if bit == 0 {
		return p.GetBits(6)
	}
	length, err := p.GetLengthDeterminant(0, 0, false)
	if err != nil {
		return 0, err
	}
	if p.Aligned {
		if err := p.AlignReadToByte(); err != nil {
			return 0, err
		}
	}
	return p.GetBits(uint(length) * 8)
}

// --- Semi-constrained & unconstrained whole numbers ---

func (p *PerBitData) PutSemiConstrainedWhole(value, lb int64) error {
	off := uint64(value - lb)
	octets := minOctets(off)
	if err := p.PutLengthDeterminant(uint64(octets), 0, 0, false); err != nil {
		return err
	}
	if p.Aligned {
		if err := p.AlignToByte(); err != nil {
			return err
		}
	}
	return p.PutBits(off, uint(octets)*8)
}

func (p *PerBitData) GetSemiConstrainedWhole(lb int64) (int64, error) {
	length, err := p.GetLengthDeterminant(0, 0, false)
	if err != nil {
		return 0, err
	}
	if p.Aligned {
		if err := p.AlignReadToByte(); err != nil {
			return 0, err
		}
	}
	v, err := p.GetBits(uint(length) * 8)
	if err != nil {
		return 0, err
	}
	return lb + int64(v), nil
}

func (p *PerBitData) PutUnconstrainedWhole(value int64) error {
	octets := signedMinOctets(value)
	if err := p.PutLengthDeterminant(uint64(octets), 0, 0, false); err != nil {
		return err
	}
	if p.Aligned {
		if err := p.AlignToByte(); err != nil {
			return err
		}
	}
	// Two's-complement encoding.
	mask := uint64(1)<<(uint(octets)*8) - 1
	return p.PutBits(uint64(value)&mask, uint(octets)*8)
}

func (p *PerBitData) GetUnconstrainedWhole() (int64, error) {
	length, err := p.GetLengthDeterminant(0, 0, false)
	if err != nil {
		return 0, err
	}
	if p.Aligned {
		if err := p.AlignReadToByte(); err != nil {
			return 0, err
		}
	}
	v, err := p.GetBits(uint(length) * 8)
	if err != nil {
		return 0, err
	}
	// sign-extend
	n := uint(length) * 8
	if n > 0 && (v>>(n-1))&1 == 1 {
		v |= ^uint64(0) << n
	}
	return int64(v), nil
}

func signedMinOctets(v int64) int {
	if v >= 0 {
		b := bits.Len64(uint64(v)) + 1 // +1 for sign bit
		if b <= 8 {
			return 1
		}
		return (b + 7) / 8
	}
	// negative
	u := uint64(^v)
	b := bits.Len64(u) + 1
	if b <= 8 {
		return 1
	}
	return (b + 7) / 8
}

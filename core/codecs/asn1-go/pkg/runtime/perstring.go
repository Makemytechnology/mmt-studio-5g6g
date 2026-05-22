// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import "fmt"

// Higher-level primitives: BIT STRING, OCTET STRING, and known-multiplier
// character strings (PrintableString/IA5String/VisibleString).

// PutBitString encodes a BIT STRING subject to size constraint [lb..ub]. If
// sizeExt is true, a 1-bit extension marker precedes the length when the size
// is outside the root range. For the in-range case, ubKnown=true and the length
// is encoded as a constrained whole.
func (p *PerBitData) PutBitString(bs BitString, sizeExt bool, lb, ub uint64, ubKnown bool) error {
	// Validate size constraint before encoding
	if ubKnown && !sizeExt && (bs.BitLength < lb || bs.BitLength > ub) {
		return fmt.Errorf("BIT STRING length %d bits violates constraint SIZE(%d..%d)", bs.BitLength, lb, ub)
	}
	if sizeExt {
		inExt := ubKnown && (bs.BitLength < lb || bs.BitLength > ub)
		extBit := uint64(0)
		if inExt {
			extBit = 1
		}
		if err := p.PutBits(extBit, 1); err != nil {
			return err
		}
		if inExt {
			ubKnown = false
		}
	}
	if err := p.PutLengthDeterminant(bs.BitLength, lb, ub, ubKnown); err != nil {
		return err
	}
	if p.Aligned && bs.BitLength > 16 {
		if err := p.AlignToByte(); err != nil {
			return err
		}
	}
	// write bit-by-bit (safe, but slow). Optimise later with bulk byte copy when aligned.
	for i := uint64(0); i < bs.BitLength; i++ {
		byteOff := i / 8
		bitOff := uint(7 - (i % 8))
		b := uint64(0)
		if int(byteOff) < len(bs.Bytes) {
			b = uint64((bs.Bytes[byteOff] >> bitOff) & 1)
		}
		if err := p.PutBits(b, 1); err != nil {
			return err
		}
	}
	return nil
}

func (p *PerBitData) GetBitString(sizeExt bool, lb, ub uint64, ubKnown bool) (BitString, error) {
	if sizeExt {
		ext, err := p.GetBits(1)
		if err != nil {
			return BitString{}, err
		}
		if ext == 1 {
			ubKnown = false
		}
	}
	length, err := p.GetLengthDeterminant(lb, ub, ubKnown)
	if err != nil {
		return BitString{}, err
	}
	if p.Aligned && length > 16 {
		if err := p.AlignReadToByte(); err != nil {
			return BitString{}, err
		}
	}
	bs := BitString{BitLength: length}
	bs.Bytes = make([]byte, (length+7)/8)
	for i := uint64(0); i < length; i++ {
		b, err := p.GetBits(1)
		if err != nil {
			return BitString{}, err
		}
		if b == 1 {
			bs.Bytes[i/8] |= 1 << uint(7-(i%8))
		}
	}
	return bs, nil
}

func (p *PerBitData) PutOctetString(data []byte, sizeExt bool, lb, ub uint64, ubKnown bool) error {
	length := uint64(len(data))
	// Validate size constraint before encoding
	if ubKnown && !sizeExt && (length < lb || length > ub) {
		return fmt.Errorf("OCTET STRING length %d bytes violates constraint SIZE(%d..%d)", length, lb, ub)
	}
	if sizeExt {
		inExt := ubKnown && (length < lb || length > ub)
		b := uint64(0)
		if inExt {
			b = 1
			ubKnown = false
		}
		if err := p.PutBits(b, 1); err != nil {
			return err
		}
	}
	if err := p.PutLengthDeterminant(length, lb, ub, ubKnown); err != nil {
		return err
	}
	if length == 0 {
		return nil
	}
	// Alignment rules: for APER, a fixed-size ≤2 is encoded as bit-field; otherwise align.
	if p.Aligned {
		if !(ubKnown && lb == ub && length <= 2) {
			if err := p.AlignToByte(); err != nil {
				return err
			}
		}
	}
	for _, b := range data {
		if err := p.PutBits(uint64(b), 8); err != nil {
			return err
		}
	}
	return nil
}

func (p *PerBitData) GetOctetString(sizeExt bool, lb, ub uint64, ubKnown bool) ([]byte, error) {
	if sizeExt {
		ext, err := p.GetBits(1)
		if err != nil {
			return nil, err
		}
		if ext == 1 {
			ubKnown = false
		}
	}
	length, err := p.GetLengthDeterminant(lb, ub, ubKnown)
	if err != nil {
		return nil, err
	}
	if length == 0 {
		return []byte{}, nil
	}
	if p.Aligned {
		if !(ubKnown && lb == ub && length <= 2) {
			if err := p.AlignReadToByte(); err != nil {
				return nil, err
			}
		}
	}
	out := make([]byte, length)
	for i := range out {
		v, err := p.GetBits(8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

// --- Known-multiplier character strings (X.691 §27) ---

// PutKMString encodes a known-multiplier character string per X.691 §27.
// `bitsPerChar` is the alphabet-derived bit width (e.g. 7 for PrintableString).
// In APER, when the total bit count exceeds 16, the encoder aligns to a byte
// boundary AND switches to the next-power-of-2 width (b2), which for a 7-bit
// alphabet is 8.
func (p *PerBitData) PutKMString(s string, bitsPerChar uint, sizeExt bool, lb, ub uint64, ubKnown bool) error {
	data := []byte(s)
	length := uint64(len(data))
	if sizeExt {
		inExt := ubKnown && (length < lb || length > ub)
		b := uint64(0)
		if inExt {
			b = 1
			ubKnown = false
		}
		if err := p.PutBits(b, 1); err != nil {
			return err
		}
	}
	if err := p.PutLengthDeterminant(length, lb, ub, ubKnown); err != nil {
		return err
	}
	if length == 0 {
		return nil
	}
	effective := bitsPerChar
	if p.Aligned && length*uint64(bitsPerChar) > 16 {
		if err := p.AlignToByte(); err != nil {
			return err
		}
		effective = nextPow2Bits(bitsPerChar)
	}
	for _, c := range data {
		if err := p.PutBits(uint64(c), effective); err != nil {
			return err
		}
	}
	return nil
}

func (p *PerBitData) GetKMString(bitsPerChar uint, sizeExt bool, lb, ub uint64, ubKnown bool) (string, error) {
	if sizeExt {
		ext, err := p.GetBits(1)
		if err != nil {
			return "", err
		}
		if ext == 1 {
			ubKnown = false
		}
	}
	length, err := p.GetLengthDeterminant(lb, ub, ubKnown)
	if err != nil {
		return "", err
	}
	if length == 0 {
		return "", nil
	}
	effective := bitsPerChar
	if p.Aligned && length*uint64(bitsPerChar) > 16 {
		if err := p.AlignReadToByte(); err != nil {
			return "", err
		}
		effective = nextPow2Bits(bitsPerChar)
	}
	out := make([]byte, length)
	for i := range out {
		v, err := p.GetBits(effective)
		if err != nil {
			return "", err
		}
		out[i] = byte(v)
	}
	return string(out), nil
}

func nextPow2Bits(n uint) uint {
	switch {
	case n <= 1:
		return 1
	case n <= 2:
		return 2
	case n <= 4:
		return 4
	case n <= 8:
		return 8
	case n <= 16:
		return 16
	}
	return 32
}

// --- Open type (X.691 §10.2) ---

// PutOpenType encodes `inner` as an open-type wrapper: length-determinant octets
// followed by octet-aligned inner encoding.
func (p *PerBitData) PutOpenType(inner []byte) error {
	if err := p.PutLengthDeterminant(uint64(len(inner)), 0, 0, false); err != nil {
		return err
	}
	if p.Aligned {
		if err := p.AlignToByte(); err != nil {
			return err
		}
	}
	for _, b := range inner {
		if err := p.PutBits(uint64(b), 8); err != nil {
			return err
		}
	}
	return nil
}

func (p *PerBitData) GetOpenType() ([]byte, error) {
	length, err := p.GetLengthDeterminant(0, 0, false)
	if err != nil {
		return nil, err
	}
	if p.Aligned {
		if err := p.AlignReadToByte(); err != nil {
			return nil, err
		}
	}
	out := make([]byte, length)
	for i := range out {
		v, err := p.GetBits(8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

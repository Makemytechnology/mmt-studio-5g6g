// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import (
	"fmt"
	"net"
)

// EncodeTBCD packs a digit string into TBCD bytes per TS 29.274 v19.6.0
// §8.3 (verbatim from /tmp/ts29274.txt line 17799-17801):
//
//	"...encoded as TBCD digits, i.e. digits from 0 through 9 are
//	 encoded '0000' to '1001'. When there is an odd number of digits,
//	 bits 8 to 5 of the last octet are encoded with the filler '1111'."
//
// Each octet packs two digits: low nibble = digit N, high nibble =
// digit N+1. Non-digit characters return an error so callers see
// bad input rather than silently producing garbage.
//
// Used by generated flag_conditional IEs whose YAML declares a
// sub-field as `type: tbcd_digits`.
func EncodeTBCD(digits string) ([]byte, error) {
	out := make([]byte, 0, (len(digits)+1)/2)
	var pending byte
	half := false
	for _, c := range digits {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("EncodeTBCD: non-digit %q", c)
		}
		nib := byte(c - '0')
		if !half {
			pending = nib
			half = true
		} else {
			out = append(out, (nib<<4)|pending)
			half = false
		}
	}
	if half {
		out = append(out, 0xF0|pending) // filler high nibble
	}
	return out, nil
}

// DecodeTBCD unpacks each octet's two digits in (low, high) order and
// stops at a 0xF nibble (filler).
func DecodeTBCD(b []byte) string {
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		lo := x & 0x0F
		hi := x >> 4
		if lo == 0xF {
			break
		}
		out = append(out, '0'+lo)
		if hi == 0xF {
			break
		}
		out = append(out, '0'+hi)
	}
	return string(out)
}

// IPv4/IPv6 helpers shared by F-TEID, F-SEID, UE IP Address, Node ID, etc.

// EncodeIP4or6 appends the appropriate IP bytes:
//   - if v4 set: 4 bytes
//   - if v6 set: 16 bytes
//   - both: v4 followed by v6.
// Returns the concatenation in spec order (IPv4 first per TS 29.244).
func EncodeIP4or6(v4, v6 net.IP) []byte {
	out := []byte{}
	if v4 != nil {
		out = append(out, v4.To4()...)
	}
	if v6 != nil {
		out = append(out, v6.To16()...)
	}
	return out
}

// FSEID (TS 29.244 §8.2.37).
// First byte flags: 0b0000_00V6_V4. Followed by 64-bit SEID, optional IPv4, optional IPv6.
type FSEID struct {
	SEID uint64
	IPv4 net.IP // nil if absent
	IPv6 net.IP // nil if absent
}

func (f *FSEID) Encode() []byte {
	var flags byte
	if f.IPv4 != nil {
		flags |= 0x02
	}
	if f.IPv6 != nil {
		flags |= 0x01
	}
	out := []byte{flags}
	var seidBytes [8]byte
	for i := 0; i < 8; i++ {
		seidBytes[i] = byte(f.SEID >> ((7 - i) * 8))
	}
	out = append(out, seidBytes[:]...)
	if f.IPv4 != nil {
		out = append(out, f.IPv4.To4()...)
	}
	if f.IPv6 != nil {
		out = append(out, f.IPv6.To16()...)
	}
	return out
}

func (f *FSEID) Decode(v []byte) error {
	if len(v) < 9 {
		return ErrBufferTooShort
	}
	flags := v[0]
	f.SEID = 0
	for i := 0; i < 8; i++ {
		f.SEID = (f.SEID << 8) | uint64(v[1+i])
	}
	off := 9
	if flags&0x02 != 0 {
		if len(v) < off+4 {
			return ErrBufferTooShort
		}
		f.IPv4 = net.IP(append([]byte(nil), v[off:off+4]...))
		off += 4
	}
	if flags&0x01 != 0 {
		if len(v) < off+16 {
			return ErrBufferTooShort
		}
		f.IPv6 = net.IP(append([]byte(nil), v[off:off+16]...))
	}
	return nil
}

// NodeID (TS 29.244 §8.2.38).
// First byte: type (0=IPv4, 1=IPv6, 2=FQDN) in low 4 bits.
// Value: 4/16 bytes for IP, or FQDN-encoded bytes.
type NodeID struct {
	Type uint8 // 0=IPv4, 1=IPv6, 2=FQDN
	IPv4 net.IP
	IPv6 net.IP
	FQDN string
}

func (n *NodeID) Encode() []byte {
	out := []byte{n.Type & 0x0F}
	switch n.Type {
	case 0:
		if n.IPv4 != nil {
			out = append(out, n.IPv4.To4()...)
		}
	case 1:
		if n.IPv6 != nil {
			out = append(out, n.IPv6.To16()...)
		}
	case 2:
		// FQDN encoded as length-prefixed labels (DNS wire format)
		out = append(out, encodeFQDN(n.FQDN)...)
	}
	return out
}

func (n *NodeID) Decode(v []byte) error {
	if len(v) < 1 {
		return ErrBufferTooShort
	}
	n.Type = v[0] & 0x0F
	switch n.Type {
	case 0:
		if len(v) < 5 {
			return ErrBufferTooShort
		}
		n.IPv4 = net.IP(append([]byte(nil), v[1:5]...))
	case 1:
		if len(v) < 17 {
			return ErrBufferTooShort
		}
		n.IPv6 = net.IP(append([]byte(nil), v[1:17]...))
	case 2:
		n.FQDN = decodeFQDN(v[1:])
	default:
		return fmt.Errorf("pfcp: unknown node id type %d", n.Type)
	}
	return nil
}

func encodeFQDN(name string) []byte {
	// Simple single-label encoding: each label preceded by its length byte.
	// For generator round-trips we use single-label (the spec allows DNS-style).
	out := make([]byte, 0, len(name)+2)
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			labelLen := i - start
			if labelLen > 0 {
				out = append(out, byte(labelLen))
				out = append(out, []byte(name[start:i])...)
			}
			start = i + 1
		}
	}
	return out
}

func decodeFQDN(b []byte) string {
	var s []byte
	i := 0
	for i < len(b) {
		n := int(b[i])
		i++
		if n == 0 || i+n > len(b) {
			break
		}
		if len(s) > 0 {
			s = append(s, '.')
		}
		s = append(s, b[i:i+n]...)
		i += n
	}
	return string(s)
}

// FTEID (TS 29.244 §8.2.3).
// Flags: CHID(4) CH(3) V6(2) V4(1). TEID present only if CH=0.
type FTEID struct {
	TEID uint32 // ignored when CH=1
	IPv4 net.IP
	IPv6 net.IP
	CH   bool // CHOOSE flag
	CHID bool // CHOOSE ID flag
	ID   uint8
}

func (f *FTEID) Encode() []byte {
	var flags byte
	if f.IPv4 != nil {
		flags |= 0x01
	}
	if f.IPv6 != nil {
		flags |= 0x02
	}
	if f.CH {
		flags |= 0x04
	}
	if f.CHID {
		flags |= 0x08
	}
	out := []byte{flags}
	if !f.CH {
		var b [4]byte
		b[0] = byte(f.TEID >> 24)
		b[1] = byte(f.TEID >> 16)
		b[2] = byte(f.TEID >> 8)
		b[3] = byte(f.TEID)
		out = append(out, b[:]...)
		if f.IPv4 != nil {
			out = append(out, f.IPv4.To4()...)
		}
		if f.IPv6 != nil {
			out = append(out, f.IPv6.To16()...)
		}
	} else {
		// CH=1: IP fields may be absent; if CHID, append ID byte.
		if f.CHID {
			out = append(out, f.ID)
		}
	}
	return out
}

func (f *FTEID) Decode(v []byte) error {
	if len(v) < 1 {
		return ErrBufferTooShort
	}
	flags := v[0]
	f.CH = flags&0x04 != 0
	f.CHID = flags&0x08 != 0
	off := 1
	if !f.CH {
		if len(v) < off+4 {
			return ErrBufferTooShort
		}
		f.TEID = uint32(v[off])<<24 | uint32(v[off+1])<<16 | uint32(v[off+2])<<8 | uint32(v[off+3])
		off += 4
		if flags&0x01 != 0 {
			if len(v) < off+4 {
				return ErrBufferTooShort
			}
			f.IPv4 = net.IP(append([]byte(nil), v[off:off+4]...))
			off += 4
		}
		if flags&0x02 != 0 {
			if len(v) < off+16 {
				return ErrBufferTooShort
			}
			f.IPv6 = net.IP(append([]byte(nil), v[off:off+16]...))
		}
	} else if f.CHID {
		if len(v) < off+1 {
			return ErrBufferTooShort
		}
		f.ID = v[off]
	}
	return nil
}

// UEIPAddress (TS 29.244 v19.5.0 §8.2.62).
//
// Wire layout per Figure 8.2.62-1:
//
//	Octet 5         flags  Spare(8) | IP6PL(7) | CHV6(6) | CHV4(5) |
//	                       IPv6D(4) | S/D(3) | V4(2) | V6(1)
//	(m..m+3)        IPv4 address                   ← when V4=1
//	(p..p+15)       IPv6 address                   ← when V6=1
//	(r)             IPv6 Prefix Delegation Bits    ← when IPv6D=1
//	(s)             IPv6 Prefix Length             ← when IP6PL=1
//
// SourceOrDestination encodes the §8.2.62 S/D bit (applicable only
// inside PDI / Create-TE / Update-TE IEs; ignored elsewhere):
// false=Source IP, true=Destination IP.
//
// Hand-coded in the runtime alongside FTEID / FSEID / NodeID because
// the §8.2.62 flag interactions (V4 vs CHV4, IPv6D vs IP6PL) are too
// complex for the current YAML flag_conditional schema. Layout is
// stable per the spec.
type UEIPAddress struct {
	IPv4                     net.IP // nil when V4=0
	IPv6                     net.IP // nil when V6=0
	SourceOrDestination      bool   // bit 3 — true=Destination
	IPv6PrefixDelegationBits *uint8 // nil when IPv6D=0
	IPv6PrefixLength         *uint8 // nil when IP6PL=0
	ChooseV4                 bool   // bit 5 — CP requests UPF allocation
	ChooseV6                 bool   // bit 6
}

func (u *UEIPAddress) Encode() []byte {
	var flags byte
	if u.IPv6 != nil {
		flags |= 0x01
	}
	if u.IPv4 != nil {
		flags |= 0x02
	}
	if u.SourceOrDestination {
		flags |= 0x04
	}
	if u.IPv6PrefixDelegationBits != nil {
		flags |= 0x08
	}
	if u.ChooseV4 {
		flags |= 0x10
	}
	if u.ChooseV6 {
		flags |= 0x20
	}
	if u.IPv6PrefixLength != nil {
		flags |= 0x40
	}
	out := []byte{flags}
	if u.IPv4 != nil {
		out = append(out, u.IPv4.To4()...)
	}
	if u.IPv6 != nil {
		out = append(out, u.IPv6.To16()...)
	}
	if u.IPv6PrefixDelegationBits != nil {
		out = append(out, *u.IPv6PrefixDelegationBits)
	}
	if u.IPv6PrefixLength != nil {
		out = append(out, *u.IPv6PrefixLength)
	}
	return out
}

func (u *UEIPAddress) Decode(v []byte) error {
	if len(v) < 1 {
		return ErrBufferTooShort
	}
	flags := v[0]
	off := 1
	u.SourceOrDestination = flags&0x04 != 0
	u.ChooseV4 = flags&0x10 != 0
	u.ChooseV6 = flags&0x20 != 0
	if flags&0x02 != 0 {
		if len(v) < off+4 {
			return ErrBufferTooShort
		}
		u.IPv4 = net.IP(append([]byte(nil), v[off:off+4]...))
		off += 4
	}
	if flags&0x01 != 0 {
		if len(v) < off+16 {
			return ErrBufferTooShort
		}
		u.IPv6 = net.IP(append([]byte(nil), v[off:off+16]...))
		off += 16
	}
	if flags&0x08 != 0 {
		if len(v) < off+1 {
			return ErrBufferTooShort
		}
		b := v[off]
		u.IPv6PrefixDelegationBits = &b
		off++
	}
	if flags&0x40 != 0 {
		if len(v) < off+1 {
			return ErrBufferTooShort
		}
		b := v[off]
		u.IPv6PrefixLength = &b
		off++
	}
	return nil
}

func (u *UEIPAddress) EncodeBytes() []byte        { return u.Encode() }
func (u *UEIPAddress) DecodeBytes(v []byte) error { return u.Decode(v) }

// OuterHeaderCreation (TS 29.244 v19.5.0 §8.2.56).
//
// Wire layout per Figure 8.2.56-1 — 2-byte Description field +
// presence-driven sub-fields:
//
//	Octets 5..6     Description (16-bit bitmap, see Table 8.2.56-1)
//	(m..m+3)        TEID                         ← when GTP-U bits 5/1 or 5/2 set
//	(p..p+3)        IPv4 Address                 ← when bits 5/1, 5/3, 5/5 set
//	(q..q+15)       IPv6 Address                 ← when bits 5/2, 5/4, 5/6 set
//	(r..r+1)        Port Number                  ← when bits 5/3 or 5/4 set
//	(t..t+2)        C-TAG                        ← when bit 5/7 set
//	(u..u+2)        S-TAG                        ← when bit 5/8 set
//
// Description bit positions (octet/bit, all little-endian within
// each octet — bit 1 = LSB):
//   octet 5: 1=GTP-U/UDP/IPv4, 2=GTP-U/UDP/IPv6, 3=UDP/IPv4,
//            4=UDP/IPv6, 5=IPv4, 6=IPv6, 7=C-TAG, 8=S-TAG
//   octet 6: 1=N19 Indication, 2=N6 Indication, 3=Low Layer SSM+C-TEID
//
// Hand-coded in the runtime because the field-presence rules
// depend on combinations of Description bits — too involved for
// the generator's flag_conditional schema today. Layout is stable.
type OuterHeaderCreation struct {
	Description uint16 // raw 16-bit bitmap; helper bits below
	TEID        uint32
	IPv4        net.IP
	IPv6        net.IP
	Port        uint16
	CTAG        []byte // 3 bytes when present
	STAG        []byte // 3 bytes when present
}

// OHC Description bit constants (octet 5 low byte, octet 6 high byte
// per the spec figure).
const (
	OHCDescGTPUUDPIPv4   uint16 = 1 << 0
	OHCDescGTPUUDPIPv6   uint16 = 1 << 1
	OHCDescUDPIPv4       uint16 = 1 << 2
	OHCDescUDPIPv6       uint16 = 1 << 3
	OHCDescIPv4          uint16 = 1 << 4
	OHCDescIPv6          uint16 = 1 << 5
	OHCDescCTAG          uint16 = 1 << 6
	OHCDescSTAG          uint16 = 1 << 7
	OHCDescN19Indication uint16 = 1 << 8
	OHCDescN6Indication  uint16 = 1 << 9
	OHCDescLowLayerSSM   uint16 = 1 << 10
)

func (o *OuterHeaderCreation) Encode() []byte {
	out := make([]byte, 0, 16)
	// Description: octet 5 = low byte, octet 6 = high byte (per
	// §8.2.56 figure where octet 5 holds bits 5/1..5/8 and octet 6
	// holds 6/1..6/3). The 2-byte field is little-endian on the
	// wire because bit numbering in the figure starts at octet 5.
	out = append(out, byte(o.Description), byte(o.Description>>8))
	if o.Description&(OHCDescGTPUUDPIPv4|OHCDescGTPUUDPIPv6) != 0 {
		out = append(out, byte(o.TEID>>24), byte(o.TEID>>16),
			byte(o.TEID>>8), byte(o.TEID))
	}
	if o.Description&(OHCDescGTPUUDPIPv4|OHCDescUDPIPv4|OHCDescIPv4) != 0 {
		if o.IPv4 != nil {
			out = append(out, o.IPv4.To4()...)
		}
	}
	if o.Description&(OHCDescGTPUUDPIPv6|OHCDescUDPIPv6|OHCDescIPv6) != 0 {
		if o.IPv6 != nil {
			out = append(out, o.IPv6.To16()...)
		}
	}
	if o.Description&(OHCDescUDPIPv4|OHCDescUDPIPv6) != 0 {
		out = append(out, byte(o.Port>>8), byte(o.Port))
	}
	if o.Description&OHCDescCTAG != 0 && len(o.CTAG) >= 3 {
		out = append(out, o.CTAG[:3]...)
	}
	if o.Description&OHCDescSTAG != 0 && len(o.STAG) >= 3 {
		out = append(out, o.STAG[:3]...)
	}
	return out
}

func (o *OuterHeaderCreation) Decode(v []byte) error {
	if len(v) < 2 {
		return ErrBufferTooShort
	}
	o.Description = uint16(v[0]) | uint16(v[1])<<8
	off := 2
	if o.Description&(OHCDescGTPUUDPIPv4|OHCDescGTPUUDPIPv6) != 0 {
		if len(v) < off+4 {
			return ErrBufferTooShort
		}
		o.TEID = uint32(v[off])<<24 | uint32(v[off+1])<<16 | uint32(v[off+2])<<8 | uint32(v[off+3])
		off += 4
	}
	if o.Description&(OHCDescGTPUUDPIPv4|OHCDescUDPIPv4|OHCDescIPv4) != 0 {
		if len(v) < off+4 {
			return ErrBufferTooShort
		}
		o.IPv4 = net.IP(append([]byte(nil), v[off:off+4]...))
		off += 4
	}
	if o.Description&(OHCDescGTPUUDPIPv6|OHCDescUDPIPv6|OHCDescIPv6) != 0 {
		if len(v) < off+16 {
			return ErrBufferTooShort
		}
		o.IPv6 = net.IP(append([]byte(nil), v[off:off+16]...))
		off += 16
	}
	if o.Description&(OHCDescUDPIPv4|OHCDescUDPIPv6) != 0 {
		if len(v) < off+2 {
			return ErrBufferTooShort
		}
		o.Port = uint16(v[off])<<8 | uint16(v[off+1])
		off += 2
	}
	if o.Description&OHCDescCTAG != 0 {
		if len(v) < off+3 {
			return ErrBufferTooShort
		}
		o.CTAG = append([]byte(nil), v[off:off+3]...)
		off += 3
	}
	if o.Description&OHCDescSTAG != 0 {
		if len(v) < off+3 {
			return ErrBufferTooShort
		}
		o.STAG = append([]byte(nil), v[off:off+3]...)
	}
	return nil
}

func (o *OuterHeaderCreation) EncodeBytes() []byte        { return o.Encode() }
func (o *OuterHeaderCreation) DecodeBytes(v []byte) error { return o.Decode(v) }

// MBR (TS 29.244 v19.5.0 §8.2.8) and GBR (§8.2.9) share a layout —
// 5-byte big-endian UL + 5-byte big-endian DL — and a unit (kbps,
// per §8.2.8 line 21380): "The UL/DL MBR fields shall be encoded as
// kilobits per second (1 kbps = 1000 bps) in binary value."
//
// 40-bit values (max 1 Tbps); higher kbps saturate at (1<<40)-1.
// Hand-coded here so consumers see typed UL/DL uint64 fields rather
// than 5-byte slices and a separate encodeU40BE helper.
type MBR struct {
	UL uint64 // kbps
	DL uint64 // kbps
}

// GBR has the same wire shape and unit semantics; aliasing
// keeps the runtime small.
type GBR = MBR

func (m *MBR) Encode() []byte {
	out := make([]byte, 10)
	encU40BE(out[0:5], m.UL)
	encU40BE(out[5:10], m.DL)
	return out
}

func (m *MBR) Decode(v []byte) error {
	if len(v) < 10 {
		return ErrBufferTooShort
	}
	m.UL = decU40BE(v[0:5])
	m.DL = decU40BE(v[5:10])
	return nil
}

func (m *MBR) EncodeBytes() []byte        { return m.Encode() }
func (m *MBR) DecodeBytes(v []byte) error { return m.Decode(v) }

// 40-bit big-endian unsigned helpers — common across §8.2.8 / §8.2.9 /
// §8.2.13 Subsequent {UL,DL} Volume.
func encU40BE(out []byte, v uint64) {
	if v > (1<<40)-1 {
		v = (1 << 40) - 1
	}
	out[0] = byte(v >> 32)
	out[1] = byte(v >> 24)
	out[2] = byte(v >> 16)
	out[3] = byte(v >> 8)
	out[4] = byte(v)
}

func decU40BE(v []byte) uint64 {
	return uint64(v[0])<<32 | uint64(v[1])<<24 |
		uint64(v[2])<<16 | uint64(v[3])<<8 | uint64(v[4])
}

// APNDNN (TS 29.244 v19.5.0 §8.2.117).
//
// Carries an APN / Data Network Name from CP to UP function. The
// encoding follows TS 23.003 §9.1 — dotted labels with each label
// length-prefixed on the wire. Same shape as the NAS DNN IE
// (TS 24.501 §9.11.2.1B).
//
// §8.2.117 NOTE explicitly says: "The APN/DNN field is not encoded
// as a dotted string as commonly used in documentation." — i.e. the
// wire bytes are the length-prefixed labels, not "ims.apn1.foo".
// The runtime exposes the human-readable Value string and handles
// the wire conversion.
type APNDNN struct {
	Value string
}

func (a *APNDNN) Encode() []byte {
	if a.Value == "" {
		return nil
	}
	out := make([]byte, 0, len(a.Value)+8)
	start := 0
	for i := 0; i <= len(a.Value); i++ {
		if i == len(a.Value) || a.Value[i] == '.' {
			labelLen := i - start
			if labelLen > 0 {
				out = append(out, byte(labelLen))
				out = append(out, a.Value[start:i]...)
			}
			start = i + 1
		}
	}
	return out
}

func (a *APNDNN) Decode(v []byte) error {
	if len(v) == 0 {
		a.Value = ""
		return nil
	}
	var parts []string
	for i := 0; i < len(v); {
		ln := int(v[i])
		i++
		if ln == 0 || i+ln > len(v) {
			return ErrInvalidLength
		}
		parts = append(parts, string(v[i:i+ln]))
		i += ln
	}
	if len(parts) == 0 {
		a.Value = ""
		return nil
	}
	a.Value = parts[0]
	for _, p := range parts[1:] {
		a.Value += "." + p
	}
	return nil
}

func (a *APNDNN) EncodeBytes() []byte        { return a.Encode() }
func (a *APNDNN) DecodeBytes(v []byte) error { return a.Decode(v) }

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ikev2

import (
	"encoding/binary"
	"fmt"
)

// ---- §3.4 Key Exchange (KE) ----

// KE — RFC 7296 §3.4 Key Exchange payload body. The 4-octet
// (DH Group | RESERVED) header precedes the raw DH public value.
//
// "The length of the Diffie-Hellman public value for MODP groups
// MUST be equal to the length of the prime modulus over which the
// exponentiation was performed, prepending zero bits to the value
// if necessary." — §3.4
type KE struct {
	DHGroup uint16
	Public  []byte
}

func (k *KE) Marshal() []byte {
	buf := make([]byte, 4+len(k.Public))
	binary.BigEndian.PutUint16(buf[0:2], k.DHGroup)
	// buf[2:4] RESERVED
	copy(buf[4:], k.Public)
	return buf
}

func ParseKE(buf []byte) (*KE, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("ikev2 KE: header truncated (%d)", len(buf))
	}
	return &KE{
		DHGroup: binary.BigEndian.Uint16(buf[0:2]),
		Public:  append([]byte(nil), buf[4:]...),
	}, nil
}

// ---- §3.5 Identification (IDi / IDr) ----

// IDType — RFC 7296 §3.5 (verbatim subset):
//
//	ID_IPV4_ADDR     1
//	ID_FQDN          2
//	ID_RFC822_ADDR   3
//	ID_IPV6_ADDR     5
//	ID_DER_ASN1_DN   9
//	ID_DER_ASN1_GN  10
//	ID_KEY_ID       11
//
// TS 24.502 §7.3.2.1 mandates the UE use ID_KEY_ID with a random
// value in the IDi of the initial IKE_AUTH request.
type IDType uint8

const (
	IDTypeIPv4Addr   IDType = 1
	IDTypeFQDN       IDType = 2
	IDTypeRFC822Addr IDType = 3
	IDTypeIPv6Addr   IDType = 5
	IDTypeDERASN1DN  IDType = 9
	IDTypeDERASN1GN  IDType = 10
	IDTypeKeyID      IDType = 11
)

// ID — RFC 7296 §3.5. Same body for IDi (PayloadIDi) and IDr
// (PayloadIDr); only the surrounding payload type differs.
type ID struct {
	Type IDType
	Data []byte
}

func (i *ID) Marshal() []byte {
	buf := make([]byte, 4+len(i.Data))
	buf[0] = byte(i.Type)
	// buf[1:4] RESERVED
	copy(buf[4:], i.Data)
	return buf
}

func ParseID(buf []byte) (*ID, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("ikev2 ID: header truncated (%d)", len(buf))
	}
	return &ID{
		Type: IDType(buf[0]),
		Data: append([]byte(nil), buf[4:]...),
	}, nil
}

// ---- §3.8 Authentication ----

// AuthMethod — RFC 7296 §3.8 (verbatim subset):
//
//	RSA Digital Signature      1
//	Shared Key Message Integrity Code  2
//	DSS Digital Signature      3
//
// EAP-based auth uses none of these — instead the IKE_AUTH messages
// contain EAP payloads (§2.16) and the AUTH payload is computed
// from the EAP-derived MSK after EAP-Success.
type AuthMethod uint8

const (
	AuthRSASignature   AuthMethod = 1
	AuthSharedKeyMIC   AuthMethod = 2
	AuthDSSSignature   AuthMethod = 3
)

// Auth — RFC 7296 §3.8 Authentication payload body.
type Auth struct {
	Method AuthMethod
	Data   []byte
}

func (a *Auth) Marshal() []byte {
	buf := make([]byte, 4+len(a.Data))
	buf[0] = byte(a.Method)
	// buf[1:4] RESERVED
	copy(buf[4:], a.Data)
	return buf
}

func ParseAuth(buf []byte) (*Auth, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("ikev2 AUTH: header truncated (%d)", len(buf))
	}
	return &Auth{
		Method: AuthMethod(buf[0]),
		Data:   append([]byte(nil), buf[4:]...),
	}, nil
}

// ---- §3.9 Nonce ----

// Nonce — RFC 7296 §3.9. "The size of the Nonce Data MUST be between
// 16 and 256 octets, inclusive." Body is just the raw value.
type Nonce []byte

func (n Nonce) Marshal() []byte { return []byte(n) }

func ParseNonce(buf []byte) (Nonce, error) {
	if len(buf) < 16 || len(buf) > 256 {
		return nil, fmt.Errorf("ikev2 Nonce: length %d outside [16, 256] (RFC 7296 §3.9)",
			len(buf))
	}
	return Nonce(append([]byte(nil), buf...)), nil
}

// ---- §3.10 Notify ----

// NotifyType — RFC 7296 §3.10.1 "Notify Message Types" (verbatim
// subset; full list in IANA "Internet Key Exchange Version 2 (IKEv2)
// Parameters"). Errors are 1-16383, status types are 16384+.
type NotifyType uint16

const (
	NotifyUnsupportedCriticalPayload NotifyType = 1
	NotifyInvalidIKESPI              NotifyType = 4
	NotifyInvalidMajorVersion        NotifyType = 5
	NotifyInvalidSyntax              NotifyType = 7
	NotifyInvalidMessageID           NotifyType = 9
	NotifyInvalidSPI                 NotifyType = 11
	NotifyNoProposalChosen           NotifyType = 14
	NotifyInvalidKEPayload           NotifyType = 17
	NotifyAuthenticationFailed       NotifyType = 24
	NotifySingleSANotPermitted       NotifyType = 34
	NotifyInternalAddressFailure     NotifyType = 36
	NotifyFailedCPRequired           NotifyType = 37
	NotifyTSUnacceptable             NotifyType = 38
	NotifyInvalidSelectors           NotifyType = 39

	NotifyInitialContact         NotifyType = 16384
	NotifySetWindowSize          NotifyType = 16385
	NotifyAdditionalTSPossible   NotifyType = 16386
	NotifyIPCOMPSupported        NotifyType = 16387
	NotifyNATDetectionSourceIP   NotifyType = 16388
	NotifyNATDetectionDestIP     NotifyType = 16389
	NotifyCookie                 NotifyType = 16390
	NotifyUseTransportMode       NotifyType = 16391
	NotifyEAPOnlyAuthentication  NotifyType = 16417
	NotifyRekeyySA               NotifyType = 16393
)

// Notify — RFC 7296 §3.10. Layout:
//
//	Protocol ID (1) | SPI Size (1) | Notify Message Type (2)
//	[ SPI (variable) ] [ Notification Data (variable) ]
type Notify struct {
	ProtocolID ProtocolID // 0 if not protocol-specific (§3.10)
	SPI        []byte
	Type       NotifyType
	Data       []byte
}

func (n *Notify) Marshal() []byte {
	buf := make([]byte, 4+len(n.SPI)+len(n.Data))
	buf[0] = byte(n.ProtocolID)
	buf[1] = byte(len(n.SPI))
	binary.BigEndian.PutUint16(buf[2:4], uint16(n.Type))
	copy(buf[4:], n.SPI)
	copy(buf[4+len(n.SPI):], n.Data)
	return buf
}

func ParseNotify(buf []byte) (*Notify, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("ikev2 Notify: header truncated (%d)", len(buf))
	}
	n := &Notify{
		ProtocolID: ProtocolID(buf[0]),
		Type:       NotifyType(binary.BigEndian.Uint16(buf[2:4])),
	}
	spiLen := int(buf[1])
	if 4+spiLen > len(buf) {
		return nil, fmt.Errorf("ikev2 Notify: SPI len %d overruns buffer", spiLen)
	}
	if spiLen > 0 {
		n.SPI = append([]byte(nil), buf[4:4+spiLen]...)
	}
	if 4+spiLen < len(buf) {
		n.Data = append([]byte(nil), buf[4+spiLen:]...)
	}
	return n, nil
}

// ---- §3.11 Delete ----

// Delete — RFC 7296 §3.11 Delete payload body. Layout:
//
//	Protocol ID (1) | SPI Size (1) | Num of SPIs (2) | SPIs (variable)
//
// "Deletion of the IKE SA is indicated by a protocol ID of 1 (IKE)
// but no SPIs. Deletion of a Child SA, such as ESP or AH, will
// contain the IPsec protocol ID of that protocol (2 for AH, 3 for
// ESP), and the SPI is the SPI the sending endpoint would expect in
// inbound ESP or AH packets." — §3.11
type Delete struct {
	ProtocolID ProtocolID
	SPISize    uint8
	SPIs       [][]byte // each entry has length SPISize
}

func (d *Delete) Marshal() []byte {
	if len(d.SPIs) > 0xFFFF {
		panic(fmt.Sprintf("ikev2 Delete: too many SPIs (%d)", len(d.SPIs)))
	}
	out := make([]byte, 4+len(d.SPIs)*int(d.SPISize))
	out[0] = byte(d.ProtocolID)
	out[1] = d.SPISize
	binary.BigEndian.PutUint16(out[2:4], uint16(len(d.SPIs)))
	off := 4
	for _, spi := range d.SPIs {
		copy(out[off:], spi)
		off += int(d.SPISize)
	}
	return out
}

func ParseDelete(buf []byte) (*Delete, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("ikev2 Delete: header truncated (%d)", len(buf))
	}
	d := &Delete{
		ProtocolID: ProtocolID(buf[0]),
		SPISize:    buf[1],
	}
	num := int(binary.BigEndian.Uint16(buf[2:4]))
	// §3.11: "It MUST be zero for IKE (SPI is in message header) or
	// four for AH and ESP."
	switch d.ProtocolID {
	case ProtocolIKE:
		if d.SPISize != 0 {
			return nil, fmt.Errorf("ikev2 Delete: IKE protocol with non-zero SPI Size %d (RFC 7296 §3.11)",
				d.SPISize)
		}
		if num != 0 {
			return nil, fmt.Errorf("ikev2 Delete: IKE protocol with %d SPIs, want 0 (RFC 7296 §3.11)", num)
		}
		return d, nil
	case ProtocolAH, ProtocolESP:
		if d.SPISize != 4 {
			return nil, fmt.Errorf("ikev2 Delete: AH/ESP SPI Size %d, want 4 (RFC 7296 §3.11)",
				d.SPISize)
		}
	default:
		return nil, fmt.Errorf("ikev2 Delete: unknown Protocol ID %d", d.ProtocolID)
	}
	want := 4 + num*int(d.SPISize)
	if want > len(buf) {
		return nil, fmt.Errorf("ikev2 Delete: %d SPIs * %d-byte size overrun buffer (%d)",
			num, d.SPISize, len(buf))
	}
	off := 4
	for i := 0; i < num; i++ {
		d.SPIs = append(d.SPIs, append([]byte(nil), buf[off:off+int(d.SPISize)]...))
		off += int(d.SPISize)
	}
	return d, nil
}

// ---- §3.16 EAP ----

// EAP — RFC 7296 §3.16 EAP payload. The body is a complete EAP
// packet (RFC 3748 §4): Code (1) | Identifier (1) | Length (2) |
// Data. We don't crack open the EAP packet here — that's eap5g's
// job for EAP-Type=254 (Expanded) with the 5G vendor.
type EAP []byte

func (e EAP) Marshal() []byte { return []byte(e) }

func ParseEAP(buf []byte) (EAP, error) {
	if len(buf) < 4 {
		return nil, fmt.Errorf("ikev2 EAP: shorter than RFC 3748 §4 header (%d)", len(buf))
	}
	declared := int(binary.BigEndian.Uint16(buf[2:4]))
	if declared != len(buf) {
		return nil, fmt.Errorf("ikev2 EAP: declared length %d != payload body %d",
			declared, len(buf))
	}
	return EAP(append([]byte(nil), buf...)), nil
}

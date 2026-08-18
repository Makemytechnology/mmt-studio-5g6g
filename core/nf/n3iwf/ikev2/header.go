// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ikev2

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Header is RFC 7296 §3.1 IKE Header. Field offsets, verbatim from
// the wire diagram (Figure 4):
//
//	0..7    Initiator's SPI (8 octets, MUST NOT be zero)
//	8..15   Responder's SPI (8 octets, zero in the first message of
//	        an initial exchange)
//	16      Next Payload (1 octet — see PayloadType in constants.go)
//	17      MjVer (4 bits, MUST be 2) | MnVer (4 bits, MUST be 0)
//	18      Exchange Type (1 octet — see ExchangeType)
//	19      Flags (1 octet — see Flag* in constants.go)
//	20..23  Message ID (4 octets, big endian)
//	24..27  Length (4 octets, big endian — total message length
//	        including header)
type Header struct {
	SPIi         [8]byte
	SPIr         [8]byte
	NextPayload  PayloadType
	Version      uint8 // 0x20 for IKEv2 per §3.1; surfaced raw so the
	                   // decoder can validate / reject INVALID_MAJOR_VERSION
	ExchangeType ExchangeType
	Flags        uint8
	MessageID    uint32
	Length       uint32
}

// IsInitiator returns true iff the I (Initiator) flag is set.
// RFC 7296 §3.1: "MUST be set in messages sent by the original
// initiator of the IKE SA and MUST be cleared in messages sent by
// the original responder."
func (h *Header) IsInitiator() bool { return h.Flags&FlagInitiator != 0 }

// IsResponse returns true iff the R (Response) flag is set. RFC 7296
// §3.1: "MUST be cleared in all request messages and MUST be set in
// all responses."
func (h *Header) IsResponse() bool { return h.Flags&FlagResponse != 0 }

// ParseHeader decodes the 28-octet IKE header at the start of buf.
// Returns an error if buf is too short. Per RFC 7296 §3.1 the
// Initiator's SPI MUST NOT be zero — that is enforced here so the
// caller never has to second-guess a peer that sends junk.
func ParseHeader(buf []byte) (*Header, error) {
	if len(buf) < HeaderLen {
		return nil, fmt.Errorf("ikev2: header truncated (%d < %d)",
			len(buf), HeaderLen)
	}
	h := &Header{
		NextPayload:  PayloadType(buf[16]),
		Version:      buf[17],
		ExchangeType: ExchangeType(buf[18]),
		Flags:        buf[19],
		MessageID:    binary.BigEndian.Uint32(buf[20:24]),
		Length:       binary.BigEndian.Uint32(buf[24:28]),
	}
	copy(h.SPIi[:], buf[0:8])
	copy(h.SPIr[:], buf[8:16])
	if isZero(h.SPIi[:]) {
		// §3.1 "Initiator's SPI ... value MUST NOT be zero".
		return nil, errors.New("ikev2: initiator SPI is zero (RFC 7296 §3.1)")
	}
	return h, nil
}

// MarshalHeader encodes h into a 28-byte slice. The Length field is
// taken verbatim from h.Length — caller is responsible for setting
// it to (HeaderLen + total payloads length) per §3.1.
func MarshalHeader(h *Header) []byte {
	buf := make([]byte, HeaderLen)
	copy(buf[0:8], h.SPIi[:])
	copy(buf[8:16], h.SPIr[:])
	buf[16] = byte(h.NextPayload)
	if h.Version == 0 {
		buf[17] = VersionByte // §3.1 default to IKEv2 (MjVer=2, MnVer=0)
	} else {
		buf[17] = h.Version
	}
	buf[18] = byte(h.ExchangeType)
	buf[19] = h.Flags
	binary.BigEndian.PutUint32(buf[20:24], h.MessageID)
	binary.BigEndian.PutUint32(buf[24:28], h.Length)
	return buf
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

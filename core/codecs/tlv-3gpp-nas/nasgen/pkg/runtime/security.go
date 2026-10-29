// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

// SecurityHeaderType per TS 24.501 §9.3.
type SecurityHeaderType uint8

const (
	SecurityHeaderTypePlain                         SecurityHeaderType = 0x00
	SecurityHeaderTypeIntegrityProtected            SecurityHeaderType = 0x01
	SecurityHeaderTypeIntegrityProtectedCiphered    SecurityHeaderType = 0x02
	SecurityHeaderTypeIntegrityProtectedNewContext  SecurityHeaderType = 0x03
	SecurityHeaderTypeIntegrityProtectedCipheredNew SecurityHeaderType = 0x04
)

// Extended Protocol Discriminator values (TS 24.501 §9.2).
const (
	EPD5GMM uint8 = 0x7E
	EPD5GSM uint8 = 0x2E
)

// Legacy Protocol Discriminator values (TS 24.007 §11.2.3.1.1) — low nibble of
// octet 1 in LTE NAS PDUs. High nibble is SHT (EMM) or EBI (ESM).
const (
	PDEMM uint8 = 0x07
	PDESM uint8 = 0x02
)

// NASSecurityHeader — security-protected NAS wrapper (TS 24.501 §9.1.1).
type NASSecurityHeader struct {
	EPD                uint8
	SecurityHeaderType SecurityHeaderType
	MAC                [4]byte
	SequenceNumber     uint8
	PlainNASMessage    []byte // still ciphered if SHT==2 or 4
}

// ParseSecurityHeader parses the 7-octet header + payload.
// Returns a header with PlainNASMessage set to the remaining bytes.
// It does NOT decrypt; ciphertext is returned as-is in PlainNASMessage.
func ParseSecurityHeader(data []byte) (*NASSecurityHeader, error) {
	if len(data) < 7 {
		return nil, ErrBufferTooShort
	}
	h := &NASSecurityHeader{
		EPD:                data[0],
		SecurityHeaderType: SecurityHeaderType(data[1] & 0x0F),
		SequenceNumber:     data[6],
	}
	copy(h.MAC[:], data[2:6])
	h.PlainNASMessage = append([]byte(nil), data[7:]...)
	return h, nil
}

func (h *NASSecurityHeader) Encode() []byte {
	out := make([]byte, 7+len(h.PlainNASMessage))
	out[0] = h.EPD
	out[1] = byte(h.SecurityHeaderType) & 0x0F
	copy(out[2:6], h.MAC[:])
	out[6] = h.SequenceNumber
	copy(out[7:], h.PlainNASMessage)
	return out
}

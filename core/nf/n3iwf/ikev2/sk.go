// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ikev2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
)

// SK is the §3.14 "Encrypted and Authenticated" payload — it wraps
// other payloads in an authenticated-encryption envelope. Per §3.14:
//
//	"The Encrypted payload, if present in a message, MUST be the last
//	 payload in the message. Often, it is the only payload in the
//	 message."
//
// Wire layout (Figure 21, verbatim):
//
//	Generic Payload Header (4)
//	Initialization Vector (block_size for CBC)
//	Encrypted IKE Payloads
//	Padding (0..255 octets)
//	Pad Length (1)
//	Integrity Checksum Data (ICV)
//
// Plaintext = inner_payloads || Padding || Pad Length, where the
// total is a multiple of the cipher block size. The ICV is
// "computed over the encrypted message" (§3.14) — i.e. over
// IKE Header || preceding payload bytes || SK generic header ||
// IV || ciphertext (everything except the ICV itself).

// CipherSuite holds the negotiated cipher + integrity algorithms
// and per-direction keys. Per RFC 7296 §2.14, send-direction uses
// SK_ei + SK_ai (or SK_er + SK_ar) — whichever side is the
// "initiator" of the IKE SA. The caller wires the right pair in.
type CipherSuite struct {
	// EncrID is the negotiated §3.3.2 ENCR transform ID, e.g.
	// ENCR_AES_CBC. Only ENCR_AES_CBC is implemented here — the
	// authenticated-encryption (AEAD / AES-GCM) format described
	// in §3.14 ("see [AEAD]") is out of scope for this phase.
	EncrID    uint16
	EncrKey   []byte // SK_e{i,r}; len ∈ {16, 24, 32} for AES
	BlockSize int    // cipher block size in octets

	// IntegID is the negotiated §3.3.2 INTEG transform ID, e.g.
	// INTEG_HMAC_SHA256_128. Only HMAC-SHA1-96 and HMAC-SHA256-128
	// are implemented here.
	IntegID  uint16
	IntegKey []byte // SK_a{i,r}; len = full hash output (RFC 4868)
	ICVLen   int    // ICV truncation length in octets
}

// NewAESCBC_HMACSHA256_128 returns the operator-mandated minimum
// suite (matching the proposal IKEDefaultProposal builds): AES-CBC
// for ENCR + HMAC-SHA-256-128 for INTEG. encKey length picks the
// AES variant: 16 ⇒ AES-128, 24 ⇒ AES-192, 32 ⇒ AES-256.
func NewAESCBC_HMACSHA256_128(encKey, integKey []byte) (*CipherSuite, error) {
	switch len(encKey) {
	case 16, 24, 32:
	default:
		return nil, fmt.Errorf("ikev2 SK: AES key length %d not in {16,24,32}", len(encKey))
	}
	// RFC 4868 §2.1: SHA-256 HMAC key length is 32 octets.
	if len(integKey) != sha256.Size {
		return nil, fmt.Errorf("ikev2 SK: HMAC-SHA256 key length %d != %d (RFC 4868 §2.1)",
			len(integKey), sha256.Size)
	}
	return &CipherSuite{
		EncrID: ENCR_AES_CBC, EncrKey: encKey, BlockSize: aes.BlockSize,
		IntegID: INTEG_HMAC_SHA256_128, IntegKey: integKey, ICVLen: 16,
	}, nil
}

// NewAESCBC_HMACSHA1_96 returns a fallback suite for older peers:
// AES-CBC + HMAC-SHA1-96 (RFC 7296 §3.3.2 default INTEG).
func NewAESCBC_HMACSHA1_96(encKey, integKey []byte) (*CipherSuite, error) {
	switch len(encKey) {
	case 16, 24, 32:
	default:
		return nil, fmt.Errorf("ikev2 SK: AES key length %d not in {16,24,32}", len(encKey))
	}
	if len(integKey) != sha1.Size {
		return nil, fmt.Errorf("ikev2 SK: HMAC-SHA1 key length %d != %d", len(integKey), sha1.Size)
	}
	return &CipherSuite{
		EncrID: ENCR_AES_CBC, EncrKey: encKey, BlockSize: aes.BlockSize,
		IntegID: INTEG_HMAC_SHA1_96, IntegKey: integKey, ICVLen: 12,
	}, nil
}

// integHash returns a fresh hash.Hash for the negotiated INTEG.
func (c *CipherSuite) integHash() hash.Hash {
	switch c.IntegID {
	case INTEG_HMAC_SHA256_128:
		return hmac.New(sha256.New, c.IntegKey)
	case INTEG_HMAC_SHA1_96:
		return hmac.New(sha1.New, c.IntegKey)
	}
	panic(fmt.Sprintf("ikev2 SK: unsupported INTEG id %d", c.IntegID))
}

// EncryptedMessage builds a complete IKEv2 message wrapping
// innerPayloads in a §3.14 SK payload. precedingPayloads (if any)
// are emitted in the clear before the SK — common case is nil.
//
// hdr.Length is overwritten with the final message size. hdr.NextPayload
// is overwritten with the type of the first preceding payload (or
// PayloadSK if none precede the SK).
//
// The plaintext that goes into the cipher is:
//
//	inner_payload_chain || Padding || Pad Length
//
// padded so that (len(inner) + len(Pad) + 1) is a multiple of
// BlockSize. Padding bytes are zero (§3.14: "MAY contain any value")
// — zero is the simplest deterministic choice and accepted by
// every IKEv2 peer.
//
// The ICV covers everything from the start of the IKE header through
// the last byte of ciphertext (§3.14 verbatim: "starting with the
// Fixed IKE header through the Pad Length. The checksum MUST be
// computed over the encrypted message.").
func (c *CipherSuite) EncryptedMessage(
	hdr Header,
	precedingPayloads []Payload,
	innerPayloads []Payload,
) ([]byte, error) {
	if len(innerPayloads) == 0 {
		return nil, errors.New("ikev2 SK: at least one inner payload required")
	}

	// 1. Serialise the inner payload chain.
	innerBytes, firstInner := MarshalPayloads(innerPayloads)

	// 2. Build the plaintext: inner || pad (zeros) || pad_length.
	padLen := c.BlockSize - ((len(innerBytes) + 1) % c.BlockSize)
	if padLen == c.BlockSize {
		padLen = 0
	}
	if padLen > 255 {
		return nil, errors.New("ikev2 SK: pad length > 255 (impossible)")
	}
	plaintext := make([]byte, len(innerBytes)+padLen+1)
	copy(plaintext, innerBytes)
	// zero padding (already zero-initialised)
	plaintext[len(plaintext)-1] = byte(padLen)

	// 3. Generate IV and encrypt with AES-CBC.
	iv := make([]byte, c.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	ciphertext, err := c.aesCBCEncrypt(iv, plaintext)
	if err != nil {
		return nil, err
	}

	// 4. Lay out the SK payload body: IV || ciphertext || ICV (zeros for now).
	skBody := make([]byte, 0, len(iv)+len(ciphertext)+c.ICVLen)
	skBody = append(skBody, iv...)
	skBody = append(skBody, ciphertext...)
	// reserve ICV bytes; computed below.
	icvOffset := len(skBody)
	skBody = append(skBody, make([]byte, c.ICVLen)...)

	// 5. Wrap the SK body in a generic payload header. The SK
	//    payload's Next Payload field is the type of the FIRST
	//    encrypted payload (§3.14: "an exception in the standard
	//    header format ... that type is placed here").
	skGenericHeader := make([]byte, PayloadHeaderLen)
	skGenericHeader[0] = byte(firstInner) // SK NextPayload = first inner type
	// skGenericHeader[1] = 0 (RESERVED, C bit clear)
	skPayloadLen := PayloadHeaderLen + len(skBody)
	binary.BigEndian.PutUint16(skGenericHeader[2:4], uint16(skPayloadLen))

	// 6. Pre-existing payload chain (those NOT inside SK).
	var precedingBytes []byte
	firstNext := PayloadSK
	if len(precedingPayloads) > 0 {
		// Re-serialise the chain, then patch the last entry's
		// NextPayload to point at SK. Easier: walk MarshalPayloads
		// then patch via offsets — but cleaner: manually emit each
		// generic header pointing at the next, including SK as the
		// final next.
		off := 0
		for i, p := range precedingPayloads {
			next := PayloadSK
			if i+1 < len(precedingPayloads) {
				next = precedingPayloads[i+1].Type
			}
			pl := marshalOne(p, next)
			precedingBytes = append(precedingBytes, pl...)
			_ = off
		}
		firstNext = precedingPayloads[0].Type
	}

	// 7. Patch the IKE header.
	hdr.NextPayload = firstNext
	totalLen := uint32(HeaderLen + len(precedingBytes) + skPayloadLen)
	hdr.Length = totalLen
	hdrBytes := MarshalHeader(&hdr)

	// 8. Compute the ICV over (IKE header || preceding bytes ||
	//    SK generic header || IV || ciphertext). Per §3.14 the ICV
	//    covers "the entire message starting with the Fixed IKE
	//    header through the Pad Length. The checksum MUST be
	//    computed over the encrypted message."
	mac := c.integHash()
	mac.Write(hdrBytes)
	mac.Write(precedingBytes)
	mac.Write(skGenericHeader)
	mac.Write(iv)
	mac.Write(ciphertext)
	icv := mac.Sum(nil)[:c.ICVLen]
	copy(skBody[icvOffset:], icv)

	// 9. Stitch the final message.
	out := make([]byte, 0, totalLen)
	out = append(out, hdrBytes...)
	out = append(out, precedingBytes...)
	out = append(out, skGenericHeader...)
	out = append(out, skBody...)
	return out, nil
}

// DecryptMessage parses a §3.14-wrapped IKEv2 message and returns
// the decoded IKE header, any preceding (clear) payloads, and the
// inner (decrypted) payloads. Verifies the ICV in constant time
// before decrypting.
func (c *CipherSuite) DecryptMessage(msg []byte) (
	*Header, []Payload, []Payload, error,
) {
	if len(msg) < HeaderLen+PayloadHeaderLen {
		return nil, nil, nil, fmt.Errorf("ikev2 SK: message too short (%d)", len(msg))
	}
	hdr, err := ParseHeader(msg)
	if err != nil {
		return nil, nil, nil, err
	}
	if int(hdr.Length) != len(msg) {
		return nil, nil, nil, fmt.Errorf("ikev2 SK: hdr.Length=%d != actual %d",
			hdr.Length, len(msg))
	}

	// Walk the payload chain manually because §3.14 puts the type
	// of the first ENCRYPTED payload in SK's NextPayload field — so
	// the generic chain-walker would keep reading past SK looking
	// for that type. Halt at SK and recurse into its body separately.
	var preceding []Payload
	off := HeaderLen
	curType := hdr.NextPayload
	var skOff int
	var skBody []byte
	for curType != PayloadNone {
		if off+PayloadHeaderLen > len(msg) {
			return nil, nil, nil, fmt.Errorf("ikev2 SK: chain header truncated at %d", off)
		}
		nextType := PayloadType(msg[off])
		plLen := int(binary.BigEndian.Uint16(msg[off+2 : off+4]))
		if plLen < PayloadHeaderLen || off+plLen > len(msg) {
			return nil, nil, nil, fmt.Errorf("ikev2 SK: payload at %d has bad length %d",
				off, plLen)
		}
		if curType == PayloadSK {
			skOff = off
			skBody = msg[off+PayloadHeaderLen : off+plLen]
			break
		}
		preceding = append(preceding, Payload{
			Type: curType,
			Data: append([]byte(nil), msg[off+PayloadHeaderLen:off+plLen]...),
		})
		off += plLen
		curType = nextType
	}
	if skBody == nil {
		return nil, nil, nil, errors.New("ikev2 SK: SK payload absent (RFC 7296 §3.14)")
	}
	skGenericHeader := msg[skOff : skOff+PayloadHeaderLen]

	if len(skBody) < c.BlockSize+c.ICVLen {
		return nil, nil, nil, fmt.Errorf("ikev2 SK: body too short (%d) for IV+ICV",
			len(skBody))
	}
	iv := skBody[:c.BlockSize]
	ct := skBody[c.BlockSize : len(skBody)-c.ICVLen]
	icv := skBody[len(skBody)-c.ICVLen:]
	if len(ct)%c.BlockSize != 0 {
		return nil, nil, nil, fmt.Errorf("ikev2 SK: ciphertext len %d not block-aligned",
			len(ct))
	}

	// ICV check first (constant time) — §3.14: "checksum MUST be
	// computed over the encrypted message."
	mac := c.integHash()
	mac.Write(msg[:HeaderLen])
	if len(preceding) > 0 {
		mac.Write(msg[HeaderLen : HeaderLen+(skOff-HeaderLen)])
	}
	mac.Write(skGenericHeader)
	mac.Write(iv)
	mac.Write(ct)
	wantICV := mac.Sum(nil)[:c.ICVLen]
	if subtle.ConstantTimeCompare(wantICV, icv) != 1 {
		return nil, nil, nil, errors.New("ikev2 SK: ICV mismatch (integrity check failed)")
	}

	// Decrypt + strip pad.
	pt, err := c.aesCBCDecrypt(iv, ct)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(pt) < 1 {
		return nil, nil, nil, errors.New("ikev2 SK: plaintext empty (no Pad Length byte)")
	}
	padLen := int(pt[len(pt)-1])
	if padLen+1 > len(pt) {
		return nil, nil, nil, fmt.Errorf("ikev2 SK: Pad Length %d > plaintext %d",
			padLen, len(pt))
	}
	inner := pt[:len(pt)-padLen-1]

	// Parse inner payload chain. SK's NextPayload field gave us
	// the type of the first encrypted payload.
	innerFirst := PayloadType(skGenericHeader[0])
	innerPayloads, err := ParsePayloads(inner, innerFirst)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ikev2 SK: inner payload chain: %w", err)
	}
	return hdr, preceding, innerPayloads, nil
}

func (c *CipherSuite) aesCBCEncrypt(iv, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.EncrKey)
	if err != nil {
		return nil, err
	}
	if len(plaintext)%c.BlockSize != 0 {
		return nil, fmt.Errorf("ikev2 SK: plaintext %d not block-aligned (RFC 7296 §3.14)",
			len(plaintext))
	}
	out := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, plaintext)
	return out, nil
}

func (c *CipherSuite) aesCBCDecrypt(iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.EncrKey)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, ciphertext)
	return out, nil
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package esp implements RFC 4303 Encapsulating Security Payload
// for the N3IWF NWu data plane (TS 24.502 §7.4 user-plane SAs and
// the signalling SA over which NAS rides).
//
// Cipher suite scope (matching what the IKEv2 handler negotiates in
// CREATE_CHILD_SA per nf/n3iwf/ikev2): AES-CBC-256 for confidentiality
// + HMAC-SHA-256-128 (RFC 4868 §2.1) for integrity, in tunnel mode
// (Next Header = 4 / IPv4 or 41 / IPv6 per IANA "Assigned Internet
// Protocol Numbers"). AEAD modes are out of scope for this phase.
//
// Wire layout (RFC 4303 §2 Figure 1, verbatim):
//
//	SPI (4)
//	Sequence Number (4)
//	Payload Data — IV (block_size, AES-CBC) || Ciphertext
//	Padding (0..255) | Pad Length (1) | Next Header (1)   ⟵ encrypted
//	ICV (variable)                                        ⟵ over SPI..ciphertext
//
// Authoritative spec: RFC 4303 (specs/ietf/rfc4303.txt).
package esp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
)

// Header lengths.
const (
	HeaderLen = 8 // SPI(4) + SeqNum(4)
	IVLen     = 16
	ICVLen    = 16 // HMAC-SHA-256-128 truncated to 128 bits (RFC 4868 §2.1)

	// NextHdr IANA "Assigned Internet Protocol Numbers" values used in
	// tunnel mode — the encapsulated inner packet's IP version.
	NextHdrIPv4 uint8 = 4
	NextHdrIPv6 uint8 = 41
)

// SA holds the per-direction key + state needed to encap or decap
// ESP packets. One SA per direction — outbound ESP uses an SA with
// the responder→initiator (or initiator→responder, mirror) keys
// depending on which side this is.
//
// SeqOut starts at 1 — RFC 4303 §3.3.3: "The sender's counter is
// initialized to 0 when an SA is established. The sender increments
// the sequence number ... counter for this SA and inserts the
// low-order 32 bits of the value into the Sequence Number field."
// First transmitted Sequence Number is 1.
type SA struct {
	SPI       uint32
	EncrKey   []byte // AES-256 key (32 octets)
	IntegKey  []byte // HMAC-SHA-256 key (32 octets per RFC 4868 §2.1)
	SeqOut    uint32 // last sent seq num
	SeqInWin  *replayWindow
}

// NewSA builds an outbound or inbound SA with a 64-packet replay
// window per RFC 4303 §3.4.3 ("a minimum window size of 32 packets
// MUST be supported, but a window size of 64 is preferred and
// SHOULD be employed as the default").
func NewSA(spi uint32, encrKey, integKey []byte) (*SA, error) {
	if len(encrKey) != 32 {
		return nil, fmt.Errorf("esp: encr key length %d != 32 (AES-256)", len(encrKey))
	}
	if len(integKey) != sha256.Size {
		return nil, fmt.Errorf("esp: integ key length %d != %d (HMAC-SHA-256, RFC 4868 §2.1)",
			len(integKey), sha256.Size)
	}
	return &SA{
		SPI:      spi,
		EncrKey:  append([]byte(nil), encrKey...),
		IntegKey: append([]byte(nil), integKey...),
		SeqInWin: newReplayWindow(64),
	}, nil
}

// Encap wraps inner (a complete IPv4 or IPv6 packet) in an ESP
// packet using this SA's outbound state. nextHdr selects the inner
// payload's IP version (NextHdrIPv4 / NextHdrIPv6).
//
// Returns the ESP packet ready to drop into a UDP datagram (NAT-T,
// RFC 3948) or a raw IP socket (protocol 50, RFC 4303 §2).
func (s *SA) Encap(inner []byte, nextHdr uint8) ([]byte, error) {
	// Bump seq num + check overflow per §3.3.3.
	if s.SeqOut == 0xFFFFFFFF {
		return nil, errors.New("esp: outbound sequence number wrapped (RFC 4303 §3.3.3 — must rekey)")
	}
	// Generate a fresh, unpredictable IV — required for AES-CBC's
	// IND-CPA security. crypto/rand satisfies the cryptographic-PRNG
	// requirement.
	iv := make([]byte, IVLen)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	s.SeqOut++
	return buildPacket(s.SPI, s.SeqOut, s.EncrKey, s.IntegKey, iv, inner, nextHdr)
}

// encapWithIV is the deterministic-IV variant used only by tests so
// the Go and Python sides can lock to a byte-exact hex vector.
// Production code MUST call Encap, which generates a fresh random IV
// per RFC 4303 §3.3 ("the IV [...] MUST be chosen ... to ensure that
// no two packets are encrypted with the same IV").
func (s *SA) encapWithIV(inner []byte, nextHdr uint8, iv []byte, seq uint32) ([]byte, error) {
	if len(iv) != IVLen {
		return nil, fmt.Errorf("esp: test IV length %d != %d", len(iv), IVLen)
	}
	return buildPacket(s.SPI, seq, s.EncrKey, s.IntegKey, iv, inner, nextHdr)
}

// buildPacket is the pure transformation: (inputs) → wire bytes. No
// state mutation, no IV generation, no seq-num bump — caller owns
// both. Shared by Encap (production path) and encapWithIV (test path).
func buildPacket(
	spi, seq uint32,
	encrKey, integKey, iv, inner []byte,
	nextHdr uint8,
) ([]byte, error) {
	if len(inner) == 0 {
		return nil, errors.New("esp: inner payload empty")
	}
	// Plaintext = inner | padding | padlen | nextHdr.
	// Total length must be multiple of AES block size (16). Pad
	// pattern per RFC 4303 §2.4: 1, 2, 3, ..., padlen.
	tailLen := 2 // PadLen + NextHdr
	rem := (len(inner) + tailLen) % aes.BlockSize
	padLen := 0
	if rem != 0 {
		padLen = aes.BlockSize - rem
	}
	if padLen > 255 {
		return nil, fmt.Errorf("esp: pad length %d > 255 (RFC 4303 §2.4)", padLen)
	}
	plaintext := make([]byte, len(inner)+padLen+tailLen)
	copy(plaintext, inner)
	for i := 0; i < padLen; i++ {
		plaintext[len(inner)+i] = byte(i + 1) // §2.4 pattern
	}
	plaintext[len(plaintext)-2] = byte(padLen)
	plaintext[len(plaintext)-1] = nextHdr

	block, err := aes.NewCipher(encrKey)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plaintext)

	// Assemble: SPI | Seq | IV | Ciphertext (we'll append ICV).
	pkt := make([]byte, 0, HeaderLen+IVLen+len(ciphertext)+ICVLen)
	pkt = appendU32(pkt, spi)
	pkt = appendU32(pkt, seq)
	pkt = append(pkt, iv...)
	pkt = append(pkt, ciphertext...)

	// ICV = HMAC-SHA-256(IntegKey, SPI || Seq || IV || Ciphertext)
	// truncated to 128 bits per RFC 4303 §2.8 + RFC 4868 §2.1.
	mac := hmac.New(sha256.New, integKey)
	mac.Write(pkt) // covers SPI..Ciphertext exactly per §2.8
	icv := mac.Sum(nil)[:ICVLen]
	pkt = append(pkt, icv...)
	return pkt, nil
}

// Decap unwraps an ESP packet, verifies the ICV (constant-time),
// runs anti-replay (§3.4.3), AES-CBC-decrypts, strips padding, and
// returns the inner IP packet plus its NextHdr (NextHdrIPv4 / IPv6).
//
// SPI must match s.SPI — caller is expected to have demuxed by SPI
// before calling Decap.
func (s *SA) Decap(pkt []byte) ([]byte, uint8, error) {
	if len(pkt) < HeaderLen+IVLen+aes.BlockSize+ICVLen {
		return nil, 0, fmt.Errorf("esp: packet too short (%d) for header+IV+1block+ICV", len(pkt))
	}
	spi := binary.BigEndian.Uint32(pkt[0:4])
	seq := binary.BigEndian.Uint32(pkt[4:8])
	if spi != s.SPI {
		return nil, 0, fmt.Errorf("esp: SPI %08x != SA SPI %08x", spi, s.SPI)
	}

	// Anti-replay first per §3.4.3 — cheap check before HMAC.
	if !s.SeqInWin.check(seq) {
		return nil, 0, fmt.Errorf("esp: replay/old sequence number %d (RFC 4303 §3.4.3)", seq)
	}

	icvOff := len(pkt) - ICVLen
	icv := pkt[icvOff:]
	macInput := pkt[:icvOff]

	// HMAC-SHA-256 verify (constant-time).
	mac := hmac.New(sha256.New, s.IntegKey)
	mac.Write(macInput)
	want := mac.Sum(nil)[:ICVLen]
	if subtle.ConstantTimeCompare(want, icv) != 1 {
		return nil, 0, errors.New("esp: ICV mismatch (RFC 4303 §3.4.4 — packet rejected)")
	}

	// AES-CBC decrypt the body (after IV, before ICV).
	iv := pkt[HeaderLen : HeaderLen+IVLen]
	ct := pkt[HeaderLen+IVLen : icvOff]
	if len(ct)%aes.BlockSize != 0 {
		return nil, 0, fmt.Errorf("esp: ciphertext len %d not block-aligned", len(ct))
	}
	block, err := aes.NewCipher(s.EncrKey)
	if err != nil {
		return nil, 0, err
	}
	pt := make([]byte, len(ct))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(pt, ct)

	// Strip pad / pull NextHdr.
	if len(pt) < 2 {
		return nil, 0, errors.New("esp: plaintext shorter than PadLen+NextHdr trailer")
	}
	padLen := int(pt[len(pt)-2])
	nextHdr := pt[len(pt)-1]
	if 2+padLen > len(pt) {
		return nil, 0, fmt.Errorf("esp: pad len %d overruns plaintext %d", padLen, len(pt))
	}
	// Verify pad pattern per §2.4 — implementations SHOULD check it
	// to detect cipher errors that didn't perturb the ICV (which
	// would be the cipher itself being broken, but cheap defense).
	for i := 0; i < padLen; i++ {
		if pt[len(pt)-2-padLen+i] != byte(i+1) {
			return nil, 0, fmt.Errorf("esp: pad pattern violation at byte %d (RFC 4303 §2.4)", i)
		}
	}
	inner := append([]byte(nil), pt[:len(pt)-2-padLen]...)

	// ICV passed + replay check passed — commit the seq num to the
	// window so a duplicate packet is rejected next time.
	s.SeqInWin.commit(seq)
	return inner, nextHdr, nil
}

func appendU32(b []byte, v uint32) []byte {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], v)
	return append(b, t[:]...)
}

// replayWindow implements the §3.4.3 anti-replay window. Stores the
// last 'size' bits in a uint64 (so size <= 64). Window slides forward
// when a fresh higher seq num arrives.
//
// Verbatim from RFC 4303 §3.4.3: "If the received packet's sequence
// number falls within the window and is new, or if the packet is to
// the right of the window, then the receiver proceeds to ICV
// verification. If the ICV validation fails, the receiver MUST
// discard the received IP datagram as invalid; this is an auditable
// event."
type replayWindow struct {
	last uint32 // highest seen seq num
	size uint   // window size (typically 64)
	bits uint64 // bit i = (last - i) was seen
}

func newReplayWindow(size uint) *replayWindow {
	if size == 0 || size > 64 {
		size = 64
	}
	return &replayWindow{size: size}
}

// check returns false if seq is too old or already seen. Does NOT
// commit — caller must call commit() after the ICV passes (otherwise
// a forged packet with valid seq num but wrong ICV would shift the
// window and let a real later packet be denied).
func (w *replayWindow) check(seq uint32) bool {
	if seq == 0 {
		// §3.4.3: "the value 0 is reserved" if extended seq numbers
		// are not in use — packets with seq=0 are rejected.
		return false
	}
	if seq > w.last {
		return true // future packet, definitely accept (subject to ICV)
	}
	diff := w.last - seq
	if diff >= uint32(w.size) {
		return false // beyond window — too old
	}
	if w.bits&(1<<diff) != 0 {
		return false // already seen
	}
	return true
}

func (w *replayWindow) commit(seq uint32) {
	if seq > w.last {
		shift := seq - w.last
		if shift >= uint32(w.size) {
			w.bits = 0
		} else {
			w.bits <<= shift
		}
		w.bits |= 1
		w.last = seq
		return
	}
	diff := w.last - seq
	w.bits |= 1 << diff
}

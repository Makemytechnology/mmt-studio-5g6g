# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""RFC 4303 Encapsulating Security Payload — UE-side mirror.

The N3IWF UE simulator drives the data plane over NWu (TS 24.502 §7.4):
ESP-in-UDP encapsulating an inner IPv4 (NextHdr=4) or IPv6 (NextHdr=41)
packet. Cipher suite — AES-CBC-256 + HMAC-SHA-256-128 — is what the
IKEv2 handler negotiates in CREATE_CHILD_SA.

Wire layout (RFC 4303 §2 Figure 1, verbatim):
    SPI (4)
    Sequence Number (4)
    IV (16)               ⟵ AES-CBC IV
    Ciphertext (n*16)     ⟵ AES-CBC over (inner | pad | padlen | nexthdr)
    ICV (16)              ⟵ HMAC-SHA-256-128 over SPI..Ciphertext

Mirrors nf/n3iwf/esp/esp.go byte-exactly so a packet built by either
side parses on the other.
"""

import hmac as _hmac
import hashlib as _hashlib
import os
import struct
from dataclasses import dataclass, field
from typing import Tuple

from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes


HEADER_LEN = 8        # SPI(4) + SeqNum(4)
IV_LEN = 16
ICV_LEN = 16          # HMAC-SHA-256-128 (RFC 4868 §2.1)
BLOCK_SIZE = 16       # AES

# IANA "Assigned Internet Protocol Numbers" — tunnel-mode inner.
NEXT_HDR_IPV4 = 4
NEXT_HDR_IPV6 = 41


# ─── §3.4.3 Anti-replay window ────────────────────────────────────


class _ReplayWindow:
    """64-bit sliding window per RFC 4303 §3.4.3.

    'A minimum window size of 32 packets MUST be supported, but a
    window size of 64 is preferred and SHOULD be employed as the
    default.' Stores the last `size` bits in a uint64.

    check() does NOT commit — caller commits *after* the ICV passes,
    so a forged packet with a valid seq num but wrong ICV cannot
    advance the window and starve a legitimate later packet."""

    def __init__(self, size: int = 64):
        if size == 0 or size > 64:
            size = 64
        self.size = size
        self.last = 0    # highest seen seq num
        self.bits = 0    # bit i ⇒ (last - i) was seen

    def check(self, seq: int) -> bool:
        # §3.4.3: seq=0 reserved when extended seq nums are off.
        if seq == 0:
            return False
        if seq > self.last:
            return True
        diff = self.last - seq
        if diff >= self.size:
            return False
        return (self.bits & (1 << diff)) == 0

    def commit(self, seq: int) -> None:
        if seq > self.last:
            shift = seq - self.last
            if shift >= self.size:
                self.bits = 0
            else:
                self.bits = (self.bits << shift) & ((1 << self.size) - 1)
            self.bits |= 1
            self.last = seq
            return
        diff = self.last - seq
        self.bits |= (1 << diff)


# ─── SA: per-direction key + state ────────────────────────────────


@dataclass
class SA:
    """One SA per direction. Mirrors nf/n3iwf/esp/esp.go::SA.

    SeqOut starts at 0 — RFC 4303 §3.3.3: 'The sender's counter is
    initialized to 0 ... the sender increments and inserts the
    low-order 32 bits.' First transmitted seq num is 1."""

    spi: int
    encr_key: bytes              # AES-256 key (32 octets)
    integ_key: bytes             # HMAC-SHA-256 key (32 octets)
    seq_out: int = 0
    seq_in: _ReplayWindow = field(default_factory=lambda: _ReplayWindow(64))

    def __post_init__(self):
        if len(self.encr_key) != 32:
            raise ValueError(
                f"esp: encr key length {len(self.encr_key)} != 32 (AES-256)"
            )
        if len(self.integ_key) != 32:
            raise ValueError(
                f"esp: integ key length {len(self.integ_key)} != 32 "
                f"(HMAC-SHA-256, RFC 4868 §2.1)"
            )

    # ── Encap (RFC 4303 §2 + §3.3) ────────────────────────────────

    def encap(self, inner: bytes, next_hdr: int = NEXT_HDR_IPV4) -> bytes:
        """Wrap an inner IP packet. Returns the ESP packet ready to
        drop into a UDP datagram (NAT-T, RFC 3948) or raw IP-50."""
        if self.seq_out == 0xFFFFFFFF:
            raise ValueError(
                "esp: outbound sequence number wrapped (RFC 4303 §3.3.3 — must rekey)"
            )
        # Fresh, unpredictable IV. os.urandom is the cryptographic PRNG.
        iv = os.urandom(IV_LEN)
        self.seq_out += 1
        return _build_packet(
            spi=self.spi, seq=self.seq_out,
            encr_key=self.encr_key, integ_key=self.integ_key,
            iv=iv, inner=inner, next_hdr=next_hdr,
        )

    # ── Encap with caller-supplied IV / seq — TESTING ONLY ────────
    #
    # Lets the byte-vector cross-language test pin every input that
    # affects the wire bytes, so the Go and Python sides can lock to
    # the same hex constant. Production code MUST use encap(), which
    # generates a fresh random IV per RFC 4303 §3.3 ('the IV [...]
    # MUST be chosen ... to ensure that no two packets are encrypted
    # with the same IV').
    def _encap_deterministic(
        self, inner: bytes, next_hdr: int, iv: bytes, seq: int,
    ) -> bytes:
        if len(iv) != IV_LEN:
            raise ValueError(f"esp: test IV length {len(iv)} != {IV_LEN}")
        return _build_packet(
            spi=self.spi, seq=seq,
            encr_key=self.encr_key, integ_key=self.integ_key,
            iv=iv, inner=inner, next_hdr=next_hdr,
        )

    # ── Decap (RFC 4303 §3.4) ─────────────────────────────────────

    def decap(self, pkt: bytes) -> Tuple[bytes, int]:
        """Unwrap an ESP packet. Returns (inner, next_hdr).
        Raises on ICV failure, replay, or pad violation."""
        if len(pkt) < HEADER_LEN + IV_LEN + BLOCK_SIZE + ICV_LEN:
            raise ValueError(
                f"esp: packet too short ({len(pkt)}) for header+IV+1block+ICV"
            )
        spi, seq = struct.unpack(">II", pkt[:8])
        if spi != self.spi:
            raise ValueError(f"esp: SPI {spi:08x} != SA SPI {self.spi:08x}")

        # §3.4.3: replay check first — cheap before HMAC.
        if not self.seq_in.check(seq):
            raise ValueError(
                f"esp: replay/old sequence number {seq} (RFC 4303 §3.4.3)"
            )

        icv_off = len(pkt) - ICV_LEN
        icv = pkt[icv_off:]
        mac_input = pkt[:icv_off]

        want = _hmac.new(self.integ_key, mac_input, _hashlib.sha256).digest()[:ICV_LEN]
        # §3.4.4 — constant-time compare.
        if not _hmac.compare_digest(want, icv):
            raise ValueError("esp: ICV mismatch (RFC 4303 §3.4.4 — packet rejected)")

        iv = pkt[HEADER_LEN:HEADER_LEN + IV_LEN]
        ct = pkt[HEADER_LEN + IV_LEN:icv_off]
        if len(ct) % BLOCK_SIZE != 0:
            raise ValueError(f"esp: ciphertext len {len(ct)} not block-aligned")

        pt = _aes_cbc_decrypt(self.encr_key, iv, ct)
        if len(pt) < 2:
            raise ValueError("esp: plaintext shorter than PadLen+NextHdr trailer")
        pad_len = pt[-2]
        next_hdr = pt[-1]
        if 2 + pad_len > len(pt):
            raise ValueError(
                f"esp: pad len {pad_len} overruns plaintext {len(pt)}"
            )
        # §2.4 pad pattern check — defends against cipher-error packets
        # that somehow survive the ICV.
        for i in range(pad_len):
            expected = i + 1
            actual = pt[-2 - pad_len + i]
            if actual != expected:
                raise ValueError(
                    f"esp: pad pattern violation at byte {i} (RFC 4303 §2.4)"
                )
        inner = bytes(pt[:-2 - pad_len])

        # ICV + replay both passed — only NOW commit so a forged packet
        # can't shift the window.
        self.seq_in.commit(seq)
        return inner, next_hdr


# ─── helpers ──────────────────────────────────────────────────────


def _build_packet(
    spi: int, seq: int,
    encr_key: bytes, integ_key: bytes,
    iv: bytes, inner: bytes, next_hdr: int,
) -> bytes:
    """The pure transformation: (inputs) → wire bytes.

    No state mutation, no IV generation, no seq-num bump — caller
    owns both. Shared by encap() (production path with random IV +
    auto-incremented seq) and _encap_deterministic() (test path
    that pins both for byte-exact cross-language vectors)."""
    if not inner:
        raise ValueError("esp: inner payload empty")
    # Plaintext = inner | padding | padlen | nexthdr.
    # Total must be a multiple of AES block size. Pad pattern per
    # §2.4: 1, 2, 3, ..., padlen.
    tail_len = 2  # PadLen + NextHdr
    rem = (len(inner) + tail_len) % BLOCK_SIZE
    pad_len = 0 if rem == 0 else (BLOCK_SIZE - rem)
    if pad_len > 255:
        raise ValueError(f"esp: pad length {pad_len} > 255 (RFC 4303 §2.4)")
    plaintext = bytearray(inner)
    for i in range(pad_len):
        plaintext.append(i + 1)             # §2.4 pattern
    plaintext.append(pad_len)
    plaintext.append(next_hdr)

    ct = _aes_cbc_encrypt(encr_key, iv, bytes(plaintext))

    pkt = struct.pack(">II", spi, seq) + iv + ct
    # §2.8: ICV scope is SPI..Ciphertext (not including ICV itself).
    icv = _hmac.new(integ_key, pkt, _hashlib.sha256).digest()[:ICV_LEN]
    return pkt + icv


def _aes_cbc_encrypt(key: bytes, iv: bytes, plaintext: bytes) -> bytes:
    c = Cipher(algorithms.AES(key), modes.CBC(iv)).encryptor()
    return c.update(plaintext) + c.finalize()


def _aes_cbc_decrypt(key: bytes, iv: bytes, ciphertext: bytes) -> bytes:
    c = Cipher(algorithms.AES(key), modes.CBC(iv)).decryptor()
    return c.update(ciphertext) + c.finalize()


def peek_spi(pkt: bytes) -> int:
    """Demux helper — first 4 octets are SPI per RFC 4303 §2."""
    if len(pkt) < 4:
        raise ValueError("esp: packet too short to contain SPI")
    return struct.unpack(">I", pkt[:4])[0]

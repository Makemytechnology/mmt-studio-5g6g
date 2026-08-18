# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Pure GTP-U §5.1 codec — no I/O, no TUN, no netlink.

Mirrors nf/n3iwf/gtpu/gtpu.go byte-exactly so packets built by either
side parse on the other. The runtime tunnel manager lives in gtpu.py;
this module is the wire-format-only path that unit tests can import
without pulling in fcntl/netlink (so it runs on macOS / Windows too).

Spec scope:
    TS 29.281 §5.1     — Outline of the GTP-U Header (Figure 5.1-1)
    TS 29.281 §5.2     — GTP-U Extension Header (parser walks; no
                          generation here)
    TS 29.281 §6.1     — Message Types (Table 6.1-1) — only G-PDU
                          (255) and Echo Request/Response (1/2) are
                          meaningful for N3IWF
"""

import struct
from dataclasses import dataclass
from typing import Tuple


# ─── §6.1 Message Types (verbatim subset) ─────────────────────────
MSG_ECHO_REQUEST = 1
MSG_ECHO_RESPONSE = 2
MSG_END_MARKER = 254
MSG_GPDU = 255

# §5.1 Mandatory header octets: Flags(1) + MsgType(1) + Length(2) + TEID(4) = 8.
HEADER_LEN = 8

# §5.1 Figure 5.1-1 flags:
#   Version(3) | PT(1) | spare(1) | E(1) | S(1) | PN(1)
_FLAG_VERSION_1 = 0b001 << 5     # GTPv1 — §5.1: 'shall be set to 1'
_FLAG_PT = 1 << 4                # PT=1 ⇒ GTP (vs GTP')
_FLAG_E = 1 << 2                 # Extension Header present
_FLAG_S = 1 << 1                 # Sequence Number present
_FLAG_PN = 1 << 0                # N-PDU Number present

# Outbound G-PDU with no S/PN/E options.
FLAGS_BASE_GPDU = _FLAG_VERSION_1 | _FLAG_PT  # 0x30


@dataclass
class Header:
    """Parsed §5.1 GTP-U header. Optional fields are zero / False
    when their flag was clear on the wire."""
    version: int          # 1 for GTPv1
    protocol_type: int    # 1 for GTP, 0 for GTP'
    type: int             # MSG_GPDU / MSG_ECHO_*
    length: int           # §5.1: payload length AFTER the 8-octet mandatory header
    teid: int

    has_seq: bool = False
    has_npdu: bool = False
    has_ext: bool = False
    seq: int = 0          # §5.1 NOTE 1
    npdu: int = 0         # §5.1 NOTE 2
    next_ext_type: int = 0  # §5.1 NOTE 3


class NotGPDU(ValueError):
    """Header parsed cleanly but the message wasn't a G-PDU.
    Caller can dispatch Echo Request/Response separately."""


def encap_gpdu(teid: int, inner: bytes) -> bytes:
    """Wrap an inner T-PDU (a complete IPv4 / IPv6 datagram) in a §5.1
    G-PDU header with no optional fields.

    teid identifies the receiver's tunnel endpoint per §5.1
    ('This field unambiguously identifies a tunnel endpoint in the
    receiving GTP-U protocol entity')."""
    if not inner:
        raise ValueError("gtpu: T-PDU empty")
    if len(inner) > 0xFFFF:
        raise ValueError(
            f"gtpu: T-PDU length {len(inner)} > 65535 (Length field is 16-bit)"
        )
    if not (0 <= teid <= 0xFFFFFFFF):
        raise ValueError(f"gtpu: TEID {teid} not in [0, 2^32-1]")
    return (
        bytes([FLAGS_BASE_GPDU, MSG_GPDU])
        + struct.pack(">H", len(inner))
        + struct.pack(">I", teid)
        + inner
    )


def decode_gpdu(buf: bytes) -> Tuple[Header, bytes]:
    """Parse a wire-format GTP-U packet, expect type=G-PDU, and
    return (Header, T-PDU). Raises NotGPDU if the message type is
    well-formed but isn't a G-PDU."""
    hdr, body = decode_header(buf)
    if hdr.type != MSG_GPDU:
        raise NotGPDU(f"gtpu: not a G-PDU — got message type {hdr.type}")
    return hdr, body


def decode_header(buf: bytes) -> Tuple[Header, bytes]:
    """Parse the §5.1 header (mandatory + optional block + extension
    headers per §5.2), validate Version=1 / PT=1 / Length matches,
    and return (Header, body) where body is the T-PDU."""
    if len(buf) < HEADER_LEN:
        raise ValueError(
            f"gtpu: packet too short for header ({len(buf)} < {HEADER_LEN})"
        )
    flags = buf[0]
    h = Header(
        version=(flags >> 5) & 0x07,
        protocol_type=(flags >> 4) & 0x01,
        type=buf[1],
        length=struct.unpack(">H", buf[2:4])[0],
        teid=struct.unpack(">I", buf[4:8])[0],
    )
    if h.version != 1:
        raise ValueError(f"gtpu: version {h.version} != 1 (TS 29.281 §5.1)")
    if h.protocol_type != 1:
        raise ValueError(
            f"gtpu: PT {h.protocol_type} — GTP' not supported (TS 29.281 §5.1)"
        )

    # §5.1 NOTE 4: the optional 4-octet block is present iff any of
    # S / PN / E is set. Walk it, then any §5.2 extension headers.
    has_optional = bool(flags & (_FLAG_S | _FLAG_PN | _FLAG_E))
    off = HEADER_LEN
    if has_optional:
        if len(buf) < HEADER_LEN + 4:
            raise ValueError(f"gtpu: optional header block truncated ({len(buf)})")
        h.has_seq = bool(flags & _FLAG_S)
        h.has_npdu = bool(flags & _FLAG_PN)
        h.has_ext = bool(flags & _FLAG_E)
        h.seq = struct.unpack(">H", buf[8:10])[0]
        h.npdu = buf[10]
        h.next_ext_type = buf[11]
        off += 4

        # §5.2 extension headers: walk if E=1 and Next Ext Type != 0.
        # We don't process specific extensions — just skip each so the
        # caller can locate the T-PDU.
        next_type = h.next_ext_type
        while h.has_ext and next_type != 0:
            if off + 1 > len(buf):
                raise ValueError(f"gtpu: extension header truncated at {off}")
            ext_len = buf[off] * 4  # §5.2.1: length in 4-octet units
            if ext_len < 4:
                raise ValueError(
                    "gtpu: extension header length 0 (TS 29.281 §5.2.1)"
                )
            if off + ext_len > len(buf):
                raise ValueError(
                    f"gtpu: extension header overruns buffer (off={off}, len={ext_len})"
                )
            next_type = buf[off + ext_len - 1]
            off += ext_len

    # §5.1 Length: payload (everything after the mandatory 8 octets)
    # MUST equal the Length field. Includes optional block + any ext
    # headers per §5.1.
    want = h.length
    got = len(buf) - HEADER_LEN
    if got != want:
        raise ValueError(
            f"gtpu: Length={want} != actual payload {got} (TS 29.281 §5.1)"
        )
    return h, buf[off:]


def peek_teid(buf: bytes) -> int:
    """Demux helper — TEID lives at offsets 4..8 per §5.1."""
    if len(buf) < HEADER_LEN:
        raise ValueError("gtpu: packet too short to contain TEID")
    return struct.unpack(">I", buf[4:8])[0]

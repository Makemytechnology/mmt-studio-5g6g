# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""IKEv2 initiator helpers for the N3IWF UE simulator.

Authoritative spec: RFC 7296 (IKEv2). Sections cited verbatim:
  - §3.1   IKE Header (28 octets, big-endian)
  - §3.2   Generic Payload Header (4 octets)
  - §3.3   Security Association payload + Proposal/Transform
  - §3.4   Key Exchange (KE)
  - §3.5   Identification (IDi/IDr)
  - §3.8   Authentication
  - §3.9   Nonce (16..256 octets)
  - §3.10  Notify
  - §3.14  Encrypted (SK) — AES-CBC + HMAC integrity
  - §2.10  Diffie-Hellman (modp groups from RFC 3526)
  - §2.13  prf+
  - §2.14  SKEYSEED + 7-key derivation

Mirrors the Go side at nf/n3iwf/ikev2/. Wire format byte-exact;
key derivation byte-exact (same {SK_d, SK_a{i,r}, SK_e{i,r},
SK_p{i,r}} given same inputs). The tester drives the N3IWF as a
UE — i.e. the *initiator* per RFC 7296 §1.2.
"""

import os
import secrets
import struct
from dataclasses import dataclass, field
from typing import List, Optional, Tuple

from cryptography.hazmat.primitives import hashes, hmac
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes


# ─── §3.1 Exchange Type table (verbatim) ───────────────────────────
EXCHANGE_IKE_SA_INIT = 34
EXCHANGE_IKE_AUTH = 35
EXCHANGE_CREATE_CHILD_SA = 36
EXCHANGE_INFORMATIONAL = 37


# ─── §3.2 Next Payload Type table (verbatim) ───────────────────────
PAYLOAD_NONE = 0
PAYLOAD_SA = 33
PAYLOAD_KE = 34
PAYLOAD_IDI = 35
PAYLOAD_IDR = 36
PAYLOAD_CERT = 37
PAYLOAD_CERTREQ = 38
PAYLOAD_AUTH = 39
PAYLOAD_NONCE = 40
PAYLOAD_NOTIFY = 41
PAYLOAD_DELETE = 42
PAYLOAD_VENDOR = 43
PAYLOAD_TSI = 44
PAYLOAD_TSR = 45
PAYLOAD_SK = 46
PAYLOAD_CP = 47
PAYLOAD_EAP = 48


# ─── §3.1 Flags ────────────────────────────────────────────────────
FLAG_INITIATOR = 1 << 3
FLAG_VERSION = 1 << 4
FLAG_RESPONSE = 1 << 5

HEADER_LEN = 28
PAYLOAD_HEADER_LEN = 4
VERSION_BYTE = 0x20  # MjVer=2, MnVer=0 per §3.1


# ─── §3.3.1/§3.3.2 Protocol & Transform tables ─────────────────────
PROTO_IKE = 1
PROTO_AH = 2
PROTO_ESP = 3

TRANSFORM_ENCR = 1
TRANSFORM_PRF = 2
TRANSFORM_INTEG = 3
TRANSFORM_DH = 4
TRANSFORM_ESN = 5

ENCR_AES_CBC = 12
PRF_HMAC_SHA256 = 5  # RFC 4868
INTEG_HMAC_SHA256_128 = 12  # RFC 4868
DH_MODP_2048 = 14  # RFC 3526 §3
ATTR_KEY_LENGTH = 14


# ─── §3.5 ID types ────────────────────────────────────────────────
ID_IPV4_ADDR = 1
ID_FQDN = 2
ID_RFC822_ADDR = 3
ID_KEY_ID = 11


# ─── §3.10 Notify types (subset) ───────────────────────────────────
NOTIFY_INVALID_KE_PAYLOAD = 17
NOTIFY_NO_PROPOSAL_CHOSEN = 14
NOTIFY_AUTHENTICATION_FAILED = 24


# ─── §3.1 IKE Header ───────────────────────────────────────────────


@dataclass
class Header:
    spi_i: bytes
    spi_r: bytes
    next_payload: int
    exchange_type: int
    flags: int
    message_id: int
    length: int = 0
    version: int = VERSION_BYTE

    def marshal(self) -> bytes:
        if len(self.spi_i) != 8 or len(self.spi_r) != 8:
            raise ValueError("RFC 7296 §3.1: SPIs are 8 octets")
        return (
            self.spi_i
            + self.spi_r
            + bytes([self.next_payload, self.version,
                     self.exchange_type, self.flags])
            + struct.pack(">II", self.message_id, self.length)
        )


def parse_header(buf: bytes) -> Header:
    if len(buf) < HEADER_LEN:
        raise ValueError(f"RFC 7296 §3.1 header truncated ({len(buf)} < {HEADER_LEN})")
    spi_i = buf[0:8]
    spi_r = buf[8:16]
    if spi_i == b"\x00" * 8:
        # §3.1 verbatim: "Initiator's SPI ... value MUST NOT be zero."
        raise ValueError("RFC 7296 §3.1: initiator SPI is zero")
    next_payload, version, exchange, flags = buf[16], buf[17], buf[18], buf[19]
    message_id, length = struct.unpack(">II", buf[20:28])
    return Header(spi_i=spi_i, spi_r=spi_r, next_payload=next_payload,
                  exchange_type=exchange, flags=flags, message_id=message_id,
                  length=length, version=version)


# ─── §3.2 Generic payload chain ────────────────────────────────────


@dataclass
class Payload:
    type: int
    data: bytes
    critical: bool = False


def marshal_payloads(payloads: List[Payload]) -> Tuple[bytes, int]:
    """Returns (bytes, first_payload_type) — caller writes
    first_payload_type into the IKE header's NextPayload field."""
    if not payloads:
        return b"", PAYLOAD_NONE
    out = bytearray()
    for i, p in enumerate(payloads):
        nxt = payloads[i + 1].type if i + 1 < len(payloads) else PAYLOAD_NONE
        pl_len = PAYLOAD_HEADER_LEN + len(p.data)
        if pl_len > 0xFFFF:
            raise ValueError("RFC 7296 §3.2 payload length overflow")
        crit = 0x80 if p.critical else 0
        out += bytes([nxt, crit]) + struct.pack(">H", pl_len) + p.data
    return bytes(out), payloads[0].type


def parse_payloads(buf: bytes, first_type: int) -> List[Payload]:
    out = []
    pt = first_type
    off = 0
    while pt != PAYLOAD_NONE:
        if off + PAYLOAD_HEADER_LEN > len(buf):
            raise ValueError(f"RFC 7296 §3.2 payload header truncated at {off}")
        nxt = buf[off]
        crit_byte = buf[off + 1]
        if crit_byte & 0x7F:
            raise ValueError(f"RFC 7296 §3.2 RESERVED bits set at {off}")
        pl_len = struct.unpack(">H", buf[off + 2:off + 4])[0]
        if pl_len < PAYLOAD_HEADER_LEN or off + pl_len > len(buf):
            raise ValueError(f"RFC 7296 §3.2 bad payload length {pl_len}")
        out.append(Payload(type=pt, data=buf[off + PAYLOAD_HEADER_LEN:off + pl_len],
                            critical=bool(crit_byte & 0x80)))
        off += pl_len
        pt = nxt
    return out


def find_payload(payloads: List[Payload], pt: int) -> Optional[Payload]:
    for p in payloads:
        if p.type == pt:
            return p
    return None


# ─── §3.3 SA / Proposal / Transform ────────────────────────────────


@dataclass
class Attribute:
    type: int
    is_tv: bool = False
    tv_value: int = 0
    value: bytes = b""

    def marshal(self) -> bytes:
        if self.is_tv:
            return struct.pack(">HH", self.type | 0x8000, self.tv_value)
        return struct.pack(">HH", self.type & 0x7FFF, len(self.value)) + self.value


@dataclass
class Transform:
    type: int
    id: int
    attributes: List[Attribute] = field(default_factory=list)

    def marshal(self, last: int) -> bytes:
        attrs = b"".join(a.marshal() for a in self.attributes)
        tlen = 8 + len(attrs)
        return (struct.pack(">BBH", last, 0, tlen)
                + struct.pack(">BBH", self.type, 0, self.id)
                + attrs)


@dataclass
class Proposal:
    num: int
    protocol_id: int
    spi: bytes = b""
    transforms: List[Transform] = field(default_factory=list)

    def marshal(self, last: int) -> bytes:
        tparts = []
        for i, t in enumerate(self.transforms):
            tlast = 0 if i + 1 == len(self.transforms) else 3
            tparts.append(t.marshal(tlast))
        tbody = b"".join(tparts)
        plen = 8 + len(self.spi) + len(tbody)
        return (struct.pack(">BBH", last, 0, plen)
                + bytes([self.num, self.protocol_id, len(self.spi),
                         len(self.transforms)])
                + self.spi + tbody)


@dataclass
class SA:
    proposals: List[Proposal] = field(default_factory=list)

    def marshal(self) -> bytes:
        out = bytearray()
        for i, p in enumerate(self.proposals):
            last = 0 if i + 1 == len(self.proposals) else 2
            out += p.marshal(last)
        return bytes(out)


def default_ike_proposal() -> Proposal:
    """Operator-mandated minimum: AES-CBC-256 + HMAC-SHA256-128 +
    PRF-HMAC-SHA256 + MODP-2048. Mirrors ikev2.IKEDefaultProposal()
    on the Go side."""
    return Proposal(
        num=1, protocol_id=PROTO_IKE,
        transforms=[
            Transform(type=TRANSFORM_ENCR, id=ENCR_AES_CBC,
                      attributes=[Attribute(type=ATTR_KEY_LENGTH, is_tv=True,
                                             tv_value=256)]),
            Transform(type=TRANSFORM_PRF, id=PRF_HMAC_SHA256),
            Transform(type=TRANSFORM_INTEG, id=INTEG_HMAC_SHA256_128),
            Transform(type=TRANSFORM_DH, id=DH_MODP_2048),
        ],
    )


# ─── §3.4 Key Exchange (KE) ────────────────────────────────────────


def marshal_ke(dh_group: int, public: bytes) -> bytes:
    return struct.pack(">HH", dh_group, 0) + public


def parse_ke(buf: bytes) -> Tuple[int, bytes]:
    if len(buf) < 4:
        raise ValueError("RFC 7296 §3.4 KE truncated")
    dh_group = struct.unpack(">H", buf[0:2])[0]
    return dh_group, buf[4:]


# ─── §3.5 Identification ───────────────────────────────────────────


def marshal_id(id_type: int, data: bytes) -> bytes:
    return bytes([id_type, 0, 0, 0]) + data


def parse_id(buf: bytes) -> Tuple[int, bytes]:
    if len(buf) < 4:
        raise ValueError("RFC 7296 §3.5 ID truncated")
    return buf[0], buf[4:]


# ─── §3.9 Nonce ────────────────────────────────────────────────────


def fresh_nonce(n: int = 32) -> bytes:
    if not 16 <= n <= 256:
        raise ValueError("RFC 7296 §3.9: Nonce must be 16..256 octets")
    return secrets.token_bytes(n)


# ─── §3.10 Notify ──────────────────────────────────────────────────


@dataclass
class Notify:
    type: int
    protocol_id: int = 0
    spi: bytes = b""
    data: bytes = b""

    def marshal(self) -> bytes:
        return (bytes([self.protocol_id, len(self.spi)])
                + struct.pack(">H", self.type)
                + self.spi + self.data)


def parse_notify(buf: bytes) -> Notify:
    if len(buf) < 4:
        raise ValueError("RFC 7296 §3.10 Notify truncated")
    proto = buf[0]
    spi_len = buf[1]
    notify_type = struct.unpack(">H", buf[2:4])[0]
    if 4 + spi_len > len(buf):
        raise ValueError("RFC 7296 §3.10 SPI overruns")
    spi = buf[4:4 + spi_len]
    data = buf[4 + spi_len:]
    return Notify(type=notify_type, protocol_id=proto, spi=spi, data=data)


# ─── §2.10 + RFC 3526 §3 Diffie-Hellman MODP-2048 ──────────────────

# RFC 3526 §3 (verbatim): the 2048-bit MODP prime. Generator = 2.
MODP_2048_HEX = (
    "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1"
    "29024E088A67CC74020BBEA63B139B22514A08798E3404DD"
    "EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245"
    "E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED"
    "EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE45B3D"
    "C2007CB8A163BF0598DA48361C55D39A69163FA8FD24CF5F"
    "83655D23DCA3AD961C62F356208552BB9ED529077096966D"
    "670C354E4ABC9804F1746C08CA18217C32905E462E36CE3B"
    "E39E772C180E86039B2783A2EC07A28FB5C55DF06F4C52C9"
    "DE2BCBF6955817183995497CEA956AE515D2261898FA0510"
    "15728E5A8AACAA68FFFFFFFFFFFFFFFF"
)
_MODP_2048_P = int(MODP_2048_HEX, 16)
_MODP_2048_G = 2
_MODP_2048_LEN = (_MODP_2048_P.bit_length() + 7) // 8  # = 256


class DH_MODP2048:
    """Diffie-Hellman MODP-2048 (RFC 3526 §3) with the RFC 6989
    trivial-subgroup checks (peer != {1, p-1}). Public values are
    big-endian zero-padded to the modulus length per RFC 7296 §2.14."""

    def __init__(self):
        self.priv = secrets.randbelow(_MODP_2048_P - 2) + 1  # in [1, p-2]
        pub_int = pow(_MODP_2048_G, self.priv, _MODP_2048_P)
        self.public = pub_int.to_bytes(_MODP_2048_LEN, "big")

    def shared(self, peer_public: bytes) -> bytes:
        if not peer_public:
            raise ValueError("RFC 7296 §2.10: empty peer DH public value")
        peer_int = int.from_bytes(peer_public, "big")
        if peer_int <= 0 or peer_int >= _MODP_2048_P:
            raise ValueError("DH peer public out of [1, p-1]")
        if peer_int == 1 or peer_int == _MODP_2048_P - 1:
            raise ValueError("RFC 6989: peer in trivial subgroup")
        s = pow(peer_int, self.priv, _MODP_2048_P)
        return s.to_bytes(_MODP_2048_LEN, "big")


# ─── §2.13 prf + §2.14 SKEYSEED ────────────────────────────────────


def prf_hmac_sha256(key: bytes, data: bytes) -> bytes:
    h = hmac.HMAC(key, hashes.SHA256())
    h.update(data)
    return h.finalize()


def prf_plus(key: bytes, seed: bytes, n: int) -> bytes:
    """RFC 7296 §2.13 verbatim:
       T1 = prf(K, S | 0x01)
       Ti = prf(K, T(i-1) | S | i)
       prf+(K,S) = T1 | T2 | T3 | ...
       Limited to 255 * out per §2.13."""
    out_size = 32  # SHA-256 output
    if n > 255 * out_size:
        raise ValueError("RFC 7296 §2.13: prf+ output > 255 * out")
    out = bytearray()
    T = b""
    i = 1
    while len(out) < n:
        T = prf_hmac_sha256(key, T + seed + bytes([i]))
        out += T
        i += 1
    return bytes(out[:n])


@dataclass
class IKESAKeys:
    SK_d: bytes
    SK_ai: bytes
    SK_ar: bytes
    SK_ei: bytes
    SK_er: bytes
    SK_pi: bytes
    SK_pr: bytes


def derive_ike_sa_keys(
    ni: bytes, nr: bytes, g_ir: bytes,
    spi_i: bytes, spi_r: bytes,
    integ_key_len: int, encr_key_len: int,
) -> IKESAKeys:
    """RFC 7296 §2.14:
        SKEYSEED = prf(Ni|Nr, g^ir)
        {SK_d|SK_ai|SK_ar|SK_ei|SK_er|SK_pi|SK_pr}
            = prf+(SKEYSEED, Ni|Nr|SPIi|SPIr)
    Lengths come from the negotiated transforms."""
    skeyseed = prf_hmac_sha256(ni + nr, g_ir)
    seed = ni + nr + spi_i + spi_r
    out_size = 32
    total = out_size + 2 * integ_key_len + 2 * encr_key_len + 2 * out_size
    stream = prf_plus(skeyseed, seed, total)
    off = 0
    def take(n):
        nonlocal off
        s = stream[off:off + n]
        off += n
        return s
    return IKESAKeys(
        SK_d=take(out_size),
        SK_ai=take(integ_key_len), SK_ar=take(integ_key_len),
        SK_ei=take(encr_key_len), SK_er=take(encr_key_len),
        SK_pi=take(out_size), SK_pr=take(out_size),
    )


# ─── §3.13 Traffic Selectors (TSi / TSr) ──────────────────────────

# RFC 7296 §3.13.1 verbatim TS Type table:
TS_IPV4_ADDR_RANGE = 7
TS_IPV6_ADDR_RANGE = 8

# IP Protocol ID = 0 means "any protocol" per §3.13.1.
IP_PROTOCOL_ANY = 0


@dataclass
class TrafficSelector:
    ts_type: int             # TS_IPV4_ADDR_RANGE / TS_IPV6_ADDR_RANGE
    ip_protocol: int         # 0 = any
    start_port: int
    end_port: int
    start_addr: bytes        # 4 octets for IPv4, 16 for IPv6
    end_addr: bytes

    def marshal(self) -> bytes:
        if self.ts_type == TS_IPV4_ADDR_RANGE:
            addr_len = 4
        elif self.ts_type == TS_IPV6_ADDR_RANGE:
            addr_len = 16
        else:
            raise ValueError(f"§3.13.1 unsupported TS Type {self.ts_type}")
        if len(self.start_addr) != addr_len or len(self.end_addr) != addr_len:
            raise ValueError(
                f"§3.13.1 address length must be {addr_len} for type {self.ts_type}"
            )
        # Selector Length = 8 (header) + 2*addr_len.
        sel_len = 8 + 2 * addr_len
        return (
            bytes([self.ts_type, self.ip_protocol])
            + struct.pack(">HHH", sel_len, self.start_port, self.end_port)
            + self.start_addr + self.end_addr
        )


def marshal_ts(selectors: List[TrafficSelector]) -> bytes:
    """RFC 7296 §3.13: 1 octet Num TSs | 3 octets RESERVED | TSs..."""
    if not selectors:
        raise ValueError("§3.13: TS payload MUST contain at least one selector")
    if len(selectors) > 255:
        raise ValueError("§3.13: Number of TSs is a single octet")
    out = bytes([len(selectors), 0, 0, 0])
    for s in selectors:
        out += s.marshal()
    return out


def parse_ts(buf: bytes) -> List[TrafficSelector]:
    if len(buf) < 4:
        raise ValueError("§3.13 TS payload header truncated")
    n = buf[0]
    # buf[1:4] RESERVED — ignore.
    selectors: List[TrafficSelector] = []
    off = 4
    for _ in range(n):
        if off + 8 > len(buf):
            raise ValueError("§3.13 TS substructure header truncated")
        ts_type = buf[off]
        ip_proto = buf[off + 1]
        sel_len = struct.unpack(">H", buf[off + 2:off + 4])[0]
        s_port = struct.unpack(">H", buf[off + 4:off + 6])[0]
        e_port = struct.unpack(">H", buf[off + 6:off + 8])[0]
        if off + sel_len > len(buf):
            raise ValueError("§3.13 TS Selector Length overruns")
        if ts_type == TS_IPV4_ADDR_RANGE:
            addr_len = 4
        elif ts_type == TS_IPV6_ADDR_RANGE:
            addr_len = 16
        else:
            raise ValueError(f"§3.13.1 unknown TS Type {ts_type}")
        if sel_len != 8 + 2 * addr_len:
            raise ValueError(
                f"§3.13 sel_len {sel_len} != expected {8 + 2 * addr_len} "
                f"for TS Type {ts_type}"
            )
        start = buf[off + 8:off + 8 + addr_len]
        end = buf[off + 8 + addr_len:off + 8 + 2 * addr_len]
        selectors.append(TrafficSelector(
            ts_type=ts_type, ip_protocol=ip_proto,
            start_port=s_port, end_port=e_port,
            start_addr=start, end_addr=end,
        ))
        off += sel_len
    if off != len(buf):
        raise ValueError("§3.13 TS payload has trailing octets")
    return selectors


# ─── §3.11 Delete Payload ─────────────────────────────────────────

# RFC 7296 §3.11 verbatim:
#   "If the Protocol ID is 1 (IKE), the SPI Size MUST be zero, and the
#    Number of SPIs MUST be zero, since the IKE SA is uniquely
#    identified by the Initiator's and Responder's SPIs in the IKE
#    header."
# For ESP and AH, SPI Size MUST be 4 (the ESP/AH SPI is 32 bits).


@dataclass
class Delete:
    protocol_id: int           # 1=IKE, 2=AH, 3=ESP per §3.3
    spi_size: int              # 0 for IKE, 4 for ESP/AH
    spis: List[bytes] = field(default_factory=list)

    def marshal(self) -> bytes:
        if self.protocol_id == PROTO_IKE:
            if self.spi_size != 0 or self.spis:
                raise ValueError("RFC 7296 §3.11: IKE Delete MUST carry no SPIs")
        else:
            for s in self.spis:
                if len(s) != self.spi_size:
                    raise ValueError(
                        f"§3.11 SPI len {len(s)} != declared {self.spi_size}"
                    )
        body = bytes([self.protocol_id, self.spi_size])
        body += struct.pack(">H", len(self.spis))
        for s in self.spis:
            body += s
        return body


def parse_delete(buf: bytes) -> Delete:
    if len(buf) < 4:
        raise ValueError("RFC 7296 §3.11 Delete header < 4 octets")
    proto = buf[0]
    spi_size = buf[1]
    n = struct.unpack(">H", buf[2:4])[0]
    if proto == PROTO_IKE:
        if spi_size != 0 or n != 0:
            raise ValueError("RFC 7296 §3.11: IKE Delete MUST have spi_size=0, num=0")
    elif proto in (PROTO_AH, PROTO_ESP):
        if spi_size != 4:
            raise ValueError(f"§3.11: AH/ESP SPI Size MUST be 4 (got {spi_size})")
    if 4 + n * spi_size != len(buf):
        raise ValueError("§3.11 Delete length mismatch")
    spis = [buf[4 + i * spi_size:4 + (i + 1) * spi_size] for i in range(n)]
    return Delete(protocol_id=proto, spi_size=spi_size, spis=spis)


# ─── §3.15 Configuration Payload ──────────────────────────────────

# RFC 7296 §3.15 verbatim CFG Type table:
CFG_REQUEST = 1
CFG_REPLY = 2
CFG_SET = 3
CFG_ACK = 4

# §3.15.1 Configuration Attribute types (subset relevant to N3IWF):
CP_INTERNAL_IP4_ADDRESS = 1
CP_INTERNAL_IP4_NETMASK = 2
CP_INTERNAL_IP4_DNS = 3
CP_INTERNAL_IP6_ADDRESS = 8
CP_INTERNAL_IP6_DNS = 10

# RFC 7296 §3.15.1: the high bit of the 16-bit attribute type field
# is RESERVED — implementations "MUST set it to zero on transmit and
# MUST ignore it on receive."
_CP_ATTR_TYPE_MASK = 0x7FFF


@dataclass
class CPAttribute:
    type: int
    value: bytes


@dataclass
class CP:
    cfg_type: int
    attrs: List[CPAttribute] = field(default_factory=list)

    def marshal(self) -> bytes:
        # 1 octet CFG Type + 3 octets RESERVED, then attributes.
        buf = bytearray([self.cfg_type, 0, 0, 0])
        for a in self.attrs:
            t = a.type & _CP_ATTR_TYPE_MASK  # zero high bit on transmit
            buf += struct.pack(">HH", t, len(a.value)) + a.value
        return bytes(buf)


def parse_cp(buf: bytes) -> CP:
    if len(buf) < 4:
        raise ValueError("RFC 7296 §3.15 CP header < 4 octets")
    cfg_type = buf[0]
    # buf[1:4] are RESERVED — ignore per §3.15.
    attrs: List[CPAttribute] = []
    off = 4
    while off < len(buf):
        if off + 4 > len(buf):
            raise ValueError("§3.15 attribute header truncated")
        raw_type, vlen = struct.unpack(">HH", buf[off:off + 4])
        atype = raw_type & _CP_ATTR_TYPE_MASK  # mask reserved high bit
        off += 4
        if off + vlen > len(buf):
            raise ValueError("§3.15 attribute value overruns")
        attrs.append(CPAttribute(type=atype, value=buf[off:off + vlen]))
        off += vlen
    return CP(cfg_type=cfg_type, attrs=attrs)


# ─── §2.17 KEYMAT for Child SAs ───────────────────────────────────


@dataclass
class ChildSAKeys:
    """RFC 7296 §2.17 verbatim:
        KEYMAT = prf+(SK_d, Ni | Nr)        — without PFS
        KEYMAT = prf+(SK_d, g^ir(new) | Ni | Nr)  — with PFS DH
    Order taken from KEYMAT (§2.17): encr_i, integ_i, encr_r, integ_r."""
    encr_i: bytes
    integ_i: bytes
    encr_r: bytes
    integ_r: bytes


def derive_child_sa_keys(
    sk_d: bytes,
    ni: bytes,
    nr: bytes,
    encr_key_len: int,
    integ_key_len: int,
    g_ir_new: bytes = b"",
) -> ChildSAKeys:
    """RFC 7296 §2.17 — derive a Child SA's two-direction key bundle.
    g_ir_new is empty unless CREATE_CHILD_SA negotiated PFS DH."""
    seed = g_ir_new + ni + nr if g_ir_new else ni + nr
    total = 2 * encr_key_len + 2 * integ_key_len
    stream = prf_plus(sk_d, seed, total)
    off = 0

    def take(n):
        nonlocal off
        s = stream[off:off + n]
        off += n
        return s

    return ChildSAKeys(
        encr_i=take(encr_key_len),
        integ_i=take(integ_key_len),
        encr_r=take(encr_key_len),
        integ_r=take(integ_key_len),
    )


# ─── §2.15 AUTH (shared-key MAC over signed octets) ───────────────

# RFC 7296 §2.15 verbatim:
#   "AUTH = prf( prf( Shared Secret, "Key Pad for IKEv2" ),
#                <InitiatorSignedOctets> )"
# where for shared-key auth the shared secret here is the EAP-derived
# K_N3IWF (a.k.a. Knh, TS 33.501 §6.5.2 — 32 octets).
KEY_PAD_IKEV2 = b"Key Pad for IKEv2"


def compute_auth_octets(
    shared_secret: bytes,
    real_message: bytes,
    nonce_peer: bytes,
    sk_p: bytes,
    id_body: bytes,
) -> bytes:
    """RFC 7296 §2.15 — produce the raw AUTH-data octets.

    For the *initiator* (UE→N3IWF):
        real_message = IKE_SA_INIT request bytes (RealMessage1)
        nonce_peer   = Nr (responder nonce from IKE_SA_INIT response)
        sk_p         = SK_pi
        id_body      = IDi-body (id_type | reserved×3 | id_data)

    For the *responder*, swap: RealMessage2 / Ni / SK_pr / IDr-body.
    Returns the 32-octet PRF output, ready to drop into an AUTH
    payload after the 4-octet (Auth Method | RESERVED) header per §3.8.
    """
    inner = prf_hmac_sha256(shared_secret, KEY_PAD_IKEV2)
    maced_id = prf_hmac_sha256(sk_p, id_body)
    return prf_hmac_sha256(inner, real_message + nonce_peer + maced_id)


# ─── §3.14 Encrypted (SK) — AES-CBC + HMAC-SHA256-128 ──────────────


_BLOCK = 16
_ICV = 16  # HMAC-SHA-256 truncated to 128 bits per RFC 4868 §2.1


def _hmac_sha256_128(key: bytes, data: bytes) -> bytes:
    h = hmac.HMAC(key, hashes.SHA256())
    h.update(data)
    return h.finalize()[:_ICV]


def _aes_cbc_encrypt(key: bytes, iv: bytes, plaintext: bytes) -> bytes:
    c = Cipher(algorithms.AES(key), modes.CBC(iv)).encryptor()
    return c.update(plaintext) + c.finalize()


def _aes_cbc_decrypt(key: bytes, iv: bytes, ciphertext: bytes) -> bytes:
    c = Cipher(algorithms.AES(key), modes.CBC(iv)).decryptor()
    return c.update(ciphertext) + c.finalize()


def encrypted_message(
    hdr: Header, preceding: List[Payload], inner: List[Payload],
    encr_key: bytes, integ_key: bytes,
) -> bytes:
    """Build a complete §3.14 SK-wrapped IKEv2 message.

    - encr_key: SK_e for this direction (UE→N3IWF uses SK_ei).
    - integ_key: SK_a for this direction.
    - Pads inner_bytes||pad||pad_length to multiple of 16 octets,
      pad bytes = zero (§3.14 "MAY contain any value").
    - ICV covers IKE header || preceding bytes || SK generic header
      || IV || ciphertext (§3.14 verbatim).
    """
    if not inner:
        raise ValueError("RFC 7296 §3.14: at least one inner payload required")
    inner_bytes, first_inner = marshal_payloads(inner)

    pad_len = _BLOCK - ((len(inner_bytes) + 1) % _BLOCK)
    if pad_len == _BLOCK:
        pad_len = 0
    plaintext = inner_bytes + bytes(pad_len) + bytes([pad_len])

    iv = os.urandom(_BLOCK)
    ciphertext = _aes_cbc_encrypt(encr_key, iv, plaintext)

    sk_body_len = len(iv) + len(ciphertext) + _ICV
    sk_payload_len = PAYLOAD_HEADER_LEN + sk_body_len
    sk_generic = bytes([first_inner, 0]) + struct.pack(">H", sk_payload_len)

    if preceding:
        preceding_bytes = bytearray()
        for i, p in enumerate(preceding):
            nxt = preceding[i + 1].type if i + 1 < len(preceding) else PAYLOAD_SK
            pl_len = PAYLOAD_HEADER_LEN + len(p.data)
            preceding_bytes += bytes([nxt, 0x80 if p.critical else 0])
            preceding_bytes += struct.pack(">H", pl_len) + p.data
        first_next = preceding[0].type
    else:
        preceding_bytes = b""
        first_next = PAYLOAD_SK

    hdr.next_payload = first_next
    hdr.length = HEADER_LEN + len(preceding_bytes) + sk_payload_len
    hdr_bytes = hdr.marshal()

    icv_input = hdr_bytes + bytes(preceding_bytes) + sk_generic + iv + ciphertext
    icv = _hmac_sha256_128(integ_key, icv_input)

    return hdr_bytes + bytes(preceding_bytes) + sk_generic + iv + ciphertext + icv


def decrypt_message(
    msg: bytes, encr_key: bytes, integ_key: bytes,
) -> Tuple[Header, List[Payload], List[Payload]]:
    """Inverse of encrypted_message. Verifies ICV in constant time
    before decrypting (compare_digest)."""
    hdr = parse_header(msg)
    if hdr.length != len(msg):
        raise ValueError(f"RFC 7296 §3.1 length mismatch: hdr={hdr.length} actual={len(msg)}")

    # Walk chain manually because §3.14 places the inner-first type
    # in SK's NextPayload field — the generic walker would keep
    # going past SK looking for a next payload.
    preceding = []
    off = HEADER_LEN
    cur = hdr.next_payload
    sk_off = -1
    sk_body = b""
    while cur != PAYLOAD_NONE:
        if off + PAYLOAD_HEADER_LEN > len(msg):
            raise ValueError(f"chain header truncated at {off}")
        nxt = msg[off]
        pl_len = struct.unpack(">H", msg[off + 2:off + 4])[0]
        if pl_len < PAYLOAD_HEADER_LEN or off + pl_len > len(msg):
            raise ValueError(f"bad payload length {pl_len} at {off}")
        if cur == PAYLOAD_SK:
            sk_off = off
            sk_body = msg[off + PAYLOAD_HEADER_LEN:off + pl_len]
            break
        preceding.append(Payload(type=cur, data=msg[off + PAYLOAD_HEADER_LEN:off + pl_len]))
        off += pl_len
        cur = nxt
    if sk_off < 0:
        raise ValueError("RFC 7296 §3.14: SK payload absent")

    sk_generic = msg[sk_off:sk_off + PAYLOAD_HEADER_LEN]

    if len(sk_body) < _BLOCK + _ICV:
        raise ValueError("SK body too short for IV+ICV")
    iv = sk_body[:_BLOCK]
    ct = sk_body[_BLOCK:-_ICV]
    icv = sk_body[-_ICV:]
    if len(ct) % _BLOCK != 0:
        raise ValueError("SK ciphertext not block-aligned")

    icv_input = msg[:HEADER_LEN] + msg[HEADER_LEN:sk_off] + sk_generic + iv + ct
    want_icv = _hmac_sha256_128(integ_key, icv_input)
    import hmac as stdhmac
    if not stdhmac.compare_digest(want_icv, icv):
        raise ValueError("RFC 7296 §3.14: ICV mismatch")

    pt = _aes_cbc_decrypt(encr_key, iv, ct)
    if not pt:
        raise ValueError("plaintext empty")
    pad_length = pt[-1]
    if pad_length + 1 > len(pt):
        raise ValueError(f"SK pad length {pad_length} > plaintext")
    inner_bytes = pt[:-pad_length - 1]
    inner_first = sk_generic[0]
    inner_payloads = parse_payloads(inner_bytes, inner_first)
    return hdr, preceding, inner_payloads


# ─── Convenience: full IKE_SA_INIT request from a UE ───────────────


def build_ike_sa_init_request(spi_i: bytes, dh: DH_MODP2048, nonce: bytes) -> bytes:
    """Synthesise an IKE_SA_INIT initiator request — UE→N3IWF.
    Returns the wire bytes."""
    sa = SA(proposals=[default_ike_proposal()])
    pls = [
        Payload(type=PAYLOAD_SA, data=sa.marshal()),
        Payload(type=PAYLOAD_KE, data=marshal_ke(DH_MODP_2048, dh.public)),
        Payload(type=PAYLOAD_NONCE, data=nonce),
    ]
    body, first = marshal_payloads(pls)
    hdr = Header(
        spi_i=spi_i, spi_r=b"\x00" * 8,  # §3.1: zero in initial request
        next_payload=first, exchange_type=EXCHANGE_IKE_SA_INIT,
        flags=FLAG_INITIATOR, message_id=0,
        length=HEADER_LEN + len(body),
    )
    return hdr.marshal() + body

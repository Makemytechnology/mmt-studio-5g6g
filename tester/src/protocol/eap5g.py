# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""EAP-5G client encoder for the N3IWF UE simulator.

Authoritative spec: TS 24.502 v19.3.0 §9.3.2 "EAP-5G method"
(PDF: specs/3gpp/ts_124502v190300p.pdf). Mirrors the Go encoder at
nf/n3iwf/eap5g/.

§9.3.2.2 Message format (verbatim):
  - Code is 1 (Request) / 2 (Response) per RFC 3748 §4.1.
  - Type is 254 (Expanded) per RFC 3748 §5.7.
  - Vendor-Id is 3GPP IANA SMI PEN 10415 (decimal), encoded as 3
    octets big-endian = 00 28 AF.
  - Vendor-Type is the EAP-5G method id 3 (TS 33.402 annex C).
  - Message-Id values per the §9.3.2.2.x tables:
        5G-Start-Id        1   §9.3.2.2.1
        5G-NAS-Id          2   §9.3.2.2.2 / §9.3.2.2.3
        5G-Notification-Id 3   §9.3.2.2.5 / §9.3.2.2.6
        5G-Stop-Id         4   §9.3.2.2.4
"""

import struct
from dataclasses import dataclass, field
from typing import List, Optional


CODE_REQUEST = 1
CODE_RESPONSE = 2
CODE_SUCCESS = 3
CODE_FAILURE = 4

TYPE_EXPANDED = 254
VENDOR_ID_3GPP = 10415  # § 9.3.2.2.x — wire bytes 00 28 AF
VENDOR_TYPE_EAP5G = 3

MSG_ID_START = 1
MSG_ID_NAS = 2
MSG_ID_NOTIFICATION = 3
MSG_ID_STOP = 4


# §9.3.2.2.2 AN-parameter type values (UE→N3IWF in 5G-NAS Response):
AN_PARAM_GUAMI = 0x01
AN_PARAM_SELECTED_PLMN = 0x02
AN_PARAM_REQUESTED_NSSAI = 0x03
AN_PARAM_ESTABLISHMENT_CAUSE = 0x04
AN_PARAM_SELECTED_NID = 0x05

EXT_AN_PARAM_UE_IDENTITY = 0x06


def _fixed_prefix(code: int, identifier: int) -> bytes:
    """The 12-octet RFC 3748 §5.7 expanded EAP prefix:
        Code (1) | Identifier (1) | Length (2 — patched later) |
        Type (1) | Vendor-Id (3) | Vendor-Type (4)
    Vendor-Id is encoded as 3 octets big-endian (RFC 3748 §5.7)."""
    return (bytes([code, identifier, 0, 0, TYPE_EXPANDED])
            + bytes([(VENDOR_ID_3GPP >> 16) & 0xFF,
                     (VENDOR_ID_3GPP >> 8) & 0xFF,
                     VENDOR_ID_3GPP & 0xFF])
            + struct.pack(">I", VENDOR_TYPE_EAP5G))


def _patch_length(buf: bytearray) -> bytes:
    """Patch Length field (RFC 3748 §4.1, covers entire packet)."""
    if len(buf) > 0xFFFF:
        raise ValueError(f"EAP packet too long: {len(buf)}")
    buf[2:4] = struct.pack(">H", len(buf))
    return bytes(buf)


def encode_an_parameters(params: List[tuple]) -> bytes:
    """params is a list of (type:int, value:bytes) — encoded per
    §9.3.2.2.2 figure 9.3.2.2.2-3:
        AN-parameter type (1) | AN-parameter length (1) | value (var)
    """
    out = bytearray()
    for t, v in params:
        if len(v) > 255:
            raise ValueError(f"AN-parameter value > 255 octets (type 0x{t:02x})")
        out += bytes([t, len(v)]) + v
    return bytes(out)


# ─── Builders (UE side — Response messages and ack) ───────────────


def build_5g_nas_response(
    identifier: int,
    nas_pdu: bytes,
    an_params: bytes = b"",
    ext_an_params: Optional[bytes] = None,
) -> bytes:
    """EAP-Response/5G-NAS message — UE → N3IWF (§9.3.2.2.2).

    Wire layout:
        Code(1=Response) | Id | Length(2) | Type(254) |
        Vendor-Id(3) | Vendor-Type(4) | Message-Id(2=NAS) |
        Spare(1) | AN-params length(2) | AN-params |
        NAS-PDU length(2) | NAS-PDU |
        [ Extended-AN-params length(2) | Extended-AN-params ]
    """
    buf = bytearray(_fixed_prefix(CODE_RESPONSE, identifier))
    buf += bytes([MSG_ID_NAS, 0])  # Message-Id, Spare
    buf += struct.pack(">H", len(an_params)) + an_params
    buf += struct.pack(">H", len(nas_pdu)) + nas_pdu
    if ext_an_params is not None:
        buf += struct.pack(">H", len(ext_an_params)) + ext_an_params
    return _patch_length(buf)


def build_5g_stop_response(identifier: int) -> bytes:
    """EAP-Response/5G-Stop (§9.3.2.2.4)."""
    buf = bytearray(_fixed_prefix(CODE_RESPONSE, identifier))
    buf += bytes([MSG_ID_STOP, 0])  # Message-Id, Spare
    return _patch_length(buf)


def build_5g_notification_response(identifier: int, an_params: bytes = b"") -> bytes:
    """EAP-Response/5G-Notification (§9.3.2.2.6)."""
    buf = bytearray(_fixed_prefix(CODE_RESPONSE, identifier))
    buf += bytes([MSG_ID_NOTIFICATION, 0])  # Message-Id, Spare
    buf += struct.pack(">H", len(an_params)) + an_params
    return _patch_length(buf)


# ─── Parser (network → UE direction — Request messages) ───────────


@dataclass
class Request:
    code: int
    identifier: int
    message_id: int
    nas_pdu: bytes = b""
    an_params: bytes = b""
    extensions: bytes = b""


def parse(buf: bytes) -> Request:
    """Decode an EAP-Request/5G-* message (network → UE). Validates
    Type / Vendor-Id / Length per RFC 3748 §4.1, §5.7 and TS 24.502
    §9.3.2.2.x. Returns the raw NAS-PDU and AN-params bytes — caller
    decodes those further."""
    if len(buf) < 13:
        raise ValueError(f"EAP-5G packet shorter than 13 octets ({len(buf)})")
    declared = struct.unpack(">H", buf[2:4])[0]
    if declared != len(buf):
        raise ValueError(f"EAP Length {declared} != actual {len(buf)} (RFC 3748 §4.1)")
    if buf[4] != TYPE_EXPANDED:
        raise ValueError(f"EAP Type {buf[4]} != 254 (Expanded, RFC 3748 §5.7)")
    vid = (buf[5] << 16) | (buf[6] << 8) | buf[7]
    if vid != VENDOR_ID_3GPP:
        raise ValueError(f"Vendor-Id {vid} != {VENDOR_ID_3GPP} (TS 24.502 §9.3.2.2.x)")
    vt = struct.unpack(">I", buf[8:12])[0]
    if vt != VENDOR_TYPE_EAP5G:
        raise ValueError(f"Vendor-Type {vt} != {VENDOR_TYPE_EAP5G} (TS 24.502 §9.3.2.2.x)")
    req = Request(code=buf[0], identifier=buf[1], message_id=buf[12])
    body = buf[13:]
    if req.message_id == MSG_ID_START:
        # §9.3.2.2.1: Spare(1) | Extensions(opt)
        if len(body) < 1:
            raise ValueError("5G-Start: missing Spare")
        req.extensions = body[1:]
    elif req.message_id == MSG_ID_NAS:
        # §9.3.2.2.3 (network → UE):
        #   Spare(1) | NAS-PDU length(2) | NAS-PDU | Extensions
        if len(body) < 3:
            raise ValueError("5G-NAS request: short header")
        nas_len = struct.unpack(">H", body[1:3])[0]
        if 3 + nas_len > len(body):
            raise ValueError(f"NAS-PDU length {nas_len} overruns packet")
        req.nas_pdu = body[3:3 + nas_len]
        req.extensions = body[3 + nas_len:]
    elif req.message_id == MSG_ID_NOTIFICATION:
        # §9.3.2.2.5: Spare(1) | AN-params length(2) | AN-params | Extensions
        if len(body) < 3:
            raise ValueError("5G-Notification request: short header")
        an_len = struct.unpack(">H", body[1:3])[0]
        if 3 + an_len > len(body):
            raise ValueError(f"AN-params length {an_len} overruns packet")
        req.an_params = body[3:3 + an_len]
        req.extensions = body[3 + an_len:]
    return req

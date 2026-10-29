# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Pure SMS-over-NAS codec — no I/O, no AMF state.

Mirrors nf/smsf/codec_decode.go and nf/smsf/smsf.go byte-exactly so
fixtures built by either side parse on the other. This module is the
wire-format-only path that unit tests can import without pulling in
the live NAS / NGAP / SCTP machinery.

The MO-SMS-over-NAS payload is a nested TLV stack:

    Payload Container (TS 24.501 §9.11.3.39, type=SMS=2)
    └── CP-DATA / CP-ACK / CP-ERROR (TS 24.011 §8.1)
        └── RP-DATA / RP-ACK / RP-ERROR (TS 24.011 §8.2)
            └── SMS-SUBMIT / SMS-DELIVER TPDU (TS 23.040 §9.2.2)
                └── TP-User-Data (TS 23.040 §9.2.3.24,
                    encoded per TS 23.038 §6.1.2 / §6.2)

Every public symbol cites the §clause it implements. Unimplemented
branches carry TS-numbered TODOs so a future audit can grep the doc
ID and find every gap.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass, field


# ============================================================
# CP-Layer constants — TS 24.011 §8.1.3
# ============================================================
CP_DATA = 0x01
CP_ACK = 0x04
CP_ERROR = 0x10

# ============================================================
# RP-Layer constants — TS 24.011 §8.2.2 Table 8.4
# ============================================================
RP_DATA_MS_TO_NET = 0x00
RP_DATA_NET_TO_MS = 0x01
RP_ACK_MS_TO_NET = 0x02
RP_ACK_NET_TO_MS = 0x03
RP_ERROR_MS_TO_NET = 0x04
RP_ERROR_NET_TO_MS = 0x05

# Protocol Discriminator for SMS messages — TS 24.007 §11.2.3.1.1 Table 11.2
PD_SMS = 0x09


# ================================================================
# TP-Address codec — TS 23.040 §9.1.2.5
# ================================================================

def encode_tp_address(msisdn: str) -> bytes:
    """Encode a TP-DA / TP-OA address per TS 23.040 §9.1.2.5.

    Layout: numDigits(1) + TOA(1) + BCD digits with 0xF fill nibble
    when the digit count is odd. Note this is the TPDU address form;
    the RP-OA / RP-DA address form (TS 24.011 §8.2.5.1) does NOT
    carry the leading numDigits octet — see encode_rp_address.
    """
    intl = msisdn.startswith("+")
    digits = msisdn.lstrip("+")
    toa = 0x91 if intl else 0x81  # TON=Intl/Unknown, NPI=ISDN/E.164
    bcd = bytearray()
    for i in range(0, len(digits), 2):
        d1 = int(digits[i]) & 0x0F
        d2 = int(digits[i + 1]) & 0x0F if i + 1 < len(digits) else 0x0F
        bcd.append((d2 << 4) | d1)
    return bytes([len(digits), toa]) + bytes(bcd)


def decode_tp_address(data: bytes, offset: int = 0) -> tuple[str, int]:
    """Decode TP-DA / TP-OA per TS 23.040 §9.1.2.5.

    Returns (msisdn, bytes_consumed).
    """
    num_digits = data[offset]
    toa = data[offset + 1]
    bcd_bytes = (num_digits + 1) // 2
    bcd = data[offset + 2 : offset + 2 + bcd_bytes]

    digits = []
    for b in bcd:
        lo, hi = b & 0x0F, (b >> 4) & 0x0F
        if lo <= 9:
            digits.append(str(lo))
        if hi <= 9:
            digits.append(str(hi))
    msisdn = "".join(digits[:num_digits])
    if (toa >> 4) & 0x07 == 0x01:  # TON=International
        msisdn = "+" + msisdn
    return msisdn, 2 + bcd_bytes


# ================================================================
# RP-Address codec — TS 24.011 §8.2.5.1 / §8.2.5.2
# ================================================================

def encode_rp_address(msisdn: str) -> bytes:
    """Encode the *contents* of an RP-OA / RP-DA element per
    TS 24.011 §8.2.5.1 (Figure 8.5).

    The caller must prepend the length octet (= len of the bytes
    returned here). Returns b"" for an empty address; caller emits
    a single 0x00 length byte in that case.

    Layout: TOA(1) + BCD digits with 0xF fill nibble when odd.
    Crucially this OMITS the "number of digits" octet that the
    TPDU address form carries — the digit count is implicit in
    the LV length per §8.2.5.1.
    """
    if not msisdn:
        return b""
    intl = msisdn.startswith("+")
    digits = msisdn.lstrip("+")
    toa = 0x91 if intl else 0x81
    bcd = bytearray()
    for i in range(0, len(digits), 2):
        d1 = int(digits[i]) & 0x0F
        d2 = int(digits[i + 1]) & 0x0F if i + 1 < len(digits) else 0x0F
        bcd.append((d2 << 4) | d1)
    return bytes([toa]) + bytes(bcd)


def decode_rp_address(contents: bytes) -> str:
    """Decode the contents of an RP-OA / RP-DA element per
    TS 24.011 §8.2.5.1. ``contents`` is the value field after the
    length-octet has already been stripped by the caller.
    """
    if not contents:
        return ""
    toa = contents[0]
    bcd = contents[1:]
    digits = []
    for b in bcd:
        lo, hi = b & 0x0F, (b >> 4) & 0x0F
        if lo <= 9:
            digits.append(str(lo))
        if hi <= 9:
            digits.append(str(hi))
    msisdn = "".join(digits)
    if (toa >> 4) & 0x07 == 0x01:
        msisdn = "+" + msisdn
    return msisdn


# ================================================================
# CP-Layer encode / decode — TS 24.011 §8.1
# ================================================================

@dataclass
class CPMessage:
    ti: int = 0          # TS 24.011 §8.1.2 Transaction Identifier
    msg_type: int = 0    # TS 24.011 §8.1.3
    user_data: bytes = b""  # CP-User data (RP-PDU bytes) — §8.1.4.1
    cause: int = 0       # CP-Cause octet — §8.1.4.2 (CP-ERROR only)


def encode_cp_data(ti: int, rp_pdu: bytes) -> bytes:
    """Encode a CP-DATA PDU wrapping an RP-PDU per TS 24.011 §7.2.1.

    Layout: octet1 = TI<<4 | PD(0x09); octet2 = MsgType(0x01);
    octet3 = LengthOf(RP-PDU); octets 4.. = RP-PDU bytes.
    """
    pd_ti = ((ti & 0x0F) << 4) | PD_SMS
    return bytes([pd_ti, CP_DATA, len(rp_pdu)]) + rp_pdu


def encode_cp_ack(ti: int) -> bytes:
    """Encode a CP-ACK PDU per TS 24.011 §7.2.2 (no body)."""
    return bytes([((ti & 0x0F) << 4) | PD_SMS, CP_ACK])


def encode_cp_error(ti: int, cause: int) -> bytes:
    """Encode a CP-ERROR PDU per TS 24.011 §7.2.3 with a CP-Cause
    octet (§8.1.4.2)."""
    return bytes([((ti & 0x0F) << 4) | PD_SMS, CP_ERROR, cause & 0xFF])


def decode_cp(data: bytes) -> CPMessage:
    """Parse a CP-layer PDU per TS 24.011 §8.1.

    Layout per §7.2 / Figure 8.1:
        octet 1: TI (high nibble) | PD=0x9 (low nibble) — §8.1.2
        octet 2: Message type — §8.1.3
        octet 3..: type-dependent body (CP-DATA → CP-User data IE,
                   CP-ACK → empty, CP-ERROR → CP-Cause octet).
    """
    if len(data) < 2:
        raise ValueError(f"CP PDU too short: {len(data)} bytes")
    pd_ti = data[0]
    if pd_ti & 0x0F != PD_SMS:
        raise ValueError(
            f"CP: bad protocol discriminator 0x{pd_ti & 0x0F:X} (want 0x9)")
    msg = CPMessage(ti=(pd_ti >> 4) & 0x0F, msg_type=data[1])
    if msg.msg_type == CP_DATA:
        # CP-User data IE per §8.1.4.1: length(1) + RP-PDU.
        if len(data) < 3:
            raise ValueError("CP-DATA: missing CP-User data IE")
        ud_len = data[2]
        if len(data) < 3 + ud_len:
            raise ValueError(
                f"CP-DATA: declared len {ud_len} > remaining {len(data) - 3}")
        msg.user_data = bytes(data[3 : 3 + ud_len])
    elif msg.msg_type == CP_ACK:
        pass  # No body — §7.2.2
    elif msg.msg_type == CP_ERROR:
        if len(data) < 3:
            raise ValueError("CP-ERROR: missing CP-Cause")
        msg.cause = data[2]
    else:
        # TODO(spec: TS 24.011 §8.1.3): unknown msg-types should be
        # answered with CP-ERROR cause=97 ("message type non-existent
        # or not implemented") per §6.4. We currently fail the decode
        # instead of generating that response.
        raise ValueError(f"CP: unknown message type 0x{msg.msg_type:02X}")
    return msg


# ================================================================
# RP-Layer encode / decode — TS 24.011 §8.2
# ================================================================

@dataclass
class RPMessage:
    mti: int = 0
    reference: int = 0
    oa: str = ""        # RP-Originator Address — §8.2.5.1
    da: str = ""        # RP-Destination Address — §8.2.5.2
    user_data: bytes = b""  # RP-User data IE — §8.2.5.3 (the TPDU)
    cause: int = 0      # RP-Cause — §8.2.5.4 (RP-ERROR only)


def encode_rp_data_ms_to_net(reference: int, smsc: str, tpdu: bytes) -> bytes:
    """Encode an RP-DATA MS→Net per TS 24.011 §7.3.1.2.

    Layout (§8.2):
        MTI(1) | Ref(1) | RP-OA-len(0) | RP-DA-LV (smsc) |
        RP-UD-len(1) | TPDU
    """
    out = bytearray([RP_DATA_MS_TO_NET, reference & 0xFF, 0x00])
    if smsc:
        da = encode_rp_address(smsc)
        out.append(len(da))
        out += da
    else:
        out.append(0x00)
    out.append(len(tpdu))
    out += tpdu
    return bytes(out)


def encode_rp_data_net_to_ms(reference: int, smsc: str, tpdu: bytes) -> bytes:
    """Encode an RP-DATA Net→MS per TS 24.011 §7.3.1.1.

    Layout: MTI(1) | Ref(1) | RP-OA-LV (smsc) | RP-DA-len(0) |
    RP-UD-len(1) | TPDU
    """
    out = bytearray([RP_DATA_NET_TO_MS, reference & 0xFF])
    if smsc:
        oa = encode_rp_address(smsc)
        out.append(len(oa))
        out += oa
    else:
        out.append(0x00)
    out.append(0x00)  # empty RP-DA per §8.2.5.2 (Net→MS).
    out.append(len(tpdu))
    out += tpdu
    return bytes(out)


def encode_rp_ack(reference: int, *, net_to_ms: bool = False) -> bytes:
    """Encode an RP-ACK per TS 24.011 §7.3.3 (bare; no Status-Report)."""
    mti = RP_ACK_NET_TO_MS if net_to_ms else RP_ACK_MS_TO_NET
    return bytes([mti, reference & 0xFF])


def encode_rp_error(reference: int, cause: int, *, net_to_ms: bool = False) -> bytes:
    """Encode an RP-ERROR per TS 24.011 §7.3.4 with RP-Cause LV
    (length=1 | cause-octet) per §8.2.5.4. Diagnostic field omitted.
    """
    mti = RP_ERROR_NET_TO_MS if net_to_ms else RP_ERROR_MS_TO_NET
    return bytes([mti, reference & 0xFF, 0x01, cause & 0x7F])


def decode_rp(data: bytes) -> RPMessage:
    """Parse an RP-layer PDU per TS 24.011 §8.2 (matches the Go
    side's smsf.DecodeRP). RP-DATA layout:

        MTI | Ref | RP-OA-len | RP-OA-value? |
        RP-DA-len | RP-DA-value? | RP-UD-len | RP-UD
    """
    if len(data) < 2:
        raise ValueError(f"RP PDU too short: {len(data)} bytes")
    msg = RPMessage(mti=data[0], reference=data[1])
    if msg.mti in (RP_DATA_MS_TO_NET, RP_DATA_NET_TO_MS):
        off = 2
        # RP-OA per §8.2.5.1
        oa_len = data[off]; off += 1
        if off + oa_len > len(data):
            raise ValueError("RP-DATA: RP-OA length exceeds buffer")
        if oa_len > 0:
            msg.oa = decode_rp_address(bytes(data[off : off + oa_len]))
        off += oa_len
        # RP-DA per §8.2.5.2
        da_len = data[off]; off += 1
        if off + da_len > len(data):
            raise ValueError("RP-DATA: RP-DA length exceeds buffer")
        if da_len > 0:
            msg.da = decode_rp_address(bytes(data[off : off + da_len]))
        off += da_len
        # RP-User data per §8.2.5.3
        ud_len = data[off]; off += 1
        if off + ud_len > len(data):
            raise ValueError("RP-DATA: RP-UD length exceeds buffer")
        msg.user_data = bytes(data[off : off + ud_len])
    elif msg.mti in (RP_ACK_MS_TO_NET, RP_ACK_NET_TO_MS):
        # TODO(spec: TS 24.011 §8.2.5.3): RP-ACK *may* include an
        # RP-User data IE carrying a Status-Report TPDU. Bare ACKs
        # only here.
        pass
    elif msg.mti in (RP_ERROR_MS_TO_NET, RP_ERROR_NET_TO_MS):
        if len(data) < 4:
            raise ValueError("RP-ERROR truncated")
        cause_len = data[2]
        if cause_len >= 1 and 3 + cause_len <= len(data):
            msg.cause = data[3] & 0x7F
    else:
        # TODO(spec: TS 24.011 §8.2.2): RP-SMMA (MTI=4) and reserved
        # MTIs not handled; RP-SMMA would relay UE memory-available
        # to the SMS-GMSC per TS 23.040 §10.2.
        raise ValueError(f"RP: unknown MTI 0x{msg.mti:02X}")
    return msg


# ================================================================
# TPDU layer — SMS-SUBMIT (TS 23.040 §9.2.2.2)
# ================================================================

@dataclass
class SMSSubmitTPDU:
    udhi: bool = False        # TP-UDHI — TS 23.040 §9.2.3.23
    srr: bool = False         # TP-Status-Report-Request — §9.2.3.5
    vpf: int = 0              # TP-VPF — §9.2.3.3
    reference: int = 0        # TP-MR — §9.2.3.6
    da_msisdn: str = ""       # TP-DA — §9.2.3.8
    pid: int = 0              # TP-PID — §9.2.3.9
    dcs: int = 0              # TP-DCS — §9.2.3.10
    udl: int = 0              # TP-UDL — §9.2.3.16
    udh: bytes = b""          # TP-UDH IEDs — §9.2.3.24
    ud: bytes = b""           # TP-UD body after UDH stripped — §9.2.3.24
    encoding: str = ""        # "gsm7" | "8bit" | "ucs2" — TS 23.038 §4


def encode_sms_submit(*, mr: int, da_msisdn: str, text: str,
                      encoding: str = "gsm7", udh: bytes = b"") -> bytes:
    """Encode an SMS-SUBMIT TPDU per TS 23.040 §9.2.2.2 Table 9.2.2.2-1.

    Defaults: TP-VPF=0 (no VP), TP-PID=0 (default), TP-DCS=0/8 derived
    from ``encoding``. Use ``udh`` for concatenated SMS UDH IEDs (the
    UDHL byte is added automatically).
    """
    first = 0x01  # MTI=01 (SMS-SUBMIT, MS→SC), VPF=00, no UDHI/SRR/RP
    if udh:
        first |= 0x40  # TP-UDHI
    tp_da = encode_tp_address(da_msisdn)
    tp_pid = 0x00
    tp_dcs = 0x08 if encoding == "ucs2" else 0x00

    if encoding == "ucs2":
        ud_bytes = text.encode("utf-16-be")
        if udh:
            udh_block = bytes([len(udh)]) + udh
            ud_payload = udh_block + ud_bytes
            tp_udl = len(ud_payload)
        else:
            ud_payload = ud_bytes
            tp_udl = len(ud_bytes)
    else:
        # GSM 7-bit per TS 23.038 §6.1.2.1 — straight ASCII subset
        # mapped 1:1 to default-alphabet septets, then packed.
        # TODO(spec: TS 23.038 §6.2.1): full default-alphabet table
        # plus extension table. We currently support 7-bit ASCII only;
        # any non-ASCII char turns into '?' in the packed stream.
        septets = bytes((b if b < 0x80 else ord('?')) for b in text.encode("ascii", "replace"))
        num_septets = len(septets)
        packed = _gsm7_pack(septets)
        if udh:
            udh_block = bytes([len(udh)]) + udh
            udh_octets = len(udh_block)
            fill_bits = (7 - ((udh_octets * 8) % 7)) % 7
            tp_udl = num_septets + ((udh_octets * 8 + fill_bits) // 7)
            pad = b"\x00" * ((fill_bits + 7) // 8)
            ud_payload = udh_block + pad + packed
        else:
            ud_payload = packed
            tp_udl = num_septets

    return bytes([first, mr & 0xFF]) + tp_da + bytes([tp_pid, tp_dcs, tp_udl & 0xFF]) + ud_payload


def decode_sms_submit(data: bytes) -> SMSSubmitTPDU:
    """Decode an SMS-SUBMIT TPDU per TS 23.040 §9.2.2.2."""
    if len(data) < 7:
        raise ValueError(f"SMS-SUBMIT too short: {len(data)} bytes")
    first = data[0]
    mti = first & 0x03
    if mti != 0x01:
        raise ValueError(f"SMS-SUBMIT: TP-MTI={mti} (want 1)")
    msg = SMSSubmitTPDU(
        udhi=bool(first & 0x40),
        srr=bool(first & 0x20),
        vpf=(first >> 3) & 0x03,
        reference=data[1],
    )
    da, da_bytes = decode_tp_address(data, 2)
    if da_bytes == 0:
        raise ValueError("SMS-SUBMIT: malformed TP-DA")
    msg.da_msisdn = da
    off = 2 + da_bytes
    if off + 2 > len(data):
        raise ValueError("SMS-SUBMIT: truncated at TP-PID/TP-DCS")
    msg.pid = data[off]; off += 1
    msg.dcs = data[off]; off += 1
    # TP-VP per §9.2.3.12
    if msg.vpf == 0x00:
        pass
    elif msg.vpf == 0x02:
        # TODO(spec: TS 23.040 §9.2.3.12.1): decode relative VP into
        # a duration. Skipped octet for now.
        off += 1
    elif msg.vpf in (0x01, 0x03):
        # TODO(spec: TS 23.040 §9.2.3.12.2 / §9.2.3.12.3): decode
        # absolute / enhanced VP forms.
        off += 7
    if off >= len(data):
        raise ValueError("SMS-SUBMIT: truncated at TP-UDL")
    msg.udl = data[off]; off += 1
    # Encoding from DCS per TS 23.038 §4 Table 4-1 (General Data Coding)
    grp = (msg.dcs >> 4) & 0x0F
    if grp & 0x0C == 0x00:  # General Data Coding (top nibble 00xx)
        msg.encoding = {0: "gsm7", 1: "8bit", 2: "ucs2"}.get(
            (msg.dcs >> 2) & 0x03, "8bit")
    else:
        # TODO(spec: TS 23.038 §4): Message Waiting Indication and
        # Data coding/message classes are not yet decoded.
        msg.encoding = "8bit"
    body = bytes(data[off:])
    if msg.udhi and body:
        udhl = body[0]
        if 1 + udhl > len(body):
            raise ValueError(
                f"SMS-SUBMIT: TP-UDH length {udhl} exceeds remainder {len(body) - 1}")
        msg.udh = body[1 : 1 + udhl]
        msg.ud = body[1 + udhl:]
    else:
        msg.ud = body
    return msg


# ================================================================
# GSM 7-bit packing helpers — TS 23.038 §6.1.2.1.1
# ================================================================

def _gsm7_pack(septets: bytes) -> bytes:
    """Pack a sequence of 7-bit septets into octets per
    TS 23.038 §6.1.2.1.1 ("Packing of 7-bit characters")."""
    out = bytearray()
    shift = 0
    for i, s in enumerate(septets):
        if shift == 7:
            shift = 0
            continue
        octet = s >> shift
        if i + 1 < len(septets):
            octet |= (septets[i + 1] << (7 - shift)) & 0xFF
        out.append(octet & 0xFF)
        shift += 1
    return bytes(out)


def _gsm7_unpack(packed: bytes, num_septets: int) -> bytes:
    """Inverse of _gsm7_pack — returns ``num_septets`` septets."""
    out = bytearray()
    bit_offset = 0
    while len(out) < num_septets:
        byte_idx = bit_offset // 8
        bit_idx = bit_offset % 8
        if byte_idx >= len(packed):
            break
        v = packed[byte_idx]
        if byte_idx + 1 < len(packed):
            v |= packed[byte_idx + 1] << 8
        out.append((v >> bit_idx) & 0x7F)
        bit_offset += 7
    return bytes(out)


# ================================================================
# TODO list — wire format only; runtime gaps documented elsewhere
# ================================================================

# TODO(spec: TS 23.040 §9.2.2.1): implement decode_sms_deliver — the
# reverse of encode_sms_deliver in nf/smsf/smsf.go (which the tester
# does not yet have a Python counterpart to). Needed for end-to-end
# round-trip verification of the MT path.
#
# TODO(spec: TS 23.040 §9.2.2.3): SMS-STATUS-REPORT codec for the
# TP-Status-Report-Request flow (TP-SRR=1 in SMS-SUBMIT triggers an
# SMS-STATUS-REPORT back to the originator).
#
# TODO(spec: TS 23.040 §9.2.3.24.1): full UDH IE decoder. Today only
# the concat-IED layout (8-bit ref / 16-bit ref) is generated; the
# many other IE types (port addressing, SMSC control parameters,
# alternate reply address) parse as opaque bytes via SMSSubmitTPDU.udh.
#
# TODO(spec: TS 23.038 §6.2.1): full GSM-7 default alphabet table +
# extension table. encode_sms_submit currently restricts itself to
# the ASCII subset.
#
# TODO(spec: TS 24.501 §9.11.3.39): when concat-MO needs >65535 octets
# of payload container, split across multiple UL NAS Transport
# messages. Not yet hit in practice.
#
# TODO(spec: TS 29.540 §5.2.2.4): tester-side Nsmsf_SMService_UplinkSMS
# producer mock — would let us validate the SBI form of MO-SMS in
# isolation from the AMF NAS path.

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""TS 24.539 (Rel-19) port/user-plane-node management service codec.

Python mirror of the Go codec at core/edge/tsn/ttmgmt — the two are
wire-compatible (identical message-type / operation / parameter-name
spaces and value encodings). Used by the tester's UE DS-TT simulation
to terminate PMICs the TSN AF / TSCTSF sends inside the NAS Port
management information container (TS 24.501 §9.11.4.27), and to answer
with the matching COMPLETE message.

The container that travels inside NAS 0x74 / PFCP IE 202 is exactly a
serialised TS 24.539 §8 message (this module).
"""

import struct

# ── Port management service message types (§9.1) ──
MSG_MANAGE_PORT_COMMAND = 0x01
MSG_MANAGE_PORT_COMPLETE = 0x02
MSG_PORT_MGMT_NOTIFY = 0x03
MSG_PORT_MGMT_NOTIFY_ACK = 0x04
MSG_PORT_MGMT_NOTIFY_COMPLETE = 0x05
MSG_PORT_MGMT_CAPABILITY = 0x06

# ── User plane node management service message types (§9.5A) ──
MSG_MANAGE_UPNODE_COMMAND = 0x01
MSG_MANAGE_UPNODE_COMPLETE = 0x02
MSG_UPNODE_MGMT_NOTIFY = 0x03
MSG_UPNODE_MGMT_NOTIFY_ACK = 0x04

# ── Operation codes (Table 9.2.1 / 9.5B.1) ──
OP_GET_CAPABILITIES = 0x01
OP_READ_PARAMETER = 0x02
OP_SET_PARAMETER = 0x03
OP_SUBSCRIBE_NOTIFY = 0x04
OP_UNSUBSCRIBE = 0x05
OP_SELECTIVE_READ = 0x06
OP_SELECTIVE_SUBSCRIBE_NOTIFY = 0x07
OP_SELECTIVE_UNSUBSCRIBE = 0x08
OP_DELETE_PARAMETER_ENTRY = 0x09

_OP_WITH_VALUE = {
    OP_SET_PARAMETER, OP_SELECTIVE_READ, OP_SELECTIVE_SUBSCRIBE_NOTIFY,
    OP_SELECTIVE_UNSUBSCRIBE, OP_DELETE_PARAMETER_ENTRY,
}

# ── Port parameter names (Table 9.2.1) — subset in active use ──
PORT_TX_PROPAGATION_DELAY = 0x0001
PORT_TRAFFIC_CLASS_TABLE = 0x0002
PORT_GATE_ENABLED = 0x0003
PORT_ADMIN_BASE_TIME = 0x0004
PORT_ADMIN_CONTROL_LIST_LENGTH = 0x0005
PORT_ADMIN_CONTROL_LIST = 0x0006
PORT_ADMIN_CYCLE_TIME = 0x0007
PORT_TICK_GRANULARITY = 0x0008
PORT_TX_PROP_DELAY_DELTA_THRESHOLD = 0x0009
PORT_TSN_TIME_DOMAIN_NUMBER = 0x00D4
PORT_PTP_INSTANCE_LIST = 0x00E9

# ── User plane node parameter names (Table 9.5B.1) ──
NODE_ADDRESS = 0x0001
NODE_ID = 0x0003
NODE_NWTT_PORT_NUMBERS = 0x0004
NODE_SYNC_STATE = 0x0090
NODE_CLOCK_QUALITY = 0x0091

# ── Management service causes (Table 9.4.1 / 9.5.1) ──
CAUSE_PARAM_NOT_SUPPORTED = 0x01
CAUSE_INVALID_PARAM_VALUE = 0x02
CAUSE_PARAM_VALUE_UNAVAILABLE = 0x03
CAUSE_PROTOCOL_ERROR = 0x6F

# IEIs inside MANAGE ... COMPLETE (tables 8.2.1.1, 8.8.1.1)
_IEI_CAPABILITY = 0x70
_IEI_STATUS = 0x71
_IEI_UPDATE_RESULT = 0x72


class TTMgmtError(Exception):
    pass


def _op_has_value(code):
    return code in _OP_WITH_VALUE


class Operation:
    __slots__ = ("code", "param", "value")

    def __init__(self, code, param=0, value=b""):
        self.code = code
        self.param = param
        self.value = value

    def __repr__(self):
        return f"Operation(0x{self.code:02x}, param=0x{self.param:04x}, {len(self.value)}B)"


class Message:
    """Decoded PMS or UMS message. Which lists are meaningful depends on
    the message type (see TS 24.539 §8)."""

    def __init__(self, msg_type):
        self.type = msg_type
        self.ops = []            # COMMAND
        self.capabilities = []   # COMPLETE / CAPABILITY (list of param ids)
        self.status = []         # [(param, value)] read results / notify
        self.status_errors = []  # [(param, cause)]
        self.updates = []        # [(param, value)] set/delete results
        self.update_errors = []  # [(param, cause)]

    # ── encode ──
    def encode(self):
        out = bytes([self.type])
        if self.type == MSG_MANAGE_PORT_COMMAND:  # == MSG_MANAGE_UPNODE_COMMAND
            body = _encode_ops(self.ops)
            if not body:
                raise TTMgmtError("COMMAND requires at least one operation")
            out += _lve(body)
        elif self.type == MSG_MANAGE_PORT_COMPLETE:  # == MSG_MANAGE_UPNODE_COMPLETE
            if self.capabilities:
                out += bytes([_IEI_CAPABILITY]) + _lve(_encode_caps(self.capabilities))
            if self.status or self.status_errors:
                out += bytes([_IEI_STATUS]) + _lve(_encode_status(self.status, self.status_errors, 2))
            if self.updates or self.update_errors:
                # Port update result value length field is ONE octet
                # (table 9.5.1), unlike port status (table 9.4.1, two).
                out += bytes([_IEI_UPDATE_RESULT]) + _lve(_encode_status(self.updates, self.update_errors, 1))
        elif self.type == MSG_PORT_MGMT_NOTIFY:  # == MSG_UPNODE_MGMT_NOTIFY
            out += _lve(_encode_status(self.status, self.status_errors, 2))
        elif self.type in (MSG_PORT_MGMT_NOTIFY_ACK, MSG_PORT_MGMT_NOTIFY_COMPLETE):
            pass  # header only
        elif self.type == MSG_PORT_MGMT_CAPABILITY:
            out += _lve(_encode_caps(self.capabilities))
        else:
            raise TTMgmtError(f"unknown message type 0x{self.type:02x}")
        return out


def _lve(body):
    return struct.pack("!H", len(body)) + body


def _encode_ops(ops):
    b = b""
    for op in ops:
        b += bytes([op.code])
        if op.code == OP_GET_CAPABILITIES:
            continue
        b += struct.pack("!H", op.param)
        if _op_has_value(op.code):
            b += struct.pack("!H", len(op.value)) + op.value
    return b


def _encode_caps(names):
    return b"".join(struct.pack("!H", n) for n in names)


def _encode_status(ok, errs, len_octets):
    b = bytes([len(ok)])
    for param, value in ok:
        b += struct.pack("!H", param)
        b += struct.pack("!H", len(value)) if len_octets == 2 else bytes([len(value)])
        b += value
    b += bytes([len(errs)])
    for param, cause in errs:
        b += struct.pack("!H", param) + bytes([cause])
    return b


# ── decode ──
def decode_pms(b):
    return _decode(b, pms=True)


def decode_ums(b):
    return _decode(b, pms=False)


def _decode(b, pms):
    if len(b) < 1:
        raise TTMgmtError("message too short")
    m = Message(b[0])
    rest = b[1:]
    max_type = MSG_PORT_MGMT_CAPABILITY if pms else MSG_UPNODE_MGMT_NOTIFY_ACK
    if m.type == 0 or m.type > max_type:
        raise TTMgmtError(f"reserved message type 0x{m.type:02x}")
    if m.type == MSG_MANAGE_PORT_COMMAND:
        body, _ = _read_lve(rest)
        m.ops = _decode_ops(body)
    elif m.type == MSG_MANAGE_PORT_COMPLETE:
        while rest:
            iei = rest[0]
            body, rest = _read_lve(rest[1:])
            if iei == _IEI_CAPABILITY:
                m.capabilities = _decode_caps(body)
            elif iei == _IEI_STATUS:
                m.status, m.status_errors = _decode_status(body, 2)
            elif iei == _IEI_UPDATE_RESULT:
                m.updates, m.update_errors = _decode_status(body, 1)
            # unknown IE ignored (§7.5.1)
    elif m.type == MSG_PORT_MGMT_NOTIFY:
        body, _ = _read_lve(rest)
        m.status, m.status_errors = _decode_status(body, 2)
    elif m.type == MSG_PORT_MGMT_NOTIFY_ACK:
        pass
    elif m.type == MSG_PORT_MGMT_NOTIFY_COMPLETE:
        if not pms:
            raise TTMgmtError("reserved UMS message type 0x05")
    elif m.type == MSG_PORT_MGMT_CAPABILITY:
        if not pms:
            raise TTMgmtError("reserved UMS message type 0x06")
        body, _ = _read_lve(rest)
        m.capabilities = _decode_caps(body)
    return m


def _read_lve(b):
    if len(b) < 2:
        raise TTMgmtError("message too short")
    n = struct.unpack("!H", b[:2])[0]
    if len(b) < 2 + n:
        raise TTMgmtError("message too short")
    return b[2:2 + n], b[2 + n:]


def _decode_ops(b):
    ops = []
    while b:
        code = b[0]
        b = b[1:]
        if code == 0 or code > OP_DELETE_PARAMETER_ENTRY:
            raise TTMgmtError(f"spare operation code 0x{code:02x}")
        op = Operation(code)
        if code != OP_GET_CAPABILITIES:
            if len(b) < 2:
                raise TTMgmtError("message too short")
            op.param = struct.unpack("!H", b[:2])[0]
            b = b[2:]
            if _op_has_value(code):
                if len(b) < 2:
                    raise TTMgmtError("message too short")
                n = struct.unpack("!H", b[:2])[0]
                b = b[2:]
                if len(b) < n:
                    raise TTMgmtError("message too short")
                op.value = bytes(b[:n])
                b = b[n:]
        ops.append(op)
    return ops


def _decode_caps(b):
    if len(b) % 2 != 0:
        raise TTMgmtError("capability list length not a multiple of 2")
    return [struct.unpack("!H", b[i:i + 2])[0] for i in range(0, len(b), 2)]


def _decode_status(b, len_octets):
    if len(b) < 1:
        raise TTMgmtError("message too short")
    n_ok = b[0]
    b = b[1:]
    ok = []
    for _ in range(n_ok):
        if len(b) < 2 + len_octets:
            raise TTMgmtError("message too short")
        param = struct.unpack("!H", b[:2])[0]
        b = b[2:]
        if len_octets == 2:
            n = struct.unpack("!H", b[:2])[0]
            b = b[2:]
        else:
            n = b[0]
            b = b[1:]
        if len(b) < n:
            raise TTMgmtError("message too short")
        ok.append((param, bytes(b[:n])))
        b = b[n:]
    if len(b) < 1:
        raise TTMgmtError("message too short")
    n_err = b[0]
    b = b[1:]
    errs = []
    for _ in range(n_err):
        if len(b) < 3:
            raise TTMgmtError("message too short")
        errs.append((struct.unpack("!H", b[:2])[0], b[2]))
        b = b[3:]
    return ok, errs


# ── value helpers (Table 9.2.1 encodings) ──
def encode_bool(v):
    return b"\x01" if v else b"\x00"


def encode_uint32(v):
    return struct.pack("!I", v)


def encode_uint64(v):
    return struct.pack("!Q", v)


def encode_propagation_delay(ns):
    """txPropagationDelay / delta threshold: ns × 2^16 in 8 octets,
    saturating per the 'too big to represent' rule."""
    scaled = int(ns * 65536.0)
    max_val = (1 << 63) - 1
    if scaled >= max_val:
        return struct.pack("!Q", max_val)
    if scaled < 0:
        scaled = 0
    return struct.pack("!Q", scaled)


def decode_propagation_delay(v):
    if len(v) != 8:
        raise TTMgmtError("propagation delay must be 8 octets")
    return struct.unpack("!Q", v)[0] / 65536.0


def encode_admin_base_time(seconds, nanoseconds):
    """10-octet PTPtime: 48-bit seconds + 32-bit nanoseconds."""
    return bytes([(seconds >> 40) & 0xFF, (seconds >> 32) & 0xFF]) + \
        struct.pack("!I", seconds & 0xFFFFFFFF) + struct.pack("!I", nanoseconds)


def decode_admin_base_time(v):
    if len(v) != 10:
        raise TTMgmtError("AdminBaseTime must be 10 octets")
    seconds = (v[0] << 40) | (v[1] << 32) | struct.unpack("!I", v[2:6])[0]
    return seconds, struct.unpack("!I", v[6:10])[0]


def encode_admin_control_list(entries):
    """entries: list of (gate_states, time_interval_ns). Each entry is
    op-name(1, always 0=SetGateStates) + gate states(1) + interval(4)."""
    b = b""
    for gate_states, interval in entries:
        b += bytes([0, gate_states]) + struct.pack("!I", interval)
    return b


def decode_admin_control_list(v):
    if len(v) % 6 != 0:
        raise TTMgmtError("AdminControlList length not a multiple of 6")
    return [(v[i + 1], struct.unpack("!I", v[i + 2:i + 6])[0]) for i in range(0, len(v), 6)]


def encode_nwtt_port_numbers(ports):
    return b"".join(struct.pack("!H", p) for p in ports)


def decode_nwtt_port_numbers(v):
    if len(v) % 2 != 0:
        raise TTMgmtError("NW-TT port numbers length not a multiple of 2")
    return [struct.unpack("!H", v[i:i + 2])[0] for i in range(0, len(v), 2)]


# ── convenience constructors ──
def new_get_capabilities():
    m = Message(MSG_MANAGE_PORT_COMMAND)
    m.ops = [Operation(OP_GET_CAPABILITIES)]
    return m


def new_read_params(*params):
    m = Message(MSG_MANAGE_PORT_COMMAND)
    m.ops = [Operation(OP_READ_PARAMETER, p) for p in params]
    return m


def new_set_param(param, value):
    m = Message(MSG_MANAGE_PORT_COMMAND)
    m.ops = [Operation(OP_SET_PARAMETER, param, value)]
    return m


def new_notify(*status):
    m = Message(MSG_PORT_MGMT_NOTIFY)
    m.status = list(status)
    return m

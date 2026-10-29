# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Device-side TSN Translator (DS-TT) simulation — TS 23.501 §5.27/§5.28.

A DS-TT is the UE-side translator port of the 5GS bridge. The tester UE
hosts one per Ethernet PDU session: it holds the TS 24.539 port
parameter store (gate schedule, txPropagationDelay, PTP instance list,
port state) and terminates the PMICs the TSN AF / TSCTSF sends inside
the NAS Port management information container (TS 24.501 §9.11.4.27),
answering with a MANAGE PORT COMPLETE the UE returns in the PDU Session
Modification Complete.

Mirrors the NW-TT logic in the Go core (core/nf/upf/pfcp/nwtt.go) so
both ends of the bridge speak the same TS 24.539 dialect.
"""

from src.protocol import ttmgmt as tt


# Port parameters the DS-TT advertises to a "get capabilities" op.
_SUPPORTED = [
    tt.PORT_TX_PROPAGATION_DELAY,
    tt.PORT_TRAFFIC_CLASS_TABLE,
    tt.PORT_GATE_ENABLED,
    tt.PORT_ADMIN_BASE_TIME,
    tt.PORT_ADMIN_CONTROL_LIST_LENGTH,
    tt.PORT_ADMIN_CONTROL_LIST,
    tt.PORT_ADMIN_CYCLE_TIME,
    tt.PORT_TICK_GRANULARITY,
    tt.PORT_TSN_TIME_DOMAIN_NUMBER,
    tt.PORT_PTP_INSTANCE_LIST,
]


class DSTT:
    """One DS-TT port bound to an Ethernet PDU session."""

    def __init__(self, mac, residence_time_ns=100_000):
        self.mac = bytes(mac)
        self.residence_time_ns = residence_time_ns
        # TS 24.539 port parameter store, name → value bytes.
        self.params = {
            tt.PORT_TX_PROPAGATION_DELAY: tt.encode_propagation_delay(500),  # 500 ns
            tt.PORT_GATE_ENABLED: tt.encode_bool(False),
            tt.PORT_TICK_GRANULARITY: tt.encode_uint32(1),
        }
        # PTP instance list installed by TSCTSF ConfigCreate (raw value).
        self.ptp_instance_list = None
        # Notify-subscribed parameter ids.
        self.subs = set()
        # Subscribed params changed since the last NOTIFY was emitted
        # (drives the §5.2.3 unsolicited NOTIFY via pending_notify()).
        self._changed = set()

    # ── convenience views the Robot layer / tests read ──
    @property
    def gate_enabled(self):
        v = self.params.get(tt.PORT_GATE_ENABLED)
        return bool(v and v[0] == 0x01)

    @property
    def gate_schedule(self):
        v = self.params.get(tt.PORT_ADMIN_CONTROL_LIST)
        return tt.decode_admin_control_list(v) if v else []

    @property
    def admin_cycle_time_ns(self):
        v = self.params.get(tt.PORT_ADMIN_CYCLE_TIME)
        return int.from_bytes(v, "big") if v else 0

    def mac_str(self):
        return "-".join(f"{b:02x}" for b in self.mac)

    # ── PMIC termination (TS 24.539 §5.2.1) ──
    def handle_pmic(self, container):
        """Terminate a port management service message from the TSN AF.

        Returns the reply container bytes (MANAGE PORT COMPLETE) for a
        COMMAND, or None for a NOTIFY-ACK / non-command.
        """
        msg = tt.decode_pms(container)
        if msg.type != tt.MSG_MANAGE_PORT_COMMAND:
            return None
        reply = tt.Message(tt.MSG_MANAGE_PORT_COMPLETE)
        for op in msg.ops:
            # Read family — plain and selective (§9.2 op 0x02/0x06). The
            # selective variant scopes the read to a table entry via the
            # value field; the DS-TT sim returns the whole parameter,
            # which the AF filters. Both answered in the port status IE.
            if op.code == tt.OP_GET_CAPABILITIES:
                reply.capabilities = list(_SUPPORTED)
            elif op.code in (tt.OP_READ_PARAMETER, tt.OP_SELECTIVE_READ):
                val = self._read(op.param)
                if val is not None:
                    reply.status.append((op.param, val))
                else:
                    reply.status_errors.append((op.param, tt.CAUSE_PARAM_VALUE_UNAVAILABLE))
            elif op.code == tt.OP_SET_PARAMETER:
                self._write(op.param, op.value)
                reply.updates.append((op.param, op.value))
                if op.param in self.subs:
                    self._changed.add(op.param)
            elif op.code in (tt.OP_SUBSCRIBE_NOTIFY, tt.OP_SELECTIVE_SUBSCRIBE_NOTIFY):
                self.subs.add(op.param)
                reply.status.append((op.param, self._read(op.param) or b""))
            elif op.code in (tt.OP_UNSUBSCRIBE, tt.OP_SELECTIVE_UNSUBSCRIBE):
                self.subs.discard(op.param)
            elif op.code == tt.OP_DELETE_PARAMETER_ENTRY:
                if op.param == tt.PORT_PTP_INSTANCE_LIST:
                    self.ptp_instance_list = None
                else:
                    self.params.pop(op.param, None)
                reply.updates.append((op.param, b""))
            else:
                reply.update_errors.append((op.param, tt.CAUSE_PROTOCOL_ERROR))
        return reply.encode()

    def _read(self, param):
        """Return the current value of a port parameter, or None."""
        if param == tt.PORT_PTP_INSTANCE_LIST:
            return self.ptp_instance_list
        return self.params.get(param)

    def _write(self, param, value):
        if param == tt.PORT_PTP_INSTANCE_LIST:
            self.ptp_instance_list = value
        else:
            self.params[param] = value

    def pending_notify(self):
        """Build a PORT MANAGEMENT NOTIFY (TS 24.539 §5.2.3) for the
        subscribed parameters whose value changed since the last SET,
        or None if nothing is pending. The UE ships it to the AF in a
        DS-TT-initiated PDU Session Modification; the AF replies with a
        PORT MANAGEMENT NOTIFY ACK. Clears the pending set."""
        if not self._changed:
            return None
        status = []
        for param in sorted(self._changed):
            val = self._read(param)
            if val is not None:
                status.append((param, val))
        self._changed.clear()
        if not status:
            return None
        return tt.new_notify(*status).encode()

    def capability_report(self):
        """PORT MANAGEMENT CAPABILITY message the DS-TT can push to the AF."""
        m = tt.Message(tt.MSG_PORT_MGMT_CAPABILITY)
        m.capabilities = list(_SUPPORTED)
        return m.encode()

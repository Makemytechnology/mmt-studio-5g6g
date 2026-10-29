# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Robot Framework keyword library — Rel-19 TSN / TSC (TS 23.501 §5.27/§5.28).

Drives the tester's DS-TT-capable UE plus the core's Rel-19 control-plane
REST surfaces:

  TSN AF   /api/tsnaf/*, /api/tsn/cnc/*   (5GS bridge, CNC stream config)
  TSCTSF   /api/tsctsf/*                  (time sync, TSC QoS, ASTI)
  DetNet   /restconf/data/ietf-detnet:*   (RFC 8040 / RFC 9633)

The DS-TT termination logic lives on the UE state machine
(src/statemachine/dstt.py); this library only exposes it as Robot
keywords and asserts the results the way a TSN CNC operator would.
"""

import os
import sys

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
for p in (PROJECT_ROOT, os.path.join(PROJECT_ROOT, "libs")):
    if p not in sys.path:
        sys.path.insert(0, p)

from robot.api.deco import keyword, library
from robot.api import logger
from robot.libraries.BuiltIn import BuiltIn

from src.core.api import core_api


@library(scope="GLOBAL", version="1.0")
class TsnLibrary:
    ROBOT_LIBRARY_SCOPE = "GLOBAL"

    def __init__(self):
        self._ue_lib = None

    def _ues(self):
        if not self._ue_lib:
            self._ue_lib = BuiltIn().get_library_instance("UeLibrary")
        return self._ue_lib

    def _ue(self, imsi):
        return self._ues()._get(imsi)

    # ── UE / DS-TT side ─────────────────────────────────────────────

    @keyword("Establish Ethernet PDU Session")
    def establish_ethernet_pdu(self, imsi, dnn="tsn", psi=1, sst=1,
                               dstt_mac=None, residence_time_ns=100000):
        """Establish an Ethernet (type 5) PDU session with a DS-TT —
        TS 23.501 §5.28.1. The UE includes the DS-TT MAC (0x6E), the
        UE-DS-TT residence time (0x6F) and a capability PMIC (0x74)."""
        mac = bytes.fromhex(dstt_mac.replace("-", "").replace(":", "")) if dstt_mac else None
        ok = self._ue(imsi).establish_pdu_session(
            dnn=dnn, sst=int(sst), pdu_session_id=int(psi),
            pdu_session_type=5, dstt_mac=mac,
            residence_time_ns=int(residence_time_ns))
        if not ok:
            raise AssertionError("Ethernet PDU session request failed")

    @keyword("Get DSTT MAC")
    def get_dstt_mac(self, imsi, psi=1):
        dstt = self._ue(imsi).dstts.get(int(psi))
        if not dstt:
            raise AssertionError(f"no DS-TT for IMSI={imsi} PSI={psi}")
        return dstt.mac_str()

    @keyword("DSTT Gate Should Be Enabled")
    def dstt_gate_enabled(self, imsi, psi=1):
        dstt = self._ue(imsi).dstts.get(int(psi))
        if not dstt or not dstt.gate_enabled:
            raise AssertionError(
                f"DS-TT gate not enabled (IMSI={imsi} PSI={psi}) — "
                f"expected the CNC gate schedule PMIC to have been applied")
        logger.info(f"DS-TT gate schedule: {dstt.gate_schedule} cycle={dstt.admin_cycle_time_ns}ns")
        return dstt.gate_schedule

    @keyword("DSTT Gate Schedule Should Match")
    def dstt_gate_matches(self, imsi, expected, psi=1, cycle_ns=None):
        """Assert the DS-TT holds the EXACT gate schedule the CNC pushed.
        `expected` is a list of {gate_states, duration_ns} dicts (the
        same shapes passed to Configure TSN Stream)."""
        dstt = self._ue(imsi).dstts.get(int(psi))
        if not dstt:
            raise AssertionError(f"no DS-TT for IMSI={imsi} PSI={psi}")
        want = [(int(e["gate_states"]), int(e["duration_ns"])) for e in expected]
        got = dstt.gate_schedule
        if got != want:
            raise AssertionError(f"gate schedule mismatch: DS-TT has {got}, CNC sent {want}")
        if cycle_ns is not None and dstt.admin_cycle_time_ns != int(cycle_ns):
            raise AssertionError(
                f"admin cycle time mismatch: {dstt.admin_cycle_time_ns} != {cycle_ns}")
        logger.info(f"DS-TT gate schedule matches CNC config: {got}")

    @keyword("DSTT TxPropagationDelay Should Be")
    def dstt_txprop(self, imsi, expected_ns, psi=1):
        """Read the DS-TT txPropagationDelay (used by the §5.27.5 bridge
        delay) and assert it matches — proves the PMIC read path."""
        from src.protocol import ttmgmt as tt
        dstt = self._ue(imsi).dstts.get(int(psi))
        val = dstt._read(tt.PORT_TX_PROPAGATION_DELAY) if dstt else None
        if val is None:
            raise AssertionError("txPropagationDelay unavailable at DS-TT")
        got = tt.decode_propagation_delay(val)
        if abs(got - float(expected_ns)) > 0.5:
            raise AssertionError(f"txPropagationDelay {got}ns != {expected_ns}ns")
        return got

    @keyword("DSTT Should Have PTP Instance")
    def dstt_has_ptp(self, imsi, psi=1):
        dstt = self._ue(imsi).dstts.get(int(psi))
        if not dstt or dstt.ptp_instance_list is None:
            raise AssertionError(
                f"DS-TT has no PTP instance (IMSI={imsi} PSI={psi}) — "
                f"expected TSCTSF time-sync ConfigCreate to have provisioned one")
        return dstt.ptp_instance_list.hex()

    # ── gNB / TSCAI side ────────────────────────────────────────────

    @keyword("gNB Should Have Received TSCAI")
    def gnb_has_tscai(self, psi=1, gnb_name=None):
        """Assert the gNB decoded TSC Assistance Information for the
        session (TS 38.413 TSCTrafficCharacteristics)."""
        gnb_lib = BuiltIn().get_library_instance("GnbLibrary")
        machines = list(gnb_lib._gnbs.values())
        if gnb_name:
            machines = [gnb_lib._gnbs[gnb_name]]
        for m in machines:
            tscai = getattr(m, "tscai_by_psi", {}).get(int(psi))
            if tscai:
                logger.info(f"gNB TSCAI PSI={psi}: {tscai}")
                return tscai
        raise AssertionError(
            f"gNB received no TSCAI for PSI={psi} — the SMF should have "
            f"attached TSCTrafficCharacteristics to the QoS flow")

    @keyword("gNB TSCAI Periodicity Should Be")
    def gnb_tscai_periodicity(self, expected_us, psi=1, direction="dl"):
        """Assert the gNB's decoded TSCAI periodicity for the flow
        matches the CNC stream interval (TS 23.501 §5.27.2.4)."""
        tscai = self.gnb_has_tscai(psi=psi)
        # tscai = {qfi: {"ul"/"dl": {periodicity, burst_arrival_time_ns}}}
        for qfi, dirs in tscai.items():
            entry = dirs.get(direction.lower())
            if entry and entry.get("periodicity") == int(expected_us):
                logger.info(f"gNB TSCAI QFI={qfi} {direction} periodicity={expected_us}us")
                return qfi
        raise AssertionError(
            f"no QoS flow with {direction} periodicity {expected_us}us in gNB TSCAI {tscai}")

    # ── TSN AF (5GS bridge / CNC) ───────────────────────────────────

    @keyword("List TSN Bridges")
    def list_bridges(self):
        return core_api("/api/tsnaf/bridges") or []

    @keyword("Wait For TSN Bridge")
    def wait_for_bridge(self, timeout=10):
        """Poll until the TSN AF has learned at least one 5GS bridge
        (fed by the SMF TSN_BRIDGE_INFO report after establishment)."""
        import time
        deadline = time.time() + int(timeout)
        while time.time() < deadline:
            bridges = core_api("/api/tsnaf/bridges") or []
            if bridges:
                logger.info(f"5GS bridge(s): {bridges}")
                return bridges
            time.sleep(0.5)
        raise AssertionError("no 5GS bridge learned by the TSN AF within timeout")

    @keyword("Get Bridge Capabilities")
    def bridge_capabilities(self, bridge_id_num):
        caps = core_api(f"/api/tsnaf/bridges/{bridge_id_num}/capabilities")
        if not caps:
            raise AssertionError(f"no capabilities for bridge {bridge_id_num}")
        return caps

    @keyword("Get Bridge DSTT Port")
    def bridge_dstt_port(self, bridge_id_num):
        """Return the UPF-allocated DS-TT port number of the bridge —
        dynamic per PDU session (TS 23.502 §4.3.2.2.1 step 10b), so
        suites must read it instead of hardcoding."""
        caps = self.bridge_capabilities(bridge_id_num)
        for p in caps.get("ports") or []:
            if p.get("kind") == "ds-tt":
                return p["port"]
        raise AssertionError(f"bridge {bridge_id_num} has no DS-TT port: {caps}")

    @keyword("Bridge Should Report Delays")
    def bridge_reports_delays(self, bridge_id_num):
        """Assert the §5.27.5 bridge-delay report is non-empty and every
        entry has independent min/max delays (per port pair per TC)."""
        caps = self.bridge_capabilities(bridge_id_num)
        delays = caps.get("bridge_delays") or []
        if not delays:
            raise AssertionError(f"bridge {bridge_id_num} reports no bridge_delays (§5.27.5)")
        for d in delays:
            if d.get("independent_delay_max_ns", 0) < d.get("independent_delay_min_ns", 0):
                raise AssertionError(f"delay max < min: {d}")
        logger.info(f"bridge {bridge_id_num}: {len(delays)} delay entries")
        return delays

    @keyword("Remove TSN Stream")
    def remove_stream(self, stream_id):
        core_api(f"/api/tsn/cnc/streams/{stream_id}", method="DELETE")

    @keyword("Get Stream Status")
    def stream_status(self, stream_id):
        st = core_api(f"/api/tsnaf/streams/{stream_id}")
        if not st:
            raise AssertionError(f"no status for stream {stream_id}")
        return st

    @keyword("Inject gPTP Announce")
    def inject_gptp_announce(self, bridge_id, port, gm_identity, gm_priority1=100,
                             gm_clock_class=6, domain=0, steps_removed=0):
        """Deliver a (g)PTP Announce at an NW-TT port — the N6 ingress a
        raw-L2 bench can't produce — and run BMCA (TS 23.501 §5.27.1.6,
        IEEE 802.1AS §9.3). Returns the recommended port states."""
        resp = core_api("/api/upf/nwtt/gptp/announce", method="POST", body={
            "bridge_id": int(bridge_id), "port": int(port), "domain": int(domain),
            "gm_priority1": int(gm_priority1), "gm_priority2": 128,
            "gm_clock_class": int(gm_clock_class), "gm_identity": gm_identity,
            "steps_removed": int(steps_removed),
        })
        if not resp or not resp.get("ok"):
            raise AssertionError(f"gPTP announce injection failed: {resp}")
        return resp

    @keyword("Get gPTP Port States")
    def gptp_port_states(self, bridge_id, domain=0):
        resp = core_api(f"/api/upf/nwtt/gptp/port-states?bridge_id={int(bridge_id)}&domain={int(domain)}")
        if resp is None:
            raise AssertionError("port-states query failed")
        return resp

    @keyword("gPTP Port State Should Be")
    def gptp_port_state_should_be(self, bridge_id, port, expected, domain=0):
        resp = self.gptp_port_states(bridge_id, domain)
        got = (resp.get("port_states") or {}).get(str(int(port)))
        if got != expected:
            raise AssertionError(
                f"port {port} state = {got}, want {expected} (all: {resp})")
        return got

    @keyword("Stream Should Have BAT Offset")
    def stream_bat_offset(self, stream_id, expected_ns=None):
        """Assert the RAN's EnTSCAC Burst Arrival Time offset feedback
        (TS 23.501 §5.27.2.5) reached the TSN AF stream — the full loop
        gNB Notify → AMF → SMF → PCF BAT_OFFSET_INFO → TSN AF."""
        st = self.stream_status(stream_id)
        off = st.get("bat_offset_ns", 0)
        if not off:
            raise AssertionError(
                f"stream {stream_id} has no BAT offset feedback yet: {st}")
        if expected_ns is not None and int(off) != int(expected_ns):
            raise AssertionError(f"BAT offset {off}ns != expected {expected_ns}ns")
        logger.info(f"stream {stream_id} BAT offset: {off}ns")
        return off

    @keyword("Configure TSN Stream")
    def configure_stream(self, stream_id, bridge_id, ingress_port, egress_port,
                         traffic_class=5, interval_us=500, max_frame_size=200,
                         dest_mac=None, vlan_id=0, gate_schedule=None):
        """CNC stream request (TS 23.501 §5.28.2) — POST /api/tsn/cnc/streams."""
        body = {
            "stream_id": stream_id,
            "bridge_id": int(bridge_id),
            "ingress_port": int(ingress_port),
            "egress_port": int(egress_port),
            "traffic_class": int(traffic_class),
            "interval_us": int(interval_us),
            "max_frame_size": int(max_frame_size),
            "dest_mac": dest_mac or "",
            "vlan_id": int(vlan_id),
        }
        if gate_schedule:
            body["gate_schedule"] = gate_schedule
        resp = core_api("/api/tsn/cnc/streams", method="POST", body=body)
        if not resp or not resp.get("ok"):
            raise AssertionError(f"stream config failed: {resp}")
        return resp

    # ── TSCTSF ──────────────────────────────────────────────────────

    @keyword("Create Time Sync Config")
    def create_time_sync(self, config_id, up_node_id, dnn="tsn",
                         protocol="GPTP", profile="8021as", time_domain=20,
                         gm_enable=True):
        body = {
            "config_id": config_id,
            "up_node_id": int(up_node_id),
            "dnn": dnn,
            "req_ptp_ins": {"instance_type": "PTP_RELAY",
                            "protocol": protocol, "ptp_profile": profile},
            "gm_enable": bool(gm_enable),
            "time_dom": int(time_domain),
        }
        # Idempotent for test reruns: a leftover config with the same id
        # from a previous run would otherwise reject the create.
        core_api(f"/api/tsctsf/time-sync/configs/{config_id}", method="DELETE", quiet=True)
        resp = core_api("/api/tsctsf/time-sync/configs", method="POST", body=body)
        if not resp or not resp.get("ok"):
            raise AssertionError(f"time sync config failed: {resp}")
        return resp

    @keyword("Create TSC App Session")
    def create_app_session(self, session_id, supi, dnn="tsn",
                           delay_ms=10, burst_octets=1000, gbr_dl_kbps=2000,
                           periodicity_us=2000):
        body = {
            "session_id": session_id, "af_id": "robot-af",
            "supi": supi, "dnn": dnn,
            "tsc_qos_req": {
                "req_5gs_delay_ms": float(delay_ms),
                "max_tsc_burst_size": int(burst_octets),
                "req_gbr_dl_kbps": int(gbr_dl_kbps),
                "tscai_input_dl": {"periodicity_us": int(periodicity_us)},
            },
        }
        core_api(f"/api/tsctsf/app-sessions/{session_id}", method="DELETE", quiet=True)
        resp = core_api("/api/tsctsf/app-sessions", method="POST", body=body)
        if not resp or not resp.get("ok"):
            raise AssertionError(f"TSC app session failed: {resp}")
        return resp

    @keyword("Get TSCTSF Capabilities")
    def tsctsf_capabilities(self):
        return core_api("/api/tsctsf/capabilities") or []

    @keyword("Delete TSC App Session")
    def delete_app_session(self, session_id):
        core_api(f"/api/tsctsf/app-sessions/{session_id}", method="DELETE")

    @keyword("Delete Time Sync Config")
    def delete_time_sync(self, config_id):
        core_api(f"/api/tsctsf/time-sync/configs/{config_id}", method="DELETE")

    @keyword("Create ASTI Config")
    def create_asti(self, config_id, supi, enabled=True, err_budget_ns=1000):
        """Ntsctsf_ASTI_Create — 5G access-stratum time distribution
        (TS 29.565 §5.4.2.2, TS 23.501 §5.27.1.9 Uu error budget)."""
        body = {
            "config_id": config_id,
            "supis": [supi],
            "as_time_dis_enabled": bool(enabled),
            "time_sync_err_bdgt_ns": int(err_budget_ns),
        }
        core_api(f"/api/tsctsf/asti/{config_id}", method="DELETE", quiet=True)
        resp = core_api("/api/tsctsf/asti", method="POST", body=body)
        if not resp or not resp.get("ok"):
            raise AssertionError(f"ASTI config failed: {resp}")
        return resp

    @keyword("List ASTI Configs")
    def list_asti(self):
        return core_api("/api/tsctsf/asti") or []

    @keyword("Delete ASTI Config")
    def delete_asti(self, config_id):
        core_api(f"/api/tsctsf/asti/{config_id}", method="DELETE")

    # ── DetNet (RESTCONF, RFC 8040 / RFC 9633) ──────────────────────

    @keyword("Create DetNet Flow")
    def create_detnet_flow(self, name, supi, dnn="tsn", interval_us=2000,
                           max_pkts=2, max_payload=472, min_bandwidth_bps=3000000,
                           node_latency_us=10000):
        """POST an app-flow to the RESTCONF datastore (RFC 8040 §4.4.1)."""
        body = {"ietf-detnet:app-flow": [{
            "name": name,
            "traffic-specification": {
                "interval": int(interval_us),
                "max-pkts-per-interval": int(max_pkts),
                "max-payload-size": int(max_payload),
                "min-bandwidth": str(min_bandwidth_bps),
            },
            "mmt-3gpp-detnet:5gs-node-max-latency": int(node_latency_us),
            "mmt-3gpp-detnet:target-supi": supi,
            "mmt-3gpp-detnet:target-dnn": dnn,
        }]}
        resp = core_api("/restconf/data/ietf-detnet:detnet/app-flows",
                        method="POST", body=body)
        # RESTCONF returns 201 with empty body; core_api returns None on
        # empty JSON, so re-read to confirm the flow exists.
        flows = core_api("/restconf/data/ietf-detnet:detnet")
        if not flows:
            raise AssertionError(f"DetNet flow {name} not visible after POST")
        return flows

    @keyword("List DetNet Flows")
    def list_detnet_flows(self):
        return core_api("/restconf/data/ietf-detnet:detnet") or {}

    @keyword("Delete DetNet Flow")
    def delete_detnet_flow(self, name):
        core_api(f"/restconf/data/ietf-detnet:detnet/app-flows/app-flow={name}",
                 method="DELETE")

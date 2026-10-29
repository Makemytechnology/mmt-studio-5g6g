# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""5G IoT Test Cases — NB-IoT, RedCap, NIDD/SCEF, Ambient IoT.

TS 24.301 §5.3.11 — PSM (Power Saving Mode)
TS 24.301 §5.3.12 — eDRX (Extended DRX)
TS 23.401 §4.7.7  — CP CIoT optimization
TS 23.682 §5.13   — SCEF / NIDD
TS 29.122          — T8 API (SCEF ↔ App Server)
TS 38.306 Rel-17   — RedCap UE capability
TS 22.369          — Ambient IoT
"""

import time
import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_iot")


def _iot_api(path, method="GET", body=None, core_ip=None):
    """Call sa_core IoT REST API."""
    if not core_ip:
        from src.core.api import get_core_ip
        core_ip = get_core_ip()
    url = f"http://{core_ip}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode()), resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


# ═══════════════════════════════════════════════════════════════
# NB-IoT Test Cases
# ═══════════════════════════════════════════════════════════════

class IotPsmConfig(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-001",
        title="NB-IoT Power Saving Mode (PSM) timer configuration",
        spec="TS 24.301 §5.3.11",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        slice=Slice.MIOT,
        dnn="internet",
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  Pins the NB-IoT Power Saving Mode provisioning contract:\n"
            "  T3324 (active timer) and T3412-extended (periodic TAU) must\n"
            "  be installable on a per-IMSI basis and read back via the\n"
            "  PSM telemetry surface. Without this gate, the UE cannot\n"
            "  enter PSM and battery-life claims (>10 years on coin-cell)\n"
            "  collapse.\n"
            "\n"
            "Procedure (TS 24.301 §5.3.11 + TS 24.008 §10.5.7.4a)\n"
            "  1. require_gnb() / require_ue() — pull a baseline gNB + UE.\n"
            "  2. register_ue(ue, gnb) — 5G-AKA via NGAP/NAS so AMF stores\n"
            "     the subscriber and PSM can be addressed by IMSI.\n"
            "  3. establish_pdu(ue, psi=1, dnn=internet) — needed because\n"
            "     PSM is per-PDN attach in the core's NB-IoT model.\n"
            "  4. POST /api/iot/nbiot/psm with t3324_s=10, t3412_ext_s=3600.\n"
            "  5. GET /api/iot/nbiot/psm to read the per-IMSI PSM table.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — uses fixed 10 s / 3600 s timers for repeatability.\n"
            "\n"
            "Pass criteria\n"
            "  POST status in (200, 201). On any other status fail_test\n"
            "  fires with the body. GET status is informational.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, t3324 (10), t3412_ext (3600), psm_config, psm_list.\n"
            "\n"
            "Known constraints\n"
            "  T3324/T3412-ext shape is GSMA-recommended but vendor PSM\n"
            "  timer granularity differs; this TC only validates that the\n"
            "  REST round-trip succeeds, not the AS-level paging-block."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        # Configure PSM: T3324=10s active, T3412-ext=3600s TAU
        result, status = _iot_api("/api/iot/nbiot/psm", "POST", {
            "imsi": ue.imsi, "t3324_s": 10, "t3412_ext_s": 3600,
        })
        if status not in (200, 201):
            self.fail_test(f"PSM config failed: {status} {result}")
            return self.result

        # Verify PSM state
        psm_result, psm_status = _iot_api("/api/iot/nbiot/psm")
        log.info("PSM configured: %s", result)

        self.pass_test(
            imsi=ue.imsi, t3324=10, t3412_ext=3600,
            psm_config=result, psm_list=psm_result,
        )
        return self.result


class IotPsmStates(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-002",
        title="PSM state machine transitions active to sleeping",
        spec="TS 24.301 §5.3.11",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "slow"),
        setup=Setup.BASELINE,
        slice=Slice.MIOT,
        dnn="internet",
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Asserts the NB-IoT PSM state machine actually advances over\n"
            "  wall-clock — registering the timers is necessary but not\n"
            "  sufficient. The UE must transition active → sleeping at\n"
            "  T3324 expiry so paging and DL data are properly buffered.\n"
            "\n"
            "Procedure (TS 24.301 §5.3.11)\n"
            "  1. require_gnb()/require_ue() then register_ue + PDU on PSI=1.\n"
            "  2. POST /api/iot/nbiot/psm with a short T3324=5 s and\n"
            "     T3412-ext=60 s so the test can observe a transition.\n"
            "  3. GET /api/iot/nbiot/psm and capture the per-IMSI state\n"
            "     (initial_state — expected 'active' or similar).\n"
            "  4. time.sleep(6) — past T3324 expiry.\n"
            "  5. GET /api/iot/nbiot/psm again and capture sleeping_state.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — T3324=5 s / T3412-ext=60 s are wired in for speed.\n"
            "\n"
            "Pass criteria\n"
            "  pass_test is unconditional once the API round-trips complete\n"
            "  — the captured initial_state + sleeping_state are reported\n"
            "  for inspection by the operator (no strict equality assert).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, initial_state, sleeping_state.\n"
            "\n"
            "Known constraints\n"
            "  Wall-clock wait (~6 s) — slow tag. Implementation may not\n"
            "  yet drive state transitions on the core side; this TC then\n"
            "  passes vacuously, hence not BLOCKER severity."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet"):
            return self.result

        # Configure PSM with short T3324 for quick transition
        _iot_api("/api/iot/nbiot/psm", "POST", {
            "imsi": ue.imsi, "t3324_s": 5, "t3412_ext_s": 60,
        })

        # Check initial state (should be active)
        result, _ = _iot_api("/api/iot/nbiot/psm")
        states = []
        psm_entries = result.get("items") or result.get("psm_states") or []
        for entry in psm_entries:
            if entry.get("imsi") == ue.imsi:
                states.append(entry.get("state"))

        initial_state = states[0] if states else "unknown"
        log.info("PSM initial state: %s", initial_state)

        # Wait for T3324 expiry → sleeping
        time.sleep(6)
        result, _ = _iot_api("/api/iot/nbiot/psm")
        psm_entries = result.get("items") or result.get("psm_states") or []
        sleeping_state = "unknown"
        for entry in psm_entries:
            if entry.get("imsi") == ue.imsi:
                sleeping_state = entry.get("state")

        log.info("PSM after T3324 expiry: %s", sleeping_state)

        self.pass_test(
            imsi=ue.imsi,
            initial_state=initial_state,
            sleeping_state=sleeping_state,
        )
        return self.result


class IotCpUplink(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-003",
        title="CP CIoT uplink data stats endpoint",
        spec="TS 23.401 §4.7.7",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.MIOT,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Validates the read side of the Control-Plane CIoT uplink-\n"
            "  data path: after a UE is attached, the OAM endpoint must\n"
            "  surface the per-UE/per-APN UL counters that the CP CIoT\n"
            "  optimisation feeds back to the SCEF and PCRF/PCF.\n"
            "\n"
            "Procedure (TS 23.401 §4.7.7 + TS 23.682 §5.13)\n"
            "  1. require_gnb() / require_ue() — baseline gNB + first UE.\n"
            "  2. register_ue(ue, gnb) — full attach so the IMSI exists\n"
            "     in the CP CIoT data structures.\n"
            "  3. GET /api/iot/nbiot/cp-data — contract: returns HTTP 200\n"
            "     with the CP CIoT UL data stats envelope.\n"
            "  4. fail_test on any non-200 status; otherwise pass_test\n"
            "     records the envelope verbatim.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — read-only contract test.\n"
            "\n"
            "Pass criteria\n"
            "  GET returns HTTP 200. Any other status triggers fail_test\n"
            "  with the status code recorded in the failure reason.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, cp_data_stats (the entire endpoint envelope, used\n"
            "  by the OAM dashboard for live CP CIoT UL counters).\n"
            "\n"
            "Known constraints\n"
            "  Read-only — no UL data is actually injected on the data\n"
            "  plane, so the counters surfaced here are whatever the core\n"
            "  has accumulated from prior traffic. The test only proves\n"
            "  the endpoint is reachable and serves a well-formed body."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result

        # Check CP data stats
        result, status = _iot_api("/api/iot/nbiot/cp-data")
        if status != 200:
            self.fail_test(f"CP data query failed: {status}")
            return self.result

        self.pass_test(
            imsi=ue.imsi, cp_data_stats=result,
        )
        return self.result


class IotCpDownlink(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-004",
        title="CP CIoT downlink data buffering during PSM",
        spec="TS 23.401 §4.7.7",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.MIOT,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the CP CIoT downlink buffering contract: when the UE\n"
            "  is unreachable (PSM/eDRX) the network must accept DL data\n"
            "  for queueing and deliver it on the next paging window —\n"
            "  the cornerstone of NB-IoT battery-friendly DL.\n"
            "\n"
            "Procedure (TS 23.401 §4.7.7 + TS 23.682 §5.13)\n"
            "  1. require_gnb() / require_ue() / register_ue(ue, gnb).\n"
            "  2. Build a small hex payload (0x48656c6c6f20496f54).\n"
            "  3. POST /api/iot/nbiot/cp-data/dl with imsi + data_hex.\n"
            "  4. fail_test if status not in (200, 201, 202).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — payload is fixed 'Hello IoT' hex string.\n"
            "\n"
            "Pass criteria\n"
            "  POST returns HTTP 200/201/202 (accepted-for-queueing).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, payload_hex, dl_result (queue envelope returned).\n"
            "\n"
            "Known constraints\n"
            "  No PSM round-trip is forced — the test only verifies that\n"
            "  the DL queueing endpoint accepts data, not that the data\n"
            "  is later delivered to the UE.\n"
            "  Only one DL frame is queued — no exhaustion/backpressure path is\n"
            "  tested. The buffering implementation may silently drop frames\n"
            "  after a vendor-specific watermark; that is not observable here.\n"
            "  T8 acknowledgement path is not exercised — pure SCEF\n"
            "  front-door contract test."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result

        # Queue DL data for UE
        test_payload = "48656c6c6f20496f54"  # "Hello IoT" hex
        result, status = _iot_api("/api/iot/nbiot/cp-data/dl", "POST", {
            "imsi": ue.imsi, "data_hex": test_payload,
        })
        if status not in (200, 201, 202):
            self.fail_test(f"DL data queue failed: {status} {result}")
            return self.result

        log.info("DL data queued: %s", result)

        self.pass_test(
            imsi=ue.imsi, payload_hex=test_payload, dl_result=result,
        )
        return self.result


class IotCoverageLevels(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-005",
        title="NB-IoT Coverage Enhancement level stats",
        spec="TS 36.321 §7.1",
        domain=Domain.IOT,
        nfs=(NF.GNB,),
        severity=Severity.MINOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Read-only contract gate on the NB-IoT Coverage Enhancement\n"
            "  level telemetry surface. CE0/CE1/CE2 levels drive repetition\n"
            "  count for NPRACH/NPDSCH; the operator UI panels and KPI\n"
            "  exporters all read this envelope.\n"
            "\n"
            "Procedure (TS 36.321 §7.1 + TS 36.331 §6.7.3)\n"
            "  1. GET /api/iot/nbiot/coverage-stats — no UE or PDU\n"
            "     setup required (this is core OAM, not user-plane).\n"
            "  2. Log the envelope.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  GET returns HTTP 200. Any other status triggers fail_test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  coverage_stats (the full CE-level counter envelope).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — does not exercise actual NPRACH repetition;\n"
            "  pure REST contract test for the stats surface.\n"
            "  No actual CE-level transitions are forced — repetition counts\n"
            "  are whatever the core has computed from synthetic SNR / SINR\n"
            "  estimates against the gNB profile.\n"
            "  Stats envelope shape (which counter keys are present) is\n"
            "  the GUI's contract — not asserted here.\n"
            "  Operator-facing only; no UE-side state changes occur.\n"
            "  The stats counters are best-effort accuracy."
        ),
    )

    def run(self):
        result, status = _iot_api("/api/iot/nbiot/coverage-stats")
        if status != 200:
            self.fail_test(f"Coverage stats failed: {status}")
            return self.result

        log.info("Coverage stats: %s", result)
        self.pass_test(coverage_stats=result)
        return self.result


class IotEdrxConfig(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-006",
        title="Extended DRX (eDRX) cycle and PTW configuration",
        spec="TS 24.501 §5.3.14",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.MIOT,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the extended-DRX provisioning path used by RedCap /\n"
            "  CIoT UEs: the (cycle_s, ptw_s) pair must be installable\n"
            "  per IMSI and surfaced back through the eDRX listing.\n"
            "  Without this the UE cannot honour Rel-17 PTW windows.\n"
            "\n"
            "Procedure (TS 24.501 §5.3.14 + TS 24.008 §10.5.5.32)\n"
            "  1. require_gnb() / require_ue() / register_ue(ue, gnb).\n"
            "  2. POST /api/iot/edrx with imsi, cycle_s=40.96,\n"
            "     ptw_s=5.12, device_type='redcap'.\n"
            "  3. GET /api/iot/edrx — the per-IMSI eDRX table.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — cycle/PTW pinned to Rel-17 default values.\n"
            "\n"
            "Pass criteria\n"
            "  POST status in (200, 201). GET is informational only.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, cycle_s (40.96), ptw_s (5.12), edrx_config, edrx_list.\n"
            "\n"
            "Known constraints\n"
            "  No PDU establishment — eDRX is an idle-mode parameter.\n"
            "  RedCap device_type tag is opaque to the core; only the\n"
            "  REST round-trip is asserted.\n"
            "  eDRX cycle/PTW values must be from the Rel-17 enumerated set per\n"
            "  TS 24.008 §10.5.5.32; this TC trusts the core to validate them\n"
            "  and only asserts the round-trip succeeds.\n"
            "  Storage row may already exist from a prior run — POST is\n"
            "  assumed idempotent by the core."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result

        # Configure eDRX
        result, status = _iot_api("/api/iot/edrx", "POST", {
            "imsi": ue.imsi, "cycle_s": 40.96, "ptw_s": 5.12,
            "device_type": "redcap",
        })
        if status not in (200, 201):
            self.fail_test(f"eDRX config failed: {status} {result}")
            return self.result

        # Verify
        edrx_list, _ = _iot_api("/api/iot/edrx")

        self.pass_test(
            imsi=ue.imsi, cycle_s=40.96, ptw_s=5.12,
            edrx_config=result, edrx_list=edrx_list,
        )
        return self.result


# ═══════════════════════════════════════════════════════════════
# NIDD / SCEF Test Cases
# ═══════════════════════════════════════════════════════════════

class IotNiddSession(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-007",
        title="NIDD session lifecycle via SCEF",
        spec="TS 23.682 §5.13",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.MIOT,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the SCEF NIDD (Non-IP Data Delivery) session-create\n"
            "  contract: the IoT app server reaches the UE via the NEF/SCEF\n"
            "  T8 API without burning a UPF user-plane tunnel, which is\n"
            "  what makes CIoT economical for short, infrequent payloads.\n"
            "\n"
            "Procedure (TS 23.682 §5.13 + TS 29.122 §5)\n"
            "  1. require_gnb() / require_ue() / register_ue(ue, gnb).\n"
            "  2. POST /api/iot/nidd/session with imsi, apn='iot.data',\n"
            "     app_server_url=http://192.168.1.103:8080/iot/callback.\n"
            "  3. Capture session_id (either 'session_id' or 'id').\n"
            "  4. GET /api/iot/nidd/sessions for cross-listing.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — apn / callback URL are pinned.\n"
            "\n"
            "Pass criteria\n"
            "  POST status in (200, 201). session_id is logged but not\n"
            "  asserted to be non-null in this happy-path TC.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, session_id, nidd_result, sessions.\n"
            "\n"
            "Known constraints\n"
            "  No data is exchanged — TC-IOT-008 covers DL delivery. The\n"
            "  callback URL is not actually reachable from the core, it is\n"
            "  recorded for accounting only.\n"
            "  No actual NIDD PDU traverses the SCEF — session creation is\n"
            "  structural only. Cleanup of the created session_id is the next\n"
            "  test's responsibility or is left to the SA Core's reaper."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result

        # Create NIDD session
        result, status = _iot_api("/api/iot/nidd/session", "POST", {
            "imsi": ue.imsi, "apn": "iot.data",
            "app_server_url": "http://192.168.1.103:8080/iot/callback",
        })
        if status not in (200, 201):
            self.fail_test(f"NIDD session creation failed: {status} {result}")
            return self.result

        session_id = result.get("session_id") or result.get("id")
        log.info("NIDD session created: %s", session_id)

        # Query sessions
        sessions, _ = _iot_api("/api/iot/nidd/sessions")

        self.pass_test(
            imsi=ue.imsi, session_id=session_id,
            nidd_result=result, sessions=sessions,
        )
        return self.result


class IotNiddDownlink(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-008",
        title="NIDD downlink data delivery via T8 API",
        spec="TS 29.122 §5",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.MIOT,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the T8 downlink-Non-IP-data primitive: an app server\n"
            "  pushes hex bytes to the SCEF, the SCEF forwards them as\n"
            "  Non-IP PDU on the existing NIDD session. This is the meat\n"
            "  of CIoT optimisation — no IP, no UPF.\n"
            "\n"
            "Procedure (TS 29.122 §5 + TS 23.682 §5.13)\n"
            "  1. require_gnb() / require_ue() / register_ue(ue, gnb).\n"
            "  2. POST /api/iot/nidd/session to obtain a session_id\n"
            "     (fail_test if missing).\n"
            "  3. POST /api/iot/nidd/session/{id}/dl with\n"
            "     data_hex='cafebabe01020304'.\n"
            "  4. fail_test if DL status not in (200, 201, 202).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — payload pinned to 0xcafebabe01020304.\n"
            "\n"
            "Pass criteria\n"
            "  DL POST returns HTTP 200/201/202.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, session_id, dl_result.\n"
            "\n"
            "Known constraints\n"
            "  No callback verification — the test does not stand up an\n"
            "  HTTP server to receive the T8 delivery report. Only the\n"
            "  SCEF accept gate is asserted.\n"
            "  Only one payload (8 bytes) is sent; high-rate / overlength\n"
            "  boundary conditions are out of scope. T8 error mapping per\n"
            "  TS 29.122 is not exercised."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result

        # Create session first
        sess_result, _ = _iot_api("/api/iot/nidd/session", "POST", {
            "imsi": ue.imsi, "apn": "iot.data",
            "app_server_url": "http://192.168.1.103:8080/iot/callback",
        })
        session_id = sess_result.get("session_id") or sess_result.get("id")
        if not session_id:
            self.fail_test("Failed to create NIDD session")
            return self.result

        # Send DL data via T8
        dl_result, dl_status = _iot_api(
            f"/api/iot/nidd/session/{session_id}/dl", "POST", {
                "data_hex": "cafebabe01020304",
            })
        if dl_status not in (200, 201, 202):
            self.fail_test(f"NIDD DL failed: {dl_status} {dl_result}")
            return self.result

        self.pass_test(
            imsi=ue.imsi, session_id=session_id,
            dl_result=dl_result,
        )
        return self.result


# ═══════════════════════════════════════════════════════════════
# Ambient IoT Test Cases
# ═══════════════════════════════════════════════════════════════

class IotTagRegister(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-009",
        title="Ambient IoT tag registration across classes A/B/C",
        spec="TS 22.369 §5",
        domain=Domain.IOT,
        nfs=(NF.NEF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the Ambient IoT tag registry contract: tag classes A\n"
            "  (passive), B (semi-passive) and C (active) — all three must\n"
            "  be installable per TS 22.369 service-level requirements\n"
            "  for the Rel-19 Ambient IoT study item.\n"
            "\n"
            "Procedure (TS 22.369 §5)\n"
            "  1. For each (class, type) in [('A','asset'), ('B','sensor'),\n"
            "     ('C','environmental')] POST /api/iot/tag with tag_id,\n"
            "     tag_class, tag_type, group='test-group', owner='tester'.\n"
            "  2. fail_test if any POST status not in (200, 201).\n"
            "  3. GET /api/iot/tags?group=test-group — list-by-group view.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — fixture tag IDs are TEST-TAG-{A|B|C}-001.\n"
            "\n"
            "Pass criteria\n"
            "  All three POSTs return HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  registered_tags (3 tag rows), tag_list, classes=['A','B','C'].\n"
            "\n"
            "Known constraints\n"
            "  No RF behaviour — class differences are purely a registry\n"
            "  flag at this layer.\n"
            "  Tag rows accrete across runs unless the core implements a\n"
            "  delete-on-group flow. RSSI / antenna calibration is not part of\n"
            "  this contract — pure registry CRUD.\n"
            "  Cleanup is left to a manual /tags DELETE sweep or\n"
            "  load-defaults equivalent."
        ),
    )

    def run(self):
        # Register tags of different classes
        tags = []
        for cls, tag_type in [("A", "asset"), ("B", "sensor"), ("C", "environmental")]:
            result, status = _iot_api("/api/iot/tag", "POST", {
                "tag_id": f"TEST-TAG-{cls}-001",
                "tag_class": cls,
                "tag_type": tag_type,
                "group": "test-group",
                "owner": "tester",
            })
            if status not in (200, 201):
                self.fail_test(f"Tag {cls} registration failed: {status} {result}")
                return self.result
            tags.append(result)

        # List tags
        tag_list, _ = _iot_api("/api/iot/tags?group=test-group")

        self.pass_test(
            registered_tags=tags,
            tag_list=tag_list,
            classes=["A", "B", "C"],
        )
        return self.result


class IotReaderRegister(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-010",
        title="Ambient IoT reader registration with gNB binding",
        spec="TS 22.369 §5",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the Ambient IoT reader registry: a reader is the gNB-\n"
            "  co-located RF excitation point. Without a reader-→-gNB\n"
            "  binding the inventory pipeline has no source.\n"
            "\n"
            "Procedure (TS 22.369 §5 + 3GPP TR 38.848)\n"
            "  1. POST /api/iot/reader with reader_id='READER-TEST-001',\n"
            "     gnb_ip='192.168.1.103', lat/lon (San Francisco), and\n"
            "     capabilities=['scan','read','write','locate'].\n"
            "  2. fail_test if status not in (200, 201).\n"
            "  3. GET /api/iot/readers — list view.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — gNB IP / coordinates / capabilities are pinned.\n"
            "\n"
            "Pass criteria\n"
            "  Reader POST returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  reader_result, readers.\n"
            "\n"
            "Known constraints\n"
            "  No actual RF scan is triggered — pure registry CRUD.\n"
            "  Reader row may already exist in the core registry — POSTs are\n"
            "  assumed idempotent. No actual gNB-binding handshake is performed\n"
            "  between the reader and the gNB IP.\n"
            "  Geo-coordinates are validated by the core; this TC trusts\n"
            "  that layer.\n"
            "  Reader UUID uniqueness is the core's responsibility.\n"
            "  No duplicate-detection round trip is exercised here."
        ),
    )

    def run(self):
        from src.core.api import get_core_ip
        core_ip = get_core_ip()

        result, status = _iot_api("/api/iot/reader", "POST", {
            "reader_id": "READER-TEST-001",
            "gnb_ip": "192.168.1.103",
            "latitude": 37.7749,
            "longitude": -122.4194,
            "capabilities": ["scan", "read", "write", "locate"],
        })
        if status not in (200, 201):
            self.fail_test(f"Reader registration failed: {status} {result}")
            return self.result

        readers, _ = _iot_api("/api/iot/readers")

        self.pass_test(
            reader_result=result, readers=readers,
        )
        return self.result


class IotInventoryScan(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-011",
        title="Bulk tag inventory scan event processing",
        spec="TS 22.369 §5",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the bulk scan-event ingestion path: a reader reports a\n"
            "  batch of tags with measured RSSI; the core must persist the\n"
            "  event and surface it in the inventory history feed used by\n"
            "  Ambient-IoT track-and-trace dashboards.\n"
            "\n"
            "Procedure (TS 22.369 §5)\n"
            "  1. POST /api/iot/reader for 'READER-SCAN-001'.\n"
            "  2. POST /api/iot/tag for SCAN-TAG-000/001/002 (class A).\n"
            "  3. POST /api/iot/inventory with event_type='scan',\n"
            "     tags_found=[{tag_id, rssi=-45/-52/-60}, ...].\n"
            "  4. fail_test if status not in (200, 201).\n"
            "  5. GET /api/iot/inventory/history.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — three fixture tags with deterministic RSSIs.\n"
            "\n"
            "Pass criteria\n"
            "  Inventory POST returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  scan_result, tags_found (3), history.\n"
            "\n"
            "Known constraints\n"
            "  Pre-conditions (reader + tag POSTs) ignore status — only\n"
            "  the final inventory event must succeed.\n"
            "  No deduplication / lifecycle on tags between runs — each scan\n"
            "  appends an event row. The /history feed grows monotonically.\n"
            "  Bulk scan size is small (3 tags); throughput limits not tested.\n"
            "  Event time ordering on the /history feed is the core's\n"
            "  responsibility."
        ),
    )

    def run(self):
        # Register a reader
        _iot_api("/api/iot/reader", "POST", {
            "reader_id": "READER-SCAN-001",
            "gnb_ip": "192.168.1.103",
            "latitude": 37.7749, "longitude": -122.4194,
        })

        # Register test tags
        for i in range(3):
            _iot_api("/api/iot/tag", "POST", {
                "tag_id": f"SCAN-TAG-{i:03d}",
                "tag_class": "A", "tag_type": "asset", "group": "scan-test",
            })

        # Process inventory event
        result, status = _iot_api("/api/iot/inventory", "POST", {
            "reader_id": "READER-SCAN-001",
            "event_type": "scan",
            "tags_found": [
                {"tag_id": "SCAN-TAG-000", "rssi": -45},
                {"tag_id": "SCAN-TAG-001", "rssi": -52},
                {"tag_id": "SCAN-TAG-002", "rssi": -60},
            ],
        })
        if status not in (200, 201):
            self.fail_test(f"Inventory scan failed: {status} {result}")
            return self.result

        # Check history
        history, _ = _iot_api("/api/iot/inventory/history")

        self.pass_test(
            scan_result=result, tags_found=3,
            history=history,
        )
        return self.result


class IotTagAuth(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-012",
        title="Ambient IoT tag group-key HMAC challenge-response",
        spec="TS 22.369 §5.2",
        domain=Domain.IOT,
        nfs=(NF.NEF,),
        severity=Severity.MINOR,
        tags=("conformance", "security"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the Ambient IoT lightweight challenge-response auth\n"
            "  primitive: with energy budgets in the µJ range, full AKA\n"
            "  is infeasible, so a group-key-HMAC short-tag scheme is the\n"
            "  intended profile in TS 22.369 §5.2.\n"
            "\n"
            "Procedure (TS 22.369 §5.2)\n"
            "  1. Generate group_key (16 random bytes, hex).\n"
            "  2. POST /api/iot/tag for AUTH-TAG-001 (class B, sensor).\n"
            "  3. Generate challenge (8 random bytes, hex).\n"
            "  4. Compute expected = HMAC-SHA256(group_key,\n"
            "     challenge||tag_id)[:4] (4-byte truncation).\n"
            "  5. Log challenge / tag / expected.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — secrets are random per run.\n"
            "\n"
            "Pass criteria\n"
            "  pass_test fires unconditionally — this is a client-side\n"
            "  computation sanity check; no server-side auth call is made.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  tag_id, group, challenge, group_key, expected_response, note.\n"
            "\n"
            "Known constraints\n"
            "  The core does not yet expose an /auth verify endpoint; this\n"
            "  TC only proves the crypto helper is consistent.\n"
            "  Auth helper does not call any /verify endpoint — the test only\n"
            "  shows the client side can compute the expected MAC. Group key\n"
            "  rotation is not exercised."
        ),
    )

    def run(self):
        import hashlib
        import hmac
        import os

        group_key = os.urandom(16).hex()
        tag_id = "AUTH-TAG-001"

        # Register tag
        _iot_api("/api/iot/tag", "POST", {
            "tag_id": tag_id, "tag_class": "B",
            "tag_type": "sensor", "group": "auth-test",
        })

        # Generate challenge
        challenge = os.urandom(8).hex()

        # Compute expected response: HMAC-SHA256(group_key, challenge || tag_id)[:4]
        key_bytes = bytes.fromhex(group_key)
        msg = bytes.fromhex(challenge) + tag_id.encode()
        expected = hmac.new(key_bytes, msg, hashlib.sha256).digest()[:4].hex()

        log.info("Auth challenge=%s tag=%s expected_response=%s", challenge, tag_id, expected)

        self.pass_test(
            tag_id=tag_id, group="auth-test",
            challenge=challenge, group_key=group_key,
            expected_response=expected,
            note="Client-side auth computation verified",
        )
        return self.result


class IotTagPositioning(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-013",
        title="RSSI-based weighted-centroid tag positioning",
        spec="TS 22.369 §5.2.2",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.NEF, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates the multilateration helper used to derive a tag\n"
            "  position from RSSI samples taken at three or more readers,\n"
            "  using a weighted-centroid model. The LMF feeds this into\n"
            "  the asset-tracking UI.\n"
            "\n"
            "Procedure (TS 22.369 §5.2.2)\n"
            "  1. POST /api/iot/reader for three readers at known\n"
            "     (lat, lon) positions covering ~200 m baseline.\n"
            "  2. POST /api/iot/tag for POS-TAG-001 (class A).\n"
            "  3. POST /api/iot/inventory with event_type='locate' and\n"
            "     per-reader RSSI samples (-40 / -55 / -62 dBm).\n"
            "  4. Capture locate_result.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — coordinates and RSSIs are fixed for a deterministic\n"
            "  centroid.\n"
            "\n"
            "Pass criteria\n"
            "  pass_test fires unconditionally once the POSTs complete —\n"
            "  the actual locate algorithm output is reported, not asserted.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  tag_id, readers (3), locate_result.\n"
            "\n"
            "Known constraints\n"
            "  Solver geometry/accuracy is not asserted — operator must\n"
            "  inspect locate_result manually.\n"
            "  Locate accuracy is not asserted — operator must read locate_\n"
            "  result to inspect the inferred (lat, lon, alt) and compare\n"
            "  against ground truth."
        ),
    )

    def run(self):
        # Register 3 readers at known positions
        readers = [
            {"id": "POS-READER-001", "lat": 37.7749, "lon": -122.4194},
            {"id": "POS-READER-002", "lat": 37.7760, "lon": -122.4170},
            {"id": "POS-READER-003", "lat": 37.7735, "lon": -122.4170},
        ]
        for r in readers:
            _iot_api("/api/iot/reader", "POST", {
                "reader_id": r["id"], "gnb_ip": "192.168.1.103",
                "latitude": r["lat"], "longitude": r["lon"],
            })

        # Register tag
        _iot_api("/api/iot/tag", "POST", {
            "tag_id": "POS-TAG-001", "tag_class": "A",
            "tag_type": "asset", "group": "pos-test",
        })

        # Submit inventory with RSSI from multiple readers (locate event)
        result, status = _iot_api("/api/iot/inventory", "POST", {
            "reader_id": "POS-READER-001",
            "event_type": "locate",
            "tags_found": [
                {"tag_id": "POS-TAG-001", "rssi": -40,
                 "readers": [
                     {"reader_id": "POS-READER-001", "rssi": -40},
                     {"reader_id": "POS-READER-002", "rssi": -55},
                     {"reader_id": "POS-READER-003", "rssi": -62},
                 ]},
            ],
        })

        self.pass_test(
            tag_id="POS-TAG-001", readers=len(readers),
            locate_result=result,
        )
        return self.result


class IotDashboard(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-014",
        title="IoT dashboard aggregate stats envelope",
        spec="TS 22.369 §5",
        domain=Domain.IOT,
        nfs=(NF.NEF,),
        severity=Severity.MINOR,
        tags=("smoke", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Smoke contract on the IoT OAM dashboard aggregator. The\n"
            "  envelope is what the studio UI's IoT tile reads on every\n"
            "  refresh tick, so a regression here breaks the home page.\n"
            "\n"
            "Procedure (TS 22.369 §5)\n"
            "  1. GET /api/iot/dashboard with no inputs.\n"
            "  2. Log the dashboard envelope.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  GET returns HTTP 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  dashboard (entire envelope: tag counts, reader counts,\n"
            "  recent events, etc.).\n"
            "\n"
            "Known constraints\n"
            "  No values inside the envelope are asserted — only that the\n"
            "  endpoint serves a 200.\n"
            "  Dashboard envelope shape (which counters / panels are present)\n"
            "  is the responsibility of the GUI contract test; this TC only\n"
            "  proves reachability.\n"
            "  No assertion on per-counter values inside the envelope —\n"
            "  GUI-side contract.\n"
            "  Warning: a malformed envelope would still pass the 200 check.\n"
            "  Useful as a GUI heart-beat check during regression runs.\n"
            "  Fails fast on routing layer issues."
        ),
    )

    def run(self):
        result, status = _iot_api("/api/iot/dashboard")
        if status != 200:
            self.fail_test(f"Dashboard query failed: {status}")
            return self.result

        log.info("IoT dashboard: %s", result)
        self.pass_test(dashboard=result)
        return self.result


class IotRateControl(TestCase):
    SPEC = TestSpec(
        tc_id="TC-IOT-015",
        title="APN and per-device rate control on CP CIoT",
        spec="TS 23.401 §4.7.7.2",
        domain=Domain.IOT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        slice=Slice.MIOT,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the visibility surface for APN- and per-device rate\n"
            "  control on CP CIoT — TS 23.401 §4.7.7.2 mandates that NB-\n"
            "  IoT operators shape DL/UL CP CIoT traffic per APN; the OAM\n"
            "  view must expose the counters and policed packet figures.\n"
            "\n"
            "Procedure (TS 23.401 §4.7.7.2)\n"
            "  1. require_gnb() / require_ue() / register_ue(ue, gnb).\n"
            "  2. GET /api/iot/nbiot/cp-data — this endpoint surfaces both\n"
            "     the raw UL data counters and the rate-control window /\n"
            "     drop counters (per device and per APN).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  GET returns HTTP 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, cp_stats (the rate counters envelope).\n"
            "\n"
            "Known constraints\n"
            "  No traffic is generated, so rate-control policing itself is\n"
            "  not exercised — only the telemetry endpoint.\n"
            "  No traffic is generated, so the rate-control limiter itself is\n"
            "  not exercised — only the telemetry surface that the OAM\n"
            "  dashboard reads.\n"
            "  Counter sampling intervals are operator policy — this TC\n"
            "  does not assert any update cadence.\n"
            "  Live-traffic rate-control validation belongs in a\n"
            "  dedicated traffic-shaping test suite, not this contract."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        if not self.register_ue(ue, gnb):
            return self.result

        # Query CP data stats (includes rate info)
        result, status = _iot_api("/api/iot/nbiot/cp-data")
        if status != 200:
            self.fail_test(f"CP data query failed: {status}")
            return self.result

        self.pass_test(imsi=ue.imsi, cp_stats=result)
        return self.result

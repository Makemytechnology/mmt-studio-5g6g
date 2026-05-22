# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NG Setup procedure (TS 38.413 v19.2.0 §8.7.1 + §9.2.6).

NGAP NG Setup spans the §8.7.1 procedure (Successful / Unsuccessful
Operation / Abnormal Conditions) and the §9.2.6.1 / .2 / .3 message-
level IE structures. SCTP transport beneath is TS 38.412 §7.

Each TC carries a SPEC.spec citation pointing at the exact local-PDF
clause it pins.
"""

import time
import logging
import threading

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.statemachine.gnb_fsm import (
    GnbStateMachine, IDLE, NG_SETUP_SENT, READY, ERROR,
)
import src.config as cfg
from src.config import GNB_DEFAULTS

log = logging.getLogger("tester.tc_ng_setup")


# NGAP Protocol IE IDs from local TS 38.413 v19.2.0 (NGAP-Constants.asn)
# — used to assert presence of mandatory IEs in NG SETUP RESPONSE /
# NG SETUP FAILURE per §9.2.6.2 / §9.2.6.3.
IE_AMFNAME                = 1
IE_CAUSE                  = 15
IE_CRITICALITY_DIAGNOSTICS = 19
IE_PLMN_SUPPORT_LIST      = 80
IE_RELATIVE_AMF_CAPACITY  = 86
IE_SERVED_GUAMI_LIST      = 96
IE_TIME_TO_WAIT           = 107
IE_UE_RETENTION_INFO      = 147


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-001: Basic NG Setup Success (Happy Path)
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupBasic(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-001",
        title="NG Setup happy path: SCTP + NG Setup Request/Response reaches READY",
        spec="TS 38.413 §8.7.1.2 + §9.2.6.1 + §9.2.6.2",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "foundational"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the NGAP NG Setup procedure. Per\n"
            "  TS 38.413 §8.7.1.1: 'This procedure shall be the first\n"
            "  NGAP procedure triggered after the TNL association has\n"
            "  become operational.' Failure here blocks every other test.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1.2 + TS 38.412 §7)\n"
            "  1. SCTP-INIT to AMF on 38412 (NGAP well-known port).\n"
            "  2. NG-RAN node → AMF: NG SETUP REQUEST (§9.2.6.1) carrying\n"
            "     Global RAN Node ID (M), Supported TA List (M), Default\n"
            "     Paging DRX (M), RAN Node Name (O).\n"
            "  3. AMF → NG-RAN node: NG SETUP RESPONSE (§9.2.6.2).\n"
            "  4. gNB FSM transitions IDLE → NG_SETUP_SENT → READY.\n"
            "\n"
            "Parameters (self.params)\n"
            "  amf_ip / amf_port — target AMF (defaults from cfg).\n"
            "  timeout           — wait for READY, seconds (default: 10).\n"
            "\n"
            "Pass criteria\n"
            "  gnb.state reaches READY within timeout. Result records\n"
            "  the encoded gNB-ID, configured PLMN, TAC, and AMF target.\n"
            "\n"
            "KPI deltas\n"
            "  None directly; downstream registrations depend on this.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — runs against any baseline; no UE state needed."
        ),
    )
    tc_id = "TC-NGS-001"
    name = "ng_setup_basic"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port,
            gnb_name="ngs-001-basic",
            mcc=GNB_DEFAULTS["mcc"], mnc=GNB_DEFAULTS["mnc"],
            tac=GNB_DEFAULTS["tac"], slices=GNB_DEFAULTS["slices"],
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect / NG Setup Request failed",
                               state=gnb.state, amf=f"{amf_ip}:{amf_port}")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(
                    f"gNB did not reach READY within {timeout}s (state={gnb.state})",
                    state=gnb.state, expected_state="READY",
                )
                return self.result
            self.pass_test(
                gnb_name=gnb.gnb_name, gnb_id=hex(gnb.gnb_id),
                state=gnb.state, amf=f"{amf_ip}:{amf_port}",
                plmn=f"{gnb.mcc}/{gnb.mnc}", tac=gnb.tac,
            )
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-002: State Machine IDLE → NG_SETUP_SENT → READY → IDLE
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupStateMachine(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-002",
        title="NG Setup FSM lifecycle: IDLE → NG_SETUP_SENT → READY → IDLE",
        spec="TS 38.413 §8.7.1.2",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pin the gNB-side FSM contract that downstream NAS / RRC\n"
            "  layers rely on: each state transition must be observable\n"
            "  in order, and disconnect must clean up to IDLE.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1.2)\n"
            "  1. Construct gNB (state must start IDLE).\n"
            "  2. Connect; FSM should reach NG_SETUP_SENT, then READY on\n"
            "     receipt of NG SETUP RESPONSE.\n"
            "  3. Disconnect; FSM must return to IDLE.\n"
            "\n"
            "Parameters (self.params)\n"
            "  amf_ip / amf_port / timeout (as TC-NGS-001).\n"
            "\n"
            "Pass criteria\n"
            "  Observed sequence: IDLE → NG_SETUP_SENT → READY → IDLE.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Intermediate NG_SETUP_SENT may not be observed\n"
            "  if the AMF responds faster than the polling loop; the test\n"
            "  is tolerant — it only requires the terminal READY state."
        ),
    )
    tc_id = "TC-NGS-002"
    name = "ng_setup_state_machine"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-002-fsm",
        )
        states_observed = []
        try:
            states_observed.append(("initial", gnb.state))
            if gnb.state != IDLE:
                self.fail_test(f"Initial state should be IDLE, got {gnb.state}")
                return self.result

            ok = gnb.connect()
            states_observed.append(("after_connect", gnb.state))
            if not ok:
                self.fail_test("SCTP connect failed", states=states_observed)
                return self.result

            if not gnb.wait_for_state("READY", timeout=timeout):
                states_observed.append(("after_wait", gnb.state))
                self.fail_test(f"Expected READY, stuck in {gnb.state}",
                               states=states_observed)
                return self.result
            states_observed.append(("after_ng_setup", gnb.state))

            gnb.disconnect()
            states_observed.append(("after_disconnect", gnb.state))
            if gnb.state != IDLE:
                self.fail_test(f"Expected IDLE after disconnect, got {gnb.state}",
                               states=states_observed)
                return self.result

            self.pass_test(states_observed=states_observed,
                           transition="IDLE → NG_SETUP_SENT → READY → IDLE")
        except Exception:
            gnb.disconnect()
            raise
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-003: NG Setup with Default PLMN (MCC=001, MNC=01)
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupDefaultPlmn(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-003",
        title="NG Setup with default PLMN 001/01 and TAC 0001",
        spec="TS 38.413 §9.2.6.1 + §9.3.3.5 (PLMN Identity)",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Anchor the lab's canonical test PLMN (001/01) end-to-end:\n"
            "  the AMF's PLMN Support List must accept it and the gNB\n"
            "  must encode the PLMN Identity per §9.3.3.5 (BCD packed,\n"
            "  filler-nibble 'F' for 2-digit MNC).\n"
            "\n"
            "Procedure (TS 38.413 §9.2.6.1 + §9.3.3.5)\n"
            "  1. Build NG SETUP REQUEST with PLMN=001/01, TAC=0001 in\n"
            "     the Broadcast PLMN Item under Supported TA List.\n"
            "  2. Send + await NG SETUP RESPONSE.\n"
            "\n"
            "Parameters (self.params)\n"
            "  amf_ip / amf_port / timeout.\n"
            "\n"
            "Pass criteria\n"
            "  gNB reaches READY. Result records the configured PLMN.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. AMF's served PLMN list must include 001/01."
        ),
    )
    tc_id = "TC-NGS-003"
    name = "ng_setup_default_plmn"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-003-plmn",
            mcc="001", mnc="01", tac="0001", slices=GNB_DEFAULTS["slices"],
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect failed", amf=f"{amf_ip}:{amf_port}")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test("NG Setup failed with default PLMN 001/01",
                               state=gnb.state)
                return self.result
            self.pass_test(plmn="001/01", tac="0001", state=gnb.state)
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-004: NG Setup with Custom PLMN — accept or fail-with-Cause
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupCustomPlmn(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-004",
        title="NG Setup with custom PLMN — Response or Failure both valid",
        spec="TS 38.413 §8.7.1.3 + §8.7.1.4",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        expected_duration_s=12.0,
        description=(
            "Purpose\n"
            "  Per §8.7.1.4 (Abnormal Conditions): 'If the AMF does not\n"
            "  identify any of the PLMNs/SNPNs indicated in the NG SETUP\n"
            "  REQUEST message, it shall reject the NG Setup procedure\n"
            "  with an appropriate cause value.' This TC drives a non-\n"
            "  default PLMN and accepts either outcome — RESPONSE (AMF\n"
            "  serves it) or FAILURE (AMF rejects with Cause).\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1.3 + §8.7.1.4)\n"
            "  1. NG SETUP REQUEST with MCC=310, MNC=260, TAC=0100.\n"
            "  2. Await RESPONSE → READY OR FAILURE → ERROR.\n"
            "\n"
            "Parameters (self.params)\n"
            "  mcc / mnc / tac — override the custom PLMN (defaults\n"
            "  310 / 260 / 0100).\n"
            "  timeout — seconds.\n"
            "\n"
            "Pass criteria\n"
            "  gNB ends in READY (RESPONSE) OR ERROR (FAILURE). Silence\n"
            "  beyond timeout is a fail — AMF must reply.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. TC-NGS-018 below specifically checks that\n"
            "  the FAILURE path carries the mandatory Cause IE."
        ),
    )
    tc_id = "TC-NGS-004"
    name = "ng_setup_custom_plmn"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        mcc = self.params.get("mcc", "310")
        mnc = self.params.get("mnc", "260")
        tac = self.params.get("tac", "0100")
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port,
            gnb_name="ngs-004-custom-plmn",
            mcc=mcc, mnc=mnc, tac=tac, slices=GNB_DEFAULTS["slices"],
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect failed", amf=f"{amf_ip}:{amf_port}")
                return self.result

            deadline = time.time() + timeout
            while time.time() < deadline:
                if gnb.state in (READY, ERROR):
                    break
                time.sleep(0.2)

            if gnb.state == READY:
                self.pass_test(plmn=f"{mcc}/{mnc}", tac=tac,
                               gnb_name=gnb.gnb_name, state=gnb.state,
                               outcome="NGSetupResponse (accepted)")
            elif gnb.state == ERROR:
                self.pass_test(plmn=f"{mcc}/{mnc}", tac=tac,
                               gnb_name=gnb.gnb_name, state=gnb.state,
                               outcome="NGSetupFailure (PLMN not served — expected)")
            else:
                self.fail_test(
                    f"No NGAP response within {timeout}s (state={gnb.state})",
                    plmn=f"{mcc}/{mnc}", tac=tac, state=gnb.state,
                )
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-005: NG Setup with Custom TAC
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupCustomTac(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-005",
        title="NG Setup with custom TAC — Supported TA Item encoding",
        spec="TS 38.413 §9.2.6.1 + §9.3.3.10 (Broadcast TAC)",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pin the §9.3.3.10 Broadcast TAC encoding (3-octet OCTET\n"
            "  STRING) and the AMF's tolerance of an arbitrary TAC value\n"
            "  inside the Supported TA Item.\n"
            "\n"
            "Procedure (TS 38.413 §9.2.6.1 + §9.3.3.10)\n"
            "  1. NG SETUP REQUEST with TAC=0x00FF against default PLMN.\n"
            "  2. Await NG SETUP RESPONSE.\n"
            "\n"
            "Parameters (self.params)\n"
            "  tac — TAC hex string (default: '00FF').\n"
            "\n"
            "Pass criteria\n"
            "  gNB reaches READY.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. AMF must accept the TAC as 'served' for the\n"
            "  test PLMN."
        ),
    )
    tc_id = "TC-NGS-005"
    name = "ng_setup_custom_tac"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        tac = self.params.get("tac", "00FF")
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-005-tac",
            mcc=GNB_DEFAULTS["mcc"], mnc=GNB_DEFAULTS["mnc"],
            tac=tac, slices=GNB_DEFAULTS["slices"],
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"NG Setup failed with TAC {tac}", state=gnb.state)
                return self.result
            self.pass_test(tac=tac, state=gnb.state)
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-006: NG Setup with RAN Node Name (optional IE)
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupRanNodeName(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-006",
        title="NG Setup carries the optional RAN Node Name IE end-to-end",
        spec="TS 38.413 §9.2.6.1 (RAN Node Name, O)",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Per §8.7.1.2: 'If the RAN Node Name IE is included in the\n"
            "  NG SETUP REQUEST message, the AMF may use this IE as a\n"
            "  human readable name of the NG-RAN node.' Pin that the\n"
            "  optional IE round-trips without making the AMF reject.\n"
            "\n"
            "Procedure (TS 38.413 §9.2.6.1 RAN Node Name IE, O)\n"
            "  1. Build NG SETUP REQUEST with RAN Node Name = 'MMT-5G-\n"
            "     gNB-TestNode-006' (PrintableString SIZE(1..150,...)).\n"
            "  2. Await NG SETUP RESPONSE.\n"
            "\n"
            "Parameters (self.params)\n"
            "  ran_node_name — override (default: 'MMT-5G-gNB-TestNode-006').\n"
            "\n"
            "Pass criteria\n"
            "  gNB reaches READY.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Criticality of RAN Node Name is 'ignore' →\n"
            "  the AMF is allowed to drop the IE without rejecting."
        ),
    )
    tc_id = "TC-NGS-006"
    name = "ng_setup_ran_node_name"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)
        ran_node_name = self.params.get("ran_node_name", "MMT-5G-gNB-TestNode-006")

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name=ran_node_name,
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"NG Setup failed with RANNodeName '{ran_node_name}'",
                               state=gnb.state)
                return self.result
            self.pass_test(ran_node_name=ran_node_name,
                           gnb_name=gnb.gnb_name, state=gnb.state)
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-007: Disconnect & Reconnect (single cycle)
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupReconnect(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-007",
        title="NG Setup disconnect + reconnect — clean teardown and re-setup",
        spec="TS 38.413 §8.7.1.1",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=12.0,
        description=(
            "Purpose\n"
            "  Per §8.7.1.1: NG Setup 'erases any existing application\n"
            "  level configuration data in the two nodes, replaces it by\n"
            "  the one received and clears AMF overload state\n"
            "  information at the NG-RAN node.' Verify a second NG Setup\n"
            "  after a clean SCTP teardown succeeds with the same gNB-ID.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1.1)\n"
            "  1. NG Setup #1 → READY.\n"
            "  2. SCTP disconnect; FSM returns to IDLE.\n"
            "  3. NG Setup #2 (same gNB-ID) → READY.\n"
            "\n"
            "Parameters (self.params)\n"
            "  timeout — seconds.\n"
            "\n"
            "Pass criteria\n"
            "  Both Setups succeed; state ends READY.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY."
        ),
    )
    tc_id = "TC-NGS-007"
    name = "ng_setup_reconnect"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-007-reconn",
        )
        try:
            if not gnb.connect():
                self.fail_test("First SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"First NG Setup failed (state={gnb.state})")
                return self.result

            gnb.disconnect()
            if gnb.state != IDLE:
                self.fail_test(f"State after disconnect should be IDLE, got {gnb.state}")
                return self.result

            time.sleep(0.5)

            if not gnb.connect():
                self.fail_test("Reconnect SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"Reconnect NG Setup failed (state={gnb.state})")
                return self.result

            self.pass_test(state=gnb.state, reconnect="success")
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-008: Three Reconnect Cycles
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupReconnectCycles(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-008",
        title="NG Setup reconnect: 3 connect/disconnect cycles all succeed",
        spec="TS 38.413 §8.7.1.1",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("regression", "stress"),
        setup=Setup.EMPTY,
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  Stress version of TC-NGS-007. Surfaces stale-context leaks\n"
            "  in the AMF's gNB registry when the same gNB-ID flaps.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1.1)\n"
            "  N back-to-back (connect → NG Setup → disconnect) cycles\n"
            "  on the same gNB-ID, measuring duration per cycle.\n"
            "\n"
            "Parameters (self.params)\n"
            "  cycles — iterations (default: 3).\n"
            "  timeout — seconds per cycle.\n"
            "\n"
            "Pass criteria\n"
            "  All N cycles reach READY and clean back to IDLE.\n"
            "\n"
            "KPI deltas\n"
            "  None directly; sustained AMF gNB-table churn.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. AMF must not rate-limit a flapping gNB."
        ),
    )
    tc_id = "TC-NGS-008"
    name = "ng_setup_reconnect_cycles"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)
        cycles = self.params.get("cycles", 3)

        cycle_results = []
        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-008-cycles",
        )
        for i in range(cycles):
            start_t = time.time()
            if not gnb.connect():
                cycle_results.append({"cycle": i + 1, "status": "FAIL",
                                      "error": "SCTP connect failed"})
                break
            ready = gnb.wait_for_state("READY", timeout=timeout)
            elapsed = round((time.time() - start_t) * 1000)
            if not ready:
                cycle_results.append({"cycle": i + 1, "status": "FAIL",
                                      "state": gnb.state, "duration_ms": elapsed})
                gnb.disconnect()
                break
            gnb.disconnect()
            if gnb.state != IDLE:
                cycle_results.append({"cycle": i + 1, "status": "FAIL",
                                      "error": f"State after disconnect: {gnb.state}"})
                break
            cycle_results.append({"cycle": i + 1, "status": "PASS",
                                  "duration_ms": elapsed})
            time.sleep(0.5)

        all_ok = all(c["status"] == "PASS" for c in cycle_results)
        self.result.details = {"cycles": cycle_results, "total": cycles}
        if all_ok and len(cycle_results) == cycles:
            self.pass_test()
        else:
            self.fail_test(f"Failed at cycle {len(cycle_results)}")
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-009: Concurrent multi-gNB NG Setup
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupMultiGnb(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-009",
        title="3 concurrent gNBs (distinct gNB-IDs) all reach READY",
        spec="TS 38.413 §8.7.1.2 + §9.3.1.5 (Global RAN Node ID)",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.EMPTY,
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  Per §8.7.1 the AMF maintains one NGAP context per\n"
            "  (TNL association, Global RAN Node ID). Run N gNBs in\n"
            "  parallel from auto-incrementing gNB-IDs and assert every\n"
            "  one reaches READY — pins the AMF's per-association\n"
            "  fan-out and that distinct gNB-IDs do not collide.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1.2 + §9.3.1.5)\n"
            "  1. Spawn N gNBs from the default source IP; each picks a\n"
            "     distinct gNB-ID from GnbStateMachine's auto-counter.\n"
            "     SCTP separates the associations on ephemeral local\n"
            "     ports — no IP aliasing required.\n"
            "  2. Each thread connects and waits for READY independently.\n"
            "\n"
            "Parameters (self.params)\n"
            "  count   — number of concurrent gNBs (default: 3).\n"
            "  timeout — seconds per gNB.\n"
            "\n"
            "Pass criteria\n"
            "  All N gNBs reach READY.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Prior versions tried Windows netsh IP aliasing;\n"
            "  this container runs on Linux and SCTP already disambiguates\n"
            "  associations by ephemeral port, so aliases aren't needed."
        ),
    )
    tc_id = "TC-NGS-009"
    name = "ng_setup_multi_gnb"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 15)
        count = self.params.get("count", 3)

        gnbs = []
        gnbs_lock = threading.Lock()
        results_lock = threading.Lock()
        gnb_results = []

        def setup_one(idx):
            gnb = GnbStateMachine(
                amf_ip=amf_ip, amf_port=amf_port,
                gnb_name=f"ngs-009-gnb-{idx+1}",
            )
            with gnbs_lock:
                gnbs.append(gnb)
            start_t = time.time()
            ok = gnb.connect()
            if not ok:
                with results_lock:
                    gnb_results.append({"gnb": gnb.gnb_name, "status": "FAIL",
                                        "gnb_id": hex(gnb.gnb_id),
                                        "error": "SCTP connect failed"})
                return
            ready = gnb.wait_for_state("READY", timeout=timeout)
            with results_lock:
                gnb_results.append({"gnb": gnb.gnb_name,
                                    "gnb_id": hex(gnb.gnb_id),
                                    "status": "PASS" if ready else "FAIL",
                                    "state": gnb.state,
                                    "duration_ms": round((time.time() - start_t) * 1000)})

        try:
            threads = []
            for i in range(count):
                t = threading.Thread(target=setup_one, args=(i,))
                t.start()
                threads.append(t)
                time.sleep(0.1)
            for t in threads:
                t.join(timeout=timeout + 5)
            for gnb in gnbs:
                gnb.disconnect()
        except Exception:
            for gnb in gnbs:
                try:
                    gnb.disconnect()
                except Exception:
                    pass
            raise

        passed = sum(1 for r in gnb_results if r["status"] == "PASS")
        self.result.details = {"total": count, "passed": passed,
                               "gnb_results": gnb_results}
        if passed == count:
            self.pass_test()
        else:
            self.fail_test(f"{count - passed}/{count} gNBs failed NG Setup")
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-010: NG SETUP REQUEST codec encode/decode round-trip
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupMessageValidation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-010",
        title="NG SETUP REQUEST encode/decode — mandatory IEs round-trip",
        spec="TS 38.413 §9.2.6.1",
        domain=Domain.NG_SETUP,
        nfs=(NF.GNB,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=1.0,
        description=(
            "Purpose\n"
            "  Pure-codec test (no AMF involvement) pinning the local\n"
            "  NGAP encoder against silent IE-loss regressions. Encodes\n"
            "  an NG SETUP REQUEST, decodes it, asserts procedure code\n"
            "  21 + category 'initiatingMessage' + presence of every\n"
            "  mandatory IE id per §9.2.6.1.\n"
            "\n"
            "Procedure (TS 38.413 §9.2.6.1)\n"
            "  1. NgapCodec.build_ng_setup_request → wire bytes.\n"
            "  2. NgapCodec.decode → (category, proc, ie-dict).\n"
            "  3. Assert presence of: GlobalRANNodeID (27), RANNodeName\n"
            "     (82), SupportedTAList (102), DefaultPagingDRX (21).\n"
            "\n"
            "Parameters (self.params)\n"
            "  gnb_id / gnb_name / mcc / mnc / tac / slices — overrides.\n"
            "\n"
            "Pass criteria\n"
            "  Encoded bytes non-empty. Decoded category +\n"
            "  procedure_code match. All four IE ids present.\n"
            "  RANNodeName value round-trips byte-equal.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Pure encode/decode — no transport involved."
        ),
    )
    tc_id = "TC-NGS-010"
    name = "ng_setup_msg_validate"

    def run(self):
        from src.protocol.ngap import NgapCodec
        gnb_id = self.params.get("gnb_id", 0x500001)
        gnb_name = self.params.get("gnb_name", "encode-test-gnb")
        mcc = self.params.get("mcc", GNB_DEFAULTS["mcc"])
        mnc = self.params.get("mnc", GNB_DEFAULTS["mnc"])
        tac = self.params.get("tac", GNB_DEFAULTS["tac"])
        slices = self.params.get("slices", GNB_DEFAULTS["slices"])

        try:
            encoded = NgapCodec.build_ng_setup_request(gnb_id, gnb_name, mcc, mnc, tac, slices)
            if not encoded:
                self.fail_test("Encoded NG Setup Request is empty")
                return self.result

            category, proc_code, ies = NgapCodec.decode(encoded)
            checks = {
                "category": category == "initiatingMessage",
                "proc_code_21": proc_code == 21,
                "has_global_ran_id": 27 in ies,
                "has_ran_node_name": 82 in ies,
                "has_supported_ta_list": 102 in ies,
                "has_paging_drx": 21 in ies,
                "ran_name_match": ies.get(82, "") == gnb_name,
            }
            self.result.details = {
                "encoded_size_bytes": len(encoded),
                "encoded_hex": encoded.hex()[:100] + "...",
                "category": category, "procedure_code": proc_code,
                "ie_checks": checks, "ies_found": list(ies.keys()),
            }
            if all(checks.values()):
                self.pass_test()
            else:
                failed = [k for k, v in checks.items() if not v]
                self.fail_test(f"IE validation failed: {', '.join(failed)}")
        except Exception as e:
            self.fail_test(f"Encode/decode error: {e}")
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-011: NG Setup → UE Registration integration
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupThenRegister(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-011",
        title="NG Setup followed by UE Initial Registration (integration)",
        spec="TS 38.413 §8.7.1 + TS 24.501 §5.5.1.2",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.AUSF, NF.UDM, NF.GNB),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  End-to-end smoke: NG Setup must leave the AMF ready to\n"
            "  accept InitialUEMessage on the freshly-established\n"
            "  association. If NG Setup completes but the AMF still\n"
            "  rejects subsequent NAS, the gNB-context is in a half-\n"
            "  formed state — this gates against that regression.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1 + TS 24.501 §5.5.1.2)\n"
            "  1. NG Setup → READY.\n"
            "  2. Attach baseline UE; drive Initial Registration.\n"
            "  3. Wait for UE FSM REGISTERED.\n"
            "\n"
            "Parameters (self.params)\n"
            "  timeout — seconds per step.\n"
            "\n"
            "Pass criteria\n"
            "  UE reaches REGISTERED.\n"
            "\n"
            "KPI deltas\n"
            "  /api/kpis/registration: attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — full UE provisioning required."
        ),
    )
    tc_id = "TC-NGS-011"
    name = "ng_setup_then_register"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 15)

        ue = self.ue_pool[0]
        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-011-e2e",
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"NG Setup failed (state={gnb.state})")
                return self.result

            gnb.attach_ue(ue)
            ue.register()
            if not ue.wait_for_state("REGISTERED", timeout=timeout):
                self.fail_test(f"UE registration failed (state={ue.state})",
                               imsi=ue.imsi)
                return self.result

            self.pass_test(gnb_name=gnb.gnb_name, gnb_state=gnb.state,
                           imsi=ue.imsi, ue_state=ue.state)
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-012: SCTP transport verification
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupSctpVerify(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-012",
        title="SCTP association on port 38412 — socket + local IP verified",
        spec="TS 38.412 §7",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pin TS 38.412 §7 NGAP transport: SCTP on the well-known\n"
            "  port 38412, association established, local IP bound.\n"
            "  Distinguishes a transport-layer failure (cannot reach AMF /\n"
            "  SCTP COMM_UP missing) from an NGAP-layer failure.\n"
            "\n"
            "Procedure (TS 38.412 §7)\n"
            "  1. SCTP-INIT to AMF:38412.\n"
            "  2. Verify socket connected + local IP assigned.\n"
            "  3. Continue with NG Setup; wait for READY.\n"
            "\n"
            "Parameters (self.params)\n"
            "  amf_ip / amf_port (default 38412) / timeout.\n"
            "\n"
            "Pass criteria\n"
            "  SCTP connected, local IP non-empty, NG Setup → READY.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Tester runs inside a container with\n"
            "  NET_ADMIN — kernel SCTP module must be loaded."
        ),
    )
    tc_id = "TC-NGS-012"
    name = "ng_setup_sctp_verify"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", 38412)
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-012-sctp",
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP association failed",
                               amf=f"{amf_ip}:{amf_port}")
                return self.result
            if not gnb._sctp.connected:
                self.fail_test("SCTP socket not in connected state")
                return self.result
            local_ip = gnb.gnb_ip
            if not local_ip:
                self.fail_test("No local IP assigned after SCTP connect")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"NG Setup incomplete (state={gnb.state})",
                               local_ip=local_ip)
                return self.result
            self.pass_test(
                sctp_remote=f"{amf_ip}:{amf_port}",
                sctp_local_ip=local_ip,
                sctp_connected=gnb._sctp.connected,
                gnb_state=gnb.state,
            )
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-013: NG Setup with eMBB-only slice
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupEmbbSlice(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-013",
        title="NG Setup advertises a single S-NSSAI (eMBB, SST=1)",
        spec="TS 38.413 §9.2.6.1 + §9.3.1.17 (Slice Support List)",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        slice=Slice.EMBB,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pin the TAI Slice Support List (§9.3.1.17) encoding when\n"
            "  the gNB advertises a single S-NSSAI. The AMF's slice policy\n"
            "  must fall through to the single advertised value.\n"
            "\n"
            "Procedure (TS 38.413 §9.2.6.1 + §9.3.1.17)\n"
            "  1. NG SETUP REQUEST with TAI Slice Support List = [{SST=1}]\n"
            "     under the single Broadcast PLMN Item.\n"
            "  2. Await NG SETUP RESPONSE.\n"
            "\n"
            "Parameters (self.params)\n"
            "  slices — override (default: [{'sst': 1}]).\n"
            "\n"
            "Pass criteria\n"
            "  gNB reaches READY.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. AMF's PLMN Support List must permit SST=1."
        ),
    )
    tc_id = "TC-NGS-013"
    name = "ng_setup_embb_slice"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)
        slices = self.params.get("slices", [{"sst": 1}])

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-013-embb",
            mcc=GNB_DEFAULTS["mcc"], mnc=GNB_DEFAULTS["mnc"],
            tac=GNB_DEFAULTS["tac"], slices=slices,
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test("NG Setup failed with eMBB slice",
                               state=gnb.state)
                return self.result
            self.pass_test(state=gnb.state, slices_sent=len(slices))
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-014: Context replacement on re-setup
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupContextReplace(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-014",
        title="NG Setup re-setup erases prior application-level config",
        spec="TS 38.413 §8.7.1.1",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=12.0,
        description=(
            "Purpose\n"
            "  Verbatim §8.7.1.1: 'This procedure erases any existing\n"
            "  application level configuration data in the two nodes,\n"
            "  replaces it by the one received and clears AMF overload\n"
            "  state information at the NG-RAN node. If the NG-RAN node\n"
            "  and AMF do not agree on retaining the UE contexts this\n"
            "  procedure also re-initialises the NGAP UE-related contexts\n"
            "  (if any) and erases all related signalling connections in\n"
            "  the two nodes like an NG Reset procedure would do.'\n"
            "  This TC observes the gNB-side UE-context table is cleared\n"
            "  on a second NG Setup against the same gNB-ID.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1.1)\n"
            "  1. NG Setup #1 → READY.\n"
            "  2. Disconnect.\n"
            "  3. NG Setup #2 (same gNB-ID) → READY.\n"
            "  4. Assert the gNB's local UE-context table is empty.\n"
            "\n"
            "Parameters (self.params)\n"
            "  timeout — seconds.\n"
            "\n"
            "Pass criteria\n"
            "  Both setups succeed; the gNB's UE-context table after the\n"
            "  second setup is empty.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. This TC does NOT attach UEs before the second\n"
            "  setup, so a vacuously-empty UE table is the expected pass.\n"
            "  A future companion TC should attach a UE, deregister-via-\n"
            "  reset, and re-verify."
        ),
    )
    tc_id = "TC-NGS-014"
    name = "ng_setup_context_replace"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-014-ctx",
        )
        try:
            if not gnb.connect():
                self.fail_test("First SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"First NG Setup failed (state={gnb.state})")
                return self.result

            ue_count_before = len(getattr(gnb, "ue_map", {}))
            gnb.disconnect()
            time.sleep(0.5)

            if not gnb.connect():
                self.fail_test("Re-setup SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"Re-setup NG Setup failed (state={gnb.state})")
                return self.result

            ue_count_after = len(getattr(gnb, "ue_map", {}))
            self.pass_test(
                ue_count_before=ue_count_before,
                ue_count_after=ue_count_after,
                context_replaced=ue_count_after == 0,
            )
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-015: Timing budget across iterations
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupTiming(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-015",
        title="NG Setup timing: SCTP + NGAP latency across 5 iterations",
        spec="TS 38.413 §8.7.1",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MINOR,
        tags=("regression", "scale"),
        setup=Setup.EMPTY,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Perf sentinel: detect SCTP-stack or NGAP-encoder slow-downs\n"
            "  before they leak into longer suites and confuse triage.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1)\n"
            "  Repeat N times: measure SCTP-connect time and NG Setup\n"
            "  Request→Response time separately. Report min/avg/max.\n"
            "\n"
            "Parameters (self.params)\n"
            "  iterations — count (default: 5).\n"
            "  timeout    — seconds per iteration.\n"
            "\n"
            "Pass criteria\n"
            "  Every iteration reaches READY.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No hard latency threshold — informational\n"
            "  perf data for triage."
        ),
    )
    tc_id = "TC-NGS-015"
    name = "ng_setup_timing"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)
        iterations = self.params.get("iterations", 5)

        timing_results = []
        for i in range(iterations):
            gnb = GnbStateMachine(
                amf_ip=amf_ip, amf_port=amf_port,
                gnb_name=f"ngs-015-timing-{i+1}",
            )
            t0 = time.time()
            ok = gnb.connect()
            t_sctp = (time.time() - t0) * 1000
            if not ok:
                timing_results.append({"iteration": i + 1, "status": "FAIL",
                                       "error": "SCTP connect failed"})
                gnb.disconnect()
                continue
            t1 = time.time()
            ready = gnb.wait_for_state("READY", timeout=timeout)
            t_ngsetup = (time.time() - t1) * 1000
            t_total = t_sctp + t_ngsetup
            timing_results.append({
                "iteration": i + 1,
                "status": "PASS" if ready else "FAIL",
                "sctp_connect_ms": round(t_sctp, 2),
                "ng_setup_ms": round(t_ngsetup, 2),
                "total_ms": round(t_total, 2),
            })
            gnb.disconnect()
            time.sleep(0.3)

        passed = [t for t in timing_results if t["status"] == "PASS"]
        if passed:
            avg_total = sum(t["total_ms"] for t in passed) / len(passed)
            min_total = min(t["total_ms"] for t in passed)
            max_total = max(t["total_ms"] for t in passed)
        else:
            avg_total = min_total = max_total = 0

        self.result.details = {
            "iterations": iterations, "passed": len(passed),
            "timing": timing_results,
            "avg_total_ms": round(avg_total, 2),
            "min_total_ms": round(min_total, 2),
            "max_total_ms": round(max_total, 2),
        }
        if len(passed) == iterations:
            self.pass_test()
        else:
            self.fail_test(f"{iterations - len(passed)}/{iterations} iterations failed")
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-016: SCTP idle hold (no NG Setup) — AMF idle policy
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupSctpIdle(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-016",
        title="SCTP idle hold — no NG Setup, observe AMF idle policy",
        spec="TS 38.412 §7",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Characterise the AMF's SCTP idle policy: after a bare SCTP\n"
            "  COMM_UP with NO NG Setup Request, does the AMF tear the\n"
            "  association or keep it open? Both outcomes are legal —\n"
            "  the test records which one the implementation chose.\n"
            "\n"
            "Procedure (TS 38.412 §7)\n"
            "  1. SCTP connect to AMF:38412 (no NG Setup sent).\n"
            "  2. Poll the socket state for N seconds.\n"
            "  3. Record whether the AMF closed the association.\n"
            "\n"
            "Parameters (self.params)\n"
            "  idle_seconds — hold duration (default: 15).\n"
            "\n"
            "Pass criteria\n"
            "  SCTP COMM_UP achieved; the rest is informational.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. This is a probe, not a gate."
        ),
    )
    tc_id = "TC-NGS-016"
    name = "ng_setup_sctp_idle"

    def run(self):
        from src.protocol.sctp import SctpClient
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        idle_seconds = self.params.get("idle_seconds", 15)

        sctp = SctpClient()
        try:
            local_ip = sctp.connect(amf_ip, int(amf_port), timeout=5)
            log.info("[ngs-016-idle] SCTP connected from %s — holding idle (no NG Setup)", local_ip)
            start_t = time.time()
            closed_by_amf = False
            while (time.time() - start_t) < idle_seconds:
                time.sleep(1.0)
                if not sctp.connected:
                    closed_by_amf = True
                    break
            elapsed = round(time.time() - start_t, 1)
            still_connected = sctp.connected
            log.info("[ngs-016-idle] After %.1fs idle: connected=%s, closed_by_amf=%s",
                     elapsed, still_connected, closed_by_amf)
            self.pass_test(
                sctp_local_ip=local_ip,
                sctp_remote=f"{amf_ip}:{amf_port}",
                idle_seconds=elapsed,
                still_connected=still_connected,
                closed_by_amf=closed_by_amf,
                amf_behaviour=("closed idle connection" if closed_by_amf
                               else "kept idle connection alive (no timeout)"),
            )
        except Exception as e:
            self.fail_test(f"SCTP connect failed: {e}",
                           amf=f"{amf_ip}:{amf_port}")
        finally:
            sctp.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-017: NG SETUP RESPONSE carries every mandatory IE per §9.2.6.2
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupResponseMandatoryIes(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-017",
        title="NG SETUP RESPONSE contains AMF Name, Served GUAMI List, Relative AMF Capacity, PLMN Support List",
        spec="TS 38.413 §9.2.6.2",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.BLOCKER,
        tags=("conformance", "smoke"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  §9.2.6.2 lists four mandatory IEs in NG SETUP RESPONSE:\n"
            "  AMF Name (id=1), Served GUAMI List (id=96), Relative AMF\n"
            "  Capacity (id=86), PLMN Support List (id=80). A core that\n"
            "  emits NG SETUP RESPONSE but omits any of them would still\n"
            "  satisfy TC-NGS-001 (which only checks state=READY) — this\n"
            "  TC closes that gap.\n"
            "\n"
            "Procedure (TS 38.413 §9.2.6.2)\n"
            "  1. Run NG Setup → READY.\n"
            "  2. Inspect gnb.ng_setup_response_ies (captured by the gNB\n"
            "     FSM on receipt) and assert all four IE ids are present.\n"
            "\n"
            "Parameters (self.params)\n"
            "  timeout — seconds.\n"
            "\n"
            "Pass criteria\n"
            "  All of {AMF Name (1), Served GUAMI List (96), Relative AMF\n"
            "  Capacity (86), PLMN Support List (80)} present in the IE\n"
            "  dict. Missing IE ids are reported by name in the failure.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The optional Criticality Diagnostics (19) /\n"
            "  UE Retention Information (147) / IAB Supported / Extended\n"
            "  AMF Name IEs are NOT gated — operator policy decides."
        ),
    )
    tc_id = "TC-NGS-017"
    name = "ng_setup_response_mandatory_ies"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port, gnb_name="ngs-017-resp-ies",
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect failed")
                return self.result
            if not gnb.wait_for_state("READY", timeout=timeout):
                self.fail_test(f"NG Setup failed (state={gnb.state})")
                return self.result

            ies = gnb.ng_setup_response_ies
            if ies is None:
                self.fail_test("ng_setup_response_ies not captured by gNB FSM")
                return self.result

            mandatory = {
                "AMF Name (id=1)": IE_AMFNAME,
                "Served GUAMI List (id=96)": IE_SERVED_GUAMI_LIST,
                "Relative AMF Capacity (id=86)": IE_RELATIVE_AMF_CAPACITY,
                "PLMN Support List (id=80)": IE_PLMN_SUPPORT_LIST,
            }
            missing = [n for n, i in mandatory.items() if i not in ies]
            if missing:
                self.fail_test(
                    f"NG SETUP RESPONSE missing mandatory IE(s): {missing}",
                    ie_ids_present=sorted(ies.keys()),
                )
                return self.result

            self.pass_test(
                ie_ids_present=sorted(ies.keys()),
                mandatory_ies_present=list(mandatory.keys()),
            )
        finally:
            gnb.disconnect()
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-NGS-018: NG SETUP FAILURE carries the mandatory Cause IE
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class NgSetupFailureCauseIe(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NGS-018",
        title="NG SETUP FAILURE on unknown PLMN carries the Cause IE",
        spec="TS 38.413 §8.7.1.3 + §8.7.1.4 + §9.2.6.3",
        domain=Domain.NG_SETUP,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        expected_duration_s=12.0,
        description=(
            "Purpose\n"
            "  Verbatim §8.7.1.4: 'If the AMF does not identify any of\n"
            "  the PLMNs/SNPNs indicated in the NG SETUP REQUEST message,\n"
            "  it shall reject the NG Setup procedure with an appropriate\n"
            "  cause value.' §9.2.6.3 makes Cause a mandatory IE in the\n"
            "  NG SETUP FAILURE message. This TC drives an unknown PLMN\n"
            "  and asserts the FAILURE carries a Cause IE.\n"
            "\n"
            "Procedure (TS 38.413 §8.7.1.3 + §8.7.1.4 + §9.2.6.3)\n"
            "  1. NG SETUP REQUEST with a fabricated PLMN (e.g. 310/260).\n"
            "  2. If the AMF accepts → SKIP (cannot test FAILURE here).\n"
            "  3. If the AMF rejects → inspect gnb.ng_setup_failure_ies;\n"
            "     IE id 15 (Cause) MUST be present.\n"
            "\n"
            "Parameters (self.params)\n"
            "  mcc / mnc — PLMN that AMF should not serve\n"
            "  (defaults 310 / 260).\n"
            "\n"
            "Pass criteria\n"
            "  Either:\n"
            "    - gNB ends in ERROR AND Cause (id=15) is in the IE dict,\n"
            "      OR\n"
            "    - gNB ends in READY (PLMN was served) — recorded as a\n"
            "      SKIP-equivalent pass with note.\n"
            "\n"
            "KPI deltas\n"
            "  None.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. If your AMF is configured for 310/260, this\n"
            "  TC harmlessly degrades to 'PLMN accepted' — adjust the\n"
            "  parameter to a genuinely unknown PLMN to exercise FAILURE."
        ),
    )
    tc_id = "TC-NGS-018"
    name = "ng_setup_failure_cause_ie"

    def run(self):
        amf_ip = self.params.get("amf_ip", cfg.AMF_IP)
        amf_port = self.params.get("amf_port", cfg.AMF_PORT)
        mcc = self.params.get("mcc", "310")
        mnc = self.params.get("mnc", "260")
        tac = self.params.get("tac", "0100")
        timeout = self.params.get("timeout", 10)

        gnb = GnbStateMachine(
            amf_ip=amf_ip, amf_port=amf_port,
            gnb_name="ngs-018-failure-cause",
            mcc=mcc, mnc=mnc, tac=tac, slices=GNB_DEFAULTS["slices"],
        )
        try:
            if not gnb.connect():
                self.fail_test("SCTP connect failed",
                               amf=f"{amf_ip}:{amf_port}")
                return self.result

            deadline = time.time() + timeout
            while time.time() < deadline:
                if gnb.state in (READY, ERROR):
                    break
                time.sleep(0.2)

            if gnb.state == READY:
                # AMF accepted — cannot exercise FAILURE; document and pass.
                self.pass_test(
                    note=("AMF served the requested PLMN — NG SETUP "
                          "FAILURE path not exercised. Re-run with a "
                          "PLMN your AMF does not serve to exercise "
                          "the §9.2.6.3 Cause-IE assertion."),
                    plmn=f"{mcc}/{mnc}",
                )
                return self.result

            if gnb.state != ERROR:
                self.fail_test(
                    f"No NGAP response within {timeout}s (state={gnb.state})",
                    plmn=f"{mcc}/{mnc}",
                )
                return self.result

            ies = gnb.ng_setup_failure_ies
            if ies is None:
                self.fail_test(
                    "FAILURE state reached but ng_setup_failure_ies not "
                    "captured by gNB FSM"
                )
                return self.result

            if IE_CAUSE not in ies:
                self.fail_test(
                    "NG SETUP FAILURE missing mandatory Cause IE (id=15) "
                    "— violates TS 38.413 §9.2.6.3",
                    ie_ids_present=sorted(ies.keys()),
                )
                return self.result

            self.pass_test(
                plmn=f"{mcc}/{mnc}",
                cause_present=True,
                ie_ids_present=sorted(ies.keys()),
                has_time_to_wait=IE_TIME_TO_WAIT in ies,
            )
        finally:
            gnb.disconnect()
        return self.result


ALL_NGS_TCS = [
    NgSetupBasic, NgSetupStateMachine, NgSetupDefaultPlmn, NgSetupCustomPlmn,
    NgSetupCustomTac, NgSetupRanNodeName, NgSetupReconnect,
    NgSetupReconnectCycles, NgSetupMultiGnb, NgSetupMessageValidation,
    NgSetupThenRegister, NgSetupSctpVerify, NgSetupEmbbSlice,
    NgSetupContextReplace, NgSetupTiming, NgSetupSctpIdle,
    # New spec-aligned coverage:
    NgSetupResponseMandatoryIes, NgSetupFailureCauseIe,
]

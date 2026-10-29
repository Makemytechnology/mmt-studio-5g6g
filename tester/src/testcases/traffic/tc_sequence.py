# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test case: JSON-driven test sequences.

Allows defining multi-step test scenarios in JSON that execute
against gnb_pool / ue_pool without writing Python code.

Supported actions:
    register       — attach + register a UE
    deregister     — deregister a UE
    pdu_session    — establish PDU session
    wait           — sleep for N seconds
    assert_state   — assert UE or gNB is in expected state
    disconnect_gnb — disconnect a gNB
    connect_gnb    — connect a gNB
"""

import json
import os
import time
import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.sequence")

CONFIG_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", "config")
SEQUENCES_FILE = os.path.join(CONFIG_DIR, "test_sequences.json")


def load_sequences() -> list:
    """Load all test sequences from the JSON file."""
    if not os.path.exists(SEQUENCES_FILE):
        return []
    with open(SEQUENCES_FILE, "r") as f:
        data = json.load(f)
    return data.get("sequences", [])


def save_sequences(sequences: list):
    """Persist test sequences to JSON file."""
    os.makedirs(os.path.dirname(SEQUENCES_FILE), exist_ok=True)
    with open(SEQUENCES_FILE, "w") as f:
        json.dump({"sequences": sequences}, f, indent=2)


def get_sequence(name: str) -> dict:
    """Get a single sequence by name."""
    for seq in load_sequences():
        if seq["name"] == name:
            return seq
    return None


def upsert_sequence(seq: dict):
    """Insert or update a sequence."""
    sequences = load_sequences()
    for i, existing in enumerate(sequences):
        if existing["name"] == seq["name"]:
            sequences[i] = seq
            save_sequences(sequences)
            return
    sequences.append(seq)
    save_sequences(sequences)


def delete_sequence(name: str) -> bool:
    """Delete a sequence by name. Returns True if found."""
    sequences = load_sequences()
    before = len(sequences)
    sequences = [s for s in sequences if s["name"] != name]
    if len(sequences) < before:
        save_sequences(sequences)
        return True
    return False


class SequenceTestCase(TestCase):
    """Execute a JSON-defined test sequence step by step.

    Pass ``sequence_name`` in params to select which sequence to run,
    or pass ``steps`` directly as a list of step dicts.
    """

    SPEC = TestSpec(
        tc_id="TC-SEQ-001",
        title="JSON-driven multi-step test sequence executor",
        spec="TS 24.501 §5.5",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("regression",),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        description=(
            "Purpose\n"
            "  Lets non-Python users stitch together multi-step regression\n"
            "  scenarios (NAS register / PDU session / wait / state asserts /\n"
            "  gNB connect-disconnect) in JSON and run them through the same\n"
            "  test harness as the coded TCs. Cites TS 24.501 §5.5 because\n"
            "  the registration + deregistration + service-request state\n"
            "  machine those steps drive is the NAS 5GMM/5GSM FSM defined\n"
            "  there. The sequence runner is the only TC that exists to be\n"
            "  data-driven — each entry in config/test_sequences.json is its\n"
            "  own logical test case, sharing this class as the executor.\n"
            "\n"
            "Procedure (TS 24.501 §5.5 NAS state machine)\n"
            "  1. Resolve steps: prefer params['steps'] (inline list); else\n"
            "     get_sequence(params['sequence_name']) -> seq['steps'] from\n"
            "     config/test_sequences.json. Set result.test_name=seq:NAME.\n"
            "  2. For each step dict {action, params}:\n"
            "       a. _execute_step() dispatches on action to one of\n"
            "          register/deregister/pdu_session/wait/assert_state/\n"
            "          connect_gnb/disconnect_gnb handler.\n"
            "       b. register   -> require_ue+gnb, gnb.attach_ue,\n"
            "          ue.register, wait_for_state REGISTERED.\n"
            "       c. deregister -> ue.deregister, wait DEREGISTERED.\n"
            "       d. pdu_session-> ue.establish_pdu_session(dnn, sst, sd,\n"
            "          pdu_session_id), poll ue.pdu_sessions for psi.\n"
            "       e. wait       -> time.sleep(seconds).\n"
            "       f. assert_state -> ue.state or gnb.state == expected.\n"
            "       g. connect_gnb -> gnb.connect(), wait READY.\n"
            "       h. disconnect_gnb -> gnb.disconnect().\n"
            "  3. Append {step, action, ok} to step_results; first failed\n"
            "     step short-circuits with fail_test(label).\n"
            "  4. StopTest from a step is recorded as ok=False, no extra\n"
            "     fail message (handler already set result.status).\n"
            "  5. Exceptions become fail_test(step_label error: msg).\n"
            "\n"
            "Parameters (self.params)\n"
            "  sequence_name — name to look up in config/test_sequences.json\n"
            "                  (default: 'unnamed').\n"
            "  steps         — inline list of step dicts; if set, bypasses\n"
            "                  sequence_name lookup.\n"
            "\n"
            "Pass criteria\n"
            "  Every step's handler returns True AND no StopTest/exception.\n"
            "  pass_test() is called only after the full list completes.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  steps (list of {step, action, ok[, error]}).\n"
            "\n"
            "Known constraints\n"
            "  No looping/branching/parallel constructs — sequences are\n"
            "  straight-line. Unknown action returns False (entire run\n"
            "  fails on first such step). sequence_name not found fails\n"
            "  before any step runs."
        ),
    )

    def run(self):
        # Resolve steps
        steps = self.params.get("steps")
        seq_name = self.params.get("sequence_name", "unnamed")

        if steps is None:
            seq = get_sequence(seq_name)
            if seq is None:
                self.fail_test(f"Sequence not found: {seq_name}")
                return self.result
            steps = seq.get("steps", [])
            self.result.test_name = f"seq:{seq_name}"

        step_results = []
        for i, step in enumerate(steps):
            action = step.get("action", "")
            params = step.get("params", {})
            step_label = f"step[{i}] {action}"

            try:
                ok = self._execute_step(action, params)
                step_results.append({"step": i, "action": action, "ok": ok})
                if not ok:
                    self.fail_test(f"{step_label} failed")
                    self.result.details["steps"] = step_results
                    return self.result
            except StopTest:
                step_results.append({"step": i, "action": action, "ok": False})
                self.result.details["steps"] = step_results
                return self.result
            except Exception as e:
                step_results.append({"step": i, "action": action, "ok": False, "error": str(e)})
                self.fail_test(f"{step_label} error: {e}")
                self.result.details["steps"] = step_results
                return self.result

        self.result.details["steps"] = step_results
        self.pass_test()
        return self.result

    # ── Step dispatchers ──

    def _execute_step(self, action: str, params: dict) -> bool:
        handler = {
            "register": self._step_register,
            "deregister": self._step_deregister,
            "pdu_session": self._step_pdu_session,
            "wait": self._step_wait,
            "assert_state": self._step_assert_state,
            "connect_gnb": self._step_connect_gnb,
            "disconnect_gnb": self._step_disconnect_gnb,
        }.get(action)

        if handler is None:
            log.warning("Unknown action: %s", action)
            return False

        return handler(params)

    def _step_register(self, params: dict) -> bool:
        gnb = self.require_gnb()
        ue = self.require_ue(params.get("imsi"))
        timeout = params.get("timeout", 15)
        gnb.attach_ue(ue)
        ue.register()
        return ue.wait_for_state("REGISTERED", timeout=timeout)

    def _step_deregister(self, params: dict) -> bool:
        ue = self.require_ue(params.get("imsi"))
        timeout = params.get("timeout", 15)
        ue.deregister()
        return ue.wait_for_state("DEREGISTERED", timeout=timeout)

    def _step_pdu_session(self, params: dict) -> bool:
        ue = self.require_ue(params.get("imsi"))
        psi = params.get("psi", 1)
        dnn = params.get("dnn", "internet")
        sst = params.get("sst", 1)
        sd = params.get("sd")
        timeout = params.get("timeout", 15)
        ue.establish_pdu_session(dnn=dnn, sst=sst, sd=sd, pdu_session_id=psi)
        deadline = time.time() + timeout
        while time.time() < deadline:
            if psi in ue.pdu_sessions:
                return True
            time.sleep(0.5)
        return False

    def _step_wait(self, params: dict) -> bool:
        seconds = params.get("seconds", 1)
        log.info("Sequence wait: %s seconds", seconds)
        time.sleep(seconds)
        return True

    def _step_assert_state(self, params: dict) -> bool:
        target = params.get("target", "ue")  # "ue" or "gnb"
        expected = params.get("state")

        if target == "ue":
            ue = self.require_ue(params.get("imsi"))
            ok = ue.state == expected
            if not ok:
                log.warning("assert_state: UE %s is %s, expected %s", ue.imsi, ue.state, expected)
            return ok
        elif target == "gnb":
            gnb = self.require_gnb(state=None)  # don't check state in require
            if not self.gnb_pool:
                return False
            gnb = self.gnb_pool[0]
            ok = gnb.state == expected
            if not ok:
                log.warning("assert_state: gNB is %s, expected %s", gnb.state, expected)
            return ok
        return False

    def _step_connect_gnb(self, params: dict) -> bool:
        if not self.gnb_pool:
            self.fail_test("No gNBs available")
            raise StopTest()
        gnb = self.gnb_pool[0]
        ok = gnb.connect()
        if ok and params.get("wait_ready", True):
            gnb.wait_for_state("READY", timeout=params.get("timeout", 10))
        return gnb.state == "READY"

    def _step_disconnect_gnb(self, params: dict) -> bool:
        if not self.gnb_pool:
            return True
        self.gnb_pool[0].disconnect()
        return True

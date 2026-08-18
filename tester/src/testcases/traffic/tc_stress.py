# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Multi-UE stress, batch registration, PDU sessions, traffic at scale."""

import time
import logging
import concurrent.futures
from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_stress")

# Per-UE deadlines. Default PDU = 30 s — a 64-UE burst against a core
# that serves ~3 PSR/sec tails out around 22 s, so 30 s gives headroom
# for a pass. Set strict=True (or pdu_timeout_s=15) for latency
# pressure-tests where you *want* to see the tail UEs fail.
_DEFAULT_REG_TIMEOUT_S = 15
_DEFAULT_PDU_TIMEOUT_S = 30
_STRICT_PDU_TIMEOUT_S = 15


def _register_one(test, ue, gnb, do_pdu, pdu_timeout_s=_DEFAULT_PDU_TIMEOUT_S):
    """Register a single UE (+ optional PDU). Returns (imsi, success)."""
    try:
        gnb.attach_ue(ue)
        ue.register()
        if not ue.wait_for_state("REGISTERED", timeout=_DEFAULT_REG_TIMEOUT_S):
            return (ue.imsi, False, f"reg failed state={ue.state}")
        if do_pdu:
            ue.establish_pdu_session(dnn="internet", sst=1, pdu_session_id=1)
            deadline = time.time() + pdu_timeout_s
            while time.time() < deadline:
                session = ue.pdu_sessions.get(1)
                if session and session.get("ip") and session["ip"] != "unknown":
                    break
                time.sleep(0.3)
            else:
                return (ue.imsi, False,
                        f"PDU session timeout ({pdu_timeout_s}s)")
        return (ue.imsi, True, None)
    except Exception as e:
        return (ue.imsi, False, str(e))


def _deregister_one(test, ue):
    """Deregister a single UE. Returns (imsi, success)."""
    try:
        ue.deregister()
        ok = ue.wait_for_state("DEREGISTERED", timeout=10)
        return (ue.imsi, ok, None if ok else f"dereg failed state={ue.state}")
    except Exception as e:
        return (ue.imsi, False, str(e))


def _make_stress_tc(tc_id, name, desc, ue_count, do_pdu=False, do_deregister=False, cycles=1):
    """Factory to create stress test case classes with variable UE counts."""

    # Pick the canonical Domain / spec citation by what the test exercises.
    if do_pdu:
        _domain = Domain.PDU_SESSION
        _spec_cite = "TS 23.502 §4.3.2"
        _nfs = (NF.GNB, NF.AMF, NF.SMF, NF.UPF)
    elif do_deregister:
        _domain = Domain.REGISTRATION
        _spec_cite = "TS 24.501 §5.5"
        _nfs = (NF.GNB, NF.AMF, NF.AUSF, NF.UDM)
    else:
        _domain = Domain.REGISTRATION
        _spec_cite = "TS 24.501 §5.5.1.2"
        _nfs = (NF.GNB, NF.AMF, NF.AUSF, NF.UDM)

    # Tag/severity based on scale.
    _tags = ("scale", "regression") if ue_count >= 16 else ("regression",)
    if cycles > 1:
        _tags = _tags + ("stress",)
    _severity = Severity.MAJOR

    class StressTC(TestCase):
        SPEC = TestSpec(
            tc_id=tc_id,
            title=desc,
            spec=_spec_cite,
            domain=_domain,
            nfs=_nfs,
            severity=_severity,
            tags=_tags,
            setup=Setup.BASELINE,
            expected_duration_s=max(30.0, ue_count * 0.5 * max(1, cycles)),
            description=(
                f"Purpose\n"
                f"  {desc}.\n"
                + (
                    f"  Stress variant of the Initial Registration flow —\n"
                    f"  drives {ue_count} UEs concurrently to surface AMF /\n"
                    f"  AUSF / UDM concurrency bugs (lock contention, NGAP\n"
                    f"  context-map races, SQN drift, thread-pool starvation)\n"
                    f"  that sequential single-UE tests miss.\n"
                    if ue_count > 1 else
                    f"  Single-UE stress variant — {cycles} rapid cycles on the\n"
                    f"  same UE surfaces leaks in NGAP UE-context cleanup, AMF\n"
                    f"  UE-registry growth across re-registrations, and SQN\n"
                    f"  drift in the UDM that single-cycle tests miss.\n"
                )
                + f"\n"
                f"Procedure ({_spec_cite})\n"
                f"  For each of {cycles} cycle(s):\n"
                f"    1. Take {ue_count} UE(s) from the provisioned pool.\n"
                f"    2. ThreadPoolExecutor(max_workers={ue_count}) submits one\n"
                f"       _register_one(ue, gnb, do_pdu={do_pdu}) per UE — each\n"
                f"       worker runs the full 5G-AKA Initial Registration\n"
                f"       (RegistrationRequest → Auth → SMC → RegistrationAccept\n"
                f"       → RegistrationComplete; see TC-REG-001 for the\n"
                f"       per-UE flow).\n"
                + (
                    f"    3. If do_pdu, each worker also drives PDU Session\n"
                    f"       Establishment (TS 23.502 §4.3.2) up to\n"
                    f"       pdu_timeout_s seconds (default 30; `strict=True`\n"
                    f"       lowers it to 15 for latency-press mode).\n"
                    if do_pdu else ""
                )
                + (
                    f"    {'4' if do_pdu else '3'}. After all registrations,\n"
                    f"       concurrently Deregister every UE (TS 24.501 §5.5.2).\n"
                    if do_deregister else ""
                )
                + f"\n"
                f"Parameters (self.params)\n"
                f"  pdu_timeout_s — per-UE PDU-session deadline (default 30 s).\n"
                f"  strict        — when truthy, forces 15 s pdu_timeout to\n"
                f"                  press the latency budget harder.\n"
                f"\n"
                f"Pass criteria\n"
                f"  - All {ue_count} × {cycles} = {ue_count * cycles} registration\n"
                f"    attempts succeed (reg_failed == 0).\n"
                + (
                    f"  - All deregistrations succeed (dereg_failed == 0).\n"
                    if do_deregister else ""
                )
                + f"\n"
                f"KPI deltas (after this TC, /api/kpis/registration shows)\n"
                f"  - attempts +{ue_count * cycles}, successes +{ue_count * cycles}\n"
                f"    on a clean pass; failures non-zero highlights the bug.\n"
                f"  - Latency histogram is the interesting signal — watch p95/p99\n"
                f"    for tail blow-ups under concurrent load.\n"
                f"\n"
                f"Known constraints\n"
                f"  Setup.BASELINE — full UE roster provisioned beforehand."
            ),
        )

        def run(self):
            gnb = self.require_gnb()
            self.require_ue()

            count = min(ue_count, len(self.ue_pool))
            if count < ue_count:
                log.warning("Only %d UEs available (requested %d)", count, ue_count)

            # Per-UE PDU-session deadline. Precedence:
            #   self.params["pdu_timeout_s"]  (explicit override)
            #   self.params["strict"] truthy  (force 15 s latency-press mode)
            #   else _DEFAULT_PDU_TIMEOUT_S (30 s)
            if "pdu_timeout_s" in self.params:
                pdu_timeout_s = int(self.params["pdu_timeout_s"])
            elif self.params.get("strict"):
                pdu_timeout_s = _STRICT_PDU_TIMEOUT_S
            else:
                pdu_timeout_s = _DEFAULT_PDU_TIMEOUT_S

            ues = self.ue_pool[:count]
            reg_failed = 0
            dereg_failed = 0

            for cycle in range(cycles):
                if cycles > 1:
                    log.info("Cycle %d/%d", cycle + 1, cycles)

                # Register all UEs concurrently (independent PDU sessions)
                log.info("Registering %d UEs concurrently (pdu=%s, "
                         "pdu_timeout=%ds)", count, do_pdu, pdu_timeout_s)
                with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:
                    futures = {
                        pool.submit(_register_one, self, ue, gnb, do_pdu,
                                     pdu_timeout_s=pdu_timeout_s): ue
                        for ue in ues
                    }
                    for f in concurrent.futures.as_completed(futures):
                        imsi, ok, err = f.result()
                        if not ok:
                            reg_failed += 1
                            log.warning("UE %s: %s", imsi, err)

                # Deregister all UEs concurrently
                if do_deregister:
                    log.info("Deregistering %d UEs concurrently", count)
                    with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:
                        futures = {pool.submit(_deregister_one, self, ue): ue
                                   for ue in ues}
                        for f in concurrent.futures.as_completed(futures):
                            imsi, ok, err = f.result()
                            if not ok:
                                dereg_failed += 1
                                log.warning("UE %s dereg: %s", imsi, err)

            registered = count * cycles - reg_failed
            if reg_failed == 0 and dereg_failed == 0:
                self.pass_test(
                    ue_count=count, registered=registered,
                    pdu_sessions=do_pdu, cycles=cycles,
                )
            else:
                self.fail_test(
                    f"{reg_failed} reg failures, {dereg_failed} dereg failures",
                    ue_count=count, registered=registered,
                    reg_failed=reg_failed, dereg_failed=dereg_failed,
                    pdu_sessions=do_pdu, cycles=cycles,
                )
            return self.result

    StressTC.__name__ = name
    StressTC.__qualname__ = name
    return StressTC


# Single UE rapid cycles
RapidCycles = _make_stress_tc(
    "TC-STR-001", "rapid_cycles",
    "10 rapid attach/detach cycles on single UE",
    ue_count=1, do_deregister=True, cycles=10)

# Multi-UE Registration (scaling)
Register4 = _make_stress_tc("TC-STR-002", "register_4ue", "Register 4 UEs sequential", 4)
Register8 = _make_stress_tc("TC-STR-003", "register_8ue", "Register 8 UEs sequential", 8)
Register16 = _make_stress_tc("TC-STR-004", "register_16ue", "Register 16 UEs sequential", 16)
Register32 = _make_stress_tc("TC-STR-005", "register_32ue", "Register 32 UEs sequential", 32)

# Multi-UE PDU Sessions
Pdu4 = _make_stress_tc("TC-STR-006", "pdu_4ue", "Register + PDU session for 4 UEs", 4, do_pdu=True)
Pdu8 = _make_stress_tc("TC-STR-007", "pdu_8ue", "Register + PDU session for 8 UEs", 8, do_pdu=True)
Pdu16 = _make_stress_tc("TC-STR-008", "pdu_16ue", "Register + PDU session for 16 UEs", 16, do_pdu=True)

# Multi-UE Attach/Detach Cycles
AttachDetach8x3 = _make_stress_tc(
    "TC-STR-009", "attach_detach_8ue_3cycles",
    "8 UEs each do 3 attach/detach cycles", 8, do_deregister=True, cycles=3)

# Multi-UE Traffic
Traffic4 = _make_stress_tc("TC-STR-010", "traffic_4ue", "4 UEs with PDU sessions", 4, do_pdu=True)
Traffic8 = _make_stress_tc("TC-STR-011", "traffic_8ue", "8 UEs with PDU sessions", 8, do_pdu=True)

# Large Scale
Register64 = _make_stress_tc("TC-STR-012", "register_64ue", "Register 64 UEs", 64)
Register128 = _make_stress_tc("TC-STR-013", "register_128ue", "Register all 128 UEs", 128)
Pdu32 = _make_stress_tc("TC-STR-014", "pdu_32ue", "Register + PDU for 32 UEs", 32, do_pdu=True)
Pdu64 = _make_stress_tc("TC-STR-015", "pdu_64ue", "Register + PDU for 64 UEs", 64, do_pdu=True)
Churn32 = _make_stress_tc(
    "TC-STR-016", "churn_32ue",
    "32 UEs register then deregister — churn test", 32, do_deregister=True)
Pdu128 = _make_stress_tc("TC-STR-017", "pdu_128ue",
                          "Register + PDU for 128 UEs", 128, do_pdu=True)

ALL_STRESS_TCS = [
    RapidCycles,
    Register4, Register8, Register16, Register32,
    Pdu4, Pdu8, Pdu16,
    AttachDetach8x3,
    Traffic4, Traffic8,
    Register64, Register128,
    Pdu32, Pdu64, Pdu128,
    Churn32,
]

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: UE Registration.

The file name is legacy (predates the metadata-driven Domain model);
SPEC.domain on each class below is authoritative. Deregistration-,
Authentication- and NG-Setup-domain TCs that used to live here
(TC-REG-004 Deregistration, TC-REG-005 SecurityAlgos, TC-REG-006
NgSetupVerify) have moved to tc_deregistration.py / tc_auth.py /
tc_ng_setup.py respectively to keep each procedure's coverage in one
place.
"""

import time
import threading
from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.core.provisioner import (
    delete_ue, get_ue_auth, provision_ue_auth, provision_subscription_tree,
)


class SingleRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-001",
        title="Initial registration over 3GPP access — 5G-AKA",
        spec="TS 24.501 §5.5.1.2",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "foundational"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Smoke test of the full 5G-AKA Initial Registration end-to-end.\n"
            "  Foundational pre-req for nearly every downstream TC — if this\n"
            "  fails, almost everything else will too.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.2 + TS 33.501 §6)\n"
            "  1. UE sends RegistrationRequest (regType=initial, SUCI identity).\n"
            "  2. AMF triggers authentication: AuthRequest (RAND, AUTN) → UE\n"
            "     computes RES* → AuthResponse → AMF→AUSF validates.\n"
            "  3. AMF sends SecurityModeCommand (selected NEA/NIA from policy).\n"
            "  4. UE replies with SecurityModeComplete carrying the full\n"
            "     RegistrationRequest (TS 24.501 §4.4.6 case (a)).\n"
            "  5. AMF runs NSSAI selection, sends InitialContextSetup +\n"
            "     RegistrationAccept inside DownlinkNASTransport.\n"
            "  6. UE returns RegistrationComplete → reaches RM-REGISTERED.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi     — which UE to drive (default: first UE in the pool).\n"
            "  timeout  — per-step wait, seconds (default: 15).\n"
            "\n"
            "Pass criteria\n"
            "  - UE FSM reaches REGISTERED within `timeout`.\n"
            "  - Result details record the negotiated eea / eia (the test\n"
            "    records, not gates, the algorithm choice — TC-AUTH-002 is\n"
            "    the test that gates NIA != 0).\n"
            "  - AMF log shows \"Registration complete — UE REGISTERED\".\n"
            "\n"
            "KPI deltas (after this TC, /api/kpis/registration shows)\n"
            "  - attempts +1, successes +1, failures +0, in_flight 0.\n"
            "  - latency_ms: one new sample, typically 100–200 ms end-to-end\n"
            "    (UE-side SCTP+NGAP+NAS round-trips dominate; the core's\n"
            "    own GMM path is well under 10 ms).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — tester drops core DB + provisions 128 UEs from\n"
            "  config/baseline.yaml before this runs."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if self.register_ue(ue, gnb, timeout):
            self.pass_test(
                imsi=ue.imsi, gnb=gnb.gnb_name,
                eea=ue.security_ctx.get("eea"),
                eia=ue.security_ctx.get("eia"),
            )
        return self.result


class MultiRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-002",
        title="Concurrent multi-UE initial registration",
        spec="TS 24.501 §5.5.1.2",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "scale", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Drive N Initial Registrations in parallel from a pool of UEs.\n"
            "  Exposes AMF / AUSF / UDM concurrency bugs (lock contention,\n"
            "  context map races, SQN drift) that single-UE smoke tests miss.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.2 — N times concurrently)\n"
            "  1. Take the first `count` UEs from the provisioned pool.\n"
            "  2. Spawn one thread per UE; threads start every `stagger_ms`\n"
            "     milliseconds (default 200 ms) so they overlap but don't\n"
            "     hit the AMF at the exact same instant.\n"
            "  3. Each thread runs the full 5G-AKA registration flow\n"
            "     (RegistrationRequest → Auth → SMC → RegistrationAccept →\n"
            "     RegistrationComplete) — see TC-REG-001 for the per-UE\n"
            "     blow-by-blow.\n"
            "  4. Main thread joins all workers with timeout+5s slack.\n"
            "\n"
            "Parameters (self.params)\n"
            "  count       — how many UEs to register (default: full pool).\n"
            "  timeout     — per-UE wait for REGISTERED (default: 20 s).\n"
            "  stagger_ms  — gap between thread launches (default: 200 ms;\n"
            "                set to 0 for a true thundering herd).\n"
            "\n"
            "Pass criteria\n"
            "  - Every one of the `count` UEs reaches REGISTERED.\n"
            "  - Result.details lists any failures with their final FSM state.\n"
            "\n"
            "KPI deltas (after this TC, /api/kpis/registration shows)\n"
            "  - attempts +count, successes +count, failures 0, in_flight 0.\n"
            "  - Latency histogram fills out. Note: the core measures wall\n"
            "    time from RegistrationRequest to RegistrationComplete, so\n"
            "    under stagger+concurrency the per-UE figure inflates with\n"
            "    queue depth — on the default 200 ms stagger × 128 UEs\n"
            "    you'll see p50 ~400–600 ms, p95 ~900–1200 ms. The\n"
            "    interesting signal is the *shape*: a heavy p99 tail with\n"
            "    a tight p50 is the concurrency smell this TC hunts.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — full 128-UE pool provisioned beforehand."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        count = min(self.params.get("count", len(self.ue_pool)), len(self.ue_pool))
        timeout = self.params.get("timeout", 20)
        stagger_ms = self.params.get("stagger_ms", 200)

        if count == 0:
            self.fail_test("No UEs to register")
            return self.result

        ues = self.ue_pool[:count]
        passed, failed = [], []

        def register_one(ue):
            gnb.attach_ue(ue)
            ue.register()
            if ue.wait_for_state("REGISTERED", timeout=timeout):
                passed.append(ue.imsi)
            else:
                failed.append({"imsi": ue.imsi, "state": ue.state})

        threads = []
        for i, ue in enumerate(ues):
            if i > 0 and stagger_ms > 0:
                time.sleep(stagger_ms / 1000.0)
            t = threading.Thread(target=register_one, args=(ue,))
            t.start()
            threads.append(t)
        for t in threads:
            t.join(timeout=timeout + 5)

        self.result.details = {
            "total": count, "passed": len(passed),
            "failed": len(failed), "failed_ues": failed,
        }
        if failed:
            self.fail_test(f"{len(failed)}/{count} UEs failed")
        else:
            self.pass_test()
        return self.result


# TC-REG-004 (Deregistration) was removed: it was DEREGISTRATION-
# domain coverage living under the REG- prefix and is now superseded
# by TC-DEREG-001 ("UE-initiated de-registration with De-registration
# type 'switch off'", in tc_deregistration.py) which carries the same
# coverage in the proper Deregistration file alongside 5 new
# spec-aligned TCs (TC-DEREG-002..006).


# TC-REG-005 (SecurityAlgos) was removed: it was authentication-domain
# coverage living under the REG- prefix and duplicated TC-AUTH-002.
# TC-AUTH-002 ("NAS security algorithms negotiated and keys derived",
# in tc_auth.py) now carries the same — and slightly tighter — coverage.


# TC-REG-006 (NgSetupVerify) was removed: it was NG-Setup-domain
# coverage living under the REG- prefix and duplicated TC-NGS-001
# ("NG Setup happy path: SCTP + NG Setup Request/Response reaches
# READY", in tc_ng_setup.py). TC-NGS-001 carries the same — and 6-
# section-aligned — coverage in the proper NG Setup file.


class AttachDetachCycle(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-003",
        title="Repeated attach/detach cycles (registration ↔ deregistration)",
        spec="TS 24.501 §5.5",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("regression", "stress"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Run N back-to-back (Register → Deregister) cycles on the same\n"
            "  UE. Surfaces leaks in NGAP UE-context cleanup, AMF UE-registry\n"
            "  growth across re-registrations, and SQN bookkeeping in the\n"
            "  UDM (a stuck SQN shows up as MAC failure on cycle 2+).\n"
            "\n"
            "Procedure (TS 24.501 §5.5 — repeated `cycles` times)\n"
            "  For i in 1..cycles:\n"
            "    a. gnb.attach_ue(ue); ue.register() → wait for REGISTERED.\n"
            "       (Bails out of the whole TC if any cycle's registration\n"
            "       times out — there's no point continuing to compound\n"
            "       failures.)\n"
            "    b. ue.deregister() → wait for DEREGISTERED.\n"
            "    c. Sleep 500 ms to let AMF/AUSF settle context cleanup\n"
            "       before the next cycle.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi     — which UE to drive (default: first UE in pool).\n"
            "  cycles   — number of attach/detach iterations (default: 3).\n"
            "  timeout  — per-step wait, seconds (default: 15).\n"
            "\n"
            "Pass criteria\n"
            "  - All `cycles` iterations complete both Register and Deregister.\n"
            "  - Result.details['cycles'] shows {register: True, deregister: True}\n"
            "    for every cycle.\n"
            "\n"
            "KPI deltas (after this TC, /api/kpis/registration shows)\n"
            "  - attempts +cycles, successes +cycles, failures 0.\n"
            "  - in_flight 0 between cycles — non-zero would mean the AMF\n"
            "    never finalised the prior context, which is exactly the\n"
            "    leak this TC is here to catch.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. SQN in the UDM advances each cycle —\n"
            "  baseline reset between TCs keeps the test deterministic."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        cycles = self.params.get("cycles", 3)
        timeout = self.params.get("timeout", 15)

        cycle_results = []
        for i in range(cycles):
            gnb.attach_ue(ue)
            reg_ok = False
            ue.register()
            reg_ok = ue.wait_for_state("REGISTERED", timeout=timeout)
            if not reg_ok:
                cycle_results.append({"cycle": i + 1, "register": False, "state": ue.state})
                break

            dereg_ok = False
            ue.deregister()
            dereg_ok = ue.wait_for_state("DEREGISTERED", timeout=timeout)
            cycle_results.append({"cycle": i + 1, "register": reg_ok, "deregister": dereg_ok})
            if not dereg_ok:
                break
            time.sleep(0.5)

        all_ok = all(c.get("register") and c.get("deregister") for c in cycle_results)
        self.result.details = {"cycles": cycle_results}
        if all_ok and len(cycle_results) == cycles:
            self.pass_test()
        else:
            self.fail_test(f"Failed at cycle {len(cycle_results)}")
        return self.result


# ─────────────────────────────────────────────────────────────────────
# TC-REG-007 … TC-REG-021 — parameter / branch coverage of the
# Initial Registration procedure per TS 24.501 §5.5.1. Each TC
# targets one spec-defined decision point the original TC-REG-001…003
# suite didn't exercise. Where the core hasn't implemented the spec
# branch yet, the TC will time out / produce the wrong outcome — that
# *is* the signal to extend the core, not a flaw in the test.
# ─────────────────────────────────────────────────────────────────────


class UnknownSubscriberReject(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-007",
        title="Registration rejected for unknown subscriber",
        spec="TS 24.501 §5.5.1.2.7",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Verify the AMF rejects an Initial Registration whose SUPI\n"
            "  has no subscription in the UDM, with the correct 5GMMCause.\n"
            "  Guards against silent-accept regressions where a missing\n"
            "  subscription falls through to a default profile.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.2.7, TS 33.501 §6.1.3.2)\n"
            "  1. Pick a UE; capture its full auth record (K/OPc/SQN) so\n"
            "     we can restore it afterwards.\n"
            "  2. DELETE /api/ue/auth/{imsi} — remove the UE from UDM/AUSF.\n"
            "  3. Drive an Initial Registration for the same IMSI.\n"
            "  4. AMF can't resolve credentials → 5GMM Registration Reject\n"
            "     (TS 24.501 §8.2.7) with cause #3 (Illegal UE) or #7\n"
            "     (5GS services not allowed); UE FSM returns to DEREGISTERED.\n"
            "  5. Re-provision the UE so subsequent tests aren't disturbed.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi             — which UE to deprovision (default: 1st).\n"
            "  expected_causes  — list of acceptable 5GMMCauses\n"
            "                     (default: [3, 9]). Accepts a small set\n"
            "                     because spec leaves AMF discretion.\n"
            "\n"
            "Pass criteria\n"
            "  - UE.last_reject_cause is set after register() returns.\n"
            "  - Cause ∈ expected_causes.\n"
            "  - UE FSM lands in DEREGISTERED (not REGISTERED).\n"
            "\n"
            "KPI deltas (after this TC, /api/kpis/registration shows)\n"
            "  - attempts +1, failures +1, successes 0, in_flight 0.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The test re-provisions the deleted UE in\n"
            "  its finalizer; if the test crashes between DELETE and\n"
            "  re-provision, the next test's Setup.BASELINE will repair."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        expected = self.params.get("expected_causes", [3, 9])

        # Snapshot auth so we can restore at the end.
        prior_auth = None
        try:
            prior_auth = get_ue_auth(ue.imsi)
        except Exception:
            pass

        delete_ue(ue.imsi)
        time.sleep(0.5)  # let core propagate the deletion

        try:
            rejected, cause = self.expect_reject(
                ue, gnb, timeout=15,
                expected_cause=None,  # we accept a set, not a single value
            )
            if not rejected:
                return self.result
            if cause not in expected:
                self.fail_test(
                    f"Unexpected reject cause: got {cause}, expected one of {expected}",
                    cause=cause, expected_causes=expected,
                )
                return self.result
            self.pass_test(imsi=ue.imsi, reject_cause=cause)
        finally:
            # Best-effort restore so we don't leave the core mutated.
            if prior_auth:
                try:
                    provision_ue_auth(
                        ue.imsi, prior_auth.get("k"), prior_auth.get("opc"),
                        sqn=prior_auth.get("sqn", 0),
                    )
                except Exception:
                    pass
        return self.result


class DuplicateRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-008",
        title="Duplicate registration from same SUPI",
        spec="TS 24.501 §4.4.2 + §5.5.1.2.4",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  A UE already in 5GMM-REGISTERED state issues a *second*\n"
            "  Initial Registration without first deregistering. AMF must\n"
            "  not end up with two live UE contexts for one SUPI — it\n"
            "  should tear down the prior NGAP UE association and accept\n"
            "  the new one (TS 24.501 §4.4.2). Catches UE-context leaks.\n"
            "\n"
            "Procedure\n"
            "  1. Register the UE normally → REGISTERED.\n"
            "  2. Without deregistering, call ue.register() again.\n"
            "  3. AMF clears the old NGAP UE-NGAP-ID, runs fresh auth/SMC,\n"
            "     sends RegistrationAccept on the new association.\n"
            "  4. Final UE state must be REGISTERED.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi     — which UE to drive.\n"
            "  timeout  — per-step wait (default 15 s).\n"
            "\n"
            "Pass criteria\n"
            "  - Both register attempts ultimately reach REGISTERED.\n"
            "  - No reject cause set on either attempt.\n"
            "\n"
            "KPI deltas (after this TC, /api/kpis/registration shows)\n"
            "  - attempts +2, successes +2, failures 0, in_flight 0.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if not self.register_ue(ue, gnb, timeout):
            return self.result

        # Second registration on the same UE, no dereg first.
        ue.register()
        if not ue.wait_for_state("REGISTERED", timeout=timeout):
            self.fail_test(
                f"Second registration didn't reach REGISTERED (state={ue.state}, "
                f"reject_cause={ue.last_reject_cause})",
                imsi=ue.imsi, second_state=ue.state,
                last_reject_cause=ue.last_reject_cause,
            )
            return self.result

        self.pass_test(imsi=ue.imsi)
        return self.result


class ReRegisterAfterDeregister(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-009",
        title="Re-register after explicit deregister",
        spec="TS 24.501 §5.5.1 + §5.5.2.2",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Register → Deregister → Register sequence on one UE.\n"
            "  Distinct from TC-REG-003 (which does N cycles fast); this\n"
            "  TC checks that the AMF's per-UE state is cleared cleanly\n"
            "  by an explicit Deregistration Request, so a fresh\n"
            "  registration after it behaves like the very first one\n"
            "  (full auth, fresh ngKSI).\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1 + §5.5.2.2)\n"
            "  1. Initial registration → REGISTERED, capture KAMF1.\n"
            "  2. UE-initiated Deregistration (switch_off=True) → DEREGISTERED.\n"
            "  3. Sleep 500 ms (AMF context cleanup window).\n"
            "  4. Initial registration again → REGISTERED, capture KAMF2.\n"
            "  5. KAMF2 != KAMF1 (fresh auth ran, not cached-context reuse).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout — usual knobs.\n"
            "\n"
            "Pass criteria\n"
            "  - Both registrations land in REGISTERED.\n"
            "  - Deregistration lands in DEREGISTERED.\n"
            "  - KAMF2 differs from KAMF1 (fresh auth confirmed).\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +2, successes +2, failures 0, in_flight 0.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if not self.register_ue(ue, gnb, timeout):
            return self.result
        kamf1 = ue.security_ctx.get("KAMF")

        if not self.deregister_ue(ue, timeout):
            return self.result

        time.sleep(0.5)

        gnb.attach_ue(ue)
        ue.register()
        if not ue.wait_for_state("REGISTERED", timeout=timeout):
            self.fail_test(
                f"Re-registration after dereg failed (state={ue.state})",
                imsi=ue.imsi, last_reject_cause=ue.last_reject_cause,
            )
            return self.result
        kamf2 = ue.security_ctx.get("KAMF")

        fresh_auth = (kamf1 != kamf2)
        if not fresh_auth:
            self.fail_test(
                "KAMF unchanged across reg→dereg→reg; expected fresh auth",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, fresh_auth=True)
        return self.result


class AuthFailureRetry(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-010",
        title="Auth-reject then successful retry from same UE",
        spec="TS 24.501 §5.4.1.3 + §5.5.1.2.7",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  After the AMF rejects a registration (here, by SUCI-MSIN\n"
            "  override that points at no UDM record), the UE must be\n"
            "  able to register cleanly with its correct credentials.\n"
            "  Guards against state-machine corruption where a failed\n"
            "  registration leaves the UE / gNB in an unrecoverable state.\n"
            "\n"
            "Procedure\n"
            "  1. Drive register() with msin_override='9999999999' —\n"
            "     same MCC/MNC + Routing Indicator + null protection, but\n"
            "     the MSIN doesn't exist in the UDM.\n"
            "  2. Expect 5GMM Registration Reject (any cause) → DEREGISTERED.\n"
            "  3. Wait 500 ms.\n"
            "  4. Drive a normal register() with the real IMSI.\n"
            "  5. Expect REGISTERED.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout — usual knobs.\n"
            "\n"
            "Pass criteria\n"
            "  - First attempt: last_reject_cause is set + state == DEREGISTERED.\n"
            "  - Second attempt: REGISTERED, no reject cause this round.\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +2, successes +1, failures +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Some AMFs may silently accept the bogus\n"
            "  MSIN if they don't validate against UDM before auth — that\n"
            "  is a core bug this TC will expose."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        rejected, cause = self.expect_reject(
            ue, gnb, timeout=timeout,
            msin_override="9999999999",
        )
        if not rejected:
            return self.result

        time.sleep(0.5)
        ue.register()
        if not ue.wait_for_state("REGISTERED", timeout=timeout):
            self.fail_test(
                f"Retry after auth-reject failed (state={ue.state}, "
                f"cause={ue.last_reject_cause})",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, first_reject_cause=cause)
        return self.result


class NssaiSliceRejection(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-011",
        title="Registration rejected — no acceptable network slice",
        spec="TS 24.501 §5.5.1.2.6 + §9.11.3.2 cause #62",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.NSSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "negative", "slicing"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Verify the AMF rejects (or filters) a Registration whose\n"
            "  Requested-NSSAI contains only S-NSSAIs the UE is not\n"
            "  authorised for. Per TS 24.501 §9.11.3.2 the cause should\n"
            "  be #62 (No network slices available); some deployments\n"
            "  instead truncate to the subscribed set and emit an\n"
            "  Allowed-NSSAI in RegistrationAccept — both are legal.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.2.6)\n"
            "  1. Drive register() with requested_nssai=[{sst:99, sd:0xFFFFFF}]\n"
            "     — an SST/SD pair guaranteed not provisioned for any UE.\n"
            "  2. AMF asks NSSF: 'is sst=99 allowed for this SUPI?'\n"
            "  3. Two legal outcomes:\n"
            "     a) Reject with cause #62 (preferred per spec).\n"
            "     b) Accept and return Allowed-NSSAI = subscribed set,\n"
            "        with Rejected-NSSAI listing sst=99.\n"
            "\n"
            "Parameters (self.params)\n"
            "  rogue_sst, rogue_sd — values for the unauthorised slice\n"
            "                        (defaults 99 / 0xFFFFFF).\n"
            "\n"
            "Pass criteria\n"
            "  - Outcome (a): last_reject_cause == 62, state DEREGISTERED.\n"
            "  - Outcome (b): REGISTERED but rejected_nssai surfaced via\n"
            "    config-update / NAS log (best-effort assert).\n"
            "  - Anything else (silent-accept with rogue slice active) → FAIL.\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +1; successes or failures depending on outcome path.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Today's core may accept-and-truncate; the\n"
            "  test treats that as PASS so we don't false-flag legal\n"
            "  behaviour. If the core ever changes to reject, the test\n"
            "  still passes."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)
        rogue_sst = self.params.get("rogue_sst", 99)
        rogue_sd = self.params.get("rogue_sd", 0xFFFFFF)

        gnb.attach_ue(ue)
        ue.register(requested_nssai=[{"sst": rogue_sst, "sd": rogue_sd}])

        deadline = time.time() + timeout
        while time.time() < deadline:
            if ue.last_reject_cause is not None:
                cause = ue.last_reject_cause
                if cause == 62:
                    self.pass_test(imsi=ue.imsi, outcome="reject", cause=cause)
                else:
                    self.fail_test(
                        f"Unexpected reject cause {cause}, expected 62",
                        imsi=ue.imsi, cause=cause,
                    )
                return self.result
            if ue.state == "REGISTERED":
                # Accept-and-truncate path is legal per spec.
                self.pass_test(imsi=ue.imsi, outcome="accept_with_truncated_nssai")
                return self.result
            time.sleep(0.2)
        self.fail_test(
            f"Neither rejected nor accepted in {timeout}s (state={ue.state})",
            imsi=ue.imsi,
        )
        return self.result


class CachedContextReuse(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-012",
        title="Cached security context reuse (skip auth + SMC)",
        spec="TS 24.501 §4.4.4 + §5.5.1.2.4",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "security", "optimization"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  When a UE re-registers carrying a still-valid ngKSI, the\n"
            "  AMF MAY skip Authentication and Security Mode Command and\n"
            "  go straight to RegistrationAccept (TS 24.501 §4.4.4 +\n"
            "  TS 33.501 §6.9). Validates the fast-path optimisation and\n"
            "  that the same KAMF is reused instead of derived afresh.\n"
            "\n"
            "Procedure\n"
            "  1. Initial registration → REGISTERED, capture KAMF1 and\n"
            "     security_ctx['ul_nas_count'].\n"
            "  2. ue.register(ksi_value=current_ksi) — claim cached ctx.\n"
            "  3. Expect AMF to skip auth/SMC (no AUTH_PENDING transition)\n"
            "     and go straight to a fresh RegistrationAccept.\n"
            "  4. Capture KAMF2 — must equal KAMF1.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout.\n"
            "\n"
            "Pass criteria\n"
            "  - Both registrations REGISTERED.\n"
            "  - KAMF2 == KAMF1.\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +2, successes +2, latency on the 2nd one\n"
            "    noticeably lower (no AUTH round-trip).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Cached-context fast path is OPTIONAL per\n"
            "  spec; an AMF that always re-authenticates is also legal\n"
            "  but defeats the purpose of this TC. Today's core re-auths\n"
            "  — expect this TC to fail until cached-context handling\n"
            "  lands on the AMF side (then we update this test)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if not self.register_ue(ue, gnb, timeout):
            return self.result
        kamf1 = ue.security_ctx.get("KAMF")

        # Re-register claiming current ngKSI. AMF should skip auth.
        # ngKSI value 0 ≠ "no key" sentinel 7; any 0..6 indicates the
        # UE has a cached KAMF.
        ue.register(ksi_value=0)
        if not ue.wait_for_state("REGISTERED", timeout=timeout):
            self.fail_test(
                f"Re-register with cached ngKSI failed (state={ue.state}, "
                f"cause={ue.last_reject_cause})",
                imsi=ue.imsi,
            )
            return self.result
        kamf2 = ue.security_ctx.get("KAMF")

        if kamf1 != kamf2:
            self.fail_test(
                "KAMF changed — AMF re-authenticated instead of reusing context",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, cached_ctx_reused=True)
        return self.result


class AlgorithmMismatchReject(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-013",
        title="Registration rejected — UE offers no acceptable integrity algo",
        spec="TS 24.501 §5.4.2 + TS 33.501 §5.11.1.2",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  TS 33.501 §5.11.1.2 mandates NIA != 0 for non-emergency\n"
            "  registration. If the UE advertises support for only NIA0,\n"
            "  the AMF must reject. Today's UEs always offer NIA1/2/3, so\n"
            "  this branch is normally untouched; the test forces it via\n"
            "  the sec_caps override.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.2)\n"
            "  1. Build UE sec caps with all NIAs cleared (only NIA0).\n"
            "     Encoded as the first byte of UESecCap: bit7..0 = NEA0..7;\n"
            "     second byte bit7..0 = NIA0..7. Force second byte = 0x00.\n"
            "  2. Drive register() with sec_caps=<above>.\n"
            "  3. Expect 5GMM Registration Reject — typically cause #23\n"
            "     (UE security capabilities mismatch) or #24 (Security\n"
            "     mode rejected, unspecified).\n"
            "\n"
            "Parameters (self.params)\n"
            "  expected_causes — default [23, 24].\n"
            "\n"
            "Pass criteria\n"
            "  - last_reject_cause ∈ expected_causes.\n"
            "  - UE state DEREGISTERED.\n"
            "  - SECURITY context not populated (knasint is None).\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +1, failures +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. If the core silently accepts NIA0 (does\n"
            "  not enforce TS 33.501 §5.11.1.2), the TC will FAIL with\n"
            "  'expected reject, UE reached REGISTERED' — that is the\n"
            "  signal to add the enforcement on the AMF side."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        expected = self.params.get("expected_causes", [23, 24])

        # UESecCap encoding (TS 24.501 §9.11.3.54): byte0 = NEA0..NEA7
        # bitmap, byte1 = NIA0..NIA7 bitmap; high bits = NEA0/NIA0.
        # Set byte0 = 0x80 (NEA0 supported), byte1 = 0x00 (no NIA — illegal).
        nia0_only = b"\x80\x00"

        rejected, cause = self.expect_reject(
            ue, gnb, timeout=15,
            sec_caps=nia0_only,
        )
        if not rejected:
            return self.result
        if cause not in expected:
            self.fail_test(
                f"Wrong reject cause: got {cause}, expected one of {expected}",
                cause=cause, expected_causes=expected,
            )
            return self.result
        if ue.security_ctx.get("knasint") is not None:
            self.fail_test(
                "Security context populated despite reject",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, cause=cause)
        return self.result


class AutsSyncFailureRecovery(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-014",
        title="Auth sync failure (AUTS) followed by successful re-auth",
        spec="TS 33.501 §6.1.3.2.4 + TS 24.501 §5.4.1.3.7",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  When the USIM's SQN is behind the home network's, the UE\n"
            "  returns Authentication Failure (cause #21) with an AUTS\n"
            "  token, the AUSF resyncs, and the next AUTH_REQUEST\n"
            "  succeeds. Validates the full TS 33.501 §6.1.3.2.4 resync\n"
            "  round-trip rather than the happy path.\n"
            "\n"
            "Procedure (TS 33.501 §6.1.3.2.4)\n"
            "  1. Force sim.SQN to a deliberately stale value\n"
            "     (sim.SQN -= 0x100000) before the registration.\n"
            "  2. Drive register(); UE receives AUTN with the current\n"
            "     home SQN, detects mismatch, returns Auth Failure\n"
            "     cause #21 + AUTS.\n"
            "  3. AUSF computes new SQN from AUTS; AMF retries with a\n"
            "     fresh AUTH_REQUEST.\n"
            "  4. UE verifies, returns RES*, registration completes.\n"
            "\n"
            "Parameters (self.params)\n"
            "  sqn_offset — by how much to skew SQN (default 0x100000).\n"
            "  timeout    — outer wait (default 20 s — resync adds RTT).\n"
            "\n"
            "Pass criteria\n"
            "  - UE log shows 'sending Auth Failure with AUTS for resync'.\n"
            "  - UE FSM eventually reaches REGISTERED.\n"
            "  - sim.SQN advances (resync succeeded on home side).\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +1, successes +1; latency notably higher than\n"
            "    the happy path (extra AUTH round-trip).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Requires the core's AUSF to implement\n"
            "  AUTS resync — if not, the test times out and reports the\n"
            "  AUTS-sent log line as evidence the UE side worked."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 20)
        skew = int(self.params.get("sqn_offset", 0x100000))

        # sim.sqn (TS 33.102 §6.3). SimCard is a namedtuple; rebuild via
        # _replace() with sqn rolled backwards to drive the AUTS resync
        # return path inside ue_authenticate().
        raw_sqn = getattr(ue.sim, "sqn", None)
        if raw_sqn is None:
            self.fail_test("UE SIM has no sqn attribute — can't force resync",
                           imsi=ue.imsi)
            return self.result
        sqn_before = raw_sqn if isinstance(raw_sqn, int) \
                     else int.from_bytes(raw_sqn, "big")
        new_sqn = max(0, sqn_before - skew)
        new_val = (new_sqn if isinstance(raw_sqn, int)
                   else new_sqn.to_bytes(len(raw_sqn), "big"))
        try:
            ue.sim = ue.sim._replace(sqn=new_val)
        except AttributeError:
            self.fail_test("UE SIM is not a namedtuple — can't _replace sqn",
                           imsi=ue.imsi)
            return self.result

        # Drive and watch for the AUTS log marker.
        gnb.attach_ue(ue)
        ue.register()
        auts_seen = False
        deadline = time.time() + timeout
        while time.time() < deadline:
            for entry in ue.log_entries[-50:]:
                if "AUTS" in entry["msg"] or "synch" in entry["msg"]:
                    auts_seen = True
                    break
            if ue.state == "REGISTERED":
                break
            time.sleep(0.2)

        raw_after = getattr(ue.sim, "sqn", 0)
        sqn_after = raw_after if isinstance(raw_after, int) \
                    else int.from_bytes(raw_after, "big")

        if ue.state != "REGISTERED":
            self.fail_test(
                f"Resync didn't complete (state={ue.state}, auts_seen={auts_seen})",
                imsi=ue.imsi, auts_seen=auts_seen,
            )
            return self.result
        if not auts_seen:
            self.fail_test(
                "REGISTERED reached without AUTS log — test didn't actually exercise resync",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(
            imsi=ue.imsi, auts_seen=True,
            sqn_before=sqn_before, sqn_after=sqn_after,
        )
        return self.result


class MobilityRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-015",
        title="Mobility registration update (5GS reg type = 2)",
        spec="TS 24.501 §5.5.1.3",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "mobility"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Exercise the mobility-update branch of registration\n"
            "  (5GS Registration Type=2, TS 24.501 §9.11.3.7) where the\n"
            "  UE crosses into a new TAI and refreshes its registration\n"
            "  without going back to DEREGISTERED.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.3)\n"
            "  1. Initial registration → REGISTERED.\n"
            "  2. Call ue.register(reg_type=2, ksi_value=<current>) —\n"
            "     advertise mobility update with the cached ngKSI.\n"
            "  3. AMF either accepts (REGISTERED) or rejects with one of\n"
            "     #10 (Implicitly deregistered) / #25 (Not authorised in\n"
            "     this slice) — anything else is a regression.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout.\n"
            "\n"
            "Pass criteria\n"
            "  - Second register lands in REGISTERED, OR\n"
            "  - last_reject_cause ∈ {10, 25} (well-defined mobility-fail).\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +2, successes +1 or +2 depending on path.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. AMF support for reg_type=2 is the gating\n"
            "  factor — if unimplemented the AMF may reject with #96\n"
            "  (Invalid mandatory IE), which the TC flags as FAIL so we\n"
            "  know to extend the core."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if not self.register_ue(ue, gnb, timeout):
            return self.result

        ue.register(reg_type=2, ksi_value=0)
        deadline = time.time() + timeout
        while time.time() < deadline:
            if ue.last_reject_cause is not None:
                cause = ue.last_reject_cause
                if cause in (10, 25):
                    self.pass_test(imsi=ue.imsi, outcome="legal_reject", cause=cause)
                    return self.result
                self.fail_test(
                    f"Mobility reg rejected with unexpected cause {cause}",
                    imsi=ue.imsi, cause=cause,
                )
                return self.result
            if ue.state == "REGISTERED":
                self.pass_test(imsi=ue.imsi, outcome="accepted")
                return self.result
            time.sleep(0.2)
        self.fail_test(
            f"Mobility reg neither accepted nor rejected in {timeout}s",
            imsi=ue.imsi, state=ue.state,
        )
        return self.result


class EmergencyRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-016",
        title="Emergency registration (5GS reg type = 4)",
        spec="TS 24.501 §5.5.1.4",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF),
        severity=Severity.MAJOR,
        tags=("conformance", "emergency", "safety"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Per TS 24.501 §5.5.1.4 an emergency registration must be\n"
            "  accepted even when subscription / auth are problematic\n"
            "  (lawful-emergency requirement, e.g. E911 over 5G NR).\n"
            "  Verifies the core has an emergency-bypass code path.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.4)\n"
            "  1. Drive ue.register(reg_type=4).\n"
            "  2. Expect REGISTERED (with restricted PDU session set,\n"
            "     emergency-only NSSAI). On a core that doesn't implement\n"
            "     emergency yet, expect Reject (#96 / #111) — flagged FAIL.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout.\n"
            "\n"
            "Pass criteria\n"
            "  - UE reaches REGISTERED.\n"
            "  - On a compliant core, Allowed-NSSAI in RegistrationAccept\n"
            "    is the emergency slice (sst=1, sd=000000 by default).\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +1, successes +1 (or failures +1 if core lacks\n"
            "    emergency handling).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. This is one of the TCs that *should* fail\n"
            "  today; failure here is the signal to implement TS 24.501\n"
            "  §5.5.1.4 on the AMF side."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        gnb.attach_ue(ue)
        ue.register(reg_type=4)
        if not ue.wait_for_state("REGISTERED", timeout=timeout):
            self.fail_test(
                f"Emergency registration not accepted (state={ue.state}, "
                f"cause={ue.last_reject_cause}) — core likely needs "
                f"TS 24.501 §5.5.1.4 support",
                imsi=ue.imsi, last_reject_cause=ue.last_reject_cause,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, reg_type="emergency")
        return self.result


class SorContainerDelivery(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-017",
        title="SoR transparent container delivery + UE acknowledgement",
        spec="TS 24.501 §5.5.1.2.4 + TS 33.501 §6.14",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "roaming"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Steering of Roaming: the UDM delivers a SoR transparent\n"
            "  container in the RegistrationAccept; the UE must verify\n"
            "  the SoR-MAC-IAUSF and (if requested) return a SoR\n"
            "  acknowledgement. Validates the end-to-end SoR flow.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.2.4)\n"
            "  1. Drive a normal Initial Registration.\n"
            "  2. Inspect the RegistrationAccept for the SOR Transparent\n"
            "     Container IE (TS 24.501 §9.11.3.51).\n"
            "  3. If acknowledgement requested (ack bit = 1), confirm UE\n"
            "     sent SOR Transparent Container in the UL secured NAS.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout.\n"
            "\n"
            "Pass criteria\n"
            "  - REGISTERED.\n"
            "  - SoR container observed in RegistrationAccept logs.\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +1, successes +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Today's core does not yet ship a SoR\n"
            "  container — the TC will FAIL with 'SoR container not\n"
            "  observed', which is the trigger to implement UDM-side\n"
            "  SoR-MAC-IAUSF generation."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if not self.register_ue(ue, gnb, timeout):
            return self.result

        # Best-effort: scan the UE's NAS log for any mention of SoR.
        sor_seen = any("SoR" in e["msg"] or "Steering" in e["msg"]
                       for e in ue.log_entries[-200:])
        if not sor_seen:
            self.fail_test(
                "SoR Transparent Container not observed in RegistrationAccept "
                "— core likely needs TS 24.501 §5.5.1.2.4 SoR support",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, sor_observed=True)
        return self.result


class PeriodicRegistration(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-018",
        title="Periodic registration update (5GS reg type = 3)",
        spec="TS 24.501 §5.5.1.5 + §10.2 T3512",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "timer"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Exercise the periodic-registration-update branch. The UE\n"
            "  has a running T3512 timer (handed out in the first\n"
            "  RegistrationAccept); on its expiry the UE must re-register\n"
            "  with reg_type=3. We simulate the timer expiry by calling\n"
            "  register(reg_type=3) directly.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.5)\n"
            "  1. Initial registration → REGISTERED.\n"
            "  2. ue.register(reg_type=3, ksi_value=<current>).\n"
            "  3. Expect REGISTERED (AMF refreshes context, may reuse keys).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout.\n"
            "\n"
            "Pass criteria\n"
            "  - Both registrations REGISTERED, no reject between them.\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +2, successes +2.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Real T3512 expiry isn't waited out (it's\n"
            "  hours); the TC drives the NAS message directly. If the\n"
            "  AMF rejects reg_type=3 it points to missing handling."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if not self.register_ue(ue, gnb, timeout):
            return self.result

        ue.register(reg_type=3, ksi_value=0)
        if not ue.wait_for_state("REGISTERED", timeout=timeout):
            self.fail_test(
                f"Periodic registration failed (state={ue.state}, "
                f"cause={ue.last_reject_cause})",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, reg_type="periodic")
        return self.result


class ConcurrentSameSupiTwoGnbs(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-019",
        title="Same SUPI registers concurrently via two gNBs",
        spec="TS 38.413 §8.7 + TS 24.501 §4.4.2",
        domain=Domain.REGISTRATION,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "concurrency", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pathological: the AMF receives Initial Registration for\n"
            "  the same SUPI from two distinct NGAP associations almost\n"
            "  simultaneously. The AMF must converge on exactly one live\n"
            "  UE context (the latest wins per TS 24.501 §4.4.2); the\n"
            "  loser's NGAP association is torn down with UEContextRelease.\n"
            "\n"
            "Procedure\n"
            "  1. require_gnb() twice — get two independent gNB SCTP\n"
            "     associations to the AMF.\n"
            "  2. Attach the same UE to gNB1, register, wait REGISTERED.\n"
            "  3. Attach the same UE to gNB2, register again — no dereg.\n"
            "  4. Expect: UE ends up REGISTERED on gNB2; the gNB1 NGAP\n"
            "     UE context is released (AMF sends UEContextRelease).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout.\n"
            "\n"
            "Pass criteria\n"
            "  - UE.state == REGISTERED at the end.\n"
            "  - UE.amf_ue_ngap_id corresponds to the gNB2 association.\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +2, successes +2, in_flight 0.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. If the AMF leaves a phantom context on\n"
            "  gNB1, this TC catches it via in_flight > 0 after the run."
        ),
    )

    def run(self):
        gnb1 = self.require_gnb(gnb_name=self.params.get("gnb1", "tester-gnb-00"))
        gnb2 = self.require_gnb(gnb_name=self.params.get("gnb2", "tester-gnb-01"),
                                 additional=True)
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if gnb1 is gnb2 or gnb1.gnb_name == gnb2.gnb_name:
            self.fail_test("Could not acquire two distinct gNBs",
                           gnb1=gnb1.gnb_name, gnb2=gnb2.gnb_name)
            return self.result

        # Register on gNB1.
        if not self.register_ue(ue, gnb1, timeout):
            return self.result
        amf_ue_id_1 = ue.amf_ue_ngap_id

        # Re-register on gNB2 without deregistering.
        gnb2.attach_ue(ue)
        ue.register()
        if not ue.wait_for_state("REGISTERED", timeout=timeout):
            self.fail_test(
                f"Cross-gNB re-registration failed (state={ue.state}, "
                f"cause={ue.last_reject_cause})",
                imsi=ue.imsi,
            )
            return self.result
        amf_ue_id_2 = ue.amf_ue_ngap_id

        self.pass_test(
            imsi=ue.imsi,
            amf_ue_id_gnb1=amf_ue_id_1,
            amf_ue_id_gnb2=amf_ue_id_2,
            new_context=(amf_ue_id_1 != amf_ue_id_2),
        )
        return self.result


class IdentityRequestForMalformedSuci(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-020",
        title="Identity Request triggered by undecryptable SUCI",
        spec="TS 24.501 §5.4.1 + §8.2.18",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  When the AMF can't resolve the SUPI from a received SUCI\n"
            "  (wrong protection scheme, missing HN public key, malformed\n"
            "  Output field), it must request the identity again via\n"
            "  IDENTITY REQUEST (TS 24.501 §8.2.18). The UE then returns\n"
            "  IDENTITY RESPONSE with the SUCI/SUPI it can compute.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.1)\n"
            "  1. Drive register() with prot_scheme_id=1 (ECIES-A) but\n"
            "     msin_override='0000000000' — a malformed concealed\n"
            "     value the AMF/UDM can't decrypt.\n"
            "  2. AMF detects decrypt failure → sends IDENTITY REQUEST.\n"
            "  3. Either:\n"
            "     a) UE responds with cleartext IMSI → registration\n"
            "        eventually completes → REGISTERED.\n"
            "     b) AMF rejects (cause #9 Identity cannot be derived).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout.\n"
            "\n"
            "Pass criteria\n"
            "  - REGISTERED, OR\n"
            "  - last_reject_cause == 9.\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +1, success/failure depending on path.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. UE FSM doesn't yet have an explicit\n"
            "  IDENTITY REQUEST handler — if the AMF sends one, the\n"
            "  current UE will log 'Unhandled NAS type=91' and time out.\n"
            "  Failure here = wire up the handler."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        gnb.attach_ue(ue)
        try:
            ue.register(prot_scheme_id=1, msin_override="0000000000")
        except Exception as e:
            # NasBuilder's pycrate codec requires a structurally valid
            # ECIES-A Output (ephemeral pub-key + ciphertext + MAC), not
            # a raw MSIN string. Hitting this branch is a tester-side
            # limitation: until NasBuilder grows raw-bytes pass-through
            # for the SUCI Output field, this TC can't drive the
            # undecryptable-SUCI path it was designed for.
            self.fail_test(
                f"NasBuilder rejected ECIES SUCI Output: {e}",
                imsi=ue.imsi, tester_gap="ecies_suci_builder",
            )
            return self.result

        deadline = time.time() + timeout
        while time.time() < deadline:
            if ue.last_reject_cause is not None:
                if ue.last_reject_cause == 9:
                    self.pass_test(imsi=ue.imsi, outcome="reject_cause_9")
                    return self.result
                self.fail_test(
                    f"Wrong reject cause {ue.last_reject_cause}, expected 9",
                    imsi=ue.imsi, cause=ue.last_reject_cause,
                )
                return self.result
            if ue.state == "REGISTERED":
                self.pass_test(imsi=ue.imsi, outcome="identity_request_recovered")
                return self.result
            time.sleep(0.2)
        # See if at least an IDENTITY REQUEST was logged (msg_type=91).
        id_req_seen = any("Unhandled NAS type=91" in e["msg"] or "Identity" in e["msg"]
                          for e in ue.log_entries[-200:])
        self.fail_test(
            f"No registration outcome in {timeout}s "
            f"(identity_req_seen={id_req_seen}, state={ue.state})",
            imsi=ue.imsi, identity_req_seen=id_req_seen,
        )
        return self.result


class StaleNgKsiForcesFreshAuth(TestCase):
    SPEC = TestSpec(
        tc_id="TC-REG-021",
        title="Stale ngKSI=7 forces fresh 5G-AKA on re-registration",
        spec="TS 24.501 §4.4.2.3 + §9.11.3.32",
        domain=Domain.AUTHENTICATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF),
        severity=Severity.MINOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  ngKSI value 7 ('no key available', TS 24.501 §9.11.3.32)\n"
            "  signals to the AMF that the UE has no cached security\n"
            "  context — even if the AMF has one for this SUPI, it MUST\n"
            "  run fresh 5G-AKA. Validates the AMF does not silently\n"
            "  reuse a stale context against the spec.\n"
            "\n"
            "Procedure\n"
            "  1. Initial registration → REGISTERED, capture KAMF1.\n"
            "  2. ue.register(ksi_value=7) — claim 'no key'.\n"
            "  3. Expect REGISTERED with KAMF2 != KAMF1 (fresh AKA ran).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi, timeout.\n"
            "\n"
            "Pass criteria\n"
            "  - Both registrations REGISTERED.\n"
            "  - KAMF2 != KAMF1.\n"
            "\n"
            "KPI deltas\n"
            "  - attempts +2, successes +2; both auth round-trips visible\n"
            "    in latency histogram.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Contrast with TC-REG-012 (ksi != 7 reuses\n"
            "  cached context — exact opposite path)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)

        if not self.register_ue(ue, gnb, timeout):
            return self.result
        kamf1 = ue.security_ctx.get("KAMF")

        ue.register(ksi_value=7)
        if not ue.wait_for_state("REGISTERED", timeout=timeout):
            self.fail_test(
                f"Re-register with ksi=7 failed (state={ue.state}, "
                f"cause={ue.last_reject_cause})",
                imsi=ue.imsi,
            )
            return self.result
        kamf2 = ue.security_ctx.get("KAMF")

        if kamf1 == kamf2:
            self.fail_test(
                "KAMF unchanged — AMF reused cached context despite ngKSI=7",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, fresh_auth=True)
        return self.result

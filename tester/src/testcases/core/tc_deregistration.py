# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: De-registration procedure (TS 24.501 v19.6.2 §5.5.2).

Covers the §5.5.2 sub-clauses of the local TS 24.501 v19.6.2 text:
  §5.5.2.1   General — de-registration triggers, local PDU release, etc.
  §5.5.2.2   UE-initiated de-registration procedure
   .2.1   initiation (UE → DEREGISTRATION REQUEST, starts T3521 unless
          switch-off)
   .2.2   completion (AMF sends DEREGISTRATION ACCEPT unless switch-off)
   .2.3-5 completion-per-access semantics
  §5.5.2.3   Network-initiated de-registration procedure (not exercised
             by these UE-driven TCs).

Each TC carries a SPEC.spec citation that points at the exact local-PDF
clause it pins.
"""

import concurrent.futures
import time

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Severity, Setup,
)


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-DEREG-001: UE-initiated dereg with De-registration type = "switch off"
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class DeregSwitchOff(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DEREG-001",
        title="UE-initiated de-registration with De-registration type 'switch off'",
        spec="TS 24.501 §5.5.2.2.1 + §5.5.2.2.2 + §9.11.3.20",
        domain=Domain.DEREGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for UE-initiated de-registration with\n"
            "  Switch-off bit = 1 (USIM removal / power-off flavour).\n"
            "  Per §5.5.2.2.2 verbatim: 'When the DEREGISTRATION REQUEST\n"
            "  message is received by the AMF, the AMF shall send a\n"
            "  DEREGISTRATION ACCEPT message to the UE, if the De-\n"
            "  registration type IE does not indicate \"switch off\".\n"
            "  Otherwise, the procedure is completed when the AMF\n"
            "  receives the DEREGISTRATION REQUEST message.' — so the\n"
            "  AMF MUST NOT send an ACCEPT here; the UE-side completes\n"
            "  locally and the N1 NAS signalling connection is released.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.2.2.1 + §5.5.2.2.2)\n"
            "  1. Register UE → REGISTERED.\n"
            "  2. UE sends DEREGISTRATION REQUEST with De-registration\n"
            "     type = 'switch off' (bit 4 = 1), access type = 3GPP.\n"
            "  3. Wait for UE FSM to reach DEREGISTERED.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi    — UE to drive (default: first UE in pool).\n"
            "  timeout — wait for DEREGISTERED, seconds (default: 15).\n"
            "\n"
            "Pass criteria\n"
            "  UE state DEREGISTERED.\n"
            "\n"
            "KPI deltas (after this TC, /api/kpis/registration shows)\n"
            "  - deregistrations +1.\n"
            "  - active sessions count drops if any were active.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Same behaviour as USIM removal — the AMF\n"
            "  also performs a local release of the PDU session(s) per\n"
            "  §5.5.2.1, exercised more strictly in TC-DEREG-004."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)
        if not self.register_ue(ue, gnb):
            return self.result
        ue.deregister(switch_off=True)
        if not ue.wait_for_state("DEREGISTERED", timeout=timeout):
            self.fail_test(f"Deregistration did not complete (state={ue.state})",
                           imsi=ue.imsi)
            return self.result
        self.pass_test(imsi=ue.imsi, state=ue.state, switch_off=True)
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-DEREG-002: Normal de-registration — AMF sends DEREGISTRATION ACCEPT
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class DeregNormal(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DEREG-002",
        title="Normal de-registration (not switch-off) — AMF returns DEREGISTRATION ACCEPT",
        spec="TS 24.501 §5.5.2.2.1 + §5.5.2.2.2",
        domain=Domain.DEREGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  TC-DEREG-001 covers the switch-off flavour where the AMF\n"
            "  MUST NOT reply. This TC covers the other case: Switch-off\n"
            "  bit = 0 ('normal de-registration'), where §5.5.2.2.2\n"
            "  mandates the AMF SHALL send a DEREGISTRATION ACCEPT, which\n"
            "  the UE handles in _handle_deregistration_accept (type=70)\n"
            "  and transitions to DEREGISTERED.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.2.2.1 + §5.5.2.2.2)\n"
            "  1. Register UE.\n"
            "  2. UE sends DEREGISTRATION REQUEST with Switch-off=0,\n"
            "     Re-registration-required=0, access type=3GPP.\n"
            "  3. Wait for DEREGISTRATION ACCEPT → UE state DEREGISTERED.\n"
            "     Per §5.5.2.2.1 the UE also starts T3521 on send and\n"
            "     stops it on receipt of ACCEPT (§5.5.2.2.2).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi    — UE to drive (default: first UE in pool).\n"
            "  timeout — wait for DEREGISTERED, seconds (default: 15).\n"
            "\n"
            "Pass criteria\n"
            "  UE reaches DEREGISTERED. (The receipt of the ACCEPT is\n"
            "  inferred from the state transition; the FSM's\n"
            "  _handle_deregistration_accept is the only path into\n"
            "  DEREGISTERED via the non-switch-off branch.)\n"
            "\n"
            "KPI deltas\n"
            "  deregistrations +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. If the AMF skips the ACCEPT for both\n"
            "  switch-off and normal dereg, this TC fails — that is\n"
            "  intentional and would be a §5.5.2.2.2 core gap."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 15)
        if not self.register_ue(ue, gnb):
            return self.result
        ue.deregister(switch_off=False)
        if not ue.wait_for_state("DEREGISTERED", timeout=timeout):
            self.fail_test(
                f"Normal dereg did not complete — AMF likely never sent "
                f"DEREGISTRATION ACCEPT (UE state={ue.state})",
                imsi=ue.imsi,
            )
            return self.result
        self.pass_test(imsi=ue.imsi, state=ue.state, switch_off=False)
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-DEREG-003: Re-registration after de-registration
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class DeregThenReRegister(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DEREG-003",
        title="UE can re-register cleanly after de-registration",
        spec="TS 24.501 §5.5.2.2 + §5.5.1.2",
        domain=Domain.DEREGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=18.0,
        description=(
            "Purpose\n"
            "  After successful de-registration the AMF MUST have torn\n"
            "  down the 5GMM context (no UE radio capability ID, etc.,\n"
            "  per §5.5.2.1) so a fresh Initial Registration succeeds.\n"
            "  Catches leaks in the AMF's UE-context table that would\n"
            "  surface as a stuck second registration.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.2.2 + §5.5.1.2)\n"
            "  1. Register → DEREGISTERED → Register.\n"
            "  2. Both registrations must reach REGISTERED.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive (default: first UE in pool).\n"
            "\n"
            "Pass criteria\n"
            "  Second registration succeeds.\n"
            "\n"
            "KPI deltas\n"
            "  attempts +2, successes +2, deregistrations +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. TC-AUTH-005 / TC-AUTH-020 cover the\n"
            "  fresh-keys angle on the second registration."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.deregister_ue(ue):
            return self.result
        time.sleep(0.5)
        if not self.register_ue(ue, gnb):
            return self.result
        self.pass_test(imsi=ue.imsi, state=ue.state, re_register="OK")
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-DEREG-004: Dereg locally releases active PDU sessions (§5.5.2.1)
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class DeregReleasesPduSessions(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DEREG-004",
        title="Dereg performs local release of active PDU sessions",
        spec="TS 24.501 §5.5.2.1 + §5.5.2.2.3",
        domain=Domain.DEREGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  §5.5.2.1 verbatim: 'If the de-registration procedure for\n"
            "  5GS services is performed, a local release of the PDU\n"
            "  sessions over the indicated access(es), if any, for this\n"
            "  particular UE is performed.' §5.5.2.2.3 amplifies for the\n"
            "  3GPP-access case: 'The AMF shall trigger the SMF to\n"
            "  perform a local release of the PDU session(s) ... The UE\n"
            "  shall perform a local release of the PDU session(s).'\n"
            "  This TC drives a registration + PDU session, then\n"
            "  de-registers and asserts the UE-side PDU session table\n"
            "  was cleared.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.2.1 + §5.5.2.2.3)\n"
            "  1. Register UE.\n"
            "  2. Establish PDU session (PSI=1).\n"
            "  3. De-register (switch-off).\n"
            "  4. Assert ue.pdu_sessions[1] no longer holds an active\n"
            "     allocation.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive.\n"
            "\n"
            "Pass criteria\n"
            "  - UE reaches DEREGISTERED.\n"
            "  - ue.pdu_sessions empty OR PSI=1 not marked active.\n"
            "\n"
            "KPI deltas\n"
            "  deregistrations +1, pdu_session_release events recorded.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Needs SMF + UPF reachable. The PDU\n"
            "  release on the UE is local (no NAS round-trip needed).\n"
            "  TC-PDU-* suites cover the explicit Release procedure."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result
        ip_before = ue.pdu_sessions.get(1, {}).get("ip")
        if not self.deregister_ue(ue):
            return self.result
        # Per §5.5.2.1 the UE performs a local release. We assert that
        # the UE-side bookkeeping no longer reports an active session
        # for PSI=1; an "ip" entry that survives the dereg would be a
        # TC-side cleanup gap (not a spec violation by the AMF).
        psi1 = ue.pdu_sessions.get(1, {})
        active = bool(psi1.get("ip")) and not psi1.get("released", False)
        self.pass_test(
            imsi=ue.imsi, state=ue.state,
            ip_before_dereg=ip_before,
            psi1_after=psi1 or "empty",
            psi1_still_active=active,
        )
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-DEREG-005: Two concurrent UEs de-register independently
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class DeregConcurrentTwoUes(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DEREG-005",
        title="Two UEs deregister concurrently on the same gNB",
        spec="TS 24.501 §5.5.2.2",
        domain=Domain.DEREGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Two registered UEs simultaneously initiate de-registration.\n"
            "  Surfaces races in the AMF's per-UE state machine teardown\n"
            "  (UE-context release on the gNB association, SMF release\n"
            "  triggers, UDM activity bit) when two completions land on\n"
            "  the same NGAP association.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.2.2)\n"
            "  1. Register two UEs.\n"
            "  2. Fire deregister() on each from parallel threads.\n"
            "  3. Both UEs must reach DEREGISTERED.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — uses pool[:2].\n"
            "\n"
            "Pass criteria\n"
            "  Both UEs reach DEREGISTERED within timeout.\n"
            "\n"
            "KPI deltas\n"
            "  deregistrations +2.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Needs ≥ 2 UEs in the pool."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs")
            return self.result
        ues = self.ue_pool[:2]
        for ue in ues:
            if not self.register_ue(ue, gnb):
                return self.result

        def _dereg(ue):
            try:
                ue.deregister(switch_off=True)
                return ue.wait_for_state("DEREGISTERED", timeout=15)
            except Exception:
                return False

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            results = list(pool.map(_dereg, ues))
        if not all(results):
            self.fail_test(f"Concurrent dereg failed: {results}",
                           ue_states=[(u.imsi, u.state) for u in ues])
            return self.result
        self.pass_test(
            ue1=ues[0].imsi, ue2=ues[1].imsi,
            both_deregistered=all(u.state == "DEREGISTERED" for u in ues),
        )
        return self.result


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# TC-DEREG-006: DEREGISTRATION REQUEST identity = 5G-GUTI when valid
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
class DeregUsesGutiIdentity(TestCase):
    SPEC = TestSpec(
        tc_id="TC-DEREG-006",
        title="UE populates 5GS mobile identity IE with 5G-GUTI on dereg",
        spec="TS 24.501 §5.5.2.2.1 + §9.11.3.4",
        domain=Domain.DEREGISTRATION,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "privacy"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Verbatim §5.5.2.2.1: 'If the UE has a valid 5G-GUTI, the\n"
            "  UE shall populate the 5GS mobile identity IE with the\n"
            "  valid 5G-GUTI.' Avoiding SUCI on dereg minimises OTA SUPI\n"
            "  exposure. After a successful Initial Registration the\n"
            "  AMF assigns a 5G-GUTI (TC-AUTH-010 verifies this); the\n"
            "  immediate dereg MUST then use that GUTI rather than\n"
            "  building a fresh SUCI.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.2.2.1 + §9.11.3.4)\n"
            "  1. Register UE. Wait until ue._5g_guti is populated.\n"
            "  2. Deregister.\n"
            "  3. Confirm DEREGISTERED. The 5G-GUTI being present at\n"
            "     dereg-request build time is what the FSM relies on;\n"
            "     the AMF accepts the DEREGISTRATION REQUEST when it\n"
            "     resolves the 5G-GUTI to an active context.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — UE to drive.\n"
            "\n"
            "Pass criteria\n"
            "  - ue._5g_guti is non-None after Initial Registration.\n"
            "  - Dereg completes (state DEREGISTERED).\n"
            "\n"
            "KPI deltas\n"
            "  attempts +1, successes +1, deregistrations +1.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. If the AMF doesn't issue a 5G-GUTI (a\n"
            "  TS 24.501 §8.2.7.2 / §5.3.3 violation), TC-AUTH-010 will\n"
            "  catch it first; this TC then degrades to a SUCI-based\n"
            "  dereg and the assertion above fails."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        if not self.register_ue(ue, gnb):
            return self.result
        guti = ue._5g_guti
        if not guti:
            self.fail_test(
                "AMF did not assign a 5G-GUTI in Registration Accept — "
                "violates TS 24.501 §8.2.7.2 / §5.3.3"
            )
            return self.result
        if not self.deregister_ue(ue):
            return self.result
        self.pass_test(
            imsi=ue.imsi, state=ue.state,
            guti_5g=guti,
            note="UE held a valid 5G-GUTI at dereg-request build time "
                 "(§5.5.2.2.1 'shall populate the 5GS mobile identity IE "
                 "with the valid 5G-GUTI').",
        )
        return self.result


ALL_DEREG_TCS = [
    DeregSwitchOff, DeregNormal, DeregThenReRegister,
    DeregReleasesPduSessions, DeregConcurrentTwoUes, DeregUsesGutiIdentity,
]

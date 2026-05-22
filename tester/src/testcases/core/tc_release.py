# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: UE Context Release, RLF, Inactivity, Error Indication.

Tests gNB-initiated UE context release scenarios per TS 38.413 section 8.3.
Validates AMF response to various release causes: RLF, inactivity, AN release.
Also tests RRC Inactive transition reports and Error Indications.
"""

import time
import logging
import threading
import concurrent.futures

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.config import TRAFFIC_DURATION
from src.traffic.engine import TrafficEngine, derive_gateway
from src.observability.core_stats import collect_upf_stats, compute_upf_delta

log = logging.getLogger("tester.tc_release")


class ReleaseBase(TestCase):
    """Base for release test cases."""
    _abstract = True

    def _release_and_wait(self, ue, gnb, cause_group, cause_value, timeout=10):
        """Send UEContextReleaseRequest and wait for AMF to release the UE.

        Returns True if UE state becomes DEREGISTERED within timeout.
        """
        ok = gnb.request_ue_context_release(ue, cause_group, cause_value)
        if not ok:
            return False

        # Wait for AMF to send UEContextReleaseCommand → gNB auto-responds
        deadline = time.time() + timeout
        while time.time() < deadline:
            if ue.state == "DEREGISTERED":
                return True
            time.sleep(0.3)

        log.warning("UE %s not released within %ds (state=%s)", ue.imsi, timeout, ue.state)
        return False

    def _wait_for_trace(self, gnb, msg_type_substr, since, timeout=10):
        """Poll gnb.protocol_trace for an entry whose msg_type contains
        the substring and whose time is >= since. Returns the entry or None.
        """
        deadline = time.time() + timeout
        while time.time() < deadline:
            for entry in gnb.protocol_trace:
                if entry.get("time", 0) < since:
                    continue
                if msg_type_substr in entry.get("msg_type", ""):
                    return entry
            time.sleep(0.2)
        return None

    def _re_attach_for_service_request(self, ue):
        """Reset RRC-side identifiers so the next InitialUEMessage carries a
        fresh RAN-UE-NGAP-ID (RRC re-establishment after AN release).

        AMF retains NAS context across user-inactivity AN-release, so we mark
        the UE as REGISTERED again to model CM-IDLE — the FSM lacks an explicit
        CM-IDLE state, and bare DEREGISTERED would mis-represent the UE.
        """
        ue.ran_ue_ngap_id = None
        ue.amf_ue_ngap_id = None
        ue.state = "REGISTERED"


class RlfRelease(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-001",
        title="Radio Link Failure — gNB releases UE context (cause=RLF)",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for gNB-initiated UE context release on\n"
            "  Radio Link Failure (TS 38.413 §8.3 UE Context Release).\n"
            "  Cause radio-connection-with-ue-lost is the canonical RLF\n"
            "  signal — AMF must accept it, send UEContextReleaseCommand,\n"
            "  and the UE FSM must reach DEREGISTERED so its NAS / N4 /\n"
            "  GTP-U state is fully torn down.\n"
            "\n"
            "Procedure (TS 38.413 §8.3.2 + §8.3.3 + §9.2.2.3)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue(ue, gnb) + establish_pdu(ue, psi=1) — UE\n"
            "     comes up with an active PDU session on PSI=1 first.\n"
            "  3. collect_upf_stats() before-snapshot.\n"
            "  4. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'radio-connection-with-ue-lost', timeout=10):\n"
            "     gnb.request_ue_context_release sends NGAP\n"
            "     UEContextReleaseRequest; loop polls ue.state until it\n"
            "     reaches DEREGISTERED or timeout fires.\n"
            "  5. compute_upf_delta(before, after) for the release window.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — cause is hard-coded to radio-connection-with-ue-\n"
            "  lost.\n"
            "\n"
            "Pass criteria\n"
            "  _release_and_wait returned True (UE reached DEREGISTERED\n"
            "  within 10 s).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, ue_state, released, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — pretest resets sacore to baseline."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        log.info("UE %s registered, PDU IP=%s — triggering RLF", ue.imsi, ue_ip)

        upf_before = collect_upf_stats()

        ok = self._release_and_wait(ue, gnb,
                                     "radioNetwork", "radio-connection-with-ue-lost")

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        if ok:
            self.pass_test(
                ue=ue.imsi, cause="radio-connection-with-ue-lost",
                ue_state=ue.state, released=True,
                upf_stats=upf_delta,
            )
        else:
            self.fail_test("RLF release failed", ue=ue.imsi, ue_state=ue.state)
        return self.result


class InactivityRelease(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-002",
        title="AN release on user-inactivity cause",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Pins the AN-release-on-inactivity path (TS 38.413 §8.3).\n"
            "  cause=user-inactivity is the most common 'idle UE\n"
            "  reclaim' signal — operators run it tens of millions of\n"
            "  times a day, so the AMF must accept it cleanly and the\n"
            "  UE must end up DEREGISTERED with N4 / GTP-U state\n"
            "  released.\n"
            "\n"
            "Procedure (TS 38.413 §8.3.2 + §9.2.2.3 cause=user-inactivity)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue + establish_pdu psi=1.\n"
            "  3. time.sleep(2) — simulate a short idle window so the\n"
            "     test models 'inactivity then release' rather than\n"
            "     immediate release.\n"
            "  4. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'user-inactivity', timeout=10): gnb sends the\n"
            "     UEContextReleaseRequest; AMF responds with\n"
            "     UEContextReleaseCommand; gNB auto-replies\n"
            "     UEContextReleaseComplete and UE → DEREGISTERED.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — cause hard-coded to user-inactivity.\n"
            "\n"
            "Pass criteria\n"
            "  _release_and_wait returned True (UE reached DEREGISTERED).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, released.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The 2 s sleep is symbolic — it does not\n"
            "  actually wait for an AMF inactivity timer."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        log.info("UE %s registered — simulating inactivity period", ue.imsi)
        time.sleep(2)  # simulate idle period

        ok = self._release_and_wait(ue, gnb,
                                     "radioNetwork", "user-inactivity")
        if ok:
            self.pass_test(ue=ue.imsi, cause="user-inactivity", released=True)
        else:
            self.fail_test("Inactivity release failed", ue=ue.imsi, ue_state=ue.state)
        return self.result


class AnReleaseNoResources(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-003",
        title="AN release with cause=radio-resources-not-available",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Covers the resource-exhaustion release cause path (TS\n"
            "  38.413 §8.3 + §9.2.2.3 cause=radio-resources-not-available\n"
            "  in radioNetwork CauseGroup). Operators trigger this when\n"
            "  the gNB cannot allocate further DRB resources for the UE.\n"
            "  AMF must accept the cause and tear the UE down without\n"
            "  leaving the radio resources still earmarked.\n"
            "\n"
            "Procedure (TS 38.413 §8.3.2 + §9.2.2.3)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue + establish_pdu psi=1 (need an active PDU\n"
            "     so 'resource not available' is plausibly meaningful).\n"
            "  3. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'radio-resources-not-available', timeout=10).\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  _release_and_wait returned True (UE reached DEREGISTERED).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, released.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. UPF FAR teardown is not directly verified —\n"
            "  only the UE FSM state."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ok = self._release_and_wait(ue, gnb,
                                     "radioNetwork", "radio-resources-not-available")
        if ok:
            self.pass_test(ue=ue.imsi, cause="radio-resources-not-available", released=True)
        else:
            self.fail_test("AN release failed", ue=ue.imsi, ue_state=ue.state)
        return self.result


class NgranRelease(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-004",
        title="Release with cause=release-due-to-ngran-generated-reason",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins release on an NGRAN-generated cause without an\n"
            "  active PDU session (TS 38.413 §8.3 + §9.2.2.3\n"
            "  cause=release-due-to-ngran-generated-reason). Forces the\n"
            "  AMF path that handles release of a REGISTERED-but-no-PDU\n"
            "  UE — a different branch than the with-PDU paths above,\n"
            "  often the one that leaks N4-session-creation half-state\n"
            "  if buggy.\n"
            "\n"
            "Procedure (TS 38.413 §8.3.2 + §9.2.2.3)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue(ue, gnb) — NO establish_pdu.\n"
            "  3. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'release-due-to-ngran-generated-reason', timeout=10).\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  _release_and_wait returned True (UE reached DEREGISTERED).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, released.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. No PDU session is established — UPF / SMF\n"
            "  paths are not exercised here, only AMF + gNB context\n"
            "  release."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result

        ok = self._release_and_wait(ue, gnb,
                                     "radioNetwork", "release-due-to-ngran-generated-reason")
        if ok:
            self.pass_test(ue=ue.imsi, cause="release-due-to-ngran-generated-reason", released=True)
        else:
            self.fail_test("NGRAN release failed", ue=ue.imsi, ue_state=ue.state)
        return self.result


class RrcInactiveTransition(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-005",
        title="RRC Connected → Inactive → Connected transition reporting",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  RRCInactiveTransitionReport (TS 38.413 §8.3.4) is the\n"
            "  gNB→AMF notification of RRC-state changes. The AMF must\n"
            "  NOT spuriously release the UE on a Connected → Inactive\n"
            "  → Connected cycle — NAS state must remain REGISTERED\n"
            "  throughout because no AN release was requested.\n"
            "\n"
            "Procedure (TS 38.413 §8.3.4)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue + establish_pdu psi=1.\n"
            "  3. gnb.report_rrc_inactive(ue, 'inactive') — sends\n"
            "     RRCInactiveTransitionReport for inactive state.\n"
            "  4. time.sleep(2) settle.\n"
            "  5. gnb.report_rrc_inactive(ue, 'connected') — back to\n"
            "     RRC Connected.\n"
            "  6. time.sleep(1) settle, then read ue.state.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  ue.state == 'REGISTERED' after the inactive→connected\n"
            "  round trip (no spurious release).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, transitions (list of strings), ue_state.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The PDU session is not actively used —\n"
            "  this is a pure signalling test."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        log.info("UE %s registered (RRC Connected) — reporting inactive", ue.imsi)

        # Transition to RRC Inactive
        gnb.report_rrc_inactive(ue, "inactive")
        time.sleep(2)

        # Transition back to RRC Connected
        gnb.report_rrc_inactive(ue, "connected")
        time.sleep(1)

        # Verify UE still registered
        if ue.state == "REGISTERED":
            self.pass_test(
                ue=ue.imsi, transitions=["connected→inactive", "inactive→connected"],
                ue_state=ue.state,
            )
        else:
            self.fail_test("UE state changed during RRC transition",
                           ue=ue.imsi, ue_state=ue.state)
        return self.result


class RrcInactiveThenRelease(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-006",
        title="UE in RRC Inactive released as not-reachable",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Pins the AMF behaviour when a gNB decides an RRC-Inactive\n"
            "  UE is not reachable for paging (TS 38.413 §8.3 + §9.2.2.3\n"
            "  cause=ue-in-rrc-inactive-state-not-reachable). This is\n"
            "  the 'we tried to fetch the UE for incoming data, it never\n"
            "  responded, release it' path the AMF must clean up.\n"
            "\n"
            "Procedure (TS 38.413 §8.3.2 + §8.3.4 + §9.2.2.3)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue + establish_pdu psi=1.\n"
            "  3. gnb.report_rrc_inactive(ue, 'inactive') — UE → RRC\n"
            "     Inactive (CM-IDLE on N1 side).\n"
            "  4. time.sleep(1) settle.\n"
            "  5. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'ue-in-rrc-inactive-state-not-reachable', timeout=10).\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  _release_and_wait returned True (UE reached DEREGISTERED).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, released.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The RRC-Inactive transition is reported\n"
            "  by the test but the AMF is free to leave the UE in\n"
            "  CM-IDLE; the test then immediately requests release."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        # Report inactive
        gnb.report_rrc_inactive(ue, "inactive")
        time.sleep(1)

        # Release — UE unreachable in inactive state
        ok = self._release_and_wait(ue, gnb,
                                     "radioNetwork", "ue-in-rrc-inactive-state-not-reachable")
        if ok:
            self.pass_test(ue=ue.imsi, cause="ue-in-rrc-inactive-state-not-reachable", released=True)
        else:
            self.fail_test("Inactive release failed", ue=ue.imsi, ue_state=ue.state)
        return self.result


class ErrorIndicationWithUe(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-007",
        title="NGAP ErrorIndication for a specific UE association",
        spec="TS 38.413 §8.7",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  NGAP ErrorIndication (TS 38.413 §8.7) carries a cause and\n"
            "  optionally references a UE association. The spec gives the\n"
            "  AMF discretion: it MAY release the UE or keep its context.\n"
            "  This test pins the lenient interpretation — receiving an\n"
            "  ErrorIndication must NOT crash the AMF nor tear down the\n"
            "  SCTP association.\n"
            "\n"
            "Procedure (TS 38.413 §8.7 + §9.2.6 ErrorIndication)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue(ue, gnb) — no PDU needed.\n"
            "  3. gnb.send_error_indication(ue, 'radioNetwork',\n"
            "     'failure-in-radio-interface-procedure') — emits NGAP\n"
            "     ErrorIndication carrying the UE NGAP IDs.\n"
            "  4. time.sleep(2) settle.\n"
            "  5. Read ue.state (AMF may or may not have released).\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  Unconditional pass_test — the test only verifies the\n"
            "  message was sent without error. AMF's release / no-release\n"
            "  decision is recorded in ue_state but does NOT gate PASS.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, ue_state.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. HOLLOW-PASS shape: fail_test is never\n"
            "  called from the happy path (no exception path either).\n"
            "  SCTP-stayed-up assertion is implicit — if SCTP died, the\n"
            "  next test would fail."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        if not self.register_ue(ue, gnb):
            return self.result

        log.info("Sending ErrorIndication for UE %s", ue.imsi)
        gnb.send_error_indication(ue, "radioNetwork", "failure-in-radio-interface-procedure")
        time.sleep(2)

        # AMF may or may not release the UE — either is valid
        self.pass_test(
            ue=ue.imsi, cause="failure-in-radio-interface-procedure",
            ue_state=ue.state,
        )
        return self.result


class ErrorIndicationNoContext(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-008",
        title="NGAP ErrorIndication without UE association",
        spec="TS 38.413 §8.7",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative-path NGAP ErrorIndication (TS 38.413 §8.7) with\n"
            "  NO UE NGAP IDs and cause=unspecified — i.e. a stray non-\n"
            "  UE-associated error. The AMF MUST NOT tear down the SCTP\n"
            "  association in response; otherwise a single bogus error\n"
            "  on any gNB takes the entire NG-AP signalling link down.\n"
            "\n"
            "Procedure (TS 38.413 §8.7 + §9.2.6 ErrorIndication)\n"
            "  1. require_gnb() — no UE needed.\n"
            "  2. gnb.send_error_indication(None, 'radioNetwork',\n"
            "     'unspecified') — emits non-UE-associated\n"
            "     ErrorIndication.\n"
            "  3. time.sleep(2) settle.\n"
            "  4. Read gnb.state.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  gnb.state == 'READY' after the ErrorIndication (the SCTP\n"
            "  association is still up and NG Setup state is intact).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  cause, gnb_state.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. No UE side-effects checked — this is a\n"
            "  pure NG-AP link-survival test."
        ),
    )

    def run(self):
        gnb = self.require_gnb()

        log.info("Sending ErrorIndication without UE context")
        gnb.send_error_indication(None, "radioNetwork", "unspecified")
        time.sleep(2)

        # gNB should still be connected
        if gnb.state == "READY":
            self.pass_test(cause="unspecified", gnb_state=gnb.state)
        else:
            self.fail_test("gNB state changed after error indication", gnb_state=gnb.state)
        return self.result


class RlfThenReRegister(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-009",
        title="RLF release → re-register → new PDU → UL traffic",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("regression", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Full RLF-recovery loop (TS 38.413 §8.3): a UE that just\n"
            "  lost the radio link must be able to come back, get a new\n"
            "  PDU session allocated, and start sending traffic again.\n"
            "  Catches SMF/UPF leaks that prevent re-registration after\n"
            "  RLF (e.g. residual PFCP session blocking a fresh PDU\n"
            "  establishment).\n"
            "\n"
            "Procedure (TS 38.413 §8.3 RLF + 24.501 §5.5.1 + §6.4.1)\n"
            "  1. Phase 1: require_gnb() + require_ue();\n"
            "     register_ue + establish_pdu psi=1; record ue_ip_before.\n"
            "  2. Phase 2: _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'radio-connection-with-ue-lost', timeout=10) — RLF\n"
            "     release. fail_test on failure.\n"
            "  3. time.sleep(1) settle.\n"
            "  4. Phase 3: require_gnb() again (gnb2); manually reset\n"
            "     ue.state='DEREGISTERED', clear NGAP IDs and\n"
            "     pdu_sessions; register_ue(ue, gnb2);\n"
            "     establish_pdu psi=1; record ue_ip_after.\n"
            "  5. Phase 4: TrafficEngine UDP UL 1M for 5s from new IP\n"
            "     to derive_gateway(new_ip); record kbps.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  Phases 1-3 must each succeed (fail_test fires if any\n"
            "  fails). Phase 4 reports traffic_ok = (kbps > 0) but does\n"
            "  NOT drive fail_test — pass_test is unconditional once\n"
            "  re-register succeeds.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, ip_before, ip_after, rlf_released, re_registered,\n"
            "  traffic_ok, post_rlf_kbps.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. HOLLOW-PASS shape on Phase 4: zero post-\n"
            "  RLF kbps still results in PASS — operator must inspect\n"
            "  traffic_ok / post_rlf_kbps to gate manually."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()

        # Phase 1: register + PDU
        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip_before = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        log.info("Phase 1: UE %s registered, IP=%s", ue.imsi, ue_ip_before)

        # Phase 2: RLF release
        ok = self._release_and_wait(ue, gnb,
                                     "radioNetwork", "radio-connection-with-ue-lost")
        if not ok:
            self.fail_test("RLF release failed", ue=ue.imsi)
            return self.result
        log.info("Phase 2: UE released (RLF)")

        time.sleep(1)

        # Phase 3: re-register on fresh gNB
        gnb2 = self.require_gnb()
        ue.state = "DEREGISTERED"
        ue.ran_ue_ngap_id = None
        ue.amf_ue_ngap_id = None
        ue.pdu_sessions.clear()

        if not self.register_ue(ue, gnb2):
            self.fail_test("Re-registration failed after RLF", ue=ue.imsi)
            return self.result
        if not self.establish_pdu(ue, psi=1):
            self.fail_test("New PDU session failed after RLF re-register", ue=ue.imsi)
            return self.result

        ue_ip_after = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip_after)
        log.info("Phase 3: UE re-registered, new IP=%s", ue_ip_after)

        # Phase 4: verify traffic works (UL, UDP, 5s)
        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip_after, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="1M", duration=5, direction="ul",
        )
        session.start()
        stats = session.stop()

        traffic_ok = stats.throughput_kbps > 0
        kbps = round(stats.throughput_kbps, 1)

        log.info("Phase 4: post-RLF traffic: %s (%.1f kbps)",
                 "OK" if traffic_ok else "FAIL", kbps)

        self.pass_test(
            ue=ue.imsi,
            ip_before=ue_ip_before, ip_after=ue_ip_after,
            rlf_released=True, re_registered=True,
            traffic_ok=traffic_ok, post_rlf_kbps=kbps,
        )
        return self.result


class MultiUeRlf(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-010",
        title="Multiple UEs experience RLF release concurrently",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=45.0,
        description=(
            "Purpose\n"
            "  Stress version of TC-REL-001: concurrent RLF releases\n"
            "  surface AMF/SMF race conditions on UE-context cleanup\n"
            "  (parallel UEContextReleaseCommand dispatch, PFCP Modify\n"
            "  serialisation, UPF FAR teardown). Catches issues that\n"
            "  single-UE RLF tests miss.\n"
            "\n"
            "Procedure (TS 38.413 §8.3 RLF, parallel)\n"
            "  1. require_gnb() + require_ue();\n"
            "     ue_count = min(8, len(ue_pool)).\n"
            "  2. ThreadPoolExecutor(max_workers=ue_count) concurrently\n"
            "     registers all UEs: attach → register → wait REGISTERED\n"
            "     → establish_pdu psi=1 → poll for IP. Any failure →\n"
            "     immediate fail_test.\n"
            "  3. ThreadPoolExecutor concurrently calls\n"
            "     _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'radio-connection-with-ue-lost') per UE; collect\n"
            "     {imsi, released, state} for each.\n"
            "  4. Aggregate released count.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — ue_count computed (min(8, pool size)).\n"
            "\n"
            "Pass criteria\n"
            "  released == ue_count (every UE's _release_and_wait\n"
            "  returned True, i.e. reached DEREGISTERED).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_count, released, results (per-UE).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. UE pool must be ≥ 2; cap is 8 to keep\n"
            "  wall time bounded."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()

        ue_count = min(8, len(self.ue_pool))
        ues = self.ue_pool[:ue_count]

        # Register all UEs concurrently
        def _reg_one(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                if not ue.wait_for_state("REGISTERED", timeout=15):
                    return (ue, False)
                ue.establish_pdu_session(dnn="internet", sst=1, pdu_session_id=1)
                deadline = time.time() + 15
                while time.time() < deadline:
                    s = ue.pdu_sessions.get(1)
                    if s and s.get("ip") and s["ip"] != "unknown":
                        return (ue, True)
                    time.sleep(0.3)
                return (ue, False)
            except Exception:
                return (ue, False)

        log.info("Registering %d UEs", ue_count)
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {pool.submit(_reg_one, ue): ue for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue, ok = f.result()
                if not ok:
                    self.fail_test(f"UE {ue.imsi} registration failed")
                    return self.result

        log.info("All %d UEs registered — triggering concurrent RLF", ue_count)

        # RLF release all concurrently
        results = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {pool.submit(self._release_and_wait, ue, gnb,
                                   "radioNetwork", "radio-connection-with-ue-lost"): ue
                       for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue = futures[f]
                ok = f.result()
                results.append({"imsi": ue.imsi, "released": ok, "state": ue.state})
                log.info("UE %s: %s (state=%s)", ue.imsi[-3:],
                         "released" if ok else "FAIL", ue.state)

        released = sum(1 for r in results if r["released"])
        if released == ue_count:
            self.pass_test(ue_count=ue_count, released=released, results=results)
        else:
            self.fail_test(f"{ue_count - released}/{ue_count} UEs not released",
                           released=released, results=results)
        return self.result


class InactivityReleaseThenUlTraffic(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-011",
        title="Inactivity release → re-register → sustained UL traffic",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Inactivity-release variant of TC-REL-009 (TS 38.413 §8.3\n"
            "  cause=user-inactivity) followed by sustained UDP UL. Pins\n"
            "  that the re-registered UE's UPF UL FAR delivers packets at\n"
            "  full duration_s — not just a 5-second smoke burst.\n"
            "\n"
            "Procedure (TS 38.413 §8.3 + TS 24.501 §6.4.1)\n"
            "  1. require_gnb() + require_ue(); duration = TRAFFIC_DURATION.\n"
            "  2. register_ue + establish_pdu psi=1; record ue_ip_before.\n"
            "  3. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'user-inactivity'). fail_test on failure.\n"
            "  4. time.sleep(1).\n"
            "  5. require_gnb() (gnb2); reset ue.state='DEREGISTERED',\n"
            "     clear NGAP IDs and pdu_sessions; register_ue(ue, gnb2);\n"
            "     establish_pdu psi=1; record ue_ip_after.\n"
            "  6. collect_upf_stats() before; TrafficEngine UDP UL 1M for\n"
            "     duration s from ue_ip_after to derive_gateway(after);\n"
            "     collect_upf_stats() after; compute_upf_delta.\n"
            "\n"
            "Parameters (self.params)\n"
            "  TRAFFIC_DURATION env var (default 30 s).\n"
            "\n"
            "Pass criteria\n"
            "  stats.throughput_kbps > 0 (sustained UL delivered packets\n"
            "  through the re-established UPF FAR).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, ip_before, ip_after, ul_kbps, ul_jitter_ms,\n"
            "  ul_loss_pct, duration_s, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Tagged 'slow' — wall time ≈ TRAFFIC_DURATION\n"
            "  plus release + re-register overhead."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = TRAFFIC_DURATION

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip_before = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        log.info("UE %s registered, IP=%s — triggering inactivity release", ue.imsi, ue_ip_before)

        # Inactivity release
        ok = self._release_and_wait(ue, gnb, "radioNetwork", "user-inactivity")
        if not ok:
            self.fail_test("Inactivity release failed", ue=ue.imsi, ue_state=ue.state)
            return self.result
        log.info("UE released (inactivity)")

        time.sleep(1)

        # Re-register on fresh gNB
        gnb2 = self.require_gnb()
        ue.state = "DEREGISTERED"
        ue.ran_ue_ngap_id = None
        ue.amf_ue_ngap_id = None
        ue.pdu_sessions.clear()

        if not self.register_ue(ue, gnb2):
            self.fail_test("Re-registration failed")
            return self.result
        if not self.establish_pdu(ue, psi=1):
            self.fail_test("PDU session failed after re-register")
            return self.result

        ue_ip_after = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip_after)
        log.info("UE re-registered, new IP=%s — starting UL traffic", ue_ip_after)

        # UL traffic via TrafficEngine
        engine = TrafficEngine.get()
        upf_before = collect_upf_stats()

        session = engine.create_session(
            src_ip=ue_ip_after, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="1M", duration=duration, direction="ul",
        )
        session.start()
        stats = session.stop()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        ul_kbps = round(stats.throughput_kbps, 1)
        ul_jitter = round(stats.jitter_ms, 2)
        ul_loss = round(stats.loss_pct, 2)

        log.info("Post-inactivity UL: %.1f kbps, jitter=%.1fms, loss=%.1f%%",
                 ul_kbps, ul_jitter, ul_loss)

        if stats.throughput_kbps > 0:
            self.pass_test(
                ue=ue.imsi, cause="user-inactivity",
                ip_before=ue_ip_before, ip_after=ue_ip_after,
                ul_kbps=ul_kbps, ul_jitter_ms=ul_jitter, ul_loss_pct=ul_loss,
                duration_s=duration, upf_stats=upf_delta,
            )
        else:
            self.fail_test("UL traffic failed after inactivity re-register")
        return self.result


class InactivityReleaseThenDlTraffic(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-012",
        title="Inactivity release → re-register → sustained DL traffic",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  DL mirror of TC-REL-011 (TS 38.413 §8.3\n"
            "  cause=user-inactivity then re-register). Some UPF builds\n"
            "  restore only the UL FAR on re-registration; this gates\n"
            "  the DL FAR's TEID re-bind to the new gNB so sustained DL\n"
            "  delivers packets after a release-and-recover cycle.\n"
            "\n"
            "Procedure (TS 38.413 §8.3 + TS 24.501 §6.4.1)\n"
            "  1. require_gnb() + require_ue(); duration = TRAFFIC_DURATION.\n"
            "  2. register_ue + establish_pdu psi=1; record ue_ip_before.\n"
            "  3. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'user-inactivity'). fail_test on failure.\n"
            "  4. time.sleep(1); require_gnb() (gnb2); reset UE state\n"
            "     and re-register; establish_pdu psi=1; record\n"
            "     ue_ip_after.\n"
            "  5. collect_upf_stats() before; TrafficEngine UDP DL 1M\n"
            "     for duration s targeting ue_ip_after on port 5202;\n"
            "     compute_upf_delta after.\n"
            "\n"
            "Parameters (self.params)\n"
            "  TRAFFIC_DURATION env var.\n"
            "\n"
            "Pass criteria\n"
            "  stats.throughput_kbps > 0 (sustained DL delivered packets\n"
            "  through the re-established DL UPF FAR / new RAN TEID).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, ip_before, ip_after, dl_kbps, dl_jitter_ms,\n"
            "  dl_loss_pct, duration_s, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Tagged 'slow'."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = TRAFFIC_DURATION

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip_before = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        log.info("UE %s registered, IP=%s — triggering inactivity release", ue.imsi, ue_ip_before)

        # Inactivity release
        ok = self._release_and_wait(ue, gnb, "radioNetwork", "user-inactivity")
        if not ok:
            self.fail_test("Inactivity release failed", ue=ue.imsi, ue_state=ue.state)
            return self.result
        log.info("UE released (inactivity)")

        time.sleep(1)

        # Re-register on fresh gNB
        gnb2 = self.require_gnb()
        ue.state = "DEREGISTERED"
        ue.ran_ue_ngap_id = None
        ue.amf_ue_ngap_id = None
        ue.pdu_sessions.clear()

        if not self.register_ue(ue, gnb2):
            self.fail_test("Re-registration failed")
            return self.result
        if not self.establish_pdu(ue, psi=1):
            self.fail_test("PDU session failed after re-register")
            return self.result

        ue_ip_after = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        log.info("UE re-registered, new IP=%s — starting DL traffic", ue_ip_after)

        # DL traffic via TrafficEngine
        engine = TrafficEngine.get()
        upf_before = collect_upf_stats()

        session = engine.create_session(
            src_ip=ue_ip_after, dst_ip=ue_ip_after, protocol="udp",
            dst_port=5202, bandwidth="1M", duration=duration, direction="dl",
        )
        session.start()
        stats = session.stop()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        dl_kbps = round(stats.throughput_kbps, 1)
        dl_jitter = round(stats.jitter_ms, 2)
        dl_loss = round(stats.loss_pct, 2)

        log.info("Post-inactivity DL: %.1f kbps, jitter=%.1fms, loss=%.1f%%",
                 dl_kbps, dl_jitter, dl_loss)

        if stats.throughput_kbps > 0:
            self.pass_test(
                ue=ue.imsi, cause="user-inactivity",
                ip_before=ue_ip_before, ip_after=ue_ip_after,
                dl_kbps=dl_kbps, dl_jitter_ms=dl_jitter, dl_loss_pct=dl_loss,
                duration_s=duration, upf_stats=upf_delta,
            )
        else:
            self.fail_test("DL traffic failed after inactivity re-register")
        return self.result


class InactivityReleaseWithBidirTraffic(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-013",
        title="Bidir traffic followed by user-inactivity AN release",
        spec="TS 38.413 §8.3",
        domain=Domain.MOBILITY,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the 'AN release immediately after a real bidir burst'\n"
            "  sequence (TS 38.413 §8.3). The pre-release UL+DL flows\n"
            "  must both deliver packets (UPF PDR/FAR/QER are live), and\n"
            "  the subsequent user-inactivity AN release must succeed\n"
            "  cleanly. Surfaces races between active iperf3 sessions and\n"
            "  the release path (e.g. FAR teardown blocked by in-flight\n"
            "  packets).\n"
            "\n"
            "Procedure (TS 38.413 §8.3 + TS 29.281 §5 GTP-U)\n"
            "  1. require_gnb() + require_ue(); duration = 5 (in-test\n"
            "     constant).\n"
            "  2. register_ue + establish_pdu psi=1.\n"
            "  3. ue_ip = pdu_sessions[1].ip; server = derive_gateway.\n"
            "  4. collect_upf_stats() before.\n"
            "  5. TrafficEngine.run_bidir UDP 1M for 5s on UL=5201,\n"
            "     DL=5202; capture ul_kbps, dl_kbps.\n"
            "  6. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'user-inactivity').\n"
            "  7. compute_upf_delta.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — duration is hard-coded to 5 s.\n"
            "\n"
            "Pass criteria\n"
            "  Release succeeded AND ul_stats / dl_stats both non-None\n"
            "  AND ul_kbps > 0 AND dl_kbps > 0. Both gates must hold.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, cause, ue_state, released, ul_kbps, dl_kbps,\n"
            "  duration_s, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The 5 s bidir window is short — catches\n"
            "  startup-only races, not sustained-load ones."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = 5

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)
        log.info("UE %s registered, IP=%s — running 1Mbps bidir traffic for %ds",
                 ue.imsi, ue_ip, duration)

        upf_before = collect_upf_stats()

        engine = TrafficEngine.get()
        ul_stats, dl_stats = engine.run_bidir(
            ip_a=ue_ip, ip_b=server, server=server, protocol="udp",
            ul_port=5201, dl_port=5202, bandwidth="1M",
            duration=duration, udp=True,
        )

        ul_kbps = round(ul_stats.throughput_kbps, 1) if ul_stats else 0
        dl_kbps = round(dl_stats.throughput_kbps, 1) if dl_stats else 0
        log.info("Pre-release: UL=%.1f kbps DL=%.1f kbps — triggering inactivity release",
                 ul_kbps, dl_kbps)

        ok = self._release_and_wait(ue, gnb, "radioNetwork", "user-inactivity")

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        if not ok:
            self.fail_test("Inactivity release failed",
                           ue=ue.imsi, ue_state=ue.state,
                           ul_kbps=ul_kbps, dl_kbps=dl_kbps)
            return self.result

        if not (ul_stats and dl_stats and ul_kbps > 0 and dl_kbps > 0):
            self.fail_test(
                f"Pre-release traffic incomplete (UL={ul_kbps}kbps DL={dl_kbps}kbps)",
                released=True, ul_kbps=ul_kbps, dl_kbps=dl_kbps,
            )
            return self.result

        self.pass_test(
            ue=ue.imsi, cause="user-inactivity",
            ue_state=ue.state, released=True,
            ul_kbps=ul_kbps, dl_kbps=dl_kbps,
            duration_s=duration, upf_stats=upf_delta,
        )
        return self.result


class InactivityThenUlServiceRequest(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-014",
        title="Inactivity AN release then UL Service Request resumes UE",
        spec="TS 24.501 §8.2.15",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Pins the release-then-resume sequence (TS 38.413 §8.3 AN\n"
            "  release + TS 24.501 §8.2.15 SERVICE REQUEST + §5.6.1.4\n"
            "  accept). After a user-inactivity release puts the UE in\n"
            "  CM-IDLE, a UE-triggered NAS Service Request (type=data)\n"
            "  must cause the AMF to send InitialContextSetupRequest and\n"
            "  the resumed bearer must carry UL traffic.\n"
            "\n"
            "Procedure (TS 38.413 §8.3 + TS 24.501 §8.2.15 + §5.6.1.4)\n"
            "  1. require_gnb() + require_ue(); duration = TRAFFIC_DURATION.\n"
            "  2. register_ue + establish_pdu psi=1.\n"
            "  3. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'user-inactivity'). fail_test on failure.\n"
            "  4. time.sleep(1).\n"
            "  5. _re_attach_for_service_request(ue) — reset\n"
            "     ran_ue_ngap_id, amf_ue_ngap_id, mark UE REGISTERED to\n"
            "     model CM-IDLE-with-NAS-context.\n"
            "  6. gnb.send_service_request(ue, service_type=1) — UL data\n"
            "     trigger.\n"
            "  7. _wait_for_trace(gnb, 'InitialContextSetup', sr_ts,\n"
            "     timeout=10) — look for AMF's ICS in protocol_trace.\n"
            "     fail_test if missing.\n"
            "  8. time.sleep(1); collect_upf_stats() before;\n"
            "     TrafficEngine UDP UL 1M for duration s; compute_upf_delta.\n"
            "\n"
            "Parameters (self.params)\n"
            "  TRAFFIC_DURATION env var.\n"
            "\n"
            "Pass criteria\n"
            "  stats non-None AND stats.throughput_kbps > 0 (sustained UL\n"
            "  through the resumed bearer).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, ue_ip, cause='user-inactivity',\n"
            "  service_request_accepted, ul_kbps, ul_jitter_ms,\n"
            "  ul_loss_pct, duration_s, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Tagged 'slow'. SR-accept is verified by\n"
            "  presence of the ICS trace entry — if the AMF rejects with\n"
            "  SERVICE REJECT instead, the test fails as 'AMF did not\n"
            "  accept'."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = TRAFFIC_DURATION

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)
        log.info("UE %s registered, IP=%s", ue.imsi, ue_ip)

        # Inactivity release — UE → CM-IDLE
        if not self._release_and_wait(ue, gnb, "radioNetwork", "user-inactivity"):
            self.fail_test("Inactivity release failed", ue=ue.imsi, ue_state=ue.state)
            return self.result
        log.info("UE %s released (user-inactivity) — CM-IDLE", ue.imsi)

        time.sleep(1)

        # UL data arrival → UE-triggered Service Request
        self._re_attach_for_service_request(ue)
        sr_ts = time.time()
        log.info("UE %s has UL data — sending Service Request (type=data)", ue.imsi)
        gnb.send_service_request(ue, service_type=1)

        # AMF accepts → InitialContextSetupRequest (proc 14)
        ics = self._wait_for_trace(gnb, "InitialContextSetup", sr_ts, timeout=10)
        if ics is None:
            self.fail_test("AMF did not accept Service Request "
                           "(no InitialContextSetupRequest)",
                           ue=ue.imsi, sr_type="data")
            return self.result
        log.info("Service Request accepted (InitialContextSetupRequest at +%.2fs)",
                 ics["time"] - sr_ts)

        time.sleep(1)

        # Verify UL traffic flows after SR
        upf_before = collect_upf_stats()

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="1M", duration=duration, direction="ul",
        )
        session.start()
        stats = session.stop()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        ul_kbps = round(stats.throughput_kbps, 1) if stats else 0
        ul_jitter = round(stats.jitter_ms, 2) if stats else 0
        ul_loss = round(stats.loss_pct, 2) if stats else 0

        log.info("Post-SR UL: %.1f kbps, jitter=%.1fms, loss=%.1f%%",
                 ul_kbps, ul_jitter, ul_loss)

        if stats and stats.throughput_kbps > 0:
            self.pass_test(
                ue=ue.imsi, ue_ip=ue_ip,
                cause="user-inactivity", service_request_accepted=True,
                ul_kbps=ul_kbps, ul_jitter_ms=ul_jitter, ul_loss_pct=ul_loss,
                duration_s=duration, upf_stats=upf_delta,
            )
        else:
            self.fail_test("UL traffic failed after Service Request",
                           service_request_accepted=True, ul_kbps=ul_kbps)
        return self.result


class InactivityThenDlPaging(ReleaseBase):
    SPEC = TestSpec(
        tc_id="TC-REL-015",
        title="Inactivity AN release then DL data triggers AMF Paging",
        spec="TS 38.413 §8.5",
        domain=Domain.IDLE_MODE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Pins the release-then-paging-then-resume sequence (TS\n"
            "  38.413 §8.3 release + §8.5 Paging + TS 24.501 §5.6.2 +\n"
            "  §5.6.1.4). After a user-inactivity release, network-side\n"
            "  DL data hits the UPF; SMF notifies AMF; AMF emits NGAP\n"
            "  Paging; UE replies with mobile-terminated Service Request;\n"
            "  DL must resume.\n"
            "\n"
            "Procedure (TS 38.413 §8.3 + §8.5 + TS 24.501 §5.6.2)\n"
            "  1. require_gnb() + require_ue(); duration = TRAFFIC_DURATION.\n"
            "  2. register_ue + establish_pdu psi=1.\n"
            "  3. _release_and_wait(ue, gnb, 'radioNetwork',\n"
            "     'user-inactivity'). fail_test on failure.\n"
            "  4. time.sleep(1).\n"
            "  5. Arm gnb._paging_event = threading.Event(); spawn UDP\n"
            "     DL trigger session (100K, 3s, port 5202) in a daemon\n"
            "     thread.\n"
            "  6. Wait up to 10s for _paging_event. fail_test if missing.\n"
            "  7. _re_attach_for_service_request(ue);\n"
            "     gnb.send_service_request(ue, service_type=2) — MT SR.\n"
            "  8. _wait_for_trace(gnb, 'InitialContextSetup', sr_ts,\n"
            "     timeout=10); fail_test if missing.\n"
            "  9. Join trigger; sleep 1; collect_upf_stats(); TrafficEngine\n"
            "     UDP DL 1M for duration s; compute_upf_delta.\n"
            "\n"
            "Parameters (self.params)\n"
            "  TRAFFIC_DURATION env var.\n"
            "\n"
            "Pass criteria\n"
            "  Paging received AND ICS observed AND dl_stats non-None\n"
            "  AND dl_stats.throughput_kbps > 0.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, ue_ip, cause, paging_received, paging_info,\n"
            "  service_request_accepted, dl_kbps, dl_jitter_ms,\n"
            "  dl_loss_pct, duration_s, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Tagged 'slow'. Lab UPF must support the\n"
            "  'buffer + notify SMF on first DL packet' path (FAR\n"
            "  action=BUFFER + report)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = TRAFFIC_DURATION

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        log.info("UE %s registered, IP=%s", ue.imsi, ue_ip)

        # Inactivity release — UE → CM-IDLE
        if not self._release_and_wait(ue, gnb, "radioNetwork", "user-inactivity"):
            self.fail_test("Inactivity release failed", ue=ue.imsi, ue_state=ue.state)
            return self.result
        log.info("UE %s released (user-inactivity) — CM-IDLE", ue.imsi)

        time.sleep(1)

        # Arm paging detector before triggering DL
        gnb._paging_event = threading.Event()

        # Network-side DL data — should cause AMF to page
        engine = TrafficEngine.get()
        trigger_session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="udp",
            dst_port=5202, bandwidth="100K", duration=3, direction="dl",
        )
        trigger_thread = threading.Thread(target=trigger_session.start, daemon=True)
        trigger_thread.start()
        log.info("DL data triggered for paged UE %s", ue.imsi)

        paging_received = gnb._paging_event.wait(timeout=10)
        log.info("Paging %s", "received" if paging_received else "NOT received")

        if not paging_received:
            trigger_thread.join(timeout=5)
            trigger_session.stop()
            self.fail_test("Paging not received after DL data trigger",
                           ue=ue.imsi, cause="user-inactivity")
            return self.result

        # UE responds — mobile-terminated Service Request
        self._re_attach_for_service_request(ue)
        sr_ts = time.time()
        gnb.send_service_request(ue, service_type=2)
        log.info("UE %s sent Service Request (type=mobile-terminated)", ue.imsi)

        ics = self._wait_for_trace(gnb, "InitialContextSetup", sr_ts, timeout=10)

        trigger_thread.join(timeout=10)
        trigger_session.stop()

        if ics is None:
            self.fail_test("AMF did not accept Service Request after Paging",
                           paging_received=True, paging_info=gnb._last_paging)
            return self.result
        log.info("Service Request accepted (InitialContextSetupRequest at +%.2fs)",
                 ics["time"] - sr_ts)

        time.sleep(1)

        # Verify sustained DL flows after paging+SR
        upf_before = collect_upf_stats()
        dl_session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="udp",
            dst_port=5202, bandwidth="1M", duration=duration, direction="dl",
        )
        dl_session.start()
        dl_stats = dl_session.stop()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        dl_kbps = round(dl_stats.throughput_kbps, 1) if dl_stats else 0
        dl_jitter = round(dl_stats.jitter_ms, 2) if dl_stats else 0
        dl_loss = round(dl_stats.loss_pct, 2) if dl_stats else 0

        log.info("Post-paging DL: %.1f kbps, jitter=%.1fms, loss=%.1f%%",
                 dl_kbps, dl_jitter, dl_loss)

        if dl_stats and dl_stats.throughput_kbps > 0:
            self.pass_test(
                ue=ue.imsi, ue_ip=ue_ip,
                cause="user-inactivity",
                paging_received=True, paging_info=gnb._last_paging,
                service_request_accepted=True,
                dl_kbps=dl_kbps, dl_jitter_ms=dl_jitter, dl_loss_pct=dl_loss,
                duration_s=duration, upf_stats=upf_delta,
            )
        else:
            self.fail_test("DL traffic failed after Paging+SR",
                           paging_received=True, service_request_accepted=True,
                           dl_kbps=dl_kbps)
        return self.result


ALL_RELEASE_TCS = [
    RlfRelease, InactivityRelease, AnReleaseNoResources, NgranRelease,
    RrcInactiveTransition, RrcInactiveThenRelease,
    ErrorIndicationWithUe, ErrorIndicationNoContext,
    RlfThenReRegister, MultiUeRlf,
    InactivityReleaseThenUlTraffic, InactivityReleaseThenDlTraffic,
    InactivityReleaseWithBidirTraffic,
    InactivityThenUlServiceRequest,
    InactivityThenDlPaging,
]

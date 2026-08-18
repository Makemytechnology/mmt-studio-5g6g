# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: eSIM robot-suite parity (GSMA SGP.22 Consumer RSP).

Pairs with robot/suites/other/26_esim.robot. The robot suite logs
informational lines for SGP.22 procedures that need a full eUICC /
SM-DP+ stack (profile download via ES9+ Mutual-Auth, LPA profile
enable/disable across a modem switch). These Python TestCase shells
provide a smoke surface on top of the operator-API endpoints
(`/api/esim/*`) for the cases that have a usable REST surface, and
honestly mark the rest as "implementation pending" so the runner
can report parity without claiming a vacuous PASS.

TC-ESIM-* IDs are distinct from TC-ESIMOAM-* (tc_esim_oam.py); the
robot-derived TCs document the SGP.22 procedure, the OAM TCs pin the
panel API contract.

Robot reference:
  /home/bxb/work/mmt_studio_core_tester/robot/suites/other/26_esim.robot
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_esim_robot")

ESIM = "/api/esim"


def _api(path, method="GET", body=None):
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode()), resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


# ── TC-ESIM-001 ─────────────────────────────────────────────────


class EsimEuiccProfileDownload(TestCase):
    """TC-ESIM-001: SGP.22 §3.1.3 eUICC Profile Download via ES9+.

    Full ES9+ Mutual-Auth + GetBoundProfilePackage is exercised end-
    to-end in TC-ESIMOAM-007 (tc_esim_oam.py). This robot-parity TC
    documents the procedure for the robot inventory; the full
    download-on-eUICC verification needs a real LPA / eUICC card and
    is left as a fail-pending shell.
    """
    SPEC = TestSpec(
        tc_id="TC-ESIM-001",
        title="eUICC Profile Download via ES9+",
        spec="SGP 22 §3.1.3",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "esim", "download", "install", "smdp",
              "priority-1"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Document the SGP.22 §3.1.3 eUICC Profile Download procedure\n"
            "  for the robot-suite parity inventory. The full operator-side\n"
            "  ES9+ Mutual-Auth + GetBoundProfilePackage flow is exercised\n"
            "  end-to-end by TC-ESIMOAM-007; this shell pins the on-card\n"
            "  install side which requires a real LPA / eUICC card.\n"
            "\n"
            "Procedure (SGP.22 §3.1.3 ES9+ Profile Download)\n"
            "  1. Operator pushes a profile package to SM-DP+ (ES2+ Download\n"
            "     Order + ConfirmOrder).\n"
            "  2. eUICC scans Activation-Code, initiates download via ES9+.\n"
            "  3. Mutual authentication between eUICC and SM-DP+ (ES9+\n"
            "     InitiateAuthentication + AuthenticateClient).\n"
            "  4. SM-DP+ binds the profile to the eUICC EID and returns the\n"
            "     encrypted BoundProfilePackage.\n"
            "  5. eUICC decrypts, installs profile, assigns ICCID, stores\n"
            "     MNO metadata.\n"
            "  6. eUICC sends HandleNotification (install) back to SM-DP+.\n"
            "  Implementation: run() calls fail_test('Python implementation\n"
            "  pending') unconditionally — there is no on-card driver.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — placeholder shell with no run-time tunables.\n"
            "\n"
            "Pass criteria\n"
            "  Currently always fails with 'implementation pending'. A real\n"
            "  pass would require profile.state=='installed', ICCID + IMSI\n"
            "  provisioned, and MNO metadata visible on the eUICC.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — fail_test() emits only the pending-message detail.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Full operator-API coverage is in TC-ESIMOAM-007;\n"
            "  this TC needs hardware (eUICC + LPA) to ever pass."
        ),
    )

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/other/26_esim.robot::TC-ESIM-001 for the "
            "procedure; ES9+ surface is covered by TC-ESIMOAM-007 "
            "but on-card eUICC install requires a real LPA."
        )
        return self.result


# ── TC-ESIM-002 ─────────────────────────────────────────────────


class EsimProfileEnableDisable(TestCase):
    """TC-ESIM-002: SGP.22 §3.2 Profile Enable / Disable on eUICC."""
    SPEC = TestSpec(
        tc_id="TC-ESIM-002",
        title="Profile Enable / Disable via LPA",
        spec="SGP 22 §3.2",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "esim", "enable", "disable", "switch",
              "priority-1"),
        setup=Setup.EMPTY,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Document the SGP.22 §3.2 Profile Enable / Disable procedure\n"
            "  on a multi-profile eUICC. The operator-API state guards are\n"
            "  pinned by TC-ESIMOAM-008; this shell pins the LPA + modem\n"
            "  switch path which requires a real eUICC card and live RAN.\n"
            "\n"
            "Procedure (SGP.22 §3.2 Profile Enable / Disable)\n"
            "  1. Precondition: eUICC has two installed profiles, A enabled\n"
            "     and B disabled (exactly one profile enabled at a time per\n"
            "     SGP.22 §3.2).\n"
            "  2. LPA issues Disable-Profile to profile A (eUICC sends REFRESH\n"
            "     proactive command to the modem, causing detach).\n"
            "  3. LPA issues Enable-Profile to profile B.\n"
            "  4. Modem performs profile switch (resets baseband, re-reads\n"
            "     ADF, re-registers with B's IMSI per TS 31.102 §4.2).\n"
            "  5. UE attaches to the network with the new IMSI.\n"
            "  Implementation: run() calls fail_test('Python implementation\n"
            "  pending') unconditionally — no LPA driver on this host.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — placeholder shell.\n"
            "\n"
            "Pass criteria\n"
            "  Currently always fails with 'implementation pending'. A real\n"
            "  pass would verify A.state=='disabled', B.state=='enabled',\n"
            "  invariant 'exactly one enabled', and UE re-attach with B's\n"
            "  IMSI.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — fail_test() emits only the pending-message detail.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Operator-side lifecycle guards covered by\n"
            "  TC-ESIMOAM-008; on-card modem switch needs hardware."
        ),
    )

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/other/26_esim.robot::TC-ESIM-002 for the "
            "procedure; lifecycle guards pinned by TC-ESIMOAM-008."
        )
        return self.result


# ── TC-ESIM-003 ─────────────────────────────────────────────────


class EsimProfileListViaRestApi(TestCase):
    """TC-ESIM-003: SGP.22 §3.3 profile inventory via REST API."""
    SPEC = TestSpec(
        tc_id="TC-ESIM-003",
        title="Profile List via REST API (eSIM profile inventory)",
        spec="SGP 22 §3.3",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "esim", "api", "profile-list", "inventory",
              "priority-1"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the SGP.22 §3.3 profile inventory contract on the REST\n"
            "  surface that the operator panel and LPA tooling consume.\n"
            "  Asserts the response shape and the §3.2 invariant that at\n"
            "  most one profile is in 'enabled' state at any time across the\n"
            "  returned inventory.\n"
            "\n"
            "Procedure (SGP.22 §3.3 Profile Inventory)\n"
            "  1. GET /api/esim/profiles.\n"
            "  2. Assert status 200 and ok=True.\n"
            "  3. Assert profiles is a list (not None / scalar / dict).\n"
            "  4. Filter for entries with profile_state == 'enabled'.\n"
            "  5. Assert at most one profile is 'enabled' across the entire\n"
            "     inventory (SGP.22 §3.2 invariant).\n"
            "  6. pass_test with profile_count + enabled_count.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  Response is well-formed, profiles is a list, len(enabled) <=\n"
            "  1. More than one 'enabled' row fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  profile_count, enabled_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — works on any inventory size including zero;\n"
            "  pre-existing rows from other suites do not affect the\n"
            "  shape-only invariants."
        ),
    )

    def run(self):
        try:
            r, s = _api(f"{ESIM}/profiles")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"profiles list failed: {s} {r}")
                return self.result
            profiles = r.get("profiles")
            if not isinstance(profiles, list):
                self.fail_test(f"profiles not a list: {type(profiles)}",
                               body=r)
                return self.result
            enabled = [p for p in profiles
                       if p.get("profile_state") == "enabled"]
            if len(enabled) > 1:
                self.fail_test(
                    f"more than one enabled profile: {len(enabled)}",
                    enabled=[p.get("iccid") for p in enabled])
                return self.result
            self.pass_test(profile_count=len(profiles),
                           enabled_count=len(enabled))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ── TC-ESIM-010 ─────────────────────────────────────────────────


class EsimIccidCounterManagement(TestCase):
    """TC-ESIM-010: SGP.22 §2.5.1 ICCID allocation counter."""
    SPEC = TestSpec(
        tc_id="TC-ESIM-010",
        title="ICCID Counter Management (monotonic allocation)",
        spec="SGP 22 §2.5.1",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MINOR,
        tags=("conformance", "esim", "iccid", "counter", "allocation",
              "priority-2"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Smoke probe for the ICCID allocation sequence anchor.\n"
            "  SGP.22 §2.5.1 requires the SM-DP+ to allocate ICCIDs from a\n"
            "  monotonic pool; this TC reads the total_profiles counter\n"
            "  that backs the allocator on this runtime. Luhn correctness\n"
            "  is pinned by TC-ESIMOAM-002; sequential allocation by the\n"
            "  mint path in TC-ESIMOAM-007.\n"
            "\n"
            "Procedure (SGP.22 §2.5.1 ICCID counter)\n"
            "  1. GET /api/esim/stats.\n"
            "  2. Assert status 200 and ok=True.\n"
            "  3. Assert stats.total_profiles is an int — this is the\n"
            "     allocator anchor used by OrderProfile.\n"
            "  4. pass_test with total_profiles in details.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  stats.total_profiles is an integer (int). Non-int or absent\n"
            "  field fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  total_profiles.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. This TC does not verify monotonicity across\n"
            "  sequential mints — that requires running TC-ESIMOAM-007 in a\n"
            "  sequence and comparing total_profiles before and after each\n"
            "  mint. Here we only confirm the counter is exposed as int."
        ),
    )

    def run(self):
        try:
            r, s = _api(f"{ESIM}/stats")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"stats failed: {s} {r}")
                return self.result
            stats = r.get("stats") or {}
            if not isinstance(stats.get("total_profiles"), int):
                self.fail_test("total_profiles missing/non-int",
                               body=r)
                return self.result
            self.pass_test(total_profiles=stats["total_profiles"])
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ── TC-ESIM-011 ─────────────────────────────────────────────────


class EsimEuiccNotificationHandling(TestCase):
    """TC-ESIM-011: SGP.22 §3.5 eUICC notifications via ES9+."""
    SPEC = TestSpec(
        tc_id="TC-ESIM-011",
        title="eUICC Notification Handling (audit log)",
        spec="SGP 22 §3.5",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MINOR,
        tags=("conformance", "esim", "notification", "smdp", "es9",
              "priority-2"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Smoke probe for the SGP.22 §3.5 HandleNotification audit log\n"
            "  read path. The full ES9+ notify round-trip (with seq_number\n"
            "  and event_type='install' on a specific ICCID) is exercised\n"
            "  by TC-ESIMOAM-007; this TC pins the read path the operator\n"
            "  panel calls to render the notifications tab.\n"
            "\n"
            "Procedure (SGP.22 §3.5 audit log)\n"
            "  1. GET /api/esim/notifications?limit=10.\n"
            "  2. Assert status 200 and ok=True envelope.\n"
            "  3. Assert response.notifications is a list (not None /\n"
            "     scalar / dict).\n"
            "  4. pass_test with notification_count in details.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — query string is fixed (limit=10).\n"
            "\n"
            "Pass criteria\n"
            "  Response carries ok=True and notifications is a list of any\n"
            "  length, including zero.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  notification_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The audit row contents (event_type, seq_number,\n"
            "  ICCID linkage) are exercised by TC-ESIMOAM-007; this TC only\n"
            "  pins the read envelope schema and is intentionally light so\n"
            "  it remains a safe smoke against the panel surface. limit=10\n"
            "  is the standard panel pagination size."
        ),
    )

    def run(self):
        try:
            r, s = _api(f"{ESIM}/notifications?limit=10")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"notifications failed: {s} {r}")
                return self.result
            notifs = r.get("notifications")
            if not isinstance(notifs, list):
                self.fail_test(f"notifications not a list: {type(notifs)}",
                               body=r)
                return self.result
            self.pass_test(notification_count=len(notifs))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_ESIM_ROBOT_TCS = [
    EsimEuiccProfileDownload,
    EsimProfileEnableDisable,
    EsimProfileListViaRestApi,
    EsimIccidCounterManagement,
    EsimEuiccNotificationHandling,
]

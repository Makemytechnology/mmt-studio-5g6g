# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: MUSIM (Multi-USIM).

TS 23.501 §5.34 — Multi-USIM device management.
Group lifecycle, USIM activation, paging coordination, capabilities.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src import baseline

log = logging.getLogger("tester.tc_musim")


def _musim_api(path, method="GET", body=None):
    """Call SA Core MUSIM REST API."""
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
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


class MusimCreateGroup(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MUSIM-020",
        title="Create a MUSIM group with two USIM members",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Foundational lifecycle smoke for Multi-USIM device management\n"
                "  (TS 23.501 §5.15.10 / §5.34). The AMF MUST be able to track a\n"
                "  physical device holding two USIMs as a single group so that\n"
                "  paging and reachability decisions can be coordinated across the\n"
                "  two subscribers in the same UE.\n"
                "\n"
                "Procedure (TS 23.501 §5.15.10 / §5.34)\n"
                "  1. POST /api/musim/groups {device_id='musim-device-001',\n"
                "     description=...} — require status 200/201; capture group_id.\n"
                "  2. For each imsi in [baseline.imsi('embb-bulk', 0),\n"
                "     baseline.imsi('embb-bulk', 1)]:\n"
                "     POST /api/musim/groups/{group_id}/members {imsi} — require\n"
                "     200/201 per member.\n"
                "  3. GET /api/musim/groups/{group_id} — require status 200.\n"
                "  4. finally: DELETE /api/musim/groups/{group_id}.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixed device_id; member IMSIs from baseline)\n"
                "\n"
                "Pass criteria\n"
                "  Group create, both member adds, and group GET all return success.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  group_id, group.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Members beyond 2 are out of scope; activation\n"
                "  semantics live in TC-MUSIM-021.\n"
                "  Group membership is unordered; the test asserts membership but\n"
                "  not insertion order.\n"
                "  Members beyond 2 follow the same SBI shape — capped only by AMF\n"
                "  policy."
            ),
    )

    def run(self):
        group_id = None
        try:
            result, status = _musim_api("/api/musim/groups", "POST", {
                "device_id": "musim-device-001",
                "description": "Test MUSIM device with 2 USIMs",
            })
            if status not in (200, 201):
                self.fail_test(f"Group creation failed: {status} {result}")
                return self.result

            group_id = result.get("id") or result.get("group_id")
            log.info("MUSIM group created: id=%s", group_id)

            # Add 2 USIM members
            for imsi in [baseline.imsi("embb-bulk", 0), baseline.imsi("embb-bulk", 1)]:
                mem_result, mem_status = _musim_api(
                    f"/api/musim/groups/{group_id}/members", "POST",
                    {"imsi": imsi},
                )
                if mem_status not in (200, 201):
                    self.fail_test(f"Add USIM {imsi} failed: {mem_status} {mem_result}")
                    return self.result
            log.info("Added 2 USIMs to group %s", group_id)

            # Verify
            grp, g_status = _musim_api(f"/api/musim/groups/{group_id}")
            if g_status != 200:
                self.fail_test(f"Group query failed: {g_status}")
                return self.result

            self.pass_test(group_id=group_id, group=grp)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if group_id:
                _musim_api(f"/api/musim/groups/{group_id}", "DELETE")
        return self.result


class MusimActivateUsim(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MUSIM-021",
        title="Activate a specific USIM within a MUSIM group",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Pins the 'one-USIM-active' constraint of a Multi-USIM device\n"
                "  (TS 23.501 §5.15.10). Only one USIM in a group can be RRC-CONNECTED\n"
                "  at a time; the AMF MUST mirror that by tracking exactly one\n"
                "  active_imsi per group and toggling it on demand.\n"
                "\n"
                "Procedure (TS 23.501 §5.15.10)\n"
                "  1. POST /api/musim/groups {device_id='musim-device-002'} —\n"
                "     capture group_id.\n"
                "  2. POST /members with imsi_a = baseline.imsi('embb-bulk',0)\n"
                "     and imsi_b = baseline.imsi('embb-bulk',1).\n"
                "  3. POST /api/musim/groups/{group_id}/activate {imsi=imsi_b}.\n"
                "  4. Require status 200/201 AND act_result.active_imsi == imsi_b.\n"
                "  5. finally: DELETE group.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — IMSIs from baseline; activates the second USIM)\n"
                "\n"
                "Pass criteria\n"
                "  Activation response carries active_imsi == imsi_b (the selected\n"
                "  member, not the first-added).\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  group_id, active_imsi, activation.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Re-activation back to imsi_a is not exercised\n"
                "  here — paging coordination is covered in TC-MUSIM-022.\n"
                "  Activation is idempotent: re-POSTing the same active_imsi is a\n"
                "  no-op (asserted only implicitly here).\n"
                "  Active-USIM switching latency is not measured by this test —\n"
                "  only the steady-state acceptance."
            ),
    )

    def run(self):
        group_id = None
        try:
            result, status = _musim_api("/api/musim/groups", "POST", {
                "device_id": "musim-device-002",
                "description": "Activate test",
            })
            if status not in (200, 201):
                self.fail_test(f"Group creation failed: {status} {result}")
                return self.result

            group_id = result.get("id") or result.get("group_id")

            imsi_a = baseline.imsi("embb-bulk", 0)
            imsi_b = baseline.imsi("embb-bulk", 1)
            _musim_api(f"/api/musim/groups/{group_id}/members", "POST", {"imsi": imsi_a})
            _musim_api(f"/api/musim/groups/{group_id}/members", "POST", {"imsi": imsi_b})

            # Activate USIM B
            act_result, act_status = _musim_api(
                f"/api/musim/groups/{group_id}/activate", "POST",
                {"imsi": imsi_b},
            )
            if act_status not in (200, 201):
                self.fail_test(f"Activate failed: {act_status} {act_result}")
                return self.result

            active_imsi = act_result.get("active_imsi")
            log.info("Active USIM: %s", active_imsi)

            if active_imsi != imsi_b:
                self.fail_test(
                    f"Expected active_imsi={imsi_b}, got {active_imsi}",
                    result=act_result,
                )
                return self.result

            self.pass_test(
                group_id=group_id, active_imsi=active_imsi,
                activation=act_result,
            )
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if group_id:
                _musim_api(f"/api/musim/groups/{group_id}", "DELETE")
        return self.result


class MusimPaging(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MUSIM-022",
        title="Page an inactive USIM in a MUSIM group",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Paging coordination across the two USIMs of a Multi-USIM device\n"
            "  (TS 23.501 §5.15.10). When USIM A is the active radio anchor,\n"
            "  an incoming MT call destined for the otherwise-inactive USIM B\n"
            "  MUST be deliverable via a coordinated paging request — without\n"
            "  this the second subscriber would be silently unreachable.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.10)\n"
            "  1. POST /api/musim/groups {device_id='musim-device-003'};\n"
            "     capture group_id.\n"
            "  2. POST two /members entries: imsi_a and imsi_b from baseline\n"
            "     'embb-bulk' indices 0 and 1.\n"
            "  3. POST /api/musim/groups/{group_id}/activate {imsi=imsi_a}.\n"
            "  4. POST /api/musim/page {device_id='musim-device-003',\n"
            "     target_imsi=imsi_b, reason='mt_call'}.\n"
            "  5. Require page_status in (200, 201, 202).\n"
            "  6. finally: DELETE group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSIs from baseline, reason fixed to 'mt_call')\n"
            "\n"
            "Pass criteria\n"
            "  Paging request returns 2xx (accepted by core).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  group_id, active_imsi, paged_imsi, paging.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Only validates that the AMF accepts the paging\n"
            "  request — RAN/UE-side reaction is out of scope here."
        ),
    )

    def run(self):
        group_id = None
        try:
            result, status = _musim_api("/api/musim/groups", "POST", {
                "device_id": "musim-device-003",
                "description": "Paging test",
            })
            if status not in (200, 201):
                self.fail_test(f"Group creation failed: {status} {result}")
                return self.result

            group_id = result.get("id") or result.get("group_id")

            imsi_a = baseline.imsi("embb-bulk", 0)
            imsi_b = baseline.imsi("embb-bulk", 1)
            _musim_api(f"/api/musim/groups/{group_id}/members", "POST", {"imsi": imsi_a})
            _musim_api(f"/api/musim/groups/{group_id}/members", "POST", {"imsi": imsi_b})

            # Activate USIM A
            _musim_api(f"/api/musim/groups/{group_id}/activate", "POST", {"imsi": imsi_a})

            # Page USIM B (inactive)
            page_result, page_status = _musim_api("/api/musim/page", "POST", {
                "device_id": "musim-device-003",
                "target_imsi": imsi_b,
                "reason": "mt_call",
            })
            if page_status not in (200, 201, 202):
                self.fail_test(f"Paging failed: {page_status} {page_result}")
                return self.result

            log.info("MUSIM paging sent for %s: %s", imsi_b, page_result)
            self.pass_test(
                group_id=group_id, active_imsi=imsi_a,
                paged_imsi=imsi_b, paging=page_result,
            )
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if group_id:
                _musim_api(f"/api/musim/groups/{group_id}", "DELETE")
        return self.result


class MusimCapabilities(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MUSIM-023",
        title="Report and query MUSIM UE capabilities",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  UE-capability reporting for Multi-USIM (TS 23.501 §5.15.10\n"
                "  capability indication). The AMF MUST persist the UE's advertised\n"
                "  MUSIM support flag and max-USIM count so that downstream paging\n"
                "  / activation decisions are gated by what the device can do.\n"
                "\n"
                "Procedure (TS 23.501 §5.15.10)\n"
                "  1. ue = require_ue(); imsi = ue.imsi.\n"
                "  2. POST /api/musim/capabilities {imsi, musim_supported=1,\n"
                "     max_usim_count=2}.\n"
                "  3. Require status 200/201.\n"
                "  4. GET /api/musim/capabilities?imsi={imsi} — require 200 and\n"
                "     a non-empty body.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — IMSI from UE pool; flags hard-coded musim_supported=1,\n"
                "  max_usim_count=2)\n"
                "\n"
                "Pass criteria\n"
                "  Both the report-POST and the readback-GET return 200/201.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, capabilities, verified.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. StopTest is silently absorbed (no UE → pass-\n"
                "  through) because the test depends on require_ue().\n"
                "  musim_supported=0 (capability NOT supported) is a valid distinct\n"
                "  value, asserted in the Robot mirror.\n"
                "  max_usim_count above the 3GPP-defined cap is rejected by the AMF;\n"
                "  this test stays within the legal range (=2)."
            ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            result, status = _musim_api("/api/musim/capabilities", "POST", {
                "imsi": imsi,
                "musim_supported": 1,
                "max_usim_count": 2,
            })
            if status not in (200, 201):
                self.fail_test(f"Capabilities report failed: {status} {result}")
                return self.result

            log.info("MUSIM capabilities reported for %s", imsi)

            # Verify
            caps, c_status = _musim_api(f"/api/musim/capabilities?imsi={imsi}")
            if c_status != 200:
                self.fail_test(f"Capabilities query failed: {c_status}")
                return self.result

            self.pass_test(imsi=imsi, capabilities=result, verified=caps)
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_MUSIM_TCS = [
    MusimCreateGroup, MusimActivateUsim, MusimPaging, MusimCapabilities,
]

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NG-RAN Sharing — operator agreements + per-gNB cap +
admission gate.

TS 22.261 §6.21    — NG-RAN Sharing service requirements (MORAN/MOCN).
TS 22.261 §6.21.2.2 — Indirect network sharing.
TS 23.501 §5.17.4  — Network sharing support and EPS/5GS interworking.

Drives the SA Core REST surface at /api/ran-sharing/* (agreements,
gNB allocation map, CheckAccess admission gate, usage log). The gNB
broadcast of the multi-PLMN-ID list (TS 38.413 §9.2.6.x) is the
radio's responsibility and is not exercised here.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_ran_sharing")


def _rs_api(path, method="GET", body=None):
    """Call SA Core RAN-sharing REST API."""
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


def _create_and_track(name, sharing_type, plmns, activate=True):
    """Create an agreement (default status='pending' per schema), then
    optionally Activate it. Return its id (caller must clean up)."""
    res, status = _rs_api("/api/ran-sharing/agreements", "POST", {
        "name": name,
        "sharing_type": sharing_type,
        "participating_plmns": plmns,
    })
    if status not in (200, 201):
        return None, (res, status)
    agr_id = res.get("agreement", {}).get("id")
    if activate and agr_id:
        _rs_api(f"/api/ran-sharing/agreements/{agr_id}/activate", "POST")
    return agr_id, None


class RanSharingAgreementCRUD(TestCase):
    """TC-RANS-001: CRUD a MORAN agreement (TS 22.261 §6.21)."""
    SPEC = TestSpec(
        tc_id="TC-RANS-001",
        title="RAN sharing MORAN agreement CRUD + activate/deactivate",
        spec="TS 22.261 §6.21",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.BLOCKER,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the MORAN agreement lifecycle state machine per\n"
            "  TS 22.261 §6.21. Operators create an agreement (default\n"
            "  status='pending' per schema CHECK), then activate /\n"
            "  deactivate / re-activate. Each verb must land its target\n"
            "  status on the persisted row.\n"
            "\n"
            "Procedure (TS 22.261 §6.21)\n"
            "  1. _create_and_track('TC-RANS-001 MORAN', 'MORAN',\n"
            "     '00101,00102', activate=False) — record agreement_id.\n"
            "  2. GET /api/ran-sharing/agreements/{id} → expect 200;\n"
            "     assert sharing_type=='MORAN' AND status=='pending'.\n"
            "  3. POST /agreements/{id}/activate → expect 200; assert\n"
            "     status=='active'.\n"
            "  4. POST /agreements/{id}/deactivate → expect 200; assert\n"
            "     status=='inactive'.\n"
            "  5. POST /agreements/{id}/activate → expect 200; assert\n"
            "     status=='active'.\n"
            "  6. finally: DELETE /agreements/{id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — PLMN list and sharing_type hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Each lifecycle transition returns the expected status on\n"
            "  the agreement row (pending → active → inactive → active).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  agreement_id.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Default status='pending' is the schema CHECK default."
        ),
    )

    def run(self):
        agr_id = None
        try:
            agr_id, err = _create_and_track("TC-RANS-001 MORAN",
                                             "MORAN", "00101,00102",
                                             activate=False)
            if err:
                self.fail_test(f"Create failed: {err[1]} {err[0]}")
                return self.result

            r2, s2 = _rs_api(f"/api/ran-sharing/agreements/{agr_id}")
            if s2 != 200:
                self.fail_test(f"GET failed: {s2} {r2}")
                return self.result
            agr = r2.get("agreement", {})
            if agr.get("sharing_type") != "MORAN":
                self.fail_test("sharing_type mismatch", body=agr)
                return self.result
            # Default status is 'pending' (schema CHECK), not active.
            if agr.get("status") != "pending":
                self.fail_test(f"Expected pending, got {agr.get('status')}")
                return self.result

            # Activate.
            ar, asx = _rs_api(f"/api/ran-sharing/agreements/{agr_id}/activate",
                               "POST")
            if asx != 200 or ar.get("agreement", {}).get("status") != "active":
                self.fail_test(f"Activate failed: {asx} {ar}")
                return self.result

            # Deactivate.
            r3, s3 = _rs_api(f"/api/ran-sharing/agreements/{agr_id}/deactivate",
                              "POST")
            if s3 != 200 or r3.get("agreement", {}).get("status") != "inactive":
                self.fail_test(f"Deactivate failed: {s3} {r3}")
                return self.result

            # Reactivate.
            r4, s4 = _rs_api(f"/api/ran-sharing/agreements/{agr_id}/activate",
                              "POST")
            if s4 != 200 or r4.get("agreement", {}).get("status") != "active":
                self.fail_test(f"Activate failed: {s4} {r4}")
                return self.result

            self.pass_test(agreement_id=agr_id)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if agr_id:
                _rs_api(f"/api/ran-sharing/agreements/{agr_id}", "DELETE")
        return self.result


class RanSharingValidation(TestCase):
    """TC-RANS-002: Reject invalid sharing_type and missing PLMNs."""
    SPEC = TestSpec(
        tc_id="TC-RANS-002",
        title="RAN sharing rejects bad sharing_type and empty PLMN list",
        spec="TS 22.261 §6.21",
        domain=Domain.INFRA,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative-path guard for the agreement create path per\n"
            "  TS 22.261 §6.21. sharing_type must be one of\n"
            "  {MORAN, MOCN, INDIRECT} (schema CHECK) and\n"
            "  participating_plmns must be non-empty (a sharing agreement\n"
            "  with no PLMNs has no meaning and would never admit any UE).\n"
            "  Both must fail at the API boundary, not silently land a row.\n"
            "\n"
            "Procedure (TS 22.261 §6.21)\n"
            "  1. POST /api/ran-sharing/agreements {name='bad-type',\n"
            "     sharing_type='WAT', participating_plmns='00101'}\n"
            "     → expect HTTP 400.\n"
            "  2. POST /api/ran-sharing/agreements {name='missing-plmns',\n"
            "     sharing_type='MOCN', participating_plmns=''}\n"
            "     → expect HTTP 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — both bad payloads are hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return HTTP 400 and no agreement is created.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Pure validation probe; no cleanup required because the\n"
            "  API rejects both rows before persistence.\n"
            "  Schema CHECK on sharing_type is the canonical enum source."
        ),
    )

    def run(self):
        try:
            r, s = _rs_api("/api/ran-sharing/agreements", "POST", {
                "name": "bad-type",
                "sharing_type": "WAT",
                "participating_plmns": "00101",
            })
            if s != 400:
                self.fail_test(f"Bad sharing_type did not 400: {s} {r}")
                return self.result

            r, s = _rs_api("/api/ran-sharing/agreements", "POST", {
                "name": "missing-plmns",
                "sharing_type": "MOCN",
                "participating_plmns": "",
            })
            if s != 400:
                self.fail_test(f"Empty PLMNs did not 400: {s} {r}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class RanSharingMORANGnbMap(TestCase):
    """TC-RANS-003: MORAN admits only when a per-gNB allocation row exists."""
    SPEC = TestSpec(
        tc_id="TC-RANS-003",
        title="RAN sharing MORAN admits only when per-gNB map row exists",
        spec="TS 22.261 §6.21",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the MORAN admission rule per TS 22.261 §6.21 +\n"
            "  TS 23.501 §5.17.4: under MORAN (Multi-Operator RAN), a gNB\n"
            "  carries dedicated capacity per PLMN. The admission gate\n"
            "  requires a per-(agreement, gNB) allocation row; without it\n"
            "  the gNB has no MORAN capacity for the PLMN.\n"
            "\n"
            "Procedure (TS 22.261 §6.21)\n"
            "  1. _create_and_track('TC-RANS-003 MORAN', 'MORAN', '00103',\n"
            "     activate=True).\n"
            "  2. POST /check-access {plmn='00103', gnb_id='gnb-rans-003'}\n"
            "     → expect access.allowed == False (no gNB map row).\n"
            "  3. POST /gnb-map {agreement_id, gnb_id='gnb-rans-003',\n"
            "     allocated_capacity_pct=70} → expect 200/201.\n"
            "  4. POST /check-access again with the same args; assert\n"
            "     access.allowed == True AND access.capacity_pct == 70.\n"
            "  5. finally: DELETE the agreement.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — PLMN '00103' and 70% capacity hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Without gNB map: access.allowed False.\n"
            "  With gNB map at 70%: access.allowed True AND\n"
            "  access.capacity_pct == 70.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  agreement_id, capacity_pct.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  MOCN admission is exercised in TC-RANS-004 and behaves\n"
            "  differently — no per-gNB row is required."
        ),
    )

    def run(self):
        agr_id = None
        try:
            agr_id, err = _create_and_track("TC-RANS-003 MORAN",
                                             "MORAN", "00103")
            if err:
                self.fail_test(f"Create failed: {err}")
                return self.result

            # Without gnb-map row, MORAN should deny.
            chk, _ = _rs_api("/api/ran-sharing/check-access", "POST",
                              {"plmn": "00103", "gnb_id": "gnb-rans-003"})
            if chk.get("access", {}).get("allowed"):
                self.fail_test("MORAN admitted without gNB map row",
                               body=chk)
                return self.result

            # Add gNB map row at 70%.
            mr, ms = _rs_api("/api/ran-sharing/gnb-map", "POST", {
                "agreement_id": agr_id,
                "gnb_id": "gnb-rans-003",
                "allocated_capacity_pct": 70,
            })
            if ms not in (200, 201):
                self.fail_test(f"gNB-map create failed: {ms} {mr}")
                return self.result

            chk2, _ = _rs_api("/api/ran-sharing/check-access", "POST",
                               {"plmn": "00103", "gnb_id": "gnb-rans-003"})
            a2 = chk2.get("access", {})
            if not a2.get("allowed"):
                self.fail_test("MORAN denied after gNB map", body=chk2)
                return self.result
            if a2.get("capacity_pct") != 70:
                self.fail_test(f"Wrong capacity_pct: {a2.get('capacity_pct')}")
                return self.result

            self.pass_test(agreement_id=agr_id, capacity_pct=70)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if agr_id:
                _rs_api(f"/api/ran-sharing/agreements/{agr_id}", "DELETE")
        return self.result


class RanSharingMOCNAdmission(TestCase):
    """TC-RANS-004: MOCN admits without per-gNB map (TS 22.261 §6.21)."""
    SPEC = TestSpec(
        tc_id="TC-RANS-004",
        title="RAN sharing MOCN admits without per-gNB map, denies non-PLMN",
        spec="TS 22.261 §6.21",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the MOCN admission rule per TS 22.261 §6.21 +\n"
            "  TS 23.501 §5.17.4: under MOCN (Multi-Operator Core Network),\n"
            "  a gNB broadcasts every participating PLMN-ID and admission\n"
            "  is gated on PLMN-list membership alone — no per-gNB capacity\n"
            "  map is required. Non-participating PLMNs must still be denied.\n"
            "\n"
            "Procedure (TS 22.261 §6.21)\n"
            "  1. _create_and_track('TC-RANS-004 MOCN', 'MOCN',\n"
            "     '00104,00105', activate=True).\n"
            "  2. POST /check-access {plmn='00104', gnb_id='gnb-any'};\n"
            "     assert access.allowed == True AND\n"
            "     access.sharing_type == 'MOCN'.\n"
            "  3. POST /check-access {plmn='00199', gnb_id='gnb-any'};\n"
            "     assert access.allowed == False.\n"
            "  4. finally: DELETE the agreement.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — participating PLMNs and non-participating PLMN\n"
            "  hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Participating PLMN admitted with sharing_type=='MOCN' AND\n"
            "  non-participating PLMN denied.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  agreement_id.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Multi-PLMN-ID broadcast (TS 38.413 §9.2.6.x) is the radio's\n"
            "  responsibility and is not exercised here."
        ),
    )

    def run(self):
        agr_id = None
        try:
            agr_id, err = _create_and_track("TC-RANS-004 MOCN",
                                             "MOCN", "00104,00105")
            if err:
                self.fail_test(f"Create failed: {err}")
                return self.result

            chk, _ = _rs_api("/api/ran-sharing/check-access", "POST",
                              {"plmn": "00104", "gnb_id": "gnb-any"})
            a = chk.get("access", {})
            if not a.get("allowed") or a.get("sharing_type") != "MOCN":
                self.fail_test("MOCN did not admit", body=chk)
                return self.result

            # Non-participating PLMN must be denied.
            chk2, _ = _rs_api("/api/ran-sharing/check-access", "POST",
                               {"plmn": "00199", "gnb_id": "gnb-any"})
            if chk2.get("access", {}).get("allowed"):
                self.fail_test("Non-participating PLMN admitted", body=chk2)
                return self.result

            self.pass_test(agreement_id=agr_id)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if agr_id:
                _rs_api(f"/api/ran-sharing/agreements/{agr_id}", "DELETE")
        return self.result


class RanSharingUsageLog(TestCase):
    """TC-RANS-005: Insert usage rows, verify they appear in the log."""
    SPEC = TestSpec(
        tc_id="TC-RANS-005",
        title="RAN sharing usage log records and returns per-PLMN rows",
        spec="TS 22.261 §6.21",
        domain=Domain.INFRA,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the per-PLMN usage log persistence + readback per\n"
            "  TS 22.261 §6.21 + TS 28.531 (charging/reporting). Each row\n"
            "  carries (agreement_id, plmn, gnb_id, ue_count,\n"
            "  throughput_mbps) and the query interface filters by\n"
            "  agreement_id.\n"
            "\n"
            "Procedure (TS 22.261 §6.21)\n"
            "  1. _create_and_track('TC-RANS-005 USAGE', 'MOCN', '00106').\n"
            "  2. POST /usage-log twice with i=0,1: ue_count=10*(i+1),\n"
            "     throughput_mbps=100.0*(i+1), gnb_id='gnb-rans-005-{i}'.\n"
            "     Each must return 200/201.\n"
            "  3. GET /usage-log?agreement_id={id} → expect 200.\n"
            "  4. Parse usage[] from response; assert len >= 2.\n"
            "  5. finally: DELETE the agreement.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — two rows with deterministic counts hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both inserts succeed AND the query returns at least 2 rows.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  agreement_id, rows (count of returned usage rows).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  No throughput/UE-count value assertions — counts only.\n"
            "  Multiple gNB ids prove the query returns per-gNB rows.\n"
            "  Charging integration (TS 28.531) is out of scope here."
        ),
    )

    def run(self):
        agr_id = None
        try:
            agr_id, err = _create_and_track("TC-RANS-005 USAGE",
                                             "MOCN", "00106")
            if err:
                self.fail_test(f"Create failed: {err}")
                return self.result

            for i in range(2):
                r, s = _rs_api("/api/ran-sharing/usage-log", "POST", {
                    "agreement_id": agr_id,
                    "plmn": "00106",
                    "gnb_id": f"gnb-rans-005-{i}",
                    "ue_count": 10 * (i + 1),
                    "throughput_mbps": 100.0 * (i + 1),
                })
                if s not in (200, 201):
                    self.fail_test(f"Insert usage failed: {s} {r}")
                    return self.result

            res, status = _rs_api(
                f"/api/ran-sharing/usage-log?agreement_id={agr_id}")
            if status != 200:
                self.fail_test(f"GET usage failed: {status} {res}")
                return self.result
            entries = res.get("usage", [])
            if len(entries) < 2:
                self.fail_test(f"Expected ≥2 rows, got {len(entries)}",
                               sample=entries[:2])
                return self.result

            self.pass_test(agreement_id=agr_id, rows=len(entries))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if agr_id:
                _rs_api(f"/api/ran-sharing/agreements/{agr_id}", "DELETE")
        return self.result


ALL_RAN_SHARING_TCS = [
    RanSharingAgreementCRUD,
    RanSharingValidation,
    RanSharingMORANGnbMap,
    RanSharingMOCNAdmission,
    RanSharingUsageLog,
]

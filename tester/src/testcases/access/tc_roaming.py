# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Roaming — operator-side agreements + sessions + CDRs.

TS 23.501 §5.6.3   — Session Management Roaming (HR vs LBO).
TS 23.501 §5.7.1.11 — QoS aspects of home-routed roaming.
TS 32.240 / 32.298 — Charging architecture + CDR fields.

Drives the SA Core REST surface at /api/roaming/* (agreements, active
sessions, detection probe, CDR ledger, TAP export). The actual SBI
SEPP / N32 wire path is not exercised here — that lives in
infra/roaming/sepp.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_roaming")


def _roam_api(path, method="GET", body=None):
    """Call SA Core roaming REST API."""
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


class RoamingAgreementCRUD(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ROAM-001",
        title="Roaming agreement CRUD round-trip (POST/GET/PATCH/DELETE)",
        spec="TS 23.501 §5.6.3",
        domain=Domain.ROAMING,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundation gate for every downstream roaming test in this file.\n"
            "  Pins TS 23.501 §5.18 (operator agreements layer) and TS 23.122\n"
            "  (PLMN selection): the operator must be able to declare a\n"
            "  Visited-PLMN partnership row carrying direction + roaming_mode\n"
            "  + endpoints, retrieve it, mutate enabled, and tear it down\n"
            "  without leaking state.\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + TS 23.122 §3.2)\n"
            "  1. DELETE /api/roaming/agreements/999-99 (cleanup prior run).\n"
            "  2. POST /api/roaming/agreements with full envelope: PLMN,\n"
            "     name, direction='both', roaming_mode='lbo', max_ues=100,\n"
            "     and four endpoint URLs (AUSF, UDM, SMF, SEPP).\n"
            "  3. Require HTTP 200/201.\n"
            "  4. GET /api/roaming/agreements/{plmn}; assert HTTP 200,\n"
            "     partner_plmn_id matches, roaming_mode=='lbo',\n"
            "     direction=='both'.\n"
            "  5. PATCH /api/roaming/agreements/{plmn}/enabled body\n"
            "     {enabled:false}; require HTTP 200.\n"
            "  6. finally: DELETE /api/roaming/agreements/{plmn} (always).\n"
            "  7. pass_test(plmn=plmn).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — synthetic PLMN 999-99 used for isolation).\n"
            "\n"
            "Pass criteria\n"
            "  All four REST steps return their expected status codes AND\n"
            "  the GET round-trip preserves direction+roaming_mode.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  plmn (synthetic partner PLMN echoed back on pass).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — pure REST against in-process SA Core. No N32\n"
            "  / SEPP wire exchange. DELETE is in finally{} so a failed\n"
            "  assertion still cleans up the synthetic row."
        ),
    )

    def run(self):
        plmn = "999-99"
        try:
            # Cleanup from prior run.
            _roam_api(f"/api/roaming/agreements/{plmn}", "DELETE")

            # Create.
            res, status = _roam_api("/api/roaming/agreements", "POST", {
                "partner_plmn_id": plmn,
                "partner_name": "Test Roaming Partner",
                "direction": "both",
                "roaming_mode": "lbo",
                "max_ues": 100,
                "ausf_endpoint": "http://hplmn.example:8080",
                "udm_endpoint":  "http://hplmn.example:8081",
                "smf_endpoint":  "http://hplmn.example:8082",
                "sepp_endpoint": "https://sepp.example:32443",
            })
            if status not in (200, 201):
                self.fail_test(f"Create failed: {status} {res}")
                return self.result

            # Read.
            r2, s2 = _roam_api(f"/api/roaming/agreements/{plmn}")
            if s2 != 200 or r2.get("partner_plmn_id") != plmn:
                self.fail_test(f"GET mismatch: {s2} {r2}")
                return self.result
            if r2.get("roaming_mode") != "lbo" or r2.get("direction") != "both":
                self.fail_test("Mode/direction mismatch", body=r2)
                return self.result

            # Disable via PATCH.
            r3, s3 = _roam_api(f"/api/roaming/agreements/{plmn}/enabled",
                                "PATCH", {"enabled": False})
            if s3 != 200:
                self.fail_test(f"PATCH disable failed: {s3} {r3}")
                return self.result

            log.info("Roaming agreement CRUD ok: %s", plmn)
            self.pass_test(plmn=plmn)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _roam_api(f"/api/roaming/agreements/{plmn}", "DELETE")
        return self.result


class RoamingValidation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ROAM-002",
        title="Roaming agreement rejects invalid direction / mode values",
        spec="TS 23.501 §5.6.3",
        domain=Domain.ROAMING,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative-path schema gate for /api/roaming/agreements. The\n"
            "  TS 23.501 §5.18 roaming model only defines two roaming modes\n"
            "  (Home-Routed / Local Break-Out) and three directions\n"
            "  (inbound / outbound / both). Accepting strangers in either\n"
            "  field would poison the partner database and could mis-route\n"
            "  PDU sessions at the SMF.\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + TS 23.122)\n"
            "  1. POST /api/roaming/agreements with partner_plmn_id=999-98,\n"
            "     direction='sideways', roaming_mode='lbo'. Require HTTP 400.\n"
            "  2. POST /api/roaming/agreements with partner_plmn_id=999-98,\n"
            "     direction='both', roaming_mode='magic'. Require HTTP 400.\n"
            "  3. Any other status code (200/201/422/5xx) fails the test.\n"
            "  4. pass_test() with no kwargs on success.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — both payloads hardcoded for schema fuzzing).\n"
            "\n"
            "Pass criteria\n"
            "  Both bad-direction and bad-mode POSTs return HTTP 400 exactly.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() called without kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — no cleanup needed since the POSTs are\n"
            "  expected to be rejected before persisting. Does not check\n"
            "  the response body — only the status code. Test would still\n"
            "  pass against a stub that 400-rejects every roaming POST."
        ),
    )

    def run(self):
        plmn = "999-98"
        try:
            r, s = _roam_api("/api/roaming/agreements", "POST", {
                "partner_plmn_id": plmn,
                "direction": "sideways",
                "roaming_mode": "lbo",
            })
            if s != 400:
                self.fail_test(f"Bad direction did not 400: {s} {r}")
                return self.result

            r, s = _roam_api("/api/roaming/agreements", "POST", {
                "partner_plmn_id": plmn,
                "direction": "both",
                "roaming_mode": "magic",
            })
            if s != 400:
                self.fail_test(f"Bad mode did not 400: {s} {r}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class RoamingDetect(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ROAM-003",
        title="DetectRoaming probe matches an in-DB agreement",
        spec="TS 23.501 §5.6.3",
        domain=Domain.ROAMING,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the IMSI->HPLMN extraction + agreement lookup pipeline\n"
            "  (TS 23.501 §5.18, TS 23.122 §4.2 PLMN selection). When an\n"
            "  AMF sees a UE whose home PLMN is in the partner-PLMN table,\n"
            "  it must classify the session as roaming and propagate the\n"
            "  configured roaming_mode to the SMF for HR vs LBO selection.\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + TS 23.122 §4.2)\n"
            "  1. POST /api/roaming/agreements creating HPLMN 310-260\n"
            "     ('T-Mobile US') with direction='both', roaming_mode='hr'.\n"
            "  2. GET /api/roaming/detect/310260999900001 — IMSI whose\n"
            "     MCC=310 MNC=260 matches the partner row.\n"
            "  3. Assert HTTP 200.\n"
            "  4. Assert response.is_roaming truthy.\n"
            "  5. Assert response.home_plmn_id == '310-260'.\n"
            "  6. Assert response.roaming_mode == 'hr'.\n"
            "  7. finally: DELETE /api/roaming/agreements/310-260.\n"
            "  8. pass_test(home_plmn=plmn, mode=res.roaming_mode).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — PLMN and IMSI hardcoded for deterministic match).\n"
            "\n"
            "Pass criteria\n"
            "  Detect probe returns HTTP 200 AND is_roaming truthy AND\n"
            "  home_plmn_id == provisioned PLMN AND roaming_mode == 'hr'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  home_plmn, mode.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — pure REST. Agreement always torn down in\n"
            "  finally{}. Test does not exercise the negative case where\n"
            "  the IMSI's PLMN is not in the table; that path is covered\n"
            "  implicitly by the absence-by-default of unprovisioned PLMNs."
        ),
    )

    def run(self):
        plmn = "310-260"
        imsi = "310260999900001"
        try:
            _roam_api("/api/roaming/agreements", "POST", {
                "partner_plmn_id": plmn,
                "partner_name": "T-Mobile US",
                "direction": "both",
                "roaming_mode": "hr",
            })

            res, status = _roam_api(f"/api/roaming/detect/{imsi}")
            if status != 200:
                self.fail_test(f"Detect failed: {status} {res}")
                return self.result
            if not res.get("is_roaming"):
                self.fail_test("Detect did not mark IMSI as roaming",
                               body=res)
                return self.result
            if res.get("home_plmn_id") != plmn:
                self.fail_test(f"Wrong HPLMN: got {res.get('home_plmn_id')} "
                               f"expected {plmn}")
                return self.result
            if res.get("roaming_mode") != "hr":
                self.fail_test(f"Wrong mode: {res.get('roaming_mode')}")
                return self.result

            self.pass_test(home_plmn=plmn, mode=res.get("roaming_mode"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _roam_api(f"/api/roaming/agreements/{plmn}", "DELETE")
        return self.result


class RoamingSessionLifecycle(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ROAM-004",
        title="Roaming session lifecycle — open, list, release",
        spec="TS 23.501 §5.6.3",
        domain=Domain.ROAMING,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the active-roaming-session state machine on the V-SMF\n"
            "  (TS 23.501 §5.18 + §5.6.3 — SM aspects of roaming). Each\n"
            "  inbound roamer must yield a single row in the active session\n"
            "  table at admission and be cleanly removed on release; orphan\n"
            "  rows would mis-count concurrent roamers against partner caps.\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + §5.6.3)\n"
            "  1. POST /api/roaming/sessions {imsi=310260999900002,\n"
            "     home_plmn=310-260, visited_plmn=001-01, direction=inbound,\n"
            "     roaming_mode=lbo}. Require HTTP 200/201.\n"
            "  2. GET /api/roaming/sessions; assert any row matches our IMSI.\n"
            "  3. POST /api/roaming/sessions/{imsi}/release; require HTTP 200.\n"
            "  4. GET /api/roaming/sessions again; assert no row matches IMSI.\n"
            "  5. pass_test(imsi=imsi).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hardcoded synthetic IMSI for isolation).\n"
            "\n"
            "Pass criteria\n"
            "  Create returns 200/201 AND IMSI present in active list AND\n"
            "  release returns 200 AND IMSI absent from active list after.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — no real N9/N32 traffic; only the session\n"
            "  table is exercised. No finally{} cleanup: if release fails\n"
            "  mid-test the row is left behind for the next harness reset."
        ),
    )

    def run(self):
        imsi = "310260999900002"
        try:
            res, status = _roam_api("/api/roaming/sessions", "POST", {
                "imsi": imsi,
                "home_plmn_id": "310-260",
                "visited_plmn_id": "001-01",
                "direction": "inbound",
                "roaming_mode": "lbo",
            })
            if status not in (200, 201):
                self.fail_test(f"Create session failed: {status} {res}")
                return self.result

            actives, _ = _roam_api("/api/roaming/sessions")
            seen = any(s.get("imsi") == imsi for s in actives)
            if not seen:
                self.fail_test("Session not in active list", count=len(actives))
                return self.result

            r2, s2 = _roam_api(f"/api/roaming/sessions/{imsi}/release", "POST")
            if s2 != 200:
                self.fail_test(f"Release failed: {s2} {r2}")
                return self.result

            actives, _ = _roam_api("/api/roaming/sessions")
            still = any(s.get("imsi") == imsi for s in actives)
            if still:
                self.fail_test("Session still active after release")
                return self.result

            self.pass_test(imsi=imsi)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class RoamingCDRExport(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ROAM-005",
        title="Roaming CDR ledger — insert, stats, TAP export",
        spec="TS 32.240",
        domain=Domain.CHARGING,
        nfs=(NF.CHF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the roaming-CDR ledger end-to-end (TS 32.240 charging\n"
            "  architecture; TS 23.501 §5.18 — roaming records). Each\n"
            "  closed roaming session must yield a CDR; the ledger must\n"
            "  surface it as unexported until the TAP/3 export runs, and\n"
            "  must drop the unexported counter to zero afterward — the\n"
            "  monotonic invariant that interconnect billing depends on.\n"
            "\n"
            "Procedure (TS 32.240 + TS 23.501 §5.18)\n"
            "  1. POST /api/roaming/cdrs with synthetic session record:\n"
            "     imsi=310260999900003, home_plmn=310-260, visited=001-01,\n"
            "     direction=inbound, record_type=session, bytes_ul=1000,\n"
            "     bytes_dl=2000, duration_sec=60.0. Require HTTP 200/201.\n"
            "  2. GET /api/roaming/cdrs/stats; assert unexported >= 1.\n"
            "  3. POST /api/roaming/cdrs/export; assert HTTP 200 and the\n"
            "     response carries exported >= 1.\n"
            "  4. GET /api/roaming/cdrs/stats again; assert unexported == 0.\n"
            "  5. pass_test(exported=<from export>, total=<total_cdrs>).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — single synthetic CDR shape).\n"
            "\n"
            "Pass criteria\n"
            "  Create 200/201 AND stats.unexported >= 1 before export AND\n"
            "  export 200 with exported >= 1 AND stats.unexported == 0 after.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  exported, total.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — the export is local-file or in-memory in the\n"
            "  simulator; no real TAP/3 batch transmission is performed.\n"
            "  Test does not clean up the inserted CDR row, so total_cdrs\n"
            "  drifts upward across runs."
        ),
    )

    def run(self):
        try:
            r, s = _roam_api("/api/roaming/cdrs", "POST", {
                "imsi": "310260999900003",
                "home_plmn_id": "310-260",
                "visited_plmn_id": "001-01",
                "direction": "inbound",
                "record_type": "session",
                "bytes_ul": 1000,
                "bytes_dl": 2000,
                "duration_sec": 60.0,
            })
            if s not in (200, 201):
                self.fail_test(f"Create CDR failed: {s} {r}")
                return self.result

            stats, _ = _roam_api("/api/roaming/cdrs/stats")
            if stats.get("unexported", 0) < 1:
                self.fail_test("CDR not in stats unexported", body=stats)
                return self.result

            er, es = _roam_api("/api/roaming/cdrs/export", "POST")
            if es != 200 or er.get("exported", 0) < 1:
                self.fail_test(f"Export failed: {es} {er}")
                return self.result

            stats2, _ = _roam_api("/api/roaming/cdrs/stats")
            if stats2.get("unexported") != 0:
                self.fail_test("Unexported should be 0 after export",
                               body=stats2)
                return self.result

            self.pass_test(exported=er.get("exported"),
                           total=stats2.get("total_cdrs"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class RoamingEquivalentPLMNList(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ROAM-010",
        title="Equivalent PLMNs IE surfaces in roaming agreement record",
        spec="TS 23.501 §5.6.3",
        domain=Domain.ROAMING,
        nfs=(NF.AMF,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MINOR,
        tags=("conformance", "equivalent-plmn"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Persistence smoke for the partner PLMN identifier that the\n"
            "  AMF will later surface as the Equivalent PLMNs IE\n"
            "  (TS 24.501 §9.11.3.45) in NAS Registration Accept. Pins\n"
            "  TS 23.501 §5.18 + TS 23.122 §3 — the operator-configured\n"
            "  equivalent-PLMN list is sourced from the same roaming\n"
            "  agreement table.\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + TS 23.122 §3)\n"
            "  1. DELETE /api/roaming/agreements/001-02 (cleanup prior run).\n"
            "  2. POST /api/roaming/agreements with partner_plmn_id=001-02,\n"
            "     name='TC-ROAM-010 equivalent PLMN', direction='both',\n"
            "     roaming_mode='lbo'. Require HTTP 200/201; otherwise emit\n"
            "     'Python implementation pending' fail message pointing at\n"
            "     robot/suites/mobility/29_roaming.robot::TC-ROAM-010.\n"
            "  3. GET /api/roaming/agreements/001-02; assert HTTP 200 and\n"
            "     partner_plmn_id round-trips.\n"
            "  4. finally: DELETE the synthetic agreement.\n"
            "  5. pass_test(equivalent_plmn, direction, roaming_mode).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed synthetic PLMN 001-02).\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET 200 AND partner_plmn_id == '001-02'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  equivalent_plmn, direction, roaming_mode.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no SA Core baseline assumed. This is a hollow-\n"
            "  pass shape for the IE: the Python test only verifies REST\n"
            "  persistence of the PLMN id; the NAS-leg encoding of\n"
            "  Equivalent PLMNs IE (TS 24.501 §9.11.3.45) lives in the\n"
            "  Robot scenario robot/suites/mobility/29_roaming.robot."
        ),
    )

    def run(self):
        plmn = "001-02"
        try:
            _roam_api(f"/api/roaming/agreements/{plmn}", "DELETE")
            res, status = _roam_api("/api/roaming/agreements", "POST", {
                "partner_plmn_id": plmn,
                "partner_name": "TC-ROAM-010 equivalent PLMN",
                "direction": "both",
                "roaming_mode": "lbo",
            })
            if status not in (200, 201):
                self.fail_test(
                    "Python implementation pending — see "
                    "robot/suites/mobility/29_roaming.robot::TC-ROAM-010 "
                    "for the procedure.",
                    response=res, status=status)
                return self.result

            r2, s2 = _roam_api(f"/api/roaming/agreements/{plmn}")
            if s2 != 200 or r2.get("partner_plmn_id") != plmn:
                self.fail_test(f"agreement read-back wrong: {s2} {r2}")
                return self.result
            self.pass_test(equivalent_plmn=plmn,
                           direction=r2.get("direction"),
                           roaming_mode=r2.get("roaming_mode"))
        except Exception as e:
            self.fail_test(
                "Python implementation pending — see "
                "robot/suites/mobility/29_roaming.robot::TC-ROAM-010 "
                "for the procedure.",
                error=str(e))
        finally:
            _roam_api(f"/api/roaming/agreements/{plmn}", "DELETE")
        return self.result


ALL_ROAMING_TCS = [
    RoamingAgreementCRUD,
    RoamingValidation,
    RoamingDetect,
    RoamingSessionLifecycle,
    RoamingCDRExport,
    RoamingEquivalentPLMNList,
]

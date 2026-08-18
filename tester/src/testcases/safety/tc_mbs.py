# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: 5G Multicast/Broadcast Services (MBS).

TS 23.247 §4.1   5G MBS architecture (umbrella).
TS 23.247 §4.2   MBS reference points (N6mb, MBSF, MBSU, MB-UPF).
TS 23.247 §7     MBS Session Procedures (Create/Activate/Deactivate/Release).
TS 23.247 §7.2   MBS service-area handling (TAI scoping).

Drives the SA Core REST surface at /api/mbs/* — sessions, members,
service areas, content delivery, and the audit log.

All endpoints return `{ok, ...}` envelopes keyed by domain noun
(`sessions`, `session`, `areas`, `area`, `members`, `delivery`,
`content_log`, `stats`).
"""

import json
import logging
import time
import urllib.request
import urllib.error

from src import baseline
from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_mbs")


def _mbs_api(path, method="GET", body=None):
    """Call SA Core MBS REST API."""
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


def _create_session(tmgi, **kwargs):
    """Create an MBS session; return (id, session, error)."""
    body = {
        "tmgi": tmgi,
        "name": "tc-mbs",
        "session_type": "multicast",
        "qos_5qi": 9,
    }
    body.update(kwargs)
    res, status = _mbs_api("/api/mbs/sessions", "POST", body)
    if status not in (200, 201):
        return None, None, f"create failed: {status} {res}"
    sess = res.get("session", {})
    return sess.get("id"), sess, None


def _delete_session(sid):
    if sid:
        _mbs_api(f"/api/mbs/sessions/{sid}", "DELETE")


class MbsSessionLifecycle(TestCase):
    """TC-MBS-001: create → activate → deactivate (TS 23.247 §7)."""
    SPEC = TestSpec(
        tc_id="TC-MBS-001",
        title="MBS session lifecycle: create → activate → deactivate",
        spec="TS 23.247 §7",
        domain=Domain.VAS,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  TS 23.247 §7 defines the MBS session state machine:\n"
            "  created -> activated -> deactivated. Once deactivated, a\n"
            "  session cannot be re-activated — operators must create a\n"
            "  fresh session with a new TMGI. This TC pins the happy path\n"
            "  plus the invalid deactivated->activated transition.\n"
            "\n"
            "Procedure (TS 23.247 §7)\n"
            "  1. _create_session(tmgi='TMGI-001-<ts>') with multicast,\n"
            "     5QI=9. Assert response.status == 'created'.\n"
            "  2. POST /api/mbs/sessions/{sid}/activate; assert HTTP 200\n"
            "     and response.session.status == 'activated'.\n"
            "  3. POST /api/mbs/sessions/{sid}/deactivate; assert HTTP 200\n"
            "     and response.session.status == 'deactivated'.\n"
            "  4. POST /api/mbs/sessions/{sid}/activate AGAIN; assert\n"
            "     HTTP 400 (transition forbidden by state machine).\n"
            "  5. finally: DELETE the session for cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — TMGI is timestamp-derived for uniqueness).\n"
            "\n"
            "Pass criteria\n"
            "  All three legal transitions succeed AND the second activate\n"
            "  on a deactivated session is rejected with HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id.\n"
            "\n"
            "Known constraints\n"
            "  TMGI is UNIQUE in the schema — TC uses int(time.time()) to\n"
            "  avoid collisions when the suite re-runs."
        ),
    )

    def run(self):
        # tmgi UNIQUE per schema; use a timestamp-derived value.
        tmgi = f"TMGI-001-{int(time.time())}"
        sid, sess, err = _create_session(tmgi)
        try:
            if err:
                self.fail_test(err)
                return self.result
            if sess.get("status") != "created":
                self.fail_test(f"create status: {sess.get('status')}")
                return self.result

            r, s = _mbs_api(f"/api/mbs/sessions/{sid}/activate", "POST")
            if s != 200 or r.get("session", {}).get("status") != "activated":
                self.fail_test(f"activate: {s} {r}")
                return self.result

            r2, s2 = _mbs_api(f"/api/mbs/sessions/{sid}/deactivate", "POST")
            if s2 != 200 or r2.get("session", {}).get("status") != "deactivated":
                self.fail_test(f"deactivate: {s2} {r2}")
                return self.result

            # Re-activate must be rejected (deactivated -> activated not allowed).
            r3, s3 = _mbs_api(f"/api/mbs/sessions/{sid}/activate", "POST")
            if s3 != 400:
                self.fail_test(f"re-activate did not 400: {s3} {r3}")
                return self.result

            self.pass_test(session_id=sid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_session(sid)
        return self.result


class MbsValidation(TestCase):
    """TC-MBS-002: invalid session_type / missing tmgi → 400."""
    SPEC = TestSpec(
        tc_id="TC-MBS-002",
        title="MBS session input validation rejects bad payloads",
        spec="TS 23.247 §7",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  MBS session_type is constrained to {multicast, broadcast}\n"
            "  (TS 23.247 §4.1) and TMGI is mandatory + unique. This TC\n"
            "  pins the input-validation contract on POST /sessions so bad\n"
            "  payloads never reach the state machine.\n"
            "\n"
            "Procedure (TS 23.247 §7)\n"
            "  1. POST /api/mbs/sessions with body\n"
            "     {tmgi='TMGI-X', session_type='BAD'}.\n"
            "  2. Assert HTTP 400 — invalid session_type must be rejected.\n"
            "  3. POST /api/mbs/sessions with body\n"
            "     {tmgi='', session_type='multicast'}.\n"
            "  4. Assert HTTP 400 — empty TMGI must be rejected.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payloads are hard-coded negative cases).\n"
            "\n"
            "Pass criteria\n"
            "  Both negative POSTs return HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Only the SA-Core input validator is exercised; downstream\n"
            "  MBSF/MBSU rejection paths are not driven here."
        ),
    )

    def run(self):
        try:
            r, s = _mbs_api("/api/mbs/sessions", "POST",
                             {"tmgi": "TMGI-X", "session_type": "BAD"})
            if s != 400:
                self.fail_test(f"bad session_type did not 400: {s} {r}")
                return self.result

            r2, s2 = _mbs_api("/api/mbs/sessions", "POST",
                               {"tmgi": "", "session_type": "multicast"})
            if s2 != 400:
                self.fail_test(f"empty tmgi did not 400: {s2} {r2}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class MbsMembers(TestCase):
    """TC-MBS-003: join + list + leave members on a multicast session."""
    SPEC = TestSpec(
        tc_id="TC-MBS-003",
        title="MBS multicast session join/list/leave members",
        spec="TS 23.247 §7",
        domain=Domain.VAS,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Multicast MBS sessions track per-UE membership so the SMF\n"
            "  can authorise N4 rules per joined IMSI (TS 23.247 §7).\n"
            "  Operations must be idempotent (UNIQUE(session_id, imsi)\n"
            "  with INSERT OR IGNORE) so re-join doesn't fault. Leave must\n"
            "  stamp left_at but keep the audit row.\n"
            "\n"
            "Procedure (TS 23.247 §7)\n"
            "  1. _create_session(tmgi='TMGI-MEM-<ts>'). Get sid.\n"
            "  2. For each of baseline IMSI #0 and #1: POST\n"
            "     /api/mbs/sessions/{sid}/join with the imsi; assert 200.\n"
            "  3. GET /api/mbs/sessions/{sid}/members; assert len == 2.\n"
            "  4. Re-POST /join for IMSI #0; assert HTTP 200 (idempotent).\n"
            "  5. POST /api/mbs/sessions/{sid}/leave for IMSI #0.\n"
            "  6. GET /members; assert at least one row carries non-null\n"
            "     left_at timestamp.\n"
            "  7. finally: DELETE the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — baseline IMSIs from src.baseline).\n"
            "\n"
            "Pass criteria\n"
            "  Two joins create two members AND re-join is idempotent (200)\n"
            "  AND leave stamps left_at on at least one row.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  active_members.\n"
            "\n"
            "Known constraints\n"
            "  Audit row is kept post-leave; active count is derived by\n"
            "  filtering left_at IS NULL."
        ),
    )

    def run(self):
        tmgi = f"TMGI-MEM-{int(time.time())}"
        sid, _, err = _create_session(tmgi)
        try:
            if err:
                self.fail_test(err)
                return self.result

            for imsi in (baseline.imsi("embb-bulk", 0), baseline.imsi("embb-bulk", 1)):
                r, s = _mbs_api(f"/api/mbs/sessions/{sid}/join", "POST",
                                 {"imsi": imsi})
                if s != 200:
                    self.fail_test(f"join {imsi}: {s} {r}")
                    return self.result

            ml, _ = _mbs_api(f"/api/mbs/sessions/{sid}/members")
            members = ml.get("members", [])
            if len(members) != 2:
                self.fail_test(f"len(members) != 2: {members}")
                return self.result

            # Re-join is idempotent (UNIQUE(session_id, imsi) + INSERT OR IGNORE).
            r2, s2 = _mbs_api(f"/api/mbs/sessions/{sid}/join", "POST",
                               {"imsi": baseline.imsi("embb-bulk", 0)})
            if s2 != 200:
                self.fail_test(f"idempotent re-join failed: {s2} {r2}")
                return self.result

            # Leave one — left_at gets stamped.
            _mbs_api(f"/api/mbs/sessions/{sid}/leave", "POST",
                      {"imsi": baseline.imsi("embb-bulk", 0)})
            ml2, _ = _mbs_api(f"/api/mbs/sessions/{sid}/members")
            left = [m for m in ml2.get("members", []) if m.get("left_at")]
            if not left:
                self.fail_test("no left_at after leave", body=ml2)
                return self.result

            self.pass_test(active_members=2 - len(left))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_session(sid)
        return self.result


class MbsAreas(TestCase):
    """TC-MBS-004: service-area CRUD (TS 23.247 §7.2)."""
    SPEC = TestSpec(
        tc_id="TC-MBS-004",
        title="MBS service-area CRUD (TAI scoping)",
        spec="TS 23.247 §7.2",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  MBS service areas scope multicast/broadcast delivery to a\n"
            "  TAI list (TS 23.247 §7.2). The operator surface must support\n"
            "  create + list + delete and reject empty tracking_areas — an\n"
            "  empty list would imply network-wide broadcast which the MBSF\n"
            "  does not permit on this path.\n"
            "\n"
            "Procedure (TS 23.247 §7.2)\n"
            "  1. POST /api/mbs/areas with name='area-tc-<ts>',\n"
            "     tracking_areas='00101:0001,00101:0002', description.\n"
            "  2. Assert HTTP 200/201; capture response.area.id (aid).\n"
            "  3. GET /api/mbs/areas; assert aid is present in areas list.\n"
            "  4. POST /api/mbs/areas with name='x', tracking_areas=''.\n"
            "  5. Assert HTTP 400 — empty TAI list must be rejected.\n"
            "  6. DELETE /api/mbs/areas/{aid} for cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — area name is timestamp-derived).\n"
            "\n"
            "Pass criteria\n"
            "  Create succeeds, the area is listed AND empty-TAI POST is\n"
            "  rejected with HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  area_id.\n"
            "\n"
            "Known constraints\n"
            "  TAI list format is 'plmn:tac,plmn:tac,...'; the validator\n"
            "  string-checks but doesn't decode individual TAIs."
        ),
    )

    def run(self):
        name = f"area-tc-{int(time.time())}"
        try:
            r, s = _mbs_api("/api/mbs/areas", "POST", {
                "name": name,
                "tracking_areas": "00101:0001,00101:0002",
                "description": "tc-mbs-area",
            })
            if s not in (200, 201):
                self.fail_test(f"create area failed: {s} {r}")
                return self.result
            area = r.get("area", {})
            aid = area.get("id")

            ls, _ = _mbs_api("/api/mbs/areas")
            if not any(a.get("id") == aid for a in ls.get("areas", [])):
                self.fail_test("area missing from list")
                return self.result

            # Empty TAI list must 400.
            br, bs = _mbs_api("/api/mbs/areas", "POST",
                               {"name": "x", "tracking_areas": ""})
            if bs != 400:
                self.fail_test(f"empty TAIs did not 400: {bs} {br}")
                return self.result

            _mbs_api(f"/api/mbs/areas/{aid}", "DELETE")
            self.pass_test(area_id=aid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class MbsContentDelivery(TestCase):
    """TC-MBS-005: send content → audit log records delivery."""
    SPEC = TestSpec(
        tc_id="TC-MBS-005",
        title="MBS content send is recorded in the delivery audit log",
        spec="TS 23.247 §7",
        domain=Domain.VAS,
        nfs=(NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  MBS content delivery is only legal on an activated session\n"
            "  with at least one joined member (TS 23.247 §7). Pre-activation\n"
            "  /send must be rejected; once activated and joined, /send must\n"
            "  succeed, the delivery row must record recipients_count from\n"
            "  the member list, and the action must surface in /content-log.\n"
            "\n"
            "Procedure (TS 23.247 §7)\n"
            "  1. _create_session(tmgi='TMGI-CD-<ts>'). Get sid.\n"
            "  2. POST /api/mbs/sessions/{sid}/send with content_type and\n"
            "     content_data, BEFORE activation. Assert HTTP 400.\n"
            "  3. POST /sessions/{sid}/activate (best-effort).\n"
            "  4. POST /sessions/{sid}/join with baseline IMSI #98.\n"
            "  5. POST /sessions/{sid}/send with content_type='video/mp4'\n"
            "     and content_data. Assert HTTP 200 and\n"
            "     response.delivery.status == 'delivered'.\n"
            "  6. Assert response.delivery.recipients_count == 1.\n"
            "  7. GET /api/mbs/content-log?limit=10; assert at least one\n"
            "     row has session_id == sid.\n"
            "  8. finally: DELETE the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed content payload).\n"
            "\n"
            "Pass criteria\n"
            "  Pre-activation send 400 AND post-activation send returns\n"
            "  delivered with recipients_count == 1 AND row in content-log.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id.\n"
            "\n"
            "Known constraints\n"
            "  content_data is base64-encoded opaque bytes — payload\n"
            "  routing to the MB-UPF is not exercised here."
        ),
    )

    def run(self):
        tmgi = f"TMGI-CD-{int(time.time())}"
        sid, _, err = _create_session(tmgi)
        try:
            if err:
                self.fail_test(err)
                return self.result

            # Send before activation must reject (activated-only).
            br, bs = _mbs_api(f"/api/mbs/sessions/{sid}/send", "POST",
                               {"content_type": "video/mp4",
                                "content_data": "abcdef"})
            if bs != 400:
                self.fail_test(f"send before activate did not 400: {bs} {br}")
                return self.result

            _mbs_api(f"/api/mbs/sessions/{sid}/activate", "POST")
            _mbs_api(f"/api/mbs/sessions/{sid}/join", "POST",
                     {"imsi": baseline.imsi("embb-bulk", 98)})
            r, s = _mbs_api(f"/api/mbs/sessions/{sid}/send", "POST",
                             {"content_type": "video/mp4",
                              "content_data": "abcdef"})
            if s != 200 or r.get("delivery", {}).get("status") != "delivered":
                self.fail_test(f"send failed: {s} {r}")
                return self.result
            if r.get("delivery", {}).get("recipients_count") != 1:
                self.fail_test(f"recipients_count != 1: {r}")
                return self.result

            cl, _ = _mbs_api("/api/mbs/content-log?limit=10")
            if not any(row.get("session_id") == sid
                       for row in cl.get("content_log", [])):
                self.fail_test("delivery missing from content-log",
                               sample=cl.get("content_log", [])[:3])
                return self.result

            self.pass_test(session_id=sid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_session(sid)
        return self.result


class MbsStats(TestCase):
    """TC-MBS-006: stats reports the GUI panel's expected counters."""
    SPEC = TestSpec(
        tc_id="TC-MBS-006",
        title="MBS stats endpoint exposes GUI counters",
        spec="TS 23.247 §7",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("smoke",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the GUI / observability schema: the MBS panel reads six\n"
            "  counters and any rename or drop breaks the operator view.\n"
            "  This TC asserts every counter key is present on /stats.\n"
            "  TS 23.247 §7.\n"
            "\n"
            "Procedure (TS 23.247 §7)\n"
            "  1. GET /api/mbs/stats with no query params.\n"
            "  2. Assert HTTP 200 AND response.ok is truthy.\n"
            "  3. Extract response.stats; for each of total_sessions,\n"
            "     active_sessions, multicast_sessions, broadcast_sessions,\n"
            "     active_members, delivered_content — assert the key is\n"
            "     present.\n"
            "  4. fail_test on first missing key.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — pure GET).\n"
            "\n"
            "Pass criteria\n"
            "  All six required counter keys present under response.stats.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  stats — full stats dict, passed through to result.details.\n"
            "\n"
            "Known constraints\n"
            "  Values are not asserted (they depend on TC ordering); only\n"
            "  schema-shape conformance is checked."
        ),
    )

    def run(self):
        try:
            r, s = _mbs_api("/api/mbs/stats")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"GET stats failed: {s} {r}")
                return self.result
            stats = r.get("stats", {})
            for k in ("total_sessions", "active_sessions",
                      "multicast_sessions", "broadcast_sessions",
                      "active_members", "delivered_content"):
                if k not in stats:
                    self.fail_test(f"missing stats key '{k}'", body=stats)
                    return self.result
            self.pass_test(stats=stats)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_MBS_TCS = [
    MbsSessionLifecycle,
    MbsValidation,
    MbsMembers,
    MbsAreas,
    MbsContentDelivery,
    MbsStats,
]

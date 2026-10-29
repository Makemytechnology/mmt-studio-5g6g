# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Mission Critical Communications (MCX).

TS 23.280 — Common functional architecture for MC services
TS 23.379 — MCPTT (Mission Critical Push-To-Talk) architecture
TS 23.281 — MCVideo architecture
TS 23.282 — MCData architecture
TS 24.379 — MCPTT call control (Stage 3)
TS 24.380 — MCPTT floor control (Stage 3)

These TCs are the Python siblings of robot/suites/voice_media/21_mcx.robot.
The robot side only `Log`s the tc_id; the Python side does a minimal
smoke check against the SA Core MCX REST surface (/api/mcx/*) when an
obvious endpoint exists, and falls back to a "Python implementation
pending" fail otherwise — so the GUI Domain pivot lists every catalog
entry as either a real check or a clearly-flagged gap, never a vacuous
PASS.

Endpoints exercised live in mmt_studio_core/services/mcx/api/mcx_routes.py.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_mcx")


def _mcx_api(path, method="GET", body=None):
    """Call SA Core MCX REST API. Returns (json_body_or_str, status)."""
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read().decode()
            try:
                return json.loads(raw), resp.status
            except Exception:
                return raw, resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


def _pending(tc, tc_id, reason=""):
    """Mark a TC as a pending-Python gap with a clear pointer to the
    Robot suite that owns the spec'd procedure. Surfaces as FAIL so
    the operator sees a real gap rather than a vacuous PASS."""
    msg = (
        f"Python implementation pending — see "
        f"robot/suites/voice_media/21_mcx.robot::{tc_id} for the "
        f"specified procedure."
    )
    if reason:
        msg += f" ({reason})"
    tc.fail_test(msg)


def _ensure_group(name, max_members=50, priority=5):
    """Best-effort: return the id of an MCX group with the given name,
    creating it if necessary. Returns None if group creation fails."""
    res, status = _mcx_api("/api/mcx/groups", "POST", {
        "name": name,
        "group_type": "normal",
        "max_members": max_members,
        "priority": priority,
    })
    if status in (200, 201) and isinstance(res, dict):
        return res.get("id") or res.get("group_id")
    # Name may already exist — try to find it via list.
    listing, lstatus = _mcx_api("/api/mcx/groups")
    if lstatus == 200 and isinstance(listing, dict):
        for g in listing.get("items", []):
            if g.get("name") == name:
                return g.get("id") or g.get("group_id")
    return None


def _delete_group(gid):
    if gid is None:
        return
    try:
        _mcx_api(f"/api/mcx/groups/{gid}", "DELETE")
    except Exception:
        pass


# ─── MCPTT Group Call Setup / Teardown ────────────────────────────────


class McxMcpttGroupCallSetup(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-001",
        title="MCPTT pre-arranged group call setup",
        spec="TS 23.379 §10.6.2.3.1.1.2",
        domain=Domain.MCX,
        nfs=(NF.AMF, NF.SMF, NF.UPF, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcptt", "group-call", "setup"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Pins the pre-arranged MCPTT group-call setup primitive on\n"
            "  the SA Core MCX router. TS 23.379 §10.6.2.3.1.1.2 makes the\n"
            "  MCPTT server the call originator; here we drive it via REST.\n"
            "\n"
            "Procedure (TS 23.379 §10.6.2.3.1.1.2 + TS 24.379)\n"
            "  1. require_ue() → grab imsi.\n"
            "  2. POST /api/mcx/users {imsi} → fail_test if not 200/201 or\n"
            "     no mcptt_id is returned.\n"
            "  3. _ensure_group('tc-mcx-001-grp') — fail_test if None.\n"
            "  4. POST /api/mcx/groups/{gid}/members with mcptt_id+role.\n"
            "  5. POST /api/mcx/calls/group with originator=mcptt_id,\n"
            "     group_id=gid, emergency=False.\n"
            "  6. fail_test on non-(200, 201) or non-dict response.\n"
            "  7. Capture call_id (call_id or id).\n"
            "  8. finally: POST /api/mcx/calls/{id}/end + _delete_group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Group-call POST returns 200/201 dict with the call envelope.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  mcptt_id, group_id, call (call envelope).\n"
            "\n"
            "Known constraints\n"
            "  No MBMS / floor signalling on the wire — the Robot suite\n"
            "  21_mcx.robot owns the SIP/MBMS path end-to-end.\n"
            "  No MBMS / SIP signalling on the wire — the Robot suite at\n"
            "  robot/suites/voice_media/21_mcx.robot owns the full SIP/MBMS\n"
            "  path end-to-end."
        ),
    )

    def run(self):
        gid = None
        call_id = None
        try:
            ue = self.require_ue()
            imsi = ue.imsi
            # Create MCX user for originator.
            user, ustatus = _mcx_api("/api/mcx/users", "POST", {"imsi": imsi})
            if ustatus not in (200, 201):
                self.fail_test(f"user create failed: {ustatus} {user}")
                return self.result
            mcptt_id = user.get("mcptt_id") if isinstance(user, dict) else None
            if not mcptt_id:
                self.fail_test(f"user create returned no mcptt_id: {user}")
                return self.result

            gid = _ensure_group("tc-mcx-001-grp")
            if not gid:
                self.fail_test("could not create / locate MCX group")
                return self.result

            # Add originator to group.
            _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                     {"mcptt_id": mcptt_id, "role": "member"})

            call, status = _mcx_api("/api/mcx/calls/group", "POST", {
                "originator": mcptt_id, "group_id": gid, "emergency": False,
            })
            if status not in (200, 201) or not isinstance(call, dict):
                self.fail_test(f"group call init failed: {status} {call}")
                return self.result
            call_id = call.get("call_id") or call.get("id")
            self.pass_test(mcptt_id=mcptt_id, group_id=gid, call=call)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if call_id:
                _mcx_api(f"/api/mcx/calls/{call_id}/end", "POST")
            _delete_group(gid)
        return self.result


class McxMcpttEmergencyGroupCallSetup(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-002",
        title="MCPTT emergency group call setup",
        spec="TS 23.379 §10.6.2.6.1",
        domain=Domain.MCX,
        nfs=(NF.AMF, NF.SMF, NF.UPF, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcptt", "group-call", "emergency"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Validates the emergency-priority annotation on MCPTT group\n"
            "  call setup. TS 23.379 §10.6.2.6.1 calls out emergency group\n"
            "  call setup as a distinct flow with elevated priority and\n"
            "  pre-emption rights; the response must surface that.\n"
            "\n"
            "Procedure (TS 23.379 §10.6.2.6.1 + TS 24.379)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/mcx/users {imsi} → fail_test on non-2xx.\n"
            "  3. _ensure_group('tc-mcx-002-grp'); add mcptt_id member.\n"
            "  4. POST /api/mcx/calls/group with emergency=True.\n"
            "  5. fail_test on non-(200, 201) or non-dict response.\n"
            "  6. Heuristic emergency_flag check: any of is_emergency,\n"
            "     emergency=True, call_type=='emergency', or priority<=2.\n"
            "  7. fail_test if no emergency indicator surfaces.\n"
            "  8. finally: end call + delete group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Group-call POST 200/201 AND the response carries any one\n"
            "  recognised emergency indicator.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  call (full envelope).\n"
            "\n"
            "Known constraints\n"
            "  Heuristic indicator detection — accepts any of four shapes\n"
            "  to stay tolerant of router-side schema choices."
        ),
    )

    def run(self):
        gid = None
        call_id = None
        try:
            ue = self.require_ue()
            imsi = ue.imsi
            user, ustatus = _mcx_api("/api/mcx/users", "POST", {"imsi": imsi})
            if ustatus not in (200, 201):
                self.fail_test(f"user create failed: {ustatus} {user}")
                return self.result
            mcptt_id = user.get("mcptt_id")

            gid = _ensure_group("tc-mcx-002-grp")
            if not gid:
                self.fail_test("could not create / locate MCX group")
                return self.result
            _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                     {"mcptt_id": mcptt_id, "role": "member"})

            call, status = _mcx_api("/api/mcx/calls/group", "POST", {
                "originator": mcptt_id, "group_id": gid, "emergency": True,
            })
            if status not in (200, 201) or not isinstance(call, dict):
                self.fail_test(f"emergency call init failed: {status} {call}")
                return self.result
            call_id = call.get("call_id") or call.get("id")
            # Heuristic check on emergency / priority flag if exposed.
            emergency_flag = (
                call.get("is_emergency") or call.get("emergency")
                or call.get("call_type") == "emergency"
                or (isinstance(call.get("priority"), int) and call["priority"] <= 2)
            )
            if not emergency_flag:
                self.fail_test(
                    "emergency call did not carry emergency indicator",
                    call=call,
                )
                return self.result
            self.pass_test(call=call)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if call_id:
                _mcx_api(f"/api/mcx/calls/{call_id}/end", "POST")
            _delete_group(gid)
        return self.result


class McxMcpttGroupCallTeardown(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-003",
        title="MCPTT group call teardown",
        spec="TS 23.379 §10.6.2.3.1.1.3",
        domain=Domain.MCX,
        nfs=(NF.AMF, NF.SMF, NF.UPF, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcptt", "group-call", "teardown"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Pins the MCPTT group-call teardown primitive. TS 23.379\n"
            "  §10.6.2.3.1.1.3 'Release pre-arranged group call' must be\n"
            "  callable from the MCPTT server and ack'd by the router.\n"
            "\n"
            "Procedure (TS 23.379 §10.6.2.3.1.1.3 + TS 24.379)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/mcx/users → mcptt_id, fail_test if missing.\n"
            "  3. _ensure_group('tc-mcx-003-grp') + add member.\n"
            "  4. POST /api/mcx/calls/group (emergency=False); pull\n"
            "     call_id, fail_test if missing.\n"
            "  5. POST /api/mcx/calls/{call_id}/end.\n"
            "  6. fail_test if end status not in (200, 201, 204).\n"
            "  7. finally: _delete_group(gid).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  End call POST returns 200/201/204.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  call_id, ended (the response envelope).\n"
            "\n"
            "Known constraints\n"
            "  No SIP BYE on the wire — REST end primitive only.\n"
            "  No SIP BYE on the wire — REST end primitive only. RTP teardown\n"
            "  is not asserted. Group row is reaped on finally so the GMS\n"
            "  stays tidy.\n"
            "  Pure REST teardown contract — operator must run the Robot\n"
            "  suite for full SIP path validation."
        ),
    )

    def run(self):
        gid = None
        call_id = None
        try:
            ue = self.require_ue()
            imsi = ue.imsi
            user, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": imsi})
            mcptt_id = user.get("mcptt_id") if isinstance(user, dict) else None
            if not mcptt_id:
                self.fail_test(f"user create returned no mcptt_id: {user}")
                return self.result

            gid = _ensure_group("tc-mcx-003-grp")
            if not gid:
                self.fail_test("could not create / locate MCX group")
                return self.result
            _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                     {"mcptt_id": mcptt_id, "role": "member"})

            call, _ = _mcx_api("/api/mcx/calls/group", "POST", {
                "originator": mcptt_id, "group_id": gid, "emergency": False,
            })
            call_id = call.get("call_id") or call.get("id") if isinstance(call, dict) else None
            if not call_id:
                self.fail_test(f"group call init returned no id: {call}")
                return self.result

            ended, status = _mcx_api(f"/api/mcx/calls/{call_id}/end", "POST")
            if status not in (200, 201, 204):
                self.fail_test(f"end call failed: {status} {ended}")
                return self.result
            self.pass_test(call_id=call_id, ended=ended)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group(gid)
        return self.result


# ─── MCPTT Floor Control (TS 24.380) ──────────────────────────────────


class McxMcpttFloorRequestAndGrant(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-010",
        title="MCPTT floor request and grant",
        spec="TS 24.380 §6.2.4.3",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcptt", "floor-control", "request", "grant"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Validates the MCPTT floor request primitive: with a group\n"
            "  call active, the participant must be able to request the\n"
            "  floor and receive a grant decision. TS 24.380 §6.2.4.3\n"
            "  drives the 'no permission' → 'has permission' FSM step.\n"
            "\n"
            "Procedure (TS 24.380 §6.2.4.3 + TS 23.379 §10.7)\n"
            "  1. require_ue(); POST /api/mcx/users → mcptt_id.\n"
            "  2. _ensure_group('tc-mcx-010-grp'); add member.\n"
            "  3. POST /api/mcx/calls/group; pull call_id (fail if missing).\n"
            "  4. POST /api/mcx/floor/{call_id}/request with mcptt_id and\n"
            "     priority=5.\n"
            "  5. fail_test if floor POST not in (200, 201).\n"
            "  6. finally: end call + delete group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — priority pinned at 5 (normal).\n"
            "\n"
            "Pass criteria\n"
            "  Floor request returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  call_id, floor_result (grant envelope).\n"
            "\n"
            "Known constraints\n"
            "  No RTCP / MBCP messaging — REST primitive only. Grant body\n"
            "  shape is not asserted.\n"
            "  No RTCP / MBCP messaging — REST primitive only. Grant body\n"
            "  shape is not asserted, only the HTTP success of the request.\n"
            "  Floor revoke / queue ordering are covered by TC-MCX-012\n"
            "  and TC-MCX-051 respectively (both pending)."
        ),
    )

    def run(self):
        gid = None
        call_id = None
        try:
            ue = self.require_ue()
            user, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            mcptt_id = user.get("mcptt_id") if isinstance(user, dict) else None
            if not mcptt_id:
                self.fail_test(f"user create returned no mcptt_id: {user}")
                return self.result

            gid = _ensure_group("tc-mcx-010-grp")
            if not gid:
                self.fail_test("could not create / locate MCX group")
                return self.result
            _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                     {"mcptt_id": mcptt_id, "role": "member"})

            call, _ = _mcx_api("/api/mcx/calls/group", "POST", {
                "originator": mcptt_id, "group_id": gid, "emergency": False,
            })
            call_id = call.get("call_id") or call.get("id") if isinstance(call, dict) else None
            if not call_id:
                self.fail_test(f"group call init returned no id: {call}")
                return self.result

            res, status = _mcx_api(f"/api/mcx/floor/{call_id}/request",
                                   "POST", {"mcptt_id": mcptt_id, "priority": 5})
            if status not in (200, 201):
                self.fail_test(f"floor request failed: {status} {res}")
                return self.result
            self.pass_test(call_id=call_id, floor_result=res)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if call_id:
                _mcx_api(f"/api/mcx/calls/{call_id}/end", "POST")
            _delete_group(gid)
        return self.result


class McxMcpttFloorRelease(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-011",
        title="MCPTT floor release returns floor to idle",
        spec="TS 24.380 §6.2.4.5",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcptt", "floor-control", "release"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Validates the floor-release primitive — the inverse of\n"
            "  TC-MCX-010. TS 24.380 §6.2.4.5 walks the 'has permission'\n"
            "  → 'has no permission' transition the controller follows\n"
            "  when the talker pushes-to-release.\n"
            "\n"
            "Procedure (TS 24.380 §6.2.4.5)\n"
            "  1. require_ue(); POST /api/mcx/users → mcptt_id.\n"
            "  2. _ensure_group('tc-mcx-011-grp'); add member.\n"
            "  3. POST /api/mcx/calls/group; capture call_id.\n"
            "  4. POST /api/mcx/floor/{call_id}/request to take the floor.\n"
            "  5. POST /api/mcx/floor/{call_id}/release with mcptt_id.\n"
            "  6. fail_test if release status not in (200, 201, 204).\n"
            "  7. finally: end call + delete group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Release POST returns 200/201/204.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  call_id, release_result (response envelope).\n"
            "\n"
            "Known constraints\n"
            "  Floor-state readback isn't checked — only the release\n"
            "  endpoint ack is gated.\n"
            "  Floor-state readback isn't checked — only the release endpoint\n"
            "  ack is gated. RTP/MBCP teardown is the Robot suite's job.\n"
            "  Floor revoke ordering is exercised separately (TC-MCX-012\n"
            "  and TC-MCX-051, both pending stubs today)."
        ),
    )

    def run(self):
        gid = None
        call_id = None
        try:
            ue = self.require_ue()
            user, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            mcptt_id = user.get("mcptt_id") if isinstance(user, dict) else None
            if not mcptt_id:
                self.fail_test(f"user create returned no mcptt_id: {user}")
                return self.result

            gid = _ensure_group("tc-mcx-011-grp")
            if not gid:
                self.fail_test("could not create / locate MCX group")
                return self.result
            _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                     {"mcptt_id": mcptt_id, "role": "member"})

            call, _ = _mcx_api("/api/mcx/calls/group", "POST", {
                "originator": mcptt_id, "group_id": gid, "emergency": False,
            })
            call_id = call.get("call_id") or call.get("id") if isinstance(call, dict) else None
            if not call_id:
                self.fail_test(f"group call init returned no id: {call}")
                return self.result

            _mcx_api(f"/api/mcx/floor/{call_id}/request", "POST",
                     {"mcptt_id": mcptt_id, "priority": 5})
            res, status = _mcx_api(f"/api/mcx/floor/{call_id}/release",
                                   "POST", {"mcptt_id": mcptt_id})
            if status not in (200, 201, 204):
                self.fail_test(f"floor release failed: {status} {res}")
                return self.result
            self.pass_test(call_id=call_id, release_result=res)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if call_id:
                _mcx_api(f"/api/mcx/calls/{call_id}/end", "POST")
            _delete_group(gid)
        return self.result


class McxMcpttFloorPreemption(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-012",
        title="MCPTT floor preemption by higher priority user",
        spec="TS 24.380 §6.2.4.5.4",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcptt", "floor-control",
              "preemption", "emergency"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Validates the floor revoke (pre-emption) path: when a higher\n"
            "  priority (emergency) user requests the floor, the controller\n"
            "  must revoke the incumbent and grant to the new requester.\n"
            "  TS 24.380 §6.2.4.5.4 'Receive Floor Revoke' is the gate.\n"
            "\n"
            "Procedure (TS 24.380 §6.2.4.5.4)\n"
            "  1. (spec'd) Two users A (priority 10) + B (priority 1,\n"
            "     emergency) in same group.\n"
            "  2. A takes the floor.\n"
            "  3. B requests the floor with elevated priority.\n"
            "  4. Controller emits Floor-Revoke to A and Floor-Granted to B.\n"
            "  5. Verify A is in 'pending' state, B in 'has permission'.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/voice_media/\n"
            "  21_mcx.robot::TC-MCX-012 with reason 'needs two MCX users\n"
            "  with distinct priorities'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  Python tester process cannot synthesise two distinct MCX\n"
            "  users with priorities; the Robot suite owns this case."
        ),
    )

    def run(self):
        _pending(self, "TC-MCX-012",
                 "needs two MCX users with distinct priorities")
        return self.result


# ─── MCVideo (TS 23.281) ─────────────────────────────────────────────


class McxMcvideoGroupCallSetup(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-020",
        title="MCVideo group call setup",
        spec="TS 23.281 §7.1.2.3.1.1",
        domain=Domain.MCX,
        nfs=(NF.AMF, NF.SMF, NF.UPF, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcvideo", "group-call", "setup"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Pins the pre-arranged MCVideo group-call setup procedure.\n"
            "  TS 23.281 §7.1.2.3.1.1 mirrors the MCPTT procedure but with\n"
            "  the addition of a video stream. The SA Core MCX router\n"
            "  exposes a single /calls/group endpoint distinguished by\n"
            "  the 'service' attribute.\n"
            "\n"
            "Procedure (TS 23.281 §7.1.2.3.1.1 + TS 23.379 §10.6)\n"
            "  1. require_ue(); POST /api/mcx/users → mcptt_id.\n"
            "  2. _ensure_group('tc-mcx-020-grp'); add member.\n"
            "  3. POST /api/mcx/calls/group with service='mcvideo' and\n"
            "     emergency=False.\n"
            "  4. fail_test on non-(200, 201) or non-dict.\n"
            "  5. Capture call_id.\n"
            "  6. finally: end call + delete group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  MCVideo group call POST returns 200/201 dict.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  call (full envelope).\n"
            "\n"
            "Known constraints\n"
            "  No video media path is established — Robot suite owns the\n"
            "  RTP/RTCP / MBMS verification.\n"
            "  No video media path is established — Robot suite owns the\n"
            "  RTP/RTCP / MBMS verification. Call row is reaped on finally."
        ),
    )

    def run(self):
        gid = None
        call_id = None
        try:
            ue = self.require_ue()
            user, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            mcptt_id = user.get("mcptt_id") if isinstance(user, dict) else None
            if not mcptt_id:
                self.fail_test(f"user create returned no mcptt_id: {user}")
                return self.result

            gid = _ensure_group("tc-mcx-020-grp")
            if not gid:
                self.fail_test("could not create / locate MCX group")
                return self.result
            _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                     {"mcptt_id": mcptt_id, "role": "member"})

            call, status = _mcx_api("/api/mcx/calls/group", "POST", {
                "originator": mcptt_id, "group_id": gid,
                "service": "mcvideo", "emergency": False,
            })
            if status not in (200, 201) or not isinstance(call, dict):
                self.fail_test(f"mcvideo group call init failed: {status} {call}")
                return self.result
            call_id = call.get("call_id") or call.get("id")
            self.pass_test(call=call)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if call_id:
                _mcx_api(f"/api/mcx/calls/{call_id}/end", "POST")
            _delete_group(gid)
        return self.result


class McxMcvideoTransmissionControl(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-021",
        title="MCVideo transmission control grant and revoke",
        spec="TS 23.281 §7.7.1",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MINOR,
        tags=("conformance", "mcx", "mcvideo", "transmission-control"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Drives the MCVideo transmission-control FSM, mirror of the\n"
            "  MCPTT floor controller but for video uplink. TS 23.281 §7.7.1\n"
            "  models this as a request → granted → released cycle.\n"
            "\n"
            "Procedure (TS 23.281 §7.7.1)\n"
            "  1. (spec'd) Establish MCVideo group call.\n"
            "  2. Request transmission via dedicated endpoint.\n"
            "  3. Verify grant, then release.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/voice_media/\n"
            "  21_mcx.robot::TC-MCX-021 with reason 'no MCVideo\n"
            "  transmission control endpoint yet'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  SA Core does not yet expose a separate /api/mcx/video\n"
            "  transmission control surface; flagged as a gap.\n"
            "  Pending stub — _pending() always fails. Operator must wire\n"
            "  an MCVideo transmission control endpoint in the SA Core MCX\n"
            "  router for this to graduate to a real check.\n"
            "  Once the SA Core exposes /api/mcx/video/* this TC can\n"
            "  be upgraded to drive the full request→grant→release cycle."
        ),
    )

    def run(self):
        _pending(self, "TC-MCX-021",
                 "no MCVideo transmission control endpoint yet")
        return self.result


# ─── MCData SDS (TS 23.282) ──────────────────────────────────────────


class McxMcdataSdsPrivateMessage(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-030",
        title="MCData SDS private message send",
        spec="TS 23.282 §7.4.2.2",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcdata", "sds", "private", "message"),
        setup=Setup.BASELINE,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  Validates the MCData SDS one-to-one private-message send.\n"
            "  TS 23.282 §7.4.2.2 places standalone SDS over the signalling\n"
            "  control plane (no MBMS), addressed between mcptt_ids.\n"
            "\n"
            "Procedure (TS 23.282 §7.4.2.2 + TS 24.282)\n"
            "  1. require_ue() → sender imsi.\n"
            "  2. POST /api/mcx/users {imsi=sender} → sender_mcptt_id;\n"
            "     fail_test if missing.\n"
            "  3. If ue_pool has >=2 UEs, POST users for ue_pool[1] →\n"
            "     recipient_mcptt_id; else recipient = sender (self-send).\n"
            "  4. POST /api/mcx/messages with sender, recipient,\n"
            "     content='TC-MCX-030 hello'.\n"
            "  5. fail_test if status not in (200, 201).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Message POST returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sender, recipient, message (the created envelope).\n"
            "\n"
            "Known constraints\n"
            "  When the UE pool has only one UE the test devolves to a\n"
            "  self-send, which still exercises the SDS POST route but\n"
            "  not true two-party addressing.\n"
            "  When the UE pool has only one UE the test devolves to a self-\n"
            "  send, which still exercises the SDS POST route but not true\n"
            "  two-party addressing. Disposition tracking is TC-MCX-032."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            sender_user, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            sender = sender_user.get("mcptt_id") if isinstance(sender_user, dict) else None
            if not sender:
                self.fail_test(f"sender user create no mcptt_id: {sender_user}")
                return self.result

            # Recipient: synthesize via a second IMSI (best-effort)
            # — fall back to sending to self if pool only has one UE.
            recipient = sender  # safe fallback
            if len(self.ue_pool) > 1:
                ue2 = self.ue_pool[1]
                ru, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue2.imsi})
                if isinstance(ru, dict) and ru.get("mcptt_id"):
                    recipient = ru["mcptt_id"]

            msg, status = _mcx_api("/api/mcx/messages", "POST", {
                "sender": sender,
                "recipient": recipient,
                "content": "TC-MCX-030 hello",
            })
            if status not in (200, 201):
                self.fail_test(f"sds send failed: {status} {msg}")
                return self.result
            self.pass_test(sender=sender, recipient=recipient, message=msg)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class McxMcdataSdsGroupMessage(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-031",
        title="MCData SDS group message send",
        spec="TS 23.282 §7.4.2.5",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "mcdata", "sds", "group", "message"),
        setup=Setup.BASELINE,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  Validates the MCData group standalone-SDS send. TS 23.282\n"
            "  §7.4.2.5 sends one message addressed to a group_id; the\n"
            "  MCData server fans it out to all members.\n"
            "\n"
            "Procedure (TS 23.282 §7.4.2.5 + TS 24.282)\n"
            "  1. require_ue(); POST /api/mcx/users → sender mcptt_id;\n"
            "     fail_test if missing.\n"
            "  2. _ensure_group('tc-mcx-031-grp'); fail_test if None.\n"
            "  3. POST /api/mcx/groups/{gid}/members with sender.\n"
            "  4. POST /api/mcx/messages with sender, group_id=gid,\n"
            "     content='TC-MCX-031 group hello'.\n"
            "  5. fail_test if status not in (200, 201).\n"
            "  6. finally: _delete_group(gid).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Group SDS POST returns HTTP 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sender, group_id, message (the created envelope).\n"
            "\n"
            "Known constraints\n"
            "  Fan-out delivery is not verified — only the send-side\n"
            "  acceptance gate is exercised.\n"
            "  Fan-out delivery is not verified — only the send-side\n"
            "  acceptance gate is exercised. RTCP delivery confirmation is\n"
            "  TC-MCX-032 (currently pending)."
        ),
    )

    def run(self):
        gid = None
        try:
            ue = self.require_ue()
            user, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            sender = user.get("mcptt_id") if isinstance(user, dict) else None
            if not sender:
                self.fail_test(f"user create returned no mcptt_id: {user}")
                return self.result

            gid = _ensure_group("tc-mcx-031-grp")
            if not gid:
                self.fail_test("could not create / locate MCX group")
                return self.result
            _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                     {"mcptt_id": sender, "role": "member"})

            msg, status = _mcx_api("/api/mcx/messages", "POST", {
                "sender": sender,
                "group_id": gid,
                "content": "TC-MCX-031 group hello",
            })
            if status not in (200, 201):
                self.fail_test(f"group sds send failed: {status} {msg}")
                return self.result
            self.pass_test(sender=sender, group_id=gid, message=msg)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group(gid)
        return self.result


class McxMcdataSdsDeliveryConfirmation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-032",
        title="MCData SDS delivery confirmation tracking",
        spec="TS 23.282 §7.4.2.1.2",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MINOR,
        tags=("conformance", "mcx", "mcdata", "sds", "delivery", "confirmation"),
        setup=Setup.BASELINE,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  Validates the SDS delivery-confirmation feedback loop. TS\n"
            "  23.282 §7.4.2.1.2 promises a disposition-notification on\n"
            "  message delivery (mirrors RFC 8262 IMDN); the sender must\n"
            "  observe a delivered/displayed receipt.\n"
            "\n"
            "Procedure (TS 23.282 §7.4.2.1.2)\n"
            "  1. (spec'd) Send a private SDS message and capture id.\n"
            "  2. As recipient, PUT /api/mcx/data/messages/{id} with\n"
            "     state=delivered (or displayed).\n"
            "  3. Verify sender side now reflects the disposition.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/voice_media/\n"
            "  21_mcx.robot::TC-MCX-032 with reason 'no per-message PUT/\n"
            "  disposition endpoint yet'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  SA Core MCX router does not yet expose the per-message PUT\n"
            "  disposition surface.\n"
            "  Pending stub — _pending() always fails. Disposition surface\n"
            "  on the SA Core MCX router has to be implemented before the\n"
            "  TC can graduate to a real check."
        ),
    )

    def run(self):
        _pending(self, "TC-MCX-032",
                 "no per-message PUT/disposition endpoint yet")
        return self.result


# ─── User / Group Management (TS 23.379 §A.3 / §A.4) ─────────────────


class McxUserProfileCreation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-040",
        title="MCX user profile created from registered UE IMSI",
        spec="TS 23.379 §A.3",
        domain=Domain.MCX,
        nfs=(NF.UDM, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "user-profile", "creation", "imsi"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the MCPTT user-profile create / read with the\n"
            "  idempotency invariant: the same IMSI re-posted must yield\n"
            "  the same mcptt_id. TS 23.379 §A.3 places the user profile\n"
            "  as the canonical config-data row.\n"
            "\n"
            "Procedure (TS 23.379 §A.3)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/mcx/users {imsi}; fail_test on non-2xx.\n"
            "  3. Capture mcptt_id; fail_test if absent.\n"
            "  4. POST /api/mcx/users {imsi} again.\n"
            "  5. fail_test if status non-2xx.\n"
            "  6. fail_test if second mcptt_id != first.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return 2xx AND mcptt_id is byte-equal on both.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  mcptt_id, idempotent=True.\n"
            "\n"
            "Known constraints\n"
            "  No persistence-across-restart check; only same-session\n"
            "  idempotency.\n"
            "  No persistence-across-restart check; only same-session\n"
            "  idempotency. Operator must run a separate restart-based test\n"
            "  to validate UDM-side persistence.\n"
            "  Race conditions between concurrent users sharing the same\n"
            "  IMSI are not exercised — single-process test only."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            first, s1 = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            if s1 not in (200, 201) or not isinstance(first, dict):
                self.fail_test(f"first user create failed: {s1} {first}")
                return self.result
            mcptt_id = first.get("mcptt_id")
            if not mcptt_id:
                self.fail_test(f"user create returned no mcptt_id: {first}")
                return self.result

            second, s2 = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            if s2 not in (200, 201) or not isinstance(second, dict):
                self.fail_test(f"second user create failed: {s2} {second}")
                return self.result
            if second.get("mcptt_id") != mcptt_id:
                self.fail_test(
                    f"idempotency broken: first={mcptt_id} second={second.get('mcptt_id')}"
                )
                return self.result
            self.pass_test(mcptt_id=mcptt_id, idempotent=True)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class McxGroupCreationAndMembership(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-041",
        title="MCX group creation and membership management",
        spec="TS 23.379 §A.4",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "group", "creation", "membership"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins group-management CRUD on the MCX router. TS 23.379\n"
            "  §A.4 defines the related-group configuration data — group\n"
            "  + member rows must be persistable via the REST surface.\n"
            "\n"
            "Procedure (TS 23.379 §A.4)\n"
            "  1. require_ue(); POST /api/mcx/users → mcptt_id (fail if\n"
            "     missing).\n"
            "  2. POST /api/mcx/groups with name='tc-mcx-041-grp',\n"
            "     group_type='normal', max_members=50, priority=5.\n"
            "     If non-2xx, fall back to _ensure_group() — fail_test if\n"
            "     still no id.\n"
            "  3. POST /api/mcx/groups/{gid}/members with mcptt_id+role.\n"
            "  4. fail_test if member POST not in (200, 201).\n"
            "  5. finally: _delete_group(gid).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — group shape pinned.\n"
            "\n"
            "Pass criteria\n"
            "  Group create AND member add both return 2xx.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  group_id, mcptt_id.\n"
            "\n"
            "Known constraints\n"
            "  GET-back to assert the member is listed is performed\n"
            "  implicitly via the response codes only, not body shape.\n"
            "  GET-back to assert the member is listed is performed\n"
            "  implicitly via the response codes only, not body shape.\n"
            "  Group row is reaped on finally."
        ),
    )

    def run(self):
        gid = None
        try:
            ue = self.require_ue()
            user, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            mcptt_id = user.get("mcptt_id") if isinstance(user, dict) else None
            if not mcptt_id:
                self.fail_test(f"user create returned no mcptt_id: {user}")
                return self.result

            grp, status = _mcx_api("/api/mcx/groups", "POST", {
                "name": "tc-mcx-041-grp",
                "group_type": "normal",
                "max_members": 50,
                "priority": 5,
            })
            if status not in (200, 201):
                # Maybe group already exists — locate it.
                gid = _ensure_group("tc-mcx-041-grp")
                if not gid:
                    self.fail_test(f"group create failed: {status} {grp}")
                    return self.result
            else:
                gid = grp.get("id") or grp.get("group_id")

            _, m_status = _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                                   {"mcptt_id": mcptt_id, "role": "member"})
            if m_status not in (200, 201):
                self.fail_test(f"add member failed: {m_status}")
                return self.result

            self.pass_test(group_id=gid, mcptt_id=mcptt_id)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group(gid)
        return self.result


class McxGroupCapacityLimit(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-042",
        title="MCX group rejects member at max capacity",
        spec="TS 23.379 §A.4",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MINOR,
        tags=("conformance", "mcx", "group", "capacity", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates the operator capacity policy on top of the §A.4\n"
            "  group data: max_members=1 must reject the second member.\n"
            "  Without this, group-priority scheduling for emergency calls\n"
            "  has no quantitative anchor.\n"
            "\n"
            "Procedure (TS 23.379 §A.4)\n"
            "  1. require_ue(); POST /api/mcx/users → mcptt_a.\n"
            "  2. POST /api/mcx/groups (max_members=1); fall back via\n"
            "     _ensure_group('tc-mcx-042-grp', max_members=1).\n"
            "  3. POST first member; fail_test if not 2xx.\n"
            "  4. If ue_pool len > 1, POST users for ue_pool[1] → mcptt_b.\n"
            "     If no mcptt_b, _pending() to flag the gap.\n"
            "  5. POST second member; fail_test if status IS 2xx (must\n"
            "     reject due to capacity).\n"
            "  6. finally: _delete_group(gid).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — capacity=1 pinned.\n"
            "\n"
            "Pass criteria\n"
            "  Second member POST returns NON-2xx status.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  group_id, capacity_status (the rejected status code).\n"
            "\n"
            "Known constraints\n"
            "  When the UE pool is single-UE the test reports as pending\n"
            "  (FAIL) rather than passing vacuously.\n"
            "  When the UE pool is single-UE the test reports as pending\n"
            "  (FAIL) rather than passing vacuously. Operator can extend the\n"
            "  pool to enable the real capacity check."
        ),
    )

    def run(self):
        gid = None
        try:
            ue = self.require_ue()
            user_a, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue.imsi})
            mcptt_a = user_a.get("mcptt_id") if isinstance(user_a, dict) else None
            if not mcptt_a:
                self.fail_test(f"user A create returned no mcptt_id: {user_a}")
                return self.result

            grp, status = _mcx_api("/api/mcx/groups", "POST", {
                "name": "tc-mcx-042-grp",
                "group_type": "normal",
                "max_members": 1,
                "priority": 5,
            })
            if status not in (200, 201):
                gid = _ensure_group("tc-mcx-042-grp", max_members=1)
                if not gid:
                    self.fail_test(f"group create failed: {status} {grp}")
                    return self.result
            else:
                gid = grp.get("id") or grp.get("group_id")

            _, m1 = _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                             {"mcptt_id": mcptt_a, "role": "member"})
            if m1 not in (200, 201):
                self.fail_test(f"first member add failed: {m1}")
                return self.result

            # Second member with a different mcptt_id (or pending if pool too small).
            mcptt_b = None
            if len(self.ue_pool) > 1:
                ue2 = self.ue_pool[1]
                ub, _ = _mcx_api("/api/mcx/users", "POST", {"imsi": ue2.imsi})
                if isinstance(ub, dict):
                    mcptt_b = ub.get("mcptt_id")
            if not mcptt_b:
                _pending(self, "TC-MCX-042",
                         "needs a second distinct MCX user in pool")
                return self.result

            _, m2 = _mcx_api(f"/api/mcx/groups/{gid}/members", "POST",
                             {"mcptt_id": mcptt_b, "role": "member"})
            if m2 in (200, 201):
                self.fail_test(
                    f"second member add expected to fail (capacity=1), got {m2}",
                )
                return self.result
            self.pass_test(group_id=gid, capacity_status=m2)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group(gid)
        return self.result


# ─── Priority / Preemption (TS 23.379 §10.6.2.6.1.2 / TS 24.380) ─────


class McxEmergencyPreemptsNormal(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-050",
        title="MCX emergency call preempts active normal call",
        spec="TS 23.379 §10.6.2.6.1.2",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "priority", "preemption", "emergency"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Drives the in-place emergency upgrade: a normal group call\n"
            "  is active and an emergency call must pre-empt it. TS 23.379\n"
            "  §10.6.2.6.1.2 'MCPTT group call upgraded to emergency' +\n"
            "  TS 24.380 §6.2.4.5.4 floor revoke.\n"
            "\n"
            "Procedure (TS 23.379 §10.6.2.6.1.2 + TS 24.380 §6.2.4.5.4)\n"
            "  1. (spec'd) Two users + same group. User A initiates a\n"
            "     normal group call; user B joins and holds the floor.\n"
            "  2. User C initiates an emergency group call against the\n"
            "     same group.\n"
            "  3. Controller pre-empts the existing call: revokes floor\n"
            "     from B, signals emergency on the call, grants floor to C.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/voice_media/\n"
            "  21_mcx.robot::TC-MCX-050 with reason 'needs multi-user call\n"
            "  + floor orchestration'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  Multi-user call + floor orchestration is not yet wired in\n"
            "  the Python tester process."
        ),
    )

    def run(self):
        _pending(self, "TC-MCX-050",
                 "needs multi-user call + floor orchestration")
        return self.result


class McxPriorityFloorQueueOrdering(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MCX-051",
        title="Floor queue orders requests by priority level",
        spec="TS 24.380 §6.2.4.9",
        domain=Domain.MCX,
        nfs=(NF.AF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "mcx", "priority", "floor-queue", "ordering"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Validates the 'U: queued' state of the floor participant\n"
            "  FSM (TS 24.380 §6.2.4.9): floor queue is priority-ordered,\n"
            "  high-priority requesters cut ahead of earlier low-priority\n"
            "  ones when the floor is released.\n"
            "\n"
            "Procedure (TS 24.380 §6.2.4.9)\n"
            "  1. (spec'd) Three users A, B (low), C (high) in same\n"
            "     group call.\n"
            "  2. A holds the floor; B requests (queued); C requests\n"
            "     (queued at front).\n"
            "  3. A releases. Controller grants to C first, then B on\n"
            "     C's release.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/voice_media/\n"
            "  21_mcx.robot::TC-MCX-051 with reason 'needs three MCX users\n"
            "  with distinct priorities'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  Synthesising three distinct MCX users with distinct\n"
            "  priorities is not yet wired in the tester."
        ),
    )

    def run(self):
        _pending(self, "TC-MCX-051",
                 "needs three MCX users with distinct priorities")
        return self.result


# ─────────────────────────────────────────────────────────────────────


ALL_MCX_TCS = [
    McxMcpttGroupCallSetup,
    McxMcpttEmergencyGroupCallSetup,
    McxMcpttGroupCallTeardown,
    McxMcpttFloorRequestAndGrant,
    McxMcpttFloorRelease,
    McxMcpttFloorPreemption,
    McxMcvideoGroupCallSetup,
    McxMcvideoTransmissionControl,
    McxMcdataSdsPrivateMessage,
    McxMcdataSdsGroupMessage,
    McxMcdataSdsDeliveryConfirmation,
    McxUserProfileCreation,
    McxGroupCreationAndMembership,
    McxGroupCapacityLimit,
    McxEmergencyPreemptsNormal,
    McxPriorityFloorQueueOrdering,
]

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Fault Management (TS 28.532 / TS 32.111-1 / X.733).

TS 28.532 §11.2a   Generic fault supervision management service —
                   raise / change / ack / clear life-cycle.
TS 32.111-1        Original 3GPP fault management spec (deferred:
                   PDF not in specs/3gpp/).
ITU-T X.733        Alarm-reporting function — perceived severity +
                   probable-cause vocabularies.

Drives the SA Core REST surface at /api/fm/*: synthetic raise (drill /
operator-initiated), ack, clear (single + bulk), severity histogram,
active list, history. Endpoints return `{ok, ...}` for mutators and
`{alarms, timestamp}` for list endpoints — matches templates/faults.html.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_fm")


def _fm_api(path, method="GET", body=None):
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


def _raise_alarm(mo, problem, severity="Major", alarm_type="Processing",
                 cause="softwareError", text=""):
    return _fm_api("/api/fm/raise", "POST", {
        "managed_object": mo,
        "alarm_type": alarm_type,
        "perceived_severity": severity,
        "probable_cause": cause,
        "specific_problem": problem,
        "additional_text": text,
    })


class FmRaiseAndList(TestCase):
    """TC-FM-001: Raise an alarm; it appears in active-alarms + alarm-counts."""
    SPEC = TestSpec(
        tc_id="TC-FM-001",
        title="Raise alarm appears in active-alarms and alarm-counts",
        spec="TS 28.532 §11.2a",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundational smoke for the TS 28.532 §11.2a fault supervision\n"
            "  raise path plus the X.733 alarm-list / counts read paths.\n"
            "  Verifies that synthesising one Major alarm via /api/fm/raise\n"
            "  is reflected in both the active-alarms list and the alarm-\n"
            "  counts histogram tile the dashboard renders.\n"
            "\n"
            "Procedure (TS 28.532 §11.2a + X.733)\n"
            "  1. POST /api/fm/raise with managed_object='test/mo/TC-FM-001',\n"
            "     perceived_severity='Major', alarm_type='Processing',\n"
            "     probable_cause='softwareError'. Assert 200 + ok + alarm_id.\n"
            "  2. GET /api/fm/active-alarms; assert the new alarm_id appears\n"
            "     in alarms[].\n"
            "  3. GET /api/fm/alarm-counts; assert counts['Major'] >= 1 and\n"
            "     counts['total'] >= 1.\n"
            "  4. Finally clause POSTs /api/fm/clear-all scoped to the test\n"
            "     managed_object to reset state.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — managed_object, severity and cause are fixed test\n"
            "  fixtures.\n"
            "\n"
            "Pass criteria\n"
            "  Raise returns ok with non-empty alarm_id, alarm_id is in the\n"
            "  active list, Major and total counts are both >= 1.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  alarm_id.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — clear-all in finally prevents state bleed into\n"
            "  later TCs. Counts assertion is a >=1 lower bound (other suites\n"
            "  may have raised alarms concurrently)."
        ),
    )

    def run(self):
        try:
            r, s = _raise_alarm("test/mo/TC-FM-001", "TC-FM-001 synthetic alarm",
                                 severity="Major")
            if s != 200 or not r.get("ok") or not r.get("alarm_id"):
                self.fail_test(f"raise failed: {s} {r}")
                return self.result
            aid = r["alarm_id"]

            r2, _ = _fm_api("/api/fm/active-alarms")
            ids = [a.get("alarm_id") for a in r2.get("alarms", [])]
            if aid not in ids:
                self.fail_test(f"alarm {aid} not in active list", ids=ids)
                return self.result

            counts, _ = _fm_api("/api/fm/alarm-counts")
            if counts.get("Major", 0) < 1 or counts.get("total", 0) < 1:
                self.fail_test(f"counts didn't reflect raise: {counts}")
                return self.result
            self.pass_test(alarm_id=aid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _fm_api("/api/fm/clear-all", "POST", {"managed_object": "test/mo/TC-FM-001"})
        return self.result


class FmAckClear(TestCase):
    """TC-FM-002: Ack moves to ack_state=Acknowledged; clear removes from active."""
    SPEC = TestSpec(
        tc_id="TC-FM-002",
        title="Alarm ack flips ack_state; clear removes from active list",
        spec="TS 28.532 §11.2a",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Cover the ack → clear half of the X.733 alarm life-cycle on\n"
            "  TS 28.532 §11.2a. Confirms acknowledge flips the ack_state\n"
            "  field without removing the alarm from the active list, and\n"
            "  clear removes it from the active list entirely.\n"
            "\n"
            "Procedure (TS 28.532 §11.2a + ITU-T X.733 life-cycle)\n"
            "  1. POST /api/fm/raise with severity='Critical', mo='test/mo/\n"
            "     TC-FM-002'. Capture alarm_id.\n"
            "  2. POST /api/fm/acknowledge {alarm_id, user:'tc-fm-002'};\n"
            "     assert 200 + ok=True.\n"
            "  3. GET /api/fm/active-alarms; find the alarm_id row and\n"
            "     assert ack_state == 'Acknowledged'.\n"
            "  4. POST /api/fm/clear {alarm_id, text:'TC-FM-002 done'};\n"
            "     assert 200 + ok=True.\n"
            "  5. GET /api/fm/active-alarms again and assert alarm_id is no\n"
            "     longer in the alarms[] list.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — managed_object, severity, user, and text are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Ack returns ok, ack_state flips to 'Acknowledged', clear\n"
            "  returns ok, and alarm_id disappears from the active list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No finally cleanup — the clear step is the\n"
            "  cleanup. A failure mid-flow could leak an alarm row."
        ),
    )

    def run(self):
        try:
            r, _ = _raise_alarm("test/mo/TC-FM-002", "TC-FM-002 ack+clear",
                                 severity="Critical")
            aid = r.get("alarm_id")
            if not aid:
                self.fail_test(f"raise failed: {r}")
                return self.result

            # Ack
            ar, ass = _fm_api("/api/fm/acknowledge", "POST",
                               {"alarm_id": aid, "user": "tc-fm-002"})
            if ass != 200 or not ar.get("ok"):
                self.fail_test(f"ack failed: {ass} {ar}")
                return self.result

            # Verify ack state
            active, _ = _fm_api("/api/fm/active-alarms")
            row = next((a for a in active.get("alarms", [])
                        if a.get("alarm_id") == aid), None)
            if not row or row.get("ack_state") != "Acknowledged":
                self.fail_test(f"ack_state not Acknowledged: {row}")
                return self.result

            # Clear
            cr, css = _fm_api("/api/fm/clear", "POST",
                               {"alarm_id": aid, "text": "TC-FM-002 done"})
            if css != 200 or not cr.get("ok"):
                self.fail_test(f"clear failed: {css} {cr}")
                return self.result

            active2, _ = _fm_api("/api/fm/active-alarms")
            ids = [a.get("alarm_id") for a in active2.get("alarms", [])]
            if aid in ids:
                self.fail_test(f"alarm {aid} still active after clear")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class FmCorrelation(TestCase):
    """TC-FM-003: Re-raising same (mo, cause, problem) bumps raise_count, no new id."""
    SPEC = TestSpec(
        tc_id="TC-FM-003",
        title="Re-raising same alarm bumps raise_count without minting a new id",
        spec="TS 28.532 §11.2a",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the X.733 alarm-correlation contract: re-raising the same\n"
            "  (managed_object, probable_cause, specific_problem) tuple must\n"
            "  correlate with the existing active row, bumping raise_count\n"
            "  rather than allocating a new alarm_id. Prevents alarm-flap\n"
            "  storms from carpeting the dashboard.\n"
            "\n"
            "Procedure (TS 28.532 §11.2a + X.733 §8.1.1)\n"
            "  1. POST /api/fm/raise three times with identical mo='test/mo/\n"
            "     TC-FM-003' and specific_problem='TC-FM-003 flap'.\n"
            "  2. Assert the three responses carry the same alarm_id (no\n"
            "     duplication).\n"
            "  3. GET /api/fm/active-alarms; locate the row by alarm_id and\n"
            "     assert raise_count >= 3.\n"
            "  4. Finally clause clears /api/fm/clear-all for the test MO.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — three identical raises with fixed fixture body.\n"
            "\n"
            "Pass criteria\n"
            "  r1.alarm_id == r2.alarm_id == r3.alarm_id and the active-list\n"
            "  row reports raise_count >= 3.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  raise_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Correlation key is (mo, probable_cause,\n"
            "  specific_problem) — changing any of the three would mint a\n"
            "  new row instead."
        ),
    )

    def run(self):
        try:
            r1, _ = _raise_alarm("test/mo/TC-FM-003", "TC-FM-003 flap")
            r2, _ = _raise_alarm("test/mo/TC-FM-003", "TC-FM-003 flap")
            r3, _ = _raise_alarm("test/mo/TC-FM-003", "TC-FM-003 flap")
            if not (r1.get("alarm_id") == r2.get("alarm_id") == r3.get("alarm_id")):
                self.fail_test(
                    f"correlation failed: {r1.get('alarm_id')} "
                    f"{r2.get('alarm_id')} {r3.get('alarm_id')}")
                return self.result
            aid = r1["alarm_id"]
            active, _ = _fm_api("/api/fm/active-alarms")
            row = next((a for a in active.get("alarms", [])
                        if a.get("alarm_id") == aid), None)
            if not row or row.get("raise_count", 0) < 3:
                self.fail_test(f"raise_count not bumped: {row}")
                return self.result
            self.pass_test(raise_count=row.get("raise_count"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _fm_api("/api/fm/clear-all", "POST", {"managed_object": "test/mo/TC-FM-003"})
        return self.result


class FmClearAll(TestCase):
    """TC-FM-004: clear-all scoped to managed_object empties only that scope."""
    SPEC = TestSpec(
        tc_id="TC-FM-004",
        title="clear-all scoped to managed_object empties only that scope",
        spec="TS 28.532 §11.2a",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the managed_object scoping on /api/fm/clear-all. Bulk\n"
            "  clear must affect only the requested MO and never bleed\n"
            "  across other managed objects. This is the foundational\n"
            "  cleanup primitive that nearly every other FM test depends on.\n"
            "\n"
            "Procedure (TS 28.532 §11.2a bulk clear)\n"
            "  1. POST /api/fm/raise twice on mo='test/mo/TC-FM-004A'\n"
            "     (alpha-1, alpha-2 specific problems).\n"
            "  2. POST /api/fm/raise once on mo='test/mo/TC-FM-004B' (beta-1);\n"
            "     capture beta alarm_id.\n"
            "  3. POST /api/fm/clear-all with managed_object='test/mo/\n"
            "     TC-FM-004A'.\n"
            "  4. Assert response.cleared >= 2.\n"
            "  5. GET /api/fm/active-alarms; assert beta_id is still in the\n"
            "     alarms[] list.\n"
            "  6. Finally clause clears MO-B as cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — two fixed managed_objects (A and B).\n"
            "\n"
            "Pass criteria\n"
            "  clear-all scoped to A returns ok with cleared >= 2 and the\n"
            "  beta alarm on B remains in the active list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. cleared count is a lower bound (>=2) — alarms\n"
            "  from prior runs on the same MO would inflate it."
        ),
    )

    def run(self):
        try:
            _raise_alarm("test/mo/TC-FM-004A", "alpha-1")
            _raise_alarm("test/mo/TC-FM-004A", "alpha-2")
            r3, _ = _raise_alarm("test/mo/TC-FM-004B", "beta-1")
            beta_id = r3.get("alarm_id")

            r, s = _fm_api("/api/fm/clear-all", "POST",
                            {"managed_object": "test/mo/TC-FM-004A"})
            if s != 200 or not r.get("ok"):
                self.fail_test(f"clear-all failed: {s} {r}")
                return self.result
            if r.get("cleared", 0) < 2:
                self.fail_test(f"expected ≥2 cleared, got {r.get('cleared')}",
                               body=r)
                return self.result

            # Beta should still be active
            active, _ = _fm_api("/api/fm/active-alarms")
            ids = [a.get("alarm_id") for a in active.get("alarms", [])]
            if beta_id not in ids:
                self.fail_test(
                    f"beta cleared too: id={beta_id} not in {ids}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _fm_api("/api/fm/clear-all", "POST", {"managed_object": "test/mo/TC-FM-004B"})
        return self.result


class FmHistory(TestCase):
    """TC-FM-005: cleared alarms appear in alarm-history."""
    SPEC = TestSpec(
        tc_id="TC-FM-005",
        title="Cleared alarms surface in /alarm-history",
        spec="TS 28.532 §11.2a",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the FM history journal: every cleared alarm must surface\n"
            "  in /api/fm/alarm-history with perceived_severity='Cleared'.\n"
            "  Confirms the X.733 life-cycle terminator is durably recorded\n"
            "  for post-mortem and compliance audit.\n"
            "\n"
            "Procedure (TS 28.532 §11.2a + X.733 history)\n"
            "  1. POST /api/fm/raise on mo='test/mo/TC-FM-005' with\n"
            "     specific_problem='TC-FM-005 history'. Capture alarm_id.\n"
            "  2. POST /api/fm/clear with {alarm_id}.\n"
            "  3. GET /api/fm/alarm-history?limit=200.\n"
            "  4. Locate the row matching alarm_id in alarms[].\n"
            "  5. Assert the row exists and row.perceived_severity ==\n"
            "     'Cleared'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — managed_object and history limit are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  History contains the alarm_id with perceived_severity ==\n"
            "  'Cleared'. Missing row or wrong severity fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. limit=200 must be high enough to retain the row\n"
            "  in busy environments; concurrent suites raising many alarms\n"
            "  could push the row out of the history window before this\n"
            "  TC reads it back."
        ),
    )

    def run(self):
        try:
            r, _ = _raise_alarm("test/mo/TC-FM-005", "TC-FM-005 history")
            aid = r.get("alarm_id")
            _fm_api("/api/fm/clear", "POST", {"alarm_id": aid})

            hist, _ = _fm_api("/api/fm/alarm-history?limit=200")
            row = next((a for a in hist.get("alarms", [])
                        if a.get("alarm_id") == aid), None)
            if not row:
                self.fail_test(f"alarm {aid} not in history")
                return self.result
            if row.get("perceived_severity") != "Cleared":
                self.fail_test(f"history shows wrong severity: {row}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class FmValidation(TestCase):
    """TC-FM-006: Invalid alarm_type / severity / missing field → 400."""
    SPEC = TestSpec(
        tc_id="TC-FM-006",
        title="Invalid alarm_type / severity / missing field returns 400",
        spec="TS 28.532 §11.2a",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the X.733 input vocabulary at the FM REST route layer.\n"
            "  Malformed raises (missing mandatory attrs, invalid alarm_type,\n"
            "  Cleared severity on raise) and bad ack ids must produce clean\n"
            "  HTTP 400 / 404 — never 500 leaks from the SQLite layer.\n"
            "\n"
            "Procedure (TS 28.532 §11.2a + X.733 §8.1.1 vocabulary)\n"
            "  1. POST /api/fm/raise with missing managed_object; assert 400.\n"
            "  2. POST /api/fm/raise with alarm_type='BAD' (not in X.733\n"
            "     event-types); assert 400.\n"
            "  3. POST /api/fm/raise with perceived_severity='Cleared'\n"
            "     (terminator, not valid on raise); assert 400.\n"
            "  4. POST /api/fm/acknowledge with empty body {}; assert 400\n"
            "     (missing alarm_id).\n"
            "  5. POST /api/fm/acknowledge with alarm_id=99999999 (unknown);\n"
            "     assert 404.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — bodies are hard-coded negative-path fixtures.\n"
            "\n"
            "Pass criteria\n"
            "  Steps 1-4 return 400, step 5 returns 404. Any 500 or 200\n"
            "  fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No state cleanup — every step is a negative path\n"
            "  that should leave no rows behind."
        ),
    )

    def run(self):
        try:
            # Missing managed_object
            r1, s1 = _fm_api("/api/fm/raise", "POST", {
                "alarm_type": "Processing",
                "perceived_severity": "Major",
                "specific_problem": "x",
            })
            if s1 != 400:
                self.fail_test(f"missing mo did not 400: {s1}")
                return self.result

            # Invalid alarm_type
            r2, s2 = _fm_api("/api/fm/raise", "POST", {
                "managed_object": "x",
                "alarm_type": "BAD",
                "perceived_severity": "Major",
                "specific_problem": "x",
            })
            if s2 != 400:
                self.fail_test(f"bad alarm_type did not 400: {s2}")
                return self.result

            # Invalid severity (Cleared isn't allowed on raise)
            r3, s3 = _fm_api("/api/fm/raise", "POST", {
                "managed_object": "x",
                "alarm_type": "Processing",
                "perceived_severity": "Cleared",
                "specific_problem": "x",
            })
            if s3 != 400:
                self.fail_test(f"Cleared on raise did not 400: {s3}")
                return self.result

            # Ack with no alarm_id
            r4, s4 = _fm_api("/api/fm/acknowledge", "POST", {})
            if s4 != 400:
                self.fail_test(f"ack with no id did not 400: {s4}")
                return self.result

            # Ack non-existent alarm → 404
            r5, s5 = _fm_api("/api/fm/acknowledge", "POST", {"alarm_id": 99999999})
            if s5 != 404:
                self.fail_test(f"ack of unknown id did not 404: {s5}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class FmCountsConsistency(TestCase):
    """TC-FM-007: counts['total'] equals sum of severity buckets."""
    SPEC = TestSpec(
        tc_id="TC-FM-007",
        title="alarm-counts total equals sum of severity buckets",
        spec="TS 28.532 §11.2a",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the alarm-counts histogram invariant the dashboard tile\n"
            "  relies on: the 'total' field must equal the sum of the five\n"
            "  X.733 perceived-severity buckets. A drift here is a sign of\n"
            "  a stale or partial sampler write.\n"
            "\n"
            "Procedure (TS 28.532 §11.2a + ITU-T X.733 §8.1.2.3)\n"
            "  1. GET /api/fm/alarm-counts.\n"
            "  2. Compute sev_sum = Critical + Major + Minor + Warning +\n"
            "     Indeterminate (defaulting missing keys to 0).\n"
            "  3. Assert sev_sum == counts['total']. Mismatch fails the\n"
            "     test with both numbers in the failure body.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  sum(severity buckets) == total exactly. Off-by-one or wider\n"
            "  drift fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  counts (the full payload).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Runs read-only; safe to interleave with other\n"
            "  FM TCs but a race against an in-flight raise that bumps a\n"
            "  severity bucket but has not yet been counted into 'total'\n"
            "  could trip the invariant momentarily. Re-run on transient\n"
            "  failure; alternatively gate behind Setup.QUIESCED. Counts\n"
            "  payload is included in failure details to aid triage."
        ),
    )

    def run(self):
        try:
            r, _ = _fm_api("/api/fm/alarm-counts")
            sev_sum = (r.get("Critical", 0) + r.get("Major", 0)
                       + r.get("Minor", 0) + r.get("Warning", 0)
                       + r.get("Indeterminate", 0))
            if sev_sum != r.get("total", -1):
                self.fail_test(
                    f"severity sum {sev_sum} != total {r.get('total')}",
                    counts=r)
                return self.result
            self.pass_test(counts=r)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_FM_TCS = [
    FmRaiseAndList,
    FmAckClear,
    FmCorrelation,
    FmClearAll,
    FmHistory,
    FmValidation,
    FmCountsConsistency,
]

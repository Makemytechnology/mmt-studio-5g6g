# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Subscriber Trace (TS 32.421 / 32.422 / 32.423).

TS 32.421 — Trace concepts and requirements (Trace Recording Session).
TS 32.422 — Trace control + configuration management (depth, NE list,
            duration, IMSI filter).
TS 32.423 — Trace data definition and management (per-record fields,
            file naming).

Drives the SA Core REST surface at /api/trace/*: session start (with
depth + interfaces + duration_sec validation), list, stop, delete,
records, JSON / XML export. Endpoints return `{ok, ...}` envelopes
matching templates/traces.html (`d.ok && d.sessions`, `d.ok &&
d.records`).
"""

import json
import logging
import urllib.request
import urllib.error

from src import baseline
from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_trace")


def _trace_api(path, method="GET", body=None, raw=False):
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            body_bytes = resp.read()
            if raw:
                return body_bytes, resp.status, dict(resp.headers)
            return json.loads(body_bytes.decode()), resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


class TraceStartListStop(TestCase):
    """TC-TRACE-001: Start a trace, list, stop. (TS 32.422 §5.6 trace activation)"""
    SPEC = TestSpec(
        tc_id="TC-TRACE-001",
        title="Subscriber Trace start / list / stop activation",
        spec="TS 32.422 §5.6",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the TS 32.422 §5.6 trace activation lifecycle:\n"
            "  start (create Trace Recording Session), list (the OAM\n"
            "  console enumerates active sessions), stop (transition\n"
            "  to 'stopped' so trace files can be collected). This is\n"
            "  the foundation smoke for every subscriber-trace flow.\n"
            "\n"
            "Procedure (TS 32.422 §5.6 + TS 32.421 §4)\n"
            "  1. POST /api/trace/start with imsi=baseline.imsi(embb-\n"
            "     bulk,0), depth=medium, interfaces=N1,N2, duration_\n"
            "     sec=600; assert HTTP 200, r.ok, non-empty trace_ref.\n"
            "  2. GET /api/trace/sessions; assert lr.ok and trace_ref\n"
            "     appears in the sessions list.\n"
            "  3. POST /api/trace/{trace_ref}/stop; assert HTTP 200\n"
            "     and sr.ok.\n"
            "  4. GET /api/trace/sessions again; find the matching row\n"
            "     and assert status == 'stopped'.\n"
            "  5. finally DELETEs the session for cleanliness.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — body fields hard-coded; IMSI from baseline).\n"
            "\n"
            "Pass criteria\n"
            "  Start returns trace_ref AND list contains it AND stop\n"
            "  returns 200/ok AND post-stop status is 'stopped'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE so baseline.imsi(embb-bulk,0) resolves.\n"
            "  duration_sec=600 is well within the route range."
        ),
    )

    def run(self):
        try:
            r, s = _trace_api("/api/trace/start", "POST", {
                "imsi": baseline.imsi("embb-bulk", 0),
                "depth": "medium",
                "interfaces": "N1,N2",
                "duration_sec": 600,
            })
            if s != 200 or not r.get("ok") or not r.get("trace_ref"):
                self.fail_test(f"start failed: {s} {r}")
                return self.result
            ref = r["trace_ref"]

            # List
            lr, _ = _trace_api("/api/trace/sessions")
            if not lr.get("ok"):
                self.fail_test(f"list missing ok: {lr}")
                return self.result
            refs = [s.get("trace_ref") for s in lr.get("sessions", [])]
            if ref not in refs:
                self.fail_test(f"ref {ref} not listed", refs=refs[:5])
                return self.result

            # Stop
            sr, ss = _trace_api(f"/api/trace/{ref}/stop", "POST")
            if ss != 200 or not sr.get("ok"):
                self.fail_test(f"stop failed: {ss} {sr}")
                return self.result

            # Verify status flipped
            lr2, _ = _trace_api("/api/trace/sessions")
            row = next((s for s in lr2.get("sessions", [])
                        if s.get("trace_ref") == ref), None)
            if not row or row.get("status") != "stopped":
                self.fail_test(f"status not stopped: {row}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            try:
                _trace_api(f"/api/trace/{ref}", "DELETE")
            except Exception:
                pass
        return self.result


class TraceDepthValidation(TestCase):
    """TC-TRACE-002: Bad depth → 400 (TS 32.422 §5.6 depth enumeration)."""
    SPEC = TestSpec(
        tc_id="TC-TRACE-002",
        title="Bad depth / out-of-range duration on trace/start → 400",
        spec="TS 32.422 §5.6",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative pin for the §5.6 depth enumeration and the\n"
            "  duration_sec range. TS 32.422 defines depth as a fixed\n"
            "  enum (minimum / medium / maximum / minimumWithoutVendor-\n"
            "  Specific / mediumWithoutVendorSpecific / maximumWithout-\n"
            "  VendorSpecific) and operator-config caps duration; the\n"
            "  route must reject anything outside these bounds at\n"
            "  activation time, so a bad session can never start\n"
            "  chewing disk or eat its trace-collector budget.\n"
            "\n"
            "Procedure (TS 32.422 §5.6)\n"
            "  1. POST /api/trace/start with body {depth:BAD}.\n"
            "     Capture status; assert HTTP 400 (depth not in the\n"
            "     §5.6 enumeration).\n"
            "  2. POST /api/trace/start with body {duration_sec:\n"
            "     999999} — far past any sane operator cap. Capture\n"
            "     status; assert HTTP 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — bad payloads hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The exact valid range for duration_sec is\n"
            "  not asserted here; only that 999999 is out of bounds."
        ),
    )

    def run(self):
        try:
            r, s = _trace_api("/api/trace/start", "POST", {
                "depth": "BAD",
            })
            if s != 400:
                self.fail_test(f"bad depth did not 400: {s} {r}")
                return self.result

            # Out-of-range duration
            r2, s2 = _trace_api("/api/trace/start", "POST", {
                "duration_sec": 999999,
            })
            if s2 != 400:
                self.fail_test(f"large duration did not 400: {s2} {r2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceDuplicateStartIsRejected(TestCase):
    """TC-TRACE-003: Re-using a trace_ref after one already exists fails."""
    SPEC = TestSpec(
        tc_id="TC-TRACE-003",
        title="Duplicate trace_ref start is rejected (PRIMARY KEY)",
        spec="TS 32.421 §4",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the trace_ref PRIMARY KEY constraint on the trace\n"
            "  session table. TS 32.421 §4 defines Trace Reference as\n"
            "  the globally unique identifier of a Trace Recording\n"
            "  Session — re-using one mid-run would corrupt the\n"
            "  collected records by mixing different sessions in the\n"
            "  same file namespace and would break the TS 32.423 §6\n"
            "  file-naming convention that relies on trace_ref being\n"
            "  the row's natural key.\n"
            "\n"
            "Procedure (TS 32.421 §4)\n"
            "  1. POST /api/trace/start with trace_ref='tc-trace-003-\n"
            "     fixed', depth=minimum; assert HTTP 200 / r.ok.\n"
            "  2. POST /api/trace/start with the same fixed trace_ref;\n"
            "     assert HTTP status != 200 (duplicate rejected by the\n"
            "     PRIMARY KEY constraint).\n"
            "  3. finally DELETEs the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — trace_ref hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  First start 200/ok AND second start status != 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The exact error status (409 vs 400) is not\n"
            "  asserted — only that 200 is impossible on duplicate."
        ),
    )

    def run(self):
        ref = "tc-trace-003-fixed"
        try:
            # First start succeeds
            r, s = _trace_api("/api/trace/start", "POST",
                              {"trace_ref": ref, "depth": "minimum"})
            if s != 200 or not r.get("ok"):
                self.fail_test(f"first start failed: {s} {r}")
                return self.result

            # Second start with same ref must fail (PRIMARY KEY)
            r2, s2 = _trace_api("/api/trace/start", "POST",
                                 {"trace_ref": ref, "depth": "minimum"})
            if s2 == 200:
                self.fail_test(f"duplicate ref accepted: {r2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            try:
                _trace_api(f"/api/trace/{ref}", "DELETE")
            except Exception:
                pass
        return self.result


class TraceRecordsEmpty(TestCase):
    """TC-TRACE-004: Records endpoint returns {ok, records: []} for fresh ref."""
    SPEC = TestSpec(
        tc_id="TC-TRACE-004",
        title="Trace records endpoint returns empty list for a fresh ref",
        spec="TS 32.423 §5.1",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the TS 32.423 §5.1 trace-data envelope shape on\n"
            "  /api/trace/{ref}/records. A fresh trace ref will have\n"
            "  zero captured records, but the records endpoint must\n"
            "  still return a well-formed {ok, records: []} envelope\n"
            "  so the OAM panel can render the empty state cleanly\n"
            "  (rather than 404, which would force the UI to special-\n"
            "  case the just-started condition and would break the\n"
            "  records-export pre-flight pattern).\n"
            "\n"
            "Procedure (TS 32.423 §5.1)\n"
            "  1. POST /api/trace/start with depth=minimum; capture\n"
            "     trace_ref.\n"
            "  2. GET /api/trace/{ref}/records.\n"
            "  3. Assert HTTP 200 and rec.ok.\n"
            "  4. Assert isinstance(rec['records'], list) — emptiness\n"
            "     is OK; missing-or-not-list is not.\n"
            "  5. finally DELETEs the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none).\n"
            "\n"
            "Pass criteria\n"
            "  HTTP 200 AND rec.ok AND records is a list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  record_count — len(rec['records']); typically 0.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. depth=minimum minimises bookkeeping; record\n"
            "  contents (Stage-3 record fields) are not asserted here."
        ),
    )

    def run(self):
        try:
            r, _ = _trace_api("/api/trace/start", "POST",
                              {"depth": "minimum"})
            ref = r.get("trace_ref")
            rec, s = _trace_api(f"/api/trace/{ref}/records")
            if s != 200 or not rec.get("ok"):
                self.fail_test(f"records GET failed: {s} {rec}")
                return self.result
            if not isinstance(rec.get("records"), list):
                self.fail_test(f"records not a list: {rec}")
                return self.result
            self.pass_test(record_count=len(rec["records"]))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            try:
                _trace_api(f"/api/trace/{ref}", "DELETE")
            except Exception:
                pass
        return self.result


class TraceStopUnknown(TestCase):
    """TC-TRACE-005: Stopping an unknown ref → 404."""
    SPEC = TestSpec(
        tc_id="TC-TRACE-005",
        title="Stopping an unknown trace ref returns 404",
        spec="TS 32.422 §5.6",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative pin for /api/trace/{ref}/stop — stopping a\n"
            "  non-existent trace_ref must 404 rather than silently\n"
            "  succeed or 500. TS 32.422 §5.6 (Trace Deactivation)\n"
            "  must be side-effect-free on unknown refs so OAM tools\n"
            "  can probe state without creating phantom records, and\n"
            "  so a misspelled trace_ref in an automation script can\n"
            "  never silently match an unrelated session by accident.\n"
            "\n"
            "Procedure (TS 32.422 §5.6)\n"
            "  1. POST /api/trace/no-such-ref/stop with no body. The\n"
            "     trace_ref 'no-such-ref' is deliberately synthetic\n"
            "     and never appears in the trace_session table.\n"
            "  2. Capture the HTTP status from the response.\n"
            "  3. Assert status == 404 (not 200, which would silently\n"
            "     pretend a session was stopped; not 500, which would\n"
            "     leak an internal exception trace).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none).\n"
            "\n"
            "Pass criteria\n"
            "  Response HTTP status == 404.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Body shape on 404 is not asserted; only\n"
            "  the status code matters here. Concurrent test runs\n"
            "  must never register a 'no-such-ref' trace."
        ),
    )

    def run(self):
        try:
            r, s = _trace_api("/api/trace/no-such-ref/stop", "POST")
            if s != 404:
                self.fail_test(f"unknown ref stop did not 404: {s} {r}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceExportJSON(TestCase):
    """TC-TRACE-006: JSON export returns Content-Disposition + parseable JSON."""
    SPEC = TestSpec(
        tc_id="TC-TRACE-006",
        title="JSON export returns Content-Disposition + parseable body",
        spec="TS 32.423 §6",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the TS 32.423 §6 trace file-export contract on the\n"
            "  JSON path. The OAM console downloads trace records by\n"
            "  GETting /export/json; the server must emit a Content-\n"
            "  Disposition: attachment header (so the browser saves\n"
            "  rather than renders) carrying the trace_ref, and the\n"
            "  body must be parseable JSON with at least trace_ref +\n"
            "  records fields.\n"
            "\n"
            "Procedure (TS 32.423 §6)\n"
            "  1. POST /api/trace/start with depth=medium, interfaces=\n"
            "     N1,N2,SIP; capture trace_ref.\n"
            "  2. GET /api/trace/{ref}/export/json (raw=True so headers\n"
            "     come back).\n"
            "  3. Assert HTTP 200.\n"
            "  4. Assert 'attachment' in Content-Disposition AND\n"
            "     trace_ref is part of the disposition string.\n"
            "  5. Parse body as JSON; assert doc['trace_ref'] == ref\n"
            "     and 'records' in doc.\n"
            "  6. finally DELETEs the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none).\n"
            "\n"
            "Pass criteria\n"
            "  HTTP 200 AND Content-Disposition is attachment with\n"
            "  trace_ref AND body parses with both keys.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. XML export path is not exercised here;\n"
            "  Content-Disposition filename format is not asserted."
        ),
    )

    def run(self):
        try:
            r, _ = _trace_api("/api/trace/start", "POST",
                              {"depth": "medium", "interfaces": "N1,N2,SIP"})
            ref = r.get("trace_ref")
            body, s, hdrs = _trace_api(f"/api/trace/{ref}/export/json",
                                        raw=True)
            if s != 200:
                self.fail_test(f"json export failed: {s}")
                return self.result
            cd = hdrs.get("Content-Disposition", "")
            if "attachment" not in cd or ref not in cd:
                self.fail_test(f"bad Content-Disposition: {cd!r}")
                return self.result
            doc = json.loads(body.decode())
            if doc.get("trace_ref") != ref or "records" not in doc:
                self.fail_test(f"bad export doc: {doc}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            try:
                _trace_api(f"/api/trace/{ref}", "DELETE")
            except Exception:
                pass
        return self.result


class TraceDelete(TestCase):
    """TC-TRACE-007: DELETE /api/trace/{ref} cascades; subsequent stop → 404."""
    SPEC = TestSpec(
        tc_id="TC-TRACE-007",
        title="DELETE /trace/{ref} cascades; subsequent stop → 404",
        spec="TS 32.422 §5.6",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the FK CASCADE on the trace_session row. Deleting\n"
            "  a session via /api/trace/{ref} must cascade to remove\n"
            "  the records rows; afterwards any /stop call on the\n"
            "  same ref must 404 (no row to stop, no zombie session).\n"
            "  This keeps the TS 32.422 §5.6 lifecycle in lock-step\n"
            "  with the TS 32.423 data store so 'delete-then-stop'\n"
            "  never silently resurrects a dead session.\n"
            "\n"
            "Procedure (TS 32.422 §5.6 + TS 32.423 §5.1)\n"
            "  1. POST /api/trace/start with depth=minimum; capture\n"
            "     trace_ref.\n"
            "  2. DELETE /api/trace/{ref}; assert HTTP 200 and dr.ok\n"
            "     (cascades records).\n"
            "  3. POST /api/trace/{ref}/stop; assert HTTP 404 (no\n"
            "     session row left to operate on).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none).\n"
            "\n"
            "Pass criteria\n"
            "  DELETE returns 200/ok AND subsequent /stop returns 404.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The cascade on records rows is not directly\n"
            "  inspected — the row count check is implicit via 404.\n"
            "  Re-running DELETE on the same ref would also 404."
        ),
    )

    def run(self):
        try:
            r, _ = _trace_api("/api/trace/start", "POST", {"depth": "minimum"})
            ref = r.get("trace_ref")
            dr, ds = _trace_api(f"/api/trace/{ref}", "DELETE")
            if ds != 200 or not dr.get("ok"):
                self.fail_test(f"delete failed: {ds} {dr}")
                return self.result
            # Subsequent stop must 404
            _, ss = _trace_api(f"/api/trace/{ref}/stop", "POST")
            if ss != 404:
                self.fail_test(f"after delete, stop did not 404: {ss}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_TRACE_TCS = [
    TraceStartListStop,
    TraceDepthValidation,
    TraceDuplicateStartIsRejected,
    TraceRecordsEmpty,
    TraceStopUnknown,
    TraceExportJSON,
    TraceDelete,
]

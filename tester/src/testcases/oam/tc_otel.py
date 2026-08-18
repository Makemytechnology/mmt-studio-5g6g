# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: OpenTelemetry observability (W3C Trace Context + TS 28.552 §6).

W3C Trace Context  https://www.w3.org/TR/trace-context/  trace_id (16
                   bytes hex) / span_id (8 bytes hex) / parent linkage.
TS 28.552 §6       PM measurements via OTEL exporters (deferred until
                   the SDK dep is vendored).
TS 28.554 §5       E2E KPIs that map to OTEL traces.

Drives the SA Core REST surface at /api/otel/*: status (config + ring
+ counters), config patch with vocabulary 400s, smoke-test span
emission with parent linkage, ring readback + filters, single-trace
tree, per-(NF, operation) counters, reset.
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

log = logging.getLogger("tester.tc_otel")

OTEL = "/api/otel"


def _api(path, method="GET", body=None):
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


class OtelStatusShape(TestCase):
    """TC-OTEL-001: /status carries config + ring + emitted count."""
    SPEC = TestSpec(
        tc_id="TC-OTEL-001",
        title="OTEL /status carries config + ring + emitted count",
        spec="TS 28.532 §11",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Smoke probe for the OTEL subsystem status endpoint. The\n"
            "  observability dashboard renders config flags + ring buffer\n"
            "  health + emitted-span totals from /api/otel/status; this TC\n"
            "  pins the envelope shape so a regression surfaces here.\n"
            "\n"
            "Procedure (TS 28.532 §11 + OpenTelemetry W3C Trace Context)\n"
            "  1. GET /api/otel/status; assert 200 + ok=True.\n"
            "  2. Extract status = r['status']. Assert these 6 keys are\n"
            "     present: config, ring_size, ring_capacity, spans_emitted,\n"
            "     counter_keys, sdk_vendored.\n"
            "  3. Extract cfg = status['config']. Assert these 7 sub-keys\n"
            "     are present: enabled, metrics_enabled, traces_enabled,\n"
            "     logs_enabled, exporter, endpoint, prometheus_port.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  Envelope ok=True and all 6 top-level + 7 config sub-keys\n"
            "  present. Missing key fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Shape-only — does not assert non-zero\n"
            "  spans_emitted or a specific exporter (exporter='noop' is the\n"
            "  default until the upstream OTEL SDK is vendored into the\n"
            "  build). Safe to interleave with other OTEL TCs since it\n"
            "  is read-only."
        ),
    )

    def run(self):
        try:
            r, s = _api(OTEL + "/status")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"status failed: {s} {r}")
                return self.result
            st = r.get("status", {})
            for k in ("config", "ring_size", "ring_capacity",
                      "spans_emitted", "counter_keys", "sdk_vendored"):
                if k not in st:
                    self.fail_test(f"status missing {k}", got=list(st))
                    return self.result
            cfg = st["config"]
            for k in ("enabled", "metrics_enabled", "traces_enabled",
                      "logs_enabled", "exporter", "endpoint",
                      "prometheus_port"):
                if k not in cfg:
                    self.fail_test(f"config missing {k}", got=list(cfg))
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class OtelConfigPatchValidation(TestCase):
    """TC-OTEL-002: bad exporter / empty patch → 400; valid patch round-trips."""
    SPEC = TestSpec(
        tc_id="TC-OTEL-002",
        title="OTEL config patch validation: bad exporter / empty body → 400",
        spec="TS 28.532 §11",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the config-patch vocabulary on /api/otel/config. Bad\n"
            "  exporter strings and empty patch bodies must produce a clean\n"
            "  400; a valid otel_endpoint patch must round-trip end-to-end\n"
            "  via the config sub-object.\n"
            "\n"
            "Procedure (TS 28.532 §11 OTEL config + OpenTelemetry exporters)\n"
            "  1. PATCH /api/otel/config with {otel_exporter:'BAD'}; assert\n"
            "     400 (exporter not in {noop, otlp_grpc, otlp_http, ...}).\n"
            "  2. PATCH with empty body {}; assert 400.\n"
            "  3. PATCH with otel_endpoint='http://otel-collector.test:4317'\n"
            "     (a valid URI); assert 200 and the response payload's\n"
            "     config.endpoint == the patched value.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — bodies are hard-coded negative and positive fixtures.\n"
            "\n"
            "Pass criteria\n"
            "  Step 1 and 2 return 400; step 3 returns 200 and the patched\n"
            "  endpoint is visible in config.endpoint.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Step 3 mutates global OTEL config — the\n"
            "  endpoint value persists until the next /config patch or\n"
            "  process restart. Concurrent TCs that read the endpoint\n"
            "  would see the patched value until then; for full isolation\n"
            "  another TC should reset endpoint to a known default."
        ),
    )

    def run(self):
        try:
            r, s = _api(OTEL + "/config", "PATCH",
                         {"otel_exporter": "BAD"})
            if s != 400:
                self.fail_test(f"bad exporter did not 400: {s} {r}")
                return self.result

            r, s = _api(OTEL + "/config", "PATCH", {})
            if s != 400:
                self.fail_test(f"empty patch did not 400: {s}")
                return self.result

            # Valid update — flip endpoint and read it back
            target = "http://otel-collector.test:4317"
            r, s = _api(OTEL + "/config", "PATCH",
                         {"otel_endpoint": target})
            if s != 200 or r.get("config", {}).get("endpoint") != target:
                self.fail_test(f"valid patch failed: {s} {r}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class OtelTestSpanEmission(TestCase):
    """TC-OTEL-003: /test-span emits with W3C-format ids + status=ok default."""
    SPEC = TestSpec(
        tc_id="TC-OTEL-003",
        title="/test-span emits W3C-format ids and round-trips via /spans",
        spec="TS 28.532 §11",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Round-trip the OTEL test-span emission path. Validates that\n"
            "  /test-span produces W3C Trace Context-compliant ID lengths\n"
            "  (32-hex trace_id, 16-hex span_id) and that the span persists\n"
            "  in the ring buffer with attributes, events, and a default\n"
            "  status='ok'.\n"
            "\n"
            "Procedure (W3C Trace Context + TS 28.532 §11)\n"
            "  1. POST /api/otel/test-span with nf='amf', operation='tc-\n"
            "     otel-003', attributes={imsi:imsi(embb-bulk,2)},\n"
            "     event_name='step.1', duration_us=17000.\n"
            "  2. Assert 200 + ok=True. Extract tid, sid.\n"
            "  3. Assert tid is 32 chars of lowercase hex (16-byte W3C\n"
            "     trace_id) and sid is 16 chars of lowercase hex (8-byte\n"
            "     span_id).\n"
            "  4. GET /api/otel/spans/{tid}; assert 200 and exactly 1 span\n"
            "     in spans[].\n"
            "  5. Assert span.nf=='amf', span.operation=='tc-otel-003',\n"
            "     span.status=='ok' (default), attributes.imsi==expected,\n"
            "     and at least one event named 'step.1'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — span attributes and IDs are fixed fixtures.\n"
            "\n"
            "Pass criteria\n"
            "  W3C ID lengths match, round-trip yields 1 span with all\n"
            "  attrs, event, and default status preserved.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Ring buffer is shared across TCs; this test\n"
            "  fetches by trace_id so it is collision-safe."
        ),
    )

    def run(self):
        try:
            r, s = _api(OTEL + "/test-span", "POST", {
                "nf": "amf", "operation": "tc-otel-003",
                "attributes": {"imsi": baseline.imsi("embb-bulk", 2)},
                "event_name": "step.1",
                "duration_us": 17000,
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"test-span failed: {s} {r}")
                return self.result
            tid, sid = r.get("trace_id"), r.get("span_id")
            # W3C: 16-byte trace_id (32 hex), 8-byte span_id (16 hex)
            if not tid or len(tid) != 32 or not all(c in "0123456789abcdef" for c in tid):
                self.fail_test(f"bad trace_id: {tid!r}")
                return self.result
            if not sid or len(sid) != 16 or not all(c in "0123456789abcdef" for c in sid):
                self.fail_test(f"bad span_id: {sid!r}")
                return self.result

            # Read back via /spans/{trace_id}
            tr, ts = _api(f"{OTEL}/spans/{tid}")
            if ts != 200 or len(tr.get("spans", [])) != 1:
                self.fail_test(f"trace tree wrong: {ts} {tr}")
                return self.result
            span = tr["spans"][0]
            if span.get("nf") != "amf" or span.get("operation") != "tc-otel-003":
                self.fail_test(f"span attrs wrong: {span}")
                return self.result
            if span.get("status") != "ok":
                self.fail_test(f"default status not ok: {span.get('status')}")
                return self.result
            if span.get("attributes", {}).get("imsi") != baseline.imsi("embb-bulk", 2):
                self.fail_test(f"attribute lost: {span}")
                return self.result
            if not any(e.get("name") == "step.1"
                       for e in span.get("events", [])):
                self.fail_test(f"event missing: {span.get('events')}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class OtelParentSpanLinkage(TestCase):
    """TC-OTEL-004: child span inherits trace_id, links via parent_span_id."""
    SPEC = TestSpec(
        tc_id="TC-OTEL-004",
        title="Child span inherits trace_id, links via parent_span_id",
        spec="TS 28.532 §11",
        domain=Domain.OAM,
        nfs=(NF.AMF, NF.AUSF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin W3C Trace Context parent propagation across NFs. A child\n"
            "  span on a different NF (ausf) must inherit the trace_id of\n"
            "  the parent (amf) span and carry parent_span_id pointing back\n"
            "  to the root, so trace trees reconstruct correctly.\n"
            "\n"
            "Procedure (W3C Trace Context propagation)\n"
            "  1. POST /api/otel/test-span with nf='amf',\n"
            "     operation='registration'. Capture tid, root_sid.\n"
            "  2. POST another /api/otel/test-span with nf='ausf',\n"
            "     operation='ue-auth', parent_trace_id=tid,\n"
            "     parent_span_id=root_sid.\n"
            "  3. Assert child.trace_id == tid (inherited).\n"
            "  4. GET /api/otel/spans/{tid}; assert 2 spans returned.\n"
            "  5. Find the child span (nf=='ausf') and assert its\n"
            "     parent_span_id == root_sid.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — fixed NFs and operations.\n"
            "\n"
            "Pass criteria\n"
            "  Child inherits trace_id, parent linkage is set, both spans\n"
            "  visible under the single trace_id.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. trace_id is W3C 16-byte; if the upstream layer\n"
            "  used 64-bit ids the format check in TC-OTEL-003 would have\n"
            "  caught it first."
        ),
    )

    def run(self):
        try:
            # Root
            root, _ = _api(OTEL + "/test-span", "POST", {
                "nf": "amf", "operation": "registration",
            })
            tid, root_sid = root.get("trace_id"), root.get("span_id")

            # Child on the same trace_id
            child, _ = _api(OTEL + "/test-span", "POST", {
                "nf": "ausf", "operation": "ue-auth",
                "parent_trace_id": tid, "parent_span_id": root_sid,
            })
            if child.get("trace_id") != tid:
                self.fail_test(f"child trace_id != root: {child}")
                return self.result

            # Trace tree should hold both
            tr, _ = _api(f"{OTEL}/spans/{tid}")
            spans = tr.get("spans", [])
            if len(spans) != 2:
                self.fail_test(f"expected 2 spans, got {len(spans)}",
                               trace=spans)
                return self.result
            child_span = next((s for s in spans if s.get("nf") == "ausf"), None)
            if not child_span or child_span.get("parent_span_id") != root_sid:
                self.fail_test(f"parent linkage missing: {child_span}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class OtelFilterAndCounters(TestCase):
    """TC-OTEL-005: /spans filters by nf/operation; /counters reflects."""
    SPEC = TestSpec(
        tc_id="TC-OTEL-005",
        title="OTEL /spans filter + /counters per-(nf,operation)",
        spec="TS 28.532 §11",
        domain=Domain.OAM,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
            description=(
            "Purpose\n"
            "  Pin the /spans query-filter contract and the per-(NF,\n"
            "  operation) counters aggregator. SRE workflows pivot on these\n"
            "  endpoints for trace drill-down + dashboards; both must agree\n"
            "  exactly on the emitted-span totals.\n"
            "\n"
            "Procedure (TS 28.532 §11 OTEL counters)\n"
            "  1. POST /api/otel/reset to zero the ring + counters (makes\n"
            "     the subsequent counts deterministic).\n"
            "  2. POST /api/otel/test-span three times with nf='amf',\n"
            "     operation='registration'.\n"
            "  3. POST /api/otel/test-span once with nf='smf',\n"
            "     operation='pdu-establish'.\n"
            "  4. GET /api/otel/spans?nf=amf&operation=registration;\n"
            "     assert len(spans) == 3.\n"
            "  5. GET /api/otel/counters; assert counters['amf:\n"
            "     registration'] == 3 and counters['smf:pdu-establish']\n"
            "     == 1.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — fixed NFs / operations / counts.\n"
            "\n"
            "Pass criteria\n"
            "  Filter returns exactly 3 amf:registration spans; counters\n"
            "  aggregator shows the same 3/1 split.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Test issues /reset and then 4 emits — concurrent\n"
            "  emitters could inflate the counter; runs in single-process\n"
            "  CI safely."
        ),
    )

    def run(self):
        try:
            # Reset to make counts deterministic.
            _api(OTEL + "/reset", "POST")

            # 3x amf:registration + 1x smf:pdu-establish
            for _ in range(3):
                _api(OTEL + "/test-span", "POST",
                     {"nf": "amf", "operation": "registration"})
            _api(OTEL + "/test-span", "POST",
                 {"nf": "smf", "operation": "pdu-establish"})

            # Filter by nf=amf
            af, _ = _api(OTEL + "/spans?nf=amf&operation=registration")
            if len(af.get("spans", [])) != 3:
                self.fail_test(f"nf+op filter wrong: {len(af.get('spans', []))}")
                return self.result

            # Counters
            cr, _ = _api(OTEL + "/counters")
            cs = cr.get("counters", {})
            if cs.get("amf:registration") != 3:
                self.fail_test(f"amf counter wrong: {cs}")
                return self.result
            if cs.get("smf:pdu-establish") != 1:
                self.fail_test(f"smf counter wrong: {cs}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class OtelTraceNotFound(TestCase):
    """TC-OTEL-006: GET /spans/{unknown_trace_id} → 404."""
    SPEC = TestSpec(
        tc_id="TC-OTEL-006",
        title="GET /spans/{unknown_trace_id} returns 404",
        spec="TS 28.532 §11",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  API hygiene gate for unknown trace_id lookups on the OTEL\n"
            "  trace store. A GET on a trace_id that has never been\n"
            "  emitted must surface a clean RFC 9110 404, never a 200 with\n"
            "  an empty body or a 500 from a downstream nil-pointer\n"
            "  dereference in the trace tree builder. The dashboard relies\n"
            "  on the 404 to render a clean 'trace not found' tile.\n"
            "\n"
            "Procedure (RFC 9110 + W3C Trace Context unknown lookup)\n"
            "  1. Construct an unminted trace_id by repeating 'f' 32 times\n"
            "     (a valid W3C-format 16-byte hex string — len and charset\n"
            "     pass — but never actually emitted into the ring).\n"
            "  2. GET /api/otel/spans/{unknown_tid}.\n"
            "  3. Assert HTTP status == 404 exactly. Capture status code\n"
            "     in failure message if other.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — unknown id is hard-coded ('f' * 32).\n"
            "\n"
            "Pass criteria\n"
            "  Endpoint returns exactly 404. Any other status (200 with\n"
            "  empty list, 500 from null deref, 400 from input validator\n"
            "  over-reach) fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Pure negative path — side-effect-free and safe\n"
            "  to run concurrently with other OTEL suites."
        ),
    )

    def run(self):
        try:
            _, s = _api(f"{OTEL}/spans/" + "f" * 32)
            if s != 404:
                self.fail_test(f"unknown trace_id did not 404: {s}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class OtelReset(TestCase):
    """TC-OTEL-007: /reset zeroes ring + counters."""
    SPEC = TestSpec(
        tc_id="TC-OTEL-007",
        title="OTEL /reset zeroes ring buffer + counters",
        spec="TS 28.532 §11",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the operator-driven reset path. /api/otel/reset must\n"
            "  fully purge both the ring buffer (drops accumulated spans)\n"
            "  and the per-(NF, operation) counter map. Downstream SRE\n"
            "  tooling depends on this for between-test isolation.\n"
            "\n"
            "Procedure (TS 28.532 §11 OTEL reset)\n"
            "  1. POST /api/otel/test-span with nf='test',\n"
            "     operation='tc-otel-007' to seed at least one span.\n"
            "  2. POST /api/otel/reset.\n"
            "  3. GET /api/otel/status; assert status.ring_size == 0 and\n"
            "     status.spans_emitted == 0.\n"
            "  4. GET /api/otel/counters; assert len(counters) == 0.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — fixed seed/reset/read sequence.\n"
            "\n"
            "Pass criteria\n"
            "  Post-reset ring_size and spans_emitted are both 0 and the\n"
            "  counters dict is empty.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Reset is a global mutator — running this in\n"
            "  parallel with other OTEL TCs that depend on ring state\n"
            "  (e.g. TC-OTEL-005 filter-and-counters) would erase their\n"
            "  state mid-flight. Best run as part of a serialised OTEL\n"
            "  test sweep."
        ),
    )

    def run(self):
        try:
            _api(OTEL + "/test-span", "POST",
                 {"nf": "test", "operation": "tc-otel-007"})
            _api(OTEL + "/reset", "POST")
            r, _ = _api(OTEL + "/status")
            st = r.get("status", {})
            if st.get("ring_size") != 0:
                self.fail_test(f"ring not zero after reset: {st}")
                return self.result
            if st.get("spans_emitted") != 0:
                self.fail_test(f"emitted not zero after reset: {st}")
                return self.result
            cr, _ = _api(OTEL + "/counters")
            if len(cr.get("counters", {})) != 0:
                self.fail_test(f"counters not empty: {cr}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class OtelStatusError(TestCase):
    """TC-OTEL-008: explicit status='error' is preserved on the span."""
    SPEC = TestSpec(
        tc_id="TC-OTEL-008",
        title="Explicit span status=error is preserved across round-trip",
        spec="TS 28.532 §11",
        domain=Domain.OAM,
        nfs=(NF.SMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin explicit-status preservation on the emit path. When a\n"
            "  span is posted with status='error' the stored span must\n"
            "  retain that value verbatim — no silent coercion to the\n"
            "  default 'ok'. Critical for error-trace dashboards.\n"
            "\n"
            "Procedure (W3C Trace Context + TS 28.532 §11 statuses)\n"
            "  1. POST /api/otel/test-span with nf='smf',\n"
            "     operation='tc-otel-008', status='error'. Capture\n"
            "     trace_id (tid).\n"
            "  2. GET /api/otel/spans/{tid}; assert span = spans[0]\n"
            "     present.\n"
            "  3. Assert span.status == 'error' (not coerced to default\n"
            "     'ok').\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — fixed NF/operation and status='error' body.\n"
            "\n"
            "Pass criteria\n"
            "  Round-trip span carries status='error' exactly.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Status field uses OpenTelemetry status\n"
            "  vocabulary; non-{ok, error, unset} values may be rejected\n"
            "  by future validators. This TC only round-trips one explicit\n"
            "  'error' value — full enumeration coverage belongs to\n"
            "  dedicated parameterised tests."
        ),
    )

    def run(self):
        try:
            r, _ = _api(OTEL + "/test-span", "POST", {
                "nf": "smf", "operation": "tc-otel-008",
                "status": "error",
            })
            tid = r.get("trace_id")
            tr, _ = _api(f"{OTEL}/spans/{tid}")
            sp = tr.get("spans", [{}])[0]
            if sp.get("status") != "error":
                self.fail_test(f"status not preserved: {sp}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_OTEL_TCS = [
    OtelStatusShape,
    OtelConfigPatchValidation,
    OtelTestSpanEmission,
    OtelParentSpanLinkage,
    OtelFilterAndCounters,
    OtelTraceNotFound,
    OtelReset,
    OtelStatusError,
]

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: trace correlation index (NGAP ↔ SBI ↔ PFCP bridge).

TS 29.500 §6.10.2.5  3gpp-Sbi-Correlation-Info.
TS 23.502 §4.4.1.2   N4 SEID pairs (PDU session bind).
TS 38.413            AMF-UE-NGAP-ID / RAN-UE-NGAP-ID.
TS 32.422            Subscriber Trace; ngap_trace_ref linkage.

The correlation table is the single pivot the operator panel uses
to walk from one transport identifier to every other ID tied to the
same UE call. These TCs pin the round-trip behaviour: register, look
up by each natural key, UPSERT on second register, 404 on unknown
call_id, list/limit, delete, reset.
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

log = logging.getLogger("tester.tc_trace_correlation")

CORR = "/api/trace/correlation"


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


class TraceCorrRegisterAndLookupByIMSI(TestCase):
    """TC-TRC-001: register row, look up by IMSI."""
    SPEC = TestSpec(
        tc_id="TC-TRC-001",
        title="Trace correlation register + lookup by IMSI",
        spec="TS 32.422 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the trace-correlation register-then-lookup-by-IMSI\n"
            "  round-trip. TS 32.422 §5 (Trace Session Activation) ties\n"
            "  a subscriber identity to a Trace Recording Session; the\n"
            "  correlation table is the single pivot OAM uses to walk\n"
            "  from IMSI → NGAP / SBI / PFCP IDs on the same call. By-\n"
            "  IMSI lookup is the most common operator query.\n"
            "\n"
            "Procedure (TS 32.422 §5 + TS 28.532 trace-management)\n"
            "  1. POST /api/trace/correlation/reset — wipes prior rows\n"
            "     so the lookup count is deterministic.\n"
            "  2. POST /api/trace/correlation with imsi=baseline.imsi\n"
            "     (embb-bulk,0), amf_ue_ngap_id=4001, gnb_id=gnb-001.\n"
            "  3. Assert HTTP 200 and non-empty r.call_id.\n"
            "  4. GET /api/trace/correlation/by/imsi/{imsi}.\n"
            "  5. Assert HTTP 200, exactly 1 row returned, with the\n"
            "     same call_id from step 3.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — body fields hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Register returns call_id AND by-IMSI lookup returns one\n"
            "  row carrying that exact call_id.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE so baseline.imsi(embb-bulk,0) resolves.\n"
            "  /reset is destructive — assumed safe in test contexts."
        ),
    )

    def run(self):
        try:
            _api(CORR + "/reset", "POST")
            imsi = baseline.imsi("embb-bulk", 0)
            r, s = _api(CORR, "POST", {
                "imsi": imsi,
                "amf_ue_ngap_id": 4001,
                "gnb_id": "gnb-001",
            })
            if s != 200 or not r.get("call_id"):
                self.fail_test(f"register failed: {s} {r}")
                return self.result
            cid = r["call_id"]

            r2, s2 = _api(CORR + "/by/imsi/" + imsi)
            rows = r2.get("correlations", [])
            if s2 != 200 or len(rows) != 1 or rows[0].get("call_id") != cid:
                self.fail_test(f"imsi lookup wrong: {s2} {r2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrLookupByAmfNgapID(TestCase):
    """TC-TRC-002: lookup by amf_ue_ngap_id (TS 38.413)."""
    SPEC = TestSpec(
        tc_id="TC-TRC-002",
        title="Trace correlation lookup by AMF-UE-NGAP-ID",
        spec="TS 38.413 §8",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the NGAP-leg lookup index on the correlation table.\n"
            "  TS 38.413 §8 defines AMF-UE-NGAP-ID as the AMF-assigned\n"
            "  identifier of a UE association (Stage-3 INTEGER 0..2^40-\n"
            "  1); this is the key the OAM panel uses to walk from a\n"
            "  captured NGAP message back to the UE's full call\n"
            "  envelope (IMSI / PFCP SEIDs / OTEL trace) on the\n"
            "  correlation row.\n"
            "\n"
            "Procedure (TS 38.413 §8 + TS 32.422 §5)\n"
            "  1. POST /api/trace/correlation/reset.\n"
            "  2. POST /api/trace/correlation with imsi=baseline.imsi\n"
            "     (embb-bulk,1), amf_ue_ngap_id=4242. Capture call_id.\n"
            "  3. GET /api/trace/correlation/by/amf-ue-ngap-id/4242.\n"
            "  4. Assert HTTP 200, exactly 1 row, with the captured\n"
            "     call_id.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — body fields hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  By-amf-ue-ngap-id lookup returns one row with the\n"
            "  matching call_id.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. RAN-UE-NGAP-ID side of the NGAP pair is\n"
            "  pinned separately; this only exercises the AMF side."
        ),
    )

    def run(self):
        try:
            _api(CORR + "/reset", "POST")
            r, _ = _api(CORR, "POST", {
                "imsi": baseline.imsi("embb-bulk", 1),
                "amf_ue_ngap_id": 4242,
            })
            cid = r.get("call_id")
            r2, s2 = _api(CORR + "/by/amf-ue-ngap-id/4242")
            rows = r2.get("correlations", [])
            if s2 != 200 or len(rows) != 1 or rows[0].get("call_id") != cid:
                self.fail_test(f"amf-ngap lookup wrong: {s2} {r2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrLookupBySEID(TestCase):
    """TC-TRC-003: lookup by SEID — PFCP bridge (TS 23.502 §4.4.1.2)."""
    SPEC = TestSpec(
        tc_id="TC-TRC-003",
        title="Trace correlation lookup by PFCP SEID (up + cp)",
        spec="TS 23.502 §4.4.1.2",
        domain=Domain.OAM,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the PFCP-leg lookup. TS 23.502 §4.4.1.2 defines\n"
            "  the SMF→UPF SEID pair (CP-side seid_cp + UP-side seid_\n"
            "  up) that binds a PDU session at the N4 interface. Both\n"
            "  SEIDs must resolve via the same /by/seid endpoint to\n"
            "  the same call_id — letting OAM walk a captured PFCP\n"
            "  request or response back to the originating UE.\n"
            "\n"
            "Procedure (TS 23.502 §4.4.1.2 + TS 29.244 PFCP)\n"
            "  1. POST /api/trace/correlation/reset.\n"
            "  2. POST /api/trace/correlation with imsi=baseline.imsi\n"
            "     (embb-bulk,2), seid_up=7777, seid_cp=8888, teid_dl=\n"
            "     100, teid_ul=200. Capture call_id.\n"
            "  3. GET /by/seid/7777; assert exactly 1 row with call_id.\n"
            "  4. GET /by/seid/8888; assert exactly 1 row with call_id\n"
            "     (the OR clause matches either column).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed SEID/TEID values).\n"
            "\n"
            "Pass criteria\n"
            "  Both /by/seid/{seid_up} and /by/seid/{seid_cp} return\n"
            "  exactly one row carrying the same call_id.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. teid_dl / teid_ul are written but not\n"
            "  individually re-looked-up here."
        ),
    )

    def run(self):
        try:
            _api(CORR + "/reset", "POST")
            r, _ = _api(CORR, "POST", {
                "imsi": baseline.imsi("embb-bulk", 2),
                "seid_up": 7777,
                "seid_cp": 8888,
                "teid_dl": 100, "teid_ul": 200,
            })
            cid = r.get("call_id")
            r2, _ = _api(CORR + "/by/seid/7777")
            rows = r2.get("correlations", [])
            if len(rows) != 1 or rows[0].get("call_id") != cid:
                self.fail_test(f"seid_up lookup wrong: {r2}")
                return self.result
            # seid_cp also resolves to the same call_id (OR clause).
            r3, _ = _api(CORR + "/by/seid/8888")
            rows3 = r3.get("correlations", [])
            if len(rows3) != 1 or rows3[0].get("call_id") != cid:
                self.fail_test(f"seid_cp lookup wrong: {r3}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrLookupByOtelTraceID(TestCase):
    """TC-TRC-004: lookup by OTEL trace_id closes the W3C↔3GPP loop."""
    SPEC = TestSpec(
        tc_id="TC-TRC-004",
        title="Trace correlation lookup by W3C OTEL trace_id",
        spec="TS 32.422 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Closes the W3C ↔ 3GPP correlation loop. TS 29.500\n"
            "  §6.10.2.5 binds the 3gpp-Sbi-Correlation-Info header to\n"
            "  in-band SBI tracing; pairing that with a W3C-format\n"
            "  otel_trace_id (32-hex) lets a vendor observability\n"
            "  pipeline pivot from an APM span back to the 3GPP call\n"
            "  via the trace-correlation table — the only place where\n"
            "  W3C trace IDs are pinned to subscriber identifiers in\n"
            "  the operator dataplane.\n"
            "\n"
            "Procedure (TS 29.500 §6.10.2.5 + TS 32.422 §5)\n"
            "  1. POST /api/trace/correlation/reset.\n"
            "  2. POST /api/trace/correlation with imsi=baseline.imsi\n"
            "     (embb-bulk,3), otel_trace_id='abcdef0123456789abcdef\n"
            "     0123456789' (32-hex W3C trace_id). Capture call_id.\n"
            "  3. GET /api/trace/correlation/by/otel-trace-id/{tid}.\n"
            "  4. Assert exactly 1 row with call_id matching step 2.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — tid hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  By-otel-trace-id lookup returns exactly one row with\n"
            "  the registered call_id.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. otel_span_id (16-hex per-hop) is not\n"
            "  exercised — only the trace-level pivot."
        ),
    )

    def run(self):
        try:
            _api(CORR + "/reset", "POST")
            tid = "abcdef0123456789abcdef0123456789"
            r, _ = _api(CORR, "POST", {
                "imsi": baseline.imsi("embb-bulk", 3),
                "otel_trace_id": tid,
            })
            cid = r.get("call_id")
            r2, _ = _api(CORR + "/by/otel-trace-id/" + tid)
            rows = r2.get("correlations", [])
            if len(rows) != 1 or rows[0].get("call_id") != cid:
                self.fail_test(f"otel-trace-id lookup wrong: {r2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrUpsertPreservesCallID(TestCase):
    """TC-TRC-005: second register on same natural key updates in place."""
    SPEC = TestSpec(
        tc_id="TC-TRC-005",
        title="Second register on the same natural key UPSERTs in place",
        spec="TS 32.422 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins UPSERT semantics on the correlation row. A real\n"
            "  call writes its AMF leg first (NGAP-ID arrives on N1/N2)\n"
            "  then its SMF leg later (SEID + TEIDs on N11/N4). The\n"
            "  table must MERGE the second write into the first row on\n"
            "  matching IMSI rather than spawn a fresh call_id —\n"
            "  otherwise OAM would see two half-rows per call.\n"
            "\n"
            "Procedure (TS 32.422 §5 + TS 23.502 §4.4.1.2)\n"
            "  1. POST /api/trace/correlation/reset.\n"
            "  2. POST /correlation with imsi=baseline.imsi(embb-\n"
            "     bulk,4), amf_ue_ngap_id=5005. Capture cid1.\n"
            "  3. POST /correlation with the same imsi, seid_up=1234,\n"
            "     teid_dl=9001, teid_ul=9002. Capture cid2.\n"
            "  4. Assert cid1 == cid2 (no new row).\n"
            "  5. Pull r2.row; assert amf_ue_ngap_id == 5005 was\n"
            "     preserved and seid_up == 1234 was merged in.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IDs hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Same call_id on both writes AND row carries fields from\n"
            "  both AMF and SMF legs.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Natural-key collision on IMSI is the\n"
            "  UPSERT pivot; SEID-collision UPSERT not exercised here."
        ),
    )

    def run(self):
        try:
            _api(CORR + "/reset", "POST")
            # AMF leg writes IMSI + AMF-UE-NGAP-ID first.
            r1, _ = _api(CORR, "POST", {
                "imsi": baseline.imsi("embb-bulk", 4),
                "amf_ue_ngap_id": 5005,
            })
            cid1 = r1.get("call_id")
            # SMF leg later attaches SEID + TEIDs against the same IMSI.
            r2, _ = _api(CORR, "POST", {
                "imsi": baseline.imsi("embb-bulk", 4),
                "seid_up": 1234,
                "teid_dl": 9001, "teid_ul": 9002,
            })
            cid2 = r2.get("call_id")
            if cid1 != cid2:
                self.fail_test(f"upsert created new call_id: {cid1} vs {cid2}")
                return self.result
            row = r2.get("row", {})
            if row.get("amf_ue_ngap_id") != 5005:
                self.fail_test(f"amf_ue_ngap_id not preserved: {row}")
                return self.result
            if row.get("seid_up") != 1234:
                self.fail_test(f"seid_up not merged: {row}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrUnknownCallID404(TestCase):
    """TC-TRC-006: GET /correlation/{unknown} → 404, DELETE → 404."""
    SPEC = TestSpec(
        tc_id="TC-TRC-006",
        title="Unknown call_id GET / DELETE both return 404",
        spec="TS 32.422 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative pin for the correlation /{call_id} surface. An\n"
            "  unknown call_id must 404 on both GET and DELETE — never\n"
            "  return empty 200 (which would be misleading), never\n"
            "  500 (which would leak internals), and never silently\n"
            "  create a row (which would let an attacker enumerate\n"
            "  IDs). Pins side-effect-free behaviour on the TS 32.422\n"
            "  §5 trace-management API per TS 28.532 service\n"
            "  conventions.\n"
            "\n"
            "Procedure (TS 32.422 §5 + TS 28.532)\n"
            "  1. GET /api/trace/correlation/call-deadbeef (synthetic\n"
            "     call_id deliberately outside any natural-key write).\n"
            "  2. Capture HTTP status.\n"
            "  3. Assert status == 404.\n"
            "  4. DELETE /api/trace/correlation/call-deadbeef.\n"
            "  5. Assert status == 404.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — synthetic call_id hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both GET and DELETE on the unknown call_id return 404.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Body content of the 404 is not asserted.\n"
            "  Concurrent runs must never register 'call-deadbeef'."
        ),
    )

    def run(self):
        try:
            _, s = _api(CORR + "/call-deadbeef")
            if s != 404:
                self.fail_test(f"unknown call_id GET did not 404: {s}")
                return self.result
            _, s2 = _api(CORR + "/call-deadbeef", "DELETE")
            if s2 != 404:
                self.fail_test(f"unknown call_id DELETE did not 404: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrListAndLimit(TestCase):
    """TC-TRC-007: list returns rows newest-first, ?limit= caps it."""
    SPEC = TestSpec(
        tc_id="TC-TRC-007",
        title="Correlation list returns newest-first, ?limit= caps rows",
        spec="TS 32.422 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the listing order + limit cap on the correlation\n"
            "  table. The OAM panel paginates the live-calls view with\n"
            "  ?limit= and assumes newest-first ordering so an operator\n"
            "  watching the page sees their last test rise to the top.\n"
            "  This TC writes 5 deterministic rows and verifies the\n"
            "  cap and ordering both hold.\n"
            "\n"
            "Procedure (TS 32.422 §5 + TS 28.532 trace-management)\n"
            "  1. POST /api/trace/correlation/reset.\n"
            "  2. POST 5 rows with amf_ue_ngap_id = 7000..7004,\n"
            "     each with a fresh baseline IMSI (offset 69+i).\n"
            "  3. GET /api/trace/correlation?limit=3.\n"
            "  4. Assert exactly 3 rows returned.\n"
            "  5. Assert rows[0].amf_ue_ngap_id == 7004 (newest of\n"
            "     the five we just wrote rises to the top).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — limit hard-coded; row count fixed).\n"
            "\n"
            "Pass criteria\n"
            "  len(rows) == 3 AND rows[0].amf_ue_ngap_id == 7004.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE for baseline IMSIs. Ordering uses NGAP\n"
            "  ID as a wall-clock proxy because they monotonically\n"
            "  increase across the 5 writes."
        ),
    )

    def run(self):
        try:
            _api(CORR + "/reset", "POST")
            for i in range(5):
                _api(CORR, "POST", {
                    "imsi": baseline.imsi("embb-bulk", 69 + i),
                    "amf_ue_ngap_id": 7000 + i,
                })
            r, _ = _api(CORR + "?limit=3")
            rows = r.get("correlations", [])
            if len(rows) != 3:
                self.fail_test(f"limit=3 returned {len(rows)} rows")
                return self.result
            # Newest first: highest amf_ue_ngap_id should land on top.
            if rows[0].get("amf_ue_ngap_id") != 7004:
                self.fail_test(f"order wrong: {rows[0]}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrDeleteAndReset(TestCase):
    """TC-TRC-008: DELETE one row + /reset wipes all."""
    SPEC = TestSpec(
        tc_id="TC-TRC-008",
        title="Correlation single-row DELETE and /reset wipe all",
        spec="TS 32.422 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins both per-row DELETE and global /reset on the\n"
            "  correlation table. Per-row DELETE supports surgical\n"
            "  removal (e.g. expunge a single call's correlation) and\n"
            "  must be followed by a 404 on GET; /reset is the OAM\n"
            "  big-red-button that wipes the table back to zero rows.\n"
            "  Both are essential lifecycle operations on TS 32.422 §5\n"
            "  trace management.\n"
            "\n"
            "Procedure (TS 32.422 §5 + TS 28.532)\n"
            "  1. POST /api/trace/correlation/reset.\n"
            "  2. POST /correlation with imsi=baseline.imsi(embb-\n"
            "     bulk,7). Capture call_id.\n"
            "  3. DELETE /correlation/{call_id}; assert HTTP 200.\n"
            "  4. GET /correlation/{call_id}; assert HTTP 404.\n"
            "  5. POST 3 more rows (slots 79..81).\n"
            "  6. POST /correlation/reset.\n"
            "  7. GET /correlation; assert r.count == 0.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — counts and slots hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Per-row DELETE returns 200, follow-up GET is 404, AND\n"
            "  /reset drives listing count to 0.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE for baseline IMSIs. /reset is global —\n"
            "  cooperatively destructive when other tests run in\n"
            "  parallel."
        ),
    )

    def run(self):
        try:
            _api(CORR + "/reset", "POST")
            r, _ = _api(CORR, "POST", {"imsi": baseline.imsi("embb-bulk", 7)})
            cid = r.get("call_id")
            _, s = _api(CORR + "/" + cid, "DELETE")
            if s != 200:
                self.fail_test(f"DELETE failed: {s}")
                return self.result
            _, s2 = _api(CORR + "/" + cid)
            if s2 != 404:
                self.fail_test(f"row still present after DELETE: {s2}")
                return self.result
            # Populate, then reset.
            for i in range(3):
                _api(CORR, "POST", {"imsi": baseline.imsi("embb-bulk", 79 + i)})
            _api(CORR + "/reset", "POST")
            r3, _ = _api(CORR)
            if r3.get("count", -1) != 0:
                self.fail_test(f"reset did not clear: {r3}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrNoNaturalKey400(TestCase):
    """TC-TRC-009: empty body / no natural key → 400."""
    SPEC = TestSpec(
        tc_id="TC-TRC-009",
        title="Correlation register with no natural key returns 400",
        spec="TS 32.422 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative pin for the natural-key requirement on the\n"
            "  correlation register endpoint. A correlation row exists\n"
            "  to bind a UE call's identifiers together; without any\n"
            "  identifying key (IMSI / NGAP-ID / SEID / TEID / OTEL\n"
            "  trace_id) the row would be an orphan with no lookup\n"
            "  surface, and the OAM panel would render a phantom call\n"
            "  with only metadata (gnb_id) and no subscriber binding.\n"
            "  The route must reject such writes with HTTP 400 — this\n"
            "  TC pins that contract.\n"
            "\n"
            "Procedure (TS 32.422 §5 + TS 28.532)\n"
            "  1. POST /api/trace/correlation with body {gnb_id:\n"
            "     'gnb-orphan'} — no natural key whatsoever (no\n"
            "     IMSI, no NGAP-ID, no SEID/TEID, no otel_trace_id).\n"
            "  2. Capture the HTTP status code.\n"
            "  3. Assert HTTP 400 (orphan write rejected).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payload hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Response status == 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The exact list of recognised natural keys\n"
            "  is not enumerated by this test."
        ),
    )

    def run(self):
        try:
            _, s = _api(CORR, "POST", {"gnb_id": "gnb-orphan"})
            if s != 400:
                self.fail_test(f"orphan register did not 400: {s}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class TraceCorrRecordHasMandatoryFields(TestCase):
    """TC-TRC-010: trace record carries the mandatory TS 32.423 fields."""
    SPEC = TestSpec(
        tc_id="TC-TRC-010",
        title="Trace correlation record carries mandatory TS 32.423 fields",
        spec="TS 32.422 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MINOR,
        tags=("conformance", "trace-record"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the mandatory TS 32.423 §5.1 trace-record field\n"
            "  set on a correlation row. The OAM trace-record viewer\n"
            "  relies on imsi + amf_ue_ngap_id + gnb_id being present\n"
            "  to render the call summary; missing any one would\n"
            "  break the panel. Full PCAP field-level validation\n"
            "  (timestamp / direction / message-type) lives in the\n"
            "  matching robot scenario (30_trace.robot::TC-TRC-010).\n"
            "\n"
            "Procedure (TS 32.423 §5.1 + TS 32.422 §5)\n"
            "  1. POST /api/trace/correlation/reset.\n"
            "  2. POST /correlation with imsi=baseline.imsi(embb-\n"
            "     bulk,10), amf_ue_ngap_id=7010, gnb_id=gnb-trc-010.\n"
            "  3. Assert HTTP 200 and r.call_id.\n"
            "  4. Pull r.row; assert each of imsi, amf_ue_ngap_id,\n"
            "     gnb_id is non-empty.\n"
            "  5. Assert row.imsi == the IMSI written in step 2.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IDs hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Register returns call_id AND row carries all three\n"
            "  mandatory fields AND row.imsi round-trips.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  call_id, imsi, amf_ue_ngap_id, gnb_id.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Falls through to a pending-message\n"
            "  fail_test if the core does not return call_id (legacy\n"
            "  pre-feature-flag path)."
        ),
    )

    def run(self):
        try:
            _api(CORR + "/reset", "POST")
            imsi = baseline.imsi("embb-bulk", 10)
            r, s = _api(CORR, "POST", {
                "imsi": imsi,
                "amf_ue_ngap_id": 7010,
                "gnb_id": "gnb-trc-010",
            })
            if s != 200 or not r.get("call_id"):
                self.fail_test(
                    "Python implementation pending — see "
                    "robot/suites/regulatory/30_trace.robot::TC-TRC-010 "
                    "for the procedure.",
                    response=r, status=s)
                return self.result
            row = r.get("row") or {}
            for k in ("imsi", "amf_ue_ngap_id", "gnb_id"):
                if not row.get(k):
                    self.fail_test(f"mandatory field {k} missing", row=row)
                    return self.result
            if row.get("imsi") != imsi:
                self.fail_test(f"IMSI mismatch: {row.get('imsi')!r} vs "
                               f"{imsi!r}")
                return self.result
            self.pass_test(call_id=r.get("call_id"), imsi=row.get("imsi"),
                           amf_ue_ngap_id=row.get("amf_ue_ngap_id"),
                           gnb_id=row.get("gnb_id"))
        except Exception as e:
            self.fail_test(
                "Python implementation pending — see "
                "robot/suites/regulatory/30_trace.robot::TC-TRC-010 "
                "for the procedure.",
                error=str(e))
        return self.result


ALL_TRACE_CORR_TCS = [
    TraceCorrRegisterAndLookupByIMSI,
    TraceCorrLookupByAmfNgapID,
    TraceCorrLookupBySEID,
    TraceCorrLookupByOtelTraceID,
    TraceCorrUpsertPreservesCallID,
    TraceCorrUnknownCallID404,
    TraceCorrListAndLimit,
    TraceCorrDeleteAndReset,
    TraceCorrNoNaturalKey400,
    TraceCorrRecordHasMandatoryFields,
]

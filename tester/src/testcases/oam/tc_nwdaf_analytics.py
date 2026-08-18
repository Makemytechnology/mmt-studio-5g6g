# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NWDAF Analytics (TS 23.288 §6.1).

TS 23.288 §6.1   Procedures for analytics exposure (umbrella).
TS 23.288 §6.1.1 Analytics Subscribe / Unsubscribe.
TS 23.288 §6.1.2 Analytics Request (one-shot Nnwdaf_AnalyticsInfo).
TS 23.288 §6.1.3 Contents of Analytics Exposure (statistics +
                  predictions, with confidence per prediction).
TS 23.288 §6.2   Procedures for Data Collection (the periodic loop
                  that feeds dataCache).

Drives the SA Core REST surface at /api/nwdaf/*: dashboard aggregator
(every Analytics ID at once), per-ID one-shot, subscription CRUD,
recent-results history, service status. Endpoints return `{ok, ...}`
matching templates/nwdaf.html (`d.ok && d.analytics.NF_LOAD …`).
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_nwdaf_analytics")


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


# Per TS 23.288 §6.1 catalogue + the runtime's analytics.ValidAnalyticsIDs
EXPECTED_IDS = (
    "NF_LOAD",            # §6.5
    "UE_MOBILITY",        # §6.7.2
    "UE_COMMUNICATION",   # §6.7.3
    "QOS_SUSTAINABILITY", # §6.9
    "ABNORMAL_BEHAVIOUR", # §6.7.5
    "PDU_SESSION",
    "SLICE_LOAD",         # §6.3
)


class NwdafDashboardShape(TestCase):
    """TC-NWDAF-A-001: /api/nwdaf/analytics returns every supported ID."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-001",
        title="/api/nwdaf/analytics returns every supported analytics ID",
        spec="TS 23.288 §6.1",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Smoke-tests the NWDAF dashboard aggregator endpoint. Pins\n"
            "  TS 23.288 §6.1 — the analytics-exposure catalogue must\n"
            "  surface every supported analytics ID on a single GET, each\n"
            "  carrying the §6.1.3 (result + confidence) envelope. The\n"
            "  ordered supported_ids field is used by the operator UI to\n"
            "  render the panel layout deterministically.\n"
            "\n"
            "Procedure (TS 23.288 §6.1 + §6.1.3)\n"
            "  1. GET /api/nwdaf/analytics (no params).\n"
            "  2. Assert HTTP 200 and r.ok == True.\n"
            "  3. For each id in EXPECTED_IDS (NF_LOAD, UE_MOBILITY,\n"
            "     UE_COMMUNICATION, QOS_SUSTAINABILITY, ABNORMAL_BEHAVIOUR,\n"
            "     PDU_SESSION, SLICE_LOAD) verify it is a key in\n"
            "     r['analytics'] and carries 'result' and 'confidence'.\n"
            "  4. Verify r['supported_ids'] equals list(EXPECTED_IDS) in\n"
            "     exact order so the UI never reshuffles.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — uses the dashboard's all-IDs default).\n"
            "\n"
            "Pass criteria\n"
            "  Every EXPECTED_IDS entry present with §6.1.3 fields AND\n"
            "  supported_ids ordering matches list(EXPECTED_IDS).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() called without kwargs; result implicit).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no UEs needed; aggregator computes from\n"
            "  cached data points and emits zero-confidence rows when\n"
            "  no §6.2 data has been ingested yet."
        ),
    )

    def run(self):
        try:
            r, s = _api("/api/nwdaf/analytics")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"dashboard failed: {s} {r}")
                return self.result
            a = r.get("analytics", {})
            for aid in EXPECTED_IDS:
                if aid not in a:
                    self.fail_test(f"missing {aid}", got=list(a))
                    return self.result
                # §6.1.3 mandates a 'result' map and a 'confidence'
                row = a[aid]
                if "result" not in row or "confidence" not in row:
                    self.fail_test(f"{aid} missing result/confidence: {row}")
                    return self.result
            if r.get("supported_ids") != list(EXPECTED_IDS):
                self.fail_test(
                    f"supported_ids ordering mismatch: {r.get('supported_ids')}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafSingleAnalytic(TestCase):
    """TC-NWDAF-A-002: per-ID GET (TS 23.288 §6.1.2 Analytics Request)."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-002",
        title="Per-ID GET — Analytics Request (Nnwdaf_AnalyticsInfo)",
        spec="TS 23.288 §6.1.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the per-ID one-shot Nnwdaf_AnalyticsInfo request path\n"
            "  (TS 23.288 §6.1.2 / TS 29.520 §5.3). Unlike the dashboard\n"
            "  aggregator, this hits a single analytics ID with a custom\n"
            "  observation window and must round-trip the ID echo plus\n"
            "  the §6.1.3 result + confidence envelope.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.2 + §6.1.3)\n"
            "  1. GET /api/nwdaf/analytics/UE_MOBILITY?window_sec=120.\n"
            "  2. Assert HTTP 200 and r.ok == True.\n"
            "  3. Pull r['result'] and assert analytics_id echoes\n"
            "     UE_MOBILITY (consumer would otherwise route to the\n"
            "     wrong handler).\n"
            "  4. Assert 'result' and 'confidence' keys are present in\n"
            "     the result block per §6.1.3.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — window_sec=120 is hard-coded to exercise the\n"
            "  query-param path).\n"
            "\n"
            "Pass criteria\n"
            "  analytics_id == 'UE_MOBILITY' AND 'result' in res AND\n"
            "  'confidence' in res.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Confidence may be 0.0 when no AMF mobility\n"
            "  events have been ingested in the requested window."
        ),
    )

    def run(self):
        try:
            r, s = _api("/api/nwdaf/analytics/UE_MOBILITY?window_sec=120")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"single fetch failed: {s} {r}")
                return self.result
            res = r.get("result", {})
            if res.get("analytics_id") != "UE_MOBILITY":
                self.fail_test(f"wrong id: {res}")
                return self.result
            if "result" not in res or "confidence" not in res:
                self.fail_test(f"§6.1.3 fields missing: {res}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafBadAnalyticID(TestCase):
    """TC-NWDAF-A-003: unknown analytics_id → 400 (TS 23.288 §6.1)."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-003",
        title="Unknown analytics_id returns 400",
        spec="TS 23.288 §6.1",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative pin for the analytics-ID allow-list. TS 23.288\n"
            "  §6.1 enumerates the valid Analytics IDs as a closed set\n"
            "  (NF_LOAD, UE_MOBILITY, UE_COMMUNICATION, QOS_SUSTAIN-\n"
            "  ABILITY, ABNORMAL_BEHAVIOUR, PDU_SESSION, SLICE_LOAD).\n"
            "  Anything outside this set must be rejected at the route\n"
            "  layer with HTTP 400 so consumers never receive vacuous\n"
            "  responses or leak internal exception strings (which\n"
            "  would be both a UX papercut and a security signal).\n"
            "\n"
            "Procedure (TS 23.288 §6.1)\n"
            "  1. GET /api/nwdaf/analytics/BAD_ID — synthetic\n"
            "     analytics_id deliberately outside the §6.1 catalogue.\n"
            "  2. Capture the HTTP status code.\n"
            "  3. Assert status == 400. Any other code (200, 404, 500)\n"
            "     means the allow-list is not policed correctly at\n"
            "     the request-routing layer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — bad ID hard-coded as 'BAD_ID').\n"
            "\n"
            "Pass criteria\n"
            "  Response HTTP status == 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Does not inspect body — only the status\n"
            "  code is asserted; the body error-shape contract is\n"
            "  covered by other validation tests."
        ),
    )

    def run(self):
        try:
            r, s = _api("/api/nwdaf/analytics/BAD_ID")
            if s != 400:
                self.fail_test(f"bad id did not 400: {s} {r}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafSubscribeUnsubscribe(TestCase):
    """TC-NWDAF-A-004: Subscribe + Unsubscribe round-trip (§6.1.1)."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-004",
        title="Subscribe + Unsubscribe round-trip",
        spec="TS 23.288 §6.1.1",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins TS 23.288 §6.1.1 Subscribe / Unsubscribe round-trip\n"
            "  on /api/nwdaf/subscriptions. Confirms a new subscription\n"
            "  receives a sub_id, surfaces on the listing endpoint, and\n"
            "  can be cleanly DELETEd. This is the Nnwdaf_Event-\n"
            "  Subscription contract a consumer NF (PCF, AMF, AF via NEF)\n"
            "  uses to opt in to periodic analytics notifications.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1 + TS 29.520 §5.2)\n"
            "  1. POST /api/nwdaf/subscriptions with consumer_nf,\n"
            "     analytics_id=NF_LOAD, callback_url, interval_sec=60.\n"
            "  2. Assert HTTP 200, r.ok, and a non-empty sub_id.\n"
            "  3. GET /api/nwdaf/subscriptions and assert sub_id is in\n"
            "     the listing (drives the operator UI).\n"
            "  4. DELETE /api/nwdaf/subscriptions/{sub_id}; assert 200\n"
            "     and ur.ok.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — body fields are hard-coded for determinism).\n"
            "\n"
            "Pass criteria\n"
            "  Create returns sub_id AND list contains sub_id AND DELETE\n"
            "  returns 200 / ok=True.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Notification delivery to callback_url is not\n"
            "  asserted here; that path is covered by exposure tests."
        ),
    )

    def run(self):
        try:
            r, s = _api("/api/nwdaf/subscriptions", "POST", {
                "consumer_nf": "tc-nwdaf-a-004",
                "analytics_id": "NF_LOAD",
                "callback_url": "http://example.invalid/cb",
                "interval_sec": 60,
            })
            if s != 200 or not r.get("ok") or not r.get("sub_id"):
                self.fail_test(f"subscribe failed: {s} {r}")
                return self.result
            sid = r["sub_id"]

            # List
            lst, _ = _api("/api/nwdaf/subscriptions")
            ids = [s.get("sub_id") for s in lst.get("subscriptions", [])]
            if sid not in ids:
                self.fail_test(f"sub {sid} not listed", ids=ids[:5])
                return self.result

            # Unsubscribe
            ur, us = _api(f"/api/nwdaf/subscriptions/{sid}", "DELETE")
            if us != 200 or not ur.get("ok"):
                self.fail_test(f"unsubscribe failed: {us} {ur}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafSubscribeValidation(TestCase):
    """TC-NWDAF-A-005: subscribe with bad analytics_id / missing fields → 400."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-005",
        title="Subscribe with bad analytics_id / missing fields returns 400",
        spec="TS 23.288 §6.1.1",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative pin for the §6.1.1 Subscribe input vocabulary.\n"
            "  Subscriptions referencing an unknown analytics_id or\n"
            "  missing the mandatory consumer_nf identifier must be\n"
            "  rejected before any row is persisted — otherwise the\n"
            "  scheduler would later try to compute analytics for a\n"
            "  ghost subscription, and the operator panel would render\n"
            "  consumer_nf=NULL rows that defeat the purpose of tracking\n"
            "  per-consumer subscriptions.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1 + TS 29.520 §5.2)\n"
            "  1. POST /api/nwdaf/subscriptions with body\n"
            "     {consumer_nf:tc, analytics_id:GARBAGE} — bad ID.\n"
            "     Assert HTTP 400 (analytics_id allow-list violated).\n"
            "  2. POST /api/nwdaf/subscriptions with body\n"
            "     {analytics_id:NF_LOAD} only (no consumer_nf). Assert\n"
            "     HTTP 400 (mandatory field missing).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — both bad payloads hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return HTTP 400 cleanly (no 5xx, no silent\n"
            "  200 with an empty sub_id).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Body content (per-field error messages) is\n"
            "  not asserted — only the HTTP status is part of the\n"
            "  §6.1.1 wire contract being pinned here."
        ),
    )

    def run(self):
        try:
            r, s = _api("/api/nwdaf/subscriptions", "POST", {
                "consumer_nf": "tc",
                "analytics_id": "GARBAGE",
            })
            if s != 400:
                self.fail_test(f"bad analytics_id did not 400: {s} {r}")
                return self.result

            r2, s2 = _api("/api/nwdaf/subscriptions", "POST", {
                "analytics_id": "NF_LOAD",
            })
            if s2 != 400:
                self.fail_test(f"missing consumer_nf did not 400: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafRecentHistory(TestCase):
    """TC-NWDAF-A-006: /api/nwdaf/recent returns persisted §6.1.3 results."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-006",
        title="/api/nwdaf/recent returns persisted §6.1.3 results",
        spec="TS 23.288 §6.1.3",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the analytics result-history surface backing the\n"
            "  operator UI 'recent results' panel. TS 23.288 §6.1.3\n"
            "  permits NWDAF to retain prior analytics outputs for\n"
            "  later inspection (per-ID timestamped rows used for\n"
            "  trend visualisation and replay-into-consumer debugging);\n"
            "  this test exercises the history endpoint shape\n"
            "  (well-formed list) without asserting any specific row\n"
            "  content because content is rate- and ingest-dependent.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.3 + §6.2)\n"
            "  1. GET /api/nwdaf/analytics/PDU_SESSION — primes the\n"
            "     history table with at least one row.\n"
            "  2. GET /api/nwdaf/recent?analytics_id=PDU_SESSION&limit=5.\n"
            "  3. Assert HTTP 200 and r.ok.\n"
            "  4. Assert r['recent'] is a list (may be empty if the\n"
            "     prime did not persist; emptiness is not a failure).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — limit=5 hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  r.ok AND isinstance(r['recent'], list).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rows — count of returned recent entries.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. limit=5 cap, no startTime / endTime windowing\n"
            "  is exercised; full bulk-export semantics live elsewhere."
        ),
    )

    def run(self):
        try:
            # Trigger a compute so a row lands in the history table
            _api("/api/nwdaf/analytics/PDU_SESSION")

            r, s = _api("/api/nwdaf/recent?analytics_id=PDU_SESSION&limit=5")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"recent failed: {s} {r}")
                return self.result
            if not isinstance(r.get("recent"), list):
                self.fail_test(f"recent not a list: {r}")
                return self.result
            self.pass_test(rows=len(r["recent"]))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafStatus(TestCase):
    """TC-NWDAF-A-007: /api/nwdaf/status reports cache + supported IDs."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-007",
        title="/api/nwdaf/status reports data cache + supported IDs",
        spec="TS 23.288 §6.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the /api/nwdaf/status service-health endpoint. TS\n"
            "  23.288 §6.2 (Data Collection) feeds the in-memory cache\n"
            "  that drives analytics; the status endpoint must report\n"
            "  cache fill plus the §6.1 catalogue so the operator can\n"
            "  spot a stuck data-collection pipeline at a glance and\n"
            "  so external monitoring can scrape NWDAF readiness\n"
            "  without parsing the dashboard payload.\n"
            "\n"
            "Procedure (TS 23.288 §6.2 + §6.1)\n"
            "  1. GET /api/nwdaf/status.\n"
            "  2. Assert HTTP 200 and r.ok.\n"
            "  3. Assert keys 'cached_data_points', 'analytics_ids',\n"
            "     'supported_ids' all present.\n"
            "  4. Assert set(r['supported_ids']) == set(EXPECTED_IDS) —\n"
            "     covers the §6.1 catalogue (order not asserted, that\n"
            "     belongs to TC-NWDAFA-001).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none).\n"
            "\n"
            "Pass criteria\n"
            "  All three status keys present AND supported_ids set\n"
            "  matches the §6.1 catalogue.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. cached_data_points value not asserted —\n"
            "  cache may be 0 immediately after baseline reset."
        ),
    )

    def run(self):
        try:
            r, s = _api("/api/nwdaf/status")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"status failed: {s} {r}")
                return self.result
            for k in ("cached_data_points", "analytics_ids", "supported_ids"):
                if k not in r:
                    self.fail_test(f"missing {k}", body=list(r))
                    return self.result
            if set(r["supported_ids"]) != set(EXPECTED_IDS):
                self.fail_test(
                    f"supported_ids mismatch: {r['supported_ids']}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_NWDAF_ANALYTICS_TCS = [
    NwdafDashboardShape,
    NwdafSingleAnalytic,
    NwdafBadAnalyticID,
    NwdafSubscribeUnsubscribe,
    NwdafSubscribeValidation,
    NwdafRecentHistory,
    NwdafStatus,
]

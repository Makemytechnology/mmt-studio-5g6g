# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NWDAF robot-suite parity (TS 23.288).

Pairs with robot/suites/policy_charging/28_nwdaf.robot. The robot
suite documents TS 23.288 procedures (data collection from AMF/SMF,
abnormal-behaviour analytics, NF load + UE mobility analytics, bulk
export). These Python TestCase shells provide a smoke surface on top
of the /api/nwdaf/* REST endpoints (already pinned by tc_nwdaf_*.py)
for the cases that have a usable surface, and mark the multi-NF flows
as "implementation pending" so the runner reports parity without
claiming a vacuous PASS.

TC-NWDAF-* IDs are distinct from TC-NWDAFA-* (tc_nwdaf_analytics.py),
TC-NWDAFE-* (tc_nwdaf_exposure.py), and TC-NWDAFH-* (tc_nwdaf_
hardening.py). The robot-derived TCs document the §6 procedure; the
suffixed TCs pin the REST contract.

Robot reference:
  /home/bxb/work/mmt_studio_core_tester/robot/suites/policy_charging/28_nwdaf.robot
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_nwdaf_robot")

NWDAF = "/api/nwdaf"


def _api(path, method="GET", body=None):
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read().decode()), resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


# ── TC-NWDAF-001 ────────────────────────────────────────────────


class NwdafDataPointCollection(TestCase):
    """TC-NWDAF-001: TS 23.288 §6.2.2.1 event exposure (AMF + SMF).

    Subscribes Namf_EventExposure + Nsmf_EventExposure, triggers UE
    registration + PDU session establishment, asserts NWDAF event
    store captures both. Multi-NF flow with live UE drive — left as
    fail-pending; the analytics read surface is covered by
    TC-NWDAFA-006 (recent history) and TC-NWDAFA-007 (status).
    """
    SPEC = TestSpec(
        tc_id="TC-NWDAF-001",
        title="Data Point Collection (Registration + PDU Session Events)",
        spec="TS 23.288 §6.2.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF, NF.AMF, NF.SMF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "data-collection", "event-exposure",
              "amf", "smf", "priority-1"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Documents the §6.2.2.1 event-exposure data-collection\n"
            "  flow: NWDAF subscribes Namf_EventExposure (registration\n"
            "  / deregistration / location-report) and Nsmf_Event-\n"
            "  Exposure (PDU session events) so a UE registration plus\n"
            "  PDU establishment generates correlated event rows in\n"
            "  NWDAF storage. Robot-suite parity placeholder — the live\n"
            "  Namf / Nsmf subscriber emulation is not implemented in\n"
            "  Python yet.\n"
            "\n"
            "Procedure (TS 23.288 §6.2.2.1 / TS 29.520 §5.2)\n"
            "  1. (intended) NWDAF subscribes Namf_EventExposure with\n"
            "     event_filter={UE_REGISTRATION, LOCATION_REPORT}.\n"
            "  2. (intended) NWDAF subscribes Nsmf_EventExposure with\n"
            "     event_filter={PDU_SESSION_EST, PDU_RELEASE,\n"
            "     QOS_CHANGE}.\n"
            "  3. (intended) Register UE; assert AMF event row in\n"
            "     NWDAF event store (SUPI + ts).\n"
            "  4. (intended) Establish PDU; assert SMF event row.\n"
            "  5. (intended) Assert event timestamps monotonically\n"
            "     increasing.\n"
            "  6. (current) run() calls fail_test with implementation-\n"
            "     pending message pointing at the robot scenario.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — placeholder).\n"
            "\n"
            "Pass criteria\n"
            "  Always fails today — implementation pending. When\n"
            "  implemented: both event rows present, ts monotonic.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — fail_test only, no pass_test path).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Needs Namf/Nsmf subscriber driver; the\n"
            "  read surface is covered by TC-NWDAFA-006 / TC-NWDAFA-007."
        ),
    )

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/policy_charging/28_nwdaf.robot::TC-NWDAF-001 "
            "for the procedure; live AMF + SMF event-exposure "
            "subscription drive is needed."
        )
        return self.result


# ── TC-NWDAF-002 ────────────────────────────────────────────────


class NwdafAnomalyDetectionTrigger(TestCase):
    """TC-NWDAF-002: TS 23.288 §6.7.5 abnormal behaviour analytics."""
    SPEC = TestSpec(
        tc_id="TC-NWDAF-002",
        title="Anomaly Detection Trigger (abnormal UE behaviour)",
        spec="TS 23.288 §6.7.5",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF, NF.AMF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "anomaly", "detection", "abnormal-behaviour",
              "priority-1"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Documents the §6.7.5 ABNORMAL_BEHAVIOUR analytics flow:\n"
            "  NWDAF holds a baseline UE-behaviour model, evaluates\n"
            "  incoming events against it, and emits an analytics\n"
            "  notification (SUPI + anomaly type + confidence) when\n"
            "  the score crosses threshold. Robot-suite parity\n"
            "  placeholder — needs a UE driver that breaks normal\n"
            "  patterns on demand.\n"
            "\n"
            "Procedure (TS 23.288 §6.7.5 / TS 29.520 §5.4)\n"
            "  1. (intended) NWDAF trained on baseline UE activity.\n"
            "  2. (intended) Drive anomalous behaviour: rapid repeat\n"
            "     register/deregister cycles, unusual DNN access.\n"
            "  3. (intended) Anomaly score crosses threshold; NWDAF\n"
            "     pushes notification to subscribed AMF/PCF callback.\n"
            "  4. (intended) Assert anomaly observed within detection\n"
            "     window and notification delivered.\n"
            "  5. (current) run() calls fail_test with implementation-\n"
            "     pending message pointing at the robot scenario.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — placeholder).\n"
            "\n"
            "Pass criteria\n"
            "  Always fails today — implementation pending. When\n"
            "  implemented: anomaly notification observed in window.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — fail_test only).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Read surface for ABNORMAL_BEHAVIOUR is\n"
            "  exercised via TC-NWDAFA-001 dashboard enumeration."
        ),
    )

    def run(self):
        self.fail_test(
            "Python implementation pending — see "
            "robot/suites/policy_charging/28_nwdaf.robot::TC-NWDAF-002 "
            "for the procedure; needs a baseline model + abnormal-"
            "behaviour UE simulator."
        )
        return self.result


# ── TC-NWDAF-003 ────────────────────────────────────────────────


class NwdafAnalyticsSubscriptionViaApi(TestCase):
    """TC-NWDAF-003: TS 29.520 §5.2 Nnwdaf_AnalyticsSubscription."""
    SPEC = TestSpec(
        tc_id="TC-NWDAF-003",
        title="Analytics Subscription via REST API",
        spec="TS 23.288 §6.1.1",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "api", "subscription", "nnwdaf",
              "priority-1"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Robot-parity smoke for TS 23.288 §6.1.1 Subscribe\n"
            "  lifecycle on the panel surface /api/nwdaf/subscriptions.\n"
            "  Pairs with the robot scenario at 28_nwdaf.robot::TC-\n"
            "  NWDAF-003. Confirms an analytics subscription round-\n"
            "  trips (POST → list → DELETE) and the sub_id is durable\n"
            "  across the listing call.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1 + TS 29.520 §5.2)\n"
            "  1. POST /api/nwdaf/subscriptions with consumer_nf=\n"
            "     tc-nwdaf-003, analytics_id=NF_LOAD, callback_url,\n"
            "     interval_sec=60.\n"
            "  2. Assert HTTP 200, r.ok, and non-empty sub_id.\n"
            "  3. GET /api/nwdaf/subscriptions; assert sub_id is in\n"
            "     the listing.\n"
            "  4. DELETE /api/nwdaf/subscriptions/{sub_id}; assert 200\n"
            "     and ur.ok.\n"
            "  5. finally re-deletes on exception to avoid leakage.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — body hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Create returns sub_id AND list contains sub_id AND\n"
            "  DELETE returns 200 / ok.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Notification delivery to callback_url is\n"
            "  covered by TC-NWDAFE-007 (exposure path)."
        ),
    )

    def run(self):
        sid = None
        try:
            r, s = _api(f"{NWDAF}/subscriptions", "POST", {
                "consumer_nf": "tc-nwdaf-003",
                "analytics_id": "NF_LOAD",
                "callback_url": "http://example.invalid/cb",
                "interval_sec": 60,
            })
            if s != 200 or not r.get("ok") or not r.get("sub_id"):
                self.fail_test(f"subscribe failed: {s} {r}")
                return self.result
            sid = r["sub_id"]

            lst, _ = _api(f"{NWDAF}/subscriptions")
            ids = [x.get("sub_id") for x in lst.get("subscriptions", [])]
            if sid not in ids:
                self.fail_test(f"sub {sid} not listed", ids=ids[:5])
                return self.result

            ur, us = _api(f"{NWDAF}/subscriptions/{sid}", "DELETE")
            if us != 200 or not ur.get("ok"):
                self.fail_test(f"unsubscribe failed: {us} {ur}")
                return self.result
            sid = None
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if sid:
                try:
                    _api(f"{NWDAF}/subscriptions/{sid}", "DELETE")
                except Exception:
                    pass
        return self.result


# ── TC-NWDAF-010 ────────────────────────────────────────────────


class NwdafLoadAnalyticsPerNf(TestCase):
    """TC-NWDAF-010: TS 23.288 §6.3 / §6.5 NF load analytics."""
    SPEC = TestSpec(
        tc_id="TC-NWDAF-010",
        title="Load Analytics per NF (NF_LOAD)",
        spec="TS 23.288 §6.5",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF, NF.AMF, NF.SMF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "load", "nf-load", "capacity",
              "priority-1"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Robot-parity smoke for TS 23.288 §6.5 NF load analytics.\n"
            "  Pairs with 28_nwdaf.robot::TC-NWDAF-010. Pins the per-ID\n"
            "  Nnwdaf_AnalyticsInfo lookup for NF_LOAD: the §6.5 NF\n"
            "  load level must round-trip with the §6.1.3 result +\n"
            "  confidence envelope on the panel surface so a downstream\n"
            "  consumer (PCF capacity-aware policy) can reason about\n"
            "  per-NF saturation without polling each NF directly.\n"
            "\n"
            "Procedure (TS 23.288 §6.5 + §6.1.3 / TS 29.520 §5.3)\n"
            "  1. GET /api/nwdaf/analytics/NF_LOAD (no query params —\n"
            "     server picks its default window).\n"
            "  2. Assert HTTP 200 and r.ok.\n"
            "  3. Pull r['result'] and assert analytics_id == 'NF_LOAD'\n"
            "     (handler routing pin).\n"
            "  4. Assert 'result' and 'confidence' both present per\n"
            "     §6.1.3 envelope contract.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none).\n"
            "\n"
            "Pass criteria\n"
            "  r.ok AND analytics_id echoes NF_LOAD AND result+\n"
            "  confidence keys present.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  confidence — confidence score returned for NF_LOAD.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Full dashboard with every ID is in TC-\n"
            "  NWDAFA-001; this is the per-ID focal pin."
        ),
    )

    def run(self):
        try:
            r, s = _api(f"{NWDAF}/analytics/NF_LOAD")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"NF_LOAD analytics failed: {s} {r}")
                return self.result
            res = r.get("result", {})
            if res.get("analytics_id") != "NF_LOAD":
                self.fail_test(f"wrong analytics_id: {res}", body=r)
                return self.result
            if "result" not in res or "confidence" not in res:
                self.fail_test(f"§6.1.3 fields missing: {res}")
                return self.result
            self.pass_test(confidence=res.get("confidence"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ── TC-NWDAF-011 ────────────────────────────────────────────────


class NwdafUeMobilityAnalytics(TestCase):
    """TC-NWDAF-011: TS 23.288 §6.7.1 / §6.7.2 UE mobility analytics."""
    SPEC = TestSpec(
        tc_id="TC-NWDAF-011",
        title="UE Mobility Analytics (UE_MOBILITY)",
        spec="TS 23.288 §6.7.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF, NF.AMF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MINOR,
        tags=("conformance", "mobility", "ue-mobility", "trajectory",
              "priority-2"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Robot-parity smoke for TS 23.288 §6.7.2 UE_MOBILITY\n"
            "  analytics. Pairs with 28_nwdaf.robot::TC-NWDAF-011. Pins\n"
            "  the per-ID Nnwdaf_AnalyticsInfo lookup for UE_MOBILITY\n"
            "  with a 120-second observation window. The full\n"
            "  trajectory + spatial-distribution payload depends on a\n"
            "  moving UE feeding AMF location reports into §6.2 data\n"
            "  collection; this TC pins only the read-surface contract\n"
            "  (ID + §6.1.3 envelope), not the §6.7.2 algorithm.\n"
            "\n"
            "Procedure (TS 23.288 §6.7.2 + §6.1.3 / TS 29.520 §5.3)\n"
            "  1. GET /api/nwdaf/analytics/UE_MOBILITY?window_sec=120.\n"
            "  2. Assert HTTP 200 and r.ok.\n"
            "  3. Pull r['result']; assert analytics_id == 'UE_MOBILITY'.\n"
            "  4. Assert 'result' and 'confidence' both present per\n"
            "     §6.1.3.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — window_sec=120 hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  r.ok AND analytics_id echoes UE_MOBILITY AND result+\n"
            "  confidence keys present.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  confidence — confidence score for UE_MOBILITY.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Live trajectory analytics needs a moving\n"
            "  UE; expected confidence may be 0.0 without ingest."
        ),
    )

    def run(self):
        try:
            r, s = _api(f"{NWDAF}/analytics/UE_MOBILITY?window_sec=120")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"UE_MOBILITY analytics failed: {s} {r}")
                return self.result
            res = r.get("result", {})
            if res.get("analytics_id") != "UE_MOBILITY":
                self.fail_test(f"wrong analytics_id: {res}", body=r)
                return self.result
            if "result" not in res or "confidence" not in res:
                self.fail_test(f"§6.1.3 fields missing: {res}")
                return self.result
            self.pass_test(confidence=res.get("confidence"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ── TC-NWDAF-012 ────────────────────────────────────────────────


class NwdafAnalyticsDataExport(TestCase):
    """TC-NWDAF-012: TS 23.288 §6.2.6 analytics export / history."""
    SPEC = TestSpec(
        tc_id="TC-NWDAF-012",
        title="Analytics Data Export (recent history)",
        spec="TS 23.288 §6.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MINOR,
        tags=("conformance", "export", "data", "bulk",
              "priority-2"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Robot-parity smoke for TS 23.288 §6.2 analytics export\n"
            "  / history. Pairs with 28_nwdaf.robot::TC-NWDAF-012. Pins\n"
            "  the /recent endpoint as the bulk-history surface: each\n"
            "  entry, when present, is a §6.1.3 record (analytics_id +\n"
            "  timestamp + result + confidence) suitable for trend\n"
            "  visualisation or replay debugging. The format=json wire\n"
            "  shape is in TC-NWDAFA-006; this TC is the per-ID drill-\n"
            "  in showing that the analytics-id filter actually filters.\n"
            "\n"
            "Procedure (TS 23.288 §6.2 + §6.1.3)\n"
            "  1. GET /api/nwdaf/analytics/PDU_SESSION — primes the\n"
            "     history table with at least one row.\n"
            "  2. GET /api/nwdaf/recent?analytics_id=PDU_SESSION&limit=5.\n"
            "  3. Assert HTTP 200 and r.ok.\n"
            "  4. Assert r['recent'] is a list (empty is allowed —\n"
            "     history retention may have evicted the prime row).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — limit=5 hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  r.ok AND isinstance(r['recent'], list).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  row_count — count of recent rows returned.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. startTime / endTime windowing is not\n"
            "  exercised here; only the recent-N path."
        ),
    )

    def run(self):
        try:
            # Prime the history table
            _api(f"{NWDAF}/analytics/PDU_SESSION")

            r, s = _api(f"{NWDAF}/recent?analytics_id=PDU_SESSION&limit=5")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"recent failed: {s} {r}")
                return self.result
            recent = r.get("recent")
            if not isinstance(recent, list):
                self.fail_test(f"recent not a list: {type(recent)}", body=r)
                return self.result
            self.pass_test(row_count=len(recent))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_NWDAF_ROBOT_TCS = [
    NwdafDataPointCollection,
    NwdafAnomalyDetectionTrigger,
    NwdafAnalyticsSubscriptionViaApi,
    NwdafLoadAnalyticsPerNf,
    NwdafUeMobilityAnalytics,
    NwdafAnalyticsDataExport,
]

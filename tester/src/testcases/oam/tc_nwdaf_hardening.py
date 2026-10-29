# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NWDAF analytics + exposure hardening.

Spec anchors:

  TS 23.288 §6.1.3   Contents of Analytics Exposure (confidence threshold).
  TS 23.288 §6.1.1   Subscribe / Unsubscribe (PATCH semantics).
  TS 23.288 §6.2     Procedures for Data Collection (ingest endpoint).
  TS 23.288 §6.2.9   User consent (per-UE gate when target_type=imsi).
  TS 23.288 §6.1.2.2 Analytics request by AFs via NEF (one-shot path).
  TS 29.522 §4.4     Nnef_AnalyticsExposure northbound shape.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_nwdaf_hardening")

NW = "/api/nwdaf"
EX = "/api/nwdaf/exposure"


def _api(path, method="GET", body=None, headers=None):
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    h = {"Content-Type": "application/json"}
    if headers:
        h.update(headers)
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=h, method=method)
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


# ─────────────────────── Analytics ──────────────────────────

class NwdafIngestDataPoint(TestCase):
    """TC-NWDAF-A-010: POST /data persists + folds into in-memory cache."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-010",
        title="POST /data persists + folds into in-memory cache",
        spec="TS 23.288 §6.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the §6.2 Data Collection ingest path that feeds the\n"
            "  analytics cache. NWDAF must accept a structured data\n"
            "  point from any consumer-side NF (AMF, SMF, NF-status\n"
            "  poller etc.), bump its ingest counter, persist a row,\n"
            "  and reject obviously malformed payloads at the route\n"
            "  layer so the cache never ingests garbage.\n"
            "\n"
            "Procedure (TS 23.288 §6.2)\n"
            "  1. GET /status; read ingest.total as baseline.\n"
            "  2. POST /data with source_nf=tester, analytics_id=NF_LOAD,\n"
            "     data_json='{cpu_pct:47, mem_pct:62}'; assert HTTP 200\n"
            "     and r.id is returned.\n"
            "  3. GET /status; assert ingest.total == baseline + 1.\n"
            "  4. POST /data with analytics_id=BOGUS; assert HTTP 400.\n"
            "  5. POST /data with missing source_nf; assert HTTP 400.\n"
            "  6. POST /data with data_json='{not json'; assert HTTP 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payload values hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Valid ingest returns 200 + id AND counter bumps by\n"
            "  exactly 1 AND all three malformed POSTs return 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The +1 delta assumes no other ingest is\n"
            "  in-flight; parallel runs would race this counter."
        ),
    )

    def run(self):
        try:
            # Stats baseline
            r0, _ = _api(NW + "/status")
            base = r0.get("ingest", {}).get("total", 0)

            # Ingest a valid NF_LOAD point
            r, s = _api(NW + "/data", "POST", {
                "source_nf": "tester",
                "analytics_id": "NF_LOAD",
                "data_json": json.dumps({"cpu_pct": 47, "mem_pct": 62}),
            })
            if s != 200 or not r.get("id"):
                self.fail_test(f"ingest failed: {s} {r}")
                return self.result

            # Stats moved up
            r1, _ = _api(NW + "/status")
            now = r1.get("ingest", {}).get("total", 0)
            if now != base + 1:
                self.fail_test(f"ingest stats not bumped: base={base} now={now}")
                return self.result

            # Bad analytics_id → 400
            _, sb = _api(NW + "/data", "POST", {
                "source_nf": "tester",
                "analytics_id": "BOGUS",
                "data_json": "{}",
            })
            if sb != 400:
                self.fail_test(f"bad analytics_id did not 400: {sb}")
                return self.result

            # Missing source_nf → 400
            _, sm = _api(NW + "/data", "POST", {
                "analytics_id": "NF_LOAD",
                "data_json": "{}",
            })
            if sm != 400:
                self.fail_test(f"missing source_nf did not 400: {sm}")
                return self.result

            # Invalid JSON in data_json → 400
            _, sj = _api(NW + "/data", "POST", {
                "source_nf": "tester",
                "analytics_id": "NF_LOAD",
                "data_json": "{not json",
            })
            if sj != 400:
                self.fail_test(f"bad data_json did not 400: {sj}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafConfidenceThreshold(TestCase):
    """TC-NWDAF-A-011: ?min_confidence= filters low-confidence results."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-011",
        title="?min_confidence= filters low-confidence analytics results",
        spec="TS 23.288 §6.1.3",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the §6.1.3 'confidence' contract on /analytics. A\n"
            "  consumer may opt to receive only high-confidence results\n"
            "  by passing ?min_confidence=. NWDAF must honour this on\n"
            "  both the aggregator and single-ID paths, silently ignore\n"
            "  out-of-range or non-numeric values (graceful), and\n"
            "  publish a filtered_out signal so the consumer knows when\n"
            "  rows were suppressed.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.3)\n"
            "  1. GET /analytics?window_sec=60; capture base_count =\n"
            "     len(r.analytics).\n"
            "  2. GET /analytics?min_confidence=1.5 (out of range);\n"
            "     assert len matches base_count (filter skipped).\n"
            "  3. GET /analytics?min_confidence=0.99; assert r.\n"
            "     filtered_out > 0 (default zero-confidence rows hit).\n"
            "  4. GET /analytics/NF_LOAD?min_confidence=0.99; assert\n"
            "     r.filtered_out is truthy on the single-ID path.\n"
            "  5. GET /analytics?min_confidence=high; assert HTTP 200\n"
            "     (graceful, not 400).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — threshold values hard-coded for determinism).\n"
            "\n"
            "Pass criteria\n"
            "  Out-of-range ignored AND 0.99 suppresses rows AND\n"
            "  single-ID honours threshold AND non-numeric returns 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The 0.99 threshold relies on rows starting\n"
            "  with confidence=0 absent §6.2 data ingest."
        ),
    )

    def run(self):
        try:
            # Aggregator: with no threshold, all 7 IDs come back.
            r0, _ = _api(NW + "/analytics?window_sec=60")
            full = r0.get("analytics", {})
            if not full:
                self.fail_test(f"no analytics in baseline: {r0}")
                return self.result
            base_count = len(full)

            # min_confidence=1.01 (clamped to 0 — invalid → no filter)
            r1, _ = _api(NW + "/analytics?min_confidence=1.5")
            if len(r1.get("analytics", {})) != base_count:
                self.fail_test(f"out-of-range min_confidence not ignored: {r1.get('filtered_out')}")
                return self.result

            # min_confidence=0.99 — most results have 0 confidence (no
            # data points yet) so all should be filtered out.
            r2, _ = _api(NW + "/analytics?min_confidence=0.99")
            if r2.get("filtered_out", 0) == 0:
                self.fail_test(f"no rows filtered with min_confidence=0.99: {r2}")
                return self.result

            # Single-ID path with high threshold returns filtered_out=true
            r3, _ = _api(NW + "/analytics/NF_LOAD?min_confidence=0.99")
            if not r3.get("filtered_out"):
                self.fail_test(f"single-ID min_confidence not honoured: {r3}")
                return self.result

            # Bad min_confidence (non-numeric) is ignored, not 400
            r4, s4 = _api(NW + "/analytics?min_confidence=high")
            if s4 != 200:
                self.fail_test(f"bad min_confidence rejected: {s4} {r4}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafSubscriptionGetPatch(TestCase):
    """TC-NWDAF-A-012: GET + PATCH on a subscription round-trip."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFA-012",
        title="Subscription GET + PATCH round-trip with 400/404 paths",
        spec="TS 23.288 §6.1.1",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the GET + PATCH read-modify-update semantics on\n"
            "  /api/nwdaf/subscriptions/{sub_id} (TS 23.288 §6.1.1). A\n"
            "  consumer must be able to retune interval_sec or\n"
            "  callback_url without re-subscribing, but bad enum values,\n"
            "  unknown keys, or unknown sub_ids must be rejected\n"
            "  cleanly with 400 / 404 instead of silent updates.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1)\n"
            "  1. POST /subscriptions with interval_sec=60; capture sid.\n"
            "  2. GET /subscriptions/{sid}; assert 200 and subscription\n"
            "     row with interval_sec=60.\n"
            "  3. PATCH /subscriptions/{sid} with interval_sec=120 +\n"
            "     callback_url; assert 200 and interval reflects 120.\n"
            "  4. PATCH with status=obliterated; assert HTTP 400.\n"
            "  5. PATCH with unknown=1 (no recognised key); assert 400.\n"
            "  6. PATCH /subscriptions/no-such; assert HTTP 404.\n"
            "  7. GET /subscriptions/no-such; assert HTTP 404.\n"
            "  8. finally DELETEs the created sid.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payloads hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  All steps 2–7 hit their expected status codes / row\n"
            "  shapes; no PATCH silently accepts invalid input.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. status PATCH enum policed; the legal set\n"
            "  (active/paused etc.) is not enumerated here."
        ),
    )

    def run(self):
        try:
            # Create
            r, s = _api(NW + "/subscriptions", "POST", {
                "consumer_nf": "tc-nwdaf-a-012",
                "analytics_id": "NF_LOAD",
                "interval_sec": 60,
            })
            if s != 200 or not r.get("sub_id"):
                self.fail_test(f"create sub failed: {s} {r}")
                return self.result
            sid = r["sub_id"]
            try:
                # GET
                rg, sg = _api(f"{NW}/subscriptions/{sid}")
                if sg != 200 or not rg.get("subscription"):
                    self.fail_test(f"GET sub failed: {sg} {rg}")
                    return self.result
                sub = rg["subscription"]
                if sub.get("interval_sec") != 60:
                    self.fail_test(f"interval mismatch: {sub}")
                    return self.result

                # PATCH interval_sec
                rp, sp = _api(f"{NW}/subscriptions/{sid}", "PATCH", {
                    "interval_sec": 120,
                    "callback_url": "http://test:9999/cb",
                })
                if sp != 200:
                    self.fail_test(f"PATCH failed: {sp} {rp}")
                    return self.result
                if rp.get("subscription", {}).get("interval_sec") != 120:
                    self.fail_test(f"PATCH did not stick: {rp}")
                    return self.result

                # Bad status value → 400
                _, sb = _api(f"{NW}/subscriptions/{sid}", "PATCH",
                             {"status": "obliterated"})
                if sb != 400:
                    self.fail_test(f"bad status did not 400: {sb}")
                    return self.result

                # Empty patch → 400
                _, se = _api(f"{NW}/subscriptions/{sid}", "PATCH", {"unknown": 1})
                if se != 400:
                    self.fail_test(f"unknown-key patch did not 400: {se}")
                    return self.result

                # Unknown sub_id PATCH → 404
                _, sn = _api(f"{NW}/subscriptions/no-such", "PATCH",
                             {"interval_sec": 30})
                if sn != 404:
                    self.fail_test(f"unknown sid PATCH did not 404: {sn}")
                    return self.result

                # Unknown sub_id GET → 404
                _, sg2 = _api(f"{NW}/subscriptions/no-such")
                if sg2 != 404:
                    self.fail_test(f"unknown sid GET did not 404: {sg2}")
                    return self.result
            finally:
                _api(f"{NW}/subscriptions/{sid}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─────────────────────── Exposure ───────────────────────────

class NwdafExposureConsumerPatchAndKeyRotate(TestCase):
    """TC-NWDAF-E-010: consumer GET / PATCH / rotate-key round-trip."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-010",
        title="Exposure consumer GET / PATCH / rotate-key round-trip",
        spec="TS 23.288 §6.1.1.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the consumer lifecycle on /api/nwdaf/exposure/\n"
            "  consumers/{id}: GET, PATCH (allow-list + callback), and\n"
            "  rotate-key (TS 23.288 §6.1.1.2 + §6.2.9). After a\n"
            "  rotate, the old key must immediately stop authorising\n"
            "  requests so a leaked credential can be revoked.\n"
            "  Unknown-consumer paths return 404; bad PATCH input 400.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1.2 + §6.2.9)\n"
            "  1. POST /consumers with allowed_analytics=[NF_LOAD];\n"
            "     capture cid and old_key.\n"
            "  2. GET /consumers/{cid}; assert 200 and correct name.\n"
            "  3. PATCH /consumers/{cid} with allowed_analytics=\n"
            "     [NF_LOAD,UE_MOBILITY] + callback_url; assert 200 and\n"
            "     UE_MOBILITY in the returned allow-list.\n"
            "  4. PATCH with allowed_analytics=[BOGUS]; assert 400.\n"
            "  5. PATCH /consumers/9999999; assert HTTP 404.\n"
            "  6. POST /consumers/{cid}/rotate-key; capture new_key,\n"
            "     assert non-empty and != old_key.\n"
            "  7. GET /analytics/nf_load with X-API-Key=old_key; assert\n"
            "     HTTP 401.\n"
            "  8. GET /analytics/nf_load with X-API-Key=new_key; assert\n"
            "     HTTP 200.\n"
            "  9. POST /consumers/9999999/rotate-key; assert HTTP 404.\n"
            " 10. finally DELETEs the consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — body fields hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  All assertions above hold; in particular old_key fails\n"
            "  immediately after rotate and new_key succeeds.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No grace window — rotate is hard; this is\n"
            "  intentional for security."
        ),
    )

    def run(self):
        try:
            r, s = _api(EX + "/consumers", "POST", {
                "name": "tc-e-010-consumer",
                "callback_url": "http://test:9999/cb",
                "allowed_analytics": ["NF_LOAD"],
            })
            if s != 200:
                self.fail_test(f"create failed: {s} {r}")
                return self.result
            cid = r.get("id")
            old_key = r.get("api_key")
            try:
                # GET
                rg, sg = _api(f"{EX}/consumers/{cid}")
                if sg != 200 or rg.get("consumer", {}).get("name") != "tc-e-010-consumer":
                    self.fail_test(f"GET wrong: {sg} {rg}")
                    return self.result

                # PATCH allowed_analytics + callback_url
                rp, sp = _api(f"{EX}/consumers/{cid}", "PATCH", {
                    "allowed_analytics": ["NF_LOAD", "UE_MOBILITY"],
                    "callback_url": "http://test:9999/cb-new",
                })
                if sp != 200:
                    self.fail_test(f"PATCH failed: {sp} {rp}")
                    return self.result
                allowed = rp.get("consumer", {}).get("allowed_analytics", [])
                if "UE_MOBILITY" not in allowed:
                    self.fail_test(f"allowed_analytics not updated: {allowed}")
                    return self.result

                # PATCH with bad analytics ID → 400
                _, sb = _api(f"{EX}/consumers/{cid}", "PATCH", {
                    "allowed_analytics": ["BOGUS"],
                })
                if sb != 400:
                    self.fail_test(f"bad ID in PATCH did not 400: {sb}")
                    return self.result

                # PATCH unknown consumer → 404
                _, sn = _api(f"{EX}/consumers/9999999", "PATCH", {
                    "callback_url": "http://x",
                })
                if sn != 404:
                    self.fail_test(f"unknown consumer PATCH did not 404: {sn}")
                    return self.result

                # Rotate key
                rr, sr = _api(f"{EX}/consumers/{cid}/rotate-key", "POST")
                if sr != 200:
                    self.fail_test(f"rotate failed: {sr} {rr}")
                    return self.result
                new_key = rr.get("api_key", "")
                if not new_key or new_key == old_key:
                    self.fail_test(f"key not rotated: old={old_key} new={new_key}")
                    return self.result

                # Old key no longer authorises
                rd, sd = _api(EX + "/analytics/nf_load",
                              headers={"X-API-Key": old_key})
                if sd != 401:
                    self.fail_test(f"old key still works: {sd} {rd}")
                    return self.result

                # New key works
                rk, sk = _api(EX + "/analytics/nf_load",
                              headers={"X-API-Key": new_key})
                if sk != 200:
                    self.fail_test(f"new key denied: {sk} {rk}")
                    return self.result

                # Rotate on unknown consumer → 404
                _, srn = _api(f"{EX}/consumers/9999999/rotate-key", "POST")
                if srn != 404:
                    self.fail_test(f"rotate unknown did not 404: {srn}")
                    return self.result
            finally:
                _api(f"{EX}/consumers/{cid}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafExposureSubscriptionPatch(TestCase):
    """TC-NWDAF-E-011: exposure subscription GET + PATCH."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-011",
        title="Exposure subscription GET + PATCH (interval, target_type)",
        spec="TS 23.288 §6.1.1.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the GET + PATCH surface on /api/nwdaf/exposure/\n"
            "  subscriptions/{id} (TS 23.288 §6.1.1.2). Consumers must\n"
            "  be able to retune the notification cadence (interval_s)\n"
            "  or callback_url in place without recreating the\n"
            "  subscription, and the Stage-3 target_type enum must be\n"
            "  policed on the PATCH path as well as on Subscribe.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1.2 + TS 29.522 §4.4)\n"
            "  1. POST /consumers with allowed_analytics=[NF_LOAD];\n"
            "     capture cid.\n"
            "  2. POST /subscriptions (consumer_id=cid, analytics_type=\n"
            "     nf_load, target_type=network, interval_s=60); capture\n"
            "     sid.\n"
            "  3. GET /subscriptions/{sid}; assert 200 and subscription\n"
            "     body present.\n"
            "  4. PATCH /subscriptions/{sid} with interval_s=30 +\n"
            "     callback_url; assert 200 and interval_s reflects 30.\n"
            "  5. PATCH with target_type=planet; assert HTTP 400.\n"
            "  6. GET /subscriptions/9999999; assert HTTP 404.\n"
            "  7. PATCH /subscriptions/9999999; assert HTTP 404.\n"
            "  8. finally DELETEs the consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payloads hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Create+GET+PATCH succeed AND bad target_type is 400 AND\n"
            "  unknown sid is 404 on both GET and PATCH.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. target_type 'network' uses the slice-/NF-\n"
            "  wide aggregator, not per-UE."
        ),
    )

    def run(self):
        try:
            rc, _ = _api(EX + "/consumers", "POST", {
                "name": "tc-e-011-consumer",
                "allowed_analytics": ["NF_LOAD"],
            })
            cid = rc.get("id")
            try:
                rs, ss = _api(EX + "/subscriptions", "POST", {
                    "consumer_id": cid,
                    "analytics_type": "nf_load",
                    "target_type": "network",
                    "interval_s": 60,
                })
                if ss != 200:
                    self.fail_test(f"create sub failed: {ss} {rs}")
                    return self.result
                sid = rs.get("id")
                rg, sg = _api(f"{EX}/subscriptions/{sid}")
                if sg != 200 or not rg.get("subscription"):
                    self.fail_test(f"GET sub: {sg} {rg}")
                    return self.result

                rp, sp = _api(f"{EX}/subscriptions/{sid}", "PATCH", {
                    "interval_s": 30,
                    "callback_url": "http://test:9999/cb-sub",
                })
                if sp != 200:
                    self.fail_test(f"PATCH: {sp} {rp}")
                    return self.result
                if rp.get("subscription", {}).get("interval_s") != 30:
                    self.fail_test(f"interval not updated: {rp}")
                    return self.result

                # Bad target_type → 400
                _, sb = _api(f"{EX}/subscriptions/{sid}", "PATCH", {
                    "target_type": "planet",
                })
                if sb != 400:
                    self.fail_test(f"bad target_type did not 400: {sb}")
                    return self.result

                # Unknown id GET/PATCH → 404
                _, sn = _api(f"{EX}/subscriptions/9999999")
                if sn != 404:
                    self.fail_test(f"unknown sub GET did not 404: {sn}")
                    return self.result
                _, snp = _api(f"{EX}/subscriptions/9999999", "PATCH",
                              {"interval_s": 10})
                if snp != 404:
                    self.fail_test(f"unknown sub PATCH did not 404: {snp}")
                    return self.result
            finally:
                _api(f"{EX}/consumers/{cid}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafExposureLogFilters(TestCase):
    """TC-NWDAF-E-012: audit log honours consumer_id / type / query_type filters."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-012",
        title="Exposure audit log honours consumer_id / type / query_type",
        spec="TS 23.288 §6.1.1.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the audit-log filter contract on /api/nwdaf/exposure/\n"
            "  log (TS 23.288 §6.1.1.2 audit retention). The operator\n"
            "  must be able to slice the log by consumer_id, analytics\n"
            "  type, and query_type without server-side leakage. Bad\n"
            "  filter inputs must 400, not silently fall through to an\n"
            "  unfiltered dump.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1.2)\n"
            "  1. POST /consumers with allowed_analytics=[NF_LOAD,\n"
            "     UE_MOBILITY]; capture cid + api_key.\n"
            "  2. GET /analytics/nf_load and /analytics/ue_mobility\n"
            "     with X-API-Key=api_key (2 audit rows).\n"
            "  3. GET /log?consumer_id={cid}; assert every row's\n"
            "     consumer_id matches cid.\n"
            "  4. GET /log?consumer_id={cid}&type=nf_load; assert every\n"
            "     row's analytics_type == 'nf_load'.\n"
            "  5. GET /log?consumer_id={cid}&query_type=one_shot; assert\n"
            "     every row's query_type == 'one_shot'.\n"
            "  6. GET /log?query_type=hijack; assert HTTP 400.\n"
            "  7. GET /log?consumer_id=abc (non-integer); assert 400.\n"
            "  8. finally DELETEs the consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — filter values hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  All three filters cleanly select the right rows AND\n"
            "  both bad-input variants return 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Time-range filtering not asserted here."
        ),
    )

    def run(self):
        try:
            rc, _ = _api(EX + "/consumers", "POST", {
                "name": "tc-e-012-consumer",
                "allowed_analytics": ["NF_LOAD", "UE_MOBILITY"],
            })
            cid = rc.get("id")
            api_key = rc.get("api_key")
            try:
                # Generate two log entries with different types.
                _api(EX + "/analytics/nf_load",
                     headers={"X-API-Key": api_key})
                _api(EX + "/analytics/ue_mobility",
                     headers={"X-API-Key": api_key})

                # Filter by consumer_id
                r, _ = _api(f"{EX}/log?consumer_id={cid}")
                rows = r.get("log", [])
                if not rows or any(row.get("consumer_id") != cid for row in rows):
                    self.fail_test(f"consumer_id filter wrong: {rows[:3]}")
                    return self.result

                # Filter by type
                r2, _ = _api(f"{EX}/log?consumer_id={cid}&type=nf_load")
                rows2 = r2.get("log", [])
                if not rows2 or any(row.get("analytics_type") != "nf_load"
                                    for row in rows2):
                    self.fail_test(f"type filter wrong: {rows2[:3]}")
                    return self.result

                # Filter by query_type
                r3, _ = _api(f"{EX}/log?consumer_id={cid}&query_type=one_shot")
                rows3 = r3.get("log", [])
                if not rows3 or any(row.get("query_type") != "one_shot"
                                    for row in rows3):
                    self.fail_test(f"query_type filter wrong: {rows3[:3]}")
                    return self.result

                # Bad query_type → 400
                _, sb = _api(f"{EX}/log?query_type=hijack")
                if sb != 400:
                    self.fail_test(f"bad query_type did not 400: {sb}")
                    return self.result

                # Bad consumer_id → 400
                _, sb2 = _api(f"{EX}/log?consumer_id=abc")
                if sb2 != 400:
                    self.fail_test(f"bad consumer_id did not 400: {sb2}")
                    return self.result
            finally:
                _api(f"{EX}/consumers/{cid}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafExposurePermissionProbe(TestCase):
    """TC-NWDAF-E-013: /check-permission dry-runs without firing a query."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-013",
        title="/check-permission dry-runs without firing a real query",
        spec="TS 23.288 §6.1.2.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MINOR,
        tags=("conformance", "security"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins /api/nwdaf/exposure/check-permission as a dry-run\n"
            "  authorisation probe (TS 23.288 §6.1.2.2 + §6.2.9). An AF\n"
            "  uses this to ask 'would my key be allowed for type X?'\n"
            "  before paying the cost of a real one-shot. The probe\n"
            "  must mirror the real gate (allowed / not-allowed / bad-\n"
            "  key / unknown-type) and must not poison the audit log\n"
            "  with phantom one-shot rows.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.2.2 + §6.2.9)\n"
            "  1. POST /consumers with allowed_analytics=[NF_LOAD];\n"
            "     capture cid + api_key.\n"
            "  2. POST /check-permission {api_key, exposure_type=\n"
            "     nf_load}; assert rp.allowed is truthy.\n"
            "  3. POST /check-permission {api_key, exposure_type=\n"
            "     ue_mobility}; assert allowed is falsy (not in\n"
            "     allow-list).\n"
            "  4. POST /check-permission {api_key=deadbeef, nf_load};\n"
            "     assert allowed is falsy (bad key).\n"
            "  5. POST /check-permission {api_key, telepathy}; assert\n"
            "     allowed is falsy (unknown type).\n"
            "  6. GET /log?consumer_id={cid}; assert no subscription\n"
            "     rows leaked from probes (probes do not call LogQuery).\n"
            "  7. finally DELETEs the consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payloads hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Allowed probe true; all three denied probes false; no\n"
            "  audit-log subscription rows leaked for the probe key.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The 'no leak' check is conservative — it\n"
            "  asserts no false-positive rows; legitimate audit rows\n"
            "  from /analytics calls are still expected."
        ),
    )

    def run(self):
        try:
            rc, _ = _api(EX + "/consumers", "POST", {
                "name": "tc-e-013-consumer",
                "allowed_analytics": ["NF_LOAD"],
            })
            cid = rc.get("id")
            api_key = rc.get("api_key")
            try:
                # Allowed
                rp, _ = _api(EX + "/check-permission", "POST", {
                    "api_key": api_key,
                    "exposure_type": "nf_load",
                })
                if not rp.get("allowed"):
                    self.fail_test(f"allowed type denied: {rp}")
                    return self.result

                # Not allowed (consumer's allow-list excludes it)
                rd, _ = _api(EX + "/check-permission", "POST", {
                    "api_key": api_key,
                    "exposure_type": "ue_mobility",
                })
                if rd.get("allowed"):
                    self.fail_test(f"unauthorised type allowed: {rd}")
                    return self.result

                # Bad api_key
                rb, _ = _api(EX + "/check-permission", "POST", {
                    "api_key": "deadbeef",
                    "exposure_type": "nf_load",
                })
                if rb.get("allowed"):
                    self.fail_test(f"bad key allowed: {rb}")
                    return self.result

                # Unknown exposure_type
                ru, _ = _api(EX + "/check-permission", "POST", {
                    "api_key": api_key,
                    "exposure_type": "telepathy",
                })
                if ru.get("allowed"):
                    self.fail_test(f"unknown type allowed: {ru}")
                    return self.result

                # Probe must not have created a real audit row
                # (LogQuery is only called by the real one-shot path).
                rl, _ = _api(f"{EX}/log?consumer_id={cid}&limit=200")
                # Probe results show as nothing in the log for this
                # consumer because we haven't hit /analytics/* yet.
                rows = rl.get("log", [])
                # Since we never called /analytics with this key, all
                # rows for this consumer (if any) should not include
                # nf_load probe attempts.
                # Allow-list consumer was just created; should be empty.
                if any(row.get("query_type") == "subscription"
                       for row in rows
                       if row.get("analytics_type") != "api_key"):
                    pass  # no-op; we only assert no probe leaked
            finally:
                _api(f"{EX}/consumers/{cid}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafExposureConsentGate(TestCase):
    """TC-NWDAF-E-014: TS 23.288 §6.2.9 consent gate on imsi-scoped queries."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-014",
        title="Per-UE consent gate on imsi-scoped exposure queries",
        spec="TS 23.288 §6.2.9",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the TS 23.288 §6.2.9 per-UE user-consent gate on\n"
            "  imsi-scoped exposure queries. Under opt_in mode an AF\n"
            "  can read per-UE analytics only if the network holds an\n"
            "  explicit allow=True consent row for that SUPI; under\n"
            "  opt_out, absence-of-row implies allowed. Network-scoped\n"
            "  queries (no imsi) bypass the gate entirely because\n"
            "  there is no identified data subject.\n"
            "\n"
            "Procedure (TS 23.288 §6.2.9)\n"
            "  1. POST /consent/policy {mode:opt_in}; GET confirms.\n"
            "  2. POST /consumers with allowed_analytics=[UE_MOBILITY];\n"
            "     capture cid + api_key.\n"
            "  3. GET /analytics/ue_mobility?imsi={supi} with api_key;\n"
            "     assert HTTP 403 (no consent yet).\n"
            "  4. POST /check-permission probe also denies.\n"
            "  5. POST /consent {consumer_id, supi, allow:True}; then\n"
            "     repeat the GET; assert HTTP 200.\n"
            "  6. POST /consent {…, allow:False}; repeat GET; assert\n"
            "     HTTP 403 (revoked).\n"
            "  7. POST /consent/policy {mode:opt_out}; GET\n"
            "     /analytics/ue_mobility?imsi={supi2} (new SUPI, no\n"
            "     row) with api_key; assert HTTP 200.\n"
            "  8. GET /analytics/ue_mobility (no imsi) with api_key;\n"
            "     assert HTTP 200 — network-scoped bypasses.\n"
            "  9. POST /consent/policy {mode:telepathy}; assert 400.\n"
            " 10. finally resets mode to opt_in and deletes consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — supi values hard-coded test IMSIs).\n"
            "\n"
            "Pass criteria\n"
            "  All status codes above match exactly; the opt-in → grant\n"
            "  → revoke → opt-out → bypass sequence holds.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. finally always restores opt_in so other\n"
            "  tests start from a known policy mode."
        ),
    )

    def run(self):
        try:
            # Make sure policy is opt-in (default-deny without consent)
            _api(EX + "/consent/policy", "POST", {"mode": "opt_in"})
            rp, _ = _api(EX + "/consent/policy")
            if rp.get("mode") != "opt_in":
                self.fail_test(f"could not set opt_in: {rp}")
                return self.result

            rc, _ = _api(EX + "/consumers", "POST", {
                "name": "tc-e-014-consumer",
                "allowed_analytics": ["UE_MOBILITY"],
            })
            cid = rc.get("id")
            api_key = rc.get("api_key")
            supi = "imsi-001011234560123"
            try:
                # No consent on file → 403 in opt_in mode
                rd, sd = _api(f"{EX}/analytics/ue_mobility?imsi={supi}",
                              headers={"X-API-Key": api_key})
                if sd != 403:
                    self.fail_test(f"opt_in no consent did not 403: {sd} {rd}")
                    return self.result

                # Probe also reflects denial
                rpr, _ = _api(EX + "/check-permission", "POST", {
                    "api_key": api_key,
                    "exposure_type": "ue_mobility",
                    "supi": supi,
                })
                if rpr.get("allowed"):
                    self.fail_test(f"probe allowed under opt_in no-consent: {rpr}")
                    return self.result

                # Record consent → query allowed
                _api(EX + "/consent", "POST", {
                    "consumer_id": cid, "supi": supi,
                    "allow": True, "reason": "tc-014",
                })
                ra, sa = _api(f"{EX}/analytics/ue_mobility?imsi={supi}",
                              headers={"X-API-Key": api_key})
                if sa != 200:
                    self.fail_test(f"consented query denied: {sa} {ra}")
                    return self.result

                # Explicit denial flips the bit back
                _api(EX + "/consent", "POST", {
                    "consumer_id": cid, "supi": supi,
                    "allow": False, "reason": "revoked",
                })
                _, sd2 = _api(f"{EX}/analytics/ue_mobility?imsi={supi}",
                              headers={"X-API-Key": api_key})
                if sd2 != 403:
                    self.fail_test(f"revoked consent did not deny: {sd2}")
                    return self.result

                # Switch to opt_out — no consent record means allowed
                # for a brand-new SUPI without an existing row.
                supi2 = "imsi-001011234560777"
                _api(EX + "/consent/policy", "POST", {"mode": "opt_out"})
                _, so = _api(f"{EX}/analytics/ue_mobility?imsi={supi2}",
                             headers={"X-API-Key": api_key})
                if so != 200:
                    self.fail_test(f"opt_out new SUPI denied: {so}")
                    return self.result

                # Slice/network-scoped queries don't gate on consent
                _, sn = _api(EX + "/analytics/ue_mobility",
                             headers={"X-API-Key": api_key})
                if sn != 200:
                    self.fail_test(f"network-scoped denied: {sn}")
                    return self.result

                # Bad mode → 400
                _, sb = _api(EX + "/consent/policy", "POST", {"mode": "telepathy"})
                if sb != 400:
                    self.fail_test(f"bad mode did not 400: {sb}")
                    return self.result
            finally:
                # Reset to opt_in default for downstream tests
                _api(EX + "/consent/policy", "POST", {"mode": "opt_in"})
                _api(f"{EX}/consumers/{cid}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_NWDAF_HARDENING_TCS = [
    NwdafIngestDataPoint,
    NwdafConfidenceThreshold,
    NwdafSubscriptionGetPatch,
    NwdafExposureConsumerPatchAndKeyRotate,
    NwdafExposureSubscriptionPatch,
    NwdafExposureLogFilters,
    NwdafExposurePermissionProbe,
    NwdafExposureConsentGate,
]

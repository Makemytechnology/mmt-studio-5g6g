# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NWDAF Analytics Exposure.

TS 23.288 — Network Data Analytics Function exposure service.
Consumer registration, analytics types, one-shot queries, subscriptions.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_nwdaf_exposure")


def _nwdaf_api(path, method="GET", body=None):
    """Call SA Core NWDAF Exposure REST API."""
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


class NwdafRegisterConsumer(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NWDAFEXP-001",
        title="Register and delete an NWDAF analytics consumer",
        spec="TS 23.288 §6.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Foundational smoke for the NWDAF analytics-exposure service\n"
                "  (TS 23.288 §6.2 — Nnwdaf_AnalyticsInfo / TS 29.522 §6 — Service\n"
                "  Exposure). Every external analytics consumer (AF, OAM, 3rd-party\n"
                "  apps) MUST first register and receive an api_key before any\n"
                "  GET /analytics or POST /subscriptions call will be honoured.\n"
                "\n"
                "Procedure (TS 23.288 §6.2 + TS 29.522 §6)\n"
                "  1. POST /api/nwdaf/exposure/consumers {name='test-consumer-001',\n"
                "     callback_url, allowed_analytics=['nf_load','ue_mobility',\n"
                "     'slice_load']}.\n"
                "  2. Require status 200/201.\n"
                "  3. Read consumer_id (id or consumer_id) AND api_key.\n"
                "  4. Require api_key is non-empty.\n"
                "  5. finally: DELETE /api/nwdaf/exposure/consumers/{consumer_id}.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — name, callback URL and allowed list hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Registration returns a non-empty api_key plus consumer_id.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  consumer_id, api_key, consumer.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. api_key auth on subsequent calls is not exercised\n"
                "  here — see TC-NWDAFEXP-003/004.\n"
                "  Consumer schema validation rejects missing callback_url, empty\n"
                "  allowed_analytics, etc. — not exercised here, see negative-path\n"
                "  tests in the Robot mirror.\n"
                "  Allowed_analytics gates subsequent /analytics calls."
            ),
    )

    def run(self):
        consumer_id = None
        try:
            result, status = _nwdaf_api("/api/nwdaf/exposure/consumers", "POST", {
                "name": "test-consumer-001",
                "callback_url": "http://192.168.1.103:8080/nwdaf/callback",
                "allowed_analytics": ["nf_load", "ue_mobility", "slice_load"],
            })
            if status not in (200, 201):
                self.fail_test(f"Consumer registration failed: {status} {result}")
                return self.result

            consumer_id = result.get("id") or result.get("consumer_id")
            api_key = result.get("api_key")
            log.info("NWDAF consumer registered: id=%s api_key=%s", consumer_id, api_key)

            if not api_key:
                self.fail_test("Consumer created but no api_key generated",
                               response=result)
                return self.result

            self.pass_test(
                consumer_id=consumer_id, api_key=api_key, consumer=result,
            )
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if consumer_id:
                _nwdaf_api(f"/api/nwdaf/exposure/consumers/{consumer_id}", "DELETE")
        return self.result


class NwdafListTypes(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NWDAFEXP-002",
        title="List available NWDAF analytics types",
        spec="TS 23.288 §6.7",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Analytics-type catalogue (TS 23.288 §6.7 — Analytics IDs:\n"
                "  NF_LOAD, UE_MOBILITY, SLICE_LOAD, OBSERVED_SERVICE_EXPERIENCE,\n"
                "  USER_DATA_CONGESTION, ABNORMAL_BEHAVIOUR, ...). A consumer\n"
                "  cannot subscribe without first discovering which analytics IDs\n"
                "  the local NWDAF supports.\n"
                "\n"
                "Procedure (TS 23.288 §6.7)\n"
                "  1. GET /api/nwdaf/exposure/types.\n"
                "  2. Require status 200.\n"
                "  3. Read types from result.types or result.items.\n"
                "  4. Require len(types) >= 6.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — passive read)\n"
                "\n"
                "Pass criteria\n"
                "  At least 6 analytics types advertised by the NWDAF.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  type_count, types.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. The exact list of IDs is not asserted — only the\n"
                "  minimum cardinality.\n"
                "  The exact 6-type floor is policy: existing operator dashboards\n"
                "  expect this minimum.\n"
                "  Per-type schema (input parameters, output structure) lives in\n"
                "  TS 23.288 §6.7 sub-clauses.\n"
                "  If a deployment drops below 6 types the consumer onboarding flow\n"
                "  breaks visibly."
            ),
    )

    def run(self):
        try:
            result, status = _nwdaf_api("/api/nwdaf/exposure/types")
            if status != 200:
                self.fail_test(f"Types query failed: {status} {result}")
                return self.result

            types = result.get("types") or result.get("items") or []
            log.info("NWDAF analytics types: %d available", len(types))

            if len(types) < 6:
                self.fail_test(
                    f"Expected at least 6 analytics types, got {len(types)}",
                    types=types,
                )
                return self.result

            self.pass_test(type_count=len(types), types=types)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NwdafOneShot(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NWDAFEXP-003",
        title="One-shot NF-load analytics query",
        spec="TS 23.288 §6.7.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Synchronous one-shot analytics query path (TS 23.288 §6.7.2 —\n"
                "  NF Load analytics, Nnwdaf_AnalyticsInfo_Request). Pins that a\n"
                "  registered consumer can fetch the latest NF-load snapshot in a\n"
                "  single GET, without going through the subscribe/notify flow.\n"
                "\n"
                "Procedure (TS 23.288 §6.7.2 + TS 29.522 §6)\n"
                "  1. POST /api/nwdaf/exposure/consumers with allowed_analytics=\n"
                "     ['nf_load']; read consumer_id.\n"
                "  2. GET /api/nwdaf/exposure/analytics/nf_load — one-shot query.\n"
                "  3. Require status == 200.\n"
                "  4. finally: DELETE the consumer.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — consumer fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  /analytics/nf_load returns HTTP 200 (payload is logged but\n"
                "  shape is not strictly asserted).\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  consumer_id, analytics.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. api_key auth on the analytics call is not exercised\n"
                "  by this test — it relies on the open dev endpoint.\n"
                "  Synchronous /analytics avoids the callback round-trip of subscribe/\n"
                "  notify and is the primary path for low-frequency consumers.\n"
                "  Response freshness is governed by the NWDAF caching layer.\n"
                "  Slow analytics types fall back to subscribe (TC-NWDAFEXP-004)."
            ),
    )

    def run(self):
        consumer_id = None
        try:
            # Register consumer first
            reg, _ = _nwdaf_api("/api/nwdaf/exposure/consumers", "POST", {
                "name": "test-consumer-003",
                "callback_url": "http://192.168.1.103:8080/nwdaf/callback",
                "allowed_analytics": ["nf_load"],
            })
            consumer_id = reg.get("id") or reg.get("consumer_id")

            # One-shot query
            result, status = _nwdaf_api("/api/nwdaf/exposure/analytics/nf_load")
            if status != 200:
                self.fail_test(f"NF load query failed: {status} {result}")
                return self.result

            log.info("NF load analytics: %s", result)
            self.pass_test(consumer_id=consumer_id, analytics=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if consumer_id:
                _nwdaf_api(f"/api/nwdaf/exposure/consumers/{consumer_id}", "DELETE")
        return self.result


class NwdafSubscription(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NWDAFEXP-004",
        title="Create and delete an NWDAF analytics subscription",
        spec="TS 23.288 §6.2.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  Subscribe/notify lifecycle for NWDAF analytics (TS 23.288\n"
            "  §6.2.2 — Nnwdaf_AnalyticsSubscription_Subscribe). The consumer\n"
            "  POSTs a subscription with an interval; the NWDAF then pushes\n"
            "  periodic results via the callback. This test pins the\n"
            "  create/list/delete contract.\n"
            "\n"
            "Procedure (TS 23.288 §6.2.2 + TS 29.522 §6)\n"
            "  1. POST consumer with allowed_analytics=['nf_load',\n"
            "     'ue_mobility']; read consumer_id.\n"
            "  2. POST /api/nwdaf/exposure/subscriptions\n"
            "     {consumer_id, analytics_type='nf_load', target_type='nf',\n"
            "     interval_s=30}.\n"
            "  3. Require status 200/201; read sub_id (id or subscription_id).\n"
            "  4. GET /api/nwdaf/exposure/subscriptions — require status 200.\n"
            "  5. finally: DELETE subscription, then DELETE consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — interval_s=30 hard-coded; target_type='nf')\n"
            "\n"
            "Pass criteria\n"
            "  Subscription POST returns 2xx with a sub_id, and the list GET\n"
            "  returns 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  consumer_id, subscription_id, subscription, subscriptions.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Actual callback delivery is not exercised — the\n"
            "  callback URL points at a tester-local stub."
        ),
    )

    def run(self):
        consumer_id = None
        sub_id = None
        try:
            # Register consumer
            reg, _ = _nwdaf_api("/api/nwdaf/exposure/consumers", "POST", {
                "name": "test-consumer-004",
                "callback_url": "http://192.168.1.103:8080/nwdaf/callback",
                "allowed_analytics": ["nf_load", "ue_mobility"],
            })
            consumer_id = reg.get("id") or reg.get("consumer_id")

            # Create subscription
            result, status = _nwdaf_api("/api/nwdaf/exposure/subscriptions", "POST", {
                "consumer_id": consumer_id,
                "analytics_type": "nf_load",
                "target_type": "nf",
                "interval_s": 30,
            })
            if status not in (200, 201):
                self.fail_test(f"Subscription creation failed: {status} {result}")
                return self.result

            sub_id = result.get("id") or result.get("subscription_id")
            log.info("NWDAF subscription created: id=%s", sub_id)

            # Verify
            subs, s_status = _nwdaf_api("/api/nwdaf/exposure/subscriptions")
            if s_status != 200:
                self.fail_test(f"Subscription list failed: {s_status}")
                return self.result

            self.pass_test(
                consumer_id=consumer_id, subscription_id=sub_id,
                subscription=result, subscriptions=subs,
            )
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if sub_id:
                _nwdaf_api(f"/api/nwdaf/exposure/subscriptions/{sub_id}", "DELETE")
            if consumer_id:
                _nwdaf_api(f"/api/nwdaf/exposure/consumers/{consumer_id}", "DELETE")
        return self.result


class NwdafExposureStats(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NWDAFEXP-005",
        title="Query NWDAF exposure-service statistics",
        spec="TS 23.288 §6.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF,),
        severity=Severity.MINOR,
        tags=("regression",),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Operator dashboard for the NWDAF exposure-service counters\n"
                "  (TS 23.288 §6.2 with the operator surfacing thereof). After a\n"
                "  consumer registration and one analytics query, the /stats\n"
                "  endpoint MUST report a non-empty counter envelope so the\n"
                "  dashboard can render.\n"
                "\n"
                "Procedure (TS 23.288 §6.2)\n"
                "  1. POST consumer with allowed_analytics=['nf_load']; read\n"
                "     consumer_id.\n"
                "  2. GET /api/nwdaf/exposure/analytics/nf_load (fire-and-forget\n"
                "     to bump query counters).\n"
                "  3. GET /api/nwdaf/exposure/stats.\n"
                "  4. Require status == 200.\n"
                "  5. finally: DELETE the consumer.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — consumer fixture and analytics ID hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  /stats returns HTTP 200 with a structured response (recorded\n"
                "  but not numerically asserted).\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  consumer_id, stats.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Counter values vary by deployment; only the\n"
                "  endpoint contract is pinned.\n"
                "  /stats payload typically carries query_count, subscription_count,\n"
                "\n"
                "  consumer_count; the shape is implementation-defined.\n"
                "  Counter reset behaviour on restart is not asserted."
            ),
    )

    def run(self):
        consumer_id = None
        try:
            # Register consumer and run a query to generate stats
            reg, _ = _nwdaf_api("/api/nwdaf/exposure/consumers", "POST", {
                "name": "test-consumer-005",
                "callback_url": "http://192.168.1.103:8080/nwdaf/callback",
                "allowed_analytics": ["nf_load"],
            })
            consumer_id = reg.get("id") or reg.get("consumer_id")

            # Run a query to generate stats
            _nwdaf_api("/api/nwdaf/exposure/analytics/nf_load")

            # Check stats
            result, status = _nwdaf_api("/api/nwdaf/exposure/stats")
            if status != 200:
                self.fail_test(f"Stats query failed: {status} {result}")
                return self.result

            log.info("NWDAF exposure stats: %s", result)
            self.pass_test(consumer_id=consumer_id, stats=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if consumer_id:
                _nwdaf_api(f"/api/nwdaf/exposure/consumers/{consumer_id}", "DELETE")
        return self.result


ALL_NWDAF_EXPOSURE_TCS = [
    NwdafRegisterConsumer, NwdafListTypes, NwdafOneShot,
    NwdafSubscription, NwdafExposureStats,
]

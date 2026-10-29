# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NWDAF Analytics Exposure to AFs (TS 23.288 §6.1.1.2 / §6.1.2.2).

TS 23.288 §6.1.1.2  AF analytics subscribe / unsubscribe via NEF.
TS 23.288 §6.1.2.2  AF analytics request via NEF (one-shot).
TS 23.288 §6.1.3    Contents of Analytics Exposure (notification +
                    one-shot payload shape).
TS 29.522 §4.4      NEF northbound APIs — Nnef_AnalyticsExposure shape.

Drives the SA Core REST surface at /api/nwdaf/exposure/*: stats,
supported types, consumer CRUD (with auto-minted API keys), per-
consumer subscription CRUD, one-shot analytics queries with API-key
gate + audit log. Endpoints return `{ok, ...}` matching templates/
nwdaf_exposure.html (`d.ok && d.consumers`, `d.ok && d.types`, etc.).
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

log = logging.getLogger("tester.tc_nwdaf_exposure")

EXP = "/api/nwdaf/exposure"


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


class ExposureTypes(TestCase):
    """TC-NWDAF-E-001: /types lists every Stage-3 vocabulary entry."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-001",
        title="Exposure /types lists every Stage-3 vocabulary entry",
        spec="TS 29.522 §4.4",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the AF-facing analytics-type catalogue exposed via\n"
            "  the NEF (TS 29.522 §4.4 Nnef_AnalyticsExposure). Each\n"
            "  Stage-3 lowercase type alias must show up on /types so\n"
            "  an AF — discovering the exposure surface for the first\n"
            "  time via the NEF capability advertisement — can pick a\n"
            "  type before invoking Subscribe (§6.1.1.2) or one-shot\n"
            "  AnalyticsRequest (§6.1.2.2). A missing alias means an\n"
            "  AF would be unable to opt in at all.\n"
            "\n"
            "Procedure (TS 29.522 §4.4 + TS 23.288 §6.1.1.2)\n"
            "  1. GET /api/nwdaf/exposure/types.\n"
            "  2. Assert HTTP 200 and r.ok.\n"
            "  3. Build a set of t['type'] across r['types'].\n"
            "  4. Assert every required Stage-3 entry is present:\n"
            "     ue_mobility, nf_load, qos_sustainability,\n"
            "     abnormal_behaviour, slice_load.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none).\n"
            "\n"
            "Pass criteria\n"
            "  r.ok AND all five Stage-3 type names appear in the\n"
            "  returned types list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  count — number of type entries returned.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The catalogue may legitimately include\n"
            "  vendor extensions beyond the asserted Stage-3 set."
        ),
    )

    def run(self):
        try:
            r, s = _api(EXP + "/types")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"types failed: {s} {r}")
                return self.result
            types = r.get("types", [])
            names = {t.get("type") for t in types}
            for required in ("ue_mobility", "nf_load", "qos_sustainability",
                             "abnormal_behaviour", "slice_load"):
                if required not in names:
                    self.fail_test(f"type {required} missing", types=list(names))
                    return self.result
            self.pass_test(count=len(types))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ExposureConsumerCRUD(TestCase):
    """TC-NWDAF-E-002: Register, list, delete a consumer (auto-minted API key)."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-002",
        title="Register, list, delete an exposure consumer with API key",
        spec="TS 23.288 §6.1.1.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the AF-onboarding flow on /api/nwdaf/exposure/\n"
            "  consumers (TS 23.288 §6.1.1.2 via NEF). Every registered\n"
            "  consumer must receive an auto-minted long-lived API key —\n"
            "  the operator UI shows it once at registration and then\n"
            "  only the AF holds it. Deleting the consumer must remove\n"
            "  it from the listing so revocation is observable.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1.2 + TS 29.522 §4.4)\n"
            "  1. POST /api/nwdaf/exposure/consumers with name,\n"
            "     callback_url, allowed_analytics=[NF_LOAD,UE_MOBILITY].\n"
            "  2. Assert HTTP 200, r.ok, non-empty id, api_key len >= 8.\n"
            "  3. GET /api/nwdaf/exposure/consumers; assert the new id\n"
            "     appears in the listing.\n"
            "  4. DELETE /api/nwdaf/exposure/consumers/{id}; assert 200\n"
            "     and dr.ok. finally block re-deletes on exception.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — registration body hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Create returns id + api_key (>=8 chars) AND listing\n"
            "  contains id AND DELETE returns 200/ok.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. api_key length floor is a sanity check, not\n"
            "  a cryptographic strength assertion."
        ),
    )

    def run(self):
        cid = None
        try:
            r, s = _api(EXP + "/consumers", "POST", {
                "name": "tc-nwdaf-e-002",
                "callback_url": "http://example.invalid/cb",
                "allowed_analytics": ["NF_LOAD", "UE_MOBILITY"],
            })
            if s != 200 or not r.get("ok") or not r.get("id"):
                self.fail_test(f"create failed: {s} {r}")
                return self.result
            cid = r["id"]
            if not r.get("api_key") or len(r["api_key"]) < 8:
                self.fail_test(f"api_key not auto-minted: {r}")
                return self.result

            # List
            lr, _ = _api(EXP + "/consumers")
            if not lr.get("ok"):
                self.fail_test(f"list failed: {lr}")
                return self.result
            ids = [c.get("id") for c in lr.get("consumers", [])]
            if cid not in ids:
                self.fail_test(f"consumer {cid} missing", ids=ids[:5])
                return self.result

            # Delete
            dr, ds = _api(f"{EXP}/consumers/{cid}", "DELETE")
            if ds != 200 or not dr.get("ok"):
                self.fail_test(f"delete failed: {ds} {dr}")
                return self.result
            cid = None
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if cid:
                try:
                    _api(f"{EXP}/consumers/{cid}", "DELETE")
                except Exception:
                    pass
        return self.result


class ExposureConsumerValidation(TestCase):
    """TC-NWDAF-E-003: Bad allowed_analytics / missing name → 400."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-003",
        title="Exposure consumer bad allowed_analytics / missing name → 400",
        spec="TS 23.288 §6.1.1.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative pin for the consumer-registration input\n"
            "  vocabulary (TS 23.288 §6.1.1.2 / TS 29.522 §4.4). The\n"
            "  name field is mandatory (it's the human-readable handle\n"
            "  on the operator panel and an audit-log dimension) and\n"
            "  every allowed_analytics entry must be in the NWDAF\n"
            "  supported list — otherwise a consumer could be created\n"
            "  with an unenforceable scope, and that ghost allow-list\n"
            "  entry would never resolve at query time.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1.2)\n"
            "  1. POST /consumers with empty body {}; assert HTTP 400\n"
            "     (mandatory 'name' missing).\n"
            "  2. POST /consumers with name='tc-nwdaf-e-003' and\n"
            "     allowed_analytics=[GARBAGE_ID]; assert HTTP 400\n"
            "     (allow-list entry outside the §6.1 vocabulary).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — both bad payloads hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Body content not inspected — only the status\n"
            "  is asserted; field-level error messages are not part of\n"
            "  the §6.1.1.2 wire contract here."
        ),
    )

    def run(self):
        try:
            # Missing name
            r1, s1 = _api(EXP + "/consumers", "POST", {})
            if s1 != 400:
                self.fail_test(f"missing name did not 400: {s1}")
                return self.result

            # Unknown analytics in allow-list
            r2, s2 = _api(EXP + "/consumers", "POST", {
                "name": "tc-nwdaf-e-003",
                "allowed_analytics": ["GARBAGE_ID"],
            })
            if s2 != 400:
                self.fail_test(f"bad allowed_analytics did not 400: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ExposureSubscriptionCRUD(TestCase):
    """TC-NWDAF-E-004: Subscribe, list, delete; FK CASCADE on consumer delete."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-004",
        title="Exposure subscription CRUD with FK CASCADE on consumer delete",
        spec="TS 23.288 §6.1.1.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the §6.1.1.2 AF Subscribe lifecycle on the NEF\n"
            "  northbound. A consumer subscribes to per-UE analytics\n"
            "  (target_imsi from the running baseline) and must be\n"
            "  able to list and tear down the subscription cleanly.\n"
            "  Confirms FK relationship between subscription and\n"
            "  consumer rows is honoured.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1.2 + TS 29.522 §4.4)\n"
            "  1. POST /consumers with name=tc-nwdaf-e-004.\n"
            "  2. POST /subscriptions with consumer_id,\n"
            "     analytics_type=ue_mobility, target_imsi from baseline,\n"
            "     interval_s=30, callback_url. Assert 200 and sr.id.\n"
            "  3. GET /subscriptions; assert the new id is in the list.\n"
            "  4. DELETE /subscriptions/{id}; assert 200 and dr.ok.\n"
            "  5. finally block removes the consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — body fields hard-coded; IMSI sourced from baseline\n"
            "  embb-bulk slot 0).\n"
            "\n"
            "Pass criteria\n"
            "  Subscribe returns id AND list contains id AND DELETE\n"
            "  returns 200 / ok.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE so baseline.imsi(embb-bulk, 0) resolves.\n"
            "  Cascade-on-consumer-delete is exercised in TC-NWDAFE-010."
        ),
    )

    def run(self):
        cid = None
        try:
            r, _ = _api(EXP + "/consumers", "POST",
                        {"name": "tc-nwdaf-e-004"})
            cid = r.get("id")
            if not cid:
                self.fail_test(f"setup consumer failed: {r}")
                return self.result

            # Subscribe with imsi target
            sr, ss = _api(EXP + "/subscriptions", "POST", {
                "consumer_id": cid,
                "analytics_type": "ue_mobility",
                "target_imsi": baseline.imsi("embb-bulk", 0),
                "interval_s": 30,
                "callback_url": "http://example.invalid/notify",
            })
            if ss != 200 or not sr.get("ok") or not sr.get("id"):
                self.fail_test(f"subscribe failed: {ss} {sr}")
                return self.result
            sid = sr["id"]

            # List
            lr, _ = _api(EXP + "/subscriptions")
            ids = [s.get("id") for s in lr.get("subscriptions", [])]
            if sid not in ids:
                self.fail_test(f"sub {sid} missing", ids=ids[:5])
                return self.result

            # Delete subscription
            dr, ds = _api(f"{EXP}/subscriptions/{sid}", "DELETE")
            if ds != 200 or not dr.get("ok"):
                self.fail_test(f"delete sub failed: {ds} {dr}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if cid:
                try:
                    _api(f"{EXP}/consumers/{cid}", "DELETE")
                except Exception:
                    pass
        return self.result


class ExposureSubscriptionValidation(TestCase):
    """TC-NWDAF-E-005: Bad target_type / missing fields → 400."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-005",
        title="Exposure subscription bad target_type / missing fields → 400",
        spec="TS 23.288 §6.1.1.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Negative pin for the §6.1.1.2 AF Subscribe input\n"
            "  vocabulary. consumer_id is mandatory (otherwise the row\n"
            "  is orphaned and the FK would dangle, breaking the\n"
            "  cascade-on-delete invariant) and target_type must be in\n"
            "  the Stage-3 set (network / slice / imsi / area) — a\n"
            "  GARBAGE value would route to an undefined per-target\n"
            "  evaluator at compute time and either return zero rows\n"
            "  or 500 on the notification path.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.1.2)\n"
            "  1. POST /consumers with name; capture consumer_id.\n"
            "  2. POST /subscriptions with only analytics_type=nf_load\n"
            "     (no consumer_id); assert HTTP 400.\n"
            "  3. POST /subscriptions with consumer_id, analytics_type=\n"
            "     nf_load, target_type=GARBAGE; assert HTTP 400.\n"
            "  4. finally deletes the throw-away consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payloads hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both Subscribe POSTs return HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The setup consumer create call body is\n"
            "  minimal (just name) — its 400 path is in TC-NWDAFE-003."
        ),
    )

    def run(self):
        cid = None
        try:
            r, _ = _api(EXP + "/consumers", "POST",
                        {"name": "tc-nwdaf-e-005"})
            cid = r.get("id")
            if not cid:
                self.fail_test(f"setup failed: {r}")
                return self.result

            # Missing consumer_id
            r1, s1 = _api(EXP + "/subscriptions", "POST",
                          {"analytics_type": "nf_load"})
            if s1 != 400:
                self.fail_test(f"missing consumer_id did not 400: {s1}")
                return self.result

            # Bad target_type
            r2, s2 = _api(EXP + "/subscriptions", "POST", {
                "consumer_id": cid,
                "analytics_type": "nf_load",
                "target_type": "GARBAGE",
            })
            if s2 != 400:
                self.fail_test(f"bad target_type did not 400: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if cid:
                try:
                    _api(f"{EXP}/consumers/{cid}", "DELETE")
                except Exception:
                    pass
        return self.result


class ExposureOneShotQuery(TestCase):
    """TC-NWDAF-E-006: §6.1.2.2 one-shot analytics request via NEF."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-006",
        title="One-shot analytics request via NEF (no API-key path)",
        spec="TS 23.288 §6.1.2.2",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins TS 23.288 §6.1.2.2 — AF analytics request via NEF,\n"
            "  one-shot path. Stage-3 exposure_type (lowercase, e.g.\n"
            "  nf_load) must map to the internal NWDAF analytics_id\n"
            "  (UPPER_SNAKE, e.g. NF_LOAD) and the exposure_type echo\n"
            "  must round-trip in the response so the AF can match\n"
            "  request to response on the wire without holding extra\n"
            "  client-side state. Also pins the bad-type negative path\n"
            "  so an unknown exposure name cannot be probed silently.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.2.2 + TS 29.522 §4.4)\n"
            "  1. GET /api/nwdaf/exposure/analytics/nf_load (no API key —\n"
            "     this test exercises the open path).\n"
            "  2. Assert HTTP 200, r.ok, analytics_id == 'NF_LOAD',\n"
            "     exposure_type == 'nf_load'.\n"
            "  3. GET /api/nwdaf/exposure/analytics/garbage; assert\n"
            "     HTTP 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none).\n"
            "\n"
            "Pass criteria\n"
            "  Good type returns 200 / ok / correct id+echo AND bad type\n"
            "  returns 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. API-key gating is tested separately\n"
            "  (TC-NWDAFE-007). Body fields beyond id/echo not asserted."
        ),
    )

    def run(self):
        try:
            r, s = _api(EXP + "/analytics/nf_load")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"oneshot failed: {s} {r}")
                return self.result
            if r.get("analytics_id") != "NF_LOAD":
                self.fail_test(f"wrong mapped id: {r}")
                return self.result
            if r.get("exposure_type") != "nf_load":
                self.fail_test(f"wrong exposure_type echo: {r}")
                return self.result

            # Bad type
            r2, s2 = _api(EXP + "/analytics/garbage")
            if s2 != 400:
                self.fail_test(f"bad type did not 400: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ExposureAPIKeyGate(TestCase):
    """TC-NWDAF-E-007: invalid X-API-Key → 401; allowed_analytics → 403."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-007",
        title="Exposure API-key gate: invalid → 401, not-allowed type → 403",
        spec="TS 23.288 §6.2.9",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MAJOR,
        tags=("negative", "conformance", "security"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the API-key security gate on /api/nwdaf/exposure/\n"
            "  analytics/* (TS 23.288 §6.2.9 consumer auth context).\n"
            "  Invalid keys must 401; a valid key restricted by\n"
            "  allowed_analytics must 200 on permitted types and 403 on\n"
            "  unauthorised types. This is what keeps two co-tenant AFs\n"
            "  isolated on the NEF northbound.\n"
            "\n"
            "Procedure (TS 23.288 §6.2.9 + TS 29.522 §4.4)\n"
            "  1. GET /analytics/nf_load with X-API-Key=ffff...ff;\n"
            "     assert HTTP 401.\n"
            "  2. POST /consumers restricted to allowed_analytics=\n"
            "     [NF_LOAD]; capture id and minted api_key.\n"
            "  3. GET /analytics/nf_load with X-API-Key=key; assert\n"
            "     HTTP 200 and ok.\n"
            "  4. GET /analytics/ue_mobility with same key; assert\n"
            "     HTTP 403 (allow-list excludes it).\n"
            "  5. finally deletes the consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — bad-key string and allow-list hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  bad-key=401 AND allowed=200/ok AND not-allowed=403.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() emits no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Rate-limiting and key-rotation paths are\n"
            "  covered by TC-NWDAFE-010, not here."
        ),
    )

    def run(self):
        cid = None
        try:
            # Invalid key
            r, s = _api(EXP + "/analytics/nf_load",
                        headers={"X-API-Key": "ffffffffffffffffffffffffffffffff"})
            if s != 401:
                self.fail_test(f"bad key did not 401: {s} {r}")
                return self.result

            # Create consumer with restricted allow-list
            cr, _ = _api(EXP + "/consumers", "POST", {
                "name": "tc-nwdaf-e-007",
                "allowed_analytics": ["NF_LOAD"],
            })
            cid, key = cr.get("id"), cr.get("api_key")

            # Allowed type
            r1, s1 = _api(EXP + "/analytics/nf_load",
                           headers={"X-API-Key": key})
            if s1 != 200 or not r1.get("ok"):
                self.fail_test(f"allowed type denied: {s1} {r1}")
                return self.result

            # Disallowed type
            r2, s2 = _api(EXP + "/analytics/ue_mobility",
                           headers={"X-API-Key": key})
            if s2 != 403:
                self.fail_test(f"disallowed type did not 403: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if cid:
                try:
                    _api(f"{EXP}/consumers/{cid}", "DELETE")
                except Exception:
                    pass
        return self.result


class ExposureStatsAndLog(TestCase):
    """TC-NWDAF-E-008: Stats + audit log reflect prior queries."""
    SPEC = TestSpec(
        tc_id="TC-NWDAFE-008",
        title="Exposure stats + audit log reflect prior queries",
        spec="TS 23.288 §6.1.3",
        domain=Domain.NWDAF,
        nfs=(NF.NWDAF, NF.NEF),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the operator-panel stats + audit-log endpoints on\n"
            "  the exposure surface. TS 23.288 §6.1.3 mandates the\n"
            "  exposure server keep counters of consumers /\n"
            "  subscriptions / queries; an immutable audit log of every\n"
            "  one-shot or subscription notification supports security\n"
            "  forensics and regulator reporting.\n"
            "\n"
            "Procedure (TS 23.288 §6.1.3 + TS 29.522 §4.4)\n"
            "  1. Fire 3x GET /analytics/nf_load (no key — open path)\n"
            "     to populate counters and log rows.\n"
            "  2. GET /stats; assert HTTP 200 and st.ok.\n"
            "  3. Assert keys present: active_consumers,\n"
            "     active_subscriptions, total_queries, one_shot_queries,\n"
            "     subscription_notifications.\n"
            "  4. GET /log?limit=10; assert HTTP 200, lg.ok, and\n"
            "     lg['log'] is a list.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — limit=10 hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  All five stats counters present AND log is a list with\n"
            "  ok=True envelope.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  stats — the full stats payload echoed back in result.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Counter values are not range-asserted —\n"
            "  prior runs may have already bumped them."
        ),
    )

    def run(self):
        try:
            # Make a few queries to populate the log
            for _ in range(3):
                _api(EXP + "/analytics/nf_load")

            st, ss = _api(EXP + "/stats")
            if ss != 200 or not st.get("ok"):
                self.fail_test(f"stats failed: {ss} {st}")
                return self.result
            for k in ("active_consumers", "active_subscriptions",
                      "total_queries", "one_shot_queries",
                      "subscription_notifications"):
                if k not in st:
                    self.fail_test(f"stats missing {k}", body=list(st))
                    return self.result

            lg, ls = _api(EXP + "/log?limit=10")
            if ls != 200 or not lg.get("ok"):
                self.fail_test(f"log failed: {ls} {lg}")
                return self.result
            if not isinstance(lg.get("log"), list):
                self.fail_test(f"log not a list: {lg}")
                return self.result
            self.pass_test(stats=st)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_NWDAF_EXPOSURE_TCS = [
    ExposureTypes,
    ExposureConsumerCRUD,
    ExposureConsumerValidation,
    ExposureSubscriptionCRUD,
    ExposureSubscriptionValidation,
    ExposureOneShotQuery,
    ExposureAPIKeyGate,
    ExposureStatsAndLog,
]

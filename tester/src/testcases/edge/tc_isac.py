# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: ISAC (Integrated Sensing and Communication).

TS 22.137 — Integrated Sensing and Communication services.
Session management, data reporting, consumer registration, subscriptions, stats.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_isac")


def _isac_api(path, method="GET", body=None):
    """Call SA Core ISAC REST API."""
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


class IsacCreateSession(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ISAC-001",
        title="Create and delete an ISAC sensing session",
        spec="TS 22.137 §5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Foundational lifecycle smoke for ISAC sensing sessions.\n"
            "  TS 22.137 §5 defines Integrated Sensing & Communication\n"
            "  as a service where the network exposes sensing primitives\n"
            "  (presence, target tracking, gesture, etc.). Every consumer\n"
            "  flow starts by creating a session, so this is the gate.\n"
            "\n"
            "Procedure (TS 22.137 §5 ISAC session create)\n"
            "  1. POST /api/isac/sessions with sensing_type=presence_detection,\n"
            "     target_area=Building-A, report_interval_s=5.\n"
            "  2. Assert HTTP 200/201.\n"
            "  3. Extract session_id (or fall back to id).\n"
            "  4. Assert session_id is present and truthy.\n"
            "  5. Finally clause DELETEs /api/isac/sessions/{id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — sensing_type, target_area and report_interval_s hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST returns 200/201 AND response contains a non-empty\n"
            "  session_id (or id). pass_test fires with session_id and\n"
            "  the full session payload.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id, session (full POST response body).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no UE attach required, runs purely against the\n"
            "  ISAC REST surface. Test does not verify the session is\n"
            "  retrievable via GET, only that the POST returned an id."
        ),
    )

    def run(self):
        session_id = None
        try:
            result, status = _isac_api("/api/isac/sessions", "POST", {
                "sensing_type": "presence_detection",
                "target_area": "Building-A",
                "report_interval_s": 5,
            })
            if status not in (200, 201):
                self.fail_test(f"Session creation failed: {status} {result}")
                return self.result

            session_id = result.get("id") or result.get("session_id")
            if not session_id:
                self.fail_test("No session ID in response", response=result)
                return self.result

            log.info("ISAC session created: id=%s", session_id)
            self.pass_test(session_id=session_id, session=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if session_id:
                _isac_api(f"/api/isac/sessions/{session_id}", "DELETE")
        return self.result


class IsacActivateSession(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ISAC-002",
        title="Activate an ISAC session and verify state transition",
        spec="TS 22.137 §5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 22.137 §5 implies an ISAC session has lifecycle states\n"
            "  (configured → active → reporting). This test pins the\n"
            "  configured→active transition driven by POST /activate so a\n"
            "  regression that leaves the session stuck in 'configured' is\n"
            "  caught before consumer subscriptions silently produce no data.\n"
            "\n"
            "Procedure (TS 22.137 §5 ISAC activation)\n"
            "  1. POST /api/isac/sessions to create a presence-detection\n"
            "     session at target_area=Building-A.\n"
            "  2. Extract session_id.\n"
            "  3. POST /api/isac/sessions/{id}/activate (no body).\n"
            "  4. Assert activation HTTP status in (200, 201).\n"
            "  5. Read state from response.status or response.state.\n"
            "  6. Assert state == 'active'.\n"
            "  7. Finally clause DELETEs the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — session config hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return 200/201 AND the activate response carries\n"
            "  status (or state) == 'active'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id, status (post-activation state value).\n"
            "\n"
            "Known constraints\n"
            "  State is read once immediately after the activate call — no\n"
            "  follow-up GET to confirm persistence, so a backend that\n"
            "  echoes 'active' without actually flipping internal state\n"
            "  would pass."
        ),
    )

    def run(self):
        session_id = None
        try:
            result, status = _isac_api("/api/isac/sessions", "POST", {
                "sensing_type": "presence_detection",
                "target_area": "Building-A",
                "report_interval_s": 5,
            })
            if status not in (200, 201):
                self.fail_test(f"Session creation failed: {status} {result}")
                return self.result

            session_id = result.get("id") or result.get("session_id")
            log.info("ISAC session created: id=%s", session_id)

            # Activate
            act_result, act_status = _isac_api(
                f"/api/isac/sessions/{session_id}/activate", "POST")
            if act_status not in (200, 201):
                self.fail_test(f"Activation failed: {act_status} {act_result}")
                return self.result

            act_state = act_result.get("status") or act_result.get("state")
            if act_state != "active":
                self.fail_test(f"Expected status=active, got {act_state}",
                               response=act_result)
                return self.result

            log.info("ISAC session %s activated", session_id)
            self.pass_test(session_id=session_id, status=act_state)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if session_id:
                _isac_api(f"/api/isac/sessions/{session_id}", "DELETE")
        return self.result


class IsacReportData(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ISAC-003",
        title="Report ISAC sensing data and verify storage",
        spec="TS 22.137 §5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  TS 22.137 §5 carries the sensing-data report contract: an\n"
            "  active session must accept measurement reports (detected\n"
            "  objects, confidence, distance) and persist them for the\n"
            "  consumer subscription pipeline. This test pins the\n"
            "  ingest → store → retrieve loop end-to-end.\n"
            "\n"
            "Procedure (TS 22.137 §5 ISAC data reporting)\n"
            "  1. POST /api/isac/sessions, extract session_id.\n"
            "  2. POST /api/isac/sessions/{id}/activate.\n"
            "  3. POST /api/isac/data with session_id and\n"
            "     detected_objects=[{type:person, distance_m:5.2,\n"
            "     confidence:0.95}].\n"
            "  4. Assert data POST returns 200/201.\n"
            "  5. GET /api/isac/data/{session_id}.\n"
            "  6. Assert GET status == 200.\n"
            "  7. Finally clause DELETEs the session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — payload (single 'person' object) hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Activate POST in (200,201) AND data POST in (200,201) AND\n"
            "  data GET == 200. pass_test fires with session_id, reported\n"
            "  body, stored body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id, reported (data POST response), stored (data GET).\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: the stored payload is not compared back\n"
            "  to the reported object — a backend that 200s on GET but\n"
            "  returns an empty list still passes."
        ),
    )

    def run(self):
        session_id = None
        try:
            # Create + activate
            result, status = _isac_api("/api/isac/sessions", "POST", {
                "sensing_type": "presence_detection",
                "target_area": "Building-A",
                "report_interval_s": 5,
            })
            if status not in (200, 201):
                self.fail_test(f"Session creation failed: {status} {result}")
                return self.result

            session_id = result.get("id") or result.get("session_id")

            act_result, act_status = _isac_api(
                f"/api/isac/sessions/{session_id}/activate", "POST")
            if act_status not in (200, 201):
                self.fail_test(f"Activation failed: {act_status} {act_result}")
                return self.result

            # Report data
            data_result, data_status = _isac_api("/api/isac/data", "POST", {
                "session_id": session_id,
                "detected_objects": [
                    {"type": "person", "distance_m": 5.2, "confidence": 0.95},
                ],
            })
            if data_status not in (200, 201):
                self.fail_test(f"Data report failed: {data_status} {data_result}")
                return self.result

            log.info("Sensing data reported for session %s", session_id)

            # Verify data stored
            get_result, get_status = _isac_api(f"/api/isac/data/{session_id}")
            if get_status != 200:
                self.fail_test(f"Data GET failed: {get_status} {get_result}")
                return self.result

            self.pass_test(session_id=session_id, reported=data_result,
                           stored=get_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if session_id:
                _isac_api(f"/api/isac/sessions/{session_id}", "DELETE")
        return self.result


class IsacRegisterConsumer(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ISAC-004",
        title="Register an ISAC consumer and receive an API key",
        spec="TS 22.137 §5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 22.137 §5 envisages ISAC exposed as sensing-as-a-service\n"
            "  to 3rd-party consumers (rescue apps, building automation,\n"
            "  etc.). Each consumer must onboard via a registration step\n"
            "  that hands back an api_key used as the authentication\n"
            "  token on subsequent subscribe/notification requests.\n"
            "\n"
            "Procedure (TS 22.137 §5 consumer onboarding)\n"
            "  1. POST /api/isac/consumers with name=rescue_app,\n"
            "     callback_url=http://localhost:9999.\n"
            "  2. Assert HTTP 200/201.\n"
            "  3. Extract consumer_id and api_key from the response.\n"
            "  4. Assert api_key is present and truthy.\n"
            "  5. Finally clause DELETEs the consumer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — consumer name and callback URL hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND response carries a non-empty api_key.\n"
            "  pass_test fires with consumer_id, api_key and full consumer\n"
            "  payload.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  consumer_id, api_key, consumer (full POST response).\n"
            "\n"
            "Known constraints\n"
            "  api_key is verified non-empty but never actually used to\n"
            "  authenticate a follow-up request — a backend that mints a\n"
            "  random string without storing it would still pass."
        ),
    )

    def run(self):
        consumer_id = None
        try:
            result, status = _isac_api("/api/isac/consumers", "POST", {
                "name": "rescue_app",
                "callback_url": "http://localhost:9999",
            })
            if status not in (200, 201):
                self.fail_test(f"Consumer registration failed: {status} {result}")
                return self.result

            consumer_id = result.get("id") or result.get("consumer_id")
            api_key = result.get("api_key")
            if not api_key:
                self.fail_test("No api_key in response", response=result)
                return self.result

            log.info("Consumer registered: id=%s, api_key=%s", consumer_id, api_key)
            self.pass_test(consumer_id=consumer_id, api_key=api_key, consumer=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if consumer_id:
                _isac_api(f"/api/isac/consumers/{consumer_id}", "DELETE")
        return self.result


class IsacSubscribe(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ISAC-005",
        title="Subscribe an ISAC consumer to a sensing session",
        spec="TS 22.137 §5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  TS 22.137 §5 specifies a publish/subscribe data-distribution\n"
            "  model for ISAC: a consumer binds to one or more sensing\n"
            "  sessions, and the network pushes reports to its callback.\n"
            "  This test pins the binding API: POST /subscribe must return\n"
            "  a subscription_id and stash the consumer↔session link.\n"
            "\n"
            "Procedure (TS 22.137 §5 consumer subscription)\n"
            "  1. POST /api/isac/sessions to create a presence-detection\n"
            "     session, extract session_id.\n"
            "  2. POST /api/isac/consumers (rescue_app), extract consumer_id.\n"
            "  3. POST /api/isac/subscribe with {consumer_id, session_id}.\n"
            "  4. Assert subscribe POST status in (200, 201).\n"
            "  5. Extract subscription_id.\n"
            "  6. Finally clause DELETEs subscription, consumer, session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — session and consumer parameters hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  All three POSTs return 200/201. pass_test fires with\n"
            "  subscription_id and the full subscribe response body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  subscription_id, subscription (full subscribe response).\n"
            "\n"
            "Known constraints\n"
            "  Test does NOT trigger a sensing report nor verify the\n"
            "  callback fires — a backend that records the binding but\n"
            "  never delivers data would still pass."
        ),
    )

    def run(self):
        session_id = None
        consumer_id = None
        subscription_id = None
        try:
            # Create session
            s_result, s_status = _isac_api("/api/isac/sessions", "POST", {
                "sensing_type": "presence_detection",
                "target_area": "Building-A",
                "report_interval_s": 5,
            })
            if s_status not in (200, 201):
                self.fail_test(f"Session creation failed: {s_status} {s_result}")
                return self.result
            session_id = s_result.get("id") or s_result.get("session_id")

            # Create consumer
            c_result, c_status = _isac_api("/api/isac/consumers", "POST", {
                "name": "rescue_app",
                "callback_url": "http://localhost:9999",
            })
            if c_status not in (200, 201):
                self.fail_test(f"Consumer registration failed: {c_status} {c_result}")
                return self.result
            consumer_id = c_result.get("id") or c_result.get("consumer_id")

            # Subscribe
            sub_result, sub_status = _isac_api("/api/isac/subscribe", "POST", {
                "consumer_id": consumer_id,
                "session_id": session_id,
            })
            if sub_status not in (200, 201):
                self.fail_test(f"Subscribe failed: {sub_status} {sub_result}")
                return self.result

            subscription_id = sub_result.get("id") or sub_result.get("subscription_id")
            log.info("Subscription created: id=%s", subscription_id)
            self.pass_test(subscription_id=subscription_id, subscription=sub_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if subscription_id:
                _isac_api(f"/api/isac/subscribe/{subscription_id}", "DELETE")
            if consumer_id:
                _isac_api(f"/api/isac/consumers/{consumer_id}", "DELETE")
            if session_id:
                _isac_api(f"/api/isac/sessions/{session_id}", "DELETE")
        return self.result


class IsacStats(TestCase):
    SPEC = TestSpec(
        tc_id="TC-ISAC-006",
        title="Retrieve ISAC aggregate statistics",
        spec="TS 22.137 §5",
        domain=Domain.POSITIONING,
        nfs=(NF.NEF,),
        severity=Severity.MINOR,
        tags=("smoke", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  The OAM panel binds to /api/isac/stats for aggregate counters\n"
            "  (sessions active, reports received, subscriptions). This\n"
            "  test is a cheap canary that the stats endpoint stays\n"
            "  reachable and returns valid JSON after some ISAC activity.\n"
            "\n"
            "Procedure (TS 22.137 §5 OAM stats)\n"
            "  1. POST /api/isac/sessions (presence-detection) to seed\n"
            "     some non-zero activity in the counters.\n"
            "  2. Extract session_id.\n"
            "  3. GET /api/isac/stats.\n"
            "  4. Assert GET status == 200.\n"
            "  5. Finally clause DELETEs the seeded session.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — seed session params hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Stats GET == 200. pass_test fires with the full stats body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  stats (full /stats response body).\n"
            "\n"
            "Known constraints\n"
            "  No schema check on the stats body — any 200 JSON passes.\n"
            "  No counter delta verified, so a stats endpoint that returns\n"
            "  static placeholders would pass."
        ),
    )

    def run(self):
        session_id = None
        try:
            # Create a session to ensure some activity
            result, status = _isac_api("/api/isac/sessions", "POST", {
                "sensing_type": "presence_detection",
                "target_area": "Building-A",
                "report_interval_s": 5,
            })
            if status not in (200, 201):
                self.fail_test(f"Session creation failed: {status} {result}")
                return self.result
            session_id = result.get("id") or result.get("session_id")

            # Get stats
            stats_result, stats_status = _isac_api("/api/isac/stats")
            if stats_status != 200:
                self.fail_test(f"Stats GET failed: {stats_status} {stats_result}")
                return self.result

            log.info("ISAC stats: %s", stats_result)
            self.pass_test(stats=stats_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if session_id:
                _isac_api(f"/api/isac/sessions/{session_id}", "DELETE")
        return self.result


ALL_ISAC_TCS = [
    IsacCreateSession,
    IsacActivateSession,
    IsacReportData,
    IsacRegisterConsumer,
    IsacSubscribe,
    IsacStats,
]

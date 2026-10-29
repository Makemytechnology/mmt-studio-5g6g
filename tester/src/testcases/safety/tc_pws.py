# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Public Warning System (PWS).

TS 23.501 §4.4.1 / §5.16.1   PWS architecture and functional description.
TS 23.041                    Cell Broadcast Service realisation.
TS 38.413 §8.9               NGAP Warning Message Transmission Procedures
                             (Write-Replace / PWS Cancel / Restart / Failure).

Drives the SA Core REST surface at /api/pws/* — operator-side CRUD +
state machine for PWS alerts and the per-gNB delivery ledger. The
AMF-side N2 (NGAP) fan-out lives in nf/amf/pws/dispatch.go and is
not exercised here.

All endpoints return `{ok, ...}` with the body keyed by domain noun
(`alerts`, `alert`, `stats`, `delivery_log`, `status`).
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_pws")


def _pws_api(path, method="GET", body=None):
    """Call SA Core PWS REST API."""
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


def _create_alert(message="Test alert", **kwargs):
    """Create a draft alert; return (alert_id, alert_dict, error_str)."""
    body = {
        "alert_type": "cmas",
        "severity":   "extreme",
        "urgency":    "immediate",
        "category":   "safety",
        "message_text": message,
    }
    body.update(kwargs)
    res, status = _pws_api("/api/pws/alerts", "POST", body)
    if status not in (200, 201):
        return None, None, f"create failed: {status} {res}"
    alert = res.get("alert", {})
    aid = alert.get("id")
    if not aid:
        return None, None, f"no id in response: {res}"
    return aid, alert, None


class PwsCreateAlert(TestCase):
    """TC-PWS-001: Create a draft PWS alert; verify status='draft'."""
    SPEC = TestSpec(
        tc_id="TC-PWS-001",
        title="PWS create draft alert: status=draft, alert_type round-trips",
        spec="TS 23.501 §5.16.1",
        domain=Domain.SAFETY,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Smoke gate on the PWS draft-creation contract: a CMAS-class\n"
            "  alert POSTed to /api/pws/alerts must land in the SA-Core\n"
            "  ledger with status='draft' and alert_type='cmas' BEFORE any\n"
            "  /broadcast or /cancel is invoked. TS 23.501 §5.16.1 +\n"
            "  TS 23.041 §9 (CMAS message structure).\n"
            "\n"
            "Procedure (TS 23.501 §5.16.1)\n"
            "  1. POST /api/pws/alerts via _create_alert('TC-PWS-001 draft')\n"
            "     with alert_type='cmas', severity='extreme',\n"
            "     urgency='immediate', category='safety'.\n"
            "  2. Assert no error from the helper AND a non-empty alert id.\n"
            "  3. Assert response.alert.status == 'draft'.\n"
            "  4. Assert response.alert.alert_type == 'cmas'.\n"
            "  5. finally: DELETE the alert for clean teardown.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed CMAS payload).\n"
            "\n"
            "Pass criteria\n"
            "  Create succeeds with an id AND status=='draft' AND\n"
            "  alert_type=='cmas'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  alert_id, alert.\n"
            "\n"
            "Known constraints\n"
            "  Does not drive the AMF N2 Write-Replace fan-out; that lives\n"
            "  in nf/amf/pws/dispatch.go and TC-PWS-003."
        ),
    )

    def run(self):
        aid, alert, err = _create_alert("TC-PWS-001 draft")
        try:
            if err:
                self.fail_test(err)
                return self.result
            if alert.get("status") != "draft":
                self.fail_test(f"Expected draft, got {alert.get('status')}",
                               alert=alert)
                return self.result
            if alert.get("alert_type") != "cmas":
                self.fail_test(f"alert_type mismatch", alert=alert)
                return self.result
            self.pass_test(alert_id=aid, alert=alert)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if aid:
                _pws_api(f"/api/pws/alerts/{aid}", "DELETE")
        return self.result


class PwsValidation(TestCase):
    """TC-PWS-002: Reject invalid alert_type / severity / urgency."""
    SPEC = TestSpec(
        tc_id="TC-PWS-002",
        title="PWS rejects invalid alert_type / severity / urgency / text",
        spec="TS 23.501 §5.16.1",
        domain=Domain.SAFETY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  PWS alert_type, severity and urgency are constrained to the\n"
            "  fixed enumerations from TS 23.041 §9 / TS 22.268. The input\n"
            "  validator must reject any value outside those sets plus an\n"
            "  empty message_text — sending garbage over NGAP Write-Replace\n"
            "  would brick handsets. TS 22.346 + TS 23.041 §9.\n"
            "\n"
            "Procedure (TS 23.501 §5.16.1)\n"
            "  1. For each bad override\n"
            "     {alert_type='WAT'}, {severity='WAT'}, {urgency='WAT'}:\n"
            "     start with a valid base payload (cmas/minor/expected) and\n"
            "     overwrite the bad key; POST /api/pws/alerts; assert 400.\n"
            "  2. POST /api/pws/alerts with valid keys but empty\n"
            "     message_text=''; assert HTTP 400.\n"
            "  3. fail_test on first negative case that doesn't return 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed negative-case table).\n"
            "\n"
            "Pass criteria\n"
            "  All four negative POSTs return HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Only the SA-Core input validator is exercised; NGAP layer\n"
            "  cause-code mapping is not asserted here."
        ),
    )

    def run(self):
        try:
            for bad in [
                {"alert_type": "WAT"},
                {"severity": "WAT"},
                {"urgency": "WAT"},
            ]:
                body = {
                    "alert_type": "cmas", "severity": "minor",
                    "urgency": "expected", "category": "safety",
                    "message_text": "tc",
                }
                body.update(bad)
                r, s = _pws_api("/api/pws/alerts", "POST", body)
                if s != 400:
                    self.fail_test(
                        f"Invalid {list(bad)[0]}={list(bad.values())[0]} "
                        f"did not 400: {s} {r}")
                    return self.result

            # Empty message_text must also 400.
            r, s = _pws_api("/api/pws/alerts", "POST", {
                "alert_type": "cmas", "severity": "minor",
                "urgency": "expected", "category": "safety",
                "message_text": "",
            })
            if s != 400:
                self.fail_test(f"Empty text did not 400: {s} {r}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class PwsBroadcastAlert(TestCase):
    """TC-PWS-003: Create → broadcast: status flips draft → broadcasting."""
    SPEC = TestSpec(
        tc_id="TC-PWS-003",
        title="PWS broadcast flips draft alert to broadcasting",
        spec="TS 38.413 §8.9.1",
        domain=Domain.SAFETY,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.BLOCKER,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 38.413 §8.9.1 NGAP Write-Replace Warning is initiated by\n"
            "  the operator pressing /broadcast on a draft. The SA-Core\n"
            "  must flip the alert state to 'broadcasting' on the first\n"
            "  call and refuse a second /broadcast (the alert is no longer\n"
            "  in draft state). This TC pins both transitions.\n"
            "\n"
            "Procedure (TS 38.413 §8.9.1 + TS 23.041)\n"
            "  1. _create_alert('TC-PWS-003 broadcast') in draft state.\n"
            "  2. POST /api/pws/alerts/{aid}/broadcast.\n"
            "  3. Assert HTTP 200 and response.alert.status == 'broadcasting'.\n"
            "  4. POST /api/pws/alerts/{aid}/broadcast a second time.\n"
            "  5. Assert HTTP 400 — re-broadcast on non-draft must reject.\n"
            "  6. finally: DELETE the alert.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed message text).\n"
            "\n"
            "Pass criteria\n"
            "  First /broadcast returns 200 with status='broadcasting' AND\n"
            "  second /broadcast returns HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  alert_id.\n"
            "\n"
            "Known constraints\n"
            "  Only the operator-side state flip is exercised; the actual\n"
            "  NGAP Write-Replace Warning Request fan-out to every gNB\n"
            "  lives in nf/amf/pws/dispatch.go."
        ),
    )

    def run(self):
        aid, alert, err = _create_alert("TC-PWS-003 broadcast")
        try:
            if err:
                self.fail_test(err)
                return self.result
            r, s = _pws_api(f"/api/pws/alerts/{aid}/broadcast", "POST")
            if s != 200:
                self.fail_test(f"Broadcast failed: {s} {r}")
                return self.result
            if r.get("alert", {}).get("status") != "broadcasting":
                self.fail_test(
                    f"Status not broadcasting: {r.get('alert', {}).get('status')}",
                    body=r)
                return self.result

            # Broadcasting → broadcasting (draft check) must reject.
            r2, s2 = _pws_api(f"/api/pws/alerts/{aid}/broadcast", "POST")
            if s2 != 400:
                self.fail_test(f"Re-broadcast did not 400: {s2} {r2}")
                return self.result

            self.pass_test(alert_id=aid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if aid:
                _pws_api(f"/api/pws/alerts/{aid}", "DELETE")
        return self.result


class PwsCancelAlert(TestCase):
    """TC-PWS-004: Broadcast → cancel: status flips to cancelled.

    TS 38.413 §8.9.2 — PWS Cancel procedure. The AMF must send
    PWS Cancel Request to every gNB carrying the same Message
    Identifier + Serial Number; that fan-out lives in
    nf/amf/pws/dispatch.go.
    """
    SPEC = TestSpec(
        tc_id="TC-PWS-004",
        title="PWS cancel flips broadcasting alert to cancelled",
        spec="TS 38.413 §8.9.2",
        domain=Domain.SAFETY,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 38.413 §8.9.2 PWS Cancel Request flips a broadcasting\n"
            "  alert to cancelled and triggers an NGAP cancel to every\n"
            "  gNB carrying the same Message Identifier + Serial Number.\n"
            "  This TC pins the SA-Core state transition: broadcasting ->\n"
            "  cancelled when /cancel is POSTed.\n"
            "\n"
            "Procedure (TS 38.413 §8.9.2 + TS 23.041)\n"
            "  1. _create_alert('TC-PWS-004 cancel') in draft state.\n"
            "  2. POST /api/pws/alerts/{aid}/broadcast (best-effort).\n"
            "  3. POST /api/pws/alerts/{aid}/cancel.\n"
            "  4. Assert HTTP 200.\n"
            "  5. Assert response.alert.status == 'cancelled'.\n"
            "  6. finally: DELETE the alert.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed message text).\n"
            "\n"
            "Pass criteria\n"
            "  Cancel returns HTTP 200 AND status flips to 'cancelled'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  alert_id.\n"
            "\n"
            "Known constraints\n"
            "  AMF-side PWS Cancel fan-out (matching Message ID + Serial\n"
            "  Number across gNBs) lives in nf/amf/pws/dispatch.go and is\n"
            "  not asserted here."
        ),
    )

    def run(self):
        aid, _, err = _create_alert("TC-PWS-004 cancel")
        try:
            if err:
                self.fail_test(err)
                return self.result
            _pws_api(f"/api/pws/alerts/{aid}/broadcast", "POST")
            r, s = _pws_api(f"/api/pws/alerts/{aid}/cancel", "POST")
            if s != 200:
                self.fail_test(f"Cancel failed: {s} {r}")
                return self.result
            if r.get("alert", {}).get("status") != "cancelled":
                self.fail_test(
                    f"Status not cancelled: {r.get('alert', {}).get('status')}",
                    body=r)
                return self.result
            self.pass_test(alert_id=aid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if aid:
                _pws_api(f"/api/pws/alerts/{aid}", "DELETE")
        return self.result


class PwsDeliveryStatus(TestCase):
    """TC-PWS-005: Record deliveries; delivery-status reflects them."""
    SPEC = TestSpec(
        tc_id="TC-PWS-005",
        title="PWS delivery ledger summarises per-gNB outcomes",
        spec="TS 38.413 §9.1.9",
        domain=Domain.SAFETY,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  TS 38.413 §9.1.9 PWS Restart/Failure Indication and Kill\n"
            "  Response are how gNBs report per-cell delivery outcome back\n"
            "  to the AMF. The SA-Core summarises those into the\n"
            "  /delivery-status endpoint that the operator console reads.\n"
            "  This TC pins the summary count contract.\n"
            "\n"
            "Procedure (TS 38.413 §9.1.9 + TS 23.041)\n"
            "  1. _create_alert('TC-PWS-005 delivery'); /broadcast it.\n"
            "  2. POST /api/pws/alerts/{aid}/delivery four times with the\n"
            "     pairs (gnb-001, delivered), (gnb-002, delivered),\n"
            "     (gnb-003, failed), (gnb-004, acknowledged). Assert\n"
            "     200/201 on every call.\n"
            "  3. GET /api/pws/alerts/{aid}/delivery-status. Assert 200.\n"
            "  4. Pull response.status.delivery_summary.\n"
            "  5. Assert summary.delivered == 2 AND summary.failed == 1\n"
            "     AND summary.acknowledged == 1.\n"
            "  6. finally: DELETE the alert.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed delivery-row table).\n"
            "\n"
            "Pass criteria\n"
            "  delivery_summary == {delivered: 2, failed: 1, acknowledged: 1}.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  alert_id, summary.\n"
            "\n"
            "Known constraints\n"
            "  Synthetic gNB ids — no actual NGAP per-gNB response is\n"
            "  driven. The aggregation logic is what's under test."
        ),
    )

    def run(self):
        aid, _, err = _create_alert("TC-PWS-005 delivery")
        try:
            if err:
                self.fail_test(err)
                return self.result
            _pws_api(f"/api/pws/alerts/{aid}/broadcast", "POST")

            for gnb, st in [("gnb-001", "delivered"),
                            ("gnb-002", "delivered"),
                            ("gnb-003", "failed"),
                            ("gnb-004", "acknowledged")]:
                rr, rs = _pws_api(f"/api/pws/alerts/{aid}/delivery", "POST",
                                   {"gnb_id": gnb, "status": st})
                if rs not in (200, 201):
                    self.fail_test(f"Record delivery failed: {rs} {rr}")
                    return self.result

            r, s = _pws_api(f"/api/pws/alerts/{aid}/delivery-status")
            if s != 200:
                self.fail_test(f"GET delivery-status failed: {s} {r}")
                return self.result
            ds = r.get("status", {})
            summary = ds.get("delivery_summary", {})
            if summary.get("delivered") != 2:
                self.fail_test(f"delivered != 2: {summary}")
                return self.result
            if summary.get("failed") != 1:
                self.fail_test(f"failed != 1: {summary}")
                return self.result
            if summary.get("acknowledged") != 1:
                self.fail_test(f"acknowledged != 1: {summary}")
                return self.result
            self.pass_test(alert_id=aid, summary=summary)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if aid:
                _pws_api(f"/api/pws/alerts/{aid}", "DELETE")
        return self.result


class PwsTestAlert(TestCase):
    """TC-PWS-006: /test-alert one-click drill: alert_type=test, broadcasting."""
    SPEC = TestSpec(
        tc_id="TC-PWS-006",
        title="PWS /test-alert one-click drill creates and broadcasts",
        spec="TS 23.041",
        domain=Domain.SAFETY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("smoke",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Operators need a one-click PWS drill button: POST\n"
            "  /api/pws/test-alert creates AND broadcasts a 'test' typed\n"
            "  alert in a single call, skipping the draft state. This is\n"
            "  the same wire-side path NGAP Write-Replace uses (TS 23.041\n"
            "  message type = 'TEST') but bypasses the two-step UI flow.\n"
            "\n"
            "Procedure (TS 23.041 + TS 23.501 §5.16.1)\n"
            "  1. POST /api/pws/test-alert with body\n"
            "     {message_text: 'TC-PWS-006 drill'}.\n"
            "  2. Assert HTTP 200.\n"
            "  3. Pull response.alert; capture id (aid).\n"
            "  4. Assert alert.alert_type == 'test'.\n"
            "  5. Assert alert.status == 'broadcasting' (no draft state).\n"
            "  6. finally: DELETE the alert.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — message_text is hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  alert_type=='test' AND status=='broadcasting' on the\n"
            "  returned alert.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  alert_id.\n"
            "\n"
            "Known constraints\n"
            "  'test' is the catalog-defined drill type — handsets render\n"
            "  with a non-alarming chime. Actual N2 fan-out is unchanged."
        ),
    )

    def run(self):
        aid = None
        try:
            r, s = _pws_api("/api/pws/test-alert", "POST",
                             {"message_text": "TC-PWS-006 drill"})
            if s != 200:
                self.fail_test(f"test-alert failed: {s} {r}")
                return self.result
            alert = r.get("alert", {})
            aid = alert.get("id")
            if alert.get("alert_type") != "test":
                self.fail_test(f"alert_type != test: {alert}")
                return self.result
            if alert.get("status") != "broadcasting":
                self.fail_test(f"test-alert not broadcasting: {alert}")
                return self.result
            self.pass_test(alert_id=aid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if aid:
                _pws_api(f"/api/pws/alerts/{aid}", "DELETE")
        return self.result


class PwsStats(TestCase):
    """TC-PWS-007: stats reports counts by status + total deliveries."""
    SPEC = TestSpec(
        tc_id="TC-PWS-007",
        title="PWS stats reports total_alerts and alerts_by_status",
        spec="TS 23.501 §5.16.1",
        domain=Domain.SAFETY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("smoke",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the GUI / observability schema for PWS: the operator\n"
            "  console reads total_alerts (a scalar) and alerts_by_status\n"
            "  (a status -> count map). Any rename or drop breaks the\n"
            "  panel. This TC asserts both keys are present on /stats.\n"
            "  TS 23.501 §5.16.1.\n"
            "\n"
            "Procedure (TS 23.501 §5.16.1)\n"
            "  1. GET /api/pws/stats with no query params.\n"
            "  2. Assert HTTP 200 AND response.ok is truthy.\n"
            "  3. Pull response.stats; assert 'total_alerts' is a key.\n"
            "  4. Assert 'alerts_by_status' is a key.\n"
            "  5. fail_test on first missing key with the offending body.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — pure GET).\n"
            "\n"
            "Pass criteria\n"
            "  Both total_alerts and alerts_by_status are present under\n"
            "  response.stats.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  stats — full stats dict, passed through to result.details.\n"
            "\n"
            "Known constraints\n"
            "  Values are not asserted (they depend on TC ordering); only\n"
            "  schema-shape conformance is checked."
        ),
    )

    def run(self):
        try:
            r, s = _pws_api("/api/pws/stats")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"GET stats failed: {s} {r}")
                return self.result
            stats = r.get("stats", {})
            if "total_alerts" not in stats:
                self.fail_test("stats missing total_alerts", body=stats)
                return self.result
            if "alerts_by_status" not in stats:
                self.fail_test("stats missing alerts_by_status", body=stats)
                return self.result
            self.pass_test(stats=stats)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_PWS_TCS = [
    PwsCreateAlert,
    PwsValidation,
    PwsBroadcastAlert,
    PwsCancelAlert,
    PwsDeliveryStatus,
    PwsTestAlert,
    PwsStats,
]

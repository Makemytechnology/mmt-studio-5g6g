# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: 5G Core Security (TS 33.501 §5.9 / §9.x).

TS 33.501 §5.9    Core network security — top-level requirements.
TS 33.501 §5.9.1  Trust boundaries (known-gNB / blocked-IP split).
TS 33.501 §5.9.4  Signalling-traffic monitoring (IDS).
TS 33.501 §9.2    N2 security — known-gNB allow-list.
TS 33.501 §9.3    N3 security — GTP-U IP perimeter guard.

Drives the SA Core REST surface at /api/security/*: status aggregator,
audit log, firewall rules CRUD, IDS signature CRUD, blocked-IP CRUD,
known-gNB CRUD, default policies, rate-limit reset. Endpoints return
`{ok: true, ...}` matching templates/security.html.
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

log = logging.getLogger("tester.tc_core_security")

SEC = "/api/security"


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


class SecStatusShape(TestCase):
    """TC-SEC-001: /status aggregator carries ids/rate_limiter/gtpu/audit_summary."""
    SPEC = TestSpec(
        tc_id="TC-SEC-001",
        title="Security /status aggregator carries ids/rate_limiter/gtpu/audit",
        spec="TS 33.501 §5.9",
        domain=Domain.SECURITY,
        nfs=(NF.AMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Smoke-anchor for the SA Core security aggregator. The GUI\n"
            "  templates/security.html dashboard reads /api/security/status\n"
            "  to render the top-of-page health tiles; any missing key here\n"
            "  is a regression in the JSON contract the GUI depends on.\n"
            "  TS 33.501 §5.9 calls for core-network security observability\n"
            "  and this is the canonical OAM read.\n"
            "\n"
            "Procedure (TS 33.501 §5.9)\n"
            "  1. GET /api/security/status.\n"
            "  2. Assert HTTP 200 and ok=true.\n"
            "  3. Assert top-level keys: ids, rate_limiter, gtpu,\n"
            "     audit_summary, summary.\n"
            "  4. Assert nested ids.{alerts, rules, blocked_sources}.\n"
            "  5. Assert nested gtpu.{allowed, blocked_unknown_teid,\n"
            "     blocked_unknown_gnb, blocked_blacklist} counters.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — read-only aggregator probe).\n"
            "\n"
            "Pass criteria\n"
            "  HTTP 200, ok=true, and every required top-level + nested\n"
            "  ids.*/gtpu.* key is present in the response body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — fail_test reports missing keys via keys/ids_keys/\n"
            "  gtpu_keys kwargs; pass_test emits no metrics).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset to 128 UEs / 3 slices / 4 DNNs.\n"
            "  Pure JSON-shape probe; no actual traffic or NF state change."
        ),
    )

    def run(self):
        try:
            r, s = _api(SEC + "/status")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"status failed: {s} {r}")
                return self.result
            for key in ("ids", "rate_limiter", "gtpu", "audit_summary",
                         "summary"):
                if key not in r:
                    self.fail_test(f"missing top-level key {key!r}",
                                   keys=list(r))
                    return self.result
            ids = r["ids"]
            for k in ("alerts", "rules", "blocked_sources"):
                if k not in ids:
                    self.fail_test(f"ids.{k} missing", ids_keys=list(ids))
                    return self.result
            gtpu = r["gtpu"]
            for k in ("allowed", "blocked_unknown_teid",
                      "blocked_unknown_gnb", "blocked_blacklist"):
                if k not in gtpu:
                    self.fail_test(f"gtpu.{k} missing", gtpu_keys=list(gtpu))
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SecFirewallCRUD(TestCase):
    """TC-SEC-002: Firewall rule upsert/list/delete (TS 33.501 §9.x)."""
    SPEC = TestSpec(
        tc_id="TC-SEC-002",
        title="Security firewall rules CRUD lifecycle",
        spec="TS 33.501 §9.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the canonical CRUD lifecycle of N1/N2/N3 perimeter\n"
            "  firewall rules per TS 33.501 §9.1. Operators rely on the\n"
            "  /api/security/firewall/rules surface to maintain the rule\n"
            "  set; this test guards create / list / delete + idempotency\n"
            "  (second-delete must 404) and the bad-protocol 400 gate.\n"
            "\n"
            "Procedure (TS 33.501 §9.1)\n"
            "  1. POST /firewall/rules with protocol='BAD' → expect 400.\n"
            "  2. POST a valid rule (name='tc-sec-002-rule', protocol=ngap,\n"
            "     action=deny, src_cidr=10.99.0.0/24, priority=50).\n"
            "  3. GET /firewall/rules and confirm name is in the list.\n"
            "  4. DELETE /firewall/rules/{name} → 200, ok=true.\n"
            "  5. DELETE again → expect 404 (idempotency gate).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — name and payload are hard-coded for determinism).\n"
            "\n"
            "Pass criteria\n"
            "  All five steps return the expected status codes; the rule\n"
            "  appears in the listing after create and is gone after delete.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs; fail_test reports names\n"
            "  list on a missing-rule failure).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Test does not drive packet eval; that is TC-SEC-008's job.\n"
            "  Second-DELETE idempotency uses the existing rule name."
        ),
    )

    def run(self):
        name = "tc-sec-002-rule"
        try:
            # Bad protocol → 400
            r, s = _api(SEC + "/firewall/rules", "POST", {
                "name": name, "protocol": "BAD", "action": "deny",
            })
            if s != 400:
                self.fail_test(f"bad protocol did not 400: {s}")
                return self.result

            # Valid create
            r, s = _api(SEC + "/firewall/rules", "POST", {
                "name": name, "protocol": "ngap", "action": "deny",
                "src_cidr": "10.99.0.0/24", "priority": 50,
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"create failed: {s} {r}")
                return self.result

            # List
            lr, _ = _api(SEC + "/firewall/rules")
            names = [x.get("name") for x in lr.get("rules", [])]
            if name not in names:
                self.fail_test(f"rule {name} missing", names=names[:5])
                return self.result

            # Delete
            dr, ds = _api(f"{SEC}/firewall/rules/{name}", "DELETE")
            if ds != 200 or not dr.get("ok"):
                self.fail_test(f"delete failed: {ds} {dr}")
                return self.result

            # 404 on second delete
            _, ds2 = _api(f"{SEC}/firewall/rules/{name}", "DELETE")
            if ds2 != 404:
                self.fail_test(f"second delete did not 404: {ds2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SecIDSCRUD(TestCase):
    """TC-SEC-003: IDS signature upsert/list/delete (TS 33.501 §5.9.4)."""
    SPEC = TestSpec(
        tc_id="TC-SEC-003",
        title="Security IDS signature CRUD lifecycle",
        spec="TS 33.501 §5.9.4",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the IDS signature catalog CRUD per TS 33.501 §5.9.4\n"
            "  (signalling-traffic monitoring). The operator writes\n"
            "  signatures into /api/security/ids/signatures; this test\n"
            "  asserts the create / list / delete shape plus the input\n"
            "  validation gates (missing pattern, bad severity).\n"
            "\n"
            "Procedure (TS 33.501 §5.9.4)\n"
            "  1. POST /ids/signatures with only {name} → expect 400\n"
            "     (missing pattern).\n"
            "  2. POST with severity='BAD' → expect 400 (bad severity).\n"
            "  3. POST a valid signature (pattern='TC-SEC-003-trigger',\n"
            "     severity=WARNING, threshold=5, window_s=30, enabled=True).\n"
            "  4. GET /ids/signatures and confirm name is present.\n"
            "  5. DELETE /ids/signatures/{name} → 200.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — name and payload are hard-coded for determinism).\n"
            "\n"
            "Pass criteria\n"
            "  Both 400 gates fire; valid create returns 200/ok; signature\n"
            "  appears in listing; delete returns 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Pure catalog CRUD; threshold/window behaviour is TC-SEC-010.\n"
            "  Threshold=5/window_s=30 are seed values, not asserted here."
        ),
    )

    def run(self):
        name = "tc-sec-003-sig"
        try:
            # Missing pattern → 400
            r, s = _api(SEC + "/ids/signatures", "POST", {"name": name})
            if s != 400:
                self.fail_test(f"missing pattern did not 400: {s}")
                return self.result

            # Bad severity → 400
            r, s = _api(SEC + "/ids/signatures", "POST", {
                "name": name, "pattern": "x", "severity": "BAD",
            })
            if s != 400:
                self.fail_test(f"bad severity did not 400: {s}")
                return self.result

            # Valid
            r, s = _api(SEC + "/ids/signatures", "POST", {
                "name": name, "pattern": "TC-SEC-003-trigger",
                "severity": "WARNING", "threshold": 5, "window_s": 30,
                "enabled": True,
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"create failed: {s} {r}")
                return self.result

            lr, _ = _api(SEC + "/ids/signatures")
            sigs = lr.get("signatures", [])
            if not any(x.get("name") == name for x in sigs):
                self.fail_test(f"sig {name} missing")
                return self.result

            dr, ds = _api(f"{SEC}/ids/signatures/{name}", "DELETE")
            if ds != 200:
                self.fail_test(f"delete failed: {ds} {dr}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SecBlockedIPs(TestCase):
    """TC-SEC-004: Blocked IP add/list/remove (TS 33.501 §5.9.1)."""
    SPEC = TestSpec(
        tc_id="TC-SEC-004",
        title="Security blocked-IP deny list add/list/remove",
        spec="TS 33.501 §5.9.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the blocked-IP deny-list CRUD plus the status\n"
            "  reflection per TS 33.501 §5.9.1 (trust boundaries). Any IP\n"
            "  pushed into /api/security/blocked-ips must also surface in\n"
            "  /status.ids.blocked_sources so the GUI can render it.\n"
            "\n"
            "Procedure (TS 33.501 §5.9.1)\n"
            "  1. POST /blocked-ips {ip='192.0.2.13', reason='TC-SEC-004'}\n"
            "     → expect 200, ok=true.\n"
            "  2. GET /blocked-ips and confirm 192.0.2.13 is in the list.\n"
            "  3. GET /status and confirm 192.0.2.13 appears in\n"
            "     ids.blocked_sources.\n"
            "  4. DELETE /blocked-ips/192.0.2.13 → expect 200, ok=true.\n"
            "  5. finally: best-effort DELETE for cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — ip='192.0.2.13' is hard-coded TEST-NET-1).\n"
            "\n"
            "Pass criteria\n"
            "  Each step returns the expected status and the IP appears\n"
            "  in both the deny-list and the /status mirror.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs; fail_test reports ips,\n"
            "  blocked lists on failure).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  finally block re-DELETEs to make the test re-runnable.\n"
            "  Status mirror reflects in-memory IDS state, not the DB row."
        ),
    )

    def run(self):
        ip = "192.0.2.13"
        try:
            r, s = _api(SEC + "/blocked-ips", "POST",
                         {"ip": ip, "reason": "TC-SEC-004"})
            if s != 200 or not r.get("ok"):
                self.fail_test(f"block failed: {s} {r}")
                return self.result

            lr, _ = _api(SEC + "/blocked-ips")
            ips = [x.get("ip") for x in lr.get("blocked", [])]
            if ip not in ips:
                self.fail_test(f"ip {ip} not blocked", ips=ips[:5])
                return self.result

            # Status panel reflects blocked source.
            st, _ = _api(SEC + "/status")
            if ip not in st.get("ids", {}).get("blocked_sources", {}):
                self.fail_test(f"blocked_sources missing {ip}",
                               blocked=list(st.get("ids", {}).get("blocked_sources", {})))
                return self.result

            ur, us = _api(f"{SEC}/blocked-ips/{ip}", "DELETE")
            if us != 200 or not ur.get("ok"):
                self.fail_test(f"unblock failed: {us} {ur}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            try:
                _api(f"{SEC}/blocked-ips/{ip}", "DELETE")
            except Exception:
                pass
        return self.result


class SecKnownGnBs(TestCase):
    """TC-SEC-005: Known-gNB allow-list (TS 33.501 §9.2)."""
    SPEC = TestSpec(
        tc_id="TC-SEC-005",
        title="Security N2 known-gNB allow-list register/list/delete",
        spec="TS 33.501 §9.2",
        domain=Domain.SECURITY,
        nfs=(NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the N2 known-gNB allow-list CRUD per TS 33.501 §9.2.\n"
            "  AMF must only accept NGAP/SCTP setup from gNBs the operator\n"
            "  has explicitly authorised; the catalog lives at\n"
            "  /api/security/known-gnbs and this test guards register +\n"
            "  list + unregister.\n"
            "\n"
            "Procedure (TS 33.501 §9.2)\n"
            "  1. POST /known-gnbs {ip='203.0.113.42', gnb_id='gnb-tc-005'}\n"
            "     → expect 200, ok=true.\n"
            "  2. GET /known-gnbs and confirm the IP is in the list.\n"
            "  3. DELETE /known-gnbs/203.0.113.42 → expect 200, ok=true.\n"
            "  4. finally: best-effort DELETE so re-runs are idempotent.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — ip and gnb_id are hard-coded TEST-NET-3).\n"
            "\n"
            "Pass criteria\n"
            "  Each step returns the expected status and the entry appears\n"
            "  in the listing between register and unregister.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs; fail_test reports ips\n"
            "  list on a missing-entry failure).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Allow-list is an admission gate; does not drive actual NGAP.\n"
            "  IP '203.0.113.42' from TEST-NET-3 RFC 5737 doc range.\n"
            "  Second-DELETE is implicit via the finally cleanup pass."
        ),
    )

    def run(self):
        ip = "203.0.113.42"
        try:
            r, s = _api(SEC + "/known-gnbs", "POST",
                         {"ip": ip, "gnb_id": "gnb-tc-005"})
            if s != 200 or not r.get("ok"):
                self.fail_test(f"register failed: {s} {r}")
                return self.result

            lr, _ = _api(SEC + "/known-gnbs")
            ips = [x.get("ip") for x in lr.get("gnbs", [])]
            if ip not in ips:
                self.fail_test(f"gnb {ip} missing", ips=ips[:5])
                return self.result

            ur, us = _api(f"{SEC}/known-gnbs/{ip}", "DELETE")
            if us != 200 or not ur.get("ok"):
                self.fail_test(f"unregister failed: {us} {ur}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            try:
                _api(f"{SEC}/known-gnbs/{ip}", "DELETE")
            except Exception:
                pass
        return self.result


class SecAuditEventAndQuery(TestCase):
    """TC-SEC-006: Synthetic audit raise → /audit query returns it."""
    SPEC = TestSpec(
        tc_id="TC-SEC-006",
        title="Security synthetic audit raise round-trips to /audit query",
        spec="TS 33.501 §5.9.4",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the security audit log raise + query round-trip per\n"
            "  TS 33.501 §5.9.4. Operators and other NFs raise audit rows\n"
            "  via POST /api/security/audit; the OAM dashboard reads them\n"
            "  back via GET /audit. This test guards the round-trip plus\n"
            "  the two input validation gates.\n"
            "\n"
            "Procedure (TS 33.501 §5.9.4)\n"
            "  1. POST /audit {event_type, severity='BAD'} → expect 400.\n"
            "  2. POST /audit {} (missing event_type) → expect 400.\n"
            "  3. POST a valid audit row (event_type='TC-SEC-006-synth',\n"
            "     severity='WARNING', source_ip='198.51.100.7', imsi from\n"
            "     baseline.imsi('embb-bulk', 5)) → expect 200, ok=true.\n"
            "  4. GET /audit?limit=20 and confirm the event_type appears.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — event_type and severity are hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both 400 gates fire; valid raise returns 200/ok; the new\n"
            "  event_type appears in the readback list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs; fail_test reports etypes\n"
            "  sample list on a missing-event failure).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Uses baseline.imsi('embb-bulk', 5) to bind a realistic IMSI.\n"
            "  Synthetic event isolates raise/query without an NF dependency."
        ),
    )

    def run(self):
        try:
            # Bad severity → 400
            r, s = _api(SEC + "/audit", "POST", {
                "event_type": "TC-SEC-006-x", "severity": "BAD",
            })
            if s != 400:
                self.fail_test(f"bad severity did not 400: {s}")
                return self.result

            # Missing event_type → 400
            r, s = _api(SEC + "/audit", "POST", {})
            if s != 400:
                self.fail_test(f"missing event_type did not 400: {s}")
                return self.result

            # Valid
            event_type = "TC-SEC-006-synth"
            r, s = _api(SEC + "/audit", "POST", {
                "event_type": event_type, "severity": "WARNING",
                "source_ip": "198.51.100.7", "imsi": baseline.imsi("embb-bulk", 5),
                "detail": "TC-SEC-006 synthetic",
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"raise failed: {s} {r}")
                return self.result

            ev, _ = _api(SEC + "/audit?limit=20")
            events = ev.get("events", [])
            if not any(e.get("event_type") == event_type for e in events):
                self.fail_test(f"event not in audit log",
                                etypes=[e.get("event_type") for e in events[:5]])
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SecPoliciesAndReset(TestCase):
    """TC-SEC-007: Default policies + rate-limit reset."""
    SPEC = TestSpec(
        tc_id="TC-SEC-007",
        title="Security default policies + rate-limit reset",
        spec="TS 33.501 §9.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("smoke", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the default security-policy catalog plus the\n"
            "  rate-limiter administrative reset per TS 33.501 §9.1.\n"
            "  The seed deployment ships three named policies\n"
            "  (ngap_signalling, nas_auth, gtpu_traffic); any of them\n"
            "  going missing is a deploy regression. The reset endpoint\n"
            "  is the OAM lever for clearing the rate-limiter token-buckets.\n"
            "\n"
            "Procedure (TS 33.501 §9.1)\n"
            "  1. GET /api/security/policies → expect 200, ok=true.\n"
            "  2. Assert policies[].name contains ngap_signalling,\n"
            "     nas_auth, gtpu_traffic.\n"
            "  3. POST /rate-limit/reset → expect 200, ok=true.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — policy names are spec-defined defaults).\n"
            "\n"
            "Pass criteria\n"
            "  policies list contains all three required names AND the\n"
            "  rate-limit reset returns 200/ok.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs; fail_test reports got\n"
            "  policy names on a missing-policy failure).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs (so default\n"
            "  policies are reloaded from the seed).\n"
            "  Reset does not assert any specific counter value, only OK.\n"
            "  Policy set is loaded at sacore start; live add is roadmap."
        ),
    )

    def run(self):
        try:
            r, s = _api(SEC + "/policies")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"policies failed: {s} {r}")
                return self.result
            pols = r.get("policies", [])
            names = {p.get("name") for p in pols}
            for required in ("ngap_signalling", "nas_auth", "gtpu_traffic"):
                if required not in names:
                    self.fail_test(f"policy {required} missing",
                                   got=list(names))
                    return self.result

            rr, rs = _api(SEC + "/rate-limit/reset", "POST")
            if rs != 200 or not rr.get("ok"):
                self.fail_test(f"reset failed: {rs} {rr}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SecFirewallEvalPriority(TestCase):
    """TC-SEC-008: /firewall/eval walks rules in priority order; first match wins."""
    SPEC = TestSpec(
        tc_id="TC-SEC-008",
        title="Security firewall eval walks rules in priority order",
        spec="TS 33.501 §5.9.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the firewall evaluator's first-match-by-priority\n"
            "  semantics per TS 33.501 §5.9.1. Operators rely on rule\n"
            "  ordering by integer priority (lowest first) and on a clean\n"
            "  fall-through to the built-in allow when no rule matches.\n"
            "  Hit counters must also bump for matched rules.\n"
            "\n"
            "Procedure (TS 33.501 §5.9.1)\n"
            "  1. Best-effort DELETE tc008-deny-bad / -allow-trusted /\n"
            "     -default-deny so prior runs do not bias.\n"
            "  2. POST three rules on protocol=ngap: priority 10 deny\n"
            "     10.99.99.99/32, priority 20 allow 10.99.0.0/24, priority\n"
            "     30 default deny.\n"
            "  3. POST /firewall/eval {ngap, 10.99.99.99} →\n"
            "     action=deny, rule=tc008-deny-bad.\n"
            "  4. POST /firewall/eval {ngap, 10.99.0.42} →\n"
            "     action=allow, rule=tc008-allow-trusted.\n"
            "  5. POST /firewall/eval {ngap, 192.168.1.1} →\n"
            "     action=deny, rule=tc008-default-deny.\n"
            "  6. POST /firewall/eval {sbi, 192.168.1.1} → built-in allow,\n"
            "     rule=''.\n"
            "  7. GET /firewall/hits and confirm tc008-deny-bad.count >= 1.\n"
            "  8. finally: DELETE all three rules.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — rule set is hard-coded for determinism).\n"
            "\n"
            "Pass criteria\n"
            "  All four /firewall/eval probes return the expected (action,\n"
            "  rule) pair AND the hit counter for the matched rule bumps.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  CRUD failures during setup raise via API errors, not asserts."
        ),
    )

    def run(self):
        try:
            # Wipe rules from prior runs and stand up a deterministic set.
            for n in ("tc008-deny-bad", "tc008-allow-trusted", "tc008-default-deny"):
                _api(f"{SEC}/firewall/rules/{n}", "DELETE")
            # priority 10 (most specific): deny one /32 on ngap
            _api(f"{SEC}/firewall/rules", "POST", {
                "name": "tc008-deny-bad", "protocol": "ngap", "action": "deny",
                "src_cidr": "10.99.99.99/32", "enabled": True, "priority": 10,
            })
            # priority 20: allow a trusted /24
            _api(f"{SEC}/firewall/rules", "POST", {
                "name": "tc008-allow-trusted", "protocol": "ngap", "action": "allow",
                "src_cidr": "10.99.0.0/24", "enabled": True, "priority": 20,
            })
            # priority 30: default deny on ngap
            _api(f"{SEC}/firewall/rules", "POST", {
                "name": "tc008-default-deny", "protocol": "ngap", "action": "deny",
                "enabled": True, "priority": 30,
            })

            # Bad IP → first rule wins → deny
            r, s = _api(f"{SEC}/firewall/eval", "POST", {
                "protocol": "ngap", "src_ip": "10.99.99.99",
            })
            if s != 200 or r.get("action") != "deny" or r.get("rule") != "tc008-deny-bad":
                self.fail_test(f"specific deny failed: {s} {r}")
                return self.result
            # Trusted /24 → second rule wins → allow
            r2, _ = _api(f"{SEC}/firewall/eval", "POST", {
                "protocol": "ngap", "src_ip": "10.99.0.42",
            })
            if r2.get("action") != "allow" or r2.get("rule") != "tc008-allow-trusted":
                self.fail_test(f"trusted allow failed: {r2}")
                return self.result
            # Anything else on ngap → default deny
            r3, _ = _api(f"{SEC}/firewall/eval", "POST", {
                "protocol": "ngap", "src_ip": "192.168.1.1",
            })
            if r3.get("action") != "deny" or r3.get("rule") != "tc008-default-deny":
                self.fail_test(f"default deny did not catch: {r3}")
                return self.result
            # Wrong protocol → no rule applies → built-in default allow
            r4, _ = _api(f"{SEC}/firewall/eval", "POST", {
                "protocol": "sbi", "src_ip": "192.168.1.1",
            })
            if r4.get("action") != "allow" or r4.get("rule") != "":
                self.fail_test(f"protocol mismatch: {r4}")
                return self.result

            # Hit counters bumped for matched rules
            hits, _ = _api(f"{SEC}/firewall/hits")
            h = hits.get("hits", {})
            if h.get("tc008-deny-bad", {}).get("count", 0) < 1:
                self.fail_test(f"hit counter not bumped: {h}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for n in ("tc008-deny-bad", "tc008-allow-trusted", "tc008-default-deny"):
                _api(f"{SEC}/firewall/rules/{n}", "DELETE")
        return self.result


class SecFirewallInputValidation(TestCase):
    """TC-SEC-009: bad CIDR / port_range / protocol / action → 400."""
    SPEC = TestSpec(
        tc_id="TC-SEC-009",
        title="Security firewall rule input validation rejects bad fields",
        spec="TS 33.501 §5.9.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative-path guard for firewall-rule input validation per\n"
            "  TS 33.501 §5.9.1. Bad src_cidr, inverted port_range,\n"
            "  unsupported protocol and unsupported action must all be\n"
            "  rejected at the API boundary — never silently swallowed —\n"
            "  so misconfigured operators cannot land malformed rules.\n"
            "\n"
            "Procedure (TS 33.501 §5.9.1)\n"
            "  1. POST /firewall/rules {name='tc009-bad', protocol='ngap',\n"
            "     action='deny', src_cidr='not-a-cidr'} → expect 400.\n"
            "  2. POST {name, protocol='any', action='deny',\n"
            "     port_range='9000-100'} (lo>hi) → expect 400.\n"
            "  3. POST {name, protocol='ftp', action='deny'} (protocol\n"
            "     not in whitelist) → expect 400.\n"
            "  4. POST {name, protocol='any', action='log'} (action\n"
            "     not in {allow, deny}) → expect 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — all four bad payloads are hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Every one of the four POSTs returns HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Pure validation probe; nothing should land server-side and\n"
            "  no cleanup is required."
        ),
    )

    def run(self):
        try:
            # Bad src_cidr
            _, s = _api(f"{SEC}/firewall/rules", "POST", {
                "name": "tc009-bad", "protocol": "ngap", "action": "deny",
                "src_cidr": "not-a-cidr",
            })
            if s != 400:
                self.fail_test(f"bad src_cidr did not 400: {s}")
                return self.result
            # Bad port_range (lo > hi)
            _, s2 = _api(f"{SEC}/firewall/rules", "POST", {
                "name": "tc009-bad", "protocol": "any", "action": "deny",
                "port_range": "9000-100",
            })
            if s2 != 400:
                self.fail_test(f"bad port_range did not 400: {s2}")
                return self.result
            # Bad protocol
            _, s3 = _api(f"{SEC}/firewall/rules", "POST", {
                "name": "tc009-bad", "protocol": "ftp", "action": "deny",
            })
            if s3 != 400:
                self.fail_test(f"bad protocol did not 400: {s3}")
                return self.result
            # Bad action
            _, s4 = _api(f"{SEC}/firewall/rules", "POST", {
                "name": "tc009-bad", "protocol": "any", "action": "log",
            })
            if s4 != 400:
                self.fail_test(f"bad action did not 400: {s4}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SecIDSThresholdSuppressThenAlert(TestCase):
    """TC-SEC-010: signature trips only at/above threshold within window."""
    SPEC = TestSpec(
        tc_id="TC-SEC-010",
        title="Security IDS signature suppresses below threshold then alerts",
        spec="TS 33.501 §5.9.4",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the IDS rate-threshold semantics per TS 33.501 §5.9.4:\n"
            "  a signature with threshold=N within window=W must suppress\n"
            "  the first N-1 hits and only promote on hit N. Counters must\n"
            "  show raw=N and alerts=1.\n"
            "\n"
            "Procedure (TS 33.501 §5.9.4)\n"
            "  1. Best-effort DELETE tc010-burst and POST /hits/reset to\n"
            "     start from a clean slate.\n"
            "  2. POST /ids/signatures {tc010-burst, pattern='tc010-burst-\n"
            "     payload', threshold=3, window_s=60, enabled=true,\n"
            "     severity=WARNING}.\n"
            "  3. POST /ids/test twice for src=10.250.0.10 — both must\n"
            "     return detected=False.\n"
            "  4. POST /ids/test a third time — must return detected=True.\n"
            "  5. GET /ids/hits and assert hits.tc010-burst.count == 3 and\n"
            "     alerts.tc010-burst.count == 1.\n"
            "  6. finally: DELETE the signature.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — signature name, threshold, window, src IP are\n"
            "  hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Hits 1+2 do not promote (detected=False), hit 3 promotes\n"
            "  (detected=True) AND raw counter is 3 AND alerts counter is 1.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs; fail_test reports\n"
            "  hit/alert counters on a counter-shape failure).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Wall-clock independent; uses POST /hits/reset for repeatability."
        ),
    )

    def run(self):
        try:
            sig_name = "tc010-burst"
            _api(f"{SEC}/ids/signatures/{sig_name}", "DELETE")
            _api(f"{SEC}/hits/reset", "POST")
            # Threshold 3 within 60s — first 2 hits suppressed; 3rd promotes.
            r, s = _api(f"{SEC}/ids/signatures", "POST", {
                "name": sig_name, "pattern": "tc010-burst-payload",
                "threshold": 3, "window_s": 60, "enabled": True,
                "severity": "WARNING",
            })
            if s != 200:
                self.fail_test(f"create signature failed: {s} {r}")
                return self.result

            src = "10.250.0.10"
            # Hits 1 + 2: detected=False
            for i in range(2):
                rd, _ = _api(f"{SEC}/ids/test", "POST", {
                    "event_type": sig_name, "source_ip": src,
                    "detail": "tc010-burst-payload",
                })
                if rd.get("detected") is True:
                    self.fail_test(f"promoted before threshold (hit {i+1}): {rd}")
                    return self.result
            # Hit 3: detected=True
            rd3, _ = _api(f"{SEC}/ids/test", "POST", {
                "event_type": sig_name, "source_ip": src,
                "detail": "tc010-burst-payload",
            })
            if rd3.get("detected") is not True:
                self.fail_test(f"no alert at threshold: {rd3}")
                return self.result

            # Hit counters: raw=3, alerts=1
            hits, _ = _api(f"{SEC}/ids/hits")
            raw = hits.get("hits", {}).get(sig_name, {}).get("count", 0)
            alerts = hits.get("alerts", {}).get(sig_name, {}).get("count", 0)
            if raw != 3 or alerts != 1:
                self.fail_test(f"counter shape: raw={raw} alerts={alerts}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _api(f"{SEC}/ids/signatures/tc010-burst", "DELETE")
        return self.result


class SecIDSAutoBlockOnTrip(TestCase):
    """TC-SEC-011: signature with auto_block_ttl_s adds source IP to deny list."""
    SPEC = TestSpec(
        tc_id="TC-SEC-011",
        title="Security IDS auto-block adds source IP on signature trip",
        spec="TS 33.501 §5.9.4",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the auto-block side-effect of IDS signature promotion\n"
            "  per TS 33.501 §5.9.4. A signature with auto_block_ttl_s set\n"
            "  MUST push the source IP onto the blocked-IP deny list with\n"
            "  expires_at populated when the signature trips.\n"
            "\n"
            "Procedure (TS 33.501 §5.9.4)\n"
            "  1. Best-effort DELETE tc011-autoblock and 10.250.0.11; POST\n"
            "     /hits/reset.\n"
            "  2. POST /ids/signatures {tc011-autoblock, pattern='tc011-\n"
            "     payload', threshold=1, window_s=60, auto_block_ttl_s=300,\n"
            "     enabled=true, severity=CRITICAL}.\n"
            "  3. POST /ids/test {event_type=tc011-autoblock,\n"
            "     source_ip=10.250.0.11, detail='tc011-payload'} → must\n"
            "     return detected=True.\n"
            "  4. GET /blocked-ips and find the row for 10.250.0.11; assert\n"
            "     expires_at is populated.\n"
            "  5. finally: DELETE signature and unblock the source IP.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — signature, source IP, TTL hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Single hit promotes (detected=True), source IP appears in\n"
            "  /blocked-ips, AND expires_at is non-empty.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  auto_block_ttl_s=300 chosen well above TTL-sweep cadence."
        ),
    )

    def run(self):
        try:
            sig_name = "tc011-autoblock"
            src = "10.250.0.11"
            _api(f"{SEC}/ids/signatures/{sig_name}", "DELETE")
            _api(f"{SEC}/blocked-ips/" + src, "DELETE")
            _api(f"{SEC}/hits/reset", "POST")
            # threshold=1 trips on first hit; auto_block_ttl_s=300 → 5 min.
            _api(f"{SEC}/ids/signatures", "POST", {
                "name": sig_name, "pattern": "tc011-payload",
                "threshold": 1, "window_s": 60,
                "auto_block_ttl_s": 300, "enabled": True,
                "severity": "CRITICAL",
            })
            rd, _ = _api(f"{SEC}/ids/test", "POST", {
                "event_type": sig_name, "source_ip": src,
                "detail": "tc011-payload",
            })
            if rd.get("detected") is not True:
                self.fail_test(f"signature did not promote: {rd}")
                return self.result

            # Source IP should now be on the blocked-ips list with expires_at set.
            bl, _ = _api(f"{SEC}/blocked-ips")
            row = next((b for b in bl.get("blocked", []) if b.get("ip") == src), None)
            if not row:
                self.fail_test(f"source IP not auto-blocked: {bl}")
                return self.result
            if not row.get("expires_at"):
                self.fail_test(f"expires_at not set: {row}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _api(f"{SEC}/ids/signatures/tc011-autoblock", "DELETE")
            _api(f"{SEC}/blocked-ips/10.250.0.11", "DELETE")
        return self.result


class SecBlockedIPTTLExpiry(TestCase):
    """TC-SEC-012: TTL block expires; IsBlocked returns false after expiry."""
    SPEC = TestSpec(
        tc_id="TC-SEC-012",
        title="Security TTL-bounded blocked-IP expires and is swept",
        spec="TS 33.501 §5.9.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins TTL-bounded blocked-IP expiry semantics per TS 33.501\n"
            "  §5.9.1. Operators add a row with /blocked-ips/ttl; once the\n"
            "  TTL elapses, ListBlockedIPs must sweep the row so the deny\n"
            "  list does not accumulate stale entries.\n"
            "\n"
            "Procedure (TS 33.501 §5.9.1)\n"
            "  1. Best-effort DELETE the IP first to make the test\n"
            "     re-runnable.\n"
            "  2. POST /blocked-ips/ttl {ip='10.250.0.12', reason='tc012',\n"
            "     ttl_s=1} → expect 200.\n"
            "  3. GET /blocked-ips and find the row; assert expires_at is\n"
            "     non-empty.\n"
            "  4. time.sleep(2.0) so we are past TTL.\n"
            "  5. GET /blocked-ips again and assert the row is gone.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — ttl_s=1 hard-coded; total wait 2s).\n"
            "\n"
            "Pass criteria\n"
            "  Row is present with expires_at set immediately after POST\n"
            "  AND absent from the list after sleeping past the TTL.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Adds ~2s wall-clock; sweep is lazy on the read path.\n"
            "  TTL granularity is 1s (smallest practical for the test)."
        ),
    )

    def run(self):
        try:
            import time
            ip = "10.250.0.12"
            _api(f"{SEC}/blocked-ips/" + ip, "DELETE")
            r, s = _api(f"{SEC}/blocked-ips/ttl", "POST", {
                "ip": ip, "reason": "tc012", "ttl_s": 1,
            })
            if s != 200:
                self.fail_test(f"ttl block failed: {s} {r}")
                return self.result
            bl1, _ = _api(f"{SEC}/blocked-ips")
            row = next((b for b in bl1.get("blocked", []) if b.get("ip") == ip), None)
            if not row or not row.get("expires_at"):
                self.fail_test(f"row missing or expires_at empty: {row}")
                return self.result
            # Wait past TTL; ListBlockedIPs sweeps expired rows.
            time.sleep(2)
            bl2, _ = _api(f"{SEC}/blocked-ips")
            still = next((b for b in bl2.get("blocked", []) if b.get("ip") == ip), None)
            if still is not None:
                self.fail_test(f"row still present after TTL: {still}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SecIDSRegexPattern(TestCase):
    """TC-SEC-013: pattern in /…/ form is treated as a regex against detail."""
    SPEC = TestSpec(
        tc_id="TC-SEC-013",
        title="Security IDS slash-delimited pattern is interpreted as regex",
        spec="TS 33.501 §5.9.4",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the IDS regex pattern convention per TS 33.501 §5.9.4:\n"
            "  patterns wrapped in slashes (/.../ form) are interpreted as\n"
            "  Go regexp against the event detail; bare strings remain plain\n"
            "  substring matches. This guards both branches of that\n"
            "  decision tree.\n"
            "\n"
            "Procedure (TS 33.501 §5.9.4)\n"
            "  1. Best-effort DELETE tc013-regex; POST /hits/reset.\n"
            "  2. POST /ids/signatures {tc013-regex,\n"
            "     pattern='/SQL.*injection/', threshold=1, window_s=60,\n"
            "     enabled=true, severity=CRITICAL}.\n"
            "  3. POST /ids/test with detail='harmless payload' → must\n"
            "     return detected=False (regex must NOT match).\n"
            "  4. POST /ids/test with detail='SQL fragment injection\n"
            "     attempt' → must return detected=True.\n"
            "  5. finally: DELETE the signature.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — pattern is hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Harmless payload returns detected=False AND the matching\n"
            "  payload returns detected=True.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Slash-delimited form is the wire convention; bare strings\n"
            "  remain substring matches and are not exercised here."
        ),
    )

    def run(self):
        try:
            sig_name = "tc013-regex"
            _api(f"{SEC}/ids/signatures/{sig_name}", "DELETE")
            _api(f"{SEC}/hits/reset", "POST")
            _api(f"{SEC}/ids/signatures", "POST", {
                "name": sig_name,
                "pattern": "/SQL.*injection/",
                "threshold": 1, "window_s": 60, "enabled": True,
                "severity": "CRITICAL",
            })
            # Should NOT match — no regex
            rd, _ = _api(f"{SEC}/ids/test", "POST", {
                "event_type": "OTHER_EVENT", "source_ip": "10.250.0.13",
                "detail": "harmless payload",
            })
            if rd.get("detected") is True:
                self.fail_test(f"regex falsely matched: {rd}")
                return self.result
            # SHOULD match
            rd2, _ = _api(f"{SEC}/ids/test", "POST", {
                "event_type": "OTHER_EVENT", "source_ip": "10.250.0.13",
                "detail": "SQL fragment injection attempt",
            })
            if rd2.get("detected") is not True:
                self.fail_test(f"regex did not match: {rd2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _api(f"{SEC}/ids/signatures/tc013-regex", "DELETE")
        return self.result


ALL_CORE_SECURITY_TCS = [
    SecStatusShape,
    SecFirewallCRUD,
    SecIDSCRUD,
    SecBlockedIPs,
    SecKnownGnBs,
    SecAuditEventAndQuery,
    SecPoliciesAndReset,
    SecFirewallEvalPriority,
    SecFirewallInputValidation,
    SecIDSThresholdSuppressThenAlert,
    SecIDSAutoBlockOnTrip,
    SecBlockedIPTTLExpiry,
    SecIDSRegexPattern,
]

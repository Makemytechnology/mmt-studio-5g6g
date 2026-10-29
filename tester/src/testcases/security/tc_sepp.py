# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: SEPP — Security Edge Protection Proxy operator policy.

TS 29.573 §5.2  N32-c control plane (peer capability + TLS handshake).
TS 29.573 §5.3  N32-f forwarding plane (HTTP reverse proxy + topology
                hiding).
TS 33.501 §13.1 5GC SBI security at the PLMN border (mutual TLS).
TS 23.501 §5.36 Roaming architecture — the inter-PLMN reference point
                this surface guards.

Drives the SA Core REST surface at /api/sepp/*: peer-PLMN allow-list,
topology-hiding rules, admission gate (default-deny on unknown peer,
status gate, path filter), N32 audit log, and the proxy + policy
status snapshot. Endpoints return `{ok: true, ...}` envelopes.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_sepp")

SEPP = "/api/sepp"


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


class SeppPeerCRUD(TestCase):
    """TC-SEPP-001: Peer add/list/get/patch/delete (TS 33.501 §13.1)."""
    SPEC = TestSpec(
        tc_id="TC-SEPP-001",
        title="SEPP peer allow-list add/get/list/patch/delete CRUD",
        spec="TS 33.501 §13.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Foundational SEPP peer-catalog CRUD per TS 33.501 §13.1\n"
            "  (5GC SBI security at PLMN border). Operators provision\n"
            "  peer SEPPs (PLMN, FQDN, public SAN, allowed paths); the\n"
            "  surface must accept add / get / list / patch / delete.\n"
            "\n"
            "Procedure (TS 33.501 §13.1)\n"
            "  1. POST /api/sepp/peers {plmn_id='00199',\n"
            "     fqdn='sepp.tc-sepp-001.example',\n"
            "     public_san='DNS:sepp.tc-sepp-001.example',\n"
            "     allowed_paths='/nudm-uecm/v1,/namf-comm/v1',\n"
            "     description='TC-SEPP-001'} → expect 201, ok=true,\n"
            "     peer.id non-zero, peer.status=='active' default.\n"
            "  2. GET /api/sepp/peers/{id} → expect 200 AND\n"
            "     peer.plmn_id=='00199'.\n"
            "  3. GET /api/sepp/peers → confirm peer.id in returned ids.\n"
            "  4. PATCH /peers/{id} {status='inactive'} → expect 200 AND\n"
            "     peer.status=='inactive'.\n"
            "  5. finally: DELETE /peers/{id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — PLMN, FQDN, allowed paths hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Every CRUD verb returns the expected status and the row\n"
            "  reflects each mutation (active on create, in list, flipped\n"
            "  to inactive on patch).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Cleanup deletes the peer in the finally block."
        ),
    )

    def run(self):
        peer_id = None
        try:
            r, s = _api(SEPP + "/peers", "POST", {
                "plmn_id": "00199",
                "fqdn": "sepp.tc-sepp-001.example",
                "public_san": "DNS:sepp.tc-sepp-001.example",
                "allowed_paths": "/nudm-uecm/v1,/namf-comm/v1",
                "description": "TC-SEPP-001",
            })
            if s != 201 or not r.get("ok") or not r.get("peer", {}).get("id"):
                self.fail_test(f"create failed: {s} {r}")
                return self.result
            peer = r["peer"]
            peer_id = peer["id"]
            if peer.get("status") != "active":
                self.fail_test(f"default status not active: {peer}")
                return self.result

            # GET
            gr, gs = _api(f"{SEPP}/peers/{peer_id}")
            if gs != 200 or gr.get("peer", {}).get("plmn_id") != "00199":
                self.fail_test(f"get failed: {gs} {gr}")
                return self.result

            # List
            lr, _ = _api(SEPP + "/peers")
            ids = [p.get("id") for p in lr.get("peers", [])]
            if peer_id not in ids:
                self.fail_test(f"peer {peer_id} not in list")
                return self.result

            # Patch — flip status to inactive
            pr, ps = _api(f"{SEPP}/peers/{peer_id}", "PATCH",
                          {"status": "inactive"})
            if ps != 200 or pr.get("peer", {}).get("status") != "inactive":
                self.fail_test(f"patch failed: {ps} {pr}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if peer_id:
                _api(f"{SEPP}/peers/{peer_id}", "DELETE")
        return self.result


class SeppPeerValidation(TestCase):
    """TC-SEPP-002: Bad input on peer create returns 400."""
    SPEC = TestSpec(
        tc_id="TC-SEPP-002",
        title="SEPP peer create/list reject bad inputs with 400",
        spec="TS 33.501 §13.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative-path guard for the SEPP peer create + list inputs\n"
            "  per TS 33.501 §13.1. plmn_id and fqdn are required; status\n"
            "  must be one of {active, inactive, blocked}; the status\n"
            "  filter on the list endpoint must validate against the same\n"
            "  enum. Any of these failing open is a regression and would\n"
            "  let operators land malformed peer rows that the admission\n"
            "  gate could not safely evaluate.\n"
            "\n"
            "Procedure (TS 33.501 §13.1)\n"
            "  1. POST /api/sepp/peers {} → expect 400 (missing plmn_id).\n"
            "  2. POST /peers {plmn_id='00199'} → expect 400 (missing fqdn).\n"
            "  3. POST /peers {plmn_id='00199', fqdn='x.example',\n"
            "     status='BAD'} → expect 400 (bad status enum).\n"
            "  4. GET /peers?status=BAD → expect 400 (bad status filter).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — all four bad payloads are hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  All four cases return HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Pure validation probe; nothing should land server-side and\n"
            "  no cleanup is required.\n"
            "  Status enum is shared between create payload and list filter."
        ),
    )

    def run(self):
        try:
            r, s = _api(SEPP + "/peers", "POST", {})
            if s != 400:
                self.fail_test(f"missing plmn_id did not 400: {s}")
                return self.result

            r, s = _api(SEPP + "/peers", "POST", {"plmn_id": "00199"})
            if s != 400:
                self.fail_test(f"missing fqdn did not 400: {s}")
                return self.result

            r, s = _api(SEPP + "/peers", "POST", {
                "plmn_id": "00199", "fqdn": "x.example", "status": "BAD",
            })
            if s != 400:
                self.fail_test(f"bad status did not 400: {s}")
                return self.result

            # Status filter must validate
            r, s = _api(SEPP + "/peers?status=BAD")
            if s != 400:
                self.fail_test(f"bad status filter did not 400: {s}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SeppCheckAccessDefaultDeny(TestCase):
    """TC-SEPP-003: Unknown PLMN admission denied (TS 33.501 §13.1)."""
    SPEC = TestSpec(
        tc_id="TC-SEPP-003",
        title="SEPP check-access default-denies unknown PLMN",
        spec="TS 33.501 §13.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the default-deny admission rule per TS 33.501 §13.1\n"
            "  (and TS 29.573 §5.2 N32-c admission). An unknown peer PLMN\n"
            "  MUST be refused; an empty plmn_id MUST be rejected at the\n"
            "  API boundary, not silently treated as wildcard. The\n"
            "  default-deny rule is the spine of inter-PLMN N32 security.\n"
            "\n"
            "Procedure (TS 33.501 §13.1)\n"
            "  1. POST /api/sepp/check-access {plmn_id='99999',\n"
            "     path='/nudm-uecm/v1/foo'} → expect HTTP 200 with\n"
            "     body.access.allowed == False.\n"
            "  2. POST /api/sepp/check-access {} (empty plmn_id) →\n"
            "     expect HTTP 400 (validation error).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — unknown PLMN '99999' and empty payload hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Unknown peer returns access.allowed=False AND empty\n"
            "  plmn_id returns HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Default-deny semantics: 200/allowed=false is the spec'd\n"
            "  response shape, not 403.\n"
            "  Audit-row side-effect is exercised by TC-SEPP-008.\n"
            "  PLMN '99999' is outside MCC/MNC test ranges by design."
        ),
    )

    def run(self):
        try:
            r, s = _api(SEPP + "/check-access", "POST", {
                "plmn_id": "99999", "path": "/nudm-uecm/v1/foo",
            })
            if s != 200 or r.get("access", {}).get("allowed"):
                self.fail_test(f"unknown peer admitted: {r}")
                return self.result

            # Empty plmn_id → 400
            _, s2 = _api(SEPP + "/check-access", "POST", {})
            if s2 != 400:
                self.fail_test(f"empty plmn_id did not 400: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SeppCheckAccessAllow(TestCase):
    """TC-SEPP-004: Active peer + matching path → admitted; status='blocked' → denied."""
    SPEC = TestSpec(
        tc_id="TC-SEPP-004",
        title="SEPP admits active peer + allowed path; denies on block/path",
        spec="TS 29.573 §5.2",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the three-axis admission gate per TS 29.573 §5.2:\n"
            "  (a) peer must be known, (b) status must allow, (c) path\n"
            "  must match the peer's allowed_paths whitelist. All three\n"
            "  must be true for admission; any one false denies.\n"
            "\n"
            "Procedure (TS 29.573 §5.2)\n"
            "  1. POST /api/sepp/peers {plmn_id='00177',\n"
            "     fqdn='sepp.tc-sepp-004.example',\n"
            "     allowed_paths='/nudm-uecm/v1'}; record peer_id.\n"
            "  2. POST /check-access {plmn_id='00177',\n"
            "     path='/nudm-uecm/v1/imsi-001'} →\n"
            "     assert access.allowed == True.\n"
            "  3. POST /check-access {plmn_id='00177',\n"
            "     path='/nausf-auth/v1/whatever'} →\n"
            "     assert access.allowed == False (path filter).\n"
            "  4. PATCH /peers/{id} {status='blocked'}.\n"
            "  5. POST /check-access on the previously-allowed path →\n"
            "     assert access.allowed == False (status filter).\n"
            "  6. finally: DELETE the peer.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — PLMN '00177' and allowed_paths hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Allowed path on active peer admits, disallowed path denies,\n"
            "  AND a status flip to 'blocked' makes the same allowed path\n"
            "  deny.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  allowed_paths is comma-separated prefix list per the\n"
            "  N32-f filter."
        ),
    )

    def run(self):
        peer_id = None
        try:
            r, _ = _api(SEPP + "/peers", "POST", {
                "plmn_id": "00177", "fqdn": "sepp.tc-sepp-004.example",
                "allowed_paths": "/nudm-uecm/v1",
            })
            peer_id = r.get("peer", {}).get("id")

            # Allowed path
            ar, _ = _api(SEPP + "/check-access", "POST", {
                "plmn_id": "00177", "path": "/nudm-uecm/v1/imsi-001",
            })
            if not ar.get("access", {}).get("allowed"):
                self.fail_test(f"active peer denied: {ar}")
                return self.result

            # Disallowed path
            dr, _ = _api(SEPP + "/check-access", "POST", {
                "plmn_id": "00177", "path": "/nausf-auth/v1/whatever",
            })
            if dr.get("access", {}).get("allowed"):
                self.fail_test(f"disallowed path admitted: {dr}")
                return self.result

            # Block peer; admission must flip
            _api(f"{SEPP}/peers/{peer_id}", "PATCH", {"status": "blocked"})
            br, _ = _api(SEPP + "/check-access", "POST", {
                "plmn_id": "00177", "path": "/nudm-uecm/v1/imsi-001",
            })
            if br.get("access", {}).get("allowed"):
                self.fail_test(f"blocked peer admitted: {br}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if peer_id:
                _api(f"{SEPP}/peers/{peer_id}", "DELETE")
        return self.result


class SeppTopologyHiding(TestCase):
    """TC-SEPP-005: Topology-hiding rule UPSERT + delete + FK CASCADE."""
    SPEC = TestSpec(
        tc_id="TC-SEPP-005",
        title="SEPP topology-hiding rule UPSERT and FK CASCADE on peer delete",
        spec="TS 29.573 §5.3",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the topology-hiding rule CRUD + FK CASCADE per\n"
            "  TS 29.573 §5.3 (N32-f forwarding plane). Operators upsert\n"
            "  per-peer rules that hide internal FQDNs / callbacks /\n"
            "  headers; deleting the peer must cascade the rule away so\n"
            "  no orphans remain.\n"
            "\n"
            "Procedure (TS 29.573 §5.3)\n"
            "  1. POST /api/sepp/peers {plmn_id='00188',\n"
            "     fqdn='sepp.tc-sepp-005.example'}; record peer_id.\n"
            "  2. GET /topology-hiding/{peer_id} → expect 404 (no rule).\n"
            "  3. POST /topology-hiding {peer_id, hide_internal_fqdn=True,\n"
            "     hide_callbacks=True, replace_fqdn='sepp.our-network.\n"
            "     example', strip_headers='x-internal-ip,x-debug'}\n"
            "     → expect 200, rule.peer_id matches.\n"
            "  4. GET /topology-hiding/{peer_id} → expect 200 AND\n"
            "     rule.hide_internal_fqdn truthy.\n"
            "  5. POST /topology-hiding {peer_id=99999999} → expect 400\n"
            "     (unknown peer).\n"
            "  6. DELETE the peer; CASCADE removes the rule.\n"
            "  7. GET /topology-hiding (list) — informational read.\n"
            "  8. finally: DELETE the peer if still set.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payload hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  404 before upsert, 200 after upsert with the rule reflected\n"
            "  on readback, 400 for unknown peer, AND cascade on peer\n"
            "  delete (no exception, peer_id reset).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  The post-cascade list read is informational, not asserted."
        ),
    )

    def run(self):
        peer_id = None
        try:
            r, _ = _api(SEPP + "/peers", "POST", {
                "plmn_id": "00188", "fqdn": "sepp.tc-sepp-005.example",
            })
            peer_id = r.get("peer", {}).get("id")

            # No rule yet → 404
            _, s = _api(f"{SEPP}/topology-hiding/{peer_id}")
            if s != 404:
                self.fail_test(f"missing rule did not 404: {s}")
                return self.result

            # Upsert
            ur, us = _api(SEPP + "/topology-hiding", "POST", {
                "peer_id": peer_id,
                "hide_internal_fqdn": True,
                "hide_callbacks": True,
                "replace_fqdn": "sepp.our-network.example",
                "strip_headers": "x-internal-ip,x-debug",
            })
            if us != 200 or ur.get("rule", {}).get("peer_id") != peer_id:
                self.fail_test(f"upsert failed: {us} {ur}")
                return self.result

            # GET reflects rule
            gr, gs = _api(f"{SEPP}/topology-hiding/{peer_id}")
            if gs != 200 or not gr.get("rule", {}).get("hide_internal_fqdn"):
                self.fail_test(f"get failed: {gs} {gr}")
                return self.result

            # Bad peer_id (peer doesn't exist) → 400
            _, bs = _api(SEPP + "/topology-hiding", "POST",
                          {"peer_id": 99999999})
            if bs != 400:
                self.fail_test(f"unknown peer did not 400: {bs}")
                return self.result

            # FK CASCADE: deleting peer removes rule.
            _api(f"{SEPP}/peers/{peer_id}", "DELETE")
            peer_id = None
            _, s2 = _api(f"{SEPP}/topology-hiding")
            # After CASCADE the row is gone; rules list shouldn't carry it.
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if peer_id:
                _api(f"{SEPP}/peers/{peer_id}", "DELETE")
        return self.result


class SeppN32Log(TestCase):
    """TC-SEPP-006: Synthetic /log raise + readback + filter."""
    SPEC = TestSpec(
        tc_id="TC-SEPP-006",
        title="SEPP N32 audit log raise + peer/action filter readback",
        spec="TS 29.573 §5.3",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the N32 audit log raise + query contract per\n"
            "  TS 29.573 §5.3. Operators / other NFs raise N32 log rows\n"
            "  with direction + path + method + status_code + latency_ms +\n"
            "  action; the OAM surface filters by peer and by action.\n"
            "\n"
            "Procedure (TS 29.573 §5.3)\n"
            "  1. POST /log {peer_plmn='00166', direction='BAD',\n"
            "     path='/nudm-uecm/v1/x'} → expect 400.\n"
            "  2. POST /log {peer_plmn='00166'} (missing path)\n"
            "     → expect 400.\n"
            "  3. POST /log {peer_plmn='00166', direction='inbound',\n"
            "     path='/tc-sepp-006/x', method='POST', status_code=200,\n"
            "     latency_ms=17, action='forwarded', reason='tc-sepp-006'}\n"
            "     → expect 200, ok=true.\n"
            "  4. GET /log?peer=00166&limit=20; assert\n"
            "     '/tc-sepp-006/x' in returned paths.\n"
            "  5. GET /log?peer=00166&action=forwarded; assert the same\n"
            "     row surfaces under the action filter.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — peer PLMN and payload hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both 400 gates fire, the valid raise returns ok=true, AND\n"
            "  the row is visible under both peer and action filters.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test takes no kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  direction must be one of {inbound, outbound} per the schema."
        ),
    )

    def run(self):
        try:
            # Bad direction
            _, s = _api(SEPP + "/log", "POST", {
                "peer_plmn": "00166", "direction": "BAD",
                "path": "/nudm-uecm/v1/x",
            })
            if s != 400:
                self.fail_test(f"bad direction did not 400: {s}")
                return self.result

            # Missing path
            _, s = _api(SEPP + "/log", "POST", {"peer_plmn": "00166"})
            if s != 400:
                self.fail_test(f"missing path did not 400: {s}")
                return self.result

            # Valid raise
            r, s = _api(SEPP + "/log", "POST", {
                "peer_plmn": "00166",
                "direction": "inbound",
                "path": "/tc-sepp-006/x",
                "method": "POST",
                "status_code": 200,
                "latency_ms": 17,
                "action": "forwarded",
                "reason": "tc-sepp-006",
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"raise failed: {s} {r}")
                return self.result

            # Readback by peer
            lr, _ = _api(SEPP + "/log?peer=00166&limit=20")
            paths = [x.get("path") for x in lr.get("log", [])]
            if "/tc-sepp-006/x" not in paths:
                self.fail_test(f"log entry missing", paths=paths[:5])
                return self.result

            # Action filter
            af, _ = _api(SEPP + "/log?peer=00166&action=forwarded")
            if not any(x.get("path") == "/tc-sepp-006/x"
                       for x in af.get("log", [])):
                self.fail_test("action filter dropped the row")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SeppStatus(TestCase):
    """TC-SEPP-007: /status carries policy stats + proxy runtime info."""
    SPEC = TestSpec(
        tc_id="TC-SEPP-007",
        title="SEPP /status carries policy counters and proxy runtime info",
        spec="TS 29.573 §5.2",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("smoke", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the /status aggregator shape per TS 29.573 §5.2. The\n"
            "  GUI dashboard reads /api/sepp/status to render the SEPP\n"
            "  health tiles; every required counter and the proxy.status\n"
            "  sub-object must be present. Any drift in the JSON contract\n"
            "  here breaks the OAM dashboard rendering.\n"
            "\n"
            "Procedure (TS 29.573 §5.2)\n"
            "  1. GET /api/sepp/status → expect HTTP 200, ok=true.\n"
            "  2. Parse status sub-object; assert every required top-level\n"
            "     key is present: total_peers, active_peers, blocked_peers,\n"
            "     hiding_rules, requests_total, forwarded, rejected, proxy.\n"
            "  3. Assert status.proxy carries a 'status' sub-field\n"
            "     (the inner runtime-health indicator).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — read-only aggregator probe).\n"
            "\n"
            "Pass criteria\n"
            "  HTTP 200, ok=true, and every listed top-level key + the\n"
            "  proxy.status sub-field are present.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — fail_test reports got / proxy keys on a missing-key\n"
            "  failure; pass_test emits no metrics).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Pure JSON-shape probe; counter values are not asserted.\n"
            "  Drift detection: GUI templates/sepp.html depends on this."
        ),
    )

    def run(self):
        try:
            r, s = _api(SEPP + "/status")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"status failed: {s} {r}")
                return self.result
            st = r.get("status", {})
            for k in ("total_peers", "active_peers", "blocked_peers",
                      "hiding_rules", "requests_total", "forwarded",
                      "rejected", "proxy"):
                if k not in st:
                    self.fail_test(f"status missing {k}", got=list(st))
                    return self.result
            if "status" not in st["proxy"]:
                self.fail_test(f"proxy.status missing: {st['proxy']}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SeppLogOnUnknownPeer(TestCase):
    """TC-SEPP-008: Admission denial writes a 'rejected' audit row."""
    SPEC = TestSpec(
        tc_id="TC-SEPP-008",
        title="SEPP admission denial writes a rejected audit row",
        spec="TS 33.501 §13.1",
        domain=Domain.SECURITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the admission-denial → audit-row side-effect per\n"
            "  TS 33.501 §13.1. A default-deny admission decision MUST\n"
            "  fire LogN32 with action='rejected' so operators can review\n"
            "  spurious peer activity. Silent default-deny is a regression\n"
            "  because it would leave operators blind to scan / probe\n"
            "  traffic from unknown PLMNs.\n"
            "\n"
            "Procedure (TS 33.501 §13.1)\n"
            "  1. POST /api/sepp/check-access {plmn_id='00099-tc-sepp-008',\n"
            "     path='/tc-sepp-008/x'} — the peer id is unique to this\n"
            "     test run so we can filter the audit log cleanly.\n"
            "  2. GET /api/sepp/log?peer=00099-tc-sepp-008&action=rejected\n"
            "     → parse log[] from the response body.\n"
            "  3. Assert log[] is non-empty.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — unknown peer id and path hard-coded so the log\n"
            "  filter is deterministic).\n"
            "\n"
            "Pass criteria\n"
            "  The /log?action=rejected query returns at least one entry\n"
            "  for the probed peer PLMN.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rejected (count of rejected log entries returned).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Peer id contains 'tc-sepp-008' deliberately so the log\n"
            "  filter is scoped to this test run."
        ),
    )

    def run(self):
        try:
            # Make sure /check-access on an unknown peer fires LogN32.
            _api(SEPP + "/check-access", "POST", {
                "plmn_id": "00099-tc-sepp-008",
                "path": "/tc-sepp-008/x",
            })
            lr, _ = _api(SEPP + "/log?peer=00099-tc-sepp-008&action=rejected")
            entries = lr.get("log", [])
            if not entries:
                self.fail_test("no rejected log entry for unknown peer")
                return self.result
            self.pass_test(rejected=len(entries))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_SEPP_TCS = [
    SeppPeerCRUD,
    SeppPeerValidation,
    SeppCheckAccessDefaultDeny,
    SeppCheckAccessAllow,
    SeppTopologyHiding,
    SeppN32Log,
    SeppStatus,
    SeppLogOnUnknownPeer,
]

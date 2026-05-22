# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Lawful Intercept (LI) — ADMF + POI behaviour.

Spec anchors (verifiable against locally-loaded specs and the LI
package header):

  TS 33.501 §5.9 NOTE 3 — only LI hook in the local 5G security
                          spec: SUPI is mixed into KAMF derivation
                          precisely so the LI pipeline can correlate
                          per-target events.

  TS 33.127 §5.2 — LI administrative function security (token-gate
                   on /api/li/*).
  TS 33.127 §5.3 — Target provisioning (ADMF surface).
  TS 33.127 §7.4.2 — AMF as IRI-POI for mobility-management events.
  TS 33.127 §7.5   — SMF as IRI-POI / SMF+UPF as CC-POI for
                     session-management events.

  TS 33.128 §6.2.1 — Registration / De-registration to 5GS Event.
  TS 33.128 §6.2.2 — PDU Session Establishment Event.
  TS 33.128 §6.2.3 — PDU Session Release Event.

The X1/X2/X3 stage-3 wire transports remain TODO at the core (see
security/li/li.go header); these tests exercise the operator-facing
OAM surface and the per-NF event capture into li_iri_events /
li_cc_sessions.
"""

import json
import logging
import time
import urllib.request
import urllib.error

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_li")


def _li_api(path, method="GET", body=None, headers=None):
    """Call SA Core LI REST API and return (json|str, status)."""
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    h = {"Content-Type": "application/json"}
    if headers:
        h.update(headers)
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=h, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read().decode()
            try:
                return json.loads(raw), resp.status
            except Exception:
                return raw, resp.status
    except urllib.error.HTTPError as e:
        try:
            err_body = json.loads(e.read().decode())
        except Exception:
            err_body = {"error": str(e)}
        return err_body, e.code
    except Exception as e:
        return {"error": str(e)}, 0


def _cleanup_warrant(wid):
    """Best-effort delete; ignore errors. Tests use deterministic
    warrant_ids and the row must be removed before the next run so
    the UNIQUE(warrant_id) constraint doesn't refuse the re-create.
    Falls back to revoke for older builds that don't expose delete."""
    try:
        _li_api(f"/api/li/warrant/{wid}/delete", "POST")
    except Exception:
        pass
    try:
        _li_api(f"/api/li/warrant/{wid}/revoke", "POST")
    except Exception:
        pass


# ─── X2 / X3 mock-MDF helpers ────────────────────────────────────────


def _tester_ip_for_core():
    """IPv4 that the core can use to reach this tester. The tester
    container runs on 172.30.0.20 in mmtnet (docker-compose.yml).
    Lazy import keeps the helper usable from non-network unit tests."""
    import socket
    try:
        # The hostname normally resolves to the primary mmtnet IP.
        ip = socket.gethostbyname(socket.gethostname())
        if ip and ip != "127.0.0.1":
            return ip
    except Exception:
        pass
    return "172.30.0.20"


def _mock_mdf_url():
    return f"http://{_tester_ip_for_core()}:5001/_mock_mdf"


def _mock_mdf_local(path, method="GET", body=None):
    """Call the mock MDF state/reset endpoints from the tester
    process itself (loopback)."""
    import urllib.request
    url = f"http://127.0.0.1:5001{path}"
    h = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=h, method=method)
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return json.loads(resp.read().decode()), resp.status
    except Exception as e:
        return {"error": str(e)}, 0


def _mock_mdf_reset():
    _mock_mdf_local("/_mock_mdf/reset", "POST")


def _mock_mdf_state():
    body, _ = _mock_mdf_local("/_mock_mdf/state")
    return body or {"x2": [], "x3": [], "fail_remaining": 0}


def _enable_x2_delivery(interval_ms=200):
    """Flip the network_config flags that arm the deliverers."""
    _li_api("/api/network-config", "POST", {
        "li_x2_enabled": 1,
        "li_x3_enabled": 1,
        "li_mdf_poll_interval_ms": interval_ms,
    })


def _disable_x2_delivery():
    _li_api("/api/network-config", "POST", {
        "li_x2_enabled": 0,
        "li_x3_enabled": 0,
    })


def _wait_for_mock_x2(matcher, timeout=8.0):
    """Poll the mock MDF state until matcher(ev) is true for some
    received X2 event. Returns the matched event or None."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        st = _mock_mdf_state()
        for batch in st.get("x2") or []:
            for ev in batch.get("events", []):
                if matcher(ev):
                    return ev
        time.sleep(0.2)
    return None


def _wait_for_mock_x3(matcher, timeout=8.0):
    deadline = time.time() + timeout
    while time.time() < deadline:
        st = _mock_mdf_state()
        for batch in st.get("x3") or []:
            for ev in batch.get("events", []):
                if matcher(ev):
                    return ev
        time.sleep(0.2)
    return None


def _wait_for_iri(warrant_id, expected_event, target_imsi=None, timeout=6.0):
    """Poll /api/li/warrant/{id}/iri until an event with event_type==expected
    (and optional target IMSI) appears. Returns the matched record or None."""
    deadline = time.time() + timeout
    while time.time() < deadline:
        body, st = _li_api(f"/api/li/warrant/{warrant_id}/iri")
        if st == 200 and isinstance(body, dict):
            for r in body.get("records") or []:
                if r.get("event_type") == expected_event:
                    if target_imsi is None or r.get("target_imsi") == target_imsi:
                        return r
        time.sleep(0.25)
    return None


# ─── TC-LI-001 ───────────────────────────────────────────────────────


class LiIRIOnRegistration(TestCase):
    """TC-LI-001: AMF acts as IRI-POI for Registration to 5GS Event.

    TS 33.128 §6.2.1 + TS 33.127 §7.4.2 — when a target IMSI registers
    to 5GS, the AMF emits a Registration IRI to the LI pipeline.
    Verified by creating a warrant for the test UE's IMSI, registering
    the UE, and confirming a REGISTER record lands in the warrant's
    IRI stream.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-001",
        title="LI AMF emits IRI on Registration to 5GS event",
        spec="TS 33.128 §6.2.1",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Pins the AMF-as-IRI-POI rule per TS 33.128 §6.2.1 + TS 33.127\n"
            "  §7.4.2: when a target IMSI Registers to 5GS, the AMF MUST\n"
            "  emit a Registration IRI to the LI pipeline so the ADMF can\n"
            "  surface it on warrant.iri. This is the foundational LI hook\n"
            "  the rest of TS 33.128 §6.2 builds on.\n"
            "\n"
            "Procedure (TS 33.128 §6.2.1 + TS 33.127 §7.4.2)\n"
            "  1. require_gnb() and require_ue() — get a gNB and SIM-backed UE.\n"
            "  2. Cleanup warrant_id 'W-LI-001' (delete/revoke best-effort).\n"
            "  3. POST /api/li/warrant {warrant_id, authority='test-authority',\n"
            "     case_reference='tc-li-001', target_imsi=ue.imsi, scope='iri',\n"
            "     operator='tester'} → expect 200/201, ok=true.\n"
            "  4. register_ue(ue, gnb) — full 5G-AKA NGAP+NAS handshake.\n"
            "  5. _wait_for_iri(warrant_id, 'REGISTER', ue.imsi, timeout=6s) —\n"
            "     polls /api/li/warrant/{id}/iri until the row appears.\n"
            "  6. Parse event_data JSON for the IRI payload.\n"
            "  7. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — warrant_id and authority are hard-coded for determinism).\n"
            "\n"
            "Pass criteria\n"
            "  A REGISTER IRI row with target_imsi=ue.imsi appears in the\n"
            "  warrant's IRI stream within 6 seconds of registration.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, imsi, iri (parsed event_data dict).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Stage-3 X1/X2 wire transports are still TODO; this test\n"
            "  asserts the per-NF capture into li_iri_events only."
        ),
    )

    def run(self):
        warrant_id = "W-LI-001"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            body, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id,
                "authority": "test-authority",
                "case_reference": "tc-li-001",
                "target_imsi": ue.imsi,
                "scope": "iri",
                "operator": "tester",
            })
            if st not in (200, 201) or not body.get("ok"):
                self.fail_test(f"warrant create failed: {st} {body}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result

            iri = _wait_for_iri(warrant_id, "REGISTER", target_imsi=ue.imsi)
            if iri is None:
                self.fail_test("REGISTER IRI never landed for warrant",
                               imsi=ue.imsi, warrant=warrant_id)
                return self.result

            event_data = iri.get("event_data") or "{}"
            try:
                ev = json.loads(event_data) if isinstance(event_data, str) else event_data
            except Exception:
                ev = {}
            self.pass_test(warrant_id=warrant_id, imsi=ue.imsi, iri=ev)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-002 ───────────────────────────────────────────────────────


class LiIRIOnPDUSession(TestCase):
    """TC-LI-002: SMF acts as IRI-POI for PDU Session Establishment.

    TS 33.128 §6.2.2 + TS 33.127 §7.5 — when a target establishes a
    PDU session, the SMF emits a PDU Session Establishment IRI. The
    payload carries DNN + allocated UE IPv4/v6 + UPF id (operator
    policy data, not part of the spec wire).
    """
    SPEC = TestSpec(
        tc_id="TC-LI-002",
        title="LI SMF emits IRI on PDU Session Establishment event",
        spec="TS 33.128 §6.2.2",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the SMF-as-IRI-POI rule per TS 33.128 §6.2.2 + TS 33.127\n"
            "  §7.5: when a target establishes a PDU session, the SMF MUST\n"
            "  emit a PDU Session Establishment IRI carrying at least DNN\n"
            "  and the allocated UE IPv4 so the ADMF can correlate sessions\n"
            "  to target identity.\n"
            "\n"
            "Procedure (TS 33.128 §6.2.2 + TS 33.127 §7.5)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-002'.\n"
            "  3. POST /api/li/warrant with scope='iri', target_imsi=ue.imsi.\n"
            "  4. register_ue(ue, gnb) — establishes 5G-AKA + NAS context.\n"
            "  5. establish_pdu(ue) — NAS PDU Session Establishment to DNN\n"
            "     'internet', PSI=1.\n"
            "  6. _wait_for_iri(warrant_id, 'PDU_SESSION_ESTABLISHMENT',\n"
            "     ue.imsi, timeout=6s).\n"
            "  7. Parse event_data JSON; assert dnn is non-empty.\n"
            "  8. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — warrant_id, authority, scope hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  A PDU_SESSION_ESTABLISHMENT IRI row appears for the target\n"
            "  IMSI within 6 seconds AND its event_data carries a 'dnn' key.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, imsi, dnn, ipv4.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  X1/X2/X3 wire transports remain TODO; this test only\n"
            "  asserts SMF capture into li_iri_events."
        ),
    )

    def run(self):
        warrant_id = "W-LI-002"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            body, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id,
                "authority": "test-authority",
                "case_reference": "tc-li-002",
                "target_imsi": ue.imsi,
                "scope": "iri",
                "operator": "tester",
            })
            if st not in (200, 201):
                self.fail_test(f"warrant create failed: {st} {body}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            iri = _wait_for_iri(warrant_id, "PDU_SESSION_ESTABLISHMENT",
                                target_imsi=ue.imsi)
            if iri is None:
                self.fail_test("PDU_SESSION_ESTABLISHMENT IRI never landed",
                               imsi=ue.imsi, warrant=warrant_id)
                return self.result
            try:
                ev = json.loads(iri.get("event_data") or "{}")
            except Exception:
                ev = {}
            if not ev.get("dnn"):
                self.fail_test(f"IRI payload missing dnn: {ev}")
                return self.result
            self.pass_test(warrant_id=warrant_id, imsi=ue.imsi,
                           dnn=ev.get("dnn"), ipv4=ev.get("ipv4"))
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-003 ───────────────────────────────────────────────────────


class LiCCActivationOnPDUSession(TestCase):
    """TC-LI-003: SMF/UPF act as CC-POI for cc-scoped warrants.

    TS 33.127 §7.5 + TS 33.128 §6.2.4 — a warrant with scope iri+cc
    (or cc) causes a CC session to be opened on PDU session
    establishment. We don't yet fork the actual content stream
    (TODO: TS 33.127 X3 transport — deferred); the metadata row in
    li_cc_sessions is what we assert against today, exposed via
    GET /api/li/cc-sessions.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-003",
        title="LI iri+cc warrant opens CC session on PDU establishment",
        spec="TS 33.127 §7.5",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the SMF+UPF-as-CC-POI rule per TS 33.127 §7.5 +\n"
            "  TS 33.128 §6.2.4: a warrant with scope iri+cc (or cc) must\n"
            "  open a CC session row in li_cc_sessions when the target's\n"
            "  PDU session establishes — even though X3 wire transport for\n"
            "  content frames is still TODO at the core.\n"
            "\n"
            "Procedure (TS 33.127 §7.5)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-003'.\n"
            "  3. POST /api/li/warrant with scope='iri+cc',\n"
            "     target_imsi=ue.imsi.\n"
            "  4. register_ue(ue, gnb).\n"
            "  5. establish_pdu(ue) — fires the SMF CheckAndActivateCC hook.\n"
            "  6. Poll GET /api/li/cc-sessions?imsi={imsi} for up to 4 s\n"
            "     until a non-empty list is returned.\n"
            "  7. Assert the first row's warrant_id matches.\n"
            "  8. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — warrant_id, scope hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  /api/li/cc-sessions returns at least one row for the IMSI\n"
            "  AND its warrant_id matches the provisioned warrant.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, imsi, session_type.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Asserts metadata row only; X3 content frames are roadmap."
        ),
    )

    def run(self):
        warrant_id = "W-LI-003"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            body, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id,
                "authority": "test-authority",
                "case_reference": "tc-li-003",
                "target_imsi": ue.imsi,
                "scope": "iri+cc",
                "operator": "tester",
            })
            if st not in (200, 201):
                self.fail_test(f"warrant create failed: {st} {body}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            # Poll cc-sessions; the SMF establish path fires
            # CheckAndActivateCC at the same point as the IRI capture.
            deadline = time.time() + 4.0
            sessions = []
            while time.time() < deadline:
                body, st = _li_api(f"/api/li/cc-sessions?imsi={ue.imsi}")
                if st == 200 and isinstance(body, list) and body:
                    sessions = body
                    break
                time.sleep(0.25)
            if not sessions:
                self.fail_test("no CC session opened for iri+cc warrant",
                               imsi=ue.imsi, warrant=warrant_id)
                return self.result
            row = sessions[0]
            if row.get("warrant_id") != warrant_id:
                self.fail_test(f"CC session bound to wrong warrant: {row}")
                return self.result
            self.pass_test(warrant_id=warrant_id, imsi=ue.imsi,
                           session_type=row.get("session_type"))
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-010 ───────────────────────────────────────────────────────


class LiWarrantProvisioningAPI(TestCase):
    """TC-LI-010: ADMF provisioning round-trip via REST.

    TS 33.127 §5.3 — operator provisions warrants through the ADMF
    surface; the catalog lists active warrants. POST /api/li/warrant
    with a fresh warrant_id, then GET /api/li/warrants asserts the
    row is visible with status=active.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-010",
        title="LI ADMF warrant provisioning round-trip via REST",
        spec="TS 33.127 §5.3",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Smoke gate on the ADMF provisioning round-trip per\n"
            "  TS 33.127 §5.3. Operator-driven warrants land via\n"
            "  POST /api/li/warrant; the active catalog is GET\n"
            "  /api/li/warrants?status=active. Any divergence between\n"
            "  what was POSTed and what the catalog returns is a\n"
            "  provisioning regression.\n"
            "\n"
            "Procedure (TS 33.127 §5.3)\n"
            "  1. Cleanup warrant_id 'W-LI-010'.\n"
            "  2. POST /api/li/warrant {warrant_id, authority='court-test',\n"
            "     case_reference='tc-li-010', target_imsi='imsi-tc-li-010',\n"
            "     scope='iri', mdf_endpoint='mdf://test', operator='tester'}\n"
            "     → expect 200/201, ok=true.\n"
            "  3. GET /api/li/warrants?status=active → expect 200, list.\n"
            "  4. Locate the warrant by warrant_id; assert scope=='iri' and\n"
            "     status=='active'.\n"
            "  5. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — provisioning fields are hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Provisioning POST returns ok=true AND the warrant appears in\n"
            "  the active list with the same scope and status='active'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, scope.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Uses placeholder mdf_endpoint='mdf://test'; no delivery\n"
            "  worker is exercised here."
        ),
    )

    def run(self):
        warrant_id = "W-LI-010"
        try:
            _cleanup_warrant(warrant_id)
            body, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id,
                "authority": "court-test",
                "case_reference": "tc-li-010",
                "target_imsi": "imsi-tc-li-010",
                "scope": "iri",
                "mdf_endpoint": "mdf://test",
                "operator": "tester",
            })
            if st not in (200, 201) or not body.get("ok"):
                self.fail_test(f"create failed: {st} {body}")
                return self.result

            rows, st = _li_api("/api/li/warrants?status=active")
            if st != 200 or not isinstance(rows, list):
                self.fail_test(f"list failed: {st} {rows}")
                return self.result
            row = next((r for r in rows if r.get("warrant_id") == warrant_id), None)
            if row is None:
                self.fail_test(f"warrant {warrant_id} not in active list")
                return self.result
            if row.get("scope") != "iri" or row.get("status") != "active":
                self.fail_test(f"unexpected row state: {row}")
                return self.result
            self.pass_test(warrant_id=warrant_id, scope=row.get("scope"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-011 ───────────────────────────────────────────────────────


class LiAuditLogLifecycle(TestCase):
    """TC-LI-011: ADMF audit trail covers lifecycle verbs.

    TS 33.127 §8 — the audit log MUST record every provisioning
    action with operator identity + timestamp. Verified by creating
    then revoking a warrant and asserting both `warrant_created` and
    `warrant_revoked` actions appear in /api/li/audit?warrant_id=…
    """
    SPEC = TestSpec(
        tc_id="TC-LI-011",
        title="LI ADMF audit log records warrant create and revoke",
        spec="TS 33.127 §8",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the ADMF audit-trail contract per TS 33.127 §8: every\n"
            "  provisioning action MUST persist an audit row with the\n"
            "  operator identity and timestamp. This test guards the two\n"
            "  most common verbs (warrant_created + warrant_revoked).\n"
            "\n"
            "Procedure (TS 33.127 §8)\n"
            "  1. Cleanup warrant_id 'W-LI-011'.\n"
            "  2. POST /api/li/warrant {warrant_id, authority='a',\n"
            "     case_reference='tc-li-011', target_imsi='imsi-tc-li-011',\n"
            "     scope='iri', operator='alice'} → expect 200/201.\n"
            "  3. POST /api/li/warrant/W-LI-011/revoke.\n"
            "  4. GET /api/li/audit?warrant_id=W-LI-011 → expect 200, list.\n"
            "  5. Collect distinct .action values; assert both\n"
            "     'warrant_created' and 'warrant_revoked' are present.\n"
            "  6. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — operator='alice' is the audit identity under test).\n"
            "\n"
            "Pass criteria\n"
            "  The audit log for this warrant contains both\n"
            "  'warrant_created' and 'warrant_revoked' actions.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, actions (sorted list of audit actions).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Operator identity is asserted via the audit row's presence,\n"
            "  not by string-matching on 'alice'."
        ),
    )

    def run(self):
        warrant_id = "W-LI-011"
        try:
            _cleanup_warrant(warrant_id)
            _, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id, "authority": "a",
                "case_reference": "tc-li-011", "target_imsi": "imsi-tc-li-011",
                "scope": "iri", "operator": "alice",
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st}")
                return self.result
            _li_api(f"/api/li/warrant/{warrant_id}/revoke", "POST")

            rows, st = _li_api(f"/api/li/audit?warrant_id={warrant_id}")
            if st != 200 or not isinstance(rows, list):
                self.fail_test(f"audit list failed: {st} {rows}")
                return self.result
            actions = {r.get("action") for r in rows}
            if "warrant_created" not in actions or "warrant_revoked" not in actions:
                self.fail_test(f"audit missing lifecycle actions: {actions}")
                return self.result
            self.pass_test(warrant_id=warrant_id, actions=sorted(actions))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-012 ───────────────────────────────────────────────────────


class LiDeactivationStopsCapture(TestCase):
    """TC-LI-012: revoking a warrant stops further IRI capture.

    TS 33.127 §5.3.3 — target deactivation must drop the POI's
    matching state. After RevokeWarrant, refreshTargets rebuilds
    the per-IMSI cache and CaptureIRI no-ops for that IMSI.

    Verified end-to-end by: register UE → REGISTER IRI captured;
    revoke the warrant; deregister UE; confirm the AMF's
    DEREGISTER hook produced NO additional IRI row (because the
    cache no longer matches this IMSI by the time the dereg
    completes).
    """
    SPEC = TestSpec(
        tc_id="TC-LI-012",
        title="LI revoked warrant stops further IRI capture",
        spec="TS 33.127 §5.3.3",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the deactivation contract per TS 33.127 §5.3.3:\n"
            "  revoking a warrant MUST drop the matching state in the\n"
            "  POI cache so subsequent UE events for that IMSI no longer\n"
            "  surface IRI rows for the revoked warrant. This guards the\n"
            "  end-to-end stop-the-stream behaviour.\n"
            "\n"
            "Procedure (TS 33.127 §5.3.3)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-012'.\n"
            "  3. POST /api/li/warrant with scope='iri', target_imsi=ue.imsi.\n"
            "  4. register_ue(ue, gnb) — produces REGISTER IRI.\n"
            "  5. _wait_for_iri(warrant_id, 'REGISTER', ue.imsi).\n"
            "  6. Snapshot count_pre_revoke via /warrant/{id}/iri.\n"
            "  7. POST /api/li/warrant/{id}/revoke; sleep 0.5s for the AMF\n"
            "     refreshTargets cycle.\n"
            "  8. deregister_ue(ue); sleep 1.0s for any IRI to land.\n"
            "  9. Re-read /warrant/{id}/iri; assert count_post <=\n"
            "     count_pre_revoke (no new rows after revoke).\n"
            " 10. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSI bound to the UE returned by require_ue()).\n"
            "\n"
            "Pass criteria\n"
            "  count_post is NOT greater than count_pre_revoke — no IRI\n"
            "  rows landed for the warrant after revocation.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, iri_pre_revoke, iri_post.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Sleep windows (0.5s + 1.0s) cover refreshTargets cadence."
        ),
    )

    def run(self):
        warrant_id = "W-LI-012"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            _, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id, "authority": "a",
                "case_reference": "tc-li-012", "target_imsi": ue.imsi,
                "scope": "iri", "operator": "tester",
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result
            first = _wait_for_iri(warrant_id, "REGISTER", target_imsi=ue.imsi)
            if first is None:
                self.fail_test("first REGISTER IRI never landed")
                return self.result

            body, _ = _li_api(f"/api/li/warrant/{warrant_id}/iri")
            count_pre_revoke = len((body or {}).get("records") or [])

            # Revoke first, then trigger a UE event (deregistration)
            # that *would* be captured if the warrant were still active.
            _li_api(f"/api/li/warrant/{warrant_id}/revoke", "POST")
            time.sleep(0.5)  # let refreshTargets settle
            self.deregister_ue(ue)
            time.sleep(1.0)

            body, _ = _li_api(f"/api/li/warrant/{warrant_id}/iri")
            count_post = len((body or {}).get("records") or [])
            if count_post > count_pre_revoke:
                self.fail_test(
                    f"IRI captured after revoke: {count_pre_revoke} -> {count_post}")
                return self.result
            self.pass_test(warrant_id=warrant_id,
                           iri_pre_revoke=count_pre_revoke,
                           iri_post=count_post)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-013 ───────────────────────────────────────────────────────


class LiScopeRejectsBogus(TestCase):
    """TC-LI-013: ADMF rejects warrants with invalid scope.

    TS 33.127 §5.3 — scope MUST be one of {iri, cc, iri+cc}. The
    package rejects anything else; the route surface returns 400
    so misconfigured operators cannot land bad rows.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-013",
        title="LI ADMF rejects warrant with invalid scope",
        spec="TS 33.127 §5.3",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative-path guard per TS 33.127 §5.3: warrant scope MUST\n"
            "  be one of {iri, cc, iri+cc}. The ADMF must reject anything\n"
            "  else at the API boundary so misconfigured operators cannot\n"
            "  land malformed warrants in the catalog.\n"
            "\n"
            "Procedure (TS 33.127 §5.3)\n"
            "  1. POST /api/li/warrant {warrant_id='W-LI-013',\n"
            "     authority='a', case_reference='tc-li-013',\n"
            "     target_imsi='imsi-x', scope='audio-only',\n"
            "     operator='tester'}.\n"
            "  2. If accepted (200/201), best-effort cleanup and fail.\n"
            "  3. Assert the response body mentions 'scope' (case-insensitive\n"
            "     substring match on the stringified body).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — scope='audio-only' is the bogus value under test).\n"
            "\n"
            "Pass criteria\n"
            "  Provisioning POST is rejected (status != 200/201) AND the\n"
            "  error body mentions 'scope'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  status, error (first 120 chars of the error body).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Cleanup is best-effort in case a buggy build accepted it.\n"
            "  Test does not assert any specific HTTP code (e.g. 400 vs 422),\n"
            "  only that it is not a success."
        ),
    )

    def run(self):
        try:
            body, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": "W-LI-013", "authority": "a",
                "case_reference": "tc-li-013", "target_imsi": "imsi-x",
                "scope": "audio-only", "operator": "tester",
            })
            if st == 200 or st == 201:
                _cleanup_warrant("W-LI-013")
                self.fail_test(f"bogus scope accepted: {body}")
                return self.result
            if "scope" not in str(body).lower():
                self.fail_test(f"error message doesn't mention scope: {body}")
                return self.result
            self.pass_test(status=st, error=str(body)[:120])
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── TC-LI-014 ───────────────────────────────────────────────────────


class LiRequiredFields(TestCase):
    """TC-LI-014: ADMF rejects warrants missing required fields.

    TS 33.127 §5.3 — every warrant carries at least a unique id,
    issuing authority, case reference, and target identity. Missing
    target_imsi is rejected.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-014",
        title="LI ADMF rejects warrant missing target_imsi",
        spec="TS 33.127 §5.3",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative-path guard per TS 33.127 §5.3: every warrant MUST\n"
            "  carry at minimum {warrant_id, authority, case_reference,\n"
            "  target identity}. A POST missing target_imsi must be\n"
            "  refused at the API boundary so the catalog cannot land\n"
            "  rows that the POI cache could never match against a UE.\n"
            "\n"
            "Procedure (TS 33.127 §5.3)\n"
            "  1. POST /api/li/warrant {warrant_id='W-LI-014',\n"
            "     authority='a', case_reference='tc-li-014', scope='iri',\n"
            "     operator='tester'} — note the deliberate omission of\n"
            "     target_imsi.\n"
            "  2. Inspect the HTTP status; if accepted as 200/201, attempt\n"
            "     a best-effort cleanup_warrant('W-LI-014') and fail the\n"
            "     test (the API leaked a bad row).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payload is hard-coded to miss target_imsi).\n"
            "\n"
            "Pass criteria\n"
            "  The provisioning POST is rejected with status not in\n"
            "  {200, 201} — the missing-target-identity gate fires.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  status (the HTTP status code from the rejected POST).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Cleanup runs only if the row leaked through (defensive).\n"
            "  Test does not assert a specific HTTP code, only non-success."
        ),
    )

    def run(self):
        try:
            body, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": "W-LI-014", "authority": "a",
                "case_reference": "tc-li-014",
                "scope": "iri", "operator": "tester",
            })
            if st in (200, 201):
                _cleanup_warrant("W-LI-014")
                self.fail_test(f"missing target_imsi accepted: {body}")
                return self.result
            self.pass_test(status=st)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── TC-LI-015 ───────────────────────────────────────────────────────


class LiStatsCounter(TestCase):
    """TC-LI-015: /api/li/stats reports the active warrant count.

    OAM dashboard consumes this. Two new active warrants raise the
    counter by 2 from the snapshot baseline; revoking one drops it
    by 1.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-015",
        title="LI /stats active_warrants counter tracks creates and revokes",
        spec="TS 33.127 §8",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Guards the OAM stats counter contract per TS 33.127 §8:\n"
            "  /api/li/stats.active_warrants must move in lock-step with\n"
            "  warrant create / revoke verbs so the dashboard tile is\n"
            "  trustworthy.\n"
            "\n"
            "Procedure (TS 33.127 §8)\n"
            "  1. Cleanup warrant_ids 'W-LI-015a', 'W-LI-015b'.\n"
            "  2. GET /api/li/stats; record base_n = active_warrants.\n"
            "  3. POST /api/li/warrant for each of the two warrant ids\n"
            "     (scope='iri', target imsi-<wid>).\n"
            "  4. GET /api/li/stats; record after_n; assert\n"
            "     after_n - base_n >= 2.\n"
            "  5. POST /api/li/warrant/W-LI-015a/revoke.\n"
            "  6. GET /api/li/stats; record post_n; assert\n"
            "     post_n == after_n - 1.\n"
            "  7. finally: cleanup both warrants.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — warrant ids and scope hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  after_n - base_n >= 2 (creates) AND\n"
            "  post_n == after_n - 1 (revoke drops by exactly one).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  base, after_create, after_revoke.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Other test runs in parallel may bump the counter; the\n"
            "  assertion uses >= for the create delta to remain robust."
        ),
    )

    def run(self):
        wids = ["W-LI-015a", "W-LI-015b"]
        try:
            for w in wids:
                _cleanup_warrant(w)
            base, st = _li_api("/api/li/stats")
            if st != 200 or not isinstance(base, dict):
                self.fail_test(f"stats failed: {st} {base}")
                return self.result
            base_n = int(base.get("active_warrants") or 0)
            for w in wids:
                _, st = _li_api("/api/li/warrant", "POST", {
                    "warrant_id": w, "authority": "a",
                    "case_reference": "tc-li-015", "target_imsi": f"imsi-{w}",
                    "scope": "iri", "operator": "tester",
                })
                if st not in (200, 201):
                    self.fail_test(f"create failed: {w} {st}")
                    return self.result
            after, _ = _li_api("/api/li/stats")
            after_n = int(after.get("active_warrants") or 0)
            if after_n - base_n < 2:
                self.fail_test(f"counter didn't bump: base={base_n} after={after_n}")
                return self.result
            _li_api(f"/api/li/warrant/{wids[0]}/revoke", "POST")
            post, _ = _li_api("/api/li/stats")
            post_n = int(post.get("active_warrants") or 0)
            if post_n != after_n - 1:
                self.fail_test(f"revoke didn't drop counter: after={after_n} post={post_n}")
                return self.result
            self.pass_test(base=base_n, after_create=after_n, after_revoke=post_n)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for w in wids:
                _cleanup_warrant(w)
        return self.result


# ─── TC-LI-016 ───────────────────────────────────────────────────────


class LiMarkDelivered(TestCase):
    """TC-LI-016: MDF-ack pipeline flips delivered=1 on IRI rows.

    TS 33.127 X2 (deferred wire) — once the MDF has accepted IRI
    events up to a sequence number, the ADMF flips them in the
    pending→delivered queue. Today this is exposed via
    POST /api/li/warrant/{id}/mark-delivered for OAM-driven flush.
    Verified by capturing IRIs (via live registration on the same
    UE), calling mark-delivered, and observing the GetDeliveryStats
    counters move.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-016",
        title="LI mark-delivered flips pending IRI rows to delivered",
        spec="TS 33.127 §6.3",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Pins the OAM-driven flush of pending IRI rows per\n"
            "  TS 33.127 §6.3. mark-delivered flips li_iri_events rows\n"
            "  from pending to delivered up to a max_id; this is the\n"
            "  catch-up path when the MDF acks out-of-band.\n"
            "\n"
            "Procedure (TS 33.127 §6.3)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-016'.\n"
            "  3. POST /api/li/warrant scope='iri', target_imsi=ue.imsi.\n"
            "  4. register_ue(ue, gnb); _wait_for_iri(warrant, 'REGISTER').\n"
            "  5. GET /api/li/stats; snapshot iri.pending = pending_pre;\n"
            "     assert pending_pre > 0.\n"
            "  6. POST /api/li/warrant/{id}/mark-delivered {max_id=0}\n"
            "     (max_id=0 → server picks unbounded) → expect 200.\n"
            "  7. GET /api/li/stats; assert iri.pending < pending_pre.\n"
            "  8. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — max_id=0 (∞) is hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  pending_pre > 0 AND post-mark iri.pending < pending_pre.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending_pre, pending_post, delivered_post.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  max_id=0 uses the server-side 'all rows' interpretation."
        ),
    )

    def run(self):
        warrant_id = "W-LI-016"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            _, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id, "authority": "a",
                "case_reference": "tc-li-016", "target_imsi": ue.imsi,
                "scope": "iri", "operator": "tester",
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st}")
                return self.result
            if not self.register_ue(ue, gnb):
                return self.result
            if _wait_for_iri(warrant_id, "REGISTER", target_imsi=ue.imsi) is None:
                self.fail_test("REGISTER IRI never landed pre-mark")
                return self.result

            stats_pre, _ = _li_api("/api/li/stats")
            iri_pre = (stats_pre or {}).get("iri") or {}
            pending_pre = int(iri_pre.get("pending") or 0)
            if pending_pre <= 0:
                self.fail_test(f"expected pending>0 before mark, got {iri_pre}")
                return self.result

            _, st = _li_api(f"/api/li/warrant/{warrant_id}/mark-delivered",
                            "POST", {"max_id": 0})  # 0 → server picks ∞
            if st != 200:
                self.fail_test(f"mark-delivered failed: {st}")
                return self.result

            stats_post, _ = _li_api("/api/li/stats")
            iri_post = (stats_post or {}).get("iri") or {}
            if int(iri_post.get("pending") or 0) >= pending_pre:
                self.fail_test(f"pending didn't drop: pre={iri_pre} post={iri_post}")
                return self.result
            self.pass_test(pending_pre=pending_pre,
                           pending_post=iri_post.get("pending"),
                           delivered_post=iri_post.get("delivered"))
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-017 ───────────────────────────────────────────────────────


class LiCcOnlyScopeNoIRI(TestCase):
    """TC-LI-017: cc-only scope MUST NOT capture IRI events.

    TS 33.127 §7 — IRI vs CC are separate handover surfaces; a
    cc-only warrant only opens the CC stream and must not bleed
    over into IRI capture (separation enforced in li.CaptureIRI:
    only iri / iri+cc scopes hit the IRI table).
    """
    SPEC = TestSpec(
        tc_id="TC-LI-017",
        title="LI cc-only warrant skips IRI capture",
        spec="TS 33.127 §7",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the IRI/CC separation per TS 33.127 §7: a cc-only\n"
            "  warrant must open the CC stream only and never bleed into\n"
            "  the IRI table. The separation is enforced in\n"
            "  li.CaptureIRI which short-circuits on cc-only scope.\n"
            "\n"
            "Procedure (TS 33.127 §7)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-017'.\n"
            "  3. POST /api/li/warrant with scope='cc',\n"
            "     target_imsi=ue.imsi.\n"
            "  4. register_ue(ue, gnb).\n"
            "  5. establish_pdu(ue).\n"
            "  6. Sleep 1.0s to let any stray IRI capture flush.\n"
            "  7. GET /api/li/warrant/{id}/iri; assert records count == 0.\n"
            "  8. GET /api/li/cc-sessions?imsi={imsi}; assert at least one\n"
            "     row with matching warrant_id.\n"
            "  9. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — scope='cc' is the value under test).\n"
            "\n"
            "Pass criteria\n"
            "  IRI records list is empty for this warrant AND at least one\n"
            "  CC session row exists for the IMSI bound to this warrant.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, iri_count, cc_sessions.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Sleep guards against late-landing IRI rows."
        ),
    )

    def run(self):
        warrant_id = "W-LI-017"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            _, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id, "authority": "a",
                "case_reference": "tc-li-017", "target_imsi": ue.imsi,
                "scope": "cc", "operator": "tester",
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result
            time.sleep(1.0)

            body, _ = _li_api(f"/api/li/warrant/{warrant_id}/iri")
            iri_count = len((body or {}).get("records") or [])
            if iri_count != 0:
                self.fail_test(
                    f"cc-only warrant captured IRI: {iri_count} record(s)")
                return self.result
            cc, _ = _li_api(f"/api/li/cc-sessions?imsi={ue.imsi}")
            if not (isinstance(cc, list) and any(
                    r.get("warrant_id") == warrant_id for r in cc)):
                self.fail_test("cc-only warrant didn't open CC session")
                return self.result
            self.pass_test(warrant_id=warrant_id,
                           iri_count=iri_count,
                           cc_sessions=len(cc))
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-018 ───────────────────────────────────────────────────────


class LiX1Provision(TestCase):
    """TC-LI-018: ADMF→POI provisioning via the X1 reference point.

    TS 33.127 §6.2 — X1 is the canonical ADMF surface. Verifies the
    /api/li/x1/provision route accepts a warrant in the X1 payload
    shape and the row appears in the active warrant list with the
    matching scope. The audit log carries an x1_provision row in
    addition to the underlying warrant_created row.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-018",
        title="LI X1 provision creates an active warrant",
        spec="TS 33.127 §6.2",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the X1 reference-point provisioning verb per TS 33.127\n"
            "  §6.2. /api/li/x1/provision is the canonical ADMF→POI\n"
            "  provisioning surface; it must land an active warrant and\n"
            "  also lay an 'x1_provision' audit row alongside the\n"
            "  underlying 'warrant_created'.\n"
            "\n"
            "Procedure (TS 33.127 §6.2)\n"
            "  1. Cleanup warrant_id 'W-LI-018'.\n"
            "  2. POST /api/li/x1/provision {warrant_id,\n"
            "     authority='x1-admf', case_reference='tc-li-018',\n"
            "     target_imsi='imsi-tc-li-018', scope='iri+cc',\n"
            "     operator='x1-tester'} → expect 200/201, ok=true.\n"
            "  3. GET /api/li/warrants?status=active → locate warrant\n"
            "     by id; assert scope == 'iri+cc'.\n"
            "  4. GET /api/li/audit?warrant_id=W-LI-018 → collect distinct\n"
            "     action values; assert both 'x1_provision' and\n"
            "     'warrant_created' are present.\n"
            "  5. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — X1 payload is hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  X1 provision returns ok=true, warrant is active with\n"
            "  requested scope, AND audit log carries both required actions.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, audit_actions.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  X1 wire-shape stage-3 is operator-facing JSON; full ASN.1\n"
            "  X1 over TLS is roadmap."
        ),
    )

    def run(self):
        warrant_id = "W-LI-018"
        try:
            _cleanup_warrant(warrant_id)
            body, st = _li_api("/api/li/x1/provision", "POST", {
                "warrant_id": warrant_id,
                "authority": "x1-admf",
                "case_reference": "tc-li-018",
                "target_imsi": "imsi-tc-li-018",
                "scope": "iri+cc",
                "operator": "x1-tester",
            })
            if st not in (200, 201) or not body.get("ok"):
                self.fail_test(f"x1 provision failed: {st} {body}")
                return self.result
            rows, _ = _li_api(f"/api/li/warrants?status=active")
            row = next((r for r in (rows or []) if r.get("warrant_id") == warrant_id), None)
            if row is None or row.get("scope") != "iri+cc":
                self.fail_test(f"warrant not visible / wrong scope: {row}")
                return self.result
            audit, _ = _li_api(f"/api/li/audit?warrant_id={warrant_id}")
            actions = {r.get("action") for r in (audit or [])}
            if "x1_provision" not in actions or "warrant_created" not in actions:
                self.fail_test(f"audit missing x1_provision: {actions}")
                return self.result
            self.pass_test(warrant_id=warrant_id, audit_actions=sorted(actions))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-019 ───────────────────────────────────────────────────────


class LiX1Deactivate(TestCase):
    """TC-LI-019: X1 deactivate flips a warrant from active to revoked.

    TS 33.127 §6.2 — deactivate is the spec-named verb for stopping
    interception. /api/li/x1/deactivate/{id} drops the cache entry
    and lays an x1_deactivate audit row alongside warrant_revoked.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-019",
        title="LI X1 deactivate flips active warrant to revoked",
        spec="TS 33.127 §6.2",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the X1 deactivate verb per TS 33.127 §6.2 — the\n"
            "  spec-named stop-interception verb. /api/li/x1/deactivate\n"
            "  flips the warrant's status from active to revoked AND lays\n"
            "  an 'x1_deactivate' audit row.\n"
            "\n"
            "Procedure (TS 33.127 §6.2)\n"
            "  1. Cleanup warrant_id 'W-LI-019'.\n"
            "  2. POST /api/li/x1/provision (authority='x1-admf',\n"
            "     scope='iri', target_imsi='imsi-tc-li-019').\n"
            "  3. POST /api/li/x1/deactivate/W-LI-019 → expect 200, ok=true.\n"
            "  4. GET /api/li/warrants → locate warrant; assert\n"
            "     status == 'revoked'.\n"
            "  5. GET /api/li/audit?warrant_id=W-LI-019 → assert\n"
            "     'x1_deactivate' is in the action set.\n"
            "  6. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payload hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Deactivate returns ok=true AND the warrant row's status\n"
            "  is now 'revoked' AND the audit log records 'x1_deactivate'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, status.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  The provisioned warrant in step 2 may not appear in\n"
            "  /warrants?status=active if upstream catalog is filtered;\n"
            "  test queries /warrants unfiltered to find it."
        ),
    )

    def run(self):
        warrant_id = "W-LI-019"
        try:
            _cleanup_warrant(warrant_id)
            _li_api("/api/li/x1/provision", "POST", {
                "warrant_id": warrant_id, "authority": "x1-admf",
                "case_reference": "tc-li-019", "target_imsi": "imsi-tc-li-019",
                "scope": "iri", "operator": "x1-tester",
            })
            body, st = _li_api(f"/api/li/x1/deactivate/{warrant_id}", "POST")
            if st != 200 or not body.get("ok"):
                self.fail_test(f"deactivate failed: {st} {body}")
                return self.result
            row, _ = _li_api(f"/api/li/warrants")
            r = next((x for x in (row or []) if x.get("warrant_id") == warrant_id), None)
            if r is None or r.get("status") != "revoked":
                self.fail_test(f"status not flipped: {r}")
                return self.result
            audit, _ = _li_api(f"/api/li/audit?warrant_id={warrant_id}")
            actions = {x.get("action") for x in (audit or [])}
            if "x1_deactivate" not in actions:
                self.fail_test(f"audit missing x1_deactivate: {actions}")
                return self.result
            self.pass_test(warrant_id=warrant_id, status=r.get("status"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-020 ───────────────────────────────────────────────────────


class LiX1Modify(TestCase):
    """TC-LI-020: X1 modify changes scope / end_time / mdf_endpoint.

    TS 33.127 §6.2 — the ADMF can adjust mutable warrant fields over
    the warrant's lifetime. Target identity is fixed; everything
    else (scope, end_time, MDF endpoint) is modifiable.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-020",
        title="LI X1 modify updates scope and MDF endpoint of warrant",
        spec="TS 33.127 §6.2",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the X1 modify verb per TS 33.127 §6.2: scope and\n"
            "  mdf_endpoint are mutable over a warrant's lifetime (target\n"
            "  identity is not). /api/li/x1/modify must accept the new\n"
            "  values and the catalog must reflect them on readback.\n"
            "\n"
            "Procedure (TS 33.127 §6.2)\n"
            "  1. Cleanup warrant_id 'W-LI-020'.\n"
            "  2. POST /api/li/x1/provision with scope='iri',\n"
            "     mdf_endpoint='mdf://old'.\n"
            "  3. POST /api/li/x1/modify {warrant_id, scope='iri+cc',\n"
            "     mdf_endpoint='mdf://new', operator='x1-tester'}\n"
            "     → expect 200, ok=true.\n"
            "  4. GET /api/li/warrants → locate the warrant; assert\n"
            "     scope=='iri+cc' AND mdf_endpoint=='mdf://new'.\n"
            "  5. finally: revoke + delete the warrant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — modify payload hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Modify returns ok=true AND the catalog row reflects both\n"
            "  the new scope and the new mdf_endpoint.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, scope, mdf_endpoint.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Target identity (target_imsi) is immutable and not modified\n"
            "  by this test."
        ),
    )

    def run(self):
        warrant_id = "W-LI-020"
        try:
            _cleanup_warrant(warrant_id)
            _li_api("/api/li/x1/provision", "POST", {
                "warrant_id": warrant_id, "authority": "x1-admf",
                "case_reference": "tc-li-020", "target_imsi": "imsi-tc-li-020",
                "scope": "iri", "operator": "x1-tester",
                "mdf_endpoint": "mdf://old",
            })
            body, st = _li_api("/api/li/x1/modify", "POST", {
                "warrant_id": warrant_id,
                "scope": "iri+cc",
                "mdf_endpoint": "mdf://new",
                "operator": "x1-tester",
            })
            if st != 200 or not body.get("ok"):
                self.fail_test(f"modify failed: {st} {body}")
                return self.result
            rows, _ = _li_api("/api/li/warrants")
            r = next((x for x in (rows or []) if x.get("warrant_id") == warrant_id), None)
            if r is None or r.get("scope") != "iri+cc" or r.get("mdf_endpoint") != "mdf://new":
                self.fail_test(f"modify not applied: {r}")
                return self.result
            self.pass_test(warrant_id=warrant_id, scope=r.get("scope"),
                           mdf_endpoint=r.get("mdf_endpoint"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
        return self.result


# ─── TC-LI-021 ───────────────────────────────────────────────────────


class LiX2DeliveryToMDF(TestCase):
    """TC-LI-021: X2 IRI delivery — POI POSTs IRI events to MDF.

    TS 33.127 §6.3 — the X2 deliverer pulls pending li_iri_events
    rows for warrants with a non-empty mdf_endpoint and POSTs JSON
    to {endpoint}/x2/iri. Verified by configuring the warrant with
    the tester's mock-MDF URL, registering the UE, and reading the
    captured event off the mock.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-021",
        title="LI X2 deliverer pushes IRI events to the MDF",
        spec="TS 33.127 §6.3",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the X2 IRI delivery worker per TS 33.127 §6.3:\n"
            "  pending li_iri_events for warrants with a configured\n"
            "  mdf_endpoint MUST be POSTed to {endpoint}/x2/iri and the\n"
            "  stats.iri.delivered counter MUST increment on success.\n"
            "\n"
            "Procedure (TS 33.127 §6.3)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-021', reset the tester's mock\n"
            "     MDF, and enable X2/X3 delivery at 200 ms cadence.\n"
            "  3. POST /api/li/warrant with mdf_endpoint pointing at the\n"
            "     tester's mock MDF URL.\n"
            "  4. register_ue(ue, gnb) — produces REGISTER IRI.\n"
            "  5. _wait_for_mock_x2 for an event with warrant_id and\n"
            "     event_type='REGISTER' (timeout 10s).\n"
            "  6. Assert target_imsi in the delivered event matches ue.imsi.\n"
            "  7. GET /api/li/stats; assert iri.delivered > 0.\n"
            "  8. finally: disable X2 delivery, cleanup warrant, reset mock.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — interval_ms=200, mdf URL auto-derived).\n"
            "\n"
            "Pass criteria\n"
            "  REGISTER event with correct warrant_id + target_imsi lands\n"
            "  at the mock MDF AND core stats.iri.delivered > 0.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, event_type, sequence, delivered.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Tester runs the mock MDF on port 5001; core reaches it via\n"
            "  the tester's mmtnet IP."
        ),
    )

    def run(self):
        warrant_id = "W-LI-021"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            _mock_mdf_reset()
            _enable_x2_delivery(interval_ms=200)

            body, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id, "authority": "court-x2",
                "case_reference": "tc-li-021", "target_imsi": ue.imsi,
                "scope": "iri", "operator": "tester",
                "mdf_endpoint": _mock_mdf_url(),
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st} {body}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result

            ev = _wait_for_mock_x2(
                lambda e: e.get("warrant_id") == warrant_id
                and e.get("event_type") == "REGISTER",
                timeout=10.0,
            )
            if ev is None:
                self.fail_test("no REGISTER event delivered to mock MDF",
                               state=_mock_mdf_state())
                return self.result
            if ev.get("target_imsi") != ue.imsi:
                self.fail_test(f"wrong target_imsi in event: {ev}")
                return self.result

            stats, _ = _li_api("/api/li/stats")
            iri = (stats or {}).get("iri") or {}
            if int(iri.get("delivered") or 0) <= 0:
                self.fail_test(f"core didn't flip delivered counter: {iri}")
                return self.result

            self.pass_test(warrant_id=warrant_id,
                           event_type=ev.get("event_type"),
                           sequence=ev.get("sequence"),
                           delivered=iri.get("delivered"))
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _disable_x2_delivery()
            _cleanup_warrant(warrant_id)
            _mock_mdf_reset()
        return self.result


# ─── TC-LI-022 ───────────────────────────────────────────────────────


class LiX2RetryOnFailure(TestCase):
    """TC-LI-022: X2 retries on transient MDF failure.

    TS 33.127 §6.3 — the queue is the buffer; failed deliveries do
    not lose rows. Verified by injecting a 500-fail on the first
    POST attempt; the row stays delivered=0 and the next tick
    succeeds.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-022",
        title="LI X2 deliverer retries on transient MDF failure",
        spec="TS 33.127 §6.3",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins the X2 retry-on-failure contract per TS 33.127 §6.3:\n"
            "  the delivery queue MUST be the buffer — a transient MDF\n"
            "  failure must not lose rows. The worker retries on its next\n"
            "  tick and the row eventually lands.\n"
            "\n"
            "Procedure (TS 33.127 §6.3)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-022', reset mock MDF.\n"
            "  3. POST /_mock_mdf/fail-next?n=1 → arm one 500 response.\n"
            "  4. Enable X2 delivery at 200 ms cadence.\n"
            "  5. POST /api/li/warrant with mdf_endpoint = mock MDF URL.\n"
            "  6. register_ue(ue, gnb).\n"
            "  7. _wait_for_mock_x2 for REGISTER event (timeout 10 s)\n"
            "     — should land on the second tick after the seeded 500.\n"
            "  8. finally: disable X2 delivery, cleanup, reset mock.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — failure count n=1 hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Despite one injected 500 the REGISTER event eventually\n"
            "  lands at the mock MDF within the 10 s polling window.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, sequence.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Mock MDF's fail-next counter decrements per request so the\n"
            "  next tick succeeds."
        ),
    )

    def run(self):
        warrant_id = "W-LI-022"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            _mock_mdf_reset()
            # Inject one failure BEFORE the deliverer ticks; second
            # round-trip succeeds.
            _mock_mdf_local("/_mock_mdf/fail-next?n=1", "POST")
            _enable_x2_delivery(interval_ms=200)

            _, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id, "authority": "court-x2",
                "case_reference": "tc-li-022", "target_imsi": ue.imsi,
                "scope": "iri", "operator": "tester",
                "mdf_endpoint": _mock_mdf_url(),
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result

            ev = _wait_for_mock_x2(
                lambda e: e.get("warrant_id") == warrant_id
                and e.get("event_type") == "REGISTER",
                timeout=10.0,
            )
            if ev is None:
                self.fail_test("retry did not deliver after first 500",
                               state=_mock_mdf_state())
                return self.result
            self.pass_test(warrant_id=warrant_id,
                           sequence=ev.get("sequence"))
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _disable_x2_delivery()
            _cleanup_warrant(warrant_id)
            _mock_mdf_reset()
        return self.result


# ─── TC-LI-023 ───────────────────────────────────────────────────────


class LiX2DisabledDoesNotPush(TestCase):
    """TC-LI-023: with li_x2_enabled=0 the worker does not push.

    Defensive: a deployment without configured MDFs must not
    accidentally exfiltrate. The DB toggle is the off-switch.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-023",
        title="LI X2 disabled flag prevents any MDF delivery",
        spec="TS 33.127 §6.3",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Defensive guard per TS 33.127 §6.3: a deployment without\n"
            "  configured MDFs must not accidentally exfiltrate. With the\n"
            "  network_config flag li_x2_enabled=0, no X2 events may leave\n"
            "  the core even if a warrant has an mdf_endpoint set.\n"
            "\n"
            "Procedure (TS 33.127 §6.3)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-023', reset mock MDF.\n"
            "  3. _disable_x2_delivery() — POST li_x2_enabled=0,\n"
            "     li_x3_enabled=0.\n"
            "  4. POST /api/li/warrant with mdf_endpoint = mock MDF URL.\n"
            "  5. register_ue(ue, gnb) — would normally fire a REGISTER\n"
            "     IRI delivery.\n"
            "  6. time.sleep(2.0) (well past one delivery tick).\n"
            "  7. GET mock MDF state; count x2 events; assert == 0.\n"
            "  8. finally: cleanup warrant, reset mock.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — toggles are hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  After the 2 s wall-clock wait, the mock MDF has received\n"
            "  exactly zero X2 events.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, x2_received.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  Adds ~2s wall-clock; sleep window covers default tick cadence."
        ),
    )

    def run(self):
        warrant_id = "W-LI-023"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            _mock_mdf_reset()
            _disable_x2_delivery()

            _, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id, "authority": "court-x2",
                "case_reference": "tc-li-023", "target_imsi": ue.imsi,
                "scope": "iri", "operator": "tester",
                "mdf_endpoint": _mock_mdf_url(),
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result

            # Wait the deliverer's normal cadence; nothing should land.
            time.sleep(2.0)
            st = _mock_mdf_state()
            x2_received = sum(len(b.get("events", [])) for b in (st.get("x2") or []))
            if x2_received != 0:
                self.fail_test(
                    f"x2 disabled but {x2_received} events delivered: {st}")
                return self.result
            self.pass_test(warrant_id=warrant_id, x2_received=0)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _cleanup_warrant(warrant_id)
            _mock_mdf_reset()
        return self.result


# ─── TC-LI-024 ───────────────────────────────────────────────────────


class LiX3CCDelivery(TestCase):
    """TC-LI-024: X3 CC delivery — OPENED + CLOSED events to MDF.

    TS 33.127 §6.4 — the X3 channel carries CC. Today it carries the
    lifecycle (OPENED on activation, CLOSED on stop); per-packet
    frames are roadmap. Verified by registering with iri+cc scope,
    establishing a PDU session, then deregistering, and confirming
    both OPENED and CLOSED phases land at the mock.
    """
    SPEC = TestSpec(
        tc_id="TC-LI-024",
        title="LI X3 deliverer pushes OPENED and CLOSED CC lifecycle events",
        spec="TS 33.127 §6.4",
        domain=Domain.LAWFUL_INTERCEPT,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Pins the X3 CC lifecycle delivery per TS 33.127 §6.4. The\n"
            "  X3 channel carries content; today it transports the\n"
            "  lifecycle (OPENED on activation, CLOSED on stop). Both\n"
            "  phases MUST land at the configured MDF when scope=iri+cc.\n"
            "  Per-frame X3 content payload is roadmap.\n"
            "\n"
            "Procedure (TS 33.127 §6.4)\n"
            "  1. require_gnb() and require_ue().\n"
            "  2. Cleanup warrant_id 'W-LI-024', reset mock MDF, enable X2/X3\n"
            "     delivery at 200 ms cadence.\n"
            "  3. POST /api/li/warrant with scope='iri+cc',\n"
            "     mdf_endpoint=mock MDF.\n"
            "  4. register_ue(ue, gnb); establish_pdu(ue) — opens CC.\n"
            "  5. _wait_for_mock_x3 for warrant + phase='OPENED'\n"
            "     (timeout 10 s).\n"
            "  6. deregister_ue(ue) — stops CC.\n"
            "  7. _wait_for_mock_x3 for phase='CLOSED' (timeout 10 s).\n"
            "  8. finally: disable X2/X3, cleanup warrant, reset mock.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — scope='iri+cc' hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  Both X3 events (phase='OPENED' on establish AND\n"
            "  phase='CLOSED' on dereg) land at the mock MDF.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  warrant_id, opened_seq, closed_seq.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — sacore reset between runs.\n"
            "  X3 per-packet content delivery is roadmap; only lifecycle\n"
            "  events are asserted."
        ),
    )

    def run(self):
        warrant_id = "W-LI-024"
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            _cleanup_warrant(warrant_id)
            _mock_mdf_reset()
            _enable_x2_delivery(interval_ms=200)

            _, st = _li_api("/api/li/warrant", "POST", {
                "warrant_id": warrant_id, "authority": "court-x3",
                "case_reference": "tc-li-024", "target_imsi": ue.imsi,
                "scope": "iri+cc", "operator": "tester",
                "mdf_endpoint": _mock_mdf_url(),
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st}")
                return self.result

            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            opened = _wait_for_mock_x3(
                lambda e: e.get("warrant_id") == warrant_id
                and e.get("phase") == "OPENED",
                timeout=10.0,
            )
            if opened is None:
                self.fail_test("OPENED CC event not delivered",
                               state=_mock_mdf_state())
                return self.result

            self.deregister_ue(ue)
            closed = _wait_for_mock_x3(
                lambda e: e.get("warrant_id") == warrant_id
                and e.get("phase") == "CLOSED",
                timeout=10.0,
            )
            if closed is None:
                self.fail_test("CLOSED CC event not delivered",
                               state=_mock_mdf_state())
                return self.result
            self.pass_test(warrant_id=warrant_id,
                           opened_seq=opened.get("sequence"),
                           closed_seq=closed.get("sequence"))
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _disable_x2_delivery()
            _cleanup_warrant(warrant_id)
            _mock_mdf_reset()
        return self.result


# ─── Auto-discovery ──────────────────────────────────────────────────


ALL_LI_TCS = [
    LiIRIOnRegistration,
    LiIRIOnPDUSession,
    LiCCActivationOnPDUSession,
    LiWarrantProvisioningAPI,
    LiAuditLogLifecycle,
    LiDeactivationStopsCapture,
    LiScopeRejectsBogus,
    LiRequiredFields,
    LiStatsCounter,
    LiMarkDelivered,
    LiCcOnlyScopeNoIRI,
    LiX1Provision,
    LiX1Deactivate,
    LiX1Modify,
    LiX2DeliveryToMDF,
    LiX2RetryOnFailure,
    LiX2DisabledDoesNotPush,
    LiX3CCDelivery,
]

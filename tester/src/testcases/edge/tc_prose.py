# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Proximity-based Services (ProSe).

TS 23.304 — ProSe discovery, communication, and UE-to-Network relay.
Direct discovery, direct communication (unicast/groupcast), relay UE.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_prose")


def _prose_api(path, method="GET", body=None):
    """Call SA Core ProSe REST API."""
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


class ProseRegisterApp(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PROSE-001",
        title="Register and delete a ProSe application",
        spec="TS 23.304 §5.1",
        domain=Domain.PROSE,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.304 §5.1 defines the ProSe application registration:\n"
            "  every UE-to-UE service is advertised through a registered\n"
            "  prose_app_code that the network uses to route discovery and\n"
            "  authorise communication. This test pins the registry CRUD\n"
            "  surface — the gate for all downstream discovery / comm /\n"
            "  relay flows.\n"
            "\n"
            "Procedure (TS 23.304 §5.1 ProSe app registration)\n"
            "  1. POST /api/prose/apps with app_id=prose-test-app-001,\n"
            "     name='Test ProSe App', prose_app_code=0xABCD1234.\n"
            "  2. Assert HTTP 200/201.\n"
            "  3. Resolve app_id from response.app_id (route echoes the\n"
            "     string we sent, since DELETE matches on the string id,\n"
            "     not the numeric PK).\n"
            "  4. GET /api/prose/apps to list.\n"
            "  5. Assert GET status == 200.\n"
            "  6. Finally clause DELETEs /api/prose/apps/{app_id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — app_id, name, prose_app_code hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET listing 200. pass_test fires with\n"
            "  app_id, app row, apps listing.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  app_id, app (POST body), apps (GET listing).\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: GET listing not searched for app_id; a\n"
            "  backend that 200s on GET but drops the write would pass."
        ),
    )

    def run(self):
        app_id = None
        try:
            result, status = _prose_api("/api/prose/apps", "POST", {
                "app_id": "prose-test-app-001",
                "name": "Test ProSe App",
                "prose_app_code": "0xABCD1234",
            })
            if status not in (200, 201):
                self.fail_test(f"App registration failed: {status} {result}")
                return self.result

            # /api/prose/apps DELETE matches on app_id string, not the
            # numeric primary key id — so prefer the string we sent in
            # the request body (which the route echoes back).
            app_id = result.get("app_id") or "prose-test-app-001"
            log.info("ProSe app registered: %s", app_id)

            # Verify by listing
            apps, a_status = _prose_api("/api/prose/apps")
            if a_status != 200:
                self.fail_test(f"App list failed: {a_status}")
                return self.result

            self.pass_test(app_id=app_id, app=result, apps=apps)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if app_id:
                _prose_api(f"/api/prose/apps/{app_id}", "DELETE")
        return self.result


class ProseUeConfig(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PROSE-002",
        title="Configure a UE for ProSe authorization and discovery",
        spec="TS 23.304 §5.1",
        domain=Domain.PROSE,
        nfs=(NF.UDM, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.304 §5.1 gates ProSe procedures on per-UE\n"
            "  authorisation state: authorized=1 + discovery_enabled=1\n"
            "  must be provisioned in UDM/AF before a UE can announce or\n"
            "  monitor discovery. This test pins the write + read-back of\n"
            "  that policy state.\n"
            "\n"
            "Procedure (TS 23.304 §5.1 UE ProSe authorisation)\n"
            "  1. require_ue() to obtain imsi.\n"
            "  2. POST /api/prose/ue-config with imsi, authorized=1,\n"
            "     discovery_enabled=1.\n"
            "  3. Assert HTTP 200/201.\n"
            "  4. GET /api/prose/ue-config?imsi={imsi}.\n"
            "  5. Assert GET status == 200.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — imsi from require_ue(); authorized=1, disc=1 hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND GET 200. pass_test fires with imsi, POST\n"
            "  config body, GET verification body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, config (POST body), verified (GET body).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Hollow-pass shape: the GET body is not\n"
            "  field-checked to confirm authorized==1, discovery_enabled==1\n"
            "  were actually persisted."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            result, status = _prose_api("/api/prose/ue-config", "POST", {
                "imsi": imsi,
                "authorized": 1,
                "discovery_enabled": 1,
            })
            if status not in (200, 201):
                self.fail_test(f"UE config failed: {status} {result}")
                return self.result

            log.info("ProSe UE configured: imsi=%s", imsi)

            # Verify
            cfg, c_status = _prose_api(f"/api/prose/ue-config?imsi={imsi}")
            if c_status != 200:
                self.fail_test(f"UE config query failed: {c_status}")
                return self.result

            self.pass_test(imsi=imsi, config=result, verified=cfg)
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ProseDiscovery(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PROSE-003",
        title="ProSe direct discovery announce and monitor",
        spec="TS 23.304 §5.3",
        domain=Domain.PROSE,
        nfs=(NF.UDM, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  TS 23.304 §5.3 defines ProSe Direct Discovery: a UE in\n"
            "  announcer role broadcasts a prose_app_code, monitor-role\n"
            "  UEs listen and report matches. Both roles are gated on\n"
            "  authorized + discovery_enabled state. This test exercises\n"
            "  one announce/monitor pair end-to-end.\n"
            "\n"
            "Procedure (TS 23.304 §5.3 Direct Discovery)\n"
            "  1. require_ue() → ue1; pick ue2 = ue_pool[1] if pool size\n"
            "     >= 2, else reuse ue1 (single-UE fallback).\n"
            "  2. POST /api/prose/ue-config for ue1 (and ue2 if distinct)\n"
            "     with authorized=1, discovery_enabled=1.\n"
            "  3. POST /api/prose/discovery/announce with imsi=ue1.imsi,\n"
            "     app_code=0xABCD1234, service_info='test-service'.\n"
            "  4. Assert announce HTTP in (200, 201).\n"
            "  5. POST /api/prose/discovery/monitor with imsi=ue2.imsi,\n"
            "     app_code=0xABCD1234.\n"
            "  6. Assert monitor HTTP in (200, 201).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — UEs from pool; prose_app_code hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Both announce and monitor POST return 200/201. pass_test\n"
            "  fires with announcer IMSI, monitor IMSI and both payloads.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  announcer (imsi1), monitor (imsi2), announce, discovery.\n"
            "\n"
            "Known constraints\n"
            "  No assertion that monitor body actually surfaces the\n"
            "  announced code (no match check) — a stub that 200s without\n"
            "  cross-referencing announce/monitor would pass."
        ),
    )

    def run(self):
        try:
            ue1 = self.require_ue()
            # Try to get a second UE
            ue2 = None
            if len(self.ue_pool) >= 2:
                ue2 = self.ue_pool[1]

            imsi1 = ue1.imsi
            imsi2 = ue2.imsi if ue2 else imsi1

            # Configure both UEs for ProSe
            _prose_api("/api/prose/ue-config", "POST", {
                "imsi": imsi1, "authorized": 1, "discovery_enabled": 1,
            })
            if imsi2 != imsi1:
                _prose_api("/api/prose/ue-config", "POST", {
                    "imsi": imsi2, "authorized": 1, "discovery_enabled": 1,
                })

            # UE1 announces
            ann_result, ann_status = _prose_api("/api/prose/discovery/announce", "POST", {
                "imsi": imsi1,
                "app_code": "0xABCD1234",
                "service_info": "test-service",
            })
            if ann_status not in (200, 201):
                self.fail_test(f"Announce failed: {ann_status} {ann_result}")
                return self.result
            log.info("UE1 announced: %s", ann_result)

            # UE2 monitors
            mon_result, mon_status = _prose_api("/api/prose/discovery/monitor", "POST", {
                "imsi": imsi2,
                "app_code": "0xABCD1234",
            })
            if mon_status not in (200, 201):
                self.fail_test(f"Monitor failed: {mon_status} {mon_result}")
                return self.result
            log.info("UE2 monitor result: %s", mon_result)

            self.pass_test(
                announcer=imsi1, monitor=imsi2,
                announce=ann_result, discovery=mon_result,
            )
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ProseCommunication(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PROSE-004",
        title="ProSe Direct Communication unicast setup and release",
        spec="TS 23.304 §5.3.4",
        domain=Domain.PROSE,
        nfs=(NF.UDM, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  TS 23.304 §5.3.4 defines ProSe Direct Communication. The §5.1\n"
            "  authorisation gate is independent of discovery_enabled — a\n"
            "  separate communication_enabled flag must be set. This test\n"
            "  pins both the auth provisioning and the unicast-session\n"
            "  setup-then-release lifecycle.\n"
            "\n"
            "Procedure (TS 23.304 §5.3.4 Direct Communication)\n"
            "  1. require_ue() → ue1; ue2 = ue_pool[1] if pool >= 2 else ue1.\n"
            "  2. POST /api/prose/ue-config for ue1 (and ue2 if distinct)\n"
            "     with authorized=1, discovery_enabled=1,\n"
            "     communication_enabled=1.\n"
            "  3. POST /api/prose/communication/setup with source_imsi=ue1,\n"
            "     target_imsi=ue2, session_type='unicast', service='data'.\n"
            "  4. Assert HTTP 200/201.\n"
            "  5. Extract session_id and status from response.\n"
            "  6. Finally clause POSTs /api/prose/communication/{id}/release.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — UEs from pool; session_type='unicast' hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Setup POST 200/201. pass_test fires with source IMSI,\n"
            "  target IMSI, session_id, session status, full session body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  source, target, session_id, status, session.\n"
            "\n"
            "Known constraints\n"
            "  Release path is in finally — not error-checked. Session\n"
            "  status string is reported but not asserted; a backend that\n"
            "  returns status='failed' but HTTP 201 would pass."
        ),
    )

    def run(self):
        session_id = None
        try:
            ue1 = self.require_ue()
            ue2 = None
            if len(self.ue_pool) >= 2:
                ue2 = self.ue_pool[1]

            imsi1 = ue1.imsi
            imsi2 = ue2.imsi if ue2 else imsi1

            # TS 23.304 §5.1 — communication is gated on its own flag,
            # independent of discovery_enabled. Set both for clarity.
            _prose_api("/api/prose/ue-config", "POST", {
                "imsi": imsi1, "authorized": 1, "discovery_enabled": 1,
                "communication_enabled": 1,
            })
            if imsi2 != imsi1:
                _prose_api("/api/prose/ue-config", "POST", {
                    "imsi": imsi2, "authorized": 1, "discovery_enabled": 1,
                    "communication_enabled": 1,
                })

            # Setup communication session
            result, status = _prose_api("/api/prose/communication/setup", "POST", {
                "source_imsi": imsi1,
                "target_imsi": imsi2,
                "session_type": "unicast",
                "service": "data",
            })
            if status not in (200, 201):
                self.fail_test(f"Communication setup failed: {status} {result}")
                return self.result

            session_id = result.get("id") or result.get("session_id")
            sess_status = result.get("status", "unknown")
            log.info("ProSe session: id=%s status=%s", session_id, sess_status)

            self.pass_test(
                source=imsi1, target=imsi2,
                session_id=session_id, status=sess_status, session=result,
            )
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if session_id:
                _prose_api(f"/api/prose/communication/{session_id}/release", "POST")
        return self.result


class ProseRelay(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PROSE-005",
        title="UE-to-Network relay registration and discovery",
        spec="TS 23.304 §5.4",
        domain=Domain.PROSE,
        nfs=(NF.UDM, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  TS 23.304 §5.4 defines the UE-to-Network Relay role: a UE\n"
            "  with cellular access proxies traffic for sidelink-only peers.\n"
            "  Two gates are required: §5.4 capability (relay_capable=1)\n"
            "  AND §5.1 policy (relay_enabled=1). This test pins the relay\n"
            "  register + discover round-trip.\n"
            "\n"
            "Procedure (TS 23.304 §5.4 + §5.1 UE-to-Network Relay)\n"
            "  1. require_ue() to obtain imsi.\n"
            "  2. POST /api/prose/ue-config with authorized=1,\n"
            "     discovery_enabled=1, relay_capable=1, relay_enabled=1.\n"
            "  3. POST /api/prose/relay/register with imsi,\n"
            "     relay_service_code=0x1234.\n"
            "  4. Assert HTTP 200/201.\n"
            "  5. POST /api/prose/relay/discover with imsi (same UE acts\n"
            "     as discoverer — needs authorised flag from §5.1).\n"
            "  6. Assert HTTP 200/201.\n"
            "  7. Parse relays = response.relays / response.items / [].\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — relay_service_code=0x1234 hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Both register and discover POST in (200, 201). pass_test\n"
            "  fires with imsi, register payload and discovered relay list.\n"
            "  An empty relays list is acceptable: §5.4 says a UE excludes\n"
            "  itself from its own discovery view.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, relay (register body), relays (discovery list).\n"
            "\n"
            "Known constraints\n"
            "  Self-exclusion makes len(relays)==0 a legal pass — a buggy\n"
            "  discovery endpoint that returns [] in all cases would pass\n"
            "  this test."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # TS 23.304 §5.1 — relay needs both relay_capable AND
            # relay_enabled (§5.4 cap + §5.1 policy).
            _prose_api("/api/prose/ue-config", "POST", {
                "imsi": imsi, "authorized": 1, "discovery_enabled": 1,
                "relay_capable": 1, "relay_enabled": 1,
            })

            # Register UE as relay
            result, status = _prose_api("/api/prose/relay/register", "POST", {
                "imsi": imsi,
                "relay_service_code": "0x1234",
            })
            if status not in (200, 201):
                self.fail_test(f"Relay registration failed: {status} {result}")
                return self.result
            log.info("Relay registered: %s", result)

            # Discover relays — needs an authorised UE on the request
            # side (TS 23.304 §5.1 discovery gate).
            disc_result, disc_status = _prose_api("/api/prose/relay/discover", "POST", {
                "imsi": imsi,
                "relay_service_code": "0x1234",
            })
            if disc_status not in (200, 201):
                self.fail_test(f"Relay discovery failed: {disc_status} {disc_result}")
                return self.result

            relays = disc_result.get("relays") or disc_result.get("items") or []
            log.info("Discovered %d relay(s)", len(relays))
            # The relay UE excludes itself from its own discovery view
            # (§5.4: a UE doesn't relay to itself). With one relay UE
            # we accept either an empty list (self-exclude) or a
            # populated list (a sibling UE). Both are spec-conformant.

            self.pass_test(imsi=imsi, relay=result, relays=relays)
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ProseAuthorizationGate(TestCase):
    SPEC = TestSpec(
        tc_id="TC-PROSE-006",
        title="Unauthorised UE blocked from ProSe announce and comm setup",
        spec="TS 23.304 §5.1",
        domain=Domain.PROSE,
        nfs=(NF.UDM, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Negative-path test for TS 23.304 §5.1: when a UE is NOT\n"
            "  authorised for ProSe, every discovery/communication\n"
            "  primitive must be rejected at the NEF/AF authorisation gate\n"
            "  with HTTP 403. A regression that lets unauthorised UEs\n"
            "  announce or set up sessions is a serious security hole.\n"
            "\n"
            "Procedure (TS 23.304 §5.1 authorisation gate)\n"
            "  1. require_ue() to obtain imsi.\n"
            "  2. POST /api/prose/ue-config forcing imsi into the FULLY\n"
            "     unauthorised state (authorized=0, discovery_enabled=0,\n"
            "     communication_enabled=0, relay_capable=0,\n"
            "     relay_enabled=0).\n"
            "  3. POST /api/prose/discovery/announce with imsi,\n"
            "     app_code=0xDEADBEEF. Assert status == 403.\n"
            "  4. POST /api/prose/communication/setup with source_imsi=imsi,\n"
            "     target_imsi=imsi, session_type='unicast'.\n"
            "     Assert status == 403.\n"
            "  5. Finally clause RESTORES the UE's ProSe state to all-on\n"
            "     so downstream tests aren't poisoned.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — flag values hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  Both negative POSTs return EXACTLY HTTP 403 (not 401, not\n"
            "  500). pass_test fires with imsi.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi.\n"
            "\n"
            "Known constraints\n"
            "  Strict 403 match — failure mode is well-defined, no hollow-\n"
            "  pass shape. Restoration in finally clause keeps the UE\n"
            "  fully authorised after the test."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # Force unauthorised state.
            _prose_api("/api/prose/ue-config", "POST", {
                "imsi": imsi, "authorized": 0, "discovery_enabled": 0,
                "communication_enabled": 0, "relay_capable": 0, "relay_enabled": 0,
            })

            res, status = _prose_api("/api/prose/discovery/announce", "POST", {
                "imsi": imsi, "app_code": "0xDEADBEEF",
            })
            if status != 403:
                self.fail_test(
                    f"expected 403 for unauthorised announce, got {status} {res}")
                return self.result

            res2, s2 = _prose_api("/api/prose/communication/setup", "POST", {
                "source_imsi": imsi, "target_imsi": imsi,
                "session_type": "unicast",
            })
            if s2 != 403:
                self.fail_test(
                    f"expected 403 for unauthorised comm setup, got {s2} {res2}")
                return self.result

            self.pass_test(imsi=imsi)
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            # Restore authorised state for downstream tests.
            _prose_api("/api/prose/ue-config", "POST", {
                "imsi": imsi, "authorized": 1, "discovery_enabled": 1,
                "communication_enabled": 1, "relay_capable": 1, "relay_enabled": 1,
            })
        return self.result


ALL_PROSE_TCS = [
    ProseRegisterApp, ProseUeConfig, ProseDiscovery,
    ProseCommunication, ProseRelay, ProseAuthorizationGate,
]

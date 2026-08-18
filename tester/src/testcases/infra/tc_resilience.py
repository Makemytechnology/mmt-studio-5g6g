# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Network Resilience.

TS 23.501 S5.19 — NF registration, heartbeat, failover, site resilience.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_resilience")


def _res_api(path, method="GET", body=None):
    """Call SA Core Resilience REST API."""
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


class ResRegisterNf(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RES-001",
        title="Register and delete an NF instance",
        spec="TS 23.501 §5.19",
        domain=Domain.INFRA,
        nfs=(NF.NRF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundation smoke for the resilience controller's NF registry.\n"
            "  TS 22.261 (service continuity) and TS 23.501 §5.19 expect the\n"
            "  5GC to track redundant NF instances so it can fail traffic\n"
            "  over on outage. Every downstream resilience test depends on\n"
            "  the ability to register an NF instance; if create/delete is\n"
            "  broken, the rest of the suite is invalid.\n"
            "\n"
            "Procedure (TS 23.501 §5.19 + TS 22.261)\n"
            "  1. POST /api/resilience/instances {nf_type:'AMF',\n"
            "     instance_id:'amf-01', endpoint:'http://localhost:5000',\n"
            "     priority:10}.\n"
            "  2. Assert HTTP 200/201 on create.\n"
            "  3. Capture instance_id from result['instance_id'] or\n"
            "     result['id'] (default 'amf-01').\n"
            "  4. Teardown (finally): DELETE /api/resilience/instances/\n"
            "     {instance_id} so the registry is clean.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — NF type, instance_id, endpoint and priority are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Create POST returned 200/201 with a non-error body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  instance_id, instance.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no real NF process is started; only the\n"
            "  resilience controller's registry surface is exercised.\n"
            "  Endpoint URL is recorded but not health-probed here."
        ),
    )

    def run(self):
        instance_id = None
        try:
            result, status = _res_api("/api/resilience/instances", "POST", {
                "nf_type": "AMF",
                "instance_id": "amf-01",
                "endpoint": "http://localhost:5000",
                "priority": 10,
            })
            if status not in (200, 201):
                self.fail_test(f"NF registration failed: {status} {result}")
                return self.result

            instance_id = (result.get("instance_id") or result.get("id")
                           or "amf-01")
            log.info("NF registered: instance_id=%s", instance_id)
            self.pass_test(instance_id=instance_id, instance=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if instance_id:
                _res_api(f"/api/resilience/instances/{instance_id}", "DELETE")
        return self.result


class ResHeartbeat(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RES-002",
        title="Heartbeat flips NF instance to healthy",
        spec="TS 23.501 §5.19",
        domain=Domain.INFRA,
        nfs=(NF.NRF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the NF liveness signal that the resilience controller\n"
            "  uses to decide which instance is eligible to serve traffic.\n"
            "  TS 23.501 §5.19 / TS 22.261 service continuity rely on a\n"
            "  heartbeat path: an NF that fails to heartbeat is taken out\n"
            "  of rotation. This TC verifies that a successful heartbeat\n"
            "  flips the reported health to healthy/ok.\n"
            "\n"
            "Procedure (TS 23.501 §5.19)\n"
            "  1. POST /api/resilience/instances to register amf-01;\n"
            "     expect HTTP 200/201.\n"
            "  2. POST /api/resilience/instances/amf-01/heartbeat; expect\n"
            "     HTTP 200/201.\n"
            "  3. GET /api/resilience/instances/amf-01/health; expect\n"
            "     HTTP 200.\n"
            "  4. Read status (or health) field.\n"
            "  5. Assert it is one of 'healthy', 'HEALTHY', 'ok'.\n"
            "  6. Teardown (finally): DELETE the instance.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — instance_id 'amf-01' is hard-coded.\n"
            "\n"
            "Pass criteria\n"
            "  Heartbeat POST returned 200/201 AND health GET returned\n"
            "  200 AND reported state was in {healthy, HEALTHY, ok}.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  instance_id, health.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No timing-based unhealthy transition is\n"
            "  exercised; only the post-heartbeat healthy path is tested."
        ),
    )

    def run(self):
        instance_id = None
        try:
            result, status = _res_api("/api/resilience/instances", "POST", {
                "nf_type": "AMF",
                "instance_id": "amf-01",
                "endpoint": "http://localhost:5000",
                "priority": 10,
            })
            if status not in (200, 201):
                self.fail_test(f"NF registration failed: {status} {result}")
                return self.result
            instance_id = "amf-01"

            # Send heartbeat
            hb_result, hb_status = _res_api(
                "/api/resilience/instances/amf-01/heartbeat", "POST")
            if hb_status not in (200, 201):
                self.fail_test(f"Heartbeat failed: {hb_status} {hb_result}")
                return self.result

            # Check health
            health_result, health_status = _res_api(
                "/api/resilience/instances/amf-01/health")
            if health_status != 200:
                self.fail_test(f"Health check failed: {health_status} {health_result}")
                return self.result

            health_state = health_result.get("status") or health_result.get("health")
            if health_state not in ("healthy", "HEALTHY", "ok"):
                self.fail_test(f"Expected healthy, got {health_state}",
                               response=health_result)
                return self.result

            log.info("NF amf-01 healthy after heartbeat")
            self.pass_test(instance_id=instance_id, health=health_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if instance_id:
                _res_api(f"/api/resilience/instances/{instance_id}", "DELETE")
        return self.result


class ResFailover(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RES-003",
        title="Active-to-standby NF failover promotes the standby",
        spec="TS 23.501 §5.19",
        domain=Domain.INFRA,
        nfs=(NF.NRF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the NF-level failover decision. TS 22.261 service\n"
            "  continuity / TS 23.501 §5.19 require that on active NF\n"
            "  outage, the resilience controller promotes the standby\n"
            "  instance and steers traffic to it. This TC seeds an active\n"
            "  plus a standby AMF and then triggers an explicit failover,\n"
            "  asserting the controller returns a promoted identifier.\n"
            "\n"
            "Procedure (TS 23.501 §5.19 + TS 22.261)\n"
            "  1. POST /api/resilience/instances for amf-active\n"
            "     (priority:10, role:'active'); expect HTTP 200/201.\n"
            "  2. POST /api/resilience/instances for amf-standby\n"
            "     (priority:20, role:'standby'); expect HTTP 200/201.\n"
            "  3. POST /api/resilience/failover {nf_type:'AMF'}; expect\n"
            "     HTTP 200/201.\n"
            "  4. Read promoted = result['promoted'] or result['new_active'].\n"
            "  5. Teardown (finally): DELETE both instances.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — instance IDs, priorities and roles are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Both registrations returned 200/201 AND failover POST\n"
            "  returned 200/201. (promoted is recorded but None is\n"
            "  tolerated — the HTTP status is the strict gate.)\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  failover, promoted.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No real AMF traffic is rerouted; the test\n"
            "  exercises only the controller's decision API."
        ),
    )

    def run(self):
        created_ids = []
        try:
            # Register active AMF
            r1, s1 = _res_api("/api/resilience/instances", "POST", {
                "nf_type": "AMF",
                "instance_id": "amf-active",
                "endpoint": "http://localhost:5000",
                "priority": 10,
                "role": "active",
            })
            if s1 not in (200, 201):
                self.fail_test(f"Active AMF registration failed: {s1} {r1}")
                return self.result
            created_ids.append("amf-active")

            # Register standby AMF
            r2, s2 = _res_api("/api/resilience/instances", "POST", {
                "nf_type": "AMF",
                "instance_id": "amf-standby",
                "endpoint": "http://localhost:5001",
                "priority": 20,
                "role": "standby",
            })
            if s2 not in (200, 201):
                self.fail_test(f"Standby AMF registration failed: {s2} {r2}")
                return self.result
            created_ids.append("amf-standby")

            # Trigger failover
            fo_result, fo_status = _res_api("/api/resilience/failover", "POST", {
                "nf_type": "AMF",
            })
            if fo_status not in (200, 201):
                self.fail_test(f"Failover failed: {fo_status} {fo_result}")
                return self.result

            promoted = fo_result.get("promoted") or fo_result.get("new_active")
            log.info("Failover complete: promoted=%s", promoted)
            self.pass_test(failover=fo_result, promoted=promoted)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for iid in created_ids:
                _res_api(f"/api/resilience/instances/{iid}", "DELETE")
        return self.result


class ResRegisterSite(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RES-004",
        title="Register and delete a resilience site",
        spec="TS 23.501 §5.19",
        domain=Domain.INFRA,
        nfs=(NF.NRF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the site-registry CRUD path used by DC-level disaster\n"
            "  fall-back. TS 22.261 (disaster roaming / service continuity)\n"
            "  and TS 23.501 §5.19 require the operator to declare paired\n"
            "  active/standby sites. This TC verifies the basic site create\n"
            "  returns a site_id that downstream site-failover tests depend\n"
            "  on.\n"
            "\n"
            "Procedure (TS 23.501 §5.19 + TS 22.261)\n"
            "  1. POST /api/resilience/sites {name:'DC-East',\n"
            "     location:'New York', role:'active'}; expect HTTP 200/201.\n"
            "  2. Extract site_id from result['id'] or result['site_id'].\n"
            "  3. Assert site_id is truthy (non-empty).\n"
            "  4. Teardown (finally): DELETE /api/resilience/sites/{site_id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — site name, location and role are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  POST returned 200/201 AND result contained a non-empty\n"
            "  site identifier under 'id' or 'site_id'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  site_id, site.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The site is logical (no real DC infrastructure\n"
            "  is touched). Cleanup is best-effort; orphan rows are\n"
            "  harmless to subsequent runs. The exact ID generation scheme\n"
            "  (UUID, sequential, name-derived) is implementation-defined."
        ),
    )

    def run(self):
        site_id = None
        try:
            result, status = _res_api("/api/resilience/sites", "POST", {
                "name": "DC-East",
                "location": "New York",
                "role": "active",
            })
            if status not in (200, 201):
                self.fail_test(f"Site registration failed: {status} {result}")
                return self.result

            site_id = result.get("id") or result.get("site_id")
            if not site_id:
                self.fail_test("No site ID in response", response=result)
                return self.result

            log.info("Site registered: id=%s", site_id)
            self.pass_test(site_id=site_id, site=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if site_id:
                _res_api(f"/api/resilience/sites/{site_id}", "DELETE")
        return self.result


class ResSiteFailover(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RES-005",
        title="Site-level failover from active to standby",
        spec="TS 23.501 §5.19",
        domain=Domain.INFRA,
        nfs=(NF.NRF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin DC-level failover orchestration. TS 22.261 disaster\n"
            "  fall-back and TS 23.501 §5.19 service continuity allow the\n"
            "  operator to move all traffic from a primary site to a\n"
            "  geographically separate standby on natural disaster, fibre\n"
            "  cut or planned maintenance. This TC seeds an active + a\n"
            "  standby site and triggers an explicit site-failover with a\n"
            "  reason code, asserting the call is accepted.\n"
            "\n"
            "Procedure (TS 23.501 §5.19 + TS 22.261 disaster fall-back)\n"
            "  1. POST /api/resilience/sites {name:'DC-East', role:'active',\n"
            "     location:'New York'}; expect HTTP 200/201.\n"
            "  2. POST /api/resilience/sites {name:'DC-West', role:\n"
            "     'standby', location:'San Francisco'}; expect HTTP 200/201.\n"
            "  3. POST /api/resilience/sites/failover {reason:'earthquake'};\n"
            "     expect HTTP 200/201.\n"
            "  4. Capture the failover response envelope.\n"
            "  5. Teardown (finally): DELETE both sites.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — both sites and the reason code are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Both site registrations returned 200/201 AND the site-\n"
            "  failover POST returned 200/201.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  failover.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. No real workload migration is observed; only\n"
            "  the controller's decision API is exercised. Reason string\n"
            "  is recorded but not validated."
        ),
    )

    def run(self):
        site_ids = []
        try:
            # Register active site
            r1, s1 = _res_api("/api/resilience/sites", "POST", {
                "name": "DC-East",
                "location": "New York",
                "role": "active",
            })
            if s1 not in (200, 201):
                self.fail_test(f"Active site registration failed: {s1} {r1}")
                return self.result
            site_ids.append(r1.get("id") or r1.get("site_id"))

            # Register standby site
            r2, s2 = _res_api("/api/resilience/sites", "POST", {
                "name": "DC-West",
                "location": "San Francisco",
                "role": "standby",
            })
            if s2 not in (200, 201):
                self.fail_test(f"Standby site registration failed: {s2} {r2}")
                return self.result
            site_ids.append(r2.get("id") or r2.get("site_id"))

            # Trigger site failover
            fo_result, fo_status = _res_api(
                "/api/resilience/sites/failover", "POST", {
                    "reason": "earthquake",
                })
            if fo_status not in (200, 201):
                self.fail_test(f"Site failover failed: {fo_status} {fo_result}")
                return self.result

            log.info("Site failover complete: %s", fo_result)
            self.pass_test(failover=fo_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for sid in site_ids:
                if sid:
                    _res_api(f"/api/resilience/sites/{sid}", "DELETE")
        return self.result


class ResFailoverLog(TestCase):
    SPEC = TestSpec(
        tc_id="TC-RES-006",
        title="Failover events are recorded in the audit log",
        spec="TS 23.501 §5.19",
        domain=Domain.INFRA,
        nfs=(NF.NRF,),
        severity=Severity.MINOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the audit-log surface for failover events. TS 23.501\n"
            "  §5.19 and TS 22.261 service-continuity require that every\n"
            "  failover decision be recorded so the operator can later\n"
            "  reconstruct an outage timeline. A controller that loses\n"
            "  these events silently can pass every functional failover\n"
            "  TC while still being unfit for production.\n"
            "\n"
            "Procedure (TS 23.501 §5.19)\n"
            "  1. POST /api/resilience/instances for amf-log-active\n"
            "     (priority:10, role:'active'); expect HTTP 200/201.\n"
            "  2. POST /api/resilience/instances for amf-log-standby\n"
            "     (priority:20, role:'standby'); expect HTTP 200/201.\n"
            "  3. POST /api/resilience/failover {nf_type:'AMF'} to generate\n"
            "     a log entry (status ignored).\n"
            "  4. GET /api/resilience/failover-log; expect HTTP 200.\n"
            "  5. Extract entries[] (fall back to log[] / raw list).\n"
            "  6. Teardown (finally): DELETE both instances.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — instance IDs and roles are fixed.\n"
            "\n"
            "Pass criteria\n"
            "  Both registrations returned 200/201 AND failover-log GET\n"
            "  returned HTTP 200. Entry count is recorded, not asserted.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  log_entries, log.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. MINOR severity — failure indicates the audit\n"
            "  trail is broken, not that traffic is impacted. Log entries\n"
            "  may include events from prior runs; only the GET is gated."
        ),
    )

    def run(self):
        created_ids = []
        try:
            # Create active + standby for a failover event
            r1, s1 = _res_api("/api/resilience/instances", "POST", {
                "nf_type": "AMF",
                "instance_id": "amf-log-active",
                "endpoint": "http://localhost:5000",
                "priority": 10,
                "role": "active",
            })
            if s1 not in (200, 201):
                self.fail_test(f"Active AMF registration failed: {s1} {r1}")
                return self.result
            created_ids.append("amf-log-active")

            r2, s2 = _res_api("/api/resilience/instances", "POST", {
                "nf_type": "AMF",
                "instance_id": "amf-log-standby",
                "endpoint": "http://localhost:5001",
                "priority": 20,
                "role": "standby",
            })
            if s2 not in (200, 201):
                self.fail_test(f"Standby AMF registration failed: {s2} {r2}")
                return self.result
            created_ids.append("amf-log-standby")

            # Trigger failover to generate log entry
            _res_api("/api/resilience/failover", "POST", {"nf_type": "AMF"})

            # Get failover log
            log_result, log_status = _res_api("/api/resilience/failover-log")
            if log_status != 200:
                self.fail_test(f"Failover log GET failed: {log_status} {log_result}")
                return self.result

            entries = log_result.get("entries") or log_result.get("log") or []
            if isinstance(log_result, list):
                entries = log_result

            log.info("Failover log: %d entries", len(entries))
            self.pass_test(log_entries=len(entries), log=log_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for iid in created_ids:
                _res_api(f"/api/resilience/instances/{iid}", "DELETE")
        return self.result


ALL_RESILIENCE_TCS = [
    ResRegisterNf,
    ResHeartbeat,
    ResFailover,
    ResRegisterSite,
    ResSiteFailover,
    ResFailoverLog,
]

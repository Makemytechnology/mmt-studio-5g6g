# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: EAS (Edge Application Server) Discovery.

TS 23.548 — Edge Application Server discovery and selection.
Registry, discovery, DNAI mapping, DNS resolution, discovery logging.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_eas")


def _eas_api(path, method="GET", body=None):
    """Call SA Core EAS REST API."""
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


class EasRegister(TestCase):
    SPEC = TestSpec(
        tc_id="TC-EAS-001",
        title="Register and delete an EAS instance",
        spec="TS 23.548 §6.2",
        domain=Domain.MEC,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Foundational CRUD smoke for the EASDF registry. TS 23.548 §6.2\n"
            "  mandates an Edge Application Server registration store that\n"
            "  3rd-party AFs use to advertise EAS instances. If POST + GET +\n"
            "  DELETE don't round-trip cleanly, no downstream discovery,\n"
            "  DNAI mapping or DNS resolution flow can possibly work.\n"
            "\n"
            "Procedure (TS 23.548 §6.2 EAS provisioning)\n"
            "  1. POST /api/eas/registry with app_id=eas-test-app-001,\n"
            "     endpoint_url, dnai=dnai-east-01, lat/lon.\n"
            "  2. Assert HTTP 200/201 and extract eas_id from response.\n"
            "  3. GET /api/eas/registry to list all entries.\n"
            "  4. Assert listing returns HTTP 200.\n"
            "  5. Finally clause DELETEs /api/eas/registry/{eas_id} to\n"
            "     leave the registry clean for the next test.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — fixed app_id, endpoint, dnai, lat/lon hardcoded in body.\n"
            "\n"
            "Pass criteria\n"
            "  POST status in (200, 201) AND registry GET returns 200.\n"
            "  pass_test fires with eas_id, eas row, full registry snapshot.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  eas_id, eas (full POST response body), registry (GET listing).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — runs against an empty registry. Test does NOT\n"
            "  assert the new row is in the GET response — only that the GET\n"
            "  itself returns 200, so a buggy DB layer that silently drops\n"
            "  writes would still pass this hollow listing check."
        ),
    )

    def run(self):
        eas_id = None
        try:
            result, status = _eas_api("/api/eas/registry", "POST", {
                "app_id": "eas-test-app-001",
                "name": "Test Edge App",
                "endpoint_url": "http://10.0.1.10:8080/app",
                "dnai": "dnai-east-01",
                "latitude": 37.7749,
                "longitude": -122.4194,
            })
            if status not in (200, 201):
                self.fail_test(f"EAS registration failed: {status} {result}")
                return self.result

            eas_id = result.get("id") or result.get("eas_id")
            log.info("EAS registered: id=%s", eas_id)

            # Verify by listing
            registry, r_status = _eas_api("/api/eas/registry")
            if r_status != 200:
                self.fail_test(f"Registry list failed: {r_status}")
                return self.result

            self.pass_test(eas_id=eas_id, eas=result, registry=registry)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if eas_id:
                _eas_api(f"/api/eas/registry/{eas_id}", "DELETE")
        return self.result


class EasDiscover(TestCase):
    SPEC = TestSpec(
        tc_id="TC-EAS-002",
        title="Discover EAS instances with location-based ranking",
        spec="TS 23.548 §6.3",
        domain=Domain.MEC,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Validates the EASDF discovery contract (TS 23.548 §6.3): given\n"
            "  a UE IMSI and an app_id, the EASDF must return a candidate list\n"
            "  of EAS instances ranked by proximity / DNAI policy. Two\n"
            "  geographically distinct EAS rows are seeded so the ranker has\n"
            "  something to order.\n"
            "\n"
            "Procedure (TS 23.548 §6.3 EAS discovery)\n"
            "  1. require_ue() — pull a UE so we have a real IMSI for the\n"
            "     discovery request body.\n"
            "  2. POST /api/eas/registry twice with app_id=eas-discover-app,\n"
            "     once at lat=37.7749/lon=-122.4194/dnai-west-01, once at\n"
            "     lat=40.7128/lon=-74.0060/dnai-east-01.\n"
            "  3. POST /api/eas/discover with {app_id, imsi}.\n"
            "  4. Parse results from response.results or response.items.\n"
            "  5. Finally clause DELETEs both EAS rows.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — IMSI from require_ue(), locations and app_id hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  All two POST /registry calls return 200/201 AND POST /discover\n"
            "  returns 200/201. pass_test fires with imsi, eas_count, full\n"
            "  discovery payload.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, eas_count (len of results list), discovery (full response).\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: test does NOT assert eas_count >= 1 nor that\n"
            "  the ranking actually puts the closer DNAI first. An EASDF stub\n"
            "  that always returns an empty list would still pass."
        ),
    )

    def run(self):
        eas_ids = []
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # Register 2 EAS instances
            for i, (lat, lon, dnai) in enumerate([
                (37.7749, -122.4194, "dnai-west-01"),
                (40.7128, -74.0060, "dnai-east-01"),
            ]):
                result, status = _eas_api("/api/eas/registry", "POST", {
                    "app_id": "eas-discover-app",
                    "name": f"Edge App {i + 1}",
                    "endpoint_url": f"http://10.0.{i + 1}.10:8080/app",
                    "dnai": dnai,
                    "latitude": lat,
                    "longitude": lon,
                })
                if status not in (200, 201):
                    self.fail_test(f"EAS {i + 1} registration failed: {status} {result}")
                    return self.result
                eid = result.get("id") or result.get("eas_id")
                eas_ids.append(eid)

            # Discover
            disc_result, disc_status = _eas_api("/api/eas/discover", "POST", {
                "app_id": "eas-discover-app",
                "imsi": imsi,
            })
            if disc_status not in (200, 201):
                self.fail_test(f"Discovery failed: {disc_status} {disc_result}")
                return self.result

            results_list = (disc_result.get("results") or
                            disc_result.get("items") or [])
            log.info("Discovered %d EAS instances", len(results_list))

            self.pass_test(
                imsi=imsi, eas_count=len(results_list),
                discovery=disc_result,
            )
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for eid in eas_ids:
                if eid:
                    _eas_api(f"/api/eas/registry/{eid}", "DELETE")
        return self.result


class EasDnaiMap(TestCase):
    SPEC = TestSpec(
        tc_id="TC-EAS-003",
        title="DNAI mapping create, verify, delete",
        spec="TS 23.548 §6.2.3",
        domain=Domain.MEC,
        nfs=(NF.SMF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.548 §6.2.3 defines the DNAI (Data Network Access\n"
            "  Identifier) as the SMF/PCF anchor used to steer PDU sessions\n"
            "  toward a specific local data network. This test pins the\n"
            "  CRUD surface that the EASDF / SMF read at PDU establish to\n"
            "  decide which UPF/N6 break-out to use.\n"
            "\n"
            "Procedure (TS 23.548 §6.2.3 DNAI provisioning)\n"
            "  1. POST /api/eas/dnai with dnai=dnai-test-001, description,\n"
            "     location_hint=US-East-1.\n"
            "  2. Extract dnai_id from response (falls back to the literal\n"
            "     string identifier).\n"
            "  3. GET /api/eas/dnai to list all DNAIs.\n"
            "  4. Assert GET status == 200.\n"
            "  5. Finally clause DELETEs /api/eas/dnai/{dnai_id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — dnai id, description and location_hint hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST status in (200, 201) AND GET status == 200. pass_test\n"
            "  fires with dnai_id, dnai row, full dnais listing.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  dnai_id, dnai (POST body echo), dnais (GET listing).\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: the GET listing is not searched for\n"
            "  dnai_id, so a backend that drops the write but still serves\n"
            "  GET 200 would pass this test."
        ),
    )

    def run(self):
        dnai_id = None
        try:
            result, status = _eas_api("/api/eas/dnai", "POST", {
                "dnai": "dnai-test-001",
                "description": "Test DNAI for east region",
                "location_hint": "US-East-1",
            })
            if status not in (200, 201):
                self.fail_test(f"DNAI mapping failed: {status} {result}")
                return self.result

            dnai_id = result.get("id") or result.get("dnai_id") or "dnai-test-001"
            log.info("DNAI mapped: %s", dnai_id)

            # Verify
            dnais, d_status = _eas_api("/api/eas/dnai")
            if d_status != 200:
                self.fail_test(f"DNAI list failed: {d_status}")
                return self.result

            self.pass_test(dnai_id=dnai_id, dnai=result, dnais=dnais)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if dnai_id:
                _eas_api(f"/api/eas/dnai/{dnai_id}", "DELETE")
        return self.result


class EasDns(TestCase):
    SPEC = TestSpec(
        tc_id="TC-EAS-004",
        title="EAS DNS registration and resolution",
        spec="TS 23.548 §6.2.3.2.2",
        domain=Domain.MEC,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  TS 23.548 §6.2.3.2.2 defines the EASDF DNS sub-function: an\n"
            "  FQDN must resolve to the endpoint URL of the matching EAS so\n"
            "  the UE's edge-app traffic is steered to the local instance.\n"
            "  This test exercises the FQDN-attach + resolve round-trip.\n"
            "\n"
            "Procedure (TS 23.548 §6.2.3.2.2 EASDF DNS handling)\n"
            "  1. POST /api/eas/registry with app_id=eas-dns-app, endpoint\n"
            "     http://10.0.5.10:8080/app, dnai=dnai-dns-01.\n"
            "  2. Extract eas_id from the response.\n"
            "  3. POST /api/eas/dns binding fqdn=testapp.edge.local → eas_id.\n"
            "  4. POST /api/eas/dns/resolve with fqdn=testapp.edge.local.\n"
            "  5. Read endpoint_url (or endpoint) from the resolve body.\n"
            "  6. Finally clause DELETEs the DNS entry and the EAS row.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — app_id, endpoint, dnai and FQDN hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST /registry, POST /dns, POST /dns/resolve all return\n"
            "  200/201. pass_test fires with eas_id, fqdn, dns row, full\n"
            "  resolve response.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  eas_id, fqdn, dns (POST body), resolved (resolve response).\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: the resolved endpoint_url is captured but\n"
            "  never compared back to the registered endpoint. A resolver\n"
            "  that returns garbage as long as it's HTTP 200 would pass."
        ),
    )

    def run(self):
        eas_id = None
        dns_id = None
        try:
            # Register EAS
            eas_result, eas_status = _eas_api("/api/eas/registry", "POST", {
                "app_id": "eas-dns-app",
                "name": "DNS Test Edge App",
                "endpoint_url": "http://10.0.5.10:8080/app",
                "dnai": "dnai-dns-01",
                "latitude": 37.7749,
                "longitude": -122.4194,
            })
            if eas_status not in (200, 201):
                self.fail_test(f"EAS registration failed: {eas_status} {eas_result}")
                return self.result

            eas_id = eas_result.get("id") or eas_result.get("eas_id")

            # Register DNS entry
            dns_result, dns_status = _eas_api("/api/eas/dns", "POST", {
                "eas_id": eas_id,
                "fqdn": "testapp.edge.local",
            })
            if dns_status not in (200, 201):
                self.fail_test(f"DNS registration failed: {dns_status} {dns_result}")
                return self.result

            dns_id = dns_result.get("id") or dns_result.get("dns_id")
            log.info("DNS entry created: fqdn=testapp.edge.local eas_id=%s", eas_id)

            # Resolve
            resolve_result, resolve_status = _eas_api("/api/eas/dns/resolve", "POST", {
                "fqdn": "testapp.edge.local",
            })
            if resolve_status != 200:
                self.fail_test(f"DNS resolve failed: {resolve_status} {resolve_result}")
                return self.result

            resolved_url = resolve_result.get("endpoint_url") or resolve_result.get("endpoint")
            log.info("DNS resolved: testapp.edge.local -> %s", resolved_url)

            self.pass_test(
                eas_id=eas_id, fqdn="testapp.edge.local",
                dns=dns_result, resolved=resolve_result,
            )
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if dns_id:
                _eas_api(f"/api/eas/dns/{dns_id}", "DELETE")
            if eas_id:
                _eas_api(f"/api/eas/registry/{eas_id}", "DELETE")
        return self.result


class EasDiscoveryLog(TestCase):
    SPEC = TestSpec(
        tc_id="TC-EAS-005",
        title="EAS discovery events are written to audit log",
        spec="TS 23.548 §6.3",
        domain=Domain.MEC,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  TS 23.548 §6.3 leaves auditing implementation-defined but\n"
            "  this product ships an EASDF discovery-log so operators can\n"
            "  observe who discovered which EAS and when. This test pins\n"
            "  that audit trail: a discovery call must produce at least\n"
            "  one observable log entry.\n"
            "\n"
            "Procedure (TS 23.548 §6.3 + EASDF audit trail)\n"
            "  1. require_ue() to obtain a real IMSI.\n"
            "  2. POST /api/eas/registry with app_id=eas-log-app at\n"
            "     dnai-log-01 to seed something discoverable.\n"
            "  3. POST /api/eas/discover with {app_id, imsi} to generate\n"
            "     a discovery event (response ignored).\n"
            "  4. GET /api/eas/discovery-log to read back the audit trail.\n"
            "  5. Parse entries from response.entries / .items / .logs.\n"
            "  6. Finally clause DELETEs the seeded EAS row.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — app_id, endpoint, dnai, lat/lon hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  GET /discovery-log returns 200. pass_test fires with imsi,\n"
            "  log_count, full discovery_log payload.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, log_count (len of entries list), discovery_log payload.\n"
            "\n"
            "Known constraints\n"
            "  Hollow-pass shape: log_count is computed but not asserted\n"
            "  >= 1, so an EASDF that silently drops audit rows but returns\n"
            "  HTTP 200 on the log query passes."
        ),
    )

    def run(self):
        eas_id = None
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # Register an EAS
            eas_result, _ = _eas_api("/api/eas/registry", "POST", {
                "app_id": "eas-log-app",
                "name": "Log Test Edge App",
                "endpoint_url": "http://10.0.6.10:8080/app",
                "dnai": "dnai-log-01",
                "latitude": 37.7749,
                "longitude": -122.4194,
            })
            eas_id = eas_result.get("id") or eas_result.get("eas_id")

            # Run discovery to generate log entry
            _eas_api("/api/eas/discover", "POST", {
                "app_id": "eas-log-app",
                "imsi": imsi,
            })

            # Check discovery log
            result, status = _eas_api("/api/eas/discovery-log")
            if status != 200:
                self.fail_test(f"Discovery log query failed: {status} {result}")
                return self.result

            entries = result.get("entries") or result.get("items") or result.get("logs") or []
            log.info("Discovery log has %d entries", len(entries))

            self.pass_test(
                imsi=imsi, log_count=len(entries), discovery_log=result,
            )
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if eas_id:
                _eas_api(f"/api/eas/registry/{eas_id}", "DELETE")
        return self.result


class EasUpdate(TestCase):
    SPEC = TestSpec(
        tc_id="TC-EAS-006",
        title="Mid-life update of EAS capacity and status",
        spec="TS 23.558 §8.4.3.2.3",
        domain=Domain.MEC,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.558 §8.4.3.2.3 defines Eees_EASRegistration_Update so an\n"
            "  AF can declare mid-life capacity / operational-status changes\n"
            "  on an already-registered EAS (e.g. scaling up or marking it for\n"
            "  maintenance). This test pins that PUT contract.\n"
            "\n"
            "Procedure (TS 23.558 §8.4.3.4.3 EAS update)\n"
            "  1. POST /api/eas/registry with app_id=eas-tc-eas-006,\n"
            "     capacity=100, endpoint http://10.99.6.10:8080.\n"
            "  2. Extract eas_id.\n"
            "  3. PUT /api/eas/registry/{eas_id} with capacity=250 and\n"
            "     status=maintenance.\n"
            "  4. Assert response.capacity == 250 (int-coerced).\n"
            "  5. Assert response.status == 'maintenance'.\n"
            "  6. Finally clause DELETEs the row.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — initial capacity=100, target capacity=250 and the new\n"
            "  status='maintenance' are hardcoded.\n"
            "\n"
            "Pass criteria\n"
            "  POST 200/201 AND PUT 200 AND capacity field == 250 AND status\n"
            "  field == 'maintenance' on the PUT response.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  eas_id, capacity (post-update), status (post-update).\n"
            "\n"
            "Known constraints\n"
            "  Update is verified inline on the PUT response only — no\n"
            "  follow-up GET, so a route that echoes the request body without\n"
            "  persisting it would still pass."
        ),
    )

    def run(self):
        eas_id = None
        try:
            row, st = _eas_api("/api/eas/registry", "POST", {
                "app_id": "eas-tc-eas-006",
                "name": "Update Test", "endpoint_url": "http://10.99.6.10:8080",
                "dnai": "dnai-tc-eas-006",
                "capacity": 100,
            })
            if st not in (200, 201):
                self.fail_test(f"create failed: {st} {row}")
                return self.result
            eas_id = row.get("id") or row.get("eas_id")
            updated, st = _eas_api(f"/api/eas/registry/{eas_id}", "PUT", {
                "capacity": 250, "status": "maintenance",
            })
            if st != 200:
                self.fail_test(f"update failed: {st} {updated}")
                return self.result
            if int(updated.get("capacity") or 0) != 250:
                self.fail_test(f"capacity not updated: {updated}")
                return self.result
            if updated.get("status") != "maintenance":
                self.fail_test(f"status not updated: {updated}")
                return self.result
            self.pass_test(eas_id=eas_id, capacity=updated.get("capacity"),
                           status=updated.get("status"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if eas_id:
                _eas_api(f"/api/eas/registry/{eas_id}", "DELETE")
        return self.result


class EasStatusEnvelope(TestCase):
    SPEC = TestSpec(
        tc_id="TC-EAS-007",
        title="EAS /status returns a {count, items} OAM envelope",
        spec="TS 23.548 §6.2",
        domain=Domain.MEC,
        nfs=(NF.NEF,),
        severity=Severity.MINOR,
        tags=("smoke", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  The OAM dashboard binds to a contract that /api/eas/status\n"
            "  returns a `{count, items}` envelope (count = number of EAS\n"
            "  rows, items = the list). A regression that flattens the\n"
            "  envelope or renames keys breaks the panel; this test is a\n"
            "  cheap canary against that.\n"
            "\n"
            "Procedure (TS 23.548 §6.2 OAM status envelope)\n"
            "  1. GET /api/eas/status (no body, read-only).\n"
            "  2. Assert HTTP 200.\n"
            "  3. Assert 'count' key present in body.\n"
            "  4. Assert 'items' key present in body.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read-only check.\n"
            "\n"
            "Pass criteria\n"
            "  status == 200 AND 'count' in body AND 'items' in body.\n"
            "  pass_test fires with count value.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  count.\n"
            "\n"
            "Known constraints\n"
            "  Pure shape contract — no semantic check that count equals\n"
            "  len(items) or that items elements have the expected fields."
        ),
    )

    def run(self):
        body, st = _eas_api("/api/eas/status")
        if st != 200:
            self.fail_test(f"status failed: {st} {body}")
            return self.result
        if "count" not in body or "items" not in body:
            self.fail_test(f"envelope missing keys: {body}")
            return self.result
        self.pass_test(count=body.get("count"))
        return self.result


ALL_EAS_TCS = [
    EasRegister, EasDiscover, EasDnaiMap, EasDns, EasDiscoveryLog,
    EasUpdate, EasStatusEnvelope,
]

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Network Slice as a Service (NSaaS).

TS 28.531 — Management and orchestration of network slicing.
Tenant lifecycle, slice provisioning, activation, decommissioning, SLA.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_nsaas")


def _nsaas_api(path, method="GET", body=None):
    """Call SA Core NSaaS REST API."""
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


class NsaasSeedTemplates(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSAAS-001",
        title="Seed NSaaS slice templates and verify the catalogue",
        spec="TS 28.531 §8.2",
        domain=Domain.PROVISIONING,
        nfs=(NF.NSSF, NF.NRF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Foundational sanity for NSaaS slice-template catalogue\n"
                "  (TS 28.531 §8.2 — Management of network slice instances). The\n"
                "  seed routine bootstraps the operator-canonical slice templates\n"
                "  (eMBB/URLLC/MIoT etc.) so per-tenant provisioning has something\n"
                "  to clone from. Without a populated catalogue every downstream\n"
                "  NSaaS test fails the lookup step.\n"
                "\n"
                "Procedure (TS 28.531 §8.2 + §6.4)\n"
                "  1. POST /api/nsaas/templates/seed — require status 200/201.\n"
                "  2. GET /api/nsaas/templates — require status 200.\n"
                "  3. Accept templates/items envelopes; require items non-empty.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — passive seed + readback)\n"
                "\n"
                "Pass criteria\n"
                "  Seed returns 2xx AND list is non-empty after seed.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  seeded, template_count, templates.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Seed is expected to be idempotent — retries don't\n"
                "  multiply rows.\n"
                "  Template authoring (operator GUI / manifest tooling) is upstream\n"
                "  and out of scope here.\n"
                "  Sub-resources of each template (NSSAI, AMBR, ...) are exercised\n"
                "  in TC-NSAAS-010.\n"
                "  Stage-2 inventory model lives in TS 28.532."
            ),
    )

    def run(self):
        try:
            # Seed templates
            result, status = _nsaas_api("/api/nsaas/templates/seed", "POST")
            if status not in (200, 201):
                self.fail_test(f"Template seed failed: {status} {result}")
                return self.result
            log.info("Templates seeded: %s", result)

            # Verify templates exist
            templates, t_status = _nsaas_api("/api/nsaas/templates")
            if t_status != 200:
                self.fail_test(f"Template list failed: {t_status}")
                return self.result

            items = templates.get("templates") or templates.get("items") or []
            if not items:
                self.fail_test("No templates found after seeding")
                return self.result

            log.info("Found %d templates after seeding", len(items))
            self.pass_test(seeded=result, template_count=len(items), templates=items)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NsaasCreateTenant(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSAAS-002",
        title="Create and delete an NSaaS tenant",
        spec="TS 28.531 §6.4",
        domain=Domain.PROVISIONING,
        nfs=(NF.NSSF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Tenant lifecycle CRUD (TS 28.531 §6.4 — Customer/CSP/CSC roles).\n"
                "  Every NSaaS slice is owned by a tenant; the test pins that a\n"
                "  fresh tenant POST yields an identifier and is then cleaned up\n"
                "  via DELETE so subsequent tests start from a known state.\n"
                "\n"
                "Procedure (TS 28.531 §6.4)\n"
                "  1. POST /api/nsaas/tenants {name='test-tenant-002',\n"
                "     contact_email='test@example.com'}.\n"
                "  2. Require status 200/201.\n"
                "  3. Read tenant_id from result.id or result.tenant_id.\n"
                "  4. Require tenant_id is non-empty.\n"
                "  5. finally: DELETE /api/nsaas/tenants/{tenant_id}.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — tenant name and contact_email hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Create returns a non-empty id; teardown DELETE is best-effort.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  tenant_id, tenant.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY — no slices associated yet.\n"
                "  Tenant-level RBAC tokens are not exercised here — the test runs\n"
                "  as the OAM superuser.\n"
                "  Re-creating a tenant with the same name yields a fresh id (no\n"
                "  upsert).\n"
                "  Deletion is hard-delete; soft-delete semantics live elsewhere."
            ),
    )

    def run(self):
        tenant_id = None
        try:
            result, status = _nsaas_api("/api/nsaas/tenants", "POST", {
                "name": "test-tenant-002",
                "contact_email": "test@example.com",
            })
            if status not in (200, 201):
                self.fail_test(f"Tenant creation failed: {status} {result}")
                return self.result

            tenant_id = result.get("id") or result.get("tenant_id")
            log.info("Tenant created: id=%s", tenant_id)

            if not tenant_id:
                self.fail_test("Tenant created but no id returned", response=result)
                return self.result

            self.pass_test(tenant_id=tenant_id, tenant=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if tenant_id:
                _nsaas_api(f"/api/nsaas/tenants/{tenant_id}", "DELETE")
        return self.result


class NsaasProvisionSlice(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSAAS-003",
        title="Provision a network slice for a tenant",
        spec="TS 28.531 §8.2",
        domain=Domain.PROVISIONING,
        nfs=(NF.NSSF, NF.NRF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Per-tenant slice provisioning (TS 28.531 §8.2 — Network Slice\n"
                "  Instance allocation). Cloning a template into a tenant-scoped\n"
                "  slice is the central NSaaS lifecycle operation; this test pins\n"
                "  the happy path: seed → template lookup → tenant create →\n"
                "  slice POST → status=='provisioned'.\n"
                "\n"
                "Procedure (TS 28.531 §8.2 + §6.4)\n"
                "  1. POST /api/nsaas/templates/seed (idempotent).\n"
                "  2. GET /api/nsaas/templates; take items[0]; read template_id.\n"
                "  3. POST /api/nsaas/tenants {name='test-tenant-003',\n"
                "     contact_email='provision@example.com'}; read tenant_id.\n"
                "  4. POST /api/nsaas/slices {tenant_id, template_id,\n"
                "     name='test-slice-003'}.\n"
                "  5. Require status 200/201 AND result.status == 'provisioned'.\n"
                "  6. finally: DELETE slice then DELETE tenant.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — names and emails hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Slice POST returns 2xx AND status field equals 'provisioned'.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  slice_id, status, slice.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Activation is the next state and lives in\n"
                "  TC-NSAAS-004.\n"
                "  Subordinate fields (priority, max-subs) are accepted but not\n"
                "  asserted on the response shape by this test.\n"
                "  Concurrent slice creation per tenant is allowed but exercised by\n"
                "  stress tests, not here."
            ),
    )

    def run(self):
        tenant_id = None
        slice_id = None
        try:
            # Seed templates
            _nsaas_api("/api/nsaas/templates/seed", "POST")

            # Get first template
            templates, _ = _nsaas_api("/api/nsaas/templates")
            items = templates.get("templates") or templates.get("items") or []
            if not items:
                self.fail_test("No templates available")
                return self.result
            template_id = items[0].get("id") or items[0].get("template_id")

            # Create tenant
            tenant, _ = _nsaas_api("/api/nsaas/tenants", "POST", {
                "name": "test-tenant-003",
                "contact_email": "provision@example.com",
            })
            tenant_id = tenant.get("id") or tenant.get("tenant_id")

            # Provision slice
            result, status = _nsaas_api("/api/nsaas/slices", "POST", {
                "tenant_id": tenant_id,
                "template_id": template_id,
                "name": "test-slice-003",
            })
            if status not in (200, 201):
                self.fail_test(f"Slice provisioning failed: {status} {result}")
                return self.result

            slice_id = result.get("id") or result.get("slice_id")
            slice_status = result.get("status", "unknown")
            log.info("Slice provisioned: id=%s status=%s", slice_id, slice_status)

            if slice_status != "provisioned":
                self.fail_test(f"Expected status=provisioned, got {slice_status}",
                               slice=result)
                return self.result

            self.pass_test(slice_id=slice_id, status=slice_status, slice=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if slice_id:
                _nsaas_api(f"/api/nsaas/slices/{slice_id}", "DELETE")
            if tenant_id:
                _nsaas_api(f"/api/nsaas/tenants/{tenant_id}", "DELETE")
        return self.result


class NsaasActivateSlice(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSAAS-004",
        title="Activate a provisioned NSaaS slice",
        spec="TS 28.531 §8.3",
        domain=Domain.PROVISIONING,
        nfs=(NF.NSSF, NF.NRF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Forward edge of the NSaaS state machine (TS 28.531 §8.3 —\n"
                "  Activation of network slice instance). After provisioning, the\n"
                "  slice MUST be activatable so radio/core resources are bound;\n"
                "  the AS-side state field has to transition from 'provisioned' to\n"
                "  'active' as a result.\n"
                "\n"
                "Procedure (TS 28.531 §8.3)\n"
                "  1. POST /api/nsaas/templates/seed; GET templates; pick first.\n"
                "  2. POST /api/nsaas/tenants {name='test-tenant-004'}; read\n"
                "     tenant_id.\n"
                "  3. POST /api/nsaas/slices {tenant_id, template_id,\n"
                "     name='test-slice-004'}; read slice_id.\n"
                "  4. POST /api/nsaas/slices/{slice_id}/activate.\n"
                "  5. Require status 200/201 AND result.status == 'active'.\n"
                "  6. finally: DELETE slice, DELETE tenant.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Activate POST returns 2xx and the slice state is 'active'.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  slice_id, status, slice.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Decommission is the next edge — see TC-NSAAS-005.\n"
                "  Activation may trigger NSSF/NRF side-effects (slice registration\n"
                "  in the NRF); these are validated separately.\n"
                "  Re-activation of an already-active slice is a no-op (not asserted)."
            ),
    )

    def run(self):
        tenant_id = None
        slice_id = None
        try:
            _nsaas_api("/api/nsaas/templates/seed", "POST")
            templates, _ = _nsaas_api("/api/nsaas/templates")
            items = templates.get("templates") or templates.get("items") or []
            if not items:
                self.fail_test("No templates available")
                return self.result
            template_id = items[0].get("id") or items[0].get("template_id")

            tenant, _ = _nsaas_api("/api/nsaas/tenants", "POST", {
                "name": "test-tenant-004",
                "contact_email": "activate@example.com",
            })
            tenant_id = tenant.get("id") or tenant.get("tenant_id")

            sl, _ = _nsaas_api("/api/nsaas/slices", "POST", {
                "tenant_id": tenant_id,
                "template_id": template_id,
                "name": "test-slice-004",
            })
            slice_id = sl.get("id") or sl.get("slice_id")

            # Activate
            result, status = _nsaas_api(f"/api/nsaas/slices/{slice_id}/activate", "POST")
            if status not in (200, 201):
                self.fail_test(f"Slice activation failed: {status} {result}")
                return self.result

            act_status = result.get("status", "unknown")
            log.info("Slice activated: id=%s status=%s", slice_id, act_status)

            if act_status != "active":
                self.fail_test(f"Expected status=active, got {act_status}", slice=result)
                return self.result

            self.pass_test(slice_id=slice_id, status=act_status, slice=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if slice_id:
                _nsaas_api(f"/api/nsaas/slices/{slice_id}", "DELETE")
            if tenant_id:
                _nsaas_api(f"/api/nsaas/tenants/{tenant_id}", "DELETE")
        return self.result


class NsaasDecommission(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSAAS-005",
        title="Decommission an active NSaaS slice",
        spec="TS 28.531 §8.4",
        domain=Domain.PROVISIONING,
        nfs=(NF.NSSF, NF.NRF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  Tear-down edge of the NSaaS state machine (TS 28.531 §8.4 —\n"
            "  Deactivation / Termination of network slice instance). An\n"
            "  active slice MUST be decommissionable so its core/RAN footprint\n"
            "  is released; the state field has to transition to\n"
            "  'decommissioned'.\n"
            "\n"
            "Procedure (TS 28.531 §8.4)\n"
            "  1. Seed templates and pick first template_id.\n"
            "  2. POST /api/nsaas/tenants {name='test-tenant-005'};\n"
            "     read tenant_id.\n"
            "  3. POST /api/nsaas/slices to provision; read slice_id.\n"
            "  4. POST /api/nsaas/slices/{slice_id}/activate.\n"
            "  5. POST /api/nsaas/slices/{slice_id}/decommission.\n"
            "  6. Require status 200/201 AND result.status ==\n"
            "     'decommissioned'.\n"
            "  7. finally: DELETE slice, DELETE tenant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixtures hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Decommission returns 2xx and the slice state ==\n"
            "  'decommissioned'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  slice_id, status, slice.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Pre-decommission activation is fire-and-forget —\n"
            "  the test does not assert intermediate 'active' state here."
        ),
    )

    def run(self):
        tenant_id = None
        slice_id = None
        try:
            _nsaas_api("/api/nsaas/templates/seed", "POST")
            templates, _ = _nsaas_api("/api/nsaas/templates")
            items = templates.get("templates") or templates.get("items") or []
            if not items:
                self.fail_test("No templates available")
                return self.result
            template_id = items[0].get("id") or items[0].get("template_id")

            tenant, _ = _nsaas_api("/api/nsaas/tenants", "POST", {
                "name": "test-tenant-005",
                "contact_email": "decom@example.com",
            })
            tenant_id = tenant.get("id") or tenant.get("tenant_id")

            sl, _ = _nsaas_api("/api/nsaas/slices", "POST", {
                "tenant_id": tenant_id,
                "template_id": template_id,
                "name": "test-slice-005",
            })
            slice_id = sl.get("id") or sl.get("slice_id")

            # Activate then decommission
            _nsaas_api(f"/api/nsaas/slices/{slice_id}/activate", "POST")

            result, status = _nsaas_api(f"/api/nsaas/slices/{slice_id}/decommission", "POST")
            if status not in (200, 201):
                self.fail_test(f"Decommission failed: {status} {result}")
                return self.result

            dec_status = result.get("status", "unknown")
            log.info("Slice decommissioned: id=%s status=%s", slice_id, dec_status)

            if dec_status != "decommissioned":
                self.fail_test(f"Expected status=decommissioned, got {dec_status}",
                               slice=result)
                return self.result

            self.pass_test(slice_id=slice_id, status=dec_status, slice=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if slice_id:
                _nsaas_api(f"/api/nsaas/slices/{slice_id}", "DELETE")
            if tenant_id:
                _nsaas_api(f"/api/nsaas/tenants/{tenant_id}", "DELETE")
        return self.result


class NsaasSla(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSAAS-006",
        title="Query SLA metrics for an active NSaaS slice",
        spec="TS 28.531 §9",
        domain=Domain.PROVISIONING,
        nfs=(NF.NSSF, NF.NWDAF),
        severity=Severity.MINOR,
        tags=("regression",),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  SLA telemetry surface for a live slice (TS 28.531 §9 —\n"
                "  Performance assurance / TS 28.554). The dashboard polls\n"
                "  /slices/{id}/sla to render per-tenant KPI tiles; the endpoint\n"
                "  MUST return 200 with a structured payload on an active slice.\n"
                "\n"
                "Procedure (TS 28.531 §9 + §8.3)\n"
                "  1. Seed templates; pick first template_id.\n"
                "  2. POST /api/nsaas/tenants {name='test-tenant-006'}; read\n"
                "     tenant_id.\n"
                "  3. POST /api/nsaas/slices to provision; read slice_id.\n"
                "  4. POST /api/nsaas/slices/{slice_id}/activate.\n"
                "  5. GET /api/nsaas/slices/{slice_id}/sla.\n"
                "  6. Require status == 200.\n"
                "  7. finally: DELETE slice, DELETE tenant.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  SLA GET returns HTTP 200 (payload shape is logged, not strictly\n"
                "  asserted — dashboard tiles tolerate empty metrics).\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  slice_id, sla.\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. NWDAF feed of SLA values is asynchronous; numeric\n"
                "  KPI values are not asserted here.\n"
                "  Payload includes per-KPI buckets (throughput / latency / loss);\n"
                "  this test pins the envelope, not the per-KPI math.\n"
                "  Empty-metrics response is tolerated — the dashboard handles it."
            ),
    )

    def run(self):
        tenant_id = None
        slice_id = None
        try:
            _nsaas_api("/api/nsaas/templates/seed", "POST")
            templates, _ = _nsaas_api("/api/nsaas/templates")
            items = templates.get("templates") or templates.get("items") or []
            if not items:
                self.fail_test("No templates available")
                return self.result
            template_id = items[0].get("id") or items[0].get("template_id")

            tenant, _ = _nsaas_api("/api/nsaas/tenants", "POST", {
                "name": "test-tenant-006",
                "contact_email": "sla@example.com",
            })
            tenant_id = tenant.get("id") or tenant.get("tenant_id")

            sl, _ = _nsaas_api("/api/nsaas/slices", "POST", {
                "tenant_id": tenant_id,
                "template_id": template_id,
                "name": "test-slice-006",
            })
            slice_id = sl.get("id") or sl.get("slice_id")

            _nsaas_api(f"/api/nsaas/slices/{slice_id}/activate", "POST")

            # Get SLA metrics
            result, status = _nsaas_api(f"/api/nsaas/slices/{slice_id}/sla")
            if status != 200:
                self.fail_test(f"SLA query failed: {status} {result}")
                return self.result

            log.info("SLA metrics for slice %s: %s", slice_id, result)
            self.pass_test(slice_id=slice_id, sla=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if slice_id:
                _nsaas_api(f"/api/nsaas/slices/{slice_id}", "DELETE")
            if tenant_id:
                _nsaas_api(f"/api/nsaas/tenants/{tenant_id}", "DELETE")
        return self.result


# ─── TC-NSAAS-010 ────────────────────────────────────────────────────


class NsaasProvisionPopulatesCatalogFK(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSAAS-010",
        title="ProvisionSlice populates nsaas_slices.nssai_catalog_id",
        spec="TS 28.531 §8.2",
        domain=Domain.PROVISIONING,
        nfs=(NF.NSSF, NF.NRF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  Foreign-key integrity between an NSaaS slice row and the\n"
            "  underlying NSSAI catalogue (TS 28.531 §8.2 mapped onto the\n"
            "  TS 23.501 §5.15.2.1 S-NSSAI registry). When a slice is\n"
            "  provisioned, nsaas_slices.nssai_catalog_id MUST point at a real\n"
            "  catalogue row, and the slice's SST MUST match that row's SST.\n"
            "  This stops orphan rows from drifting between the two stores.\n"
            "\n"
            "Procedure (TS 28.531 §8.2 + TS 23.501 §5.15.2.1)\n"
            "  1. Seed templates; pick first; read template_id.\n"
            "  2. POST /api/nsaas/tenants {name='tc-nsaas-010'}; read\n"
            "     tenant_id.\n"
            "  3. POST /api/nsaas/slices {tenant_id, template_id,\n"
            "     name='tc-nsaas-010-slice'} — require status 200/201; capture\n"
            "     slice_id.\n"
            "  4. GET /api/nsaas/slices; find our row (id or slice_id match).\n"
            "  5. Require nssai_catalog_id is populated.\n"
            "  6. GET /api/catalog/nssai; find row with id == cat_id.\n"
            "  7. Require int(cat_row.sst) == int(mine.sst).\n"
            "  8. finally: DELETE slice, DELETE tenant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixtures hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Slice has a non-null nssai_catalog_id that resolves to an\n"
            "  existing catalogue row whose SST matches the slice's SST.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  slice_id, nssai_catalog_id, catalog_row, slice_sst, slice_sd.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. SD-equality check is not asserted (only SST), to\n"
            "  tolerate SD-defaulted templates."
        ),
    )

    def run(self):
        tenant_id = None
        slice_id = None
        try:
            _nsaas_api("/api/nsaas/templates/seed", "POST")
            tpl_resp, _ = _nsaas_api("/api/nsaas/templates")
            tpls = tpl_resp.get("items") or tpl_resp.get("templates") or []
            if not tpls:
                self.fail_test("no NSaaS templates after seed")
                return self.result
            tpl = tpls[0]
            template_id = tpl.get("id") or tpl.get("template_id")

            ten, _ = _nsaas_api("/api/nsaas/tenants", "POST", {
                "name": "tc-nsaas-010",
                "contact_email": "fk@example.com",
            })
            tenant_id = ten.get("id") or ten.get("tenant_id")

            sl, st = _nsaas_api("/api/nsaas/slices", "POST", {
                "tenant_id": tenant_id, "template_id": template_id,
                "name": "tc-nsaas-010-slice",
            })
            if st not in (200, 201):
                self.fail_test(f"provision failed: {st} {sl}")
                return self.result
            slice_id = sl.get("id") or sl.get("slice_id")

            # Fetch the slice listing, find ours, assert nssai_catalog_id.
            slices_resp, _ = _nsaas_api("/api/nsaas/slices")
            slices = slices_resp.get("slices") or slices_resp.get("items") or []
            mine = next((s for s in slices
                         if (s.get("id") or s.get("slice_id")) == slice_id), None)
            if not mine:
                self.fail_test("provisioned slice not in /api/nsaas/slices",
                               slices_count=len(slices))
                return self.result

            cat_id = mine.get("nssai_catalog_id")
            if not cat_id:
                self.fail_test("nssai_catalog_id not populated on slice",
                               slice=mine)
                return self.result

            # Cross-check the catalog has the matching (sst, sd).
            cat_resp, _ = _nsaas_api("/api/catalog/nssai")
            cat_items = cat_resp.get("items") or []
            cat_row = next((c for c in cat_items if c.get("id") == cat_id), None)
            if not cat_row:
                self.fail_test(f"catalog has no row id={cat_id}",
                               catalog_size=len(cat_items))
                return self.result
            if int(cat_row.get("sst", -1)) != int(mine.get("sst", -2)):
                self.fail_test(f"sst mismatch slice={mine.get('sst')} "
                               f"cat={cat_row.get('sst')}",
                               slice=mine, catalog_row=cat_row)
                return self.result

            self.pass_test(slice_id=slice_id, nssai_catalog_id=cat_id,
                           catalog_row=cat_row,
                           slice_sst=mine.get("sst"),
                           slice_sd=mine.get("sd"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if slice_id:
                _nsaas_api(f"/api/nsaas/slices/{slice_id}", "DELETE")
            if tenant_id:
                _nsaas_api(f"/api/nsaas/tenants/{tenant_id}", "DELETE")
        return self.result


ALL_NSAAS_TCS = [
    NsaasSeedTemplates, NsaasCreateTenant, NsaasProvisionSlice,
    NsaasActivateSlice, NsaasDecommission, NsaasSla,
    NsaasProvisionPopulatesCatalogFK,
]

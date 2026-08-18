# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Slice Catalog (nssai_catalog) — TS 23.501 §5.15.2.

Exercises the canonical slice catalogue that PLMN advertisements,
UPF anchors, UE subscriptions, and NSaaS-instantiated slices all
foreign-key into. Specifically asserts the FK chain closed by the
slicing/quick-wins work:

  - TS 23.501 §5.15.2.1 — S-NSSAI = SST + optional SD
  - TS 23.501 §5.15.2.2 — Standardised SST table 5.15.2.2-1
  - TS 28.531 §8.2 — NSaaS provisioning populates the catalog FK
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_slice_catalog")


def _api(path, method="GET", body=None, params=None):
    """Call a SA Core REST endpoint and return (json|str, status)."""
    from src.core.api import get_core_ip
    qs = ""
    if params:
        from urllib.parse import urlencode
        qs = "?" + urlencode(params)
    url = f"http://{get_core_ip()}:5000{path}{qs}"
    headers = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body else None
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
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


def _catalog_items():
    body, status = _api("/api/catalog/nssai")
    if status != 200 or not isinstance(body, dict):
        return []
    return body.get("items") or []


def _find_catalog_row(items, sst, sd=""):
    """Return the catalog item matching (sst, sd) or None."""
    sd_norm = (sd or "").lower()
    for it in items:
        if int(it.get("sst", -1)) == int(sst) and (it.get("sd") or "").lower() == sd_norm:
            return it
    return None


# ─── TC-CATALOG-001 ──────────────────────────────────────────────────


class CatalogCRUDRoundTrip(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CATALOG-001",
        title="S-NSSAI catalog CRUD round-trip on /api/catalog/nssai",
        spec="TS 23.501 §5.15.2.1",
        domain=Domain.SLICING,
        nfs=(NF.NSSF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins TS 23.501 §5.15.2.1 — S-NSSAI = SST (1 byte) +\n"
            "  optional SD (3 bytes) — by exercising the canonical core\n"
            "  catalogue surface. Every other slicing FK (PLMN advert,\n"
            "  UPF anchor, UE subscription, NSaaS slice) joins through\n"
            "  this catalog, so CRUD must round-trip cleanly or the\n"
            "  whole slicing tab is unsafe to edit.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.2.1 + local REST API)\n"
            "  1. POST /api/catalog/nssai with {sst:200, sd:'0a0b0c',\n"
            "     name:'test-cat-001'} — SST=200 is intentionally far\n"
            "     from standardised SSTs to avoid seed collisions.\n"
            "     Expect 200/201 and a JSON body containing 'id'.\n"
            "  2. GET /api/catalog/nssai (via _catalog_items()); locate\n"
            "     the row by (sst=200, sd='0a0b0c'); assert\n"
            "     name == 'test-cat-001'.\n"
            "  3. PUT /api/catalog/nssai/{id} with the same (sst, sd)\n"
            "     and name='test-cat-001-renamed'. Expect 200/204.\n"
            "  4. GET again; assert name now == 'test-cat-001-renamed'.\n"
            "  5. finally: DELETE /api/catalog/nssai/{id} (cleanup).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; SST/SD/name hardcoded to the test fixture.)\n"
            "\n"
            "Pass criteria\n"
            "  POST → 200/201 with valid id; GET listing contains the row\n"
            "  with original name; PUT → 200/204; GET listing contains\n"
            "  the row with renamed name. Any deviation calls fail_test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  catalog_id (the POSTed id),\n"
            "  post_get_put_delete='ok'.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — REST is hit via http://<core_ip>:5000.\n"
            "  Uses SST=200 (private range) to avoid trampling seeded\n"
            "  catalogue rows. DELETE is best-effort cleanup in finally\n"
            "  and is NOT itself asserted — a hung DELETE would leak\n"
            "  the test row but not fail the case."
        ),
    )

    def run(self):
        created_id = None
        try:
            # POST a brand-new (sst, sd) — pick high SST to avoid collisions.
            body, status = _api("/api/catalog/nssai", "POST",
                                {"sst": 200, "sd": "0a0b0c", "name": "test-cat-001"})
            if status not in (200, 201):
                self.fail_test(f"POST failed: {status} {body}")
                return self.result
            created_id = body.get("id") if isinstance(body, dict) else None
            if not created_id:
                self.fail_test("POST returned no id", response=body)
                return self.result

            # GET should return our row.
            row = _find_catalog_row(_catalog_items(), 200, "0a0b0c")
            if not row:
                self.fail_test("created row not in GET listing")
                return self.result
            if row.get("name") != "test-cat-001":
                self.fail_test(f"name mismatch: {row.get('name')}", row=row)
                return self.result

            # PUT renames the row.
            _, status = _api(f"/api/catalog/nssai/{created_id}", "PUT",
                             {"sst": 200, "sd": "0a0b0c", "name": "test-cat-001-renamed"})
            if status not in (200, 204):
                self.fail_test(f"PUT failed: {status}")
                return self.result
            row = _find_catalog_row(_catalog_items(), 200, "0a0b0c")
            if not row or row.get("name") != "test-cat-001-renamed":
                self.fail_test("PUT did not persist new name", row=row)
                return self.result

            self.pass_test(catalog_id=created_id, post_get_put_delete="ok")
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if created_id:
                _api(f"/api/catalog/nssai/{created_id}", "DELETE")
        return self.result


# ─── TC-CATALOG-002 ──────────────────────────────────────────────────


class CatalogStandardisedSSTSeed(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CATALOG-002",
        title="Catalog boot seed includes TS 23.501 §5.15.2.2 standardised SSTs",
        spec="TS 23.501 §5.15.2.2",
        domain=Domain.SLICING,
        nfs=(NF.NSSF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins TS 23.501 §5.15.2.2 Table 5.15.2.2-1 standardised SST\n"
            "  values (1=eMBB, 2=URLLC, 3=MIoT, 4=V2X). At minimum the\n"
            "  seeded catalogue must contain SST=1 and SST=3 because\n"
            "  they back the seeded PFCP anchors upf-embb and upf-miot;\n"
            "  if either is missing the slicing tab and downstream UPF\n"
            "  routing both break. Regression guard against the catalog\n"
            "  empty-after-migration class of bugs.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.2.2 + local REST API)\n"
            "  1. GET /api/catalog/nssai (via _catalog_items()).\n"
            "  2. Build ssts_present = sorted set of int(item['sst']) for\n"
            "     every catalog row.\n"
            "  3. Compute missing = [s for s in (1,3) if s not in\n"
            "     ssts_present].\n"
            "  4. If missing → fail_test with required SSTs missing.\n"
            "  5. Else → pass_test with the SST list and count.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; required SST set {1,3} is hardcoded.)\n"
            "\n"
            "Pass criteria\n"
            "  Both SST=1 and SST=3 appear in the GET listing on a\n"
            "  freshly booted core. SST=2 (URLLC) and SST=4 (V2X) are\n"
            "  NOT required by this test — they may be added by NSaaS\n"
            "  provisioning later.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ssts=<sorted list of SSTs present>,\n"
            "  count=<number of catalog items>.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Only the eMBB+MIoT seed minimum is\n"
            "  enforced — SST=2 and SST=4 absence is silently tolerated\n"
            "  even though §5.15.2.2 standardises them. SD values are\n"
            "  not inspected, only SSTs."
        ),
    )

    def run(self):
        try:
            items = _catalog_items()
            ssts_present = sorted(set(int(it.get("sst", 0)) for it in items))
            log.info("catalog SSTs at boot: %s", ssts_present)
            # eMBB (SST=1) and MIoT (SST=3) are bound to the seeded
            # PFCP anchors upf-embb and upf-miot, so they must exist.
            missing = [s for s in (1, 3) if s not in ssts_present]
            if missing:
                self.fail_test(f"required SSTs missing from seed: {missing}",
                               present=ssts_present)
                return self.result
            self.pass_test(ssts=ssts_present, count=len(items))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── TC-CATALOG-003 ──────────────────────────────────────────────────


class CatalogPopulatedByNSaaSProvision(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CATALOG-003",
        title="NSaaS slice provisioning upserts a matching catalog row",
        spec="TS 28.531 §8.2",
        domain=Domain.SLICING,
        nfs=(NF.NSSF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins TS 28.531 §8.2 (Network Slice as a Service —\n"
            "  provisioning lifecycle): when NSaaS ProvisionSlice runs\n"
            "  for a tenant against a template, the resulting slice's\n"
            "  (SST, SD) MUST appear in the canonical S-NSSAI catalogue\n"
            "  so PLMN advertisement / UPF routing / UE subscription FKs\n"
            "  can resolve it. End-to-end check that the NSaaS path\n"
            "  closes the catalogue FK chain.\n"
            "\n"
            "Procedure (TS 28.531 §8.2 + local NSaaS REST API)\n"
            "  1. POST /api/nsaas/templates/seed — idempotently install\n"
            "     the canonical NSaaS template set.\n"
            "  2. GET /api/nsaas/templates → pick the eMBB template\n"
            "     (sst=1), record template_id and (sst, sd).\n"
            "  3. POST /api/nsaas/tenants with {name:'tc-catalog-003',\n"
            "     contact_email:...} → record tenant_id.\n"
            "  4. POST /api/nsaas/slices with {tenant_id, template_id,\n"
            "     name:'tc-catalog-003-slice'}. Expect 200/201;\n"
            "     record slice_id.\n"
            "  5. GET /api/catalog/nssai; _find_catalog_row by\n"
            "     (template.sst, template.sd) — must return a row.\n"
            "  6. finally: DELETE /api/nsaas/slices/{slice_id} then\n"
            "     /api/nsaas/tenants/{tenant_id} (cleanup).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; tenant/slice names and template selection\n"
            "  (first SST=1) are hardcoded.)\n"
            "\n"
            "Pass criteria\n"
            "  Template seed + tenant create + slice create all return\n"
            "  success AND _find_catalog_row(items, template.sst,\n"
            "  template.sd) is not None after provisioning.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  slice_id, catalog_row (the matched dict), template_sst,\n"
            "  template_sd.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Test does NOT verify the catalog row\n"
            "  name / metadata — only existence by (SST, SD). Cleanup\n"
            "  (slice + tenant DELETE) is best-effort in finally and\n"
            "  not asserted; a hung core would leak the tenant. Falls\n"
            "  back to tpls[0] if no eMBB template (SST=1) is present,\n"
            "  which would weaken the assertion."
        ),
    )

    def run(self):
        tenant_id = None
        slice_id = None
        try:
            _api("/api/nsaas/templates/seed", "POST")
            tpl_resp, _ = _api("/api/nsaas/templates")
            tpls = tpl_resp.get("items") or tpl_resp.get("templates") or []
            if not tpls:
                self.fail_test("no NSaaS templates after seed")
                return self.result
            # Pick the eMBB template (SST=1, SD=000001) — known to the seed.
            tpl = next((t for t in tpls if int(t.get("sst", 0)) == 1), tpls[0])
            template_id = tpl.get("id")

            ten_resp, _ = _api("/api/nsaas/tenants", "POST", {
                "name": "tc-catalog-003",
                "contact_email": "catalog@example.com",
            })
            tenant_id = ten_resp.get("id") or ten_resp.get("tenant_id")

            sl_resp, status = _api("/api/nsaas/slices", "POST", {
                "tenant_id": tenant_id, "template_id": template_id,
                "name": "tc-catalog-003-slice",
            })
            if status not in (200, 201):
                self.fail_test(f"provision failed: {status} {sl_resp}")
                return self.result
            slice_id = sl_resp.get("id") or sl_resp.get("slice_id")

            # The catalog must now contain a row matching the template
            # (sst, sd). Match against (1, '000001') from the eMBB template.
            row = _find_catalog_row(_catalog_items(),
                                    tpl.get("sst", 1), tpl.get("sd", ""))
            if not row:
                self.fail_test("provisioning did not upsert catalog",
                               template_sst=tpl.get("sst"),
                               template_sd=tpl.get("sd"))
                return self.result

            self.pass_test(slice_id=slice_id, catalog_row=row,
                           template_sst=tpl.get("sst"),
                           template_sd=tpl.get("sd"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if slice_id:
                _api(f"/api/nsaas/slices/{slice_id}", "DELETE")
            if tenant_id:
                _api(f"/api/nsaas/tenants/{tenant_id}", "DELETE")
        return self.result


# ─── TC-CATALOG-004 ──────────────────────────────────────────────────


class CatalogIdempotentUpsert(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CATALOG-004",
        title="Catalog upsert is idempotent across repeated NSaaS provisions",
        spec="TS 23.501 §5.15.2.1",
        domain=Domain.SLICING,
        nfs=(NF.NSSF,),
        severity=Severity.MAJOR,
        tags=("regression", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins idempotent get-or-create semantics on the S-NSSAI\n"
            "  catalogue (TS 23.501 §5.15.2.1 — the (SST, SD) tuple is a\n"
            "  natural key). Two NSaaS provisions of the same template\n"
            "  must never produce two catalog rows for the same\n"
            "  (SST, SD). Regression guard against db/crud/nssai.go\n"
            "  drifting into an insert-always pattern.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.2.1 + TS 28.531 §8.2)\n"
            "  1. POST /api/nsaas/templates/seed and pick eMBB template\n"
            "     (sst=1); record (sst, sd).\n"
            "  2. POST /api/nsaas/tenants {name:'tc-catalog-004', ...}.\n"
            "  3. before = _catalog_items(); count_before = rows where\n"
            "     (sst, sd) match the template tuple.\n"
            "  4. For n in ('a','b'): POST /api/nsaas/slices\n"
            "     {tenant_id, template_id, name:f'tc-cat-004-{n}'}.\n"
            "     Expect 200/201; record slice_a / slice_b ids.\n"
            "  5. after = _catalog_items(); count_after = same filter.\n"
            "  6. delta = count_after - count_before; assert delta in\n"
            "     (0, 1). Anything else → fail_test.\n"
            "  7. finally: DELETE both slices then the tenant.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; template = first SST=1, two provisions\n"
            "  are hardcoded.)\n"
            "\n"
            "Pass criteria\n"
            "  Both POST /api/nsaas/slices return 200/201 AND the\n"
            "  catalog row count for (template.sst, template.sd) grows\n"
            "  by exactly 0 or 1 — never 2 — across the two provisions.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rows_before, rows_after, delta, sst, sd.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The test only counts rows matching the\n"
            "  template's exact (SST, SD) — it does not assert the\n"
            "  template_id FK or slice rows themselves are deduplicated\n"
            "  (those have separate lifecycle). Cleanup is best-effort."
        ),
    )

    def run(self):
        tenant_id = None
        slice_a = None
        slice_b = None
        try:
            _api("/api/nsaas/templates/seed", "POST")
            tpl_resp, _ = _api("/api/nsaas/templates")
            tpls = tpl_resp.get("items") or tpl_resp.get("templates") or []
            tpl = next((t for t in tpls if int(t.get("sst", 0)) == 1), tpls[0])
            template_id = tpl.get("id")
            sst, sd = int(tpl.get("sst", 1)), tpl.get("sd", "")

            ten_resp, _ = _api("/api/nsaas/tenants", "POST", {
                "name": "tc-catalog-004",
                "contact_email": "idem@example.com",
            })
            tenant_id = ten_resp.get("id") or ten_resp.get("tenant_id")

            before = _catalog_items()
            count_before = sum(1 for it in before
                               if int(it.get("sst", -1)) == sst
                               and (it.get("sd") or "") == (sd or ""))

            for n, holder in (("a", slice_a), ("b", slice_b)):
                resp, st = _api("/api/nsaas/slices", "POST", {
                    "tenant_id": tenant_id, "template_id": template_id,
                    "name": f"tc-cat-004-{n}",
                })
                if st not in (200, 201):
                    self.fail_test(f"provision {n} failed: {st} {resp}")
                    return self.result
                if n == "a":
                    slice_a = resp.get("id") or resp.get("slice_id")
                else:
                    slice_b = resp.get("id") or resp.get("slice_id")

            after = _catalog_items()
            count_after = sum(1 for it in after
                              if int(it.get("sst", -1)) == sst
                              and (it.get("sd") or "") == (sd or ""))

            # Either no new row (template's (sst,sd) already in catalog)
            # or exactly +1 — never +2 — for two provisions.
            delta = count_after - count_before
            if delta not in (0, 1):
                self.fail_test(f"non-idempotent: catalog rows for "
                               f"({sst},{sd}) went {count_before}→{count_after}")
                return self.result

            self.pass_test(rows_before=count_before, rows_after=count_after,
                           delta=delta, sst=sst, sd=sd)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for sid in (slice_a, slice_b):
                if sid:
                    _api(f"/api/nsaas/slices/{sid}", "DELETE")
            if tenant_id:
                _api(f"/api/nsaas/tenants/{tenant_id}", "DELETE")
        return self.result


# ─── TC-CATALOG-005 ──────────────────────────────────────────────────


class CatalogSliceTabReachable(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CATALOG-005",
        title="Slice Catalog GUI endpoint returns documented {items: [...]} shape",
        spec="TS 23.501 §5.15.2.1",
        domain=Domain.SLICING,
        nfs=(NF.NSSF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pins the GUI contract for the Slicing & QoS → Slice\n"
            "  Catalog tab: the documented envelope is\n"
            "  `{ items: [<S-NSSAI rows>] }`. The GUI iterates\n"
            "  response.items unconditionally, so any drift in the\n"
            "  envelope (renaming to data/rows, returning a raw list,\n"
            "  returning 500) silently empties the tab. TS 23.501\n"
            "  §5.15.2.1 governs row shape — this test only covers the\n"
            "  envelope.\n"
            "\n"
            "Procedure (local REST API surface)\n"
            "  1. GET /api/catalog/nssai.\n"
            "  2. Assert HTTP status == 200; otherwise fail_test.\n"
            "  3. Assert response body is a dict AND contains key\n"
            "     'items'; otherwise fail_test.\n"
            "  4. Assert body['items'] is a list; otherwise fail_test.\n"
            "  5. pass_test reporting len(items).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed.)\n"
            "\n"
            "Pass criteria\n"
            "  status == 200 AND isinstance(body, dict) AND 'items' in\n"
            "  body AND isinstance(body['items'], list).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  item_count (len of body['items']).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Envelope-only — the test does not\n"
            "  validate any per-row schema (sst, sd, name), only that\n"
            "  the GUI's outer iteration shape exists. Empty items=[]\n"
            "  still passes (use TC-CATALOG-002 to gate non-empty)."
        ),
    )

    def run(self):
        try:
            body, status = _api("/api/catalog/nssai")
            if status != 200:
                self.fail_test(f"non-200 status: {status}", body=body)
                return self.result
            if not isinstance(body, dict) or "items" not in body:
                self.fail_test("response not a dict with 'items' key", body=body)
                return self.result
            if not isinstance(body["items"], list):
                self.fail_test("'items' is not a list", body=body)
                return self.result
            self.pass_test(item_count=len(body["items"]))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_SLICE_CATALOG_TCS = [
    CatalogCRUDRoundTrip,
    CatalogStandardisedSSTSeed,
    CatalogPopulatedByNSaaSProvision,
    CatalogIdempotentUpsert,
    CatalogSliceTabReachable,
]

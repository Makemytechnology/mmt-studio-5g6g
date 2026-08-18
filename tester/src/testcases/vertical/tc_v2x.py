# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Vehicle-to-Everything (V2X) over 5GS.

TS 22.186 §5      — V2X service requirements
TS 23.287 §4.4    — V2X policy / parameter provisioning architecture
TS 23.287 §5.1.2  — V2X policy / parameter provisioning procedure
TS 23.287 §5.2    — V2X authorization
TS 23.287 §5.4.4  — Standardised PQI values (Table 5.4.4-1)
TS 23.287 §5.5    — V2X subscription data
TS 24.587 §5      — UE Policy Container envelope (deferred)

The tests drive the SA Core REST surface at /api/v2x/* and verify
that the responses mirror the spec semantics: the canonical PQI rows
are present (§5.4.4), the subscription gate denies un-authorised UEs
from receiving policy (§5.1.2), and policy provisioning produces the
three canonical container elements (auth_policy / pc5_qos_params /
v2x_frequencies).
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_v2x")


def _v2x_api(path, method="GET", body=None):
    """Call SA Core V2X REST API. Returns (json_body, status_code)."""
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


# ─────────────────────────────────────────────────────────────────────
# TS 23.287 §5.4.4 — Standardised PQI catalog
# ─────────────────────────────────────────────────────────────────────


class V2XServiceTypesList(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-001",
        title="V2X standardised PQI catalog (Table 5.4.4-1) is seeded",
        spec="TS 23.287 §5.4.4",
        domain=Domain.V2X,
        nfs=(NF.PCF, NF.SMF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundational seed-check on the PC5 QoS catalog. TS 23.287\n"
            "  §5.4.4 (Table 5.4.4-1) lists the standardised PQI rows the\n"
            "  PCF/SMF must surface; without them the V2X policy layer\n"
            "  has no QoS vocabulary.\n"
            "\n"
            "Procedure (TS 23.287 §5.4.4 Table 5.4.4-1)\n"
            "  1. GET /api/v2x/service-types.\n"
            "  2. fail_test if HTTP != 200 OR response is not a list OR\n"
            "     list has fewer than 5 rows.\n"
            "  3. Build pqis = set of row.pqi values.\n"
            "  4. Required smoke-set {21, 22, 23, 55, 90}; fail_test if\n"
            "     any missing.\n"
            "  5. For each row, fail_test if resource_type not in\n"
            "     ('GBR', 'NonGBR', 'DelCritGBR') — Table 5.4.4-1 CHECK.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  All five canonical PQIs are present AND every row has a\n"
            "  valid resource_type.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rows (count), pqis (sorted set).\n"
            "\n"
            "Known constraints\n"
            "  Smoke set is intentionally narrower than TC-V2X-030's\n"
            "  full-row set so operator edits to row 56..59 don't break\n"
            "  the smoke gate.\n"
            "  Smoke set is intentionally narrower than TC-V2X-030's full-row\n"
            "  set so operator edits to row 56..59 don't break the smoke gate.\n"
            "  PER values are not asserted here."
        ),
    )

    def run(self):
        try:
            list_, status = _v2x_api("/api/v2x/service-types")
            if status != 200:
                self.fail_test(f"list failed: {status} {list_}")
                return self.result
            if not isinstance(list_, list) or len(list_) < 5:
                self.fail_test(f"expected ≥5 PQI rows, got {len(list_) if isinstance(list_, list) else 'non-list'}")
                return self.result

            pqis = {row.get("pqi") for row in list_}
            # Table 5.4.4-1 includes (at minimum) PQIs 21, 22, 23, 55, 90.
            # We assert a subset to stay tolerant of operator edits.
            required = {21, 22, 23, 55, 90}
            missing = required - pqis
            if missing:
                self.fail_test(f"missing canonical PQIs: {sorted(missing)}",
                               present=sorted(pqis))
                return self.result

            # Spot-check resource_type CHECK constraint
            for row in list_:
                if row.get("resource_type") not in ("GBR", "NonGBR", "DelCritGBR"):
                    self.fail_test(f"row pqi={row.get('pqi')} has invalid resource_type")
                    return self.result

            self.pass_test(rows=len(list_), pqis=sorted(pqis))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class V2XServiceTypeCRUD(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-002",
        title="Operator CRUD on V2X PQI table with validation",
        spec="TS 23.287 §5.4.4",
        domain=Domain.V2X,
        nfs=(NF.PCF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Full operator CRUD walk on the PC5 QoS / PQI table with\n"
            "  input validation. TS 23.287 §5.4.4 places this row-set as\n"
            "  the canonical PC5 QoS catalog; the table must be editable\n"
            "  and reject malformed values.\n"
            "\n"
            "Procedure (TS 23.287 §5.4.4)\n"
            "  1. Pre-cleanup: DELETE /service-types/199 (custom range).\n"
            "  2. POST /service-types with pqi=199, GBR, priority=5,\n"
            "     PDB=50ms, PER=1e-3, fiveqi_uu=3. fail_test on non-2xx.\n"
            "  3. GET /service-types/199; fail_test if HTTP != 200 or\n"
            "     pqi mismatch.\n"
            "  4. PUT /service-types/199 swapping to NonGBR/priority=6/\n"
            "     PDB=60. fail_test if status != 200 or upd.ok falsy.\n"
            "  5. GET back; fail_test if resource_type != NonGBR or\n"
            "     priority_level != 6.\n"
            "  6. POST /service-types with resource_type='Bogus' (pqi=198).\n"
            "     fail_test if status != 400.\n"
            "  7. DELETE /service-types/199; fail_test if status != 200.\n"
            "  8. GET /service-types/199; fail_test if status != 404.\n"
            "  9. finally: DELETE /service-types/199 again.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — custom_pqi pinned to 199.\n"
            "\n"
            "Pass criteria\n"
            "  All seven CRUD/validation gates pass.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pqi (199).\n"
            "\n"
            "Known constraints\n"
            "  Uses operator range 199; collisions with manual edits in\n"
            "  that range will trip the test."
        ),
    )

    def run(self):
        custom_pqi = 199  # operator range; outside seeded values
        try:
            # Cleanup any stragglers from a previous run.
            _v2x_api(f"/api/v2x/service-types/{custom_pqi}", "DELETE")

            create, status = _v2x_api("/api/v2x/service-types", "POST", {
                "service_name": "tc_v2x_002_custom",
                "pqi": custom_pqi,
                "resource_type": "GBR",
                "priority_level": 5,
                "packet_delay_ms": 50,
                "packet_error_rate": "1e-3",
                "fiveqi_uu": 3,
                "description": "Test PQI for TC-V2X-002",
            })
            if status not in (200, 201):
                self.fail_test(f"create failed: {status} {create}")
                return self.result

            # Fetch
            got, gstatus = _v2x_api(f"/api/v2x/service-types/{custom_pqi}")
            if gstatus != 200 or got.get("pqi") != custom_pqi:
                self.fail_test(f"get after create failed: {gstatus} {got}")
                return self.result

            # Update
            upd, ustatus = _v2x_api(f"/api/v2x/service-types/{custom_pqi}", "PUT", {
                "service_name": "tc_v2x_002_custom",
                "resource_type": "NonGBR",
                "priority_level": 6,
                "packet_delay_ms": 60,
                "packet_error_rate": "1e-2",
                "description": "Updated",
            })
            if ustatus != 200 or not upd.get("ok"):
                self.fail_test(f"update failed: {ustatus} {upd}")
                return self.result

            got2, _ = _v2x_api(f"/api/v2x/service-types/{custom_pqi}")
            if got2.get("resource_type") != "NonGBR" or got2.get("priority_level") != 6:
                self.fail_test(f"update did not persist: {got2}")
                return self.result

            # Reject: invalid resource_type
            _, bad_status = _v2x_api("/api/v2x/service-types", "POST", {
                "service_name": "tc_v2x_002_bad", "pqi": 198,
                "resource_type": "Bogus", "priority_level": 1,
                "packet_delay_ms": 10, "packet_error_rate": "1e-4",
            })
            if bad_status != 400:
                self.fail_test(f"expected 400 for bad resource_type, got {bad_status}")
                return self.result

            # Delete + verify 404
            _, dstatus = _v2x_api(f"/api/v2x/service-types/{custom_pqi}", "DELETE")
            if dstatus != 200:
                self.fail_test(f"delete failed: {dstatus}")
                return self.result
            _, gone = _v2x_api(f"/api/v2x/service-types/{custom_pqi}")
            if gone != 404:
                self.fail_test(f"expected 404 after delete, got {gone}")
                return self.result

            self.pass_test(pqi=custom_pqi)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _v2x_api(f"/api/v2x/service-types/{custom_pqi}", "DELETE")
        return self.result


# ─────────────────────────────────────────────────────────────────────
# TS 23.287 §5.2 / §5.5 — V2X subscription / authorization
# ─────────────────────────────────────────────────────────────────────


class V2XAuthorizeUE(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-003",
        title="Authorize a UE for V2X and verify subscription",
        spec="TS 23.287 §5.5",
        domain=Domain.V2X,
        nfs=(NF.UDM, NF.PCF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates V2X subscription authorization and the schema-\n"
            "  validation gate. TS 23.287 §5.5 defines the subscription\n"
            "  data (v2x_authorized flag + ue_type + pc5_ambr_kbps); the\n"
            "  authorize endpoint must persist these and reject malformed\n"
            "  ue_type values.\n"
            "\n"
            "Procedure (TS 23.287 §5.5)\n"
            "  1. require_ue() → imsi.\n"
            "  2. GET /api/v2x/subscription/{imsi} — pre-state.\n"
            "  3. POST /api/v2x/authorize with ue_type='vehicle',\n"
            "     pc5_ambr_kbps=50000. fail_test if !=200 or ok falsy.\n"
            "  4. GET /subscription/{imsi}; fail_test if status != 200\n"
            "     or v2x_authorized is falsy.\n"
            "  5. fail_test if v2x_ue_type != 'vehicle'.\n"
            "  6. fail_test if v2x_pc5_ambr_kbps != 50000.\n"
            "  7. POST /authorize with ue_type='alien'; fail_test if\n"
            "     status != 400 (must reject).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Authorize 200/ok + subscription readback shows authorized\n"
            "  + ue_type/pc5_ambr match + invalid ue_type → 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, subscription (full envelope).\n"
            "\n"
            "Known constraints\n"
            "  Does not exercise actual PC5 sidelink; pure subscription\n"
            "  CRUD."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # Pre-state: not authorised.
            sub0, _ = _v2x_api(f"/api/v2x/subscription/{imsi}")
            log.info("V2X pre-state imsi=%s authorized=%s", imsi, sub0.get("v2x_authorized"))

            # Authorise.
            res, status = _v2x_api("/api/v2x/authorize", "POST", {
                "imsi": imsi, "ue_type": "vehicle", "pc5_ambr_kbps": 50000,
            })
            if status != 200 or not res.get("ok"):
                self.fail_test(f"authorize failed: {status} {res}")
                return self.result

            # Re-read.
            sub1, sstatus = _v2x_api(f"/api/v2x/subscription/{imsi}")
            if sstatus != 200 or not sub1.get("v2x_authorized"):
                self.fail_test(f"subscription not flipped: {sub1}")
                return self.result
            if sub1.get("v2x_ue_type") != "vehicle":
                self.fail_test(f"ue_type mismatch: {sub1}")
                return self.result
            if sub1.get("v2x_pc5_ambr_kbps") != 50000:
                self.fail_test(f"pc5_ambr_kbps mismatch: {sub1}")
                return self.result

            # Reject: invalid ue_type per TS 23.287 §5.5.
            _, bad_status = _v2x_api("/api/v2x/authorize", "POST", {
                "imsi": imsi, "ue_type": "alien", "pc5_ambr_kbps": 1000,
            })
            if bad_status != 400:
                self.fail_test(f"expected 400 for invalid ue_type, got {bad_status}")
                return self.result

            self.pass_test(imsi=imsi, subscription=sub1)
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class V2XPC5QoSQuery(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-004",
        title="PC5 QoS table query is gated on V2X authorization",
        spec="TS 23.287 §5.4",
        domain=Domain.V2X,
        nfs=(NF.UDM, NF.PCF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance", "security"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates the authorization gate on PC5 QoS retrieval. TS\n"
            "  23.287 §5.4 says only V2X-authorised UEs may receive the\n"
            "  PC5 QoS catalog; an unauthorised UE must be denied (403).\n"
            "\n"
            "Procedure (TS 23.287 §5.4)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/v2x/deauthorize {imsi} (forces pre-state).\n"
            "  3. GET /api/v2x/pc5-qos/{imsi}; fail_test if status != 403.\n"
            "  4. POST /api/v2x/authorize {imsi, vehicle, 50000}.\n"
            "  5. GET /api/v2x/pc5-qos/{imsi}; fail_test if status != 200.\n"
            "  6. Read qos = res.pc5_qos_params; fail_test if len < 5.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Unauthorised GET → 403 AND authorised GET → 200 with at\n"
            "  least 5 PQI rows.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, pqi_count.\n"
            "\n"
            "Known constraints\n"
            "  Authorisation state persists after the test — does not\n"
            "  call deauthorize on teardown.\n"
            "  Authorisation state persists after the test — does not call\n"
            "  deauthorize on teardown. PQI row schema is checked separately\n"
            "  by TC-V2X-052.\n"
            "  Operator must run TC-V2X-003 first to put the UE in a\n"
            "  known authorisation state if needed."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # First deauthorise → expect 403.
            _v2x_api("/api/v2x/deauthorize", "POST", {"imsi": imsi})
            _, denied = _v2x_api(f"/api/v2x/pc5-qos/{imsi}")
            if denied != 403:
                self.fail_test(f"expected 403 when unauthorised, got {denied}")
                return self.result

            # Authorise + fetch.
            _v2x_api("/api/v2x/authorize", "POST", {
                "imsi": imsi, "ue_type": "vehicle", "pc5_ambr_kbps": 50000,
            })
            res, status = _v2x_api(f"/api/v2x/pc5-qos/{imsi}")
            if status != 200:
                self.fail_test(f"pc5-qos fetch failed: {status} {res}")
                return self.result
            qos = res.get("pc5_qos_params") or []
            if len(qos) < 5:
                self.fail_test(f"expected ≥5 PQI rows, got {len(qos)}")
                return self.result

            self.pass_test(imsi=imsi, pqi_count=len(qos))
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─────────────────────────────────────────────────────────────────────
# TS 23.287 §5.1.2 — V2X policy / parameter provisioning
# ─────────────────────────────────────────────────────────────────────


class V2XAuthorizedPLMNs(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-005",
        title="Per-UE V2X authorised-PLMN list CRUD",
        spec="TS 23.287 §5.1.2",
        domain=Domain.V2X,
        nfs=(NF.UDM, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates idempotent CRUD on the per-UE authorised-PLMN\n"
            "  list, the §5.1.2 container element that gates roaming-PLMN\n"
            "  V2X access. Re-adding an existing entry must not duplicate.\n"
            "\n"
            "Procedure (TS 23.287 §5.1.2)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /authorized-plmns three times: imsi+00101,\n"
            "     imsi+00102, imsi+00101 (duplicate). All three must\n"
            "     return 200/201 — fail_test on any non-2xx.\n"
            "  3. GET /authorized-plmns/{imsi}; read authorized_plmns.\n"
            "  4. fail_test if either '00101' or '00102' missing.\n"
            "  5. DELETE /authorized-plmns?imsi&plmn_id=00102.\n"
            "  6. fail_test if delete status != 200.\n"
            "  7. GET /authorized-plmns/{imsi}; fail_test if '00102'\n"
            "     still present.\n"
            "  8. finally: DELETE both PLMNs for this IMSI.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — PLMN IDs pinned to 00101/00102.\n"
            "\n"
            "Pass criteria\n"
            "  All three POSTs 2xx, both PLMNs visible, DELETE 200,\n"
            "  deleted PLMN no longer visible.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, plmns_after_delete.\n"
            "\n"
            "Known constraints\n"
            "  Duplicate-add behaviour is binary (2xx accepted) — list\n"
            "  count after duplicate is not asserted."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # Add two PLMNs (idempotent).
            _, s1 = _v2x_api("/api/v2x/authorized-plmns", "POST",
                             {"imsi": imsi, "plmn_id": "00101"})
            _, s2 = _v2x_api("/api/v2x/authorized-plmns", "POST",
                             {"imsi": imsi, "plmn_id": "00102"})
            _, s_dup = _v2x_api("/api/v2x/authorized-plmns", "POST",
                                {"imsi": imsi, "plmn_id": "00101"})
            for s, label in ((s1, "first"), (s2, "second"), (s_dup, "dup")):
                if s not in (200, 201):
                    self.fail_test(f"{label} POST failed: {s}")
                    return self.result

            # List.
            res, status = _v2x_api(f"/api/v2x/authorized-plmns/{imsi}")
            plmns = res.get("authorized_plmns") or []
            if "00101" not in plmns or "00102" not in plmns:
                self.fail_test(f"PLMN list missing entries: {plmns}")
                return self.result

            # Delete.
            _, ds = _v2x_api(f"/api/v2x/authorized-plmns?imsi={imsi}&plmn_id=00102",
                             "DELETE")
            if ds != 200:
                self.fail_test(f"delete failed: {ds}")
                return self.result

            res2, _ = _v2x_api(f"/api/v2x/authorized-plmns/{imsi}")
            plmns2 = res2.get("authorized_plmns") or []
            if "00102" in plmns2:
                self.fail_test(f"00102 still present after delete: {plmns2}")
                return self.result

            self.pass_test(imsi=imsi, plmns_after_delete=plmns2)
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _v2x_api(f"/api/v2x/authorized-plmns?imsi={imsi}&plmn_id=00101",
                     "DELETE")
            _v2x_api(f"/api/v2x/authorized-plmns?imsi={imsi}&plmn_id=00102",
                     "DELETE")
        return self.result


class V2XPolicyProvision(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-006",
        title="V2X policy provisioning emits canonical container body",
        spec="TS 23.287 §5.1.2",
        domain=Domain.V2X,
        nfs=(NF.UDM, NF.PCF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates the V2X policy / parameter provisioning endpoint:\n"
            "  unauthorised UEs are denied (403), authorised UEs receive\n"
            "  the canonical §5.1.2 container body (auth_policy,\n"
            "  pc5_qos_params, v2x_frequencies).\n"
            "\n"
            "Procedure (TS 23.287 §5.1.2)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/v2x/deauthorize {imsi}.\n"
            "  3. POST /api/v2x/policy/provision {imsi}; fail_test if\n"
            "     status != 403 (spec gate).\n"
            "  4. POST /authorize (vehicle, 50000), POST /authorized-plmns\n"
            "     (00101).\n"
            "  5. POST /policy/provision {imsi}; fail_test on !=200 or\n"
            "     ok falsy.\n"
            "  6. For each key in (auth_policy, pc5_qos_params,\n"
            "     v2x_frequencies): fail_test if not in policy.\n"
            "  7. Pull auth_policy; fail_test if 00101 not in\n"
            "     authorized_plmns OR ue_type != 'vehicle'.\n"
            "  8. fail_test if len(pc5_qos_params) < 5.\n"
            "  9. finally: DELETE /authorized-plmns 00101.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  All four §5.1.2 container checks + auth gate succeed.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, qos_count, plmn_count.\n"
            "\n"
            "Known constraints\n"
            "  v2x_frequencies content is not value-asserted, only presence."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # Spec gate: unauthorised UEs cannot receive policy (§5.1.2).
            _v2x_api("/api/v2x/deauthorize", "POST", {"imsi": imsi})
            _, denied = _v2x_api("/api/v2x/policy/provision", "POST", {"imsi": imsi})
            if denied != 403:
                self.fail_test(f"expected 403 when unauthorised, got {denied}")
                return self.result

            # Authorise + add a PLMN.
            _v2x_api("/api/v2x/authorize", "POST", {
                "imsi": imsi, "ue_type": "vehicle", "pc5_ambr_kbps": 50000,
            })
            _v2x_api("/api/v2x/authorized-plmns", "POST",
                     {"imsi": imsi, "plmn_id": "00101"})

            res, status = _v2x_api("/api/v2x/policy/provision", "POST", {"imsi": imsi})
            if status != 200 or not res.get("ok"):
                self.fail_test(f"provision failed: {status} {res}")
                return self.result

            policy = res.get("policy") or {}
            # TS 23.287 §5.1.2 container elements:
            for key in ("auth_policy", "pc5_qos_params", "v2x_frequencies"):
                if key not in policy:
                    self.fail_test(f"policy missing {key}: keys={list(policy.keys())}")
                    return self.result

            ap = policy["auth_policy"] or {}
            if "00101" not in (ap.get("authorized_plmns") or []):
                self.fail_test(f"auth_policy.authorized_plmns missing: {ap}")
                return self.result
            if ap.get("ue_type") != "vehicle":
                self.fail_test(f"auth_policy.ue_type mismatch: {ap}")
                return self.result

            qos = policy["pc5_qos_params"] or []
            if len(qos) < 5:
                self.fail_test(f"pc5_qos_params too small: {len(qos)}")
                return self.result

            self.pass_test(imsi=imsi, qos_count=len(qos),
                           plmn_count=len(ap.get("authorized_plmns") or []))
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _v2x_api(f"/api/v2x/authorized-plmns?imsi={imsi}&plmn_id=00101",
                     "DELETE")
        return self.result


class V2XPolicyLogAudit(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-007",
        title="V2X policy provisioning is recorded in audit log",
        spec="TS 23.287 §5.1.2",
        domain=Domain.V2X,
        nfs=(NF.UDM, NF.PCF, NF.AF),
        severity=Severity.MINOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates the audit-trail on V2X policy provisioning. TS\n"
            "  23.287 §5.1.2 doesn't strictly mandate the log, but\n"
            "  operator-grade deployments require it for regulatory\n"
            "  traceability; the policy/log endpoint must record each\n"
            "  successful provision.\n"
            "\n"
            "Procedure (TS 23.287 §5.1.2 + operator OAM)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/v2x/authorize (vehicle, 50000) to ensure\n"
            "     subsequent provision succeeds.\n"
            "  3. GET /policy/log?imsi=...&limit=10; capture count.\n"
            "  4. POST /policy/provision {imsi}.\n"
            "  5. GET /policy/log; fail_test if status != 200.\n"
            "  6. fail_test if n_after <= n_before.\n"
            "  7. fail_test if top entry imsi != IMSI under test.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Log count strictly increases AND latest entry IMSI matches.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, entries_added, latest (the new top entry).\n"
            "\n"
            "Known constraints\n"
            "  Race-safe only because logs are per-IMSI filtered; if a\n"
            "  parallel test provisions the same IMSI, the assertion may\n"
            "  see a different entry on top."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            _v2x_api("/api/v2x/authorize", "POST", {
                "imsi": imsi, "ue_type": "vehicle", "pc5_ambr_kbps": 50000,
            })

            # Snapshot count for this IMSI.
            before, _ = _v2x_api(f"/api/v2x/policy/log?imsi={imsi}&limit=10")
            n_before = before.get("count", 0)

            # Provision.
            _v2x_api("/api/v2x/policy/provision", "POST", {"imsi": imsi})

            after, status = _v2x_api(f"/api/v2x/policy/log?imsi={imsi}&limit=10")
            if status != 200:
                self.fail_test(f"audit log read failed: {status} {after}")
                return self.result

            n_after = after.get("count", 0)
            if n_after <= n_before:
                self.fail_test(f"audit log not appended: before={n_before} after={n_after}")
                return self.result

            entries = after.get("entries") or []
            if not entries or entries[0].get("imsi") != imsi:
                self.fail_test(f"top entry does not match imsi={imsi}: {entries[:1]}")
                return self.result

            self.pass_test(imsi=imsi, entries_added=n_after - n_before,
                           latest=entries[0])
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─────────────────────────────────────────────────────────────────────


# ─────────────────────────────────────────────────────────────────────
# Robot-catalog TCs lifted into Python so they appear in the GUI
# Domain pivot. Each maps to one entry in
# robot/suites/other/20_v2x.robot. The robot side only `Log`s the
# tc_id — these Python siblings do a minimal smoke check (against a
# REST endpoint that obviously exists in the SA Core V2X surface) or
# raise a clear "Python implementation pending" fail when the
# specified procedure needs NAS / PC5 signalling we can't synthesise
# from a tester process.
# ─────────────────────────────────────────────────────────────────────


def _pending(tc, suite_path, tc_id, reason=""):
    """Mark a TC as a pending-Python gap with a clear pointer to the
    Robot suite that owns the spec'd procedure. Surfaces as FAIL so
    the operator sees a real gap rather than a vacuous PASS."""
    msg = (
        f"Python implementation pending — see {suite_path}::{tc_id} "
        f"for the specified procedure."
    )
    if reason:
        msg += f" ({reason})"
    tc.fail_test(msg)


# ─── V2X PDU Session (DNN=v2x) — needs UE NAS signalling, pending ──


class V2xPduSessionDnnV2x(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-010",
        title="V2X PDU Session Establishment on DNN=v2x (5QI=3)",
        spec="TS 23.287 §5.2.2.1",
        domain=Domain.V2X,
        nfs=(NF.AMF, NF.SMF, NF.UPF, NF.PCF),
        slice=Slice.NONE,
        dnn="v2x",
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "pdu-session", "dnn-v2x", "5qi-3"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the V2X-over-Uu PDU Session Establishment on DNN=v2x\n"
            "  (TS 23.287 §5.2.2.1). The default QoS flow must carry\n"
            "  5QI=3 (V2X messages) per TS 23.501 §5.7.4 standardised 5QI.\n"
            "\n"
            "Procedure (TS 23.287 §5.2.2.1 + TS 23.501 §5.7.4)\n"
            "  1. (spec'd) Authorise UE for V2X.\n"
            "  2. UE → AMF → SMF: PDU Session Establishment Request on\n"
            "     DNN=v2x.\n"
            "  3. Verify allocated IP comes from the v2x pool.\n"
            "  4. Verify the default QoS flow's QFI maps to 5QI=3.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/other/20_v2x.robot\n"
            "  ::TC-V2X-010 with reason 'needs UE NAS PDU Session\n"
            "  Establishment'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  UE NAS PDU Session Establishment isn't yet synthesisable\n"
            "  from the Python tester; Robot suite owns it.\n"
            "  UE NAS PDU Session Establishment isn't yet synthesisable from\n"
            "  the Python tester; Robot suite owns it. Pending stub — fails\n"
            "  every run by design."
        ),
    )

    def run(self):
        _pending(self, "robot/suites/other/20_v2x.robot", "TC-V2X-010",
                 "needs UE NAS PDU Session Establishment")
        return self.result


class V2xDualDnnPduSession(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-011",
        title="V2X UE with dual PDU sessions (internet + v2x)",
        spec="TS 23.501 §5.6.1",
        domain=Domain.V2X,
        nfs=(NF.AMF, NF.SMF, NF.UPF),
        slice=Slice.NONE,
        dnn="v2x",
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "pdu-session", "dual-dnn"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Validates multi-DNN concurrent PDU sessions on a V2X UE.\n"
            "  TS 23.501 §5.6.1 allows multiple PDU sessions per UE; the\n"
            "  V2X use case is PSI=1 internet (default 5QI=9) + PSI=2\n"
            "  v2x (5QI=3) sharing the same UE NAS connection.\n"
            "\n"
            "Procedure (TS 23.501 §5.6.1)\n"
            "  1. (spec'd) Authorise UE for V2X.\n"
            "  2. UE → SMF: PDU Session Establishment on DNN=internet\n"
            "     (PSI=1) — default eMBB flow.\n"
            "  3. UE → SMF: PDU Session Establishment on DNN=v2x (PSI=2)\n"
            "     — 5QI=3.\n"
            "  4. Verify both sessions are active and use independent\n"
            "     GTP-U tunnels.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/other/20_v2x.robot\n"
            "  ::TC-V2X-011 with reason 'needs UE NAS dual PDU session\n"
            "  setup'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  Dual PDU session orchestration is unavailable from the\n"
            "  Python tester."
        ),
    )

    def run(self):
        _pending(self, "robot/suites/other/20_v2x.robot", "TC-V2X-011",
                 "needs UE NAS dual PDU session setup")
        return self.result


class V2xNonAuthorizedUeRejected(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-012",
        title="Non-V2X UE rejected on DNN=v2x (negative)",
        spec="TS 23.287 §6.2.2",
        domain=Domain.V2X,
        nfs=(NF.AMF, NF.SMF, NF.UDM),
        slice=Slice.NONE,
        dnn="v2x",
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "negative", "authorization"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative gate: a non-V2X-authorised UE must be rejected\n"
            "  when it tries to establish a PDU session on DNN=v2x. TS\n"
            "  23.287 §6.2.2 places the gate at the SMF; expected reject\n"
            "  cause is 5GSM #33 (service option not subscribed) or #29.\n"
            "\n"
            "Procedure (TS 23.287 §6.2.2 + TS 24.501 §6.4.1.5)\n"
            "  1. (spec'd) UE is NOT V2X-authorised.\n"
            "  2. UE → SMF: PDU Session Establishment Request, DNN=v2x.\n"
            "  3. SMF responds with Establishment Reject, cause #33 or #29.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/other/20_v2x.robot\n"
            "  ::TC-V2X-012 with reason 'needs UE NAS PDU Session\n"
            "  Establishment Reject path'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  Cannot synthesise NAS reject codes from this tester.\n"
            "  Cannot synthesise NAS reject codes from this tester. Pending\n"
            "  stub — fails every run, surfacing the gap to the operator\n"
            "  dashboard.\n"
            "  Once UE NAS PDU est-reject is wired into the Python tester,\n"
            "  this TC can become a real conformance gate."
        ),
    )

    def run(self):
        _pending(self, "robot/suites/other/20_v2x.robot", "TC-V2X-012",
                 "needs UE NAS PDU Session Establishment Reject path")
        return self.result


# ─── V2X QoS flows — also UE-side, pending ──


class V2xQosFlow5Qi3Signaling(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-020",
        title="V2X signaling QoS flow uses 5QI=3 (GBR)",
        spec="TS 23.501 §5.7.4",
        domain=Domain.V2X,
        nfs=(NF.SMF, NF.UPF, NF.PCF),
        slice=Slice.NONE,
        dnn="v2x",
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "qos", "5qi-3", "gbr"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Validates the V2X signalling QoS flow over Uu uses the\n"
            "  standardised 5QI=3 (V2X messages, GBR, 50ms PDB). TS\n"
            "  23.501 §5.7.4 pins this 5QI for V2X signalling traffic;\n"
            "  the data plane must respect the PDB budget.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.4 + TS 23.287 §5.4.4)\n"
            "  1. (spec'd) Establish DNN=v2x PDU session.\n"
            "  2. Verify default QoS flow's QFI is 5QI=3 (GBR, 50ms PDB).\n"
            "  3. Generate synthetic V2X-shaped UDP traffic.\n"
            "  4. Measure delivered PDB against 50ms budget.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/other/20_v2x.robot\n"
            "  ::TC-V2X-020 with reason 'needs active DNN=v2x PDU session\n"
            "  + traffic shaping'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  Active DNN=v2x PDU session + QFI traffic shaping not yet\n"
            "  available from the Python tester.\n"
            "  Active DNN=v2x PDU session + QFI traffic shaping not yet\n"
            "  available from the Python tester. Pending stub — flagged FAIL\n"
            "  to expose the gap."
        ),
    )

    def run(self):
        _pending(self, "robot/suites/other/20_v2x.robot", "TC-V2X-020",
                 "needs active DNN=v2x PDU session + traffic shaping")
        return self.result


class V2xDataQosFlow5Qi80(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-021",
        title="V2X data QoS flow on 5QI=80 (NonGBR)",
        spec="TS 23.501 §5.7.4",
        domain=Domain.V2X,
        nfs=(NF.SMF, NF.UPF, NF.PCF),
        slice=Slice.NONE,
        dnn="v2x",
        severity=Severity.MINOR,
        tags=("conformance", "v2x", "qos", "5qi-80", "nonGBR"),
        setup=Setup.BASELINE,
        expected_duration_s=8.0,
        description=(
            "Purpose\n"
            "  Validates dedicated-bearer activation with 5QI=80 (low-\n"
            "  latency eMBB, NonGBR) for high-bitrate V2X data on an\n"
            "  existing DNN=v2x session. TS 23.501 §5.7.4 row 80 covers\n"
            "  low-latency eMBB; the QoS isolation gate must hold.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.4 + TS 23.502 §4.3.3)\n"
            "  1. (spec'd) Existing DNN=v2x session.\n"
            "  2. SMF triggers PDU Session Modification with new QoS rule\n"
            "     mapping a SDF to 5QI=80 dedicated flow.\n"
            "  3. Validate QoS isolation from the default 5QI=3 bearer.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/other/20_v2x.robot\n"
            "  ::TC-V2X-021 with reason 'needs dedicated QoS flow\n"
            "  modification'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  Dedicated-bearer modification needs UE NAS QoS Rule\n"
            "  handling that the Python tester doesn't yet drive.\n"
            "  Dedicated-bearer modification needs UE NAS QoS Rule handling\n"
            "  that the Python tester doesn't yet drive. Pending stub —\n"
            "  flagged FAIL."
        ),
    )

    def run(self):
        _pending(self, "robot/suites/other/20_v2x.robot", "TC-V2X-021",
                 "needs dedicated QoS flow modification")
        return self.result


# ─── PQI Table provisioning — checkable via /api/v2x/service-types ──


class V2xPqiTableFullProvisioned(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-030",
        title="V2X PQI table is fully provisioned (Table 5.4.4-1)",
        spec="TS 23.287 §5.4.4",
        domain=Domain.V2X,
        nfs=(NF.PCF, NF.SMF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "pqi", "config"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Stronger version of TC-V2X-001: asserts the full standardised\n"
            "  Table 5.4.4-1 row-set is present (21-23 GBR, 55-59 NonGBR,\n"
            "  90-91 DelCritGBR), not just the smoke-set five.\n"
            "\n"
            "Procedure (TS 23.287 §5.4.4 Table 5.4.4-1)\n"
            "  1. GET /api/v2x/service-types.\n"
            "  2. fail_test if status != 200 or response not a list.\n"
            "  3. Build pqis = set of row.pqi values.\n"
            "  4. Required = {21, 22, 23, 55, 56, 57, 58, 59, 90, 91}.\n"
            "     fail_test if any missing (with present list shown).\n"
            "  5. For each row whose pqi is in `required`, fail_test if\n"
            "     resource_type not in (GBR, NonGBR, DelCritGBR).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  All 10 standardised PQIs present AND each has a valid\n"
            "  resource_type.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rows (count), pqis (sorted int list).\n"
            "\n"
            "Known constraints\n"
            "  PQI 24-26 / 82-83 / 92-93 are in the deployment-policy\n"
            "  extension range and are NOT asserted here — only the core\n"
            "  10 row-set.\n"
            "  PQI 24-26 / 82-83 / 92-93 are in the deployment-policy\n"
            "  extension range and are NOT asserted here — only the core 10\n"
            "  row-set required by TS 23.287 Table 5.4.4-1."
        ),
    )

    def run(self):
        try:
            rows, status = _v2x_api("/api/v2x/service-types")
            if status != 200 or not isinstance(rows, list):
                self.fail_test(f"service-types fetch failed: {status} {rows}")
                return self.result
            pqis = {row.get("pqi") for row in rows}
            # Table 5.4.4-1 row set. PQI 24-26 / 92-93 / 82-83 are in
            # the deployment-policy extension range — we assert the
            # core 10 standardised rows.
            required = {21, 22, 23, 55, 56, 57, 58, 59, 90, 91}
            missing = required - pqis
            if missing:
                self.fail_test(
                    f"missing standardised PQIs: {sorted(missing)}",
                    present=sorted(p for p in pqis if isinstance(p, int)),
                )
                return self.result
            for row in rows:
                if row.get("pqi") in required:
                    if row.get("resource_type") not in ("GBR", "NonGBR", "DelCritGBR"):
                        self.fail_test(
                            f"PQI {row.get('pqi')} has invalid "
                            f"resource_type {row.get('resource_type')!r}"
                        )
                        return self.result
            self.pass_test(rows=len(rows), pqis=sorted(p for p in pqis if isinstance(p, int)))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class V2xPqi90CollisionAvoidance(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-031",
        title="PQI 90 — Cooperative Collision Avoidance characteristics",
        spec="TS 23.287 §5.4.4",
        domain=Domain.V2X,
        nfs=(NF.PCF, NF.SMF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "pqi", "collision-avoidance", "delay-critical"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Asserts PQI 90 — Cooperative Collision Avoidance — is in\n"
            "  the V2X catalog and carries resource_type=DelCritGBR per\n"
            "  TS 23.287 Table 5.4.4-1. Collision-avoidance traffic is\n"
            "  delay-critical, so this row underpins V2X safety services.\n"
            "\n"
            "Procedure (TS 23.287 §5.4.4 Table 5.4.4-1)\n"
            "  1. GET /api/v2x/service-types; fail_test if status != 200\n"
            "     or response not a list.\n"
            "  2. Find row with pqi == 90; fail_test if absent.\n"
            "  3. fail_test if row.resource_type != 'DelCritGBR' (the row\n"
            "     is logged with the test failure for diagnostics).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  PQI 90 row is present AND resource_type == 'DelCritGBR'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pqi (90), row (the full row).\n"
            "\n"
            "Known constraints\n"
            "  Priority/PDB/PER values from Table 5.4.4-1 (3 / 10ms /\n"
            "  1e-4) are noted in the spec but NOT asserted here — only\n"
            "  the resource_type.\n"
            "  Priority/PDB/PER values from Table 5.4.4-1 (3 / 10ms / 1e-4)\n"
            "  are noted in the spec but NOT asserted here — only the\n"
            "  resource_type. Tighter assertion is operator policy.\n"
            "  Operator may add a strict-PDB assert variant if the QoS\n"
            "  shape needs to be locked."
        ),
    )

    def run(self):
        try:
            rows, status = _v2x_api("/api/v2x/service-types")
            if status != 200 or not isinstance(rows, list):
                self.fail_test(f"service-types fetch failed: {status}")
                return self.result
            row = next((r for r in rows if r.get("pqi") == 90), None)
            if not row:
                self.fail_test("PQI 90 not present in service-types")
                return self.result
            if row.get("resource_type") != "DelCritGBR":
                self.fail_test(
                    f"PQI 90 resource_type expected DelCritGBR, "
                    f"got {row.get('resource_type')!r}",
                    row=row,
                )
                return self.result
            self.pass_test(pqi=90, row=row)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class V2xPqi91EmergencyTrajectory(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-032",
        title="PQI 91 — Emergency Trajectory Alignment characteristics",
        spec="TS 23.287 §5.4.4",
        domain=Domain.V2X,
        nfs=(NF.PCF, NF.SMF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "pqi", "emergency", "delay-critical"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Asserts PQI 91 — Emergency Trajectory Alignment — is in\n"
            "  the V2X catalog with resource_type=DelCritGBR. PQI 91 is\n"
            "  the highest-priority V2X service in Table 5.4.4-1 (prio=2,\n"
            "  PDB=3ms, PER=1e-5) and the row MUST exist.\n"
            "\n"
            "Procedure (TS 23.287 §5.4.4 Table 5.4.4-1)\n"
            "  1. GET /api/v2x/service-types; fail_test if status != 200\n"
            "     or response not a list.\n"
            "  2. Find row with pqi == 91; fail_test if absent.\n"
            "  3. fail_test if row.resource_type != 'DelCritGBR'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  PQI 91 row present AND resource_type == 'DelCritGBR'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pqi (91), row.\n"
            "\n"
            "Known constraints\n"
            "  Priority=2 / PDB=3ms / PER=1e-5 values are not strictly\n"
            "  asserted (Table 5.4.4-1 leaves some operator latitude\n"
            "  for tighter values).\n"
            "  Priority=2 / PDB=3ms / PER=1e-5 values are not strictly\n"
            "  asserted (Table 5.4.4-1 leaves some operator latitude for\n"
            "  tighter values). Row presence + resource_type are the gate.\n"
            "  Some operators set PER as low as 1e-6 for emergency calls;\n"
            "  the TC stays tolerant of that."
        ),
    )

    def run(self):
        try:
            rows, status = _v2x_api("/api/v2x/service-types")
            if status != 200 or not isinstance(rows, list):
                self.fail_test(f"service-types fetch failed: {status}")
                return self.result
            row = next((r for r in rows if r.get("pqi") == 91), None)
            if not row:
                self.fail_test("PQI 91 not present in service-types")
                return self.result
            if row.get("resource_type") != "DelCritGBR":
                self.fail_test(
                    f"PQI 91 resource_type expected DelCritGBR, "
                    f"got {row.get('resource_type')!r}",
                    row=row,
                )
                return self.result
            self.pass_test(pqi=91, row=row)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── PCF V2X Policy Association ──


class V2xPolicyAssociationCreate(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-040",
        title="V2X Policy Association created during registration",
        spec="TS 29.486 §5.4",
        domain=Domain.V2X,
        nfs=(NF.AMF, NF.PCF, NF.UDM, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "pcf", "policy", "npcf-v2x"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  REST smoke-check for the V2X Policy Association content.\n"
            "  TS 29.486 §5.4 (Npcf_V2XPolicy) carries the same container\n"
            "  the policy/provision route returns; if that container is\n"
            "  intact the PCF can wire it into an actual Policy\n"
            "  Association during AMF-anchored UE registration.\n"
            "\n"
            "Procedure (TS 29.486 §5.4 + TS 23.287 §6.2.3)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/v2x/authorize (vehicle, 50000).\n"
            "  3. POST /api/v2x/authorized-plmns {imsi, plmn=00101}.\n"
            "  4. POST /api/v2x/policy/provision {imsi}; fail_test on\n"
            "     status != 200 or ok falsy.\n"
            "  5. fail_test if 'auth_policy' not in returned policy keys.\n"
            "  6. finally: DELETE /authorized-plmns 00101.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Provision returns 200/ok=True AND auth_policy is present.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, policy_keys.\n"
            "\n"
            "Known constraints\n"
            "  End-to-end AMF→PCF Policy Association binding lives in the\n"
            "  Robot suite; this test only exercises the container.\n"
            "  End-to-end AMF→PCF Policy Association binding lives in the\n"
            "  Robot suite; this test only exercises the container content,\n"
            "  not the Npcf_V2XPolicy signalling path."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi
            _v2x_api("/api/v2x/authorize", "POST", {
                "imsi": imsi, "ue_type": "vehicle", "pc5_ambr_kbps": 50000,
            })
            _v2x_api("/api/v2x/authorized-plmns", "POST",
                     {"imsi": imsi, "plmn_id": "00101"})
            res, status = _v2x_api("/api/v2x/policy/provision", "POST", {"imsi": imsi})
            if status != 200 or not res.get("ok"):
                self.fail_test(f"provision failed: {status} {res}")
                return self.result
            policy = res.get("policy") or {}
            if "auth_policy" not in policy:
                self.fail_test(
                    "policy/provision response missing auth_policy",
                    keys=list(policy.keys()),
                )
                return self.result
            self.pass_test(imsi=imsi, policy_keys=list(policy.keys()))
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            try:
                _v2x_api(f"/api/v2x/authorized-plmns?imsi={imsi}&plmn_id=00101",
                         "DELETE")
            except Exception:
                pass
        return self.result


class V2xPolicyAssociationDelete(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-041",
        title="V2X Policy Association deleted on deregistration",
        spec="TS 29.486 §5.4",
        domain=Domain.V2X,
        nfs=(NF.AMF, NF.PCF, NF.UDM, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MINOR,
        tags=("conformance", "v2x", "pcf", "policy", "deregistration"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates the deauth→deny-policy edge of the V2X Policy\n"
            "  Association. TS 29.486 §5.4 subscription DELETE makes the\n"
            "  PCF stop serving V2X policy; the REST analogue is a 403 on\n"
            "  /policy/provision after /deauthorize.\n"
            "\n"
            "Procedure (TS 29.486 §5.4)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/v2x/deauthorize {imsi}.\n"
            "  3. POST /api/v2x/policy/provision {imsi}.\n"
            "  4. fail_test if status != 403.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Provision after deauth returns HTTP 403.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, denied_status (the 403 value).\n"
            "\n"
            "Known constraints\n"
            "  Leaves UE in deauthorised state — tests that follow must\n"
            "  re-authorise as needed.\n"
            "  Leaves UE in deauthorised state — tests that follow must\n"
            "  re-authorise as needed. The /deauthorize → 403 invariant is\n"
            "  the actual gate.\n"
            "  Operator may re-authorise the UE before subsequent tests\n"
            "  to avoid surprise denials.\n"
            "  State leakage is intentional."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi
            _v2x_api("/api/v2x/deauthorize", "POST", {"imsi": imsi})
            _, status = _v2x_api("/api/v2x/policy/provision", "POST", {"imsi": imsi})
            if status != 403:
                self.fail_test(
                    f"expected 403 after deauth, got {status}",
                    imsi=imsi,
                )
                return self.result
            self.pass_test(imsi=imsi, denied_status=status)
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── V2X Configuration API ──


class V2xConfigApiRead(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-050",
        title="GET /api/v2x/config returns V2X configuration entries",
        spec="TS 23.287 §5.1.2",
        domain=Domain.V2X,
        nfs=(NF.PCF, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "api", "config"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Read-side smoke on the operator V2X configuration block.\n"
            "  TS 23.287 §5.1.2 surfaces v2x_enabled, pc5_nr_enabled, and\n"
            "  ue_pc5_ambr_kbps as the gating knobs; the config endpoint\n"
            "  must expose at least one of those keys.\n"
            "\n"
            "Procedure (TS 23.287 §5.1.2)\n"
            "  1. GET /api/v2x/config; fail_test if status != 200.\n"
            "  2. Collect keys, tolerating both a flat dict shape and the\n"
            "     {items: [{key,value}, ...]} envelope.\n"
            "  3. expected_any = {v2x_enabled, pc5_nr_enabled,\n"
            "     ue_pc5_ambr_kbps}.\n"
            "  4. fail_test if none of expected_any intersects keys.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  GET 200 AND at least one of the three expected keys is\n"
            "  surfaced.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  present_keys (string-only sorted list).\n"
            "\n"
            "Known constraints\n"
            "  Tolerant of any of three envelope shapes — operator may\n"
            "  ship the config as a flat dict, an items list, or a\n"
            "  config list.\n"
            "  Tolerant of any of three envelope shapes — operator may ship\n"
            "  the config as a flat dict, an items list, or a config list.\n"
            "  Key presence is the only gate."
        ),
    )

    def run(self):
        try:
            res, status = _v2x_api("/api/v2x/config")
            if status != 200:
                self.fail_test(f"config GET failed: {status} {res}")
                return self.result
            # Tolerate either a flat dict or a {items: [...]} envelope.
            keys = set()
            if isinstance(res, dict):
                keys.update(res.keys())
                items = res.get("items") or res.get("config")
                if isinstance(items, list):
                    for entry in items:
                        if isinstance(entry, dict) and "key" in entry:
                            keys.add(entry["key"])
                        elif isinstance(entry, dict):
                            keys.update(entry.keys())
            expected_any = {"v2x_enabled", "pc5_nr_enabled", "ue_pc5_ambr_kbps"}
            if not (expected_any & keys):
                self.fail_test(
                    f"none of {sorted(expected_any)} present in config",
                    present=sorted(keys),
                )
                return self.result
            self.pass_test(present_keys=sorted(k for k in keys if isinstance(k, str)))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class V2xConfigApiUpdate(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-051",
        title="PUT /api/v2x/config persists updated value",
        spec="TS 23.287 §5.1.2",
        domain=Domain.V2X,
        nfs=(NF.PCF, NF.AF),
        slice=Slice.NONE,
        severity=Severity.MINOR,
        tags=("conformance", "v2x", "api", "config", "update"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the operator-facing V2X config write path. The\n"
            "  ue_pc5_ambr_kbps value carried in the §5.1.2 container is\n"
            "  tunable; a PUT must round-trip through the GET surface.\n"
            "\n"
            "Procedure (TS 23.287 §5.1.2)\n"
            "  1. PUT /api/v2x/config with {key: ue_pc5_ambr_kbps,\n"
            "     value: '100000'}. fail_test if status not in (200,\n"
            "     201, 204).\n"
            "  2. GET /api/v2x/config; fail_test if status != 200.\n"
            "  3. Locate ue_pc5_ambr_kbps in either flat dict or items\n"
            "     list envelope.\n"
            "  4. fail_test if str(found_val) != '100000'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — write value pinned to 100000 kbps.\n"
            "\n"
            "Pass criteria\n"
            "  PUT 2xx AND GET reflects new value.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  value (the stringified found_val).\n"
            "\n"
            "Known constraints\n"
            "  Mutates global V2X config — could affect other tests.\n"
            "  Does not reset to a prior value.\n"
            "  Mutates global V2X config — could affect other tests. Does\n"
            "  not reset to a prior value on teardown; operator is expected\n"
            "  to provision baseline before regression.\n"
            "  Operator should consider running TC-V2X-050 before to\n"
            "  establish a baseline value to compare to."
        ),
    )

    def run(self):
        try:
            _, put_status = _v2x_api("/api/v2x/config", "PUT", {
                "key": "ue_pc5_ambr_kbps", "value": "100000",
            })
            if put_status not in (200, 201, 204):
                self.fail_test(f"config PUT failed: {put_status}")
                return self.result
            res, status = _v2x_api("/api/v2x/config")
            if status != 200:
                self.fail_test(f"config GET after PUT failed: {status}")
                return self.result
            # Locate the value regardless of envelope shape.
            found_val = None
            if isinstance(res, dict):
                if "ue_pc5_ambr_kbps" in res:
                    found_val = res["ue_pc5_ambr_kbps"]
                else:
                    for entry in (res.get("items") or res.get("config") or []):
                        if isinstance(entry, dict) and entry.get("key") == "ue_pc5_ambr_kbps":
                            found_val = entry.get("value")
                            break
            if str(found_val) != "100000":
                self.fail_test(
                    f"ue_pc5_ambr_kbps not persisted: got {found_val!r}",
                )
                return self.result
            self.pass_test(value=found_val)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class V2xServiceTypesApi(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-052",
        title="GET /api/v2x/service-types returns full PQI catalog",
        spec="TS 23.287 §5.4.4",
        domain=Domain.V2X,
        nfs=(NF.PCF, NF.SMF),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "api", "pqi", "service-types"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Schema-shape gate on the /service-types row contract that\n"
            "  the GUI PQI editor consumes. TS 23.287 §5.4.4 names pqi,\n"
            "  service_name, resource_type and packet_delay_ms as the\n"
            "  column set every row must surface.\n"
            "\n"
            "Procedure (TS 23.287 §5.4.4)\n"
            "  1. GET /api/v2x/service-types; fail_test if status != 200\n"
            "     or response not a list.\n"
            "  2. fail_test if len(rows) < 5 (smoke-set sanity).\n"
            "  3. required_cols = {pqi, service_name, resource_type,\n"
            "     packet_delay_ms}.\n"
            "  4. For each row, fail_test if any required column is\n"
            "     missing (failure body carries the offending row).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Row count >= 5 AND every row carries all four columns.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rows (count).\n"
            "\n"
            "Known constraints\n"
            "  Doesn't assert value types (pqi being int, etc.) — only\n"
            "  key presence.\n"
            "  Doesn't assert value types (pqi being int, etc.) — only key\n"
            "  presence. PQI numerical sanity is left to TC-V2X-001 and\n"
            "  TC-V2X-030."
        ),
    )

    def run(self):
        try:
            rows, status = _v2x_api("/api/v2x/service-types")
            if status != 200 or not isinstance(rows, list):
                self.fail_test(f"service-types fetch failed: {status}")
                return self.result
            if len(rows) < 5:
                self.fail_test(f"expected ≥5 PQI rows, got {len(rows)}")
                return self.result
            required_cols = {"pqi", "service_name", "resource_type", "packet_delay_ms"}
            for row in rows:
                missing = required_cols - set(row.keys())
                if missing:
                    self.fail_test(
                        f"row pqi={row.get('pqi')} missing cols {sorted(missing)}",
                        row=row,
                    )
                    return self.result
            self.pass_test(rows=len(rows))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class V2xSubscribersApi(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-053",
        title="GET /api/v2x/subscribers lists V2X-authorised UEs",
        spec="TS 23.287 §5.5",
        domain=Domain.V2X,
        nfs=(NF.UDM, NF.PCF),
        slice=Slice.NONE,
        severity=Severity.MINOR,
        tags=("conformance", "v2x", "api", "subscribers"),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the subscription-listing view the operator UI\n"
            "  reads. After authorisation the UE must surface in the\n"
            "  /subscribers feed (§5.5 subscription data).\n"
            "\n"
            "Procedure (TS 23.287 §5.5)\n"
            "  1. require_ue() → imsi.\n"
            "  2. POST /api/v2x/authorize (vehicle, 50000).\n"
            "  3. GET /api/v2x/subscribers; fail_test if status != 200.\n"
            "  4. Tolerate list / items / subscribers envelope shapes.\n"
            "  5. fail_test if envelope is not a list shape.\n"
            "  6. Find subscriber with imsi == self.imsi; fail_test if\n"
            "     missing.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  GET 200 AND authorised IMSI is listed in /subscribers.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, subscriber (full row), total (list length).\n"
            "\n"
            "Known constraints\n"
            "  Authorisation state persists after the test. Subscriber\n"
            "  row schema (v2x_ue_type / pc5_ambr) is not strictly\n"
            "  asserted — only IMSI presence.\n"
            "  Authorisation state persists after the test. Subscriber row\n"
            "  schema (v2x_ue_type / pc5_ambr) is not strictly asserted —\n"
            "  only IMSI presence in the list."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi
            _v2x_api("/api/v2x/authorize", "POST", {
                "imsi": imsi, "ue_type": "vehicle", "pc5_ambr_kbps": 50000,
            })
            res, status = _v2x_api("/api/v2x/subscribers")
            if status != 200:
                self.fail_test(f"subscribers GET failed: {status} {res}")
                return self.result
            # Accept either list or {items: [...]}/{subscribers: [...]} envelope.
            items = res if isinstance(res, list) else (
                res.get("items") or res.get("subscribers") or []
            )
            if not isinstance(items, list):
                self.fail_test(f"unexpected subscribers payload: {res}")
                return self.result
            found = next((s for s in items if s.get("imsi") == imsi), None)
            if not found:
                self.fail_test(
                    f"authorised IMSI {imsi} not listed",
                    subscriber_count=len(items),
                )
                return self.result
            self.pass_test(imsi=imsi, subscriber=found, total=len(items))
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─── CM-IDLE / Service Request — UE NAS, pending ──


class V2xSessionPreservedInIdle(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-060",
        title="V2X PDU session survives CM-IDLE → Service Request",
        spec="TS 23.502 §4.2.3.2",
        domain=Domain.V2X,
        nfs=(NF.AMF, NF.SMF, NF.UPF),
        slice=Slice.NONE,
        dnn="v2x",
        severity=Severity.MAJOR,
        tags=("conformance", "v2x", "idle-mode", "service-request",
              "session-preservation"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Validates V2X PDU session survival across CM-CONNECTED →\n"
            "  CM-IDLE → CM-CONNECTED. TS 23.502 §4.2.3.2 (Service Request\n"
            "  procedure) reactivates the user-plane on demand; the v2x\n"
            "  session, IP and QoS flows must come back without renewal.\n"
            "\n"
            "Procedure (TS 23.502 §4.2.3.2)\n"
            "  1. (spec'd) Establish DNN=v2x PDU session, capture IP.\n"
            "  2. Drive gNB context release → UE enters CM-IDLE.\n"
            "  3. UE sends Service Request → CM-CONNECTED.\n"
            "  4. Verify DNN=v2x session is reactivated with same IP\n"
            "     and QoS flow set.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/other/20_v2x.robot\n"
            "  ::TC-V2X-060 with reason 'needs UE NAS CM-IDLE / Service\n"
            "  Request cycle'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  CM-IDLE / Service Request cycle requires real UE NAS\n"
            "  signalling that the Python tester doesn't provide.\n"
            "  CM-IDLE / Service Request cycle requires real UE NAS\n"
            "  signalling that the Python tester doesn't provide. Pending\n"
            "  stub — fails by design."
        ),
    )

    def run(self):
        _pending(self, "robot/suites/other/20_v2x.robot", "TC-V2X-060",
                 "needs UE NAS CM-IDLE / Service Request cycle")
        return self.result


class V2xDualDnnPreservedInIdle(TestCase):
    SPEC = TestSpec(
        tc_id="TC-V2X-061",
        title="Dual DNN (internet + v2x) preserved across CM-IDLE",
        spec="TS 23.502 §4.2.3.2",
        domain=Domain.V2X,
        nfs=(NF.AMF, NF.SMF, NF.UPF),
        slice=Slice.NONE,
        severity=Severity.MINOR,
        tags=("conformance", "v2x", "idle-mode", "dual-dnn"),
        setup=Setup.BASELINE,
        expected_duration_s=12.0,
        description=(
            "Purpose\n"
            "  Extends TC-V2X-060 to multi-session: a UE with PSI=1\n"
            "  (internet) AND PSI=2 (v2x) must have BOTH DRBs reactivated\n"
            "  by a single Service Request after CM-IDLE — TS 23.502\n"
            "  §4.2.3.2 plus the §5.6.1 multi-session model.\n"
            "\n"
            "Procedure (TS 23.502 §4.2.3.2 + TS 23.501 §5.6.1)\n"
            "  1. (spec'd) Establish PSI=1 on DNN=internet (5QI=9).\n"
            "  2. Establish PSI=2 on DNN=v2x (5QI=3).\n"
            "  3. Drive UE to CM-IDLE.\n"
            "  4. UE sends Service Request → CM-CONNECTED.\n"
            "  5. Verify both DRBs are restored and both PDU sessions\n"
            "     resume with their original anchors.\n"
            "  Actual implementation today: only calls _pending() which\n"
            "  records a FAIL pointing at robot/suites/other/20_v2x.robot\n"
            "  ::TC-V2X-061 with reason 'needs UE NAS dual PDU session +\n"
            "  CM-IDLE cycle'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none.\n"
            "\n"
            "Pass criteria\n"
            "  Never passes from Python today — _pending() always fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pending pointer message recorded as failure reason.\n"
            "\n"
            "Known constraints\n"
            "  Compound prerequisite — dual PDU session AND CM-IDLE\n"
            "  cycle — neither of which the Python tester drives."
        ),
    )

    def run(self):
        _pending(self, "robot/suites/other/20_v2x.robot", "TC-V2X-061",
                 "needs UE NAS dual PDU session + CM-IDLE cycle")
        return self.result


# ─────────────────────────────────────────────────────────────────────


ALL_V2X_TCS = [
    V2XServiceTypesList,
    V2XServiceTypeCRUD,
    V2XAuthorizeUE,
    V2XPC5QoSQuery,
    V2XAuthorizedPLMNs,
    V2XPolicyProvision,
    V2XPolicyLogAudit,
    # Robot-catalog imports
    V2xPduSessionDnnV2x,
    V2xDualDnnPduSession,
    V2xNonAuthorizedUeRejected,
    V2xQosFlow5Qi3Signaling,
    V2xDataQosFlow5Qi80,
    V2xPqiTableFullProvisioned,
    V2xPqi90CollisionAvoidance,
    V2xPqi91EmergencyTrajectory,
    V2xPolicyAssociationCreate,
    V2xPolicyAssociationDelete,
    V2xConfigApiRead,
    V2xConfigApiUpdate,
    V2xServiceTypesApi,
    V2xSubscribersApi,
    V2xSessionPreservedInIdle,
    V2xDualDnnPreservedInIdle,
]

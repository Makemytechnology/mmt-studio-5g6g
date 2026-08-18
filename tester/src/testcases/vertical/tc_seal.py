# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: SEAL (Service Enabler Architecture Layer).

TS 23.434 — Group management, location, configuration, identity mapping.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src import baseline

log = logging.getLogger("tester.tc_seal")


def _seal_api(path, method="GET", body=None):
    """Call SA Core SEAL REST API."""
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


class SealCreateGroup(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SEAL-001",
        title="Create a SEAL group and add a member",
        spec="TS 23.434 §10",
        domain=Domain.MCX,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Smoke CRUD on the SEAL Group Management Server (GMS). TS\n"
            "  23.434 §10 sets the GMS as the shared group registry for\n"
            "  VAL services (MCPTT/MCVideo/MCData/V2X). Without working\n"
            "  CRUD here, every overlying VAL group call is undefined.\n"
            "\n"
            "Procedure (TS 23.434 §10.3)\n"
            "  1. POST /api/seal/groups with name='test-seal-group-001'.\n"
            "  2. fail_test if status not in (200, 201).\n"
            "  3. Extract group_id (id or group_id).\n"
            "  4. POST /api/seal/groups/{id}/members with a single admin\n"
            "     row (imsi = baseline eMBB UE #0, role='admin').\n"
            "  5. fail_test if member POST not in (200, 201).\n"
            "  6. GET /api/seal/groups/{id} to verify.\n"
            "  7. finally: DELETE the group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — group name + member IMSI are pinned.\n"
            "\n"
            "Pass criteria\n"
            "  Both group POST and member POST succeed; group readback 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  group_id, group, members.\n"
            "\n"
            "Known constraints\n"
            "  No VAL service traffic — pure registry CRUD.\n"
            "  Pure registry CRUD — no VAL service traffic is generated.\n"
            "  Group is reaped on finally so the GMS does not accrete rows\n"
            "  across test runs."
        ),
    )

    def run(self):
        group_id = None
        try:
            result, status = _seal_api("/api/seal/groups", "POST", {
                "name": "test-seal-group-001",
            })
            if status not in (200, 201):
                self.fail_test(f"Group creation failed: {status} {result}")
                return self.result

            group_id = result.get("id") or result.get("group_id")
            log.info("SEAL group created: id=%s", group_id)

            # Add a member
            mem_result, mem_status = _seal_api(
                f"/api/seal/groups/{group_id}/members", "POST",
                [{"imsi": baseline.imsi("embb-bulk", 0), "role": "admin"}],
            )
            if mem_status not in (200, 201):
                self.fail_test(f"Add member failed: {mem_status} {mem_result}")
                return self.result

            # Verify
            grp, g_status = _seal_api(f"/api/seal/groups/{group_id}")
            if g_status != 200:
                self.fail_test(f"Group query failed: {g_status}")
                return self.result

            self.pass_test(group_id=group_id, group=grp, members=mem_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if group_id:
                _seal_api(f"/api/seal/groups/{group_id}", "DELETE")
        return self.result


class SealGroupMembers(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SEAL-002",
        title="Add multiple SEAL group members with distinct roles",
        spec="TS 23.434 §10",
        domain=Domain.MCX,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the role-aware bulk-member-add path against the\n"
            "  GMS. TS 23.434 §10.3.2 mandates per-member role assignment\n"
            "  (administrator / member / non-member-observer flavours),\n"
            "  which downstream MCPTT permission gating reads.\n"
            "\n"
            "Procedure (TS 23.434 §10.3.2)\n"
            "  1. POST /api/seal/groups with name='test-seal-group-002'.\n"
            "  2. Capture group_id.\n"
            "  3. POST /api/seal/groups/{id}/members with 3 rows:\n"
            "     [{imsi=embb-bulk[0], role=admin},\n"
            "      {imsi=embb-bulk[1], role=member},\n"
            "      {imsi=embb-bulk[2], role=viewer}].\n"
            "  4. fail_test if member POST not in (200, 201).\n"
            "  5. GET /api/seal/groups/{id}/members and capture items.\n"
            "  6. finally: DELETE the group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — roles fixed (admin/member/viewer).\n"
            "\n"
            "Pass criteria\n"
            "  Bulk member POST 200/201 AND listing GET 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  group_id, member_count, members, added.\n"
            "\n"
            "Known constraints\n"
            "  No assertion on which roles persist — only count + 200.\n"
            "  No assertion on which roles persist — only count + HTTP 200.\n"
            "  Role-based permission gating is the responsibility of the\n"
            "  consuming VAL service (e.g. MCPTT)."
        ),
    )

    def run(self):
        group_id = None
        try:
            result, status = _seal_api("/api/seal/groups", "POST", {
                "name": "test-seal-group-002",
            })
            if status not in (200, 201):
                self.fail_test(f"Group creation failed: {status} {result}")
                return self.result

            group_id = result.get("id") or result.get("group_id")

            # Add 3 members with different roles
            members = [
                {"imsi": baseline.imsi("embb-bulk", 0), "role": "admin"},
                {"imsi": baseline.imsi("embb-bulk", 1), "role": "member"},
                {"imsi": baseline.imsi("embb-bulk", 2), "role": "viewer"},
            ]
            mem_result, mem_status = _seal_api(
                f"/api/seal/groups/{group_id}/members", "POST", members,
            )
            if mem_status not in (200, 201):
                self.fail_test(f"Add members failed: {mem_status} {mem_result}")
                return self.result
            log.info("Added 3 members to group %s", group_id)

            # Verify members
            mem_list, ml_status = _seal_api(f"/api/seal/groups/{group_id}/members")
            if ml_status != 200:
                self.fail_test(f"Members query failed: {ml_status}")
                return self.result

            items = mem_list.get("members") or mem_list.get("items") or []
            log.info("Group has %d members", len(items))

            self.pass_test(
                group_id=group_id, member_count=len(items),
                members=items, added=mem_result,
            )
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if group_id:
                _seal_api(f"/api/seal/groups/{group_id}", "DELETE")
        return self.result


class SealLocation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SEAL-003",
        title="SEAL Location Management subscription lifecycle",
        spec="TS 23.434 §11",
        domain=Domain.MCX,
        nfs=(NF.NEF, NF.AF, NF.LMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Smoke CRUD on the SEAL Location Management Server (LMS).\n"
            "  TS 23.434 §11 places the LMS as the location-information\n"
            "  broker for VAL services (e.g. MCPTT dispatcher console).\n"
            "  A subscription is the canonical pull primitive.\n"
            "\n"
            "Procedure (TS 23.434 §11.3.2)\n"
            "  1. require_ue() — pull first UE from pool (LMS uses IMSI).\n"
            "  2. POST /api/seal/location/subscriptions with\n"
            "     target_type='imsi', target_id=imsi,\n"
            "     callback_url=http://192.168.1.103:8080/seal/location,\n"
            "     interval_s=60.\n"
            "  3. fail_test if status not in (200, 201).\n"
            "  4. GET /api/seal/location/subscriptions.\n"
            "  5. finally: DELETE the subscription if created.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — callback URL / interval are pinned.\n"
            "\n"
            "Pass criteria\n"
            "  Subscription POST 200/201 AND list GET 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, subscription_id, subscription, subscriptions.\n"
            "\n"
            "Known constraints\n"
            "  Callback is not actually invoked — endpoint not exercised.\n"
            "  Callback is not actually invoked — the test only proves the\n"
            "  LMS accepts the subscription record. Triggering a location\n"
            "  event is out of scope here."
        ),
    )

    def run(self):
        sub_id = None
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            result, status = _seal_api("/api/seal/location/subscriptions", "POST", {
                "target_type": "imsi",
                "target_id": imsi,
                "callback_url": "http://192.168.1.103:8080/seal/location",
                "interval_s": 60,
            })
            if status not in (200, 201):
                self.fail_test(f"Location subscription failed: {status} {result}")
                return self.result

            sub_id = result.get("id") or result.get("subscription_id")
            log.info("Location subscription created: id=%s", sub_id)

            # Verify
            subs, s_status = _seal_api("/api/seal/location/subscriptions")
            if s_status != 200:
                self.fail_test(f"Subscription list failed: {s_status}")
                return self.result

            self.pass_test(
                imsi=imsi, subscription_id=sub_id,
                subscription=result, subscriptions=subs,
            )
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if sub_id:
                _seal_api(f"/api/seal/location/subscriptions/{sub_id}", "DELETE")
        return self.result


class SealConfig(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SEAL-004",
        title="SEAL Configuration Management for a UE",
        spec="TS 23.434 §13",
        domain=Domain.MCX,
        nfs=(NF.NEF, NF.AF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Smoke on the SEAL Configuration Management Server (CMS).\n"
            "  TS 23.434 §13 makes the CMS the place where VAL-service\n"
            "  user-config (radio params, codec choice, etc.) is staged\n"
            "  for later pull-down by the UE.\n"
            "\n"
            "Procedure (TS 23.434 §13.3)\n"
            "  1. require_ue() — first UE in pool.\n"
            "  2. POST /api/seal/configs with target_type='imsi',\n"
            "     target_id=imsi, config_key='max_tx_power',\n"
            "     config_value='23'.\n"
            "  3. fail_test if status not in (200, 201).\n"
            "  4. GET /api/seal/configs?target_id=imsi.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — config key/value are pinned (TX-power=23 dBm).\n"
            "\n"
            "Pass criteria\n"
            "  Config POST 200/201 AND list GET 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, config_id, config, configs.\n"
            "\n"
            "Known constraints\n"
            "  Config is not actually fetched by a UE; only the staging\n"
            "  endpoint is exercised.\n"
            "  Config is not actually fetched by a UE; only the staging\n"
            "  endpoint is exercised. The applied-on-UE feedback loop is\n"
            "  left to TC-SEAL-005 / TC-SEAL-006.\n"
            "  Multiple configs per target_id are allowed by the schema\n"
            "  but this TC writes only one."
        ),
    )

    def run(self):
        config_id = None
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            result, status = _seal_api("/api/seal/configs", "POST", {
                "target_type": "imsi",
                "target_id": imsi,
                "config_key": "max_tx_power",
                "config_value": "23",
            })
            if status not in (200, 201):
                self.fail_test(f"Config set failed: {status} {result}")
                return self.result

            config_id = result.get("id") or result.get("config_id")
            log.info("SEAL config set: id=%s", config_id)

            # Verify
            configs, c_status = _seal_api(f"/api/seal/configs?target_id={imsi}")
            if c_status != 200:
                self.fail_test(f"Config query failed: {c_status}")
                return self.result

            self.pass_test(
                imsi=imsi, config_id=config_id,
                config=result, configs=configs,
            )
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SealIdentity(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SEAL-005",
        title="SEAL identity mapping VAL user ID to IMSI",
        spec="TS 23.434 §12",
        domain=Domain.MCX,
        nfs=(NF.NEF, NF.AF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Validates the forward direction of the SEAL Identity\n"
            "  Management Server (IdMS): given a VAL-layer user identity\n"
            "  the IdMS must yield the bound 3GPP-layer IMSI. This is the\n"
            "  glue between application-level identities and the subscriber.\n"
            "\n"
            "Procedure (TS 23.434 §12.3)\n"
            "  1. require_ue() to obtain a real IMSI.\n"
            "  2. Construct val_user_id = 'val-user-{last4(imsi)}'.\n"
            "  3. POST /api/seal/identity/mappings with val_user_id +\n"
            "     imsi.\n"
            "  4. fail_test if mapping POST not in (200, 201).\n"
            "  5. GET /api/seal/identity/resolve?val_user_id={val_user_id}.\n"
            "  6. fail_test if resolved.imsi != original imsi.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — val_user_id derived from IMSI suffix.\n"
            "\n"
            "Pass criteria\n"
            "  Mapping POST 200/201 AND resolve returns the correct IMSI.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  val_user_id, imsi, mapping, resolved.\n"
            "\n"
            "Known constraints\n"
            "  No mapping DELETE in this TC (TC-SEAL-006 cleans up its own\n"
            "  reverse-lookup fixtures).\n"
            "  No mapping DELETE in this TC — operator may want to call the\n"
            "  delete-by-val-id route after the test for full cleanup. IdMS\n"
            "  expiry policy is out of scope."
        ),
    )

    def run(self):
        mapping_id = None
        try:
            ue = self.require_ue()
            imsi = ue.imsi
            val_user_id = f"val-user-{imsi[-4:]}"

            result, status = _seal_api("/api/seal/identity/mappings", "POST", {
                "val_user_id": val_user_id,
                "imsi": imsi,
            })
            if status not in (200, 201):
                self.fail_test(f"Identity mapping failed: {status} {result}")
                return self.result

            mapping_id = result.get("id") or result.get("mapping_id")
            log.info("Identity mapping created: %s -> %s", val_user_id, imsi)

            # Resolve
            resolved, r_status = _seal_api(
                f"/api/seal/identity/resolve?val_user_id={val_user_id}")
            if r_status != 200:
                self.fail_test(f"Identity resolve failed: {r_status}")
                return self.result

            resolved_imsi = resolved.get("imsi")
            if resolved_imsi != imsi:
                self.fail_test(
                    f"Resolved IMSI mismatch: expected {imsi}, got {resolved_imsi}",
                    resolved=resolved,
                )
                return self.result

            self.pass_test(
                val_user_id=val_user_id, imsi=imsi,
                mapping=result, resolved=resolved,
            )
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SealIdentityReverseLookup(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SEAL-006",
        title="SEAL IdMS reverse lookup IMSI to multiple VAL users",
        spec="TS 23.434 §12",
        domain=Domain.MCX,
        nfs=(NF.NEF, NF.AF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Validates the reverse direction of the IdMS, with the\n"
            "  1-to-many cardinality TS 23.434 §12 explicitly allows\n"
            "  (one subscriber can carry multiple VAL personas).\n"
            "\n"
            "Procedure (TS 23.434 §12.3)\n"
            "  1. require_ue() to obtain imsi.\n"
            "  2. Build two VAL ids: val_a='val-rev-a-{tail4}',\n"
            "     val_b='val-rev-b-{tail4}'.\n"
            "  3. For each val id POST /api/seal/identity/mappings\n"
            "     with imsi — fail_test on the first non-(200, 201).\n"
            "  4. GET /api/seal/identity/resolve?imsi={imsi}.\n"
            "  5. Read val_users list; fail_test if either val id is not\n"
            "     present.\n"
            "  6. finally: DELETE /api/seal/identity/mappings/{val_id}\n"
            "     for both.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — VAL ids derived from imsi suffix.\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return 200/201 AND reverse-lookup carries both\n"
            "  val_user_ids in val_users.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, val_user_count.\n"
            "\n"
            "Known constraints\n"
            "  Cleanup reads `ue.imsi` in finally — relies on require_ue()\n"
            "  having succeeded in try; if StopTest was raised first the\n"
            "  finally still runs against a populated ue."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi
            val_a = f"val-rev-a-{imsi[-4:]}"
            val_b = f"val-rev-b-{imsi[-4:]}"

            # Map two VAL users to the same IMSI.
            for vu in (val_a, val_b):
                _, s = _seal_api("/api/seal/identity/mappings", "POST", {
                    "val_user_id": vu, "imsi": imsi,
                })
                if s not in (200, 201):
                    self.fail_test(f"map {vu} failed: {s}")
                    return self.result

            # Reverse lookup by IMSI.
            res, status = _seal_api(f"/api/seal/identity/resolve?imsi={imsi}")
            if status != 200:
                self.fail_test(f"reverse resolve failed: {status} {res}")
                return self.result
            users = res.get("val_users") or []
            ids = {u.get("val_user_id") for u in users}
            if val_a not in ids or val_b not in ids:
                self.fail_test(
                    f"missing val_user_ids in reverse lookup: {ids}",
                    response=res,
                )
                return self.result

            self.pass_test(imsi=imsi, val_user_count=len(users))
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for vu in (f"val-rev-a-{ue.imsi[-4:]}", f"val-rev-b-{ue.imsi[-4:]}"):
                _seal_api(f"/api/seal/identity/mappings/{vu}", "DELETE")
        return self.result


ALL_SEAL_TCS = [
    SealCreateGroup, SealGroupMembers, SealLocation,
    SealConfig, SealIdentity, SealIdentityReverseLookup,
]

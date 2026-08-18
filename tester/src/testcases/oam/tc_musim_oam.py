# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Multi-USIM operator-API.

TS 23.501 §5.34      System support for Multi-USIM devices.
TS 23.502 §4.2.6     Multi-USIM procedures (paging / busy / pre-empt).
TS 24.501 §9.11.3.91 MUSIM Allowed Indication NAS IE.

Drives /api/musim/* (panel + tester surface). Operator-API only;
no UE / gNB. The package's `Page()` simulates the network-side
paging decision and writes the audit log without any radio.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src import baseline

log = logging.getLogger("tester.tc_musim_oam")

M = "/api/musim"


def _api(path, method="GET", body=None):
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
    h = {"Content-Type": "application/json"}
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, headers=h, method=method)
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            return json.loads(resp.read().decode()), resp.status
    except urllib.error.HTTPError as e:
        try:
            err = json.loads(e.read().decode())
        except Exception:
            err = {"error": str(e)}
        return err, e.code
    except Exception as e:
        return {"error": str(e)}, 0


def _delete_group_safe(gid):
    if gid:
        try:
            _api(f"{M}/groups/{gid}", "DELETE")
        except Exception:
            pass


# ── TCs ─────────────────────────────────────────────────────────


class MusimStats(TestCase):
    """TC-MUSIM-OAM-001: /stats returns counters + by_outcome histogram."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-001",
        title="MUSIM /stats returns counters + by_outcome histogram",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
            description=(
            "Purpose\n"
            "  Smoke probe for the MUSIM dashboard tile. Validates the\n"
            "  /api/musim/stats envelope carries the counters and outcome\n"
            "  histogram templates/musim.html consumes, and that the\n"
            "  outcome vocabulary matches TS 23.502 §4.2.6 paging results.\n"
            "\n"
            "Procedure (TS 23.501 §5.34 + TS 23.502 §4.2.6)\n"
            "  1. GET /api/musim/stats; assert status 200 + ok=True.\n"
            "  2. Assert these 4 counters are present and int-typed in\n"
            "     stats: total_groups, total_members, musim_capable_ues,\n"
            "     total_paging_events.\n"
            "  3. Assert stats.by_outcome.keys() == exactly the 4-element\n"
            "     set {delivered, switched, timeout, rejected}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  Envelope is ok=True, all 4 counters are ints, and the\n"
            "  outcome histogram contains exactly the 4 expected keys (no\n"
            "  more, no fewer).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  total_groups, total_members, musim_capable_ues,\n"
            "  total_paging_events, by_outcome (via **st).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. by_outcome equality is strict — extra keys\n"
            "  would fail (catches accidental vocabulary additions). The\n"
            "  test is read-only and safe to interleave with other MUSIM\n"
            "  TCs since it only inspects shape, not counts."
        ),
    )

    def run(self):
        try:
            r, s = _api(M + "/stats")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"stats failed: {s} {r}")
                return self.result
            st = r.get("stats") or {}
            for k in ("total_groups", "total_members", "musim_capable_ues",
                      "total_paging_events"):
                if not isinstance(st.get(k), int):
                    self.fail_test(f"{k} missing/non-int", body=r)
                    return self.result
            by = st.get("by_outcome") or {}
            need = {"delivered", "switched", "timeout", "rejected"}
            if set(by.keys()) != need:
                self.fail_test(f"by_outcome keys != {need}", body=r)
                return self.result
            self.pass_test(**st)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class MusimGroupCRUD(TestCase):
    """TC-MUSIM-OAM-002: Group Create → Get → Patch → Delete."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-002",
        title="MUSIM group full CRUD lifecycle",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Round-trip the MUSIM group CRUD lifecycle on the operator\n"
            "  API. Validates Create with member listing, Get, Patch of\n"
            "  active_imsi (USIM-selection IE pre-seed), and Delete with\n"
            "  FK CASCADE on member rows — all four verbs the panel needs.\n"
            "\n"
            "Procedure (TS 23.501 §5.34 + TS 24.501 IE seeding)\n"
            "  1. POST /api/musim/groups with device_id + 2 baseline IMSIs;\n"
            "     assert 200 + ok=True. Capture gid = response.id.\n"
            "  2. GET /api/musim/groups/{gid}; assert members count == 2\n"
            "     and active_imsi == imsi(embb-bulk, 0) (first by\n"
            "     priority).\n"
            "  3. PATCH /api/musim/groups/{gid} with description='crud-\n"
            "     patched' and active_imsi=imsi(1). Assert 200 + ok.\n"
            "  4. GET again; assert active_imsi flip took effect.\n"
            "  5. DELETE /api/musim/groups/{gid}; assert 200 + ok.\n"
            "  6. Finally clause issues best-effort DELETE for safety.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — uses baseline embb-bulk IMSIs 0 and 1.\n"
            "\n"
            "Pass criteria\n"
            "  Every verb returns 200/ok; member count, active_imsi pick,\n"
            "  and patch flip all match expected values.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — seeded IMSIs required. Finally cleanup\n"
            "  shields against test-leak group rows."
        ),
    )

    def run(self):
        gid = None
        try:
            r, s = _api(M + "/groups", "POST", {
                "device_id": "tc-musim-002",
                "description": "crud",
                "imsis": [baseline.imsi("embb-bulk", 0), baseline.imsi("embb-bulk", 1)],
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"create failed: {s} {r}")
                return self.result
            gid = r["id"]

            gr, gs = _api(f"{M}/groups/{gid}")
            if gs != 200 or not gr.get("ok"):
                self.fail_test(f"get failed: {gs} {gr}")
                return self.result
            g = gr["group"]
            if len(g.get("members") or []) != 2:
                self.fail_test(f"member count != 2: {g}")
                return self.result
            if g.get("active_imsi") != baseline.imsi("embb-bulk", 0):
                self.fail_test(f"unexpected active_imsi: {g}")
                return self.result

            pr, ps = _api(f"{M}/groups/{gid}", "PATCH", {
                "description": "crud-patched",
                "active_imsi": baseline.imsi("embb-bulk", 1),
            })
            if ps != 200 or not pr.get("ok"):
                self.fail_test(f"patch failed: {ps} {pr}")
                return self.result

            gr2, _ = _api(f"{M}/groups/{gid}")
            if gr2["group"]["active_imsi"] != baseline.imsi("embb-bulk", 1):
                self.fail_test("active_imsi patch did not stick",
                               body=gr2)
                return self.result

            dr, ds = _api(f"{M}/groups/{gid}", "DELETE")
            if ds != 200 or not dr.get("ok"):
                self.fail_test(f"delete failed: {ds} {dr}")
                return self.result
            gid = None
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group_safe(gid)
        return self.result


class MusimMemberAddRemove(TestCase):
    """TC-MUSIM-OAM-003: Add / Remove members; active_imsi clears when removed."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-003",
        title="MUSIM member add/remove keeps active selection consistent",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the member CRUD contract: adding a second member is\n"
            "  acceptable, but removing the currently-active USIM must\n"
            "  clear the group's active_imsi so the operator is forced to\n"
            "  re-elect via PATCH (no stale selection).\n"
            "\n"
            "Procedure (TS 23.501 §5.34 member management)\n"
            "  1. POST /api/musim/groups with one IMSI (imsi(embb-bulk, 0)).\n"
            "     Capture gid.\n"
            "  2. POST /api/musim/groups/{gid}/members with imsi(1),\n"
            "     priority=1, usim_index=1. Assert 200 + ok=True.\n"
            "  3. GET /api/musim/groups/{gid}; assert len(members) == 2.\n"
            "  4. Find member-id corresponding to active_imsi.\n"
            "  5. DELETE /api/musim/members/{active_mid}; assert 200 + ok.\n"
            "  6. GET again; assert group.active_imsi == '' (cleared).\n"
            "  7. Finally clause deletes the group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — baseline IMSIs 0 and 1 are used.\n"
            "\n"
            "Pass criteria\n"
            "  Add returns ok, member count is 2, delete of active member\n"
            "  returns ok, and post-delete active_imsi == '' exactly.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Active-IMSI lookup assumes deterministic\n"
            "  member.id mapping; relies on the package's ORM behaviour."
        ),
    )

    def run(self):
        gid = None
        try:
            r, _ = _api(M + "/groups", "POST", {
                "device_id": "tc-musim-003",
                "imsis": [baseline.imsi("embb-bulk", 0)],
            })
            gid = r["id"]
            ar, as_ = _api(f"{M}/groups/{gid}/members", "POST", {
                "imsi": baseline.imsi("embb-bulk", 1), "priority": 1, "usim_index": 1,
            })
            if as_ != 200 or not ar.get("ok"):
                self.fail_test(f"add member failed: {as_} {ar}")
                return self.result

            gr, _ = _api(f"{M}/groups/{gid}")
            members = gr["group"]["members"]
            if len(members) != 2:
                self.fail_test(f"expected 2 members, got {len(members)}",
                               body=gr)
                return self.result
            # Find member-id of active IMSI.
            active_imsi = gr["group"]["active_imsi"]
            active_mid = next(m["id"] for m in members if m["imsi"] == active_imsi)

            dr, ds = _api(f"{M}/members/{active_mid}", "DELETE")
            if ds != 200 or not dr.get("ok"):
                self.fail_test(f"remove member failed: {ds} {dr}")
                return self.result

            gr2, _ = _api(f"{M}/groups/{gid}")
            if gr2["group"]["active_imsi"] != "":
                self.fail_test("active_imsi not cleared after removing active",
                               body=gr2)
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group_safe(gid)
        return self.result


class MusimGroupValidation(TestCase):
    """TC-MUSIM-OAM-004: Group create/patch validation surfaces 400s."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-004",
        title="MUSIM group create + patch input validation",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin route-layer validation for MUSIM group create + patch.\n"
            "  Malformed bodies and non-member active-IMSI patches must\n"
            "  fail at the route validator with a clean 400, never as a\n"
            "  500 leaking from a downstream SQLite CHECK constraint.\n"
            "\n"
            "Procedure (TS 23.501 §5.34 route validation)\n"
            "  1. POST /api/musim/groups with empty body {}; assert 400\n"
            "     (missing device_id).\n"
            "  2. POST with device_id='tc-musim-004-blank' and a blank\n"
            "     IMSI in the imsis list; assert 400 (blank-imsi reject).\n"
            "  3. POST a valid group with one IMSI (imsi(embb-bulk, 2));\n"
            "     capture gid.\n"
            "  4. PATCH /api/musim/groups/{gid} with active_imsi=\n"
            "     '001019999999999' (not in members); assert 400 and the\n"
            "     error body contains 'not a member'.\n"
            "  5. Finally clause deletes the group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — bodies are hard-coded negative-path fixtures.\n"
            "\n"
            "Pass criteria\n"
            "  Step 1, 2, 4 all return 400; step 4 also has 'not a member'\n"
            "  substring in the error string.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Finally cleanup avoids leaking the valid\n"
            "  group seeded in step 3."
        ),
    )

    def run(self):
        gid = None
        try:
            _, s1 = _api(M + "/groups", "POST", {})
            if s1 != 400:
                self.fail_test(f"missing device_id did not 400: {s1}")
                return self.result
            _, s2 = _api(M + "/groups", "POST", {
                "device_id": "tc-musim-004-blank",
                "imsis": ["", "001019999988877"],
            })
            if s2 != 400:
                self.fail_test(f"blank imsi did not 400: {s2}")
                return self.result

            r, _ = _api(M + "/groups", "POST", {
                "device_id": "tc-musim-004",
                "imsis": [baseline.imsi("embb-bulk", 2)],
            })
            gid = r["id"]
            pr, ps = _api(f"{M}/groups/{gid}", "PATCH", {
                "active_imsi": "001019999999999",
            })
            if ps != 400:
                self.fail_test(f"non-member active did not 400: {ps} {pr}")
                return self.result
            if "not a member" not in (pr.get("error") or ""):
                self.fail_test(f"unexpected error message: {pr}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group_safe(gid)
        return self.result


class MusimCapabilityUpsert(TestCase):
    """TC-MUSIM-OAM-005: Capability upsert (TS 24.501 §9.11.3.91)."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-005",
        title="Per-IMSI MUSIM capability upsert (ON CONFLICT path)",
        spec="TS 24.501 §9.11.3.91",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the per-IMSI MUSIM capability upsert path that seeds the\n"
            "  TS 24.501 §9.11.3.91 MUSIM Allowed Indication NAS IE. Two\n"
            "  POSTs for the same IMSI must collapse to a single row via\n"
            "  ON CONFLICT, and out-of-range max_usim_count must 400.\n"
            "\n"
            "Procedure (TS 24.501 §9.11.3.91)\n"
            "  1. POST /api/musim/capabilities for imsi(embb-bulk, 3) with\n"
            "     max_usim_count=3, min_paging_interval_ms=2560. Assert\n"
            "     200 + ok=True.\n"
            "  2. GET /api/musim/capabilities; assert the row exists and\n"
            "     max_usim_count == 3.\n"
            "  3. POST same IMSI again with max_usim_count=4. Assert 200.\n"
            "  4. GET capabilities; assert exactly one row for this IMSI\n"
            "     (no duplicate insertion via ON CONFLICT) and\n"
            "     max_usim_count == 4 (upsert wrote through).\n"
            "  5. POST with max_usim_count=99 (out of valid range);\n"
            "     assert 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — uses baseline IMSI #3.\n"
            "\n"
            "Pass criteria\n"
            "  First/second upserts both 200, single row remains with\n"
            "  updated value, out-of-range value returns 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Test leaves a single capability row for the\n"
            "  IMSI (no cleanup); future runs idempotently overwrite it."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 3)
        try:
            r, s = _api(M + "/capabilities", "POST", {
                "imsi": imsi, "musim_supported": True,
                "max_usim_count": 3, "min_paging_interval_ms": 2560,
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"upsert failed: {s} {r}")
                return self.result
            lr, _ = _api(M + "/capabilities")
            row = next((c for c in lr.get("capabilities", []) if c["imsi"] == imsi), None)
            if not row:
                self.fail_test(f"imsi {imsi} not in list",
                               sample=[c["imsi"] for c in lr.get("capabilities", [])][:5])
                return self.result
            if row.get("max_usim_count") != 3:
                self.fail_test(f"max_usim_count != 3: {row}")
                return self.result

            _, s2 = _api(M + "/capabilities", "POST", {
                "imsi": imsi, "musim_supported": True,
                "max_usim_count": 4, "min_paging_interval_ms": 2560,
            })
            if s2 != 200:
                self.fail_test(f"second upsert failed: {s2}")
                return self.result
            lr2, _ = _api(M + "/capabilities")
            rows = [c for c in lr2.get("capabilities", []) if c["imsi"] == imsi]
            if len(rows) != 1:
                self.fail_test(f"upsert created duplicate row(s): {len(rows)}")
                return self.result
            if rows[0].get("max_usim_count") != 4:
                self.fail_test(f"upsert did not stick: {rows[0]}")
                return self.result

            _, sbad = _api(M + "/capabilities", "POST", {
                "imsi": imsi, "musim_supported": True, "max_usim_count": 99,
            })
            if sbad != 400:
                self.fail_test(f"max_usim=99 did not 400: {sbad}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class MusimPageDelivered(TestCase):
    """TC-MUSIM-OAM-006: Paging the active USIM → delivered."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-006",
        title="Paging the active USIM yields delivered outcome",
        spec="TS 23.502 §4.2.6",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the TS 23.502 §4.2.6 'delivered' paging outcome: paging\n"
            "  the currently-active USIM in a multi-IMSI group must resolve\n"
            "  in-band (no switch, no busy/timeout) and write a 'delivered'\n"
            "  row to the audit log.\n"
            "\n"
            "Procedure (TS 23.502 §4.2.6 paging — happy path)\n"
            "  1. POST /api/musim/groups with device_id='tc-musim-006' and\n"
            "     2 IMSIs (imsi 4 + 5). Capture gid. First IMSI (4) is the\n"
            "     active member by priority.\n"
            "  2. POST /api/musim/page with device_id='tc-musim-006',\n"
            "     target_imsi=imsi(4) (the active one), reason=tc-delivered.\n"
            "  3. Assert 200 + ok=True and result.outcome == 'delivered'.\n"
            "  4. GET /api/musim/paging-log?device_id=tc-musim-006 and\n"
            "     assert 'delivered' appears in the outcomes seen.\n"
            "  5. Finally clause deletes the group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — baseline IMSIs 4 + 5 are used.\n"
            "\n"
            "Pass criteria\n"
            "  Page call returns ok with outcome=='delivered' and the\n"
            "  paging-log contains at least one 'delivered' audit row.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Operator-API only — Page() simulates the\n"
            "  paging decision without any radio."
        ),
    )

    def run(self):
        gid = None
        try:
            r, _ = _api(M + "/groups", "POST", {
                "device_id": "tc-musim-006",
                "imsis": [baseline.imsi("embb-bulk", 4), baseline.imsi("embb-bulk", 5)],
            })
            gid = r["id"]
            pr, ps = _api(M + "/page", "POST", {
                "device_id": "tc-musim-006",
                "target_imsi": baseline.imsi("embb-bulk", 4),
                "reason": "tc-delivered",
            })
            if ps != 200 or not pr.get("ok"):
                self.fail_test(f"page failed: {ps} {pr}")
                return self.result
            res = pr.get("result") or {}
            if res.get("outcome") != "delivered":
                self.fail_test(f"outcome != delivered: {res}", body=pr)
                return self.result
            lr, _ = _api(f"{M}/paging-log?device_id=tc-musim-006")
            outcomes = {e["outcome"] for e in lr.get("log", [])}
            if "delivered" not in outcomes:
                self.fail_test(f"delivered audit row missing: {outcomes}",
                               body=lr)
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group_safe(gid)
        return self.result


class MusimPageSwitched(TestCase):
    """TC-MUSIM-OAM-007: Paging an inactive USIM → switched + active flips."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-007",
        title="Paging an inactive USIM triggers a switch and flips active",
        spec="TS 23.502 §4.2.6",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the TS 23.502 §4.2.6 'switched' paging outcome: paging\n"
            "  the inactive USIM in a multi-IMSI MUSIM group must trigger\n"
            "  a switch, flip group.active_imsi, and surface prior_active /\n"
            "  new_active in the response so the dashboard can audit the\n"
            "  pre-emption.\n"
            "\n"
            "Procedure (TS 23.502 §4.2.6 paging — switch path)\n"
            "  1. POST /api/musim/groups for tc-musim-007 with 2 IMSIs\n"
            "     (imsi 6 + 7). Capture gid. IMSI 6 is active.\n"
            "  2. POST /api/musim/page with target_imsi=imsi(7) (the\n"
            "     inactive one), reason='tc-switched'.\n"
            "  3. Assert 200 + ok=True and result.outcome == 'switched'.\n"
            "  4. Assert result.prior_active_imsi == imsi(6).\n"
            "  5. Assert result.new_active_imsi == imsi(7).\n"
            "  6. GET /api/musim/groups/{gid}; assert group.active_imsi\n"
            "     has been updated to imsi(7).\n"
            "  7. Finally clause deletes the group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — baseline IMSIs 6 + 7 are used.\n"
            "\n"
            "Pass criteria\n"
            "  outcome=='switched'; prior/new active match expected IMSIs;\n"
            "  group state is updated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Operator-side simulation; no UE/gNB exchange."
        ),
    )

    def run(self):
        gid = None
        try:
            r, _ = _api(M + "/groups", "POST", {
                "device_id": "tc-musim-007",
                "imsis": [baseline.imsi("embb-bulk", 6), baseline.imsi("embb-bulk", 7)],
            })
            gid = r["id"]
            pr, ps = _api(M + "/page", "POST", {
                "device_id": "tc-musim-007",
                "target_imsi": baseline.imsi("embb-bulk", 7),
                "reason": "tc-switched",
            })
            if ps != 200 or not pr.get("ok"):
                self.fail_test(f"page failed: {ps} {pr}")
                return self.result
            res = pr.get("result") or {}
            if res.get("outcome") != "switched":
                self.fail_test(f"outcome != switched: {res}", body=pr)
                return self.result
            if res.get("prior_active_imsi") != baseline.imsi("embb-bulk", 6):
                self.fail_test(f"prior_active != 301: {res}", body=pr)
                return self.result
            if res.get("new_active_imsi") != baseline.imsi("embb-bulk", 7):
                self.fail_test(f"new_active != 302: {res}", body=pr)
                return self.result
            gr, _ = _api(f"{M}/groups/{gid}")
            if gr["group"]["active_imsi"] != baseline.imsi("embb-bulk", 7):
                self.fail_test(f"group.active_imsi not updated", body=gr)
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group_safe(gid)
        return self.result


class MusimPageRejected(TestCase):
    """TC-MUSIM-OAM-008: Paging a non-member IMSI → rejected."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-008",
        title="Paging a non-member IMSI yields rejected outcome",
        spec="TS 23.502 §4.2.6",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the TS 23.502 §4.2.6 'rejected' paging outcome: paging\n"
            "  a target_imsi that is not a member of the device's MUSIM\n"
            "  group must return outcome='rejected' without mutating the\n"
            "  group's active_imsi.\n"
            "\n"
            "Procedure (TS 23.502 §4.2.6 paging — reject path)\n"
            "  1. POST /api/musim/groups for tc-musim-008 with one IMSI\n"
            "     (imsi(8)). Capture gid; imsi(8) is the active member.\n"
            "  2. POST /api/musim/page with device_id='tc-musim-008' and\n"
            "     target_imsi='001011199999999' (not in the group).\n"
            "  3. Assert 200 + ok=True (the call itself succeeds) and\n"
            "     result.outcome == 'rejected'.\n"
            "  4. GET /api/musim/groups/{gid}; assert active_imsi is still\n"
            "     imsi(8) (unchanged by the reject).\n"
            "  5. Finally clause deletes the group.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — baseline IMSI #8 + a fixed non-member IMSI string.\n"
            "\n"
            "Pass criteria\n"
            "  Page returns ok=True with outcome=='rejected' and the\n"
            "  group's active_imsi is unchanged from before the page.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Returns 200 (not 404) because the device_id\n"
            "  itself is known — only the target_imsi is invalid."
        ),
    )

    def run(self):
        gid = None
        try:
            r, _ = _api(M + "/groups", "POST", {
                "device_id": "tc-musim-008",
                "imsis": [baseline.imsi("embb-bulk", 8)],
            })
            gid = r["id"]
            pr, ps = _api(M + "/page", "POST", {
                "device_id": "tc-musim-008",
                "target_imsi": "001011199999999",
                "reason": "tc-rejected",
            })
            if ps != 200 or not pr.get("ok"):
                self.fail_test(f"page failed: {ps} {pr}")
                return self.result
            if pr["result"]["outcome"] != "rejected":
                self.fail_test(f"outcome != rejected: {pr}", body=pr)
                return self.result
            gr, _ = _api(f"{M}/groups/{gid}")
            if gr["group"]["active_imsi"] != baseline.imsi("embb-bulk", 8):
                self.fail_test(f"active_imsi changed on reject", body=gr)
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_group_safe(gid)
        return self.result


class MusimNotFound(TestCase):
    """TC-MUSIM-OAM-009: GET / PATCH / DELETE on unknown group → 404."""
    SPEC = TestSpec(
        tc_id="TC-MUSIM-009",
        title="MUSIM unknown group / member / device returns 404",
        spec="TS 23.501 §5.34",
        domain=Domain.MOBILITY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  API hygiene gate for unknown-resource handling on the MUSIM\n"
            "  panel surface. Every GET/PATCH/DELETE on a non-existent\n"
            "  group / member must return 404, and paging an unknown\n"
            "  device_id must also 404 — no 500 leaks, no silent 200s.\n"
            "\n"
            "Procedure (TS 23.501 §5.34 + RFC 9110)\n"
            "  1. GET /api/musim/groups/99999999; assert 404.\n"
            "  2. PATCH /api/musim/groups/99999999 with {description:'x'};\n"
            "     assert 404.\n"
            "  3. DELETE /api/musim/groups/99999999; assert 404.\n"
            "  4. DELETE /api/musim/members/99999999; assert 404.\n"
            "  5. POST /api/musim/page with device_id='tc-musim-no-such'\n"
            "     and an arbitrary target_imsi; assert 404.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — all unknown IDs are hard-coded.\n"
            "\n"
            "Pass criteria\n"
            "  All five endpoints return exactly 404. Any 200 / 500 / 400\n"
            "  fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Pure negative path — runs without seeded data\n"
            "  and never creates a row. Group id 99999999 and member id\n"
            "  99999999 are well above any realistically allocated PK so\n"
            "  the test is collision-free in any environment."
        ),
    )

    def run(self):
        try:
            for path, method, body in (
                (f"{M}/groups/99999999", "GET", None),
                (f"{M}/groups/99999999", "PATCH", {"description": "x"}),
                (f"{M}/groups/99999999", "DELETE", None),
                (f"{M}/members/99999999", "DELETE", None),
            ):
                _, st = _api(path, method, body)
                if st != 404:
                    self.fail_test(f"{method} {path} did not 404: {st}")
                    return self.result
            _, ps = _api(M + "/page", "POST", {
                "device_id": "tc-musim-no-such",
                "target_imsi": "001011111110501",
            })
            if ps != 404:
                self.fail_test(f"page unknown device did not 404: {ps}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_MUSIM_OAM_TCS = [
    MusimStats,
    MusimGroupCRUD,
    MusimMemberAddRemove,
    MusimGroupValidation,
    MusimCapabilityUpsert,
    MusimPageDelivered,
    MusimPageSwitched,
    MusimPageRejected,
    MusimNotFound,
]

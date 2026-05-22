# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: per-TA NSSAI policy — TS 23.501 §5.15.3 / §5.15.5.2.

Exercises the ta_nssai_policy table that gates Allowed-NSSAI selection
per Tracking Area. The NSSF reads this table at registration time
(nf/nssf/selection.go::taPolicyAllows → crud.TANssaiPolicyAllows);
when a (TAC, SST, SD) row exists with allowed=0, the slice lands in
the Rejected NSSAI with cause = NotInRegistrationArea (TS 24.501
§9.11.3.46 cause value 1).

These are API-contract + persistence tests. End-to-end NSSF gating
during NAS Initial Registration is exercised separately in robot
suites that drive a gNB with a specific TAC.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_ta_nssai")


def _api(path, method="GET", body=None):
    from src.core.api import get_core_ip
    url = f"http://{get_core_ip()}:5000{path}"
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


def _create_tac(tac, mcc="001", mnc="01", name=""):
    return _api("/api/tac/tracking-areas", "POST",
                {"tac": tac, "plmn_mcc": mcc, "plmn_mnc": mnc, "name": name})


def _delete_tac(tac):
    return _api(f"/api/tac/tracking-areas/{tac}", "DELETE")


def _list_policy(tac):
    body, status = _api(f"/api/tac/tracking-areas/{tac}/nssai")
    if status != 200 or not isinstance(body, list):
        return []
    return body


# ─── TC-TANSSAI-001 ──────────────────────────────────────────────────


class TaPolicyDefaultAllow(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TANSSAI-001",
        title="Per-TA NSSAI policy: empty TAC = default-allow",
        spec="TS 23.501 §5.15.3",
        domain=Domain.SLICING,
        nfs=(NF.NSSF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the NSSF default-allow fall-through (TS 23.501 §5.15.3\n"
            "  Allowed NSSAI selection). When the ta_nssai_policy table\n"
            "  has zero rows for a TAC, every Subscribed S-NSSAI must be\n"
            "  a candidate (taPolicyAllows fall-through path). If this\n"
            "  regresses, brand-new TACs silently reject all slices and\n"
            "  no UE can register in the new TA.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.3 + ta_nssai_policy table contract)\n"
            "  1. tac = '0aa001'.\n"
            "  2. _delete_tac(tac) — pre-cleanup if rerun.\n"
            "  3. _create_tac(tac, mcc='001', mnc='01',\n"
            "     name='tc-tanssai-001') via POST\n"
            "     /api/tac/tracking-areas; require status 200/201.\n"
            "  4. _list_policy(tac) → GET\n"
            "     /api/tac/tracking-areas/{tac}/nssai.\n"
            "  5. Assert returned list is empty (no rows).\n"
            "  6. finally: _delete_tac(tac) for cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — tac is hard-coded.\n"
            "\n"
            "Pass criteria\n"
            "  TAC POST succeeded (status in {200, 201}) AND policy list\n"
            "  returned empty.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  tac, policy_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no baseline/UE pool required. API-contract\n"
            "  test only; does NOT exercise NSSF gating during a real\n"
            "  NAS Initial Registration."
        ),
    )

    def run(self):
        tac = "0aa001"
        try:
            _delete_tac(tac)  # pre-cleanup if test rerun
            _, st = _create_tac(tac, name="tc-tanssai-001")
            if st not in (200, 201):
                self.fail_test(f"TAC create failed: {st}")
                return self.result

            policies = _list_policy(tac)
            if policies:
                self.fail_test(f"expected empty policy, got {policies}")
                return self.result
            self.pass_test(tac=tac, policy_count=0)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_tac(tac)
        return self.result


# ─── TC-TANSSAI-002 ──────────────────────────────────────────────────


class TaPolicyExplicitDeny(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TANSSAI-002",
        title="Per-TA NSSAI policy: explicit deny round-trips with allowed=false",
        spec="TS 23.501 §5.15.3",
        domain=Domain.SLICING,
        nfs=(NF.NSSF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the explicit-deny round-trip (TS 23.501 §5.15.3). A\n"
            "  deny row written via POST must come back from GET with\n"
            "  allowed=false — and that's the row the NSSF\n"
            "  taPolicyAllows path consults to land the slice in\n"
            "  Rejected NSSAI with cause = NotInRegistrationArea (TS\n"
            "  24.501 §9.11.3.46 cause value 1) at NAS Registration.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.3 + 24.501 §9.11.3.46)\n"
            "  1. tac = '0aa002'.\n"
            "  2. _delete_tac(tac); _create_tac(tac,\n"
            "     name='tc-tanssai-002').\n"
            "  3. POST /api/tac/tracking-areas/{tac}/nssai with body\n"
            "     {sst: 1, sd: '', allowed: False}; require status\n"
            "     200/201.\n"
            "  4. _list_policy(tac) and find the row with sst==1.\n"
            "  5. Assert row exists AND row['allowed'] is False.\n"
            "  6. finally: _delete_tac(tac).\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — tac and sst are hard-coded.\n"
            "\n"
            "Pass criteria\n"
            "  Deny POST returned status 200/201 AND a row with sst==1\n"
            "  is present in the listing AND that row's allowed field\n"
            "  is False (not truthy / None / 'false' string).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  tac, denied_sst, row.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. API-contract test — actual NAS Registration\n"
            "  Reject is verified separately in Robot suites."
        ),
    )

    def run(self):
        tac = "0aa002"
        try:
            _delete_tac(tac)
            _create_tac(tac, name="tc-tanssai-002")
            _, st = _api(f"/api/tac/tracking-areas/{tac}/nssai", "POST",
                         {"sst": 1, "sd": "", "allowed": False})
            if st not in (200, 201):
                self.fail_test(f"deny POST failed: {st}")
                return self.result

            policies = _list_policy(tac)
            row = next((p for p in policies if int(p.get("sst", -1)) == 1), None)
            if not row:
                self.fail_test("deny row not present after POST",
                               policies=policies)
                return self.result
            if row.get("allowed") is not False:
                self.fail_test(f"expected allowed=false, got {row.get('allowed')}",
                               row=row)
                return self.result
            self.pass_test(tac=tac, denied_sst=1, row=row)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_tac(tac)
        return self.result


# ─── TC-TANSSAI-003 ──────────────────────────────────────────────────


class TaPolicyAllowOverride(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TANSSAI-003",
        title="Per-TA NSSAI policy: POST flips deny→allow (INSERT OR REPLACE)",
        spec="TS 23.501 §5.15.3",
        domain=Domain.SLICING,
        nfs=(NF.NSSF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the (tac, sst, sd) UNIQUE-key upsert contract on\n"
            "  ta_nssai_policy (TS 23.501 §5.15.3 + INSERT OR REPLACE\n"
            "  semantics). Operators flip policies live; the new POST\n"
            "  for an existing (TAC, SST, SD) tuple must overwrite the\n"
            "  old row rather than creating a second one — otherwise\n"
            "  the NSSF sees ambiguous policy and behaviour is\n"
            "  undefined.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.3 + ta_nssai_policy UNIQUE key)\n"
            "  1. tac = '0aa003'.\n"
            "  2. _delete_tac(tac); _create_tac(tac,\n"
            "     name='tc-tanssai-003').\n"
            "  3. POST /api/tac/tracking-areas/{tac}/nssai\n"
            "     {sst:1, sd:'', allowed:False} — initial deny.\n"
            "  4. POST same endpoint {sst:1, sd:'', allowed:True} —\n"
            "     flip to allow.\n"
            "  5. _list_policy(tac); filter rows with sst==1.\n"
            "  6. Assert exactly 1 sst=1 row AND its allowed is True.\n"
            "  7. finally: _delete_tac(tac).\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  len(sst1_rows) == 1 AND sst1_rows[0]['allowed'] is True\n"
            "  (the second POST replaced the first; new value wins).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  tac, final_row.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Does NOT check the deny POST's HTTP status\n"
            "  individually — only the final listing state."
        ),
    )

    def run(self):
        tac = "0aa003"
        try:
            _delete_tac(tac)
            _create_tac(tac, name="tc-tanssai-003")
            _api(f"/api/tac/tracking-areas/{tac}/nssai", "POST",
                 {"sst": 1, "sd": "", "allowed": False})
            _api(f"/api/tac/tracking-areas/{tac}/nssai", "POST",
                 {"sst": 1, "sd": "", "allowed": True})

            policies = _list_policy(tac)
            sst1_rows = [p for p in policies if int(p.get("sst", -1)) == 1]
            if len(sst1_rows) != 1:
                self.fail_test(f"INSERT OR REPLACE produced "
                               f"{len(sst1_rows)} rows, expected 1",
                               policies=policies)
                return self.result
            if sst1_rows[0].get("allowed") is not True:
                self.fail_test("override allowed flag did not flip",
                               row=sst1_rows[0])
                return self.result
            self.pass_test(tac=tac, final_row=sst1_rows[0])
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_tac(tac)
        return self.result


# ─── TC-TANSSAI-004 ──────────────────────────────────────────────────


class TaPolicyDeleteCascadesOnTAC(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TANSSAI-004",
        title="Per-TA NSSAI policy: DELETE on TAC cascades to policy rows",
        spec="TS 23.501 §5.15.3",
        domain=Domain.SLICING,
        nfs=(NF.NSSF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the ON DELETE CASCADE foreign key from\n"
            "  ta_nssai_policy.tac → tracking_areas.tac (TS 23.501\n"
            "  §5.15.3 + persistence contract). Operators expect that\n"
            "  removing a TAC also reaps its policy rows; otherwise\n"
            "  orphan rows accumulate and a future TAC reused for a\n"
            "  different deployment inherits stale gating.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.3 + DB cascade)\n"
            "  1. tac = '0aa004'.\n"
            "  2. _create_tac(tac, name='tc-tanssai-004').\n"
            "  3. for sst in (1, 2, 3):\n"
            "       POST /api/tac/tracking-areas/{tac}/nssai\n"
            "       {sst, sd:'', allowed:True}.\n"
            "  4. _list_policy(tac); assert len == 3.\n"
            "  5. _delete_tac(tac).\n"
            "  6. _list_policy(tac); assert empty (cascade worked).\n"
            "  7. finally: _delete_tac(tac) — idempotent re-delete.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  before == 3 rows AND after == 0 rows. Both gates must\n"
            "  hold — pre-condition (3 rows) and the cascade itself.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rows_before, rows_after.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. A GET on a deleted TAC returns 200 with [] —\n"
            "  TAC absence is not an error per the API contract."
        ),
    )

    def run(self):
        tac = "0aa004"
        try:
            _create_tac(tac, name="tc-tanssai-004")
            for sst in (1, 2, 3):
                _api(f"/api/tac/tracking-areas/{tac}/nssai", "POST",
                     {"sst": sst, "sd": "", "allowed": True})
            before = _list_policy(tac)
            if len(before) != 3:
                self.fail_test(f"expected 3 policy rows, got {len(before)}",
                               policies=before)
                return self.result

            _delete_tac(tac)
            # After TAC delete the GET returns 200/[] (TAC absence is
            # not an error — the listing just becomes empty).
            after = _list_policy(tac)
            if after:
                self.fail_test(f"policy rows survived TAC delete: {after}")
                return self.result
            self.pass_test(rows_before=len(before), rows_after=len(after))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _delete_tac(tac)
        return self.result


# ─── TC-TANSSAI-005 ──────────────────────────────────────────────────


class TaPolicyMultipleTACsIndependent(TestCase):
    SPEC = TestSpec(
        tc_id="TC-TANSSAI-005",
        title="Per-TA NSSAI policy: two TACs hold independent policies",
        spec="TS 23.501 §5.15.3",
        domain=Domain.SLICING,
        nfs=(NF.NSSF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins per-TAC policy isolation (TS 23.501 §5.15.3 + (TAC,\n"
            "  SST, SD) primary key). Two TACs with different policies\n"
            "  must not bleed rows into each other's listings — that's\n"
            "  the keying the NSSF reads at NAS Registration to compute\n"
            "  Allowed-NSSAI per UE.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.3 + ta_nssai_policy key contract)\n"
            "  1. tac_a, tac_b = '0aa005', '0aa006'.\n"
            "  2. For each TAC: _delete_tac(t); _create_tac(t,\n"
            "     name='tc-tanssai-005-{t}').\n"
            "  3. POST /api/tac/tracking-areas/{tac_a}/nssai\n"
            "     {sst:1, sd:'', allowed:False} — deny on A.\n"
            "  4. POST /api/tac/tracking-areas/{tac_b}/nssai\n"
            "     {sst:2, sd:'', allowed:True} — allow on B.\n"
            "  5. pa = _list_policy(tac_a); pb = _list_policy(tac_b).\n"
            "  6. Assert pa is exactly [sst=1 row] and pb is exactly\n"
            "     [sst=2 row].\n"
            "  7. finally: _delete_tac for both TACs.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None.\n"
            "\n"
            "Pass criteria\n"
            "  len(pa) == 1 AND pa[0].sst == 1 AND len(pb) == 1 AND\n"
            "  pb[0].sst == 2 (each TAC's listing returns only its\n"
            "  own rows).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  tac_a_rows, tac_b_rows.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Does NOT verify allowed flag values in the\n"
            "  assertion — only SST and row count per TAC."
        ),
    )

    def run(self):
        tac_a, tac_b = "0aa005", "0aa006"
        try:
            for t in (tac_a, tac_b):
                _delete_tac(t)
                _create_tac(t, name=f"tc-tanssai-005-{t}")
            _api(f"/api/tac/tracking-areas/{tac_a}/nssai", "POST",
                 {"sst": 1, "sd": "", "allowed": False})
            _api(f"/api/tac/tracking-areas/{tac_b}/nssai", "POST",
                 {"sst": 2, "sd": "", "allowed": True})

            pa = _list_policy(tac_a)
            pb = _list_policy(tac_b)
            if not (len(pa) == 1 and int(pa[0].get("sst", -1)) == 1):
                self.fail_test(f"TAC A policy unexpected: {pa}")
                return self.result
            if not (len(pb) == 1 and int(pb[0].get("sst", -1)) == 2):
                self.fail_test(f"TAC B policy unexpected: {pb}")
                return self.result
            self.pass_test(tac_a_rows=pa, tac_b_rows=pb)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for t in (tac_a, tac_b):
                _delete_tac(t)
        return self.result


ALL_TA_NSSAI_TCS = [
    TaPolicyDefaultAllow,
    TaPolicyExplicitDeny,
    TaPolicyAllowOverride,
    TaPolicyDeleteCascadesOnTAC,
    TaPolicyMultipleTACsIndependent,
]

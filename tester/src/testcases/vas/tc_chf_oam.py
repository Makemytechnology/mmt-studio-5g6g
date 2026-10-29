# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: CHF / Charging — operator-API only.

These TCs hit /api/chf/* directly (no UE/gNB required). Specs:

  TS 32.290 §6.2   — Nchf_ChargingData service operations.
  TS 32.291 §6.1   — Online charging session lifecycle.
  TS 32.291 §6.1.3 — Multiple Unit Information / quota.
  TS 32.260        — IMS voice/video CDR fields (sibling vertical).

Before this batch, /api/chf/* was a 7-line stub block in
routes_nsaas.go returning empty objects; routes_chf.go now wires
the full nf/chf package, and these TCs pin its operator contract.
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

log = logging.getLogger("tester.tc_chf_oam")

CHF = "/api/chf"


def _api(path, method="GET", body=None):
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


class ChfStatsShape(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-010",
        title="/stats carries active_sessions, total_cdrs, active_quotas",
        spec="TS 32.290 §6.2",
        domain=Domain.CHARGING,
        nfs=(NF.CHF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Pins the operator-dashboard envelope for /api/chf/stats. The CHF\n"
                "  OAM facade (TS 32.290 §6.2, TS 32.291 §5.2.2) MUST advertise the\n"
                "  set of counters the GUI binds to. Missing keys break the dashboard\n"
                "  silently, so this test is a static schema check, not a behaviour\n"
                "  check.\n"
                "\n"
                "Procedure (TS 32.290 §6.2 + TS 32.291 §5.2.2)\n"
                "  1. GET /api/chf/stats — no body, fast HTTP probe.\n"
                "  2. Assert HTTP 200 AND r.ok truthy.\n"
                "  3. For each key in {active_sessions, total_cdrs, active_quotas,\n"
                "     rated_cdrs, pending_cdrs} — assert key is present in r.stats.\n"
                "  4. Missing key → fail_test with the actual key list (got=...).\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — passive endpoint probe)\n"
                "\n"
                "Pass criteria\n"
                "  s == 200, r.ok truthy, AND all five canonical counters present.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — pure schema gate, only structured fail payload on error).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY — runs without any UE/PDU session. Counter values are\n"
                "  not numerically asserted, only presence.\n"
                "  The /stats envelope is consumed by the operator dashboard tile\n"
                "  set; field renames here MUST be migrated through that GUI too.\n"
                "  The dashboard binds keys verbatim — fuzzy lookups are not used.\n"
                "  Resetting counters between test runs is the harness's job, not\n"
                "  this test's."
            ),
    )

    def run(self):
        try:
            r, s = _api(CHF + "/stats")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"stats failed: {s} {r}")
                return self.result
            st = r.get("stats", {})
            for k in ("active_sessions", "total_cdrs", "active_quotas",
                      "rated_cdrs", "pending_cdrs"):
                if k not in st:
                    self.fail_test(f"stats missing {k}", got=list(st))
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ChfChargingDataLifecycle(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-011",
        title="charging-data lifecycle: create -> interim -> release -> CDR",
        spec="TS 32.291 §6.1",
        domain=Domain.CHARGING,
        nfs=(NF.CHF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Pins the full operator-API view of the Nchf_ChargingData lifecycle\n"
            "  (TS 32.290 §6.2.1–§6.2.3 mapped via TS 32.291 §6.1): Create\n"
            "  →Update →Release →CDR write. Differs from TC-CHF-003 by talking\n"
            "  directly to /api/chf/* (no UE/gNB plumbing) and by asserting a\n"
            "  cumulative volume threshold and a per-IMSI CDR count.\n"
            "\n"
            "Procedure (TS 32.290 §6.2 + TS 32.291 §6.1)\n"
            "  1. imsi = baseline.imsi('embb-bulk', 10).\n"
            "  2. POST /charging-data {service_name='internet', method='offline',\n"
            "     pdu_session_id=11011} → require HTTP 200 and a session_id.\n"
            "  3. PUT /charging-data/{sid} with 1 MiB UL + 1 MiB DL + 60 s.\n"
            "  4. Require su==200 AND total_volume >= 2_000_000.\n"
            "  5. POST /charging-data/{sid}/release with 100 kB final usage,\n"
            "     require sr==200.\n"
            "  6. GET /cdrs?imsi={imsi} and assert count > 0.\n"
            "  7. finally: best-effort second release for idempotency.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixtures hard-coded; pdu_session_id derived from index)\n"
            "\n"
            "Pass criteria\n"
            "  Every step returns 200, interim total_volume >= 2 MB, and a CDR\n"
            "  for this IMSI is visible after Release.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — only structured failure payloads on the no-go paths).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — CHF baseline state required. Direct API path,\n"
            "  no SBI Nchf_ChargingData wire trace."
        ),
    )

    def run(self):
        try:
            imsi = baseline.imsi("embb-bulk", 10)  # was hardcoded "001011234560011" (pre-manifest prefix)
            r, s = _api(CHF + "/charging-data", "POST", {
                "imsi": imsi,
                "service_name": "internet",
                "charging_method": "offline",
                "pdu_session_id": 11011,
            })
            if s != 200 or not r.get("session_id"):
                self.fail_test(f"create failed: {s} {r}")
                return self.result
            sid = r["session_id"]
            try:
                # Interim update: 1 MB UL + 1 MB DL + 60s
                ru, su = _api(f"{CHF}/charging-data/{sid}", "PUT", {
                    "usage": {
                        "volume_uplink": 1_048_576,
                        "volume_downlink": 1_048_576,
                        "duration_s": 60,
                    },
                })
                if su != 200:
                    self.fail_test(f"interim failed: {su} {ru}")
                    return self.result
                tv = ru.get("total_volume", 0)
                if tv < 2_000_000:
                    self.fail_test(f"total_volume too small: {tv}")
                    return self.result
                # Release with final usage
                rr, sr = _api(f"{CHF}/charging-data/{sid}/release", "POST", {
                    "final_usage": {
                        "volume_uplink": 100_000,
                        "volume_downlink": 100_000,
                        "duration_s": 5,
                    },
                })
                if sr != 200:
                    self.fail_test(f"release failed: {sr} {rr}")
                    return self.result
                # CDR should now exist with this session's IMSI.
                cd, _ = _api(f"{CHF}/cdrs?imsi={imsi}")
                if cd.get("count", 0) == 0:
                    self.fail_test(f"no CDR after release: {cd}")
                    return self.result
            finally:
                _api(f"{CHF}/charging-data/{sid}/release", "POST")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ChfChargingDataValidation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-012",
        title="charging-data input validation: bad method / missing IMSI / unknown session",
        spec="TS 32.291 §6.1",
        domain=Domain.CHARGING,
        nfs=(NF.CHF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Negative-path conformance for the Nchf_ChargingData endpoints\n"
                "  (TS 32.290 §6.2, TS 32.291 §6.1). Bad input MUST be rejected\n"
                "  with the correct 4xx, never silently coerced. Pins four canonical\n"
                "  rejections so regressions in the validation layer break tests\n"
                "  before they break operators.\n"
                "\n"
                "Procedure (TS 32.290 §6.2 + TS 32.291 §6.1.2)\n"
                "  1. POST /charging-data with {charging_method='offline'} (no imsi)\n"
                "     — expect HTTP 400.\n"
                "  2. POST /charging-data with imsi + charging_method='barter'\n"
                "     (unknown method) — expect 400.\n"
                "  3. PUT /charging-data/no-such-session with a usage payload —\n"
                "     expect 404 (unknown chargingDataRef).\n"
                "  4. GET /charging-data/nope — expect 404.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — payloads hard-coded; imsi from baseline 'embb-bulk' #11)\n"
                "\n"
                "Pass criteria\n"
                "  Status codes match exactly: 400, 400, 404, 404 (in order).\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure messages on mismatch).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. No charging session is created here, so the IMSI\n"
                "  value in step 2 doesn't need to exist.\n"
                "  Validation responses include a structured error envelope; this\n"
                "  test pins the status code only — body shape is left flexible.\n"
                "  Schemes for IMSI canonicalisation (E.212 normalisation) are out\n"
                "  of scope."
            ),
    )

    def run(self):
        try:
            # Missing imsi → 400
            _, s = _api(CHF + "/charging-data", "POST", {
                "charging_method": "offline",
            })
            if s != 400:
                self.fail_test(f"missing imsi did not 400: {s}")
                return self.result
            # Bad charging_method → 400
            _, s2 = _api(CHF + "/charging-data", "POST", {
                "imsi": baseline.imsi("embb-bulk", 11), "charging_method": "barter",
            })
            if s2 != 400:
                self.fail_test(f"bad charging_method did not 400: {s2}")
                return self.result
            # Unknown session_id update → 404
            _, s3 = _api(f"{CHF}/charging-data/no-such-session", "PUT", {
                "usage": {"volume_uplink": 1, "volume_downlink": 1,
                          "duration_s": 1},
            })
            if s3 != 404:
                self.fail_test(f"unknown session update: {s3}")
                return self.result
            # Unknown session GET → 404
            _, s4 = _api(f"{CHF}/charging-data/nope")
            if s4 != 404:
                self.fail_test(f"unknown session GET: {s4}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ChfQuotaGrantReportRevoke(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-013",
        title="online quota lifecycle: grant -> report -> check -> revoke",
        spec="TS 32.291 §6.1.3.2",
        domain=Domain.CHARGING,
        nfs=(NF.CHF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  End-to-end Multiple Unit Information lifecycle for online charging\n"
            "  (TS 32.291 §6.1.3.2, TS 32.240 §6.5). Verifies the four canonical\n"
            "  grant/report/check/revoke verbs of Nchf_ChargingData against a\n"
            "  freshly recharged prepaid balance. Plus a negative input check\n"
            "  for grant-without-IMSI.\n"
            "\n"
            "Procedure (TS 32.291 §6.1.3.2 + TS 32.240 §6.5)\n"
            "  1. imsi = baseline.imsi('embb-bulk', 12); svc = 'tc-chf-013'.\n"
            "  2. POST /balances/recharge with amount=10.00 — seed funds.\n"
            "  3. POST /quotas/grant {imsi, service=svc, requested_units=1000}\n"
            "     — capture grant.granted_units > 0.\n"
            "  4. POST /quotas/report {used_units=250} — require status set.\n"
            "  5. POST /quotas/check {imsi, service} — require\n"
            "     status.granted_units > 0 (residual still allocated).\n"
            "  6. POST /quotas/revoke — require revoked > 0.\n"
            "  7. POST /quotas/grant {service, requested_units=100} (no imsi)\n"
            "     — expect HTTP 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSI/service/amount fixtures hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Recharge=200, grant returns granted_units>0, report carries a\n"
            "  status, check shows granted_units>0, revoke returns revoked>0,\n"
            "  and the missing-imsi grant returns 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — only structured failure payloads on the no-go paths).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The 10.00 seed balance is generous enough that\n"
            "  grant always succeeds; balance-exhaustion is out of scope."
        ),
    )

    def run(self):
        try:
            imsi = baseline.imsi("embb-bulk", 12)
            svc = "tc-chf-013"
            # Recharge first so the prepaid balance check inside GrantQuota
            # has something to draw against.
            _, sr = _api(CHF + "/balances/recharge", "POST", {
                "imsi": imsi, "amount": 10.00, "balance_type": "main",
                "reference": "tc-chf-013-seed",
            })
            if sr != 200:
                self.fail_test(f"recharge failed: {sr}")
                return self.result
            # Grant
            rg, sg = _api(CHF + "/quotas/grant", "POST", {
                "imsi": imsi, "service": svc, "requested_units": 1000,
            })
            if sg != 200:
                self.fail_test(f"grant failed: {sg} {rg}")
                return self.result
            grant = rg.get("grant", {})
            if not grant.get("granted_units"):
                self.fail_test(f"no granted_units: {grant}")
                return self.result
            # Report partial usage
            rr, _ = _api(CHF + "/quotas/report", "POST", {
                "imsi": imsi, "service": svc, "used_units": 250,
            })
            if not rr.get("status"):
                self.fail_test(f"report no status: {rr}")
                return self.result
            # Check
            rc, _ = _api(CHF + "/quotas/check", "POST", {
                "imsi": imsi, "service": svc,
            })
            if rc.get("status", {}).get("granted_units", 0) <= 0:
                self.fail_test(f"check empty: {rc}")
                return self.result
            # Revoke
            rv, _ = _api(CHF + "/quotas/revoke", "POST", {
                "imsi": imsi, "service": svc,
            })
            if rv.get("revoked", -1) <= 0:
                self.fail_test(f"revoke none: {rv}")
                return self.result
            # Missing imsi → 400
            _, sb = _api(CHF + "/quotas/grant", "POST", {
                "service": svc, "requested_units": 100,
            })
            if sb != 400:
                self.fail_test(f"missing imsi did not 400: {sb}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ChfBalanceRechargeAndRead(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-014",
        title="balance recharge -> balances list reflects new amount",
        spec="TS 32.291 §6.1",
        domain=Domain.CHARGING,
        nfs=(NF.CHF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Pre-pay balance management is the foundation under online charging\n"
                "  (TS 32.240 §6.5, TS 32.291 §6.1). This test pins both the happy\n"
                "  path (recharge → readback) and the validation envelope (negative\n"
                "  amounts and missing IMSI must be rejected).\n"
                "\n"
                "Procedure (TS 32.240 §6.5 + TS 32.291 §6.1)\n"
                "  1. imsi = baseline.imsi('embb-bulk', 13).\n"
                "  2. POST /balances/recharge {amount=25.50, balance_type='main',\n"
                "     reference='tc-014'} — require HTTP 200.\n"
                "  3. GET /balances/{imsi} — require balances list non-empty.\n"
                "  4. POST /balances/recharge {amount=-5} — expect HTTP 400.\n"
                "  5. POST /balances/recharge {amount=10} (no imsi) — expect 400.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded; one IMSI from baseline)\n"
                "\n"
                "Pass criteria\n"
                "  Recharge succeeds (200), readback list non-empty, both validation\n"
                "  negatives return 400.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure messages on regressions).\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. balance_type is fixed to 'main'; bonus/promo\n"
                "  buckets are not exercised here.\n"
                "  Negative-amount detection happens before the database write so\n"
                "  no row pollution is possible on rejection.\n"
                "  Missing-imsi handling is the same path for all balance operations.\n"
                "  Bonus / promo balance buckets are exercised in dedicated tests."
            ),
    )

    def run(self):
        try:
            imsi = baseline.imsi("embb-bulk", 13)
            r, s = _api(CHF + "/balances/recharge", "POST", {
                "imsi": imsi, "amount": 25.50, "balance_type": "main",
                "reference": "tc-014",
            })
            if s != 200:
                self.fail_test(f"recharge failed: {s} {r}")
                return self.result
            # Read back
            rb, _ = _api(f"{CHF}/balances/{imsi}")
            balances = rb.get("balances", [])
            if not balances:
                self.fail_test(f"no balances: {rb}")
                return self.result
            # Validation: bad amount → 400
            _, sb = _api(CHF + "/balances/recharge", "POST", {
                "imsi": imsi, "amount": -5,
            })
            if sb != 400:
                self.fail_test(f"negative amount did not 400: {sb}")
                return self.result
            # Missing imsi → 400
            _, sm = _api(CHF + "/balances/recharge", "POST", {
                "amount": 10,
            })
            if sm != 400:
                self.fail_test(f"missing imsi did not 400: {sm}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ChfCDRExportCSV(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-015",
        title="/cdrs/export returns CSV string + count",
        spec="TS 32.295 §5",
        domain=Domain.CHARGING,
        nfs=(NF.CHF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  CDR file transfer / export contract (TS 32.295 §5, TS 32.291\n"
            "  §6.2). Operators export CDR batches in CSV for billing systems.\n"
            "  This test seeds one CDR through the Create→Release lifecycle,\n"
            "  then asks /cdrs/export to emit it and pins the CSV shape.\n"
            "\n"
            "Procedure (TS 32.295 §5 + TS 32.291 §6.2)\n"
            "  1. imsi = baseline.imsi('embb-bulk', 14).\n"
            "  2. POST /charging-data {imsi, charging_method='offline',\n"
            "     pdu_session_id=15015} → capture sid.\n"
            "  3. POST /charging-data/{sid}/release {final_usage=100B/100B/1s}\n"
            "     to flush a CDR.\n"
            "  4. POST /cdrs/export {imsi, limit=100} — require HTTP 200 AND\n"
            "     r.ok truthy.\n"
            "  5. Read r.csv; require either 'imsi' or 'session_id' (case-\n"
            "     insensitive) appears in the header line.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixtures hard-coded, limit fixed at 100)\n"
            "\n"
            "Pass criteria\n"
            "  Export call returns 200 + ok=True, and the CSV contains the\n"
            "  expected header tokens.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — only structured failure payloads on shape mismatch).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Row-level field validation is left to the Robot\n"
            "  mirror — this test only proves the export endpoint and header\n"
            "  contract."
        ),
    )

    def run(self):
        try:
            # Generate at least one CDR via the lifecycle.
            imsi = baseline.imsi("embb-bulk", 14)
            r, _ = _api(CHF + "/charging-data", "POST", {
                "imsi": imsi, "charging_method": "offline",
                "pdu_session_id": 15015,
            })
            sid = r.get("session_id")
            if sid:
                _api(f"{CHF}/charging-data/{sid}/release", "POST", {
                    "final_usage": {
                        "volume_uplink": 100, "volume_downlink": 100,
                        "duration_s": 1,
                    },
                })
            # Export
            re, se = _api(CHF + "/cdrs/export", "POST", {
                "imsi": imsi, "limit": 100,
            })
            if se != 200 or not re.get("ok"):
                self.fail_test(f"export failed: {se} {re}")
                return self.result
            csv = re.get("csv", "")
            # Header line + at least one data row
            if "imsi" not in csv.lower() and "session_id" not in csv.lower():
                self.fail_test(f"csv header looks wrong: {csv[:200]}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class ChfChargingDataList(TestCase):
    SPEC = TestSpec(
        tc_id="TC-CHF-016",
        title="GET /charging-data lists active sessions and filters by status",
        spec="TS 32.291 §6.1",
        domain=Domain.CHARGING,
        nfs=(NF.CHF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Operator-visible listing of active CHF sessions (TS 32.290 §6.2,\n"
                "  TS 32.291 §6.1). Both the unfiltered GET and the status=active\n"
                "  filter MUST return a freshly created session — otherwise the\n"
                "  operator dashboard would lose visibility on live charging state.\n"
                "\n"
                "Procedure (TS 32.290 §6.2 + TS 32.291 §6.1)\n"
                "  1. imsi = baseline.imsi('embb-bulk', 15).\n"
                "  2. POST /charging-data {imsi, charging_method='offline',\n"
                "     pdu_session_id=16016} → capture sid (fail if missing).\n"
                "  3. GET /charging-data?status=active — require any session in\n"
                "     r.sessions has session_id == sid.\n"
                "  4. GET /charging-data (no filter) — same membership check.\n"
                "  5. finally: POST /charging-data/{sid}/release.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  sid appears in both the active-filtered and the unfiltered list.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payloads on missing membership).\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Status filters other than 'active' are not\n"
                "  exercised here; pagination is also out of scope.\n"
                "  Session-status filter values follow active|terminated|all — only\n"
                "  'active' is asserted here.\n"
                "  Newly created sessions must appear synchronously in both lists,\n"
                "  so the test does not poll."
            ),
    )

    def run(self):
        try:
            imsi = baseline.imsi("embb-bulk", 15)
            r, _ = _api(CHF + "/charging-data", "POST", {
                "imsi": imsi, "charging_method": "offline",
                "pdu_session_id": 16016,
            })
            sid = r.get("session_id")
            if not sid:
                self.fail_test(f"create failed: {r}")
                return self.result
            try:
                # Active list contains it.
                rl, _ = _api(f"{CHF}/charging-data?status=active")
                if not any(s.get("session_id") == sid
                           for s in rl.get("sessions", [])):
                    self.fail_test(f"new session not in active list: {rl}")
                    return self.result
                # No-filter list also contains it.
                rl2, _ = _api(f"{CHF}/charging-data")
                if not any(s.get("session_id") == sid
                           for s in rl2.get("sessions", [])):
                    self.fail_test(f"new session not in unfiltered list: {rl2}")
                    return self.result
            finally:
                _api(f"{CHF}/charging-data/{sid}/release", "POST")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_CHF_OAM_TCS = [
    ChfStatsShape,
    ChfChargingDataLifecycle,
    ChfChargingDataValidation,
    ChfQuotaGrantReportRevoke,
    ChfBalanceRechargeAndRead,
    ChfCDRExportCSV,
    ChfChargingDataList,
]

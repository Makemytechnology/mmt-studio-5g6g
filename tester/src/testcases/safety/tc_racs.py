# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Restricted Access Control (RACS).

TS 22.011 §4         Service accessibility umbrella.
TS 22.261 §6.13      Access control requirements (priority + barring).
TS 23.501 §5.18      Service Continuity, including AC restrictions.
TS 24.501 §4.5       Unified Access Control (operator barring categories).

Drives the SA Core REST surface at /api/racs/*: the four restriction
levels (normal, restricted, emergency_only, full_lockdown), per-
access-category barring factors, the per-IMSI admission gate, and
the audit log. Endpoints return flat objects (or arrays) keyed by
domain noun — no `{ok, ...}` wrapping.
"""

import json
import logging
import urllib.request
import urllib.error

from src import baseline
from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_racs")


def _racs_api(path, method="GET", body=None):
    """Call SA Core RACS REST API."""
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


def _deactivate():
    _racs_api("/api/racs/deactivate", "POST")


class RacsActivateRestriction(TestCase):
    """TC-RACS-001: Activate + read back the restriction level."""
    SPEC = TestSpec(
        tc_id="TC-RACS-001",
        title="RACS activate a restriction level and read it back",
        spec="TS 23.501 §5.18",
        domain=Domain.IDLE_MODE,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.502 §4.2.10 + TS 23.501 §5.18 — operators must be\n"
            "  able to activate one of the four restriction levels\n"
            "  (normal, restricted, emergency_only, full_lockdown) and\n"
            "  read it back. This smoke gate verifies POST /activate\n"
            "  round-trips the level and reason fields exactly.\n"
            "\n"
            "Procedure (TS 23.501 §5.18)\n"
            "  1. _deactivate() — clear any prior restriction state.\n"
            "  2. POST /api/racs/activate with body\n"
            "     {level: 'restricted', reason: 'TC-RACS-001 drill',\n"
            "      areas: 'TAC-001,TAC-002'}.\n"
            "  3. Assert HTTP 200.\n"
            "  4. Assert response.restriction_level == 'restricted'.\n"
            "  5. Assert response.reason == 'TC-RACS-001 drill'.\n"
            "  6. finally: _deactivate() to restore normal state.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed restriction payload).\n"
            "\n"
            "Pass criteria\n"
            "  Activate returns HTTP 200 AND restriction_level/reason\n"
            "  round-trip exactly.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Areas string is a comma-separated TAC list; not parsed into\n"
            "  individual TAC validation by the operator surface."
        ),
    )

    def run(self):
        try:
            _deactivate()
            r, s = _racs_api("/api/racs/activate", "POST", {
                "level": "restricted",
                "reason": "TC-RACS-001 drill",
                "areas": "TAC-001,TAC-002",
            })
            if s != 200:
                self.fail_test(f"activate failed: {s} {r}")
                return self.result
            if r.get("restriction_level") != "restricted":
                self.fail_test(f"level mismatch: {r}")
                return self.result
            if r.get("reason") != "TC-RACS-001 drill":
                self.fail_test(f"reason not persisted: {r}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _deactivate()
        return self.result


class RacsValidation(TestCase):
    """TC-RACS-002: Invalid level / out-of-range barring → 400."""
    SPEC = TestSpec(
        tc_id="TC-RACS-002",
        title="RACS rejects invalid level and out-of-range barring",
        spec="TS 24.501 §4.5",
        domain=Domain.IDLE_MODE,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 24.501 §4.5 Unified Access Control reserves access\n"
            "  categories 0..63 and barring factors in [0.0, 1.0]. The\n"
            "  SA-Core validators on /activate and /barring must reject\n"
            "  out-of-set levels, out-of-range categories, and factors > 1\n"
            "  before any state changes — drives reliable AC barring.\n"
            "\n"
            "Procedure (TS 24.501 §4.5 + TS 23.502 §4.2.10)\n"
            "  1. POST /api/racs/activate with body {level: 'BAD'}.\n"
            "  2. Assert HTTP 400 — invalid level rejected.\n"
            "  3. POST /api/racs/barring with access_category=99,\n"
            "     barring_factor=0.5, barring_time_s=60.\n"
            "  4. Assert HTTP 400 — out-of-range category rejected.\n"
            "  5. POST /api/racs/barring with access_category=0,\n"
            "     barring_factor=1.5, barring_time_s=60.\n"
            "  6. Assert HTTP 400 — factor > 1 rejected.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed negative-case table).\n"
            "\n"
            "Pass criteria\n"
            "  All three negative POSTs return HTTP 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Only the validator is exercised; NGAP RRC RejectCause\n"
            "  emission on real UE access is not driven."
        ),
    )

    def run(self):
        try:
            r, s = _racs_api("/api/racs/activate", "POST", {"level": "BAD"})
            if s != 400:
                self.fail_test(f"bad level did not 400: {s} {r}")
                return self.result

            r2, s2 = _racs_api("/api/racs/barring", "POST", {
                "access_category": 99, "barring_factor": 0.5, "barring_time_s": 60,
            })
            if s2 != 400:
                self.fail_test(f"out-of-range cat did not 400: {s2} {r2}")
                return self.result

            r3, s3 = _racs_api("/api/racs/barring", "POST", {
                "access_category": 0, "barring_factor": 1.5, "barring_time_s": 60,
            })
            if s3 != 400:
                self.fail_test(f"factor>1 did not 400: {s3} {r3}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class RacsFullLockdown(TestCase):
    """TC-RACS-003: full_lockdown denies every access (TS 22.261 §6.13)."""
    SPEC = TestSpec(
        tc_id="TC-RACS-003",
        title="RACS full_lockdown denies every access category",
        spec="TS 22.261 §6.13",
        domain=Domain.IDLE_MODE,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  TS 22.261 §6.13 requires a 'full lockdown' admission level\n"
            "  that denies all access — including category 2 (emergency)\n"
            "  and category 7 (MMTel-voice). This TC pins that under\n"
            "  /activate level=full_lockdown the /check-access admission\n"
            "  gate denies every category probed.\n"
            "\n"
            "Procedure (TS 22.261 §6.13 + TS 24.501 §4.5)\n"
            "  1. _deactivate() — clear state.\n"
            "  2. POST /api/racs/activate with level='full_lockdown',\n"
            "     reason='tc'.\n"
            "  3. For each access_category in (0, 2, 7) — MO data,\n"
            "     emergency, MMTel-voice:\n"
            "     a. POST /api/racs/check-access with baseline IMSI #0\n"
            "        and the category.\n"
            "     b. Assert HTTP 200 (endpoint must answer).\n"
            "     c. Assert response.allowed is falsy.\n"
            "  4. finally: _deactivate() to restore normal.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — baseline IMSI from src.baseline).\n"
            "\n"
            "Pass criteria\n"
            "  All three category probes return allowed=False under\n"
            "  full_lockdown.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  full_lockdown DOES deny emergency — operators wanting\n"
            "  emergency-only should use level=emergency_only (TC-RACS-004)."
        ),
    )

    def run(self):
        try:
            _deactivate()
            _racs_api("/api/racs/activate", "POST",
                      {"level": "full_lockdown", "reason": "tc"})

            for cat in (0, 2, 7):  # MO data, emergency, MMTel-voice
                r, s = _racs_api("/api/racs/check-access", "POST",
                                  {"imsi": baseline.imsi("embb-bulk", 0),
                                   "access_category": cat})
                if s != 200:
                    self.fail_test(f"check failed cat={cat}: {s} {r}")
                    return self.result
                if r.get("allowed"):
                    self.fail_test(f"cat={cat} admitted under lockdown",
                                   body=r)
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _deactivate()
        return self.result


class RacsEmergencyOnly(TestCase):
    """TC-RACS-004: emergency_only admits cat=2, denies others."""
    SPEC = TestSpec(
        tc_id="TC-RACS-004",
        title="RACS emergency_only admits emergency, denies other categories",
        spec="TS 24.501 §4.5",
        domain=Domain.IDLE_MODE,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  TS 24.501 §4.5 defines an emergency_only barring profile:\n"
            "  category 2 (emergency) is admitted while every other AC\n"
            "  category is denied. This TC pins both halves of the\n"
            "  contract by probing /check-access for cat=2 (must admit)\n"
            "  and cat=0 (MO data — must deny).\n"
            "\n"
            "Procedure (TS 24.501 §4.5 + TS 23.501 §5.18)\n"
            "  1. _deactivate() — clear state.\n"
            "  2. POST /api/racs/activate with level='emergency_only'.\n"
            "  3. POST /api/racs/check-access with baseline IMSI #1 and\n"
            "     access_category=2.\n"
            "  4. Assert HTTP 200 AND response.allowed is truthy.\n"
            "  5. POST /api/racs/check-access with the same IMSI and\n"
            "     access_category=0.\n"
            "  6. Assert response.allowed is falsy.\n"
            "  7. finally: _deactivate() to restore normal.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — baseline IMSI from src.baseline).\n"
            "\n"
            "Pass criteria\n"
            "  cat=2 admitted (allowed=True) AND cat=0 denied (allowed=False).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Only categories 0 and 2 are probed; full conformance over\n"
            "  every AC category lives in unit tests."
        ),
    )

    def run(self):
        try:
            _deactivate()
            _racs_api("/api/racs/activate", "POST",
                      {"level": "emergency_only"})

            # cat=2 (emergency) MUST admit.
            r, s = _racs_api("/api/racs/check-access", "POST",
                              {"imsi": baseline.imsi("embb-bulk", 1), "access_category": 2})
            if s != 200 or not r.get("allowed"):
                self.fail_test(f"emergency denied: {r}")
                return self.result

            # cat=0 (MO data) MUST deny.
            r2, s2 = _racs_api("/api/racs/check-access", "POST",
                                {"imsi": baseline.imsi("embb-bulk", 1),
                                 "access_category": 0})
            if r2.get("allowed"):
                self.fail_test(f"non-emergency admitted: {r2}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _deactivate()
        return self.result


class RacsBarringConfig(TestCase):
    """TC-RACS-005: SetBarringFactor + GetBarringConfigs roundtrip."""
    SPEC = TestSpec(
        tc_id="TC-RACS-005",
        title="RACS per-category barring factor set/get/reset roundtrip",
        spec="TS 24.501 §4.5",
        domain=Domain.IDLE_MODE,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 24.501 §4.5 Unified Access Control parameters\n"
            "  uac-BarringFactor (0..1) and uac-BarringTime (s) are set\n"
            "  per access category. The SA-Core /barring CRUD must accept\n"
            "  POST, surface the row via GET with enabled=True, and reset\n"
            "  to defaults (factor=1.0, enabled=False) on DELETE.\n"
            "\n"
            "Procedure (TS 24.501 §4.5)\n"
            "  1. POST /api/racs/barring with access_category=7,\n"
            "     barring_factor=0.4, barring_time_s=32.\n"
            "  2. Assert HTTP 200.\n"
            "  3. GET /api/racs/barring; locate row with cat=7.\n"
            "  4. Assert row.barring_factor ~= 0.4 (within 1e-3).\n"
            "  5. Assert row.enabled in (1, True).\n"
            "  6. DELETE /api/racs/barring/7 — resets factor=1.0, disabled.\n"
            "  7. GET /barring again; assert the row (if present) has\n"
            "     enabled NOT in (1, True).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed category, factor, time).\n"
            "\n"
            "Pass criteria\n"
            "  SET round-trips factor + enabled=True AND DELETE disables.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  barring_factor float compared with abs() < 1e-3 tolerance\n"
            "  to absorb JSON serialisation rounding."
        ),
    )

    def run(self):
        try:
            r, s = _racs_api("/api/racs/barring", "POST", {
                "access_category": 7,
                "barring_factor": 0.4,
                "barring_time_s": 32,
            })
            if s != 200:
                self.fail_test(f"set barring failed: {s} {r}")
                return self.result

            cfg, _ = _racs_api("/api/racs/barring")
            row = next((c for c in cfg if c.get("access_category") == 7), None)
            if row is None:
                self.fail_test("cat=7 missing from barring list",
                               sample=cfg[:3])
                return self.result
            if abs(float(row.get("barring_factor", 0)) - 0.4) > 1e-3:
                self.fail_test(f"barring_factor mismatch: {row}")
                return self.result
            if row.get("enabled") not in (1, True):
                self.fail_test(f"enabled flag wrong: {row}")
                return self.result

            # Reset via DELETE — factor=1.0, disabled.
            _racs_api("/api/racs/barring/7", "DELETE")
            cfg2, _ = _racs_api("/api/racs/barring")
            row2 = next((c for c in cfg2 if c.get("access_category") == 7), None)
            if row2 and row2.get("enabled") in (1, True):
                self.fail_test(f"reset did not disable: {row2}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class RacsAccessLog(TestCase):
    """TC-RACS-006: Audit log records check decisions."""
    SPEC = TestSpec(
        tc_id="TC-RACS-006",
        title="RACS access-log records both allowed and barred decisions",
        spec="TS 22.261 §6.13",
        domain=Domain.IDLE_MODE,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Every admission decision must be auditable. /access-log\n"
            "  is the per-IMSI ledger that records allowed / barred\n"
            "  outcomes from /check-access. This TC drives one barred\n"
            "  decision under full_lockdown and asserts the log captures\n"
            "  it. TS 22.261 §6.13.\n"
            "\n"
            "Procedure (TS 22.261 §6.13)\n"
            "  1. _deactivate() — start clean.\n"
            "  2. POST /api/racs/activate level=full_lockdown.\n"
            "  3. POST /api/racs/check-access with baseline IMSI #49,\n"
            "     access_category=0 (must be barred).\n"
            "  4. _deactivate() — lift restriction.\n"
            "  5. POST /api/racs/check-access for the same IMSI/category\n"
            "     (would be allowed now).\n"
            "  6. GET /api/racs/access-log?limit=20.\n"
            "  7. Collect set of decision strings.\n"
            "  8. Assert 'barred' is in the decision set.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — baseline IMSI #49).\n"
            "\n"
            "Pass criteria\n"
            "  /access-log contains at least one row with decision='barred'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  decisions — list of distinct decision values found.\n"
            "\n"
            "Known constraints\n"
            "  Log is rolling — older entries beyond limit are not\n"
            "  available; concurrent TCs may race against the same log."
        ),
    )

    def run(self):
        try:
            _deactivate()
            _racs_api("/api/racs/activate", "POST",
                      {"level": "full_lockdown"})
            _racs_api("/api/racs/check-access", "POST",
                      {"imsi": baseline.imsi("embb-bulk", 49), "access_category": 0})
            _deactivate()
            _racs_api("/api/racs/check-access", "POST",
                      {"imsi": baseline.imsi("embb-bulk", 49), "access_category": 0})

            log_rows, _ = _racs_api("/api/racs/access-log?limit=20")
            decisions = {row.get("decision") for row in log_rows}
            if "barred" not in decisions:
                self.fail_test(f"no barred entry: {decisions}")
                return self.result
            self.pass_test(decisions=list(decisions))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _deactivate()
        return self.result


class RacsStats(TestCase):
    """TC-RACS-007: Stats reports cumulative allow/bar counts."""
    SPEC = TestSpec(
        tc_id="TC-RACS-007",
        title="RACS stats reports cumulative allow/bar counts",
        spec="TS 22.261 §6.13",
        domain=Domain.IDLE_MODE,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("smoke",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the GUI / observability schema: the operator console\n"
            "  reads three RACS counters — total (admissions probed),\n"
            "  allowed (admissions granted), barred (admissions denied).\n"
            "  Any rename or drop breaks the panel. This TC asserts all\n"
            "  three keys are present on /stats. TS 22.261 §6.13.\n"
            "\n"
            "Procedure (TS 22.261 §6.13)\n"
            "  1. GET /api/racs/stats with no query params.\n"
            "  2. Assert HTTP 200.\n"
            "  3. For each of {total, allowed, barred} assert the key is\n"
            "     present in the response body.\n"
            "  4. fail_test on first missing key with the offending body.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — pure GET).\n"
            "\n"
            "Pass criteria\n"
            "  All three counter keys are present at the top level of\n"
            "  the /stats response.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  stats — full /stats body, passed through to result.details.\n"
            "\n"
            "Known constraints\n"
            "  Counter values are not asserted (they depend on TC run\n"
            "  order); only schema-shape conformance is checked."
        ),
    )

    def run(self):
        try:
            r, s = _racs_api("/api/racs/stats")
            if s != 200:
                self.fail_test(f"stats failed: {s} {r}")
                return self.result
            for k in ("total", "allowed", "barred"):
                if k not in r:
                    self.fail_test(f"missing key {k}", body=r)
                    return self.result
            self.pass_test(stats=r)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_RACS_TCS = [
    RacsActivateRestriction,
    RacsValidation,
    RacsFullLockdown,
    RacsEmergencyOnly,
    RacsBarringConfig,
    RacsAccessLog,
    RacsStats,
]

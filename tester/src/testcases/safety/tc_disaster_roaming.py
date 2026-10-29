# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Disaster Roaming.

TS 23.501 §5.40         Disaster Roaming for PLMNs (5GC architecture).
TS 23.501 §5.40.2       Disaster condition handling — declaration lifecycle.
TS 23.501 §5.40.3       Restrictions of services and applications.
TS 22.261 §6.31         Service requirements: Disaster Roaming.

Drives the SA Core REST surface at /api/disaster-roaming/*: the
declaration ledger, the per-IMSI admission gate, the active-roaming
UE register, and the audit log. Endpoints return flat objects (or
arrays) keyed by domain noun — no `{ok, ...}` wrapping.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_disaster_roaming")


def _dr_api(path, method="GET", body=None):
    """Call SA Core Disaster Roaming REST API."""
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


def _declare(name="DR-test", reason="natural", areas="Test"):
    return _dr_api("/api/disaster-roaming/declare", "POST", {
        "name": name, "reason": reason, "affected_areas": areas,
    })


def _end_all():
    _dr_api("/api/disaster-roaming/end", "POST")


class DrDeclareDisaster(TestCase):
    """TC-DR-001: Declare → status reflects disaster_active=true."""
    SPEC = TestSpec(
        tc_id="TC-DR-001",
        title="Disaster Roaming declaration flips status to disaster_active",
        spec="TS 23.501 §5.40.2",
        domain=Domain.ROAMING,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the disaster-declaration lifecycle: an operator POST must\n"
            "  flip the SA-Core ledger to disaster_active=True and round-trip\n"
            "  the declaration_id / name, gating every subsequent partner-PLMN\n"
            "  admission decision. TS 22.261 §6.31 + TS 23.501 §5.40.2.\n"
            "\n"
            "Procedure (TS 23.501 §5.40.2 + TS 22.261 §6.31)\n"
            "  1. _end_all() — POST /api/disaster-roaming/end clears any prior\n"
            "     declaration so the test starts from a known inactive state.\n"
            "  2. _declare('Earthquake-001', 'natural', 'Tokyo') — POST\n"
            "     /api/disaster-roaming/declare with name + reason + areas.\n"
            "  3. Assert HTTP 200/201 and that a declaration_id was minted.\n"
            "  4. GET /api/disaster-roaming/status; assert disaster_active=True\n"
            "     and declaration.name == 'Earthquake-001'.\n"
            "  5. finally: _end_all() resets the ledger for the next TC.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed declaration name and reason).\n"
            "\n"
            "Pass criteria\n"
            "  declare returns 200/201 with a declaration_id AND\n"
            "  status.disaster_active is truthy AND declaration.name matches.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  declaration_id.\n"
            "\n"
            "Known constraints\n"
            "  Uses the SA-Core REST surface only — the actual N2/N1 PLMN\n"
            "  selection-list update path is not exercised here."
        ),
    )

    def run(self):
        try:
            _end_all()
            r, s = _declare("Earthquake-001", "natural", "Tokyo")
            if s not in (200, 201) or not r.get("declaration_id"):
                self.fail_test(f"Declare failed: {s} {r}")
                return self.result

            st, _ = _dr_api("/api/disaster-roaming/status")
            if not st.get("disaster_active"):
                self.fail_test("disaster_active not true after declare",
                               status=st)
                return self.result
            if (st.get("declaration") or {}).get("name") != "Earthquake-001":
                self.fail_test("declaration name mismatch", status=st)
                return self.result
            self.pass_test(declaration_id=r.get("declaration_id"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _end_all()
        return self.result


class DrValidation(TestCase):
    """TC-DR-002: Empty name → 400 (TS 23.501 §5.40.2)."""
    SPEC = TestSpec(
        tc_id="TC-DR-002",
        title="Disaster Roaming rejects bad declare and bad check payloads",
        spec="TS 23.501 §5.40.2",
        domain=Domain.ROAMING,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Negative-path conformance: payload validation on the disaster-\n"
            "  roaming REST surface. An empty declaration name and a /check\n"
            "  missing the mandatory hplmn field must be refused before any\n"
            "  state changes — protects the partner-PLMN admission gate from\n"
            "  garbage input. TS 23.501 §5.40.2.\n"
            "\n"
            "Procedure (TS 23.501 §5.40.2)\n"
            "  1. POST /api/disaster-roaming/declare with body {'name': ''}.\n"
            "  2. Assert HTTP 400 — empty name must be rejected.\n"
            "  3. POST /api/disaster-roaming/check with only an IMSI (hplmn\n"
            "     omitted) — the check requires both keys per the spec.\n"
            "  4. Assert HTTP 400 — missing hplmn must be rejected.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payloads are hard-coded negative cases).\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return HTTP 400 (not 200, not 5xx).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Only exercises the REST input-validation layer; downstream NAS\n"
            "  Cause-code mapping is checked by the matching robot suite."
        ),
    )

    def run(self):
        try:
            r, s = _dr_api("/api/disaster-roaming/declare", "POST",
                            {"name": ""})
            if s != 400:
                self.fail_test(f"Empty name did not 400: {s} {r}")
                return self.result

            # /check requires both imsi+hplmn.
            r2, s2 = _dr_api("/api/disaster-roaming/check", "POST",
                              {"imsi": "001011234567"})
            if s2 != 400:
                self.fail_test(f"Missing hplmn did not 400: {s2} {r2}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class DrCheckRoaming(TestCase):
    """TC-DR-003: /check admits a partner UE while disaster is active."""
    SPEC = TestSpec(
        tc_id="TC-DR-003",
        title="Disaster Roaming /check admits partner UE during disaster",
        spec="TS 23.501 §5.40.3",
        domain=Domain.ROAMING,
        nfs=(NF.AMF, NF.UDM),
        severity=Severity.BLOCKER,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the admission contract: while a disaster declaration is\n"
            "  active, partner-PLMN UEs must be admitted by /check and the\n"
            "  active-roaming-UE register must surface them. This is the\n"
            "  positive counterpart to TC-DR-004 (deny-when-inactive).\n"
            "  TS 22.261 §6.31 + TS 23.501 §5.40.3.\n"
            "\n"
            "Procedure (TS 23.501 §5.40.3)\n"
            "  1. _end_all() — start from a known-inactive ledger.\n"
            "  2. _declare('Earthquake-002') — open a disaster window.\n"
            "  3. POST /api/disaster-roaming/check with IMSI 44010123456001\n"
            "     and hplmn=44010 (a partner-PLMN).\n"
            "  4. Assert HTTP 200 and allowed=True.\n"
            "  5. GET /api/disaster-roaming/roaming-ues; assert the IMSI now\n"
            "     appears in the active register.\n"
            "  6. finally: _end_all() clears state for the next TC.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed partner IMSI and HPLMN).\n"
            "\n"
            "Pass criteria\n"
            "  check returns 200/allowed=True AND IMSI present in roaming-ues.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  reason — the admission reason returned by /check.\n"
            "\n"
            "Known constraints\n"
            "  Exercises only the REST admission gate; the AMF Registration\n"
            "  Accept with 5GS Mobility Identity for roaming is covered in\n"
            "  the matching robot scenario."
        ),
    )

    def run(self):
        try:
            _end_all()
            _declare("Earthquake-002")

            r, s = _dr_api("/api/disaster-roaming/check", "POST", {
                "imsi": "44010123456001",
                "hplmn": "44010",
            })
            if s != 200 or not r.get("allowed"):
                self.fail_test(f"check did not allow: {s} {r}")
                return self.result

            # IMSI now appears in the active-roaming-UE register.
            ues, _ = _dr_api("/api/disaster-roaming/roaming-ues")
            if not any(u.get("imsi") == "44010123456001" for u in ues):
                self.fail_test("IMSI missing from roaming-ues",
                               sample=ues[:3])
                return self.result

            self.pass_test(reason=r.get("reason"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _end_all()
        return self.result


class DrDenyWhenInactive(TestCase):
    """TC-DR-004: /check denies when no disaster is active."""
    SPEC = TestSpec(
        tc_id="TC-DR-004",
        title="Disaster Roaming /check denies when no disaster active",
        spec="TS 23.501 §5.40.2",
        domain=Domain.ROAMING,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Disaster Roaming must be opt-in: when no declaration is in\n"
            "  effect a partner-PLMN UE must NOT be admitted via /check. This\n"
            "  pins the default-deny behaviour of the admission gate.\n"
            "  TS 23.501 §5.40.2 + TS 22.261 §6.31.\n"
            "\n"
            "Procedure (TS 23.501 §5.40.2)\n"
            "  1. _end_all() — explicitly clear any active declaration.\n"
            "  2. POST /api/disaster-roaming/check with IMSI 44010123456999\n"
            "     and hplmn=44010 (partner PLMN).\n"
            "  3. Assert HTTP 200 (the endpoint must answer, not error).\n"
            "  4. Assert allowed=False — admission must be denied while the\n"
            "     gate is off.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed partner IMSI and HPLMN).\n"
            "\n"
            "Pass criteria\n"
            "  check returns HTTP 200 AND allowed is falsy.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  reason — the deny reason returned by /check.\n"
            "\n"
            "Known constraints\n"
            "  Only checks the operator-side REST gate. NAS Cause #11\n"
            "  (PLMN not allowed) mapping is verified by the robot suite."
        ),
    )

    def run(self):
        try:
            _end_all()  # ensure inactive
            r, s = _dr_api("/api/disaster-roaming/check", "POST", {
                "imsi": "44010123456999",
                "hplmn": "44010",
            })
            if s != 200:
                self.fail_test(f"check failed: {s} {r}")
                return self.result
            if r.get("allowed"):
                self.fail_test("Roaming admitted with no disaster",
                               body=r)
                return self.result
            self.pass_test(reason=r.get("reason"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class DrRoamingUes(TestCase):
    """TC-DR-005: Active-roaming-UE register grows with each /check admit."""
    SPEC = TestSpec(
        tc_id="TC-DR-005",
        title="Disaster Roaming roaming-UE register accumulates admits",
        spec="TS 23.501 §5.40.3",
        domain=Domain.ROAMING,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  The active-roaming-UE register must accumulate every admit\n"
            "  during a declaration and clear once the declaration ends —\n"
            "  the register is what dashboards and audit consume. This TC\n"
            "  pins both halves of that contract.\n"
            "  TS 23.501 §5.40.3 + TS 22.261 §6.31.\n"
            "\n"
            "Procedure (TS 23.501 §5.40.3)\n"
            "  1. _end_all(); _declare('Typhoon-005') — fresh declaration.\n"
            "  2. POST /check three times for IMSIs ...456101, ...456102,\n"
            "     ...456103 (all on partner PLMN 44010).\n"
            "  3. GET /api/disaster-roaming/roaming-ues; assert all three\n"
            "     IMSIs appear in the returned set.\n"
            "  4. _end_all() — end the disaster; GetDisasterRoamingUEs joins\n"
            "     against active declarations so the rows must drop.\n"
            "  5. GET /roaming-ues again; assert none of the three IMSIs\n"
            "     are still surfaced.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — three fixed partner IMSIs).\n"
            "\n"
            "Pass criteria\n"
            "  All three IMSIs are present during the disaster AND none are\n"
            "  present after _end_all().\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  active_ues — count of UEs still active after end (expected 0).\n"
            "\n"
            "Known constraints\n"
            "  The audit row is kept in the DB after end; only the join with\n"
            "  active declarations filters it from the visible register."
        ),
    )

    def run(self):
        try:
            _end_all()
            _declare("Typhoon-005")

            for imsi in ("44010123456101",
                         "44010123456102",
                         "44010123456103"):
                _dr_api("/api/disaster-roaming/check", "POST",
                         {"imsi": imsi, "hplmn": "44010"})

            ues, _ = _dr_api("/api/disaster-roaming/roaming-ues")
            seen = {u.get("imsi") for u in ues}
            for needed in ("44010123456101",
                           "44010123456102",
                           "44010123456103"):
                if needed not in seen:
                    self.fail_test(f"missing IMSI {needed}", sample=ues[:3])
                    return self.result

            # End the disaster — register clears (UEs only show while
            # the declaration's status='active'; the row is kept for
            # audit but GetDisasterRoamingUEs filters via JOIN).
            _end_all()
            ues2, _ = _dr_api("/api/disaster-roaming/roaming-ues")
            seen2 = {u.get("imsi") for u in ues2}
            for needed in ("44010123456101",
                           "44010123456102",
                           "44010123456103"):
                if needed in seen2:
                    self.fail_test(f"IMSI {needed} still active after end",
                                   sample=ues2[:3])
                    return self.result

            self.pass_test(active_ues=len(seen2))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _end_all()
        return self.result


class DrDeclarationHistory(TestCase):
    """TC-DR-006: Declarations endpoint returns active + ended history."""
    SPEC = TestSpec(
        tc_id="TC-DR-006",
        title="Disaster Roaming declarations history lists past + active",
        spec="TS 23.501 §5.40.2",
        domain=Domain.ROAMING,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  The /declarations endpoint is the operator-facing history of\n"
            "  every disaster window. It must list both active and ended\n"
            "  declarations with their status, so audit and report tooling\n"
            "  can reconstruct any past event. TS 23.501 §5.40.2.\n"
            "\n"
            "Procedure (TS 23.501 §5.40.2)\n"
            "  1. _end_all() — start clean.\n"
            "  2. _declare('Flood-006') — open a window.\n"
            "  3. _end_all() — close it.\n"
            "  4. GET /api/disaster-roaming/declarations; collect the set of\n"
            "     declaration names.\n"
            "  5. Assert 'Flood-006' is in the returned names.\n"
            "  6. Assert the matching row's status is one of {active, ended}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed declaration name).\n"
            "\n"
            "Pass criteria\n"
            "  Declaration appears in /declarations AND status is in the\n"
            "  permitted set {active, ended}.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  count — total number of declaration rows returned.\n"
            "\n"
            "Known constraints\n"
            "  History is unbounded in this build — old declarations are not\n"
            "  pruned, so /declarations grows over time."
        ),
    )

    def run(self):
        try:
            _end_all()
            _declare("Flood-006")
            _end_all()

            decls, _ = _dr_api("/api/disaster-roaming/declarations")
            names = {d.get("name") for d in decls}
            if "Flood-006" not in names:
                self.fail_test(f"declaration missing: {names}")
                return self.result
            # Ended declarations show status='ended'.
            ended = [d for d in decls if d.get("name") == "Flood-006"]
            if ended and ended[0].get("status") not in ("ended", "active"):
                self.fail_test("status not in {active,ended}",
                               sample=ended)
                return self.result
            self.pass_test(count=len(decls))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class DrAuditLog(TestCase):
    """TC-DR-007: /log records admit + deny entries."""
    SPEC = TestSpec(
        tc_id="TC-DR-007",
        title="Disaster Roaming audit log records admit and deny",
        spec="TS 23.501 §5.40.3",
        domain=Domain.ROAMING,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Every admission decision must be auditable. The /log endpoint\n"
            "  is the per-IMSI ledger of admit / deny actions; this TC pins\n"
            "  that both classes of decision are recorded across declaration\n"
            "  boundaries. TS 23.501 §5.40.3.\n"
            "\n"
            "Procedure (TS 23.501 §5.40.3)\n"
            "  1. _end_all(); _declare('Tsunami-007').\n"
            "  2. POST /check for IMSI ...456777 (partner-PLMN) — this is\n"
            "     admitted while the declaration is active.\n"
            "  3. _end_all() — close the window.\n"
            "  4. POST /check for IMSI ...456778 — this is denied now that\n"
            "     no declaration is active.\n"
            "  5. GET /api/disaster-roaming/log?limit=20; collect the\n"
            "     set of action strings.\n"
            "  6. Assert at least one of 'admitted' or 'denied' is present.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed partner IMSIs).\n"
            "\n"
            "Pass criteria\n"
            "  Log contains 'admitted' OR 'denied' in the action column.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  actions — list of distinct action values found in /log.\n"
            "\n"
            "Known constraints\n"
            "  The log endpoint caps at limit rows; older entries may be\n"
            "  truncated. Race conditions across parallel TCs are possible."
        ),
    )

    def run(self):
        try:
            _end_all()
            _declare("Tsunami-007")
            _dr_api("/api/disaster-roaming/check", "POST",
                     {"imsi": "44010123456777", "hplmn": "44010"})
            _end_all()
            # After end, check denies again.
            _dr_api("/api/disaster-roaming/check", "POST",
                     {"imsi": "44010123456778", "hplmn": "44010"})

            log_rows, _ = _dr_api("/api/disaster-roaming/log?limit=20")
            actions = {row.get("action") for row in log_rows}
            if "admitted" not in actions and "denied" not in actions:
                self.fail_test(f"no admit/deny in log: {actions}")
                return self.result
            self.pass_test(actions=list(actions))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _end_all()
        return self.result


class DrStats(TestCase):
    """TC-DR-008: Stats reports cumulative admit/deny counts."""
    SPEC = TestSpec(
        tc_id="TC-DR-008",
        title="Disaster Roaming stats reports declaration + admit counters",
        spec="TS 23.501 §5.40",
        domain=Domain.ROAMING,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("smoke",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  GUI/observability surface contract: the /stats endpoint must\n"
            "  expose the four counters the operator console reads. Any drift\n"
            "  (missing key, renamed key) breaks the dashboard, so this TC\n"
            "  pins the schema. TS 23.501 §5.40.\n"
            "\n"
            "Procedure (TS 23.501 §5.40)\n"
            "  1. GET /api/disaster-roaming/stats with no query params.\n"
            "  2. Assert HTTP 200.\n"
            "  3. For each required key — total_declarations, total_admitted,\n"
            "     total_denied, current_roaming_ues — assert it is present\n"
            "     in the returned body.\n"
            "  4. fail_test on first missing key with the offending body.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — pure GET).\n"
            "\n"
            "Pass criteria\n"
            "  All four counters are present as top-level keys in the body.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  stats — the full /stats body, passed through to result.details.\n"
            "\n"
            "Known constraints\n"
            "  Counter values are not asserted (they depend on other TCs\n"
            "  in the run order); only key presence is checked."
        ),
    )

    def run(self):
        try:
            r, s = _dr_api("/api/disaster-roaming/stats")
            if s != 200:
                self.fail_test(f"stats failed: {s} {r}")
                return self.result
            for k in ("total_declarations", "total_admitted", "total_denied",
                      "current_roaming_ues"):
                if k not in r:
                    self.fail_test(f"missing key {k}", body=r)
                    return self.result
            self.pass_test(stats=r)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_DISASTER_ROAMING_TCS = [
    DrDeclareDisaster,
    DrValidation,
    DrCheckRoaming,
    DrDenyWhenInactive,
    DrRoamingUes,
    DrDeclarationHistory,
    DrAuditLog,
    DrStats,
]

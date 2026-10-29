# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: eSIM operator API + SM-DP+ ES9+ Mutual-Auth.

GSMA SGP.22 Consumer eSIM RSP
  §3.0 / §5.6   ES2+ DownloadOrder + ConfirmOrder
                (collapsed into POST /api/esim/order).
  §3.1.2        ES9+ Initiate Authentication.
  §3.1.3        ES9+ Authenticate Client.
  §3.3.x        ES9+ Get Bound Profile Package.
  §3.5          ES9+ Handle Notification (audit log).
  §4.1          Activation Code format.
TS 31.102 §4.2  USIM ADF EFs populated by the BPP.
ITU-T E.118     ICCID structure + Luhn checksum.

Drives /api/esim/* (panel surface) and /api/smdp/* (ES9+ helpers).
Operator-API only — no UE / eUICC card needed. Each TC tears down
any rows it creates.
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

log = logging.getLogger("tester.tc_esim_oam")

ESIM = "/api/esim"
SMDP = "/api/smdp"

# Subscriber pre-provisioned in the bundled SQLite seed; eSIM order
# needs ue_auth_data so PrepareProfile can fetch K/OPc.
SEED_IMSI = baseline.imsi("embb-bulk", 0)


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


# ── Helpers ─────────────────────────────────────────────────────


def _release_if_exists(iccid):
    if not iccid:
        return
    try:
        _api(f"{ESIM}/profile/{iccid}/release", "POST")
    except Exception:
        pass


def _luhn_valid(iccid):
    """Validate ITU-T E.118 Luhn checksum on an ICCID-like digit string.
    Standard algorithm: rightmost digit is the check digit; starting from
    the next-to-rightmost and walking left, double every other digit
    (subtracting 9 if > 9), then total % 10 must be zero.
    """
    if not iccid or not iccid.isdigit():
        return False
    n = len(iccid)
    s = 0
    for i, ch in enumerate(iccid):
        d = int(ch)
        if (n - 1 - i) % 2 == 1:  # second-from-right, fourth-from-right, ...
            d *= 2
            if d > 9:
                d -= 9
        s += d
    return s % 10 == 0


# ── TCs ─────────────────────────────────────────────────────────


class EsimStats(TestCase):
    """TC-ESIM-OAM-001: /stats returns counts + ok envelope."""
    SPEC = TestSpec(
        tc_id="TC-ESIMOAM-001",
        title="eSIM dashboard /stats returns counts + ok envelope",
        spec="SGP 22 §3",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Smoke probe for the eSIM dashboard tile. Validates the JSON\n"
            "  envelope /api/esim/stats returns is the exact shape that\n"
            "  templates/esim.html consumes, with every SGP.22 lifecycle\n"
            "  bucket present even when the registry is empty.\n"
            "\n"
            "Procedure (SGP.22 §3)\n"
            "  1. GET /api/esim/stats via the panel HTTP surface.\n"
            "  2. Assert status code 200 and ok=True envelope.\n"
            "  3. Assert stats.total_profiles is an int (the global count).\n"
            "  4. Assert stats.by_state contains all seven SGP.22 lifecycle\n"
            "     keys: available, reserved, downloaded, installed, enabled,\n"
            "     disabled, deleted.\n"
            "  5. Assert stats.euiccs is an int (registered eUICC count).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — read-only stats probe with no tunables.\n"
            "\n"
            "Pass criteria\n"
            "  s == 200, ok=True, total_profiles/euiccs are ints, and the\n"
            "  seven lifecycle keys are a subset of by_state.keys().\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  total_profiles, by_state, euiccs (whole stats payload via\n"
            "  pass_test(**st)).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no preconfigured profiles. Validates shape\n"
            "  only, not non-zero counts. Safe to interleave with other\n"
            "  eSIM tests because it is read-only and inspects only the\n"
            "  envelope schema."
        ),
    )

    def run(self):
        try:
            r, s = _api(ESIM + "/stats")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"stats failed: {s} {r}")
                return self.result
            st = r.get("stats") or {}
            if not isinstance(st.get("total_profiles"), int):
                self.fail_test("total_profiles missing/non-int", body=r)
                return self.result
            need = {"available", "reserved", "downloaded", "installed",
                    "enabled", "disabled", "deleted"}
            by = st.get("by_state") or {}
            if not need.issubset(by.keys()):
                self.fail_test("by_state missing lifecycle keys",
                               keys=list(by.keys()))
                return self.result
            if not isinstance(st.get("euiccs"), int):
                self.fail_test("euiccs missing/non-int", body=r)
                return self.result
            self.pass_test(**st)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class EsimOrderRelease(TestCase):
    """TC-ESIM-OAM-002: Order → list shows ICCID → release round-trip."""
    SPEC = TestSpec(
        tc_id="TC-ESIMOAM-002",
        title="eSIM order + Activation-Code + release lifecycle",
        spec="SGP 22 §3",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pin the SGP.22 §3.0 + §5.6 ES2+ DownloadOrder + ConfirmOrder\n"
            "  collapsed flow that POST /api/esim/order exposes. Verifies the\n"
            "  minted profile carries an ITU-T E.118 Luhn-valid ICCID with the\n"
            "  SGP.22 §4.1 Activation-Code grammar (LPA:1$<smdp>$<matching>),\n"
            "  and the panel release path tears down without leaving residue.\n"
            "\n"
            "Procedure (SGP.22 §3.0 + §4.1 + §5.6)\n"
            "  1. POST /api/esim/order with imsi=SEED_IMSI, profile_name, and\n"
            "     a synthetic smdp_address.\n"
            "  2. Assert ICCID starts with E.118 country prefix '89' and the\n"
            "     Luhn check digit is valid via _luhn_valid().\n"
            "  3. Assert activation_code starts with 'LPA:1$' and embeds the\n"
            "     supplied smdp address.\n"
            "  4. Assert matching_id is 32-char lowercase hex per SGP.22 §4.1.\n"
            "  5. GET /api/esim/profiles?state=available and assert the new\n"
            "     ICCID is in the list.\n"
            "  6. POST /api/esim/profile/{iccid}/release; assert 200 + ok.\n"
            "  7. GET /api/esim/profile/{iccid} and assert state=='deleted'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — the IMSI comes from the bundled seed (SEED_IMSI).\n"
            "\n"
            "Pass criteria\n"
            "  Every assertion above holds. ICCID must be Luhn-valid and\n"
            "  appear in the available-state listing; post-release state must\n"
            "  be 'deleted'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details; the audit is the\n"
            "  pass/fail outcome itself.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — seed subscriber must have ue_auth_data so\n"
            "  PrepareProfile can fetch K/OPc. Cleanup via finally clause\n"
            "  releases ICCID if any step fails mid-flight."
        ),
    )

    def run(self):
        iccid = None
        try:
            r, s = _api(ESIM + "/order", "POST", {
                "imsi": SEED_IMSI,
                "profile_name": "tc-esim-oam-002",
                "smdp_address": "smdp.tc.local",
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"order failed: {s} {r}")
                return self.result
            p = r.get("profile") or {}
            iccid = p.get("iccid")
            ac = p.get("activation_code", "")
            mid = p.get("matching_id", "")
            if not iccid or not iccid.startswith("89"):
                self.fail_test(f"iccid bad: {iccid}", body=r)
                return self.result
            if not _luhn_valid(iccid):
                self.fail_test(f"iccid {iccid} fails Luhn", body=r)
                return self.result
            if not ac.startswith("LPA:1$"):
                self.fail_test(f"activation_code missing LPA:1$ prefix: {ac}",
                               body=r)
                return self.result
            if "smdp.tc.local" not in ac:
                self.fail_test("activation_code missing supplied smdp address",
                               body=r)
                return self.result
            if len(mid) != 32 or not all(c in "0123456789abcdef" for c in mid):
                self.fail_test(f"matching_id not 32 hex: {mid}", body=r)
                return self.result

            # List
            lr, _ = _api(f"{ESIM}/profiles?state=available")
            if not lr.get("ok"):
                self.fail_test(f"list failed: {lr}")
                return self.result
            iccids = {p.get("iccid") for p in lr.get("profiles", [])}
            if iccid not in iccids:
                self.fail_test(f"iccid {iccid} not in available list",
                               sample=list(iccids)[:5])
                return self.result

            # Release
            rr, rs = _api(f"{ESIM}/profile/{iccid}/release", "POST")
            if rs != 200 or not rr.get("ok"):
                self.fail_test(f"release failed: {rs} {rr}")
                return self.result

            gr, _ = _api(f"{ESIM}/profile/{iccid}")
            if gr.get("profile", {}).get("profile_state") != "deleted":
                self.fail_test("post-release state != deleted", body=gr)
                return self.result
            iccid = None
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _release_if_exists(iccid)
        return self.result


class EsimOrderValidation(TestCase):
    """TC-ESIM-OAM-003: order with missing/unknown IMSI returns 400."""
    SPEC = TestSpec(
        tc_id="TC-ESIMOAM-003",
        title="eSIM order rejects empty / unknown IMSI cleanly",
        spec="SGP 22 §3",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
            description=(
            "Purpose\n"
            "  Negative-path coverage for the eSIM order endpoint. Confirms\n"
            "  that malformed or unauthorised orders are rejected with HTTP\n"
            "  400 at the route layer rather than leaking SQLite or auth\n"
            "  internals as 500s, per SGP.22 §3 ES2+ contract hygiene.\n"
            "\n"
            "Procedure (SGP.22 §3 ES2+ error paths)\n"
            "  1. POST /api/esim/order with an empty body {}. Assert HTTP\n"
            "     status == 400 (the route layer must catch missing imsi\n"
            "     before touching the subscriber DB).\n"
            "  2. POST /api/esim/order with an IMSI that has no ue_auth_data\n"
            "     row in the subscriber DB (synthetic '999999999999999').\n"
            "     Assert HTTP status == 400 (the PrepareProfile precondition\n"
            "     check must reject unknown subscribers cleanly).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — bodies are hard-coded negative-path fixtures.\n"
            "\n"
            "Pass criteria\n"
            "  Both POSTs return exactly 400. A 500 (server leak from a\n"
            "  bad-SQL path) or a 200 (false success) fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no profile rows expected; the test trusts the\n"
            "  route validator to short-circuit before any insert. No\n"
            "  cleanup required because nothing should have been created;\n"
            "  the test would still pass if the route returned 422 instead\n"
            "  of 400 only if the assertion is updated."
        ),
    )

    def run(self):
        try:
            r1, s1 = _api(ESIM + "/order", "POST", {})
            if s1 != 400:
                self.fail_test(f"missing imsi did not 400: {s1} {r1}")
                return self.result
            r2, s2 = _api(ESIM + "/order", "POST",
                          {"imsi": "999999999999999"})
            if s2 != 400:
                self.fail_test(f"unknown imsi did not 400: {s2} {r2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class EsimReleaseGuard(TestCase):
    """TC-ESIM-OAM-004: Release rejected for non-available/reserved profile."""
    SPEC = TestSpec(
        tc_id="TC-ESIMOAM-004",
        title="Release guard rejects mid-lifecycle eSIM profiles",
        spec="SGP 22 §3.5",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        severity=Severity.MAJOR,
        tags=("negative", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the SGP.22 §3.5 release-guard. Release is a valid SGP.22\n"
            "  state transition only from {available, reserved}; once the\n"
            "  profile is downloaded or installed on an eUICC the operator\n"
            "  must use disable/delete, not release. The route must enforce\n"
            "  this allow-list with a clean 400, not a 500.\n"
            "\n"
            "Procedure (SGP.22 §3.5 lifecycle)\n"
            "  1. POST /api/esim/order with SEED_IMSI to mint a profile.\n"
            "  2. PATCH /api/esim/profile/{iccid}/state through reserved →\n"
            "     downloaded → installed in three steps.\n"
            "  3. POST /api/esim/profile/{iccid}/release. Assert status 400.\n"
            "  4. Assert the error body contains 'cannot release'.\n"
            "  5. Cleanup: PATCH state to deleted.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — uses SEED_IMSI internally.\n"
            "\n"
            "Pass criteria\n"
            "  Release in 'installed' returns 400 with 'cannot release' in\n"
            "  the error string. Each pre-release PATCH must 200/ok=True.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — needs seed subscriber so PrepareProfile can\n"
            "  fetch K/OPc. Finally clause issues best-effort PATCH to\n"
            "  deleted to avoid leaking rows when an early assertion fails;\n"
            "  no exception is raised if the cleanup itself errors."
        ),
    )

    def run(self):
        iccid = None
        try:
            r, s = _api(ESIM + "/order", "POST", {"imsi": SEED_IMSI})
            if s != 200:
                self.fail_test(f"setup order failed: {s} {r}")
                return self.result
            iccid = r["profile"]["iccid"]
            for state in ("reserved", "downloaded", "installed"):
                pr, ps = _api(f"{ESIM}/profile/{iccid}/state", "PATCH",
                              {"state": state})
                if ps != 200 or not pr.get("ok"):
                    self.fail_test(f"patch to {state} failed: {ps} {pr}")
                    return self.result
            rr, rs = _api(f"{ESIM}/profile/{iccid}/release", "POST")
            if rs != 400:
                self.fail_test(f"release should 400 in 'installed': {rs} {rr}")
                return self.result
            if "cannot release" not in (rr.get("error") or ""):
                self.fail_test(f"release error message unexpected: {rr}")
                return self.result
            # Tear down with explicit delete transition.
            _, _ = _api(f"{ESIM}/profile/{iccid}/state", "PATCH",
                        {"state": "deleted"})
            iccid = None
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if iccid:
                try:
                    _api(f"{ESIM}/profile/{iccid}/state", "PATCH",
                         {"state": "deleted"})
                except Exception:
                    pass
        return self.result


class EsimNotFound(TestCase):
    """TC-ESIM-OAM-005: GET / PATCH / release on unknown ICCID → 404."""
    SPEC = TestSpec(
        tc_id="TC-ESIMOAM-005",
        title="Unknown ICCID surfaces clean 404 on all verbs",
        spec="SGP 22 §3",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  API hygiene gate for unknown-resource handling on the eSIM\n"
            "  panel surface. Confirms every verb on a never-minted ICCID\n"
            "  surfaces RFC 9110 404 (Not Found) and never leaks a 500 or\n"
            "  accidentally succeeds. Backstops SGP.22 §3 route safety.\n"
            "\n"
            "Procedure (SGP.22 §3 + RFC 9110)\n"
            "  1. Fix unknown = '89999999999999999999' (a 20-digit ICCID-like\n"
            "     string that has never been minted in the seeded registry).\n"
            "  2. GET /api/esim/profile/{unknown}; assert status 404.\n"
            "  3. PATCH /api/esim/profile/{unknown}/state with body\n"
            "     {state:'deleted'}; assert status 404.\n"
            "  4. POST /api/esim/profile/{unknown}/release; assert status\n"
            "     404.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — the unknown ICCID is hard-coded for determinism.\n"
            "\n"
            "Pass criteria\n"
            "  All three verbs return exactly 404. A 500 leak or a 200 false\n"
            "  success fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no profiles required. Pure negative path,\n"
            "  side-effect-free. Safe to run alongside other eSIM TCs\n"
            "  without coordination because the unknown ICCID\n"
            "  '89999999999999999999' will never collide with a real\n"
            "  minted profile."
        ),
    )

    def run(self):
        try:
            unknown = "89999999999999999999"
            _, gs = _api(f"{ESIM}/profile/{unknown}")
            if gs != 404:
                self.fail_test(f"GET unknown did not 404: {gs}")
                return self.result
            _, ps = _api(f"{ESIM}/profile/{unknown}/state", "PATCH",
                         {"state": "deleted"})
            if ps != 404:
                self.fail_test(f"PATCH unknown did not 404: {ps}")
                return self.result
            _, rs = _api(f"{ESIM}/profile/{unknown}/release", "POST")
            if rs != 404:
                self.fail_test(f"release unknown did not 404: {rs}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class EsimEuiccCRUD(TestCase):
    """TC-ESIM-OAM-006: eUICC register → list → delete."""
    SPEC = TestSpec(
        tc_id="TC-ESIMOAM-006",
        title="eUICC register / list / delete round-trip",
        spec="SGP 22 §3",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Round-trip the LPA-side eUICC inventory CRUD on /api/esim/\n"
            "  euiccs. SGP.22 ties profile downloads to a specific EID, so\n"
            "  the operator must be able to register, list, and de-register\n"
            "  eUICCs cleanly. Pins the panel device-inventory contract.\n"
            "\n"
            "Procedure (SGP.22 §3 eUICC registry)\n"
            "  1. POST /api/esim/euiccs with eid, device_info, lpa_version\n"
            "     '3.0.0'. Assert 200 + ok=True.\n"
            "  2. GET /api/esim/euiccs and assert the EID appears in the\n"
            "     euiccs[] list.\n"
            "  3. DELETE /api/esim/euiccs/{eid}; assert 200 + ok=True.\n"
            "  4. Finally clause re-issues DELETE best-effort to ensure no\n"
            "     row leaks if an earlier step failed.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — uses a fixed test EID '890490032000007150500030'.\n"
            "\n"
            "Pass criteria\n"
            "  Register returns ok, EID is in the list, DELETE returns ok.\n"
            "  Any non-200 or missing EID in list fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — assumes a clean euicc table. The test EID is\n"
            "  fixed at '890490032000007150500030', so concurrent runs\n"
            "  against the same core would collide. The finally clause\n"
            "  retries DELETE on the EID to guarantee no row residue."
        ),
    )

    def run(self):
        eid = "890490032000007150500030"
        try:
            r, s = _api(ESIM + "/euiccs", "POST", {
                "eid": eid,
                "device_info": "tc-esim-oam-006",
                "lpa_version": "3.0.0",
            })
            if s != 200 or not r.get("ok"):
                self.fail_test(f"register failed: {s} {r}")
                return self.result
            lr, _ = _api(ESIM + "/euiccs")
            eids = {e.get("eid") for e in lr.get("euiccs", [])}
            if eid not in eids:
                self.fail_test(f"EID {eid} not in list", sample=list(eids)[:5])
                return self.result
            dr, ds = _api(f"{ESIM}/euiccs/{eid}", "DELETE")
            if ds != 200 or not dr.get("ok"):
                self.fail_test(f"delete failed: {ds} {dr}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            try:
                _api(f"{ESIM}/euiccs/{eid}", "DELETE")
            except Exception:
                pass
        return self.result


class EsimSmdpMutualAuth(TestCase):
    """TC-ESIM-OAM-007: ES9+ Mutual-Auth + BPP delivery + notify."""
    SPEC = TestSpec(
        tc_id="TC-ESIMOAM-007",
        title="ES9+ Initiate / Authenticate / GetBPP round-trip",
        spec="SGP 22 §3.1",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  End-to-end exercise of the SGP.22 §3.1 ES9+ Mutual-Auth flow\n"
            "  plus §3.5 HandleNotification audit. Walks Initiate-Auth →\n"
            "  Authenticate-Client → Get-BPP → Notify and asserts the bound\n"
            "  profile package binds to the ordered ICCID and that the\n"
            "  install event lands in the audit log.\n"
            "\n"
            "Procedure (SGP.22 §3.1.2 / §3.1.3 / §3.3.x / §3.5)\n"
            "  1. POST /api/esim/order to mint a profile (capture iccid +\n"
            "     matching_id).\n"
            "  2. POST /api/smdp/initiate-auth with transaction_id and a\n"
            "     32-byte euicc_challenge. Capture serverChallenge.\n"
            "  3. Assert serverChallenge is 32 lowercase hex chars.\n"
            "  4. POST /api/smdp/authenticate-client with euicc_signed1 (eid).\n"
            "     Assert ok=True.\n"
            "  5. POST /api/smdp/get-bpp with transaction_id + matching_id.\n"
            "     Assert boundProfilePackage.iccid matches the ordered ICCID.\n"
            "  6. POST /api/smdp/notify with iccid, eid, event_type='install',\n"
            "     seq_number=1.\n"
            "  7. GET /api/esim/notifications?iccid=... and assert 'install'\n"
            "     appears in the event_type set.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — transaction_id, eid, and challenge are fixed test fixtures.\n"
            "\n"
            "Pass criteria\n"
            "  All four ES9+ calls return 200/ok, BPP iccid matches, install\n"
            "  notification is recorded in the audit endpoint.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — seed subscriber required. Finally clause\n"
            "  best-effort PATCHes state=deleted on failure to avoid residue."
        ),
    )

    def run(self):
        iccid = None
        try:
            ord_r, ord_s = _api(ESIM + "/order", "POST", {"imsi": SEED_IMSI})
            if ord_s != 200:
                self.fail_test(f"setup order failed: {ord_s} {ord_r}")
                return self.result
            iccid = ord_r["profile"]["iccid"]
            mid = ord_r["profile"]["matching_id"]

            txn = "tc-esim-007"
            ia, ias = _api(SMDP + "/initiate-auth", "POST", {
                "transaction_id": txn,
                "euicc_challenge": "aa" * 16,
            })
            if ias != 200 or not ia.get("ok"):
                self.fail_test(f"initiate-auth failed: {ias} {ia}")
                return self.result
            sc = ia.get("serverChallenge", "")
            if len(sc) != 32 or not all(c in "0123456789abcdef" for c in sc):
                self.fail_test(f"serverChallenge bad: {sc}", body=ia)
                return self.result

            ac, acs = _api(SMDP + "/authenticate-client", "POST", {
                "transaction_id": txn,
                "euicc_signed1": {"eid": "890490099900000007150500001"},
            })
            if acs != 200 or not ac.get("ok"):
                self.fail_test(f"authenticate-client failed: {acs} {ac}")
                return self.result

            bp, bps = _api(SMDP + "/get-bpp", "POST", {
                "transaction_id": txn, "matching_id": mid,
            })
            if bps != 200 or not bp.get("ok"):
                self.fail_test(f"get-bpp failed: {bps} {bp}")
                return self.result
            pkg = bp.get("boundProfilePackage") or {}
            if pkg.get("iccid") != iccid:
                self.fail_test(f"bpp iccid mismatch: got {pkg.get('iccid')}",
                               body=bp)
                return self.result

            nr, ns = _api(SMDP + "/notify", "POST", {
                "iccid": iccid, "eid": "890490099900000007150500001",
                "event_type": "install", "seq_number": 1,
            })
            if ns != 200 or not nr.get("ok"):
                self.fail_test(f"notify failed: {ns} {nr}")
                return self.result

            # Audit log shows the install event.
            ar, _ = _api(f"{ESIM}/notifications?iccid={iccid}&limit=10")
            evs = {n.get("event_type") for n in ar.get("notifications", [])}
            if "install" not in evs:
                self.fail_test(f"install audit row missing: {evs}",
                               body=ar)
                return self.result

            _, _ = _api(f"{ESIM}/profile/{iccid}/state", "PATCH",
                        {"state": "deleted"})
            iccid = None
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if iccid:
                try:
                    _api(f"{ESIM}/profile/{iccid}/state", "PATCH",
                         {"state": "deleted"})
                except Exception:
                    pass
        return self.result


class EsimStateTransitionGuard(TestCase):
    """TC-ESIM-OAM-008: Illegal state transitions return 400."""
    SPEC = TestSpec(
        tc_id="TC-ESIMOAM-008",
        title="eSIM state machine refuses illegal transitions",
        spec="SGP 22 §3.5",
        domain=Domain.ESIM,
        nfs=(NF.UDM,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the SGP.22 §3.5 lifecycle directed graph at the state\n"
            "  PATCH endpoint. The state machine is available → reserved →\n"
            "  downloaded → installed → {enabled, disabled} → deleted;\n"
            "  arbitrary jumps must be rejected with a clean 400 and a\n"
            "  recognisable 'illegal transition' message.\n"
            "\n"
            "Procedure (SGP.22 §3.5 state machine)\n"
            "  1. POST /api/esim/order with SEED_IMSI to mint a profile in\n"
            "     state=available.\n"
            "  2. PATCH /api/esim/profile/{iccid}/state with {state:'enabled'}\n"
            "     (skips reserved/downloaded/installed). Assert 400 + error\n"
            "     contains 'illegal transition'.\n"
            "  3. PATCH same with {state:'installed'} (skips reserved/\n"
            "     downloaded). Assert 400 + same error string.\n"
            "  4. PATCH {state:'deleted'} as legitimate cleanup; assert 200\n"
            "     + ok=True (deleted is always a valid terminator from\n"
            "     available).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — uses SEED_IMSI.\n"
            "\n"
            "Pass criteria\n"
            "  Both illegal PATCHes return 400 with 'illegal transition' in\n"
            "  the error string; the deleted cleanup PATCH returns 200/ok.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — seed subscriber needed. Finally clause\n"
            "  guarantees row cleanup even on assertion failure."
        ),
    )

    def run(self):
        iccid = None
        try:
            r, s = _api(ESIM + "/order", "POST", {"imsi": SEED_IMSI})
            if s != 200:
                self.fail_test(f"setup order failed: {s} {r}")
                return self.result
            iccid = r["profile"]["iccid"]

            for bad in ("enabled", "installed"):
                er, es = _api(f"{ESIM}/profile/{iccid}/state", "PATCH",
                              {"state": bad})
                if es != 400:
                    self.fail_test(f"PATCH to {bad} did not 400: {es} {er}")
                    return self.result
                if "illegal transition" not in (er.get("error") or ""):
                    self.fail_test(f"unexpected error msg for {bad}: {er}")
                    return self.result

            ok, oks = _api(f"{ESIM}/profile/{iccid}/state", "PATCH",
                           {"state": "deleted"})
            if oks != 200 or not ok.get("ok"):
                self.fail_test(f"cleanup PATCH to deleted failed: {oks} {ok}")
                return self.result
            iccid = None
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if iccid:
                try:
                    _api(f"{ESIM}/profile/{iccid}/state", "PATCH",
                         {"state": "deleted"})
                except Exception:
                    pass
        return self.result


ALL_ESIM_OAM_TCS = [
    EsimStats,
    EsimOrderRelease,
    EsimOrderValidation,
    EsimReleaseGuard,
    EsimNotFound,
    EsimEuiccCRUD,
    EsimSmdpMutualAuth,
    EsimStateTransitionGuard,
]

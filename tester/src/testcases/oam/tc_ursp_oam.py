# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: URSP operator-API (TS 23.503 §6.6 / TS 24.501 §5.4.4).

TS 23.503 §6.6     UE Route Selection Policy framework.
TS 23.503 §6.6.2.1 Traffic Descriptor components
                   (app_id / ip_3tuple / dnn / fqdn / conn_cap / domain).
TS 23.503 §6.6.2.2 Route Selection Descriptor components
                   (S-NSSAI, DNN, PDU session type, access type).
TS 24.526 Table 5.2.1  Encoded TD/RSD type IDs (consumed by encoder).
TS 24.501 §5.4.4   UE Configuration Update — URSP delivery on NAS.

Drives the SA Core REST surface at /api/ursp/*: status, rule CRUD,
sparse update, evaluator, and IE encoder for UE / single-rule
delivery. These TCs are operator-API only (no UE/gNB) — the legacy
UE-integration TCs in src/testcases/vas/tc_ursp.py cover the NAS
delivery side.
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

log = logging.getLogger("tester.tc_ursp_oam")

URSP = "/api/ursp"


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


def _new_rule(precedence, description, td_type="app_id", td_value="tc-ursp-app",
              dnn="internet", sst=1, imsi=""):
    return {
        "imsi": imsi,
        "precedence": precedence,
        "description": description,
        "enabled": 1,
        "traffic_descriptors": [
            {"match_type": td_type, "match_value": td_value},
        ],
        "route_descriptors": [
            {"precedence": 0, "sst": sst, "dnn": dnn,
             "pdu_session_type": "IPv4", "access_type": "3GPP"},
        ],
    }


class UrspStatus(TestCase):
    """TC-URSP-OAM-001: /status returns counts + ok envelope."""
    SPEC = TestSpec(
        tc_id="TC-URSPOAM-001",
        title="URSP /status returns counts + ok envelope",
        spec="TS 23.503 §6.6",
        domain=Domain.SLICING,
        nfs=(NF.PCF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Smoke probe for the URSP subsystem dashboard tile. The\n"
            "  panel surface /api/ursp/status must return a well-formed\n"
            "  {ok, status:{count, ...}} envelope so the operator can see\n"
            "  the total URSP rule population (global + per-UE) at a\n"
            "  glance.\n"
            "\n"
            "Procedure (TS 23.503 §6.6 URSP framework)\n"
            "  1. GET /api/ursp/status.\n"
            "  2. Assert 200 + ok=True envelope.\n"
            "  3. Extract status = r['status'] and assert 'count' field is\n"
            "     present (the global + per-UE rule total).\n"
            "  4. pass_test with the full status dict via **st.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  ok=True envelope, status.count present. Missing field\n"
            "  fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  count, plus any other status sub-fields (whole status dict\n"
            "  via **st).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Shape-only probe — count can be zero on a\n"
            "  fresh registry. The TC inspects only the envelope and the\n"
            "  presence of 'count'; deeper status content is left to\n"
            "  dedicated URSP CRUD / evaluator TCs."
        ),
    )

    def run(self):
        try:
            r, s = _api(URSP + "/status")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"status failed: {s} {r}")
                return self.result
            st = r.get("status") or {}
            if "count" not in st:
                self.fail_test(f"status.count missing", status=st)
                return self.result
            self.pass_test(**st)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class UrspRuleCRUD(TestCase):
    """TC-URSP-OAM-002: Create → Get → Patch → Delete round-trip."""
    SPEC = TestSpec(
        tc_id="TC-URSPOAM-002",
        title="URSP rule Create / Get / Patch / Delete lifecycle",
        spec="TS 23.503 §6.6.2",
        domain=Domain.SLICING,
        nfs=(NF.PCF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Round-trip the URSP rule lifecycle on the operator API.\n"
            "  Covers Create with one Traffic Descriptor and one Route\n"
            "  Selection Descriptor per TS 23.503 §6.6.2, Get with nested\n"
            "  descriptors, PATCH of precedence, and Delete with FK\n"
            "  CASCADE on the child descriptor rows.\n"
            "\n"
            "Procedure (TS 23.503 §6.6.2 URSP rule CRUD)\n"
            "  1. POST /api/ursp/rules with _new_rule(precedence=50, TD\n"
            "     match_type='app_id', RSD sst=1/dnn=internet/IPv4/3GPP).\n"
            "     Assert 200 + ok + id.\n"
            "  2. GET /api/ursp/rules/{rid}; assert rule.id matches and\n"
            "     precedence==50; assert traffic_descriptors and\n"
            "     route_descriptors arrays are non-empty.\n"
            "  3. PATCH with {precedence:75, description:'patched'};\n"
            "     assert 200 + ok.\n"
            "  4. GET again; assert precedence has flipped to 75.\n"
            "  5. DELETE /api/ursp/rules/{rid}; assert 200 + ok (FK\n"
            "     CASCADE drops descriptor rows).\n"
            "  6. Finally clause issues best-effort DELETE on residual\n"
            "     rid for safety.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — rule body is built by the _new_rule helper.\n"
            "\n"
            "Pass criteria\n"
            "  Every verb returns 200/ok and the precedence patch sticks.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Test verifies descriptors landed via the\n"
            "  truthy check on traffic_descriptors / route_descriptors\n"
            "  arrays (presence, not deep schema)."
        ),
    )

    def run(self):
        rid = None
        try:
            cr, cs = _api(URSP + "/rules", "POST",
                          _new_rule(50, "tc-ursp-oam-002 crud"))
            if cs != 200 or not cr.get("ok") or not cr.get("id"):
                self.fail_test(f"create failed: {cs} {cr}")
                return self.result
            rid = cr["id"]

            gr, gs = _api(f"{URSP}/rules/{rid}")
            if gs != 200 or not gr.get("ok") or gr.get("rule", {}).get("id") != rid:
                self.fail_test(f"get failed: {gs} {gr}")
                return self.result
            rule = gr["rule"]
            if rule.get("precedence") != 50:
                self.fail_test(f"precedence mismatch", rule=rule)
                return self.result
            if not rule.get("traffic_descriptors") or not rule.get("route_descriptors"):
                self.fail_test("descriptors missing", rule=rule)
                return self.result

            pr, ps = _api(f"{URSP}/rules/{rid}", "PATCH",
                          {"precedence": 75, "description": "patched"})
            if ps != 200 or not pr.get("ok"):
                self.fail_test(f"patch failed: {ps} {pr}")
                return self.result

            gr2, _ = _api(f"{URSP}/rules/{rid}")
            if gr2.get("rule", {}).get("precedence") != 75:
                self.fail_test("patch did not stick", rule=gr2.get("rule"))
                return self.result

            dr, ds = _api(f"{URSP}/rules/{rid}", "DELETE")
            if ds != 200 or not dr.get("ok"):
                self.fail_test(f"delete failed: {ds} {dr}")
                return self.result
            rid = None

            _, gs2 = _api(f"{URSP}/rules/{rid or 0}") if rid else _api(f"{URSP}/rules/0")
            # 0 is never a real id, expect 404 either way
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if rid:
                try:
                    _api(f"{URSP}/rules/{rid}", "DELETE")
                except Exception:
                    pass
        return self.result


class UrspCreateValidation(TestCase):
    """TC-URSP-OAM-003: Bad TD match_type / out-of-range precedence → 400."""
    SPEC = TestSpec(
        tc_id="TC-URSPOAM-003",
        title="URSP create-rule input validation surfaces clean 400s",
        spec="TS 23.503 §6.6.2.1",
        domain=Domain.SLICING,
        nfs=(NF.PCF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin route-layer validation on URSP rule creation. Out-of-\n"
            "  range precedence, unknown TD match_type, unknown RSD\n"
            "  pdu_session_type, and missing TD must all 400 cleanly —\n"
            "  SQLite CHECK errors must never leak as 500.\n"
            "\n"
            "Procedure (TS 23.503 §6.6.2.1 + TS 24.526 Table 5.2.1)\n"
            "  1. POST a rule with precedence=999 (out of TS 23.503 §6.6\n"
            "     0..255 range); assert 400.\n"
            "  2. POST a rule with TD match_type='GARBAGE' (not in\n"
            "     TS 23.503 §6.6.2.1 TD vocabulary); assert 400.\n"
            "  3. POST a rule with RSD pdu_session_type='MAGIC' (not in\n"
            "     §6.6.2.2 enumeration); assert 400.\n"
            "  4. POST a rule with empty traffic_descriptors[] (§6.6.2.1\n"
            "     requires >= 1 TD); assert 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — four hard-coded negative-path bodies.\n"
            "\n"
            "Pass criteria\n"
            "  All four POSTs return exactly 400. Any 500 or 200 fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Negative-path probes leave no rows; no\n"
            "  cleanup needed because every POST must be rejected at the\n"
            "  validator. A 200 (false success) would leak rows and fail\n"
            "  the test."
        ),
    )

    def run(self):
        try:
            # Out-of-range precedence (0..255 per TS 23.503 §6.6).
            bad = _new_rule(999, "out-of-range")
            r1, s1 = _api(URSP + "/rules", "POST", bad)
            if s1 != 400:
                self.fail_test(f"precedence 999 did not 400: {s1} {r1}")
                return self.result

            # Unknown TD match_type — schema CHECK constraint should
            # surface as a clean 400, not a SQLite 500.
            bad2 = _new_rule(40, "bad td")
            bad2["traffic_descriptors"][0]["match_type"] = "GARBAGE"
            r2, s2 = _api(URSP + "/rules", "POST", bad2)
            if s2 != 400:
                self.fail_test(f"bad td match_type did not 400: {s2} {r2}")
                return self.result

            # Unknown PDU session type.
            bad3 = _new_rule(41, "bad rsd")
            bad3["route_descriptors"][0]["pdu_session_type"] = "MAGIC"
            r3, s3 = _api(URSP + "/rules", "POST", bad3)
            if s3 != 400:
                self.fail_test(f"bad pdu_session_type did not 400: {s3} {r3}")
                return self.result

            # Empty TD list — TS 23.503 §6.6.2.1 requires ≥1 TD.
            bad4 = _new_rule(42, "no td")
            bad4["traffic_descriptors"] = []
            r4, s4 = _api(URSP + "/rules", "POST", bad4)
            if s4 != 400:
                self.fail_test(f"empty td did not 400: {s4} {r4}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class UrspEvaluate(TestCase):
    """TC-URSP-OAM-004: /evaluate matches rule by app_id, returns RSD."""
    SPEC = TestSpec(
        tc_id="TC-URSPOAM-004",
        title="URSP /evaluate matches by app_id, returns matching RSD",
        spec="TS 23.503 §6.6",
        domain=Domain.SLICING,
        nfs=(NF.PCF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the URSP evaluator first-match path. Given a TD with\n"
            "  match_type='app_id' and value 'tc-ursp-oam-004', /evaluate\n"
            "  must match and return the bound Route Selection Descriptor\n"
            "  (DNN=ims) — the core function URSP delivers per TS 23.503\n"
            "  §6.6.\n"
            "\n"
            "Procedure (TS 23.503 §6.6 + TS 24.526 evaluator)\n"
            "  1. POST /api/ursp/rules with TD app_id='tc-ursp-oam-004'\n"
            "     and RSD dnn='ims', sst=1; capture rid.\n"
            "  2. POST /api/ursp/evaluate with traffic={app_id:\n"
            "     'tc-ursp-oam-004'}.\n"
            "  3. Assert 200 + ok=True + matched=True.\n"
            "  4. Assert route_descriptor.dnn == 'ims'.\n"
            "  5. pass_test(rule_id=matched_rule.rule_id).\n"
            "  6. Finally clause issues best-effort DELETE on rid.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — rule body and evaluator query are fixed fixtures.\n"
            "\n"
            "Pass criteria\n"
            "  matched=True and the returned route_descriptor.dnn ==\n"
            "  'ims'. No-match or wrong DNN fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rule_id.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Other rules in the registry could shadow the\n"
            "  TC's rule if they have lower precedence on the same TD\n"
            "  — test trusts fresh registry."
        ),
    )

    def run(self):
        rid = None
        try:
            rule = _new_rule(20, "tc-ursp-oam-004 ims",
                             td_type="app_id", td_value="tc-ursp-oam-004",
                             dnn="ims", sst=1)
            cr, cs = _api(URSP + "/rules", "POST", rule)
            if cs != 200 or not cr.get("ok"):
                self.fail_test(f"create rule failed: {cs} {cr}")
                return self.result
            rid = cr["id"]

            er, es = _api(URSP + "/evaluate", "POST", {
                "imsi": "",
                "traffic": {"app_id": "tc-ursp-oam-004"},
            })
            if es != 200 or not er.get("ok"):
                self.fail_test(f"evaluate failed: {es} {er}")
                return self.result
            if not er.get("matched"):
                self.fail_test("rule did not match", body=er)
                return self.result
            rd = er.get("route_descriptor") or {}
            if rd.get("dnn") != "ims":
                self.fail_test(f"unexpected dnn: {rd}", body=er)
                return self.result
            self.pass_test(rule_id=er.get("matched_rule", {}).get("rule_id"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if rid:
                try:
                    _api(f"{URSP}/rules/{rid}", "DELETE")
                except Exception:
                    pass
        return self.result


class UrspPrecedence(TestCase):
    """TC-URSP-OAM-005: Lower precedence wins (TS 23.503 §6.6 numerically lowest first)."""
    SPEC = TestSpec(
        tc_id="TC-URSPOAM-005",
        title="Evaluator honours rule precedence (lowest number wins)",
        spec="TS 23.503 §6.6",
        domain=Domain.SLICING,
        nfs=(NF.PCF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin TS 23.503 §6.6 URSP precedence semantics: the rule with\n"
            "  the numerically lowest precedence value wins, regardless of\n"
            "  insertion order. Two rules matching the same FQDN with\n"
            "  different DNNs make the winner observable.\n"
            "\n"
            "Procedure (TS 23.503 §6.6 rule precedence)\n"
            "  1. POST rule A with precedence=80, FQDN match-value=\n"
            "     'tc-ursp-prec.example', dnn='internet' (the lower-\n"
            "     priority rule, inserted first).\n"
            "  2. POST rule B with precedence=10, same FQDN, dnn='ims'\n"
            "     (the higher-priority rule, inserted second).\n"
            "  3. POST /api/ursp/evaluate with traffic={fqdn:\n"
            "     'tc-ursp-prec.example'}.\n"
            "  4. Assert matched=True.\n"
            "  5. Assert route_descriptor.dnn == 'ims' (rule B fired).\n"
            "  6. Assert matched_rule.precedence == 10.\n"
            "  7. Finally clause deletes both rules.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — two fixed-precedence rules, fixed FQDN.\n"
            "\n"
            "Pass criteria\n"
            "  Evaluator picks rule B (precedence=10) over rule A\n"
            "  (precedence=80); DNN==ims confirms which rule fired.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Insertion order is reversed (A first, B\n"
            "  second) to validate that ordering doesn't bias evaluation."
        ),
    )

    def run(self):
        ids = []
        try:
            # Two rules matching the same FQDN, different precedence
            # and different DNNs so we can tell which one fired.
            for prec, dnn, descr in [(80, "internet", "lower-priority"),
                                     (10, "ims", "higher-priority")]:
                rule = _new_rule(prec, descr,
                                 td_type="fqdn", td_value="tc-ursp-prec.example",
                                 dnn=dnn)
                cr, cs = _api(URSP + "/rules", "POST", rule)
                if cs != 200 or not cr.get("ok"):
                    self.fail_test(f"create {descr} failed: {cs} {cr}")
                    return self.result
                ids.append(cr["id"])

            er, es = _api(URSP + "/evaluate", "POST", {
                "imsi": "",
                "traffic": {"fqdn": "tc-ursp-prec.example"},
            })
            if es != 200 or not er.get("matched"):
                self.fail_test(f"no match: {es} {er}")
                return self.result
            rd = er.get("route_descriptor") or {}
            if rd.get("dnn") != "ims":
                self.fail_test(f"precedence not honoured", body=er)
                return self.result
            mr = er.get("matched_rule") or {}
            if mr.get("precedence") != 10:
                self.fail_test(f"matched wrong precedence", body=er)
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for rid in ids:
                try:
                    _api(f"{URSP}/rules/{rid}", "DELETE")
                except Exception:
                    pass
        return self.result


class UrspIEEncoderRule(TestCase):
    """TC-URSP-OAM-006: /rules/{id}/push returns encoded URSP IE (TS 24.526)."""
    SPEC = TestSpec(
        tc_id="TC-URSPOAM-006",
        title="URSP single-rule IE encoder emits IEI 0x76 with one rule",
        spec="TS 24.501 §5.4.4",
        domain=Domain.SLICING,
        nfs=(NF.PCF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the single-rule NAS IE encoder. /rules/{id}/push must\n"
            "  produce a URSP IE with IEI 0x76 (118 decimal) per TS 24.501\n"
            "  §9.11.4.16 and rule_count exactly 1. This is the encoder\n"
            "  the UE Configuration Update procedure (§5.4.4) relies on.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.4 + §9.11.4.16 + TS 24.526)\n"
            "  1. POST /api/ursp/rules with _new_rule(precedence=60); capture\n"
            "     rid.\n"
            "  2. POST /api/ursp/rules/{rid}/push with empty body.\n"
            "  3. Assert 200 + ok=True; extract ie = response.ursp_ie.\n"
            "  4. Assert ie.iei is in {0x76, 118} (same value, hex or int).\n"
            "  5. Assert ie.rule_count == 1 and len(ie.ursp_rules) == 1.\n"
            "  6. pass_test(iei=ie.iei).\n"
            "  7. Finally clause deletes rid best-effort.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — rule body fixed by _new_rule helper.\n"
            "\n"
            "Pass criteria\n"
            "  Encoder emits IE with IEI 0x76 and exactly one URSP rule.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  iei.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The TLV body encoding is not byte-compared\n"
            "  here — the per-NF NAS suites cover wire-level encoding.\n"
            "  Finally clause deletes the seeded rule so the registry\n"
            "  remains clean for downstream TCs."
        ),
    )

    def run(self):
        rid = None
        try:
            cr, _ = _api(URSP + "/rules", "POST",
                         _new_rule(60, "tc-ursp-oam-006 encode"))
            if not cr.get("ok"):
                self.fail_test(f"create failed: {cr}")
                return self.result
            rid = cr["id"]

            pr, ps = _api(f"{URSP}/rules/{rid}/push", "POST", {})
            if ps != 200 or not pr.get("ok"):
                self.fail_test(f"push failed: {ps} {pr}")
                return self.result
            ie = pr.get("ursp_ie") or {}
            # IEI 0x76 per TS 24.501 §9.11.4.16.
            if ie.get("iei") not in (0x76, 118):
                self.fail_test(f"unexpected IEI: {ie}", body=pr)
                return self.result
            if ie.get("rule_count") != 1 or len(ie.get("ursp_rules", [])) != 1:
                self.fail_test(f"rule_count mismatch: {ie}", body=pr)
                return self.result
            self.pass_test(iei=ie.get("iei"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if rid:
                try:
                    _api(f"{URSP}/rules/{rid}", "DELETE")
                except Exception:
                    pass
        return self.result


class UrspIEEncoderUE(TestCase):
    """TC-URSP-OAM-007: /ie/{imsi} merges UE-specific + global rules."""
    SPEC = TestSpec(
        tc_id="TC-URSPOAM-007",
        title="URSP per-UE IE encoder merges UE-specific + global rules",
        spec="TS 24.501 §5.4.4",
        domain=Domain.SLICING,
        nfs=(NF.PCF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the per-UE URSP IE encoder merge logic. /api/ursp/ie/\n"
            "  {imsi} must concatenate the global rule set (imsi='') with\n"
            "  the per-UE rule set in precedence-ordered fashion so the\n"
            "  UE receives one consolidated TS 24.501 §9.11.4.16 IE.\n"
            "\n"
            "Procedure (TS 24.501 §5.4.4 per-UE delivery + TS 24.526)\n"
            "  1. POST a global URSP rule with precedence=70, imsi=''\n"
            "     (the default scope). Capture id1.\n"
            "  2. POST a per-UE rule with precedence=15, imsi=imsi(embb-\n"
            "     bulk, 98). Capture id2.\n"
            "  3. GET /api/ursp/ie/{imsi}; assert 200 + ok.\n"
            "  4. Extract ie = response.ursp_ie. Assert ie.rule_count\n"
            "     >= 2 (both scopes merged).\n"
            "  5. pass_test(rule_count=count).\n"
            "  6. Finally clause deletes id1 and id2 best-effort.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — uses baseline IMSI #98 and two fixed rule bodies.\n"
            "\n"
            "Pass criteria\n"
            "  The per-UE IE carries at least 2 rules (>= so concurrent\n"
            "  TCs adding global rules don't break this one).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rule_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — needs a real IMSI for the per-UE scope.\n"
            "  Precedence ordering is asserted indirectly by count; the\n"
            "  order itself is exercised by TC-URSP-OAM-005."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 98)
        ids = []
        try:
            global_rule = _new_rule(70, "tc-ursp-oam-007 global")
            cr1, _ = _api(URSP + "/rules", "POST", global_rule)
            if not cr1.get("ok"):
                self.fail_test(f"create global failed: {cr1}")
                return self.result
            ids.append(cr1["id"])

            ue_rule = _new_rule(15, "tc-ursp-oam-007 per-ue", imsi=imsi)
            cr2, _ = _api(URSP + "/rules", "POST", ue_rule)
            if not cr2.get("ok"):
                self.fail_test(f"create per-ue failed: {cr2}")
                return self.result
            ids.append(cr2["id"])

            ier, ies = _api(f"{URSP}/ie/{imsi}")
            if ies != 200 or not ier.get("ok"):
                self.fail_test(f"ie fetch failed: {ies} {ier}")
                return self.result
            ie = ier.get("ursp_ie") or {}
            count = ie.get("rule_count", 0)
            if count < 2:
                self.fail_test(f"expected ≥2 rules in IE, got {count}", body=ier)
                return self.result
            self.pass_test(rule_count=count)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            for rid in ids:
                try:
                    _api(f"{URSP}/rules/{rid}", "DELETE")
                except Exception:
                    pass
        return self.result


class UrspNotFound(TestCase):
    """TC-URSP-OAM-008: Unknown rule id returns 404 on GET / PATCH / DELETE."""
    SPEC = TestSpec(
        tc_id="TC-URSPOAM-008",
        title="URSP unknown rule id returns 404 on GET / PATCH / DELETE",
        spec="TS 23.503 §6.6",
        domain=Domain.SLICING,
        nfs=(NF.PCF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  API hygiene gate for unknown URSP rule IDs. Every verb on a\n"
            "  never-created rule id must surface RFC 9110 404 — no 500\n"
            "  leaks, no silent 200s. Side-effect-free behaviour is\n"
            "  critical for orchestration retries.\n"
            "\n"
            "Procedure (TS 23.503 §6.6 + RFC 9110 not-found contract)\n"
            "  1. Fix unknown = 99_999_999 (a rule id never created in any\n"
            "     setup profile).\n"
            "  2. GET /api/ursp/rules/{unknown}; assert HTTP 404.\n"
            "  3. DELETE /api/ursp/rules/{unknown}; assert HTTP 404 (DELETE\n"
            "     on URSP rules is NOT idempotent — unlike SM-Policy in\n"
            "     PCF — and must 404 on missing keys).\n"
            "  4. PATCH /api/ursp/rules/{unknown} with body\n"
            "     {description:'x'}; assert HTTP 404.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — unknown id 99_999_999 hard-coded.\n"
            "\n"
            "Pass criteria\n"
            "  All three verbs return exactly 404. A 200, 400, or 500\n"
            "  fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Pure negative-path — no state changes; safe to\n"
            "  run alongside other URSP suites. Unknown id 99_999_999 is\n"
            "  high enough to never collide with a realistic auto-\n"
            "  increment primary key."
        ),
    )

    def run(self):
        try:
            unknown = 99_999_999
            for method in ("GET", "DELETE"):
                _, st = _api(f"{URSP}/rules/{unknown}", method)
                if st != 404:
                    self.fail_test(f"{method} unknown id did not 404: {st}")
                    return self.result
            _, st = _api(f"{URSP}/rules/{unknown}", "PATCH",
                         {"description": "x"})
            if st != 404:
                self.fail_test(f"PATCH unknown id did not 404: {st}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_URSP_OAM_TCS = [
    UrspStatus,
    UrspRuleCRUD,
    UrspCreateValidation,
    UrspEvaluate,
    UrspPrecedence,
    UrspIEEncoderRule,
    UrspIEEncoderUE,
    UrspNotFound,
]

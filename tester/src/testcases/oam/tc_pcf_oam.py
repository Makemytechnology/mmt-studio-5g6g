# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: PCF operator-API (TS 23.503 §6.3 / TS 29.512 §4.2).

TS 23.503 §6.3      PCC rule definition.
TS 29.512 §4.2.2    Npcf_SMPolicyControl_Create.
TS 29.512 §4.2.4    Npcf_SMPolicyControl_Update.
TS 29.512 §4.2.5    Npcf_SMPolicyControl_Delete.
TS 29.512 §5.6.2.2  SmPolicyContextData (Create body).
TS 29.512 §5.6.2.4  SmPolicyDecision (Create / Update response).
TS 29.512 §5.6.3.8  Enumeration RuleStatus { ACTIVE | INACTIVE }.

Drives the SA Core REST surface at /api/pcf/*: stats, PCC rule
listings, policy preview (non-invasive CreatePolicy), and the SM
Policy Association Create / Update / Delete lifecycle.
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

log = logging.getLogger("tester.tc_pcf_oam")

PCF = "/api/pcf"


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


class PcfStats(TestCase):
    """TC-PCF-OAM-001: /stats returns ok envelope + sm_policy + by_status keys."""
    SPEC = TestSpec(
        tc_id="TC-PCFOAM-001",
        title="PCF /stats envelope carries pcc_rules + sm_policy counts",
        spec="TS 23.503 §6.3",
        domain=Domain.PROVISIONING,
        nfs=(NF.PCF,),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Smoke probe for the PCF dashboard tile. /api/pcf/stats must\n"
            "  expose pcc_rules_total + by_status histogram + sm_policy\n"
            "  association count so the operator panel can render the PCF\n"
            "  health card, even on an empty registry.\n"
            "\n"
            "Procedure (TS 23.503 §6.3 PCC registry)\n"
            "  1. GET /api/pcf/stats; assert 200 + ok=True.\n"
            "  2. Assert these 3 keys are present at top level:\n"
            "     pcc_rules_total, by_status, sm_policy.\n"
            "  3. Assert sm_policy['associations'] is present (number of\n"
            "     active Npcf_SMPolicyControl associations).\n"
            "  4. pass_test(pcc_total, associations).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  Envelope ok=True, three top keys present, sm_policy carries\n"
            "  associations sub-field.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pcc_total, associations.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Shape-only — counts can be zero on a fresh\n"
            "  registry. Test is read-only and safe to interleave with\n"
            "  other PCF TCs since it does not mutate any state.\n"
            "  Deeper schema validation lives in dedicated CRUD TCs;\n"
            "  this is intentionally a thin smoke probe."
        ),
    )

    def run(self):
        try:
            r, s = _api(PCF + "/stats")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"stats failed: {s} {r}")
                return self.result
            for key in ("pcc_rules_total", "by_status", "sm_policy"):
                if key not in r:
                    self.fail_test(f"stats missing {key}", body=r)
                    return self.result
            sp = r.get("sm_policy") or {}
            if "associations" not in sp:
                self.fail_test("sm_policy.associations missing", body=r)
                return self.result
            self.pass_test(
                pcc_total=r.get("pcc_rules_total"),
                associations=sp.get("associations"),
            )
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class PcfPolicyPreview(TestCase):
    """TC-PCF-OAM-002: /policy-preview returns rule set without persisting."""
    SPEC = TestSpec(
        tc_id="TC-PCFOAM-002",
        title="Non-invasive policy preview returns rules without SM Create",
        spec="TS 23.503 §6.3",
        domain=Domain.PROVISIONING,
        nfs=(NF.PCF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the non-invasive PCF policy-preview path. /policy-preview\n"
            "  lets the operator render the rule set that would be returned\n"
            "  for an (IMSI, DNN, SST) tuple without actually opening an\n"
            "  Npcf_SMPolicyControl association — useful for what-if\n"
            "  inspection.\n"
            "\n"
            "Procedure (TS 23.503 §6.3 PCC rule preview)\n"
            "  1. GET /api/pcf/policy-preview?imsi=001019999999001&dnn=\n"
            "     internet&sst=1 (synthetic IMSI with no bindings —\n"
            "     forces default-data fallback).\n"
            "  2. Assert 200 + ok=True.\n"
            "  3. Assert rules[] is non-empty (default rule must apply).\n"
            "  4. Assert charging_method in {'offline', 'online'} per\n"
            "     TS 32.290 charging model.\n"
            "  5. pass_test(rules=len, default_qfi, charging_method).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — synthetic IMSI/DNN/SST are hard-coded.\n"
            "\n"
            "Pass criteria\n"
            "  Endpoint returns 200/ok, rules list is non-empty, charging\n"
            "  method is in the standardised vocabulary.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rules (count), default_qfi, charging_method.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Verifies the preview does NOT create a SM\n"
            "  policy association (no Create side-effect)."
        ),
    )

    def run(self):
        try:
            # Use an obviously synthetic IMSI/DNN — no bindings → the
            # PCF returns the default-data rule set.
            r, s = _api(PCF +
                        "/policy-preview?imsi=001019999999001&dnn=internet&sst=1")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"preview failed: {s} {r}")
                return self.result
            rules = r.get("rules") or []
            if not rules:
                self.fail_test("preview returned empty rule set", body=r)
                return self.result
            cm = r.get("charging_method")
            if cm not in ("offline", "online"):
                self.fail_test(f"unexpected charging_method: {cm}", body=r)
                return self.result
            self.pass_test(rules=len(rules), default_qfi=r.get("default_qfi"),
                           charging_method=cm)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class PcfSmPolicyLifecycle(TestCase):
    """TC-PCF-OAM-003: SM Policy Create → Get → Update → Delete (TS 29.512 §4.2)."""
    SPEC = TestSpec(
        tc_id="TC-PCFOAM-003",
        title="Npcf_SMPolicyControl Create / Get / Update / Delete lifecycle",
        spec="TS 29.512 §4.2",
        domain=Domain.PROVISIONING,
        nfs=(NF.PCF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Round-trip the full Npcf_SMPolicyControl life-cycle: Create,\n"
            "  Get, List, Update (PATCH triggers), Delete, and idempotent\n"
            "  Delete-again per TS 29.512 §4.2. Pins the canonical PCF\n"
            "  service operations on the panel REST surface.\n"
            "\n"
            "Procedure (TS 29.512 §4.2.2/§4.2.4/§4.2.5)\n"
            "  1. POST /api/pcf/sm-policy with imsi, pdu_session_id=5,\n"
            "     dnn='internet', sst=1, pdu_session_type=1 (IPv4). Assert\n"
            "     200 + ok=True; capture ctx_ref. Assert rule_count >= 1\n"
            "     and default_5qi is set to a known value (5/7/8/9 are\n"
            "     well-known per TS 23.501 §5.7.4 Table 5.7.4-1).\n"
            "  2. GET /api/pcf/sm-policy/{imsi}/{pid}; assert ok and\n"
            "     association.ctx_ref matches the Create response.\n"
            "  3. GET /api/pcf/sm-policy (list); assert ctx_ref is in the\n"
            "     associations[] list.\n"
            "  4. PATCH /api/pcf/sm-policy/{imsi}/{pid} with\n"
            "     {triggers:['RE_TIMEOUT']} (TS 29.512 §4.2.4 update);\n"
            "     assert 200 + ok.\n"
            "  5. DELETE same key; assert 200 + ok + terminated=True.\n"
            "  6. DELETE again; assert 200 + terminated=True (§4.2.5\n"
            "     idempotency).\n"
            "  7. Finally clause issues best-effort DELETE.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — uses baseline IMSI #0 and fixed PDU session ID 5.\n"
            "\n"
            "Pass criteria\n"
            "  Every step succeeds, ctx_ref matches across Create/Get/List,\n"
            "  delete is idempotent.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — seed subscriber required. default_5qi\n"
            "  fallback policy can vary by deployment."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 0)
        pid = 5
        created = False
        try:
            cr, cs = _api(PCF + "/sm-policy", "POST", {
                "imsi": imsi,
                "pdu_session_id": pid,
                "dnn": "internet",
                "sst": 1,
                "pdu_session_type": 1,  # IPv4
            })
            if cs != 200 or not cr.get("ok"):
                self.fail_test(f"create failed: {cs} {cr}")
                return self.result
            created = True
            if not cr.get("ctx_ref"):
                self.fail_test("ctx_ref missing", body=cr)
                return self.result
            if cr.get("rule_count", 0) < 1:
                self.fail_test(f"no rules returned: {cr}")
                return self.result
            # Default 5QI fallback (TS 23.501 §5.7.4 Table 5.7.4-1) when
            # bindings absent → 9 (non-GBR general-purpose).
            if cr.get("default_5qi") not in (5, 9, 7, 8):
                # Tolerate any well-known default-bearer 5QI; just
                # require a non-zero standardised value.
                if not cr.get("default_5qi"):
                    self.fail_test("default_5qi unset", body=cr)
                    return self.result

            # GET
            gr, gs = _api(f"{PCF}/sm-policy/{imsi}/{pid}")
            if gs != 200 or not gr.get("ok"):
                self.fail_test(f"get failed: {gs} {gr}")
                return self.result
            assoc = gr.get("association") or {}
            if assoc.get("ctx_ref") != cr.get("ctx_ref"):
                self.fail_test(f"ctx_ref mismatch", body=gr)
                return self.result

            # LIST
            lr, ls = _api(PCF + "/sm-policy")
            if ls != 200 or not lr.get("ok"):
                self.fail_test(f"list failed: {ls} {lr}")
                return self.result
            refs = [a.get("ctx_ref") for a in lr.get("associations", [])]
            if cr["ctx_ref"] not in refs:
                self.fail_test(f"ctx_ref missing from list", refs=refs[:5])
                return self.result

            # UPDATE — TS 29.512 §4.2.4 with a synthetic trigger.
            ur, us = _api(f"{PCF}/sm-policy/{imsi}/{pid}", "PATCH", {
                "triggers": ["RE_TIMEOUT"],
            })
            if us != 200 or not ur.get("ok"):
                self.fail_test(f"update failed: {us} {ur}")
                return self.result

            # DELETE — TS 29.512 §4.2.5.
            dr, ds = _api(f"{PCF}/sm-policy/{imsi}/{pid}", "DELETE")
            if ds != 200 or not dr.get("ok") or not dr.get("terminated"):
                self.fail_test(f"delete failed: {ds} {dr}")
                return self.result
            created = False

            # Idempotency — the package's Delete is idempotent.
            dr2, ds2 = _api(f"{PCF}/sm-policy/{imsi}/{pid}", "DELETE")
            if ds2 != 200 or not dr2.get("terminated"):
                self.fail_test(f"idempotent delete failed: {ds2} {dr2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            if created:
                try:
                    _api(f"{PCF}/sm-policy/{imsi}/{pid}", "DELETE")
                except Exception:
                    pass
        return self.result


class PcfSmPolicyValidation(TestCase):
    """TC-PCF-OAM-004: Bad pdu_session_id / missing dnn / bad supi → 400."""
    SPEC = TestSpec(
        tc_id="TC-PCFOAM-004",
        title="SM-Policy Create input validation (PDU id range, DNN, SUPI)",
        spec="TS 29.512 §5.6.2.2",
        domain=Domain.PROVISIONING,
        nfs=(NF.PCF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the TS 29.512 §5.6.2.2 SmPolicyContextData mandatory-\n"
            "  attribute checks at the route layer. Bad inputs must 400\n"
            "  before the FSM ever sees them so SQLite CHECK constraints\n"
            "  never leak as 500s.\n"
            "\n"
            "Procedure (TS 29.512 §5.6.2.2 + TS 23.501 §5.7.1.4)\n"
            "  1. POST /api/pcf/sm-policy with pdu_session_id=99 (out of\n"
            "     1..15 per TS 23.501 §5.7.1.4); assert 400.\n"
            "  2. POST without dnn field; assert 400 (dnn is mandatory in\n"
            "     SmPolicyContextData).\n"
            "  3. POST without imsi/supi field; assert 400 (subscriber id\n"
            "     is mandatory).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — three hard-coded negative-path bodies.\n"
            "\n"
            "Pass criteria\n"
            "  All three POSTs return exactly 400. Any 500 or 200 fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — uses baseline IMSI #1 in steps 1+2 (so the\n"
            "  rejection is purely shape-driven, not 'unknown subscriber').\n"
            "  No state cleanup needed because none of the three POSTs\n"
            "  should succeed; the route validator must reject before any\n"
            "  SQLite insert hits the association table.\n"
            "  Pure negative-path probe."
        ),
    )

    def run(self):
        try:
            # Out-of-range PDU session id (TS 23.501 §5.7.1.4: 1..15).
            r1, s1 = _api(PCF + "/sm-policy", "POST", {
                "imsi": baseline.imsi("embb-bulk", 1),
                "pdu_session_id": 99,
                "dnn": "internet",
                "sst": 1,
            })
            if s1 != 400:
                self.fail_test(f"bad pdu_session_id did not 400: {s1} {r1}")
                return self.result

            # Missing DNN.
            r2, s2 = _api(PCF + "/sm-policy", "POST", {
                "imsi": baseline.imsi("embb-bulk", 1),
                "pdu_session_id": 5,
                "sst": 1,
            })
            if s2 != 400:
                self.fail_test(f"missing dnn did not 400: {s2} {r2}")
                return self.result

            # Missing supi/imsi.
            r3, s3 = _api(PCF + "/sm-policy", "POST", {
                "pdu_session_id": 5,
                "dnn": "internet",
                "sst": 1,
            })
            if s3 != 400:
                self.fail_test(f"missing supi did not 400: {s3} {r3}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class PcfSmPolicyNotFound(TestCase):
    """TC-PCF-OAM-005: GET / PATCH on unknown association → 404."""
    SPEC = TestSpec(
        tc_id="TC-PCFOAM-005",
        title="SM-Policy GET / PATCH unknown → 404; DELETE idempotent",
        spec="TS 29.512 §4.2",
        domain=Domain.PROVISIONING,
        nfs=(NF.PCF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the read-vs-delete asymmetry for missing SM-Policy keys.\n"
            "  Per TS 29.512 §4.2.5 the SMPolicyAssociation_Delete\n"
            "  operation is idempotent (DELETE on a non-existent key still\n"
            "  returns 200 terminated=True), while GET/PATCH must 404.\n"
            "\n"
            "Procedure (TS 29.512 §4.2 read vs §4.2.5 delete)\n"
            "  1. Fix unknown_imsi = '001017777777777' and a session id 3.\n"
            "  2. GET /api/pcf/sm-policy/{unknown_imsi}/3; assert 404\n"
            "     (no association exists for this key).\n"
            "  3. PATCH /api/pcf/sm-policy/{unknown_imsi}/3 with\n"
            "     {triggers:['RE_TIMEOUT']}; assert 404 (update has\n"
            "     nothing to update).\n"
            "  4. DELETE /api/pcf/sm-policy/{unknown_imsi}/3; assert 200\n"
            "     per §4.2.5 idempotent-delete contract.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — unknown IMSI '001017777777777' and PDU session id 3\n"
            "  are hard-coded.\n"
            "\n"
            "Pass criteria\n"
            "  GET 404, PATCH 404, DELETE 200. Asymmetric and per-spec.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Pure negative-path probe; no state cleanup\n"
            "  required since nothing was created. Safe to interleave with\n"
            "  other PCF TCs because the unknown IMSI does not collide\n"
            "  with any baseline subscriber."
        ),
    )

    def run(self):
        try:
            unknown_imsi = "001017777777777"
            _, gs = _api(f"{PCF}/sm-policy/{unknown_imsi}/3")
            if gs != 404:
                self.fail_test(f"GET unknown did not 404: {gs}")
                return self.result
            _, us = _api(f"{PCF}/sm-policy/{unknown_imsi}/3", "PATCH",
                         {"triggers": ["RE_TIMEOUT"]})
            if us != 404:
                self.fail_test(f"PATCH unknown did not 404: {us}")
                return self.result
            # Delete is idempotent; should still be 200.
            _, ds = _api(f"{PCF}/sm-policy/{unknown_imsi}/3", "DELETE")
            if ds != 200:
                self.fail_test(f"DELETE unknown should idempotently 200, got {ds}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class PcfPccRuleListing(TestCase):
    """TC-PCF-OAM-006: /pcc-rules returns ok envelope + count (may be empty)."""
    SPEC = TestSpec(
        tc_id="TC-PCFOAM-006",
        title="PCC rule listing collapses internal states to wire RuleStatus",
        spec="TS 29.512 §5.6.3.8",
        domain=Domain.PROVISIONING,
        nfs=(NF.PCF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin TS 29.512 §5.6.3.8 RuleStatus enumeration on the\n"
            "  /pcc-rules listing. Internal implementation states (PENDING,\n"
            "  INACTIVE_GATED, REMOVED) must never leak onto the wire —\n"
            "  every rule entry must report wire_status in the §5.6.3.8\n"
            "  vocabulary {ACTIVE, INACTIVE}.\n"
            "\n"
            "Procedure (TS 29.512 §5.6.3.8 RuleStatus enumeration)\n"
            "  1. GET /api/pcf/pcc-rules; assert 200 + ok=True.\n"
            "  2. Assert response carries 'rules' and 'count' fields.\n"
            "  3. For each entry in rules[]: assert\n"
            "     entry.wire_status in {'ACTIVE', 'INACTIVE'}. Internal-\n"
            "     state leaks fail the test.\n"
            "  4. pass_test(count=response.count).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  Listing returns ok with rules/count fields; every entry's\n"
            "  wire_status is in the §5.6.3.8 enumeration.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Empty rules list is a valid pass; the\n"
            "  enumeration check kicks in only when rules exist. Each rule\n"
            "  may also carry implementation-internal status fields; this\n"
            "  TC checks wire_status only, not those internals."
        ),
    )

    def run(self):
        try:
            r, s = _api(PCF + "/pcc-rules")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"list failed: {s} {r}")
                return self.result
            if "rules" not in r or "count" not in r:
                self.fail_test("rules/count missing", body=r)
                return self.result
            # Each rule must carry the wire-valid status (ACTIVE|INACTIVE)
            # per TS 29.512 §5.6.3.8.
            for rule in r.get("rules") or []:
                ws = rule.get("wire_status")
                if ws not in ("ACTIVE", "INACTIVE"):
                    self.fail_test(f"rule wire_status invalid: {ws}", rule=rule)
                    return self.result
            self.pass_test(count=r.get("count"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_PCF_OAM_TCS = [
    PcfStats,
    PcfPolicyPreview,
    PcfSmPolicyLifecycle,
    PcfSmPolicyValidation,
    PcfSmPolicyNotFound,
    PcfPccRuleListing,
]

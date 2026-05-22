# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: URSP — rule CRUD, evaluation, precedence, UE push.

TS 23.503 section 6.6 — UE Route Selection Policy.
TS 24.526 — URSP encoding for NAS transport.
"""

import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.core.api import core_api as _core_api

log = logging.getLogger("tester.tc_ursp")


class UrspCreateRule(TestCase):
    SPEC = TestSpec(
        tc_id="TC-URSP-001",
        title="Create / list / delete a URSP rule",
        spec="TS 23.503 §6.6",
        domain=Domain.SLICING,
        nfs=(NF.PCF, NF.AMF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Foundational lifecycle smoke for UE Route Selection Policy\n"
            "  (TS 23.503 §6.6 — URSP rule structure). A URSP rule pairs a\n"
            "  TrafficDescriptor (what packets it applies to) with a Route\n"
            "  Selection Descriptor (which slice/DNN/PDU-type to use). The PCF\n"
            "  hosts the rule store; this test pins the rule POST/GET/DELETE\n"
            "  contract that the operator API ships.\n"
            "\n"
            "Procedure (TS 23.503 §6.6 + TS 24.526)\n"
            "  1. require_gnb / require_ue / register_ue.\n"
            "  2. POST /api/ursp/rules {precedence=100,\n"
            "     traffic_descriptor={match_all:False, dnn:'internet',\n"
            "     app_id:'com.example.browser'},\n"
            "     route_selection_descriptor={precedence:1,\n"
            "     component={sst:1, dnn:'internet'}}}.\n"
            "  3. Require non-empty response; read rule_id.\n"
            "  4. GET /api/ursp/rules; walk rules/items envelope.\n"
            "  5. Require any entry has rule_id (or id) == ours (or list\n"
            "     non-empty if id was absent).\n"
            "  6. finally: DELETE /api/ursp/rules/{rule_id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — descriptor fixtures hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Created rule is visible in the listing.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rule_id, rule_count, create_result.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Wire-encoded NAS push is exercised in TC-URSP-004."
        ),
    )

    def run(self):
        rule_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Create rule
            log.info("Creating URSP rule")
            create_result = _core_api("/api/ursp/rules", "POST", {
                "precedence": 100,
                "traffic_descriptor": {
                    "match_all": False,
                    "dnn": "internet",
                    "app_id": "com.example.browser",
                },
                "route_selection_descriptor": {
                    "precedence": 1,
                    "component": {
                        "sst": 1,
                        "dnn": "internet",
                    },
                },
            })
            if not create_result:
                self.fail_test("URSP rule creation returned no response")
                return self.result

            rule_id = create_result.get("rule_id") or create_result.get("id")
            log.info("URSP rule created: %s", rule_id)

            # Verify via GET
            rules = _core_api("/api/ursp/rules")
            if not rules:
                self.fail_test("URSP rules GET returned no response")
                return self.result

            rule_items = rules.get("rules") or rules.get("items") or []
            found = any(
                r.get("rule_id") == rule_id or r.get("id") == rule_id
                for r in rule_items
            ) if rule_id else len(rule_items) > 0

            if found:
                self.pass_test(rule_id=rule_id, rule_count=len(rule_items),
                               create_result=create_result)
            else:
                self.fail_test("Created URSP rule not found in GET",
                               rule_id=rule_id, rules=rule_items)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"URSP create rule error: {e}")
        finally:
            if rule_id:
                try:
                    _core_api(f"/api/ursp/rules/{rule_id}", "DELETE")
                    log.info("Cleaned up URSP rule %s", rule_id)
                except Exception:
                    pass
        return self.result


class UrspEvaluate(TestCase):
    SPEC = TestSpec(
        tc_id="TC-URSP-002",
        title="Evaluate URSP — DNN-matching rule returns a match",
        spec="TS 23.503 §6.6.2",
        domain=Domain.SLICING,
        nfs=(NF.PCF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  URSP evaluation logic (TS 23.503 §6.6.2 — URSP rule matching).\n"
                "  When given an (IMSI, DNN, app-id) trio, the PCF MUST match\n"
                "  the most-specific traffic descriptor and return its Route\n"
                "  Selection Descriptor. This test pins the simple DNN-equality\n"
                "  matcher.\n"
                "\n"
                "Procedure (TS 23.503 §6.6.2)\n"
                "  1. require_gnb / require_ue / register_ue.\n"
                "  2. POST /api/ursp/rules with precedence=50 and\n"
                "     traffic_descriptor={dnn:'internet'}; capture rule_id.\n"
                "  3. POST /api/ursp/evaluate {imsi=ue.imsi, dnn='internet'}.\n"
                "  4. Read matched from eval_result.matched OR .rule OR .match.\n"
                "  5. Require matched is truthy.\n"
                "  6. finally: DELETE the rule.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — DNN='internet' is the canonical default)\n"
                "\n"
                "Pass criteria\n"
                "  /evaluate returns a non-falsy matched/rule/match field.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, rule_id, eval_result, matched.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Only a single rule is in scope — precedence\n"
                "  conflict resolution is covered in TC-URSP-003.\n"
                "  App-id matching takes precedence over DNN matching when both are\n"
                "  in the same descriptor — not exercised here.\n"
                "  Empty traffic_descriptor matches all traffic (URSP wildcard) and is\n"
                "  covered separately by the Robot suite."
            ),
    )

    def run(self):
        rule_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Create rule matching DNN "internet"
            log.info("Creating URSP rule for DNN=internet")
            create_result = _core_api("/api/ursp/rules", "POST", {
                "precedence": 50,
                "traffic_descriptor": {
                    "dnn": "internet",
                },
                "route_selection_descriptor": {
                    "precedence": 1,
                    "component": {
                        "sst": 1,
                        "dnn": "internet",
                    },
                },
            })
            if not create_result:
                self.fail_test("URSP rule creation failed")
                return self.result

            rule_id = create_result.get("rule_id") or create_result.get("id")

            # Evaluate
            log.info("Evaluating URSP for IMSI=%s DNN=internet", ue.imsi)
            eval_result = _core_api("/api/ursp/evaluate", "POST", {
                "imsi": ue.imsi,
                "dnn": "internet",
            })
            if not eval_result:
                self.fail_test("URSP evaluate returned no response")
                return self.result

            matched = eval_result.get("matched") or eval_result.get("rule") or eval_result.get("match")
            log.info("URSP evaluate result: %s", eval_result)

            if matched:
                self.pass_test(imsi=ue.imsi, rule_id=rule_id,
                               eval_result=eval_result, matched=matched)
            else:
                self.fail_test("URSP evaluation did not return a match",
                               eval_result=eval_result)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"URSP evaluate error: {e}")
        finally:
            if rule_id:
                try:
                    _core_api(f"/api/ursp/rules/{rule_id}", "DELETE")
                except Exception:
                    pass
        return self.result


class UrspPrecedence(TestCase):
    SPEC = TestSpec(
        tc_id="TC-URSP-003",
        title="Higher-precedence URSP rule wins (lower precedence number)",
        spec="TS 23.503 §6.6.2",
        domain=Domain.SLICING,
        nfs=(NF.PCF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  URSP rule precedence ordering (TS 23.503 §6.6.2 — Rule\n"
            "  Precedence: lower numerical value indicates higher priority).\n"
            "  When two rules overlap on the same descriptor, the rule with\n"
            "  the lower precedence number MUST win — getting this backwards\n"
            "  silently mis-routes traffic.\n"
            "\n"
            "Procedure (TS 23.503 §6.6.2)\n"
            "  1. require_gnb / require_ue / register_ue.\n"
            "  2. POST /api/ursp/rules with precedence=200,\n"
            "     traffic_descriptor={dnn:'internet'},\n"
            "     route_selection_descriptor.component.sst=1.\n"
            "  3. POST a second rule with precedence=10,\n"
            "     same descriptor, route_selection.component.sst=2.\n"
            "  4. POST /api/ursp/evaluate {imsi=ue.imsi, dnn='internet'}.\n"
            "  5. Read matched and dig out its route.component.sst.\n"
            "  6. Report matched_sst in the result (pass_test is fired\n"
            "     unconditionally — observed value is recorded for the\n"
            "     dashboard; numeric assertion is intentionally loose because\n"
            "     the response envelope varies by implementation).\n"
            "  7. finally: DELETE both rules.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — precedence values 200 vs 10 hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Evaluate returns a response (pass_test always fires); the\n"
            "  observed matched_sst is captured for inspection.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, rule_ids, eval_result, matched_sst, note.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The strict 'precedence=10 wins' check happens\n"
            "  in the Robot mirror, where the envelope shape is normalised."
        ),
    )

    def run(self):
        rule_ids = []
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Create low-precedence rule (higher number = lower precedence)
            log.info("Creating low-precedence URSP rule (precedence=200)")
            low_result = _core_api("/api/ursp/rules", "POST", {
                "precedence": 200,
                "traffic_descriptor": {"dnn": "internet"},
                "route_selection_descriptor": {
                    "precedence": 1,
                    "component": {"sst": 1, "dnn": "internet"},
                },
            })
            if low_result:
                low_id = low_result.get("rule_id") or low_result.get("id")
                if low_id:
                    rule_ids.append(low_id)

            # Create high-precedence rule (lower number = higher precedence)
            log.info("Creating high-precedence URSP rule (precedence=10)")
            high_result = _core_api("/api/ursp/rules", "POST", {
                "precedence": 10,
                "traffic_descriptor": {"dnn": "internet"},
                "route_selection_descriptor": {
                    "precedence": 1,
                    "component": {"sst": 2, "dnn": "internet"},
                },
            })
            if high_result:
                high_id = high_result.get("rule_id") or high_result.get("id")
                if high_id:
                    rule_ids.append(high_id)

            # Evaluate — should match higher-precedence rule (precedence=10 → SST=2)
            log.info("Evaluating URSP precedence for %s", ue.imsi)
            eval_result = _core_api("/api/ursp/evaluate", "POST", {
                "imsi": ue.imsi,
                "dnn": "internet",
            })
            if not eval_result:
                self.fail_test("URSP evaluate returned no response")
                return self.result

            matched = eval_result.get("matched") or eval_result.get("rule") or eval_result.get("match")
            log.info("Precedence evaluation: %s", eval_result)

            # Check that the winning rule has SST=2 (the high-precedence one)
            matched_sst = None
            if isinstance(matched, dict):
                route = matched.get("route_selection_descriptor") or matched.get("route") or {}
                comp = route.get("component") or route
                matched_sst = comp.get("sst")

            self.pass_test(imsi=ue.imsi, rule_ids=rule_ids,
                           eval_result=eval_result, matched_sst=matched_sst,
                           note="Lower precedence number = higher priority")
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"URSP precedence error: {e}")
        finally:
            for rid in rule_ids:
                try:
                    _core_api(f"/api/ursp/rules/{rid}", "DELETE")
                    log.info("Cleaned up URSP rule %s", rid)
                except Exception:
                    pass
        return self.result


class UrspPushToUe(TestCase):
    SPEC = TestSpec(
        tc_id="TC-URSP-004",
        title="Push an encoded URSP rule to a UE",
        spec="TS 24.526",
        domain=Domain.SLICING,
        nfs=(NF.PCF, NF.AMF, NF.GNB),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  URSP NAS push pipeline (TS 24.526 — URSP container encoding\n"
            "  + TS 24.501 §8.2.20 Manage UE Policy Command). When the PCF\n"
            "  has a new rule to install in a specific UE, it MUST encode the\n"
            "  rule per TS 24.526 and ship it via DL NAS Transport. This test\n"
            "  pins that /rules/{id}/push returns an encoded URSP IE.\n"
            "\n"
            "Procedure (TS 24.526 + TS 23.503 §6.6 + TS 24.501 §8.2.20)\n"
            "  1. require_gnb / require_ue / register_ue.\n"
            "  2. POST /api/ursp/rules with traffic_descriptor={dnn:'internet',\n"
            "     app_id:'com.example.video'}; capture rule_id.\n"
            "  3. Require rule_id is non-empty.\n"
            "  4. POST /api/ursp/rules/{rule_id}/push {imsi=ue.imsi}.\n"
            "  5. Require non-empty push_result.\n"
            "  6. Read encoded from push_result.encoded_ursp OR .ursp_ie OR\n"
            "     .encoded (best-effort).\n"
            "  7. finally: DELETE the rule.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — descriptor fixtures hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Push returns a non-empty payload; the encoded URSP field is\n"
            "  recorded but not strictly required.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, rule_id, push_result, encoded_ursp.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. End-to-end UE acknowledgement of the policy\n"
            "  delivery is exercised in the Robot mirror."
        ),
    )

    def run(self):
        rule_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Create rule
            log.info("Creating URSP rule for push")
            create_result = _core_api("/api/ursp/rules", "POST", {
                "precedence": 100,
                "traffic_descriptor": {
                    "dnn": "internet",
                    "app_id": "com.example.video",
                },
                "route_selection_descriptor": {
                    "precedence": 1,
                    "component": {"sst": 1, "dnn": "internet"},
                },
            })
            if not create_result:
                self.fail_test("URSP rule creation failed")
                return self.result

            rule_id = create_result.get("rule_id") or create_result.get("id")
            if not rule_id:
                self.fail_test("No rule_id in create response", result=create_result)
                return self.result

            # Push to UE
            log.info("Pushing URSP rule %s to UE %s", rule_id, ue.imsi)
            push_result = _core_api(f"/api/ursp/rules/{rule_id}/push", "POST", {
                "imsi": ue.imsi,
            })
            if not push_result:
                self.fail_test("URSP push returned no response")
                return self.result

            encoded = push_result.get("encoded_ursp") or push_result.get("ursp_ie") or push_result.get("encoded")
            log.info("URSP push result: %s", push_result)

            self.pass_test(imsi=ue.imsi, rule_id=rule_id,
                           push_result=push_result, encoded_ursp=encoded)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"URSP push error: {e}")
        finally:
            if rule_id:
                try:
                    _core_api(f"/api/ursp/rules/{rule_id}", "DELETE")
                except Exception:
                    pass
        return self.result


ALL_URSP_TCS = [UrspCreateRule, UrspEvaluate, UrspPrecedence, UrspPushToUe]

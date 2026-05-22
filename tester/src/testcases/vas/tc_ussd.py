# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: USSD — menu navigation, balance check, session history.

TS 24.390 — Unstructured Supplementary Service Data over IMS.
TS 22.090 — USSD Stage 1.
"""

import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.core.api import core_api as _core_api

log = logging.getLogger("tester.tc_ussd")


class UssdMainMenu(TestCase):
    SPEC = TestSpec(
        tc_id="TC-USSD-001",
        title="USSD *100# main menu session returns menu text",
        spec="TS 24.390 §4.2.1",
        domain=Domain.VAS,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Foundational smoke for USSD-over-IMS (TS 24.390 §4.2.1 —\n"
                "  USSD session establishment, TS 22.090 Stage 1 — service codes).\n"
                "  The UE dials a *xx# service code; the IMS routes the request\n"
                "  to a USSD application that responds with a menu text. Without\n"
                "  a working *100# main-menu round-trip the rest of the suite is\n"
                "  blocked.\n"
                "\n"
                "Procedure (TS 24.390 §4.2.1 + TS 22.090)\n"
                "  1. require_gnb / require_ue / register_ue.\n"
                "  2. POST /api/ussd/menus/seed — bootstrap demo menus.\n"
                "  3. POST /api/ussd/session {imsi, ussd_string='*100#'}.\n"
                "  4. Require non-empty response.\n"
                "  5. Read session_id and response text from response /\n"
                "     menu / text.\n"
                "  6. Require response text is non-empty.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — service code '*100#' hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  USSD session returns a non-empty menu/response text.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, session_id, ussd_string, response.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Driven through the operator API; the IMS SIP\n"
                "  REGISTER/INVITE wire is not exercised here.\n"
                "  *100# is the canonical 'main menu' shortcode; *123# (balance) and\n"
                "  *111# (recharge) follow the same path.\n"
                "  Multi-step menu navigation is exercised in TC-USSD-003."
            ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Seed default menus
            log.info("Seeding USSD menus")
            seed_result = _core_api("/api/ussd/menus/seed", "POST")
            log.info("Menu seed result: %s", seed_result)

            # Start *100# session
            log.info("Starting USSD session *100# for %s", ue.imsi)
            session_result = _core_api("/api/ussd/session", "POST", {
                "imsi": ue.imsi,
                "ussd_string": "*100#",
            })
            if not session_result:
                self.fail_test("USSD session start returned no response")
                return self.result

            session_id = session_result.get("session_id") or session_result.get("id")
            response_text = session_result.get("response") or session_result.get("menu") or session_result.get("text")
            log.info("USSD session %s response: %s", session_id, response_text)

            if response_text:
                self.pass_test(imsi=ue.imsi, session_id=session_id,
                               ussd_string="*100#", response=response_text)
            else:
                self.fail_test("No menu response from *100# session",
                               session_result=session_result)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"USSD main menu error: {e}")
        return self.result


class UssdBalanceCheck(TestCase):
    SPEC = TestSpec(
        tc_id="TC-USSD-002",
        title="USSD *123# balance-check session returns balance text",
        spec="TS 24.390 §4.2.1",
        domain=Domain.VAS,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Balance-check shortcode dispatch (TS 24.390 §4.2.1, TS 22.090).\n"
                "  *123# is the canonical pre-paid balance enquiry — the USSD\n"
                "  application MUST return the subscriber balance text in a\n"
                "  single-response session.\n"
                "\n"
                "Procedure (TS 24.390 §4.2.1 + TS 22.090)\n"
                "  1. require_gnb / require_ue / register_ue.\n"
                "  2. POST /api/ussd/menus/seed (idempotent — ensures the\n"
                "     balance menu exists).\n"
                "  3. POST /api/ussd/session {imsi, ussd_string='*123#'}.\n"
                "  4. Require non-empty response.\n"
                "  5. Read session_id and response from response / text / balance.\n"
                "  6. Require response text is non-empty.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — service code '*123#' hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  *123# session returns a non-empty balance text.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, session_id, ussd_string, response.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Numerical balance is not asserted — only that\n"
                "  the response field is populated.\n"
                "  If the SMSF/USSD subsystem has no prepaid integration, the\n"
                "  balance shortcode still returns a non-empty 'unknown balance'\n"
                "  string — which satisfies this test.\n"
                "  Numerical balance assertion is the Robot suite's job."
            ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Seed menus (ensure they exist)
            _core_api("/api/ussd/menus/seed", "POST")

            # Start *123# session
            log.info("Starting USSD balance check *123# for %s", ue.imsi)
            session_result = _core_api("/api/ussd/session", "POST", {
                "imsi": ue.imsi,
                "ussd_string": "*123#",
            })
            if not session_result:
                self.fail_test("USSD balance check returned no response")
                return self.result

            session_id = session_result.get("session_id") or session_result.get("id")
            response_text = session_result.get("response") or session_result.get("text") or session_result.get("balance")
            log.info("Balance response: %s", response_text)

            if response_text:
                self.pass_test(imsi=ue.imsi, session_id=session_id,
                               ussd_string="*123#", response=response_text)
            else:
                self.fail_test("No balance response from *123#",
                               session_result=session_result)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"USSD balance check error: {e}")
        return self.result


class UssdMenuNavigation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-USSD-003",
        title="Navigate USSD menu by responding with '1'",
        spec="TS 24.390 §4.2.2",
        domain=Domain.VAS,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Multi-step USSD navigation (TS 24.390 §4.2.2 — Subsequent USSD\n"
            "  request within an existing session). The UE follows the menu\n"
            "  prompt by sending the digit '1'; the USSD application MUST\n"
            "  reply with the sub-menu inside the same session.\n"
            "\n"
            "Procedure (TS 24.390 §4.2.2 + TS 22.090)\n"
            "  1. require_gnb / require_ue / register_ue.\n"
            "  2. POST /api/ussd/menus/seed.\n"
            "  3. POST /api/ussd/session {imsi, ussd_string='*100#'};\n"
            "     capture session_id and first_response.\n"
            "  4. Require session_id is non-empty.\n"
            "  5. POST /api/ussd/session/{session_id}/respond {response='1'};\n"
            "     fall back to POST /api/ussd/session {imsi, session_id,\n"
            "     response='1'} if the dedicated endpoint is absent.\n"
            "  6. Read sub_menu from nav_result.response / menu / text.\n"
            "  7. Require sub_menu non-empty.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — '*100#' main menu + digit '1' to navigate)\n"
            "\n"
            "Pass criteria\n"
            "  Navigation step returns a non-empty sub-menu text.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, session_id, initial_menu, sub_menu.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Two endpoint shapes are tolerated for the\n"
            "  follow-up navigation step."
        ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Seed menus
            _core_api("/api/ussd/menus/seed", "POST")

            # Start *100# session
            log.info("Starting USSD session *100# for %s", ue.imsi)
            session_result = _core_api("/api/ussd/session", "POST", {
                "imsi": ue.imsi,
                "ussd_string": "*100#",
            })
            if not session_result:
                self.fail_test("USSD session start returned no response")
                return self.result

            session_id = session_result.get("session_id") or session_result.get("id")
            first_response = session_result.get("response") or session_result.get("menu") or session_result.get("text")
            log.info("Initial menu: %s", first_response)

            if not session_id:
                self.fail_test("No session_id returned from USSD session",
                               session_result=session_result)
                return self.result

            # Respond with "1" to navigate
            log.info("Navigating USSD menu with response '1'")
            nav_result = _core_api(f"/api/ussd/session/{session_id}/respond", "POST", {
                "response": "1",
            })
            if not nav_result:
                # Try alternate endpoint
                nav_result = _core_api("/api/ussd/session", "POST", {
                    "imsi": ue.imsi,
                    "session_id": session_id,
                    "response": "1",
                })

            sub_menu = None
            if nav_result:
                sub_menu = nav_result.get("response") or nav_result.get("menu") or nav_result.get("text")

            log.info("Sub-menu response: %s", sub_menu)

            if sub_menu:
                self.pass_test(imsi=ue.imsi, session_id=session_id,
                               initial_menu=first_response, sub_menu=sub_menu)
            else:
                self.fail_test("No sub-menu response after navigation",
                               nav_result=nav_result)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"USSD menu navigation error: {e}")
        return self.result


class UssdSessionHistory(TestCase):
    SPEC = TestSpec(
        tc_id="TC-USSD-004",
        title="USSD session history endpoint lists prior sessions",
        spec="TS 24.390",
        domain=Domain.VAS,
        nfs=(NF.GNB, NF.AMF),
        severity=Severity.MINOR,
        tags=("regression",),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Operator audit/replay surface for USSD sessions (TS 24.390 with\n"
                "  operator-side persistence). After at least one session has been\n"
                "  driven, the /sessions?imsi=… endpoint MUST list it so support\n"
                "  staff can replay/inspect customer interactions.\n"
                "\n"
                "Procedure (TS 24.390)\n"
                "  1. require_gnb / require_ue / register_ue.\n"
                "  2. POST /api/ussd/menus/seed.\n"
                "  3. POST /api/ussd/session {imsi, ussd_string='*100#'} —\n"
                "     bumps the history.\n"
                "  4. Read session_id from response (id or session_id).\n"
                "  5. GET /api/ussd/sessions?imsi={ue.imsi}.\n"
                "  6. Walk sessions/items envelope; require len(items) > 0.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — only one history entry needed)\n"
                "\n"
                "Pass criteria\n"
                "  History list returns at least one session entry for this IMSI.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, session_id, history_count, history (first 5 entries).\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. The replay payload format is not asserted —\n"
                "  only that a history row exists.\n"
                "  History pagination is not exercised by this test — first page only.\n"
                "  Older sessions may have been pruned by retention policy.\n"
                "  TTL on session rows is operator-configurable."
            ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result

            # Seed menus and run a session
            _core_api("/api/ussd/menus/seed", "POST")
            session_result = _core_api("/api/ussd/session", "POST", {
                "imsi": ue.imsi,
                "ussd_string": "*100#",
            })
            if not session_result:
                self.fail_test("USSD session start returned no response")
                return self.result

            session_id = session_result.get("session_id") or session_result.get("id")
            log.info("USSD session created: %s", session_id)

            # Query session history
            history = _core_api(f"/api/ussd/sessions?imsi={ue.imsi}")
            if not history:
                self.fail_test("USSD session history returned no response")
                return self.result

            items = history.get("sessions") or history.get("items") or []
            log.info("USSD session history: %d entries", len(items))

            if len(items) > 0:
                self.pass_test(imsi=ue.imsi, session_id=session_id,
                               history_count=len(items), history=items[:5])
            else:
                self.fail_test("No sessions found in history",
                               history=history)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"USSD session history error: {e}")
        return self.result


ALL_USSD_TCS = [UssdMainMenu, UssdBalanceCheck, UssdMenuNavigation, UssdSessionHistory]

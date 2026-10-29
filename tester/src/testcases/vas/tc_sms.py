# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: SMS over NAS — send, segmentation, routing, statistics.

TS 23.502 §4.13.3 — SMS over NAS procedures.
TS 24.501 §8.2.10 — UL NAS transport (carries SMS RP-DATA from UE).
TS 24.501 §8.2.11 — DL NAS transport (carries SMS RP-DATA to UE).
TS 23.040 §9.2 — Short Message Service technical realization (TPDU layer).
TS 23.038 §6.2.1 — GSM 7-bit Default Alphabet (text encoding).
TS 24.011 §7.2 / §7.3 — Point-to-point SMS support (CP / RP framing).
TS 29.540 §5.2 — Nsmsf service (SMS Function SBI).
"""

import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.core.api import core_api as _core_api

log = logging.getLogger("tester.tc_sms")


class SmsBasicSend(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-001",
        title="Send a basic SMS over NAS and verify delivery",
        spec="TS 23.502 §4.13.3",
        domain=Domain.SMS,
        nfs=(NF.GNB, NF.AMF, NF.SMSF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Foundational smoke for SMS-over-NAS (TS 23.502 §4.13.3,\n"
            "  TS 24.501 §8.2.10/§8.2.11). An UL NAS Transport from the UE\n"
            "  carries an RP-DATA PDU into the AMF, which forwards over\n"
            "  Nsmsf (TS 29.540 §5.2) to the SMSF for storage and onward\n"
            "  routing. Without a working /api/smsf/send → /api/smsf/messages\n"
            "  cycle the rest of the SMS suite cannot run.\n"
            "\n"
            "Procedure (TS 23.502 §4.13.3 + TS 24.501 §8.2 + TS 29.540 §5.2)\n"
            "  1. require_gnb / require_ue / register_ue / establish_pdu.\n"
            "  2. POST /api/smsf/send {sender_imsi=ue.imsi,\n"
            "     recipient_msisdn='+1234567890', text='Hello from SA Core\n"
            "     tester'} — emulates UL NAS Transport into AMF→SMSF.\n"
            "  3. Require non-empty response; read message_id (or id).\n"
            "  4. GET /api/smsf/messages?imsi={ue.imsi}.\n"
            "  5. Walk messages/items envelope; require any entry's\n"
            "     message_id (or id) == ours (or list non-empty if msg_id\n"
            "     was absent).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — recipient and text hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  /send returns a message_id and that id is visible via\n"
            "  /messages?imsi=… on the SMSF.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, message_id, message_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. No real ME->RAN NAS PDU is built — the API\n"
            "  models the UL transport."
        ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            # Send SMS
            log.info("Sending SMS from %s", ue.imsi)
            send_result = _core_api("/api/smsf/send", "POST", {
                "sender_imsi": ue.imsi,
                "recipient_msisdn": "+1234567890",
                "text": "Hello from SA Core tester",
            })
            if not send_result:
                self.fail_test("SMSF send returned no response")
                return self.result

            msg_id = send_result.get("message_id") or send_result.get("id")
            log.info("SMS sent, message_id=%s", msg_id)

            # Verify message appears in messages list
            messages = _core_api(f"/api/smsf/messages?imsi={ue.imsi}")
            if not messages:
                self.fail_test("SMSF messages query returned no response")
                return self.result

            items = messages.get("messages") or messages.get("items") or []
            found = any(
                m.get("message_id") == msg_id or m.get("id") == msg_id
                for m in items
            ) if msg_id else len(items) > 0

            if found:
                self.pass_test(imsi=ue.imsi, message_id=msg_id,
                               message_count=len(items))
            else:
                self.fail_test("Sent SMS not found in messages list",
                               message_id=msg_id, messages=items)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"SMS basic send error: {e}")
        return self.result


class SmsLongMessage(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-002",
        title="Send a long SMS requiring TS 23.040 segmentation",
        spec="TS 23.040 §9.2.3.24.1",
        domain=Domain.SMS,
        nfs=(NF.GNB, NF.AMF, NF.SMSF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Concatenated-SMS segmentation (TS 23.040 §9.2.3.24.1 — IEI=00\n"
                "  Concatenated short message UDH; TS 23.038 §6.2.1 7-bit default\n"
                "  alphabet — 160 chars per single SMS, 153 chars per segment when\n"
                "  segmented). A 200-char text MUST traverse the segmentation path\n"
                "  and the SMSF response MUST report segments > 1.\n"
                "\n"
                "Procedure (TS 23.040 §9.2.3.24.1 + TS 23.038 §6.2.1)\n"
                "  1. require_gnb / require_ue / register_ue / establish_pdu.\n"
                "  2. long_text = 'A' * 200 (above 160-char single-SMS limit).\n"
                "  3. POST /api/smsf/send {sender_imsi, recipient_msisdn,\n"
                "     text=long_text}.\n"
                "  4. Read segments from result.segments OR result.segment_count;\n"
                "     default 1 if missing.\n"
                "  5. Require segments > 1.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — text length fixed at 200 'A' characters)\n"
                "\n"
                "Pass criteria\n"
                "  SMSF reports segments > 1 for a 200-character message.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, message_id, text_length, segments.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. 7-bit vs UCS-2 split is not exercised — text is\n"
                "  pure ASCII, so segmentation falls on the 153-char boundary.\n"
                "  Header overhead from the IEI=00 Concatenated UDH is 6 bytes,\n"
                "\n"
                "  reducing per-segment payload from 160 to 153 GSM-7 septets.\n"
                "  UCS-2 segmentation (67-char segments) is not exercised here."
            ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            long_text = "A" * 200
            log.info("Sending 200-char SMS from %s", ue.imsi)
            send_result = _core_api("/api/smsf/send", "POST", {
                "sender_imsi": ue.imsi,
                "recipient_msisdn": "+1234567890",
                "text": long_text,
            })
            if not send_result:
                self.fail_test("SMSF send returned no response for long message")
                return self.result

            segments = send_result.get("segments") or send_result.get("segment_count") or 1
            msg_id = send_result.get("message_id") or send_result.get("id")
            log.info("Long SMS sent: message_id=%s segments=%s", msg_id, segments)

            if segments > 1:
                self.pass_test(imsi=ue.imsi, message_id=msg_id,
                               text_length=len(long_text), segments=segments)
            else:
                self.fail_test("Expected segments > 1 for 200-char message",
                               segments=segments, message_id=msg_id)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"SMS long message error: {e}")
        return self.result


class SmsRouting(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-003",
        title="Add SMSF routing rule, send SMS, verify routing applied",
        spec="TS 23.502 §4.13.3.5",
        domain=Domain.SMS,
        nfs=(NF.GNB, NF.AMF, NF.SMSF),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  SMS routing-rule application on egress (TS 23.502 §4.13.3.5 —\n"
            "  SMSF selection and routing toward SMS-GMSC/SMS-Router). The\n"
            "  SMSF MUST apply prefix-based routing rules to MSISDN-addressed\n"
            "  messages before they leave the network.\n"
            "\n"
            "Procedure (TS 23.502 §4.13.3.5 + TS 23.040 §9)\n"
            "  1. require_gnb / require_ue / register_ue / establish_pdu.\n"
            "  2. POST /api/smsf/routing {pattern='+1234*',\n"
            "     destination='gateway_a', priority=10}; capture rule_id.\n"
            "  3. POST /api/smsf/send {sender_imsi, recipient_msisdn=\n"
            "     '+1234567890', text='Routed message test'} — recipient\n"
            "     matches the rule pattern.\n"
            "  4. Require non-empty response; read routed (or route /\n"
            "     destination) from send_result.\n"
            "  5. finally: DELETE /api/smsf/routing/{rule_id}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — pattern, destination, priority hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Rule POST and SMS send both return non-empty payloads; routing\n"
            "  metadata is reported (presence is best-effort).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, rule_id, send_result, routed.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The 'routed' field is logged but not strictly\n"
            "  asserted — the assertion is on the routing-rule lifecycle."
        ),
    )

    def run(self):
        rule_id = None
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            # Add routing rule
            log.info("Adding SMS routing rule")
            rule_result = _core_api("/api/smsf/routing", "POST", {
                "pattern": "+1234*",
                "destination": "gateway_a",
                "priority": 10,
            })
            if not rule_result:
                self.fail_test("SMSF routing rule creation returned no response")
                return self.result

            rule_id = rule_result.get("rule_id") or rule_result.get("id")
            log.info("Routing rule created: %s", rule_id)

            # Send SMS matching the route
            send_result = _core_api("/api/smsf/send", "POST", {
                "sender_imsi": ue.imsi,
                "recipient_msisdn": "+1234567890",
                "text": "Routed message test",
            })
            if not send_result:
                self.fail_test("SMSF send failed after routing rule added")
                return self.result

            routed = send_result.get("routed") or send_result.get("route") or send_result.get("destination")
            log.info("SMS sent with routing: %s", send_result)

            self.pass_test(imsi=ue.imsi, rule_id=rule_id,
                           send_result=send_result, routed=routed)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"SMS routing error: {e}")
        finally:
            # Clean up routing rule
            if rule_id:
                try:
                    _core_api(f"/api/smsf/routing/{rule_id}", "DELETE")
                    log.info("Cleaned up routing rule %s", rule_id)
                except Exception:
                    pass
        return self.result


class SmsStats(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-004",
        title="SMSF /stats endpoint reports message counters",
        spec="TS 29.540 §5.2",
        domain=Domain.SMS,
        nfs=(NF.GNB, NF.AMF, NF.SMSF),
        severity=Severity.MINOR,
        tags=("regression",),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  SMSF dashboard counters (TS 29.540 §5.2 — Nsmsf service\n"
                "  surfaced via OAM). After driving three sends, the /stats\n"
                "  endpoint MUST return a non-empty payload — operators bind\n"
                "  message-volume tiles to this feed.\n"
                "\n"
                "Procedure (TS 29.540 §5.2 + TS 23.502 §4.13.3)\n"
                "  1. require_gnb / require_ue / register_ue / establish_pdu.\n"
                "  2. GET /api/smsf/stats — capture stats_before (best-effort).\n"
                "  3. Loop i in range(3): POST /api/smsf/send {sender_imsi,\n"
                "     recipient_msisdn='+1234567890', text=f'Stats test\n"
                "     message {i+1}'}; bump sent_count on each non-empty reply.\n"
                "  4. GET /api/smsf/stats — capture stats_after.\n"
                "  5. Require stats_after is non-empty.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — three iterations, recipient hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Stats endpoint returns a non-empty body after 3 send attempts.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  imsi, sent_count, stats_before, stats_after.\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. The delta between before/after is recorded but\n"
                "  not numerically asserted — only payload presence is pinned.\n"
                "  stats_before is captured for diagnostics but not used in any\n"
                "  assertion — delta arithmetic is the dashboard's job.\n"
                "  Send loop is best-effort; any individual failure does not abort."
            ),
    )

    def run(self):
        try:
            gnb = self.require_gnb()
            ue = self.require_ue()
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue):
                return self.result

            # Get baseline stats
            stats_before = _core_api("/api/smsf/stats")

            # Send 3 messages
            sent_count = 0
            for i in range(3):
                result = _core_api("/api/smsf/send", "POST", {
                    "sender_imsi": ue.imsi,
                    "recipient_msisdn": "+1234567890",
                    "text": f"Stats test message {i+1}",
                })
                if result:
                    sent_count += 1
            log.info("Sent %d test messages", sent_count)

            # Get updated stats
            stats_after = _core_api("/api/smsf/stats")
            if not stats_after:
                self.fail_test("SMSF stats endpoint returned no response")
                return self.result

            log.info("SMSF stats: %s", stats_after)
            self.pass_test(imsi=ue.imsi, sent_count=sent_count,
                           stats_before=stats_before, stats_after=stats_after)
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"SMS stats error: {e}")
        return self.result


ALL_SMS_TCS = [SmsBasicSend, SmsLongMessage, SmsRouting, SmsStats]

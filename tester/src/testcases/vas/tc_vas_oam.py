# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: SMSF / USSD / Supplementary / MBS — operator-API only.

These TCs hit /api/* directly (no UE/gNB required). Specs:

  SMSF — TS 23.502 §4.13.3 (SMS over NAS), TS 23.040 (TPDU + UDH +
         TP-VP), TS 23.038 (GSM-7 alphabet), TS 24.011 (CP/RP).
  USSD — TS 24.390 §4.2 (USSD over IMS), TS 22.090 (Stage 1).
  Supp — TS 24.604/611/615/607/608 (§4.5 activate/deactivate),
         TS 22.030 §6.5.2 + Annex B Table B.1 (MMI strings).
  MBS  — TS 23.247 §7 (session lifecycle), TS 23.247 §7.2 (TAI
         scoping), TS 23.003 §15.2 (TMGI), §19.4.2 (TAI),
         TS 23.501 Table 5.7.4-1 (5QI).
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

log = logging.getLogger("tester.tc_vas_oam")


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


# ─────────────────────── SMSF ───────────────────────────────

class SmsfStatsEnvelope(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-010",
        title="SMSF /stats returns pending/delivered/failed/expired/total",
        spec="TS 23.502 §4.13.3",
        domain=Domain.SMS,
        nfs=(NF.SMSF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Operator GUI counter envelope for the SMSF /stats endpoint\n"
                "  (TS 23.502 §4.13.3 — SMS over NAS; TS 29.540 §5.2 — Nsmsf\n"
                "  service surfaced via OAM). Dashboard tiles bind to the five\n"
                "  counter fields; missing keys break rendering silently, so this\n"
                "  test is a static schema gate.\n"
                "\n"
                "Procedure (TS 23.502 §4.13.3 + TS 29.540 §5.2)\n"
                "  1. GET /api/smsf/stats.\n"
                "  2. Require status == 200 AND r.ok truthy.\n"
                "  3. For each key in {pending, delivered, failed, expired, total}\n"
                "     — require it appears in r.stats.\n"
                "  4. Any missing key fails with the actual key list.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — passive endpoint probe)\n"
                "\n"
                "Pass criteria\n"
                "  All five counter keys present in r.stats.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only the structured failure payload on regressions).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Counter values are not asserted numerically.\n"
                "  OAM endpoint is unauthenticated in dev; production deployments\n"
                "  front this with the operator-API auth gateway.\n"
                "  Counter zero-state on a freshly booted SMSF still satisfies the\n"
                "  key-presence gate.\n"
                "  The dashboard polls /stats on a 5 s tick — schema regressions break\n"
                "  all five tiles at once.\n"
                "  Empty body is treated as failure since the GUI cannot render."
            ),
    )

    def run(self):
        try:
            r, s = _api("/api/smsf/stats")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"stats failed: {s} {r}")
                return self.result
            st = r.get("stats", {})
            for k in ("pending", "delivered", "failed", "expired", "total"):
                if k not in st:
                    self.fail_test(f"stats missing {k}", got=list(st))
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SmsfSegmentUtility(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-011",
        title="SMSF /segment forces UCS-2 fallback when GSM-7 incompatible",
        spec="TS 23.040 §9.2.3.24.1",
        domain=Domain.SMS,
        nfs=(NF.SMSF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Concatenated-SMS segmentation utility (TS 23.040 §9.2.3.24.1 —\n"
                "  Concatenated short message UDH; TS 23.038 §6.2.1 — GSM-7\n"
                "  Default Alphabet). The /segment helper MUST detect when the\n"
                "  body contains characters outside the GSM-7 alphabet and force\n"
                "  UCS-2 encoding so no character is lost on the wire.\n"
                "\n"
                "Procedure (TS 23.040 §9.2.3.24.1 + TS 23.038 §6.2.1)\n"
                "  1. POST /api/smsf/segment {body='Hello world', encoding='gsm7'}\n"
                "     — require encoding=='gsm7', count==1, gsm7_compatible truthy.\n"
                "  2. POST /api/smsf/segment {body='こんにちは',\n"
                "     encoding='gsm7'} — require encoding=='ucs2' (forced),\n"
                "     gsm7_compatible not True.\n"
                "  3. POST /api/smsf/segment {body='x', encoding='rot13'} —\n"
                "     unknown encoding must return HTTP 400.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — three canonical probes hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Pure GSM-7 body stays gsm7+single-segment; kanji forces UCS-2;\n"
                "  unknown encoding returns 400.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payloads on shape mismatch).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Segment counts beyond 1 are not asserted here\n"
                "  (see TC-SMS-002 in tc_sms.py for the long-message path).\n"
                "  Mixed GSM-7 / UCS-2 bodies are not exercised here — the parser\n"
                "  promotes to UCS-2 on any non-GSM-7 character.\n"
                "  Encoding 'gsm7' is the default; explicit 'ucs2' bypasses the auto-\n"
                "  detect path entirely."
            ),
    )

    def run(self):
        try:
            # Pure GSM-7 body: 1 segment, gsm7
            r, _ = _api("/api/smsf/segment", "POST",
                        {"body": "Hello world", "encoding": "gsm7"})
            if r.get("encoding") != "gsm7" or r.get("count") != 1:
                self.fail_test(f"gsm7 path: {r}")
                return self.result
            if not r.get("gsm7_compatible"):
                self.fail_test(f"gsm7_compatible expected: {r}")
                return self.result
            # Out-of-alphabet (kanji) → forced UCS-2
            r2, _ = _api("/api/smsf/segment", "POST",
                         {"body": "こんにちは", "encoding": "gsm7"})
            if r2.get("encoding") != "ucs2":
                self.fail_test(f"forced ucs2 fallback: {r2}")
                return self.result
            if r2.get("gsm7_compatible") is True:
                self.fail_test(f"gsm7_compatible should be false: {r2}")
                return self.result
            # Bad encoding → 400
            _, s = _api("/api/smsf/segment", "POST",
                        {"body": "x", "encoding": "rot13"})
            if s != 400:
                self.fail_test(f"bad encoding did not 400: {s}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SmsfSendValidation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-012",
        title="SMSF /send rejects bad MSISDN / encoding / oversized body",
        spec="TS 23.040 §9.2",
        domain=Domain.SMS,
        nfs=(NF.SMSF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Input validation envelope for the SMSF /send endpoint\n"
                "  (TS 23.040 §9.2 TPDU layer requires E.164 MSISDNs and a non-\n"
                "  empty payload; TS 23.502 §4.13.3 SMS-over-NAS path). Bad\n"
                "  inputs MUST be rejected with 400 — silent coercion would let\n"
                "  malformed SMS reach the SMS-GMSC.\n"
                "\n"
                "Procedure (TS 23.040 §9.2 + TS 23.502 §4.13.3)\n"
                "  1. POST /api/smsf/send {sender='abc',recipient='+15551234567',\n"
                "     body='x'} — non-E.164 sender; expect 400.\n"
                "  2. POST with sender='+15550001', recipient='12-bad' — bad\n"
                "     recipient; expect 400.\n"
                "  3. POST with valid MSISDNs but encoding='ascii' — unknown\n"
                "     encoding; expect 400.\n"
                "  4. POST with body='' — empty body; expect 400.\n"
                "  5. POST with body='x' * 10001 — oversized; expect 400.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — five fixed probes)\n"
                "\n"
                "Pass criteria\n"
                "  All five negatives return HTTP 400.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure messages on mismatch).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. The 10000-char limit is an SMSF policy ceiling,\n"
                "  not a 3GPP-mandated maximum.\n"
                "  E.164 format check is the SMSF's responsibility — the operator-API\n"
                "  does not normalise prefixes (no '+' stripping).\n"
                "  Encoding validation runs before length validation, so encoding=400\n"
                "  shadows oversized=400."
            ),
    )

    def run(self):
        try:
            # Bad sender MSISDN
            _, s = _api("/api/smsf/send", "POST", {
                "sender": "abc", "recipient": "+15551234567", "body": "x",
            })
            if s != 400:
                self.fail_test(f"bad sender did not 400: {s}")
                return self.result
            # Bad recipient
            _, s2 = _api("/api/smsf/send", "POST", {
                "sender": "+15550001", "recipient": "12-bad", "body": "x",
            })
            if s2 != 400:
                self.fail_test(f"bad recipient did not 400: {s2}")
                return self.result
            # Bad encoding
            _, s3 = _api("/api/smsf/send", "POST", {
                "sender": "+15550001", "recipient": "+15551234567",
                "body": "x", "encoding": "ascii",
            })
            if s3 != 400:
                self.fail_test(f"bad encoding did not 400: {s3}")
                return self.result
            # Empty body
            _, s4 = _api("/api/smsf/send", "POST", {
                "sender": "+15550001", "recipient": "+15551234567",
                "body": "",
            })
            if s4 != 400:
                self.fail_test(f"empty body did not 400: {s4}")
                return self.result
            # Oversized body (10001 chars)
            _, s5 = _api("/api/smsf/send", "POST", {
                "sender": "+15550001", "recipient": "+15551234567",
                "body": "x" * 10001,
            })
            if s5 != 400:
                self.fail_test(f"oversized body did not 400: {s5}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SmsfRoutingCRUD(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-013",
        title="SMSF routing rule CRUD round-trip + bad route_type -> 400",
        spec="TS 23.502 §4.13.3.5",
        domain=Domain.SMS,
        nfs=(NF.SMSF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  SMSF routing-table CRUD contract (TS 23.502 §4.13.3.5 — SMSF\n"
                "  selection / SMS-Router gateway selection). Operators MUST be\n"
                "  able to add a prefix-based routing rule, see it in the listing,\n"
                "  and delete it. Bad inputs MUST return 400.\n"
                "\n"
                "Procedure (TS 23.502 §4.13.3.5)\n"
                "  1. POST /api/smsf/routing {msisdn_pattern='+1555.*',\n"
                "     route_type='carrier-pigeon'} — unknown type; expect 400.\n"
                "  2. POST with msisdn_pattern='' — empty pattern; expect 400.\n"
                "  3. POST {msisdn_pattern='+1888.*', route_type='smsc',\n"
                "     destination='smsc-east', priority=50} — valid create.\n"
                "  4. Read rule.id from the response; require non-empty rid.\n"
                "  5. GET /api/smsf/routing; require any rules[].id == rid.\n"
                "  6. DELETE /api/smsf/routing/{rid} — require HTTP 200.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Two validation 400s, valid create returns an id, listing\n"
                "  contains the id, and delete returns 200.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure messages on regressions).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Valid route_type vocabulary is local|smsc|forward.\n"
                "  Routing rules are stored ordered by priority ascending; priority\n"
                "  ties fall back to insertion order.\n"
                "  Pattern syntax is regex-lite per the SMSF docs; full PCRE is not\n"
                "  supported."
            ),
    )

    def run(self):
        try:
            # Bad route_type → 400
            _, s = _api("/api/smsf/routing", "POST", {
                "msisdn_pattern": "+1555.*", "route_type": "carrier-pigeon",
            })
            if s != 400:
                self.fail_test(f"bad route_type did not 400: {s}")
                return self.result
            # Empty pattern → 400
            _, s2 = _api("/api/smsf/routing", "POST", {
                "msisdn_pattern": "",
            })
            if s2 != 400:
                self.fail_test(f"empty pattern did not 400: {s2}")
                return self.result
            # Valid create — vocab per nf/smsf is local|smsc|forward
            r, _ = _api("/api/smsf/routing", "POST", {
                "msisdn_pattern": "+1888.*", "route_type": "smsc",
                "destination": "smsc-east", "priority": 50,
            })
            rule = r.get("rule", {})
            rid = rule.get("id")
            if not rid:
                self.fail_test(f"create returned no id: {r}")
                return self.result
            # List
            lst, _ = _api("/api/smsf/routing")
            if not any(rule.get("id") == rid for rule in lst.get("rules", [])):
                self.fail_test(f"new rule not in list: {lst}")
                return self.result
            # Delete
            _, sd = _api(f"/api/smsf/routing/{rid}", "DELETE")
            if sd != 200:
                self.fail_test(f"delete failed: {sd}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SmsfMessageNotFound(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SMS-014",
        title="SMSF GET /messages/{unknown} -> 404; non-integer id -> 400",
        spec="TS 23.040",
        domain=Domain.SMS,
        nfs=(NF.SMSF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Error-envelope contract for the SMSF /messages/{id} endpoint\n"
                "  (TS 23.040 — message store addressing). The GUI distinguishes\n"
                "  'message not yet ingested' (404) from 'caller bug' (400); the\n"
                "  two MUST never collapse into the same status.\n"
                "\n"
                "Procedure (TS 23.040 + operator-API)\n"
                "  1. GET /api/smsf/messages/9999999 — id far above any persisted\n"
                "     row; expect HTTP 404.\n"
                "  2. GET /api/smsf/messages/not-an-int — non-integer path param;\n"
                "     expect HTTP 400.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — two probes hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Status codes match exactly: 404, 400.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payloads on mismatch).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. The 9999999 marker assumes the SMSF id space\n"
                "  never reaches that value during the test run.\n"
                "  404 vs 400 distinction matters for the GUI: 404 is silent, 400\n"
                "  is a toast.\n"
                "  ID space is integer-only; UUID-like ids are out of scope.\n"
                "  If the SMSF has been seeded with synthetic messages, the 9999999\n"
                "  id MAY collide — the harness avoids that range deliberately.\n"
                "  Negative ids are rejected with 400 too (not asserted here).\n"
                "  Path traversal via '../' is rejected upstream by the router."
            ),
    )

    def run(self):
        try:
            _, s = _api("/api/smsf/messages/9999999")
            if s != 404:
                self.fail_test(f"unknown id did not 404: {s}")
                return self.result
            _, s2 = _api("/api/smsf/messages/not-an-int")
            if s2 != 400:
                self.fail_test(f"non-int id did not 400: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─────────────────────── USSD ───────────────────────────────

class UssdMenuSeedAndList(TestCase):
    SPEC = TestSpec(
        tc_id="TC-USSD-010",
        title="USSD /menus/seed populates the default tree, /menus lists it",
        spec="TS 24.390",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        slice=Slice.NONE,
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Foundational sanity for the USSD menu store (TS 24.390 §4.2 —\n"
                "  USSD over IMS, TS 22.090 Stage 1). The seed bootstraps the\n"
                "  reference menu tree used by *100# / *123# tests; without a\n"
                "  populated menu list every session test fails the lookup step.\n"
                "\n"
                "Procedure (TS 24.390 §4.2 + TS 22.090)\n"
                "  1. POST /api/ussd/menus/seed — require status 200 AND r.ok\n"
                "     truthy AND r.menu_count > 0.\n"
                "  2. GET /api/ussd/menus — require lst.count > 0.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — passive seed + readback)\n"
                "\n"
                "Pass criteria\n"
                "  Seed reports menu_count > 0 AND list shows count > 0.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payload on empty store).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Seed is expected to be idempotent.\n"
                "  Seed is idempotent; rerun adds no new rows.\n"
                "  Menu count exact value is implementation-defined.\n"
                "  Both endpoints share the same store; a non-zero seed but empty\n"
                "  list would indicate a read-path regression.\n"
                "  If menus are cleared between test runs, expect /menus to also\n"
                "  return 0 until a re-seed.\n"
                "  Menu hierarchy (children/leaves) is not asserted here.\n"
                "  Setup.EMPTY ensures no UE pollutes the menu state.\n"
                "  Per-language menu sets are out of scope.\n"
                "  Cache invalidation between seed and list is synchronous."
            ),
    )

    def run(self):
        try:
            r, s = _api("/api/ussd/menus/seed", "POST")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"seed failed: {s} {r}")
                return self.result
            if r.get("menu_count", 0) <= 0:
                self.fail_test(f"menu_count zero after seed: {r}")
                return self.result
            lst, _ = _api("/api/ussd/menus")
            if lst.get("count", 0) <= 0:
                self.fail_test(f"menu list empty: {lst}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class UssdSessionInitiateBalance(TestCase):
    SPEC = TestSpec(
        tc_id="TC-USSD-011",
        title="USSD *123# initiates a session and returns menu text",
        spec="TS 24.390 §4.2.1",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Operator-API USSD session lifecycle (TS 24.390 §4.2.1 — USSD\n"
                "  session establishment over IMS). The *123# service code MUST\n"
                "  open a session, return a session_id and a text payload, and\n"
                "  then close cleanly on /end.\n"
                "\n"
                "Procedure (TS 24.390 §4.2.1)\n"
                "  1. POST /api/ussd/menus/seed (idempotent).\n"
                "  2. POST /api/ussd/session {imsi=baseline.imsi('embb-bulk',10),\n"
                "     ussd_string='*123#'} — require status == 200.\n"
                "  3. Read session_id; require non-empty.\n"
                "  4. Read r.text; require non-empty.\n"
                "  5. POST /api/ussd/session/{sid}/end — require status == 200.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — IMSI from baseline embb-bulk index 10)\n"
                "\n"
                "Pass criteria\n"
                "  Session init returns 200 + sid + non-empty text; /end returns\n"
                "  200.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payloads on any step regression).\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Single-step session only; multi-step navigation\n"
                "  is covered in TC-USSD-003 (tc_ussd.py).\n"
                "  Session timeout (idle TTL) is not exercised here — sessions are\n"
                "  explicitly closed via /end.\n"
                "  Concurrent sessions per IMSI are allowed; cardinality is not\n"
                "  asserted.\n"
                "  Service code parsing is greedy: *123# matches before *1#."
            ),
    )

    def run(self):
        try:
            _api("/api/ussd/menus/seed", "POST")
            r, s = _api("/api/ussd/session", "POST", {
                "imsi": baseline.imsi("embb-bulk", 10),
                "ussd_string": "*123#",
            })
            if s != 200:
                self.fail_test(f"session init failed: {s} {r}")
                return self.result
            sid = r.get("session_id")
            if not sid:
                self.fail_test(f"no session_id: {r}")
                return self.result
            text = r.get("text", "")
            if not text:
                self.fail_test(f"no text in response: {r}")
                return self.result
            # End the session.
            _, s2 = _api(f"/api/ussd/session/{sid}/end", "POST")
            if s2 != 200:
                self.fail_test(f"end failed: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class UssdSessionUnknownCode(TestCase):
    SPEC = TestSpec(
        tc_id="TC-USSD-012",
        title="USSD unknown *xxx# -> 400; missing imsi -> 400",
        spec="TS 24.390",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Negative-path conformance for the USSD session endpoint\n"
                "  (TS 24.390 §4.2.1 — USSD session establishment). Unknown\n"
                "  service codes MUST be rejected with 400 (not silently swallowed\n"
                "  into the default menu) and missing IMSI MUST also be a 400.\n"
                "\n"
                "Procedure (TS 24.390 §4.2.1)\n"
                "  1. POST /api/ussd/session {imsi=baseline.imsi('embb-bulk',11),\n"
                "     ussd_string='*99999#'} — unknown code; expect 400.\n"
                "  2. POST /api/ussd/session {ussd_string='*123#'} (no imsi) —\n"
                "     expect 400.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — both probes hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Both probes return HTTP 400.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payloads on mismatch).\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. The exact body shape of the error is not asserted —\n"
                "  only the status code.\n"
                "  Diagnostic message body shape is implementation-defined; this\n"
                "  test pins only the status code.\n"
                "  Service-code namespace is open — anything outside the menu store\n"
                "  is treated as 'unknown'.\n"
                "  Other malformed strings (missing leading '*') are covered by\n"
                "  the Robot mirror.\n"
                "  Empty body POST is a separate negative path.\n"
                "  Concurrency hammering is not part of this test.\n"
                "  401/403 (auth) cannot occur here — operator-API is open in dev."
            ),
    )

    def run(self):
        try:
            _, s = _api("/api/ussd/session", "POST", {
                "imsi": baseline.imsi("embb-bulk", 11),
                "ussd_string": "*99999#",
            })
            if s != 400:
                self.fail_test(f"unknown code did not 400: {s}")
                return self.result
            # Missing IMSI
            _, s2 = _api("/api/ussd/session", "POST", {"ussd_string": "*123#"})
            if s2 != 400:
                self.fail_test(f"missing imsi did not 400: {s2}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class UssdMenuCRUD(TestCase):
    SPEC = TestSpec(
        tc_id="TC-USSD-013",
        title="USSD menu CRUD: create / patch / delete a node",
        spec="TS 24.390",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  Operator-API CRUD for USSD menu nodes (TS 24.390 — USSD over\n"
                "  IMS). Provisioning a menu node, patching its title, and\n"
                "  deleting it MUST all succeed; PATCHing only unknown keys MUST\n"
                "  return 400 so silent no-ops never reach production menus.\n"
                "\n"
                "Procedure (TS 24.390 + operator-API)\n"
                "  1. POST /api/ussd/menus {code='*909#', title='tc-ussd-013',\n"
                "     action_type='custom_text', action_data=...} — require 200\n"
                "     and non-empty id.\n"
                "  2. PATCH /api/ussd/menus/{mid} {title='tc-ussd-013-renamed'}\n"
                "     — require 200.\n"
                "  3. PATCH /api/ussd/menus/{mid} {unknown_key='x'} — only\n"
                "     unknown keys; expect 400.\n"
                "  4. DELETE /api/ussd/menus/{mid} — require 200.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — fixtures hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Create=200, patch=200, unknown-key patch=400, delete=200.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payloads on regressions).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. Children / hierarchy operations are out of scope.\n"
                "  Sibling reordering (move within parent) is not exercised here.\n"
                "  Concurrent edits to the same menu id are not serialised by this\n"
                "  test — single-writer assumption.\n"
                "  Validation of action_type vocabulary is upstream and pinned in\n"
                "  the Robot mirror."
            ),
    )

    def run(self):
        try:
            r, s = _api("/api/ussd/menus", "POST", {
                "code": "*909#",
                "title": "tc-ussd-013",
                "action_type": "custom_text",
                "action_data": "Test menu node",
            })
            if s != 200:
                self.fail_test(f"create failed: {s} {r}")
                return self.result
            mid = r.get("id")
            if not mid:
                self.fail_test(f"no id: {r}")
                return self.result
            # Patch title
            _, s2 = _api(f"/api/ussd/menus/{mid}", "PATCH",
                         {"title": "tc-ussd-013-renamed"})
            if s2 != 200:
                self.fail_test(f"patch failed: {s2}")
                return self.result
            # Empty patch → 400
            _, s3 = _api(f"/api/ussd/menus/{mid}", "PATCH",
                         {"unknown_key": "x"})
            if s3 != 400:
                self.fail_test(f"empty patch did not 400: {s3}")
                return self.result
            # Delete
            _, s4 = _api(f"/api/ussd/menus/{mid}", "DELETE")
            if s4 != 200:
                self.fail_test(f"delete failed: {s4}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─────────────────────── Supplementary ──────────────────────

class SuppActivateCFUOpAPI(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SS-010",
        title="Supplementary CFU activate / interrogate / deactivate (op API)",
        spec="TS 24.604 §4.5.1",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Operator-API view of the CFU state machine (TS 24.604 §4.5.1\n"
            "  Activation, §4.5.2 Deactivation, §4.5.7 Interrogation). Same\n"
            "  state machine as tc_supplementary.SsCallForwarding but driven\n"
            "  through the bare /api/supplementary/* endpoints with no UE\n"
            "  bring-up dependency, and the interrogate body shape\n"
            "  (record.active in {0,1}) is asserted explicitly.\n"
            "\n"
            "Procedure (TS 24.604 §4.5.1 + §4.5.2 + §4.5.7)\n"
            "  1. imsi = baseline.imsi('embb-bulk', 99).\n"
            "  2. POST /api/supplementary/activate {imsi, service_type='CFU',\n"
            "     forwarding_number='+15551234567'} — require 200.\n"
            "  3. GET /api/supplementary/interrogate?imsi=…&service_type=CFU\n"
            "     — require exists truthy AND record.active == 1.\n"
            "  4. POST /api/supplementary/deactivate {imsi,\n"
            "     service_type='CFU'} — require 200.\n"
            "  5. GET /interrogate again — require record.active == 0.\n"
            "  6. DELETE /api/supplementary/services?imsi={imsi} (cleanup).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSI from baseline; forwarding number fixed)\n"
            "\n"
            "Pass criteria\n"
            "  active flips from 1 to 0 across the activate→deactivate cycle.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — only structured failure payloads on regressions).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Only the unconditional variant is exercised."
        ),
    )

    def run(self):
        try:
            imsi = baseline.imsi("embb-bulk", 99)
            # Activate
            r, s = _api("/api/supplementary/activate", "POST", {
                "imsi": imsi, "service_type": "CFU",
                "forwarding_number": "+15551234567",
            })
            if s != 200:
                self.fail_test(f"activate failed: {s} {r}")
                return self.result
            # Interrogate
            ri, _ = _api(f"/api/supplementary/interrogate?imsi={imsi}&service_type=CFU")
            if not ri.get("exists") or ri.get("record", {}).get("active") != 1:
                self.fail_test(f"interrogate wrong: {ri}")
                return self.result
            # Deactivate
            _, sd = _api("/api/supplementary/deactivate", "POST", {
                "imsi": imsi, "service_type": "CFU",
            })
            if sd != 200:
                self.fail_test(f"deactivate failed: {sd}")
                return self.result
            ri2, _ = _api(f"/api/supplementary/interrogate?imsi={imsi}&service_type=CFU")
            if ri2.get("record", {}).get("active") != 0:
                self.fail_test(f"interrogate after deactivate: {ri2}")
                return self.result
            # Cleanup
            _api(f"/api/supplementary/services?imsi={imsi}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SuppActivateValidation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SS-011",
        title="Supplementary activate: bad service_type / forwarding_number -> 400",
        spec="TS 24.604 §4.5.1",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.BASELINE,
        description=(
                "Purpose\n"
                "  Input validation envelope on /api/supplementary/activate\n"
                "  (TS 24.604 §4.5.1 — CDIV activation requires a target SIA on\n"
                "  every CF-family service). Unknown service_type, missing\n"
                "  forwarding_number for CFU, and a badly-shaped forwarding number\n"
                "  MUST all be rejected with 400 — silent acceptance would let\n"
                "  unreachable forwarding loops install.\n"
                "\n"
                "Procedure (TS 24.604 §4.5.1 + TS 23.003 §3.3 MSISDN format)\n"
                "  1. POST /api/supplementary/activate {imsi=baseline.imsi(\n"
                "     'miot-pool', 0), service_type='BOGUS'} — expect 400.\n"
                "  2. POST {imsi, service_type='CFU'} (no forwarding_number) —\n"
                "     expect 400.\n"
                "  3. POST {imsi, service_type='CFU',\n"
                "     forwarding_number='not-a-number'} — expect 400.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — three probes hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  All three probes return HTTP 400.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payloads on mismatch).\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Other CF-family variants (CFB, CFNRy, CFNRc)\n"
                "  follow the same SIA rule but are not driven by this test.\n"
                "  Validation runs at the operator-API edge; behind it the IMS HSS\n"
                "  would also reject these inputs.\n"
                "  Per-service forwarding-number rules (CFB/CFNRy/CFNRc all require\n"
                "  DN) share the same code path."
            ),
    )

    def run(self):
        try:
            # Unknown service_type
            _, s = _api("/api/supplementary/activate", "POST", {
                "imsi": baseline.imsi("miot-pool", 0), "service_type": "BOGUS",
            })
            if s != 400:
                self.fail_test(f"bad service_type did not 400: {s}")
                return self.result
            # CFU without forwarding_number → 400
            _, s2 = _api("/api/supplementary/activate", "POST", {
                "imsi": baseline.imsi("miot-pool", 0), "service_type": "CFU",
            })
            if s2 != 400:
                self.fail_test(f"CFU without DN did not 400: {s2}")
                return self.result
            # Forwarding to bad number → 400
            _, s3 = _api("/api/supplementary/activate", "POST", {
                "imsi": baseline.imsi("miot-pool", 0), "service_type": "CFU",
                "forwarding_number": "not-a-number",
            })
            if s3 != 400:
                self.fail_test(f"bad forwarding_number did not 400: {s3}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SuppMMIParse(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SS-012",
        title="MMI string parser: *21*+1234# -> CFU registration; *#21# -> interrogation",
        spec="TS 22.030 §6.5.2",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  MMI shorthand parser (TS 22.030 §6.5.2 + Annex B Table B.1 —\n"
            "  control of supplementary services via the keypad). The parser\n"
            "  MUST classify the canonical procedure variants per the *SC*SI#\n"
            "  / *SC# / *#SC# / #SC# template:\n"
            "    *SC*SIA#  → Registration when SC is a CF service (SIA is\n"
            "                the forwarded-to number).\n"
            "    *SC#      → Activation (no Supplementary Info).\n"
            "    *#SC#     → Interrogation.\n"
            "\n"
            "Procedure (TS 22.030 §6.5.2 + Annex B Table B.1)\n"
            "  1. POST /api/supplementary/mmi {mmi='*21*+15551234567#'} —\n"
            "     SC=21 (CFU), SIA=+15551234567. Require service_code=='21',\n"
            "     service_name=='CFU', sia=='+15551234567',\n"
            "     procedure=='registration' (CF+SIA → Registration).\n"
            "  2. POST {mmi='*43#'} — SC=43 (CW), no SI. Require\n"
            "     procedure=='activation'.\n"
            "  3. POST {mmi='*#21#'} — Interrogation form. Require\n"
            "     procedure=='interrogation'.\n"
            "  4. POST {mmi='no-trailing-hash'} — malformed; expect 400.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — four canonical MMI strings hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Each of the three valid MMIs parses to the expected\n"
            "  service_code / service_name / procedure; the malformed string\n"
            "  returns 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — only structured failure payloads on regressions).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Deactivation (#SC#) and Erasure (##SC#) are not\n"
            "  exercised here."
        ),
    )

    def run(self):
        try:
            # *SC*SI# with SIA = forwarded-to DN on a CF service is
            # Registration per TS 22.030 §6.5.2 ("forwarded-to number"
            # disambiguation), not Activation.
            r, s = _api("/api/supplementary/mmi", "POST",
                        {"mmi": "*21*+15551234567#"})
            if s != 200:
                self.fail_test(f"parse failed: {s} {r}")
                return self.result
            if r.get("service_code") != "21" or r.get("service_name") != "CFU":
                self.fail_test(f"wrong sc/name: {r}")
                return self.result
            if r.get("sia") != "+15551234567":
                self.fail_test(f"wrong sia: {r}")
                return self.result
            # CF + SIA → Registration, not Activation.
            if (r.get("procedure") or "").lower() != "registration":
                self.fail_test(f"CF+SIA must be Registration: {r}")
                return self.result
            # Bare activation: *43# (CW, no SI) is plain Activation.
            ra, _ = _api("/api/supplementary/mmi", "POST",
                         {"mmi": "*43#"})
            if (ra.get("procedure") or "").lower() != "activation":
                self.fail_test(f"*43# must be Activation: {ra}")
                return self.result
            # Interrogation form *#SC#
            r2, _ = _api("/api/supplementary/mmi", "POST",
                         {"mmi": "*#21#"})
            if (r2.get("procedure") or "").lower() != "interrogation":
                self.fail_test(f"*#21# must be Interrogation: {r2}")
                return self.result
            # Bad MMI → 400
            _, sb = _api("/api/supplementary/mmi", "POST",
                         {"mmi": "no-trailing-hash"})
            if sb != 400:
                self.fail_test(f"bad MMI did not 400: {sb}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class SuppBulkSetMixed(TestCase):
    SPEC = TestSpec(
        tc_id="TC-SS-013",
        title="Supplementary /bulk applies mixed activate/deactivate set",
        spec="TS 24.611 §4.5.1",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.BASELINE,
        description=(
            "Purpose\n"
            "  Bulk supplementary-service flip on the operator API. Covers a\n"
            "  mixed set across three feature families: Communication Waiting\n"
            "  (TS 24.615 §4.5 — formally moved out of TS 24.611 in Rel-10+),\n"
            "  Barring of All Outgoing Calls (TS 24.611 §4.5.1 — with barring\n"
            "  password), and Originating Identification Restriction (TS 24.607\n"
            "  §4.5). The /bulk endpoint MUST report ok=true on each per-row\n"
            "  result so partial failure is surfaced atomically.\n"
            "\n"
            "Procedure (TS 24.615 §4.5 + TS 24.611 §4.5.1 + TS 24.607 §4.5)\n"
            "  1. imsi = baseline.imsi('miot-pool', 12).\n"
            "  2. POST /api/supplementary/bulk {imsi, services=[\n"
            "     {service_type='CW', active=True},\n"
            "     {service_type='BAOC', active=True, barring_password='1234'},\n"
            "     {service_type='OIR', active=False}]}.\n"
            "  3. Require status 200 AND r.ok not False.\n"
            "  4. Require len(results)==3 AND all(x.ok for x in results).\n"
            "  5. DELETE /api/supplementary/services?imsi={imsi} for cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — service set hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  All three per-row results have ok==True.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — only structured failure payloads on regressions).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The barring_password is a fixture; password\n"
            "  rotation semantics are out of scope."
        ),
    )

    def run(self):
        try:
            imsi = baseline.imsi("miot-pool", 12)
            r, s = _api("/api/supplementary/bulk", "POST", {
                "imsi": imsi,
                "services": [
                    {"service_type": "CW", "active": True},
                    {"service_type": "BAOC", "active": True,
                     "barring_password": "1234"},
                    {"service_type": "OIR", "active": False},
                ],
            })
            if s != 200 or r.get("ok") is False:
                self.fail_test(f"bulk failed: {s} {r}")
                return self.result
            # Each per-row result has ok=true
            results = r.get("results", [])
            if len(results) != 3 or not all(x.get("ok") for x in results):
                self.fail_test(f"bulk per-row: {results}")
                return self.result
            _api(f"/api/supplementary/services?imsi={imsi}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


# ─────────────────────── MBS ────────────────────────────────

class MbsTMGIValidation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MBS-010",
        title="MBS TMGI validation: bad TMGI -> 400; hex + FQDN forms accepted",
        spec="TS 23.003 §15.2",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  TMGI (Temporary Mobile Group Identity) parser conformance for\n"
            "  the MBS session endpoint (TS 23.003 §15.2 — TMGI format; TS\n"
            "  23.247 §7 — MBS session lifecycle). Two canonical forms MUST be\n"
            "  accepted: the raw 12-hex form and the FQDN form. Anything else\n"
            "  MUST return 400.\n"
            "\n"
            "Procedure (TS 23.003 §15.2 + TS 23.247 §7)\n"
            "  1. POST /api/mbs/sessions {tmgi='not-a-tmgi',\n"
            "     name='tc010-bad'} — expect 400.\n"
            "  2. POST {tmgi='0001A2B3C4D5', name='tc010-hex',\n"
            "     session_type='multicast'} — raw 12-hex; expect 200 or 201;\n"
            "     capture session.id.\n"
            "  3. POST {tmgi='ABCDEF@001.99.mbms.3gppnetwork.org',\n"
            "     name='tc010-fqdn', session_type='broadcast'} — FQDN form;\n"
            "     expect 200 or 201; capture session.id.\n"
            "  4. DELETE both sessions for cleanup.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — three canonical TMGI probes hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Bad TMGI returns 400; both valid forms return 2xx.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — only structured failure payloads on regressions).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. The 'mbms.3gppnetwork.org' suffix is the canonical\n"
            "  3GPP-reserved FQDN for TMGI-based service identifiers."
        ),
    )

    def run(self):
        try:
            # Bad TMGI (not hex)
            _, s = _api("/api/mbs/sessions", "POST", {
                "tmgi": "not-a-tmgi", "name": "tc010-bad",
            })
            if s != 400:
                self.fail_test(f"bad TMGI did not 400: {s}")
                return self.result
            # Raw 12-hex form
            r, s2 = _api("/api/mbs/sessions", "POST", {
                "tmgi": "0001A2B3C4D5", "name": "tc010-hex",
                "session_type": "multicast",
            })
            if s2 != 200 and s2 != 201:
                self.fail_test(f"hex TMGI rejected: {s2} {r}")
                return self.result
            sid_hex = r.get("session", {}).get("id")
            # FQDN form
            r2, s3 = _api("/api/mbs/sessions", "POST", {
                "tmgi": "ABCDEF@001.99.mbms.3gppnetwork.org",
                "name": "tc010-fqdn",
                "session_type": "broadcast",
            })
            if s3 != 200 and s3 != 201:
                self.fail_test(f"fqdn TMGI rejected: {s3} {r2}")
                return self.result
            sid_fqdn = r2.get("session", {}).get("id")
            # Cleanup
            if sid_hex:
                _api(f"/api/mbs/sessions/{sid_hex}", "DELETE")
            if sid_fqdn:
                _api(f"/api/mbs/sessions/{sid_fqdn}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class Mbs5QIValidation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MBS-011",
        title="MBS out-of-range 5QI -> 400",
        spec="TS 23.501 §5.7.4",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("negative", "conformance"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  5QI range check on MBS session creation (TS 23.501 §5.7.4\n"
                "  Table 5.7.4-1 — Standardised 5QI values run 1..85 with the\n"
                "  reserved/operator-specific space defined up to 255). A 5QI of\n"
                "  999 falls outside the standardised + operator-specific range\n"
                "  and MUST be rejected with 400 before any session resource is\n"
                "  allocated.\n"
                "\n"
                "Procedure (TS 23.501 §5.7.4 Table 5.7.4-1)\n"
                "  1. POST /api/mbs/sessions {tmgi='0001AAAAAAAA',\n"
                "     name='tc011-bad-qi', qos_5qi=999}.\n"
                "  2. Require HTTP 400.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — single probe with qos_5qi=999)\n"
                "\n"
                "Pass criteria\n"
                "  Out-of-range 5QI returns HTTP 400.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payload on regression).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. The lower-bound check (5QI=0) is not exercised\n"
                "  here — only the high end.\n"
                "  Standardised 5QIs end at 85 in TS 23.501 Table 5.7.4-1; the\n"
                "  operator-specific range runs 128..254 with a reserved bin in\n"
                "  between. Values >255 are unambiguously invalid.\n"
                "  Lower-bound (5QI=0) rejection is exercised by the Robot mirror.\n"
                "  The MBS session is never allocated on rejection so no cleanup\n"
                "  is required after this probe."
            ),
    )

    def run(self):
        try:
            # 5QI = 999 → out of [1, 255]
            _, s = _api("/api/mbs/sessions", "POST", {
                "tmgi": "0001AAAAAAAA", "name": "tc011-bad-qi",
                "qos_5qi": 999,
            })
            if s != 400:
                self.fail_test(f"bad 5QI did not 400: {s}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class MbsTAIValidation(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MBS-012",
        title="MBS TAI validation: bad TAI -> 400; valid TAI accepted",
        spec="TS 23.003 §19.4.2",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        description=(
                "Purpose\n"
                "  TAI (Tracking Area Identity) parser on the MBS area endpoint\n"
                "  (TS 23.003 §19.4.2.3 — TAI format MCC-MNC-TAC; TS 23.247 §7.2\n"
                "  — TAI scoping for MBS session delivery). Malformed TAI strings\n"
                "  MUST be rejected with 400; properly-shaped CSV lists of MCC-\n"
                "  MNC-TAC tuples MUST be accepted.\n"
                "\n"
                "Procedure (TS 23.003 §19.4.2.3 + TS 23.247 §7.2)\n"
                "  1. POST /api/mbs/areas {name='tc012-bad',\n"
                "     tracking_areas='00101-XYZ123'} — non-hex TAC; expect 400.\n"
                "  2. POST {name='tc012-good',\n"
                "     tracking_areas='00101-000001,00101-000002',\n"
                "     description='tester area'} — valid MCC-MNC-TAC pair;\n"
                "     expect 200 or 201.\n"
                "  3. Capture area.id and DELETE for cleanup.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none — two canonical probes hard-coded)\n"
                "\n"
                "Pass criteria\n"
                "  Bad TAI returns 400; valid TAI list returns 2xx.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  (none — only structured failure payloads on regressions).\n"
                "\n"
                "Known constraints\n"
                "  Setup.EMPTY. The MCC=001/MNC=01 prefix is the canonical\n"
                "  test-PLMN; TACs are zero-padded 6-hex.\n"
                "  Non-hex characters in TAC are the most common operator typo;\n"
                "  this test pins the rejection so silent acceptance never reaches\n"
                "  the data plane.\n"
                "  MNC length variability (2 vs 3 digits) is allowed by TS 23.003\n"
                "  §19.4.2.3 — not exercised here."
            ),
    )

    def run(self):
        try:
            # Bad TAI
            _, s = _api("/api/mbs/areas", "POST", {
                "name": "tc012-bad",
                "tracking_areas": "00101-XYZ123",
            })
            if s != 400:
                self.fail_test(f"bad TAI did not 400: {s}")
                return self.result
            # Valid TAI
            r, s2 = _api("/api/mbs/areas", "POST", {
                "name": "tc012-good",
                "tracking_areas": "00101-000001,00101-000002",
                "description": "tester area",
            })
            if s2 != 201 and s2 != 200:
                self.fail_test(f"valid area rejected: {s2} {r}")
                return self.result
            aid = r.get("area", {}).get("id")
            if aid:
                _api(f"/api/mbs/areas/{aid}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class MbsTAIListManagement(TestCase):
    SPEC = TestSpec(
        tc_id="TC-MBS-013",
        title="MBS area TAI list: append / remove on an existing area",
        spec="TS 23.247 §7.2",
        domain=Domain.VAS,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        description=(
            "Purpose\n"
            "  MBS area-scoping TAI-list manipulation (TS 23.247 §7.2 — TAI\n"
            "  list defines where the MBS session is delivered; TS 23.003\n"
            "  §19.4.2.3 — TAI format). The operator MUST be able to append\n"
            "  TAIs (idempotently), remove TAIs, and have malformed inputs\n"
            "  rejected with 400.\n"
            "\n"
            "Procedure (TS 23.247 §7.2 + TS 23.003 §19.4.2.3)\n"
            "  1. POST /api/mbs/areas {name='tc013-area',\n"
            "     tracking_areas='00101-000001'}; capture area.id (aid).\n"
            "  2. POST /api/mbs/areas/{aid}/tais {append=['00101-000002',\n"
            "     '00101-000003']} — require 200; require all three TAIs\n"
            "     present in returned tracking_areas string.\n"
            "  3. POST {append=['00101-000002']} again — idempotent;\n"
            "     count('00101-000002') in result MUST be 1.\n"
            "  4. POST {remove=['00101-000001']} — require\n"
            "     '00101-000001' NOT in result.\n"
            "  5. POST {} (no append, no remove) — expect 400.\n"
            "  6. POST {append=['bad-tai']} — expect 400.\n"
            "  7. finally: DELETE /api/mbs/areas/{aid}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixtures hard-coded)\n"
            "\n"
            "Pass criteria\n"
            "  Append + idempotent re-append + remove all succeed with\n"
            "  correct content; empty body and bad TAI both return 400.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — only structured failure payloads on regressions).\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Append/remove cannot be combined in one call —\n"
            "  this test exercises them in separate POSTs."
        ),
    )

    def run(self):
        try:
            r, s = _api("/api/mbs/areas", "POST", {
                "name": "tc013-area",
                "tracking_areas": "00101-000001",
            })
            if s != 201 and s != 200:
                self.fail_test(f"create area failed: {s} {r}")
                return self.result
            aid = r.get("area", {}).get("id")
            try:
                # Append two
                ra, sa = _api(f"/api/mbs/areas/{aid}/tais", "POST", {
                    "append": ["00101-000002", "00101-000003"],
                })
                if sa != 200:
                    self.fail_test(f"append failed: {sa} {ra}")
                    return self.result
                tais = ra.get("area", {}).get("tracking_areas", "")
                for t in ("00101-000001", "00101-000002", "00101-000003"):
                    if t not in tais:
                        self.fail_test(f"append missing {t}: {tais}")
                        return self.result
                # Idempotent re-append
                ra2, _ = _api(f"/api/mbs/areas/{aid}/tais", "POST", {
                    "append": ["00101-000002"],
                })
                tais2 = ra2.get("area", {}).get("tracking_areas", "")
                if tais2.count("00101-000002") != 1:
                    self.fail_test(f"append not idempotent: {tais2}")
                    return self.result
                # Remove one
                rr, _ = _api(f"/api/mbs/areas/{aid}/tais", "POST", {
                    "remove": ["00101-000001"],
                })
                taisR = rr.get("area", {}).get("tracking_areas", "")
                if "00101-000001" in taisR:
                    self.fail_test(f"remove failed: {taisR}")
                    return self.result
                # No-op (neither append nor remove) → 400
                _, sn = _api(f"/api/mbs/areas/{aid}/tais", "POST", {})
                if sn != 400:
                    self.fail_test(f"empty body did not 400: {sn}")
                    return self.result
                # Bad TAI in append → 400
                _, sb = _api(f"/api/mbs/areas/{aid}/tais", "POST", {
                    "append": ["bad-tai"],
                })
                if sb != 400:
                    self.fail_test(f"bad TAI in append did not 400: {sb}")
                    return self.result
            finally:
                _api(f"/api/mbs/areas/{aid}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_VAS_OAM_TCS = [
    SmsfStatsEnvelope,
    SmsfSegmentUtility,
    SmsfSendValidation,
    SmsfRoutingCRUD,
    SmsfMessageNotFound,
    UssdMenuSeedAndList,
    UssdSessionInitiateBalance,
    UssdSessionUnknownCode,
    UssdMenuCRUD,
    SuppActivateCFUOpAPI,
    SuppActivateValidation,
    SuppMMIParse,
    SuppBulkSetMixed,
    MbsTMGIValidation,
    Mbs5QIValidation,
    MbsTAIValidation,
    MbsTAIListManagement,
]

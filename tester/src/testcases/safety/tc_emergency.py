# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: 5GS Emergency Services — operator surface.

TS 22.101 §10           Emergency Calls (umbrella).
TS 23.501 §5.16.4       Emergency Services architecture (5GC).
TS 23.501 §5.16.4.6     QoS for Emergency Services.
TS 23.501 §5.16.4.9     Handling of PDU Sessions for Emergency Services
                        (Request type "Emergency Request" = 3).
TS 23.167 §6.2.2 / §7.5 IMS E-CSCF / PSAP interworking.
RFC 5031 §4.2           urn:service:sos[.sub-service].

Drives the SA Core REST surface at /api/emergency/* (singleton
config, active session ledger, PDU classifier, QoS lookup, URN
check, PSAP probe). The actual NAS / NGAP / SIP-INVITE wire path is
not exercised here; that lives in nf/amf/gmm and the IMS handler.
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

log = logging.getLogger("tester.tc_emergency")


def _em_api(path, method="GET", body=None):
    """Call SA Core emergency REST API."""
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


class EmergencyConfigCRUD(TestCase):
    """TC-EMG-001: Read + update + read singleton emergency_config."""
    SPEC = TestSpec(
        tc_id="TC-EMG-001",
        title="Emergency Services singleton config GET/POST round-trip",
        spec="TS 23.501 §5.16.4",
        domain=Domain.EMERGENCY,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the operator-facing schema of the singleton\n"
            "  emergency_config row: every key the AMF / SMF / IMS handler\n"
            "  reads at runtime (enabled flag, DNN, QFI, ARP, max_sessions)\n"
            "  must be present, and allow-list POST must round-trip while\n"
            "  silently dropping unknown fields. TS 23.501 §5.16.4.\n"
            "\n"
            "Procedure (TS 23.501 §5.16.4)\n"
            "  1. GET /api/emergency/config — assert HTTP 200.\n"
            "  2. For each of {enabled, emergency_dnn, emergency_qfi,\n"
            "     arp_priority, max_sessions} assert the key is present.\n"
            "  3. POST {psap_sip_uri, psap_ip, psap_port=5061,\n"
            "     emergency_qfi=6, max_sessions=50, not_a_real_field=...}.\n"
            "  4. Assert HTTP 200 and psap_port=5061, emergency_qfi=6 in\n"
            "     the returned body (round-trip).\n"
            "  5. Reset PSAP fields to '', port 5060, QFI=5 for downstream\n"
            "     tests.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — payloads are hard-coded).\n"
            "\n"
            "Pass criteria\n"
            "  All required GET keys present AND POST round-trips PSAP port\n"
            "  and QFI exactly as set.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Singleton row — TC must restore defaults in its tail to keep\n"
            "  downstream emergency TCs deterministic."
        ),
    )

    def run(self):
        try:
            r1, s1 = _em_api("/api/emergency/config")
            if s1 != 200:
                self.fail_test(f"GET config failed: {s1} {r1}")
                return self.result
            for k in ("enabled", "emergency_dnn", "emergency_qfi",
                      "arp_priority", "max_sessions"):
                if k not in r1:
                    self.fail_test(f"missing config key '{k}'", body=r1)
                    return self.result

            # POST a few changes; allow-list fields only.
            r2, s2 = _em_api("/api/emergency/config", "POST", {
                "psap_sip_uri": "sip:psap@tc-emg.local",
                "psap_ip": "10.99.99.1",
                "psap_port": 5061,
                "emergency_qfi": 6,
                "max_sessions": 50,
                "not_a_real_field": "ignored",
            })
            if s2 != 200:
                self.fail_test(f"POST config failed: {s2} {r2}")
                return self.result
            if r2.get("psap_port") != 5061:
                self.fail_test("PSAP port not persisted", body=r2)
                return self.result
            if r2.get("emergency_qfi") != 6:
                self.fail_test("Emergency QFI not persisted", body=r2)
                return self.result

            # Reset to a sane PSAP-less default for downstream tests.
            _em_api("/api/emergency/config", "POST", {
                "psap_sip_uri": "", "psap_ip": "", "psap_port": 5060,
                "emergency_qfi": 5,
            })
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class EmergencyClassifier(TestCase):
    """TC-EMG-002: PDU classifier — Request type=3 OR DNN=sos.

    TS 23.501 §5.16.4.9 — Both signals are equally valid; the
    classifier must accept either and reject neither.
    """
    SPEC = TestSpec(
        tc_id="TC-EMG-002",
        title="Emergency PDU classifier accepts request_type=3 OR DNN=sos",
        spec="TS 23.501 §5.16.4.9",
        domain=Domain.EMERGENCY,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.BLOCKER,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the PDU classifier: TS 23.501 §5.16.4.9 says EITHER an\n"
            "  Emergency PDU-session Request type (=3) OR a DNN matching the\n"
            "  configured emergency DNN (case-insensitive 'sos') must mark\n"
            "  the session as emergency. Neither alone may be ignored, and\n"
            "  normal IMS/internet PDUs must not be flagged.\n"
            "\n"
            "Procedure (TS 23.501 §5.16.4.9)\n"
            "  1. For each (request_type, dnn, expected) in the case table:\n"
            "       (3, 'internet', True)  — Emergency Request type wins.\n"
            "       (1, 'sos',      True)  — DNN match (lowercase).\n"
            "       (1, 'SOS',      True)  — DNN match (uppercase).\n"
            "       (1, 'internet', False) — neither signal.\n"
            "       (2, 'ims',      False) — existing IMS PDU.\n"
            "  2. GET /api/emergency/classify?request_type=...&dnn=... per\n"
            "     case; assert HTTP 200.\n"
            "  3. Assert response.is_emergency == expected for every row.\n"
            "  4. fail_test on first mismatch with rt/dnn context.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed case table).\n"
            "\n"
            "Pass criteria\n"
            "  Every classify call returns is_emergency exactly as expected.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Classifier only checks request_type and DNN; URN-based\n"
            "  detection on the SIP path is exercised by TC-EMG-003."
        ),
    )

    def run(self):
        try:
            cases = [
                # (request_type, dnn, expected)
                (3, "internet", True),    # Emergency Request type
                (1, "sos",      True),    # DNN=sos (case-insensitive)
                (1, "SOS",      True),    # DNN matches case-insensitively
                (1, "internet", False),   # Neither signal
                (2, "ims",      False),   # Existing IMS PDU
            ]
            for rt, dnn, expected in cases:
                r, s = _em_api(f"/api/emergency/classify?request_type={rt}&dnn={dnn}")
                if s != 200:
                    self.fail_test(f"classify failed: {s} {r}")
                    return self.result
                if r.get("is_emergency") != expected:
                    self.fail_test(
                        f"classify(rt={rt}, dnn={dnn!r}): "
                        f"got {r.get('is_emergency')} expected {expected}")
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class EmergencyURNCheck(TestCase):
    """TC-EMG-003: SIP Request-URI urn:service:sos check (RFC 5031 §4.2)."""
    SPEC = TestSpec(
        tc_id="TC-EMG-003",
        title="Emergency SIP urn:service:sos URN classifier",
        spec="RFC 5031 §4.2",
        domain=Domain.EMERGENCY,
        nfs=(NF.AMF, NF.CSCF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the SIP-side emergency classifier: RFC 5031 §4.2 reserves\n"
            "  urn:service:sos and a fixed set of sub-services\n"
            "  (ambulance/fire/police/mountain/marine). The Request-URI\n"
            "  parser must accept those, be case-insensitive on scheme + ns,\n"
            "  and reject non-emergency URIs (sip:911@... is NOT a URN).\n"
            "\n"
            "Procedure (RFC 5031 §4.2 + TS 23.167 §6.2.2)\n"
            "  1. For each (uri, expected) in the case table:\n"
            "       urn:service:sos             -> True\n"
            "       urn:service:sos.ambulance   -> True\n"
            "       urn:service:sos.fire        -> True\n"
            "       urn:service:sos.police      -> True\n"
            "       URN:Service:SOS             -> True (case-insensitive)\n"
            "       sip:911@psap.example        -> False (not a URN)\n"
            "       urn:service:other           -> False (not sos)\n"
            "  2. minimally percent-encode ':' and ' ' in the URI.\n"
            "  3. GET /api/emergency/check-urn?request_uri=...; assert 200.\n"
            "  4. Assert response.is_emergency == expected.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed case table).\n"
            "\n"
            "Pass criteria\n"
            "  Every check-urn call returns is_emergency exactly as expected.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  Only the URN classifier is exercised; full P-CSCF routing to\n"
            "  E-CSCF on emergency detection lives in the IMS handler tests."
        ),
    )

    def run(self):
        try:
            cases = [
                ("urn:service:sos",            True),
                ("urn:service:sos.ambulance",  True),
                ("urn:service:sos.fire",       True),
                ("urn:service:sos.police",     True),
                ("URN:Service:SOS",            True),  # case-insensitive
                ("sip:911@psap.example",       False),
                ("urn:service:other",          False),
            ]
            for uri, expected in cases:
                # urlencode uri minimally.
                encoded = uri.replace(":", "%3A").replace(" ", "%20")
                r, s = _em_api(f"/api/emergency/check-urn?request_uri={encoded}")
                if s != 200:
                    self.fail_test(f"check-urn failed: {s} {r}")
                    return self.result
                if r.get("is_emergency") != expected:
                    self.fail_test(
                        f"check-urn({uri!r}): got {r.get('is_emergency')} "
                        f"expected {expected}")
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class EmergencyQoS(TestCase):
    """TC-EMG-004: Emergency QoS profile reflects config (TS 23.501 §5.16.4.6)."""
    SPEC = TestSpec(
        tc_id="TC-EMG-004",
        title="Emergency QoS endpoint reflects configured QFI and ARP",
        spec="TS 23.501 §5.16.4.6",
        domain=Domain.EMERGENCY,
        nfs=(NF.AMF, NF.SMF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.16.4.6 requires the emergency PDU session to use\n"
            "  the operator-configured QFI / ARP with resource_type=NonGBR\n"
            "  (default 5QI=5 for IMS-signalling-like emergency PDU). The\n"
            "  /qos endpoint is the runtime view the SMF/PCF consume — it\n"
            "  must reflect /config changes immediately.\n"
            "\n"
            "Procedure (TS 23.501 §5.16.4.6)\n"
            "  1. POST /api/emergency/config with emergency_qfi=7,\n"
            "     arp_priority=2.\n"
            "  2. GET /api/emergency/qos; assert HTTP 200.\n"
            "  3. Assert qfi=7 AND fiveqi=7 (they alias).\n"
            "  4. Assert arp_priority=2.\n"
            "  5. Assert resource_type='NonGBR'.\n"
            "  6. Reset config back to QFI=5, ARP=1 for downstream TCs.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hard-coded QFI/ARP values).\n"
            "\n"
            "Pass criteria\n"
            "  /qos returns qfi=7, fiveqi=7, arp_priority=2,\n"
            "  resource_type=NonGBR after /config write.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  qfi, arp.\n"
            "\n"
            "Known constraints\n"
            "  Resets defaults in its tail; must not run in parallel with\n"
            "  TC-EMG-005 or other QoS-sensitive emergency TCs."
        ),
    )

    def run(self):
        try:
            # Set QFI=7, ARP=2.
            _em_api("/api/emergency/config", "POST",
                    {"emergency_qfi": 7, "arp_priority": 2})

            r, s = _em_api("/api/emergency/qos")
            if s != 200:
                self.fail_test(f"GET qos failed: {s} {r}")
                return self.result
            if r.get("qfi") != 7 or r.get("fiveqi") != 7:
                self.fail_test(f"QFI mismatch: {r}")
                return self.result
            if r.get("arp_priority") != 2:
                self.fail_test(f"ARP mismatch: {r}")
                return self.result
            if r.get("resource_type") != "NonGBR":
                self.fail_test(f"resource_type wrong: {r}")
                return self.result

            # Reset.
            _em_api("/api/emergency/config", "POST",
                    {"emergency_qfi": 5, "arp_priority": 1})
            self.pass_test(qfi=7, arp=2)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class EmergencySessionLifecycle(TestCase):
    """TC-EMG-005: Open + list + release an emergency session."""
    SPEC = TestSpec(
        tc_id="TC-EMG-005",
        title="Emergency session open / list / release lifecycle",
        spec="TS 23.501 §5.16.4.9",
        domain=Domain.EMERGENCY,
        nfs=(NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the emergency session ledger lifecycle: create / list /\n"
            "  release. At least one of IMSI/IMEI must be provided\n"
            "  (TS 23.501 §5.16.4.9 allows IMEI-only when no USIM). Once\n"
            "  created the session must surface in /sessions, and /release\n"
            "  must drop it from the active set.\n"
            "\n"
            "Procedure (TS 23.501 §5.16.4.9)\n"
            "  1. POST /api/emergency/sessions WITHOUT imsi/imei (only\n"
            "     pdu_session_id + ip_addr). Assert HTTP 400.\n"
            "  2. POST /sessions with baseline imsi + IMEI + pdu_session_id\n"
            "     + ip_addr + gnb_ip + tac + cell_id. Assert 200/201 and\n"
            "     that response.id is set.\n"
            "  3. GET /sessions; assert the new id appears in the list.\n"
            "  4. POST /sessions/{id}/release.\n"
            "  5. GET /sessions again; assert the id is gone from active.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — baseline IMSI from src.baseline).\n"
            "\n"
            "Pass criteria\n"
            "  Bad payload 400 AND create succeeds AND session present in\n"
            "  list AND absent from list after /release.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id.\n"
            "\n"
            "Known constraints\n"
            "  Active-list snapshot is taken via plain GET; concurrent TCs\n"
            "  can race against /release in this ledger surface."
        ),
    )

    def run(self):
        try:
            # Validation: neither imei nor imsi must 400.
            br, bs = _em_api("/api/emergency/sessions", "POST",
                              {"pdu_session_id": 1, "ip_addr": "10.99.0.5"})
            if bs != 400:
                self.fail_test(f"Empty IMSI/IMEI did not 400: {bs} {br}")
                return self.result

            r, s = _em_api("/api/emergency/sessions", "POST", {
                "imsi": baseline.imsi("embb-bulk", 0),
                "imei": "350000000000911",
                "pdu_session_id": 1,
                "ip_addr": "10.99.0.5",
                "gnb_ip": "172.30.0.20",
                "tac": "0001",
                "cell_id": "00000001",
            })
            if s not in (200, 201) or not r.get("id"):
                self.fail_test(f"Create session failed: {s} {r}")
                return self.result
            sid = r["id"]

            # Active list contains it.
            actives, _ = _em_api("/api/emergency/sessions")
            if not any(x.get("id") == sid for x in actives):
                self.fail_test("Session missing from active list",
                               count=len(actives))
                return self.result

            # Release.
            _em_api(f"/api/emergency/sessions/{sid}/release", "POST")

            actives2, _ = _em_api("/api/emergency/sessions")
            if any(x.get("id") == sid for x in actives2):
                self.fail_test("Session still active after release")
                return self.result

            self.pass_test(session_id=sid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class EmergencyLocationReporting(TestCase):
    """TC-EMG-010: Location reporting active during emergency session."""
    SPEC = TestSpec(
        tc_id="TC-EMG-010",
        title="Location reporting active during emergency session",
        spec="TS 23.501 §5.16.2",
        domain=Domain.EMERGENCY,
        nfs=(NF.AMF, NF.LMF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "location"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.16.2 mandates location reporting (cell_id + TAC +\n"
            "  optional geodetic) is active during emergency sessions so the\n"
            "  PSAP can receive Geolocation in the SIP INVITE. This TC pins\n"
            "  that the SA-Core ledger preserves the location anchors the\n"
            "  AMF supplied at session create.\n"
            "\n"
            "Procedure (TS 23.501 §5.16.2)\n"
            "  1. POST /api/emergency/sessions with imsi=001010000000910,\n"
            "     imei, pdu_session_id=1, ip_addr=10.99.0.10,\n"
            "     gnb_ip=172.30.0.20, tac=0001, cell_id=00000010.\n"
            "  2. Assert HTTP 200/201 and response.id present.\n"
            "  3. GET /api/emergency/sessions; locate the row by id.\n"
            "  4. Assert non-empty cell_id AND non-empty tac on the row.\n"
            "  5. finally: POST /sessions/{id}/release to clean up.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hard-coded test session payload).\n"
            "\n"
            "Pass criteria\n"
            "  Session created AND active-list row carries both cell_id and\n"
            "  tac as non-empty strings.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id, cell_id, tac.\n"
            "\n"
            "Known constraints\n"
            "  AMF->LMF activation + SIP Geolocation header propagation are\n"
            "  out of scope here — see robot/voice_media/24_emergency.robot."
        ),
    )

    def run(self):
        sid = None
        try:
            r, s = _em_api("/api/emergency/sessions", "POST", {
                "imsi":  "001010000000910",
                "imei":  "350000000000910",
                "pdu_session_id": 1,
                "ip_addr": "10.99.0.10",
                "gnb_ip":  "172.30.0.20",
                "tac":     "0001",
                "cell_id": "00000010",
            })
            if s not in (200, 201) or not r.get("id"):
                self.fail_test(
                    "Python implementation pending — see "
                    "robot/suites/voice_media/24_emergency.robot::TC-EMG-010 "
                    "for the procedure.",
                    response=r, status=s)
                return self.result
            sid = r["id"]

            actives, _ = _em_api("/api/emergency/sessions")
            mine = next((x for x in actives if x.get("id") == sid), None)
            if mine is None:
                self.fail_test("session not surfaced in active list",
                               count=len(actives))
                return self.result
            for k in ("cell_id", "tac"):
                if not mine.get(k):
                    self.fail_test(f"location anchor {k} missing", row=mine)
                    return self.result
            self.pass_test(session_id=sid, cell_id=mine.get("cell_id"),
                           tac=mine.get("tac"))
        except Exception as e:
            self.fail_test(
                "Python implementation pending — see "
                "robot/suites/voice_media/24_emergency.robot::TC-EMG-010 "
                "for the procedure.",
                error=str(e))
        finally:
            if sid:
                try:
                    _em_api(f"/api/emergency/sessions/{sid}/release", "POST")
                except Exception:
                    pass
        return self.result


class EmergencyUnauthenticated(TestCase):
    """TC-EMG-011: Emergency services granted without 5G-AKA."""
    SPEC = TestSpec(
        tc_id="TC-EMG-011",
        title="Emergency session without authentication (no USIM)",
        spec="TS 23.501 §5.16.3",
        domain=Domain.EMERGENCY,
        nfs=(NF.AMF, NF.SMF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "unauthenticated"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.16.3 + TS 23.401 §4.3.12 require emergency-only\n"
            "  service for unauthenticated UEs: registration without 5G-AKA,\n"
            "  IMEI-only identification, emergency PDU with request_type=3.\n"
            "  This TC pins the SA-Core contract that an emergency session\n"
            "  can be created with IMEI only (no IMSI).\n"
            "\n"
            "Procedure (TS 23.501 §5.16.3)\n"
            "  1. POST /api/emergency/sessions with imei=350000000000911,\n"
            "     pdu_session_id=1, ip_addr=10.99.0.11, and NO imsi field\n"
            "     (simulates a USIM-less UE).\n"
            "  2. Assert HTTP 200/201 and response.id present.\n"
            "  3. pass_test(session_id, mode='imei_only').\n"
            "  4. finally: POST /sessions/{id}/release to clean up.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hard-coded IMEI-only payload).\n"
            "\n"
            "Pass criteria\n"
            "  Session create returns 200/201 with an id, despite no IMSI.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id, mode.\n"
            "\n"
            "Known constraints\n"
            "  Full NAS Registration with 5GS Registration type = Emergency\n"
            "  Registration and temporary 5G-GUTI assignment is exercised by\n"
            "  the matching robot/voice_media/24_emergency.robot scenario."
        ),
    )

    def run(self):
        sid = None
        try:
            # IMEI-only — no IMSI, simulating a USIM-less UE.
            r, s = _em_api("/api/emergency/sessions", "POST", {
                "imei":  "350000000000911",
                "pdu_session_id": 1,
                "ip_addr": "10.99.0.11",
            })
            if s not in (200, 201) or not r.get("id"):
                self.fail_test(
                    "Python implementation pending — see "
                    "robot/suites/voice_media/24_emergency.robot::TC-EMG-011 "
                    "for the procedure.",
                    response=r, status=s)
                return self.result
            sid = r["id"]
            self.pass_test(session_id=sid, mode="imei_only")
        except Exception as e:
            self.fail_test(
                "Python implementation pending — see "
                "robot/suites/voice_media/24_emergency.robot::TC-EMG-011 "
                "for the procedure.",
                error=str(e))
        finally:
            if sid:
                try:
                    _em_api(f"/api/emergency/sessions/{sid}/release", "POST")
                except Exception:
                    pass
        return self.result


class EmergencyDeregistrationCleanup(TestCase):
    """TC-EMG-012: Cleanup of emergency state on deregistration."""
    SPEC = TestSpec(
        tc_id="TC-EMG-012",
        title="Emergency deregistration cleans up session + IP + context",
        spec="TS 24.501 §5.5.2.2.1",
        domain=Domain.EMERGENCY,
        nfs=(NF.AMF, NF.SMF, NF.UPF),
        slice=Slice.NONE,
        dnn="",
        severity=Severity.MAJOR,
        tags=("conformance", "cleanup"),
        setup=Setup.EMPTY,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 24.501 §5.5.2 deregistration of an emergency-registered\n"
            "  UE must tear down the PDU session, return the IP, and clear\n"
            "  AMF + SMF + UPF contexts. This TC pins the SA-Core ledger\n"
            "  contract: after /release the session is no longer surfaced\n"
            "  in the active list.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.2.2.1)\n"
            "  1. POST /api/emergency/sessions with imsi=001010000000912,\n"
            "     imei=350000000000912, pdu_session_id=1,\n"
            "     ip_addr=10.99.0.12. Assert 200/201 with response.id.\n"
            "  2. POST /api/emergency/sessions/{sid}/release; assert HTTP\n"
            "     200 from the release endpoint.\n"
            "  3. GET /api/emergency/sessions; assert no row in the active\n"
            "     list has id == sid.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hard-coded session payload).\n"
            "\n"
            "Pass criteria\n"
            "  Release returns HTTP 200 AND session is absent from /sessions\n"
            "  on the post-release GET.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id, active_after_release.\n"
            "\n"
            "Known constraints\n"
            "  NAS Deregistration, IP-pool return and UPF PFCP Session\n"
            "  Deletion are out of scope; see the matching robot scenario."
        ),
    )

    def run(self):
        try:
            r, s = _em_api("/api/emergency/sessions", "POST", {
                "imsi":  "001010000000912",
                "imei":  "350000000000912",
                "pdu_session_id": 1,
                "ip_addr": "10.99.0.12",
            })
            if s not in (200, 201) or not r.get("id"):
                self.fail_test(
                    "Python implementation pending — see "
                    "robot/suites/voice_media/24_emergency.robot::TC-EMG-012 "
                    "for the procedure.",
                    response=r, status=s)
                return self.result
            sid = r["id"]

            _, rs = _em_api(f"/api/emergency/sessions/{sid}/release", "POST")
            if rs != 200:
                self.fail_test(f"release returned {rs}")
                return self.result

            actives, _ = _em_api("/api/emergency/sessions")
            if any(x.get("id") == sid for x in actives):
                self.fail_test("session still active after release",
                               session_id=sid)
                return self.result
            self.pass_test(session_id=sid, active_after_release=0)
        except Exception as e:
            self.fail_test(
                "Python implementation pending — see "
                "robot/suites/voice_media/24_emergency.robot::TC-EMG-012 "
                "for the procedure.",
                error=str(e))
        return self.result


ALL_EMERGENCY_TCS = [
    EmergencyConfigCRUD,
    EmergencyClassifier,
    EmergencyURNCheck,
    EmergencyQoS,
    EmergencySessionLifecycle,
    EmergencyLocationReporting,
    EmergencyUnauthenticated,
    EmergencyDeregistrationCleanup,
]

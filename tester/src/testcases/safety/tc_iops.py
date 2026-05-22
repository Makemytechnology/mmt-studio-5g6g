# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Isolated E-UTRAN Operation for Public Safety (IOPS).

TS 23.401 §K.1     IOPS general description.
TS 23.401 §K.2.1   Operation of isolated public-safety networks.
TS 23.401 §K.2.3   IOPS network configuration (cached AKA tuples).
TS 23.401 §K.2.4   IOPS establishment / termination lifecycle.
TS 22.346          IOPS service requirements (TODO; not loaded locally).

Drives the SA Core REST surface at /api/iops/*: per-gNB IOPS config,
the lifecycle state machine (normal → backhaul_lost → iops_activated
→ restoring → restored), the pre-cached AKA tuple store, the
local-session ledger, and the event log.

All endpoints return `{ok, ...}` envelopes keyed by domain noun.
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

log = logging.getLogger("tester.tc_iops")


def _iops_api(path, method="GET", body=None):
    """Call SA Core IOPS REST API."""
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


GNB = "gnb-iops-test-001"


def _setup_config():
    """UPSERT a fresh per-gNB IOPS config for `GNB`."""
    return _iops_api("/api/iops/config", "POST", {
        "gnb_id": GNB,
        "iops_enabled": 1,
        "local_auth_enabled": 1,
        "max_local_ues": 50,
        "local_ip_pool": "10.99.1.0/24",
    })


class IopsConfigCRUD(TestCase):
    """TC-IOPS-001: UPSERT + GET per-gNB IOPS config (TS 23.401 §K.2.3)."""
    SPEC = TestSpec(
        tc_id="TC-IOPS-001",
        title="IOPS per-gNB config UPSERT and read-back",
        spec="TS 23.401 §K.2.3",
        domain=Domain.SAFETY,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  IOPS per-gNB configuration is the operational anchor for\n"
            "  isolated public-safety operation: max local UEs, local IP\n"
            "  pool, and the local-auth flag are read by the AMF when it\n"
            "  enters iops_activated. The UPSERT contract (POST creates or\n"
            "  updates) and per-gNB read-back must round-trip every field.\n"
            "  TS 23.401 §K.2.3.\n"
            "\n"
            "Procedure (TS 23.401 §K.2.3)\n"
            "  1. POST /api/iops/config with gnb_id=gnb-iops-test-001,\n"
            "     iops_enabled=1, local_auth_enabled=1, max_local_ues=50,\n"
            "     local_ip_pool='10.99.1.0/24'.\n"
            "  2. Assert HTTP 200/201 and response.config carries\n"
            "     max_local_ues=50 and local_ip_pool='10.99.1.0/24'.\n"
            "  3. GET /api/iops/config/{gnb_id} to read it back single-row.\n"
            "  4. Assert HTTP 200 on the read-back.\n"
            "  5. pass_test(config=response.config).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed gNB id and pool).\n"
            "\n"
            "Pass criteria\n"
            "  UPSERT 200/201 with fields round-tripping AND per-gNB GET\n"
            "  succeeds with HTTP 200.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  config — the round-tripped per-gNB IOPS config.\n"
            "\n"
            "Known constraints\n"
            "  Does not verify the AMF actually consumes the config; the\n"
            "  full backhaul-loss path is exercised by TC-IOPS-002."
        ),
    )

    def run(self):
        try:
            r, s = _setup_config()
            if s not in (200, 201):
                self.fail_test(f"UPSERT failed: {s} {r}")
                return self.result
            cfg = r.get("config", {})
            if cfg.get("max_local_ues") != 50:
                self.fail_test(f"max_local_ues mismatch: {cfg}")
                return self.result
            if cfg.get("local_ip_pool") != "10.99.1.0/24":
                self.fail_test(f"ip_pool mismatch: {cfg}")
                return self.result

            # Read back individually.
            r2, s2 = _iops_api(f"/api/iops/config/{GNB}")
            if s2 != 200:
                self.fail_test(f"GET single config failed: {s2} {r2}")
                return self.result
            self.pass_test(config=r2.get("config"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class IopsLifecycle(TestCase):
    """TC-IOPS-002: Declare → status reflects iops_activated → Restore.

    TS 23.401 §K.2.4 — establishment + termination. Declare records
    backhaul_lost + iops_activated; Restore records restoring +
    restored. The state machine refuses skip-state transitions.
    """
    SPEC = TestSpec(
        tc_id="TC-IOPS-002",
        title="IOPS lifecycle: declare backhaul-loss then restore",
        spec="TS 23.401 §K.2.4",
        domain=Domain.SAFETY,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Pins the IOPS establishment + termination state machine:\n"
            "  normal -> backhaul_lost -> iops_activated on declare, then\n"
            "  iops_activated -> restoring -> normal on restore. The /status\n"
            "  table is what the operator console reads; both the per-call\n"
            "  response and the table must agree. TS 23.401 §K.2.4.\n"
            "\n"
            "Procedure (TS 23.401 §K.2.4)\n"
            "  1. _setup_config() — ensure the gNB has an IOPS config row.\n"
            "  2. POST /api/iops/declare with reason=backhaul_failure.\n"
            "  3. Assert HTTP 200 and response.state == 'iops_activated'.\n"
            "  4. GET /api/iops/status; locate row by gnb_id.\n"
            "  5. Assert the row exists AND row.state == 'iops_activated'.\n"
            "  6. POST /api/iops/restore for the same gnb_id.\n"
            "  7. Assert HTTP 200 and response.state == 'normal'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed gNB id).\n"
            "\n"
            "Pass criteria\n"
            "  declare returns state=iops_activated AND /status row shows\n"
            "  iops_activated AND restore returns state=normal.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  State machine refuses skip-state transitions (normal ->\n"
            "  iops_activated direct, etc.); those negatives live in unit\n"
            "  tests, not this conformance TC."
        ),
    )

    def run(self):
        try:
            _setup_config()

            r, s = _iops_api("/api/iops/declare", "POST",
                              {"gnb_id": GNB, "reason": "backhaul_failure"})
            if s != 200:
                self.fail_test(f"declare failed: {s} {r}")
                return self.result
            if r.get("state") != "iops_activated":
                self.fail_test(f"state not iops_activated: {r.get('state')}")
                return self.result

            # /status returns per-gNB rows; ours must show iops_activated.
            ss, _ = _iops_api("/api/iops/status")
            row = next((g for g in ss.get("gnbs", []) if g.get("gnb_id") == GNB),
                       None)
            if not row:
                self.fail_test("gNB missing from status table",
                               gnbs=ss.get("gnbs"))
                return self.result
            if row.get("state") != "iops_activated":
                self.fail_test(f"status row state: {row}")
                return self.result

            rr, rs = _iops_api("/api/iops/restore", "POST", {"gnb_id": GNB})
            if rs != 200:
                self.fail_test(f"restore failed: {rs} {rr}")
                return self.result
            if rr.get("state") != "normal":
                self.fail_test(f"state not normal: {rr.get('state')}")
                return self.result

            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class IopsCachedCredentials(TestCase):
    """TC-IOPS-003: Cache + list AKA tuples (TS 23.401 §K.2.3)."""
    SPEC = TestSpec(
        tc_id="TC-IOPS-003",
        title="IOPS cached AKA credentials store + local-auth probe",
        spec="TS 23.401 §K.2.3",
        domain=Domain.SAFETY,
        nfs=(NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  IOPS local authentication relies on AKA tuples pre-cached at\n"
            "  the gNB from the macro AUSF/UDM before backhaul loss\n"
            "  (TS 23.401 §K.2.3). The cache store must accept batches,\n"
            "  enumerate them per-gNB, drive a local-auth probe to\n"
            "  allowed=True for cached IMSIs, and honour DELETE.\n"
            "\n"
            "Procedure (TS 23.401 §K.2.3)\n"
            "  1. _setup_config() to ensure the gNB has an IOPS row.\n"
            "  2. POST /api/iops/cache-credentials with TWO tuples\n"
            "     (rand_hex, autn_hex, xres_star_hex, kseaf_hex,\n"
            "     expires_at=2030-01-01T00:00:00Z) for baseline IMSIs 0,1.\n"
            "  3. Assert HTTP 200 and response.cached == 2.\n"
            "  4. GET /api/iops/cache/{gnb_id}; assert HTTP 200 and\n"
            "     count >= 2.\n"
            "  5. GET /api/iops/local-auth?gnb_id=...&imsi=...0; assert\n"
            "     result.allowed is truthy (cached IMSI accepted).\n"
            "  6. DELETE /api/iops/cache/{gnb_id}/{imsi1}; GET cache; assert\n"
            "     count == 1.\n"
            "  7. Final cleanup: DELETE the remaining entry.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — baseline IMSIs from src.baseline).\n"
            "\n"
            "Pass criteria\n"
            "  Both tuples cached (count=2), local-auth admits a cached\n"
            "  IMSI, delete decrements count to 1.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  (none — pass_test() is called without metric kwargs).\n"
            "\n"
            "Known constraints\n"
            "  The RAND/AUTN/XRES*/KSEAF values are stub bytes; real EAP-AKA'\n"
            "  derivation is not exercised here."
        ),
    )

    def run(self):
        try:
            _setup_config()

            future = "2030-01-01T00:00:00Z"
            r, s = _iops_api("/api/iops/cache-credentials", "POST", {
                "gnb_id": GNB,
                "credentials": [{
                    "imsi": baseline.imsi("embb-bulk", 0),
                    "rand_hex": "aa" * 16,
                    "autn_hex": "bb" * 16,
                    "xres_star_hex": "cc" * 16,
                    "kseaf_hex": "dd" * 32,
                    "expires_at": future,
                }, {
                    "imsi": baseline.imsi("embb-bulk", 1),
                    "rand_hex": "11" * 16,
                    "autn_hex": "22" * 16,
                    "xres_star_hex": "33" * 16,
                    "kseaf_hex": "44" * 32,
                    "expires_at": future,
                }],
            })
            if s != 200 or r.get("cached") != 2:
                self.fail_test(f"cache failed: {s} {r}")
                return self.result

            ls, ls_s = _iops_api(f"/api/iops/cache/{GNB}")
            if ls_s != 200:
                self.fail_test(f"GET cache failed: {ls_s} {ls}")
                return self.result
            if ls.get("count") < 2:
                self.fail_test(f"count < 2: {ls}")
                return self.result

            # LocalAuth probe — cached IMSI must be allowed.
            au, _ = _iops_api(
                f"/api/iops/local-auth?gnb_id={GNB}&imsi={baseline.imsi('embb-bulk', 0)}")
            if not au.get("result", {}).get("allowed"):
                self.fail_test(f"local-auth probe denied: {au}")
                return self.result

            # Delete one entry, count drops by 1.
            _iops_api(f"/api/iops/cache/{GNB}/{baseline.imsi('embb-bulk', 1)}", "DELETE")
            ls2, _ = _iops_api(f"/api/iops/cache/{GNB}")
            if ls2.get("count") != 1:
                self.fail_test(f"count not 1 after delete: {ls2}")
                return self.result

            # Cleanup — remove last cached entry.
            _iops_api(f"/api/iops/cache/{GNB}/{baseline.imsi('embb-bulk', 0)}", "DELETE")
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class IopsLocalSessions(TestCase):
    """TC-IOPS-004: Open + release a local PDU session.

    TS 23.401 §K.2.4 — Local EPC handles PDU sessions until backhaul
    returns. service_type CHECK is voice/data/ptt/emergency.
    """
    SPEC = TestSpec(
        tc_id="TC-IOPS-004",
        title="IOPS local PDU session open + list + release",
        spec="TS 23.401 §K.2.4",
        domain=Domain.SAFETY,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Once a gNB is in iops_activated state, PDU sessions are\n"
            "  handled by the local EPC until backhaul returns (TS 23.401\n"
            "  §K.2.4). The local-session ledger must reject invalid\n"
            "  service_type (only voice/data/ptt/emergency are allowed in\n"
            "  the schema CHECK), accept valid PTT sessions, surface them\n"
            "  per-gNB, and honour /release.\n"
            "\n"
            "Procedure (TS 23.401 §K.2.4)\n"
            "  1. _setup_config() for the gNB.\n"
            "  2. POST /api/iops/local-sessions with service_type='invalid';\n"
            "     assert HTTP 400.\n"
            "  3. POST /local-sessions with service_type='ptt',\n"
            "     ip_address='10.99.1.5'; assert HTTP 200/201 with id.\n"
            "  4. GET /local-sessions?gnb_id=...; assert the new id is in\n"
            "     response.sessions.\n"
            "  5. POST /local-sessions/{sid}/release.\n"
            "  6. pass_test(session_id=sid).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hard-coded IMSI / IP).\n"
            "\n"
            "Pass criteria\n"
            "  Bad service_type 400 AND PTT session created AND surfaced in\n"
            "  per-gNB list (release is best-effort and not asserted).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  session_id.\n"
            "\n"
            "Known constraints\n"
            "  Does not assert /release returns 200; the release call is a\n"
            "  best-effort cleanup. Session ID is the SA-Core PK, not SUPI."
        ),
    )

    def run(self):
        try:
            _setup_config()

            # Validation: bad service_type must 400.
            br, bs = _iops_api("/api/iops/local-sessions", "POST", {
                "gnb_id": GNB, "imsi": "001011234567",
                "service_type": "invalid", "ip_address": "10.99.1.5",
            })
            if bs != 400:
                self.fail_test(f"bad service_type did not 400: {bs} {br}")
                return self.result

            r, s = _iops_api("/api/iops/local-sessions", "POST", {
                "gnb_id": GNB, "imsi": "001011234567",
                "service_type": "ptt", "ip_address": "10.99.1.5",
            })
            if s not in (200, 201) or not r.get("id"):
                self.fail_test(f"create session failed: {s} {r}")
                return self.result
            sid = r["id"]

            ls, _ = _iops_api(f"/api/iops/local-sessions?gnb_id={GNB}")
            if not any(x.get("id") == sid for x in ls.get("sessions", [])):
                self.fail_test("session not in list")
                return self.result

            _iops_api(f"/api/iops/local-sessions/{sid}/release", "POST")
            self.pass_test(session_id=sid)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class IopsEvents(TestCase):
    """TC-IOPS-005: Event log captures every state transition."""
    SPEC = TestSpec(
        tc_id="TC-IOPS-005",
        title="IOPS event log captures every state transition",
        spec="TS 23.401 §K.2.4",
        domain=Domain.SAFETY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Operators need a per-gNB audit trail of every IOPS state\n"
            "  transition for postmortem analysis. /api/iops/events is the\n"
            "  ledger; this TC pins that a full declare + restore cycle\n"
            "  appends all four transition events. TS 23.401 §K.2.4.\n"
            "\n"
            "Procedure (TS 23.401 §K.2.4)\n"
            "  1. _setup_config() for the gNB.\n"
            "  2. POST /api/iops/declare with reason=backhaul_failure to\n"
            "     drive backhaul_lost -> iops_activated transitions.\n"
            "  3. POST /api/iops/restore to drive restoring -> restored.\n"
            "  4. GET /api/iops/events?gnb_id=...&limit=10. Assert HTTP 200.\n"
            "  5. Extract event_type field from each event row.\n"
            "  6. For each of {backhaul_lost, iops_activated, restoring,\n"
            "     restored} assert the event_type is present in the list.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — fixed gNB id).\n"
            "\n"
            "Pass criteria\n"
            "  All four event_types (backhaul_lost, iops_activated,\n"
            "  restoring, restored) appear in /events within last 10 rows.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  events — the list of event_type values found.\n"
            "\n"
            "Known constraints\n"
            "  Event ordering not strictly asserted; only set membership.\n"
            "  Events table grows unbounded — no pruning in this build."
        ),
    )

    def run(self):
        try:
            _setup_config()
            _iops_api("/api/iops/declare", "POST",
                       {"gnb_id": GNB, "reason": "backhaul_failure"})
            _iops_api("/api/iops/restore", "POST", {"gnb_id": GNB})

            r, s = _iops_api(f"/api/iops/events?gnb_id={GNB}&limit=10")
            if s != 200:
                self.fail_test(f"GET events failed: {s} {r}")
                return self.result

            evt_types = [e.get("event_type") for e in r.get("events", [])]
            for needed in ("backhaul_lost", "iops_activated",
                           "restoring", "restored"):
                if needed not in evt_types:
                    self.fail_test(f"missing event '{needed}'", evt=evt_types)
                    return self.result
            self.pass_test(events=evt_types)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class IopsServiceCatalog(TestCase):
    """TC-IOPS-006: DefaultLocalServices catalog (TS 22.346 — TODO)."""
    SPEC = TestSpec(
        tc_id="TC-IOPS-006",
        title="IOPS default local-services catalog",
        spec="TS 22.346",
        domain=Domain.SAFETY,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("smoke",),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 22.346 §6 lists the minimum service set an IOPS-enabled\n"
            "  network must offer locally: emergency, push-to-talk (PTT),\n"
            "  voice, and data. The /api/iops/services catalog is the\n"
            "  programmatic surface other NFs query to scope local QoS\n"
            "  rules; this TC pins the four required names are present.\n"
            "\n"
            "Procedure (TS 22.346 §6)\n"
            "  1. GET /api/iops/services with no query params.\n"
            "  2. Assert HTTP 200.\n"
            "  3. Collect the set of name fields from response.services.\n"
            "  4. For each of {emergency, ptt, voice, data} assert the\n"
            "     service is present in the set.\n"
            "  5. fail_test on first missing service.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — pure GET).\n"
            "\n"
            "Pass criteria\n"
            "  All four required service names appear in /services.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  services — the list of service names found.\n"
            "\n"
            "Known constraints\n"
            "  Catalog content beyond names (QoS profile per service) is\n"
            "  not asserted — that lives in the IOPS QoS unit tests."
        ),
    )

    def run(self):
        try:
            r, s = _iops_api("/api/iops/services")
            if s != 200:
                self.fail_test(f"GET services failed: {s} {r}")
                return self.result
            names = {svc.get("name") for svc in r.get("services", [])}
            for required in ("emergency", "ptt", "voice", "data"):
                if required not in names:
                    self.fail_test(f"missing service '{required}': {names}")
                    return self.result
            self.pass_test(services=list(names))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_IOPS_TCS = [
    IopsConfigCRUD,
    IopsLifecycle,
    IopsCachedCredentials,
    IopsLocalSessions,
    IopsEvents,
    IopsServiceCatalog,
]

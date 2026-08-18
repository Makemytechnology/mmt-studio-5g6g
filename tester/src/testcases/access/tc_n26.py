# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: N26 — EPC ↔ 5GC inter-system handover panel.

TS 23.501 §5.17.2.2 — Interworking with N26 (mapped contexts).
TS 23.501 §5.17.2.2.2 — Mobility for single-registration UEs.
TS 23.502 §4.11 — System interworking procedures with EPC.

Drives the SA Core REST surface at /api/n26/* (audit log + handover
trigger + mapped-context cache). The actual NAS / S1 / NGAP signalling
is not exercised here — that lives in the GMM and EPC handlers.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src import delta

log = logging.getLogger("tester.tc_n26")


def _n26_api(path, method="GET", body=None):
    """Call SA Core N26 REST API."""
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


class N26Status(TestCase):
    SPEC = TestSpec(
        tc_id="TC-N26-001",
        title="GET /api/n26/status reports cache + audit shape",
        spec="TS 23.501 §5.17.2",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Bring-up probe for the N26 inter-system interworking surface\n"
            "  (TS 23.501 §5.18 / §5.17.2 — N26 enables mobility between EPC\n"
            "  MME and 5GC AMF via mapped UE contexts). Asserts the SA Core\n"
            "  exposes the N26 control surface with the right envelope keys\n"
            "  so every downstream handover/audit test in this file has a\n"
            "  consistent contract to lean on.\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + §5.17.2)\n"
            "  1. GET http://<core>:5000/api/n26/status (no body).\n"
            "  2. Assert HTTP 200; fail with status+body otherwise.\n"
            "  3. Walk the response dict and require five top-level keys:\n"
            "     n26_enabled, amf_ue_contexts, mme_ue_contexts,\n"
            "     context_ttl_seconds, audit. Missing any one fails.\n"
            "  4. Log full status envelope, pass_test with the response.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — pure read probe of the operator REST surface).\n"
            "\n"
            "Pass criteria\n"
            "  HTTP 200 AND all five envelope keys present. Any missing key\n"
            "  triggers fail_test(\"missing key '<k>' in status\").\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  status (full /api/n26/status envelope as nested dict).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — read-only against in-process SA Core simulator.\n"
            "  Hollow-pass shape: the test checks only key presence, not the\n"
            "  semantics of the counter values, so a core that always returns\n"
            "  zeros + n26_enabled=false would still pass."
        ),
    )

    def run(self):
        try:
            res, status = _n26_api("/api/n26/status")
            if status != 200:
                self.fail_test(f"GET /api/n26/status failed: {status} {res}")
                return self.result
            for k in ("n26_enabled", "amf_ue_contexts", "mme_ue_contexts",
                      "context_ttl_seconds", "audit"):
                if k not in res:
                    self.fail_test(f"missing key '{k}' in status", body=res)
                    return self.result
            log.info("N26 status: %s", res)
            self.pass_test(status=res)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class N26Handover5gTo4g(TestCase):
    SPEC = TestSpec(
        tc_id="TC-N26-002",
        title="5G→4G inter-system handover via N26 — audit + mapped context",
        spec="TS 23.501 §5.17.2.2.2",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.DELTA,
        setup_notes="Off-roster IMSI via src.delta.ue() (clones embb-bulk[0]).",
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the 5GS-to-EPS inter-system handover with N26 path\n"
            "  (TS 23.501 §5.18 / §5.17.2.2.2). The AMF must be able to\n"
            "  forge a mapped MM/SM context, hand it to the MME over N26,\n"
            "  and surface both the resulting mme_ue_s1ap_id and a\n"
            "  status=completed audit row for operator inspection.\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + §5.17.2.2.2)\n"
            "  1. Pick off-roster IMSI 001019999900201; delta.ue(imsi)\n"
            "     clones embb-bulk[0] (auth/AMBR/slice/DNN bindings) into\n"
            "     subscription DB and arranges teardown on exit.\n"
            "  2. POST /api/n26/handover/5g-to-4g with {imsi}. Require\n"
            "     HTTP 200/201, JSON success=True, source_rat=5G, target_rat=4G.\n"
            "  3. GET /api/n26/handovers/{imsi}; require non-empty audit list.\n"
            "  4. Assert audit[0].status == 'completed'.\n"
            "  5. Log audit_id + mme_ue_s1ap_id; pass_test with both.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSI is hardcoded for off-roster isolation).\n"
            "\n"
            "Pass criteria\n"
            "  HTTP 200/201 + success=True + RAT direction 5G->4G AND\n"
            "  audit[0].status == 'completed'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  audit_id, mme_ue_s1ap_id.\n"
            "\n"
            "Known constraints\n"
            "  Setup.DELTA — off-roster IMSI cloned then torn down; does not\n"
            "  perturb baseline UE roster. The actual S1 NAS / N26 signalling\n"
            "  is mocked inside SA Core; only the REST audit + mapped-context\n"
            "  bookkeeping is exercised here."
        ),
    )

    def run(self):
        # Off-roster IMSI provisioned via delta.ue() — clones the full
        # profile (auth + AMBR + slice + DNN + bindings) from
        # embb-bulk[0], removes on context exit. Pre-refactor used
        # _ensure_ue() which UPSERTed fixed K/OP via /api/ue/auth and
        # would clobber baseline credentials if the IMSI happened to
        # collide.
        imsi = "001019999900201"
        try:
            with delta.ue(imsi):
                res, status = _n26_api("/api/n26/handover/5g-to-4g", "POST",
                                        {"imsi": imsi})
                if status not in (200, 201):
                    self.fail_test(f"Handover failed: {status} {res}")
                    return self.result
                if not res.get("success"):
                    self.fail_test(f"Handover not success: {res}")
                    return self.result
                if res.get("source_rat") != "5G" or res.get("target_rat") != "4G":
                    self.fail_test("RAT direction mismatch", body=res)
                    return self.result

                # Audit row should appear in /handovers within seconds.
                audit, st_st = _n26_api(f"/api/n26/handovers/{imsi}")
                if st_st != 200 or not audit:
                    self.fail_test(f"Audit empty: {st_st} {audit}")
                    return self.result
                if audit[0].get("status") != "completed":
                    self.fail_test("First audit row not completed",
                                   row=audit[0])
                    return self.result

                log.info("Handover 5G→4G recorded id=%s mme_ue_s1ap_id=%s",
                         res.get("audit_id"), res.get("mme_ue_s1ap_id"))
                self.pass_test(audit_id=res.get("audit_id"),
                               mme_ue_s1ap_id=res.get("mme_ue_s1ap_id"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class N26Handover4gTo5g(TestCase):
    SPEC = TestSpec(
        tc_id="TC-N26-003",
        title="4G→5G inter-system handover via N26 — audit + AMF UE id",
        spec="TS 23.501 §5.17.2.2.2",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.DELTA,
        setup_notes="Off-roster IMSI via src.delta.ue() (clones embb-bulk[0]).",
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Mirror of TC-N26-002 in the reverse direction — pins the\n"
            "  EPS-to-5GS inter-system handover via N26 (TS 23.501 §5.18 /\n"
            "  §5.17.2.2.2). MME forwards the mapped UE context to AMF,\n"
            "  AMF allocates an amf_ue_ngap_id and writes a completed\n"
            "  audit row for the inbound side of inter-system mobility.\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + §5.17.2.2.2)\n"
            "  1. Off-roster IMSI 001019999900202 via delta.ue() (clones\n"
            "     embb-bulk[0] template, auto-removed on exit).\n"
            "  2. POST /api/n26/handover/4g-to-5g with {imsi}. Require\n"
            "     HTTP 200/201, success=True, source_rat=4G, target_rat=5G.\n"
            "  3. GET /api/n26/handovers/{imsi}; require non-empty list.\n"
            "  4. Assert audit[0].status == 'completed'.\n"
            "  5. Log audit_id + amf_ue_ngap_id; pass_test with both.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hardcoded off-roster IMSI for isolation).\n"
            "\n"
            "Pass criteria\n"
            "  HTTP 200/201 + success=True + RAT direction 4G->5G AND\n"
            "  audit[0].status == 'completed'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  audit_id, amf_ue_ngap_id.\n"
            "\n"
            "Known constraints\n"
            "  Setup.DELTA — cloned IMSI cleaned up on context exit. Real\n"
            "  NGAP/S1 wire path is not exercised here; the test only pins\n"
            "  the operator REST surface (audit + mapped-context cache)."
        ),
    )

    def run(self):
        imsi = "001019999900202"
        try:
            with delta.ue(imsi):
                res, status = _n26_api("/api/n26/handover/4g-to-5g", "POST",
                                        {"imsi": imsi})
                if status not in (200, 201):
                    self.fail_test(f"Handover failed: {status} {res}")
                    return self.result
                if not res.get("success"):
                    self.fail_test(f"Handover not success: {res}")
                    return self.result
                if res.get("source_rat") != "4G" or res.get("target_rat") != "5G":
                    self.fail_test("RAT direction mismatch", body=res)
                    return self.result

                audit, st_st = _n26_api(f"/api/n26/handovers/{imsi}")
                if st_st != 200 or not audit:
                    self.fail_test(f"Audit empty: {st_st} {audit}")
                    return self.result
                if audit[0].get("status") != "completed":
                    self.fail_test("First audit row not completed",
                                   row=audit[0])
                    return self.result

                log.info("Handover 4G→5G recorded id=%s amf_ue_ngap_id=%s",
                         res.get("audit_id"), res.get("amf_ue_ngap_id"))
                self.pass_test(audit_id=res.get("audit_id"),
                               amf_ue_ngap_id=res.get("amf_ue_ngap_id"))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class N26HandoverUnknownUE(TestCase):
    SPEC = TestSpec(
        tc_id="TC-N26-004",
        title="N26 handover for unknown IMSI returns 404 (no audit row)",
        spec="TS 23.501 §5.17.2.2",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Negative-path conformance for N26 handover (TS 23.501 §5.18 /\n"
            "  §5.17.2.2). An IMSI absent from the AMF subscription DB must\n"
            "  not yield a mapped context or audit row — otherwise the\n"
            "  inter-system audit log becomes a write surface for stranger\n"
            "  IMSIs (operator/PCI risk).\n"
            "\n"
            "Procedure (TS 23.501 §5.18 + §5.17.2.2)\n"
            "  1. Use IMSI 001019999999999 (not provisioned, not seeded\n"
            "     in any baseline roster, no delta.ue() context wrapping).\n"
            "  2. POST /api/n26/handover/5g-to-4g with {imsi}.\n"
            "  3. Assert HTTP status == 404 exactly. Any other status\n"
            "     (200/201/4xx-not-404/5xx) fails the test.\n"
            "  4. Log the rejection; pass_test with status + body.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hardcoded unprovisioned IMSI).\n"
            "\n"
            "Pass criteria\n"
            "  HTTP status == 404. No audit row, no mapped context expected.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  status (int), body (response payload).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — no delta provisioning needed (IMSI is meant\n"
            "  to be absent). Hollow-pass shape: the test does not verify\n"
            "  /api/n26/handovers stays empty for this IMSI; it only checks\n"
            "  the response code, so a server that 404s but still writes an\n"
            "  audit row would still pass this check."
        ),
    )

    def run(self):
        imsi = "001019999999999"
        try:
            res, status = _n26_api("/api/n26/handover/5g-to-4g", "POST",
                                    {"imsi": imsi})
            if status != 404:
                self.fail_test(f"Unknown IMSI did not 404: {status} {res}")
                return self.result
            log.info("Unknown IMSI correctly rejected: %s", res)
            self.pass_test(status=status, body=res)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class N26HandoverHistory(TestCase):
    SPEC = TestSpec(
        tc_id="TC-N26-005",
        title="N26 audit log contains both 5G→4G and 4G→5G handover rows",
        spec="TS 23.501 §5.17.5.2.1",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.DELTA,
        setup_notes="Two off-roster IMSIs via src.delta.ues().",
        expected_duration_s=6.0,
        description=(
            "Purpose\n"
            "  Pins the consolidated N26 audit log (TS 23.501 §5.17.5.2.1 —\n"
            "  AMF management of UE context, §5.18 — N26 interworking).\n"
            "  Operators rely on /api/n26/handovers as a single chronological\n"
            "  ledger of cross-RAT moves for billing/forensics; this test\n"
            "  proves both directions land in the same ledger.\n"
            "\n"
            "Procedure (TS 23.501 §5.17.5.2.1 + §5.18)\n"
            "  1. delta.ues(imsi_a, imsi_b) clones two off-roster IMSIs\n"
            "     (001019999900203, 001019999900204) into subscription DB.\n"
            "  2. POST /api/n26/handover/5g-to-4g for imsi_a (response\n"
            "     ignored — only the audit side-effect matters).\n"
            "  3. POST /api/n26/handover/4g-to-5g for imsi_b.\n"
            "  4. GET /api/n26/handovers (full ledger). Require HTTP 200.\n"
            "  5. Build set of (imsi, source_rat, target_rat) tuples.\n"
            "  6. Assert (imsi_a, '5G', '4G') AND (imsi_b, '4G', '5G')\n"
            "     both present.\n"
            "  7. Log total row count; pass_test(total=len(res)).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — hardcoded IMSIs).\n"
            "\n"
            "Pass criteria\n"
            "  Both (imsi_a,5G,4G) and (imsi_b,4G,5G) tuples appear in the\n"
            "  /api/n26/handovers response set.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  total (int — row count of /api/n26/handovers at assertion time).\n"
            "\n"
            "Known constraints\n"
            "  Setup.DELTA — both IMSIs torn down on context exit. The test\n"
            "  does not assert audit ordering or timestamp monotonicity; only\n"
            "  set membership."
        ),
    )

    def run(self):
        imsi_a = "001019999900203"
        imsi_b = "001019999900204"
        try:
            with delta.ues(imsi_a, imsi_b):
                _n26_api("/api/n26/handover/5g-to-4g", "POST", {"imsi": imsi_a})
                _n26_api("/api/n26/handover/4g-to-5g", "POST", {"imsi": imsi_b})

                res, status = _n26_api("/api/n26/handovers")
                if status != 200:
                    self.fail_test(f"GET /handovers failed: {status} {res}")
                    return self.result
                seen = {(r.get("imsi"), r.get("source_rat"), r.get("target_rat"))
                        for r in res}
                if (imsi_a, "5G", "4G") not in seen:
                    self.fail_test(f"5G→4G entry missing for {imsi_a}",
                                   sample=res[:3])
                    return self.result
                if (imsi_b, "4G", "5G") not in seen:
                    self.fail_test(f"4G→5G entry missing for {imsi_b}",
                                   sample=res[:3])
                    return self.result

                log.info("History contains both handovers (%d total rows)",
                         len(res))
                self.pass_test(total=len(res))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_N26_TCS = [
    N26Status,
    N26Handover5gTo4g,
    N26Handover4gTo5g,
    N26HandoverUnknownUE,
    N26HandoverHistory,
]

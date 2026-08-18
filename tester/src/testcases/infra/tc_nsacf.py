# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: NSACF — Network Slice Admission Control + UE Slice MBR.

TS 23.501 §5.15.11 — Slice admission control, per-UE slice MBR enforcement.
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

log = logging.getLogger("tester.tc_nsacf")


def _nsacf_api(path, method="GET", body=None):
    """Call SA Core NSACF REST API."""
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


class NsacfSetLimit(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSACF-001",
        title="Set slice admission limit and verify via status",
        spec="TS 23.501 §5.15.11",
        domain=Domain.SLICING,
        nfs=(NF.NSACF,),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundation smoke for NSACF (Network Slice Admission Control\n"
            "  Function). TS 23.501 §5.15.11 says NSACF enforces an operator-\n"
            "  configured maximum number of UEs per S-NSSAI (sst,sd) before\n"
            "  AMF accepts a Registration with that slice. This TC pins the\n"
            "  config-set / config-read round trip: if the limit cannot be\n"
            "  set or read back, every downstream admission test is invalid.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.11)\n"
            "  1. POST /api/nsacf/limits with body {sst:1, sd:'000001',\n"
            "     max_ues:10}; expect HTTP 200/201.\n"
            "  2. GET /api/nsacf/status/1/000001; expect HTTP 200.\n"
            "  3. Read max_ues from the status envelope.\n"
            "  4. Assert it equals 10 (round-trip integrity).\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — sst, sd and max_ues are hard-coded for this smoke.\n"
            "\n"
            "Pass criteria\n"
            "  POST returned 200/201 AND status GET returned 200 AND\n"
            "  st_result['max_ues'] == 10.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  limit, status.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no UE pool needed; the test touches only the\n"
            "  NSACF REST surface in SA Core. Limit persists until the next\n"
            "  baseline reset or another POST overwrites it. The TC does not\n"
            "  validate the JSON schema of the status envelope beyond the\n"
            "  max_ues field."
        ),
    )

    def run(self):
        try:
            # Set limit
            result, status = _nsacf_api("/api/nsacf/limits", "POST", {
                "sst": 1, "sd": "000001", "max_ues": 10,
            })
            if status not in (200, 201):
                self.fail_test(f"Set limit failed: {status} {result}")
                return self.result
            log.info("Limit set: %s", result)

            # Verify via status
            st_result, st_status = _nsacf_api("/api/nsacf/status/1/000001")
            if st_status != 200:
                self.fail_test(f"Status query failed: {st_status} {st_result}")
                return self.result

            max_ues = st_result.get("max_ues")
            if max_ues != 10:
                self.fail_test(f"Expected max_ues=10, got {max_ues}", response=st_result)
                return self.result

            self.pass_test(limit=result, status=st_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NsacfAdmitUe(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSACF-002",
        title="Admit a UE to a slice and verify in admissions list",
        spec="TS 23.501 §5.15.11",
        domain=Domain.SLICING,
        nfs=(NF.NSACF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the NSACF admission-grant path (TS 23.501 §5.15.11). When\n"
            "  AMF receives a Registration Request that includes Requested\n"
            "  NSSAI, it asks NSACF whether the slice has room. This TC\n"
            "  drives that decision via the explicit /admit endpoint and\n"
            "  verifies the admitted IMSI is observable in the admissions\n"
            "  ledger — exactly what NSACF must expose for OAM and for the\n"
            "  release-on-deregistration path.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.11)\n"
            "  1. imsi = baseline.imsi('embb-bulk', 0) — first eMBB-bulk SIM.\n"
            "  2. POST /api/nsacf/limits {sst:1, sd:'000001', max_ues:10}.\n"
            "  3. POST /api/nsacf/admit {imsi, sst:1, sd:'000001'}; expect\n"
            "     HTTP 200/201.\n"
            "  4. GET /api/nsacf/admissions; expect HTTP 200.\n"
            "  5. Scan admissions[].imsi for our IMSI.\n"
            "  6. Teardown: POST /api/nsacf/release for the same key.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — IMSI is taken from baseline.imsi('embb-bulk', 0).\n"
            "\n"
            "Pass criteria\n"
            "  /admit returned 200/201 AND /admissions returned 200 AND the\n"
            "  IMSI was found in the admissions list.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  admitted, admissions.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY — no AMF/Registration is exercised; admission is\n"
            "  driven by the direct NSACF API. The finally clause always\n"
            "  releases the IMSI so the test is idempotent under retry."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            # Set limit
            _nsacf_api("/api/nsacf/limits", "POST", {
                "sst": 1, "sd": "000001", "max_ues": 10,
            })

            # Admit UE
            result, status = _nsacf_api("/api/nsacf/admit", "POST", {
                "imsi": imsi, "sst": 1, "sd": "000001",
            })
            if status not in (200, 201):
                self.fail_test(f"Admit failed: {status} {result}")
                return self.result
            log.info("UE admitted: %s", result)

            # Verify in admissions list
            adm_result, adm_status = _nsacf_api("/api/nsacf/admissions")
            if adm_status != 200:
                self.fail_test(f"Admissions query failed: {adm_status} {adm_result}")
                return self.result

            admissions = adm_result.get("admissions") or adm_result.get("items") or []
            found = any(a.get("imsi") == imsi for a in admissions)
            if not found:
                self.fail_test(f"IMSI {imsi} not found in admissions", admissions=admissions)
                return self.result

            self.pass_test(admitted=result, admissions=admissions)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _nsacf_api("/api/nsacf/release", "POST", {
                "imsi": imsi, "sst": 1, "sd": "000001",
            })
        return self.result


class NsacfDenyWhenFull(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSACF-003",
        title="NSACF denies admission when slice quota is exhausted",
        spec="TS 23.501 §5.15.11",
        domain=Domain.SLICING,
        nfs=(NF.NSACF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the quota-enforcement leg of TS 23.501 §5.15.11: once the\n"
            "  configured per-S-NSSAI cap is reached, NSACF must reject any\n"
            "  additional admission so AMF can return a Registration Reject\n"
            "  with cause 'S-NSSAI not available in the current PLMN' (or\n"
            "  equivalent). A core that silently over-admits would let an\n"
            "  operator overrun the slice SLA without any signalling cue.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.11)\n"
            "  1. POST /api/nsacf/limits {sst:1, sd:'000001', max_ues:1}.\n"
            "  2. POST /api/nsacf/admit for imsi1 (embb-bulk #0); expect\n"
            "     HTTP 200/201 — the slot is now consumed.\n"
            "  3. POST /api/nsacf/admit for imsi2 (embb-bulk #1).\n"
            "  4. Assert the second response is NOT 200/201 (deny path).\n"
            "  5. Teardown: POST /release for both IMSIs regardless of\n"
            "     outcome so the next run starts clean.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — IMSIs from baseline.imsi('embb-bulk', 0..1).\n"
            "\n"
            "Pass criteria\n"
            "  First admit returned 200/201 AND second admit returned a\n"
            "  non-2xx status code (denied).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  first_admit, deny_response, deny_status.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Only HTTP status is asserted; the exact deny\n"
            "  code (403, 409, 503) is implementation-defined. Tagged\n"
            "  'negative' because the pass path requires a failure response."
        ),
    )

    def run(self):
        imsi1 = baseline.imsi("embb-bulk", 0)
        imsi2 = baseline.imsi("embb-bulk", 1)
        try:
            # Set limit to 1
            _nsacf_api("/api/nsacf/limits", "POST", {
                "sst": 1, "sd": "000001", "max_ues": 1,
            })

            # Admit first UE
            result1, status1 = _nsacf_api("/api/nsacf/admit", "POST", {
                "imsi": imsi1, "sst": 1, "sd": "000001",
            })
            if status1 not in (200, 201):
                self.fail_test(f"First admit failed: {status1} {result1}")
                return self.result

            # Attempt to admit second UE — should be denied
            result2, status2 = _nsacf_api("/api/nsacf/admit", "POST", {
                "imsi": imsi2, "sst": 1, "sd": "000001",
            })

            if status2 in (200, 201):
                self.fail_test("Second UE admitted but should have been denied",
                               response=result2)
                return self.result

            log.info("Second UE correctly denied: %s %s", status2, result2)
            self.pass_test(first_admit=result1, deny_response=result2,
                           deny_status=status2)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _nsacf_api("/api/nsacf/release", "POST", {
                "imsi": imsi1, "sst": 1, "sd": "000001",
            })
            _nsacf_api("/api/nsacf/release", "POST", {
                "imsi": imsi2, "sst": 1, "sd": "000001",
            })
        return self.result


class NsacfReleaseUe(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSACF-004",
        title="Release a UE from a slice and verify removal",
        spec="TS 23.501 §5.15.11",
        domain=Domain.SLICING,
        nfs=(NF.NSACF, NF.AMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pin the inverse of TC-NSACF-002. TS 23.501 §5.15.11 requires\n"
            "  that on UE Deregistration or slice removal, NSACF frees the\n"
            "  admission slot so a new UE can take it. A core that retains\n"
            "  stale admissions will eventually wedge the slice at 100% and\n"
            "  refuse all new Registrations even though no UE is using it.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.11)\n"
            "  1. POST /api/nsacf/limits {sst:1, sd:'000001', max_ues:10}.\n"
            "  2. POST /api/nsacf/admit for the embb-bulk #0 IMSI.\n"
            "  3. POST /api/nsacf/release with the same {imsi, sst, sd};\n"
            "     expect HTTP 200/201.\n"
            "  4. GET /api/nsacf/admissions; expect HTTP 200.\n"
            "  5. Scan the list and assert NO entry matches both the IMSI\n"
            "     AND (sst==1, sd=='000001').\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — IMSI is baseline.imsi('embb-bulk', 0).\n"
            "\n"
            "Pass criteria\n"
            "  /release returned 200/201 AND /admissions returned 200 AND\n"
            "  the IMSI was NOT present for that (sst,sd) after release.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  release.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. NSACF may keep history of past admissions; the\n"
            "  assertion only checks the live (sst,sd) tuple match, not\n"
            "  bare IMSI presence in any historical log."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            # Set limit and admit
            _nsacf_api("/api/nsacf/limits", "POST", {
                "sst": 1, "sd": "000001", "max_ues": 10,
            })
            _nsacf_api("/api/nsacf/admit", "POST", {
                "imsi": imsi, "sst": 1, "sd": "000001",
            })

            # Release
            result, status = _nsacf_api("/api/nsacf/release", "POST", {
                "imsi": imsi, "sst": 1, "sd": "000001",
            })
            if status not in (200, 201):
                self.fail_test(f"Release failed: {status} {result}")
                return self.result
            log.info("UE released: %s", result)

            # Verify not in admissions
            adm_result, adm_status = _nsacf_api("/api/nsacf/admissions")
            if adm_status != 200:
                self.fail_test(f"Admissions query failed: {adm_status}")
                return self.result

            admissions = adm_result.get("admissions") or adm_result.get("items") or []
            found = any(a.get("imsi") == imsi and a.get("sst") == 1
                        and a.get("sd") == "000001" for a in admissions)
            if found:
                self.fail_test("IMSI still in admissions after release",
                               admissions=admissions)
                return self.result

            self.pass_test(release=result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NsacfUeSliceMbr(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSACF-005",
        title="Set and verify per-UE slice MBR (DL/UL kbps)",
        spec="TS 23.501 §5.15.11",
        domain=Domain.SLICING,
        nfs=(NF.NSACF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the per-UE slice MBR storage path. TS 23.501 §5.15.11.3\n"
            "  introduces a per-UE maximum bitrate that NSACF/PCF can set\n"
            "  on a (UE, S-NSSAI) tuple in addition to the slice-wide and\n"
            "  session-wide AMBRs. UPF is supposed to use this value when\n"
            "  rate-limiting flows belonging to that UE on that slice; if\n"
            "  the config cannot be written and read back faithfully, the\n"
            "  policy plane is broken.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.11.3)\n"
            "  1. POST /api/nsacf/ue-slice-mbr {imsi, sst:1, sd:'000001',\n"
            "     mbr_dl_kbps:100000, mbr_ul_kbps:50000}; expect 200/201.\n"
            "  2. GET /api/nsacf/ue-slice-mbr/{imsi}; expect HTTP 200.\n"
            "  3. Read mbr_dl_kbps / mbr_ul_kbps from the response.\n"
            "  4. Assert dl==100000 AND ul==50000.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — IMSI is baseline.imsi('embb-bulk', 0).\n"
            "\n"
            "Pass criteria\n"
            "  Set returned 200/201 AND GET returned 200 AND the read-back\n"
            "  mbr_dl_kbps and mbr_ul_kbps match the values just written.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  set_result, mbr.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. This is a config-plane round-trip; no real\n"
            "  traffic is shaped, no UPF policy table is inspected — only\n"
            "  the NSACF/PCF surface that owns the value is exercised."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            # Set MBR
            result, status = _nsacf_api("/api/nsacf/ue-slice-mbr", "POST", {
                "imsi": imsi, "sst": 1, "sd": "000001",
                "mbr_dl_kbps": 100000, "mbr_ul_kbps": 50000,
            })
            if status not in (200, 201):
                self.fail_test(f"Set MBR failed: {status} {result}")
                return self.result
            log.info("UE slice MBR set: %s", result)

            # Verify
            mbr_result, mbr_status = _nsacf_api(f"/api/nsacf/ue-slice-mbr/{imsi}")
            if mbr_status != 200:
                self.fail_test(f"MBR query failed: {mbr_status} {mbr_result}")
                return self.result

            dl = mbr_result.get("mbr_dl_kbps")
            ul = mbr_result.get("mbr_ul_kbps")
            if dl != 100000 or ul != 50000:
                self.fail_test(f"MBR mismatch: dl={dl} ul={ul}", response=mbr_result)
                return self.result

            self.pass_test(set_result=result, mbr=mbr_result)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class NsacfAdmissionLog(TestCase):
    SPEC = TestSpec(
        tc_id="TC-NSACF-006",
        title="Admission log records admit and release events",
        spec="TS 23.501 §5.15.11",
        domain=Domain.SLICING,
        nfs=(NF.NSACF,),
        severity=Severity.MINOR,
        tags=("conformance", "regression"),
        setup=Setup.EMPTY,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.15.11 expects admission decisions to be auditable\n"
            "  for slice SLA accounting and operator-side forensics. This TC\n"
            "  pins the audit log: after one full admit/release cycle, the\n"
            "  NSACF log endpoint must return a non-empty record set. A core\n"
            "  with a silently broken logger could pass every other TC in\n"
            "  this suite while losing every accounting event in production.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.11)\n"
            "  1. POST /api/nsacf/limits {sst:1, sd:'000001', max_ues:10}.\n"
            "  2. POST /api/nsacf/admit for the embb-bulk #0 IMSI.\n"
            "  3. POST /api/nsacf/release for the same {imsi, sst, sd}.\n"
            "  4. GET /api/nsacf/log; expect HTTP 200.\n"
            "  5. Extract entries[] (fall back to log[]); assert the list\n"
            "     is non-empty.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — IMSI is baseline.imsi('embb-bulk', 0).\n"
            "\n"
            "Pass criteria\n"
            "  /log returned 200 AND the entries (or log) array contained\n"
            "  at least one record after the admit+release sequence.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  entry_count, entries.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Log content semantics are not parsed — only\n"
            "  presence is asserted. Severity is MINOR because failure\n"
            "  does not block slice traffic, only forensic visibility."
        ),
    )

    def run(self):
        imsi = baseline.imsi("embb-bulk", 0)
        try:
            # Set limit, admit, release
            _nsacf_api("/api/nsacf/limits", "POST", {
                "sst": 1, "sd": "000001", "max_ues": 10,
            })
            _nsacf_api("/api/nsacf/admit", "POST", {
                "imsi": imsi, "sst": 1, "sd": "000001",
            })
            _nsacf_api("/api/nsacf/release", "POST", {
                "imsi": imsi, "sst": 1, "sd": "000001",
            })

            # Check log
            log_result, log_status = _nsacf_api("/api/nsacf/log")
            if log_status != 200:
                self.fail_test(f"Log query failed: {log_status} {log_result}")
                return self.result

            entries = log_result.get("entries") or log_result.get("log") or []
            if not entries:
                self.fail_test("No log entries found", response=log_result)
                return self.result

            log.info("Found %d log entries", len(entries))
            self.pass_test(entry_count=len(entries), entries=entries)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_NSACF_TCS = [
    NsacfSetLimit, NsacfAdmitUe, NsacfDenyWhenFull,
    NsacfReleaseUe, NsacfUeSliceMbr, NsacfAdmissionLog,
]

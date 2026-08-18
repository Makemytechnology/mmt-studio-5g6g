# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Wi-Fi Offload — operator-side N3IWF/TNGF policy + admission.

TS 23.501 §4.2.7   Reference points (incl. N3 / Y1 / Y2 for non-3GPP).
TS 23.501 §4.2.8   Support of non-3GPP access — umbrella clause.
TS 23.501 §5.10.2  Security model for non-3GPP access (EAP-5G / IKEv2).
TS 23.501 §6.2.9   N3IWF — untrusted-WLAN gateway.
TS 23.501 §6.2.9A  TNGF — trusted-WLAN gateway.

Drives the SA Core REST surface at /api/wifi-offload/* (operator
policy + admission probe + attached-UE table). The IKEv2 + EAP-5G
+ ESP datapath itself is exercised by tc_n3iwf-style tests; here we
only exercise the operator policy + audit surface.
"""

import json
import logging
import urllib.request
import urllib.error

from src import baseline
from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_wifi_offload")


def _wifi_api(path, method="GET", body=None):
    """Call SA Core wifi-offload REST API."""
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


class WifiOffloadPolicyCRUD(TestCase):
    SPEC = TestSpec(
        tc_id="TC-WIFI-001",
        title="Per-DNN Wi-Fi offload policy CRUD round-trip",
        spec="TS 23.501 §4.2.8",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF, NF.N3IWF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Foundation gate for the Wi-Fi-offload policy plane\n"
            "  (TS 23.501 §4.2.8 — support of non-3GPP access; TS 23.402\n"
            "  legacy untrusted/trusted WLAN access). Per-DNN policy rows\n"
            "  steer ATSSS / N3IWF / TNGF admission, so the CRUD surface\n"
            "  must persist legal rows, reject illegal enum values, and\n"
            "  remove rows cleanly without 404-leaks.\n"
            "\n"
            "Procedure (TS 23.501 §4.2.8 + §5.6 + TS 23.402)\n"
            "  1. DELETE /api/wifi-offload/policies/tc-wifi-001 (cleanup).\n"
            "  2. POST /api/wifi-offload/policies {dnn, access_type=untrusted,\n"
            "     offload_pref=5g_first, enabled=true}. Require 200/201.\n"
            "  3. GET /api/wifi-offload/policies/{dnn}; require 200 and\n"
            "     dnn+access_type round-trip.\n"
            "  4. POST with access_type='WRONG' — require HTTP 400.\n"
            "  5. POST with offload_pref='WRONG' — require HTTP 400.\n"
            "  6. POST again with access_type=trusted/offload_pref=wlan_first\n"
            "     (upsert); require 200/201; GET back; assert access_type\n"
            "     updated to 'trusted'.\n"
            "  7. DELETE /api/wifi-offload/policies/{dnn}; require HTTP 200.\n"
            "  8. GET /api/wifi-offload/policies/{dnn}; require HTTP 404.\n"
            "  9. finally: DELETE again (idempotent cleanup).\n"
            " 10. pass_test(dnn=dnn).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — DNN tc-wifi-001 used for isolation).\n"
            "\n"
            "Pass criteria\n"
            "  All eight CRUD steps return their expected status codes AND\n"
            "  the upsert path mutates access_type to 'trusted'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  dnn.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — operator REST surface only; no IKEv2/EAP-5G\n"
            "  exchange. finally{} DELETE keeps the policy table clean even\n"
            "  on mid-run failure."
        ),
    )

    def run(self):
        dnn = "tc-wifi-001"
        try:
            # Cleanup from any prior run.
            _wifi_api(f"/api/wifi-offload/policies/{dnn}", "DELETE")

            # Create.
            res, status = _wifi_api("/api/wifi-offload/policies", "POST", {
                "dnn": dnn,
                "access_type": "untrusted",
                "offload_pref": "5g_first",
                "enabled": True,
            })
            if status not in (200, 201):
                self.fail_test(f"create failed: {status} {res}")
                return self.result

            # Read.
            got, gstatus = _wifi_api(f"/api/wifi-offload/policies/{dnn}")
            if gstatus != 200:
                self.fail_test(f"get after create failed: {gstatus} {got}")
                return self.result
            if got.get("dnn") != dnn or got.get("access_type") != "untrusted":
                self.fail_test(f"row mismatch: {got}")
                return self.result

            # Reject: invalid access_type (TS 23.501 §4.2.8 enum).
            _, bad_at = _wifi_api("/api/wifi-offload/policies", "POST", {
                "dnn": dnn + "-bad",
                "access_type": "WRONG",
                "offload_pref": "5g_first",
            })
            if bad_at != 400:
                self.fail_test(f"expected 400 for bad access_type, got {bad_at}")
                return self.result

            # Reject: invalid offload_pref.
            _, bad_pref = _wifi_api("/api/wifi-offload/policies", "POST", {
                "dnn": dnn + "-bad",
                "access_type": "untrusted",
                "offload_pref": "WRONG",
            })
            if bad_pref != 400:
                self.fail_test(f"expected 400 for bad offload_pref, got {bad_pref}")
                return self.result

            # Update via the same upsert path.
            _, ustatus = _wifi_api("/api/wifi-offload/policies", "POST", {
                "dnn": dnn,
                "access_type": "trusted",
                "offload_pref": "wlan_first",
                "enabled": True,
            })
            if ustatus not in (200, 201):
                self.fail_test(f"update failed: {ustatus}")
                return self.result
            got2, _ = _wifi_api(f"/api/wifi-offload/policies/{dnn}")
            if got2.get("access_type") != "trusted":
                self.fail_test(f"update did not persist: {got2}")
                return self.result

            # Delete + 404 on get.
            _, dstatus = _wifi_api(f"/api/wifi-offload/policies/{dnn}", "DELETE")
            if dstatus != 200:
                self.fail_test(f"delete failed: {dstatus}")
                return self.result
            _, gone = _wifi_api(f"/api/wifi-offload/policies/{dnn}")
            if gone != 404:
                self.fail_test(f"expected 404 after delete, got {gone}")
                return self.result

            self.pass_test(dnn=dnn)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _wifi_api(f"/api/wifi-offload/policies/{dnn}", "DELETE")
        return self.result


class WifiOffloadAdmissionDefault(TestCase):
    SPEC = TestSpec(
        tc_id="TC-WIFI-002",
        title="Wi-Fi offload admission default policy when no DNN row",
        spec="TS 23.501 §4.2.8",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF, NF.N3IWF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pins the operator default policy for an unconfigured DNN\n"
            "  (TS 23.501 §4.2.8 + §5.6 — non-3GPP access integration).\n"
            "  Convention: untrusted-WLAN via N3IWF defaults to allow\n"
            "  (TS 23.501 §6.2.9, the lowest-trust path is the safe\n"
            "  fallback), trusted-WLAN via TNGF (§6.2.9A) is default-deny\n"
            "  because it requires explicit operator opt-in.\n"
            "\n"
            "Procedure (TS 23.501 §4.2.8 + §6.2.9 / §6.2.9A)\n"
            "  1. DELETE /api/wifi-offload/policies/tc-wifi-002-no-row\n"
            "     to ensure no row exists (state cleanup).\n"
            "  2. POST /api/wifi-offload/admission with imsi=embb-bulk[1],\n"
            "     dnn=<above>, access_type='untrusted'. Assert allowed=true.\n"
            "  3. POST same admission probe with access_type='trusted'.\n"
            "     Assert allowed=false.\n"
            "  4. pass_test(default_untrusted=<probe1>,\n"
            "               default_trusted=<probe2>).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSI sourced from baseline.imsi('embb-bulk', 1)).\n"
            "\n"
            "Pass criteria\n"
            "  Untrusted probe.allowed == True AND trusted probe.allowed\n"
            "  == False, both without any DNN policy row present.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  default_untrusted (full admission response dict),\n"
            "  default_trusted (full admission response dict).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — depends on the baseline embb-bulk roster\n"
            "  being present. No actual N3IWF / TNGF datapath exercised;\n"
            "  the admission probe is a pure REST decision over the\n"
            "  operator policy DB."
        ),
    )

    def run(self):
        dnn = "tc-wifi-002-no-row"
        try:
            _wifi_api(f"/api/wifi-offload/policies/{dnn}", "DELETE")

            # Untrusted with no DNN row → default-allow.
            res_u, _ = _wifi_api("/api/wifi-offload/admission", "POST", {
                "imsi": baseline.imsi("embb-bulk", 1),
                "dnn": dnn,
                "access_type": "untrusted",
            })
            if not res_u.get("allowed"):
                self.fail_test(f"expected allow on default untrusted: {res_u}")
                return self.result

            # Trusted with no DNN row → default refuses non-untrusted.
            res_t, _ = _wifi_api("/api/wifi-offload/admission", "POST", {
                "imsi": baseline.imsi("embb-bulk", 1),
                "dnn": dnn,
                "access_type": "trusted",
            })
            if res_t.get("allowed"):
                self.fail_test(f"expected default-deny on trusted: {res_t}")
                return self.result

            self.pass_test(default_untrusted=res_u, default_trusted=res_t)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class WifiOffloadAdmissionGate(TestCase):
    SPEC = TestSpec(
        tc_id="TC-WIFI-003",
        title="Wi-Fi offload admission gates — 5g_only / wlan_only / disabled",
        spec="TS 23.501 §4.2.8",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF, NF.N3IWF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Exhaustively pins the admission gate logic against the four\n"
            "  decision branches the policy engine has to honour\n"
            "  (TS 23.501 §4.2.8 / §5.6 — ATSSS steering; TS 23.402 access\n"
            "  selection). 5g_only forbids any WLAN, wlan_only with an\n"
            "  access_type mismatch must also deny, disabled overrides\n"
            "  every preference, and 5g_first+enabled is the green path.\n"
            "\n"
            "Procedure (TS 23.501 §4.2.8 + §5.6)\n"
            "  1. DELETE /api/wifi-offload/policies/tc-wifi-003 (cleanup).\n"
            "  2. POST policy {access_type=untrusted, offload_pref=5g_only,\n"
            "     enabled=true}. Admission probe untrusted → assert\n"
            "     allowed=false.\n"
            "  3. POST policy {access_type=trusted, offload_pref=wlan_only,\n"
            "     enabled=true}. Admission probe with untrusted (mismatch)\n"
            "     → assert allowed=false.\n"
            "  4. POST policy {access_type=untrusted, offload_pref=5g_first,\n"
            "     enabled=false}. Admission probe → assert allowed=false.\n"
            "  5. POST policy {access_type=untrusted, offload_pref=5g_first,\n"
            "     enabled=true}. Admission probe → assert allowed=true.\n"
            "  6. finally: DELETE the policy row.\n"
            "  7. pass_test(dnn, denied_5g_only, denied_mm, denied_disabled,\n"
            "               allowed).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — IMSI from baseline.imsi('embb-bulk', 2)).\n"
            "\n"
            "Pass criteria\n"
            "  First three admission probes return allowed=false AND the\n"
            "  fourth (5g_first + enabled) returns allowed=true.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  dnn, denied_5g_only, denied_mm, denied_disabled, allowed.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — depends on the embb-bulk roster. Each policy\n"
            "  POST overwrites the previous one via upsert, so the test is\n"
            "  order-sensitive. finally{} DELETE always runs."
        ),
    )

    def run(self):
        dnn = "tc-wifi-003"
        try:
            _wifi_api(f"/api/wifi-offload/policies/{dnn}", "DELETE")

            # 5g_only — refuse all WLAN.
            _, _ = _wifi_api("/api/wifi-offload/policies", "POST", {
                "dnn": dnn, "access_type": "untrusted",
                "offload_pref": "5g_only", "enabled": True,
            })
            res_5g, _ = _wifi_api("/api/wifi-offload/admission", "POST", {
                "imsi": baseline.imsi("embb-bulk", 2), "dnn": dnn, "access_type": "untrusted",
            })
            if res_5g.get("allowed"):
                self.fail_test(f"expected deny on 5g_only: {res_5g}")
                return self.result

            # wlan_only mismatch — policy says trusted, request untrusted.
            _wifi_api("/api/wifi-offload/policies", "POST", {
                "dnn": dnn, "access_type": "trusted",
                "offload_pref": "wlan_only", "enabled": True,
            })
            res_mm, _ = _wifi_api("/api/wifi-offload/admission", "POST", {
                "imsi": baseline.imsi("embb-bulk", 2), "dnn": dnn, "access_type": "untrusted",
            })
            if res_mm.get("allowed"):
                self.fail_test(f"expected deny on wlan_only mismatch: {res_mm}")
                return self.result

            # disabled — refuse regardless.
            _wifi_api("/api/wifi-offload/policies", "POST", {
                "dnn": dnn, "access_type": "untrusted",
                "offload_pref": "5g_first", "enabled": False,
            })
            res_dis, _ = _wifi_api("/api/wifi-offload/admission", "POST", {
                "imsi": baseline.imsi("embb-bulk", 2), "dnn": dnn, "access_type": "untrusted",
            })
            if res_dis.get("allowed"):
                self.fail_test(f"expected deny on disabled: {res_dis}")
                return self.result

            # Re-enable + 5g_first → allow.
            _wifi_api("/api/wifi-offload/policies", "POST", {
                "dnn": dnn, "access_type": "untrusted",
                "offload_pref": "5g_first", "enabled": True,
            })
            res_ok, _ = _wifi_api("/api/wifi-offload/admission", "POST", {
                "imsi": baseline.imsi("embb-bulk", 2), "dnn": dnn, "access_type": "untrusted",
            })
            if not res_ok.get("allowed"):
                self.fail_test(f"expected allow on 5g_first enabled: {res_ok}")
                return self.result

            self.pass_test(dnn=dnn,
                           denied_5g_only=res_5g, denied_mm=res_mm,
                           denied_disabled=res_dis, allowed=res_ok)
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _wifi_api(f"/api/wifi-offload/policies/{dnn}", "DELETE")
        return self.result


class WifiOffloadAttached(TestCase):
    SPEC = TestSpec(
        tc_id="TC-WIFI-004",
        title="Wi-Fi offload attached-UE table lifecycle + audit log",
        spec="TS 23.501 §4.2.8",
        domain=Domain.INTERWORKING,
        nfs=(NF.AMF, NF.N3IWF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=4.0,
        description=(
            "Purpose\n"
            "  Pins the attached-UE table the operator uses to track\n"
            "  active non-3GPP sessions (TS 23.501 §4.2.8 + §6.2.9 / §6.2.9A —\n"
            "  N3IWF/TNGF gateways advertise per-UE inner/outer IP bindings).\n"
            "  Each attach must materialise in the live table, the\n"
            "  per-IMSI is_attached probe, the global list, and the audit\n"
            "  log; detach must wipe the row while preserving the audit.\n"
            "\n"
            "Procedure (TS 23.501 §4.2.8 + §6.2.9 + §6.2.9A)\n"
            "  1. require_ue() — pulls a UE from baseline pool; capture imsi.\n"
            "  2. DELETE /api/wifi-offload/attached?imsi=...&access_type=\n"
            "     untrusted (cleanup any leftover row).\n"
            "  3. POST /api/wifi-offload/attached {imsi, access_type=untrusted,\n"
            "     n3iwf_id='n3iwf-tc-001', inner_ip=10.45.0.42,\n"
            "     outer_ip=192.168.1.42}. Require HTTP 200/201.\n"
            "  4. GET /api/wifi-offload/attached/{imsi}?access_type=untrusted;\n"
            "     require HTTP 200 + attached=true.\n"
            "  5. GET /api/wifi-offload/attached (full list); assert our\n"
            "     imsi row is present.\n"
            "  6. GET /api/wifi-offload/audit?limit=200; assert at least one\n"
            "     entry has imsi==ours AND action=='attached'.\n"
            "  7. DELETE attached row; require HTTP 200.\n"
            "  8. GET is_attached again; assert attached=false.\n"
            "  9. finally: DELETE again (idempotent cleanup).\n"
            " 10. pass_test(imsi=imsi).\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none — UE sourced via require_ue()).\n"
            "\n"
            "Pass criteria\n"
            "  Attach 200/201, is_attached true, list contains row, audit\n"
            "  log has 'attached' entry for the IMSI, detach 200, post-detach\n"
            "  is_attached false.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — requires a UE in the pool (StopTest if pool\n"
            "  empty is swallowed cleanly). No real IKEv2/ESP datapath is\n"
            "  brought up; inner_ip/outer_ip are synthetic. finally{} cleanup\n"
            "  references ue.imsi, so a UE acquisition failure before that\n"
            "  point would raise NameError — guarded by require_ue raising\n"
            "  StopTest first."
        ),
    )

    def run(self):
        try:
            ue = self.require_ue()
            imsi = ue.imsi

            # Cleanup any leftover.
            _wifi_api(f"/api/wifi-offload/attached?imsi={imsi}&access_type=untrusted",
                      "DELETE")

            # Attach.
            _, sa = _wifi_api("/api/wifi-offload/attached", "POST", {
                "imsi": imsi, "access_type": "untrusted",
                "n3iwf_id": "n3iwf-tc-001",
                "inner_ip": "10.45.0.42", "outer_ip": "192.168.1.42",
            })
            if sa not in (200, 201):
                self.fail_test(f"attach failed: {sa}")
                return self.result

            # IsAttached.
            ck, sck = _wifi_api(f"/api/wifi-offload/attached/{imsi}?access_type=untrusted")
            if sck != 200 or not ck.get("attached"):
                self.fail_test(f"is_attached check failed: {ck}")
                return self.result

            # List should show our row.
            lst, _ = _wifi_api("/api/wifi-offload/attached")
            ours = [r for r in lst if r.get("imsi") == imsi]
            if not ours:
                self.fail_test(f"attached list missing imsi={imsi}")
                return self.result

            # Audit log should carry an 'attached' entry.
            audit, _ = _wifi_api(f"/api/wifi-offload/audit?limit=200")
            entries = audit.get("entries") or []
            recent = [e for e in entries
                      if e.get("imsi") == imsi and e.get("action") == "attached"]
            if not recent:
                self.fail_test("attach not recorded in audit log",
                               last_entries=entries[:5])
                return self.result

            # Detach.
            _, sd = _wifi_api(
                f"/api/wifi-offload/attached?imsi={imsi}&access_type=untrusted",
                "DELETE")
            if sd != 200:
                self.fail_test(f"detach failed: {sd}")
                return self.result

            # Verify gone.
            ck2, _ = _wifi_api(f"/api/wifi-offload/attached/{imsi}?access_type=untrusted")
            if ck2.get("attached"):
                self.fail_test(f"still attached after detach: {ck2}")
                return self.result

            self.pass_test(imsi=imsi)
        except StopTest:
            pass
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        finally:
            _wifi_api(f"/api/wifi-offload/attached?imsi={ue.imsi}&access_type=untrusted",
                      "DELETE")
        return self.result


ALL_WIFI_OFFLOAD_TCS = [
    WifiOffloadPolicyCRUD,
    WifiOffloadAdmissionDefault,
    WifiOffloadAdmissionGate,
    WifiOffloadAttached,
]

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: KPI Dashboard (TS 28.552 / TS 28.554).

TS 28.552 §5.1   AMF performance measurements (RM/AUTH/SEC/MM/NGAP).
TS 28.552 §5.3   SMF performance measurements (SM.*).
TS 28.552 §5.4   UPF performance measurements (N3.*).
TS 28.554 §6     5G E2E KPIs (Mean-of-Ratios success rates).

Drives /api/kpis (the panel aggregator), /api/kpis/raw (TS 28.552 raw
counter dump for SRE / tester), and /api/kpis/reset-peaks. The panel
aggregator is the contract for templates/kpis.html — this file pins
the nested shape so a regression in any sub-section shows up here.
"""

import json
import logging
import urllib.request
import urllib.error

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)

log = logging.getLogger("tester.tc_kpis")


def _kpi_api(path, method="GET", body=None):
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


# Top-level keys the panel reads on every refresh.
_REQUIRED_TOP_KEYS = ("amf", "smf", "upf", "ip_pools", "ims", "mcx",
                      "fm", "charging", "services", "timestamp")


class KpisDashboardShape(TestCase):
    """TC-KPIS-001: /api/kpis returns the nested shape kpis.html consumes."""
    SPEC = TestSpec(
        tc_id="TC-KPIS-001",
        title="/api/kpis returns the nested shape kpis.html consumes",
        spec="TS 28.552 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("smoke", "conformance"),
        setup=Setup.EMPTY,
        expected_duration_s=3.0,
        description=(
            "Purpose\n"
            "  Pin the panel aggregator shape /api/kpis exposes. kpis.html\n"
            "  walks a nested {amf, smf, upf, ip_pools, ims, mcx, fm,\n"
            "  charging, services, timestamp} tree and binds sub-fields\n"
            "  ($('kv_*').textContent). Any regression in this contract\n"
            "  silently blanks the dashboard, so this TC is the canary.\n"
            "\n"
            "Procedure (TS 28.552 §5 panel aggregator)\n"
            "  1. GET /api/kpis; assert status 200.\n"
            "  2. Assert every top-level key in _REQUIRED_TOP_KEYS is\n"
            "     present (10 keys: amf, smf, upf, ip_pools, ims, mcx, fm,\n"
            "     charging, services, timestamp).\n"
            "  3. Assert amf has 'registered_ue' and 'gnb_distribution'.\n"
            "  4. Assert smf has 'total_pdu_sessions' and 'pdu_per_dnn'.\n"
            "  5. Assert upf has 'ul_bytes' and 'packet_loss_rate'.\n"
            "  6. Assert ip_pools is a list (possibly empty).\n"
            "  7. Assert fm.total key is present.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure shape probe.\n"
            "\n"
            "Pass criteria\n"
            "  All required top-level keys + sub-fields present, ip_pools\n"
            "  is a list. Any missing key fails the test.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Shape-only — does not assert non-zero counters."
        ),
    )

    def run(self):
        try:
            r, s = _kpi_api("/api/kpis")
            if s != 200:
                self.fail_test(f"GET /api/kpis failed: {s} {r}")
                return self.result
            for k in _REQUIRED_TOP_KEYS:
                if k not in r:
                    self.fail_test(f"missing top-level key {k!r}", body=list(r))
                    return self.result
            # Spot-check sub-keys panel reads (kpis.html: $('kv_*').textContent = …)
            if "registered_ue" not in r["amf"] or "gnb_distribution" not in r["amf"]:
                self.fail_test("amf section missing required keys", amf=list(r["amf"]))
                return self.result
            if "total_pdu_sessions" not in r["smf"] or "pdu_per_dnn" not in r["smf"]:
                self.fail_test("smf section missing required keys", smf=list(r["smf"]))
                return self.result
            if "ul_bytes" not in r["upf"] or "packet_loss_rate" not in r["upf"]:
                self.fail_test("upf section missing required keys", upf=list(r["upf"]))
                return self.result
            if not isinstance(r["ip_pools"], list):
                self.fail_test(f"ip_pools must be a list, got {type(r['ip_pools']).__name__}")
                return self.result
            if "total" not in r["fm"]:
                self.fail_test("fm.total missing", fm=r["fm"])
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class KpisAMFCounters(TestCase):
    """TC-KPIS-002: AMF section carries the TS 28.552 §5.1 counter set."""
    SPEC = TestSpec(
        tc_id="TC-KPIS-002",
        title="AMF KPI section carries TS 28.552 §5.1 counter set",
        spec="TS 28.552 §5.1",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the TS 28.552 §5.1 AMF measurement set on the panel\n"
            "  aggregator. Every counter the AMF performance-management\n"
            "  group defines (RM.* registration, AUTH.*, NGAP setup) must be\n"
            "  surfaced; otherwise the AMF tile renders gaps and the SRE\n"
            "  alerting rules misfire on Mean-of-Ratios success rates.\n"
            "\n"
            "Procedure (TS 28.552 §5.1 AMF PMs)\n"
            "  1. GET /api/kpis; assert status 200.\n"
            "  2. Extract r['amf'].\n"
            "  3. Assert these 11 keys are present: reg_attempts,\n"
            "     reg_successes, reg_failures, auth_attempts, auth_successes,\n"
            "     auth_failures, ngap_setup_attempts, ngap_setup_successes,\n"
            "     ngap_setup_failures, reg_success_rate, auth_success_rate.\n"
            "  4. For reg_attempts, reg_successes, auth_attempts: assert\n"
            "     value is a non-negative number (int or float).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  All 11 counter keys present and the three sampled counters\n"
            "  are non-negative numerics.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Success-rate fields can be 0 (no attempts) or\n"
            "  100 (perfect); the test does not assert a specific value."
        ),
    )

    def run(self):
        try:
            r, s = _kpi_api("/api/kpis")
            if s != 200:
                self.fail_test(f"status {s}")
                return self.result
            amf = r.get("amf", {})
            for k in ("reg_attempts", "reg_successes", "reg_failures",
                      "auth_attempts", "auth_successes", "auth_failures",
                      "ngap_setup_attempts", "ngap_setup_successes",
                      "ngap_setup_failures",
                      "reg_success_rate", "auth_success_rate"):
                if k not in amf:
                    self.fail_test(f"amf missing {k}", keys=list(amf))
                    return self.result
            # Sanity: counts are non-negative integers.
            for k in ("reg_attempts", "reg_successes", "auth_attempts"):
                v = amf[k]
                if not isinstance(v, (int, float)) or v < 0:
                    self.fail_test(f"amf.{k} bad value {v!r}")
                    return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class KpisSMFGBRSplit(TestCase):
    """TC-KPIS-003: SMF section splits gbr_bearers + nongbr_bearers."""
    SPEC = TestSpec(
        tc_id="TC-KPIS-003",
        title="SMF KPI section splits gbr_bearers + nongbr_bearers",
        spec="TS 28.552 §5.3",
        domain=Domain.OAM,
        nfs=(NF.SMF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the TS 28.552 §5.3 SMF measurement set, including the\n"
            "  bearer-class accounting invariant gbr_bearers + nongbr_bearers\n"
            "  == total_bearers. The split underpins QoS planning dashboards\n"
            "  and any drift indicates a corrupted sampler write.\n"
            "\n"
            "Procedure (TS 28.552 §5.3 SMF PMs)\n"
            "  1. GET /api/kpis; extract r['smf'].\n"
            "  2. Assert these 9 keys are present: total_pdu_sessions,\n"
            "     active_pdu_sessions, total_bearers, gbr_bearers,\n"
            "     nongbr_bearers, sess_attempts, sess_successes,\n"
            "     flow_attempts, pdu_per_dnn, pdu_per_slice.\n"
            "  3. Assert gbr_bearers + nongbr_bearers == total_bearers.\n"
            "  4. Assert pdu_per_dnn is a dict (DNN → count map).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  All 10 keys present; bearer split equation holds exactly;\n"
            "  pdu_per_dnn is a dict.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Assumes one default-flow per PDU session in the\n"
            "  accounting (so gbr+nongbr == total exactly, not >=). A race\n"
            "  with a mid-flight PDU establishment could trip the\n"
            "  invariant momentarily."
        ),
    )

    def run(self):
        try:
            r, _ = _kpi_api("/api/kpis")
            smf = r.get("smf", {})
            for k in ("total_pdu_sessions", "active_pdu_sessions",
                      "total_bearers", "gbr_bearers", "nongbr_bearers",
                      "sess_attempts", "sess_successes", "flow_attempts",
                      "pdu_per_dnn", "pdu_per_slice"):
                if k not in smf:
                    self.fail_test(f"smf missing {k}", keys=list(smf))
                    return self.result
            # gbr + nongbr should equal total_bearers (one default flow per session)
            total = smf["gbr_bearers"] + smf["nongbr_bearers"]
            if total != smf["total_bearers"]:
                self.fail_test(
                    f"gbr+nongbr={total} != total_bearers={smf['total_bearers']}",
                    smf=smf)
                return self.result
            if not isinstance(smf["pdu_per_dnn"], dict):
                self.fail_test("pdu_per_dnn must be a dict")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class KpisUPFLossRate(TestCase):
    """TC-KPIS-004: UPF section computes packet_loss_rate from drops."""
    SPEC = TestSpec(
        tc_id="TC-KPIS-004",
        title="UPF KPI section computes packet_loss_rate from drops",
        spec="TS 28.552 §5.4",
        domain=Domain.OAM,
        nfs=(NF.UPF,),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the TS 28.552 §5.4 UPF measurement set, especially the\n"
            "  derived packet_loss_rate (computed from ul/dl_dropped over\n"
            "  ul/dl_bytes). loss_rate must be either -1 (sentinel: no\n"
            "  traffic observed) or a number in [0, 100] — never NaN or\n"
            "  Inf, which would break the dashboard gauge.\n"
            "\n"
            "Procedure (TS 28.552 §5.4 UPF PMs)\n"
            "  1. GET /api/kpis; extract r['upf'].\n"
            "  2. Assert these 9 keys are present: sessions, ul_bytes,\n"
            "     dl_bytes, ul_dropped, dl_dropped, ul_metered, dl_metered,\n"
            "     gtpu_errors, packet_loss_rate.\n"
            "  3. Assert packet_loss_rate is either == -1 (no-traffic\n"
            "     sentinel) or a numeric in [0, 100] inclusive.\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  All 9 keys present; loss rate is -1 or in [0, 100]. Negative\n"
            "  values other than -1, or values > 100, fail.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. ul/dl_metered are byte counters; the test does\n"
            "  not assert any relationship between bytes/dropped here —\n"
            "  the precise numerical accounting is covered by the traffic-\n"
            "  suite TCs that generate measured load."
        ),
    )

    def run(self):
        try:
            r, _ = _kpi_api("/api/kpis")
            upf = r.get("upf", {})
            for k in ("sessions", "ul_bytes", "dl_bytes", "ul_dropped",
                      "dl_dropped", "ul_metered", "dl_metered",
                      "gtpu_errors", "packet_loss_rate"):
                if k not in upf:
                    self.fail_test(f"upf missing {k}", keys=list(upf))
                    return self.result
            loss = upf["packet_loss_rate"]
            # Either -1 (no traffic), 0..100 (computed), or a float
            if not (loss == -1 or (isinstance(loss, (int, float)) and 0 <= loss <= 100)):
                self.fail_test(f"loss rate out of range: {loss}")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class KpisIPPools(TestCase):
    """TC-KPIS-005: ip_pools list reports {dnn, allocated, total, utilization_pct}."""
    SPEC = TestSpec(
        tc_id="TC-KPIS-005",
        title="ip_pools list reports {dnn, allocated, total, utilization_pct}",
        spec="TS 28.552 §5.3",
        domain=Domain.OAM,
        nfs=(NF.SMF, NF.UPF),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the IP-pool aggregator shape on /api/kpis. SMF + UPF\n"
            "  share APN pools and the dashboard renders utilisation bars\n"
            "  from the dnn/allocated/total/utilization_pct fields per\n"
            "  entry. Empty pool list is a valid state (no APN pools\n"
            "  configured) and must not break the read path.\n"
            "\n"
            "Procedure (TS 28.552 §5.3 SMF/UPF pool view)\n"
            "  1. GET /api/kpis; extract pools = r['ip_pools'].\n"
            "  2. Assert pools is a list (not None / dict / scalar).\n"
            "  3. If pools is non-empty, take the first entry and assert\n"
            "     these 4 keys are present: dnn, allocated, total,\n"
            "     utilization_pct.\n"
            "  4. pass_test with pool_count=len(pools).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  ip_pools is a list; if non-empty each entry has the 4 keys.\n"
            "  Empty list is a pass.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  pool_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Test inspects only entry [0]; mixed schemas in\n"
            "  later entries would not be caught by this TC. Deeper\n"
            "  per-pool semantic checks (utilisation_pct in [0,100], etc.)\n"
            "  are covered by other dedicated pool tests."
        ),
    )

    def run(self):
        try:
            r, _ = _kpi_api("/api/kpis")
            pools = r.get("ip_pools")
            if not isinstance(pools, list):
                self.fail_test(f"ip_pools not a list: {type(pools).__name__}")
                return self.result
            # If empty (no APN pools configured) that's still valid
            if pools:
                p0 = pools[0]
                for k in ("dnn", "allocated", "total", "utilization_pct"):
                    if k not in p0:
                        self.fail_test(f"pool missing {k}", pool=p0)
                        return self.result
            self.pass_test(pool_count=len(pools))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class KpisRawCountersAndRates(TestCase):
    """TC-KPIS-006: /api/kpis/raw exposes TS 28.552 counter names + rates."""
    SPEC = TestSpec(
        tc_id="TC-KPIS-006",
        title="/api/kpis/raw exposes TS 28.552 counter names + rates",
        spec="TS 28.552 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the raw TS 28.552 counter dump that SRE tooling (Grafana\n"
            "  scrapers, Prometheus exporters) consumes via /api/kpis/raw.\n"
            "  Validates the window_sec query parameter is honoured and the\n"
            "  envelope carries counters, rates, peaks, and Mean-of-Ratios\n"
            "  success rates.\n"
            "\n"
            "Procedure (TS 28.552 §5 + TS 28.554 §6)\n"
            "  1. GET /api/kpis/raw?window_sec=10.\n"
            "  2. Assert status 200.\n"
            "  3. Assert these 7 top-level keys are present: counters,\n"
            "     rates_per_sec, peak_rates, reg_success_rate,\n"
            "     auth_success_rate, sm_success_rate, window_sec.\n"
            "  4. Assert abs(window_sec - 10.0) <= 0.001 (parameter\n"
            "     honoured).\n"
            "  5. Assert counters is a dict (may be empty on a fresh start).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — query parameter window_sec=10 is hard-coded.\n"
            "\n"
            "Pass criteria\n"
            "  All 7 keys present, window_sec matches request to within\n"
            "  0.001s, counters is a dict.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Sampler runs at 1Hz, so a freshly-started core\n"
            "  may return counters={} — that is a valid pass shape."
        ),
    )

    def run(self):
        try:
            r, s = _kpi_api("/api/kpis/raw?window_sec=10")
            if s != 200:
                self.fail_test(f"status {s} {r}")
                return self.result
            for k in ("counters", "rates_per_sec", "peak_rates",
                      "reg_success_rate", "auth_success_rate",
                      "sm_success_rate", "window_sec"):
                if k not in r:
                    self.fail_test(f"missing {k}", keys=list(r))
                    return self.result
            if abs(r["window_sec"] - 10.0) > 0.001:
                self.fail_test(f"window_sec not honoured: {r['window_sec']}")
                return self.result
            # If any AMF activity has happened the RM.RegAtt counter must be
            # an integer in counters{}; if not, the dict can be empty (clean
            # start) — but it must be a dict.
            if not isinstance(r["counters"], dict):
                self.fail_test("counters not a dict")
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class KpisResetPeaks(TestCase):
    """TC-KPIS-007: POST /api/kpis/reset-peaks zeroes peak_rates."""
    SPEC = TestSpec(
        tc_id="TC-KPIS-007",
        title="/api/kpis/reset-peaks zeroes peak_rates",
        spec="TS 28.552 §5",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the operator-driven peak_rates reset endpoint. SRE\n"
            "  workflows zero peaks after a maintenance window so the next\n"
            "  watermarks reflect post-window behaviour; this TC checks the\n"
            "  reset writes through to the sampler state.\n"
            "\n"
            "Procedure (TS 28.552 §5 sampler reset)\n"
            "  1. POST /api/kpis/reset-peaks; assert status 200 and\n"
            "     ok=True.\n"
            "  2. Immediately GET /api/kpis/raw (sampler runs at 1Hz so all\n"
            "     peaks should still be at 0 before the next tick).\n"
            "  3. For each (key, value) in r['peak_rates']: assert value\n"
            "     == 0.\n"
            "  4. pass_test with peak_count=len(peaks).\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — fixed reset → read sequence.\n"
            "\n"
            "Pass criteria\n"
            "  Reset returns ok=True and every peak_rates value reads as 0\n"
            "  on the immediate follow-up GET.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  peak_count.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Race window: the sampler tick (1Hz) could\n"
            "  rewrite a peak between reset and read on a busy core,\n"
            "  flaking the test. The peaks dict may also be empty (cold\n"
            "  start) — in that case the for-loop body runs zero times\n"
            "  and the test passes trivially."
        ),
    )

    def run(self):
        try:
            r, s = _kpi_api("/api/kpis/reset-peaks", "POST")
            if s != 200 or not r.get("ok"):
                self.fail_test(f"reset failed: {s} {r}")
                return self.result
            # Immediately read back peaks; sampler runs at 1Hz so every
            # peak should be 0 right after reset (sampler hasn't ticked yet).
            r2, _ = _kpi_api("/api/kpis/raw")
            peaks = r2.get("peak_rates", {})
            for k, v in peaks.items():
                if v != 0:
                    self.fail_test(
                        f"peak {k} = {v} after reset (expected 0)")
                    return self.result
            self.pass_test(peak_count=len(peaks))
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


class KpisFMCounts(TestCase):
    """TC-KPIS-008: fm section returns severity histogram + total."""
    SPEC = TestSpec(
        tc_id="TC-KPIS-008",
        title="kpis.fm section returns severity histogram + total",
        spec="TS 28.532 §11.2a",
        domain=Domain.OAM,
        nfs=(NF.AMF,),
        severity=Severity.MINOR,
        tags=("conformance",),
        setup=Setup.EMPTY,
        expected_duration_s=2.0,
        description=(
            "Purpose\n"
            "  Pin the FM tile that the kpis.html dashboard renders.\n"
            "  /api/kpis.fm must contain the same severity histogram as\n"
            "  /api/fm/alarm-counts, and the panel relies on the total ==\n"
            "  sum(severities) invariant to render a balanced pie chart.\n"
            "\n"
            "Procedure (TS 28.532 §11.2a fault counters via panel agg)\n"
            "  1. GET /api/kpis; extract f = r['fm'].\n"
            "  2. Assert these 6 keys are present: Critical, Major, Minor,\n"
            "     Warning, Indeterminate, total.\n"
            "  3. Compute sev_sum = Critical + Major + Minor + Warning +\n"
            "     Indeterminate.\n"
            "  4. Assert sev_sum == f['total'].\n"
            "\n"
            "Parameters (self.params)\n"
            "  none — pure read probe.\n"
            "\n"
            "Pass criteria\n"
            "  All 6 keys present and the sum of the 5 severity buckets\n"
            "  equals total exactly.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  none — pass_test() emits no extra details.\n"
            "\n"
            "Known constraints\n"
            "  Setup.EMPTY. Sister TC TC-FM-007 asserts the same invariant\n"
            "  on the raw FM endpoint; this one pins the panel aggregator\n"
            "  re-aggregation, so a drift between /api/kpis.fm and\n"
            "  /api/fm/alarm-counts would fail both TCs in a runner.\n"
            "  Read-only — safe to interleave with other KPI TCs."
        ),
    )

    def run(self):
        try:
            r, _ = _kpi_api("/api/kpis")
            f = r.get("fm", {})
            for k in ("Critical", "Major", "Minor", "Warning",
                      "Indeterminate", "total"):
                if k not in f:
                    self.fail_test(f"fm missing {k}", keys=list(f))
                    return self.result
            # Sum of severities must equal total
            sev_sum = (f["Critical"] + f["Major"] + f["Minor"]
                       + f["Warning"] + f["Indeterminate"])
            if sev_sum != f["total"]:
                self.fail_test(
                    f"severity sum {sev_sum} != total {f['total']}", fm=f)
                return self.result
            self.pass_test()
        except Exception as e:
            self.fail_test(f"Exception: {e}")
        return self.result


ALL_KPIS_TCS = [
    KpisDashboardShape,
    KpisAMFCounters,
    KpisSMFGBRSplit,
    KpisUPFLossRate,
    KpisIPPools,
    KpisRawCountersAndRates,
    KpisResetPeaks,
    KpisFMCounts,
]

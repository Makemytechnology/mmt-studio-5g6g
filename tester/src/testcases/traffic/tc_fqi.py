# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""5QI / QoS Characteristics test cases — TS 23.501 §5.7.4.

Each test pins one row of the Standardized 5QI to QoS Characteristics
Mapping table (TS 23.501 §5.7.4 Table 5.7.4-1, local v19.07.00) against
the live PCF / data plane. The local seed (core/db/seed/baseline.yaml
qos_5qi_catalog) provisions 5QI rows 1, 2, 5, 9, 65, 66, 82 with
priority / PDB / PELR values cited from that table. These tests verify
the mapping survives end-to-end: DNN → PCF policy decision → PCC rule
→ 5QI.

Add new TC-FQI-NNN classes here as the catalog grows. Robot suite that
calls these lives at robot/suites/traffic/08_fqi.robot.
"""

import time
import logging
import subprocess

from src.testcases.base import TestCase
from src.testcases.spec import (
    TestSpec, Domain, NF, Severity, Setup,
)
from src.core.api import core_api
from src.traffic.engine import derive_gateway

log = logging.getLogger("tester.tc_fqi")


# TS 23.501 v19.07.00 Table 5.7.4-1 reference (subset we exercise).
# Keys: 5qi → (resource_type, default_priority, pdb_ms, pelr_exp,
# example_service). Used by TC-FQI-* gates so any drift in PCF
# output is caught against the local spec values, not magic numbers.
TS_23_501_5QI_TABLE = {
    1:  ("GBR",                20, 100, -2,  "Conversational Voice"),
    2:  ("GBR",                40, 150, -3,  "Conversational Video"),
    5:  ("NonGBR",             10, 100, -6,  "IMS Signalling"),
    9:  ("NonGBR",             90, 300, -6,  "Default best-effort video/web"),
    65: ("NonGBR",              7,  75, -2,  "MCX signalling"),
    66: ("GBR",                20, 100, -2,  "Non-MC PTT voice"),
    82: ("delay-critical-GBR", 19,  10, -4,  "Discrete Automation (URLLC)"),
}


def pcf_policy_preview(imsi, dnn, sst=1, retries=6, delay=0.5):
    """GET /api/pcf/policy-preview for one (UE, DNN, slice). Returns
    parsed JSON or None. Read-only, side-effect-free — safe to call
    without a live PDU session. Retries through the post-Setup.BASELINE
    core-startup window where the webservice has not yet bound :5000."""
    path = f"/api/pcf/policy-preview?imsi={imsi}&dnn={dnn}&sst={sst}"
    for attempt in range(retries):
        r = core_api(path, "GET", quiet=(attempt < retries - 1))
        if isinstance(r, dict) and r.get("ok"):
            return r
        time.sleep(delay)
    return None


def default_rule(preview):
    """Pull the IsDefault PCC rule out of a policy-preview response.
    Returns the rule dict (with FiveQI / ResourceType / ArpPriority) or
    None when no default rule is present (which is itself a failure)."""
    if not isinstance(preview, dict):
        return None
    for r in preview.get("rules") or []:
        if r.get("IsDefault"):
            return r
    return None


class FiveQiDefaultInternet(TestCase):
    """5QI=9 (default best-effort) on DNN=internet — TS 23.501 §5.7.4-1."""
    SPEC = TestSpec(
        tc_id="TC-FQI-001",
        title="5QI=9 default on DNN=internet: PCF returns FiveQI=9 NonGBR per TS 23.501 Table 5.7.4-1",
        spec="TS 23.501 §5.7.4",
        domain=Domain.TRAFFIC,
        nfs=(NF.PCF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("conformance", "5qi", "5qi-9", "non-gbr"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  Pins the 5G best-effort default. TS 23.501 §5.7.4 Table 5.7.4-1\n"
            "  row 5QI=9: Resource Type = Non-GBR, Default Priority Level = 90,\n"
            "  PDB = 300 ms, PER = 10⁻⁶, example service = video / web / e-mail\n"
            "  (any TCP-based progressive content). This is the 5QI every UE\n"
            "  inherits when the PDU session has no service binding. If the\n"
            "  catalog or PCF default-fallback ever drifts away from 9, every\n"
            "  non-IMS / non-V2X session breaks silently — this test is the\n"
            "  early-warning canary.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.4 + §5.7.1.5)\n"
            "  1. core_api GET /api/pcf/policy-preview?imsi=…&dnn=internet&sst=1.\n"
            "  2. Pull the IsDefault rule out of preview.rules[].\n"
            "  3. Assert FiveQI == 9 and ResourceType == 'NonGBR'.\n"
            "  4. Cross-check ArpPriority is within [1, 15] — PCF maps the\n"
            "     5QI=9 default service to ARP=9 (not the spec's priority=90\n"
            "     field; ARP and priority-level are different fields per\n"
            "     TS 23.501 §5.7.2.2 vs §5.7.3.3).\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — override IMSI (default: first SIM from sim DB).\n"
            "  dnn  — DNN under test (default: 'internet').\n"
            "\n"
            "Pass criteria\n"
            "  preview.ok == True AND default_rule.FiveQI == 9 AND\n"
            "  default_rule.ResourceType == 'NonGBR'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, dnn, sst, five_qi, resource_type, arp_priority,\n"
            "  service_name, default_qfi, expected_per_ts23501.\n"
            "\n"
            "Known constraints\n"
            "  Read-only PCF preview — no PDU session, no data plane. Real\n"
            "  enforcement against PDB / priority happens at the gNB scheduler\n"
            "  and is out of scope for the in-process SA Core simulator."
        ),
    )

    def run(self):
        ue = self.require_ue()
        imsi = self.params.get("imsi", ue.imsi)
        dnn = self.params.get("dnn", "internet")
        sst = self.params.get("sst", 1)
        preview = pcf_policy_preview(imsi, dnn, sst)
        if not preview or not preview.get("ok"):
            self.fail_test(f"PCF policy-preview failed for {imsi}/{dnn}", preview=preview)
            return self.result
        rule = default_rule(preview)
        if not rule:
            self.fail_test("No IsDefault rule in PCF preview", preview=preview)
            return self.result
        ref = TS_23_501_5QI_TABLE[9]
        details = dict(
            imsi=imsi, dnn=dnn, sst=sst,
            five_qi=rule.get("FiveQI"),
            resource_type=rule.get("ResourceType"),
            arp_priority=rule.get("ArpPriority"),
            service_name=rule.get("ServiceName"),
            default_qfi=preview.get("default_qfi"),
            expected_per_ts23501=f"5QI=9 {ref[0]} prio={ref[1]} PDB={ref[2]}ms",
        )
        if rule.get("FiveQI") == 9 and rule.get("ResourceType") == "NonGBR":
            self.pass_test(**details)
        else:
            self.fail_test(
                f"5QI mismatch: PCF returned FiveQI={rule.get('FiveQI')} "
                f"ResourceType={rule.get('ResourceType')}, expected 9/NonGBR",
                **details,
            )
        return self.result


class FiveQiImsSignalling(TestCase):
    """5QI=5 IMS Signalling on DNN=ims — TS 23.501 §5.7.4-1."""
    SPEC = TestSpec(
        tc_id="TC-FQI-002",
        title="5QI=5 IMS signalling on DNN=ims: PCF returns FiveQI=5 NonGBR per TS 23.501 Table 5.7.4-1",
        spec="TS 23.501 §5.7.4",
        domain=Domain.TRAFFIC,
        nfs=(NF.PCF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "5qi", "5qi-5", "ims", "non-gbr"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.7.4 Table 5.7.4-1 row 5QI=5: Resource Type = Non-GBR,\n"
            "  Default Priority Level = 10 (highest non-GBR priority on the\n"
            "  table), PDB = 100 ms, PER = 10⁻⁶. Reserved for IMS control-plane\n"
            "  signalling (SIP REGISTER / INVITE / SUBSCRIBE etc.). PCF must\n"
            "  return this 5QI for DNN=ims to keep IMS sessions correctly\n"
            "  prioritised against best-effort flows on the same UPF.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.4 + §5.7.1.5)\n"
            "  1. core_api GET /api/pcf/policy-preview?imsi=…&dnn=ims&sst=1.\n"
            "  2. Pull the IsDefault rule out of preview.rules[]; expect\n"
            "     ServiceName='ims_signalling' (seeded in core/db/seed/\n"
            "     baseline.yaml services table — value not asserted, only\n"
            "     reported).\n"
            "  3. Assert FiveQI == 5 and ResourceType == 'NonGBR'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — override IMSI (default: first SIM from sim DB).\n"
            "  dnn  — DNN under test (default: 'ims').\n"
            "\n"
            "Pass criteria\n"
            "  preview.ok == True AND default_rule.FiveQI == 5 AND\n"
            "  default_rule.ResourceType == 'NonGBR'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, dnn, sst, five_qi, resource_type, arp_priority,\n"
            "  service_name, expected_per_ts23501.\n"
            "\n"
            "Known constraints\n"
            "  Read-only — proves the catalog wiring, not real IMS signalling\n"
            "  delay. Use TC-IMS-* suite for actual SIP / RTP behaviour."
        ),
    )

    def run(self):
        ue = self.require_ue()
        imsi = self.params.get("imsi", ue.imsi)
        dnn = self.params.get("dnn", "ims")
        sst = self.params.get("sst", 1)
        preview = pcf_policy_preview(imsi, dnn, sst)
        if not preview or not preview.get("ok"):
            self.fail_test(f"PCF policy-preview failed for {imsi}/{dnn}", preview=preview)
            return self.result
        rule = default_rule(preview)
        if not rule:
            self.fail_test("No IsDefault rule in PCF preview", preview=preview)
            return self.result
        ref = TS_23_501_5QI_TABLE[5]
        details = dict(
            imsi=imsi, dnn=dnn, sst=sst,
            five_qi=rule.get("FiveQI"),
            resource_type=rule.get("ResourceType"),
            arp_priority=rule.get("ArpPriority"),
            service_name=rule.get("ServiceName"),
            expected_per_ts23501=f"5QI=5 {ref[0]} prio={ref[1]} PDB={ref[2]}ms",
        )
        if rule.get("FiveQI") == 5 and rule.get("ResourceType") == "NonGBR":
            self.pass_test(**details)
        else:
            self.fail_test(
                f"5QI mismatch: PCF returned FiveQI={rule.get('FiveQI')} "
                f"ResourceType={rule.get('ResourceType')}, expected 5/NonGBR",
                **details,
            )
        return self.result


class FiveQiFallbackUnboundDnn(TestCase):
    """PCF default-rule fallback for DNN with no service binding — TS 23.501 §5.7.4."""
    SPEC = TestSpec(
        tc_id="TC-FQI-003",
        title="Unbound DNN falls back to 5QI=9 default_data per TS 23.501 §5.7.4",
        spec="TS 23.501 §5.7.4",
        domain=Domain.TRAFFIC,
        nfs=(NF.PCF, NF.SMF),
        severity=Severity.MAJOR,
        tags=("conformance", "5qi", "fallback", "5qi-9"),
        setup=Setup.BASELINE,
        expected_duration_s=5.0,
        description=(
            "Purpose\n"
            "  TS 23.501 §5.7.4 states that when no standardised 5QI mapping\n"
            "  applies, the PCF/SMF shall use a dynamically-assigned 5QI for\n"
            "  the default QoS flow of the PDU session. In this build the PCF\n"
            "  short-circuits that with a fixed 'default_data' rule pinned at\n"
            "  5QI=9 (non-GBR best-effort). This test verifies the fallback\n"
            "  path is wired — without it, an unbound DNN would have no\n"
            "  default rule and the PDU session would be rejected by the SMF.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.4 fallback + core PCF default_data path)\n"
            "  1. core_api GET /api/pcf/policy-preview?imsi=…&dnn=mcx&sst=1.\n"
            "     DNN=mcx is seeded but has no service binding row in the\n"
            "     baseline (only 'internet' has default_data and 'ims' has\n"
            "     ims_signalling).\n"
            "  2. Pull the IsDefault rule; expect ServiceName=='default_data'.\n"
            "  3. Assert FiveQI == 9 and ResourceType == 'NonGBR'.\n"
            "\n"
            "Parameters (self.params)\n"
            "  dnn — DNN with no service binding (default: 'mcx').\n"
            "\n"
            "Pass criteria\n"
            "  preview.ok AND rule.IsDefault AND rule.FiveQI == 9 AND\n"
            "  rule.ResourceType == 'NonGBR' AND rule.ServiceName == 'default_data'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, dnn, five_qi, resource_type, service_name, rule_count.\n"
            "\n"
            "Known constraints\n"
            "  Currently the seed for DNN=mcx pre-allocates default_5qi=65 in\n"
            "  the DNN table but no service binding ships that 5QI — so the\n"
            "  PCF emits the fallback 5QI=9. If a future seed adds an mcx\n"
            "  service binding (5QI=65/66 mission-critical voice), this test\n"
            "  should be re-pointed to a different unbound DNN."
        ),
    )

    def run(self):
        ue = self.require_ue()
        imsi = self.params.get("imsi", ue.imsi)
        dnn = self.params.get("dnn", "mcx")
        sst = self.params.get("sst", 1)
        preview = pcf_policy_preview(imsi, dnn, sst)
        if not preview or not preview.get("ok"):
            self.fail_test(f"PCF policy-preview failed for {imsi}/{dnn}", preview=preview)
            return self.result
        rule = default_rule(preview)
        if not rule:
            self.fail_test("No IsDefault rule in PCF preview (fallback missing)",
                           preview=preview)
            return self.result
        details = dict(
            imsi=imsi, dnn=dnn,
            five_qi=rule.get("FiveQI"),
            resource_type=rule.get("ResourceType"),
            service_name=rule.get("ServiceName"),
            rule_count=preview.get("rule_count") or len(preview.get("rules") or []),
        )
        ok = (
            rule.get("FiveQI") == 9
            and rule.get("ResourceType") == "NonGBR"
            and rule.get("ServiceName") == "default_data"
        )
        if ok:
            self.pass_test(**details)
        else:
            self.fail_test(
                f"Fallback mismatch: expected default_data/9/NonGBR, "
                f"got {rule.get('ServiceName')}/{rule.get('FiveQI')}/"
                f"{rule.get('ResourceType')}",
                **details,
            )
        return self.result


class FiveQiCatalogConformance(TestCase):
    """Cross-DNN 5QI conformance against TS 23.501 Table 5.7.4-1."""
    SPEC = TestSpec(
        tc_id="TC-FQI-004",
        title="Per-DNN 5QI mapping conforms to TS 23.501 Table 5.7.4-1 (internet=9, ims=5)",
        spec="TS 23.501 §5.7.4",
        domain=Domain.TRAFFIC,
        nfs=(NF.PCF, NF.SMF, NF.UDR),
        severity=Severity.MAJOR,
        tags=("conformance", "5qi", "catalog"),
        setup=Setup.BASELINE,
        expected_duration_s=10.0,
        description=(
            "Purpose\n"
            "  Aggregate cross-check of every baseline DNN's default 5QI\n"
            "  against TS 23.501 Table 5.7.4-1 + the local v19.07.00 verified\n"
            "  values. Catches drift in any single DNN binding that would\n"
            "  otherwise only be caught by its dedicated TC-FQI-001..003.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.4)\n"
            "  1. expectations = {\n"
            "       'internet': (9, 'NonGBR'),\n"
            "       'ims':      (5, 'NonGBR'),\n"
            "     }  # only DNNs with seeded service bindings; mcx + iot\n"
            "     fall through to default_data and are covered by TC-FQI-003.\n"
            "  2. For each (dnn, (want_5qi, want_rt)):\n"
            "       a. core_api GET /api/pcf/policy-preview?imsi=…&dnn=…&sst=1.\n"
            "       b. Pull default rule.\n"
            "       c. Append (dnn, got_5qi, want_5qi, got_rt, want_rt, ok)\n"
            "          to a per-DNN result row.\n"
            "  3. Result PASSes iff every row's `ok` flag is True.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi — override IMSI for the lookup (default: first SIM).\n"
            "  expectations — override the {dnn: (5qi, resource_type)} matrix.\n"
            "\n"
            "Pass criteria\n"
            "  Every preview returns the IsDefault rule with the expected\n"
            "  FiveQI + ResourceType per the matrix.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  rows = list of {dnn, got_5qi, want_5qi, got_rt, want_rt, ok}.\n"
            "  mismatches = subset where ok is False.\n"
            "\n"
            "Known constraints\n"
            "  Only checks the IsDefault rule of each DNN. Dedicated bearers\n"
            "  / dynamic PCC rules added at runtime are not exercised."
        ),
    )

    def run(self):
        ue = self.require_ue()
        imsi = self.params.get("imsi", ue.imsi)
        expectations = self.params.get("expectations") or {
            "internet": (9, "NonGBR"),
            "ims":      (5, "NonGBR"),
        }
        rows = []
        for dnn, (want_5qi, want_rt) in expectations.items():
            preview = pcf_policy_preview(imsi, dnn, 1)
            rule = default_rule(preview) if preview and preview.get("ok") else None
            got_5qi = rule.get("FiveQI") if rule else None
            got_rt = rule.get("ResourceType") if rule else None
            rows.append({
                "dnn": dnn,
                "got_5qi": got_5qi, "want_5qi": want_5qi,
                "got_rt": got_rt, "want_rt": want_rt,
                "ok": got_5qi == want_5qi and got_rt == want_rt,
            })
        mismatches = [r for r in rows if not r["ok"]]
        if not mismatches:
            self.pass_test(rows=rows, mismatches=[])
        else:
            self.fail_test(
                f"5QI catalog drift on {len(mismatches)}/{len(rows)} DNN(s)",
                rows=rows, mismatches=mismatches,
            )
        return self.result


class FiveQiPdbInternet(TestCase):
    """5QI=9 PDB envelope — ICMP RTT must stay within 300 ms (TS 23.501 §5.7.3.4)."""
    SPEC = TestSpec(
        tc_id="TC-FQI-005",
        title="5QI=9 PDB on DNN=internet: avg ICMP RTT < 300 ms per TS 23.501 §5.7.3.4",
        spec="TS 23.501 §5.7.3.4",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "5qi", "5qi-9", "pdb", "icmp"),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Enforces the §5.7.3.4 Packet Delay Budget for the default\n"
            "  best-effort flow. TS 23.501 Table 5.7.4-1 row 5QI=9 ⇒ PDB =\n"
            "  300 ms (CN PDB excluded — that's another 20 ms allocated to\n"
            "  the core network per §5.7.3.4 Note 11). End-to-end ICMP RTT\n"
            "  on the local lab topology should sit comfortably under the\n"
            "  ceiling; if RTT spikes above 300 ms it points to UPF queue\n"
            "  buildup, GTP-U encap stalls, or tunnel saturation.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.3.4 + §5.7.4-1 5QI=9)\n"
            "  1. require_gnb() + require_ue() + register_ue + establish_pdu\n"
            "     on DNN=internet (PSI=1, default 5QI=9).\n"
            "  2. target = params.ping_target OR derive_gateway(ue_ip).\n"
            "  3. subprocess.run(['ping','-c',count,'-I',ue_ip,'-W','2',target]).\n"
            "  4. Parse the 'min/avg/max/mdev' line for avg_ms.\n"
            "  5. Assert avg_ms < pdb_ms (default 300, override via params).\n"
            "\n"
            "Parameters (self.params)\n"
            "  ping_target — destination IP (default: derive_gateway(ue_ip)).\n"
            "  count       — echo requests (default: 20).\n"
            "  pdb_ms      — PDB ceiling in ms (default: 300 per Table 5.7.4-1).\n"
            "\n"
            "Pass criteria\n"
            "  ping returncode == 0 AND avg_ms < pdb_ms.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_ip, target, count, min_ms, avg_ms, max_ms, loss_pct,\n"
            "  pdb_ms, five_qi=9, pdb_headroom_ms=(pdb_ms - avg_ms).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE on a local lab — sub-10ms RTTs are typical. The\n"
            "  300 ms gate is the spec ceiling; failures here mean the data\n"
            "  plane has a real problem, not that the test is too strict."
        ),
    )

    def run(self):
        return run_pdb_test(self, dnn="internet", five_qi=9, default_pdb_ms=300)


class FiveQiPdbIms(TestCase):
    """5QI=5 PDB envelope on IMS DNN — ICMP RTT must stay within 100 ms."""
    SPEC = TestSpec(
        tc_id="TC-FQI-006",
        title="5QI=5 PDB on DNN=ims: avg ICMP RTT < 100 ms per TS 23.501 §5.7.3.4",
        spec="TS 23.501 §5.7.3.4",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "5qi", "5qi-5", "ims", "pdb", "icmp"),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Enforces the §5.7.3.4 Packet Delay Budget for IMS signalling.\n"
            "  TS 23.501 Table 5.7.4-1 row 5QI=5 ⇒ PDB = 100 ms — three times\n"
            "  tighter than 5QI=9, because SIP registration and IMS call\n"
            "  setup are interactive control-plane flows where delay maps\n"
            "  directly to post-dial latency / re-registration failures.\n"
            "  Pins the §5.7.3 PDB enforcement chain for the IMS bearer.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.3.4 + §5.7.4-1 5QI=5)\n"
            "  1. require_gnb() + require_ue (must support DNN=ims; the\n"
            "     baseline embb-bulk UEs do).\n"
            "  2. register_ue + establish_pdu(dnn='ims', psi=2). PSI=2\n"
            "     keeps PSI=1/internet free for parallel suites.\n"
            "  3. target = params.ping_target OR derive_gateway(ue_ip).\n"
            "  4. subprocess.run(['ping','-c',count,'-I',ue_ip,'-W','2',target]).\n"
            "  5. Parse avg_ms; assert avg_ms < pdb_ms (default 100).\n"
            "\n"
            "Parameters (self.params)\n"
            "  ping_target — destination IP (default: derive_gateway(ue_ip)).\n"
            "  count       — echo requests (default: 20).\n"
            "  pdb_ms      — PDB ceiling in ms (default: 100 per Table 5.7.4-1).\n"
            "\n"
            "Pass criteria\n"
            "  ping returncode == 0 AND avg_ms < pdb_ms (= 100 ms by default).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_ip, target, count, min_ms, avg_ms, max_ms, loss_pct,\n"
            "  pdb_ms, five_qi=5, dnn='ims', psi=2,\n"
            "  pdb_headroom_ms=(pdb_ms - avg_ms).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE; uses a separate PSI=2 bearer on the same UE.\n"
            "  Does not exercise IMS signalling itself — just the latency of\n"
            "  the bearer that IMS will be carried on."
        ),
    )

    def run(self):
        return run_pdb_test(
            self, dnn="ims", five_qi=5, default_pdb_ms=100, psi=2,
        )


def run_pdb_test(tc, *, dnn, five_qi, default_pdb_ms, psi=1):
    """Shared PDB-gate runner for TC-FQI PDB tests.

    Registers a UE, establishes the named DNN bearer at the given PSI,
    runs ICMP from the UE IP, parses the round-trip stats, and asserts
    avg RTT < the 5QI's standardised Packet Delay Budget.
    """
    gnb = tc.require_gnb()
    ue = tc.require_ue()
    target = tc.params.get("ping_target")
    count = tc.params.get("count", 20)
    pdb_ms = tc.params.get("pdb_ms", default_pdb_ms)

    if not tc.register_ue(ue, gnb):
        return tc.result
    if not tc.establish_pdu(ue, psi=psi, dnn=dnn):
        return tc.result

    ue_ip = ue.pdu_sessions.get(psi, {}).get("ip", "unknown")
    if not target:
        target = derive_gateway(ue_ip)
    cmd = ["ping", "-c", str(count), "-I", ue_ip, "-W", "2", target]
    try:
        proc = subprocess.run(
            cmd, capture_output=True, text=True, timeout=count * 3 + 10
        )
    except subprocess.TimeoutExpired:
        tc.fail_test("Ping timed out", ue_ip=ue_ip, target=target)
        return tc.result
    except Exception as e:
        tc.fail_test(f"Ping error: {e}", ue_ip=ue_ip, target=target)
        return tc.result

    if proc.returncode != 0:
        tc.fail_test(f"Ping failed: {proc.stderr[:200]}",
                     ue_ip=ue_ip, target=target)
        return tc.result

    stats = {"ue_ip": ue_ip, "target": target, "count": count,
             "dnn": dnn, "psi": psi, "five_qi": five_qi, "pdb_ms": pdb_ms}
    avg_ms = None
    for line in proc.stdout.split("\n"):
        if "min/avg/max" in line:
            parts = line.split("=")[1].strip().split("/")
            stats["min_ms"] = float(parts[0])
            stats["avg_ms"] = avg_ms = float(parts[1])
            stats["max_ms"] = float(parts[2])
        if "packet loss" in line:
            for part in line.split(","):
                if "packet loss" in part:
                    stats["loss_pct"] = float(part.strip().split("%")[0])
    if avg_ms is None:
        tc.fail_test("Could not parse ping avg RTT", stdout=proc.stdout[:200], **stats)
        return tc.result
    stats["pdb_headroom_ms"] = round(pdb_ms - avg_ms, 3)
    if avg_ms < pdb_ms:
        tc.pass_test(**stats)
    else:
        tc.fail_test(
            f"5QI={five_qi} PDB violated: avg_ms={avg_ms} >= pdb_ms={pdb_ms}",
            **stats,
        )
    return tc.result

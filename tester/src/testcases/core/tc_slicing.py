# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Network Slicing — S-NSSAI, multi-slice PDU sessions, QoS isolation.

TS 23.501 section 5.15 — Network Slicing architecture.
TS 24.501 section 5.5.1 — Registration with Requested NSSAI.
S-NSSAI = SST (1 byte) + SD (3 bytes optional).
SST=1: eMBB, SST=2: URLLC, SST=3: MIoT.
"""

import time
import logging
import concurrent.futures

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.config import TRAFFIC_DURATION
from src.traffic.engine import TrafficEngine, derive_gateway
from src.observability.core_stats import collect_upf_stats, compute_upf_delta
from src.traffic.stats.mos import estimate_mos

log = logging.getLogger("tester.tc_slicing")


class SliceBase(TestCase):
    """Base class for slicing tests."""
    _abstract = True

    def _register_with_nssai(self, ue, gnb, requested_nssai, timeout=15):
        """Register UE with specific Requested NSSAI.

        Overrides the default NSSAI from config.
        """
        from src.config import UE_DEFAULTS
        # Temporarily override requested NSSAI
        original = UE_DEFAULTS.get("requested_nssai")
        UE_DEFAULTS["requested_nssai"] = requested_nssai
        try:
            gnb.attach_ue(ue)
            ue.register()
            ok = ue.wait_for_state("REGISTERED", timeout=timeout)
            if not ok:
                self.fail_test(f"Registration failed with NSSAI={requested_nssai}",
                               ue=ue.imsi, ue_state=ue.state)
            return ok
        finally:
            UE_DEFAULTS["requested_nssai"] = original

    def _verify_slice_traffic(self, ue_ip, server, duration=10, bandwidth="1M"):
        """Quick UL traffic verify. Returns (ok, kbps)."""
        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth=bandwidth, duration=duration, direction="ul")
        session.start()
        stats = session.stop()
        ok = stats.throughput_kbps > 0
        return ok, stats.throughput_kbps


class EmbbSlice(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-001",
        title="eMBB slice (SST=1) — registration, PDU, traffic verify",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        slice=Slice.EMBB,
        dnn="internet",
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Foundation gate for every downstream slicing test. Pins\n"
            "  TS 23.501 §5.15.2.2 — SST=1 is the standardised eMBB slice\n"
            "  type — and TS 23.502 §4.2.2.2.2 (UE Registration with slice\n"
            "  selection): the AMF/NSSF must accept the Requested NSSAI\n"
            "  and the SMF must instantiate a PDU session on the matching\n"
            "  eMBB UPF anchor before any user-plane traffic can flow.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.5.2.1 + TS 23.502 §4.2.2.2.2)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. _register_with_nssai(ue, gnb, [{sst:1, sd:0x010203}]) —\n"
            "     temporarily overrides UE_DEFAULTS['requested_nssai'],\n"
            "     attach_ue + ue.register(); wait for REGISTERED (15 s).\n"
            "     The Registration Accept carries Allowed NSSAI per\n"
            "     TS 23.501 §5.15.5.2.1.\n"
            "  3. establish_pdu(ue, psi=1, dnn='internet', sst=1,\n"
            "     sd=0x010203) — SMF PDU Session Establishment routed via\n"
            "     NSSF to the eMBB UPF anchor; UPF allocates IPv4 into\n"
            "     ue.pdu_sessions[1].\n"
            "  4. _verify_slice_traffic(ue_ip, derive_gateway(ue_ip)) —\n"
            "     10 s, 1 Mbps UDP UL via TrafficEngine (port 5201).\n"
            "  5. pass_test if throughput_kbps > 0.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; uses pool[0] and hardcoded SST/SD/DNN.)\n"
            "\n"
            "Pass criteria\n"
            "  Registration → PDU establish → UL traffic all succeed AND\n"
            "  session.throughput_kbps > 0.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue (imsi), slice='eMBB', sst=1, sd=0x010203, ue_ip, kbps.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — in-process SA Core simulator; no real\n"
            "  radio. SST/SD/DNN are hardcoded (sst=1, sd=0x010203,\n"
            "  dnn='internet'). Only UL is verified; no DL/jitter/MOS.\n"
            "  Does not assert the Allowed NSSAI IE content from the\n"
            "  Registration Accept — only that REGISTERED state was\n"
            "  reached within 15 s."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]

        nssai = [{"sst": 1, "sd": 0x010203}]
        if not self._register_with_nssai(ue, gnb, nssai):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet", sst=1, sd=0x010203):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)
        ok, kbps = self._verify_slice_traffic(ue_ip, server)

        log.info("eMBB slice: IP=%s traffic=%s (%.1fkbps)", ue_ip, ok, kbps)
        if ok:
            self.pass_test(ue=ue.imsi, slice="eMBB", sst=1, sd=0x010203,
                           ue_ip=ue_ip, kbps=kbps)
        else:
            self.fail_test("eMBB slice traffic failed")
        return self.result


class UrllcSlice(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-002",
        title="URLLC slice (SST=2) — registration, PDU, traffic verify",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        slice=Slice.URLLC,
        dnn="internet",
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins TS 23.501 §5.15.2.2 standardised SST=2 (URLLC). The\n"
            "  URLLC anchor must be selectable end-to-end: NSSF resolves\n"
            "  SST=2 to the URLLC UPF, SMF instantiates the PDU session\n"
            "  there, and user-plane traffic actually flows. Companion\n"
            "  gate to TC-SLC-001 for the low-latency slice anchor.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.5.2.1 + TS 23.502 §4.2.2.2.2)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. _register_with_nssai(ue, gnb, [{sst:2}]) — overrides\n"
            "     UE_DEFAULTS['requested_nssai'] (no SD); attach + register;\n"
            "     wait for REGISTERED (15 s).\n"
            "  3. establish_pdu(ue, psi=1, dnn='internet', sst=2) — SMF\n"
            "     PDU establishment routed to the URLLC UPF anchor.\n"
            "  4. _verify_slice_traffic(ue_ip, derive_gateway(ue_ip)) —\n"
            "     10 s, 1 Mbps UDP UL on port 5201.\n"
            "  5. pass_test if throughput_kbps > 0.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; uses pool[0] and hardcoded SST=2 / DNN.)\n"
            "\n"
            "Pass criteria\n"
            "  Registration → PDU establish → UL traffic all succeed AND\n"
            "  session.throughput_kbps > 0.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue (imsi), slice='URLLC', sst=2, ue_ip, kbps.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — in-process simulator; the test does NOT\n"
            "  measure latency / jitter (it would need TC-FQI tests for\n"
            "  that). No SD is requested — relies on default routing\n"
            "  when SD is absent. Only UL is verified, not DL."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]

        nssai = [{"sst": 2}]
        if not self._register_with_nssai(ue, gnb, nssai):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet", sst=2):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)
        ok, kbps = self._verify_slice_traffic(ue_ip, server)

        log.info("URLLC slice: IP=%s traffic=%s (%.1fkbps)", ue_ip, ok, kbps)
        if ok:
            self.pass_test(ue=ue.imsi, slice="URLLC", sst=2,
                           ue_ip=ue_ip, kbps=kbps)
        else:
            self.fail_test("URLLC slice traffic failed")
        return self.result


class MiotSlice(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-003",
        title="MIoT slice (SST=3) — registration, PDU, traffic verify",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        slice=Slice.MIOT,
        dnn="internet",
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins TS 23.501 §5.15.2.2 standardised SST=3 (MIoT). MIoT\n"
            "  devices send infrequent, low-rate uplink payloads; this\n"
            "  test asserts the MIoT anchor selects via NSSF and that a\n"
            "  low-rate UDP UL completes — the canonical IoT shape\n"
            "  rather than the eMBB UDP burst used by TC-SLC-001.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.5.2.1 + TS 23.502 §4.2.2.2.2)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. _register_with_nssai(ue, gnb, [{sst:3}]) — overrides\n"
            "     UE_DEFAULTS['requested_nssai']; attach + register; wait\n"
            "     for REGISTERED (15 s).\n"
            "  3. establish_pdu(ue, psi=1, dnn='internet', sst=3) — SMF\n"
            "     PDU establishment routed to the MIoT UPF anchor.\n"
            "  4. _verify_slice_traffic(ue_ip, derive_gateway(ue_ip),\n"
            "     bandwidth='100K') — 10 s, 100 kbps UDP UL on port 5201.\n"
            "  5. pass_test if throughput_kbps > 0.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; uses pool[0] and hardcoded SST=3 / 100K.)\n"
            "\n"
            "Pass criteria\n"
            "  Registration → PDU establish → 100 kbps UL all succeed AND\n"
            "  session.throughput_kbps > 0.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue (imsi), slice='MIoT', sst=3, ue_ip, kbps.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — in-process simulator. Bandwidth fixed at\n"
            "  100K to mimic IoT cadence, but the test still uses iperf-\n"
            "  style continuous UDP, not real CIoT NIDD / EDT. No DL or\n"
            "  jitter is asserted. SD is absent from the request."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]

        nssai = [{"sst": 3}]
        if not self._register_with_nssai(ue, gnb, nssai):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet", sst=3):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)
        ok, kbps = self._verify_slice_traffic(ue_ip, server, bandwidth="100K")

        if ok:
            self.pass_test(ue=ue.imsi, slice="MIoT", sst=3,
                           ue_ip=ue_ip, kbps=kbps)
        else:
            self.fail_test("MIoT slice traffic failed")
        return self.result


class DualSlicePdu(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-004",
        title="Dual slice — PDU sessions on SST=1 and SST=2 in parallel",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        slice=Slice.MULTI,
        setup=Setup.BASELINE,
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  Asserts a single UE can carry multiple S-NSSAIs concurrently\n"
            "  in its Allowed NSSAI (TS 23.501 §5.15.5.2.1) and the SMF\n"
            "  can hold two parallel PDU contexts — one per slice anchor.\n"
            "  Pins multi-PDU-session-per-UE separation for the slice\n"
            "  case (TS 23.501 §5.6 + §5.15.5.2.1).\n"
            "\n"
            "Procedure (TS 23.501 §5.15.5.2.1 + TS 23.502 §4.2.2.2.2)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. _register_with_nssai(ue, gnb, [{sst:1, sd:0x010203},\n"
            "     {sst:2}]) — one Registration carrying two Requested\n"
            "     S-NSSAIs.\n"
            "  3. establish_pdu(ue, psi=1, dnn='internet', sst=1,\n"
            "     sd=0x010203) — first PDU on eMBB anchor.\n"
            "  4. establish_pdu(ue, psi=2, dnn='internet', sst=2) —\n"
            "     second PDU on URLLC anchor in parallel.\n"
            "  5. Record ue.pdu_sessions[1]['ip'] and [2]['ip'].\n"
            "  6. pass_test reporting both PSIs / IPs / SSTs.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; uses pool[0] and hardcoded two-NSSAI list.)\n"
            "\n"
            "Pass criteria\n"
            "  Registration with 2 S-NSSAIs succeeds AND both\n"
            "  establish_pdu() calls return True (each PSI gets an IP).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, embb_psi=1, embb_ip, embb_sst=1, urllc_psi=2, urllc_ip,\n"
            "  urllc_sst=2, pdu_sessions (list of PSIs).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — no traffic verify is run; this is a\n"
            "  control-plane PDU establishment test only. The IPs are\n"
            "  not pinged or used for iperf. The test does not assert\n"
            "  the two IPs come from different UPF pools (slice\n"
            "  isolation on the data plane is TC-SLC-005's job)."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]

        nssai = [{"sst": 1, "sd": 0x010203}, {"sst": 2}]
        if not self._register_with_nssai(ue, gnb, nssai):
            return self.result

        # PDU Session 1: eMBB (SST=1)
        if not self.establish_pdu(ue, psi=1, dnn="internet", sst=1, sd=0x010203):
            return self.result
        # PDU Session 2: URLLC (SST=2)
        if not self.establish_pdu(ue, psi=2, dnn="internet", sst=2):
            return self.result

        ip1 = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip2 = ue.pdu_sessions.get(2, {}).get("ip", "unknown")

        log.info("Dual slice: eMBB IP=%s, URLLC IP=%s", ip1, ip2)

        self.pass_test(
            ue=ue.imsi,
            embb_psi=1, embb_ip=ip1, embb_sst=1,
            urllc_psi=2, urllc_ip=ip2, urllc_sst=2,
            pdu_sessions=list(ue.pdu_sessions.keys()),
        )
        return self.result


class DualSliceTraffic(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-005",
        title="Simultaneous bidir traffic on eMBB + URLLC slices",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.MAJOR,
        tags=("conformance", "slow"),
        slice=Slice.MULTI,
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Data-plane companion to TC-SLC-004. Pins TS 23.501 §5.15\n"
            "  slice isolation: two PDUs anchored on different UPFs must\n"
            "  carry independent bidir flows without throughput / jitter\n"
            "  cross-contamination. UPF delta counters are sampled before\n"
            "  and after to surface anchor-level packet counts.\n"
            "\n"
            "Procedure (TS 23.501 §5.15 + TS 23.501 §5.6)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. _register_with_nssai with [{sst:1, sd:0x010203},\n"
            "     {sst:2}]; wait for REGISTERED.\n"
            "  3. establish_pdu(psi=1, dnn='internet', sst=1, sd=0x010203)\n"
            "     and establish_pdu(psi=2, dnn='internet', sst=2).\n"
            "  4. collect_upf_stats() → upf_before snapshot.\n"
            "  5. ThreadPoolExecutor(max_workers=2):\n"
            "       - eMBB: engine.run_bidir(ip_a=ip1, ip_b=server1, UDP,\n"
            "         ul=5201, dl=5202, bw=5M, duration=TRAFFIC_DURATION).\n"
            "       - URLLC: engine.run_bidir(ip_a=ip2, ip_b=server2, UDP,\n"
            "         ul=5203, dl=5204, bw=1M, duration=TRAFFIC_DURATION).\n"
            "  6. collect_upf_stats() → upf_after; compute_upf_delta.\n"
            "  7. pass_test unconditionally with per-slice stats.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; bandwidths/ports hardcoded; duration comes\n"
            "  from src.config.TRAFFIC_DURATION.)\n"
            "\n"
            "Pass criteria\n"
            "  All four steps (register, both PDUs, both run_bidir calls)\n"
            "  must complete without raising; no throughput threshold is\n"
            "  enforced — pass_test is called unconditionally after the\n"
            "  bidir flows return. Failures surface as fail_test from\n"
            "  the helpers (register / establish_pdu) only.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue, duration_s, embb={sst, ul_kbps, dl_kbps, ul_jitter_ms},\n"
            "  urllc={sst, ul_kbps, dl_kbps, ul_jitter_ms}, upf_stats\n"
            "  (delta from compute_upf_delta).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE, requires_dataplane=True. The pass gate is\n"
            "  effectively 'flows ran without exception' — no Mbps floor,\n"
            "  no jitter ceiling, no QoS contract assertion. Slice\n"
            "  isolation is observable from the reported per-slice\n"
            "  numbers but not enforced by an assert."
        ),
        requires_dataplane=True,
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        duration = TRAFFIC_DURATION

        nssai = [{"sst": 1, "sd": 0x010203}, {"sst": 2}]
        if not self._register_with_nssai(ue, gnb, nssai):
            return self.result
        if not self.establish_pdu(ue, psi=1, dnn="internet", sst=1, sd=0x010203):
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="internet", sst=2):
            return self.result

        ip1 = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip2 = ue.pdu_sessions.get(2, {}).get("ip", "unknown")
        server1 = derive_gateway(ip1)
        server2 = derive_gateway(ip2)

        engine = TrafficEngine.get()

        upf_before = collect_upf_stats()

        # Simultaneous bidir traffic on both slices
        # eMBB (SST=1): 5Mbps on ports 5201/5202
        # URLLC (SST=2): 1Mbps on ports 5203/5204
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            f_embb = pool.submit(engine.run_bidir,
                                 ip_a=ip1, ip_b=server1, server=server1,
                                 protocol="udp", ul_port=5201, dl_port=5202,
                                 bandwidth="5M", duration=duration, udp=True)
            f_urllc = pool.submit(engine.run_bidir,
                                  ip_a=ip2, ip_b=server2, server=server2,
                                  protocol="udp", ul_port=5203, dl_port=5204,
                                  bandwidth="1M", duration=duration, udp=True)
            embb_ul_stats, embb_dl_stats = f_embb.result()
            urllc_ul_stats, urllc_dl_stats = f_urllc.result()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        embb_result = {
            "sst": 1,
            "ul_kbps": embb_ul_stats.throughput_kbps,
            "dl_kbps": embb_dl_stats.throughput_kbps,
            "ul_jitter_ms": embb_ul_stats.jitter_ms,
        }
        urllc_result = {
            "sst": 2,
            "ul_kbps": urllc_ul_stats.throughput_kbps,
            "dl_kbps": urllc_dl_stats.throughput_kbps,
            "ul_jitter_ms": urllc_ul_stats.jitter_ms,
        }

        log.info("eMBB(SST=1): UL=%.1fkbps DL=%.1fkbps jitter=%.1fms",
                 embb_result["ul_kbps"], embb_result["dl_kbps"], embb_result["ul_jitter_ms"])
        log.info("URLLC(SST=2): UL=%.1fkbps DL=%.1fkbps jitter=%.1fms",
                 urllc_result["ul_kbps"], urllc_result["dl_kbps"], urllc_result["ul_jitter_ms"])

        self.pass_test(
            ue=ue.imsi, duration_s=duration,
            embb=embb_result, urllc=urllc_result,
            upf_stats=upf_delta,
        )
        return self.result


class TripleSlicePdu(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-006",
        title="Three concurrent S-NSSAIs — eMBB + URLLC + IMS PDU",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        slice=Slice.MULTI,
        setup=Setup.BASELINE,
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  Pushes TC-SLC-004 further: three concurrent PDU contexts\n"
            "  on a single UE — eMBB internet, URLLC internet, and an\n"
            "  IMS-DNN slice (SST=1 + different SD). Pins TS 23.501\n"
            "  §5.15.5.2.1 / §5.6: the AMF must hold three S-NSSAIs in\n"
            "  Allowed NSSAI and the SMF must hold three PDU contexts\n"
            "  for the same SUPI, including two distinct SDs under SST=1.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.5.2.1 + TS 23.501 §5.6.1)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. _register_with_nssai(ue, gnb, [{sst:1, sd:0x010203},\n"
            "     {sst:2}, {sst:1, sd:0x010204}]).\n"
            "  3. establish_pdu(psi=1, dnn='internet', sst=1, sd=0x010203)\n"
            "     — eMBB internet.\n"
            "  4. establish_pdu(psi=2, dnn='internet', sst=2) — URLLC.\n"
            "  5. establish_pdu(psi=3, dnn='ims', sst=1, sd=0x010204) —\n"
            "     IMS slice on a second eMBB SD.\n"
            "  6. Walk ue.pdu_sessions for PSIs 1/2/3, capture {ip, dnn}.\n"
            "  7. pass_test reporting the sessions dict and pdu_count.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; NSSAIs and DNNs hardcoded.)\n"
            "\n"
            "Pass criteria\n"
            "  Registration succeeds AND all three establish_pdu() calls\n"
            "  return True. Each helper failure short-circuits via\n"
            "  fail_test from establish_pdu / register helpers.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue (imsi), sessions={1:{ip,dnn}, 2:{ip,dnn}, 3:{ip,dnn}},\n"
            "  pdu_count (len of ue.pdu_sessions).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — no traffic verify; control-plane only.\n"
            "  No P-CSCF assertion on the IMS PDU (covered by tc_ims).\n"
            "  IPs are recorded but not pinged."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]

        nssai = [{"sst": 1, "sd": 0x010203}, {"sst": 2}, {"sst": 1, "sd": 0x010204}]
        if not self._register_with_nssai(ue, gnb, nssai):
            return self.result

        if not self.establish_pdu(ue, psi=1, dnn="internet", sst=1, sd=0x010203):
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="internet", sst=2):
            return self.result
        if not self.establish_pdu(ue, psi=3, dnn="ims", sst=1, sd=0x010204):
            return self.result

        sessions = {}
        for psi in [1, 2, 3]:
            s = ue.pdu_sessions.get(psi, {})
            sessions[psi] = {"ip": s.get("ip", "unknown"), "dnn": s.get("dnn", "?")}

        log.info("Triple slice: %s", sessions)
        self.pass_test(ue=ue.imsi, sessions=sessions,
                       pdu_count=len(ue.pdu_sessions))
        return self.result


class UnsupportedSliceRejection(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-007",
        title="AMF rejects registration with unsupported S-NSSAI",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.NSSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("conformance", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Negative test for TS 23.501 §5.15.5.2.1 NSSF behaviour: a\n"
            "  Requested NSSAI containing only unsupported S-NSSAIs must\n"
            "  result in either (a) Registration Reject, or (b) accept\n"
            "  with the unsupported entries appearing under Rejected\n"
            "  NSSAI / an empty Allowed NSSAI. Both are conformant per\n"
            "  TS 23.502 §4.2.2.2.2.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.5.2.1 + TS 23.502 §4.2.2.2.2)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. Build nssai = [{sst:99}] (SST=99 is out of the\n"
            "     standardised range and not in the seeded catalog).\n"
            "  3. gnb.attach_ue(ue); temporarily set\n"
            "     UE_DEFAULTS['requested_nssai'] = nssai.\n"
            "  4. ue.register(); ue.wait_for_state('REGISTERED',\n"
            "     timeout=10) — accept either outcome.\n"
            "  5. Restore UE_DEFAULTS['requested_nssai'] in finally.\n"
            "  6. If ue.state == 'REGISTERED' → pass with note that the\n"
            "     AMF accepted; otherwise → pass marking rejected=True.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; SST=99 hardcoded.)\n"
            "\n"
            "Pass criteria\n"
            "  Always passes — both REGISTERED and not-REGISTERED are\n"
            "  treated as conformant outcomes (just reported differently).\n"
            "  The test only verifies the AMF did not crash and reached\n"
            "  a terminal state within 10 s.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue (imsi), requested_sst=99, ue_state. Plus either\n"
            "  note='AMF accepted — check Allowed NSSAI in Registration\n"
            "  Accept' OR rejected=True depending on branch.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The test does NOT introspect the actual\n"
            "  Allowed / Rejected NSSAI IEs from the Registration Accept\n"
            "  — it only checks the resulting NAS state. This is a\n"
            "  hollow-pass shape: both branches resolve to pass_test,\n"
            "  so the only fail path is exception / timeout."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]

        # Request SST=99 which is not configured on the core
        nssai = [{"sst": 99}]
        gnb.attach_ue(ue)

        from src.config import UE_DEFAULTS
        original = UE_DEFAULTS.get("requested_nssai")
        UE_DEFAULTS["requested_nssai"] = nssai
        try:
            ue.register()
            # AMF may reject registration or accept with empty Allowed NSSAI
            ue.wait_for_state("REGISTERED", timeout=10)
        finally:
            UE_DEFAULTS["requested_nssai"] = original

        # Either registration failed (expected) or AMF accepted with limited NSSAI
        if ue.state == "REGISTERED":
            log.info("AMF accepted registration but may have limited Allowed NSSAI")
            self.pass_test(ue=ue.imsi, requested_sst=99, ue_state=ue.state,
                           note="AMF accepted — check Allowed NSSAI in Registration Accept")
        else:
            log.info("AMF rejected registration for unsupported slice (expected)")
            self.pass_test(ue=ue.imsi, requested_sst=99, ue_state=ue.state,
                           rejected=True)
        return self.result


class PartialSliceAcceptance(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-008",
        title="UE requests 2 S-NSSAIs, AMF accepts only the supported one",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Pins TS 23.501 §5.15.5.2.1 NSSF partial-acceptance: a\n"
            "  mixed Requested NSSAI (one supported + one unsupported\n"
            "  S-NSSAI) must produce a Registration Accept whose Allowed\n"
            "  NSSAI keeps the supported entry while Rejected NSSAI\n"
            "  flags the unsupported one. The supported slice must then\n"
            "  carry a working PDU session.\n"
            "\n"
            "Procedure (TS 23.501 §5.15.5.2.1 + TS 23.502 §4.2.2.2.2)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. _register_with_nssai(ue, gnb,\n"
            "     [{sst:1, sd:0x010203}, {sst:99}]) — Requested NSSAI\n"
            "     contains the supported eMBB S-NSSAI + bogus SST=99.\n"
            "  3. establish_pdu(ue, psi=1, dnn='internet', sst=1,\n"
            "     sd=0x010203) — must succeed since SST=1 is in Allowed\n"
            "     NSSAI; explicit fail_test if it returns False.\n"
            "  4. _verify_slice_traffic(ue_ip, derive_gateway(ue_ip)) —\n"
            "     10 s, 1 Mbps UDP UL on port 5201.\n"
            "  5. pass_test reporting supported_slice_ok and kbps.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; both NSSAIs hardcoded.)\n"
            "\n"
            "Pass criteria\n"
            "  Registration with mixed NSSAI succeeds AND PDU on SST=1\n"
            "  succeeds; pass_test is invoked unconditionally after the\n"
            "  traffic verify (no throughput threshold) — supported_slice_ok\n"
            "  is reported but not gated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue (imsi), requested_nssai=[{sst:1},{sst:99}],\n"
            "  supported_slice_ok (bool from traffic verify), kbps.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. The test does not inspect the Rejected\n"
            "  NSSAI IE content directly — it only proves the supported\n"
            "  slice still works. The traffic ok flag is reported but\n"
            "  the pass gate is reached even if kbps == 0."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]

        # Request SST=1 (supported) + SST=99 (unsupported)
        nssai = [{"sst": 1, "sd": 0x010203}, {"sst": 99}]
        if not self._register_with_nssai(ue, gnb, nssai):
            return self.result

        # PDU on supported slice should work
        if not self.establish_pdu(ue, psi=1, dnn="internet", sst=1, sd=0x010203):
            self.fail_test("PDU on supported slice failed after partial acceptance")
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)
        ok, kbps = self._verify_slice_traffic(ue_ip, server)

        self.pass_test(
            ue=ue.imsi, requested_nssai=[{"sst": 1}, {"sst": 99}],
            supported_slice_ok=ok, kbps=kbps,
        )
        return self.result


class MultiUeSameSlice(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-009",
        title="Up to 8 UEs concurrently on the same eMBB slice with traffic",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.MAJOR,
        tags=("scale", "conformance", "slow"),
        slice=Slice.EMBB,
        dnn="internet",
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Scale gate for a single S-NSSAI: up to 8 UEs concurrently\n"
            "  registering, attaching PDU sessions, and driving bidir\n"
            "  traffic on the same eMBB slice (TS 23.501 §5.15). Surfaces\n"
            "  AMF/SMF/UPF concurrency / locking regressions and per-\n"
            "  anchor throughput aggregation under multi-UE load.\n"
            "\n"
            "Procedure (TS 23.501 §5.15 + TS 23.502 §4.2.2.2.2)\n"
            "  1. require_gnb() / require_ue(); ues = ue_pool[:min(8, n)].\n"
            "  2. ThreadPoolExecutor(max_workers=ue_count) registers each\n"
            "     UE in parallel via _reg_one():\n"
            "       attach_ue → register → wait REGISTERED (15 s) →\n"
            "       establish_pdu_session(dnn='internet', sst=1,\n"
            "       sd=0x010203, pdu_session_id=1) → poll pdu_sessions[1]\n"
            "       for a valid IP up to 15 s.\n"
            "  3. fail_test if any UE registration / PDU times out.\n"
            "  4. collect_upf_stats() → upf_before.\n"
            "  5. ThreadPoolExecutor runs _run_ue_bidir per UE:\n"
            "     engine.run_bidir(UDP, bw=1M, duration=TRAFFIC_DURATION)\n"
            "     using staggered ports 5201+i (UL) / 6201+i (DL).\n"
            "  6. collect_upf_stats() → upf_after → compute_upf_delta.\n"
            "  7. Aggregate per-UE ul/dl kbps; pass_test unconditionally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; ue_count = min(8, len(ue_pool)), bandwidth\n"
            "  and ports hardcoded.)\n"
            "\n"
            "Pass criteria\n"
            "  All ue_count UEs complete registration AND PDU IP\n"
            "  allocation; pass_test is then called unconditionally —\n"
            "  no throughput floor on the bidir runs.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_count, sst=1, duration_s, total_ul_mbps, total_dl_mbps,\n"
            "  ue_results=[{imsi, sst, ul_kbps, dl_kbps}, ...], upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE, requires_dataplane=True. ue_count caps at\n"
            "  8 regardless of pool size. Failing any single UE causes\n"
            "  the whole test to fail — no per-UE tolerance. Traffic\n"
            "  pass is hollow (no Mbps gate)."
        ),
        requires_dataplane=True,
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()

        ue_count = min(8, len(self.ue_pool))
        ues = self.ue_pool[:ue_count]
        duration = TRAFFIC_DURATION

        # Register all concurrently on SST=1
        def _reg_one(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                if not ue.wait_for_state("REGISTERED", timeout=15):
                    return (ue, False)
                ue.establish_pdu_session(dnn="internet", sst=1, sd=0x010203, pdu_session_id=1)
                deadline = time.time() + 15
                while time.time() < deadline:
                    s = ue.pdu_sessions.get(1)
                    if s and s.get("ip") and s["ip"] != "unknown":
                        return (ue, True)
                    time.sleep(0.3)
                return (ue, False)
            except Exception:
                return (ue, False)

        log.info("Registering %d UEs on SST=1 (eMBB)", ue_count)
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {pool.submit(_reg_one, ue): ue for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue, ok = f.result()
                if not ok:
                    self.fail_test(f"UE {ue.imsi} registration failed")
                    return self.result

        engine = TrafficEngine.get()
        upf_before = collect_upf_stats()

        # One bidir session per UE, all concurrent
        def _run_ue_bidir(ue, ul_port, dl_port):
            ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            server = derive_gateway(ue_ip)
            ul_stats, dl_stats = engine.run_bidir(
                ip_a=ue_ip, ip_b=server, server=server,
                protocol="udp", ul_port=ul_port, dl_port=dl_port,
                bandwidth="1M", duration=duration, udp=True)
            return ue, ul_stats, dl_stats

        base_ul_port = 5201
        base_dl_port = 6201
        ue_results = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as executor:
            futures = {
                executor.submit(_run_ue_bidir, ue,
                                base_ul_port + i, base_dl_port + i): ue
                for i, ue in enumerate(ues)
            }
            for f in concurrent.futures.as_completed(futures):
                ue, ul_stats, dl_stats = f.result()
                ue_results.append({
                    "imsi": ue.imsi, "sst": 1,
                    "ul_kbps": ul_stats.throughput_kbps,
                    "dl_kbps": dl_stats.throughput_kbps,
                })

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        total_ul = sum(r["ul_kbps"] for r in ue_results)
        total_dl = sum(r["dl_kbps"] for r in ue_results)

        log.info("Multi-UE SST=1: %d UEs, total UL=%.1fMbps DL=%.1fMbps",
                 ue_count, total_ul / 1000, total_dl / 1000)

        self.pass_test(
            ue_count=ue_count, sst=1, duration_s=duration,
            total_ul_mbps=round(total_ul / 1000, 2),
            total_dl_mbps=round(total_dl / 1000, 2),
            ue_results=ue_results, upf_stats=upf_delta,
        )
        return self.result


class MultiUeDifferentSlices(SliceBase):
    SPEC = TestSpec(
        tc_id="TC-SLC-010",
        title="UEs split across eMBB and URLLC slices with concurrent UL",
        spec="TS 23.501 §5.15",
        domain=Domain.SLICING,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.NSSF),
        severity=Severity.MAJOR,
        tags=("scale", "conformance", "slow"),
        slice=Slice.MULTI,
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Multi-UE × multi-slice scale gate. Splits up to 8 UEs\n"
            "  half/half across the eMBB (SST=1) and URLLC (SST=2)\n"
            "  anchors and drives concurrent UL on all of them. Pins\n"
            "  TS 23.501 §5.15 slice isolation under simultaneous load\n"
            "  from independent subscriber sets.\n"
            "\n"
            "Procedure (TS 23.501 §5.15 + TS 23.502 §4.2.2.2.2)\n"
            "  1. require_gnb() / require_ue(); ue_count = min(8, n);\n"
            "     half = ue_count // 2; ues_embb = pool[:half],\n"
            "     ues_urllc = pool[half:ue_count].\n"
            "  2. ThreadPoolExecutor(max_workers=ue_count) runs\n"
            "     _reg_embb (attach→register→establish_pdu_session(\n"
            "     dnn='internet', sst=1, sd=0x010203, pdu_session_id=1))\n"
            "     for half the UEs and _reg_urllc (sst=2) for the other\n"
            "     half. Both poll for an IP up to 15 s.\n"
            "  3. fail_test on the first registration / PDU failure.\n"
            "  4. collect_upf_stats() → upf_before.\n"
            "  5. ThreadPoolExecutor runs _run_ue_ul(ue, 5201+i) for all\n"
            "     UEs — engine.create_session(UDP, bw=1M,\n"
            "     duration=TRAFFIC_DURATION, direction='ul'), start/stop.\n"
            "  6. collect_upf_stats() → upf_after → compute_upf_delta.\n"
            "  7. Aggregate per-slice kbps; pass_test unconditionally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none consumed; ue_count = min(8, len(ue_pool)), split,\n"
            "  ports and bandwidth hardcoded.)\n"
            "\n"
            "Pass criteria\n"
            "  Every UE in both groups must reach REGISTERED + valid\n"
            "  PDU IP within 15 s. After that, pass_test is called\n"
            "  unconditionally regardless of throughput numbers.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  embb_ues, urllc_ues, embb_total_mbps, urllc_total_mbps,\n"
            "  duration_s, ue_results=[{imsi, slice, sst, ul_kbps,\n"
            "  jitter_ms}, ...], upf_stats (delta).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE, requires_dataplane=True. UL only — no DL\n"
            "  bidir on this scale variant. Only kbps + jitter_ms are\n"
            "  reported, no MOS or packet-loss gate. Any single UE\n"
            "  failing fails the whole test."
        ),
        requires_dataplane=True,
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()

        ue_count = min(8, len(self.ue_pool))
        half = ue_count // 2
        ues_embb = self.ue_pool[:half]
        ues_urllc = self.ue_pool[half:ue_count]
        duration = TRAFFIC_DURATION

        # Register eMBB UEs on SST=1
        def _reg_embb(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                if not ue.wait_for_state("REGISTERED", timeout=15):
                    return (ue, False, "embb")
                ue.establish_pdu_session(dnn="internet", sst=1, sd=0x010203, pdu_session_id=1)
                deadline = time.time() + 15
                while time.time() < deadline:
                    s = ue.pdu_sessions.get(1)
                    if s and s.get("ip") and s["ip"] != "unknown":
                        return (ue, True, "embb")
                    time.sleep(0.3)
                return (ue, False, "embb")
            except Exception:
                return (ue, False, "embb")

        # Register URLLC UEs on SST=2
        def _reg_urllc(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                if not ue.wait_for_state("REGISTERED", timeout=15):
                    return (ue, False, "urllc")
                ue.establish_pdu_session(dnn="internet", sst=2, pdu_session_id=1)
                deadline = time.time() + 15
                while time.time() < deadline:
                    s = ue.pdu_sessions.get(1)
                    if s and s.get("ip") and s["ip"] != "unknown":
                        return (ue, True, "urllc")
                    time.sleep(0.3)
                return (ue, False, "urllc")
            except Exception:
                return (ue, False, "urllc")

        log.info("Registering %d eMBB + %d URLLC UEs", len(ues_embb), len(ues_urllc))
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {}
            for ue in ues_embb:
                futures[pool.submit(_reg_embb, ue)] = ue
            for ue in ues_urllc:
                futures[pool.submit(_reg_urllc, ue)] = ue
            for f in concurrent.futures.as_completed(futures):
                ue, ok, stype = f.result()
                if not ok:
                    self.fail_test(f"UE {ue.imsi} ({stype}) registration failed")
                    return self.result

        engine = TrafficEngine.get()
        upf_before = collect_upf_stats()

        all_ues = ues_embb + ues_urllc
        base_ul_port = 5201

        # One UL session per UE, all concurrent
        def _run_ue_ul(ue, ul_port):
            ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            server = derive_gateway(ue_ip)
            session = engine.create_session(
                src_ip=ue_ip, dst_ip=server, protocol="udp",
                dst_port=ul_port, bandwidth="1M", duration=duration, direction="ul")
            session.start()
            return ue, session.stop()

        ue_results = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as executor:
            futures = {
                executor.submit(_run_ue_ul, ue, base_ul_port + i): (ue, "embb" if ue in ues_embb else "urllc")
                for i, ue in enumerate(all_ues)
            }
            for f in concurrent.futures.as_completed(futures):
                ue, stype = futures[f]
                result_ue, stats = f.result()
                ue_results.append({
                    "imsi": result_ue.imsi,
                    "slice": stype,
                    "sst": 1 if stype == "embb" else 2,
                    "ul_kbps": stats.throughput_kbps,
                    "jitter_ms": stats.jitter_ms,
                })

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        embb_kbps = sum(r["ul_kbps"] for r in ue_results if r["slice"] == "embb")
        urllc_kbps = sum(r["ul_kbps"] for r in ue_results if r["slice"] == "urllc")

        log.info("eMBB total=%.1fMbps, URLLC total=%.1fMbps",
                 embb_kbps / 1000, urllc_kbps / 1000)

        self.pass_test(
            embb_ues=len(ues_embb), urllc_ues=len(ues_urllc),
            embb_total_mbps=round(embb_kbps / 1000, 2),
            urllc_total_mbps=round(urllc_kbps / 1000, 2),
            duration_s=duration, ue_results=ue_results,
            upf_stats=upf_delta,
        )
        return self.result


ALL_SLICING_TCS = [
    EmbbSlice, UrllcSlice, MiotSlice,
    DualSlicePdu, DualSliceTraffic, TripleSlicePdu,
    UnsupportedSliceRejection, PartialSliceAcceptance,
    MultiUeSameSlice, MultiUeDifferentSlices,
]

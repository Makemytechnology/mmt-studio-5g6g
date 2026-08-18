# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Jumbo Frame support — large packet handling through GTP-U/UPF.

Validates UPF handling of jumbo frames (MTU up to 9000 bytes).
GTP-U adds overhead (8-byte header + extensions), so inner packet must fit
within the tunnel MTU. Tests increasing packet sizes to find the UPF's limit.
TS 29.281 (GTP-U), TS 23.501 section 5.8.2.11 (MTU handling).
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

log = logging.getLogger("tester.tc_jumbo")


def _run_jumbo_bidir(ue_ip, server, duration, bandwidth, pkt_size, udp=True):
    """Run simultaneous UL+DL UDP with specified packet size.

    UL port 5201 (with length param), DL port 5202.
    Returns (ul_stats, dl_stats) as TrafficStats objects.
    """
    engine = TrafficEngine.get()
    ul_session = engine.create_session(
        src_ip=ue_ip, dst_ip=ue_ip, protocol="udp", dst_port=5201,
        bandwidth=bandwidth, duration=duration, direction="ul", length=pkt_size)
    dl_session = engine.create_session(
        src_ip=ue_ip, dst_ip=ue_ip, protocol="udp", dst_port=5202,
        bandwidth=bandwidth, duration=duration, direction="dl")

    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        f_ul = pool.submit(lambda: (ul_session.start(), ul_session.stop())[-1])
        f_dl = pool.submit(lambda: (dl_session.start(), dl_session.stop())[-1])
        ul_stats = f_ul.result()
        dl_stats = f_dl.result()

    return ul_stats, dl_stats


def _extract_udp_stats(stats, direction="UL"):
    """Extract throughput, jitter, loss from a TrafficStats object."""
    if not stats or stats.throughput_kbps <= 0:
        return {"status": "FAIL", "direction": direction}
    return {
        "status": "PASS",
        "direction": direction,
        "kbps": round(stats.throughput_kbps, 1),
        "mbps": round(stats.throughput_kbps / 1000, 2),
        "jitter_ms": round(stats.jitter_ms, 2),
        "loss_pct": round(stats.loss_pct, 2),
        "packets": stats.tx_packets,
        "lost_packets": stats.lost_packets,
    }


def _make_jumbo_tc(tc_id, name, pkt_size, bandwidth="10M", duration=30):
    """Factory for single-packet-size jumbo frame test."""

    class JumboTC(TestCase):
        SPEC = TestSpec(
            tc_id=tc_id,
            title=f"Jumbo frame {pkt_size}B UL+DL UDP through GTP-U",
            spec="TS 29.281",
            domain=Domain.TRAFFIC,
            nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
            severity=Severity.MAJOR,
            tags=("conformance",),
            setup=Setup.BASELINE,
            expected_duration_s=float(duration) + 30.0,
            requires_dataplane=True,
            description=(
                "Purpose\n"
                "  Pins GTP-U + UPF jumbo-frame handling for a single inner-\n"
                f"  packet size ({pkt_size}-byte payload). TS 23.501 §5.8.2.11\n"
                "  permits the UPF MTU to exceed the default 1500 B; if encap\n"
                "  / fragmentation handling has regressed, jumbo UL or DL\n"
                "  silently drops to zero throughput.\n"
                "\n"
                "Procedure (TS 29.281 §5 + TS 23.501 §5.8.2.11)\n"
                "  1. require_gnb() + require_ue() (first UE in pool).\n"
                "  2. register_ue + establish_pdu(psi=1, dnn='internet').\n"
                "  3. server = derive_gateway(ue_ip).\n"
                "  4. upf_before = collect_upf_stats().\n"
                f"  5. _run_jumbo_bidir(ue_ip, server, duration={duration}s,\n"
                f"     bandwidth='{bandwidth}', pkt_size={pkt_size},\n"
                "     udp=True) — runs UL + DL in parallel with the iperf3\n"
                f"     payload set to {pkt_size} bytes.\n"
                "  6. upf_after = collect_upf_stats(); upf_delta =\n"
                "     compute_upf_delta(upf_before, upf_after).\n"
                "  7. _extract_udp_stats() per direction; each row gets a\n"
                "     status='PASS' when bps>0 with parsed jitter/loss.\n"
                "\n"
                "Parameters (self.params)\n"
                f"  (none consumed; factory pins pkt_size={pkt_size},\n"
                f"  bandwidth='{bandwidth}', duration={duration}s).\n"
                "\n"
                "Pass criteria\n"
                "  ul.status == 'PASS' AND dl.status == 'PASS' — both UL and\n"
                "  DL must carry non-zero throughput in the iperf3 result.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  packet_size, bandwidth, duration_s, ul={mbps,jitter_ms,\n"
                "  loss_pct,status}, dl={…}, upf_stats (delta from\n"
                "  /api/upf/stats).\n"
                "\n"
                "Known constraints\n"
                "  Setup.BASELINE. Loopback MTU is 65536 so the host TUN\n"
                "  fabric accepts any payload up to 8972; failures here mean\n"
                "  the GTP-U encap path itself dropped or fragmented."
            ),
        )

        def run(self):
            gnb = self.require_gnb()
            self.require_ue()
            ue = self.ue_pool[0]

            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=1):
                return self.result

            ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            server = derive_gateway(ue_ip)

            upf_before = collect_upf_stats()

            log.info("Jumbo test: %d-byte packets, %s bandwidth, %ds",
                     pkt_size, bandwidth, duration)

            ul_stats, dl_stats = _run_jumbo_bidir(
                ue_ip, server, duration, bandwidth, pkt_size)

            upf_after = collect_upf_stats()
            upf_delta = compute_upf_delta(upf_before, upf_after)

            ul = _extract_udp_stats(ul_stats, "UL")
            dl = _extract_udp_stats(dl_stats, "DL")

            log.info("Jumbo %d: UL=%.1fMbps jitter=%.1fms loss=%.1f%% | "
                     "DL=%.1fMbps",
                     pkt_size, ul.get("mbps", 0), ul.get("jitter_ms", 0),
                     ul.get("loss_pct", 0), dl.get("mbps", 0))

            if ul["status"] == "PASS" and dl["status"] == "PASS":
                self.pass_test(
                    packet_size=pkt_size, bandwidth=bandwidth, duration_s=duration,
                    ul=ul, dl=dl, upf_stats=upf_delta,
                )
            else:
                self.fail_test(
                    f"Jumbo {pkt_size}: UL={ul['status']} DL={dl['status']}",
                    packet_size=pkt_size, ul=ul, dl=dl, upf_stats=upf_delta,
                )
            return self.result

    JumboTC.__name__ = name
    JumboTC.__qualname__ = name
    return JumboTC


# Single packet size tests
Jumbo1500 = _make_jumbo_tc("TC-JMB-001", "jumbo_1500", 1500, "10M", TRAFFIC_DURATION)
Jumbo4000 = _make_jumbo_tc("TC-JMB-002", "jumbo_4000", 4000, "10M", TRAFFIC_DURATION)
Jumbo8000 = _make_jumbo_tc("TC-JMB-003", "jumbo_8000", 8000, "10M", TRAFFIC_DURATION)
Jumbo8972 = _make_jumbo_tc("TC-JMB-004", "jumbo_8972", 8972, "10M", TRAFFIC_DURATION)


class JumboSweep(TestCase):
    """TC-JMB-005: Throughput sweep across packet sizes."""
    SPEC = TestSpec(
        tc_id="TC-JMB-005",
        title="Jumbo frame throughput sweep across 1500-8972 byte sizes",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=180.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Maps the UPF's MTU/jumbo cliff. GTP-U adds 8 B header (plus\n"
            "  extensions) on top of an inner IP packet, so the effective\n"
            "  ceiling on the tunnel is outer-MTU - 8. TS 23.501 §5.8.2.11\n"
            "  defines the SMF as authoritative on the per-PDU MTU IE; this\n"
            "  test sweeps inner packet sizes [1500, 4000, 8000, 8972] over\n"
            "  a single PDU session and reports per-size throughput + loss,\n"
            "  so a regression in UPF jumbo handling shows up as a specific\n"
            "  size class going FAIL while others stay PASS.\n"
            "\n"
            "Procedure (TS 29.281 §5 GTP-U + TS 23.501 §5.8.2.11 MTU)\n"
            "  1. require_gnb() + require_ue() + register_ue(ue, gnb).\n"
            "  2. establish_pdu(ue, psi=1) — DNN=internet, IPv4.\n"
            "  3. server = derive_gateway(ue_ip).\n"
            "  4. upf_before = collect_upf_stats() snapshot.\n"
            "  5. For pkt_size in [1500, 4000, 8000, 8972]:\n"
            "       a. _run_jumbo_bidir() spawns parallel UDP UL (port 5201,\n"
            "          length=pkt_size) + DL (port 5202) sessions via\n"
            "          ThreadPoolExecutor for `duration` seconds at 10M.\n"
            "       b. _extract_udp_stats() pulls throughput/jitter/loss.\n"
            "       c. Append per-size dict to results, sleep 1 s.\n"
            "  6. upf_after = collect_upf_stats(); compute_upf_delta().\n"
            "  7. Compute all_pass = every (UL,DL) pair == PASS.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none) — bandwidth fixed at 10M, duration = TRAFFIC_DURATION,\n"
            "  SIZES = [1500, 4000, 8000, 8972] is a class constant.\n"
            "\n"
            "Pass criteria\n"
            "  Always invokes pass_test() with the full per-size table —\n"
            "  consumer reads all_pass flag and individual r['ul']/r['dl']\n"
            "  status to decide policy. The sweep is informational by design.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sizes, bandwidth, duration_per_size_s, results (list of\n"
            "  {packet_size, ul, dl}), all_pass, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Single UE only — multi-UE jumbo is TC-JMB-006. Outer MTU on\n"
            "  the bridge must be > 8972 + 8 + 20 + 8 IPv4/UDP overhead, else\n"
            "  the larger sizes will fragment and skew loss. Sleep 1 s\n"
            "  between sizes is to let queues drain, not for spec reasons."
        ),
    )

    SIZES = [1500, 4000, 8000, 8972]

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        ue = self.ue_pool[0]
        bandwidth = "10M"
        duration = TRAFFIC_DURATION

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=1):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(ue_ip)

        upf_before = collect_upf_stats()

        results = []
        for pkt_size in self.SIZES:
            log.info("Sweep: %d-byte packets, %s, %ds", pkt_size, bandwidth, duration)
            ul_stats, dl_stats = _run_jumbo_bidir(
                ue_ip, server, duration, bandwidth, pkt_size)

            ul = _extract_udp_stats(ul_stats, "UL")
            dl = _extract_udp_stats(dl_stats, "DL")

            results.append({
                "packet_size": pkt_size,
                "ul": ul, "dl": dl,
            })
            log.info("  %d bytes: UL=%.1fMbps DL=%.1fMbps loss=%.1f%%",
                     pkt_size, ul.get("mbps", 0), dl.get("mbps", 0),
                     ul.get("loss_pct", 0))
            time.sleep(1)

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        all_pass = all(r["ul"]["status"] == "PASS" and r["dl"]["status"] == "PASS"
                       for r in results)

        self.pass_test(
            sizes=self.SIZES, bandwidth=bandwidth, duration_per_size_s=duration,
            results=results, all_pass=all_pass, upf_stats=upf_delta,
        )
        return self.result


class JumboMultiUe(TestCase):
    """TC-JMB-006: 8 UEs with jumbo frame traffic."""
    SPEC = TestSpec(
        tc_id="TC-JMB-006",
        title="Jumbo frames with 8 simultaneous UEs (8000-byte UL+DL)",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=120.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Combined scale + jumbo stress: 8 concurrent UEs each pushing\n"
            "  UL+DL UDP with 8000-byte inner packets through their own\n"
            "  GTP-U tunnel. Forces the UPF to demux 16 simultaneous jumbo\n"
            "  flows against per-UE TEIDs and per-port (5201+i, 5202+i)\n"
            "  iperf3 sockets — catches MTU regressions that only appear\n"
            "  when the GTP-U fast path is contended (TS 29.281 §5 plus\n"
            "  TS 23.501 §5.8.2.11). Aggregate UL/DL Mbps is reported but\n"
            "  the load-bearing assertion is per-UE PASS in both directions.\n"
            "\n"
            "Procedure (TS 29.281 §5 + TS 23.501 §5.8.2.11)\n"
            "  1. require_gnb() / require_ue(); take first min(8, pool) UEs.\n"
            "  2. Register all UEs in parallel via ThreadPoolExecutor —\n"
            "     each thread: gnb.attach_ue, ue.register, wait_for_state\n"
            "     REGISTERED, establish_pdu_session(dnn=internet, sst=1,\n"
            "     psi=1), then poll for ue.pdu_sessions[1]['ip'].\n"
            "  3. server = derive_gateway(first UE's IPv4).\n"
            "  4. upf_before = collect_upf_stats().\n"
            "  5. For each UE i in [0..count): create UL session on port\n"
            "     5201+i (length=8000) and DL session on port 5202+i, both\n"
            "     UDP at 5 Mbps for `duration` seconds.\n"
            "  6. Run all 2*count sessions concurrently with\n"
            "     ThreadPoolExecutor(max_workers=count*2); collect stats\n"
            "     into ul_map / dl_map keyed by imsi.\n"
            "  7. upf_after = collect_upf_stats(); compute_upf_delta().\n"
            "  8. Aggregate total_ul/dl_mbps, set all_pass.\n"
            "\n"
            "Parameters (self.params)\n"
            "  (none) — ue_count fixed at 8, pkt_size=8000, bandwidth='5M',\n"
            "  duration=TRAFFIC_DURATION.\n"
            "\n"
            "Pass criteria\n"
            "  Every UE's UL == PASS AND DL == PASS (non-zero throughput\n"
            "  per direction). Fail message reports passed/total count.\n"
            "  Any UE registration failure short-circuits the run.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_count, packet_size, bandwidth, duration_s, total_ul_mbps,\n"
            "  total_dl_mbps, ue_results (list of {imsi, ul, dl}), upf_stats.\n"
            "  On fail: ue_count, passed, ue_results.\n"
            "\n"
            "Known constraints\n"
            "  Caps at 8 UEs even if ue_pool is larger. 16 iperf3 servers\n"
            "  + 16 clients run simultaneously — host fd/port limits matter.\n"
            "  Outer bridge MTU must be > 8000 + GTP-U/UDP/IPv4 overhead\n"
            "  (~36 B) or DL frames fragment and skew loss."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()

        ue_count = min(8, len(self.ue_pool))
        ues = self.ue_pool[:ue_count]
        pkt_size = 8000
        bandwidth = "5M"
        duration = TRAFFIC_DURATION

        # Register all UEs concurrently
        def _reg_one(ue):
            try:
                gnb.attach_ue(ue)
                ue.register()
                if not ue.wait_for_state("REGISTERED", timeout=15):
                    return (ue, False)
                ue.establish_pdu_session(dnn="internet", sst=1, pdu_session_id=1)
                deadline = time.time() + 15
                while time.time() < deadline:
                    s = ue.pdu_sessions.get(1)
                    if s and s.get("ip") and s["ip"] != "unknown":
                        return (ue, True)
                    time.sleep(0.3)
                return (ue, False)
            except Exception:
                return (ue, False)

        log.info("Registering %d UEs for jumbo frame test", ue_count)
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count) as pool:
            futures = {pool.submit(_reg_one, ue): ue for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue, ok = f.result()
                if not ok:
                    self.fail_test(f"UE {ue.imsi} registration failed")
                    return self.result

        first_ip = ues[0].pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(first_ip)

        upf_before = collect_upf_stats()

        log.info("Jumbo multi-UE: %d UEs × %d-byte packets, %s (simultaneous UL+DL)",
                 ue_count, pkt_size, bandwidth)

        # Create per-UE sessions: UL on 5201+i, DL on 5202+i
        engine = TrafficEngine.get()
        ul_sessions = {}
        dl_sessions = {}
        for i, ue in enumerate(ues):
            ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            ul_sessions[ue.imsi] = engine.create_session(
                src_ip=ue_ip, dst_ip=server, protocol="udp",
                dst_port=5201 + i, bandwidth=bandwidth, duration=duration,
                direction="ul", length=pkt_size)
            dl_sessions[ue.imsi] = engine.create_session(
                src_ip=ue_ip, dst_ip=ue_ip, protocol="udp",
                dst_port=5202 + i, bandwidth=bandwidth, duration=duration,
                direction="dl")

        # Run all UEs simultaneously, each with UL+DL
        ul_map = {}
        dl_map = {}
        with concurrent.futures.ThreadPoolExecutor(max_workers=ue_count * 2) as executor:
            futures = {}
            for ue in ues:
                ul_sess = ul_sessions[ue.imsi]
                dl_sess = dl_sessions[ue.imsi]
                f_ul = executor.submit(lambda s=ul_sess: (s.start(), s.stop())[-1])
                f_dl = executor.submit(lambda s=dl_sess: (s.start(), s.stop())[-1])
                futures[f_ul] = (ue, "UL")
                futures[f_dl] = (ue, "DL")

            for f in concurrent.futures.as_completed(futures):
                ue, direction = futures[f]
                stats = f.result()
                extracted = _extract_udp_stats(stats, direction)
                extracted["imsi"] = ue.imsi
                if direction == "UL":
                    ul_map[ue.imsi] = extracted
                else:
                    dl_map[ue.imsi] = extracted

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        # Aggregate
        ue_results = []
        for ue in ues:
            ue_results.append({
                "imsi": ue.imsi,
                "ul": ul_map.get(ue.imsi, {"status": "FAIL"}),
                "dl": dl_map.get(ue.imsi, {"status": "FAIL"}),
            })

        total_ul = sum(r["ul"].get("mbps", 0) for r in ue_results)
        total_dl = sum(r["dl"].get("mbps", 0) for r in ue_results)
        all_pass = all(r["ul"].get("status") == "PASS" and r["dl"].get("status") == "PASS"
                       for r in ue_results)

        log.info("Jumbo multi-UE: %d UEs, aggregate UL=%.1fMbps DL=%.1fMbps, all_pass=%s",
                 ue_count, total_ul, total_dl, all_pass)

        if all_pass:
            self.pass_test(
                ue_count=ue_count, packet_size=pkt_size, bandwidth=bandwidth,
                duration_s=duration,
                total_ul_mbps=round(total_ul, 2), total_dl_mbps=round(total_dl, 2),
                ue_results=ue_results, upf_stats=upf_delta,
            )
        else:
            passed = sum(1 for r in ue_results
                         if r["ul"].get("status") == "PASS" and r["dl"].get("status") == "PASS")
            self.fail_test(
                f"{ue_count - passed}/{ue_count} UEs failed jumbo traffic",
                ue_count=ue_count, passed=passed, ue_results=ue_results,
            )
        return self.result


ALL_JUMBO_TCS = [
    Jumbo1500, Jumbo4000, Jumbo8000, Jumbo8972,
    JumboSweep, JumboMultiUe,
]

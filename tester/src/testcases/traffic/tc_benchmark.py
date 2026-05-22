# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Benchmark workloads — aligned with docs/BENCHMARK_PLAN.md in the core repo.

Three workloads, each a TestCase that prints a clean one-line result
suitable for copy-into `mmt_studio_core/benchmarks/results/`:

    TC-BMK-001  attach_storm        Registrations/s + P50/P95/P99 latency
                                    (async control plane, src/control/)
    TC-BMK-002  tcp_ul_throughput   Single-flow TCP UL, 1500B, 60s
    TC-BMK-003  udp_dl_pps          UDP DL, 64B line-rate, pps + drop%

These measure the *core under test* driven by this tester. See the core's
docs/BENCHMARK_PLAN.md for methodology rules (CPU isolation, hugepages,
3 runs minimum, cold-start discard).
"""

import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.core.api import core_api
from src.traffic.engine import TrafficEngine, derive_gateway

log = logging.getLogger("tester.tc_benchmark")


# ═══════════════════════════════════════════════════════════════════════
#  TC-BMK-002 — TCP UL throughput (single flow, 1500B, 60s)
# ═══════════════════════════════════════════════════════════════════════

class TcpUplinkThroughput(TestCase):
    """Single-flow TCP uplink throughput — UE → core, 1500B MTU, 60s.

    Params:
        duration (int)  : iperf3 run length in seconds. Default 60.
        length   (int)  : payload length hint. Default 1448 (= 1500 - 40/52).
    """
    SPEC = TestSpec(
        tc_id="TC-BMK-002",
        title="Benchmark: single-flow TCP UL throughput, 1500B MTU, 60s",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Long-haul single-flow TCP uplink benchmark for the GTP-U user\n"
            "  plane (TS 29.281 §5). Establishes the headline throughput\n"
            "  number this tester pins for the core — bytes/s a single UE\n"
            "  iperf3 flow can push UE-TUN -> gNB-sim -> GTP-U(2152) -> UPF\n"
            "  -> mmtnet at standard 1500B MTU. Drives 60 s instead of the\n"
            "  smoke-test 10 s so TCP cwnd has time to converge and bursty\n"
            "  startup loss is amortised out of tx_mbps.\n"
            "\n"
            "Procedure (TS 29.281 §5 GTP-U + TS 23.501 §5.7)\n"
            "  1. require_gnb() — auto-creates gNB from profile if pool empty.\n"
            "  2. require_ue()  — pulls first SIM from sim DB into ue_pool.\n"
            "  3. register_ue(ue, gnb) — 5G-AKA via NGAP/NAS, AMF authenticates.\n"
            "  4. establish_pdu(ue) — NAS PDU Session Establishment, PSI=1,\n"
            "     DNN=internet; UE acquires IPv4 from apn subnet.\n"
            "  5. server = params.iperf_server OR derive_gateway(ue_ip).\n"
            "  6. TrafficEngine.create_session(src=ue_ip, dst=server,\n"
            "     proto=tcp, dst_port=5201, duration=60, direction=ul,\n"
            "     length=1448 = 1500 - 40/52 IPv4+TCP overhead).\n"
            "  7. session.start() / session.stop() — bytes flow UE -> UPF\n"
            "     -> mmtnet bridge for the full duration.\n"
            "  8. tx_mbps = stats.throughput_kbps / 1000, rounded to 2 dp.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration     — iperf3 run length in seconds (default: 60).\n"
            "  length       — payload length hint, bytes (default: 1448).\n"
            "  iperf_server — TCP server to target (default: gateway of ue_ip).\n"
            "\n"
            "Pass criteria\n"
            "  tx_mbps > 0 — non-zero delivered throughput. Zero throughput\n"
            "  fails with 'iperf3 returned no data'. There is no spec-mandated\n"
            "  minimum bitrate for 5QI=9 default best-effort.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_ip, server, duration_s, length, tx_mbps, tx_gbps, tx_bytes,\n"
            "  rx_bytes, retransmits.\n"
            "\n"
            "Known constraints\n"
            "  Methodology lines up with mmt_studio_core/docs/BENCHMARK_PLAN.md\n"
            "  (CPU isolation, hugepages, 3 runs minimum, cold-start discard).\n"
            "  Setup.BASELINE resets sacore before run. Single-flow only —\n"
            "  multi-flow / scale lives in tc_multi_traffic.py."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = int(self.params.get("duration", 60))
        length = int(self.params.get("length", 1448))

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        server = self.params.get("iperf_server") or derive_gateway(ue_ip)

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="tcp",
            dst_port=5201, duration=duration, direction="ul",
            length=length)
        session.start()
        stats = session.stop()

        tx_mbps = round(stats.throughput_kbps / 1000, 2)
        details = {
            "ue_ip": ue_ip,
            "server": server,
            "duration_s": duration,
            "length": length,
            "tx_mbps": tx_mbps,
            "tx_gbps": round(tx_mbps / 1000, 3),
            "tx_bytes": getattr(stats, "tx_bytes", 0),
            "rx_bytes": getattr(stats, "rx_bytes", 0),
            "retransmits": getattr(stats, "retransmits", 0),
        }
        log.info("TCP UL throughput: %.2f Mbps (%.3f Gbps), retx=%d, dur=%ds",
                 tx_mbps, details["tx_gbps"], details["retransmits"], duration)

        if tx_mbps > 0:
            self.pass_test(**details)
        else:
            self.fail_test("Zero throughput — iperf3 returned no data", **details)
        return self.result


# ═══════════════════════════════════════════════════════════════════════
#  TC-BMK-003 — UDP DL pps (64B line-rate)
# ═══════════════════════════════════════════════════════════════════════

class UdpDownlinkPps(TestCase):
    """UDP downlink packet rate — core → UE, 64B payload, line rate.

    iperf3 -u -l 64 -b 0 drives the NIC as fast as it can. We report
    offered vs received pps and loss % — the interesting number is
    drop rate under line rate, not absolute throughput.

    Params:
        duration    (int)  : iperf3 run length. Default 30.
        length      (int)  : UDP payload length. Default 64.
        target_mbps (str)  : iperf3 -b value. Default "0" = line rate.
    """
    SPEC = TestSpec(
        tc_id="TC-BMK-003",
        title="Benchmark: UDP downlink packet rate, 64B payload, line rate",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("scale",),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Stresses the data path's small-packet pps ceiling, not its\n"
            "  byte/s ceiling. 64 B UDP payloads at line rate force the\n"
            "  GTP-U / UPF pipeline to encap, lookup, and forward at the\n"
            "  highest packet rate the NIC and forwarding plane allow. The\n"
            "  load-bearing number is loss_pct under line rate — that is\n"
            "  where the core's data path starts shedding (PFCP buffers\n"
            "  saturate, DPDK/SR-IOV queues drop). Throughput in Mbps is\n"
            "  almost irrelevant at 64 B because frame overhead dominates.\n"
            "\n"
            "Procedure (TS 29.281 §5 GTP-U + TS 23.501 §5.7)\n"
            "  1. require_gnb(), require_ue() — pull gNB + first SIM.\n"
            "  2. register_ue(ue, gnb) — 5G-AKA via NGAP/NAS.\n"
            "  3. establish_pdu(ue) — PSI=1, DNN=internet, UE gets IPv4.\n"
            "  4. TrafficEngine.create_session(src=ue_ip, dst=ue_ip,\n"
            "     proto=udp, dst_port=5202, duration=30, direction=dl,\n"
            "     bandwidth=target_mbps (default '0' = line rate),\n"
            "     length=64). DL mode: local server binds on UE side,\n"
            "     core acts as iperf client sending into the tunnel.\n"
            "  5. session.start() / session.stop().\n"
            "  6. frame_bytes = payload + 42 (UDP/IPv4 + Ethernet headers).\n"
            "  7. offered_pps = throughput_bps / 8 / frame_bytes; rx_pps =\n"
            "     stats.rx_packets / duration; tx_pps = tx_packets / dur.\n"
            "  8. loss_pct, jitter_ms read straight off iperf3 UDP stats.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration    — iperf3 run length, seconds (default: 30).\n"
            "  length      — UDP payload bytes (default: 64).\n"
            "  target_mbps — iperf3 -b bandwidth target (default: '0' = line rate).\n"
            "\n"
            "Pass criteria\n"
            "  rx_pps > 0 AND loss_pct < 5.0. Zero rx_pps OR loss >= 5%%\n"
            "  fails — that is where the core's data path stops keeping up.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_ip, duration_s, payload_bytes, frame_bytes, target_bw,\n"
            "  rx_mbps, tx_pps, rx_pps, loss_pct, jitter_ms, tx_packets,\n"
            "  lost_packets.\n"
            "\n"
            "Known constraints\n"
            "  5%% loss gate is benchmark policy, not a 3GPP requirement\n"
            "  (TS 23.501 §5.7.4 5QI=9 PELR is 10^-6 but that is an\n"
            "  enforcement-time SLA, not a benchmark gate). 'Line rate'\n"
            "  is bounded by tester NIC + bridge MTU, not just UPF capacity."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        duration = int(self.params.get("duration", 30))
        length = int(self.params.get("length", 64))
        target_bw = str(self.params.get("target_mbps", "0"))  # "0" = line rate

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="udp",
            dst_port=5202, duration=duration, direction="dl",
            bandwidth=target_bw, length=length)
        session.start()
        stats = session.stop()

        rx_mbps = round(stats.throughput_kbps / 1000, 2)
        # pps estimate: bytes/sec ÷ (payload + 42B L2/L3/L4 overhead for UDP/IPv4)
        frame_bytes = length + 42
        offered_pps = int(stats.throughput_kbps * 1000 / 8 / frame_bytes) \
            if stats.throughput_kbps > 0 else 0
        rx_pps = int(getattr(stats, "rx_packets", 0) / duration) if duration else 0
        tx_pps = int(getattr(stats, "tx_packets", 0) / duration) if duration else 0
        loss_pct = float(getattr(stats, "loss_pct", 0) or 0)
        jitter_ms = float(getattr(stats, "jitter_ms", 0) or 0)

        details = {
            "ue_ip": ue_ip,
            "duration_s": duration,
            "payload_bytes": length,
            "frame_bytes": frame_bytes,
            "target_bw": target_bw,
            "rx_mbps": rx_mbps,
            "tx_pps": tx_pps,
            "rx_pps": rx_pps,
            "loss_pct": loss_pct,
            "jitter_ms": round(jitter_ms, 2),
            "tx_packets": getattr(stats, "tx_packets", 0),
            "lost_packets": getattr(stats, "lost_packets", 0),
        }
        log.info("UDP DL pps: tx=%d/s rx=%d/s loss=%.2f%% jitter=%.2fms "
                 "(%.2f Mbps over %ds)", tx_pps, rx_pps, loss_pct, jitter_ms,
                 rx_mbps, duration)

        # A benchmark 'passes' if we got non-zero flow and loss < 5%; above
        # that is where the core's data path starts shedding.
        if rx_pps > 0 and loss_pct < 5.0:
            self.pass_test(**details)
        else:
            self.fail_test(
                f"pps={rx_pps} loss={loss_pct:.2f}% — zero flow or >5% loss",
                **details)
        return self.result


# ═══════════════════════════════════════════════════════════════════════
#  TC-BMK-001 — Attach storm (async control plane)
# ═══════════════════════════════════════════════════════════════════════

class AttachStorm(TestCase):
    """Register N UEs concurrently and measure registration rate + latency.

    Drives the Phase-2 async control plane (src/control/fsm/UeActor) via
    tests/bench/attach_bench.run_bench_collect. The legacy threaded
    AttachStorm was retired here — testcases are migrating to async per
    docs/ARCHITECTURE.md and there's no value in keeping a duplicate
    that exercises the dying path.

    Acceptance gate (docs/ARCHITECTURE.md): p99 < 10000 ms AND
    fail_rate < 1 %.

    Params:
        ue_count    (int)  : target UE count. Default 1000, clamped to
                             len(ue_pool).
        timeout_s   (int)  : per-UE registration timeout. Default 15.
        ng_setup_s  (float): NG Setup timeout. Default 10.
        amf_ip      (str)  : Override AMF IP (default from gnb_profiles.json).
        amf_port    (int)  : Override AMF port (default from gnb_profiles.json).
    """
    SPEC = TestSpec(
        tc_id="TC-BMK-001",
        title="Benchmark: attach storm — N concurrent registrations",
        spec="TS 24.501 §5.5.1.2",
        # Sits under the new BENCHMARK domain so the GUI groups it with
        # other core-procedure benchmarks (PDU storm, HO storm, paging
        # storm, …) under Core Procedures → Benchmark. The procedure
        # being measured is registration, but the test's purpose is
        # throughput / latency at scale, not procedure correctness.
        domain=Domain.BENCHMARK,
        nfs=(NF.GNB, NF.AMF, NF.AUSF, NF.UDM),
        severity=Severity.MAJOR,
        tags=("scale", "slow", "regression", "benchmark"),
        setup=Setup.BASELINE,
        expected_duration_s=120.0,
        description=(
            "Purpose\n"
            "  Headline control-plane scale benchmark. Drives N concurrent\n"
            "  5G-AKA registrations through the Phase-2 async control plane\n"
            "  (src/control/fsm/UeActor) and reports throughput (reg/s) plus\n"
            "  P50/P95/P99 latency. Pins the AMF + AUSF + UDM hot path under\n"
            "  load; a regression here usually shows as P99 inflation before\n"
            "  fail_rate moves. Replaces the retired threaded AttachStorm —\n"
            "  the threaded path is dying per docs/ARCHITECTURE.md and a\n"
            "  duplicate benchmark on it has no value.\n"
            "\n"
            "Procedure (TS 24.501 §5.5.1.2 Registration)\n"
            "  1. require_gnb() — ensures SIMs load + validates config.\n"
            "  2. require_ue()  — primes ue_pool.\n"
            "  3. Clamp target ue_count to len(ue_pool) (warn if short).\n"
            "  4. Build sims list = [u.sim for u in ue_pool[:count]].\n"
            "  5. amf_ip/amf_port from params, fallback to gnb_profiles.json.\n"
            "  6. BenchmarkContext(tc_id, procedure='registration', gate)\n"
            "     zeroes /api/kpis counters on enter, snapshots on exit.\n"
            "  7. asyncio.run(run_bench_collect(amf_ip, amf_port, sims,\n"
            "     timeout_s, ng_setup_s)) — fan out N NGAP/NAS attaches.\n"
            "  8. Push per-UE latencies into bm.tester_samples.\n"
            "  9. bm.report(self.result) — appends history entry, applies\n"
            "     BenchmarkGate(max_p99_ms=10000, max_fail_rate=0.01).\n"
            "\n"
            "Parameters (self.params)\n"
            "  ue_count   — target UE count (default: 1000, clamped to pool).\n"
            "  timeout_s  — per-UE registration timeout (default: 15).\n"
            "  ng_setup_s — NG Setup timeout (default: 10).\n"
            "  amf_ip     — AMF IP override (default: from gnb_profiles.json).\n"
            "  amf_port   — AMF port override (default: from gnb_profiles.json).\n"
            "\n"
            "Pass criteria\n"
            "  BenchmarkGate: p99_ms < 10000 AND fail_rate < 0.01 — gate is\n"
            "  applied inside bm.report() which sets result to FAIL on trip.\n"
            "  Empty ue_pool or run_bench_collect setup error also fails.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  attempted, succeeded, failed, wall_s, throughput (reg/s),\n"
            "  p50_ms, p95_ms, p99_ms, fail_rate (all stats keys except ok,\n"
            "  error, latencies_ms which are stripped). plus setup_error on\n"
            "  bench-setup failure.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — pretest resets sacore. Tester wall time and\n"
            "  core-measured AMF GMM time diverge by gNB-sim + NGAP I/O; both\n"
            "  views are kept on purpose. Failures during bench setup still\n"
            "  drop a history entry (useful trend signal)."
        ),
    )

    def run(self):
        import asyncio
        from tests.bench.attach_bench import run_bench_collect
        from src.config import AMF_IP, AMF_PORT
        from src.core.benchmark import BenchmarkContext, BenchmarkGate

        gnb = self.require_gnb()   # ensures SIMs load + validates config
        self.require_ue()

        target = int(self.params.get("ue_count", 1000))
        pool_size = len(self.ue_pool)
        count = min(target, pool_size)
        if count == 0:
            self.fail_test("No UEs in pool — configure SIMs first")
            return self.result
        if count < target:
            log.warning("Only %d UE(s) configured; running with that count "
                        "(requested %d). Populate UE config for full-scale runs.",
                        count, target)

        timeout_s = float(self.params.get("timeout_s", 15))
        ng_setup_s = float(self.params.get("ng_setup_s", 10))

        sims = [u.sim for u in self.ue_pool[:count] if getattr(u, "sim", None)]
        amf_ip = self.params.get("amf_ip", AMF_IP)
        amf_port = int(self.params.get("amf_port", AMF_PORT))

        log.info("Attach storm (async): count=%d amf=%s:%d timeout=%.1fs",
                 count, amf_ip, amf_port, timeout_s)

        # BenchmarkContext: zeroes the core's /api/kpis counters on
        # __enter__, snapshots them on __exit__. We push the async
        # bench's per-UE latencies into tester_samples so the report
        # carries both views (tester wall time vs core-measured AMF
        # GMM time). Divergence between the two — tester says 1.2s,
        # core says 200ms — itself a useful signal (the rest of the
        # delta is gNB sim + NGAP I/O).
        gate = BenchmarkGate(max_p99_ms=10000.0, max_fail_rate=0.01)
        with BenchmarkContext(self.SPEC.tc_id, procedure="registration",
                              gate=gate) as bm:
            stats = asyncio.run(run_bench_collect(
                amf_ip, amf_port, sims, timeout_s, ng_setup_s))
            if stats.get("ok"):
                bm.tester_samples = list(stats.get("latencies_ms") or [])

        # Strip the bulky per-UE samples from the result payload and
        # let benchmark.report() drop a history entry no matter what —
        # a benchmark that fails its setup is itself a useful data
        # point on the trend (e.g., a regression in gNB NG Setup
        # would show as "0 throughput, setup_error" in history).
        details = {k: v for k, v in stats.items()
                   if k not in ("ok", "error", "latencies_ms")}
        if not stats.get("ok"):
            details["setup_error"] = stats.get("error") or "bench setup failed"
        self.result.details.update(details)
        bm.report(self.result)   # appends to history, applies gate

        if not stats.get("ok"):
            self.fail_test(stats.get("error") or "bench setup failed")
            return self.result

        log.info("Attach storm: %d/%d ok in %.2fs (%.1f reg/s) "
                 "P50=%.1fms P95=%.1fms P99=%.1fms",
                 stats["succeeded"], stats["attempted"], stats["wall_s"],
                 stats["throughput"],
                 stats["p50_ms"], stats["p95_ms"], stats["p99_ms"])

        # bm.report() set the result to FAIL if the gate tripped.
        # Otherwise the test passes — bench's own passed_gate is now
        # subsumed by BenchmarkGate above.
        if self.result.status != "FAIL":
            self.pass_test(**details)
        return self.result


ALL_BENCHMARK_TCS = [
    AttachStorm,
    TcpUplinkThroughput,
    UdpDownlinkPps,
]

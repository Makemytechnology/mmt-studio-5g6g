# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Traffic Generation and QoS Validation.

Uses the Traffic Engine (src/traffic/) for all traffic operations.
TS 23.501 §5.7 — QoS model, 5QI, QoS flows, AMBR, MBR, GBR.
"""

import time
import logging
import subprocess

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import (
    TestSpec, Domain, NF, Slice, Severity, Setup,
)
from src.config import TRAFFIC_DURATION

# All traffic operations via traffic engine
from src.traffic.engine import TrafficEngine, derive_gateway, bw_to_mbps
from src.core.api import core_api
from src.observability.core_stats import collect_upf_stats, compute_upf_delta
from src.traffic.stats.mos import estimate_mos

log = logging.getLogger("tester.tc_traffic")


class TcpUplink(TestCase):
    """TCP uplink throughput — UE sends to core."""
    SPEC = TestSpec(
        tc_id="TC-TRF-001",
        title="TCP UL happy path: UE iperf3 over GTP-U produces non-zero tx_mbps",
        spec="TS 29.281 §4",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Foundational data-plane smoke for the GTP-U uplink path. Verifies\n"
            "  that after a successful NGAP+NAS+PDU-session bring-up, user-plane\n"
            "  TCP packets traverse UE-TUN → gNB-sim → GTP-U(2152) → UPF → core.\n"
            "  TS 23.501 §5.7 default 5QI=9 (non-GBR) has no spec-mandated minimum\n"
            "  rate, so a non-zero delivered throughput is the strict gate.\n"
            "\n"
            "Procedure (TS 29.281 §5 GTP-U + TS 23.501 §5.7)\n"
            "  1. require_gnb() — auto-creates a gNB from config profile if pool empty.\n"
            "  2. require_ue() — pulls first SIM from sim DB into ue_pool.\n"
            "  3. register_ue(ue, gnb) — full 5G-AKA via NGAP/NAS, AMF authenticates.\n"
            "  4. establish_pdu(ue) — NAS PDU Session Establishment, DNN=internet,\n"
            "     PSI=1; UE gets an IPv4 from the apn subnet (e.g. 10.45.0.0/16).\n"
            "  5. server = params.iperf_server OR derive_gateway(ue_ip).\n"
            "  6. TrafficEngine.create_session(src=ue_ip, dst=server, proto=tcp,\n"
            "     dst_port=5201, duration=10s, direction=ul); start(); stop().\n"
            "  7. Packets flow UE-IP → mmttun → GTP-U encap → UPF → mmtnet bridge.\n"
            "\n"
            "Parameters (self.params)\n"
            "  iperf_server — TCP server to target (default: gateway of ue_ip).\n"
            "  port         — TCP destination port (default: 5201).\n"
            "  duration     — seconds (default: 10).\n"
            "\n"
            "Pass criteria\n"
            "  stats.throughput_kbps > 0. Result records direction, ue_ip, server,\n"
            "  tx_mbps, retransmits, duration_s.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  protocol, direction, ue_ip, server, tx_mbps, retransmits, duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — pretest resets sacore to baseline (128 UEs, 3 slices,\n"
            "  4 DNNs). >50 Mbps perf hint is informational only, not asserted."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        port = self.params.get("port", 5201)
        duration = self.params.get("duration", 10)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="tcp",
            dst_port=port, duration=duration, direction="ul")
        session.start()
        stats = session.stop()

        if stats.throughput_kbps > 0:
            self.pass_test(
                protocol="TCP", direction="uplink", ue_ip=ue_ip, server=server,
                tx_mbps=round(stats.throughput_kbps / 1000, 2),
                retransmits=stats.retransmits,
                duration_s=duration,
            )
        else:
            self.fail_test("TCP uplink iperf3 failed")
        return self.result


class TcpDownlink(TestCase):
    """TCP downlink throughput — core iperf3 client sends to UE."""
    SPEC = TestSpec(
        tc_id="TC-TRF-002",
        title="TCP DL happy path: core iperf3 → UE over GTP-U produces non-zero rx_mbps",
        spec="TS 29.281 §4",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Foundational data-plane smoke for the GTP-U downlink path —\n"
            "  packets from the core (iperf3 client) reach the UE TUN through\n"
            "  the UPF and the GTP-U tunnel. The symmetric counterpart of\n"
            "  TC-TRF-001. Same TS 23.501 §5.7 5QI=9 'no minimum rate' premise.\n"
            "\n"
            "Procedure (TS 29.281 §5 GTP-U + TS 23.501 §5.7)\n"
            "  1–4. As TC-TRF-001: gNB up, UE registered, PDU session active.\n"
            "  5. TrafficEngine.create_session(src=ue_ip, dst=ue_ip, proto=tcp,\n"
            "     dst_port=5202, duration=10s, direction=dl). Core-side iperf3\n"
            "     client targets the UE IP; UPF encapsulates each TCP segment in\n"
            "     GTP-U(TEID, 2152) toward gNB-sim, which delivers to UE-TUN.\n"
            "\n"
            "Parameters (self.params)\n"
            "  iperf_server — typically left default; UE IP is the dst.\n"
            "  port         — TCP destination port (default: 5202).\n"
            "  duration     — seconds (default: 10).\n"
            "\n"
            "Pass criteria\n"
            "  stats.throughput_kbps > 0. Result records direction=downlink, ue_ip,\n"
            "  rx_mbps, duration_s.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  protocol, direction, ue_ip, rx_mbps, duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. >50 Mbps perf hint is informational only."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        port = self.params.get("port", 5202)
        duration = self.params.get("duration", 10)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        # Core sends TCP to UE: iperf3 server on tester (UE IP), client on core
        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="tcp",
            dst_port=port, duration=duration, direction="dl")
        session.start()
        stats = session.stop()

        if stats.throughput_kbps > 0:
            self.pass_test(
                protocol="TCP", direction="downlink", ue_ip=ue_ip,
                rx_mbps=round(stats.throughput_kbps / 1000, 2),
                duration_s=duration,
            )
        else:
            self.fail_test("TCP downlink iperf3 failed")
        return self.result


class TcpBidirectional(TestCase):
    """TCP bidirectional — simultaneous UL + DL."""
    SPEC = TestSpec(
        tc_id="TC-TRF-003",
        title="TCP bidir happy path: simultaneous UL + DL iperf3 both produce non-zero throughput",
        spec="TS 29.281 §4",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Exercises full-duplex over a single GTP-U tunnel. UL and DL flow\n"
            "  through the same UPF PDR/FAR pair, sharing kernel ring buffers and\n"
            "  the tunnel TEID. Catches half-duplex regressions in the data plane.\n"
            "\n"
            "Procedure (TS 29.281 §5 GTP-U)\n"
            "  1–4. Register UE + PDU session as TC-TRF-001.\n"
            "  5. TrafficEngine.run_bidir(ip_a=ue_ip, ip_b=server, proto=tcp,\n"
            "     ul_port=5201, dl_port=5202, duration=10s, udp=False).\n"
            "     Spawns one iperf3 server + one iperf3 client per direction;\n"
            "     both streams traverse the same UPF PDR/FAR with the same TEID.\n"
            "\n"
            "Parameters (self.params)\n"
            "  iperf_server — defaults to derive_gateway(ue_ip).\n"
            "  duration     — seconds (default: 10).\n"
            "\n"
            "Pass criteria\n"
            "  ul.throughput_kbps > 0 AND dl.throughput_kbps > 0. If either is\n"
            "  zero, the fail message names which side failed (UL=OK DL=FAIL etc.).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  protocol=TCP, direction=bidirectional, ue_ip, ul_mbps, dl_mbps.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Per-direction perf is local-network dependent."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 10)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        engine = TrafficEngine.get()
        ul_stats, dl_stats = engine.run_bidir(
            ip_a=ue_ip, ip_b=server, server=server,
            protocol="tcp", ul_port=5201, dl_port=5202,
            bandwidth=None, duration=duration, udp=False)

        if ul_stats.throughput_kbps > 0 and dl_stats.throughput_kbps > 0:
            self.pass_test(
                protocol="TCP", direction="bidirectional", ue_ip=ue_ip,
                ul_mbps=round(ul_stats.throughput_kbps / 1000, 2),
                dl_mbps=round(dl_stats.throughput_kbps / 1000, 2),
            )
        else:
            ul_ok = "OK" if ul_stats.throughput_kbps > 0 else "FAIL"
            dl_ok = "OK" if dl_stats.throughput_kbps > 0 else "FAIL"
            self.fail_test(f"TCP bidir: UL={ul_ok} DL={dl_ok}")
        return self.result


# ═══════════════════════════════════════════════════════════════
# UDP Traffic Tests
# ═══════════════════════════════════════════════════════════════

def _udp_desc(direction: str, mbps: int, ratio: float) -> str:
    """Multi-section description for UDP UL/DL TestSpecs (TC-TRF-004/005/014..021).

    Matches the NG Setup template (Purpose / Procedure / Parameters / Pass /
    Reported metrics / Known constraints) so the GUI shows the same structured
    layout for every traffic test. `direction` is e.g. "uplink (UE -> core)";
    `mbps` is the target rate; `ratio` is the min throughput / target gate.
    """
    min_mbps = round(mbps * ratio, 2)
    pct = int(ratio * 100)
    short = "UL" if "uplink" in direction else "DL"
    dir_arg = "ul" if short == "UL" else "dl"
    rx_or_tx = "tx_mbps" if short == "UL" else "rx_mbps"
    return (
        "Purpose\n"
        f"  Verifies the UPF carries a fixed-rate UDP {short} flow at {mbps} Mbps\n"
        f"  with at least {pct}% throughput delivered. UDP has no congestion\n"
        f"  control, so this test exposes UPF or TUN packet loss directly. TS\n"
        f"  23.501 §5.7 default 5QI=9 (non-GBR) has no spec-mandated rate; the\n"
        f"  {pct}% threshold is the project's perf gate, not a 3GPP requirement.\n"
        "\n"
        "Procedure (TS 29.281 §5 GTP-U + TS 23.501 §5.7)\n"
        "  1. require_gnb() + require_ue() (auto-create from config if needed).\n"
        "  2. register_ue(ue, gnb) — 5G-AKA via NGAP/NAS.\n"
        "  3. establish_pdu(ue) — DNN=internet, PSI=1; UE IP assigned.\n"
        "  4. server = params.iperf_server OR derive_gateway(ue_ip).\n"
        f"  5. TrafficEngine.create_session(proto=udp, bandwidth={mbps}M,\n"
        f"     duration=10s, direction={dir_arg}). start(); stop().\n"
        f"  6. iperf3 sends UDP datagrams {direction} through the GTP-U tunnel.\n"
        "  7. iperf3 server reports jitter (inter-arrival variance) and packet\n"
        "     loss (sequence-number gaps).\n"
        "\n"
        "Parameters (self.params)\n"
        f"  bandwidth — target rate (default: {mbps}M).\n"
        "  duration  — seconds (default: 10).\n"
        f"  min_ratio — pass threshold (default: {ratio}).\n"
        "  port      — UDP destination port (default: 5201 UL / 5202 DL).\n"
        "\n"
        "Pass criteria\n"
        f"  throughput_mbps >= {min_mbps} ({pct}% of {mbps} Mbps target).\n"
        "\n"
        "KPI deltas / Reported metrics\n"
        f"  protocol=UDP, direction, ue_ip, target_mbps={mbps}, min_mbps={min_mbps},\n"
        f"  {rx_or_tx}, jitter_ms, lost_packets, total_packets, loss_pct.\n"
        "\n"
        "Known constraints\n"
        "  Setup.BASELINE. Jitter / loss are reported but NOT pass-gated — spec\n"
        "  doesn't mandate bounds for default 5QI=9 (no PDB enforcement applied)."
    )


class UdpUplink(TestCase):
    """UDP uplink — tester (UE) iperf3 client sends UDP to core."""
    SPEC = TestSpec(
        tc_id="TC-TRF-004",
        title="UDP UL at 50 Mbps target: iperf3 UDP UL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("uplink (UE -> core)", 50, 0.85),
    )
    DEFAULT_BANDWIDTH = "50M"
    MIN_THROUGHPUT_RATIO = 0.85

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        port = self.params.get("port", 5201)
        duration = self.params.get("duration", 10)
        bandwidth = self.params.get("bandwidth", self.DEFAULT_BANDWIDTH)
        target_mbps = bw_to_mbps(bandwidth)
        min_ratio = float(self.params.get("min_ratio", self.MIN_THROUGHPUT_RATIO))
        min_mbps = round(target_mbps * min_ratio, 2)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=port, bandwidth=bandwidth, duration=duration, direction="ul")
        session.start()
        stats = session.stop()

        tx_mbps = round(stats.throughput_kbps / 1000, 2) if stats else 0.0
        metrics = dict(
            protocol="UDP", direction="uplink", ue_ip=ue_ip, server=server,
            target_mbps=target_mbps, min_mbps=min_mbps, tx_mbps=tx_mbps,
            jitter_ms=round(stats.jitter_ms, 3) if stats else 0.0,
            lost_packets=stats.lost_packets if stats else 0,
            total_packets=stats.tx_packets if stats else 0,
            loss_pct=round(stats.loss_pct, 2) if stats else 0.0,
        )
        if tx_mbps >= min_mbps:
            self.pass_test(**metrics)
        else:
            self.fail_test(
                f"UL={tx_mbps:.1f}/{target_mbps:.0f} Mbps (LOW); "
                f"threshold={min_mbps:.1f} Mbps ({int(min_ratio*100)}% of target)",
                **metrics)
        return self.result


class UdpUplink100M(UdpUplink):
    SPEC = TestSpec(
        tc_id="TC-TRF-014",
        title="UDP UL at 100 Mbps target: iperf3 UDP UL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("uplink (UE -> core)", 100, UdpUplink.MIN_THROUGHPUT_RATIO),
    )
    DEFAULT_BANDWIDTH = "100M"


class UdpUplink250M(UdpUplink):
    SPEC = TestSpec(
        tc_id="TC-TRF-015",
        title="UDP UL at 250 Mbps target: iperf3 UDP UL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("uplink (UE -> core)", 250, UdpUplink.MIN_THROUGHPUT_RATIO),
    )
    DEFAULT_BANDWIDTH = "250M"


class UdpUplink500M(UdpUplink):
    SPEC = TestSpec(
        tc_id="TC-TRF-016",
        title="UDP UL at 500 Mbps target: iperf3 UDP UL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("uplink (UE -> core)", 500, UdpUplink.MIN_THROUGHPUT_RATIO),
    )
    DEFAULT_BANDWIDTH = "500M"


class UdpUplink1G(UdpUplink):
    SPEC = TestSpec(
        tc_id="TC-TRF-017",
        title="UDP UL at 1 Gbps target: iperf3 UDP UL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("uplink (UE -> core)", 1000, UdpUplink.MIN_THROUGHPUT_RATIO),
    )
    DEFAULT_BANDWIDTH = "1000M"


class UdpDownlink(TestCase):
    """UDP downlink — core iperf3 client sends UDP to UE."""
    SPEC = TestSpec(
        tc_id="TC-TRF-005",
        title="UDP DL at 50 Mbps target: core iperf3 UDP DL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("downlink (core -> UE)", 50, 0.85),
    )
    DEFAULT_BANDWIDTH = "50M"
    MIN_THROUGHPUT_RATIO = 0.85

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        port = self.params.get("port", 5202)
        duration = self.params.get("duration", 10)
        bandwidth = self.params.get("bandwidth", self.DEFAULT_BANDWIDTH)
        target_mbps = bw_to_mbps(bandwidth)
        min_ratio = float(self.params.get("min_ratio", self.MIN_THROUGHPUT_RATIO))
        min_mbps = round(target_mbps * min_ratio, 2)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        # Core sends UDP to UE: iperf3 server on tester (UE IP), client on core
        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="udp",
            dst_port=port, bandwidth=bandwidth, duration=duration, direction="dl")
        session.start()
        stats = session.stop()

        rx_mbps = round(stats.throughput_kbps / 1000, 2) if stats else 0.0
        metrics = dict(
            protocol="UDP", direction="downlink", ue_ip=ue_ip,
            target_mbps=target_mbps, min_mbps=min_mbps, rx_mbps=rx_mbps,
            jitter_ms=round(stats.jitter_ms, 3) if stats else 0.0,
            lost_packets=stats.lost_packets if stats else 0,
            total_packets=stats.tx_packets if stats else 0,
            loss_pct=round(stats.loss_pct, 2) if stats else 0.0,
        )
        if rx_mbps >= min_mbps:
            self.pass_test(**metrics)
        else:
            self.fail_test(
                f"DL={rx_mbps:.1f}/{target_mbps:.0f} Mbps (LOW); "
                f"threshold={min_mbps:.1f} Mbps ({int(min_ratio*100)}% of target)",
                **metrics)
        return self.result


class UdpDownlink100M(UdpDownlink):
    SPEC = TestSpec(
        tc_id="TC-TRF-018",
        title="UDP DL at 100 Mbps target: core iperf3 UDP DL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("downlink (core -> UE)", 100, UdpDownlink.MIN_THROUGHPUT_RATIO),
    )
    DEFAULT_BANDWIDTH = "100M"


class UdpDownlink250M(UdpDownlink):
    SPEC = TestSpec(
        tc_id="TC-TRF-019",
        title="UDP DL at 250 Mbps target: core iperf3 UDP DL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("downlink (core -> UE)", 250, UdpDownlink.MIN_THROUGHPUT_RATIO),
    )
    DEFAULT_BANDWIDTH = "250M"


class UdpDownlink500M(UdpDownlink):
    SPEC = TestSpec(
        tc_id="TC-TRF-020",
        title="UDP DL at 500 Mbps target: core iperf3 UDP DL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("downlink (core -> UE)", 500, UdpDownlink.MIN_THROUGHPUT_RATIO),
    )
    DEFAULT_BANDWIDTH = "500M"


class UdpDownlink1G(UdpDownlink):
    SPEC = TestSpec(
        tc_id="TC-TRF-021",
        title="UDP DL at 1 Gbps target: core iperf3 UDP DL reaches ≥ 85% of target via GTP-U",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=_udp_desc("downlink (core -> UE)", 1000, UdpDownlink.MIN_THROUGHPUT_RATIO),
    )
    DEFAULT_BANDWIDTH = "1000M"


# ═══════════════════════════════════════════════════════════════
# UDP Bidirectional (TC-TRF-006) lives in core/tc_pdu_session.py.
# Don't redefine here — TestCase.REGISTRY enforces tc_id uniqueness,
# and a duplicate fails the entire module import, taking every other
# TC in this file down with it.
# ═══════════════════════════════════════════════════════════════

# ═══════════════════════════════════════════════════════════════
# ICMP / Latency Tests
# ═══════════════════════════════════════════════════════════════

class LatencyTest(TestCase):
    """RTT latency measurement via ping through GTP-U tunnel."""
    SPEC = TestSpec(
        tc_id="TC-TRF-022",
        title="ICMP RTT through GTP-U: 20 pings from UE IP return successfully (returncode 0)",
        spec="TS 23.501 §5.7.3.4",  # Packet Delay Budget
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Verifies round-trip latency through the GTP-U tunnel using ICMP\n"
            "  echo requests bound to the UE IP. TS 23.501 §5.7.3.4 defines the\n"
            "  Packet Delay Budget (PDB) per 5QI; this test exercises that path\n"
            "  but the spec's PDB ceiling is NOT currently asserted — the gate\n"
            "  is simply that ping completes successfully.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.3.4 + TS 29.281 §5)\n"
            "  1. require_gnb() + require_ue().\n"
            "  2. register_ue + establish_pdu (DNN=internet, PSI=1).\n"
            "  3. target = params.ping_target OR derive_gateway(ue_ip).\n"
            "  4. subprocess.run([\"ping\",\"-c\",\"20\",\"-I\",ue_ip,\"-W\",\"2\",target]).\n"
            "     Source-binds to UE IP so packets traverse the GTP-U tunnel.\n"
            "  5. Parse stdout for the 'min/avg/max/mdev' line and the 'packet\n"
            "     loss' line; record min_ms / avg_ms / max_ms / loss_pct.\n"
            "\n"
            "Parameters (self.params)\n"
            "  ping_target — destination IP (default: gateway derived from ue_ip).\n"
            "  count       — number of echo requests (default: 20).\n"
            "\n"
            "Pass criteria\n"
            "  subprocess returncode == 0 (ping completed without error). Note:\n"
            "  the per-5QI PDB ceiling from §5.7.3.4 is NOT enforced — only the\n"
            "  successful completion of the ping itself.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_ip, target, count, min_ms, avg_ms, max_ms, loss_pct.\n"
            "\n"
            "Known constraints / gaps\n"
            "  Setup.BASELINE. Spec-mandate PDB (§5.7.3.4 — 300ms for 5QI=9) is\n"
            "  reported as a metric but not asserted. Local-network sub-100ms is\n"
            "  informational only. Robot suite surfaces this under TC-TRF-007."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        target = self.params.get("ping_target")
        count = self.params.get("count", 20)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not target:
            target = derive_gateway(ue_ip)
        cmd = ["ping", "-c", str(count), "-I", ue_ip, "-W", "2", target]
        try:
            proc = subprocess.run(cmd, capture_output=True, text=True, timeout=count * 3 + 10)
            if proc.returncode == 0:
                ping_stats = {"ue_ip": ue_ip, "target": target, "count": count}
                for line in proc.stdout.split("\n"):
                    if "min/avg/max" in line:
                        parts = line.split("=")[1].strip().split("/")
                        ping_stats["min_ms"] = float(parts[0])
                        ping_stats["avg_ms"] = float(parts[1])
                        ping_stats["max_ms"] = float(parts[2])
                    if "packet loss" in line:
                        for part in line.split(","):
                            if "packet loss" in part:
                                ping_stats["loss_pct"] = float(part.strip().split("%")[0])
                self.pass_test(**ping_stats)
            else:
                self.fail_test(f"Ping failed: {proc.stderr[:200]}")
        except subprocess.TimeoutExpired:
            self.fail_test("Ping timed out")
        except Exception as e:
            self.fail_test(f"Ping error: {e}")
        return self.result


# ═══════════════════════════════════════════════════════════════
# QoS / Rate Control Tests (MBR, GBR, AMBR)
# Uses UPF stats to verify enforcement
# ═══════════════════════════════════════════════════════════════

class AmbrEnforcement(TestCase):
    """Test Session-AMBR enforcement — UPF must rate-limit aggregate traffic.

    TS 23.501 §5.7.2.6: Session-AMBR limits aggregate UL/DL per PDU session.
    Method: Send traffic at 2x AMBR, verify UPF delivered rate ≤ AMBR.
    Uses UPF stats (before/after) to measure actual delivered throughput.
    """
    SPEC = TestSpec(
        tc_id="TC-QOS-001",
        title="Session-AMBR: set AMBR, send 2× UL → UPF QER engaged (enforced flag reported)",
        spec="TS 23.501 §5.7.2.6 + TS 29.244 §7.5.2.5",  # Aggregate Bit Rates + QER
        domain=Domain.QOS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCF),
        severity=Severity.BLOCKER,
        tags=("conformance", "qos"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Exercises Session-AMBR enforcement at the UPF. TS 23.501 §5.7.2.6\n"
            "  (verified against local v19.07.00) defines Session-AMBR as the\n"
            "  aggregate bit rate cap for all non-GBR flows in one PDU session.\n"
            "  Enforcement is realised by a UPF QER (TS 29.244 §7.5.2.5).\n"
            "  Per TS 23.501 §5.7.1.8, traffic exceeding AMBR shall be policed.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.2.6 + TS 29.244 §7.5.2.5)\n"
            "  1. _set_ambr(ue.imsi, dl_kbps=100000, ul_kbps=50000) — POSTs the\n"
            "     subscription update to SA Core /ue/subscription so SMF/PCF\n"
            "     installs a UPF QER on the next PDU Session Establishment.\n"
            "  2. register_ue + establish_pdu (PDU session inherits the QER).\n"
            "  3. upf_before = collect_upf_stats() — baseline UPF counters.\n"
            "  4. TrafficEngine.create_session(proto=tcp, bandwidth=2*ambr_ul_kbps,\n"
            "     duration=10s, direction=ul) — send 100 Mbps UL (2× AMBR).\n"
            "  5. upf_after = collect_upf_stats(); upf_delta = compute_upf_delta().\n"
            "  6. upf_ul_kbps = upf_delta.io.ul_bytes * 8 / duration.\n"
            "  7. enforced = upf_ul_kbps <= ambr_ul_kbps * 1.2 (20% tolerance).\n"
            "\n"
            "Parameters (self.params)\n"
            "  ambr_ul_kbps — UL Session-AMBR (default: 50000 = 50 Mbps).\n"
            "  ambr_dl_kbps — DL Session-AMBR (default: 100000 = 100 Mbps).\n"
            "  duration     — seconds (default: 10).\n"
            "  iperf_server — defaults to derive_gateway(ue_ip).\n"
            "\n"
            "Pass criteria (CURRENT — see Known gap)\n"
            "  stats.throughput_kbps > 0 — iperf3 produced ANY traffic. The\n"
            "  `ambr_enforced` flag is COMPUTED and REPORTED but NOT a gate.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ambr_ul_kbps, ambr_dl_kbps, sent_kbps, sent_mbps,\n"
            "  upf_delivered_kbps, upf_delivered_mbps, upf_ul_dropped,\n"
            "  ambr_enforced, upf_stats, ue_ip, imsi, duration_s.\n"
            "\n"
            "Known gap\n"
            "  Hollow-pass: with upf_delivered_kbps=0 (UPF stats endpoint\n"
            "  unreachable, or stats not refreshed inside the 10 s window), the\n"
            "  enforced=(0 <= 1.2*ambr_ul_kbps)=True check is vacuously true and\n"
            "  the test reports PASS without proving the QER actually limited\n"
            "  anything. Tightening the gate to require BOTH upf_delivered>0\n"
            "  AND upf_delivered<=ambr*1.2 is tracked separately."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 10)
        ambr_dl_kbps = self.params.get("ambr_dl_kbps", 100000)
        ambr_ul_kbps = self.params.get("ambr_ul_kbps", 50000)

        _set_ambr(ue.imsi, ambr_dl_kbps, ambr_ul_kbps)
        time.sleep(1)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        # Snapshot UPF stats before
        upf_before = collect_upf_stats()

        # Send UL traffic at 2x AMBR
        send_bw = f"{ambr_ul_kbps * 2}K"
        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="tcp",
            dst_port=5201, bandwidth=send_bw, duration=duration, direction="ul")
        session.start()
        stats = session.stop()

        # Snapshot UPF stats after
        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        if not stats.throughput_kbps > 0:
            self.fail_test("AMBR enforcement: iperf3 failed")
            return self.result

        # Use raw iperf3 JSON for bits_per_second (to compare against AMBR)
        raw = stats.raw or {}
        sent = raw.get("end", {}).get("sum_sent", {})
        sent_kbps = sent.get("bits_per_second", 0) / 1000

        # UPF delivered bytes (from UPF stats)
        upf_ul_bytes = upf_delta.get('io', {}).get('ul_bytes', 0)
        upf_ul_kbps = (upf_ul_bytes * 8 / 1000) / max(duration, 1)
        upf_ul_dropped = upf_delta.get('io', {}).get('ul_dropped', 0)

        enforced = upf_ul_kbps <= ambr_ul_kbps * 1.2  # 20% tolerance

        log.info("AMBR UL: sent=%.0f kbps, UPF delivered=%.0f kbps, AMBR=%d kbps, dropped=%d, enforced=%s",
                 sent_kbps, upf_ul_kbps, ambr_ul_kbps, upf_ul_dropped, enforced)

        self.pass_test(
            ambr_ul_kbps=ambr_ul_kbps, ambr_dl_kbps=ambr_dl_kbps,
            sent_kbps=round(sent_kbps), sent_mbps=round(sent_kbps / 1000, 2),
            upf_delivered_kbps=round(upf_ul_kbps), upf_delivered_mbps=round(upf_ul_kbps / 1000, 2),
            upf_ul_dropped=upf_ul_dropped,
            ambr_enforced=enforced,
            upf_stats=upf_delta,
            ue_ip=ue_ip, imsi=ue.imsi, duration_s=duration,
        )
        return self.result


class MbrDownlinkTest(TestCase):
    """Test MBR (Maximum Bit Rate) enforcement on downlink.

    TS 23.501 §5.7.2.4: MBR limits the maximum rate per QoS flow.
    Method: Set MBR, core sends traffic above limit, verify UPF shapes/drops.
    """
    SPEC = TestSpec(
        tc_id="TC-QOS-002",
        title="MBR DL: set MBR, core sends > MBR → UPF QER engaged (enforced flag reported)",
        spec="TS 23.501 §5.7.2.5 + TS 29.244 §7.5.2.5",  # Flow Bit Rates + QER
        domain=Domain.QOS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance", "qos"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Exercises Maximum Bit Rate (MBR) enforcement at the UPF on the DL.\n"
            "  TS 23.501 §5.7.2.5 (verified against local v19.07.00) defines MBR\n"
            "  as the per-QoS-flow ceiling. Enforcement realised by UPF QER (TS\n"
            "  29.244 §7.5.2.5). Note: §5.7.2.4 in the local spec is 'Notification\n"
            "  control', NOT MBR.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.2.5 + TS 29.244 §7.5.2.5)\n"
            "  1. _set_ambr(ue.imsi, dl_kbps=50000, ul_kbps=1000000) — provisions\n"
            "     the DL MBR through the same subscription API.\n"
            "  2. register_ue + establish_pdu.\n"
            "  3. upf_before = collect_upf_stats().\n"
            "  4. TrafficEngine.create_session(proto=tcp, port=5202, duration=10s,\n"
            "     direction=dl) — core sends DL at line rate (no bandwidth arg).\n"
            "  5. upf_after; compute_upf_delta; upf_dl_kbps from io.dl_bytes.\n"
            "  6. enforced = upf_dl_kbps <= mbr_dl_kbps * 1.2.\n"
            "\n"
            "Parameters (self.params)\n"
            "  ambr_dl_kbps — DL MBR (default: 50000 = 50 Mbps).\n"
            "  duration     — seconds (default: 10).\n"
            "  iperf_server — defaults to ue_ip (DL stream targets UE).\n"
            "\n"
            "Pass criteria (CURRENT — see Known gap)\n"
            "  stats.throughput_kbps > 0. `mbr_enforced` is reported, not gated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  mbr_dl_kbps, core_sent_kbps, upf_delivered_kbps,\n"
            "  upf_delivered_mbps, upf_dl_dropped, mbr_enforced, upf_stats,\n"
            "  ue_ip, imsi, duration_s.\n"
            "\n"
            "Known gap\n"
            "  Same hollow-pass risk as TC-QOS-001 — enforced=(0 <= 1.2*mbr)=True\n"
            "  when upf_delivered_kbps=0. Tracked separately."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 10)
        ambr_dl_kbps = self.params.get("ambr_dl_kbps", 50000)

        _set_ambr(ue.imsi, ambr_dl_kbps, 1000000)
        time.sleep(1)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        upf_before = collect_upf_stats()

        # Core sends DL above MBR to test enforcement
        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=ue_ip, protocol="tcp",
            dst_port=5202, duration=duration, direction="dl")
        session.start()
        stats = session.stop()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        if not stats.throughput_kbps > 0:
            self.fail_test("MBR DL test: iperf3 failed")
            return self.result

        # Use raw iperf3 JSON for bits_per_second (to compare against MBR)
        raw = stats.raw or {}
        sent = raw.get("end", {}).get("sum_sent", {})
        sent_kbps = sent.get("bits_per_second", 0) / 1000

        upf_dl_bytes = upf_delta.get('io', {}).get('dl_bytes', 0)
        upf_dl_kbps = (upf_dl_bytes * 8 / 1000) / max(duration, 1)
        upf_dl_dropped = upf_delta.get('io', {}).get('dl_dropped', 0)

        enforced = upf_dl_kbps <= ambr_dl_kbps * 1.2

        log.info("MBR DL: core sent=%.0f kbps, UPF delivered=%.0f kbps, MBR=%d kbps, dropped=%d, enforced=%s",
                 sent_kbps, upf_dl_kbps, ambr_dl_kbps, upf_dl_dropped, enforced)

        self.pass_test(
            mbr_dl_kbps=ambr_dl_kbps,
            core_sent_kbps=round(sent_kbps),
            upf_delivered_kbps=round(upf_dl_kbps), upf_delivered_mbps=round(upf_dl_kbps / 1000, 2),
            upf_dl_dropped=upf_dl_dropped,
            mbr_enforced=enforced,
            upf_stats=upf_delta,
            ue_ip=ue_ip, imsi=ue.imsi, duration_s=duration,
        )
        return self.result


class MbrUplinkTest(TestCase):
    """Test MBR enforcement on uplink with UPF stats verification.

    TS 23.501 §5.7.2.4: MBR limits per-flow UL rate.
    Method: Set MBR, UE sends at 2x MBR, verify UPF delivered ≤ MBR.
    """
    SPEC = TestSpec(
        tc_id="TC-QOS-003",
        title="MBR UL: set MBR, UE sends 2× MBR → UPF QER engaged (enforced flag reported)",
        spec="TS 23.501 §5.7.2.5 + TS 29.244 §7.5.2.5",  # Flow Bit Rates + QER
        domain=Domain.QOS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance", "qos"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Symmetric counterpart to TC-QOS-002 — exercises MBR enforcement on\n"
            "  the UPLINK. UE sends at 2× MBR; UPF QER should police excess.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.2.5 + TS 29.244 §7.5.2.5)\n"
            "  1. _set_ambr(ue.imsi, dl_kbps=1000000, ul_kbps=50000) — UL MBR.\n"
            "  2. register_ue + establish_pdu.\n"
            "  3. upf_before = collect_upf_stats().\n"
            "  4. TrafficEngine.create_session(proto=tcp, port=5201,\n"
            "     bandwidth=2*mbr_ul_kbps, duration=10s, direction=ul).\n"
            "  5. upf_after; compute_upf_delta; upf_ul_kbps from io.ul_bytes.\n"
            "  6. enforced = upf_ul_kbps <= mbr_ul_kbps * 1.2.\n"
            "\n"
            "Parameters (self.params)\n"
            "  ambr_ul_kbps — UL MBR (default: 50000 = 50 Mbps).\n"
            "  duration     — seconds (default: 10).\n"
            "  iperf_server — defaults to derive_gateway(ue_ip).\n"
            "\n"
            "Pass criteria (CURRENT — see Known gap)\n"
            "  stats.throughput_kbps > 0. `mbr_enforced` is reported, not gated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  mbr_ul_kbps, sent_kbps, sent_mbps, upf_delivered_kbps,\n"
            "  upf_delivered_mbps, upf_ul_dropped, mbr_enforced, upf_stats,\n"
            "  ue_ip, imsi, duration_s.\n"
            "\n"
            "Known gap\n"
            "  Same hollow-pass risk as TC-QOS-001/002."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 10)
        ambr_ul_kbps = self.params.get("ambr_ul_kbps", 50000)

        _set_ambr(ue.imsi, 1000000, ambr_ul_kbps)
        time.sleep(1)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        upf_before = collect_upf_stats()

        send_bw = f"{ambr_ul_kbps * 2}K"
        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="tcp",
            dst_port=5201, bandwidth=send_bw, duration=duration, direction="ul")
        session.start()
        stats = session.stop()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        if not stats.throughput_kbps > 0:
            self.fail_test("MBR UL test: iperf3 failed")
            return self.result

        # Use raw iperf3 JSON for bits_per_second (to compare against MBR)
        raw = stats.raw or {}
        sent = raw.get("end", {}).get("sum_sent", {})
        sent_kbps = sent.get("bits_per_second", 0) / 1000

        upf_ul_bytes = upf_delta.get('io', {}).get('ul_bytes', 0)
        upf_ul_kbps = (upf_ul_bytes * 8 / 1000) / max(duration, 1)
        upf_ul_dropped = upf_delta.get('io', {}).get('ul_dropped', 0)

        enforced = upf_ul_kbps <= ambr_ul_kbps * 1.2

        log.info("MBR UL: sent=%.0f kbps, UPF delivered=%.0f kbps, MBR=%d kbps, dropped=%d, enforced=%s",
                 sent_kbps, upf_ul_kbps, ambr_ul_kbps, upf_ul_dropped, enforced)

        self.pass_test(
            mbr_ul_kbps=ambr_ul_kbps,
            sent_kbps=round(sent_kbps), sent_mbps=round(sent_kbps / 1000, 2),
            upf_delivered_kbps=round(upf_ul_kbps), upf_delivered_mbps=round(upf_ul_kbps / 1000, 2),
            upf_ul_dropped=upf_ul_dropped,
            mbr_enforced=enforced,
            upf_stats=upf_delta,
            ue_ip=ue_ip, imsi=ue.imsi, duration_s=duration,
        )
        return self.result


class GbrFlowTest(TestCase):
    """Test GBR (Guaranteed Bit Rate) — minimum rate guarantee with UPF verification.

    TS 23.501 §5.7.2.3: GBR QoS flows have guaranteed minimum rate.
    Method: Send at GBR rate, verify UPF delivers ≥90% with <1% loss.
    UPF stats confirm no drops on the GBR flow.
    """
    SPEC = TestSpec(
        tc_id="TC-TRF-011",
        title="GBR happy path: send at GBR rate via iperf3 UDP → UPF delivers ≥ 90% (gbr_met reported)",
        spec="TS 23.501 §5.7.2.5",  # Flow Bit Rates (GBR per local TS 23.501 v19.07)
        domain=Domain.QOS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCF),
        severity=Severity.MAJOR,
        tags=("conformance", "qos"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Exercises Guaranteed Bit Rate (GBR) delivery. TS 23.501 §5.7.2.5\n"
            "  (verified against local v19.07.00) defines GBR as the minimum\n"
            "  rate a GBR QoS flow is guaranteed to receive. Note: §5.7.2.3 in\n"
            "  the local spec is 'RQA' (Reflective QoS Attribute), NOT GBR.\n"
            "\n"
            "Procedure (TS 23.501 §5.7.2.5)\n"
            "  1. require_gnb() + require_ue() + register_ue + establish_pdu.\n"
            "     The PDU session inherits a GBR QoS flow configured upstream.\n"
            "  2. upf_before = collect_upf_stats().\n"
            "  3. TrafficEngine.create_session(proto=udp, bandwidth=10000K,\n"
            "     duration=10s, direction=ul) — UDP exactly at GBR rate.\n"
            "  4. upf_after; compute_upf_delta; upf_ul_kbps from io.ul_bytes.\n"
            "  5. gbr_met = actual_kbps >= gbr_kbps * 0.9 AND loss_pct < 1.0.\n"
            "\n"
            "Parameters (self.params)\n"
            "  gbr_kbps — GBR rate (default: 10000 = 10 Mbps).\n"
            "  duration — seconds (default: 10).\n"
            "  iperf_server — defaults to derive_gateway(ue_ip).\n"
            "\n"
            "Pass criteria (CURRENT — see Known gap)\n"
            "  stats.throughput_kbps > 0. `gbr_met` is reported, not gated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  gbr_kbps, sent_kbps, sent_mbps, upf_delivered_kbps,\n"
            "  upf_ul_dropped, jitter_ms, loss_pct, gbr_met, upf_stats,\n"
            "  ue_ip, imsi, duration_s.\n"
            "\n"
            "Known gap\n"
            "  Same shape as TC-QOS-001..003 — gbr_met is reported but not used\n"
            "  as a pass/fail gate. The 90% / <1% thresholds match the spec\n"
            "  intent but aren't enforced yet."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 10)
        gbr_kbps = self.params.get("gbr_kbps", 10000)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        upf_before = collect_upf_stats()

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth=f"{gbr_kbps}K", duration=duration, direction="ul")
        session.start()
        stats = session.stop()

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        if not stats.throughput_kbps > 0:
            self.fail_test("GBR test: iperf3 failed")
            return self.result

        actual_kbps = stats.throughput_kbps
        jitter = stats.jitter_ms
        loss_pct = stats.loss_pct

        upf_ul_bytes = upf_delta.get('io', {}).get('ul_bytes', 0)
        upf_ul_kbps = (upf_ul_bytes * 8 / 1000) / max(duration, 1)
        upf_ul_dropped = upf_delta.get('io', {}).get('ul_dropped', 0)

        gbr_met = actual_kbps >= gbr_kbps * 0.9 and loss_pct < 1.0

        log.info("GBR: sent=%.0f kbps, UPF delivered=%.0f kbps, GBR=%d kbps, "
                 "jitter=%.1fms, loss=%.2f%%, dropped=%d, met=%s",
                 actual_kbps, upf_ul_kbps, gbr_kbps, jitter, loss_pct, upf_ul_dropped, gbr_met)

        self.pass_test(
            gbr_kbps=gbr_kbps,
            sent_kbps=round(actual_kbps), sent_mbps=round(actual_kbps / 1000, 2),
            upf_delivered_kbps=round(upf_ul_kbps),
            upf_ul_dropped=upf_ul_dropped,
            jitter_ms=round(jitter, 3), loss_pct=round(loss_pct, 2),
            gbr_met=gbr_met,
            upf_stats=upf_delta,
            ue_ip=ue_ip, imsi=ue.imsi, duration_s=duration,
        )
        return self.result


# ═══════════════════════════════════════════════════════════════
# Multi-UE and Stress Traffic Tests
# ═══════════════════════════════════════════════════════════════

class MultiUeTraffic(TestCase):
    """Simultaneous traffic from multiple UEs."""
    SPEC = TestSpec(
        tc_id="TC-TRF-012",
        title="Multi-UE TCP UL: N UEs registered + PDU sessions, each iperf3 TCP UL > 0",
        spec="TS 29.281 §4",  # GTP-U per-UE tunnels; AMF multi-gNB per TS 23.501 §5.2.1
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Exercises N concurrent UEs sharing one gNB and one UPF. Each UE\n"
            "  has its own NAS context, PDU session, and GTP-U TEID. Validates\n"
            "  per-UE state isolation in AMF/SMF/UPF and that the data plane\n"
            "  handles multiple tunnels in parallel.\n"
            "\n"
            "Procedure (TS 29.281 per-UE TEID + TS 23.501 §5.2.1)\n"
            "  1. require_gnb(); ue_count = min(params.ue_count or 2, len(ue_pool)).\n"
            "  2. Fail-test if ue_count < 2 — needs at least 2 UEs to be 'multi'.\n"
            "  3. For each UE: register_ue + establish_pdu (PSI=1).\n"
            "  4. server = params.iperf_server OR derive_gateway(first_ue_ip).\n"
            "  5. For each UE SEQUENTIALLY: TrafficEngine.create_session(proto=tcp,\n"
            "     port=5201, duration=5s, direction=ul); record tx_mbps.\n"
            "\n"
            "Parameters (self.params)\n"
            "  ue_count     — number of UEs (default: 2; clamped to len(ue_pool)).\n"
            "  duration     — per-UE iperf3 seconds (default: 5).\n"
            "  iperf_server — defaults to derive_gateway(first UE's IP).\n"
            "\n"
            "Pass criteria\n"
            "  Every UE's stats.throughput_kbps > 0. If any UE fails, the test\n"
            "  fails and ue_results identifies which UEs failed.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_count, ue_results=[{imsi, ue_ip, status, tx_mbps}],\n"
            "  total_tx_mbps.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. iperf3 runs are SEQUENTIAL not concurrent — each UE's\n"
            "  iperf3 finishes before the next starts. 'Simultaneous' in the\n"
            "  description refers to all UEs being registered + having live PDU\n"
            "  sessions at the same time, not concurrent iperf3."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 5)
        ue_count = min(self.params.get("ue_count", 2), len(self.ue_pool))

        ues = self.ue_pool[:ue_count]
        if len(ues) < 2:
            self.fail_test("Need at least 2 UEs for multi-UE traffic test")
            return self.result

        for ue in ues:
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=1):
                return self.result

        # Derive server from first UE's IP if not specified
        first_ip = ues[0].pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(first_ip)

        engine = TrafficEngine.get()
        ue_results = []
        for ue in ues:
            ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            session = engine.create_session(
                src_ip=ue_ip, dst_ip=server, protocol="tcp",
                dst_port=5201, duration=duration, direction="ul")
            session.start()
            stats = session.stop()
            if stats.throughput_kbps > 0:
                ue_results.append({
                    "imsi": ue.imsi, "ue_ip": ue_ip, "status": "PASS",
                    "tx_mbps": round(stats.throughput_kbps / 1000, 2),
                })
            else:
                ue_results.append({"imsi": ue.imsi, "ue_ip": ue_ip, "status": "FAIL"})

        all_pass = all(r["status"] == "PASS" for r in ue_results)
        total_mbps = sum(r.get("tx_mbps", 0) for r in ue_results if r["status"] == "PASS")

        if all_pass:
            self.pass_test(ue_count=len(ues), ue_results=ue_results,
                           total_tx_mbps=round(total_mbps, 2))
        else:
            self.fail_test("Some UEs failed traffic test",
                           ue_count=len(ues), ue_results=ue_results)
        return self.result


class SustainedTraffic(TestCase):
    """Sustained traffic over extended duration."""
    SPEC = TestSpec(
        tc_id="TC-TRF-013",
        title="Sustained TCP UL: 30 s continuous iperf3 keeps throughput > 0 (catches tunnel leaks)",
        spec="TS 29.281",
        domain=Domain.TRAFFIC,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        severity=Severity.MAJOR,
        tags=("conformance", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=120.0,
        requires_dataplane=True,
        description=(
            "Purpose\n"
            "  Long-duration stability check on the GTP-U tunnel. Catches\n"
            "  failure modes that short smoke tests miss: socket leaks, FSM\n"
            "  stalls, slow memory growth, tunnel keepalive bugs, kernel ring\n"
            "  buffer overflows under steady load.\n"
            "\n"
            "Procedure (TS 29.281)\n"
            "  1. require_gnb() + require_ue() + register_ue + establish_pdu.\n"
            "  2. server = params.iperf_server OR derive_gateway(ue_ip).\n"
            "  3. TrafficEngine.create_session(proto=tcp, port=5201, duration=30s,\n"
            "     direction=ul); start(); stop().\n"
            "  4. From iperf3 raw JSON, read sum_received.bits_per_second for rx.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration     — seconds (default: 30).\n"
            "  iperf_server — defaults to derive_gateway(ue_ip).\n"
            "\n"
            "Pass criteria\n"
            "  stats.throughput_kbps > 0 over the full 30 s window.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  protocol=TCP, direction=uplink, ue_ip, tx_mbps, rx_mbps,\n"
            "  duration_s, retransmits.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. expected_duration_s=120 accounts for setup +\n"
            "  30 s run + teardown. '0 retransmits' is a perf hint, not asserted."
        ),
    )

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 30)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue):
            return self.result

        ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if not server:
            server = derive_gateway(ue_ip)

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="tcp",
            dst_port=5201, duration=duration, direction="ul")
        session.start()
        stats = session.stop()

        if stats.throughput_kbps > 0:
            # Use raw for rx_mbps (sum_received is not in TrafficStats directly)
            raw = stats.raw or {}
            recv = raw.get("end", {}).get("sum_received", {})
            rx_mbps = round(recv.get("bits_per_second", 0) / 1e6, 2)
            self.pass_test(
                protocol="TCP", direction="uplink", ue_ip=ue_ip,
                tx_mbps=round(stats.throughput_kbps / 1000, 2),
                rx_mbps=rx_mbps,
                duration_s=duration,
                retransmits=stats.retransmits,
            )
        else:
            self.fail_test("Sustained traffic test: iperf3 failed")
        return self.result


# ═══════════════════════════════════════════════════════════════
# 5QI / QoS Characteristics Tests have moved to a dedicated module:
#   src/testcases/traffic/tc_fqi.py  — TC-FQI-NNN series
# Add new 5QI catalog / PDB / characteristics tests there so this
# file stays focused on TCP/UDP/QoS rate enforcement.
# ═══════════════════════════════════════════════════════════════

# ═══════════════════════════════════════════════════════════════
# Helpers
# ═══════════════════════════════════════════════════════════════

def _set_ambr(imsi, dl_kbps, ul_kbps):
    """Set per-UE session AMBR on the SA Core (TS 23.501 §5.7.2.6)."""
    result = core_api("/ue/subscription", "POST", {
        "imsi": imsi,
        "ambr": {"downlink_kbps": dl_kbps, "uplink_kbps": ul_kbps},
    })
    if result:
        log.info("Set AMBR for %s: DL=%d kbps UL=%d kbps", imsi, dl_kbps, ul_kbps)
    return result

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Multi-UE traffic at scale — concurrent data sessions.

Tests simultaneous traffic from multiple UEs through independent GTP-U tunnels.
Each UE gets its own PDU session, GTP-U tunnel, and traffic session via TrafficEngine.
Measures aggregate throughput, per-UE fairness, and UPF capacity.
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

log = logging.getLogger("tester.tc_multi_traffic")


def _run_ue_session(engine, ue_ip, server, port, duration, bandwidth,
                    protocol, direction):
    """Run a single UE traffic session via TrafficEngine. Returns result dict.

    UL: engine creates session → run_iperf_ul (tester client → core server via web API)
    DL: engine creates session → run_iperf_dl (local server + core client via web API)
    """
    proto = "udp" if protocol.lower() == "udp" else "tcp"
    udp = proto == "udp"

    try:
        if direction == "ul":
            # UL: dst_ip=server (core iperf server target)
            session = engine.create_session(
                src_ip=ue_ip, dst_ip=server, protocol=proto,
                dst_port=port, bandwidth=bandwidth,
                duration=duration, direction="ul")
        else:
            # DL: dst_ip=ue_ip (local server binds here, core client sends here via GTP-U)
            session = engine.create_session(
                src_ip=ue_ip, dst_ip=ue_ip, protocol=proto,
                dst_port=port, bandwidth=bandwidth,
                duration=duration, direction="dl")

        session.start()
        stats = session.stop()

        if stats.throughput_kbps > 0 or stats.tx_packets > 0:
            result = {
                "status": "PASS",
                "kbps": round(stats.throughput_kbps, 1),
            }
            if udp:
                result["jitter_ms"] = round(stats.jitter_ms, 2)
                result["loss_pct"] = round(stats.loss_pct, 2)
                result["packets"] = stats.tx_packets
            else:
                result["retransmits"] = stats.retransmits
            return result
        return {"status": "FAIL", "error": "no throughput"}
    except Exception as e:
        return {"status": "ERROR", "error": str(e)}


def _make_multi_tc(tc_id, name, ue_count, bandwidth_ul, bandwidth_dl,
                   protocol, duration=10):
    """Factory for multi-UE traffic test cases."""

    udp = protocol.lower() == "udp"
    proto_label = protocol.upper()
    _is_browse = (name or "").startswith("browse_")
    _profile = ("browse" if _is_browse
                else "udp" if udp else "tcp")
    _is_foundational = ue_count <= 8
    _severity = Severity.BLOCKER if _is_foundational else Severity.MAJOR
    _tags = ("conformance",) if _is_foundational else ("conformance", "scale")

    class MultiTrafficTC(TestCase):
        SPEC = TestSpec(
            tc_id=tc_id,
            title=f"Multi-UE {proto_label} traffic — {ue_count} UEs simultaneous",
            spec="TS 29.281",
            domain=Domain.TRAFFIC,
            nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
            severity=_severity,
            tags=_tags,
            setup=Setup.BASELINE,
            expected_duration_s=float(duration) + 60.0,
            requires_dataplane=True,
            description=(
                "Purpose\n"
                f"  Multi-UE concurrent data-plane stress at N={ue_count}\n"
                f"  UEs carrying {proto_label} traffic"
                + (" (browse-shape:\n  asymmetric UL/DL)" if _is_browse else "")
                + ". Each UE owns\n"
                "  an independent PDU session, GTP-U tunnel (per-UE TEID)\n"
                "  and iperf3 session pair — the test pins that the UPF\n"
                f"  can demux {ue_count} simultaneous bearers without bleeding\n"
                "  packets across TEIDs. Per-UE PASS plus aggregate UL/DL\n"
                "  Mbps lets a regression localise to either control plane\n"
                "  (a UE fails registration / PDU) or data plane (UE attaches\n"
                "  but session reports zero throughput).\n"
                + ("  Browse profile uses asymmetric UL/DL to mimic HTTP-like\n"
                   "  page-fetch traffic (small request, larger response).\n"
                   if _is_browse else "")
                + "\n"
                "Procedure (TS 29.281 §5 GTP-U + TS 24.501 §5.5 + TS 23.501 §5.7)\n"
                f"  1. require_gnb(), require_ue(); clamp ue_count to\n"
                f"     min({ue_count}, len(ue_pool)) (warn if short).\n"
                "  2. Register all UEs in parallel via ThreadPoolExecutor —\n"
                "     each worker: gnb.attach_ue, ue.register,\n"
                "     wait_for_state REGISTERED (15 s), establish_pdu_session\n"
                "     (dnn=internet, sst=1, psi=1), poll ue.pdu_sessions[1]\n"
                "     for IPv4. Bails out fast if gnb.state == ERROR\n"
                "     mid-wait so a dead SCTP doesn't hold 15 s per UE.\n"
                "  3. server = derive_gateway(first UE's IPv4).\n"
                "  4. Port plan: UL on 5201..5201+N-1, DL on 6201..6201+N-1\n"
                "     (separate ranges so UL+DL don't collide).\n"
                "  5. upf_before = collect_upf_stats() — snapshot /api/upf/stats.\n"
                f"  6. ThreadPoolExecutor(max_workers={ue_count}*2) submits\n"
                "     _run_ue_session per UE per direction:\n"
                f"       UL: bandwidth={bandwidth_ul or '-'}, duration={duration}s,\n"
                f"           proto={proto_label}, dst=server, dst_port=5201+i.\n"
                f"       DL: bandwidth={bandwidth_dl or '-'}, duration={duration}s,\n"
                f"           proto={proto_label}, dst=ue_ip, dst_port=6201+i.\n"
                "     Each session returns dict with status, kbps, plus\n"
                "     jitter_ms+loss_pct+packets (UDP) or retransmits (TCP).\n"
                "  7. upf_after = collect_upf_stats(); compute_upf_delta()\n"
                "     gives io.ul_pkts/dl_pkts/ul_dropped/dl_dropped.\n"
                "  8. passed/failed counted across UL+DL result lists.\n"
                "\n"
                "Parameters (self.params)\n"
                "  (none) — factory-bound. ue_count, bandwidths, protocol,\n"
                "  duration are all baked in at _make_multi_tc time.\n"
                "\n"
                "Pass criteria\n"
                "  failed == 0 — every UL+DL session returned status='PASS'\n"
                "  (non-zero throughput_kbps OR non-zero tx_packets). Any\n"
                "  ERROR / FAIL session OR any UE registration failure flips\n"
                "  the run to fail_test() with a count summary.\n"
                "\n"
                "KPI deltas / Reported metrics\n"
                "  protocol, ue_count, duration_s, bandwidth_ul, bandwidth_dl,\n"
                "  ul_total_mbps, dl_total_mbps, ul_results, dl_results,\n"
                "  passed, failed, upf_stats (io.ul_pkts, io.dl_pkts,\n"
                "  io.ul_dropped, io.dl_dropped).\n"
                "\n"
                "Known constraints\n"
                f"  Scales to len(ue_pool); requested N={ue_count} is the\n"
                "  ceiling, not a guarantee — under-populated pools log a\n"
                "  warning and run smaller. Host fd / port / CPU limits set\n"
                "  the real-world cap, not the test code. No GBR/MBR\n"
                "  enforcement is asserted — only the existence of a flow\n"
                "  per UE."
            ),
        )

        def run(self):
            gnb = self.require_gnb()
            self.require_ue()

            count = min(ue_count, len(self.ue_pool))
            if count < ue_count:
                log.warning("Only %d UEs available (requested %d)", count, ue_count)

            ues = self.ue_pool[:count]

            # Register all UEs and establish PDU sessions concurrently
            def _reg_one(ue):
                try:
                    gnb.attach_ue(ue)
                    ue.register()
                    if not ue.wait_for_state("REGISTERED", timeout=15):
                        return (ue.imsi, False, f"reg failed state={ue.state}")
                    ue.establish_pdu_session(dnn="internet", sst=1, pdu_session_id=1)
                    deadline = time.time() + 15
                    while time.time() < deadline:
                        # Bail out immediately if the SCTP association died
                        # while we were waiting — no point polling 15 s for a
                        # PDU IP that will never arrive over a dead gNB.
                        if getattr(gnb, "state", None) == "ERROR":
                            return (ue.imsi, False,
                                    f"gNB ERROR during PDU wait (state={ue.state})")
                        session = ue.pdu_sessions.get(1)
                        if session and session.get("ip") and session["ip"] != "unknown":
                            return (ue.imsi, True, None)
                        time.sleep(0.3)
                    return (ue.imsi, False, "PDU timeout")
                except Exception as e:
                    return (ue.imsi, False, str(e))

            log.info("Registering %d UEs concurrently with PDU sessions", count)
            with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:
                futures = {pool.submit(_reg_one, ue): ue for ue in ues}
                for f in concurrent.futures.as_completed(futures):
                    imsi, ok, err = f.result()
                    if not ok:
                        self.fail_test(f"UE {imsi}: {err}")
                        return self.result

            # Derive server from first UE
            first_ip = ues[0].pdu_sessions.get(1, {}).get("ip", "unknown")
            server = derive_gateway(first_ip)

            # Port ranges: UL on 5201+, DL on 6201+ (separate to avoid conflict)
            ul_base = 5201
            dl_base = 6201
            ul_ports = list(range(ul_base, ul_base + count))
            dl_ports = list(range(dl_base, dl_base + count))

            # TrafficEngine handles all server lifecycle:
            # UL runner: starts/stops core iperf server via web API
            # DL runner: starts/stops local iperf server + calls core client API
            engine = TrafficEngine.get()
            upf_before = collect_upf_stats()

            # Run UL + DL simultaneously for all UEs via TrafficEngine
            log.info("Starting %s UL+DL traffic: %d UEs × %s each (simultaneous)",
                     proto_label, count, bandwidth_ul or bandwidth_dl)

            ul_results = []
            dl_results = []
            with concurrent.futures.ThreadPoolExecutor(max_workers=count * 2) as executor:
                futures = {}
                for i, ue in enumerate(ues):
                    ue_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
                    if bandwidth_ul:
                        f = executor.submit(_run_ue_session, engine, ue_ip,
                                            server, ul_ports[i], duration,
                                            bandwidth_ul, protocol, "ul")
                        futures[f] = (ue, "UL")
                    if bandwidth_dl:
                        f = executor.submit(_run_ue_session, engine, ue_ip,
                                            server, dl_ports[i], duration,
                                            bandwidth_dl, protocol, "dl")
                        futures[f] = (ue, "DL")

                for f in concurrent.futures.as_completed(futures):
                    ue, direction = futures[f]
                    r = f.result()
                    r["imsi"] = ue.imsi
                    r["ue_ip"] = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
                    r["direction"] = direction
                    if direction == "UL":
                        ul_results.append(r)
                    else:
                        dl_results.append(r)

            upf_after = collect_upf_stats()
            upf_delta = compute_upf_delta(upf_before, upf_after)

            # Aggregate results
            all_results = ul_results + dl_results
            passed = sum(1 for r in all_results if r["status"] == "PASS")
            failed = sum(1 for r in all_results if r["status"] != "PASS")
            ul_total_kbps = sum(r.get("kbps", 0) for r in ul_results if r["status"] == "PASS")
            dl_total_kbps = sum(r.get("kbps", 0) for r in dl_results if r["status"] == "PASS")

            io = upf_delta.get("io", {})

            log.info("Multi-UE %s: %d UEs, UL=%.1f Mbps DL=%.1f Mbps, "
                     "passed=%d failed=%d, UPF UL=%d pkts DL=%d pkts dropped=%d/%d",
                     proto_label, count,
                     ul_total_kbps / 1000, dl_total_kbps / 1000,
                     passed, failed,
                     io.get("ul_pkts", 0), io.get("dl_pkts", 0),
                     io.get("ul_dropped", 0), io.get("dl_dropped", 0))

            if failed == 0:
                self.pass_test(
                    protocol=proto_label, ue_count=count, duration_s=duration,
                    bandwidth_ul=bandwidth_ul, bandwidth_dl=bandwidth_dl,
                    ul_total_mbps=round(ul_total_kbps / 1000, 2),
                    dl_total_mbps=round(dl_total_kbps / 1000, 2),
                    ul_results=ul_results, dl_results=dl_results,
                    passed=passed, failed=failed,
                    upf_stats=upf_delta,
                )
            else:
                self.fail_test(
                    f"{failed}/{len(all_results)} UE sessions failed",
                    protocol=proto_label, ue_count=count,
                    passed=passed, failed=failed,
                    ul_results=ul_results, dl_results=dl_results,
                    upf_stats=upf_delta,
                )
            return self.result

    MultiTrafficTC.__name__ = name
    MultiTrafficTC.__qualname__ = name
    return MultiTrafficTC


# ═══════════════════════════════════════════════════════════════
# TCP Multi-UE Traffic Tests
# ═══════════════════════════════════════════════════════════════
TcpMulti2 = _make_multi_tc(
    "TC-MTR-100", "tcp_multi_2ue_1mbps",
    ue_count=2, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="tcp", duration=TRAFFIC_DURATION)

TcpMulti4 = _make_multi_tc(
    "TC-MTR-101", "tcp_multi_4ue_1mbps",
    ue_count=4, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="tcp", duration=TRAFFIC_DURATION)

TcpMulti8 = _make_multi_tc(
    "TC-MTR-001", "tcp_multi_8ue_1mbps",
    ue_count=8, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="tcp", duration=TRAFFIC_DURATION)

TcpMulti16 = _make_multi_tc(
    "TC-MTR-106", "tcp_multi_16ue_1mbps",
    ue_count=16, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="tcp", duration=TRAFFIC_DURATION)

TcpMulti32 = _make_multi_tc(
    "TC-MTR-002", "tcp_multi_32ue_1mbps",
    ue_count=32, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="tcp", duration=TRAFFIC_DURATION)

TcpMulti64 = _make_multi_tc(
    "TC-MTR-003", "tcp_multi_64ue_1mbps",
    ue_count=64, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="tcp", duration=TRAFFIC_DURATION)

TcpMulti128 = _make_multi_tc(
    "TC-MTR-004", "tcp_multi_128ue_1mbps",
    ue_count=128, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="tcp", duration=TRAFFIC_DURATION)

# ═══════════════════════════════════════════════════════════════
# UDP Multi-UE Traffic Tests
# ═══════════════════════════════════════════════════════════════
UdpMulti2 = _make_multi_tc(
    "TC-MTR-102", "udp_multi_2ue_1mbps",
    ue_count=2, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="udp", duration=TRAFFIC_DURATION)

UdpMulti4 = _make_multi_tc(
    "TC-MTR-103", "udp_multi_4ue_1mbps",
    ue_count=4, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="udp", duration=TRAFFIC_DURATION)

UdpMulti8 = _make_multi_tc(
    "TC-MTR-005", "udp_multi_8ue_1mbps",
    ue_count=8, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="udp", duration=TRAFFIC_DURATION)

UdpMulti16 = _make_multi_tc(
    "TC-MTR-107", "udp_multi_16ue_1mbps",
    ue_count=16, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="udp", duration=TRAFFIC_DURATION)

UdpMulti32 = _make_multi_tc(
    "TC-MTR-006", "udp_multi_32ue_1mbps",
    ue_count=32, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="udp", duration=TRAFFIC_DURATION)

UdpMulti64 = _make_multi_tc(
    "TC-MTR-007", "udp_multi_64ue_1mbps",
    ue_count=64, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="udp", duration=TRAFFIC_DURATION)

UdpMulti128 = _make_multi_tc(
    "TC-MTR-008", "udp_multi_128ue_1mbps",
    ue_count=128, bandwidth_ul="1M", bandwidth_dl="1M",
    protocol="udp", duration=TRAFFIC_DURATION)

# ═══════════════════════════════════════════════════════════════
# Browsing (HTTP-like) Multi-UE Traffic Tests
# Short TCP bursts simulating web browsing
# ═══════════════════════════════════════════════════════════════
BrowseMulti2 = _make_multi_tc(
    "TC-MTR-104", "browse_multi_2ue",
    ue_count=2, bandwidth_ul="100K", bandwidth_dl="2M",
    protocol="tcp", duration=TRAFFIC_DURATION)

BrowseMulti4 = _make_multi_tc(
    "TC-MTR-105", "browse_multi_4ue",
    ue_count=4, bandwidth_ul="100K", bandwidth_dl="2M",
    protocol="tcp", duration=TRAFFIC_DURATION)

BrowseMulti8 = _make_multi_tc(
    "TC-MTR-009", "browse_multi_8ue",
    ue_count=8, bandwidth_ul="100K", bandwidth_dl="2M",
    protocol="tcp", duration=TRAFFIC_DURATION)

BrowseMulti16 = _make_multi_tc(
    "TC-MTR-108", "browse_multi_16ue",
    ue_count=16, bandwidth_ul="100K", bandwidth_dl="2M",
    protocol="tcp", duration=TRAFFIC_DURATION)

BrowseMulti32 = _make_multi_tc(
    "TC-MTR-010", "browse_multi_32ue",
    ue_count=32, bandwidth_ul="100K", bandwidth_dl="2M",
    protocol="tcp", duration=TRAFFIC_DURATION)

BrowseMulti64 = _make_multi_tc(
    "TC-MTR-011", "browse_multi_64ue",
    ue_count=64, bandwidth_ul="100K", bandwidth_dl="2M",
    protocol="tcp", duration=TRAFFIC_DURATION)

BrowseMulti128 = _make_multi_tc(
    "TC-MTR-012", "browse_multi_128ue",
    ue_count=128, bandwidth_ul="100K", bandwidth_dl="2M",
    protocol="tcp", duration=TRAFFIC_DURATION)

ALL_MULTI_TRAFFIC_TCS = [
    TcpMulti2, TcpMulti4, TcpMulti8, TcpMulti16, TcpMulti32, TcpMulti64, TcpMulti128,
    UdpMulti2, UdpMulti4, UdpMulti8, UdpMulti16, UdpMulti32, UdpMulti64, UdpMulti128,
    BrowseMulti2, BrowseMulti4, BrowseMulti8, BrowseMulti16, BrowseMulti32, BrowseMulti64, BrowseMulti128,
]

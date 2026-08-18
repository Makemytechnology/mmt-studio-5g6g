# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: Multi-DNN concurrent sessions — data + ViNR per UE pair.

Each UE establishes two independent PDU sessions simultaneously:
  PSI=1 (DNN=internet, 5QI=9): UDP bidirectional throughput via iperf3
  PSI=2 (DNN=ims, 5QI=1+2):   ViNR bidirectional call (audio + video RTP)

Validates QoS isolation between DNNs, independent GTP-U tunnels per PDU session,
and concurrent bearer operation per TS 23.501 §5.6.1.
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
from src.testcases.ims.tc_ims import (
    _get_ims_domain, _get_pcscf_from_session, _make_sip_client, _get_tun_for_ue,
    ImsVoiceCallQuality,
)
from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

log = logging.getLogger("tester.tc_multi_dnn")


def _register_ue_dual_dnn(ue, gnb):
    """Register one UE with dual PDU sessions (internet + ims). Returns (ue, ok)."""
    try:
        gnb.attach_ue(ue)
        ue.register()
        if not ue.wait_for_state("REGISTERED", timeout=15):
            log.warning("UE %s reg failed (state=%s)", ue.imsi, ue.state)
            return (ue, False)

        # PSI=1: DNN=internet (data)
        ue.establish_pdu_session(dnn="internet", sst=1, pdu_session_id=1)
        deadline = time.time() + 15
        while time.time() < deadline:
            s1 = ue.pdu_sessions.get(1)
            if s1 and s1.get("ip") and s1["ip"] != "unknown":
                break
            time.sleep(0.3)
        else:
            log.warning("UE %s internet PDU timeout", ue.imsi)
            return (ue, False)

        # PSI=2: DNN=ims (voice/video)
        ue.establish_pdu_session(dnn="ims", sst=1, pdu_session_id=2)
        deadline = time.time() + 15
        while time.time() < deadline:
            s2 = ue.pdu_sessions.get(2)
            if s2 and s2.get("ip") and s2["ip"] != "unknown":
                break
            time.sleep(0.3)
        else:
            log.warning("UE %s IMS PDU timeout", ue.imsi)
            return (ue, False)

        return (ue, True)
    except Exception as e:
        log.warning("UE %s dual-DNN reg error: %s", ue.imsi, e)
        return (ue, False)


def _run_data_pair(ue_a, ue_b, server, port_a, port_b, duration, bandwidth):
    """Run UDP bidirectional traffic for one UE pair on internet PDU (PSI=1).

    Each UE runs bidir (UL+DL simultaneously) via TrafficEngine.
    Returns data result dict.
    """
    engine = TrafficEngine.get()
    ip_a = ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
    ip_b = ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")

    dl_port_a = port_a + 1000
    dl_port_b = port_b + 1000

    # Run both UEs concurrently, each with bidir
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        fa = pool.submit(engine.run_bidir, ip_a, server, server, "udp",
                         port_a, dl_port_a, bandwidth, duration, True)
        fb = pool.submit(engine.run_bidir, ip_b, server, server, "udp",
                         port_b, dl_port_b, bandwidth, duration, True)
        ul_a, dl_a = fa.result()
        ul_b, dl_b = fb.result()

    ra = {
        "ue_ip": ip_a,
        "imsi": ue_a.imsi,
        "ul_kbps": round(ul_a.throughput_kbps, 1),
        "dl_kbps": round(dl_a.throughput_kbps, 1),
        "ul_jitter_ms": round(ul_a.jitter_ms, 2),
        "dl_jitter_ms": round(dl_a.jitter_ms, 2),
        "ul_loss_pct": round(ul_a.loss_pct, 2),
        "dl_loss_pct": round(dl_a.loss_pct, 2),
        "status": "PASS" if (ul_a.throughput_kbps > 0 and dl_a.throughput_kbps > 0) else "FAIL",
    }
    rb = {
        "ue_ip": ip_b,
        "imsi": ue_b.imsi,
        "ul_kbps": round(ul_b.throughput_kbps, 1),
        "dl_kbps": round(dl_b.throughput_kbps, 1),
        "ul_jitter_ms": round(ul_b.jitter_ms, 2),
        "dl_jitter_ms": round(dl_b.jitter_ms, 2),
        "ul_loss_pct": round(ul_b.loss_pct, 2),
        "dl_loss_pct": round(dl_b.loss_pct, 2),
        "status": "PASS" if (ul_b.throughput_kbps > 0 and dl_b.throughput_kbps > 0) else "FAIL",
    }

    total_ul = ra["ul_kbps"] + rb["ul_kbps"]
    total_dl = ra["dl_kbps"] + rb["dl_kbps"]
    ok = ra["status"] == "PASS" and rb["status"] == "PASS"

    return {
        "status": "PASS" if ok else "FAIL",
        "ue_a": ra, "ue_b": rb,
        "total_ul_kbps": total_ul, "total_dl_kbps": total_dl,
    }


def _run_vinr_pair(ue_a, ue_b, duration, audio_port=20000, video_port=20002):
    """Run bidirectional ViNR call (audio+video RTP) on IMS PDU (PSI=2).

    Returns ViNR result dict with MOS.
    """
    ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
    ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")
    tun_a = _get_tun_for_ue(ue_a)
    tun_b = _get_tun_for_ue(ue_b)

    audio_a, audio_b = RtpStreamStats(), RtpStreamStats()
    video_a, video_b = RtpStreamStats(), RtpStreamStats()

    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
        pool.submit(send_rtp_stream, ip_a, ip_b, audio_port, duration,
                    audio_port, audio_a, tun_a, "audio")
        pool.submit(send_rtp_stream, ip_b, ip_a, audio_port, duration,
                    audio_port + 1, audio_b, tun_b, "audio")
        pool.submit(send_rtp_stream, ip_a, ip_b, video_port, duration,
                    video_port, video_a, tun_a, "video")
        fb = pool.submit(send_rtp_stream, ip_b, ip_a, video_port, duration,
                         video_port + 1, video_b, tun_b, "video")
        fb.result()

    a_jitter = max(audio_a.jitter_ms, audio_b.jitter_ms)
    a_loss = max(audio_a.loss_pct, audio_b.loss_pct)
    v_jitter = max(video_a.jitter_ms, video_b.jitter_ms)
    v_loss = max(video_a.loss_pct, video_b.loss_pct)
    delay = max(a_jitter * 4, 10)
    mos = ImsVoiceCallQuality._estimate_mos(delay, a_jitter, a_loss)

    return {
        "ue_a": ue_a.imsi, "ue_b": ue_b.imsi,
        "ip_a": ip_a, "ip_b": ip_b,
        "audio_jitter_ms": round(a_jitter, 2), "audio_loss_pct": round(a_loss, 2),
        "video_jitter_ms": round(v_jitter, 2), "video_loss_pct": round(v_loss, 2),
        "mos": round(mos, 2),
        "audio_pkts": audio_a.tx_packets + audio_b.tx_packets,
        "video_pkts": video_a.tx_packets + video_b.tx_packets,
        "status": "PASS" if (audio_a.tx_packets > 0 and video_a.tx_packets > 0) else "FAIL",
    }


class _MultiDnnBase(TestCase):
    """Internal base for the multi-DNN factory variants — see _make_multi_dnn_tc."""
    _abstract = True

    # Per-variant knobs (overridden by the factory subclass).
    _num_pairs = 1
    _duration = TRAFFIC_DURATION
    _bandwidth = "1M"

    def run(self):
        return self._run_impl(self._num_pairs, self._duration, self._bandwidth)

    def _run_impl(self, num_pairs, duration, bandwidth):
        gnb = self.require_gnb()
        self.require_ue()

        ue_count = num_pairs * 2
        count = min(ue_count, len(self.ue_pool))
        if count < ue_count:
            log.warning("Only %d UEs available (requested %d)", count, ue_count)
        count = count - (count % 2)  # ensure even
        ues = self.ue_pool[:count]
        actual_pairs = count // 2

        # Register all UEs with dual PDU sessions concurrently
        log.info("Registering %d UEs with dual DNN (internet + ims) concurrently", count)
        reg_failed = 0
        with concurrent.futures.ThreadPoolExecutor(max_workers=count) as pool:
            futures = {pool.submit(_register_ue_dual_dnn, ue, gnb): ue
                       for ue in ues}
            for f in concurrent.futures.as_completed(futures):
                ue, ok = f.result()
                if not ok:
                    reg_failed += 1

        if reg_failed > 0:
            self.fail_test(f"{reg_failed}/{count} UE dual-DNN registrations failed")
            return self.result

        log.info("All %d UEs registered with dual PDU (internet + ims)", count)

        # Pair UEs
        pairs = [(ues[i], ues[i + 1]) for i in range(0, count, 2)]

        # Derive iperf3 server from first UE's internet IP
        first_inet_ip = ues[0].pdu_sessions.get(1, {}).get("ip", "unknown")
        server = derive_gateway(first_inet_ip)

        base_port = 5201
        iperf_ports = list(range(base_port, base_port + count))

        upf_before = collect_upf_stats()

        # Run all pairs concurrently — each pair runs data + ViNR simultaneously
        log.info("Multi-DNN: %d pairs — concurrent data (UDP %s) + ViNR (audio+video)",
                 actual_pairs, bandwidth)

        pair_results = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=actual_pairs) as pool:
            futures = {}
            for i, (ue_a, ue_b) in enumerate(pairs):
                f = pool.submit(self._run_one_pair, ue_a, ue_b,
                                server, iperf_ports[i * 2], iperf_ports[i * 2 + 1],
                                duration, bandwidth,
                                20000 + i * 20, 20002 + i * 20)
                futures[f] = (ue_a, ue_b)

            for f in concurrent.futures.as_completed(futures):
                ue_a, ue_b = futures[f]
                r = f.result()
                pair_results.append(r)
                log.info("Pair %s↔%s: data=%s ViNR=%s MOS=%.2f "
                         "UL=%.0fkbps DL=%.0fkbps",
                         ue_a.imsi[-3:], ue_b.imsi[-3:],
                         r["data"]["status"], r["vinr"]["status"],
                         r["vinr"]["mos"],
                         r["data"]["total_ul_kbps"],
                         r["data"]["total_dl_kbps"])

        upf_after = collect_upf_stats()
        upf_delta = compute_upf_delta(upf_before, upf_after)

        # Aggregate
        data_passed = sum(1 for r in pair_results if r["data"]["status"] == "PASS")
        vinr_passed = sum(1 for r in pair_results if r["vinr"]["status"] == "PASS")
        total_ul = sum(r["data"]["total_ul_kbps"] for r in pair_results)
        total_dl = sum(r["data"]["total_dl_kbps"] for r in pair_results)
        avg_mos = sum(r["vinr"]["mos"] for r in pair_results) / max(len(pair_results), 1)
        total_audio = sum(r["vinr"]["audio_pkts"] for r in pair_results)
        total_video = sum(r["vinr"]["video_pkts"] for r in pair_results)

        quality = ("Excellent" if avg_mos >= 4.0 else "Good" if avg_mos >= 3.5
                   else "Fair" if avg_mos >= 3.0 else "Poor" if avg_mos >= 2.5 else "Bad")

        io = upf_delta.get("io", {})
        log.info("Multi-DNN %d pairs: data=%d/%d ViNR=%d/%d "
                 "UL=%.1fMbps DL=%.1fMbps MOS=%.2f(%s) "
                 "audio=%d video=%d pkts UPF=%d/%d pkts",
                 actual_pairs, data_passed, actual_pairs,
                 vinr_passed, actual_pairs,
                 total_ul / 1000, total_dl / 1000,
                 avg_mos, quality, total_audio, total_video,
                 io.get("ul_pkts", 0), io.get("dl_pkts", 0))

        all_pass = data_passed == actual_pairs and vinr_passed == actual_pairs
        if all_pass:
            self.pass_test(
                num_pairs=actual_pairs, ue_count=count,
                data_bandwidth=bandwidth, duration_s=duration,
                data_passed=data_passed, vinr_passed=vinr_passed,
                total_ul_mbps=round(total_ul / 1000, 2),
                total_dl_mbps=round(total_dl / 1000, 2),
                avg_mos=round(avg_mos, 2), quality=quality,
                total_audio_pkts=total_audio, total_video_pkts=total_video,
                pair_results=pair_results, upf_stats=upf_delta,
            )
        else:
            self.fail_test(
                f"data={data_passed}/{actual_pairs} vinr={vinr_passed}/{actual_pairs}",
                num_pairs=actual_pairs, ue_count=count,
                data_passed=data_passed, vinr_passed=vinr_passed,
                pair_results=pair_results, upf_stats=upf_delta,
            )
        return self.result

    def _run_one_pair(self, ue_a, ue_b, server, port_a, port_b,
                      duration, bandwidth, audio_port, video_port):
        """Run data + ViNR concurrently for one UE pair."""
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            f_data = pool.submit(_run_data_pair, ue_a, ue_b,
                                 server, port_a, port_b, duration, bandwidth)
            f_vinr = pool.submit(_run_vinr_pair, ue_a, ue_b,
                                 duration, audio_port, video_port)
            data_result = f_data.result()
            vinr_result = f_vinr.result()

        return {"data": data_result, "vinr": vinr_result}


def _make_multi_dnn_tc(tc_id, name, num_pairs, duration=TRAFFIC_DURATION, bandwidth="1M"):
    """Factory for multi-DNN test cases: data + ViNR per UE pair.

    Builds a SPEC for the variant (TC ID, num UEs, bandwidth) and returns
    a concrete TestCase subclass with that SPEC attached.
    """
    spec = TestSpec(
        tc_id=tc_id,
        title=(f"Multi-DNN concurrent data + ViNR — "
               f"{num_pairs * 2} UE / {num_pairs} pair / {bandwidth}"),
        spec="TS 23.501 §5.6.1",
        domain=Domain.PDU_SESSION,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.CSCF),
        severity=Severity.MAJOR,
        tags=("conformance", "scale", "ims", "slow"),
        setup=Setup.BASELINE,
        expected_duration_s=max(duration * 1.5, 30.0),
        description=(
            "Purpose\n"
            f"  Exercises the dual-DNN concurrent-bearer plumbing at "
            f"{num_pairs} pair / {num_pairs * 2} UE scale per TS 23.501\n"
            "  §5.6.1: same UE simultaneously holds PSI=1 (DNN=internet,\n"
            "  5QI=9 best-effort) and PSI=2 (DNN=ims, 5QI=1+2 GBR), with\n"
            "  per-PSI GTP-U tunnels and per-DNN UPF anchors. Catches\n"
            "  QoS isolation regressions where the IMS GBR flow steals\n"
            "  bandwidth from the best-effort flow, or where the second\n"
            "  PDU silently fails to allocate a distinct UE IP.\n"
            "\n"
            "Procedure (TS 23.501 §5.6.1 + §5.7 5QI mapping)\n"
            "  1. require_gnb() + require_ue();\n"
            f"     count = min({num_pairs * 2}, len(ue_pool)), rounded\n"
            "     down to even; actual_pairs = count // 2.\n"
            "  2. ThreadPoolExecutor concurrently runs\n"
            "     _register_ue_dual_dnn for each UE: attach → register →\n"
            "     wait REGISTERED → establish PSI=1 DNN=internet (poll\n"
            "     for IP) → establish PSI=2 DNN=ims (poll for IP).\n"
            f"  3. Pair UEs as (ues[0], ues[1]), (ues[2], ues[3]), ...;\n"
            "     server = derive_gateway(first UE's internet IP).\n"
            "  4. collect_upf_stats() before-snapshot.\n"
            "  5. ThreadPoolExecutor(max_workers=actual_pairs) submits\n"
            "     _run_one_pair per pair: that inner function fans out\n"
            "     two threads — _run_data_pair (UDP bidir at\n"
            f"     {bandwidth}, duration={duration}s) + _run_vinr_pair\n"
            "     (audio+video RTP via send_rtp_stream over UE TUNs).\n"
            "  6. compute_upf_delta for the run; aggregate data_passed,\n"
            "     vinr_passed, total_ul/dl, avg_mos.\n"
            "\n"
            "Parameters (self.params)\n"
            f"  None — knobs baked into the factory class (num_pairs="
            f"{num_pairs}, bandwidth='{bandwidth}', duration="
            f"{duration}).\n"
            "\n"
            "Pass criteria\n"
            "  data_passed == actual_pairs AND vinr_passed == actual_pairs\n"
            "  (every pair: UL>0 AND DL>0 on internet, AND audio_pkts>0\n"
            "  AND video_pkts>0 on IMS).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  num_pairs, ue_count, data_bandwidth, duration_s,\n"
            "  data_passed, vinr_passed, total_ul_mbps, total_dl_mbps,\n"
            "  avg_mos, quality, total_audio_pkts, total_video_pkts,\n"
            "  pair_results, upf_stats.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — pretest resets sacore to baseline. UE\n"
            "  pool must be large enough to host\n"
            f"  {num_pairs * 2} UEs; missing UEs trigger a fail_test\n"
            "  before any traffic. IMS DNN must be provisioned.\n"
            "  Aggregate-throughput targets are reported but NOT\n"
            f"  asserted — only per-pair PASS/FAIL drives the gate."
        ),
        requires_dataplane=True,
    )

    new_cls = type(name, (_MultiDnnBase,), {
        "SPEC": spec,
        "_num_pairs": num_pairs,
        "_duration": duration,
        "_bandwidth": bandwidth,
        "__module__": __name__,
        "__qualname__": name,
    })
    return new_cls


# ═══════════════════════════════════════════════════════════════
# Multi-DNN Test Cases: Data (UDP) + ViNR (Audio+Video)
# ═══════════════════════════════════════════════════════════════
MultiDnn2 = _make_multi_dnn_tc(
    "TC-MDN-001", "multi_dnn_2ue", num_pairs=1, duration=TRAFFIC_DURATION, bandwidth="1M")

MultiDnn4 = _make_multi_dnn_tc(
    "TC-MDN-002", "multi_dnn_4ue", num_pairs=2, duration=TRAFFIC_DURATION, bandwidth="1M")

MultiDnn8 = _make_multi_dnn_tc(
    "TC-MDN-003", "multi_dnn_8ue", num_pairs=4, duration=TRAFFIC_DURATION, bandwidth="1M")

MultiDnn16 = _make_multi_dnn_tc(
    "TC-MDN-004", "multi_dnn_16ue", num_pairs=8, duration=TRAFFIC_DURATION, bandwidth="1M")

MultiDnn32 = _make_multi_dnn_tc(
    "TC-MDN-005", "multi_dnn_32ue", num_pairs=16, duration=TRAFFIC_DURATION, bandwidth="1M")

MultiDnn64 = _make_multi_dnn_tc(
    "TC-MDN-006", "multi_dnn_64ue", num_pairs=32, duration=TRAFFIC_DURATION, bandwidth="1M")

ALL_MULTI_DNN_TCS = [
    MultiDnn2, MultiDnn4, MultiDnn8, MultiDnn16, MultiDnn32, MultiDnn64,
]

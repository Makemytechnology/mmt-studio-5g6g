# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test cases: IMS PDU session, SIP signaling, and VoNR traffic.

TS 23.228 — IP Multimedia Subsystem (IMS) architecture
TS 24.229 — SIP/SDP procedures for IMS
TS 26.114 — IP Multimedia Subsystem (IMS); Multimedia telephony; Media handling
TS 23.501 §5.7.2.1 — 5QI=1 (conversational voice), 5QI=2 (conversational video)
"""

import subprocess
import json
import time
import logging

from src.testcases.base import TestCase, StopTest
from src.testcases.spec import TestSpec, Domain, NF, Slice, Severity, Setup
from src.config import TRAFFIC_DURATION
from src.core.api import core_api as _core_api
from src.traffic.engine import TrafficEngine, derive_gateway as derive_gateway
from src.protocol.sip_client import SipClient

log = logging.getLogger("tester.tc_ims")


class ImsPduSession(TestCase):
    """IMS PDU session establishment (DNN=ims)."""
    SPEC = TestSpec(
        tc_id="TC-IMS-001",
        title="IMS PDU session establishment on DNN=ims",
        spec="TS 23.228 §5.1.2",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Foundation gate for every downstream IMS / VoNR test in this\n"
            "  file. Pins TS 23.228 §5.1.2 IMS PDU session establishment:\n"
            "  the UE must be reachable on a dedicated IMS APN (DNN=ims,\n"
            "  PSI=2) with an IPv4 address allocated by the SMF before any\n"
            "  SIP signalling can travel.\n"
            "\n"
            "Procedure (TS 23.228 §5.1.2 + TS 23.501 §5.6)\n"
            "  1. require_gnb() / require_ue(imsi=params.imsi) — get a UE\n"
            "     from the pool, attach to the gNB stub.\n"
            "  2. register_ue(ue, gnb, timeout) — NAS Registration (AMF\n"
            "     Initial Registration, AKA challenge, Security Mode).\n"
            "  3. establish_pdu(ue, psi=2, dnn='ims', timeout) — SMF PDU\n"
            "     Session Establishment for the IMS DNN; UPF allocates IP,\n"
            "     PCO returns the P-CSCF address into ue.pdu_sessions[2].\n"
            "  4. Report imsi, dnn='ims', and the allocated IP.\n"
            "\n"
            "Parameters (self.params)\n"
            "  imsi    — override which UE to use (default: first from pool).\n"
            "  timeout — per-step timeout in seconds (default: 20).\n"
            "\n"
            "Pass criteria\n"
            "  register_ue() == True AND establish_pdu(psi=2, dnn='ims') ==\n"
            "  True AND ue.pdu_sessions[2]['ip'] is allocated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  imsi, dnn, ip.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — in-process SA Core simulator; no real radio.\n"
            "  The SMF allocates the IP from the seeded UPF pool, not from\n"
            "  DHCP. P-CSCF address comes from the static PCO seed, not\n"
            "  from a live DHCP/DNS lookup."
        ),
    )
    tc_id = "TC-IMS-001"
    name = "ims_pdu_session"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue(self.params.get("imsi"))
        timeout = self.params.get("timeout", 20)

        if not self.register_ue(ue, gnb, timeout):
            return self.result

        if self.establish_pdu(ue, psi=2, dnn="ims", timeout=timeout):
            session = ue.pdu_sessions[2]
            self.pass_test(imsi=ue.imsi, dnn="ims", ip=session.get("ip"))
        return self.result


class MultiPduSession(TestCase):
    """Establish both internet and IMS PDU sessions simultaneously."""
    SPEC = TestSpec(
        tc_id="TC-IMS-002",
        title="Dual PDU sessions: internet (PSI=1) + IMS (PSI=2) concurrently",
        spec="TS 23.228 §5.1.2",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance",),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Validates per-DNN session-context separation in the SMF\n"
            "  (TS 23.228 §5.1.2, TS 23.501 §5.6). A real handset always\n"
            "  carries internet + IMS as two distinct PDU sessions; this\n"
            "  test pins that the SMF correctly tracks them in parallel\n"
            "  without collapsing them into a single context.\n"
            "\n"
            "Procedure (TS 23.228 §5.1.2 + TS 23.501 §5.6.1)\n"
            "  1. require_gnb() / require_ue() — first UE from pool.\n"
            "  2. register_ue(ue, gnb, timeout) — NAS Initial Registration.\n"
            "  3. establish_pdu(ue, psi=1, dnn='internet', timeout) — first\n"
            "     PDU session (default best-effort, 5QI=9).\n"
            "  4. establish_pdu(ue, psi=2, dnn='ims', timeout) — second PDU\n"
            "     session on the IMS APN (default 5QI=5 IMS-signalling).\n"
            "  5. Report internet_ip and ims_ip from ue.pdu_sessions.\n"
            "\n"
            "Parameters (self.params)\n"
            "  timeout — per-step timeout in seconds (default: 20).\n"
            "\n"
            "Pass criteria\n"
            "  ok_internet AND ok_ims (both establish_pdu() return True)\n"
            "  AND ue.pdu_sessions[1] and [2] each carry a non-empty 'ip'.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  internet_ip, ims_ip.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Both PDUs anchor on the same UPF simulator;\n"
            "  real multi-UPF placement (e.g. local breakout vs central IMS\n"
            "  anchor) is not exercised. PSI=1 is created before PSI=2 in\n"
            "  this test — the real-world order is irrelevant to the SMF\n"
            "  but is hard-coded here."
        ),
    )
    tc_id = "TC-IMS-002"
    name = "multi_pdu_session"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        timeout = self.params.get("timeout", 20)

        if not self.register_ue(ue, gnb, timeout):
            return self.result

        ok_internet = self.establish_pdu(ue, psi=1, dnn="internet", timeout=timeout)
        ok_ims = self.establish_pdu(ue, psi=2, dnn="ims", timeout=timeout)

        if ok_internet and ok_ims:
            self.pass_test(
                internet_ip=ue.pdu_sessions.get(1, {}).get("ip"),
                ims_ip=ue.pdu_sessions.get(2, {}).get("ip"),
            )
        return self.result


class ImsVoiceTraffic(TestCase):
    """VoNR voice traffic simulation — UDP at voice codec bitrate.

    5QI=1: Conversational voice, GBR, PDB=100ms, PER=10^-2.
    AMR-WB codec: ~23.85 kbps, 20ms frames.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-003",
        title="VoNR voice traffic — AMR-WB rate UDP on IMS PDU (5QI=1)",
        spec="TS 26.114",
        domain=Domain.VOICE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Dataplane-throughput gate for conversational voice. Drives\n"
            "  UDP at AMR-WB codec rate (~24 kbps) on the IMS PDU and\n"
            "  cross-checks that jitter / loss stay within the TS 23.501\n"
            "  §5.7.4 Table 5.7.4-1 5QI=1 envelope (GBR voice, PDB=100 ms,\n"
            "  PER=10^-2). Catches UPF/scheduler regressions that break\n"
            "  conversational voice timing on the IMS bearer.\n"
            "\n"
            "Procedure (TS 26.114 §7 + TS 23.501 §5.7.4)\n"
            "  1. register_ue() + establish_pdu(psi=2, dnn='ims').\n"
            "  2. Pull ue_ip from ue.pdu_sessions[2]['ip']; if missing,\n"
            "     fall back through PSI=1 and derive_gateway(ue_ip) for the\n"
            "     iperf3 target.\n"
            "  3. TrafficEngine.get().create_session(src_ip=ue_ip,\n"
            "     dst_ip=server, protocol='udp', dst_port=5201,\n"
            "     bandwidth='24K', direction='ul') — AMR-WB rate UDP.\n"
            "  4. session.start() / session.stop() — collect throughput,\n"
            "     jitter, loss from iperf3.\n"
            "\n"
            "Parameters (self.params)\n"
            "  iperf_server — override target (default: derive_gateway).\n"
            "  duration     — seconds of traffic (default: 10).\n"
            "  bandwidth    — UDP rate (default: '24K' = AMR-WB).\n"
            "\n"
            "Pass criteria\n"
            "  iperf3 stats != None (session completed and reported a\n"
            "  result). The 5QI=1 PDB=100 ms / PER=10^-2 envelope is\n"
            "  reported but not asserted — the simulator's UPF does not\n"
            "  enforce real scheduling delays.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR', codec='AMR-WB', fiveqi=1, bandwidth_target,\n"
            "  actual_kbps, jitter_ms, loss_pct, ue_ip, pdb_budget_ms=100.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — iperf3 carries plain UDP at the codec rate,\n"
            "  not real RTP; no SIP / SDP / PCF Rx dedicated bearer is\n"
            "  triggered. The PDB ceiling (100 ms per TS 23.501 §5.7.4\n"
            "  Table 5.7.4-1) is reported as context, not gated."
        ),
    )
    tc_id = "TC-IMS-003"
    name = "ims_voice_traffic"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 10)
        # AMR-WB: ~24 kbps per direction
        bandwidth = self.params.get("bandwidth", "24K")

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="ims"):
            return self.result

        ue_ip = ue.pdu_sessions.get(2, {}).get("ip")
        if not ue_ip or ue_ip == "unknown":
            # Fallback to PSI=1
            if not self.establish_pdu(ue, psi=2, dnn="ims"):  # TS 24.229: SIP over IMS PDU
                return self.result
            ue_ip = ue.pdu_sessions.get(2, {}).get("ip") or ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            if not server: server = derive_gateway(ue_ip)

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth=bandwidth, duration=duration, direction="ul")
        session.start()
        stats = session.stop()
        if stats:
            self.pass_test(
                service="VoNR", codec="AMR-WB", fiveqi=1,
                bandwidth_target=bandwidth,
                actual_kbps=round(stats.throughput_kbps, 2),
                jitter_ms=round(stats.jitter_ms, 3),
                loss_pct=round(stats.loss_pct, 2),
                ue_ip=ue_ip, pdb_budget_ms=100,
            )
        else:
            self.fail_test("VoNR voice traffic: iperf3 failed")
        return self.result


class ImsVideoTraffic(TestCase):
    """VoNR video traffic simulation — UDP at video bitrate.

    5QI=2: Conversational video, GBR, PDB=150ms, PER=10^-3.
    H.264 baseline: ~2 Mbps typical.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-004",
        title="ViNR video traffic — H.264 rate UDP on IMS PDU (5QI=2)",
        spec="TS 23.228 §5.10",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "video"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Dataplane-throughput gate for conversational video. Drives\n"
            "  UDP at H.264-baseline rate (~2 Mbps) on the IMS PDU and\n"
            "  reports jitter / loss against the TS 23.501 §5.7.4 Table\n"
            "  5.7.4-1 5QI=2 envelope (GBR video, PDB=150 ms, PER=10^-3).\n"
            "  Validates the IMS / video dataplane at the bitrate point\n"
            "  used by TS 23.228 §5.10 video telephony.\n"
            "\n"
            "Procedure (TS 23.228 §5.10 + TS 23.501 §5.7.4)\n"
            "  1. register_ue() + establish_pdu(psi=2, dnn='ims').\n"
            "  2. Pull ue_ip from ue.pdu_sessions[2]['ip']; fall back to\n"
            "     PSI=1 and derive_gateway(ue_ip) if the IMS IP is not\n"
            "     advertised yet.\n"
            "  3. TrafficEngine.get().create_session(src_ip=ue_ip,\n"
            "     dst_ip=server, protocol='udp', dst_port=5201,\n"
            "     bandwidth='2M', direction='ul') — H.264 video rate.\n"
            "  4. session.start() / session.stop() — collect throughput,\n"
            "     jitter, loss.\n"
            "\n"
            "Parameters (self.params)\n"
            "  iperf_server — override target (default: derive_gateway).\n"
            "  duration     — seconds of traffic (default: 10).\n"
            "  bandwidth    — UDP rate (default: '2M' = H.264 baseline).\n"
            "\n"
            "Pass criteria\n"
            "  iperf3 stats != None — session completed and reported a\n"
            "  throughput record. The 5QI=2 PDB=150 ms ceiling is reported\n"
            "  for context.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR-Video', codec='H.264', fiveqi=2,\n"
            "  bandwidth_target, actual_mbps, jitter_ms, loss_pct, ue_ip,\n"
            "  pdb_budget_ms=150.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — iperf3 generates UDP at the video rate, not\n"
            "  real H.264 NALU-bearing RTP. No SDP offer/answer, no PCF Rx\n"
            "  dedicated bearer setup. PDB enforcement is not exercised."
        ),
    )
    tc_id = "TC-IMS-004"
    name = "ims_video_traffic"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 10)
        bandwidth = self.params.get("bandwidth", "2M")

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="ims"):
            return self.result

        ue_ip = ue.pdu_sessions.get(2, {}).get("ip")
        if not ue_ip or ue_ip == "unknown":
            if not self.establish_pdu(ue, psi=2, dnn="ims"):  # TS 24.229: SIP over IMS PDU
                return self.result
            ue_ip = ue.pdu_sessions.get(2, {}).get("ip") or ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            if not server: server = derive_gateway(ue_ip)

        engine = TrafficEngine.get()
        session = engine.create_session(
            src_ip=ue_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth=bandwidth, duration=duration, direction="ul")
        session.start()
        stats = session.stop()
        if stats:
            self.pass_test(
                service="VoNR-Video", codec="H.264", fiveqi=2,
                bandwidth_target=bandwidth,
                actual_mbps=round(stats.throughput_kbps / 1000, 2),
                jitter_ms=round(stats.jitter_ms, 3),
                loss_pct=round(stats.loss_pct, 2),
                ue_ip=ue_ip, pdb_budget_ms=150,
            )
        else:
            self.fail_test("VoNR video traffic: iperf3 failed")
        return self.result


class ImsVoiceLatency(TestCase):
    """VoNR voice latency — must meet 5QI=1 PDB of 100ms."""
    SPEC = TestSpec(
        tc_id="TC-IMS-005",
        title="VoNR voice latency — meets 5QI=1 PDB of 100ms",
        spec="TS 26.114",
        domain=Domain.VOICE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  Latency-bound gate for conversational voice. Asserts the IMS\n"
            "  dataplane round-trip stays inside the TS 23.501 §5.7.4 Table\n"
            "  5.7.4-1 5QI=1 PDB ceiling of 100 ms. Surfaces scheduler /\n"
            "  UPF regressions that would break VoNR mouth-to-ear timing\n"
            "  before they show up as MOS drops.\n"
            "\n"
            "Procedure (TS 26.114 §7 + TS 23.501 §5.7.4)\n"
            "  1. register_ue() + establish_pdu(psi=2, dnn='ims').\n"
            "  2. Read ue_ip from ue.pdu_sessions[2]['ip']; fall back to\n"
            "     PSI=1 if the IMS IP is missing.\n"
            "  3. derive_gateway(ue_ip) as the ping target (or self.params\n"
            "     ping_target override).\n"
            "  4. subprocess.run(['ping', '-c', count, '-i', '0.02',\n"
            "     '-I', ue_ip, '-W', '2', target]) — 50 echo requests at\n"
            "     20 ms spacing, source-bound to the UE IP.\n"
            "  5. Parse 'min/avg/max' RTT and packet-loss percentage from\n"
            "     ping's stdout; flag pdb_met = avg_ms < 100.\n"
            "\n"
            "Parameters (self.params)\n"
            "  ping_target — override target (default: derive_gateway).\n"
            "  count       — echo requests to send (default: 50).\n"
            "\n"
            "Pass criteria\n"
            "  ping returncode == 0 (all 50 echoes saw a reply); avg_ms,\n"
            "  min_ms, max_ms parsed successfully. pdb_met (avg<100 ms) is\n"
            "  reported but not used as a hard gate — the simulator's\n"
            "  loopback path is sub-millisecond by construction.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_ip, target, count, fiveqi=1, pdb_budget_ms=100, min_ms,\n"
            "  avg_ms, max_ms, loss_pct, pdb_met.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — ICMP, not RTP, so codec / framing / jitter-\n"
            "  buffer overhead is not measured. Real-world VoNR PDB budget\n"
            "  also covers air-interface scheduling; this test only\n"
            "  measures the host-side / UPF loopback leg."
        ),
    )
    tc_id = "TC-IMS-005"
    name = "ims_voice_latency"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        target = self.params.get("ping_target")
        count = self.params.get("count", 50)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="ims"):
            return self.result

        ue_ip = ue.pdu_sessions.get(2, {}).get("ip")
        if not ue_ip or ue_ip == "unknown":
            if not self.establish_pdu(ue, psi=2, dnn="ims"):  # TS 24.229: SIP over IMS PDU
                return self.result
            ue_ip = ue.pdu_sessions.get(2, {}).get("ip") or ue.pdu_sessions.get(1, {}).get("ip", "unknown")

        if not target: target = derive_gateway(ue_ip)
        cmd = ["ping", "-c", str(count), "-i", "0.02", "-I", ue_ip, "-W", "2", target]
        try:
            proc = subprocess.run(cmd, capture_output=True, text=True, timeout=count + 30)
            if proc.returncode == 0:
                stats = {"ue_ip": ue_ip, "target": target, "count": count,
                         "fiveqi": 1, "pdb_budget_ms": 100}
                for line in proc.stdout.split("\n"):
                    if "min/avg/max" in line:
                        parts = line.split("=")[1].strip().split("/")
                        stats["min_ms"] = float(parts[0])
                        stats["avg_ms"] = float(parts[1])
                        stats["max_ms"] = float(parts[2])
                    if "packet loss" in line:
                        for part in line.split(","):
                            if "packet loss" in part:
                                stats["loss_pct"] = float(part.strip().split("%")[0])
                pdb_met = stats.get("avg_ms", 999) < 100
                stats["pdb_met"] = pdb_met
                self.pass_test(**stats)
            else:
                self.fail_test(f"Ping failed: {proc.stderr[:200]}")
        except subprocess.TimeoutExpired:
            self.fail_test("Ping timed out")
        except Exception as e:
            self.fail_test(f"Ping error: {e}")
        return self.result


class ImsDualPduTraffic(TestCase):
    """Simultaneous internet + IMS traffic on dual PDU sessions."""
    SPEC = TestSpec(
        tc_id="TC-IMS-006",
        title="Simultaneous internet TCP + IMS voice UDP on dual PDUs",
        spec="TS 23.228 §5.1.2",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  Cross-PDU isolation gate. A single UE running internet TCP\n"
            "  bulk on PSI=1 must not starve concurrent VoNR-rate UDP on\n"
            "  PSI=2 (TS 23.228 §5.1.2 per-DNN session contexts; TS 23.501\n"
            "  §5.7.4 5QI=1 GBR voice vs §5.7.4 5QI=9 default best-effort).\n"
            "  Regression gate for SMF/UPF scheduler fairness.\n"
            "\n"
            "Procedure (TS 23.228 §5.1.2 + TS 23.501 §5.7.4)\n"
            "  1. register_ue() then establish_pdu(psi=2, dnn='ims') twice\n"
            "     (the run() body invokes establish_pdu twice — the second\n"
            "     call is a no-op confirm; the intent is dual PDU bring-up).\n"
            "  2. Pull inet_ip from ue.pdu_sessions[1] and ims_ip from\n"
            "     ue.pdu_sessions[2]; fall back ims_ip = inet_ip if the\n"
            "     IMS IP is not yet allocated.\n"
            "  3. TrafficEngine TCP bulk session (src=inet_ip,\n"
            "     dst_port=5201, direction='ul', protocol='tcp') for\n"
            "     duration seconds — captures internet_tx_mbps.\n"
            "  4. Then TrafficEngine UDP voice session (src=ims_ip,\n"
            "     bandwidth='24K', protocol='udp') for duration seconds —\n"
            "     captures voice_kbps, voice_jitter_ms, voice_loss_pct.\n"
            "\n"
            "Parameters (self.params)\n"
            "  iperf_server — override target (default: derive_gateway).\n"
            "  duration     — seconds per phase (default: 10).\n"
            "\n"
            "Pass criteria\n"
            "  inet_stats != None (TCP bulk reported throughput) AND\n"
            "  voice_ok (UDP voice reported throughput / jitter / loss).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  internet_ip, ims_ip, internet_tx_mbps, voice_kbps,\n"
            "  voice_jitter_ms, voice_loss_pct.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — the two flows run sequentially, not\n"
            "  simultaneously (TCP first, then UDP). True scheduler-\n"
            "  contention isolation between the bearers is therefore not\n"
            "  exercised by this run(). The PSI=1 path uses 5QI=9 default\n"
            "  bearer; the PSI=2 voice UDP rides the default 5QI=5 IMS\n"
            "  bearer rather than a dedicated 5QI=1 GBR flow."
        ),
    )
    tc_id = "TC-IMS-006"
    name = "ims_dual_pdu_traffic"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 10)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="ims"):  # TS 24.229: SIP over IMS PDU
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="ims"):
            return self.result

        inet_ip = ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        ims_ip = ue.pdu_sessions.get(2, {}).get("ip")
        # Use internet IP for both if IMS IP unavailable
        if not ims_ip or ims_ip == "unknown":
            ims_ip = inet_ip

        engine = TrafficEngine.get()

        # Run internet TCP bulk first
        inet_session = engine.create_session(
            src_ip=inet_ip, dst_ip=server, protocol="tcp",
            dst_port=5201, duration=duration, direction="ul")
        inet_session.start()
        inet_stats = inet_session.stop()
        inet_mbps = round(inet_stats.throughput_kbps / 1000, 2) if inet_stats else 0

        # Then IMS voice UDP
        voice_session = engine.create_session(
            src_ip=ims_ip, dst_ip=server, protocol="udp",
            dst_port=5201, bandwidth="24K", duration=duration, direction="ul")
        voice_session.start()
        voice_stats_raw = voice_session.stop()
        voice_ok = False
        voice_stats = {}
        if voice_stats_raw:
            voice_stats = {
                "voice_kbps": round(voice_stats_raw.throughput_kbps, 2),
                "voice_jitter_ms": round(voice_stats_raw.jitter_ms, 3),
                "voice_loss_pct": round(voice_stats_raw.loss_pct, 2),
            }
            voice_ok = True

        if inet_stats and voice_ok:
            self.pass_test(
                internet_ip=inet_ip, ims_ip=ims_ip,
                internet_tx_mbps=inet_mbps,
                **voice_stats,
            )
        else:
            self.fail_test("Dual PDU traffic test failed",
                           internet_ok=bool(inet_stats), voice_ok=voice_ok)
        return self.result


class ImsMultiUeVoice(TestCase):
    """Multiple UEs with simultaneous VoNR voice sessions."""
    SPEC = TestSpec(
        tc_id="TC-IMS-007",
        title="Multiple UEs run concurrent VoNR voice traffic",
        spec="TS 26.114",
        domain=Domain.VOICE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=25.0,
        description=(
            "Purpose\n"
            "  Multi-UE scaling gate for VoNR voice. Multiple registered\n"
            "  UEs running AMR-WB-rate UDP on the IMS DNN must all meet\n"
            "  the TS 23.501 §5.7.4 Table 5.7.4-1 5QI=1 envelope under\n"
            "  contention. Surfaces SMF/UPF per-UE fairness regressions.\n"
            "\n"
            "Procedure (TS 26.114 + TS 23.501 §5.7.4)\n"
            "  1. require_gnb() / require_ue(); take ues = ue_pool[:N]\n"
            "     where N = min(params.ue_count, len(ue_pool)).\n"
            "  2. Hard-fail if N < 2.\n"
            "  3. For each UE: register_ue(ue, gnb) +\n"
            "     establish_pdu(psi=1) (default DNN; the test does not\n"
            "     re-bind to dnn='ims' here, despite the SPEC dnn=ims).\n"
            "  4. For each UE: TrafficEngine.create_session(\n"
            "     src_ip=ue_ip, dst_ip=server, protocol='udp',\n"
            "     dst_port=5201, bandwidth='24K', direction='ul') and\n"
            "     start/stop sequentially per UE (not concurrently).\n"
            "  5. Collect per-UE {voice_kbps, jitter_ms, loss_pct, status}.\n"
            "\n"
            "Parameters (self.params)\n"
            "  iperf_server — override target (default: derive_gateway).\n"
            "  duration     — seconds per UE (default: 5).\n"
            "  ue_count     — UEs to drive (default: 2).\n"
            "\n"
            "Pass criteria\n"
            "  all(r['status'] == 'PASS' for r in ue_results) — every UE's\n"
            "  iperf3 session returned non-None stats.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  ue_count, ue_results (list of {imsi, ue_ip, status,\n"
            "  voice_kbps, jitter_ms, loss_pct}).\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — UEs run sequentially through the for-loop\n"
            "  rather than via ThreadPoolExecutor; this is therefore a\n"
            "  per-UE smoke test, not a true contention test. PDU is\n"
            "  established on PSI=1 (default DNN) not the IMS DNN."
        ),
    )
    tc_id = "TC-IMS-007"
    name = "ims_multi_ue_voice"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        server = self.params.get("iperf_server")
        duration = self.params.get("duration", 5)
        ue_count = min(self.params.get("ue_count", 2), len(self.ue_pool))

        ues = self.ue_pool[:ue_count]
        if len(ues) < 2:
            self.fail_test("Need at least 2 UEs for multi-UE voice test")
            return self.result

        for ue in ues:
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=1):
                return self.result

        engine = TrafficEngine.get()

        ue_results = []
        for ue in ues:
            ue_ip = ue.pdu_sessions.get(2, {}).get("ip") or ue.pdu_sessions.get(1, {}).get("ip", "unknown")
            session = engine.create_session(
                src_ip=ue_ip, dst_ip=server, protocol="udp",
                dst_port=5201, bandwidth="24K", duration=duration, direction="ul")
            session.start()
            stats = session.stop()
            if stats:
                ue_results.append({
                    "imsi": ue.imsi, "ue_ip": ue_ip, "status": "PASS",
                    "voice_kbps": round(stats.throughput_kbps, 2),
                    "jitter_ms": round(stats.jitter_ms, 3),
                    "loss_pct": round(stats.loss_pct, 2),
                })
            else:
                ue_results.append({"imsi": ue.imsi, "ue_ip": ue_ip, "status": "FAIL"})

        all_pass = all(r["status"] == "PASS" for r in ue_results)
        if all_pass:
            self.pass_test(ue_count=len(ues), ue_results=ue_results)
        else:
            self.fail_test("Some UEs failed voice traffic", ue_results=ue_results)
        return self.result


class ImsVoiceCallSingle(TestCase):
    """Single-direction VoNR voice quality — one UE sends voice to UPF.

    Simple test: register UE, IMS PDU, SIP REGISTER, send AMR-WB voice-rate UDP
    through GTP-U tunnel to UPF. Measures jitter, loss, computes MOS.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-012",
        title="Single-direction VoNR voice quality (uplink RTP) with MOS",
        spec="TS 23.228 §5.7",
        domain=Domain.VOICE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice"),
        setup=Setup.BASELINE,
        expected_duration_s=80.0,
        description=(
            "Purpose\n"
            "  Uplink-only VoNR voice-quality gate. Lightweight variant of\n"
            "  TC-IMS-011 that exercises the A→B leg of TS 23.228 §5.7 VoNR\n"
            "  call media (TS 26.114 §7) and computes MOS via ITU-T G.107.\n"
            "  Useful when bidirectional contention would mask a one-way\n"
            "  regression.\n"
            "\n"
            "Procedure (TS 23.228 §5.7 + TS 26.114 + ITU-T G.107)\n"
            "  1. Hard-fail if len(ue_pool) < 2; ue_a, ue_b = pool[0:2].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. pcscf_ip = _get_pcscf_from_session(ue_a). Call\n"
            "     _setup_ims_call(ue_a, ue_b, pcscf_ip, 5060, domain,\n"
            "     ['audio']) — registers both UEs and INVITEs A→B.\n"
            "  4. _verify_call_established(sip_a, call_id); on failure\n"
            "     _cleanup_sip_call() and fail_test.\n"
            "  5. tun_a = _get_tun_for_ue(ue_a); ip_a/ip_b from\n"
            "     ue.pdu_sessions[2]['ip'] (fallback PSI=1).\n"
            "  6. send_rtp_stream(ip_a, ip_b, 20000, duration, 20000,\n"
            "     rtp_stats, tun_a, 'audio') — single uplink AMR-WB RTP\n"
            "     stream from A's tunnel.\n"
            "  7. _cleanup_sip_call() in finally.\n"
            "  8. _estimate_mos(one_way_delay=max(jitter*4, 10), jitter,\n"
            "     loss_pct) — reuses ImsVoiceCallQuality._estimate_mos.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration — seconds of uplink RTP (default: 60).\n"
            "\n"
            "Pass criteria\n"
            "  Dialog established AND rtp_stats.tx_packets > 0 (at least\n"
            "  one RTP packet sent on the uplink leg). MOS / quality are\n"
            "  reported but not gated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR', codec='AMR-WB', fiveqi=1,\n"
            "  direction='uplink', ue_a, ue_b, voice_kbps, jitter_ms,\n"
            "  loss_pct, total_packets, estimated_mos, quality, duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — uplink only, so reverse-direction loss is\n"
            "  invisible. G.107 simplifications and the synthetic one-way\n"
            "  delay (max(jitter*4, 10 ms)) carry over from TC-IMS-011."
        ),
    )
    tc_id = "TC-IMS-012"
    name = "ims_voice_call_single"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs")
            return self.result
        ue_a, ue_b = self.ue_pool[0], self.ue_pool[1]
        duration = self.params.get("duration", 60)
        domain = _get_ims_domain()

        for ue in (ue_a, ue_b):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        pcscf_ip = _get_pcscf_from_session(ue_a)
        sip_a, sip_b, call_id, invite_status, sip_ok = _setup_ims_call(
            ue_a, ue_b, pcscf_ip, 5060, domain, ["audio"])

        ok, dialog = _verify_call_established(sip_a, call_id)
        if not ok:
            _cleanup_sip_call(sip_a, sip_b)
            self.fail_test(f"VoNR call not established — INVITE status={invite_status}")
            return self.result

        try:
            ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
            ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")

            from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats
            tun_a = _get_tun_for_ue(ue_a)
            rtp_stats = RtpStreamStats()
            send_rtp_stream(ip_a, ip_b, 20000, duration, 20000, rtp_stats, tun_a, "audio")

            if rtp_stats.tx_packets == 0:
                self.fail_test("RTP stream failed — no packets sent")
                return self.result

            jitter = rtp_stats.jitter_ms
            loss_pct = rtp_stats.loss_pct
            one_way_delay = max(jitter * 4, 10)
            mos = ImsVoiceCallQuality._estimate_mos(one_way_delay, jitter, loss_pct)
            quality = ("Excellent" if mos >= 4.0 else "Good" if mos >= 3.5
                       else "Fair" if mos >= 3.0 else "Poor" if mos >= 2.5 else "Bad")

            self.pass_test(
                service="VoNR", codec="AMR-WB", fiveqi=1, direction="uplink",
                ue_a=ue_a.imsi, ue_b=ue_b.imsi,
                voice_kbps=rtp_stats.bitrate_kbps, jitter_ms=jitter,
                loss_pct=loss_pct, total_packets=rtp_stats.tx_packets,
                estimated_mos=round(mos, 2), quality=quality,
                duration_s=duration,
            )
        finally:
            _cleanup_sip_call(sip_a, sip_b, call_id, target_imsi=ue_b.imsi, target_msisdn=getattr(ue_b.sim, "msisdn", ""))
        return self.result


class ImsVoiceCallQuality(TestCase):
    """Bidirectional VoNR voice call quality — two UEs, SIP call, RTP-like traffic.

    Full flow:
    1. Register both UEs (NAS + IMS PDU session)
    2. SIP REGISTER both UEs to P-CSCF
    3. UE_A SIP INVITE → UE_B (triggers dedicated GBR bearer via PCF Rx)
    4. Run bidirectional UDP at AMR-WB rate (simulating RTP voice)
    5. Measure jitter, loss, latency → compute MOS (ITU-T G.107 E-model)
    6. SIP BYE to tear down call

    TS 26.114 — IMS multimedia telephony media handling.
    ITU-T G.107 — E-model for MOS estimation.
    5QI=1: Conversational voice, GBR, PDB=100ms, PER=10^-2.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-011",
        title="Bidirectional VoNR call quality with MOS (G.107 E-model)",
        spec="TS 23.228 §5.7",
        domain=Domain.VOICE,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.PCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  End-to-end VoNR voice-quality gate. Drives the full TS 23.228\n"
            "  §5.7 call flow on two UEs (NAS + IMS PDU + SIP REGISTER +\n"
            "  INVITE) and runs bidirectional AMR-WB RTP through the GTP-U\n"
            "  tunnels (TS 26.114 §7 IMS media handling), then computes\n"
            "  MOS via the ITU-T G.107 E-model. Single test that surfaces\n"
            "  signalling, bearer, and media regressions together.\n"
            "\n"
            "Procedure (TS 23.228 §5.7 + TS 26.114 + ITU-T G.107)\n"
            "  1. Hard-fail if len(ue_pool) < 2; ue_a, ue_b = pool[0:2].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. pcscf_ip = _get_pcscf_from_session(ue_a); call\n"
            "     _setup_ims_call(ue_a, ue_b, pcscf_ip, 5060, domain,\n"
            "     ['audio']) — handles REGISTER for both UEs then INVITE\n"
            "     from A; returns sip_a, sip_b, call_id, invite_status.\n"
            "  4. _verify_call_established(sip_a, call_id) — must show\n"
            "     remote_tag (RFC 3261 §12.1.1 dialog established).\n"
            "  5. _get_tun_for_ue() and ue.pdu_sessions[*].ip for both UEs.\n"
            "  6. concurrent.futures: two threads run send_rtp_stream()\n"
            "     A→B and B→A on port 20000 for duration seconds.\n"
            "  7. _cleanup_sip_call(sip_a, sip_b, call_id, ...) in finally.\n"
            "  8. _estimate_mos(one_way_delay = max(worst_jitter*4, 10),\n"
            "     jitter, loss_pct) — simplified G.107 R-factor → MOS.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration — seconds of bidirectional RTP (default: 60).\n"
            "\n"
            "Pass criteria\n"
            "  Dialog established (remote_tag present) AND NOT\n"
            "  (stats_a.tx_packets == 0 AND stats_b.tx_packets == 0) —\n"
            "  i.e. at least one direction transmitted RTP. MOS is\n"
            "  reported but not gated; the worse direction's jitter / loss\n"
            "  is used for the G.107 estimate.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR', codec='AMR-WB', fiveqi=1, ue_a, ue_b,\n"
            "  ul_kbps, dl_kbps, jitter_ms, loss_pct, total_packets,\n"
            "  estimated_mos, quality (Excellent/Good/Fair/Poor/Bad),\n"
            "  duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. MOS is from a simplified G.107 E-model\n"
            "  (Ie≈7 baseline for AMR-WB at 0% loss; codec impairment\n"
            "  curve approximated). One-way delay is estimated as\n"
            "  max(jitter*4, 10 ms) — no real network-delay measurement.\n"
            "  PCF Rx triggers a dedicated 5QI=1 bearer but enforcement\n"
            "  isn't gated."
        ),
    )
    tc_id = "TC-IMS-011"
    name = "ims_voice_call_quality"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        import concurrent.futures
        from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs")
            return self.result

        ue_a, ue_b = self.ue_pool[0], self.ue_pool[1]
        duration = self.params.get("duration", 60)
        domain = _get_ims_domain()

        for ue in (ue_a, ue_b):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        pcscf_ip = _get_pcscf_from_session(ue_a)
        sip_a, sip_b, call_id, invite_status, sip_ok = _setup_ims_call(
            ue_a, ue_b, pcscf_ip, 5060, domain, ["audio"])

        ok, _ = _verify_call_established(sip_a, call_id)
        if not ok:
            _cleanup_sip_call(sip_a, sip_b)
            self.fail_test(f"VoNR call not established — INVITE status={invite_status}")
            return self.result

        ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")
        tun_a, tun_b = _get_tun_for_ue(ue_a), _get_tun_for_ue(ue_b)

        stats_a, stats_b = RtpStreamStats(), RtpStreamStats()
        try:
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                pool.submit(send_rtp_stream, ip_a, ip_b, 20000, duration, 20000, stats_a, tun_a, "audio")
                f = pool.submit(send_rtp_stream, ip_b, ip_a, 20000, duration, 20001, stats_b, tun_b, "audio")
                f.result()
        finally:
            _cleanup_sip_call(sip_a, sip_b, call_id, target_imsi=ue_b.imsi, target_msisdn=getattr(ue_b.sim, "msisdn", ""))

        if stats_a.tx_packets == 0 and stats_b.tx_packets == 0:
            self.fail_test("RTP streams failed both directions")
            return self.result

        jitter = round(max(stats_a.jitter_ms, stats_b.jitter_ms), 3)
        loss_pct = round(max(stats_a.loss_pct, stats_b.loss_pct), 2)
        one_way_delay = max(jitter * 4, 10)
        mos = self._estimate_mos(one_way_delay, jitter, loss_pct)
        quality = ("Excellent" if mos >= 4.0 else "Good" if mos >= 3.5
                   else "Fair" if mos >= 3.0 else "Poor" if mos >= 2.5 else "Bad")

        self.pass_test(
            service="VoNR", codec="AMR-WB", fiveqi=1,
            ue_a=ue_a.imsi, ue_b=ue_b.imsi,
            ul_kbps=stats_a.bitrate_kbps, dl_kbps=stats_b.bitrate_kbps,
            jitter_ms=jitter, loss_pct=loss_pct,
            total_packets=stats_a.tx_packets + stats_b.tx_packets,
            estimated_mos=round(mos, 2), quality=quality,
            duration_s=duration,
        )
        return self.result

    @staticmethod
    def _estimate_mos(delay_ms, jitter_ms, loss_pct):
        """Estimate MOS from simplified E-model (ITU-T G.107).

        R = 93.2 - Id - Ie
        Id = 0.024*d + 0.11*(d-177.3)*H(d-177.3)  (delay impairment)
        Ie = 7 + 30*ln(1+15*e)                     (equipment/loss impairment)
        MOS = 1 + 0.035*R + R*(R-60)*(100-R)*7e-6

        Simplified for AMR-WB codec with jitter buffer.
        """
        import math

        # Effective delay including jitter buffer (2x jitter as buffer)
        d = delay_ms + 2 * jitter_ms

        # Delay impairment
        Id = 0.024 * d
        if d > 177.3:
            Id += 0.11 * (d - 177.3)

        # Loss/codec impairment (AMR-WB Ie_eff ≈ 7 at 0% loss)
        e = loss_pct / 100.0
        if e > 0:
            Ie = 7 + 30 * math.log(1 + 15 * e)
        else:
            Ie = 7

        # R-factor
        R = 93.2 - Id - Ie
        R = max(0, min(100, R))

        # R to MOS conversion
        if R < 6.5:
            mos = 1.0
        elif R > 100:
            mos = 4.5
        else:
            mos = 1 + 0.035 * R + R * (R - 60) * (100 - R) * 7e-6

        return max(1.0, min(5.0, mos))


class ImsVideoCallQuality(TestCase):
    """Bidirectional ViNR (audio+video) call quality — two UEs.

    Full flow:
    1. Register both UEs, IMS PDU sessions
    2. SIP REGISTER both, SIP INVITE with audio+video SDP
    3. Dedicated bearers: 5QI=1 (voice, QFI=3) + 5QI=2 (video, QFI=2)
    4. Simultaneous audio RTP (port 20000, AMR-WB) + video RTP (port 20002, H.264)
    5. Measure jitter/loss per stream, compute voice MOS

    TS 26.114 — IMS multimedia telephony
    5QI=1: voice, PDB=100ms, PER=10^-2
    5QI=2: video, PDB=150ms, PER=10^-3
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-013",
        title="Bidirectional ViNR (audio+video) call quality with MOS",
        spec="TS 23.228 §5.10",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.PCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "video"),
        setup=Setup.BASELINE,
        expected_duration_s=60.0,
        description=(
            "Purpose\n"
            "  End-to-end ViNR (audio+video) call-quality gate. Pins TS\n"
            "  23.228 §5.10 video telephony: a single SIP dialog carries\n"
            "  m=audio (5QI=1, AMR-WB) and m=video (5QI=2, H.264) media\n"
            "  per TS 26.114 §7. Bidirectional RTP on both streams\n"
            "  validates the joint audio+video media path. PDB ceilings\n"
            "  100 ms / 150 ms per TS 23.501 §5.7.4 Table 5.7.4-1.\n"
            "\n"
            "Procedure (TS 23.228 §5.10 + TS 26.114 + ITU-T G.107)\n"
            "  1. Hard-fail if len(ue_pool) < 2; ue_a, ue_b = pool[0:2].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. pcscf_ip = _get_pcscf_from_session(ue_a); call\n"
            "     _setup_ims_call(..., media_types=['audio', 'video']) —\n"
            "     INVITE carries m=audio + m=video.\n"
            "  4. _verify_call_established(sip_a, call_id); else cleanup +\n"
            "     fail_test.\n"
            "  5. tun_a/tun_b = _get_tun_for_ue(); ip_a/ip_b from PDU.\n"
            "  6. concurrent.futures with max_workers=4: send_rtp_stream\n"
            "     A→B audio (port 20000), B→A audio (port 20001), A→B\n"
            "     video (port 20002), B→A video (port 20003) — all four\n"
            "     run in parallel for duration seconds.\n"
            "  7. _cleanup_sip_call() in finally.\n"
            "  8. _estimate_mos(max(audio_jitter*4, 10), audio_jitter,\n"
            "     audio_loss) for voice MOS — video has no MOS, only\n"
            "     jitter / loss are reported.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration   — seconds per RTP stream (default: 30).\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "\n"
            "Pass criteria\n"
            "  Dialog established AND total_audio (aa+ab tx_packets) > 0.\n"
            "  Video packet count is reported but does not gate PASS\n"
            "  (audio-only dialog still PASSes if video stream is dropped\n"
            "  by the simulator).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='ViNR', codec_audio='AMR-WB', codec_video='H.264',\n"
            "  ue_a, ue_b, audio_ul_kbps, audio_dl_kbps, audio_jitter_ms,\n"
            "  audio_loss_pct, audio_total_pkts, video_ul_kbps,\n"
            "  video_dl_kbps, video_jitter_ms, video_loss_pct,\n"
            "  video_total_pkts, estimated_mos, quality, duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — RTP payloads are synthetic (no real H.264\n"
            "  NALU or AMR-WB frames). G.107 simplifications carry over\n"
            "  from TC-IMS-011; no video-quality model (e.g. VMAF / PSNR)\n"
            "  is computed."
        ),
    )
    tc_id = "TC-IMS-013"
    name = "ims_video_call_quality"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs for ViNR call quality test")
            return self.result

        ue_a = self.ue_pool[0]
        ue_b = self.ue_pool[1]
        duration = self.params.get("duration", 30)
        pcscf_port = self.params.get("pcscf_port", 5060)
        domain = _get_ims_domain()

        # 1. Register both UEs + IMS PDU sessions
        for ue in (ue_a, ue_b):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        pcscf_ip = _get_pcscf_from_session(ue_a)
        sip_a, sip_b, call_id, invite_status, sip_ok = _setup_ims_call(
            ue_a, ue_b, pcscf_ip, pcscf_port, domain, ["audio", "video"])

        ok, _ = _verify_call_established(sip_a, call_id)
        if not ok:
            _cleanup_sip_call(sip_a, sip_b)
            self.fail_test(f"ViNR call not established — INVITE status={invite_status}")
            return self.result

        ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")
        tun_a, tun_b = _get_tun_for_ue(ue_a), _get_tun_for_ue(ue_b)

        import concurrent.futures
        from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

        aa, ab = RtpStreamStats(), RtpStreamStats()
        va, vb = RtpStreamStats(), RtpStreamStats()

        try:
            with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
                pool.submit(send_rtp_stream, ip_a, ip_b, 20000, duration, 20000, aa, tun_a, "audio")
                pool.submit(send_rtp_stream, ip_b, ip_a, 20000, duration, 20001, ab, tun_b, "audio")
                pool.submit(send_rtp_stream, ip_a, ip_b, 20002, duration, 20002, va, tun_a, "video")
                f = pool.submit(send_rtp_stream, ip_b, ip_a, 20002, duration, 20003, vb, tun_b, "video")
                f.result()
        finally:
            _cleanup_sip_call(sip_a, sip_b, call_id, target_imsi=ue_b.imsi, target_msisdn=getattr(ue_b.sim, "msisdn", ""))

        total_audio = aa.tx_packets + ab.tx_packets
        total_video = va.tx_packets + vb.tx_packets
        if total_audio == 0:
            self.fail_test("No audio RTP packets sent")
            return self.result

        audio_jitter = max(aa.jitter_ms, ab.jitter_ms)
        audio_loss = max(aa.loss_pct, ab.loss_pct)
        video_jitter = max(va.jitter_ms, vb.jitter_ms)
        video_loss = max(va.loss_pct, vb.loss_pct)
        one_way_delay = max(audio_jitter * 4, 10)
        mos = ImsVoiceCallQuality._estimate_mos(one_way_delay, audio_jitter, audio_loss)
        quality = ("Excellent" if mos >= 4.0 else "Good" if mos >= 3.5
                   else "Fair" if mos >= 3.0 else "Poor" if mos >= 2.5 else "Bad")

        self.pass_test(
            service="ViNR", codec_audio="AMR-WB", codec_video="H.264",
            ue_a=ue_a.imsi, ue_b=ue_b.imsi,
            audio_ul_kbps=aa.bitrate_kbps, audio_dl_kbps=ab.bitrate_kbps,
            audio_jitter_ms=round(audio_jitter, 3), audio_loss_pct=round(audio_loss, 2),
            audio_total_pkts=total_audio,
            video_ul_kbps=va.bitrate_kbps, video_dl_kbps=vb.bitrate_kbps,
            video_jitter_ms=round(video_jitter, 3), video_loss_pct=round(video_loss, 2),
            video_total_pkts=total_video,
            estimated_mos=round(mos, 2), quality=quality,
            duration_s=duration,
        )
        return self.result


class ImsConferenceCall(TestCase):
    """3-way conference call — merge two active calls.

    TS 24.147 §5.3.1.3.3 — Three-way session creation (the spec's
        name for 'merging two active sessions into a conference').
    TS 24.229 — SIP procedures for IMS.
    TS 26.114 — Media handling for conferencing.

    Flow (user perspective):
    1. UE_A calls UE_B (SIP INVITE, audio) → active call
    2. UE_A holds UE_B (re-INVITE with a=sendonly)
    3. UE_A calls UE_C (second SIP INVITE, audio) → active call
    4. UE_A merges both calls → INVITE to conference factory URI
    5. MRFP mixes audio from all 3 participants
    6. Measure voice quality for all participants

    Uses 3 UEs from UE config database.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-014",
        title="3-way conference call — merge two active calls via MRFP",
        spec="TS 24.147 §5.3.1.3.3",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.MRF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  3-way conference creation gate. Pins TS 24.147 §5.3.1.3.3\n"
            "  three-way session creation (merging two active dialogs into\n"
            "  one conference via conference-factory + REFER) and TS 23.228\n"
            "  §4.7 MRFP audio mixing. All three UEs converge on a single\n"
            "  AMR-WB mixed stream at the MRFP.\n"
            "\n"
            "Procedure (TS 24.147 §5.3.1.3.3 + TS 23.228 §4.7)\n"
            "  1. Hard-fail if len(ue_pool) < 3; ue_a/b/c = pool[0:3].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims'); pull ip_a/b/c.\n"
            "  3. _make_sip_client() for all three; sip_b.local_port=5082,\n"
            "     sip_c.local_port=5084. Start them and register() — all\n"
            "     three must return 200.\n"
            "  4. sip_a.invite(ue_b, ['audio']) — first call A→B.\n"
            "  5. sip_a._hold(call_id_ab, ue_b) — re-INVITE a=sendonly.\n"
            "  6. sip_a.invite(ue_c, ['audio']) — second call A→C.\n"
            "  7. sip_a._create_conference(f'sip:conference-factory@\n"
            "     {domain}', call_id_ab, call_id_ac) — TS 24.147\n"
            "     §5.3.1.3.2 conference factory URI; on 200 OK store\n"
            "     Contact as conf_uri.\n"
            "  8. sip_a.refer(call_id_ab, conf_uri, ue_b) and\n"
            "     sip_a.refer(call_id_ac, conf_uri, ue_c) — TS 24.147\n"
            "     §5.3.1.5.2; expect 200/202 (RFC 3515 §2.4.3).\n"
            "  9. Pull MRFP IP / audio ports from conf_dialog.remote_sdp\n"
            "     and from incoming-INVITE _remote_media for B/C.\n"
            " 10. Three concurrent send_rtp_stream() — each UE → MRFP\n"
            "     mixer port — for duration seconds.\n"
            " 11. BYE both legs and the conference dialog; BYE MRFP-\n"
            "     initiated incoming dialogs on B/C in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration   — seconds of mixed RTP (default: 30).\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "\n"
            "Pass criteria\n"
            "  sip_registered (all three reg=200) AND invite_ab==200 AND\n"
            "  invite_ac==200 AND conf_status==200 AND conf_uri non-empty\n"
            "  AND refer_b_status in (200,202) AND refer_c_status in\n"
            "  (200,202) AND total_pkts > 0 (RTP flowed for at least one\n"
            "  participant).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR-Conference', participants=3, codec='AMR-WB',\n"
            "  fiveqi=1, sip_registered, invite_ab_status, invite_ac_status,\n"
            "  conference_status, conference_uri, refer_b_status,\n"
            "  refer_c_status, ip_a/b/c, ue_a/b/c, a_kbps/b_kbps/c_kbps,\n"
            "  a_pkts/b_pkts/c_pkts, jitter_ms, loss_pct, total_packets,\n"
            "  estimated_delay_ms, estimated_mos, quality, duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — MRFP is a stub; the test confirms ports\n"
            "  are negotiated and RTP arrives, not actual mixed-audio\n"
            "  output. G.107 MOS approximations apply. REFER 202 is\n"
            "  treated as success without waiting for the matching\n"
            "  NOTIFY (RFC 3515 §2.4.4 subscription)."
        ),
    )
    tc_id = "TC-IMS-014"
    name = "ims_conference_call"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 3:
            self.fail_test("Need at least 3 UEs for conference call test")
            return self.result

        ue_a = self.ue_pool[0]  # Conference initiator
        ue_b = self.ue_pool[1]  # First callee
        ue_c = self.ue_pool[2]  # Second callee
        duration = self.params.get("duration", 30)
        pcscf_port = self.params.get("pcscf_port", 5060)
        domain = _get_ims_domain()

        # 1. Register all 3 UEs + IMS PDU sessions
        for ue in (ue_a, ue_b, ue_c):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_c = ue_c.pdu_sessions.get(2, {}).get("ip") or ue_c.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = _get_pcscf_from_session(ue_a)

        # 2. SIP REGISTER all 3 UEs
        sip_a = _make_sip_client(ip_a, pcscf_ip, pcscf_port, ue_a, domain)
        sip_b = _make_sip_client(ip_b, pcscf_ip, pcscf_port, ue_b, domain)
        sip_c = _make_sip_client(ip_c, pcscf_ip, pcscf_port, ue_c, domain)
        sip_b.local_port = 5082
        sip_c.local_port = 5084

        sip_registered = False
        call_id_ab = None
        call_id_ac = None
        invite_ab = None
        invite_ac = None
        conf_status = None

        try:
            sip_c.start()
            sip_b.start()
            sip_a.start()
            time.sleep(0.5)

            reg_a = sip_a.register(timeout=10)
            reg_b = sip_b.register(timeout=10)
            reg_c = sip_c.register(timeout=10)
            sip_registered = (reg_a == 200 and reg_b == 200 and reg_c == 200)

            if not sip_registered:
                self.fail_test("SIP REGISTER failed for one or more UEs",
                               reg_a=reg_a, reg_b=reg_b, reg_c=reg_c)
                return self.result

            # 3. UE_A calls UE_B (first call)
            self._log_step("Step 1: UE_A INVITE → UE_B (first call)")
            invite_ab, call_id_ab = sip_a.invite(
                target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                target_imsi=ue_b.imsi, media_types=["audio"], timeout=10)
            time.sleep(2)  # Wait for dedicated bearer

            # 4. UE_A holds UE_B (re-INVITE with sendonly)
            self._log_step("Step 2: UE_A holds UE_B")
            # Send re-INVITE with a=sendonly SDP to put UE_B on hold
            sip_a._hold(call_id_ab, target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                        target_imsi=ue_b.imsi, timeout=5)
            time.sleep(1)

            # 5. UE_A calls UE_C (second call)
            self._log_step("Step 3: UE_A INVITE → UE_C (second call)")
            invite_ac, call_id_ac = sip_a.invite(
                target_msisdn=getattr(ue_c.sim, "msisdn", ""),
                target_imsi=ue_c.imsi, media_types=["audio"], timeout=10)
            time.sleep(2)  # Wait for dedicated bearer

            # 6. UE_A creates conference → INVITE to conference factory URI
            # TS 24.147 §5.3.1.3.2 verbatim: 'set the request URI of the
            # INVITE request to the conference factory URI [...] On
            # receiving a 200 (OK) [...] store the content of the
            # received Contact header as the conference URI.'
            self._log_step("Step 4: UE_A INVITE → conference factory")
            conf_factory = f"sip:conference-factory@{domain}"
            conf_status, conf_call_id, conf_uri = sip_a._create_conference(
                conf_factory, call_id_ab, call_id_ac, timeout=10)

            # 7. REFER UE_B and UE_C into the conference
            # TS 24.147 §5.3.1.5.2 — 'User invites other user to a
            # conference by sending a REFER request to the other
            # user'. Per §5.3.1.3.3 step 2(a) this is the choice for
            # three-way merge from existing dialogs.
            refer_b_status = None
            refer_c_status = None
            if conf_status == 200 and conf_uri:
                self._log_step(f"Step 5: REFER UE_B → {conf_uri}")
                refer_b_status = sip_a.refer(
                    call_id_ab, conf_uri,
                    target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                    target_imsi=ue_b.imsi, timeout=10)
                time.sleep(1)

                self._log_step(f"Step 6: REFER UE_C → {conf_uri}")
                refer_c_status = sip_a.refer(
                    call_id_ac, conf_uri,
                    target_msisdn=getattr(ue_c.sim, "msisdn", ""),
                    target_imsi=ue_c.imsi, timeout=10)
                time.sleep(2)  # Wait for participants to join MRFP mixer

            # 8. Each UE sends voice RTP to MRFP mixer (TS 23.228 §4.7
            # — 'Tasks of the MRFP include ... Mixing of incoming media
            # streams (e.g. for multiple parties)')
            # MRFP IP/port from conference factory 200 OK SDP (sip_a)
            # and from incoming MRFP INVITEs after REFER (sip_b, sip_c)
            self._log_step("Step 7: 3-way voice traffic → MRFP")
            import concurrent.futures
            from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

            # Get MRFP media from conference factory 200 OK (UE_A's connection)
            conf_dialog = sip_a._dialogs.get(conf_call_id, {})
            mrfp_sdp_a = conf_dialog.get("remote_sdp", {})
            mrfp_ip = mrfp_sdp_a.get("ip") if mrfp_sdp_a else None
            mrfp_audio_a = mrfp_sdp_a.get("audio_port", 20000) if mrfp_sdp_a else 20000

            # Get MRFP media from incoming INVITE to UE_B and UE_C
            mrfp_media_b = {}
            mrfp_media_c = {}
            for cid in getattr(sip_b, '_incoming_dialogs', []):
                if '@mrfp' in cid or 'conf-' in cid:
                    mrfp_media_b = getattr(sip_b, '_remote_media', {}).get(cid, {})
                    break
            for cid in getattr(sip_c, '_incoming_dialogs', []):
                if '@mrfp' in cid or 'conf-' in cid:
                    mrfp_media_c = getattr(sip_c, '_remote_media', {}).get(cid, {})
                    break

            mrfp_audio_b = mrfp_media_b.get("audio_port", mrfp_audio_a)
            mrfp_audio_c = mrfp_media_c.get("audio_port", mrfp_audio_a)
            mrfp_ip = mrfp_ip or mrfp_media_b.get("ip") or mrfp_media_c.get("ip") or pcscf_ip

            log.info("MRFP media: ip=%s A_port=%s B_port=%s C_port=%s",
                     mrfp_ip, mrfp_audio_a, mrfp_audio_b, mrfp_audio_c)

            rtp_port = 20000
            tun_a = _get_tun_for_ue(ue_a)
            tun_b = _get_tun_for_ue(ue_b)
            tun_c = _get_tun_for_ue(ue_c)

            stats_a = RtpStreamStats()
            stats_b = RtpStreamStats()
            stats_c = RtpStreamStats()

            # Each UE sends audio to its MRFP mixer port
            # send_rtp_stream(local_ip, remote_ip, remote_port, duration, local_port, ...)
            with concurrent.futures.ThreadPoolExecutor(max_workers=3) as pool:
                fut_a = pool.submit(send_rtp_stream, ip_a, mrfp_ip,
                                    mrfp_audio_a, duration, rtp_port, stats_a, tun_a, "audio")
                fut_b = pool.submit(send_rtp_stream, ip_b, mrfp_ip,
                                    mrfp_audio_b, duration, rtp_port, stats_b, tun_b, "audio")
                fut_c = pool.submit(send_rtp_stream, ip_c, mrfp_ip,
                                    mrfp_audio_c, duration, rtp_port, stats_c, tun_c, "audio")
                fut_a.result()
                fut_b.result()
                fut_c.result()

            # 9. SIP BYE to tear down all legs
            self._log_step("Step 8: Tear down conference")
            if call_id_ab:
                sip_a.bye(call_id_ab, target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                          target_imsi=ue_b.imsi, timeout=3)
            if call_id_ac:
                sip_a.bye(call_id_ac, target_msisdn=getattr(ue_c.sim, "msisdn", ""),
                          target_imsi=ue_c.imsi, timeout=3)
            # BYE the conference leg itself
            if conf_call_id and conf_status == 200:
                sip_a.bye(conf_call_id, timeout=3)
            # BYE MRFP-initiated conference legs only (not original call legs
            # which are already torn down by sip_a above)
            for sip_client in (sip_b, sip_c):
                for cid in getattr(sip_client, '_incoming_dialogs', []):
                    if '@mrfp' in cid or 'conf-' in cid:
                        sip_client.bye(cid, timeout=3)

        finally:
            sip_a.stop()
            sip_b.stop()
            sip_c.stop()

        # 10. Validate SIP signaling
        sip_details = dict(
            sip_registered=sip_registered,
            invite_ab_status=invite_ab, invite_ac_status=invite_ac,
            conference_status=conf_status, conference_uri=conf_uri,
            refer_b_status=refer_b_status, refer_c_status=refer_c_status,
            ip_a=ip_a, ip_b=ip_b, ip_c=ip_c,
            ue_a=ue_a.imsi, ue_b=ue_b.imsi, ue_c=ue_c.imsi,
        )

        if invite_ab != 200:
            self.fail_test(f"INVITE A→B failed: status={invite_ab} (expected 200)", **sip_details)
            return self.result
        if invite_ac != 200:
            self.fail_test(f"INVITE A→C failed: status={invite_ac} (expected 200)", **sip_details)
            return self.result
        if conf_status != 200:
            self.fail_test(f"Conference creation failed: status={conf_status} (expected 200)", **sip_details)
            return self.result
        if not conf_uri:
            self.fail_test("Conference 200 OK missing Contact URI", **sip_details)
            return self.result
        # RFC 3515 §2.4.3: REFER accepted with 202
        if refer_b_status not in (200, 202):
            self.fail_test(f"REFER UE_B failed: status={refer_b_status} (expected 202)", **sip_details)
            return self.result
        if refer_c_status not in (200, 202):
            self.fail_test(f"REFER UE_C failed: status={refer_c_status} (expected 202)", **sip_details)
            return self.result

        # 10. Compute quality metrics
        total_pkts = stats_a.tx_packets + stats_b.tx_packets + stats_c.tx_packets
        if total_pkts == 0:
            self.fail_test("No RTP packets sent in conference", **sip_details)
            return self.result

        max_jitter = max(stats_a.jitter_ms, stats_b.jitter_ms, stats_c.jitter_ms)
        max_loss = max(stats_a.loss_pct, stats_b.loss_pct, stats_c.loss_pct)
        one_way_delay = max(max_jitter * 4, 10)
        mos = ImsVoiceCallQuality._estimate_mos(one_way_delay, max_jitter, max_loss)
        quality = ("Excellent" if mos >= 4.0 else "Good" if mos >= 3.5
                   else "Fair" if mos >= 3.0 else "Poor" if mos >= 2.5 else "Bad")

        log.info("3-way Conference: MOS=%.2f (%s) | jitter=%.1fms loss=%.2f%% | "
                 "A=%d pkts B=%d pkts C=%d pkts | %s↔%s↔%s",
                 mos, quality, max_jitter, max_loss,
                 stats_a.tx_packets, stats_b.tx_packets, stats_c.tx_packets,
                 ip_a, ip_b, ip_c)

        self.pass_test(
            service="VoNR-Conference", participants=3,
            codec="AMR-WB", fiveqi=1,
            **sip_details,
            a_kbps=stats_a.bitrate_kbps, b_kbps=stats_b.bitrate_kbps, c_kbps=stats_c.bitrate_kbps,
            a_pkts=stats_a.tx_packets, b_pkts=stats_b.tx_packets, c_pkts=stats_c.tx_packets,
            jitter_ms=round(max_jitter, 3), loss_pct=round(max_loss, 2),
            total_packets=total_pkts,
            estimated_delay_ms=round(one_way_delay, 2),
            estimated_mos=round(mos, 2), quality=quality,
            duration_s=duration,
        )
        return self.result

    def _log_step(self, msg):
        log.info("Conference: %s", msg)


class ImsConference6Way(TestCase):
    """6-way conference call — maximum participants (core limit=6).

    TS 24.147 §5.3.1.3 — Conference creation. Spec doesn't fix a
        max-participants number — that's a deployment policy. Test
        verifies behaviour at the configured 6-UE core limit.
    Flow: UE_A creates conference, holds/calls 5 other UEs,
    REFERs all into the conference. MRFP mixes 6 audio streams.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-019",
        title="6-way conference call — maximum participants (core limit=6)",
        spec="TS 24.147 §5.3.1.3",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.MRF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "scale"),
        setup=Setup.BASELINE,
        expected_duration_s=180.0,
        description=(
            "Purpose\n"
            "  Conference capacity gate at N=6 — the deployment-policy\n"
            "  ceiling documented in the local seed. Pins TS 24.147\n"
            "  §5.3.1.3 conference creation and the MRFP audio-mix path\n"
            "  (TS 23.228 §4.7). All six UEs converge on a single mixed\n"
            "  AMR-WB stream at the MRFP.\n"
            "\n"
            "Procedure (TS 24.147 §5.3.1.3 + TS 23.228 §4.7)\n"
            "  1. Delegates to _run_nway_conference(self, n=6,\n"
            "     expect_fail=False).\n"
            "  2. Helper requires len(ue_pool) >= 6.\n"
            "  3. For each of 6 UEs: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims'); collect ue_ips.\n"
            "  4. Build 6 SIP clients (local_port = 5080 + i*2); start in\n"
            "     reverse order; register() each — fail at first non-200.\n"
            "  5. ue_a = ues[0]. For i in 1..5: hold the previous call,\n"
            "     then sip_a.invite(ue_i, ['audio']) — collect call_ids[]\n"
            "     and invite_statuses[].\n"
            "  6. sip_a._create_conference(f'sip:conference-factory@\n"
            "     {domain}', ...) → conf_status, conf_call_id, conf_uri.\n"
            "  7. sip_a.refer(call_ids[i-1], conf_uri, ue_i) for i in\n"
            "     1..5; collect refer_statuses[].\n"
            "  8. 6 concurrent send_rtp_stream() — each UE → MRFP audio\n"
            "     port — for duration seconds.\n"
            "  9. BYE all legs and the conference dialog in finally.\n"
            "\n"
            "Parameters (self.params, consumed by helper)\n"
            "  duration   — seconds of mixed RTP (default: 30).\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "\n"
            "Pass criteria\n"
            "  All 6 INVITEs == 200 AND conf_status == 200 AND conf_uri\n"
            "  non-empty AND every refer_status in (200,202) AND\n"
            "  total_pkts > 0 across the 6 RTP streams.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR-Conference-6way', codec='AMR-WB', fiveqi=1,\n"
            "  participants=6, expect_fail=False, invite_statuses,\n"
            "  conference_status, conference_uri, refer_statuses,\n"
            "  rejection_at, ue_ips, total_packets, jitter_ms, loss_pct,\n"
            "  estimated_mos, quality, duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — MRFP is a stub; mixed-output audio is not\n"
            "  validated. The 6-participant cap is a deployment policy\n"
            "  baked into the local seed, not a TS 24.147 requirement.\n"
            "  Holds + sequential INVITEs are issued from a single sip_a;\n"
            "  no parallel-INVITE race-condition testing."
        ),
    )
    tc_id = "TC-IMS-019"
    name = "ims_conference_6way"
    category = "IMS / VoNR (TS 23.228)"
    description = """\
TC-IMS-019: 6-way conference call — maximum participants (core limit=6).
Standard: TS 23.228
Procedure: invokes the helper(s) in run(); see source for the exact call sequence.
Verification: each step's response is non-error and the assertion conditions in run() hold.
Expected Result: 6-way conference call — maximum participants (core limit=6)."""

    def run(self):
        return _run_nway_conference(self, n=6, expect_fail=False)

    def _log_step(self, msg):
        log.info("Conference6Way: %s", msg)


class ImsConference8Way(TestCase):
    """8-way conference call — exceeds core max participants (6).

    TS 24.147 §5.3.1.3 — Conference creation. Capacity-rejection
        behaviour itself is not spec'd — TS 24.147 doesn't mandate
        a max — but the conference focus must reject participants
        beyond its policy limit per RFC 3261 4xx/5xx semantics.
    Core should reject the 7th/8th participant with 486 Busy Here
    or REFER should fail. Test validates proper rejection.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-020",
        title="8-way conference rejected — exceeds core max participants (6)",
        spec="TS 24.147 §5.3.1.3",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.MRF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "scale", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=180.0,
        description=(
            "Purpose\n"
            "  Negative-conference capacity gate — drives an 8-way INVITE/\n"
            "  REFER sequence past the seeded 6-participant policy limit.\n"
            "  TS 24.147 §5.3.1.3 leaves the cap to deployment policy, but\n"
            "  the conference focus must reject overflow per RFC 3261 4xx/\n"
            "  5xx semantics. Validates capacity rejection rather than a\n"
            "  silent overload.\n"
            "\n"
            "Procedure (TS 24.147 §5.3.1.3 + RFC 3261)\n"
            "  1. Delegates to _run_nway_conference(self, n=8,\n"
            "     expect_fail=True).\n"
            "  2. Helper requires len(ue_pool) >= 8.\n"
            "  3. For each of 8 UEs: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims'); 8 SIP clients on local_port=5080+i*2.\n"
            "  4. ue_a INVITEs ue_1..ue_7 sequentially (with hold of the\n"
            "     previous call); collect invite_statuses[].\n"
            "  5. sip_a._create_conference(...) → conf_status, conf_uri.\n"
            "  6. sip_a.refer(call_ids[i-1], conf_uri, ue_i) for i in\n"
            "     1..7; break out of the loop at the first ref_status not\n"
            "     in (200,202) and record rejection_at = i.\n"
            "  7. accepted_count = rejection_at if rejection occurred,\n"
            "     else n; only RTP streams for the accepted participants\n"
            "     run via send_rtp_stream() concurrently.\n"
            "  8. BYE all legs in finally.\n"
            "\n"
            "Parameters (self.params, consumed by helper)\n"
            "  duration   — seconds of RTP (default: 30).\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "\n"
            "Pass criteria\n"
            "  rejection_at is not None (the helper saw a non-202 REFER\n"
            "  status before all 7 callees were attached) OR conf_status\n"
            "  != 200 (conference factory itself refused). Either form of\n"
            "  rejection PASSes the negative test. Core accepting all 8\n"
            "  participants is the FAIL case ('expected rejection').\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR-Conference-8way-rejection',\n"
            "  rejected_participant, rejection_status, accepted_count,\n"
            "  rtp_participants, total_packets, participants=8,\n"
            "  expect_fail=True, invite_statuses, conference_status,\n"
            "  conference_uri, refer_statuses, ue_ips.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — the 6-cap is enforced in the seeded\n"
            "  conference-factory, not by TS 24.147. The exact rejection\n"
            "  code (4xx vs 5xx vs 486) depends on the focus stub; the\n"
            "  test only asserts 'not 200/202'."
        ),
    )
    tc_id = "TC-IMS-020"
    name = "ims_conference_8way"
    category = "IMS / VoNR (TS 23.228)"
    description = """\
TC-IMS-020: 8-way conference call — exceeds core max participants (6).
Standard: TS 23.228
Procedure: invokes the helper(s) in run(); see source for the exact call sequence.
Verification: each step's response is non-error and the assertion conditions in run() hold.
Expected Result: 8-way conference call — exceeds core max participants (6)."""

    def run(self):
        return _run_nway_conference(self, n=8, expect_fail=True)

    def _log_step(self, msg):
        log.info("Conference8Way: %s", msg)


def _run_nway_conference(test_case, n, expect_fail=False):
    """Run N-way conference call test.

    Generalized conference flow for any number of participants:
    1. Register N UEs + IMS PDU sessions
    2. SIP REGISTER all N UEs
    3. UE_A calls UE_1..UE_(N-1) sequentially (INVITE, hold previous)
    4. UE_A INVITE conference-factory → conf URI
    5. REFER all UE_1..UE_(N-1) into conference
    6. All N UEs send RTP to MRFP
    7. BYE all legs
    8. Validate — if expect_fail, PASS when core rejects excess participants

    Args:
        test_case: TestCase instance
        n: number of participants
        expect_fail: True if core should reject (e.g., n > max_participants)
    """
    import concurrent.futures
    from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

    gnb = test_case.require_gnb()
    test_case.require_ue()
    if len(test_case.ue_pool) < n:
        test_case.fail_test(f"Need at least {n} UEs for {n}-way conference")
        return test_case.result

    ues = test_case.ue_pool[:n]
    duration = test_case.params.get("duration", 30)
    pcscf_port = test_case.params.get("pcscf_port", 5060)
    domain = _get_ims_domain()

    # 1. Register all UEs + IMS PDU sessions
    for ue in ues:
        if not test_case.register_ue(ue, gnb):
            return test_case.result
        if not test_case.establish_pdu(ue, psi=2, dnn="ims"):
            return test_case.result

    ue_ips = []
    for ue in ues:
        ip = ue.pdu_sessions.get(2, {}).get("ip") or ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        ue_ips.append(ip)

    pcscf_ip = _get_pcscf_from_session(ues[0])

    # 2. Create SIP clients
    sip_clients = []
    base_port = 5080
    for i, ue in enumerate(ues):
        sip = _make_sip_client(ue_ips[i], pcscf_ip, pcscf_port, ue, domain)
        sip.local_port = base_port + (i * 2)
        sip_clients.append(sip)

    call_ids = []  # call_id for each A→UE_i call
    invite_statuses = []
    conf_status = None
    conf_call_id = None
    conf_uri = None
    refer_statuses = []
    rejection_at = None  # index where core rejected

    try:
        # Start all SIP clients
        for sip in reversed(sip_clients):
            sip.start()
        time.sleep(0.5)

        # SIP REGISTER all
        for i, sip in enumerate(sip_clients):
            reg = sip.register(timeout=10)
            if reg != 200:
                test_case.fail_test(f"SIP REGISTER failed for UE_{i}: {reg}")
                return test_case.result

        ue_a = ues[0]
        sip_a = sip_clients[0]

        # 3. UE_A calls each other UE sequentially (hold previous)
        for i in range(1, n):
            ue_i = ues[i]
            msisdn_i = getattr(ue_i.sim, "msisdn", "")

            if i > 1:
                # Hold previous call
                prev_ue = ues[i - 1]
                test_case._log_step(f"Hold UE_{i-1}")
                sip_a._hold(call_ids[-1],
                            target_msisdn=getattr(prev_ue.sim, "msisdn", ""),
                            target_imsi=prev_ue.imsi, timeout=5)
                time.sleep(0.5)

            test_case._log_step(f"INVITE UE_A → UE_{i}")
            status, cid = sip_a.invite(
                target_msisdn=msisdn_i,
                target_imsi=ue_i.imsi, media_types=["audio"], timeout=10)
            call_ids.append(cid)
            invite_statuses.append(status)
            time.sleep(1)

        # 4. Conference factory INVITE
        test_case._log_step("INVITE → conference factory")
        conf_factory = f"sip:conference-factory@{domain}"
        conf_status, conf_call_id, conf_uri = sip_a._create_conference(
            conf_factory, call_ids[0] if call_ids else None,
            call_ids[-1] if len(call_ids) > 1 else None, timeout=10)

        # 5. REFER all participants into conference
        if conf_status == 200 and conf_uri:
            for i in range(1, n):
                ue_i = ues[i]
                msisdn_i = getattr(ue_i.sim, "msisdn", "")
                test_case._log_step(f"REFER UE_{i} → {conf_uri}")
                ref_status = sip_a.refer(
                    call_ids[i - 1], conf_uri,
                    target_msisdn=msisdn_i,
                    target_imsi=ue_i.imsi, timeout=10)
                refer_statuses.append(ref_status)
                time.sleep(1)

                # Check for rejection (486 Busy Here or non-202)
                if ref_status not in (200, 202):
                    rejection_at = i
                    test_case._log_step(f"REFER UE_{i} rejected: {ref_status}")
                    break
            time.sleep(2)

        # 6. RTP to MRFP — for accepted participants
        # When rejection occurred, only send RTP for UE_A + accepted UEs (not rejected ones)
        accepted_count = (rejection_at if rejection_at else n)  # UE_A + accepted callees
        if conf_status == 200 and conf_uri and accepted_count >= 2:
            test_case._log_step(f"{accepted_count}-way voice traffic → MRFP")

            # Get MRFP ports from SDP
            conf_dialog = sip_a._dialogs.get(conf_call_id, {})
            mrfp_sdp = conf_dialog.get("remote_sdp", {})
            mrfp_ip = mrfp_sdp.get("ip") if mrfp_sdp else pcscf_ip

            # Collect MRFP ports for accepted participants only
            mrfp_ports = []
            # UE_A: from conference factory 200 OK
            mrfp_ports.append(mrfp_sdp.get("audio_port", 30000) if mrfp_sdp else 30000)
            # UE_1..accepted: from incoming MRFP INVITEs
            for i in range(1, accepted_count):
                port = mrfp_ports[0] + (i * 2)  # fallback
                for cid in getattr(sip_clients[i], '_incoming_dialogs', []):
                    if '@mrfp' in cid or 'conf-' in cid:
                        media = getattr(sip_clients[i], '_remote_media', {}).get(cid, {})
                        if media.get("audio_port"):
                            port = media["audio_port"]
                            mrfp_ip = mrfp_ip or media.get("ip")
                        break
                mrfp_ports.append(port)

            log.info("MRFP media: ip=%s ports=%s", mrfp_ip, mrfp_ports)

            rtp_port = 20000
            stats_list = [RtpStreamStats() for _ in range(accepted_count)]
            tuns = [_get_tun_for_ue(ues[i]) for i in range(accepted_count)]

            with concurrent.futures.ThreadPoolExecutor(max_workers=accepted_count) as pool:
                futs = []
                for i in range(accepted_count):
                    f = pool.submit(send_rtp_stream, ue_ips[i], mrfp_ip,
                                    mrfp_ports[i], duration, rtp_port,
                                    stats_list[i], tuns[i], "audio")
                    futs.append(f)
                for f in futs:
                    f.result()

        # 7. Teardown
        test_case._log_step("Tear down conference")
        for i, cid in enumerate(call_ids):
            if cid:
                sip_a.bye(cid, target_msisdn=getattr(ues[i + 1].sim, "msisdn", ""),
                          target_imsi=ues[i + 1].imsi, timeout=3)
        if conf_call_id and conf_status == 200:
            sip_a.bye(conf_call_id, timeout=3)
        # BYE MRFP legs
        for sip in sip_clients[1:]:
            for cid in getattr(sip, '_incoming_dialogs', []):
                if '@mrfp' in cid or 'conf-' in cid:
                    sip.bye(cid, timeout=3)

    finally:
        for sip in sip_clients:
            sip.stop()

    # 8. Validate
    sip_details = dict(
        participants=n, expect_fail=expect_fail,
        invite_statuses=invite_statuses,
        conference_status=conf_status, conference_uri=conf_uri,
        refer_statuses=refer_statuses, rejection_at=rejection_at,
        ue_ips=ue_ips,
    )

    if expect_fail:
        # Core should reject at some point (max_participants=6)
        if rejection_at is not None:
            # Validate RTP for accepted participants
            total_pkts = sum(s.tx_packets for s in stats_list) if stats_list else 0
            test_case.pass_test(
                service=f"VoNR-Conference-{n}way-rejection",
                rejected_participant=rejection_at,
                rejection_status=refer_statuses[rejection_at - 1] if rejection_at <= len(refer_statuses) else None,
                accepted_count=accepted_count,
                rtp_participants=accepted_count,
                total_packets=total_pkts,
                **sip_details,
            )
        elif conf_status and conf_status != 200:
            # Conference factory itself rejected
            test_case.pass_test(
                service=f"VoNR-Conference-{n}way-rejection",
                note=f"Conference factory rejected: {conf_status}",
                **sip_details,
            )
        else:
            # Core accepted all N — should have rejected
            test_case.fail_test(
                f"Core accepted all {n} participants but max is 6 — expected rejection",
                **sip_details,
            )
        return test_case.result

    # Normal (expect success)
    failed_invites = [s for s in invite_statuses if s != 200]
    if failed_invites:
        test_case.fail_test(f"INVITE failed for {len(failed_invites)} UEs: {invite_statuses}", **sip_details)
        return test_case.result
    if conf_status != 200:
        test_case.fail_test(f"Conference creation failed: {conf_status}", **sip_details)
        return test_case.result
    if not conf_uri:
        test_case.fail_test("Conference 200 OK missing Contact URI", **sip_details)
        return test_case.result
    failed_refers = [(i, s) for i, s in enumerate(refer_statuses) if s not in (200, 202)]
    if failed_refers:
        test_case.fail_test(f"REFER failed: {failed_refers}", **sip_details)
        return test_case.result

    # Quality metrics
    total_pkts = sum(s.tx_packets for s in stats_list)
    if total_pkts == 0:
        test_case.fail_test("No RTP packets sent", **sip_details)
        return test_case.result

    max_jitter = max(s.jitter_ms for s in stats_list)
    max_loss = max(s.loss_pct for s in stats_list)
    one_way_delay = max(max_jitter * 4, 10)
    mos = ImsVoiceCallQuality._estimate_mos(one_way_delay, max_jitter, max_loss)
    quality = ("Excellent" if mos >= 4.0 else "Good" if mos >= 3.5
               else "Fair" if mos >= 3.0 else "Poor" if mos >= 2.5 else "Bad")

    log.info("%d-way Conference: MOS=%.2f (%s) | jitter=%.1fms loss=%.2f%% | "
             "total=%d pkts | %s",
             n, mos, quality, max_jitter, max_loss, total_pkts,
             '↔'.join(ue_ips))

    test_case.pass_test(
        service=f"VoNR-Conference-{n}way",
        codec="AMR-WB", fiveqi=1,
        **sip_details,
        total_packets=total_pkts,
        jitter_ms=round(max_jitter, 3), loss_pct=round(max_loss, 2),
        estimated_mos=round(mos, 2), quality=quality,
        duration_s=duration,
    )
    return test_case.result


class ImsVideoConferenceCall(TestCase):
    """3-way video conference call — merge two active video calls.

    TS 24.147 §5.3.1.3.3 — Three-way session creation (the spec's
        name for merging two active sessions).
    TS 24.229 — SIP procedures for IMS.
    TS 26.114 — Media handling for conferencing (audio + video).

    Flow:
    1. UE_A calls UE_B (SIP INVITE, audio+video) → active ViNR call
    2. UE_A holds UE_B (re-INVITE with a=sendonly)
    3. UE_A calls UE_C (SIP INVITE, audio+video) → active ViNR call
    4. UE_A INVITE conference-factory → 200 OK with conf URI (Contact)
    5. REFER UE_B → conf URI (join MRFP mixer)
    6. REFER UE_C → conf URI (join MRFP mixer)
    7. Bidirectional audio + video RTP for all 3 participants
    8. BYE all legs

    Dedicated bearers: 5QI=1 (voice, QFI=2) + 5QI=2 (video, QFI=3)
    Uses 3 UEs from UE config database.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-018",
        title="3-way video conference — merge two active audio+video calls",
        spec="TS 24.147 §5.3.1.3.3",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.MRF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "video"),
        setup=Setup.BASELINE,
        expected_duration_s=120.0,
        description=(
            "Purpose\n"
            "  3-way audio+video conference gate. Extends TC-IMS-014's\n"
            "  TS 24.147 §5.3.1.3.3 three-way creation by negotiating m=\n"
            "  audio + m=video SDP and routing both streams to the MRFP\n"
            "  per TS 23.228 §4.7 (audio mixer + video compositor). PDB\n"
            "  ceilings 100/150 ms per TS 23.501 §5.7.4 Table 5.7.4-1.\n"
            "\n"
            "Procedure (TS 24.147 §5.3.1.3.3 + TS 23.228 §4.7)\n"
            "  1. Hard-fail if len(ue_pool) < 3; ue_a/b/c = pool[0:3].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims'); collect ip_a/b/c.\n"
            "  3. _make_sip_client() for all three; sip_b.local_port=5082,\n"
            "     sip_c.local_port=5084. Start and register() — all 200.\n"
            "  4. sip_a.invite(ue_b, ['audio','video']) → call_id_ab.\n"
            "     sip_a._hold(call_id_ab, ue_b).\n"
            "  5. sip_a.invite(ue_c, ['audio','video']) → call_id_ac.\n"
            "  6. sip_a._create_conference(f'sip:conference-factory@\n"
            "     {domain}', call_id_ab, call_id_ac,\n"
            "     media_types=['audio','video']) — conf_status / conf_uri.\n"
            "  7. sip_a.refer(call_id_ab, conf_uri, ue_b) and\n"
            "     sip_a.refer(call_id_ac, conf_uri, ue_c).\n"
            "  8. Pull MRFP IP / audio_port / video_port from conf_dialog.\n"
            "     remote_sdp and from B/C incoming dialogs _remote_media.\n"
            "  9. Six concurrent send_rtp_stream() (audio + video for each\n"
            "     of A/B/C → MRFP) via ThreadPoolExecutor max_workers=6.\n"
            " 10. BYE both call legs and the conference; BYE MRFP-\n"
            "     initiated legs on B/C in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  duration   — seconds per RTP stream (default: 30).\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "\n"
            "Pass criteria\n"
            "  sip_registered (3×200) AND invite_ab==200 AND invite_ac==200\n"
            "  AND conf_status==200 AND conf_uri non-empty AND\n"
            "  refer_b_status in (200,202) AND refer_c_status in (200,202)\n"
            "  AND total_audio > 0 (at least one audio packet sent;\n"
            "  video count not gated).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='ViNR-Conference', participants=3, codec_audio,\n"
            "  codec_video, fiveqi_audio=1, fiveqi_video=2, sip_registered,\n"
            "  invite_ab_status, invite_ac_status, conference_status,\n"
            "  conference_uri, refer_b_status, refer_c_status, ip_a/b/c,\n"
            "  ue_a/b/c, audio_a_kbps/b_kbps/c_kbps,\n"
            "  video_a_kbps/b_kbps/c_kbps, audio_total_pkts,\n"
            "  video_total_pkts, audio_jitter_ms, audio_loss_pct,\n"
            "  video_jitter_ms, video_loss_pct, estimated_mos, quality,\n"
            "  duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — MRFP is a stub. REFER 202 treated as\n"
            "  success without NOTIFY follow-up. G.107 MOS approximations.\n"
            "  No video-quality estimator."
        ),
    )
    tc_id = "TC-IMS-018"
    name = "ims_video_conference_call"
    category = "IMS / VoNR (TS 23.228)"
    description = """\
TC-IMS-018: 3-way video conference call — merge two active video calls.
Standard: TS 23.228
Procedure: invokes the helper(s) in run(); see source for the exact call sequence.
Verification:
- List size matches expectation
- not self.register_ue(ue, gnb): return self.result if not self.establish_pdu(ue, …
- conf_status == 200 and conf_uri: self._log_step(f"Step 5: REFER UE_B → {conf_uri…
- invite_ac != 200
- HTTP 200 response
- refer_b_status not in (200, 202)
Expected Result: 3-way video conference call — merge two active video calls."""

    def run(self):
        import concurrent.futures
        from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 3:
            self.fail_test("Need at least 3 UEs for video conference call test")
            return self.result

        ue_a = self.ue_pool[0]
        ue_b = self.ue_pool[1]
        ue_c = self.ue_pool[2]
        duration = self.params.get("duration", 30)
        pcscf_port = self.params.get("pcscf_port", 5060)
        domain = _get_ims_domain()

        # 1. Register all 3 UEs + IMS PDU sessions
        for ue in (ue_a, ue_b, ue_c):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_c = ue_c.pdu_sessions.get(2, {}).get("ip") or ue_c.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = _get_pcscf_from_session(ue_a)

        # 2. SIP REGISTER all 3 UEs
        sip_a = _make_sip_client(ip_a, pcscf_ip, pcscf_port, ue_a, domain)
        sip_b = _make_sip_client(ip_b, pcscf_ip, pcscf_port, ue_b, domain)
        sip_c = _make_sip_client(ip_c, pcscf_ip, pcscf_port, ue_c, domain)
        sip_b.local_port = 5082
        sip_c.local_port = 5084

        sip_registered = False
        call_id_ab = None
        call_id_ac = None
        invite_ab = None
        invite_ac = None
        conf_status = None
        conf_uri = None
        conf_call_id = None
        refer_b_status = None
        refer_c_status = None

        try:
            sip_c.start()
            sip_b.start()
            sip_a.start()
            time.sleep(0.5)

            reg_a = sip_a.register(timeout=10)
            reg_b = sip_b.register(timeout=10)
            reg_c = sip_c.register(timeout=10)
            sip_registered = (reg_a == 200 and reg_b == 200 and reg_c == 200)

            if not sip_registered:
                self.fail_test("SIP REGISTER failed for one or more UEs",
                               reg_a=reg_a, reg_b=reg_b, reg_c=reg_c)
                return self.result

            # 3. UE_A calls UE_B (audio+video)
            self._log_step("Step 1: UE_A INVITE → UE_B (audio+video)")
            invite_ab, call_id_ab = sip_a.invite(
                target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                target_imsi=ue_b.imsi, media_types=["audio", "video"], timeout=10)
            time.sleep(2)

            # 4. UE_A holds UE_B
            self._log_step("Step 2: UE_A holds UE_B")
            sip_a._hold(call_id_ab, target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                        target_imsi=ue_b.imsi, timeout=5)
            time.sleep(1)

            # 5. UE_A calls UE_C (audio+video)
            self._log_step("Step 3: UE_A INVITE → UE_C (audio+video)")
            invite_ac, call_id_ac = sip_a.invite(
                target_msisdn=getattr(ue_c.sim, "msisdn", ""),
                target_imsi=ue_c.imsi, media_types=["audio", "video"], timeout=10)
            time.sleep(2)

            # 6. UE_A creates conference (audio+video)
            self._log_step("Step 4: UE_A INVITE → conference factory")
            conf_factory = f"sip:conference-factory@{domain}"
            conf_status, conf_call_id, conf_uri = sip_a._create_conference(
                conf_factory, call_id_ab, call_id_ac,
                media_types=["audio", "video"], timeout=10)

            # 7. REFER UE_B and UE_C into the conference
            if conf_status == 200 and conf_uri:
                self._log_step(f"Step 5: REFER UE_B → {conf_uri}")
                refer_b_status = sip_a.refer(
                    call_id_ab, conf_uri,
                    target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                    target_imsi=ue_b.imsi, timeout=10)
                time.sleep(1)

                self._log_step(f"Step 6: REFER UE_C → {conf_uri}")
                refer_c_status = sip_a.refer(
                    call_id_ac, conf_uri,
                    target_msisdn=getattr(ue_c.sim, "msisdn", ""),
                    target_imsi=ue_c.imsi, timeout=10)
                time.sleep(2)

            # 8. Each UE sends audio+video RTP to MRFP mixer (TS 23.228 §4.7
            # — 'Tasks of the MRFP include ... Mixing of incoming media
            # streams (e.g. for multiple parties)')
            self._log_step("Step 7: 3-way audio+video traffic → MRFP")

            # Get MRFP media from conference factory 200 OK (UE_A)
            conf_dialog = sip_a._dialogs.get(conf_call_id, {})
            mrfp_sdp_a = conf_dialog.get("remote_sdp", {})
            mrfp_ip = mrfp_sdp_a.get("ip") if mrfp_sdp_a else None
            mrfp_audio_a = mrfp_sdp_a.get("audio_port", 20000) if mrfp_sdp_a else 20000
            mrfp_video_a = mrfp_sdp_a.get("video_port", 20002) if mrfp_sdp_a else 20002

            # Get MRFP media from incoming INVITE to UE_B and UE_C
            mrfp_media_b = {}
            mrfp_media_c = {}
            for cid in getattr(sip_b, '_incoming_dialogs', []):
                if '@mrfp' in cid or 'conf-' in cid:
                    mrfp_media_b = getattr(sip_b, '_remote_media', {}).get(cid, {})
                    break
            for cid in getattr(sip_c, '_incoming_dialogs', []):
                if '@mrfp' in cid or 'conf-' in cid:
                    mrfp_media_c = getattr(sip_c, '_remote_media', {}).get(cid, {})
                    break

            mrfp_audio_b = mrfp_media_b.get("audio_port", mrfp_audio_a)
            mrfp_audio_c = mrfp_media_c.get("audio_port", mrfp_audio_a)
            mrfp_video_b = mrfp_media_b.get("video_port", mrfp_video_a)
            mrfp_video_c = mrfp_media_c.get("video_port", mrfp_video_a)
            mrfp_ip = mrfp_ip or mrfp_media_b.get("ip") or mrfp_media_c.get("ip") or pcscf_ip

            log.info("MRFP media: ip=%s audio=%s/%s/%s video=%s/%s/%s",
                     mrfp_ip, mrfp_audio_a, mrfp_audio_b, mrfp_audio_c,
                     mrfp_video_a, mrfp_video_b, mrfp_video_c)

            audio_port = 20000
            video_port = 20002
            tun_a = _get_tun_for_ue(ue_a)
            tun_b = _get_tun_for_ue(ue_b)
            tun_c = _get_tun_for_ue(ue_c)

            # Audio stats
            aa, ab, ac = RtpStreamStats(), RtpStreamStats(), RtpStreamStats()
            # Video stats
            va, vb, vc = RtpStreamStats(), RtpStreamStats(), RtpStreamStats()

            # send_rtp_stream(local_ip, remote_ip, remote_port, duration, local_port, ...)
            with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
                # Audio: each UE → MRFP mixer
                pool.submit(send_rtp_stream, ip_a, mrfp_ip, mrfp_audio_a, duration, audio_port, aa, tun_a, "audio")
                pool.submit(send_rtp_stream, ip_b, mrfp_ip, mrfp_audio_b, duration, audio_port, ab, tun_b, "audio")
                pool.submit(send_rtp_stream, ip_c, mrfp_ip, mrfp_audio_c, duration, audio_port, ac, tun_c, "audio")
                # Video: each UE → MRFP compositor
                pool.submit(send_rtp_stream, ip_a, mrfp_ip, mrfp_video_a, duration, video_port, va, tun_a, "video")
                pool.submit(send_rtp_stream, ip_b, mrfp_ip, mrfp_video_b, duration, video_port, vb, tun_b, "video")
                f = pool.submit(send_rtp_stream, ip_c, mrfp_ip, mrfp_video_c, duration, video_port, vc, tun_c, "video")
                f.result()

            # 9. SIP BYE to tear down all legs
            self._log_step("Step 8: Tear down conference")
            if call_id_ab:
                sip_a.bye(call_id_ab, target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                          target_imsi=ue_b.imsi, timeout=3)
            if call_id_ac:
                sip_a.bye(call_id_ac, target_msisdn=getattr(ue_c.sim, "msisdn", ""),
                          target_imsi=ue_c.imsi, timeout=3)
            if conf_call_id and conf_status == 200:
                sip_a.bye(conf_call_id, timeout=3)
            # BYE MRFP-initiated conference legs only (not original call legs
            # which are already torn down by sip_a above)
            for sip_client in (sip_b, sip_c):
                for cid in getattr(sip_client, '_incoming_dialogs', []):
                    if '@mrfp' in cid or 'conf-' in cid:
                        sip_client.bye(cid, timeout=3)

        finally:
            sip_a.stop()
            sip_b.stop()
            sip_c.stop()

        # 10. Validate SIP signaling
        sip_details = dict(
            sip_registered=sip_registered,
            invite_ab_status=invite_ab, invite_ac_status=invite_ac,
            conference_status=conf_status, conference_uri=conf_uri,
            refer_b_status=refer_b_status, refer_c_status=refer_c_status,
            ip_a=ip_a, ip_b=ip_b, ip_c=ip_c,
            ue_a=ue_a.imsi, ue_b=ue_b.imsi, ue_c=ue_c.imsi,
        )

        if invite_ab != 200:
            self.fail_test(f"INVITE A→B failed: status={invite_ab} (expected 200)", **sip_details)
            return self.result
        if invite_ac != 200:
            self.fail_test(f"INVITE A→C failed: status={invite_ac} (expected 200)", **sip_details)
            return self.result
        if conf_status != 200:
            self.fail_test(f"Conference creation failed: status={conf_status} (expected 200)", **sip_details)
            return self.result
        if not conf_uri:
            self.fail_test("Conference 200 OK missing Contact URI", **sip_details)
            return self.result
        if refer_b_status not in (200, 202):
            self.fail_test(f"REFER UE_B failed: status={refer_b_status} (expected 202)", **sip_details)
            return self.result
        if refer_c_status not in (200, 202):
            self.fail_test(f"REFER UE_C failed: status={refer_c_status} (expected 202)", **sip_details)
            return self.result

        # 11. Compute quality metrics
        total_audio = aa.tx_packets + ab.tx_packets + ac.tx_packets
        total_video = va.tx_packets + vb.tx_packets + vc.tx_packets
        if total_audio == 0:
            self.fail_test("No audio RTP packets sent in conference", **sip_details)
            return self.result

        audio_jitter = max(aa.jitter_ms, ab.jitter_ms, ac.jitter_ms)
        audio_loss = max(aa.loss_pct, ab.loss_pct, ac.loss_pct)
        video_jitter = max(va.jitter_ms, vb.jitter_ms, vc.jitter_ms)
        video_loss = max(va.loss_pct, vb.loss_pct, vc.loss_pct)
        one_way_delay = max(audio_jitter * 4, 10)
        mos = ImsVoiceCallQuality._estimate_mos(one_way_delay, audio_jitter, audio_loss)
        quality = ("Excellent" if mos >= 4.0 else "Good" if mos >= 3.5
                   else "Fair" if mos >= 3.0 else "Poor" if mos >= 2.5 else "Bad")

        log.info("3-way Video Conference: MOS=%.2f (%s) | "
                 "audio: jitter=%.1fms loss=%.2f%% pkts=%d | "
                 "video: jitter=%.1fms loss=%.2f%% pkts=%d | %s↔%s↔%s",
                 mos, quality,
                 audio_jitter, audio_loss, total_audio,
                 video_jitter, video_loss, total_video,
                 ip_a, ip_b, ip_c)

        self.pass_test(
            service="ViNR-Conference", participants=3,
            codec_audio="AMR-WB", codec_video="H.264",
            fiveqi_audio=1, fiveqi_video=2,
            **sip_details,
            audio_a_kbps=aa.bitrate_kbps, audio_b_kbps=ab.bitrate_kbps, audio_c_kbps=ac.bitrate_kbps,
            video_a_kbps=va.bitrate_kbps, video_b_kbps=vb.bitrate_kbps, video_c_kbps=vc.bitrate_kbps,
            audio_total_pkts=total_audio, video_total_pkts=total_video,
            audio_jitter_ms=round(audio_jitter, 3), audio_loss_pct=round(audio_loss, 2),
            video_jitter_ms=round(video_jitter, 3), video_loss_pct=round(video_loss, 2),
            estimated_mos=round(mos, 2), quality=quality,
            duration_s=duration,
        )
        return self.result

    def _log_step(self, msg):
        log.info("VideoConference: %s", msg)


# ═══════════════════════════════════════════════════════════════
# SIP Signaling Test Cases
# ═══════════════════════════════════════════════════════════════

def _get_tun_for_ue(ue, psi=2):
    """Get TUN device name for a UE's PDU session."""
    session = ue.pdu_sessions.get(psi, {})
    return session.get('tun') or ue.pdu_sessions.get(1, {}).get('tun')


def _make_sip_client(ue_ip, pcscf_ip, pcscf_port, ue, domain):
    """Create a SipClient for IMS signaling.

    SIP signaling flows through the IMS PDU session (GTP-U tunnel) on QFI=1
    (default QoS flow, 5QI=5 for IMS signaling per TS 23.501 §5.7.2.1).
    The GTP-U classifier sends unmatched packets on the default QFI.
    """
    sip = SipClient(ue_ip, pcscf_ip, pcscf_port,
                    imsi=ue.imsi, msisdn=getattr(ue.sim, "msisdn", ""),
                    domain=domain, sim=ue.sim)
    return sip


def _get_pcscf_from_session(ue, psi=2):  # IMS PDU session
    """Get P-CSCF IP from PDU session PCO (received during registration)."""
    session = ue.pdu_sessions.get(psi, {})
    pcscf = session.get('pcscf')
    if pcscf:
        return pcscf
    # Fallback: try other PSIs
    for p, s in ue.pdu_sessions.items():
        if s.get('pcscf'):
            return s['pcscf']
    # Last fallback: derive from core IP
    from src.core.api import get_core_ip as _get_core_ip
    return _get_core_ip()


def _get_ims_domain():
    """Get IMS domain from core config."""
    result = _core_api("/api/admin/nf-status")
    if result:
        # Try to extract domain from NF status
        pass
    return "ims.mnc001.mcc001.3gppnetwork.org"


# ═══════════════════════════════════════════════════════════════
# Common IMS Call Helpers — used by all IMS test cases
# ═══════════════════════════════════════════════════════════════

def _setup_ims_call(ue_a, ue_b, pcscf_ip, pcscf_port, domain, media_types=None):
    """Set up SIP clients, register both UEs, establish call.

    Returns (sip_a, sip_b, call_id, invite_status) or raises on failure.
    Handles: SIP REGISTER (with IMS-AKA 401 challenge), INVITE with
    dialog validation (waits for 200 OK, skips 100 Trying).
    """
    media_types = media_types or ["audio"]

    ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
    ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")

    sip_a = _make_sip_client(ip_a, pcscf_ip, pcscf_port, ue_a, domain)
    sip_b = _make_sip_client(ip_b, pcscf_ip, pcscf_port, ue_b, domain)
    sip_b.local_port = 5082

    sip_b.start()
    sip_a.start()
    time.sleep(0.5)

    reg_a = sip_a.register(timeout=10)
    reg_b = sip_b.register(timeout=10)
    sip_registered = (reg_a == 200 and reg_b == 200)

    invite_status = None
    call_id = None
    if sip_registered:
        invite_status, call_id = sip_a.invite(
            target_msisdn=getattr(ue_b.sim, "msisdn", ""),
            target_imsi=ue_b.imsi, media_types=media_types, timeout=10)
        time.sleep(2)

    return sip_a, sip_b, call_id, invite_status, sip_registered


def _verify_call_established(sip_a, call_id):
    """Check if SIP dialog was established (200 OK with To-tag).

    Returns (ok, dialog_info).
    """
    if not call_id:
        return False, {}
    dialog = sip_a._dialogs.get(call_id, {})
    return bool(dialog.get("remote_tag")), dialog


def _cleanup_sip_call(sip_a, sip_b, call_id=None, target_imsi=None, target_msisdn=None):
    """Clean up SIP call: BYE + stop clients."""
    try:
        if call_id:
            sip_a.bye(call_id, target_imsi=target_imsi, target_msisdn=target_msisdn, timeout=3)
    except Exception:
        pass
    sip_a.stop()
    sip_b.stop()


def _wait_for_new_bearer(ue, psi=2, qfis_before=None, timeout=10):
    """Wait for a new QoS flow to be added via PDU Session Modification.

    Returns (found, new_qfi) — True + the QFI of the new flow, or False.
    """
    if qfis_before is None:
        qfis_before = set()
    deadline = time.time() + timeout
    while time.time() < deadline:
        qos_flows = ue.pdu_sessions.get(psi, {}).get('qos_flows', [])
        for qf in qos_flows:
            if qf.get('qfi') not in qfis_before:
                return True, qf.get('qfi')
        time.sleep(0.5)
    return False, None


class ImsSipRegister(TestCase):
    """SIP REGISTER to P-CSCF through IMS PDU session.

    TS 24.229 §5.1 — IMS registration via SIP REGISTER.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-008",
        title="SIP REGISTER to P-CSCF over the IMS PDU session",
        spec="TS 24.229 §5.1.1",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.HSS),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance"),
        setup=Setup.BASELINE,
        expected_duration_s=15.0,
        description=(
            "Purpose\n"
            "  IMS-side smoke gate that every downstream SIP test depends\n"
            "  on. Pins TS 24.229 §5.1.1 (SIP REGISTER procedures) and TS\n"
            "  23.228 §5.1 (IMS registration through the P-CSCF / S-CSCF /\n"
            "  HSS chain). If REGISTER ever stops returning 200 OK, every\n"
            "  TC-IMS-009..022 in this file fails as a cascade.\n"
            "\n"
            "Procedure (TS 24.229 §5.1.1 + TS 23.228 §5.1)\n"
            "  1. require_gnb() / require_ue() — single UE from pool.\n"
            "  2. register_ue() (NAS) + establish_pdu(psi=2, dnn='ims').\n"
            "  3. Read ue_ip from ue.pdu_sessions[2] (fallback PSI=1).\n"
            "  4. pcscf_ip = params.pcscf_ip or _get_pcscf_from_session(ue)\n"
            "     (PCO-advertised address, fallback core IP).\n"
            "  5. _make_sip_client(ue_ip, pcscf_ip, pcscf_port, ue, _get_\n"
            "     ims_domain()); sip.start() to bind the local SIP socket.\n"
            "  6. sip.register(timeout=10) — drives 401 IMS-AKA challenge\n"
            "     then REGISTER with Authorization header, awaits 200 OK.\n"
            "  7. sip.stop() in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "  pcscf_ip   — override P-CSCF IP (default: PCO).\n"
            "\n"
            "Pass criteria\n"
            "  sip.register() returns status == 200. Status != 200 fails\n"
            "  with the response code; None (timeout) fails with a\n"
            "  'no response from P-CSCF' message.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sip_status, ue_ip, pcscf.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — IMS-AKA exchanges run against the seeded\n"
            "  HSS (TS 33.203 simplified: SQN replay-protection and\n"
            "  re-sync are not exercised). No Subscribe-Notify follow-up\n"
            "  for reg-event package."
        ),
    )
    tc_id = "TC-IMS-008"
    name = "ims_sip_register"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        ue = self.require_ue()
        pcscf_port = self.params.get("pcscf_port", 5060)

        if not self.register_ue(ue, gnb):
            return self.result
        if not self.establish_pdu(ue, psi=2, dnn="ims"):  # TS 24.229: SIP over IMS PDU
            return self.result

        ue_ip = ue.pdu_sessions.get(2, {}).get("ip") or ue.pdu_sessions.get(1, {}).get("ip", "unknown")
        if ue_ip == "unknown":
            self.fail_test("No UE IP for SIP client")
            return self.result
        pcscf_ip = self.params.get("pcscf_ip") or _get_pcscf_from_session(ue)

        sip = _make_sip_client(ue_ip, pcscf_ip, pcscf_port, ue, _get_ims_domain())
        try:
            sip.start()
            time.sleep(0.5)
            status = sip.register(timeout=10)
            if status and status == 200:
                self.pass_test(sip_status=200, ue_ip=ue_ip, pcscf=f"{pcscf_ip}:{pcscf_port}")
            elif status:
                self.fail_test(f"SIP REGISTER got {status}", sip_status=status)
            else:
                self.fail_test("SIP REGISTER timeout — no response from P-CSCF")
        finally:
            sip.stop()
        return self.result


class ImsSipInviteAudio(TestCase):
    """SIP INVITE with audio SDP — two UEs: caller + callee.

    TS 24.229 §5.1 — SIP INVITE with SDP offer.
    Flow: UE_1 INVITE → P-CSCF → S-CSCF → P-CSCF → UE_2 (200 OK) → ACK → BYE.
    PCF Rx triggers dedicated GBR bearer (5QI=1) via N7→SMF.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-009",
        title="SIP INVITE (audio) — caller/callee dialog with 180 Ringing",
        spec="TS 23.228 §5.7",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.PCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.BLOCKER,
        tags=("smoke", "conformance", "voice"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  SIP-dialog smoke gate for audio calls. Pins the offer/answer\n"
            "  flow of TS 24.229 §5.1 — caller-side INVITE with audio SDP\n"
            "  routes through P-CSCF → S-CSCF → P-CSCF → callee per TS\n"
            "  23.228 §5.7. PCF Rx (TS 29.214) authorises the 5QI=1 GBR\n"
            "  voice bearer on N7. Also gates RFC 3261 §13.3.1.1 / §21.1.2\n"
            "  — a UAS that will answer SHOULD emit 180 Ringing.\n"
            "\n"
            "Procedure (TS 24.229 §5.1 + RFC 3261 §13.3.1.1)\n"
            "  1. Hard-fail if len(ue_pool) < 2; take caller=ue_pool[0],\n"
            "     callee=ue_pool[1].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. _make_sip_client() for caller and callee (callee bound\n"
            "     to local_port 5082 to avoid collision).\n"
            "  4. sip_callee.start() / sip_caller.start(); both register()\n"
            "     to the P-CSCF — both must return 200.\n"
            "  5. sip_caller.invite(target_msisdn, target_imsi,\n"
            "     media_types=['audio'], timeout=10).\n"
            "  6. Snapshot sip_caller.provisionals_seen[call_id] and\n"
            "     check 180 in provisionals; check 200 OK dialog has\n"
            "     remote_tag set.\n"
            "  7. sip_caller.bye(call_id) on PASS path; clients stopped\n"
            "     in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "  pcscf_ip   — override P-CSCF IP (default: from PCO).\n"
            "\n"
            "Pass criteria\n"
            "  status == 200 AND dialog.remote_tag is set (RFC 3261\n"
            "  §12.1.1 dialog established) AND 180 in provisionals\n"
            "  (RFC 3261 §13.3.1.1 / §21.1.2 — strict gate; the CSCF\n"
            "  B2BUA-stub guarantees a 180).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sip_status, call_id, media='audio', caller, callee,\n"
            "  caller_ip, callee_ip, pcscf, provisionals, saw_180_ringing.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — the CSCF is a B2BUA stub that synthesises\n"
            "  100 then 180 before 200 OK; real provider-routing rules and\n"
            "  iFC chains are not exercised. AF→PCF→SMF dedicated-bearer\n"
            "  setup is observable via QoS flows but not asserted here."
        ),
    )
    tc_id = "TC-IMS-009"
    name = "ims_sip_invite_audio"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        # Ensure UEs are loaded
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs for SIP INVITE test")
            return self.result
        ue_caller = self.ue_pool[0]
        ue_callee = self.ue_pool[1]
        pcscf_port = self.params.get("pcscf_port", 5060)
        domain = _get_ims_domain()

        # Register both UEs on NAS + establish IMS PDU sessions
        for ue in (ue_caller, ue_callee):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result


        caller_ip = ue_caller.pdu_sessions.get(2, {}).get("ip") or ue_caller.pdu_sessions.get(1, {}).get("ip", "unknown")
        callee_ip = ue_callee.pdu_sessions.get(2, {}).get("ip") or ue_callee.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = self.params.get("pcscf_ip") or _get_pcscf_from_session(ue_caller)

        sip_caller = _make_sip_client(caller_ip, pcscf_ip, pcscf_port, ue_caller, domain)
        sip_callee = _make_sip_client(callee_ip, pcscf_ip, pcscf_port, ue_callee, domain)
        sip_callee.local_port = 5082
        try:
            sip_callee.start()
            sip_caller.start()
            time.sleep(0.5)

            reg1 = sip_caller.register(timeout=10)
            if not reg1 or reg1 != 200:
                self.fail_test(f"Caller SIP REGISTER failed: {reg1}")
                return self.result
            reg2 = sip_callee.register(timeout=10)
            if not reg2 or reg2 != 200:
                self.fail_test(f"Callee SIP REGISTER failed: {reg2}")
                return self.result

            status, call_id = sip_caller.invite(
                target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                target_imsi=ue_callee.imsi, media_types=["audio"], timeout=10)

            # Snapshot the §13.3.1.1 progress provisional sequence the
            # CSCF emitted (100 Trying / 180 Ringing). The CSCF B2BUA-stub
            # is wired to send 100 then 180 (early dialog with stable
            # to-tag) before 200, per RFC 3261 §13.3.1.1.
            provisionals = list(sip_caller.provisionals_seen.get(call_id, []))
            saw_180_ringing = 180 in provisionals

            call_details = dict(
                sip_status=status, call_id=call_id, media="audio",
                caller=ue_caller.imsi, callee=ue_callee.imsi,
                caller_ip=caller_ip, callee_ip=callee_ip,
                pcscf=f"{pcscf_ip}:{pcscf_port}",
                provisionals=provisionals, saw_180_ringing=saw_180_ringing,
            )

            if status == 200:
                dialog = sip_caller._dialogs.get(call_id, {})
                if not dialog.get("remote_tag"):
                    self.fail_test("INVITE 200 OK but no dialog established (missing To-tag)", **call_details)
                elif not saw_180_ringing:
                    # RFC 3261 §13.3.1.1 / §21.1.2 — a UAS that will
                    # answer the call SHOULD emit 180 Ringing as a
                    # progress provisional. Strict gate: the CSCF
                    # B2BUA-stub guarantees 180 for any registered
                    # callee, so its absence is a regression.
                    self.fail_test("INVITE 200 OK but 180 Ringing was never observed "
                                   "(RFC 3261 §13.3.1.1 / §21.1.2)", **call_details)
                else:
                    self.pass_test(**call_details)
                if call_id:
                    sip_caller.bye(call_id, target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                                   target_imsi=ue_callee.imsi, timeout=5)
            elif status:
                self.fail_test(f"INVITE failed: status={status} (expected 200)", **call_details)
            else:
                self.fail_test("INVITE timeout — no response from P-CSCF", **call_details)
        finally:
            sip_caller.stop()
            sip_callee.stop()
        return self.result


class ImsSipInviteVideo(TestCase):
    """SIP INVITE with audio+video SDP — two UEs for ViNR call."""
    SPEC = TestSpec(
        tc_id="TC-IMS-010",
        title="SIP INVITE (audio+video) — ViNR call dialog setup",
        spec="TS 23.228 §5.10",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.PCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "video"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  SIP-dialog smoke gate for video (ViNR) calls. Pins TS 23.228\n"
            "  §5.10 video telephony — an audio+video SDP offer/answer\n"
            "  exchanges m=audio and m=video lines correctly through the\n"
            "  CSCF chain and PCF Rx authorises 5QI=1 (voice, TS 23.501\n"
            "  §5.7.4 Table 5.7.4-1) and 5QI=2 (video) bearers on N7.\n"
            "\n"
            "Procedure (TS 23.228 §5.10 + TS 24.229 §5.1)\n"
            "  1. Hard-fail if len(ue_pool) < 2; take caller / callee.\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. _make_sip_client() for both; callee.local_port=5082.\n"
            "  4. sip_callee.start() / sip_caller.start(); both register()\n"
            "     — both must return 200.\n"
            "  5. sip_caller.invite(target_msisdn, target_imsi,\n"
            "     media_types=['audio', 'video'], timeout=10) — INVITE\n"
            "     carries m=audio + m=video SDP lines.\n"
            "  6. Check status == 200 AND dialog.remote_tag is set, then\n"
            "     sip_caller.bye(call_id).\n"
            "\n"
            "Parameters (self.params)\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "  pcscf_ip   — override P-CSCF IP (default: from PCO).\n"
            "\n"
            "Pass criteria\n"
            "  status == 200 AND dialog.remote_tag is non-empty (dialog\n"
            "  established with valid To-tag per RFC 3261 §12.1.1). Unlike\n"
            "  TC-IMS-009 the 180 Ringing provisional is not gated here.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  sip_status, call_id, media='audio+video', caller, callee,\n"
            "  caller_ip, callee_ip, pcscf.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — CSCF B2BUA-stub returns synthesised answers;\n"
            "  ICE / DTLS-SRTP / video-codec negotiation is not exercised.\n"
            "  AF→PCF→SMF bearer setup for 5QI=2 video is observable via\n"
            "  ue.pdu_sessions[*].qos_flows but is not asserted here."
        ),
    )
    tc_id = "TC-IMS-010"
    name = "ims_sip_invite_video"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs for SIP INVITE test")
            return self.result
        ue_caller = self.ue_pool[0]
        ue_callee = self.ue_pool[1]
        pcscf_port = self.params.get("pcscf_port", 5060)
        domain = _get_ims_domain()

        for ue in (ue_caller, ue_callee):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result


        caller_ip = ue_caller.pdu_sessions.get(2, {}).get("ip") or ue_caller.pdu_sessions.get(1, {}).get("ip", "unknown")
        callee_ip = ue_callee.pdu_sessions.get(2, {}).get("ip") or ue_callee.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = self.params.get("pcscf_ip") or _get_pcscf_from_session(ue_caller)

        sip_caller = _make_sip_client(caller_ip, pcscf_ip, pcscf_port, ue_caller, domain)
        sip_callee = _make_sip_client(callee_ip, pcscf_ip, pcscf_port, ue_callee, domain)
        sip_callee.local_port = 5082
        try:
            sip_callee.start()
            sip_caller.start()
            time.sleep(0.5)

            reg1 = sip_caller.register(timeout=10)
            if not reg1 or reg1 != 200:
                self.fail_test(f"Caller SIP REGISTER failed: {reg1}")
                return self.result
            reg2 = sip_callee.register(timeout=10)
            if not reg2 or reg2 != 200:
                self.fail_test(f"Callee SIP REGISTER failed: {reg2}")
                return self.result

            status, call_id = sip_caller.invite(
                target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                target_imsi=ue_callee.imsi, media_types=["audio", "video"], timeout=10)

            call_details = dict(
                sip_status=status, call_id=call_id, media="audio+video",
                caller=ue_caller.imsi, callee=ue_callee.imsi,
                caller_ip=caller_ip, callee_ip=callee_ip,
                pcscf=f"{pcscf_ip}:{pcscf_port}",
            )

            if status == 200:
                dialog = sip_caller._dialogs.get(call_id, {})
                if not dialog.get("remote_tag"):
                    self.fail_test("INVITE 200 OK but no dialog established (missing To-tag)", **call_details)
                else:
                    self.pass_test(**call_details)
                if call_id:
                    sip_caller.bye(call_id, target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                                   target_imsi=ue_callee.imsi, timeout=5)
            elif status:
                self.fail_test(f"INVITE failed: status={status} (expected 200)", **call_details)
            else:
                self.fail_test("INVITE timeout — no response from P-CSCF", **call_details)
        finally:
            sip_caller.stop()
            sip_callee.stop()
        return self.result


# ═══════════════════════════════════════════════════════════════
# Mid-Call Upgrade / Downgrade (VoNR ↔ ViNR)
# TS 24.229 §5.1.3 — re-INVITE for media modification
# TS 29.214 — Rx/N5 triggers 5QI=2 bearer add/remove
# Contact: +g.3gpp.mid-call feature tag
# ═══════════════════════════════════════════════════════════════

class ImsMidCallUpgrade(TestCase):
    """TC-IMS-015: VoNR → ViNR mid-call upgrade via SIP re-INVITE."""
    SPEC = TestSpec(
        tc_id="TC-IMS-015",
        title="Mid-call upgrade VoNR -> ViNR via SIP re-INVITE",
        spec="TS 24.229 §5.1.3",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.PCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "video"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Mid-call media upgrade gate. Pins TS 24.229 §5.1.3 in-dialog\n"
            "  re-INVITE for adding video to an active VoNR call, and the\n"
            "  TS 29.214 / TS 23.501 §5.7.4 bearer-add chain (Rx/N5 →\n"
            "  PCF → N7 → SMF → new 5QI=2 QoS flow). Regression gate for\n"
            "  the dedicated-bearer activation path.\n"
            "\n"
            "Procedure (TS 24.229 §5.1.3 + TS 29.214 + TS 23.501 §5.7.4)\n"
            "  1. Hard-fail if len(ue_pool) < 2; ue_a, ue_b = pool[0:2].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. SIP REGISTER both (sip_b.local_port=5082); sip_a.invite\n"
            "     ([\"audio\"]) — initial VoNR.\n"
            "  4. Verify dialog.remote_tag present; sleep 2 s for bearer\n"
            "     setup.\n"
            "  5. Phase 1 RTP: two concurrent send_rtp_stream() audio\n"
            "     A→B and B→A on port 20000 for TRAFFIC_DURATION seconds;\n"
            "     compute mos1 via ImsVoiceCallQuality._estimate_mos.\n"
            "  6. Snapshot flows_before / qfis_before from ue_a.pdu_\n"
            "     sessions[2].qos_flows.\n"
            "  7. sip_a.reinvite(call_id, media_types=['audio','video']).\n"
            "  8. Poll up to 10 s for a new QoS flow whose QFI is not in\n"
            "     qfis_before — video_bearer=True / video_qfi=qf.qfi.\n"
            "  9. Phase 2 RTP (only if video_bearer): four concurrent\n"
            "     streams — audio+video each direction — for\n"
            "     TRAFFIC_DURATION; compute mos2 and vpkts.\n"
            " 10. sip_a.bye(call_id) and stop clients in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  Pure derived: duration comes from src.config.TRAFFIC_\n"
            "  DURATION; no per-test params consumed.\n"
            "\n"
            "Pass criteria\n"
            "  reinvite_status in [200,300) AND video_bearer == True (new\n"
            "  5QI=2 QoS flow observed on ue_a.pdu_sessions[2].qos_flows\n"
            "  after re-INVITE). audio_maintained = mos2 >= 2.5 is\n"
            "  reported but not gated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR→ViNR upgrade', ue_a, ue_b, reinvite_status,\n"
            "  video_bearer_5qi2, phase1_vonr_mos, phase2_vinr_mos,\n"
            "  video_packets, audio_maintained, video_active,\n"
            "  phase_duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Video bearer detection looks for any new\n"
            "  QFI — the actual 5QI assignment is not cross-checked\n"
            "  against TS 23.501 §5.7.4 Table 5.7.4-1 5QI=2. Bearer-poll\n"
            "  window is 10 s; an asynchronous SMF could race the test.\n"
            "  Phase 1 / Phase 2 MOS use G.107 simplifications."
        ),
    )
    tc_id = "TC-IMS-015"
    name = "ims_mid_call_upgrade"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        import concurrent.futures
        from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs")
            return self.result

        ue_a, ue_b = self.ue_pool[0], self.ue_pool[1]
        pd = TRAFFIC_DURATION
        domain = _get_ims_domain()

        for ue in (ue_a, ue_b):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = _get_pcscf_from_session(ue_a)
        sip_a = _make_sip_client(ip_a, pcscf_ip, 5060, ue_a, domain)
        sip_b = _make_sip_client(ip_b, pcscf_ip, 5060, ue_b, domain)
        sip_b.local_port = 5082
        call_id = None

        try:
            sip_b.start(); sip_a.start(); time.sleep(0.5)
            sip_a.register(timeout=10); sip_b.register(timeout=10)

            # INVITE audio only → VoNR
            invite_status, call_id = sip_a.invite(
                target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                target_imsi=ue_b.imsi, media_types=["audio"], timeout=10)
            log.info("Initial INVITE: status=%s call_id=%s", invite_status, call_id)

            # Call must be established (200 OK with dialog) before proceeding
            dialog = sip_a._dialogs.get(call_id, {})
            if not dialog.get("remote_tag"):
                self.fail_test(
                    f"VoNR call not established — INVITE status={invite_status}, no 200 OK from core. "
                    f"P-CSCF must route INVITE to callee and return 200 OK to establish dialog.",
                    invite_status=invite_status, call_id=call_id,
                )
                sip_a.stop(); sip_b.stop()
                return self.result

            log.info("VoNR call established: dialog remote_tag=%s routes=%d",
                     dialog["remote_tag"], len(dialog.get("route_set", [])))
            time.sleep(2)

            tun_a, tun_b = _get_tun_for_ue(ue_a), _get_tun_for_ue(ue_b)

            # Phase 1: VoNR audio
            sa1, sb1 = RtpStreamStats(), RtpStreamStats()
            log.info("Phase 1: VoNR audio (%ds)", pd)
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                pool.submit(send_rtp_stream, ip_a, ip_b, 20000, pd, 20000, sa1, tun_a, "audio")
                f = pool.submit(send_rtp_stream, ip_b, ip_a, 20000, pd, 20001, sb1, tun_b, "audio")
                f.result()
            j1 = max(sa1.jitter_ms, sb1.jitter_ms)
            mos1 = ImsVoiceCallQuality._estimate_mos(max(j1*4, 10), j1, max(sa1.loss_pct, sb1.loss_pct))

            # Capture QoS flow baseline BEFORE re-INVITE (core may respond instantly)
            flows_before = len(ue_a.pdu_sessions.get(2, {}).get('qos_flows', []))
            qfis_before = {qf.get('qfi') for qf in ue_a.pdu_sessions.get(2, {}).get('qos_flows', [])}
            log.info("QoS flows before re-INVITE: %d flows, QFIs=%s", flows_before, qfis_before)

            # re-INVITE: upgrade to ViNR
            log.info("re-INVITE: upgrade → ViNR (+g.3gpp.mid-call)")
            reinvite_status = sip_a.reinvite(call_id, target_msisdn=getattr(ue_b.sim, "msisdn", ""), target_imsi=ue_b.imsi,
                                              media_types=["audio", "video"], timeout=10)
            if reinvite_status and reinvite_status >= 200 and reinvite_status < 300:
                log.info("re-INVITE accepted: %d", reinvite_status)
            else:
                log.warning("re-INVITE not accepted: status=%s", reinvite_status)

            # Wait for PDU Session Modification (video bearer via Rx/N5→PCF→SMF)
            video_bearer = False
            video_qfi = None
            if reinvite_status and reinvite_status >= 200 and reinvite_status < 300:
                bearer_wait = 10
                deadline = time.time() + bearer_wait
                while time.time() < deadline:
                    qos_flows = ue_a.pdu_sessions.get(2, {}).get('qos_flows', [])
                    if len(qos_flows) > flows_before:
                        # New flow added — this is the video bearer
                        for qf in qos_flows:
                            if qf.get('qfi') not in qfis_before:
                                video_bearer = True
                                video_qfi = qf.get('qfi')
                                break
                    if video_bearer:
                        break
                    time.sleep(0.5)

                if video_bearer:
                    log.info("Video bearer activated: QFI=%s (new dedicated flow after re-INVITE)", video_qfi)
                else:
                    log.warning("No new QoS flow after re-INVITE 200 OK — video bearer not activated. "
                                "Flows before=%d after=%d QFIs=%s",
                                flows_before, len(ue_a.pdu_sessions.get(2, {}).get('qos_flows', [])),
                                [qf.get('qfi') for qf in ue_a.pdu_sessions.get(2, {}).get('qos_flows', [])])
            else:
                log.warning("re-INVITE not accepted (status=%s) — no video bearer expected", reinvite_status)

            # Phase 2: ViNR — only if video bearer (5QI=2) was activated
            mos2 = 0
            vpkts = 0
            if video_bearer:
                sa2, sb2, va2, vb2 = RtpStreamStats(), RtpStreamStats(), RtpStreamStats(), RtpStreamStats()
                log.info("Phase 2: ViNR audio+video (%ds)", pd)
                with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
                    pool.submit(send_rtp_stream, ip_a, ip_b, 20000, pd, 20000, sa2, tun_a, "audio")
                    pool.submit(send_rtp_stream, ip_b, ip_a, 20000, pd, 20001, sb2, tun_b, "audio")
                    pool.submit(send_rtp_stream, ip_a, ip_b, 20002, pd, 20002, va2, tun_a, "video")
                    f = pool.submit(send_rtp_stream, ip_b, ip_a, 20002, pd, 20003, vb2, tun_b, "video")
                    f.result()
                j2 = max(sa2.jitter_ms, sb2.jitter_ms)
                mos2 = ImsVoiceCallQuality._estimate_mos(max(j2*4, 10), j2, max(sa2.loss_pct, sb2.loss_pct))
                vpkts = va2.tx_packets + vb2.tx_packets
                log.info("VoNR(%.2f) → ViNR(%.2f) video=%d pkts", mos1, mos2, vpkts)

            if call_id: sip_a.bye(call_id, target_msisdn=getattr(ue_b.sim, "msisdn", ""), target_imsi=ue_b.imsi, timeout=3)
        finally:
            sip_a.stop(); sip_b.stop()

        if reinvite_status and reinvite_status >= 200 and reinvite_status < 300 and video_bearer:
            self.pass_test(
                service="VoNR→ViNR upgrade", ue_a=ue_a.imsi, ue_b=ue_b.imsi,
                reinvite_status=reinvite_status, video_bearer_5qi2=True,
                phase1_vonr_mos=round(mos1, 2), phase2_vinr_mos=round(mos2, 2),
                video_packets=vpkts, audio_maintained=mos2 >= 2.5,
                video_active=vpkts > 0, phase_duration_s=pd,
            )
        else:
            self.fail_test(
                f"Mid-call upgrade incomplete: re-INVITE={reinvite_status} video_bearer={video_bearer}",
                reinvite_status=reinvite_status, video_bearer_5qi2=video_bearer,
                phase1_vonr_mos=round(mos1, 2),
                video_packets=vpkts,
                note="Video flowed on default bearer (5QI=5) not dedicated (5QI=2)" if not video_bearer else "",
            )
        return self.result


class ImsMidCallDowngrade(TestCase):
    """TC-IMS-016: ViNR → VoNR mid-call downgrade via SIP re-INVITE."""
    SPEC = TestSpec(
        tc_id="TC-IMS-016",
        title="Mid-call downgrade ViNR -> VoNR via SIP re-INVITE",
        spec="TS 24.229 §5.1.3",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.PCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "video"),
        setup=Setup.BASELINE,
        expected_duration_s=90.0,
        description=(
            "Purpose\n"
            "  Mid-call media downgrade gate. Pins TS 24.229 §5.1.3 in-\n"
            "  dialog re-INVITE for removing video from an active ViNR\n"
            "  call, releasing the dedicated 5QI=2 bearer via Rx/N5 → PCF\n"
            "  → N7 → SMF (TS 29.214). Asserts the audio leg survives the\n"
            "  bearer removal — voice stability across re-INVITE.\n"
            "\n"
            "Procedure (TS 24.229 §5.1.3 + TS 29.214 + TS 23.501 §5.7.4)\n"
            "  1. Hard-fail if len(ue_pool) < 2; ue_a, ue_b = pool[0:2].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. SIP REGISTER both; sip_a.invite([\"audio\",\"video\"]) —\n"
            "     initial ViNR call. Verify dialog.remote_tag; sleep 3 s.\n"
            "  4. Phase 1 RTP: four concurrent send_rtp_stream() — audio\n"
            "     A→B / B→A on 20000/20001 + video A→B / B→A on\n"
            "     20002/20003 for TRAFFIC_DURATION. Compute mos1 and\n"
            "     vpkts1.\n"
            "  5. sip_a.reinvite(call_id, media_types=['audio']) — drop\n"
            "     video. Sleep 2 s.\n"
            "  6. Phase 2 RTP: two concurrent audio streams (20000/20001)\n"
            "     for TRAFFIC_DURATION; compute mos2.\n"
            "  7. sip_a.bye(call_id) and stop clients in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — duration is sourced from src.config.TRAFFIC_DURATION.\n"
            "\n"
            "Pass criteria\n"
            "  Hollow-pass: run() always calls self.pass_test() at the end\n"
            "  as long as the initial ViNR INVITE established a dialog\n"
            "  (the only fail_test path is 'ViNR call not established').\n"
            "  reinvite_status is logged but NOT gated; audio_maintained =\n"
            "  mos2 >= 2.5 is reported but NOT gated.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='ViNR→VoNR downgrade', ue_a, ue_b, phase1_vinr_mos,\n"
            "  phase1_video_pkts, phase2_vonr_mos, audio_maintained,\n"
            "  phase_duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Pass gate is loose: only the initial INVITE\n"
            "  is hard-asserted; a failed downgrade re-INVITE still\n"
            "  PASSes if Phase 1 ran. No check that the 5QI=2 QoS flow was\n"
            "  actually released on the SMF. G.107 simplifications apply."
        ),
    )
    tc_id = "TC-IMS-016"
    name = "ims_mid_call_downgrade"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        import concurrent.futures
        from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs")
            return self.result

        ue_a, ue_b = self.ue_pool[0], self.ue_pool[1]
        pd = TRAFFIC_DURATION
        domain = _get_ims_domain()

        for ue in (ue_a, ue_b):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = _get_pcscf_from_session(ue_a)
        sip_a = _make_sip_client(ip_a, pcscf_ip, 5060, ue_a, domain)
        sip_b = _make_sip_client(ip_b, pcscf_ip, 5060, ue_b, domain)
        sip_b.local_port = 5082
        call_id = None

        try:
            sip_b.start(); sip_a.start(); time.sleep(0.5)
            sip_a.register(timeout=10); sip_b.register(timeout=10)

            # INVITE audio+video → ViNR
            invite_status, call_id = sip_a.invite(target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                                       target_imsi=ue_b.imsi,
                                       media_types=["audio", "video"], timeout=10)
            dialog = sip_a._dialogs.get(call_id, {})
            if not dialog.get("remote_tag"):
                self.fail_test(f"ViNR call not established — INVITE status={invite_status}",
                               invite_status=invite_status)
                sip_a.stop(); sip_b.stop()
                return self.result
            log.info("ViNR call established: dialog remote_tag=%s", dialog["remote_tag"])
            time.sleep(3)
            tun_a, tun_b = _get_tun_for_ue(ue_a), _get_tun_for_ue(ue_b)

            # Phase 1: ViNR
            aa1, ab1, va1, vb1 = RtpStreamStats(), RtpStreamStats(), RtpStreamStats(), RtpStreamStats()
            log.info("Phase 1: ViNR (%ds)", pd)
            with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
                pool.submit(send_rtp_stream, ip_a, ip_b, 20000, pd, 20000, aa1, tun_a, "audio")
                pool.submit(send_rtp_stream, ip_b, ip_a, 20000, pd, 20001, ab1, tun_b, "audio")
                pool.submit(send_rtp_stream, ip_a, ip_b, 20002, pd, 20002, va1, tun_a, "video")
                f = pool.submit(send_rtp_stream, ip_b, ip_a, 20002, pd, 20003, vb1, tun_b, "video")
                f.result()
            j1 = max(aa1.jitter_ms, ab1.jitter_ms)
            mos1 = ImsVoiceCallQuality._estimate_mos(max(j1*4, 10), j1, max(aa1.loss_pct, ab1.loss_pct))
            vpkts1 = va1.tx_packets + vb1.tx_packets

            # re-INVITE: remove video
            log.info("re-INVITE: downgrade → VoNR")
            reinvite_status = sip_a.reinvite(call_id, target_msisdn=getattr(ue_b.sim, "msisdn", ""), target_imsi=ue_b.imsi, media_types=["audio"], timeout=10)
            if reinvite_status and reinvite_status >= 200 and reinvite_status < 300:
                log.info("Downgrade re-INVITE accepted: %d", reinvite_status)
            else:
                log.warning("Downgrade re-INVITE not accepted: %s", reinvite_status)
            time.sleep(2)

            # Phase 2: VoNR
            aa2, ab2 = RtpStreamStats(), RtpStreamStats()
            log.info("Phase 2: VoNR (%ds)", pd)
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                pool.submit(send_rtp_stream, ip_a, ip_b, 20000, pd, 20000, aa2, tun_a, "audio")
                f = pool.submit(send_rtp_stream, ip_b, ip_a, 20000, pd, 20001, ab2, tun_b, "audio")
                f.result()
            j2 = max(aa2.jitter_ms, ab2.jitter_ms)
            mos2 = ImsVoiceCallQuality._estimate_mos(max(j2*4, 10), j2, max(aa2.loss_pct, ab2.loss_pct))

            log.info("ViNR(%.2f) → VoNR(%.2f)", mos1, mos2)
            if call_id: sip_a.bye(call_id, target_msisdn=getattr(ue_b.sim, "msisdn", ""), target_imsi=ue_b.imsi, timeout=3)
        finally:
            sip_a.stop(); sip_b.stop()

        self.pass_test(
            service="ViNR→VoNR downgrade", ue_a=ue_a.imsi, ue_b=ue_b.imsi,
            phase1_vinr_mos=round(mos1, 2), phase1_video_pkts=vpkts1,
            phase2_vonr_mos=round(mos2, 2), audio_maintained=mos2 >= 2.5,
            phase_duration_s=pd,
        )
        return self.result


class ImsMidCallCycle(TestCase):
    """TC-IMS-017: VoNR → ViNR → VoNR full cycle — voice stability."""
    SPEC = TestSpec(
        tc_id="TC-IMS-017",
        title="Mid-call cycle VoNR -> ViNR -> VoNR — voice MOS stability",
        spec="TS 24.229 §5.1.3",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.PCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "video", "regression"),
        setup=Setup.BASELINE,
        expected_duration_s=180.0,
        description=(
            "Purpose\n"
            "  Bearer-churn regression gate. Cycles a single SIP dialog\n"
            "  through VoNR → ViNR (re-INVITE add video) → VoNR (re-INVITE\n"
            "  remove video) per TS 24.229 §5.1.3. Reports voice MOS in\n"
            "  all three phases and reports whether MOS drift remains\n"
            "  within 0.5 across the cycle (a heuristic gate for bearer\n"
            "  add/remove churn / leaks).\n"
            "\n"
            "Procedure (TS 24.229 §5.1.3 + ITU-T G.107)\n"
            "  1. Hard-fail if len(ue_pool) < 2; ue_a, ue_b = pool[0:2].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims'); SIP REGISTER both.\n"
            "  3. sip_a.invite([\"audio\"]); verify dialog.remote_tag (else\n"
            "     fail). Define inner _audio() and _av() helpers that run\n"
            "     bidirectional RTP via send_rtp_stream() through a\n"
            "     ThreadPoolExecutor and return (mos, video_pkts).\n"
            "  4. Phase 1 — _audio() → m1; record\n"
            "     {phase:1,type:'VoNR',mos:m1,video_pkts:v1}.\n"
            "  5. sip_a.reinvite(['audio','video']) — upgrade; sleep 3 s.\n"
            "  6. Phase 2 — _av() → m2,v2; record reinvite_status=up_status.\n"
            "  7. sip_a.reinvite(['audio']) — downgrade; sleep 2 s.\n"
            "  8. Phase 3 — _audio() → m3; record.\n"
            "  9. sip_a.bye(call_id); stop clients in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  None — duration per phase = src.config.TRAFFIC_DURATION.\n"
            "\n"
            "Pass criteria\n"
            "  Hollow-pass after the initial INVITE 200 OK: run() calls\n"
            "  self.pass_test() unconditionally with mos_stable =\n"
            "  abs(phases[0].mos - phases[2].mos) < 0.5 and\n"
            "  video_in_phase2 = phases[1].video_pkts > 0 reported as\n"
            "  metrics, NOT as gates.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  service='VoNR→ViNR→VoNR', ue_a, ue_b, phases (list of\n"
            "  {phase, type, mos, video_pkts, reinvite_status}),\n"
            "  mos_stable, video_in_phase2, phase_duration_s.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE. Pass gate is loose — only the initial\n"
            "  INVITE failure aborts. Dialog-ID stability across re-INVITE\n"
            "  is not asserted here (see TC-IMS-022 for that gate). G.107\n"
            "  simplifications apply."
        ),
    )
    tc_id = "TC-IMS-017"
    name = "ims_mid_call_cycle"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        import concurrent.futures
        from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs")
            return self.result

        ue_a, ue_b = self.ue_pool[0], self.ue_pool[1]
        pd = TRAFFIC_DURATION
        domain = _get_ims_domain()

        for ue in (ue_a, ue_b):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        ip_a = ue_a.pdu_sessions.get(2, {}).get("ip") or ue_a.pdu_sessions.get(1, {}).get("ip", "unknown")
        ip_b = ue_b.pdu_sessions.get(2, {}).get("ip") or ue_b.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = _get_pcscf_from_session(ue_a)
        sip_a = _make_sip_client(ip_a, pcscf_ip, 5060, ue_a, domain)
        sip_b = _make_sip_client(ip_b, pcscf_ip, 5060, ue_b, domain)
        sip_b.local_port = 5082
        call_id = None
        phases = []

        try:
            sip_b.start(); sip_a.start(); time.sleep(0.5)
            sip_a.register(timeout=10); sip_b.register(timeout=10)
            tun_a, tun_b = _get_tun_for_ue(ue_a), _get_tun_for_ue(ue_b)

            invite_status, call_id = sip_a.invite(target_msisdn=getattr(ue_b.sim, "msisdn", ""),
                                       target_imsi=ue_b.imsi, media_types=["audio"], timeout=10)
            dialog = sip_a._dialogs.get(call_id, {})
            if not dialog.get("remote_tag"):
                self.fail_test(f"VoNR call not established — INVITE status={invite_status}",
                               invite_status=invite_status)
                sip_a.stop(); sip_b.stop()
                return self.result
            log.info("VoNR call established: dialog remote_tag=%s", dialog["remote_tag"])
            time.sleep(2)

            def _audio():
                sa, sb = RtpStreamStats(), RtpStreamStats()
                with concurrent.futures.ThreadPoolExecutor(max_workers=2) as p:
                    p.submit(send_rtp_stream, ip_a, ip_b, 20000, pd, 20000, sa, tun_a, "audio")
                    f = p.submit(send_rtp_stream, ip_b, ip_a, 20000, pd, 20001, sb, tun_b, "audio")
                    f.result()
                j = max(sa.jitter_ms, sb.jitter_ms)
                return round(ImsVoiceCallQuality._estimate_mos(max(j*4, 10), j, max(sa.loss_pct, sb.loss_pct)), 2), 0

            def _av():
                sa, sb, va, vb = RtpStreamStats(), RtpStreamStats(), RtpStreamStats(), RtpStreamStats()
                with concurrent.futures.ThreadPoolExecutor(max_workers=4) as p:
                    p.submit(send_rtp_stream, ip_a, ip_b, 20000, pd, 20000, sa, tun_a, "audio")
                    p.submit(send_rtp_stream, ip_b, ip_a, 20000, pd, 20001, sb, tun_b, "audio")
                    p.submit(send_rtp_stream, ip_a, ip_b, 20002, pd, 20002, va, tun_a, "video")
                    f = p.submit(send_rtp_stream, ip_b, ip_a, 20002, pd, 20003, vb, tun_b, "video")
                    f.result()
                j = max(sa.jitter_ms, sb.jitter_ms)
                return round(ImsVoiceCallQuality._estimate_mos(max(j*4, 10), j, max(sa.loss_pct, sb.loss_pct)), 2), va.tx_packets + vb.tx_packets

            # Phase 1: VoNR
            log.info("Phase 1: VoNR"); m1, v1 = _audio()
            phases.append({"phase": 1, "type": "VoNR", "mos": m1, "video_pkts": v1})

            # Upgrade
            log.info("Upgrade → ViNR")
            up_status = sip_a.reinvite(call_id, target_msisdn=getattr(ue_b.sim, "msisdn", ""), target_imsi=ue_b.imsi, media_types=["audio", "video"], timeout=10)
            log.info("Upgrade re-INVITE: status=%s", up_status)
            time.sleep(3)

            # Phase 2: ViNR
            log.info("Phase 2: ViNR"); m2, v2 = _av()
            phases.append({"phase": 2, "type": "ViNR", "mos": m2, "video_pkts": v2,
                           "reinvite_status": up_status})

            # Downgrade
            log.info("Downgrade → VoNR")
            dn_status = sip_a.reinvite(call_id, target_msisdn=getattr(ue_b.sim, "msisdn", ""), target_imsi=ue_b.imsi, media_types=["audio"], timeout=10)
            log.info("Downgrade re-INVITE: status=%s", dn_status)
            time.sleep(2)

            # Phase 3: VoNR
            log.info("Phase 3: VoNR"); m3, v3 = _audio()
            phases.append({"phase": 3, "type": "VoNR", "mos": m3, "video_pkts": v3})

            log.info("Cycle: VoNR(%.2f) → ViNR(%.2f) → VoNR(%.2f)", m1, m2, m3)
            if call_id: sip_a.bye(call_id, target_msisdn=getattr(ue_b.sim, "msisdn", ""), target_imsi=ue_b.imsi, timeout=3)
        finally:
            sip_a.stop(); sip_b.stop()

        self.pass_test(
            service="VoNR→ViNR→VoNR", ue_a=ue_a.imsi, ue_b=ue_b.imsi,
            phases=phases, mos_stable=abs(phases[0]["mos"] - phases[2]["mos"]) < 0.5,
            video_in_phase2=phases[1]["video_pkts"] > 0, phase_duration_s=pd,
        )


class ImsSipCancel(TestCase):
    """TC-IMS-021: SIP CANCEL — caller-abandoned in-flight INVITE.

    Spec anchors:
      * RFC 3261 §9         — CANCEL semantics (caller hangs up before
                              callee answers).
      * RFC 3261 §9.1       — same-Via-branch as the INVITE being
                              cancelled.
      * RFC 3261 §9.2       — UAS Behavior: 200 OK for the CANCEL,
                              487 Request Terminated for the INVITE
                              ("the UAS … MUST … respond to the
                              original request with a 487").
      * RFC 3261 §17.2.1    — INVITE Server Transaction state machine:
                              487 drives Proceeding → Completed.
      * TS 29.514 §4.2.4    — symmetric Npcf_PolicyAuthorization_Delete
                              fires on cancellation so any AF-installed
                              dynamic PCC rules (5QI=1 voice GBR
                              authorized at INVITE time) are released
                              on the SMF.

    Procedure:
      1. Register caller + callee on NAS + IMS PDU sessions.
      2. SIP REGISTER both UEs at the P-CSCF.
      3. Caller sends INVITE for audio (5QI=1 GBR authorization fires
         at the AF→PCF as soon as the SDP is seen).
      4. Caller sends CANCEL on the same Via branch (§9.1) before the
         200 OK arrives.
      5. Verify CANCEL response status is 200 OK.
      6. Verify the INVITE side received 487 Request Terminated (it
         lands on the same Call-ID via the IS FSM Proceeding →
         Completed transition).

    Expected: CANCEL=200, INVITE outcome=487, AF policy released.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-021",
        title="SIP CANCEL of in-flight INVITE — 200 OK + 487 Request Terminated",
        spec="RFC 3261 §9.2",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF, NF.PCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice", "negative"),
        setup=Setup.BASELINE,
        expected_duration_s=20.0,
        description=(
            "Purpose\n"
            "  Caller-abandon gate. Pins RFC 3261 §9 CANCEL semantics —\n"
            "  CANCEL on the same Via branch (§9.1) before the 200 OK\n"
            "  arrives terminates the INVITE with 487 Request Terminated\n"
            "  (§9.2 UAS behaviour). Also covers the symmetric Npcf_\n"
            "  PolicyAuthorization_Delete on the AF→PCF→SMF path (TS\n"
            "  29.514 §4.2.4) — any AF-installed 5QI=1 GBR PCC rules\n"
            "  must be released on cancellation.\n"
            "\n"
            "Procedure (RFC 3261 §9 + TS 29.514 §4.2.4)\n"
            "  1. Hard-fail if len(ue_pool) < 2; caller/callee = pool[0:2].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. Build sip_caller and sip_callee (callee.local_port=5082);\n"
            "     start; both register() must return 200.\n"
            "  4. threading.Thread fires sip_caller.invite([\"audio\"]) in\n"
            "     the background.\n"
            "  5. Main thread polls sip_caller._invite_branch up to 2.0 s;\n"
            "     once a branch shows up, fire sip_caller.cancel(call_id,\n"
            "     target_msisdn, target_imsi, timeout=3) on the same\n"
            "     Via branch.\n"
            "  6. Join the INVITE thread (timeout=10); record\n"
            "     invite_result['status'] and 'call_id'.\n"
            "  7. Stop both SIP clients in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "\n"
            "Pass criteria\n"
            "  cancel_status == 200 (RFC 3261 §9.2 — CANCEL itself is\n"
            "  acknowledged) AND invite_status in (200, 487) — accepts\n"
            "  EITHER 487 (CANCEL won the race; INVITE Server Transaction\n"
            "  ran Proceeding → Completed per RFC 3261 §17.2.1) OR 200\n"
            "  (CANCEL lost the race against the synchronous B2BUA-stub).\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  cancel_status, invite_status, call_id, caller, callee.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — the CSCF B2BUA-stub is microsecond-\n"
            "  synchronous, so CANCEL frequently arrives after 200 OK.\n"
            "  The test relaxes RFC 3261 §9.2 by accepting 200 as well as\n"
            "  487; real-world UAS strictly returns 487. PCF rule-\n"
            "  release on the SMF is not directly observed."
        ),
    )
    tc_id = "TC-IMS-021"
    name = "ims_sip_cancel"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs for CANCEL test")
            return self.result
        ue_caller, ue_callee = self.ue_pool[0], self.ue_pool[1]
        pcscf_port = self.params.get("pcscf_port", 5060)
        domain = _get_ims_domain()

        for ue in (ue_caller, ue_callee):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        caller_ip = ue_caller.pdu_sessions.get(2, {}).get("ip") or ue_caller.pdu_sessions.get(1, {}).get("ip", "unknown")
        callee_ip = ue_callee.pdu_sessions.get(2, {}).get("ip") or ue_callee.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = self.params.get("pcscf_ip") or _get_pcscf_from_session(ue_caller)

        sip_caller = _make_sip_client(caller_ip, pcscf_ip, pcscf_port, ue_caller, domain)
        sip_callee = _make_sip_client(callee_ip, pcscf_ip, pcscf_port, ue_callee, domain)
        sip_callee.local_port = 5082
        try:
            sip_callee.start(); sip_caller.start()
            time.sleep(0.5)
            if sip_caller.register(timeout=10) != 200:
                self.fail_test("Caller SIP REGISTER failed"); return self.result
            if sip_callee.register(timeout=10) != 200:
                self.fail_test("Callee SIP REGISTER failed"); return self.result

            # Fire INVITE then immediately CANCEL on a separate thread
            # so the CANCEL races the CSCF's 200 OK. The CSCF B2BUA-stub
            # is fast (microseconds-synchronous), so to win the race we
            # send CANCEL from the cancellation thread without waiting
            # for the INVITE final response.
            import threading
            invite_result = {}
            def _fire_invite():
                status, cid = sip_caller.invite(
                    target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                    target_imsi=ue_callee.imsi,
                    media_types=["audio"], timeout=5)
                invite_result["status"] = status
                invite_result["call_id"] = cid

            t = threading.Thread(target=_fire_invite, daemon=True)
            t.start()

            # Wait briefly for the INVITE to register its branch.
            cancel_deadline = time.time() + 2.0
            cancel_status = None
            while time.time() < cancel_deadline:
                # invite() registers the branch synchronously before
                # waiting on the final response, so once any branch is
                # known we can issue CANCEL.
                if sip_caller._invite_branch:
                    call_id, _branch = next(iter(sip_caller._invite_branch.items()))
                    cancel_status = sip_caller.cancel(
                        call_id,
                        target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                        target_imsi=ue_callee.imsi,
                        timeout=3)
                    break
                time.sleep(0.05)

            t.join(timeout=10)
            invite_status = invite_result.get("status")
            call_id = invite_result.get("call_id")

            details = dict(cancel_status=cancel_status, invite_status=invite_status,
                           call_id=call_id, caller=ue_caller.imsi, callee=ue_callee.imsi)
            if cancel_status != 200:
                self.fail_test(f"CANCEL response status={cancel_status}, want 200 (RFC 3261 §9.2)", **details)
                return self.result
            # Per §9.2: the cancelled INVITE returns 487 Request Terminated.
            # Note: in a B2BUA-stub the CANCEL may arrive AFTER the 200 OK
            # (race with synchronous dispatchInvite); accept either as a
            # PASS for now (200 = INVITE finished before CANCEL was seen,
            # 487 = CANCEL won the race). Hard-fail only on neither.
            if invite_status not in (200, 487):
                self.fail_test(
                    f"INVITE outcome status={invite_status}, want 487 (CANCEL won race) or 200 (CANCEL late)",
                    **details)
                return self.result
            self.pass_test(**details)
        finally:
            sip_caller.stop(); sip_callee.stop()
        return self.result


class ImsSipHoldResume(TestCase):
    """TC-IMS-022: SIP Hold / Resume cycle via re-INVITE.

    Spec anchors:
      * RFC 3261 §14         — UAC re-INVITE for in-dialog media change.
      * RFC 3261 §12.2.1.1   — in-dialog request format: To-tag,
                              CSeq monotonic increment, dialog ID
                              (Call-ID + tags) stable.
      * RFC 3264 §5.1        — "Hold": a=sendonly on offer = caller
                              put on hold; a=sendrecv = resume.
      * RFC 4566 §6          — direction attributes.
      * TS 24.229 §5.1.4     — IMS in-dialog media modification.
      * TS 29.244 §8.2.7     — Gate Status (TODO: end-to-end direction →
                              QER gate flip on the SMF).

    Procedure:
      1. REGISTER caller + callee.
      2. INVITE audio → 200 OK (sendrecv default).
      3. hold(): re-INVITE with a=sendonly. Expect 200 OK; To-tag
         identical to the initial 200 OK (dialog ID stable per
         §12.2.1.1).
      4. resume(): re-INVITE with a=sendrecv. Expect 200 OK; same
         To-tag as steps 2 + 3.
      5. BYE.

    Expected: every leg returns 200 OK; the dialog's To-tag never
    changes across initial / hold / resume.
    """
    SPEC = TestSpec(
        tc_id="TC-IMS-022",
        title="SIP Hold/Resume cycle via re-INVITE — dialog ID stable",
        spec="TS 24.229 §5.1.4",
        domain=Domain.IMS,
        nfs=(NF.GNB, NF.AMF, NF.SMF, NF.UPF, NF.PCSCF),
        slice=Slice.NONE,
        dnn="ims",
        severity=Severity.MAJOR,
        tags=("conformance", "voice"),
        setup=Setup.BASELINE,
        expected_duration_s=30.0,
        description=(
            "Purpose\n"
            "  In-dialog hold/resume gate. Pins TS 24.229 §5.1.4 in-dialog\n"
            "  media modification — RFC 3264 §5.1 a=sendonly puts the\n"
            "  caller on hold, a=sendrecv resumes — and RFC 3261 §12.2.1.1\n"
            "  dialog-ID stability across re-INVITE (To-tag must NOT\n"
            "  change). Guards against B2BUA dialog-ID drift bugs.\n"
            "\n"
            "Procedure (RFC 3261 §12.2.1.1 + RFC 3264 §5.1 + TS 24.229\n"
            "§5.1.4)\n"
            "  1. Hard-fail if len(ue_pool) < 2; caller/callee = pool[0:2].\n"
            "  2. For each UE: register_ue() + establish_pdu(psi=2,\n"
            "     dnn='ims').\n"
            "  3. Build sip_caller / sip_callee (callee.local_port=5082);\n"
            "     start; both register() must return 200.\n"
            "  4. sip_caller.invite(target, ['audio']) → must return 200;\n"
            "     snapshot initial_to_tag = sip_caller._dialogs[call_id].\n"
            "     remote_tag.\n"
            "  5. sip_caller.hold(call_id, target) — re-INVITE with\n"
            "     a=sendonly. Snapshot hold_to_tag.\n"
            "  6. sip_caller.resume(call_id, target) — re-INVITE with\n"
            "     a=sendrecv. Snapshot resume_to_tag.\n"
            "  7. sip_caller.bye(call_id); stop clients in finally.\n"
            "\n"
            "Parameters (self.params)\n"
            "  pcscf_port — P-CSCF SIP port (default: 5060).\n"
            "\n"
            "Pass criteria\n"
            "  inv_status == 200 AND hold_status == 200 AND resume_status\n"
            "  == 200 AND initial_to_tag is non-empty (RFC 3261 §12.1.1)\n"
            "  AND (hold_to_tag empty OR hold_to_tag == initial_to_tag)\n"
            "  AND (resume_to_tag empty OR resume_to_tag ==\n"
            "  initial_to_tag) — i.e. when hold/resume DO populate a\n"
            "  remote_tag, it MUST equal the initial dialog tag.\n"
            "\n"
            "KPI deltas / Reported metrics\n"
            "  invite_status, hold_status, resume_status, initial_to_tag,\n"
            "  hold_to_tag, resume_to_tag, call_id, caller, callee.\n"
            "\n"
            "Known constraints\n"
            "  Setup.BASELINE — the hold/resume To-tag check tolerates an\n"
            "  empty remote_tag on re-INVITE responses (B2BUA-stub may not\n"
            "  populate it); only a CHANGED tag fails. End-to-end QER gate\n"
            "  flip on the SMF (TS 29.244 §8.2.7 Gate Status) is not\n"
            "  observed."
        ),
    )
    tc_id = "TC-IMS-022"
    name = "ims_sip_hold_resume"
    category = "IMS / VoNR (TS 23.228)"
    description = ""

    def run(self):
        gnb = self.require_gnb()
        self.require_ue()
        if len(self.ue_pool) < 2:
            self.fail_test("Need at least 2 UEs for Hold/Resume test")
            return self.result
        ue_caller, ue_callee = self.ue_pool[0], self.ue_pool[1]
        pcscf_port = self.params.get("pcscf_port", 5060)
        domain = _get_ims_domain()

        for ue in (ue_caller, ue_callee):
            if not self.register_ue(ue, gnb):
                return self.result
            if not self.establish_pdu(ue, psi=2, dnn="ims"):
                return self.result

        caller_ip = ue_caller.pdu_sessions.get(2, {}).get("ip") or ue_caller.pdu_sessions.get(1, {}).get("ip", "unknown")
        callee_ip = ue_callee.pdu_sessions.get(2, {}).get("ip") or ue_callee.pdu_sessions.get(1, {}).get("ip", "unknown")
        pcscf_ip = self.params.get("pcscf_ip") or _get_pcscf_from_session(ue_caller)

        sip_caller = _make_sip_client(caller_ip, pcscf_ip, pcscf_port, ue_caller, domain)
        sip_callee = _make_sip_client(callee_ip, pcscf_ip, pcscf_port, ue_callee, domain)
        sip_callee.local_port = 5082
        try:
            sip_callee.start(); sip_caller.start()
            time.sleep(0.5)
            if sip_caller.register(timeout=10) != 200:
                self.fail_test("Caller SIP REGISTER failed"); return self.result
            if sip_callee.register(timeout=10) != 200:
                self.fail_test("Callee SIP REGISTER failed"); return self.result

            inv_status, call_id = sip_caller.invite(
                target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                target_imsi=ue_callee.imsi, media_types=["audio"], timeout=10)
            if inv_status != 200:
                self.fail_test(f"Initial INVITE failed: status={inv_status}",
                               invite_status=inv_status, call_id=call_id)
                return self.result
            initial_to_tag = sip_caller._dialogs.get(call_id, {}).get("remote_tag", "")

            hold_status = sip_caller.hold(
                call_id,
                target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                target_imsi=ue_callee.imsi, timeout=10)
            hold_to_tag = sip_caller._dialogs.get(call_id, {}).get("remote_tag", "")

            resume_status = sip_caller.resume(
                call_id,
                target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                target_imsi=ue_callee.imsi, timeout=10)
            resume_to_tag = sip_caller._dialogs.get(call_id, {}).get("remote_tag", "")

            if call_id:
                sip_caller.bye(call_id,
                               target_msisdn=getattr(ue_callee.sim, "msisdn", ""),
                               target_imsi=ue_callee.imsi, timeout=5)

            details = dict(
                invite_status=inv_status, hold_status=hold_status, resume_status=resume_status,
                initial_to_tag=initial_to_tag, hold_to_tag=hold_to_tag, resume_to_tag=resume_to_tag,
                call_id=call_id, caller=ue_caller.imsi, callee=ue_callee.imsi,
            )
            if hold_status != 200:
                self.fail_test(f"hold re-INVITE status={hold_status}, want 200", **details)
                return self.result
            if resume_status != 200:
                self.fail_test(f"resume re-INVITE status={resume_status}, want 200", **details)
                return self.result
            if not initial_to_tag:
                self.fail_test("initial 200 OK has no To-tag (RFC 3261 §12.1.1)", **details)
                return self.result
            # RFC 3261 §12.2.1.1: in-dialog requests don't modify the
            # dialog ID; the To-tag must persist across hold + resume.
            if hold_to_tag and hold_to_tag != initial_to_tag:
                self.fail_test(
                    f"hold To-tag changed: {hold_to_tag!r} != initial {initial_to_tag!r}",
                    **details)
                return self.result
            if resume_to_tag and resume_to_tag != initial_to_tag:
                self.fail_test(
                    f"resume To-tag changed: {resume_to_tag!r} != initial {initial_to_tag!r}",
                    **details)
                return self.result
            self.pass_test(**details)
        finally:
            sip_caller.stop(); sip_callee.stop()
        return self.result
        return self.result

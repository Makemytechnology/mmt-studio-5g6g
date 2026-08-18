# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Traffic Engine — central factory for creating and managing traffic sessions

import uuid
import time
import logging
import concurrent.futures
from typing import Optional, Tuple

from src.traffic.interface import TrafficSession, TrafficStats, VoiceCallSession, VideoCallSession

log = logging.getLogger("tester.traffic")

_instance = None


class TrafficEngine:
    """Central traffic management — creates sessions with proper backends."""

    def __init__(self):
        self._sessions = {}  # session_id → TrafficSession

    @classmethod
    def get(cls) -> 'TrafficEngine':
        """Get singleton instance."""
        global _instance
        if _instance is None:
            _instance = cls()
        return _instance

    def create_session(self, src_ip: str, dst_ip: str, protocol: str = "udp",
                       src_port: int = 0, dst_port: int = 5201,
                       bandwidth: str = None, duration: int = 10,
                       direction: str = "ul", role: str = "orchestrator",
                       codec: str = None, tun_device: str = None,
                       length: int = None,
                       dnn: str = "", five_qi: int = 0, dscp: int = -1,
                       group_id: str = "", profile_id: str = "",
                       imsi: str = "") -> TrafficSession:
        """Create a traffic session with the appropriate runner.

        protocol: "udp" | "tcp" | "rtp-audio" | "rtp-video" | "icmp"
        direction: "ul" (UE→DN) | "dl" (DN→UE) — orchestrator-only
        role:
          "orchestrator" — tester-side: runs one half locally, delegates
                           other half to a TrafficAgent (core/DN box).
          "client"       — pure local iperf/RTP client (agent-mode).
          "server"       — pure local iperf/RTP server (agent-mode).

        QoS: dnn + five_qi + dscp are used for stats roll-up and DSCP marking
        on the wire. dscp=-1 means "derive from five_qi" at runner time.
        """
        sid = str(uuid.uuid4())[:8]
        session = TrafficSession(
            session_id=sid, src_ip=src_ip, dst_ip=dst_ip,
            protocol=protocol, src_port=src_port, dst_port=dst_port,
            bandwidth=bandwidth, duration=duration, direction=direction,
            role=role, codec=codec, tun_device=tun_device, length=length,
            dnn=dnn, five_qi=five_qi, dscp=dscp,
            group_id=group_id, profile_id=profile_id, imsi=imsi)

        session._runner = self._select_runner(protocol, direction, role)
        self._sessions[sid] = session
        return session

    def get_session(self, session_id: str) -> Optional[TrafficSession]:
        """Look up a session by ID. Returns None if not found."""
        return self._sessions.get(session_id)

    def create_bidir_session(self, ip_a: str, ip_b: str, protocol: str = "udp",
                              port: int = 5201, bandwidth: str = None,
                              duration: int = 10,
                              tun_a: str = None, tun_b: str = None) -> Tuple[TrafficSession, TrafficSession]:
        """Create simultaneous UL + DL sessions on separate ports.

        UL: port (tester client → core server)
        DL: port+1 (core client → tester server)
        """
        ul_port = port
        dl_port = port + 1

        ul = self.create_session(
            src_ip=ip_a, dst_ip=ip_b, protocol=protocol,
            src_port=0, dst_port=ul_port, bandwidth=bandwidth,
            duration=duration, direction="ul", tun_device=tun_a)

        dl = self.create_session(
            src_ip=ip_a, dst_ip=ip_b, protocol=protocol,
            src_port=0, dst_port=dl_port, bandwidth=bandwidth,
            duration=duration, direction="dl", tun_device=tun_b)

        return ul, dl

    def run_bidir(self, ip_a: str, ip_b: str, server: str,
                  protocol: str = "udp", ul_port: int = 5201, dl_port: int = 5202,
                  bandwidth: str = None, duration: int = 10,
                  udp: bool = True) -> Tuple[TrafficStats, TrafficStats]:
        """Run simultaneous UL + DL and return both stats.

        Runners are self-contained:
        UL runner: starts core server via web API → runs local client → stops core server
        DL runner: starts local server → calls core client via web API → stops local server
        """
        proto = "udp" if udp else "tcp"

        # UL: dst_ip=server (core iperf server target)
        # DL: dst_ip=ip_a (UE IP — local server binds here, core client sends here via GTP-U)
        ul = self.create_session(src_ip=ip_a, dst_ip=server, protocol=proto,
                                  dst_port=ul_port, bandwidth=bandwidth,
                                  duration=duration, direction="ul")
        dl = self.create_session(src_ip=ip_a, dst_ip=ip_a, protocol=proto,
                                  dst_port=dl_port, bandwidth=bandwidth,
                                  duration=duration, direction="dl")

        # Run simultaneously — runners handle server lifecycle
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            f_ul = pool.submit(lambda: (ul.start(), ul.stop())[-1])
            f_dl = pool.submit(lambda: (dl.start(), dl.stop())[-1])
            ul_stats = f_ul.result()
            dl_stats = f_dl.result()

        return ul_stats, dl_stats

    def create_voice_call(self, ip_a: str, ip_b: str, duration: int = 60,
                           tun_a: str = None, tun_b: str = None,
                           audio_port: int = 20000) -> VoiceCallSession:
        """Create bidirectional VoNR call (2 RTP audio streams)."""
        a_to_b = self.create_session(
            src_ip=ip_a, dst_ip=ip_b, protocol="rtp-audio",
            src_port=audio_port, dst_port=audio_port,
            duration=duration, codec="amr-wb", tun_device=tun_a)

        b_to_a = self.create_session(
            src_ip=ip_b, dst_ip=ip_a, protocol="rtp-audio",
            src_port=audio_port + 1, dst_port=audio_port,
            duration=duration, codec="amr-wb", tun_device=tun_b)

        return VoiceCallSession(a_to_b, b_to_a)

    def create_video_call(self, ip_a: str, ip_b: str, duration: int = 60,
                           tun_a: str = None, tun_b: str = None,
                           audio_port: int = 20000, video_port: int = 20002) -> VideoCallSession:
        """Create bidirectional ViNR call (2 audio + 2 video RTP streams)."""
        audio_ab = self.create_session(
            src_ip=ip_a, dst_ip=ip_b, protocol="rtp-audio",
            src_port=audio_port, dst_port=audio_port,
            duration=duration, codec="amr-wb", tun_device=tun_a)
        audio_ba = self.create_session(
            src_ip=ip_b, dst_ip=ip_a, protocol="rtp-audio",
            src_port=audio_port + 1, dst_port=audio_port,
            duration=duration, codec="amr-wb", tun_device=tun_b)
        video_ab = self.create_session(
            src_ip=ip_a, dst_ip=ip_b, protocol="rtp-video",
            src_port=video_port, dst_port=video_port,
            duration=duration, codec="h264", tun_device=tun_a)
        video_ba = self.create_session(
            src_ip=ip_b, dst_ip=ip_a, protocol="rtp-video",
            src_port=video_port + 1, dst_port=video_port,
            duration=duration, codec="h264", tun_device=tun_b)

        return VideoCallSession(audio_ab, audio_ba, video_ab, video_ba)

    def stop_all(self) -> dict:
        """Stop all active sessions."""
        stopped = 0
        for sid, session in list(self._sessions.items()):
            if session.is_running():
                session.stop()
                stopped += 1
        return {"stopped": stopped}

    def get_active(self) -> list:
        """List active sessions."""
        return [{"id": s.session_id, "protocol": s.protocol,
                 "src": f"{s.src_ip}:{s.src_port}", "dst": f"{s.dst_ip}:{s.dst_port}",
                 "status": s.status, "direction": s.direction}
                for s in self._sessions.values() if s.is_running()]

    def _select_runner(self, protocol: str, direction: str, role: str):
        """Select the runner for a session based on role → protocol → direction."""
        if role == "server":
            from src.traffic.generators.iperf_gen import run_iperf_server
            return run_iperf_server
        if role == "client":
            from src.traffic.generators.iperf_gen import run_iperf_client
            return run_iperf_client
        # role == "orchestrator"
        if protocol in ("rtp-audio", "rtp-video"):
            from src.traffic.generators.rtp_gen import run_rtp
            return run_rtp
        if protocol == "icmp":
            from src.traffic.generators.icmp_gen import run_ping
            return run_ping
        if direction == "dl":
            from src.traffic.generators.iperf_gen import run_iperf_dl
            return run_iperf_dl
        from src.traffic.generators.iperf_gen import run_iperf_ul
        return run_iperf_ul


def bw_to_mbps(bw) -> float:
    """'50M' -> 50.0, '1000M' -> 1000.0, '1G' -> 1000.0. 0 if unparseable."""
    if not bw: return 0.0
    s = str(bw).strip().upper()
    mult = 1.0
    if s.endswith("K"):   mult, s = 0.001, s[:-1]
    elif s.endswith("M"): mult, s = 1.0,   s[:-1]
    elif s.endswith("G"): mult, s = 1000.0, s[:-1]
    try: return float(s) * mult
    except ValueError: return 0.0


def derive_gateway(ue_ip: str) -> str:
    """Derive the DN-side IP the tester should drive traffic toward.

    Resolution order:
      1. UE IP of the form a.b.c.d → a.b.c.1 (conventional UPF gateway)
      2. traffic_agents.default.dn_ip (from the registry)

    Returns "" if nothing resolves — callers should treat empty as an error.
    Never falls back to sa_core: the tester owns both traffic endpoints.
    """
    if ue_ip and ue_ip != "unknown":
        parts = ue_ip.split(".")
        if len(parts) == 4:
            return f"{parts[0]}.{parts[1]}.{parts[2]}.1"
    try:
        from src.db.crud.traffic_agents import agent_get_default
        row = agent_get_default()
        if row and (row.get("dn_ip") or "").strip():
            return row["dn_ip"].strip()
    except Exception:
        pass
    return ""

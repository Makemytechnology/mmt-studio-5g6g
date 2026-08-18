# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Traffic interfaces — data classes for sessions, stats, calls

import time
import threading
from typing import Optional


class TrafficStats:
    """Results from a traffic session."""

    def __init__(self):
        self.tx_packets = 0
        self.rx_packets = 0
        self.tx_bytes = 0
        self.rx_bytes = 0
        self.throughput_kbps = 0.0
        self.jitter_ms = 0.0
        self.loss_pct = 0.0
        self.lost_packets = 0
        self.latency_ms = 0.0
        self.latency_p95_ms = 0.0
        self.mos = 0.0
        self.duration_s = 0.0
        self.retransmits = 0
        self.raw = None  # original iperf3/rtp result for debugging

    def to_dict(self):
        return {
            "tx_packets": self.tx_packets, "rx_packets": self.rx_packets,
            "tx_bytes": self.tx_bytes, "rx_bytes": self.rx_bytes,
            "throughput_kbps": round(self.throughput_kbps, 1),
            "jitter_ms": round(self.jitter_ms, 2),
            "loss_pct": round(self.loss_pct, 2),
            "lost_packets": self.lost_packets,
            "latency_ms": round(self.latency_ms, 2),
            "mos": round(self.mos, 2),
            "duration_s": round(self.duration_s, 1),
        }


class TrafficSession:
    """A single traffic flow between two endpoints.

    role:
      "orchestrator" (default) — tester-side full flow: runs one half locally
        and delegates the other half to a remote TrafficAgent.
      "client" — pure local iperf/RTP client only (agent-mode).
      "server" — pure local iperf/RTP server only (agent-mode).
    """

    def __init__(self, session_id: str, src_ip: str, dst_ip: str,
                 protocol: str, src_port: int, dst_port: int,
                 bandwidth: str = None, duration: int = 10,
                 direction: str = "ul", role: str = "orchestrator",
                 codec: str = None, tun_device: str = None,
                 length: int = None,
                 dnn: str = "", five_qi: int = 0, dscp: int = -1,
                 group_id: str = "", profile_id: str = "",
                 imsi: str = ""):
        self.session_id = session_id
        self.src_ip = src_ip
        self.dst_ip = dst_ip
        self.protocol = protocol
        self.src_port = src_port
        self.dst_port = dst_port
        self.bandwidth = bandwidth
        self.duration = duration
        self.direction = direction
        self.role = role
        self.codec = codec
        self.tun_device = tun_device
        self.length = length

        # QoS classification — used for DSCP marking and stats roll-up.
        self.dnn = dnn or ""
        self.five_qi = int(five_qi) if five_qi else 0
        self.dscp = int(dscp) if dscp is not None else -1
        self.group_id = group_id or ""
        self.profile_id = profile_id or ""
        self.imsi = imsi or ""

        self.stats = TrafficStats()
        self.status = "created"  # created | running | completed | error
        self._thread = None
        self._start_time = None
        self._runner = None  # set by engine based on protocol
        self._proc = None    # optional subprocess handle — set by runner, killed on cancel()

    def start(self):
        """Start traffic generation in background thread."""
        if not self._runner:
            raise RuntimeError("No runner set — use TrafficEngine.create_session()")
        self.status = "running"
        self._start_time = time.time()
        self._thread = threading.Thread(target=self._run, daemon=True,
                                         name=f"traffic-{self.session_id}")
        self._thread.start()

    def _run(self):
        try:
            self._runner(self)
            self.status = "completed"
        except Exception as e:
            import logging
            logging.getLogger("tester.traffic").error(
                "Session %s (%s %s) runner error: %s",
                self.session_id, self.direction, self.protocol, e, exc_info=True)
            self.status = "error"
            self.stats.raw = str(e)

    def stop(self) -> TrafficStats:
        """Wait for completion and return stats.

        Join window = duration + 15s. iperf3 with `-t N` exits at t=N on
        its own; the client has --connect-timeout to bail fast on broken
        paths. 15s is enough slack for cleanup and JSON-parse, without
        lingering 90s on a test that was meant to run 60s.
        """
        if self._thread:
            self._thread.join(timeout=self.duration + 5)
        if self._start_time:
            self.stats.duration_s = time.time() - self._start_time
        return self.stats

    def cancel(self):
        """Force-terminate a running runner (kills its subprocess, if any)."""
        proc = self._proc
        if not proc:
            return
        try:
            proc.terminate()
            try:
                proc.wait(timeout=2)
            except Exception:
                proc.kill()
        except Exception:
            pass

    def is_running(self) -> bool:
        return self.status == "running"


class VoiceCallSession:
    """Bidirectional VoNR call — 2 RTP audio streams."""

    def __init__(self, a_to_b: TrafficSession, b_to_a: TrafficSession):
        self.a_to_b = a_to_b
        self.b_to_a = b_to_a
        self.status = "created"

    def start(self):
        self.a_to_b.start()
        self.b_to_a.start()
        self.status = "running"

    def stop(self) -> TrafficStats:
        stats_a = self.a_to_b.stop()
        stats_b = self.b_to_a.stop()
        self.status = "completed"

        # Aggregate
        combined = TrafficStats()
        combined.tx_packets = stats_a.tx_packets + stats_b.tx_packets
        combined.jitter_ms = max(stats_a.jitter_ms, stats_b.jitter_ms)
        combined.loss_pct = max(stats_a.loss_pct, stats_b.loss_pct)
        combined.throughput_kbps = stats_a.throughput_kbps + stats_b.throughput_kbps
        combined.duration_s = max(stats_a.duration_s, stats_b.duration_s)

        # MOS estimation
        from src.traffic.stats.mos import estimate_mos
        delay = max(combined.jitter_ms * 4, 10)
        combined.mos = estimate_mos(delay, combined.jitter_ms, combined.loss_pct)

        return combined


class VideoCallSession:
    """Bidirectional ViNR call — 2 audio + 2 video RTP streams."""

    def __init__(self, audio_a_b: TrafficSession, audio_b_a: TrafficSession,
                 video_a_b: TrafficSession, video_b_a: TrafficSession):
        self.audio_a_b = audio_a_b
        self.audio_b_a = audio_b_a
        self.video_a_b = video_a_b
        self.video_b_a = video_b_a
        self.status = "created"

    def start(self):
        for s in (self.audio_a_b, self.audio_b_a, self.video_a_b, self.video_b_a):
            s.start()
        self.status = "running"

    def stop(self):
        stats = {}
        for name, s in [("audio_a_b", self.audio_a_b), ("audio_b_a", self.audio_b_a),
                         ("video_a_b", self.video_a_b), ("video_b_a", self.video_b_a)]:
            stats[name] = s.stop()
        self.status = "completed"

        combined = TrafficStats()
        combined.tx_packets = sum(s.tx_packets for s in stats.values())
        combined.jitter_ms = max(stats["audio_a_b"].jitter_ms, stats["audio_b_a"].jitter_ms)
        combined.loss_pct = max(stats["audio_a_b"].loss_pct, stats["audio_b_a"].loss_pct)

        from src.traffic.stats.mos import estimate_mos
        delay = max(combined.jitter_ms * 4, 10)
        combined.mos = estimate_mos(delay, combined.jitter_ms, combined.loss_pct)
        combined.raw = {k: v.to_dict() for k, v in stats.items()}

        return combined

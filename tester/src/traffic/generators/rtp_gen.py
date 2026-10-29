# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# RTP traffic generator — voice (AMR-WB) and video (H.264)

import logging
from src.traffic.interface import TrafficSession
from src.protocol.rtp_stream import send_rtp_stream, RtpStreamStats

log = logging.getLogger("tester.traffic.rtp")


def run_rtp(session: TrafficSession):
    """Run RTP stream (voice or video)."""
    media_type = "audio"
    if session.codec in ("h264", "h265") or session.protocol == "rtp-video":
        media_type = "video"

    rtp_stats = RtpStreamStats()
    send_rtp_stream(
        src_ip=session.src_ip, dst_ip=session.dst_ip,
        dst_port=session.dst_port, duration=session.duration,
        local_port=session.src_port, stats=rtp_stats,
        tun_device=session.tun_device, media_type=media_type)

    # Copy to TrafficStats
    session.stats.tx_packets = rtp_stats.tx_packets
    session.stats.throughput_kbps = rtp_stats.bitrate_kbps
    session.stats.jitter_ms = rtp_stats.jitter_ms
    session.stats.loss_pct = rtp_stats.loss_pct
    session.stats.lost_packets = rtp_stats.lost_packets

    if media_type == "audio":
        from src.traffic.stats.mos import estimate_mos
        delay = max(session.stats.jitter_ms * 4, 10)
        session.stats.mos = estimate_mos(delay, session.stats.jitter_ms, session.stats.loss_pct)

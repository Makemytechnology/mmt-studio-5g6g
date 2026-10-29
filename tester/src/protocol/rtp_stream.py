# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""RTP voice stream generator for VoNR testing.

RFC 3550 — RTP: A Transport Protocol for Real-Time Applications
TS 26.114 — IMS multimedia telephony media handling

Generates AMR-WB voice-rate RTP packets and measures quality metrics
(jitter, packet loss) for MOS estimation.
"""

import socket
import struct
import time
import threading
import logging
import os

log = logging.getLogger("tester.rtp")

# RTP header constants
RTP_VERSION = 2

# Audio: AMR-WB (TS 26.114, RFC 4867)
RTP_PT_AMR_WB = 96       # Dynamic payload type for AMR-WB (from SDP)
AMR_WB_CLOCK_RATE = 16000  # AMR-WB sample rate
AMR_WB_FRAME_MS = 20     # 20ms per AMR-WB frame
AMR_WB_PPS = 50           # 1000ms / 20ms = 50 packets/sec
AMR_WB_FRAME_SIZE = 32   # ~32 bytes payload (mode 8 = 23.85 kbps)

# Video: H.264 (TS 26.114, RFC 6184)
RTP_PT_H264 = 99          # Dynamic payload type for H.264 (from SDP)
H264_CLOCK_RATE = 90000   # H.264 RTP clock rate
H264_FRAME_MS = 33        # ~30 fps = 33ms per frame
H264_PPS = 30             # 30 packets/sec
H264_FRAME_SIZE = 1200    # ~1200 bytes per NAL unit (2 Mbps / 30 fps / 8)


class RtpStreamStats:
    """Collected stats from an RTP stream."""
    def __init__(self):
        self.tx_packets = 0
        self.rx_packets = 0
        self.tx_bytes = 0
        self.rx_bytes = 0
        self.lost_packets = 0
        self.jitter_ms = 0.0
        self.max_jitter_ms = 0.0
        self.duration_s = 0.0

    @property
    def loss_pct(self):
        expected = self.tx_packets if self.tx_packets > 0 else self.rx_packets
        if expected == 0:
            return 0.0
        return round((self.lost_packets / expected) * 100, 2)

    @property
    def bitrate_kbps(self):
        if self.duration_s <= 0:
            return 0.0
        return round((self.tx_bytes * 8) / (self.duration_s * 1000), 2)

    def to_dict(self):
        return {
            'tx_packets': self.tx_packets,
            'rx_packets': self.rx_packets,
            'tx_bytes': self.tx_bytes,
            'rx_bytes': self.rx_bytes,
            'lost_packets': self.lost_packets,
            'jitter_ms': round(self.jitter_ms, 3),
            'max_jitter_ms': round(self.max_jitter_ms, 3),
            'loss_pct': self.loss_pct,
            'bitrate_kbps': self.bitrate_kbps,
            'duration_s': round(self.duration_s, 1),
        }


def build_rtp_packet(seq, timestamp, ssrc, payload, pt=RTP_PT_AMR_WB):
    """Build an RTP packet per RFC 3550 §5.1."""
    byte0 = (RTP_VERSION << 6)  # V=2, P=0, X=0, CC=0
    byte1 = pt & 0x7F           # M=0, PT
    header = struct.pack('!BBHII', byte0, byte1, seq & 0xFFFF, timestamp, ssrc)
    return header + payload


def send_rtp_stream(local_ip, remote_ip, remote_port, duration_s,
                    local_port=20000, stats=None, tun_device=None,
                    media_type="audio"):
    """Send RTP stream (audio or video).

    Args:
        local_ip: UE's IP (bind address)
        remote_ip: Destination IP (other UE)
        remote_port: Destination RTP port
        duration_s: Duration in seconds
        local_port: Source RTP port (from SDP m= line)
        stats: RtpStreamStats object to populate
        tun_device: TUN interface name for SO_BINDTODEVICE (forces GTP-U)
        media_type: "audio" (AMR-WB, 5QI=1) or "video" (H.264, 5QI=2)

    Returns: RtpStreamStats
    """
    if stats is None:
        stats = RtpStreamStats()

    # Select media profile
    if media_type == "video":
        pt = RTP_PT_H264
        clock_rate = H264_CLOCK_RATE
        frame_ms = H264_FRAME_MS
        pps = H264_PPS
        frame_size = H264_FRAME_SIZE
    else:  # audio
        pt = RTP_PT_AMR_WB
        clock_rate = AMR_WB_CLOCK_RATE
        frame_ms = AMR_WB_FRAME_MS
        pps = AMR_WB_PPS
        frame_size = AMR_WB_FRAME_SIZE

    ssrc = struct.unpack('!I', os.urandom(4))[0]
    seq = 0
    timestamp = 0
    frame_interval = frame_ms / 1000.0
    ts_increment = clock_rate // pps

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        if tun_device:
            try:
                sock.setsockopt(socket.SOL_SOCKET, socket.SO_BINDTODEVICE,
                                tun_device.encode() + b'\0')
            except OSError as e:
                log.warning("SO_BINDTODEVICE %s failed: %s", tun_device, e)
        sock.bind((local_ip, local_port))

        payload = os.urandom(frame_size)

        start = time.monotonic()
        next_send = start

        while (time.monotonic() - start) < duration_s:
            pkt = build_rtp_packet(seq, timestamp, ssrc, payload, pt)
            try:
                sock.sendto(pkt, (remote_ip, remote_port))
                stats.tx_packets += 1
                stats.tx_bytes += len(pkt)
            except OSError as e:
                log.debug("RTP send error: %s", e)

            seq += 1
            timestamp += ts_increment

            next_send += frame_interval
            sleep_time = next_send - time.monotonic()
            if sleep_time > 0:
                time.sleep(sleep_time)

        stats.duration_s = time.monotonic() - start
    finally:
        sock.close()

    log.info("RTP %s TX: %d pkts, %.1f kbps, %.1fs %s:%d → %s:%d",
             media_type, stats.tx_packets, stats.bitrate_kbps, stats.duration_s,
             local_ip, local_port, remote_ip, remote_port)
    return stats


def receive_rtp_stream(local_ip, local_port, duration_s, stats=None):
    """Receive RTP stream and measure jitter/loss.

    Jitter calculated per RFC 3550 §6.4.1.
    """
    if stats is None:
        stats = RtpStreamStats()

    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        sock.bind((local_ip, local_port))
        sock.settimeout(1.0)

        last_arrival = None
        last_rtp_ts = None
        jitter = 0.0
        max_seq = -1
        min_seq = -1

        start = time.monotonic()
        while (time.monotonic() - start) < duration_s + 2:  # +2s grace
            try:
                data, addr = sock.recvfrom(2048)
            except socket.timeout:
                if (time.monotonic() - start) > duration_s:
                    break
                continue

            if len(data) < 12:
                continue

            # Parse RTP header
            byte0, byte1, seq, rtp_ts, ssrc = struct.unpack('!BBHII', data[:12])
            version = (byte0 >> 6) & 0x03
            if version != 2:
                continue

            stats.rx_packets += 1
            stats.rx_bytes += len(data)

            # Track sequence for loss calculation
            if min_seq < 0:
                min_seq = seq
            max_seq = max(max_seq, seq)

            # Jitter calculation (RFC 3550 §A.8)
            now = time.monotonic()
            if last_arrival is not None and last_rtp_ts is not None:
                # D(i,j) = (Rj - Ri) - (Sj - Si)
                arrival_diff = (now - last_arrival) * RTP_CLOCK_RATE
                rtp_diff = rtp_ts - last_rtp_ts
                d = abs(arrival_diff - rtp_diff)
                jitter += (d - jitter) / 16.0
            last_arrival = now
            last_rtp_ts = rtp_ts

        stats.duration_s = time.monotonic() - start
        stats.jitter_ms = (jitter / RTP_CLOCK_RATE) * 1000
        stats.max_jitter_ms = stats.jitter_ms  # simplified

        # Loss calculation from sequence numbers
        if max_seq >= 0 and min_seq >= 0:
            expected = max_seq - min_seq + 1
            stats.lost_packets = max(0, expected - stats.rx_packets)

    finally:
        sock.close()

    log.info("RTP RX done: %d/%d pkts (%.1f%% loss), jitter=%.1fms, %.1fs on %s:%d",
             stats.rx_packets, stats.rx_packets + stats.lost_packets,
             stats.loss_pct, stats.jitter_ms, stats.duration_s,
             local_ip, local_port)
    return stats


def voice_call(local_ip, remote_ip, rtp_port=20000, duration_s=60):
    """Run a bidirectional voice call: send RTP + receive simultaneously.

    Returns: (tx_stats, rx_stats)
    """
    tx_stats = RtpStreamStats()
    rx_stats = RtpStreamStats()

    # Receiver on local port
    rx_thread = threading.Thread(
        target=receive_rtp_stream,
        args=(local_ip, rtp_port, duration_s, rx_stats),
        daemon=True)

    # Sender to remote
    tx_thread = threading.Thread(
        target=send_rtp_stream,
        args=(local_ip, remote_ip, rtp_port, duration_s, rtp_port + 1, tx_stats),
        daemon=True)

    rx_thread.start()
    time.sleep(0.1)
    tx_thread.start()

    tx_thread.join(timeout=duration_s + 10)
    rx_thread.join(timeout=duration_s + 10)

    return tx_stats, rx_stats

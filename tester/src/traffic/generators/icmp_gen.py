# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# ICMP ping generator

import subprocess
import logging
from src.traffic.interface import TrafficSession

log = logging.getLogger("tester.traffic.icmp")


def run_ping(session: TrafficSession):
    """Run ping from src_ip to dst_ip."""
    count = session.duration * 5  # 5 pings per second
    cmd = ["ping", "-c", str(count), "-i", "0.2",
           "-I", session.src_ip, "-W", "2", session.dst_ip]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True,
                              timeout=session.duration + 30)
        if proc.returncode == 0:
            session.stats.tx_packets = count
            for line in proc.stdout.split("\n"):
                if "min/avg/max" in line:
                    parts = line.split("=")[1].strip().split("/")
                    session.stats.latency_ms = float(parts[1])  # avg
                    session.stats.latency_p95_ms = float(parts[2])  # max as proxy
                if "packet loss" in line:
                    for part in line.split(","):
                        if "packet loss" in part:
                            session.stats.loss_pct = float(part.strip().split("%")[0])
            session.stats.rx_packets = int(count * (1 - session.stats.loss_pct / 100))
    except Exception as e:
        log.warning("Ping error: %s", e)

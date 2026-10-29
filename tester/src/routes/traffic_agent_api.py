# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/traffic_agent_api.py — Traffic Agent API routes.
#
# This router is mounted ONLY by the standalone agent entrypoint
# (src/traffic/agent_main.py) running on a DN / APN-side host.
# The tester's main app does NOT mount this router.
#
# Design rules:
#   - Agent is a pure executor. No orchestration, no voice/video compositing,
#     no bidir, no sa_core access.
#   - Only role=client|server sessions are accepted.
#   - Optional X-Agent-Token header is enforced when a token is configured.

import logging
import os
import shutil
import socket
import subprocess

from fastapi import APIRouter, Header, HTTPException, Request

log = logging.getLogger("tester.agent.api")

traffic_agent_api_router = APIRouter()


# ── Auth (shared-secret header) ──

def _configured_token() -> str:
    """Token the agent expects. Empty string disables auth."""
    return os.environ.get("SA_AGENT_TOKEN", "").strip()


def _check_token(x_agent_token: str):
    expected = _configured_token()
    if not expected:
        return  # auth disabled
    if not x_agent_token or not _constant_time_eq(x_agent_token, expected):
        raise HTTPException(status_code=401, detail="invalid or missing X-Agent-Token")


def _constant_time_eq(a: str, b: str) -> bool:
    if len(a) != len(b):
        return False
    out = 0
    for x, y in zip(a.encode(), b.encode()):
        out |= x ^ y
    return out == 0


def _engine():
    from src.traffic.engine import TrafficEngine
    return TrafficEngine.get()


# ── Session lifecycle ──

@traffic_agent_api_router.post("/api/traffic/start")
async def api_agent_start(request: Request,
                           x_agent_token: str = Header(default="")):
    """Start a pure-local traffic session.

    Accepts only role=client|server. Anything else is rejected — the agent
    never orchestrates, never delegates, never composites.
    """
    _check_token(x_agent_token)
    d = await request.json()

    role = (d.get("role") or "").strip().lower()
    if role not in ("client", "server"):
        raise HTTPException(
            status_code=400,
            detail=f"agent only accepts role=client|server (got {role!r})")
    if d.get("direction") == "bidir":
        raise HTTPException(status_code=400, detail="agent does not orchestrate bidir")

    port = int(d.get("port") or d.get("dst_port") or 5201)
    session = _engine().create_session(
        src_ip=d.get("src_ip", ""),
        dst_ip=d.get("dst_ip", ""),
        protocol=d.get("protocol", "udp"),
        src_port=int(d.get("src_port", 0)),
        dst_port=port,
        bandwidth=d.get("bandwidth"),
        duration=int(d.get("duration", 60)),
        role=role,
        length=(int(d["length"]) if d.get("length") else None),
        dnn=d.get("dnn", ""),
        five_qi=int(d.get("five_qi") or 0),
        dscp=int(d["dscp"]) if d.get("dscp") is not None else -1,
        group_id=d.get("group_id", ""),
        profile_id=d.get("profile_id", ""),
        imsi=d.get("imsi", ""))
    session.start()
    return {"ok": True, "session_id": session.session_id, "role": role}


@traffic_agent_api_router.get("/api/traffic/sessions/{session_id}")
def api_agent_session_status(session_id: str,
                              x_agent_token: str = Header(default="")):
    _check_token(x_agent_token)
    session = _engine().get_session(session_id)
    if not session:
        raise HTTPException(status_code=404, detail=f"session {session_id} not found")
    return {
        "session_id": session.session_id,
        "status": session.status,
        "role": session.role,
        "protocol": session.protocol,
        "stats": session.stats.to_dict(),
    }


@traffic_agent_api_router.post("/api/traffic/sessions/{session_id}/stop")
def api_agent_session_stop(session_id: str,
                            x_agent_token: str = Header(default="")):
    _check_token(x_agent_token)
    session = _engine().get_session(session_id)
    if not session:
        raise HTTPException(status_code=404, detail=f"session {session_id} not found")
    if session.is_running():
        session.cancel()
    stats = session.stop()
    return {"ok": True, "session_id": session.session_id,
            "status": session.status, "stats": stats.to_dict()}


@traffic_agent_api_router.post("/api/traffic/stop-all")
def api_agent_stop_all(x_agent_token: str = Header(default="")):
    _check_token(x_agent_token)
    return _engine().stop_all()


@traffic_agent_api_router.get("/api/traffic/active")
def api_agent_active(x_agent_token: str = Header(default="")):
    _check_token(x_agent_token)
    return {"items": _engine().get_active()}


# ── Introspection ──

@traffic_agent_api_router.get("/api/traffic/capabilities")
def api_agent_capabilities():
    """Report what this agent can run. Not gated by the token — used for discovery."""
    iperf_version = None
    if shutil.which("iperf3"):
        try:
            r = subprocess.run(["iperf3", "--version"], capture_output=True,
                                text=True, timeout=3)
            text = (r.stdout or r.stderr or "").strip()
            iperf_version = text.splitlines()[0] if text else None
        except Exception:
            pass
    return {
        "ok": True,
        "hostname": socket.gethostname(),
        "protocols": ["udp", "tcp", "rtp-audio", "rtp-video", "icmp"],
        "roles": ["client", "server"],
        "iperf3": iperf_version,
        "ping": bool(shutil.which("ping")),
        "auth_required": bool(_configured_token()),
    }


@traffic_agent_api_router.get("/api/traffic/healthz")
def api_agent_healthz():
    return {"ok": True}


# ── DN-side observability (local NIC counters only — no sa_core) ──

@traffic_agent_api_router.get("/api/traffic/dn-stats")
def api_agent_dn_stats(iface: str = None,
                        x_agent_token: str = Header(default="")):
    """Per-interface rx/tx counters from `/proc/net/dev`.

    Optional ?iface=<name> filter. Values are cumulative since boot — callers
    should snapshot before/after and compute deltas themselves.
    """
    _check_token(x_agent_token)
    return {"interfaces": _read_proc_net_dev(iface_filter=iface)}


def _read_proc_net_dev(iface_filter: str = None) -> list:
    """Parse /proc/net/dev into a list of per-interface stat dicts."""
    try:
        with open("/proc/net/dev", "r") as f:
            lines = f.readlines()
    except Exception as e:
        log.warning("failed to read /proc/net/dev: %s", e)
        return []
    # Skip the two header lines.
    rows = []
    for line in lines[2:]:
        if ":" not in line:
            continue
        name, rest = line.split(":", 1)
        name = name.strip()
        if iface_filter and name != iface_filter:
            continue
        fields = rest.split()
        if len(fields) < 16:
            continue
        # Columns per kernel docs: recv {bytes, packets, errs, drop, fifo, frame,
        # compressed, multicast} then send {bytes, packets, errs, drop, fifo,
        # colls, carrier, compressed}
        try:
            rows.append({
                "iface":      name,
                "rx_bytes":   int(fields[0]),
                "rx_packets": int(fields[1]),
                "rx_errors":  int(fields[2]),
                "rx_dropped": int(fields[3]),
                "tx_bytes":   int(fields[8]),
                "tx_packets": int(fields[9]),
                "tx_errors":  int(fields[10]),
                "tx_dropped": int(fields[11]),
            })
        except (ValueError, IndexError):
            continue
    return rows

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# iperf3 traffic generator — wraps iperf3 subprocess.
#
# Four runners:
#   run_iperf_client(session)  — pure local iperf3 -c (agent-mode role=client)
#   run_iperf_server(session)  — pure local iperf3 -s (agent-mode role=server)
#   run_iperf_ul(session)      — orchestrator UL: local client + remote server via TrafficAgent
#   run_iperf_dl(session)      — orchestrator DL: local server + remote client via TrafficAgent

import json
import socket
import subprocess
import logging
import time

from src.traffic.interface import TrafficSession, TrafficStats

log = logging.getLogger("tester.traffic.iperf")


# ── Pure local runners (called by agent, and by orchestrator for its half) ──

def run_iperf_client(session: TrafficSession):
    """Run iperf3 client locally. Parses stats into session.stats."""
    udp = (session.protocol != "tcp")
    cmd = ["iperf3", "-c", session.dst_ip, "-p", str(session.dst_port),
           "-t", str(session.duration), "-J",
           # --connect-timeout caps iperf3's own TCP-SYN retry. Without it,
           # Linux retries for ~127 s, longer than our subprocess watchdog,
           # so we'd SIGKILL iperf3 before it produced an error JSON. 10 s is
           # plenty for a working path and still leaves ~20 s slack below our
           # 30 s cushion for iperf3 to exit cleanly with rc=1.
           "--connect-timeout", "10000"]
    if session.src_ip and session.src_ip != "unknown":
        cmd.extend(["-B", session.src_ip])
    if udp:
        cmd.append("-u")
    if session.length:
        cmd.extend(["--length", str(session.length)])
    if session.bandwidth:
        cmd.extend(["-b", session.bandwidth])
    tos = _resolve_tos(session)
    if tos is not None:
        cmd.extend(["--tos", str(tos)])

    log.debug("iperf3 client: %s", " ".join(cmd))
    try:
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        session._proc = proc
        # +5s slack: iperf3 should exit at -t N on its own. If control
        # channel is degraded it hangs waiting for the server's final-report
        # TCP ack — send SIGINT first (iperf3 handles it gracefully, flushes
        # JSON), then SIGKILL if it still won't go.
        try:
            out, err = proc.communicate(timeout=session.duration + 5)
        except subprocess.TimeoutExpired:
            proc.terminate()           # SIGTERM — iperf3 catches and flushes
            try:
                out, err = proc.communicate(timeout=2)
            except subprocess.TimeoutExpired:
                proc.kill()
                out, err = proc.communicate()
        session._proc = None
        if proc.returncode == 0:
            data = json.loads(out)
            _parse_iperf_result(session.stats, data, udp=udp)
            session.stats.raw = data
            log.debug("client OK: %.1f kbps", session.stats.throughput_kbps)
            return
        reason = _extract_iperf_error(out, err)
        log.warning("iperf3 client rc=%d dst=%s:%d: %s",
                    proc.returncode, session.dst_ip, session.dst_port, reason)
        session.stats.raw = reason
    except Exception as e:
        session._proc = None
        log.warning("iperf3 client exception: %s", e)


def run_iperf_server(session: TrafficSession):
    """Run iperf3 server locally (--one-off → exits when client disconnects).

    Binds to session.src_ip (or 0.0.0.0) on session.dst_port.
    On cancel(), the subprocess is killed via session._proc.
    """
    bind_ip = session.src_ip or "0.0.0.0"

    # Pre-bind sanity check so "cannot assign requested address" (tun not up
    # yet, wrong IP, etc.) surfaces with a clear OS error before iperf3 swallows
    # it into its JSON blob.
    if bind_ip not in ("0.0.0.0", ""):
        err = _check_bindable(bind_ip, session.dst_port)
        if err:
            log.warning("iperf3 server bind-check failed on %s:%d: %s",
                         bind_ip, session.dst_port, err)
            session.stats.raw = f"bind check failed: {err}"
            return

    cmd = ["iperf3", "-s", "-B", bind_ip, "-p", str(session.dst_port),
           "--one-off", "-J"]
    log.debug("iperf3 server: %s", " ".join(cmd))
    try:
        proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        session._proc = proc
        # --one-off exits after the first client session completes, so
        # duration + 15s is plenty. The orchestrator calls .cancel() in
        # its finally block the moment its remote client finishes, so a
        # huge buffer here was just dead wait at cleanup time.
        try:
            out, err = proc.communicate(timeout=session.duration + 5)
        except subprocess.TimeoutExpired:
            proc.kill()
            out, err = proc.communicate()
        session._proc = None
        if proc.returncode == 0 and out:
            try:
                data = json.loads(out)
                _parse_iperf_result(session.stats, data,
                                     udp=(session.protocol != "tcp"))
                session.stats.raw = data
                log.debug("server OK: %.1f kbps", session.stats.throughput_kbps)
            except Exception as e:
                log.warning("iperf3 server JSON parse failed: %s", e)
        else:
            # iperf3 -J writes errors to stdout as JSON; include both streams.
            reason = _extract_iperf_error(out, err)
            log.warning("iperf3 server rc=%d on %s:%d: %s",
                        proc.returncode, bind_ip, session.dst_port, reason)
            session.stats.raw = reason
    except Exception as e:
        session._proc = None
        log.warning("iperf3 server exception: %s", e)


# ── Orchestrator runners (tester-side — talk to remote TrafficAgent) ──

def _orchestrator_agent(session: TrafficSession):
    """Pick the best TrafficAgent for this session — by DNN if set, else default.

    Raises NoAgentConfigured with a clear operator-facing message if the
    traffic_agents registry is empty. The usual cause is a fresh install
    where nobody has registered a DN-side agent yet — tell them where to
    fix it rather than burying the error inside a generic session trace.
    """
    from src.traffic.remote import TrafficAgent, NoAgentConfigured
    try:
        if session.dnn:
            return TrafficAgent.for_dnn(session.dnn)
        return TrafficAgent.default()
    except NoAgentConfigured as e:
        log.error(
            "No traffic agent registered — every UL/DL session will fail "
            "silently until one is added. Run `./run_traffic_engine.sh` on "
            "the DN host, then register it via the UI (Infrastructure tab "
            "→ Traffic Agents → + Add Agent) or the CRUD: "
            "`from src.db.crud.traffic_agents import agent_add; "
            "agent_add('core-dn', 'http://<dn_host>:9100', "
            "dn_ip='10.45.0.1', is_default=True)`. Original: %s", e)
        raise


def _qos_spec_fields(session: TrafficSession) -> dict:
    """Fields to pass along to the remote agent so its runner can mark DSCP."""
    return {
        "dnn": session.dnn,
        "five_qi": session.five_qi,
        "dscp": session.dscp,
        "group_id": session.group_id,
        "profile_id": session.profile_id,
        "imsi": session.imsi,
    }


def run_iperf_ul(session: TrafficSession):
    """UL orchestrator: ask agent to start server, run local client, stop server."""
    agent = _orchestrator_agent(session)

    remote_spec = {
        "role": "server",
        "protocol": session.protocol,
        "src_ip": "0.0.0.0",
        "dst_ip": "",
        "port": session.dst_port,
        "duration": session.duration,
        "length": session.length,
        **_qos_spec_fields(session),
    }
    remote_sid = agent.start(remote_spec)
    if not remote_sid:
        log.warning("UL: agent failed to start server on port %d", session.dst_port)
        return
    # Let iperf3 -s finish binding before the client dials in.
    time.sleep(0.5)
    try:
        run_iperf_client(session)
    finally:
        agent.stop(remote_sid)


def run_iperf_dl(session: TrafficSession):
    """DL orchestrator: run local server, ask agent to run client, collect stats."""
    from src.traffic.engine import TrafficEngine
    engine = TrafficEngine.get()

    # Local server session — binds to UE IP, auto-exits after client finishes.
    local = engine.create_session(
        src_ip=session.dst_ip, dst_ip="",
        protocol=session.protocol,
        src_port=0, dst_port=session.dst_port,
        duration=session.duration, role="server",
        length=session.length,
        dnn=session.dnn, five_qi=session.five_qi, dscp=session.dscp,
        group_id=session.group_id, profile_id=session.profile_id,
        imsi=session.imsi)
    local.start()
    time.sleep(0.5)

    agent = _orchestrator_agent(session)
    remote_spec = {
        "role": "client",
        "protocol": session.protocol,
        "src_ip": "",
        "dst_ip": session.dst_ip,
        "port": session.dst_port,
        "duration": session.duration,
        "bandwidth": session.bandwidth,
        "length": session.length,
        **_qos_spec_fields(session),
    }
    remote_sid = agent.start(remote_spec)
    if not remote_sid:
        log.warning("DL: agent failed to start client for %s:%d",
                    session.dst_ip, session.dst_port)
        local.cancel()
        local.stop()
        return

    try:
        remote_stats = agent.wait(remote_sid, timeout=session.duration + 5)
        if remote_stats:
            _apply_remote_stats(session.stats, remote_stats)
            session.stats.raw = remote_stats
            log.debug("DL OK: %.1f kbps jitter=%.2f loss=%.2f",
                      session.stats.throughput_kbps,
                      session.stats.jitter_ms, session.stats.loss_pct)
        else:
            log.warning("DL: no stats from agent for %s:%d",
                        session.dst_ip, session.dst_port)
    finally:
        # Local server exits on --one-off; cancel guards against hangs.
        local.cancel()
        local.stop()


# ── Stats parsing ──

def _parse_iperf_result(stats: TrafficStats, data: dict, udp: bool = False):
    """Parse raw iperf3 JSON into TrafficStats.

    iperf3's end.sum is sender-side; end.sum_received is receiver-side.
    On a server, sum reports 0 bytes — read throughput from sum_received.
    """
    end = data.get("end", {})
    if udp:
        s = end.get("sum", {}) or {}
        recv = end.get("sum_received", {}) or {}
        # Prefer whichever side actually observed data.
        primary = s if s.get("bytes", 0) else recv
        stats.throughput_kbps = round(primary.get("bits_per_second", 0) / 1000, 1)
        stats.jitter_ms = round((recv.get("jitter_ms") or s.get("jitter_ms") or 0), 2)
        stats.loss_pct = round((recv.get("lost_percent") or s.get("lost_percent") or 0), 2)
        stats.tx_packets = s.get("packets", 0) or recv.get("packets", 0)
        stats.lost_packets = recv.get("lost_packets") or s.get("lost_packets") or 0
        stats.rx_packets = stats.tx_packets - stats.lost_packets
        stats.tx_bytes = s.get("bytes", 0)
        stats.rx_bytes = recv.get("bytes", 0)
    else:
        sent = end.get("sum_sent", {}) or {}
        recv = end.get("sum_received", {}) or {}
        primary = sent if sent.get("bytes", 0) else recv
        stats.throughput_kbps = round(primary.get("bits_per_second", 0) / 1000, 1)
        stats.tx_bytes = sent.get("bytes", 0)
        stats.rx_bytes = recv.get("bytes", 0)
        stats.retransmits = sent.get("retransmits", 0)


def _check_bindable(ip: str, port: int) -> str:
    """Pre-flight: confirm the OS can bind (ip, port). Returns error string
    on failure, empty string on success.

    This catches the common "tun not up yet" and "wrong UE IP" cases with a
    clean OS error message before iperf3 buries them in a JSON blob.
    """
    for fam, stype in ((socket.AF_INET, socket.SOCK_STREAM),
                       (socket.AF_INET, socket.SOCK_DGRAM)):
        s = socket.socket(fam, stype)
        try:
            s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            s.bind((ip, 0))
        except OSError as e:
            return f"{e.errno} {e.strerror} (proto={'tcp' if stype==socket.SOCK_STREAM else 'udp'})"
        finally:
            s.close()
    # Check the actual port too (TCP only — UDP bind of same port in quick succession is fine).
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        s.bind((ip, port))
    except OSError as e:
        return f"{e.errno} {e.strerror} (port {port} busy?)"
    finally:
        s.close()
    return ""


def _extract_iperf_error(out: bytes, err: bytes) -> str:
    """Best-effort pull of a human message from iperf3's output streams.

    iperf3 -J writes {'error': '...'} to stdout on failure; stderr is usually
    empty. Fall back to either stream's raw text.
    """
    try:
        data = json.loads(out or b"")
        if isinstance(data, dict) and data.get("error"):
            return str(data["error"])[:500]
    except Exception:
        pass
    for blob in (err, out):
        if not blob:
            continue
        s = blob.decode(errors="replace").strip()
        if s:
            return s[:500]
    return "(no output)"


def _resolve_tos(session: TrafficSession):
    """Return the TOS byte to hand to `iperf3 --tos`, or None if no QoS set.

    Resolution order:
      1. session.dscp >= 0   → use it (explicit override)
      2. session.five_qi > 0 → look up standard 3GPP 5QI → DSCP mapping
      3. otherwise           → no TOS marking
    """
    try:
        from src.db.crud.traffic_profiles import dscp_for_five_qi, tos_for_dscp
    except Exception:
        return None
    if session.dscp is not None and session.dscp >= 0:
        return tos_for_dscp(session.dscp)
    if session.five_qi and session.five_qi > 0:
        return tos_for_dscp(dscp_for_five_qi(session.five_qi))
    return None


def _apply_remote_stats(stats: TrafficStats, data: dict):
    """Apply a TrafficStats-shaped dict (from agent) onto a local TrafficStats."""
    stats.throughput_kbps = round(data.get("throughput_kbps", 0), 1)
    stats.jitter_ms = round(data.get("jitter_ms", 0), 2)
    stats.loss_pct = round(data.get("loss_pct", 0), 2)
    stats.tx_packets = data.get("tx_packets", 0)
    stats.rx_packets = data.get("rx_packets", 0)
    stats.lost_packets = data.get("lost_packets", 0)
    stats.tx_bytes = data.get("tx_bytes", 0)
    stats.rx_bytes = data.get("rx_bytes", 0)
    stats.retransmits = data.get("retransmits", 0)
    stats.duration_s = data.get("duration_s", 0)

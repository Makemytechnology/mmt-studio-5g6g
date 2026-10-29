# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Group + Profile orchestration for TrafficEngine.
#
# A profile is a declarative QoS class (DNN + 5QI + protocol + rates + SLA).
# A group binds a profile to a set of UE-member params with a run pattern.
# run_profile() expands one profile into N sessions; run_group() resolves
# group members from the DB and dispatches via run_profile().

import logging
import threading
import time
import uuid
from typing import Dict, List, Optional

from src.traffic.interface import TrafficSession, TrafficStats

log = logging.getLogger("tester.traffic.groups")


# ──────────────────────────────────────────────────────────────────────────
# Stats rollup

class GroupResult:
    """Aggregate outcome of a group run, with roll-ups by 5QI and by DNN."""

    def __init__(self, run_id: str, group_id: str = "", profile_id: str = ""):
        self.run_id = run_id
        self.group_id = group_id
        self.profile_id = profile_id
        self.status = "created"   # created | running | completed | error
        self.started_at = None
        self.finished_at = None
        self.sessions: List[TrafficSession] = []
        self.sla_met: Optional[bool] = None
        self.sla_violations: List[Dict] = []

    def by_five_qi(self) -> Dict[int, Dict]:
        out: Dict[int, Dict] = {}
        for s in self.sessions:
            out.setdefault(s.five_qi, []).append(s.stats)
        return {q: _agg(stats_list) for q, stats_list in out.items()}

    def by_dnn(self) -> Dict[str, Dict]:
        out: Dict[str, Dict] = {}
        for s in self.sessions:
            out.setdefault(s.dnn or "", []).append(s.stats)
        return {d: _agg(stats_list) for d, stats_list in out.items()}

    def to_dict(self) -> Dict:
        return {
            "run_id": self.run_id,
            "group_id": self.group_id,
            "profile_id": self.profile_id,
            "status": self.status,
            "started_at": self.started_at,
            "finished_at": self.finished_at,
            "session_count": len(self.sessions),
            "sessions": [{
                "session_id": s.session_id, "status": s.status,
                "imsi": s.imsi, "dnn": s.dnn, "five_qi": s.five_qi,
                "dscp": s.dscp, "protocol": s.protocol, "direction": s.direction,
                "stats": s.stats.to_dict(),
            } for s in self.sessions],
            "by_five_qi": {str(k): v for k, v in self.by_five_qi().items()},
            "by_dnn": self.by_dnn(),
            "sla_met": self.sla_met,
            "sla_violations": self.sla_violations,
        }


def _agg(stats_list: List[TrafficStats]) -> Dict:
    """Aggregate a list of TrafficStats into one summary dict."""
    if not stats_list:
        return {}
    n = len(stats_list)
    tp = [s.throughput_kbps for s in stats_list]
    jit = [s.jitter_ms for s in stats_list]
    loss = [s.loss_pct for s in stats_list]
    lat = [s.latency_ms for s in stats_list if s.latency_ms]
    return {
        "flows": n,
        "throughput_kbps_total": round(sum(tp), 1),
        "throughput_kbps_avg":   round(sum(tp) / n, 1),
        "throughput_kbps_min":   round(min(tp), 1),
        "throughput_kbps_max":   round(max(tp), 1),
        "jitter_ms_avg":         round(sum(jit) / n, 2),
        "jitter_ms_max":         round(max(jit), 2),
        "loss_pct_avg":          round(sum(loss) / n, 2),
        "loss_pct_max":          round(max(loss), 2),
        "latency_ms_avg":        round(sum(lat) / len(lat), 2) if lat else 0.0,
        "tx_packets_total":      sum(s.tx_packets for s in stats_list),
        "rx_packets_total":      sum(s.rx_packets for s in stats_list),
    }


def _check_sla(profile: Dict, agg: Dict) -> List[str]:
    """Return list of SLA violation strings for an aggregate block."""
    violations = []
    min_kbps = profile.get("sla_min_kbps", 0) or 0
    if min_kbps and agg.get("throughput_kbps_min", 0) < min_kbps:
        violations.append(
            f"throughput {agg['throughput_kbps_min']} < min {min_kbps} kbps")
    max_jit = profile.get("sla_max_jitter_ms", 0) or 0
    if max_jit and agg.get("jitter_ms_max", 0) > max_jit:
        violations.append(
            f"jitter {agg['jitter_ms_max']} > max {max_jit} ms")
    max_loss = profile.get("sla_max_loss_pct", 0) or 0
    if max_loss and agg.get("loss_pct_max", 0) > max_loss:
        violations.append(
            f"loss {agg['loss_pct_max']} > max {max_loss}%")
    max_lat = profile.get("sla_max_latency_ms", 0) or 0
    if max_lat and agg.get("latency_ms_avg", 0) > max_lat:
        violations.append(
            f"latency {agg['latency_ms_avg']} > max {max_lat} ms")
    return violations


# ──────────────────────────────────────────────────────────────────────────
# In-memory run registry (short-lived; reboots wipe it)

_runs: Dict[str, GroupResult] = {}
_runs_lock = threading.Lock()


def get_run(run_id: str) -> Optional[GroupResult]:
    with _runs_lock:
        return _runs.get(run_id)


def list_runs() -> List[Dict]:
    with _runs_lock:
        return [
            {"run_id": r.run_id, "group_id": r.group_id,
             "profile_id": r.profile_id, "status": r.status,
             "started_at": r.started_at, "finished_at": r.finished_at,
             "session_count": len(r.sessions)}
            for r in _runs.values()
        ]


# ──────────────────────────────────────────────────────────────────────────
# Member resolution
#
# Member params: per-UE data needed to construct a session:
#   { imsi, src_ip (UE IP), tun_device (optional), dst_ip (override DN IP) }
#
# Callers supply member_params explicitly (from the UE FSM pool, from a
# fixture, or from the pdu_session_flows table). The resolvers below are
# convenience — they aren't authoritative.

def resolve_from_flows(imsis: List[str], dnn: str, five_qi: int) -> List[Dict]:
    """Resolve member params from the pdu_session_flows snapshot table."""
    from src.db.crud.traffic_profiles import flow_get
    out = []
    for imsi in imsis:
        flow = flow_get(imsi, dnn, five_qi)
        if not flow:
            log.debug("no flow for imsi=%s dnn=%s 5qi=%s — skipping", imsi, dnn, five_qi)
            continue
        out.append({
            "imsi": imsi,
            "src_ip": flow.get("ue_ip") or "",
            "dst_ip": flow.get("dn_ip") or "",
            "tun_device": flow.get("tun_device") or "",
        })
    return out


# ──────────────────────────────────────────────────────────────────────────
# Profile / Group runners

def run_profile(profile: Dict, members: List[Dict],
                 base_port: int = 5201) -> GroupResult:
    """Run one session per member, all derived from `profile`.

    members: list of {imsi, src_ip, [dst_ip], [tun_device]} dicts. Each UE gets
             a distinct port (base_port + index) so agent-side servers don't
             collide.
    """
    from src.traffic.engine import TrafficEngine, derive_gateway
    engine = TrafficEngine.get()

    run = GroupResult(
        run_id=str(uuid.uuid4())[:8],
        group_id="",
        profile_id=profile.get("id", ""))
    run.status = "running"
    run.started_at = time.time()

    # DSCP: explicit override in profile, else derive from 5QI.
    dscp = profile.get("dscp", -1)
    if dscp is None or int(dscp) < 0:
        from src.db.crud.traffic_profiles import dscp_for_five_qi
        dscp = dscp_for_five_qi(profile.get("five_qi", 9))

    for i, m in enumerate(members):
        src_ip = m.get("src_ip") or ""
        dst_ip = m.get("dst_ip") or derive_gateway(src_ip)
        if not src_ip or not dst_ip:
            log.warning("skipping member %s — cannot resolve src=%r dst=%r",
                        m.get("imsi"), src_ip, dst_ip)
            continue
        session = engine.create_session(
            src_ip=src_ip, dst_ip=dst_ip,
            protocol=profile.get("protocol", "udp"),
            dst_port=base_port + i,
            bandwidth=profile.get("bandwidth") or None,
            duration=int(profile.get("duration", 10)),
            direction=profile.get("direction", "ul"),
            role="orchestrator",
            codec=profile.get("codec") or None,
            tun_device=m.get("tun_device") or None,
            length=(int(profile["length"]) if profile.get("length") else None),
            dnn=profile.get("dnn", ""),
            five_qi=int(profile.get("five_qi") or 0),
            dscp=int(dscp),
            profile_id=profile.get("id", ""),
            imsi=m.get("imsi", ""))
        run.sessions.append(session)

    # Fire all sessions concurrently.
    for s in run.sessions:
        s.start()
    for s in run.sessions:
        s.stop()

    run.status = "completed"
    run.finished_at = time.time()

    # SLA verdict per (5QI, DNN) aggregate.
    violations = []
    for q, agg in run.by_five_qi().items():
        v = _check_sla(profile, agg)
        if v:
            violations.append({"five_qi": q, "violations": v})
    run.sla_violations = violations
    run.sla_met = (len(violations) == 0)

    with _runs_lock:
        _runs[run.run_id] = run
    log.info("profile %s run %s: %d sessions, sla_met=%s",
             profile.get("id"), run.run_id, len(run.sessions), run.sla_met)
    return run


def run_group(group_id: str,
              members_ip_map: Optional[Dict[str, str]] = None,
              base_port: int = 5201) -> GroupResult:
    """Run a named group. Members come from the DB; per-UE IPs come from:

      1. members_ip_map (if provided)  — {imsi: src_ip} from the caller
      2. pdu_session_flows table       — looked up by (imsi, dnn, 5qi)

    If neither resolves a member, it is skipped (logged at debug).
    """
    from src.db.crud.traffic_profiles import group_get, profile_get
    group = group_get(group_id)
    if not group:
        raise ValueError(f"group {group_id!r} not found")
    profile = profile_get(group["profile_id"])
    if not profile:
        raise ValueError(
            f"group {group_id!r} references missing profile {group['profile_id']!r}")

    imsis = group.get("members") or []
    members: List[Dict] = []

    if members_ip_map:
        for imsi in imsis:
            ip = members_ip_map.get(imsi)
            if ip:
                members.append({"imsi": imsi, "src_ip": ip})
    if not members:
        members = resolve_from_flows(imsis, profile.get("dnn", ""),
                                      int(profile.get("five_qi") or 0))

    pattern = group.get("pattern", "concurrent")
    if pattern != "concurrent":
        log.info("group %s pattern=%r not yet implemented — running concurrent",
                 group_id, pattern)

    result = run_profile(profile, members, base_port=base_port)
    result.group_id = group_id
    with _runs_lock:
        _runs[result.run_id] = result   # re-store with group_id set
    return result

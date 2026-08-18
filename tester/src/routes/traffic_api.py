# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/traffic_api.py — Traffic orchestrator API routes (TESTER ONLY).
#
# Mounted by the tester's main app. The standalone agent on the DN host
# mounts traffic_agent_api.py instead — agent and orchestrator are separate
# routers with non-overlapping responsibilities.
#
# Anything that composites or delegates (bidir, voice/video calls, multi-leg
# test flows) lives here. Pure executor endpoints live in traffic_agent_api.py.

import logging

from fastapi import APIRouter, HTTPException, Request

log = logging.getLogger("tester.routes.traffic")

traffic_api_router = APIRouter()


def _engine():
    from src.traffic.engine import TrafficEngine
    return TrafficEngine.get()


# ── Session lifecycle ──

@traffic_api_router.post("/api/traffic/start")
async def api_traffic_start(request: Request):
    """Start a traffic session from the tester.

    Orchestrator mode only (the tester drives both ends via a remote agent).
    role=client|server is rejected here — those are agent-side concerns.
    """
    d = await request.json()
    engine = _engine()

    role = (d.get("role") or "orchestrator").strip().lower()
    if role != "orchestrator":
        raise HTTPException(
            status_code=400,
            detail="tester accepts orchestrator sessions only; "
                   "use the agent's /api/traffic/start for client/server roles")
    direction = d.get("direction", "ul")

    # Accept both "port" (old API) and "dst_port" aliases.
    port = int(d.get("port") or d.get("dst_port") or 5201)

    if direction == "bidir":
        from src.traffic.engine import derive_gateway
        server = d.get("dst_ip") or derive_gateway(d.get("src_ip", ""))
        ul, dl = engine.create_bidir_session(
            ip_a=d["src_ip"], ip_b=server,
            protocol=d.get("protocol", "udp"),
            port=port,
            bandwidth=d.get("bandwidth"),
            duration=int(d.get("duration", 60)))
        ul.start()
        dl.start()
        return {"ok": True, "sessions": [ul.session_id, dl.session_id]}

    session = engine.create_session(
        src_ip=d.get("src_ip", ""),
        dst_ip=d.get("dst_ip", ""),
        protocol=d.get("protocol", "udp"),
        src_port=int(d.get("src_port", 0)),
        dst_port=port,
        bandwidth=d.get("bandwidth"),
        duration=int(d.get("duration", 60)),
        direction=direction,
        role="orchestrator",
        length=(int(d["length"]) if d.get("length") else None),
        dnn=d.get("dnn", ""),
        five_qi=int(d.get("five_qi") or 0),
        dscp=int(d["dscp"]) if d.get("dscp") is not None else -1,
        imsi=d.get("imsi", ""))
    session.start()
    return {"ok": True, "session_id": session.session_id}


@traffic_api_router.get("/api/traffic/sessions/{session_id}")
def api_traffic_session_status(session_id: str):
    """Get a session's current status + stats (tester-local sessions only)."""
    session = _engine().get_session(session_id)
    if not session:
        raise HTTPException(status_code=404, detail=f"session {session_id} not found")
    return {
        "session_id": session.session_id,
        "status": session.status,
        "role": session.role,
        "protocol": session.protocol,
        "direction": session.direction,
        "stats": session.stats.to_dict(),
    }


@traffic_api_router.post("/api/traffic/sessions/{session_id}/stop")
def api_traffic_session_stop(session_id: str):
    """Stop a single tester-local session and return its final stats."""
    session = _engine().get_session(session_id)
    if not session:
        raise HTTPException(status_code=404, detail=f"session {session_id} not found")
    if session.is_running():
        session.cancel()
    stats = session.stop()
    return {"ok": True, "session_id": session.session_id,
            "status": session.status, "stats": stats.to_dict()}


# ── Voice/Video calls (orchestrator composites — tester only) ──

@traffic_api_router.post("/api/traffic/voice-call")
async def api_traffic_voice(request: Request):
    """Start a VoNR voice call."""
    d = await request.json()
    engine = _engine()
    call = engine.create_voice_call(
        ip_a=d["src_ip"], ip_b=d["dst_ip"],
        duration=int(d.get("duration", 60)),
        tun_a=d.get("tun_a"), tun_b=d.get("tun_b"))
    call.start()
    return {"ok": True, "type": "voice_call",
            "sessions": [call.a_to_b.session_id, call.b_to_a.session_id]}


@traffic_api_router.post("/api/traffic/video-call")
async def api_traffic_video(request: Request):
    """Start a ViNR video call."""
    d = await request.json()
    engine = _engine()
    call = engine.create_video_call(
        ip_a=d["src_ip"], ip_b=d["dst_ip"],
        duration=int(d.get("duration", 60)),
        tun_a=d.get("tun_a"), tun_b=d.get("tun_b"))
    call.start()
    return {"ok": True, "type": "video_call",
            "sessions": [call.audio_a_b.session_id, call.audio_b_a.session_id,
                         call.video_a_b.session_id, call.video_b_a.session_id]}


# ── Global ops ──

@traffic_api_router.post("/api/traffic/stop-all")
def api_traffic_stop_all():
    """Stop all active tester-local sessions."""
    return _engine().stop_all()


@traffic_api_router.get("/api/traffic/active")
def api_traffic_active():
    """List active tester-local sessions."""
    return {"items": _engine().get_active()}


# ── Profiles (declarative DNN/5QI QoS classes) ──

@traffic_api_router.get("/api/traffic/profiles")
def api_profiles_list():
    from src.db.crud.traffic_profiles import profile_list
    return {"items": profile_list()}


@traffic_api_router.get("/api/traffic/profiles/{profile_id}")
def api_profiles_get(profile_id: str):
    from src.db.crud.traffic_profiles import profile_get
    p = profile_get(profile_id)
    if not p:
        raise HTTPException(status_code=404, detail=f"profile {profile_id!r} not found")
    return p


@traffic_api_router.post("/api/traffic/profiles")
async def api_profiles_add(request: Request):
    from src.db.crud.traffic_profiles import profile_add
    d = await request.json()
    try:
        return profile_add(d)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@traffic_api_router.put("/api/traffic/profiles/{profile_id}")
async def api_profiles_update(profile_id: str, request: Request):
    from src.db.crud.traffic_profiles import profile_update
    d = await request.json()
    try:
        return profile_update(profile_id, d)
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))


@traffic_api_router.delete("/api/traffic/profiles/{profile_id}")
def api_profiles_delete(profile_id: str):
    from src.db.crud.traffic_profiles import profile_delete
    return {"ok": profile_delete(profile_id)}


# ── Groups (profile × UE cohort with a run pattern) ──

@traffic_api_router.get("/api/traffic/groups")
def api_groups_list():
    from src.db.crud.traffic_profiles import group_list
    return {"items": group_list()}


@traffic_api_router.get("/api/traffic/groups/{group_id}")
def api_groups_get(group_id: str):
    from src.db.crud.traffic_profiles import group_get
    g = group_get(group_id)
    if not g:
        raise HTTPException(status_code=404, detail=f"group {group_id!r} not found")
    return g


@traffic_api_router.post("/api/traffic/groups")
async def api_groups_add(request: Request):
    from src.db.crud.traffic_profiles import group_add, group_set_members
    d = await request.json()
    try:
        group = group_add(d)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))
    members = d.get("members") or []
    if members:
        group_set_members(group["id"], members)
    from src.db.crud.traffic_profiles import group_get
    return group_get(group["id"])


@traffic_api_router.put("/api/traffic/groups/{group_id}")
async def api_groups_update(group_id: str, request: Request):
    from src.db.crud.traffic_profiles import group_update, group_set_members, group_get
    d = await request.json()
    try:
        group_update(group_id, d)
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    if "members" in d:
        group_set_members(group_id, d["members"] or [])
    return group_get(group_id)


@traffic_api_router.delete("/api/traffic/groups/{group_id}")
def api_groups_delete(group_id: str):
    from src.db.crud.traffic_profiles import group_delete
    return {"ok": group_delete(group_id)}


@traffic_api_router.post("/api/traffic/groups/{group_id}/members")
async def api_groups_set_members(group_id: str, request: Request):
    """Replace the full membership list for a group."""
    from src.db.crud.traffic_profiles import group_set_members, group_get
    d = await request.json()
    imsis = d.get("members") or d.get("imsis") or []
    if not group_get(group_id):
        raise HTTPException(status_code=404, detail=f"group {group_id!r} not found")
    group_set_members(group_id, imsis)
    return group_get(group_id)


# ── Group runs (profile × members) ──

@traffic_api_router.post("/api/traffic/groups/{group_id}/run")
async def api_groups_run(group_id: str, request: Request):
    """Execute a group. Returns run_id; poll /runs/{run_id} for stats."""
    import threading
    from src.traffic.groups import run_group, GroupResult, _runs, _runs_lock
    import uuid

    body = {}
    try:
        body = await request.json()
    except Exception:
        pass
    members_ip_map = body.get("members_ip_map") or body.get("ips") or None

    # Pre-register a placeholder so the UI can see the run immediately.
    run_id = str(uuid.uuid4())[:8]
    placeholder = GroupResult(run_id=run_id, group_id=group_id)
    placeholder.status = "running"
    import time as _t
    placeholder.started_at = _t.time()
    with _runs_lock:
        _runs[run_id] = placeholder

    def _go():
        try:
            actual = run_group(group_id, members_ip_map=members_ip_map)
            # Stitch the placeholder's run_id so pollers see the final result.
            actual.run_id = run_id
            with _runs_lock:
                _runs[run_id] = actual
        except Exception as e:
            log.exception("group %s run failed", group_id)
            placeholder.status = "error"
            placeholder.sla_violations = [{"error": str(e)}]
            placeholder.finished_at = _t.time()

    threading.Thread(target=_go, daemon=True,
                      name=f"group-run-{run_id}").start()
    return {"ok": True, "run_id": run_id, "group_id": group_id}


@traffic_api_router.get("/api/traffic/runs")
def api_runs_list():
    from src.traffic.groups import list_runs
    return {"items": list_runs()}


@traffic_api_router.get("/api/traffic/runs/{run_id}")
def api_runs_get(run_id: str):
    from src.traffic.groups import get_run
    r = get_run(run_id)
    if not r:
        raise HTTPException(status_code=404, detail=f"run {run_id!r} not found")
    return r.to_dict()


# ── PDU session flow snapshot (for debugging / orchestrator member lookup) ──

@traffic_api_router.get("/api/traffic/flows")
def api_flows_list(imsi: str = None, dnn: str = None, five_qi: int = None):
    from src.db.crud.traffic_profiles import flow_list_for_ue, flow_list_by_dnn
    if imsi:
        return {"items": flow_list_for_ue(imsi)}
    if dnn:
        return {"items": flow_list_by_dnn(dnn, five_qi)}
    raise HTTPException(status_code=400, detail="pass imsi= or dnn=")


@traffic_api_router.post("/api/traffic/flows")
async def api_flows_upsert(request: Request):
    """Upsert a (imsi, dnn, 5qi) → (tun, ue_ip, dn_ip) flow record."""
    from src.db.crud.traffic_profiles import flow_upsert
    d = await request.json()
    row = flow_upsert(
        imsi=d["imsi"], dnn=d["dnn"], five_qi=int(d["five_qi"]),
        qfi=int(d.get("qfi", 0)),
        tun_device=d.get("tun_device", ""),
        ue_ip=d.get("ue_ip", ""), dn_ip=d.get("dn_ip", ""))
    return row

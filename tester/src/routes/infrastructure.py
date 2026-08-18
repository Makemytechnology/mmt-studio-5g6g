# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/infrastructure.py — Infrastructure management routes
#
# FastAPI Router: infrastructure_router

from fastapi import APIRouter, HTTPException, Request, Query
from src.routes.common import crud, log

infrastructure_router = APIRouter()


@infrastructure_router.get("/api/infra/config")
def api_infra_get():
    return crud.infra_get()


@infrastructure_router.put("/api/infra/config")
async def api_infra_update(request: Request):
    d = await request.json()
    return crud.infra_update(d)


@infrastructure_router.get("/api/infra/interfaces")
def api_infra_interfaces():
    return {"items": crud.get_interfaces()}


@infrastructure_router.get("/api/infra/tunnels")
def api_infra_tunnels():
    return {"items": crud.get_active_tunnels()}


# ── AMF Targets ──

@infrastructure_router.get("/api/infra/amf-targets")
def api_amf_list():
    return {"items": crud.amf_list()}


@infrastructure_router.post("/api/infra/amf-targets")
async def api_amf_add(request: Request):
    d = await request.json()
    try:
        result = crud.amf_add(d["name"], d["ip"], d.get("port", 38412),
                              d.get("role", "active"), d.get("weight", 100))
        return result
    except (ValueError, KeyError) as e:
        raise HTTPException(status_code=400, detail=str(e))


@infrastructure_router.put("/api/infra/amf-targets/{name}")
async def api_amf_update(name: str, request: Request):
    d = await request.json()
    try:
        return crud.amf_update(name, d)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@infrastructure_router.delete("/api/infra/amf-targets/{name}")
def api_amf_delete(name: str):
    return {"ok": crud.amf_delete(name)}


# ── SCTP Multi-Home ──

@infrastructure_router.get("/api/infra/sctp-addresses")
def api_sctp_list(gnb_name: str = Query(default=None)):
    return {"items": crud.sctp_addr_list(gnb_name)}


@infrastructure_router.post("/api/infra/sctp-addresses")
async def api_sctp_add(request: Request):
    d = await request.json()
    return crud.sctp_addr_add(d["gnb_name"], d["ip"], d.get("is_primary", False))


@infrastructure_router.delete("/api/infra/sctp-addresses")
async def api_sctp_delete(request: Request):
    d = await request.json()
    return {"ok": crud.sctp_addr_delete(d["gnb_name"], d["ip"])}


# ── AMF Pool Assignments ──

@infrastructure_router.get("/api/infra/amf-assignments")
def api_assignments_list():
    return {"items": crud.amf_assignment_list()}


@infrastructure_router.post("/api/infra/amf-assignments")
async def api_assignment_set(request: Request):
    d = await request.json()
    return crud.amf_assignment_set(d["amf_name"], d["gnb_name"])


@infrastructure_router.delete("/api/infra/amf-assignments")
async def api_assignment_delete(request: Request):
    d = await request.json()
    return {"ok": crud.amf_assignment_delete(d["amf_name"], d["gnb_name"])}


# ── Traffic Agents (DN/APN-side tester engines) ──

@infrastructure_router.get("/api/infra/traffic-agents")
def api_agent_list():
    return {"items": crud.agent_list()}


@infrastructure_router.post("/api/infra/traffic-agents")
async def api_agent_add(request: Request):
    d = await request.json()
    try:
        row = crud.agent_add(
            agent_id=d["id"], url=d["url"],
            dnn=d.get("dnn", ""), dn_ip=d.get("dn_ip", ""),
            token=d.get("token", ""),
            is_default=bool(d.get("is_default", False)),
            notes=d.get("notes", ""))
        return row
    except ValueError as e:
        from fastapi import HTTPException
        raise HTTPException(status_code=400, detail=str(e))


@infrastructure_router.put("/api/infra/traffic-agents/{agent_id}")
async def api_agent_update(agent_id: str, request: Request):
    d = await request.json()
    try:
        return crud.agent_update(agent_id, d)
    except ValueError as e:
        from fastapi import HTTPException
        raise HTTPException(status_code=404, detail=str(e))


@infrastructure_router.delete("/api/infra/traffic-agents/{agent_id}")
def api_agent_delete(agent_id: str):
    return {"ok": crud.agent_delete(agent_id)}


@infrastructure_router.get("/api/infra/traffic-agents/{agent_id}/health")
def api_agent_health(agent_id: str):
    """Check a registered agent's live health via its /api/traffic/healthz."""
    row = crud.agent_get(agent_id)
    if not row:
        from fastapi import HTTPException
        raise HTTPException(status_code=404, detail=f"agent {agent_id!r} not found")
    from src.traffic.remote import TrafficAgent
    agent = TrafficAgent.from_row(row)
    ok = agent.healthz()
    caps = agent.capabilities() if ok else {}
    return {"ok": ok, "url": row["url"], "capabilities": caps}

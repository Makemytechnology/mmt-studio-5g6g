# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/provisioning.py — UE and gNB configuration routes
#
# FastAPI Router: provisioning_router
# Mirrors sa_core's webservice/routes/provisioning.py pattern.

from fastapi import APIRouter, HTTPException, Request
from src.routes.common import crud, log, _mask_imsi

provisioning_router = APIRouter()


# ── UE Config (sim_db) ──

@provisioning_router.get("/api/sim-db")
def api_sim_list():
    from src.protocol.sim_db import sim_db_list
    return {"items": sim_db_list()}


@provisioning_router.get("/api/sim-db/{imsi}")
def api_sim_get(imsi: str):
    from src.protocol.sim_db import sim_db_get
    entry = sim_db_get(imsi)
    if not entry:
        raise HTTPException(status_code=404, detail="not found")
    return entry


@provisioning_router.post("/api/sim-db")
async def api_sim_add(request: Request):
    from src.protocol.sim_db import sim_db_add
    d = await request.json()
    try:
        entry = sim_db_add(d)
        return entry
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@provisioning_router.put("/api/sim-db/{imsi}")
async def api_sim_update(imsi: str, request: Request):
    from src.protocol.sim_db import sim_db_update
    d = await request.json()
    try:
        entry = sim_db_update(imsi, d)
        return entry
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@provisioning_router.delete("/api/sim-db/{imsi}")
def api_sim_delete(imsi: str):
    from src.protocol.sim_db import sim_db_delete
    ok = sim_db_delete(imsi)
    return {"ok": ok}


@provisioning_router.post("/api/sim-db/import")
async def api_sim_import(request: Request):
    from src.protocol.sim_db import sim_db_import
    d = await request.json()
    items = d.get("items", [])
    overwrite = d.get("overwrite", False)
    count = sim_db_import(items, overwrite)
    return {"ok": True, "imported": count}


# ── gNB Config ──

@provisioning_router.get("/api/gnb-config")
def api_gnb_list():
    from src.protocol.gnb_config import gnb_cfg_list
    from src.config import GNB_PROFILES_PATH
    return {"items": gnb_cfg_list(GNB_PROFILES_PATH)}


@provisioning_router.get("/api/gnb-config/{name}")
def api_gnb_get(name: str):
    from src.protocol.gnb_config import gnb_cfg_get
    from src.config import GNB_PROFILES_PATH
    entry = gnb_cfg_get(GNB_PROFILES_PATH, name)
    if not entry:
        raise HTTPException(status_code=404, detail="not found")
    return entry


@provisioning_router.post("/api/gnb-config")
async def api_gnb_add(request: Request):
    from src.protocol.gnb_config import gnb_cfg_add
    from src.config import GNB_PROFILES_PATH
    d = await request.json()
    try:
        entry = gnb_cfg_add(GNB_PROFILES_PATH, d)
        return entry
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@provisioning_router.put("/api/gnb-config/{name}")
async def api_gnb_update(name: str, request: Request):
    from src.protocol.gnb_config import gnb_cfg_update
    from src.config import GNB_PROFILES_PATH
    d = await request.json()
    try:
        entry = gnb_cfg_update(GNB_PROFILES_PATH, name, d)
        return entry
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@provisioning_router.delete("/api/gnb-config/{name}")
def api_gnb_delete(name: str):
    from src.protocol.gnb_config import gnb_cfg_delete
    from src.config import GNB_PROFILES_PATH
    ok = gnb_cfg_delete(GNB_PROFILES_PATH, name)
    return {"ok": ok}

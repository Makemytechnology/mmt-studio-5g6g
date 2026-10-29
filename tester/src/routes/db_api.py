# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/db_api.py — Database API routes
#
# FastAPI Router: db_api_router

from fastapi import APIRouter, HTTPException, Query
from src.routes.common import crud, log

db_api_router = APIRouter()


@db_api_router.get("/api/db/stats")
def api_db_stats():
    from src.db.runs import list_runs
    return {
        "ue_count": crud.ue_count(),
        "gnb_count": len(crud.gnb_list()),
        "test_results": crud.result_stats(),
        "pending_sync": len(crud.sync_pending()),
        "recent_runs": len(list_runs(limit=100)),
    }


@db_api_router.get("/api/db/ue")
def api_db_ue_list(gnb_name: str = Query(default=None)):
    return {"items": crud.ue_list(gnb_name)}


@db_api_router.get("/api/db/ue/{imsi}")
def api_db_ue_get(imsi: str):
    ue = crud.ue_get(imsi)
    if not ue:
        raise HTTPException(status_code=404, detail="not found")
    return ue


@db_api_router.get("/api/db/results")
def api_db_results(limit: int = Query(default=50), status: str = Query(default=None), name: str = Query(default=None)):
    return {"items": crud.result_list(limit, status, name)}


@db_api_router.get("/api/db/results/{rid}")
def api_db_result_get(rid: int):
    r = crud.result_get(rid)
    if not r:
        raise HTTPException(status_code=404, detail="not found")
    return r


@db_api_router.post("/api/db/migrate")
def api_db_migrate():
    from src.config import GNB_PROFILES_PATH
    crud.migrate_from_json(None, GNB_PROFILES_PATH)
    return {"ok": True, "ue_count": crud.ue_count(), "gnb_count": len(crud.gnb_list())}

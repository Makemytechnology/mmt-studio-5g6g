# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/test_execution.py — Test execution routes
#
# FastAPI Router: test_execution_router

import threading
from fastapi import APIRouter, HTTPException, Request
from src.routes.common import log

test_execution_router = APIRouter()

_running_tests = {}


@test_execution_router.post("/api/tests/{name}/run")
async def api_run_test(name: str, request: Request):
    """Run a single test."""
    log.info("Test requested: %s", name)
    t = _running_tests.get(name)
    if t and t.is_alive():
        raise HTTPException(status_code=409, detail="Test already running")

    from src.app import runner, gnb_pool, ue_pool
    params = await request.json() if await request.body() else {}

    def _run():
        runner.run_test(name, gnb_pool, ue_pool, params)
        log.info("Test done: %s -> %s", name, runner.results[-1].status if runner.results else "?")

    t = threading.Thread(target=_run, daemon=True, name=f"test-{name}")
    _running_tests[name] = t
    t.start()
    return {"ok": True, "test": name, "status": "started"}


@test_execution_router.get("/api/tests")
def api_test_catalog():
    """List all registered test cases."""
    from src.app import runner
    catalog = []
    for name, cls in runner._registry.items():
        catalog.append({
            "name": name,
            "tc_id": getattr(cls, "tc_id", ""),
            "category": getattr(cls, "category", ""),
            "description": getattr(cls, "description", ""),
        })
    return {"items": catalog, "total": len(catalog)}

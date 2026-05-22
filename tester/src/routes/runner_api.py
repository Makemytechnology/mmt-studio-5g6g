# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/runner_api.py — runner-wide configuration API.
#
# Currently exposes pretest_mode (full | baseline | delta) which the
# test runner consults before each test case to decide whether to
# reset the SUT to baseline first. See src/testcases/runner_config.py
# for the on-disk shape (config/runner.json) and env-var override
# (TESTER_PRETEST_MODE).

from fastapi import APIRouter, HTTPException, Request

from src.testcases import runner_config

runner_api_router = APIRouter()


@runner_api_router.get("/api/runner/config")
def api_runner_config_get():
    cfg = runner_config.load()
    return {
        "config": cfg,
        "valid_pretest_modes": list(runner_config.PRETEST_MODES),
    }


@runner_api_router.put("/api/runner/config")
async def api_runner_config_put(request: Request):
    body = await request.json()
    try:
        merged = runner_config.save(body)
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))
    return {"ok": True, "config": merged}

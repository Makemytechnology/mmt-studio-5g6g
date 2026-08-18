# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/analysis.py — Test analysis routes
#
# FastAPI Router: analysis_router

from fastapi import APIRouter, HTTPException, Query
from src.routes.common import analysis

analysis_router = APIRouter()


@analysis_router.get("/api/analysis/pass-rate")
def api_pass_rate(last_n: int = Query(default=10)):
    return {"items": analysis.pass_rate(last_n)}


@analysis_router.get("/api/analysis/flaky")
def api_flaky():
    return {"items": analysis.flaky_tests()}


@analysis_router.get("/api/analysis/failures")
def api_failures():
    return {"items": analysis.failure_heatmap()}


@analysis_router.get("/api/analysis/suites")
def api_suites(run_id: str = Query(default=None)):
    return {"items": analysis.suite_summary(run_id=run_id)}


@analysis_router.get("/api/analysis/regressions/{run_id}")
def api_regressions(run_id: str):
    return {"items": analysis.regressions(run_id)}


@analysis_router.get("/api/analysis/compare")
def api_compare(a: str = Query(default=None), b: str = Query(default=None)):
    if not a or not b:
        raise HTTPException(status_code=400, detail="provide ?a=run_id&b=run_id")
    return analysis.compare_runs(a, b)


@analysis_router.get("/api/analysis/metric-trend")
def api_metric_trend(test: str = Query(default=None), metric: str = Query(default=None)):
    if not test or not metric:
        raise HTTPException(status_code=400, detail="provide ?test=name&metric=name")
    return {"items": analysis.metric_trend(test, metric)}

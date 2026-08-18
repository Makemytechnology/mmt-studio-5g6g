# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/reports.py — Test runs and report generation routes
#
# FastAPI Router: reports_router

from fastapi import APIRouter, HTTPException, Query
from fastapi.responses import FileResponse
from src.routes.common import runs, report_gen, log

reports_router = APIRouter()


@reports_router.get("/api/runs")
def api_runs_list(limit: int = Query(default=20)):
    return {"items": runs.list_runs(limit)}


@reports_router.get("/api/runs/{run_id}")
def api_run_get(run_id: str):
    run = runs.get_run(run_id)
    if not run:
        raise HTTPException(status_code=404, detail="not found")
    return run


@reports_router.get("/api/runs/{run_id}/report/{fmt}")
def api_run_report(run_id: str, fmt: str):
    if fmt == "html":
        path = report_gen.generate_html_report(run_id)
    elif fmt == "json":
        path = report_gen.generate_json_report(run_id)
    elif fmt in ("junit", "xml"):
        path = report_gen.generate_junit_xml(run_id)
    else:
        raise HTTPException(status_code=400, detail=f"unknown format: {fmt}")
    if path:
        mtype = {"html": "text/html", "json": "application/json", "junit": "application/xml", "xml": "application/xml"}
        return FileResponse(path, media_type=mtype.get(fmt, "application/octet-stream"))
    raise HTTPException(status_code=500, detail="generation failed")


@reports_router.get("/api/reports")
def api_reports_list():
    return {"items": report_gen.list_reports()}

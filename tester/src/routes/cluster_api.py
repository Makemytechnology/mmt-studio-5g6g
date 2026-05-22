# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/cluster_api.py — Controller + Worker API routes
#
# FastAPI Router: cluster_api_router
#
# Controller endpoints (when role=controller):
#   POST /api/controller/register    — worker registers
#   POST /api/controller/heartbeat   — worker heartbeat
#   POST /api/controller/result      — worker reports results
#   GET  /api/controller/workers     — list workers
#   POST /api/controller/start-run   — start distributed run
#   GET  /api/controller/runs/<id>   — get run status
#
# Worker endpoints (when role=worker):
#   POST /api/worker/start           — controller tells worker to start
#   POST /api/worker/stop            — controller tells worker to stop
#   GET  /api/worker/status          — worker status
#
# Cluster config endpoints (any role):
#   GET  /api/cluster/config         — get cluster config
#   PUT  /api/cluster/config         — update cluster config

from fastapi import APIRouter, HTTPException, Request
from src.cluster.config import load_config, save_config

cluster_api_router = APIRouter()

# Lazy-initialized controller/worker instances
_controller = None
_worker = None


def _get_controller():
    global _controller
    if _controller is None:
        from src.cluster.controller import Controller
        _controller = Controller()
    return _controller


def _get_worker():
    global _worker
    if _worker is None:
        cfg = load_config()
        from src.cluster.worker import Worker
        _worker = Worker(
            node_id=cfg.get("node_id", "tester-01"),
            controller_url=cfg.get("controller_url", ""),
            gnb_count=cfg.get("worker", {}).get("gnb_count", 10000),
            ues_active=cfg.get("worker", {}).get("ues_per_gnb_active", 1000),
            ues_idle=cfg.get("worker", {}).get("ues_per_gnb_idle", 10000),
        )
    return _worker


# ── Cluster Config ──

@cluster_api_router.get("/api/cluster/config")
def api_cluster_config_get():
    return load_config()


@cluster_api_router.put("/api/cluster/config")
async def api_cluster_config_update(request: Request):
    d = await request.json()
    cfg = load_config()
    cfg.update(d)
    save_config(cfg)
    return {"ok": True}


# ── Controller Endpoints ──

@cluster_api_router.post("/api/controller/register")
async def api_controller_register(request: Request):
    d = await request.json()
    ctrl = _get_controller()
    result = ctrl.register_worker(
        d["node_id"], d["ip"], d.get("port", 5001), d.get("gnb_count", 10000))
    return result


@cluster_api_router.post("/api/controller/heartbeat")
async def api_controller_heartbeat(request: Request):
    d = await request.json()
    ctrl = _get_controller()
    return ctrl.worker_heartbeat(d["node_id"], d.get("metrics"))


@cluster_api_router.post("/api/controller/result")
async def api_controller_result(request: Request):
    d = await request.json()
    ctrl = _get_controller()
    return ctrl.worker_report_result(d["node_id"], d["run_id"], d["results"])


@cluster_api_router.get("/api/controller/workers")
def api_controller_workers():
    ctrl = _get_controller()
    return {"items": ctrl.get_workers()}


@cluster_api_router.post("/api/controller/start-run")
async def api_controller_start_run(request: Request):
    d = await request.json()
    ctrl = _get_controller()
    result = ctrl.start_run(
        run_type=d.get("run_type", "regression"),
        test_names=d.get("test_names"),
        params=d.get("params"),
    )
    return result


@cluster_api_router.get("/api/controller/runs/{run_id}")
def api_controller_run_get(run_id: str):
    ctrl = _get_controller()
    run = ctrl.get_run(run_id)
    if not run:
        raise HTTPException(status_code=404, detail="not found")
    return run


# ── Worker Endpoints ──

@cluster_api_router.post("/api/worker/start")
async def api_worker_start(request: Request):
    d = await request.json()
    worker = _get_worker()
    return worker.handle_start(
        d["run_id"], d.get("run_type", "regression"),
        d.get("test_names"), d.get("params"))


@cluster_api_router.post("/api/worker/stop")
async def api_worker_stop(request: Request):
    d = await request.json()
    worker = _get_worker()
    return worker.handle_stop(d.get("run_id", ""))


@cluster_api_router.get("/api/worker/status")
def api_worker_status():
    worker = _get_worker()
    return {
        "node_id": worker.node_id,
        "status": worker.status,
        "registered": worker.registered,
        "gnb_start": worker.gnb_start,
        "gnb_count": worker.gnb_count,
        "current_run": worker.current_run,
        "metrics": worker.metrics,
    }

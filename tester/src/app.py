#!/usr/bin/env python3
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""SA Tester web UI and REST API — FastAPI + Uvicorn."""

import os, sys, json, time, logging, threading

THIS_DIR = os.path.dirname(os.path.abspath(__file__))
PROJECT_ROOT = os.path.abspath(os.path.join(THIS_DIR, ".."))

# Add project root, libs, and libs/pycrate to path
for p in (PROJECT_ROOT, os.path.join(PROJECT_ROOT, "libs"),
          os.path.join(PROJECT_ROOT, "libs", "pycrate")):
    if p not in sys.path:
        sys.path.insert(0, p)

# ── Centralized logging (must be before any other imports that use get_logger) ──
from src.tester_logger import (
    setup_logging, get_logger, load_levels,
    RingBufferHandler, get_all_loggers, set_level, set_all_levels, save_levels,
)
setup_logging(level="DEBUG")
load_levels()
log = get_logger("app")

from src.startup_banner import log_banner
log_banner()

from fastapi import FastAPI, Request, HTTPException
from fastapi.responses import JSONResponse, StreamingResponse, FileResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates

from src.config import AMF_IP, AMF_PORT, TESTER_WEB_PORT, SIM_DB_PATH, GNB_PROFILES_PATH
from src.statemachine import GnbStateMachine, UeStateMachine
from src.protocol.sim_db import (
    load_all_sims, load_sims_auto,
    sim_db_list, sim_db_get, sim_db_add, sim_db_update,
    sim_db_delete, sim_db_clone, sim_db_import,
    sim_db_clone_range, sim_db_delete_range,
)
from src.protocol.gnb_config import (
    gnb_cfg_list, gnb_cfg_get, gnb_cfg_add, gnb_cfg_update,
    gnb_cfg_delete, gnb_cfg_clone, gnb_cfg_import,
)
from src.testcases.test_runner import TestRunner
from src.testcases.registry import discover_all
from src.testcases.traffic.tc_sequence import (
    SequenceTestCase, load_sequences, upsert_sequence,
    delete_sequence, get_sequence,
)
from src.ai_engine.ollama_client import OllamaClient, OllamaConfig
from src.ai_engine.rag_engine import RAGEngine
from src.ai_engine.pcap_analyzer import PcapAnalyzer
from src.protocol.gtpu import GtpuManager
from src.db.schema import ensure_schema
from src.db import crud as db

# ── Initialize DB ──
ensure_schema()
# Auto-migrate JSON → SQLite on first run (if DB is empty)
if db.ue_count() == 0:
    db.migrate_from_json(sim_json_path=None, gnb_json_path=GNB_PROFILES_PATH)
    log.info("DB initialized: %d UEs, %d gNBs", db.ue_count(), len(db.gnb_list()))
# One-shot: promote legacy infra_config.traffic_engine_url into traffic_agents.
db.migrate_legacy_traffic_url()
# Seed default core-side slave row if registry is still empty (dockerized
# layout default: http://172.30.0.10:9100, env-overridable).
db.seed_default_traffic_agent()
# Seed 3GPP-inspired traffic profiles (voice/video/IMS-sig/best-effort/gaming/...).
db.seed_default_profiles()

# ── Global state ──
gnb_pool = []
ue_pool = []
runner = TestRunner()
gtpu_manager = GtpuManager()

# ── AI Engine ──
_ai_config = OllamaConfig()
_ai_client = OllamaClient(_ai_config)
_ai_rag = RAGEngine(_ai_client, store_path=os.path.join(PROJECT_ROOT, "data", "rag_store.json"))
_ai_pcap = PcapAnalyzer(_ai_client)
_ai_chat_history = []  # conversation history for chat endpoint

# Auto-discover and register all test cases from testcases/ subdirectories
for tc in discover_all():
    runner.register(tc)

# ── Load robot suites as the test catalog ──
_ROBOT_DIR = os.path.join(PROJECT_ROOT, "robot", "suites")
runner.load_robot_suites(_ROBOT_DIR)

# ── FastAPI App ──
_SRC_DIR = os.path.dirname(os.path.abspath(__file__))
app = FastAPI(title="SA Tester")
templates = Jinja2Templates(directory=os.path.join(_SRC_DIR, "templates"))
app.mount("/static", StaticFiles(directory=os.path.join(_SRC_DIR, "static")), name="static")

# ── Register route routers (mirrors sa_core pattern) ──
from src.routes import register_routers
register_routers(app)


@app.get("/")
async def index(request: Request):
    return templates.TemplateResponse(request, "tester_index.html")


# ── Network Interfaces API ──

@app.get("/api/network-interfaces")
async def api_network_interfaces():
    """List network interfaces with their IPv4 addresses."""
    import subprocess
    interfaces = []
    try:
        result = subprocess.run(['ip', '-4', '-o', 'addr', 'show'], capture_output=True, text=True)
        for line in result.stdout.strip().split('\n'):
            if not line.strip():
                continue
            parts = line.split()
            iface = parts[1]
            ip = parts[3].split('/')[0]
            interfaces.append({"interface": iface, "ip": ip})
    except Exception as e:
        log.warning("Failed to list network interfaces: %s", e)
    return {"items": interfaces}


# ── GTP-U API ──

@app.get("/api/gtpu/tunnels")
async def api_gtpu_tunnels():
    return {"tunnels": gtpu_manager.get_tunnels(), "available": gtpu_manager.available}


# ── gNB API ──

@app.get("/api/gnbs")
async def api_gnbs():
    return {"items": [g.to_dict() for g in gnb_pool]}

@app.post("/api/gnbs")
async def api_add_gnb(request: Request):
    data = await request.json() if await request.body() else {}
    gnb = GnbStateMachine(
        amf_ip=data.get("amf_ip", AMF_IP), amf_port=data.get("amf_port", AMF_PORT),
        gnb_name=data.get("gnb_name"), mcc=data.get("mcc"), mnc=data.get("mnc"), tac=data.get("tac"),
        gtpu_manager=gtpu_manager)
    gnb_pool.append(gnb)
    return {"ok": True, "gnb": gnb.to_dict()}

@app.post("/api/gnbs/{idx}/connect")
async def api_gnb_connect(idx: int):
    if idx >= len(gnb_pool):
        raise HTTPException(status_code=404, detail="Invalid index")
    ok = gnb_pool[idx].connect()
    return {"ok": ok, "state": gnb_pool[idx].state}

@app.post("/api/gnbs/{idx}/disconnect")
async def api_gnb_disconnect(idx: int):
    if idx >= len(gnb_pool):
        raise HTTPException(status_code=404, detail="Invalid index")
    gnb_pool[idx].disconnect()
    return {"ok": True}

@app.post("/api/gnbs/{idx}/remove")
async def api_gnb_remove(idx: int):
    if idx >= len(gnb_pool):
        raise HTTPException(status_code=404, detail="Invalid index")
    gnb_pool.pop(idx).disconnect()
    return {"ok": True}


# ── UE API ──

@app.get("/api/ues")
async def api_ues():
    return {"items": [u.to_dict() for u in ue_pool]}

@app.post("/api/ues/load-sims")
async def api_load_sims():
    sims = load_sims_auto(SIM_DB_PATH)
    created = 0
    for s in sims:
        if not any(u.imsi == s.imsi for u in ue_pool):
            ue_pool.append(UeStateMachine(s))
            created += 1
    return {"ok": True, "loaded": created, "total": len(ue_pool)}

@app.post("/api/ues/{imsi}/register")
async def api_ue_register(imsi: str):
    ue = next((u for u in ue_pool if u.imsi == imsi), None)
    if not ue:
        raise HTTPException(status_code=404, detail="UE not found")
    if not gnb_pool:
        raise HTTPException(status_code=400, detail="No gNBs")
    gnb = gnb_pool[0]
    if gnb.state != "READY":
        raise HTTPException(status_code=400, detail=f"gNB not ready ({gnb.state})")
    gnb.attach_ue(ue)
    ue.register()
    return {"ok": True, "state": ue.state}

@app.post("/api/ues/{imsi}/deregister")
async def api_ue_deregister(imsi: str):
    ue = next((u for u in ue_pool if u.imsi == imsi), None)
    if not ue:
        raise HTTPException(status_code=404, detail="UE not found")
    ue.deregister()
    return {"ok": True, "state": ue.state}

@app.post("/api/ues/{imsi}/pdu-session")
async def api_ue_pdu_session(imsi: str, request: Request):
    ue = next((u for u in ue_pool if u.imsi == imsi), None)
    if not ue:
        raise HTTPException(status_code=404, detail="UE not found")
    data = await request.json() if await request.body() else {}
    ok = ue.establish_pdu_session(dnn=data.get("dnn", "internet"),
                                   sst=data.get("sst", 1), sd=data.get("sd"),
                                   pdu_session_id=data.get("psi", 1))
    return {"ok": ok}


# ── SIM DB CRUD API ──

@app.get("/api/sim-db")
async def api_sim_db():
    """List all SIM entries from JSON file."""
    return {"items": sim_db_list()}


@app.get("/api/sim-db/{imsi}")
async def api_sim_db_get(imsi: str):
    """Get a single SIM entry."""
    entry = sim_db_get(imsi)
    if entry is None:
        raise HTTPException(status_code=404, detail=f"IMSI {imsi} not found")
    return entry


@app.post("/api/sim-db")
async def api_sim_db_add(request: Request):
    """Add a new SIM entry. Body: {imsi, k, opc, op_type?, sqn?, amf?}"""
    data = await request.json() if await request.body() else {}
    if not data.get("imsi") or not data.get("k") or not data.get("opc"):
        raise HTTPException(status_code=400, detail="imsi, k, and opc are required")
    try:
        entry = sim_db_add(data)
        log.info("SIM added: %s", data["imsi"])
        return {"ok": True, "entry": entry}
    except ValueError as e:
        raise HTTPException(status_code=409, detail=str(e))


@app.put("/api/sim-db/{imsi}")
async def api_sim_db_update(imsi: str, request: Request):
    """Update an existing SIM entry. Body: {k?, opc?, op_type?, sqn?, amf?}"""
    data = await request.json() if await request.body() else {}
    try:
        entry = sim_db_update(imsi, data)
        log.info("SIM updated: %s", imsi)
        return {"ok": True, "entry": entry}
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))


@app.delete("/api/sim-db/{imsi}")
async def api_sim_db_delete(imsi: str):
    """Delete a SIM entry."""
    global ue_pool
    ok = sim_db_delete(imsi)
    if ok:
        # Also remove from ue_pool if loaded
        ue_pool = [u for u in ue_pool if u.imsi != imsi]
        log.info("SIM deleted: %s", imsi)
    return {"ok": ok}


@app.post("/api/sim-db/clone")
async def api_sim_db_clone(request: Request):
    """Clone a SIM. Body: {source_imsi, new_imsi}"""
    data = await request.json() if await request.body() else {}
    src = data.get("source_imsi", "")
    new = data.get("new_imsi", "")
    if not src or not new:
        raise HTTPException(status_code=400, detail="source_imsi and new_imsi required")
    try:
        entry = sim_db_clone(src, new)
        log.info("SIM cloned: %s -> %s", src, new)
        return {"ok": True, "entry": entry}
    except ValueError as e:
        raise HTTPException(status_code=409, detail=str(e))


@app.post("/api/sim-db/clone/range")
async def api_sim_db_clone_range(request: Request):
    """Bulk clone N SIMs from source_imsi, starting at start_imsi and
    walking +1 each step. Mirrors mmt-studio-core-go's
    /api/ue/clone/range so the tester and core SMF speak the same
    bulk-provisioning shape.

    Body: {source_imsi, start_imsi, count}
    """
    data = await request.json() if await request.body() else {}
    src = data.get("source_imsi", "")
    start = data.get("start_imsi", "")
    count = int(data.get("count") or 0)
    try:
        n = sim_db_clone_range(src, start, count)
        log.info("SIM bulk clone: %s -> %s x%d (created=%d)", src, start, count, n)
        return {"ok": True, "created": n}
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.post("/api/sim-db/delete/range")
async def api_sim_db_delete_range(request: Request):
    """Bulk delete N sequential SIMs starting at start_imsi. Mirrors
    mmt-studio-core-go's /api/ue/delete/range. Missing IMSIs in the
    range are silently skipped — re-running is idempotent.

    Body: {start_imsi, count}
    """
    global ue_pool
    data = await request.json() if await request.body() else {}
    start = data.get("start_imsi", "")
    count = int(data.get("count") or 0)
    try:
        n = sim_db_delete_range(start, count)
        # Drop deleted IMSIs from the in-memory ue_pool so subsequent
        # tests don't try to attach a removed SIM.
        if n > 0:
            removed_set = set()
            cur = start
            for _ in range(count):
                removed_set.add(cur)
                # mirror _inc_dec_str: numeric increment with width preserved
                arr = list(cur)
                for i in range(len(arr) - 1, -1, -1):
                    if arr[i] < '9':
                        arr[i] = chr(ord(arr[i]) + 1)
                        break
                    arr[i] = '0'
                cur = ''.join(arr)
            ue_pool = [u for u in ue_pool if u.imsi not in removed_set]
        log.info("SIM bulk delete: %s x%d (deleted=%d)", start, count, n)
        return {"ok": True, "deleted": n}
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))


@app.post("/api/sim-db/import")
async def api_sim_db_import(request: Request):
    """Import SIM entries. Body: {items: [...], overwrite?: bool}"""
    data = await request.json() if await request.body() else {}
    items = data.get("items", [])
    overwrite = data.get("overwrite", False)
    if not items:
        raise HTTPException(status_code=400, detail="items list required")
    count = sim_db_import(items, overwrite=overwrite)
    log.info("SIM import: %d entries", count)
    return {"ok": True, "imported": count}


# ── gNB Config Profiles API ──

@app.get("/api/gnb-config")
async def api_gnb_cfg():
    return {"items": gnb_cfg_list(GNB_PROFILES_PATH)}

@app.get("/api/gnb-config/{name}")
async def api_gnb_cfg_get(name: str):
    entry = gnb_cfg_get(GNB_PROFILES_PATH, name)
    if entry is None:
        raise HTTPException(status_code=404, detail=f"Profile '{name}' not found")
    return entry

@app.post("/api/gnb-config")
async def api_gnb_cfg_add(request: Request):
    data = await request.json() if await request.body() else {}
    if not data.get("gnb_name"):
        raise HTTPException(status_code=400, detail="gnb_name is required")
    try:
        entry = gnb_cfg_add(GNB_PROFILES_PATH, data)
        log.info("gNB config added: %s", data["gnb_name"])
        return {"ok": True, "entry": entry}
    except ValueError as e:
        raise HTTPException(status_code=409, detail=str(e))

@app.put("/api/gnb-config/{name}")
async def api_gnb_cfg_update(name: str, request: Request):
    data = await request.json() if await request.body() else {}
    try:
        entry = gnb_cfg_update(GNB_PROFILES_PATH, name, data)
        log.info("gNB profile updated: %s", name)
        return {"ok": True, "entry": entry}
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))

@app.delete("/api/gnb-config/{name}")
async def api_gnb_cfg_delete(name: str):
    ok = gnb_cfg_delete(GNB_PROFILES_PATH, name)
    if ok:
        log.info("gNB profile deleted: %s", name)
    return {"ok": ok}

@app.post("/api/gnb-config/clone")
async def api_gnb_cfg_clone(request: Request):
    data = await request.json() if await request.body() else {}
    src = data.get("source_name", "")
    new = data.get("new_name", "")
    if not src or not new:
        raise HTTPException(status_code=400, detail="source_name and new_name required")
    try:
        entry = gnb_cfg_clone(GNB_PROFILES_PATH, src, new)
        log.info("gNB profile cloned: %s -> %s", src, new)
        return {"ok": True, "entry": entry}
    except ValueError as e:
        raise HTTPException(status_code=409, detail=str(e))

@app.post("/api/gnb-config/import")
async def api_gnb_cfg_import(request: Request):
    data = await request.json() if await request.body() else {}
    items = data.get("items", [])
    overwrite = data.get("overwrite", False)
    if not items:
        raise HTTPException(status_code=400, detail="items list required")
    count = gnb_cfg_import(GNB_PROFILES_PATH, items, overwrite=overwrite)
    log.info("gNB profile import: %d entries", count)
    return {"ok": True, "imported": count}

@app.post("/api/gnb-config/{name}/apply")
async def api_gnb_cfg_apply(name: str):
    """Create a gNB from a saved profile and add to gnb_pool."""
    entry = gnb_cfg_get(GNB_PROFILES_PATH, name)
    if entry is None:
        raise HTTPException(status_code=404, detail=f"Profile '{name}' not found")
    # Validate — all config must come from GUI
    required = ["gnb_id", "gnb_name", "amf_ip", "mcc", "mnc", "tac", "slices"]
    missing = [f for f in required if not entry.get(f)]
    if missing:
        raise HTTPException(status_code=400, detail=f"gNB config missing: {missing}")

    gnb_id_str = str(entry["gnb_id"])
    gnb_id = int(gnb_id_str, 16) if gnb_id_str.startswith("0x") else int(gnb_id_str)
    slices = entry["slices"]
    for s in slices:
        if isinstance(s.get("sd"), str) and s["sd"].startswith("0x"):
            s["sd"] = int(s["sd"], 16)

    # Ensure gnb_ip is available on the interface (create alias if needed)
    gnb_ip = entry.get("gnb_ip")
    iface = entry.get("interface")
    if gnb_ip and iface:
        from src.testcases.base import _ensure_ip_on_interface
        _ensure_ip_on_interface(iface, gnb_ip)

    gnb = GnbStateMachine(
        amf_ip=entry["amf_ip"],
        amf_port=entry.get("amf_port", 38412),
        gnb_id=gnb_id,
        gnb_name=entry["gnb_name"],
        mcc=entry["mcc"],
        mnc=entry["mnc"],
        tac=entry["tac"],
        slices=slices,
        source_ip=gnb_ip,
        gtpu_manager=gtpu_manager,
    )
    gnb_pool.append(gnb)
    log.info("gNB created from profile '%s': %s (source_ip=%s, iface=%s)", name, gnb.gnb_name, gnb_ip, iface)
    return {"ok": True, "gnb": gnb.to_dict()}


# ── Database API ──

@app.get("/api/db/stats")
async def api_db_stats():
    """Get DB statistics."""
    return {
        "ue_count": db.ue_count(),
        "gnb_count": len(db.gnb_list()),
        "test_results": db.result_stats(),
        "pending_sync": len(db.sync_pending()),
    }


@app.get("/api/db/ue")
async def api_db_ue_list(gnb_name: str = None):
    """List all UEs from DB."""
    return {"items": db.ue_list(gnb_name)}


@app.get("/api/db/ue/{imsi}")
async def api_db_ue_get(imsi: str):
    """Get UE from DB."""
    ue = db.ue_get(imsi)
    if not ue:
        raise HTTPException(status_code=404, detail="not found")
    return ue


@app.get("/api/db/results")
async def api_db_results(limit: int = 50, status: str = None, name: str = None):
    """List test results from DB."""
    return {"items": db.result_list(limit, status, name)}


@app.get("/api/db/results/{rid}")
async def api_db_result_get(rid: int):
    """Get full test result from DB."""
    r = db.result_get(rid)
    if not r:
        raise HTTPException(status_code=404, detail="not found")
    return r


@app.post("/api/db/migrate")
async def api_db_migrate():
    """Re-run JSON → SQLite migration."""
    db.migrate_from_json(None, GNB_PROFILES_PATH)
    return {"ok": True, "ue_count": db.ue_count(), "gnb_count": len(db.gnb_list())}


# ── Test Runs & Reports API ──

@app.get("/api/runs")
async def api_runs_list(limit: int = 20):
    """List test runs."""
    from src.db.runs import list_runs
    return {"items": list_runs(limit)}


@app.get("/api/runs/{run_id}")
async def api_run_get(run_id: str):
    """Get test run with results."""
    from src.db.runs import get_run
    run = get_run(run_id)
    if not run:
        raise HTTPException(status_code=404, detail="not found")
    return run


@app.get("/api/runs/{run_id}/report/{fmt}")
async def api_run_report(run_id: str, fmt: str):
    """Generate and serve report (html/json/junit)."""
    from src.db.reports import generate_html_report, generate_json_report, generate_junit_xml
    if fmt == "html":
        path = generate_html_report(run_id)
        if path:
            return FileResponse(path, media_type="text/html")
    elif fmt == "json":
        path = generate_json_report(run_id)
        if path:
            return FileResponse(path, media_type="application/json")
    elif fmt in ("junit", "xml"):
        path = generate_junit_xml(run_id)
        if path:
            return FileResponse(path, media_type="application/xml")
    raise HTTPException(status_code=500, detail="report generation failed")


@app.get("/api/reports")
async def api_reports_list():
    """List generated reports."""
    from src.db.reports import list_reports
    return {"items": list_reports()}


@app.get("/api/analysis/pass-rate")
async def api_analysis_pass_rate(last_n: int = 10):
    """Pass rate trend over recent runs."""
    from src.db.analysis import pass_rate
    return {"items": pass_rate(last_n)}


@app.get("/api/analysis/flaky")
async def api_analysis_flaky():
    """Flaky tests detection."""
    from src.db.analysis import flaky_tests
    return {"items": flaky_tests()}


@app.get("/api/analysis/failures")
async def api_analysis_failures():
    """Failure heatmap — most frequently failing tests."""
    from src.db.analysis import failure_heatmap
    return {"items": failure_heatmap()}


@app.get("/api/analysis/suites")
async def api_analysis_suites(run_id: str = None):
    """Suite summary for latest run."""
    from src.db.analysis import suite_summary
    return {"items": suite_summary(run_id=run_id)}


@app.get("/api/analysis/regressions/{run_id}")
async def api_analysis_regressions(run_id: str):
    """Find regressions compared to previous run."""
    from src.db.analysis import regressions
    return {"items": regressions(run_id)}


@app.get("/api/analysis/compare")
async def api_analysis_compare(a: str = None, b: str = None):
    """Compare two runs."""
    from src.db.analysis import compare_runs
    if not a or not b:
        raise HTTPException(status_code=400, detail="provide ?a=run_id&b=run_id")
    return compare_runs(a, b)


@app.get("/api/analysis/metric-trend")
async def api_analysis_metric_trend(test: str = None, metric: str = None):
    """Performance metric trend for a test."""
    from src.db.analysis import metric_trend
    if not test or not metric:
        raise HTTPException(status_code=400, detail="provide ?test=name&metric=name")
    return {"items": metric_trend(test, metric)}


# ── Core Provisioning & Admin API ──

@app.post("/api/core/sync-ues")
async def api_core_sync_ues():
    """Provision all UEs from sim_db.json to sa_core."""
    from src.core.provisioner import sync_all_ues
    provisioned, failed = sync_all_ues()
    return {"ok": True, "provisioned": provisioned, "failed": failed}


@app.post("/api/core/provision-ue")
async def api_core_provision_ue(request: Request):
    """Provision a single UE on sa_core."""
    from src.core.provisioner import provision_ue_auth, provision_subscription
    d = await request.json()
    imsi = d.get("imsi")
    if not imsi:
        raise HTTPException(status_code=400, detail="imsi required")
    result = provision_ue_auth(
        imsi=imsi, k=d.get("k", ""), opc=d.get("opc", ""),
        op_type=d.get("op_type", "OPC"), amf=d.get("amf", "8000"),
        sqn=d.get("sqn", 0), msisdn=d.get("msisdn"),
        suci_profile=d.get("suci_profile"), hn_private_key=d.get("hn_private_key"))
    if result and result.get("ok"):
        provision_subscription(imsi)
    return result or {"error": "provisioning failed"}


@app.post("/api/core/provision-suci-key")
async def api_core_provision_suci_key(request: Request):
    """Provision SUCI HN private key on sa_core for a UE."""
    from src.core.provisioner import provision_suci_keys
    d = await request.json()
    imsi = d.get("imsi")
    hn_private_key = d.get("hn_private_key")
    suci_profile = d.get("suci_profile", "A")
    if not imsi or not hn_private_key:
        raise HTTPException(status_code=400, detail="imsi and hn_private_key required")
    result = provision_suci_keys(imsi, hn_private_key, suci_profile)
    return result or {"error": "failed"}


@app.delete("/api/core/delete-ue/{imsi}")
async def api_core_delete_ue(imsi: str):
    """Delete a UE from sa_core."""
    from src.core.provisioner import delete_ue
    return delete_ue(imsi) or {"error": "failed"}


@app.get("/api/core/nf-status")
async def api_core_nf_status():
    """Get sa_core NF status."""
    from src.core.admin import get_nf_status
    return get_nf_status() or {"error": "core unreachable"}


@app.post("/api/core/soft-restart")
async def api_core_soft_restart():
    """Soft restart sa_core — flush UE contexts, PDU sessions, IMS, IP pools."""
    from src.core.admin import soft_restart
    return soft_restart()


@app.post("/api/core/flush-ue-contexts")
async def api_core_flush_contexts():
    """Flush UE contexts on sa_core AMF."""
    from src.core.admin import flush_ue_contexts
    return flush_ue_contexts() or {"error": "failed"}


@app.post("/api/core/clear-pdu-sessions")
async def api_core_clear_pdu():
    """Clear PDU sessions on sa_core."""
    from src.core.admin import clear_pdu_sessions
    return clear_pdu_sessions() or {"error": "failed"}


@app.get("/api/core/db-stats")
async def api_core_db_stats():
    """Get sa_core DB statistics."""
    from src.core.admin import get_db_stats
    return get_db_stats() or {"error": "core unreachable"}


@app.get("/api/core/upf-stats")
async def api_core_upf_stats():
    """UPF counters via sa_core — observability side-channel, not the traffic path."""
    from src.observability.core_stats import collect_upf_stats
    return collect_upf_stats()


# ── Training Notes API ──

@app.get("/api/training-note/{tc_id}")
async def api_training_note(tc_id: str):
    """Serve training note markdown for a test case."""
    import glob
    notes_dir = os.path.join(PROJECT_ROOT, "docs", "training_notes")
    pattern = os.path.join(notes_dir, "**", f"{tc_id}*.md")
    matches = glob.glob(pattern, recursive=True)
    if not matches:
        raise HTTPException(status_code=404, detail=f"No training note for {tc_id}")
    with open(matches[0], "r", encoding="utf-8") as f:
        content = f.read()
    return {"tc_id": tc_id, "content": content, "file": os.path.basename(matches[0])}


# ── Test API ──

@app.get("/api/tests")
async def api_tests():
    return {"available": runner.get_test_list(), "results": runner.get_results()}

_running_tests = {}   # name → Thread

@app.post("/api/tests/{name}/run")
async def api_run_test(name: str, request: Request):
    log.info("Test requested: %s", name)

    # Prevent double-runs of the same test
    t = _running_tests.get(name)
    if t and t.is_alive():
        raise HTTPException(status_code=409, detail="Test already running")

    params = await request.json() if await request.body() else {}

    def _run():
        try:
            result = runner.run_test(name, gnb_pool, ue_pool, params)
            log.info("Test done: %s -> %s", name, result.status)
        except Exception as e:
            log.error("Test %s CRASHED: %s", name, e, exc_info=True)
        finally:
            _running_tests.pop(name, None)

    t = threading.Thread(target=_run, daemon=True, name=f"test-{name}")
    t.start()
    _running_tests[name] = t
    return {"ok": True, "test": name, "status": "started"}

@app.post("/api/tests/clear")
async def api_clear_results():
    runner.clear_results()
    return {"ok": True}

@app.post("/api/tests/save-report")
async def api_save_report():
    """Save a timestamped report snapshot of current results."""
    from src.testcases.test_runner import save_run_report
    results = runner.get_results()
    test_list = runner.get_test_list()
    with _robot_lock:
        robot_res = list(_robot_results)
    path = save_run_report(results, test_list, robot_res)
    return {"ok": True, "path": path}

@app.get("/api/tests/reports")
async def api_list_reports():
    """List all saved test reports."""
    return {"reports": runner.list_reports()}

@app.get("/api/tests/reports/{filename}")
async def api_get_report(filename: str):
    """Load a specific saved report."""
    data = runner.load_report(filename)
    if data is None:
        raise HTTPException(status_code=404, detail="Report not found")
    return data


# ── Per-test packet capture ──
#
# TestRunner wraps each test with a tcpdump subprocess writing to
# ACTIVE_PCAP_PATH (=/tmp/mmt-active.pcap inside the satester
# container). Two endpoints expose that capture:
#
#   GET /api/tests/active/pcap.stream
#       Server-Sent-Events-style chunked stream. As long as a test
#       is running, every fresh byte tcpdump writes to the active
#       pcap is forwarded to the client. The client side is meant
#       to be a `wireshark -k -i -` pipe, but anything that speaks
#       pcap (tshark, scapy, custom parsers) works too. Connection
#       closes shortly after the test ends.
#
#   GET /api/tests/runs/<basename>/pcap
#       Static download of a completed test's pcap. `basename` is
#       the run_id reported in /api/tests results.

# StreamingResponse + FileResponse already imported at the top of
# this file; just need the pcap module here for the path constants.
from src.testcases import _pcap_capture as _pcap

@app.get("/api/tests/active/pcap.stream")
async def api_stream_active_pcap():
    """Stream the live pcap as long as a test is running. Wireshark
    on the operator's host can attach with:

        curl.exe -N -s http://<host>:5001/api/tests/active/pcap.stream \\
            | "C:\\Program Files\\Wireshark\\Wireshark.exe" -k -i -

    Streaming, not polling -- the response body keeps flowing
    chunk-by-chunk as tcpdump flushes packets (it runs with -U so
    each packet is on disk before the next one is read)."""
    path = _pcap.ACTIVE_PCAP_PATH

    def gen():
        # Wait briefly for a test to start so the curl spawned by the
        # Windows watcher exactly at-trigger time doesn't see the
        # connection close before the test even appended its
        # RUNNING entry to /api/tests.
        wait_deadline = time.time() + 5.0
        while not os.path.exists(path) and time.time() < wait_deadline:
            time.sleep(0.05)
        if not os.path.exists(path):
            return

        try:
            f = open(path, "rb")
        except FileNotFoundError:
            return

        try:
            # Idle counter so we exit a stale stream a beat AFTER the
            # active pcap file disappears (PcapCapture.stop renames it
            # to the per-run final path). 1 s of idle reads with no
            # active file == test is over, we can flush + close.
            idle_after_done = 0
            while True:
                chunk = f.read(65536)
                if chunk:
                    yield chunk
                    idle_after_done = 0
                    continue
                # No new bytes. Either the test is still running
                # between packets, or it's over. Differentiate by
                # checking the active file: PcapCapture.stop moves
                # it away, so its disappearance is the "done" signal.
                if not os.path.exists(path):
                    idle_after_done += 1
                    if idle_after_done > 10:   # ~1 s post-done
                        return
                time.sleep(0.1)
        finally:
            f.close()

    return StreamingResponse(gen(), media_type="application/vnd.tcpdump.pcap")


@app.get("/api/tests/runs/{run_id}/pcap")
async def api_download_run_pcap(run_id: str):
    """Download a completed test's pcap. `run_id` matches the value
    that test_runner.py composes
    (`{YYYYMMDD_HHMMSS}_{test_name}`) and that /api/tests results
    surface as `run_id`."""
    # Resolve safely under the runs directory; reject anything that
    # tries to escape via .. or absolute paths.
    safe = os.path.basename(run_id)
    if safe != run_id:
        raise HTTPException(status_code=400, detail="invalid run_id")
    candidate = os.path.join(_pcap.RUNS_DIR, f"{safe}.pcap")
    if not os.path.isfile(candidate):
        raise HTTPException(status_code=404, detail="pcap not found")
    return FileResponse(
        candidate,
        media_type="application/vnd.tcpdump.pcap",
        filename=f"{safe}.pcap",
    )


# ── Robot Framework API ──

_robot_results = []   # list of dicts with suite/test results
_robot_lock = threading.Lock()
_robot_running = False

def _run_robot_suite(suite_path, test_names=None):
    """Run a Robot Framework suite in a background thread."""
    global _robot_running
    import tempfile
    try:
        from robot.api import TestSuite
        from robot.reporting import ResultWriter

        output_dir = os.path.join(PROJECT_ROOT, "data", "robot_output")
        os.makedirs(output_dir, exist_ok=True)

        result_entry = {
            "suite": os.path.basename(suite_path),
            "status": "RUNNING",
            "started": time.time(),
            "tests": [],
        }
        with _robot_lock:
            _robot_results.append(result_entry)

        # Build robot arguments
        args = [
            "--outputdir", output_dir,
            "--loglevel", "DEBUG",
        ]
        if test_names:
            for tn in test_names:
                args.extend(["--test", tn])

        # Run using robot.run
        import robot
        rc = robot.run(suite_path, *args, output=os.path.join(output_dir, "output.xml"))

        # Parse results from output.xml
        from robot.api import ExecutionResult
        xml_path = os.path.join(output_dir, "output.xml")
        if os.path.exists(xml_path):
            exec_result = ExecutionResult(xml_path)
            suite_result = exec_result.suite
            test_results = []
            for test in suite_result.tests:
                test_results.append({
                    "name": test.name,
                    "status": test.status,
                    "message": str(test.message) if test.message else "",
                    "elapsed_ms": test.elapsed_time.total_seconds() * 1000
                    if hasattr(test.elapsed_time, 'total_seconds')
                    else test.elapsedtime,
                    "tags": list(test.tags),
                })
            result_entry["tests"] = test_results
            result_entry["total"] = suite_result.statistics.total
            result_entry["passed"] = suite_result.statistics.passed
            result_entry["failed"] = suite_result.statistics.failed
            result_entry["status"] = "PASS" if suite_result.statistics.failed == 0 else "FAIL"
        else:
            result_entry["status"] = "ERROR"
            result_entry["error"] = "No output.xml generated"

        result_entry["return_code"] = rc
        result_entry["duration_ms"] = round((time.time() - result_entry["started"]) * 1000)
        log.info("Robot suite %s: %s (rc=%d)", result_entry["suite"], result_entry["status"], rc)

    except Exception as e:
        log.error("Robot execution error: %s", e, exc_info=True)
        result_entry["status"] = "ERROR"
        result_entry["error"] = str(e)
        result_entry["duration_ms"] = round((time.time() - result_entry["started"]) * 1000)
    finally:
        _robot_running = False


@app.get("/api/robot/suites")
async def api_robot_suites():
    """List available Robot Framework test suites (recursively, grouped by subdir)."""
    suites_dir = os.path.join(PROJECT_ROOT, "robot", "suites")
    suites = []
    if os.path.isdir(suites_dir):
        robot_files = []
        for root, _dirs, files in os.walk(suites_dir):
            for f in files:
                if f.endswith(".robot"):
                    full = os.path.join(root, f)
                    robot_files.append((os.path.relpath(full, suites_dir), full))
        robot_files.sort()

        for rel, fpath in robot_files:
            group = os.path.dirname(rel) or ""  # e.g. "access", "other"
            doc = ""
            tests = []
            with open(fpath, "r", encoding="utf-8") as fh:
                in_tests = False
                in_doc = False
                for line in fh:
                    stripped = line.strip()
                    if stripped.startswith("*** Test Cases ***"):
                        in_tests = True
                        in_doc = False
                        continue
                    if stripped.startswith("*** ") and "Test Cases" not in stripped:
                        in_tests = False
                    if stripped.startswith("Documentation") and not in_tests:
                        doc = stripped.replace("Documentation", "", 1).strip()
                        in_doc = True
                        continue
                    if in_doc and stripped.startswith("..."):
                        doc += " " + stripped[3:].strip()
                        continue
                    if in_doc:
                        in_doc = False
                    if in_tests and stripped and not stripped.startswith("[") and not stripped.startswith("#") and not stripped.startswith("..."):
                        if line[0] != " " and line[0] != "\t" and not stripped.startswith("$") and not stripped.startswith("@"):
                            tests.append(stripped)
            suites.append({
                "file": rel,                       # e.g. "access/01_registration.robot"
                "name": os.path.basename(rel),     # e.g. "01_registration.robot"
                "group": group,                    # e.g. "access"
                "path": fpath,
                "documentation": doc,
                "tests": tests,
                "test_count": len(tests),
            })
    return {"suites": suites}


@app.post("/api/robot/run")
async def api_robot_run(request: Request):
    """Run a Robot Framework suite or specific tests."""
    global _robot_running
    if _robot_running:
        raise HTTPException(status_code=409, detail="Robot suite already running")

    data = await request.json() if await request.body() else {}
    suite_file = data.get("suite", "")
    test_names = data.get("tests", [])  # empty = run all

    suites_dir = os.path.join(PROJECT_ROOT, "robot", "suites")
    suite_path = os.path.join(suites_dir, suite_file)
    if not os.path.exists(suite_path):
        # Caller may have passed just the basename (e.g. "01_registration.robot")
        # while suites now live under group subdirs — locate by basename.
        match = None
        for root, _dirs, files in os.walk(suites_dir):
            if suite_file in files:
                match = os.path.join(root, suite_file)
                break
        if match:
            suite_path = match
        else:
            raise HTTPException(status_code=404, detail=f"Suite not found: {suite_file}")

    _robot_running = True
    import threading as _thr
    t = _thr.Thread(target=_run_robot_suite, args=(suite_path, test_names or None), daemon=True)
    t.start()
    return {"ok": True, "suite": suite_file}


@app.get("/api/robot/results")
async def api_robot_results():
    """Get Robot Framework execution results."""
    with _robot_lock:
        return {
            "running": _robot_running,
            "results": _robot_results[-20:],  # last 20 runs
        }


@app.post("/api/robot/results/clear")
async def api_robot_clear():
    """Clear Robot Framework results."""
    with _robot_lock:
        _robot_results.clear()
    return {"ok": True}


# ── Logs API (ring-buffer backed, ems_server style) ──

@app.get("/api/logs")
async def api_logs(after_seq: int = 0, level: str = "", logger: str = "",
                   search: str = "", limit: int = 200):
    """
    Poll log entries from the ring buffer.

    Query params:
        after_seq  -- only entries with seq > this value (incremental polling)
        level      -- minimum level filter (DEBUG / INFO / WARNING / ERROR)
        logger     -- substring match on logger name
        search     -- substring match on message text
        limit      -- max entries to return (default 200)
    """
    ring = RingBufferHandler.get_instance()
    entries = ring.get_entries(
        after_seq=after_seq, level=level,
        logger_filter=logger, search=search, last_n=limit,
    )
    return {
        "entries": entries,
        "current_seq": ring.current_seq,
        "loggers": ring.get_logger_names(),
    }


@app.get("/api/logs/stream")
async def api_logs_stream(after_seq: int = 0, level: str = "", logger: str = ""):
    """Server-Sent Events stream of new log entries."""
    ring = RingBufferHandler.get_instance()
    last_seq = after_seq

    def generate():
        nonlocal last_seq
        while True:
            entries = ring.get_entries(
                after_seq=last_seq, level=level,
                logger_filter=logger, last_n=50,
            )
            if entries:
                last_seq = entries[-1]["seq"]
                yield f"data: {json.dumps(entries)}\n\n"
            else:
                yield "data: []\n\n"
            time.sleep(1)

    return StreamingResponse(
        generate(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


@app.get("/api/logs/config")
async def api_logs_config():
    """Return all active tester.* loggers and their levels."""
    return {"loggers": get_all_loggers()}


@app.post("/api/logs/level")
async def api_logs_set_level(request: Request):
    """Change a logger's level at runtime. Body: {"logger":"tester.app","level":"WARNING"}"""
    data = await request.json() if await request.body() else {}
    name = data.get("logger", "")
    level = data.get("level", "")
    if not name or not level:
        raise HTTPException(status_code=400, detail="Missing logger or level")
    valid = ("DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL")
    if level.upper() not in valid:
        raise HTTPException(status_code=400, detail=f"Invalid level. Use: {valid}")
    set_level(name, level)
    save_levels()
    log.info("Logger %s level changed to %s", name, level.upper())
    return {"ok": True, "logger": name, "level": level.upper()}


@app.post("/api/logs/level/all")
async def api_logs_set_level_all(request: Request):
    """Set the same level on every tester logger. Body: {"level":"WARNING"}"""
    data = await request.json() if await request.body() else {}
    level = data.get("level", "")
    if not level:
        raise HTTPException(status_code=400, detail="Missing level")
    valid = ("DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL")
    if level.upper() not in valid:
        raise HTTPException(status_code=400, detail=f"Invalid level. Use: {valid}")
    set_all_levels(level)
    save_levels()
    log.info("All loggers set to %s", level.upper())
    return {"ok": True, "level": level.upper()}


@app.post("/api/logs/clear")
async def api_logs_clear():
    """Clear the in-memory log ring buffer."""
    ring = RingBufferHandler.get_instance()
    ring.clear()
    log.info("Log buffer cleared by user")
    return {"ok": True, "message": "Log buffer cleared"}


LOG_DIR = "/var/log/sa_tester"


@app.get("/api/logs/file")
async def api_logs_file_info():
    """Get log file path and size info."""
    log_dir = LOG_DIR
    log_path = os.path.join(log_dir, "sa_tester.log")
    files = []
    try:
        for f in sorted(os.listdir(log_dir)):
            fp = os.path.join(log_dir, f)
            if os.path.isfile(fp):
                files.append({
                    "name": f,
                    "path": fp,
                    "size_bytes": os.path.getsize(fp),
                    "size_mb": round(os.path.getsize(fp) / (1024 * 1024), 2),
                    "modified": os.path.getmtime(fp),
                })
    except Exception:
        pass
    return {"log_dir": log_dir, "current_log": log_path, "files": files}


@app.get("/api/logs/file/download")
async def api_logs_file_download():
    """Download the current log file."""
    log_path = os.path.join(LOG_DIR, "sa_tester.log")
    if os.path.exists(log_path):
        return FileResponse(log_path, media_type="text/plain", filename="sa_tester.log")
    raise HTTPException(status_code=404, detail="Log file not found")


@app.get("/api/logs/file/tail")
async def api_logs_file_tail(lines: int = 100):
    """Get last N lines of the log file."""
    log_path = os.path.join(LOG_DIR, "sa_tester.log")
    try:
        with open(log_path, "r", encoding="utf-8", errors="replace") as f:
            all_lines = f.readlines()
            tail = all_lines[-lines:] if len(all_lines) > lines else all_lines
        return {"lines": len(tail), "content": "".join(tail)}
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))


@app.post("/api/logs/file/clear")
async def api_logs_file_clear():
    """Clear (truncate) all log files."""
    import logging.handlers
    log_dir = LOG_DIR
    cleared = []
    # Close and reopen the file handler to release the file
    tester_root = logging.getLogger("tester")
    for handler in tester_root.handlers:
        if isinstance(handler, logging.handlers.RotatingFileHandler):
            handler.close()
    # Truncate all log files
    try:
        for f in os.listdir(log_dir):
            fp = os.path.join(log_dir, f)
            if os.path.isfile(fp) and f.startswith("sa_tester"):
                open(fp, "w").close()
                cleared.append(f)
    except Exception as e:
        raise HTTPException(status_code=500, detail=str(e))
    # Reopen the file handler
    for handler in tester_root.handlers:
        if isinstance(handler, logging.handlers.RotatingFileHandler):
            handler.stream = handler._open()
    # Also clear ring buffer
    RingBufferHandler.get_instance().clear()
    log.info("Log files cleared by user: %s", cleared)
    return {"ok": True, "cleared": cleared}


# ── AI Engine API ──

@app.get("/api/ai/status")
async def api_ai_status():
    """AI engine status: Ollama connectivity, models, RAG store size."""
    health = _ai_client.health_check()
    return {
        "ollama": health,
        "rag": {
            "documents": _ai_rag.doc_count,
            "chunks": _ai_rag.chunk_count,
            "indexed_docs": _ai_rag.store.get_docs(),
        },
        "pcap": {
            "tshark_available": _ai_pcap.tshark_available,
        },
    }


@app.post("/api/ai/ask")
async def api_ai_ask(request: Request):
    """Ask a question using RAG (retrieval-augmented generation)."""
    data = await request.json() if await request.body() else {}
    question = data.get("question", "").strip()
    if not question:
        raise HTTPException(status_code=400, detail="question is required")

    use_rag = data.get("use_rag", True)
    doc_filter = data.get("doc_filter", "")
    top_k = data.get("top_k", 8)

    if use_rag and _ai_rag.chunk_count > 0:
        result = _ai_rag.query(question, top_k=top_k, doc_filter=doc_filter)
        sources = [
            {
                "doc_id": sr.chunk.doc_id,
                "section": sr.chunk.section,
                "score": round(sr.score, 4),
                "text": sr.chunk.text[:200],
            }
            for sr in result.sources[:5]
        ]
        return {
            "answer": result.answer,
            "sources": sources,
            "model": result.model,
            "duration_ms": round(result.duration_ms),
            "mode": "rag",
        }
    else:
        result = _ai_rag.query_no_rag(question)
        return {
            "answer": result.answer,
            "sources": [],
            "model": result.model,
            "duration_ms": round(result.duration_ms),
            "mode": "direct",
        }


@app.post("/api/ai/chat")
async def api_ai_chat(request: Request):
    """Chat with conversation history."""
    data = await request.json() if await request.body() else {}
    message = data.get("message", "").strip()
    if not message:
        raise HTTPException(status_code=400, detail="message is required")

    clear = data.get("clear", False)
    if clear:
        _ai_chat_history.clear()

    _ai_chat_history.append({"role": "user", "content": message})
    # Keep last 20 messages to avoid context overflow
    if len(_ai_chat_history) > 20:
        _ai_chat_history[:] = _ai_chat_history[-20:]

    result = _ai_client.chat(_ai_chat_history[:])
    assistant_msg = result.get("response", "")
    _ai_chat_history.append({"role": "assistant", "content": assistant_msg})

    return {
        "response": assistant_msg,
        "model": result.get("model", ""),
        "duration_ms": round(result.get("total_duration_ms", 0)),
        "history_length": len(_ai_chat_history),
    }


@app.post("/api/ai/chat/clear")
async def api_ai_chat_clear():
    """Clear chat history."""
    _ai_chat_history.clear()
    return {"ok": True}


@app.post("/api/ai/analyze-log")
async def api_ai_analyze_log(request: Request):
    """Analyze SA tester log events with AI."""
    data = await request.json() if await request.body() else {}
    question = data.get("question", "")
    # Get recent logs from ring buffer
    ring = RingBufferHandler.get_instance()
    level = data.get("level", "")
    logger_filter = data.get("logger", "")
    count = data.get("count", 100)
    entries = ring.get_entries(level=level, logger_filter=logger_filter, last_n=count)

    if not entries:
        return {"answer": "No log entries found to analyze.", "entries": 0}

    result = _ai_client.analyze_log(entries, question=question)
    return {
        "answer": result.get("response", ""),
        "model": result.get("model", ""),
        "duration_ms": round(result.get("total_duration_ms", 0)),
        "entries_analyzed": len(entries),
    }


@app.post("/api/ai/analyze-pcap")
async def api_ai_analyze_pcap(request: Request):
    """Analyze a Wireshark PCAP file with AI."""
    data = await request.json() if await request.body() else {}
    pcap_path = data.get("path", "")
    question = data.get("question", "")

    if not pcap_path:
        raise HTTPException(status_code=400, detail="path is required")
    if not os.path.isfile(pcap_path):
        raise HTTPException(status_code=404, detail=f"File not found: {pcap_path}")

    summary = _ai_pcap.analyze_pcap(pcap_path, ai_analyze=True)
    return _ai_pcap.to_dict(summary)


@app.get("/api/ai/index")
async def api_ai_index_list():
    """List indexed documents in the RAG knowledge base."""
    docs = _ai_rag.store.get_docs()
    doc_info = []
    for doc_id in docs:
        chunks = _ai_rag.store.get_doc_chunks(doc_id)
        doc_info.append({
            "doc_id": doc_id,
            "chunks": len(chunks),
            "sections": list(set(c.section for c in chunks if c.section)),
        })
    return {"documents": doc_info, "total_chunks": _ai_rag.chunk_count}


@app.post("/api/ai/index")
async def api_ai_index_add(request: Request):
    """Index a document into the RAG knowledge base.
    Body: {doc_id, section, text} or {doc_id, chunks: [{section, text}, ...]}
    """
    data = await request.json() if await request.body() else {}
    doc_id = data.get("doc_id", "")
    if not doc_id:
        raise HTTPException(status_code=400, detail="doc_id is required")

    chunks_data = data.get("chunks")
    if chunks_data:
        count = _ai_rag.index_chunks(doc_id, chunks_data)
    else:
        section = data.get("section", "")
        text = data.get("text", "")
        if not text:
            raise HTTPException(status_code=400, detail="text or chunks required")
        count = _ai_rag.index_text(doc_id, section, text)

    _ai_rag.save()
    log.info("Indexed %d chunks for doc %s", count, doc_id)
    return {"ok": True, "doc_id": doc_id, "chunks_indexed": count}


@app.delete("/api/ai/index/{doc_id}")
async def api_ai_index_delete(doc_id: str):
    """Remove a document from the knowledge base."""
    removed = _ai_rag.store.remove_doc(doc_id)
    _ai_rag.save()
    log.info("Removed doc %s (%d chunks)", doc_id, removed)
    return {"ok": True, "doc_id": doc_id, "chunks_removed": removed}


@app.post("/api/tests/{name}/analyze")
async def api_test_analyze(name: str):
    """Generate AI analysis for a single test result including protocol trace and logs."""
    # Find the latest result for this test
    results = runner.get_results()
    result = None
    for r in reversed(results):
        if r.get("test_name") == name:
            result = r
            break
    if result is None:
        raise HTTPException(status_code=404, detail="No results found for this test")

    # Get test description
    test_list = runner.get_test_list()
    desc = next((t["description"] for t in test_list if t["name"] == name), "")

    # Build comprehensive context for AI
    lines = []
    lines.append(f"=== Single Test Analysis: {name} ===")
    lines.append(f"Status: {result.get('status')}")
    lines.append(f"Duration: {result.get('duration_ms', 0):.0f} ms")
    lines.append(f"Timestamp: {result.get('timestamp', '')}")
    if desc:
        lines.append(f"\nTest Description:\n{desc}")
    if result.get("error"):
        lines.append(f"\nError: {result['error']}")
    if result.get("details"):
        lines.append("\nTest Details:")
        for k, v in result["details"].items():
            lines.append(f"  {k}: {v}")

    # Protocol trace (NGAP messages) — compact, no hex for AI
    trace = result.get("protocol_trace", [])
    if trace:
        lines.append(f"\n=== Protocol Trace ({len(trace)} messages) ===")
        for t in trace[-20:]:
            ts = time.strftime('%H:%M:%S', time.localtime(t.get('time', 0)))
            d = t.get('dir', '?')
            msg = t.get('msg_type', '?')
            proc = t.get('proc', '?')
            sz = t.get('size', 0)
            gnb = t.get('gnb', '')
            lines.append(f"  [{ts}] {d:2s} {gnb}: {msg} (proc={proc}, {sz}B)")
    else:
        lines.append("\nNo protocol trace captured for this test.")

    # Test execution logs — only warnings/errors + key INFO
    logs = result.get("logs", [])
    if logs:
        important = [lg for lg in logs if lg.get('level') in ('WARNING', 'ERROR', 'CRITICAL')]
        if len(important) < 20:
            important = logs[-30:]
        lines.append(f"\n=== Execution Logs ({len(important)} of {len(logs)} entries) ===")
        for lg in important[-30:]:
            ts_val = lg.get("ts", 0)
            if isinstance(ts_val, (int, float)) and ts_val > 1e9:
                ts_str = time.strftime('%H:%M:%S', time.localtime(ts_val))
            else:
                ts_str = str(ts_val)[:8]
            lines.append(f"  [{ts_str}] [{lg.get('level','?'):7s}] {lg.get('logger','')}: {lg.get('msg','')}")
    else:
        lines.append("\nNo execution logs captured.")

    context = "\n".join(lines)

    try:
        ai_ok = _ai_client.is_available
    except Exception:
        ai_ok = False

    if not ai_ok:
        log.info("AI not available, returning raw context for %s", name)
        return {
            "ok": True, "report": context,
            "mode": "plain", "model": "", "duration_ms": 0,
        }

    log.info("Starting AI analysis for %s (context %d chars)...", name, len(context))
    t0 = time.time()
    try:
        ai_result = _ai_client.generate(
            prompt=(
                f"Briefly analyze test '{name}': what was tested, protocol flow, "
                f"spec compliance (3GPP TS 38.413), and verdict. Be concise."
            ),
            context=context,
            system=(
                "You are a 5G protocol test analyst. Analyze concisely with sections: "
                "Summary, Protocol Flow, Spec Compliance, Verdict. Cite 3GPP specs."
            ),
            max_tokens=512,
            timeout=60,
        )
        elapsed_ms = round((time.time() - t0) * 1000)
        log.info("AI analysis for %s done in %d ms", name, elapsed_ms)

        if ai_result.get("error"):
            log.warning("AI returned error: %s — falling back to raw context", ai_result["error"])
            return {
                "ok": True, "report": context,
                "mode": "plain", "model": "", "duration_ms": elapsed_ms,
                "note": ai_result["error"],
            }

        return {
            "ok": True,
            "report": ai_result.get("response", ""),
            "mode": "ai",
            "model": ai_result.get("model", ""),
            "duration_ms": elapsed_ms,
        }
    except Exception as exc:
        elapsed_ms = round((time.time() - t0) * 1000)
        log.error("AI analysis crashed: %s", exc, exc_info=True)
        return {
            "ok": True, "report": context,
            "mode": "plain", "model": "", "duration_ms": elapsed_ms,
            "note": f"AI error: {exc}",
        }


@app.post("/api/ai/report")
async def api_ai_report(request: Request):
    """Generate an AI-powered test report from test results.
    Body: {format: "detailed"|"summary"|"compliance", include_robot: bool}
    """
    data = await request.json() if await request.body() else {}
    fmt = data.get("format", "detailed")
    include_robot = data.get("include_robot", True)

    # Gather all built-in test results
    builtin_results = runner.get_results()
    test_list = runner.get_test_list()

    # Build test descriptions map
    test_desc = {}
    for t in test_list:
        test_desc[t["name"]] = t.get("description", "")

    # Gather Robot Framework results
    robot_results = []
    if include_robot:
        with _robot_lock:
            robot_results = list(_robot_results)

    # Build context for AI
    lines = []
    lines.append("=== SA Tester Test Results Report ===")
    lines.append(f"Generated: {time.strftime('%Y-%m-%d %H:%M:%S')}")
    lines.append(f"Total Built-in Tests Available: {len(test_list)}")
    lines.append(f"Total Built-in Results: {len(builtin_results)}")
    lines.append("")

    # Built-in test results
    if builtin_results:
        passed = sum(1 for r in builtin_results if r.get("status") == "PASS")
        failed = sum(1 for r in builtin_results if r.get("status") == "FAIL")
        errors = sum(1 for r in builtin_results if r.get("status") not in ("PASS", "FAIL"))
        lines.append(f"Built-in: {passed} PASS, {failed} FAIL, {errors} other")
        lines.append("")
        for r in builtin_results:
            name = r.get("test_name", "")
            desc = test_desc.get(name, "")
            status = r.get("status", "?")
            dur = r.get("duration_ms", 0)
            err = r.get("error", "")
            details = r.get("details", {})
            lines.append(f"  [{status}] {name} ({dur:.0f}ms)")
            if desc:
                lines.append(f"    Description: {desc}")
            if err:
                lines.append(f"    Error: {err}")
            if details:
                for k, v in details.items():
                    lines.append(f"    {k}: {v}")
        lines.append("")

    # Robot Framework results
    if robot_results:
        lines.append("=== Robot Framework Results ===")
        for rr in robot_results:
            lines.append(f"Suite: {rr.get('suite', '?')} — Status: {rr.get('status', '?')}")
            if rr.get("passed") is not None:
                lines.append(f"  Passed: {rr['passed']}, Failed: {rr.get('failed', 0)}")
            for t in rr.get("tests", []):
                lines.append(f"  [{t['status']}] {t['name']} ({t.get('elapsed_ms', 0):.0f}ms)")
                if t.get("message"):
                    lines.append(f"    Message: {t['message'][:200]}")
        lines.append("")

    context = "\n".join(lines)

    if not _ai_client.is_available:
        # Generate a plain-text report without AI
        report = context
        report += "\n--- Report generated without AI (Ollama not available) ---\n"
        return {
            "report": report,
            "mode": "plain",
            "model": "",
            "duration_ms": 0,
        }

    # AI-powered report generation
    prompts = {
        "detailed": (
            "Generate a detailed test report for this 5G SA Core tester run. "
            "For each test case: explain what was tested (referencing 3GPP specs), "
            "the expected behavior, the actual result, and for failures explain "
            "possible root causes. Include a summary with pass/fail statistics, "
            "compliance assessment against TS 38.413 / TS 24.501, and recommendations."
        ),
        "summary": (
            "Generate a concise executive summary report for this 5G SA Core tester run. "
            "Include overall pass/fail statistics, highlight any failures with brief "
            "root cause analysis, and provide a compliance verdict."
        ),
        "compliance": (
            "Generate a 3GPP standards compliance report for this 5G SA Core tester run. "
            "For each test, assess compliance with the relevant 3GPP specification section "
            "(TS 38.413 for NGAP/NG Setup, TS 24.501 for NAS/Registration, TS 23.501 for "
            "PDU Sessions). Flag any non-compliant behavior and cite specific spec sections."
        ),
    }
    prompt = prompts.get(fmt, prompts["detailed"])

    result = _ai_client.generate(
        prompt=prompt,
        context=context,
        system=(
            "You are a 5G NR SA Core test engineer generating professional test reports. "
            "Reference 3GPP specifications precisely (TS 38.413 for NGAP, TS 24.501 for NAS, "
            "TS 23.501 for architecture, TS 33.501 for security). Use structured format with "
            "sections, tables where appropriate, and clear pass/fail verdicts. "
            "The report should be suitable for submission to certification bodies."
        ),
    )

    return {
        "report": result.get("response", ""),
        "mode": "ai",
        "format": fmt,
        "model": result.get("model", ""),
        "duration_ms": round(result.get("total_duration_ms", 0)),
        "test_count": len(builtin_results),
        "robot_suites": len(robot_results),
    }


@app.get("/api/ai/models")
async def api_ai_models():
    """List available Ollama models."""
    models = _ai_client.list_models()
    return {
        "models": [
            {
                "name": m.get("name", ""),
                "size": m.get("size", 0),
                "modified": m.get("modified_at", ""),
            }
            for m in models
        ],
        "configured": _ai_config.model,
    }


@app.post("/api/ai/models/pull")
async def api_ai_models_pull(request: Request):
    """Pull (download) a model. Body: {model: "name"}"""
    data = await request.json() if await request.body() else {}
    model = data.get("model", "")
    if not model:
        raise HTTPException(status_code=400, detail="model name required")
    result = _ai_client.pull_model(model)
    return result


# ── Quick Setup ──

@app.post("/api/quick-setup")
async def api_quick_setup(request: Request):
    import src.config as cfg
    data = await request.json() if await request.body() else {}
    # Persist AMF IP/Port so test cases pick up the GUI value
    if data.get("amf_ip"):
        cfg.AMF_IP = data["amf_ip"]
    if data.get("amf_port"):
        cfg.AMF_PORT = int(data["amf_port"])
    if not gnb_pool:
        gnb_pool.append(GnbStateMachine(
            amf_ip=data.get("amf_ip", AMF_IP), amf_port=data.get("amf_port", AMF_PORT)))
    gnb = gnb_pool[0]
    if gnb.state == "IDLE":
        gnb.connect()
        gnb.wait_for_state("READY", timeout=5)
    sims = load_sims_auto(SIM_DB_PATH)
    created = 0
    for sim in sims:
        if not any(u.imsi == sim.imsi for u in ue_pool):
            ue_pool.append(UeStateMachine(sim))
            created += 1
    return {"ok": True, "gnb_state": gnb.state, "ues_loaded": created, "total_ues": len(ue_pool)}


# ── Test Sequence API ──

@app.get("/api/sequences")
async def api_sequences():
    return {"items": load_sequences()}

@app.post("/api/sequences")
async def api_save_sequence(request: Request):
    seq = await request.json() if await request.body() else {}
    if not seq.get("name"):
        raise HTTPException(status_code=400, detail="name is required")
    if "steps" not in seq:
        seq["steps"] = []
    upsert_sequence(seq)
    return {"ok": True}

@app.delete("/api/sequences/{name}")
async def api_delete_sequence(name: str):
    ok = delete_sequence(name)
    return {"ok": ok}

@app.post("/api/sequences/{name}/run")
async def api_run_sequence(name: str):
    seq = get_sequence(name)
    if seq is None:
        raise HTTPException(status_code=404, detail=f"Sequence not found: {name}")
    params = {"sequence_name": name, "steps": seq.get("steps", [])}
    runner.run_test_async("sequence", gnb_pool, ue_pool, params)
    return {"ok": True, "sequence": name}


# ── Main ──

if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description="SA Tester — 5G Core Network Tester")
    parser.add_argument("--amf-ip", default=AMF_IP)
    parser.add_argument("--amf-port", type=int, default=AMF_PORT)
    parser.add_argument("--port", type=int, default=TESTER_WEB_PORT)
    parser.add_argument("--sim-db", default="", help="Path to core's sacore.db")
    parser.add_argument("--auto-setup", action="store_true")
    args = parser.parse_args()

    import src.config as cfg
    cfg.AMF_IP = args.amf_ip
    cfg.AMF_PORT = args.amf_port
    if args.sim_db:
        cfg.SIM_DB_PATH = args.sim_db

    if args.auto_setup:
        gnb = GnbStateMachine(amf_ip=args.amf_ip, amf_port=args.amf_port)
        gnb_pool.append(gnb)
        gnb.connect()
        for sim in load_sims_auto(cfg.SIM_DB_PATH):
            ue_pool.append(UeStateMachine(sim))

    host = "0.0.0.0"
    port = args.port
    log.info("SA Tester on port %d -> AMF %s:%d", port, args.amf_ip, args.amf_port)
    print(f"\n  *** SA Tester GUI: http://127.0.0.1:{port} ***\n")

    import uvicorn
    uvicorn.run(app, host=host, port=port, log_level="warning")

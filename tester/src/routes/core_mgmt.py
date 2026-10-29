# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/core_mgmt.py — SA Core management routes (provisioning, admin)
#
# FastAPI Router: core_mgmt_router

from fastapi import APIRouter, HTTPException, Request
from src.routes.common import log

core_mgmt_router = APIRouter()


@core_mgmt_router.post("/api/core/sync-ues")
def api_core_sync():
    from src.core.provisioner import sync_all_ues
    p, f = sync_all_ues()
    return {"ok": True, "provisioned": p, "failed": f}


@core_mgmt_router.post("/api/core/provision-ue")
async def api_core_provision(request: Request):
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
    return result or {"error": "failed"}


@core_mgmt_router.post("/api/core/provision-suci-key")
async def api_core_suci_key(request: Request):
    from src.core.provisioner import provision_suci_keys
    d = await request.json()
    result = provision_suci_keys(d.get("imsi"), d.get("hn_private_key"), d.get("suci_profile", "A"))
    return result or {"error": "failed"}


@core_mgmt_router.delete("/api/core/delete-ue/{imsi}")
def api_core_delete(imsi: str):
    from src.core.provisioner import delete_ue
    return delete_ue(imsi) or {"error": "failed"}


@core_mgmt_router.get("/api/core/nf-status")
def api_core_status():
    from src.core.admin import get_nf_status
    return get_nf_status() or {"error": "core unreachable"}


@core_mgmt_router.post("/api/core/soft-restart")
def api_core_restart():
    from src.core.admin import soft_restart
    return soft_restart()


@core_mgmt_router.post("/api/core/flush-ue-contexts")
def api_core_flush():
    from src.core.admin import flush_ue_contexts
    return flush_ue_contexts() or {"error": "failed"}


@core_mgmt_router.post("/api/core/clear-pdu-sessions")
def api_core_clear_pdu():
    from src.core.admin import clear_pdu_sessions
    return clear_pdu_sessions() or {"error": "failed"}


@core_mgmt_router.get("/api/core/db-stats")
def api_core_db_stats():
    from src.core.admin import get_db_stats
    return get_db_stats() or {"error": "core unreachable"}


@core_mgmt_router.get("/api/core/upf-stats")
def api_core_upf_stats():
    """UPF counters via sa_core — observability side-channel, NOT the traffic path."""
    from src.observability.core_stats import collect_upf_stats
    return collect_upf_stats()

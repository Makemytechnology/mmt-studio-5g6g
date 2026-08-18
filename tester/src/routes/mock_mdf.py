# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/mock_mdf.py — in-tester MDF stand-in for LI X2 / X3
# delivery tests.
#
# Spec context:
#   TS 33.127 §6.3 — X2 IRI delivery (POI POSTs each event to MDF).
#   TS 33.127 §6.4 — X3 CC delivery (POI POSTs CC events to MDF).
#
# The core's X2/X3 deliverers POST JSON envelopes to the warrant's
# configured mdf_endpoint. Tests configure the warrant to point at
# this mock, register/establish, and then read the captured rows
# from /_mock_mdf/state to assert what was delivered.
#
# /_mock_mdf/x2/iri  — receives X2 IRI batches (returns 200 on
#                      success; can be configured to fail next N
#                      requests via /_mock_mdf/fail-next).
# /_mock_mdf/x3/cc   — receives X3 CC batches.
# /_mock_mdf/state   — read current capture (used by tests).
# /_mock_mdf/reset   — clear capture buffers + reset failure counter.
# /_mock_mdf/fail-next?n=2 — fail the next N requests with 500.

import threading
from typing import Any, Dict, List

from fastapi import APIRouter, Request, Response

mock_mdf_router = APIRouter()

_lock = threading.Lock()
_state: Dict[str, Any] = {
    "x2": [],   # list of received batches (dicts)
    "x3": [],
    "fail_remaining": 0,
}


def _bump_state(channel: str, body: Any):
    with _lock:
        _state[channel].append(body)


def _consume_failure() -> bool:
    with _lock:
        if _state["fail_remaining"] > 0:
            _state["fail_remaining"] -= 1
            return True
        return False


@mock_mdf_router.post("/_mock_mdf/x2/iri")
async def mock_x2(request: Request):
    if _consume_failure():
        return Response(status_code=500, content="injected-failure")
    body = await request.json()
    _bump_state("x2", body)
    return {"ok": True, "channel": "x2", "events": len(body.get("events", []))}


@mock_mdf_router.post("/_mock_mdf/x3/cc")
async def mock_x3(request: Request):
    if _consume_failure():
        return Response(status_code=500, content="injected-failure")
    body = await request.json()
    _bump_state("x3", body)
    return {"ok": True, "channel": "x3", "events": len(body.get("events", []))}


@mock_mdf_router.get("/_mock_mdf/state")
def mock_state():
    with _lock:
        return {
            "x2": list(_state["x2"]),
            "x3": list(_state["x3"]),
            "fail_remaining": _state["fail_remaining"],
        }


@mock_mdf_router.post("/_mock_mdf/reset")
def mock_reset():
    with _lock:
        _state["x2"].clear()
        _state["x3"].clear()
        _state["fail_remaining"] = 0
    return {"ok": True}


@mock_mdf_router.post("/_mock_mdf/fail-next")
def mock_fail_next(n: int = 1):
    with _lock:
        _state["fail_remaining"] = max(0, int(n))
    return {"ok": True, "fail_remaining": _state["fail_remaining"]}


def total_x2_events() -> List[Dict[str, Any]]:
    """Helper used by tests — flatten all X2 events received so far."""
    out: List[Dict[str, Any]] = []
    with _lock:
        for batch in _state["x2"]:
            for ev in batch.get("events", []):
                out.append(ev)
    return out


def total_x3_events() -> List[Dict[str, Any]]:
    out: List[Dict[str, Any]] = []
    with _lock:
        for batch in _state["x3"]:
            for ev in batch.get("events", []):
                out.append(ev)
    return out

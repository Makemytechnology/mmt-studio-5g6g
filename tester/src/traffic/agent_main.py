#!/usr/bin/env python3
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Standalone Traffic Agent — a tester-owned process that runs on the DN /
APN-side host.

Key properties:
  - Owned by sa_tester. Not part of sa_core. Makes zero sa_core API calls.
  - Pure executor: only accepts role=client|server sessions.
  - Optional shared-secret auth via X-Agent-Token header (SA_AGENT_TOKEN env
    or --token arg). If unset, auth is disabled — fine for isolated lab nets,
    not for production.

Run with:
    python -m src.traffic.agent_main --bind 0.0.0.0 --port 9100 --token <secret>
    SA_AGENT_TOKEN=<secret> python -m src.traffic.agent_main

No DB, no AI. Ships with a tiny independent dashboard at GET / so you can
open http://<dn-host>:9100/ in a browser without needing the full tester UI.
"""

import argparse
import logging
import os
import sys

_THIS_DIR = os.path.dirname(os.path.abspath(__file__))
_PROJECT_ROOT = os.path.abspath(os.path.join(_THIS_DIR, "..", ".."))
# Mirror run.sh / src/app.py — vendored deps (fastapi, uvicorn, starlette, ...)
# live under libs/, so both paths need to be importable when launching via
# `python -m src.traffic.agent_main` with no site-packages install.
for _p in (_PROJECT_ROOT,
           os.path.join(_PROJECT_ROOT, "libs"),
           os.path.join(_PROJECT_ROOT, "libs", "pycrate")):
    if _p not in sys.path and os.path.isdir(_p):
        sys.path.insert(0, _p)


def create_agent_app():
    """Build a minimal FastAPI app with the agent router + dashboard mounted."""
    from fastapi import FastAPI
    from fastapi.responses import HTMLResponse, PlainTextResponse
    from src.routes.traffic_agent_api import traffic_agent_api_router
    from src.traffic.agent_ui import AGENT_UI_HTML

    app = FastAPI(
        title="SA Traffic Agent",
        description="Pure executor for sa_tester's traffic engine. "
                    "Runs iperf3/RTP client+server sessions on behalf of a "
                    "remote orchestrator; does not call sa_core.")
    app.include_router(traffic_agent_api_router)

    @app.get("/", response_class=HTMLResponse, include_in_schema=False)
    def _ui():
        return AGENT_UI_HTML

    @app.get("/robots.txt", response_class=PlainTextResponse, include_in_schema=False)
    def _robots():
        return "User-agent: *\nDisallow: /\n"

    return app


def main(argv=None):
    p = argparse.ArgumentParser(description="SA Traffic Agent")
    p.add_argument("--bind", default="0.0.0.0", help="bind address (default: 0.0.0.0)")
    p.add_argument("--port", type=int, default=9100, help="listen port (default: 9100)")
    p.add_argument("--token", default=None,
                   help="shared secret; clients must send it as X-Agent-Token. "
                        "Overrides SA_AGENT_TOKEN env var.")
    p.add_argument("--log-level", default="INFO", help="log level (default: INFO)")
    args = p.parse_args(argv)

    if args.token is not None:
        os.environ["SA_AGENT_TOKEN"] = args.token

    logging.basicConfig(
        level=getattr(logging, args.log_level.upper(), logging.INFO),
        format="%(asctime)s %(levelname)s %(name)s: %(message)s")

    log = logging.getLogger("agent.main")
    log.info("starting traffic agent on %s:%d", args.bind, args.port)

    import uvicorn
    # Uvicorn's per-request access log (INFO) is spammy when the orchestrator
    # polls /sessions/{id} once per second. Only show it when --log-level=DEBUG;
    # at INFO it's suppressed entirely.
    enable_access_log = (args.log_level.upper() == "DEBUG")
    uvicorn.run(create_agent_app(), host=args.bind, port=args.port,
                log_level=args.log_level.lower(),
                access_log=enable_access_log)


if __name__ == "__main__":
    main()

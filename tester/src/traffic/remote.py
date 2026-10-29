# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Traffic Agent client — HTTP client the tester's orchestrator uses to drive
# a remote, tester-owned traffic engine on the DN / APN side.
#
# sa_core is never in the loop. Resolution is via the traffic_agents table.

import json
import logging
import time
import urllib.request

log = logging.getLogger("tester.traffic.remote")


class NoAgentConfigured(RuntimeError):
    """Raised when no traffic agent is registered and one is required."""


class TrafficAgent:
    """HTTP client for a remote TrafficEngine.

    Prefer the factories:
        TrafficAgent.default()      — the default registry entry
        TrafficAgent.for_dnn(dnn)   — look up by DNN, fall back to default
        TrafficAgent.for_id(id)     — look up by explicit id
    """

    def __init__(self, base_url: str, token: str = "", agent_id: str = "",
                 dnn: str = "", dn_ip: str = ""):
        self.base_url = base_url.rstrip('/')
        self.token = token or ""
        self.agent_id = agent_id
        self.dnn = dnn
        self.dn_ip = dn_ip

    # ── Factories ──

    @classmethod
    def from_row(cls, row: dict) -> 'TrafficAgent':
        if not row:
            raise NoAgentConfigured("no traffic agent row provided")
        url = (row.get("url") or "").strip()
        if not url:
            raise NoAgentConfigured(
                f"traffic agent {row.get('id')!r} has empty url")
        return cls(base_url=url,
                   token=row.get("token") or "",
                   agent_id=row.get("id") or "",
                   dnn=row.get("dnn") or "",
                   dn_ip=row.get("dn_ip") or "")

    @classmethod
    def default(cls) -> 'TrafficAgent':
        from src.db.crud.traffic_agents import agent_get_default
        row = agent_get_default()
        if not row:
            raise NoAgentConfigured(
                "no traffic agent configured — add one via traffic_agents CRUD "
                "or infra_config.traffic_engine_url (auto-migrated on startup)")
        return cls.from_row(row)

    @classmethod
    def for_dnn(cls, dnn: str) -> 'TrafficAgent':
        from src.db.crud.traffic_agents import agent_get_by_dnn
        row = agent_get_by_dnn(dnn)
        if not row:
            raise NoAgentConfigured(f"no traffic agent for dnn={dnn!r} (and no default)")
        return cls.from_row(row)

    @classmethod
    def for_id(cls, agent_id: str) -> 'TrafficAgent':
        from src.db.crud.traffic_agents import agent_get
        row = agent_get(agent_id)
        if not row:
            raise NoAgentConfigured(f"no traffic agent with id={agent_id!r}")
        return cls.from_row(row)

    # ── Session lifecycle ──

    def start(self, spec: dict) -> str:
        result = self._call("/api/traffic/start", "POST", spec, timeout=10)
        if not result:
            return None
        return result.get("session_id")

    def status(self, session_id: str) -> dict:
        return self._call(f"/api/traffic/sessions/{session_id}", "GET", timeout=5) or {}

    def stop(self, session_id: str) -> dict:
        result = self._call(f"/api/traffic/sessions/{session_id}/stop",
                            "POST", timeout=10)
        if not result:
            return {}
        return result.get("stats") or {}

    def wait(self, session_id: str, timeout: int) -> dict:
        # Poll every 0.3s instead of 1s — when iperf3 finishes at t=duration
        # the client is idle waiting for the next tick; 0.3s cuts worst-case
        # tail-latency by up to 700ms per session.
        deadline = time.time() + timeout
        while time.time() < deadline:
            s = self.status(session_id)
            if s.get("status") in ("completed", "error"):
                return s.get("stats") or {}
            time.sleep(0.3)
        log.warning("agent session %s wait timeout after %ds", session_id, timeout)
        return self.stop(session_id)

    # ── Introspection ──

    def capabilities(self) -> dict:
        return self._call("/api/traffic/capabilities", "GET", timeout=5) or {}

    def healthz(self) -> bool:
        return bool(self._call("/api/traffic/healthz", "GET", timeout=3))

    def dn_stats(self, iface: str = None) -> list:
        path = "/api/traffic/dn-stats"
        if iface:
            path += f"?iface={iface}"
        result = self._call(path, "GET", timeout=5) or {}
        return result.get("interfaces", [])

    # ── Internals ──

    def _call(self, path: str, method: str = "POST",
              body: dict = None, timeout: int = 10):
        url = f"{self.base_url}{path}"
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(url, data=data, method=method)
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if self.token:
            req.add_header("X-Agent-Token", self.token)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as resp:
                raw = resp.read()
                return json.loads(raw) if raw else {}
        except Exception as e:
            log.warning("agent %s %s failed: %s", method, url, e)
            return None

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Registry of tester-owned traffic engines on DN/APN-side hosts.

import logging
import os
import secrets
from typing import Dict, List, Optional

from src.db.engine import get_db

log = logging.getLogger("tester.db.traffic_agents")


def agent_list() -> List[Dict]:
    with get_db() as conn:
        rows = conn.execute(
            "SELECT * FROM traffic_agents ORDER BY is_default DESC, dnn, id"
        ).fetchall()
        return [dict(r) for r in rows]


def agent_get(agent_id: str) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute(
            "SELECT * FROM traffic_agents WHERE id=?", (agent_id,)
        ).fetchone()
        return dict(row) if row else None


def agent_get_default() -> Optional[Dict]:
    """Return the row flagged is_default=1, or the first one if none flagged."""
    with get_db() as conn:
        row = conn.execute(
            "SELECT * FROM traffic_agents WHERE is_default=1 LIMIT 1"
        ).fetchone()
        if row:
            return dict(row)
        row = conn.execute("SELECT * FROM traffic_agents ORDER BY id LIMIT 1").fetchone()
        return dict(row) if row else None


def agent_get_by_dnn(dnn: str) -> Optional[Dict]:
    """Find the first agent whose `dnn` matches. Falls back to default."""
    with get_db() as conn:
        row = conn.execute(
            "SELECT * FROM traffic_agents WHERE dnn=? LIMIT 1", (dnn,)
        ).fetchone()
        if row:
            return dict(row)
    return agent_get_default()


def agent_add(agent_id: str, url: str, dnn: str = "", dn_ip: str = "",
              token: str = "", is_default: bool = False, notes: str = "") -> Dict:
    if agent_get(agent_id):
        raise ValueError(f"traffic agent '{agent_id}' already exists")
    with get_db() as conn:
        if is_default:
            conn.execute("UPDATE traffic_agents SET is_default=0")
        conn.execute(
            "INSERT INTO traffic_agents (id, url, dnn, dn_ip, token, is_default, notes) "
            "VALUES (?, ?, ?, ?, ?, ?, ?)",
            (agent_id, url.rstrip('/'), dnn, dn_ip, token,
             1 if is_default else 0, notes))
        conn.commit()
    return agent_get(agent_id)


def agent_update(agent_id: str, updates: Dict) -> Dict:
    if not agent_get(agent_id):
        raise ValueError(f"traffic agent '{agent_id}' not found")
    fields = ["url", "dnn", "dn_ip", "token", "is_default", "notes"]
    set_clauses, values = [], []
    for f in fields:
        if f in updates:
            set_clauses.append(f"{f}=?")
            v = updates[f]
            if f == "is_default":
                v = 1 if v else 0
            if f == "url" and isinstance(v, str):
                v = v.rstrip('/')
            values.append(v)
    if not set_clauses:
        return agent_get(agent_id)
    with get_db() as conn:
        if updates.get("is_default"):
            conn.execute("UPDATE traffic_agents SET is_default=0 WHERE id<>?", (agent_id,))
        values.append(agent_id)
        conn.execute(
            f"UPDATE traffic_agents SET {', '.join(set_clauses)} WHERE id=?", values)
        conn.commit()
    return agent_get(agent_id)


def agent_delete(agent_id: str) -> bool:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM traffic_agents WHERE id=?", (agent_id,))
        conn.commit()
        return cur.rowcount > 0


def generate_token() -> str:
    """Suggest a token value for a new agent row."""
    return secrets.token_urlsafe(32)


# ── Migration: legacy infra_config.traffic_engine_url → traffic_agents row ──

def migrate_legacy_traffic_url() -> Optional[Dict]:
    """Copy infra_config.traffic_engine_url into traffic_agents as 'default'.

    Runs idempotently: only imports if the registry is currently empty AND the
    legacy URL is non-empty. The caller should invoke after ensure_schema().
    """
    try:
        existing = agent_list()
        if existing:
            return None
        from src.db.crud.infrastructure import infra_get
        legacy_url = (infra_get().get("traffic_engine_url") or "").strip()
        if not legacy_url:
            return None
        row = agent_add(agent_id="default", url=legacy_url,
                        dnn="", dn_ip="", token="", is_default=True,
                        notes="migrated from infra_config.traffic_engine_url")
        log.info("migrated traffic_engine_url → traffic_agents[default]: %s", legacy_url)
        return row
    except Exception as e:
        log.warning("traffic_engine_url migration failed: %s", e)
        return None


# ── Seed: default agent for the dockerized core-side slave ─────────────
# In the Docker stack (mmt-studio-orchestrate), `satraffic` runs the
# Python traffic-agent slave (`src.traffic.agent_main`) inside sacore's
# netns at 172.30.0.10:9100. The tester (master) needs a traffic_agents
# row with that URL so `TrafficAgent.default()` resolves automatically.
#
# Idempotent: only seeds if the registry is empty AND the legacy
# migration didn't already populate it. No auth — the slave runs with
# token unset (lab-only deployment), so the row's `token` is empty.
#
# Env:
#   SA_AGENT_DEFAULT_URL — base URL of the default slave
#                          (default: http://172.30.0.10:9100, the
#                          dockerized sacore bridge IP).
def seed_default_traffic_agent() -> Optional[Dict]:
    try:
        if agent_list():
            return None  # legacy migration or prior seed already populated
        url = os.environ.get("SA_AGENT_DEFAULT_URL",
                             "http://172.30.0.10:9100").strip()
        if not url:
            return None
        row = agent_add(
            agent_id="core-default",
            url=url,
            dnn="",
            dn_ip="",
            token="",
            is_default=True,
            notes="seeded for dockerized core-side satraffic slave; "
                  "override URL via SA_AGENT_DEFAULT_URL",
        )
        log.info("seeded default traffic agent → %s", url)
        return row
    except Exception as e:
        log.warning("default traffic agent seed failed: %s", e)
        return None

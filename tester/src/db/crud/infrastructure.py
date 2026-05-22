# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Infrastructure CRUD — multi-homing, AMF targets, SCTP addresses

import json
import subprocess
import logging
from typing import Dict, List, Optional
from src.db.engine import get_db

log = logging.getLogger("tester.db")


# ── Infra Config (single row) ──

def infra_get() -> Dict:
    """Get infrastructure config."""
    with get_db() as conn:
        row = conn.execute("SELECT * FROM infra_config WHERE id=1").fetchone()
        return dict(row) if row else {
            "multi_home_mode": "single", "sctp_heartbeat_s": 30,
            "sctp_max_retrans": 5, "gtpu_port": 2152, "qos_mode": "strict",
            "traffic_engine_url": "",
        }


def infra_update(updates: Dict) -> Dict:
    """Update infrastructure config."""
    fields = ["multi_home_mode", "sctp_heartbeat_s", "sctp_max_retrans", "gtpu_port", "qos_mode", "traffic_engine_url"]
    set_clauses, values = [], []
    for f in fields:
        if f in updates:
            set_clauses.append(f"{f}=?")
            values.append(updates[f])
    if set_clauses:
        with get_db() as conn:
            conn.execute(f"UPDATE infra_config SET {', '.join(set_clauses)} WHERE id=1", values)
            conn.commit()
    return infra_get()


# ── AMF Targets ──

def amf_list() -> List[Dict]:
    with get_db() as conn:
        return [dict(r) for r in conn.execute("SELECT * FROM amf_targets ORDER BY role, name").fetchall()]


def amf_get(name: str) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute("SELECT * FROM amf_targets WHERE name=?", (name,)).fetchone()
        return dict(row) if row else None


def amf_add(name: str, ip: str, port: int = 38412, role: str = "active", weight: int = 100) -> Dict:
    if amf_get(name):
        raise ValueError(f"AMF '{name}' already exists")
    with get_db() as conn:
        conn.execute("INSERT INTO amf_targets (name, ip, port, role, weight) VALUES (?, ?, ?, ?, ?)",
                     (name, ip, port, role, weight))
        conn.commit()
    return amf_get(name)


def amf_update(name: str, updates: Dict) -> Dict:
    if not amf_get(name):
        raise ValueError(f"AMF '{name}' not found")
    fields = ["ip", "port", "role", "weight"]
    set_clauses, values = [], []
    for f in fields:
        if f in updates:
            set_clauses.append(f"{f}=?")
            values.append(updates[f])
    if set_clauses:
        values.append(name)
        with get_db() as conn:
            conn.execute(f"UPDATE amf_targets SET {', '.join(set_clauses)} WHERE name=?", values)
            conn.commit()
    return amf_get(name)


def amf_delete(name: str) -> bool:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM amf_targets WHERE name=?", (name,))
        conn.commit()
        return cur.rowcount > 0


# ── SCTP Multi-Home Addresses ──

def sctp_addr_list(gnb_name: str = None) -> List[Dict]:
    with get_db() as conn:
        if gnb_name:
            rows = conn.execute("SELECT * FROM sctp_addresses WHERE gnb_name=? ORDER BY is_primary DESC", (gnb_name,)).fetchall()
        else:
            rows = conn.execute("SELECT * FROM sctp_addresses ORDER BY gnb_name, is_primary DESC").fetchall()
        return [dict(r) for r in rows]


def sctp_addr_add(gnb_name: str, ip: str, is_primary: bool = False) -> Dict:
    with get_db() as conn:
        conn.execute("INSERT OR REPLACE INTO sctp_addresses (gnb_name, ip, is_primary) VALUES (?, ?, ?)",
                     (gnb_name, ip, 1 if is_primary else 0))
        conn.commit()
    return {"gnb_name": gnb_name, "ip": ip, "is_primary": is_primary}


def sctp_addr_delete(gnb_name: str, ip: str) -> bool:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM sctp_addresses WHERE gnb_name=? AND ip=?", (gnb_name, ip))
        conn.commit()
        return cur.rowcount > 0


# ── AMF-gNB Assignments (for pool mode) ──

def amf_assignment_list() -> List[Dict]:
    with get_db() as conn:
        return [dict(r) for r in conn.execute(
            "SELECT * FROM amf_gnb_assignments ORDER BY amf_name, gnb_name").fetchall()]


def amf_assignment_set(amf_name: str, gnb_name: str) -> Dict:
    with get_db() as conn:
        conn.execute("INSERT OR REPLACE INTO amf_gnb_assignments (amf_name, gnb_name) VALUES (?, ?)",
                     (amf_name, gnb_name))
        conn.commit()
    return {"amf_name": amf_name, "gnb_name": gnb_name}


def amf_assignment_delete(amf_name: str, gnb_name: str) -> bool:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM amf_gnb_assignments WHERE amf_name=? AND gnb_name=?",
                           (amf_name, gnb_name))
        conn.commit()
        return cur.rowcount > 0


# ── Network Interfaces (read-only, from OS) ──

def get_interfaces() -> List[Dict]:
    """Get tester's network interfaces with IPs."""
    interfaces = []
    try:
        r = subprocess.run(['ip', '-4', '-o', 'addr', 'show'],
                           capture_output=True, text=True, timeout=5)
        for line in r.stdout.strip().split('\n'):
            if not line.strip():
                continue
            parts = line.split()
            iface = parts[1]
            ip = parts[3].split('/')[0]
            interfaces.append({"interface": iface, "ip": ip})
    except Exception:
        pass
    return interfaces


def get_active_tunnels() -> List[Dict]:
    """Get active GTP-U TUN interfaces."""
    tunnels = []
    try:
        r = subprocess.run(['ip', '-o', 'link', 'show', 'type', 'tun'],
                           capture_output=True, text=True, timeout=5)
        for line in r.stdout.strip().split('\n'):
            if not line.strip():
                continue
            parts = line.split(':')
            if len(parts) >= 2:
                name = parts[1].strip().split('@')[0]
                if name.startswith('tun-ue-'):
                    tunnels.append({"name": name})
    except Exception:
        pass
    return tunnels

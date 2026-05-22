# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# gNB Config CRUD

import json
from typing import Dict, List, Optional
from src.db.engine import get_db


def gnb_list() -> List[Dict]:
    with get_db() as conn:
        rows = conn.execute("SELECT * FROM gnb_config ORDER BY gnb_name").fetchall()
        result = []
        for r in rows:
            d = dict(r)
            try:
                d["slices"] = json.loads(d.pop("slices_json", "[]"))
            except Exception:
                d["slices"] = []
            result.append(d)
        return result


def gnb_get(gnb_name: str) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute("SELECT * FROM gnb_config WHERE gnb_name=?", (gnb_name,)).fetchone()
        if not row:
            return None
        d = dict(row)
        try:
            d["slices"] = json.loads(d.pop("slices_json", "[]"))
        except Exception:
            d["slices"] = []
        return d


def gnb_add(entry: Dict) -> Dict:
    name = entry.get("gnb_name", "").strip()
    if not name:
        raise ValueError("gnb_name required")
    if gnb_get(name):
        raise ValueError(f"gNB '{name}' already exists")
    slices_json = json.dumps(entry.get("slices", []))
    with get_db() as conn:
        conn.execute("""
            INSERT INTO gnb_config (gnb_name, gnb_id, gnb_ip, interface,
                                     amf_ip, amf_port, mcc, mnc, tac, paging_drx, slices_json)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """, (name, entry.get("gnb_id", ""), entry.get("gnb_ip", ""),
              entry.get("interface", ""), entry.get("amf_ip", ""),
              entry.get("amf_port", 38412), entry.get("mcc", ""),
              entry.get("mnc", ""), entry.get("tac", ""),
              entry.get("paging_drx", "v128"), slices_json))
        conn.commit()
    return gnb_get(name)


def gnb_update(gnb_name: str, updates: Dict) -> Dict:
    if not gnb_get(gnb_name):
        raise ValueError(f"gNB '{gnb_name}' not found")
    fields_map = {"gnb_id": "gnb_id", "gnb_ip": "gnb_ip", "interface": "interface",
                  "amf_ip": "amf_ip", "amf_port": "amf_port",
                  "mcc": "mcc", "mnc": "mnc", "tac": "tac", "paging_drx": "paging_drx"}
    set_clauses, values = [], []
    for k, col in fields_map.items():
        if k in updates:
            set_clauses.append(f"{col}=?")
            values.append(updates[k])
    if "slices" in updates:
        set_clauses.append("slices_json=?")
        values.append(json.dumps(updates["slices"]))
    if set_clauses:
        values.append(gnb_name)
        with get_db() as conn:
            conn.execute(f"UPDATE gnb_config SET {', '.join(set_clauses)} WHERE gnb_name=?", values)
            conn.commit()
    return gnb_get(gnb_name)


def gnb_delete(gnb_name: str) -> bool:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM gnb_config WHERE gnb_name=?", (gnb_name,))
        conn.commit()
        return cur.rowcount > 0


def gnb_clone(source_name: str, new_name: str) -> Dict:
    source = gnb_get(source_name)
    if not source:
        raise ValueError(f"Source gNB '{source_name}' not found")
    if gnb_get(new_name):
        raise ValueError(f"gNB '{new_name}' already exists")
    entry = dict(source)
    entry.pop("id", None)
    entry["gnb_name"] = new_name
    return gnb_add(entry)

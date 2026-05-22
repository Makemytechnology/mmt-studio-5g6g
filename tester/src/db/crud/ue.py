# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# UE CRUD (mirrors sa_core/db/crud/ue.py)

import logging
from typing import Dict, List, Optional
from src.db.engine import get_db

log = logging.getLogger("tester.db")


def ue_list(gnb_name: Optional[str] = None) -> List[Dict]:
    with get_db() as conn:
        if gnb_name:
            rows = conn.execute("SELECT * FROM ue WHERE gnb_name=? ORDER BY imsi", (gnb_name,)).fetchall()
        else:
            rows = conn.execute("SELECT * FROM ue ORDER BY imsi").fetchall()
        return [dict(r) for r in rows]


def ue_get(imsi: str) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute("SELECT * FROM ue WHERE imsi=?", (imsi,)).fetchone()
        return dict(row) if row else None


def ue_add(entry: Dict) -> Dict:
    imsi = entry.get("imsi", "").strip()
    if not imsi:
        raise ValueError("IMSI required")
    if ue_get(imsi):
        raise ValueError(f"IMSI {imsi} already exists")
    with get_db() as conn:
        conn.execute("""
            INSERT INTO ue (imsi, msisdn, k, opc, op_type, sqn, amf, gnb_name,
                            identity_type, routing_indicator, protection_scheme,
                            hn_pub_key_id, hn_pub_key)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """, (
            imsi, entry.get("msisdn", ""),
            entry["k"], entry["opc"],
            entry.get("op_type", "OPC"), entry.get("sqn", 0),
            entry.get("amf", "8000"), entry.get("gnb_name", ""),
            entry.get("identity_type", entry.get("supi_type", "supi")),
            entry.get("routing_indicator", "0000"),
            entry.get("protection_scheme", 0),
            entry.get("hn_pub_key_id", 0),
            entry.get("hn_pub_key", ""),
        ))
        conn.commit()
    return ue_get(imsi)


def ue_update(imsi: str, updates: Dict) -> Dict:
    existing = ue_get(imsi)
    if not existing:
        raise ValueError(f"IMSI {imsi} not found")
    fields = ["msisdn", "k", "opc", "op_type", "sqn", "amf", "gnb_name",
              "identity_type", "routing_indicator", "protection_scheme",
              "hn_pub_key_id", "hn_pub_key"]
    set_clauses = []
    values = []
    for f in fields:
        val = updates.get(f)
        if val is None and f == "identity_type":
            val = updates.get("supi_type")
        if val is not None:
            set_clauses.append(f"{f}=?")
            values.append(val)
    if not set_clauses:
        return existing
    values.append(imsi)
    with get_db() as conn:
        conn.execute(f"UPDATE ue SET {', '.join(set_clauses)} WHERE imsi=?", values)
        conn.commit()
    return ue_get(imsi)


def ue_delete(imsi: str) -> bool:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM ue WHERE imsi=?", (imsi,))
        conn.commit()
        return cur.rowcount > 0


def ue_clone(source_imsi: str, new_imsi: str) -> Dict:
    source = ue_get(source_imsi)
    if not source:
        raise ValueError(f"Source IMSI {source_imsi} not found")
    if ue_get(new_imsi):
        raise ValueError(f"Target IMSI {new_imsi} already exists")
    entry = dict(source)
    entry.pop("id", None)
    entry["imsi"] = new_imsi
    return ue_add(entry)


def ue_import_bulk(entries: List[Dict], overwrite: bool = False) -> int:
    count = 0
    for entry in entries:
        imsi = entry.get("imsi", "").strip()
        if not imsi:
            continue
        if ue_get(imsi):
            if overwrite:
                ue_update(imsi, entry)
                count += 1
        else:
            try:
                ue_add(entry)
                count += 1
            except Exception:
                pass
    return count


def ue_count() -> int:
    with get_db() as conn:
        row = conn.execute("SELECT COUNT(*) FROM ue").fetchone()
        return row[0] if row else 0

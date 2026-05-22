# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Core sync state CRUD

import time
from typing import Dict, List, Optional
from src.db.engine import get_db


def sync_mark(imsi: str, status: str = "provisioned"):
    with get_db() as conn:
        conn.execute("""
            INSERT INTO core_sync (imsi, synced_at, core_status) VALUES (?, ?, ?)
            ON CONFLICT(imsi) DO UPDATE SET synced_at=?, core_status=?
        """, (imsi, time.time(), status, time.time(), status))
        conn.commit()


def sync_status(imsi: str) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute("SELECT * FROM core_sync WHERE imsi=?", (imsi,)).fetchone()
        return dict(row) if row else None


def sync_pending() -> List[str]:
    with get_db() as conn:
        synced = {r[0] for r in conn.execute(
            "SELECT imsi FROM core_sync WHERE core_status='provisioned'").fetchall()}
        all_ues = {r[0] for r in conn.execute("SELECT imsi FROM ue").fetchall()}
        return sorted(all_ues - synced)

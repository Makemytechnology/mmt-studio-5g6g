# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Test Results CRUD

import json
import time
from typing import Dict, List, Optional
from src.db.engine import get_db


def result_save(test_name: str, tc_id: str = "", category: str = "",
                status: str = "PASS", duration_ms: float = 0,
                details: Dict = None, error: str = None,
                upf_stats: Dict = None, logs: list = None,
                run_id: str = None) -> int:
    with get_db() as conn:
        cur = conn.execute("""
            INSERT INTO test_results (run_id, test_name, tc_id, category, status,
                                       duration_ms, timestamp, details_json, error,
                                       upf_stats_json, logs_json)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """, (run_id, test_name, tc_id, category, status, duration_ms,
              time.strftime("%Y-%m-%d %H:%M:%S"),
              json.dumps(details) if details else None, error,
              json.dumps(upf_stats) if upf_stats else None,
              json.dumps(logs[-100:]) if logs else None))
        conn.commit()
        return cur.lastrowid


def result_list(limit: int = 50, status: str = None, test_name: str = None) -> List[Dict]:
    query = "SELECT id, run_id, test_name, tc_id, category, status, duration_ms, timestamp, error FROM test_results"
    conditions, values = [], []
    if status:
        conditions.append("status=?")
        values.append(status)
    if test_name:
        conditions.append("test_name LIKE ?")
        values.append(f"%{test_name}%")
    if conditions:
        query += " WHERE " + " AND ".join(conditions)
    query += " ORDER BY id DESC LIMIT ?"
    values.append(limit)
    with get_db() as conn:
        return [dict(r) for r in conn.execute(query, values).fetchall()]


def result_get(result_id: int) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute("SELECT * FROM test_results WHERE id=?", (result_id,)).fetchone()
        if not row:
            return None
        d = dict(row)
        for jf in ("details_json", "upf_stats_json", "protocol_trace_json", "logs_json"):
            if d.get(jf):
                try:
                    d[jf.replace("_json", "")] = json.loads(d[jf])
                except Exception:
                    pass
        return d


def result_stats() -> Dict:
    with get_db() as conn:
        total = conn.execute("SELECT COUNT(*) FROM test_results").fetchone()[0]
        passed = conn.execute("SELECT COUNT(*) FROM test_results WHERE status='PASS'").fetchone()[0]
        failed = conn.execute("SELECT COUNT(*) FROM test_results WHERE status='FAIL'").fetchone()[0]
        return {"total": total, "passed": passed, "failed": failed, "error": total - passed - failed}

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test Run Manager — create, track, and query test execution sessions."""

import uuid
import time
import json
import logging
from typing import Dict, List, Optional

from src.db.schema import get_db

log = logging.getLogger("tester.runs")


def create_run(run_type="single", trigger="manual", suites=None, notes=None) -> str:
    """Create a new test run. Returns run_id (UUID)."""
    run_id = str(uuid.uuid4())[:8]
    with get_db() as conn:
        conn.execute("""
            INSERT INTO test_runs (id, run_type, status, trigger, started_at, suites, notes)
            VALUES (?, ?, 'running', ?, ?, ?, ?)
        """, (run_id, run_type, trigger, time.time(),
              json.dumps(suites) if suites else None, notes))
        conn.commit()
    log.info("Test run created: %s (%s, %s)", run_id, run_type, trigger)
    return run_id


def complete_run(run_id: str, status="completed"):
    """Mark a test run as completed and compute summary."""
    with get_db() as conn:
        rows = conn.execute(
            "SELECT status, COUNT(*) FROM test_results WHERE run_id=? GROUP BY status",
            (run_id,)).fetchall()
        counts = {r[0]: r[1] for r in rows}
        total = sum(counts.values())
        duration_row = conn.execute(
            "SELECT SUM(duration_ms) FROM test_results WHERE run_id=?", (run_id,)).fetchone()
        duration = duration_row[0] if duration_row else 0

        conn.execute("""
            UPDATE test_runs SET status=?, completed_at=?, total=?, passed=?, failed=?,
                error=?, skipped=?, duration_ms=?
            WHERE id=?
        """, (status, time.time(), total, counts.get('PASS', 0), counts.get('FAIL', 0),
              counts.get('ERROR', 0), counts.get('SKIP', 0), duration, run_id))
        conn.commit()
    log.info("Run %s completed: %d total, %d pass, %d fail",
             run_id, total, counts.get('PASS', 0), counts.get('FAIL', 0))


def save_result(run_id: str, test_name: str, tc_id: str, suite: str, category: str,
                status: str, duration_ms: float, error: str = None,
                details: Dict = None, upf_stats: Dict = None,
                protocol_trace: list = None, logs: list = None) -> int:
    """Save a test result linked to a run. Returns result_id."""
    with get_db() as conn:
        cur = conn.execute("""
            INSERT INTO test_results (run_id, test_name, tc_id, suite, category, status,
                                       duration_ms, timestamp, error, details_json,
                                       upf_stats_json, protocol_trace_json, logs_json)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        """, (run_id, test_name, tc_id, suite, category, status, duration_ms,
              time.strftime("%Y-%m-%d %H:%M:%S"), error,
              json.dumps(details) if details else None,
              json.dumps(upf_stats) if upf_stats else None,
              json.dumps(protocol_trace[:50]) if protocol_trace else None,
              json.dumps(logs[-100:]) if logs else None))
        conn.commit()
        return cur.lastrowid


def save_metrics(result_id: int, metrics: Dict[str, tuple]):
    """Save performance metrics for a test result.

    metrics: {"throughput_mbps": (45.2, "mbps"), "mos": (4.23, "score"), ...}
    """
    with get_db() as conn:
        for name, (value, unit) in metrics.items():
            if value is not None:
                conn.execute(
                    "INSERT INTO test_metrics (result_id, metric_name, metric_value, unit) VALUES (?, ?, ?, ?)",
                    (result_id, name, value, unit))
        conn.commit()


def get_run(run_id: str) -> Optional[Dict]:
    """Get test run with all results."""
    with get_db() as conn:
        run = conn.execute("SELECT * FROM test_runs WHERE id=?", (run_id,)).fetchone()
        if not run:
            return None
        d = dict(run)
        results = conn.execute(
            "SELECT id, test_name, tc_id, suite, category, status, duration_ms, error "
            "FROM test_results WHERE run_id=? ORDER BY id", (run_id,)).fetchall()
        d["results"] = [dict(r) for r in results]
        return d


def list_runs(limit=20, run_type=None) -> List[Dict]:
    """List recent test runs."""
    query = "SELECT * FROM test_runs"
    values = []
    if run_type:
        query += " WHERE run_type=?"
        values.append(run_type)
    query += " ORDER BY started_at DESC LIMIT ?"
    values.append(limit)
    with get_db() as conn:
        return [dict(r) for r in conn.execute(query, values).fetchall()]


def get_result_detail(result_id: int) -> Optional[Dict]:
    """Get full test result with metrics."""
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
        metrics = conn.execute(
            "SELECT metric_name, metric_value, unit FROM test_metrics WHERE result_id=?",
            (result_id,)).fetchall()
        d["metrics"] = {r[0]: {"value": r[1], "unit": r[2]} for r in metrics}
        return d

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test Analysis Engine — trends, regressions, flaky detection, metrics."""

import json
import logging
from typing import Dict, List, Optional, Tuple

from src.db.schema import get_db

log = logging.getLogger("tester.analysis")


def pass_rate(last_n: int = 10) -> List[Dict]:
    """Pass rate per run for the last N runs."""
    with get_db() as conn:
        runs = conn.execute("""
            SELECT id, run_type, total, passed, failed, error, started_at, duration_ms
            FROM test_runs WHERE status='completed'
            ORDER BY started_at DESC LIMIT ?
        """, (last_n,)).fetchall()
        result = []
        for r in runs:
            total = r[2] or 1
            result.append({
                "run_id": r[0], "run_type": r[1],
                "total": r[2], "passed": r[3], "failed": r[4], "error": r[5],
                "pass_rate": round(r[3] / total * 100, 1) if total else 0,
                "started_at": r[6], "duration_ms": r[7],
            })
        return list(reversed(result))


def metric_trend(test_name: str, metric_name: str, last_n: int = 20) -> List[Dict]:
    """Performance metric trend for a specific test over the last N runs."""
    with get_db() as conn:
        rows = conn.execute("""
            SELECT tr.timestamp, tm.metric_value, tm.unit, tr.run_id
            FROM test_metrics tm
            JOIN test_results tr ON tr.id = tm.result_id
            WHERE tr.test_name = ? AND tm.metric_name = ? AND tr.status = 'PASS'
            ORDER BY tr.id DESC LIMIT ?
        """, (test_name, metric_name, last_n)).fetchall()
        return [{"timestamp": r[0], "value": r[1], "unit": r[2], "run_id": r[3]}
                for r in reversed(rows)]


def regressions(current_run_id: str, previous_run_id: str = None) -> List[Dict]:
    """Find tests that passed in previous run but failed in current run."""
    with get_db() as conn:
        if not previous_run_id:
            # Find the run before current
            current = conn.execute(
                "SELECT started_at FROM test_runs WHERE id=?", (current_run_id,)).fetchone()
            if not current:
                return []
            prev = conn.execute("""
                SELECT id FROM test_runs WHERE status='completed' AND started_at < ?
                ORDER BY started_at DESC LIMIT 1
            """, (current[0],)).fetchone()
            if not prev:
                return []
            previous_run_id = prev[0]

        rows = conn.execute("""
            SELECT c.test_name, c.tc_id, c.status AS current_status,
                   p.status AS previous_status, c.error
            FROM test_results c
            JOIN test_results p ON p.test_name = c.test_name AND p.run_id = ?
            WHERE c.run_id = ? AND c.status IN ('FAIL','ERROR') AND p.status = 'PASS'
        """, (previous_run_id, current_run_id)).fetchall()

        return [{"test_name": r[0], "tc_id": r[1], "current": r[2],
                 "previous": r[3], "error": r[4]} for r in rows]


def flaky_tests(last_n: int = 10, threshold: int = 2) -> List[Dict]:
    """Find tests that alternate PASS/FAIL across recent runs.

    A test is flaky if it changes status more than `threshold` times
    in the last `last_n` runs.
    """
    with get_db() as conn:
        # Get last N run IDs
        runs = conn.execute("""
            SELECT id FROM test_runs WHERE status='completed'
            ORDER BY started_at DESC LIMIT ?
        """, (last_n,)).fetchall()
        if len(runs) < 2:
            return []
        run_ids = [r[0] for r in runs]
        placeholders = ",".join(["?"] * len(run_ids))

        # Get all results for these runs
        rows = conn.execute(f"""
            SELECT test_name, run_id, status
            FROM test_results WHERE run_id IN ({placeholders})
            ORDER BY test_name, id
        """, run_ids).fetchall()

        # Count status changes per test
        from collections import defaultdict
        test_statuses = defaultdict(list)
        for r in rows:
            test_statuses[r[0]].append(r[2])

        flaky = []
        for test_name, statuses in test_statuses.items():
            changes = sum(1 for i in range(1, len(statuses)) if statuses[i] != statuses[i-1])
            if changes >= threshold:
                flaky.append({
                    "test_name": test_name,
                    "status_changes": changes,
                    "recent_statuses": statuses[-last_n:],
                    "last_status": statuses[-1],
                })
        return sorted(flaky, key=lambda x: -x["status_changes"])


def suite_summary(suite_name: str = None, run_id: str = None) -> List[Dict]:
    """Summary per suite (or all suites) for a specific run or latest."""
    with get_db() as conn:
        if not run_id:
            r = conn.execute(
                "SELECT id FROM test_runs WHERE status='completed' ORDER BY started_at DESC LIMIT 1"
            ).fetchone()
            if not r:
                return []
            run_id = r[0]

        query = """
            SELECT suite, status, COUNT(*) as cnt, AVG(duration_ms) as avg_dur
            FROM test_results WHERE run_id=?
        """
        values = [run_id]
        if suite_name:
            query += " AND suite=?"
            values.append(suite_name)
        query += " GROUP BY suite, status ORDER BY suite"

        rows = conn.execute(query, values).fetchall()

        # Aggregate by suite
        from collections import defaultdict
        suites = defaultdict(lambda: {"total": 0, "passed": 0, "failed": 0, "error": 0, "avg_ms": 0})
        for r in rows:
            s = suites[r[0] or "unknown"]
            s["total"] += r[2]
            if r[1] == "PASS":
                s["passed"] += r[2]
            elif r[1] == "FAIL":
                s["failed"] += r[2]
            else:
                s["error"] += r[2]
            s["avg_ms"] = round(r[3] or 0)

        return [{"suite": k, **v, "pass_rate": round(v["passed"] / max(v["total"], 1) * 100, 1)}
                for k, v in sorted(suites.items())]


def failure_heatmap(last_n: int = 10) -> List[Dict]:
    """Which tests fail most frequently across recent runs."""
    with get_db() as conn:
        runs = conn.execute("""
            SELECT id FROM test_runs WHERE status='completed'
            ORDER BY started_at DESC LIMIT ?
        """, (last_n,)).fetchall()
        if not runs:
            return []
        run_ids = [r[0] for r in runs]
        placeholders = ",".join(["?"] * len(run_ids))

        rows = conn.execute(f"""
            SELECT test_name, tc_id,
                   SUM(CASE WHEN status='FAIL' THEN 1 ELSE 0 END) as fail_count,
                   SUM(CASE WHEN status='PASS' THEN 1 ELSE 0 END) as pass_count,
                   COUNT(*) as total
            FROM test_results WHERE run_id IN ({placeholders})
            GROUP BY test_name HAVING fail_count > 0
            ORDER BY fail_count DESC
        """, run_ids).fetchall()

        return [{"test_name": r[0], "tc_id": r[1], "fail_count": r[2],
                 "pass_count": r[3], "total": r[4],
                 "fail_rate": round(r[2] / max(r[4], 1) * 100, 1)}
                for r in rows]


def compare_runs(run_a: str, run_b: str) -> Dict:
    """Compare two runs — show regressions, improvements, new failures."""
    with get_db() as conn:
        a_results = {r[0]: r[1] for r in conn.execute(
            "SELECT test_name, status FROM test_results WHERE run_id=?", (run_a,)).fetchall()}
        b_results = {r[0]: r[1] for r in conn.execute(
            "SELECT test_name, status FROM test_results WHERE run_id=?", (run_b,)).fetchall()}

    regressions_list = []
    improvements = []
    stable_pass = 0
    stable_fail = 0

    for test, status_b in b_results.items():
        status_a = a_results.get(test)
        if not status_a:
            continue
        if status_a == "PASS" and status_b in ("FAIL", "ERROR"):
            regressions_list.append({"test": test, "was": status_a, "now": status_b})
        elif status_a in ("FAIL", "ERROR") and status_b == "PASS":
            improvements.append({"test": test, "was": status_a, "now": status_b})
        elif status_a == "PASS" and status_b == "PASS":
            stable_pass += 1
        elif status_a in ("FAIL", "ERROR") and status_b in ("FAIL", "ERROR"):
            stable_fail += 1

    return {
        "run_a": run_a, "run_b": run_b,
        "regressions": regressions_list,
        "improvements": improvements,
        "stable_pass": stable_pass,
        "stable_fail": stable_fail,
    }

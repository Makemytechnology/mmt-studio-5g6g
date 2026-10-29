# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Controller — orchestrates distributed test execution
#
# Responsibilities:
#   - Manage worker registration and health
#   - Distribute gNB ranges to workers
#   - Coordinate test runs across workers
#   - Aggregate results and metrics
#   - Generate reports from aggregated data

import time
import uuid
import json
import logging
import urllib.request
from typing import Dict, List, Optional

log = logging.getLogger("tester.controller")


class WorkerNode:
    """Represents a connected worker."""
    __slots__ = ('node_id', 'ip', 'port', 'gnb_start', 'gnb_count',
                 'status', 'last_heartbeat', 'metrics')

    def __init__(self, node_id, ip, port, gnb_start=0, gnb_count=0):
        self.node_id = node_id
        self.ip = ip
        self.port = port
        self.gnb_start = gnb_start
        self.gnb_count = gnb_count
        self.status = "registered"  # registered | running | idle | offline
        self.last_heartbeat = time.time()
        self.metrics = {}


class Controller:
    """Test execution controller for distributed testing."""

    def __init__(self):
        self.workers: Dict[str, WorkerNode] = {}
        self.runs: Dict[str, dict] = {}
        self._auto_assign_counter = 0

    def register_worker(self, node_id: str, ip: str, port: int,
                        gnb_count: int = 10000) -> dict:
        """Worker calls this to register itself."""
        gnb_start = self._auto_assign_counter
        self._auto_assign_counter += gnb_count

        worker = WorkerNode(node_id, ip, port, gnb_start, gnb_count)
        self.workers[node_id] = worker

        log.info("Worker registered: %s (%s:%d) gNBs %d-%d",
                 node_id, ip, port, gnb_start, gnb_start + gnb_count - 1)

        return {
            "ok": True,
            "node_id": node_id,
            "gnb_start": gnb_start,
            "gnb_count": gnb_count,
            "total_workers": len(self.workers),
        }

    def worker_heartbeat(self, node_id: str, metrics: dict = None) -> dict:
        """Worker sends periodic heartbeat with metrics."""
        worker = self.workers.get(node_id)
        if not worker:
            return {"ok": False, "error": "unknown worker"}
        worker.last_heartbeat = time.time()
        worker.status = "idle"
        if metrics:
            worker.metrics = metrics
        return {"ok": True}

    def get_workers(self) -> List[dict]:
        """List all workers with status."""
        now = time.time()
        result = []
        for w in self.workers.values():
            # Mark offline if no heartbeat for 30s
            if now - w.last_heartbeat > 30:
                w.status = "offline"
            result.append({
                "node_id": w.node_id, "ip": w.ip, "port": w.port,
                "gnb_start": w.gnb_start, "gnb_count": w.gnb_count,
                "status": w.status, "last_heartbeat": w.last_heartbeat,
                "metrics": w.metrics,
            })
        return result

    def start_run(self, run_type: str = "regression", test_names: list = None,
                  params: dict = None) -> dict:
        """Start a distributed test run across all workers."""
        run_id = str(uuid.uuid4())[:8]
        active_workers = [w for w in self.workers.values() if w.status != "offline"]

        if not active_workers:
            return {"ok": False, "error": "no active workers"}

        run = {
            "id": run_id,
            "type": run_type,
            "status": "starting",
            "started_at": time.time(),
            "workers": [w.node_id for w in active_workers],
            "test_names": test_names,
            "params": params or {},
            "results": {},
        }
        self.runs[run_id] = run

        # Send start command to each worker
        for worker in active_workers:
            try:
                self._send_worker_command(worker, "start", {
                    "run_id": run_id,
                    "run_type": run_type,
                    "test_names": test_names,
                    "params": params,
                })
                worker.status = "running"
            except Exception as e:
                log.warning("Failed to start worker %s: %s", worker.node_id, e)
                run["results"][worker.node_id] = {"status": "failed", "error": str(e)}

        run["status"] = "running"
        log.info("Run %s started: %d workers, type=%s", run_id, len(active_workers), run_type)
        return {"ok": True, "run_id": run_id, "workers": len(active_workers)}

    def worker_report_result(self, node_id: str, run_id: str, results: dict) -> dict:
        """Worker reports its test results for a run."""
        run = self.runs.get(run_id)
        if not run:
            return {"ok": False, "error": "unknown run"}
        run["results"][node_id] = results

        # Check if all workers reported
        expected = set(run.get("workers", []))
        reported = set(run["results"].keys())
        if reported >= expected:
            run["status"] = "completed"
            run["completed_at"] = time.time()
            log.info("Run %s completed: all %d workers reported", run_id, len(reported))

        return {"ok": True}

    def get_run(self, run_id: str) -> Optional[dict]:
        """Get run status with aggregated results."""
        run = self.runs.get(run_id)
        if not run:
            return None
        # Aggregate
        total = passed = failed = 0
        for node_results in run.get("results", {}).values():
            if isinstance(node_results, dict):
                total += node_results.get("total", 0)
                passed += node_results.get("passed", 0)
                failed += node_results.get("failed", 0)
        run["aggregate"] = {"total": total, "passed": passed, "failed": failed}
        return run

    def _send_worker_command(self, worker: WorkerNode, command: str, payload: dict):
        """Send command to worker via REST API."""
        url = f"http://{worker.ip}:{worker.port}/api/worker/{command}"
        data = json.dumps(payload).encode()
        req = urllib.request.Request(url, data=data, method="POST")
        req.add_header("Content-Type", "application/json")
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())

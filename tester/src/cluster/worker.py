# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Worker — executes tests for assigned gNB range
#
# Each worker:
#   - Registers with controller on startup
#   - Receives gNB range assignment (e.g., gNBs 0-9999)
#   - Creates gNB state machines for its range
#   - Generates UEs per gNB (active + idle)
#   - Executes test commands from controller
#   - Reports results and metrics back

import time
import json
import threading
import logging
import urllib.request
from typing import Dict, List

log = logging.getLogger("tester.worker")


class Worker:
    """Test execution worker for distributed testing."""

    def __init__(self, node_id: str, controller_url: str,
                 gnb_count: int = 10000, ues_active: int = 1000, ues_idle: int = 10000):
        self.node_id = node_id
        self.controller_url = controller_url
        self.gnb_count = gnb_count
        self.ues_active = ues_active
        self.ues_idle = ues_idle

        # Assigned by controller
        self.gnb_start = 0
        self.registered = False

        # State
        self.status = "idle"  # idle | running | error
        self.current_run = None
        self.metrics = {
            "gnbs_active": 0,
            "ues_registered": 0,
            "ues_connected": 0,
            "registrations_per_sec": 0,
            "calls_active": 0,
            "errors": 0,
        }

        # Heartbeat thread
        self._stop = threading.Event()
        self._heartbeat_thread = None

    def register(self) -> bool:
        """Register with controller and get gNB range assignment."""
        try:
            result = self._controller_api("/api/controller/register", {
                "node_id": self.node_id,
                "ip": self._get_local_ip(),
                "port": 5001,
                "gnb_count": self.gnb_count,
            })
            if result and result.get("ok"):
                self.gnb_start = result["gnb_start"]
                self.registered = True
                log.info("Registered with controller: gNBs %d-%d",
                         self.gnb_start, self.gnb_start + self.gnb_count - 1)
                self._start_heartbeat()
                return True
            log.warning("Registration failed: %s", result)
            return False
        except Exception as e:
            log.warning("Cannot reach controller at %s: %s", self.controller_url, e)
            return False

    def handle_start(self, run_id: str, run_type: str,
                     test_names: list = None, params: dict = None) -> dict:
        """Controller tells this worker to start a test run."""
        self.status = "running"
        self.current_run = run_id
        log.info("Starting run %s (type=%s) for gNBs %d-%d",
                 run_id, run_type, self.gnb_start, self.gnb_start + self.gnb_count - 1)

        # Run in background thread
        t = threading.Thread(target=self._execute_run,
                             args=(run_id, run_type, test_names, params),
                             daemon=True, name=f"run-{run_id}")
        t.start()

        return {"ok": True, "run_id": run_id, "status": "started"}

    def handle_stop(self, run_id: str) -> dict:
        """Controller tells this worker to stop."""
        self.status = "idle"
        self.current_run = None
        return {"ok": True}

    def _execute_run(self, run_id, run_type, test_names, params):
        """Execute the assigned test run."""
        start = time.time()
        total = passed = failed = 0

        try:
            # For each gNB in our range, run the test
            for gnb_idx in range(self.gnb_start, self.gnb_start + self.gnb_count):
                if self.status != "running":
                    break

                # Simulate gNB registration + UE procedures
                gnb_result = self._run_gnb_tests(gnb_idx, test_names, params)
                total += gnb_result.get("total", 0)
                passed += gnb_result.get("passed", 0)
                failed += gnb_result.get("failed", 0)

                # Update metrics
                self.metrics["gnbs_active"] = gnb_idx - self.gnb_start + 1
                self.metrics["ues_registered"] = total

                # Log progress every 100 gNBs
                if (gnb_idx - self.gnb_start) % 100 == 0:
                    elapsed = time.time() - start
                    rate = total / max(elapsed, 1)
                    log.info("Progress: %d/%d gNBs, %d UEs, %.0f reg/s",
                             gnb_idx - self.gnb_start, self.gnb_count, total, rate)

        except Exception as e:
            log.error("Run %s failed: %s", run_id, e)
            self.metrics["errors"] += 1

        # Report results to controller
        duration_ms = (time.time() - start) * 1000
        results = {
            "node_id": self.node_id,
            "total": total, "passed": passed, "failed": failed,
            "duration_ms": duration_ms,
            "gnb_range": [self.gnb_start, self.gnb_start + self.gnb_count - 1],
        }

        try:
            self._controller_api("/api/controller/result", {
                "node_id": self.node_id,
                "run_id": run_id,
                "results": results,
            })
        except Exception as e:
            log.warning("Failed to report results to controller: %s", e)

        self.status = "idle"
        self.current_run = None
        log.info("Run %s complete: %d total, %d pass, %d fail (%.1fs)",
                 run_id, total, passed, failed, duration_ms / 1000)

    def _run_gnb_tests(self, gnb_idx, test_names, params) -> dict:
        """Run tests for a single gNB.

        Override this in subclass for actual test execution.
        Base implementation delegates to existing test runner.
        """
        # Import here to avoid circular imports
        from src.testcases.test_runner import TestRunner

        runner = TestRunner()
        gnb_pool = []
        ue_pool = []
        total = passed = failed = 0

        for name in (test_names or list(runner._registry.keys())[:1]):
            result = runner.run_test(name, gnb_pool, ue_pool, params)
            if result:
                total += 1
                if result.status == "PASS":
                    passed += 1
                else:
                    failed += 1

        return {"total": total, "passed": passed, "failed": failed}

    def _start_heartbeat(self):
        """Start background heartbeat to controller."""
        def _heartbeat_loop():
            while not self._stop.is_set():
                try:
                    self._controller_api("/api/controller/heartbeat", {
                        "node_id": self.node_id,
                        "metrics": self.metrics,
                    })
                except Exception:
                    pass
                self._stop.wait(timeout=10)

        self._heartbeat_thread = threading.Thread(target=_heartbeat_loop,
                                                   daemon=True, name="heartbeat")
        self._heartbeat_thread.start()

    def stop(self):
        """Stop worker."""
        self._stop.set()
        self.status = "stopped"

    def _controller_api(self, path, body):
        """Call controller REST API."""
        url = f"{self.controller_url}{path}"
        data = json.dumps(body).encode()
        req = urllib.request.Request(url, data=data, method="POST")
        req.add_header("Content-Type", "application/json")
        with urllib.request.urlopen(req, timeout=10) as resp:
            return json.loads(resp.read())

    def _get_local_ip(self):
        """Get this machine's IP address."""
        import socket
        try:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
            s.connect(("8.8.8.8", 80))
            ip = s.getsockname()[0]
            s.close()
            return ip
        except Exception:
            return "127.0.0.1"

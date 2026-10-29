# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test runner — discovers, registers, and executes TestCase subclasses.

Robot .robot files are the single source of truth for test definitions.
Python TestCase classes provide execution logic and are mapped via tc_id.
"""

import json
import os
import logging
import time
from concurrent.futures import ThreadPoolExecutor

from src.testcases.base import TestCase, TestResult, StopTest
from src.tester_logger import RingBufferHandler
from src.testcases.robot_parser import parse_all_suites, suite_to_category
from src.testcases import runner_config
from src.testcases.spec import TestSpec
from src.testcases._pcap_capture import PcapCapture

log = logging.getLogger("tester.runner")

_PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
_RESULTS_DIR = os.path.join(_PROJECT_ROOT, "data", "test_results")
_HISTORY_FILE = os.path.join(_RESULTS_DIR, "history.json")


def _save_history(results):
    """Persist test results to JSON for future viewing."""
    os.makedirs(_RESULTS_DIR, exist_ok=True)
    try:
        with open(_HISTORY_FILE, "w", encoding="utf-8") as f:
            json.dump(results, f, indent=2)
    except Exception as e:
        log.warning("Failed to save test history: %s", e)


def _load_history():
    """Load persisted test results."""
    if not os.path.exists(_HISTORY_FILE):
        return []
    try:
        with open(_HISTORY_FILE, "r", encoding="utf-8") as f:
            return json.load(f)
    except Exception:
        return []


def _spec_payload(cls):
    """Serialise the class's SPEC for the catalog. Returns None for
    classes that haven't been retrofitted yet (transitional safety)."""
    if cls is None:
        return None
    spec = getattr(cls, "SPEC", None)
    if not isinstance(spec, TestSpec):
        return None
    return spec.to_dict()


def save_run_report(results, test_list, robot_results=None):
    """Save a timestamped report snapshot for the AI analyzer."""
    os.makedirs(_RESULTS_DIR, exist_ok=True)
    ts = time.strftime("%Y%m%d_%H%M%S")
    report = {
        "timestamp": time.strftime("%Y-%m-%d %H:%M:%S"),
        "summary": {
            "total": len(results),
            "pass": sum(1 for r in results if r.get("status") == "PASS"),
            "fail": sum(1 for r in results if r.get("status") == "FAIL"),
            "error": sum(1 for r in results if r.get("status") not in ("PASS", "FAIL", "PENDING")),
        },
        "results": results,
        "test_descriptions": {t["name"]: t.get("description", "") for t in test_list},
        "robot_results": robot_results or [],
    }
    path = os.path.join(_RESULTS_DIR, f"report_{ts}.json")
    try:
        with open(path, "w", encoding="utf-8") as f:
            json.dump(report, f, indent=2)
        log.info("Test report saved: %s", path)
    except Exception as e:
        log.warning("Failed to save report: %s", e)
    return path


class TestRunner:
    """Manages test case registration and execution.

    Robot .robot files define the test catalog (names, docs, tags).
    Python TestCase subclasses provide execution logic, mapped via tc_id.
    """

    def __init__(self):
        self.test_classes = {}    # name → TestCase subclass
        self._tc_id_map = {}      # tc_id → TestCase subclass
        self._robot_tests = []    # list of dicts from robot parser
        self.results = []
        self._executor = ThreadPoolExecutor(max_workers=8, thread_name_prefix="test")
        self._history = _load_history()

    def register(self, test_cls):
        """Register a TestCase subclass."""
        self.test_classes[test_cls.name] = test_cls
        if test_cls.tc_id:
            self._tc_id_map[test_cls.tc_id] = test_cls

    def load_robot_suites(self, robot_dir):
        """Parse all .robot files and use them as the test catalog."""
        self._robot_tests = parse_all_suites(robot_dir)
        log.info("Loaded %d robot test definitions from %s", len(self._robot_tests), robot_dir)

    def get_test_list(self):
        """Build the test catalog: robot-defined TCs merged with Python implementations.

        Each entry carries the full SPEC metadata under the `spec` key
        so the GUI can pivot / filter by Domain / NF / Severity / Tags
        / Slice without round-tripping back to the server. SPEC metadata
        is the authoritative source for category and tags; the Robot-
        suite-derived fields are kept for backward-compatibility with
        the catalog UI but should be considered deprecated.

        Returns list of dicts with keys:
            name, tc_id, description, tags, suite, category,
            has_implementation, robot_name,
            spec: { tc_id, title, spec, spec_ts, spec_section,
                    domain, nfs, slice, dnn, severity, tags,
                    setup, setup_notes, description,
                    expected_duration_s, flaky,
                    requires_dataplane, requires_real_hw }
        """
        result = []
        seen_tc_ids = set()

        # Robot-defined TCs are the source of truth for the catalog;
        # SPEC fields come from the Python implementation when present.
        for rtc in self._robot_tests:
            tc_id = rtc["tc_id"]
            seen_tc_ids.add(tc_id)

            # Look up Python implementation by tc_id
            impl_cls = self._tc_id_map.get(tc_id)
            spec_dict = _spec_payload(impl_cls)
            # Prefer SPEC's tags + category over the Robot suite when
            # SPEC is present — single source of truth.
            tags = spec_dict["tags"] if spec_dict else rtc.get("tags", [])
            category = (
                f"{spec_dict['domain']} ({spec_dict['spec_ts']})"
                if spec_dict else suite_to_category(rtc["suite"])
            )

            result.append({
                "name": impl_cls.name if impl_cls else tc_id,
                "tc_id": tc_id,
                "description": (
                    spec_dict["description"] if spec_dict
                    else rtc["documentation"]
                ),
                "tags": tags,
                "suite": rtc["suite"],
                "category": category,
                "has_implementation": impl_cls is not None,
                "robot_name": rtc["robot_name"],
                "spec": spec_dict,
            })

        # Python-only TCs (no matching robot file) — backward compat
        for name, cls in self.test_classes.items():
            tc_id = cls.tc_id
            if tc_id and tc_id in seen_tc_ids:
                continue  # already covered by robot
            if not tc_id and name in seen_tc_ids:
                continue
            spec_dict = _spec_payload(cls)
            tags = spec_dict["tags"] if spec_dict else []
            category = (
                f"{spec_dict['domain']} ({spec_dict['spec_ts']})"
                if spec_dict else (getattr(cls, 'category', '') or "Traffic")
            )
            # Synthesise a robot-style display name for Python-only TCs.
            # The GUI's name renderer strips a leading "TC-XXX-NNN " prefix
            # then shows the rest — emitting "<tc_id> <SPEC.title>" gives
            # these TCs the same readable label that Robot-defined TCs get
            # from their suite file, instead of falling back to the raw
            # snake_case class name (e.g. "tc reg 007").
            synth_robot_name = ""
            if spec_dict and spec_dict.get("title") and tc_id:
                synth_robot_name = f"{tc_id} {spec_dict['title']}"
            result.append({
                "name": name,
                "tc_id": tc_id or name,
                "description": (
                    spec_dict["description"] if spec_dict else cls.description
                ),
                "tags": tags,
                "suite": "",
                "category": category,
                "has_implementation": True,
                "robot_name": synth_robot_name,
                "spec": spec_dict,
            })

        return result

    def run_test(self, name, gnb_pool, ue_pool, params=None):
        """Instantiate and run a test case. Returns TestResult.

        `name` can be either a Python class name or a robot tc_id.
        """
        cls = self.test_classes.get(name)
        if cls is None:
            # Try looking up by tc_id
            cls = self._tc_id_map.get(name)

        if cls is None:
            r = TestResult(name)
            # Check if it's a known robot TC without implementation
            robot_tc = next((t for t in self._robot_tests if t["tc_id"] == name), None)
            if robot_tc:
                r.status = "NOT_IMPLEMENTED"
                r.error = f"Test '{robot_tc['robot_name']}' is defined in {robot_tc['suite']}.robot but has no Python implementation yet"
            else:
                r.status = "ERROR"
                r.error = f"Unknown test: {name}"
            self.results.append(r)
            log.error("Cannot run test: %s — %s", name, r.error)
            self._persist()
            return r

        mode = runner_config.pretest_mode()
        log.info("Test %s: STARTING (pretest_mode=%s)", name, mode)

        # Pretest gate — pretest_mode governs how we bring the SUT to a
        # known state before this test (see config/runner.json).
        #
        # 'full' and 'baseline' both call reset-to-baseline: 'full' is
        # documented for a freshly-rebuilt stack, 'baseline' for an
        # already-running one. The in-loop behaviour is identical
        # because docker rebuild is an operator concern, not a runner
        # one. Reset wipes the core DB and restarts sa_core; on
        # restart SeedAll re-seeds from db/seed/baseline.yaml
        # (128 UEs, 3 slices, 4 DNNs, 16 IMS subs).
        #
        # 'delta' skips the reset — the test must apply its own
        # pre-config and clean up after itself. Order-sensitive; use
        # only with hermetic tests.
        if mode in ("full", "baseline"):
            try:
                from src.core.admin import reset_to_baseline
                if not reset_to_baseline():
                    r = TestResult(name)
                    r.status = "ERROR"
                    r.error = "core did not come back after reset-to-baseline within 30s"
                    self.results.append(r)
                    self._persist()
                    log.error("Test %s: ERROR — core reset failed", name)
                    return r
            except Exception as e:
                log.warning("reset-to-baseline failed (continuing anyway): %s", e)
        else:  # 'delta'
            log.info("Test %s: skipping reset-to-baseline (pretest_mode=delta)", name)

        tc = cls(gnb_pool, ue_pool, params)
        tc.result.status = "RUNNING"

        # Per-test packet capture. Started BEFORE result is appended
        # to runner.results (and thus before /api/tests reports
        # status=RUNNING) so the streaming HTTP endpoint can start
        # serving the live pcap the moment a client polls. tcpdump
        # in-process replaces the older docker-sidecar / Windows-
        # watcher design which raced sub-second tests; see
        # _pcap_capture.py for the long rationale.
        run_id = f"{time.strftime('%Y%m%d_%H%M%S')}_{name}"
        tc.result.run_id = run_id
        pcap = PcapCapture(run_id=run_id)
        try:
            pcap.start()
        except Exception as e:
            log.warning("PcapCapture.start failed for %s: %s", name, e)

        self.results.append(tc.result)

        # Capture log snapshot start point
        ring = RingBufferHandler.get_instance()
        log_start_seq = ring.current_seq
        trace_start = time.time()

        # Note: pre/post UPF-stats snapshots (via sa_core) used to live here.
        # Removed — the tester owns the traffic plane end-to-end now, and
        # tests must not make sa_core calls. For out-of-band UPF visibility,
        # use /api/core/upf-stats from the UI (not during a test run).

        start = time.time()
        try:
            tc.run()
        except StopTest:
            pass  # result already set by require_*
        except Exception as e:
            tc.result.status = "ERROR"
            tc.result.error = str(e)
            log.error("Test %s exception: %s", name, e, exc_info=True)
        finally:
            # Clean up all resources after test — fresh state for next test
            try:
                tc.cleanup()
            except Exception:
                pass
            # Stop tcpdump and record the saved pcap path AFTER tc.run
            # AND tc.cleanup -- packets emitted during cleanup (SCTP
            # SHUTDOWN, NGAP UE Context Release) are part of the test
            # story and belong in the pcap.
            try:
                pcap.stop()
                if os.path.exists(pcap.final_path):
                    tc.result.pcap_path = pcap.final_path
            except Exception as e:
                log.warning("PcapCapture.stop failed for %s: %s", name, e)

        tc.result.duration_ms = (time.time() - start) * 1000
        status = tc.result.status
        dur = tc.result.duration_ms
        err = tc.result.error

        if status == "PASS":
            log.info("Test %s: PASS (%.0f ms)", name, dur)
        else:
            log.warning("Test %s: %s (%.0f ms) - %s", name, status, dur, err or "no details")

        # Capture logs emitted during this test
        test_logs = ring.get_entries(after_seq=log_start_seq, last_n=500)
        tc.result.logs = [
            {"ts": e["timestamp"], "level": e["level"], "logger": e["logger_name"], "msg": e["message"]}
            for e in test_logs
        ]

        # Capture protocol trace from all gNBs
        trace = []
        for gnb in gnb_pool:
            if hasattr(gnb, 'get_trace'):
                gnb_trace = gnb.get_trace(after_time=trace_start)
                for t in gnb_trace:
                    t["gnb"] = gnb.gnb_name
                trace.extend(gnb_trace)
        trace.sort(key=lambda x: x.get("time", 0))
        tc.result.protocol_trace = trace

        self._persist()
        return tc.result

    def run_test_async(self, name, gnb_pool, ue_pool, params=None):
        return self._executor.submit(self.run_test, name, gnb_pool, ue_pool, params)

    def get_results(self):
        return [r.to_dict() for r in self.results]

    def get_history(self):
        """Get all results: current session + persisted history."""
        return self._history

    def clear_results(self):
        self.results.clear()
        self._persist()

    def _persist(self):
        """Save current session results to disk."""
        data = [r.to_dict() for r in self.results]
        self._history = data
        _save_history(data)

    @staticmethod
    def list_reports():
        """List all saved report files."""
        if not os.path.isdir(_RESULTS_DIR):
            return []
        files = sorted(
            [f for f in os.listdir(_RESULTS_DIR) if f.startswith("report_") and f.endswith(".json")],
            reverse=True,
        )
        reports = []
        for f in files[:50]:
            path = os.path.join(_RESULTS_DIR, f)
            try:
                with open(path, "r", encoding="utf-8") as fh:
                    data = json.load(fh)
                reports.append({
                    "file": f,
                    "timestamp": data.get("timestamp", ""),
                    "summary": data.get("summary", {}),
                })
            except Exception:
                reports.append({"file": f, "timestamp": "?", "summary": {}})
        return reports

    @staticmethod
    def load_report(filename):
        """Load a specific saved report."""
        path = os.path.join(_RESULTS_DIR, filename)
        if not os.path.exists(path):
            return None
        with open(path, "r", encoding="utf-8") as f:
            return json.load(f)

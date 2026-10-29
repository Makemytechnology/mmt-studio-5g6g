# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""BenchmarkContext — collect core-side + tester-side KPIs around a
test window and append the result to data/benchmark_history.jsonl.

Smallest-useful-slice scope:
  * Core-side: snapshot /api/kpis/snapshot before + after; diff the
    registration counters and use the post-window histogram (the
    core's Reset() makes "before" effectively zero, so the diff is
    the post snapshot).
  * Tester-side: caller pushes per-event wall times into
    `bm.tester_samples`. We compute percentiles ourselves so the
    tester is self-contained — no need to trust the core's math.
  * Persistence: one JSON line per run in
    tester/data/benchmark_history.jsonl. Trend tools read that file
    out-of-band.

Usage:

    from src.core.benchmark import BenchmarkContext

    with BenchmarkContext("TC-BMK-001") as bm:
        # drive the workload, record per-event latencies
        for ue in self.ues:
            t0 = time.monotonic_ns()
            ue.register()
            ue.wait_for_state("REGISTERED")
            bm.tester_samples.append((time.monotonic_ns() - t0) / 1e6)
    bm.report(self.result)                # writes to TC details + history
"""

from __future__ import annotations

import json
import logging
import os
import time
from contextlib import AbstractContextManager
from dataclasses import dataclass, field
from typing import Optional

from src.core.api import core_api as _core_api

log = logging.getLogger("tester.benchmark")

# History lives next to the test results — same volume mount so the
# GUI's future Benchmarks tab can read it without extra plumbing.
_HISTORY_PATH = os.environ.get(
    "TESTER_BENCHMARK_HISTORY",
    "/app/data/benchmark_history.jsonl",
)


# ── Tester-side percentile math ──────────────────────────────────────

def _percentiles(samples_ms: list[float]) -> dict:
    """Min / p50 / p95 / p99 / max / mean over a list of latencies.

    Empty input returns zeros (avoids special-casing in callers).
    Sort cost is O(n log n) at samplesCap=10000 → negligible vs
    the wall time of the benchmark itself.
    """
    if not samples_ms:
        return {"min_ms": 0, "p50_ms": 0, "p95_ms": 0, "p99_ms": 0,
                "max_ms": 0, "mean_ms": 0, "count": 0}
    ss = sorted(samples_ms)
    n = len(ss)
    def at(q): return ss[max(0, min(n - 1, int((n - 1) * q)))]
    return {
        "min_ms":  ss[0],
        "p50_ms":  at(0.50),
        "p95_ms":  at(0.95),
        "p99_ms":  at(0.99),
        "max_ms":  ss[-1],
        "mean_ms": sum(ss) / n,
        "count":   n,
    }


# ── Acceptance gate ──────────────────────────────────────────────────

@dataclass
class BenchmarkGate:
    """Threshold check for a benchmark result. A test can declare one
    and have the framework auto-fail when reality misses the bar.

    Any field left as None is not checked. fail_rate is a fraction
    in [0, 1].
    """
    max_p99_ms: Optional[float] = None
    max_p95_ms: Optional[float] = None
    max_mean_ms: Optional[float] = None
    max_fail_rate: Optional[float] = None
    min_throughput_per_s: Optional[float] = None

    def evaluate(self, summary: dict) -> tuple[bool, list[str]]:
        """Return (passed, [reasons]). reasons is empty on pass."""
        reasons: list[str] = []
        tp = summary.get("tester", {})
        cnt = (summary.get("core") or {}).get("counters") or {}
        attempts = cnt.get("attempts", 0)
        failures = cnt.get("failures", 0)
        fail_rate = (failures / attempts) if attempts else 0.0

        if self.max_p99_ms is not None and tp.get("p99_ms", 0) > self.max_p99_ms:
            reasons.append(f"p99={tp['p99_ms']:.1f}ms > {self.max_p99_ms}ms")
        if self.max_p95_ms is not None and tp.get("p95_ms", 0) > self.max_p95_ms:
            reasons.append(f"p95={tp['p95_ms']:.1f}ms > {self.max_p95_ms}ms")
        if self.max_mean_ms is not None and tp.get("mean_ms", 0) > self.max_mean_ms:
            reasons.append(f"mean={tp['mean_ms']:.1f}ms > {self.max_mean_ms}ms")
        if self.max_fail_rate is not None and fail_rate > self.max_fail_rate:
            reasons.append(f"fail_rate={fail_rate:.3f} > {self.max_fail_rate}")
        if self.min_throughput_per_s is not None:
            tp_rate = summary.get("throughput_per_s", 0)
            if tp_rate < self.min_throughput_per_s:
                reasons.append(f"throughput={tp_rate:.1f}/s < {self.min_throughput_per_s}/s")
        return (len(reasons) == 0, reasons)


# ── Context manager ──────────────────────────────────────────────────

@dataclass
class BenchmarkContext(AbstractContextManager):
    """Open with `with BenchmarkContext(tc_id) as bm:`; the test body
    appends per-event wall times to bm.tester_samples. report() then
    persists the merged tester+core view.
    """
    tc_id: str
    procedure: str = "registration"      # which KPI category to inspect
    gate: Optional[BenchmarkGate] = None

    # Populated during __enter__/__exit__.
    tester_samples: list[float] = field(default_factory=list)
    _t_start_ns: int = 0
    _t_end_ns: int = 0
    _core_snap_end: dict = field(default_factory=dict)

    def __enter__(self):
        # Zero the core's counters so the post snapshot is scoped to
        # this test window — no contamination from prior tests on the
        # same process.
        try:
            _core_api("/api/kpis/reset", "POST")
        except Exception as e:
            log.warning("BenchmarkContext: /api/kpis/reset failed: %s", e)
        self._t_start_ns = time.monotonic_ns()
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self._t_end_ns = time.monotonic_ns()
        try:
            snap = _core_api("/api/kpis/snapshot")
            self._core_snap_end = snap or {}
        except Exception as e:
            log.warning("BenchmarkContext: /api/kpis/snapshot failed: %s", e)
            self._core_snap_end = {}
        return False  # don't swallow exceptions

    def summary(self) -> dict:
        """Build the merged tester+core summary dict.

        Throughput is "successful events per second of test wall time"
        — derived from the core's success counter, not from
        tester_samples, because the latter doesn't distinguish
        success from failure on its own.
        """
        wall_s = (self._t_end_ns - self._t_start_ns) / 1e9
        core = (self._core_snap_end or {}).get(self.procedure, {})
        successes = ((core.get("counters") or {}).get("successes") or 0)
        throughput = (successes / wall_s) if wall_s > 0 else 0.0
        return {
            "tc_id": self.tc_id,
            "procedure": self.procedure,
            "wall_s": round(wall_s, 3),
            "throughput_per_s": round(throughput, 2),
            "tester": _percentiles(self.tester_samples),
            "core": core,
        }

    def report(self, test_result=None) -> dict:
        """Compute summary, attach to test_result.details if given,
        run the gate (if any), append to history, return summary."""
        s = self.summary()

        if test_result is not None:
            # The TC's report shows whatever's already there plus our
            # benchmark block — preserves any per-test detail the run()
            # body added before calling report().
            try:
                test_result.details.setdefault("benchmark", {}).update(s)
            except Exception:
                # If test_result.details isn't a dict we don't want
                # to clobber it. Skip silently.
                pass

        if self.gate is not None:
            passed, reasons = self.gate.evaluate(s)
            s["gate"] = {
                "passed": passed,
                "thresholds": {
                    k: getattr(self.gate, k)
                    for k in ("max_p99_ms", "max_p95_ms", "max_mean_ms",
                              "max_fail_rate", "min_throughput_per_s")
                    if getattr(self.gate, k) is not None
                },
                "reasons": reasons,
            }
            if not passed and test_result is not None:
                try:
                    test_result.status = "FAIL"
                    test_result.error = "benchmark gate: " + "; ".join(reasons)
                except Exception:
                    pass

        _append_history(s)
        log.info(
            "Benchmark %s: throughput=%.1f/s p50=%.1fms p95=%.1fms p99=%.1fms "
            "attempts=%d successes=%d failures=%d",
            self.tc_id, s["throughput_per_s"],
            s["tester"]["p50_ms"], s["tester"]["p95_ms"], s["tester"]["p99_ms"],
            ((s["core"].get("counters") or {}).get("attempts", 0)),
            ((s["core"].get("counters") or {}).get("successes", 0)),
            ((s["core"].get("counters") or {}).get("failures", 0)),
        )
        return s


# ── History persistence ──────────────────────────────────────────────

def _append_history(summary: dict) -> None:
    """One JSON line per run. Atomic-ish: open in append mode, write,
    flush. Concurrent test runs would interleave; we don't expect
    that today (TestRunner serializes), and the JSONL line boundary
    is preserved by O_APPEND."""
    summary = {**summary, "timestamp_unix_ns": time.time_ns()}
    try:
        os.makedirs(os.path.dirname(_HISTORY_PATH), exist_ok=True)
        with open(_HISTORY_PATH, "a", encoding="utf-8") as f:
            f.write(json.dumps(summary, separators=(",", ":")) + "\n")
    except Exception as e:
        log.warning("Failed to append benchmark history: %s", e)


def history(limit: int = 0, tc_id: Optional[str] = None) -> list[dict]:
    """Read the JSONL back. Used by the future Benchmarks GUI tab.

    Args:
        limit: 0 = all, else return the most recent N entries.
        tc_id: optional filter — only entries matching tc_id are returned.
    """
    if not os.path.exists(_HISTORY_PATH):
        return []
    rows: list[dict] = []
    with open(_HISTORY_PATH, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except Exception:
                continue
    if tc_id:
        rows = [r for r in rows if r.get("tc_id") == tc_id]
    if limit:
        rows = rows[-limit:]
    return rows

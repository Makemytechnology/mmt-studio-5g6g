# Observability

What's measured, where it's exposed, and how the spec-citation gate
keeps the tester from drifting away from 3GPP. This is the layer that
answers "why did this run hang at 1200 UEs?" and "is this clause
actually in TS 24.501 §6.4.1.4 or did someone hallucinate it?"

## 1. Logging

`src/tester_logger.py` is the single entry point. Every module gets a
logger via:

```python
from src.tester_logger import get_logger
log = get_logger("statemachine")    # → tester.statemachine
log.info("gNB connected to AMF %s:%d", ip, port)
```

Three sinks:

| Sink | Purpose |
|---|---|
| Console (color-coded by level) | dev workflow + `run.sh` foreground output |
| Rotating file handler at `/var/log/sa_tester/sa_tester.log` | post-mortem on a deployed tester |
| `RingBufferHandler` (in-memory, capacity 5000) | the web UI's `/api/logs` endpoint |

### 1.1 Levels

Default `INFO` for everything. Per-logger overrides persist via
`load_levels()` / `save_levels()`. The web UI surfaces a level picker
per logger so an operator can crank `tester.protocol.ngap` to `DEBUG`
mid-run and watch a specific NGAP exchange without restarting.

```python
from src.tester_logger import set_level, set_all_levels, save_levels
set_level("tester.protocol.ngap", "DEBUG")
save_levels()                               # persist to data/log_levels.json
```

### 1.2 What to log

- **State transitions** at INFO (gNB FSM, UE FSM).
- **Outbound spec-mandated messages** at DEBUG with a citation tag.
- **Spec-violation rejects** at WARNING — these are the test signal
  and ops needs them visible without DEBUG noise.
- **Backpressure events** (mailbox drops, queue saturation) at
  WARNING with a counter. See [control-plane.md §4.2](control-plane.md).
- **Never** log raw NAS/NGAP bytes at INFO — they're large and
  contain SUPI/keys at certain stages.

## 2. Stats and metrics

### 2.1 In-tester counters

`src/traffic/stats/` carries per-flow rolling counters: bytes in/out,
packets in/out, jitter, loss. Updated by the receivers under
`src/traffic/receivers/`. Exposed via the traffic API
(`src/routes/traffic_api.py`) for the UI; also persisted into
`data/satester.db` as part of each run's metrics record.

### 2.2 UPF stats are deliberately not collected

`src/observability/core_stats.py` used to scrape sa_core's
`/api/upf/*` before and after every test. **That coupling is now
explicitly forbidden** — the tester owns the traffic plane on both
ends (tester + DN-side agent), and sa_core is deliberately out of
the loop during test execution.

The functions still exist as stubs (`collect_upf_stats`,
`compute_upf_delta`) so every caller's `if upf_before and upf_after:`
guard short-circuits cleanly. Don't reintroduce live calls. If you
need a one-off UPF snapshot, hit `/api/core/upf-stats` manually from
the UI — but not as part of a run.

### 2.3 Prometheus surface (Phase 5, not landed)

[ARCHITECTURE.md §5 Phase 5](ARCHITECTURE.md) calls for a `/metrics`
endpoint on both the control plane and the Rust DP, with histograms
per phase (Reg, Auth, SMC, PDU). Acceptance is "load-test report,
known limits published." Not started.

## 3. Speccheck — the spec-citation gate

`tests/speccheck/speccheck.py` is a Python port of
`nf/tools/speccheck/speccheck.go` from `mmt-studio-core-go`. Same
operator policy on both sides: **every spec citation in code or
comments must resolve to a real clause in a loaded PDF**. No quoting
from memory. No hallucinated subsection numbers.

### 3.1 What it scans

```
patterns: "TS 24.501 §5.5.1.2", "TS 38.413 §9.2.5.1", "RFC 4960 §3.2", ...
sources : src/**/*.py
docs    : specs/common/*.{pdf,txt}
mapping : tests/speccheck/speccheck.py:DOC_MAP
```

### 3.2 Three statuses

| Status | Meaning | Action |
|---|---|---|
| `VERIFIED` | citation resolved in the loaded doc | OK |
| `MISSING` | doc loaded but the section isn't in it | real bug — typo, hallucinated subsection, or drift across spec revision |
| `UNLOADED` | the doc PDF / text isn't in `specs/common/` | either land the PDF + a `DOC_MAP` entry, or re-target the citation to a doc already loaded |

### 3.3 Run

```sh
python3 -m pytest tests/speccheck -v                  # strict (default)
SPECCHECK_LOOSE=1 python3 -m pytest tests/speccheck -v # tolerate gaps in flight
```

**Do not merge with `SPECCHECK_LOOSE=1` set.** The whole point is to
catch drift; tolerating gaps at merge time is the failure mode.

### 3.4 Current baseline

189 VERIFIED, 10 MISSING, 60 UNLOADED across 19 docs (out of 259
citations scanned). See [speccheck_punchlist.md](speccheck_punchlist.md)
for the per-citation triage. Three of the 10 `MISSING` are in
control-plane code and are the next remediation target:

- `gnb_fsm.py:1099` — `TS 38.413 §8.1.3` (likely `§9.2.2.x`)
- `ue_fsm.py:228` — `TS 33.501 §6.1.3.4` (no `.4` exists; likely `.3` sync/MAC failure path)
- `protocol/ngap.py:307` — `TS 38.413 §8.1.3.2` (same root cause as `gnb_fsm.py:1099`)

### 3.5 Adding a citation

1. Cite by section, not figure number. `§9.11.4.13` is a section;
   `§9.11.4.13.4` was a figure number we mistook for a section once
   — caught in the baseline commit.
2. Cite the most specific clause that actually contains the rule.
3. If the doc isn't loaded, **load the PDF** and add a `DOC_MAP`
   entry — don't wait for someone else to do it. PDFs go to
   `specs/common/` named `ts_NNNNNNvVVVVVVp.pdf` (3GPP) or
   `rfcNNNN.txt` (IETF).

## 4. Run history and analysis

`src/db/runs.py` and `src/db/reports.py` persist every run to
`data/satester.db`:

- `runs` table — run id, type (`single`/`suite`/`regression`), trigger
  (`cli`/`web`/`scheduled`), suites filter, start/end timestamps.
- `results` table — per-test status, duration_ms, details (JSON), errors.
- `metrics` table — per-test metric snapshots from the traffic engine.
- `reports` table — generated HTML / JUnit / JSON artifacts.

`src/db/analysis.py` exposes pass-rate trends, regression detection,
and per-test flakiness metrics. CLI: `python3 -m src.cli analysis
pass-rate`.

## 5. AI-assisted analysis

`src/ai_engine/` (Ollama-backed):

- `ollama_client.py` — local-LLM client, no data leaves the host.
- `rag_engine.py` — RAG over `data/rag_store.json` (run history + spec
  excerpts). Used for "why did test X fail at this run?" queries.
- `pcap_analyzer.py` — feeds captured PCAPs through tshark + the
  LLM for narrative diagnostic summaries.

These are diagnostic helpers — **never** in the test execution path.
A test passes or fails based on protocol assertions, not on what the
LLM thinks. Routes live at `/api/ai/*` (see
[web-api.md](web-api.md)).

## 6. What to instrument when you add a feature

When you add a control-plane phase, traffic generator, or testcase:

| Signal | Where it goes |
|---|---|
| State transitions | `tester.<module>` logger at INFO |
| Backpressure / drop events | counter + WARNING log |
| Per-phase latency | histogram (Phase 5; today: log a single duration metric) |
| Per-flow throughput / loss | `src/traffic/stats/` rolling counters |
| Spec-mandated rejection from peer | WARNING with citation tag |

If you can't tell the operator **why** the run is in its current state
from logs + DB at the moment something goes wrong, you haven't
finished the feature.

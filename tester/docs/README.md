# SA Tester — Design Documentation

Index of design docs for the SA Tester (5G SA Core Network Tester). Each doc
is scoped to one subsystem and links into the source tree at the relevant
files. Read [ARCHITECTURE.md](ARCHITECTURE.md) first — everything else
elaborates one of its sections.

## Top-level

| Doc | What it covers |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | Goals, principles, phased roadmap, controller / worker / data-plane split. The source of truth for the refactor. |
| [overview.md](overview.md) | System diagram, runtime layout, process topology, what runs where. Read this if you're new. |

## Subsystems

| Doc | Source area | What it covers |
|---|---|---|
| [control-plane.md](control-plane.md) | `src/control/`, `src/protocol/`, `src/statemachine/` | NGAP/NAS state machines (legacy threaded + new asyncio actor model), SCTP transport, security primitives. |
| [data-plane.md](data-plane.md) | `src/protocol/gtpu.py`, `src/dataplane/`, `dp-rust/` | GTP-U encap/decap, TUN device management, the Phase 3 Rust DP and CP↔DP wire protocol. |
| [testcases.md](testcases.md) | `src/testcases/`, `robot/suites/`, `tests/` | TestCase base class, the runner, Robot Framework integration, pytest unit tests, registry / discovery. |
| [web-api.md](web-api.md) | `src/app.py`, `src/routes/`, `src/templates/`, `src/static/` | FastAPI app, route blueprints, web UI, REST surface, AI engine endpoints. |
| [observability.md](observability.md) | `src/observability/`, `src/tester_logger.py`, `tests/speccheck/` | Logging ring buffer, log level controls, core stats, the `speccheck` spec-citation gate. |

## Audits and campaigns

These are scoped, time-boxed reports — not living docs. Read them for context
on why specific tests / fixes exist; do not treat their TODO sections as
current state without re-checking against the code.

| Doc | What it covers |
|---|---|
| [go_reference_gap.md](go_reference_gap.md) | Coverage comparison vs. the `mmt-studio-core-go` reference. 78 pytest tests landed; 5 real bugs surfaced via xfail. |
| [speccheck_punchlist.md](speccheck_punchlist.md) | Outstanding `MISSING` / `UNLOADED` 3GPP citations in `src/`. Triage policy and remediation rules. |

## Per-suite training notes

`training_notes/` contains a directory per Robot suite category
(`registration/`, `authentication/`, `pdu_session/`, `ng_setup/`, `ims/`,
`stress/`, `traffic/`). Each holds short references — spec sections, message
flows, edge cases — written for someone touching that suite for the first
time. Add to these as you learn; don't let them rot silently.

## How to add a doc

- One file per subsystem. Match the source-tree boundaries.
- Lead with **what's implemented today** vs **what's planned** — readers waste
  time when this isn't explicit. The ARCHITECTURE.md phase table is the
  authoritative source for "planned".
- Reference source files with `path:line` so the doc stays anchored. If a
  reference goes stale, the file rename should make it obvious during code
  review.
- Don't duplicate content from the in-package READMEs (`src/control/README.md`,
  `src/dataplane/README.md`, `dp-rust/README.md`). Link to them. They live
  next to the code that changes most often, so they decay slower.

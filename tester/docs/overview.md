# SA Tester — System Overview

Read this first. It's the 30-minute orientation: what the product is, what
runs where, and which file to open next for any given subsystem. Deep
designs live in the per-subsystem docs linked at the bottom.

## 1. What it is

The SA Tester emulates **gNB + UE pairs** that exercise a 5G Standalone
Core (AMF / SMF / UPF / IMS / LMF / NTN / IoT). It is not a UE simulator
benchmark and it is not a network emulator — it's a **functional &
performance regression rig** for *the core network on the other end of
the SCTP socket*.

Two halves:

- **Control plane (Python)** — drives NGAP/NAS state machines, IMS SIP,
  positioning, IoT, NTN. Owns all spec-mandated message construction.
- **Data plane (Python today, Rust by Phase 3)** — owns the TUN device,
  GTP-U encap/decap, traffic generators (iperf3, RTP).

The boundary between them is the part most likely to change next. See
[ARCHITECTURE.md §3](ARCHITECTURE.md) for the target architecture and
[control-plane.md](control-plane.md) / [data-plane.md](data-plane.md) for
where each half is today.

## 2. Process topology

```
┌────────────────────────── one tester host ──────────────────────────┐
│                                                                       │
│   uvicorn (FastAPI) ── port 5000 ── web UI + REST API                │
│   │                                                                   │
│   │  spawns / orchestrates                                            │
│   ▼                                                                   │
│   TestRunner (in-process today; cluster-mode = controller + workers)  │
│   │                                                                   │
│   ▼                                                                   │
│   GnbStateMachine + UeStateMachine  ──SCTP──►  AMF (5G core)         │
│   │                                                                   │
│   ▼                                                                   │
│   GtpuManager (TUN tester-dp0) ──UDP/2152──► UPF (5G core)           │
│   │                                                                   │
│   ▼                                                                   │
│   Traffic engines: iperf3, RTP/AMR, RTP/H.264, SIP/RTP for IMS       │
│                                                                       │
└───────────────────────────────────────────────────────────────────────┘
```

Cluster mode (`src/cluster/`) splits the runner into a controller + N
worker processes, one process per simulated gNB. This is the path to 10k
UEs — see [ARCHITECTURE.md §5 Phase 4](ARCHITECTURE.md).

## 3. Repository layout

```
mmt_studio_core_tester/
├── README.md              # quickstart, feature summary, suite table
├── run.sh                 # entry point (auto-sudo, bundled Python, port 5000)
├── install.sh             # deploy on a target host
├── src/
│   ├── app.py             # FastAPI app, route registration, AI engine init
│   ├── cli.py             # CLI entry: `python -m src.cli run|analysis|status`
│   ├── config.py          # paths, defaults, env-driven knobs
│   ├── tester_logger.py   # centralized logging, ring buffer, level controls
│   ├── routes/            # FastAPI blueprints (test exec, infra, db, etc.)
│   ├── protocol/          # NGAP, NAS, SCTP, GTP-U, SIP, RTP, crypto, NTN, IoT, ...
│   ├── statemachine/      # legacy threaded gNB + UE FSM
│   ├── control/           # new asyncio control plane (Phase 2+) — see its README
│   ├── dataplane/         # Python client to Rust DP (Phase 3 — placeholder)
│   ├── traffic/           # iperf3 / RTP traffic engine + agents
│   ├── testcases/         # 47 testcase modules grouped by domain
│   ├── core/              # REST client to talk to the SA Core (provisioner, admin)
│   ├── db/                # SQLite schemas, CRUD, runs, reports, analysis
│   ├── cluster/           # controller + worker for distributed mode
│   ├── observability/     # core_stats — counters, histograms
│   └── ai_engine/         # Ollama-backed RAG + PCAP analyzer
├── robot/
│   ├── suites/            # 31 .robot suites grouped by domain (access, session, mobility, ...)
│   └── resources/         # shared keywords + variables
├── tests/                 # 39 pytest files (codec round-trips, security primitives, speccheck)
│   ├── speccheck/         # the 3GPP citation gate
│   ├── bench/             # attach_bench.py — async control-plane benchmark
│   └── probes/            # one-off probes (e.g. netlink route timing)
├── dp-rust/               # Rust data-plane workspace (Phase 3 — placeholder)
├── libs/                  # bundled deps (Python 3.12, iperf3, libsctp, pycrate, ...)
├── build/                 # PyInstaller spec, Dockerfile, deploy/
├── config/                # gNB profiles, sim DB, cluster config, test sequences
├── specs/                 # 3GPP / IETF specs the tester is anchored to (PDFs + RFCs)
└── docs/                  # this directory
```

## 4. Where to start, by task

| Task | Open this first |
|---|---|
| Understand the refactor | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Add or fix a test case | [testcases.md](testcases.md), then `src/testcases/<domain>/` |
| Touch NGAP / NAS encoding | [control-plane.md](control-plane.md), then `src/protocol/ngap.py` and `src/protocol/nas.py` |
| Touch GTP-U or TUN plumbing | [data-plane.md](data-plane.md), then `src/protocol/gtpu.py` |
| Fix a 3GPP citation flagged by speccheck | [observability.md](observability.md), then [speccheck_punchlist.md](speccheck_punchlist.md) |
| Add a REST endpoint | [web-api.md](web-api.md), then `src/routes/` |
| Write a Robot suite | `robot/suites/` (existing suites as examples) + `docs/training_notes/` |
| Cross-check vs. Go reference | [go_reference_gap.md](go_reference_gap.md) |

## 5. Standards anchor

The tester strictly follows 3GPP / IMS / RFC standards. There are no
fallbacks or workarounds — **a non-compliant response from the core must
fail the test**. Every protocol-layer assertion in `src/protocol/` and
every spec-driven testcase carries a citation; `tests/speccheck/` is the
strict gate that verifies those citations resolve to real clauses in
loaded PDFs under `specs/`.

Key specs anchored in the tester:

- TS 24.501 (5G NAS), TS 38.413 (NGAP), TS 33.501 (5G Security)
- TS 29.281 (GTP-U), TS 38.415 (PDU Session Container)
- TS 24.229 (SIP/IMS), TS 24.147 (Conference), TS 26.114 (Media)
- RFC 3261 (SIP), RFC 3515 (REFER), RFC 3550 (RTP)
- TS 23.273 (Positioning), TS 38.305 (NR Positioning Methods)
- TS 22.369 (A-IoT), TS 38.821 (NTN), TS 23.401 (IoT/EPS)

The full set of loaded PDFs is `specs/common/`. Adding a citation to a
doc not yet in `specs/common/` means either landing the PDF + a `DOC_MAP`
entry in `tests/speccheck/speccheck.py` or re-targeting the citation to
a doc already loaded — see [observability.md](observability.md).

## 6. Phase status (2026-Q2)

From [ARCHITECTURE.md §5](ARCHITECTURE.md):

| Phase | Status |
|---|---|
| 1 — Netlink (pyroute2 replaces `ip` shellouts) | landed for `gtpu.py` hot path |
| 2 — Async CP (asyncio actor model) | partial — `src/control/` carries Reg flow; legacy `statemachine/` still drives all testcases |
| 3 — Rust DP (Tokio + TUN + GTP-U) | not started — `dp-rust/` and `src/dataplane/` are placeholders with the planned wire protocol |
| 4 — Shard (one worker per gNB) | scaffolded in `src/cluster/`; not yet wired into the runner default path |
| 5 — Obs + harden (`/metrics`, backpressure budgets) | partial — `src/observability/core_stats.py` exists; Prometheus surface pending |

Always re-check this against the code. The ARCHITECTURE phase table is
the source of truth — this list rots fast.

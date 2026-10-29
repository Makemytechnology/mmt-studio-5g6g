# Web API & UI

Covers the FastAPI app at port 5000: how routes are organized, what the
web UI talks to, and the migration in progress from inline routes in
`src/app.py` to dedicated routers in `src/routes/`.

## 1. App layout

```
src/
├── app.py                       # FastAPI app, ~70 inline routes (legacy)
├── routes/                      # extracted routers (migration in progress)
│   ├── __init__.py              # register_routers(app) — only the new ones are active
│   ├── infrastructure.py        # ACTIVE: infra config (AMF IP, traffic agent, etc.)
│   ├── cluster_api.py           # ACTIVE: controller / worker mgmt
│   ├── traffic_api.py           # ACTIVE: traffic profiles + flows
│   ├── traffic_agent_api.py     # ACTIVE-side: routes hit by remote agents
│   ├── provisioning.py          # PENDING: SIM DB, gNB profiles
│   ├── test_execution.py        # PENDING: run / status / cancel
│   ├── reports.py               # PENDING: HTML / JUnit / JSON exports
│   ├── analysis.py              # PENDING: pass-rate, flaky, regressions
│   ├── core_mgmt.py             # PENDING: SA Core REST proxy (provision UE, NF status)
│   ├── db_api.py                # PENDING: raw DB read endpoints
│   └── common.py                # shared response models, helpers
├── templates/
│   └── tester_index.html        # single-page UI
└── static/                      # JS, CSS, vendor libs (Bootstrap, Plotly)
```

`src/routes/__init__.py:register_routers()` is the source of truth for
which routers are active. The migration plan is: pull a route group out
of `app.py`, enable its router in `register_routers()`, leave a one-
commit overlap window where both paths exist (mounted under different
prefixes), then delete the inline version. Do **not** hot-swap a
production route without that overlap.

## 2. Route surface (today)

The endpoints below are stable; new endpoints belong in a router under
`src/routes/`, not inline in `app.py`.

### 2.1 UI / health

| Method | Path | Backed by |
|---|---|---|
| GET | `/` | `tester_index.html` |
| GET | `/api/network-interfaces` | `src/protocol/netlink.py` |
| GET | `/api/gtpu/tunnels` | `GtpuManager` snapshot |

### 2.2 gNB / UE lifecycle

| Method | Path | Notes |
|---|---|---|
| GET | `/api/gnbs` | listing of `gnb_pool` |
| POST | `/api/gnbs` | create + push to pool |
| POST | `/api/gnbs/{idx}/connect` | drive `GnbStateMachine.connect()` |
| POST | `/api/gnbs/{idx}/disconnect` | graceful SCTP shutdown |
| POST | `/api/gnbs/{idx}/remove` | tears down + drops from pool |
| GET | `/api/ues` | listing of `ue_pool` |
| POST | `/api/ues/load-sims` | bulk-load from sim DB |
| POST | `/api/ues/{imsi}/register` | drive Reg flow |
| POST | `/api/ues/{imsi}/deregister` | drive Dereg flow |
| POST | `/api/ues/{imsi}/pdu-session` | establish PDU session |

### 2.3 Provisioning (SIM DB + gNB profiles)

CRUD over `config/sim_db.json` and `config/gnb_profiles.json` (mirrored
into `data/satester.db` on first boot).

| Method | Path |
|---|---|
| GET / POST | `/api/sim-db`, `/api/sim-db/{imsi}` |
| PUT / DELETE | `/api/sim-db/{imsi}` |
| POST | `/api/sim-db/clone`, `/api/sim-db/import` |
| GET / POST | `/api/gnb-config`, `/api/gnb-config/{name}` |
| PUT / DELETE / POST | `/api/gnb-config/{name}`, `/clone`, `/import`, `/{name}/apply` |

### 2.4 Test runs / catalog

The runner endpoints are partially in `app.py` and partially elsewhere.
Catalog comes from `TestRunner` populated at boot
(`src/app.py:82-87`).

| Method | Path | Notes |
|---|---|---|
| GET | `/api/runs`, `/api/runs/{run_id}` | reads `runs` + `results` tables |
| GET | `/api/runs/{run_id}/report/{fmt}` | fmt = `html` / `junit` / `json` |
| GET | `/api/reports` | listing of generated artifacts |
| GET | `/api/db/stats`, `/api/db/ue`, `/api/db/results`, ... | raw DB views |

### 2.5 Analysis

`src/db/analysis.py` powers these — historical trends and regression
flags surfaced in the UI.

| Path | Returns |
|---|---|
| `/api/analysis/pass-rate` | per-suite pass-rate over time |
| `/api/analysis/flaky` | per-test flakiness scores |
| `/api/analysis/failures` | top failing tests |
| `/api/analysis/suites` | per-suite summary |
| `/api/analysis/regressions/{run_id}` | regressions vs. prior run |
| `/api/analysis/compare` | run-vs-run diff |
| `/api/analysis/metric-trend` | metric series for a chosen test |

### 2.6 SA Core management proxy

`src/core/` is the REST client that talks to the SA Core (provisioner,
admin). The web app exposes a thin proxy so an operator can drive the
core from the tester UI.

| Path | Notes |
|---|---|
| `POST /api/core/sync-ues` | push sim DB → core's UDM/UDR |
| `POST /api/core/provision-ue` | provision a single UE |
| `POST /api/core/provision-suci-key` | upload SUCI ECIES key |
| `DELETE /api/core/delete-ue/{imsi}` | remove UE from UDM |
| `GET /api/core/nf-status` | per-NF up/down |
| `GET /api/core/upf-stats` | one-off snapshot — see [observability.md §2.2](observability.md) |

### 2.7 AI engine

| Path | Backed by |
|---|---|
| `/api/ai/chat` | `src/ai_engine/ollama_client.py` |
| `/api/ai/rag/...` | `src/ai_engine/rag_engine.py` |
| `/api/ai/pcap` | `src/ai_engine/pcap_analyzer.py` |

These are diagnostic helpers; never on the test execution path. See
[observability.md §5](observability.md).

### 2.8 Logs

| Path | Returns |
|---|---|
| `GET /api/logs` | tail of `RingBufferHandler` (5000-entry buffer) |
| `POST /api/logs/level` | set per-logger level live |
| `GET /api/logs/loggers` | known loggers + current levels |

## 3. Boot sequence

`src/app.py` top-of-file:

1. Set up logging (`tester_logger.setup_logging`, `load_levels`).
2. Banner (`startup_banner.log_banner`).
3. Create FastAPI app, mount `/static`, `templates`.
4. Initialize SQLite via `src.db.schema.ensure_schema()`. If the DB
   has no UEs, migrate `config/sim_db.json` + `config/gnb_profiles.json`
   into it.
5. Build `gnb_pool`, `ue_pool`, `TestRunner`, `GtpuManager` as global
   state.
6. Wire AI engine.
7. `discover_all()` testcases and register with the runner.
8. Parse `robot/suites/` so the catalog includes Robot tests.
9. Register active routers from `src/routes/__init__.py`.
10. Inline routes from `app.py` are picked up by FastAPI's decorator
    registration.

`run.sh` then launches `uvicorn src.app:app --host 0.0.0.0 --port
$TESTER_WEB_PORT`. Default port is 5000; configurable via `config.py`.

## 4. Conventions for new endpoints

- Domain → router. New endpoints live in a router under `src/routes/`
  even if the router isn't active yet. Add the route, leave the
  router's `app.include_router(...)` commented out, and migrate when
  the rest of the inline domain is ready.
- Response models go through Pydantic. Never return a raw `dict`
  unless the shape is small and one-off — type drift on the wire is
  hard to chase later.
- Never block the event loop. Anything heavy (running a test, scanning
  the spec corpus, generating a report) goes through `asyncio.to_thread`
  or a background task. The UI is single-tenant operationally but
  multi-tenant under load — a stuck route blocks the entire Web UI.
- Authentication: tester is currently unauthenticated by design — it
  runs on internal networks. Don't add auth piecemeal; if it's needed
  it's a project-wide change with a session model + UI affordance.

## 5. Web UI

`src/templates/tester_index.html` is a single page; everything is
client-side rendered against the JSON APIs. Static assets live in
`src/static/` — Bootstrap, Plotly for charts, vendor libs are
checked in (zero external CDN deps, same policy as the bundled Python
runtime).

The UI talks to the same APIs documented above. There is no separate
admin / operator split — the page surfaces every action a privileged
operator should have. Lock down at the network layer if you need to
restrict access.

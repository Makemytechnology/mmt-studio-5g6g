# IOPS — Isolated E-UTRAN Operation for Public Safety

Per-gNB IOPS controller for the SA Core: lifecycle state machine
(normal → backhaul_lost → iops_activated → restoring → restored),
pre-cached AKA tuple store for Local-EPC authentication, curated
local-service catalogue, active local-session ledger, and the event
log that doubles as the state history.

# Part A — Functional

## A.1 Why IOPS?

Public-safety RAN deployments must keep working when the backhaul to
the macro EPC / 5GC is severed (cable cut, edge-router failure,
disaster-area "isolated cell"). IOPS lets the eNB / gNB:

- Detect backhaul loss.
- Bring up a Local EPC (in-process or co-located).
- Authenticate UEs against **pre-cached AKA tuples** instead of the
  unreachable HSS.
- Serve a curated set of mission-critical local services (MCPTT,
  MCData, MCVideo, emergency, basic data) until backhaul returns.
- Drain local sessions and re-attach to the macro path on restore.

The 3GPP normative anchor is **TS 23.401 Annex K** (the spec we have
locally). The dedicated IOPS service requirements doc TS 22.346 is
referenced but not loaded — its parts are TODO until the PDF lands.

This package is the operator-/controller-facing projection of the
above. It does **not** drive the radio fall-back logic itself; it
records what happened, exposes the AKA cache, and feeds the GUI panel.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **S1 / N2** | gNB ↔ macro EPC/5GC | NGAP / S1AP over SCTP | TS 23.401 §K.2.4 | Loss detection is gNB-side; this surface records the resulting state. |
| **Local EPC AKA cache** | gNB ↔ Local EPC | per-IMSI cached AKA | TS 23.401 §K.2.3 | Pre-population + lookup live here; replay happens at the AMF/MME. |
| **Local PDU sessions** | UE ↔ Local UPF | NAS over RRC | TS 23.401 §K.2.4 | `iops_local_sessions` ledger; real bearers in the embedded UPF. |
| **MCX services** | UE ↔ MCPTT / MCData server | SIP/HTTP | TS 22.346 (TODO) | `DefaultLocalServices` catalogue is operator-policy today. |

## A.3 Operator-visible behaviours

### A.3.1 Lifecycle state machine

```
   ┌──────┐ DetectBackhaulLoss   ┌────────────┐ ActivateIOPS  ┌──────────────┐
   │normal│ ─────────────────────►│backhaul_   │ ────────────► │iops_activated│
   └──┬───┘                       │  lost      │               └──────┬───────┘
      ▲                           └────────────┘                      │
      │ CompleteRestoration                                           │ BeginRestoration
      │           ┌───────────┐                                       │
      └───────────│  normal   │ ◄─────────────── ┌──────────┐ ◄──────┘
                  └───────────┘                  │ restoring│
                                                 └──────────┘
                       MarkFailed       ┌────────┐
                  ─────────────────────►│ failed │ (degraded; manual reset)
                                        └────────┘
```

Only the listed forward transitions are accepted; out-of-order calls
are rejected by the package and surfaced as HTTP 400 by the route.
Each transition appends a row to `iops_events` so the audit log IS
the state history.

### A.3.2 Per-gNB configuration (TS 23.401 §K.2.3)

`iops_config` carries one row per shared gNB:

| Field | Default | Notes |
|-------|---------|-------|
| `iops_enabled` | 1 | Master switch |
| `local_auth_enabled` | 1 | Whether Local EPC may use cached AKA |
| `max_local_ues` | 100 | Soft cap; admission honours it |
| `local_ip_pool` | `10.99.0.0/24` | Local UPF address pool |

`UpsertConfig` is idempotent (`ON CONFLICT(gnb_id) DO UPDATE`).

### A.3.3 Pre-cached AKA tuples (TS 23.401 §K.2.3)

`iops_cached_credentials` rows carry one full AKA challenge per
(gnb_id, imsi): RAND, AUTN, XRES*, KSEAF, plus a per-tuple
`expires_at`. The macro HSS pushes fresh tuples ahead of an expected
outage; LocalAuth replays them when the HSS is unreachable.

`LocalAuthenticate(gnb_id, imsi)` returns `{allowed, method,
expires_at}` if a fresh row exists; otherwise `{allowed: false,
reason: ...}`. The actual replay happens in the AMF/MME.

### A.3.4 Curated local-service catalogue

`DefaultLocalServices()` returns the operator-policy view:

| Service | Priority | Rate limit (kbps) |
|---------|---------:|------------------:|
| `emergency` | 1 | unlimited |
| `ptt` | 2 | unlimited |
| `voice` | 3 | 64 |
| `data` | 4 | 256 |

Real prioritisation rides the public-safety MCX stack and per-mission
overrides — TODO TS 22.346.

### A.3.5 Local session ledger

`iops_local_sessions` rows carry one PDU session per (gnb_id, imsi)
during IOPS, with `service_type ∈ {voice, data, ptt, emergency}` and
`status ∈ {active, released}`. The ledger is the operator-side audit
trail; real bearers are in the embedded UPF.

## A.4 Operator REST API (`/api/iops/*`)

All endpoints return `{ok, ...}` envelopes keyed by domain noun.

### A.4.1 Stats / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/iops/stats` | `{ok, stats: {configured_gnbs, enabled_gnbs, activations, cached_credentials, active_local_sessions, restorations}}` |
| GET | `/api/iops/status` | per-gNB row table — `{ok, gnbs: [{gnb_id, state, iops_enabled, local_auth_enabled, max_local_ues, local_ip_pool}]}` |

### A.4.2 Per-gNB config (TS 23.401 §K.2.3)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/iops/config` | list every gNB's config |
| GET    | `/api/iops/config/{gnb}` | one gNB's config |
| POST   | `/api/iops/config` | UPSERT `{gnb_id, iops_enabled, local_auth_enabled, max_local_ues, local_ip_pool}` |

### A.4.3 Lifecycle (TS 23.401 §K.2.4)

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/iops/declare` | two-step backhaul_lost → iops_activated (`{gnb_id, reason?}`) |
| POST | `/api/iops/restore` | two-step restoring → restored (`{gnb_id}`) |
| GET  | `/api/iops/events?gnb_id=…&limit=N` | event log |

### A.4.4 Cached AKA tuples

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/iops/cache/{gnb}` | `{ok, gnb_id, count, credentials: [{imsi, expires_at}]}` |
| POST   | `/api/iops/cache-credentials` | bulk UPSERT `{gnb_id, credentials: [{imsi, rand_hex, autn_hex, xres_star_hex, kseaf_hex, expires_at}]}` |
| DELETE | `/api/iops/cache/{gnb}/{imsi}` | remove one tuple |
| GET    | `/api/iops/local-auth?gnb_id=…&imsi=…` | replay-availability probe |

### A.4.5 Local sessions + service catalog

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/iops/local-sessions?gnb_id=…` | session list (filter by gNB) |
| POST   | `/api/iops/local-sessions` | open `{gnb_id, imsi, service_type, ip_address}` (CHECK voice/data/ptt/emergency) |
| POST   | `/api/iops/local-sessions/{id}/release` | mark released |
| GET    | `/api/iops/services` | DefaultLocalServices catalogue |
| GET    | `/api/iops/service-available?gnb_id=…&service=…` | admission probe |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| MCPTT > MCVideo > MCData prioritisation rules | TS 22.346 | Flat `DefaultLocalServices` today; not loaded locally. |
| Nomadic EPS (truck-mounted Local EPC) | TS 23.401 Annex K | Touched in §K.2 but no dedicated subclause to anchor against. |
| USIM-based local AKA against the Local EPC | TS 22.346 | LocalAuthenticate just checks tuple availability; full AKA replay lives in AMF/MME. |

---

# Part B — Design

## B.1 Process layout

```
┌───────────────── safety/iops ───────────────────┐
│                                                 │
│  Per-gNB config                                 │
│   iops_config                                   │
│   ├── UpsertConfig                              │
│   ├── GetConfig / ListConfigs                   │
│                                                 │
│  Lifecycle state machine                        │
│   transition(gnb, target, reason)               │
│   ├── DetectBackhaulLoss                        │
│   ├── ActivateIOPS                              │
│   ├── BeginRestoration / CompleteRestoration    │
│   ├── MarkFailed                                │
│   └── GetState / GetAllStates                   │
│                                                 │
│  Cached AKA tuples (Local-EPC pre-pop)          │
│   iops_cached_credentials                       │
│   ├── CacheCredential                           │
│   ├── ListCachedCredentials                     │
│   ├── DeleteCachedCredential                    │
│   └── LocalAuthenticate                         │
│                                                 │
│  Local sessions + service catalog               │
│   iops_local_sessions                           │
│   ├── CreateLocalSession / ReleaseLocalSession  │
│   ├── ListLocalSessions                         │
│   ├── DefaultLocalServices                      │
│   └── CheckServiceAvailable                     │
│                                                 │
│  Event log (state history)                      │
│   iops_events  ◄── logEvent() each transition   │
│                                                 │
└─────────────────────────────────────────────────┘
                     ▲
                     │ JSON / HTTP
                     │
            webservice/app/routes_iops.go
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `safety/iops/iops.go` | ~480 | State machine + config + cached AKA + local sessions + event log + stats. |
| `safety/iops/iops_test.go` | ~200 | State transitions + cache lifecycle + local-session CHECK constraints. |
| `db/schemas/domains.go` | (slice) | DDL for `iops_config`, `iops_events`, `iops_cached_credentials`, `iops_local_sessions`. |
| `webservice/app/routes_iops.go` | ~340 | REST surface for §A.4. |

Tests:
- `mmt_studio_core_tester/src/testcases/safety/tc_iops.py` — 6 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS iops_config (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  gnb_id              TEXT NOT NULL UNIQUE,
  iops_enabled        INTEGER NOT NULL DEFAULT 1,
  local_auth_enabled  INTEGER NOT NULL DEFAULT 1,
  max_local_ues       INTEGER NOT NULL DEFAULT 100,
  local_ip_pool       TEXT NOT NULL DEFAULT '10.99.0.0/24',
  local_services_json TEXT,
  created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS iops_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  gnb_id      TEXT NOT NULL,
  event_type  TEXT NOT NULL CHECK (event_type IN (
                'backhaul_lost','iops_activated','restoring','restored','failed')),
  reason      TEXT,
  created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS iops_cached_credentials (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  gnb_id          TEXT NOT NULL,
  imsi            TEXT NOT NULL,
  rand_hex        TEXT NOT NULL,
  autn_hex        TEXT NOT NULL,
  xres_star_hex   TEXT NOT NULL,
  kseaf_hex       TEXT NOT NULL,
  expires_at      TEXT NOT NULL,
  UNIQUE (gnb_id, imsi)
);

CREATE TABLE IF NOT EXISTS iops_local_sessions (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  gnb_id        TEXT NOT NULL,
  imsi          TEXT NOT NULL,
  service_type  TEXT NOT NULL CHECK (service_type IN ('voice','data','ptt','emergency')),
  ip_address    TEXT,
  status        TEXT NOT NULL DEFAULT 'active'
                CHECK (status IN ('active','released')),
  created_at    TEXT NOT NULL DEFAULT (datetime('now')),
  released_at   TEXT
);
```

The `event_type` and `service_type` CHECK constraints are surfaced as
HTTP 400 by the route's vocabulary guards.

## B.4 State transitions

`validTransitions` (in `iops.go`) is the canonical map; the package
refuses any transition not listed.

```
normal           → backhaul_lost
backhaul_lost    → iops_activated, failed
iops_activated   → restoring, failed
restoring        → normal, failed
failed           → normal              (manual reset)
```

`transition(gnb, target, reason)`:

1. Reads current state from in-memory map.
2. Checks `validTransitions[current][target]`; rejects otherwise.
3. Updates in-memory state.
4. Appends `iops_events` row with `event_type = stateToEvent[target]`.

The in-memory state map is process-local; if the process restarts it
reconstructs from the most recent `iops_events` row per gNB.

## B.5 Public API

```go
type State string
const (
    StateNormal       State = "normal"
    StateBackhaulLost State = "backhaul_lost"
    StateIOPSActive   State = "iops_activated"
    StateRestoring    State = "restoring"
    StateFailed       State = "failed"
)

// Lifecycle
func DetectBackhaulLoss(gnbID, reason string) map[string]interface{}
func ActivateIOPS(gnbID string) map[string]interface{}
func BeginRestoration(gnbID string) map[string]interface{}
func CompleteRestoration(gnbID string) map[string]interface{}
func MarkFailed(gnbID, reason string) map[string]interface{}
func GetState(gnbID string) string
func GetAllStates() map[string]string

// Per-gNB config
func UpsertConfig(gnbID string, iopsEnabled, localAuthEnabled bool,
    maxLocalUEs int, localIPPool string) error
func GetConfig(gnbID string) (map[string]interface{}, error)
func ListConfigs() ([]map[string]interface{}, error)

// Cached AKA tuples
type CachedCredential struct {
    GnbID, IMSI, RandHex, AutnHex, XresStarHex, KseafHex, ExpiresAt string
}
func CacheCredential(c CachedCredential) error
func ListCachedCredentials(gnbID string) ([]map[string]interface{}, error)
func DeleteCachedCredential(gnbID, imsi string) error
func LocalAuthenticate(gnbID, imsi string) map[string]interface{}

// Local sessions + service catalog
type LocalService struct{ Name string; Enabled bool; Priority, RateLimitKbps int }
func DefaultLocalServices() []LocalService
func CheckServiceAvailable(gnbID, serviceName string) bool
func CreateLocalSession(gnbID, imsi, serviceType, ipAddress string) (int64, error)
func ReleaseLocalSession(sessionID int64) error
func ListLocalSessions(gnbID string) ([]map[string]interface{}, error)

// Event log + stats
func GetEvents(gnbID string, limit int) ([]map[string]interface{}, error)
func GetStats() map[string]interface{}
```

## B.6 Test coverage

### B.6.1 Go unit tests

`safety/iops/iops_test.go` — state-machine transitions (allowed +
rejected), cached-AKA TTL, local-session CHECK constraints.

### B.6.2 Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-IOPS-001 `iops_config_crud` | UPSERT + GET per-gNB IOPS config. |
| TC-IOPS-002 `iops_lifecycle` | Declare → status reflects iops_activated → Restore → normal. |
| TC-IOPS-003 `iops_cached_credentials` | Bulk cache → list → local-auth probe → delete. |
| TC-IOPS-004 `iops_local_sessions` | Bad service_type → 400; create → list → release. |
| TC-IOPS-005 `iops_events` | After Declare + Restore, all four event_types appear in log. |
| TC-IOPS-006 `iops_service_catalog` | DefaultLocalServices returns emergency / ptt / voice / data. |

All six are wired into `tc_iops.py::ALL_IOPS_TCS` and pass against
the current core build.

## B.7 References

- **TS 23.401 Annex K**:
  - §K.1 — IOPS general description.
  - §K.2.1 — Operation of isolated public-safety networks.
  - §K.2.2 — UE configuration (IOPS-enabled USIM, dedicated PLMN).
  - §K.2.3 — IOPS network configuration (cached AKA tuples).
  - §K.2.4 — IOPS network establishment / termination.
  - §K.2.5 — UE mobility within / out of IOPS.
- **TS 22.346** — IOPS service requirements (TODO; not loaded
  locally).

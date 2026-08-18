# NWDAF Analytics — Operator REST Surface (TS 23.288 §6.1)

The operator-side REST surface for the NWDAF analytics-exposure
service: dashboard aggregator across the seven supported Analytics
IDs, per-ID one-shot, subscription CRUD, history, and service status.

The NF package itself (`nf/nwdaf`) implements the §6.1 procedures and
the §6.2 collection loop; this surface is the consumer-facing wiring
that drives `templates/nwdaf.html` and the operator API.

# Part A — Functional

## A.1 Why NWDAF analytics?

Operators need *what's happening across the network right now* without
having to scrape per-NF KPIs and roll them up themselves. NWDAF
realises that at the Stage 2 level: each Analytics ID computes one
operator-meaningful question over the data points the collection loop
has gathered from AMF / SMF / UPF / OAM, with a confidence score per
prediction (TS 23.288 §6.1.3).

Seven Analytics IDs are wired:

| ID | Spec clause | Question |
|----|-------------|----------|
| `NF_LOAD`            | TS 23.288 §6.5    | Is any NF approaching its design ceiling? |
| `UE_MOBILITY`        | TS 23.288 §6.7.2  | How many UEs registered/connected; mobility patterns? |
| `UE_COMMUNICATION`   | TS 23.288 §6.7.3  | Traffic volume + flow stability per UE / DNN. |
| `QOS_SUSTAINABILITY` | TS 23.288 §6.9    | Is the QoS target still being met (drop rate)? |
| `ABNORMAL_BEHAVIOUR` | TS 23.288 §6.7.5  | Anomaly detection across UEs and the control plane. |
| `PDU_SESSION`        | (mapped to §6.4) | Active PDU sessions, IP-pool occupancy, per-DNN counts. |
| `SLICE_LOAD`         | TS 23.288 §6.3   | Per-S-NSSAI session count + peaks. |

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **Nnwdaf_AnalyticsInfo** | Consumer NF → NWDAF | REST | TS 23.288 §6.1.2 / TS 29.520 | Mapped to `GET /api/nwdaf/analytics/{id}`. |
| **Nnwdaf_EventsSubscription** | Consumer NF → NWDAF | REST | TS 23.288 §6.1.1 / TS 29.520 | `POST /api/nwdaf/subscriptions` + `DELETE …/{sid}`. |
| **Data Collection** | Producer NF → NWDAF | in-process | TS 23.288 §6.2 | `nf/nwdaf/collectors` + `collectionLoop` (every 30s). |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/nwdaf/*` (this file). |

The dashboard aggregator (`GET /api/nwdaf/analytics`) is an
operator-side superset — one round-trip computes every ID. The
spec's Analytics Request operates on one ID at a time; the panel
needs all seven on each refresh, so we collapse the multi-call into
one server-side fan-out.

## A.3 Operator-visible behaviours

### A.3.1 §6.1.3 result + confidence

Every result row carries a `result` map (the per-ID payload — see
`nf/nwdaf/analytics/analytics.go` for the per-ID shape) and a
`confidence` ∈ [0, 1] (TS 23.288 §6.1.3 mandates per-prediction
confidence). Statistics-only outputs (no prediction) carry
`confidence=1.0`.

### A.3.2 Filter scope

Both the dashboard and the per-ID GET accept `?imsi=` / `?dnn=` query
parameters. The NWDAF computes against the cached data points and
*passes through* points with no IMSI / DNN tag (catch-all data) when
filtering — this is intentional so operator-initiated requests still
see network-wide data even when scoping to a target.

### A.3.3 Time window

`?window_sec=N` controls how far back the result computes from
(default 300s). The collection loop runs every 30s, so a 300s window
gives the analytics ~10 sample points to work with.

### A.3.4 Subscription persistence

Subscriptions live in `nwdaf_subscriptions`. `Unsubscribe` flips
status to `cancelled` rather than deleting the row — the audit trail
is preserved and the notification loop picks up the status change on
its next tick.

## A.4 Operator REST API (`/api/nwdaf/*`)

### A.4.1 Dashboard / one-shot

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/nwdaf/analytics?window_sec=N&imsi=&dnn=&min_confidence=F` | Compute every Analytics ID; one round-trip for the panel. `min_confidence ∈ [0,1]` filters out low-confidence results (TS 23.288 §6.1.3). |
| GET | `/api/nwdaf/analytics/{id}?window_sec=N&imsi=&dnn=&min_confidence=F` | Compute one ID (TS 23.288 §6.1.2 Analytics Request). When the result's confidence is below the threshold, the response carries `filtered_out: true` and `result: null`. |

`{id}` must be one of the seven supported IDs; unknown IDs return
400 with the allowlist in the error message. `min_confidence`
out-of-range or non-numeric is silently ignored (treated as 0).

### A.4.2 Subscriptions (TS 23.288 §6.1.1)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/nwdaf/subscriptions` | `{ok, subscriptions: [...]}` — every active subscription. |
| POST   | `/api/nwdaf/subscriptions` | `{consumer_nf, analytics_id, target_imsi?, target_dnn?, target_sst?, callback_url?, interval_sec?}` → `{ok, sub_id}`. |
| GET    | `/api/nwdaf/subscriptions/{sid}` | `{ok, subscription: {...}}`; 404 if unknown. |
| PATCH  | `/api/nwdaf/subscriptions/{sid}` | Sparse update — allow-list `target_imsi, target_dnn, target_sst, callback_url, interval_sec, status`. Bad `status` value or no allowed fields → 400. |
| DELETE | `/api/nwdaf/subscriptions/{sid}` | flip status → cancelled; 404 if unknown. |

### A.4.3 Data ingestion (TS 23.288 §6.2)

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/nwdaf/data` | `{source_nf, analytics_id, imsi?, dnn?, data_json, collected_at?}` → `{ok, id}`. Rejects unknown analytics_id, missing source_nf, non-JSON `data_json` with 400. The point lands in both the persisted `nwdaf_data_points` row and the in-memory cache so the next analytics call sees it without waiting for a collection-loop tick. |

### A.4.4 History + service status

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/nwdaf/recent?analytics_id=&limit=N` | Persisted §6.1.3 result rows from `nwdaf_analytics` table. |
| GET | `/api/nwdaf/status` | `{ok, cached_data_points, analytics_ids, supported_ids, ingest:{total, per_id}}` — for ops health checks. |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| TS 29.520 (Nnwdaf services Stage 3) JSON schemas | — | Surface keeps the same fields; not bit-exact to OpenAPI yet. |
| TS 23.288 §6.2.6.1 Bulked Data Collection | — | Single-NF collection only; bulk request not implemented. |
| TS 23.288 §6.2.7 Event Muting Mechanism | — | Notifications are not mutable; rely on subscription cancel instead. |

---

# Part B — Design

## B.1 Process layout

```
                ┌──────────────────────────────────────────────┐
                │  templates/nwdaf.html (panel)                │
                │  fetch('/api/nwdaf/analytics')               │
                └────────────────────┬─────────────────────────┘
                                     │
                                     ▼
                ┌──────────────────────────────────────────────┐
                │  routes_nwdaf_analytics.go                   │
                │   ├ GET    /api/nwdaf/analytics          (fan-out)
                │   ├ GET    /api/nwdaf/analytics/{id}     (§6.1.2)
                │   ├ GET    /api/nwdaf/subscriptions      (§6.1.1 list)
                │   ├ POST   /api/nwdaf/subscriptions      (§6.1.1 create)
                │   ├ DELETE /api/nwdaf/subscriptions/{sid}(§6.1.1 cancel)
                │   ├ GET    /api/nwdaf/recent             (§6.1.3 history)
                │   └ GET    /api/nwdaf/status             (service health)
                └────────────────────┬─────────────────────────┘
                                     │
                                     ▼
                ┌──────────────────────────────────────────────┐
                │  nwdaf.DefaultService                        │
                │   ├ GetAnalytics(id, imsi, dnn, window)      │
                │   ├ Subscribe / Unsubscribe / ListSub        │
                │   ├ GetRecentAnalytics(id, limit)            │
                │   ├ Status() — cached_data_points / IDs      │
                │   └ collectionLoop (30s tick)                │
                │      → collectors.CollectAll()               │
                │      → INSERT nwdaf_data_points              │
                └──────────────────────────────────────────────┘
                                     ▲
                                     │
   AMF / SMF / UPF / OAM (NF producers) push to collectors.CollectAll()
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `nf/nwdaf/nwdaf.go` | 480 | Service singleton, GetAnalytics, Subscribe, ListSubscriptions, collection loop. |
| `nf/nwdaf/analytics/analytics.go` | (slice) | Per-ID compute (NF load, mobility, QoS, anomalies, PDU, slice). |
| `nf/nwdaf/collectors/collectors.go` | (slice) | Producer-side data collection. |
| `webservice/app/routes_nwdaf_analytics.go` | ~180 | REST surface for §A.4 (this file). |
| `webservice/cmd/sacore-web/main.go` | (slice) | Calls `nwdaf.DefaultService.Start()` at boot. |

Tests:
- `mmt_studio_core_tester/src/testcases/oam/tc_nwdaf_analytics.py` — 7 live integration TCs.

## B.3 supportedAnalyticsIDs ordering

```go
var supportedAnalyticsIDs = []string{
    AnalyticsNFLoad, AnalyticsUEMobility, AnalyticsUECommunication,
    AnalyticsQoSSustainability, AnalyticsAbnormalBehaviour,
    AnalyticsPDUSession, AnalyticsSliceLoad,
}
```

Ordered slice rather than ranging the `ValidAnalyticsIDs` map so the
panel layout is deterministic across polls. The tester pins the
ordering as a contract via `EXPECTED_IDS`.

## B.4 Public API

```go
// Routes
func (s *Server) registerNWDAFAnalyticsRoutes()

// Service (consumed by the routes; stable)
nwdaf.DefaultService *Service

func (*Service) GetAnalytics(id, imsi, dnn string, windowSec int)
                             analytics.AnalyticsResult
func (*Service) Subscribe(consumerNF, analyticsID, targetIMSI,
                          targetDNN, targetSST, callbackURL string,
                          intervalSec int) string
func (*Service) Unsubscribe(subID string) bool
func (*Service) ListSubscriptions() []map[string]any
func (*Service) GetRecentAnalytics(id string, limit int) []map[string]any
func (*Service) Status() map[string]any

// Operator-API helpers (api.go) used by routes_nwdaf_analytics.go
func (*Service) IngestDataPoint(dp analytics.DataPoint) (int64, error)
func (*Service) GetSubscription(subID string) (map[string]any, error)
func (*Service) UpdateSubscription(subID string, patch map[string]any) (bool, error)
func (*Service) IngestStats() map[string]any
```

## B.5 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-NWDAF-A-001 `nwdaf_analytics_dashboard`        | every supported ID present; result+confidence per §6.1.3; ordering pinned |
| TC-NWDAF-A-002 `nwdaf_analytics_single`           | per-ID GET with window_sec; §6.1.3 fields present |
| TC-NWDAF-A-003 `nwdaf_analytics_bad_id`           | unknown analytics_id → 400 |
| TC-NWDAF-A-004 `nwdaf_subscribe_unsubscribe`      | round-trip create + list + cancel |
| TC-NWDAF-A-005 `nwdaf_subscribe_validation`       | bad analytics_id / missing consumer_nf → 400 |
| TC-NWDAF-A-006 `nwdaf_recent_history`             | persisted result rows readable via `/recent` |
| TC-NWDAF-A-007 `nwdaf_status`                     | service status reports cache + supported IDs |
| TC-NWDAF-A-010 `nwdaf_ingest_data_point`          | POST /data persists + bumps cache; bad analytics_id / missing source_nf / invalid data_json → 400 |
| TC-NWDAF-A-011 `nwdaf_confidence_threshold`       | `?min_confidence=` filters low-confidence results on aggregator + single-ID; out-of-range / non-numeric ignored |
| TC-NWDAF-A-012 `nwdaf_subscription_get_patch`     | GET + PATCH a subscription; bad status / unknown sid / unknown-key patch each return their proper code |

All ten wired into `tc_nwdaf_analytics.py::ALL_NWDAF_ANALYTICS_TCS`
and `tc_nwdaf_hardening.py::ALL_NWDAF_HARDENING_TCS`. All pass
against the current core build.

## B.6 References

- **TS 23.288** §6.1   — Procedures for analytics exposure (umbrella).
- **TS 23.288** §6.1.1 — Analytics Subscribe / Unsubscribe.
- **TS 23.288** §6.1.2 — Analytics Request.
- **TS 23.288** §6.1.3 — Contents of Analytics Exposure.
- **TS 23.288** §6.2   — Procedures for Data Collection.
- **TS 23.288** §6.3 / §6.5 / §6.7.2 / §6.7.3 / §6.7.5 / §6.9 — per-ID outputs.
- **TS 29.520** — Nnwdaf services Stage 3 (TODO; PDF not loaded).
- `docs/design/nf/nwdaf.md` — package internals (collectors, analytics engine).
- `docs/design/oam/nwdaf_exposure.md` — sibling Exposure Surface.

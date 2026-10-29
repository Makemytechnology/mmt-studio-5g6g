# NWDAF Exposure — AF / 3rd-Party Operator Surface (TS 23.288 §6.1.x via NEF)

The operator-side REST surface for exposing NWDAF analytics to AFs
and 3rd parties through the NEF: consumer registration with API keys,
per-consumer subscriptions, periodic notifications, and one-shot
queries. Mirrors the Nnef_AnalyticsExposure shape from TS 29.522 §4.4.

# Part A — Functional

## A.1 Why a separate exposure surface?

The internal `/api/nwdaf/*` surface is for SBI consumer NFs and the
operator dashboard. AFs and 3rd-party analytics consumers come in
through the NEF — they need:

- **Authentication** (TS 23.288 §6.2.9 user consent → today: per-
  consumer API key + analytics allow-list).
- **A different vocabulary** — AFs use the Stage-3 query strings
  (`ue_mobility`, `nf_load`, `qos_sustainability`, …) per TS 29.522
  §4.4, not the internal Analytics IDs.
- **An audit trail** — every query the AF makes is logged with
  `query_type ∈ {one_shot, subscription}` so operators can bill or
  audit per-consumer usage.
- **A subscription notifier** — the periodic loop that POSTs
  notifications to the AF's `callback_url` on its declared
  `interval_s`.

The exposure package owns all four; this surface is the wiring.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **AF analytics request via NEF** | AF → NEF → NWDAF | REST | TS 23.288 §6.1.2.2 / TS 29.522 §4.4 | `GET /api/nwdaf/exposure/analytics/{type}` (one-shot). |
| **AF analytics subscribe via NEF** | AF → NEF → NWDAF | REST | TS 23.288 §6.1.1.2 / TS 29.522 §4.4 | `POST /api/nwdaf/exposure/subscriptions` + notifier loop. |
| **Notification** | NWDAF → AF callback | REST | TS 23.288 §6.1.3 | `SubscriptionManager.CheckAndNotify` posts every `interval_s`. |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/nwdaf/exposure/*` (this file). |

The package's `ExposureTypes` map translates the AF-facing Stage-3
strings to internal Analytics IDs. The same internal compute path
serves both the dashboard and the exposure surface — the difference
is the audit + auth gate that the exposure surface adds.

## A.3 Operator-visible behaviours

### A.3.1 Consumer registration + auto-minted API key

`POST /api/nwdaf/exposure/consumers` registers a consumer. If the
caller doesn't supply `api_key`, the route mints a random 32-char
hex key (`exposure.GenerateAPIKey`) and returns it in the response —
operators copy that key into the AF's config.

The optional `allowed_analytics` field is the per-consumer allow-list:
when set, only queries for those analytics types are honoured (others
return 403). The route validates each entry against the canonical
`analytics.ValidAnalyticsIDs` set so a typo in registration doesn't
silently lock the consumer out at query time.

### A.3.2 Subscription target_type vocabulary

Three target scopes per the schema CHECK:
- `imsi` — analytics scoped to a single UE (`target_id` is the IMSI).
- `slice` — analytics scoped to one S-NSSAI (`target_id` is the slice key).
- `network` — network-wide (`target_id` is null).

The panel sends `target_imsi` / `target_slice` as separate fields;
the route resolves these into the canonical `(target_type, target_id)`
form. Direct API callers can use either.

### A.3.3 One-shot query authentication

`GET /api/nwdaf/exposure/analytics/{type}` is the AF's primary path.
The route accepts an optional `X-API-Key` header — when present, it:

1. Looks up the consumer (`ValidateAPIKey`); 401 on unknown / inactive.
2. Checks `CheckAnalyticsPermission` against `allowed_analytics`;
   403 on disallowed type.
3. Logs the query as `query_type='one_shot'` with the resolved
   consumer_id and the response code.

Without `X-API-Key`, the route is anonymous (consumer_id=NULL in the
log). Operators decide which mode to deploy by whether they configure
a proxy that requires the header.

### A.3.4 Notification audit trail

The background `SubscriptionManager` loop runs every 10s, walks every
active subscription, and POSTs to its `callback_url` if the elapsed
time since `last_notified_at` exceeds `interval_s`. Each notification
writes `query_type='subscription'` with the HTTP response code from
the AF's callback — operators can quickly filter the log to find
failing callbacks.

## A.4 Operator REST API (`/api/nwdaf/exposure/*`)

### A.4.1 Stats / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/nwdaf/exposure/stats` | `{ok, active_consumers, active_subscriptions, total_queries, one_shot_queries, subscription_notifications}`. |
| GET | `/api/nwdaf/exposure/types` | `{ok, types: [{type, internal_id, description}, ...]}` — Stage-3 vocabulary. |

### A.4.2 Consumers

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/nwdaf/exposure/consumers`        | `{ok, consumers: [...]}`. |
| POST   | `/api/nwdaf/exposure/consumers`        | `{name, callback_url?, api_key?, allowed_analytics?}` → `{ok, id, api_key}`. |
| GET    | `/api/nwdaf/exposure/consumers/{id}`   | Single consumer; 404 if unknown. |
| PATCH  | `/api/nwdaf/exposure/consumers/{id}`   | Sparse update — `name, callback_url, allowed_analytics, active`. Bad analytics_id in `allowed_analytics` → 400. |
| POST   | `/api/nwdaf/exposure/consumers/{id}/rotate-key` | Generate new API key; old key immediately invalid. Audit-logged via LogQuery. |
| DELETE | `/api/nwdaf/exposure/consumers/{id}`   | FK CASCADE deletes the consumer's subscriptions. |

### A.4.3 Subscriptions

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/nwdaf/exposure/subscriptions`        | `{ok, subscriptions: [...]}`. |
| POST   | `/api/nwdaf/exposure/subscriptions`        | `{consumer_id, analytics_type, target_type?\|target_imsi?\|target_slice?, interval_s?, callback_url?}` → `{ok, id}`. |
| GET    | `/api/nwdaf/exposure/subscriptions/{id}`   | Single subscription; 404 if unknown. |
| PATCH  | `/api/nwdaf/exposure/subscriptions/{id}`   | Sparse update — `target_type, target_id, interval_s, callback_url, active`. Bad target_type → 400. |
| DELETE | `/api/nwdaf/exposure/subscriptions/{id}`   | hard-delete the subscription. |

### A.4.4 One-shot query + audit log

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/nwdaf/exposure/analytics/{type}?imsi=&slice=&window_sec=` | TS 23.288 §6.1.2.2 one-shot; optional `X-API-Key` header. UE-scoped queries (`imsi=`) gate on TS 23.288 §6.2.9 user consent. |
| POST | `/api/nwdaf/exposure/check-permission` | `{api_key, exposure_type, supi?}` → `{allowed, reason, consent?}`. Dry-run probe: same logic as a real query but does **not** bump the audit log. |
| GET | `/api/nwdaf/exposure/log?consumer_id=&type=&query_type=&since=&limit=` | Audit log with filters. `query_type ∈ {subscription, one_shot}`; bad value → 400. |

### A.4.5 User consent (TS 23.288 §6.2.9)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/nwdaf/exposure/consent/policy` | `{ok, mode}` — `opt_in` (default-deny without consent) or `opt_out` (default-allow). |
| POST   | `/api/nwdaf/exposure/consent/policy` | `{mode}` — set the global mode; bad value → 400. |
| GET    | `/api/nwdaf/exposure/consent?consumer_id=&limit=` | List consent rows. |
| POST   | `/api/nwdaf/exposure/consent` | `{consumer_id, supi, allow, reason?}` — record (or update) a per-(consumer, SUPI) consent row. UNIQUE(consumer_id, supi) → re-POST is an idempotent update. |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| TS 29.522 §5 OpenAPI schemas | — | Surface keeps the same fields; not bit-exact yet. |
| TS 23.288 §6.2.9 user consent | — | Only per-consumer allow-list enforced; no global consent gate. |
| Subscription notification retry | — | One POST attempt per tick; HTTP error logged, no exponential backoff. |
| Slice key → S-NSSAI mapping | — | `slice` query string is plumbed through as the `dnn` slot of `GetAnalytics` — works for the SLICE_LOAD analytic, not yet a clean S-NSSAI lookup. |

---

# Part B — Design

## B.1 Process layout

```
           ┌──────────────────────────────────────────────┐
           │  AF / 3rd-party  (X-API-Key + Stage-3 type)  │
           └────────────────────┬─────────────────────────┘
                                │
                                ▼
           ┌──────────────────────────────────────────────┐
           │  routes_nwdaf_exposure.go                    │
           │   ├ /stats /types                            │
           │   ├ /consumers (CRUD + auto-mint API key)    │
           │   ├ /subscriptions (CRUD; resolve target_*)  │
           │   ├ /analytics/{type} (one-shot, §6.1.2.2)   │
           │   └ /log                                     │
           └────────────────────┬─────────────────────────┘
                                │
                                ▼
           ┌──────────────────────────────────────────────┐
           │  nf/nwdaf/exposure                           │
           │   ├ ListConsumers / CreateConsumer …         │
           │   ├ ListSubscriptions / CreateSubscription   │
           │   ├ GenerateAPIKey / ValidateAPIKey          │
           │   ├ CheckAnalyticsPermission (allow-list)    │
           │   ├ LogQuery / GetLog / GetStats             │
           │   └ SubscriptionManager  ── loop ──┐         │
           └────────────────┬─────────────────┬─┘         │
                            │                 │           │
                            ▼                 ▼           │
                ┌──────────────────────┐  ┌──────────────┐
                │ nwdaf_exposure_*     │  │ AF callback  │ ◀ POST notification
                │ tables (consumers,   │  │ (HTTP)       │   {sub_id, result, ts}
                │ subs, log)           │  └──────────────┘
                └────────────┬─────────┘
                             │ feeds /analytics/{type} via
                             │ nwdaf.DefaultService.GetAnalytics
                             ▼
                ┌──────────────────────┐
                │ nf/nwdaf (analytics) │
                │  → GetAnalytics      │
                └──────────────────────┘
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `nf/nwdaf/exposure/exposure.go` | 560 | Consumer + subscription + log CRUD; API-key + permission helpers; `SubscriptionManager` notifier. |
| `webservice/app/routes_nwdaf_exposure.go` | ~280 | REST surface for §A.4 (this file). |
| `db/schemas/domains.go` | (slice) | `nwdaf_exposure_consumers` / `nwdaf_exposure_subscriptions` / `nwdaf_exposure_log` DDL. |

Tests:
- `mmt_studio_core_tester/src/testcases/oam/tc_nwdaf_exposure.py` — 8 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS nwdaf_exposure_consumers (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  name              TEXT NOT NULL UNIQUE,
  callback_url      TEXT,
  api_key           TEXT UNIQUE,
  allowed_analytics TEXT,   -- JSON array or CSV; empty = all allowed
  active            INTEGER NOT NULL DEFAULT 1,
  created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS nwdaf_exposure_subscriptions (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  consumer_id       INTEGER NOT NULL
                    REFERENCES nwdaf_exposure_consumers(id) ON DELETE CASCADE,
  analytics_type    TEXT NOT NULL,
  target_type       TEXT NOT NULL CHECK(target_type IN ('imsi','slice','network')),
  target_id         TEXT,
  interval_s        INTEGER NOT NULL DEFAULT 60,
  callback_url      TEXT,
  active            INTEGER NOT NULL DEFAULT 1,
  last_notified_at  TEXT,
  created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS nwdaf_exposure_log (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  consumer_id       INTEGER,
  analytics_type    TEXT,
  query_type        TEXT NOT NULL CHECK(query_type IN ('subscription','one_shot')),
  response_code     INTEGER,
  created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

-- TS 23.288 §6.2.9 user consent — per-(consumer, SUPI) decision.
CREATE TABLE IF NOT EXISTS nwdaf_user_consent (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  consumer_id   INTEGER NOT NULL
                REFERENCES nwdaf_exposure_consumers(id) ON DELETE CASCADE,
  supi          TEXT NOT NULL,
  allow         INTEGER NOT NULL DEFAULT 1,
  reason        TEXT NOT NULL DEFAULT '',
  recorded_at   TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(consumer_id, supi)
);

-- Single-row policy table; mode controls behaviour when no
-- per-UE row exists. opt_in is the safer default.
CREATE TABLE IF NOT EXISTS nwdaf_consent_policy (
  id          INTEGER PRIMARY KEY,
  mode        TEXT NOT NULL DEFAULT 'opt_in'
              CHECK(mode IN ('opt_in','opt_out')),
  updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
```

The CASCADE on `consumer_id` is what makes `DELETE /consumers/{id}`
safe — every subscription is removed atomically with the consumer.

## B.4 Stage-3 → internal Analytics ID map

```go
var ExposureTypes = map[string]string{
    "ue_mobility":         "UE_MOBILITY",
    "ue_communication":    "UE_COMMUNICATION",
    "nf_load":             "NF_LOAD",
    "network_performance": "QOS_SUSTAINABILITY",
    "abnormal_behaviour":  "ABNORMAL_BEHAVIOUR",
    "qos_sustainability":  "QOS_SUSTAINABILITY",
    "pdu_session":         "PDU_SESSION",
    "slice_load":          "SLICE_LOAD",
}
```

The route consults this map on every `/analytics/{type}` request and
returns 400 + writes a `query_type='one_shot'` log row with the bad
type for unknown entries.

## B.5 Public API

```go
// Routes
func (s *Server) registerNWDAFExposureRoutes()
func (s *Server) registerNWDAFExposureHardeningRoutes()

// Package surface (consumed by routes; stable)
func ListConsumers() ([]map[string]any, error)
func GetConsumer(consumerID int64) (map[string]any, error)
func CreateConsumer(name, callbackURL, apiKey string,
                    allowedAnalytics []string) (int64, error)
func UpdateConsumer(consumerID int64, patch map[string]any) (map[string]any, error)
func RotateAPIKey(consumerID int64) (string, map[string]any, error)
func DeleteConsumer(consumerID int64) error
func ListSubscriptions() ([]map[string]any, error)
func GetSubscription(subID int64) (map[string]any, error)
func CreateSubscription(consumerID int64, analyticsType, targetType,
                        targetID string, intervalS int,
                        callbackURL string) (int64, error)
func UpdateSubscription(subID int64, patch map[string]any) (map[string]any, error)
func DeleteSubscription(subID int64) error
func LogQuery(consumerID *int64, analyticsType, queryType string,
              responseCode int)
func GetLog(limit int) ([]map[string]any, error)
type LogFilter struct{ ConsumerID *int64; AnalyticsType, QueryType, Since string; Limit int }
func GetLogFiltered(f LogFilter) ([]map[string]any, error)
func GetStats() (map[string]any, error)
func ListExposureTypes() []TypeInfo
func GenerateAPIKey() string
func ValidateAPIKey(apiKey string) (map[string]any, error)
func CheckAnalyticsPermission(consumer map[string]any,
                              analyticsType string) bool

// User consent (TS 23.288 §6.2.9)
const ConsentModeOptIn  = "opt_in"
const ConsentModeOptOut = "opt_out"
func GetConsentMode() string
func SetConsentMode(mode string) error
func SetConsent(consumerID int64, supi string, allow bool, reason string) error
func GetConsent(consumerID int64, supi string) (allow, exists bool, err error)
func ListConsent(consumerID int64, limit int) ([]map[string]any, error)
func ConsentAllowed(consumerID int64, supi string) (bool, string)

// Permission probe (dry-run)
func CheckPermission(apiKey, exposureType, supi string) map[string]any
func MarshalConsumerAllowed(row map[string]any)
```

## B.6 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-NWDAF-E-001 `exposure_types`              | /types lists Stage-3 vocabulary entries |
| TC-NWDAF-E-002 `exposure_consumer_crud`      | register → list → delete; auto-minted api_key returned |
| TC-NWDAF-E-003 `exposure_consumer_validation`| missing name / unknown analytics_id in allow-list → 400 |
| TC-NWDAF-E-004 `exposure_subscription_crud`  | subscribe (target_imsi → resolved to imsi target_type) → list → delete |
| TC-NWDAF-E-005 `exposure_subscription_validation` | missing consumer_id / bad target_type → 400 |
| TC-NWDAF-E-006 `exposure_oneshot_query`      | §6.1.2.2 one-shot with vocab translation; bad type → 400 |
| TC-NWDAF-E-007 `exposure_api_key_gate`       | invalid key → 401; allow-list mismatch → 403 |
| TC-NWDAF-E-008 `exposure_stats_and_log`      | stats reflect query counts; log records each query |
| TC-NWDAF-E-010 `nwdaf_exposure_consumer_patch_rotate` | consumer GET/PATCH/rotate-key; old key 401 / new key 200; bad analytics_id in PATCH → 400 |
| TC-NWDAF-E-011 `nwdaf_exposure_subscription_patch` | subscription GET/PATCH; bad target_type → 400 |
| TC-NWDAF-E-012 `nwdaf_exposure_log_filters` | `?consumer_id=&type=&query_type=` filter; bad value → 400 |
| TC-NWDAF-E-013 `nwdaf_exposure_permission_probe` | `/check-permission` allows / denies without firing a real query |
| TC-NWDAF-E-014 `nwdaf_exposure_consent_gate` | TS 23.288 §6.2.9: opt_in default-deny without consent; explicit consent flips bit; opt_out flips default; slice/network scopes are ungated |

All thirteen wired into `tc_nwdaf_exposure.py::ALL_NWDAF_EXPOSURE_TCS`
+ `tc_nwdaf_hardening.py::ALL_NWDAF_HARDENING_TCS`. All pass against
the current core build.

## B.7 References

- **TS 23.288** §6.1.1.2 — AF analytics subscribe / unsubscribe via NEF.
- **TS 23.288** §6.1.2.2 — AF analytics request via NEF (one-shot).
- **TS 23.288** §6.1.3   — Contents of Analytics Exposure (notification + one-shot payload).
- **TS 29.522** §4.4     — NEF northbound APIs (Nnef_AnalyticsExposure).
- **TS 29.522** §5       — JSON schemas (TODO; not loaded locally).
- **TS 23.288** §6.2.9   — User consent for analytics (TODO; allow-list only today).
- `docs/design/oam/nwdaf_analytics.md` — sibling internal-consumer surface.
- `docs/design/nf/nwdaf.md` — package internals (collectors, analytics engine).

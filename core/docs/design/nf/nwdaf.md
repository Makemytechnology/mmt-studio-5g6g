# NWDAF — Design Document

3GPP TS 23.288-aligned Network Data Analytics Function for the MMT 5G
Core. Periodically collects state from AMF/SMF/UPF (and in time, OAM
+ AFs), computes per-Analytics-ID outputs, persists results to
`nwdaf_analytics`, and pushes notifications to subscribed consumer
NFs / AFs.

## 1. Role in 5GC

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Nnwdaf** | consumer NF (AMF/SMF/PCF/...) | Nnwdaf_AnalyticsInfo + Nnwdaf_EventsSubscription | TS 29.520 — TODO scaffold |
| (data collection) | producer NF | Nnf_EventExposure / OAM | TS 23.288 §6.2 — in-process today |
| (exposure) | AF via NEF | Nnef_AnalyticsExposure | TS 29.522 — `exposure/` partial |

The Go NWDAF runs in-process and pulls its data via local function
calls (today: a placeholder stub that returns zeros until the real
producer wiring lands — see `collectors/collectors.go:48-71`). Outputs
follow TS 23.288 §6.1.3 ("Contents of Analytics Exposure"): per
Analytics ID a `result` map plus a `confidence` score for predictions.

## 2. Architecture

```
                ┌──────────────────────────────────────────────────┐
                │   Consumer NF (AMF / SMF / PCF) or AF via NEF    │
                └─────────┬────────────────────────────┬───────────┘
                          │ Subscribe / GetAnalytics     │ Exposure pull/push
                          ▼                              ▼
┌────────────────────────  NWDAF process  ───────────────────────────────┐
│                                                                         │
│  nwdaf.go (Service singleton)                                            │
│   • Start: spawns collectionLoop goroutine                              │
│   • GetAnalytics(analyticsID, targetIMSI, targetDNN, timeWindow)        │
│       — TS 23.288 §6.1.2 (one-shot Analytics Request)                   │
│   • Subscribe / Unsubscribe / ListSubscriptions                         │
│       — TS 23.288 §6.1.1 (Analytics Subscribe/Unsubscribe)              │
│   • collectionLoop: tick every collectInterval (default 30 s)           │
│       → collectAll  → DB INSERT into nwdaf_data_points + cache          │
│       → processSubscriptions: per-sub, if interval elapsed call         │
│         GetAnalytics + sendNotification(callbackURL)                    │
│   • dataCache: map[analyticsID][]DataPoint (cap maxCachePoints=500)    │
│                                                                         │
│  analytics/ — TS 23.288 §6.x compute engines per Analytics ID           │
│    NF_LOAD                §6.5                                          │
│    UE_MOBILITY            §6.7.2                                        │
│    UE_COMMUNICATION       §6.7.3                                        │
│    QOS_SUSTAINABILITY     §6.9                                          │
│    ABNORMAL_BEHAVIOUR     §6.7.5                                        │
│    SLICE_LOAD             §6.3                                          │
│    PDU_SESSION            (mapped to UE_COMMUNICATION; §6.4 TODO)       │
│                                                                         │
│  collectors/ — TS 23.288 §6.2 Data Collection                           │
│    CollectAMFData / CollectSMFData / CollectUPFData                     │
│    (today: zero-stubs; §6.2.3 OAM + §6.2.6.1 bulked TODO)              │
│                                                                         │
│  exposure/ — TS 23.288 §6.1.1.2 / §6.1.2.2 Analytics exposure to AFs    │
│    via NEF; §6.1.3 notification body shape; consumer CRUD,             │
│    subscriptions, audit log, API-key auth, background notifier.        │
└─────────────────────────────────────────────────────────────────────────┘
                          │ DB persist + read
                          ▼
                    db/engine
                  (nwdaf_data_points,
                   nwdaf_analytics,
                   nwdaf_subscriptions,
                   exposure_consumers,
                   exposure_subscriptions,
                   exposure_audit)
```

## 3. Package / file map

| Package | LOC | Role |
|---------|-----|------|
| `nf/nwdaf` (root) | ~480 | `Service` singleton (`DefaultService`). `Start`/`Stop`, `GetAnalytics`, `Subscribe`/`Unsubscribe`/`ListSubscriptions`, `GetRecentAnalytics`, `Status`. `collectionLoop` + `processSubscriptions` + HTTP-notify path. |
| `nf/nwdaf/analytics` | ~600 | Compute engine. `ComputeAnalytics(id, points, timeWindow) → AnalyticsResult`. `DataPoint`/`AnalyticsResult` types. `ValidAnalyticsIDs` set. Per-Analytics-ID `compute<X>` functions. |
| `nf/nwdaf/collectors` | ~250 | `CollectAll` + `CollectAMFData`, `CollectSMFData`, `CollectUPFData` (placeholder stubs today — see §7). |
| `nf/nwdaf/exposure` | ~700 | NEF-side exposure. `ExposureTypes` mapping (`ue_mobility` → `UE_MOBILITY`...). Consumer CRUD, subscription CRUD, audit log, API-key auth (`X-API-Key`), `processSubscriptions` notifier. |

## 4. SBI / interactions

### 4.1 Analytics IDs (TS 23.288 §6.x)

| Constant | TS 23.288 § | What it computes | Mapped to |
|----------|------------|-----------------|-----------|
| `NF_LOAD` | §6.5 | Per-NF load level over the time window | `computeNFLoad` |
| `UE_MOBILITY` | §6.7.2 | UE mobility statistics | `computeUEMobility` |
| `UE_COMMUNICATION` | §6.7.3 | Traffic patterns + periodicity | `computeUECommunication` |
| `QOS_SUSTAINABILITY` | §6.9 | QoS achievable per area | `computeQoSSustainability` |
| `ABNORMAL_BEHAVIOUR` | §6.7.5 | UE / device anomalies | `computeAbnormalBehaviour` |
| `PDU_SESSION` | (TODO §6.4) | Mapped onto UE_COMMUNICATION until §6.4 lands | reuse |
| `SLICE_LOAD` | §6.3 | Per-S-NSSAI load level | `computeSliceLoad` |

### 4.2 Service operations

```
TS 23.288 §6.1.2 Analytics Request (one-shot):
   GetAnalytics(analyticsID, targetIMSI, targetDNN, timeWindow) → AnalyticsResult
       1. Read recent points from dataCache[analyticsID]
       2. Filter by IMSI / DNN scope
       3. analytics.ComputeAnalytics(...)
       4. Persist row in nwdaf_analytics
       5. Return result {Result map, Confidence, DataPointsUsed,
          TimeWindowSec, ComputedAt}.

TS 23.288 §6.1.1 Analytics Subscribe/Unsubscribe:
   Subscribe(consumerNF, analyticsID, targetIMSI, targetDNN, targetSST,
             callbackURL, intervalSec) → subID
       INSERT into nwdaf_subscriptions WHERE status='active'
   Unsubscribe(subID) → bool
       UPDATE nwdaf_subscriptions SET status='cancelled'

TS 23.288 §6.2 Data Collection cycle:
   collectionLoop tick (default 30 s):
       collectors.CollectAll() → []DataPoint
       INSERT into nwdaf_data_points
       Prune nwdaf_data_points WHERE collected_at < now-86400
       Update s.dataCache[analyticsID] (cap maxCachePoints=500)
       processSubscriptions:
           for each active sub: if (now - last_notified) >= intervalSec
               result = GetAnalytics(...)
               sendNotification(callbackURL, result)
               UPDATE nwdaf_subscriptions SET last_notified=...
```

### 4.3 Exposure to AFs via NEF

`exposure/` provides:
- `ExposureTypes` map flipping external query strings (`"ue_mobility"`)
  to internal Analytics IDs (`"UE_MOBILITY"`).
- Consumer CRUD with API-key auth (`X-API-Key` header).
- Per-consumer allow-list (`CheckAnalyticsPermission`).
- Subscription background notifier emitting per TS 23.288 §6.1.3
  body shape.
- Audit log of every analytics serve.

## 5. Lifecycle — Subscribe + periodic notification

```
Consumer NF                    NWDAF Service                      DB
 │                                │                                │
 │── Subscribe(consumerNF,       │                                │
 │     "NF_LOAD",                │                                │
 │     callbackURL=                                               │
 │       "http://amf:8080/onload",│                                │
 │     intervalSec=60) ──────────▶│                                │
 │                                │ allocate subID                │
 │                                │ INSERT nwdaf_subscriptions ──▶│
 │◄── subID ──────────────────────│                                │
 │                                │                                │
 │  --- 30 s --- collectionLoop tick                               │
 │                                │ collectors.CollectAll()        │
 │                                │ → INSERT nwdaf_data_points    │
 │                                │ → s.dataCache[NF_LOAD] += []  │
 │                                │ processSubscriptions:          │
 │                                │   sub due? yes                 │
 │                                │   result = GetAnalytics(...)  │
 │                                │   POST callbackURL with        │
 │                                │     {sub_id, analytics_id,    │
 │                                │      result, timestamp}        │
 │◄── HTTP POST                ──┤                                │
 │   notify body                  │                                │
 │                                │ UPDATE nwdaf_subscriptions     │
 │                                │   SET last_notified=now ─────▶│
 │                                │                                │
 │  ... 60 s later ...                                             │
 │  Loop continues                                                  │
 │                                                                  │
 │── Unsubscribe(subID) ─────────▶│ UPDATE status='cancelled' ────▶│
```

## 6. Key types / public API

```go
// nwdaf/nwdaf.go
type Service struct{ /* mu sync.Mutex; running; stopCh; collectInterval; dataCache; maxCachePoints */ }
func NewService(collectIntervalSec int) *Service
var DefaultService = NewService(30)

func (s *Service) Start()
func (s *Service) Stop()
func (s *Service) GetAnalytics(analyticsID, targetIMSI, targetDNN string, timeWindow int) analytics.AnalyticsResult
func (s *Service) Subscribe(consumerNF, analyticsID, targetIMSI, targetDNN, targetSST,
                            callbackURL string, intervalSec int) string
func (s *Service) Unsubscribe(subID string) bool
func (s *Service) ListSubscriptions() []map[string]any
func (s *Service) GetRecentAnalytics(analyticsID string, limit int) []map[string]any
func (s *Service) Status() map[string]any

// nwdaf/analytics/analytics.go
const (
    AnalyticsNFLoad            = "NF_LOAD"             // §6.5
    AnalyticsUEMobility        = "UE_MOBILITY"         // §6.7.2
    AnalyticsUECommunication   = "UE_COMMUNICATION"    // §6.7.3
    AnalyticsQoSSustainability = "QOS_SUSTAINABILITY"  // §6.9
    AnalyticsAbnormalBehaviour = "ABNORMAL_BEHAVIOUR"  // §6.7.5
    AnalyticsPDUSession        = "PDU_SESSION"         // §6.4 TODO
    AnalyticsSliceLoad         = "SLICE_LOAD"          // §6.3
)
var ValidAnalyticsIDs map[string]bool

type DataPoint struct {
    SourceNF, AnalyticsID, IMSI, DNN string
    DataJSON string
    CollectedAt float64
}
type AnalyticsResult struct {
    AnalyticsID    string
    Result         map[string]any
    Confidence     float64
    Message        string
    ComputedAt     float64
    DataPointsUsed int
    TimeWindowSec  int
}

func ComputeAnalytics(analyticsID string, dataPoints []DataPoint, timeWindow int) AnalyticsResult

// nwdaf/collectors/collectors.go
func CollectAll() []analytics.DataPoint
func CollectAMFData() []analytics.DataPoint
func CollectSMFData() []analytics.DataPoint
func CollectUPFData() []analytics.DataPoint

// nwdaf/exposure/exposure.go
var ExposureTypes map[string]string  // "ue_mobility" → "UE_MOBILITY" (etc.)

// (Consumer + subscription CRUD + APIKey auth + Audit + notifier — see file)
func RegisterConsumer(name, apiKey string, allowedAnalyticsIDs []string) error
func CheckAnalyticsPermission(consumerName, analyticsID string) bool
func AddSubscription(...) (string, error)
func RunNotifier(stop chan struct{})
```

## 7. What's not implemented

Grepped TODOs in `nf/nwdaf/`:

| Area | Status | Source |
|------|--------|--------|
| TS 29.520 Stage-3 OpenAPI surface (`Nnwdaf_AnalyticsInfo`, `Nnwdaf_EventsSubscription` JSON schemas) | not implemented; in-process API | `nwdaf.go:21-23, 98, 167` |
| TS 23.288 §6.2.6.1 Bulked Data Collection | not implemented | `nwdaf.go:25, collectors.go:17` |
| TS 23.288 §6.2.7 Event Muting Mechanism | not implemented | `nwdaf.go:27` |
| TS 23.288 §6.2.3 Data Collection from OAM | not pulled | `collectors.go:14` |
| TS 23.288 §6.2.4 Correlation between network + service data | each DataPoint stays single-NF | `collectors.go:19` |
| TS 23.288 §6.4 Observed Service Experience proper compute | aliased to UE_COMMUNICATION | `analytics.go:17, 445` |
| Real producer-side data collection | `CollectAMFData`/`CollectSMFData`/`CollectUPFData` are zero-value stubs (`recover` swallow) — `collectors.go:55-71` says verbatim "in production, import amf/uectx and query" | `collectors.go:55-99` |
| TS 29.522 §5 Nnef_AnalyticsExposure full OpenAPI shape | partial — `ExposureTypes` mapping only | `exposure/exposure.go:17-19` |
| TS 23.288 §6.2.9 User consent gating | absent — only per-consumer allow-list | `exposure/exposure.go:20-22` |
| Confidence score for predictions | computed but heuristic | `analytics.go` per-ID functions |
| Authentication on Subscribe / GetAnalytics (consumer-NF identity) | exposure has API-Key; main NWDAF service does not | `nwdaf.go` Subscribe takes consumerNF string |

## 8. References

Spec citations grepped from `nf/nwdaf/`:

- **TS 23.288** v18+ §6.1 Procedures for analytics exposure, §6.1.1
  Analytics Subscribe/Unsubscribe, §6.1.2 Analytics Request, §6.1.3
  Contents of Analytics Exposure, §6.2 Data Collection, §6.2.2 from
  NFs, §6.2.3 from OAM (TODO), §6.2.4 correlation (TODO), §6.2.6.1
  Bulked Data Collection (TODO), §6.2.7 Event Muting (TODO), §6.2.9
  user consent (TODO), §6.3 Slice load, §6.4 Observed Service
  Experience (TODO), §6.5 NF load, §6.7.2 UE mobility, §6.7.3 UE
  communication, §6.7.5 Abnormal behaviour, §6.9 QoS sustainability
- **TS 29.520** Nnwdaf services Stage 3 — TODO scaffold at every entry point
- **TS 29.522** §4.4 Northbound APIs at NEF — exposure routes mirror
  `Nnef_EventExposure`

---
*Last refreshed against commit `13a181d`.*

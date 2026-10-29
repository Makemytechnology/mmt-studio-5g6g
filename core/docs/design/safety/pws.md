# PWS — Public Warning System

Operator-side Public Warning System surface for the SA Core: alert
CRUD + state machine (draft → broadcasting → completed | cancelled),
per-gNB delivery ledger, statistics, and a one-click "test drill"
helper. The AMF-side N2 (NGAP) fan-out lives in
`nf/amf/pws/dispatch.go`; this package owns the inputs and the
audit trail.

# Part A — Functional

## A.1 Why PWS?

3GPP-defined PWS lets a public-safety authority (or the operator
acting as a CBE) push an emergency cell-broadcast message to every
camped UE in a target area:

| Variant | Anchor | Notes |
|---------|--------|-------|
| **ETWS** | Earthquake & Tsunami Warning | Japan-originated; primary + secondary notification |
| **CMAS** | Commercial Mobile Alert System | US WEA / IPAWS path |
| **EU-Alert** | European unified channel | Wraps ETWS + national channels |
| **test** | Drill / monitor | Same wire path; UE-side suppressible |

5GS architecture is anchored at TS 23.501 §4.4.1 + §5.16.1 and defers
the on-wire encoding to TS 23.041 (Cell Broadcast Service). The
AMF-side N2 procedures (Write-Replace, PWS Cancel, PWS Restart
Indication, PWS Failure) are in TS 38.413 §8.9.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **CBE → CBC** | CBE (operator panel) → CBC | SBc-AP / proprietary | TS 23.041 | This panel **is** the CBE; we accept alerts directly (TODO: SBc-AP). |
| **CBC → AMF (N50)** | CBC → AMF | SBc-AP | TS 23.041 | Not present — CBC is collapsed into the panel today. |
| **N2 (AMF → NG-RAN)** | AMF → gNB | NGAP Warning Message Transmission | TS 38.413 §8.9 | Live in `nf/amf/pws/dispatch.go`. |
| **CB-DATA** | gNB → UE | RRC SystemInformation | TS 23.041 | RAN-side; outside this surface. |

## A.3 Operator-visible behaviours

### A.3.1 Alert lifecycle

```
   ┌──────┐  Broadcast   ┌──────────────┐  Complete   ┌───────────┐
   │draft │ ───────────► │broadcasting  │ ──────────► │completed  │
   └──┬───┘              └──────┬───────┘             └───────────┘
      │                         │
      │ Cancel                  │ Cancel
      ▼                         ▼
   ┌─────────────────────────────────┐
   │           cancelled             │
   └─────────────────────────────────┘
```

Each transition is a separate REST endpoint so the operator can stage
and review wording before anything goes on the air. `Broadcast`
rejects non-draft alerts (HTTP 400); `Cancel` accepts either draft or
broadcasting. `Complete` is for "the warning is over" without a hard
Cancel that would also kill cached UE state.

### A.3.2 Per-gNB delivery ledger

Every NGAP outcome from the AMF-side fan-out is recorded in
`pws_delivery_log` with `status ∈ {pending, delivered, failed,
acknowledged}`. The `/api/pws/alerts/{id}/delivery-status` endpoint
joins this against `pws_alerts` to surface a `delivery_summary` map
the GUI displays as `Delivered: N  Failed: N  Pending: N  Ack: N`.

### A.3.3 Allow-listed vocabularies

| Field | Values | Anchor |
|-------|--------|--------|
| `alert_type` | `etws` / `cmas` / `eu_alert` / `test` | TS 23.041 |
| `severity` | `extreme` / `severe` / `moderate` / `minor` / `unknown` | CMAS / EU-Alert |
| `urgency` | `immediate` / `expected` / `future` / `past` / `unknown` | CMAS / EU-Alert |
| `status` | `draft` / `broadcasting` / `completed` / `cancelled` | this package |

All four are CHECK-constrained at the schema layer; the route returns
HTTP 400 for any out-of-vocabulary input.

### A.3.4 Test alerts (drill)

`POST /api/pws/test-alert` is a one-click shortcut: it creates a
minimal `alert_type=test` alert and immediately broadcasts it. Used
for monthly / quarterly system drills without the operator having to
fill out the full creation form.

### A.3.5 CBS encoding preview

`EncodeCBSMessage(text, msg_id, serial_number)` returns a metadata
sketch — page count (max 15 per TS 23.041), text length, `gsm7`
encoding tag — so the operator can preview "this alert will take N
pages" before broadcast. Live GSM 7-bit packing is a TODO; the CBC
normally does it.

## A.4 Operator REST API (`/api/pws/*`)

All endpoints return `{ok, ...}` with the body keyed by domain noun.

### A.4.1 Stats / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/pws/stats` | `{ok, stats: {total_alerts, alerts_by_status, total_deliveries}}` |
| GET | `/api/pws/status` | alias of `/stats` |

### A.4.2 Alerts (TS 23.501 §5.16.1)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/pws/alerts?status=…&alert_type=…` | list (server-side `status` filter, client-side `alert_type` filter) |
| POST   | `/api/pws/alerts` | create draft (allow-listed vocabulary) |
| GET    | `/api/pws/alerts/{id}` | one alert |
| DELETE | `/api/pws/alerts/{id}` | remove (cascades to delivery log) |
| POST   | `/api/pws/alerts/{id}/broadcast` | flip draft → broadcasting (TS 38.413 §8.9.1) |
| POST   | `/api/pws/alerts/{id}/cancel`    | flip draft\|broadcasting → cancelled (TS 38.413 §8.9.2) |
| POST   | `/api/pws/alerts/{id}/complete`  | flip broadcasting → completed |
| POST   | `/api/pws/test-alert` | create+broadcast a `test` alert |

### A.4.3 Delivery ledger (TS 38.413 §9.1.9)

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/pws/alerts/{id}/delivery` | record one row `{gnb_id, status}` |
| GET    | `/api/pws/alerts/{id}/delivery-status` | per-alert summary `{alert_status, total_gnbs, delivery_summary, deliveries}` |
| GET    | `/api/pws/delivery-log?limit=N` | global delivery log (newest first), joined with `message_id`/`alert_type` |

### A.4.4 CBS encoding preview

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/pws/encode-preview?text=X&message_id=N&serial_number=K` | placeholder GSM-7 page count + length |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| ETWS / CMAS message structure on the CBE → CBC leg | TS 23.041 | Not loaded locally; everything CBS-wire is a TODO until the PDF lands |
| CB-DATA / CB-PAGE encoding (GSM 7-bit packing, language indicator, page count) | TS 23.041 | `EncodeCBSMessage` is a metadata sketch only |
| Serial Number / Message Identifier allocation rules | TS 23.041 | Today we randomise; operator allocation rules are deferred |
| SBc-AP between CBC and AMF | TS 23.041 | Panel **is** the CBE; we accept alerts directly |

---

# Part B — Design

## B.1 Process layout

```
┌──────────────────── safety/pws ────────────────────┐
│                                                    │
│  Alert CRUD + state machine     Delivery ledger    │
│   pws_alerts                     pws_delivery_log  │
│   ├── CreateAlert                ├── RecordDelivery
│   ├── GetAlert / ListAlerts      ├── GetDeliveries │
│   ├── BroadcastAlert             └── ListDeliveryLog
│   ├── CancelAlert                                  │
│   ├── CompleteAlert                                │
│   └── DeleteAlert                                  │
│                                                    │
│  CBS encoding preview            Stats             │
│   EncodeCBSMessage                GetStats         │
│      pages = ceil(len/82)           total_alerts   │
│      max 15 pages (TS 23.041)       alerts_by_status
│      encoding=gsm7 (TODO)           total_deliveries
│                                                    │
└────────────────────────────────────────────────────┘
              │
              │  consumed by
              ▼
       nf/amf/pws/dispatch.go    ← AMF-side NGAP fan-out
       (Write-Replace / Cancel /  TS 38.413 §8.9
        Restart Indication)
              │
              ▼
            gNBs
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `safety/pws/pws.go` | ~390 | Alert CRUD + state machine + delivery ledger + stats + CBS preview. |
| `safety/pws/pws_test.go` | ~150 | Vocabulary validation, state transitions, delivery log, encoding preview. |
| `db/schemas/domains.go` | (slice) | DDL for `pws_alerts` + `pws_delivery_log`. |
| `webservice/app/routes_pws.go` | ~265 | REST surface for §A.4. |
| `nf/amf/pws/dispatch.go` | (separate) | NGAP fan-out (Write-Replace / Cancel). |

Tests:
- `mmt_studio_core_tester/src/testcases/safety/tc_pws.py` — 7 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS pws_alerts (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id          INTEGER NOT NULL,
  serial_number       INTEGER NOT NULL,
  alert_type          TEXT NOT NULL DEFAULT 'cmas'
                      CHECK (alert_type IN ('etws','cmas','eu_alert','test')),
  severity            TEXT NOT NULL DEFAULT 'unknown'
                      CHECK (severity IN ('extreme','severe','moderate','minor','unknown')),
  urgency             TEXT NOT NULL DEFAULT 'unknown'
                      CHECK (urgency IN ('immediate','expected','future','past','unknown')),
  category            TEXT NOT NULL DEFAULT 'safety',
  message_text        TEXT NOT NULL DEFAULT '',
  language            TEXT NOT NULL DEFAULT 'en',
  target_areas        TEXT,                         -- JSON-encoded TAI list
  number_of_broadcasts INTEGER NOT NULL DEFAULT 10,
  repetition_period_s INTEGER NOT NULL DEFAULT 60,
  status              TEXT NOT NULL DEFAULT 'draft'
                      CHECK (status IN ('draft','broadcasting','completed','cancelled')),
  created_at          TEXT NOT NULL DEFAULT (datetime('now')),
  broadcast_at        TEXT,
  completed_at        TEXT
);

CREATE TABLE IF NOT EXISTS pws_delivery_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  alert_id        INTEGER NOT NULL,
  gnb_id          TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','delivered','failed','acknowledged')),
  delivered_at    TEXT,
  ack_at          TEXT,
  FOREIGN KEY (alert_id) REFERENCES pws_alerts(id) ON DELETE CASCADE
);
```

`ON DELETE CASCADE` on the delivery log keeps the per-gNB rows in
lock-step with the alert; the GUI can confidently show "alert deleted
→ delivery log gone too."

## B.4 State machine

```go
BroadcastAlert(id):    requires status='draft'                     → 'broadcasting'
CancelAlert(id):       accepts status IN ('draft','broadcasting')  → 'cancelled'
CompleteAlert(id):     requires status='broadcasting'              → 'completed'
DeleteAlert(id):       any state; cascades to delivery_log
```

Each transition is a single UPDATE … WHERE id=? AND status=...; if
the WHERE fails (wrong source state), the package returns an error
and the route maps it to HTTP 400.

## B.5 Public API

```go
const (
    StatusDraft        = "draft"
    StatusBroadcasting = "broadcasting"
    StatusCompleted    = "completed"
    StatusCancelled    = "cancelled"
)

// CRUD + state machine
func CreateAlert(config map[string]interface{}) (map[string]interface{}, error)
func GetAlert(id int64) (map[string]interface{}, error)
func ListAlerts(status string) ([]map[string]interface{}, error)
func BroadcastAlert(id int64) (map[string]interface{}, error)
func CancelAlert(id int64) (map[string]interface{}, error)
func CompleteAlert(id int64) (map[string]interface{}, error)
func DeleteAlert(id int64) error

// Delivery ledger
func RecordDelivery(alertID int64, gnbID, status string) error
func GetDeliveries(alertID int64) ([]map[string]interface{}, error)
func ListDeliveryLog(limit int) ([]map[string]interface{}, error)

// CBS preview (TODO: real GSM-7 packing)
func EncodeCBSMessage(text string, msgID, serialNum int) map[string]interface{}

// Stats / GUI panel
func GetStats() map[string]interface{}
func List() ([]map[string]any, error)   // alias of ListAlerts("")
func Status() map[string]any            // alias of GetStats
```

## B.6 Test coverage

### B.6.1 Go unit tests

`safety/pws/pws_test.go` — vocabulary validation, state-machine
transitions (allowed + rejected), delivery-log RecordDelivery /
GetDeliveries, EncodeCBSMessage page count.

### B.6.2 Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-PWS-001 `pws_create_alert` | Create → status=draft, CHECK-validated alert_type/severity/urgency. |
| TC-PWS-002 `pws_validation` | Invalid alert_type / severity / urgency / empty message_text → 400. |
| TC-PWS-003 `pws_broadcast_alert` | draft → broadcasting; re-broadcast 400. |
| TC-PWS-004 `pws_cancel_alert` | broadcasting → cancelled (TS 38.413 §8.9.2). |
| TC-PWS-005 `pws_delivery_status` | record 4 deliveries (delivered/failed/ack) → summary tallies match. |
| TC-PWS-006 `pws_test_alert` | one-click drill: alert_type=test, status=broadcasting. |
| TC-PWS-007 `pws_stats` | stats reports `total_alerts`, `alerts_by_status`, `total_deliveries`. |

All seven are wired into `tc_pws.py::ALL_PWS_TCS` and pass against the
current core build.

## B.7 References

- **TS 23.501**:
  - §4.4.1 — PWS architecture (defers wire to TS 23.041).
  - §5.16.1 — PWS functional description.
- **TS 23.041** — Cell Broadcast Service realisation (TODO; not loaded locally).
- **TS 38.413**:
  - §8.9 — NGAP Warning Message Transmission Procedures (Write-Replace,
    PWS Cancel, PWS Restart Indication, PWS Failure).
  - §9.1.9 — Warning-related IEs.

# nidd — Design Document

Non-IP Data Delivery (NIDD) for the MMT 5G Core. The package
captures the SCEF role from **TS 23.682 §5.13** and the NB-IoT
CP-CIoT small-data path from **TS 23.401 §4.3.17**.

## 1. Role / Scope

NIDD is the SCEF-mediated path that lets an SCS/AS exchange Non-IP
data with a UE without provisioning an IP PDN connection. The
SCEF terminates T6a/T6b on the EPC side and a T8 northbound API
to the SCS/AS. This package owns:

| Concern | Spec | Implementation |
|---------|------|----------------|
| NIDD configuration row (per UE+APN+AS) | TS 23.682 §5.13.2 | `iot_nidd_sessions` |
| Mobile Originated NIDD | TS 23.682 §5.13.4 | `SendMO` → `nidd_data_log` UL |
| Mobile Terminated NIDD | TS 23.682 §5.13.3 | `SendMT` — buffered if UE asleep |
| High-latency DL delivery | TS 23.682 §5.13.3 | `FlushBuffered` on UE wake |
| App-server registry (T8 callbacks) | (TS 29.122 — TODO) | `RegisterAppServer` / `nidd_app_servers` |
| CP CIoT NAS-borne small data | TS 23.401 §4.3.17 | `iot_cp_data` table + `AppendCP` / `MarkCPDelivered` / `PendingCP` |

The MO/MT routing into `delivered` vs `buffered` is driven by the
UE state passed in by the caller (typically `nbiot.GetPSM(imsi).State`).

Out of scope:

- Wire-level T8 API (TODO `TS 29.122`).
- RDS / reliable-delivery acks (TODO `TS 24.250 §6`).
- Authentication of the SCS/AS — `auth_token` is persisted but not
  validated here.

## 2. Architecture

```
         ┌─────────────────────────────────────────────┐
         │   SCS / AS  (T8 callback consumer)          │
         └────────────────┬────────────────────────────┘
                          │ callback_url (HTTP)
                          │ TODO TS 29.122
                          ▼
┌────────────────────────────────────────────────────────────────┐
│  iot/nidd                                                      │
│                                                                │
│  Sessions (TS 23.682 §5.13.2)                                  │
│   CreateSession / GetSession / FindSession / ListSessions /    │
│   SuspendSession / TerminateSession                            │
│                                                                │
│  MO/MT path (TS 23.682 §5.13.3 / §5.13.4)                      │
│   SendMO   ── INSERT nidd_data_log direction=UL status=delivered│
│   SendMT   ── INSERT nidd_data_log direction=DL                │
│                  status = (delivered | buffered) by UE state   │
│   FlushBuffered ── UPDATE buffered → delivered on UE wake      │
│                                                                │
│  AS registry                                                   │
│   RegisterAppServer / ListAppServers                           │
│                                                                │
│  CP CIoT data (TS 23.401 §4.3.17)                              │
│   AppendCP / MarkCPDelivered / PendingCP                       │
│                                                                │
│  GUI panel:  List() / Status()                                 │
└──────────────┬─────────────────────────────────────────────────┘
               │
               ▼
┌────────────────────────────────────────────────────────────────┐
│  db/engine — SQLite                                            │
│   iot_nidd_sessions                                            │
│   nidd_data_log                                                │
│   nidd_app_servers                                             │
│   iot_cp_data                                                  │
└────────────────────────────────────────────────────────────────┘
               ▲
               │ caller passes UE state from
               │ iot/nbiot.GetPSM(imsi).State
               │
┌────────────────────────────────────────────────────────────────┐
│ iot/nbiot — PSM state machine (read-only consumer-side)        │
└────────────────────────────────────────────────────────────────┘
```

## 3. File / Package Map

| File | LOC | Role |
|------|-----|------|
| `iot/nidd/nidd.go` | 486 | All four concerns: sessions, MO/MT, AS registry, CP CIoT |
| `iot/nidd/nidd_test.go` | 267 | Per-concern coverage incl. high-latency buffering |

Tables touched (schema lives elsewhere — `db/migrations`):

| Table | Used by |
|-------|---------|
| `iot_nidd_sessions` | `CreateSession` / `GetSession` / `FindSession` / `ListSessions` / `SuspendSession` / `TerminateSession` |
| `nidd_data_log` | `SendMO` / `SendMT` / `FlushBuffered` / `GetLog` / `ListLogs` |
| `nidd_app_servers` | `RegisterAppServer` / `ListAppServers` |
| `iot_cp_data` | `AppendCP` / `MarkCPDelivered` / `PendingCP` / `List` |

## 4. Public API

```go
// Session lifecycle (TS 23.682 §5.13.2 / §5.13.5 / §5.13.8)
func CreateSession(imsi, sessionID, apn, appServerURL string) (*Session, error)
func GetSession(id int64) (*Session, error)
func FindSession(imsi, apn string) (*Session, error)
func ListSessions(imsi string) ([]Session, error)
func SuspendSession(id int64) error
func TerminateSession(id int64) error

// MO NIDD (TS 23.682 §5.13.4)
func SendMO(sessionID int64, payload []byte) (*DataLog, error)

// MT NIDD (TS 23.682 §5.13.3)
//   ueState ∈ {"" | "active" | "sleeping" | "unreachable"}
//   sleeping/unreachable → status='buffered'; else 'delivered'
func SendMT(sessionID int64, payload []byte, ueState string) (*DataLog, error)

// High-latency drain (TS 23.682 §5.13.3) — UE has woken from PSM
func FlushBuffered(sessionID int64) (int, error)

// AS registry (T8 callbacks — TS 29.122 northbound is TODO)
func RegisterAppServer(appServerID, name, callbackURL, authToken string) (*AppServer, error)
func ListAppServers() ([]AppServer, error)

// CP CIoT NAS-borne small data (TS 23.401 §4.3.17)
func AppendCP(imsi, direction string, payload []byte, apn *string) (*CPData, error)
func MarkCPDelivered(id int64) error
func PendingCP(imsi string) ([]CPData, error)

// Logs
func GetLog(id int64) (*DataLog, error)
func ListLogs(sessionID int64, limit int) ([]DataLog, error)

// GUI panel
func List() ([]map[string]any, error)
func Status() map[string]any
```

Validation gates (return errors rather than silently normalising):

- `CreateSession`: IMSI / APN / `app_server_url` are required; if
  `sessionID` is empty, generate `nidd-<imsi>-<unix-nano>`.
- `SendMO` / `SendMT`: empty payload rejected; session must exist
  and be `active`.
- `AppendCP`: direction must be `"UL"` or `"DL"`; empty payload
  rejected.

## 5. Lifecycle (one MT NIDD message to a sleeping UE)

```
SCS/AS                   SCEF (iot/nidd)              UE (NB-IoT)
  │                            │                           │
  │── Configure NIDD (T8) ────▶│  CreateSession            │
  │       imsi, apn,           │   INSERT iot_nidd_sessions│
  │       app_server_url       │   status='active'         │
  │                            │                           │
  │── MT data (T8) ───────────▶│  SendMT(payload,          │
  │       payload              │     ueState=nbiot.GetPSM. │
  │                            │             State)        │
  │                            │   if state in             │
  │                            │     {sleeping,            │
  │                            │      unreachable}         │
  │                            │     INSERT nidd_data_log  │
  │                            │       status='buffered'   │
  │                            │   else                    │
  │                            │     status='delivered'    │
  │                            │                           │
  │                            │     (UE eventually does   │
  │                            │      MO TAU → nbiot.Wake) │
  │                            │ ◄── nbiot.Wake(imsi)      │
  │                            │                           │
  │                            │  FlushBuffered(sessID)    │
  │                            │   UPDATE nidd_data_log    │
  │                            │     SET status='delivered'│
  │                            │     WHERE buffered AND DL │
  │                            │                           │
  │                            │── deliver via NAS ───────▶│
```

CP CIoT path (TS 23.401 §4.3.17) is the parallel small-data
delivery path used when the UE has CP-CIoT EPS optimisation
negotiated (see `nbiot.Capabilities.CPCIoTSupported`):

```
MME / SCEF                        UE
   │                              │
   │── AppendCP(imsi, DL, ...) ──▶│  enqueue iot_cp_data row
   │                              │  delivered=0
   │                              │
   │  (NAS PDU acked)             │
   │── MarkCPDelivered(id) ──────▶│  delivered=1, delivered_at=now
   │                              │
   │── PendingCP(imsi) — poll ───▶│  reads remaining DL rows on UE wake
```

## 6. Key Types

```go
type Session struct {
    ID                       int64
    IMSI, SessionID, APN,
    AppServerURL, Status,
    CreatedAt                string
}

type CPData struct {
    ID                       int64
    IMSI, Direction          string  // direction = UL | DL
    DataPayload              []byte
    APN                      *string
    Delivered                bool
    CreatedAt                string
    DeliveredAt              *string
}

type DataLog struct {
    ID, SessionID            int64
    Direction, DataHex,
    Status, CreatedAt        string
    DataLength               int
    DeliveredAt              *string
}

type AppServer struct {
    ID                       int64
    AppServerID, Name,
    CallbackURL, AuthToken,
    CreatedAt                string
}
```

`Status` enum on `nidd_data_log`: `pending | delivered | buffered |
failed | expired`. On `iot_nidd_sessions`: `active | suspended |
terminated`.

## 7. Stubs / TODOs

| Site | TS | Comment |
|------|-----|---------|
| `nidd.go:31` | TS 29.122 | Wire the SCEF T8 northbound API (NIDD configuration / subscription / message delivery) when the spec PDF is added to `specs/3gpp/` |
| `nidd.go:34` | TS 24.250 §6 | Wrap on-the-wire RDS PDUs (SAPI / sequence number / ACK) onto the `iot_cp_data` payload when reliable delivery is requested |
| `nidd.go:338` | TS 29.122 | Bind app servers to formal `Nnef_NIDD` APIs once T8 spec PDF is loaded |

## 8. References

Spec citations grounded in `iot/nidd/nidd.go`:

- **TS 23.682 §5.13** — Non-IP Data Delivery procedures (overall
  procedure set).
- **TS 23.682 §5.13.1** — T6a/T6b connection establishment.
- **TS 23.682 §5.13.2** — NIDD Configuration. Verbatim:
  *"the SCS/AS may use the NIDD Configuration procedure to set up
  the parameters under which the SCEF will provide NIDD services
  to the SCS/AS for a specific UE"*.
- **TS 23.682 §5.13.3** — Mobile Terminated NIDD; high-latency
  communication path (origin of `status='buffered'`).
- **TS 23.682 §5.13.4** — Mobile Originated NIDD.
- **TS 23.682 §5.13.5** — T6a/T6b connection release (drives
  `TerminateSession`).
- **TS 23.682 §5.13.8** — NIDD Authorisation Update (drives
  `SuspendSession`).
- **TS 23.401 §4.3.17** — Support for Machine Type Communications;
  CP CIoT optimisation drives `iot_cp_data`.
- **TS 23.401 §4.3.17.8** — Support for NIDD at the EPC bearer
  level.
- **TS 24.301** — referenced for ESM Data Transport message type
  carrying `iot_cp_data` payloads.
- **TS 29.122** — T8 SCEF northbound API (TODO).
- **TS 24.250 §6** — RDS reliable-delivery PDUs (TODO).

---
*Last refreshed against commit `13a181d`.*

# MBS — 5G Multicast / Broadcast Services

Operator-of-record surface for 5G MBS in the SA Core. Owns the
session lifecycle (created → activated → deactivated), the service-
area registry (TAI-list scoping), member management for multicast
sessions, immediate + scheduled content delivery, and the audit log.

# Part A — Functional

## A.1 Why MBS?

5G MBS lets the network deliver one stream of bytes to many UEs
without unicast bandwidth multiplication: video (live events, public
safety video), software updates, IoT firmware fan-out, mission-
critical group calls. The architecture is in TS 23.247:

| Concept | Anchor |
|---------|--------|
| MBS Session (TMGI keyed) | TS 23.247 §4.1 / §7 |
| Multicast vs Broadcast | TS 23.247 §4.1 |
| MBS Service Area (TAI list) | TS 23.247 §7.2 |
| MBSF / MBSU / MB-UPF / MB-SMF | TS 23.247 §4.2 |
| Service requirements | TS 22.146 / TS 22.246 |

This package is the operator-/MBSF-facing projection. It does **not**
encode N6mb, MBSU, or MB-UPF traffic; it owns the data the GUI panel
operates on and the lifecycle gating the wire layer follows.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **N33mb** | AF → MBSF | SBI | TS 23.247 §4.2 | Out of scope; this surface is panel-only. |
| **N6mb** | MB-UPF → AF | UDP/IP | TS 23.247 §4.2 | Out of scope; we record content-log metadata only. |
| **MB-N9 / N3mb** | MB-UPF ↔ NG-RAN | GTP-U | TS 23.247 §4.2 | Wire-side; outside this surface. |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/mbs/*` (this file). |

## A.3 Operator-visible behaviours

### A.3.1 Session lifecycle (TS 23.247 §7)

```
   ┌──────┐  Activate    ┌──────────┐  Deactivate  ┌─────────────┐
   │created│ ───────────►│ activated│ ────────────►│deactivated │
   └──┬────┘              └──────────┘             └─────────────┘
      │                                                   ▲
      │                            (terminal — only delete or recreate)
      └──────────── Delete ─────────────────────────────────────────►
```

Only `created → activated` and `activated → deactivated` are accepted;
any other transition (re-activate, re-deactivate, sending content
before activation) returns HTTP 400. The schema's CHECK constraint
on `status` makes the database the second line of defence.

### A.3.2 Service areas (TS 23.247 §7.2)

A `mbs_areas` row carries `(name UNIQUE, tracking_areas, description)`
where `tracking_areas` is a comma-separated TAI-list string. Sessions
optionally point at one area via the `area_id` FK; deleting an area
sets dependent sessions' `area_id=NULL` (audit trail wins).

### A.3.3 Member management (multicast)

Members join via IMSI; `INSERT OR IGNORE` makes re-joining the same
session idempotent. `LeaveSession` stamps `left_at` on the row but
keeps it for audit. `ListMembers` returns active members (where
`left_at IS NULL`) before the historical ones.

### A.3.4 Content delivery

`SendContent` is the immediate-fan-out path: it requires the session
to be in `activated` state, snapshots the current active-member count
into `recipients_count`, and writes one `mbs_content_log` row with
`status='delivered'`. The wire payload itself is **not** stored; only
metadata (content_type, content_size, recipients_count, timestamps).

`ScheduleContent` records a deferred delivery with `status='pending'`.
A future MBSTF integration would honour the `scheduled_at` clock and
flip the row to `delivering` → `delivered` or `failed`; today the row
is just an audit-log entry.

## A.4 Operator REST API (`/api/mbs/*`)

All endpoints return `{ok, ...}` envelopes keyed by domain noun.

### A.4.1 Stats / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/mbs/stats` | `{ok, stats: {total_sessions, active_sessions, multicast_sessions, broadcast_sessions, active_members, delivered_content}}` |
| GET | `/api/mbs/status` | alias of `/stats` |

### A.4.2 Sessions (TS 23.247 §7)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/mbs/sessions?session_type=…&status=…` | list (filter by type / status); each row carries `member_count` |
| POST   | `/api/mbs/sessions` | create `{tmgi, name, session_type, qos_5qi, area_id?, max_bitrate_kbps}` |
| GET    | `/api/mbs/sessions/{id}` | one session |
| DELETE | `/api/mbs/sessions/{id}` | remove (cascades to members + content log) |
| POST   | `/api/mbs/sessions/{id}/activate` | created → activated |
| POST   | `/api/mbs/sessions/{id}/deactivate` | activated → deactivated |

### A.4.3 Members (multicast)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/mbs/sessions/{id}/members` | active first (`left_at IS NULL`) then history |
| POST   | `/api/mbs/sessions/{id}/join` | `{imsi}`, idempotent |
| POST   | `/api/mbs/sessions/{id}/leave` | `{imsi}`, stamps `left_at` |

### A.4.4 Service areas (TS 23.247 §7.2)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/mbs/areas` | list |
| POST   | `/api/mbs/areas` | create `{name, tracking_areas, description?}` — TAI list validated per TS 23.003 §19.4.2 |
| DELETE | `/api/mbs/areas/{id}` | remove (sessions' `area_id` set NULL) |
| POST   | `/api/mbs/areas/{id}/tais` | mutate TAI list `{append?, remove?}` — idempotent re-append; bad TAI → 400 |

### A.4.5 Content delivery

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/mbs/sessions/{id}/send` | immediate `{content_type, content_data}`; only when status=activated |
| POST   | `/api/mbs/sessions/{id}/schedule` | deferred `{content_type, content_data, deliver_at}` |
| GET    | `/api/mbs/content-log?limit=N` | newest-first audit rows |

### A.4.6 Spec-compliance gates at write time

| Field | Validator | Spec |
|-------|-----------|------|
| `tmgi` | `ValidateTMGI` — accepts 12–14 hex (raw) or `<6hex>@<MCC>.<MNC>.mbms.3gppnetwork.org` (FQDN). | TS 23.003 §15.2 |
| `qos_5qi` | `Validate5QI` — `[1, 255]`. | TS 23.501 Table 5.7.4-1 / §5.7.4 (non-standardised range) |
| `tracking_areas` | `ValidateTAIList` — every entry matches `<MCC><MNC>-<TAC>` with TAC = 6 hex chars. | TS 23.003 §19.4.2 |
| `max_bitrate_kbps` | `ValidateBitrate` — non-negative. | — |

Bad input surfaces as a 400 with the spec citation, not as a 500
from a wire-side codec or schema CHECK.

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| MBSF / MBSU / MB-UPF / MB-SMF wire-protocol details | TS 23.247 §6 | Out of scope; this surface is operator-/audit-only. |
| MBS-CDR charging | TS 23.247 §8 | SBI / charging concern; not here. |
| Real wire fan-out | TS 23.247 §7 | We persist `recipients_count` from the live member list; we don't actually transmit. |
| `delivering` → `delivered` clock-driven flip | this package | Scheduled rows stay `pending` until a future MBSTF integration. |

---

# Part B — Design

## B.1 Process layout

```
┌────────────── safety/mbs ──────────────┐
│                                        │
│  Sessions (TS 23.247 §7)               │
│   mbs_sessions                         │
│   ├── CreateSession                    │
│   ├── ActivateSession                  │
│   ├── DeactivateSession                │
│   ├── ListSessions   (with member_count)
│   ├── GetSession                       │
│   └── DeleteSession                    │
│                                        │
│  Members                               │
│   mbs_members  (UNIQUE(session_id, imsi))
│   ├── JoinSession  (INSERT OR IGNORE)  │
│   ├── LeaveSession (stamps left_at)    │
│   └── ListMembers                      │
│                                        │
│  Service areas                         │
│   mbs_areas    (UNIQUE(name))          │
│   ├── CreateArea / ListAreas           │
│   └── DeleteArea  (FK SET NULL)        │
│                                        │
│  Content delivery                      │
│   mbs_content_log                      │
│   ├── SendContent                      │
│   ├── ScheduleContent                  │
│   └── ListContentLog                   │
│                                        │
│  Stats                                 │
│   GetStats — total/active/multi/broad/ │
│              members/delivered         │
└────────────────────────────────────────┘
                  ▲
                  │ JSON / HTTP
                  │
        webservice/app/routes_mbs.go
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `safety/mbs/mbs.go` | ~360 | Sessions + members + areas + content + stats. |
| `db/schemas/domains.go` | (slice) | DDL for `mbs_areas`, `mbs_sessions`, `mbs_members`, `mbs_content_log`. |
| `webservice/app/routes_mbs.go` | ~290 | REST surface for §A.4. |

Tests:
- `mmt_studio_core_tester/src/testcases/safety/tc_mbs.py` — 6 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS mbs_areas (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  name            TEXT NOT NULL UNIQUE,
  tracking_areas  TEXT NOT NULL,
  description     TEXT,
  created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS mbs_sessions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  tmgi            TEXT NOT NULL UNIQUE,
  name            TEXT,
  session_type    TEXT NOT NULL DEFAULT 'multicast'
                  CHECK (session_type IN ('multicast','broadcast')),
  status          TEXT NOT NULL DEFAULT 'created'
                  CHECK (status IN ('created','activated','deactivated')),
  qos_5qi         INTEGER NOT NULL DEFAULT 9,
  area_id         INTEGER,
  max_bitrate_kbps INTEGER,
  created_at      TEXT NOT NULL DEFAULT (datetime('now')),
  activated_at    TEXT,
  FOREIGN KEY (area_id) REFERENCES mbs_areas(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS mbs_members (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id      INTEGER NOT NULL,
  imsi            TEXT NOT NULL,
  joined_at       TEXT NOT NULL DEFAULT (datetime('now')),
  left_at         TEXT,
  UNIQUE (session_id, imsi),
  FOREIGN KEY (session_id) REFERENCES mbs_sessions(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS mbs_content_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id      INTEGER NOT NULL,
  content_type    TEXT NOT NULL,
  content_size    INTEGER NOT NULL DEFAULT 0,
  scheduled_at    TEXT,
  delivered_at    TEXT,
  recipients_count INTEGER NOT NULL DEFAULT 0,
  status          TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','delivering','delivered','failed')),
  FOREIGN KEY (session_id) REFERENCES mbs_sessions(id) ON DELETE CASCADE
);
```

The `UNIQUE(session_id, imsi)` makes `JoinSession` idempotent without
the application code having to check first; the `ON DELETE CASCADE`
on members and content_log keeps history aligned with the parent
session.

## B.4 Public API

```go
const (
    StatusCreated     = "created"
    StatusActivated   = "activated"
    StatusDeactivated = "deactivated"
    TypeMulticast     = "multicast"
    TypeBroadcast     = "broadcast"
)

// Sessions
func CreateSession(tmgi, name, sessionType string, qos5QI int,
    areaID *int64, maxBitrateKbps int) (map[string]interface{}, error)
func GetSession(id int64) (map[string]interface{}, error)
func ActivateSession(id int64) (map[string]interface{}, error)
func DeactivateSession(id int64) (map[string]interface{}, error)
func ListSessions(sessionType, status string) ([]map[string]interface{}, error)
func DeleteSession(id int64) error

// Members
func JoinSession(sessionID int64, imsi string) error
func LeaveSession(sessionID int64, imsi string) error
func ListMembers(sessionID int64) ([]map[string]interface{}, error)

// Areas
func CreateArea(name, trackingAreas, description string) (map[string]interface{}, error)
func ListAreas() ([]map[string]interface{}, error)
func DeleteArea(id int64) error

// Content delivery
func SendContent(sessionID int64, contentType string, contentSize int) (map[string]interface{}, error)
func ScheduleContent(sessionID int64, contentType string, contentSize int,
    deliverAt string) (map[string]interface{}, error)
func ListContentLog(limit int) ([]map[string]interface{}, error)

// Stats / GUI
func GetStats() map[string]interface{}
func List() ([]map[string]any, error)
func Status() map[string]any

// Validators (TS 23.003 §15.2 / §19.4.2, TS 23.501 §5.7.4)
func ValidateTMGI(tmgi string) error
func ValidateTAIList(taiList string) error
func Validate5QI(qi int) error
func ValidateBitrate(kbps int) error

// TAI list management on an existing area (TS 23.247 §7.2)
func AppendTAIs(areaID int64, tais []string) (map[string]interface{}, error)
func RemoveTAIs(areaID int64, tais []string) (map[string]interface{}, error)
```

## B.5 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-MBS-001 `mbs_session_lifecycle` | create → activate → deactivate; re-activate from deactivated rejected with 400. |
| TC-MBS-002 `mbs_validation` | invalid session_type / empty tmgi → 400. |
| TC-MBS-003 `mbs_members` | join (idempotent UNIQUE) → list → leave (`left_at` stamped). |
| TC-MBS-004 `mbs_areas` | area CRUD; empty `tracking_areas` → 400. |
| TC-MBS-005 `mbs_content_delivery` | send before activate → 400; activate → join → send → audit row in content-log. |
| TC-MBS-006 `mbs_stats` | stats reports `total_sessions / active_sessions / multicast_sessions / broadcast_sessions / active_members / delivered_content`. |

### Operator-API hardening tests (`tc_vas_oam.py`)

| TC | Coverage |
|----|----------|
| TC-MBS-010 `mbs_tmgi_validation`       | bad TMGI → 400; both raw 12-hex and FQDN forms accepted (TS 23.003 §15.2) |
| TC-MBS-011 `mbs_5qi_validation`        | 5QI 999 → 400 (TS 23.501 §5.7.4) |
| TC-MBS-012 `mbs_tai_validation`        | bad TAI in `tracking_areas` → 400; valid `<MCC><MNC>-<TAC>` accepted (TS 23.003 §19.4.2) |
| TC-MBS-013 `mbs_tai_list_management`   | append/remove TAIs on an existing area; idempotent re-append; bad TAI → 400; empty body → 400 |

All ten (six lifecycle + four hardening) pass against the current
core build.

## B.6 References

- **TS 22.146 / 22.246** — MBMS / MBS service requirements.
- **TS 23.247**:
  - §4.1 — 5G MBS architecture (umbrella).
  - §4.2 — MBS reference points.
  - §6 — TODO; MBSF / MBSU / MB-UPF / MB-SMF wire-protocol details.
  - §7 — MBS Session Procedures.
  - §7.2 — MBS service-area handling.
  - §8 — TODO; charging.

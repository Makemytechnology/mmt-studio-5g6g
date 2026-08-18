# Disaster Roaming

Disaster Roaming admission control for the SA Core: the operator-
side declaration lifecycle, the per-IMSI admission gate the AMF calls
during Initial Registration, the active-roaming-UE register, and the
audit log of every admit/deny decision.

# Part A — Functional

## A.1 Why Disaster Roaming?

When a partner PLMN suffers a disaster outage (earthquake, flood,
wildfire, deliberate jamming, …), its subscribers may not be able to
attach to their HPLMN. TS 22.261 §6.31 requires the serving PLMN to
admit those UEs even when no normal roaming agreement exists, for
the duration of the declared "Disaster Condition".

5GS architecture for this is in TS 23.501 §5.40:

| Subclause | Topic |
|-----------|-------|
| §5.40     | Disaster Roaming for PLMNs (architecture umbrella) |
| §5.40.2   | Disaster condition handling (declaration lifecycle) |
| §5.40.3   | Restrictions of services and applications |

This package owns the operator-/AMF-facing projection of the above.
It does **not** drive on-air NAS/NGAP itself; it computes the inputs
the AMF needs.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **Initial Registration** | UE → AMF | NAS over NGAP | TS 23.501 §5.40.3 | AMF calls `CheckDisasterRoaming(imsi, hplmn)` before deciding admission. |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/disaster-roaming/*` (this file). |
| **Service restriction** | SMF / PCF | per-DNN policy | TS 23.501 §5.40.3 | Out of scope here; the package only gates registration admission. |

## A.3 Operator-visible behaviours

### A.3.1 Declaration lifecycle

A `disaster_declarations` row carries:

| Field | Notes |
|-------|-------|
| `name` | free-text declaration name |
| `reason` | free-text reason / trigger |
| `affected_areas` | free-text geographic scope |
| `status` | `active` \| `ended` |
| `declared_by` | operator account |
| `declared_at` / `ended_at` | timestamps |

`DeclareDisaster(...)` inserts a new row in `active`; `EndDisaster()`
flips every active row to `ended` (and stamps `ended_at`).
`EndDisasterByID(id)` ends one specific row.

### A.3.2 Admission gate

`CheckDisasterRoaming(imsi, hplmn)` returns:

```json
{ "allowed": true,  "reason": "Disaster Condition active",
  "declaration_id": 7 }
```

or

```json
{ "allowed": false, "reason": "no active Disaster Condition" }
```

If `allowed=true` the IMSI is appended to `disaster_roaming_ues`
(scoped to the active declaration) — that's how the GUI shows
"who is currently riding the disaster gate."

### A.3.3 Active-roaming-UE register

`disaster_roaming_ues` rows survive the declaration ending — the
audit history must remain intact. `GetDisasterRoamingUEs()` JOINs
against `disaster_declarations WHERE status='active'`, so when the
declaration ends the register **appears** to clear from the panel
even though no rows are deleted.

### A.3.4 Audit log

Every `CheckDisasterRoaming` call emits a `disaster_roaming_log` row
with `action ∈ {admitted, denied, released}`. The `released` action
is logged when `ReleaseRoamingUE(imsi, hplmn)` is called explicitly
(today: a no-op on the register itself, but the log entry survives
for audit).

## A.4 Operator REST API (`/api/disaster-roaming/*`)

Endpoints return flat objects (or arrays) keyed by domain noun. No
`{ok, ...}` wrapping — the panel uses `d.disaster_active` style
direct property access.

### A.4.1 Status / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/disaster-roaming/status` | `{disaster_active, declaration}` |
| GET | `/api/disaster-roaming/stats` | `{total_declarations, total_admitted, total_denied, current_roaming_ues, active_declarations}` |

### A.4.2 Declaration lifecycle (TS 23.501 §5.40.2)

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/disaster-roaming/declare` | `{name, reason?, affected_areas?, declared_by?}` → `{ok, declaration_id, name}` |
| POST   | `/api/disaster-roaming/end` | end every active declaration (or one if `{declaration_id}` is supplied) |
| GET    | `/api/disaster-roaming/declarations` | full history (active + ended) |

### A.4.3 Admission + register

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/disaster-roaming/check` | `{imsi, hplmn}` → AMF-style `{allowed, reason, declaration_id?}` |
| GET    | `/api/disaster-roaming/roaming-ues` | UEs currently admitted under an active declaration |
| POST   | `/api/disaster-roaming/release` | log a "released" action for `{imsi, hplmn}` |

### A.4.4 Audit log

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/disaster-roaming/log?limit=N` | newest-first audit entries with `action ∈ {admitted, denied, released}` |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| Per-DNN service restriction during disaster | TS 23.501 §5.40.3 | Out of scope here; SMF/PCF concern |
| HPLMN-list authorisation (who can declare a disaster *for* whom?) | TS 23.501 §5.40.2 | Today the panel is a single trust boundary; no MNO-pairing rules |
| RAN-side disaster signalling (broadcast SIB telling UEs they may roam) | TS 38.331 | RAN-side; outside this surface |

---

# Part B — Design

## B.1 Process layout

```
┌──────────── safety/disaster_roaming ────────────┐
│                                                 │
│  Declaration lifecycle                          │
│   disaster_declarations                         │
│   ├── DeclareDisaster                           │
│   ├── EndDisaster / EndDisasterByID             │
│   ├── GetDisasterStatus / IsDisasterActive      │
│   ├── GetActiveDeclaration                      │
│   └── GetAllDeclarations                        │
│                                                 │
│  Admission gate                                 │
│   CheckDisasterRoaming(imsi, hplmn) →           │
│     - admit IF active Disaster Condition        │
│     - deny otherwise                            │
│   addRoamingUE(decl_id, imsi, hplmn)            │
│                                                 │
│  Roaming-UE register (audit-preserving)         │
│   disaster_roaming_ues                          │
│   GetDisasterRoamingUEs                         │
│      JOIN disaster_declarations WHERE active    │
│   ReleaseRoamingUE  (logs only; row stays)      │
│                                                 │
│  Audit log                                      │
│   disaster_roaming_log                          │
│   ├── logDR(imsi, hplmn, action, reason)        │
│   └── GetDRLog(limit)                           │
│                                                 │
└─────────────────────────────────────────────────┘
                         ▲
                         │ JSON / HTTP
                         │
          webservice/app/routes_disaster_roaming.go
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `safety/disaster_roaming/disaster_roaming.go` | ~340 | Declaration CRUD + admission probe + roaming-UE register + audit log + stats. |
| `safety/disaster_roaming/disaster_roaming_test.go` | ~150 | Declare/end roundtrip, check vs status, register membership, audit log. |
| `db/schemas/domains.go` | (slice) | DDL for `disaster_declarations`, `disaster_roaming_ues`, `disaster_roaming_log`. |
| `webservice/app/routes_disaster_roaming.go` | ~165 | REST surface for §A.4. |

Tests:
- `mmt_studio_core_tester/src/testcases/safety/tc_disaster_roaming.py` — 8 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS disaster_declarations (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  name            TEXT NOT NULL,
  reason          TEXT NOT NULL DEFAULT '',
  affected_areas  TEXT NOT NULL DEFAULT '',
  status          TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','ended')),
  declared_by     TEXT NOT NULL DEFAULT 'operator',
  declared_at     TEXT NOT NULL DEFAULT (datetime('now')),
  ended_at        TEXT
);

CREATE TABLE IF NOT EXISTS disaster_roaming_ues (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  declaration_id  INTEGER NOT NULL,
  imsi            TEXT NOT NULL,
  hplmn           TEXT NOT NULL,
  connected_at    TEXT NOT NULL DEFAULT (datetime('now')),
  services_used   TEXT,
  UNIQUE (declaration_id, imsi),
  FOREIGN KEY (declaration_id) REFERENCES disaster_declarations(id)
);

CREATE TABLE IF NOT EXISTS disaster_roaming_log (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  imsi        TEXT NOT NULL,
  hplmn       TEXT NOT NULL,
  action      TEXT NOT NULL CHECK (action IN ('admitted','denied','released')),
  reason      TEXT,
  created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
```

The `UNIQUE(declaration_id, imsi)` on the register makes
`addRoamingUE` idempotent (`INSERT OR IGNORE`); a UE that re-checks
during the same declaration won't double-enrol.

## B.4 Admission algorithm

```
CheckDisasterRoaming(imsi, hplmn):
    decl = GetActiveDeclaration()
    if decl != nil:
        addRoamingUE(decl.id, imsi, hplmn)
        logDR(imsi, hplmn, "admitted", "Disaster Condition active")
        return Allowed{decl.id}
    if checkNormalRoaming(hplmn):
        # Falls through to normal Roaming admission.
        return Allowed{reason: "normal roaming agreement"}
    logDR(imsi, hplmn, "denied", "no active Disaster Condition")
    return Denied
```

The `checkNormalRoaming` fallback ensures that disabling the disaster
flag doesn't accidentally tear down ordinary inbound roamers.

## B.5 Public API

```go
// Declaration lifecycle
func DeclareDisaster(name, reason, affectedAreas, declaredBy string) (int64, error)
func EndDisaster()
func EndDisasterByID(id int64) error
func GetDisasterStatus() map[string]interface{}
func IsDisasterActive() bool
func GetActiveDeclaration() (map[string]interface{}, error)
func GetAllDeclarations() ([]map[string]interface{}, error)

// Admission gate
type AdmissionResult struct {
    Allowed       bool
    Reason        string
    DeclarationID int64
}
func CheckDisasterRoaming(imsi, hplmn string) AdmissionResult
func CheckDisasterRoamingMap(imsi, hplmn string) map[string]interface{}

// Roaming-UE register
func GetDisasterRoamingUEs() ([]map[string]interface{}, error)
func ReleaseRoamingUE(imsi, hplmn string) error

// Audit log
func GetDRLog(limit int) ([]map[string]interface{}, error)

// Stats / GUI panel
func GetDRStats() map[string]interface{}
func List() ([]map[string]any, error)
func Status() map[string]any
```

## B.6 Test coverage

### B.6.1 Go unit tests

`safety/disaster_roaming/disaster_roaming_test.go` — declare/end
roundtrip, admission allowed-vs-denied gating, register UNIQUE
constraint, audit log content.

### B.6.2 Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-DR-001 `dr_declare_disaster` | declare → status reflects disaster_active=true. |
| TC-DR-002 `dr_validation` | empty name → 400; missing imsi/hplmn → 400. |
| TC-DR-003 `dr_check_roaming` | partner UE admitted while active; appears in roaming-ues register. |
| TC-DR-004 `dr_deny_when_inactive` | check denies when no disaster is active. |
| TC-DR-005 `dr_roaming_ues` | three checks → all three appear; end → register clears (JOIN gating). |
| TC-DR-006 `dr_declaration_history` | declare + end → history surfaces both states. |
| TC-DR-007 `dr_audit_log` | log records admit + deny across the lifecycle. |
| TC-DR-008 `dr_stats` | stats has total_declarations / admitted / denied / current_roaming_ues. |

All eight are wired into `tc_disaster_roaming.py::ALL_DISASTER_ROAMING_TCS`
and pass against the current core build.

## B.7 References

- **TS 22.261** §6.31 — Service requirements for Disaster Roaming.
- **TS 23.501**:
  - §5.40 — Disaster Roaming for PLMNs (architecture umbrella).
  - §5.40.2 — Disaster condition handling.
  - §5.40.3 — Restrictions of services and applications.
- **TS 38.331** — RAN-side disaster signalling (TODO; out of scope).

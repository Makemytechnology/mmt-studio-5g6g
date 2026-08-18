# FM — Fault Management / Alarms

The 5GC alarm subsystem (`oam/fm`) and its operator REST surface
(`/api/fm/*`). NFs raise alarms when something operator-visible
breaks; the manager correlates duplicates, persists every state
change to the `alarms` table, and exposes an in-memory active view
sorted by severity for the GUI panel.

# Part A — Functional

## A.1 Why FM?

Without an alarm pipeline, transient faults disappear into log files
and operators only learn about them after a customer complaint. FM
gives the operator one ranked queue of every active fault with the
context to decide *what to do next*: managed object, severity,
probable cause, raise count (so flapping faults don't look like one
incident), and an ack/clear life-cycle that distinguishes
"someone is on it" from "fixed."

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **NF → FM** | NF code → `fm.Default` | in-process | TS 28.532 §11.2a | `fm.Raise` / `fm.Clear` from each NF's hot path. |
| **FM → DB** | `fm.Default` → `alarms` table | SQLite | — | Every state change persisted; in-memory cache rebuilt on `Init`. |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/fm/*` (this file). |
| **TS 28.111** | — | — | TODO | Canonical operations vocabulary; not loaded locally. |

## A.3 Operator-visible behaviours

### A.3.1 Correlation rule (TS 28.532 §11.2a)

A repeated raise with the same `(ManagedObject, ProbableCause,
SpecificProblem)` does **not** generate a new alarm row — it bumps
`raise_count`, refreshes `additional_text` / `last_raised`, and is
silent unless the severity actually changed. Operator load matters:
when a fault flaps (multiple cleanup goroutines fire on the same
SCTP loss), one record with `raise_count=N` is the right view. See
`fault_manager.go:241-249` for the rationale.

### A.3.2 Severity vocabulary (X.733)

Sort order: `Critical < Major < Minor < Warning < Indeterminate <
Cleared`. The list endpoints sort ascending by severity, then descending
by `last_raised` so the top of the list is "the most urgent thing that
just happened."

`Cleared` is a **terminal** state — operators reach it via clear /
clear-all, never via raise. The route returns 400 if a caller sends
`perceived_severity=Cleared` to `/api/fm/raise`.

### A.3.3 Synthetic raise (drills, alarm correlation tests)

`POST /api/fm/raise` is the operator-initiated path. Used by drills
("simulate a Critical SCTP loss on gNB-12"), tester correlation tests,
and the GUI's "Raise alarm" form. Same correlation rule applies — a
synthetic raise of an existing alarm bumps `raise_count` rather than
creating a duplicate.

### A.3.4 Acknowledgement

`POST /api/fm/acknowledge` flips `ack_state` to `Acknowledged` and
stamps `ack_time` + `ack_user`. The package falls through to a
direct DB UPDATE if the alarm has already been Cleared (and thus
dropped from the in-memory active map) so historical alarms can still
be acknowledged from the GUI's history tab.

## A.4 Operator REST API (`/api/fm/*`)

### A.4.1 Read endpoints

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/fm/active-alarms` | `{alarms: [...], timestamp}` — every non-cleared alarm sorted by severity. |
| GET | `/api/fm/alarm-counts` | `{Critical, Major, Minor, Warning, Indeterminate, total}` severity histogram. |
| GET | `/api/fm/alarm-history?limit=N&include_active=bool` | `{alarms, timestamp}` — DB-backed, newest first; `include_active=false` filters to Cleared rows only. |

### A.4.2 Mutation endpoints

| Method | Path | Body | Purpose |
|--------|------|------|---------|
| POST | `/api/fm/raise`        | `{managed_object, alarm_type, perceived_severity, probable_cause, specific_problem, additional_text}` | Raise (or correlate). |
| POST | `/api/fm/acknowledge`  | `{alarm_id, user?}` | Ack a specific alarm. |
| POST | `/api/fm/clear`        | `{alarm_id, text?}` | Clear a specific alarm. |
| POST | `/api/fm/clear-all`    | `{managed_object?}` | Clear every active alarm (optionally scoped). |

All four mutators reply `{ok: true, …}` with the affected `alarm_id`
or `cleared` count. Validation errors (bad alarm_type, bad severity,
missing required field) return 400 with a human-readable error
message; ack/clear of a non-existent alarm returns 404.

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| TS 28.111 (Generic fault supervision Stage 2/3) PDF not loaded | — | Operations vocabulary tracked in prose only. |
| ITU-T X.733 management context + STATE-CHANGE service | — | Vocabulary tracked; service not implemented. |
| TS 28.532 §11.5 streaming alarm-stream surface | — | Polling-only; no wire-level streaming. |

---

# Part B — Design

## B.1 Process layout

```
                +-------------------------+
   NF code ---> | fm.Raise(RaiseInput)    |
                +-----------+-------------+
                            | correlationKey =
                            |  managedObject :: probableCause :: specificProblem
                            v
                +-------------------------+
                | Manager                 |
                |  active map[key]*Alarm  |
                |  seq    int64           |
                +-----------+-------------+
                            | persist(Alarm)
                            v
                +-------------------------+
                | alarms table (SQLite)   |
                |  PK alarm_id            |
                |  ON CONFLICT DO UPDATE  |
                +-----------+-------------+
                            ^
                            | Init() hydrates active rows on boot
                            |
   GUI / REST -+ ActiveAlarms() -> []Alarm sorted by severity, last_raised
              +- History(limit, includeActive)
              +- Counts() -> {Critical: N, Major: N, ..., total: N}
              +- Ack(id, user), ClearByID(id, text), ClearAll(scope)
                            ^
                            |  JSON / HTTP
                            |
                webservice/app/routes_fm.go
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `oam/fm/fault_manager.go` | 583 | `Manager`, `Alarm`, X.733 / 3GPP enumerations, persistence |
| `oam/fm/fault_manager_test.go` | 124 | correlation, Ack, Clear, ClearAll, History tests |
| `db/schemas/fm.go` | 26 | `alarms` DDL + indexes |
| `webservice/app/routes_fm.go` | ~205 | REST surface for §A.4 (this file). |

Tests:
- `mmt_studio_core_tester/src/testcases/oam/tc_fm.py` — 7 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS alarms (
  alarm_id           INTEGER PRIMARY KEY,
  managed_object     TEXT NOT NULL,
  alarm_type         TEXT NOT NULL,
  probable_cause     TEXT NOT NULL,
  perceived_severity TEXT NOT NULL,
  specific_problem   TEXT NOT NULL,
  additional_text    TEXT NOT NULL DEFAULT '',
  additional_info    TEXT NOT NULL DEFAULT '',
  event_time         TEXT NOT NULL DEFAULT (datetime('now')),
  last_raised        TEXT NOT NULL DEFAULT (datetime('now')),
  clear_time         TEXT,
  ack_state          TEXT NOT NULL DEFAULT 'Unacknowledged',
  ack_time           TEXT,
  ack_user           TEXT,
  raise_count        INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_alarm_severity ON alarms(perceived_severity);
CREATE INDEX IF NOT EXISTS idx_alarm_moi      ON alarms(managed_object);
```

The route layer enforces vocabulary at the surface (alarm_type +
severity allowlist); the DB layer uses raw `TEXT` so producers in the
package can add enumerations without a schema migration.

## B.4 Constants surface (X.733 + 3GPP)

```go
// Alarm types (TS 28.532 §11.2a / X.733)
AlarmTypeCommunications | AlarmTypeProcessing | AlarmTypeEnvironmental
AlarmTypeQoS | AlarmTypeEquipment

// Severities (X.733)
SeverityCritical | SeverityMajor | SeverityMinor | SeverityWarning
SeverityIndeterminate | SeverityCleared

// Probable causes (X.733 §8.1.3 subset)
CauseLossOfSignal | CauseCommunicationsSubsystemFailure
CauseDegradedSignal | CauseCallSetupFailure
CauseConnectionEstablishmentError | CauseSoftwareError
CauseSoftwareProgramError | CauseConfigurationError
CauseCorruptData | CauseOutOfMemory | CauseStorageCapacityProblem
CauseThresholdCrossed | CauseQoSResourceNotAvailable
CauseResponseTimeExcessive | CauseBandwidthReduced
CauseEquipmentMalfunction | CausePowerProblem
CauseApplicationSubsystemFailure
```

The route layer's allowlist (`alarmTypeOK`, `raiseSeverityOK`) is
keyed against these constants — adding a new alarm type or severity
to the package automatically extends the allowlist.

## B.5 Alarm row

```go
type Alarm struct {
    AlarmID           int64
    ManagedObject     string
    AlarmType         string
    ProbableCause     string
    PerceivedSeverity string
    SpecificProblem   string
    AdditionalText    string
    AdditionalInfo    string  // JSON-serialised
    EventTime         float64 // unix seconds, fractional
    LastRaised        float64
    ClearTime         *float64
    AckState          string  // "Unacknowledged" | "Acknowledged"
    AckTime           *float64
    AckUser           string
    RaiseCount        int     // bumped on every correlated re-raise
}
```

## B.6 Public API

```go
// Manager singleton + factory
var  Default *Manager
func NewManager() *Manager
func (*Manager) Init() error          // hydrate active map from alarms table

// Life-cycle
func (*Manager) Raise(in RaiseInput) (int64, error)
func (*Manager) Clear(mo, cause, problem, text string) (int64, error)
func (*Manager) ClearByID(id int64, text string) (bool, error)
func (*Manager) ClearAll(managedObject string) (int, error)
func (*Manager) Ack(id int64, user string) (bool, error)

// Read
func (*Manager) ActiveAlarms() []Alarm
func (*Manager) History(limit int, includeActive bool) ([]Alarm, error)
func (*Manager) Counts() map[string]int

// Routes
func (s *Server) registerFMRoutes()
```

## B.7 Producer call sites today

`grep -rn 'fm\\.\\(Default\\.\\)\\?Raise\\|fm\\.\\(Default\\.\\)\\?Clear' nf/ services/ webservice/` shows:

| Site | Effect |
|------|--------|
| `nf/amf/ngap/sctp_transitions.go:147,177` | `fm.Clear` on SCTP up; `fm.Raise` on SCTP down |
| `nf/amf/ngap/server.go:249`               | `fm.Raise` on NGAP-level fault |
| `nf/amf/ngap/ngsetup/ngsetup.go:144`      | `fm.Clear` on successful NG Setup |
| `webservice/cmd/sacore-web/main.go:78`    | `fm.Default.Raise` on startup hard error |
| `webservice/app/routes_fm.go`             | operator-initiated raise / ack / clear |

## B.8 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-FM-001 `fm_raise_and_list`        | raise → active list + counts reflect Major raise |
| TC-FM-002 `fm_ack_clear`             | ack flips ack_state; clear removes from active |
| TC-FM-003 `fm_correlation`           | three raises of same (mo, cause, problem) → one alarm_id, raise_count≥3 |
| TC-FM-004 `fm_clear_all_scoped`      | clear-all scoped to managed_object preserves other scopes |
| TC-FM-005 `fm_history`               | cleared alarms appear in alarm-history with severity=Cleared |
| TC-FM-006 `fm_validation`            | bad alarm_type / Cleared-on-raise / missing field → 400; unknown id → 404 |
| TC-FM-007 `fm_counts_consistency`    | severity histogram sum equals total |

All seven wired into `tc_fm.py::ALL_FM_TCS` and pass against the
current core build.

## B.9 References

- **TS 28.532** §11.2a — Generic fault supervision management service.
- **TS 28.532** §11.5  — Streaming data reporting service (conceptual model).
- **TS 28.111** — Generic fault supervision Stage 2/3 (TODO; not loaded locally).
- **TS 32.111-1** — Original 3GPP alarm management spec (TODO; not loaded locally).
- **ITU-T X.733** — Alarm reporting function (TODO; vocabulary basis).
- `docs/design/oam/pm.md` — counter producer (companion surface).
- `docs/design/oam/kpis.md` — KPI dashboard that reads `fm.Default.Counts()`.

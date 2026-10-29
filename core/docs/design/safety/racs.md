# RACS — Restricted Access Control

Operator-side restriction-level state, per-category Unified Access
Control (UAC) barring with priority-subscriber pass-through, and the
audit log of every admission decision.

# Part A — Functional

## A.1 Why RACS?

When the network is overloaded, under attack, or the operator is
asked by a regulator to throttle access (peacetime drills, civic
unrest, mass-event cells), the AMF needs a way to deny / rate-limit
new attaches without taking the whole 5GC offline. RACS provides
four restriction levels:

| Level | Behaviour |
|-------|-----------|
| `normal` | Default: per-category UAC barring (TS 24.501 §4.5) only. |
| `restricted` | Admit `cat=2` (emergency) and priority subscribers only. |
| `emergency_only` | Admit `cat=2` only; everyone else denied. |
| `full_lockdown` | Deny every new access (including emergency — peacetime drill). |

Plus a per-access-category barring factor (TS 24.501 §4.5.2) that
applies in `normal` mode: a Bernoulli draw against
`barring_factor ∈ [0.0, 1.0]` with 1.0 meaning never bar and 0.0
meaning always bar; barred UEs back off for `barring_time_s` seconds.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **AMF Initial Registration** | UE → AMF | NAS over NGAP | TS 23.501 §5.18 | AMF calls `CheckAccess(imsi, access_category)`. |
| **UAC barring** | gNB → UE | RRC SIB1 | TS 24.501 §4.5 | gNB-side broadcast; this surface is the operator-of-record for the values. |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/racs/*` (this file). |

## A.3 Operator-visible behaviours

### A.3.1 Singleton restriction state

`racs_config` is a singleton (`id=1` CHECK). `ActivateRestriction`
sets the level, reason, areas, and `activated_by`; `Deactivate` snaps
back to `normal` and clears the trail. The audit log preserves every
historical state change via `racs_access_log`.

### A.3.2 Priority subscribers

`isPriorityUser(imsi)` is a JOIN against `ue_slice_dnn` for `MIN(arp_priority) ≤ 5`.
Anyone with at least one ARP priority ≤ 5 in any of their slice/DNN
bindings is treated as priority — passes the `restricted` gate.

### A.3.3 Per-category barring

`SetBarringFactor(cat, factor, time_s)` is an UPSERT
(`ON CONFLICT(access_category) DO UPDATE`). Setting `factor=1.0`
(or calling DELETE on the route) disables barring for that category.
Setting `factor=0.0` always bars.

### A.3.4 Audit log

Every `CheckAccess` call writes one `racs_access_log` row carrying
`{imsi, access_category, restriction_level, decision ∈ {allowed, barred}, reason, ts}`.
The reason field carries either the policy text ("non-priority barred during
restricted mode") or the random draw ("barring draw=0.732 >= factor=0.50 for cat=7").

## A.4 Operator REST API (`/api/racs/*`)

Endpoints return flat objects (or arrays) keyed by domain noun.

### A.4.1 Status / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/racs/status` | full singleton config row |
| GET | `/api/racs/stats` | `{total, allowed, barred}` from the audit log |

### A.4.2 Restriction level (TS 23.501 §5.18)

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/racs/activate` | `{level, reason?, areas?, activated_by?}` (CHECK-validated) |
| POST | `/api/racs/deactivate` | snap back to `normal` |

### A.4.3 Per-category barring (TS 24.501 §4.5)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/racs/barring` | every barring row, sorted by access_category |
| POST   | `/api/racs/barring` | UPSERT `{access_category, barring_factor, barring_time_s}` |
| DELETE | `/api/racs/barring/{cat}` | reset to `factor=1.0, time=0, disabled` |

### A.4.4 Admission gate

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/racs/check-access` | `{imsi, access_category}` → `{allowed, reason, restriction_level}` |
| GET  | `/api/racs/access-log?limit=N` | newest-first audit entries |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| RAN-side broadcast of UAC values | TS 38.331 (SIB1) | Outside this surface; gNB must consume the operator-side values somehow. |
| ARP-priority threshold (currently `≤ 5`) | TS 23.501 §5.7.2.2 | Hard-coded; should be per-policy operator config. |
| SOR (Steering Of Roaming) integration | TS 23.122 | Not present; this surface only handles AC restrictions, not roaming-list updates. |

---

# Part B — Design

## B.1 Process layout

```
┌──────────── safety/racs ────────────┐
│                                     │
│  Singleton config                   │
│   racs_config (id=1)                │
│   ├── ActivateRestriction           │
│   ├── DeactivateRestriction         │
│   └── GetRestrictionStatus          │
│                                     │
│  Per-category barring               │
│   racs_barring_config               │
│   ├── SetBarringFactor (UPSERT)     │
│   ├── GetBarringConfigs             │
│   └── EvaluateBarring(cat) → draw   │
│                                     │
│  Admission gate                     │
│   CheckAccess(imsi, cat) →          │
│      switch on restriction_level    │
│      ├── normal: EvaluateBarring    │
│      ├── full_lockdown: deny        │
│      ├── emergency_only: cat==2 ok  │
│      └── restricted: cat==2 ||      │
│                       isPriorityUser │
│   isPriorityUser(imsi) — JOIN       │
│      ue_slice_dnn ARP ≤ 5            │
│                                     │
│  Audit log + stats                  │
│   racs_access_log                   │
│   ├── GetAccessLog(limit)           │
│   └── GetAccessStats                │
└─────────────────────────────────────┘
                ▲
                │ JSON / HTTP
                │
        webservice/app/routes_racs.go
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `safety/racs/racs.go` | ~195 | Restriction config + barring + admission gate + audit log + stats. |
| `db/schemas/domains.go` | (slice) | DDL for `racs_config`, `racs_barring_config`, `racs_access_log`. |
| `webservice/app/routes_racs.go` | ~165 | REST surface for §A.4. |

Tests:
- `mmt_studio_core_tester/src/testcases/safety/tc_racs.py` — 7 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS racs_config (
  id                INTEGER PRIMARY KEY CHECK (id = 1),
  restriction_level TEXT NOT NULL DEFAULT 'normal'
                    CHECK (restriction_level IN
                          ('normal','restricted','emergency_only','full_lockdown')),
  reason            TEXT NOT NULL DEFAULT '',
  affected_areas    TEXT NOT NULL DEFAULT '',
  activated_at      TEXT NOT NULL DEFAULT '',
  activated_by      TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS racs_barring_config (
  access_category   INTEGER PRIMARY KEY,
  barring_factor    REAL NOT NULL DEFAULT 1.0,
  barring_time_s    INTEGER NOT NULL DEFAULT 0,
  enabled           INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS racs_access_log (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  imsi              TEXT NOT NULL,
  access_category   INTEGER NOT NULL,
  restriction_level TEXT NOT NULL,
  decision          TEXT NOT NULL CHECK (decision IN ('allowed','barred')),
  reason            TEXT,
  created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
```

## B.4 Admission algorithm

```
CheckAccess(imsi, cat):
   level = racs_config.restriction_level
   switch level:
     case "normal":
        barred, why = EvaluateBarring(cat)        # Bernoulli draw
        log(barred ? "barred" : "allowed", why)
     case "full_lockdown":
        log("barred", "full lockdown")
     case "emergency_only":
        if cat == 2: log("allowed", "emergency")
        else:        log("barred", "non-emergency during emergency_only")
     case "restricted":
        if cat == 2:                 log("allowed", "emergency always")
        else if isPriorityUser(imsi): log("allowed", "priority subscriber")
        else:                         log("barred", "non-priority during restricted")
```

`EvaluateBarring(cat)`:

```
m = SELECT * FROM racs_barring_config WHERE access_category=cat
if m is None or not m.enabled: return false  (no barring)
factor = m.barring_factor
if factor <= 0: return true  (always bar)
if factor >= 1: return false (never bar)
draw = rand.Float64()
return draw >= factor   (Bernoulli; ~1-factor chance of barring)
```

## B.5 Public API

```go
// Restriction state
func GetRestrictionStatus() map[string]interface{}
func ActivateRestriction(level, reason, areas, activatedBy string) error
func DeactivateRestriction()

// Barring
func SetBarringFactor(accessCategory int, factor float64, timeS int)
func GetBarringConfigs() ([]map[string]interface{}, error)
func EvaluateBarring(accessCategory int) (bool, string)

// Admission gate
func CheckAccess(imsi string, accessCategory int) map[string]interface{}

// Audit log + stats
func GetAccessLog(limit int) ([]map[string]interface{}, error)
func GetAccessStats() map[string]interface{}

// GUI panel
func List() ([]map[string]any, error)
func Status() map[string]any
```

## B.6 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-RACS-001 `racs_activate_restriction` | activate `restricted` → status reflects level + reason. |
| TC-RACS-002 `racs_validation` | invalid level / cat>63 / factor>1 all → 400. |
| TC-RACS-003 `racs_full_lockdown` | every cat (incl. 2) denied under full_lockdown. |
| TC-RACS-004 `racs_emergency_only` | cat=2 admit, cat=0 deny. |
| TC-RACS-005 `racs_barring_config` | UPSERT factor=0.4 + time → list shows enabled=1, factor=0.4; DELETE resets. |
| TC-RACS-006 `racs_access_log` | check decisions appear in /access-log with `decision='barred'`. |
| TC-RACS-007 `racs_stats` | stats reports `total / allowed / barred` counters. |

All seven are wired into `tc_racs.py::ALL_RACS_TCS` and pass against
the current core build.

## B.7 References

- **TS 22.011** §4 — Service accessibility umbrella.
- **TS 22.261** §6.13 — Access control requirements (priority + barring).
- **TS 23.501** §5.18 — Service Continuity, including AC restrictions.
- **TS 24.501** §4.5 — Unified Access Control (operator barring categories).
- **TS 38.331** — TODO; RAN-side SIB1 broadcast of UAC values.
- **TS 23.122** — TODO; SOR (Steering of Roaming) integration.

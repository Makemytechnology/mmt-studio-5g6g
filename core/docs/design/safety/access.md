# access — Design Document

Operator-side access-restriction state for the AMF's pre-
authentication admission gates: Forbidden TAI / Forbidden PLMN
lookups (**TS 24.501 §5.3.13** and **§5.3.13A**) and Unified Access
Control barring of access categories (**TS 24.501 §4.5**).

## 1. Role / scope

`safety/access/` owns three pieces of operator state:

- The **Forbidden TAI** list — a per-`(plmn_id, tac)` deny table the
  AMF must refuse Initial Registration from per TS 24.501 §5.3.13.
- The **Forbidden PLMN** list — a per-`plmn_id` deny table per
  TS 24.501 §5.3.13A.
- **UAC barring** rules — per-access-category `(barring_factor,
  barring_time_s)` tuples driving the §4.5 random-barring gate.

The composite admission gate `CheckAccess(imsi, plmn, tac, category)`
runs the three checks in order (PLMN → TAI → UAC) and short-circuits
on the first failure with a §-cited cause. Every decision (allow or
deny) is appended to `access_decision_log` so a regulator can ask
"why was IMSI X refused on 2026-04-30 at 14:32?" and get a single
SQL row back.

This is the **operator surface**. The UE-side state machine
(TS 24.501 §4.5.2 / §4.5.2A — determination of access identities and
access category) is out of scope: callers assert whichever
`access_category` the UE claimed and we only check operator barring
against it.

A Disaster Condition (TS 22.261 §6.31.2.1) can override these gates
for inbound roamers; that path lives in `safety/disaster_roaming/`.

## 2. Architecture

```
   AMF Initial-Registration handler                       Operator panel
            │                                                  │
            │ CheckAccess(imsi, plmn, tac, category)           │
            │                                                  │
            ▼                                                  ▼
   ┌─────────────────────────────────────────────────────────────────┐
   │                        safety/access                              │
   │                                                                   │
   │   ┌──────────────────────┐  PLMN gate                             │
   │   │ access_forbidden_plmn│  (TS 24.501 §5.3.13A)                  │
   │   └──────────┬───────────┘                                        │
   │              │ Forbidden? → deny + cause "TS 24.501 §5.3.13A"     │
   │              ▼                                                    │
   │   ┌──────────────────────┐  TAI gate                              │
   │   │ access_forbidden_tai │  (TS 24.501 §5.3.13)                   │
   │   └──────────┬───────────┘                                        │
   │              │ Forbidden? → deny + cause "TS 24.501 §5.3.13"      │
   │              ▼                                                    │
   │   ┌──────────────────────┐  UAC gate                              │
   │   │ access_uac_barring   │  (TS 24.501 §4.5)                      │
   │   └──────────┬───────────┘                                        │
   │              │ EvaluateUACBarring: rand.Float64() < barring_factor│
   │              │ Barred? → deny + cause "TS 24.501 §4.5"            │
   │              ▼                                                    │
   │   ┌──────────────────────┐                                        │
   │   │ access_decision_log  │  Allow or Deny — both audited          │
   │   └──────────────────────┘                                        │
   └─────────────────────────────────────────────────────────────────┘
```

### 2.1 Tables

| Table | Holds |
|-------|-------|
| `access_forbidden_tai` | `(plmn_id, tac, reason, added_by, added_at)` |
| `access_forbidden_plmn` | `(plmn_id, reason, added_by, added_at)` |
| `access_uac_barring` | `(access_category, barring_factor, barring_time_s, enabled, updated_at)` |
| `access_decision_log` | `(imsi, plmn_id, tac, decision, reason, ts)` |

## 3. File map

| File | Role |
|------|------|
| `safety/access/access.go` | All public API + composite gate + audit log + stats |
| `safety/access/access_test.go` | Forbidden-list / UAC-barring / composite-gate tests |

## 4. Wire / API surface

This package does not speak any spec wire format. It is the operator
state the AMF reads at NAS-handler time. The §-cites it ships back
in `CheckResult.CauseRef` are the audit-trail justification — the
NAS Reject the AMF builds afterwards is the AMF's responsibility
(TS 24.501 §5.3.20 governs reject-cause semantics; see TODO).

## 5. Headline procedures

### 5.1 Composite admission gate

```
CheckAccess(imsi, plmnID, tac, category):
    if plmnID != "" && IsForbiddenPLMN(plmnID):
        return Deny, "TS 24.501 §5.3.13A"
    if plmnID != "" && tac != "" && IsForbiddenTAI(plmnID, tac):
        return Deny, "TS 24.501 §5.3.13"
    if category >= 0:
        barred, backoff = EvaluateUACBarring(category)
        if barred:
            return Deny, "TS 24.501 §4.5"
    return Allow, ""
    -- always logDecision(imsi, plmnID, tac, result)
```

Empty `tac` skips the TAI check (the UE may not yet have a TAC at
the very start of registration). Negative `category` skips the UAC
gate (callers without category info).

### 5.2 UAC random-barring (`EvaluateUACBarring`, `access.go:208`)

The §4.5 spec leaves the actual draw to the UE; the AMF only
configures the factor + time. The package re-runs the same draw on
the network side so an operator can predict the blocked-rate at a
given `barring_factor`:

```
factor = 0.0  → never barred
factor = 1.0  → always barred (returns full backoff_time)
factor ∈ (0,1):
    draw = rand.Float64()      // [0, 1)
    barred iff draw < factor
```

`EvaluateUACBarring` returns `(barred bool, backoffSec int)`. When
`enabled=0`, the rule is skipped (returns false, 0).

### 5.3 Audit log

`logDecision` (`access.go:314`) appends one row per `CheckAccess`
call regardless of outcome. The `reason` column embeds the §-cite
in parentheses so the row alone is regulator-readable.

## 6. Key types / public API

```go
const (
    DecisionAllow = "allow"
    DecisionDeny  = "deny"
)

type CheckResult struct {
    Allowed  bool   `json:"allowed"`
    Reason   string `json:"reason"`    // human-readable
    CauseRef string `json:"cause_ref"` // §clause justifying the deny
}

// Forbidden TAI list (TS 24.501 §5.3.13)
func AddForbiddenTAI(plmnID, tac, reason, addedBy string) error
func RemoveForbiddenTAI(plmnID, tac string) error
func ListForbiddenTAIs() ([]map[string]interface{}, error)
func IsForbiddenTAI(plmnID, tac string) bool

// Forbidden PLMN list (TS 24.501 §5.3.13A)
func AddForbiddenPLMN(plmnID, reason, addedBy string) error
func RemoveForbiddenPLMN(plmnID string) error
func ListForbiddenPLMNs() ([]map[string]interface{}, error)
func IsForbiddenPLMN(plmnID string) bool

// UAC barring (TS 24.501 §4.5)
func SetUACBarring(category int, barringFactor float64, barringTime int, enabled bool) error
func RemoveUACBarring(category int) error
func ListUACBarring() ([]map[string]interface{}, error)
func EvaluateUACBarring(category int) (barred bool, backoffSec int)

// Composite gate
func CheckAccess(imsi, plmnID, tac string, category int) CheckResult  // access.go:245

// Audit
func GetDecisionLog(limit int) ([]map[string]interface{}, error)

// Stats / GUI
func GetStats() map[string]interface{}
func List() ([]map[string]any, error)
func Status() map[string]any
```

`access_category` is bounded to `[0, 63]` — TS 24.501 §4.5 (max
6-bit access category field).

## 7. Stubs / TODOs from grep

| Site | TODO |
|------|------|
| `access.go:39` | `TODO(spec: TS 22.011)` — Service accessibility / Access Class Barring legacy semantics; not loaded locally; UAC §4.5 supersedes this in 5GS. |
| `access.go:42` | `TODO(spec: TS 23.122)` — UE-side PLMN selection responses to the rejects we emit. Out of scope. |

The header also flags two **deferred** spec clauses (no in-line
TODO marker) for transparency:

- TS 24.501 §4.5.2 Determination of access identities / access
  category for normal access — UE-side state machine.
- TS 24.501 §4.5.2A Same for Disaster-Roaming-related access.

## 8. References

Only specs cited in source:

- **TS 24.501** — Non-Access-Stratum (NAS) protocol for 5GS
  - §4.5 Unified access control (overall framework)
  - §5.3.13 Lists of 5GS forbidden tracking areas
  - §5.3.13A Forbidden PLMN lists
  - §5.3.20 (referenced; reject-cause semantics on the AMF side)
- **TS 22.261** — Service requirements for the 5G system
  - §6.31.2.1 Disaster Condition definition (cross-link to `safety/disaster_roaming/`)
- **TS 22.011** (TODO; not loaded locally)
- **TS 23.122** (TODO; not loaded locally)

Cross-link: `safety/disaster_roaming/` widens the gate during a
declared Disaster Condition. The AMF runs both probes in series
(Disaster admit overrides forbidden-list deny when the UE qualifies
as a Disaster Inbound Roamer per TS 23.501 §5.40.1).

---
*Last refreshed against commit `13a181d`.*

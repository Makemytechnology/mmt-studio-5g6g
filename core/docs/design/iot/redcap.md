# redcap — Design Document

NR Reduced Capability (RedCap / eRedCap) UE classification helpers
for the MMT 5G Core. Pure decision-only module: no per-UE state,
no SBI surface — AMF / SMF call into it when they need to gate
behaviour on RAT type.

## 1. Role / Scope

The 5G Core needs to know, per UE:

- Is this UE RedCap or eRedCap (sub-types of NR per
  **TS 23.501 §5.41**)?
- Is Dual Connectivity allowed for this UE?
- Is this RAT type allowed as the **primary** RAT of a PDU
  session (subscription-gated)?

Per-UE RedCap support is recorded against `ue.*` rows by the AMF;
this package is a constants + decision-helper module only — no
SQL schema, no goroutines, no goroutines, no `db/engine` calls.

## 2. Architecture

```
                  ┌────────────────────────────────────┐
                  │ AMF / SMF (per-UE policy gates)    │
                  └──────────────┬─────────────────────┘
                                 │
                                 ▼
                  ┌────────────────────────────────────┐
                  │ iot/redcap                         │
                  │  RATTypeNR / RATTypeNRRedCap /     │
                  │            RATTypeNReRedCap        │
                  │                                    │
                  │  IsRedCap(ratType) bool            │
                  │  IsDualConnectivityAllowed(...)    │
                  │  IsAllowedAsPrimaryRAT(rat, sub)   │
                  │  Status() — GUI panel surface      │
                  └────────────────────────────────────┘
```

No data plane, no DB, no timers — this is 80 LOC of pure functions
plus three string constants.

## 3. File / Package Map

| File | LOC | Role |
|------|-----|------|
| `iot/redcap/redcap.go` | 87 | RAT-type constants + 3 decision helpers + `Status()` |
| `iot/redcap/redcap_test.go` | 78 | unit tests for `IsRedCap` / DC gate / primary-RAT gate |

## 4. Public API

```go
// RAT-type identifiers — TS 23.501 §5.41.
const (
    RATTypeNRRedCap  = "NR_REDCAP"   // §5.41 first para
    RATTypeNReRedCap = "NR_EREDCAP"  // §5.41 second para
    RATTypeNR        = "NR"          // comparison case
)

// IsRedCap returns true for NR_REDCAP or NR_EREDCAP per §5.41.
func IsRedCap(ratType string) bool

// IsDualConnectivityAllowed returns false for RedCap / eRedCap UEs.
// Per §5.41: "the Dual Connectivity function does not apply to the
// NR RedCap UE" in this Release.
func IsDualConnectivityAllowed(ratType string) bool

// IsAllowedAsPrimaryRAT defers to the subscription flag for
// RedCap / eRedCap UEs (subscription-derived restriction in §5.41);
// returns true otherwise.
func IsAllowedAsPrimaryRAT(ratType string, subscriptionAllowsAsPrimary bool) bool

// Status returns the supported RAT-type list for the GUI panel.
func Status() map[string]any
```

## 5. Lifecycle

There is no lifecycle — every helper is pure. The only decision flow
is at PDU-session-establishment time:

```
SMF receives session establishment with RAT-type = "NR_REDCAP"
  ↓
redcap.IsRedCap("NR_REDCAP")                       → true
redcap.IsDualConnectivityAllowed("NR_REDCAP")      → false  (§5.41)
redcap.IsAllowedAsPrimaryRAT("NR_REDCAP", subFlag) → subFlag
  ↓
SMF gates DC + primary-RAT behaviour accordingly
```

## 6. Key Types

None — the package exports no struct types. Just three string
constants and three boolean helpers.

## 7. Stubs / TODOs

| Site | TS | Comment |
|------|-----|---------|
| `redcap.go:26` | TS 38.306 | Add per-band RedCap capability bit decoding once the NR-side UE-Capability spec lands in `specs/3gpp/` |
| `redcap.go:57` | TS 23.501 §5.41 | Re-check the DC gate when a future Release loosens DC for eRedCap; current decision is intentionally conservative (deny both) |

## 8. References

Spec citations grounded in `iot/redcap/redcap.go`:

- **TS 23.501 §5.41** — NR RedCap and NR eRedCap UEs differentiation.
  - First para → `RATTypeNRRedCap` constant
  - Second para → `RATTypeNReRedCap` constant
  - "The Dual Connectivity function does not apply to the NR RedCap
    UE" → `IsDualConnectivityAllowed`
  - "NR RedCap not allowed as primary RAT" subscription text →
    `IsAllowedAsPrimaryRAT`
- **TS 38.306** — NR UE radio access capabilities (TODO at
  `redcap.go:26`).

---
*Last refreshed against commit `13a181d`.*

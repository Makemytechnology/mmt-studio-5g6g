# PCF — Design Document

3GPP TS 23.503 / TS 29.512-aligned Policy Control Function for the
MMT 5G Core. Resolves PCC rules and Session-AMBR for each PDU session
and exposes an Npcf_SMPolicyControl-shaped consumer surface to the
SMF (in-process). Also owns URSP rules (TS 23.503 §6.6) for UE Route
Selection Policy delivery via NAS.

## 1. Role in 5GC

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **N7** | SMF | Npcf_SMPolicyControl (HTTP/2 JSON) | TS 29.512 — in-process today |
| **N5** | AF | Npcf_PolicyAuthorization | TS 29.514 — partial (`pcf.go` AF helpers) |
| **N15** | AMF | Npcf_AMPolicyControl, UE Policy Control | TS 29.507 / §UE-policy — not implemented |
| (URSP delivery) | UE via AMF DL NAS | URSP IE encoding | TS 23.503 §6.6, TS 24.501 §5.4.4 |

The Go PCF runs in-process — the SMF calls `pcf.GetPolicyForSession` /
`smpolicy.Default.Create` directly. Shapes returned (`SmPolicyDecision`,
`SmPolicyContextData`, `RuleReport`) are modelled on the OpenAPI types
in TS 29.512 so the SBI lift is mechanical. PCC rule lifecycle on the
SBI uses only `ACTIVE` / `INACTIVE` per §5.6.3.8; the in-memory
manager carries extra states (`PccRulePending`, `PccRuleInactiveGated`,
`PccRuleRemoved`) that map to those two on the wire (`pcf.go:WireRuleStatus`).

## 2. Architecture

```
                     ┌──────────────┐                  ┌─────────────┐
                     │     SMF      │                  │     AF      │
                     └──────┬───────┘                  └──────┬──────┘
                            │ N7 (in-proc)                    │ N5 (partial)
                            ▼                                 ▼
┌───────────────────────  PCF process  ─────────────────────────────┐
│                                                                    │
│  pcf/  (root)                                                      │
│   pcf.go (~19 KLOC) — PCC rule data model, in-memory               │
│      PccRuleManager (per-(IMSI, DNN) lifecycle), CreatePolicy,    │
│      AF service-info path (TS 29.514), N7 NotifySMFPolicyUpdate   │
│      (TS 29.512 §4.2.3 helper).                                    │
│                                                                    │
│  pcf/smpolicy/ — Npcf_SMPolicyControl service surface             │
│   smpolicy.go — Create / Update / Delete / PushNotify             │
│   transitions.go — per-association FSM transition table           │
│   fsm/ — state, event, generic FSM engine                         │
│   webservice/ — HTTP routes (when SBI lift lands)                 │
│                                                                    │
│  pcf/ursp/ — UE Route Selection Policy                             │
│   ursp.go — TrafficDescriptor / RouteDescriptor / Rule + DB CRUD,  │
│             evaluation engine, URSP IE encoder for NAS delivery    │
└────────────────────────────────────────────────────────────────────┘
                            │ DB writes (services, bindings,
                            │ pcc_rules, charging_profiles, ursp_*)
                            ▼
                       db/engine
```

## 3. Package / file map

| Package | LOC | Role |
|---------|-----|------|
| `nf/pcf` (root) | 19k bytes (~620 lines) | `PCCRule`, `PCCRuleSet`, `CreatePolicy(imsi, dnn, sst)`, `GetPolicyForSession`. `PccRuleManager` singleton (`GlobalPccRuleManager`) tracking per-(IMSI, DNN) rule lifecycle (`AddRule`, `ActivateRules`, `DeactivateRules`, `RemoveRules`). N7 `NotifySMFPolicyUpdate` push. AF `ProcessAFServiceInfo` path (TS 29.514). |
| `nf/pcf/smpolicy` | ~1100 | Npcf_SMPolicyControl service surface. `SmPolicyContextData` / `SmPolicyDecision` / `SmPolicyUpdateContextData` / `RuleReport` mirror TS 29.512 §5.6.2.x types. `Default` Manager (`Create / Update / Delete / PushNotify`). |
| `nf/pcf/smpolicy/fsm` | ~150 | Per-association FSM: `StateNone → StateCreatePending → StateActive ⇆ StateUpdatePending`, `StateActive → StateTerminating → StateTerminated`. |
| `nf/pcf/smpolicy/webservice` | small | Web routes for `/api/pcf/smpolicy*`. |
| `nf/pcf/ursp` | ~430 | URSP rule CRUD (`Rule`, `TrafficDescriptor`, `RouteDescriptor`), `EvaluateURSP(imsi, TrafficInfo) → RouteDescriptor`, IE encoding for NAS delivery (TS 24.501 §5.4.4). |

## 4. SBI / FSM interactions

### 4.1 N7 service ops (TS 29.512 §4.2.x)

| Op | § | Direction | Method | Notes |
|----|---|-----------|--------|-------|
| Create | §4.2.2 | SMF → PCF | `smpolicy.Default.Create(SmPolicyContextData)` → `SmPolicyDecision` | Allocates `SmPolicyCtxRef` (in-process internal id; spec: PCF-assigned URI per §4.2.2.2 step 2). |
| UpdateNotify | §4.2.3 | PCF → SMF | `smpolicy.Default.PushNotify(key, SmPolicyDecision)` | PCF-initiated push: AF activated dynamic rule, revalidation expired, subscription policy changed. |
| Update | §4.2.4 | SMF → PCF | `Default.Update(key, SmPolicyContextDataUpdate) → SmPolicyDecision` | Carries Policy Control Request Triggers + RuleReport list. |
| Delete | §4.2.5 | SMF → PCF | `Default.Delete(key) → DeleteStatus` | Termination. |

`RuleStatus` enum on the wire (TS 29.512 §5.6.3.8): exactly two values
`{ ACTIVE, INACTIVE }`. The internal `PccRuleState` carries five for
bookkeeping; `WireRuleStatus()` collapses to the two-valued set
(`pcf.go:194-200`).

### 4.2 SM Policy Association FSM

```
                       ┌─────────┐
                       │  None   │
                       └────┬────┘
              §4.2.2 Create │
                            ▼
                       ┌──────────────────┐
                       │ CreatePending    │
                       └────────┬─────────┘
                                │ Create Response delivered
                                ▼
              ┌────────────────────────────────────────┐
              │            Active                       │◄─────┐
              └─────────┬──────────┬────────────────────┘      │
              §4.2.3    │          │ §4.2.4                    │
              UpdateNotify          Update                     │
                        ▼          ▼                            │
                  ┌───────────────────┐                          │
                  │  UpdatePending    │ ─── ack / RuleReport ────┘
                  └───────────────────┘
              §4.2.5 Delete │
                            ▼
                       ┌──────────────────┐
                       │  Terminating     │
                       └────────┬─────────┘
                                │ Delete Response
                                ▼
                          ┌─────────────┐
                          │ Terminated  │  (terminal; slot free)
                          └─────────────┘
```

Spec: TS 29.512 §4.2 doesn't formally name a PCF-side FSM but the
states above are implicit across §4.2.2 / §4.2.4 / §4.2.5 (see
`smpolicy/fsm/state.go:14-21`). Revalidation Timer (§4.2.2.4 /
§4.2.3.4) is armed in `Active`; expiry triggers a PCF-initiated
UpdateNotify with a fresh decision.

### 4.3 N5 / Npcf_PolicyAuthorization (TS 29.514)

`pcf.go:ProcessAFServiceInfo` walks SDP media descriptions, derives
SDF filters (`pcf.go` §4.2.2.2 helper), activates the matching named
rules via `PccRuleManager.ActivateRules`, and pushes the new decision
via `NotifySMFPolicyUpdate`. SDP-to-SDF-filter algorithm follows
TS 29.513 (referenced in `pcf.go:25`).

## 5. Lifecycle — SM Policy Association establish + AF push

```
SMF                           PCF                          DB
 │                             │                            │
 │── pcf.GetPolicyForSession( imsi, dnn, sst ) ──────▶│      │
 │                             │ CreatePolicy(...)         │
 │                             │  bindings = crud.BindingsList(imsi, dnn)
 │                             │  for each binding:
 │                             │    svc = crud.ServicesGet(name)
 │                             │    rules = append(rules, svc → PCCRule)
 │                             │  if no bindings:
 │                             │    rules = [ default_data 5QI=9 NonGBR ]
 │                             │  defaultQFI = bindings[i].IsDefault → idx+1
 │                             │  chargingMethod = derived from charging_profile
 │◄── PCCRuleSet { rules, defaultQFI, chargingMethod } ──────│
 │
 │── smpolicy.Default.Create(SmPolicyContextData{
 │     SUPI, PDUSessionID, DNN, SST, SD, PDUSessionType }) ─▶│
 │                             │ FSM: None → CreatePending → Active
 │                             │ Allocate SmPolicyCtxRef
 │                             │ Build SmPolicyDecision (PCC rules + AMBR
 │                             │   + DefaultQFI + Default5QI + ChargingMethod
 │                             │   + RevalidationTime)
 │◄── SmPolicyDecision ──────────────────────────────────────│
 │   (SMF stashes SmPolicyCtxRef on session.Session)
 │
 │   ... data flowing ...
 │                             │
 │   AF arrives via N5 / Npcf_PolicyAuthorization (§4.2.2):
 │      pcf.ProcessAFServiceInfo(IMS service info):
 │        - derive SDF filters (TS 29.513)
 │        - GlobalPccRuleManager.ActivateRules(IMSI, DNN, [services...])
 │
 │                             │ NotifySMFPolicyUpdate(IMSI, pduSessID,
 │                             │   serviceNames, sdpMedia)
 │                             │ → push SmPolicyDecision to SMF (§4.2.3)
 │◄── SmPolicyDecision (UpdateNotify) ──────────────────────│
 │   FSM: Active → UpdatePending → Active (after RuleReport ack)
 │
 │   ... session torn down ...
 │── smpolicy.Default.Delete(key) ─────────────────────────▶│
 │                             │ FSM: Active → Terminating → Terminated
 │◄── DeleteStatus ─────────────────────────────────────────│
```

## 6. Lifecycle — URSP delivery (TS 23.503 §6.6 + TS 24.501 §5.4.4)

```
UE registers → AMF reaches PCF for UE Policy (TODO — N15 not wired)
                         │
                         ▼
              ursp.GetRulesForUE(imsi)
                  → SELECT * FROM ursp_rules / *_descriptors
                  → []Rule sorted by Precedence
                         │
                         ▼
              ursp.Encode(rules) → URSP IE bytes (TS 24.501 §9.11.4.7)
                         │
                         ▼
              AMF DL NAS Transport / Configuration Update Command
                  carrying URSP IE
                         │
                         ▼
              UE applies the rules locally to PDU session selection.
```

The DL delivery path (Configuration Update Command / Manage UE Policy
Command) is **not yet wired** — `ursp` exposes the rule store + IE
encoder; the AMF GMM handlers don't consume them yet.

## 7. Key types / public API

```go
// pcf/pcf.go
type PCCRule struct {
    ServiceName     string
    FiveQI          int
    ResourceType    string  // "GBR" | "NonGBR"
    ArpPriority     int
    GBRULKbps, GBRDLKbps, MBRULKbps, MBRDLKbps int
    ChargingProfile string
    IsDefault       bool
}
type PCCRuleSet struct {
    Rules          []PCCRule
    DefaultQFI     uint8
    ChargingMethod string
}
func CreatePolicy(imsi, dnn string, sst uint8) PCCRuleSet
func GetPolicyForSession(imsi, dnn string, sst uint8) PCCRuleSet

type PccRuleState string
const (
    PccRuleActive   PccRuleState = "ACTIVE"   // §5.6.3.8
    PccRuleInactive PccRuleState = "INACTIVE"
    // Internal-only; collapse via WireRuleStatus()
    PccRulePending       PccRuleState = "PENDING"
    PccRuleInactiveGated PccRuleState = "INACTIVE_GATED"
    PccRuleRemoved       PccRuleState = "REMOVED"
)
func (s PccRuleState) WireRuleStatus() PccRuleState

type PccRuleManager struct{ /* sync.Mutex, map[ruleKey][]*PccRuleEntry */ }
var GlobalPccRuleManager = &PccRuleManager{ ... }

func (m *PccRuleManager) AddRule(imsi, dnn, svcName string, status PccRuleState) *PccRuleEntry
func (m *PccRuleManager) ActivateRules(imsi, dnn string, svcNames []string) []*PccRuleEntry
func (m *PccRuleManager) DeactivateRules(imsi, dnn string)
func (m *PccRuleManager) DeactivateRulesByName(imsi, dnn string, svcNames []string)
func (m *PccRuleManager) RemoveRules(imsi, dnn string)
func (m *PccRuleManager) GetRules(imsi, dnn string) []*PccRuleEntry
func (m *PccRuleManager) GetActiveServiceNames(imsi, dnn string) []string

// N7 helpers
func NotifySMFPolicyUpdate(imsi string, pduSessionID int, services []string, sdpMedia []map[string]any) bool

// pcf/smpolicy/smpolicy.go
type SmPolicyContextData struct{ SUPI string; PDUSessionID uint8; DNN string; SST uint8; SD string; PDUSessionType uint8 }
type SmPolicyDecision struct{
    PccRules []pcf.PCCRule
    DefaultQFI uint8
    SessionAMBRUL, SessionAMBRDL int   // kbps
    Default5QI int
    ChargingMethod string
    RevalidationTime time.Time
    SmPolicyCtxRef string
}
type SmPolicyContextDataUpdate struct{ Triggers []string; RuleReports []RuleReport }
type RuleReport struct{ PccRuleIDs []string; RuleStatus pcf.PccRuleState; FailureCode string }
type DeleteStatus int

type Manager struct { ... }
var Default = NewManager()
func (m *Manager) Create(ctx SmPolicyContextData) (SmPolicyDecision, error)        // §4.2.2
func (m *Manager) Update(key string, upd SmPolicyContextDataUpdate) (SmPolicyDecision, error) // §4.2.4
func (m *Manager) PushNotify(key string, dec SmPolicyDecision)                     // §4.2.3
func (m *Manager) Delete(key string) (DeleteStatus, error)                         // §4.2.5

// pcf/ursp/ursp.go
type TrafficDescriptor struct{ ID, RuleID int64; MatchType, MatchValue string }
type RouteDescriptor   struct{ ID, RuleID int64; Precedence int; SST *int; SD *string; DNN, PDUSessionType, AccessType string }
type Rule              struct{ ID int64; IMSI *string; Precedence int; ... TrafficDescriptors / RouteDescriptors }
type TrafficInfo       struct{ AppID, DNN, FQDN, DstIP, DstPort string; ... }

func List() ([]Rule, error)
func Get(id int64) (*Rule, error)
func Add(r Rule) (int64, error)
func Delete(id int64) error
func EvaluateURSP(imsi string, info TrafficInfo) *RouteDescriptor
func Encode(rules []Rule) []byte                // TS 24.501 §9.11.4.7
```

## 8. Operator REST surface

Until 2026-05, the URSP/PCF panel APIs were 6-line + 0-line stubs in
`routes_nsaas.go` returning empty objects — the real machinery was
unreachable from the panel and tester. The two route blocks below
(`webservice/app/routes_ursp.go`, `routes_pcf.go`) expose the package
APIs as `{ok: true, ...}` JSON.

### 8.1 URSP — `/api/ursp/*` (TS 23.503 §6.6)

| Method | Path | Calls | Notes |
|--------|------|-------|-------|
| `GET` | `/api/ursp/status` | `ursp.Status()` | Returns rule count. |
| `GET` | `/api/ursp/rules?imsi=` | `ursp.List(imsi)` | Empty `imsi` → all; non-empty → per-UE + global merged. |
| `GET` | `/api/ursp/rules/{id}` | `ursp.Get(id)` | 404 when nil. |
| `POST` | `/api/ursp/rules` | `ursp.CreateRule(CreateInput)` | Validates precedence ∈ [0, 255] (§6.6) and TD/RSD enums against the schema CHECK constraint, atomic insert across `ursp_rules` + `ursp_traffic_descriptors` + `ursp_route_descriptors`. |
| `PATCH` | `/api/ursp/rules/{id}` | `ursp.UpdateRule(id, patch)` | Sparse update; allow-list `precedence | description | enabled | imsi`. Empty IMSI → NULL (global). |
| `DELETE` | `/api/ursp/rules/{id}` | `ursp.DeleteRule(id)` | FK CASCADE removes TDs + RSDs. |
| `POST` | `/api/ursp/rules/{id}/push` | `ursp.BuildURSPIEForRule(id)` | Returns encoded URSP IE (TS 24.526 + §9.11.4.16) for one rule. |
| `GET` | `/api/ursp/ie/{imsi}` | `ursp.BuildURSPIEForUE(imsi)` | Returns the encoded URSP IE the AMF would ship to that UE. |
| `POST` | `/api/ursp/evaluate` | `ursp.EvaluateURSP(imsi, traffic)` | First-match-by-precedence (§6.6); returns `matched_rule` + `route_descriptor`. |

Validators in `nf/pcf/ursp/api.go` mirror the schema CHECK constraints
on `ursp_traffic_descriptors.match_type` / `ursp_route_descriptors.{pdu_session_type,access_type}`
so bad input surfaces as a clean 400 instead of a SQLite CHECK 500.

A latent deadlock in `ursp.List()` was uncovered while wiring the
operator API: `engine.Open()` pins `MaxOpenConns=1` (SQLite single-
writer); the per-row `enrichRule()` recursion called `db.Query()`
inside the outer rows iterator, which would block forever once the
table actually had a row. Fixed in `nf/pcf/ursp/ursp.go::List` —
drain the result set first, then enrich each row.

### 8.2 PCF — `/api/pcf/*` (TS 23.503 §6.3 + TS 29.512 §4.2)

| Method | Path | Calls | Notes |
|--------|------|-------|-------|
| `GET` | `/api/pcf/stats` | `pcf.Stats()` + `smpolicy.Stats()` | Total PCC rules, by_status, V2X association count, SM-policy registry counts. |
| `GET` | `/api/pcf/pcc-rules?imsi=&dnn=` | `pcf.ListPccRules(imsi, dnn)` | In-memory rules (both `GlobalPccRuleManager` and `DefaultPccRuleManager`); each entry carries `wire_status` per §5.6.3.8 (ACTIVE \| INACTIVE) for SBI compatibility. |
| `GET` | `/api/pcf/policy-preview?imsi=&dnn=&sst=` | `pcf.PreviewPolicy(...)` | Non-invasive — runs `CreatePolicy` without opening an SM-Policy association. |
| `GET` | `/api/pcf/sm-policy` | `smpolicy.ListAssociations()` | Returns `AssociationView[]` with FSM state, ctxRef, AMBR, default 5QI/QFI. |
| `GET` | `/api/pcf/sm-policy/{imsi}/{pdu_id}` | `smpolicy.GetAssociationView` | 404 when not found. |
| `POST` | `/api/pcf/sm-policy` | `smpolicy.Create` | Body mirrors §5.6.2.2 SmPolicyContextData. Validates `pdu_session_id ∈ [1, 15]` (TS 23.501 §5.7.1.4); accepts SUPI directly or `imsi` (auto-prefixed with `imsi-`). |
| `PATCH` | `/api/pcf/sm-policy/{imsi}/{pdu_id}` | `smpolicy.Update` | Body mirrors §5.6.2.3 SmPolicyUpdateContextData (`triggers` + `rule_reports`). 404 when association unknown. |
| `DELETE` | `/api/pcf/sm-policy/{imsi}/{pdu_id}` | `smpolicy.Delete` | Idempotent per §4.2.5 (returns `terminated:true` even when the key was already gone). |
| `GET` | `/api/pcf/v2x/{imsi}` | `pcf.GetPC5QoSForGnb` | Operator readout of V2X policy association (TS 23.287). |

The panel/tester now drives the full Create → Update → Delete
lifecycle without going through the SMF, which lets us test PCF state
transitions in isolation.

### 8.3 Tester coverage map

Operator-API only — no UE/gNB. The legacy UE-integration TCs in
`src/testcases/vas/tc_ursp.py` cover the NAS delivery side.

| TC ID | File | Spec | Asserts |
|-------|------|------|---------|
| TC-URSP-OAM-001 | `oam/tc_ursp_oam.py` | TS 23.503 §6.6 | `/status` envelope + `count` field. |
| TC-URSP-OAM-002 | ″ | TS 23.503 §6.6.2 | Create → Get → Patch → Get (re-read) → Delete. |
| TC-URSP-OAM-003 | ″ | TS 23.503 §6.6.2.1 | precedence > 255, bad TD `match_type`, bad RSD `pdu_session_type`, empty TD list all return 400. |
| TC-URSP-OAM-004 | ″ | TS 23.503 §6.6 | `/evaluate` matches by `app_id`, RSD's DNN reflects rule. |
| TC-URSP-OAM-005 | ″ | TS 23.503 §6.6 | Two FQDN rules, lower precedence wins (numerically lowest first). |
| TC-URSP-OAM-006 | ″ | TS 24.501 §5.4.4 | `/rules/{id}/push` returns IE with `iei = 0x76` and `rule_count = 1`. |
| TC-URSP-OAM-007 | ″ | TS 24.501 §5.4.4 | `/ie/{imsi}` merges UE-specific + global rules in the encoded IE. |
| TC-URSP-OAM-008 | ″ | TS 23.503 §6.6 | Unknown `id` → 404 on `GET`/`PATCH`/`DELETE`. |
| TC-PCF-OAM-001 | `oam/tc_pcf_oam.py` | TS 23.503 §6.3 | `/stats` envelope + sm-policy + by_status keys. |
| TC-PCF-OAM-002 | ″ | TS 23.503 §6.3 | `/policy-preview` returns rule set with valid `charging_method ∈ {online, offline}`. |
| TC-PCF-OAM-003 | ″ | TS 29.512 §4.2 | Create → Get → list → Update (`RE_TIMEOUT` trigger) → Delete → idempotent Delete. |
| TC-PCF-OAM-004 | ″ | TS 29.512 §5.6.2.2 | `pdu_session_id = 99` and missing `dnn`/`supi` all 400. |
| TC-PCF-OAM-005 | ″ | TS 29.512 §4.2.4 | GET/PATCH on unknown association → 404; DELETE is idempotent → 200. |
| TC-PCF-OAM-006 | ″ | TS 29.512 §5.6.3.8 | Every listed PCC rule carries wire-valid `wire_status ∈ {ACTIVE, INACTIVE}`. |

## 9. What's not implemented

Grepped TODOs in `nf/pcf/`:

| Area | Status | Source |
|------|--------|--------|
| HTTP/2 JSON SBI front-end (full Npcf_SMPolicyControl) | scaffold; in-process only | `smpolicy/smpolicy.go:7-23` (header) |
| N15 / Npcf_AMPolicyControl + UE Policy Control | not implemented | — |
| Find IMS PDU session + lookup event-gated bindings | TODO | `pcf/pcf.go:493` |
| URSP delivery via DL NAS (UE Policy Container / Configuration Update Command) | not wired | — |
| AMBR / Default5QI extraction beyond defaults | partial | `smpolicy.SmPolicyDecision` flattens session-rule map |
| Revalidation timer arm/expire | not armed | `smpolicy/fsm/state.go:48-50` |
| RuleReport / FailureCode (§5.6.3.9) handling beyond tracking | partial | `smpolicy/smpolicy.go:RuleReport` |
| Charging Function (CHF) integration | not wired | `pcf.PCCRule.ChargingMethod` is informational |
| ATSSS rule descriptors (§6.3 with `atsssRule`) | not modelled | — |
| Dynamic PCC rule from AF Media Component (full §4.2.2.2) | partial — `ProcessAFServiceInfo` derives SDF filters and calls `ActivateRules` | `pcf.go` |

## 10. References

Spec citations grepped from `nf/pcf/`:

- **TS 23.501** — slice/QoS context (referenced indirectly via PCC rules)
- **TS 23.503** v19.7.0 §6.2.1 PCF role, §6.3 / §6.3.1 PCC rule
  definition, §6.6 URSP
- **TS 24.501** §5.4.4 (URSP delivery), §9.11.4.7 (URSP IE encoding)
- **TS 29.512** v19.6.0 §4.2.2 / §4.2.3 / §4.2.4 / §4.2.5 service ops,
  §5.6.2.2 SmPolicyContextData, §5.6.2.3 SmPolicyUpdateContextData,
  §5.6.2.4 SmPolicyDecision, §5.6.2.6 PccRule, §5.6.2.15 RuleReport,
  §5.6.3.8 RuleStatus enum, §5.6.3.9 FailureCode
- **TS 29.513** PCC signalling flows + QoS parameter mapping (used for
  SDP→SDF filter derivation)
- **TS 29.514** v19.6.0 §4.2.2 Create, §4.2.2.2 initial provisioning of
  service information (Npcf_PolicyAuthorization)
- **TS 23.502** §4.3.3.2 referenced by `DeactivateRulesByName`

---
*Last refreshed: operator REST surface added (URSP + PCF), `List()`
deadlock fix, tester OAM coverage map (14 TCs).*

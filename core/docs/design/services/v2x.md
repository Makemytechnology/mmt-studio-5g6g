# V2X — Design Document

Vehicle-to-Everything service over 5GS — operator-side state for
the **TS 23.287** V2X reference architecture and the
**TS 23.287 §5.1.2** policy / parameter provisioning procedure that
delivers V2X policy to each UE.

---

## Part A — Functional view

### A.1 What V2X is, in plain terms

A car talks to another car ("V2V"), to a roadside unit / traffic
light ("V2I"), to a pedestrian's phone ("V2P"), and to a back-end
service over the cellular network ("V2N"). Together these four
things are **V2X**. Many of those messages — collision warnings,
platooning telemetry, sensor sharing — have to travel **directly
between vehicles** with sub-10 ms latency, even in places where the
network is congested or out of reach.

5G V2X gives those direct messages a dedicated radio path: the
**PC5 sidelink**. The 5G core's job is the slower-but-essential
part: deciding which UEs are allowed to use V2X, which carriers
they may use, and which **PQI** (PC5 QoS Identifier) corresponds to
each safety-class of traffic. This package is that operator-side
control surface.

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| **Regulatory / safety mandate** | Many jurisdictions tie connected-vehicle / VRU-protection programmes to spectrum licences; V2X is how the operator delivers it. |
| **Differentiated transport sale** | Carriers monetise PC5 alongside Uu — a single SIM, two radio paths, two SLAs. |
| **No latency floor from the WAN** | PC5 sidelink keeps cooperative-driving messages local to the road segment; no backhaul round-trip. |
| **Standardised QoS for safety classes** | TS 23.287 §5.4.4 fixes a PQI table the whole industry honours; vehicles from any vendor speak it. |
| **Re-uses existing Uu** | When PC5 isn't available the Uu carries the same V2X traffic — operator keeps service continuity for a graceful degrade. |
| **Pedestrian / VRU coverage** | Phones and bicycles can be V2X UEs (`ue_type='pedestrian'`); the same authorisation gate applies. |

### A.3 Customer use cases (TS 22.186 §5)

| Use case | Profile |
|----------|---------|
| **Vehicle platooning** | Trucks following a leader UE at sub-1 m gap; tightest latency / reliability budget. |
| **Advanced driving** | Cooperative collision avoidance, cooperative lane change, intersection movement assist. |
| **Extended sensors** | Vehicles share raw / fused sensor data (camera, LiDAR, radar) with peers and RSUs. |
| **Remote driving** | Tele-operated vehicle; UL-heavy (≥10 Mb/s sustained) — flagged as deferred QoS budget. |
| **VRU safety** | Pedestrian / cyclist UE broadcasts position / intent to nearby vehicles. |

### A.4 Actors and roles

```
   Vehicle UE 1                  Vehicle UE 2                  RSU / VRU UE
        │ PC5 (TS 24.588)             │ PC5                       │
        ◀────── platooning,           │                           │
                sensor sharing,       │                           │
                collision-avoidance ──┼──── V2X messages ─────────┘
                                       │
   Operator panel                      │   ┌─── 5GC ────┐
        │                              │   │            │
        │ provision PQI table          │   │   AMF      │
        │ authorise UE                 │   │   PCF ◀── V2X policy
        │ manage authorised PLMNs      │   │   UDM      │     (TS 24.587 §5;
        │ provision V2X policy         │   │   …        │      UE Policy Container,
        ▼                              │   └─────┬──────┘      TS 24.501 §D.6.1)
   ┌──────────────────────────────────────────────────────────────────────┐
   │                services/v2x  (this package)                           │
   │                                                                       │
   │   v2x_service_types     ── PQI catalogue (§5.4.4 Table 5.4.4-1)       │
   │   v2x_config            ── operator knobs (NR PC5 freqs, enables)     │
   │   ue.v2x_*              ── per-UE V2X subscription (§5.5)             │
   │   v2x_authorized_plmns  ── per-UE allowed PLMN list (§5.1.2)          │
   │   v2x_policy_log        ── audit of every PCF→UE policy push (§5.1.2) │
   └──────────────────────────────────────────────────────────────────────┘
                                       ▲
                                       │ /api/v2x/* (this design doc)
                                       │
                                ┌──────┴───────┐
                                │  Operator    │
                                │  REST surface│
                                └──────────────┘
```

| Actor | Role | Touches this package via |
|-------|------|--------------------------|
| **Vehicle / VRU UE** | Authorised consumer of PC5 / Uu V2X traffic. | `LoadSubscription`, `IsAuthorized`, `ProvisionPolicy` |
| **PCF (TS 23.287 §4.2)** | Builds and pushes the V2X policy container to the UE. | Reads `BuildV2XPolicyParams` (or `ProvisionPolicy`) and wraps the result in a UE Policy Container per TS 24.587 §5. |
| **AMF (transparent)** | Carries the UE Policy Container down to the UE. | Wire deferred. |
| **Operator (OAM panel)** | Curates PQI table, authorises subscribers, manages per-UE allowed PLMNs, drives policy provisioning. | `/api/v2x/*` |
| **NR PC5 RAN** | Honours the PQI → QoS mapping when scheduling sidelink. | Reads `v2x_service_types`. |

### A.5 Operator workflow

```
   Provisioning (catalogue)
   ────────────────────────
   1.  GET    /api/v2x/service-types         confirm seeded §5.4.4 PQIs
   2.  POST   /api/v2x/service-types         add operator-custom PQI rows
   3.  POST   /api/v2x/config                set NR PC5 frequencies, enables

   Per-UE (authorisation)
   ──────────────────────
   4.  POST   /api/v2x/authorize             ue_type ∈ {vehicle,pedestrian},
                                              pc5_ambr_kbps  (§5.2 + §5.5)
   5.  POST   /api/v2x/authorized-plmns      per-UE allowed PLMN list (§5.1.2)

   Policy push
   ───────────
   6.  POST   /api/v2x/policy/provision      builds the §5.1.2 V2X policy
                                              container body, writes audit row,
                                              returns the container body. The
                                              actual UE Policy Container envelope
                                              (TS 24.587 §5 / TS 24.501 §D.6.1)
                                              is the deferred wire.

   Audit
   ─────
   7.  GET    /api/v2x/policy/log            recent §5.1.2 deliveries

   Lifecycle
   ─────────
   8.  POST   /api/v2x/deauthorize           clears subscription (idempotent)
   9.  DELETE /api/v2x/service-types/{pqi}   drop a custom PQI row
```

### A.6 The PQI catalogue — what the seed contains (TS 23.287 §5.4.4)

Seeded canonical rows mirror **TS 23.287 Table 5.4.4-1**:

| PQI | Service | Resource type | Priority | PDB (ms) | PER |
|-----|---------|---------------|----------|----------|-----|
| 21 | platooning_higher | GBR | 3 | 20 | 1e-4 |
| 22 | sensor_sharing_higher | GBR | 4 | 50 | 1e-2 |
| 23 | info_sharing_driving | GBR | 3 | 100 | 1e-4 |
| 55 | coop_lane_change_higher | NonGBR | 3 | 10 | 1e-4 |
| 56 | platooning_informative | NonGBR | 6 | 20 | 1e-1 |
| 57 | coop_lane_change_lower | NonGBR | 5 | 25 | 1e-1 |
| 58 | sensor_sharing_lower | NonGBR | 4 | 100 | 1e-2 |
| 59 | platooning_reporting | NonGBR | 6 | 500 | 1e-1 |
| 90 | collision_avoidance | DelCritGBR | 3 | 10 | 1e-4 |
| 91 | emergency_trajectory | DelCritGBR | 2 | 3 | 1e-5 |

`resource_type` is enforced by a SQL CHECK to the §5.4.4 enum
{`GBR`, `NonGBR`, `DelCritGBR`}; the route also rejects bad enum
values with 400 before the SQL round-trip.

### A.7 The V2X policy container — what `ProvisionPolicy` returns

`POST /api/v2x/policy/provision` returns the canonical container
body called for in **TS 23.287 §5.1.2**:

```json
{
  "auth_policy": {
    "authorized_plmns": ["00101"],
    "ue_type": "vehicle",
    "pc5_rats": ["nr"],
    "pc5_ambr_kbps": 50000
  },
  "pc5_qos_params": [ /* every row in v2x_service_types */ ],
  "v2x_frequencies": [38500, 38600, 5905]
}
```

The wire-side wrap into a **UE Policy Container** (TS 24.587 §5,
embedded in a NAS DL Generic UE Policy Command per TS 24.501
§D.6.1) is deferred — see TODO.

### A.8 What is NOT in scope here

| Thing | Where it lives |
|-------|----------------|
| **PC5 RRC / link establishment** | TS 24.588 §5/§6 — out of this package; PC5 link signalling is a UE↔UE concern, not a 5GC operator surface. |
| **PC5 unicast security** | TS 33.536 (referenced from TS 24.588 §6); deferred. |
| **V2X message family routing (V2X-PSID gating)** | TS 23.287 §6.x; application-layer above this. |
| **UE-to-Network relay for V2X over Uu** | TS 23.287 §5.6 (hand-off into TS 23.304 ProSe relay); deferred. |
| **Roaming V2X authorisation (HPLMN vs VPLMN)** | TS 23.287 §5.3; deferred. |
| **UE Policy Container wire envelope** | TS 24.587 §5 / TS 24.501 §D.6.1; the body is built, the wire wrap is deferred. |
| **Remote-driving QoS budgets (≥10 Mb/s UL)** | TS 22.186 §5.5; not a §5.4.4 PQI — operator must define a custom row. |

---

## Part B — Design

### B.1 Architecture

```
   Operator REST  ──────────────────────────────────────────────┐
   /api/v2x/*                                                    │
        │                                                        │
        ▼                                                        │
   ┌──────────────────────────────────────────────────────────┐ │
   │        services/v2x/v2x.go  (Go package, this design)     │ │
   │                                                           │ │
   │   ServiceType CRUD          → v2x_service_types          │ │
   │   Config get/set/list       → v2x_config                 │ │
   │   AuthorizeUE / Deauthorize → ue.v2x_* (upsert on auth)  │ │
   │   AuthorizedPLMN add/del    → v2x_authorized_plmns       │ │
   │   ProvisionPolicy           → v2x_policy_log + body      │ │
   │   ListPolicyLog             → v2x_policy_log read        │ │
   └──────────┬───────────────────────────────────────────────┘ │
              │                                                  │
              ▼                                                  │
   ┌──────────────────────────────────────────────────────────┐ │
   │             SQLite (db/schemas/v2x.go)                    │ │
   │                                                           │ │
   │   v2x_service_types(id, service_name UNIQUE, pqi,         │ │
   │     resource_type CHECK ∈ {GBR,NonGBR,DelCritGBR},        │ │
   │     priority_level, packet_delay_ms, packet_error_rate,   │ │
   │     max_data_burst, avg_window_ms, fiveqi_uu, description)│ │
   │   v2x_config(key PK, value)                               │ │
   │   v2x_authorized_plmns(id, imsi, plmn_id,                 │ │
   │     UNIQUE(imsi, plmn_id), idx imsi)                      │ │
   │   v2x_policy_log(id, imsi, ue_type, pc5_ambr_kbps,        │ │
   │     plmn_count, qos_count, freq_count, created_at, idx    │ │
   │     imsi)                                                 │ │
   │   ue.v2x_authorized / v2x_ue_type CHECK ∈                 │ │
   │     {vehicle,pedestrian} / v2x_pc5_ambr_kbps              │ │
   └──────────────────────────────────────────────────────────┘ │
                                                                 │
                                                       wire-format│
                                                       UE Policy  │
                                                       Container  │
                                                       (TS 24.587 │
                                                       §5; deferred)
```

### B.2 Field → spec map

| Field / row | Spec § |
|-------------|--------|
| `v2x_service_types.pqi` / `resource_type` / `priority_level` / `packet_delay_ms` / `packet_error_rate` / `max_data_burst` / `avg_window_ms` | TS 23.287 §5.4 PC5 QoS framework, §5.4.4 Table 5.4.4-1 |
| `v2x_service_types.fiveqi_uu` | TS 23.287 §5.4 (Uu-side 5QI for the same flow when PC5 is unavailable) |
| `ue.v2x_authorized` | TS 23.287 §5.2 V2X authorization |
| `ue.v2x_ue_type` (CHECK ∈ {vehicle,pedestrian}) | TS 23.287 §5.5 V2X subscription |
| `ue.v2x_pc5_ambr_kbps` | TS 23.287 §5.5 (PC5 AMBR cap) |
| `v2x_authorized_plmns` | TS 23.287 §5.1.2 ("authorized PLMNs" container element) |
| `v2x_config["nr_pc5_frequencies"]` | TS 23.287 §5.1.2 (V2X frequencies element) |
| `v2x_policy_log` | TS 23.287 §5.1.2 (audit per PCF→UE delivery) |
| Policy container body shape | TS 23.287 §5.1.2 + TS 24.587 §5 / TS 24.501 §D.6.1 (envelope deferred) |

### B.3 File map

| File | Role |
|------|------|
| `services/v2x/v2x.go` | Types, all public API, SQL access |
| `services/v2x/v2x_test.go` | CRUD + subscription + provisioning tests |
| `db/schemas/v2x.go` | DDL: PQI table, config, per-UE PLMN list, policy log, ue ALTERs, seeded canonical PQI rows |
| `webservice/app/routes_v2x.go` | REST surface `/api/v2x/*` |
| `webservice/app/domain_routes.go` | Wires `registerV2XRoutes()` into `RegisterDomainRoutes` |

### B.4 REST surface

| Method | Path | Backing | Notes |
|--------|------|---------|-------|
| `GET` | `/api/v2x/status` | `v2x.Status()` | Catalog summary + items. |
| `GET` | `/api/v2x/service-types` | `ListServiceTypes` | PQI catalogue (§5.4.4 Table 5.4.4-1). |
| `GET` | `/api/v2x/service-types/{pqi}` | `GetServiceType(pqi)` | 404 if missing. |
| `POST` | `/api/v2x/service-types` | `CreateServiceType` | Operator-custom row. 400 on bad `resource_type` enum. |
| `PUT` | `/api/v2x/service-types/{pqi}` | `UpdateServiceType` | Update by PQI. |
| `DELETE` | `/api/v2x/service-types/{pqi}` | `DeleteServiceType` | Drop a row. |
| `GET` | `/api/v2x/config` | `ListConfig` | Operator knobs. |
| `GET` | `/api/v2x/config/{key}` | `GetConfig(key)` | Single key. |
| `POST` | `/api/v2x/config` | `SetConfig(key, value)` | Upsert. |
| `GET` | `/api/v2x/frequencies` | `LoadFrequencies` | Parsed `nr_pc5_frequencies`. |
| `GET` | `/api/v2x/subscription/{imsi}` | `LoadSubscription` | TS 23.287 §5.5 read. Returns `{v2x_authorized:false}` if unknown. |
| `POST` | `/api/v2x/authorize` | `AuthorizeUE` | TS 23.287 §5.2/§5.5; upserts the `ue` row. 400 on bad `ue_type`. |
| `POST` | `/api/v2x/deauthorize` | `DeauthorizeUE` | Idempotent. |
| `GET` | `/api/v2x/pc5-qos/{imsi}` | `GetPC5QoSParams` | Gated on §5.4 — unauthorised UE → 403. |
| `GET` | `/api/v2x/authorized-plmns/{imsi}` | `ListAuthorizedPLMNs` | TS 23.287 §5.1.2. |
| `POST` | `/api/v2x/authorized-plmns` | `AddAuthorizedPLMN` | Idempotent `INSERT OR IGNORE` on `(imsi, plmn_id)`. |
| `DELETE` | `/api/v2x/authorized-plmns?imsi=&plmn_id=` | `DeleteAuthorizedPLMN` | Single row. |
| `POST` | `/api/v2x/policy/provision` | `ProvisionPolicy` | TS 23.287 §5.1.2. 403 when UE is not authorised. |
| `GET` | `/api/v2x/policy/log[?imsi=&limit=]` | `ListPolicyLog` | Audit read; recent first. |

### B.5 Authorisation gate (TS 23.287 §5.1.2)

`ProvisionPolicy(imsi)` runs:

```
1. sub = LoadSubscription(imsi)
   - SELECT v2x_authorized, v2x_ue_type, v2x_pc5_ambr_kbps FROM ue WHERE imsi=?
   - if no row OR auth==0 → return nil
2. policy = BuildV2XPolicyParams(imsi, sub)
   - auth_policy.authorized_plmns ← v2x_authorized_plmns(imsi)
   - auth_policy.ue_type           ← sub.v2x_ue_type
   - auth_policy.pc5_rats          ← ["nr"]
   - auth_policy.pc5_ambr_kbps     ← sub.v2x_pc5_ambr_kbps
   - pc5_qos_params                ← ListServiceTypes()
   - v2x_frequencies               ← LoadFrequencies()
3. INSERT v2x_policy_log row (one per call)
4. return policy
```

The route maps `nil` from `ProvisionPolicy` to **403** to give the
caller a spec-shaped error rather than a soft success.

### B.6 Key types / public API

```go
type ServiceType struct {
    ID              int64
    ServiceName     string
    PQI             int
    ResourceType    string  // GBR | NonGBR | DelCritGBR
    PriorityLevel   int
    PacketDelayMS   int
    PacketErrorRate string
    MaxDataBurst    *int
    AvgWindowMS     *int
    FiveQIUu        *int
    Description     *string
}

type Config struct { Key, Value string }

type V2XSubscription struct {
    V2XAuthorized  bool
    V2XUEType      string  // vehicle | pedestrian
    V2XPC5AMBRKbps int
}

type PolicyLogEntry struct {
    ID          int64
    IMSI        string
    UEType      string
    PC5AMBRKbps int
    PLMNCount   int
    QoSCount    int
    FreqCount   int
    CreatedAt   string
}

// Catalog (TS 23.287 §5.4.4)
func ListServiceTypes() ([]ServiceType, error)
func GetServiceType(pqi int) (*ServiceType, error)
func CreateServiceType(s ServiceType) (int64, error)
func UpdateServiceType(pqi int, s ServiceType) error
func DeleteServiceType(pqi int) error

// Operator config
func ListConfig() ([]Config, error)
func GetConfig(key string) (string, error)
func SetConfig(key, value string) error
func LoadFrequencies() []int

// Subscription (TS 23.287 §5.2/§5.5)
func LoadSubscription(imsi string) *V2XSubscription
func IsAuthorized(imsi string) bool
func GetPC5QoSParams(imsi string) []ServiceType
func AuthorizeUE(imsi, ueType string, pc5AMBRKbps int) error
func DeauthorizeUE(imsi string) error

// Authorised PLMN list (TS 23.287 §5.1.2)
func ListAuthorizedPLMNs(imsi string) []string
func AddAuthorizedPLMN(imsi, plmnID string) error
func DeleteAuthorizedPLMN(imsi, plmnID string) error

// Policy provisioning (TS 23.287 §5.1.2)
func BuildV2XPolicyParams(imsi string, sub *V2XSubscription) map[string]interface{}
func ProvisionPolicy(imsi string) map[string]interface{}
func ListPolicyLog(imsi string, limit int) ([]PolicyLogEntry, error)
```

### B.7 Tester coverage

| Test | Spec § | What it asserts |
|------|--------|-----------------|
| `TC-V2X-001 v2x_service_types_list` | §5.4.4 | Canonical PQIs (subset of {21,22,23,55,90}) seeded; `resource_type` ∈ §5.4.4 enum. |
| `TC-V2X-002 v2x_service_type_crud` | §5.4.4 | Operator-custom PQI create/get/put/delete; 400 on bad `resource_type`; 404 on get-after-delete. |
| `TC-V2X-003 v2x_authorize_ue` | §5.2/§5.5 | Subscription flips; bad `ue_type` → 400. |
| `TC-V2X-004 v2x_pc5_qos_query` | §5.4 | Unauthorised UE → 403; authorised → ≥5 rows. |
| `TC-V2X-005 v2x_authorized_plmns` | §5.1.2 | Add/list/delete + idempotent re-add. |
| `TC-V2X-006 v2x_policy_provision` | §5.1.2 | Container has the three canonical elements; unauthorised UE → 403. |
| `TC-V2X-007 v2x_policy_log_audit` | §5.1.2 | Each provision appends one audit row; top entry matches IMSI. |

All seven currently PASS (`b4bb747` core × `205488f` tester).

### B.8 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| Package header (`services/v2x/v2x.go:27-37`) | TS 23.287 §5.3 (roaming auth), §5.6 (UE-to-Network relay), §6.x (V2X-PSID gating); TS 24.588 §6 (PC5 link security); TS 22.186 §5.5 (remote-driving QoS budgets). |
| Wire envelope | TS 24.587 §5 / TS 24.501 §D.6.1 — `ProvisionPolicy` builds the body; the UE Policy Container wrap is the deferred wire. |

### B.9 References

Only specs cited in source:

- **TS 22.186** — V2X service requirements
- **TS 23.287** — Architectural enhancements for 5G V2X services
  - §4.2 Reference architecture
  - §4.4 V2X policy / parameter provisioning
  - §5.1.2 V2X policy / parameter provisioning procedure
  - §5.2 V2X authorization
  - §5.4 PC5 QoS framework
  - §5.4.4 Standardised PQI values (Table 5.4.4-1)
  - §5.5 V2X subscription data
- **TS 24.587 §5** — UE Policy Container envelope (deferred wire)
- **TS 24.588 §5** — PC5 signalling protocol procedures (deferred)

Cross-link: `services/prose/` is the sibling D2D-over-PC5 surface
(TS 23.304); the two share the PC5 radio but their authorisation,
policy delivery and use-cases are distinct.

---
*Last refreshed against commit `b4bb747`.*

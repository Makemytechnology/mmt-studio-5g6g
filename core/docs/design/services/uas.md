# UAS — Design Document

Uncrewed Aerial Systems (UAV / drone) control plane over 5GS —
operator-side state for the **TS 23.256** UAS architecture and the
USS/UTM-driven flight authorisation, Net-RID, position tracking,
and C2 pairing procedures it defines.

---

## Part A — Functional view

### A.1 What the UAS surface is, in plain terms

A drone (UAV) wants to fly. Before it does, the operator's network
has to know **which** drone (registry), whether the **flight plan**
is admissible (USS/UTM check against no-fly zones and envelope),
**who** is controlling it (UAV-C pairing), and **how** to keep
tabs on it in flight (Net-Remote-ID broadcast, periodic position
reports, anomaly detection).

5G is the **command-and-control** transport for the drone, the
**telemetry** uplink, and the **identification** carrier (Net-RID).
This package is the operator-side state machine that backs all four.

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| **Regulatory mandate (FAA Part 107, EASA Open / Specific, CAAC)** | UTM-side participation — flight authorisation + Remote ID — is required for commercial drone ops; operators sell connectivity bundled with that. |
| **C2 over licensed spectrum** | Sub-50 ms PDB on 5QI=3; reliable, jitter-bounded, no Wi-Fi unlicensed contention. |
| **BVLOS service** | Beyond-Visual-Line-of-Sight flights need network-side compliance + Net-RID; the operator is the gatekeeper. |
| **Differentiated SLA per drone class** | Inspection / surveying / delivery / public-safety each get their own 5QI / DRB profile and per-UAV envelope. |
| **Audit & compliance trail** | Every authorisation, every position update, every Remote-ID query is recorded — useful for incident reconstruction and regulator response. |
| **Re-uses subscriber infrastructure** | UAV is just another UE; UDM / AMF / SMF carry it the same way they carry phones, with a UAS NF on top. |

### A.3 Customer use cases (TS 22.125 §5)

| Use case | Profile |
|----------|---------|
| **Industrial inspection** | Power-line, pipeline, wind-farm flyovers; scheduled BVLOS routes; high-rate video uplink. |
| **Surveying / mapping** | Photogrammetry sorties; payload bandwidth + accurate Net-RID. |
| **Delivery / logistics** | Goods drones with strict per-area gating (urban NFZs, school zones). |
| **Public-safety / SAR** | Police, fire, search-and-rescue; emergency authorisation flow. |
| **Agriculture** | Precision spraying / monitoring; long-endurance, large NFZ exclusions. |
| **Smart-cities / surveillance** | Traffic monitoring, crowd telemetry; positioned and tracked at all times. |
| **Connected-airspace / UTM trials** | Operator partners with a USS to run multi-vendor airspace tests. |

### A.4 Actors and roles

```
   UAV (UE)                 UAV-C (Controller)              USS / UTM
        │  C2 over Uu (5QI=3)        │                          │
        │     TS 23.256 §5.5         │                          │
        ▼                            │                          │
   ┌──────────────────────────────────────────────────────────────────┐
   │                        services/uas  (this package)                │
   │                                                                    │
   │   uas_registry      ── §5.2.1 UAV identity                         │
   │   uas_flight_auth   ── §5.2.4 USS/UTM authorisation outcome        │
   │   uas_no_fly_zones  ── §5.2.4 forbidden volumes                    │
   │   uas_positions     ── §5.2.5 / §5.2.6 location reports + history  │
   │   uas_c2_sessions   ── §5.2.3 pairing + §5.5 C2 session lifecycle  │
   └──────────────────────────────────────────────────────────────────┘
                                                              ▲
                                                              │ /api/uas/*
                                                              │
                                                       ┌──────┴───────┐
                                                       │   Operator   │
                                                       │  REST surface│
                                                       └──────────────┘
```

| Actor | Spec role | Touches this package via |
|-------|-----------|--------------------------|
| **UAV** | The drone; carries a UE that registers as a UAV in the §5.2.1 registry. | `RegisterUAV`, `UpdatePosition`, `RemoteIDBroadcast` |
| **UAV-C (controller)** | The remote pilot's ground station. Pairs with the UAV under §5.2.3. | `EstablishC2` |
| **USS / UTM** | UAS Service Supplier / UAS Traffic Management — the §5.2.4 authority. | Local stand-in: `AuthorizeFlight` checks NFZ + envelope. Real wire is UAE-USS / UAE-UTM (TS 22.125 §5.4) — deferred. |
| **Operator (OAM)** | Curates the UAV registry, manages NFZs, audits flights. | `/api/uas/*` |
| **5GC (AMF/SMF/UDM)** | Carries the C2 PDU session at 5QI=3 (TS 23.501 Table 5.7.4-1 referenced for §5.5). | C2 session is recorded here; the actual PDU establish runs in `nf/smf/`. |

### A.5 Operator workflow

```
   1.  POST   /api/uas/registry                §5.2.1 UAV identity
                                               (auto-allocates uav_id when blank)

   2.  POST   /api/uas/no-fly-zones            §5.2.4 — operator-curated
                                               forbidden volumes
                                               (lat/lon box ± alt cap)

   3.  POST   /api/uas/authorize-flight        §5.2.4 — USS/UTM gate:
                                                 - UAV must exist + not deregistered/grounded
                                                 - waypoint NFZ check
                                                 - per-UAV envelope (max_speed, max_alt)
                                                 → returns flight_id + restrictions
                                                 → flips uas_registry.status = 'active'

   4.  POST   /api/uas/c2/establish            §5.2.3 pairing + §5.5 C2 session
                                               default qos_5qi = 3 (V2X / DCC,
                                               TS 23.501 Table 5.7.4-1)
                                               §5.5 one-active rule → HTTP 409 on conflict

   5.  POST   /api/uas/position                §5.2.5/§5.2.6 telemetry
                                               (periodic; appends to uas_positions)

   6.  GET    /api/uas/remote-id/{uav_id}      §5.2.5 Net-RID payload
                                               (ASTM F3411-22a §4 fields:
                                                ua_type, id_type, uas_id,
                                                operator_id, location, vector,
                                                authentication, timestamp)

   7.  GET    /api/uas/anomaly/{uav_id}        §5.2.6 envelope check vs.
                                               (max_speed, max_alt, NFZ)

   8.  POST   /api/uas/revoke-authorization    operator yanks a flight
   9.  DELETE /api/uas/c2/{c2_id}              terminate C2 session
   10. POST   /api/uas/c2/{c2_id}/failover     §5.6 stub — marks failed,
                                               returns manual-failover hint
```

### A.6 The §5.2.4 gate — what `AuthorizeFlight` enforces

```
   AuthorizeFlight(uav_id, flight_plan)
   ────────────────────────────────────
   • UAV exists?               no → {authorized:false, error:"UAV not found"}
   • UAV.status deregistered?  yes → {authorized:false, error:"UAV deregistered"}
   • UAV.status grounded?      yes → {authorized:false, error:"UAV grounded"}
   • For every waypoint in flight_plan.waypoints:
       for every active NFZ:
         (lat, lon) inside NFZ box?
         (alt cap NULL or alt ≤ NFZ.alt_max_m)?
         → record violation
   • violations non-empty?     yes → {authorized:false, error:"no-fly zone violation",
                                       violations:[...]}
   • else:
       INSERT uas_flight_auth (status='authorized')
       UPDATE uas_registry SET status='active'
       restrictions := envelope hints from UAV.max_speed_mps / max_altitude_m
       → {authorized:true, flight_id, restrictions}
```

### A.7 The §5.5 C2 lifecycle

```
   EstablishC2(uav_id, controller_id, qos_5qi=3)
   ─────────────────────────────────────────────
   • UAV exists?  no → 404 / error
   • Active C2 session for this UAV already?  yes → 409 (one-per-UAV rule)
   • INSERT uas_c2_sessions (status='active', qos_5qi)

   GetC2Status(c2_id) → row or 404
   TerminateC2(c2_id) → status='terminated'
   FailoverC2(c2_id)  → status='failed' (manual_failover_required hint;
                        TS 23.256 §5.6 link recovery is the deferred wire)
```

### A.8 Net-RID — what `RemoteIDBroadcast` returns

`GET /api/uas/remote-id/{uav_id}` returns the canonical
**TS 23.256 §5.2.5** Net-RID payload, with field names borrowed
from **ASTM F3411-22a §4**:

```json
{
  "ua_type": "Rotorcraft",
  "id_type": "serial_number",
  "uas_id":  "<serial_number or uav_id fallback>",
  "uav_id":  "UAV-...",
  "serial_number": "SN-...",
  "operator_id":   "<imsi if known>",
  "latitude":  37.7749,
  "longitude": -122.4194,
  "geodetic_altitude_m": 50,
  "height_agl_m":        50,
  "direction_deg":       90,
  "speed_horizontal_mps": 5,
  "timestamp":     "<RFC3339>",
  "timestamp_utc": "<RFC3339>",
  "flight_id":      "FLT-... or null",
  "flight_status":  "authorized | none | ..."
}
```

Direct (BLE / Wi-Fi NaN) Remote ID broadcast is **out of 5GC
scope** — that's the UAV's local responsibility (ASTM F3411 §5);
the deferred TODO is documented in source.

### A.9 What is NOT in scope here

| Thing | Where it lives |
|-------|----------------|
| **UAA wire** (UAS NF ↔ USS) | TS 23.256 §5.2.1 / §5.2.7; UAE-USS / UAE-UTM HTTP/JSON API deferred. |
| **C2 link recovery** | TS 23.256 §5.6; `FailoverC2` returns a manual hint. |
| **UAV swarm group communication** | TS 23.256 §5.4; deferred. |
| **DAA (Detect-and-Avoid) data exchange** | TS 23.256 §5.2.2; deferred. |
| **Direct Remote ID** | ASTM F3411 §5 (BLE / Wi-Fi NaN); UAV-local concern. |
| **C2 controller authentication** | TS 22.125 §5.3.x; today the route accepts the controller_id string verbatim. |
| **PDU session establishment for C2** | `nf/smf/`; this package records the C2 session row but doesn't program SMF/UPF. |

---

## Part B — Design

### B.1 Architecture

```
   Operator REST  ──────────────────────────────────────────────┐
   /api/uas/*                                                    │
        │                                                        │
        ▼                                                        │
   ┌──────────────────────────────────────────────────────────┐ │
   │       services/uas/uas.go  (Go package, this design)      │ │
   │                                                           │ │
   │   UAV registry CRUD          → uas_registry              │ │
   │   AuthorizeFlight + revoke   → uas_flight_auth + status  │ │
   │   No-Fly Zone CRUD           → uas_no_fly_zones          │ │
   │   UpdatePosition + history   → uas_positions             │ │
   │   RemoteIDBroadcast          → registry × positions ×    │ │
   │                                 active flight            │ │
   │   DetectAnomaly              → registry × positions ×    │ │
   │                                 NFZ × active flight      │ │
   │   EstablishC2 / Status /     → uas_c2_sessions           │ │
   │     Terminate / Failover                                 │ │
   └──────────┬───────────────────────────────────────────────┘ │
              │                                                  │
              ▼                                                  │
   ┌──────────────────────────────────────────────────────────┐ │
   │             SQLite (db/schemas/domains.go::UasDDL)        │ │
   │                                                           │ │
   │   uas_registry      (id PK, imsi, uav_id UNIQUE NOT NULL, │ │
   │                      serial_number, manufacturer, model,  │ │
   │                      max_speed_mps DEFAULT 20.0,          │ │
   │                      max_altitude_m DEFAULT 120.0,        │ │
   │                      status CHECK ∈ {registered, active,  │ │
   │                          grounded, deregistered})          │ │
   │   uas_flight_auth   (id, uav_id FK→registry CASCADE,      │ │
   │                      flight_id UNIQUE, flight_plan_json,  │ │
   │                      authorized, restrictions,            │ │
   │                      status CHECK ∈ {pending, authorized, │ │
   │                          active, completed, revoked})      │ │
   │   uas_no_fly_zones  (id, name, lat/lon box, alt_max_m,    │ │
   │                      reason, active)                       │ │
   │   uas_positions     (id, uav_id, lat, lon, alt_m,          │ │
   │                      heading_deg, speed_mps, timestamp)    │ │
   │   uas_c2_sessions   (id, uav_id, controller_id, qos_5qi,   │ │
   │                      status, created_at)                   │ │
   └──────────────────────────────────────────────────────────┘ │
                                                                 │
                                                       wire-format│
                                                       UAE-USS /  │
                                                       UAE-UTM    │
                                                       (TS 22.125 │
                                                       §5.4; deferred)
```

### B.2 Field → spec map

| Field / row | Spec § |
|-------------|--------|
| `uas_registry.uav_id` | TS 23.256 §5.2.5 (CAA-Level UAV ID; ASTM F3411 id-type=`serial_number` when populated) |
| `uas_registry.imsi` | TS 23.256 §5.2.1 (UAV is a UE) |
| `uas_registry.max_speed_mps` / `max_altitude_m` | TS 23.256 §5.2.4 envelope inputs to authorisation |
| `uas_registry.status` | TS 23.256 §5.2.4 (authorisation transitions register → active) |
| `uas_flight_auth.*` | TS 23.256 §5.2.4 (UAV flight authorisation outcome) |
| `uas_no_fly_zones.*` | TS 23.256 §5.2.4 (forbidden volumes) |
| `uas_positions.*` | TS 23.256 §5.2.5 / §5.2.6 location reporting + tracking |
| `uas_c2_sessions.qos_5qi` | TS 23.256 §5.5 (default 5QI=3 per TS 23.501 Table 5.7.4-1) |
| Net-RID payload field names | ASTM F3411-22a §4 (referenced from TS 23.256 §5.2.5) |

### B.3 File map

| File | Role |
|------|------|
| `services/uas/uas.go` | Types, FSM, all public API, SQL access |
| `services/uas/uas_test.go` | Lifecycle + authorisation + C2 tests |
| `db/schemas/domains.go::UasDDL` | DDL: registry, flight_auth, NFZ, positions, c2_sessions |
| `webservice/app/routes_uas.go` | REST surface `/api/uas/*` |
| `webservice/app/domain_routes.go` | Wires `registerUASRoutes()` into `RegisterDomainRoutes` |

### B.4 REST surface

| Method | Path | Backing | Notes |
|--------|------|---------|-------|
| `GET` | `/api/uas/status` | `GetUASStats` | Aggregate counters. |
| `GET` | `/api/uas/registry` | `ListUAVs` | All non-deregistered UAVs. |
| `GET` | `/api/uas/registry/{uav_id}` | `GetUAVByUAVID` | 404 if missing. |
| `POST` | `/api/uas/registry` | `RegisterUAV` | TS 23.256 §5.2.1; auto-allocates `uav_id` when blank. |
| `DELETE` | `/api/uas/registry/{key}` | `DeleteUAV` (numeric) or `DeleteUAVByUAVID` (string) | Accepts either form. |
| `POST` | `/api/uas/authorize-flight` | `AuthorizeFlight` | TS 23.256 §5.2.4; returns `{authorized, flight_id, restrictions}` on success or `{authorized:false, violations:[…]}`. |
| `POST` | `/api/uas/revoke-authorization` | `RevokeAuthorization` | Sets flight status='revoked'. |
| `GET` | `/api/uas/authorization/{uav_id}` | `CheckAuthorization` | Latest authorised flight. |
| `GET` | `/api/uas/no-fly-zones` | `ListNoFlyZones` | Active zones only. |
| `POST` | `/api/uas/no-fly-zones` | `CreateNoFlyZone` | 400 on inverted lat/lon box. |
| `DELETE` | `/api/uas/no-fly-zones/{zone_id}` | `DeleteNoFlyZone` | Soft (`active=0`). |
| `POST` | `/api/uas/position` | `UpdatePosition` | TS 23.256 §5.2.5/§5.2.6. |
| `GET` | `/api/uas/position/{uav_id}` | `GetPosition` | 404 if no position rows. |
| `GET` | `/api/uas/position/{uav_id}/history?limit=` | `GetFlightHistory` | Recent first. |
| `GET` | `/api/uas/anomaly/{uav_id}` | `DetectAnomaly` | §5.2.6 envelope check. |
| `GET` | `/api/uas/remote-id/{uav_id}` | `RemoteIDBroadcast` | §5.2.5 Net-RID; ASTM F3411-22a fields. |
| `POST` | `/api/uas/c2/establish` | `EstablishC2` | TS 23.256 §5.2.3 + §5.5; default 5QI=3; **409** when UAV already has active C2. |
| `GET` | `/api/uas/c2/status/{c2_id}` | `GetC2Status` | 404 if missing. |
| `DELETE` | `/api/uas/c2/{c2_id}` | `TerminateC2` | status='terminated'. |
| `POST` | `/api/uas/c2/{c2_id}/failover` | `FailoverC2` | status='failed' + manual hint (§5.6 stub). |

### B.5 Key types / public API

```go
type UAV struct {
    ID, MaxSpeedMPS, MaxAltitudeM int64    // sketch — see source for full type
    IMSI, UAVID, SerialNumber, Manufacturer, Model, Status, CreatedAt string
    /* speed/alt are *float64 in the actual struct */
}

type FlightAuth struct {
    ID                                 int64
    UAVID, FlightID, Status, CreatedAt string
    FlightPlanJSON, Restrictions, AuthorizedAt *string
    Authorized                         int
}

type NoFlyZone struct {
    ID                          int64
    Name, CreatedAt             string
    LatMin, LatMax, LonMin, LonMax float64
    AltMaxM                     *float64
    Reason                      *string
    Active                      int
}

type C2Session struct {
    ID                                  int64
    UAVID, ControllerID, Status, CreatedAt string
    QoS5QI                              int
}

const C2Default5QI = 3  // TS 23.501 Table 5.7.4-1 (§5.5)

// Registry (§5.2.1)
func ListUAVs() ([]UAV, error)
func GetUAV(id int64) (*UAV, error)
func GetUAVByUAVID(uavID string) (*UAV, error)
func RegisterUAV(imsi, uavID, serial, mfr, model string, maxSpd, maxAlt float64) (int64, error)
func DeregisterUAV(id int64) error
func DeleteUAV(id int64) error
func DeleteUAVByUAVID(uavID string) error

// Flight authorisation (§5.2.4)
func AuthorizeFlight(uavID string, plan map[string]interface{}) (map[string]interface{}, error)
func RevokeAuthorization(flightID string) error
func CheckAuthorization(uavID string) map[string]interface{}

// No-Fly Zones (§5.2.4)
func ListNoFlyZones() ([]NoFlyZone, error)
func CreateNoFlyZone(name string, latMin, latMax, lonMin, lonMax float64,
    altMaxM *float64, reason string) (int64, error)
func DeleteNoFlyZone(id int64) error

// Position / Net-RID (§5.2.5 / §5.2.6)
func UpdatePosition(uavID string, lat, lon, alt, heading, speed float64) error
func GetPosition(uavID string) map[string]interface{}
func GetFlightHistory(uavID string, limit int) []map[string]interface{}
func DetectAnomaly(uavID string) map[string]interface{}
func RemoteIDBroadcast(uavID string) (map[string]interface{}, error)

// C2 (§5.2.3 + §5.5)
func EstablishC2(uavID, controllerID string, qos5qi int) (map[string]interface{}, error)
func GetC2Status(sessionID int64) (*C2Session, error)
func TerminateC2(sessionID int64) error
func FailoverC2(sessionID int64) (map[string]interface{}, error)

// Aggregates
func GetUASStats() map[string]interface{}
```

### B.6 Tester coverage

| Test | Spec § | Asserts |
|------|--------|---------|
| `TC-UAS-001 uas_register_uav` | §5.2.1 | Register + delete; carries unique uav_id. |
| `TC-UAS-002 uas_authorize_flight` | §5.2.4 | Plan against an empty NFZ set → authorised. |
| `TC-UAS-003 uas_update_position` | §5.2.5 | Update + read back. |
| `TC-UAS-004 uas_no_fly_zone` | §5.2.4 | NFZ create + delete. |
| `TC-UAS-005 uas_c2_session` | §5.2.3 / §5.5 | Establish + status. |
| `TC-UAS-006 uas_remote_id` | §5.2.5 | Net-RID payload contains uav_id, serial_number, lat/lon. |
| `TC-UAS-007 uas_no_fly_zone_violation` | §5.2.4 | Plan crossing NFZ → `authorized=false` + `violations`. |
| `TC-UAS-008 uas_c2_single_active` | §5.5 | Second EstablishC2 → HTTP 409; first stays active. |
| `TC-UAS-009 uas_anomaly_detect` | §5.2.6 | Position outside (max_speed, max_alt) → `anomaly=true` with both axes named. |

All nine currently PASS (`11b2f8d` core × `5f32449` tester).

### B.7 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| Package header (`services/uas/uas.go:31-46`) | TS 23.256 §5.2.2 (DAA), §5.2.7 (UAS NF discovery via NRF), §5.4 (group / swarm), §5.6 (C2 link recovery — `FailoverC2` is a manual-hint stub); TS 22.125 §5.3.x (controller auth); ASTM F3411 §5 (Direct Remote ID — UAV-local). |
| `FailoverC2` body | §5.6 — full alternate-DRB / redundant-DN switch is the next step. |

### B.8 References

Only specs cited in source:

- **TS 22.125** — UAS service requirements (§5)
- **TS 23.256** — Support of Uncrewed Aerial Systems (UAS)
  - §5.2.1 UAV authentication & authorisation (registry)
  - §5.2.3 UAV ↔ UAV-C C2 pairing
  - §5.2.4 UAV flight authorisation with USS/UTM
  - §5.2.5 Network Remote Identification (Net-RID)
  - §5.2.6 UAV location reporting / tracking
  - §5.5 C2 communication (default 5QI=3)
- **TS 23.501** — Table 5.7.4-1 (5QI=3 V2X / DCC characteristic; referenced by §5.5)
- **ASTM F3411-22a** §4 — Remote ID broadcast field semantics

Cross-link: `nf/smf/` is the consumer that programmes the actual C2
PDU session (5QI=3) onto the UPF; this package only records the C2
session intent. `services/v2x/` shares the §5QI=3 / DCC profile but
its authorisation surface is distinct.

---
*Last refreshed against commit `11b2f8d`.*

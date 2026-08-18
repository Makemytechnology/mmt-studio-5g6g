# ProSe — Design Document

5G Proximity Services (D2D over PC5) — operator-side state for the
**TS 23.304** ProSe reference architecture: per-UE authorization &
policy provisioning, Direct Discovery (Models A and B), Direct
Communication (broadcast / groupcast / unicast), and UE-to-Network
relay.

---

## Part A — Functional view

### A.1 What ProSe is, in plain terms

Two phones near each other don't always need to talk through the
cell tower. The **PC5 sidelink** lets them talk directly. ProSe is
the operator-side rules engine that decides **who is allowed** to
use that sidelink, **how they find each other** ("discovery"),
**how they speak** once found (unicast / groupcast / broadcast),
and — when one of them has signal and the other doesn't — **who
gets to be the relay** that bridges the second UE back into the 5GC.

This package is not a UE-side stack and not the radio. It's the
control plane: the policy that gates use, the discovery state, the
session ledger, and the relay registry.

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| **Public safety / first-responder comms** | Mission-critical voice / data needs to keep working when the cell is down or congested. ProSe relay extends coverage; D2D voice keeps calls inside a cordon. |
| **Coverage extension** | One UE with signal can carry traffic for another UE in a basement / tunnel / disaster area as a UE-to-Network relay. |
| **Local low-latency comms** | Vehicles, factory robots, drones can talk directly without a backhaul round-trip. |
| **Group push-to-talk / broadcast** | Groupcast PC5 is the natural delivery for fleet / construction / event radio. |
| **Privacy-preserving discovery** | The network only learns who you announced to — not who you actually talked to. |
| **New revenue surface** | Operators can sell ProSe-enabled apps (mass-events, factory-floor, fleet) as differentiated products with their own Application Codes. |

### A.3 Customer use cases (TS 22.278 §6)

| Use case | What ProSe provides |
|----------|---------------------|
| **Public safety voice / data** | TS 22.278 mandates direct UE-to-UE comms for first responders even out of network coverage. |
| **Industrial private networks** | Robot-to-robot / robot-to-AGV signalling without cabling, low jitter, no cell hop. |
| **In-vehicle / V2X-adjacent** | Sensor sharing across nearby vehicles uses the same PC5; V2X (`services/v2x`) is the safety-class peer; ProSe is the consumer/communication peer. |
| **Stadium / event broadcast** | Groupcast announcements, push-to-talk for staff, fan-app overlays. |
| **Smart-city / wearable** | Phones discovering nearby connected devices for payment, ticketing, AR. |
| **Coverage extension** | Indoor UE relays through an outdoor UE; a remote UE in a basement reaches the 5GC via a UE-to-Network relay. |

### A.4 Actors and roles

```
   UE A (announcer)              UE B (monitor)
      │   PC5 (TS 24.555)          │
      │   ProSe Application Code   │
      ▼                             ▼
   ┌────────────────────────────────────────────────────────────┐
   │                services/prose  (this package)                │
   │                                                              │
   │   prose_apps        ── §5.2 Application Code registry        │
   │   prose_ue_config   ── §5.1 PCF authorization (per-feature)  │
   │   announcements     ── §5.2 in-flight presence (in-memory)   │
   │   prose_sessions    ── §5.3 unicast/groupcast/relay rows     │
   │   relayRegistry     ── §5.4 UE-to-Network relay (in-memory)  │
   │   prose_discovery_filters ── §5.2 monitor filters            │
   └────────────────────────────────────────────────────────────┘
                                      ▲
                                      │ /api/prose/*
                                      │
                               ┌──────┴───────┐
                               │   Operator   │
                               │  REST surface│
                               └──────────────┘

      UE C (remote)             UE D (relay)            5GC (Uu)
         │ PC5                     │ PC5+Uu (§5.4)         │
         │  via D ─────────────────┼──────────────────────▶│
         │                         │                       │
```

| Actor | Role | Touches this package via |
|-------|------|--------------------------|
| **Announcing UE** | Periodically broadcasts its ProSe Application Code (§5.2 Model A). | `Announce`, `Withdraw` |
| **Monitoring UE** | Collects announcements matching a filter (§5.2 Model B). | `Monitor`, `GetActiveAnnouncements` |
| **Communication peer** | Sets up unicast/groupcast on PC5. | `SetupUnicastWithAuth`, `SetupGroupcastWithAuth`, `Release` |
| **Relay UE** | Carries another UE's traffic toward the 5GC (§5.4). | `RegisterRelay`, `DiscoverRelays` |
| **Remote UE** | Has no direct cell signal; reaches 5GC via the relay (§5.4). | `ConnectViaRelay` |
| **PCF (TS 23.304 §4.2)** | Authorises and provisions UE feature flags. | `SetUEConfig`, `CheckAuthorization` |
| **Operator (OAM)** | Curates the application registry, manages per-UE policy, audits sessions. | `/api/prose/*` |

### A.5 Operator workflow

```
   Provisioning
   ────────────
   1.  POST /api/prose/apps          §5.2 application registry
                                     (app_id, name, prose_app_code,
                                      validity_hours)
   2.  POST /api/prose/ue-config     §5.1 PCF policy per UE
                                     (authorized + per-feature flags:
                                      discovery_enabled,
                                      communication_enabled,
                                      relay_capable, relay_enabled)

   Discovery (TS 23.304 §5.2)
   ──────────────────────────
   3.  POST /api/prose/discovery/announce   Model A — "I am here"
   4.  POST /api/prose/discovery/monitor    Model B — "Who is there?"
   5.  GET  /api/prose/discovery/active     all current announcements

   Communication (TS 23.304 §5.3)
   ──────────────────────────────
   6.  POST /api/prose/communication/setup
            { source_imsi, target_imsi, session_type:
              "unicast" | "groupcast", group_id?, service? }
            §5.3.3 groupcast or §5.3.4 unicast
   7.  POST /api/prose/communication/{id}/release   §6.4.3.3 release

   Relay (TS 23.304 §5.4)
   ─────────────────────
   8.  POST /api/prose/relay/register   UE registers as relay
   9.  POST /api/prose/relay/discover   discover relays (auth-gated)
   10. POST /api/prose/relay/connect    remote UE → through relay UE

   Audit
   ─────
   11. GET  /api/prose/sessions                       list w/ filters
   12. GET  /api/prose/authorization/{imsi}           current §5.1 verdict
   13. GET  /api/prose/status                         aggregate counters
```

### A.6 The §5.1 authorization gate

```
   authorizeUE(imsi, service)
   ──────────────────────────
   • prose_ue_config row missing                → false (HTTP 403)
   • cfg.authorized == 0                        → false (HTTP 403)
   • service == "discovery"      → cfg.discovery_enabled
   • service == "communication"  → cfg.communication_enabled
   • service == "relay"          → cfg.relay_enabled AND cfg.relay_capable
```

Every state-changing surface (announce, monitor, communication
setup, relay register / discover / connect) runs this gate before
mutating state. Routes map a denied verdict to **HTTP 403** — the
spec is explicit that the PCF gates these features independently
(§5.1).

### A.7 Discovery models — when each fires

| Model | Spec § | Direction | Use case |
|-------|--------|-----------|----------|
| **Model A — "I am here"** | §5.2 | Announcer-driven push | Periodic presence broadcast (e.g., "I am ride-share driver 42"). |
| **Model B — "Who is there?"** | §5.2 | Monitor-driven query | Pull-style "find someone offering service X" with a filter. |
| **Open Discovery** | §5.2.3 | Either model, **no per-app permission filtering** | Modelled here. |
| **Restricted Discovery** | §5.2.4 | Per-app permission lists; ProSe Restricted Discovery Code material | **Deferred TODO**. |

### A.8 What is NOT in scope here

| Thing | Where it lives |
|-------|----------------|
| **Restricted (closed) discovery** | TS 23.304 §5.2.4; deferred. |
| **Discovery message protection** (5G ProSe code material, integrity) | TS 23.304 §5.2.6; deferred. |
| **PC5 Layer-2 link establishment** (PC5-S Direct Link Establishment Request / Accept) | TS 23.304 §6.4.3.1, TS 24.555 §5; only the DB row is modelled. |
| **PC5 RRC / link security activation** | TS 24.555 §6 + TS 33.503; deferred. |
| **UE-to-Network relay path keying** | TS 23.304 §5.4.4 + N3IWF interaction; deferred. |
| **UE-to-UE relay (§5.5)** | Deferred. |
| **NAS-layer ProSe procedures (PC3 / PC3a)** | TS 24.554 §5 wire deferred; outcome persisted only. |

---

## Part B — Design

### B.1 Architecture

```
   Operator REST  ──────────────────────────────────────────────┐
   /api/prose/*                                                  │
        │                                                        │
        ▼                                                        │
   ┌──────────────────────────────────────────────────────────┐ │
   │   services/prose/                                         │ │
   │     prose.go      — App / UEConfig / Session CRUD          │
   │     discovery.go  — announcements, monitor, relay reg,     │
   │                     authorization gate                     │
   │                                                           │ │
   │   Auth gate: authorizeUE(imsi, service ∈ {discovery,       │
   │                          communication, relay})           │ │
   │                                                           │ │
   │   In-memory state (process-local, sync.Mutex):            │ │
   │     announcements   key=(imsi, app_code)                  │ │
   │     relayRegistry   key=imsi                              │ │
   │                                                           │ │
   │   Persistent state (SQLite):                              │ │
   │     prose_apps                                            │ │
   │     prose_ue_config                                       │ │
   │     prose_sessions                                        │ │
   │     prose_discovery_filters                               │ │
   └──────────┬───────────────────────────────────────────────┘ │
              │                                                  │
              ▼                                                  │
                                                       wire-format│
                                                       PC5-S      │
                                                       (TS 24.555 │
                                                        §5;       │
                                                        deferred) │
```

### B.2 Field → spec map

| Field / row | Spec § |
|-------------|--------|
| `prose_apps.prose_app_code` | TS 23.304 §5.2 (the discovery key) |
| `prose_ue_config.authorized` | TS 23.304 §5.1 master enable |
| `prose_ue_config.discovery_enabled` | §5.1 / §5.2 (Models A & B) |
| `prose_ue_config.communication_enabled` | §5.1 / §5.3 (groupcast/unicast/broadcast) |
| `prose_ue_config.relay_capable` / `relay_enabled` | §5.1 / §5.4 (capability + policy) |
| `prose_sessions.session_type` | §5.3.2 broadcast / §5.3.3 groupcast / §5.3.4 unicast / §5.4 relay |
| announcement validity | §5.2 (the announcement TTL) |
| relayRegistry validity (1800 s) | §5.4 (operator-local relay registration TTL) |

### B.3 File map

| File | Role |
|------|------|
| `services/prose/prose.go` | App / UEConfig / Session SQL CRUD |
| `services/prose/discovery.go` | Announce / Monitor, relay registry, auth gate |
| `services/prose/prose_test.go` | Coverage of CRUD + gate + lifecycle |
| `webservice/app/routes_prose.go` | REST surface `/api/prose/*` |
| `webservice/app/domain_routes.go` | Wires `registerProSeRoutes()` |

### B.4 REST surface

| Method | Path | Backing | Notes |
|--------|------|---------|-------|
| `GET` | `/api/prose/status` | `GetStats` | Counters (apps / configs / sessions / relays). |
| `GET` | `/api/prose/apps` | `ListApps` | App registry. |
| `POST` | `/api/prose/apps` | `CreateApp` | TS 23.304 §5.2. |
| `DELETE` | `/api/prose/apps/{app_id}` | `DeleteApp` | |
| `GET` | `/api/prose/ue-config?imsi=` | `GetUEConfig` | Returns `{authorized:false}` if no row. |
| `POST` | `/api/prose/ue-config` | `SetUEConfig` | Upsert; per-feature flags (§5.1). |
| `GET` | `/api/prose/authorization/{imsi}` | `CheckAuthorization` | 404 if no row. |
| `POST` | `/api/prose/discovery/announce` | `Announce` | Model A; auth deny → 403. |
| `DELETE` | `/api/prose/discovery/announce?imsi=&app_code=` | `Withdraw` | |
| `POST` | `/api/prose/discovery/monitor` | `Monitor` | Model B; auth deny → 403. |
| `GET` | `/api/prose/discovery/active` | `GetActiveAnnouncements` | All non-expired. |
| `POST` | `/api/prose/communication/setup` | `SetupUnicastWithAuth` / `SetupGroupcastWithAuth` | §5.3.3/§5.3.4; auth deny → 403. |
| `POST` | `/api/prose/communication/{session_id}/release` | `Release` | §6.4.3.3. |
| `GET` | `/api/prose/sessions[?imsi=&status=]` | `ListSessions` | Audit. |
| `POST` | `/api/prose/relay/register` | `RegisterRelay` | §5.4; auth deny → 403. |
| `POST` | `/api/prose/relay/discover` | `DiscoverRelays` | Auth deny → 403. |
| `POST` | `/api/prose/relay/connect` | `ConnectViaRelay` | 403 / 404 / 201 based on outcome. |
| `GET` | `/api/prose/relays/active` | `GetActiveRelays` | Registered relays. |

### B.5 Key types / public API

```go
type App struct {
    ID            int64
    AppID, Name, ProseAppCode, CreatedAt string
    ValidityHours int
}

type UEConfig struct {
    ID                                          int64
    IMSI, UpdatedAt                             string
    Authorized, DiscoveryEnabled, CommunicationEnabled,
    RelayCapable, RelayEnabled                  int
}

type Session struct {
    ID                                int64
    SessionType, SourceIMSI, Status, CreatedAt string
    TargetIMSI, GroupID, RelayIMSI, Service, ReleasedAt *string
}

// App registry (§5.2)
func ListApps() ([]App, error)
func CreateApp(appID, name, code string, validityHours int) (int64, error)
func DeleteApp(appID string) error

// UE config / auth (§5.1)
func GetUEConfig(imsi string) (*UEConfig, error)
func SetUEConfig(imsi string, authorized, discoveryEnabled, commEnabled, relayCapable, relayEnabled int) error
func CheckAuthorization(imsi string) map[string]interface{}

// Discovery (§5.2)
func Announce(imsi, appCode string, validitySec int, metadata map[string]interface{}) map[string]interface{}
func Withdraw(imsi, appCode string) bool
func Monitor(imsi string, filters map[string]interface{}) map[string]interface{}
func GetActiveAnnouncements() []map[string]interface{}

// Communication (§5.3)
func SetupUnicast(sourceIMSI, targetIMSI, service string) (int64, error)
func SetupGroupcast(sourceIMSI, groupID, service string) (int64, error)
func SetupUnicastWithAuth(sourceIMSI, targetIMSI, service string) map[string]interface{}
func SetupGroupcastWithAuth(sourceIMSI, groupID, service string) map[string]interface{}
func ReleaseSession(id int64) error
func Release(sessionID int64) map[string]interface{}
func ListSessions(imsi, status string) ([]Session, error)
func DeleteSession(id int64) error

// Relay (§5.4)
func RegisterRelay(imsi, serviceCode, connectivity string) map[string]interface{}
func DiscoverRelays(imsi, serviceCode string) map[string]interface{}
func ConnectViaRelay(remoteIMSI, relayIMSI string) map[string]interface{}
func GetActiveRelays() []map[string]interface{}

// Aggregates
func GetStats() map[string]interface{}
```

### B.6 Tester coverage

| Test | Spec § | Asserts |
|------|--------|---------|
| `TC-PROSE-001 prose_register_app` | §5.2 | App registry CRUD. |
| `TC-PROSE-002 prose_ue_config` | §5.1 | UE config write + readback. |
| `TC-PROSE-003 prose_discovery` | §5.2 (A+B) | Announce + monitor; UE-self exclusion respected. |
| `TC-PROSE-004 prose_communication` | §5.3.4 | Unicast setup + release; requires `communication_enabled`. |
| `TC-PROSE-005 prose_relay` | §5.4 | Register relay + discover; requires `relay_capable + relay_enabled`. |
| `TC-PROSE-006 prose_authorization_gate` | §5.1 | Unauthorised UE → 403 on announce + setup. |

All six currently PASS (`f26aff6` core × `3086ce4` tester).

### B.7 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| `discovery.go:19-23` | TS 23.304 §5.2.4 (Restricted Discovery), §5.4.4 (relay path keying / N3IWF). |
| `prose.go` package header | TS 24.554 §5 (NAS-layer wire), TS 24.555 §5/§6 (PC5-S link establishment + security). |
| `SetupUnicast` / `SetupGroupcast` | TS 23.304 §6.4.3.1 — full PC5 Layer-2 link establishment is the deferred wire. |

### B.8 References

Only specs cited in source:

- **TS 22.278** — 5G ProSe service requirements
- **TS 23.304** — Architectural enhancements for 5G ProSe
  - §5.1 Authorization & policy provisioning
  - §5.2 Direct Discovery (Models A, B; Open §5.2.3)
  - §5.3 Direct Communication (broadcast §5.3.2, groupcast §5.3.3, unicast §5.3.4)
  - §5.4 UE-to-Network relay
  - §6.4 Procedures
- **TS 24.554 §5** — NAS-layer ProSe procedures (deferred wire)
- **TS 24.555 §5** — PC5 signalling protocol (deferred wire)

Cross-link: `services/v2x/` is the safety-class peer over the same
PC5 radio; the two services share the carrier but have independent
authorisation (V2X subscription vs. ProSe ue-config).

---
*Last refreshed against commit `f26aff6`.*

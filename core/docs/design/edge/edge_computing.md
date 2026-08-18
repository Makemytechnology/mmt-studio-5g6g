# Edge Computing — Design Document

The umbrella view of edge computing in MMT Studio Core: what it
delivers to operators and customers, and how the EAS registry, MEC
orchestrator, EASDF lookup, AF-influence rules, and ULCL install
state fit together end-to-end.

> Spec anchors:
> - **TS 23.501 §5.13** *Edge Computing* — the umbrella; defines AF
>   influence, ULCL/BP, local PSA, LADN.
> - **TS 23.501 §5.6.5** — LADN service area as a set of TAIs.
> - **TS 23.501 §5.6.4** — Uplink Classifier and Branching Point.
> - **TS 23.502 §4.3.6** — Application Function influence on traffic
>   routing.
> - **TS 23.548** — 5G System Enhancements for Edge Computing
>   (EAS Discovery, EAS↔DNAI mapping).
> - **TS 23.558** — Architecture for enabling Edge Applications
>   (EDGEAPP: EEC / EES / ECS / EAS roles).
> - **TS 29.244 §7.5.4** — PFCP Session Modification (ULCL install
>   carrier).
> - **TS 29.558** — EDGEAPP wire-format APIs (deferred).

Per-package deep-dives:
[`eas.md`](eas.md) · [`mec.md`](mec.md).

---

## Part A — Functional view

### A.1 Why edge computing exists in this network

A 5G operator's user plane normally backhauls traffic from the gNB
to a centralised PSA-UPF, then out to the data network. That works
for browsing and streaming, but breaks down for three classes of
service:

1. **Latency-sensitive apps** (cloud gaming, AR/VR, robotics) that
   cannot tolerate the round-trip to a far-away PSA.
2. **Bandwidth-heavy local apps** (factory video, stadium replay,
   CDN egress) that would saturate backhaul if every UE pulled bytes
   through the central UPF.
3. **Locality-bound services** (private networks, regulatory
   geofencing, factory ATS) where data must stay in a defined
   physical area.

Edge computing solves all three with one architectural move: **put
the application server, and a local PSA-UPF, near the user**. The
challenge becomes *steering* — how does the network know which
UE-app pairs to route locally, and how does the user-plane get
reconfigured to do so without a UE-visible change?

3GPP's answer is the **EDGEAPP** architecture (TS 23.558) plus the
**5GC enhancements** in TS 23.501 §5.13 / TS 23.548 / §5.6.4. This
product implements the operator-facing surface of both.

### A.2 The five roles in edge computing (and where this product sits)

| Role | Spec name | What it does | Where it lives |
|------|-----------|--------------|----------------|
| **EAS** | Edge Application Server | The app itself — runs at an edge site, terminates UE traffic. | Persisted in `edge/eas` (`eas_registry`) — one row per EAS instance. |
| **EES** | Edge Enabler Server | Hosts EAS registration + EAS discovery for an edge site. | **Collapsed into the EAS registry** in this build. The TS 29.558 wire APIs (`Eees_EASRegistration_*`) are deferred. |
| **ECS** | Edge Configuration Server | Hands out EES contact info to the UE's EEC. | Out of scope (UE-side feature). |
| **EEC** | Edge Enabler Client | UE-side library that talks to the ECS/EES. | UE-side; the network only sees its DNS Query for the app FQDN. |
| **MEC orchestrator** | (3GPP doesn't name this) | Operator-facing thing that decides which apps deploy where, and authors AF-influence rules. | `edge/mec` — sites, apps, deployments, traffic rules, ULCL state. |

The product **collapses EES into the EAS registry** (one in-process
surface) and runs the **MEC orchestrator as a small in-memory
operator panel**. Both decisions match a single-box test deployment;
production multi-vendor deployments would replace each with a remote
appliance speaking TS 29.558.

### A.3 What edge computing delivers (operator view)

| Capability | What the operator gets | Business value |
|------------|------------------------|----------------|
| **Edge sites** | Named physical edge locations, each with a TAI list and a local DN attach point. | Defines *where* edge serving can happen. |
| **EAS registry** | Catalog of edge applications: FQDN, DNAI, capacity, supported DNNs/slices, lat/lon. | Single source of truth that Discovery, the panel, and the SMF all read from. |
| **EAS Discovery** | Two procedures: Distributed-Anchor (ranked candidate list, UE-proximity-aware) and Session-Breakout (EASDF answer for a UE DNS Query, DNAI-aware). | The handshake that picks *which* EAS instance gets a particular UE. |
| **DNAI mapping** | Operator-authored map: DNAI string → location hint + UPF-instance hint. | SMF resolves "where does this DNAI's local PSA live?" without a separate config file. |
| **EASDF FQDN table** | DNS-style table: FQDN → EAS row. | Session-Breakout discovery: UE DNS-queries `app.edge.local`, EASDF returns the EAS that should serve it. |
| **App deployment lifecycle** | Deploy / undeploy of an app instance at a site. | OAM-driven dynamic EAS instantiation (TS 23.558 §8.12). |
| **AF traffic-routing influence** | Per-`(AppID, SiteID, DNN)` steering rule with priority. | The operator's lever to say "for this app on this DNN at this site, redirect packets to the local edge target". |
| **ULCL install state** | Per-`(IMSI, PDU session, rule)` row showing whether the SMF→UPF push landed. | Distinguishes *intent* from *enforcement* — operator can debug "rule exists but not enforcing". |
| **Discovery audit log** | One row per `Discover()` call: criteria + picked EAS id. | OAM's "did discovery actually pick the right EAS?" view. |
| **Active-sessions × AF-influence** | Read-only join: live PDU sessions × AF rules that match each session's DNN. | "Which active subscribers are getting edge steering right now?" |

### A.4 Customer use cases this enables

| Use case | Profile |
|----------|---------|
| **Cloud gaming on private 5G** | Edge site = venue; EAS rows per game server; AF-influence routes the gaming DNN locally. |
| **Factory MEC** (deterministic local delivery) | Site = one cell; EAS = controller; pairs naturally with TSN for end-to-end determinism. |
| **Stadium / venue CDN egress** | Site covers the venue; EAS rows per CDN node; AF-influence steers CDN DNN traffic locally. |
| **Geofenced analytics / regulatory** | Site = regulatory zone; AF-influence keeps the analytics DNN in-zone. |
| **Cloud XR / AR rendering** | Render farm at the metro edge; latency-bound discovery picks the closest. |
| **Private 5G enterprise apps** | All apps on-prem; data never crosses the WAN; resilience to upstream cuts. |
| **Dynamic EAS scale-out** | Deploy / undeploy across sites at runtime; running counter reflects the change. |

### A.5 Actors and roles

```
   Operator panel        AF (today: operator   EASDF stub        UE  (PDU            SMF
        │                proxy; future:        (UE DNS Query)        establish)      │
        │                Nnef_TrafficInfluence)│                     │                │
        ▼                ▼                     ▼                     ▼                ▼
   ┌──────────────────────────────────────────────────────────────────────────────────┐
   │                       edge/eas + edge/mec  (this surface)                         │
   │                                                                                   │
   │   eas_registry · eas_dnai_map · eas_dns_entries · eas_discovery_log               │
   │   sites · apps · trafficRules · ulclState                                         │
   └──────────────────────────────────────────────────────────────────────────────────┘
                                              │
                              ListTrafficRules │ RecordULCLInstall
                                              ▼
                                   ┌──────────────────────┐
                                   │  nf/smf/session/     │
                                   │  ulcl.go             │
                                   │   ─ PFCP install     │
                                   │     of Create-PDR /  │
                                   │     Create-FAR       │
                                   └──────────┬───────────┘
                                              ▼
                                          nf/upf  (dataplane)
```

| Actor | Role |
|-------|------|
| **Operator** | Authors sites, registers EASs, publishes FQDNs, writes traffic rules. |
| **Third-party AF** | Brings the application; future Nnef_TrafficInfluence push lands here. |
| **EASDF** | Shapes DNS answers so the UE resolves to the nearest instance. |
| **SMF** | Picks PSA-UPF by LADN at PDU establish, installs ULCL/BP for matching AF rules, records the install verdict. |
| **UPF** | Enforces the installed PDR + FAR. |
| **OAM panel** | Renders catalogue, rules, audit log, active-session × AF-rule joins. |

### A.6 The two EAS Discovery procedures (when each one fires)

#### A.6.1 Distributed-Anchor (TS 23.548 §6.2.2.2)

The PSA is already at the edge — discovery just needs to pick *which*
EAS instance among several candidates. Used when an AF or the UE
holds a list of candidate FQDNs and asks "which one?".

- **Inputs:** `app_id`, optional DNN/SST/DNAI, optional UE lat/lon.
- **Output:** ranked list of EAS rows.
- **Ranking:** operator-policy weights (DNAI match +50, DNN match
  +30, S-NSSAI match +20, capacity 0..20, proximity 0..30 via
  haversine). Spec only mandates "as close as possible to the UE
  topologically"; the weights are local choice.
- **Side effect:** writes a row to the discovery log.

#### A.6.2 Session-Breakout via EASDF (TS 23.548 §6.2.3.2.2)

The PSA is *central*; the SMF needs to insert a **ULCL/BP** and a
Local PSA on the path to a specific edge site. Trigger is the UE's
DNS Query for the app FQDN.

- **Inputs:** the FQDN from the UE's DNS Query + optional criteria.
- **Output:** `EASDFAnswer{FQDN, EAS, DNAI}` — the SMF feeds the
  DNAI into the §6.8 map to insert ULCL/BP/Local-PSA at the right
  edge site.
- **Ranking:** same scoring function but **without** the proximity
  term — the SMF chooses the L-PSA via the §6.8 map, not UE
  topological coordinates.
- **Side effect:** also writes the discovery log row.

### A.7 AF-influence and ULCL — how a packet actually reaches the edge

```
Operator panel / future AF              edge/mec               nf/smf            nf/upf
   │                                       │                     │                  │
   │── POST /api/mec/af-influence ───────▶│                     │                  │
   │   { app_id, site_id, dnn,            │ AddTrafficRule       │                  │
   │     target_ip, target_port,          │ → trafficRules       │                  │
   │     priority }                       │                      │                  │
   │◀── { rule_id }                       │                      │                  │
   │                                       │                     │                  │
   │   (UE later establishes a PDU         │                     │                  │
   │    session for DNN=internet)          │                     │                  │
   │                                       │ ◀── ListTrafficRules │                 │
   │                                       │                     │                  │
   │                                       │                     │── PFCP Modify  ─▶│
   │                                       │                     │   (Create-PDR +  │
   │                                       │                     │    Create-FAR    │
   │                                       │                     │    in 0xE000…    │
   │                                       │                     │    ID space)     │
   │                                       │                     │                  │
   │                                       │ ◀── RecordULCLInstall                  │
   │                                       │   (verdict per rule)                   │
   │                                       │                                        │
   │── GET /api/mec/active-sessions ──────────────────────────▶│                    │
   │   joins smf.session × mec rules × ULCLState                                    │
   │◀── [{imsi, dnn, af_rules:[…], ulcl_state:[…], ulcl_installed:N, …}]           │
```

Today's coverage:

- **Control-plane install lands.** The SMF emits the PFCP Session
  Modify with Create-PDR + Create-FAR; the UPF accepts the rule
  and the install verdict is recorded.
- **Dataplane uplink re-anchor at a Local PSA** — the next piece;
  current C dataplane treats the new PDR/FAR as additional
  match-and-forward state on the same session.

### A.8 Discovery walk-through (UE DNS Query for an edge app)

```
Operator sets up:
  POST /api/mec/sites    {name:"site-A", tais:["00101-0042"], ...}
  POST /api/eas/registry {app_id:"game", endpoint_url:"http://eas.A:8080",
                          dnai:"dnai-A", lat/lon, capacity:200}
  POST /api/eas/dns      {fqdn:"play.edge.local", eas_id:N}
  POST /api/mec/af-influence
                         {app_id:"game", site_id:"edge-001",
                          dnn:"internet", target_ip:"eas.A", priority:90}

UE registers in TAI 00101-0042. AMF→SMF establish for DNN=internet.

SMF establish path:
  → upf/registry picks the regular PSA-UPF.
  → mec.RulesForDNN("internet")   matches our rule
  → installULCLForSession(IMSI, pduSessID, "internet", rules)
       PFCP Modify → UPF (Create-PDR/FAR in 0xE000… ID space)
  → mec.RecordULCLInstall(...)    per rule

UE issues DNS Query for play.edge.local. EASDF stub asks the eas pkg:
  → eas.ResolveDNS("play.edge.local")
       JOIN eas_dns_entries × eas_registry → ResolveAnswer{FQDN, EAS, DNAI}
  → eas.DiscoverViaEASDF(fqdn, criteria) returns the same EAS row
       with proximity-stripped score, plus the DNAI hint.

Operator reads /api/mec/active-sessions:
  [{imsi, pdu_session_id:1, dnn:"internet",
    af_rule_count:1, af_rules:[{rule_id, target_ip:"eas.A", ...}],
    ulcl_state:[{rule_id, pdr, far, target, installed_ok:true}],
    ulcl_installed:1, ulcl_attempted:1}, ...]

Operator reads /api/eas/discovery-log:
  [{imsi, app_id:"game", criteria, results_count:1,
    selected_eas_id:N, ...}]
```

Every line in the walk-through has a corresponding REST endpoint.

### A.9 In scope vs. out of scope

**In scope (delivered):**

- EAS registry (full CRUD, supported DNNs / slices, capacity, status).
- DNAI mapping (operator-authored description / location hint /
  UPF-instance hint).
- EAS Discovery — both Distributed-Anchor (proximity-aware) and
  Session-Breakout-via-EASDF (proximity-stripped).
- EASDF FQDN table (`eas_dns_entries`) — idempotent register, list,
  delete, `ResolveDNS`.
- Discovery audit log — one row per call.
- MEC sites mapped to LADN service areas (TS 23.501 §5.6.5);
  CRUD + lookup-by-TAI.
- MEC apps with CRUD + EASDF FQDN/TAI/app helper (`/api/mec/lookup`).
- EAS deployment lifecycle — deploy / undeploy / find-nearest;
  running-instance counter.
- AF traffic-routing influence rules — CRUD; consumed by SMF
  establish path; visible in `/api/mec/ulcl-rules` and
  `/api/mec/af-influences`.
- **ULCL install at the UPF (control plane)** — SMF emits PFCP
  Modify (Create-PDR + Create-FAR) and records the install verdict
  in `mec.ULCLState`.
- Active-sessions × AF-influence × ULCL-state read view.

**In scope (delivered, but limited surface area):**

- Wire format is JSON, not the TS 29.558 EDGEAPP HTTP/JSON shapes.
- Ranking weights are operator policy, not spec-mandated.
- MEC orchestrator state is in-memory (vanishes on restart). EAS
  registry / DNAI map / FQDN table / discovery log are SQLite-
  persisted.

**Out of scope:**

- **UPF dataplane uplink-fork at a Local PSA** — control-plane install
  lands; the C dataplane currently treats Create-PDR/FAR as added
  match-and-forward state on the same session.
- TS 29.558 EDGEAPP wire APIs.
- AF→NEF→PCF→SMF push of AF-influence (TS 23.502 §4.3.6.2).
- AF-provided EAS Deployment Information stored in UDR
  (TS 23.548 §6.2.3.4).
- EEC ↔ ECS bootstrapping (UE-side).
- Cross-site session continuity (SSC mode 3).

### A.10 Glossary

| Term | Meaning here |
|------|--------------|
| **EAS** | Edge Application Server — the app at the edge. |
| **EES** | Edge Enabler Server — registers EAS, answers discovery (collapsed into the EAS registry here). |
| **ECS** | Edge Configuration Server — bootstraps the UE's EEC (out of scope here). |
| **EEC** | Edge Enabler Client — UE-side library (out of scope here). |
| **EASDF** | EAS Discovery Function — answers UE DNS queries with EAS info. |
| **DNAI** | Data Network Access Identifier — names the network attach point of an EAS. |
| **LADN** | Local Area Data Network (TS 23.501 §5.6.5) — a DN bound to a TAI list. |
| **PSA** | PDU Session Anchor — the UPF that terminates the PDU session toward the DN. |
| **L-PSA** | Local PSA — a PSA placed near the edge, used in Session-Breakout. |
| **ULCL / BP** | Uplink Classifier / Branching Point — UPF feature that splits traffic between PSAs (central + local). |
| **AF-influence** | An operator-authored rule that retargets traffic toward an edge site (TS 23.502 §4.3.6). |
| **TAI** | Tracking Area Identity — `MCC-MNC-TAC`. A site carries a TAI list. |
| **MEC** | Multi-access Edge Computing — the broader (ETSI) edge orchestration term; here the in-process orchestrator. |

### A.11 Spec map (reading order)

1. **TS 23.501 §5.6.5** — LADN service area (foundation for sites).
2. **TS 23.501 §5.13** — Edge Computing umbrella + ULCL/BP, local
   PSA selection.
3. **TS 23.501 §5.6.4** — ULCL / Branching Point specifics.
4. **TS 23.502 §4.3.6** — AF traffic-routing influence (the steering
   lever).
5. **TS 23.548 §6.2** — EAS Discovery procedures (Distributed-
   Anchor vs Session-Breakout).
6. **TS 23.548 §6.8** — EAS↔DNAI mapping (the SMF's hint to place
   the L-PSA).
7. **TS 23.558 §6 / §8** — EDGEAPP architecture roles + EAS
   registration / discovery information flows.
8. **TS 29.244 §7.5.4** — PFCP Session Modification (the wire
   carrying ULCL install).
9. **TS 29.558** — EDGEAPP stage-3 wire APIs (deferred).

---

## Part B — Design

### B.1 Architecture map

```
   ┌──────────────────────────────────────────────────────────────┐
   │  webservice/app:  routes_edge.go  +  routes_mec.go            │
   │                   /api/eas/*       /api/mec/*                 │
   └──────────────────────────┬───────────────────────────────────┘
                              │
            ┌─────────────────┴───────────────────┐
            ▼                                     ▼
   ┌────────────────────┐               ┌──────────────────────┐
   │     edge/eas        │               │      edge/mec         │
   │ (SQLite-persisted)  │               │ (in-memory, sync.Mutex)│
   │                     │               │                       │
   │ eas_registry        │               │ sites                 │
   │ eas_dnai_map        │               │ apps + Instances      │
   │ eas_dns_entries     │               │ trafficRules          │
   │ eas_discovery_log   │               │ ulclState             │
   └────────────────────┘                └──────────┬────────────┘
                                                    │
                                                    │  ListTrafficRules /
                                                    │  RecordULCLInstall
                                                    ▼
                                       ┌─────────────────────────┐
                                       │   nf/smf/session/        │
                                       │   establish.go +         │
                                       │   ulcl.go                │
                                       │   (PFCP Session Modify)  │
                                       └────────────┬────────────┘
                                                    ▼
                                                 nf/upf
                                            (Create-PDR / Create-FAR)
```

### B.2 On-disk schema (EAS side)

Four EAS tables live under `db/schemas/domains.go::EasDDL`. SQLite
text-mode timestamps everywhere — every write goes through
`datetime('now')` (DDL default); comparisons are lexicographic.

#### B.2.1 `eas_registry` — the authoritative EAS catalog

| Column | Type | Constraints | Purpose |
|--------|------|-------------|---------|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | Stable handle the FQDN table + discovery log point at. |
| `app_id` | TEXT | NOT NULL | Operator-curated app handle. Multiple EAS rows can share an `app_id` (different sites for the same app). |
| `name` | TEXT | NULLable | Display name for the panel. |
| `endpoint_url` | TEXT | NOT NULL | Reachable URL of the EAS instance. EASDF substring-matches against this for FQDN→EAS lookup; future migration extracts a dedicated FQDN field (TS 23.558 §8.2.4). |
| `dnai` | TEXT | NULLable | DNAI string the SMF feeds the §6.8 map to pick the L-PSA. |
| `latitude` / `longitude` | REAL | NULLable | UE-proximity input for Distributed-Anchor scoring. |
| `supported_dnns` / `supported_slices` | TEXT | NULLable | Discovery filter inputs (CSV / JSON). |
| `capacity` | INTEGER | NOT NULL DEFAULT 100 | Capacity score input. |
| `active_connections` | INTEGER | NOT NULL DEFAULT 0 | Capacity utilisation; subtracted from capacity in scoring. |
| `status` | TEXT | NOT NULL CHECK ∈ {`active`, `inactive`, `maintenance`} | Discovery filters on `active`; OAM toggles via PUT. |
| `created_at` / `updated_at` | TEXT | NOT NULL DEFAULT `datetime('now')` | Update writes refresh `updated_at`. |

#### B.2.2 `eas_dnai_map` — DNAI string → operator metadata

| Column | Type | Constraints | Purpose |
|--------|------|-------------|---------|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | |
| `dnai` | TEXT | UNIQUE NOT NULL | DNAI string referenced from `eas_registry.dnai`. |
| `description` | TEXT | NULLable | Operator note (which campus / region). |
| `location_hint` | TEXT | NULLable | Free-text geographic hint. |
| `upf_instance` | TEXT | NULLable | Operator-local UPF id; **out-of-spec hint**. |

#### B.2.3 `eas_dns_entries` — EASDF FQDN→EAS table

| Column | Type | Constraints | Purpose |
|--------|------|-------------|---------|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | |
| `fqdn` | TEXT | UNIQUE NOT NULL | Hostname a UE puts in its DNS Query for the edge app. Stored lowercased. |
| `eas_id` | INTEGER | NOT NULL FK → `eas_registry` | Which EAS row answers for this FQDN. CASCADE on EAS delete. |
| `created_at` | TEXT | NOT NULL DEFAULT `datetime('now')` | |

**Idempotency:** `RegisterDNSEntry(fqdn, eas_id)` does
`INSERT … ON CONFLICT(fqdn) DO UPDATE SET eas_id=excluded.eas_id` so
re-registering an FQDN points it at the new EAS without duplicate
rows.

**Index:** `idx_eas_dns_entries_eas_id(eas_id)` so cascading delete
on the EAS row is cheap.

#### B.2.4 `eas_discovery_log` — every Discover call, audited

| Column | Type | Constraints | Purpose |
|--------|------|-------------|---------|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | Insertion order. |
| `imsi` | TEXT | NULLable | Subscriber the discovery was for; null for unattributed calls. |
| `app_id` | TEXT | NULLable | App the criteria asked for. |
| `criteria_json` | TEXT | NULLable | Full marshalled `DiscoveryCriteria`. |
| `results_count` | INTEGER | NOT NULL DEFAULT 0 | How many candidates the call produced. |
| `selected_eas_id` | INTEGER | NULLable | id of the top-ranked candidate (FK-style — no CASCADE). |
| `created_at` | TEXT | NOT NULL DEFAULT `datetime('now')` | |

**Index:** `idx_eas_discovery_log_selected_eas_id(selected_eas_id)`
so OAM can ask "which discoveries picked EAS N?" cheaply.

The audit log is **append-only** — application code never
UPDATEs / DELETEs from it.

### B.3 In-memory orchestrator state (MEC side)

The MEC orchestrator uses **process-local maps**, not SQLite. Sites,
apps, deployments, traffic rules, and ULCL state are operator-
authored runtime that the GUI panel and tester mutate on every CRUD;
persisting them would require schema, migrations, and conflict
semantics a single-box test deployment doesn't need.

```go
var (
    mu           sync.Mutex
    sites        = map[string]*Site{}        // key: "edge-NNN"
    apps         = map[string]*App{}         // key: "eas-NNN"
    trafficRules = map[string]*TrafficRule{} // key: "rule-NNN"
    ulclState    = map[ulclKey]*ULCLState{}  // key: (imsi, pduSess, ruleID)
)
```

- **Lock:** one `sync.Mutex` guards every map. Writes are short;
  reads return slice copies so callers iterate without holding the
  lock.
- **Lifetime:** all state vanishes on restart. Operators re-author
  after restart.
- **What does persist:** the EAS registration outcome lives in
  `eas_registry` (SQLite), so discovery has a stable view even if
  the MEC orchestrator restarts.

#### B.3.1 `Site`

```go
type Site struct {
    SiteID, Name      string
    TAIs              []string  // TS 23.501 §5.6.5 LADN service area
    LocalDNIP, LocalDNCIDR string
    Capacity          int
    Status            string    // active | maintenance | offline
    CreatedAt         float64
}
```

`FindSiteByTAI(tai)` — scans for a site whose TAI list contains
`tai`. Used by the EASDF / SMF when picking an L-PSA.

#### B.3.2 `App` + `AppInstance`

```go
type App struct {
    AppID, Name, FQDN, DNN, IPFilter, Protocol string
    Port, Priority                              int
    Instances map[string]*AppInstance           // key: site_id
    CreatedAt float64
}

type AppInstance struct {
    SiteID, AppIP, Status string
    AppPort, ActiveSessions int
    DeployedAt float64
}
```

Lookup helpers: `FindAppByFQDN`, `FindNearestInstance(appID, tai)`.

#### B.3.3 `TrafficRule` (AF-influence — the ULCL "intent")

```go
type TrafficRule struct {
    RuleID, AppID, SiteID, DNN, TargetIP, TargetFQDN string
    TargetPort, Priority int
    CreatedAt float64
}
```

Match helpers: `RulesForDNN(dnn)` (used by the SMF establish hook +
the OAM active-sessions view), `RulesForApp(appID, dnn)`.

#### B.3.4 `ULCLState` (the ULCL "verdict")

```go
type ULCLState struct {  // TS 23.501 §5.6.4 / TS 29.244 §7.5.4
    IMSI, RuleID, Target, ErrMsg string
    PDUSessionID, PDRID          int
    FARID                        uint32
    InstalledOK                  bool
    Timestamp                    float64
}
```

Recorder API: `RecordULCLInstall`, `ULCLForSession`,
`ClearULCLForSession`. Written from `nf/smf/session/ulcl.go`,
read by `/api/mec/active-sessions`.

### B.4 Wire-encoding decisions

#### B.4.1 EAS registry / discovery — JSON shape today

The HTTP surface (`/api/eas/*`) uses plain JSON shaped to match the
`EAS`, `DiscoveryCriteria`, `EASDFAnswer`, `DNAIMapping`, `DNSEntry`
Go types. There is no ASN.1 / OpenAPI envelope.

Decision: **TS 29.558 EDGEAPP wire APIs are deferred**. The local
PDF set does not include TS 29.558; rather than guess at the field
names + nested envelopes, the local stack ships a flat JSON shape
that mirrors the TS 23.558 §8 information flows. A future deployment
can add a TS 29.558-conformant translator without touching the data
path.

#### B.4.2 EASDF lookup — FQDN table, not real DNS

`ResolveDNS` is a SQL JOIN against `eas_dns_entries × eas_registry`,
not a UDP/53 listener. Reasons: the local EASDF clients (OAM panel,
tester) speak HTTP REST; a real EASDF would import a DNS server lib
(out of scope); the data path (FQDN → EAS row + DNAI hint) is the
same either way.

#### B.4.3 AF-influence — operator-author + SMF consultation + ULCL install

| Surface | What it does | Status |
|---------|--------------|--------|
| `POST /api/mec/af-influence` | Operator authors a rule. | Wired. |
| `GET /api/mec/af-influences` | Read view. | Wired. |
| `DELETE /api/mec/af-influence/{rule_id}` | Operator removes a rule. | Wired. |
| `mec.RulesForDNN(dnn)` | SMF reads matching rules during establish. | Wired. |
| SMF establish path | Calls `installULCLForSession` (`nf/smf/session/ulcl.go`). | Wired. |
| PFCP Session Modify (Create-PDR + Create-FAR) | SMF→UPF install. | Wired (control plane). |
| `mec.RecordULCLInstall` | Verdict per `(IMSI, pduSess, ruleID)`. | Wired. |
| UPF dataplane uplink-fork at Local PSA | Actual packet redirection. | **Roadmap.** |
| AF→NEF push of rules | Remote AF authors via NEF. | **Roadmap.** |

#### B.4.4 What is *not* wired

| Spec § | Wire | Status |
|--------|------|--------|
| TS 29.558 EDGEAPP APIs (`Eees_EASRegistration_*`, `Ecs_EESConfigInfoRetrieval`, `Eees_EASDiscovery`) | HTTP/JSON per stage-3 | Not implemented; flat JSON instead. |
| TS 23.502 §4.3.6.2 `Nnef_TrafficInfluence_Create` | NEF→PCF→SMF | Not implemented; rule store is operator-local. |
| TS 23.548 §6.2.3.4 AF EAS Deployment Information in UDR | Nudr_DataRepository | Not implemented; discovery uses local registry. |
| TS 23.501 §5.6.4 dataplane uplink fork at Local PSA | UPF C dataplane | Control install lands; uplink re-anchor is roadmap. |

### B.5 SMF AF-influence + ULCL install hook

`nf/smf/session/establish.go` after the success branch:

```go
if rules := mec.RulesForDNN(in.DNN); len(rules) > 0 {
    log.WithIMSI(in.IMSI).Infof(
        "AF-influence applies to PDU session id=%d dnn=%s — %d rule(s) matched",
        in.PDUSessionID, in.DNN, len(rules))
    installULCLForSession(in.IMSI, uint8(in.PDUSessionID), in.DNN, rules)
}
```

`installULCLForSession` (`nf/smf/session/ulcl.go`) for each matching
rule:

1. Allocates a `(PDR, FAR)` id pair in the reserved
   `0xE000` / `0xE000_0000` ID space (collision-free with the
   Establish path's allocations; visible on PCAP).
2. Builds Create-PDR (matching DL traffic to `target_ip[:port]`)
   and Create-FAR (forwarding to the local DN attach point).
3. Sends a PFCP Session Modification Request (TS 29.244 §7.5.4) to
   the UPF.
4. Calls `mec.RecordULCLInstall(...)` with `installed_ok = true/false`
   plus the error string.

The OAM panel renders the verdict per session via
`GET /api/mec/active-sessions`.

### B.6 Code map

```
edge/eas/                              ←  the EAS registry / discovery surface
├── eas.go                             ─  CRUD, discovery scoring, DNAI map,
│                                          discovery log, EASDF answer builder
└── dns.go                             ─  EASDF FQDN table:
                                          RegisterDNSEntry / ListDNSEntries /
                                          DeleteDNSEntry / ResolveDNS

edge/mec/                              ←  in-memory MEC orchestrator
└── mec.go                             ─  Site / App / AppInstance /
                                          TrafficRule / ULCLState +
                                          lookup helpers (RulesForDNN,
                                          RulesForApp, FindSiteByTAI,
                                          FindAppByFQDN, FindNearestInstance,
                                          RecordULCLInstall, ULCLForSession,
                                          GetStats)

db/schemas/domains.go ::EasDDL         ─  4 EAS tables + indexes

webservice/app/routes_edge.go          ─  /api/eas/* routes (CRUD, discover,
                                          dnai, dns, dns/resolve,
                                          discovery-log, status)
webservice/app/routes_mec.go           ─  /api/mec/* routes (sites, apps,
                                          deploy, undeploy, ulcl-rules,
                                          af-influence, af-influences,
                                          lookup, active-sessions, status)

nf/smf/session/establish.go            ─  AF-influence consultation + ULCL
                                          install trigger
nf/smf/session/ulcl.go                 ─  Create-PDR / Create-FAR build +
                                          PFCP Session Modify dispatch
nf/upf/                                ─  PFCP IE consumer (dataplane)
```

### B.7 Read paths the OAM panel relies on

| Endpoint | Backing query / call | Notes |
|----------|----------------------|-------|
| `GET /api/eas/registry` | `SELECT … FROM eas_registry ORDER BY id` | Catalog list. |
| `GET /api/eas/registry/{id}` | `SELECT … WHERE id=?` | 404 if missing. |
| `POST /api/eas/discover` | `eas.Discover(c)` — Distributed-Anchor; writes one log row. | Returns `{results, selected, count}`. |
| `GET /api/eas/dnai` | `SELECT * FROM eas_dnai_map` | DNAI catalog. |
| `GET /api/eas/dns` | `SELECT * FROM eas_dns_entries ORDER BY id DESC` | EASDF table. |
| `POST /api/eas/dns/resolve` | `eas.ResolveDNS(fqdn)` — JOIN `eas_dns_entries × eas_registry`. | Returns `ResolveAnswer{FQDN, EASID, EndpointURL, DNAI}` or 404. |
| `GET /api/eas/discovery-log` | `SELECT … FROM eas_discovery_log ORDER BY id DESC LIMIT ?` | `{entries, count}`. |
| `GET /api/eas/status` | `eas.Status()` (count + items snapshot) | Panel headline. |
| `GET /api/mec/sites` | `mec.ListSites()` | In-memory snapshot. |
| `GET /api/mec/apps` | `mec.ListApps()` | In-memory snapshot. |
| `GET /api/mec/ulcl-rules` / `GET /api/mec/af-influences` | `mec.ListTrafficRules()` | Two views of the same map. |
| `GET /api/mec/lookup?fqdn=&tai=&app_id=` | `FindAppByFQDN` + `FindSiteByTAI` + `FindNearestInstance` | EASDF helper. |
| `GET /api/mec/active-sessions` | `session.Default.All()` × `mec.RulesForDNN(s.DNN)` × `mec.ULCLForSession(...)` | "Who's getting steered + did the install land" view. |
| `GET /api/mec/status` | `mec.GetStats()` | Counter dashboard. |

### B.8 Write paths

| Operation | Tables / state | Side effects |
|-----------|----------------|--------------|
| `eas.CreateEAS` | INSERT `eas_registry` | None. |
| `eas.UpdateEAS` | UPDATE `eas_registry` (whitelisted fields) | Bumps `updated_at`. |
| `eas.DeleteEAS` | DELETE `eas_registry` | CASCADE deletes `eas_dns_entries` rows pointing at this EAS. |
| `eas.CreateDNAI` / `DeleteDNAI` | INSERT/DELETE `eas_dnai_map` | None. |
| `eas.RegisterDNSEntry` | INSERT-or-UPDATE `eas_dns_entries` (UNIQUE on `fqdn`) | None. |
| `eas.DeleteDNSEntry` / `DeleteDNSEntryByFQDN` | DELETE `eas_dns_entries` | None. |
| `eas.Discover` / `DiscoverViaEASDF` | (read) + INSERT `eas_discovery_log` | One log row per call. |
| `mec.AddSite` / `RemoveSite` | mutates `sites` map | None. |
| `mec.AddApp` / `RemoveApp` | mutates `apps` map | None. |
| `mec.DeployInstance` / `UndeployInstance` | mutates `app.Instances[siteID]` | None. |
| `mec.AddTrafficRule` / `DeleteTrafficRule` | mutates `trafficRules` map | None. |
| `mec.RecordULCLInstall` | mutates `ulclState` map | Per-rule install verdict. |
| `mec.ClearULCLForSession` | drops `ulclState` rows for the session | Called on session release. |

The only persistent CASCADE is `eas_dns_entries` on
`eas_registry.id` deletion. MEC orchestrator state is in-memory
only.

### B.9 Retention and growth

- `eas_registry` — operator-controlled count; small.
- `eas_dnai_map` — small (one row per DNAI).
- `eas_dns_entries` — bounded by published FQDNs; small.
- `eas_discovery_log` — grows with discovery activity; **unbounded
  by application code today**; operator should snapshot + trim
  periodically.
- MEC orchestrator state (sites, apps, deployments, rules, ULCL
  state) — all in-memory; vanishes on restart.

### B.10 Where the spec deferrals show up

| File / surface | Spec target | Surface deferred |
|----------------|-------------|------------------|
| `edge/eas/eas.go:24-31` (header TODOs) | TS 23.558 §8.4.3.4 `Eees_EASRegistration_*` | The wire APIs themselves; local stack persists outcomes only. |
| `edge/eas/eas.go:327` | TS 23.558 §8.2.4 EAS Profile | Dedicated FQDN field (today: substring-match against `endpoint_url`). |
| `edge/eas/eas.go:330` | TS 23.548 §6.2.3.4 | AF-provided EAS Deployment Information in UDR (`Nudr_DataRepository`). |
| `edge/eas/eas.go:408` | TS 23.548 §6.8.2 | Bidirectional N6/N9 routing translation (DNAI → UPF instance + N6 routing-info). |
| `edge/mec/mec.go` (file-level role notes) | TS 23.502 §4.3.6.2 + TS 29.522 `Nnef_TrafficInfluence_Create` | AF→NEF push of traffic-influence rules. |
| `nf/smf/session/ulcl.go` header | TS 23.501 §5.6.4 | UPF dataplane uplink-fork at a Local PSA (control install lands). |

When the TS 29.558 / TS 29.522 PDFs land in the local spec set, each
entry above becomes a concrete implementation ticket.

---
*Last refreshed against commit `13a181d`.*

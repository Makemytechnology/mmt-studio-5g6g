# MEC — Design Document

Multi-access Edge Computing orchestrator: in-memory registry of edge
sites, edge applications, deployed app instances, AF traffic-routing
influence rules, and the **ULCL/BP** install state the SMF emits
when it programmes those rules onto the wire.

---

## Part A — Functional view

### A.1 What MEC is, in plain terms

The customer's application — a video analyser, an AR renderer, a
real-time game server, an industrial vision pipeline — is **moved
out of a remote cloud and placed on a server next to the radio**.
Traffic from the UE no longer crosses the operator's backhaul to the
internet; it gets pulled off at the local UPF and steered to that
nearby server.

Two things have to be true for that to work:

1. The operator must know **where the local servers are** (sites),
   what apps are deployed where (instances), and which UE traffic
   should go to which of them (rules).
2. When a UE actually opens a PDU session that matches one of those
   rules, the **SMF must reprogramme the UPF** — installing an
   **Uplink Classifier (ULCL)** or **Branching Point (BP)** so the
   matched traffic forks off the default path and lands on the local
   app.

`edge/mec` owns the **registry and the install state** for both
halves. The wire programming itself runs in `nf/smf/session/ulcl.go`
and the PFCP push lands at `nf/upf/`; this package is the operator
panel's source of truth and the SMF's lookup table.

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| **Latency**: round-trip to a remote cloud is gone. | Cloud-gaming / AR / industrial-control SLAs become reachable. |
| **Backhaul cost**: traffic stays inside the metro / site. | High-bitrate streams (video, sensor floods) don't traverse paid transit. |
| **Data residency / sovereignty**: enterprise data never leaves the campus. | Sells into regulated verticals — health, defence, government, EU GDPR-strict deployments. |
| **New revenue surface**: operator can host third-party apps on its edge fabric. | Edge-as-a-Service catalogue, per-app SLAs, per-session billing. |
| **Resilience**: campus apps keep running if the WAN is cut. | Continuity for factories, hospitals, ports. |

### A.3 Customer use cases

| Use case | Why it lands at the edge |
|----------|--------------------------|
| **Cloud gaming / cloud XR** | <20 ms motion-to-photon needs the render server within the metro. |
| **Industrial vision** (defect inspection, robot guidance) | Camera bitrates × site count would crush WAN; latency tight. |
| **Connected-vehicle / V2X compute** | Local fusion of RSU + vehicle telemetry; metro-bounded. |
| **Private 5G / non-public networks** | Enterprise wants its data and its apps physically on-site. |
| **CDN at the edge** | Live streaming, sports, large-file delivery served from the metro. |
| **Smart-stadium / smart-venue apps** | Per-event capacity scaled at the venue, not the regional DC. |

### A.4 Actors and roles

```
   Operator panel             Third-party AF / EAS provider
        │                              │
        │ AddSite, AddApp,             │ Eees_EASRegistration
        │ AddTrafficRule,              │ (handled by edge/eas)
        │ DeployInstance               │
        ▼                              ▼
   ┌───────────────────────────────────────────────────────┐
   │                  edge/mec  (this package)              │
   │   sites · apps · instances · trafficRules · ULCL state │
   └───────────────────────────────────────────────────────┘
        ▲                       ▲                    ▲
        │ FindSiteByTAI         │ FindAppByFQDN      │ ULCLForSession
        │ ListTrafficRules      │ (DNS-side hint)    │ (panel readout)
        │ RecordULCLInstall     │                    │
        │                       │                    │
   ┌────┴────┐             ┌────┴─────┐         ┌────┴────────┐
   │  SMF    │             │  EASDF   │         │  OAM panel  │
   │ (PSA-   │             │  (DNS    │         │ /api/mec/   │
   │  UPF +  │             │  answer  │         │ active-     │
   │  ULCL)  │             │  shaping)│         │ sessions    │
   └────┬────┘             └──────────┘         └─────────────┘
        │
        ▼
   PFCP Modify  (TS 29.244 §7.5.4)  →  UPF dataplane
```

| Actor | What they do | Touches this package via |
|-------|--------------|--------------------------|
| **Operator** | Defines sites, deploys apps, writes traffic rules. | `AddSite`, `AddApp`, `DeployInstance`, `AddTrafficRule` |
| **Third-party AF / EAS provider** | Brings the application; registration outcome flows into `edge/eas/` (separate persistence) and pairs with an instance here. | `App` lookup |
| **EASDF** | Shapes DNS answers so the UE resolves to the **nearest** instance. | `FindAppByFQDN` (TS 23.548 §6.2.3.2.2) |
| **SMF** | At PDU-session establish, picks PSA-UPF by LADN area and installs ULCL/BP for matching AF rules. | `FindSiteByTAI`, `ListTrafficRules`, `RecordULCLInstall` |
| **UPF** | Enforces the installed PDR + FAR. | (downstream consumer; no direct call here) |
| **OAM panel** | Renders site / app / rule state and per-session ULCL install verdicts. | All read APIs + `ULCLForSession` |

### A.5 Operator workflow

```
   1.  Provision the edge site         AddSite(name, [TAIs], localDNIP, localDNCIDR, capacity)
                                       — TS 23.501 §5.6.5 LADN service area
   2.  Register the application        AddApp(name, fqdn, dnn, ipFilter, port, protocol)
                                       — addressable via FQDN (EASDF lookup key)
   3.  Deploy the instance             DeployInstance(appID, siteID, appIP, appPort)
                                       — TS 23.558 §8.12 dynamic EAS instantiation outcome
   4.  Pair with EAS registry          edge/eas.CreateEAS(...)   (separate package)
                                       — TS 23.558 §8.4.3 EES registration
   5.  Define the traffic-routing rule AddTrafficRule(appID, siteID, dnn,
                                                     targetIP, targetFQDN,
                                                     targetPort, priority)
                                       — TS 23.502 §4.3.6 AF influence
   6.  UE attaches → PDU establish     SMF resolves FindSiteByTAI(uePLMN+TAC)
                                            and ListTrafficRules() filtered by DNN
   7.  ULCL/BP install fires           SMF emits PFCP Modify (Create-PDR + Create-FAR)
                                            into the UPF (TS 23.501 §5.6.4, TS 29.244 §7.5.4)
   8.  Install state records           RecordULCLInstall(IMSI, pduSessID, ruleID,
                                                         pdr, far, target, ok, err)
   9.  Operator audits                 GET /api/mec/active-sessions
                                            → shows ulcl_state per session
```

### A.6 ULCL — what it is, and why it lives here

**ULCL = Uplink Classifier** (TS 23.501 §5.6.4). Sibling concept:
**Branching Point (BP)** for IPv6 multi-homing. Same role: a UE PDU
session picks up an extra packet-detection rule that **forks one
flow off the default path** and points it at a local DN attach point
on the same UPF — so traffic to a specific destination gets local
breakout while everything else still flows through the central PSA.

The decision to install a ULCL is **driven by the AF-influence rules
held in this package**: every rule tuple `(AppID, SiteID, DNN,
TargetIP[:Port])` becomes a candidate ULCL the SMF tries to push
when a matching PDU session establishes.

So MEC owns three things end-to-end for this story:

1. **The intent** — `TrafficRule` rows: "send anything bound for this
   target, on this DNN, to that local instance".
2. **The address book** — `Site` rows describe the local DN attach
   point (`LocalDNIP`, `LocalDNCIDR`) the FAR forwards to.
3. **The install verdict** — `ULCLState` rows record whether the
   PFCP push actually landed (per `(IMSI, PDU session, rule)`), so
   the OAM panel and the tester can tell **intent** from
   **enforcement**.

The PFCP-on-the-wire code itself lives in `nf/smf/session/ulcl.go`
(SMF-side install) and the dataplane consumer is `nf/upf/`. Neither
of those is in MEC's tree, but their **input** (TrafficRules) and
**output** (ULCLState) both pass through `edge/mec`. That's why
ULCL has no doc of its own — it would be a 100% cross-reference to
this one.

### A.7 What is NOT in scope here

| Thing | Where it lives |
|-------|----------------|
| EAS-side registration persistence | `edge/eas/` (TS 23.558 §8.4.3) |
| `Nnef_TrafficInfluence`, `Npcf_PolicyAuthorization` wire | outside this repo's scope |
| PFCP install of the ULCL on the UPF | `nf/smf/session/ulcl.go` + `nf/upf/` |
| Local PSA-UPF dataplane fork (uplink re-anchor) | UPF dataplane — next step (see ulcl.go header) |
| Persistent storage of the registry | by design — `edge/mec` is in-memory |

---

## Part B — Design

### B.1 Architecture

```
              ┌─────────────────────────────────────────────┐
              │  GUI panel  /  AF  /  EASDF stub  /  SMF    │
              └──────────────────┬──────────────────────────┘
                                 │
                                 ▼
   ┌────────────────────────────────────────────────────────────────┐
   │                    edge/mec  (sync.Mutex)                       │
   │                                                                 │
   │   sites        : map[siteID]*Site         — TS 23.501 §5.6.5   │
   │   apps         : map[appID]*App                                 │
   │     └ Instances: map[siteID]*AppInstance  — TS 23.558 §8.12    │
   │   trafficRules : map[ruleID]*TrafficRule  — TS 23.502 §4.3.6   │
   │   ulclState    : map[(imsi,pduSess,rule)]*ULCLState            │
   │                                            — TS 23.501 §5.6.4  │
   └────────────────────────────────────────────────────────────────┘
              │                            │                  │
              ▼                            ▼                  ▼
        FindSiteByTAI               FindAppByFQDN     ListTrafficRules
        (SMF picks PSA-UPF          (EASDF DNS        (SMF programmes
         per LADN area)              answer lookup)    ULCL/BP)
                                                       │
                                                       ▼
                                                 RecordULCLInstall
                                                 ULCLForSession
```

State is intentionally non-persistent (in `sync.Mutex`-guarded maps);
the package is the runtime cache the GUI panel and the test harness
share. Persistence of EAS registration outcomes lives in
`edge/eas/eas_registry`.

### B.2 Spec → field map

| Field | Spec § | Notes |
|-------|--------|-------|
| `Site.TAIs` | TS 23.501 §5.6.5 | LADN service area = set of TAIs |
| `Site.LocalDNIP` / `LocalDNCIDR` | TS 23.501 §5.6.5 | local DN attach point used as ULCL FAR target |
| `App.FQDN` | TS 23.548 §6.2.3.2.2 | EASDF DNS-Query key |
| `AppInstance` | TS 23.558 §8.12 | dynamic EAS instantiation outcome |
| `TrafficRule` | TS 23.502 §4.3.6 | AF traffic-routing influence (the ULCL "intent") |
| `ULCLState` | TS 23.501 §5.6.4; TS 29.244 §7.5.4 | per-session install verdict (PDR + FAR ids, ok/err) |

### B.3 File map

| File | Role |
|------|------|
| `edge/mec/mec.go` | Domain types + thread-safe registry + all public API + ULCL state recorder |
| `edge/mec/mec_test.go` | CRUD + lookup tests |

### B.4 Wire / API surface

This package does not speak any spec wire format. It is a Go-only
in-memory orchestrator state. The §-cites in source are pointers to
the spec procedures whose **outcome** is persisted here; the
authoritative wire APIs live elsewhere:

- `Nnef_TrafficInfluence`, `Npcf_PolicyAuthorization` — outside
- `Eees_EASRegistration_*` — `edge/eas/`
- N4 PFCP for ULCL/BP install — `nf/smf/session/ulcl.go` →
  `nf/upf/` (TS 29.244 §7.5.4 PFCP Session Modification, with
  Create-PDR §7.5.4.17 and Create-FAR §7.5.4.17 IEs)

### B.5 Headline procedures

#### B.5.1 Site → PDU-session anchoring

```
SMF                                  edge/mec
 │── FindSiteByTAI(uePLMN+TAC) ─────▶│
 │                                   │ scan sites where Status="active"
 │                                   │ and Site.TAIs contains tai
 │◀── *Site (or nil) ────────────────│
 │
 │  (uses Site.LocalDNIP / LocalDNCIDR
 │   to pick PSA-UPF for this PDU session)
```

#### B.5.2 EASDF DNS-Query lookup

```
EASDF stub                            edge/mec
 │── FindAppByFQDN(uefqdn) ─────────▶│
 │                                   │ case-insensitive equality match on App.FQDN
 │◀── *App (or nil) ─────────────────│
 │
 │  (cross-references edge/eas DiscoverViaEASDF
 │   for the EAS-row half of the lookup)
```

#### B.5.3 EAS deployment

```
Operator panel / orchestrator         edge/mec
 │── DeployInstance(appID,           │
 │    siteID, appIP, appPort) ──────▶│
 │                                   │ verify app+site exist
 │                                   │ create AppInstance{Status:"running"}
 │                                   │ store in app.Instances[siteID]
 │◀── *AppInstance, err ─────────────│
```

The matching EES-side registration outcome must subsequently be
written via `edge/eas.CreateEAS` before EAS Discovery can return the
new instance.

#### B.5.4 ULCL install + readout

```
SMF (PDU establish)                   edge/mec
 │── ListTrafficRules() ─────────────▶│
 │◀── []*TrafficRule ─────────────────│
 │
 │  (filter by DNN, allocate
 │   (PDR,FAR) ids in the 0xE000 /
 │   0xE000_0000 reserved space —
 │   see nf/smf/session/ulcl.go)
 │
 │── PFCP Session Modify ─────▶ UPF
 │      Create-PDR + Create-FAR
 │
 │── RecordULCLInstall(imsi,         │
 │    pduSess, ruleID, pdr, far,     │
 │    target, ok, err) ──────────────▶│
 │                                    │ INSERT into ulclState map
 │
                                      ◀── ULCLForSession(imsi, pduSess)
                                          (OAM panel readout via
                                           /api/mec/active-sessions)
```

### B.6 Key types / public API

```go
type Site struct {
    SiteID, Name      string
    TAIs              []string // TS 23.501 §5.6.5
    LocalDNIP, LocalDNCIDR string
    Capacity          int
    Status            string  // active | maintenance | offline
    CreatedAt         float64
}

type App struct {
    AppID, Name, FQDN, DNN, IPFilter, Protocol string
    Port, Priority                              int
    Instances map[string]*AppInstance
    CreatedAt float64
}

type AppInstance struct {
    SiteID, AppIP, Status string
    AppPort, ActiveSessions int
    DeployedAt float64
}

type TrafficRule struct {
    RuleID, AppID, SiteID, DNN, TargetIP, TargetFQDN string
    TargetPort, Priority int
    CreatedAt float64
}

type ULCLState struct {  // TS 23.501 §5.6.4 install verdict
    IMSI, RuleID, Target, ErrMsg string
    PDUSessionID, PDRID          int
    FARID                        uint32
    InstalledOK                  bool
    Timestamp                    float64
}

// Sites
func AddSite(name string, tais []string, localDNIP, localDNCIDR string, capacity int) *Site
func ListSites() []*Site
func FindSiteByTAI(tai string) *Site                // mec.go:179

// Apps
func AddApp(name, fqdn, dnn, ipFilter string, port int, protocol string) *App
func FindAppByFQDN(fqdn string) *App                // mec.go:321

// Deployments
func DeployInstance(appID, siteID, appIP string, appPort int) (*AppInstance, error)  // mec.go:259
func UndeployInstance(appID, siteID string) bool
func FindNearestInstance(appID, tai string) *AppInstance

// Traffic rules (ULCL "intent")
func AddTrafficRule(appID, siteID, dnn, targetIP, targetFQDN string, targetPort, priority int) *TrafficRule
func ListTrafficRules() []*TrafficRule
func DeleteTrafficRule(ruleID string) bool

// ULCL state (install "verdict")        // mec.go:417 onward
func RecordULCLInstall(imsi string, pduSessionID int, ruleID string,
    pdrID int, farID uint32, target string, ok bool, err string)
func ULCLForSession(imsi string, pduSessionID int) []*ULCLState
func ClearULCLForSession(imsi string, pduSessionID int)

// Stats
func GetStats() map[string]any
```

### B.7 Stubs / TODOs from grep

No `TODO` comments in `edge/mec/mec.go`. The package is a bounded
in-memory store; "missing" wire APIs (NEF, PCF, PFCP) live in
sibling packages. The dataplane uplink-fork ("real ULCL re-anchor at
a Local PSA") is tracked in `nf/smf/session/ulcl.go` header, not
here.

### B.8 References

Only specs cited in source:

- **TS 23.501** — System Architecture for the 5G System
  - §5.6.4 Uplink Classifier (ULCL) and Branching Point
  - §5.6.5 Local Area Data Network (LADN) — service area = TAI set
- **TS 23.502** — Procedures for the 5G System
  - §4.3.6 Application Function influence on traffic routing
- **TS 23.548** — 5G System Enhancements for Edge Computing
  - §5.1 EASDF function
  - §6.2.3.2.2 EAS Discovery Procedure with EASDF
- **TS 23.558** — Architecture for enabling Edge Applications
  - §8.4.3 EAS registration
  - §8.12 Dynamic EAS instantiation triggering
- **TS 29.244** — PFCP (Packet Forwarding Control Protocol)
  - §7.5.4 Session Modification Request (ULCL install carrier)
  - §7.5.4.17 Create PDR / Create FAR IEs

Cross-link: `edge/eas/` is the persisted EES-side registry that the
deployments tracked here flow into. `nf/smf/session/ulcl.go` is the
SMF-side install path that consumes `ListTrafficRules` and writes
back via `RecordULCLInstall`. `nf/upf/` is the dataplane that
enforces the Create-PDR / Create-FAR IEs.

---
*Last refreshed against commit `13a181d`.*

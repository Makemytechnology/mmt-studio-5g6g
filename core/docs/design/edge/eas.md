# EAS — Design Document

Edge Application Server registry, EES-side registration persistence,
and the two EAS Discovery procedures (Distributed-Anchor and
Session-Breakout-via-EASDF) that the SMF / AF / EASDF stack drives.

---

## Part A — Functional view

### A.1 What the EAS surface is, in plain terms

When an operator runs MEC, the **applications** that customers want
to reach at the edge — game servers, AR renderers, video analysers,
factory controllers — have to be **catalogued, located, and
discovered**. A UE asks "where do I go to talk to game-server.foo?";
the network has to answer with the right one out of many, in the
right place.

`edge/eas` is the **catalogue and the discovery engine**:

- It persists every Edge Application Server instance the operator
  has registered (FQDN, DNAI, capacity, supported DNNs / slices,
  geo-coords, status).
- It answers two distinct **discovery questions** — one for AFs
  asking "give me a ranked list" (Distributed-Anchor), one for
  EASDF resolving a UE DNS Query (Session-Breakout) — using the
  same scoring core but different proximity policy.
- It persists the **DNAI map** (DNAI string → operator metadata
  describing the local DN / UPF instance) and an **audit log** of
  every discovery call so the operator can reconstruct "why did
  this UE get sent to that EAS?".

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| **Single source of truth** for what's deployed where | One place the GUI panel, the SMF, and the discovery engine all read from. No drift across config files. |
| **Two discovery models in one engine** | Distributed-Anchor (AF asks; UE-proximity-aware) and Session-Breakout (UE DNS asks; SMF picks L-PSA via DNAI map). Same scoring core, one less moving part. |
| **Audit trail for every pick** | "Who selected what, when, with which criteria, against how many candidates." Useful for billing disputes, capacity tuning, regulatory evidence. |
| **DNAI map is the wire-side glue** | The DNAI string the SMF feeds the §6.8 mapper to insert ULCL/BP at the right edge site comes out of one query here. |
| **EES wire APIs are deferrable** | Persisting outcomes means a future TS 29.558 translator drops in without re-architecting. |

### A.3 Customer use cases (what the catalogue enables)

| Use case | What this package contributes |
|----------|-------------------------------|
| **Cloud gaming on private 5G** | Game-server EAS rows per venue; AF queries Discovery, gets the closest one with capacity. |
| **Stadium / venue CDN** | CDN node EAS rows; UEs DNS-query the FQDN, EASDF gets the right node. |
| **Factory controllers** | Per-cell EAS rows mapped to the cell's DNAI; SMF inserts the local-PSA on PDU establish. |
| **Health / regulated** | EAS rows constrained to slices/DNNs the regulator approved; discovery filter enforces the constraint. |
| **OAM "where would this UE go?"** | Run `Discover()` with the UE's criteria, no commit, read the ranking — pure planning surface. |

### A.4 Actors and roles

```
   Operator panel        AF (today: operator    EASDF stub             SMF
        │                proxy; future:         (UE DNS Query)         (PDU establish)
        │                Nnef_TrafficInfluence)
        │                       │                     │                     │
        │ CRUD                  │ Discover            │ DiscoverViaEASDF    │ ResolveDNAIForFQDN
        ▼                       ▼                     ▼                     ▼
   ┌────────────────────────────────────────────────────────────────────────┐
   │                        edge/eas (this package)                         │
   │   eas_registry · eas_dnai_map · eas_dns_entries · eas_discovery_log    │
   └────────────────────────────────────────────────────────────────────────┘
                                   ▲
                                   │ FQDN/TAI/AppID lookup helpers
                                   │
                            ┌──────┴───────┐
                            │   edge/mec   │
                            │  (paired —   │
                            │  TrafficRule │
                            │  intent +    │
                            │  ULCL state) │
                            └──────────────┘
```

| Actor | Role | Touches this package via |
|-------|------|--------------------------|
| **Operator** | Curates the catalogue: registers EASs, maps DNAIs, publishes FQDNs. | `CreateEAS`, `UpdateEAS`, `DeleteEAS`, `CreateDNAI`, `RegisterDNSEntry` |
| **AF / Distributed-Anchor caller** | Asks "rank candidates for this app, optionally near this UE". | `Discover` |
| **EASDF (DNS-Query path)** | Asks "what EAS answers for this FQDN?" — proximity excluded. | `DiscoverViaEASDF`, `ResolveDNS` |
| **SMF** | Needs the DNAI to feed the §6.8 map and pick a local PSA. | `MapEASToDNAI`, `ResolveDNAIForFQDN` |
| **OAM panel** | Reads the catalogue, the discovery audit log, and the DNAI map. | All read APIs + `ListDiscoveryLog` |

### A.5 Operator workflow

```
   1.  Register EAS instance         CreateEAS(appID, endpointURL,
                                               dnai, lat, lon,
                                               dnns, slices, capacity)
                                     — TS 23.558 §8.4.3.2 (Eees_EASRegistration)
   2.  Map the DNAI                  CreateDNAI(dnai, description,
                                                locationHint, upfInstance)
                                     — TS 23.548 §6.8 EAS↔DNAI map
   3.  (optional) Publish FQDN       RegisterDNSEntry(fqdn, easID)
                                     — UE DNS Query target → EAS row
   4.  Application asks              Discover(criteria) — Distributed-Anchor
                                     OR
                                     UE issues DNS Query for fqdn →
                                     EASDF calls DiscoverViaEASDF(fqdn, criteria)
   5.  SMF threads the DNAI          ResolveDNAIForFQDN(fqdn, criteria)
                                     into the §6.8 mapper for L-PSA placement
   6.  Audit                         ListDiscoveryLog(limit) — every call recorded
   7.  Lifecycle                     UpdateEAS / DeleteEAS as the deployment evolves
                                     (FK CASCADE drops dependent eas_dns_entries rows)
```

### A.6 The two discovery models — when each fires

| Model | Spec § | When | Proximity term | Picks |
|-------|--------|------|---------------|-------|
| **Distributed-Anchor** | TS 23.548 §6.2.2.2 | An AF (or OAM tool) holds the criteria and wants a ranked candidate list. | **on** — haversine UE↔EAS, `30·exp(-d/30km)` | Top of the ranking |
| **Session-Breakout via EASDF** | TS 23.548 §6.2.3.2.2 | UE issues a DNS Query for an edge-app FQDN. | **off** — under this model the SMF picks the L-PSA via the §6.8 DNAI map, not UE topology | A single EAS row + DNAI |

The scoring core is the same function; the dispatch decides whether
the proximity term contributes. Weights (DNAI +50, DNN +30,
S-NSSAI +20, capacity 0..20, proximity 0..30) are **operator
policy**, not spec-derived.

### A.7 What is NOT in scope here

| Thing | Where it lives |
|-------|----------------|
| `Eees_EASRegistration_*` wire (TS 23.558 §8.4.3.4) | deferred — local stack persists outcomes only |
| AF-provided EAS Deployment Information stored in UDR (TS 23.548 §6.2.3.4) | deferred — discovery uses the local registry |
| Bidirectional DNAI ↔ N6 / N9 routing translation (TS 23.548 §6.8.2) | `nf/smf/` + `nf/upf/`; this package only surfaces the EAS-side half |
| The actual DNS server | EASDF stub speaks `ResolveDNS` over HTTP today, not UDP/53 |
| Sites, traffic rules, ULCL state | `edge/mec/` (paired registry) |
| Application bytes flowing to the EAS | UPF dataplane |

---

## Part B — Design

### B.1 Architecture

```
                       ┌─────────────────────────────────────┐
   AF / Operator ───▶  │  CreateEAS / UpdateEAS / DeleteEAS  │
   Panel               │  (TS 23.558 §8.4.3.2)               │
                       └──────────────┬──────────────────────┘
                                      │  INSERT/UPDATE/DELETE
                                      ▼
                         ┌──────────────────────────┐
                         │     eas_registry         │
                         │  (id, app_id, dnai,      │
                         │   endpoint_url, lat/lon, │
                         │   slices/dnns, capacity) │
                         └──────────┬───────────────┘
                                    │
              ┌─────────────────────┼─────────────────────────┐
              │                     │                         │
              ▼                     ▼                         ▼
    ┌──────────────────┐  ┌──────────────────┐   ┌────────────────────┐
    │  Discover()      │  │ DiscoverViaEASDF │   │   MapEASToDNAI /   │
    │  TS 23.548       │  │ TS 23.548        │   │   ResolveDNAIForFQDN│
    │  §6.2.2.2        │  │ §6.2.3.2.2       │   │   TS 23.548 §6.8    │
    │  (Distributed-   │  │ (Session-        │   └────────────────────┘
    │   Anchor)        │  │  Breakout)       │
    │  scoreEAS w/     │  │  scoreEAS w/o    │
    │  proximity       │  │  proximity term  │
    └──────────────────┘  └──────────────────┘
              │                     │
              ▼                     ▼
        eas_discovery_log (audit row per Discover call)
```

### B.2 Connectivity-model split

| Spec model | TS 23.548 § | Helper | Proximity term |
|------------|-------------|--------|----------------|
| Distributed-Anchor | §6.2.2.2 | `Discover` | **on** (haversine vs UE lat/lon) |
| Session-Breakout (via EASDF DNS) | §6.2.3.2.2 | `DiscoverViaEASDF` | **off** (SMF/EASDF chooses L-PSA via §6.8) |

### B.3 File map

| File | Role |
|------|------|
| `edge/eas/eas.go` | All public API, scoring, DB access |
| `edge/eas/dns.go` | EASDF FQDN table (`RegisterDNSEntry`, `ResolveDNS`, list/delete) |
| `edge/eas/eas_test.go` | CRUD + scoring + EASDF/DNAI tests |

### B.4 Wire / API surface

Spec wire-formats are **not** implemented here. The package speaks
only Go and SQL. Spec context for each function it persists:

| Public function | Spec backstop |
|-----------------|---------------|
| `CreateEAS` | TS 23.558 §8.4.3.2.2 / §8.4.3.4.2 (`Eees_EASRegistration_Request`) |
| `UpdateEAS` | TS 23.558 §8.4.3.2.3 / §8.4.3.4.3 (`Eees_EASRegistration_Update`) |
| `DeleteEAS` | TS 23.558 §8.4.3.2.4 / §8.4.3.4.4 (`Eees_EASRegistration_Deregister`) |
| `Discover` | TS 23.548 §6.2.2.2 (Distributed-Anchor EAS Discovery) |
| `DiscoverViaEASDF` | TS 23.548 §6.2.3.2.2 (EAS Discovery with EASDF) |
| `MapEASToDNAI`, `ResolveDNAIForFQDN` | TS 23.548 §6.8 EAS↔DNAI mapping |
| `RegisterDNSEntry` / `ResolveDNS` | EASDF FQDN table (operator-local helper) |

### B.5 Headline procedures

#### B.5.1 Distributed-Anchor Discovery (`Discover`)

```
caller (AF / SMF)              eas pkg
   │                             │
   │── DiscoveryCriteria ───────▶│
   │   {AppID, DNN, SST, DNAI,   │
   │    UELat, UELon}            │
   │                             │ SELECT … FROM eas_registry
   │                             │ WHERE app_id=? AND status='active'
   │                             │
   │                             │ for each candidate:
   │                             │   computeDistance (haversine)
   │                             │   scoreEAS — DNAI +50, DNN +30,
   │                             │              SST +20, capacity ×20,
   │                             │              proximity 30·exp(-d/30)
   │                             │
   │                             │ insertion-sort by score desc
   │                             │ INSERT INTO eas_discovery_log
   │                             │
   │◀── []EAS (ranked) ──────────│
```

#### B.5.2 EASDF Discovery (`DiscoverViaEASDF`)

```
SMF / EASDF stub (UE DNS Query)         eas pkg
   │                                       │
   │── (fqdn, AppID-narrow hint) ─────────▶│
   │                                       │ SELECT * FROM eas_registry WHERE status='active'
   │                                       │
   │                                       │ filter: substring-match endpoint_url ⊇ fqdn
   │                                       │ fallback: app_id == criteria.AppID
   │                                       │
   │                                       │ scoreEAS WITHOUT proximity term
   │                                       │ pick top, attach EAS row + DNAI
   │                                       │ INSERT INTO eas_discovery_log
   │                                       │
   │◀── EASDFAnswer{FQDN, EAS, DNAI} ──────│
```

The DNAI in the answer is what the SMF feeds the §6.8 map to insert
the ULCL/BP/Local-PSA at the right edge site.

### B.6 Key types / public API

```go
type EAS struct {
    ID, AppID, EndpointURL, Status, CreatedAt, UpdatedAt
    Name, DNAI, SupportedDNNs, SupportedSlices *string
    Latitude, Longitude *float64
    Capacity, ActiveConnections int
    DistanceKM, Score *float64  // computed in Discover only
}

type DNAIMapping struct { ID; DNAI; Description, LocationHint, UPFInstance *string }

type DiscoveryCriteria struct {
    IMSI, AppID, DNN, DNAI string
    SST                    *int
    UELatitude, UELongitude *float64
}

type EASDFAnswer struct { FQDN string; EAS EAS; DNAI, UEIPHint string }

// CRUD
func CreateEAS(...) (int64, error)             // eas.go:128
func UpdateEAS(id int64, fields map[string]any) error
func DeleteEAS(id int64) error
func ListEAS() ([]EAS, error)
func GetEAS(id int64) (*EAS, error)

// DNAI map (TS 23.548 §6.8)
func ListDNAI() ([]DNAIMapping, error)
func CreateDNAI(dnai string, description, locationHint, upfInstance *string) (int64, error)
func DeleteDNAI(id int64) error

// Discovery
func Discover(c DiscoveryCriteria) ([]EAS, error)                          // eas.go:253
func DiscoverViaEASDF(fqdn string, c DiscoveryCriteria) (*EASDFAnswer, error)  // eas.go:332
func MapEASToDNAI(easID int64) (string, error)                             // eas.go:411
func ResolveDNAIForFQDN(fqdn string, c DiscoveryCriteria) (string, error)  // eas.go:435
func ListDiscoveryLog(limit int) ([]DiscoveryLog, error)
```

`scoreEAS` (`eas.go:493`) is the operator-policy ranking; weights are
deliberately not spec-derived.

### B.7 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| `eas.go:327` | `TODO TS 23.558 §8.2.4` — split a dedicated FQDN field out of `endpoint_url` so EASDF lookup is exact rather than substring. |
| `eas.go:330` | `TODO TS 23.548 §6.2.3.4` — wire to AF-provided EAS Deployment Information stored in UDR (`Nudr_DataRepository`); today the candidate set is the local registry only. |
| `eas.go:408` | `TODO TS 23.548 §6.8.2` — bidirectional N6/N9 routing translation (DNAI → UPF instance + N6 routing-info) is delegated to the SMF/SMSF stack; this helper just surfaces the EAS-side half. |

### B.8 References

Only specs cited in source:

- **TS 23.548** — 5G System Enhancements for Edge Computing
  - §6.2 EAS Discovery / Re-discovery
  - §6.2.2.2 Distributed-Anchor EAS Discovery
  - §6.2.3.2.2 EAS Discovery Procedure with EASDF
  - §6.2.3.4 EAS Deployment Information (AF-provided)
  - §6.8 Mapping between EAS address Information and DNAI
- **TS 23.558** — Architecture for enabling Edge Applications (EDGEAPP)
  - §8.2.4 EAS Profile (FQDN field referenced by TODO)
  - §8.4.3.2 EAS registration / update / de-registration information flows
  - §8.4.3.3 information-flow steps
  - §8.4.3.4 `Eees_EASRegistration_*` operations
- **RFC 7871** — EDNS Client Subnet (referenced by `EASDFAnswer.UEIPHint`)

Cross-link: `edge/mec/` carries the LADN-anchored Site/App registry
the AF feeds **into** the EAS pool here; both `FindAppByFQDN`
(`mec.go:321`) and `DiscoverViaEASDF` (`eas.go:332`) implement two
halves of the same TS 23.548 §6.2.3.2.2 lookup.

---
*Last refreshed against commit `13a181d`.*

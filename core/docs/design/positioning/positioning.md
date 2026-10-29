# Positioning — Design Document

The umbrella view of network-side positioning in MMT Studio Core:
LCS exposure (GMLC), positioning compute (LMF), gNB / antenna /
PRS provisioning, LCS privacy gating, geofencing, and the
operator-facing REST surface that ties them together.

> Spec anchors:
> - **TS 22.071** — Location Services (Stage 1)
> - **TS 23.273** — 5G System Location Services (Stage 2)
> - **TS 23.271** §9 — LCS privacy
> - **TS 29.515** — Ngmlc Service (deferred)
> - **TS 29.572** — Nlmf Service (Stage 3)
> - **TS 37.355** — LTE Positioning Protocol (LPP)
> - **TS 38.211 §7.4.1.7** — Positioning Reference Signal (PRS)
> - **TS 38.305** — NG-RAN positioning protocols (procedures + methods)
> - **TS 38.455** — NRPPa (NR Positioning Protocol A)

Per-NF deep-dives: [`../nf/lmf.md`](../nf/lmf.md) ·
[`../nf/gmlc.md`](../nf/gmlc.md). For UE-to-UE sidelink ranging
see the sibling doc [`ranging.md`](./ranging.md) (TS 23.586) — a
separate architecture, not covered here.

---

## Part A — Functional view

### A.1 What positioning is, in plain terms

A subscriber, an emergency caller, a fleet vehicle, a regulated
device — they are all on the network, and **someone with the right
authorisation needs to know where they are**, accurately, and on
demand. The 5G core's positioning surface is what answers that
question.

It is **network-side** positioning: the network computes the
location of a UE using radio measurements (gNB-side via NRPPa, or
UE-side via LPP), or accepts a UE-reported GNSS fix, gates the
result by privacy and authorisation policy, and returns
`{lat, lon, altitude, uncertainty, confidence}` to the requester.

Two distinct services:

| Service | Spec | What it does |
|---------|------|--------------|
| **LCS via GMLC** | TS 23.273 §4.3.3 | Outside-facing gateway. An LCS Client (commercial app, emergency, lawful intercept, value-added) asks the GMLC for a UE's location. |
| **Determine-Location via LMF** | TS 23.273 §4.3.8, TS 29.572 §5.2 | The compute engine. The LMF picks a method, drives the measurement collection, runs the math, returns a result. |

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| **Regulatory obligation** | Emergency-services positioning (E911 / 112) is a licence condition in most jurisdictions. Indoor accuracy targets keep tightening. |
| **Lawful Intercept** | Positioning is part of the LI handover; the operator cannot ship without it. |
| **Commercial revenue** | Sell location to navigation apps, fleet management, asset tracking, retail analytics, ad-tech. |
| **Public safety** | First-responder location, missing-person assists, geofenced incident response. |
| **Better than GNSS** | Indoors / urban-canyon / dense-foliage where GNSS dies, network-side multi-RTT / TDOA / AoA still works. |
| **No client install** | The UE only has to be a UE — no app needed for the basic E-CID / multi-RTT cases. |
| **Differentiated SLA** | Sub-3 m sells; sub-10 m sells; "best effort" sells differently. |

### A.3 Customer use cases

| Use case | What positioning provides |
|----------|---------------------------|
| **Emergency services (E911 / 112)** | Caller location to PSAP, with a confidence number, within sub-50 m indoors. |
| **Lawful Intercept** | Subject location feed handed to the authorised consumer per regulation. |
| **Fleet / asset tracking** | Periodic location updates per IMSI, optionally batched in the history view. |
| **Geofencing** | "Tell me when this UE crosses into / out of this polygon." Used for compliance, parental controls, fleet ops. |
| **Indoor navigation** | Multi-RTT / DL-AoD with sub-10 m accuracy where GNSS is unavailable. |
| **Network-assisted GNSS (A-GNSS)** | Operator delivers ephemeris / assistance to the UE, accepts the UE's resulting fix. |
| **Roadside-units / V2X precision** | Network-side cross-check of UE-reported V2X position. |
| **Ad-tech / retail analytics** | Aggregated, consented, low-frequency location for footfall analysis. |

### A.4 Actors and roles

```
   LCS Client (Le)                                 OAM / Operator
   commercial / emergency /                        - register gNB position
   lawful_intercept / value_added                  - register antenna info
        │                                          - allocate PRS resource
        │ /api/location/request                    - manage geofences
        │ /api/location/{id}                       - manage LCS privacy
        │ /api/location/history                    │
        ▼                                          ▼
   ┌──────────────────────────────────────────────────────────┐
   │           webservice/app/routes_positioning.go            │
   │   (the operator-facing REST surface — this design doc)    │
   └─────────────────┬─────────────────────────────────┬──────┘
                     │                                 │
                     ▼                                 ▼
              ┌────────────┐                   ┌──────────────┐
              │  nf/gmlc   │                   │   nf/lmf     │
              │  (LCS      │  in-process       │  (compute    │
              │   gateway, │  Go call          │   engine,    │
              │   QoS      │ ────────────────▶ │   methods,   │
              │   shaping) │                   │   PRS,       │
              └────────────┘                   │   geofence)  │
                                               └──────┬───────┘
                                                      │
                          ┌───────────────────────────┴────────┐
                          │                                    │
                          ▼                                    ▼
                  NRPPa over AMF/N2                   LPP (UE-transparent
                  (gNB measurements                    via AMF/RAN —
                   — TS 38.455 §8.2;                    TS 37.355 §6;
                   wire deferred)                       wire deferred)

   Privacy gate (TS 23.271 §9): emergency bypasses; commercial denied
   if (IMSI, client_type='commercial') has lcs_privacy.allowed=0.
```

| Actor | Role |
|-------|------|
| **LCS Client** | Outside consumer of location: PSAP, LI consumer, commercial app, fleet platform. Talks to the GMLC over HTTP. |
| **GMLC** | Le-facing gateway. Defaults `client_type` to `commercial`, shapes `qos` (`accuracy_m`, `response_time_s`), forwards to LMF. |
| **LMF** | Picks a positioning method per QoS, drives the math, persists session + history. |
| **AMF (transparent)** | Carries NRPPa for gNB measurements + LPP for UE measurements; in this build the AMF round-trip is intra-process. |
| **gNB** | Source of NRPPa measurements (E-CID, RTT, TDOA, AoA, AoD). |
| **UE** | Source of LPP measurements (RSTD, RSRP, GNSS fix, SRS). |
| **Operator (OAM)** | Provisions gNB position + antenna, allocates PRS resources, defines geofences, manages LCS privacy. |

### A.5 Operator workflow

```
   Provisioning (one-time / per change)
   ──────────────────────────────────────
   1.  POST /api/gnb/position             register every gNB's lat/lon/alt
                                          — input to multi-RTT / TDOA / AoA math

   2.  POST /api/gnb/antenna              azimuth / beamwidth / downtilt /
                                          numBeams — input to E-CID bearing
                                          + DL-AoD / UL-AoA bearings

   3.  POST /api/prs/allocate             allocate PRS resource
                                          (TS 38.211 §7.4.1.7) — periodicity,
                                          combSize, numSymbols, numRB, …
                                          out-of-range inputs are clamped
                                          per spec sets, not rejected.

   4.  POST /api/geofences                define a polygon / circle the
                                          operator wants entry / exit alerts on

   5.  POST /api/lcs-privacy              per-(IMSI, client_type) consent
                                          rows; emergency bypasses; commercial
                                          can be denied per TS 23.271 §9.

   Live request (per LCS call)
   ──────────────────────────────
   6.  POST /api/location/request         { imsi, method, accuracy_m,
                                            response_time_s, client_type }

                                          Privacy gate runs first:
                                            client_type='emergency'  → admit
                                            (imsi, commercial) deny  → reject
                                            otherwise                 → admit

                                          GMLC builds qos, calls LMF.
                                          LMF picks method (or honours the
                                          request), runs the kernel,
                                          persists session, returns
                                          { session_id, lat, lon, alt,
                                            uncertainty_m, confidence,
                                            state, method }.

   7.  GET  /api/location/{session_id}    fetch a stored result.

   8.  GET  /api/location/history?imsi=   per-UE completed-session history.

   PRS lifecycle
   ─────────────
   9.  GET    /api/prs/{gnb_id}           list active PRS resources.
   10. DELETE /api/prs/{prs_id}           deactivate.
```

### A.6 Methods — what gets picked, when

`selectMethod` (`lmf.go:386-414`) is **operator policy**, not
spec-mandated. Default thresholds:

| QoS `accuracy_m` | Picked method | Notes |
|------------------|---------------|-------|
| ≤ 3 m, response_time ≥ 5 s | `multi_rtt` | Best raw accuracy; needs PRS + ≥3 gNBs. |
| ≤ 3 m | `hybrid_rtt_aoa` | Sub-3 m without the response-time budget. |
| ≤ 5 m | `hybrid_rtt_aoa` | RTT distance + AoA bearing per gNB. |
| ≤ 10 m | `dl_aod` (if antenna info known) or `dl_tdoa` | Bearing intersection or weighted-centroid TDOA. |
| ≤ 15 m | `hybrid_gnss_ecid` | Inverse-variance fusion of GNSS + E-CID. |
| ≤ 30 m | `ul_aoa` | AoA bearing intersection at the gNB. |
| else | `ecid` | Cell-ID + TA + beam offset. |

A caller can also pass an explicit `method` to bypass the picker.

| Method | Spec § | Status | Algorithm |
|--------|--------|--------|-----------|
| `ecid` (NR E-CID) | TS 38.305 §8.9 | Implemented (heuristic) | TA × c/2 distance + beam-bearing offset; uncertainty floor 30/50 m. |
| `multi_rtt` | TS 38.305 §8.10 | Implemented | `trilaterateRTT` + linear-system solver (`lmf.go:1029-1068`). |
| `dl_tdoa` | TS 38.305 §8.12 | Simplified | `hyperbolicTrilaterate` (weighted-centroid; not full hyperbolic LS). |
| `ul_tdoa` | TS 38.305 §8.13 | Simplified | reuses `hyperbolicTrilaterate`. |
| `dl_aod` | TS 38.305 §8.11 | Implemented | bearing intersection (`intersectBearings`) for ≥2 gNBs, else project along beam. |
| `ul_aoa` | TS 38.305 §8.14 | Implemented | bearing intersection. |
| `agnss` (A-GNSS) | TS 38.305 §8.1 | Implemented (UE fix passthrough) | UE-reported lat/lon taken at face value. |
| `hybrid_gnss_ecid` | TS 38.305 §8.1 + §8.9 | Implemented (operator fusion) | inverse-variance weighted average. |
| `hybrid_rtt_aoa` | TS 38.305 §8.10 + §8.14 | Implemented (operator fusion) | per-gNB RTT distance × AoA bearing projection, weighted average + optional RTT trilateration. |

Fallback chain: if a method is requested but its measurement set is
empty (e.g. no RTT samples for `multi_rtt`), the LMF falls back to
`ecid` rather than failing.

### A.7 LCS privacy — the gate that runs first

```
   client_type           lcs_privacy row?       outcome
   ──────────────────────────────────────────────────────────
   emergency             (don't care)           admit (TS 23.271 §9)
   lawful_intercept      (don't care)           admit (operator policy)
   commercial            allowed = 0            DENY → 403
   commercial            allowed = 1            admit
   commercial            no row                 admit (default-allow)
   value_added           same as commercial     admit/deny per row
```

The gate runs in `routes_positioning.go` at the
`POST /api/location/request` entry, **before** the GMLC delegation.
Emergency requests are unconditional. The lcs_privacy table is
operator-managed via `GET / POST /api/lcs-privacy`.

### A.8 PRS — what the operator allocates and why

PRS (Positioning Reference Signal, TS 38.211 §7.4.1.7) is the radio
resource that DL-TDOA / DL-AoD / multi-RTT use as the reference
signal. The operator allocates it **per gNB** with a small parameter
set:

| Parameter | Spec set | Notes |
|-----------|----------|-------|
| `frequency_layer` | operator-local | Which carrier the resource lives on. |
| `periodicity_ms` | {4, 5, 8, 10, 16, 20, 32, 40, 64, 80, 160, 320, 640, 1280, 2560, 5120, 10240} | Out-of-set values are clamped, not rejected. |
| `comb_size` | {2, 4, 6, 12} | Density. |
| `num_symbols` | {2, 4, 6, 12} with `num_symbols × comb_size ≤ 12` | Constraint per §7.4.1.7. |
| `num_rb` | [24, 272] | Bandwidth allocation. |

Validation policy: clamp to spec sets — operator-friendly contract
for the GUI provisioning panel. The LMF returns the actual
post-clamp values so the OAM panel sees what was committed.

### A.9 Geofencing — what's wired today

`/api/geofences` exposes CRUD for polygon / circle geofences. The
geofence engine in the LMF persists fence definitions and tracks
per-UE inside/outside state. Area-event reporting against the
DB-backed `geofences` table is a stub today: `CheckPosition`
returns `nil` until the §6.3 area-event wiring lands
(`lmf.go:982-1004`). Geofences themselves are stored and
operator-readable; subscription / event delivery is the open piece.

### A.10 What is NOT in scope here

| Thing | Where it lives |
|-------|----------------|
| **NRPPa wire codec** (TS 38.455 §8.2) | Deferred. `HandleNRPPaMeasurementResponse` consumes pre-decoded measurement maps. |
| **LPP wire codec** (TS 37.355 §6) | Deferred. `HandleLPPMeasurementResponse` consumes pre-decoded LPP data. |
| **Full TS 23.273 §6.2 5GC-MT-LR signalling chain** | LCS Service Request → AMF → LMF → NRPPa/LPP not modelled. `RequestLocation` jumps straight into the per-method branch. |
| **Ngmlc SBI** (TS 29.515) | Deferred. GMLC is a thin in-process Go shim. |
| **Nlmf HTTP/2 + JSON envelope** (TS 29.572 §6) | Deferred. Calls are intra-process. |
| **Geofence area-event delivery** (TS 23.273 §6.3) | Definitions stored; subscription / event not wired. |
| **UE-to-UE sidelink ranging** | `positioning/ranging/` (TS 23.586) — different architecture, see [`ranging.md`](./ranging.md). |
| **Charging / billing** | `nf/chf/`. |
| **LI handover format** | LI feature, not this surface. |

---

## Part B — Design

### B.1 Architecture

```
   ┌──────────────────────────────────────────────────────────────────┐
   │  webservice/app/routes_positioning.go  (REST surface)             │
   │                                                                   │
   │  /api/positioning/sessions                                        │
   │  /api/positioning/history                                         │
   │  /api/geofences[/{fence_id}]                                      │
   │  /api/lcs-privacy                                                 │
   │  /api/gnb/position                                                │
   │  /api/gnb/antenna                                                 │
   │  /api/location/request   ← privacy gate runs here                 │
   │  /api/location/{session_id}                                       │
   │  /api/location/history                                            │
   │  /api/prs/allocate                                                │
   │  /api/prs/{gnb_id}                                                │
   │  /api/prs/{prs_id}        (DELETE)                                │
   └─────────────┬─────────────────────────────────────────────────┬───┘
                 │                                                 │
                 ▼                                                 ▼
       ┌────────────────────┐                            ┌────────────────────┐
       │      nf/gmlc        │                            │      lcs_privacy   │
       │  RequestLocation    │                            │  table (SQLite)    │
       │  GetLocation        │                            │  (imsi, client_type│
       │  CancelLocation     │                            │   allowed)         │
       │  LocationHistory    │                            └────────────────────┘
       │  AllocatePRS / …    │
       │  RegisterGnb…       │
       │  (TS 23.273 §4.3.3) │
       └────────┬────────────┘
                │  in-process Go call
                ▼
       ┌────────────────────────────────────────────────────────────┐
       │                       nf/lmf                                │
       │  Context (singleton)                                        │
       │    sessions          map[id]*Session                        │
       │    gnbPositions      map[gnbID]{lat,lon,alt}                │
       │    gnbAntennaInfo    map[gnbID]{azimuth,beamwidth,downtilt} │
       │    prsResources      map[prsID]*PRSResource                 │
       │                                                             │
       │  RequestLocation  ──▶ selectMethod (operator policy)         │
       │                       dispatch → execute<Method>            │
       │                                                             │
       │  execute*  positioning kernels:                              │
       │     ECID / MultiRTT / DL-TDOA / UL-TDOA /                   │
       │     DL-AoD / UL-AoA / A-GNSS /                              │
       │     Hybrid GNSS+ECID / Hybrid RTT+AoA                       │
       │                                                             │
       │  HandleNRPPaMeasurementResponse   (gNB → LMF ingest)         │
       │  HandleLPPMeasurementResponse     (UE  → LMF ingest)         │
       │  AllocatePRSResource              (TS 38.211 §7.4.1.7)       │
       │  GeofenceEngine                   (decision-only stub)       │
       └────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
                       SQLite (`webservice/sacore.db`)
                       positioning_sessions / location_history /
                       gnb_positions / gnb_antenna_info /
                       prs_resources / lcs_privacy / geofences
```

NRPPa and LPP wire codecs are **not modelled** today — both ingestion
entry points consume pre-decoded measurement maps. See B.7 for the
explicit TS 38.455 §8.2 / TS 37.355 §6 TODOs.

### B.2 REST surface (operator-facing)

| Method | Path | Backing | Notes |
|--------|------|---------|-------|
| `GET` | `/api/positioning/sessions` | `lmf.ListSessions(limit)` | Recent sessions across all UEs. |
| `GET` | `/api/positioning/history` | `lmf.LocationHistory(imsi, limit)` | Per-UE completed-session history. |
| `GET` / `POST` / `DELETE` | `/api/geofences[/{fence_id}]` | `geofences` table CRUD | Definitions only; area-event delivery is a stub. |
| `GET` / `POST` | `/api/lcs-privacy` | `lcs_privacy` table | TS 23.271 §9 consent gate input. |
| `POST` | `/api/gnb/position` | `lmf.RegisterGnbPosition` + persisted | Provisioning. |
| `POST` | `/api/gnb/antenna` | `lmf.RegisterGnbAntenna` + persisted | TS 38.455 §9.2.44 TRP info. |
| `POST` | `/api/location/request` | privacy gate → `gmlc.RequestLocation` → `lmf.RequestLocation` | The Determine-Location entry point. |
| `GET` | `/api/location/{session_id}` | `lmf.GetSessionByID` | 404 if missing. |
| `GET` | `/api/location/history?imsi=` | `lmf.LocationHistory(imsi, limit)` | Per-UE result history. |
| `POST` | `/api/prs/allocate` | `lmf.AllocatePRSResource` (TS 38.211 §7.4.1.7) | Out-of-range inputs are clamped. |
| `GET` | `/api/prs/{gnb_id}` | `lmf.GetPRSResourcesForGnb` | Active resources at a gNB. |
| `DELETE` | `/api/prs/{prs_id}` | `lmf.DeactivatePRSResource` | Soft deactivate. |

### B.3 Privacy gate (TS 23.271 §9)

`routes_positioning.go:344-360`:

```
on POST /api/location/request:
   parse { imsi, client_type, ... }
   if client_type == "emergency":
       admit unconditionally
   else:
       SELECT allowed FROM lcs_privacy WHERE imsi=? AND client_type=?
       if row found AND allowed=0:
           return 403 { "error": "denied by lcs_privacy", ... }
   delegate to gmlc.RequestLocation(...)
```

Default-allow when no row exists; explicit deny when row says
`allowed=0`. The gate is the only synchronous policy check on the
hot path.

### B.4 SBI surfaces (current shape)

| Operation | Spec | Status | Where |
|-----------|------|--------|-------|
| `Nlmf_Location_DetermineLocation` | TS 29.572 §5.2.2.2 | Intra-process Go (HTTP/2 envelope deferred) | `lmf.RequestLocation` |
| `Nlmf_Location_CancelLocation` | TS 29.572 §5.2.2.4 | Intra-process Go | `lmf.CancelSession` |
| `Ngmlc` (TS 29.515) | TS 29.515 | Deferred | — |
| `NRPPa Location Information Transfer` | TS 38.455 §8.2 | Pre-decoded ingest only | `lmf.HandleNRPPaMeasurementResponse` |
| `LPP ProvideLocationInformation` | TS 37.355 §6 | Pre-decoded ingest only | `lmf.HandleLPPMeasurementResponse` |
| PRS resource configuration | TS 38.211 §7.4.1.7 | Implemented (parameters validated/clamped) | `lmf.AllocatePRSResource` |

### B.5 Determine-Location lifecycle (synchronous)

```
GMLC                 LMF.Context              gNB / UE
  │ RequestLocation   │                         │
  ├──────────────────►│ selectMethod()          │
  │                   │ allocate session-id     │
  │                   │   state=PENDING→ACTIVE  │
  │                   │                         │
  │                   │ execute<Method>(s):     │
  │                   │   reads cached gNB pos  │
  │                   │   reads measurements    │
  │                   │   from s.RTTMeasurements│
  │                   │   etc. (if pre-fed) or  │
  │                   │   falls back to ECID    │
  │                   │                         │
  │                   │ (today: no NRPPa/LPP    │
  │                   │  request goes out — TODO│
  │                   │  TS 23.273 §6.2 +       │
  │                   │  TS 38.455 §8.2 +       │
  │                   │  TS 37.355 §6)          │
  │                   │                         │
  │                   │ setResult / FAILED      │
  │                   │ storeSession (SQLite)   │
  │                   │   positioning_sessions  │
  │                   │   + location_history    │
  │                   │     when COMPLETED      │
  │ Session response  │                         │
  │◄──────────────────│                         │
```

Async ingestion path (when wire codecs land):

```
gNB ─ NRPPa(...measurement report...) ─►  AMF transparent
                                       ─► LMF.HandleNRPPaMeasurementResponse(sessionID, measurements)
UE  ─ LPP(...ProvideLocationInformation...)
                                       ─► LMF.HandleLPPMeasurementResponse(sessionID, lppData)
```

Both append into the matching slice on `Session` (`RTTMeasurements`,
`TDOAMeasurements`, `AoAMeasurements`, `AoDMeasurements`,
`GNSSData`) so a follow-up `RequestLocation` can consume the
freshly-arrived data.

### B.6 Key types (umbrella)

```go
// Session — one positioning request (nf/lmf)
type Session struct {
    SessionID, IMSI, Method, State string
    CreatedAt, CompletedAt float64
    QoS              map[string]float64
    CellInfo         map[string]any
    TimingAdvance    *float64
    BeamIndex        *int
    RTTMeasurements  []map[string]any
    TDOAMeasurements []map[string]any
    AoAMeasurements  []map[string]any
    AoDMeasurements  []map[string]any
    GNSSData         map[string]any
    Latitude, Longitude, Altitude, UncertaintyM *float64
    Confidence *int
}

// PRS resource (TS 38.211 §7.4.1.7)
type PRSResource struct {
    PRSResourceID, FrequencyLayer, DLPRSResourceSetID,
    PeriodicityMS, SlotOffset, NumRB, StartPRB,
    NumSymbols, CombSize, REOffset, SequenceID int
    GnbID    string
    Active   bool
    CreatedAt float64
}

// LocationResult — GMLC return shape (nf/gmlc)
type LocationResult struct {
    SessionID, State, Method, IMSI string
    Latitude, Longitude, Altitude, UncertaintyM *float64
    Confidence *int
}
```

GMLC public API (in-process Go):

```go
// nf/gmlc
func RequestLocation(imsi, method string, accuracyM, responseTimeS float64,
    clientType string) LocationResult
func GetLocation(sessionID string) *LocationResult
func CancelLocation(sessionID string)
func LocationHistory(imsi string, limit int) []map[string]any

func RegisterGnbPosition(gnbID string, lat, lon, alt float64)
func RegisterGnbAntenna(gnbID string, azimuthDeg, beamwidthDeg, downtiltDeg float64, numBeams int)
func AllocatePRS(gnbID string, frequencyLayer, periodicityMS, numRB,
    numSymbols, combSize int) *lmf.PRSResource
func GetPRSConfig(gnbID string) []*lmf.PRSResource
func DeactivatePRS(prsID int)

func Status() map[string]any
```

LMF public API:

```go
// nf/lmf
func GetLMF() *Context

// Provisioning
func (*Context) RegisterGnbPosition(gnbID string, lat, lon, alt float64)
func (*Context) RegisterGnbAntenna(gnbID string, azimuthDeg, beamwidthDeg, downtiltDeg float64, numBeams int)

// Nlmf_Location
func (*Context) RequestLocation(imsi, method string, qos map[string]float64,
    lcsClientType string) *Session
func (*Context) CancelSession(sessionID string)
func (*Context) GetSession(sessionID string) *Session
func (*Context) GetSessionByID(sessionID string) (*Session, error)
func (*Context) ListSessions(limit int) ([]Session, error)
func (*Context) LocationHistory(imsi string, limit int) []map[string]any

// PRS (TS 38.211 §7.4.1.7)
func (*Context) AllocatePRSResource(gnbID string, frequencyLayer, periodicityMS,
    numRB, numSymbols, combSize int) *PRSResource
func (*Context) GetPRSResourcesForGnb(gnbID string) []*PRSResource
func (*Context) DeactivatePRSResource(prsResourceID int)

// Measurement ingest (NRPPa / LPP — pre-decoded today)
func (*Context) HandleNRPPaMeasurementResponse(sessionID string, measurements []map[string]any)
func (*Context) HandleLPPMeasurementResponse(sessionID string, lppData map[string]any)

// Geofence (decision-only stub today)
func GetGeofenceEngine() *GeofenceEngine
func (*GeofenceEngine) CheckPosition(imsi string, lat, lon float64) []map[string]any
func (*GeofenceEngine) ResetState(imsi string)
```

### B.7 Persistence

| Table | Owner | Purpose |
|-------|-------|---------|
| `positioning_sessions` | LMF | One row per `RequestLocation` (state, method, result, QoS). |
| `location_history` | LMF | Append-only per-IMSI completed-result history. |
| `gnb_positions` | LMF (via REST) | Provisioned gNB lat/lon/alt. |
| `gnb_antenna_info` | LMF (via REST) | Provisioned azimuth / beamwidth / downtilt / numBeams. |
| `prs_resources` | LMF | Active PRS resources per gNB. |
| `lcs_privacy` | routes_positioning | Per-(IMSI, client_type) consent gate. |
| `geofences` | LMF (via REST) | Polygon / circle definitions. Area-event delivery deferred. |

All tables are SQLite text-mode timestamps via `datetime('now')`.

### B.8 PRS validation

`AllocatePRSResource` clamps inputs to the TS 38.211 §7.4.1.7 sets
(`lmf.go:852-901`):

- periodicity ∈ {4, 5, 8, 10, 16, 20, 32, 40, 64, 80, 160, 320, 640,
  1280, 2560, 5120, 10240}
- combSize ∈ {2, 4, 6, 12}
- numSymbols ∈ {2, 4, 6, 12} with `numSymbols × combSize ≤ 12`
- numRB ∈ [24, 272]

Clamp, not reject — operator-friendly contract for the GUI
provisioning panel.

### B.9 What's not implemented — TODOs / stubs

| Anchor | Where | Status |
|--------|-------|--------|
| TS 29.572 §6 | `lmf.go:39-41` | HTTP/2 + JSON SBI envelope not wired. Calls intra-process. |
| TS 38.455 §8.2 | `lmf.go:42-46`, `:926-933` | NRPPa wire codec not modelled (E-CID Init/Report, Multi-RTT, TDOA, AoA Information Transfer). `HandleNRPPaMeasurementResponse` consumes pre-decoded maps. |
| TS 37.355 §6 | `lmf.go:47-50`, `:953-960` | LPP message envelope not modelled (ProvideLocationInformation, ProvideAssistanceData). `HandleLPPMeasurementResponse` consumes pre-decoded maps. |
| TS 23.273 §6.2 | `lmf.go:51-54` | Full 5GC-MT-LR signalling chain (LCS Service Request → AMF → LMF → NRPPa/LPP) not modelled. |
| TS 23.273 §6.x / §6.3 | `lmf.go:55-60`, `:998-1004` | Geofence area-event reporting against `geofences` table is a stub. `CheckPosition` returns `nil`. |
| TS 38.305 §8.9.3 / §8.9.4 | `lmf.go:434-436` | Proper DL/UL E-CID Positioning Procedures (real measurement collection over NRPPa) not done — current `executeECID` uses an offset-from-first-gNB heuristic. |
| Hybrid fusion weighting | `lmf.go:712-715`, `:758-763` | Inverse-variance fusion is operator policy, not §-mandated. |
| Method-selection table | `lmf.go:380-413` | QoS → method mapping is operator policy, not §-mandated. |
| TDOA hyperbolic solver | `lmf.go:536-578` | Weighted-centroid — not a full hyperbolic LS. |
| Ngmlc SBI | `gmlc.go:19-22` | TS 29.515 service operations not modelled. |
| GMLC ↔ AMF Le orchestration | `gmlc.go` | TS 23.273 §6 procedures (5GC-MT-LR / 5GC-MO-LR with deferred / area-event variants) not modelled. |
| LCS-client authorisation | `gmlc.go:46-48` | No policy check on `clientType` beyond the privacy-gate row in `routes_positioning.go`. |

### B.10 References

- **TS 22.071** — Location Services (Stage 1)
- **TS 23.271 §9** — LCS privacy
- **TS 23.273** — 5G LCS (Stage 2): §4.3.2, §4.3.3, §4.3.8, §4.4.1,
  §6.2, §6.3
- **TS 29.515** — Ngmlc Service (deferred)
- **TS 29.572** — Nlmf Service: §5.2.2.2, §5.2.2.4, §6
- **TS 37.355 §6** — LPP message envelope (deferred wire)
- **TS 38.211 §7.4.1.7** — PRS (and §7.4.1.7.3 / §7.4.1.7.4)
- **TS 38.305** — NG-RAN positioning protocols: §4.3, §8.1, §8.9,
  §8.9.3 / §8.9.4, §8.10, §8.11, §8.12, §8.13, §8.14
- **TS 38.455** — NRPPa: §8.2, §8.2.3, §8.2.6, §9.2.44

Per-NF design docs: [`../nf/lmf.md`](../nf/lmf.md) ·
[`../nf/gmlc.md`](../nf/gmlc.md). Sidelink ranging (TS 23.586,
device-to-device) is a different architecture covered in
[`ranging.md`](./ranging.md).

---
*Last refreshed against commit `a094196`.*

# LMF — Location Management Function

3GPP TS 23.273 §4.3.8 LMF; positioning compute engine for the 5GC.
~1.5k LOC at `nf/lmf/`. Receives Determine-Location requests, drives
NRPPa (gNB-side measurements) and LPP (UE-side measurements), runs
the per-method positioning math, persists session results to SQLite.

## 1. Role in 5GC

The LMF takes a positioning request (UE IMSI + QoS hints), picks a
positioning method, fans the measurement collection out to the gNBs
(via NRPPa over the AMF/N2 plane) and the UE (via LPP transparent
to the AMF/RAN), and returns a {lat, lon, uncertainty} result over
Nlmf_Location.

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **NLs / Nlmf** | AMF / GMLC | Nlmf_Location SBI | TS 29.572 §5.2 |
| **NL1** | LMF ↔ AMF (LCS messaging) | (intra-process today) | TS 23.273 §6.2 |
| **NRPPa** | gNB (via AMF transparent) | NRPPa Location Information Transfer | TS 38.455 §8.2 |
| **LPP** | UE (via AMF/gNB transparent) | LPP message envelope | TS 37.355 §6 |
| **Le** | LCS Client (via GMLC) | upstream of `gmlc.RequestLocation` | TS 23.273 §4.4.1 |

Listed reference-points and TS numbers are all already cited in
`nf/lmf/lmf.go:6-60`.

## 2. Architecture

```
        LCS client (commercial / emergency / lawful_intercept)
                        │  Le
                        ▼
                ┌─── nf/gmlc ────┐
                │ RequestLocation│
                └───────┬────────┘
                        │  in-process Go call
                        ▼
        ┌──────────────── nf/lmf ─────────────────┐
        │  lmf.go                                 │
        │   ├── Context (singleton)               │
        │   │     sessions   map[id]*Session      │
        │   │     gnbPositions / gnbAntennaInfo   │
        │   │     prsResources                    │
        │   │                                     │
        │   ├── RequestLocation  (TS 29.572 §5.2.2.2)
        │   │     selectMethod (operator policy)  │
        │   │     dispatch → execute<Method>      │
        │   │                                     │
        │   ├── execute*  (positioning kernels)   │
        │   │     ECID / MultiRTT / DL-TDOA /     │
        │   │     UL-TDOA / DL-AoD / UL-AoA /     │
        │   │     A-GNSS / Hybrid GNSS+ECID /     │
        │   │     Hybrid RTT+AoA                  │
        │   │                                     │
        │   ├── HandleNRPPaMeasurementResponse    │
        │   │     (gNB → LMF measurements ingest) │
        │   ├── HandleLPPMeasurementResponse      │
        │   │     (UE → LMF measurements ingest)  │
        │   ├── AllocatePRSResource (TS 38.211 §7.4.1.7)
        │   └── GeofenceEngine (decision-only)    │
        │                                         │
        │  webservice/sacore.db — SQLite store    │
        └─────────────────────────────────────────┘
                        │
                        ▼
              positioning_sessions / location_history
              (db/engine SQLite)
```

NRPPa and LPP wire codecs are **not modelled** today — both
ingestion entry points consume pre-decoded measurement maps.
See §8 for the explicit TS 38.455 §8.2 / TS 37.355 §6 TODOs.

## 3. Package / file map

| File | Role |
|------|------|
| `nf/lmf/lmf.go` | Entire LMF: types, Context, Nlmf service ops, all positioning kernels, PRS allocator, geofence stub, helpers |
| `nf/lmf/lmf_test.go` | Per-method unit tests |
| `nf/lmf/webservice/sacore.db` | SQLite artefact (no Go code) |

The package is one file because the surfaces (Nlmf, NRPPa-ingest,
LPP-ingest, PRS, geofence) are all small and share `Context`.

## 4. SBI surface (current shape)

| Method (Go) | 3GPP operation | Spec § |
|-------------|----------------|--------|
| `Context.RequestLocation` | Nlmf_Location_DetermineLocation | TS 29.572 §5.2.2.2 |
| `Context.CancelSession` | Nlmf_Location_CancelLocation | TS 29.572 §5.2.2.4 |
| `Context.GetSession` / `GetSessionByID` | (resource read; no SBI op) | — |
| `Context.HandleNRPPaMeasurementResponse` | NRPPa Location Information Transfer | TS 38.455 §8.2 (TODO) |
| `Context.HandleLPPMeasurementResponse` | LPP ProvideLocationInformation | TS 37.355 §6 (TODO) |
| `Context.AllocatePRSResource` | PRS resource configuration | TS 38.211 §7.4.1.7 |
| `Context.RegisterGnbPosition` / `RegisterGnbAntenna` | local provisioning | — |

HTTP/2 + JSON envelope is not yet in place: `lmf.go:39-41` carries the
explicit TODO for TS 29.572 §6 SBI surface wiring.

## 5. Positioning methods supported

Source: switch in `Context.RequestLocation` (`lmf.go:222-243`) plus
`selectMethod` (`lmf.go:386-414`).

| Method | Spec § | Status | Algorithm in code | Fallback |
|--------|--------|--------|-------------------|----------|
| `ecid` (NR E-CID) | TS 38.305 §8.9 | Implemented (heuristic) | `executeECID` `lmf.go:437-470` — TA × c/2 distance + beam bearing offset; uncertainty floor 30/50 m | — (terminal) |
| `multi_rtt` | TS 38.305 §8.10 | Implemented | `executeMultiRTT` + `trilaterateRTT` + linear-system solver `trilaterate` `lmf.go:1029-1068` | E-CID if <3 RTT |
| `dl_tdoa` | TS 38.305 §8.12 | Simplified | `hyperbolicTrilaterate` (weighted-centroid; not full hyperbolic LS) `lmf.go:532-578` | E-CID if <3 RSTD |
| `ul_tdoa` | TS 38.305 §8.13 | Simplified | reuses `hyperbolicTrilaterate` | E-CID if <3 RSTD |
| `dl_aod` | TS 38.305 §8.11 | Implemented | `executeDLAoD` — bearing intersection (`intersectBearings` `lmf.go:1104-1127`) for ≥2 gNBs, else project along beam | E-CID if no AoD measurements |
| `ul_aoa` | TS 38.305 §8.14 | Implemented | `executeULAoA` — same bearing intersection | E-CID if no AoA measurements |
| `agnss` (A-GNSS) | TS 38.305 §8.1 | Implemented (UE fix passthrough) | `executeAGNSS` — UE-reported lat/lon taken at face value | E-CID if no GNSS data |
| `hybrid_gnss_ecid` | TS 38.305 §8.1 + §8.9 (operator fusion, non-§) | Implemented | inverse-variance weighted average of GNSS + E-CID `lmf.go:716-756` | partial result on either input |
| `hybrid_rtt_aoa` | TS 38.305 §8.10 + §8.14 (operator fusion, non-§) | Implemented | per-gNB (RTT distance + AoA bearing) projection, weighted average plus optional RTT trilateration | E-CID |

Method picker `selectMethod` is **operator policy**, not §-mandated —
explicitly flagged in `lmf.go:380-384`. QoS `accuracy_m` thresholds:

```
≤3m  →  multi_rtt  (response_time≥5s)  | hybrid_rtt_aoa
≤5m  →  hybrid_rtt_aoa
≤10m →  dl_aod (if antenna info known) | dl_tdoa
≤15m →  hybrid_gnss_ecid
≤30m →  ul_aoa
else →  ecid
```

## 6. Headline lifecycle — Determine-Location (synchronous)

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
  │                   │  TS 23.273 §6.2 + §38.455│
  │                   │  §8.2 + §37.355 §6)     │
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
gNB ─ NRPPa(...measurement report...) ─►  AMF transparent  ─► LMF.HandleNRPPaMeasurementResponse(sessionID, measurements)
UE  ─ LPP(...ProvideLocationInformation...) ─► (UE→AMF→LMF) ─► LMF.HandleLPPMeasurementResponse(sessionID, lppData)
```

Both append into the matching slice on `Session` (`RTTMeasurements`,
`TDOAMeasurements`, `AoAMeasurements`, `AoDMeasurements`,
`GNSSData`) so a follow-up `RequestLocation` (or re-execution) can
consume the freshly-arrived data.

## 7. Key types / public API

```go
// Session — one positioning request
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

// Singleton
func GetLMF() *Context

// Provisioning
func (*Context) RegisterGnbPosition(gnbID string, lat, lon, alt float64)
func (*Context) RegisterGnbAntenna(gnbID string, azimuthDeg, beamwidthDeg, downtiltDeg float64, numBeams int)

// Nlmf_Location
func (*Context) RequestLocation(imsi, method string, qos map[string]float64, lcsClientType string) *Session
func (*Context) CancelSession(sessionID string)
func (*Context) GetSession(sessionID string) *Session
func (*Context) GetSessionByID(sessionID string) (*Session, error)
func (*Context) ListSessions(limit int) ([]Session, error)
func (*Context) LocationHistory(imsi string, limit int) []map[string]any

// PRS (TS 38.211 §7.4.1.7)
func (*Context) AllocatePRSResource(gnbID string, frequencyLayer, periodicityMS, numRB, numSymbols, combSize int) *PRSResource
func (*Context) GetPRSResourcesForGnb(gnbID string) []*PRSResource
func (*Context) DeactivatePRSResource(prsResourceID int)

// Measurement ingest (NRPPa / LPP — pre-decoded today)
func (*Context) HandleNRPPaMeasurementResponse(sessionID string, measurements []map[string]any)
func (*Context) HandleLPPMeasurementResponse(sessionID string, lppData map[string]any)

// Geofence (decision-only stub)
type GeofenceEngine struct{/*...*/}
func GetGeofenceEngine() *GeofenceEngine
func (*GeofenceEngine) CheckPosition(imsi string, lat, lon float64) []map[string]any
func (*GeofenceEngine) ResetState(imsi string)
```

PRS validation in `AllocatePRSResource` clamps inputs to TS 38.211
§7.4.1.7 sets (`lmf.go:852-901`):

- periodicity ∈ {4,5,8,10,16,20,32,40,64,80,160,320,640,1280,2560,5120,10240}
- combSize ∈ {2,4,6,12}
- numSymbols ∈ {2,4,6,12} with `numSymbols × combSize ≤ 12`
- numRB ∈ [24, 272]

Out-of-range inputs are clamped, not rejected (operator-friendly
contract for the GUI provisioning panel).

## 8. What's not implemented — TODOs / stubs (from source)

Verbatim from `lmf.go`:

| Anchor | Where | Status |
|--------|-------|--------|
| TS 29.572 §6 | `lmf.go:39-41` | HTTP/2 + JSON SBI envelope not wired. Calls are intra-process. |
| TS 38.455 §8.2 | `lmf.go:42-46`, `:926-933` | NRPPa wire codec not modelled (E-CID Measurement Initiation/Report, Multi-RTT, TDOA, AoA Information Transfer Procedures). `HandleNRPPaMeasurementResponse` consumes pre-decoded maps. |
| TS 37.355 §6 | `lmf.go:47-50`, `:953-960` | LPP message envelope not modelled (ProvideLocationInformation, ProvideAssistanceData). `HandleLPPMeasurementResponse` consumes pre-decoded maps. |
| TS 23.273 §6.2 | `lmf.go:51-54` | Full 5GC-MT-LR signalling chain (LCS Service Request → AMF → LMF → NRPPa/LPP) not modelled. `RequestLocation` jumps directly into the per-method branch. |
| TS 23.273 §6.x / §6.3 | `lmf.go:55-60`, `:998-1004` | Geofence area-event reporting wiring against the DB-backed `geofences` table is a stub. `CheckPosition` returns `nil`. |
| TS 38.305 §8.9.3 / §8.9.4 | `lmf.go:434-436` | Proper DL/UL E-CID Positioning Procedures (real measurement collection over NRPPa) not done — current `executeECID` uses an offset-from-first-gNB heuristic. |
| Hybrid fusion weighting | `lmf.go:712-715`, `:758-763` | Inverse-variance fusion is operator policy, not §-mandated. |
| Method-selection table | `lmf.go:380-384`, `:382-413` | QoS → method mapping is operator policy, not §-mandated. |
| TDOA hyperbolic solver | `lmf.go:536-578` | Uses weighted-centroid — not a full hyperbolic LS (acknowledged in code as "Simplified"). |

## 9. References (cited in source)

Only references already grep-verified inside `nf/lmf/`:

- TS 23.273 §4.3.3, §4.3.8, §4.4.1, §6.2, §6.3
- TS 29.572 §5.2.2.2, §5.2.2.4, §6
- TS 37.355 §6
- TS 38.211 §7.4.1.7, §7.4.1.7.3, §7.4.1.7.4
- TS 38.305 §4.3, §8.1, §8.9, §8.9.3, §8.9.4, §8.10, §8.11, §8.12, §8.13, §8.14
- TS 38.455 §8.2, §8.2.3, §8.2.6

---
*Last refreshed against commit `13a181d`.*

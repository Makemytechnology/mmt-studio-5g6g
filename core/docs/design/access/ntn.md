# NTN — Non-Terrestrial Network Access

5G Non-Terrestrial Network support for NR satellite access in the SA
Core build. The package owns the operator-side state for satellite-
served UEs: a constellation registry (LEO / MEO / GEO / HAPS), an
ephemeris / visibility model, geographic Tracking Area mapping,
propagation-delay math driving NAS-timer guard bands, a feeder-link
rebinding ledger, LEO-coverage DL buffering, and the Phase-2
enhancements (5G satellite backhaul, store-and-forward, inter-
satellite links, regenerative payload).

# Part A — Functional

## A.1 Why NTN?

Satellite access extends 5G coverage to ocean, polar, mountainous, and
disaster-relief footprints where terrestrial gNBs cannot reach. NTN
is normatively integrated into 5GS by TS 23.501 §5.4.10 (NR satellite
access RAT-type), §5.4.11 (integration into 5GS), §5.4.13
(discontinuous coverage), §5.4.14 (UE-Satellite-UE / ISL), §5.43 (5G
satellite backhaul), and TS 38.821 (the TR).

The NTN package is the **operator-/AMF-facing projection** of an NR
satellite access network. It does **not** drive on-air RRC or NGAP
itself; it computes the inputs the AMF + GUI need to reason about
satellite-served UEs:

- Where a satellite is *now* (`GetSatellitePosition`).
- Whether a UE on the ground sees it (`ComputeVisibility`).
- Which Tracking Area a (lat, lon) maps to (`TAIManager`).
- Propagation delay per leg, and the resulting NAS-timer extension.
- Active feeder-link bindings and historical switches.
- DL packets that need to wait through a coverage gap.
- Phase-2 ledgers: backhaul links, store-and-forward queue, ISL hops,
  regenerative-payload onboard NF profile.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **NG over satellite** | gNB ↔ AMF/UPF via satellite hop | NGAP / GTP-U | TS 38.300 §16.14, TS 38.821 §5.1 (transparent) / §5.2 (regenerative) | Operator config + delay model; on-wire NGAP is in `nf/amf/ngap` |
| **Service link** | UE ↔ satellite | NR Uu | TS 38.300 §16.14 | Slant-range / RTT model only; PHY is RAN-side |
| **Feeder link** | satellite ↔ ground gateway | physical-layer feeder | TS 38.821 §6.2.5 (feeder link switch) | `FeederLinkManager` ledger + switch history |
| **N2 (regenerative)** | onboard NF ↔ ground 5GC | NGAP over feeder | TS 23.501 §5.4.11.9 | DB row holding onboard-NF list + status (`RegenerativePayload`) |
| **5G satellite backhaul** | terrestrial gNB ↔ 5GC via satellite uplink | operator-policy mapping | TS 23.501 §5.43 | `BackhaulManager` ledger (gnb_id ↔ sat_id, capacity) |
| **ISL** | satellite ↔ satellite | onboard mesh | TS 23.501 §5.4.14, TS 38.821 §5 (architecture, no single-clause anchor) | `ntn_isl_links` DB pair table + `ISLManager` mesh ledger |

## A.3 Operator-visible behaviours

### A.3.1 Constellation registry

- LEO / MEO / GEO / HAPS satellites loaded by `LoadDefaults` (2 LEO + 1
  GEO) or operator POST.
- Per-satellite `MinRTTMS` / `MaxRTTMS` are **derived** from altitude
  and 10° elevation slant-range — not free-form, see §B.4.1.
- Ground stations carry `connected_gnb_ip` so the feeder-link ledger
  can re-bind by sat_id.

### A.3.2 Coverage check (UE → constellation)

`CheckCoverage(constellation, lat, lon, minElev)` returns the highest-
elevation visible satellite plus a count of all visible satellites.
When `covered=false`, downlink for that UE goes into the DL coverage
buffer (TS 23.501 §5.4.13 LEO pass gap model). Buffer is per-IMSI,
capped at 100 packets, TTL 3600 s; `FlushDLBuffer(imsi)` drains it on
coverage resumption.

### A.3.3 NAS-timer guard band

LEO RTT is ~2-50 ms; GEO RTT is ~270-540 ms. That dwarfs terrestrial
NAS-timer windows. `GetAdjustedNASTimers(sat)` extends the
TS 24.501 §5.3.7 timers (T3510, T3511, T3512, T3517, T3521) by `4 ×
max-RTT` (operator policy informed by TS 38.821 §6.3 — not §-mandated)
and doubles `T3512` (periodic registration) for GEO.

### A.3.4 Geographic TAI

A satellite beam moves over the ground, so the Tracking Area the UE
observes depends on the **UE's** location, not the gNB
(TS 23.501 §5.4.11.7). The TAI manager maps `(lat, lon)` to a TAC
using a haversine-radius test. TAU is triggered when the UE crosses an
NTN TA boundary.

### A.3.5 Feeder-link switching

A LEO satellite transits between gateways every few minutes. The
operator records the active `(sat → gNB / gs)` binding and logs each
switch event so the GUI / OAM layer can trace re-bindings
(TS 38.821 §6.2.5).

### A.3.6 Phase-2 — Regenerative payload (TS 38.821 §5.2 / TS 23.501 §5.4.11.9)

For regenerative satellites the NF profile (which AMF/UPF/SMF run
**onboard**) is operator-configured per `sat_id`. `GetSatCapabilities`
joins this with the satellite's ISL pair table to produce a
`{onboard_nfs, has_regenerative, isl_count, isl_links}` snapshot the
GUI / scheduler can branch on.

### A.3.7 Phase-2 — Store-and-forward queue

Downlink data targeted at a UE behind an out-of-contact LEO is queued
in `ntn_store_forward` keyed by `(sat_id, target)`, with priority and
status (queued/forwarded/expired). Spec status: TR 38.821 v16.2 has no
single-clause normative anchor for S&F — see §A.5.

### A.3.8 Phase-2 — Inter-satellite links (TS 23.501 §5.4.14)

The `ntn_isl_links` DB table holds the operator-visible ISL adjacency
with `(sat1_id, sat2_id, bandwidth_mbps, latency_ms, status)`. Pair
keys are normalized so `sat1_id < sat2_id` and unique. The
`ISLManager` (in-memory mesh) is a separate operator surface for the
adjacency map without DB persistence.

### A.3.9 Phase-2 — 5G satellite backhaul (TS 23.501 §5.43)

`BackhaulManager` records which terrestrial gNB rides which satellite
uplink, at what capacity, and tracks `current_mbps` so the GUI can
flag over-utilization.

## A.4 Operator REST API (`/api/ntn/*`)

All endpoints respond with JSON. Bodies use snake_case keys.

### A.4.1 Constellation

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ntn/constellation` | `{satellites, ground_stations}` |
| GET    | `/api/ntn/satellites` | satellite list |
| POST   | `/api/ntn/satellite` (alias `/satellites`) | add a satellite (`sat_id`, `orbit_type`, `altitude_km`, …) |
| GET    | `/api/ntn/satellites/{sat_id}` | one satellite |
| DELETE | `/api/ntn/satellites/{sat_id}` | remove |
| GET    | `/api/ntn/ground-stations` | ground-station list |
| POST   | `/api/ntn/ground-station` (alias `/ground-stations`) | add a ground station |
| POST   | `/api/ntn/load-defaults` | seed 2 LEO + 1 GEO + 5 TAIs (Indian-subcontinent grid) |

### A.4.2 Coverage / DL buffer (TS 23.501 §5.4.13)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ntn/coverage?lat=…&lon=…&min_elev=10` | best visible sat |
| GET    | `/api/ntn/buffer-status?imsi=…` | per-IMSI or aggregate `{total_ues_buffered, total_packets}` |
| POST   | `/api/ntn/buffer` | enqueue `{imsi, data}` |
| POST   | `/api/ntn/buffer/{imsi}/flush` | drain non-stale entries |

### A.4.3 Feeder links (TS 38.821 §6.2.5)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ntn/feeder-links` | `{active_links, switch_history}` |
| POST   | `/api/ntn/feeder-links` | register `{sat_id, gs_id, gnb_ip}` |
| POST   | `/api/ntn/feeder-links/switch` | move sat to new gs |
| GET    | `/api/ntn/feeder-links/history?limit=N` | switch ledger only |

### A.4.4 Geographic TAI (TS 23.501 §5.4.11.7)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ntn/tais` | `{tais, count}` |
| POST   | `/api/ntn/tais/load-defaults` | seed 5-TAI grid for `{mcc, mnc}` |
| GET    | `/api/ntn/tais/lookup` (alias `/tai-lookup`) | `{tai, location}` for `?lat&lon` |

### A.4.5 Timing (TS 38.821 §6.3)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ntn/timing?sat_id=…&lat=…&lon=…` | `{delay, adjusted_timers}` |
| POST   | `/api/ntn/propagation` | raw delay map (no NAS wrap) |
| GET    | `/api/ntn/satellites/{sat_id}/nas-timers` | adjusted NAS timers only |

### A.4.6 Phase-2 (DB-backed surface)

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/ntn/phase2/regenerative` | UPSERT regen profile (sat_id in body) |
| POST   | `/api/ntn/phase2/regenerative/{sat_id}` | UPSERT (sat_id in path) |
| GET    | `/api/ntn/phase2/regenerative` | list profiles |
| GET    | `/api/ntn/phase2/regenerative/{sat_id}` | one profile |
| DELETE | `/api/ntn/phase2/regenerative/{sat_id}` | remove |
| GET    | `/api/ntn/phase2/capabilities/{sat_id}` | `{onboard_nfs, has_regenerative, isl_links, isl_count, …}` |
| POST   | `/api/ntn/phase2/store-forward` | enqueue `{sat_id, target, data_hex, priority}` |
| GET    | `/api/ntn/phase2/store-forward` | aggregate `{queue, count}` (all sats) |
| GET    | `/api/ntn/phase2/store-forward/{sat_id}` | per-sat queue |
| GET    | `/api/ntn/phase2/isl` | DB-backed pair table |
| POST   | `/api/ntn/phase2/isl` | UPSERT `{sat1_id, sat2_id, bandwidth_mbps, latency_ms, status?}` |
| DELETE | `/api/ntn/phase2/isl/{link_id}` | by row id |

### A.4.7 Phase-2 (in-memory operator ledgers)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ntn/phase2/backhaul` | all gNB→sat backhaul links |
| POST   | `/api/ntn/phase2/backhaul` | provision `{gnb_id, satellite_id, capacity_mbps}` |
| DELETE | `/api/ntn/phase2/backhaul/{gnb_id}` | deprovision |
| POST   | `/api/ntn/phase2/backhaul/{gnb_id}/usage` | report `{mbps}` |
| GET    | `/api/ntn/phase2/backhaul/stats` | `{total_links, active_links, capacity_mbps, usage_mbps, utilization_pct}` |
| POST/GET | `/api/ntn/phase2/saf/...` | per-IMSI byte-count buffer ledger |
| GET/POST/DEL | `/api/ntn/phase2/isl-mesh{,/...}` | in-memory adjacency mesh (separate from DB-backed `/phase2/isl`) |
| GET    | `/api/ntn/phase2/stats` | merged Phase-2 dashboard |

## A.5 Spec gaps / TODOs

| TODO | Status | Notes |
|------|--------|-------|
| `TS 38.821 (S&F)` | Operator stub | Store-and-forward operation is not yet a normative clause in v16.2 of the TR. Pending Rel-19+. |
| `TS 38.821 (ISL)` | Operator stub | ISL is referenced in TR §5.x architecture variants without a single-clause anchor in v16.2. |
| `TS 38.331 §6.3.x` | Not decoded | NTN-specific RRC IEs (epoch time, ephemeris parameter set) are not decoded; `SatelliteConfig` holds local orbital parameters only. |
| `TS 23.502 §4.x` | Not wired | Satellite-aware Registration / Service Request signalling is not wired into the AMF; `TAIManager` is consulted in isolation. |

---

# Part B — Design

## B.1 Process surface

```
┌──────────────────── NTN process surface (in-AMF) ─────────────────────────┐
│                                                                           │
│  Constellation (in-memory)                                                │
│   ├── satellites:      map[satID] *SatelliteConfig                        │
│   │                    (orbit_type, alt_km, incl, lon, beam_count,        │
│   │                     min/max RTT — derived in NewSatelliteConfig)      │
│   └── groundStations:  map[gsID]  *GroundStationConfig                    │
│                                                                           │
│           DefaultConstellation ── LoadDefaults() seeds 2× LEO + 1× GEO    │
│                                                                           │
│  Ephemeris                          GeographicTAI ──────────              │
│   GetSatellitePosition(sat, t)        TAIManager.GetTAIForLocation        │
│   ComputeVisibility(pos, ueLat,       (haversineKM ≤ radius_km)           │
│                     ueLon, minElev)   HasTAIChanged(oldTAC, newLat, lon)  │
│           │                                  │                            │
│           ▼                                  ▼                            │
│  CoverageManager                     Timing                               │
│   CheckCoverage → best satellite      ComputePropagationDelay             │
│   BufferDLPacket / FlushDLBuffer        (service-link slant + feeder)     │
│   (TS 23.501 §5.4.13 LEO gaps)        GetAdjustedNASTimers                │
│                                         (4× max-RTT guard, 2× T3512 GEO)  │
│                                                                           │
│  FeederLinkManager                   Phase-2 surfaces (phase2.go)         │
│   activeLinks: satID → {gs,gnb,t}     BackhaulManager (TS 23.501 §5.43)   │
│   switchHistory  (TS 38.821 §6.2.5)   SAFManager      (operator)          │
│                                       ISLManager      (operator mesh)     │
│                                                                           │
│  DB-backed Phase-2 (ntn.go tail)                                          │
│   RegenerativePayload  → ntn_regenerative_config                          │
│   StoreAndForward      → ntn_store_forward                                │
│   InterSatLink         → ntn_isl_links                                    │
│                                                                           │
└───────────────────────────────────────────────────────────────────────────┘
                                    ▲
                                    │ JSON / HTTP via OAM panel
                                    │
                                  GUI / API surface
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `access/ntn/ntn.go` | ~770 | Constellation + ephemeris + coverage + feeder link + TAI + timing + DB-backed Phase-2 (regenerative / S&F / ISL) + helpers. |
| `access/ntn/phase2.go` | ~420 | Operator-side ledgers for §5.43 backhaul, S&F counters, ISL pair table — pure in-memory, no DB. |
| `db/schemas/ntn.go` | 60 | DDL for `ntn_regenerative_config`, `ntn_store_forward`, `ntn_isl_links`. |
| `webservice/app/routes_ntn.go` | ~600 | REST surface for §A.4. |

Tests:
- `access/ntn/ntn_test.go` — RTT math, visibility geometry, TAI containment, propagation-delay model, NAS-timer guard, feeder-link switch.
- `access/ntn/phase2_test.go` — backhaul provision / deprovision / usage update; S&F enqueue / drain; ISL add / remove / neighbours.
- `mmt_studio_core_tester/src/testcases/vertical/tc_ntn.py` — 12 vertical TCs (TC-NTN-001 → TC-NTN-012).
- `mmt_studio_core_tester/src/testcases/infra/tc_ntn_phase2.py` — 5 Phase-2 TCs (TC-NTN2-001 → TC-NTN2-005).

## B.3 Wire-format / NAS interactions (in code today)

### B.3.1 NAS-timer guard band (`GetAdjustedNASTimers`, ntn.go:488)

| Timer | Base | Adjustment | Anchor |
|-------|------|------------|--------|
| `T3510` (Registration)            | 15 s   | `+ 4 × max-RTT` | TS 24.501 §5.3.7 + TS 38.821 §6.3 (operator policy, not §-mandated) |
| `T3511` (Re-registration)         | 10 s   | `+ 4 × max-RTT` | same |
| `T3512` (Periodic registration)   | 3240 s | doubled for GEO; otherwise unchanged | ntn.go:494 |
| `T3517` (Service Request)         | 15 s   | `+ 4 × max-RTT` | same |
| `T3521` (Detach)                  | 15 s   | `+ 4 × max-RTT` | same |

`max-RTT` is `SatelliteConfig.MaxRTTMS / 1000`. `MaxRTTMS` is computed
in `NewSatelliteConfig` (ntn.go:104-112) from a 10° elevation slant-
range model.

### B.3.2 Propagation-delay model (`ComputePropagationDelay`, ntn.go:467)

```
    UE → service link → SAT → feeder link → ground gateway → 5GC
```

| Field | Computation | Notes |
|-------|-------------|-------|
| `service_link_ms` | `slant_km / c × 1000` (slant from `ComputeVisibility`) | nil when satellite not visible from UE |
| `feeder_link_ms`  | `altitude_km / c × 1000` | Approximation: nadir distance, not gateway-specific |
| `total_one_way_ms`| service + feeder | nil when not visible |
| `rtt_ms`          | `2 × total_one_way_ms` | round-trip estimate |

When the UE position is not supplied, the function falls back to
`feeder × 2` as a worst-case proxy (ntn.go:479-481).

### B.3.3 Visibility / position math

- `GetSatellitePosition(sat, t)` (ntn.go:206) — analytic position
  model. GEO is fixed at `(0, longitude)`; HAPS sits at a fixed
  `(lat, lon)`. LEO/MEO uses Kepler period from `GravitationalParam`
  (398 600.4418 km³/s²), mod-folds the orbit fraction, projects to
  `(lat, lon)` using the inclination, and corrects for Earth's
  rotation (`360°/86400 s`).
- `ComputeVisibility(pos, ueLat, ueLon, minElev)` — great-circle
  separation → elevation angle at UE → slant range. Returns
  `(visible, elev_deg, slant_km)`.

### B.3.4 DB schema (Phase-2)

| Table | Writer | Columns |
|-------|--------|---------|
| `ntn_regenerative_config` | `RegenerativePayload` (ntn.go:510) | `sat_id` UNIQUE, `onboard_nfs` (JSON-encoded list), `processing_capacity` INT, `memory_mb` INT, `status` TEXT (default `standby`). |
| `ntn_store_forward`       | `StoreAndForward` (ntn.go:552) | `sat_id`, `target`, `data_hex`, `data_size`, `priority`, `status` ∈ `{queued, forwarded, expired}`. Indexed on `status` and `(sat_id, status)`. |
| `ntn_isl_links`           | `InterSatLink` (ntn.go:586) | normalized `(sat1_id, sat2_id)` UNIQUE (sorted ascending), `bandwidth_mbps`, `latency_ms`, `status`. |

DDL lives in `db/schemas/ntn.go`; the schema package's `Register("ntn", ...)` is invoked at NTN-package init.

## B.4 Headline procedures

### B.4.1 Timing-advance management (UE → satellite → gateway)

```
NewSatelliteConfig(altKM)      ←── orbit / altitude / inclination
        │
        ├── MinRTTMS  = 2 × (alt / c)              ← nadir RTT
        └── MaxRTTMS  = 2 × ((slant10°/c) + min)   ← 10° elevation horizon
                                │
                                ▼
                    GetAdjustedNASTimers(sat)
                    +4 × max-RTT guard band added to T3510/T3511/T3517/T3521
                    T3512 doubled for GEO orbit_type
```

### B.4.2 Ephemeris + visibility per UE

```
CheckCoverage(constellation, ueLat, ueLon, minElev)        ntn.go:267
        for each sat in constellation:
            pos = GetSatellitePosition(sat, now)
            (vis, elev, slant) = ComputeVisibility(pos, ueLat, ueLon, minElev)
            if vis and elev > best_elev: best = sat
        → {covered, serving_satellite, elevation_deg, slant_range_km,
           visible_satellites, satellite_position}
```

When `covered=false`, `BufferDLPacket(imsi, data)` enqueues DL packets
under `dlBuffer[imsi]` with `maxBufferPerUE = 100`, `bufferTTL = 3600 s`.
On coverage resumption `FlushDLBuffer(imsi)` returns the non-stale
entries.

### B.4.3 Geographic TAI

```go
TAIManager.AddTAI(GeographicTAI{TAIID, MCC, MNC, TAC,
                                CenterLat, CenterLon, RadiusKM})
TAIManager.GetTAIForLocation(lat, lon) *GeographicTAI
TAIManager.HasTAIChanged(oldTAC, newLat, newLon) bool
```

`Contains(lat, lon)` is a haversine ≤ radius_km test. `LoadDefaults`
seeds five 500-km-radius TAIs across an Indian-subcontinent grid.

### B.4.4 Feeder-link switch

```
RegisterFeederLink(satID, gsID, gnbIP):
   old = activeLinks[satID]
   activeLinks[satID] = {gs_id, gnb_ip, since}
   if old != nil and old.gs_id != gsID:
       switchHistory ← {sat_id, from_gs, to_gs, ts}

InitiateSwitch(satID, newGSID, newGnbIP):
   thin wrapper that returns {switched, from_gs, to_gs}
```

### B.4.5 Phase-2 Backhaul (TS 23.501 §5.43)

```
Provision(gnbID, satID, capMbps)        phase2.go:83
Deprovision(gnbID)                      phase2.go:103
SetActive(gnbID, active)                phase2.go:115
UpdateUsage(gnbID, mbps)                phase2.go:129
Get(gnbID) / All() / Stats()
```

`Stats()` returns `{total_links, active_links, total_capacity_mbps,
total_usage_mbps, utilization_pct}`.

### B.4.6 Phase-2 Store-and-Forward

```
StoreAndForward(satID, dataHex, target)         ntn.go:552  (DB INSERT)
GetStoreForwardQueue(satID)                     ntn.go:564  (status='queued')
ListStoreForwardQueued()                        ntn.go:572  (all sats)
ForwardQueued(id) / ExpireQueued(id)
```

Store-and-forward is operator policy modelled on TS 23.501 §5.4.13
(LEO pass gaps); see TODO at ntn.go:46.

### B.4.7 Phase-2 ISL

DB-backed pair table at `ntn_isl_links` is the operator-of-record;
in-memory `ISLManager` is a separate adjacency mesh for fast lookup
and is **not** persisted.

```
InterSatLink(sat1, sat2, config)        ntn.go:586  (UPSERT, normalize pair)
DeleteISLLink(linkID int64)             ntn.go:619
ListISLLinksForSat(satID)               ntn.go:615
```

### B.4.8 Phase-2 Regenerative payload

```
RegenerativePayload(satID, config)      ntn.go:510  (UPSERT)
   ├── onboard_nfs       JSON-encoded list (e.g. "AMF,UPF" or ["AMF","UPF"])
   ├── processing_capacity, memory_mb
   └── status            standby | active

GetSatCapabilities(satID)               ntn.go:640
   → {sat_id, regenerative, isl_links, has_regenerative, isl_count}
```

## B.5 Public API

```go
// Constellation
type SatelliteConfig struct {
    SatID, Name, OrbitType  string   // LEO|MEO|GEO|HAPS
    AltitudeKM, InclinationDeg, LongitudeDeg float64
    BeamCount               int
    BeamDiameterKM          float64
    MinRTTMS, MaxRTTMS      float64  // derived
}
type GroundStationConfig struct {
    GSID, Name              string
    Latitude, Longitude     float64
    ConnectedGnbIP          string
    Active                  bool
}
type Constellation struct{ /* ... */ }
var DefaultConstellation = NewConstellation()
func (c *Constellation) AddSatellite(s *SatelliteConfig)
func (c *Constellation) GetSatellite(id string) *SatelliteConfig
func (c *Constellation) GetGroundStationForGnb(gnbIP string) *GroundStationConfig
func (c *Constellation) LoadDefaults()  // seeds 2× LEO + 1× GEO + 1 GS

// Ephemeris / visibility
type SatellitePosition struct{ Latitude, Longitude, AltitudeKM, Timestamp float64 }
func GetSatellitePosition(sat *SatelliteConfig, atTime float64) *SatellitePosition
func ComputeVisibility(pos *SatellitePosition, ueLat, ueLon, minElev float64) (vis bool, elev, slant float64)

// Coverage + buffering
type CoverageManager struct{ /* ... */ }
var DefaultCoverageMgr = NewCoverageManager()
func (m *CoverageManager) CheckCoverage(c *Constellation, ueLat, ueLon, minElev float64) map[string]any
func (m *CoverageManager) BufferDLPacket(imsi string, data any)
func (m *CoverageManager) FlushDLBuffer(imsi string) []dlEntry
func (m *CoverageManager) GetBufferStatus(imsi string) map[string]any  // "" → aggregate

// Feeder link
type FeederLinkManager struct{ /* ... */ }
var DefaultFeederLinkMgr = NewFeederLinkManager()
func (f *FeederLinkManager) RegisterFeederLink(satID, gsID, gnbIP string) map[string]any
func (f *FeederLinkManager) InitiateSwitch(satID, newGSID, newGnbIP string) map[string]any
func (f *FeederLinkManager) GetSwitchHistory(limit int) []map[string]any
func (f *FeederLinkManager) GetAllActiveLinks() map[string]map[string]any

// Geographic TAI
type GeographicTAI struct {
    TAIID, MCC, MNC, TAC string
    CenterLat, CenterLon, RadiusKM float64
}
type TAIManager struct{ /* ... */ }
var DefaultTAIMgr = NewTAIManager()
func (m *TAIManager) GetTAIForLocation(lat, lon float64) *GeographicTAI
func (m *TAIManager) HasTAIChanged(oldTAC string, newLat, newLon float64) bool

// Timing
func ComputePropagationDelay(sat *SatelliteConfig, ueLat, ueLon *float64) map[string]any
func GetAdjustedNASTimers(sat *SatelliteConfig) map[string]any

// Phase-2 (in-memory ledgers, phase2.go)
type BackhaulManager struct{ /* ... */ }
type SAFManager struct{ /* ... */ }
type ISLManager struct{ /* ... */ }
var DefaultBackhaulMgr = NewBackhaulManager()
var DefaultSAFMgr      = NewSAFManager()
var DefaultISLMgr      = NewISLManager()

// Phase-2 (DB-backed, ntn.go)
func RegenerativePayload(satID string, config map[string]any) (map[string]any, error)
func GetRegenerativeConfig(satID string) (map[string]any, error)
func ListRegenerativeConfigs() ([]map[string]any, error)
func DeleteRegenerativeConfig(satID string) error
func StoreAndForward(satID, dataHex, target string) (map[string]any, error)
func GetStoreForwardQueue(satID string) ([]map[string]any, error)
func ListStoreForwardQueued() ([]map[string]any, error)
func InterSatLink(sat1ID, sat2ID string, config map[string]any) (map[string]any, error)
func ListISLLinks() ([]map[string]any, error)
func ListISLLinksForSat(satID string) ([]map[string]any, error)
func DeleteISLLink(linkID int64) error
func GetSatCapabilities(satID string) map[string]any
func GetPhase2Stats() (map[string]any, error)
```

Constants exported for the math:

```go
const (
    EarthRadiusKM      = 6371.0
    SpeedOfLightKMS    = 299792.458
    GravitationalParam = 398600.4418  // km³/s² (Earth)
)
```

## B.6 Test coverage

### B.6.1 Go unit tests

`access/ntn/ntn_test.go`, `access/ntn/phase2_test.go` — RTT math,
visibility geometry, TAI containment, propagation-delay, NAS-timer
guard, feeder-link switch; backhaul/SAF/ISL ledger CRUD.

### B.6.2 Live integration tests (Python tester)

| Suite | TC IDs | Coverage |
|-------|--------|----------|
| `vertical/tc_ntn.py` | TC-NTN-001 → TC-NTN-012 | Defaults, satellite/GS CRUD, LEO+GEO coverage, timing, timer adjustment, TAI lookup/change, feeder-link panel, DL buffer, ephemeris positions. |
| `infra/tc_ntn_phase2.py` | TC-NTN2-001 → TC-NTN2-005 | Regenerative config, S&F queue, ISL pair, capabilities snapshot, Phase-2 stats. |

All 17 are wired into the `tests/ntn` runner and pass against the
current core build.

## B.7 References

- **TS 22.261** §6.3.2.3 — service requirements for satellite access.
- **TS 23.501**:
  - §5.4.10 — identification and restriction of NR satellite access.
  - §5.4.11 — integrating NR satellite access into 5GS (umbrella).
  - §5.4.11.4 — UE location verification.
  - §5.4.11.7 — Tracking Area handling for NR satellite access.
  - §5.4.11.9 — N2 / connection management for regenerative payload.
  - §5.4.13 — discontinuous network coverage for satellite access.
  - §5.4.13.2 — coverage availability provisioning analogue.
  - §5.4.13.4 — paging analogue.
  - §5.4.14 — UE-Satellite-UE communication (ISL).
  - §5.43   — 5G satellite backhaul.
- **TS 23.502** §4.x — TODO; satellite-aware Reg/SR not wired.
- **TS 38.300** §16.14 — NR support for non-terrestrial networks.
- **TS 38.331** §6.3.x — TODO; NTN RRC IEs not decoded.
- **TS 38.821** (TR — NTN study):
  - §4.1 — NTN overview.
  - §5.1 — transparent satellite-based NG-RAN.
  - §5.2 — regenerative satellite-based NG-RAN.
  - §6.2.5 — feeder link switch.
  - §6.3 — UL timing advance / RACH (drives our delay+NAS-timer model).
  - §5.x architecture variants — ISL/S&F deferred items.

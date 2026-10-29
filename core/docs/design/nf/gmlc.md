# GMLC — Gateway Mobile Location Centre

3GPP TS 23.273 §4.3.3 GMLC. ~250 LOC at `nf/gmlc/`. Thin LCS-client-
facing entry that delegates to the LMF.

## 1. Role in 5GC

The GMLC is the location-services gateway: an external LCS client
(commercial app, emergency service, lawful intercept consumer)
issues a positioning request to the GMLC, which forwards it to the
LMF and returns the result. In this build the GMLC layer is a
straight pass-through over Go calls to `nf/lmf`.

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Le** | LCS Client | external | TS 23.273 §4.4.1 |
| **NL5** / **Ngmlc** (planned) | NEF / consumer | Ngmlc SBI | TS 29.515 (TODO) |
| (intra-NF) | LMF | `lmf.GetLMF().RequestLocation` | TS 29.572 §5.2.2.2 |

All references above are already cited in `nf/gmlc/gmlc.go:6-22`.

## 2. Architecture

```
LCS Client (Le)
   │   class ∈ {commercial, emergency, lawful_intercept, value_added}
   ▼
┌─── nf/gmlc ───┐         ┌─── nf/lmf ────┐
│ RequestLocation│ ──────► │ Context       │
│ GetLocation    │         │ RequestLocation
│ CancelLocation │         │ GetSession    │
│ AllocatePRS …  │         │ AllocatePRS … │
└────────────────┘         └───────────────┘
```

The GMLC's job here is QoS-hint shaping: bundle `accuracyM` /
`responseTimeS` into an LMF-shaped `qos` map, set a default
`clientType="commercial"`, and forward.

## 3. Package / file map

| File | Role |
|------|------|
| `nf/gmlc/gmlc.go` | All public functions; LMF delegate |
| `nf/gmlc/gmlc_test.go` | Unit tests |
| `nf/gmlc/webservice/sacore.db` | SQLite artefact (no Go code) |

## 4. Non-SBI surface

No HTTP router today (TODO TS 29.515 — `gmlc.go:19-22`). API is
in-process Go.

| Method (Go) | 3GPP operation | Spec |
|-------------|----------------|------|
| `RequestLocation` | (Le-side) → Nlmf_Location_DetermineLocation | TS 29.572 §5.2.2.2 |
| `GetLocation` | (Le-side) result lookup | — |
| `CancelLocation` | Nlmf_Location_CancelLocation | TS 29.572 §5.2.2.4 |
| `LocationHistory` | session history (LMF-cached) | — |
| `RegisterGnbPosition` / `RegisterGnbAntenna` | provisioning passthrough | — |
| `AllocatePRS` / `GetPRSConfig` / `DeactivatePRS` | PRS provisioning passthrough (TS 38.211 §7.4.1.7) | — |

## 5. Headline lifecycle — RequestLocation

`gmlc.go:49-78`:

```
client                  GMLC                     LMF
  │ RequestLocation      │                       │
  │  (imsi, method,      │                       │
  │   accuracyM,         │                       │
  │   responseTimeS,     │                       │
  │   clientType)        │                       │
  ├─────────────────────►│                       │
  │                      │ default clientType to │
  │                      │  "commercial" if "".  │
  │                      │ Build qos = {         │
  │                      │   accuracy_m: …,      │
  │                      │   response_time_s: …  │
  │                      │ } (only if non-zero). │
  │                      │                       │
  │                      │ ctx := lmf.GetLMF()   │
  │                      │ session :=            │
  │                      │  ctx.RequestLocation  │
  │                      │   (imsi, method, qos, │
  │                      │    clientType)        │
  │                      ├──────────────────────►│
  │                      │                       │ run positioning
  │                      │  Session              │ method, store
  │                      │◄──────────────────────│ result
  │ LocationResult       │                       │
  │◄─────────────────────│                       │
```

LMF method-selection / positioning math is owned by `nf/lmf`; see
`docs/design/nf/lmf.md`.

## 6. Key types / public API

```go
type LocationResult struct {
    SessionID, State, Method, IMSI string
    Latitude, Longitude, Altitude, UncertaintyM *float64
    Confidence *int
}

func RequestLocation(imsi, method string, accuracyM, responseTimeS float64, clientType string) LocationResult
func GetLocation(sessionID string) *LocationResult
func CancelLocation(sessionID string)
func LocationHistory(imsi string, limit int) []map[string]any

func RegisterGnbPosition(gnbID string, lat, lon, alt float64)
func RegisterGnbAntenna(gnbID string, azimuthDeg, beamwidthDeg, downtiltDeg float64, numBeams int)
func AllocatePRS(gnbID string, frequencyLayer, periodicityMS, numRB, numSymbols, combSize int) *lmf.PRSResource
func GetPRSConfig(gnbID string) []*lmf.PRSResource
func DeactivatePRS(prsID int)

func Status() map[string]any
```

`clientType` values map to the LCS client classes referenced in
TS 23.273 §4.3.2 (`commercial` / `emergency` / `lawful_intercept` /
`value_added`) — `gmlc.go:46-48`.

## 7. What's not implemented

- **Ngmlc SBI** — TS 29.515 service operations not modelled. TODO
  marker at `gmlc.go:19-22`.
- **GMLC ↔ AMF Le procedure orchestration** — TS 23.273 §6 procedures
  (5GC-MT-LR, 5GC-MO-LR with deferred / area-event variants) are not
  modelled at this layer; the GMLC is a thin shim over the LMF
  Determine-Location call.
- **Authentication / authorization of the LCS client** — no policy
  check on `clientType` beyond the default-to-commercial fallback.

## 8. References (cited in source)

Verbatim from `nf/gmlc/`:

- TS 23.273 §4.3.2, §4.3.3, §4.4.1
- TS 29.515 (Ngmlc — TODO)
- TS 29.572 §5.2.2.2, §5.2.2.4
- TS 38.211 §7.4.1.7 (transitively, via LMF PRS calls)

---
*Last refreshed against commit `13a181d`.*

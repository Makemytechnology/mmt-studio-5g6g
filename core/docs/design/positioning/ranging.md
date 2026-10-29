# Ranging — Design Document

Ranging and Sidelink (SL) Positioning service — operator-side state
for the architectural roles in **TS 23.586 §4** and the session-level
procedures in **§5–§6**.

---

## Part A — Functional view

### A.1 What Ranging / SL Positioning is, in plain terms

Two UEs, talking directly over the **PC5 sidelink** (no Uu, no
network in the middle), measure how far apart they are, and the
direction one is from the other. Distance, azimuth, elevation —
device-to-device, with sub-metre accuracy in the best case.

This is **not** the network locating a UE (that's LCS, in
`nf/lmf/`). This is one UE measuring another UE.

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| Works **without coverage** | PC5 sidelink runs even out of cellular range — tunnels, basements, disasters. |
| Lower latency than network-side LCS | No round-trip through gNB / AMF / GMLC / LMF. UE asks UE; UE answers UE. |
| Privacy-friendly by construction | The network doesn't see the measurement payload — it only enforces the consent gate. |
| Enables **proximity-based** product features | Distance + direction is enough for "nearest-of", "leader-follower", "is X within 3 m of me". |
| Re-uses the V2X / ProSe sidelink infrastructure | If the operator already runs V2X or PIN, the radio is already there. |

### A.3 Customer use cases

| Use case | What the customer is buying |
|----------|------------------------------|
| **Indoor navigation** | Wayfinding in malls, hospitals, airports, factories where GNSS is dead and the operator doesn't want to build a Wi-Fi RTT mesh. |
| **Asset / worker tracking on a site** | Forklifts, tools, lone workers — relative position, no per-asset SIM and no GNSS module needed beyond the parent UE. |
| **Pedestrian / VRU safety** | Vehicle UE measures distance to pedestrian UE for collision avoidance (V2X-adjacent). |
| **AR / VR co-location** | Two headsets agree on relative geometry without a server round-trip. |
| **Public safety responders** | "Who is closest to the casualty?" answered on PC5 even when the cell is congested or down. |
| **Group-leader following** | Fleet of drones / robots tracks distance to a leader UE. |

### A.4 Actors and roles

```
   Source UE                             Target UE
   (asks "how far,                       (the one being
    in which direction,                   measured)
    is that other UE?")
         │                                    │
         │── PC5 sidelink ranging exchange ──▶│
         │   (RSPP — TS 38.305; not modelled)│
         │                                    │
   ┌─────▼────────────────────────────────────▼──────┐
   │   positioning/ranging  (this package)           │
   │                                                  │
   │   ranging_privacy   ── consent gate (§5.1)      │
   │   ranging_sessions  ── one row per Initiate     │
   │   ranging_results_log ── per measurement type   │
   │   ranging_anchors   ── pre-positioned SL Ref UE │
   └──────────────────────────────────────────────────┘
                            ▲
                            │ provisioning
                            │
                     ┌──────┴───────┐
                     │   Operator   │
                     │  (privacy +  │
                     │   anchors)   │
                     └──────────────┘
```

| Actor | Spec role | What they do here |
|-------|-----------|--------------------|
| **Source UE** | Initiator of the ranging request | Calls `InitiateRanging(sourceIMSI, targetIMSI, method)` |
| **Target UE** | The UE being measured | Subject of the consent gate (`ranging_privacy`) |
| **SL Reference UE / Anchor** | TS 23.586 §5.2 — pre-positioned UE used as a static landmark for multi-RTT trilateration | Provisioned via `CreateAnchor` |
| **Operator** | Authorises, persists, audits | Runs `SetPrivacy`, places anchors, queries result log |

### A.5 Operator workflow

```
   1.  Provision per-target privacy   SetPrivacy(IMSI, allow_all|deny_all|contacts_only,
                                                     [allowedContacts])
   2.  Place anchors (optional)       CreateAnchor(IMSI, lat, lon, alt, type)
                                      (multi-RTT needs ≥3 anchors with known coords)
   3.  Source UE asks                 InitiateRanging(srcIMSI, tgtIMSI, method)
                                      method ∈ {RTT, AoA, multi_RTT}
   4.  Privacy gate runs              checkPrivacy(targetIMSI)
                                        deny_all                    → reject
                                        contacts_only && src ∉ list → reject
                                        otherwise                    → admit
   5.  Measurement (simulated here)   distance / azimuth / elevation / accuracy
   6.  Result row written             ranging_results_log (one per measurement type)
   7.  Operator audits                ListSessions / GetSession / privacy reports
```

The privacy gate is the **only normative checkpoint** the operator
runs synchronously. Everything downstream is measurement + audit.

### A.6 Method choice — what the customer should pick

| Method | When to use | Trade-off |
|--------|-------------|-----------|
| `RTT` | Two UEs, distance only, no anchors needed. | No direction; line-of-sight bias. |
| `AoA` | One UE with an antenna array measuring direction to a peer. | Needs the array; no distance unless paired. |
| `multi_RTT` | High-accuracy 3D position relative to ≥3 anchors. | Requires anchor provisioning; best accuracy. |

### A.7 What is NOT in scope here

- **The actual radio measurement** — RSPP messages over PC5 per
  TS 23.586 §5.3.2 / TS 38.305. `InitiateRanging` synthesises
  plausible values for the dev/lab path; values are **simulated**,
  not spec-derived.
- **Network-side LCS** — that's `nf/lmf/` against TS 23.273. The two
  share no on-wire procedures; the link is conceptual ("network
  locates UE" vs. "UE locates UE").
- **PC5 link establishment** — assumed already up (V2X / ProSe
  bring-up).

---

## Part B — Design

### B.1 Architecture

```
   Source UE (PC5)             ranging pkg                    Target UE (PC5)
       │                          │                                 │
       │ InitiateRanging          │                                 │
       │ (sourceIMSI, target,     │                                 │
       │  method)                 │                                 │
       │─────────────────────────▶│                                 │
       │                          │ checkPrivacy(target)            │
       │                          │   SELECT FROM ranging_privacy   │
       │                          │   policy ∈ {allow_all,          │
       │                          │             deny_all,           │
       │                          │             contacts_only}      │
       │                          │                                 │
       │                          │ if denied: return error         │
       │                          │                                 │
       │                          │ INSERT ranging_sessions(active) │
       │                          │ simulateMeasurement(method)     │
       │                          │ UPDATE → completed              │
       │                          │ INSERT ranging_results_log ×4   │
       │                          │   (distance, azimuth,           │
       │                          │    elevation, accuracy)         │
       │◀── result ───────────────│                                 │
```

### B.2 Tables

| Table | Holds |
|-------|-------|
| `ranging_sessions` | one row per Initiate; result + status + timestamps |
| `ranging_anchors` | static SL Reference UEs (lat/lon/alt) |
| `ranging_results_log` | one row per measurement type per session |
| `ranging_privacy` | per-target UE consent (TS 23.586 §5.1) |

### B.3 File map

| File | Role |
|------|------|
| `positioning/ranging/ranging.go` | Types, privacy gate, simulator, all public API |
| `positioning/ranging/ranging_test.go` | Privacy + lifecycle + result tests |
| `webservice/app/routes_ranging.go` | REST surface `/api/ranging/*` |

### B.4 Wire / API surface

No spec wire-format implemented. The package speaks Go and SQL.
Spec context for the operations:

| Operation | TS 23.586 § |
|-----------|-------------|
| `InitiateRanging` | §5.3 Ranging/SL Positioning control; §6.8 procedures |
| `checkPrivacy` (internal) | §5.1 Authorization and Provisioning; §6.2 |
| `CreateAnchor` | §5.2 (SL Reference UE role) |
| Method strings | TS 38.305 RSPP "Ranging Method" — see TODO |

### B.5 Headline procedures

#### B.5.1 Authorisation gate

```go
// checkPrivacy at ranging.go:353
SELECT policy, allowed_contacts FROM ranging_privacy WHERE imsi = targetIMSI
  no row              → allow (implicit)
  policy='deny_all'   → deny
  policy='contacts_only':
      allowedContacts contains sourceIMSI → allow
      else                                 → deny
  policy='allow_all'  → allow
```

#### B.5.2 Method enum

| Method | Distance range (simulated) | Accuracy (simulated) |
|--------|----------------------------|----------------------|
| `RTT` | 0.5 .. 300 m | 0.1 .. 0.4 m |
| `AoA` | 1.0 .. 200 m | 0.5 .. 1.5 m |
| `multi_RTT` | 0.3 .. 500 m | 0.05 .. 0.20 m |

`simulateMeasurement` (`ranging.go:372`) picks values uniformly in
those ranges; this is dev-loop fixture data, not spec-derived.

### B.6 Key types / public API

```go
type Session struct {
    ID                                                int64
    SourceIMSI, TargetIMSI, Method, Status            string
    DistanceM, AzimuthDeg, ElevationDeg, AccuracyM    *float64
    CreatedAt                                         string
    CompletedAt                                       *string
}

type Anchor struct {
    ID                              int64
    IMSI                            string
    Latitude, Longitude, Altitude   float64
    AnchorType                      string
    Active                          int
    CreatedAt                       string
}

type ResultLog struct {
    ID, SessionID                int64
    MeasurementType, Unit, Timestamp string
    Value                        float64
}

type PrivacyEntry struct {
    ID              int64
    IMSI            string
    Policy          string  // allow_all | deny_all | contacts_only
    AllowedContacts *string // CSV of source IMSIs
    UpdatedAt       string
}

// Sessions
func InitiateRanging(sourceIMSI, targetIMSI, method string) (map[string]any, error) // ranging.go:121
func GetSession(id int64) (*Session, error)
func ListSessions(imsi, status string) ([]Session, error)
func CancelSession(id int64) error
func DeleteSession(id int64) error

// Anchors
func ListAnchors() ([]Anchor, error)
func CreateAnchor(imsi string, lat, lon, alt float64, anchorType string) (int64, error)
func DeleteAnchor(id int64) error

// Privacy (TS 23.586 §5.1)
func SetPrivacy(imsi, policy string, allowedContacts *string) error    // ranging.go:290
func GetPrivacy(imsi string) (*PrivacyEntry, error)
func ListPrivacy() ([]PrivacyEntry, error)
func DeletePrivacy(imsi string) error
```

### B.7 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| `ranging.go:31` | `TODO TS 38.305` — once the RAN positioning protocol PDF is loaded, anchor the method strings to the RSPP "Ranging Method" parameter set and validate accuracy ranges per measurement type. |
| `ranging.go:118` (header text) | Measurement values are simulated; the spec measurement transport (RSPP over PC5 per §5.3.2) is NOT modelled — only session-level state and result persistence. |

### B.8 References

Only specs cited in source:

- **TS 23.586** — Architectural enhancements for Ranging-based services and Sidelink Positioning
  - §4.1 General concept
  - §5.1 Authorization and Provisioning
  - §5.2 UE Discovery & Selection
  - §5.3 Ranging/SL Positioning control
  - §5.3.2 measurement transport (referenced; out of scope)
  - §6.2 (privacy / authorisation procedures)
  - §6.4 Procedures for UE Discovery
  - §6.8 Procedures of Ranging/SL Positioning control
- **TS 38.305** — NG-RAN positioning protocol (referenced by TODO; PDF not loaded)

Cross-link: `nf/lmf/` is the LCS / location-management peer for the
network-side positioning path; the umbrella positioning doc lives at
[`positioning.md`](./positioning.md). The two anchor different
positioning architectures — `nf/lmf/` is **TS 23.273** LCS over NR
positioning, while `positioning/ranging/` is **TS 23.586** UE-to-UE
Ranging/SL Positioning over PC5. They share no on-wire procedures;
the link is conceptual ("network-side LCS" vs. "sidelink direct
ranging").

---
*Last refreshed against commit `13a181d`.*

# ISAC — Design Document

Integrated Sensing and Communication on 5G — operator-side session
state for the Stage-1 sensing-service primitives in **TS 22.137**.

---

## Part A — Functional view

### A.1 What ISAC is, in plain terms

The 5G radio that already carries voice and data is also a radar.
The same waveform reflects off objects in the environment, and a
sensing-capable receiver can recover **range, velocity, presence,
shape, motion** from those reflections — without the target carrying
a UE, a tag, or any client at all.

ISAC packages that capability as a **service** the operator sells:
the customer asks the network *"is anything moving in this area, how
fast, and in which direction?"* and the network answers from the
RAN's own measurements.

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| New revenue on existing spectrum | Sell sensing as a value-add over the radio you already own. No second network, no client install. |
| Replaces dedicated radar / lidar / camera deployments | One pole instead of four. Lower CapEx and OpEx for the customer. |
| Targets are **device-free** | Works on intruders, vehicles, animals, weather — anything with a radar cross-section. |
| Privacy posture is better than cameras | No imagery captured; only kinematic features (range, velocity, occupancy). |

### A.3 Customer use cases (TS 22.137 §4.1)

| Use case (operator-defined enum) | What the customer is buying |
|-----------------------------------|------------------------------|
| `intrusion_detection` | Perimeter alarm without cameras: warehouse fence-line, substation yard, airport airside. |
| `presence_detection` | Room occupancy for HVAC / lighting; elderly fall-watch in care homes. |
| `object_tracking` | Vehicle / drone trajectory through a coverage area; asset flow on factory floors. |
| `environment_monitoring` | Crowd density at venues; rainfall / foliage / construction-site state. |
| `gesture_recognition` | Touchless control surfaces (industrial, automotive, accessibility). |

The strings above are operator-local enum keys — TS 22.137 only
enumerates use cases narratively (intruder detection, trajectory
tracing, collision avoidance, traffic management, health and activity
monitoring).

### A.4 Actors and what each one does

```
   Customer / Application                    Operator
   (presence alert,                          (sells the service,
    traffic counter,                         |  authorises consumers,
    fall detector)                           |  defines sessions)
        │                                    │
        │ ListData / LatestData              │ CreateSession
        │ (read)                             │ ActivateSession
        ▼                                    ▼
   ┌────────────────────────────────────────────────────┐
   │                   5GC + sensing/isac                  │
   │           (this package — session + data store)    │
   └────────────────────────────────────────────────────┘
          ▲                                  ▲
          │ ReportData                       │ measurement
          │ (sensing receiver)               │
   ┌────────────────────────────────────────────────────┐
   │   Radio access  ── sensing transmitter / receiver  │
   │     (gNB or sensing-capable UE)                    │
   └────────────────────────────────────────────────────┘
                       ▲
                       │ reflections
                       │
                ┌──────┴──────┐
                │   target    │  (no UE — person, vehicle, object)
                └─────────────┘
```

| Actor | Role | Touches this package via |
|-------|------|--------------------------|
| **Operator** | Defines the sensing session (type, area, resolution, interval); authorises consumers. | `CreateSession`, `ActivateSession`, `CompleteSession`, `CancelSession` |
| **Sensing receiver** (gNB / UE) | Generates measurements from reflections and posts them to the network. | `ReportData` |
| **Third-party application** | Reads results via the operator's exposure layer (NEF). | `ListData`, `LatestData` |
| **Sensing target** | The thing being sensed. **No 5G client; no API surface.** | (none — passive) |

### A.5 Operator workflow

```
   1.  Provision sensing area     (TAI or geofence — out of band)
   2.  CreateSession              type, target_area, resolution, report_interval_s
   3.  ActivateSession            opens the data window
   4.  (loop) ReportData          sensing receiver(s) push DataPoints
   5.  ListData / LatestData      consumer reads
   6.  CompleteSession   or       CancelSession
```

`ReportData` is admitted **only** when the session is in `active`
state — mid-window pushes against `created` / `completed` /
`cancelled` are rejected. This is how the operator gates billable
data against the time it actually authorised the session.

### A.6 What is NOT in scope here

- The radio-side sensing transmitter and receiver — that lives in
  RAN, below this layer.
- The third-party exposure wire format — that's NEF, above.
- Encryption / integrity of the sensing payload (TS 22.137 §5.2.4) —
  the row schema persists already-authorised data.
- A spec wire-format session API — TS 22.137 is **Stage-1**; the
  session lifecycle here is operator-local policy. When 3GPP
  publishes a Stage-2 ISAC architecture (currently a study item, not
  normative at the Rel-19 floor in `specs/3gpp/`), `CreateSession`
  is meant to be re-anchored to its canonical session-establishment
  procedure. That's the single open TODO.

---

## Part B — Design

### B.1 Architecture

```
   sensing transmitter / receiver        third-party (NEF)
              │                                   ▲
              │ ReportData                        │ ListData / LatestData
              ▼                                   │ (TS 22.137 §5.2.3)
   ┌──────────────────────────────────────────────┐
   │              sensing/isac (Go)                  │
   │                                              │
   │   isac_sessions  (lifecycle FSM)             │
   │     created → active → completed | cancelled │
   │                                              │
   │   isac_data      (one row per measurement)   │
   │     session_id, timestamp,                   │
   │     detected_objects, environmental, raw_data│
   └──────────────────────────────────────────────┘
              │  CreateSession / Activate         ▲
              ▼  (TS 22.137 §5.2.2)               │
        operator panel                            │
```

### B.2 File map

| File | Role |
|------|------|
| `sensing/isac/isac.go` | Types, FSM, CRUD over `isac_sessions` and `isac_data` |
| `sensing/isac/isac_test.go` | Lifecycle + invalid-input tests |
| `webservice/app/routes_isac.go` | REST surface `/api/isac/*` |

### B.3 Wire / API surface

No spec wire-format implemented. The package speaks Go and SQL only.
Spec context for the calls it persists:

| Function | TS 22.137 § | Role |
|----------|-------------|------|
| `CreateSession` / `ActivateSession` | §5.2.2 | "configure / authorize sensing transmitters and receivers" |
| `ReportData` | §5.2.1 (narrative) | "collect 3GPP sensing data from sensing receivers" |
| `ListData` / `LatestData` | §5.2.3 | network-exposure read path |
| (encryption / integrity) | §5.2.4 | **out of scope** — schema persists already-authorised data |

### B.4 Headline procedures

#### B.4.1 Lifecycle FSM

```
                ┌────────┐  ActivateSession  ┌────────┐  CompleteSession  ┌──────────┐
   CreateSession─▶│ created│──────────────────▶│ active │──────────────────▶│completed │
                └────┬───┘                   └────┬───┘                   └──────────┘
                     │ CancelSession              │ CancelSession
                     │                            │
                     ▼                            ▼
                ┌──────────────────────────────────────┐
                │             cancelled                 │
                └──────────────────────────────────────┘
```

`ReportData` is admitted **only** when the session is in `active`
state (`isac.go:244`); any other state returns an error.

#### B.4.2 Sensing-type validation

`validSensingTypes` (`isac.go:52`) is a closed set; rows with other
strings are rejected at insert time. The list is local policy, not
spec-mandated.

### B.5 Key types / public API

```go
type Session struct {
    ID              int64
    SensingType     string  // from validSensingTypes
    TargetArea      *string
    Resolution      *string
    ReportIntervalS int
    Status          string  // created | active | completed | cancelled
    CreatedAt       string
    CompletedAt     *string
}

type DataPoint struct {
    ID              int64
    SessionID       int64
    Timestamp       string
    DetectedObjects *string
    Environmental   *string
    RawData         *string
}

// Sessions
func CreateSession(sensingType, targetArea, resolution string, reportIntervalS int) (*Session, error)
func GetSession(id int64) (*Session, error)
func ListSessions(sensingType, status string) ([]Session, error)
func ActivateSession(id int64) (*Session, error)
func CancelSession(id int64) (*Session, error)
func CompleteSession(id int64) (*Session, error)
func DeleteSession(id int64) error

// Data
func ReportData(sessionID int64, detectedObjects, environmental, rawData *string) (*DataPoint, error)
func GetDataPoint(id int64) (*DataPoint, error)
func LatestData(sessionID int64) (*DataPoint, error)
func ListData(sessionID int64, limit int) ([]DataPoint, error)
```

### B.6 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| `isac.go:34` | `TODO TS 23.288` — when 3GPP publishes a Stage-2 ISAC architecture (currently a study item, not normative at the Rel-19 floor in `specs/3gpp/`), wire `CreateSession` to the canonical sensing-session establishment procedure and add the spec PDF. |

### B.7 References

Only specs cited in source:

- **TS 22.137** — Service requirements for 5G wireless sensing
  - §4.1 General (description; sensing-type narrative)
  - §5.1 Functional service description
  - §5.2.1 (narrative — sensing-data collection)
  - §5.2.2 Configuration and authorization
  - §5.2.3 Network exposure
  - §5.2.4 Security and privacy (out of scope — schema persists already-authorised data)

Stage-2 ISAC architecture (TS 23.288 study) is referenced in the
TODO but the PDF is not loaded; speccheck does not resolve it.

---
*Last refreshed against commit `13a181d`.*

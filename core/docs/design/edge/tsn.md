# TSN — Design Document

Time-Sensitive Networking integration with 5GS — operator-side state
for the **5GS-as-TSN-bridge** model defined in **TS 23.501 §5.27**.

---

## Part A — Functional view

### A.1 What 5G-TSN is, in plain terms

A factory floor — or a TV studio, or a power-substation, or a vehicle
backbone — runs IEEE 802.1 Time-Sensitive Networking on cabled
Ethernet. Every frame must arrive within a tightly bounded latency
budget, with bounded jitter, and the clocks at both ends must agree
to nanoseconds.

5G-TSN replaces a chunk of that cable with a 5G radio link **without
the TSN endpoints noticing**. To the TSN controller (CNC) and the
talker / listener endpoints, the 5GS looks like one more **IEEE
802.1 bridge**: frames go in one port, come out the other, time
synchronisation crosses transparently, latency stays bounded.

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| Sells 5G into industrial automation | Industry 4.0 / Industrial IoT customers need TSN guarantees; without 5G-TSN, 5G can't compete with cabled Ethernet for the strict-latency floor. |
| Removes cabling from moving / hard-to-cable assets | AGVs, robots, drones, rotating machinery — anything that can't be cabled becomes addressable. |
| One bridge model, many islands | A single 5G deployment can subsume many TSN islands (lines, cells, studios). Common provisioning surface. |
| Carries deterministic L2 alongside best-effort L3 | The same gNB / UPF carries the deterministic bridge traffic and the existing IT traffic. |
| Re-uses the operator's spectrum and OAM | No second wireless technology, no second OAM stack. |

### A.3 Customer use cases

| Use case | Profile |
|----------|---------|
| **Factory motion control** (PLC ↔ drive sync) | Sub-millisecond cycle, jitter-bounded; isochronous traffic class. |
| **Robotic cell coordination** | Multiple robots sharing one logical TSN domain. |
| **In-vehicle / in-train backbones** | TSN inside a vehicle, with parts replaced by a 5G uplink. |
| **Pro audio / video over IP** | Studio-grade A/V where AVB/TSN is already the spec; 5G frees up cable. |
| **Power substation automation (IEC 61850)** | Sampled values + GOOSE under tight time budgets. |
| **Smart-grid / DER coordination** | Synchronised control across geographically dispersed inverters / batteries. |

### A.4 Actors and roles

```
   TSN domain (talker side)                       TSN domain (listener side)
   (PLC, robot controller,                        (drive, actuator,
    A/V source, DER node)                          camera sink)
        │                                                  │
        │   IEEE 802.1 frames                              │
        ▼                                                  ▼
   ┌─────────┐                                       ┌──────────┐
   │ DS-TT   │─── UE side (Device-Side TSN          │  NW-TT   │
   └────┬────┘     Translator) — TS 23.501 §5.27.0  └─────┬────┘
        │                                                  │
        │  ┌────────── 5GS = IEEE 802.1 bridge ──────────┐ │
        │  │       (one bridge per tsn_bridges row)       │ │
        │  └───────────────────────────────────────────────┘
        │                       ▲                          │
        │                       │ TSCAI / TSCAC hints      │
        │                       │ (per-stream traffic      │
        │                       │  pattern + 5QI mapping)  │
        │                       │                          │
        │                  ┌────┴────────────┐             │
        │                  │  TSN AF / CNC   │             │
        │                  │ (centralised    │             │
        │                  │  network        │             │
        │                  │  controller)    │             │
        │                  └─────────────────┘             │
        │                                                  │
        └──── gPTP grandmaster sync (§5.27.1) ─────────────┘
```

| Actor | Role | Touches this package via |
|-------|------|--------------------------|
| **TSN talker / listener** | Industrial endpoint (PLC, drive, camera). | (none — opaque) |
| **DS-TT** | Device-Side TSN Translator on the UE; the TSN port the talker plugs into. | `Bridge.DSTTPort` |
| **NW-TT** | Network-Side TSN Translator at the UPF; the TSN port the listener plugs into. | `Bridge.NWTTPort` |
| **TSN AF / CNC** | Centralised Network Configurator pushes per-stream traffic patterns and gate schedules into the bridge. | `CreateStream`, `CreateGateSchedule` |
| **gPTP grandmaster** | Time source for the TSN domain; the 5GS bridge transports its sync transparently. | `ClockDomain.GMIdentity` |
| **Operator** | Registers the bridge instance, monitors clock state, audits streams. | `CreateBridge`, `ListBridges`, `UpdateClockStatus` |

### A.5 Operator workflow

```
   1.  Operator registers the bridge   CreateBridge(bridgeID, name,
                                                   dsTTPort, nwTTPort, vlanID)
                                       — declares "5GS = bridge X with these two ports"
   2.  CNC / TSN AF programmes streams CreateStream(bridgeID,
                                                   trafficClass, priority,
                                                   maxFrameSize, intervalUS,
                                                   mapped5QI, pdbMS)
                                       — TSCAI hints + QoS-flow mapping
   3.  gPTP comes up                   CreateClockDomain(domainID, gmIdentity, ...)
                                       UpdateClockStatus(id, "synced")
                                       — 5GS transports the grandmaster across the bridge
   4.  (optional) Gate schedules       CreateGateSchedule(streamID, gateState,
                                                         startNS, durationNS, cycleNS)
                                       — IEEE 802.1Qbv programming for testers
   5.  Traffic flows                   talker → DS-TT → 5GS QoS flow → NW-TT → listener
                                       — opaque to this package
   6.  Operator monitors               ListStreams, ListClockDomains, UpdateBridgeStatus
```

### A.6 Where the determinism actually comes from

| Knob | What it controls | Spec § |
|------|------------------|--------|
| `Stream.IntervalUS` | Cycle time the talker promises (e.g. 250 µs). | TS 23.501 §5.27.2 (TSCAI) |
| `Stream.MaxFrameSize` | Burst budget per cycle. | TS 23.501 §5.27.2 (TSCAI) |
| `Stream.Mapped5QI` | The 5G QoS Flow Identifier the radio scheduler honours — picks PDB, priority, GBR profile. | TS 23.501 §5.27.3 |
| `Stream.PDBMS` | Packet Delay Budget the SMF programmes through PCC. | TS 23.501 §5.27.3 |
| `ClockDomain` | Which gPTP grandmaster the bridge is following. | TS 23.501 §5.27.1 |
| Gate schedules | 802.1Qbv per-priority gate timing on the TSN ports (operator-visible only). | IEEE 802.1Qbv |

### A.7 What is NOT in scope here

- **Programming the QoS rules on the wire** — that's `nf/smf/` and
  `nf/upf/` (PFCP TSC Assistance Information / Container) consuming
  what we persist.
- **The TSN AF wire surface** — the AF→TSN-AF / CNC interaction is
  outside the 5GC and not modelled.
- **5G System Bridge delay reporting** (TS 23.501 §5.27.5) — that's
  dynamic, AF-driven, and the open TODO in the package.
- **Gate-state scheduling at the UPF** — the 5GC does not normatively
  programme 802.1Qbv gate state; the rows here are tester fixtures
  for reasoning about timing, not enforcement.

---

## Part B — Design

### B.1 Architecture

```
   TSN domain                                  TSN domain
   (talker side)                               (listener side)
        │                                            │
   ┌────▼────┐         ┌──────────────────┐    ┌─────▼────┐
   │ DS-TT   │◀── PC5/Uu                  │    │  NW-TT   │
   └────┬────┘    5GS = IEEE 802.1 bridge │    └─────┬────┘
        │         (TS 23.501 §5.27.0)     │          │
        │                                            │
        └────────────── tsn_bridges ─────────────────┘
                          │
            ┌─────────────┼─────────────┬───────────────┐
            ▼             ▼             ▼               ▼
       tsn_streams   tsn_clock_    tsn_gate_      (TS 23.501
       (TSCAI +      domains       schedules      §5.27.5 BR delay
        5QI/PDB)     (gPTP GM)     (802.1Qbv)     — TODO)
       §5.27.2/.3    §5.27.1
```

### B.2 Field → spec map

| Field | Spec § |
|-------|--------|
| `Bridge.DSTTPort` / `NWTTPort` | TS 23.501 §5.27.0 (DS-TT / NW-TT translator ports) |
| `Stream.IntervalUS` / `MaxFrameSize` | TS 23.501 §5.27.2 (TSCAI / TSCAC traffic-pattern hints) |
| `Stream.Mapped5QI` / `PDBMS` | TS 23.501 §5.27.3 (TSC QoS Flows) |
| `ClockDomain.GMIdentity` | TS 23.501 §5.27.1 (gPTP grandmaster identity) |
| `GateSchedule.*` | IEEE 802.1Qbv terms (referenced by §5.27.2; not 3GPP-defined) |

### B.3 File map

| File | Role |
|------|------|
| `edge/tsn/tsn.go` | Types + CRUD over four tables (`tsn_bridges`, `tsn_streams`, `tsn_clock_domains`, `tsn_gate_schedules`) |
| `edge/tsn/tsn_test.go` | CRUD + clock-status + gate-schedule tests |

### B.4 Wire / API surface

No spec wire format implemented. The package speaks Go and SQL.
The 5GC-internal procedures whose **outcome** is persisted here are
TS 23.502 / TS 29.244 PFCP IEs (TSC Assistance Information,
TSC Assistance Container) — those rules are built and dispatched
from `nf/smf/` and `nf/upf/`, not here.

### B.5 Headline procedures

#### B.5.1 Bridge registration

```
operator panel             tsn pkg
   │                          │
   │── CreateBridge(          │
   │     bridgeID,            │
   │     dsTTPort, nwTTPort,  │   // TS 23.501 §5.27.0
   │     vlanID) ────────────▶│
   │                          │ INSERT tsn_bridges (status='active')
   │◀── bridge id ────────────│
```

#### B.5.2 Stream provisioning (TSCAI hints)

```
TSN AF / CNC                tsn pkg
   │── CreateStream(         │
   │     bridgeID,           │
   │     trafficClass,       │
   │     priority,           │
   │     maxFrameSize,       │   // TS 23.501 §5.27.2 TSCAI
   │     intervalUS,         │
   │     mapped5QI, pdbMS)   │   // TS 23.501 §5.27.3 TSC QoS
   │ ───────────────────────▶│
   │                         │ INSERT tsn_streams
   │◀── stream id ───────────│
```

The per-stream values are subsequently consumed by the SMF when it
builds the PFCP rules for the corresponding QoS flow.

#### B.5.3 gPTP clock-domain lifecycle

```
   freerun ──UpdateClockStatus("synced")──▶ synced
              (stamps last_sync_at)         │
                                            │
                  UpdateClockStatus("freerun")
                                            ▼
                                         freerun
```

Holdover / sync-accuracy values are operator-visible only (no
spec-derived validation here).

### B.6 Key types / public API

```go
type Bridge struct {
    ID         int64
    BridgeID, Name, DSTTPort, NWTTPort, Status, CreatedAt string
    VLANID     *int
}

type Stream struct {
    ID, BridgeID                              int64
    StreamID                                  string
    TrafficClass, Priority, MaxFrameSize, IntervalUS int
    Mapped5QI                                 *int
    PDBMS                                     *float64
    CreatedAt                                 string
}

type ClockDomain struct {
    ID                                         int64
    DomainID, GMIdentity, Status, CreatedAt    string
    SyncAccuracyNS, HoldoverCapabilityS        int
    LastSyncAt                                 *string
}

type GateSchedule struct {
    ID, StreamID                                int64
    GateState                                   string
    StartTimeNS, DurationNS, CycleTimeNS        int64
}

// Bridges
func ListBridges() ([]Bridge, error)
func CreateBridge(bridgeID, name, dsTTPort, nwTTPort string, vlanID *int) (int64, error)
func UpdateBridgeStatus(id int64, status string) error
func DeleteBridge(id int64) error

// Streams
func ListStreams(bridgeID int64) ([]Stream, error)
func CreateStream(bridgeID int64, streamID string, trafficClass, priority,
    maxFrameSize, intervalUS int, mapped5QI *int, pdbMS *float64) (int64, error)
func DeleteStream(id int64) error

// Clock domains
func ListClockDomains() ([]ClockDomain, error)
func CreateClockDomain(domainID, gmIdentity string, syncAccuracyNS, holdoverCapS int) (int64, error)
func UpdateClockStatus(id int64, status string) error  // tsn.go:266 (stamps last_sync_at on 'synced')

// Gate schedules
func ListGateSchedules(streamID int64) ([]GateSchedule, error)
func CreateGateSchedule(streamID int64, gateState string,
    startTimeNS, durationNS, cycleTimeNS int64) (int64, error)
```

### B.7 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| `tsn.go:33` | `TODO TS 23.501 §5.27.5` — wire 5G System Bridge delay reporting once the AF→TSN-AF interaction surface is anchored. |

### B.8 References

Only specs cited in source:

- **TS 23.501** — System Architecture for the 5G System
  - §5.27.0 General (5GS as IEEE 802.1 bridge; DS-TT / NW-TT)
  - §5.27.1 Time Synchronization
  - §5.27.2 TSC Assistance Information (TSCAI) and TSC Assistance Container (TSCAC)
  - §5.27.3 Support for TSC QoS Flows
  - §5.27.5 5G System Bridge delay (out of scope; TODO)
- IEEE 802.1Qbv (gate schedule terminology, referenced only)

---
*Last refreshed against commit `13a181d`.*

# frmcs — Future Railway Mobile Communication System

## 1. Role / scope

`services/frmcs/` is the FRMCS application layer riding on top of the
3GPP MCX family — the UIC-led successor to GSM-R, standardised jointly
by UIC and 3GPP. Voice / video / data services are inherited from
MCPTT / MCVideo / MCData by reference (this package never reimplements
them), with railway-specific additions:

- **Functional aliases** — role-based addressing (driver of
  train 8421, controller of region X), bound at logon.
- **Railway Emergency Call (REC)** — broadcast emergency-priority
  voice call inheriting GSM-R REC, anchored on MCPTT emergency group
  call (TS 24.379 §6.2.8.1).
- **Shunting groups** — small driver+ground groups using MCPTT
  off-network (PC5) per TS 24.379 §10.2.2.

## 2. Architecture

```
                ┌─────────────────────────────────────────┐
                │          services/frmcs/                │
                │                                         │
                │   frmcs.go     Service{Domain}          │
                │                                         │
                │   common/     FunctionalAlias           │
                │               ServiceClass {REC, …}     │
                │                                         │
                │   voice/      REC{CallID, Initiator,    │
                │               Floor *FloorController}   │
                │                                         │
                │   shunting/   Group{GroupID, Members,   │
                │               call *OffNetCall}         │
                └────────────┬────────────────────────────┘
                             │ (delegates)
                             ▼
                ┌─────────────────────────────────────────┐
                │       services/mcx/mcptt/               │
                │   FloorController (TS 24.380 §6.3.5)    │
                │   OffNetCall      (TS 24.379 §10.2.2)   │
                │   PriorityEmergency = 1                 │
                └─────────────────────────────────────────┘
```

## 3. File map

| File | LOC | Role |
|------|-----|------|
| `frmcs.go` | 73 | `Service{Domain}` + `New(domain)` |
| `common/common.go` | 65 | FunctionalAlias, ServiceClass {REC, Urgent, Assured, Business} |
| `voice/rec.go` | 119 | REC struct, `InitiateREC`, `Join`, `Release` |
| `shunting/shunting.go` | 167 | Group struct, off-network FSM wrapper |
| `voice/webservice/` | — | HTTP / WebSocket fan-out (panel) |

Test files: `common_test.go`, `rec_test.go`, `shunting_test.go`.

## 4. Wire / API surface

There is no FRMCS-specific wire codec emitted from this package. All
on-the-wire signalling flows through MCPTT:

| FRMCS verb | Maps to | Spec § |
|------------|---------|--------|
| `voice.InitiateREC` | MCPTT emergency group call SIP INVITE | TS 24.379 §6.2.8.1.1 |
| REC priority on wire | Resource-Priority header | TS 24.379 §6.2.8.1.2 / .15 |
| REC floor preempt | `FloorController` PriorityEmergency | TS 24.380 §4.1.1.4 |
| `shunting.InitiateCall` | Off-network GROUP CALL ANNOUNCEMENT | TS 24.379 §10.2.2.4.3 |
| Shunting FSM | `OffNetCall` 7-state machine S1..S7 | TS 24.379 §10.2.2.3 |

REC and shunting call IDs / SDP / participant IDs are passed verbatim
into MCPTT — FRMCS contributes the role identity (FunctionalAlias)
and the priority assignment, not the wire frames.

## 5. Headline procedures

**REC initiation** (`voice.InitiateREC`, `voice/rec.go:88-95`).
Realises TS 22.289 §4.4.1 — emergency calls established on demand
with priority guaranteeing call success. Implementation:

1. `mcptt.NewFloorController(callID)` — fresh floor server.
2. `fc.AddParticipant(initiator, PriorityEmergency)` — emergency rank.
3. `fc.RequestFloor(initiator, &PriorityEmergency)` — preempt-override
   any existing holder per TS 24.380 §4.1.1.4.
4. Returns `*REC{CallID, Initiator, Floor}`.

The actual MCPTT INVITE with the emergency-namespace
Resource-Priority header (TS 24.379 §6.2.8.1.1 / .2) is **not** yet
emitted — see TODO at `voice/rec.go:77-82`. Today only the in-process
floor controller is configured.

**REC join** (`Join`). Late joiners inherit emergency priority per
TS 24.380 §4.1.1.4 — the joiner's effective priority while the call
is in §6.2.8.1 emergency state.

**Shunting group** (`shunting.New`, `shunting/shunting.go:87-99`).
Wraps `mcptt.NewOffNetCall(groupID, local, cfg, sendWire)` — one
local FSM per radio. The shunting Group adds:

- Group ID + members []FunctionalAlias bookkeeping.
- `InitiateCall(callID, sdp)` → S1 → S2 transition (TS 24.379
  §10.2.2.4.3 originator side).
- `ReceiveAnnouncement(...)` → routes to underlying FSM with
  confirm / ack-required flags.
- `Accept` / `Reject` / `ReleaseCall` → FSM verbs.
- `Snapshot()` adds `shunting_group`, `local_alias`, `members`
  on top of the FSM snapshot.

**FRMCS service entry** (`frmcs.go:69-72`). `New(domain)` is a
trivial constructor — the FRMCS Service is mostly a namespace today.

## 6. Key types

```go
// common/common.go
type FunctionalAlias string
type ServiceClass    int  // ServiceREC, ServiceUrgent, ServiceAssured, ServiceBusiness

// voice/rec.go
type REC {
    CallID    string
    Initiator common.FunctionalAlias
    Floor     *mcptt.FloorController
}

// shunting/shunting.go
type Group {
    GroupID  string
    Members  []common.FunctionalAlias
    call     *mcptt.OffNetCall
    local    common.FunctionalAlias
}
```

## 7. Stubs / TODOs

From source:

| Location | Spec | Note |
|----------|------|------|
| `voice/rec.go:77-82` | TS 24.379 §6.2.8.1.1 + .2 | Emit MCPTT INVITE with emergency Resource-Priority header (depends on `BuildMCXInvite` MCPTT MIME body — open in `services/mcx/mcptt/mcptt.go:198`) |
| `voice/rec.go:85-87` | TS 24.379 §6.2.8.1.3 | In-progress emergency state cancellation (re-INVITE that returns to non-emergency without tearing down the call) |
| `voice/rec.go:110-114` | TS 24.379 §6.2.8.1.3 | `Release()` always tears down — needs cancel-without-release |
| `common/common.go:41-44` | UIC FRS / SRS | Canonical FunctionalAlias schema (only enforce non-empty today) |
| `common/common.go:53-57` | UIC FRS | Full UIC priority hierarchy (collapsed to 4 classes) |
| `shunting/shunting.go:37-41` | UIC FRS / SRS | "Shunting mode indication" wire flag |
| `shunting/shunting.go:43-47` | TS 24.379 §10.2.2.4.6 | Merge of off-network calls |

UIC FRS / SRS / On-Board / FIS / FFFIS PDFs are not yet in-tree, so
all UIC-specific gaps are tagged TODO without §-cite.

## 8. References

- **TS 22.289** §4.4.1 / §4.4.2 — Mobile communication system for
  railways (priority stack, emergency-call requirement)
- **TS 23.289** §4.3.3 / §4.3.4 / §4.3.5 — Mission Critical services
  over 5GS (QoS for MCPTT / MCVideo / MCData)
- **TS 24.379** §6.2.8.1 / .1 / .2 / .3 / .15 — MCPTT emergency
  group call (call control)
- **TS 24.379** §10.2.2 / .3 / .4.3 / .4.6 — Off-network MCPTT
- **TS 24.380** §4.1.1.4 — On-network effective priority
  (preempt-override outcome used by REC)
- **TS 24.380** §6.3.3 — Floor server release procedures
- TS 24.379 / 24.380 / 33.180 etc. — MCX family (inherited by reference)

UIC documents (not in-tree): FRMCS FRS, SRS, On-Board architecture,
FIS / FFFIS interface specs.

---

*Last refreshed against commit `13a181d`.*

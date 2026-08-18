# mcx — Mission Critical Services

## 1. Role / scope

`services/mcx/` implements the 3GPP MCX family — MCPTT (push-to-talk),
MCVideo, MCData (SDS / FD / message store) — sitting on a shared MCX
common services tier (TS 23.280 §7). The package surfaces:

- **User + group registry** (`mcx.go`) — `mcx_user_profiles` and
  `mcx_groups` rows, auto-provisioned by the GMM auth-hook on UE
  provisioning.
- **MCX common** (`common/`) — TS 23.280 §8.1 identities (MC ID, MC
  service ID, MC service group ID), priority constants & ordering,
  TS 33.180 §5.2 / §7.3 group key generation, MCX config envelope
  built into UEs by `GenerateUEConfig`.
- **MCPTT** (`mcptt/`) — Floor controller (TS 24.380 §6.3.5),
  on-network call coordinator binding `libs/sip` SipDialog +
  FloorController, off-network call FSM (TS 24.379 §10.2.2 — 7 states
  S1..S7, 6 timers TFG1..TFG6).
- **MCVideo** (`mcvideo/`) — TransmissionController (the §7.7
  transmission-of-floor analogue), single-transmitter model.
- **MCData** (`mcdata/`) — SDS short messages (TS 23.282 §7.4), File
  Distribution (§7.5), message store (§7.13).
- **Signaling** (`signaling/`) — WebSocket bridge event names
  (mirror TS 24.380 §6.3.5 floor states for UI replay).

The package never reimplements SIP — it composes `libs/sip` (RFC 3261
dialog FSM) and `libs/fsm` (event-loop machine).

## 2. Architecture

```
                ┌──────────────────────────────────────────────────┐
                │             services/mcx/mcx.go                  │
                │   User / Group registry (mcx_user_profiles,      │
                │   mcx_groups)  +  GetOrCreateUser hook           │
                └─────────────────┬────────────────────────────────┘
                                  │
   ┌──────────────────────────────┼─────────────────────────────────┐
   │                              │                                  │
   ▼                              ▼                                  ▼
┌──────────────┐       ┌──────────────────────┐         ┌────────────────────┐
│ common/      │       │ mcptt/               │         │ mcvideo/           │
│  TS 23.280   │       │  TS 24.379 (call ctl)│         │  TS 23.281 §7.7    │
│   §8.1 IDs   │       │  TS 24.380 (floor)   │         │  Transmission      │
│  TS 33.180   │       │                      │         │  Controller        │
│   §5.2/§7.3  │       │  FloorController     │         │   (single tx)      │
│   GMK        │       │   (libs/fsm)         │         │                    │
│  Group/User  │       │                      │         └─────────┬──────────┘
│   manager    │       │  OnNetCall           │                   │
│  Priority    │       │   = SipDialog +      │         ┌─────────▼──────────┐
│   Emergency  │       │     FloorController  │         │ mcdata/            │
│   ↓ Normal   │       │                      │         │  TS 23.282         │
└──────────────┘       │  OffNetCall (PC5)    │         │   §7.4 SDS         │
                       │   7 states / 6 timers│         │   §7.5 FD          │
                       │   TFG1..TFG6         │         │   §7.13 store      │
                       │                      │         │                    │
                       │  GlobalFloorMgr      │         │  mcx_messages      │
                       └──────────┬───────────┘         └────────────────────┘
                                  │
                                  ▼
                       ┌──────────────────────┐
                       │ signaling/           │
                       │  WebSocket events    │
                       │   floor_granted      │
                       │   floor_taken        │
                       │   transmission_*     │
                       │   message_received   │
                       └──────────────────────┘
```

## 3. File map

| File | LOC | Role |
|------|-----|------|
| `mcx.go` | 169 | User/Group registry; in-memory call list; `GetOrCreateUser` for GMM auth-hook |
| `common/common.go` | 297 | Identities (MC service ID etc.), priority constants, GMK generator, Config envelope, group/user CRUD |
| `mcptt/mcptt.go` | 264 | MCPTT call lifecycle, FloorManager singleton, MCX SIP builder helpers |
| `mcptt/floor.go` | 480 | Floor server FSM (TS 24.380 §6.3.5 — 3-state collapsed model + per-participant projection) |
| `mcptt/on_net_call.go` | 187 | On-network coordinator: SipDialog + FloorController binding |
| `mcptt/offnet_call.go` | 516 | Off-network FSM (TS 24.379 §10.2.2 — 7 states S1..S7, 6 timers TFG1..TFG6) |
| `mcvideo/mcvideo.go` | 192 | TransmissionController + VideoCall registry |
| `mcdata/mcdata.go` | 185 | SDS / FD / message store (SQL `mcx_messages`) |
| `signaling/signaling.go` | 80 | WebSocket event names + JSON encoder |

Test coverage: `mcptt/floor_test.go`, `offnet_call_test.go`,
`on_net_call_test.go`, `mcvideo/mcvideo_test.go`,
`signaling/signaling_test.go`, `common/common_test.go`. Total ~ 3.5k
LOC.

## 4. Wire / API surface

### MCX Common (TS 23.280 §8)

| Function | Spec § | Notes |
|----------|--------|-------|
| `IMSIToMCPTTID(imsi)` | §8.1.2 | Returns `mcptt:<imsi>@<MCXDomain>` (TODO §8.3.1: replace with IdMS-mapped sip: form) |
| `MCPTTIDToIMSI(id)` | §8.1.2 | Inverse |
| `ValidateMCPTTID(id)` | §8.1.2 | Enforces user@domain shape |
| `EffectivePriority(userPri, callPri, emergency)` | local-policy | Emergency wins; else `min(user, call)` |
| `CanPreempt(req, holder)` | local-policy | Emergency always preempts; else lower numeric wins |
| `GenerateGroupKey()` | TS 33.180 §7.3 | 256-bit random GMK, base64 |
| `RotateGroupKey(gid)` | TS 33.180 §5.2 | Refresh + persist |
| `CreateMCXGroup(name, type, max, pri)` | TS 23.280 §8.1.3.1 / §10.2.2 | Insert `mcx_groups` row |
| `JoinGroup(gid, mcpttID, role)` | TS 23.280 §10.2 | Validates group + cap; inserts member |
| `GenerateUEConfig(mcpttID, host)` | — | Returns the UE-side config payload {servers (REST/WS/SIP/RTP), mcptt_id, groups} |

Priority constants (`common/common.go:115-122`): `PriorityEmergency=1`,
`PriorityImminentPeril=2`, `PriorityHigh=3`, `PriorityNormal=5`,
`PriorityLow=7`, `PriorityBackground=9`, `PreemptThreshold=3`.

### MCPTT call control (TS 24.379 — Stage 3 SIP)

| Function | Spec § | Notes |
|----------|--------|-------|
| `StartGroupCall(gid, init, pri)` | (in-mem) | Legacy in-memory `Call` struct (`mcptt.go:107-112`) |
| `InitiateGroupCall(originator, gid, emergency)` | TS 23.379 / §6.2 | DB-backed via `mcx_active_calls`; spawns FloorController, adds members at PriorityNormal |
| `InitiatePrivateCall(orig, target, emergency)` | TS 23.379 | 1:1 variant |
| `EndCall(callID)` | TS 24.380 §6.3.3 | State → released, removes FloorController |
| `JoinCall(cid, mcpttID)` / `LeaveCall(cid, mcpttID)` | TS 23.280 §10.2 | Floor participant add/remove |
| `BuildMCXInvite / BuildMCXBye / BuildMCX200OK` | TS 24.379 §6.2 | Minimal SIP fields — MCPTT MIME body / Resource-Priority / feature-tag NOT yet stamped (TODO `mcptt.go:198-202`) |

### Floor controller (TS 24.380 §6.3.5)

`FloorController` is a libs/fsm machine with 3 controller-level states
(`mcptt/floor.go:42-45`), projecting to 5 per-participant states
(§6.3.5.2/.3/.4/.5/.9). Public API (`floor.go:155-205`):

| Method | Effect |
|--------|--------|
| `AddParticipant(id, priority)` | Adds to participants map; no state change |
| `RemoveParticipant(id)` | If was holder → `doRelease`; drops from queue |
| `RequestFloor(id, *priority)` | Returns `{result: granted/preempted/queued/denied/error}` |
| `ReleaseFloor(id)` | Returns `{result: released}` and grants next queued |
| `GetStatus()` | Snapshot |
| `Stop()` | Idempotent FSM stop |

Floor-request outcomes (`floor.go:284-340`): from `StateIdle` →
grant + transition to `StateTaken`; from `StateTaken`, holder
re-request is idempotent grant; non-holder request runs `canPreempt`
— preempt-override on win (TS 24.380 §4.1.1.4 outcome 1) or queue
(`§6.3.5.4` U: not permitted and Floor Taken, capped at
`MaxFloorQueue=10`).

### On-network coordinator

`OnNetCall = SipDialog + FloorController` (`mcptt/on_net_call.go`):

| Function | Spec § | Notes |
|----------|--------|-------|
| `ProcessInviteForGroupCall(invite, localTag, gid, parts)` | §6.3.2.2 | UAS path: build dialog from INVITE + new FloorController + add participants; returns 200 OK |
| `ProcessInviteForPrivateCall(invite, localTag, orig, tgt)` | §6.3.2.2 | 1:1 variant |
| `Reject(invite, code, reason)` | — | Pre-establishment refusal |
| `(*OnNetCall).HandleBye(bye)` | §6.3.3 | Tears down dialog + floor; 200 OK |
| `(*OnNetCall).Release()` | §6.3.3 | Idempotent |
| `(*OnNetCall).State()` | — | `init / early / active / terminated / released` (mapped from dialog state) |

### Off-network FSM (TS 24.379 §10.2.2)

7-state per-(group, user) FSM in `mcptt/offnet_call.go`. States
(`offnet_call.go:60-68`):

| State | Spec § | Description |
|-------|--------|-------------|
| `S1StartStop` | §10.2.2.3.1 | Start-stop |
| `S2WaitAnnounce` | §10.2.2.3.2 | Waiting for call announcement |
| `S3InCall` | §10.2.2.3.3 | Part of ongoing call |
| `S4PendingNoConfirm` | §10.2.2.3.4 | Pending user action without confirm |
| `S5PendingConfirm` | §10.2.2.3.5 | Pending user action with confirm |
| `S6Ignoring` | §10.2.2.3.6 | Ignoring incoming announcements |
| `S7PostRelease` | §10.2.2.3.7 | Waiting for announcement after release |

Timers (`offnet_call.go:114-120`) — defaults are placeholders, real
values come from operator policy / TS 24.379 Annex F:
`TFG1=3s` wait-for-announcement,
`TFG2=4s` periodic announcement interval,
`TFG3=10s` post-release cooldown,
`TFG4=15s` pending user action,
`TFG5=20s` confirm-indication display,
`TFG6=30min` max call duration.

Public verbs (`mcptt/offnet_call.go`):

| Method | Edge | Spec § |
|--------|------|--------|
| `InitiateCall(callID, sdp)` | S1 → S2 | §10.2.2.4.3 originator |
| `ReceiveAnnouncement(callID, originator, sdp, withConfirm, ackRequired)` | S1 → {S3 / S4 / S5}; S2 → S3 | §10.2.2.4.3 |
| `AcceptCall()` | S4/S5 → S3 | |
| `RejectCall()` | S4/S5 → S6 | |
| `ReleaseCall()` | S3 → S7 | |
| `Snapshot()` | — | State + sub-FSM info |

### MCVideo (TS 23.281)

`TransmissionController` (`mcvideo/mcvideo.go:59-105`) — single-
transmitter floor analogue. Public verbs:

| Method | Result |
|--------|--------|
| `AddParticipant(id) / RemoveParticipant(id)` | Bookkeeping; if leaver was transmitter → emit `transmission_released` |
| `RequestTransmission(id)` | `granted` if no current transmitter; `denied` (busy) if other; `already_transmitting` if self; `error` if not participant |
| `ReleaseTransmission(id)` | `released` if `id == Transmitter`; `error` otherwise |
| `GetStatus()` | Snapshot |

Plus `InitiateVideoGroupCall / InitiateVideoPrivateCall / EndVideoCall`
keyed by `vcall-N` IDs.

### MCData (TS 23.282)

| Function | Spec § | Notes |
|----------|--------|-------|
| `SendPrivateMessage(sender, rcpt, content)` | §7.4 SDS | Inserts `mcx_messages` row, type=`sds`, recipient set |
| `SendGroupMessage(sender, gid, content)` | §7.4 | Inserts row with `group_id` |
| `UploadFile(sender, data, name, *rcpt, *gid)` | §7.5 FD | Writes `/tmp/mcx_files/<msgID>_<name>`; logs metadata + size |
| `GetConversation(*gid, *sender, limit)` | §7.13 | Default limit 100 |
| `GetMessageByID(id)` | §7.13.1 | Single record |
| `GetFilePath(id)` | §7.5 | Returns local file path (HTTP wrapper exposes URL) |
| `MarkDelivered(id)` | §7.4 | Set `delivered=1` |

### Signaling bridge (`signaling/`)

WebSocket-side event names (UI-facing, not 3GPP), but mirror floor-
server states for verbatim replay (`signaling.go:35-56`):
`floor_granted / floor_denied / floor_released / floor_preempted /
floor_queued / call_incoming / call_ended / participant_joined /
participant_left / video_call_incoming / video_call_ended /
transmission_granted / transmission_released / message_received /
file_received / emergency_alert` plus client → server actions
`floor_request / floor_release / transmission_request /
transmission_release`.

## 5. Headline procedures

### Group call setup (on-network, MCPTT)

```
Originator → MCPTT AS: SIP INVITE for group <gid>
                       (BuildMCXInvite — minimal; full MCPTT MIME body
                        TS 24.379 §6.3.2.2.9 + Resource-Priority
                        TS 24.379 §6.2.8.1.15 + feature-tag
                        TS 24.379 §6.2.4 are TODO)
   ↓ ProcessInviteForGroupCall(invite, localTag, gid, members)
   ↓   sip.NewDialogFromRequest(invite, localTag) → SipDialog
   ↓   d.On2xx(buildAccept200)
   ↓   NewFloorController(callID)
   ↓   AddParticipant(member, PriorityNormal)  ×N        §6.3.2.2
   ↓   call = OnNetCall{Dialog, Floor}
   ↓ Return (call, 200 OK)
MCPTT AS → originator: 200 OK + To-tag stamped
   originator → AS: ACK (libs/sip resolves dialog to Confirmed)

   …
   originator: floor.RequestFloor(id, &PriorityNormal)
              StateIdle + onRequest → doGrant, send Floor Granted    §6.3.5.3.5
              + Floor Taken to other participants                    §6.3.5.3.3
              transition StateIdle → StateTaken
              (recordFloorEvent in mcx_floor_history)

   …
   any UE → AS: BYE
   AS: call.HandleBye(bye)
   ↓ call.Release() → Dialog.Terminate() + Floor.Stop()              §6.3.3
   AS → UE: 200 OK
```

### Floor preemption (TS 24.380 §4.1.1.4 outcome 1: preempt-override)

```
holder = X at priority 5;  state = StateTaken
emergency-call requester E at priority 1
   ↓ floor.RequestFloor(E, &PriorityEmergency)
   ↓ onRequest case StateTaken:
   ↓   canPreempt(1, 5)? → yes (PriorityEmergency)
   ↓   doPreempt(E)
   ↓     old = X
   ↓     Holder = E; HasFloor = true; emit floor_granted to E
   ↓     X.HasFloor = false; emit floor_preempted to X
   ↓     remain in StateTaken (holder swap)
   ↓ Reply: {result: preempted, mcptt_id: E, preempted: X}
```

The remaining six §4.1.1.4 inputs (user-priority lookup,
participant-type, call-type, etc.) and the preempt-revoke outcome
(§6.3.5.6) are TODO at `mcptt.go:65-69`.

### Off-network call setup (TS 24.379 §10.2.2.4.3)

```
A: NewOffNetCall(gid, A, cfg, sendWire) → state = S1StartStop
A: c.InitiateCall(callID, sdp)
   ↓ U:initiate event
   ↓ S1 → S2WaitAnnounce; arm TFG1
   ↓ sendWire("GROUP_CALL_PROBE/ANNOUNCEMENT", payload)

B: c.ReceiveAnnouncement(callID, A, sdp, withConfirm=false, ackReqd=false)
   ↓ R:Announcement (ack-not-required) event
   ↓ S1 → S3InCall (immediate join)

A: TFG1 fires (or B's announcement reaches A)
   ↓ S2 → S3InCall; cancel TFG1; arm TFG6 (max call duration)

A: U:release
   ↓ S3 → S7PostRelease; cancel TFG6; arm TFG3
   ↓ TFG3 fires → S1
```

### Group key rotation (TS 33.180 §5.2)

`RotateGroupKey(gid)` (`common/common.go:187-191`) generates a fresh
GMK via `GenerateGroupKey` (32-byte random, base64) and persists into
`mcx_groups.encryption_key`. The MIKEY-SAKKE per-member wrapping
(TS 33.180 §5.2.1) and the per-call key derivation chain (GMK →
GMK-ID → MKFC → SRTP master key, §7.4) are TODO at
`common/common.go:156-164`.

### MCData SDS round-trip

```
Sender: SendPrivateMessage(sender, rcpt, content)
   ↓ INSERT mcx_messages (msg_type='sds', delivered=0, ...)
   ↓ Returns {message_id, sender, recipient, content}

Recipient/UI polls: GetConversation(*gid, *sender, limit)
                    or subscribes via WebSocket message_received

Recipient consumed: MarkDelivered(message_id)
   ↓ UPDATE mcx_messages SET delivered=1
```

The on-air SDS frame (TS 23.282 §7.4.2.2 SIP MESSAGE / §7.4.2.3 MSRP
+ TS 24.282 stage-3 XML body) is NOT emitted (TODO at
`mcdata.go:50-58`). Today downstream subscribers poll the message
store.

## 6. Key types

```go
// services/mcx/mcx.go
type User  { ID, UEID, MCPTTID, DisplayName, Priority }
type Group { ID, GroupID, DisplayName, GroupType }
type Call  { ID, Type ("ptt"|"video"|"data"), GroupID, State }

// services/mcx/common/common.go
const PriorityEmergency=1, PriorityImminentPeril=2, PriorityHigh=3,
      PriorityNormal=5, PriorityLow=7, PriorityBackground=9,
      PreemptThreshold=3

// services/mcx/mcptt/mcptt.go
type Call             { ID, GroupID, Initiator, FloorHolder, State, Priority, StartedAt }
type FloorManager     { cs map[CallID]*FloorController }
var GlobalFloorMgr    = &FloorManager{...}

// services/mcx/mcptt/floor.go
type FloorParticipant { MCPTTID, Priority, State, HasFloor, Requesting, ReqTime }
type FloorController  { CallID, State (string),
                        Holder, Queue, Participants,
                        EventCb, fsm }

const StateIdle / StateTaken / StateReleasing                              // controller
const StateStartStop / StateFloorIdle / StateFloorTaken /                  // per-
      StatePermitted / StateFloorReleasing                                 // participant

// services/mcx/mcptt/on_net_call.go
type OnNetCall { CallID, GroupID, IsGroup, Dialog *sip.SipDialog, Floor *FloorController }

// services/mcx/mcptt/offnet_call.go
type OffNetCallState int    // S1..S7
type OffNetCallConfig { TFG1..TFG6 time.Duration }
type SendWireFn func(msgType string, payload map[string]any)
type OffNetCall { GroupID, MCPTTID, Config, callID, refreshMs, sdp,
                  originatorID, SendWire, fsm }

// services/mcx/mcvideo/mcvideo.go
type TransmissionController { CallID, Transmitter, Participants, EventCb }
var GlobalTxMgr = &TransmissionManager{...}
type VideoCall { ID, GroupID, Initiator, Target, CallType, State, Participants }

// services/mcx/mcdata/mcdata.go — no struct types, all DB-row maps
const FileUploadDir = "/tmp/mcx_files"
```

## 7. Stubs / TODOs

| Location | Spec | Note |
|----------|------|------|
| `common/common.go:67-71` | TS 23.280 §8.3.1 | Replace `mcptt:` scheme with sip:-form IMPU mapping returned by IdMS |
| `common/common.go:156-159` | TS 33.180 §5.2.1 | MIKEY-SAKKE wrapping of GMK to per-member SAKKE identity |
| `common/common.go:160-164` | TS 33.180 §7.4 | Per-call key derivation chain (GMK → GMK-ID → MKFC → SRTP master key) |
| `common/common.go:235-238` | TS 24.481 | Render and publish Group-Document XML to GMS via XCAP PUT |
| `mcptt/mcptt.go:65-69` | TS 24.380 §4.1.1.4 | Six remaining §4.1.1.4 inputs + preempt-revoke outcome (§6.3.5.6) |
| `mcptt/mcptt.go:198-202` | TS 24.379 §6.2.4 / §6.2.8.1.15 / §6.3.2.2.9 | MCPTT MIME body, Resource-Priority namespace, feature-tag g.3gpp.mcptt |
| `mcptt/floor.go` (callouts to §6.3.5.6 / .7 / .10) | TS 24.380 | Pending Floor Revoke, "U: not permitted but sends media", "U: not permitted and initiating" not modelled |
| `mcptt/offnet_call.go` (preamble) | TS 24.379 §10.2.2.4.6 / .4.7 | Merge of off-network calls; specific error paths |
| `mcvideo/mcvideo.go:46-51` | TS 23.281 §7.7.1 / §7.7.2 | Preemption / queueing / transmission-revoke for video |
| `mcvideo/mcvideo.go:53-56` | TS 24.281 | Stage-3 transmission-control protocol packets |
| `mcdata/mcdata.go:50-58` | TS 23.282 §7.4.2.2 / .3 | Emit SDS via SIP MESSAGE or MSRP carrier |
| `mcdata/mcdata.go:86-89` | TS 23.282 §7.5.2.x | File-distribution accept/reject information flow |
| `mcdata/mcdata.go:90-92` | TS 23.282 §7.5.3 | FD media-plane option (HTTP / FT-HTTP via MSRP) |
| `mcdata/mcdata.go:120-123` | TS 23.282 §7.13.2 | Auth/authorization on message-store endpoints |
| `mcdata/mcdata.go:125-128` | TS 23.282 §7.13.3 | Message-store search / query expressions |
| `signaling/signaling.go:21-25` | TS 24.379 §6.2.4 / §6.2.8.1.15 | SIP↔WebSocket bridge does not yet stamp MCPTT feature-tag / MIME / Resource-Priority |

Stage-3 specs **TS 24.281 (MCVideo)** and **TS 24.282 (MCData)** are
**not yet in-tree** (3gpp.org download is gated). All TODOs that need
those documents are TS-numbered without §-cite per
`feedback_spec_cite_local_pdf.md`.

## 8. References

- **TS 23.280** §6 / §7 (functional model / planes), §8.1.1 (MC ID),
  §8.1.2 (MC service user ID), §8.1.3 (MC service group ID), §8.1.4
  (MC system ID), §8.3.1 (MC service ID ↔ IMPU), §10.1.4 (user
  profile), §10.2 (group management)
- **TS 23.379** — MCPTT functional architecture (Stage 2)
- **TS 23.281** §6 / §7.1 / §7.2 / §7.7 / §7.9 — MCVideo
- **TS 23.282** §6 / §7.4 / §7.5 / §7.8 / §7.13 — MCData
- **TS 24.379** §6.2 / §6.2.4 / §6.2.8.1 / §6.2.8.1.15 / §6.3.2.2 /
  §6.3.2.2.9 / §6.3.3 / §6.5 / §10.2.2 / §10.2.2.3.x / §10.2.2.4.3
- **TS 24.380** §4.1.1.4 / §6.3.2.2 / §6.3.3 / §6.3.5 / §6.3.5.2..7
  / §6.3.5.9 / §6.3.5.10
- **TS 33.180** §5.1 / §5.2 / §5.2.1 / §7 / §7.3 / §7.4
- **TS 24.484** — MCPTT service configuration (Resource-Priority
  namespace + values, referenced from §6.2.8.1.15)
- **TS 24.481** — MCX Group Document XML (deferred)

---

*Last refreshed against commit `13a181d`.*

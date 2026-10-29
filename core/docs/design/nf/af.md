# AF — Application Function

3GPP TS 23.501 §6.2.10 Application Function. Lives in `nf/af/`. ~1.4k LOC.
The AF is the policy/QoS-authorization consumer that drives PCF →
SMF → gNB QoS-flow installation for IMS media (VoNR), MEC traffic
steering and arbitrary third-party AFs surfaced via NEF.

## 1. Role in 5GC

The AF authorizes service-specific traffic by interacting with the PCF
(N5) and, for external AFs, by reaching the 5GC through the NEF (N33).
This implementation collapses the PCF/NEF onto in-process Go calls but
keeps the §-shaped surfaces so the SBI split is mechanical.

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **N5** | PCF | Npcf_PolicyAuthorization | TS 29.514 §4.2 |
| **N33** | NEF (then PCF) | Nnef_TrafficInfluence + Naf_EventExposure | TS 29.522 §4.4.7 / TS 29.517 §4.2 |
| **N70** | IMS P-CSCF (co-located IMS-AF) | SIP/SDP-derived MediaComponentDescription | TS 23.228 |
| (intra-NF) | SMF | drives `pcf.HandleAARequest` + `smpolicy.PushNotify` | TS 29.512 §4.2.3 |

Three AF roles are recognized at the boundary
(`nf/af/af.go:92-103`): `ims`, `mec`, `third_party`. Any other value is
rejected with a §4.2.2.2 ProblemDetails-style log.

## 2. Architecture

```
         ┌────────────── IMS P-CSCF / external AF ──────────────┐
         │  SIP INVITE (SDP)            REST: /naf/event-expose │
         └──────────────────────────────────────────────────────┘
                        │                          │
                        ▼                          ▼
   ┌────────────────────────── nf/af ──────────────────────────┐
   │  af.go                                                    │
   │   ├── AFSessionManager (TS 29.514 §4.2)                   │
   │   │     CreateSession → handleIMSAuthorization /          │
   │   │                     handleMECInfluence /              │
   │   │                     handleThirdParty                  │
   │   │     Update / Delete                                   │
   │   ├── EventExposureManager (TS 29.517 §4.2)               │
   │   │     Subscribe / Unsubscribe / Notify                  │
   │   └── Traffic Influence helpers (TS 29.522 §4.4.7)        │
   │  af_transitions.go — FSM table                            │
   │  fsm/  state.go event.go fsm.go                           │
   └───────────────────────────────────────────────────────────┘
                        │
                        ▼
              pcf.HandleAARequest          (TS 29.514 §4.2.2.2)
              smpolicy.PushNotify          (TS 29.512 §4.2.3)
                        │
                        ▼
              SMF rebuilds Authorized QoS / AMBR
              SMF emits NGAP PDU SESSION RESOURCE MODIFY  (TS 38.413 §8.2.3)
```

## 3. Package / file map

| File | Role |
|------|------|
| `nf/af/af.go` | All session, event, traffic-influence APIs; ~700 LOC |
| `nf/af/af_transitions.go` | Per-session FSM transition table (TS 29.514 §4.2.{2..5}) |
| `nf/af/af_test.go` | Unit tests for CreateSession + Subscribe validation |
| `nf/af/fsm/state.go` | `State` enum: Initial, AuthPending, Active, UpdatePending, Terminated, Failed |
| `nf/af/fsm/event.go` | `Event` enum: CreateRequest, Authorized, AuthRejected, UpdateRequest, DeleteRequest, NotifyReceived |
| `nf/af/fsm/fsm.go` | Per-key FSM dispatcher (one entry per `Key{SessionID}`) |
| `nf/af/webservice/sacore.db` | SQLite artifact (no Go code) |

## 4. SBI surface — current shape

The Go API is in-process. There is no HTTP router yet (see TODO below).
Operations map 1:1 to TS 29.514 / 29.517 / 29.522 service operations:

| Method (Go) | 3GPP operation | Spec § |
|-------------|----------------|--------|
| `AFSessionManager.CreateSession` | Npcf_PolicyAuthorization_Create | TS 29.514 §4.2.2 |
| `AFSessionManager.UpdateSession` | Npcf_PolicyAuthorization_Update | TS 29.514 §4.2.3 |
| `AFSessionManager.DeleteSession` | Npcf_PolicyAuthorization_Delete | TS 29.514 §4.2.4 |
| `EventExposureManager.Subscribe` | Naf_EventExposure_Subscribe | TS 29.517 §4.2 |
| `EventExposureManager.Unsubscribe` | Naf_EventExposure_Unsubscribe | TS 29.517 §4.2 |
| `EventExposureManager.Notify` | Naf_EventExposure_Notify | TS 29.517 §4.2 (callback over HTTP) |
| `RequestTrafficInfluence` / `RevokeTrafficInfluence` | TrafficInfluence | TS 29.522 §4.4.7 / §5.4 |

Notify callbacks run in goroutines against `EventSubscription.CallbackURL`
with a 5-second timeout (`af.go:561-576`); slow consumers don't block
the producer.

Event-type set (closed) — `af.go:440-457`:

```
UE_REACHABILITY  LOCATION_REPORT  LOSS_OF_CONNECTIVITY
COMMUNICATION_FAILURE  PDU_SESSION_STATUS  QOS_MONITORING
```

## 5. Headline lifecycle — IMS authorization (VoNR media)

The `ims`-type session is the most signal-heavy path. SDP-derived
MediaComponentDescription enters via `CreateSession`, the AF turns
that into a PCF authorization request, then drives the PCF→SMF
UpdateNotify so the SMF re-emits NGAP Modify and a dedicated 5QI flow
appears at the gNB. Source: `af.go:348-401`.

```
IMS P-CSCF          AF                     PCF                    SMF                gNB
    │ INVITE+SDP    │                       │                      │                  │
    ├──────────────►│ CreateSession         │                      │                  │
    │               │   (type=ims,          │                      │                  │
    │               │    media_components)  │                      │                  │
    │               │                       │                      │                  │
    │               │ §4.2.2 Create         │                      │                  │
    │               │  pcf.HandleAARequest  │                      │                  │
    │               ├──────────────────────►│                      │                  │
    │               │                       │ translate to dynamic │                  │
    │               │                       │ SDF / PCC rules      │                  │
    │               │                       │ (TS 29.513)          │                  │
    │               │                       │                      │                  │
    │               │  smpolicy.PushNotify  │                      │                  │
    │               ├──────────────────────►│ §4.2.3 UpdateNotify  │                  │
    │               │                       ├─────────────────────►│                  │
    │               │                       │                      │ rebuild Authoriz │
    │               │                       │                      │ ed QoS + Sess-AMBR
    │               │                       │                      │ NGAP PDU Sess    │
    │               │                       │                      │ Resource Modify  │
    │               │                       │                      ├─────────────────►│
    │               │                       │                      │ TS 38.413 §8.2.3 │
    │               │  FSM: AuthPending →   │                      │                  │
    │               │       Active          │                      │                  │
    │               │                       │                      │                  │
    │               │ Update on re-INVITE: §4.2.3 (same path)       │                  │
    │               │ Delete on BYE:       §4.2.4                   │                  │
    │               │   → pcf.HandleSessionTermination(IMSI)        │                  │
```

Failure modes encoded in `handleIMSAuthorization`:

- IMSI absent → ProblemDetails (§4.2.2.2 invalid request); session
  goes Failed and is retained for observability.
- `pcf.HandleAARequest` returns false → Failed.
- No SM Policy Association → soft failure: AF session stays Active
  on best-effort default flow, mismatch logged + counted.

MEC and third-party paths are stubs (`handleMECInfluence`,
`handleThirdParty` at `af.go:403-412`) — they log and return true
without driving downstream NFs.

## 6. Per-session FSM

Transition table at `af_transitions.go:14-36` (TS 29.514 §4.2.{2..5}):

| From | Event | To | Anchor |
|------|-------|----|---------|
| Initial | CreateRequest | AuthPending | §4.2.2 |
| AuthPending | Authorized | Active | §4.2.2 |
| AuthPending | AuthRejected | Failed | §4.2.2 |
| AuthPending | DeleteRequest | Terminated | §4.2.4 (early abort) |
| Active | UpdateRequest | UpdatePending | §4.2.3 |
| UpdatePending | Authorized | Active | §4.2.3 |
| UpdatePending | AuthRejected | Active | §4.2.3 (existing auth retained) |
| Active | NotifyReceived | Active | §4.2.5 (observational self-loop) |
| Active / UpdatePending / Failed | DeleteRequest | Terminated | §4.2.4 |

States and events live in `fsm/state.go` and `fsm/event.go`.

## 7. Key types / public API (Go)

```go
// af.go
type AFSession struct {
    SessionID, AFID, AFType, IMSI, DNN string
    PDUSessionID    int
    MediaComponents []map[string]any   // SDP-derived per §5.6 of TS 29.514
    TrafficFilters  []map[string]any   // for MEC / TrafficInfluence
    Status          string             // created | active | failed | terminated
    CreatedAt, UpdatedAt float64
}

type AFSessionManager struct {/*...*/}
func NewAFSessionManager() *AFSessionManager
func (*AFSessionManager) CreateSession(afID, afType, imsi, dnn string,
    pduSessionID int, mediaComponents, trafficFilters []map[string]any) (string, bool)
func (*AFSessionManager) UpdateSession(sessionID string, mediaComponents, trafficFilters []map[string]any) bool
func (*AFSessionManager) DeleteSession(sessionID string) bool
func (*AFSessionManager) GetSession(sessionID string) *AFSession
func (*AFSessionManager) GetSessionsForUE(imsi string) []*AFSession
var SessionMgr = NewAFSessionManager()

// Event Exposure
type EventSubscription struct {
    SubID, AFID, EventType, IMSI, CallbackURL, Status string
    NotificationCount int
    CreatedAt float64
}
type EventExposureManager struct {/*...*/}
func (*EventExposureManager) Subscribe(afID, eventType, imsi, callbackURL string) string
func (*EventExposureManager) Unsubscribe(subID string) bool
func (*EventExposureManager) Notify(eventType, imsi string, eventData map[string]any)
var EventMgr = NewEventExposureManager()

// Traffic Influence (TS 29.522 §4.4.7 helper)
func RequestTrafficInfluence(afID, imsi, dnn, targetIP, targetFQDN string,
    targetPort int, edgeSiteID string) (string, bool)
func RevokeTrafficInfluence(sessionID string) bool

// Legacy compat (older callers)
func CreateTrafficInfluence(afID, dnn, snssai, dnai, appID string) string
func ListTrafficInfluences() []TrafficInfluence
func SubscribeEvent(afID, event, imsi, dnn string) string
func ListEventSubs() []EventSub
```

Closed-set guards at the boundary (CreateSession / Subscribe) reject
blank `af_id` and unknown `af_type` / `event_type` values
(`af.go:151-158`, `af.go:491-498`).

## 8. What's not implemented — TODOs / stubs

Found by reading the code (`af.go`, `af_transitions.go`):

- **HTTP/2 + JSON SBI**: in-process Go calls only. The `webservice/`
  directory holds only a SQLite DB; no HTTP router is wired. Promotion
  to `Npcf_PolicyAuthorization` over HTTP is implicit future work.
- **MEC influence**: `handleMECInfluence` (`af.go:403-407`) logs and
  returns true. No call to `edge.mec.af_influence` or actual URSP
  steering yet. The TS 23.548 §6.6 AF guidance flow is not modelled.
- **Third-party / NEF**: `handleThirdParty` (`af.go:409-412`) is a
  log-only stub. NEF integration "pending".
- **§4.2.6 Subscribe / §4.2.7 Unsubscribe** (PCF → AF event
  subscription on the policy authorization service itself): no
  events are pushed by the PCF today; the FSM has only a
  self-looping `EvNotifyReceived` for the §4.2.5 path.
- **§4.2.5 Notify (PCF → AF)**: FSM transition exists but no producer
  fires it.

## 9. References (cited in source)

Only references already grep-verified inside `nf/af/`:

- TS 23.501 §6.2.10, §5.15.4
- TS 23.502 §4.3.2.2.1
- TS 23.228 (IMS Stage-2 context)
- TS 23.548 §6.2, §6.3, §6.4, §6.6
- TS 24.501 §5.5.2
- TS 29.512 §4.2.3 (Npcf_SMPolicyControl_UpdateNotify)
- TS 29.513 (PCC rule mapping reference)
- TS 29.514 §4.2.{2..7} (Npcf_PolicyAuthorization)
- TS 29.517 §4.1, §4.2, §5.3, §5.6 (Naf_EventExposure)
- TS 29.522 §4.4.7, §4.4.14, §4.4.33, §4.4.47, §5.4 (NEF NB APIs)
- TS 38.413 §8.2.3, §9.3.1.58 (NGAP Modify / UE-AMBR IE)

---
*Last refreshed against commit `13a181d`.*

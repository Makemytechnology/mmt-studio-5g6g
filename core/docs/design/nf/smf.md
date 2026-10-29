# SMF — Design Document

3GPP TS 23.501-aligned Session Management Function for the MMT 5G
Core. Owns the per-PDU-session control plane — IP allocation, UPF
selection, PFCP/N4 to the UPF, NAS encoding for AMF piggyback, PCF
policy resolution.

## 1. Role in 5GC

The SMF anchors per-PDU-session state. UE NAS messages reach it via
the AMF (relayed in UL NAS Transport, returned in DL NAS Transport),
and it drives the UPF over PFCP/N4. Reference points the SMF
terminates:

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **N4** | UPF | PFCP over UDP/8805 | TS 29.244 |
| **N7** | PCF | Npcf_SMPolicyControl | TS 29.512 |
| **N10** | UDM | Nudm_SDM | TS 29.503 |
| **N11** | AMF | Nsmf_PDUSession (HTTP/2 SBI; in-process today) | TS 29.502 |
| **N16** | other SMF | Nsmf_PDUSession_Create (roaming) | TS 29.502 — not impl. |

The 5GSM NAS itself (TS 24.501 §6) lives at the UE↔SMF boundary but
the SMF never owns NAS security — every 5GSM PDU is "indirectly
protected ... by being piggybacked by the security protected 5GMM
messages" (TS 24.501 §4.4 para 1). The SMF therefore receives plain
5GSM bytes from the AMF after `security.RxNAS` has unwrapped them.

## 2. Architecture

```
                 ┌─ AMF ────────────────────────────┐
                 │ ulnas / pdusetup / pdumodify /   │
                 │ pdurelease                        │
                 └──────────────┬──────────────────┘
                                │ in-process: session.Establish/Modify/Release
                                ▼
┌──────────────────── SMF ────────────────────────────────────────────┐
│                                                                      │
│  session/  PDU session lifecycle ────────────────────────────────┐  │
│   establish.go   Establish: PTI → IP alloc → UPF select → PCF   │  │
│                  CreatePolicy → PFCP CommitSession → ICS bytes  │  │
│                  → AMF (handler-driven FSM events)              │  │
│   session.go     Session struct + Store                         │  │
│   fsm/           per-session 5GSM FSM (StateInactive → ...)     │  │
│   fsm_transitions.go   table: every (state, ev) → to'           │  │
│   fsm_actions.go       Action stubs (log-only)                  │  │
│   pti/           PTI tracker (per-UE, 1..254)                   │  │
│   epco.go        ePCO encode (TS 24.008 §10.5.6.3, RFC 1661)     │  │
│   qos.go         Authorized QoS Rules (TS 24.501 §9.11.4.13)    │  │
│   dlnotify.go    DL Data Notification → N1N2MessageTransfer     │  │
│   webservice/    /api/smf/sessions etc.                         │  │
│  ──────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  ipalloc/        per-DNN IPv4/IPv6 rotating allocator              │
│  ctx/            (small) SMF identity/cfg                            │
│  upf/            registry (upf_instances DB row + runtime metrics)  │
│  upfclient/      PfcpBridge — UPFBridge over UDP/8805 PFCP          │
│  pfcp/fsm/       per-PFCP-session FSM (ESTABLISH/MODIFY/DELETE)     │
│  pfcp/transitions.go  table + log-only actions                      │
└──────────────────────────────────────────────────────────────────────┘
                                │ PFCP over UDP/8805
                                ▼
                        ┌─────────────────────┐
                        │      UPF            │
                        └─────────────────────┘
```

## 3. Package / file map

| Package | LOC | Role |
|---------|-----|------|
| `nf/smf/session` | ~3300 | Public surface: `Establish` / `Modify` / `Release`, `Session` + `Store`, FSM transitions/actions, ePCO, QoS rule build, DL notification. |
| `nf/smf/session/fsm` | ~250 | Generic per-session FSM engine (mirrors `nf/amf/gmm/fsm` shape). |
| `nf/smf/session/pti` | ~220 | TS 24.501 §7.3 PTI tracker (Start/Complete/Release/AllocateNetworkPTI). UE PTIs 1..127, network PTIs 128..254. |
| `nf/smf/session/webservice` | small | HTTP handlers for `/api/smf/sessions` etc. |
| `nf/smf/ctx` | 305 | SMF identity (NF instance ID, supported DNNs, default 5QI). |
| `nf/smf/ipalloc` | 220 | Rotating per-DNN IPv4/IPv6 allocator. First host (`.1`/`::1`) reserved. |
| `nf/smf/pfcp/fsm` | ~200 | Per-PFCP-session FSM: `Inactive → EstablishInProgress → Established → ModifyInProgress / DeleteInProgress`. |
| `nf/smf/pfcp/transitions.go` | 170 | Transition table + log-only Action stubs. |
| `nf/smf/upf` | 274 | UPF registry — `upf_instances` rows + in-memory `Runtime{ActiveSessions, LoadPercent, Status, LastHeartbeat}`. |
| `nf/smf/upfclient` | ~1300 | `PfcpBridge` — implements `nf/upf.UPFBridge` over PFCP/N4 (TS 29.244 §7.5.x). Default port 8805 (§6.1). |

## 4. Wire / SBI interactions

### 4.1 PFCP messages SMF emits / consumes (TS 29.244)

| Message | Direction | § | Trigger |
|---------|-----------|---|---------|
| Heartbeat Req/Resp | both | §7.4.2 | Periodic keep-alive (today: SMF can send; receiver path scaffolded) |
| Association Setup Req/Resp | SMF→UPF / UPF→SMF | §7.4.4 | Bootstrap on first session |
| Association Update / Release | both | §7.4.4.3-5 | TODO scaffold |
| Session Establishment Req/Resp | SMF→UPF / UPF→SMF | §7.5.2/§7.5.3 | `session.Establish` after IP+UPF select |
| Session Modification Req/Resp | SMF→UPF / UPF→SMF | §7.5.4 | DL FAR flip when gNB DL-TEID arrives; rule add/change |
| Session Deletion Req/Resp | SMF→UPF / UPF→SMF | §7.5.6 | `session.Release` |
| Session Report Req/Resp | UP→CP / CP→UP | §7.5.8 | Receiver path scaffolded; URR / DL Data Report fan-in |

`upfclient/pfcp_bridge.go` is the SMF-side `UPFBridge` impl; the
in-process cgo path lives at `nf/upf/cgo_bridge_linux.go`. Selection
happens in `upfloop.Enable()` at startup (per `nf/upf/DESIGN.md` §8).

### 4.2 5GSM per-PDU-session FSM (TS 24.501 §6.1.3.1)

States (`nf/smf/session/fsm/state.go`):

```
                ┌────────────┐
                │  Inactive  │◄───────────────────────┐
                └─────┬──────┘                         │
   EvEstablishmentRequest│                              │
                  │                                    │
                  ▼                                    │
  ┌──────────────────────────────┐                    │
  │  EstablishmentPending        │ ─EvEstablishmentRejected/EvPFCPEstablishFailure→
  └──────┬───────────────────────┘                    │
   EvPFCPEstablishResponse                            │
                  ▼                                    │
  ┌──────────────────────────────┐                    │
  │  ActivationPending           │─EvResourceSetupFailure────────────────┐
  └──────┬───────────────────────┘                                       │
   EvResourceSetupResponse                                                │
                  ▼                                                       │
                ┌────────────┐  EvModifyRequest      ┌────────────────┐ │
                │   Active   │ ──────────────────────▶│  ModifyPending │ │
                │            │ ◄─EvModifyComplete/Reject/T3591─────────│ │
                └─────┬──────┘                       └────────────────┘ │
   EvReleaseRequest │ EvReleaseCommandSent                              │
                  ▼                                                       │
  ┌──────────────────────────────┐                                       │
  │  ReleasePending  T3592 armed │─EvReleaseComplete/T3592→ Released ────┘
  └──────────────────────────────┘
```

Authoritative graph: `session/fsm_transitions.go` (262 lines, every
row carries TS 24.501 §6.4.x / §10.3 / TS 38.413 §8.2.1.x cite).

Timers (TS 24.501 §10.3 Table 10.3.2):

| Timer | Default | Expiry | § |
|-------|---------|--------|---|
| T3591 | per spec | ModificationCommand fail-back to Active | §6.4.2.6 / §10.3 N3591=4 |
| T3592 | per spec | ReleaseCommand → Released | §6.4.3.5 / §10.3 N3592=4 |
| T3593 | impl-specific | reserved (PFCP keepalive) | — |

### 4.3 Per-PFCP-session FSM (TS 29.244 §7.5.2)

`nf/smf/pfcp/fsm/state.go`: `Inactive → EstablishInProgress →
Established → {ModifyInProgress, DeleteInProgress}`. Lives alongside
the 5GSM FSM so observability (`/api/smf/pfcp`) can show "session is
in PFCP modify-pending while 5GSM is Active".

### 4.4 SBI consumer surfaces

In-process today; `TODO(spec: ...)` markers identify each future
HTTP/2 SBI lift point. Active call-sites:

| Operation | Caller | Today | Future SBI |
|-----------|--------|-------|------------|
| `pcf.GetPolicyForSession(imsi, dnn, sst)` | `session.Establish` | function call | TS 29.512 §4.2.2 Npcf_SMPolicyControl_Create |
| `smpolicy.Update(key, ...)` | `session.Modify` | function call | TS 29.512 §4.2.4 |
| `smpolicy.Delete(key)` | `session.Release` | function call | TS 29.512 §4.2.5 |
| `udm.SubscriptionData(...)` | `session.Establish` | function call | TS 29.503 Nudm_SDM |
| `session.N1N2Transfer(imsi, pduSessID)` (back to AMF) | `session.dlnotify.go` | wired in `nf/amf/hooks.go:52` | TS 29.518 Namf_Communication_N1N2MessageTransfer |

## 5. Lifecycle — PDU Session Establishment (TS 23.502 §4.3.2.2.1)

Maps to `session.Establish` (`session/establish.go`, 1396 lines):

```
UE                AMF                       SMF                       PCF        UPF
 │                 │                         │                          │          │
 │── PDU Sess. Est. Request (NAS, type 193) ─┤                          │          │
 │                 │   §6.4.1.2              │                          │          │
 │                 │── (in-proc) Establish ─▶│                          │          │
 │                 │                         │                          │          │
 │                 │   pti.Default.Start(IMSI, PTI, ProcEstablishment)  │          │
 │                 │   ipalloc.Allocate(DNN, v4/v6) → UE IP             │          │
 │                 │   upf.Pick(DNN, SST) → UPF anchor                  │          │
 │                 │── pcf.GetPolicyForSession(imsi, dnn, sst) ────────▶│          │
 │                 │◄──────── PCCRuleSet (rules, defaultQFI, AMBR) ─────│          │
 │                 │   smpolicy.Default.Create(ctx) → SmPolicyDecision  │          │
 │                 │                         │                          │          │
 │                 │   bridge.SessionCreate(IMSI, pduSessID, DNN, SST,  │          │
 │                 │                         SD, ueAddr, pdnType)       │          │
 │                 │   bridge.AddPDR(UL, FAR, QER, URR) ×2              │          │
 │                 │   bridge.AddFAR(DL, ApplyAction=BUFF) — gNB TEID   │          │
 │                 │     not known yet                                  │          │
 │                 │   bridge.AddQER(QFI, gateUL/DL, MBR, GBR)          │          │
 │                 │   bridge.SetSessionAMBR(ulKbps, dlKbps)            │          │
 │                 │   bridge.AddURR(VOLUM measurement)                 │          │
 │                 │   bridge.CommitSession(IMSI, pduSessID) ─────PFCP─▶│ §7.5.2  │
 │                 │                         │                          │          ◄ §7.5.3
 │                 │   FSM: Inactive → EstablishmentPending             │          │
 │                 │   FSM: → ActivationPending (EvPFCPEstablishResponse)│         │
 │                 │                         │                          │          │
 │                 │   encode 5GSM Accept (NAS type 194) — DNN, IPv4     │         │
 │                 │     allocated, ePCO containers, AuthorizedQoSRules,│         │
 │                 │     SessionAMBR, S-NSSAI, Always-on PDU Session    │         │
 │                 │     Indication if requested.                       │         │
 │                 │                         │                          │          │
 │                 │── ICS Request (carrying NGAP PDU Session Resource Setup
 │                 │   Request Item; piggyback NAS = 5GSM Accept) ─────────▶ gNB   │
 │                 │◄── ICS Response — gNB DL F-TEID per session ──────────  gNB   │
 │                 │                         │                          │          │
 │                 │── pdusetup handler fires EvResourceSetupResponse ─▶│          │
 │                 │   bridge.UpdateFAR(DL FAR, ApplyAction=FORW,       │          │
 │                 │     OuterHeaderCreation=GTP-U + gNB TEID + gNB IP) │          │
 │                 │                         ─PFCP §7.5.4──────────────▶│          │
 │                 │   FSM: → Active                                    │          │
 │                 │   pti.Complete(IMSI, PTI, accept_bytes)            │          │
 │                 │   pti.Release(IMSI, PTI) (after ack window)        │          │
 │                 │                                                    │          │
 │                                       data plane traffic flows                  │
```

### 5.1 Modify (TS 24.501 §6.4.2)

`session.Modify` arms T3591 with N3591=4 (`fsm_transitions.go:131-143`).
On `EvModificationComplete` cancels T3591 and stays Active. Reject
keeps Active (pre-modification params). Currently UpdateFAR / UpdateQER
(`pfcp_bridge.go:657, 678`) are scaffold — see TODO.

### 5.2 Release (TS 24.501 §6.4.3 / §6.4.4)

`session.Release` fires either `EvReleaseRequest` (UE-initiated) or
`EvReleaseCommandSent` (network-initiated) → `ReleasePending`, T3592
armed (N3592=4). On Complete (or T3592 final expiry) →
`bridge.SessionDelete(...)` PFCP §7.5.6 + IP release + 5GSM
RELEASE COMMAND fan-out. Sentinel errors:

```go
var ErrPDUSessionIDInUse        = errors.New("smf: PDU session id already in use (TS 24.501 §6.4.1.2)")
var ErrPDUSessionDoesNotExist   = errors.New("smf: PDU session does not exist (TS 24.501 §6.4.1.7 item d)")
```

### 5.3 DL Data Notification (TS 23.502 §4.2.3.3 step 3a)

`session/dlnotify.go` receives §7.5.8 reports from UPF when DL data
hits a buffered DL FAR. It looks up `(IMSI, pduSessionID)` and calls
`session.N1N2Transfer(imsi, pduSessID)` — the function pointer wired
in `nf/amf/hooks.go:52` to `amf.HandleN1N2MessageTransfer`. AMF then
runs Paging if CM-IDLE.

## 6. Key types / public API

```go
// session/session.go:50-112
type Session struct {
    IMSI         string
    PDUSessionID uint8
    PTI          uint8
    DNN          string
    SST          uint8
    SD           string
    PDUType      uint8
    SSCMode      uint8
    IPv4, IPv6   netip.Addr
    AMBRDL, AMBRUL uint32
    UEAMBRDL, UEAMBRUL uint32
    FiveQI       uint8
    UPFID        string
    UPFN3IP      string
    UPFTEID      uint32
    State        State        // INACTIVE/PENDING/ACTIVE/SUSPENDED/RELEASING/RELEASED
    AuthorizedQoSRules []byte // §9.11.4.13
    RequestedExtPCO    []byte // §8.3.1.9 (UE's ext-PCO from Establish Request)
    SmPolicyCtxRef     string // PCF context ref (§4.2.2.2 step 2)
    ChargingMethod     string // "online"|"offline"
    LastKnownLocation  []byte // §9.3.1.16 from §8.3.3.2 UE Ctx Release Complete
}
type Store struct { /* sync.Map shape */ }
var Default = NewStore()

// session/establish.go
type EstablishInput struct {
    IMSI, DNN, SD string
    PDUSessionID, PTI, SST, RequestedPDUType uint8
    RequestType uint8 // §9.11.3.47: 1=Initial, 2=ExistingPDU, 3=InitEmergency, ...
    UEIPv4Address [4]byte
    UEUsageSetting uint8 // §9.11.3.55 (voice-centric / data-centric)
    AMBRDL, AMBRUL uint32
    AlwaysOn bool
    Reactivation bool
}
func Establish(in EstablishInput) ([]byte /*5GSM accept*/, error)
func Modify(imsi string, pduSessionID uint8, /* mods */) error
func Release(imsi string, pduSessionID uint8) error
func ActivateUserPlane(imsi string, pduSessionID uint8, gnbTEID uint32, gnbIPv4 [4]byte) error

// PTI tracker — session/pti/pti.go
type ProcedureKind int
const (ProcUnknown / ProcEstablishment / ProcModification / ProcRelease)
func (t *Tracker) Start(imsi string, pti uint8, kind ProcedureKind, pduSessID uint8) (*Transaction, retransmit bool, err error)
func (t *Tracker) Complete(imsi string, pti uint8, response []byte)
func (t *Tracker) Release(imsi string, pti uint8)
func (t *Tracker) ReleaseAllForUE(imsi string) int
func (t *Tracker) AllocateNetworkPTI(imsi string, kind ProcedureKind, pduSessID uint8) uint8 // 128..254

// IP allocator — ipalloc/allocator.go
func (a *Allocator) Allocate(dnn string, cidrList []string, version int) (netip.Addr, error)
func (a *Allocator) Release(dnn string, ip netip.Addr)

// UPF registry — upf/registry.go
type Instance struct { UPFID, UPFIP, N3IP, N6IP string; PFCPPort int; SupportedDNNs/SST []string; MaxSessions int64 }
type Runtime  struct { ActiveSessions, LoadPercent int; Status string; LastHeartbeat time.Time }
func Register(i Instance) error
func Pick(dnn string, sst uint8) (*Instance, error)

// PFCP bridge — upfclient/pfcp_bridge.go (~1300 lines)
type PfcpBridge struct{ /* PFCP transport, runtime stats */ }
// Implements nf/upf.UPFBridge: SessionCreate / CommitSession / SessionDelete /
// AddPDR / AddFAR / AddQER / AddURR / UpdateFAR / DeactivateDLFAR /
// SetSessionAMBR / SetUEAMBR / DrainReports / ReportsDropped / GetURRStats / ...
```

## 7. What's not implemented

Grepped TODOs in `nf/smf/`:

| Area | Status | Source |
|------|--------|--------|
| §7.5.7.2 Usage Report IE in Session Modification | parsed, not acted on | `upfclient/pfcp_bridge.go:581` |
| §8.2.65 Recovery Time Stamp robustness | partial | `upfclient/pfcp_bridge.go:283` |
| §7.6.3 Heartbeat goroutine | not running | `upfclient/pfcp_bridge.go:286` |
| §8.2.25 UP Function Features negotiation | parsed but ignored | `upfclient/pfcp_bridge.go:381` |
| §7.5.4 wire-side Modification (UpdateFAR / UpdateQER beyond DL flip) | scaffold | `upfclient/pfcp_bridge.go:657, 678` |
| §7.5.4.10 Query URR / §7.5.5.2 Usage Report | not implemented (`GetURRStats` returns 0) | `upfclient/pfcp_bridge.go:917` |
| `pfcp.NewMessage(...)` codegen helper | TODO | `upfclient/pfcp_bridge.go:1282` |
| ePCO Configure-Nak (RFC 1661 §5.3) | not emitted | `session/epco.go:234` |
| §4.2.6 step 6a User Location Information forwarding | partial | `session/establish.go:676` |
| 5GSM piggyback detail beyond default flow | partial | `session/establish.go:1099` |
| `ErrNotImplemented` sentinel for PFCP-distributed mode | exposed but used sparingly | `upfclient/pfcp_bridge.go:110` |

Architecture-level:

- `nf/smf/ctx` is intentionally minimal — no NF instance ID lookup
  beyond DB; nothing emits NRF registration yet.
- `pfcp/transitions.go` Actions are log-only ("FSM action stubs ...
  Same intent as the GMM FSM's first stage" — file header at line 7).
- N16 / roaming SMF chain: not implemented.

## 8. References

Spec citations grepped from `nf/smf/`:

- **TS 23.501** §5.7 (QoS), §5.8 (CUPS), §6.3.3 (UPF instance / selection)
- **TS 23.502** §4.2.3.3, §4.2.6, §4.3.2.2.1 (PDU Session Establishment),
  §4.3.4 (Modification)
- **TS 23.503** §6.3 PCC rules, §6.6 URSP — used by PCF helper
- **TS 24.008** §10.5.6.3 — PCO containers used in ePCO
- **TS 24.501** §6.1.3.1 (5GSM states), §6.4.1 (Establish), §6.4.2
  (Modify), §6.4.3 (UE-init Release), §6.4.4 (Network-init Release),
  §7.3 (PTI), §9.6 PTI range, §9.11.3.47 (Request type),
  §9.11.4.2 (5GSM cause), §9.11.4.13 (Authorized QoS Rules),
  §10.3 Table 10.3.2 (T3591/T3592 retransmits)
- **TS 29.244** §6.1 UDP/8805, §6.4 reliable delivery, §7.2.2 header,
  §7.4.2 Heartbeat, §7.4.4.x Association, §7.5.2/3 Establishment,
  §7.5.4 Modification, §7.5.6 Deletion, §7.5.8 Report,
  §8.2.x IE sub-clauses
- **TS 29.281** GTP-U (N3 between gNB / UPF — SMF only configures via
  PFCP)
- **TS 29.502** Nsmf_PDUSession (consumer + producer surface; SBI lift)
- **TS 29.512** §4.2.2-§4.2.5 Npcf_SMPolicyControl, §5.6.x types
- **TS 29.513** PCC signalling flows
- **TS 29.514** Npcf_PolicyAuthorization (used by AF integration)
- **TS 38.413** §8.2.1 PDU Session Resource Setup (success / failure
  drives `EvResourceSetupResponse` / `EvResourceSetupFailure`)
- **RFC 1661** PPP / PCO option layout (used in ePCO)

---
*Last refreshed against commit `13a181d`.*

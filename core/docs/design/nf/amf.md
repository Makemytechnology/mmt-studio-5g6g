# AMF — Design Document

3GPP TS 23.501-aligned Access and Mobility Management Function for the
MMT 5G Core. Spec citations follow the in-tree convention: every
`§a.b.c` resolves against a PDF under `specs/3gpp/` and is enforced by
`nf/tools/speccheck/` at test time.

## 1. Role in 5GC

The AMF terminates the N1 (NAS) and N2 (NGAP/SCTP) reference points.
It owns UE registration, connection management, paging, mobility,
NAS security, and service routing toward SMF/AUSF/UDM/NSSF/PCF for
each UE. Per the in-tree `gmm/doc.go` reference-point table:

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **N1** | UE | NAS over RRC over SCTP-relayed gNB DL/UL | TS 24.501 |
| **N2** | gNB / NG-RAN / N3IWF | NGAP over SCTP/PPID 60 | TS 38.413, TS 38.412 §7 |
| **N8** | UDM | Nudm (Nausf SBI) | TS 29.503 |
| **N11** | SMF | Nsmf (HTTP/2 SBI; in-process today) | TS 29.502 |
| **N12** | AUSF | Nausf | TS 29.509 |
| **N14** | peer AMF | Namf (mobility, N1N2MessageTransfer) | TS 29.518 |
| **N15** | PCF | Npcf | TS 29.512 |
| **N22** | NSSF | Nnssf | TS 29.531 |
| **N26** | MME | GTPv2-C (audit log only here) | TS 29.274 |

The Go AMF has the in-process consumer-side stubs for SMF / AUSF /
UDM / NSSF / PCF; they remain plain function calls today, marked
`TODO(arch: sbi-Nxx)` in code (`gmm/doc.go` lines 117-130).

## 2. Architecture

```
                      ┌──────────────────────────────────────────────────────┐
                      │                       UE                             │
                      └───────────────────────┬──────────────────────────────┘
                                              │ NAS over RRC
                                              ▼
                      ┌──────────────────────────────────────────────────────┐
                      │                    gNB / N3IWF                       │
                      └───────────────────────┬──────────────────────────────┘
                                              │ NGAP / SCTP (PPID 60, str 0..N)
                                              ▼
┌─────────────────────────────────  AMF process  ─────────────────────────────────┐
│                                                                                  │
│  ┌─ NGAP layer ────────────────────────────────────────────────────────────────┐│
│  │ ngap/transport_linux.go     SCTP listener, accept loop, COMM_UP / SHUTDOWN  ││
│  │ ngap/server.go              per-association handler + per-stream workers   ││
│  │ ngap/dispatch.go            procedureCode → Handler                        ││
│  │ ngap/wire/                  envelope encode/decode (asn1go ngap)           ││
│  │ ngap/{ngsetup,initialue,ulnas,dlnas,                                         │
│  │       initialctxsetup,uectxrelease,                                          │
│  │       pdusetup,pdumodify,pdurelease,                                         │
│  │       pwsrestart,pwsfailure,pwscancel,                                       │
│  │       writereplace,handover,paging,errind}                                   │
│  │ ngap/sctpfsm                per-association FSM (NGSetup → InService)      ││
│  │ ngap/fsm                    per-UE NGAP FSM (Idle ↔ Estab ↔ Handover)      ││
│  └────────────────────────────────┬─────────────────────────────────────────────┘│
│                                   │ Fire(event) / SendDLNAS                     │
│  ┌─ GMM layer ────────────────────▼─────────────────────────────────────────┐ │
│  │ gmm/dispatch.go             EPD/SHT decode, RxNAS, msg-type routing      │ │
│  │ gmm/{registration, registration_response, identity, auth, smc,           │ │
│  │      service, ulnas, dereg, configupdate, status, status_send,           │ │
│  │      pdu_reconcile, security_context_reuse, ie_builders}                 │ │
│  │ gmm/fsm                     per-UE 5GMM FSM (TS 24.501 §5)               │ │
│  │ gmm/fsm_actions.go          declarative Actions for transitions          │ │
│  │ gmm/fsm_transitions.go      authoritative table of every (state, ev)→to' │ │
│  └────┬─────────────────────────────────────────────────────────────────────┘ │
│       │  RxNAS / TxDL / TxSMC / Reuse / DeriveKgNB / ActivateCtx              │
│       ▼                                                                        │
│  ┌─ Security layer (single owner) ─────────────────────────────────────────┐  │
│  │ security/{rx,tx,activate,reuse,kgnb,primitives,container,init}          │  │
│  └──────────────────────────────────────────────────────────────────────────┘  │
│                                                                                  │
│  ┌─ Context stores ──────────────────┐  ┌─ Cross-cutting ─────────────────┐    │
│  │ ctx/        AMF identity, GUAMI   │  │ hooks.go    UE-removal hooks    │    │
│  │ uectx/      per-UE state + Reg.   │  │ n1n2.go     Namf step-3a paging │    │
│  │ gnbctx/     per-gNB state + Reg.  │  │ n26/        EPS handover audit  │    │
│  │ pws/        Warning fan-out       │  │ musim/      multi-USIM rows     │    │
│  └────────────────────────────────────┘  └──────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────────┘
                                   │  in-process calls
              ┌────────────────────┼────────────────────────┐
              ▼                    ▼                        ▼
         nf/smf/session     libs/sacrypto (NEA/NIA)    nf/{ausf,udm,nssf,pcf}
```

## 3. Package / file map

### 3.1 Top-level

| File / pkg | Role |
|------------|------|
| `amf.go` (`amf.go:36-393`) | `Service` boot wiring: validates bind IP, Registers every NGAP procedure handler, opens `ngap.Listener`, builds Server. Loads PLMN / GUAMI / NSSAI / security algos from DB. |
| `n1n2.go` | `HandleN1N2MessageTransfer` — Namf_Communication §4.2.3.3 step 3a; branches CM-IDLE → Paging vs CM-CONNECTED ack. |
| `hooks.go` | `init()` registers UE-removal hooks: `timers.M.CancelAllForUE`, `pti.Default.ReleaseAllForUE`. Also wires `session.N1N2Transfer = HandleN1N2MessageTransfer` to break the SMF↔AMF import cycle. |

### 3.2 Sub-packages

| Package | LOC | Role |
|---------|-----|------|
| `nf/amf/ctx` | ~250 | AMF identity (Name, GUAMIs, PLMNSupport, NetworkFeatureSupport, ciphering / integrity AlgoPriority lists). Singleton `ctx.Default`. |
| `nf/amf/uectx` | ~830 | `AmfUeCtx` (NGAP IDs, IMSI, GnbKey, `Security *SecurityCtx`, RM/CM, Reg params, UERadioCapability), `Registry` with hooks, `RMState/CMState/GMMProcedure/GMMSubStep/NGAPProcedure` enums. |
| `nf/amf/gnbctx` | ~600 | `GnbCtx` (SCTP `Conn`, GnbIP, GnbID, GnbName, BroadcastPLMNs, SupportedTAItems, `NumSCTPStreams`, `Connected`, `Supersede`/`IsSuperseded`). `Registry` keyed by gNB IP. |
| `nf/amf/security` | ~1050 | Single-owner NAS crypto: `RxNAS / TxDL / TxSMC / TxPlain / ActivateCtx / Reuse / DeriveKgNB`. Encapsulates `wrap` / `unwrap` so handlers cannot touch K_NAS\*/counts. |
| `nf/amf/gmm` | ~5500 | 5GMM state machine + handlers + FSM table. `dispatch.go` is the single NAS entry point. |
| `nf/amf/gmm/fsm` | ~600 | Generic FSM engine — `State / Event / Transition / TimerSpec`, per-UE registry, `Of(ue)`. |
| `nf/amf/ngap` | ~3500 | NGAP transport + dispatcher + per-procedure server. SCTP/Linux + TCP-stub fallback. |
| `nf/amf/ngap/wire` | ~200 | NGAP envelope encode/decode (wraps `mmt/asn1go/protocols/ngap/generated`). |
| `nf/amf/ngap/fsm` | ~400 | Per-UE NGAP FSM: `StateIdle`, `StateInitialUEReceived`, `StateEstablished`, `StateHandoverPreparation/Execution`, `StateRelease`, plus `EvNGReset` cascade. |
| `nf/amf/ngap/sctpfsm` | ~250 | Per-association FSM tracking NGSetup → InService → Reset; drives `cascadeNGResetForGnb`. |
| `nf/amf/ngap/{ngsetup,initialue,ulnas,dlnas,initialctxsetup,uectxrelease,pdusetup,pdumodify,pdurelease,pwsrestart,pwsfailure,pwscancel,writereplace,errind,handover,paging,ueradiocap,sctp_notification,sctp_transitions}` | one per procedure | TS 38.413 §8.x procedure handlers; each `init()`s a `Register(ProcCode<X>, handle)` (see `amf.go:101-114`). |
| `nf/amf/n26` | ~390 | EPC↔5GC inter-system handover audit log (`n26_handover_log` rows: `imsi, source_rat, target_rat, status`). |
| `nf/amf/musim` | 39 | Multi-USIM `musim_groups` listing — read-only DB pass-through. |
| `nf/amf/pws` | 89 | PWS broadcast fan-out: `BroadcastToAll` / `CancelToAll` walk `gnbctx.Default.All()` and call `writereplace.Send` / `pwscancel.Send`. |

## 4. Wire interactions

### 4.1 NGAP procedureCode mapping

From `ngap/dispatch.go:32-69` (each constant carries its TS 38.413
§-cite verbatim in source):

| Code | Procedure | § | Handler |
|------|-----------|----|---------|
| 0  | AMFConfigurationUpdate         | §9.4   | (registered by `amf.go:114` TODO list) |
| 4  | DownlinkNASTransport           | §8.6.2 | `dlnas` (AMF-initiated; `HandleResponse` not used) |
| 7  | DownlinkRANStatusTransfer      | §8.4.7 | (relayed in `handover.go`) |
| 9  | ErrorIndication                | §8.7.6 | `errind` |
| 10 | HandoverCancel                 | §8.4.5 | `handover.go:handleHandoverCancel` |
| 11 | HandoverNotification           | §8.4.3 | `handover.go:handleHandoverNotification` |
| 12 | HandoverPreparation            | §8.4.1 | `handover.go:handleHandoverPreparation` |
| 13 | HandoverResourceAllocation     | §8.4.2 | `handover.go:handleHandoverResourceAllocation` |
| 14 | InitialContextSetup            | §8.3.1 | `initialctxsetup` |
| 15 | InitialUEMessage               | §8.6.3 | `initialue` |
| 20 | NGReset                        | §8.7.4 | `handover.go:handleNGReset` |
| 21 | NGSetup                        | §8.7.1 | `ngsetup` |
| 24 | Paging                         | §8.6   | `paging.go:SendPaging` (AMF-initiated) |
| 25 | PathSwitchRequest              | §8.4.4 | `handover.go:handlePathSwitchRequest` |
| 26 | PDUSessionResourceModify       | §8.2.3 | `pdumodify` |
| 28 | PDUSessionResourceRelease      | §8.2.2 | `pdurelease` |
| 29 | PDUSessionResourceSetup        | §8.2.1 | `pdusetup` |
| 32 | PWSCancel                      | §8.9.2 | `pwscancel` |
| 33 | PWSFailureIndication           | §8.9.4 | `pwsfailure` |
| 34 | PWSRestartIndication           | §8.9.3 | `pwsrestart` |
| 35 | RANConfigurationUpdate         | §8.7.2 | (TODO in `amf.go:114`) |
| 41 | UEContextRelease               | §8.3.4 | `uectxrelease` |
| 42 | UEContextReleaseRequest        | §8.3.3 | `uectxrelease` |
| 44 | UERadioCapabilityInfoIndication| §8.14.1| `ueradiocap.go` |
| 46 | UplinkNASTransport             | §8.6.1 | `ulnas` |
| 49 | UplinkRANStatusTransfer        | §8.4.6 | `handover.go:handleUplinkRANStatusTransfer` |
| 51 | WriteReplaceWarning            | §8.9.1 | `writereplace` |
| 61 | HandoverSuccess                | §8.4.8 | (constant defined; handler not registered) |
| 62 | UplinkRANEarlyStatusTransfer   | §8.4.9 | `handover.go:handleUplinkRANEarlyStatus` |
| 63 | DownlinkRANEarlyStatusTransfer | §8.4.10| (relayed in handover paths) |

### 4.2 SCTP transport

`ngap/transport_linux.go` provides the real SCTP backend; non-Linux
falls through to a TCP stub (`transport_stub.go` line 10-11). The
listener uses `ishidawataru/sctp` patterns (build-tagged). `sctp_config.go`
sets default `NumSCTPStreams=16`. Per-association recv loop in
`server.go:147-310` does:

1. Drain SCTP socket → split bundled DATA chunks (RFC 4960 §6.10) via
   `wire.DecodeNext` (`server.go:289-309`).
2. Hash-route to per-stream worker via `UEStream(amfUeID) = amfUeID%(N-1)+1`
   (preserves per-UE ordering).
3. Worker dispatches to procedure handler.

### 4.3 Per-UE GMM FSM (TS 24.501 §5)

States (from `gmm/fsm/state.go`):

```
            ┌──────────────┐
            │ DEREGISTERED │◄──────────────────────────┐
            └──────┬───────┘                            │
        EvRegRequest│                              T3550│ EvAuth*Invalid
            │      │EvRegRequestContextValid          │ │ EvSecModeReject
            │      │  (§4.4 cached ctx skip auth)     │ │ EvAuthFailure
            │      ▼                                   │ │ T3560 / T3570
            │  StateRegisteredInitiated◄───────────┐  │ │
            │      ▲                               │  │ │
            │      │EvRegistrationComplete         │  │ │
   EvIdReqSent│    │                               │  │ │
            ▼      │                               │  │ │
   StateIdentific. │EvSecurityModeComplete         │  │ │
            │      │                               │  │ │
   EvIdRespValid   │                               │  │ │
            │      │                               │  │ │
            ▼      │                               │  │ │
   StateAuthentic.─┴──EvAuthValid──►StateSecurityMode  │ │
            │                                          │ │
   EvAuthRetry│ self-loop                              │ │
            │                                          │ │
   EvAuthInvalid / EvAuthFailure                       │ │
            └──────────────────────────────────────────┴─┘
                                                  │
                          EvT3512Expired (implicit dereg)
                          EvDeregRequestMO  →  StateDeregistrationInitiated
                          EvDeregRequestSentMT → StateMTDeregPending
```

Authoritative graph: `gmm/fsm_transitions.go` — every row carries a
TS 24.501 §-cite (e.g. line 39-60 §5.5.1.2.2; line 62-105 §4.4 reuse;
line 282-314 §5.4.2; line 568-602 §5.5.2).

Timers per row (TS 24.501 §10.2 Table 10.2.1, names verbatim):

| Timer | Default | Expiry event | Description |
|-------|---------|--------------|-------------|
| T3512 | 6h+4min (`fsm_transitions.go:29`) | EvT3512Expired | Periodic registration / implicit dereg |
| T3550 | spec     | EvT3550Expired | Registration Accept retransmit (§5.5.1.2.4) |
| T3560 | spec     | EvT3560AuthExpired / EvT3560SMCExpired | Auth Request / SMC retransmit |
| T3570 | spec     | EvT3570Expired | Identity Request retransmit |
| T3522 | spec     | EvT3522Expired | MT Deregistration Request retransmit |
| T3555 | spec     | EvT3555Expired | Configuration Update Command retransmit |
| Twait-ue-ctx-release | 10s impl-specific | EvTwaitDeregReleaseExpired | Bound NGAP UE Context Release Complete |

`MaxRetransmit=NASMaxRetransmit` matches §10.2 N3xxx=4 ("4 retransmits
at T seconds each"). Effective expiry = Duration × (Max+1) — done
inside `gmm/fsm/fsm.go:201-234`.

### 4.4 Per-UE NGAP FSM

`ngap/fsm/state.go` + `ngap/fsm_transitions.go` (538 lines). States
(`StateIdle, StateInitialUEReceived, StateEstablished,
StateHandoverPreparation, StateHandoverExecution, StateRelease`) are
driven by NGAP outcomes. `EvNGReset` is fanned out by
`cascadeNGResetForGnb` (server.go:236) on SCTP loss.

## 5. Lifecycle for headline procedures

### 5.1 Initial Registration (TS 24.501 §5.5.1.2 + TS 23.502 §4.2.2)

```
UE                gNB                    AMF                    AUSF/UDM     SMF
 │── RR ─────────▶│── InitialUEMessage ─▶│   §8.6.3              │
 │                │     (NAS RR inside)  │                       │
 │                │                       ├── (in-proc) udm.GetSubscription(SUPI)
 │                │                       ├── (in-proc) ausf.GenerateAV → RAND/AUTN/XRES*
 │                │  IDENTITY REQUEST    │  (only if SUCI un-resolvable)
 │◄── DL NAS ─────│◄── DLNASTransport ──│   T3570 armed
 │── ID RESP ────▶│── ULNASTransport ──▶│
 │  AUTH REQUEST                         │  T3560 (auth leg) armed
 │◄── DL NAS ─────│◄── DLNASTransport ──│   §5.4.1
 │── AUTH RESP ──▶│── ULNASTransport ──▶│   handler decodes RES* → fires
 │                │                       │   EvAuthResponseValid|Invalid
 │                │                       │
 │                │                       ├── security.ActivateCtx(KAMF, ngksi, eea, eia)
 │  SMC          │                       │   K_NASEnc/Int derived (§A.8); UL/DL=0
 │◄── DL NAS ─────│◄── DLNASTransport ──│   security.TxSMC (SHT=3) — §6.7.2
 │── SMC COMPL.──▶│── ULNASTransport ──▶│   T3560 stops; EvSecModeComplete
 │                │                       │
 │                │                       ├── initialctxsetup.Send (§8.3.1.2):
 │                │                       │   • security.DeriveKgNB(ue) — §A.9, FC=0x6E
 │                │                       │   • encode AllowedNSSAI, MobilityRestrictionList?
 │                │                       │   • piggyback REGISTRATION ACCEPT in NAS-PDU IE
 │                │                       │
 │                │◄── ICS Request ──────│
 │                │── ICS Response ─────▶│   gNB allocates DL F-TEID per session
 │                │                       │   FSM: → StateRegisteredInitiated, T3550 armed
 │◄── REG ACC. ───┤  (carried in ICS Req)
 │── REG COMPL. ─▶│── ULNASTransport ──▶│   EvRegistrationComplete
 │                │                       │   FSM: → StateRegistered; T3550 stop, T3512 start
```

### 5.2 §4.4 Cached-context skip-auth

`gmm/registration.go` + `security_context_reuse.go`:

1. RR arrives in `StateDeregistered` with non-zero ngKSI.
2. Handler resolves SUPI from 5G-GUTI → looks up existing AmfUeCtx.
3. Calls `security.Reuse(existing, rrPDU)` — verifies MAC against the
   existing ctx's K_NASInt; advances UL count on success
   (`security/reuse.go:44`, `security/doc.go` invariants I7).
4. State migrated onto the new UE handle; `actSendRegistrationAcceptReused`
   fires → `StateRegisteredInitiated`, T3550 armed
   (`fsm_transitions.go:62-138`).

### 5.3 Service Request / Reactivation (TS 24.501 §5.6.1 + §4.2.3.2)

`gmm/service.go` decodes Service Request, validates MAC via RxNAS,
runs `initialctxsetup.Send` (no SMC). K_gNB freshness = UL count of
the Service Request (`security/kgnb.go:60`, `(ULNasCount-1)`). Per
session, SMF is told to flip the DL FAR via `session.ActivateUserPlane`.

### 5.4 N1N2 paging (TS 23.502 §4.2.3.3)

`n1n2.go:HandleN1N2MessageTransfer`:
- Looks up UE by IMSI; rejects when `RM != Registered` (line 73).
- Records pending `pduSessionID` on `ue.PendingN1N2Sessions`.
- CM-IDLE → `ngap.SendPaging(ue, gnbctx.Default)` — fans out NGAP
  Paging (procedureCode 24) to every gNB whose TAC matches the UE's
  registered TAI (`paging.go:SendPaging`).
- CM-CONNECTED → log only (deferred per-session reactivation TODO).

### 5.5 Deregistration (TS 24.501 §5.5.2)

| Trigger | Event | Path | Timer |
|---------|-------|------|-------|
| UE-initiated MO | `EvDeregistrationRequestMO` | `actEnterDeregistration` → DEREGISTRATION_INITIATED → wait for UE Ctx Release Complete | Twait-ue-ctx-release (10s) |
| AMF-initiated | `EvDeregistrationRequestSentMT` | `actEnterMTDeregPending` → MT_DEREG_PENDING | T3522 with N3522=4 |
| Implicit (T3512 expiry) | `EvT3512Expired` | `actOnImplicitDeregistration` → DEREGISTERED | — |
| SCTP loss | `cascadeNGResetForGnb` (`server.go:236`) | EvNGReset into every UE NGAP FSM; per-session `session.Release` walked (`server.go:241`) | — |

### 5.6 N2 Handover (TS 38.413 §8.4)

`ngap/handover.go` — AMF acts as relay between source and target gNB
across 9 messages / 4 procedures. Reference flow from
`handover.go:18-34`:

```
1. src-gNB → AMF: HandoverRequired (§8.4.1, code 12)
   AMF → tgt-gNB: HandoverRequest (§8.4.2)            FSM: Estab → HandoverPrep
2. tgt-gNB → AMF: HandoverRequestAcknowledge
   AMF → src-gNB: HandoverCommand                     FSM: HandoverPrep → HandoverExec
3. src-gNB → AMF: UplinkRANStatusTransfer (§8.4.6)
   AMF → tgt-gNB: DownlinkRANStatusTransfer (§8.4.7)
4. tgt-gNB → AMF: HandoverNotify (§8.4.3)
   AMF → src-gNB: UEContextReleaseCommand             FSM: HandoverExec → Established
5. src-gNB → AMF: UEContextReleaseComplete            cleanup
```

Failure paths: `HandoverFailure` (§8.4.2) leaves source side; source
`HandoverCancel` (§8.4.5) tears down target side.

## 6. Key types / public API

### 6.1 Boot

```go
// amf.go:36-127
type Config struct {
    ListenAddr     string // default ":38412"
    NumSCTPStreams int    // default 16
}
func Start(cfg Config) (*Service, error)
func (s *Service) Stop()
func InitContextFromDB() error  // amf.go:267 — reads network_config + plmn rows
```

### 6.2 Per-UE (`uectx`)

```go
type AmfUeCtx struct {
    AmfUeNGAPID, RanUeNGAPID int64
    IMSI, MSISDN, GnbKey     string
    Security                 *SecurityCtx
    RM                       RMState
    CM                       CMState
    GMMProc                  GMMProcedure
    GMMSub                   GMMSubStep
    NGAPProc                 NGAPProcedure
    UERadioCapability        []byte           // §9.3.1.74
    UERadioCapabilityForPaging []byte         // §9.3.1.68
    PendingN1N2Sessions      []uint8          // n1n2.go:appendPendingSession
    LastRegRequestPDU        []byte           // §5.5.1.2.8 d/e collision compare
    InitialRRCleartextOnly   bool             // drives RINMR bit in SMC
    TMSI5G                   uint32           // 5G-GUTI bind
    LastKnownTAI / NRCGI / PLMN
    RetxNASPDU               []byte
    // ...
}
```

### 6.3 Security single-owner API (`security/`)

```go
// rx.go
func RxNAS(ue *AmfUeCtx, pdu []byte) (plain []byte, meta RxMeta, err error)
type RxMeta struct { SHT uint8; ULCount uint32; Plain, Verified bool }

// tx.go
func TxPlain(_ *AmfUeCtx, inner []byte) []byte
func TxDL(ue *AmfUeCtx, plain []byte) ([]byte, error)        // SHT=2 post-SMC (§4.4.5)
func TxSMC(ue *AmfUeCtx, inner []byte) ([]byte, error)       // SHT=3 (§9.3 NOTE 1)

// activate.go — TS 33.501 §A.8 + §6.7.2 keys + count reset
func ActivateCtx(ue *AmfUeCtx, ngksi, eea, eia uint8) error

// reuse.go — TS 24.501 §4.4 cached-ctx case (i)
func Reuse(existing *AmfUeCtx, pdu []byte) error

// kgnb.go — TS 33.501 §A.9 + §6.8.1.2.2 just-in-time, no caching (I4)
func DeriveKgNB(ue *AmfUeCtx) ([]byte, error)
```

### 6.4 GMM FSM

```go
// gmm/fsm/fsm.go
func Of(ue *uectx.AmfUeCtx) *FSM
func (f *FSM) Fire(c *Context) error
func (f *FSM) FireTimer(ev Event)
func (f *FSM) ResetTo(s State)        // §5.5.1.2.8(a) lower-layer-failure escape
func AllSnapshots() []Snapshot
```

### 6.5 Cross-NF hooks

```go
// n1n2.go — wired into nf/smf/session via hooks.go init()
func HandleN1N2MessageTransfer(imsi string, pduSessionID uint8)

// pws/dispatch.go
func BroadcastToAll(p writereplace.Params) []GnbResult
func CancelToAll(p pwscancel.Params) []GnbResult

// n26/n26.go
func LogHandover(imsi, sourceRAT, targetRAT, status string) (int64, error)
```

## 7. What's not implemented

Grepped TODOs (`grep -rn 'TODO' nf/amf`):

| Area | Status | Source |
|------|--------|--------|
| `paging`, `amfconfigupdate`, `ranconfigupdate`, `ngreset` handler `Register()` | not in `amf.go` boot list | `amf.go:114` |
| AMF Config Update (procedureCode 0) | constant defined, no handler | `dispatch.go:33` |
| HandoverSuccess (code 61) | constant defined, no handler | `dispatch.go:66` |
| RAN Config Update (code 35) | constant defined, no handler | `dispatch.go:59` |
| Both-5G-GUTI-valid bookkeeping (§5.5.1.2.8 c) | not modelled | `fsm_transitions.go:347-351` |
| §4.4.6 NAS Message Container §4.4.6 case (a) RINMR full path | partial | `gmm/smc.go` TODOs lines 12-100 |
| AUTS / SQN re-sync (§5.4.1.3.7 fail-cause-21) | terminal reject only | `fsm_transitions.go:264-272` |
| Mapped 5G NAS context (EPS→5G inter-system change, §4.4.2.1) | not implemented | `security/doc.go:373-377` |
| Non-3GPP access (N3IWF) NAS COUNT pair / access-distinguisher 0x02 | scaffold only | `security/doc.go:368-372` |
| 5GSM piggyback security | indirect via 5GMM (§4.4 para 1 verbatim) | `security/doc.go:368` |
| Two-stage T3512 (operator extension) | TODO | `gmm/fsm_actions.go:243` |
| `errind` Criticality Diagnostics emission (§8.7.2.2) | log-only | `ngap/handover.go:610` |
| Paging IE table fill — IE 069 / "Paging Origin" | partial | `ngap/paging.go:170` |
| UE Context Release: Paging Assistance Data for CE (§8.3.3.2) | logged, not stored | `ngap/uectxrelease/uectxrelease.go:520-523` |
| K_AMF rekey on §6.8.1.1.1 case 1/2.c | not implemented | `ngap/uectxrelease/uectxrelease.go:582` |
| §4.2.3.3 step 3b ProblemDetails reject over Namf SBI | log-only | `n1n2.go:63-64` |
| Per-session reactivation when CM-CONNECTED + Suspended | not implemented | `n1n2.go:94-97` |
| `nf/amf/musim` write/update API | read-only `List`/`Status` | `musim/musim.go` |
| `ngap.server_test.go` E2E tests | `t.Skip` on Linux SCTP build | `ngap/server_test.go:21,83` |
| `gmm.registration_flow_test` | `t.Skip` (subscriber fixture) | `gmm/registration_flow_test.go:34` |
| `pdusetup.pdusetup_test.go` NAS piggyback assertion | `t.Skip` | `ngap/pdusetup/pdusetup_test.go:30` |

Architecture-shape TODOs (`grep -rn 'TODO(arch:' nf/amf/gmm`):

| Marker | Locations | Direction |
|--------|-----------|-----------|
| `arch: event: DL-NAS to NGAP` | `gmm/{smc,retransmit,dereg,fsm_actions}.go` | event-bus instead of direct dlnas.Send |
| `arch: event: ICS to NGAP` | `gmm/smc.go:348` | event-bus instead of initialctxsetup.Send |
| `arch: event: UE-Context-Release to NGAP` | `gmm/{dereg,fsm_actions}.go` | event-bus |
| `arch: sbi-N11: Nsmf_PDUSession_*` | `gmm/dereg.go:359, 369` | TS 29.502 SBI |
| `arch: sbi-N12 / sbi-N8 / sbi-N22 / sbi-N15` | enumerated in `gmm/doc.go:124-130` | replace plain function calls |

## 8. References

Spec citations grepped from `nf/amf/`:

- **TS 23.003** §2.2, §2.10.1, §19.4.2.3, §28.4 — identity / TAC encoding
- **TS 23.501** §4.2.6, §5.4.4.1, §5.6.1, §5.7.3, §5.9.4, §5.15.4, §5.15.5.2.1, §5.17.2.x, §6.2.1, §6.2.6
- **TS 23.502** §4.2.2.2.2, §4.2.2.3.3, §4.2.3, §4.2.3.2, §4.2.3.3, §4.2.6, §4.3.2.2.1, §4.3.4.3, §4.9.1.3, §4.11, §4.13.3.5, §5.2.2.2
- **TS 23.527**, **TS 23.041**
- **TS 24.007** §11.2.3.1.1A
- **TS 24.008** §10.5.1.4, §10.5.6.3
- **TS 24.011** §7.2, §7.2.2
- **TS 24.501** §4.4, §4.4.3.x, §4.4.4.x, §4.4.5, §4.4.6, §5, §5.1.3.x, §5.3.3, §5.3.7, §5.4.1.x, §5.4.2.x, §5.4.3.x, §5.4.4.2, §5.4.5.x, §5.5.x, §10.2 — full 5GMM (NAS) spec for the AMF
- **TS 33.501** §6.4, §6.7.2, §6.8.1.x, §6.9.3, §A.8 (NAS keys), §A.9 (K_gNB)
- **TS 38.412** §7 (NGAP transport / SCTP streams)
- **TS 38.413** §8.2 (PDU Session Resource), §8.3 (UE Context / ICS), §8.4 (Handover), §8.6 (Initial UE / NAS Transport / Paging), §8.7 (NG Setup / NG Reset / Error Indication / AMF Configuration Update), §8.9 (PWS), §8.14.1 (UE Radio Capability Info), §9.x IE tables
- **TS 29.502 / 29.503 / 29.509 / 29.510 / 29.518 / 29.531 / 29.571** — SBI surfaces (consumer/producer) that AMF talks to (today via in-process function calls; SBI lift TODOs in `gmm/doc.go`)

---
*Last refreshed against commit `13a181d`.*

# EPC — Design Document

4G EPC interworking surface for the SA Core build: an MME shim that
terminates **S1AP** from connected eNBs (TS 36.413), drives the EPS
NAS state machine (TS 24.301) over the same UE-context store the AMF
uses for 5G, and parks **N26 mapped contexts** the AMF pushes during
5G→4G handover. There is no separate SGW/PGW — IP allocation and the
data plane are reused from the 5G SMF / UPF.

## 1. Role / Scope

The MME is the EPS control-plane peer for connected LTE eNBs. In this
build it is a **single-process side-car** to the AMF: the same UE
identity (IMSI / GUTI), the same security context store, and a shared
N26 cache for 5G ↔ 4G handover. S1AP wire decode/encode rides on the
already-compiled S1AP ASN.1 module under `codecs/asn1-go/protocols/s1ap`,
but the resolver gap (the same one NGAP had) keeps real on-wire
round-trips on a Phase-N TODO — current callers feed plain Go structs
into the EMM/ESM/S1AP handlers and consume the response maps directly
(see `access/epc/mme.go:8-17`).

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **S1-MME** | eNB ↔ MME | S1AP over SCTP (port 36412, PPID 18) | TS 36.413 | Handlers + eNB registry; no live SCTP listener |
| **S11** | MME ↔ SGW | GTP-C v2 | TS 29.274 | Stub — SGW/PGW collapsed into 5G SMF/UPF |
| **S5/S8** | SGW ↔ PGW | GTP-C v2 / GTP-U | TS 29.274 / 29.281 | Not present — handled by 5G SMF + UPF |
| **N26** | MME ↔ AMF | mapped-context push | TS 23.501 §5.17.2.2, TS 23.502 §4.11 | In-memory cache (single-registration mode) |
| **NAS (EPS)** | UE → MME | EPS NAS over RRC/S1AP | TS 24.301 | Attach / Detach / TAU / SR / Auth / SMC / Identity / HO |

## 2. Architecture

```
                      ┌──────────────────────────────────────────┐
                      │          5G AMF (same process)           │
                      │   nf/amf/gmm  / nf/amf/security          │
                      │   nf/amf/n26  ← peer of access/epc/n26   │
                      └─────────────────────┬────────────────────┘
                                            │ N26 mapped context push
                                            ▼
┌─────────────────────────── EPC MME side ─────────────────────────────┐
│                                                                      │
│  access/epc/mme.go         MME global ctx  (Default singleton)       │
│      │                     ENBCtx registry, Status() / Start(addr)   │
│      │                                                               │
│      ├── access/epc/mme/s1ap/                                        │
│      │     S1AP procedures: S1 Setup, Initial UE Msg, UL/DL NAS,     │
│      │     E-RAB Setup, Handover, Path Switch, UE Ctx Release.       │
│      │     Per-eNB registry keyed on enb_ip.                         │
│      │                                                               │
│      ├── access/epc/mme/emm/                                         │
│      │     EMM state machine + MmeUeCtx (per UE).                    │
│      │     NAS message router (HandleNASMessage) → Attach/Detach/    │
│      │     TAU/SR/Auth/SMC/Identity/Handover handlers.               │
│      │     Holds an in-memory N26 cache too — superseded by          │
│      │     access/epc/mme/n26/ for new code.                         │
│      │                                                               │
│      ├── access/epc/mme/esm/                                         │
│      │     EPS Session Management (default + dedicated bearers).     │
│      │     EBI 5–15 default, 6–15 dedicated. Reuses 5G SMF for IP    │
│      │     and UPF for data plane.                                   │
│      │                                                               │
│      └── access/epc/mme/n26/                                         │
│            MME-side mapped-context cache (TTL = 120 s).              │
│            ReceiveContextFromAMF / GetMappedContext / Consume.       │
│            ForwardContextToAMF is a 4G→5G shim — real signalling     │
│            lives in nf/amf and the EPC handler.                      │
└──────────────────────────────────────────────────────────────────────┘
                                            ▲
                                            │ S1AP/SCTP (handlers exist; listener TODO)
                                            │
                                          eNBs
```

The AMF-side peer cache is `nf/amf/n26/` — see that package for the 5G
side of the same protocol. The two caches are intentionally
mirror-symmetric (store → consume, TTL=120 s) so a handover trace
reads identically on either end.

## 3. File Map

| File | LOC | Role |
|------|----:|------|
| `access/epc/mme.go` | 89 | MME global context (`Default`), `ENBCtx`, `Status()` / `Start(addr)`. |
| `access/epc/mme/s1ap/s1ap.go` | 229 | S1AP procedures + per-eNB `enbRegistry`. SCTP port + PPID constants. |
| `access/epc/mme/emm/emm.go` | 430 | EMM state machine, `MmeUeCtx`, NAS router, EPS-AKA stub, embedded N26 cache (legacy). |
| `access/epc/mme/esm/esm.go` | 109 | PDN connectivity + dedicated bearer activation/deactivation. EBI allocator. |
| `access/epc/mme/n26/n26.go` | 197 | Spec-anchored MME-side N26 mapped-context cache (preferred surface). |

Tests:
- `access/epc/mme/n26/n26_test.go` (138 LOC) — TTL eviction, double-consume idempotency, multi-handover-in-flight scenario.

## 4. Wire Format / NAS Interactions (only what's in code)

### 4.1 S1AP procedures decoded today (`s1ap.go`)

| Procedure | Spec § | Direction | Handler |
|-----------|--------|-----------|---------|
| S1 Setup | TS 36.413 §8.7.1 | eNB → MME | `HandleS1SetupRequest` (s1ap.go:85) |
| Initial UE Message | TS 36.413 §8.6.1 | eNB → MME | `HandleInitialUEMessage` (s1ap.go:110) |
| Uplink NAS Transport | TS 36.413 §8.6.3 | eNB → MME | `HandleUplinkNASTransport` (s1ap.go:124) |
| Downlink NAS Transport | TS 36.413 §8.6.2 | MME → eNB | `GenerateDownlinkNASTransport` (s1ap.go:130) |
| E-RAB Setup Req/Resp | TS 36.413 §8.2.1 | both | `GenerateERABSetupRequest` / `HandleERABSetupResponse` (s1ap.go:142, 155) |
| Handover (S1) | TS 36.413 §8.4 | both | `HandleHandoverRequired` / `GenerateHandoverCommand` / `HandleHandoverNotify` (s1ap.go:163, 174, 179) |
| Path Switch (X2) | TS 36.413 §8.4.4 | eNB → MME | `HandlePathSwitchRequest` (s1ap.go:187) |
| UE Context Release | TS 36.413 §8.3.2 | both | `GenerateUEContextReleaseCommand` / `HandleUEContextReleaseComplete` (s1ap.go:200, 211) |

eNB context (TS 36.413 §9.2) is `EnbCtx` keyed on `enb_ip` in
`enbRegistry` (s1ap.go:38-41). PLMN identity + supported TACs are
captured but the receive path is a Go-struct API — not ASN.1 PER —
today.

### 4.2 EPS NAS messages routed today (`emm.go`)

`HandleNASMessage` dispatches by EPS NAS Message Type (TS 24.301
Table 9.8.1). Message types currently handled (emm.go:307-326):

| Type code | NAS message | Handler chain |
|-----------|-------------|---------------|
| `0x41` | Attach Request (TS 24.301 §5.5.1) | `HandleAttachRequest` → `InitiateAuthentication` → `GenerateAttachAccept` |
| `0x45` | Detach Request (§5.5.2) | `HandleDetachRequest` → `detach_accept` |
| `0x48` | TAU Request (§5.5.3) | `HandleTAURequest` |
| `0x4C` | Service Request (§5.6.1) | `HandleServiceRequest` |
| `0x53` | Authentication Response | `HandleAuthResponse` → `GenerateSecurityModeCommand` |
| `0x56` | Identity Response (§5.4.4) | `HandleIdentityResponse` |
| `0x5E` | Security Mode Complete (§5.4.3) | `HandleSecurityModeComplete` → `GenerateAttachAccept` |

State transitions held on `MmeUeCtx`:
- `EMMState`: `DEREGISTERED` → `COMMON-PROCEDURE-INITIATED` (on Attach)
  → `REGISTERED` (on Attach Accept) → `DEREGISTERED` (Detach).
- `ECMState`: `IDLE` ↔ `CONNECTED` (Service Request / context release).
- `HOState`: `IDLE` → `PREPARING` → `COMPLETED` | `CANCELLED`.

EPS-AKA today is a stub (`InitiateAuthentication` / `HandleAuthResponse`
in emm.go:202-221) — `SecurityCtx.AuthDone = true` is set without an
actual AUSF/UDM round-trip.

### 4.3 ESM bearer model (`esm.go`)

| Procedure | Spec § | Handler |
|-----------|--------|---------|
| PDN Connectivity Request | TS 24.301 §8.3.18 | `HandlePDNConnectivityRequest` (esm.go:32) — allocates EBI 5–15, defaults DNN=`internet`, QCI=9 |
| Dedicated Bearer Activation | TS 24.301 §8.3.1 | `ActivateDedicatedBearer` (esm.go:66) — EBI 6–15, QCI=1 (conversational voice), GBR 128 kbps |
| Dedicated Bearer Deactivation | TS 24.301 §8.3.7 | `DeactivateDedicatedBearer` (esm.go:98) |

EBI allocation policy is hard-coded per TS 24.301 §6.5.1
(`esm.go:39`): default 5–15, dedicated 6–15.

## 5. Headline Procedures

### 5.1 5G→4G handover (single-registration mode, N26 path)

This is the only mode the cache supports today.
TS 23.501 §5.17.2.2.1 (single-registration UEs) and
TS 23.501 §5.17.2.2.2 (mobility lifecycle) — see `n26.go:18-29`.

```
AMF (5G)                                MME (4G)                         eNB
   │                                       │                              │
   │── ReceiveContextFromAMF(IMSI,         │                              │
   │     KASME, EPSBearers, UEInfo) ──────▶│  store mappedContexts[IMSI]  │
   │                                       │  Timestamp = now             │
   │                                       │  TTL = 120 s                 │
   │                                       │                              │
   │  …UE re-attaches over LTE…            │                              │
   │                                       │◀──── Attach Request ─────────│
   │                                       │                              │
   │                                       │  GetMappedContext(IMSI)      │
   │                                       │     → if expired: drop       │
   │                                       │     → if used: drop          │
   │                                       │  ConsumeMappedContext(IMSI)  │
   │                                       │  promote to MmeUeCtx         │
   │                                       │── Attach Accept ────────────▶│
```

TTL = 120 s (`n26CTXttl`, n26.go:57) — Rel-15+ recommendation that an
EPS attach following a 5G→4G handover lands well within two minutes.
Older entries are evicted lazily on lookup
(`GetMappedContext`, n26.go:109-124).

### 5.2 4G→5G handover (forwarding shim)

`ForwardContextToAMF(imsi)` (n26.go:147 / emm.go:421) is a notification
only. The MME announces the UE's intent to move; the AMF pulls the
live EPS context via S1 and derives the 5G mapped context server-side.
The TODO at n26.go:144 marks where the actual mapped-context derivation
(EPS bearer → 5G PDU session) belongs once the EPC handler can hand
us live UE state.

### 5.3 Single-registration vs dual-registration

Code only models **single-registration** N26-mode interworking
(`access/epc/mme/n26` doc-comment, n26.go:21-25). Dual-registration
mode (TS 23.501 §5.17.2.3, "Interworking without N26") is explicitly
out of scope — see the deferred-TODO at n26.go:32-35.

### 5.4 Intra-LTE handovers

S1-handover via the MME (TS 36.413 §8.4): `HandleHandoverRequired` →
`GenerateHandoverCommand` → `HandleHandoverNotify` (s1ap.go:163-181) +
matching state transitions on `MmeUeCtx.HOState` (emm.go:279-296).
X2-handover Path Switch (TS 36.413 §8.4.4): `HandlePathSwitchRequest`
(s1ap.go:187).

## 6. Key Types / Public API

```go
// access/epc/mme.go
type MME struct {
    Name       string
    MMEGI      uint16   // MME Group ID
    MMEC       uint8    // MME Code
    S1APAddr   string
    Running    bool
    StartedAt  time.Time
    ENBs       map[string]*ENBCtx
}
var Default = &MME{...}                // process-wide singleton
func (m *MME) Status() map[string]any
func (m *MME) Start(addr string)

// access/epc/mme/emm/emm.go
type MmeUeCtx struct {
    MmeUeS1apID, EnbUeS1apID int
    IMSI, IMEISV, GUTI       string
    EMMState, ECMState       string  // DEREG/REG/COMMON-PROC-INIT, IDLE/CONNECTED
    AttachType               string  // eps | combined | emergency
    PLMN                     PLMNIdentity
    TAI, ECGI                string
    SecurityCtx              SecurityContext  // KASME / KNASenc / KNASint / NAS counts
    EPSBearers               map[int]BearerInfo
    DefaultEBI               int
    HOState                  string           // IDLE/PREPARING/PREPARED/COMPLETED/CANCELLED
    HOSourceEnbIP, HOTargetEnbIP string
}
func NewMmeUeCtx(enbUeS1apID int) *MmeUeCtx
func RegisterIMSI(ctx *MmeUeCtx, imsi string)
func SearchByMmeID(id int) *MmeUeCtx
func SearchByIMSI(imsi string) *MmeUeCtx
func HandleNASMessage(ctx *MmeUeCtx, msgType int, payload []byte) map[string]any

// access/epc/mme/n26/n26.go (preferred surface)
type MappedContext struct {
    KASME      []byte
    EPSBearers []map[string]interface{}
    UEInfo     map[string]interface{}
    Timestamp  float64
    Used       bool
}
func ReceiveContextFromAMF(imsi string, kasme []byte, bearers []map[string]any, ueInfo map[string]any) map[string]any
func GetMappedContext(imsi string) *MappedContext
func ConsumeMappedContext(imsi string) *MappedContext
func ForwardContextToAMF(imsi string) map[string]any
func GetN26Status() map[string]any
func CleanupExpired() int

// access/epc/mme/s1ap/s1ap.go
const (
    S1APSCTPPort = 36412
    S1APPPID     = 18
)
type EnbCtx struct {
    EnbIP, EnbName, EnbID string
    Connected             bool
    TACs                  []string
    PLMNIdentity          []byte
    PagingDRX             string
    ConnectedAt           time.Time
}
```

## 7. Stubs / TODOs from code grep

| File:line | Status | Notes |
|-----------|--------|-------|
| `mme.go:14-17` | Resolver gap | S1AP ASN.1 module compiles but real on-wire round-trips need the same resolver-gap work that NGAP completed. |
| `emm.go:202-213` | Stub | `InitiateAuthentication` does not call AUSF/UDM — sets `AuthDone=true` for tests. |
| `emm.go:351-418` | Legacy / superseded | The N26 cache embedded in `emm` predates `access/epc/mme/n26`. New code should use the dedicated package. |
| `esm.go:51-55` | Stub | "In production: allocate IP via SMF, create UPF session" — today the bearer is a struct with no UPF state. |
| `esm.go:81-83` | Stub | "In production: look up service definition for QCI/GBR" — dedicated bearers hard-code QCI=1, GBR=128 kbps. |
| `n26.go:144-146` | TODO TS 23.502 §4.11 | `ForwardContextToAMF` — wire the actual mapped-context derivation (EPS bearer → 5G PDU session) once the EPC handler can hand it live UE state. |
| `n26.go:32-35` | Out of scope | TS 23.501 §5.17.2.3 — Interworking *without* N26. The cache in this package is N26-only by design. |

## 8. References

Only TS clauses already cited in `access/epc/`:

- **TS 23.401** §5.5.1 (Handover), §5.7.2 (MME UE Context) — `emm.go:22, 276`
- **TS 23.501** §5.17.2.2 (Interworking with N26), §5.17.2.2.1 (General — single-registration), §5.17.2.2.2 (Mobility for single-registration UEs), §5.17.2.3 (Interworking without N26 — *out of scope*) — `n26.go:18-35`
- **TS 23.502** §4.11 (System interworking with EPC) — `n26.go:27, 144`
- **TS 24.301** §5.4.3 (Security Mode), §5.4.4 (Identity), §5.5.1 (Attach), §5.5.2 (Detach), §5.5.3 (TAU), §5.6.1 (Service Request), §6.5 (ESM), §6.5.1 (EBI allocation), §8.3.1 (Dedicated Bearer Activation), §8.3.7 (Dedicated Bearer Deactivation), §8.3.18 (PDN Connectivity), Table 9.8.1 (NAS message types) — `emm.go:3,150,184,224,242,256,267,305`, `esm.go:3,30,39,65,97`
- **TS 33.401** §6.2 (EPS security context) — `emm.go:53, 200`
- **TS 36.413** §8.2.1 (E-RAB Setup), §8.3.2 (UE Context Release), §8.4 (Handover), §8.4.4 (Path Switch), §8.6.1 (Initial UE Message), §8.6.2 / §8.6.3 (DL/UL NAS Transport), §8.7.1 (S1 Setup), §9.2 (Messages and Elementary Procedures — eNB context) — `s1ap.go:24,82,107,121,139,160,184,197`

Cross-link: the AMF-side peer of N26 lives at
`nf/amf/n26/n26.go` — that package is the 5G side of the same
protocol. They share TS 23.501 §5.17.2.2 and TS 23.502 §4.11 as
spec anchors and present mirror-symmetric APIs (`ReceiveContextFromMME`
/ `ForwardContextToMME` on the AMF side).

---
*Last refreshed against commit `13a181d`.*

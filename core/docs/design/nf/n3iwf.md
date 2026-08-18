# N3IWF — Design Document

3GPP TS 23.501 §6.3.1 / TS 24.502-aligned Non-3GPP Inter-Working
Function for the MMT 5G Core. Lets a UE attach to 5GC over **untrusted
non-3GPP access** (typical case: untrusted Wi-Fi). The UE establishes
an IKEv2 SA + IPsec child SAs to the N3IWF; 5G NAS rides inside the
IKEv2 IKE_AUTH exchanges via EAP-5G; user-plane IP packets ride in
ESP/UDP-encapsulated tunnel mode and are bridged to GTP-U/N3 toward
the UPF.

## 1. Role in 5GC

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **NWu** | UE | IKEv2 (UDP/500 + UDP/4500 NAT-T), ESP-in-UDP (UDP/4500) | TS 24.502 §7.3 / §7.4, RFC 7296, RFC 4303 |
| **N2** | AMF | NGAP/SCTP (PPID 60, the same wire as the gNB) | TS 38.413, TS 38.412 §7 |
| **N3** | UPF | GTP-U over UDP/2152 | TS 29.281 |

The N3IWF:

- Terminates IKEv2 + EAP-5G with the UE (RFC 7296 §1.2/§1.3 + TS
  24.502 §9.3.2).
- Carries the UE's NAS over NWu (EAP-5G "5G-NAS" message-id 2 inside
  IKE_AUTH SK payloads) and bridges it onto N2 with the AMF —
  inbound EAP-5G/5G-NAS → NGAP UplinkNASTransport, AMF DL NAS →
  EAP-5G DL.
- For each PDU session, sets up an IPsec child SA on NWu and a GTP-U
  tunnel on N3, and bridges packets pure-Go (no kernel IPsec stack)
  in `userplane.Bridge`.

## 2. Architecture

```
                          ┌─────────────────────────┐
                          │           UE            │
                          │ wpa_supplicant + IKEv2  │
                          └──────────────┬──────────┘
                                  IKEv2 / ESP-in-UDP
                                          │
                              UDP/500 + UDP/4500 (NAT-T)
                                          ▼
┌──────────────────────────────  N3IWF process  ──────────────────────────────────┐
│                                                                                   │
│  transport.go (pure UDP listener)                                                  │
│    UDP/500   ──── IKE messages                                                    │
│    UDP/4500  ──── IKE-NAT-T or ESP-in-UDP (demux on first 4 bytes per RFC 7296    │
│                   §3.1 / RFC 4303 §2: zeros = IKE, non-zero = ESP SPI)           │
│    UDP/2152  ──── GTP-U / N3                                                      │
│                                                                                   │
│            ┌──────────────────┐         ┌─────────────────────────────────────┐  │
│            │ ikev2/ wire      │         │  handler/ — IKEv2 state machine      │  │
│            │  header.go       │  ────▶  │  handleIKESAInit (RFC 7296 §1.2)     │  │
│            │  payload.go      │         │  handleIKEAuth (TS 24.502 §7.3.2.1) │  │
│            │  payloads_simple │         │  handleCreateChildSA (RFC 7296 §1.3)│  │
│            │  sa.go (proposal)│         │  handleInformational                │  │
│            │  dh.go (RFC3526) │         │  handler_ikeauth_final.go (final RT)│  │
│            │  prf.go (HMAC-256)│        └────────┬────────────────────────────┘  │
│            │  sk.go (AES-CBC) │                  │                                 │
│            │  cp.go (CFG)     │                  │ EAP-5G/5G-NAS payload          │
│            └──────────────────┘                  ▼                                 │
│                                          ┌──────────────┐                          │
│            ┌──────────────────┐          │ eap5g/       │                          │
│            │ ctx/ Manager     │          │   §9.3.2     │                          │
│            │  per-UE state    │          │   5G-Start / │                          │
│            │  ChildSA[]       │          │   5G-NAS /   │                          │
│            └──────────────────┘          │   5G-Notif / │                          │
│                                          │   5G-Stop    │                          │
│                                          └──────┬───────┘                          │
│                                                 │ NAS bytes                         │
│                                                 ▼                                  │
│            ┌────────────────────────────────────────────────────┐                  │
│            │ n2/ Manager — N2 SCTP to AMF                       │                  │
│            │   ngsetup.go   NG Setup (TS 38.413 §8.7.1)        │                  │
│            │   initialctx.go  Initial Context Setup hand-off   │                  │
│            │   nas_transport.go  UL/DL NAS Transport relay     │                  │
│            │   bridge_adapter.go  handler.NASBridge impl       │                  │
│            └────────────────────────────────────────────────────┘                  │
│                                                                                   │
│            ┌──────────────────┐    ┌──────────────────┐  ┌──────────────────┐  │
│            │ esp/ RFC 4303    │    │ gtpu/ TS 29.281  │  │ ipool/ inner-IP  │  │
│            │  AES-CBC-256 +  │    │  G-PDU encap     │  │ allocator (CP    │  │
│            │  HMAC-SHA-256   │    │                  │  │ CFG_REPLY)       │  │
│            └──────────────────┘    └──────────────────┘  └──────────────────┘  │
│                              ▼                ▼                                   │
│                     ┌──────────────────────────────────┐                          │
│                     │ userplane/ Bridge                 │                          │
│                     │   per-UE-PDU-session: ESP↔GTP-U   │                          │
│                     │   HandleNWu / HandleN3            │                          │
│                     │   Registry for transport demux    │                          │
│                     └──────────────────────────────────┘                          │
└──────────────────────────────────────────────────────────────────────────────────┘
                                          │ GTP-U / N3
                                          ▼
                                  ┌──────────────┐
                                  │     UPF      │
                                  └──────────────┘
```

## 3. Package / file map

| Package | LOC | Role |
|---------|-----|------|
| `nf/n3iwf` (root) | ~470 | Boot wiring (`Start`), `LoadConfig` from `n3iwf_config`, `Listen`/`Transport` (UDP/500 + 4500 NAT-T + 2152 GTP-U), demux per RFC 7296 §3.1. |
| `nf/n3iwf/ctx` | ~390 | `Manager` (per-UE registry by IPport / SPIi), `UEContext` struct (IKE keys, DH, ChildSAs, NonceI/R, NAS routing), `ChildSA` per session (signalling vs user-plane). `FreshSPIr()`. |
| `nf/n3iwf/ikev2` | ~2400 | IKEv2 wire codec (RFC 7296). `header.go`, `payload.go`, `payloads_simple.go` (Notify/IDi/IDr/AUTH/CERTREQ/CP/EAP/Delete), `sa.go` (proposal/transform), `dh.go` (RFC 3526 MODP groups), `prf.go` (HMAC-SHA-256 + KDF), `sk.go` (AES-CBC + HMAC-256-128 SK encrypt/decrypt), `cp.go` (CFG_REQUEST/REPLY §3.15). |
| `nf/n3iwf/eap5g` | ~620 | TS 24.502 §9.3.2 EAP-5G expanded EAP method. `Encode/Decode` + per-message-id helpers (5G-Start id=1, 5G-NAS id=2, 5G-Notification id=3, 5G-Stop id=4). 3GPP Vendor-Id 10415, Vendor-Type 3. |
| `nf/n3iwf/handler` | ~2200 | IKEv2 protocol state machine. `handler.go` (dispatch), `handleIKESAInit` / `handleIKEAuth` / `handleCreateChildSA` / `handleInformational`, `ikeauth_final.go` (final IKE_AUTH after EAP-Success), `createchildsa.go` (per-session ESP SA + DL TEID assignment). `bridge.go` (NASBridge interface). |
| `nf/n3iwf/n2` | ~1700 | SCTP/N2 manager toward AMF. `manager.go` (one SCTP assoc, recv loop, per-UE callback table by RAN-UE-NGAP-ID), `ngsetup.go` (NG Setup procedure), `initialctx.go` (handle Initial Context Setup, derive K_N3IWF), `nas_transport.go` (UL/DL NAS Transport with NAS-PDU IE), `bridge_adapter.go` (adapter to `handler.NASBridge`), `transport_linux.go` (real SCTP). |
| `nf/n3iwf/esp` | ~600 | RFC 4303 ESP encap/decap in tunnel mode. AES-CBC-256 + HMAC-SHA-256-128 (RFC 4868 §2.1), 64-packet replay window (§3.4.3). |
| `nf/n3iwf/gtpu` | ~360 | TS 29.281 GTP-U G-PDU encap/decap. |
| `nf/n3iwf/userplane` | ~540 | `Bridge` (one per UE PDU session): owns `SAIn`/`SAOut` (ESP) + `TEIDUp`/`TEIDDown` + `UEAddr`/`UPFAddr` UDP endpoints. `HandleNWu` (ESP in → GTP-U out) / `HandleN3` (GTP-U in → ESP out). `Registry` keyed by inbound SPI for transport demux. |
| `nf/n3iwf/ipool` | ~260 | `Pool` over a CIDR — first host reserved as gateway, rotates UE inner-IP across remaining hosts. Used to fill CP(CFG_REPLY) `INTERNAL_IP4_ADDRESS` (TS 24.502 §7.3.2.2 / RFC 7296 §3.15). |

## 4. Wire interactions

### 4.1 IKEv2 exchanges handled

`handler/handler.go:152-177` — dispatched on `hdr.ExchangeType`:

| Exchange | Spec | Handler |
|----------|------|---------|
| IKE_SA_INIT (34) | RFC 7296 §1.2 | `handleIKESAInit` — pick proposal, RFC 3526 DH, derive SK_d/SK_ai/SK_ar/SK_ei/SK_er per §2.14, SPIr, response |
| IKE_AUTH initial (35) | TS 24.502 §7.3.2.1 + RFC 7296 §2.16 | `handleIKEAuth` — UE's IDi (typically ID_KEY_ID), respond with IDr + EAP-Request/5G-Start |
| IKE_AUTH N | TS 24.502 §7.3.3 / §9.3.2 | EAP-5G/5G-NAS round-trips; bridge UL NAS to AMF via N2; on AMF DL NAS feed back EAP-Request |
| IKE_AUTH final | TS 24.502 §7.3.2.2 + RFC 7296 §2.15 | `ikeauth_final.go` — verify UE AUTH (Knh / K_N3IWF), emit our AUTH, signalling Child SA keys (§2.17), [CP(CFG_REPLY)] inner-IP |
| CREATE_CHILD_SA (36) | RFC 7296 §1.3 + TS 24.502 §7.4 | `handleCreateChildSA` — per-PDU-session ESP SAs, optional PFS DH (§1.3.1), DL TEID assignment |
| INFORMATIONAL (37) | RFC 7296 §1.4 | `handleInformational` — DELETE / NOTIFY / liveness |

All IKE_AUTH and beyond are wrapped in §3.14 SK payloads. The chosen
suite (today: AES-CBC-256 + HMAC-SHA-256-128 + PRF-HMAC-SHA-256 +
MODP-2048) lives at `ikev2.IKEDefaultProposal`; first match in the
UE's proposal list wins (`handler/handler.go:215-218`).

### 4.2 Transport demux

`transport.go` listens on three UDP sockets (RFC 7296 §3.1 +
TS 29.281 §4.4.2):

| Socket | First 4 bytes | Path | Spec |
|--------|---------------|------|------|
| UDP/500 | — | IKE messages → `handler.Handle` | RFC 7296 §3.1 |
| UDP/4500 | `00 00 00 00` (Non-ESP marker) | IKE NAT-T → `handler.Handle` (skip 4 zeros) | RFC 7296 §3.1 |
| UDP/4500 | non-zero (ESP SPI) | ESP-in-UDP → `userplane.Registry.LookupBySPI` → `Bridge.HandleNWu` | RFC 4303 §2 |
| UDP/2152 | — | GTP-U / G-PDU → `Bridge.HandleN3` (lookup by inbound TEID) | TS 29.281 |

When `userplane.Registry` is empty (no Bridge registered yet) the
ESP path silently drops per RFC 4303 §3.4.2.

### 4.3 EAP-5G wire (TS 24.502 §9.3.2)

Common header per `eap5g/eap5g.go:36-54`:

```
EAP Code (1)  Identifier (1)  Length (2)
Type = 254 (Expanded, RFC 3748 §5.7)
Vendor-Id (3) = 0x00 0x28 0xAF (decimal 10415)
Vendor-Type (4) = 3 (EAP-5G method id)
Message-Id (1) = 1 (5G-Start) | 2 (5G-NAS) | 3 (5G-Notification) | 4 (5G-Stop)
Spare (1)
[Message-Id-specific TLVs]
```

5G-NAS body carries one TLV: `T = 0x01` (NAS-PDU), `L = 2 bytes`,
`V = NAS bytes`. The handler extracts `V` and forwards via
`bridge.UplinkNAS(ueID, NASBytes)`. AMF-originated DL bytes come back
through the bridge and are wrapped as EAP-Request/5G-NAS in the next
IKE_AUTH SK response.

### 4.4 N2 (NGAP) toward AMF

`n2/manager.go` — single SCTP one-to-one association, mutex-serialised
sends. Stream 0 reserved for non-UE-associated procedures (NG Setup /
NG Reset). UE-associated streams hash by RAN-UE-NGAP-ID. Inbound recv
goroutine fans messages out via `UENGAPCtx.OnDownlinkNAS` /
`OnInitialContextSetup`. RAN-UE-NGAP-ID is allocated by the N3IWF
itself per TS 38.413 §9.3.3.2 (`Manager.nextRANID`).

## 5. Lifecycle — Untrusted-WLAN attach

### 5.1 Phase 1 — IKE SA (RFC 7296 §1.2 + TS 24.502 §7.3.2.1)

```
UE                                    N3IWF                                   AMF/UPF
 │── HDR(SPIi=X, SPIr=0), SAi1, KEi, Ni ──▶│
 │   IKE_SA_INIT request (UDP/500)         │  pickAcceptableProposal
 │                                          │  ctx.Manager.Create(srcIP, port)
 │                                          │  spir = FreshSPIr()
 │                                          │  DH (RFC 3526) → g^ir
 │                                          │  §2.14 SKEYSEED → SK_d, SK_a*, SK_e*
 │                                          │  store as UE.Keys + UE.SuiteI/SuiteR
 │◄── HDR(SPIi=X, SPIr=Y), SAr1, KEr, Nr ──│
 │   IKE_SA_INIT response                   │
 │                                          │
 │── HDR(...), SK{ IDi, [CERTREQ], SAi2,    │
 │             TSi, TSr } ─────────────────▶│
 │   IKE_AUTH initial (no AUTH; UE asks for │
 │   EAP per TS 24.502 §7.3.2.1)            │
 │                                          │  decrypt SK, parse IDi
 │                                          │  emit IDr=N3IWF identity
 │                                          │  emit EAP-Request/5G-Start (id=1)
 │◄── HDR(...), SK{ IDr, EAP-Req/5G-Start,  │
 │           [CERT] } ──────────────────────│
```

### 5.2 Phase 2 — EAP-5G NAS exchange (TS 24.502 §7.3.3 / §9.3.2)

```
 │── HDR, SK{ EAP-Resp/5G-NAS(Reg.Req) } ──▶│
 │                                          │   bridge.UplinkNAS(ueID, NAS)
 │                                          │   → n2 NGAP InitialUEMessage to AMF
 │                                          │
 │   ... AMF runs primary auth via AUSF ... │
 │                                          │
 │◄── HDR, SK{ EAP-Req/5G-NAS(AuthReq) } ──│   bridge waits for DL NAS (5s timeout)
 │── HDR, SK{ EAP-Resp/5G-NAS(AuthResp)} ─▶│
 │   (repeated for SMC, etc.)               │
 │                                          │
 │   AMF eventually issues NGAP InitialContextSetupRequest
 │   carrying SecurityKey IE = K_N3IWF (TS 33.501 §A.9 access=0x02)
 │                                          │   n2.HandleICS → store K_N3IWF on UE
 │                                          │   emit EAP-Success (RFC 3748 §4.2)
 │◄── HDR, SK{ EAP-Success } ──────────────│
```

### 5.3 Phase 3 — IKE_AUTH final (TS 24.502 §7.3.2.2 + RFC 7296 §2.16)

```
 │── HDR, SK{ AUTH, SA, TSi, TSr,           │
 │           [CP(CFG_REQUEST)] } ─────────▶│  verify AUTH (RFC 7296 §2.15 with K_N3IWF)
 │   UE authenticates with K_N3IWF over    │  on success: derive signalling Child SA
 │   InitiatorSignedOctets                 │    keys (§2.17), allocate inner-IP (CP),
 │                                          │    bind ESP SAs
 │◄── HDR, SK{ AUTH, SA, TSi, TSr,         │  emit our AUTH; CP(CFG_REPLY) with
 │           [CP(CFG_REPLY)] } ────────────│    INTERNAL_IP4_ADDRESS = ipool.Allocate()
 │   Signalling IPsec SA up                 │
```

### 5.4 Phase 4 — Per-PDU-session Child SA + N3 (RFC 7296 §1.3 + §2.17)

```
 │   AMF triggers PDU Session setup via    │
 │   PDUSessionResourceSetupRequest. UPF   │
 │   provides UPF-side TEID (TEIDUp).       │
 │                                          │  N3IWF starts CREATE_CHILD_SA flow:
 │◄── HDR, SK{ CREATE_CHILD_SA, SAr2,      │    SAr2 with fresh ESP SPIs,
 │           [KEr], Nr, TSi, TSr } ────────│    Notify(USE_TRANSPORT_MODE? no — tunnel),
 │── HDR, SK{ N(REKEY_SA?), SAi2,          │    TS 24.502 §7.4
 │           [KEi], Ni, TSi, TSr } ────────▶│  derive ChildSA keys (§2.17 KEYMAT)
 │                                          │  allocate TEIDDown for N3IWF
 │                                          │  handler.RegisterUPSA(...) →
 │                                          │    userplane.NewBridge(SAIn, SAOut,
 │                                          │      teidUp, teidDown,
 │                                          │      UEAddr, UPFAddr)
 │                                          │
 │── ESP-in-UDP/4500 (UE → UPF) ──────────▶│  Bridge.HandleNWu: ESP decap →
 │                                          │    GTP-U encap → UDP/2152 → UPF
 │◄── ESP-in-UDP/4500 (UPF → UE) ──────────│  Bridge.HandleN3: GTP-U decap →
 │                                          │    ESP encap → UDP/4500 → UE
```

### 5.5 Teardown

INFORMATIONAL with DELETE payload (RFC 7296 §3.11) — handled in
`handler/handler.go:handleInformational`. Ctx.Manager removes the UE,
Bridge unregistered from `userplane.Registry`. N2 NGAP UE Context
Release runs in parallel via `n2.Manager`.

## 6. Key types / public API

```go
// nf/n3iwf/n3iwf.go
type Config struct {
    Enabled bool
    N3IWFIP string
    IKEPort int; IKENATPort int
    InnerIPPool string         // CIDR for UE inner-IP allocator
    IPSecEncAlgo, IPSecIntAlgo string
    DHGroup int                // RFC 3526 group num
    SupportedDNNs, SupportedNSSAI string
    AMFAddr string             // "host[:port]" — empty keeps IKE-only mode
    PLMNID string; N3IWFID int; TAC string
}
func LoadConfig() (*Config, error)
func Start(parent context.Context) error  // graceful no-op when disabled
func Listen(cfg ListenConfig) (*Transport, error)
func (t *Transport) Serve(ctx context.Context) error

// nf/n3iwf/handler/handler.go
type Handler struct{ ... }
type NASBridge interface { /* UplinkNAS, ... */ }
func New(mgr *ctx.Manager, identity string) *Handler
func (h *Handler) SetBridge(b NASBridge)
func (h *Handler) SetRegistry(r *userplane.Registry)
func (h *Handler) SetInnerIPPool(p *ipool.Pool)
func (h *Handler) Handle(msg []byte, src *net.UDPAddr) ([]byte, error)
func (h *Handler) RegisterUPSA(ueID, childIdx int, teidUp uint32, ueAddr, upfAddr *net.UDPAddr) error

// nf/n3iwf/ctx/ctx.go (~340 lines)
type Manager struct { ... }
type UEContext struct {
    IKEInitiator, IKEResponder [8]byte // SPIs
    IKENonceI, IKENonceR []byte
    DH      DH
    DHPriv, DHPub, SharedKey []byte
    Keys    *prf.IKESAKeys
    SuiteI, SuiteR sk.Suite
    EncrID, IntegID, PRFID int
    EncrKeyLen int
    State   State
    ChildSAs []ChildSA            // [0] = signalling, [1..] = per-PDU-session UP
    NAS     interface{...}        // RAN-UE-NGAP-ID + DL NAS chan
    InnerIP netip.Addr
    // ...
}
type ChildSA struct {
    SPIIn, SPIOut             uint32
    EncrKeyIn, IntegKeyIn     []byte
    EncrKeyOut, IntegKeyOut   []byte
    Signalling                bool
    TEIDDown                  uint32  // N3IWF-allocated downlink TEID (UPF→N3IWF)
    PDUSessionID              uint8
}
func (m *Manager) Create(ip string, port int) *UEContext
func (m *Manager) LookupByAddr(ip string, port int) *UEContext
func (m *Manager) LookupByID(id int) *UEContext
func (m *Manager) RegisterSPIi(u *UEContext)

// nf/n3iwf/userplane/bridge.go
type Bridge struct{ SAIn, SAOut *esp.SA; TEIDUp, TEIDDown uint32; UEAddr, UPFAddr *net.UDPAddr }
func NewBridge(spiIn, spiOut uint32, encrIn, integIn, encrOut, integOut []byte,
               teidUp, teidDown uint32) (*Bridge, error)
func (b *Bridge) HandleNWu(esp []byte) (gtpu []byte, err error)
func (b *Bridge) HandleN3(gtpu []byte) (esp []byte, err error)

type Registry struct{ /* SPI → *Bridge map */ }
func (r *Registry) Add(b *Bridge) error
func (r *Registry) LookupBySPI(spi uint32) *Bridge
func (r *Registry) LookupByTEID(teid uint32) *Bridge

// nf/n3iwf/n2/manager.go
type Manager struct{ /* SCTP conn + per-UE routing table */ }
type ManagerConfig struct{ Dial DialConfig; NGSetup *NGSetupConfig }
func NewManager(parent context.Context, cfg ManagerConfig) (*Manager, error)
func NewBridgeAdapter(m *Manager) handler.NASBridge

// nf/n3iwf/eap5g/eap5g.go
type MessageID uint8
const ( ID5GStart=1; ID5GNAS=2; ID5GNotification=3; ID5GStop=4 )
const ( VendorID3GPP=10415; VendorTypeEAP5G=3 )
func Encode5GStart(id uint8) []byte
func Encode5GNAS(id uint8, nas []byte, anParameters []byte) []byte
func DecodeExpanded(eap []byte) (code uint8, msgID MessageID, body []byte, err error)
```

## 7. What's not implemented

Grepped against `nf/n3iwf/` source:

| Area | Status | Source |
|------|--------|--------|
| AEAD modes (e.g. AES-GCM-256) for ESP / IKE | not implemented; AES-CBC-256 + HMAC-SHA-256-128 only | `esp/esp.go:7-12` |
| INTEG algorithms beyond `INTEG_HMAC_SHA256_128` | rejected with error | `handler/handler.go:286-293` |
| PFS DH on CREATE_CHILD_SA (RFC 7296 §1.3.1) | optional KEi parsed but new shared-secret path scaffold | `handler/createchildsa.go:99-111` (TODO comment) |
| Trusted non-3GPP access (TNGF / TWIF / WAGF) | scaffold; access-distinguisher 0x02 noted but not switched | shares lots of code, not implemented |
| Nudm / NRF registration | not implemented (no SBI) | — |
| INFORMATIONAL liveness probes | scaffold; DELETE handled, REKEY pieces partial | `nf/n3iwf/ikev2/delete_test.go` exists |
| Replay-window robust to wraparound + ESN (RFC 4303 §2.2.1) | 32-bit only, 64-pkt window | `esp/esp.go:64-78` |
| `n3iwf_test.go` integration test | unit-style only | (no E2E harness yet) |
| `bridge_iface_check.go` | enforces compile-time interface — still placeholder | `nf/n3iwf/bridge_iface_check.go` |
| TODO `// NGSetupRequest stub` | bytes asserted but full APER parity TBD | `n2/transport_test.go:67` |

## 8. References

Spec citations grepped from `nf/n3iwf/`:

- **TS 23.501** v19.7.0 §6.3.1 — N3IWF architecture
- **TS 23.003** §2.3 — PLMN encoding
- **TS 24.502** v19.3.0 §7.3 IKE SA establishment for untrusted
  non-3GPP access, §7.3.2.1 IKE_AUTH initial, §7.3.2.2 IKE_AUTH final +
  CFG_REPLY inner-IP, §7.3.3 EAP-5G round-trips, §7.4 user-plane
  Child SAs, §9.3.2 EAP-5G method (Vendor-Id 10415, Vendor-Type 3,
  Message-Ids 1..4)
- **TS 33.501** §A.9 K_gNB / K_N3IWF KDF (FC=0x6E; access-distinguisher
  0x02 for non-3GPP) — used in `n2/initialctx.go`
- **TS 38.412** §7 NGAP transport (SCTP streams)
- **TS 38.413** §8.7.1 NG Setup, §9.2.6.1 SupportedTAList, §9.3.1.5
  Global RAN Node ID, §9.3.3.10 TAC, §9.3.4.1 GTP Tunnel — N2 surface
- **TS 29.281** §4.4.2 GTP-U UDP port 2152 — N3 surface
- **RFC 7296** IKEv2 — full reference for `nf/n3iwf/ikev2` and `handler`
- **RFC 4303** ESP — full reference for `nf/n3iwf/esp`
- **RFC 4868** §2.1 HMAC-SHA-256-128 truncation
- **RFC 3526** MODP DH groups (1024 / 1536 / 2048 / 3072 / ...)
- **RFC 3748** EAP framework — Code/Type/Expanded
- **IETF "Assigned Internet Protocol Numbers"** — Next Header values
  (4 = IPv4, 41 = IPv6) used in ESP tunnel mode

---
*Last refreshed against commit `13a181d`.*

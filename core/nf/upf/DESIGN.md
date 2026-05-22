# UPF — Design Document

3GPP TS 29.244-aligned User Plane Function for the MMT 5G Core.
Cross-references between spec text and the Go/C source live throughout
so a reader can jump from any §-clause to the file that implements it.

## 1. Role in 5GC

The UPF (User Plane Function) is the data-plane anchor for PDU
sessions in 5G. Per **TS 23.501 §6.2.3** it terminates:

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **N3** | gNB / NG-RAN | GTP-U over UDP/IP | TS 29.281 |
| **N4** | SMF | PFCP over UDP/IP | TS 29.244 |
| **N6** | Data Network (Internet, IMS, …) | IP / Ethernet | implementation-defined |
| **N9** | Other UPFs (chaining) | GTP-U over UDP/IP | TS 23.501 §5.7.5 |

UPF responsibilities (TS 23.501 §6.2.3 verbatim list):

- Packet routing & forwarding
- Per-flow QoS handling (gating, MBR/GBR enforcement)
- Traffic usage reporting (volume, time, event)
- DL data buffering when the UE is idle
- DL Data Notification triggering (paging request to AMF via SMF)
- Branching point for multi-homed PDU sessions
- Lawful Interception (out of scope here)

This UPF implementation focuses on the SMF↔UPF separated deployment:
PFCP runs over UDP between SMF and UPF; the dataplane lives in C
under `dataplane/` and is linked into the same process or a separate
UPF binary. A second cgo bridge supports collapsed SMF+UPF for tests
and dev.

## 2. Architecture

```
                          ┌──────────────────────────────────────────┐
                          │                  SMF                     │
                          │  nf/smf/session/establish.go             │
                          │  nf/smf/upfclient/pfcp_bridge.go ────────┼─── PFCP (TS 29.244)
                          └──────────────────────────────────────────┘
                                                                     │
                                                                     │ N4
                                                                     ▼
┌────────────────────── UPF process ───────────────────────────────────────┐
│                                                                          │
│  Control plane (Go) ───────────────────────────────────────────────────  │
│   nf/upf/pfcp/handler.go      decode §7.5.x messages → ManagerHook      │
│   nf/upf/upfloop/             SCTP/UDP loop, hooks                       │
│   nf/upf/upfloop/bridge_hook  ManagerHook → upf.UPFBridge                │
│   nf/upf/upf.go               Manager (per-session state, FAR/PDR/...)   │
│                                                                          │
│  Bridge (Go ↔ C) ────────────────────────────────────────────────────────│
│   nf/upf/cgo_bridge.go        UPFBridge interface                       │
│   nf/upf/cgo_bridge_linux.go  dpdkBridge — calls into libupf_dp.so       │
│                                                                          │
│  Data plane (C) ─────────────────────────────────────────────────────────│
│   dataplane/include/*.h       APIs: pkt_io, classifier, gtpu, meter,    │
│                                     session_table, slice, sdf_parser    │
│   dataplane/src/*.c           libupf_dp.so — pthread reading N3 + N6,   │
│                                              packet processing,         │
│                                              hash lookups, GTP-U enc/dec│
└──────────────────────────────────────────────────────────────────────────┘
                                                                     ▲
                                                                     │ N3 (GTP-U)
                                                                     │
                              N6 ◄──────────────── data plane ────────┴──── gNB
```

### 2.1 Control-plane packages (Go)

| Package | Role |
|---------|------|
| `nf/upf/pfcp/handler.go` | Decodes §7.5.x messages, drives `ManagerHook` |
| `nf/upf/upfloop/` | UDP listener, association manager (TS 29.244 §7.4.4), report forwarder (§7.5.8) |
| `nf/upf/upfloop/bridge_hook.go` | Adapter: `ManagerHook` → `upf.UPFBridge` |
| `nf/upf/upf.go` | `Manager` struct: per-session state map, public API for SMF callers |
| `nf/upf/cgo_bridge.go` | `UPFBridge` interface (methods listed below) |
| `nf/upf/cgo_bridge_linux.go` | `dpdkBridge` impl — wraps the C library via cgo |
| `nf/upf/report.go` | Report struct + `DrainReports` for §7.5.8 forwarder |

### 2.2 Data-plane modules (C — `dataplane/`)

| Header | Role |
|--------|------|
| `upf_pkt_io.h` | select() on N3 (GTP-U UDP) + N6 (TUN); DPDK variant `upf_pkt_io_dpdk.c` |
| `upf_session_table.h` | hash tables — `teid_hash` (UL key), `ueip_hash` (DL key) |
| `upf_classifier.h` | match incoming packets against PDR rules (Source Iface, F-TEID, UE IP, SDF) |
| `upf_gtpu.h` | TS 29.281 GTP-U encap/decap |
| `upf_qos_meter.h` | TS 23.501 §5.7.2.6 Session-AMBR + per-QoS-flow MBR/GBR via `rte_meter` |
| `upf_sdf_parser.h` | TS 29.244 §8.2.5 SDF Filter Cisco-style flow descriptors |
| `upf_dpi.h` | optional DPI hook for application-aware policies |
| `upf_slice.h` | per-S-NSSAI session indexing |
| `upf_report.h` | ring-buffer for §7.5.8 reports back to control plane |
| `upf_dp_api.h` | C API the cgo bridge calls (`upf_dp_session_create`, `_add_pdr`, etc.) |

## 3. PFCP Wire Protocol (control plane)

### 3.1 PFCP messages we handle (TS 29.244 §7)

| Group | Type | Direction | Spec § |
|-------|------|-----------|--------|
| Node | Heartbeat Req/Resp | both | §7.4.2-3 |
| Node | Association Setup Req/Resp | both | §7.4.4 |
| Node | Association Update Req/Resp | both | §7.4.4.3 |
| Node | Association Release Req/Resp | both | §7.4.4.4 |
| Session | **Session Establishment Req/Resp** | CP→UP / UP→CP | §7.5.2 |
| Session | **Session Modification Req/Resp** | CP→UP / UP→CP | §7.5.4 |
| Session | **Session Deletion Req/Resp** | CP→UP / UP→CP | §7.5.6 |
| Session | **Session Report Req/Resp** | UP→CP / CP→UP | §7.5.8 |

Full message YAML inventory: `codecs/tlv-3gpp-pfcp/pfcpgen/definitions/pfcp_messages.yaml`.

### 3.2 Steady-state per-PDU-session message exchange

The deployment commits all session rules in **one** Establishment, then
issues **one** Modification when the gNB TEID becomes known.

```
SMF                                        UPF
 │                                          │
 │── PFCP Session Establishment Request ───▶│   §7.5.2  CP→UP
 │     NodeID                               │
 │     CP F-SEID                            │
 │     UserID (SUPI=IMSI, NAI=pduSessID)    │
 │     PDNType  (1=IPv4)                    │   §8.2.79
 │     APN-DNN ("ims"/"internet")           │   §8.2.117
 │     Create PDR (×2, UL+DL)               │   §7.5.2.2
 │       PDI: SourceInterface, F-TEID(UL),  │
 │            UE IP Address (DL, S/D=1),    │
 │            SDF Filter, QFI               │
 │     Create FAR (×2)                      │   §7.5.2.3
 │       FAR-1 UL: ApplyAction=FORW         │
 │       FAR-2 DL: ApplyAction=BUFF         │   §8.2.26
 │                  (no Outer Header — gNB  │
 │                   TEID arrives later)    │
 │     Create QER  (per-flow)               │   §7.5.2.5
 │     Create QER  (id=0xFFFFFFFE — Sess-AMBR) §7.5.2.5 + §5.7.2.6
 │     Create URR  (volume measurement)     │   §7.5.2.4
 │                                          │
 │◀── Session Establishment Response ───────│
 │     UP F-SEID                            │
 │                                          │
 │  ╭ AMF receives PDUSessionResource-      │
 │  │ SetupResponse from gNB carrying       │
 │  │ DL F-TEID (gNB-allocated)             │
 │  ╰ ActivateUserPlane(gnbTEID, gnbAddr)   │
 │                                          │
 │── PFCP Session Modification Request ────▶│   §7.5.4  CP→UP
 │     Update FAR (FAR-2 / DL)              │   §7.5.4.3
 │       ApplyAction=FORW                   │
 │       UpdateForwardingParameters         │
 │         OuterHeaderCreation              │   §8.2.56
 │           (GTP-U/UDP/IPv4 + gNB TEID +   │
 │            gNB IPv4)                     │
 │                                          │
 │◀── Session Modification Response ────────│
 │                                          │
 │   …  N6/N3 traffic flows  …               │
 │                                          │
 │  AN Release (gNB sends UEContextRelease)  │
 │── PFCP Session Modification Request ────▶│   §7.5.4
 │     Update FAR (FAR-2 / DL)              │
 │       ApplyAction=BUFF (no tunnel)       │   §8.2.26
 │                                          │
 │  UE-initiated PDU Session Release         │
 │── PFCP Session Deletion Request ────────▶│   §7.5.6
 │◀── Session Deletion Response ────────────│
```

Old (pre-`f13a27c`) shape sent 8 PFCP messages per session
(empty Establishment + 7 single-rule Modifications). Current shape
matches the spec's grouped IE design (TS 29.244 §7.5.2.2
Table 7.5.2.1-1).

### 3.3 PFCP message → IE → primitive mapping

The codec is generated end-to-end from YAML:

```
pfcp_messages.yaml         each message lists its IEs (presence + type_ref)
       │
       ▼
pfcp_ie_types.yaml         each IE: type_code, min/max length, fields
       │                   plus `kind: flag_conditional` / `go_type:` overrides
       ▼
pfcpgen/pkg/codegen        emits Go struct per message + per IE
       │
       ▼
generated/                 ie_*.go, msg_*.go, dispatcher.go
       │
       ▼
nf/upf/pfcp/handler.go     decodes, calls ManagerHook
nf/smf/upfclient/          builds + sends
```

Spec-typed primitives the schema understands today:

| YAML `type:` | Go shape | Wire |
|--------------|----------|------|
| `tbcd_digits` | `string` (digits) | TBCD-packed (TS 29.274 §8.3) |
| `utf8` | `string` | raw bytes, optional `length_prefix: u8` / `u16` |
| `uint16` / `uint24` / `uint32` / `uint64` | `*uintN` (nil = absent) | big-endian |
| `smmii_list` | `[][]byte` | count-prefixed list of u16-LV bodies |

`go_type:` runtime aliases (irregular IEs hand-coded in
`pfcpgen/pkg/runtime/types.go`):

- `FTEID` (§8.2.3), `FSEID` (§8.2.37), `NodeID` (§8.2.38)
- `UEIPAddress` (§8.2.62), `OuterHeaderCreation` (§8.2.56)
- `MBR` (§8.2.8), `GBR` (§8.2.9 — `type GBR = MBR`)
- `APNDNN` (§8.2.117), `SDFFilter` and `UserID` are `flag_conditional`

## 4. Per-session rule model

### 4.1 PDR — Packet Detection Rule (TS 29.244 §7.5.2.2)

Matches incoming packets and binds them to actions.

| Field | Spec § | Use |
|-------|--------|-----|
| `PDRID` | §8.2.43 | Rule key |
| `Precedence` | §8.2.11 | Lower = higher priority |
| `PDI.SourceInterface` | §8.2.10 | 0=Access (UL), 1=Core (DL), 2=SGi-LAN, 3=CP |
| `PDI.FTEID` | §8.2.3 | UL match: UPF GTP-U TEID + UPF N3 IPv4 |
| `PDI.UEIPAddress` | §8.2.62 | DL match: UE's IPv4 (S/D=1 = destination) |
| `PDI.SDFFilter` | §8.2.5 | Cisco-style flow desc (for flow-level rules) |
| `PDI.QFI` | §8.2.62A | QoS Flow Identifier |
| `FARID` | §8.2.42 | Action to apply on match |
| `QERID` | §8.2.27 | QoS rule (rate / gate) |
| `URRID` | §8.2.30 | Usage measurement |

Default model per UE PDU session (1 default QoS flow):

- **PDR-1 UL**: src=Access, F-TEID=UPF UL TEID, → FAR-1 UL, QER-1, URR-1
- **PDR-2 DL**: src=Core, UE-IP=ue.IPv4 (S/D=1), → FAR-2 DL, QER-1, URR-1

### 4.2 FAR — Forwarding Action Rule (§7.5.2.3)

| Apply Action | Wire bit (§8.2.26) | Meaning |
|--------------|---------------------|---------|
| FORW | 0x01 | Forward (UL: to N6; DL: to N3 via Outer Header) |
| BUFF | 0x02 | Buffer (DL when UE in CM-IDLE) |
| DROP | 0x04 | Discard |
| NOCP | 0x08 | Don't notify CP |
| DUPL | 0x10 | Duplicate (LI) |

DL FAR ships with `BUFF` initially (line `establish.go:973-974`); flips
to `FORW` + `OuterHeaderCreation` on the Modification fired by
`ActivateUserPlane` after the gNB ICS Response arrives.

### 4.3 QER — QoS Enforcement Rule (§7.5.2.5)

| Field | Use |
|-------|-----|
| `QERID` | rule key (id=0xFFFFFFFE reserved for Session-AMBR) |
| `QFI` | bind to a QoS Flow (0 = session-scope) |
| `GateStatus` | UL/DL gate (open/closed) |
| `MBR` | Maximum Bit Rate (kbps, 40-bit, §8.2.8) |
| `GBR` | Guaranteed Bit Rate (kbps, 40-bit, §8.2.9) |

**Session-AMBR** rides as a separate QER (§5.7.2.6): one extra
`CreateQER` with `QERID=0xFFFFFFFE`, `QFI=0`, MBR=session AMBR.
Applied across all PDRs of the session.

**UE-AMBR** does NOT appear in PFCP — enforced at gNB per
TS 23.501 §5.7.2.6. AMF sends it via NGAP `UEAggregateMaximumBitRate`
IE (TS 38.413 §9.3.1.58) in PDU Session Resource Setup Request.

### 4.4 URR — Usage Reporting Rule (§7.5.2.4)

| Field | Use |
|-------|-----|
| `URRID` | rule key |
| `MeasurementMethod` | DURAT / VOLUM / EVENT bits (§8.2.21) |
| `ReportingTriggers` | PERIO / VOLTH / TIMTH / etc. (§8.2.22) |
| `VolumeThreshold` | UL+DL byte thresholds (§8.2.13) |
| `TimeThreshold` | seconds (§8.2.14) |

Today: VOLUM measurement, periodic trigger. Volume reports flow
back via §7.5.8 Session Report Request when thresholds trip.

## 5. Lifecycle (one PDU session, normal path)

| # | Event | Spec | Producer | Effect on UPF |
|---|-------|------|----------|---------------|
| 1 | UE registers, NAS PDU Session Establishment Request | TS 24.501 §6.4.1 | UE→AMF→SMF | — |
| 2 | SMF queues rules + PFCP Session Establishment | TS 23.502 §4.3.2 / TS 29.244 §7.5.2 | SMF | UP-SEID allocated, PDR/FAR (DL=BUFF)/QER/URR installed |
| 3 | AMF sends NGAP PDU Session Resource Setup Request to gNB | TS 38.413 §9.2.1.1 | AMF → gNB | gNB allocates DL F-TEID |
| 4 | gNB returns PDU Session Resource Setup Response | TS 38.413 §9.2.1.2 | gNB → AMF → SMF | DL F-TEID arrives at SMF |
| 5 | SMF fires PFCP Session Modification (UpdateFAR FORW + OHC) | TS 29.244 §7.5.4 | SMF | DL FAR flips BUFF→FORW, gNB tunnel installed; buffered DL packets drain |
| 6 | UE sends/receives data | — | UE↔gNB↔UPF↔DN | C dataplane: lookup UE-IP/TEID hash → PDR match → QER meter → FAR action |
| 7 | gNB sends UE Context Release (cause=21) | TS 38.413 §8.3.2 | gNB → AMF → SMF | SMF fires PFCP Modification UpdateFAR=BUFF; URR delivers final volume report |
| 8 | UE-initiated PDU Session Release | TS 24.501 §6.3.3 | UE → AMF → SMF | SMF fires PFCP Session Deletion; per-session state torn down |

Reactivation (Service Request): AMF triggers ICS Setup, gNB allocates
new DL F-TEID, SMF re-fires the §7.5.4 UpdateFAR FORW (#5 again).

## 6. Data-plane fast path

### 6.1 Hash tables (per `upf_session_table.h`)

| Hash | Key | Value | Populated by |
|------|-----|-------|--------------|
| `teid_hash` | UL TEID (uint32) | (IMSI, pduSessID, session_t*) | `upf_dp_register_teid` from PDR install |
| `ueip_hash` | UE IPv4 (uint32) | (IMSI, pduSessID, session_t*) | `upf_dp_register_ueip` from PDR install |

Both are populated by `applyCreatePDRToHook` in
`nf/upf/pfcp/handler.go` after extracting `PDI.FTEID` / `PDI.UEIPAddress`
from the §7.5.2 Establishment.

### 6.2 Packet flow

**Uplink (gNB → UPF → N6):**
```
UDP recv on N3 socket (gtpu_fd)
  ↓
GTP-U decap → (TEID, inner IP, payload)
  ↓
teid_hash[TEID]  → session_t, IMSI, pduSessID
  ↓
classifier → pick UL PDR (src=Access)
  ↓
PDR matches via SDF Filter (if any)
  ↓
QER meter (Session-AMBR + per-flow MBR)
  ↓
FAR Apply Action = FORW
  ↓
write inner IP packet to TUN device (N6)
  ↓
URR.volBytes += packet size
```

**Downlink (N6 → UPF → gNB):**
```
read packet on TUN device (tun_fd)
  ↓
ueip_hash[dst_ip]  → session_t, IMSI, pduSessID
  ↓
classifier → pick DL PDR (src=Core)
  ↓
QER meter
  ↓
FAR Apply Action:
  - BUFF? → enqueue in upf_buffer (TS 29.244 §7.5.8 trigger DLDR)
  - FORW? → GTP-U encap with Outer Header (TEID + gNB IP) → send N3
```

The C dataplane runs a dedicated pthread launched by `PktIORun` from
`cgo_bridge_linux.go:307`. It blocks in `select(gtpu_fd, tun_fd)`.

## 7. Codec source of truth

PFCP wire format is generated:

```
codecs/tlv-3gpp-pfcp/pfcpgen/
├── cmd/pfcpgen/main.go           (`go run` entry point)
├── definitions/
│   ├── pfcp_messages.yaml        23 messages
│   └── pfcp_ie_types.yaml        252 IE types
├── pkg/
│   ├── schema/                   YAML structs
│   ├── codegen/                  jen-based emitters
│   │   ├── ie.go                 byte_container / structured / bitfield / grouped
│   │   ├── flagcond.go           kind: flag_conditional emitter
│   │   ├── message.go            message struct emitter
│   │   └── dispatcher.go         decode-by-type-code dispatcher
│   └── runtime/
│       └── types.go              FTEID/FSEID/NodeID/UEIPAddress/
│                                 OuterHeaderCreation/MBR/GBR/APNDNN
│                                 + EncodeTBCD/DecodeTBCD primitives
└── generated/                    output (DO NOT EDIT)
```

To regenerate:
```bash
cd codecs/tlv-3gpp-pfcp/pfcpgen && \
  go run ./cmd/pfcpgen -d ./definitions -o ./generated
```

Strict-spec rule enforced by `nf/tools/speccheck/`: every `TS X.Y §a.b.c`
citation in code must resolve to a section header at line-start in the
local PDF. CI runs this; new citations either ground or the build
fails.

## 8. Bridge interface (`nf/upf/cgo_bridge.go`)

The Go API the SMF/Manager use to drive any UPF backend (cgo, PFCP,
or test stub):

```go
type UPFBridge interface {
    // Session lifecycle
    SessionCreate(imsi, pduSessID, dnn, sst, sd, ueAddr, pdnType)
    CommitSession(imsi, pduSessID)               // PFCP-only flush
    SessionDelete(imsi, pduSessID)

    // Rules — append into pendingRules (PFCP) or install (cgo)
    AddPDR(... ueIPv4, teid, n3IPv4)
    AddFAR(... action, dstIface, teid, peerAddr, peerPort, ohcType)
    AddQER(... qfi, gateUL, gateDL, mbrUL, mbrDL, gbrUL, gbrDL)
    AddURR(... measMethod, reportTrigger, volTh, timeTh)

    // Post-establishment changes
    UpdateFAR(... farID, teid, peerAddr, peerPort)
    DeactivateDLFAR(... farID)
    SetSessionAMBR(... ambrUL, ambrDL)
    SetUEAMBR(...)                               // no-op on PFCP (RAN-side)

    // Dataplane init (cgo only; PFCP no-op)
    PktIOInit(n3Addr, n3Port, tunName, tunAddr)
    PktIORun() / PktIOStop()
    RegisterTEID / RegisterUEIP

    // §7.5.8 reports
    DrainReports(buf []Report) int
    ReportsDropped() uint64

    // Telemetry
    GetURRStats / GetQERStats / GetIOStats / SessionCount

    // Slicing
    SliceInit / SliceDestroy / SliceSessionCreate
}
```

Two impls today:
1. `dpdkBridge` (`cgo_bridge_linux.go`) — wraps libupf_dp.so; in-process
2. `PfcpBridge` (`nf/smf/upfclient/pfcp_bridge.go`) — sends PFCP over UDP

Selection: `upfloop.Enable()` swaps `upf.Bridge()` from cgo to PFCP at
startup if SMF/UPF are separated.

## 9. What's not implemented / out of scope

| Feature | Status | Notes |
|---------|--------|-------|
| IPv6 PDU sessions | Stub | Hash and PDI plumbing accept IPv6 but C dataplane is IPv4-only today |
| Ethernet PDU type | Not implemented | §5.7.6 — needs MAC-table support in C dataplane |
| ATSSS (multi-access) | Not implemented | §5.32 |
| TSC (time-sensitive comms) | Not implemented | §5.27 |
| Network Slicing per-slice queues | Stub via `upf_slice.h` | One slice today; multi-slice fairness TODO |
| Lawful Interception | Not implemented | §6.2.3 mentions; out of scope for dev build |
| N9 chaining | Stub | `SliceSessionCreate` exists; full §5.7.5 routing is TODO |
| UE-AMBR enforcement at UPF | Not in spec | Per §5.7.2.6 it's RAN-side; we have a deployment cap on cgo for in-process tests |

## 10. File map (quick reference)

```
nf/upf/
├── DESIGN.md                   this document
├── upf.go                      Manager, Session, PDR, FAR, QER, URR types
├── cgo_bridge.go               UPFBridge interface
├── cgo_bridge_linux.go         dpdkBridge — calls libupf_dp.so
├── net_setup.go                TUN device setup for N6
├── report.go                   Report struct + DrainReports
├── pfcp/
│   └── handler.go              §7.5.x message decoder, ManagerHook
├── upfloop/
│   ├── upfloop.go              UDP listener / Enable / association
│   ├── bridge_hook.go          ManagerHook ↔ UPFBridge adapter
│   └── integration_test.go     end-to-end loopback tests
└── dataplane/
    ├── include/                C headers (12 files)
    ├── src/                    C implementation (10 files)
    └── libupf_dp.so            built artefact

nf/smf/
├── session/establish.go        SMF-side: builds rules, calls Manager
└── upfclient/pfcp_bridge.go    PfcpBridge — implements UPFBridge over PFCP

codecs/tlv-3gpp-pfcp/pfcpgen/   YAML-driven PFCP codec
```

## 11. References

- **TS 23.501** — System Architecture for the 5G System (Stage-2)
  - §6.2.3 UPF, §5.7 QoS framework
- **TS 23.502** — Procedures for the 5G System (Stage-2)
  - §4.3.2 PDU Session Establishment, §4.3.4 PDU Session Modification
- **TS 23.503** — Policy and Charging Control framework (PCC)
- **TS 29.244** — PFCP (Stage-3, the spec)
  - §6 Functional procedures, §7 Messages, §8 IEs
- **TS 29.281** — GTP-U (N3, N9 wire)
- **TS 38.413** — NGAP (gNB ↔ AMF, carries UE-AMBR)
- **TS 38.415** — PDU Session UP information (in-band on N3)
- Internal: `oam/logger/redesign.go`, `nf/tools/speccheck/`

---
*Last refreshed against commit `6b19c16` (PDN Type + APN-DNN added to
§7.5.2 Establishment).*

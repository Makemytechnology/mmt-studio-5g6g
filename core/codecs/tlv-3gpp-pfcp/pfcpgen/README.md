<!-- Copyright (c) 2026 MakeMyTechnology. All rights reserved. -->

# pfcpgen — 3GPP PFCP (TS 29.244) codec generator (Go)

Sibling project to `../../tlv-3gpp-nas/nasgen/`. Generates type-safe Go
Encode/Decode code for **PFCP** (Packet Forwarding Control Protocol — the N4
/ Sx / Sxa / Sxb interface between SMF and UPF) from YAML definitions that
mirror the IE and message tables in TS 29.244.

Built on `github.com/dave/jennifer/jen`.

## Why a separate project from NAS?

PFCP is TLV but differs from NAS in every way that matters:

| Aspect | NAS (`nasgen`) | PFCP (`pfcpgen`) |
|---|---|---|
| Type field | 1 byte (or 4 bits for half-octet) | **2 bytes** |
| Length field | 1 byte (or 2 for TLV-E) | **2 bytes** (always) |
| Enterprise-specific IEs | no | **yes** (MSB of Type = 1, 4-byte Enterprise ID injected between L and V) |
| Grouped IEs (recursive) | no | **yes** — IEs nest other IEs (Create PDR → PDI → F-TEID, etc.) |
| Mandatory-ordering for plain IEs | yes | no — every IE is tagged |
| Transport | over signalling | over **UDP/8805** |
| Message families | 5GMM/5GSM/EMM/ESM | Node-related / Session-related |

Sharing `runtime` between the two would create more friction than value.

## Layout

```
pfcpgen/
├── cmd/pfcpgen/              CLI
├── pkg/
│   ├── runtime/              byte-level PFCP primitives + common types
│   ├── schema/               YAML schema + loader
│   └── codegen/              jennifer-based Go source emitter
├── definitions/              YAML: IE types + messages
├── generated/                pfcpgen output
└── README.md
```

## Status

| Section | Count | Status |
|---|---:|---|
| Node-related messages (§7.4) | 15 | round-trip tested |
| Session-related messages (§7.5) | 8 | round-trip tested (grouped IE nesting verified) |
| IE types (primitive + grouped + decomposed) | 252 | covers the full v19.5.0 catalogue |
| Decomposed IEs with typed fields + named constants | 26+ | Cause, ApplyAction, ReportType, ReportingTriggers, MeasurementMethod, UsageReportTrigger, GateStatus, Source/Destination Interface, PDNType, MBR, GBR, Precedence, RecoveryTimeStamp (uint32), PDR/FAR/QER/URR/BAR/MAR ID, QFI, OffendingIE, TrafficEndpointID, MeasurementPeriod, DurationMeasurement |
| Byte-exact hex fixture tests | 7 | header layout, grouped IE nesting, bit-packing of ReportType / ApplyAction / GateStatus |
| pycrate interop fixtures | 23 | every PFCP message encoded by [pycrate](../../mmt_studio_core/libs/pycrate-master/) `TS29244_PFCP` round-trips through our dispatcher — 15 byte-identical, 8 cosmetic IE-ordering diffs, **0 decode failures, 0 wrong types** |
| **Total** | **23 messages / 252 IEs / 48 tests** | all `go test ./...` green |

### What the decomposition buys you

Before:
```go
msg.Cause = Cause{Value: []byte{0x01}}
msg.CreatePDR[0].Precedence = Precedence{Value: []byte{0x00, 0x00, 0x00, 0x64}}
if far.ApplyAction.Value[0] & 0x02 != 0 { /* forwarding */ }
```

After:
```go
msg.Cause = Cause{Value: CauseRequestAccepted}
msg.CreatePDR[0].Precedence = Precedence{Value: 100}
if far.ApplyAction.FORW == 1 { /* forwarding */ }
```

## Build & run

```bash
cd tlv-3gpp-pfcp/pfcpgen
go test ./...                                         # all green
go run ./cmd/pfcpgen -d ./definitions -o ./generated  # regenerate
```

## Runtime-typed helpers

Three IEs use hand-written runtime types because their wire formats have
nontrivial conditional layouts driven by flag bits:

- `runtime.FSEID` (IE 57) — flags + 64-bit SEID + optional IPv4 / IPv6
- `runtime.NodeID` (IE 60) — IPv4 / IPv6 / DNS-encoded FQDN
- `runtime.FTEID` (IE 21) — CHOOSE flag + optional TEID + conditional IPs

Everything else round-trips as a byte-container (`Value []byte`) or a grouped
struct with recursive TLV dispatch.

## Grouped IEs

PFCP's defining trick: IEs contain IEs. `CreatePDR` wraps `PDRID` +
`Precedence` + `PDI` (which itself wraps `SourceInterface` + optional `F-TEID`
+ multiple `SDFFilter` + ...). The generator treats this uniformly:

```go
type CreatePDR struct {
    PDRID       PacketDetectionRuleID
    Precedence  Precedence
    PDI         PDI                 // also a grouped struct
    FARID       *FARID              // conditional → pointer
    URRID       []URRID             // multiple: true → slice
    ...
}

// Decode recurses via runtime.Buffer.ForEachIE with a type-code switch:
// every level uses the same TLV loop.
```

## Reference specs (in `specs/3gpp/` at repo root)

- **TS 29.244 v19.5.0** — PFCP protocol and IE catalogue
  (`specs/3gpp/ts_129244v190500p.pdf`)

## Extending

1. Add new IE rows or grouped members to `definitions/pfcp_ie_types.yaml`
2. Add messages to `definitions/pfcp_messages.yaml`
3. Re-run `go run ./cmd/pfcpgen`
4. Every IE type → one file (`ie_<name>.go`), every message → one file
   (`msg_<name>.go`), plus `dispatcher.go`

## Safety

Every generated `Decode`:
- parses IEs through `runtime.Buffer.ForEachIE` — bounds-checked, panic-free
- skips unknown IE type codes (forward compat)
- returns `runtime.DecodeError` with `{msg, ie, type_code, offset, underlying}`
- validates `MinLength` on byte-container IEs before decoding

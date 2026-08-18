# tlv-3gpp-pfcp — Design Document

PFCP (TS 29.244 §7) TLV codec for the MMT 5G Core. End-to-end
generated from YAML: schema → codegen → emitted Go → runtime
hand-coded primitives.

This is the wire codec used by `nf/upf/pfcp/handler.go` (decode
side) and `nf/smf/upfclient/pfcp_bridge.go` (encode side); see the
companion design doc `nf/upf/DESIGN.md §3.3 / §7` for how the
generated types are consumed.

## 1. Role / Scope

PFCP is the protocol over the **N4** reference point between SMF
(CP function) and UPF (UP function), per **TS 29.244 §7**. This
codec emits one Go struct per message, one Go struct (or runtime
alias) per IE, and a single decode-by-type-code dispatcher. There
are roughly **20k LOC** of generated Go in `pfcpgen/generated/`
backed by **~360 LOC** of YAML and **~700 LOC** of hand-written
codegen / runtime.

The codec covers (**23 messages**, **108 IE types**):

| Group | Spec § | Messages emitted |
|-------|--------|------------------|
| Node | TS 29.244 §7.4.2 / §7.4.3 | Heartbeat Req/Resp |
| Node | TS 29.244 §7.4.4 / §7.4.5 | PFD Management Req/Resp |
| Node | TS 29.244 §7.4.4.1–§7.4.4.7 | Association Setup / Update / Release Req/Resp + Version-Not-Supported |
| Node | TS 29.244 §7.4.5.1 / §7.4.5.2 | Node Report Req/Resp |
| Node | TS 29.244 §7.4.6.1 / §7.4.6.2 | Session Set Deletion Req/Resp |
| Session | TS 29.244 §7.5.2 / §7.5.3 | Session Establishment Req/Resp |
| Session | TS 29.244 §7.5.4 / §7.5.5 | Session Modification Req/Resp |
| Session | TS 29.244 §7.5.6 / §7.5.7 | Session Deletion Req/Resp |
| Session | TS 29.244 §7.5.8 / §7.5.9 | Session Report Req/Resp |

(Per-message section anchors are verbatim in
`pfcpgen/definitions/pfcp_messages.yaml` description fields.)

## 2. Architecture

```
              codecs/tlv-3gpp-pfcp/pfcpgen/
              ─────────────────────────────
                            ▲
                            │ go run ./cmd/pfcpgen -d ./definitions -o ./generated
                            │
       ┌────────────────────┴───────────────────────────────┐
       │                                                    │
       ▼                                                    ▼
  definitions/                                         generated/
   ├── pfcp_messages.yaml      ──────────────▶          msg_*.go (23)
   │   (23 messages)                                    ie_*.go  (~200)
   ├── pfcp_ie_types.yaml      ──────────────▶          dispatcher.go
   │   (108 IE types)                                   roundtrip_test.go
   │                                                    pycrate_interop_test.go
   │                                                    flagcond_test.go
   │
   │
       ┌────────────────────┴────────┐
       │                             │
       ▼                             ▼
  pkg/schema/                 pkg/codegen/                 pkg/runtime/
   loader.go (72)              dispatcher.go (38)           decoder.go
   schema.go  (83)             flagcond.go   (341)          encoder.go
   YAML structs                generator.go  (80)           header.go    (TS §7.2.2)
                               ie.go         (448)          types.go     (675)
                               message.go    (96)           errors.go
                               naming.go     (49)           runtime_test.go

                                                           Hand-coded runtime types:
                                                            FSEID  (TS 29.244 §8.2.37)
                                                            NodeID (TS 29.244 §8.2.38)
                                                            FTEID  (TS 29.244 §8.2.3)
                                                            UEIPAddress (§8.2.62)
                                                            OuterHeaderCreation (§8.2.56)
                                                            MBR  (§8.2.8) / GBR = MBR (§8.2.9)
                                                            APNDNN (§8.2.117)
                                                            EncodeTBCD/DecodeTBCD (TS 29.274 §8.3)
```

The generator is a thin shell over `schema.Load` + `codegen.NewGenerator`;
the entry point in `cmd/pfcpgen/main.go:14-48` reads `-d` and `-o`
flags via cobra.

## 3. File / Package Map

| Path | LOC | Role |
|------|-----|------|
| `pfcpgen/cmd/pfcpgen/main.go` | 49 | CLI: `pfcpgen -d ./definitions -o ./generated` |
| `pfcpgen/definitions/pfcp_messages.yaml` | ~290 | 23 message tables (TS 29.244 §7) |
| `pfcpgen/definitions/pfcp_ie_types.yaml` | ~700 | 108 IE type entries with `type_code`, `min_length`, `max_length` |
| `pfcpgen/pkg/schema/schema.go` | 83 | YAML structs |
| `pfcpgen/pkg/schema/loader.go` | 72 | Glob + unmarshal |
| `pfcpgen/pkg/codegen/generator.go` | 80 | Top-level orchestrator |
| `pfcpgen/pkg/codegen/ie.go` | 448 | IE struct emitter (byte_container / structured / bitfield / grouped) |
| `pfcpgen/pkg/codegen/flagcond.go` | 341 | `kind: flag_conditional` emitter (SDF Filter etc.) |
| `pfcpgen/pkg/codegen/message.go` | 96 | Message struct emitter |
| `pfcpgen/pkg/codegen/dispatcher.go` | 38 | `DecodePFCPMessage(data []byte)` switch |
| `pfcpgen/pkg/codegen/naming.go` | 49 | YAML name → Go ident |
| `pfcpgen/pkg/runtime/header.go` | 103 | PFCP message header (TS 29.244 §7.2.2) |
| `pfcpgen/pkg/runtime/decoder.go` | — | TLV reader (`DecodeIE` per TS 29.244 §8.1.1) |
| `pfcpgen/pkg/runtime/encoder.go` | — | TLV writer |
| `pfcpgen/pkg/runtime/types.go` | 675 | `FSEID` / `NodeID` / `FTEID` / `UEIPAddress` / `OuterHeaderCreation` / `MBR` / `GBR` / `APNDNN` + TBCD primitives |
| `pfcpgen/pkg/runtime/errors.go` | 45 | Sentinel errors |
| `pfcpgen/pkg/runtime/runtime_test.go` | 197 | Unit tests for runtime types |
| `pfcpgen/generated/` | ~16k LOC | Output (**DO NOT EDIT**) |

## 4. Codegen Pipeline

```
pfcp_messages.yaml         each message lists its IEs (presence + type_ref)
       │
       ▼
pfcp_ie_types.yaml         each IE: type_code, min/max length, fields
       │                   plus `kind: flag_conditional` and `go_type:` overrides
       ▼
schema.Load(defDir)        deserialise YAML → MessageDef + IETypeDef
       │
       ▼
codegen.NewGenerator       pkg/codegen orchestrator
       │   ├─ ie.go         emits one ie_*.go per IETypeDef
       │   │                   • byte_container (default — `Value []byte`)
       │   │                   • structured (declared `bytes:` fields)
       │   │                   • bitfield (declared `bits:`/`offset:` fields)
       │   │                   • grouped (`grouped: true` + `members:`)
       │   │                   • runtime alias (`go_type:` reuse)
       │   ├─ flagcond.go    emits ie_*.go for `kind: flag_conditional`
       │   │                   (SDF Filter §8.2.5, Volume Measurement §8.2.13,
       │   │                    Volume Threshold §8.2.13, FlowDescription, etc.)
       │   ├─ message.go     emits one msg_*.go per MessageDef
       │   └─ dispatcher.go  emits dispatcher.go (decode-by-MessageType switch)
       │
       ▼
generated/                 output (DO NOT EDIT)
       ie_*.go                ~200 IE types
       msg_*.go               23 messages
       dispatcher.go          DecodePFCPMessage(data []byte) (interface{}, error)
       roundtrip_test.go
       pycrate_interop_test.go
       flagcond_test.go
       hex_fixture_test.go
```

### YAML primitive types

| YAML `type:` | Go shape | Wire |
|--------------|----------|------|
| `tbcd_digits` | `string` (digits) | TBCD-packed (TS 29.274 §8.3) |
| `utf8` | `string` | raw bytes, optional `length_prefix: u8` / `u16` |
| `uint16` / `uint24` / `uint32` / `uint64` | `*uintN` (nil = absent) | big-endian |
| `smmii_list` | `[][]byte` | count-prefixed list of u16-LV bodies |

### `go_type:` runtime aliases (irregular IEs hand-coded)

These IEs delegate `Encode()` / `Decode()` to `pkg/runtime/types.go`
because the wire format is too irregular for the YAML-driven path:

| IE (YAML name) | Type code | Go alias | Spec § |
|----------------|-----------|----------|--------|
| `FSEID` | 57 | `FSEID` | §8.2.37 |
| `NodeID` | 60 | `NodeID` | §8.2.38 |
| `FTEID` | 21 | `FTEID` | §8.2.3 |
| `UEIPAddress` | — | `UEIPAddress` | §8.2.62 |
| `OuterHeaderCreation` | — | `OuterHeaderCreation` | §8.2.56 |
| `MBR` | — | `MBR` | §8.2.8 |
| `GBR` | — | `GBR = MBR` | §8.2.9 |
| `APNDNN` | — | `APNDNN` | §8.2.117 |

(Type codes from TS 29.244 Table 8.1.2-1.)

### `kind: flag_conditional` IEs

Some IEs have flag bits in octet-1 followed by **conditionally
present** typed fields. Examples in `pfcpgen/definitions/pfcp_ie_types.yaml`:

- **SDFFilter** (§8.2.5) — flags FD/TTC/SPI/FL/SDFFID/SMMII gate
  presence of `FlowDescription`, `ToSTrafficClass`, `SecurityParameterIndex`,
  `FlowLabel`, `SDFFilterID`, `SMMII` respectively.
- **VolumeMeasurement** (§8.2.13) — flags TOVOL/ULVOL/DLVOL gate
  `TotalVolume`, `UplinkVolume`, `DownlinkVolume`.
- **VolumeThreshold** (§8.2.13) — same shape, threshold variant.

`flagcond.go` (341 LOC) emits the bit-tracking + conditional
encode/decode loops for these.

## 5. Generation Flow

```bash
cd codecs/tlv-3gpp-pfcp/pfcpgen && \
  go run ./cmd/pfcpgen -d ./definitions -o ./generated
```

CLI flags (cobra):

| Flag | Default | Purpose |
|------|---------|---------|
| `-d`, `--definitions` | `./definitions` | YAML directory |
| `-o`, `--output` | `./generated` | Output directory |
| `-p`, `--package` | `pfcp` | Go package name |
| `--runtime` | (none) | Override runtime import path |

On success the tool prints `generated <N> messages, <M> IE types →
<dir>` to stderr (`main.go:36-37`).

## 6. Key Runtime Types (hand-coded)

```go
// PFCP message header per TS 29.244 §7.2.2.
type Header struct {
    MessageType    uint8
    Length         uint16
    SEID           *uint64       // present iff S=1
    SequenceNumber uint32        // 24-bit on the wire
    Priority       *uint8        // present iff MP=1
}

// FSEID — F-SEID (TS 29.244 §8.2.37) — SEID + UP IP address(es).
type FSEID struct { /* SEID, IPv4, IPv6 */ }

// NodeID — TS 29.244 §8.2.38 — IPv4 / IPv6 / FQDN.
type NodeID struct { /* Type, IPv4, IPv6, FQDN */ }

// FTEID — TS 29.244 §8.2.3 — TEID + UP IP address(es) + ChooseID.
type FTEID struct { /* flags + TEID + IPs */ }

// UEIPAddress — TS 29.244 §8.2.62.
type UEIPAddress struct { /* flags + IPv4 / IPv6 + S/D bit + Prefix */ }

// OuterHeaderCreation — TS 29.244 §8.2.56 — GTP-U / UDP / IPv4-IPv6.
type OuterHeaderCreation struct { /* descriptor + TEID + peer IP+port */ }

// MBR — TS 29.244 §8.2.8 — UL+DL kbps, 40-bit each.
type MBR struct { ULKbps, DLKbps uint64 }
type GBR = MBR        // §8.2.9 — identical layout

// APNDNN — TS 29.244 §8.2.117 — labelled string.
type APNDNN struct { Value string }

// TBCD — TS 29.274 §8.3 — used by flag_conditional IEs.
func EncodeTBCD(digits string) ([]byte, error)
func DecodeTBCD(b []byte) string
```

## 7. Stubs / TODOs

A grep for `TODO|FIXME` across `pfcpgen/pkg/` and `pfcpgen/cmd/`
returns no hits at the time of this snapshot — the codec is
complete for its declared scope. Coverage gaps are at the YAML
level (e.g. some primitive IEs are still byte-containers; per-IE
field decomposition is "a non-breaking YAML refinement" per the
header comment in `pfcp_ie_types.yaml`).

## 8. Speccheck Integration

PFCP `TS 29.244 §...` and `TS 29.274 §8.3` citations in
`pfcpgen/pkg/runtime/*.go`, `pfcpgen/pkg/codegen/flagcond.go`, and
the YAML descriptions are validated by `nf/tools/speccheck/`:

```bash
go test ./nf/tools/speccheck/...
```

Strict by default — any new `TS X.Y §a.b.c` citation must resolve
to a section header at line-start in the local PDF; CI fails if
not. `SPECCHECK_LOOSE=1` temporarily tolerates MISSING/UNLOADED.

## 9. References

Spec citations grounded in `pfcpgen/` source:

- **TS 29.244 §7** — messages (per-message clauses listed in the
  YAML descriptions: §7.4.2/§7.4.3/§7.4.4(.1–.7)/§7.4.5(.1/.2)/
  §7.4.6(.1/.2)/§7.5.2/§7.5.3/§7.5.4/§7.5.5/§7.5.6/§7.5.7/§7.5.8/§7.5.9).
- **TS 29.244 §7.2.2** — Header (`runtime/header.go:7`).
- **TS 29.244 §8.1.1** — IE format (`runtime/decoder.go:82`).
- **TS 29.244 §8.2.x** — IE types: §8.2.3 FTEID, §8.2.5 SDF Filter,
  §8.2.8 MBR, §8.2.9 GBR, §8.2.13 Volume Measurement / Threshold,
  §8.2.37 FSEID, §8.2.38 NodeID, §8.2.56 OuterHeaderCreation,
  §8.2.62 UEIPAddress, §8.2.117 APNDNN.
- **TS 29.274 §8.3** — TBCD digit encoding (`runtime/types.go:11`).
- **TS 24.501 §9.11.2.1B** — DNN/APN encoding referenced from
  `runtime/types.go:618`.

External references:

- `nf/upf/DESIGN.md §3.3, §7, §10` — describes how the generated
  types are consumed by UPF and SMF.

---
*Last refreshed against commit `13a181d`.*

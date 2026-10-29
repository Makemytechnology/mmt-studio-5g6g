# tlv-3gpp-nas — Design Document

3GPP NAS TLV codec for the MMT 5G Core. End-to-end generated from
YAML: schema → codegen → emitted Go → runtime hand-coded primitives.
Mirrors the design shape of `tlv-3gpp-pfcp` but covers the NAS
layer of TS 24.501 (5GMM / 5GSM) and TS 24.301 (EMM / ESM).

## 1. Role / Scope

NAS messages are carried over RRC (NR / E-UTRA) between UE and
MME / AMF. The wire format is TLV-with-half-octet-IEIs as defined
by **TS 24.007 §11.2**, with per-message tables in
**TS 24.501 §8** (5GS) and **TS 24.301 §8** (EPS). This codec emits
one Go struct per message, one Go struct (or runtime alias) per IE,
and a single decode-by-(EPD,MessageType) dispatcher. There are
roughly **20k LOC** of generated Go in `nasgen/generated/` backed
by **~1.4k LOC** of YAML and **~1k LOC** of hand-written runtime.

Coverage (counts from `^- name:` matches in `definitions/`):

| Domain | EPD | YAML messages | YAML IE types |
|--------|-----|---------------|---------------|
| 5GMM (TS 24.501 §8) | `0x7E` | 28 | shared with 5GSM |
| 5GSM (TS 24.501 §8) | `0x2E` | 16 | 95 (`5g_ie_types.yaml`) |
| EMM (TS 24.301 §8) | `0x07` | 32 | shared with ESM |
| ESM (TS 24.301 §8) | `0x02` | 23 | 80 (`lte_ie_types.yaml`) |

(Counts are message rows in
`definitions/{5gmm,5gsm,lte_emm,lte_esm}_messages.yaml` and IE-type
rows in `definitions/{5g,lte}_ie_types.yaml`.)

The generator can filter to a single domain with
`--protocol={5g|lte|emm|esm|all}` (`nasgen/cmd/nasgen/main.go:55,62-86`).

## 2. Architecture

```
              codecs/tlv-3gpp-nas/nasgen/
              ───────────────────────────
                            ▲
                            │ go run ./cmd/nasgen -d ./definitions -o ./generated -p nas
                            │
       ┌────────────────────┴───────────────────────────────┐
       │                                                    │
       ▼                                                    ▼
  definitions/                                         generated/
   ├── 5gmm_messages.yaml      ──────────────▶          msg_*.go  (~99)
   ├── 5gsm_messages.yaml      ──────────────▶          ie_*.go   (~165)
   ├── 5g_ie_types.yaml        ──────────────▶          dispatcher.go
   ├── lte_emm_messages.yaml   ──────────────▶          roundtrip_test.go
   ├── lte_esm_messages.yaml   ──────────────▶          lte_roundtrip_test.go
   └── lte_ie_types.yaml       ──────────────▶          full_roundtrip_test.go
                                                        pycrate_interop_test.go
       ┌────────────────────────────┐
       │                            │
       ▼                            ▼
  pkg/schema/                pkg/codegen/                pkg/runtime/
   loader.go (97)             dispatcher.go (153)         decoder.go    (TS 24.007 §11.2.4)
   schema.go  (76)            generator.go  (131)         encoder.go
   YAML structs               ie.go         (316)         security.go   (TS 24.501 §9.1.1 / §9.2 / §9.3)
                              message.go    (455)         types.go      (TS 24.501 §9.11.x)
                              naming.go     (42)          errors.go
                                                          runtime_test.go

                                                        Hand-coded runtime types:
                                                          PlmnId  (TS 24.501 §9.11.3.54)
                                                          SNSSAI  (TS 24.501 §9.11.2.8)
                                                          TAI     (TS 24.501 §9.11.3.8)
                                                          MobileIdentity5GS interface:
                                                            SUCI / GUTI5G / IMEI / STMSI5G / NoIdentity
                                                            (TS 24.501 §9.11.3.4)
                                                          DNN     (TS 24.501 §9.11.2.1B)
                                                          PSIBitmap (PDU Session Status)
                                                          NASSecurityHeader (TS 24.501 §9.1.1)
```

The generator entry point lives in `cmd/nasgen/main.go:18-60`;
filtering to a single domain trims `repo.Messages` before codegen
runs (`main.go:36-39`).

## 3. File / Package Map

| Path | LOC | Role |
|------|-----|------|
| `nasgen/cmd/nasgen/main.go` | 87 | CLI: `nasgen -d ... -o ... -p nas [--protocol=5g|lte|emm|esm|all]` |
| `nasgen/definitions/5gmm_messages.yaml` | 28 messages | TS 24.501 clause 8 |
| `nasgen/definitions/5gsm_messages.yaml` | 16 messages | TS 24.501 clause 8 |
| `nasgen/definitions/5g_ie_types.yaml` | 95 IE types | TS 24.501 clause 9.11 |
| `nasgen/definitions/lte_emm_messages.yaml` | 32 messages | TS 24.301 clause 8 |
| `nasgen/definitions/lte_esm_messages.yaml` | 23 messages | TS 24.301 clause 8 |
| `nasgen/definitions/lte_ie_types.yaml` | 80 IE types | TS 24.301 clause 9 |
| `nasgen/pkg/schema/schema.go` | 76 | YAML structs (`MessageDef`, `IETypeDef`, `FieldDef`) |
| `nasgen/pkg/schema/loader.go` | 97 | Glob + unmarshal of `definitions/` |
| `nasgen/pkg/codegen/generator.go` | 131 | Top-level orchestrator |
| `nasgen/pkg/codegen/ie.go` | 316 | IE struct emitter (bit-field / structured / byte-container / runtime alias) |
| `nasgen/pkg/codegen/message.go` | 455 | Message struct emitter (incl. EPD / TI / EBI prefix handling) |
| `nasgen/pkg/codegen/dispatcher.go` | 153 | `DecodeNASMessage(data []byte)` switch by EPD + msgType |
| `nasgen/pkg/codegen/naming.go` | 42 | YAML name → Go ident |
| `nasgen/pkg/runtime/security.go` | — | `SecurityHeaderType` / EPD / `NASSecurityHeader` (TS 24.501 §9.1.1 / §9.2 / §9.3) |
| `nasgen/pkg/runtime/decoder.go` | — | TLV reader (`DecodeTLV` / `DecodeTLVE` / `DecodeTV` / `DecodeT` per TS 24.007 §11.2.4) |
| `nasgen/pkg/runtime/encoder.go` | — | TLV writer (`EncodeTLV` / `EncodeTLVE` / `EncodeTV` / `EncodeTVHalfOctet` / `EncodeT` / `EncodeLV` / `EncodeLVE`) |
| `nasgen/pkg/runtime/types.go` | — | `PlmnId` / `SNSSAI` / `TAI` / `MobileIdentity5GS` impls / `DNN` / `PSIBitmap` |
| `nasgen/pkg/runtime/errors.go` | — | Sentinel errors |
| `nasgen/pkg/runtime/runtime_test.go` | — | Unit tests for runtime types |
| `nasgen/generated/` | ~20k LOC | Output (**DO NOT EDIT**) |
| `nasgen/testdata/pycrate_fixtures.json` | — | Reference encodings from pycrate |

## 4. Codegen Pipeline

```
{5gmm,5gsm,lte_emm,lte_esm}_messages.yaml      message tables w/ IE rows
       │                                       (presence M/O/C, format V/TV/TLV/T/LV-E/TLV-E)
       ▼
{5g,lte}_ie_types.yaml                         IE type definitions
       │                                       (min/max length, fields, go_type override)
       │
       ▼
schema.Load(defDir)                            deserialise YAML → repo:
       │                                          repo.Messages   ([]MessageDef)
       │                                          repo.IETypes    ([]IETypeDef)
       │
       ▼
codegen.NewGenerator(repo, outDir, pkgName)
       │   ├─ ie.go         emits one ie_*.go per IETypeDef:
       │   │                   • bit-field IE — declares `bits:` + `offset:`
       │   │                       Generator emits Decode(byte) / Encode() byte
       │   │                       AND DecodeBytes / EncodeBytes for TLV use.
       │   │                   • structured IE — declares `bytes:` per field.
       │   │                   • byte-container — no `fields`. Round-trips
       │   │                       via `Value []byte`; min/max_length still
       │   │                       enforced on decode.
       │   │                   • runtime alias — `go_type:` reuses a
       │   │                       hand-written pkg/runtime type.
       │   ├─ message.go    emits one msg_*.go per MessageDef. Handles:
       │   │                   • Mandatory IEs at fixed offsets per TS 24.501 §8 / 24.301 §8
       │   │                   • Optional IEs by IEI (TS 24.007 §11.2)
       │   │                   • LTE ESM transaction ID + EPS Bearer
       │   │                       Identity prefix in octet 0
       │   │                       (TS 24.301 §9.2 / §9.3 — message.go:34)
       │   │                   • 5GS PD/SHT/MsgType prefix (TS 24.501 §9.4)
       │   ├─ dispatcher.go emits dispatcher.go: switch by EPD then by
       │   │                   message-type byte; security-protected
       │   │                   wrappers go through `runtime.ParseSecurityHeader`.
       │   └─ naming.go     YAML name → Go ident
       │
       ▼
generated/                 output (DO NOT EDIT)
       ie_*.go                ~165 IE types
       msg_*.go               ~99 messages
       dispatcher.go          DecodeNASMessage(data []byte) (interface{}, error)
       roundtrip_test.go      hand-fixtured round-trips
       lte_roundtrip_test.go
       full_roundtrip_test.go
       pycrate_interop_test.go  byte-equality vs pycrate encoder
```

### YAML format taxonomy (`MessageDef.IEs[i].Format`)

Per **TS 24.007 §11.2.4** — drives the encoder/decoder method picked
by `message.go`:

| Format | Header | Body | Encoder method |
|--------|--------|------|----------------|
| `V` | none | fixed-length | `WriteBytes` |
| `TV` | 1-byte IEI | fixed-length | `EncodeTV(iei, value)` |
| `TV` (half-octet) | 4-bit IEI in high nibble | 4-bit value in low nibble | `EncodeTVHalfOctet(iei, value)` (IEI ends in `-` in YAML) |
| `LV` | 1-byte length | variable | `EncodeLV(value)` |
| `TLV` | 1-byte IEI + 1-byte length | variable | `EncodeTLV(iei, value)` |
| `LV-E` | 2-byte length | variable | `EncodeLVE(value)` |
| `TLV-E` | 1-byte IEI + 2-byte length | variable | `EncodeTLVE(iei, value)` |
| `T` | 1-byte IEI | (none) | `EncodeT(iei)` |

`schema.IsHalfOctetIEI(iei)` (`schema.go:71`) flags half-octet IEIs
by the trailing `-` convention (e.g. `B-`, `9-`, `8-`).

## 5. Generation Flow

```bash
cd codecs/tlv-3gpp-nas/nasgen && \
  go run ./cmd/nasgen -d ./definitions -o ./generated -p nas
```

Optional protocol filter:

```bash
go run ./cmd/nasgen -d ./definitions -o ./generated -p nas --protocol=5g
go run ./cmd/nasgen -d ./definitions -o ./generated -p nas --protocol=emm
```

CLI flags (cobra — `cmd/nasgen/main.go:51-55`):

| Flag | Default | Purpose |
|------|---------|---------|
| `-d`, `--definitions` | `./definitions` | YAML directory |
| `-o`, `--output` | `./generated` | Output directory |
| `-p`, `--package` | `nas` | Go package name |
| `--runtime` | (none) | Override runtime import path |
| `--protocol` | `all` | `5g` / `lte` / `emm` / `esm` / `all` |

On success: `generated <N> messages, <M> IE types into <dir>`
(`main.go:46-47`).

## 6. Key Runtime Types (hand-coded)

```go
// PlmnId — PLMN identity (MCC + MNC), 3-byte BCD-swapped wire
// per TS 24.501 §9.11.3.54 + TS 24.008 §10.5.1.3.
type PlmnId struct { MCC, MNC string }
func (p *PlmnId) EncodeBCD() []byte
func DecodePlmnBCD(data []byte) (PlmnId, error)

// SNSSAI — Single Network Slice Selection Assistance Information
// (TS 24.501 §9.11.2.8).
type SNSSAI struct { SST uint8; SD, MappedHplmnSD *uint32; MappedHplmnSST *uint8 }

// TAI — 5GS Tracking Area Identity (TS 24.501 §9.11.3.8).
type TAI struct { /* PLMN + 3-byte TAC */ }

// MobileIdentity5GS — TS 24.501 §9.11.3.4 — interface implemented
// by SUCI / GUTI5G / IMEI / STMSI5G / NoIdentity.
type MobileIdentity5GS interface { Encode() []byte; Decode([]byte) error }

// DNN — TS 24.501 §9.11.2.1B — labelled APN string per
// TS 23.003 §9.1 (label-length prefixed labels, total ≤ 100 bytes).
type DNN struct { Value string }

// NASSecurityHeader — security-protected NAS wrapper
// (TS 24.501 §9.1.1) — EPD + Security Header Type (§9.3) + MAC + SQN.
type NASSecurityHeader struct { /* EPD, SHT, MAC[4], SQN, plaintext */ }
type SecurityHeaderType uint8                                          // §9.3
// Extended Protocol Discriminator constants (TS 24.501 §9.2):
//   EPD5GMM = 0x7E
//   EPD5GSM = 0x2E
// Legacy Protocol Discriminator constants (TS 24.007 §11.2.3.1.1):
//   PDEMM   = 0x07
//   PDESM   = 0x02
```

`MobileIdentity5GS` implementations:

| Type | TS 24.501 §9.11.3.4 | Notes |
|------|----------------------|-------|
| `SUCI` | type 001 | Encode is a stub returning raw bytes (`types.go:101` — TODO TS 24.501 §9.11.3.4 figure 9.11.3.4.4) |
| `GUTI5G` | type 010 / figure 9.11.3.4.3 | Full encode/decode |
| `IMEI` | (TS 24.008 §10.5.1.4 low-nibble = type 011) | TBCD digits + parity |
| `STMSI5G` | type 100 | Full encode/decode |
| `NoIdentity` | type 000 | Empty body |

The 5G NAS encoder (`pkg/runtime/encoder.go`) layers on top of a
`bytes.Buffer` with TLV / TLV-E / TV / LV-E / T helpers; the
decoder (`pkg/runtime/decoder.go`) is symmetric and follows the
TS 24.007 §11.2.4 high-nibble → format mapping (`decoder.go:160`).

## 7. Stubs / TODOs

| Site | Comment |
|------|---------|
| `pkg/runtime/types.go:101` | SUCI `Encode` is a stub returning raw bytes — TS 24.501 §9.11.3.4 figure 9.11.3.4.4 |
| `pkg/codegen/generator.go:226` (cross-codec analogue) | n/a in nasgen |

A grep for `TODO|FIXME` across `nasgen/pkg/` returns one item
(SUCI). Coverage gaps are at the YAML level — the YAML headers
note "where a message has many rarely-populated optional IEs, this
file captures the commonly used subset and trims the long tail; add
rows as needed" (`5gmm_messages.yaml:5-7`).

## 8. Speccheck Integration

NAS `TS 24.501 §...` / `TS 24.301 §...` / `TS 24.007 §...`
citations in `nasgen/pkg/runtime/*.go` and the YAML descriptions
are validated by `nf/tools/speccheck/`:

```bash
go test ./nf/tools/speccheck/...
```

Strict by default — every `TS X.Y §a.b.c` citation must resolve to
a section header at line-start in the local PDF. `SPECCHECK_LOOSE=1`
escapes for in-progress work.

## 9. References

Spec citations grounded in `nasgen/` source:

- **TS 24.007 §11.2.3.1.1** — Legacy Protocol Discriminator
  constants (`runtime/security.go:22`).
- **TS 24.007 §11.2.4** — TLV format (`runtime/decoder.go:87,160`).
- **TS 24.008 §10.5.1.3** — PLMN BCD-swap (`runtime/types.go:8`).
- **TS 24.008 §10.5.1.4** — Mobile Identity IE encoding low-nibble
  (`runtime/types.go:355`).
- **TS 24.501 §8** — 5GMM / 5GSM message tables (per-message
  citations live in YAML descriptions, e.g. "Registration request —
  TS 24.501 Table 8.2.6.1.1").
- **TS 24.501 §9.1.1** — NAS security header
  (`runtime/security.go:29`).
- **TS 24.501 §9.2** — Extended Protocol Discriminator
  (`runtime/security.go:16`).
- **TS 24.501 §9.3** — Security Header Type
  (`runtime/security.go:5`).
- **TS 24.501 §9.4** — message-type prefix layout
  (`codegen/message.go:29`).
- **TS 24.501 §9.11.2.1B** — DNN (`runtime/types.go:285`).
- **TS 24.501 §9.11.2.8** — SNSSAI (`runtime/types.go:71`).
- **TS 24.501 §9.11.3.4** — Mobile Identity 5GS
  (`runtime/types.go:86,101,126,191,361`).
- **TS 24.501 §9.11.3.8** — TAI (`runtime/types.go:79`).
- **TS 24.501 §9.11.3.54** — PLMN identity (`runtime/types.go:8`).
- **TS 24.301 §8** — EMM / ESM message tables (per-message
  citations in `lte_*_messages.yaml` descriptions).
- **TS 24.301 §9.2 / §9.3** — LTE ESM TI + EPS Bearer Identity
  prefix (`codegen/message.go:34`).
- **TS 23.003 §9.1** — APN label encoding (`runtime/types.go:285`).

External references:

- `nf/upf/DESIGN.md` — companion design doc (PFCP codec is the
  sister codec).

---
*Last refreshed against commit `13a181d`.*

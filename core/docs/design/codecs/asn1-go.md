# asn1-go — Design Document

ASN.1 → Go compiler for 3GPP PER/UPER encodings, hosted in
`codecs/asn1-go/`. Reads `.asn1` schemas (X.680) and emits Go
source with **APER** (Aligned PER) and **UPER** (Unaligned PER)
encode/decode methods (X.691). The compiler is used for the
S1AP, NGAP, NRPPa, F1AP and similar 3GPP control-plane PDUs that
ride on SCTP. See `codecs/asn1-go/README.md` for the full
"what works today" matrix.

This codec is large — roughly **80k LOC** including ~63k LOC of
generated Go in `protocols/{ngap,s1ap}/generated/` plus ~28k LOC
of source `.asn1` schemas. The compiler itself
(`pkg/{lexer,parser,ast,resolver,codegen,runtime}` +
`cmd/asn1go`) is **~5k LOC**.

## 1. Role / Scope

The compiler handles the X.680 / X.681 / X.682 / X.683 subset that
3GPP ASN.1 schemas use in practice, and emits APER/UPER
encode/decode methods per X.691. Its targets so far:

| Protocol | Spec | Source `.asn` | Generated package | Generated LOC |
|----------|------|---------------|-------------------|---------------|
| NGAP | TS 38.413 | `protocols/ngap/asn.1/*.asn` (6 files, ~33k LOC) | `protocols/ngap/generated/` | ~48k |
| S1AP | TS 36.413 | `protocols/s1ap/asn.1/*.asn` (6 files, ~7k LOC) | `protocols/s1ap/generated/` | ~27k |

(LOC counts from `wc -l`.)

The README also lists planned consumers (RRC TS 36.331 / TS 38.331,
NRPPa, F1AP, etc.); only NGAP and S1AP have populated `.asn`
schemas at the time of this snapshot.

## 2. Architecture

```
                        codecs/asn1-go/
                        ───────────────
                                ▲
                                │ go run ./cmd/asn1go [-p pkg] [-o out]
                                │     [-encoding={aper|uper|both}] *.asn
                                │
              ┌─────────────────┴─────────────────────────┐
              │                                           │
              ▼                                           ▼
 .asn schema input                               generated Go output
   protocols/ngap/asn.1/*.asn                    protocols/ngap/generated/*.go
   protocols/s1ap/asn.1/*.asn                    protocols/s1ap/generated/*.go
                                                 generated/testmodule.go (toy)

   Compiler pipeline (pkg/):

   .asn text
       │
       ▼
   lexer  ── tokens (X.680: bstring/hstring/cstring, hyphenated idents,
       │       2-char ops ::=  ..  ...  [[  ]] )
       ▼
   parser ── AST (modules, imports/exports, tag modes,
       │       SEQUENCE / CHOICE / SET OF / ENUMERATED
       │       (extensible) / TaggedType / constraints
       │       (range, SIZE, UNION |, INTERSECTION ^,
       │       extensibility (..., ...), table-constraint
       │       skeleton {ObjectSet}{@field}) /
       │       Information Object Class skeleton (X.681) /
       │       parameterised types (X.683) )
       ▼
   ast    ── node types (one Go type per ASN.1 construct)
       │
       ▼
   resolver ── cross-module symbol table; binds IMPORTS;
       │       resolves named values; binds object-set entries
       │       against WITH SYNTAX literal-token map
       ▼
   codegen ── Go code generation (jennifer) emitting:
       │       • ASN.1 → Go type mapping
       │           INTEGER     → int64
       │           ENUM        → int64 + named constants
       │           BIT STRING  → BitString
       │           OCTET STRING → []byte
       │           SEQUENCE    → struct
       │           CHOICE      → tagged union struct
       │           SEQUENCE OF → slice
       │       • struct tags  `aper:"sizeLB:X,sizeUB:Y,valueExt,..."`
       │       • specialised Marshal/Unmarshal for primitive named types
       │       • reflection-based Marshal/Unmarshal for SEQUENCE / CHOICE / SEQUENCE OF
       │       • <ObjectSet>Value typed CHOICE per object set with
       │         one alternative per entry + Present constants
       │       • <ObjectSet>Entry struct mirroring ProtocolIE-Field
       │       • parameterised-template expansion
       │         (ProtocolIE-Container{{NGSetupRequestIEs}} →
       │          []NGSetupRequestIEsEntry)
       │       • open-type wrapping of Value field around CHOICE alternative
       ▼
   runtime ── PerBitData (bit/byte rw, constrained-whole, length-determinant,
              normally-small non-negative, semi-constrained, unconstrained,
              BIT STRING, OCTET STRING, KM character strings, open-type) +
              MarshalAPER / MarshalUPER / UnmarshalAPER / UnmarshalUPER
              (single Aligned bool toggle)
```

## 3. File / Package Map

### Compiler proper

| Path | LOC | Role |
|------|-----|------|
| `cmd/asn1go/main.go` | 77 | CLI: `asn1go [-o ./generated] [-p pkg] [-m module] [--encoding=aper|uper|both] [--test] [-v] *.asn1` (cobra) |
| `pkg/lexer/lexer.go` | 357 | X.680 tokenizer |
| `pkg/lexer/token.go` | 265 | Token type + keyword table |
| `pkg/parser/parser.go` | 1200 | Recursive-descent parser (X.680 / X.681 / X.682 / X.683) |
| `pkg/parser/errors.go` | 27 | Parse-error formatting |
| `pkg/ast/ast.go` | 336 | AST node types |
| `pkg/resolver/resolver.go` | 108 | Cross-module symbol resolution |
| `pkg/resolver/objectset.go` | 83 | Object-set binding against WITH SYNTAX |
| `pkg/codegen/generator.go` | 593 | Top-level generator orchestrator |
| `pkg/codegen/coders.go` | 448 | Per-type encode/decode emitters |
| `pkg/codegen/objectset.go` | 256 | `<ObjectSet>Value` CHOICE + `<ObjectSet>Entry` emission |
| `pkg/codegen/templates.go` | 195 | Boilerplate fragments |
| `pkg/codegen/naming.go` | 50 | ASN.1 ident → Go ident |
| `pkg/runtime/perbitdata.go` | 455 | Bit-level reader/writer; constrained-whole / length-determinant / NSN / semi-constrained / unconstrained encoders |
| `pkg/runtime/marshal.go` | 720 | Reflection-based `MarshalAPER` / `UnmarshalAPER` / `MarshalUPER` / `UnmarshalUPER`; CHOICE / extension / open-type wrappers |
| `pkg/runtime/perstring.go` | 295 | KM character strings (PrintableString, IA5String, etc.) + open-type |
| `pkg/runtime/dataview.go` | 221 | Bit-buffer view abstraction |
| `pkg/runtime/decode_helpers.go` | 42 | `APERDecodable` / `UPERDecodable` interfaces |
| `pkg/runtime/types.go` | 21 | `BitString` / `OctetString` / `Enumerated` / `ObjectIdentifier` aliases |
| `pkg/runtime/pretty.go` | 153 | `DecodeAPERToJSON` debug printer |

### Schema inputs (`protocols/`)

```
protocols/
├── ngap/
│   ├── asn.1/                                 (~33k LOC source)
│   │   ├── NGAP-CommonDataTypes.asn           50    Criticality / Presence / IDs
│   │   ├── NGAP-Constants.asn                 2692  procedureCode + IE-ID consts
│   │   ├── NGAP-Containers.asn                375   PROTOCOL-IE-CONTAINER class +
│   │   │                                            ProtocolIE-Container template
│   │   ├── NGAP-IEs.asn                       17695 the IE catalogue
│   │   ├── NGAP-PDU-Contents.asn              9964  per-message IE lists +
│   │   │                                            <NGSetupRequestIEs> object sets
│   │   └── NGAP-PDU-Descriptions.asn          2390  Elementary Procedures, top-level
│   │                                                NGAP-PDU CHOICE
│   ├── generated/
│   │   ├── ngap_commondatatypes.go            187
│   │   ├── ngap_constants.go                  691
│   │   ├── ngap_containers.go                 4
│   │   ├── ngap_ies.go                        37895
│   │   ├── ngap_pdu_contents.go               9173
│   │   ├── ngap_pdu_descriptions.go           115
│   │   ├── ngap_smoke_test.go                 71      Criticality/Presence consts
│   │   └── pycrate_interop_test.go            143     byte-equality vs pycrate
│   └── testdata/                              fixtures
└── s1ap/
    ├── asn.1/                                 (~7k LOC source)
    │   ├── S1AP-CommonDataTypes.asn           29    Criticality / Presence / IDs
    │   ├── S1AP-Constants.asn                 499   procedureCode + IE-ID consts
    │   ├── S1AP-Containers.asn                210   ProtocolIE-Container template
    │   ├── S1AP-IEs.asn                       2685  IE catalogue
    │   ├── S1AP-PDU-Contents.asn              2763  per-message IE lists
    │   └── S1AP-PDU-Descriptions.asn          952   Elementary Procedures
    └── generated/
        ├── s1ap_commondatatypes.go            187
        ├── s1ap_constants.go                  462
        ├── s1ap_containers.go                 4
        ├── s1ap_ies.go                        17271
        ├── s1ap_pdu_contents.go               8346
        └── s1ap_pdu_descriptions.go           115
```

### Tooling

| Path | Role |
|------|------|
| `scripts/extract_ngap_asn1.py` | Pulls NGAP §9.4 ASN.1 out of `TS 38.413 v19.2.0` PDF |
| `scripts/extract_s1ap_asn1.py` | Pulls S1AP §9.3 ASN.1 out of TS 36.413 PDF |
| `scripts/pycrate_compare.py` | Reference encoder used by `pycrate_interop_test.go` for byte-equality checks |
| `testdata/` | Toy schema (`simple.asn1`) + fixtures |
| `generated/testmodule.go` | Output of running the compiler against the toy schema |

## 4. Public API (the compiler CLI)

Defined in `cmd/asn1go/main.go:24-40`:

```go
asn1go [flags] <input.asn1> [input2.asn1 ...]

  -o, --output    string   output directory                  (default "./generated")
  -p, --package   string   Go package name for generated code (default "asn1gen")
  -m, --module    string   full Go module path
      --encoding  string   "aper" | "uper" | "both"          (default "aper")
      --test      bool     generate test files with round-trip checks
  -v, --verbose   bool     verbose output
```

Pipeline executed by `run` (`main.go:42-77`):

1. Concatenate all input files into one source string.
2. `parser.New(srcAll).ParseModules()` → `[]ast.Module`.
3. Collect parse errors and bail if any.
4. `resolver.Build(modules)` → cross-module symbol table.
5. `codegen.New(reg, codegen.Options{...}).Generate()` writes to
   `OutDir`.

### Runtime API (the generated code consumes)

```go
// pkg/runtime/marshal.go:77-81
func MarshalAPER(v any) ([]byte, error)
func MarshalUPER(v any) ([]byte, error)
func UnmarshalAPER(b []byte, v any) error
func UnmarshalUPER(b []byte, v any) error

// Single Aligned bool toggles APER vs UPER alignment in marshalPER /
// unmarshalPER (marshal.go centralises the alignment code).

// pkg/runtime/decode_helpers.go
type APERDecodable interface { /* ... */ }
type UPERDecodable interface { /* ... */ }

// pkg/runtime/marshal.go
type APERExtensibleMarker interface { APERExtensible() }
type APERAlternativeChooser interface { APERAlternativeForID(id int64) int }

// pkg/runtime/types.go
type BitString  struct { /* Bytes []byte; BitLength int */ }
type OctetString = []byte
type Enumerated  = int64
type ObjectIdentifier = []uint64
```

## 5. Generation Flow

```bash
cd codecs/asn1-go

# Toy schema (round-trip smoke test)
go run ./cmd/asn1go -o ./generated -p testgen testdata/simple.asn1
go test ./generated/...

# Real 3GPP NGAP (TS 38.413)
go run ./cmd/asn1go \
  -o ./protocols/ngap/generated \
  -p ngap \
  protocols/ngap/asn.1/*.asn
go test ./protocols/ngap/generated/...

# S1AP (TS 36.413)
go run ./cmd/asn1go \
  -o ./protocols/s1ap/generated \
  -p s1ap \
  protocols/s1ap/asn.1/*.asn
go test ./protocols/s1ap/generated/...
```

`pycrate_interop_test.go` round-trips a curated set of NGAP PDUs
against the pycrate reference encoder for byte-equality.

## 6. Key Types

The generated code emits per-protocol Go types directly mapped from
the `.asn`. Examples from `protocols/ngap/asn.1/NGAP-CommonDataTypes.asn`
and `NGAP-Constants.asn`:

```
NGAP-CommonDataTypes:
  Criticality          ENUMERATED { reject, ignore, notify }
  Presence             ENUMERATED { optional, conditional, mandatory }
  ProcedureCode        INTEGER (0..255)
  ProtocolIE-ID        INTEGER (0..65535)
  ProtocolExtensionID  INTEGER (0..65535)
  TriggeringMessage    ENUMERATED { initiating-message,
                                    successful-outcome,
                                    unsuccessful-outcome }
  PrivateIE-ID         CHOICE { local INTEGER (0..65535),
                                global OBJECT IDENTIFIER }

NGAP-Constants:
  id-AMFConfigurationUpdate                   ProcedureCode ::= 0
  id-AMFStatusIndication                      ProcedureCode ::= 1
  ... (~70 procedure codes)
  id-AllowedNSSAI                             ProtocolIE-ID ::= 0
  ... (~700 IE IDs)
```

These map to:

```go
type Criticality int64
const ( CriticalityReject Criticality = 0; CriticalityIgnore = 1; CriticalityNotify = 2 )

type ProcedureCode int64
const ( IdAMFConfigurationUpdate ProcedureCode = 0; ... )

type ProtocolIE_ID int64
const ( IdAllowedNSSAI ProtocolIE_ID = 0; ... )
```

The bulk of `ngap_ies.go` (~38k LOC) is one Go struct per
NGAP IE, plus the `<ObjectSet>Value` typed CHOICE wrappers per
IE container — the README's "Parameterised-template expansion"
matrix entry.

## 7. Stubs / TODOs

| Site | Comment |
|------|---------|
| `pkg/codegen/generator.go:226` | `// TODO: <name> (unhandled type)` — fallback comment for AST nodes the codegen path doesn't yet emit |

Top-level remaining gaps from the README's "Remaining gaps" list
(README.md:53-69):

1. IE-builder helpers (ergonomic `NewNGSetupRequest(...)` constructors).
2. Constraint resolution for `id` + `@id` routing on decode.
3. Fragmentation for length determinants ≥ 16384 (rare in 3GPP).
4. Constrained-alphabet character strings (X.691 §27 alphabet
   optimisation skipped — `pkg/runtime/perstring.go:162`).
5. `REAL` encoding (not needed for 3GPP).

## 8. References

The compiler implements ITU-T standards; spec citations grounded in
source:

- **ITU-T X.680** — ASN.1 basic notation (`pkg/lexer/lexer.go:3`,
  `pkg/parser/parser.go:4`, README §References).
- **ITU-T X.681** — Information Object specification (`pkg/ast/ast.go:277`,
  `pkg/parser/parser.go:1020`, README).
- **ITU-T X.682** — Constraint specification (README; parser comment
  at `parser.go:4`).
- **ITU-T X.683** — Parameterisation (`pkg/ast/ast.go:323`, README).
- **ITU-T X.691** — Packed Encoding Rules (aligned and unaligned):
  - §10.2 open type — `pkg/runtime/perstring.go:255`
  - §10.5 constrained whole number — `pkg/runtime/perbitdata.go:127`
  - §10.5.6 / §10.5.7 — `pkg/runtime/perbitdata.go:138`
  - §10.6 normally small non-negative — `pkg/runtime/perbitdata.go:323`
  - §10.9 length determinant — `pkg/runtime/perbitdata.go:247`
  - §18.7 single extension addition group — `pkg/runtime/marshal.go:316`
  - §27 known-multiplier character strings — `pkg/runtime/perstring.go:162,164`
- **3GPP TS 38.413** — NGAP (README:87,100,211; ngap_smoke_test.go:36,55).
- **3GPP TS 36.413** — S1AP (README:211; `scripts/extract_s1ap_asn1.py:5`).
- **3GPP TS 36.331** — RRC (E-UTRA) (README:211 — planned).
- **3GPP TS 38.331** — NR RRC (README:211 — planned).

External references:

- `nf/upf/DESIGN.md §3.3` — companion design doc for the sister
  TLV codecs.

---
*Last refreshed against commit `13a181d`.*

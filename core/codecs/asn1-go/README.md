<!-- Copyright (c) 2026 MakeMyTechnology. All rights reserved. -->

# asn1go — ASN.1 → Go compiler for 3GPP PER/UPER

An ASN.1 compiler in Go that reads 3GPP-style `.asn1` schemas and generates
idiomatic Go source with **APER** (Aligned PER) and **UPER** (Unaligned PER)
encode/decode methods.

Code generation uses [`github.com/dave/jennifer`](https://github.com/dave/jennifer).

## Status

**MVP foundation is working end-to-end:** lexer → parser → AST → resolver →
code generator → runtime → round-trip encode/decode.

The simple test schema (`testdata/simple.asn1`) compiles cleanly and
round-trips through `Item`, `ItemID`, `Priority`, and `Response` types.

### What works today

| Feature | Status |
|---|---|
| Full ASN.1 lexer (X.680 tokens, comments, bstring/hstring/cstring, hyphenated idents, 2-char ops like `::=` `..` `...` `[[` `]]`) | ✅ |
| AST for all ASN.1 constructs needed by 3GPP | ✅ |
| Parser: modules, imports/exports, tag modes, type & value assignments | ✅ |
| Parser: INTEGER (named numbers), ENUMERATED (extensible), BIT/OCTET STRING, CHOICE, SEQUENCE (OPTIONAL/DEFAULT/extensions `...`/addition groups `[[ ]]`), SEQUENCE OF, SET OF, TaggedType | ✅ |
| Parser: character strings (Printable/UTF8/IA5/Visible/...) | ✅ |
| Parser: constraints (value range, SIZE, UNION `|`, INTERSECTION `^`, SingleValue, extensibility `(..., ...)`, table-constraint skeleton `{ObjectSet}{@field}`) | ✅ |
| Parser: Information Object Class skeleton (`CLASS { &id ... }`) | ✅ |
| Parser: parameterized type definitions & instantiations (3GPP IE containers) | ✅ basic |
| Resolver: cross-module symbol table, value-constant lookup, IMPORTS binding | ✅ |
| Codegen: ASN.1 → Go type mapping (INTEGER→int64, ENUM→int64+const, BIT STRING→BitString, OCTET STRING→[]byte, SEQUENCE→struct, CHOICE→tagged union struct, SEQ OF→slice) | ✅ |
| Codegen: struct tags (`aper:"sizeLB:X,sizeUB:Y,valueExt,..."`) | ✅ |
| Codegen: specialised Marshal/Unmarshal for primitive named types (INTEGER/ENUM/OCTET STRING/CHAR STRING/BIT STRING/BOOLEAN) | ✅ |
| Codegen: reflection-based Marshal/Unmarshal for SEQUENCE/CHOICE/SEQUENCE OF | ✅ |
| Runtime: `PerBitData` with bit/byte read/write, constrained-whole, length-determinant, normally-small non-negative, semi-constrained, unconstrained, BIT STRING, OCTET STRING, KM character strings, open-type | ✅ |
| APER vs UPER toggle via single `Aligned` bool — alignment code is centralised | ✅ |
| CLI (`asn1go`) using cobra | ✅ |

### What works for 3GPP now

| Feature | Status |
|---|---|
| `WITH SYNTAX` block parsed into literal-token → class-field map | ✅ |
| Object set assignments (`MyIEs CLASS ::= { obj1 \| obj2 \| ..., ... }`) | ✅ |
| Object bodies resolved against class WITH SYNTAX — positional literals (ID/CRITICALITY/TYPE/PRESENCE) bound to class field refs (&id/&criticality/&Value/&presence) | ✅ |
| Typed `<ObjectSet>Value` CHOICE emitted per object set, with one alternative per entry + `Present` constants | ✅ |
| `<ObjectSet>Entry` struct emitted with id/criticality/value/presence fields — mirrors `ProtocolIE-Field` | ✅ |
| **Parameterised-template expansion** — `ProtocolIE-Container{{NGSetupRequestIEs}}` inside a user SEQUENCE auto-expands to `[]NGSetupRequestIEsEntry` | ✅ |
| **Open-type wrapping** — the `Value` field is encoded as an open-type-length-prefixed wrapper around the selected CHOICE alternative, matching the 3GPP wire format | ✅ |
| End-to-end round-trip on `testdata/ngap_sample.asn1` — `NGSetupRequest` with multiple typed IEs encodes and decodes correctly | ✅ |

### Remaining gaps

1. **IE-builder helpers** — ergonomic `NewNGSetupRequest(globalRANNodeID, ...)`
   constructors that assemble the ProtocolIEs slice are not yet generated;
   callers build the slice literal themselves today.

2. **Constraint resolution for `id` + `@id` routing on decode** — on decode
   the Value field is decoded based on its declared type (the typed CHOICE);
   we do not yet validate that the `id` matches the selected alternative.

3. **Fragmentation** for length determinants ≥ 16384 (rare in 3GPP PDUs).

4. **Constrained-alphabet character strings** — the current `PrintableString`
   implementation uses 7-bit fixed width; the alphabet-constraint
   optimisation path in X.691 §27 is skipped.

5. **REAL** encoding — not needed for 3GPP.

## Layout

```
asn1go/
├── cmd/asn1go/main.go                       CLI (cobra)
├── pkg/lexer/                               Tokenizer (X.680)
├── pkg/parser/                              Recursive-descent parser
├── pkg/ast/                                 AST node types
├── pkg/resolver/                            Cross-module symbol resolution
├── pkg/codegen/                             Go code generation (jennifer)
├── pkg/runtime/                             PER/UPER bit-level encode/decode
├── testdata/simple.asn1                     Toy test schema
├── generated/                               Output of: asn1go testdata/simple.asn1
├── scripts/pycrate_compare.py               Reference encoder for byte-equality checks
└── protocols/                               One subdirectory per protocol family
    └── ngap/
        ├── asn.1/                           Real 3GPP NGAP (TS 38.413) source files
        └── generated/                       asn1go-generated Go (package ngap)
```

## Build & run

```
go build ./...

# Toy schema
go run ./cmd/asn1go -o ./generated -p testgen testdata/simple.asn1
go test ./generated/...

# Real 3GPP NGAP (TS 38.413)
go run ./cmd/asn1go -o ./protocols/ngap/generated -p ngap protocols/ngap/asn.1/*.asn
go test ./protocols/ngap/generated/...
```

## Helpers

The runtime ships pretty-printers for log/debug output that understand the
generated CHOICE / Entry / enum shapes:

```go
import "github.com/edgeq/asn1go/pkg/runtime"

var msg ngap.NGSetupRequest
out, _ := runtime.DecodeAPERToJSON(rawAperBytes, &msg)
fmt.Println(out)
```

renders as:

```json
{
  "ProtocolIEs": [
    {
      "Criticality": "ignore",
      "Id": 82,
      "Value": { "RANNodeName": "gNB-12345" }
    },
    {
      "Criticality": "ignore",
      "Id": 21,
      "Value": { "PagingDRX": "v128" }
    }
  ]
}
```

CHOICE structs collapse to the selected alternative, enums render by their
ASN.1 names, codec-only fields (`Presence`, `Present` index) are hidden, and
nil pointers are omitted. `runtime.PrettyJSON(v)` and `runtime.MustPrettyJSON(v)`
are available for already-decoded values; `DecodeUPERToJSON` is the UPER twin.

### Data-only view (Wireshark style)

For an even cleaner display that drops the Id / Criticality / ProtocolIEs
scaffolding entirely:

```go
out, _ := runtime.DecodeAPERToData(rawAperBytes, &msg)
fmt.Println(out)
```

renders the same NGSetupRequest as:

```json
{
  "RANNodeName": "gNB-12345",
  "PagingDRX": "v128"
}
```

Just the IE names and their decoded values, like a Wireshark protocol tree.
Use `runtime.DataJSON(v)` / `runtime.MustDataJSON(v)` on already-decoded
values, or `DecodeUPERToData` for UPER input.

## Adding a new protocol

1. `mkdir -p protocols/<name>/asn.1 protocols/<name>/generated`
2. Drop the `.asn`/`.asn1` source files in `protocols/<name>/asn.1/`
3. `go run ./cmd/asn1go -o ./protocols/<name>/generated -p <name> protocols/<name>/asn.1/*.asn`
4. Write tests in `protocols/<name>/generated/`

## Generated type example

`testdata/simple.asn1`:
```asn1
ItemID ::= INTEGER (0..65535)
```
generates (excerpt):
```go
type ItemID int64

func (m *ItemID) MarshalAPER() ([]byte, error) {
    w := runtime.NewWriter(true)
    if err := w.PutConstrainedWhole(int64(*m), 0, 65535); err != nil {
        return nil, err
    }
    return w.Bytes(), nil
}
```

## CLI

```
asn1go [flags] <input.asn1> [input2.asn1 ...]

  -o, --output string      Output directory (default "./generated")
  -p, --package string     Go package name (default "asn1gen")
  -m, --module string      Go module path (for runtime import)
  --encoding string        "aper" | "uper" | "both" (default "aper")
  --test                   Generate round-trip test files
  -v, --verbose            Verbose output
```

## Reference standards

- ITU-T X.680 (ASN.1 basic notation)
- ITU-T X.681 (Information Object specification)
- ITU-T X.682 (Constraint specification)
- ITU-T X.683 (Parameterisation)
- ITU-T X.691 (Packed Encoding Rules — aligned and unaligned)
- 3GPP TS 38.413 (NGAP), 36.413 (S1AP), 36.331 (RRC), 38.331 (NR RRC), etc.

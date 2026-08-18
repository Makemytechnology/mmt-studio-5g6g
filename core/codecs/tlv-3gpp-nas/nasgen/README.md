<!-- Copyright (c) 2026 MakeMyTechnology. All rights reserved. -->

# nasgen — 3GPP NAS TLV codec generator (Go)

Generates type-safe Go Encode/Decode code for 5G NAS (5GMM + 5GSM) and LTE NAS (EMM + ESM)
messages from YAML definitions that mirror the TS 24.501 / 24.301 message tables.

Built on `github.com/dave/jennifer/jen`.

## Layout

```
nasgen/
├── cmd/nasgen/            CLI entry point
├── pkg/
│   ├── runtime/           byte-level TLV primitives + NAS common types
│   ├── schema/            YAML schema types + loader
│   └── codegen/           jennifer-based Go source emitter
├── definitions/           YAML message + IE type definitions
├── generated/             (output of `go run ./cmd/nasgen`)
└── testdata/              known-good hex for round-trip tests
```

## Build & run

```bash
cd nasgen
go mod tidy
go test ./pkg/runtime/...                                  # runtime primitives
go run ./cmd/nasgen -d ./definitions -o ./generated -p nas # regenerate Go
go build ./generated/...                                   # must compile
```

## Reference specs (in `specs/3gpp/` at repo root)

- TS 24.007 v19.5.0 (`ts_124007v190500p.pdf`) — IE format types (Type 1/2/3/4/6 = V / T / TV / TLV / LV / TLV-E / LV-E)
- TS 24.008 v19.5.0 (`ts_124008v190500p.pdf`) — shared IE definitions (APN, QoS, PDP address, LAI, timers, network name, ...)
- TS 24.301 v19.6.0 (`ts_124301v190600p.pdf`) — LTE NAS stage 3 (EMM + ESM)
- TS 24.501 v19.6.0 (`ts_124501v190602p.pdf`) — 5G NAS stage 3 (5GMM + 5GSM)

## Status

| Protocol family | Messages | Status |
|-----------------|---------:|--------|
| 5GMM — TS 24.501 §8.2 | 28 | done, round-trip tested |
| 5GSM — TS 24.501 §8.3 | 16 | done, round-trip tested (auto-generated `PDUSessionID` + `PTI` struct fields) |
| LTE EMM — TS 24.301 §8.2 | 32 | done, round-trip tested |
| LTE ESM — TS 24.301 §8.3 | 23 | done, round-trip tested (auto-generated `EPSBearerIdentity` + `PTI` struct fields) |
| **Total** | **99 messages** | **174 IE types** |

`go test ./...` exercises:
- 5 runtime primitive tests (TLV/TLV-E/half-octet/PLMN BCD/unknown-IE skip)
- 22 procedure round-trip tests across every 5GMM/5GSM procedure family
- 31 procedure round-trip tests across every EMM/ESM procedure family (attach, TAU, detach, auth, SMC, identity, bearer context activate/modify/deactivate, PDN connectivity/disconnect, bearer resource, ESM info, status)
- 5G + LTE security-header surfacing through the dispatcher
- malformed-input no-panic guarantee
- **51 pycrate interop fixtures** — `TestPycrateInterop` consumes PDUs encoded by
  [pycrate](../../mmt_studio_core/libs/pycrate-master/) (TS24501_FGMM, FGSM,
  TS24301_EMM, ESM) and confirms nasgen's dispatcher decodes every message to
  the correct concrete Go type with zero errors. Run
  `python testdata/gen_pycrate_fixtures.py` to regenerate
  `testdata/pycrate_fixtures.json`. Byte-level round-trip is informational
  (both decoders can be spec-correct while producing different valid byte
  streams — zero-padded optional IEs, variable-length fields with different
  length choices). Current results: **51/51 decode, 48 byte-identical, 3 cosmetic.**

All IEs with non-trivial internal structure we haven't fully decomposed yet
(QoS rules/flows, EAP messages, ePCO, SOR containers, LADN info, S-NSSAI, DNN,
etc.) are modeled as **byte-container IEs** — their Go structs expose `Value []byte`
and round-trip correctly at the byte level. Refine their `fields:` in the YAML
when you need accessor-level decomposition; no message-level changes needed.

## Extending

1. Add message rows to `definitions/*_messages.yaml` following the spec tables.
2. Add any new IE types to `definitions/*_ie_types.yaml`.
3. Re-run `go run ./cmd/nasgen`. Every IE type becomes one file (`ie_<name>.go`),
   every message becomes one file (`msg_<name>.go`), plus `dispatcher.go`.

## Safety guarantees

Every generated `Decode`:
- bounds-checks every read — never panics
- returns `runtime.NASDecodeError` with `{msg, ie, offset, underlying}`
- skips unknown optional IEIs (TS 24.007 forward-compatibility rule)
- validates min-length before decoding structured IEs

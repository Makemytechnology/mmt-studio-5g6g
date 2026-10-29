# oam/otel — OpenTelemetry observability

> 3GPP TS 28.552 §6 (PM via OTEL exporters) · TS 28.554 §5 (E2E KPIs
> mapping to OTEL traces) · W3C Trace Context
> [`https://www.w3.org/TR/trace-context/`](https://www.w3.org/TR/trace-context/).
> Operator-side surface (`/api/otel/*`) is fully functional today;
> OTLP / Prometheus export is deferred until the
> `go.opentelemetry.io/otel` SDK is vendored.

## Part A — Functional view

### A.1 What this gives the operator

A live span ring + counters that operators can interrogate **without**
running a full collector. Spans emitted from any NF (or from operator
drills via `/api/otel/test-span`) land in:

1. an in-memory ring (default 5 000 spans, FIFO eviction),
2. a per-`(nf, operation)` counter map,
3. a `spans_emitted` running total.

The panel reads `/api/otel/status` for capacity + counts, then drills
into `/api/otel/spans` for filtering or `/api/otel/spans/{trace_id}`
for the full tree of a specific call.

### A.2 Why W3C-format IDs from day one

The ring is a temporary holding area until the SDK vendor lands; once
it does, the same `Span` records lift directly into OTLP without an ID
remap. So we generate W3C-shape identifiers now:

| Field         | Bytes | Hex chars | Source              |
|---------------|-------|-----------|---------------------|
| `trace_id`    | 16    | 32        | `crypto/rand` + fallback |
| `span_id`     | 8     | 16        | `crypto/rand` + fallback |
| `parent_span_id` | 8 | 16        | client-supplied; empty for roots |

The fallback path (deterministic non-zero from time) only fires when
`crypto/rand.Read` errors — never zeroing IDs is the invariant the
tests pin.

### A.3 Endpoints

| Method | Path                          | Purpose |
|--------|-------------------------------|---------|
| GET    | `/api/otel/status`            | Config + ring + counters + `sdk_vendored` sentinel |
| GET    | `/api/otel/config`            | Current `otel_*` config view |
| PATCH  | `/api/otel/config`            | Sparse update — allow-list `otel_*` keys; bad exporter or empty patch → 400 |
| POST   | `/api/otel/test-span`         | Operator/tester smoke span; supports parent linkage + duration pinning |
| GET    | `/api/otel/spans`             | Ring readback; filters: `trace_id`, `nf`, `operation`, `limit` |
| GET    | `/api/otel/spans/{trace_id}`  | Single trace tree (sorted by `start_time`); 404 if not in ring |
| GET    | `/api/otel/counters`          | Per-`(nf, operation)` count map |
| POST   | `/api/otel/reset`             | Zero ring + counters + `spans_emitted` |

All responses are `{ok: true, ...}` envelopes.

### A.4 Behaviours pinned by the tester (`tc_otel.py`)

| TC-ID | Behaviour |
|-------|-----------|
| TC-OTEL-001 | `/status` carries `config`, `ring_size`, `ring_capacity`, `spans_emitted`, `counter_keys`, `sdk_vendored` |
| TC-OTEL-002 | Bad exporter → 400; empty patch → 400; valid patch round-trips through `/config` |
| TC-OTEL-003 | `test-span` emits W3C-format IDs; default `status=ok`; attribute + event preserved |
| TC-OTEL-004 | Child span with `parent_trace_id` + `parent_span_id` lands on the same trace tree |
| TC-OTEL-005 | `?nf=&operation=` filter is exact-match; counters key by `nf:operation` |
| TC-OTEL-006 | Unknown `trace_id` → 404 (not 200 with empty list) |
| TC-OTEL-007 | `/reset` zeroes ring, counters, and `spans_emitted` |
| TC-OTEL-008 | Explicit `status="error"` is preserved on the span |

### A.5 What is *not* covered yet

- OTLP gRPC push of spans/metrics/logs.
- Prometheus exporter on `otel_prometheus_port`.
- Console exporter (debug builds).

These all gate on vendoring `go.opentelemetry.io/otel`; the
`sdk_vendored: false` sentinel in `/status` lets dashboards branch on
it.

## Part B — Design

### B.1 File layout

```
oam/otel/otel.go                — Config + Span + ring + counters + Init
webservice/app/routes_otel.go   — /api/otel/* HTTP surface
webservice/cmd/sacore-web/main.go — calls otel.Init() at boot
db/schemas/infra.go             — otel_* columns + CHECK on otel_exporter
docs/design/oam/otel.md         — this doc
```

Tester:

```
src/testcases/oam/tc_otel.py    — 8 TCs (TC-OTEL-001 … 008)
```

### B.2 Span shape (mirrors OTLP `Span` protobuf)

```go
type Span struct {
    TraceID, SpanID, ParentSpanID string  // hex, W3C-shape
    NF, Operation                 string
    StartTime, EndTime            int64   // unix-micros
    DurationMs                    float64 // computed at End()
    Status                        string  // "ok" | "error" | ""
    Attributes                    map[string]string
    Events                        []SpanEvent
}
```

`StartTime` is rewritable by the test surface (`duration_us > 0`) so
the tester can pin durations without sleeping in-process.

### B.3 Public API

```go
otel.Init()                              // boot hook — wires logger sink, INFOs config
otel.Status() map[string]any              // /status payload
otel.LoadConfig() Config                  // /config GET
otel.UpdateConfig(patch map[string]any) (Config, error)
otel.ValidExporter(s string) bool         // prometheus|otlp|console
otel.IsBadInput(err error) bool           // 400 vs 500 mapping

otel.StartSpan(nf, op, parentTID, parentSID string) *Span
(*Span).SetAttribute(k, v string)
(*Span).AddEvent(name string, attrs map[string]string)
(*Span).End(status string)                // ring-pushes, bumps counter

otel.RecentSpans(limit int) []Span
otel.FilterSpans(traceID, nf, op string, limit int) []Span
otel.GetTrace(traceID string) []Span      // sorted by StartTime
otel.SpanCounters() map[string]int64
otel.ResetSpans()
```

### B.4 Ring + counters concurrency

A single `sync.RWMutex` per structure (one for the ring, one for the
counter map). Reads (panel + tester) take the read lock; writes
(`End()` + reset) take the write lock. The ring is a fixed-cap slice
with a head index — eviction is O(1).

### B.5 Config patch surface

`PATCH /api/otel/config` accepts only the seven `otel_*` keys. Empty
patch → 400 (`no otel_* fields in patch`). Exporter outside
`{prometheus, otlp, console}` → 400 via `IsBadInput(err)` mapping —
keeps the schema CHECK from leaking as a 500. The patch path uses
`crud.UpdateInfraConfig` so the existing infra-config audit trail
applies.

### B.6 W3C-shape ID generation

`crypto/rand.Read` fills the byte buffer; on error a deterministic
non-zero value derived from `time.Now().UnixNano()` is stamped
instead. The all-zero case is explicitly avoided — the W3C spec
classes it as invalid.

### B.7 Boot sequence

`webservice/cmd/sacore-web/main.go` calls `otel.Init()` before the
domain-route registration so the first request sees a populated
counter map (zero) and a non-nil ring. `Init()` is idempotent.

### B.8 Deferred (when the SDK lands)

Vendoring `go.opentelemetry.io/otel/sdk/trace` lets us:

1. Push every ring-entered span over OTLP gRPC to `otel_endpoint`.
2. Surface `/metrics` with the OTEL Prometheus exporter on
   `otel_prometheus_port` (replacing the per-package
   `oam/pm/prometheus.go` ad-hoc exporter).
3. Build the console exporter for debug builds.

The ring + counters stay — they're the panel-side store and don't
duplicate OTLP work.

### B.9 References

- W3C Trace Context · [`https://www.w3.org/TR/trace-context/`](https://www.w3.org/TR/trace-context/)
- 3GPP TS 28.552 §6 — PM via OTEL exporters
- 3GPP TS 28.554 §5 — E2E KPIs mapping to OTEL traces
- OpenTelemetry spec · `https://opentelemetry.io/docs/specs/otel/`

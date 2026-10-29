# oam/logger — Structured Logging (slog Wrapper)

## 1. Role / scope

`oam/logger` is the central log facade every package in the core
imports. It owns:

- the namespaced `*Logger` produced by `Get(name)` (~150 importers,
  ~384 call sites at last count — `oam/logger/redesign.go:14-15`);
- the producer hot path, with a hard budget of **~150-250 ns/op**
  per log call (`redesign.go:340-344`);
- a single drainer goroutine that fans entries out to console / file
  / GUI ring / SSE stream / OTEL sinks;
- IMSI tagging + allow-list filtering;
- runtime level changes via `SetLevel` and `/api/logger/level`;
- JSON output mode for ELK / Loki / Splunk pipelines
  ([OBSERVABILITY.md §1](../../OBSERVABILITY.md#1-structured-logs--elk--loki--splunk)).

The redesign that took the package from synchronous slog handler
chains to the bounded ring + drainer architecture is documented
verbatim in `oam/logger/redesign.go` — it is a doc-only Go file kept
next to the code so `go doc oam/logger` shows the contract.

## 2. Architecture

```
   +----------------------------+
   | (*Logger).Info("...", kv...)|     ~150-250 ns/op
   +-------------+--------------+
                 | level check (atomic load on levelVar)
                 | extract imsi=, accumulate kv extras
                 | imsiAllowed lookup (RLock)
                 | sync.Pool.Get *Entry
                 | stamp TS/Level/Module/IMSI/Message
                 v
   +-------------+--------------+
   | logRing.EnqueueAssign(e)   |  drop-OLDEST on overflow,
   |  -> assigns Seq atomically |  bumps drops + overwrites counters
   +-------------+--------------+
                 |
                 v
   +-------------+--------------+
   | drainer goroutine          |  every 5 ms (tick) or batch ready,
   |  PopBatch -> []*Entry      |  Emit to every Sink in order;
   |  fanout to sinks           |  recover() catches sink panics +
   |  recycle entries to pool   |  auto-Unregister the offender
   +-------------+--------------+
                 |
                 v fanout
   +----------------------------+
   |  Sink registry (RWMutex)   |
   +----+----+----+----+----+---+
        |    |    |    |    |
        v    v    v    v    v
     console file GUI  SSE  OTEL placeholder
     (TTY  (rotating ring  stream  (oam/otel.Init
      color) 50MBx5)        broadcast registers)
                  |
                  v
        siBuffer.snapshot(afterSeq, level, imsi, module, limit)
                  ^
                  |  GetEntries(...)  (GUI panel poll)
                  |  Flush(timeout) before read for crispness
                  |
   GUI / REST tail callers
```

**Producer / consumer split.** The producer does no I/O, no
formatting, and no allocation past the `sync.Pool.Get`. Every sink —
console color rendering, file write, SSE broadcast, OTEL counter —
runs in the drainer goroutine. Per `redesign.go:316-344` this
sharpens the AMF/NAS handler tail under burst load (5k-15 µs per call
when slog handlers were inline -> 150-250 ns flat after the
redesign).

**Entry shape:** `Entry { Seq, TS, TSFmt, Level, LevelNo, Module,
IMSI, Message }` — `logger.go:69-78`. `Seq` is atomic and monotonic
across the process lifetime; `TSFmt` is filled by `bufferSink` so the
GUI tail column matches `formatLine`'s on-disk format.

**Overflow.** `ringBuf` (`oam/logger/ring.go`) is mutex-guarded with
drop-oldest-to-make-room. A Vyukov-style lock-free MPMC queue was
considered (`redesign.go:218-225`) but at the observed AMF rate the
mutex variant didn't show contention; the public surface is stable
either way. Two atomic counters: `Drops()` (new entry rejected when
full — currently unused; the implementation overwrites instead) and
`overwrites` (older entry pushed out to make room).

## 3. File map

| File | LOC | Role |
|---|---:|---|
| `logger.go` | 627 | public API surface — `Get`, `*Logger`, `SetLevel`, `SetIMSIFilter`, `Configure`, `GetEntries`, `Drops`, `Flush`, `CriticalSync`, `RegisterSink` |
| `redesign.go` | 400 | doc-only file: design contract, invariants I1-I7, migration plan |
| `ring.go` | 126 | `ringBuf` — mutex MPMC, drop-oldest, drops + overwrites counters |
| `drainer.go` | 219 | single background goroutine, batch emit, sentinel-barrier flush, panic-isolated sinks |
| `entry_pool.go` | 38 | `sync.Pool[*Entry]` — recycle producer-side allocations |
| `format.go` | 71 | `formatLine` — canonical text format + ANSI color decisions |
| `rotating_file.go` | 95 | size + count rotation (50 MB × 5 by default) |
| `sink.go` | 31 | `Sink` interface (`Name`, `Emit`, `Flush`, `Close`) |
| `sink_console.go` | 50 | TTY color, plain-text format |
| `sink_file.go` | 72 | wraps the rotating file |
| `sink_buffer.go` | 113 | the GUI ring (`snapshot` is the read API) |
| `sink_json.go` | 72 | JSON-per-line emitter for `SACORE_LOG_JSON=1` |
| `sink_stream.go` | 157 | SSE / WS fan-out for `/api/logs/tail` |
| `sink_otel.go` | 62 | OTLP placeholder (registered by `oam/otel.Init`) |
| `tty_unix.go` / `tty_windows.go` | 19 / 12 | OS-specific `isTTY` |

Tests: `logger_test.go`, `drainer_test.go`, `sink_json_test.go`,
`sink_stream_test.go`.

## 4. Public API / contracts

### Loggers

```go
log := logger.Get("amf.ngap")           // namespace becomes "mmt-core.amf.ngap"
log.Info("NG Setup complete", "imsi", "001010000000001")
log.Warnf("retry %d/%d", attempt, max)
child := log.WithIMSI("001010000000001") // sticky IMSI tag
```

`(*Logger)` provides **eight** methods kept byte-identical to the
Python reference (`redesign.go:65-71` invariant I1):
`Debug/Info/Warn/Error(msg, kv...)` and the printf-style
`Debugf/Infof/Warnf/Errorf(format, args...)`.

### slog handlers

The package no longer goes through an `slog.Handler` chain — the
redesign moved formatting + I/O into post-drainer sinks
(`redesign.go:54-62`). The `slog.LevelVar` is reused for the atomic
level gate (`logger.go:57`); methods accept slog-style key/value
extras after the message:

```go
log.Info("FSM transition", "from", "IDLE", "to", "AUTH", "imsi", imsi)
```

`imsi` is intercepted (`logger.go:332-342`). Other extras are
appended as `" k=v"` suffixes — same shape the legacy handler
emitted, so operator greps stay stable.

### Sinks

The `Sink` interface (`oam/logger/sink.go:11-31`):

```go
type Sink interface {
    Name() string
    Emit(batch []*Entry)
    Flush() error
    Close() error
}
```

Sinks are owned by the drainer goroutine; the `*Entry` pointers are
recycled into `sync.Pool` immediately after every `Emit` returns —
sinks **must not** retain them.

Built-in sinks registered automatically by `rebuildRootLocked`
(`logger.go:531-579`):

| Sink | When |
|---|---|
| `consoleSink` (`siConsole`) | always (text mode) |
| `jsonSink` over stdout | when `SACORE_LOG_JSON=1` (replaces console) |
| `fileSink` (`siFile`) | when `SACORE_LOG_FILE` set or `Configure` chose `disk`/`tmpfs` |
| `jsonSink` over file | when `SACORE_LOG_JSON=1` AND a file is loaded |
| `bufferSink` (`siBuffer`) | always — feeds `GetEntries` |
| `streamSink` (`siStream`) | always — fan-out hub for `/api/logs/tail` SSE |
| `otelSink` | when `oam/otel.Init` registers it (gated on `otel_logs_enabled`) |

`RegisterSink(s Sink)` / `UnregisterSink(s Sink)` are public so tests
and `oam/otel` can extend the fan-out. Panics in a sink are caught by
the drainer's `recover()` block and the offender is auto-removed
(`redesign.go:367-375`).

### IMSI tagging

```go
logger.SetIMSIFilter([]string{"001010000000001", "001010000000002"})
logger.GetIMSIFilter()
logger.ReloadIMSIFilter()  // re-read SACORE_LOG_IMSI from env
```

When the allow-list is non-empty, only matching IMSIs (and infra logs
with no IMSI tag at all) reach the ring (`logger.go:581-592`). The
filter is read under `mu.RLock`; updates are atomic-feel (RWMutex) and
also write back to `SACORE_LOG_IMSI` so a child process inherits.

The `WithIMSI` derived logger (`logger.go:297`) carries a sticky
IMSI; methods accepting `kv...` can also override it inline by
passing `"imsi", "..."`.

### Environment variables

Cross-link
[OBSERVABILITY.md "Log-level + IMSI filtering"](../../OBSERVABILITY.md#log-level--imsi-filtering):

| Env | Effect | Code |
|---|---|---|
| `LOG_LEVEL` | DEBUG / INFO / WARNING / ERROR — initial global level | `parseLevelEnv` `logger.go:594-605` |
| `SACORE_LOG_IMSI` | comma-separated allow-list | `ReloadIMSIFilter` `logger.go:176-194` |
| `SACORE_LOG_JSON` | `=1` swaps every sink to JSON-per-line | `rebuildRootLocked` `logger.go:546-567` |
| `SACORE_LOG_FILE` | rotating file path (default: none) | `initDefault` `logger.go:509-520` |
| `NO_COLOR` | disables ANSI codes on TTY sinks | `rebuildRootLocked` `logger.go:556` |

### Runtime control surfaces

| Func | Effect |
|---|---|
| `SetLevel(name string) bool` | DEBUG/INFO/WARN/WARNING/ERROR; updates `levelVar` and re-exports `LOG_LEVEL`. Webservice route `/api/logger/level` calls this. |
| `GetLevel() string` | current level name |
| `SetIMSIFilter([]string)` / `GetIMSIFilter() []string` / `ReloadIMSIFilter()` | manage the allow-list at runtime |
| `SetLogFile(path string) error` / `GetLogFile() string` | swap the rotating file at runtime; `""` to detach |
| `Configure(Config) error` | tear down + rebuild handler stack from `{Level, FilePath, Sink, BufferSize, UseColor}`. `Sink` ∈ `disk` / `tmpfs` / `ram_only` / `syslog` / `journald` (last two not yet implemented; fall back to console+buffer) |

### GUI ring + live tail

```go
GetEntries(afterSeq int64, level, imsi, module string, limit int) []Entry
ClearBuffer()
Drops() uint64                       // operator visibility for ring overflow
Flush(timeout time.Duration) error   // sentinel-barrier; called from SIGTERM
CriticalSync(module, msg string)     // bypass ring; panic / OOM / drainer-died only
```

`GetEntries` calls `logDrainer.flush(100ms)` before reading so test
callers that `log()` and immediately read see the most recent
producer entries (`logger.go:387-436`).

The live tail uses `streamSink.Subscribe` — per-subscriber buffered
channel (capacity 512, ~1.5s headroom at 5k logs/sec); slow consumers
get drop-on-full, not back-pressure.

## 5. Headline flows / lifecycle

### Boot

1. First `logger.Get(...)` triggers `initDefault` (sync.Once).
2. `LOG_LEVEL` is parsed; `SACORE_LOG_IMSI` is parsed; if
   `SACORE_LOG_FILE` is set, the rotating file is opened.
3. `rebuildRootLocked` builds the ring (4096 cap), the drainer, the
   appropriate console / file / JSON sinks, plus the always-on GUI
   buffer + SSE stream sinks.

### Producer hot path (`logger.go:322-371`)

1. Atomic `levelVar.Level()` -> early return if below.
2. Walk `kv` extras: extract `imsi=...`; collect any other key=value
   pairs into a `" k=v"` suffix string.
3. `imsiAllowed` lookup — drops disallowed entries before they burn
   a `Seq`.
4. `getEntry()` from the pool; stamp TS / Level / Module / IMSI /
   Message.
5. `logRing.EnqueueAssign(e, &seq)` — assigns `Seq` and inserts
   atomically; an evicted (overwritten) entry is recycled into the
   pool.

### Drainer cycle (`drainer.go`)

- Wakes every 5 ms or when the ring crosses a watermark.
- `PopBatch` up to 256 entries (`drainerBatchSize`).
- Acquires `RLock` on the sink list; calls `Emit(batch)` on each;
  panics caught by `defer/recover` and the offending sink is
  unregistered.
- Recycles every entry into `sync.Pool` after the last sink returns.

### Shutdown

`Flush(5*time.Second)` enqueues a sentinel; the drainer signals back
on the ack channel after every prior entry has been emitted **and**
every sink has returned from `Flush()`. The webservice SIGTERM
handler calls this before `os.Exit`.

`CriticalSync(module, msg)` is the escape hatch — bypasses the ring
and writes directly to stderr (and the rotating file when loaded).
Used only from panic recovery, the OOM notifier, and the drainer's
own "I am dying" self-report (`logger.go:466-479`).

### Wire format

`formatLine` (`oam/logger/format.go:26-65`):

```
2026-04-19 11:04:22:531 #00000017 INFO  [mmt-core.amf.gmm.fsm] [IMSI:001011234560001] FSM AUTHENTICATION -> SECURITY_MODE on AuthenticationResponse
```

JSON form (when `SACORE_LOG_JSON=1`, `oam/logger/sink_json.go`):

```json
{"ts":"2026-04-19T11:04:22.531+05:30","level":"INFO","module":"mmt-core.amf.gmm.fsm","imsi":"001011234560001","msg":"FSM AUTHENTICATION -> SECURITY_MODE on AuthenticationResponse"}
```

Keys are stable; see
[OBSERVABILITY.md §1](../../OBSERVABILITY.md#1-structured-logs--elk--loki--splunk)
for Filebeat / promtail recipes.

## 6. Stubs / TODOs

`grep -n TODO oam/logger/*.go`:

- `sink_otel.go:52` — `TODO(arch: oam/otel SDK landed)`: the
  `otelSink.Emit` body becomes a real OTLP `LogRecord` build + gRPC
  push once `go.opentelemetry.io/otel` is vendored. The sink already
  joins the drainer fan-out and bumps a `Seen()` counter today.
- `redesign.go` — entire file is a forward-looking design note (no
  symbols defined). Useful as the audit trail for the migration; the
  invariants I1-I7 are the contract the active code preserves.

`Configure`'s `journald` and `syslog` cases are not implemented (the
Go stdlib has no journald binding); they currently fall through to
the console+buffer default (`logger.go:277-280`).

`bufCap` is package-level state today; the redesign mentions exposing
it as `infra_config.log_ring_cap` (`redesign.go:215-218`) but that
column doesn't exist yet.

## 7. References

This package cites no 3GPP TS clauses in source; it carries the
3GPP-defined timestamp format (`YYYY-MM-DD HH:MM:SS:mmm`, see
`format.go:30-32` and OBSERVABILITY.md §1).

Cross-doc: [OBSERVABILITY.md
"Operator Observability"](../../OBSERVABILITY.md) — operator-facing
recipes for ELK / Loki / Filebeat / promtail and the
Prometheus-metrics + REST-snapshot channels that complement the log
stream.

---
*Last refreshed against commit `13a181d`.*

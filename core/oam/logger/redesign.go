// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// ──────────────────────────────────────────────────────────────────────
//  Planned redesign — hot-path inline, background sinks
// ──────────────────────────────────────────────────────────────────────
//
// This file is FORWARD-LOOKING design notes for a rewrite that will
// replace the synchronous multi-sink dispatch currently in
// logger.go:313-346 with a bounded ring + drainer model. It is a
// doc-only Go file; no symbols defined. Kept next to the code it
// will replace so the plan is visible at `go doc oam/logger`.
//
// The redesign preserves the public API surface exactly (150 files
// import oam/logger across ~384 call sites at the time of writing),
// so the migration is entirely behind the method bodies of
// *Logger.Debug/Info/Warn/Error/Debugf/Infof/Warnf/Errorf.
//
// # Why redesign
//
// logger.go:313 *Logger.log does this on the caller's goroutine:
//
//   1. l.base.Enabled()     — cheap (atomic load)
//   2. kv slice build       — one allocation
//   3. imsiAllowed lookup   — RLock + map read
//   4. l.base.Log(ctx, …)   — slog handler chain walks every sink
//                             SYNCHRONOUSLY: console color format +
//                             rotating file write + ANSI codegen
//   5. pushBuffer(…)        — bufMu.Lock() + list allocation
//
// Under NGAP registration bursts (>5k log lines/sec) steps 4 and 5
// dominate — file-write latency spikes and the bufMu lock stall the
// NAS handler goroutine. Operationally, "AMF hot path correlates
// with log volume" is a load-dependent tail we want gone. Also, the
// design couples new-sink additions (WS/SSE live-tail, OTLP export)
// to the hot-path contract — every new sink means either more
// inline work or a new mutex. That stops scaling.
//
// # Target shape
//
// Producers (every Logger method):
//
//     log.Info(msg, kv…)
//       ├─ level-enabled check   (atomic.LoadInt32 on levelVar)
//       ├─ IMSI filter           (read-only hash, no lock)
//       ├─ sync.Pool.Get *Entry  (no allocation after warmup)
//       ├─ stamp TS + copy msg,
//       │  module, imsi, kv      (no format, no I/O)
//       ├─ ring.Enqueue(entry)   (one CAS on MPMC bounded ring)
//       └─ return                (zero I/O, zero blocking)
//
// Drainer (single background goroutine):
//
//     for batch := range ring.PopBatch(N):
//       for each Sink s:
//         s.Emit(batch)          (format + write + network lives here)
//       pool.PutAll(batch)       (recycle entries for the producers)
//
// Everything that was inline in logger.go:313-346 moves into the
// drainer — with one exception: timestamping (time.Now()) stays
// producer-side because it's the wall-clock of when the event
// actually happened. TS *formatting* moves to the drainer.
//
// # Invariants the rewrite must preserve
//
// I1. Public API byte-identical. *Logger.{Debug,Info,Warn,Error}(msg,
//     kv…), *Logger.{Debug,Info,Warn,Error}f(fmt, args…),
//     WithIMSI, Get, SetLevel, GetLevel, SetIMSIFilter, ReloadIMSIFilter,
//     SetLogFile, GetLogFile, Configure, GetEntries, ClearBuffer —
//     same signatures, same semantics. The 150 importers MUST not
//     be touched.
//
// I2. Hot path runs in O(1) time bounded by ~200 ns median.
//     No syscalls on the producer side (time.Now on Linux uses the
//     vDSO fast path, not a syscall). No formatting. No I/O. No
//     lock longer than one CAS retry loop.
//
// I3. `fmt.Sprintf` for the *f family is UNAVOIDABLY inline —
//     callers who reach for Debugf have already paid for Sprintf
//     by choice. The kv family (Info(msg, "k", v, …)) DEFERS kv
//     rendering to the drainer: the producer copies the (any) values
//     into the Entry and the drainer converts them with fmt.Sprint
//     or slog.Attr as appropriate per sink.
//
// I4. Overflow policy is drop-OLDEST, not drop-newest and not block.
//     A drop-oldest ring is FIFO-overflowing; losing early entries
//     under sustained overload is the correct failure mode because
//     the symptom (ring full) almost always means something is
//     bursting NOW and the most recent context is what the operator
//     needs. An atomic drops_total counter is mirrored to PM
//     (oam/pm) so operators see non-zero in the status pane.
//
// I5. GetEntries — the web-GUI tail source — reads from a dedicated
//     sink_buffer that the drainer populates. GUI tail therefore
//     lags hot path by one batch cycle (typically well under 1 ms).
//     If this gap matters, expose a "flush before read" option on
//     GetEntries — but don't make the producer pay.
//
// I6. Graceful shutdown writes every entry that reached the ring
//     before Flush(timeout) was called. Flush enqueues a sentinel,
//     blocks until the drainer acks it. sacore-web calls Flush on
//     SIGTERM before os.Exit.
//
// I7. CriticalSync(msg) is the escape hatch for last-gasp messages
//     (panic handler, OOM notifier, drainer-died self-report). It
//     bypasses the ring and writes directly to stderr + file. Used
//     exclusively for "we are about to die" scenarios; every other
//     call site uses the ring-backed path.
//
// # Public API surface — unchanged vs added
//
// UNCHANGED (the 150 existing importers):
//
//     func Get(name string) *Logger
//     func SetLevel(name string) bool
//     func GetLevel() string
//     func SetIMSIFilter(imsis []string)
//     func GetIMSIFilter() []string
//     func ReloadIMSIFilter()
//     func SetLogFile(path string) error
//     func GetLogFile() string
//     func Configure(c Config) error
//     func GetEntries(afterSeq int64, level, imsi, module string, limit int) []Entry
//     func ClearBuffer()
//
//     type Logger struct{…}
//     func (*Logger) WithIMSI(imsi string) *Logger
//     func (*Logger) Debug/Info/Warn/Error(msg string, kv …any)
//     func (*Logger) Debugf/Infof/Warnf/Errorf(fmt string, a …any)
//
//     type Entry struct{ Seq int64; TS time.Time; TSFmt string; Level string;
//         LevelNo int; Module string; IMSI string; Message string }
//
//     type Config struct{ Level slog.Level; … }
//
// ADDED:
//
//     func Flush(timeout time.Duration) error
//       // Blocks until the drainer has processed every entry currently
//       // in the ring. Returns timeout error if the drainer did not
//       // ack within timeout. Idempotent. Called from the sacore-web
//       // SIGTERM path before os.Exit.
//
//     func CriticalSync(module, msg string)
//       // Bypasses the ring. Writes directly to stderr and (if loaded)
//       // the rotating file. Used only when the process is about to
//       // terminate. No kv, no format — keep it trivial and allocation-
//       // free so it survives OOM / stack exhaustion / panic contexts.
//
//     func Drops() uint64
//       // Monotonic count of entries the ring dropped because it was
//       // full at the moment of Enqueue. Mirrored to PM as
//       // oam/pm:LogDrops so operators see drift.
//
//     func RegisterSink(s Sink)
//     func UnregisterSink(s Sink)
//       // Runtime sink management. Called at init-time for the
//       // built-in sinks (console, file, buffer); optional for
//       // stream and OTEL sinks enabled via Configure.
//
// # Internal architecture
//
// File layout (replaces the flat logger.go + handler.go pair):
//
//     oam/logger/
//       logger.go         public API (unchanged surface)
//       entry.go          Entry struct + sync.Pool[*Entry]
//       ring.go           MPMC bounded ring (atomic CAS head/tail)
//       drainer.go        single background goroutine
//       filter.go         level + IMSI filter helpers
//       sink.go           Sink interface + registry
//       sink_console.go   ← content moves from handler.go + tty_*.go
//       sink_file.go      ← wraps today's rotating_file.go
//       sink_buffer.go    ← owns the GUI ring (replaces pushBuffer)
//       sink_stream.go    NEW — WS/SSE live tail broadcaster
//       sink_otel.go      NEW — OTLP batch exporter (defaults off)
//
// Type sketch (NOT a contract — the committed code may refine names):
//
//     type Sink interface {
//         Emit(batch []*Entry)   // called in drainer goroutine only
//         Flush() error          // called from Flush(timeout)
//         Close() error          // called from shutdown
//     }
//
//     type ringBuf struct {
//         // Vyukov MPMC bounded queue — power-of-two cap, CAS on
//         // per-slot seq. No per-call heap allocation; the *Entry
//         // payloads come from sync.Pool.
//         mask uint64
//         slots []slot  // len(slots) == mask+1
//         head atomic.Uint64  // producer cursor
//         tail atomic.Uint64  // consumer (drainer) cursor
//     }
//
//     type slot struct {
//         seq atomic.Uint64
//         e   *Entry
//     }
//
//     // Entry grows two fields vs today to support the kv-deferred
//     // rendering path (I3) — the rest is unchanged so Entry can be
//     // serialized to the web GUI identically.
//     type Entry struct {
//         Seq     int64
//         TS      time.Time
//         TSFmt   string           // filled by drainer, not producer
//         Level   string
//         LevelNo int
//         Module  string
//         IMSI    string
//         Message string           // pre-formatted when Debugf/Infof path
//         KV      []any            // nil when Message was pre-formatted
//     }
//
// Ring capacity: 4096 entries default; exposed as
// infra_config.log_ring_cap (DB column) with hot-reload via
// Configure(). At ~120 bytes per Entry + pool overhead that's
// ~500 KB resident — negligible.
//
// Producer contention: for the current NF call profile (many
// goroutines, moderate per-goroutine rate) a single MPMC ring is
// fine. If profiling shows the head CAS becoming contended, shard
// the ring by runtime.ProcID() (libs/fsm already uses a similar
// per-CPU sharding pattern for its event queues). Defer this until
// benchmarks actually show the hot spot.
//
// # Overflow + observability
//
// Producer pseudocode for Enqueue:
//
//     pos := head.Load()
//     for {
//         slot := &slots[pos & mask]
//         seq := slot.seq.Load()
//         diff := seq - pos
//         switch {
//         case diff == 0:  // slot is empty — claim it
//             if head.CompareAndSwap(pos, pos+1) {
//                 slot.e = entry
//                 slot.seq.Store(pos + 1)
//                 return OK
//             }
//             // lost the CAS race — retry
//         case diff < 0:   // slot still occupied — ring is FULL
//             drops.Add(1)
//             return DROPPED
//         default:         // producer lapped us — retry
//         }
//         pos = head.Load()
//     }
//
// Drop-oldest variant: when the ring is full (diff < 0), advance
// head atomically past the oldest slot, steal it, and succeed.
// Exposes a second counter drops_overwritten so the operator can
// distinguish "dropped THIS entry" vs "overwrote an earlier
// entry to make room for THIS one". Pick one and stick with it —
// mixing them in the same build is confusing. Recommendation:
// drop-oldest-to-make-room is the better default for a log where
// freshness > completeness.
//
// Observability surface:
//
//   - oam/pm:LogDrops        (monotonic counter)
//   - oam/pm:LogRingDepth    (gauge, sampled by drainer each batch)
//   - oam/pm:LogBatchSize    (histogram — p50/p95/p99 batch sizes)
//   - oam/pm:LogSinkLatency  (histogram per sink — file / stream / otel)
//
// A non-zero LogDrops is operator-visible in the web GUI status
// panel ("logs_dropped: 1,284 since boot") and in
// `journalctl -u sacore | grep drops`.
//
// # Graceful shutdown
//
//   Flush(timeout):
//     sentinel := pool.Get()
//     sentinel.Message = "__FLUSH_SENTINEL__"
//     sentinel.KV = []any{"ack", make(chan struct{})}
//     ring.EnqueueOrWait(sentinel)
//     select {
//       case <-sentinel.KV[1].(chan struct{}): return nil
//       case <-time.After(timeout): return ErrFlushTimeout
//     }
//
// Drainer recognises the sentinel by its Message value, closes the
// ack channel before returning the sentinel to the pool, continues
// processing subsequent entries (Flush is not a stop — it's a
// barrier).
//
// sacore-web wires Flush(5*time.Second) into its SIGTERM handler
// before os.Exit.
//
// # CriticalSync
//
//   CriticalSync(module, msg):
//     line := formatPlain(module, msg)   // no pool, no alloc-heavy path
//     os.Stderr.Write(line)
//     if fileHandle != nil {
//         fileHandle.Write(line)          // direct — not through sink_file
//     }
//
// Used only from panic recover handlers, oam/health's OOM notifier,
// and the drainer's own "I am dying" self-report. Not for operational
// logs; every other log site MUST go through the ring.
//
// # Migration plan (3 commits, each green on its own)
//
// Commit A — plumbing, no user-visible change:
//     - Add entry.go, ring.go, drainer.go, sink.go.
//     - Keep the current slog-based chain intact.
//     - Re-implement *Logger.log to enqueue to the ring AND call
//       the existing chain. Two sinks during the transition: the
//       new drainer (dark-launch) and the legacy direct path.
//     - Benchmark oam/logger with go test -bench — capture ns/op
//       for *Logger.Info before and after. Target ≤ 250 ns/op on
//       the hot path (current is ~3-10 µs under load).
//     - Verify no GUI / log file regressions by running the
//       existing smoke test against sacore-web.
//
// Commit B — cut over sinks:
//     - Extract sink_console.go from handler.go (and tty_unix /
//       tty_windows); sink_file.go from rotating_file.go;
//       sink_buffer.go from pushBuffer/GetEntries.
//     - Delete the legacy slog handler chain. Only the drainer path
//       remains.
//     - Bench again; confirm hot-path budget.
//
// Commit C — new capabilities:
//     - sink_stream.go: WS/SSE broadcast. Add /api/logs/tail route
//       in webservice for live tail with Last-Event-ID resume.
//     - sink_otel.go: OTLP log export; wires oam/otel's existing
//       stubs into a real exporter. Off by default; enabled via
//       infra_config.otel_logs_enabled.
//     - Add Flush, CriticalSync, Drops, RegisterSink,
//       UnregisterSink to the public API.
//
// # Performance expectations (rough; confirm with benchmarks in A)
//
//   Today's *Logger.Info on a quiet box    ~1-3 µs/op
//   Today's *Logger.Info during NGAP burst ~5-15 µs/op (I/O tail)
//   After redesign (hot path, quiet)       ~150-250 ns/op
//   After redesign (hot path, at load)     ~150-250 ns/op
//                                          (drainer catches up;
//                                           no back-pressure to
//                                           the producer)
//
// That's a 10-60× speedup on the hot path, and — more importantly —
// the AMF/NAS handler latency tails stop correlating with log rate.
// Drainer sustained throughput sets the ceiling on how many
// entries/sec the build can retain in the file; with a
// 4096-entry ring draining in ~1 ms batches we can retain
// ~4M entries/sec with moderate headroom. Beyond that, drops_total
// climbs and the operator either raises log_ring_cap, lowers
// LOG_LEVEL, or tightens SACORE_LOG_IMSI.
//
// # Risks to call out in review
//
//   - Cross-producer ordering in the file is ring-enqueue order,
//     not wall-clock order. For operational logs this is fine. For
//     strict temporal replay, consumers MUST sort by Entry.Seq —
//     which is already assigned per enqueue and monotonic.
//
//   - Crash-loss window: entries still in the ring when the process
//     dies are lost from disk. Mitigated by Flush on SIGTERM and
//     CriticalSync on panic; accept a bounded loss on SIGKILL /
//     OOM kill. Operators aware of this bound size their
//     log_ring_cap and the drainer's batch cadence accordingly.
//
//   - Drainer-goroutine bug would lose ALL logs silently. The
//     drainer MUST self-report via CriticalSync on exit (panic
//     recovery in the goroutine) so this failure mode is loud.
//
//   - Sinks must not panic. Panics in a sink are caught by the
//     drainer's recover() block, logged via CriticalSync, and the
//     sink is UnregisterSink'd so subsequent batches skip it.
//
//   - IMSI filter updates (SetIMSIFilter / ReloadIMSIFilter) need
//     to remain lock-free on the read side. The current design's
//     atomic.Value swap pattern works; preserve it.
//
// # Out of scope
//
//   - Log-level per-module overrides. Already possible today via
//     per-Logger cached levels; unchanged.
//
//   - Structured logging search / indexing. sink_otel delegates
//     that to the OTel backend; we don't ship a local search index.
//
//   - Log redaction / PII filtering beyond IMSI allow-listing.
//     Out of scope for the logger — caller's responsibility.
//
//   - Dynamic log level from AMF 5GMM traffic classes. Out of
//     scope; operators use SetLevel / LOG_LEVEL.
//
//   - Persistence across rotations. Today a rotation truncates
//     visibility in the file-tail GUI view; the ring-buffer sink
//     continues to hold the last N entries. Preserved.
//
// ──────────────────────────────────────────────────────────────────────

package logger

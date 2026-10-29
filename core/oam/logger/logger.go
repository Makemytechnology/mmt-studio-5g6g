// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package logger — centralized logging for MMT Studio Core (Go port).
//
// Mirrors oam/sacore_logging.py from the Python reference:
//   - IMSI-aware logger: Logger(name).Info(msg, imsi=...)
//   - IMSI filtering via SACORE_LOG_IMSI env var (comma-separated allow-list)
//   - Log-level control via LOG_LEVEL (DEBUG|INFO|WARNING|ERROR)
//   - Multi-sink: console + rotating file + in-memory ring buffer for web UI
//   - Color output on TTY (disable with NO_COLOR=1)
//   - Runtime reconfiguration from DB (ConfigureFromDB)
//
// Usage:
//
//	log := logger.Get("amf.ngap")
//	log.Info("NG Setup complete", "imsi", "001010000000001")
//	log.Debug("Full PDU: %x", pdu)
package logger

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ── Levels ──────────────────────────────────────────────────────────────

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// ── Package state ───────────────────────────────────────────────────────

var (
	initOnce sync.Once
	mu       sync.RWMutex

	// IMSI allow-list (nil = no filter, empty set after parse = no filter)
	imsiFilter map[string]struct{}

	// Sequence counter (monotonic across the process lifetime)
	bufCap    int = 5000
	seq       atomic.Int64
	entryPool sync.Pool

	// Current sink state
	fileHandle *rotatingFile
	filePath   string
	levelVar   = new(slog.LevelVar) // allows SetLevel at runtime

	// New ring + drainer + sink infrastructure (oam/logger/redesign.go)
	logRing    *ringBuf
	logDrainer *drainer
	siConsole  *consoleSink
	siFile     *fileSink
	siBuffer   *bufferSink
	siStream   *streamSink
)

// Entry is a single log line captured in the ring buffer.
type Entry struct {
	Seq     int64     `json:"seq"`
	TS      time.Time `json:"ts"`
	TSFmt   string    `json:"ts_fmt"`
	Level   string    `json:"level"`
	LevelNo int       `json:"level_no"`
	Module  string    `json:"module"`
	IMSI    string    `json:"imsi,omitempty"`
	Message string    `json:"message"`
}

// Config drives the handler stack. Leave zero values for sensible defaults.
type Config struct {
	Level      slog.Level // default INFO
	FilePath   string     // empty = console+buffer only
	Sink       string     // "disk"|"tmpfs"|"ram_only"|"syslog"|"journald"
	BufferSize int        // default 5000
	UseColor   bool       // auto from TTY when initializing
}

// ── Public API ──────────────────────────────────────────────────────────

// Get returns a namespaced logger under the "mmt-core" hierarchy.
// name examples: "amf.ngap", "upf", "ausf".
func Get(name string) *Logger {
	initOnce.Do(initDefault)
	return &Logger{name: "mmt-core." + name}
}

// SetLevel changes the global log level at runtime. Accepts "DEBUG", "INFO",
// "WARNING"/"WARN", "ERROR". Returns false if unknown.
func SetLevel(name string) bool {
	var lvl slog.Level
	switch strings.ToUpper(name) {
	case "DEBUG":
		lvl = LevelDebug
	case "INFO":
		lvl = LevelInfo
	case "WARN", "WARNING":
		lvl = LevelWarn
	case "ERROR":
		lvl = LevelError
	default:
		return false
	}
	levelVar.Set(lvl)
	os.Setenv("LOG_LEVEL", strings.ToUpper(name))
	return true
}

// GetLevel returns the current level name.
func GetLevel() string {
	switch levelVar.Level() {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARNING"
	case LevelError:
		return "ERROR"
	}
	return "INFO"
}

// SetIMSIFilter replaces the IMSI allow-list. Pass nil/empty to clear.
func SetIMSIFilter(imsis []string) {
	mu.Lock()
	defer mu.Unlock()
	if len(imsis) == 0 {
		imsiFilter = nil
		os.Setenv("SACORE_LOG_IMSI", "")
		return
	}
	m := make(map[string]struct{}, len(imsis))
	clean := make([]string, 0, len(imsis))
	for _, s := range imsis {
		s = strings.TrimSpace(s)
		if s != "" {
			m[s] = struct{}{}
			clean = append(clean, s)
		}
	}
	if len(m) == 0 {
		imsiFilter = nil
		os.Setenv("SACORE_LOG_IMSI", "")
		return
	}
	imsiFilter = m
	os.Setenv("SACORE_LOG_IMSI", strings.Join(clean, ","))
}

// GetIMSIFilter returns the current allow-list (empty means no filter).
func GetIMSIFilter() []string {
	mu.RLock()
	defer mu.RUnlock()
	if imsiFilter == nil {
		return nil
	}
	out := make([]string, 0, len(imsiFilter))
	for k := range imsiFilter {
		out = append(out, k)
	}
	return out
}

// ReloadIMSIFilter re-reads SACORE_LOG_IMSI from the environment.
func ReloadIMSIFilter() {
	raw := strings.TrimSpace(os.Getenv("SACORE_LOG_IMSI"))
	mu.Lock()
	defer mu.Unlock()
	imsiFilter = nil
	if raw == "" {
		return
	}
	m := make(map[string]struct{})
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			m[s] = struct{}{}
		}
	}
	if len(m) > 0 {
		imsiFilter = m
	}
}

// SetLogFile enables/replaces the rotating file handler. Pass "" to disable.
func SetLogFile(path string) error {
	mu.Lock()
	defer mu.Unlock()
	if fileHandle != nil {
		_ = fileHandle.Close()
		fileHandle = nil
		filePath = ""
	}
	if path == "" {
		rebuildRootLocked()
		return nil
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	rf, err := newRotatingFile(path, 50*1024*1024, 5)
	if err != nil {
		return err
	}
	fileHandle = rf
	filePath = path
	rebuildRootLocked()
	return nil
}

// GetLogFile returns the current file path (or empty).
func GetLogFile() string {
	mu.RLock()
	defer mu.RUnlock()
	return filePath
}

// Configure (re)installs handlers from a Config. Tears down current state.
func Configure(c Config) error {
	mu.Lock()
	defer mu.Unlock()

	if c.Level == 0 && c.Level != LevelDebug {
		c.Level = LevelInfo
	}
	if c.BufferSize > 0 && c.BufferSize != bufCap {
		bufCap = c.BufferSize
	}
	levelVar.Set(c.Level)

	if fileHandle != nil {
		_ = fileHandle.Close()
		fileHandle = nil
		filePath = ""
	}

	switch c.Sink {
	case "disk":
		path := c.FilePath
		if path == "" {
			path = strings.TrimSpace(os.Getenv("SACORE_LOG_FILE"))
			if path == "" {
				path = "/var/log/sacore/sacore.log"
			}
		}
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		rf, err := newRotatingFile(path, 50*1024*1024, 5)
		if err != nil {
			return err
		}
		fileHandle = rf
		filePath = path
	case "tmpfs":
		path := "/run/log/sacore/sacore.log"
		_ = os.MkdirAll(filepath.Dir(path), 0o755)
		rf, err := newRotatingFile(path, 50*1024*1024, 5)
		if err != nil {
			return err
		}
		fileHandle = rf
		filePath = path
	case "ram_only", "":
		// No file sink.
	default:
		// journald/syslog not implemented in Go port (stdlib has no journald);
		// fall back to console+buffer only.
	}

	rebuildRootLocked()
	return nil
}

// ── Logger wrapper (adds IMSI convenience + module name) ────────────────

// Logger is the user-facing log facade. Methods accept an optional trailing
// (key, value, …) list, where "imsi" is a special key extracted and used for
// filtering / buffer tagging.
type Logger struct {
	name string
	imsi string
}

// WithIMSI returns a child logger tagged with a default IMSI.
func (l *Logger) WithIMSI(imsi string) *Logger {
	cp := *l
	cp.imsi = imsi
	return &cp
}

func (l *Logger) Debug(msg string, kv ...any) { l.log(LevelDebug, msg, kv...) }
func (l *Logger) Info(msg string, kv ...any)  { l.log(LevelInfo, msg, kv...) }
func (l *Logger) Warn(msg string, kv ...any)  { l.log(LevelWarn, msg, kv...) }
func (l *Logger) Error(msg string, kv ...any) { l.log(LevelError, msg, kv...) }

// Debugf/Infof/… preserve printf-style ergonomics of the Python logger.
func (l *Logger) Debugf(format string, a ...any) { l.log(LevelDebug, fmt.Sprintf(format, a...)) }
func (l *Logger) Infof(format string, a ...any)  { l.log(LevelInfo, fmt.Sprintf(format, a...)) }
func (l *Logger) Warnf(format string, a ...any)  { l.log(LevelWarn, fmt.Sprintf(format, a...)) }
func (l *Logger) Errorf(format string, a ...any) { l.log(LevelError, fmt.Sprintf(format, a...)) }

// log is the producer hot path. The redesign (oam/logger/redesign.go)
// budget is ~150-250ns/op: level + IMSI filter, pool-allocate Entry,
// stamp timestamp + fields, single ring Enqueue. No formatting, no
// I/O, no slog handler chain on the producer.
//
// Extra k/v pairs (anything beyond "imsi=...") are appended to the
// message as " k=v" suffixes — same shape the legacy multiHandler
// emitted, so the wire format and operator greps stay byte-identical.
func (l *Logger) log(lvl slog.Level, msg string, kv ...any) {
	// Level check (atomic load on levelVar — no lock).
	if lvl < levelVar.Level() {
		return
	}

	// Extract optional imsi= kv pair, accumulate any extras for the
	// suffix.
	imsi := l.imsi
	var extras []string
	for i := 0; i+1 < len(kv); i += 2 {
		k, _ := kv[i].(string)
		if k == "imsi" {
			if s, ok := kv[i+1].(string); ok {
				imsi = s
			}
			continue
		}
		extras = append(extras, fmt.Sprintf("%s=%v", k, kv[i+1]))
	}
	// IMSI filter (filtered entries don't burn a Seq number).
	if !imsiAllowed(imsi) {
		return
	}

	// Pool-allocate Entry. Stamp non-Seq fields, then EnqueueAssign
	// stamps Seq + inserts atomically under the ring mutex so the
	// drainer always sees Seqs in strictly ascending insertion order
	// — even when N producers race here concurrently.
	e := getEntry()
	e.TS = time.Now()
	e.Level = levelName(lvl)
	e.LevelNo = int(lvl)
	e.Module = l.name
	e.IMSI = imsi
	if len(extras) > 0 {
		e.Message = msg + " " + strings.Join(extras, " ")
	} else {
		e.Message = msg
	}

	if logRing != nil {
		if evicted := logRing.EnqueueAssign(e, &seq); evicted != nil {
			// Recycle the entry we overwrote (drop-oldest path) so
			// nothing leaks. The Drops() counter on logRing was
			// already incremented atomically inside Enqueue.
			putEntry(evicted)
		}
	}
}

// ── GUI ring (delegates to bufferSink populated by the drainer) ─────────

// GetEntries returns buffered entries matching filters.
//
//	afterSeq: only entries with Seq > afterSeq (for polling)
//	level:    minimum level name (empty = all)
//	imsi:     exact IMSI match (empty = all)
//	module:   module prefix after "mmt-core." (empty = all)
//	limit:    max entries (0 = 500 default)
//
// Per oam/logger/redesign.go invariant I5, the GUI ring is populated
// by the drainer goroutine after every batch — values lag the
// producer by at most one drainer tick (default 5 ms). Tests that
// log() and immediately GetEntries() should call Flush() in between.
func GetEntries(afterSeq int64, level, imsi, module string, limit int) []Entry {
	if limit <= 0 {
		limit = 500
	}
	// Drain anything currently in flight so the GUI tail sees the
	// most recent producer entries — a 100 ms barrier is plenty
	// since the drainer pops every 5 ms tick. Test callers that
	// log() and immediately GetEntries() rely on this; production
	// HTTP callers gain crispness at negligible cost.
	if logDrainer != nil {
		_ = logDrainer.flush(100 * time.Millisecond)
	}
	var minLvl slog.Level = LevelDebug - 1 // accept everything when level==""
	switch strings.ToUpper(level) {
	case "DEBUG":
		minLvl = LevelDebug
	case "INFO":
		minLvl = LevelInfo
	case "WARN", "WARNING":
		minLvl = LevelWarn
	case "ERROR":
		minLvl = LevelError
	}
	if siBuffer == nil {
		return nil
	}
	// Snapshot accepts (afterSeq, level, imsi, module, limit). It uses
	// substring match for module — so callers passing "amf" still get
	// "mmt-core.amf.*" entries.
	out := siBuffer.snapshot(afterSeq, "", imsi, "mmt-core."+module, limit*2)
	if level == "" {
		// No level filter — return up to limit.
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	// Level filter: drop anything below the threshold.
	filtered := make([]Entry, 0, limit)
	for _, e := range out {
		if slog.Level(e.LevelNo) < minLvl {
			continue
		}
		filtered = append(filtered, e)
		if len(filtered) >= limit {
			break
		}
	}
	return filtered
}

// ClearBuffer empties the in-memory GUI ring.
func ClearBuffer() {
	if siBuffer != nil {
		siBuffer.clear()
	}
}

// Drops returns the monotonic count of entries the ring dropped due
// to overflow (drop-oldest policy). Operators see this in the GUI
// status pane and via `journalctl -u sacore | grep drops`.
func Drops() uint64 {
	if logRing == nil {
		return 0
	}
	return logRing.Drops()
}

// Flush blocks until the drainer has processed every entry currently
// in the ring AND every registered sink has Flush()'d. Used from the
// SIGTERM handler so no log line is lost on shutdown.
func Flush(timeout time.Duration) error {
	if logDrainer == nil {
		return nil
	}
	return logDrainer.flush(timeout)
}

// CriticalSync writes a last-gasp message directly to stderr (and the
// rotating file if loaded), bypassing the ring entirely. Used only
// from panic recovery / OOM notifier / drainer-died self-report. No
// formatting beyond the single fmt.Fprintf — the goal is to survive
// a stack-exhausted / OOM-killed context.
func CriticalSync(module, msg string) {
	now := time.Now()
	line := fmt.Sprintf("%s:%03d #00000000 CRIT  [%s] %s\n",
		now.Format("2006-01-02 15:04:05"), now.Nanosecond()/1_000_000,
		module, msg)
	_, _ = os.Stderr.WriteString(line)
	if fileHandle != nil {
		_, _ = fileHandle.Write([]byte(line))
	}
}

// RegisterSink adds a sink to the drainer's fan-out list. Used by
// tests and (later) sink_stream / sink_otel. Built-in sinks
// (console / file / buffer) register at init time.
func RegisterSink(s Sink) {
	if logDrainer != nil && s != nil {
		logDrainer.registerSink(s)
	}
}

// UnregisterSink removes a sink. Idempotent.
func UnregisterSink(s Sink) {
	if logDrainer != nil && s != nil {
		logDrainer.unregisterSink(s)
	}
}

// ── Internal ────────────────────────────────────────────────────────────

func initDefault() {
	entryPool = sync.Pool{New: func() any { return new(Entry) }}

	lvl := parseLevelEnv()
	levelVar.Set(lvl)

	// Parse IMSI filter from env
	ReloadIMSIFilter()

	// Default: console + GUI ring. File only if SACORE_LOG_FILE set.
	filePath = strings.TrimSpace(os.Getenv("SACORE_LOG_FILE"))
	if filePath != "" {
		if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err == nil {
			if rf, err := newRotatingFile(filePath, 50*1024*1024, 5); err == nil {
				fileHandle = rf
			} else {
				filePath = ""
			}
		} else {
			filePath = ""
		}
	}

	// Build the ring + drainer + the three built-in sinks. After this
	// returns, every (*Logger).log enqueue dispatches through the
	// drainer to console + file (if loaded) + GUI buffer.
	rebuildRootLocked()
}

// rebuildRootLocked rebuilds the ring + drainer + sink stack. Caller
// must hold mu (or be inside init). Idempotent — safe to call from
// SetLogFile / Configure to swap the file sink.
func rebuildRootLocked() {
	// Stop any prior drainer cleanly so its sinks Close() before we
	// build new ones.
	if logDrainer != nil {
		logDrainer.stop(2 * time.Second)
	}

	logRing = newRingBuf(4096)
	logDrainer = newDrainer(logRing)

	// SACORE_LOG_JSON=1 swaps the human-readable text format for one
	// JSON object per line — ELK / Loki / Splunk pipelines parse it
	// without a custom grok pattern. Schema in sink_json.go. The text
	// console + colour stays the default because it's what humans
	// read on a tty.
	if os.Getenv("SACORE_LOG_JSON") == "1" {
		// Register a JSON sink for stdout AND the file (when loaded);
		// otherwise the file would still be plain text and the env
		// var's intent ("everything as JSON") would be half-applied.
		logDrainer.registerSink(newJSONSink(os.Stdout))
		if fileHandle != nil {
			logDrainer.registerSink(newJSONSink(fileHandle))
			siFile = nil // file wired directly through jsonSink, not fileSink
		}
	} else {
		useColor := isTTY(os.Stdout) && os.Getenv("NO_COLOR") == ""
		siConsole = newConsoleSink("console", os.Stdout, useColor)
		logDrainer.registerSink(siConsole)

		if fileHandle != nil {
			siFile = newFileSink(fileHandle)
			logDrainer.registerSink(siFile)
		} else {
			siFile = nil
		}
	}

	siBuffer = newBufferSink(bufCap)
	logDrainer.registerSink(siBuffer)

	// streamSink is always registered; SubscribeStream is the public
	// fan-out hub for the webservice /api/logs/tail SSE feed and any
	// other live consumer. With zero subscribers, Emit returns
	// immediately (no per-entry work).
	siStream = newStreamSink()
	logDrainer.registerSink(siStream)

	logDrainer.start()
}

func imsiAllowed(imsi string) bool {
	if imsi == "" {
		return true // infrastructure logs always pass
	}
	mu.RLock()
	defer mu.RUnlock()
	if imsiFilter == nil {
		return true
	}
	_, ok := imsiFilter[imsi]
	return ok
}

func parseLevelEnv() slog.Level {
	switch strings.ToUpper(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "DEBUG":
		return LevelDebug
	case "WARN", "WARNING":
		return LevelWarn
	case "ERROR":
		return LevelError
	default:
		return LevelInfo
	}
}

func levelName(l slog.Level) string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARNING"
	case LevelError:
		return "ERROR"
	}
	return l.String()
}

// isTTY is overridden per-OS (see logger_unix.go / logger_windows.go).
var isTTY = func(f *os.File) bool { return false }

// rotatingFile satisfies io.Writer; the assertion lived here to keep
// the legacy slog Handler chain happy. The drainer-based pipeline
// doesn't need it but the type still implements Write so leaving the
// assertion off is fine.

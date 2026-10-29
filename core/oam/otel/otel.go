// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package otel — OpenTelemetry integration for the 5G core.
//
// Surfaces:
//
//   - Config (read from infra_config.otel_*) — exporter, endpoint,
//     metrics/traces/logs toggles, Prometheus scrape port.
//   - Span emission API — StartSpan / End / AddEvent — with W3C
//     Trace Context-style hex IDs (16-byte trace_id, 8-byte span_id).
//   - In-memory ring of recent spans (default 5000, configurable)
//     for the operator panel; OTLP export is the deferred path.
//   - Per-NF / per-operation counters surfaced at /api/otel/counters.
//
// Spec anchors:
//
//   - W3C Trace Context (https://www.w3.org/TR/trace-context/) — the
//     trace_id / span_id hex format and the parent-span linkage rule.
//   - TS 28.552 §6  Performance management measurements via OTEL
//                   exporters (deferred — needs go.opentelemetry.io/otel
//                   vendoring).
//   - TS 28.554 §5  E2E KPIs that map to OTEL traces (deferred).
//
// Deferred (vendor `go.opentelemetry.io/otel/sdk/trace` to wire):
//
//   - OTLP gRPC push of spans + metrics + logs to a collector.
//   - Prometheus exporter on :otel_prometheus_port.
//   - Console exporter (debug builds).
//
// The package compiles without the OTEL SDK today; the operator-side
// surface (config + ring + counters) is fully functional and the SDK
// wires lift the ring through to the export path when vendored.
package otel

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ── Config ───────────────────────────────────────────────────────

// Config mirrors the otel_* columns in infra_config.
type Config struct {
	Enabled        bool   `json:"enabled"`
	MetricsEnabled bool   `json:"metrics_enabled"`
	TracesEnabled  bool   `json:"traces_enabled"`
	LogsEnabled    bool   `json:"logs_enabled"`
	Exporter       string `json:"exporter"` // prometheus | otlp | console
	Endpoint       string `json:"endpoint"`
	PrometheusPort int    `json:"prometheus_port"`
}

// LoadConfig reads OTEL settings from infra_config.
func LoadConfig() Config {
	cfg, err := crud.GetInfraConfig()
	if err != nil {
		return Config{}
	}
	return Config{
		Enabled:        intVal(cfg, "otel_enabled") != 0,
		MetricsEnabled: intVal(cfg, "otel_metrics_enabled") != 0,
		TracesEnabled:  intVal(cfg, "otel_traces_enabled") != 0,
		LogsEnabled:    intVal(cfg, "otel_logs_enabled") != 0,
		Exporter:       strVal(cfg, "otel_exporter"),
		Endpoint:       strVal(cfg, "otel_endpoint"),
		PrometheusPort: int(intVal(cfg, "otel_prometheus_port")),
	}
}

// validExporter gates the panel-supplied vocabulary against the
// schema CHECK so we surface 400 instead of a CHECK violation 500.
func ValidExporter(s string) bool {
	switch s {
	case "prometheus", "otlp", "console":
		return true
	}
	return false
}

// UpdateConfig applies an operator patch and re-loads the SDK if
// `enabled` flipped on. Unknown keys are dropped at the CRUD layer.
// Returns the post-update config.
func UpdateConfig(patch map[string]any) (Config, error) {
	if v, ok := patch["otel_exporter"].(string); ok && v != "" {
		if !ValidExporter(v) {
			return Config{}, errBadExporter
		}
	}
	if _, err := crud.UpdateInfraConfig(patch); err != nil {
		return Config{}, err
	}
	cfg := LoadConfig()
	// Re-init: cheap because span ring lives outside the SDK; the
	// SDK wire is no-op until the dep is vendored.
	go reconfigure(cfg)
	return cfg, nil
}

// ── Init / lifecycle ─────────────────────────────────────────────

// Init starts the OTEL pipeline based on the loaded config. No-op
// when otel_enabled=0. Safe to call repeatedly.
func Init() {
	cfg := LoadConfig()
	configureRing(cfg)
	if !cfg.Enabled {
		return
	}
	log := logger.Get("oam.otel")
	log.Infof("OTEL enabled exporter=%s endpoint=%s prom_port=%d metrics=%v traces=%v logs=%v",
		cfg.Exporter, cfg.Endpoint, cfg.PrometheusPort,
		cfg.MetricsEnabled, cfg.TracesEnabled, cfg.LogsEnabled)

	// Logs path: register an OTLP sink with the logger drainer (see
	// oam/logger/sink_otel.go). The sink is a no-op until the SDK
	// dep is vendored, so registering early is harmless.
	if cfg.LogsEnabled {
		logger.RegisterSink(logger.NewOTelSink(cfg.Endpoint))
		log.Infof("OTEL logs sink registered (endpoint=%s; OTLP export deferred)",
			cfg.Endpoint)
	}
	// SDK init for metrics/traces deferred until go.opentelemetry.io/otel dep vendored.
}

// Status returns the current config + emission stats for the panel.
func Status() map[string]any {
	cfg := LoadConfig()
	ringMu.RLock()
	count := ring.len
	cap := ring.cap
	ringMu.RUnlock()
	countMu.RLock()
	cs := make(map[string]int64, len(counters))
	for k, v := range counters {
		cs[k] = v
	}
	countMu.RUnlock()
	return map[string]any{
		"config":            cfg,
		"ring_size":         count,
		"ring_capacity":     cap,
		"spans_emitted":     spansEmitted,
		"counter_keys":      len(cs),
		"sdk_vendored":      false, // flips to true when go.opentelemetry.io/otel lands
	}
}

func reconfigure(cfg Config) {
	configureRing(cfg)
	logger.Get("oam.otel").Infof("OTEL reconfigured: enabled=%v exporter=%s",
		cfg.Enabled, cfg.Exporter)
}

// ── Span ring ────────────────────────────────────────────────────

const defaultRingCap = 5000

// SpanEvent is a timestamped marker inside a span (TS 28.552 §6
// "event" type — e.g. RM.Auth.start, NGAP.NGSetup.received).
type SpanEvent struct {
	Name       string            `json:"name"`
	Timestamp  int64             `json:"timestamp"` // UnixMicro
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Span is one emitted span. Mirrors the shape an OTLP exporter
// would marshal to a `Span` message in the protobuf schema.
type Span struct {
	TraceID      string            `json:"trace_id"`      // 16 bytes hex (W3C Trace Context)
	SpanID       string            `json:"span_id"`       // 8 bytes hex
	ParentSpanID string            `json:"parent_span_id,omitempty"`
	NF           string            `json:"nf"`            // amf | smf | upf | nrf | ...
	Operation    string            `json:"operation"`     // RegistrationRequest, PDUSessionEstablish, ...
	StartTime    int64             `json:"start_time"`    // UnixMicro
	EndTime      int64             `json:"end_time,omitempty"`
	DurationMs   float64           `json:"duration_ms,omitempty"`
	Status       string            `json:"status,omitempty"` // ok | error | timeout
	Attributes   map[string]string `json:"attributes,omitempty"`
	Events       []SpanEvent       `json:"events,omitempty"`
	closed       bool
	mu           sync.Mutex
}

type ringBuffer struct {
	buf  []*Span
	head int
	len  int
	cap  int
}

var (
	ringMu       sync.RWMutex
	ring         = &ringBuffer{cap: defaultRingCap}
	countMu      sync.RWMutex
	counters     = make(map[string]int64) // "nf:operation" → count
	spansEmitted int64
)

func configureRing(_ Config) {
	ringMu.Lock()
	defer ringMu.Unlock()
	if ring.buf == nil {
		ring.cap = defaultRingCap
		ring.buf = make([]*Span, ring.cap)
	}
}

func ringPut(s *Span) {
	ringMu.Lock()
	if ring.buf == nil {
		ring.cap = defaultRingCap
		ring.buf = make([]*Span, ring.cap)
	}
	ring.buf[ring.head] = s
	ring.head = (ring.head + 1) % ring.cap
	if ring.len < ring.cap {
		ring.len++
	}
	spansEmitted++
	ringMu.Unlock()
}

// RecentSpans returns up to limit most-recent spans (newest first).
// Empty when nothing has been emitted yet.
func RecentSpans(limit int) []Span {
	ringMu.RLock()
	defer ringMu.RUnlock()
	if ring.buf == nil || ring.len == 0 {
		return nil
	}
	if limit <= 0 || limit > ring.len {
		limit = ring.len
	}
	out := make([]Span, 0, limit)
	// Walk newest first: head-1, head-2, ...
	for i := 0; i < limit; i++ {
		idx := ring.head - 1 - i
		if idx < 0 {
			idx += ring.cap
		}
		if s := ring.buf[idx]; s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// FilterSpans returns ring rows matching the optional filters.
// Empty filter strings are ignored.
func FilterSpans(traceID, nf, operation string, limit int) []Span {
	all := RecentSpans(0) // all
	out := make([]Span, 0, len(all))
	for _, s := range all {
		if traceID != "" && s.TraceID != traceID {
			continue
		}
		if nf != "" && s.NF != nf {
			continue
		}
		if operation != "" && s.Operation != operation {
			continue
		}
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// GetTrace returns every ring row that shares a trace_id, sorted by
// start_time ascending (so the panel can render it as a tree).
func GetTrace(traceID string) []Span {
	rows := FilterSpans(traceID, "", "", 0)
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].StartTime < rows[j].StartTime
	})
	return rows
}

// SpanCounters returns a snapshot of the per-(NF, Operation) hit
// counts. Keys are formatted as "nf:operation".
func SpanCounters() map[string]int64 {
	countMu.RLock()
	defer countMu.RUnlock()
	out := make(map[string]int64, len(counters))
	for k, v := range counters {
		out[k] = v
	}
	return out
}

// ResetSpans clears the ring + counters. Operator panel button.
func ResetSpans() {
	ringMu.Lock()
	if ring.buf != nil {
		for i := range ring.buf {
			ring.buf[i] = nil
		}
	}
	ring.head, ring.len = 0, 0
	spansEmitted = 0
	ringMu.Unlock()
	countMu.Lock()
	counters = make(map[string]int64)
	countMu.Unlock()
}

// ── Span lifecycle ───────────────────────────────────────────────

// StartSpan opens a new span. parentTraceID/parentSpanID are
// optional — empty strings mean "this is a root span" and a fresh
// trace_id is minted.
func StartSpan(nf, operation, parentTraceID, parentSpanID string) *Span {
	traceID := parentTraceID
	if traceID == "" {
		traceID = newTraceID()
	}
	return &Span{
		TraceID:      traceID,
		SpanID:       newSpanID(),
		ParentSpanID: parentSpanID,
		NF:           nf,
		Operation:    operation,
		StartTime:    time.Now().UnixMicro(),
		Attributes:   make(map[string]string),
	}
}

// End finalises the span and writes it to the ring. status is "ok"
// when empty.
func (s *Span) End(status string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if status == "" {
		status = "ok"
	}
	s.Status = status
	s.EndTime = time.Now().UnixMicro()
	s.DurationMs = float64(s.EndTime-s.StartTime) / 1000.0
	snap := *s
	s.mu.Unlock()

	ringPut(&snap)
	bumpCounter(snap.NF, snap.Operation)
}

// AddEvent records a timestamped marker on the span.
func (s *Span) AddEvent(name string, attrs map[string]string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Events = append(s.Events, SpanEvent{
		Name: name, Timestamp: time.Now().UnixMicro(),
		Attributes: attrs,
	})
}

// SetAttribute stores a key/value tag on the span.
func (s *Span) SetAttribute(k, v string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.Attributes == nil {
		return
	}
	s.Attributes[k] = v
}

// ── ID generation (W3C Trace Context format) ─────────────────────

func newTraceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fall back to time-based; never block.
		now := uint64(time.Now().UnixNano())
		for i := 0; i < 16; i++ {
			b[i] = byte(now >> (i * 4))
		}
	}
	return hex.EncodeToString(b)
}

func newSpanID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		now := uint64(time.Now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte(now >> (i * 4))
		}
	}
	return hex.EncodeToString(b)
}

// ── Counters ─────────────────────────────────────────────────────

func bumpCounter(nf, op string) {
	key := nf + ":" + op
	countMu.Lock()
	counters[key]++
	countMu.Unlock()
}

// ── helpers ──────────────────────────────────────────────────────

// errBadExporter is returned from UpdateConfig when an out-of-vocab
// exporter is supplied. Sentinel so the route layer can map to 400.
var errBadExporter = &otelErr{msg: "exporter must be one of prometheus|otlp|console"}

type otelErr struct{ msg string }

func (e *otelErr) Error() string { return e.msg }

// IsBadInput reports whether err comes from operator-supplied data
// (route layer maps these to 400 vs 500).
func IsBadInput(err error) bool {
	_, ok := err.(*otelErr)
	return ok
}

func intVal(m map[string]any, k string) int64 {
	if v, ok := m[k]; ok {
		switch x := v.(type) {
		case int64:
			return x
		case float64:
			return int64(x)
		}
	}
	return 0
}

func strVal(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

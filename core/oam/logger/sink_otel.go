// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"sync"
	"sync/atomic"
)

// otelSink is the placeholder OTLP log exporter sink. The full
// go.opentelemetry.io/otel/sdk wiring lives in oam/otel and is
// deferred until that dep is vendored — see oam/otel/otel.go:54
// "SDK init deferred until go.opentelemetry.io/otel dep vendored."
//
// Until then, this sink is a no-op buffer with a counter so the
// drainer can register it without fault and operators see "OTEL
// sink saw N entries" via the future status panel. When the SDK
// lands, the body of Emit becomes a single batch.LogRecord build
// + exporter.Export call; nothing in the producer or drainer side
// changes.
//
// Callers who want OTLP today can register a custom Sink via
// RegisterSink — this stub is just the canonical name.
type otelSink struct {
	mu       sync.Mutex
	seen     atomic.Uint64
	endpoint string
}

func newOTelSink(endpoint string) *otelSink {
	return &otelSink{endpoint: endpoint}
}

// NewOTelSink is the public constructor used by oam/otel.Init() once
// the OTLP exporter is configured. Importing oam/otel from logger
// would create a cycle (oam/otel logs via logger.Get); the
// RegisterSink call goes the other way: oam/otel imports logger,
// constructs the sink, and registers it.
func NewOTelSink(endpoint string) Sink {
	return newOTelSink(endpoint)
}

func (s *otelSink) Name() string { return "otel" }

func (s *otelSink) Emit(batch []*Entry) {
	// The producer-side stamp is all that matters today — every Entry
	// flows through here and bumps the seen counter so we can answer
	// "is this sink getting fed?" without a real OTLP roundtrip.
	if len(batch) == 0 {
		return
	}
	s.seen.Add(uint64(len(batch)))
	// TODO(arch: oam/otel SDK landed): convert each *Entry into a
	//   sdk/log/Record (Body=Message, Severity from Level, attrs for
	//   module+imsi+seq) and ship via OTLP gRPC to s.endpoint.
}

func (s *otelSink) Flush() error { return nil }
func (s *otelSink) Close() error { return nil }

// Seen returns the running count of Entries this sink has accepted.
// Wired into the OTEL status panel later.
func (s *otelSink) Seen() uint64 { return s.seen.Load() }

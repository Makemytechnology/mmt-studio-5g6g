// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

// Sink is a destination the drainer fans entries out to. Each sink runs
// in the drainer goroutine, never on the producer's. Implementations
// MUST NOT panic — the drainer's defer/recover catches panics and
// auto-unregisters the offending sink to keep the pipeline alive
// (see drainer.go).
//
// See oam/logger/redesign.go for the full contract.
type Sink interface {
	// Name identifies the sink for the auto-unregister log line and
	// for RegisterSink / UnregisterSink lookups.
	Name() string

	// Emit processes a batch of entries. The slice and the *Entry
	// pointers are owned by the drainer — the sink MUST NOT retain
	// them past the call (the drainer recycles into sync.Pool right
	// after Emit returns).
	Emit(batch []*Entry)

	// Flush is called from the public Flush(timeout) barrier — the
	// sink should block until any internally-buffered entries have
	// reached their final destination (file fsync, network write,
	// etc.).
	Flush() error

	// Close is called on graceful shutdown after the drainer has
	// drained the ring.
	Close() error
}

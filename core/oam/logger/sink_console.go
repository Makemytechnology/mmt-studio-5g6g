// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"io"
	"strings"
	"sync"
)

// consoleSink writes formatted log lines to a tty/stdout/stderr
// writer with optional ANSI colouring on WARNING/ERROR. Format
// matches the legacy multiHandler exactly:
//
//	YYYY-MM-DD HH:MM:SS:mmm #SEQ LEVEL [module][IMSI:xxx] msg
//
// The byte-identical output preserves any operator pipeline that
// already greps the file (and the new awk-by-column gap detection
// added in ddf8a80).
type consoleSink struct {
	mu     sync.Mutex
	w      io.Writer
	colour bool // emit ANSI codes for WARN/ERROR/CRITICAL
	name   string
}

func newConsoleSink(name string, w io.Writer, colour bool) *consoleSink {
	return &consoleSink{w: w, colour: colour, name: name}
}

func (s *consoleSink) Name() string { return s.name }

func (s *consoleSink) Emit(batch []*Entry) {
	if s.w == nil || len(batch) == 0 {
		return
	}
	// Build all lines first so the Write call is one shot per batch
	// (kernel scheduler stays predictable; tail -f sees consistent
	// chunks).
	var b strings.Builder
	b.Grow(96 * len(batch))
	for _, e := range batch {
		formatLine(&b, e, s.colour)
	}
	s.mu.Lock()
	_, _ = io.WriteString(s.w, b.String())
	s.mu.Unlock()
}

func (s *consoleSink) Flush() error { return nil }
func (s *consoleSink) Close() error { return nil }

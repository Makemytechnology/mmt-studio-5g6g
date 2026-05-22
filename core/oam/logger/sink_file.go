// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"strings"
	"sync"
)

// fileSink writes formatted log lines to a rotatingFile (existing
// rotating writer in rotating_file.go). Same format as consoleSink
// minus the ANSI codes — operators tailing the file with `less` or
// `journalctl` see clean text.
type fileSink struct {
	mu sync.Mutex
	rf *rotatingFile
}

func newFileSink(rf *rotatingFile) *fileSink {
	return &fileSink{rf: rf}
}

func (s *fileSink) Name() string { return "file" }

func (s *fileSink) Emit(batch []*Entry) {
	if s.rf == nil || len(batch) == 0 {
		return
	}
	var b strings.Builder
	b.Grow(96 * len(batch))
	for _, e := range batch {
		formatLine(&b, e, false /* no colour */)
	}
	// rotatingFile.Write already takes its own mutex; this one is
	// redundant but cheap and protects the rare case where rf is
	// swapped under us by SetLogFile.
	s.mu.Lock()
	rf := s.rf
	s.mu.Unlock()
	if rf == nil {
		return
	}
	_, _ = rf.Write([]byte(b.String()))
}

func (s *fileSink) Flush() error {
	// rotatingFile already calls Sync() per Write; no buffered state.
	return nil
}

func (s *fileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rf == nil {
		return nil
	}
	rf := s.rf
	s.rf = nil
	return rf.Close()
}

// swap is used by SetLogFile to retarget the file sink to a new
// rotatingFile without unregistering/re-registering. Closes the old
// rotatingFile.
func (s *fileSink) swap(rf *rotatingFile) {
	s.mu.Lock()
	old := s.rf
	s.rf = rf
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

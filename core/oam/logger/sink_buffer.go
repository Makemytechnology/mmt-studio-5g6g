// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"container/list"
	"fmt"
	"sync"
)

// bufferSink owns the in-memory ring that the web GUI's tail view
// reads via GetEntries. Was previously inlined as pushBuffer in
// logger.go; relocated here so it goes through the drainer like
// every other sink (per redesign.go invariant I5: GUI tail lags by
// at most one batch — typically sub-millisecond).
//
// The drainer COPIES each entry into the buffer because the producer-
// owned *Entry returns to sync.Pool after every sink Emit. We keep
// the GUI Entries as their own non-pooled copies so GetEntries
// returns stable values.
type bufferSink struct {
	mu     sync.Mutex
	buf    *list.List
	cap    int
}

func newBufferSink(capacity int) *bufferSink {
	if capacity <= 0 {
		capacity = 5000
	}
	return &bufferSink{
		buf: list.New(),
		cap: capacity,
	}
}

func (s *bufferSink) Name() string { return "gui-buffer" }

func (s *bufferSink) Emit(batch []*Entry) {
	if len(batch) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range batch {
		// Copy fields — *e returns to the pool after the drainer
		// finishes the batch, so a pointer here would dangle.
		// TSFmt is rendered here (not on the hot path) because only
		// the GUI / SSE consumers read it; console/file sinks format
		// themselves via formatLine. Format matches formatLine's
		// canonical "YYYY-MM-DD HH:MM:SS:mmm" so the GUI TIME column
		// reads the same string a tail-on-disk would.
		ts := e.TS.Format("2006-01-02 15:04:05")
		ms := e.TS.Nanosecond() / 1_000_000
		copy := &Entry{
			Seq:     e.Seq,
			TS:      e.TS,
			TSFmt:   fmt.Sprintf("%s:%03d", ts, ms),
			Level:   e.Level,
			LevelNo: e.LevelNo,
			Module:  e.Module,
			IMSI:    e.IMSI,
			Message: e.Message,
		}
		s.buf.PushBack(copy)
		for s.buf.Len() > s.cap {
			s.buf.Remove(s.buf.Front())
		}
	}
}

func (s *bufferSink) Flush() error { return nil }

func (s *bufferSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = list.New()
	return nil
}

// snapshot returns entries newer than afterSeq, optionally filtered
// by level / IMSI substring / module substring. Same shape as the
// public GetEntries — the public func delegates here.
func (s *bufferSink) snapshot(afterSeq int64, level, imsi, module string, limit int) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, 0, 64)
	for el := s.buf.Front(); el != nil; el = el.Next() {
		e := el.Value.(*Entry)
		if e.Seq <= afterSeq {
			continue
		}
		if level != "" && e.Level != level {
			continue
		}
		if imsi != "" && e.IMSI != imsi {
			continue
		}
		if module != "" && !containsIgnoreCase(e.Module, module) {
			continue
		}
		out = append(out, *e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *bufferSink) clear() {
	s.mu.Lock()
	s.buf = list.New()
	s.mu.Unlock()
}

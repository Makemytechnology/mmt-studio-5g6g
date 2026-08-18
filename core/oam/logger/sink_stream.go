// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// streamSink is a fan-out hub for live log subscribers. The webservice
// /api/logs/tail SSE route calls Subscribe to receive a per-client
// channel of *Entry; the drainer's batch arrives here and is broadcast
// non-blocking (drop-on-full per subscriber) so a slow HTTP client
// can't backpressure the drainer.
//
// Each subscriber owns one buffered channel (capacity = streamChanCap).
// If a subscriber's channel is full at broadcast time we increment its
// dropped counter and continue — slow consumers don't get a re-tail of
// missed entries; they reconnect with afterSeq= for a re-fetch.
type streamSink struct {
	mu          sync.RWMutex
	subscribers map[*Subscription]struct{}

	// Module-level singleton so RegisterSink/UnregisterSink stays a
	// no-op after the first init. Set by ensureStreamSink (idempotent).
}

// streamChanCap is the per-subscriber buffer size. ~512 entries is
// roughly 1.5 seconds of headroom at the observed 5k logs/sec peak —
// plenty for a healthy SSE client to keep up.
const streamChanCap = 512

// Subscription is what callers (the SSE handler) hold onto. C is the
// non-blocking channel of *Entry (snapshots; safe to read across
// goroutines because the drainer copies into a fresh allocation per
// fan-out). Dropped is bumped when a broadcast finds C full.
type Subscription struct {
	C       chan *Entry
	dropped atomic.Uint64
	parent  *streamSink
}

// Dropped returns how many entries the subscriber missed because its
// channel was full at broadcast. SSE clients can show "you fell N
// entries behind" in their UI.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close unsubscribes and drains the channel. Safe to call multiple
// times.
func (s *Subscription) Close() {
	if s == nil || s.parent == nil {
		return
	}
	s.parent.unsubscribe(s)
}

func newStreamSink() *streamSink {
	return &streamSink{
		subscribers: make(map[*Subscription]struct{}),
	}
}

func (s *streamSink) Name() string { return "stream" }

func (s *streamSink) Emit(batch []*Entry) {
	if len(batch) == 0 {
		return
	}
	// Snapshot the subscriber list under the read lock so a
	// concurrent Subscribe/Unsubscribe doesn't reshape the map mid-
	// loop. The pointer values themselves are stable.
	s.mu.RLock()
	subs := make([]*Subscription, 0, len(s.subscribers))
	for sub := range s.subscribers {
		subs = append(subs, sub)
	}
	s.mu.RUnlock()
	if len(subs) == 0 {
		return
	}
	// Each subscriber gets a COPY because the *Entry returns to the
	// pool after the drainer finishes the batch. A shared pointer
	// would dangle once recycled. TSFmt is stamped here (not on the
	// hot path) so SSE consumers read the same canonical string the
	// /api/logger/entries poll path returns.
	for _, e := range batch {
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
		for _, sub := range subs {
			select {
			case sub.C <- copy:
			default:
				sub.dropped.Add(1)
			}
		}
	}
}

func (s *streamSink) Flush() error { return nil }
func (s *streamSink) Close() error {
	s.mu.Lock()
	for sub := range s.subscribers {
		close(sub.C)
		delete(s.subscribers, sub)
	}
	s.mu.Unlock()
	return nil
}

func (s *streamSink) subscribe() *Subscription {
	sub := &Subscription{
		C:      make(chan *Entry, streamChanCap),
		parent: s,
	}
	s.mu.Lock()
	s.subscribers[sub] = struct{}{}
	s.mu.Unlock()
	return sub
}

func (s *streamSink) unsubscribe(sub *Subscription) {
	s.mu.Lock()
	if _, ok := s.subscribers[sub]; ok {
		delete(s.subscribers, sub)
		close(sub.C)
	}
	s.mu.Unlock()
}

// ── Public API ──────────────────────────────────────────────────────────

// SubscribeStream returns a per-caller live-tail subscription. Caller
// reads *Entry off Subscription.C and must call Sub.Close() when done
// to release the slot.
//
// Used by the webservice /api/logs/tail SSE route. Non-webservice
// callers (tests, OAM tooling) can also subscribe.
func SubscribeStream() *Subscription {
	// Trigger initDefault if it hasn't fired yet — otherwise a caller
	// who Subscribes BEFORE the first Get(name) lands on a nil
	// siStream and silently never receives anything.
	initOnce.Do(initDefault)
	if siStream == nil {
		return &Subscription{C: make(chan *Entry)}
	}
	return siStream.subscribe()
}

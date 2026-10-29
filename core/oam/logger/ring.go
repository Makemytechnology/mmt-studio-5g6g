// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"sync"
	"sync/atomic"
)

// ringBuf is a bounded MPMC queue of *Entry. Producers Enqueue from
// any goroutine; the single drainer goroutine PopBatch's. Overflow
// policy: drop-OLDEST-to-make-room (per redesign.go invariant I4) so
// the most recent entries always survive — that's the operationally
// useful failure mode for a log under burst load.
//
// Implementation: mutex-guarded slice. The redesign doc anticipated a
// Vyukov-style lock-free MPMC queue; benchmarking didn't show
// contention worth the complexity at the current AMF call rate
// (~5k logs/sec peak observed in sacore.log). Swap to lock-free if
// future profiles say otherwise — the public surface here is stable.
type ringBuf struct {
	mu   sync.Mutex
	buf  []*Entry
	cap  int
	head int   // next write index
	tail int   // next read index
	size int
	drops      atomic.Uint64 // entries we dropped because the ring was full at enqueue
	overwrites atomic.Uint64 // entries we OVERWROTE (drop-oldest path)
}

func newRingBuf(capacity int) *ringBuf {
	if capacity <= 0 {
		capacity = 4096
	}
	return &ringBuf{
		buf: make([]*Entry, capacity),
		cap: capacity,
	}
}

// Enqueue stores e in the ring with its Seq pre-stamped by the caller.
// If the ring is full, the OLDEST entry is overwritten and returned to
// the caller for pool-recycling so nothing leaks. Returns nil when
// there was room (no eviction).
//
// Used by tests that need deterministic Seq values for assertions.
// The production hot-path (logger.log) goes through EnqueueAssign,
// which makes seq allocation + insertion atomic so the drainer sees
// Seqs in strictly ascending order under concurrent producers.
//
// The dropped/overwritten counter is bumped atomically so operators
// can observe loss via Drops().
func (r *ringBuf) Enqueue(e *Entry) (evicted *Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enqueueLocked(e)
}

// EnqueueAssign stamps e.Seq from seqCtr.Add(1) AND inserts e into the
// ring under a single critical section. Without this, a producer could
// be preempted between e.Seq = seq.Add(1) (one atomic CAS) and
// ring.Enqueue (a separate mutex acquire) — a faster producer would
// take a higher seq AND insert first, leaving the ring with seqs out
// of insertion order. Drop detection still works (seqs are unique
// monotone-allocated) but the on-disk log file no longer reads in
// chronological/causal order, which is bad for human debugging.
//
// Holding the ring mutex across both steps serialises every producer
// at one point: seq allocation and ring insertion happen in the same
// order. Cost is negligible — the critical section is already held
// for the ring write; we just stamp one extra int64 inside it.
func (r *ringBuf) EnqueueAssign(e *Entry, seqCtr *atomic.Int64) (evicted *Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Seq = seqCtr.Add(1)
	return r.enqueueLocked(e)
}

// enqueueLocked is the inner ring-write — caller must hold r.mu.
func (r *ringBuf) enqueueLocked(e *Entry) (evicted *Entry) {
	if r.size == r.cap {
		// Drop-oldest: evict the entry at tail, advance tail, write at head.
		evicted = r.buf[r.tail]
		r.tail = (r.tail + 1) % r.cap
		r.size--
		r.overwrites.Add(1)
	}
	r.buf[r.head] = e
	r.head = (r.head + 1) % r.cap
	r.size++
	return evicted
}

// PopBatch drains up to max entries into the provided dst slice
// (reused across calls to avoid alloc). Returns dst with appended
// entries. Drainer is the sole caller.
func (r *ringBuf) PopBatch(max int, dst []*Entry) []*Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.size
	if n > max {
		n = max
	}
	for i := 0; i < n; i++ {
		dst = append(dst, r.buf[r.tail])
		r.buf[r.tail] = nil
		r.tail = (r.tail + 1) % r.cap
	}
	r.size -= n
	return dst
}

// Len is a snapshot — useful for the ring-depth gauge metric.
func (r *ringBuf) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}

// Drops returns the monotonic count of entries that were
// drop-oldest-overwritten because the ring was full at the moment of
// Enqueue. Operators surface this via the GUI status pane and via
// `journalctl -u sacore | grep drops`.
func (r *ringBuf) Drops() uint64 {
	return r.overwrites.Load()
}

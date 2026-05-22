// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package logger

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// drainer is the single background goroutine that pulls entries from
// the ring and dispatches each batch to every registered sink. See
// oam/logger/redesign.go for the contract — invariants I1-I7.
type drainer struct {
	ring *ringBuf

	mu    sync.RWMutex
	sinks []Sink

	stopCh chan struct{}
	doneCh chan struct{}
	tick   time.Duration

	flushCh chan chan struct{} // sentinel barrier signalling
}

const (
	drainerBatchSize    = 256
	drainerTickInterval = 5 * time.Millisecond // bound max latency for the ring → sink hop
)

func newDrainer(ring *ringBuf) *drainer {
	return &drainer{
		ring:    ring,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
		tick:    drainerTickInterval,
		flushCh: make(chan chan struct{}, 4),
	}
}

func (d *drainer) registerSink(s Sink) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sinks = append(d.sinks, s)
}

func (d *drainer) unregisterSink(s Sink) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, x := range d.sinks {
		if x == s {
			d.sinks = append(d.sinks[:i], d.sinks[i+1:]...)
			return
		}
	}
}

// snapshotSinks returns a copy of the current sink list for the
// drainer to iterate without holding the lock during Emit (a slow or
// blocking sink would otherwise serialise all RegisterSink calls).
func (d *drainer) snapshotSinks() []Sink {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Sink, len(d.sinks))
	copy(out, d.sinks)
	return out
}

func (d *drainer) start() {
	go d.run()
}

// stop signals the drainer to shut down. After Close() returns from
// every sink, doneCh is closed.
func (d *drainer) stop(timeout time.Duration) {
	close(d.stopCh)
	select {
	case <-d.doneCh:
	case <-time.After(timeout):
	}
}

// flush enqueues a barrier; returns when the drainer has drained the
// ring up to (and including) the moment flush was called and every
// registered sink has been Flush()'d.
func (d *drainer) flush(timeout time.Duration) error {
	ack := make(chan struct{}, 1)
	select {
	case d.flushCh <- ack:
	case <-time.After(timeout):
		return fmt.Errorf("logger: Flush timed out enqueueing barrier (drainer wedged?)")
	}
	select {
	case <-ack:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("logger: Flush barrier did not ack within %s", timeout)
	}
}

// run is the drainer goroutine. Catches panics from Emit/Flush/Close
// and auto-unregisters the offending sink so a buggy sink can't kill
// the pipeline (per redesign.go I7 risk note).
func (d *drainer) run() {
	defer close(d.doneCh)
	defer func() {
		if r := recover(); r != nil {
			// Drainer-died self-report: bypass the ring entirely
			// per redesign.go I7. CriticalSync writes directly to
			// stderr.
			fmt.Fprintf(os.Stderr, "logger: drainer goroutine panic: %v — pipeline is now dead\n", r)
		}
	}()

	ticker := time.NewTicker(d.tick)
	defer ticker.Stop()

	batch := make([]*Entry, 0, drainerBatchSize)

	for {
		// Drain the ring as fast as it fills. PopBatch blocks
		// nothing; if the ring is empty it returns the input slice
		// unmodified, and we wait for the tick.
		batch = d.ring.PopBatch(drainerBatchSize, batch[:0])
		if len(batch) > 0 {
			d.dispatch(batch)
			// Recycle entries to the pool now that every sink has
			// processed the batch.
			for _, e := range batch {
				putEntry(e)
			}
		}

		// Drain any pending flush barriers AFTER the batch landed,
		// so anything enqueued before the Flush() call is visible
		// to every sink before we ack.
		select {
		case ack := <-d.flushCh:
			// One more drain pass to capture stragglers that
			// arrived between the last PopBatch and now.
			batch = d.ring.PopBatch(drainerBatchSize, batch[:0])
			if len(batch) > 0 {
				d.dispatch(batch)
				for _, e := range batch {
					putEntry(e)
				}
			}
			d.flushSinks()
			close(ack)
		default:
		}

		// Stop signal: drain once more, run sink Flush+Close.
		select {
		case <-d.stopCh:
			batch = d.ring.PopBatch(drainerBatchSize, batch[:0])
			if len(batch) > 0 {
				d.dispatch(batch)
				for _, e := range batch {
					putEntry(e)
				}
			}
			d.flushSinks()
			d.closeSinks()
			return
		case <-ticker.C:
			// loop
		}
	}
}

// dispatch fans batch out to every sink. Each sink call is wrapped
// in its own panic guard so one bad sink doesn't take the others
// down.
func (d *drainer) dispatch(batch []*Entry) {
	for _, s := range d.snapshotSinks() {
		d.callEmit(s, batch)
	}
}

func (d *drainer) callEmit(s Sink, batch []*Entry) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "logger: sink %q panic during Emit: %v — auto-unregistering\n",
				s.Name(), r)
			d.unregisterSink(s)
		}
	}()
	s.Emit(batch)
}

func (d *drainer) flushSinks() {
	for _, s := range d.snapshotSinks() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "logger: sink %q panic during Flush: %v\n",
						s.Name(), r)
				}
			}()
			_ = s.Flush()
		}()
	}
}

func (d *drainer) closeSinks() {
	for _, s := range d.snapshotSinks() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "logger: sink %q panic during Close: %v\n",
						s.Name(), r)
				}
			}()
			_ = s.Close()
		}()
	}
}

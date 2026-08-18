// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// UDM SQN write-behind flusher.
//
// The hot auth path mutates the in-memory SQN via bumpSQN(). Persisting
// to UDR synchronously would put one write per UE-registration back on
// the single SQLite connection, which is exactly the bottleneck the
// cache was supposed to fix. Instead we flush dirty rows in batches.
//
// TS 33.102 §C.3.4 allows short gaps between stored and in-memory SQN
// values — the UE handles a backward SQN via the re-SYNCHRONISATION
// procedure. After a crash we may replay a handful of SQNs from the
// last flush; that's within the spec's tolerated drift and far smaller
// than the SQN re-sync window.
package udm

import (
	"sync/atomic"
	"time"

	"github.com/mmt/mmt-studio-core/nf/udr"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var (
	flusherStop   = make(chan struct{})
	flusherDone   = make(chan struct{})
	flusherActive atomic.Bool
)

// StartSQNFlusher spawns a goroutine that periodically persists dirty
// SQN values to UDR. Safe to call once per process; subsequent calls
// are no-ops. Pair with StopSQNFlusher (or the lifecycle hook registered
// in main.go) to drain pending writes on graceful shutdown.
func StartSQNFlusher(interval time.Duration) {
	if !flusherActive.CompareAndSwap(false, true) {
		return
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	log := logger.Get("udm.sqn")
	log.Infof("SQN flusher started (interval=%s)", interval)
	go func() {
		defer close(flusherDone)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-flusherStop:
				flushOnce(log)
				log.Info("SQN flusher stopped — final flush complete")
				return
			case <-t.C:
				flushOnce(log)
			}
		}
	}()
}

// StopSQNFlusher signals the goroutine to flush once and exit. Blocks
// until the flush returns. Safe to call even if the flusher was never
// started.
func StopSQNFlusher() {
	if !flusherActive.CompareAndSwap(true, false) {
		return
	}
	close(flusherStop)
	<-flusherDone
	// Reset channels so a later StartSQNFlusher still works (rare, but
	// the lifecycle machinery can re-init things in test setups).
	flusherStop = make(chan struct{})
	flusherDone = make(chan struct{})
}

func flushOnce(log *logger.Logger) {
	dirty := takeDirtySnapshot()
	if len(dirty) == 0 {
		return
	}
	ok, fail := 0, 0
	for imsi, sqn := range dirty {
		if err := udr.UpdateUeAuthData(imsi, udr.UEAuthData{SQN: sqn}); err != nil {
			log.Warnf("SQN persist %s: %v", imsi, err)
			fail++
			continue
		}
		ok++
	}
	if fail > 0 {
		log.Warnf("SQN flush: %d persisted, %d failed", ok, fail)
	} else {
		log.Debugf("SQN flush: %d row(s) persisted", ok)
	}
}

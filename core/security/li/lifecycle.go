// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// lifecycle.go — periodic-tick scaffolding for the LI subsystem.
//
// One ticker covers the warrant-expiry sweep that TS 33.127 §6.2
// requires (active warrants past their end_time must be flipped to
// expired and dropped from the in-memory POI cache so further events
// for that target are NOT captured). The X2 / X3 deliverers live in
// x2.go / x3.go and run their own goroutines.

package li

import (
	"context"
	"sync"
	"time"
)

// expireTickerInterval — how often ExpireWarrants runs. 30 s is
// the same cadence used by other periodic OAM tasks in the build
// (NWDAF analytics collection); operators don't get observable
// behaviour from a tighter cadence and the sweep is cheap.
const expireTickerInterval = 30 * time.Second

type expireWorker struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}

var expireTicker = &expireWorker{}

// StartExpireTicker launches the periodic ExpireWarrants sweep.
// Idempotent.
func StartExpireTicker() {
	expireTicker.mu.Lock()
	defer expireTicker.mu.Unlock()
	if expireTicker.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	expireTicker.cancel = cancel
	go func() {
		t := time.NewTicker(expireTickerInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ExpireWarrants()
			}
		}
	}()
}

// StopExpireTicker cancels the sweep.
func StopExpireTicker() {
	expireTicker.mu.Lock()
	defer expireTicker.mu.Unlock()
	if expireTicker.cancel != nil {
		expireTicker.cancel()
		expireTicker.cancel = nil
	}
}

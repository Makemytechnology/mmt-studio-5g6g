// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package lifecycle — graceful shutdown + startup hooks.
//
// Go port of infra/lifecycle.py. Wires SIGINT/SIGTERM to a bounded
// shutdown sequence. Subsystems register cleanup funcs via Register();
// on shutdown they run in LIFO order with a global deadline (default 5s).
// If the deadline is exceeded the process force-exits — matches the
// Python behaviour so stuck DPDK/SCTP cleanup never holds ports open.
package lifecycle

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ShutdownBudget is the soft deadline for all registered cleanup funcs.
// After it elapses the process is killed with os.Exit(1).
// 15s accommodates the slowest real-world step: AMF SCTP graceful shutdown
// (LINGER + SHUTDOWN_COMPLETE can take ~2s per gNB) and UPF netsetup
// (iptables removal via exec.Command, ~1.5s).
var ShutdownBudget = 30 * time.Second

// Func is a cleanup callback. ctx is cancelled when the budget expires —
// well-behaved callbacks should honour it (e.g. via net.Conn.SetDeadline).
type Func func(ctx context.Context)

type step struct {
	name string
	fn   Func
}

var (
	mu        sync.Mutex
	steps     []step
	shutting  bool
	log       = logger.Get("infra.lifecycle")
)

// Register adds a cleanup func. Callers should register in dependency
// order — later-registered funcs run first (LIFO) on shutdown.
func Register(name string, fn Func) {
	mu.Lock()
	defer mu.Unlock()
	steps = append(steps, step{name, fn})
}

// Shutdown runs every registered cleanup in LIFO order with the global
// budget. Safe to call multiple times — only the first call runs the
// chain; subsequent calls return immediately. After Shutdown completes
// the process exits 0 (or 1 if the budget fires first).
func Shutdown(signal string) {
	mu.Lock()
	if shutting {
		mu.Unlock()
		return
	}
	shutting = true
	pending := append([]step(nil), steps...)
	mu.Unlock()

	log.Infof("Graceful shutdown initiated (%s) — budget %s", signal, ShutdownBudget)
	ctx, cancel := context.WithTimeout(context.Background(), ShutdownBudget)
	defer cancel()

	// Hard-kill watchdog so a blocked cleanup never keeps the process alive.
	watchdog := time.AfterFunc(ShutdownBudget, func() {
		log.Warnf("Graceful shutdown exceeded %s budget — force exiting", ShutdownBudget)
		os.Exit(1)
	})
	defer watchdog.Stop()

	// Per-step soft timeout so one blocked cleanup doesn't starve the rest.
	// AMF SCTP graceful close can take ~2s (LINGER + SHUTDOWN_COMPLETE);
	// UPF netsetup teardown touches iptables via exec and can run ~1.5s.
	// AMF was observed exceeding 5s in production runs (commercial gNB
	// keeps the SCTP association open until SHUTDOWN_COMPLETE handshakes
	// out, which depends on RTT and any in-flight chunks). 15s gives the
	// AMF + UPF the slack they need without letting one stuck step eat
	// the whole ShutdownBudget — the overall watchdog still fires at 30s.
	const stepBudget = 15 * time.Second
	for i := len(pending) - 1; i >= 0; i-- {
		s := pending[i]
		if ctx.Err() != nil {
			log.Warnf("Skipping %s: shutdown budget exceeded", s.name)
			continue
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() {
				if r := recover(); r != nil {
					log.Warnf("Shutdown step %s panicked: %v", s.name, r)
				}
			}()
			s.fn(ctx)
		}()
		select {
		case <-done:
			log.Infof("Shutdown step completed: %s", s.name)
		case <-time.After(stepBudget):
			log.Warnf("Shutdown step %s exceeded %s — moving on", s.name, stepBudget)
		case <-ctx.Done():
			log.Warnf("Shutdown step %s aborted: global budget exceeded", s.name)
		}
	}
	log.Info("Graceful shutdown complete")
	os.Exit(0)
}

// InstallSignalHandlers wires SIGINT/SIGTERM to Shutdown. Call once in main.
func InstallSignalHandlers() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-ch
		Shutdown(sig.String())
	}()
	log.Info("Shutdown handlers registered (SIGTERM/SIGINT)")
}

// StartupCheck is the Python reference's empty hook — all runtime state is
// in-memory, so there is no crash recovery to perform. Kept as an
// extension point for NFs that later need to warm caches at boot.
func StartupCheck() {
	log.Info("Clean startup (all runtime state is in-memory)")
}

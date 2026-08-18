// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package health — NF health checks and background watchdog.
//
// Go port of infra/health.py. Each NF registers a Probe via Register();
// Watch() then returns a snapshot merging all probes + resource usage
// for the /health endpoint. StartWatchdog() launches a background loop
// that periodically re-probes and raises alarms on degradation.
//
// NF probes can be added lazily (e.g. when AMF initializes it calls
// health.Register("amf", amfProbe)). Unregistered NFs are simply
// absent from the output rather than reported unhealthy — that keeps
// the payload honest across different deployment footprints.
package health

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Probe returns per-NF status. Implementations MUST be non-blocking.
// Errors are captured as Status="unhealthy", Error=err.Error().
type Probe func() Status

// Status is the payload of a single probe.
type Status struct {
	Status string         `json:"status"` // "healthy" | "degraded" | "unhealthy" | "not_started"
	Error  string         `json:"error,omitempty"`
	Detail map[string]any `json:"-"`
}

// Report is the aggregate snapshot returned by Watch().
type Report struct {
	Status    string            `json:"status"` // worst of all NFs
	Timestamp time.Time         `json:"timestamp"`
	UptimeSec float64           `json:"uptime_sec"`
	NFs       map[string]Status `json:"nfs"`
	Resources map[string]any    `json:"resources"`
}

var (
	mu        sync.RWMutex
	probes    = map[string]Probe{}
	startTime atomic.Pointer[time.Time]

	watchdogRunning atomic.Bool
	watchdogDone    chan struct{}
	watchdogStop    chan struct{}

	log = logger.Get("infra.health")
)

// Register adds a probe. Replacing an existing name is allowed (hot-swap).
func Register(name string, p Probe) {
	mu.Lock()
	defer mu.Unlock()
	probes[name] = p
}

// Unregister removes a probe (e.g., NF shutdown).
func Unregister(name string) {
	mu.Lock()
	defer mu.Unlock()
	delete(probes, name)
}

// Watch runs every registered probe and returns an aggregated report.
// Probe panics are caught and reported as "unhealthy".
func Watch() Report {
	mu.RLock()
	snapshot := make(map[string]Probe, len(probes))
	for k, v := range probes {
		snapshot[k] = v
	}
	mu.RUnlock()

	rep := Report{
		Status:    "healthy",
		Timestamp: time.Now(),
		NFs:       make(map[string]Status, len(snapshot)),
		Resources: resourceUsage(),
	}
	if t := startTime.Load(); t != nil {
		rep.UptimeSec = time.Since(*t).Seconds()
	}
	for name, p := range snapshot {
		rep.NFs[name] = safeProbe(p)
		if rep.NFs[name].Status != "healthy" {
			rep.Status = "degraded"
		}
	}
	return rep
}

func safeProbe(p Probe) (s Status) {
	defer func() {
		if r := recover(); r != nil {
			s = Status{Status: "unhealthy", Error: "probe panic"}
		}
	}()
	return p()
}

// ── Watchdog ────────────────────────────────────────────────────────────

const (
	watchdogInterval = 30 * time.Second
	startupGrace     = 30 * time.Second
)

// StartWatchdog launches the background health loop. Idempotent.
// Requires an alarm hook from oam/fm — pass nil to skip alarm raising.
func StartWatchdog(onDegraded func(nf string, s Status)) {
	if watchdogRunning.Swap(true) {
		return
	}
	now := time.Now()
	startTime.Store(&now)
	watchdogStop = make(chan struct{})
	watchdogDone = make(chan struct{})
	go watchdogLoop(onDegraded)
	log.Infof("Health watchdog started (interval=%s)", watchdogInterval)
}

// StopWatchdog halts the loop. Safe to call if not running.
func StopWatchdog() {
	if !watchdogRunning.Swap(false) {
		return
	}
	close(watchdogStop)
	<-watchdogDone
	log.Info("Health watchdog stopped")
}

func watchdogLoop(onDegraded func(nf string, s Status)) {
	defer close(watchdogDone)
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-watchdogStop:
			return
		case <-ticker.C:
			rep := Watch()
			if rep.Status == "healthy" || time.Since(*startTime.Load()) < startupGrace {
				continue
			}
			if onDegraded != nil {
				for name, s := range rep.NFs {
					if s.Status != "healthy" {
						onDegraded(name, s)
					}
				}
			}
		}
	}
}

// ── Resource usage (best-effort, never errors) ──────────────────────────

func resourceUsage() map[string]any {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return map[string]any{
		"memory_mb":  float64(ms.Alloc) / (1024 * 1024),
		"sys_mb":     float64(ms.Sys) / (1024 * 1024),
		"goroutines": runtime.NumGoroutine(),
	}
}

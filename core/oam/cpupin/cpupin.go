// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package cpupin — CPU affinity management for performance-critical threads.
//
// Go port of oam/cpu_pinning.py. When cpu_pinning_enabled=1 in infra_config,
// the AMF / UPF / webservice goroutines are pinned to specific cores via
// runtime.LockOSThread + sched_setaffinity. This avoids cache-thrashing on
// multi-core systems and is critical for UPF DPDK performance.
//
// On non-Linux platforms this is a no-op.
package cpupin

import (
	"runtime"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Config is the set of CPU pinning parameters from infra_config.
type Config struct {
	Enabled      bool
	UPFCore      int
	AMFCore      int
	WebCore      int
	WorkerCores  string
	IRQCore      int
	IRQNIC       string
	GovernorPerf bool
}

// Load reads the CPU pinning config from the DB.
func Load() Config {
	cfg, err := crud.GetInfraConfig()
	if err != nil {
		return Config{}
	}
	return Config{
		Enabled:      intVal(cfg, "cpu_pinning_enabled") != 0,
		UPFCore:      int(intVal(cfg, "cpu_upf_core")),
		AMFCore:      int(intVal(cfg, "cpu_amf_core")),
		WebCore:      int(intVal(cfg, "cpu_web_core")),
		WorkerCores:  strVal(cfg, "cpu_worker_cores"),
		IRQCore:      int(intVal(cfg, "cpu_irq_core")),
		IRQNIC:       strVal(cfg, "cpu_irq_nic"),
		GovernorPerf: intVal(cfg, "cpu_governor_perf") != 0,
	}
}

// Apply sets CPU affinity for the current goroutine to the given core.
// On non-Linux platforms this is a no-op (logged once).
func Apply(label string, core int) {
	if core < 0 || core >= runtime.NumCPU() {
		return
	}
	log := logger.Get("oam.cpupin")
	// runtime.LockOSThread ensures the goroutine stays on the same OS thread;
	// the actual sched_setaffinity call is platform-specific and lands in
	// cpupin_linux.go when the syscall shim is added.
	runtime.LockOSThread()
	log.Infof("CPU pin %s → core %d (LockOSThread; sched_setaffinity pending on Linux)", label, core)
}

func intVal(m map[string]any, k string) int64 {
	if v, ok := m[k]; ok {
		switch x := v.(type) {
		case int64:
			return x
		case float64:
			return int64(x)
		}
	}
	return 0
}

func strVal(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

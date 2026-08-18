# oam/cpupin — CPU Affinity / NUMA

## 1. Role / scope

`oam/cpupin` pins performance-critical goroutines to specific CPU
cores when `cpu_pinning_enabled=1` in `infra_config`. The intent
mirrors the Python reference (`oam/cpu_pinning.py`): keep AMF / UPF /
webservice loops on dedicated cores, avoid cache-thrashing on
multi-core boxes, and give the C dataplane (DPDK) the deterministic
core residency it expects.

Today the package does the **first half**: `runtime.LockOSThread()`
fixes the goroutine to its OS thread and logs an INFO line. The
`sched_setaffinity(2)` syscall that would actually move the thread
to a specific core is pending — the package compiles on every
platform and the call is a deliberate no-op on non-Linux builds.

## 2. File map

| File | LOC | Role |
|---|---:|---|
| `oam/cpupin/cpupin.go` | 83 | `Config`, `Load`, `Apply` |

## 3. Public API / contracts

### `Config` — mirrors `infra_config` columns

```go
type Config struct {
    Enabled      bool   // cpu_pinning_enabled
    UPFCore      int    // cpu_upf_core
    AMFCore      int    // cpu_amf_core
    WebCore      int    // cpu_web_core
    WorkerCores  string // cpu_worker_cores (CSV / range)
    IRQCore      int    // cpu_irq_core
    IRQNIC       string // cpu_irq_nic
    GovernorPerf bool   // cpu_governor_perf -- request "performance" governor
}
```

### Functions

| Func | Behaviour |
|---|---|
| `Load() Config` | Reads via `crud.GetInfraConfig`; zero `Config` on DB error. |
| `Apply(label string, core int)` | Bounds-checks `core` against `runtime.NumCPU()`. Calls `runtime.LockOSThread()` and logs `oam.cpupin: CPU pin <label> -> core <N>`. The actual `sched_setaffinity` call is the next stage and lands in a `cpupin_linux.go` shim. |

## 4. Headline flows / lifecycle

NF startup (planned shape):

1. `cfg := cpupin.Load()` once.
2. If `cfg.Enabled`, each long-running goroutine calls
   `cpupin.Apply("upf-rx", cfg.UPFCore)` from inside its loop's
   first turn. `runtime.LockOSThread` ensures the goroutine never
   migrates threads, which is the prerequisite for affinity to mean
   anything.
3. The platform-specific shim (`cpupin_linux.go`, deferred) issues
   `sched_setaffinity(0, mask)` so the OS scheduler honours the
   target core.

Until the syscall lands, the LockOSThread call is still useful: it
prevents Go's M:N scheduler from moving the goroutine across threads
mid-burst, which removes one source of cache-eviction jitter.

## 5. Stubs / TODOs

- `cpupin.go:57-61` — `sched_setaffinity` pending. The log line
  literally says "(LockOSThread; sched_setaffinity pending on
  Linux)" so the operator sees the partial application.
- No call sites in the Go tree yet (`grep -rn "cpupin\." nf/ webservice/`
  is empty as of this commit). The producers will be the UPF
  packet-IO goroutine and the AMF NGAP receive loop.

## 6. References

No spec citations in source. The performance rationale lives in the
DPDK / kernel-tuning literature, not in 3GPP TS clauses.

---
*Last refreshed against commit `13a181d`.*

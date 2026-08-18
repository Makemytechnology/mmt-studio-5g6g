# oam/banner — Startup Banner + Sysctl Sanity Check

## 1. Role / scope

`oam/banner` does two things at process start:

1. **`banner.Log()`** emits a one-shot 72-column banner with
   project + version, host + virtualization, OS pretty-name,
   kernel + arch, CPU model, core count + RAM, Go version, bundled
   DPDK version, and the start timestamp. Every bug report carries
   this header so the reproduction environment is unambiguous.
2. **`banner.CheckSysctls()`** reads the runtime sysctl values that
   `sacore-web` relies on under burst SCTP / SBI traffic, and emits
   one WARN per key whose live value is below the recommendation
   shipped in `scripts/sysctl/99-sacore.conf`. Operators hit
   hard-to-diagnose symptoms (truncated SCTP bursts,
   connection-reset under load, dropped NAPI packets) when those
   values regress; surfacing the drift at boot points them at the
   cause instead of asking them to tcpdump.

Both functions are called from `webservice/app/app.go:160-161` once
at boot.

## 2. File map

| File | LOC | Role |
|---|---:|---|
| `oam/banner/banner.go` | 211 | `Log()`, host probes, DPDK version detection |
| `oam/banner/sysctl.go` | 117 | `CheckSysctls()`, recommended-value table |

## 3. Public API / contracts

### `banner.ProjectName` / `banner.ProjectVersion`

`ProjectVersion` defaults to `"1.0.0"` and is overridable via
`-ldflags "-X github.com/mmt/mmt-studio-core/oam/banner.ProjectVersion=..."`
at build time. The default matches the Python reference's banner so
operator tooling that greps the version string works for either
backend.

### `banner.Log()`

Logs (under module `startup`):

```
========================================================================
MMT Studio Core 1.0.0
Host:    sacore-host-01  (kvm/qemu)
OS:      Ubuntu 24.04.1 LTS
Kernel:  linux 6.8.0-31-generic (amd64)
CPU:     Intel(R) Xeon(R) Silver 4310 CPU @ 2.10GHz
Cores:   8   RAM: 32.0 GB
Go:      go1.22.4
DPDK:    25.11
Started: 2026-04-19T11:04:22+05:30
========================================================================
```

Internal probes (all fail-soft, return "unknown"):

| Func | Reads |
|---|---|
| `osPrettyName` | `/etc/os-release PRETTY_NAME=` |
| `virtualization` | `systemd-detect-virt` + DMI fallback (`/sys/class/dmi/id/sys_vendor`) |
| `cpuModel` | `/proc/cpuinfo "model name" / "Model" / "Hardware"` |
| `ramTotalGB` | `/proc/meminfo MemTotal` (kB -> GB) |
| `kernelRelease` | `/proc/sys/kernel/osrelease` |
| `dpdkInfo` | scans `libs/` for `dpdk-<ver>` subdirectory; reports the suffix |

The DPDK version probe (`banner.go:185-200`) is the convention
documented at `banner.go:30-32`: the bundled DPDK lives under
`libs/dpdk-<ver>/`; renaming the directory is the only change
needed to bump versions.

### `banner.CheckSysctls()`

Two tables drive the check (`sysctl.go:28-50`):

`sysctlExpect` — single-int values:

| Key | Path | Minimum |
|---|---|---:|
| `net.core.rmem_max` | `/proc/sys/net/core/rmem_max` | 8388608 |
| `net.core.wmem_max` | `/proc/sys/net/core/wmem_max` | 8388608 |
| `net.core.optmem_max` | `/proc/sys/net/core/optmem_max` | 65536 |
| `net.core.somaxconn` | `/proc/sys/net/core/somaxconn` | 65535 |
| `net.core.netdev_max_backlog` | `/proc/sys/net/core/netdev_max_backlog` | 65535 |

`sysctlTripleExpect` — space-separated triples; only the `max` slot
matters for burst handling:

| Key | Path | Slot | Minimum |
|---|---|:---:|---:|
| `net.sctp.sctp_rmem[max]` | `/proc/sys/net/sctp/sctp_rmem` | 3 | 8388608 |
| `net.sctp.sctp_wmem[max]` | `/proc/sys/net/sctp/sctp_wmem` | 3 | 8388608 |

Per below-recommendation key, one WARN line:

```
sysctl net.core.rmem_max=212992 is below recommended 8388608 -- bursts may be truncated
```

After all keys, if any drift was seen, one summary WARN with the fix
hint:

```
kernel tuning below recommendation (3 keys) -- apply: sudo cp scripts/sysctl/99-sacore.conf /etc/sysctl.d/ && sudo sysctl --system
```

A correctly-tuned host produces zero log lines. The function takes
no arguments, returns nothing — it logs via `logger.Get("startup")`
so the output is greppable alongside the boot banner.

## 4. Headline flows / lifecycle

**Boot, in order, from `webservice/app/app.go`:**

```go
// app.go:160-161
banner.Log()
banner.CheckSysctls()
```

This is the only call site; both are no-arg, idempotent (safe to
call multiple times — `Log()` will print again, `CheckSysctls()`
re-reads `/proc/sys`). Under unit tests neither is called.

The functions intentionally produce no error returns: a missing
`/etc/os-release`, a chrooted `/proc`, or a failing
`systemd-detect-virt` all degrade gracefully to "unknown" /
"bare metal" / no-output rather than aborting boot.

## 5. Stubs / TODOs

None — the package is feature-complete for its scope. The sysctl
table is deliberately small (`sysctl.go:23-27` notes "expanding this
list is fine; keep it in sync with 99-sacore.conf when the
recommendation changes"); add rows when a new burst-handling key
proves load-bearing.

## 6. References

No spec citations in source. The recommended sysctl values come
from `scripts/sysctl/99-sacore.conf` and are tied to observed
symptoms under sustained NGAP / SBI burst traffic, not to a 3GPP
clause.

---
*Last refreshed against commit `13a181d`.*

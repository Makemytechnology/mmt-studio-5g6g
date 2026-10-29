# oam/platform — Host Environment Detection

## 1. Role / scope

`oam/platform` collects host metadata at GUI-load cadence:
virtualization type, kernel, CPU model + flags, NUMA layout,
hugepage state, NICs, transparent-hugepage mode, isolcpus / nohz_full
boot args, VFIO module presence. The `Info` struct it returns is
rendered by the platform panel and is the input that preset matchers
("DPDK-capable host", "AVX-512 box", "VM with no hugepages") filter
on.

All reads are cheap `/proc` + `/sys` lookups, except a single
`systemd-detect-virt` exec for the virtualization probe. On non-Linux
builds most fields are zero-valued and the GUI falls back to
"unknown" so dev builds compile.

## 2. Architecture

```
   GUI / preset matchers
            |
            v
   platform.Get() Info
        |
        +-- detectVirt          systemd-detect-virt + DMI fallback
        +-- runtime.GOARCH
        +-- /proc/sys/kernel/osrelease
        +-- cpuInfo()           parses /proc/cpuinfo
        +-- onlineCPUs()        /sys/devices/system/cpu/online
        +-- /sys/.../cpu0/cpufreq/scaling_governor
        +-- AVX2 / AVX-512 derived from CPU flags
        +-- memGB()             /proc/meminfo MemTotal
        +-- numaNodes()         /sys/devices/system/node/node*/cpulist
        +-- hugepageState()     /sys/kernel/mm/hugepages/{2M,1G}/{nr,free}
        +-- nicList()           /sys/class/net/* (skip lo)
        +-- thpMode()           /sys/kernel/mm/transparent_hugepage/enabled
        +-- VFIOLoaded          grep vfio_pci in /proc/modules
        +-- IsolCPUs / NohzFull /proc/cmdline
```

## 3. File map

| File | LOC | Role |
|---|---:|---|
| `oam/platform/platform.go` | 281 | `Get`, `Info`, `Numa`, `HugeInfo`, `NIC` types + helpers |

## 4. Public API / contracts

### `Info` (`platform.go:26-45`)

```go
type Info struct {
    Virt          string   // "kvm" | "vmware" | "vbox" | "qemu" | "none" | ...
    Arch          string   // runtime.GOARCH
    Kernel        string   // /proc/sys/kernel/osrelease
    CPUModel      string
    CPUCores      int
    CPUOnline     int
    CPUGovernor   string   // "performance" | "powersave" | ...
    CPUFlags      []string
    HasAVX2       bool
    HasAVX512     bool
    RAMGB         float64
    NumaNodes     []Numa   // {ID, CPUs}
    Hugepages     HugeInfo // {2M_reserved, 2M_free, 1G_reserved, 1G_free, 1G_supported}
    NICs          []NIC    // {Name, SpeedMbps, OperState, MTU}
    THPMode       string   // "always" | "madvise" | "never"
    VFIOLoaded    bool
    IsolCPUs      string
    NohzFull      string
}
```

### `Get() Info`

Single entry point. Cheap enough to call on every GUI load. All
helpers are best-effort: they swallow read errors and return empty /
fallback values rather than failing the snapshot.

## 5. Headline flows / lifecycle

**GUI panel.** Webservice route
(`webservice/app/routes_misc.go:63`) returns `platform.Get()` as
JSON for the platform panel.

**Preset matcher.** `webservice/app/operations_route.go:753, :762`
calls `platform.Get()` and uses the result to gate / select tuning
presets — e.g. "1G hugepages required for DPDK fast-path"; "AVX-512
required for crypto offload preset".

**Notable derivations:**

- `Virt` — `systemd-detect-virt` first; on `none` returns "bare
  metal"; missing systemd falls back to DMI vendor/product strings
  with case-insensitive contains for "virtualbox", "vmware", "qemu",
  "kvm", "microsoft", "xen". `Get()` separately returns "vbox" /
  "vmware" / "qemu" / "none" tokens (`platform.go:105-122`); the
  banner package has its own normaliser
  (`oam/banner/banner.go:85-119`) that returns longer-form names
  ("virtualbox", "kvm/qemu") for the startup banner. They are
  intentionally separate — the GUI panel and the banner have
  different display conventions.
- `numaNodes` — falls back to a single `{ID:0}` node when
  `/sys/devices/system/node` doesn't exist, so non-NUMA hosts still
  surface as "1 node".
- `Hugepages.OneG_Supported` — set when the `hugepages-1048576kB`
  directory exists; absent on kernels that didn't enable 1 G pages.

## 6. Stubs / TODOs

No TODOs in source. The package is intentionally a thin probe — by
design it exposes data the GUI / matchers consume; it does not
**change** anything. Tuning actions live in `oam/banner/sysctl.go`
(advisory log lines) and in the planned `cpupin_linux.go` shim.

## 7. References

No spec citations in source. Field semantics follow Linux's
`/proc` + `/sys` ABI; the AVX flag names follow Intel's CPUID
documentation (matched as substrings in `/proc/cpuinfo flags:`).

---
*Last refreshed against commit `13a181d`.*

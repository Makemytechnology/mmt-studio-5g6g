// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package platform — host environment detection for tuning + UX.
//
// Go port of oam/platform_info.py. All reads are cheap /proc + /sys
// lookups — no external processes except `systemd-detect-virt` which
// is present on every systemd distro. Returns a single Info struct
// the GUI can render and preset matchers can filter on.
//
// On non-Linux builds most fields are zero-valued and the GUI falls
// back to "unknown" — this is intentional so dev builds compile.
package platform

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Info describes the host. Cheap enough for every GUI load.
type Info struct {
	Virt          string   `json:"virt"`
	Arch          string   `json:"arch"`
	Kernel        string   `json:"kernel"`
	CPUModel      string   `json:"cpu_model"`
	CPUCores      int      `json:"cpu_cores"`
	CPUOnline     int      `json:"cpu_online"`
	CPUGovernor   string   `json:"cpu_governor"`
	CPUFlags      []string `json:"cpu_flags,omitempty"`
	HasAVX2       bool     `json:"has_avx2"`
	HasAVX512     bool     `json:"has_avx512"`
	RAMGB         float64  `json:"ram_gb"`
	NumaNodes     []Numa   `json:"numa_nodes"`
	Hugepages     HugeInfo `json:"hugepages"`
	NICs          []NIC    `json:"nics"`
	THPMode       string   `json:"thp_mode"`
	VFIOLoaded    bool     `json:"vfio_loaded"`
	IsolCPUs      string   `json:"isolcpus"`
	NohzFull      string   `json:"nohz_full"`
}

// Numa is one NUMA node.
type Numa struct {
	ID   int    `json:"id"`
	CPUs string `json:"cpus"`
}

// HugeInfo holds hugepage counts.
type HugeInfo struct {
	TwoM_Reserved int  `json:"2M_reserved"`
	TwoM_Free     int  `json:"2M_free"`
	OneG_Reserved int  `json:"1G_reserved"`
	OneG_Free     int  `json:"1G_free"`
	OneG_Supported bool `json:"1G_supported"`
}

// NIC is a single network interface.
type NIC struct {
	Name      string `json:"name"`
	SpeedMbps int    `json:"speed_mbps"`
	OperState string `json:"operstate"`
	MTU       int    `json:"mtu"`
}

// Get collects the host info snapshot.
func Get() Info {
	cores, model, flags := cpuInfo()
	return Info{
		Virt:        detectVirt(),
		Arch:        runtime.GOARCH,
		Kernel:      readFile("/proc/sys/kernel/osrelease"),
		CPUModel:    model,
		CPUCores:    cores,
		CPUOnline:   onlineCPUs(),
		CPUGovernor: readFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor"),
		CPUFlags:    flags,
		HasAVX2:     containsStr(flags, "avx2"),
		HasAVX512:   containsStr(flags, "avx512f"),
		RAMGB:       memGB(),
		NumaNodes:   numaNodes(),
		Hugepages:   hugepageState(),
		NICs:        nicList(),
		THPMode:     thpMode(),
		VFIOLoaded:  strings.Contains(readFile("/proc/modules"), "vfio_pci"),
		IsolCPUs:    cmdlineParam("isolcpus"),
		NohzFull:    cmdlineParam("nohz_full"),
	}
}

// ── Internal helpers (best-effort, never error) ─────────────────────────

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func detectVirt() string {
	if out, err := exec.Command("systemd-detect-virt").Output(); err == nil {
		v := strings.TrimSpace(string(out))
		if v != "" {
			return v
		}
	}
	dmi := strings.ToLower(readFile("/sys/class/dmi/id/product_name"))
	switch {
	case strings.Contains(dmi, "virtualbox"):
		return "vbox"
	case strings.Contains(dmi, "vmware"):
		return "vmware"
	case strings.Contains(dmi, "qemu"), strings.Contains(dmi, "kvm"):
		return "qemu"
	}
	return "none"
}

func cpuInfo() (cores int, model string, flags []string) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return runtime.NumCPU(), runtime.GOARCH, nil
	}
	defer f.Close()
	flagSet := map[string]struct{}{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "processor"):
			cores++
		case strings.HasPrefix(line, "model name") && model == "":
			if _, v, ok := strings.Cut(line, ":"); ok {
				model = strings.TrimSpace(v)
			}
		case (strings.HasPrefix(line, "flags") || strings.HasPrefix(line, "Features")) && len(flagSet) == 0:
			if _, v, ok := strings.Cut(line, ":"); ok {
				for _, f := range strings.Fields(v) {
					flagSet[f] = struct{}{}
				}
			}
		}
	}
	for k := range flagSet {
		flags = append(flags, k)
	}
	sort.Strings(flags)
	if cores == 0 {
		cores = runtime.NumCPU()
	}
	return
}

func memGB() float64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseFloat(fields[1], 64)
				return kb / 1024 / 1024
			}
		}
	}
	return 0
}

func hugepageState() HugeInfo {
	var h HugeInfo
	base := "/sys/kernel/mm/hugepages"
	h.TwoM_Reserved, _ = strconv.Atoi(readFile(base + "/hugepages-2048kB/nr_hugepages"))
	h.TwoM_Free, _ = strconv.Atoi(readFile(base + "/hugepages-2048kB/free_hugepages"))
	if _, err := os.Stat(base + "/hugepages-1048576kB"); err == nil {
		h.OneG_Supported = true
		h.OneG_Reserved, _ = strconv.Atoi(readFile(base + "/hugepages-1048576kB/nr_hugepages"))
		h.OneG_Free, _ = strconv.Atoi(readFile(base + "/hugepages-1048576kB/free_hugepages"))
	}
	return h
}

func nicList() []NIC {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return nil
	}
	var out []NIC
	for _, e := range entries {
		name := e.Name()
		if name == "lo" {
			continue
		}
		speed, _ := strconv.Atoi(readFile("/sys/class/net/" + name + "/speed"))
		mtu, _ := strconv.Atoi(readFile("/sys/class/net/" + name + "/mtu"))
		out = append(out, NIC{
			Name:      name,
			SpeedMbps: speed,
			OperState: readFile("/sys/class/net/" + name + "/operstate"),
			MTU:       mtu,
		})
	}
	return out
}

func numaNodes() []Numa {
	entries, err := os.ReadDir("/sys/devices/system/node")
	if err != nil {
		return []Numa{{ID: 0}}
	}
	var out []Numa
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "node") {
			continue
		}
		id, err := strconv.Atoi(e.Name()[4:])
		if err != nil {
			continue
		}
		cpus := readFile("/sys/devices/system/node/" + e.Name() + "/cpulist")
		out = append(out, Numa{ID: id, CPUs: cpus})
	}
	if len(out) == 0 {
		return []Numa{{ID: 0}}
	}
	return out
}

func onlineCPUs() int {
	raw := readFile("/sys/devices/system/cpu/online")
	count := 0
	for _, part := range strings.Split(raw, ",") {
		if a, b, ok := strings.Cut(part, "-"); ok {
			lo, _ := strconv.Atoi(a)
			hi, _ := strconv.Atoi(b)
			count += hi - lo + 1
		} else if part != "" {
			count++
		}
	}
	return count
}

var reThp = regexp.MustCompile(`\[(\w+)\]`)

func thpMode() string {
	raw := readFile("/sys/kernel/mm/transparent_hugepage/enabled")
	m := reThp.FindStringSubmatch(raw)
	if len(m) >= 2 {
		return m[1]
	}
	return "unknown"
}

func cmdlineParam(name string) string {
	raw := readFile("/proc/cmdline")
	re := regexp.MustCompile(name + `=(\S+)`)
	m := re.FindStringSubmatch(raw)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

func containsStr(ss []string, t string) bool {
	for _, s := range ss {
		if s == t {
			return true
		}
	}
	return false
}

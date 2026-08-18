// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package banner logs a one-shot startup banner with version / platform /
// DPDK metadata so every bug report carries enough context to reproduce.
//
// Mirror of oam/startup_banner.py. Call Log() once at boot.
package banner

import (
	"bufio"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

const ProjectName = "MMT Studio Core"

// ProjectVersion is overridable from -ldflags at build time; default matches
// the Python banner ('MMT Studio Core 1.0.0') so ops tools see a consistent
// version string whichever backend is running.
var ProjectVersion = "1.0.0"

// DpdkBundledPrefix names the sub-directory under libs/ that carries the
// vendored DPDK sources. The bundled version is derived from the suffix
// (e.g. libs/dpdk-25.11/ → 25.11). A version bump only requires renaming
// the vendored directory; everything else follows from that.
const DpdkBundledPrefix = "libs/dpdk-"

// Log emits the startup banner. Safe to call multiple times.
func Log() {
	log := logger.Get("startup")

	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}

	dpdkVer := dpdkInfo()
	osName := osPrettyName()
	virt := virtualization()
	cpu := cpuModel()
	ramGB := ramTotalGB()
	goVer := runtime.Version()

	bar := strings.Repeat("=", 72)
	log.Info(bar)
	log.Infof("%s %s", ProjectName, ProjectVersion)
	log.Infof("Host:    %s  (%s)", host, virt)
	log.Infof("OS:      %s", osName)
	log.Infof("Kernel:  %s %s (%s)", runtime.GOOS, kernelRelease(), runtime.GOARCH)
	log.Infof("CPU:     %s", cpu)
	if ramGB > 0 {
		log.Infof("Cores:   %d   RAM: %.1f GB", runtime.NumCPU(), ramGB)
	} else {
		log.Infof("Cores:   %d", runtime.NumCPU())
	}
	log.Infof("Go:      %s", goVer)
	log.Infof("DPDK:    %s", dpdkVer)
	log.Infof("Started: %s", time.Now().Format(time.RFC3339))
	log.Info(bar)
}

// ── Internal probes (fail-soft, return "unknown") ───────────────────────

func osPrettyName() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return runtime.GOOS
}

func virtualization() string {
	// systemd-detect-virt is the canonical probe where available.
	if out, err := exec.Command("systemd-detect-virt").Output(); err == nil {
		v := strings.TrimSpace(string(out))
		if v != "" && v != "none" {
			switch v {
			case "oracle":
				return "virtualbox"
			case "microsoft":
				return "hyper-v"
			}
			return v
		}
		if v == "none" {
			return "bare metal"
		}
	}
	// DMI fallback.
	if b, err := os.ReadFile("/sys/class/dmi/id/sys_vendor"); err == nil {
		v := strings.ToLower(strings.TrimSpace(string(b)))
		switch {
		case strings.Contains(v, "virtualbox"), strings.Contains(v, "innotek"):
			return "virtualbox"
		case strings.Contains(v, "vmware"):
			return "vmware"
		case strings.Contains(v, "qemu"), strings.Contains(v, "kvm"):
			return "kvm/qemu"
		case strings.Contains(v, "microsoft"):
			return "hyper-v"
		case strings.Contains(v, "xen"):
			return "xen"
		}
	}
	return "bare metal"
}

func cpuModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return runtime.GOARCH
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var model, hardware string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "model name"):
			if _, v, ok := strings.Cut(line, ":"); ok {
				return strings.TrimSpace(v)
			}
		case strings.HasPrefix(line, "Model"):
			if _, v, ok := strings.Cut(line, ":"); ok {
				model = strings.TrimSpace(v)
			}
		case strings.HasPrefix(line, "Hardware"):
			if _, v, ok := strings.Cut(line, ":"); ok {
				hardware = strings.TrimSpace(v)
			}
		}
	}
	if model != "" {
		return model
	}
	if hardware != "" {
		return hardware
	}
	return runtime.GOARCH
}

func ramTotalGB() float64 {
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
				kb, _ := parseInt(fields[1])
				return float64(kb) / (1024 * 1024)
			}
		}
	}
	return 0
}

func kernelRelease() string {
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return "unknown"
}

// dpdkInfo returns the bundled DPDK version derived from the
// libs/dpdk-<ver>/ directory name. Returns "unknown" if no matching
// directory exists — which is a configuration error, not a runtime one.
func dpdkInfo() string {
	entries, err := os.ReadDir("libs")
	if err != nil {
		return "unknown"
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "dpdk-") {
			return strings.TrimPrefix(name, "dpdk-")
		}
	}
	return "unknown"
}

func parseInt(s string) (int64, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return n, nil
		}
		n = n*10 + int64(c-'0')
	}
	return n, nil
}

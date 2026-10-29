// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Startup sanity check for the kernel-tunable values sacore-web relies
// on. Reads /proc/sys/* at boot and logs a WARN line per value that's
// below the recommendation shipped in scripts/sysctl/99-sacore.conf.
//
// Operators hit hard-to-diagnose symptoms (truncated SCTP bursts,
// connection-reset under load, dropped NAPI packets) when the file
// isn't installed or a kernel upgrade reverted the values. Surfacing
// the drift at boot points them straight at the cause instead of
// asking them to tcpdump.

package banner

import (
	"os"
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// sysctlExpect mirrors the non-optional subset of 99-sacore.conf. We
// don't check every row — just the ones where an unclipped value is
// actually load-bearing for sacore-web under burst traffic. Expanding
// this list is fine; keep it in sync with 99-sacore.conf when the
// recommendation changes.
var sysctlExpect = []struct {
	path string // /proc/sys/... path
	key  string // sysctl key (for the log line)
	want int64  // minimum recommended value
}{
	{"/proc/sys/net/core/rmem_max", "net.core.rmem_max", 8388608},
	{"/proc/sys/net/core/wmem_max", "net.core.wmem_max", 8388608},
	{"/proc/sys/net/core/optmem_max", "net.core.optmem_max", 65536},
	{"/proc/sys/net/core/somaxconn", "net.core.somaxconn", 65535},
	{"/proc/sys/net/core/netdev_max_backlog", "net.core.netdev_max_backlog", 65535},
}

// sysctlTripleExpect covers the space-separated triples (min, default,
// max) where only the max slot matters for burst handling.
var sysctlTripleExpect = []struct {
	path    string
	key     string
	slot    int // 1-based slot index to check
	wantMin int64
}{
	{"/proc/sys/net/sctp/sctp_rmem", "net.sctp.sctp_rmem[max]", 3, 8388608},
	{"/proc/sys/net/sctp/sctp_wmem", "net.sctp.sctp_wmem[max]", 3, 8388608},
}

// CheckSysctls reads the runtime sysctl values and emits one WARN per
// key whose live value is below the recommendation. Silent on a
// correctly-tuned host. Safe to call before Log() or after — uses its
// own logger channel so the output is greppable.
func CheckSysctls() {
	log := logger.Get("startup")
	var drift []string

	for _, e := range sysctlExpect {
		live, ok := readSysctlInt(e.path)
		if !ok {
			continue
		}
		if live < e.want {
			log.Warnf("sysctl %s=%d is below recommended %d — bursts may be truncated",
				e.key, live, e.want)
			drift = append(drift, e.key)
		}
	}

	for _, e := range sysctlTripleExpect {
		live, ok := readSysctlTripleSlot(e.path, e.slot)
		if !ok {
			continue
		}
		if live < e.wantMin {
			log.Warnf("sysctl %s=%d is below recommended %d — bursts may be truncated",
				e.key, live, e.wantMin)
			drift = append(drift, e.key)
		}
	}

	if len(drift) > 0 {
		log.Warnf("kernel tuning below recommendation (%d keys) — apply: sudo cp scripts/sysctl/99-sacore.conf /etc/sysctl.d/ && sudo sysctl --system",
			len(drift))
	}
}

func readSysctlInt(path string) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(b))
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func readSysctlTripleSlot(path string, slot int) (int64, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(b))
	if slot < 1 || slot > len(fields) {
		return 0, false
	}
	n, err := strconv.ParseInt(fields[slot-1], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

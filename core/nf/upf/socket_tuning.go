// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Runtime socket / TUN tuning for the UPF dataplane.
// Mirrors Python upf_net_setup.py. Requires root for sysctl + ip link.
package upf

import (
	"fmt"
	"os/exec"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// applySocketTuning applies runtime socket/TUN tuning via sysctl and ip link.
func applySocketTuning(log *logger.Logger, tunQLen, gtpuRcvBuf, gtpuSndBuf, netdevBacklog int64) {
	run := func(cmd string) {
		if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
			log.Debugf("tuning: %s: %v (%s)", cmd, err, out)
		}
	}

	// IP forwarding
	run("sysctl -qw net.ipv4.ip_forward=1")

	// TUN txqueuelen
	if tunQLen > 0 {
		run(fmt.Sprintf("ip link set mmttun txqueuelen %d", tunQLen))
		log.Infof("mmttun txqueuelen=%d", tunQLen)
	}

	// GTP-U socket buffers
	if gtpuRcvBuf > 0 {
		bytes := gtpuRcvBuf * 1024 * 1024
		run(fmt.Sprintf("sysctl -qw net.core.rmem_default=%d", bytes))
		run(fmt.Sprintf("sysctl -qw net.core.rmem_max=%d", bytes))
		log.Infof("GTP-U rcvbuf=%dMB", gtpuRcvBuf)
	}
	if gtpuSndBuf > 0 {
		bytes := gtpuSndBuf * 1024 * 1024
		run(fmt.Sprintf("sysctl -qw net.core.wmem_default=%d", bytes))
		run(fmt.Sprintf("sysctl -qw net.core.wmem_max=%d", bytes))
		log.Infof("GTP-U sndbuf=%dMB", gtpuSndBuf)
	}

	// Netdev backlog
	if netdevBacklog > 0 {
		run(fmt.Sprintf("sysctl -qw net.core.netdev_max_backlog=%d", netdevBacklog))
		log.Infof("netdev_max_backlog=%d", netdevBacklog)
	}
}

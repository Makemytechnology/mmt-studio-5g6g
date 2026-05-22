// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// net_setup.go — Auto-configure host networking for UPF based on APN/DNN pools.
//
// Go port of nf/upf/upf_net_setup.py. Reads APN IP pools from the DB
// (apn_config + apn_ip_pools tables) and configures the host networking:
//   1. Enable IP forwarding (sysctl)
//   2. Ensure TUN device is up, apply txqueuelen
//   3. Add gateway IP (.1) on TUN for each APN CIDR
//   4. Add routes for each APN CIDR → TUN
//   5. iptables MASQUERADE for each APN CIDR on the external interface
//   6. iptables FORWARD rules to allow traffic
//
// All iptables rules are tagged with comment "sacore-upf" so they can be
// cleanly removed on shutdown without nuking other rules.
//
// Must be run as root (sudo). Called once at UPF startup after DPDK PktIO.
package upf

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

const (
	iptablesComment = "sacore-upf"
	defaultTunName  = "upfgtp"
)

// apnPool holds one APN name and its associated IP pool CIDRs from the DB.
type apnPool struct {
	APNName string
	IPPools []string // CIDR strings, e.g. "10.45.0.0/16"
}

// NetSetup manages UPF host network configuration (TUN, routes, NAT).
type NetSetup struct {
	log      *logger.Logger
	tunName  string
	extIface string
}

// NewNetSetup creates a NetSetup instance.
func NewNetSetup(tunName string) *NetSetup {
	if tunName == "" {
		tunName = defaultTunName
	}
	return &NetSetup{
		log:     logger.Get("upf.net_setup"),
		tunName: tunName,
	}
}

// run executes a shell command and returns (returncode, stdout).
func (ns *NetSetup) run(cmd string, check bool) (int, string) {
	out, err := exec.Command("sh", "-c", cmd).CombinedOutput()
	stdout := strings.TrimSpace(string(out))
	if err != nil {
		if check {
			ns.log.Warnf("Command failed: %s", cmd)
			if stdout != "" {
				ns.log.Warnf("    stderr: %s", stdout)
			}
		}
		// Extract exit code if possible
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), stdout
		}
		return 1, stdout
	}
	return 0, stdout
}

// detectExternalInterface finds the default external interface (the one with the default route).
func (ns *NetSetup) detectExternalInterface() string {
	rc, out := ns.run("ip route show default", false)
	if rc == 0 && out != "" {
		// "default via x.x.x.x dev eth0 ..." → extract dev name
		parts := strings.Fields(out)
		for i, p := range parts {
			if p == "dev" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	// Fallback: find first non-lo interface that's UP
	rc, out = ns.run("ip -o link show up", false)
	if rc == 0 {
		for _, line := range strings.Split(out, "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) >= 2 {
				iface := strings.TrimSpace(parts[1])
				iface = strings.SplitN(iface, "@", 2)[0]
				if iface != "lo" && !strings.HasPrefix(iface, "upf") {
					return iface
				}
			}
		}
	}
	return "eth0"
}

// loadAPNPools reads APN IP pools from the DB (apn_config JOIN apn_ip_pools).
func loadAPNPools(log *logger.Logger) []apnPool {
	db, err := engine.Open()
	if err != nil {
		log.Errorf("Failed to open DB for APN pools: %v", err)
		return nil
	}
	// Do NOT close — engine.Open() returns a shared singleton

	rows, err := db.Query(`
		SELECT a.apn_name, p.cidr
		FROM apn_config a
		JOIN apn_ip_pools p ON p.apn_id = a.id
		ORDER BY a.apn_name, p.id
	`)
	if err != nil {
		log.Errorf("Failed to query APN pools: %v", err)
		return nil
	}
	defer rows.Close()

	apnMap := make(map[string]*apnPool)
	var order []string
	for rows.Next() {
		var name, cidr string
		if err := rows.Scan(&name, &cidr); err != nil {
			log.Warnf("Row scan error: %v", err)
			continue
		}
		if _, ok := apnMap[name]; !ok {
			apnMap[name] = &apnPool{APNName: name}
			order = append(order, name)
		}
		apnMap[name].IPPools = append(apnMap[name].IPPools, cidr)
	}

	result := make([]apnPool, 0, len(order))
	for _, name := range order {
		result = append(result, *apnMap[name])
	}
	return result
}

// gatewayIP returns the .1 address and prefix length for a CIDR string.
// E.g. "10.45.0.0/16" → ("10.45.0.1", 16).
func gatewayIP(cidr string) (string, int, error) {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	ip := ipNet.IP.To4()
	if ip == nil {
		return "", 0, fmt.Errorf("not an IPv4 CIDR: %s", cidr)
	}
	// .1 = network address + 1
	gwIP := make(net.IP, len(ip))
	copy(gwIP, ip)
	gwIP[3] |= 1
	ones, _ := ipNet.Mask.Size()
	return gwIP.String(), ones, nil
}

// dbSCTPPort returns the operator-configured NGAP transport port from
// network_config.sctp_port. Falls back to the IANA default 38412
// (TS 38.412 §7) when the row/column isn't readable yet — keeps the
// firewall logic functional on a fresh DB before InitContextFromDB.
func dbSCTPPort() int {
	db, err := engine.Open()
	if err != nil {
		return 38412
	}
	var p int
	if err := db.QueryRow(`SELECT sctp_port FROM network_config WHERE id=1`).Scan(&p); err != nil || p < 1 || p > 65535 {
		return 38412
	}
	return p
}

// openFirewallPorts ensures the kernel firewall permits NGAP (SCTP)
// and GTP-U (UDP 2152) inbound. Ubuntu's default UFW posture is
// default-deny; on such hosts the UPF socket binds cleanly but every
// GTP-U datagram is silently dropped before recvfrom() sees it, so
// /proc/net/udp shows rx_queue=0 drops=0 and ul_pkts stays at zero.
//
// Handles both ufw and firewalld. No-op on hosts with neither active.
// All rules tagged with the UPF iptables comment "sacore-upf" so the
// teardown path removes them cleanly.
func (ns *NetSetup) openFirewallPorts() {
	sctpPort := dbSCTPPort()
	sctpRule := fmt.Sprintf("%d/sctp", sctpPort)
	// ── UFW (Ubuntu/Debian) ──
	if rc, out := ns.run("systemctl is-active ufw 2>/dev/null", false); rc == 0 && strings.TrimSpace(out) == "active" {
		for _, p := range []struct {
			rule string
			desc string
		}{
			{"2152/udp", "GTP-U (N3)"},
			{sctpRule, "NGAP"},
		} {
			ns.run(fmt.Sprintf("ufw allow %s comment 'sacore-upf %s'", p.rule, p.desc), false)
			ns.log.Infof("UFW: allowed %s (%s)", p.rule, p.desc)
		}
		// ufw reload is expensive and sometimes hangs on VMs — the allow
		// rule takes effect immediately for new connections without it.
		return
	}

	// ── firewalld (RHEL/Fedora) ──
	if rc, out := ns.run("systemctl is-active firewalld 2>/dev/null", false); rc == 0 && strings.TrimSpace(out) == "active" {
		ns.run("firewall-cmd --add-port=2152/udp", false)
		ns.run(fmt.Sprintf("firewall-cmd --add-port=%s", sctpRule), false)
		ns.log.Infof("firewalld: allowed 2152/udp (GTP-U) and %s (NGAP)", sctpRule)
		return
	}

	// ── raw iptables INPUT (no firewall manager) ──
	// Add ACCEPT rules tagged with our comment so INPUT-chain policies
	// that default to DROP still let GTP-U and NGAP through.
	for _, r := range []struct {
		args string
		desc string
	}{
		{"-p udp --dport 2152", "GTP-U (N3)"},
		{fmt.Sprintf("-p sctp --dport %d", sctpPort), "NGAP"},
	} {
		// Check first — avoids duplicate rules across restarts.
		if rc, _ := ns.run(fmt.Sprintf("iptables -C INPUT %s -j ACCEPT -m comment --comment %s 2>/dev/null", r.args, iptablesComment), false); rc == 0 {
			continue
		}
		if rc, _ := ns.run(fmt.Sprintf("iptables -I INPUT 1 %s -j ACCEPT -m comment --comment %s", r.args, iptablesComment), false); rc == 0 {
			ns.log.Infof("iptables INPUT ACCEPT: %s (%s)", r.args, r.desc)
		}
	}
}

// Setup configures host networking for UPF based on APN pools.
// Must be run as root.
func (ns *NetSetup) Setup() bool {
	if os.Geteuid() != 0 {
		ns.log.Error("UPF network setup requires root. Run with sudo.")
		return false
	}

	if ns.extIface == "" {
		ns.extIface = ns.detectExternalInterface()
	}

	apns := loadAPNPools(ns.log)
	if len(apns) == 0 {
		ns.log.Error("No APN pools found in DB. Nothing to configure.")
		return false
	}

	// Collect all (apn_name, cidr) pairs
	type apnCIDR struct {
		apnName string
		cidr    string
	}
	var allCIDRs []apnCIDR
	for _, apn := range apns {
		for _, cidr := range apn.IPPools {
			allCIDRs = append(allCIDRs, apnCIDR{apn.APNName, cidr})
		}
	}

	ns.log.Infof("UPF network setup: ext_iface=%s, tun=%s", ns.extIface, ns.tunName)
	names := make([]string, len(apns))
	for i, a := range apns {
		names[i] = a.APNName
	}
	ns.log.Infof("APNs: %s", strings.Join(names, ", "))
	for _, ac := range allCIDRs {
		ns.log.Infof("    %s: %s", ac.apnName, ac.cidr)
	}

	ok := true

	// ── 1. Enable IP forwarding ──
	rc, _ := ns.run("sysctl -w net.ipv4.ip_forward=1", true)
	if rc == 0 {
		ns.log.Info("IP forwarding enabled")
	} else {
		ok = false
	}

	// ── 1a. Firewall: open NGAP (SCTP 38412) + GTP-U (UDP 2152) ──
	// Mirrors what Python's run.sh and setup scripts assume (Ubuntu UFW
	// default-deny silently drops inbound 2152 before it reaches the UPF
	// socket — hours-long mystery when no counter increments).
	ns.openFirewallPorts()

	// ── 2. Ensure TUN device exists and is up ──
	rc, _ = ns.run(fmt.Sprintf("ip link show %s", ns.tunName), false)
	if rc != 0 {
		ns.log.Infof("TUN device '%s' will be created by UPF data plane", ns.tunName)
	} else {
		ns.run(fmt.Sprintf("ip link set %s up", ns.tunName), false)
	}

	// Apply configured TUN txqueuelen from infra_config
	func() {
		db, err := engine.Open()
		if err != nil {
			ns.log.Debugf("txqueuelen tune skipped: %v", err)
			return
		}
		// Do NOT close — engine.Open() returns a shared singleton
		var qlen int64
		err = db.QueryRow(`SELECT COALESCE(tun_txqueuelen, 1000) FROM infra_config LIMIT 1`).Scan(&qlen)
		if err != nil {
			ns.log.Debugf("txqueuelen tune skipped: %v", err)
			return
		}
		if qlen <= 0 {
			qlen = 1000
		}
		ns.run(fmt.Sprintf("ip link set %s txqueuelen %d", ns.tunName, qlen), false)
		ns.log.Infof("%s txqueuelen set to %d", ns.tunName, qlen)
	}()

	// ── 3+4. TUN-dependent steps (address aliases + routes) are done
	// in ApplyTunAddresses() AFTER bridge.PktIOInit() creates mmttun.
	// At Setup() time the TUN doesn't exist yet — running `ip addr add`
	// on it would silently fail and leave the IMS APN (10.46.0.0/16)
	// without a kernel route, which is exactly the symptom that broke
	// SIP REGISTER replies (the 401 from CSCF couldn't find its way
	// back through the GTP-U pipeline because `mmttun` only carried
	// 10.45.0.1/16 from DPDK's hardcoded PktIOInit).

	// ── 5. iptables: MASQUERADE for each APN CIDR ──
	for _, ac := range allCIDRs {
		// Check if rule already exists
		rc, _ = ns.run(fmt.Sprintf(
			"iptables -t nat -C POSTROUTING -s %s -o %s -j MASQUERADE -m comment --comment %s",
			ac.cidr, ns.extIface, iptablesComment,
		), false)
		if rc == 0 {
			ns.log.Infof("NAT MASQUERADE for %s already exists", ac.cidr)
		} else {
			rc, _ = ns.run(fmt.Sprintf(
				"iptables -t nat -A POSTROUTING -s %s -o %s -j MASQUERADE -m comment --comment %s",
				ac.cidr, ns.extIface, iptablesComment,
			), true)
			if rc == 0 {
				ns.log.Infof("NAT MASQUERADE: %s -> %s (APN: %s)", ac.cidr, ns.extIface, ac.apnName)
			}
		}
	}

	// ── 6. iptables: FORWARD rules ──
	// Allow forwarded traffic from/to TUN
	type fwdRule struct {
		direction string
		args      string
	}
	fwdRules := []fwdRule{
		{"in", fmt.Sprintf("-i %s -o %s", ns.tunName, ns.extIface)},
		{"out", fmt.Sprintf("-i %s -o %s -m state --state RELATED,ESTABLISHED", ns.extIface, ns.tunName)},
	}
	for _, fr := range fwdRules {
		rc, _ = ns.run(fmt.Sprintf(
			"iptables -C FORWARD %s -j ACCEPT -m comment --comment %s",
			fr.args, iptablesComment,
		), false)
		if rc == 0 {
			ns.log.Infof("FORWARD %s rule already exists", fr.direction)
		} else {
			rc, _ = ns.run(fmt.Sprintf(
				"iptables -A FORWARD %s -j ACCEPT -m comment --comment %s",
				fr.args, iptablesComment,
			), true)
			if rc == 0 {
				ns.log.Infof("FORWARD %s: %s", fr.direction, fr.args)
			}
		}
	}

	ns.log.Info("UPF network setup complete")
	return ok
}

// PrimaryTunIP returns the first APN's gateway IP (.1 of the first
// pool of the first APN) — used as the TUN's primary address by the
// DPDK PktIOInit. Returns "" when no APNs are configured.
//
// Drives the source-address selection on packets the core
// originates toward UEs (kernel longest-prefix-match still picks the
// matching alias for non-primary APNs once ApplyTunAddresses() has
// run, so this is mainly cosmetic — but keeping it APN-driven
// instead of hardcoded means an operator-managed APN list fully
// describes the UPF's IPv4 surface.)
func (ns *NetSetup) PrimaryTunIP() string {
	apns := loadAPNPools(ns.log)
	for _, apn := range apns {
		for _, cidr := range apn.IPPools {
			gw, _, err := gatewayIP(cidr)
			if err == nil {
				return gw
			}
		}
	}
	return ""
}

// ApplyTunAddresses attaches the per-APN gateway addresses and
// routes onto ns.tunName. Must be called AFTER the data plane has
// created the TUN device (bridge.PktIOInit on the DPDK path,
// equivalent path on the pure-Go fallback).
//
// At Setup() time the TUN typically doesn't exist yet, so the
// address/route adds done here used to live in Setup() and would
// silently fail. That left UEs on any APN whose CIDR didn't match
// the DPDK-hardcoded primary IP (10.45.0.1/16) unreachable on the
// downlink — e.g. the IMS APN at 10.46.0.0/16, where the CSCF's
// 401 reply had no route to the UE TUN.
//
// Idempotent: skips additions that are already present (matches the
// "already has %s/%d" / "Route %s -> %s already exists" log lines
// from the original Setup()).
func (ns *NetSetup) ApplyTunAddresses() {
	if os.Geteuid() != 0 {
		ns.log.Warnf("Apply TUN addresses skipped — needs root")
		return
	}
	if rc, _ := ns.run(fmt.Sprintf("ip link show %s", ns.tunName), false); rc != 0 {
		ns.log.Warnf("Apply TUN addresses skipped — %s does not exist", ns.tunName)
		return
	}

	apns := loadAPNPools(ns.log)
	for _, apn := range apns {
		for _, cidr := range apn.IPPools {
			gwIP, prefix, err := gatewayIP(cidr)
			if err != nil {
				ns.log.Warnf("Skipping CIDR %s: %v", cidr, err)
				continue
			}

			// Address alias.
			_, out := ns.run(fmt.Sprintf("ip addr show dev %s 2>/dev/null", ns.tunName), false)
			if strings.Contains(out, gwIP) {
				ns.log.Infof("%s already has %s/%d (APN: %s)", ns.tunName, gwIP, prefix, apn.APNName)
			} else if rc, _ := ns.run(fmt.Sprintf("ip addr add %s/%d dev %s", gwIP, prefix, ns.tunName), true); rc == 0 {
				ns.log.Infof("Added %s/%d to %s (APN: %s)", gwIP, prefix, ns.tunName, apn.APNName)
			}

			// Kernel-auto route covers the prefix already once the
			// address is attached (proto kernel scope link), but we
			// double-check and add an explicit route if missing —
			// covers the case where the address was already assigned
			// but the prefix route was deleted.
			_, out = ns.run(fmt.Sprintf("ip route show %s", cidr), false)
			if strings.Contains(out, ns.tunName) {
				ns.log.Infof("Route %s -> %s already exists (APN: %s)", cidr, ns.tunName, apn.APNName)
			} else if rc, _ := ns.run(fmt.Sprintf("ip route add %s dev %s", cidr, ns.tunName), true); rc == 0 {
				ns.log.Infof("Route added: %s -> %s (APN: %s)", cidr, ns.tunName, apn.APNName)
			}
		}
	}
}

// Cleanup removes all UPF iptables rules and the TUN device.
// Only removes rules tagged with iptablesComment, safe to run.
func (ns *NetSetup) Cleanup() {
	if os.Geteuid() != 0 {
		ns.log.Error("UPF network teardown requires root.")
		return
	}

	if ns.extIface == "" {
		ns.extIface = ns.detectExternalInterface()
	}

	ns.log.Info("UPF network teardown...")

	// Remove iptables rules with our comment
	for _, table := range []string{"nat", "filter"} {
		rc, out := ns.run(
			fmt.Sprintf("iptables -t %s -S 2>/dev/null | grep '%s'", table, iptablesComment),
			false,
		)
		if rc == 0 && out != "" {
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if line == "" {
					continue
				}
				// Convert -A to -D to delete
				delRule := strings.Replace(line, "-A ", "-D ", 1)
				ns.run(fmt.Sprintf("iptables -t %s %s", table, delRule), false)
				ns.log.Infof("Removed: iptables -t %s %s", table, delRule)
			}
		}
	}

	// Remove UFW rules we added (tagged by our comment). Use the
	// DB-configured SCTP port so we tear down the same rule we added.
	sctpRule := fmt.Sprintf("%d/sctp", dbSCTPPort())
	if rc, out := ns.run("systemctl is-active ufw 2>/dev/null", false); rc == 0 && strings.TrimSpace(out) == "active" {
		ns.run("ufw delete allow 2152/udp", false)
		ns.run(fmt.Sprintf("ufw delete allow %s", sctpRule), false)
		ns.log.Infof("UFW: removed 2152/udp and %s allow rules", sctpRule)
	}
	// Remove firewalld runtime ports (permanent rules were never added)
	if rc, out := ns.run("systemctl is-active firewalld 2>/dev/null", false); rc == 0 && strings.TrimSpace(out) == "active" {
		ns.run("firewall-cmd --remove-port=2152/udp", false)
		ns.run(fmt.Sprintf("firewall-cmd --remove-port=%s", sctpRule), false)
	}

	// Remove TUN device
	rc, _ := ns.run(fmt.Sprintf("ip link show %s", ns.tunName), false)
	if rc == 0 {
		ns.run(fmt.Sprintf("ip link delete %s", ns.tunName), false)
		ns.log.Infof("Removed TUN device: %s", ns.tunName)
	}

	ns.log.Info("UPF network teardown complete")
}

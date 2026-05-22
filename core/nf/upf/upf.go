// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package upf — User Plane Function control plane (TS 29.244 / TS 23.501 §6.3.3).
//
// The UPF splits into two halves:
//   - Dataplane (C/DPDK) — 10 .c files, stays C, linked via cgo on Linux
//   - Control plane (this package) — Go session/rule management
//
// The CgoBridge interface in cgo_bridge.go defines the Go↔C boundary.
// DPDK + hugepages is the only supported dataplane; if rte_eal_init
// fails the UPF aborts with an actionable error rather than silently
// degrading to a no-policy fallback.
package upf

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// isWSL2 reports whether this process is running under WSL2 (Microsoft
// kernel in a Hyper-V utility VM). Used to emit WSL2-specific hugepage
// remediation steps when DPDK init fails — wsl.conf + sysctl.d drop-in,
// not just `sysctl -w`. /proc/version on WSL2 contains "microsoft"
// (case-insensitive); on bare metal it doesn't.
func isWSL2() bool {
	b, _ := os.ReadFile("/proc/version")
	return bytes.Contains(bytes.ToLower(b), []byte("microsoft"))
}

// ipStringToHostU32 parses a dotted-quad IPv4 string into a HOST-byte-order
// uint32 (numerical value, MSB-first in memory when viewed logically). This
// matches the convention used by the C dataplane — wire conversion happens
// via htonl() at the socket boundary. Returns 0 for parse failures.
// binary.BigEndian.Uint32 produces the same value on LE and BE hosts, making
// the caller architecture-agnostic.
func ipStringToHostU32(s string) uint32 {
	ip := net.ParseIP(s)
	if ip == nil {
		return 0
	}
	v4 := ip.To4()
	if v4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(v4)
}

// Session mirrors the C upf_session_t for the control plane.
type Session struct {
	IMSI         string
	PDUSessionID uint8
	DNN          string
	SST          uint8
	SD           uint32
	UEAddr       uint32

	// PDNType per TS 29.244 §8.2.79 Table 8.2.79-1:
	//   1=IPv4, 2=IPv6, 3=IPv4v6, 4=Non-IP, 5=Ethernet
	// Same encoding as TS 24.501 §9.11.4.11 PDU session type, so
	// the SMF can pass the value through directly. 0 means
	// "not set" — bridge.SessionCreate skips the PDNType IE.
	PDNType uint8

	PDRs []PDR
	FARs []FAR
	QERs []QER
	URRs []URR
}

type PDR struct {
	PDRID      uint16
	Precedence uint32
	PDISource  uint8
	QFI        uint8
	FARID      uint32
	QERID      uint32
	URRID      uint32
	SDFRules   string

	// PDI fast-path match keys carried in the PFCP wire IE PDI per
	// TS 29.244 v19.5.0 §7.5.2.2:
	//   - PDISource=1 (DL, src=core): UE IP Address IE (§8.2.62)
	//     populated with V4=1 + S/D=1 + UEIPv4 — the destination
	//     UE address the dataplane matches DL packets against.
	//   - PDISource=0 (UL, src=access): F-TEID IE (§8.2.3)
	//     populated with V4=1 + TEID + N3IPv4 — the UPF's GTP-U
	//     ingress address the gNB targets.
	// Zero means "no fast-path key" — leaves PDI matching to
	// SDF Filter alone (slow path / SDF-only PDR).
	UEIPv4 uint32 // DL match: UE's IPv4 destination address
	TEID   uint32 // UL match: UPF-allocated GTP-U TEID
	N3IPv4 uint32 // UL match: UPF's N3 IPv4 (paired with TEID in F-TEID IE)
}

type FAR struct {
	FARID    uint32
	Action   uint8
	DstIface uint8
	TEID     uint32
	PeerAddr uint32
	PeerPort uint16
}

type QER struct {
	QERID  uint32
	QFI    uint8
	GateUL uint8
	GateDL uint8
	MBRUL  uint64
	MBRDL  uint64
	GBRUL  uint64
	GBRDL  uint64
}

type URR struct {
	URRID         uint32
	MeasMethod    uint8
	ReportTrigger uint8
	VolThreshUL   uint64
	VolThreshDL   uint64
	TimeThresh    uint32
	VolUL, VolDL  uint64
	PktUL, PktDL  uint64
}

// IOStats mirrors upf_dp_io_stats_t from the C API.
type IOStats struct {
	ULPkts     uint64 `json:"ul_pkts"`
	ULBytes    uint64 `json:"ul_bytes"`
	DLPkts     uint64 `json:"dl_pkts"`
	DLBytes    uint64 `json:"dl_bytes"`
	ULDropped  uint64 `json:"ul_dropped"`
	DLDropped  uint64 `json:"dl_dropped"`
	ULNoSess   uint64 `json:"ul_no_session"`
	DLNoSess   uint64 `json:"dl_no_session"`
	ULMetered  uint64 `json:"ul_metered"`
	DLMetered  uint64 `json:"dl_metered"`
	GTPUErrors uint64 `json:"gtpu_errors"`
}

type sessionKey struct {
	imsi string
	id   uint8
}

// Manager is the UPF control-plane session manager.
type Manager struct {
	mu       sync.RWMutex
	sessions map[sessionKey]*Session
	stats    IOStats
	running  bool
	log      *logger.Logger
	netSetup *NetSetup

	// reportCtx / reportCancel control the §7.5.8 report consumer
	// goroutine started from Init. Cancelling stops the consumer
	// at the next tick. See nf/upf/report.go.
	reportCtx    context.Context
	reportCancel context.CancelFunc
}

var Default = NewManager()

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[sessionKey]*Session),
		log:      logger.Get("upf"),
	}
}

func (m *Manager) Init() error {
	cfg, err := crud.GetInfraConfig()
	if err != nil {
		return err
	}
	m.log.Infof("UPF init mode=%s max_sessions=%v dpdk_mem=%vMB",
		sv(cfg, "upf_mode"), iv(cfg, "upf_max_sessions"), iv(cfg, "upf_dpdk_mem_mb"))

	// Read UPF tuning parameters from infra_config
	upfMode := sv(cfg, "upf_mode") // "socket" or "pmd"
	maxSess := uint32(iv(cfg, "upf_max_sessions"))
	dpdkMem := iv(cfg, "upf_dpdk_mem_mb")
	mbufPool := uint32(iv(cfg, "upf_mbuf_pool_size"))
	rxRing := uint16(iv(cfg, "upf_rx_ring_size"))
	txRing := uint16(iv(cfg, "upf_tx_ring_size"))
	logLevel := sv(cfg, "dpdk_log_level")
	n3Device := sv(cfg, "pmd_n3_device")
	n6Device := sv(cfg, "pmd_n6_device")
	tunQLen := iv(cfg, "tun_txqueuelen")
	gtpuRcvBuf := iv(cfg, "gtpu_rcvbuf_mb")
	gtpuSndBuf := iv(cfg, "gtpu_sndbuf_mb")
	netdevBacklog := iv(cfg, "sysctl_netdev_backlog")

	if maxSess == 0 {
		maxSess = 8192
	}
	if dpdkMem == 0 {
		dpdkMem = 256
	}

	// Auto-scale DPDK memory to fit configured max_sessions.
	// Each session needs ~20 KB (14.3 KB struct + meters + hash overhead).
	// Add 64 MB headroom for EAL internals + PktIO hash tables.
	minMem := int64(maxSess)*20/1024 + 64
	if dpdkMem < minMem {
		m.log.Infof("DPDK memory %dMB too small for %d sessions, auto-scaling to %dMB",
			dpdkMem, maxSess, minMem)
		dpdkMem = minMem
	}

	if logLevel == "" {
		logLevel = "INFO"
	}
	if n3Device == "" {
		n3Device = "net_tap0"
	}
	if n6Device == "" {
		n6Device = "net_tap1"
	}

	// DPDK log level mapping: ERROR=1, WARNING=2, INFO=4, DEBUG=7
	dpdkLogLvl := map[string]int{"ERROR": 1, "WARNING": 2, "INFO": 4, "DEBUG": 7}
	lvl := dpdkLogLvl[logLevel]
	if lvl == 0 {
		lvl = 4
	}

	// Configure host networking (TUN addrs, routes, NAT) BEFORE DPDK init.
	// exec.Command uses fork(), and DPDK's madvise(MADV_DONTFORK) on
	// hugepage memory causes the parent's mappings to become invalid
	// after fork on some Linux kernels. Do all fork-based ops first.
	m.netSetup = NewNetSetup("mmttun")
	m.netSetup.Setup() // non-fatal — logs warnings
	applySocketTuning(m.log, tunQLen, gtpuRcvBuf, gtpuSndBuf, netdevBacklog)

	// Initialize C/DPDK dataplane if bridge is available
	if bridge != nil {
		// Clean stale DPDK runtime files from previous crashed runs.
		// Matches Python: 'Cleaned N stale hugepage mappings'. Without this
		// rte_eal_init can return phantom addresses from stale /dev/hugepages
		// files, producing SIGSEGVs on first allocation.
		os.RemoveAll("/var/run/dpdk")
		stale := 0
		if entries, err := filepath.Glob("/dev/hugepages/rtemap_*"); err == nil {
			for _, f := range entries {
				if os.Remove(f) == nil {
					stale++
				}
			}
		}
		if stale > 0 {
			m.log.Infof("Cleaned %d stale hugepage mappings", stale)
		}

		// Apply compile-time tuning BEFORE init (C rejects after init)
		if maxSess >= 256 {
			_ = bridge.SetMaxSessions(maxSess)
		}
		if mbufPool > 0 || rxRing > 0 || txRing > 0 {
			_ = bridge.SetPMDTuning(mbufPool, rxRing, txRing)
		}

		// Build EAL args based on UPF mode
		// Resolve DPDK driver directory relative to the binary location
		exePath, _ := os.Executable()
		baseDir := filepath.Dir(exePath)
		dpdkDriverDir := filepath.Join(baseDir, "libs", "dpdk-25.11", "build", "drivers")
		var argv []string
		if upfMode == "pmd" {
			// PMD fast path: TAP PMDs for N3 (GTP-U) + N6 (core)
			argv = []string{"upf",
				"-d", dpdkDriverDir + "/librte_net_tap.so",
				"-d", dpdkDriverDir + "/librte_net_af_packet.so",
				"-d", dpdkDriverDir + "/librte_mempool_ring.so",
				fmt.Sprintf("--vdev=%s,iface=upf_n3", n3Device),
				fmt.Sprintf("--vdev=%s,iface=upf_n6", n6Device),
				"-m", fmt.Sprintf("%d", dpdkMem),
				fmt.Sprintf("--log-level=lib.eal:%d", lvl),
				fmt.Sprintf("--log-level=user1:%d", lvl),
			}
		} else {
			// Socket/TUN mode (default): hugepages for rte_hash +
			// rte_meter; GTP-U UDP socket + TUN for packet I/O.
			argv = []string{"upf",
				"-d", dpdkDriverDir + "/librte_mempool_ring.so",
				"-m", fmt.Sprintf("%d", dpdkMem),
				"--no-pci",
				fmt.Sprintf("--log-level=lib.eal:%d", lvl),
				fmt.Sprintf("--log-level=user1:%d", lvl),
			}
		}

		m.log.Infof("DPDK EAL args: %v", argv)
		if err := bridge.Init(len(argv), argv); err != nil {
			// DPDK + hugepages is the only supported dataplane. The
			// historical pure-Go fallback (nf/upf/godp) was removed
			// because it silently passed AMBR / metering / URR /
			// gating tests that need real C-side enforcement — a
			// vacuous-PASS footgun. If EAL init failed it's almost
			// certainly a missing hugepage allocation on the host;
			// give the operator an actionable, copy-pasteable fix
			// and abort instead of degrading invisibly.
			m.log.Errorf("DPDK init failed: %v", err)
			m.log.Errorf("hugepages required. On the host: sudo sysctl -w vm.nr_hugepages=1024")
			if isWSL2() {
				m.log.Errorf("WSL2 detected — enable systemd in /etc/wsl.conf [boot] then persist via /etc/sysctl.d/99-hugepages.conf, then wsl --shutdown")
			}
			return fmt.Errorf("dpdk init failed (hugepages?): %w", err)
		} else {
			// Pick the primary TUN IP from APN config — first
			// configured APN's gateway address. Operator overrides
			// flow through the APN form (apn_config + apn_ip_pools
			// in the DB), so adding/removing APNs here actually
			// reshapes mmttun's primary IP across restarts. ALL
			// other APN gateways become aliases via
			// ApplyTunAddresses() after this block.
			primaryTunIP := m.netSetup.PrimaryTunIP()
			if primaryTunIP == "" {
				m.log.Warnf("No APN pools configured — DPDK PktIO will skip IP attach")
			} else {
				m.log.Infof("DPDK PktIO TUN primary IP: %s (from APN config)", primaryTunIP)
			}

			if err := bridge.PktIOInit("0.0.0.0", 2152, "mmttun", primaryTunIP); err != nil {
				m.log.Warnf("PktIO init: %v", err)
			} else {
				go func() {
					if err := bridge.PktIORun(); err != nil {
						m.log.Errorf("PktIO run: %v", err)
					}
				}()
				m.log.Info("UPF DPDK dataplane started")

				// PktIOInit just created mmttun and assigned the
				// primary IP from the first APN. Attach gateway
				// aliases + routes for any remaining APNs so DL
				// traffic to UEs on those subnets has a kernel
				// route into mmttun.
				m.netSetup.ApplyTunAddresses()
			}
		}
	} else {
		// Non-Linux build paths leave `bridge` nil because cgo_bridge_linux.go's
		// init() is the only assignment. We don't support running the UPF
		// without a DPDK-backed bridge — fail fast so the operator notices
		// instead of silently bringing up a no-op dataplane.
		return fmt.Errorf("upf: no dataplane bridge available (build without cgo_bridge_linux?)")
	}

	// Start the §7.5.8 PFCP Session Report Request consumer
	// goroutine. It drains typed Report records from the C rte_ring
	// and dispatches to handlers registered via
	// RegisterReportHandler (e.g. DLDR → session.HandleDLData
	// Notification; Usage → CHF; ErrInd → session reset). When the
	// DPDK producers haven't been wired yet the consumer harmlessly
	// drains an empty ring. See nf/upf/report.go + dataplane/
	// include/upf_report.h for the full design.
	m.reportCtx, m.reportCancel = context.WithCancel(context.Background())
	StartReportConsumer(m.reportCtx)

	// Periodic per-session UL/DL stats drain to sacore.log. Without
	// this the only points-of-truth for "did data flow?" are the
	// final URR snapshot at §7.5.6 deletion (already logged) and
	// the C dataplane RTE_LOG (which goes to stderr, not the log
	// file). A live drain every tick bridges the gap so operators
	// can confirm UL/DL counters ramping in real time — the natural
	// signal for "no throughput on call N" investigations. Counters
	// are TS 29.244 v19.5.0 §8.2.41 Volume Measurement counters
	// pulled via bridge.GetURRStats from the C side's per-URR
	// accumulators.
	go m.runPeriodicStatsLogger(m.reportCtx)

	m.running = true
	return nil
}

// runPeriodicStatsLogger walks every active session every tick and
// logs cumulative UL/DL bytes+pkts from the first URR plus delta
// since the last tick. Quiet (no log line) when both delta values
// are zero — operators only see chatter when traffic is moving or
// when something just stopped moving.
func (m *Manager) runPeriodicStatsLogger(ctx context.Context) {
	const tick = 10 * time.Second
	t := time.NewTicker(tick)
	defer t.Stop()

	type prior struct {
		volUL, volDL, pktUL, pktDL uint64
	}
	priorBy := map[sessionKey]prior{}
	statsLog := logger.Get("upf.stats")

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if bridge == nil {
			continue
		}
		// Snapshot session keys under read lock; release before the
		// per-session GetURRStats calls so the bridge dispatch isn't
		// blocked on the manager mutex.
		m.mu.RLock()
		keys := make([]sessionKey, 0, len(m.sessions))
		urrs := make(map[sessionKey]uint32, len(m.sessions))
		for k, s := range m.sessions {
			keys = append(keys, k)
			if len(s.URRs) > 0 {
				urrs[k] = s.URRs[0].URRID
			}
		}
		m.mu.RUnlock()

		for _, k := range keys {
			urrID, ok := urrs[k]
			if !ok || urrID == 0 {
				continue
			}
			volUL, volDL, pktUL, pktDL, err := bridge.GetURRStats(k.imsi, k.id, urrID)
			if err != nil {
				continue
			}
			p := priorBy[k]
			// Detect counter regression. Same (imsi, pduSessID) key
			// can survive across §7.5.6 Session Deletion + §7.5.2
			// re-establish (e.g., the §6.4.1.7 item-(c) duplicate
			// handler), and the C-side per-URR accumulators are
			// memset to 0 on the new session_pool slot. Without
			// this guard the uint64 (cur - prior) wraps to ~2⁶⁴
			// and the operator sees absurd Δ values.
			if volUL < p.volUL || volDL < p.volDL || pktUL < p.pktUL || pktDL < p.pktDL {
				p = prior{} // baseline reset — treat new totals as fresh session
			}
			dULB := volUL - p.volUL
			dDLB := volDL - p.volDL
			dULP := pktUL - p.pktUL
			dDLP := pktDL - p.pktDL
			priorBy[k] = prior{volUL, volDL, pktUL, pktDL}
			if dULB == 0 && dDLB == 0 && dULP == 0 && dDLP == 0 {
				continue // no traffic this tick — stay quiet
			}
			statsLog.WithIMSI(k.imsi).Infof(
				"pduSessID=%d UL=%d B / %d pkts (Δ%d / Δ%d)  DL=%d B / %d pkts (Δ%d / Δ%d) (TS 29.244 §8.2.41 Volume Measurement)",
				k.id,
				volUL, pktUL, dULB, dULP,
				volDL, pktDL, dDLB, dDLP)
		}
		// Sweep priorBy every tick: drop entries whose key is no
		// longer in the active session set. Cheap (one map walk
		// per tick, # active sessions is the bound) and prevents
		// the table from growing across long uptimes.
		active := make(map[sessionKey]bool, len(keys))
		for _, k := range keys {
			active[k] = true
		}
		for k := range priorBy {
			if !active[k] {
				delete(priorBy, k)
			}
		}
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reportCancel != nil {
		m.reportCancel() // stops the §7.5.8 consumer goroutine
	}
	if m.netSetup != nil {
		m.netSetup.Cleanup()
	}
	if bridge != nil {
		bridge.PktIOStop()
		bridge.Cleanup()
	}
	m.running = false
	m.log.Info("UPF shutdown")
}

func (m *Manager) CreateSession(s *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := sessionKey{s.IMSI, s.PDUSessionID}
	if _, exists := m.sessions[key]; exists {
		return fmt.Errorf("upf: session exists")
	}
	m.sessions[key] = s
	// Push to C dataplane
	if bridge != nil {
		if err := bridge.SessionCreate(s.IMSI, s.PDUSessionID, s.DNN, s.SST, s.SD, s.UEAddr, s.PDNType); err != nil {
			m.log.Warnf("bridge.SessionCreate: %v", err)
		}
	}
	// Detailed "UPF session created" + "UPF DEFAULT bearer" logs are emitted
	// by smf/session/establish.go installUPFRules() under mmt-core.upf.context
	// (matching Python upf_context.py — per-UE flow traceable by module).
	return nil
}

// CommitSession finalises a session whose rules were declared via
// CreateSession + AddPDR/FAR/QER/URR/SetSessionAMBR. For the cgo
// bridge it's a no-op (the C dataplane was mutated synchronously
// at each step). For the PFCP bridge it sends the single §7.5.2
// Establishment Request carrying every Create-* IE.
//
// Callers must invoke this after the rule-add cluster and before
// any post-establishment modification (UpdateFAR, DeactivateDLFAR).
func (m *Manager) CommitSession(imsi string, id uint8) error {
	if bridge != nil {
		return bridge.CommitSession(imsi, id)
	}
	return nil
}

func (m *Manager) DeleteSession(imsi string, id uint8) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionKey{imsi, id})
	if bridge != nil {
		bridge.SessionDelete(imsi, id)
	}
	logger.Get("upf.context").WithIMSI(imsi).Infof("UPF session deleted: %s/%d", imsi, id)
	return nil
}

func (m *Manager) GetSession(imsi string, id uint8) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionKey{imsi, id}]
}

func (m *Manager) AddPDR(imsi string, id uint8, pdr PDR) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return fmt.Errorf("upf: no session")
	}
	s.PDRs = append(s.PDRs, pdr)
	if bridge != nil {
		bridge.AddPDR(imsi, id, pdr.PDRID, pdr.Precedence, pdr.PDISource, pdr.QFI,
			pdr.FARID, pdr.QERID, pdr.URRID, pdr.SDFRules,
			pdr.UEIPv4, pdr.TEID, pdr.N3IPv4)
	}
	return nil
}

func (m *Manager) AddFAR(imsi string, id uint8, far FAR) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return fmt.Errorf("upf: no session")
	}
	s.FARs = append(s.FARs, far)
	if bridge != nil {
		bridge.AddFAR(imsi, id, far.FARID, far.Action, far.DstIface, far.TEID, far.PeerAddr, far.PeerPort, 0)
	}
	return nil
}

func (m *Manager) UpdateFAR(imsi string, id uint8, farID, teid, peerAddr uint32, peerPort uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return fmt.Errorf("upf: no session")
	}
	for i := range s.FARs {
		if s.FARs[i].FARID == farID {
			s.FARs[i].Action = 1
			s.FARs[i].TEID = teid
			s.FARs[i].PeerAddr = peerAddr
			s.FARs[i].PeerPort = peerPort
			if bridge != nil {
				bridge.UpdateFAR(imsi, id, farID, teid, peerAddr, peerPort)
			}
			return nil
		}
	}
	return fmt.Errorf("upf: FAR %d not found", farID)
}

// FAR Apply Action values per TS 29.244 §8.2.26 "Apply Action":
// bit 1 (DROP), 2 (FORW), 3 (BUFF), 4 (NOCP), 5 (DUPL), …
// The UPF bridge encodes these as a single byte — we use 1=FORW,
// 2=BUFF for the two states the control plane currently drives.
const (
	farActionForward = 1 // FORW bit — send packets downstream
	farActionBuffer  = 2 // BUFF bit — hold DL packets at UPF
)

// DeactivateDL implements the UPF side of TS 23.502 §4.2.6 step 6a
// "N4 Session Modification Request (AN or N3 UPF Tunnel Info to be
// removed, Buffering on/off)". Walks the session's DL FARs
// (DstIface=access, bridge convention 0), switches Apply Action from
// FORW to BUFF, and clears the gNB TEID / peer address so the next
// Service Request reactivation (§4.2.3.2 step 12 upCnxState=
// ACTIVATING) can install a fresh tunnel without leftover state.
//
// The dataplane transition is driven by upf_dp_deactivate_dl (C API,
// TS 29.244 §8.2.26 BUFF bit); the control-plane FAR struct is kept
// in sync so /api/upf/sessions reflects the buffering state.
//
// Returns the number of FARs deactivated. Safe to call on a missing
// or already-deactivated session — returns 0 without error so the
// caller (AMF AN Release path) can be idempotent.
func (m *Manager) DeactivateDL(imsi string, id uint8) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return 0, nil
	}
	n := 0
	for i := range s.FARs {
		if s.FARs[i].DstIface != 0 { // DstIface=0 is access (downlink)
			continue
		}
		if s.FARs[i].Action == farActionBuffer && s.FARs[i].TEID == 0 {
			continue // already deactivated
		}
		if bridge != nil {
			if err := bridge.DeactivateDLFAR(imsi, id, s.FARs[i].FARID); err != nil {
				// Log + continue: we still want to update the control-
				// plane FAR so subsequent reads reflect the intended
				// BUFF state. A divergent dataplane is caught by the
				// next PDR/FAR sync or session delete.
				m.log.Warnf("DeactivateDLFAR imsi=%s pduSess=%d farID=%d: %v",
					imsi, id, s.FARs[i].FARID, err)
			}
		}
		s.FARs[i].Action = farActionBuffer
		s.FARs[i].TEID = 0
		s.FARs[i].PeerAddr = 0
		s.FARs[i].PeerPort = 0
		n++
	}
	return n, nil
}

// ActivateDL implements the UPF side of TS 23.502 §4.2.3.2 "UE
// Triggered Service Request" step 12 ("UP activate") — the inverse
// of DeactivateDL. Walks the session's DL FARs, flips Apply Action
// BUFF → FORW, and installs the fresh gNB tunnel info from the new
// InitialContextSetupResponse / PDUSessionResourceSetupResponse.
//
// The dataplane transition is driven by upf_dp_update_far which, per
// the TS 29.244 §5.2.1 logic in upf_dp_api.c, flushes any buffered
// DL packets through the newly-valid tunnel on BUFF→FORW. gtpu is
// the standard N3 peer port (2152, defined in TS 29.281 §6.1).
//
// Returns the number of FARs activated. Safe to call on missing
// sessions or sessions whose FARs are already FORW.
func (m *Manager) ActivateDL(imsi string, id uint8, gnbTEID, peerAddr uint32) (int, error) {
	const gtpuPort uint16 = 2152
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return 0, nil
	}
	n := 0
	for i := range s.FARs {
		if s.FARs[i].DstIface != 0 { // DL only
			continue
		}
		if bridge != nil {
			if err := bridge.UpdateFAR(imsi, id, s.FARs[i].FARID,
				gnbTEID, peerAddr, gtpuPort); err != nil {
				m.log.Warnf("UpdateFAR (activate) imsi=%s pduSess=%d farID=%d: %v",
					imsi, id, s.FARs[i].FARID, err)
			}
		}
		s.FARs[i].Action = farActionForward
		s.FARs[i].TEID = gnbTEID
		s.FARs[i].PeerAddr = peerAddr
		s.FARs[i].PeerPort = gtpuPort
		n++
	}
	return n, nil
}

// UpdateGnbTunnel updates the DL FAR with the gNB GTP-U tunnel endpoint.
// Called after PDUSessionResourceSetupResponse when the gNB provides its TEID/addr.
// Mirrors Python upf_context.update_gnb_tunnel.
func (m *Manager) UpdateGnbTunnel(imsi string, id uint8, gnbTEID uint32, gnbAddr string) error {
	log := logger.Get("upf.context").WithIMSI(imsi)
	// Convert gnbAddr string to host-byte-order uint32 (MSB-first numerical
	// value). The C side stores IPs and TEIDs in host byte order and
	// htonl() only at the wire boundary — architecture-agnostic.
	peerAddr := ipStringToHostU32(gnbAddr)
	// FAR-2 is the DL FAR (action=2=buffer -> action=1=forward).
	if err := m.UpdateFAR(imsi, id, 2, gnbTEID, peerAddr, 2152); err != nil {
		log.Errorf("gNB tunnel update FAILED: %s/%d: %v", imsi, id, err)
		return err
	}
	log.Infof("gNB tunnel updated: %s/%d TEID=0x%08X addr=%s",
		imsi, id, gnbTEID, gnbAddr)
	return nil
}

func (m *Manager) AddQER(imsi string, id uint8, qer QER) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return fmt.Errorf("upf: no session")
	}
	s.QERs = append(s.QERs, qer)
	if bridge != nil {
		if err := bridge.AddQER(imsi, id, qer.QERID, qer.QFI, qer.GateUL, qer.GateDL,
			qer.MBRUL, qer.MBRDL, qer.GBRUL, qer.GBRDL); err != nil {
			m.log.Warnf("bridge.AddQER: %v", err)
		}
	}
	return nil
}

// RemoveQER drops the QER with the given QERID from both the local
// session record and the dataplane (TS 29.244 §7.5.4.9 "Remove QER IE
// within PFCP Session Modification Request"). Idempotent: returns nil
// if the QER isn't present (the bridge layer already accepts "remove
// non-existent rule" as a no-op for retransmits).
func (m *Manager) RemoveQER(imsi string, id uint8, qerID uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return fmt.Errorf("upf: no session")
	}
	for i := range s.QERs {
		if s.QERs[i].QERID == qerID {
			s.QERs = append(s.QERs[:i], s.QERs[i+1:]...)
			break
		}
	}
	if bridge != nil {
		if err := bridge.RemoveQER(imsi, id, qerID); err != nil {
			m.log.Warnf("bridge.RemoveQER: %v", err)
		}
	}
	return nil
}

// RemoveFAR drops the FAR with the given FARID from both the local
// session record and the dataplane (TS 29.244 §7.5.4.7 "Remove FAR IE
// within PFCP Session Modification Request"). Idempotent on non-
// existent FARID for the same retransmit-tolerance reason as RemoveQER.
func (m *Manager) RemoveFAR(imsi string, id uint8, farID uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return fmt.Errorf("upf: no session")
	}
	for i := range s.FARs {
		if s.FARs[i].FARID == farID {
			s.FARs = append(s.FARs[:i], s.FARs[i+1:]...)
			break
		}
	}
	if bridge != nil {
		if err := bridge.RemoveFAR(imsi, id, farID); err != nil {
			m.log.Warnf("bridge.RemoveFAR: %v", err)
		}
	}
	return nil
}

func (m *Manager) AddURR(imsi string, id uint8, urr URR) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return fmt.Errorf("upf: no session")
	}
	s.URRs = append(s.URRs, urr)
	if bridge != nil {
		if err := bridge.AddURR(imsi, id, urr.URRID, urr.MeasMethod, urr.ReportTrigger,
			urr.VolThreshUL, urr.VolThreshDL, urr.TimeThresh); err != nil {
			m.log.Warnf("bridge.AddURR: %v", err)
		}
	}
	return nil
}

// SetSessionAMBR configures Session-AMBR metering in the C dataplane.
// Mirrors Python upf_context.set_session_ambr (TS 23.501 §5.7.1.6).
func (m *Manager) SetSessionAMBR(imsi string, id uint8, ambrUL, ambrDL uint64) {
	if bridge != nil {
		if err := bridge.SetSessionAMBR(imsi, id, ambrUL, ambrDL); err != nil {
			m.log.Warnf("bridge.SetSessionAMBR: %v", err)
		}
	}
}

func (m *Manager) RegisterTEID(teid uint32, imsi string, id uint8) {
	m.log.Debugf("TEID 0x%08X → %s/%d", teid, imsi, id)
	if bridge != nil {
		bridge.RegisterTEID(teid, imsi, id)
	}
}

func (m *Manager) RegisterUEIP(ueAddr uint32, imsi string, id uint8) {
	m.log.Debugf("UE-IP 0x%08X → %s/%d", ueAddr, imsi, id)
	if bridge != nil {
		bridge.RegisterUEIP(ueAddr, imsi, id)
	}
}

// UnregisterTEID / UnregisterUEIP — symmetric release for §7.5.6
// PFCP Session Deletion. For the cgo bridge this hits the dataplane
// del path (rte_hash_del_key + deferred free). For PfcpBridge it's
// a no-op — the wire-side deletion request implicitly releases the
// resources at the remote UP function. Idempotent.
func (m *Manager) UnregisterTEID(teid uint32) {
	m.log.Debugf("Unregister TEID 0x%08X", teid)
	if bridge != nil {
		bridge.UnregisterTEID(teid)
	}
}

func (m *Manager) UnregisterUEIP(ueAddr uint32) {
	m.log.Debugf("Unregister UE-IP 0x%08X", ueAddr)
	if bridge != nil {
		bridge.UnregisterUEIP(ueAddr)
	}
}

func (m *Manager) GetURRStats(imsi string, id uint8, urrID uint32) (volUL, volDL, pktUL, pktDL uint64, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.sessions[sessionKey{imsi, id}]
	if s == nil {
		return 0, 0, 0, 0, fmt.Errorf("upf: no session")
	}
	if bridge != nil {
		return bridge.GetURRStats(imsi, id, urrID)
	}
	// Pure-Go fallback: return in-memory counters from the URR
	for _, urr := range s.URRs {
		if urr.URRID == urrID {
			return urr.VolUL, urr.VolDL, urr.PktUL, urr.PktDL, nil
		}
	}
	return 0, 0, 0, 0, fmt.Errorf("upf: URR %d not found", urrID)
}

// GetQERStats returns per-QER drop counters (gate-closed + MBR-exceeded
// accumulators). Prefers the cgo bridge; in pure-Go mode returns zeros
// since the classifier path that increments these lives only in C.
func (m *Manager) GetQERStats(imsi string, id uint8, qerID uint32) (dropPktsUL, dropPktsDL, dropBytesUL, dropBytesDL uint64, err error) {
	m.mu.RLock()
	s := m.sessions[sessionKey{imsi, id}]
	m.mu.RUnlock()
	if s == nil {
		return 0, 0, 0, 0, fmt.Errorf("upf: no session")
	}
	if bridge != nil {
		return bridge.GetQERStats(imsi, id, qerID)
	}
	return 0, 0, 0, 0, nil
}

func (m *Manager) GetIOStats() IOStats {
	if bridge != nil {
		return bridge.GetIOStats()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stats
}

func (m *Manager) SessionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// Running reports whether the UPF dataplane Init() completed.
func (m *Manager) Running() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.running
}

func (m *Manager) AllSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s)
	}
	return out
}

func Status() map[string]any {
	return map[string]any{
		"running":       Default.running,
		"session_count": Default.SessionCount(),
		"io_stats":      Default.GetIOStats(),
	}
}

func iv(m map[string]any, k string) int64 {
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
func sv(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

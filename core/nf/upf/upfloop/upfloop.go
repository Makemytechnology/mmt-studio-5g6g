// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package upfloop — Integrated-PFCP bootstrap. Stands up both sides
// of one or more PFCP/N4 associations (TS 29.244) inside the same
// binary, so the SMF control plane reaches the UPF via real PFCP
// messages instead of in-process cgo calls. See nf/upf/cgo_bridge.go
// file header for the dual-impl rationale and nf/smf/upfclient/ for
// the SMF-side implementations.
//
// Multi-slice / multi-anchor (TS 23.501 v19.7.0 §5.15 + §6.3.3):
//
//	A 5GC slice (S-NSSAI) is anchored at a specific UPF chosen by
//	the SMF per (S-NSSAI, DNN). Each anchor needs its own §7.3.4
//	PFCP Association — SEID namespaces are allocator-local
//	(TS 29.244 §7.2.2.4.2), so a CP-SEID issued to anchor A is
//	meaningless to anchor B. EnableMulti spins up one
//	pfcp.Transport + pfcp.Handler + upfclient.PfcpBridge tuple
//	per slice spec, registers them in a RouterBridge, and installs
//	the router as the upf.Manager's UPFBridge. Per-session calls
//	(SessionCreate / Modify / Delete) dispatch to the right anchor
//	based on the upf.Select() result; rule-installation methods
//	keyed by (imsi, pduSessionID) follow that same anchor for the
//	lifetime of the session.
//
// Wire model on integrated CUPS:
//
//	1. Each spec.Addr (e.g. 127.0.0.1:8805 for upf-embb,
//	   127.0.0.1:8806 for upf-miot) gets its own pfcp.Transport
//	   bound to that UDP port (§6.1 default 8805 is reserved for
//	   the first slice; additional slices use adjacent ports).
//	2. Each transport feeds a pfcp.Handler whose ManagerHook is the
//	   *same* shared dataplane — one C session table, indexed by
//	   (imsi, pduSessionID), services every slice. Slice isolation
//	   is at the PFCP signalling layer; the dataplane is shared.
//	3. Each transport's address is dialed by an upfclient.PfcpBridge
//	   which performs §7.3.4 Association Setup before returning.
//	4. Every dialed bridge is registered into one upfclient.Router
//	   keyed by spec.UPFID, plus persisted to upf_instances so
//	   nf/smf/upf/registry.Select() can route per-PDU-session.
//
// Wired from webservice/cmd/sacore-web/main.go before
// upf.Default.Init() finishes EAL/PktIO bring-up.
package upfloop

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	genpfcp "github.com/mmt/pfcpgen/generated"

	smfupf "github.com/mmt/mmt-studio-core/nf/smf/upf"
	"github.com/mmt/mmt-studio-core/nf/smf/upfclient"
	"github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/nf/upf/pfcp"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// DefaultLoopAddr is the loopback N4 endpoint for the default slice.
// TS 29.244 §6.1 reserves UDP/8805 as the well-known PFCP port.
const DefaultLoopAddr = "127.0.0.1:8805"

// SliceSpec describes one UPF anchor. EnableMulti stands up a
// PfcpBridge per spec and registers it with the SMF UPF registry
// so upf.Select() can route by (DNN, SST).
type SliceSpec struct {
	// UPFID is the registry key (e.g. "upf-embb"). MUST be unique.
	UPFID string
	// Addr is the loopback PFCP listen target (e.g. "127.0.0.1:8806").
	// Empty string means default 127.0.0.1:8805.
	Addr string
	// SST is the supported slice SSTs as upper-case hex strings (e.g.
	// "01" for eMBB, "02" for URLLC, "03" for mIoT). Empty list means
	// "match any SST" — kept for the legacy single-UPF fallback.
	SST []string
	// DNNs is the supported DNNs (e.g. "internet", "ims", "iot").
	// Empty list means "match any DNN".
	DNNs []string
}

// State holds the running components for a single slice anchor.
type sliceRuntime struct {
	upfID    string
	addr     string
	server   *pfcp.Transport
	handler  *pfcp.Handler
	bridge   *upfclient.PfcpBridge
	cancelFw context.CancelFunc
}

// State is the multi-anchor lifecycle handle returned by
// Enable / EnableMulti. Close() tears down every anchor in turn.
type State struct {
	Router *upfclient.RouterBridge

	// Bridge is the *first* slice's PfcpBridge. Preserved as the
	// pre-multi-slice public field so existing callers / tests that
	// reach into State.Bridge keep compiling. New code should prefer
	// State.Router and Router.BridgeOf(upfID).
	Bridge *upfclient.PfcpBridge

	// ServerTransport / Handler are the first slice's UPF-side
	// listener + handler — kept for the same back-compat reason.
	ServerTransport *pfcp.Transport
	Handler         *pfcp.Handler

	slices []*sliceRuntime

	// hbCancel stops the per-anchor runtime heartbeat goroutine
	// (smfupf.Heartbeat publisher) on Close.
	hbCancel context.CancelFunc
}

// Close tears down every slice anchor. Idempotent.
func (s *State) Close() {
	if s == nil {
		return
	}
	if s.hbCancel != nil {
		s.hbCancel()
	}
	for _, sl := range s.slices {
		if sl.cancelFw != nil {
			sl.cancelFw()
		}
		if sl.bridge != nil {
			_ = sl.bridge.Close()
		}
		if sl.server != nil {
			_ = sl.server.Close()
		}
	}
}

// Enable stands up Integrated-PFCP at addr (pass "" for the spec
// default 127.0.0.1:8805) as a single-anchor deployment. Convenience
// wrapper around EnableMulti for legacy callers / tests.
//
// Idempotent: returns an empty State if a RouterBridge is already
// installed (a prior Enable / EnableMulti has already bootstrapped
// the loop in this process).
func Enable(addr string) (*State, error) {
	return EnableMulti([]SliceSpec{{Addr: addr}})
}

// EnableMulti stands up one PFCP anchor per spec, registers each in
// the SMF UPF registry, and installs a RouterBridge as the global
// upf.UPFBridge so per-session calls dispatch by slice (TS 23.501
// §6.3.3 UPF selection). Empty spec list is treated as a single
// default-slice deployment on 127.0.0.1:8805.
//
// Idempotent: returns an empty State if a RouterBridge is already
// installed.
func EnableMulti(specs []SliceSpec) (*State, error) {
	log := logger.Get("upf.upfloop")

	if _, ok := upf.Bridge().(*upfclient.RouterBridge); ok {
		log.Infof("Integrated-PFCP already enabled — skipping bootstrap")
		return &State{}, nil
	}
	// Pre-multi-anchor world: a *PfcpBridge global means a previous
	// (legacy) Enable already ran. Treat as already-enabled.
	if _, ok := upf.Bridge().(*upfclient.PfcpBridge); ok {
		log.Infof("Integrated-PFCP already enabled (legacy single-bridge) — skipping bootstrap")
		return &State{}, nil
	}

	if len(specs) == 0 {
		specs = []SliceSpec{{}}
	}

	// Capture the pre-swap dataplane bridge — on Linux the cgo
	// dpdkBridge installed by upf/cgo_bridge_linux.go init(). MUST
	// happen BEFORE any SetBridge call replaces the global, else the
	// hook would loop back onto our own PFCP transport.
	//
	// Single shared hook across all slice handlers: per the file
	// header, slice isolation lives at PFCP signalling, not at the
	// dataplane (one C session table services every slice — keyed by
	// (imsi, pduSessionID), no namespace collision).
	dp := upf.Bridge()
	var hook pfcp.ManagerHook
	if dp != nil {
		hook = &bridgeHook{dp: dp}
	}

	router := upfclient.NewRouterBridge()
	state := &State{Router: router}

	for i := range specs {
		sl, err := bringSliceUp(&specs[i], hook, log)
		if err != nil {
			// Roll back any anchors brought up so far so the process
			// doesn't end up with a half-bound PFCP socket.
			for _, prev := range state.slices {
				if prev.bridge != nil {
					_ = prev.bridge.Close()
				}
				if prev.server != nil {
					_ = prev.server.Close()
				}
			}
			return nil, fmt.Errorf("upfloop: bring slice %s up: %w", specs[i].UPFID, err)
		}
		router.RegisterBridge(sl.upfID, sl.bridge)
		state.slices = append(state.slices, sl)
	}

	// Telemetry shortcut (per-bridge): every bridge's GetIOStats /
	// GetURRStats / GetQERStats reads the same in-process dpdkBridge
	// since the dataplane is shared. Wire the peer once per bridge
	// so the GUI's /api/upf/io-stats path works regardless of which
	// bridge the router fans the call to.
	if dp != nil {
		for _, sl := range state.slices {
			sl.bridge.SetStatsPeer(dp)
		}
	}

	upf.SetBridge(router)
	// Publish the router on the package-level singleton so the OAM
	// surface (webservice / DPI panel) can push PFD-Management updates
	// without threading a handle through every layer. TS 29.244 §6.2.5.
	upfclient.DefaultRouter = router

	// Back-compat fields: the first slice in the spec list is the
	// "default" anchor. Existing callers (tests, lifecycle hooks)
	// that read State.Bridge / State.ServerTransport / State.Handler
	// keep working without code change.
	if len(state.slices) > 0 {
		state.Bridge = state.slices[0].bridge
		state.ServerTransport = state.slices[0].server
		state.Handler = state.slices[0].handler
	}

	// §7.5.8 Session Report Request forwarder per slice. Each slice's
	// PFCP handler owns its own Peer table (populated at §7.5.2
	// Establishment), so the forwarder must run per-slice — pulling
	// from the shared dataplane ring and routing to whichever anchor
	// owns the (imsi, pduSessionID) key.
	if dp != nil {
		ctx, cancel := context.WithCancel(context.Background())
		// One shared cancel — tied to the first slice for symmetry
		// with the pre-multi-anchor State; the goroutine itself
		// drains every anchor in the loop.
		state.slices[0].cancelFw = cancel
		go runReportForwarder(ctx, dp, state.slices)
	}

	// Per-anchor heartbeat. TS 23.501 v19.7.0 §6.3.5 (UPF Status) and
	// TS 29.244 v19.5.0 §7.4.2 (Heartbeat Request/Response) define
	// liveness over the N4 wire; here we additionally publish a
	// runtime view (active sessions, load) for the GUI by counting
	// per-bridge sessions every 5 s and calling smfupf.Heartbeat. The
	// SMF-side cache is what /api/admin/upf-instances merges into its
	// JSON response; without this tick the registry shows status=
	// "unknown" forever.
	hbCtx, hbCancel := context.WithCancel(context.Background())
	state.hbCancel = hbCancel
	go runRouterHeartbeat(hbCtx, router, log)

	upfIDs := make([]string, 0, len(state.slices))
	for _, sl := range state.slices {
		upfIDs = append(upfIDs, fmt.Sprintf("%s@%s", sl.upfID, sl.addr))
	}
	log.Infof("Integrated-PFCP enabled (multi-anchor) [%s] — upf.Manager bridge = RouterBridge (TS 29.244 §7.4.4.1, TS 23.501 §6.3.3)",
		strings.Join(upfIDs, ", "))

	return state, nil
}

// bringSliceUp stands up one anchor's transport + handler + dial,
// and registers the matching upf_instances row so upf.Select() can
// route to it by (DNN, SST).
func bringSliceUp(spec *SliceSpec, hook pfcp.ManagerHook, log *logger.Logger) (*sliceRuntime, error) {
	addr := spec.Addr
	if addr == "" {
		addr = DefaultLoopAddr
	}
	upfID := spec.UPFID
	if upfID == "" {
		upfID = "upf-default"
	}

	serverT, err := pfcp.NewTransport(addr)
	if err != nil {
		return nil, fmt.Errorf("UPF-side transport on %s: %w", addr, err)
	}

	h := pfcp.NewHandler(serverT, hook)

	// Dial must target the actual bound port. For ephemeral ":0"
	// addresses we need to read the listener back.
	dialAddr := serverT.LocalAddr()
	if dialAddr == nil {
		_ = serverT.Close()
		return nil, fmt.Errorf("UPF transport on %s: no local addr", addr)
	}
	dialTarget := (&net.UDPAddr{IP: dialAddr.IP, Port: dialAddr.Port}).String()
	if dialAddr.IP == nil || dialAddr.IP.IsUnspecified() {
		dialTarget = fmt.Sprintf("127.0.0.1:%d", dialAddr.Port)
	}

	br, err := upfclient.Dial(dialTarget)
	if err != nil {
		_ = serverT.Close()
		return nil, fmt.Errorf("SMF-side dial %s: %w", dialTarget, err)
	}

	// Persist the anchor in the SMF UPF registry so upf.Select()
	// considers it in subsequent §7.5.2 establishments. Best-effort:
	// a DB error here doesn't fail the boot — the bridge still works
	// via the RouterBridge's fallback (first-registered) path, just
	// without per-slice routing for this anchor. Logged so it's
	// visible.
	//
	// upf_ip / pfcp_port reflect the loopback PFCP listen target
	// (CP↔UP control plane). n3_ip / n6_ip must reflect the
	// gNB-reachable IP (TS 38.413 §9.3.1.6 GTPTunnel — the IP the
	// AMF will hand back to the gNB in PDU Session Resource Setup
	// Request); fall back to the host's primary non-loopback IPv4
	// when the dial target is loopback. Keeps the legacy upf-local
	// path (n3_ip = container netns IP) intact for new anchors.
	pfcpHost, port := splitHostPort(dialTarget)
	gtpuIP := primaryHostIPv4()
	if gtpuIP == "" {
		gtpuIP = pfcpHost
	}
	inst := smfupf.Instance{
		UPFID:         upfID,
		UPFIP:         pfcpHost,
		N3IP:          gtpuIP,
		N6IP:          gtpuIP,
		PFCPPort:      port,
		SupportedDNNs: spec.DNNs,
		SupportedSST:  spec.SST,
	}
	if err := smfupf.Register(inst); err != nil {
		log.Warnf("upf.Register %s: %v (RouterBridge fallback will still route)", upfID, err)
	}

	log.Infof("PFCP anchor up upfID=%s addr=%s SST=%v DNNs=%v (TS 29.244 §7.4.4.1, TS 23.501 §6.3.3)",
		upfID, dialTarget, spec.SST, spec.DNNs)

	return &sliceRuntime{
		upfID:   upfID,
		addr:    dialTarget,
		server:  serverT,
		handler: h,
		bridge:  br,
	}, nil
}

func splitHostPort(addr string) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1", 8805
	}
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)
	return host, port
}

// primaryHostIPv4 returns the first global-unicast IPv4 bound to a
// non-loopback interface — the address gNB sees when it sends GTP-U
// (TS 38.413 §9.3.1.6 GTPTunnel transport-layer-address). In the
// integrated-CUPS deployment the dataplane binds N3 on 0.0.0.0:2152
// inside the sacore netns, but the gNB still needs a routable IP to
// address; loopback would force packets back to the gNB host.
//
// Returns "" if no suitable address is found — caller falls back to
// the PFCP loopback host so registry rows still serialize cleanly.
func primaryHostIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			v4 := ip.To4()
			if v4 == nil || v4.IsLoopback() || v4.IsUnspecified() || v4.IsLinkLocalUnicast() {
				continue
			}
			return v4.String()
		}
	}
	return ""
}

// runRouterHeartbeat publishes a per-anchor liveness + load tick into
// the SMF-side UPF registry every 5 s. TS 23.501 v19.7.0 §6.3.5 (UPF
// Status) says the SMF tracks UPF availability via N4 heartbeats —
// here we additionally count active sessions per bridge so the GUI
// can show a status badge + load bar. SessionCount() walks the
// PfcpBridge's internal session table (populated at §7.5.2 Establish,
// drained at §7.5.6 Delete), so the count reflects the slice's actual
// PFCP-anchored sessions, not the dataplane aggregate.
//
// Cap loadPct at 100 — Heartbeat treats >100 as a config error and
// some GUIs render >100% as a layout glitch.
func runRouterHeartbeat(ctx context.Context, router *upfclient.RouterBridge, log *logger.Logger) {
	const cadence = 5 * time.Second
	ticker := time.NewTicker(cadence)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for upfID, br := range router.Bridges() {
				active := int(br.SessionCount())
				inst, err := smfupf.Get(upfID)
				maxSess := int64(0)
				if err == nil && inst != nil {
					maxSess = inst.MaxSessions
				}
				loadPct := 0
				if maxSess > 0 {
					loadPct = int(int64(active) * 100 / maxSess)
					if loadPct > 100 {
						loadPct = 100
					}
				}
				smfupf.Heartbeat(upfID, active, loadPct)
			}
			_ = log // kept for future debug-tier counters
		}
	}
}

// runReportForwarder bridges the UPF-C rte_ring → PFCP §7.5.8 wire
// for every slice. Pulls reports from the shared dataplane every
// 10ms; for each report, looks up which slice owns the
// (imsi, pduSessionID) by walking the per-slice handler tables and
// emits the §7.5.8 Request from that anchor's transport.
func runReportForwarder(ctx context.Context, dp upf.UPFBridge, slices []*sliceRuntime) {
	log := logger.Get("upf.upfloop.forwarder")
	const batchSize = 64
	buf := make([]upf.Report, batchSize)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n := dp.DrainReports(buf)
			for i := 0; i < n; i++ {
				forwardOneReport(log, slices, &buf[i])
			}
		}
	}
}

// forwardOneReport encodes a single upf.Report as a §7.5.8 PFCP
// Session Report Request. Walks every slice's HandlerSession table
// to find which anchor owns the (imsi, pduSessionID); the report
// rides that anchor's transport so the destination SEID is correct
// for that CP↔UP pair (TS 29.244 §7.2.2.4.2).
func forwardOneReport(log *logger.Logger, slices []*sliceRuntime, r *upf.Report) {
	for _, sl := range slices {
		sess := sl.handler.FindByIMSI(r.IMSI, r.PDUSessionID)
		if sess == nil {
			continue
		}
		switch r.Type {
		case upf.ReportDLDR:
			req := &genpfcp.SessionReportRequest{
				SEID:       sess.CPSEID, // §7.2.2.4.2 destination = peer's SEID
				ReportType: genpfcp.ReportType{DLDR: 1},
				DownlinkDataReport: &genpfcp.DownlinkDataReport{
					PDRID: []genpfcp.PacketDetectionRuleID{{Value: 2}},
				},
			}
			payload, err := encodeReport(req)
			if err != nil {
				log.WithIMSI(r.IMSI).Warnf("§7.5.8 DLDR encode: %v", err)
				return
			}
			if _, err := sl.server.SendRequest(sess.Peer, pfcp.EncodedMessage{
				MsgType: genpfcp.MessageTypeSessionReportRequest,
				SEID:    sess.CPSEID,
				IEs:     payload,
			}); err != nil {
				log.WithIMSI(r.IMSI).Warnf("§7.5.8 DLDR send to %s via %s: %v",
					sess.Peer, sl.upfID, err)
				return
			}
			log.WithIMSI(r.IMSI).Infof("§7.5.8 DLDR sent via %s pduSessID=%d UP-SEID=%#x → %s",
				sl.upfID, r.PDUSessionID, sess.UPSEID, sess.Peer)
		default:
			log.WithIMSI(r.IMSI).Debugf("§7.5.8 %s report on %s: wire-forward not yet implemented",
				r.Type, sl.upfID)
		}
		return
	}
	log.WithIMSI(r.IMSI).Warnf("§7.5.8 forwarder: no anchor owns pduSessID=%d — dropping %s report",
		r.PDUSessionID, r.Type)
}

// encodeReport strips the generated message's header (built by
// Encode()) since the transport adds its own. The generated
// msg_sessionreportrequest.Encode sets HasSEID=true (SessionReport
// is a per-session message), so 16 bytes of header are on the front.
func encodeReport(m interface {
	Encode() ([]byte, error)
}) ([]byte, error) {
	full, err := m.Encode()
	if err != nil {
		return nil, err
	}
	if len(full) < 16 {
		return nil, fmt.Errorf("encodeReport: short output %d bytes", len(full))
	}
	return full[16:], nil
}

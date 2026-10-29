// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NW-TT — the network-side TSN translator of the 5GS bridge model
// (Rel-19). One NWTT instance lives on each PFCP handler (one per UPF
// anchor) and implements the UP-function side of:
//
//	TS 29.244 §7.5.2.1/§7.5.3.6 — Create/Created Bridge Info for TSC:
//	  allocate the DS-TT port number for an Ethernet PDU session and
//	  report the 5GS User Plane Node ID (TS 23.501 §5.28.1: "port
//	  numbering is assigned by the UPF", "Bridge ID bound to the UPF");
//	TS 29.244 §7.5.4.18/§7.5.5.3 — TSC Management Information:
//	  terminate PMIC (TS 24.539 port management service) for NW-TT
//	  ports and UMIC (user plane node management service) at node
//	  level, answering the TSN AF's get/read/set/subscribe operations;
//	TS 23.501 §5.27.4 — hold-and-forward gate state per NW-TT port
//	  (AdminControlList & friends, applied to the port param store —
//	  the scheduling itself is a dataplane concern).
package pfcp

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/mmt/mmt-studio-core/edge/tsn/gptp"
	"github.com/mmt/mmt-studio-core/edge/tsn/ttmgmt"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// nwttPortState is the TS 24.539 parameter store of one translator
// port managed by this NW-TT.
type nwttPortState struct {
	params map[uint16][]byte // port parameter name → value
	subs   map[uint16]bool   // subscribe-notify armed
}

// NWTT models the network-side TSN translator of one UPF anchor.
type NWTT struct {
	mu sync.Mutex
	// nodeID — 5GS User Plane Node ID (TS 29.244 §8.2.143). Derived
	// from the anchor's bridge MAC by default (IEEE 802.1Q 14.2.5).
	nodeID uint64
	// nwttPorts — N6-side ports (preconfigured; TS 23.501 §5.28.1).
	nwttPorts []uint16
	// nextDSTT — next DS-TT port number to allocate. DS-TT ports are
	// assigned per Ethernet PDU session at establishment.
	nextDSTT uint16
	// dsttBySession — (imsi,pduSessID) → allocated DS-TT port.
	dsttBySession map[imsiPduKey]uint16

	ports      map[uint16]*nwttPortState
	nodeParams map[uint16][]byte // UMS parameter name → value

	// detectedMACs — MAC addresses learned on DS-TT ports (UL source
	// MACs of Ethernet PDU sessions; TS 29.244 §8.2.98 reporting).
	detectedMACs map[imsiPduKey][][]byte

	// gmSeq — Sync sequenceId counter for the §5.27.1.7 grandmaster
	// function (per NW-TT; wraps at 16 bits per IEEE 1588 §7.3.7).
	gmSeq atomic.Uint32

	// bmca — one Best Master Clock engine per (g)PTP domain
	// (TS 23.501 §5.27.1.6 method a: the NW-TT executes the BMCA and
	// maintains the PTP port states).
	bmca map[byte]*gptp.BMCA

	// ptpEnabled — a (g)PTP instance has been provisioned on this
	// node (TS 24.539 PTP instance list): fast-path steering is
	// switched on for every DS-TT session (upf_ptp.c).
	ptpEnabled bool

	log *logger.Logger
}

// NewNWTT builds the NW-TT for one UPF anchor. nodeID 0 derives a
// stable default.
// ── NW-TT instance registry ─────────────────────────────────────────
//
// One NW-TT (5GS bridge) exists per UPF anchor (NewHandler). The
// registry lets the webservice's TSC surface reach a bridge's NW-TT by
// node ID — e.g. to inject a (g)PTP Announce arriving from the external
// TSN network at an NW-TT port (TS 23.501 §5.27.1.6: the NW-TT runs
// BMCA on Announce received over N6) when no raw-L2 path exists.

var (
	nwttRegMu sync.RWMutex
	nwttReg   []*NWTT
)

func registerNWTT(n *NWTT) {
	nwttRegMu.Lock()
	nwttReg = append(nwttReg, n)
	nwttRegMu.Unlock()
}

// NWTTByNodeID finds the NW-TT of the 5GS bridge with the given
// user-plane node ID, or nil.
func NWTTByNodeID(id uint64) *NWTT {
	nwttRegMu.RLock()
	defer nwttRegMu.RUnlock()
	for _, n := range nwttReg {
		if n.NodeID() == id {
			return n
		}
	}
	return nil
}

func NewNWTT(nodeID uint64) *NWTT {
	if nodeID == 0 {
		nodeID = 0x0A0B0C000001 // default bridge MAC 0a-0b-0c-00-00-01
	}
	n := &NWTT{
		nodeID:        nodeID,
		nwttPorts:     []uint16{1},
		nextDSTT:      2,
		dsttBySession: map[imsiPduKey]uint16{},
		ports:         map[uint16]*nwttPortState{},
		nodeParams:    map[uint16][]byte{},
		detectedMACs:  map[imsiPduKey][][]byte{},
		bmca:          map[byte]*gptp.BMCA{},
		log:           logger.Get("upf.nwtt"),
	}
	// Node-level UMS parameters (TS 24.539 table 9.5B.1).
	n.nodeParams[ttmgmt.NodeParamID] = ttmgmt.EncodeUint64(nodeID)
	n.nodeParams[ttmgmt.NodeParamAddress] = []byte{
		byte(nodeID >> 40), byte(nodeID >> 32), byte(nodeID >> 24),
		byte(nodeID >> 16), byte(nodeID >> 8), byte(nodeID),
	}
	n.nodeParams[ttmgmt.NodeParamNWTTPortNumbers] = ttmgmt.EncodeNWTTPortNumbers(n.nwttPorts)
	n.nodeParams[ttmgmt.NodeParamGPTPGrandmasterCapable] = ttmgmt.EncodeBool(true)
	n.nodeParams[ttmgmt.NodeParamPTPGrandmasterCapable] = ttmgmt.EncodeBool(true)
	// §5.27.1.12 TSS — synchronization state: 0 = locked (5G clock).
	n.nodeParams[ttmgmt.NodeParamSynchronizationState] = []byte{0x00}
	for _, p := range n.nwttPorts {
		n.ports[p] = newNwttPortState()
	}
	return n
}

func newNwttPortState() *nwttPortState {
	s := &nwttPortState{
		params: map[uint16][]byte{},
		subs:   map[uint16]bool{},
	}
	// Defaults the TSN AF can read (TS 24.539 table 9.2.1).
	s.params[ttmgmt.PortParamTxPropagationDelay] = ttmgmt.EncodePropagationDelay(1000) // 1 µs
	s.params[ttmgmt.PortParamGateEnabled] = ttmgmt.EncodeBool(false)
	s.params[ttmgmt.PortParamTickGranularity] = ttmgmt.EncodeUint32(1)
	return s
}

// supportedPortParams is the NW-TT port capability set answered to a
// "get capabilities" operation (TS 24.539 §9.3).
var supportedPortParams = []uint16{
	ttmgmt.PortParamTxPropagationDelay,
	ttmgmt.PortParamTrafficClassTable,
	ttmgmt.PortParamGateEnabled,
	ttmgmt.PortParamAdminBaseTime,
	ttmgmt.PortParamAdminControlListLength,
	ttmgmt.PortParamAdminControlList,
	ttmgmt.PortParamAdminCycleTime,
	ttmgmt.PortParamTickGranularity,
	ttmgmt.PortParamTxPropagationDelayDeltaThreshold,
	ttmgmt.PortParamTSNTimeDomainNumber,
	ttmgmt.PortParamPTPInstanceList,
}

// AllocateDSTTPort implements the bridge-information side of a §7.5.2
// Establishment with Create Bridge Info for TSC (BII=1): allocates the
// session's DS-TT port and returns (dsttPort, nwttPorts, nodeID) for
// the §7.5.3.6 Created Bridge Info grouped IE. Idempotent per session.
func (n *NWTT) AllocateDSTTPort(imsi string, pduSessionID uint8) (uint16, []uint16, uint64) {
	key := imsiPduKey{imsi, pduSessionID}
	n.mu.Lock()
	defer n.mu.Unlock()
	port, ok := n.dsttBySession[key]
	created := false
	if !ok {
		port = n.nextDSTT
		n.nextDSTT++
		n.dsttBySession[key] = port
		n.ports[port] = newNwttPortState()
		created = true
		n.log.WithIMSI(imsi).Infof("DS-TT port %d allocated for pduSessID=%d on bridge %#x (TS 23.501 §5.28.1)",
			port, pduSessionID, n.nodeID)
	}
	ptpOn := n.ptpEnabled
	nwtts := append([]uint16(nil), n.nwttPorts...)
	nodeID := n.nodeID
	n.mu.Unlock()
	// New session on a node with an active PTP instance → steer its
	// (g)PTP traffic in the fast path (§5.27.1.2.2).
	if created && ptpOn && tscPTPSink != nil {
		tscPTPSink(imsi, pduSessionID, true)
	}
	n.mu.Lock()
	return port, nwtts, nodeID
}

// ReleaseDSTTPort frees the session's port on PDU session release.
func (n *NWTT) ReleaseDSTTPort(imsi string, pduSessionID uint8) {
	key := imsiPduKey{imsi, pduSessionID}
	n.mu.Lock()
	if port, ok := n.dsttBySession[key]; ok {
		delete(n.dsttBySession, key)
		delete(n.ports, port)
		delete(n.detectedMACs, key)
	}
	n.mu.Unlock()
}

// tscMACSink pushes learned MACs into the dataplane session table
// (upf.Default.LearnMAC via upfloop wiring); nil = signaling-level only.
var tscMACSink func(imsi string, pduSessionID uint8, mac [6]byte)

// SetTSCMACSink installs the dataplane MAC-learning hook.
func SetTSCMACSink(f func(imsi string, pduSessionID uint8, mac [6]byte)) {
	tscMACSink = f
}

// RegisterDetectedMAC records a source MAC learned on the session's
// DS-TT port (TS 29.244 §8.2.98 MAC Addresses Detected — the reporting
// ride is the usage-report path; the store also feeds /api surfaces)
// and mirrors it into the dataplane session table.
func (n *NWTT) RegisterDetectedMAC(imsi string, pduSessionID uint8, mac [6]byte) {
	key := imsiPduKey{imsi, pduSessionID}
	n.mu.Lock()
	for _, m := range n.detectedMACs[key] {
		if [6]byte(m) == mac {
			n.mu.Unlock()
			return
		}
	}
	n.detectedMACs[key] = append(n.detectedMACs[key], mac[:])
	n.mu.Unlock()
	if tscMACSink != nil {
		tscMACSink(imsi, pduSessionID, mac)
	}
	n.log.WithIMSI(imsi).Infof("Ethernet MAC detected %02x-%02x-%02x-%02x-%02x-%02x pduSessID=%d",
		mac[0], mac[1], mac[2], mac[3], mac[4], mac[5], pduSessionID)
}

// tscPTPSink enables/disables fast-path (g)PTP steering for a session
// (TS 23.501 §5.27.1.2.2; dataplane upf_ptp.c). Wired by upfloop to
// upf.Default.SetPTPSteering.
var tscPTPSink func(imsi string, pduSessionID uint8, enable bool)

// SetTSCPTPSink installs the dataplane PTP-steering hook.
func SetTSCPTPSink(f func(imsi string, pduSessionID uint8, enable bool)) {
	tscPTPSink = f
}

// tscGateSink is the dataplane gate-programming hook (Rel-19
// hold & forward, TS 23.501 §5.27.4). Set by the upfloop wiring to
// upf.Default.SetTSCGate; nil = gate state stays signaling-level.
// Kept as primitive parameters so upf/pfcp does not import nf/upf.
var tscGateSink func(imsi string, pduSessionID uint8, trafficClass uint8,
	baseTimeNS, cycleTimeNS uint64, gateStates []byte, durationsNS []uint32)

// SetTSCGateSink installs the dataplane gate hook.
func SetTSCGateSink(f func(imsi string, pduSessionID uint8, trafficClass uint8,
	baseTimeNS, cycleTimeNS uint64, gateStates []byte, durationsNS []uint32)) {
	tscGateSink = f
}

// pushGateToDataplane assembles the TS 24.539 gate parameters of a
// port (GateEnabled / AdminBaseTime / AdminControlList /
// AdminCycleTime) and, when the port is a DS-TT session port with the
// gate enabled, programs the dataplane egress gate. Caller must NOT
// hold n.mu.
func (n *NWTT) pushGateToDataplane(portNum uint16) {
	if tscGateSink == nil {
		return
	}
	n.mu.Lock()
	port := n.ports[portNum]
	var imsi string
	var pduID uint8
	for k, p := range n.dsttBySession {
		if p == portNum {
			imsi, pduID = k.IMSI, k.PDUSessionID
			break
		}
	}
	if port == nil || imsi == "" {
		n.mu.Unlock()
		return
	}
	enabled := false
	if v := port.params[ttmgmt.PortParamGateEnabled]; len(v) >= 1 {
		enabled = v[0] == 0x01
	}
	aclRaw := port.params[ttmgmt.PortParamAdminControlList]
	var baseNS, cycleNS uint64
	if v := port.params[ttmgmt.PortParamAdminBaseTime]; len(v) == 10 {
		if sec, ns, err := ttmgmt.DecodeAdminBaseTime(v); err == nil {
			baseNS = sec*1_000_000_000 + uint64(ns)
		}
	}
	if v := port.params[ttmgmt.PortParamAdminCycleTime]; len(v) == 8 {
		for _, b := range v {
			cycleNS = cycleNS<<8 | uint64(b)
		}
	}
	tc := byte(0)
	if v := port.params[ttmgmt.PortParamTSNTimeDomainNumber]; len(v) >= 1 {
		_ = v // time domain is not the TC; TC rides the traffic class table
	}
	n.mu.Unlock()

	if !enabled || len(aclRaw) == 0 || cycleNS == 0 {
		return
	}
	entries, err := ttmgmt.DecodeAdminControlList(aclRaw)
	if err != nil || len(entries) == 0 {
		n.log.Warnf("gate push %s/%d: AdminControlList: %v", imsi, pduID, err)
		return
	}
	states := make([]byte, len(entries))
	durs := make([]uint32, len(entries))
	for i, e := range entries {
		states[i] = e.GateStates
		durs[i] = e.TimeIntervalNS
	}
	tscGateSink(imsi, pduID, tc, baseNS, cycleNS, states, durs)
	n.log.WithIMSI(imsi).Infof(
		"TSC gate programmed to dataplane: port=%d entries=%d cycle=%dns (TS 23.501 §5.27.4)",
		portNum, len(entries), cycleNS)
}

// HandlePMIC terminates a TS 24.539 port management service message
// addressed to an NW-TT port and returns the reply message bytes
// (MANAGE PORT COMPLETE for a COMMAND; nil for ACKs).
func (n *NWTT) HandlePMIC(portNum uint16, container []byte) []byte {
	msg, err := ttmgmt.DecodePMS(container)
	if err != nil {
		n.log.Warnf("NW-TT PMIC decode (port %d): %v", portNum, err)
		return nil
	}
	if msg.Type != ttmgmt.MsgManagePortCommand {
		// NOTIFY ACK etc. — nothing to answer.
		return nil
	}
	if portNum == 0 && len(n.nwttPorts) > 0 {
		portNum = n.nwttPorts[0]
	}
	n.mu.Lock()
	port := n.ports[portNum]
	if port == nil {
		port = newNwttPortState()
		n.ports[portNum] = port
	}
	gateTouched := false
	ptpTouched := false
	reply := &ttmgmt.Message{Type: ttmgmt.MsgManagePortComplete}
	for _, op := range msg.Ops {
		switch op.Code {
		case ttmgmt.OpGetCapabilities:
			reply.Capabilities = supportedPortParams
		case ttmgmt.OpReadParameter:
			if v, ok := port.params[op.Param]; ok {
				reply.Status = append(reply.Status, ttmgmt.ParamStatus{Param: op.Param, Value: v})
			} else {
				reply.StatusErrors = append(reply.StatusErrors,
					ttmgmt.ParamError{Param: op.Param, Cause: ttmgmt.CauseParamValueUnavailable})
			}
		case ttmgmt.OpSetParameter:
			port.params[op.Param] = append([]byte(nil), op.Value...)
			reply.Updates = append(reply.Updates, ttmgmt.ParamStatus{Param: op.Param, Value: op.Value})
			switch op.Param {
			case ttmgmt.PortParamGateEnabled, ttmgmt.PortParamAdminBaseTime,
				ttmgmt.PortParamAdminControlList, ttmgmt.PortParamAdminCycleTime:
				gateTouched = true
			case ttmgmt.PortParamPTPInstanceList:
				ptpTouched = true
			}
		case ttmgmt.OpSubscribeNotify:
			port.subs[op.Param] = true
			reply.Status = append(reply.Status, ttmgmt.ParamStatus{
				Param: op.Param, Value: port.params[op.Param]})
		case ttmgmt.OpUnsubscribe:
			delete(port.subs, op.Param)
		case ttmgmt.OpDeleteParameterEntry:
			delete(port.params, op.Param)
			reply.Updates = append(reply.Updates, ttmgmt.ParamStatus{Param: op.Param})
		default:
			reply.UpdateErrors = append(reply.UpdateErrors,
				ttmgmt.ParamError{Param: op.Param, Cause: ttmgmt.CauseProtocolError})
		}
	}
	n.mu.Unlock()
	// Gate parameters changed → push the assembled schedule into the
	// dataplane egress gate (hold & forward, TS 23.501 §5.27.4).
	if gateTouched {
		n.pushGateToDataplane(portNum)
	}
	// A (g)PTP instance was provisioned (TS 24.539 PTP instance list,
	// TS 23.501 §5.27.1.8): enable fast-path steering for every DS-TT
	// session of this node, now and for future sessions.
	if ptpTouched {
		n.mu.Lock()
		n.ptpEnabled = true
		sessions := make(map[imsiPduKey]uint16, len(n.dsttBySession))
		for k, v := range n.dsttBySession {
			sessions[k] = v
		}
		n.mu.Unlock()
		if tscPTPSink != nil {
			for k := range sessions {
				tscPTPSink(k.IMSI, k.PDUSessionID, true)
			}
		}
	}
	out, err := reply.Encode()
	if err != nil {
		n.log.Warnf("NW-TT PMIC reply encode: %v", err)
		return nil
	}
	n.log.Infof("NW-TT port %d: PMIC %d op(s) → COMPLETE (%d B) (TS 24.539 §6.2.1)",
		portNum, len(msg.Ops), len(out))
	return out
}

// HandleUMIC terminates a TS 24.539 user plane node management service
// message and returns the reply (MANAGE USER PLANE NODE COMPLETE).
func (n *NWTT) HandleUMIC(container []byte) []byte {
	msg, err := ttmgmt.DecodeUMS(container)
	if err != nil {
		n.log.Warnf("NW-TT UMIC decode: %v", err)
		return nil
	}
	if msg.Type != ttmgmt.MsgManageUPNodeCommand {
		return nil
	}
	n.mu.Lock()
	reply := &ttmgmt.Message{Type: ttmgmt.MsgManageUPNodeComplete}
	for _, op := range msg.Ops {
		switch op.Code {
		case ttmgmt.OpGetCapabilities:
			for p := range n.nodeParams {
				reply.Capabilities = append(reply.Capabilities, p)
			}
		case ttmgmt.OpReadParameter:
			if v, ok := n.nodeParams[op.Param]; ok {
				reply.Status = append(reply.Status, ttmgmt.ParamStatus{Param: op.Param, Value: v})
			} else {
				reply.StatusErrors = append(reply.StatusErrors,
					ttmgmt.ParamError{Param: op.Param, Cause: ttmgmt.CauseParamValueUnavailable})
			}
		case ttmgmt.OpSetParameter:
			// Node identity parameters are UPF-owned (read-only for
			// the AF); everything else (static filtering entries,
			// PTP instance spec, ...) is accepted.
			if op.Param == ttmgmt.NodeParamID || op.Param == ttmgmt.NodeParamAddress {
				reply.UpdateErrors = append(reply.UpdateErrors,
					ttmgmt.ParamError{Param: op.Param, Cause: ttmgmt.CauseInvalidParamValue})
				continue
			}
			n.nodeParams[op.Param] = append([]byte(nil), op.Value...)
			reply.Updates = append(reply.Updates, ttmgmt.ParamStatus{Param: op.Param, Value: op.Value})
		case ttmgmt.OpDeleteParameterEntry:
			delete(n.nodeParams, op.Param)
			reply.Updates = append(reply.Updates, ttmgmt.ParamStatus{Param: op.Param})
		}
	}
	n.mu.Unlock()
	out, err := reply.Encode()
	if err != nil {
		n.log.Warnf("NW-TT UMIC reply encode: %v", err)
		return nil
	}
	n.log.Infof("NW-TT UMIC %d op(s) → COMPLETE (%d B) (TS 24.539 §6.3.1)", len(msg.Ops), len(out))
	return out
}

// SetClockState updates the node-level TSS synchronization state
// (TS 23.501 §5.27.1.12: 0=locked, 1=holdover, 2=freerun) — driven by
// the studio's clock-domain panel via the edge/tsn module.
func (n *NWTT) SetClockState(state byte) {
	n.mu.Lock()
	n.nodeParams[ttmgmt.NodeParamSynchronizationState] = []byte{state}
	n.mu.Unlock()
}

// ── (g)PTP processing — TS 23.501 §5.27.1.2 ──

// GPTPIngress applies the ingress-translator transform to a (g)PTP
// PDU entering the 5GS at this NW-TT (DL direction; the DS-TT side is
// symmetric): link-delay correction, cumulative rateRatio update and
// the TSi suffix (5G internal system clock) — §5.27.1.2.2.1 steps 2-6.
// Returns the rewritten PDU for transport across the PDU session.
func (n *NWTT) GPTPIngress(pdu []byte, tsiNS uint64, linkDelayGMNS, neighborRateRatio float64) ([]byte, error) {
	m, err := gptp.Decode(pdu)
	if err != nil {
		return nil, err
	}
	// One-step Sync and two-step Follow_Up carry the timing info; a
	// two-step Sync passes through with only the TSi suffix so the
	// egress TT can still compute the residence on the Follow_Up leg.
	if err := gptp.IngressAtTT(m, tsiNS, linkDelayGMNS, neighborRateRatio); err != nil {
		return nil, err
	}
	return m.Encode(), nil
}

// GPTPEgress applies the egress-translator transform on a PDU leaving
// the 5GS at this NW-TT (UL direction): rateRatio-scaled residence
// time into correctionField, suffix stripped — §5.27.1.2.2.1 step 8.
// Returns the rewritten PDU and the residence time (5GS ns) for
// §5.27.1.12 monitoring.
func (n *NWTT) GPTPEgress(pdu []byte, tseNS uint64) ([]byte, uint64, error) {
	m, err := gptp.Decode(pdu)
	if err != nil {
		return nil, 0, err
	}
	residence, err := gptp.EgressAtTT(m, tseNS)
	if err != nil {
		return nil, 0, err
	}
	return m.Encode(), residence, nil
}

// GenerateGMSyncPair emits the Sync + Follow_Up pair for a (g)PTP
// domain the 5GS is grandmaster of (TS 23.501 §5.27.1.7 option a:
// NW-TT generates on behalf of the DS-TT leader ports). The clock
// identity derives from the 5GS User Plane Node ID (EUI-64 style).
// Returns nils when the BMCA elected an external grandmaster for the
// domain — the 5GS must not source Sync while a better master exists.
func (n *NWTT) GenerateGMSyncPair(domain byte, nowNS uint64) (sync, followUp []byte) {
	n.mu.Lock()
	if engine := n.bmca[domain]; engine != nil {
		engine.Tick(nowNS)
		if _, localGM := engine.States(); !localGM {
			n.mu.Unlock()
			return nil, nil
		}
	}
	n.mu.Unlock()
	var gm [8]byte
	binary.BigEndian.PutUint64(gm[:], n.nodeID)
	seq := uint16(n.gmSeq.Add(1))
	s, f := gptp.GenerateGMSync(gm, 1, domain, seq, nowNS)
	return s.Encode(), f.Encode()
}

// ── BMCA — TS 23.501 §5.27.1.6 method a) ──

// bmcaEngine returns (creating on demand) the per-domain BMCA with the
// local clock derived from the 5GS User Plane Node ID. Caller must
// hold n.mu.
func (n *NWTT) bmcaEngine(domain byte) *gptp.BMCA {
	e := n.bmca[domain]
	if e == nil {
		var id [8]byte
		binary.BigEndian.PutUint64(id[:], n.nodeID)
		gmCapable := true
		if v := n.nodeParams[ttmgmt.NodeParamGPTPGrandmasterCapable]; len(v) >= 1 {
			gmCapable = v[0] == 0x01
		}
		e = gptp.NewBMCA(id, 128, gmCapable)
		for _, p := range n.nwttPorts {
			e.EnsurePort(p)
		}
		n.bmca[domain] = e
	}
	return e
}

// ProcessAnnounce ingests a (g)PTP Announce PDU received on a port
// (NW-TT N6 port or a DS-TT port relayed over the user plane —
// TS 23.501 §5.27.1.6: "processes the Announce messages received on
// the NW-TT ports and from the DS-TTs"), runs the BMCA and refreshes
// the affected ports' portDS.portState (readable via PMIC parameter
// portDS.portState, TS 24.539 §9.15 0x0012).
func (n *NWTT) ProcessAnnounce(portNum uint16, pdu []byte, nowNS uint64) error {
	m, err := gptp.Decode(pdu)
	if err != nil {
		return err
	}
	if m.MessageType != gptp.MsgAnnounce || m.Announce == nil {
		return fmt.Errorf("nwtt: not an Announce (type %#x)", m.MessageType)
	}
	n.mu.Lock()
	e := n.bmcaEngine(m.DomainNumber)
	e.EnsurePort(portNum)
	e.ProcessAnnounce(portNum, m.Announce, nowNS)
	e.Tick(nowNS)
	states, localGM := e.States()
	// Mirror the recommended states into the port parameter stores so
	// the TSN AF reads live states over PMIC.
	for p, st := range states {
		port := n.ports[p]
		if port == nil {
			port = newNwttPortState()
			n.ports[p] = port
		}
		port.params[ttmgmt.PTPParamPortState] = []byte{byte(st)}
	}
	n.mu.Unlock()
	n.log.Infof("BMCA domain=%d: announce on port %d gm=%x localGM=%v states=%v (TS 23.501 §5.27.1.6)",
		m.DomainNumber, portNum, m.Announce.GrandmasterIdentity, localGM, states)
	return nil
}

// PortStates exposes the BMCA outcome for a domain (Leader/Follower/
// Passive per port) plus whether the 5GS clock is grandmaster.
func (n *NWTT) PortStates(domain byte, nowNS uint64) (map[uint16]gptp.PortState, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	e := n.bmca[domain]
	if e == nil {
		return nil, true
	}
	e.Tick(nowNS)
	return e.States()
}

// NodeID exposes the 5GS User Plane Node ID.
func (n *NWTT) NodeID() uint64 { return n.nodeID }

// encodeCreatedBridgePort builds the 4-octet §8.2.141 Port Number value.
func encodeCreatedBridgePort(port uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[2:], port)
	return b
}

// encodeNodeIDValue builds the §8.2.143 5GS User Plane Node ID value
// (flags octet BID=1 + Unsigned64).
func encodeNodeIDValue(id uint64) []byte {
	b := make([]byte, 9)
	b[0] = 0x01
	binary.BigEndian.PutUint64(b[1:], id)
	return b
}

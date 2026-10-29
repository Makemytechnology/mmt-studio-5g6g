// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package tsnaf — TSN Application Function (Rel-19).
//
// The TSN AF terminates the IEEE 802.1Qcc fully-centralized-model
// interface toward the CNC and represents the 5GS as an IEEE 802.1
// bridge (TS 23.501 §5.28.0). Responsibilities implemented here:
//
//	§5.28.1  5GS bridge information collection (bridge ID, DS-TT/NW-TT
//	         ports) from the user plane via PCF notifications;
//	§5.27.5  bridge delay computation per port pair per traffic class
//	         (UE-DS-TT residence time + preconfigured CN delays);
//	§5.28.2  CNC configuration intake (PSFP / scheduled traffic) and
//	         mapping to per-stream TSC QoS requests;
//	§5.28.3  PMIC/UMIC exchange with DS-TT and NW-TT (TS 24.539
//	         payloads, transported by PCF→SMF→N1/N4);
//	§5.28.4  TSN traffic class → 5GS QoS mapping (preconfigured table,
//	         PCF performs the final 5QI selection).
//
// Reference points (in-process form):
//
//	N5  — pcf.InstallTscPccRule / smpolicy.Update+PushNotify
//	      (Npcf_PolicyAuthorization per TS 29.514);
//	CNC — webservice /api/tsn/cnc/* REST surface.
package tsnaf

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/edge/tsn"
	"github.com/mmt/mmt-studio-core/edge/tsn/ttmgmt"
	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// TrafficClassQoS is one row of the preconfigured TSN traffic class →
// 5GS QoS mapping table (TS 23.501 §5.28.4: "the TSN AF is
// preconfigured with a mapping table").
type TrafficClassQoS struct {
	// Requested 5GS delay handed to the PCF, which picks the
	// delay-critical GBR 5QI (§5.27.3).
	DelayMS float64
	// TSC priority → ARP.
	Priority int
}

// Config is the TSN AF deployment configuration.
type Config struct {
	// CNDelayMinNS / CNDelayMaxNS — preconfigured per-traffic-class
	// UE↔UPF/NW-TT delays (UPF + NW-TT residence times), the CN
	// component of the §5.27.5 bridge delay. Index = traffic class 0-7.
	CNDelayMinNS [8]uint64
	CNDelayMaxNS [8]uint64
	// Default UE-DS-TT residence time bounds used when the UE did not
	// signal one (TS 23.501 §5.27.5: "locally configured value").
	DefaultResidenceMinNS uint64
	DefaultResidenceMaxNS uint64
	// TrafficClassMap — §5.28.4 preconfigured mapping table.
	TrafficClassMap [8]TrafficClassQoS
	// PortLinkSpeedBps — NW-TT/DS-TT port speed for the
	// frame-length-dependent delay component (dependentDelay = time to
	// receive one octet at link speed, IEEE 802.1Q §12.32.1).
	PortLinkSpeedBps uint64
}

// DefaultConfig mirrors a 1 Gb/s industrial deployment: CN delays of
// 300µs..2ms, residence-time fallback 100µs..500µs.
func DefaultConfig() Config {
	c := Config{
		DefaultResidenceMinNS: 100_000,
		DefaultResidenceMaxNS: 500_000,
		PortLinkSpeedBps:      1_000_000_000,
	}
	for tc := 0; tc < 8; tc++ {
		c.CNDelayMinNS[tc] = 300_000
		c.CNDelayMaxNS[tc] = 2_000_000
	}
	// Higher traffic class → tighter delay, higher priority
	// (aligned with the tsn.Map5QI GUI mapping: TC5→86, TC4→85 …).
	c.TrafficClassMap = [8]TrafficClassQoS{
		{DelayMS: 30, Priority: 8}, // TC0 — best effort-ish
		{DelayMS: 30, Priority: 7}, // TC1
		{DelayMS: 10, Priority: 6}, // TC2
		{DelayMS: 10, Priority: 5}, // TC3
		{DelayMS: 5, Priority: 4},  // TC4
		{DelayMS: 5, Priority: 3},  // TC5
		{DelayMS: 5, Priority: 2},  // TC6
		{DelayMS: 5, Priority: 1},  // TC7 — highest
	}
	return c
}

// Service is the TSN AF instance.
type Service struct {
	mu      sync.RWMutex
	cfg     Config
	bridges map[uint64]*Bridge // by 5GS bridge ID
	// portsByMac indexes DS-TT ports across bridges by MAC address —
	// the TSN AF addresses DS-TT ports by MAC (TS 23.501 §5.28.3.2).
	portsByMac map[string]*Port
	// streams — CNC-configured streams by stream ID.
	streams map[string]*Stream

	started bool
}

// Default is the singleton wired by the webservice bootstrap.
var Default = New(DefaultConfig())

// New builds a TSN AF with the given deployment config.
func New(cfg Config) *Service {
	return &Service{
		cfg:        cfg,
		bridges:    map[uint64]*Bridge{},
		portsByMac: map[string]*Port{},
		streams:    map[string]*Stream{},
	}
}

// Start subscribes the TSN AF to PCF TSC user plane events
// (TSN_BRIDGE_INFO / BAT_OFFSET_INFO notifications, TS 29.514 §5.6.3.7).
// Idempotent.
func (s *Service) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	pcf.RegisterTscUplaneListener(s.onTscEvent)
	logger.Get("tsnaf").Infof("TSN AF started (TS 23.501 §5.28, Rel-19)")
}

// onTscEvent ingests a PCF notification: bridge/port discovery
// (§5.28.1), PMIC/UMIC uplink (§5.28.3.2) and BAT offset feedback
// (§5.27.2.5).
func (s *Service) onTscEvent(ev pcf.TscUplaneEvent) {
	log := logger.Get("tsnaf").WithIMSI(ev.IMSI)

	if ev.SessionReleased {
		s.removeSession(ev.IMSI, ev.PDUSessionID)
		return
	}
	if ev.BridgeInfo != nil {
		s.upsertDsttPort(ev)
	}
	if ev.PortManContDstt != nil {
		s.handlePortPMIC(ev, *ev.PortManContDstt, true)
	}
	for _, pm := range ev.PortManContNwtts {
		s.handlePortPMIC(ev, pm, false)
	}
	if len(ev.BridgeManCont) > 0 {
		s.handleUMIC(ev)
	}
	if ev.DetectedMac != "" {
		// UE_MAC_CH: bind the newly detected MAC to the session's
		// DS-TT port so PMIC addressing by MAC keeps working for
		// stations behind the DS-TT (TS 23.501 §5.28.3.2).
		s.mu.Lock()
		for _, b := range s.bridges {
			for _, p := range b.Ports {
				if p.DSTT && p.IMSI == ev.IMSI && p.PDUSessionID == ev.PDUSessionID {
					s.portsByMac[ev.DetectedMac] = p
				}
			}
		}
		s.mu.Unlock()
		log.Infof("UE_MAC_CH: %s bound to pdu=%d (TS 29.512 §5.6.3.6)", ev.DetectedMac, ev.PDUSessionID)
	}
	for _, bo := range ev.BatOffsets {
		// EnTSCAC: BAT offset feedback would be relayed to the talker
		// via CNC in a full deployment; record it on the stream.
		s.mu.Lock()
		for _, st := range s.streams {
			if st.RuleName() == bo.RuleID {
				st.BatOffsetNS = bo.BatOffsetNS
				if bo.AdjustedPeriodicityUS > 0 {
					st.AdjustedPeriodicityUS = bo.AdjustedPeriodicityUS
				}
				log.Infof("stream %s: RAN BAT offset %dns adjPeriodicity=%dus (TS 23.501 §5.27.2.5)",
					st.StreamID, bo.BatOffsetNS, bo.AdjustedPeriodicityUS)
			}
		}
		s.mu.Unlock()
	}
}

// removeSession drops all TSN AF state bound to a released Ethernet
// PDU session: the DS-TT port, its MAC bindings, streams configured
// over the session, and the 5GS bridge itself once no DS-TT port
// remains (TS 23.501 §5.28.1 — the bridge exists per PDU session).
// Streams are removed locally only: the SM policy association died
// with the session, so there is no rule left to revoke.
func (s *Service) removeSession(imsi string, pduSessionID uint8) {
	log := logger.Get("tsnaf").WithIMSI(imsi)
	s.mu.Lock()
	var goneBridges []uint64
	for id, b := range s.bridges {
		changed := false
		for num, p := range b.Ports {
			if p.DSTT && p.IMSI == imsi && p.PDUSessionID == pduSessionID {
				delete(b.Ports, num)
				changed = true
			}
		}
		if !changed {
			continue
		}
		hasDstt := false
		for _, p := range b.Ports {
			if p.DSTT {
				hasDstt = true
				break
			}
		}
		if !hasDstt {
			delete(s.bridges, id)
			goneBridges = append(goneBridges, id)
		}
	}
	for mac, p := range s.portsByMac {
		if p.IMSI == imsi && p.PDUSessionID == pduSessionID {
			delete(s.portsByMac, mac)
		}
	}
	var goneStreams []string
	for id, st := range s.streams {
		if (st.IMSI == imsi && st.PDUSessionID == pduSessionID) ||
			(st.PeerIMSI == imsi && st.PeerPDUSessionID == pduSessionID) {
			delete(s.streams, id)
			goneStreams = append(goneStreams, id)
		}
	}
	s.mu.Unlock()

	for _, id := range goneBridges {
		bridgeID := pcf.FormatBridgeID(id)
		if existing, err := tsn.GetBridgeByBridgeID(bridgeID); err == nil && existing != nil {
			_ = tsn.UpdateBridgeStatus(existing.ID, "released")
		}
		log.Infof("5GS bridge %s released with PDU session %d (TS 23.501 §5.28.1)",
			bridgeID, pduSessionID)
	}
	for _, id := range goneStreams {
		log.Infof("stream %s dropped with PDU session %d", id, pduSessionID)
	}
}

// handlePortPMIC decodes a PMS message arriving from a DS-TT or NW-TT
// port (MANAGE PORT COMPLETE / PORT MANAGEMENT NOTIFY / CAPABILITY —
// TS 24.539 §5.2, §6.2) and folds the parameter values into the port
// state.
func (s *Service) handlePortPMIC(ev pcf.TscUplaneEvent, pm pcf.PortManCont, dstt bool) {
	log := logger.Get("tsnaf").WithIMSI(ev.IMSI)
	msg, err := ttmgmt.DecodePMS(pm.Container)
	if err != nil {
		log.Warnf("PMIC decode (port %d): %v", pm.PortNum, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	port := s.findPortLocked(ev, pm.PortNum, dstt)
	if port == nil {
		log.Warnf("PMIC for unknown port %d (dstt=%v)", pm.PortNum, dstt)
		return
	}
	if len(msg.Capabilities) > 0 {
		port.SupportedParams = msg.Capabilities
	}
	for _, st := range msg.Status {
		port.applyParamLocked(st.Param, st.Value)
	}
	for _, up := range msg.Updates {
		port.applyParamLocked(up.Param, up.Value)
	}
	log.Infof("PMIC in: port=%d dstt=%v type=0x%02x caps=%d status=%d updates=%d",
		pm.PortNum, dstt, msg.Type, len(msg.Capabilities), len(msg.Status), len(msg.Updates))
}

// handleUMIC decodes a UMS message from the NW-TT (TS 24.539 §6.3) and
// folds node-level parameters (node ID, NW-TT port list, sync state)
// into the bridge state.
func (s *Service) handleUMIC(ev pcf.TscUplaneEvent) {
	log := logger.Get("tsnaf").WithIMSI(ev.IMSI)
	msg, err := ttmgmt.DecodeUMS(ev.BridgeManCont)
	if err != nil {
		log.Warnf("UMIC decode: %v", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var b *Bridge
	if ev.BridgeInfo != nil {
		b = s.bridges[ev.BridgeInfo.BridgeID]
	}
	// Fall back to the node ID parameter inside the UMS payload.
	for _, st := range append(msg.Status, msg.Updates...) {
		if st.Param == ttmgmt.NodeParamID && len(st.Value) == 8 {
			id := uint64(st.Value[0])<<56 | uint64(st.Value[1])<<48 |
				uint64(st.Value[2])<<40 | uint64(st.Value[3])<<32 |
				uint64(st.Value[4])<<24 | uint64(st.Value[5])<<16 |
				uint64(st.Value[6])<<8 | uint64(st.Value[7])
			if b == nil {
				b = s.bridgeLocked(id)
			}
		}
	}
	if b == nil {
		log.Warnf("UMIC without resolvable bridge (type=0x%02x)", msg.Type)
		return
	}
	for _, st := range append(msg.Status, msg.Updates...) {
		switch st.Param {
		case ttmgmt.NodeParamNWTTPortNumbers:
			if ports, err := ttmgmt.DecodeNWTTPortNumbers(st.Value); err == nil {
				for _, p := range ports {
					b.ensureNWTTPortLocked(p)
				}
			}
		case ttmgmt.NodeParamSynchronizationState:
			if len(st.Value) >= 1 {
				b.SyncState = st.Value[0]
			}
		}
	}
	log.Infof("UMIC in: bridge=%s type=0x%02x params=%d",
		pcf.FormatBridgeID(b.BridgeID), msg.Type, len(msg.Status)+len(msg.Updates))
}

// upsertDsttPort records/refreshes the DS-TT port of a PDU session from
// a TSN_BRIDGE_INFO report (TS 23.502 §4.3.2.2.1 step 10b onward) and
// persists the bridge into the tsn_bridges GUI tables.
func (s *Service) upsertDsttPort(ev pcf.TscUplaneEvent) {
	bi := ev.BridgeInfo
	s.mu.Lock()
	b := s.bridgeLocked(bi.BridgeID)
	port := b.Ports[bi.DsttPortNum]
	if port == nil {
		port = &Port{Num: bi.DsttPortNum, DSTT: true, Bridge: b}
		b.Ports[bi.DsttPortNum] = port
	}
	port.MAC = bi.DsttAddr
	port.ResidenceTimeNS = bi.DsttResidTimeNS
	port.IMSI = ev.IMSI
	port.PDUSessionID = ev.PDUSessionID
	port.DNN = ev.DNN
	if bi.DsttAddr != "" {
		s.portsByMac[bi.DsttAddr] = port
	}
	// NW-TT (N6-side) ports from the Created Bridge Info — needed for
	// the §5.27.5 per-port-pair bridge delay report toward the CNC.
	for _, num := range bi.NwttPortNums {
		if b.Ports[num] == nil {
			b.Ports[num] = &Port{Num: num, Bridge: b}
		}
	}
	s.mu.Unlock()

	logger.Get("tsnaf").WithIMSI(ev.IMSI).Infof(
		"5GS bridge %s: DS-TT port %d mac=%s residence=%dns pdu=%d (TS 23.501 §5.28.1)",
		pcf.FormatBridgeID(bi.BridgeID), bi.DsttPortNum, bi.DsttAddr,
		bi.DsttResidTimeNS, ev.PDUSessionID)

	s.persistBridge(b)
}

// persistBridge mirrors the in-memory bridge into the edge/tsn DB so
// the existing GUI panels (routes_edge.go) show live TSC state.
func (s *Service) persistBridge(b *Bridge) {
	s.mu.RLock()
	bridgeID := pcf.FormatBridgeID(b.BridgeID)
	var dsttPort, nwttPort string
	for _, p := range b.Ports {
		if p.DSTT && dsttPort == "" {
			dsttPort = fmt.Sprintf("%d", p.Num)
		}
		if !p.DSTT && nwttPort == "" {
			nwttPort = fmt.Sprintf("%d", p.Num)
		}
	}
	s.mu.RUnlock()

	if existing, err := tsn.GetBridgeByBridgeID(bridgeID); err == nil && existing == nil {
		if _, err := tsn.CreateBridge(bridgeID, "5GS bridge "+bridgeID, dsttPort, nwttPort, nil); err != nil {
			logger.Get("tsnaf").Warnf("persist bridge %s: %v", bridgeID, err)
		}
	} else if existing != nil {
		_ = tsn.UpdateBridgeStatus(existing.ID, "active")
	}
}

// findPortLocked resolves the port a PMIC belongs to.
func (s *Service) findPortLocked(ev pcf.TscUplaneEvent, portNum uint16, dstt bool) *Port {
	if ev.BridgeInfo != nil {
		if b := s.bridges[ev.BridgeInfo.BridgeID]; b != nil {
			if p := b.Ports[portNum]; p != nil {
				return p
			}
		}
	}
	for _, b := range s.bridges {
		if p := b.Ports[portNum]; p != nil && p.DSTT == dstt {
			if !dstt || (p.IMSI == ev.IMSI && p.PDUSessionID == ev.PDUSessionID) {
				return p
			}
		}
	}
	// NW-TT ports may legitimately appear before any explicit report.
	if !dstt && ev.BridgeInfo != nil {
		b := s.bridgeLocked(ev.BridgeInfo.BridgeID)
		return b.ensureNWTTPortLocked(portNum)
	}
	return nil
}

func (s *Service) bridgeLocked(id uint64) *Bridge {
	b := s.bridges[id]
	if b == nil {
		b = &Bridge{BridgeID: id, Ports: map[uint16]*Port{}}
		s.bridges[id] = b
	}
	return b
}

// SendPMICToDstt ships a TS 24.539 PMS message to a DS-TT port addressed
// by MAC (TS 23.501 §5.28.3.2 "TSN AF to DS-TT" path: PCF → SMF → N1).
func (s *Service) SendPMICToDstt(mac string, msg *ttmgmt.Message) error {
	s.mu.RLock()
	port := s.portsByMac[mac]
	s.mu.RUnlock()
	if port == nil {
		return fmt.Errorf("tsnaf: no DS-TT port with MAC %s", mac)
	}
	raw, err := msg.Encode()
	if err != nil {
		return err
	}
	k := smfsm.Key{IMSI: port.IMSI, PDUSessionID: port.PDUSessionID}
	return smpolicy.PushTscManagement(k,
		&pcf.PortManCont{Container: raw, PortNum: port.Num}, nil, nil)
}

// SendPMICToNwtt ships a PMS message to an NW-TT port (path: PCF → SMF →
// N4 TSC Management Information). Any PDU session anchored on the bridge
// carries it (TS 23.501 §5.28.3.2: "the TSN AF selects the PCF-AF
// session corresponding to any of the DS-TT MAC addresses").
func (s *Service) SendPMICToNwtt(bridgeID uint64, portNum uint16, msg *ttmgmt.Message) error {
	k, err := s.anySessionKey(bridgeID)
	if err != nil {
		return err
	}
	raw, err := msg.Encode()
	if err != nil {
		return err
	}
	return smpolicy.PushTscManagement(k, nil,
		[]pcf.PortManCont{{Container: raw, PortNum: portNum}}, nil)
}

// SendUMIC ships a UMS message to the NW-TT of a bridge.
func (s *Service) SendUMIC(bridgeID uint64, msg *ttmgmt.Message) error {
	k, err := s.anySessionKey(bridgeID)
	if err != nil {
		return err
	}
	raw, err := msg.Encode()
	if err != nil {
		return err
	}
	return smpolicy.PushTscManagement(k, nil, nil, raw)
}

func (s *Service) anySessionKey(bridgeID uint64) (smfsm.Key, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b := s.bridges[bridgeID]
	if b == nil {
		return smfsm.Key{}, fmt.Errorf("tsnaf: unknown bridge %d", bridgeID)
	}
	for _, p := range b.Ports {
		if p.DSTT && p.IMSI != "" {
			return smfsm.Key{IMSI: p.IMSI, PDUSessionID: p.PDUSessionID}, nil
		}
	}
	return smfsm.Key{}, fmt.Errorf("tsnaf: bridge %d has no PDU session", bridgeID)
}

// Status summarises the TSN AF state for the GUI / REST surface.
func (s *Service) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nPorts := 0
	for _, b := range s.bridges {
		nPorts += len(b.Ports)
	}
	return map[string]any{
		"bridges": len(s.bridges),
		"ports":   nPorts,
		"streams": len(s.streams),
	}
}

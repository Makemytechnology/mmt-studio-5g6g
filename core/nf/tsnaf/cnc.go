// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// CNC-facing stream configuration — TS 23.501 §5.28.2 (configuration of
// 5GS bridge), §5.27.2 (TSC Assistance Container derivation) and
// §5.28.4 (QoS mapping). The CNC of the IEEE 802.1Qcc fully centralized
// model pushes PSFP stream filters + transmission gate parameters; the
// TSN AF turns them into per-stream TSC QoS toward the PCF and gate
// schedules toward the DS-TT/NW-TT (PMIC).
package tsnaf

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/edge/tsn"
	"github.com/mmt/mmt-studio-core/edge/tsn/ttmgmt"
	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// StreamConfig is the CNC input for one TSN stream (802.1Qcc talker/
// listener pairing distilled to the 5GS-relevant attributes;
// TS 23.501 §5.28.2).
type StreamConfig struct {
	StreamID string `json:"stream_id"`
	BridgeID uint64 `json:"bridge_id"`
	// IngressPort / EgressPort — bridge port numbers from the §5.28.1
	// report. Ingress DS-TT → UL flow, ingress NW-TT → DL flow
	// (TS 23.501 §5.28.2 flow-direction rule).
	IngressPort uint16 `json:"ingress_port"`
	EgressPort  uint16 `json:"egress_port"`
	// PSFP stream filter parameters (IEEE 802.1Q §8.6.5.2).
	TrafficClass int `json:"traffic_class"`
	Priority     int `json:"priority"`
	// Scheduled-traffic hints (IEEE 802.1Q Annex Q.2): burst estimation
	// per §5.28.2 NOTE 4.
	MaxFrameSizeOctets int `json:"max_frame_size"`
	FramesPerInterval  int `json:"frames_per_interval"`
	IntervalUS         int `json:"interval_us"`
	// BurstArrivalTimeNS — burst arrival at the 5GS ingress port in the
	// TSN GM time domain (feeds the TSCAC; 0 = not provided).
	BurstArrivalTimeNS int64 `json:"burst_arrival_time_ns"`
	// TimeDomain — (g)PTP domain of the TSN working domain.
	TimeDomain uint64 `json:"time_domain"`
	// DestMAC / VLANID — IEEE 802.1Qcc stream identification (null
	// stream identification: destination MAC + VLAN). Propagated to
	// the PCC rule and installed as a PDI Ethernet Packet Filter at
	// the UPF (TS 29.244 §7.5.2.2).
	DestMAC string `json:"dest_mac,omitempty"`
	VLANID  int    `json:"vlan_id,omitempty"`
	// Gate schedule to program on the egress translator port
	// (hold & forward, TS 23.501 §5.27.4). Optional.
	GateSchedule []GateWindow `json:"gate_schedule,omitempty"`
	GateBaseTime int64        `json:"gate_base_time_ns,omitempty"`
	GateCycleNS  uint32       `json:"gate_cycle_ns,omitempty"`
}

// GateWindow is one AdminControlList window.
type GateWindow struct {
	GateStates byte   `json:"gate_states"` // bit N = traffic class N open
	DurationNS uint32 `json:"duration_ns"`
}

// Stream is the TSN AF's record of a configured stream.
type Stream struct {
	StreamConfig
	IMSI         string `json:"imsi"`
	PDUSessionID uint8  `json:"pdu_session_id"`
	DNN          string `json:"dnn"`
	Direction    string `json:"direction"` // "UL" | "DL"
	FiveQI       int    `json:"five_qi"`
	// EnTSCAC RAN feedback (§5.27.2.5): the BAT offset (and optionally
	// adjusted periodicity) the NG-RAN reported so the CNC can realign
	// the talker's sending time. 0 = no feedback yet.
	BatOffsetNS           int64  `json:"bat_offset_ns"`
	AdjustedPeriodicityUS uint64 `json:"adjusted_periodicity_us,omitempty"`

	// UE-UE split (§5.28.2): when both stream ports are DS-TT ports the
	// stream is split into an UL half on the talker's PDU session (the
	// main IMSI/PDUSessionID binding above) and a DL half on the
	// listener's session, recorded here. Empty for UL-only / DL-only.
	PeerIMSI         string `json:"peer_imsi,omitempty"`
	PeerPDUSessionID uint8  `json:"peer_pdu_session_id,omitempty"`
	PeerDNN          string `json:"peer_dnn,omitempty"`
}

// PeerRuleName derives the PCC rule id of the DL half of a UE-UE
// stream on the listener's session.
func (st *Stream) PeerRuleName() string { return "tsn_" + st.StreamID + "_dl" }

// RuleName derives the PCC rule id for this stream.
func (st *Stream) RuleName() string { return "tsn_" + st.StreamID }

// ConfigureStream implements the CNC → TSN AF configuration flow of
// TS 23.501 §5.28.2:
//
//  1. resolve ingress/egress ports on the bridge and derive the flow
//     direction (ingress DS-TT ⇒ UL, ingress NW-TT ⇒ DL);
//  2. identify the PDU session via the DS-TT port;
//  3. build the TSC Assistance Container inputs (§5.27.2 Table
//     5.27.2-2: flow direction, periodicity, burst arrival time at the
//     5GS ingress, time domain) and the TSC QoS request (§5.28.4:
//     burst size, requested delay from the preconfigured traffic-class
//     table, priority);
//  4. install the PCC rule over N5 and push the refreshed decision to
//     the SMF (Npcf_SMPolicyControl UpdateNotify), which derives TSCAI
//     toward NG-RAN (§5.27.2.4);
//  5. optionally program the egress gate schedule via PMIC (§5.27.4).
func (s *Service) ConfigureStream(cfg StreamConfig) (*Stream, error) {
	log := logger.Get("tsnaf.cnc")
	if cfg.StreamID == "" {
		return nil, fmt.Errorf("tsnaf: stream_id required")
	}
	if cfg.IntervalUS <= 0 {
		cfg.IntervalUS = 1000
	}
	if cfg.MaxFrameSizeOctets <= 0 {
		cfg.MaxFrameSizeOctets = 1522
	}
	if cfg.FramesPerInterval <= 0 {
		cfg.FramesPerInterval = 1
	}

	s.mu.RLock()
	b := s.bridges[cfg.BridgeID]
	var in, eg *Port
	if b != nil {
		in, eg = b.Ports[cfg.IngressPort], b.Ports[cfg.EgressPort]
	}
	s.mu.RUnlock()
	if b == nil {
		return nil, fmt.Errorf("tsnaf: unknown bridge %d", cfg.BridgeID)
	}
	if in == nil || eg == nil {
		return nil, fmt.Errorf("tsnaf: bridge %d missing port(s) %d/%d",
			cfg.BridgeID, cfg.IngressPort, cfg.EgressPort)
	}

	// §5.28.2: flow direction from the ingress port type. The DS-TT
	// port identifies the PDU session. UE-UE streams (both ports
	// DS-TT) are split into an UL half on the talker's session and a
	// DL half on the listener's session — handled below after the QoS
	// derivation.
	var sessPort, peerPort *Port
	direction := "DL"
	switch {
	case in.DSTT && eg.DSTT:
		direction = "UE-UE"
		sessPort, peerPort = in, eg
		if in.IMSI == eg.IMSI && in.PDUSessionID == eg.PDUSessionID {
			return nil, fmt.Errorf("tsnaf: UE-UE stream %s has both ports on the same PDU session", cfg.StreamID)
		}
	case in.DSTT:
		direction = "UL"
		sessPort = in
	case eg.DSTT:
		sessPort = eg
	default:
		return nil, fmt.Errorf("tsnaf: neither port of stream %s is a DS-TT port", cfg.StreamID)
	}
	if sessPort.IMSI == "" {
		return nil, fmt.Errorf("tsnaf: DS-TT port %d has no PDU session binding", sessPort.Num)
	}
	if peerPort != nil && peerPort.IMSI == "" {
		return nil, fmt.Errorf("tsnaf: egress DS-TT port %d has no PDU session binding", peerPort.Num)
	}

	// §5.28.4: preconfigured traffic class → QoS mapping.
	tcq := s.cfg.TrafficClassMap[cfg.TrafficClass&7]
	burst := cfg.MaxFrameSizeOctets * cfg.FramesPerInterval
	// Maximum Flow Bitrate from the schedule (TS 23.501 Annex I-ish:
	// burst per cycle / cycle time), kbit/s, rounded up.
	kbps := int((uint64(burst)*8*1_000_000/uint64(cfg.IntervalUS) + 999) / 1000)

	tscai := &pcf.TscaiInput{
		PeriodicityUS:      uint64(cfg.IntervalUS),
		BurstArrivalTimeNS: cfg.BurstArrivalTimeNS,
	}
	req := pcf.TscQosRequirement{
		Req5GSDelayMS:         tcq.DelayMS,
		MaxTscBurstSizeOctets: burst,
		Priority:              tcq.Priority,
		TscaiTimeDom:          cfg.TimeDomain,
		EthDestMAC:            cfg.DestMAC,
		EthVLANID:             cfg.VLANID,
	}
	switch direction {
	case "UL", "UE-UE":
		// UE-UE: the talker's session carries the UL half
		// (TS 23.501 §5.28.2: UL from talker DS-TT to the UPF).
		req.ReqGbrUlKbps = kbps
		req.TscaiInputUl = tscai
	default:
		req.ReqGbrDlKbps = kbps
		req.TscaiInputDl = tscai
	}

	st := &Stream{
		StreamConfig: cfg,
		IMSI:         sessPort.IMSI,
		PDUSessionID: sessPort.PDUSessionID,
		DNN:          sessPort.DNN,
		Direction:    direction,
	}
	if peerPort != nil {
		st.PeerIMSI = peerPort.IMSI
		st.PeerPDUSessionID = peerPort.PDUSessionID
		st.PeerDNN = peerPort.DNN
	}

	// N5: install the dynamic PCC rule, then drive the N7 update the
	// same way the IMS AF does (Update with RES_MO_RE + UpdateNotify).
	rule := pcf.InstallTscPccRule(sessPort.IMSI, sessPort.DNN, st.RuleName(), req)
	st.FiveQI = rule.FiveQI
	k := smfsm.Key{IMSI: sessPort.IMSI, PDUSessionID: sessPort.PDUSessionID}
	decision, err := smpolicy.Update(k, smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{"RES_MO_RE"},
	})
	if err != nil {
		pcf.RemoveTscPccRule(sessPort.IMSI, sessPort.DNN, st.RuleName())
		return nil, fmt.Errorf("tsnaf: N7 update: %w", err)
	}
	if err := smpolicy.PushNotify(k, decision); err != nil {
		log.Warnf("stream %s: UpdateNotify: %v", cfg.StreamID, err)
	}

	// §5.28.2 UE-UE: the DL half toward the listener DS-TT rides the
	// listener's PDU session as its own dynamic PCC rule with DL
	// TSCAI. Rollback the UL half if the listener leg fails so the
	// CNC never sees a half-configured stream.
	if peerPort != nil {
		reqDL := req
		reqDL.ReqGbrUlKbps, reqDL.TscaiInputUl = 0, nil
		reqDL.ReqGbrDlKbps, reqDL.TscaiInputDl = kbps, tscai
		pcf.InstallTscPccRule(peerPort.IMSI, peerPort.DNN, st.PeerRuleName(), reqDL)
		pk := smfsm.Key{IMSI: peerPort.IMSI, PDUSessionID: peerPort.PDUSessionID}
		pdec, perr := smpolicy.Update(pk, smpolicy.SmPolicyContextDataUpdate{
			Triggers: []string{"RES_MO_RE"},
		})
		if perr != nil {
			pcf.RemoveTscPccRule(peerPort.IMSI, peerPort.DNN, st.PeerRuleName())
			pcf.RemoveTscPccRule(sessPort.IMSI, sessPort.DNN, st.RuleName())
			return nil, fmt.Errorf("tsnaf: N7 update (UE-UE DL half): %w", perr)
		}
		if err := smpolicy.PushNotify(pk, pdec); err != nil {
			log.Warnf("stream %s: UpdateNotify (DL half): %v", cfg.StreamID, err)
		}
	}

	// §5.27.4 hold & forward: program the egress gate via PMIC.
	if len(cfg.GateSchedule) > 0 {
		if err := s.programGate(st, eg); err != nil {
			log.Warnf("stream %s: gate programming: %v", cfg.StreamID, err)
		}
	}

	s.mu.Lock()
	s.streams[cfg.StreamID] = st
	s.mu.Unlock()
	s.persistStream(st, b)

	log.Infof("stream %s configured: bridge=%s dir=%s 5qi=%d burst=%dB gbr=%dkbps periodicity=%dus (TS 23.501 §5.28.2)",
		cfg.StreamID, pcf.FormatBridgeID(cfg.BridgeID), direction, st.FiveQI, burst, kbps, cfg.IntervalUS)
	return st, nil
}

// programGate ships the IEEE 802.1Q scheduled-traffic parameters to the
// egress translator port as PMIC set-parameter operations (TS 24.539
// table 9.2.1: GateEnabled, AdminBaseTime, AdminControlList{,Length},
// AdminCycleTime — TS 23.501 §5.27.4 hold & forward).
func (s *Service) programGate(st *Stream, eg *Port) error {
	entries := make([]ttmgmt.GateControlEntry, 0, len(st.GateSchedule))
	for _, w := range st.GateSchedule {
		entries = append(entries, ttmgmt.GateControlEntry{
			GateStates:     w.GateStates,
			TimeIntervalNS: w.DurationNS,
		})
	}
	cycle := st.GateCycleNS
	if cycle == 0 {
		for _, w := range st.GateSchedule {
			cycle += w.DurationNS
		}
	}
	baseSec := uint64(st.GateBaseTime / 1_000_000_000)
	baseNS := uint32(st.GateBaseTime % 1_000_000_000)
	msg := &ttmgmt.Message{
		Type: ttmgmt.MsgManagePortCommand,
		Ops: []ttmgmt.Operation{
			{Code: ttmgmt.OpSetParameter, Param: ttmgmt.PortParamGateEnabled, Value: ttmgmt.EncodeBool(true)},
			{Code: ttmgmt.OpSetParameter, Param: ttmgmt.PortParamAdminBaseTime, Value: ttmgmt.EncodeAdminBaseTime(baseSec, baseNS)},
			{Code: ttmgmt.OpSetParameter, Param: ttmgmt.PortParamAdminControlListLength, Value: ttmgmt.EncodeUint32(uint32(len(entries)))},
			{Code: ttmgmt.OpSetParameter, Param: ttmgmt.PortParamAdminControlList, Value: ttmgmt.EncodeAdminControlList(entries)},
			{Code: ttmgmt.OpSetParameter, Param: ttmgmt.PortParamAdminCycleTime, Value: ttmgmt.EncodeUint64(uint64(cycle))},
		},
	}
	if eg.DSTT {
		return s.SendPMICToDstt(eg.MAC, msg)
	}
	return s.SendPMICToNwtt(st.BridgeID, eg.Num, msg)
}

// RemoveStream tears a stream down: PCC rule removal + N7 push + gate
// disable on the egress port.
func (s *Service) RemoveStream(streamID string) error {
	s.mu.Lock()
	st := s.streams[streamID]
	delete(s.streams, streamID)
	s.mu.Unlock()
	if st == nil {
		return fmt.Errorf("tsnaf: unknown stream %s", streamID)
	}
	removed := pcf.RemoveTscPccRule(st.IMSI, st.DNN, st.RuleName())
	if st.PeerIMSI != "" {
		// UE-UE (§5.28.2): revoke the DL half on the listener session.
		if pr := pcf.RemoveTscPccRule(st.PeerIMSI, st.PeerDNN, st.PeerRuleName()); pr {
			pk := smfsm.Key{IMSI: st.PeerIMSI, PDUSessionID: st.PeerPDUSessionID}
			if pdec, perr := smpolicy.Update(pk, smpolicy.SmPolicyContextDataUpdate{
				Triggers: []string{"RES_RELEASE"},
			}); perr == nil {
				pdec.RemovedPccRules = append(pdec.RemovedPccRules,
					pcf.PCCRule{ServiceName: st.PeerRuleName()})
				_ = smpolicy.PushNotify(pk, pdec)
			}
		}
	}
	k := smfsm.Key{IMSI: st.IMSI, PDUSessionID: st.PDUSessionID}
	decision, err := smpolicy.Update(k, smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{"RES_RELEASE"},
	})
	if err == nil {
		if removed {
			decision.RemovedPccRules = append(decision.RemovedPccRules,
				pcf.PCCRule{ServiceName: st.RuleName()})
		}
		_ = smpolicy.PushNotify(k, decision)
	}
	if dbStream, dbErr := tsn.GetStreamByStreamID(streamID); dbErr == nil && dbStream != nil {
		_ = tsn.DeleteStream(dbStream.ID)
	}
	logger.Get("tsnaf.cnc").Infof("stream %s removed (rule=%v)", streamID, removed)
	return nil
}

// Streams lists configured streams.
func (s *Service) Streams() []*Stream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Stream, 0, len(s.streams))
	for _, st := range s.streams {
		out = append(out, st)
	}
	return out
}

// StreamStatus returns the CNC-facing status of one stream, including
// the EnTSCAC RAN feedback (BAT offset / adjusted periodicity) so a
// talker can be realigned (TS 23.501 §5.27.2.5). ok=false when unknown.
func (s *Service) StreamStatus(streamID string) (map[string]any, bool) {
	s.mu.RLock()
	st := s.streams[streamID]
	s.mu.RUnlock()
	if st == nil {
		return nil, false
	}
	return map[string]any{
		"stream_id":               st.StreamID,
		"direction":               st.Direction,
		"five_qi":                 st.FiveQI,
		"interval_us":             st.IntervalUS,
		"bat_offset_ns":           st.BatOffsetNS,
		"adjusted_periodicity_us": st.AdjustedPeriodicityUS,
		"imsi":                    st.IMSI,
		"pdu_session_id":          st.PDUSessionID,
	}, true
}

// persistStream mirrors the stream into the edge/tsn GUI tables.
func (s *Service) persistStream(st *Stream, b *Bridge) {
	bridgeRow, err := tsn.GetBridgeByBridgeID(pcf.FormatBridgeID(b.BridgeID))
	if err != nil || bridgeRow == nil {
		return
	}
	if existing, _ := tsn.GetStreamByStreamID(st.StreamID); existing != nil {
		return
	}
	fiveQI := st.FiveQI
	pdb := 0.0
	if v, _, ok := pcf.FiveQICharacteristics(fiveQI); ok {
		pdb = v
	}
	if _, err := tsn.CreateStream(bridgeRow.ID, st.StreamID, st.TrafficClass,
		st.Priority, st.MaxFrameSizeOctets, st.IntervalUS, &fiveQI, &pdb); err != nil {
		logger.Get("tsnaf.cnc").Warnf("persist stream %s: %v", st.StreamID, err)
	}
}

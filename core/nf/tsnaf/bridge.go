// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GS bridge model and bridge delay computation — TS 23.501 §5.28.1
// (bridge composition/reporting) and §5.27.5 (bridge delay).
package tsnaf

import (
	"fmt"
	"sort"

	"github.com/mmt/mmt-studio-core/edge/tsn/ttmgmt"
	"github.com/mmt/mmt-studio-core/nf/pcf"
)

// Bridge is one 5GS bridge: a single UPF (PSA) with its DS-TT ports
// (one per Ethernet PDU session) and NW-TT ports (N6 side) —
// TS 23.501 §5.28.1: "granularity of a 5GS bridge is per UPF".
type Bridge struct {
	BridgeID uint64
	Ports    map[uint16]*Port
	// SyncState — node-level synchronization state reported through
	// UMIC (TS 24.539 parameter 0x0090; TS 23.501 §5.27.1.12 TSS).
	SyncState byte
}

// Port is one bridge port (DS-TT or NW-TT).
type Port struct {
	Num    uint16
	DSTT   bool
	Bridge *Bridge

	// DS-TT specifics
	MAC             string
	ResidenceTimeNS uint64 // UE-DS-TT residence time (0 = not provided)
	IMSI            string
	PDUSessionID    uint8
	DNN             string

	// Learned through PMIC (TS 24.539)
	SupportedParams      []uint16
	TxPropagationDelayNS float64 // parameter 0x0001
	TSNTimeDomain        byte    // parameter 0x00D4
	GateEnabled          bool    // parameter 0x0003
}

// applyParamLocked folds a PMIC-reported parameter value into the port
// state. Caller holds the service lock.
func (p *Port) applyParamLocked(param uint16, value []byte) {
	switch param {
	case ttmgmt.PortParamTxPropagationDelay:
		if d, err := ttmgmt.DecodePropagationDelay(value); err == nil {
			p.TxPropagationDelayNS = d
		}
	case ttmgmt.PortParamTSNTimeDomainNumber:
		if len(value) >= 1 {
			p.TSNTimeDomain = value[0]
		}
	case ttmgmt.PortParamGateEnabled:
		p.GateEnabled = len(value) >= 1 && value[0] == 0x01
	}
}

func (b *Bridge) ensureNWTTPortLocked(num uint16) *Port {
	p := b.Ports[num]
	if p == nil {
		p = &Port{Num: num, DSTT: false, Bridge: b}
		b.Ports[num] = p
	}
	return p
}

// BridgeDelay is the IEEE 802.1Q clause 12.32.1 bridge-delay tuple the
// TSN AF reports to the CNC per port pair per traffic class
// (TS 23.501 §5.27.5).
type BridgeDelay struct {
	IngressPort  uint16  `json:"ingress_port"`
	EgressPort   uint16  `json:"egress_port"`
	TrafficClass int     `json:"traffic_class"`
	IndepMinNS   uint64  `json:"independent_delay_min_ns"`
	IndepMaxNS   uint64  `json:"independent_delay_max_ns"`
	DepMinNSPerB float64 `json:"dependent_delay_min_ns_per_octet"`
	DepMaxNSPerB float64 `json:"dependent_delay_max_ns_per_octet"`
}

// portPairDelay computes the §5.27.5 delay for one (ingress, egress)
// pair and traffic class:
//
//	independentDelay = UE-DS-TT residence time (UE-provided, else the
//	                   configured min/max) + preconfigured per-traffic-
//	                   class CN min/max delay (UPF+NW-TT residence);
//	DS-TT↔DS-TT     = sum of both PDU sessions' components;
//	dependentDelay   = per-octet store time at the port link speed.
func (s *Service) portPairDelay(in, eg *Port, tc int) BridgeDelay {
	resid := func(p *Port) (uint64, uint64) {
		if p.DSTT {
			if p.ResidenceTimeNS > 0 {
				return p.ResidenceTimeNS, p.ResidenceTimeNS
			}
			return s.cfg.DefaultResidenceMinNS, s.cfg.DefaultResidenceMaxNS
		}
		return 0, 0
	}
	minNS := s.cfg.CNDelayMinNS[tc&7]
	maxNS := s.cfg.CNDelayMaxNS[tc&7]
	inMin, inMax := resid(in)
	egMin, egMax := resid(eg)
	// DS-TT→NW-TT and NW-TT→DS-TT include one residence component;
	// DS-TT→DS-TT includes both (TS 23.501 §5.27.5: "sum of the bridge
	// delays of the related PDU Sessions").
	minNS += inMin + egMin
	maxNS += inMax + egMax
	if in.DSTT && eg.DSTT {
		minNS += s.cfg.CNDelayMinNS[tc&7]
		maxNS += s.cfg.CNDelayMaxNS[tc&7]
	}
	perOctetNS := 8e9 / float64(s.cfg.PortLinkSpeedBps)
	return BridgeDelay{
		IngressPort:  in.Num,
		EgressPort:   eg.Num,
		TrafficClass: tc,
		IndepMinNS:   minNS,
		IndepMaxNS:   maxNS,
		DepMinNSPerB: perOctetNS,
		DepMaxNSPerB: perOctetNS,
	}
}

// BridgeCapabilities is the §5.28.1 report toward the CNC: bridge ID,
// port list, per-port-pair delays and PSFP/gate parameters.
type BridgeCapabilities struct {
	BridgeID     string           `json:"bridge_id"`
	BridgeIDNum  uint64           `json:"bridge_id_num"`
	Ports        []PortReport     `json:"ports"`
	BridgeDelays []BridgeDelay    `json:"bridge_delays"`
	SyncState    byte             `json:"sync_state"`
	Streams      []map[string]any `json:"streams,omitempty"`
}

// PortReport is one port entry of the CNC report.
type PortReport struct {
	Num                  uint16  `json:"port"`
	Kind                 string  `json:"kind"` // "ds-tt" | "nw-tt"
	MAC                  string  `json:"mac,omitempty"`
	ResidenceTimeNS      uint64  `json:"residence_time_ns,omitempty"`
	TxPropagationDelayNS float64 `json:"tx_propagation_delay_ns,omitempty"`
	GateEnabled          bool    `json:"gate_enabled"`
}

// Capabilities builds the CNC-facing bridge report (TS 23.501 §5.28.1:
// bridge ID, ports, bridge delay per port pair per traffic class,
// txPropagationDelay per port).
func (s *Service) Capabilities(bridgeID uint64) (*BridgeCapabilities, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b := s.bridges[bridgeID]
	if b == nil {
		return nil, fmt.Errorf("tsnaf: unknown bridge %d", bridgeID)
	}
	rep := &BridgeCapabilities{
		BridgeID:    pcf.FormatBridgeID(b.BridgeID),
		BridgeIDNum: b.BridgeID,
		SyncState:   b.SyncState,
	}
	var nums []int
	for n := range b.Ports {
		nums = append(nums, int(n))
	}
	sort.Ints(nums)
	for _, n := range nums {
		p := b.Ports[uint16(n)]
		kind := "nw-tt"
		if p.DSTT {
			kind = "ds-tt"
		}
		rep.Ports = append(rep.Ports, PortReport{
			Num: p.Num, Kind: kind, MAC: p.MAC,
			ResidenceTimeNS:      p.ResidenceTimeNS,
			TxPropagationDelayNS: p.TxPropagationDelayNS,
			GateEnabled:          p.GateEnabled,
		})
	}
	// Delays for every ordered port pair, per traffic class.
	for _, ni := range nums {
		for _, ne := range nums {
			if ni == ne {
				continue
			}
			in, eg := b.Ports[uint16(ni)], b.Ports[uint16(ne)]
			for tc := 0; tc < 8; tc++ {
				rep.BridgeDelays = append(rep.BridgeDelays, s.portPairDelay(in, eg, tc))
			}
		}
	}
	return rep, nil
}

// Bridges lists the known 5GS bridges (CNC discovery surface).
func (s *Service) Bridges() []map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []map[string]any
	for _, b := range s.bridges {
		nDstt, nNwtt := 0, 0
		for _, p := range b.Ports {
			if p.DSTT {
				nDstt++
			} else {
				nNwtt++
			}
		}
		out = append(out, map[string]any{
			"bridge_id":     pcf.FormatBridgeID(b.BridgeID),
			"bridge_id_num": b.BridgeID,
			"ds_tt_ports":   nDstt,
			"nw_tt_ports":   nNwtt,
		})
	}
	return out
}

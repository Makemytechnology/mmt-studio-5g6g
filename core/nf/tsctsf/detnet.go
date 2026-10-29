// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// DetNet controller intake — TS 23.501 §5.28.5 (5GS as a DetNet
// transit node) and TS 23.503 §6.1.3.23b (PCF/TSCTSF support of IETF
// Deterministic Networking).
//
// The DetNet controller provisions flows against the TSCTSF using the
// IETF DetNet YANG model (RFC 9633); this file accepts the JSON form
// of the relevant subtree (app-flow + traffic-specification with the
// 3GPP augmentations) and maps it onto a TSC QoS requirement per the
// §6.1.3.23b table:
//
//	5GS-node-max-latency            → Requested 5GS Delay
//	min-bandwidth                   → Requested Guaranteed Bitrate
//	max-consecutive-loss-tolerance  → Survival time (in messages)
//	interval                        → Periodicity
//	max-pkts-per-interval ×
//	  (max-payload-size + header)   → Maximum TSC Burst Size
//	  ... / interval                → Requested Maximum Bitrate
//	DetNet flow spec (src/dst IP,
//	  protocol, ports)              → Flow Description
package tsctsf

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// detnetIPHeaderOctets is the header allowance added to
// max-payload-size when deriving the burst size (§6.1.3.23b:
// "max-payload-size + header"); IPv4+UDP without options.
const detnetIPHeaderOctets = 28

// DetNetFlow is the JSON form of one RFC 9633 app-flow with its
// traffic-specification and the 3GPP extensions of §6.1.3.23b.
type DetNetFlow struct {
	// name — app-flow name (RFC 9633 §4.1); the TSC app session id
	// derives from it.
	Name string `json:"name"`

	// ── traffic-specification (RFC 9633) ──
	IntervalUS         uint64 `json:"interval_us"`           // interval
	MaxPktsPerInterval int    `json:"max_pkts_per_interval"` //
	MaxPayloadSize     int    `json:"max_payload_size"`      // octets
	MinBandwidthBps    uint64 `json:"min_bandwidth_bps"`     // min-bandwidth

	// ── 3GPP extensions (RFC 9633 augmentation, §6.1.3.23b) ──
	Node5GSMaxLatencyUS      uint64 `json:"5gs_node_max_latency_us,omitempty"`
	MaxConsecutiveLossTolNum int    `json:"max_consecutive_loss_tolerance,omitempty"`

	// ── DetNet flow specification (RFC 9633 ip-flow) ──
	SrcIP    string `json:"src_ip,omitempty"`
	DstIP    string `json:"dst_ip,omitempty"`
	Protocol int    `json:"protocol,omitempty"`
	SrcPort  int    `json:"src_port,omitempty"`
	DstPort  int    `json:"dst_port,omitempty"`
	// FlowDirection at the 5GS: "DL" (network→UE, default) or "UL".
	FlowDirection string `json:"flow_direction,omitempty"`

	// ── target PDU session (device-side port; TS 23.501 §5.28.5.3:
	//    "use the source IP address to determine the UE" — the
	//    in-process form addresses the UE directly) ──
	SUPI string `json:"supi"`
	DNN  string `json:"dnn"`
}

// sessionID derives the TSC app session identifier of a DetNet flow.
func (f *DetNetFlow) sessionID() string { return "detnet-" + f.Name }

// FlowDescription renders the RFC 9633 ip-flow as the IPFilterRule
// style flow description PCC rules use.
func (f *DetNetFlow) FlowDescription() string {
	proto := "ip"
	if f.Protocol > 0 {
		proto = fmt.Sprintf("%d", f.Protocol)
	}
	src, dst := f.SrcIP, f.DstIP
	if src == "" {
		src = "any"
	}
	if dst == "" {
		dst = "any"
	}
	out := fmt.Sprintf("permit out %s from %s", proto, src)
	if f.SrcPort > 0 {
		out += fmt.Sprintf(" %d", f.SrcPort)
	}
	out += " to " + dst
	if f.DstPort > 0 {
		out += fmt.Sprintf(" %d", f.DstPort)
	}
	return out
}

// toTscQosRequirement applies the §6.1.3.23b mapping table.
func (f *DetNetFlow) toTscQosRequirement() (pcf.TscQosRequirement, error) {
	if f.IntervalUS == 0 {
		return pcf.TscQosRequirement{}, fmt.Errorf("tsctsf: detnet flow %s: interval required (RFC 9633 traffic-specification)", f.Name)
	}
	if f.MaxPktsPerInterval <= 0 {
		f.MaxPktsPerInterval = 1
	}
	burst := f.MaxPktsPerInterval * (f.MaxPayloadSize + detnetIPHeaderOctets)
	// Requested MBR = burst / interval (§6.1.3.23b), kbit/s rounded up.
	mbrKbps := int((uint64(burst)*8*1_000_000/f.IntervalUS + 999) / 1000)
	gbrKbps := int((f.MinBandwidthBps + 999) / 1000)
	if gbrKbps == 0 {
		gbrKbps = mbrKbps
	}

	tscai := &pcf.TscaiInput{PeriodicityUS: f.IntervalUS}
	if f.MaxConsecutiveLossTolNum > 0 {
		// max-consecutive-loss-tolerance → Survival time in messages.
		tscai.SurTimeInNumMsg = uint32(f.MaxConsecutiveLossTolNum)
	}
	req := pcf.TscQosRequirement{
		Req5GSDelayMS:         float64(f.Node5GSMaxLatencyUS) / 1000.0,
		MaxTscBurstSizeOctets: burst,
	}
	if f.FlowDirection == "UL" {
		req.ReqGbrUlKbps, req.ReqMbrUlKbps = gbrKbps, mbrKbps
		req.TscaiInputUl = tscai
	} else {
		req.ReqGbrDlKbps, req.ReqMbrDlKbps = gbrKbps, mbrKbps
		req.TscaiInputDl = tscai
	}
	return req, nil
}

var (
	detnetMu    sync.Mutex
	detnetFlows = map[string]*DetNetFlow{}
)

// DetNetFlowCreate provisions a DetNet flow: maps the RFC 9633
// traffic spec to a TSC QoS requirement (§6.1.3.23b) and drives the
// regular Ntsctsf_QoSandTSCAssistance → Npcf_PolicyAuthorization
// pipeline, exactly as an AF-requested TSC session would.
func (s *Service) DetNetFlowCreate(f *DetNetFlow) error {
	if f.Name == "" || f.SUPI == "" || f.DNN == "" {
		return fmt.Errorf("tsctsf: detnet flow requires name, supi, dnn")
	}
	detnetMu.Lock()
	if _, dup := detnetFlows[f.Name]; dup {
		detnetMu.Unlock()
		return fmt.Errorf("tsctsf: detnet flow %s exists", f.Name)
	}
	detnetMu.Unlock()

	req, err := f.toTscQosRequirement()
	if err != nil {
		return err
	}
	app := &TscAppSession{
		SessionID: f.sessionID(),
		AfID:      "detnet-controller",
		SUPI:      f.SUPI,
		DNN:       f.DNN,
		TscQosReq: req,
	}
	if err := s.AppSessionCreate(app); err != nil {
		return err
	}
	detnetMu.Lock()
	detnetFlows[f.Name] = f
	detnetMu.Unlock()
	logger.Get("tsctsf.detnet").Infof(
		"DetNet flow %s provisioned: %s burst=%dB periodicity=%dus latency=%dus filter=%q (TS 23.503 §6.1.3.23b)",
		f.Name, f.FlowDirection, req.MaxTscBurstSizeOctets, f.IntervalUS,
		f.Node5GSMaxLatencyUS, f.FlowDescription())
	return nil
}

// DetNetFlowDelete removes a DetNet flow and its TSC app session.
func (s *Service) DetNetFlowDelete(name string) error {
	detnetMu.Lock()
	f := detnetFlows[name]
	delete(detnetFlows, name)
	detnetMu.Unlock()
	if f == nil {
		return fmt.Errorf("tsctsf: unknown detnet flow %s", name)
	}
	return s.AppSessionDelete(f.sessionID())
}

// DetNetFlows lists provisioned flows (RFC 8343/8344-style node
// reporting toward the controller rides Capabilities/Bridges).
func (s *Service) DetNetFlows() []*DetNetFlow {
	detnetMu.Lock()
	defer detnetMu.Unlock()
	out := make([]*DetNetFlow, 0, len(detnetFlows))
	for _, f := range detnetFlows {
		out = append(out, f)
	}
	return out
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// TSC (Time Sensitive Communication) policy support — the PCF side of
// the Rel-19 5GS TSN/TSC integration.
//
// Authoritative specs (PDFs under specs/3gpp/):
//
//	TS 23.503 v19.7.0 §6.1.3.23  — PCF interworking with the TSN AF
//	                  §6.1.3.23a — PCF interworking with the TSCTSF
//	TS 29.514 v19.6.0 §5.6.2.39  — TscaiInputContainer
//	                  §5.6.2.3   — AppSessionContextReqData TSC attributes
//	                  §5.6.3.7   — AfEvent TSN_BRIDGE_INFO / BAT_OFFSET_INFO
//	TS 29.512 v19.6.0 §5.6.2.6   — PccRule tscaiInputUl/Dl, tscaiTimeDom,
//	                               capBatAdaptation
//	                  §5.6.2.41  — TsnBridgeInfo
//	                  §5.6.2.45  — PortManagementContainer
//	                  §5.6.2.47  — BridgeManagementContainer
//	                  §5.6.3.6   — trigger TSN_BRIDGE_INFO
//	TS 23.501 v19.7.0 §5.27.2    — TSC Assistance Container / TSCAI
//	                  §5.27.3    — TSC QoS Flows (delay-critical GBR, MDBV)
//
// The TSN AF (nf/tsnaf) and the TSCTSF (nf/tsctsf) call into this file
// in place of the Npcf_PolicyAuthorization HTTP surface; the SMF picks
// the resulting PCC rules up through smpolicy.Create/Update exactly as
// it does for the IMS AF path.
package pcf

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ---------------------------------------------------------------------------
// TS 29.514 §5.6.2.39 — TscaiInputContainer
// ---------------------------------------------------------------------------

// TscaiInput mirrors TS 29.514 §5.6.2.39 "Type TscaiInputContainer":
// TSCAI input parameters the (TSN) AF / TSCTSF hands to the PCF, which the
// PCF copies into the PCC rule (29.512 §5.6.2.6 tscaiInputUl/Dl) for the
// SMF to derive the final TSCAI (TS 23.501 §5.27.2.4).
type TscaiInput struct {
	// periodicity — time period between start of two bursts, in
	// microseconds (spec type Uinteger, unit microsecond).
	PeriodicityUS uint64 `json:"periodicity_us,omitempty"`
	// burstArrivalTime — arrival time of the data burst at the 5GS
	// ingress (UL: DS-TT port, DL: NW-TT port), nanoseconds in the
	// reference time domain (wire form is DateTime; the in-process
	// port keeps nanoseconds since epoch for the §5.27.2.4 math).
	BurstArrivalTimeNS int64 `json:"burst_arrival_time_ns,omitempty"`
	// surTimeInNumMsg — survival time as a number of bursts/messages.
	SurTimeInNumMsg uint32 `json:"sur_time_in_num_msg,omitempty"`
	// surTimeInTime — survival time in microseconds. Mutually exclusive
	// with SurTimeInNumMsg per 29.514 table 5.6.2.39-1.
	SurTimeInTimeUS uint64 `json:"sur_time_in_time_us,omitempty"`
	// burstArrivalTimeWnd — acceptable earliest/latest burst arrival
	// (EnTSCAC). Requires BurstArrivalTimeNS.
	BatWindowEarliestNS int64 `json:"bat_window_earliest_ns,omitempty"`
	BatWindowLatestNS   int64 `json:"bat_window_latest_ns,omitempty"`
	// periodicityRange (EnTSCAC) — acceptable lower/upper periodicity.
	PeriodicityLowerUS uint64 `json:"periodicity_lower_us,omitempty"`
	PeriodicityUpperUS uint64 `json:"periodicity_upper_us,omitempty"`
}

// TscQosRequirement carries the AF-requested TSC QoS toward the PCF —
// the union of TS 29.514 AppSessionContextReqData TSC attributes and the
// TscQosRequirement shape of TS 29.122 §5.14.1 referenced from
// TS 29.565 §6.2.6 (Ntsctsf_QoSandTSCAssistance).
type TscQosRequirement struct {
	// Requested guaranteed / maximum bitrates (kbit/s).
	ReqGbrDlKbps int `json:"req_gbr_dl_kbps,omitempty"`
	ReqGbrUlKbps int `json:"req_gbr_ul_kbps,omitempty"`
	ReqMbrDlKbps int `json:"req_mbr_dl_kbps,omitempty"`
	ReqMbrUlKbps int `json:"req_mbr_ul_kbps,omitempty"`
	// req5Gsdelay — requested 5GS delay (PDB budget) in milliseconds.
	Req5GSDelayMS float64 `json:"req_5gs_delay_ms,omitempty"`
	// Maximum TSC burst size in octets (drives MDBV per TS 23.501
	// §5.27.3: "MDBV should be set to the aggregated burst size").
	MaxTscBurstSizeOctets int `json:"max_tsc_burst_size,omitempty"`
	// priority — TSC priority (maps to ARP / scheduling priority).
	Priority int `json:"priority,omitempty"`
	// tscaiTimeDom — (g)PTP time domain the (TSN) AF resides in
	// (29.512 §5.6.2.6 tscaiTimeDom).
	TscaiTimeDom uint64 `json:"tscai_time_dom,omitempty"`
	// tscaiInputUl / tscaiInputDl — TSCAI input containers.
	TscaiInputUl *TscaiInput `json:"tscai_input_ul,omitempty"`
	TscaiInputDl *TscaiInput `json:"tscai_input_dl,omitempty"`
	// capBatAdaptation — AF can adapt its burst sending time on
	// RAN-provided BAT offsets (EnTSCAC; 29.512 §5.6.2.6).
	CapBatAdaptation bool `json:"cap_bat_adaptation,omitempty"`
	// Ethernet flow description (TS 29.514 EthFlowDescription):
	// destination MAC + C-TAG VID of the TSN stream.
	EthDestMAC string `json:"eth_dest_mac,omitempty"`
	EthVLANID  int    `json:"eth_vlan_id,omitempty"`
}

// ---------------------------------------------------------------------------
// TS 29.512 §5.6.2.41 / §5.6.2.45 / §5.6.2.47 — user plane node info
// ---------------------------------------------------------------------------

// TsnBridgeInfo mirrors TS 29.512 §5.6.2.41 "Type TsnBridgeInfo" — the
// TSC user plane node information the SMF learns from the UPF (N4) and
// the UE (N1) and reports to the PCF on the TSN_BRIDGE_INFO trigger.
type TsnBridgeInfo struct {
	// bridgeId — TSC user plane node ID: 5GS Bridge ID per TS 23.501
	// §5.28.1 (derived from the bridge MAC address per IEEE 802.1Q
	// clause 14.2.5) or DetNet router ID.
	BridgeID uint64 `json:"bridge_id,omitempty"`
	// dsttAddr — MAC address of the DS-TT port ("aa-bb-cc-dd-ee-ff").
	DsttAddr string `json:"dstt_addr,omitempty"`
	// dsttPortNum — port number allocated by the UPF for the PDU
	// session's DS-TT port (TS 23.502 §4.3.2.2.1 step 10b).
	DsttPortNum uint16 `json:"dstt_port_num,omitempty"`
	// dsttResidTime — UE-DS-TT residence time as signalled in the NAS
	// IE (TS 24.501 §9.11.4.26), nanoseconds.
	DsttResidTimeNS uint64 `json:"dstt_resid_time_ns,omitempty"`
	// nwttPortNums — the UPF's preconfigured NW-TT (N6-side) port
	// numbers from the Created Bridge Info (TS 29.244 §7.5.3.6). The
	// TSN AF needs them to build the §5.27.5 per-port-pair bridge
	// delay report toward the CNC.
	NwttPortNums []uint16 `json:"nwtt_port_nums,omitempty"`
}

// PortManCont mirrors TS 29.512 §5.6.2.45 "Type PortManagementContainer":
// an opaque TS 24.539 port management service message plus the port it
// belongs to.
type PortManCont struct {
	Container []byte `json:"container"`
	PortNum   uint16 `json:"port_num"`
}

// BatOffsetInfo carries the NG-RAN BAT offset feedback (TS 23.501
// §5.27.2.5, 29.514 event BAT_OFFSET_INFO / EnTSCAC).
type BatOffsetInfo struct {
	RuleID                string `json:"rule_id"`
	BatOffsetNS           int64  `json:"bat_offset_ns"`
	AdjustedPeriodicityUS uint64 `json:"adjusted_periodicity_us,omitempty"`
}

// ---------------------------------------------------------------------------
// TSC user plane events toward the TSN AF / TSCTSF
// (TS 29.514 §5.6.3.7 AfEvent: TSN_BRIDGE_INFO, BAT_OFFSET_INFO)
// ---------------------------------------------------------------------------

// TscUplaneEvent is the in-process form of the Npcf_PolicyAuthorization
// Notify carrying TSN_BRIDGE_INFO / BAT_OFFSET_INFO event payloads
// (TS 29.514 §5.6.2.9 EventsNotification TSC attributes).
type TscUplaneEvent struct {
	IMSI         string
	PDUSessionID uint8
	DNN          string
	SNSSAISST    uint8

	// TSN_BRIDGE_INFO payload
	BridgeInfo       *TsnBridgeInfo
	PortManContDstt  *PortManCont
	PortManContNwtts []PortManCont
	BridgeManCont    []byte // UMIC

	// BAT_OFFSET_INFO payload
	BatOffsets []BatOffsetInfo

	// UE_MAC_CH payload (TS 29.512 §5.6.3.6 trigger UE_MAC_CH):
	// a UE MAC address newly detected on the Ethernet PDU session.
	DetectedMac string

	// SessionReleased — the Ethernet PDU session backing this DS-TT
	// port is gone. The TSN AF drops the port (and the 5GS bridge when
	// it was the last DS-TT port) plus any streams bound to the
	// session, so the CNC never configures a stream over a dead bridge
	// (TS 23.501 §5.28.1: the 5GS bridge exists per PDU session).
	SessionReleased bool
}

var (
	tscListenerMu sync.RWMutex
	tscListeners  []func(TscUplaneEvent)
)

// RegisterTscUplaneListener registers a TSN AF / TSCTSF callback for TSC
// user plane events. In-process equivalent of the PCF-initiated
// Npcf_PolicyAuthorization_Notify toward the tscNotifUri configured per
// DNN/S-NSSAI (TS 23.503 §6.1.3.23: the PCF is configured with the TSN
// AF address; §6.1.3.23a: it discovers the TSCTSF per DNN/S-NSSAI).
func RegisterTscUplaneListener(cb func(TscUplaneEvent)) {
	tscListenerMu.Lock()
	tscListeners = append(tscListeners, cb)
	tscListenerMu.Unlock()
}

// NotifyTscUplaneEvent fans a TSC user plane event out to the registered
// TSN AF / TSCTSF listeners. Called by smpolicy.Update when the SMF
// reports the TSN_BRIDGE_INFO policy control request trigger
// (TS 29.512 §4.2.4.15) or BAT offset feedback (EnTSCAC).
func NotifyTscUplaneEvent(ev TscUplaneEvent) {
	tscListenerMu.RLock()
	cbs := make([]func(TscUplaneEvent), len(tscListeners))
	copy(cbs, tscListeners)
	tscListenerMu.RUnlock()
	log := logger.Get("pcf.tsc").WithIMSI(ev.IMSI)
	log.Infof("TSC u-plane event pdu=%d dnn=%s bridge=%v pmicDstt=%v pmicNwtt=%d umic=%dB batOffsets=%d listeners=%d",
		ev.PDUSessionID, ev.DNN, ev.BridgeInfo != nil, ev.PortManContDstt != nil,
		len(ev.PortManContNwtts), len(ev.BridgeManCont), len(ev.BatOffsets), len(cbs))
	for _, cb := range cbs {
		cb(ev)
	}
}

// ---------------------------------------------------------------------------
// Dynamic TSC PCC rules (TS 23.503 §6.1.3.23: the PCF derives PCC rules
// from the TSN AF service information)
// ---------------------------------------------------------------------------

// tscRuleKey mirrors the (SUPI, DNN) binding scope of an AF session.
type tscRuleKey struct{ imsi, dnn string }

var (
	tscRuleMu    sync.Mutex
	tscRuleStore = map[tscRuleKey][]PCCRule{}
)

// delayCriticalGBR is the standardized delay-critical GBR 5QI table of
// TS 23.501 §5.7.4 Table 5.7.4-1 (Rel-19) — the candidate set for TSC
// QoS flows per §5.27.3.
var delayCriticalGBR = []struct {
	FiveQI     int
	PDBms      float64
	MDBVoctets int
	Priority   int
}{
	{85, 5, 255, 21},   // electricity distribution, V2X-grade
	{86, 5, 1354, 18},  // V2X advanced driving
	{82, 10, 255, 19},  // discrete automation, small bursts
	{83, 10, 1354, 22}, // discrete automation
	{84, 30, 1354, 24}, // intelligent transport
}

// Select5QIForTSC picks a delay-critical GBR 5QI for a TSC flow per
// TS 23.501 §5.27.3: MDBV ≥ maximum TSC burst size and PDB within the
// requested 5GS delay. Falls back to 5QI 82 when nothing matches
// tighter (smallest-PDB entries are preferred so the CN PDB headroom
// stays maximal for the TSCAI BAT adjustment of §5.27.2.4).
func Select5QIForTSC(req5GSDelayMS float64, burstOctets int) int {
	best := 0
	var bestPDB float64
	for _, c := range delayCriticalGBR {
		if burstOctets > 0 && c.MDBVoctets < burstOctets {
			continue
		}
		if req5GSDelayMS > 0 && c.PDBms > req5GSDelayMS {
			continue
		}
		// Prefer the largest PDB that still satisfies the request —
		// least restrictive on the RAN scheduler.
		if best == 0 || c.PDBms > bestPDB {
			best, bestPDB = c.FiveQI, c.PDBms
		}
	}
	if best == 0 {
		best = 82
	}
	return best
}

// FiveQICharacteristics returns (PDB ms, MDBV octets) for a delay-critical
// GBR 5QI; ok=false for non-TSC 5QIs. The SMF uses the PDB when deriving
// the CN PDB component of the TSCAI burst arrival time (§5.27.2.4).
func FiveQICharacteristics(fiveQI int) (pdbMS float64, mdbv int, ok bool) {
	for _, c := range delayCriticalGBR {
		if c.FiveQI == fiveQI {
			return c.PDBms, c.MDBVoctets, true
		}
	}
	return 0, 0, false
}

// InstallTscPccRule derives a PCC rule from AF-provided TSC service
// information and installs it for (imsi, dnn) — TS 23.503 §6.1.3.23:
//
//   - 5QI: delay-critical GBR selected from the requested 5GS delay and
//     the maximum TSC burst size (§5.28.4: PCF "selects the 5QI").
//   - GBR/MBR: from the requested bitrates; MBR defaults to GBR per
//     §5.27.3 ("MBR set equal to GBR").
//   - ARP: preconfigured TSN service value (TS 23.503 §6.1.3.23) —
//     priority 1..15 taken from req.Priority, default 2.
//   - TSCAI containers + time domain copied through for the SMF.
//
// Returns the installed rule. The caller follows up with
// smpolicy.Update + PushNotify to ship the decision to the SMF.
func InstallTscPccRule(imsi, dnn, ruleName string, req TscQosRequirement) PCCRule {
	fiveQI := Select5QIForTSC(req.Req5GSDelayMS, req.MaxTscBurstSizeOctets)
	arp := req.Priority
	if arp <= 0 || arp > 15 {
		arp = 2 // preconfigured TSN service ARP (TS 23.503 §6.1.3.23)
	}
	gbrUL, gbrDL := req.ReqGbrUlKbps, req.ReqGbrDlKbps
	mbrUL, mbrDL := req.ReqMbrUlKbps, req.ReqMbrDlKbps
	if mbrUL == 0 {
		mbrUL = gbrUL // §5.27.3: MBR = GBR for TSC flows
	}
	if mbrDL == 0 {
		mbrDL = gbrDL
	}
	rule := PCCRule{
		ServiceName:      ruleName,
		FiveQI:           fiveQI,
		ResourceType:     "GBR",
		ArpPriority:      arp,
		GBRULKbps:        gbrUL,
		GBRDLKbps:        gbrDL,
		MBRULKbps:        mbrUL,
		MBRDLKbps:        mbrDL,
		ChargingProfile:  "",
		IsDefault:        false,
		TscaiInputUl:     req.TscaiInputUl,
		TscaiInputDl:     req.TscaiInputDl,
		TscaiTimeDom:     req.TscaiTimeDom,
		CapBatAdaptation: req.CapBatAdaptation,
		MaxDataBurstVol:  req.MaxTscBurstSizeOctets,
		EthDestMAC:       req.EthDestMAC,
		EthVLANID:        req.EthVLANID,
	}
	if pdb, _, ok := FiveQICharacteristics(fiveQI); ok {
		rule.PacketDelayBudgetMS = pdb
	}

	k := tscRuleKey{imsi, dnn}
	tscRuleMu.Lock()
	rules := tscRuleStore[k]
	replaced := false
	for i := range rules {
		if rules[i].ServiceName == ruleName {
			rules[i] = rule
			replaced = true
			break
		}
	}
	if !replaced {
		rules = append(rules, rule)
	}
	tscRuleStore[k] = rules
	tscRuleMu.Unlock()

	logger.Get("pcf.tsc").WithIMSI(imsi).Infof(
		"TSC PCC rule %s installed dnn=%s 5qi=%d arp=%d gbrUL=%d gbrDL=%d mdbv=%d timeDom=%d (replaced=%v)",
		ruleName, dnn, fiveQI, arp, gbrUL, gbrDL, req.MaxTscBurstSizeOctets, req.TscaiTimeDom, replaced)
	return rule
}

// RemoveTscPccRule uninstalls a dynamic TSC rule; reports whether it
// existed. Caller ships the removal via smpolicy (RemovedPccRules).
func RemoveTscPccRule(imsi, dnn, ruleName string) bool {
	k := tscRuleKey{imsi, dnn}
	tscRuleMu.Lock()
	defer tscRuleMu.Unlock()
	rules := tscRuleStore[k]
	for i := range rules {
		if rules[i].ServiceName == ruleName {
			tscRuleStore[k] = append(rules[:i], rules[i+1:]...)
			if len(tscRuleStore[k]) == 0 {
				delete(tscRuleStore, k)
			}
			return true
		}
	}
	return false
}

// TscPccRules returns the dynamic TSC rules installed for (imsi, dnn).
// Merged into the policy output by CreatePolicy.
func TscPccRules(imsi, dnn string) []PCCRule {
	tscRuleMu.Lock()
	defer tscRuleMu.Unlock()
	return append([]PCCRule(nil), tscRuleStore[tscRuleKey{imsi, dnn}]...)
}

// ClearTscPccRules drops all dynamic TSC rules for (imsi, dnn) — PDU
// session release path.
func ClearTscPccRules(imsi, dnn string) {
	tscRuleMu.Lock()
	delete(tscRuleStore, tscRuleKey{imsi, dnn})
	tscRuleMu.Unlock()
}

// FormatBridgeID renders a 5GS Bridge ID the way TSN tooling shows
// bridge MACs (IEEE 802.1Q clause 14.2.5 derivation).
func FormatBridgeID(id uint64) string {
	return fmt.Sprintf("%02x-%02x-%02x-%02x-%02x-%02x",
		byte(id>>40), byte(id>>32), byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
}

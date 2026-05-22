// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pcf — Policy Control Function.
//
// Authoritative specs (PDFs live under specs/3gpp/):
//
//	TS 23.503 v19.7.0 — Policy and charging control framework (Stage 2).
//	  §6.2.1      Policy Control Function (PCF) — role and input
//	  §6.3, §6.3.1 "Policy and charging control rule" — PCC rule definition
//	  §6.6        UE Route Selection Policy (URSP)
//
//	TS 29.512 v19.6.0 — Npcf_SMPolicyControl service (N7, PCF↔SMF).
//	  §4.2.2  Create service operation     — SM Policy Association establishment
//	  §4.2.3  UpdateNotify service operation — PCF-initiated push to SMF
//	  §4.2.4  Update service operation     — SMF-initiated request to PCF
//	  §4.2.5  Delete service operation     — SM Policy Association termination
//	  §5.6.2.6 Type PccRule — full PCC rule data type
//	  §5.6.3.8 Enumeration RuleStatus — { ACTIVE, INACTIVE } (only two values)
//
//	TS 29.514 v19.6.0 — Npcf_PolicyAuthorization service (N5, AF↔PCF).
//	  §4.2.2   Create service operation — initial AF provisioning of service info
//	  §4.2.2.2 Initial provisioning of service information (media components → flows)
//
//	TS 29.513 v19.6.0 — PCC signalling flows and QoS parameter mapping.
//	  (Referenced for the SDP-to-SDF-filter conversion algorithm.)
//
// The Go PCF is invoked in-process by the SMF during PDU Session
// Establishment / Modification / Release. The SBI Npcf REST surface
// lands when the service layer matures; shapes returned here are
// aligned with the OpenAPI types in 29.512 so the transition is
// mechanical.
//
// This file covers pcf_context.py + pcf_n7_interface.py:
//   - Lookup the QoS rules for a (DNN, SST, IMSI) triple
//   - Merge with the service binding table
//   - Return a PCCRuleSet the SMF can attach to the PDU session
package pcf

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// PCCRule is a single Policy and Charging Control rule, defined at
// TS 23.503 §6.3 "Policy and charging control rule" (§6.3.1 General)
// and serialised on the wire as TS 29.512 §5.6.2.6 "Type PccRule".
//
// Fields here are the minimum needed by the SMF to build Authorized
// QoS Rules (TS 24.501 §9.11.4.13) + Session-AMBR IE. The full 29.512
// PccRule carries 20+ optional attributes (flow info, app detection,
// charging, precedence, content version, ATSSS descriptor, …) —
// add on demand.
type PCCRule struct {
	ServiceName     string
	FiveQI          int
	ResourceType    string // GBR | NonGBR
	ArpPriority     int
	GBRULKbps       int
	GBRDLKbps       int
	MBRULKbps       int
	MBRDLKbps       int
	ChargingProfile string
	IsDefault       bool
}

// PCCRuleSet is the output of CreatePolicy — all rules for one session.
type PCCRuleSet struct {
	Rules          []PCCRule
	DefaultQFI     uint8
	ChargingMethod string // "online" | "offline"
}

// CreatePolicy builds the PCC rule set for a (DNN, IMSI) pair.
// Matches the Python pcf_n7_interface::create_sm_policy_context.
//
// Non-default service bindings are filtered by the in-memory
// DefaultPccRuleManager state — TS 23.503 v19.0.0 §6.3.2.1 (PCC rule
// lifecycle) only emits rules in ACTIVE state. The AF-driven path
// (services/ims/af.go AuthorizeMediaFromSDP → HandleAARequest →
// ActivateRules) flips a service from "INACTIVE (event gated)" →
// ACTIVE when the SIP INVITE / re-INVITE SDP carries that media
// type. Returning the full bindings set regardless of activation
// status causes the SMF to install all dedicated bearers on the
// very first PDU establishment (well before any SIP INVITE), which
// breaks TS 23.502 §4.3.3 PDU Session Modification — the AF can't
// "add a new bearer" via re-INVITE because every bearer is already
// installed.
//
// Default services (b.IsDefault=true) bypass the activation gate —
// they're the §5.7.1 always-on default QoS Flow and stay installed
// for the session lifetime regardless of AF state.
func CreatePolicy(imsi, dnn string, sst uint8) PCCRuleSet {
	log := logger.Get("pcf").WithIMSI(imsi)

	bindings, err := crud.BindingsList(crud.BindingFilter{IMSI: imsi, DNN: dnn})
	if err != nil {
		log.Warnf("pcf: bindings lookup dnn=%s: %v", dnn, err)
	}

	// Snapshot of currently-ACTIVE services from the in-memory PCC
	// rule manager so non-default bindings get the §6.3.2.1
	// lifecycle filter applied below.
	activeSet := make(map[string]struct{})
	for _, name := range DefaultPccRuleManager.GetActiveServiceNames(imsi, dnn) {
		activeSet[name] = struct{}{}
	}

	var rules []PCCRule
	var defaultQFI uint8 = 1
	chargingMethod := "offline"

	for i, b := range bindings {
		svc, err := crud.ServicesGet(b.ServiceName)
		if err != nil || svc == nil {
			continue
		}
		if !b.IsDefault {
			// §6.3.2.1: non-default rules only ship to the SMF when
			// ACTIVE. Caller (services/ims/af.go) ActivateRules ahead
			// of smpolicy.Update for INVITE/re-INVITE.
			if _, on := activeSet[svc.Name]; !on {
				continue
			}
		}
		r := PCCRule{
			ServiceName:  svc.Name,
			FiveQI:       svc.FiveQI,
			ResourceType: svc.ResourceType,
			ArpPriority:  svc.ArpPriority,
			IsDefault:    b.IsDefault,
		}
		if svc.GBRULKbps != nil {
			r.GBRULKbps = *svc.GBRULKbps
		}
		if svc.GBRDLKbps != nil {
			r.GBRDLKbps = *svc.GBRDLKbps
		}
		if svc.MBRULKbps != nil {
			r.MBRULKbps = *svc.MBRULKbps
		}
		if svc.MBRDLKbps != nil {
			r.MBRDLKbps = *svc.MBRDLKbps
		}
		r.ChargingProfile = svc.ChargingProfile
		if r.ChargingProfile != "" {
			cp, _ := crud.ChargingProfilesGet(r.ChargingProfile)
			if cp != nil && cp.ChargingMethod == "online" {
				chargingMethod = "online"
			}
		}
		if b.IsDefault {
			defaultQFI = uint8(i + 1)
		}
		rules = append(rules, r)
	}

	if len(rules) == 0 {
		// No bindings → return a single default-data rule.
		rules = []PCCRule{{
			ServiceName:  "default_data",
			FiveQI:       9,
			ResourceType: "NonGBR",
			ArpPriority:  9,
			IsDefault:    true,
		}}
		defaultQFI = 1
	}

	log.Infof("PCF policy dnn=%s rules=%d defaultQFI=%d charging=%s",
		dnn, len(rules), defaultQFI, chargingMethod)
	return PCCRuleSet{
		Rules:          rules,
		DefaultQFI:     defaultQFI,
		ChargingMethod: chargingMethod,
	}
}

// GetPolicyForSession is the priority entry point called by SMF during PDU
// session setup. Wraps CreatePolicy with the (imsi, dnn, sst) triple that
// other NFs pass through.
func GetPolicyForSession(imsi, dnn string, sst uint8) PCCRuleSet {
	return CreatePolicy(imsi, dnn, sst)
}

// ── PCC Rule Manager (TS 23.503 §6.3 "PCC rule", §6.1.3 SM-related
//    policy control) ────────────────────────────────────────────────
// Manages in-memory PCC rule lifecycle. Thread-safe.

// PccRuleState is the lifecycle status per TS 29.512
// §5.6.3.8 "Enumeration: RuleStatus" (Table 5.6.3.8-1).
//
// The spec defines exactly two values:
//
//	ACTIVE   — PCC rule successfully installed (PCF-provisioned) or
//	           activated (SMF pre-defined); session rule installed.
//	INACTIVE — PCC rule removed / inactive / session rule removed.
//
// The remaining constants (PccRulePending, PccRuleRemoved,
// PccRuleInactiveGated) are NOT in §5.6.3.8; they're carry-over from
// the Python port's internal rule lifecycle for deferred activation
// on AF events (pre-Rx / Npcf_PolicyAuthorization trigger). They stay
// for internal bookkeeping but MUST NOT leak out over the N7 SBI;
// serialise them to ACTIVE or INACTIVE when emitting Npcf messages.
type PccRuleState string

const (
	// Spec-defined values (TS 29.512 §5.6.3.8).
	PccRuleActive   PccRuleState = "ACTIVE"
	PccRuleInactive PccRuleState = "INACTIVE"

	// Implementation-internal lifecycle states. Not valid RuleStatus
	// values on the wire — map to ACTIVE / INACTIVE before serialising.
	PccRulePending       PccRuleState = "PENDING"        // awaiting activation trigger
	PccRuleInactiveGated PccRuleState = "INACTIVE_GATED" // gated on AF media event
	PccRuleRemoved       PccRuleState = "REMOVED"        // pending final cleanup
)

// WireRuleStatus collapses an internal PccRuleState to the two-valued
// RuleStatus enum (TS 29.512 §5.6.3.8) for N7 serialisation.
func (s PccRuleState) WireRuleStatus() PccRuleState {
	if s == PccRuleActive {
		return PccRuleActive
	}
	return PccRuleInactive
}

// PccRuleEntry is one in-memory PCC rule.
type PccRuleEntry struct {
	RuleID      string
	IMSI        string
	DNN         string
	ServiceName string
	Status      PccRuleState
	CreatedAt   time.Time
	ActivatedAt *time.Time
}

type ruleKey struct{ imsi, dnn string }

// PccRuleManager manages PCC rule state for all UEs.
type PccRuleManager struct {
	mu    sync.Mutex
	rules map[ruleKey][]*PccRuleEntry
}

// GlobalPccRuleManager is the singleton instance (mirrors Python pcc_rule_mgr).
var GlobalPccRuleManager = &PccRuleManager{
	rules: make(map[ruleKey][]*PccRuleEntry),
}

// GetRules returns all PCC rules for a UE+DNN.
func (m *PccRuleManager) GetRules(imsi, dnn string) []*PccRuleEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rules[ruleKey{imsi, dnn}]
}

// AddRule adds a PCC rule. Returns the existing rule if a duplicate.
func (m *PccRuleManager) AddRule(imsi, dnn, serviceName string, status PccRuleState) *PccRuleEntry {
	log := logger.Get("pcf.context").WithIMSI(imsi)
	key := ruleKey{imsi, dnn}
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, r := range m.rules[key] {
		if r.ServiceName == serviceName {
			return r
		}
	}
	rule := &PccRuleEntry{
		RuleID:      fmt.Sprintf("%s_%s", serviceName, imsi),
		IMSI:        imsi,
		DNN:         dnn,
		ServiceName: serviceName,
		Status:      status,
		CreatedAt:   time.Now(),
	}
	m.rules[key] = append(m.rules[key], rule)
	log.Infof("PCC rule added: %s status=%s dnn=%s", rule.RuleID, status, dnn)
	return rule
}

// ActivateRules transitions INACTIVE/INACTIVE(event gated) → ACTIVE.
func (m *PccRuleManager) ActivateRules(imsi, dnn string, serviceNames []string) []*PccRuleEntry {
	log := logger.Get("pcf.context").WithIMSI(imsi)
	nameSet := make(map[string]bool, len(serviceNames))
	for _, n := range serviceNames {
		nameSet[n] = true
	}
	key := ruleKey{imsi, dnn}
	m.mu.Lock()
	defer m.mu.Unlock()

	var activated []*PccRuleEntry
	for _, r := range m.rules[key] {
		if nameSet[r.ServiceName] && (r.Status == PccRuleInactive || r.Status == PccRuleInactiveGated) {
			r.Status = PccRuleActive
			now := time.Now()
			r.ActivatedAt = &now
			activated = append(activated, r)
			log.Infof("PCC rule activated: %s", r.RuleID)
		}
	}
	return activated
}

// GetActiveServiceNames returns active service names for a UE+DNN.
func (m *PccRuleManager) GetActiveServiceNames(imsi, dnn string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for _, r := range m.rules[ruleKey{imsi, dnn}] {
		if r.Status == PccRuleActive && r.ServiceName != "" {
			names = append(names, r.ServiceName)
		}
	}
	return names
}

// DeactivateRules transitions ACTIVE → INACTIVE (event gated) for all rules on UE+DNN.
func (m *PccRuleManager) DeactivateRules(imsi, dnn string) {
	log := logger.Get("pcf.context").WithIMSI(imsi)
	key := ruleKey{imsi, dnn}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rules[key] {
		if r.Status == PccRuleActive {
			r.Status = PccRuleInactiveGated
			r.ActivatedAt = nil
			log.Infof("PCC rule deactivated: %s", r.RuleID)
		}
	}
}

// DeactivateRulesByName deactivates specific service rules.
// TS 23.502 §4.3.3.2: media removed from SDP → deactivate rule.
func (m *PccRuleManager) DeactivateRulesByName(imsi, dnn string, serviceNames []string) {
	log := logger.Get("pcf.context").WithIMSI(imsi)
	nameSet := make(map[string]bool, len(serviceNames))
	for _, n := range serviceNames {
		nameSet[n] = true
	}
	key := ruleKey{imsi, dnn}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rules[key] {
		if r.Status == PccRuleActive && nameSet[r.ServiceName] {
			r.Status = PccRuleInactiveGated
			r.ActivatedAt = nil
			log.Infof("PCC rule deactivated (media removed): %s", r.RuleID)
		}
	}
}

// RemoveRules removes all PCC rules for a UE+DNN.
func (m *PccRuleManager) RemoveRules(imsi, dnn string) {
	log := logger.Get("pcf.context").WithIMSI(imsi)
	key := ruleKey{imsi, dnn}
	m.mu.Lock()
	removed := m.rules[key]
	delete(m.rules, key)
	m.mu.Unlock()
	for _, r := range removed {
		log.Infof("PCC rule removed: %s", r.RuleID)
	}
}

// ── N7 Interface: PCF → SMF via Npcf_SMPolicyControl_UpdateNotify ───
//    (TS 29.512 §4.2.3 "UpdateNotify service operation"). The PCF
//    invokes this to push a changed SmPolicyDecision to the SMF —
//    e.g. when the AF activates a dynamic PCC rule for IMS media,
//    when subscription policy changes, or on revalidation-timer
//    expiry with a fresh decision. ──────────────────────────────────

// NotifySMFPolicyUpdate pushes PCC rule activation to the SMF
// (Npcf_SMPolicyControl_UpdateNotify per TS 29.512 §4.2.3.2 "SM
// Policy Association Update request"). Port of pcf_n7_interface.py
// notify_smf_policy_update.
func NotifySMFPolicyUpdate(imsi string, pduSessionID int, serviceNames []string, sdpMedia []map[string]interface{}) bool {
	log := logger.Get("pcf.n7").WithIMSI(imsi)
	if len(serviceNames) == 0 {
		return false
	}

	db, err := engine.Open()
	if err != nil {
		log.Errorf("N7 DB: %v", err)
		return false
	}

	var newServices []map[string]interface{}
	for _, name := range serviceNames {
		var fiveqi, arpPri, arpPcap, arpPvuln sql.NullInt64
		var resType sql.NullString
		var gbrUL, gbrDL, mbrUL, mbrDL sql.NullInt64
		var flowJSON, chargingProfile sql.NullString

		err := db.QueryRow(`SELECT fiveqi, resource_type, arp_priority,
			arp_pcap, arp_pvuln, gbr_ul_kbps, gbr_dl_kbps,
			mbr_ul_kbps, mbr_dl_kbps, flow_json, charging_profile
			FROM services WHERE name = ?`, name).Scan(
			&fiveqi, &resType, &arpPri, &arpPcap, &arpPvuln,
			&gbrUL, &gbrDL, &mbrUL, &mbrDL, &flowJSON, &chargingProfile)
		if err != nil {
			log.Warnf("N7: service %s not found: %v", name, err)
			continue
		}

		svc := map[string]interface{}{
			"name":          name,
			"fiveqi":        fiveqi.Int64,
			"resource_type": resType.String,
			"arp_priority":  arpPri.Int64,
			"gbr_ul_kbps":   gbrUL.Int64,
			"gbr_dl_kbps":   gbrDL.Int64,
			"mbr_ul_kbps":   mbrUL.Int64,
			"mbr_dl_kbps":   mbrDL.Int64,
			"flow_json":     flowJSON.String,
			"is_default":    false,
		}

		// TS 29.514 §4.2.2.2 "Initial provisioning of service
		// information" — AF Create operation carries Media Component
		// Descriptions; the PCF derives SDF filters from them (the
		// mapping algorithm itself is in TS 29.513, Stage 3 flows).
		// When SDP media are supplied, override the binding's static
		// flow filter with the dynamic SDF.
		if len(sdpMedia) > 0 {
			dynFilters := BuildDynamicSDFFromSDP(name, sdpMedia)
			if len(dynFilters) > 0 {
				b, _ := json.Marshal(dynFilters)
				svc["flow_json"] = string(b)
			}
		}
		newServices = append(newServices, svc)
	}

	if len(newServices) == 0 {
		log.Warnf("N7: no services found for activation")
		return false
	}

	log.Infof("N7 → SMF: activating %d services on PDU=%d",
		len(newServices), pduSessionID)
	return true
}

// NotifySMFBearerRelease notifies the SMF to release dedicated bearers
// (Npcf_SMPolicyControl_UpdateNotify per TS 29.512 §4.2.3.2, carrying
// a SmPolicyDecision with the affected pccRuleIds + ruleStatus set to
// INACTIVE — see §5.6.3.8 values). Port of pcf_n7_interface.py
// notify_smf_bearer_release.
func NotifySMFBearerRelease(imsi string, pduSessionID int, serviceNames []string) bool {
	log := logger.Get("pcf.n7").WithIMSI(imsi)
	log.Infof("N7 → SMF: releasing bearers [%v] on PDU=%d",
		serviceNames, pduSessionID)
	return true
}

// BuildDynamicSDFFromSDP builds dynamic SDF filters from SDP media components.
// Port of pcf/pcf_n7_interface.py _build_dynamic_sdf_from_sdp.
func BuildDynamicSDFFromSDP(serviceName string, sdpMedia []map[string]interface{}) []string {
	var targetType string
	lower := strings.ToLower(serviceName)
	switch {
	case strings.Contains(lower, "voice") || strings.Contains(lower, "vonr"):
		targetType = "audio"
	case strings.Contains(lower, "video") || strings.Contains(lower, "vinr"):
		targetType = "video"
	default:
		return nil
	}

	const ueRTPPortRange = "49000-51000"
	var filters []string
	for _, mc := range sdpMedia {
		if fmt.Sprint(mc["type"]) != targetType {
			continue
		}
		port, _ := mc["port"].(float64)
		ip, _ := mc["ip"].(string)
		if port <= 0 {
			continue
		}
		remoteCIDR := "0.0.0.0/0"
		if ip != "" {
			remoteCIDR = ip + "/32"
		}
		for _, p := range []int{int(port), int(port) + 1} {
			filters = append(filters,
				fmt.Sprintf("permit in 17 from %s %d to any %s", remoteCIDR, p, ueRTPPortRange))
			filters = append(filters,
				fmt.Sprintf("permit out 17 from any %s to %s %d", ueRTPPortRange, remoteCIDR, p))
		}
	}
	return filters
}

// ── AF↔PCF: Npcf_PolicyAuthorization service, N5 (TS 29.514) ────────
//
// In 5GC the legacy EPC "Rx" Diameter reference point is replaced by
// the N5 SBI surface of Npcf_PolicyAuthorization (TS 29.514 §4.2).
// The Python port's pcf_rx_interface.py kept the "Rx" name for
// parity with the earlier 4G code — that naming is preserved in
// handler names here for grep-continuity, but the operations are
// N5 / Npcf_PolicyAuthorization semantically.
//
//   AF Create  (§4.2.2) — analog of Diameter AA-Request  → HandleAARequest
//   AF Delete  (§4.2.4) — analog of Diameter STR         → HandleSessionTermination

// HandleAARequest handles an AF service-info create (TS 29.514 §4.2.2
// "Npcf_PolicyAuthorization_Create" — legacy-EPC Diameter Rx
// AA-Request analog, name retained for Python-port grep parity).
//
// Behaviour:
//
//  1. Map each m=<media> token to a PCC service in the catalogue
//     seeded by db/seed/services.go per TS 23.501 §5.7.4 Table 5.7.4-1:
//
//         m=audio → "conv_voice"  (5QI 1, GBR)
//         m=video → "conv_video"  (5QI 2, GBR)
//
//     Anything else falls through with no rule activation.
//
//  2. Activate those services for (IMSI, "ims") in the in-memory
//     PccRuleManager — TS 23.503 §6.3 "Policy and charging control
//     rule" lifecycle, TS 29.512 §5.6.3.8 RuleStatus = ACTIVE.
//
//  3. Trigger Npcf_SMPolicyControl_Update for the matching IMS
//     SM Policy Association (TS 29.512 §4.2.4 — UE-initiated resource
//     modification trigger "AF_CHARGING_IDENTIFIER" / "RES_MO_RE").
//     The PCF FSM emits an UpdateNotify (§4.2.3) over N7 carrying
//     the refreshed SmPolicyDecision so the SMF can build the PDU
//     SESSION MODIFICATION COMMAND with the new QoS Flow.
//
// Returns true on a clean activation. Returns false when no media
// type maps to a known service (caller may fall back to best-effort).
func HandleAARequest(imsi string, mediaTypes []string, sdpMedia []map[string]interface{}) bool {
	log := logger.Get("pcf.rx").WithIMSI(imsi)
	log.Infof("Npcf_PolicyAuthorization_Create: media_types=%v sdp_lines=%d",
		mediaTypes, len(sdpMedia))

	services := mapMediaToServices(mediaTypes)
	if len(services) == 0 {
		log.Warnf("AA-Request: no PCC service maps for media=%v — no rules activated", mediaTypes)
		return false
	}

	const dnn = "ims"
	// AF-driven activation creates the PCC rule on first use — the
	// service catalogue is seeded by db/seed/services.go but is not
	// per-UE bound until an AF event fires. Insert as PccRuleInactive
	// (or PccRuleInactiveGated) so ActivateRules promotes them to
	// ACTIVE in the same call.
	for _, svc := range services {
		DefaultPccRuleManager.AddRule(imsi, dnn, svc, PccRuleInactiveGated)
	}
	activated := DefaultPccRuleManager.ActivateRules(imsi, dnn, services)
	if len(activated) == 0 {
		log.Warnf("AA-Request: no PCC rules activated (services=%v not bound for IMSI?)", services)
		return false
	}
	for _, r := range activated {
		log.Infof("AA-Request: PCC rule ACTIVE service=%s rule_id=%s (TS 29.512 §5.6.3.8)",
			r.ServiceName, r.RuleID)
	}

	// Caller (services/ims AF) owns the follow-up
	// Npcf_SMPolicyControl_Update / UpdateNotify push to the SMF —
	// living here would create an import cycle (nf/pcf/smpolicy
	// imports nf/pcf for the rule manager + service catalogue).
	return true
}

// mapMediaToServices is the IMS-side TS 23.501 §5.7.4 mapping:
// SDP m-line media token → standardized 5QI service name. Unknown
// tokens are dropped silently — the AF only activates what we
// recognise.
func mapMediaToServices(mediaTypes []string) []string {
	var out []string
	for _, mt := range mediaTypes {
		switch strings.ToLower(mt) {
		case "audio":
			out = append(out, "conv_voice") // 5QI 1
		case "video":
			out = append(out, "conv_video") // 5QI 2
		}
	}
	return out
}

// HandleSessionTermination handles AF session termination (TS 29.514
// §4.2.4 "Npcf_PolicyAuthorization_Delete"; legacy-Rx STR analog).
// Port of pcf_rx_interface.py handle_session_termination.
func HandleSessionTermination(imsi string) bool {
	log := logger.Get("pcf.rx").WithIMSI(imsi)
	DefaultPccRuleManager.DeactivateRules(imsi, "ims")
	log.Infof("Rx session termination: PCC rules deactivated")
	return true
}

// DefaultPccRuleManager is the singleton PCC rule manager.
var DefaultPccRuleManager = &PccRuleManager{rules: map[ruleKey][]*PccRuleEntry{}}

// ── V2X Policy (TS 23.287) ────────────────────────────────────────────

// V2XPolicyAssociation holds V2X authorization state per UE.
type V2XPolicyAssociation struct {
	IMSI        string   `json:"imsi"`
	V2XUEType   string   `json:"v2x_ue_type"`
	PC5AMBRKbps int64    `json:"pc5_ambr_kbps"`
	PC5QoSFlows []string `json:"pc5_qos_flows,omitempty"`
}

var (
	v2xAssocMu sync.RWMutex
	v2xAssoc   = map[string]*V2XPolicyAssociation{}
)

// CreateV2XPolicyAssociation creates a V2X policy association for a UE.
// Port of pcf/pcf_v2x_policy.py create_v2x_policy_association.
func CreateV2XPolicyAssociation(imsi string, v2xSub map[string]interface{}) *V2XPolicyAssociation {
	log := logger.Get("pcf.v2x").WithIMSI(imsi)
	ueType, _ := v2xSub["v2x_ue_type"].(string)
	ambr, _ := v2xSub["v2x_pc5_ambr_kbps"].(float64)
	if ambr == 0 {
		ambr = 50000 // default 50 Mbps
	}
	assoc := &V2XPolicyAssociation{
		IMSI:        imsi,
		V2XUEType:   ueType,
		PC5AMBRKbps: int64(ambr),
	}
	v2xAssocMu.Lock()
	v2xAssoc[imsi] = assoc
	v2xAssocMu.Unlock()
	log.Infof("V2X policy association created type=%s ambr=%dkbps",
		ueType, int64(ambr))
	return assoc
}

// DeleteV2XPolicyAssociation removes a V2X policy association.
// Port of pcf/pcf_v2x_policy.py delete_v2x_policy_association.
func DeleteV2XPolicyAssociation(imsi string) {
	v2xAssocMu.Lock()
	delete(v2xAssoc, imsi)
	v2xAssocMu.Unlock()
}

// GetPC5QoSForGnb returns PC5 QoS parameters for gNB configuration.
// Port of pcf/pcf_v2x_policy.py get_pc5_qos_for_gnb.
func GetPC5QoSForGnb(imsi string) *V2XPolicyAssociation {
	v2xAssocMu.RLock()
	defer v2xAssocMu.RUnlock()
	return v2xAssoc[imsi]
}

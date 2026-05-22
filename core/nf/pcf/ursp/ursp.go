// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ursp — UE Route Selection Policy (TS 23.503 §6.6).
//
// Go port of nf/pcf/ursp/. Provides:
//   - URSP rule CRUD (DB-backed)
//   - Rule evaluation engine (traffic descriptor matching)
//   - URSP IE encoding for NAS delivery (TS 24.501 §5.4.4)
package ursp

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ── Types ──────────────────────────────────────────────────────────────

// TrafficDescriptor is a single TD component (TS 23.503 §6.6.2.1).
type TrafficDescriptor struct {
	ID         int64  `json:"id"`
	RuleID     int64  `json:"rule_id"`
	MatchType  string `json:"match_type"`  // app_id, ip_3tuple, dnn, fqdn, conn_cap, domain
	MatchValue string `json:"match_value"`
}

// RouteDescriptor is a single RSD component (TS 23.503 §6.6.2.2).
type RouteDescriptor struct {
	ID             int64   `json:"id"`
	RuleID         int64   `json:"rule_id"`
	Precedence     int     `json:"precedence"`
	SST            *int    `json:"sst"`
	SD             *string `json:"sd,omitempty"`
	DNN            string  `json:"dnn,omitempty"`
	PDUSessionType string  `json:"pdu_session_type,omitempty"`
	AccessType     string  `json:"access_type,omitempty"`
}

// Rule is one URSP rule with its descriptors attached.
type Rule struct {
	ID                  int64               `json:"id"`
	IMSI                *string             `json:"imsi"` // nil = global
	Precedence          int                 `json:"precedence"`
	Description         string              `json:"description"`
	Enabled             int                 `json:"enabled"`
	CreatedAt           string              `json:"created_at,omitempty"`
	TrafficDescriptors  []TrafficDescriptor `json:"traffic_descriptors"`
	RouteDescriptors    []RouteDescriptor   `json:"route_descriptors"`
}

// TrafficInfo is the input to EvaluateURSP — describes the traffic to match.
type TrafficInfo struct {
	AppID    string
	DNN      string
	FQDN     string
	DstIP    string
	DstPort  string
	Protocol string
	ConnCap  string
	Domain   string
}

// EvalResult is the output of EvaluateURSP when a match is found.
type EvalResult struct {
	RuleID          int64           `json:"rule_id"`
	RulePrecedence  int             `json:"rule_precedence"`
	RuleDescription string          `json:"rule_description"`
	RouteDescriptor RouteDescriptor `json:"route_descriptor"`
}

// ── CRUD ───────────────────────────────────────────────────────────────

// fetchTrafficDescriptors returns TDs for a rule.
func fetchTrafficDescriptors(ruleID int64) ([]TrafficDescriptor, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, rule_id, match_type, match_value
		 FROM ursp_traffic_descriptors WHERE rule_id=? ORDER BY id`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrafficDescriptor
	for rows.Next() {
		var td TrafficDescriptor
		if err := rows.Scan(&td.ID, &td.RuleID, &td.MatchType, &td.MatchValue); err != nil {
			return nil, err
		}
		out = append(out, td)
	}
	return out, rows.Err()
}

// fetchRouteDescriptors returns RSDs for a rule, ordered by precedence.
func fetchRouteDescriptors(ruleID int64) ([]RouteDescriptor, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, rule_id, precedence, sst, sd, dnn, pdu_session_type, access_type
		 FROM ursp_route_descriptors WHERE rule_id=? ORDER BY precedence, id`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteDescriptor
	for rows.Next() {
		var rd RouteDescriptor
		var sst *int
		var sd, dnn, pst, at *string
		if err := rows.Scan(&rd.ID, &rd.RuleID, &rd.Precedence, &sst, &sd,
			&dnn, &pst, &at); err != nil {
			return nil, err
		}
		rd.SST = sst
		rd.SD = sd
		if dnn != nil {
			rd.DNN = *dnn
		}
		if pst != nil {
			rd.PDUSessionType = *pst
		}
		if at != nil {
			rd.AccessType = *at
		}
		out = append(out, rd)
	}
	return out, rows.Err()
}

// enrichRule attaches traffic and route descriptors to a rule.
func enrichRule(r *Rule) error {
	tds, err := fetchTrafficDescriptors(r.ID)
	if err != nil {
		return err
	}
	r.TrafficDescriptors = tds
	rds, err := fetchRouteDescriptors(r.ID)
	if err != nil {
		return err
	}
	r.RouteDescriptors = rds
	return nil
}

// List returns URSP rules. If imsi is non-empty, returns per-UE + global rules.
func List(imsi string) ([]Rule, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var query string
	var args []any
	if imsi != "" {
		query = `SELECT id, imsi, precedence, description, enabled, created_at
		         FROM ursp_rules WHERE imsi=? OR imsi IS NULL
		         ORDER BY precedence ASC`
		args = []any{imsi}
	} else {
		query = `SELECT id, imsi, precedence, description, enabled, created_at
		         FROM ursp_rules ORDER BY precedence ASC`
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	// Drain the result set BEFORE recursing into enrichRule.
	// engine.Open() pins MaxOpenConns=1 (SQLite single-writer);
	// nesting another Query while these rows are still open
	// deadlocks the only connection.
	var out []Rule
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.IMSI, &r.Precedence, &r.Description,
			&r.Enabled, &r.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err := enrichRule(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Get returns a single rule with descriptors, or nil.
func Get(ruleID int64) (*Rule, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var r Rule
	err = db.QueryRow(
		`SELECT id, imsi, precedence, description, enabled, created_at
		 FROM ursp_rules WHERE id=?`, ruleID,
	).Scan(&r.ID, &r.IMSI, &r.Precedence, &r.Description, &r.Enabled, &r.CreatedAt)
	if err != nil {
		return nil, nil // not found
	}
	if err := enrichRule(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Status returns a summary for the URSP subsystem.
func Status() map[string]any {
	list, _ := List("")
	return map[string]any{"count": len(list)}
}

// ── Rule Evaluation Engine (TS 23.503 §6.6) ──────────────────────────

// GetURSPForUE returns all enabled URSP rules for a UE, sorted by precedence.
func GetURSPForUE(imsi string) ([]Rule, error) {
	rules, err := List(imsi)
	if err != nil {
		return nil, err
	}
	var enabled []Rule
	for _, r := range rules {
		if r.Enabled != 0 {
			enabled = append(enabled, r)
		}
	}
	return enabled, nil
}

// matchTrafficDescriptor checks if a single TD matches the traffic info.
func matchTrafficDescriptor(td TrafficDescriptor, traffic TrafficInfo) bool {
	switch td.MatchType {
	case "app_id":
		return traffic.AppID == td.MatchValue

	case "ip_3tuple":
		// Format: "protocol,dst_ip,dst_port" — '*' is wildcard
		parts := strings.SplitN(td.MatchValue, ",", 3)
		if len(parts) != 3 {
			return false
		}
		vals := []string{traffic.Protocol, traffic.DstIP, traffic.DstPort}
		for i, pattern := range parts {
			pattern = strings.TrimSpace(pattern)
			if pattern == "*" {
				continue
			}
			v := vals[i]
			if v == "" {
				v = "*"
			}
			if pattern != v {
				return false
			}
		}
		return true

	case "dnn":
		return traffic.DNN == td.MatchValue

	case "fqdn":
		matched, _ := filepath.Match(strings.ToLower(td.MatchValue), strings.ToLower(traffic.FQDN))
		return matched

	case "conn_cap":
		return traffic.ConnCap == td.MatchValue

	case "domain":
		fqdn := traffic.FQDN
		if fqdn == "" {
			fqdn = traffic.Domain
		}
		matched, _ := filepath.Match(strings.ToLower(td.MatchValue), strings.ToLower(fqdn))
		return matched
	}
	return false
}

// ruleMatches checks if any of a rule's TDs match (OR logic per TS 23.503 §6.6.2.1).
func ruleMatches(rule Rule, traffic TrafficInfo) bool {
	if len(rule.TrafficDescriptors) == 0 {
		return true // catch-all / match-all
	}
	for _, td := range rule.TrafficDescriptors {
		if matchTrafficDescriptor(td, traffic) {
			return true
		}
	}
	return false
}

// EvaluateURSP evaluates URSP rules for a UE and returns the best match.
// TS 23.503 §6.6: rules evaluated in precedence order; first match wins.
func EvaluateURSP(imsi string, traffic TrafficInfo) (*EvalResult, error) {
	log := logger.Get("pcf.ursp")
	rules, err := GetURSPForUE(imsi)
	if err != nil {
		return nil, err
	}

	for _, rule := range rules {
		if !ruleMatches(rule, traffic) {
			continue
		}
		if len(rule.RouteDescriptors) == 0 {
			log.Warnf("URSP rule id=%d matched but has no route descriptors", rule.ID)
			continue
		}
		best := rule.RouteDescriptors[0]
		sstStr := "<nil>"
		if best.SST != nil {
			sstStr = strconv.Itoa(*best.SST)
		}
		log.Infof("URSP match: rule id=%d prec=%d -> RSD sst=%s dnn=%s imsi=%s",
			rule.ID, rule.Precedence, sstStr, best.DNN, imsi)
		return &EvalResult{
			RuleID:          rule.ID,
			RulePrecedence:  rule.Precedence,
			RuleDescription: rule.Description,
			RouteDescriptor: best,
		}, nil
	}
	log.Debugf("URSP: no matching rule for imsi=%s", imsi)
	return nil, nil
}

// ── URSP NAS IE Encoding (TS 24.501 §5.4.4 / TS 24.526) ────────────

// Encoded TD/RSD type IDs per TS 24.526 Table 5.2.1.
const (
	tdTypeAppID    = 0x01
	tdTypeIP3Tuple = 0x02
	tdTypeDNN      = 0x04
	tdTypeFQDN     = 0x08
	tdTypeConnCap  = 0x10
)

const (
	rsdTypeSST        = 0x01
	rsdTypeDNN        = 0x02
	rsdTypePDUType    = 0x04
	rsdTypeAccessType = 0x08
)

var pduTypeMap = map[string]int{
	"IPv4": 0x01, "IPv6": 0x02, "IPv4v6": 0x03, "Unstructured": 0x04,
}

var accessTypeMap = map[string]int{
	"3GPP": 0x01, "non-3GPP": 0x02, "any": 0x03,
}

var tdTypeMap = map[string]int{
	"app_id": tdTypeAppID, "ip_3tuple": tdTypeIP3Tuple,
	"dnn": tdTypeDNN, "fqdn": tdTypeFQDN,
	"conn_cap": tdTypeConnCap, "domain": tdTypeFQDN,
}

// EncodedTD is a NAS-encoded traffic descriptor component.
type EncodedTD struct {
	TypeID   int    `json:"type_id"`
	TypeName string `json:"type_name"`
	Value    string `json:"value"`
	RawValue string `json:"raw_value"`
}

// EncodedRSDComponent is one component of an encoded route selection descriptor.
type EncodedRSDComponent struct {
	TypeID   int    `json:"type_id"`
	TypeName string `json:"type_name"`
	Value    any    `json:"value,omitempty"`
	SST      *int   `json:"sst,omitempty"`
	SD       any    `json:"sd,omitempty"`
	Encoded  int    `json:"encoded,omitempty"`
}

// EncodedRSD is a NAS-encoded route selection descriptor.
type EncodedRSD struct {
	Precedence int                   `json:"precedence"`
	Components []EncodedRSDComponent `json:"components"`
}

// EncodedRule is a NAS-encoded URSP rule.
type EncodedRule struct {
	RuleID                    int64        `json:"rule_id"`
	Precedence                int          `json:"precedence"`
	TrafficDescriptors        []EncodedTD  `json:"traffic_descriptors"`
	RouteSelectionDescriptors []EncodedRSD `json:"route_selection_descriptors"`
}

// URSPIE is the full URSP IE payload for UE Configuration Update.
type URSPIE struct {
	IEI         int           `json:"iei"`
	Instruction string        `json:"instruction"`
	URSPRules   []EncodedRule `json:"ursp_rules"`
	RuleCount   int           `json:"rule_count"`
}

func encodeTD(td TrafficDescriptor) EncodedTD {
	typeID := tdTypeMap[td.MatchType]
	rawHex := ""
	if td.MatchValue != "" {
		for _, b := range []byte(td.MatchValue) {
			rawHex += strconv.FormatInt(int64(b), 16)
		}
	}
	return EncodedTD{
		TypeID:   typeID,
		TypeName: td.MatchType,
		Value:    td.MatchValue,
		RawValue: rawHex,
	}
}

func encodeRSD(rd RouteDescriptor) EncodedRSD {
	var components []EncodedRSDComponent

	if rd.SST != nil {
		comp := EncodedRSDComponent{
			TypeID: rsdTypeSST, TypeName: "S-NSSAI",
			SST: rd.SST,
		}
		if rd.SD != nil {
			comp.SD = *rd.SD
		}
		components = append(components, comp)
	}
	if rd.DNN != "" {
		components = append(components, EncodedRSDComponent{
			TypeID: rsdTypeDNN, TypeName: "DNN", Value: rd.DNN,
		})
	}
	if rd.PDUSessionType != "" {
		components = append(components, EncodedRSDComponent{
			TypeID:   rsdTypePDUType,
			TypeName: "PDU_session_type",
			Value:    rd.PDUSessionType,
			Encoded:  pduTypeMap[rd.PDUSessionType],
		})
	}
	if rd.AccessType != "" && rd.AccessType != "any" {
		components = append(components, EncodedRSDComponent{
			TypeID:   rsdTypeAccessType,
			TypeName: "access_type",
			Value:    rd.AccessType,
			Encoded:  accessTypeMap[rd.AccessType],
		})
	}
	return EncodedRSD{Precedence: rd.Precedence, Components: components}
}

// EncodeRule encodes a single URSP rule into the NAS IE dict structure.
func EncodeRule(rule Rule) EncodedRule {
	tds := make([]EncodedTD, 0, len(rule.TrafficDescriptors))
	for _, td := range rule.TrafficDescriptors {
		tds = append(tds, encodeTD(td))
	}
	rsds := make([]EncodedRSD, 0, len(rule.RouteDescriptors))
	for _, rd := range rule.RouteDescriptors {
		rsds = append(rsds, encodeRSD(rd))
	}
	return EncodedRule{
		RuleID:                    rule.ID,
		Precedence:                rule.Precedence,
		TrafficDescriptors:        tds,
		RouteSelectionDescriptors: rsds,
	}
}

// BuildURSPIEForUE builds the full URSP IE payload for UE Configuration Update.
// Per TS 24.501 §5.4.4.
func BuildURSPIEForUE(imsi string) (*URSPIE, error) {
	log := logger.Get("pcf.ursp.delivery")
	rules, err := GetURSPForUE(imsi)
	if err != nil {
		return nil, err
	}
	encoded := make([]EncodedRule, 0, len(rules))
	for _, r := range rules {
		encoded = append(encoded, EncodeRule(r))
	}
	log.Infof("Built URSP IE for imsi=%s: %d rules", imsi, len(encoded))
	return &URSPIE{
		IEI:         0x76,
		Instruction: "INSTALL",
		URSPRules:   encoded,
		RuleCount:   len(encoded),
	}, nil
}

// BuildURSPIEForRule builds URSP IE for a single rule (push/test).
func BuildURSPIEForRule(ruleID int64) (*URSPIE, error) {
	rule, err := Get(ruleID)
	if err != nil || rule == nil {
		return &URSPIE{
			IEI: 0x76, Instruction: "INSTALL",
			URSPRules: []EncodedRule{}, RuleCount: 0,
		}, err
	}
	encoded := EncodeRule(*rule)
	return &URSPIE{
		IEI:         0x76,
		Instruction: "INSTALL",
		URSPRules:   []EncodedRule{encoded},
		RuleCount:   1,
	}, nil
}

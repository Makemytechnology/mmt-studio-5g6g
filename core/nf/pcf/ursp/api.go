// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// api.go — operator-API CRUD helpers for URSP rules + descriptors.
//
// Spec anchors:
//
//   - TS 23.503 §6.6   — UE Route Selection Policy (umbrella).
//   - TS 23.503 §6.6.2.1 — Traffic Descriptor components
//                          (app_id / ip_3tuple / dnn / fqdn / conn_cap / domain).
//   - TS 23.503 §6.6.2.2 — Route Selection Descriptor components.
//   - TS 24.526 Table 5.2.1 — Encoded TD/RSD type IDs (consumed by
//                              the existing encoder helpers).
//
// The existing ursp.go ships read + evaluate + encode paths; these
// helpers add the operator/test write side so the panel can author
// rules end-to-end.
package ursp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// validTDMatchTypes mirrors the schema CHECK constraint on
// `ursp_traffic_descriptors.match_type`. Surfacing the bad value
// here gives the route layer a clean 400 instead of a SQLite CHECK
// 500.
var validTDMatchTypes = map[string]bool{
	"app_id":    true,
	"ip_3tuple": true,
	"dnn":       true,
	"fqdn":      true,
	"conn_cap":  true,
	"domain":    true,
}

var validPDUSessionTypes = map[string]bool{
	"":             true,
	"IPv4":         true,
	"IPv6":         true,
	"IPv4v6":       true,
	"Unstructured": true,
}

var validAccessTypes = map[string]bool{
	"":         true,
	"3GPP":     true,
	"non-3GPP": true,
	"any":      true,
}

// CreateInput carries everything CreateRule needs in one shot —
// header attributes plus the descriptors that compose the rule.
// Empty IMSI → global rule.
type CreateInput struct {
	IMSI               string
	Precedence         int
	Description        string
	Enabled            int
	TrafficDescriptors []TrafficDescriptor
	RouteDescriptors   []RouteDescriptor
}

// CreateRule writes one URSP rule + descriptors atomically. Returns
// the new rule ID; the route layer can re-read with Get to surface
// the full structure.
func CreateRule(in CreateInput) (int64, error) {
	if in.Precedence < 0 || in.Precedence > 255 {
		return 0, fmt.Errorf(
			"precedence %d out of [0, 255] (TS 23.503 §6.6)",
			in.Precedence)
	}
	if len(in.TrafficDescriptors) == 0 {
		return 0, fmt.Errorf(
			"at least one traffic_descriptor required (TS 23.503 §6.6.2.1)")
	}
	if len(in.RouteDescriptors) == 0 {
		return 0, fmt.Errorf(
			"at least one route_descriptor required (TS 23.503 §6.6.2.2)")
	}
	for i, td := range in.TrafficDescriptors {
		if !validTDMatchTypes[td.MatchType] {
			return 0, fmt.Errorf(
				"traffic_descriptors[%d].match_type %q invalid; want one of "+
					"app_id|ip_3tuple|dnn|fqdn|conn_cap|domain", i, td.MatchType)
		}
		if td.MatchValue == "" {
			return 0, fmt.Errorf(
				"traffic_descriptors[%d].match_value required", i)
		}
	}
	for i, rd := range in.RouteDescriptors {
		if !validPDUSessionTypes[rd.PDUSessionType] {
			return 0, fmt.Errorf(
				"route_descriptors[%d].pdu_session_type %q invalid; "+
					"want IPv4|IPv6|IPv4v6|Unstructured", i, rd.PDUSessionType)
		}
		if !validAccessTypes[rd.AccessType] {
			return 0, fmt.Errorf(
				"route_descriptors[%d].access_type %q invalid; "+
					"want 3GPP|non-3GPP|any", i, rd.AccessType)
		}
	}

	enabled := in.Enabled
	if enabled == 0 {
		enabled = 1 // default-on so a freshly authored rule is live
	}

	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var imsiArg any
	if in.IMSI != "" {
		imsiArg = in.IMSI
	}
	res, err := tx.Exec(`INSERT INTO ursp_rules
		(imsi, precedence, description, enabled)
		VALUES (?, ?, ?, ?)`,
		imsiArg, in.Precedence, in.Description, enabled)
	if err != nil {
		return 0, err
	}
	ruleID, _ := res.LastInsertId()

	for _, td := range in.TrafficDescriptors {
		if _, err = tx.Exec(`INSERT INTO ursp_traffic_descriptors
			(rule_id, match_type, match_value) VALUES (?, ?, ?)`,
			ruleID, td.MatchType, td.MatchValue); err != nil {
			return 0, err
		}
	}
	for _, rd := range in.RouteDescriptors {
		var sstArg, sdArg, accessArg any
		if rd.SST != nil {
			sstArg = *rd.SST
		}
		if rd.SD != nil {
			sdArg = *rd.SD
		}
		accessArg = rd.AccessType
		if rd.AccessType == "" {
			accessArg = "any"
		}
		// pdu_session_type is OPTIONAL in TS 23.503 §6.6.2.2 — when
		// the rule doesn't pin a session type, the column must be
		// NULL so the CHECK (one-of-the-four-enum-values) is not
		// evaluated against an empty string.
		var pstArg any
		if rd.PDUSessionType != "" {
			pstArg = rd.PDUSessionType
		}
		if _, err = tx.Exec(`INSERT INTO ursp_route_descriptors
			(rule_id, precedence, sst, sd, dnn, pdu_session_type, access_type)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ruleID, rd.Precedence, sstArg, sdArg, rd.DNN,
			pstArg, accessArg); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return ruleID, nil
}

// UpdateRule applies a sparse update to a URSP rule's header
// attributes. Allow-listed columns: precedence, description,
// enabled, imsi. Returns true when a row was updated; false → 404.
func UpdateRule(ruleID int64, patch map[string]any) (bool, error) {
	allowed := map[string]bool{
		"precedence":  true,
		"description": true,
		"enabled":     true,
		"imsi":        true,
	}
	cols := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		if k == "precedence" {
			n := toInt(v)
			if n < 0 || n > 255 {
				return false, fmt.Errorf(
					"precedence %d out of [0, 255]", n)
			}
		}
		// Empty imsi → global; map "" to NULL.
		if k == "imsi" {
			s, _ := v.(string)
			if s == "" {
				cols = append(cols, "imsi=NULL")
				continue
			}
			cols = append(cols, "imsi=?")
			args = append(args, s)
			continue
		}
		cols = append(cols, k+"=?")
		args = append(args, v)
	}
	if len(cols) == 0 {
		return false, fmt.Errorf(
			"no allowed fields in patch (precedence|description|enabled|imsi)")
	}
	args = append(args, ruleID)
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	q := "UPDATE ursp_rules SET " + strings.Join(cols, ", ") + " WHERE id=?"
	res, err := db.Exec(q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteRule removes a rule + its descriptors (FK CASCADE handles
// the TD/RSD rows). Returns true when removed; false → 404.
func DeleteRule(ruleID int64) (bool, error) {
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	res, err := db.Exec(`DELETE FROM ursp_rules WHERE id=?`, ruleID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── helpers ─────────────────────────────────────────────────────

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

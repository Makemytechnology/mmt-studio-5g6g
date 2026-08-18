// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ran_sharing — operator surface for NG-RAN sharing.
//
// Models the two operator-side artefacts that NG-RAN Sharing
// requires: a sharing agreement (which PLMNs share which gNB,
// with what capacity split) and a per-gNB allocation map. The
// AMF / SMF call CheckAccess() during Initial Registration to
// decide whether a UE from a partner PLMN is admissible on a
// shared cell.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 22.261 §6.21         NG-RAN Sharing — umbrella service
//                             requirement (defines MORAN / MOCN
//                             concepts and the operator obligations
//                             to advertise + admit shared PLMNs).
//   - TS 22.261 §6.21.2.2     Indirect network sharing (one operator
//                             rents capacity from another via SMF
//                             roaming; not the MOCN gNB-share path).
//   - TS 23.501 §5.17.4       Network sharing support and interworking
//                             between EPS and 5GS — the 5GC clause
//                             that makes the NG-RAN sharing visible to
//                             the core (PLMN selection per UE).
//
// MORAN — Multi-Operator RAN: gNB hardware shared, but each PLMN has
// its own carrier / spectrum slice. Capacity split is per-gNB.
//
// MOCN — Multi-Operator Core Network: a single carrier serves
// multiple PLMNs. The gNB broadcasts every served PLMN-ID; UEs
// camp on whichever matches their HPLMN. Capacity is admission-
// controlled rather than spectrum-partitioned.
//
// Deferred (TODO at unimplemented surfaces):
//
//   - TODO(spec: TS 23.251)   The dedicated NG-RAN Sharing Stage-2
//                             spec is not loaded locally. CheckAccess
//                             enforces the operator-side admission
//                             contract; the gNB-side broadcast of the
//                             multi-PLMN-ID list (TS 38.413 §9.2.6.x)
//                             is the radio's responsibility.
//   - TODO(spec: TS 22.261)   MOCN per-PLMN QoS class differentiation
//                             — today the agreement carries a flat
//                             priority_rules JSON blob for whichever
//                             operator owns the SMF instance.
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/access_mobility.py.
package ran_sharing

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Sharing types accepted by the ran_sharing_agreements CHECK constraint.
const (
	SharingMORAN = "MORAN"
	SharingMOCN  = "MOCN"
)

// Status values for ran_sharing_agreements.status.
const (
	StatusActive   = "active"
	StatusInactive = "inactive"
)

var validSharing = map[string]bool{SharingMORAN: true, SharingMOCN: true}

// ─── Agreement CRUD (TS 22.261 §6.21) ────────────────────────────

// CreateAgreement registers a new RAN sharing agreement. Returns the
// stored row including the generated id and default status='active'.
func CreateAgreement(name, sharingType, plmns string,
	capacitySplit, priorityRules map[string]interface{}) (map[string]interface{}, error) {
	if name == "" {
		return nil, errors.New("agreement name is required")
	}
	if !validSharing[sharingType] {
		return nil, fmt.Errorf("sharing_type must be MORAN or MOCN, got %q", sharingType)
	}
	if plmns == "" {
		return nil, errors.New("participating_plmns is required")
	}
	csJSON, _ := json.Marshal(capacitySplit)
	prJSON, _ := json.Marshal(priorityRules)
	res, err := engine.Exec(
		`INSERT INTO ran_sharing_agreements
		 (name, sharing_type, participating_plmns, capacity_split_json, priority_rules_json)
		 VALUES (?,?,?,?,?)`,
		name, sharingType, plmns, string(csJSON), string(prJSON))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	logger.Get("ran_sharing").Infof(
		"RAN sharing agreement created id=%d name=%q type=%s",
		id, name, sharingType)
	return GetAgreement(id)
}

// GetAgreement returns one agreement row by id.
func GetAgreement(id int64) (map[string]interface{}, error) {
	return qRow("SELECT * FROM ran_sharing_agreements WHERE id=?", id)
}

// ListAgreements returns every agreement, optionally filtered by status.
func ListAgreements(status string) ([]map[string]interface{}, error) {
	if status != "" {
		return qRows(
			"SELECT * FROM ran_sharing_agreements WHERE status=? ORDER BY id DESC",
			status)
	}
	return qRows("SELECT * FROM ran_sharing_agreements ORDER BY id DESC")
}

// UpdateAgreement applies a sparse update to one agreement row. Only
// columns in the explicit allow-list are accepted (no SQL injection
// via column names from the operator panel).
func UpdateAgreement(id int64, fields map[string]interface{}) (map[string]interface{}, error) {
	var sets []string
	var args []interface{}
	for _, col := range []string{"name", "sharing_type", "participating_plmns", "status"} {
		if v, ok := fields[col]; ok {
			sets = append(sets, col+"=?")
			args = append(args, v)
		}
	}
	if cs, ok := fields["capacity_split"]; ok {
		b, _ := json.Marshal(cs)
		sets = append(sets, "capacity_split_json=?")
		args = append(args, string(b))
	}
	if pr, ok := fields["priority_rules"]; ok {
		b, _ := json.Marshal(pr)
		sets = append(sets, "priority_rules_json=?")
		args = append(args, string(b))
	}
	if len(sets) == 0 {
		return GetAgreement(id)
	}
	args = append(args, id)
	_, err := engine.Exec(
		fmt.Sprintf("UPDATE ran_sharing_agreements SET %s WHERE id=?",
			strings.Join(sets, ", ")),
		args...)
	if err != nil {
		return nil, err
	}
	return GetAgreement(id)
}

// ActivateAgreement flips status → 'active'.
func ActivateAgreement(id int64) (map[string]interface{}, error) {
	return UpdateAgreement(id, map[string]interface{}{"status": StatusActive})
}

// DeactivateAgreement flips status → 'inactive'.
func DeactivateAgreement(id int64) (map[string]interface{}, error) {
	return UpdateAgreement(id, map[string]interface{}{"status": StatusInactive})
}

// DeleteAgreement removes an agreement; returns true on actual delete.
func DeleteAgreement(id int64) bool {
	res, err := engine.Exec("DELETE FROM ran_sharing_agreements WHERE id=?", id)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// ─── Per-gNB allocation (MORAN — TS 22.261 §6.21) ────────────────

// ListGnBMap returns gNB capacity allocations. Pass agreementID=0 to
// list across every agreement.
func ListGnBMap(agreementID int64) ([]map[string]interface{}, error) {
	if agreementID > 0 {
		return qRows(
			"SELECT * FROM ran_sharing_gnb_map WHERE agreement_id=? ORDER BY gnb_id",
			agreementID)
	}
	return qRows("SELECT * FROM ran_sharing_gnb_map ORDER BY agreement_id, gnb_id")
}

// UpsertGnBMap registers (or updates) the capacity percentage one
// gNB devotes to one agreement. Useful for MORAN where each shared
// gNB carries a per-PLMN spectrum/scheduling slice.
func UpsertGnBMap(agreementID int64, gnbID string, capacityPct int) (map[string]interface{}, error) {
	if capacityPct < 0 || capacityPct > 100 {
		return nil, errors.New("capacity_pct must be in [0, 100]")
	}
	_, err := engine.Exec(
		`INSERT INTO ran_sharing_gnb_map (agreement_id, gnb_id, allocated_capacity_pct)
		 VALUES (?,?,?)
		 ON CONFLICT(agreement_id, gnb_id) DO UPDATE SET
		   allocated_capacity_pct=excluded.allocated_capacity_pct`,
		agreementID, gnbID, capacityPct)
	if err != nil {
		return nil, err
	}
	return qRow(
		"SELECT * FROM ran_sharing_gnb_map WHERE agreement_id=? AND gnb_id=?",
		agreementID, gnbID)
}

// DeleteGnBMap removes one (agreement, gNB) row.
func DeleteGnBMap(agreementID int64, gnbID string) error {
	_, err := engine.Exec(
		"DELETE FROM ran_sharing_gnb_map WHERE agreement_id=? AND gnb_id=?",
		agreementID, gnbID)
	return err
}

// ─── Admission gate ──────────────────────────────────────────────

// AccessResult is the structured outcome of CheckAccess.
type AccessResult struct {
	Allowed       bool   `json:"allowed"`
	Reason        string `json:"reason,omitempty"`
	AgreementID   int64  `json:"agreement_id,omitempty"`
	AgreementName string `json:"agreement_name,omitempty"`
	SharingType   string `json:"sharing_type,omitempty"`
	CapacityPct   int    `json:"capacity_pct,omitempty"`
}

// CheckAccess decides whether a UE from `plmn` is admissible on
// `gnbID`. Walks active agreements; the first matching agreement
// wins. MOCN admits if the PLMN is in the participating list (no
// per-gNB allocation needed); MORAN requires an explicit gNB map row.
func CheckAccess(plmn, gnbID string) AccessResult {
	agrs, _ := ListAgreements(StatusActive)
	for _, agr := range agrs {
		plmns := fmt.Sprintf("%v", agr["participating_plmns"])
		if !plmnInList(plmn, plmns) {
			continue
		}
		agrID := toInt64(agr["id"])
		agrName, _ := agr["name"].(string)
		stype := fmt.Sprintf("%v", agr["sharing_type"])
		gnbMaps, _ := ListGnBMap(agrID)
		for _, gm := range gnbMaps {
			if fmt.Sprintf("%v", gm["gnb_id"]) == gnbID {
				return AccessResult{
					Allowed:       true,
					Reason:        "matched per-gNB allocation",
					AgreementID:   agrID,
					AgreementName: agrName,
					SharingType:   stype,
					CapacityPct:   intValue(gm["allocated_capacity_pct"]),
				}
			}
		}
		if stype == SharingMOCN {
			// MOCN agreements admit on any gNB the agreement applies to,
			// even without a per-gNB map row — the gNB advertises the
			// shared PLMN-ID list directly.
			return AccessResult{
				Allowed:       true,
				Reason:        "MOCN agreement (no per-gNB cap)",
				AgreementID:   agrID,
				AgreementName: agrName,
				SharingType:   SharingMOCN,
			}
		}
	}
	return AccessResult{
		Allowed: false,
		Reason:  "no matching active agreement",
	}
}

// CheckAccessMap is a map-shape wrapper for the REST handlers.
func CheckAccessMap(plmn, gnbID string) map[string]interface{} {
	r := CheckAccess(plmn, gnbID)
	out := map[string]interface{}{"allowed": r.Allowed, "reason": r.Reason}
	if r.AgreementID != 0 {
		out["agreement_id"] = r.AgreementID
		out["agreement_name"] = r.AgreementName
		out["sharing_type"] = r.SharingType
		out["capacity_pct"] = r.CapacityPct
	}
	return out
}

// ─── Usage Log ───────────────────────────────────────────────────

// InsertUsageLog records one bin of per-(PLMN, gNB) usage for billing /
// audit. throughputMbps is the operator-observed average over the bin.
func InsertUsageLog(agreementID int64, plmn, gnbID string,
	ueCount int, throughputMbps float64) error {
	_, err := engine.Exec(
		`INSERT INTO ran_sharing_usage_log
		 (agreement_id, plmn, gnb_id, ue_count, throughput_mbps)
		 VALUES (?,?,?,?,?)`,
		agreementID, plmn, gnbID, ueCount, throughputMbps)
	return err
}

// ListUsageLog returns the most recent usage entries (newest first).
func ListUsageLog(agreementID int64, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	if agreementID > 0 {
		return qRows(
			"SELECT * FROM ran_sharing_usage_log WHERE agreement_id=? ORDER BY id DESC LIMIT ?",
			agreementID, limit)
	}
	return qRows(
		"SELECT * FROM ran_sharing_usage_log ORDER BY id DESC LIMIT ?",
		limit)
}

// ─── Stats ───────────────────────────────────────────────────────

// GetStats returns coarse counters for the operator dashboard.
func GetStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{}
	}
	var total, active, gnbs, usage int
	_ = db.QueryRow("SELECT COUNT(*) FROM ran_sharing_agreements").Scan(&total)
	_ = db.QueryRow("SELECT COUNT(*) FROM ran_sharing_agreements WHERE status='active'").Scan(&active)
	_ = db.QueryRow("SELECT COUNT(DISTINCT gnb_id) FROM ran_sharing_gnb_map").Scan(&gnbs)
	_ = db.QueryRow("SELECT COUNT(*) FROM ran_sharing_usage_log").Scan(&usage)
	return map[string]interface{}{
		"total_agreements":  total,
		"active_agreements": active,
		"mapped_gnbs":       gnbs,
		"usage_entries":     usage,
	}
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]map[string]any, error) { return ListAgreements("") }
func Status() map[string]any          { return GetStats() }

// ─── helpers ─────────────────────────────────────────────────────

// plmnInList tests `plmn` against a comma-or-space-separated list,
// matching the Python tester's behaviour. Substring containment on
// the raw column is too loose ("310" matches "310-260" and "23410").
func plmnInList(plmn, list string) bool {
	if plmn == "" {
		return false
	}
	// Normalise separators to commas, then split.
	for _, s := range []string{";", " ", "\t"} {
		list = strings.ReplaceAll(list, s, ",")
	}
	for _, p := range strings.Split(list, ",") {
		if strings.TrimSpace(p) == plmn {
			return true
		}
	}
	return false
}

func toInt64(v interface{}) int64 {
	switch vv := v.(type) {
	case int64:
		return vv
	case float64:
		return int64(vv)
	case int:
		return int64(vv)
	}
	return 0
}

func intValue(v interface{}) int {
	switch vv := v.(type) {
	case int64:
		return int(vv)
	case float64:
		return int(vv)
	case int:
		return vv
	}
	return 0
}

func qRow(q string, args ...interface{}) (map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		return nil, nil
	}
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	rows.Scan(ptrs...)
	m := make(map[string]interface{}, len(cols))
	for i, c := range cols {
		m[c] = vals[i]
	}
	return m, nil
}

func qRows(q string, args ...interface{}) ([]map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, nil
}

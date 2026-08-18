// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package disaster_roaming — Disaster Roaming admission control.
//
// When the operator declares a Disaster Condition, UEs from PLMNs
// without a normal roaming agreement become admissible — see
// TS 22.261 §6.31 (Minimization of Service Interruption) and
// TS 23.501 §5.40 (Support of Disaster Roaming with Minimization
// of Service Interruption). This package owns the operator-side
// declaration lifecycle and the admission probe used by the AMF
// during Initial Registration.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 22.261 §6.31         Minimization of Service Interruption
//                             (umbrella service requirement).
//   - TS 22.261 §6.31.1       Description — what a Disaster Condition is.
//   - TS 22.261 §6.31.2       Requirements — operator + UE behaviour.
//   - TS 22.261 §6.31.2.2     Disaster Condition definition (declared
//                             by national authority; geographically scoped).
//   - TS 22.261 §6.31.2.3     Disaster Roaming — special policy that
//                             applies during a Disaster Condition.
//   - TS 23.501 §5.40         Support of Disaster Roaming (5GS arch.).
//   - TS 23.501 §5.40.1       General — admission rules for Disaster
//                             Inbound Roamers.
//   - TS 23.501 §5.40.2       UE configuration and provisioning.
//   - TS 23.501 §5.40.4       Registration for Disaster Roaming service.
//   - TS 23.501 §5.40.5       Handling when a Disaster Condition is no
//                             longer applicable — release semantics.
//   - TS 23.501 §5.40.6       Prevention of signalling overload during
//                             Disaster Condition.
//
// Deferred (TODO at the unimplemented call-sites):
//
//   - TS 23.501 §5.40.3       Disaster Condition Notification and
//                             Determination — interface from the
//                             national authority is operator-specific;
//                             today we accept declarations via the
//                             operator panel only.
//   - TS 23.501 §5.40.7       UE Provisioning for EPS path.
//   - TS 24.501 §5.3.20       Reject-cause + back-off coordination
//                             with the UE (we set 'allowed=false'
//                             and let the AMF emit the NAS reject).
//   - TODO(spec: TS 23.122)   Network selection for Disaster Roaming
//                             (UE-side state machine — out of scope).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/safety_disaster_roaming.py.
package disaster_roaming

import (
	"errors"
	"fmt"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ─── Declaration CRUD ────────────────────────────────────────────

// DeclareDisaster records a new active Disaster Condition. Per
// TS 22.261 §6.31.2.2 the declaration is the precondition for any
// Disaster-Roaming admission decision. Multiple concurrent
// declarations are allowed (e.g. wildfire + flood) — they widen the
// admission gate but the row only carries the metadata.
func DeclareDisaster(name, reason, affectedAreas, declaredBy string) (int64, error) {
	if name == "" {
		return 0, errors.New("name is required")
	}
	if declaredBy == "" {
		declaredBy = "system"
	}
	res, err := engine.Exec(
		`INSERT INTO disaster_declarations (name, reason, affected_areas, declared_by)
		 VALUES (?,?,?,?)`,
		name, reason, affectedAreas, declaredBy)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	logger.Get("disaster_roaming").Warnf(
		"DISASTER declared: id=%d name=%q reason=%q areas=%q by=%s",
		id, name, reason, affectedAreas, declaredBy)
	return id, nil
}

// EndDisaster ends every currently-active declaration. Per
// TS 23.501 §5.40.5 the network must stop admitting new Disaster
// Roamers once the Condition is no longer applicable; existing
// sessions are NOT torn down here (handover to normal release path
// is operator-policy and outside this surface).
func EndDisaster() {
	_, _ = engine.Exec(
		"UPDATE disaster_declarations SET status='ended', ended_at=datetime('now') WHERE status='active'")
	logger.Get("disaster_roaming").Infof("DISASTER ended: all active declarations closed")
}

// EndDisasterByID ends one specific declaration without touching others.
func EndDisasterByID(id int64) error {
	_, err := engine.Exec(
		"UPDATE disaster_declarations SET status='ended', ended_at=datetime('now') WHERE id=? AND status='active'",
		id)
	return err
}

// GetDisasterStatus returns the current operator-visible status —
// whether anything is active and the most recent active declaration.
func GetDisasterStatus() map[string]interface{} {
	decl, _ := GetActiveDeclaration()
	return map[string]interface{}{
		"disaster_active": decl != nil,
		"declaration":     decl,
	}
}

// IsDisasterActive is a fast bool probe for callers that don't need
// the full declaration row.
func IsDisasterActive() bool {
	d, _ := GetActiveDeclaration()
	return d != nil
}

// GetActiveDeclaration returns the most recently declared still-active
// Disaster Condition (newest by id).
func GetActiveDeclaration() (map[string]interface{}, error) {
	return qRow("SELECT * FROM disaster_declarations WHERE status='active' ORDER BY id DESC LIMIT 1")
}

// GetAllDeclarations returns the full history (active + ended).
func GetAllDeclarations() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM disaster_declarations ORDER BY id DESC")
}

// ─── Roaming Admission ───────────────────────────────────────────

// AdmissionResult is the structured outcome of CheckDisasterRoaming.
type AdmissionResult struct {
	Allowed       bool   `json:"allowed"`
	Reason        string `json:"reason"`
	DisasterActive bool  `json:"disaster_active"`
	DeclarationID int64  `json:"declaration_id,omitempty"`
	NormalRoaming bool   `json:"normal_roaming"`
}

// CheckDisasterRoaming decides whether a UE from `hplmn` is
// admissible right now. The shape it returns matches what the AMF
// inserts into NAS reject vs. accept.
//
// Per TS 23.501 §5.40.1 a Disaster Inbound Roamer is admitted iff:
//   - a Disaster Condition is active in this PLMN, AND
//   - the UE's HPLMN does not already have normal roaming.
//
// We still admit when normal roaming exists — but tag the reason so
// the audit log shows the request was redundant.
//
// TODO TS 23.501 §5.40.6: signalling overload prevention. Today we
// admit unconditionally during a declaration; the spec requires
// per-PLMN throttling under congestion to avoid storming the AMF.
func CheckDisasterRoaming(imsi, hplmn string) AdmissionResult {
	log := logger.Get("disaster_roaming")
	decl, _ := GetActiveDeclaration()
	if decl == nil {
		logDR(imsi, hplmn, "denied", "no active disaster")
		return AdmissionResult{
			Allowed:        false,
			Reason:         "no active disaster declaration",
			DisasterActive: false,
		}
	}
	declID := toInt64(decl["id"])
	normal := checkNormalRoaming(hplmn)
	reason := fmt.Sprintf("disaster roaming: PLMN %s admitted (no agreement)", hplmn)
	if normal {
		reason = fmt.Sprintf("disaster active but PLMN %s already has normal roaming agreement", hplmn)
	}
	addRoamingUE(declID, imsi, hplmn)
	logDR(imsi, hplmn, "admitted", reason)
	log.Infof("Disaster roaming: IMSI=%s HPLMN=%s admitted (decl=%d)", imsi, hplmn, declID)
	return AdmissionResult{
		Allowed:        true,
		Reason:         reason,
		DisasterActive: true,
		DeclarationID:  declID,
		NormalRoaming:  normal,
	}
}

// CheckDisasterRoamingMap mirrors CheckDisasterRoaming but returns the
// shape the operator panel + REST handlers consume directly.
func CheckDisasterRoamingMap(imsi, hplmn string) map[string]interface{} {
	r := CheckDisasterRoaming(imsi, hplmn)
	return map[string]interface{}{
		"allowed":         r.Allowed,
		"reason":          r.Reason,
		"disaster_active": r.DisasterActive,
		"declaration_id":  r.DeclarationID,
		"normal_roaming":  r.NormalRoaming,
	}
}

// GetDisasterRoamingUEs returns all UEs currently admitted via an
// active Disaster Condition.
func GetDisasterRoamingUEs() ([]map[string]interface{}, error) {
	return qRows(`SELECT u.*, d.name AS disaster_name FROM disaster_roaming_ues u
		JOIN disaster_declarations d ON d.id=u.declaration_id
		WHERE d.status='active' ORDER BY u.connected_at DESC`)
}

// ReleaseRoamingUE records that a Disaster Roamer has left (used
// when the Condition ends or the UE de-registers). Logs via §5.40.5
// path; does NOT delete the row so audit history survives.
func ReleaseRoamingUE(imsi, hplmn string) error {
	logDR(imsi, hplmn, "released", "Disaster Condition no longer applicable")
	return nil
}

// ─── Logging ─────────────────────────────────────────────────────

func logDR(imsi, hplmn, action, reason string) {
	_, _ = engine.Exec(
		"INSERT INTO disaster_roaming_log (imsi, hplmn, action, reason) VALUES (?,?,?,?)",
		imsi, hplmn, action, reason)
}

// GetDRLog returns recent disaster-roaming log entries (newest first).
func GetDRLog(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	return qRows("SELECT * FROM disaster_roaming_log ORDER BY id DESC LIMIT ?", limit)
}

// ─── Stats ───────────────────────────────────────────────────────

// GetDRStats returns coarse counters for the operator dashboard.
func GetDRStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{}
	}
	var totalDecl, activeDecl, totalUEs, admitted, denied, released int
	_ = db.QueryRow("SELECT COUNT(*) FROM disaster_declarations").Scan(&totalDecl)
	_ = db.QueryRow("SELECT COUNT(*) FROM disaster_declarations WHERE status='active'").Scan(&activeDecl)
	_ = db.QueryRow(`SELECT COUNT(*) FROM disaster_roaming_ues u
		JOIN disaster_declarations d ON d.id=u.declaration_id WHERE d.status='active'`).Scan(&totalUEs)
	_ = db.QueryRow("SELECT COUNT(*) FROM disaster_roaming_log WHERE action='admitted'").Scan(&admitted)
	_ = db.QueryRow("SELECT COUNT(*) FROM disaster_roaming_log WHERE action='denied'").Scan(&denied)
	_ = db.QueryRow("SELECT COUNT(*) FROM disaster_roaming_log WHERE action='released'").Scan(&released)
	return map[string]interface{}{
		"total_declarations":  totalDecl,
		"active_declarations": activeDecl,
		"current_roaming_ues": totalUEs,
		"total_admitted":      admitted,
		"total_denied":        denied,
		"total_released":      released,
	}
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]map[string]any, error) { return GetAllDeclarations() }
func Status() map[string]any          { return GetDRStats() }

// ─── internal ────────────────────────────────────────────────────

func addRoamingUE(declID int64, imsi, hplmn string) {
	_, _ = engine.Exec(
		"INSERT OR IGNORE INTO disaster_roaming_ues (declaration_id, imsi, hplmn) VALUES (?,?,?)",
		declID, imsi, hplmn)
}

func checkNormalRoaming(hplmn string) bool {
	db, err := engine.Open()
	if err != nil {
		return false
	}
	var count int
	_ = db.QueryRow(
		"SELECT COUNT(*) FROM roaming_agreements WHERE partner_plmn_id=? AND enabled=1",
		hplmn).Scan(&count)
	return count > 0
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

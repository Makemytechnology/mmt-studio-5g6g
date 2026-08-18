// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package racs -- Restricted Access Control (TS 23.501 section 5.18).
//
// Go port of safety/racs/*.py. Manages UAC restriction levels (normal,
// restricted, emergency_only, full_lockdown), per-category barring factors,
// access checking with priority subscriber support, and access logging.
package racs

import (
	"fmt"
	"math/rand"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ---- Restriction Level Management ----

func ensureConfig() {
	_, _ = engine.Exec("INSERT OR IGNORE INTO racs_config (id) VALUES (1)")
}

// GetRestrictionStatus returns current RACS config.
func GetRestrictionStatus() map[string]interface{} {
	ensureConfig()
	m, _ := qRow("SELECT * FROM racs_config WHERE id=1")
	if m == nil { return map[string]interface{}{"restriction_level": "normal"} }
	return m
}

// ActivateRestriction activates a RACS restriction level.
func ActivateRestriction(level, reason, areas, activatedBy string) error {
	valid := map[string]bool{"normal": true, "restricted": true, "emergency_only": true, "full_lockdown": true}
	if !valid[level] { return fmt.Errorf("invalid restriction level: %s", level) }
	ensureConfig()
	_, err := engine.Exec("UPDATE racs_config SET restriction_level=?, reason=?, affected_areas=?, activated_at=datetime('now'), activated_by=? WHERE id=1",
		level, reason, areas, activatedBy)
	logger.Get("racs").Warnf("RACS restriction activated: level=%s reason=%s areas=%s by=%s", level, reason, areas, activatedBy)
	return err
}

// DeactivateRestriction returns to normal access.
func DeactivateRestriction() {
	ensureConfig()
	_, _ = engine.Exec("UPDATE racs_config SET restriction_level='normal', reason='', affected_areas='', activated_at='', activated_by='' WHERE id=1")
	logger.Get("racs").Infof("RACS restriction deactivated -- back to normal")
}

// ---- Barring Config ----

// SetBarringFactor sets barring factor for an access category.
func SetBarringFactor(accessCategory int, factor float64, timeS int) {
	if factor < 0 { factor = 0 }; if factor > 1 { factor = 1 }
	if timeS < 1 { timeS = 1 }
	enabled := 0
	if factor < 1.0 { enabled = 1 }
	_, _ = engine.Exec(`INSERT INTO racs_barring_config (access_category, barring_factor, barring_time_s, enabled)
		VALUES (?,?,?,?) ON CONFLICT(access_category) DO UPDATE SET
		barring_factor=excluded.barring_factor, barring_time_s=excluded.barring_time_s, enabled=excluded.enabled`,
		accessCategory, factor, timeS, enabled)
}

// GetBarringConfigs returns all barring configs.
func GetBarringConfigs() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM racs_barring_config ORDER BY access_category")
}

// EvaluateBarring evaluates whether access should be barred for a given category.
func EvaluateBarring(accessCategory int) (bool, string) {
	m, _ := qRow("SELECT * FROM racs_barring_config WHERE access_category=?", accessCategory)
	if m == nil { return false, "no barring configured" }
	enabled, _ := m["enabled"].(int64)
	if enabled == 0 { return false, "barring not enabled" }
	factor := 1.0
	if f, ok := m["barring_factor"].(float64); ok { factor = f }
	if factor <= 0 { return true, fmt.Sprintf("barring_factor=0.0 for cat=%d", accessCategory) }
	if factor >= 1 { return false, fmt.Sprintf("barring_factor=1.0 for cat=%d", accessCategory) }
	draw := rand.Float64()
	if draw >= factor {
		return true, fmt.Sprintf("barring draw=%.3f >= factor=%.2f for cat=%d", draw, factor, accessCategory)
	}
	return false, fmt.Sprintf("barring draw=%.3f < factor=%.2f for cat=%d", draw, factor, accessCategory)
}

// ---- Access Check ----

// CheckAccess checks whether an access attempt is allowed under current RACS policy.
func CheckAccess(imsi string, accessCategory int) map[string]interface{} {
	cfg := GetRestrictionStatus()
	level := fmt.Sprintf("%v", cfg["restriction_level"])

	logAccess := func(decision, reason string) {
		_, _ = engine.Exec("INSERT INTO racs_access_log (imsi, access_category, restriction_level, decision, reason) VALUES (?,?,?,?,?)",
			imsi, accessCategory, level, decision, reason)
	}

	switch level {
	case "normal":
		barred, reason := EvaluateBarring(accessCategory)
		d := "allowed"; if barred { d = "barred" }
		logAccess(d, reason)
		return map[string]interface{}{"allowed": !barred, "reason": reason, "restriction_level": level}

	case "full_lockdown":
		reason := "full lockdown -- no new access"
		logAccess("barred", reason)
		return map[string]interface{}{"allowed": false, "reason": reason, "restriction_level": level}

	case "emergency_only":
		if accessCategory == 2 {
			logAccess("allowed", "emergency access allowed during emergency_only")
			return map[string]interface{}{"allowed": true, "reason": "emergency access allowed", "restriction_level": level}
		}
		reason := "non-emergency barred during emergency_only"
		logAccess("barred", reason)
		return map[string]interface{}{"allowed": false, "reason": reason, "restriction_level": level}

	case "restricted":
		if accessCategory == 2 {
			logAccess("allowed", "emergency access always allowed")
			return map[string]interface{}{"allowed": true, "reason": "emergency access always allowed", "restriction_level": level}
		}
		if isPriorityUser(imsi) {
			logAccess("allowed", "priority subscriber allowed during restricted")
			return map[string]interface{}{"allowed": true, "reason": "priority subscriber allowed", "restriction_level": level}
		}
		reason := "non-priority barred during restricted mode"
		logAccess("barred", reason)
		return map[string]interface{}{"allowed": false, "reason": reason, "restriction_level": level}
	}

	reason := fmt.Sprintf("unknown restriction level: %s", level)
	logAccess("barred", reason)
	return map[string]interface{}{"allowed": false, "reason": reason, "restriction_level": level}
}

func isPriorityUser(imsi string) bool {
	db, err := engine.Open()
	if err != nil { return false }
	var minARP int
	err = db.QueryRow("SELECT MIN(arp_priority) FROM ue_slice_dnn WHERE imsi=?", imsi).Scan(&minARP)
	return err == nil && minARP <= 5
}

// ---- Access Log & Stats ----

// GetAccessLog returns recent access log entries.
func GetAccessLog(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 { limit = 100 }
	return qRows("SELECT * FROM racs_access_log ORDER BY id DESC LIMIT ?", limit)
}

// GetAccessStats returns aggregate access statistics.
func GetAccessStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil { return map[string]interface{}{} }
	var total, allowed, barred int
	_ = db.QueryRow("SELECT COUNT(*) FROM racs_access_log").Scan(&total)
	_ = db.QueryRow("SELECT COUNT(*) FROM racs_access_log WHERE decision='allowed'").Scan(&allowed)
	_ = db.QueryRow("SELECT COUNT(*) FROM racs_access_log WHERE decision='barred'").Scan(&barred)
	return map[string]interface{}{"total": total, "allowed": allowed, "barred": barred}
}

// ---- GUI panel API ----

func List() ([]map[string]any, error) { return GetBarringConfigs() }

func Status() map[string]any {
	cfg := GetRestrictionStatus()
	stats := GetAccessStats()
	for k, v := range stats { cfg[k] = v }
	return cfg
}

// helpers
func qRow(q string, args ...interface{}) (map[string]interface{}, error) {
	db, err := engine.Open(); if err != nil { return nil, err }
	rows, err := db.Query(q, args...); if err != nil { return nil, nil }; defer rows.Close()
	cols, _ := rows.Columns(); if !rows.Next() { return nil, nil }
	vals := make([]interface{}, len(cols)); ptrs := make([]interface{}, len(cols))
	for i := range vals { ptrs[i] = &vals[i] }; rows.Scan(ptrs...)
	m := make(map[string]interface{}, len(cols)); for i, c := range cols { m[c] = vals[i] }; return m, nil
}

func qRows(q string, args ...interface{}) ([]map[string]interface{}, error) {
	db, err := engine.Open(); if err != nil { return nil, err }
	rows, err := db.Query(q, args...); if err != nil { return nil, nil }; defer rows.Close()
	cols, _ := rows.Columns(); var out []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols)); ptrs := make([]interface{}, len(cols))
		for i := range vals { ptrs[i] = &vals[i] }; rows.Scan(ptrs...)
		m := make(map[string]interface{}, len(cols)); for i, c := range cols { m[c] = vals[i] }; out = append(out, m)
	}; return out, nil
}

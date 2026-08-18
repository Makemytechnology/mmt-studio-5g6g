// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package access — Access-restriction operator surface.
//
// Owns the operator-side state for the AMF's two pre-authentication
// gates: Forbidden TAI / Forbidden PLMN lookups (TS 24.501 §5.3.13
// and §5.3.13A) and Unified Access Control barring of access
// categories (TS 24.501 §4.5). The AMF queries CheckAccess() during
// Initial Registration; everything else is operator-panel CRUD plus
// an audit log so a regulator can ask "why was IMSI X refused on
// 2026-04-30 at 14:32?" and get a single SQL row.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 24.501 §4.5         Unified access control — overall framework
//                            (NAS-side determination of access category
//                            + access identities for a registration).
//   - TS 24.501 §5.3.13      Lists of 5GS forbidden tracking areas —
//                            authoritative source for the TAI gate.
//   - TS 24.501 §5.3.13A     Forbidden PLMN lists — authoritative
//                            source for the PLMN gate.
//   - TS 24.501 §5.3.20      Specific requirements for UE when
//                            receiving non-integrity-protected reject
//                            messages — relevant to the cause codes
//                            the AMF inserts after a deny here.
//   - TS 22.261 §6.31.2.1    General — a Disaster Condition can
//                            override these gates for inbound roamers
//                            (cross-references safety/disaster_roaming).
//
// Deferred (TODO at unimplemented surfaces):
//
//   - TS 24.501 §4.5.2       Determination of access identities and
//                            access category for normal access — UE-
//                            side state machine; we accept whichever
//                            access_category the UE asserts and only
//                            check operator barring against it.
//   - TS 24.501 §4.5.2A      Same for Disaster-Roaming-related access.
//   - TODO(spec: TS 22.011)  Service accessibility — Access Class
//                            Barring legacy semantics. Not loaded
//                            locally; UAC §4.5 supersedes this in 5GS.
//   - TODO(spec: TS 23.122)  NAS functions related to MS in idle
//                            mode — UE-side PLMN selection responses
//                            to the rejects we emit. Out of scope.
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/safety_access.py.
package access

import (
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Decision strings written into access_decision_log.decision and
// returned by CheckAccess.
const (
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// CheckResult is the structured outcome of an access check.
type CheckResult struct {
	Allowed  bool   `json:"allowed"`
	Reason   string `json:"reason"`   // human-readable; logged
	CauseRef string `json:"cause_ref"` // §clause that justifies the deny
}

// ─── Forbidden TAI list (TS 24.501 §5.3.13) ──────────────────────

// AddForbiddenTAI inserts (or replaces) a (plmn_id, tac) entry the
// AMF must refuse Initial-Registration from.
func AddForbiddenTAI(plmnID, tac, reason, addedBy string) error {
	if plmnID == "" || tac == "" {
		return errors.New("plmn_id and tac are required")
	}
	if addedBy == "" {
		addedBy = "operator"
	}
	_, err := engine.Exec(`INSERT INTO access_forbidden_tai
		(plmn_id, tac, reason, added_by) VALUES (?,?,?,?)
		ON CONFLICT(plmn_id, tac) DO UPDATE SET
		  reason=excluded.reason, added_by=excluded.added_by,
		  added_at=datetime('now')`,
		plmnID, tac, reason, addedBy)
	return err
}

// RemoveForbiddenTAI deletes one (plmn_id, tac) entry.
func RemoveForbiddenTAI(plmnID, tac string) error {
	_, err := engine.Exec(
		"DELETE FROM access_forbidden_tai WHERE plmn_id=? AND tac=?",
		plmnID, tac)
	return err
}

// ListForbiddenTAIs returns every TAI on the deny-list.
func ListForbiddenTAIs() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM access_forbidden_tai ORDER BY plmn_id, tac")
}

// IsForbiddenTAI reports whether (plmn_id, tac) is on the list.
func IsForbiddenTAI(plmnID, tac string) bool {
	db, err := engine.Open()
	if err != nil {
		return false
	}
	var n int
	_ = db.QueryRow(
		"SELECT COUNT(*) FROM access_forbidden_tai WHERE plmn_id=? AND tac=?",
		plmnID, tac).Scan(&n)
	return n > 0
}

// ─── Forbidden PLMN list (TS 24.501 §5.3.13A) ────────────────────

// AddForbiddenPLMN puts a PLMN on the operator deny-list.
func AddForbiddenPLMN(plmnID, reason, addedBy string) error {
	if plmnID == "" {
		return errors.New("plmn_id is required")
	}
	if addedBy == "" {
		addedBy = "operator"
	}
	_, err := engine.Exec(`INSERT INTO access_forbidden_plmn
		(plmn_id, reason, added_by) VALUES (?,?,?)
		ON CONFLICT(plmn_id) DO UPDATE SET
		  reason=excluded.reason, added_by=excluded.added_by,
		  added_at=datetime('now')`,
		plmnID, reason, addedBy)
	return err
}

// RemoveForbiddenPLMN takes a PLMN off the deny-list.
func RemoveForbiddenPLMN(plmnID string) error {
	_, err := engine.Exec("DELETE FROM access_forbidden_plmn WHERE plmn_id=?", plmnID)
	return err
}

// ListForbiddenPLMNs returns every PLMN on the deny-list.
func ListForbiddenPLMNs() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM access_forbidden_plmn ORDER BY plmn_id")
}

// IsForbiddenPLMN reports whether plmnID is on the list.
func IsForbiddenPLMN(plmnID string) bool {
	db, err := engine.Open()
	if err != nil {
		return false
	}
	var n int
	_ = db.QueryRow(
		"SELECT COUNT(*) FROM access_forbidden_plmn WHERE plmn_id=?",
		plmnID).Scan(&n)
	return n > 0
}

// ─── Unified Access Control barring (TS 24.501 §4.5) ─────────────

// SetUACBarring upserts a barring rule for one access category.
// barringFactor is in [0.0, 1.0]; barringTime is the back-off seconds.
func SetUACBarring(category int, barringFactor float64, barringTime int, enabled bool) error {
	if category < 0 || category > 63 {
		return errors.New("access_category must be in [0,63] (TS 24.501 §4.5)")
	}
	if barringFactor < 0 || barringFactor > 1 {
		return errors.New("barring_factor must be in [0.0, 1.0]")
	}
	en := 0
	if enabled {
		en = 1
	}
	_, err := engine.Exec(`INSERT INTO access_uac_barring
		(access_category, barring_factor, barring_time_s, enabled, updated_at)
		VALUES (?,?,?,?, datetime('now'))
		ON CONFLICT(access_category) DO UPDATE SET
		  barring_factor=excluded.barring_factor,
		  barring_time_s=excluded.barring_time_s,
		  enabled=excluded.enabled,
		  updated_at=datetime('now')`,
		category, barringFactor, barringTime, en)
	return err
}

// RemoveUACBarring drops one access-category rule.
func RemoveUACBarring(category int) error {
	_, err := engine.Exec("DELETE FROM access_uac_barring WHERE access_category=?", category)
	return err
}

// ListUACBarring returns all configured barring rules.
func ListUACBarring() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM access_uac_barring ORDER BY access_category")
}

// EvaluateUACBarring runs the §4.5 random-bar gate for one
// (category, attempt) pair. Returns (barred, backoffSeconds).
//
// TS 24.501 §4.5 leaves the actual "draw a uniform random number
// and compare against barring_factor" logic to the UE; the network
// only configures the factor + time. We model the same draw here
// for the AMF-side admission probe so an operator can predict the
// actual blocked rate at a given factor.
func EvaluateUACBarring(category int) (barred bool, backoffSec int) {
	db, err := engine.Open()
	if err != nil {
		return false, 0
	}
	var factor float64
	var t int
	var enabled int
	err = db.QueryRow(
		`SELECT barring_factor, barring_time_s, enabled FROM access_uac_barring
		 WHERE access_category=?`,
		category).Scan(&factor, &t, &enabled)
	if err != nil || enabled == 0 {
		return false, 0
	}
	if factor >= 1.0 {
		return true, t
	}
	if factor <= 0.0 {
		return false, 0
	}
	// rand.Float64() is in [0, 1). Bar iff draw < factor.
	if rand.Float64() < factor {
		return true, t
	}
	return false, 0
}

// ─── Composite admission gate ────────────────────────────────────

// CheckAccess composes Forbidden-PLMN, Forbidden-TAI, and (if
// `category` is non-negative) UAC barring. The first failure short-
// circuits with a §-cited cause; otherwise allow.
//
// Empty `tac` skips the TAI check (the UE may not yet have a TAC
// at the very start of registration). Negative `category` skips the
// UAC gate (callers without category info).
func CheckAccess(imsi, plmnID, tac string, category int) CheckResult {
	res := check(imsi, plmnID, tac, category)
	logDecision(imsi, plmnID, tac, res)
	return res
}

func check(imsi, plmnID, tac string, category int) CheckResult {
	if plmnID != "" && IsForbiddenPLMN(plmnID) {
		return CheckResult{
			Allowed:  false,
			Reason:   "PLMN " + plmnID + " is on operator forbidden list",
			CauseRef: "TS 24.501 §5.3.13A",
		}
	}
	if plmnID != "" && tac != "" && IsForbiddenTAI(plmnID, tac) {
		return CheckResult{
			Allowed:  false,
			Reason:   "TAI " + plmnID + "/" + tac + " is on operator forbidden list",
			CauseRef: "TS 24.501 §5.3.13",
		}
	}
	if category >= 0 {
		if barred, backoff := EvaluateUACBarring(category); barred {
			return CheckResult{
				Allowed:  false,
				Reason:   uacReason(category, backoff),
				CauseRef: "TS 24.501 §4.5",
			}
		}
	}
	return CheckResult{Allowed: true, Reason: "no operator gate matched"}
}

func uacReason(cat, backoff int) string {
	if backoff > 0 {
		return joinInt("UAC barred access_category=", cat) +
			joinInt(" (back-off ", backoff) + "s)"
	}
	return joinInt("UAC barred access_category=", cat)
}

func joinInt(prefix string, v int) string {
	return prefix + intToStr(v)
}

func intToStr(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ─── Decision audit log ──────────────────────────────────────────

func logDecision(imsi, plmnID, tac string, r CheckResult) {
	dec := DecisionAllow
	if !r.Allowed {
		dec = DecisionDeny
	}
	reason := r.Reason
	if r.CauseRef != "" {
		reason = reason + " (" + r.CauseRef + ")"
	}
	_, _ = engine.Exec(
		`INSERT INTO access_decision_log (imsi, plmn_id, tac, decision, reason)
		 VALUES (?,?,?,?,?)`,
		imsi, plmnID, tac, dec, reason)
}

// GetDecisionLog returns recent admission decisions (newest first).
func GetDecisionLog(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	return qRows("SELECT * FROM access_decision_log ORDER BY id DESC LIMIT ?", limit)
}

// ─── Stats ───────────────────────────────────────────────────────

// GetStats returns coarse counters for the operator dashboard.
func GetStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{}
	}
	var fbdTAI, fbdPLMN, uacRules, allow, deny int
	_ = db.QueryRow("SELECT COUNT(*) FROM access_forbidden_tai").Scan(&fbdTAI)
	_ = db.QueryRow("SELECT COUNT(*) FROM access_forbidden_plmn").Scan(&fbdPLMN)
	_ = db.QueryRow("SELECT COUNT(*) FROM access_uac_barring WHERE enabled=1").Scan(&uacRules)
	_ = db.QueryRow("SELECT COUNT(*) FROM access_decision_log WHERE decision='allow'").Scan(&allow)
	_ = db.QueryRow("SELECT COUNT(*) FROM access_decision_log WHERE decision='deny'").Scan(&deny)
	return map[string]interface{}{
		"forbidden_tai_count":  fbdTAI,
		"forbidden_plmn_count": fbdPLMN,
		"uac_rules_enabled":    uacRules,
		"decisions_allow":      allow,
		"decisions_deny":       deny,
	}
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]map[string]any, error) { return GetDecisionLog(100) }
func Status() map[string]any          { return GetStats() }

// ─── helpers ─────────────────────────────────────────────────────

func init() {
	rand.Seed(time.Now().UnixNano())
	_ = strings.TrimSpace
	_ = logger.Get // keep import live for future audit hooks
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

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// api.go — operator-side helpers for the NWDAF exposure surface:
// consumer / subscription updates, API-key rotation, audit-log
// filters, the TS 23.288 §6.2.9 user-consent gate, and the
// permission-probe used by `POST /api/nwdaf/exposure/check-permission`.
//
// Spec anchors:
//
//   - TS 23.288 §6.1.1.2 — Subscribe / Unsubscribe by AFs via NEF.
//                          Modify is implicit via re-subscribe in the
//                          spec; the PATCH below is the operational
//                          equivalent the panel expects.
//   - TS 23.288 §6.1.2.2 — Analytics request by AFs (one-shot).
//   - TS 23.288 §6.2.9   — User consent (per UE / SUPI). When a
//                          subscription targets a UE, the NEF must
//                          gate on a consent record from the UDM /
//                          UDR side. ConsentAllowed enforces the
//                          policy; the schema lets the operator
//                          maintain the allow/deny list.
//   - TS 29.522 §4.4     — Nnef_AnalyticsExposure — the JSON
//                          envelopes carried by these helpers map
//                          to the Stage-3 shape.
package exposure

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ── Consumer update / key rotation ──────────────────────────────

// UpdateConsumer applies a sparse update to a consumer row. Allowed
// fields: callback_url, allowed_analytics, active, name. Returns the
// updated row, or nil if the consumer was not found.
func UpdateConsumer(consumerID int64,
	patch map[string]any) (map[string]any, error) {

	allowed := map[string]bool{
		"callback_url":      true,
		"allowed_analytics": true,
		"active":            true,
		"name":              true,
	}
	cols := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		// allowed_analytics may arrive as []string or a JSON string.
		if k == "allowed_analytics" {
			switch t := v.(type) {
			case []string:
				v = strings.Join(t, ",")
			case []any:
				strs := make([]string, 0, len(t))
				for _, x := range t {
					if s, ok := x.(string); ok {
						strs = append(strs, s)
					}
				}
				v = strings.Join(strs, ",")
			}
		}
		cols = append(cols, k+"=?")
		args = append(args, v)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no allowed fields in patch")
	}
	args = append(args, consumerID)
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := "UPDATE nwdaf_exposure_consumers SET " +
		strings.Join(cols, ", ") + " WHERE id=?"
	res, err := db.Exec(q, args...)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	return GetConsumer(consumerID)
}

// RotateAPIKey replaces the API key on a consumer row, returning the
// new key. Old key is irrecoverable — callers should record the
// rotation via LogQuery so the audit trail tracks the swap.
//
// Returns ("", nil, nil) when the consumer is not found so the route
// layer can map it to 404.
func RotateAPIKey(consumerID int64) (string, map[string]any, error) {
	cur, err := GetConsumer(consumerID)
	if err != nil {
		return "", nil, err
	}
	if cur == nil {
		return "", nil, nil
	}
	newKey := GenerateAPIKey()
	db, err := engine.Open()
	if err != nil {
		return "", nil, err
	}
	if _, err := db.Exec(
		`UPDATE nwdaf_exposure_consumers SET api_key=? WHERE id=?`,
		newKey, consumerID,
	); err != nil {
		return "", nil, err
	}
	row, _ := GetConsumer(consumerID)
	return newKey, row, nil
}

// ── Subscription update ─────────────────────────────────────────

// UpdateSubscription applies a sparse update to an exposure
// subscription. Allowed: target_type, target_id, interval_s,
// callback_url, active. Returns the updated row, or nil if the
// subscription was not found.
func UpdateSubscription(subID int64,
	patch map[string]any) (map[string]any, error) {

	allowed := map[string]bool{
		"target_type":  true,
		"target_id":    true,
		"interval_s":   true,
		"callback_url": true,
		"active":       true,
	}
	cols := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		// target_type CHECK constraint enforced by DB schema.
		// Mirrors TS 23.288 §6.2.2.2 targetOfAnalyticsReporting.
		if k == "target_type" {
			s, _ := v.(string)
			switch s {
			case "imsi", "slice", "network", "nf", "nf_set", "area":
			default:
				return nil, fmt.Errorf(
					"target_type must be imsi|slice|network|nf|nf_set|area")
			}
		}
		cols = append(cols, k+"=?")
		args = append(args, v)
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("no allowed fields in patch")
	}
	args = append(args, subID)
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := "UPDATE nwdaf_exposure_subscriptions SET " +
		strings.Join(cols, ", ") + " WHERE id=?"
	res, err := db.Exec(q, args...)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	return GetSubscription(subID)
}

// ── Audit log filtering ─────────────────────────────────────────

// LogFilter scopes a GetLog call.
type LogFilter struct {
	ConsumerID    *int64
	AnalyticsType string
	QueryType     string // "subscription" | "one_shot"
	Since         string // ISO datetime
	Limit         int
}

// GetLogFiltered returns audit log rows filtered by the supplied
// LogFilter. Empty / nil fields are not applied. Limit ≤ 0 → 50.
func GetLogFiltered(f LogFilter) ([]map[string]any, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT l.*, c.name AS consumer_name
		FROM nwdaf_exposure_log l
		LEFT JOIN nwdaf_exposure_consumers c ON c.id = l.consumer_id
		WHERE 1=1`
	args := []any{}
	if f.ConsumerID != nil {
		q += ` AND l.consumer_id=?`
		args = append(args, *f.ConsumerID)
	}
	if f.AnalyticsType != "" {
		q += ` AND l.analytics_type=?`
		args = append(args, f.AnalyticsType)
	}
	if f.QueryType != "" {
		q += ` AND l.query_type=?`
		args = append(args, f.QueryType)
	}
	if f.Since != "" {
		q += ` AND l.created_at >= ?`
		args = append(args, f.Since)
	}
	q += ` ORDER BY l.id DESC LIMIT ?`
	args = append(args, limit)
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// ── User consent (TS 23.288 §6.2.9) ────────────────────────────

// ConsentMode controls how the gate behaves when no row exists for
// a (consumer, supi) pair. Per TS 23.288 §6.2.9 the operator may
// configure the policy as opt-in (default-deny) or opt-out
// (default-allow). The mode is stored in `nwdaf_consent_policy`.
const (
	ConsentModeOptIn  = "opt_in"
	ConsentModeOptOut = "opt_out"
)

// SetConsent records a consent decision for (consumer_id, supi).
// `allow=true` records consent; `allow=false` records denial.
// Both end up in `nwdaf_user_consent` so the gate has an explicit
// row regardless of the global policy.
func SetConsent(consumerID int64, supi string, allow bool, reason string) error {
	if supi == "" {
		return fmt.Errorf("supi required")
	}
	db, err := engine.Open()
	if err != nil {
		return err
	}
	allowInt := 0
	if allow {
		allowInt = 1
	}
	_, err = db.Exec(`INSERT INTO nwdaf_user_consent
		(consumer_id, supi, allow, reason, recorded_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT(consumer_id, supi) DO UPDATE SET
			allow=excluded.allow,
			reason=excluded.reason,
			recorded_at=datetime('now')`,
		consumerID, supi, allowInt, reason)
	return err
}

// GetConsent returns the recorded decision (`true`/`false`) and
// `exists=true` when a row is present, otherwise `false, false`.
func GetConsent(consumerID int64, supi string) (allow bool, exists bool, err error) {
	if supi == "" {
		return false, false, fmt.Errorf("supi required")
	}
	db, err := engine.Open()
	if err != nil {
		return false, false, err
	}
	var allowInt int
	err = db.QueryRow(`SELECT allow FROM nwdaf_user_consent
		WHERE consumer_id=? AND supi=?`, consumerID, supi).Scan(&allowInt)
	if err != nil {
		// sql.ErrNoRows → no consent on file
		return false, false, nil
	}
	return allowInt == 1, true, nil
}

// ListConsent returns recorded consent rows, optionally filtered by
// consumer_id (pass 0 to disable that filter).
func ListConsent(consumerID int64, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT * FROM nwdaf_user_consent`
	args := []any{}
	if consumerID > 0 {
		q += ` WHERE consumer_id=?`
		args = append(args, consumerID)
	}
	q += ` ORDER BY recorded_at DESC LIMIT ?`
	args = append(args, limit)
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// GetConsentMode reads the operator-configured default policy. When
// no row exists, returns "opt_in" (default-deny — the safer choice
// per TS 23.288 §6.2.9).
func GetConsentMode() string {
	db, err := engine.Open()
	if err != nil {
		return ConsentModeOptIn
	}
	var mode string
	err = db.QueryRow(`SELECT mode FROM nwdaf_consent_policy
		WHERE id=1`).Scan(&mode)
	if err != nil || (mode != ConsentModeOptIn && mode != ConsentModeOptOut) {
		return ConsentModeOptIn
	}
	return mode
}

// SetConsentMode updates the global policy.
func SetConsentMode(mode string) error {
	if mode != ConsentModeOptIn && mode != ConsentModeOptOut {
		return fmt.Errorf("mode must be %q or %q",
			ConsentModeOptIn, ConsentModeOptOut)
	}
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO nwdaf_consent_policy (id, mode, updated_at)
		VALUES (1, ?, datetime('now'))
		ON CONFLICT(id) DO UPDATE SET mode=excluded.mode,
			updated_at=datetime('now')`, mode)
	return err
}

// ConsentAllowed evaluates whether the consumer may receive analytics
// scoped to `supi`. Returns the decision plus a reason string the
// caller can pass to LogQuery.
//
//   - opt_in mode  + no row             → DENY
//   - opt_in mode  + row(allow=1)       → ALLOW
//   - opt_in mode  + row(allow=0)       → DENY
//   - opt_out mode + no row             → ALLOW
//   - opt_out mode + row(allow=0)       → DENY
//   - opt_out mode + row(allow=1)       → ALLOW
func ConsentAllowed(consumerID int64, supi string) (bool, string) {
	allow, exists, err := GetConsent(consumerID, supi)
	if err != nil {
		return false, "consent lookup failed: " + err.Error()
	}
	mode := GetConsentMode()
	switch mode {
	case ConsentModeOptIn:
		if !exists {
			return false, "no consent on file (opt_in mode)"
		}
		if !allow {
			return false, "consent explicitly denied"
		}
		return true, "consent granted"
	case ConsentModeOptOut:
		if !exists {
			return true, "no consent record (opt_out mode default-allow)"
		}
		if !allow {
			return false, "consent explicitly denied"
		}
		return true, "consent granted"
	}
	return false, "unknown consent mode"
}

// ── Permission probe ────────────────────────────────────────────

// CheckPermission is the dry-run access check the panel /
// consumer-side test harness uses without firing a real analytics
// query (and without bumping the audit log's response_code from
// 200). Returns the same allowed/reason pair the gate would emit at
// query time.
func CheckPermission(apiKey, exposureType string, supi string) map[string]any {
	out := map[string]any{
		"allowed": false,
	}
	if apiKey == "" {
		out["reason"] = "api_key required"
		return out
	}
	c, err := ValidateAPIKey(apiKey)
	if err != nil {
		out["reason"] = "internal error: " + err.Error()
		return out
	}
	if c == nil {
		out["reason"] = "invalid api_key"
		return out
	}
	internalID, ok := ExposureTypes[exposureType]
	if !ok {
		out["reason"] = "unknown exposure_type"
		return out
	}
	out["consumer_id"] = c["id"]
	out["consumer_name"] = c["name"]
	out["analytics_id"] = internalID
	if !CheckAnalyticsPermission(c, internalID) {
		out["reason"] = "consumer not authorised for this analytics type"
		return out
	}
	if supi != "" {
		cid, _ := c["id"].(int64)
		ok, reason := ConsentAllowed(cid, supi)
		if !ok {
			out["reason"] = reason
			return out
		}
		out["consent"] = reason
	}
	out["allowed"] = true
	out["reason"] = "ok"
	return out
}

// ── helpers ─────────────────────────────────────────────────────

// MarshalConsumerAllowed normalises the `allowed_analytics` column
// (CSV string in DB) into a JSON array for the response payload.
func MarshalConsumerAllowed(row map[string]any) {
	v, ok := row["allowed_analytics"].(string)
	if !ok || v == "" {
		row["allowed_analytics"] = []string{}
		return
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	row["allowed_analytics"] = out
}

// _ unused but keeps the json + time imports honest in case future
// helpers serialize timestamps.
var _ = func() (any, any) { return json.RawMessage(nil), time.Time{} }

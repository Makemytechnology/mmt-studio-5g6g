// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package wifi_offload — operator-facing surface for non-3GPP
// (WLAN) access via N3IWF / TNGF.
//
// This is the operator dashboard / admission probe; the actual
// IKEv2 + EAP-5G + ESP datapath lives in nf/n3iwf/. Here we own:
//
//   - per-DNN access policy: trusted vs untrusted, offload
//     preference (5G-first / WLAN-first / 5G-only / WLAN-only /
//     ATSSS).
//   - the in-flight table of WLAN-attached UEs (one row per
//     (IMSI, access_type)).
//   - the attach / detach / rejected audit log.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.501 §4.2.7        Reference points (incl. N3 / Y1 / Y2
//                             for non-3GPP access).
//   - TS 23.501 §4.2.8        Support of non-3GPP access — umbrella
//                             clause covering trusted + untrusted
//                             WLAN paths into 5GC.
//   - TS 23.501 §4.2.8.5      Access to 5GC from devices that do
//                             not support 5GC NAS over WLAN access
//                             (the TWIF path; TODO below).
//   - TS 23.501 §5.10.2       Security Model for non-3GPP access
//                             (EAP-5G + IKEv2 + IPsec ESP).
//   - TS 23.501 §6.2.9        N3IWF — functional description for
//                             the untrusted non-3GPP gateway.
//   - TS 23.501 §6.2.9A       TNGF — Trusted Non-3GPP Gateway
//                             Function (the trusted-WLAN path).
//
// Deferred (TODO at unimplemented surfaces):
//
//   - TS 23.501 §4.2.8.5      TWIF — Trusted WLAN Interworking
//                             Function for legacy non-NAS UEs
//                             (smart-home appliances, IoT WLAN
//                             cameras). Today access_type='wireline'
//                             is accepted in the schema but no
//                             gateway implementation exists.
//   - TODO(spec: TS 23.501)   ATSSS — Access Traffic Steering /
//                             Switching / Splitting (multi-access
//                             PDU sessions). access_type 'atsss'
//                             is a policy *preference* here; the
//                             actual MA-PDU split is the SMF/UPF's
//                             call.
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/access_mobility.py.
package wifi_offload

import (
	"errors"
	"fmt"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// AccessType vocabulary — matches the schema CHECK on
// wifi_access_policy.access_type and wifi_attached_ues.access_type.
const (
	AccessUntrusted = "untrusted" // via N3IWF (TS 23.501 §6.2.9)
	AccessTrusted   = "trusted"   // via TNGF (TS 23.501 §6.2.9A)
	AccessWireline  = "wireline"  // via W-AGF (TODO §4.2.8.5)
)

// OffloadPreference vocabulary — matches the schema CHECK on
// wifi_access_policy.offload_pref.
const (
	Pref5GFirst   = "5g_first"
	PrefWLANFirst = "wlan_first"
	Pref5GOnly    = "5g_only"
	PrefWLANOnly  = "wlan_only"
	PrefATSSS     = "atsss"
)

// Audit-log actions — matches schema CHECK on wifi_offload_log.action.
const (
	ActionAttached = "attached"
	ActionDetached = "detached"
	ActionRejected = "rejected"
)

var validAccess = map[string]bool{
	AccessUntrusted: true, AccessTrusted: true, AccessWireline: true,
}

var validPrefs = map[string]bool{
	Pref5GFirst: true, PrefWLANFirst: true, Pref5GOnly: true,
	PrefWLANOnly: true, PrefATSSS: true,
}

// ─── Per-DNN Access Policy (TS 23.501 §4.2.8) ────────────────────

// SetPolicy UPSERTs the access policy for one DNN. The tuple
// (access_type, offload_pref, enabled) drives the AMF's decision
// when a UE proposes an IKEv2 SA carrying this DNN.
func SetPolicy(dnn, accessType, offloadPref string, enabled bool) error {
	if dnn == "" {
		return errors.New("dnn is required")
	}
	if !validAccess[accessType] {
		return fmt.Errorf("invalid access_type: %q", accessType)
	}
	if !validPrefs[offloadPref] {
		return fmt.Errorf("invalid offload_pref: %q", offloadPref)
	}
	en := 0
	if enabled {
		en = 1
	}
	_, err := engine.Exec(`INSERT INTO wifi_access_policy
		(dnn, access_type, offload_pref, enabled, updated_at)
		VALUES (?,?,?,?, datetime('now'))
		ON CONFLICT(dnn) DO UPDATE SET
		  access_type=excluded.access_type,
		  offload_pref=excluded.offload_pref,
		  enabled=excluded.enabled,
		  updated_at=datetime('now')`,
		dnn, accessType, offloadPref, en)
	return err
}

// GetPolicy returns the policy row for one DNN, or nil if unset.
func GetPolicy(dnn string) (map[string]interface{}, error) {
	return qRow("SELECT * FROM wifi_access_policy WHERE dnn=?", dnn)
}

// ListPolicies returns every configured DNN policy.
func ListPolicies() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM wifi_access_policy ORDER BY dnn")
}

// DeletePolicy removes one DNN entry. Removing a policy means the
// DNN falls back to the default (5G-first via untrusted N3IWF).
func DeletePolicy(dnn string) error {
	_, err := engine.Exec("DELETE FROM wifi_access_policy WHERE dnn=?", dnn)
	return err
}

// ─── Attached UE table (in-flight) ───────────────────────────────

// AttachUE registers a UE as attached via WLAN access. UPSERT-keyed
// on (imsi, access_type) — re-attach updates IPs in place.
func AttachUE(imsi, accessType, n3iwfID, innerIP, outerIP string) error {
	if imsi == "" {
		return errors.New("imsi is required")
	}
	if !validAccess[accessType] {
		return fmt.Errorf("invalid access_type: %q", accessType)
	}
	_, err := engine.Exec(`INSERT INTO wifi_attached_ues
		(imsi, access_type, n3iwf_id, inner_ip, outer_ip, attached_at)
		VALUES (?,?,?,?,?, datetime('now'))
		ON CONFLICT(imsi, access_type) DO UPDATE SET
		  n3iwf_id=excluded.n3iwf_id,
		  inner_ip=excluded.inner_ip,
		  outer_ip=excluded.outer_ip,
		  attached_at=datetime('now')`,
		imsi, accessType, n3iwfID, innerIP, outerIP)
	if err == nil {
		logAction(imsi, accessType, ActionAttached, "via N3IWF")
		logger.Get("wifi_offload").Infof(
			"WLAN UE attached: IMSI=%s access=%s inner=%s",
			imsi, accessType, innerIP)
	}
	return err
}

// DetachUE removes a UE from the attached table.
func DetachUE(imsi, accessType string) error {
	res, err := engine.Exec(
		"DELETE FROM wifi_attached_ues WHERE imsi=? AND access_type=?",
		imsi, accessType)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		logAction(imsi, accessType, ActionDetached, "")
	}
	return nil
}

// IsAttached reports whether (imsi, accessType) currently has a row.
func IsAttached(imsi, accessType string) bool {
	db, err := engine.Open()
	if err != nil {
		return false
	}
	var n int
	_ = db.QueryRow(
		"SELECT COUNT(*) FROM wifi_attached_ues WHERE imsi=? AND access_type=?",
		imsi, accessType).Scan(&n)
	return n > 0
}

// ListAttachedUEs returns the in-flight attached UE table.
func ListAttachedUEs() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM wifi_attached_ues ORDER BY attached_at DESC")
}

// ─── Admission probe ─────────────────────────────────────────────

// AdmissionResult is the structured outcome of CheckOffload.
type AdmissionResult struct {
	Allowed     bool   `json:"allowed"`
	Reason      string `json:"reason"`
	AccessType  string `json:"access_type,omitempty"`
	OffloadPref string `json:"offload_pref,omitempty"`
}

// CheckOffload decides whether `(dnn, accessType)` is admissible.
// Exact rules:
//
//   - No policy row → default-allow as untrusted, 5g_first.
//   - Policy row with enabled=0 → deny.
//   - Policy row with offload_pref=5g_only → deny WLAN attempts.
//   - Policy row with offload_pref=wlan_only over a different
//     access_type than configured → deny.
//   - Otherwise → allow.
//
// Logs every refusal as 'rejected' in the audit log so the
// operator can spot a misconfigured DNN policy.
func CheckOffload(imsi, dnn, accessType string) AdmissionResult {
	if !validAccess[accessType] {
		return AdmissionResult{
			Allowed: false,
			Reason:  fmt.Sprintf("invalid access_type %q", accessType),
		}
	}
	pol, _ := GetPolicy(dnn)
	if pol == nil {
		// Default policy — allow only over untrusted (the safe
		// dev path through N3IWF).
		if accessType != AccessUntrusted {
			r := AdmissionResult{
				Allowed:     false,
				Reason:      "no policy for DNN; default refuses non-untrusted access",
				AccessType:  AccessUntrusted,
				OffloadPref: Pref5GFirst,
			}
			logAction(imsi, accessType, ActionRejected, r.Reason)
			return r
		}
		return AdmissionResult{
			Allowed:     true,
			Reason:      "default policy (no DNN row): untrusted N3IWF, 5g_first",
			AccessType:  AccessUntrusted,
			OffloadPref: Pref5GFirst,
		}
	}
	if intValue(pol["enabled"]) == 0 {
		r := AdmissionResult{
			Allowed: false,
			Reason:  fmt.Sprintf("policy for DNN %q is disabled", dnn),
		}
		logAction(imsi, accessType, ActionRejected, r.Reason)
		return r
	}
	pref := stringOf(pol["offload_pref"])
	if pref == Pref5GOnly {
		r := AdmissionResult{
			Allowed:     false,
			Reason:      "DNN policy is 5g_only; WLAN access not permitted",
			OffloadPref: pref,
		}
		logAction(imsi, accessType, ActionRejected, r.Reason)
		return r
	}
	if pref == PrefWLANOnly && stringOf(pol["access_type"]) != accessType {
		r := AdmissionResult{
			Allowed:     false,
			Reason:      "DNN policy is wlan_only and access_type does not match",
			OffloadPref: pref,
		}
		logAction(imsi, accessType, ActionRejected, r.Reason)
		return r
	}
	return AdmissionResult{
		Allowed:     true,
		Reason:      fmt.Sprintf("admitted under DNN %q policy", dnn),
		AccessType:  stringOf(pol["access_type"]),
		OffloadPref: pref,
	}
}

// ─── Audit log ───────────────────────────────────────────────────

func logAction(imsi, accessType, action, reason string) {
	_, _ = engine.Exec(
		`INSERT INTO wifi_offload_log (imsi, access_type, action, reason)
		 VALUES (?,?,?,?)`,
		imsi, accessType, action, reason)
}

// GetAuditLog returns the most recent audit rows (newest first).
func GetAuditLog(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	return qRows("SELECT * FROM wifi_offload_log ORDER BY id DESC LIMIT ?", limit)
}

// ─── Stats ───────────────────────────────────────────────────────

// GetStats returns counters for the operator dashboard.
func GetStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{}
	}
	var policies, attached, untrusted, trusted, attaches, detaches, rejects int
	_ = db.QueryRow("SELECT COUNT(*) FROM wifi_access_policy WHERE enabled=1").Scan(&policies)
	_ = db.QueryRow("SELECT COUNT(*) FROM wifi_attached_ues").Scan(&attached)
	_ = db.QueryRow("SELECT COUNT(*) FROM wifi_attached_ues WHERE access_type='untrusted'").Scan(&untrusted)
	_ = db.QueryRow("SELECT COUNT(*) FROM wifi_attached_ues WHERE access_type='trusted'").Scan(&trusted)
	_ = db.QueryRow("SELECT COUNT(*) FROM wifi_offload_log WHERE action='attached'").Scan(&attaches)
	_ = db.QueryRow("SELECT COUNT(*) FROM wifi_offload_log WHERE action='detached'").Scan(&detaches)
	_ = db.QueryRow("SELECT COUNT(*) FROM wifi_offload_log WHERE action='rejected'").Scan(&rejects)
	return map[string]interface{}{
		"enabled_policies":  policies,
		"attached_ues":      attached,
		"attached_untrusted": untrusted,
		"attached_trusted":  trusted,
		"total_attaches":    attaches,
		"total_detaches":    detaches,
		"total_rejected":    rejects,
	}
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]map[string]any, error) { return ListAttachedUEs() }
func Status() map[string]any          { return GetStats() }

// ─── helpers ─────────────────────────────────────────────────────

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

func stringOf(v interface{}) string {
	if v == nil {
		return ""
	}
	switch vv := v.(type) {
	case string:
		return vv
	case []byte:
		return string(vv)
	}
	return fmt.Sprintf("%v", v)
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

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// api.go — operator-API helpers for the PCF.
//
// Spec anchors:
//
//   - TS 23.503 §6.3 — PCC rule definition.
//   - TS 29.512 §5.6.2.6 — Type PccRule.
//   - TS 29.512 §5.6.3.8 — Enumeration RuleStatus (ACTIVE | INACTIVE).
//
// The rest of pcf.go owns the in-process N7 path (CreatePolicy,
// NotifySMFPolicyUpdate, …); these helpers only add panel-side
// readouts: total rule counts, per-IMSI listings, and a status
// snapshot for the dashboard.
package pcf

// PccRuleView is the panel-shaped projection of a PccRuleEntry.
// PccRuleEntry carries `time.Time` pointers and the implementation-
// internal `PccRuleState`; the view normalises both for the JSON
// surface.
type PccRuleView struct {
	RuleID      string `json:"rule_id"`
	IMSI        string `json:"imsi"`
	DNN         string `json:"dnn"`
	ServiceName string `json:"service_name"`
	Status      string `json:"status"`            // implementation-internal
	WireStatus  string `json:"wire_status"`       // TS 29.512 §5.6.3.8
	CreatedAt   string `json:"created_at,omitempty"`
	ActivatedAt string `json:"activated_at,omitempty"`
}

// ListPccRules returns rules from the in-memory manager, optionally
// filtered by IMSI and/or DNN. Empty filters → all rules.
func ListPccRules(imsi, dnn string) []PccRuleView {
	out := []PccRuleView{}
	managers := []*PccRuleManager{GlobalPccRuleManager, DefaultPccRuleManager}
	seen := map[string]bool{}
	for _, m := range managers {
		m.mu.Lock()
		for k, list := range m.rules {
			if imsi != "" && k.imsi != imsi {
				continue
			}
			if dnn != "" && k.dnn != dnn {
				continue
			}
			for _, r := range list {
				if seen[r.RuleID] {
					continue
				}
				seen[r.RuleID] = true
				v := PccRuleView{
					RuleID:      r.RuleID,
					IMSI:        r.IMSI,
					DNN:         r.DNN,
					ServiceName: r.ServiceName,
					Status:      string(r.Status),
					WireStatus:  string(r.Status.WireRuleStatus()),
				}
				if !r.CreatedAt.IsZero() {
					v.CreatedAt = r.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
				}
				if r.ActivatedAt != nil && !r.ActivatedAt.IsZero() {
					v.ActivatedAt = r.ActivatedAt.Format("2006-01-02T15:04:05Z07:00")
				}
				out = append(out, v)
			}
		}
		m.mu.Unlock()
	}
	return out
}

// PreviewPolicy builds — but does not persist — the PCC rule set the
// PCF would return for (IMSI, DNN, SST). Used by the panel to show
// "what would the SMF get" without provoking a real association.
func PreviewPolicy(imsi, dnn string, sst uint8) PCCRuleSet {
	return CreatePolicy(imsi, dnn, sst)
}

// Stats returns dashboard-shaped counts for the PCF panel.
func Stats() map[string]any {
	count := 0
	byStatus := map[string]int{}
	managers := []*PccRuleManager{GlobalPccRuleManager, DefaultPccRuleManager}
	for _, m := range managers {
		m.mu.Lock()
		for _, list := range m.rules {
			for _, r := range list {
				count++
				byStatus[string(r.Status.WireRuleStatus())]++
			}
		}
		m.mu.Unlock()
	}
	v2xAssocMu.RLock()
	v2x := len(v2xAssoc)
	v2xAssocMu.RUnlock()
	return map[string]any{
		"pcc_rules_total":  count,
		"by_status":        byStatus,
		"v2x_associations": v2x,
	}
}

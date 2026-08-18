// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// api.go — operator-API helpers for SM Policy Associations.
//
// Spec anchors:
//
//   - TS 29.512 §4.2.2 — Create
//   - TS 29.512 §4.2.4 — Update
//   - TS 29.512 §4.2.5 — Delete
//   - TS 29.512 §5.6.2.2 — SmPolicyContextData
//   - TS 29.512 §5.6.2.4 — SmPolicyDecision
//
// The existing smpolicy.go ships the SBI-shaped Create/Update/Delete
// service operations; these helpers add the panel-side read surface
// (list/get) that the in-process port previously kept private.
package smpolicy

import (
	"time"

	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
)

// AssociationView is the panel-friendly projection of one SM Policy
// Association. SmPolicyDecision carries non-JSON fields (time.Time
// without explicit marshalling, slices of pcf.PCCRule whose own
// shape is internal) — the view flattens to the attributes the
// operator panel actually displays.
type AssociationView struct {
	CtxRef            string    `json:"ctx_ref"`
	IMSI              string    `json:"imsi"`
	SUPI              string    `json:"supi"`
	PDUSessionID      uint8     `json:"pdu_session_id"`
	DNN               string    `json:"dnn"`
	SST               uint8     `json:"sst"`
	SD                string    `json:"sd,omitempty"`
	PDUSessionType    uint8     `json:"pdu_session_type"`
	DefaultQFI        uint8     `json:"default_qfi"`
	Default5QI        int       `json:"default_5qi"`
	SessionAMBRUL     int       `json:"session_ambr_ul_kbps"`
	SessionAMBRDL     int       `json:"session_ambr_dl_kbps"`
	ChargingMethod    string    `json:"charging_method"`
	RuleCount         int       `json:"rule_count"`
	RuleNames         []string  `json:"rule_names"`
	CreatedAt         time.Time `json:"created_at"`
	RevalidationTime  time.Time `json:"revalidation_time"`
	State             string    `json:"state"`
}

// ListAssociations returns every active SM Policy Association in
// PCF-internal order. Used by /api/pcf/sm-policy.
func ListAssociations() []AssociationView {
	assocMu.RLock()
	defer assocMu.RUnlock()
	out := make([]AssociationView, 0, len(assocs))
	for k, a := range assocs {
		out = append(out, viewOf(k, a))
	}
	return out
}

// GetAssociationView returns a panel-shaped projection of one
// association, or nil when not present (404).
func GetAssociationView(imsi string, pduSessionID uint8) *AssociationView {
	k := smfsm.Key{IMSI: stripSupiPrefix(imsi), PDUSessionID: pduSessionID}
	assocMu.RLock()
	a := assocs[k]
	assocMu.RUnlock()
	if a == nil {
		return nil
	}
	v := viewOf(k, a)
	return &v
}

func viewOf(k smfsm.Key, a *association) AssociationView {
	names := make([]string, 0, len(a.decision.PccRules))
	for _, r := range a.decision.PccRules {
		names = append(names, r.ServiceName)
	}
	state := ""
	if f := smfsm.Of(k); f != nil {
		state = f.State().String()
	}
	return AssociationView{
		CtxRef:           a.decision.SmPolicyCtxRef,
		IMSI:             a.key.IMSI,
		SUPI:             a.ctxData.SUPI,
		PDUSessionID:     a.key.PDUSessionID,
		DNN:              a.ctxData.DNN,
		SST:              a.ctxData.SST,
		SD:               a.ctxData.SD,
		PDUSessionType:   a.ctxData.PDUSessionType,
		DefaultQFI:       a.decision.DefaultQFI,
		Default5QI:       a.decision.Default5QI,
		SessionAMBRUL:    a.decision.SessionAMBRUL,
		SessionAMBRDL:    a.decision.SessionAMBRDL,
		ChargingMethod:   a.decision.ChargingMethod,
		RuleCount:        len(a.decision.PccRules),
		RuleNames:        names,
		CreatedAt:        a.createdAt,
		RevalidationTime: a.decision.RevalidationTime,
		State:            state,
	}
}

// Stats summarises the current association registry.
func Stats() map[string]any {
	assocMu.RLock()
	defer assocMu.RUnlock()
	byCharging := map[string]int{}
	for _, a := range assocs {
		byCharging[a.decision.ChargingMethod]++
	}
	return map[string]any{
		"associations":    len(assocs),
		"by_charging":     byCharging,
		"ctx_ref_counter": ctxRefCounter,
	}
}

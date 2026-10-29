// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// api.go — operator-API helpers for the eSIM panel + tester.
//
// Wraps the package's CRUD (esim.go), profile crypto/ICCID
// allocation (profile/), and SM-DP+ Mutual-Auth helpers (smdp/) in a
// thin, panel-friendly surface. The existing functions stay
// SBI-shaped; this layer adds the ergonomics the GUI needs:
//
//   - OrderProfile end-to-end (prepare + log + return JSON envelope),
//   - ReleaseProfile (state transition + audit notification),
//   - UpdateState (validated state machine step with audit log),
//   - Stats() (counters for the dashboard tiles).
//
// Spec anchors (GSMA SGP.* identifiers are informative — speccheck
// only matches TS/RFC, hence no §-cite):
//
//   - GSMA SGP.22 §3.0 / §5.6 — ES2+ DownloadOrder / ConfirmOrder
//                               (collapsed in OrderProfile).
//   - GSMA SGP.22 §3.5        — Handle Notification — used by the
//                               release/audit path.
//   - TS 31.102 §4.2          — USIM ADF EF contents (downstream
//                               consumer of the profile blob).
package esim

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/services/esim/smdp"
)

// ProfileOrder is the result of OrderProfile — the panel needs the
// ICCID + Activation Code in one shot so it can show the QR data
// without an additional read.
type ProfileOrder struct {
	ID             int64  `json:"id"`
	ICCID          string `json:"iccid"`
	IMSI           string `json:"imsi"`
	ActivationCode string `json:"activation_code"`
	MatchingID     string `json:"matching_id"`
	SMDPAddress    string `json:"smdp_address"`
	ProfileName    string `json:"profile_name"`
}

// OrderProfile runs the operator-side ES2+ DownloadOrder +
// ConfirmOrder sequence (GSMA SGP.22 §3.0/§5.6) end-to-end:
// allocate ICCID, build encrypted USIM profile blob, persist the
// `esim_profiles` row, and return the LPA-renderable Activation
// Code. Mirrors the panel's "Order new profile" form.
func OrderProfile(imsi, profileName, smdpAddress string) (*ProfileOrder, error) {
	if imsi == "" {
		return nil, fmt.Errorf("imsi required")
	}
	// Allow the panel to override the SM-DP+ address per-order; the
	// SM-DP+ server reads the address back out of the persisted row.
	if smdpAddress != "" {
		smdp.Server.SMDPAddress = smdpAddress
	}
	res := smdp.Server.PrepareProfile(imsi, profileName)
	if res == nil {
		return nil, fmt.Errorf("no auth data for imsi %s (subscriber must be provisioned first)", imsi)
	}
	iccid, _ := res["iccid"].(string)
	if iccid == "" {
		return nil, fmt.Errorf("PrepareProfile returned no ICCID")
	}
	// Read back the persisted row to surface the autoincrement id.
	row, err := GetProfileByICCID(iccid)
	if err != nil || row == nil {
		return nil, fmt.Errorf("profile row missing after prepare (iccid=%s)", iccid)
	}
	ac, _ := res["activation_code"].(string)
	mid, _ := res["matching_id"].(string)
	addr, _ := res["smdp_address"].(string)
	name, _ := res["profile_name"].(string)

	// Audit (SGP.22 §3.5 Handle Notification — operator-side log).
	_, _ = LogNotification(iccid, nil, "order", 0)

	return &ProfileOrder{
		ID:             row.ID,
		ICCID:          iccid,
		IMSI:           imsi,
		ActivationCode: ac,
		MatchingID:     mid,
		SMDPAddress:    addr,
		ProfileName:    name,
	}, nil
}

// ReleaseProfile retires an unused profile slot (`available` /
// `reserved` only). Real-life retires of `downloaded` /
// `installed` profiles need a SGP.22 §3.5 enable/disable cycle
// first — refusing here surfaces a clean 400 instead of leaving a
// stuck row behind.
func ReleaseProfile(iccid string) error {
	if iccid == "" {
		return fmt.Errorf("iccid required")
	}
	p, err := GetProfileByICCID(iccid)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("profile not found")
	}
	switch p.ProfileState {
	case "available", "reserved":
		// fine
	default:
		return fmt.Errorf("cannot release profile in state %q; disable / delete first", p.ProfileState)
	}
	if err := UpdateProfileState(p.ID, "deleted"); err != nil {
		return err
	}
	_, _ = LogNotification(iccid, nil, "release", 0)
	return nil
}

// SetProfileState applies a state transition with validation. Used
// by the panel to trigger enable / disable / delete from the UI.
// Only legal transitions per the SGP.22 lifecycle (mirrored in
// UpdateProfileState's validStates) are accepted.
func SetProfileState(iccid, newState string) error {
	p, err := GetProfileByICCID(iccid)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("profile not found")
	}
	if !validTransition(p.ProfileState, newState) {
		return fmt.Errorf("illegal transition %s → %s", p.ProfileState, newState)
	}
	if err := UpdateProfileState(p.ID, newState); err != nil {
		return err
	}
	_, _ = LogNotification(iccid, nil, newState, 0)
	return nil
}

// validTransition enforces the SGP.22 profile state machine
// transitions the local UpdateProfileState validator doesn't check.
// (UpdateProfileState validates only that the target state is in
// the vocabulary, not that the path makes sense.)
func validTransition(from, to string) bool {
	if from == to {
		return true
	}
	allowed := map[string][]string{
		"available":  {"reserved", "deleted"},
		"reserved":   {"downloaded", "available", "deleted"},
		"downloaded": {"installed", "deleted"},
		"installed":  {"enabled", "disabled", "deleted"},
		"enabled":    {"disabled", "deleted"},
		"disabled":   {"enabled", "deleted"},
		"deleted":    {},
	}
	for _, t := range allowed[from] {
		if t == to {
			return true
		}
	}
	return false
}

// Stats returns a panel-shaped counter set: total profiles, per-
// state breakdown, eUICCs registered. Dashboard tiles consume this
// directly.
func Stats() map[string]any {
	profiles, _ := ListProfiles("")
	euiccs, _ := ListEUICCs()
	byState := map[string]int{
		"available": 0, "reserved": 0, "downloaded": 0,
		"installed": 0, "enabled": 0, "disabled": 0, "deleted": 0,
	}
	for _, p := range profiles {
		byState[p.ProfileState]++
	}
	return map[string]any{
		"total_profiles": len(profiles),
		"by_state":       byState,
		"euiccs":         len(euiccs),
	}
}

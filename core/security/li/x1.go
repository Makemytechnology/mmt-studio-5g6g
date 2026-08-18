// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// x1.go — ADMF → POI provisioning surface (X1 reference point).
//
// Spec anchors:
//
//   - TS 33.127 §6.2 "X1 (LI provisioning)" — the ADMF authors a
//     warrant and pushes it to every POI in scope. Three operations:
//     provision (create + activate), modify (change scope / window /
//     MDF endpoint), deactivate (revoke). The X3 deliverer + X2
//     deliverer subscribe to the same warrant store so a successful
//     X1 call is what arms IRI/CC capture.
//
// The local product collapses ADMF + POI into one in-process surface
// (li.go header notes this). X1 is therefore a façade over the same
// CRUD primitives the operator panel uses; the role-explicit names
// here let operator runbooks and tester scripts speak in spec terms
// rather than HTTP-route ones. A future multi-vendor deployment
// would replace this façade with a real X1 listener that authenticates
// the remote ADMF and shares no other code path.
//
// Stage-3 ASN.1 envelope (per TS 33.128) is deferred — the local
// PDFs for 33.127 / 33.128 are not loaded. Until that work lands the
// X1 wire is JSON.

package li

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// X1ProvisionInput is the payload of an X1 provision call. Mirrors
// TS 33.127 §6.2 fields without the stage-3 ASN.1 wrapping.
type X1ProvisionInput struct {
	WarrantID    string `json:"warrant_id"`
	Authority    string `json:"authority"`
	CaseRef      string `json:"case_reference"`
	TargetIMSI   string `json:"target_imsi"`
	TargetMSISDN string `json:"target_msisdn"`
	Scope        string `json:"scope"`
	StartTime    string `json:"start_time"`
	EndTime      string `json:"end_time"`
	MDFEndpoint  string `json:"mdf_endpoint"`
	Operator     string `json:"operator"`
}

// X1ModifyInput is the payload of an X1 modify call. Only non-empty
// fields are applied; empty fields keep the existing value.
type X1ModifyInput struct {
	WarrantID   string `json:"warrant_id"`
	Scope       string `json:"scope"`
	EndTime     string `json:"end_time"`
	MDFEndpoint string `json:"mdf_endpoint"`
	Operator    string `json:"operator"`
}

// X1Provision creates and arms a warrant. Equivalent to CreateWarrant
// at the data layer; carries an x1_provision audit row so the
// trail distinguishes ADMF-driven vs. operator-panel calls.
func X1Provision(in X1ProvisionInput) error {
	if err := CreateWarrant(in.WarrantID, in.Authority, in.CaseRef,
		in.TargetIMSI, in.TargetMSISDN, in.Scope,
		in.StartTime, in.EndTime, in.MDFEndpoint, in.Operator); err != nil {
		return err
	}
	op := in.Operator
	if op == "" {
		op = "x1"
	}
	audit("x1_provision", in.WarrantID, op,
		fmt.Sprintf("scope=%s target=%s", in.Scope, in.TargetIMSI))
	return nil
}

// X1Deactivate is the spec-named verb for "stop intercepting this
// target". Implementation-level it is a revoke; the audit trail
// records both the role-named action and the underlying revoke so
// neither view loses information.
func X1Deactivate(warrantID, operator string) {
	if operator == "" {
		operator = "x1"
	}
	audit("x1_deactivate", warrantID, operator, "")
	RevokeWarrant(warrantID, operator)
}

// X1Modify changes mutable fields on an existing warrant. Per
// TS 33.127 §6.2 the scope, end_time, and MDF endpoint are the
// fields the ADMF can adjust over the warrant's lifetime; the
// target identity is fixed (a different target needs a new
// warrant). Empty input fields are skipped — partial modify is a
// supported semantics.
func X1Modify(in X1ModifyInput) error {
	if in.WarrantID == "" {
		return fmt.Errorf("x1 modify: warrant_id required")
	}
	w, err := GetWarrant(in.WarrantID)
	if err != nil {
		return err
	}
	if w == nil {
		return fmt.Errorf("x1 modify: warrant %q not found", in.WarrantID)
	}
	op := in.Operator
	if op == "" {
		op = "x1"
	}
	if in.Scope != "" {
		if !validScope(in.Scope) {
			return fmt.Errorf("x1 modify: invalid scope %q", in.Scope)
		}
		if _, err := engine.Exec("UPDATE li_warrants SET scope=? WHERE warrant_id=?",
			in.Scope, in.WarrantID); err != nil {
			return err
		}
	}
	if in.EndTime != "" {
		if _, err := engine.Exec("UPDATE li_warrants SET end_time=? WHERE warrant_id=?",
			in.EndTime, in.WarrantID); err != nil {
			return err
		}
	}
	if in.MDFEndpoint != "" {
		if _, err := engine.Exec("UPDATE li_warrants SET mdf_endpoint=? WHERE warrant_id=?",
			in.MDFEndpoint, in.WarrantID); err != nil {
			return err
		}
	}
	audit("x1_modify", in.WarrantID, op,
		fmt.Sprintf("scope=%s end=%s mdf=%s", in.Scope, in.EndTime, in.MDFEndpoint))
	refreshTargets()
	return nil
}

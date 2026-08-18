// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package redcap — NR Reduced Capability (RedCap) UE classification
// helpers.
//
// Spec anchors:
//   - TS 23.501 §5.41 NR RedCap and NR eRedCap UEs differentiation
//     — defines two CN-only RAT-type Identifiers:
//       * NR RedCap   — sub-type of NR RAT used to identify
//                       a UE indicating NR RedCap capability;
//       * NR eRedCap  — sub-type of NR RAT used to identify
//                       a UE indicating NR eRedCap capability.
//     The AMF determines the RAT-type to be NR RedCap as defined
//     in this clause; SMF/PCF then enforce policy gates such as
//     "NR RedCap not allowed as primary RAT" via subscription /
//     URSP.
//   - TS 23.501 §5.41 also notes Dual Connectivity does not apply
//     to NR RedCap UEs in this Release — IsDualConnectivityAllowed
//     captures that constraint.
//
// RedCap doesn't have its own row schema in this repo (per-UE
// RedCap support is recorded against ue.* by the AMF); this
// package is a pure constants + decision-helper module that the
// AMF / SMF call when they need to gate behaviour.
//
// TODO TS 38.306 — when the NR-side UE-Capability spec lands in
// specs/3gpp/, add the per-band RedCap capability bit decoding.
package redcap

// RAT type constants — TS 23.501 §5.41.
const (
	// RATTypeNRRedCap identifies a UE in CN signalling as a RedCap
	// UE (TS 23.501 §5.41 first para).
	RATTypeNRRedCap = "NR_REDCAP"

	// RATTypeNReRedCap identifies a UE as eRedCap (enhanced RedCap)
	// (TS 23.501 §5.41 second para).
	RATTypeNReRedCap = "NR_EREDCAP"

	// RATTypeNR is the regular NR RAT used as the comparison case.
	RATTypeNR = "NR"
)

// IsRedCap returns true when the supplied RAT-type identifies a
// RedCap (or eRedCap) UE per TS 23.501 §5.41.
func IsRedCap(ratType string) bool {
	return ratType == RATTypeNRRedCap || ratType == RATTypeNReRedCap
}

// IsDualConnectivityAllowed returns true if Dual Connectivity is
// permitted for a UE on this RAT-type. Per TS 23.501 §5.41
// ("In this Release of the specification, the Dual Connectivity
// function does not apply to the NR RedCap UE."), DC is denied
// for NR RedCap UEs. eRedCap is currently in the same prohibition
// because the spec language groups them under "the NR RedCap UE".
//
// TODO TS 23.501 §5.41 — re-check this gate when a future Release
// loosens DC for eRedCap; the current decision is intentionally
// conservative (deny both) to match this PDF's normative wording.
func IsDualConnectivityAllowed(ratType string) bool {
	return !IsRedCap(ratType)
}

// IsAllowedAsPrimaryRAT returns true unless policy bars the RAT
// type from being the primary RAT of a PDU session. The default
// PCC text in TS 23.501 §5.41 mentions "NR RedCap not allowed as
// primary RAT" / "NR eRedCap not allowed as primary RAT" as
// SUBSCRIPTION-derived restrictions — not a hard spec mandate.
//
// We expose the helper but default to allow=true; callers that
// have read a subscription saying otherwise should pass the
// derived flag in instead. This matches the "subscription gates
// override" pattern the SMF uses elsewhere.
func IsAllowedAsPrimaryRAT(ratType string, subscriptionAllowsAsPrimary bool) bool {
	if !IsRedCap(ratType) {
		return true
	}
	return subscriptionAllowsAsPrimary
}

// Status surface for the GUI panel — RedCap is decision-only and
// has no per-UE state of its own here.
func Status() map[string]any {
	return map[string]any{
		"rat_types_supported": []string{RATTypeNR, RATTypeNRRedCap, RATTypeNReRedCap},
	}
}

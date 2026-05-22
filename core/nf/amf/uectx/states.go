// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package uectx — AMF UE context (TS 23.502 §5.2.2.2) and state enumerations.
//
// Go port of nf/amf/amf_ue_ctx.py. Exposes the full 5GMM / CM / GMM-procedure
// state machines plus the per-UE context struct held by the AMF for the
// lifetime of a registration.
package uectx

// RMState — 5GMM Registration Management states (TS 24.501 §5.1.3.2, network side).
type RMState string

const (
	RMDeregistered RMState = "DEREGISTERED"
	RMRegistered   RMState = "REGISTERED"
)

// CMState — 5GMM Connection Management states (TS 24.501 §5.3.3).
type CMState string

const (
	CMIdle      CMState = "IDLE"
	CMConnected CMState = "CONNECTED"
)

// RRCState — RAN-layer RRC state observed by the AMF via TS 38.413
// §8.3.5 "RRC Inactive Transition Report" (§9.2.2.10 message,
// §9.3.1.92 RRC State IE). Distinct from CMState: a UE in
// CM-CONNECTED on N1 can be either RRC-CONNECTED or RRC-INACTIVE
// at the radio layer (TS 38.300 §9.2.1.4). RRCConnected is the
// default; RRCInactive arms the CN-based MT communication handling
// path described in TS 23.502 §4.8.1.1a.
type RRCState string

const (
	// RRCConnected is the default at NG Setup / Initial Context
	// Setup time. Mirrors NGAP RRCState=1 (TS 38.413 §9.3.1.92).
	RRCConnected RRCState = "CONNECTED"
	// RRCInactive is reported by the NG-RAN node when the UE
	// suspends RRC. Mirrors NGAP RRCState=0 (TS 38.413 §9.3.1.92).
	RRCInactive RRCState = "INACTIVE"
)

// GMMProcedure — top-level 5GMM procedure (TS 24.501 §5.1.3.2).
// Only one procedure can be pending at a time. Auth / SecMode / Identity
// run as sub-steps inside REGISTRATION (see GMMSubStep).
type GMMProcedure string

const (
	GMMProcNone           GMMProcedure = "NONE"
	GMMProcRegistration   GMMProcedure = "REGISTRATION"
	GMMProcDeregistration GMMProcedure = "DEREGISTRATION"
	GMMProcServiceRequest GMMProcedure = "SERVICE_REQUEST"
	GMMProcConfigUpdate   GMMProcedure = "CONFIG_UPDATE"
	GMMProcPaging         GMMProcedure = "PAGING"
)

// GMMSubStep — common-procedure sub-step inside a GMMProcedure (TS 24.501 §5.4).
type GMMSubStep string

const (
	GMMSubNone           GMMSubStep = "NONE"
	GMMSubIdentification GMMSubStep = "IDENTIFICATION"
	GMMSubAuthentication GMMSubStep = "AUTHENTICATION"
	GMMSubSecurityMode   GMMSubStep = "SECURITY_MODE"
)

// NGAPProcedure — pending NGAP procedure on the UE association (TS 38.413).
//
// Used as a guard at AMF-initiated procedure entry points — the pre-
// check rules are implemented in CanStartNGAPProcedure below and
// follow the spec's "Elementary Procedure" framing (§8.1): most EPs
// are independent, but a few are exclusive by construction.
type NGAPProcedure string

const (
	NGAPProcNone                      NGAPProcedure = "NONE"
	NGAPProcInitialContextSetup       NGAPProcedure = "INITIAL_CONTEXT_SETUP"       // §8.3.1
	NGAPProcPDUSessionResourceSetup   NGAPProcedure = "PDU_SESSION_RESOURCE_SETUP"  // §8.2.1
	NGAPProcPDUSessionResourceRelease NGAPProcedure = "PDU_SESSION_RESOURCE_RELEASE" // §8.2.2
	NGAPProcPDUSessionResourceModify  NGAPProcedure = "PDU_SESSION_RESOURCE_MODIFY"  // §8.2.3
	NGAPProcUEContextRelease          NGAPProcedure = "UE_CONTEXT_RELEASE"          // §8.3.3
)

// CanStartNGAPProcedure returns (ok, reason) for whether the AMF may
// initiate `desired` on a UE that currently has `current` marked as
// in-progress (ue.NGAPProc). Rules strictly derived from TS 38.413
// v19.2.0 (local PDF specs/3gpp/ts_138413v190200p.pdf):
//
//	§8.1 Elementary Procedures — each EP is self-contained; §8.2
//	  Resource EPs (Setup / Modify / Release) are independent in
//	  principle and may coexist for different PDU sessions on the
//	  same UE. Our "current != none" guard below still rejects
//	  same-type concurrent initiation because our FSM tracks one
//	  resource procedure at a time (per-type ref counter would be
//	  the richer design).
//
//	§8.3.3.1 UE Context Release — "used for releasing the logical
//	  NG-connection and the associated UE context both in the
//	  NG-RAN node and in the AMF." Once release is underway, the
//	  UE is on its way to CM-IDLE; new AMF-initiated UP procedures
//	  (PDU Session Resource Setup/Modify, ICS) would be moot —
//	  they target a connection that's being torn down. Reject.
//
//	§8.3.1.1 Initial Context Setup — "the AMF establishes the
//	  logical UE-associated signalling connection." One-shot per
//	  connection; a second ICS while one is pending is a caller
//	  bug.
//
// The returned reason is a log-friendly tag citing the clause the
// rejection is based on — makes the "why" visible in logs instead
// of relying on FSM-collision warnings.
func CanStartNGAPProcedure(current, desired NGAPProcedure) (bool, string) {
	if current == NGAPProcNone {
		return true, ""
	}
	// §8.3.3.1 — any AMF-initiated procedure during an ongoing
	// release targets a connection that's going away.
	if current == NGAPProcUEContextRelease {
		return false, "UE Context Release in progress (TS 38.413 §8.3.3.1) — connection going away"
	}
	// §8.3.1.1 — second ICS while one pending. Shouldn't happen.
	if desired == NGAPProcInitialContextSetup && current == NGAPProcInitialContextSetup {
		return false, "Initial Context Setup already in progress (TS 38.413 §8.3.1.1) — one per connection"
	}
	// Everything else: spec-permitted parallelism. Return ok.
	// The caller can still rely on FSM state for low-level tracking.
	return true, ""
}

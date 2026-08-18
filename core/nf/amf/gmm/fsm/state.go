// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package fsm — per-UE 5GMM state machine (TS 24.501 §5).
//
// A 5GMM AMF-side state machine with explicit states, events, guards, and
// declarative timer handling. Replaces the prior model where every NAS
// handler mutated `ue.GMMProc` / `ue.GMMSub` directly and started/cancelled
// timers inline. Now a single transition table captures every legal
// (state, event) → (state', action, timers) arrow from TS 24.501 §5.4 and
// §5.5, and everything else is rejected up front.
//
// The FSM is per-UE; instance lifetime == UE context lifetime.
package fsm

import "fmt"

// State is a coarse 5GMM state (TS 24.501 §5.1.3.2 + §5.4 sub-steps).
//
// The spec separates Registration Management (RM-DEREGISTERED /
// RM-REGISTERED) from Connection Management (CM-IDLE / CM-CONNECTED)
// and from the currently-running *procedure* (registration, dereg,
// service request, common procedures). We fold all three into one
// enum because the AMF-side common procedures (identification,
// authentication, security mode control) are naturally phases inside
// a larger registration procedure — modelling them as separate
// dimensions quadruples the state space without capturing any extra
// legality.
type State int

const (
	// StateDeregistered — no AMF context beyond the bare NGAP association
	// (TS 24.501 §5.1.3.2.2 "5GMM-DEREGISTERED"). Registration Request
	// transitions out.
	StateDeregistered State = iota

	// StateIdentification — AMF sent Identity Request, awaiting Identity
	// Response (TS 24.501 §5.4.3). Only taken when the Registration
	// Request's 5GS mobile identity wasn't usable as-is (rare).
	StateIdentification

	// StateAuthentication — AMF sent Authentication Request, awaiting
	// Authentication Response / Failure (TS 24.501 §5.4.1).
	StateAuthentication

	// StateSecurityMode — AMF sent Security Mode Command, awaiting
	// Security Mode Complete / Reject (TS 24.501 §5.4.2). NAS ciphering
	// becomes active on the first SHT=4 or SHT=3 DL message and on
	// receipt of a valid MAC on the SMC-Complete.
	StateSecurityMode

	// StateRegisteredInitiated — SMC-Complete received, Registration
	// Accept shipped, ICS pending, awaiting Registration Complete
	// (TS 24.501 §5.5.1.2.2). T3550 guards retransmission.
	StateRegisteredInitiated

	// StateRegistered — terminal success state for the registration
	// procedure (TS 24.501 §5.1.3.2.1 "5GMM-REGISTERED"). UL NAS
	// Transport, Service Request, Deregistration all dispatch from here.
	StateRegistered

	// StateDeregistrationInitiated — MO dereg received; PDU sessions
	// torn down, awaiting UE Context Release Complete (TS 24.501
	// §5.5.2.2). Twait-ue-ctx-release (impl-specific 10 s) guards
	// completion — T3521 itself is UE-side per §10.2.2.
	StateDeregistrationInitiated

	// StateMTDeregPending — AMF-initiated (network-triggered) dereg;
	// Deregistration Request sent, awaiting Accept from UE
	// (§5.5.2.3). T3522 guards retransmission.
	StateMTDeregPending
)

// String renders the state name for log lines.
func (s State) String() string {
	switch s {
	case StateDeregistered:
		return "DEREGISTERED"
	case StateIdentification:
		return "IDENTIFICATION"
	case StateAuthentication:
		return "AUTHENTICATION"
	case StateSecurityMode:
		return "SECURITY_MODE"
	case StateRegisteredInitiated:
		return "REGISTERED_INITIATED"
	case StateRegistered:
		return "REGISTERED"
	case StateDeregistrationInitiated:
		return "DEREGISTRATION_INITIATED"
	case StateMTDeregPending:
		return "MT_DEREG_PENDING"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

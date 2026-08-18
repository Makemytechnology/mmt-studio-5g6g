// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package fsm — per-PDU-session 5GSM state machine (TS 24.501 §6).
//
// Sibling to nf/amf/gmm/fsm: same shape (State / Event / Transition /
// declarative timer graph) but scoped to a single PDU session on the
// SMF side. One instance per (IMSI, PDUSessionID); looked up via Of.
//
// States follow TS 24.501 §6.1.3.1 "PDU SESSION STATES" on the
// network side, plus the transient PFCP/NGAP waits the session
// physically has between "accepted at NAS layer" and "GTP-U tunnel
// up end-to-end".
package fsm

import "fmt"

type State int

const (
	// StateInactive — no PDU session context. Establishment Request
	// triggers the procedure (TS 24.501 §6.1.3.1 PDU SESSION INACTIVE).
	StateInactive State = iota

	// StateEstablishmentPending — SMF has accepted the request and
	// started N4 session establishment with the UPF. Waiting on the
	// PFCP Session Establishment Response before we can ship the
	// Establishment Accept back to the UE.
	StateEstablishmentPending

	// StateActivationPending — 5GSM Establishment Accept has gone out
	// piggybacked in PDUSessionResourceSetupRequest; waiting for the
	// gNB's PDUSessionResourceSetupResponse carrying the DL-TEID and
	// TAC. Corresponds to TS 24.501 PDU SESSION ACTIVE PENDING.
	StateActivationPending

	// StateActive — GTP-U tunnel up both directions, UE data is flowing
	// (TS 24.501 §6.1.3.1 PDU SESSION ACTIVE).
	StateActive

	// StateModificationPending — SMF has sent PDU Session Modification
	// Command; waiting for Modification Complete (TS 24.501 §6.3.2).
	// T3591 guards.
	StateModificationPending

	// StateReleasePending — SMF has sent PDU Session Release Command;
	// waiting for Release Complete (TS 24.501 §6.3.3). T3592 guards.
	StateReleasePending

	// StateReleased — session torn down. Terminal; UE must re-establish
	// for another session on this PDU Session ID.
	StateReleased
)

// String renders the state name for log lines + KPI dashboards.
func (s State) String() string {
	switch s {
	case StateInactive:
		return "INACTIVE"
	case StateEstablishmentPending:
		return "ESTABLISHMENT_PENDING"
	case StateActivationPending:
		return "ACTIVATION_PENDING"
	case StateActive:
		return "ACTIVE"
	case StateModificationPending:
		return "MODIFICATION_PENDING"
	case StateReleasePending:
		return "RELEASE_PENDING"
	case StateReleased:
		return "RELEASED"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

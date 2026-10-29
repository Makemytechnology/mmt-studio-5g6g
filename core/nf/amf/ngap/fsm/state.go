// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package fsm — per-UE NGAP association state machine (TS 38.413 §8).
//
// This FSM models the NGAP-side view of a single UE context at the
// gNB, independent of the GMM / 5GSM procedures running above it.
// The spec describes these states in prose across §8.2 (PDU Session
// Resource), §8.3 (UE Context), §8.4 (Mobility), §8.6 (Paging) rather
// than a named enum — we distill the effective network-side states
// that matter for procedure collision + timer control.
//
// Race conditions this FSM is here to resolve:
//
//   1. PDUSessionResourceSetupRequest reply arrives after the gNB has
//      already Request-Release'd the UE — previously applied the Setup
//      response, installed FARs, only to tear them down seconds later.
//   2. InitialContextSetupResponse raced with UEContextReleaseRequest
//      (gNB detected UE radio link failure during ICS).
//   3. Twait-ue-ctx-release expires while UE is still generating UL NAS
//      Transport — our prior code left the association half-up.
//   4. Procedure-in-progress collisions: NGAP spec says only one
//      UE-associated elementary procedure should be in flight; FSM
//      enforces the guard at transition time.
package fsm

import "fmt"

// State is the NGAP-side view of a single UE association. It overlaps
// with uectx.NGAPProcedure but is the authoritative source from this
// package forward — uectx gets mirrored for display.
type State int

const (
	// StateNotEstablished — no UE context on the gNB yet. Initial UE
	// Message just arrived (or nothing at all); AMF hasn't sent the
	// Initial Context Setup Request. Anything the gNB sends in this
	// state that isn't InitialUEMessage / ErrorIndication is noise.
	StateNotEstablished State = iota

	// StateICSPending — AMF has sent InitialContextSetupRequest and is
	// waiting for the gNB's Response / Failure (TS 38.413 §8.3.1).
	// Twait-ICS applies; no TS-named timer — wait-for-response is a
	// guard implemented as Twait-ue-ctx-release on the AMF side.
	StateICSPending

	// StateEstablished — gNB acknowledged the UE context; this is the
	// steady state where UL/DL NAS Transport and PDU session procedures
	// can flow. Every "PDU Session Resource Setup" fork runs from here
	// and returns here on completion.
	StateEstablished

	// StateResourceSetupPending — AMF has sent PDUSessionResourceSetup-
	// Request; waiting for the gNB Response / Failure (§8.2.1). Can
	// fork into multiple concurrent Setup attempts per UE — FSM keeps
	// track of pendingSessions count, so only the first transitions
	// into this state and the last one back to Established.
	StateResourceSetupPending

	// StateResourceModifyPending — PDUSessionResourceModifyRequest sent,
	// awaiting Response / Failure (§8.2.2).
	StateResourceModifyPending

	// StateResourceReleasePending — PDUSessionResourceReleaseCommand
	// sent, awaiting Response (§8.2.3).
	StateResourceReleasePending

	// StateCtxReleasePending — AMF sent UEContextReleaseCommand
	// (§8.3.4), armed Twait-ue-ctx-release (10s), awaiting Complete.
	StateCtxReleasePending

	// StateReleased — terminal. UE Context Release Complete received
	// OR Twait-ue-ctx-release fired; gNB-side context torn down. Next
	// gNB→AMF traffic for this UE must come via a fresh InitialUE
	// Message (i.e. a new association).
	StateReleased

	// StateHandoverPreparation — source gNB sent HANDOVER REQUIRED;
	// AMF is requesting the target gNB's resources via HANDOVER
	// REQUEST (TS 38.413 §8.4.2). T_HandoverPrep armed.
	StateHandoverPreparation

	// StateHandoverExecution — target gNB acknowledged HANDOVER
	// REQUEST; source has HANDOVER COMMAND + is performing over-the-
	// air handover. Awaiting HANDOVER NOTIFY from target.
	StateHandoverExecution
)

// String renders the state name.
func (s State) String() string {
	switch s {
	case StateNotEstablished:
		return "NOT_ESTABLISHED"
	case StateICSPending:
		return "ICS_PENDING"
	case StateEstablished:
		return "ESTABLISHED"
	case StateResourceSetupPending:
		return "RESOURCE_SETUP_PENDING"
	case StateResourceModifyPending:
		return "RESOURCE_MODIFY_PENDING"
	case StateResourceReleasePending:
		return "RESOURCE_RELEASE_PENDING"
	case StateCtxReleasePending:
		return "CTX_RELEASE_PENDING"
	case StateReleased:
		return "RELEASED"
	case StateHandoverPreparation:
		return "HANDOVER_PREPARATION"
	case StateHandoverExecution:
		return "HANDOVER_EXECUTION"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

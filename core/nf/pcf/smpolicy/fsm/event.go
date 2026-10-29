// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import "fmt"

// Event drives an SM Policy Association transition. Naming mirrors
// the TS 29.512 service operation that produced it so the FSM log is
// directly greppable against the spec:
//
//	Ev<Op>Request  — handler is about to invoke the service op
//	Ev<Op>Response — op returned success (for Create: SmPolicyDecision
//	                 delivered; for Update: response with updated
//	                 decision; for Delete: termination confirmed)
//	Ev<Op>Reject   — op returned failure (ProblemDetails per
//	                 TS 29.571 §5.2.2)
//
// Handler-driven pattern (same as 5GSM): handlers run the actual
// policy build (DB lookup, binding merge, dynamic SDF derivation)
// and THEN fire the outcome event. State stays Pending between
// "Request" and "Response" so concurrent Update attempts block or
// queue cleanly once the SBI path lands.
type Event int

const (
	// TS 29.512 §4.2.2 Create ─────────────────────────────────────────
	EvCreateRequest Event = iota
	EvCreateResponse
	EvCreateReject

	// TS 29.512 §4.2.4 Update (SMF → PCF) ─────────────────────────────
	EvUpdateRequest
	EvUpdateResponse
	EvUpdateReject

	// TS 29.512 §4.2.3 UpdateNotify (PCF → SMF) ────────────────────────
	//
	// The PCF pushes a changed SmPolicyDecision to the SMF whenever a
	// Policy Control Request Trigger fires (§4.2.3 preamble): new AF
	// media, subscription change, Revalidation Timer expiry, etc.
	EvUpdateNotifySent    // PCF has sent the UpdateNotify to SMF
	EvUpdateNotifyAck     // SMF acked with enforcement result
	EvUpdateNotifyFailure // SMF returned failure (ProblemDetails)

	// TS 29.512 §4.2.5 Delete ─────────────────────────────────────────
	EvDeleteRequest
	EvDeleteResponse

	// Timers ──────────────────────────────────────────────────────────
	//
	// Revalidation Timer — the one timer defined by 29.512 for the SM
	// Policy Association lifecycle. Set per §4.2.2.4 "Provisioning of
	// revalidation time" (Create) and/or §4.2.3.4 (UpdateNotify). Its
	// value is carried in the "revalidationTime" attribute of
	// SmPolicyDecision; on expiry the SMF MUST trigger a PCC rule
	// request toward the PCF (§4.2.2.4: "the SMF shall start the
	// timer based on the revalidation time and shall trigger a PCC
	// rule request towards the PCF before the indicated revalidation
	// time") — in this FSM that appears on the PCF side as an
	// incoming Update request from the SMF, driven by the SMF's own
	// copy of the timer.
	EvRevalidationTimerExpired
)

// String renders the event name for log lines.
func (e Event) String() string {
	switch e {
	case EvCreateRequest:
		return "CreateRequest"
	case EvCreateResponse:
		return "CreateResponse"
	case EvCreateReject:
		return "CreateReject"
	case EvUpdateRequest:
		return "UpdateRequest"
	case EvUpdateResponse:
		return "UpdateResponse"
	case EvUpdateReject:
		return "UpdateReject"
	case EvUpdateNotifySent:
		return "UpdateNotifySent"
	case EvUpdateNotifyAck:
		return "UpdateNotifyAck"
	case EvUpdateNotifyFailure:
		return "UpdateNotifyFailure"
	case EvDeleteRequest:
		return "DeleteRequest"
	case EvDeleteResponse:
		return "DeleteResponse"
	case EvRevalidationTimerExpired:
		return "RevalidationTimerExpired"
	}
	return fmt.Sprintf("Event(%d)", int(e))
}

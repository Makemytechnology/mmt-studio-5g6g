// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package fsm — per-SM-Policy-Association state machine (PCF side).
//
// Authoritative spec: TS 29.512 v19.6.0 "Npcf_SMPolicyControl service"
// (PDF: specs/3gpp/ts_129512v190600p.pdf). The service is defined
// in §4.2 "Service Operations":
//
//	§4.2.2  Create      — SM Policy Association establishment
//	§4.2.3  UpdateNotify — PCF→SMF push (also carries termination)
//	§4.2.4  Update      — SMF→PCF request
//	§4.2.5  Delete      — SM Policy Association termination
//
// The spec does not define a named PCF-side state machine, but the
// association lifecycle is implicit across §4.2.2 (creation) /
// §4.2.5 (termination) / §4.2.4 (updates) — this FSM names those
// transitions so the handler code can stay declarative, mirroring
// the 5GSM FSM at nf/smf/session/fsm/.
//
// One FSM instance per SM Policy Association; the association is
// keyed by (IMSI, PDUSessionID) in this port — the spec keys it by
// an opaque smPolicyCtxRef URI allocated by the PCF on Create.
package fsm

import "fmt"

// State is the SM Policy Association state on the PCF. The 5G PCF
// model is simpler than the 4G PCRF (no separate "policy update in
// progress" super-state): once the association exists it's either
// Active or on its way out.
type State int

const (
	// StateNone — no SM Policy Association context. Initial state;
	// a Create Request (TS 29.512 §4.2.2) transitions out.
	StateNone State = iota

	// StateCreatePending — SMF has issued Npcf_SMPolicyControl_Create;
	// PCF is building the initial SmPolicyDecision (PCC rules, AMBR,
	// default QoS, charging). Transient; exists only because the
	// in-process Create is synchronous today — the FSM keeps the slot
	// for when the SBI layer introduces real round-trip latency.
	StateCreatePending

	// StateActive — Association established (Create Response delivered
	// carrying the SmPolicyDecision). The PCF is free to push
	// UpdateNotify messages (§4.2.3), and the SMF may request Update
	// (§4.2.4) at any point. Revalidation Timer may be armed here per
	// §4.2.2.4 / §4.2.3.4.
	StateActive

	// StateUpdatePending — the PCF has pushed UpdateNotify (§4.2.3);
	// waiting for the SMF acknowledgement with the enforcement result
	// (ruleReports / sessionRuleReports per §4.2.3.x). Re-enters Active
	// on ack.
	StateUpdatePending

	// StateTerminating — Delete Request (§4.2.5.2) sent toward the PCF;
	// waiting for Delete Response. Mirrors §4.2.5 "SM Policy
	// Association termination".
	StateTerminating

	// StateTerminated — association removed. Terminal; the (IMSI,
	// PDUSessionID) slot is free for re-establishment.
	StateTerminated
)

// String renders the state name for log lines.
func (s State) String() string {
	switch s {
	case StateNone:
		return "NONE"
	case StateCreatePending:
		return "CREATE_PENDING"
	case StateActive:
		return "ACTIVE"
	case StateUpdatePending:
		return "UPDATE_PENDING"
	case StateTerminating:
		return "TERMINATING"
	case StateTerminated:
		return "TERMINATED"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

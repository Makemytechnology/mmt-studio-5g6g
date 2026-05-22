// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package fsm — per-PFCP-session state machine (TS 29.244 §7.5).
//
// Models the SMF-side view of one PFCP session on a UPF, independent
// of the 5GSM NAS state above and the UPF's internal packet-path
// state below. Useful now for observability (lets the GUI report
// "session is in PFCP modify-pending") and essential later when we
// move N4 off-box and procedures stop being synchronous cgo calls.
//
// State names and transitions follow TS 29.244 §7.5.2 "PFCP session
// context state transition diagram".
package fsm

import "fmt"

// State is the PFCP session-context state (TS 29.244 §7.5.2).
type State int

const (
	// StateInactive — no PFCP session. Either never established, or
	// terminal after deletion.
	StateInactive State = iota

	// StateEstablishInProgress — SMF sent PFCP Session Establishment
	// Request, awaiting Response. T-PFCP-estab retransmission armed.
	StateEstablishInProgress

	// StateEstablished — Session Establishment Response received; FARs
	// / PDRs / QERs / URRs installed on the UPF and traffic can flow.
	StateEstablished

	// StateModifyInProgress — SMF sent PFCP Session Modification
	// Request (add / change / remove rules), awaiting Response.
	StateModifyInProgress

	// StateDeleteInProgress — SMF sent PFCP Session Deletion Request,
	// awaiting Response.
	StateDeleteInProgress
)

// String renders the state name for log lines and /api/smf/pfcp.
func (s State) String() string {
	switch s {
	case StateInactive:
		return "INACTIVE"
	case StateEstablishInProgress:
		return "ESTABLISH_IN_PROGRESS"
	case StateEstablished:
		return "ESTABLISHED"
	case StateModifyInProgress:
		return "MODIFY_IN_PROGRESS"
	case StateDeleteInProgress:
		return "DELETE_IN_PROGRESS"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

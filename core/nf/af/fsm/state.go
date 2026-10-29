// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package fsm — per-AF-session state machine.
//
// Authoritative spec: TS 29.514 v19.6.0 "Npcf_PolicyAuthorization
// Service" (PDF: specs/3gpp/ts_129514v190600p.pdf). The PA service
// operations in §4.2 drive the AF-session lifecycle:
//
//	§4.2.2  Create      — AF requests authorization of service info
//	§4.2.3  Update      — AF modifies an existing authorization
//	§4.2.4  Delete      — AF terminates the authorization
//	§4.2.5  Notify      — PCF → AF notification (resource status,
//	                       SDF termination, access-type change, …)
//	§4.2.6  Subscribe   — AF subscribes to a PCF-driven event
//	§4.2.7  Unsubscribe — AF tears down the subscription
//
// No spec-defined timer for the AF-session itself; the FSM is event-
// driven. Mirrors the SM Policy FSM at nf/pcf/smpolicy/fsm.
package fsm

import "fmt"

// State is the AF session state on the AF side.
type State int

const (
	// StateInitial — no AF session context. A §4.2.2 Create transitions
	// out.
	StateInitial State = iota

	// StateAuthPending — AF has sent Create to PCF; waiting for the
	// authorization response.
	StateAuthPending

	// StateActive — PCF returned authorization OK. AF session is
	// authoritative; §4.2.3 Update / §4.2.5 Notify operate from here.
	StateActive

	// StateUpdatePending — §4.2.3 Update sent; waiting for the response.
	StateUpdatePending

	// StateTerminated — §4.2.4 Delete confirmed. Terminal.
	StateTerminated

	// StateFailed — authorization failed (Create or Update rejected by
	// PCF). The AF session is retained for observability; the caller
	// must explicitly Delete to terminate.
	StateFailed
)

// String renders the state name for log lines.
func (s State) String() string {
	switch s {
	case StateInitial:
		return "INITIAL"
	case StateAuthPending:
		return "AUTH_PENDING"
	case StateActive:
		return "ACTIVE"
	case StateUpdatePending:
		return "UPDATE_PENDING"
	case StateTerminated:
		return "TERMINATED"
	case StateFailed:
		return "FAILED"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

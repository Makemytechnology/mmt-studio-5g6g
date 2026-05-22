// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import "fmt"

// Event drives a PFCP session transition. Covers every SMF-initiated
// PFCP message plus UPF-originated ones (Session Report, Heartbeat
// miss) and retransmission timer expiries.
type Event int

const (
	// SMF → UPF (N4 requests) ─────────────────────────────────────────
	EvEstablishRequestSent Event = iota
	EvModifyRequestSent
	EvDeleteRequestSent

	// UPF → SMF (N4 responses) ────────────────────────────────────────
	EvEstablishResponse  // success
	EvEstablishFailure   // rejected (cause in response)
	EvModifyResponse
	EvModifyFailure
	EvDeleteResponse

	// UPF-initiated ───────────────────────────────────────────────────
	EvSessionReport      // UsageReport / DL data notification
	EvPFCPHeartbeatMiss  // N peer heartbeats missed → association down

	// Timer expiries ──────────────────────────────────────────────────
	EvTPFCPEstabExpired
	EvTPFCPModifyExpired
	EvTPFCPDeleteExpired
)

// String renders the event name.
func (e Event) String() string {
	switch e {
	case EvEstablishRequestSent:
		return "EstablishRequestSent"
	case EvModifyRequestSent:
		return "ModifyRequestSent"
	case EvDeleteRequestSent:
		return "DeleteRequestSent"
	case EvEstablishResponse:
		return "EstablishResponse"
	case EvEstablishFailure:
		return "EstablishFailure"
	case EvModifyResponse:
		return "ModifyResponse"
	case EvModifyFailure:
		return "ModifyFailure"
	case EvDeleteResponse:
		return "DeleteResponse"
	case EvSessionReport:
		return "SessionReport"
	case EvPFCPHeartbeatMiss:
		return "PFCPHeartbeatMiss"
	case EvTPFCPEstabExpired:
		return "TPFCPEstabExpired"
	case EvTPFCPModifyExpired:
		return "TPFCPModifyExpired"
	case EvTPFCPDeleteExpired:
		return "TPFCPDeleteExpired"
	}
	return fmt.Sprintf("Event(%d)", int(e))
}

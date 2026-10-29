// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import "fmt"

// Event drives an AF session transition. Naming maps 1:1 to the
// TS 29.514 §4.2 service operation that produced it.
type Event int

const (
	EvCreateRequest  Event = iota // §4.2.2 AF → PCF (authorization request)
	EvAuthorized                  // Create/Update returned success
	EvAuthRejected                // Create/Update returned failure (ProblemDetails)
	EvUpdateRequest               // §4.2.3 AF → PCF
	EvDeleteRequest               // §4.2.4 AF → PCF
	EvNotifyReceived              // §4.2.5 PCF → AF notification (observational)
)

// String renders the event name for log lines.
func (e Event) String() string {
	switch e {
	case EvCreateRequest:
		return "CreateRequest"
	case EvAuthorized:
		return "Authorized"
	case EvAuthRejected:
		return "AuthRejected"
	case EvUpdateRequest:
		return "UpdateRequest"
	case EvDeleteRequest:
		return "DeleteRequest"
	case EvNotifyReceived:
		return "NotifyReceived"
	}
	return fmt.Sprintf("Event(%d)", int(e))
}

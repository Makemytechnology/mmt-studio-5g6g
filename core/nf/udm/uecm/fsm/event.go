// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import "fmt"

// Event drives a UECM transition. Naming maps 1:1 to the TS 29.503
// §5.3.2 service operation so the log is directly greppable against
// the spec:
//
//	EvRegisterRequest                — §5.3.2.2 AMF→UDM registration
//	EvRegisterReject                 — Registration Response error
//	EvDeregisterRequest              — §5.3.2.4 AMF→UDM deregistration
//	EvDeregistrationNotificationSent — §5.3.2.3 UDM→AMF push (old AMF)
//
// Handler-driven pattern (same as 5GMM / 5GSM): the UDM handler in
// nf/udm/uecm.go runs the actual registry mutation and THEN fires
// the event that advances state.
type Event int

const (
	EvRegisterRequest Event = iota
	EvRegisterReject
	EvDeregisterRequest
	EvDeregistrationNotificationSent // §5.3.2.3 — informational; reserved for multi-AMF
)

// String renders the event name for log lines.
func (e Event) String() string {
	switch e {
	case EvRegisterRequest:
		return "RegisterRequest"
	case EvRegisterReject:
		return "RegisterReject"
	case EvDeregisterRequest:
		return "DeregisterRequest"
	case EvDeregistrationNotificationSent:
		return "DeregistrationNotificationSent"
	}
	return fmt.Sprintf("Event(%d)", int(e))
}

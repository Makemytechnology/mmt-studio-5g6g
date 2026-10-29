// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package fsm — per-UE UECM (UE Context Management) state machine
// on the UDM side.
//
// Authoritative spec: TS 29.503 v19.6.0 §5.3 "Nudm_UEContextManagement
// Service" (PDF: specs/3gpp/ts_129503v190600p.pdf). The spec does
// not define a named PCF-style state chart for the UECM association,
// but the lifecycle is implicit across §5.3.2.2 / §5.3.2.4 / §5.3.2.3:
//
//	§5.3.2.2 Registration               — AMF → UDM, AMF claims the UE
//	§5.3.2.3 DeregistrationNotification — UDM → AMF push (old AMF)
//	§5.3.2.4 Deregistration             — AMF → UDM, AMF releases the UE
//	§5.3.2.5 Get                        — consumer → UDM, lookup
//
// This FSM names those transitions so the UDM handler code at
// nf/udm/uecm.go can stay declarative and the state is loggable /
// testable, mirroring the 5GMM FSM at nf/amf/gmm/fsm and the SM
// Policy FSM at nf/pcf/smpolicy/fsm.
//
// No timer is defined by the spec for the UECM association itself
// (compare §4.2.2.4 Revalidation Timer on the PCF side). The FSM is
// purely event-driven.
package fsm

import "fmt"

// State is the UECM association state on the UDM. Keyed by SUPI
// (IMSI); the spec uses the full URI /nudm-uecm/v1/{supi}/registrations/...
type State int

const (
	// StateDeregistered — no AMF currently serves this UE. Initial
	// state; a §5.3.2.2 Registration transitions out.
	StateDeregistered State = iota

	// StateRegistered — an AMF has successfully registered for this
	// UE. A new §5.3.2.2 from a different AMF causes the UDM to emit
	// §5.3.2.3 DeregistrationNotification to the old AMF (modelled
	// here as a Deregister→Register pair on the FSM) and then take
	// the new registration.
	StateRegistered
)

// String renders the state name for log lines.
func (s State) String() string {
	switch s {
	case StateDeregistered:
		return "DEREGISTERED"
	case StateRegistered:
		return "REGISTERED"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

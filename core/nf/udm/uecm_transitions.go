// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// UECM transition table (TS 29.503 §5.3.2). Installed at package init
// via fsm.SetDefaultTable. No Actions — the handlers in uecm.go carry
// out the registry work and then Fire the outcome event.
package udm

import (
	uecmfsm "github.com/mmt/mmt-studio-core/nf/udm/uecm/fsm"
)

func init() {
	uecmfsm.SetDefaultTable([]uecmfsm.Transition{
		// §5.3.2.2 Registration ──────────────────────────────────────
		{From: uecmfsm.StateDeregistered, Event: uecmfsm.EvRegisterRequest, To: uecmfsm.StateRegistered},
		// Re-register from an already-registered UE (covers AMF restart
		// and the multi-AMF handover case where RegisterAMF cleared
		// the previous record before firing EvRegisterRequest).
		{From: uecmfsm.StateRegistered, Event: uecmfsm.EvRegisterRequest, To: uecmfsm.StateRegistered},
		// Registration rejected — stay Deregistered.
		{From: uecmfsm.StateDeregistered, Event: uecmfsm.EvRegisterReject, To: uecmfsm.StateDeregistered},

		// §5.3.2.4 Deregistration ────────────────────────────────────
		{From: uecmfsm.StateRegistered, Event: uecmfsm.EvDeregisterRequest, To: uecmfsm.StateDeregistered},

		// §5.3.2.3 DeregistrationNotification — UDM → AMF push.
		// Modelled as an FSM event for logging / multi-AMF evolution.
		// In the single-AMF reference deployment the notification is
		// a no-op so we self-loop.
		{From: uecmfsm.StateRegistered, Event: uecmfsm.EvDeregistrationNotificationSent, To: uecmfsm.StateRegistered},
	})
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// AF session transition table (TS 29.514 §4.2 Npcf_PolicyAuthorization
// service operations). No Actions — handlers in af.go carry out the
// PCF call and then Fire the outcome event.
package af

import (
	affsm "github.com/mmt/mmt-studio-core/nf/af/fsm"
)

func init() {
	affsm.SetDefaultTable([]affsm.Transition{
		// §4.2.2 Create ──────────────────────────────────────────────
		{From: affsm.StateInitial, Event: affsm.EvCreateRequest, To: affsm.StateAuthPending},
		{From: affsm.StateAuthPending, Event: affsm.EvAuthorized, To: affsm.StateActive},
		{From: affsm.StateAuthPending, Event: affsm.EvAuthRejected, To: affsm.StateFailed},

		// §4.2.3 Update ──────────────────────────────────────────────
		{From: affsm.StateActive, Event: affsm.EvUpdateRequest, To: affsm.StateUpdatePending},
		{From: affsm.StateUpdatePending, Event: affsm.EvAuthorized, To: affsm.StateActive},
		// Update rejected — §4.2.3 says the existing authorization
		// remains; we fall back to Active (not Failed).
		{From: affsm.StateUpdatePending, Event: affsm.EvAuthRejected, To: affsm.StateActive},

		// §4.2.5 Notify (PCF → AF) — observational, self-loop.
		{From: affsm.StateActive, Event: affsm.EvNotifyReceived, To: affsm.StateActive},

		// §4.2.4 Delete ──────────────────────────────────────────────
		{From: affsm.StateActive, Event: affsm.EvDeleteRequest, To: affsm.StateTerminated},
		{From: affsm.StateUpdatePending, Event: affsm.EvDeleteRequest, To: affsm.StateTerminated},
		{From: affsm.StateFailed, Event: affsm.EvDeleteRequest, To: affsm.StateTerminated},
		// Early-abort: allow Delete from AuthPending if the AF
		// cancels before Authorize/Reject lands.
		{From: affsm.StateAuthPending, Event: affsm.EvDeleteRequest, To: affsm.StateTerminated},
	})
}

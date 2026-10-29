// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Per-UE GMM state machine — the declarative transition graph used by
// fsm.FSM. Each row captures a single spec-level arrow from TS 24.501
// §5.4 / §5.5; first matching (From, Event, Guard) fires.
//
// Actions live in fsm_actions.go. The dispatcher (gmm/dispatch.go) is
// the single entry point that turns inbound NAS bytes into Fire() calls;
// NGAP-side modules (initialctxsetup, uectxrelease) also call
// fsm.Of(ue).Fire when their own outcomes advance the GMM FSM.
// 5GMM transition graph — every row is sourced from a TS 24.501 clause;
// the comment above each row names the clause so the reader can verify
// the transition against the PDF in specs/3gpp/.
// Nothing in this file is derived from observed tester behaviour; if
// a row fires "unexpectedly" in a live run, check the spec first and
// the table second, not the tester's timing.
package gmm

import (
	"time"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
)

// periodicRegTimer is the AMF-side T3512 value (TS 24.501 §5.3.7). The
// spec value is 6h; the Python reference adds a 4-minute margin so the
// UE has slack before the AMF marks it implicitly deregistered.
var periodicRegTimer = timers.T3512 + 4*time.Minute

// gmmTransitions is the full 5GMM AMF-side graph. Order matters only
// when two rows share (From, Event); among tied rows the first whose
// Guard returns true fires.
var gmmTransitions = []fsm.Transition{
	// ══════════════════════════════════════════════════════════════════
	// From DEREGISTERED
	// ══════════════════════════════════════════════════════════════════

	// TS 24.501 §5.5.1.2.2 — Initial Registration Request.
	//   Source:  RM-DEREGISTERED
	//   Trigger: RegistrationRequest from UE
	//   Action:  AMF initiates Authentication procedure (§5.4.1)
	//   T3560:   §5.4.1 AUTH REQUEST retransmit (Table 10.2.1 N3560=4).
	//            Earlier code labelled this T3516 — that timer is UE-side
	//            per §10.2.2. AMF's auth guard is T3560, reused for SMC.
	{
		From:   fsm.StateDeregistered,
		Event:  fsm.EvRegistrationRequest,
		To:     fsm.StateAuthentication,
		Action: actEnterAuthentication,
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},

	// TS 24.501 §4.4 — Re-registration with a cached 5G NAS security
	// context. Spec (v19.6.2 §4.4):
	//
	//   "The 5G NAS security context which is indicated by an ngKSI can
	//   be taken into use to establish the secure exchange of NAS
	//   messages when a new N1 NAS signalling connection is established
	//   without executing a new primary authentication and key agreement
	//   procedure."
	//
	// When the UE presents an ngKSI that matches a valid native context
	// cached at the AMF for this SUPI we skip both primary auth (§5.4.1)
	// and SMC (§5.4.2) and go directly to Registration Accept
	// (§5.5.1.2.4). KgNB is re-derived from the cached KAMF with the
	// current UL NAS COUNT per TS 33.501 §A.9 before ICS ships the
	// SecurityKey IE.
	//
	// FSM-layer detail: dispatch no longer pre-fires EvRegistrationRequest
	// (see the "handler-driven events" note in gmm/dispatch.go). The RR
	// handler runs in StateDeregistered, evaluates canReuseCachedContext
	// + retroactive MAC verify, and if reuse succeeds calls
	// sendRegistrationAcceptReusedContext which fires this event — so the
	// source state is StateDeregistered (NOT StateAuthentication). A
	// previous version sourced from StateAuthentication; the Fire was
	// rejected silently, mirrorToUECtx never set RM=REGISTERED, the
	// subsequent REGISTRATION COMPLETE hit the allowedIn guard in state
	// DEREGISTERED and was dropped, and on UE-ctx release the cleanup
	// removed the UE (RM=DEREGISTERED) — killing the TMSI-cache the next
	// registration attempt (airplane-mode off) relied on.
	//   T3550: §5.5.1.2.8 Registration Accept retransmit guard.
	{
		From:   fsm.StateDeregistered,
		Event:  fsm.EvRegRequestContextValid,
		To:     fsm.StateRegisteredInitiated,
		Action: actSendRegistrationAcceptReused,
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3550",
			Duration:      timers.T3550,
			OnExpiry:      fsm.EvT3550Expired,
			Retransmit:    retransmitLastNAS("T3550"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Registration Accept retransmit guard (TS 24.501 §5.5.1.2.4)",
			Awaiting:      "Registration Complete from UE",
		}},
	},

	// TS 24.501 §5.5.1.3.4 "Mobility and periodic registration update
	// accepted by the network" — when the UE arrives via 5G-S-TMSI on a
	// new N1 NAS connection while still RM-REGISTERED at the AMF, the
	// reuse path skips auth + SMC per §4.4 but still issues a fresh
	// REGISTRATION ACCEPT (with new 5G-GUTI per buildAssigned5GGUTI).
	// Per §5.5.1.2.4 verbatim — "If the REGISTRATION ACCEPT message
	// contained a 5G-GUTI, the UE shall return a REGISTRATION
	// COMPLETE message to the AMF" — so the AMF MUST arm T3550 and
	// enter 5GMM-COMMON-PROCEDURE-INITIATED (§5.1.3.2.3.3) until Reg
	// Complete arrives. Without this row, the GMM FSM stayed at
	// StateRegistered, T3550 was never armed, and the UE's
	// Registration Complete was dropped by the §5.1.3 state guard.
	//
	// StopTimers cancels the steady-state T3512 (§5.3.7 implicit
	// dereg) so it doesn't fire while we're awaiting Reg Complete; it
	// is re-armed on the EvRegistrationComplete row at line 287.
	{
		From:       fsm.StateRegistered,
		Event:      fsm.EvRegRequestContextValid,
		To:         fsm.StateRegisteredInitiated,
		Action:     actSendRegistrationAcceptReused,
		StopTimers: []string{"T3512"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3550",
			Duration:      timers.T3550,
			OnExpiry:      fsm.EvT3550Expired,
			Retransmit:    retransmitLastNAS("T3550"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Registration Accept retransmit guard — Mobility RR via §4.4 reuse (TS 24.501 §5.5.1.3.4 + §5.5.1.2.4)",
			Awaiting:      "Registration Complete from UE",
		}},
	},

	// AMF chooses to run Identification first (SUCI couldn't resolve to
	// SUPI) — §5.4.4. Handler sendIdentityRequest fires this event on
	// entry; FSM arms T3570.
	{
		From:   fsm.StateDeregistered,
		Event:  fsm.EvIdentityRequestSent,
		To:     fsm.StateIdentification,
		Action: actEnterIdentification,
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3570",
			Duration:      timers.T3570,
			OnExpiry:      fsm.EvT3570Expired,
			Retransmit:    retransmitLastNAS("T3570"),
			MaxRetransmit: timers.NASMaxRetransmit, // TS 24.501 §10.2 N3570=4
			Description:   "Identity Request retransmit guard (TS 24.501 §5.4.3)",
			Awaiting:      "Identity Response from UE",
		}},
	},

	// ══════════════════════════════════════════════════════════════════
	// From IDENTIFICATION (AMF sent Identity Request)
	// ══════════════════════════════════════════════════════════════════

	{
		From:       fsm.StateIdentification,
		Event:      fsm.EvIdentityResponse,
		To:         fsm.StateAuthentication,
		Action:     actEnterAuthentication,
		StopTimers: []string{"T3570"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},
	{
		From:   fsm.StateIdentification,
		Event:  fsm.EvT3570Expired,
		To:     fsm.StateDeregistered,
		Action: actOnIdentityTimeout,
	},
	// TS 24.501 §5.4.3.5 b + §5.5.1.2.5 — IDENTITY RESPONSE arrived but the
	// carried identity could not be resolved to a SUPI (UE sent "No
	// identity", SUCI profile unavailable, decode failure). Handler fires
	// EvIdentityResponseFail after sending REGISTRATION REJECT cause #9
	// "UE identity cannot be derived by the network".
	{
		From:       fsm.StateIdentification,
		Event:      fsm.EvIdentityResponseFail,
		To:         fsm.StateDeregistered,
		Action:     actOnAuthenticationRejected, // reuses the generic "aborted" logger
		StopTimers: []string{"T3570"},
	},

	// ══════════════════════════════════════════════════════════════════
	// From AUTHENTICATION
	// ══════════════════════════════════════════════════════════════════

	// TS 24.501 §5.4.1.3.2 — AMF receives AUTHENTICATION RESPONSE.
	// Outcome split is handler-driven (gmm/dispatch.go no longer fires a
	// generic event; handleAuthenticationResponse decodes RES* itself and
	// fires the outcome-specific event):
	//
	//   EvAuthResponseValid   — RES* == XRES* (Table 8.2.2.1.1) →
	//                           proceed to Security Mode Control (§5.4.2).
	//                           T3560 auth-leg cancelled; T3560 re-armed
	//                           for the SMC leg. Same timer name per
	//                           spec — arm/stop by name works because
	//                           StopTimers runs before StartTimers.
	//   EvAuthResponseInvalid — RES* mismatch / missing Response Parameter
	//                           / decode failure. AMF SHALL send
	//                           AUTHENTICATION REJECT (§5.4.1.3.6) and
	//                           abort registration. Handler sends the
	//                           reject + removes the ctx; this row just
	//                           records the state transition.
	{
		From:       fsm.StateAuthentication,
		Event:      fsm.EvAuthResponseValid,
		To:         fsm.StateSecurityMode,
		Action:     actEnterSecurityMode,
		StopTimers: []string{"T3560"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560SMCExpired,
			Retransmit:    retransmitLastNAS("T3560-smc"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Security Mode Command retransmit guard (TS 24.501 §5.4.2.2)",
			Awaiting:      "Security Mode Complete from UE",
		}},
	},
	{
		From:       fsm.StateAuthentication,
		Event:      fsm.EvAuthResponseInvalid,
		To:         fsm.StateDeregistered,
		Action:     actOnAuthenticationRejected,
		StopTimers: []string{"T3560"},
	},
	// TS 24.501 §5.4.1.3.7 c/d/e/f — AUTH FAILURE retry path.
	// handleAuthenticationFailure fires EvAuthRetry when cause 20/21/26/71
	// triggers re-running startAuthentication with a fresh AV (or rotated
	// ngKSI). Self-loop swaps T3560 so the new Auth Request gets its own
	// retransmit guard. The terminal-reject path (retry-limit reached /
	// unrecoverable cause) stays on the EvAuthenticationFailure row below.
	{
		From:       fsm.StateAuthentication,
		Event:      fsm.EvAuthRetry,
		To:         fsm.StateAuthentication,
		Action:     actEnterAuthentication,
		StopTimers: []string{"T3560"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},
	// AUTHENTICATION FAILURE (§5.4.1.3.2). Full re-sync via AUTS is a
	// later port; for now any failure is terminal.
	{
		From:       fsm.StateAuthentication,
		Event:      fsm.EvAuthenticationFailure,
		To:         fsm.StateDeregistered,
		Action:     actOnAuthenticationFailure,
		StopTimers: []string{"T3560"},
	},
	// T3560 auth-leg final expiry — retransmit hook already shot
	// N3560 times; now abandon the procedure.
	{
		From:   fsm.StateAuthentication,
		Event:  fsm.EvT3560AuthExpired,
		To:     fsm.StateDeregistered,
		Action: actOnAuthenticationTimeout,
	},

	// ══════════════════════════════════════════════════════════════════
	// From SECURITY_MODE
	// ══════════════════════════════════════════════════════════════════

	{
		From:       fsm.StateSecurityMode,
		Event:      fsm.EvSecurityModeComplete,
		To:         fsm.StateRegisteredInitiated,
		Action:     actFinaliseRegistration,
		StopTimers: []string{"T3560"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3550",
			Duration:      timers.T3550,
			OnExpiry:      fsm.EvT3550Expired,
			Retransmit:    retransmitLastNAS("T3550"),
			MaxRetransmit: timers.NASMaxRetransmit, // TS 24.501 §10.2 N3550=4
			Description:   "Registration Accept retransmit guard (TS 24.501 §5.5.1.2.4)",
			Awaiting:      "Registration Complete from UE",
		}},
	},
	{
		From:       fsm.StateSecurityMode,
		Event:      fsm.EvSecurityModeReject,
		To:         fsm.StateDeregistered,
		Action:     actOnSecurityModeReject,
		StopTimers: []string{"T3560"},
	},
	{
		From:   fsm.StateSecurityMode,
		Event:  fsm.EvT3560SMCExpired,
		To:     fsm.StateDeregistered,
		Action: actOnSecurityModeTimeout,
	},

	// ══════════════════════════════════════════════════════════════════
	// From REGISTERED_INITIATED
	// ══════════════════════════════════════════════════════════════════

	{
		From:        fsm.StateRegisteredInitiated,
		Event:       fsm.EvRegistrationComplete,
		To:          fsm.StateRegistered,
		Action:      actOnRegistrationComplete,
		StopTimers:  []string{"T3550"},
		StartTimers: []fsm.TimerSpec{{
			Name:        "T3512",
			Duration:    periodicRegTimer,
			OnExpiry:    fsm.EvT3512Expired,
			Description: "Periodic Registration Update timer (TS 24.501 §5.3.7)",
			Awaiting:    "next periodic Registration from UE",
		}},
	},
	// TS 24.501 §5.5.1.2.8 c — T3550 final expiry:
	//   "on the fifth expiry of timer T3550, the registration procedure
	//    for initial registration shall be aborted and the AMF enters
	//    state 5GMM-REGISTERED. If a new 5G-GUTI was allocated in the
	//    REGISTRATION ACCEPT message, the AMF shall consider both the
	//    old and the new 5G-GUTIs as valid until the old 5G-GUTI can
	//    be considered as invalid by the AMF or the 5GMM context which
	//    has been marked as de-registered in the AMF is released."
	//
	// Enter REGISTERED (NOT DEREGISTERED — the UE almost certainly
	// received one of the 4 Accept transmits and is registered on its
	// side; it just failed to send Registration Complete).
	//
	// TODO(spec: TS 24.501 §5.5.1.2.8 c) — "both 5G-GUTIs valid" not
	//   modelled: we don't track an "old 5G-GUTI" alongside the new
	//   one, so if the UE uses its old GUTI in the next message we
	//   lose lookup. Add an `OldTMSI5G uint32` field to uectx and
	//   keep-both until the next successful registration clears it.
	{
		From:   fsm.StateRegisteredInitiated,
		Event:  fsm.EvT3550Expired,
		To:     fsm.StateRegistered,
		Action: actOnRegistrationAcceptTimeout,
		// StartTimers T3512 so the implicit-dereg clock starts; mirrors
		// the successful Registration Complete path.
		StartTimers: []fsm.TimerSpec{{
			Name:        "T3512",
			Duration:    periodicRegTimer,
			OnExpiry:    fsm.EvT3512Expired,
			Description: "Periodic Registration Update timer (TS 24.501 §5.3.7)",
			Awaiting:    "next periodic Registration from UE",
		}},
	},

	// ══════════════════════════════════════════════════════════════════
	// From REGISTERED (steady state)
	// ══════════════════════════════════════════════════════════════════

	{From: fsm.StateRegistered, Event: fsm.EvULNASTransport, To: fsm.StateRegistered, Action: actNoopREGISTERED},
	{From: fsm.StateRegistered, Event: fsm.EvServiceRequest, To: fsm.StateRegistered, Action: actNoopREGISTERED},
	{From: fsm.StateRegistered, Event: fsm.EvConfigUpdateComplete, To: fsm.StateRegistered, Action: actNoopREGISTERED},
	{From: fsm.StateRegistered, Event: fsm.EvStatus5GMM, To: fsm.StateRegistered, Action: actNoopREGISTERED},

	// TS 24.501 §5.5.1.2.8 d / e / f — RegistrationRequest can legitimately
	// arrive mid-procedure (duplicate with differing IEs) OR after the UE
	// is already registered. Spec behaviour: abort current procedure +
	// run common procedures for the new RR. One transition per state,
	// all converging on StateAuthentication with T3560 armed. The
	// StopTimers list drops whichever NAS timer was previously armed
	// (T3512 steady-state, T3550 reg-accept-retx, T3560 auth/smc-retx,
	// T3570 identity-retx) — the caller's abortCurrentProcedure also
	// wipes the per-UE timers defensively.
	{
		From:       fsm.StateRegistered,
		Event:      fsm.EvRegistrationRequest,
		To:         fsm.StateAuthentication,
		Action:     actEnterAuthentication,
		StopTimers: []string{"T3512"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},
	{
		From:       fsm.StateRegisteredInitiated,
		Event:      fsm.EvRegistrationRequest,
		To:         fsm.StateAuthentication,
		Action:     actEnterAuthentication,
		StopTimers: []string{"T3550"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},
	{
		From:       fsm.StateIdentification,
		Event:      fsm.EvRegistrationRequest,
		To:         fsm.StateAuthentication,
		Action:     actEnterAuthentication,
		StopTimers: []string{"T3570"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},
	{
		From:       fsm.StateSecurityMode,
		Event:      fsm.EvRegistrationRequest,
		To:         fsm.StateAuthentication,
		Action:     actEnterAuthentication,
		StopTimers: []string{"T3560"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},
	// Self-loop-restart in StateAuthentication: duplicate RR arrives
	// mid-auth; existing T3560 cancelled then re-armed. Same target
	// state so the handler's own abort-and-restart logic drives the
	// new procedure without double-transitioning.
	{
		From:       fsm.StateAuthentication,
		Event:      fsm.EvRegistrationRequest,
		To:         fsm.StateAuthentication,
		Action:     actEnterAuthentication,
		StopTimers: []string{"T3560"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},

	// AMF-initiated Configuration Update (§5.4.5). Command goes out,
	// FSM arms T3555 with N3555=4 retransmits per Table 10.2.1;
	// Complete cancels it and we're back in REGISTERED.
	{
		From:   fsm.StateRegistered,
		Event:  fsm.EvConfigUpdateCommandSent,
		To:     fsm.StateRegistered,
		Action: actLogConfigUpdateCommandSent,
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3555",
			Duration:      timers.T3555,
			OnExpiry:      fsm.EvT3555Expired,
			Retransmit:    retransmitLastNAS("T3555"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Configuration Update Command retransmit guard (TS 24.501 §5.4.4.2)",
			Awaiting:      "Configuration Update Complete from UE",
		}},
	},
	{
		From:       fsm.StateRegistered,
		Event:      fsm.EvConfigUpdateComplete,
		To:         fsm.StateRegistered,
		Action:     actHandleConfigUpdateComplete,
		StopTimers: []string{"T3555"},
	},
	{
		From:   fsm.StateRegistered,
		Event:  fsm.EvT3555Expired,
		To:     fsm.StateRegistered,
		Action: actLogConfigUpdateTimeout,
	},

	// AMF-initiated Deregistration (§5.5.2.3). AMF sends Dereg Request;
	// T3522 retransmits up to N3522=4 (Table 10.2.1) before final
	// expiry. Accept from UE cancels T3522.
	{
		From:       fsm.StateRegistered,
		Event:      fsm.EvDeregistrationRequestSentMT,
		To:         fsm.StateMTDeregPending,
		Action:     actEnterMTDeregPending,
		StopTimers: []string{"T3512"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3522",
			Duration:      timers.T3522,
			OnExpiry:      fsm.EvT3522Expired,
			Retransmit:    retransmitLastNAS("T3522"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Deregistration Request retransmit guard (TS 24.501 §5.5.2.3)",
			Awaiting:      "Deregistration Accept from UE",
		}},
	},

	// Mobility/periodic update: Registration Request while already REGISTERED.
	{
		From:       fsm.StateRegistered,
		Event:      fsm.EvRegistrationRequest,
		To:         fsm.StateAuthentication,
		Action:     actEnterAuthentication,
		StopTimers: []string{"T3512"},
		StartTimers: []fsm.TimerSpec{{
			Name:          "T3560",
			Duration:      timers.T3560,
			OnExpiry:      fsm.EvT3560AuthExpired,
			Retransmit:    retransmitLastNAS("T3560-auth"),
			MaxRetransmit: timers.NASMaxRetransmit,
			Description:   "Authentication Request retransmit guard (TS 24.501 §5.4.1)",
			Awaiting:      "Authentication Response from UE",
		}},
	},

	// T3512 expiry = implicit dereg (TS 24.501 §5.3.7). No NAS goodbye
	// sent; AMF just drops the UE context next time the gNB references it.
	{
		From:   fsm.StateRegistered,
		Event:  fsm.EvT3512Expired,
		To:     fsm.StateDeregistered,
		Action: actOnImplicitDeregistration,
	},

	// MO Deregistration (§5.5.2.2). T3521 is UE-side (UE waits for its
	// own DEREG ACCEPT); AMF's job is to send the Accept and let NGAP
	// release the UE context. Bound the UE Context Release Complete
	// wait with Twait-ue-ctx-release (impl-specific 10 s, RFC-level
	// safety net — no spec-defined retransmit here).
	{
		From:       fsm.StateRegistered,
		Event:      fsm.EvDeregistrationRequestMO,
		To:         fsm.StateDeregistrationInitiated,
		Action:     actEnterDeregistration,
		StopTimers: []string{"T3512"},
		StartTimers: []fsm.TimerSpec{{
			Name:     "Twait-ue-ctx-release",
			Duration: timers.TWaitUECtxRelease,
			OnExpiry: fsm.EvTwaitDeregReleaseExpired,
		}},
	},

	// ══════════════════════════════════════════════════════════════════
	// From DEREGISTRATION_INITIATED
	// ══════════════════════════════════════════════════════════════════

	{
		From:       fsm.StateDeregistrationInitiated,
		Event:      fsm.EvUEContextReleaseComplete,
		To:         fsm.StateDeregistered,
		Action:     actFinaliseDeregistration,
		StopTimers: []string{"Twait-ue-ctx-release"},
	},
	{
		From:   fsm.StateDeregistrationInitiated,
		Event:  fsm.EvTwaitDeregReleaseExpired,
		To:     fsm.StateDeregistered,
		Action: actFinaliseDeregistration,
	},

	// ══════════════════════════════════════════════════════════════════
	// From MT_DEREG_PENDING
	// ══════════════════════════════════════════════════════════════════
	{
		From:       fsm.StateMTDeregPending,
		Event:      fsm.EvDeregistrationAcceptMT,
		To:         fsm.StateDeregistered,
		Action:     actFinaliseDeregistration,
		StopTimers: []string{"T3522"},
	},
	{
		From:   fsm.StateMTDeregPending,
		Event:  fsm.EvT3522Expired,
		To:     fsm.StateDeregistered,
		Action: actFinaliseDeregistration,
	},
}

func init() {
	fsm.SetDefaultTable(gmmTransitions)
}

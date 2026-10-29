// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import "fmt"

// Event is anything that can drive an FSM transition — inbound NAS messages,
// inbound NGAP responses, and timer expiries. Kept in a single enum so the
// transition table is a uniform lookup keyed on (State, Event).
type Event int

const (
	// Inbound NAS messages from the UE ───────────────────────────────────
	EvRegistrationRequest Event = iota
	// RegistrationRequest where the UE presented an ngKSI matching a
	// valid cached 5G NAS security context at the AMF. Per TS 24.501
	// §4.4 this permits the AMF to take the cached context into use on
	// the new N1 NAS signalling connection WITHOUT a new primary
	// authentication and without an SMC round-trip.
	EvRegRequestContextValid
	EvIdentityResponse
	// Identity Response couldn't resolve to a SUPI (ErrNoSUPI / decode
	// failure). Handler-fired; routes StateIdentification → StateDeregistered
	// with a clear log trail — the previous design silently removed the
	// ctx without leaving an FSM transition record.
	EvIdentityResponseFail
	// Authentication Response outcome split — handler-driven. The FSM
	// table no longer evaluates RES* itself via a Guard; the handler is
	// the decoder-of-record and fires the outcome explicitly:
	//   EvAuthResponseValid   → StateAuthentication → StateSecurityMode
	//   EvAuthResponseInvalid → StateAuthentication → StateDeregistered
	// (handler-side path: decode / MAC-param / RES* mismatch / unexpected-type
	// all collapse to Invalid; ctx removal happens in the handler).
	EvAuthResponseValid
	EvAuthResponseInvalid
	// Auth-leg retry trigger — fired when handleAuthenticationFailure
	// re-runs startAuthentication for cause 20/21/26/71 (TS 24.501
	// §5.4.1.3.7 c/d/e/f). Self-loop on StateAuthentication to swap
	// T3560 (auth-leg) without leaving/re-entering the state.
	EvAuthRetry
	EvAuthenticationFailure
	EvSecurityModeComplete
	EvSecurityModeReject
	EvRegistrationComplete
	EvDeregistrationRequestMO
	EvDeregistrationAcceptMT     // UE → AMF, reply to MT Dereg Request
	EvServiceRequest
	EvULNASTransport
	EvIdentityRequestSent        // AMF → UE — starts T3570 on entry to StateIdentification
	EvConfigUpdateCommandSent    // AMF → UE — starts T3555 on the fly
	EvConfigUpdateComplete
	EvDeregistrationRequestSentMT // AMF → UE — starts T3522 on entry to StateMTDeregPending
	EvStatus5GMM

	// Inbound NGAP responses from the gNB ────────────────────────────────
	EvInitialContextSetupResponse
	EvUEContextReleaseComplete

	// Timer expiries ─────────────────────────────────────────────────────
	// AMF-side timers only (TS 24.501 Table 10.2.1). The Auth-leg and
	// SMC-leg both arm timer T3560, but distinct events let the FSM
	// route each timeout to its own action/log line.
	EvT3550Expired      // Registration Accept retransmit (Table 10.2.1 T3550)
	EvT3512Expired      // Mobile-reachable implicit dereg (AMF impl-specific)
	EvT3513Expired      // Paging procedure — retransmit PAGING (T3513)
	EvT3560AuthExpired  // AUTH REQUEST retransmit exhausted (T3560, auth leg)
	EvT3560SMCExpired   // SECURITY MODE COMMAND retransmit exhausted (T3560, SMC leg)
	EvT3570Expired      // IDENTITY REQUEST retransmit (T3570)
	EvT3522Expired      // MT DEREGISTRATION REQUEST retransmit (T3522)
	EvT3555Expired      // CONFIGURATION UPDATE COMMAND retransmit (T3555)
	// Twait-ue-ctx-release: implementation-specific guard on MO
	// Dereg / Ctx Release. T3521 is UE-side per §10.2.2; the AMF
	// uses a local 10s wait to bound NGAP UE Context Release Complete.
	EvTwaitDeregReleaseExpired
)

// String renders the event name for log lines.
func (e Event) String() string {
	switch e {
	case EvRegistrationRequest:
		return "RegistrationRequest"
	case EvRegRequestContextValid:
		return "RegistrationRequest(CachedContextReused)"
	case EvIdentityResponse:
		return "IdentityResponse"
	case EvIdentityResponseFail:
		return "IdentityResponse(Fail)"
	case EvAuthResponseValid:
		return "AuthenticationResponse(Valid)"
	case EvAuthResponseInvalid:
		return "AuthenticationResponse(Invalid)"
	case EvAuthRetry:
		return "AuthenticationRetry"
	case EvAuthenticationFailure:
		return "AuthenticationFailure"
	case EvSecurityModeComplete:
		return "SecurityModeComplete"
	case EvSecurityModeReject:
		return "SecurityModeReject"
	case EvRegistrationComplete:
		return "RegistrationComplete"
	case EvDeregistrationRequestMO:
		return "DeregistrationRequest(MO)"
	case EvServiceRequest:
		return "ServiceRequest"
	case EvULNASTransport:
		return "ULNASTransport"
	case EvConfigUpdateComplete:
		return "ConfigUpdateComplete"
	case EvStatus5GMM:
		return "5GMMStatus"
	case EvInitialContextSetupResponse:
		return "InitialContextSetupResponse"
	case EvUEContextReleaseComplete:
		return "UEContextReleaseComplete"
	case EvT3550Expired:
		return "T3550Expired"
	case EvT3512Expired:
		return "T3512Expired"
	case EvT3513Expired:
		return "T3513Expired"
	case EvT3560AuthExpired:
		return "T3560AuthExpired"
	case EvT3560SMCExpired:
		return "T3560SMCExpired"
	case EvT3570Expired:
		return "T3570Expired"
	case EvT3522Expired:
		return "T3522Expired"
	case EvT3555Expired:
		return "T3555Expired"
	case EvTwaitDeregReleaseExpired:
		return "TwaitDeregReleaseExpired"
	case EvIdentityRequestSent:
		return "IdentityRequestSent"
	case EvConfigUpdateCommandSent:
		return "ConfigUpdateCommandSent"
	case EvDeregistrationAcceptMT:
		return "DeregistrationAccept(MT)"
	case EvDeregistrationRequestSentMT:
		return "DeregistrationRequestSent(MT)"
	}
	return fmt.Sprintf("Event(%d)", int(e))
}

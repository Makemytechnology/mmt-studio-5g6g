// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import "fmt"

// Event drives a 5GSM transition — inbound NAS / NGAP / PFCP messages,
// handler outcomes (see handler-driven pattern in package docstring),
// and timer expiries.
//
// Pattern mirrors nf/amf/gmm/fsm: handlers complete their work first
// (SMF session.Establish runs IP alloc, UPF select, PFCP N4, NAS
// encoding) and THEN fire the outcome event. That keeps the FSM as
// the authoritative record of what happened, rather than a pre-fired
// gate. Events starting with `Ev<Action>Request` are the "procedure
// begins" markers that advance state to a Pending slot; Ev<…>Accepted
// / Ev<…>Rejected are the outcome events the handler fires when it
// knows the result.
type Event int

const (
	// Inbound 5GSM NAS (UE → AMF → SMF via UL NAS Transport) ───────────
	EvEstablishmentRequest Event = iota
	EvModificationRequest
	EvModificationComplete
	EvModificationReject
	EvReleaseRequest
	EvReleaseComplete
	EvStatus5GSM

	// Outcome events fired by SMF handlers post-work ───────────────────
	//
	// EvEstablishmentAccepted — SMF built the 5GSM Accept and handed
	//   it to the AMF for piggyback in NGAP PDUSessionResourceSetupRequest.
	//   Moves EstablishmentPending → ActivationPending per TS 24.501
	//   §6.4.1.3 "UE-requested PDU session establishment procedure
	//   accepted by the network".
	EvEstablishmentAccepted
	// EvEstablishmentRejected — SMF built the 5GSM Reject (cause per
	//   TS 24.501 §9.11.4.2). Moves EstablishmentPending → Inactive
	//   per §6.4.1.4 "UE-requested PDU session establishment procedure
	//   not accepted by the network". Handler also wipes session state.
	EvEstablishmentRejected
	// EvReleaseCommandSent — AMF/SMF shipped PDU SESSION RELEASE COMMAND
	//   (TS 24.501 §6.4.3.2). Moves Active → ReleasePending and arms
	//   T3592.
	EvReleaseCommandSent

	// Inbound N4 / PFCP (UPF → SMF) ────────────────────────────────────
	//
	// Request-sent events are fired by the SMF right after the N4
	// request goes on the wire; response events are fired when the
	// response arrives. On the synchronous dataplane path today both
	// fire back-to-back inside session.Establish.
	EvPFCPEstablishResponse
	EvPFCPEstablishFailure // TS 29.244 §7.5.3 — N4 negative response
	EvPFCPModifyResponse
	EvPFCPDeleteResponse

	// Inbound NGAP (gNB → AMF → SMF) ────────────────────────────────────
	//
	// Paired success / failure outcome events for each resource op per
	// TS 38.413 §8.2.1.2 (Successful Operation) + §8.2.1.3
	// (Unsuccessful Operation).
	EvResourceSetupResponse
	EvResourceSetupFailure
	EvResourceModifyResponse
	EvResourceModifyFailure
	EvResourceReleaseResponse

	// Timer expiries (TS 24.501 §10.3 / TS 24.008 §11.2) ───────────────
	EvT3591Expired // §10.3 T3591 — Modification Command retransmit (N3591=4)
	EvT3592Expired // §10.3 T3592 — Release Command retransmit (N3592=4)
	EvT3593Expired // PFCP-session activity / keepalive (impl-specific)
)

// String renders the event name for log lines.
func (e Event) String() string {
	switch e {
	case EvEstablishmentRequest:
		return "EstablishmentRequest"
	case EvEstablishmentAccepted:
		return "EstablishmentAccepted"
	case EvEstablishmentRejected:
		return "EstablishmentRejected"
	case EvModificationRequest:
		return "ModificationRequest"
	case EvModificationComplete:
		return "ModificationComplete"
	case EvModificationReject:
		return "ModificationReject"
	case EvReleaseRequest:
		return "ReleaseRequest"
	case EvReleaseCommandSent:
		return "ReleaseCommandSent"
	case EvReleaseComplete:
		return "ReleaseComplete"
	case EvStatus5GSM:
		return "5GSMStatus"
	case EvPFCPEstablishResponse:
		return "PFCPEstablishResponse"
	case EvPFCPEstablishFailure:
		return "PFCPEstablishFailure"
	case EvPFCPModifyResponse:
		return "PFCPModifyResponse"
	case EvPFCPDeleteResponse:
		return "PFCPDeleteResponse"
	case EvResourceSetupResponse:
		return "PDUSessionResourceSetupResponse"
	case EvResourceSetupFailure:
		return "PDUSessionResourceSetupFailure"
	case EvResourceModifyResponse:
		return "PDUSessionResourceModifyResponse"
	case EvResourceModifyFailure:
		return "PDUSessionResourceModifyFailure"
	case EvResourceReleaseResponse:
		return "PDUSessionResourceReleaseResponse"
	case EvT3591Expired:
		return "T3591Expired"
	case EvT3592Expired:
		return "T3592Expired"
	case EvT3593Expired:
		return "T3593Expired"
	}
	return fmt.Sprintf("Event(%d)", int(e))
}

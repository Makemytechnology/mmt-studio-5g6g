// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GSM per-PDU-session transition graph.
//
// Authoritative specs (verified against in-tree PDFs this turn):
//
//   - TS 24.501 §6.1.3 "5GSM sublayer states" (PDF:
//     specs/3gpp/ts_124501v190602p.pdf) — the
//     network-side 5GSM sublayer states that map to StateInactive /
//     StateActive / StateModificationPending / etc.
//
//   - TS 24.501 §6.4.1 "UE-requested PDU session establishment
//     procedure":
//       §6.4.1.2 Initiation — UE sends PDU SESSION ESTABLISHMENT
//         REQUEST (type 193).
//       §6.4.1.3 Accepted by the network — SMF ships PDU SESSION
//         ESTABLISHMENT ACCEPT (type 194).
//       §6.4.1.4 Not accepted — SMF ships PDU SESSION ESTABLISHMENT
//         REJECT (type 195) with a §9.11.4.2 cause value.
//
//   - TS 24.501 §6.4.2 "UE-requested PDU session modification" —
//     T3591 guards the Modification Command retransmit
//     (§10.3 Table 10.3.2 N3591=4).
//
//   - TS 24.501 §6.4.3 "UE-requested PDU session release" and
//     §6.4.4 "Network-requested PDU session release" — T3592 guards
//     the Release Command retransmit (§10.3 Table 10.3.2 N3592=4).
//
//   - TS 23.502 §4.3.2.2.1 "Non-roaming and Roaming with Local
//     Breakout" (PDF: specs/3gpp/ts_123502v190700p.pdf) — the
//     stage-2 call-flow steps 1-20 this table models.
//
//   - TS 38.413 §8.2.1 "PDU Session Resource Setup":
//       §8.2.1.2 Successful Operation — gNB replies with
//         PDUSessionResourceSetupResponse.
//       §8.2.1.3 Unsuccessful Operation — gNB replies with
//         PDUSessionResourceSetupFailure.
//
//   - TS 29.244 §7.5.3 "PFCP Session Establishment Response" —
//     N4 responses carrying a Cause IE; failure drives the FSM
//     to Inactive.
//
// Handler-driven pattern — same as nf/amf/gmm/fsm: session.Establish
// runs its work (IP alloc → UPF select → PFCP N4 → NAS encoding) and
// fires an outcome-specific event at the end; the FSM table below
// captures each (source, outcome) arrow explicitly. No Guard
// functions; the handler has already decided the outcome before it
// fires the event.
package session

import (
	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/smf/session/fsm"
)

var gsmTransitions = []fsm.Transition{
	// ══════════════════════════════════════════════════════════════
	// PDU Session Establishment (TS 24.501 §6.4.1 + TS 23.502 §4.3.2.2)
	// ══════════════════════════════════════════════════════════════

	// §6.4.1.2: UE → PDU SESSION ESTABLISHMENT REQUEST lands the
	// session in ESTABLISHMENT_PENDING while SMF runs steps 4-8 of
	// TS 23.502 §4.3.2.2.1 (SMF selection already done by AMF per
	// §4.3.2.2.3; this is session.Establish invoking PFCP etc.).
	{
		From:   fsm.StateInactive,
		Event:  fsm.EvEstablishmentRequest,
		To:     fsm.StateEstablishmentPending,
		Action: actEnterEstablishmentPending,
	},

	// TS 29.244 §7.5.3: PFCP N4 Session Establishment Response with
	// cause=Request accepted → SMF can ship the 5GSM Accept. Moves
	// to ACTIVATION_PENDING while we wait for the gNB's resource
	// setup response per TS 38.413 §8.2.1.2.
	{
		From:   fsm.StateEstablishmentPending,
		Event:  fsm.EvPFCPEstablishResponse,
		To:     fsm.StateActivationPending,
		Action: actAcceptReadyToShip,
	},
	// TS 29.244 §7.5.3 negative cause (UPF rejected establish): SMF
	// must abort the procedure and reply to the UE with PDU SESSION
	// ESTABLISHMENT REJECT per TS 24.501 §6.4.1.4. State goes back
	// to Inactive and the handler releases the allocated IP.
	{
		From:   fsm.StateEstablishmentPending,
		Event:  fsm.EvPFCPEstablishFailure,
		To:     fsm.StateInactive,
		Action: actEstablishmentFailedAtPFCP,
	},
	// TS 24.501 §6.4.1.4: SMF decides to reject (e.g. DNN not
	// subscribed, no UPF available, NSSAA failed). handler fires
	// EvEstablishmentRejected after sending PDU SESSION
	// ESTABLISHMENT REJECT. No ActivationPending transient.
	{
		From:   fsm.StateEstablishmentPending,
		Event:  fsm.EvEstablishmentRejected,
		To:     fsm.StateInactive,
		Action: actEstablishmentRejected,
	},

	// TS 38.413 §8.2.1.2 Successful Operation: gNB confirms the
	// resource setup → session is fully ACTIVE. The AMF's
	// handleResponse in nf/amf/ngap/pdusetup fires this event after
	// extracting the gNB tunnel TEID + updating the DL FAR.
	{
		From:   fsm.StateActivationPending,
		Event:  fsm.EvResourceSetupResponse,
		To:     fsm.StateActive,
		Action: actActivated,
	},
	// TS 38.413 §8.2.1.3 Unsuccessful Operation: gNB returned a
	// PDUSessionResourceSetupFailure for this session (e.g. Radio
	// Resources Not Available). SMF rolls back: PFCP delete + IP
	// release + 5GSM cause # per §9.11.4.2. State → Inactive.
	{
		From:   fsm.StateActivationPending,
		Event:  fsm.EvResourceSetupFailure,
		To:     fsm.StateInactive,
		Action: actEstablishmentFailedAtNGAP,
	},

	// ══════════════════════════════════════════════════════════════
	// PDU Session Modification (TS 24.501 §6.4.2)
	// ══════════════════════════════════════════════════════════════

	// §6.4.2.3 "UE-requested PDU session modification procedure
	// accepted by the network" — SMF sends PDU SESSION MODIFICATION
	// COMMAND, arms T3591 (§10.3 Table 10.3.2 N3591=4).
	{
		From:        fsm.StateActive,
		Event:       fsm.EvModificationRequest,
		To:          fsm.StateModificationPending,
		Action:      actEnterModificationPending,
		StartTimers: []fsm.TimerSpec{{
			Name:        "T3591",
			Duration:    timers.T3591,
			OnExpiry:    fsm.EvT3591Expired,
			Description: "PDU Session Modification Command wait (TS 24.501 §6.4.2.3, §10.3 N3591=4)",
			Awaiting:    "PDU Session Modification Complete / Reject from UE",
		}},
	},
	// §6.4.2.4 UE replies with PDU SESSION MODIFICATION COMPLETE.
	// T3591 cancelled; state returns to Active.
	{
		From:       fsm.StateModificationPending,
		Event:      fsm.EvModificationComplete,
		To:         fsm.StateActive,
		Action:     actModificationComplete,
		StopTimers: []string{"T3591"},
	},
	// §6.4.2.5 UE replies with PDU SESSION MODIFICATION REJECT
	// (cause per §9.11.4.2). The session stays Active on the
	// network side with the pre-modification parameters.
	{
		From:       fsm.StateModificationPending,
		Event:      fsm.EvModificationReject,
		To:         fsm.StateActive,
		Action:     actModificationRejected,
		StopTimers: []string{"T3591"},
	},
	// §6.4.2.6 T3591 final expiry — SMF abandons the modification
	// and falls back to Active with the pre-modification state.
	{
		From:   fsm.StateModificationPending,
		Event:  fsm.EvT3591Expired,
		To:     fsm.StateActive,
		Action: actOnModificationTimeout,
	},

	// ══════════════════════════════════════════════════════════════
	// PDU Session Release (TS 24.501 §6.4.3 UE-initiated / §6.4.4
	// network-initiated)
	// ══════════════════════════════════════════════════════════════

	// §6.4.3.2 UE-initiated: UE sends PDU SESSION RELEASE REQUEST;
	// SMF acks with PDU SESSION RELEASE COMMAND and arms T3592
	// (§10.3 Table 10.3.2 N3592=4).
	//
	// §6.4.4.2 Network-initiated: SMF sends PDU SESSION RELEASE
	// COMMAND directly (no UE Request). Same ReleasePending state +
	// T3592. EvReleaseCommandSent is fired by the handler after
	// shipping the Command; EvReleaseRequest is fired when the UE
	// asks first.
	{
		From:        fsm.StateActive,
		Event:       fsm.EvReleaseRequest,
		To:          fsm.StateReleasePending,
		Action:      actEnterReleasePending,
		StartTimers: []fsm.TimerSpec{{
			Name:        "T3592",
			Duration:    timers.T3592,
			OnExpiry:    fsm.EvT3592Expired,
			Description: "PDU Session Release Command wait (TS 24.501 §6.4.3.3 / §6.4.4.3, §10.3 N3592=4)",
			Awaiting:    "PDU Session Release Complete from UE",
		}},
	},
	{
		From:        fsm.StateActive,
		Event:       fsm.EvReleaseCommandSent,
		To:          fsm.StateReleasePending,
		Action:      actEnterReleasePending,
		StartTimers: []fsm.TimerSpec{{
			Name:        "T3592",
			Duration:    timers.T3592,
			OnExpiry:    fsm.EvT3592Expired,
			Description: "PDU Session Release Command wait (TS 24.501 §6.4.3.3 / §6.4.4.3, §10.3 N3592=4)",
			Awaiting:    "PDU Session Release Complete from UE",
		}},
	},
	// §6.4.3.4 / §6.4.4.4 UE replies with PDU SESSION RELEASE
	// COMPLETE. Session torn down. Terminal.
	{
		From:       fsm.StateReleasePending,
		Event:      fsm.EvReleaseComplete,
		To:         fsm.StateReleased,
		Action:     actReleased,
		StopTimers: []string{"T3592"},
	},
	// §6.4.3.5 T3592 final expiry — SMF treats as released.
	{
		From:   fsm.StateReleasePending,
		Event:  fsm.EvT3592Expired,
		To:     fsm.StateReleased,
		Action: actReleased,
	},

	// ══════════════════════════════════════════════════════════════
	// Release from non-Active states
	// ══════════════════════════════════════════════════════════════
	//
	// GMM-driven dereg (TS 24.501 §5.5.2 implicit/explicit) tears
	// down a PDU session that hasn't reached ACTIVE yet — e.g.
	// during the establishment window if SCTP aborts or the UE
	// triggers a Release on a half-established session. Accept
	// EvReleaseRequest from any pending state to avoid "no
	// transition" errors on shutdown paths.
	{
		From:   fsm.StateEstablishmentPending,
		Event:  fsm.EvReleaseRequest,
		To:     fsm.StateReleased,
		Action: actReleased,
	},
	{
		From:   fsm.StateActivationPending,
		Event:  fsm.EvReleaseRequest,
		To:     fsm.StateReleased,
		Action: actReleased,
	},
	{
		From:       fsm.StateModificationPending,
		Event:      fsm.EvReleaseRequest,
		To:         fsm.StateReleased,
		Action:     actReleased,
		StopTimers: []string{"T3591"},
	},
}

func init() {
	fsm.SetDefaultTable(gsmTransitions)
}

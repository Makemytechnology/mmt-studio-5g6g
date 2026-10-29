// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NGAP per-UE-association transition graph (TS 38.413 §8).
//
// First matching (From, Event, Guard) row fires. The table is
// exhaustive for the happy-path procedures + the races listed in
// fsm/state.go's package comment.
package ngap

import (
	"github.com/mmt/mmt-studio-core/infra/timers"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
)

var ngapTransitions = []ngapfsm.Transition{
	// ══════════════════════════════════════════════════════════════════
	// From NOT_ESTABLISHED
	// ══════════════════════════════════════════════════════════════════

	// Initial UE Message (§8.1) — gNB announces a new UE association.
	// Stays in NOT_ESTABLISHED until AMF issues Initial Context Setup
	// Request after GMM auth + SMC complete.
	{
		From:   ngapfsm.StateNotEstablished,
		Event:  ngapfsm.EvInitialUEMessage,
		To:     ngapfsm.StateNotEstablished,
		Action: actLogNGAPTransition,
	},

	// UL NAS Transport in NOT_ESTABLISHED is LEGITIMATE traffic — the
	// whole Auth Response / Auth Failure / SMC Complete exchange that
	// happens before Initial Context Setup arrives over UL NAS Transport
	// and the NGAP UE context isn't "established" until ICS Response
	// lands. Self-loop, no warning.
	{From: ngapfsm.StateNotEstablished, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateNotEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateNotEstablished, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateNotEstablished, Action: actLogErrorIndication},

	// TS 38.413 v19.2.0 §8.3.2.1 UE Context Release Request (local PDF
	// specs/3gpp/ts_138413v190200p.pdf):
	//   "The purpose of the UE Context Release Request procedure is
	//    to enable the NG-RAN node to request the release of the UE-
	//    associated logical NG-connection for a UE."
	// And §8.3.3.1 UE Context Release:
	//   "This procedure is used for releasing the logical NG-connection
	//    and the associated UE context both in the NG-RAN node and in
	//    the AMF. It can be initiated by either the AMF or the NG-RAN
	//    node."
	// Neither clause bounds the procedure to an "established" state.
	// In practice the gNB releases whenever radio link failure or
	// user-inactivity timer fires — which can happen during the very
	// NAS procedures that precede ICS (Authentication, SMC). Allow
	// the release triad from NOT_ESTABLISHED so the FSM tracks the
	// transition to CTX_RELEASE_PENDING accurately and doesn't spam
	// "procedure collision" warnings.
	{From: ngapfsm.StateNotEstablished, Event: ngapfsm.EvUECtxReleaseRequest, To: ngapfsm.StateCtxReleasePending, Action: actLogNGAPTransition},
	{From: ngapfsm.StateNotEstablished, Event: ngapfsm.EvUECtxReleaseCommand, To: ngapfsm.StateCtxReleasePending, Action: actLogNGAPTransition},
	{From: ngapfsm.StateNotEstablished, Event: ngapfsm.EvUECtxReleaseComplete, To: ngapfsm.StateReleased, Action: actLogNGAPTransition},

	// Same clauses apply during ICS_PENDING — gNB can abandon the
	// InitialContextSetup exchange by releasing the UE (e.g. radio
	// went away between receiving the ICS Request and acking it).
	{From: ngapfsm.StateICSPending, Event: ngapfsm.EvUECtxReleaseRequest, To: ngapfsm.StateCtxReleasePending, Action: actLogNGAPTransition, StopTimers: []string{"Twait-ICS"}},
	{From: ngapfsm.StateICSPending, Event: ngapfsm.EvUECtxReleaseCommand, To: ngapfsm.StateCtxReleasePending, Action: actLogNGAPTransition, StopTimers: []string{"Twait-ICS"}},

	// AMF sends Initial Context Setup Request → arm Twait-ICS
	// (TS 38.413 §8.3.1 calls this implementation-specific; 30 s gives
	// a loaded gNB headroom to process dozens of parallel ICS Requests
	// under a burst registration).
	{
		From:        ngapfsm.StateNotEstablished,
		Event:       ngapfsm.EvICSRequestSent,
		To:          ngapfsm.StateICSPending,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ICS",
			Duration:    timers.TWaitICSResponse,
			OnExpiry:    ngapfsm.EvTICSResponseExpired,
			Description: "InitialContextSetup response wait (TS 38.413 §8.3.1)",
			Awaiting:    "InitialContextSetupResponse / Failure from gNB",
		}},
	},

	// ══════════════════════════════════════════════════════════════════
	// From ICS_PENDING
	// ══════════════════════════════════════════════════════════════════

	// UL NAS Transport can land any time — gNB forwards whatever the UE
	// sent while the ICS Request was in flight. Stay in ICS_PENDING.
	{From: ngapfsm.StateICSPending, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateICSPending, Action: actLogNGAPTransition},
	{From: ngapfsm.StateICSPending, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateICSPending, Action: actLogErrorIndication},

	// Happy path: gNB acknowledged.
	{
		From:       ngapfsm.StateICSPending,
		Event:      ngapfsm.EvICSResponse,
		To:         ngapfsm.StateEstablished,
		Action:     actOnICSResponse,
		StopTimers: []string{"Twait-ICS"},
	},
	// Failure or timer expiry — drop the association; GMM side will see
	// the NGAP layer go to RELEASED and can abort its own procedures.
	{
		From:       ngapfsm.StateICSPending,
		Event:      ngapfsm.EvICSFailure,
		To:         ngapfsm.StateReleased,
		Action:     actOnICSFailure,
		StopTimers: []string{"Twait-ICS"},
	},
	// TS 38.413 §8.3.1.3: Twait-ICS expired — AMF sends
	// UEContextReleaseCommand (via the SendCtxReleaseCmdHook in
	// actOnICSTimeout) with cause Radio-Connection-With-UE-Lost and
	// waits for ReleaseComplete in CtxReleasePending. Without this
	// step the gNB keeps forwarding UL NAS for the released UE and
	// eventually ships a late ICS Response that collides with the
	// AMF's already-released FSM.
	{
		From:       ngapfsm.StateICSPending,
		Event:      ngapfsm.EvTICSResponseExpired,
		To:         ngapfsm.StateCtxReleasePending,
		Action:     actOnICSTimeout,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},

	// (StateICSPending + EvUECtxReleaseRequest / EvUECtxReleaseCommand
	//  handled earlier in the table — those rows fire first. Historical
	//  duplicate row removed.)

	// ══════════════════════════════════════════════════════════════════
	// From ESTABLISHED (steady state)
	// ══════════════════════════════════════════════════════════════════

	// UL NAS / paging response / error indication — stay Established.
	{From: ngapfsm.StateEstablished, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateEstablished, Event: ngapfsm.EvPagingResponse, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateEstablished, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateEstablished, Action: actLogErrorIndication},

	// AMF sent Paging (§8.6.1) — doesn't move the per-UE FSM; track for
	// observability only.
	{From: ngapfsm.StateEstablished, Event: ngapfsm.EvPagingSent, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},

	// PDU Session Resource Setup / Modify / Release requests — AMF-
	// initiated, so they're "RequestSent" events fired by the pdusetup
	// module at send time.
	{
		From:   ngapfsm.StateEstablished,
		Event:  ngapfsm.EvPDUResourceSetupRequestSent,
		To:     ngapfsm.StateResourceSetupPending,
		Action: actLogNGAPTransition,
	},
	{
		From:   ngapfsm.StateEstablished,
		Event:  ngapfsm.EvPDUResourceModifyRequestSent,
		To:     ngapfsm.StateResourceModifyPending,
		Action: actLogNGAPTransition,
	},
	{
		From:   ngapfsm.StateEstablished,
		Event:  ngapfsm.EvPDUResourceReleaseCommandSent,
		To:     ngapfsm.StateResourceReleasePending,
		Action: actLogNGAPTransition,
	},

	// gNB-initiated release (§8.3.3).
	{
		From:   ngapfsm.StateEstablished,
		Event:  ngapfsm.EvUECtxReleaseRequest,
		To:     ngapfsm.StateCtxReleasePending,
		Action: actSendReleaseCommand,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},
	// AMF-initiated release (e.g. deregistration).
	{
		From:   ngapfsm.StateEstablished,
		Event:  ngapfsm.EvUECtxReleaseCommand,
		To:     ngapfsm.StateCtxReleasePending,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},

	// ══════════════════════════════════════════════════════════════════
	// From RESOURCE_SETUP_PENDING
	// ══════════════════════════════════════════════════════════════════
	//
	// When multiple PDU Session Resource Setup procedures fork in
	// parallel, the FSM sits in RESOURCE_SETUP_PENDING from the first
	// RequestSent until the last Response/Failure returns. The caller
	// uses FSM.TrackResourceSetupRequest / Untrack to ref-count; we
	// only fire the PDUResourceSetupResponse event when the last one
	// returns so we stay in the pending state for the duration.

	// Parallel Resource Setup for a second PDU session on the same UE:
	// self-loop (ref counter in the caller drives when to fire the
	// final Response event; intermediate Requests keep the FSM in this
	// state).
	{From: ngapfsm.StateResourceSetupPending, Event: ngapfsm.EvPDUResourceSetupRequestSent, To: ngapfsm.StateResourceSetupPending, Action: actLogNGAPTransition},
	// UL NAS keeps flowing during Resource Setup.
	{From: ngapfsm.StateResourceSetupPending, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateResourceSetupPending, Action: actLogNGAPTransition},
	{From: ngapfsm.StateResourceSetupPending, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateResourceSetupPending, Action: actLogErrorIndication},

	{
		From:   ngapfsm.StateResourceSetupPending,
		Event:  ngapfsm.EvPDUResourceSetupResponse,
		To:     ngapfsm.StateEstablished,
		Action: actLogNGAPTransition,
	},
	{
		From:   ngapfsm.StateResourceSetupPending,
		Event:  ngapfsm.EvPDUResourceSetupFailure,
		To:     ngapfsm.StateEstablished,
		Action: actLogNGAPTransition,
	},
	// TS 38.413 §8.3.2.1 + §8.3.3.1 — UE Context Release triad is
	// state-agnostic ("can be initiated by either the AMF or the NG-RAN
	// node"). Each resource-*-pending state accepts both
	// UEContextReleaseRequest (gNB-initiated) and UEContextReleaseCommand
	// (AMF-initiated, e.g. mid-procedure deregistration). The
	// actSendReleaseCommand action is just a log-tag — the actual NGAP
	// PDU emission happens in nf/amf/ngap/uectxrelease handlers which
	// also Fire the FSM event.
	{
		From:   ngapfsm.StateResourceSetupPending,
		Event:  ngapfsm.EvUECtxReleaseRequest,
		To:     ngapfsm.StateCtxReleasePending,
		Action: actSendReleaseCommand,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},
	{
		From:   ngapfsm.StateResourceSetupPending,
		Event:  ngapfsm.EvUECtxReleaseCommand,
		To:     ngapfsm.StateCtxReleasePending,
		Action: actLogNGAPTransition,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},

	// ══════════════════════════════════════════════════════════════════
	// From RESOURCE_MODIFY_PENDING / RESOURCE_RELEASE_PENDING
	// ══════════════════════════════════════════════════════════════════
	{From: ngapfsm.StateResourceModifyPending, Event: ngapfsm.EvPDUResourceModifyResponse, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateResourceModifyPending, Event: ngapfsm.EvPDUResourceModifyFailure, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateResourceModifyPending, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateResourceModifyPending, Action: actLogNGAPTransition},
	{From: ngapfsm.StateResourceModifyPending, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateResourceModifyPending, Action: actLogErrorIndication},
	// §8.3.2.1 / §8.3.3.1 state-agnostic release during Modify.
	{
		From:   ngapfsm.StateResourceModifyPending,
		Event:  ngapfsm.EvUECtxReleaseRequest,
		To:     ngapfsm.StateCtxReleasePending,
		Action: actSendReleaseCommand,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},
	{
		From:   ngapfsm.StateResourceModifyPending,
		Event:  ngapfsm.EvUECtxReleaseCommand,
		To:     ngapfsm.StateCtxReleasePending,
		Action: actLogNGAPTransition,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},

	{From: ngapfsm.StateResourceReleasePending, Event: ngapfsm.EvPDUResourceReleaseResponse, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateResourceReleasePending, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateResourceReleasePending, Action: actLogNGAPTransition},
	{From: ngapfsm.StateResourceReleasePending, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateResourceReleasePending, Action: actLogErrorIndication},
	// §8.3.2.1 / §8.3.3.1 state-agnostic release during per-PDU Release.
	{
		From:   ngapfsm.StateResourceReleasePending,
		Event:  ngapfsm.EvUECtxReleaseRequest,
		To:     ngapfsm.StateCtxReleasePending,
		Action: actSendReleaseCommand,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},
	{
		From:   ngapfsm.StateResourceReleasePending,
		Event:  ngapfsm.EvUECtxReleaseCommand,
		To:     ngapfsm.StateCtxReleasePending,
		Action: actLogNGAPTransition,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},

	// ══════════════════════════════════════════════════════════════════
	// From CTX_RELEASE_PENDING
	// ══════════════════════════════════════════════════════════════════
	{
		From:       ngapfsm.StateCtxReleasePending,
		Event:      ngapfsm.EvUECtxReleaseComplete,
		To:         ngapfsm.StateReleased,
		Action:     actOnReleaseComplete,
		StopTimers: []string{"Twait-ue-ctx-release"},
	},
	{
		From:   ngapfsm.StateCtxReleasePending,
		Event:  ngapfsm.EvTwaitUECtxReleaseExpired,
		To:     ngapfsm.StateReleased,
		Action: actOnReleaseTimeout,
	},
	// Duplicate release requests while already releasing — gNB repeated
	// itself after not seeing our Command. Stay in state.
	{From: ngapfsm.StateCtxReleasePending, Event: ngapfsm.EvUECtxReleaseRequest, To: ngapfsm.StateCtxReleasePending, Action: actLogNGAPTransition},
	{From: ngapfsm.StateCtxReleasePending, Event: ngapfsm.EvUECtxReleaseCommand, To: ngapfsm.StateCtxReleasePending, Action: actLogNGAPTransition},
	// UL NAS during release: UE sent it before the RRC release reached
	// the air. Dispatcher may still forward it to GMM; FSM tolerates.
	{From: ngapfsm.StateCtxReleasePending, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateCtxReleasePending, Action: actLogNGAPTransition},
	{From: ngapfsm.StateCtxReleasePending, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateCtxReleasePending, Action: actLogErrorIndication},
	// Any new AMF-side procedure Request while we're releasing is a
	// caller bug — reject explicitly so the WARN fires immediately.
	// (The default "no transition" path would log too, but listing these
	// explicit rows documents which collisions we expect to see.)

	// ══════════════════════════════════════════════════════════════════
	// From ESTABLISHED → Handover (§8.4.2)
	// ══════════════════════════════════════════════════════════════════
	{
		From:        ngapfsm.StateEstablished,
		Event:       ngapfsm.EvHandoverRequired,
		To:          ngapfsm.StateHandoverPreparation,
		Action:      actLogNGAPTransition,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "T-HandoverPrep",
			Duration:    timers.THandoverPrep,
			OnExpiry:    ngapfsm.EvTHandoverPrepExpired,
			Description: "Handover preparation wait (TS 38.413 §8.4.1)",
			Awaiting:    "HandoverCommand / HandoverPreparationFailure from target",
		}},
	},
	// Path-switch variant (Xn-style) — handled inline with a short
	// RequestAck path; no separate Execution state needed.
	{From: ngapfsm.StateEstablished, Event: ngapfsm.EvPathSwitchRequest, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateEstablished, Event: ngapfsm.EvPathSwitchAck, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},

	// ══════════════════════════════════════════════════════════════════
	// From HANDOVER_PREPARATION
	// ══════════════════════════════════════════════════════════════════
	{
		From:       ngapfsm.StateHandoverPreparation,
		Event:      ngapfsm.EvHandoverRequestAck,
		To:         ngapfsm.StateHandoverExecution,
		Action:     actLogNGAPTransition,
		StopTimers: []string{"T-HandoverPrep"},
	},
	{
		From:       ngapfsm.StateHandoverPreparation,
		Event:      ngapfsm.EvHandoverFailure,
		To:         ngapfsm.StateEstablished,
		Action:     actLogNGAPTransition,
		StopTimers: []string{"T-HandoverPrep"},
	},
	{
		From:   ngapfsm.StateHandoverPreparation,
		Event:  ngapfsm.EvTHandoverPrepExpired,
		To:     ngapfsm.StateEstablished,
		Action: actLogNGAPTransition,
	},
	// UL NAS / Error Indication during handover prep — self-loop.
	{From: ngapfsm.StateHandoverPreparation, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateHandoverPreparation, Action: actLogNGAPTransition},
	{From: ngapfsm.StateHandoverPreparation, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateHandoverPreparation, Action: actLogErrorIndication},

	// ══════════════════════════════════════════════════════════════════
	// From HANDOVER_EXECUTION (§8.4.3 / §8.4.6 / §8.4.7)
	// ══════════════════════════════════════════════════════════════════
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvHandoverNotify, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvHandoverFailure, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvUplinkNASTransport, To: ngapfsm.StateHandoverExecution, Action: actLogNGAPTransition},
	// §8.4.6 UL RAN Status Transfer — PDCP SN state flowing source→AMF→target.
	// AMF forwards as DL RAN Status Transfer (§8.4.7). Neither transition
	// changes state — they're relayed during the execution window.
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvUplinkRANStatusTransfer, To: ngapfsm.StateHandoverExecution, Action: actLogNGAPTransition},
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvDownlinkRANStatusTransfer, To: ngapfsm.StateHandoverExecution, Action: actLogNGAPTransition},
	// §8.4.8 DAPS Handover Success — AMF → source informing it the UE has
	// reached the target. Doesn't change state; source clears its DAPS
	// leg on its own (separate N2 context will be released via §8.3.4).
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvHandoverSuccess, To: ngapfsm.StateHandoverExecution, Action: actLogNGAPTransition},
	// §8.4.9 / §8.4.10 UL/DL Early Status Transfer — DAPS variant of the
	// RAN-status relay during conditional handover.
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvUplinkRANEarlyStatusTransfer, To: ngapfsm.StateHandoverExecution, Action: actLogNGAPTransition},
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvDownlinkRANEarlyStatusTransfer, To: ngapfsm.StateHandoverExecution, Action: actLogNGAPTransition},
	// Early Status Transfer is also legal before the UE has entered the
	// execution window — arm it from HandoverPreparation too.
	{From: ngapfsm.StateHandoverPreparation, Event: ngapfsm.EvUplinkRANEarlyStatusTransfer, To: ngapfsm.StateHandoverPreparation, Action: actLogNGAPTransition},
	{From: ngapfsm.StateHandoverPreparation, Event: ngapfsm.EvDownlinkRANEarlyStatusTransfer, To: ngapfsm.StateHandoverPreparation, Action: actLogNGAPTransition},
	// §8.4.5 Cancellation — source gives up during execution.
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvHandoverCancel, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition},
	// §8.4.5 Cancellation — during preparation (target never allocated).
	{From: ngapfsm.StateHandoverPreparation, Event: ngapfsm.EvHandoverCancel, To: ngapfsm.StateEstablished, Action: actLogNGAPTransition,
		StopTimers: []string{"T-HandoverPrep"}},

	// TS 38.413 §8.3.2.1 + §8.3.3.1 — UE Context Release triad is
	// state-agnostic. Handover states are no exception: if the radio
	// drops mid-preparation or mid-execution the source gNB may send
	// UEContextReleaseRequest (often with cause "radio-connection-
	// with-ue-lost"). §8.4.5 Handover Cancellation would be the
	// spec-preferred path when the source initiates abort, but a
	// UEContextReleaseRequest mid-handover is semantically equivalent
	// from the FSM's point of view. Move to CtxReleasePending and
	// cancel any armed handover timer.
	{
		From:        ngapfsm.StateHandoverPreparation,
		Event:       ngapfsm.EvUECtxReleaseRequest,
		To:          ngapfsm.StateCtxReleasePending,
		Action:      actSendReleaseCommand,
		StopTimers:  []string{"T-HandoverPrep"},
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},
	{
		From:        ngapfsm.StateHandoverPreparation,
		Event:       ngapfsm.EvUECtxReleaseCommand,
		To:          ngapfsm.StateCtxReleasePending,
		Action:      actLogNGAPTransition,
		StopTimers:  []string{"T-HandoverPrep"},
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},
	{
		From:        ngapfsm.StateHandoverExecution,
		Event:       ngapfsm.EvUECtxReleaseRequest,
		To:          ngapfsm.StateCtxReleasePending,
		Action:      actSendReleaseCommand,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},
	{
		From:        ngapfsm.StateHandoverExecution,
		Event:       ngapfsm.EvUECtxReleaseCommand,
		To:          ngapfsm.StateCtxReleasePending,
		Action:      actLogNGAPTransition,
		StartTimers: []ngapfsm.TimerSpec{{
			Name:        "Twait-ue-ctx-release",
			Duration:    timers.TWaitUECtxRelease,
			OnExpiry:    ngapfsm.EvTwaitUECtxReleaseExpired,
			Description: "UEContextRelease completion wait (TS 38.413 §8.3.4)",
			Awaiting:    "UEContextReleaseComplete from gNB",
		}},
	},

	// TS 38.413 §8.7.5 Error Indication — sent to report protocol
	// violations; may land in any state, never changes state. Already
	// covered from most states; HandoverExecution was the remaining
	// gap.
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvErrorIndication, To: ngapfsm.StateHandoverExecution, Action: actLogErrorIndication},

	// ══════════════════════════════════════════════════════════════════
	// NG Reset from handover states — end of association.
	// ══════════════════════════════════════════════════════════════════
	{From: ngapfsm.StateHandoverPreparation, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset,
		StopTimers: []string{"T-HandoverPrep"}},
	{From: ngapfsm.StateHandoverExecution, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset},

	// ══════════════════════════════════════════════════════════════════
	// From any state — NG Reset / hard abort
	// ══════════════════════════════════════════════════════════════════
	//
	// NG Reset from the gNB (§8.7.4) resets every UE association on
	// that gNB. The ngap.Reset handler iterates FSMs and fires EvNGReset
	// into each; transitions here move them all to Released.
	{From: ngapfsm.StateNotEstablished, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset},
	{From: ngapfsm.StateICSPending, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset,
		StopTimers: []string{"Twait-ICS"}},
	{From: ngapfsm.StateEstablished, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset},
	{From: ngapfsm.StateResourceSetupPending, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset},
	{From: ngapfsm.StateResourceModifyPending, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset},
	{From: ngapfsm.StateResourceReleasePending, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset},
	{From: ngapfsm.StateCtxReleasePending, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased, Action: actOnNGReset,
		StopTimers: []string{"Twait-ue-ctx-release"}},
	// Silent self-loop: cascadeNGResetForGnb fires EvNGReset on every
	// UE regardless of whether the UE's handler has already released
	// it (Ctx Release Complete, UE-initiated dereg, etc.). Without this
	// row the second fire logs "procedure collision" — cosmetic noise
	// during teardown, not a real error.
	{From: ngapfsm.StateReleased, Event: ngapfsm.EvNGReset, To: ngapfsm.StateReleased},
}

func init() {
	ngapfsm.SetDefaultTable(ngapTransitions)
}

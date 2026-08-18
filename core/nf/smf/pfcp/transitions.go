// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pfcp — SMF-side PFCP session-context FSM, wired against the
// transition graph in TS 29.244 §7.5.2.
//
// The fsm sub-package is the pure engine (State / Event / Transition /
// Fire). This file owns the graph itself plus log-only Action stubs
// and Fire registration. Callers in nf/smf/session drive the FSM from
// the points where they actually talk to the UPF (today: cgo calls
// from establish.go / ReleaseWithCause).
package pfcp

import (
	"github.com/mmt/mmt-studio-core/infra/timers"
	pfcpfsm "github.com/mmt/mmt-studio-core/nf/smf/pfcp/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// pfcpEstabRetx is the per-message PFCP retransmission cadence
// (TS 29.244 §6.4 "Reliable Delivery of PFCP Messages" — default
// 3 seconds, up to 3 retries).
var pfcpEstabRetx = timers.TPFCPRetransmit

var pfcpTransitions = []pfcpfsm.Transition{
	// ── Establishment ─────────────────────────────────────────────────
	{
		From:        pfcpfsm.StateInactive,
		Event:       pfcpfsm.EvEstablishRequestSent,
		To:          pfcpfsm.StateEstablishInProgress,
		Action:      actLog("establish-request-sent"),
		StartTimers: []pfcpfsm.TimerSpec{{
			Name:          "T-PFCP-estab",
			Duration:      pfcpEstabRetx,
			OnExpiry:      pfcpfsm.EvTPFCPEstabExpired,
			MaxRetransmit: timers.NPFCPRetries,
			Description:   "PFCP Session Establishment Request retransmit (TS 29.244 §7.5.2)",
			Awaiting:      "Session Establishment Response from UPF",
		}},
	},
	{
		From:       pfcpfsm.StateEstablishInProgress,
		Event:      pfcpfsm.EvEstablishResponse,
		To:         pfcpfsm.StateEstablished,
		Action:     actLog("established"),
		StopTimers: []string{"T-PFCP-estab"},
	},
	{
		From:       pfcpfsm.StateEstablishInProgress,
		Event:      pfcpfsm.EvEstablishFailure,
		To:         pfcpfsm.StateInactive,
		Action:     actLog("establish-failure"),
		StopTimers: []string{"T-PFCP-estab"},
	},
	{
		From:   pfcpfsm.StateEstablishInProgress,
		Event:  pfcpfsm.EvTPFCPEstabExpired,
		To:     pfcpfsm.StateInactive,
		Action: actLog("establish-timeout"),
	},

	// ── Modification ──────────────────────────────────────────────────
	{
		From:        pfcpfsm.StateEstablished,
		Event:       pfcpfsm.EvModifyRequestSent,
		To:          pfcpfsm.StateModifyInProgress,
		Action:      actLog("modify-request-sent"),
		StartTimers: []pfcpfsm.TimerSpec{{
			Name:          "T-PFCP-mod",
			Duration:      pfcpEstabRetx,
			OnExpiry:      pfcpfsm.EvTPFCPModifyExpired,
			MaxRetransmit: timers.NPFCPRetries,
			Description:   "PFCP Session Modification Request retransmit (TS 29.244 §7.5.4)",
			Awaiting:      "Session Modification Response from UPF",
		}},
	},
	{
		From:       pfcpfsm.StateModifyInProgress,
		Event:      pfcpfsm.EvModifyResponse,
		To:         pfcpfsm.StateEstablished,
		Action:     actLog("modified"),
		StopTimers: []string{"T-PFCP-mod"},
	},
	{
		From:       pfcpfsm.StateModifyInProgress,
		Event:      pfcpfsm.EvModifyFailure,
		To:         pfcpfsm.StateEstablished,
		Action:     actLog("modify-failure"),
		StopTimers: []string{"T-PFCP-mod"},
	},
	{
		From:   pfcpfsm.StateModifyInProgress,
		Event:  pfcpfsm.EvTPFCPModifyExpired,
		To:     pfcpfsm.StateEstablished,
		Action: actLog("modify-timeout"),
	},

	// ── Deletion ──────────────────────────────────────────────────────
	{
		From:        pfcpfsm.StateEstablished,
		Event:       pfcpfsm.EvDeleteRequestSent,
		To:          pfcpfsm.StateDeleteInProgress,
		Action:      actLog("delete-request-sent"),
		StartTimers: []pfcpfsm.TimerSpec{{
			Name:          "T-PFCP-del",
			Duration:      pfcpEstabRetx,
			OnExpiry:      pfcpfsm.EvTPFCPDeleteExpired,
			MaxRetransmit: timers.NPFCPRetries,
			Description:   "PFCP Session Deletion Request retransmit (TS 29.244 §7.5.6)",
			Awaiting:      "Session Deletion Response from UPF",
		}},
	},
	// Delete can also be triggered while Modification is in flight
	// (GMM-driven dereg / NGAP release interrupts a modify).
	{
		From:        pfcpfsm.StateModifyInProgress,
		Event:       pfcpfsm.EvDeleteRequestSent,
		To:          pfcpfsm.StateDeleteInProgress,
		Action:      actLog("delete-request-sent-during-modify"),
		StopTimers:  []string{"T-PFCP-mod"},
		StartTimers: []pfcpfsm.TimerSpec{{
			Name:          "T-PFCP-del",
			Duration:      pfcpEstabRetx,
			OnExpiry:      pfcpfsm.EvTPFCPDeleteExpired,
			MaxRetransmit: timers.NPFCPRetries,
			Description:   "PFCP Session Deletion Request retransmit (TS 29.244 §7.5.6)",
			Awaiting:      "Session Deletion Response from UPF",
		}},
	},
	{
		From:       pfcpfsm.StateDeleteInProgress,
		Event:      pfcpfsm.EvDeleteResponse,
		To:         pfcpfsm.StateInactive,
		Action:     actLog("deleted"),
		StopTimers: []string{"T-PFCP-del"},
	},
	{
		From:   pfcpfsm.StateDeleteInProgress,
		Event:  pfcpfsm.EvTPFCPDeleteExpired,
		To:     pfcpfsm.StateInactive,
		Action: actLog("delete-timeout"),
	},

	// ── UPF-initiated (Session Report) ────────────────────────────────
	//
	// Can arrive in Established OR Modifying; doesn't change state.
	{From: pfcpfsm.StateEstablished, Event: pfcpfsm.EvSessionReport, To: pfcpfsm.StateEstablished, Action: actLog("session-report")},
	{From: pfcpfsm.StateModifyInProgress, Event: pfcpfsm.EvSessionReport, To: pfcpfsm.StateModifyInProgress, Action: actLog("session-report-during-modify")},

	// ── UPF heartbeat loss from any active state ──────────────────────
	{From: pfcpfsm.StateEstablished, Event: pfcpfsm.EvPFCPHeartbeatMiss, To: pfcpfsm.StateInactive, Action: actLog("upf-heartbeat-loss")},
	{From: pfcpfsm.StateModifyInProgress, Event: pfcpfsm.EvPFCPHeartbeatMiss, To: pfcpfsm.StateInactive, Action: actLog("upf-heartbeat-loss-during-modify"),
		StopTimers: []string{"T-PFCP-mod"}},
	{From: pfcpfsm.StateEstablishInProgress, Event: pfcpfsm.EvPFCPHeartbeatMiss, To: pfcpfsm.StateInactive, Action: actLog("upf-heartbeat-loss-during-establish"),
		StopTimers: []string{"T-PFCP-estab"}},
}

func init() {
	pfcpfsm.SetDefaultTable(pfcpTransitions)
}

// actLog returns a log-only Action — follows the convention of Stage 1
// GMM / 5GSM / NGAP FSMs. Procedure bodies stay in their caller
// modules (upfmgr / session.Establish) for now.
func actLog(tag string) pfcpfsm.Action {
	return func(c *pfcpfsm.Context) error {
		logger.Get("smf.pfcp.fsm.action").Debugf("[%s] %s event=%s cause=%d seid=%#x",
			tag, c.Key, c.Event, c.Cause, c.SEID)
		return nil
	}
}

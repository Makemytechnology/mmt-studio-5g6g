// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package shunting — FRMCS shunting group call over MCPTT
// off-network (PC5 / direct-mode).
//
// Shunting operations use a small dedicated group of drivers and
// ground staff coordinating on a siding without relying on a
// trackside MCPTT server. FRMCS reuses the 3GPP MCPTT off-network
// group call for this — each shunting group is one MCPTT off-
// network call running on every participant's radio.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 24.379 §10.2.2     Off-network group call FSM —
//                             7-state machine (S1..S7) with timers
//                             TFG1..TFG6. The shunting group
//                             below wraps mcptt.OffNetCall, which
//                             implements the §10.2.2.3 transitions.
//   - TS 24.379 §10.2.2.4.3 Originating-side procedures (GROUP
//                             CALL PROBE / ANNOUNCEMENT
//                             transmission on PC5). InitiateCall
//                             below drives this through the MCPTT
//                             package.
//   - TS 24.379 §10.2.2.4.6 Merge of off-network calls — out of
//                             scope here (TODO below).
//   - TS 22.289 §4.4.1       Priority stack covering shunting-
//                             relevant traffic (operational urgent
//                             ranks above passenger services); the
//                             FRMCS-side priority assignment for a
//                             shunting group derives from this.
//
// UIC-specific shunting requirements (FRMCS FRS / SRS) are not
// in-tree; aspects like "shunting mode indication", geographic
// fencing, and the PC5 transport selector are flagged with
// TODO(spec: UIC FRS/SRS) below.
//
// TODO(spec: UIC FRS/SRS): "shunting mode indication" — the spec
// requires an explicit indicator that a call is in shunting mode
// (vs a normal off-network group call). The current Snapshot()
// flag set carries shunting_group / local_alias but no mode
// indicator on the wire.
//
// TODO(spec: TS 24.379 §10.2.2.4.6): merge of off-network calls
// (two shunting groups joining when their members overlap). The
// MCPTT package itself flags this as out-of-scope; this package
// will need to coordinate two underlying OffNetCall FSMs once
// MCPTT supports it.
package shunting

import (
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/services/frmcs/common"
	"github.com/mmt/mmt-studio-core/services/mcx/mcptt"
)

var log = logger.Get("frmcs.shunting")

// Group is an FRMCS shunting group: a set of FunctionalAliases
// sharing one off-network MCPTT call.
type Group struct {
	GroupID string
	Members []common.FunctionalAlias

	// call is the underlying MCPTT off-network call FSM. Exactly
	// one member holds the "local" end of this FSM at any time
	// (on a real radio, each handset has its own instance).
	call *mcptt.OffNetCall

	// local is the FunctionalAlias this radio belongs to — used
	// to identify which participant the FSM is tracking.
	local common.FunctionalAlias

	mu       sync.Mutex
	released bool
}

// New creates a shunting group with the given members and binds the
// local MCPTT client (as a FunctionalAlias) to a freshly-created
// §10.2.2 off-network call FSM. cfg configures TFG1..TFG6 timer
// durations (zero fields use the MCPTT package defaults).
//
// SendWire is the PC5 transmission callback; shunting-specific
// payload wrapping (e.g. a UIC "shunting-mode" indicator) belongs
// to the caller's implementation of this function.
func New(groupID string, local common.FunctionalAlias, members []common.FunctionalAlias,
	cfg mcptt.OffNetCallConfig, sendWire mcptt.SendWireFn) *Group {

	call := mcptt.NewOffNetCall(groupID, string(local), cfg, sendWire)
	g := &Group{
		GroupID: groupID,
		Members: append([]common.FunctionalAlias(nil), members...),
		call:    call,
		local:   local,
	}
	log.Infof("FRMCS shunting group %s created: members=%v local=%s", groupID, members, local)
	return g
}

// Release terminates the underlying MCPTT call FSM idempotently.
func (g *Group) Release() {
	g.mu.Lock()
	if g.released {
		g.mu.Unlock()
		return
	}
	g.released = true
	g.mu.Unlock()
	g.call.Stop()
}

// InitiateCall transitions the local MCPTT off-network call FSM
// from S1 → S2 per TS 24.379 §10.2.2.4.3 ("Call setup" — the
// originator sends GROUP CALL PROBE / ANNOUNCEMENT and waits TFG1
// for responses). Returns the resulting state.
func (g *Group) InitiateCall(callID, sdp string) mcptt.OffNetCallState {
	return g.call.InitiateCall(callID, sdp)
}

// ReceiveAnnouncement routes an incoming PC5 GROUP CALL ANNOUNCEMENT
// into the FSM. Per TS 24.379 §10.2.2.4.3 the resulting state
// depends on whether the announcement carries a "confirm
// indication" and whether the spec's "MCPTT user acknowledgement
// required" flag is set (the latter maps to whether the user has
// to make an accept/reject decision before the call enters S3).
func (g *Group) ReceiveAnnouncement(callID string, originator common.FunctionalAlias, sdp string,
	withConfirm, ackRequired bool) mcptt.OffNetCallState {

	return g.call.ReceiveAnnouncement(callID, string(originator), sdp, withConfirm, ackRequired)
}

// Accept — user accepted an incoming call (S4/S5 → S3).
func (g *Group) Accept() mcptt.OffNetCallState { return g.call.AcceptCall() }

// Reject — user rejected an incoming call (S4/S5 → S6).
func (g *Group) Reject() mcptt.OffNetCallState { return g.call.RejectCall() }

// ReleaseCall — user ended the call (S3 → S7).
func (g *Group) ReleaseCall() mcptt.OffNetCallState { return g.call.ReleaseCall() }

// State returns the current MCPTT off-network FSM state.
func (g *Group) State() mcptt.OffNetCallState { return g.call.State() }

// IsMember returns true iff alias is in the configured members list.
func (g *Group) IsMember(alias common.FunctionalAlias) bool {
	for _, m := range g.Members {
		if m == alias {
			return true
		}
	}
	return false
}

// Snapshot wraps the underlying FSM snapshot with shunting-group
// bookkeeping.
func (g *Group) Snapshot() map[string]any {
	out := g.call.Snapshot()
	members := make([]string, len(g.Members))
	for i, m := range g.Members {
		members[i] = string(m)
	}
	out["shunting_group"] = g.GroupID
	out["local_alias"] = string(g.local)
	out["members"] = members
	return out
}

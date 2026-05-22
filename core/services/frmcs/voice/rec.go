// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package voice — FRMCS voice services on top of services/mcx/mcptt.
//
// The Railway Emergency Call (REC) is a broadcast voice call
// initiated by a driver or controller in an emergency. It inherits
// from the GSM-R REC feature and rides on MCPTT as an emergency
// group call.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 22.289 §4.4.1     The §4.4 priority stack and the
//                            "emergency calls established on demand
//                            with priority that guarantees call
//                            success independent of already running
//                            communication services" requirement.
//                            REC realises this requirement in code.
//   - TS 23.289 §4.3.3     QoS requirements for MCPTT — the bearer-
//                            level guarantees the FRMCS REC profile
//                            consumes through the MCPTT plane.
//   - TS 24.379 §6.2.8.1   MCPTT emergency group call conditions.
//   - TS 24.379 §6.2.8.1.1 SIP INVITE / SIP REFER for originating
//                            MCPTT emergency group calls — REC
//                            initiation drives this on the wire.
//   - TS 24.379 §6.2.8.1.2 Resource-Priority header field for
//                            MCPTT emergency group calls — included
//                            whenever the emergency group state is
//                            "MEGC 2: emergency-call-requested" or
//                            "MEGC 3: emergency-call-granted".
//   - TS 24.379 §6.2.8.1.15 numeric Resource-Priority values are
//                            retrieved from the MCPTT service
//                            configuration (TS 24.484), NOT
//                            hard-coded; the priority constant used
//                            here is the internal FloorController
//                            ordering, not the wire value.
//   - TS 24.380 §4.1.1.4   "Determine on-network effective
//                            priority" — REC triggers the
//                            preempt-override outcome of the
//                            §4.1.1.4 decision matrix.
//   - TS 24.380 §6.3.5     Floor server state machine — REC drives
//                            this through the local FloorController
//                            API exposed by services/mcx/mcptt; the
//                            wire-level Floor Request / Granted /
//                            Taken / Idle messages are emitted by
//                            that package, not here.
package voice

import (
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/services/frmcs/common"
	"github.com/mmt/mmt-studio-core/services/mcx/mcptt"
)

var log = logger.Get("frmcs.voice")

// REC represents an in-progress Railway Emergency Call bound to an
// MCPTT floor controller. Per TS 24.379 §6.2.8.1 the call is an
// MCPTT emergency group call (call type "emergency"); this struct
// is the FRMCS-side wrapper that holds the FunctionalAlias of the
// initiator and a handle to the underlying floor controller.
type REC struct {
	CallID    string
	Initiator common.FunctionalAlias
	Floor     *mcptt.FloorController
}

// InitiateREC creates a new emergency floor controller at MCPTT
// emergency priority and grants the floor to the initiator.
//
// Behaviour realises TS 22.289 §4.4.1 (emergency call established
// on demand with priority guaranteeing call success). At the
// MCPTT layer this maps to TS 24.379 §6.2.8.1.1 (originating MCPTT
// emergency group call SIP INVITE) plus TS 24.380 §4.1.1.4
// preempt-override of any active floor holder via the local
// FloorController.
//
// TODO(spec: TS 24.379 §6.2.8.1.1 + §6.2.8.1.2): emit the actual
// MCPTT INVITE with the emergency-namespace Resource-Priority
// header. Today this function only configures the in-process
// FloorController; the SIP signalling needed for an over-the-air
// REC has to flow through services/mcx/mcptt's BuildMCXInvite path
// (which itself has open TODOs for the MCPTT MIME body and
// Resource-Priority — TS 24.379 §6.3.2.2.9 / §6.2.8.1.15).
//
// TODO(spec: TS 24.379 §6.2.8.1.3): in-progress emergency state
// cancellation (re-INVITE) is not modelled — once a REC is up,
// the only way out of "emergency" today is to release the call.
func InitiateREC(callID string, initiator common.FunctionalAlias) *REC {
	fc := mcptt.NewFloorController(callID)
	fc.AddParticipant(string(initiator), mcptt.PriorityEmergency)
	pri := mcptt.PriorityEmergency
	fc.RequestFloor(string(initiator), &pri)
	log.Infof("FRMCS REC initiated: call=%s by alias=%s", callID, initiator)
	return &REC{CallID: callID, Initiator: initiator, Floor: fc}
}

// Join adds a participant to an active REC at emergency priority
// per TS 24.380 §4.1.1.4 (joiner inherits the emergency
// effective-priority while the call is in the §6.2.8.1 emergency
// state).
func (r *REC) Join(alias common.FunctionalAlias) {
	r.Floor.AddParticipant(string(alias), mcptt.PriorityEmergency)
	log.Infof("FRMCS REC %s: %s joined", r.CallID, alias)
}

// Release tears down the underlying floor controller per TS 24.380
// §6.3.3 (release procedures — floor state machines destroyed at
// MCPTT call release).
//
// TODO(spec: TS 24.379 §6.2.8.1.3): the in-progress emergency
// state cancel path that returns a call to non-emergency without
// tearing it down is not implemented; Release() always tears
// down. §6.2.8.1.3 needs a new method here that emits the
// SIP re-INVITE without dropping the FloorController.
func (r *REC) Release() {
	if r.Floor != nil {
		r.Floor.Stop()
	}
}

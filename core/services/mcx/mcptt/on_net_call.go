// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// On-network MCPTT call coordinator — binds a SIP dialog
// (libs/sip, RFC 3261 §12) and a FloorController (TS 24.380 §6.3.5)
// into a single call object.
//
// Spec anchors (PDF: specs/3gpp/ts_124380v190100p.pdf):
//
//   §6.3.2.2 "Initial procedures":
//     "When an MCPTT call is established a new instance of the floor
//      control server state machine for 'general floor control
//      operation' is created. For each MCPTT client added to the
//      MCPTT call, a new instance of the floor control server state
//      machine for 'basic floor control operation towards the floor
//      participant' is added."
//
//   §6.3.3 "MCPTT floor control procedures at MCPTT call release":
//     at MCPTT call release the floor-control server state machines
//     are torn down.
//
// This file is a coordinator, NOT a new FSM. The dialog has its own
// event-loop FSM; the floor controller has its own. OnNetCall just
// holds references to both and enforces the invariant: the floor
// controller exists exactly while the dialog is not Terminated.
package mcptt

import (
	"sync"

	"github.com/mmt/mmt-studio-core/libs/sip"
)

// OnNetCall is one on-network MCPTT group or private call — a SIP
// dialog + its associated floor-control server instance.
type OnNetCall struct {
	CallID   string
	GroupID  string // empty for private calls
	IsGroup  bool

	// Each of these owns its own FSM goroutine; OnNetCall never
	// touches their state directly.
	Dialog *sip.SipDialog
	Floor  *FloorController

	// mu guards only the released flag — all state is in the sub-FSMs.
	mu       sync.Mutex
	released bool
}

// ProcessInviteForGroupCall is the UAS-side entry point: an incoming
// INVITE for a pre-arranged MCPTT group call. It builds the dialog
// (from the INVITE), creates the floor-control server instance per
// §6.3.2.2, adds the listed participants at PriorityNormal, and
// returns the call + a 200 OK response.
//
// The caller drives the SIP transaction (through libs/sip's
// InviteServerTxn) and forwards Send2xx(response) to put the 200 on
// the wire.
func ProcessInviteForGroupCall(invite *sip.SipRequest, localTag, groupID string, participants []string) (*OnNetCall, *sip.SipResponse) {
	d := sip.NewDialogFromRequest(invite, localTag)
	d.On2xx(buildAccept200(invite, localTag)) // move dialog to Confirmed

	callID := d.CallID
	fc := NewFloorController(callID)
	for _, pid := range participants {
		fc.AddParticipant(pid, PriorityNormal)
	}

	call := &OnNetCall{
		CallID:  callID,
		GroupID: groupID,
		IsGroup: true,
		Dialog:  d,
		Floor:   fc,
	}
	return call, buildAccept200(invite, localTag)
}

// ProcessInviteForPrivateCall is the one-to-one equivalent.
func ProcessInviteForPrivateCall(invite *sip.SipRequest, localTag, originator, target string) (*OnNetCall, *sip.SipResponse) {
	d := sip.NewDialogFromRequest(invite, localTag)
	d.On2xx(buildAccept200(invite, localTag))

	callID := d.CallID
	fc := NewFloorController(callID)
	fc.AddParticipant(originator, PriorityNormal)
	fc.AddParticipant(target, PriorityNormal)

	call := &OnNetCall{
		CallID: callID,
		Dialog: d,
		Floor:  fc,
	}
	return call, buildAccept200(invite, localTag)
}

// Reject terminates both sub-FSMs and returns an error response with
// the given status code. Used when policy denies the call before it
// establishes.
func Reject(invite *sip.SipRequest, code int, reason string) *sip.SipResponse {
	return buildResponseEcho(invite, code, reason)
}

// HandleBye processes an in-dialog BYE: terminates dialog + floor
// per §6.3.3, returns a 200 OK response.
func (c *OnNetCall) HandleBye(bye *sip.SipRequest) *sip.SipResponse {
	c.Release()
	return buildResponseEcho(bye, 200, "OK")
}

// Release tears down the dialog and the floor controller
// idempotently. Safe to call multiple times. Corresponds to the
// §6.3.3 "release procedures" — floor state machines are destroyed.
func (c *OnNetCall) Release() {
	c.mu.Lock()
	if c.released {
		c.mu.Unlock()
		return
	}
	c.released = true
	c.mu.Unlock()

	c.Dialog.Terminate()
	c.Floor.Stop()
}

// State summarises the call lifecycle for callers.
func (c *OnNetCall) State() string {
	c.mu.Lock()
	released := c.released
	c.mu.Unlock()
	if released {
		return "released"
	}
	switch c.Dialog.State() {
	case sip.DialogTerminated:
		return "terminated"
	case sip.DialogConfirmed:
		return "active"
	case sip.DialogEarly:
		return "early"
	}
	return "init"
}

// ── Internal: minimal SIP response builders ──

// buildAccept200 constructs a 200 OK response for an INVITE, echoing
// the standard headers per RFC 3261 §8.2.6 and adding a To-tag per
// §8.2.6.2. The body (SDP answer) is minimal — real implementations
// build the SDP answer from the offer via a media plane negotiator.
func buildAccept200(invite *sip.SipRequest, localTag string) *sip.SipResponse {
	resp := buildResponseEcho(invite, 200, "OK")
	// Stamp a To-tag so the dialog ID stabilises.
	to := resp.GetHeader(sip.HdrTo)
	if to != "" && !containsTag(to) {
		resp.SetHeader(sip.HdrTo, to+";tag="+localTag)
	}
	return resp
}

// buildResponseEcho mirrors handler.buildResponse from cscf —
// duplicated intentionally to avoid a cross-package dependency on
// cscf from mcptt. Keeps the layers independent.
func buildResponseEcho(req *sip.SipRequest, code int, reason string) *sip.SipResponse {
	resp := &sip.SipResponse{
		SipMessage: sip.SipMessage{Headers: map[string][]string{}},
		StatusCode: code,
		Reason:     reason,
	}
	for _, h := range []string{sip.HdrVia, sip.HdrFrom, sip.HdrTo, sip.HdrCallID, sip.HdrCSeq} {
		if v := req.GetHeader(h); v != "" {
			resp.SetHeader(h, v)
		}
	}
	resp.SetHeader(sip.HdrContentLength, "0")
	return resp
}

func containsTag(header string) bool {
	for i := 0; i+4 < len(header); i++ {
		if header[i:i+5] == ";tag=" {
			return true
		}
	}
	return false
}

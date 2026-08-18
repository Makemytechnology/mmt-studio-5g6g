// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Off-network MCPTT group call control — TS 24.379 §10.2.2.
// PDF: specs/3gpp/ts_124379v190600p.pdf.
//
// 7-state per-group, per-user FSM defined in §10.2.2.3. Drives the
// PC5 call-setup exchange used by MCPTT clients without a network-
// side MCPTT server (ProSe / direct-mode / FRMCS off-network
// shunting). Each per-group call state machine is implemented as
// one libs/fsm.Machine — the handler is the only mutator of state
// and of the six timers TFG1..TFG6.
//
// States (§10.2.2.3):
//
//   S1  start-stop                                    (§10.2.2.3.1)
//   S2  waiting for call announcement                  (§10.2.2.3.2)
//   S3  part of ongoing call                           (§10.2.2.3.3)
//   S4  pending user action without confirm indication (§10.2.2.3.4)
//   S5  pending user action with confirm indication    (§10.2.2.3.5)
//   S6  ignoring incoming call announcements           (§10.2.2.3.6)
//   S7  waiting for call announcement after call release(§10.2.2.3.7)
//
// Transitions modeled (arrows taken from Figure 10.2.2.2-1):
//
//   S1 + U:initiate                       → S2  (caller originates)
//   S1 + R:Announcement (ack-not-reqd)    → S3  (immediate join)
//   S1 + R:Announcement (no-confirm)      → S4
//   S1 + R:Announcement (with-confirm)    → S5
//   S2 + R:Announcement / TFG1 expiry     → S3
//   S3 + U:release / TFG6 expiry          → S7
//   S4/S5 + U:accept                      → S3
//   S4/S5 + U:reject / release / TFG4     → S6
//   S5 + TFG5 expiry                      → S6
//   S7 + TFG3 expiry / other qualifiers   → S1
//   S6 + R:Announcement                   → S6   (absorbed)
//
// Timer durations (TFG1..TFG6) are operator-configurable in the
// spec (§10.2.2.4.1.1.2 derives TFG2 from the refresh interval;
// TFG6 from the max-call-duration X). Defaults in this package are
// placeholders — real values must be supplied in OffNetCallConfig
// before production use. Annex F of 24.379 holds the canonical
// ranges; this module consumes whatever the caller configures.
//
// Out of scope for this iteration (will be additive):
//   §10.2.2.4.6 Merge of calls — needs two FSMs to coordinate.
//   §10.2.2.4.7 Error handling — specific error paths.
package mcptt

import (
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/libs/fsm"
)

// ── States ──

type OffNetCallState int

const (
	S1StartStop       OffNetCallState = iota + 1 // §10.2.2.3.1
	S2WaitAnnounce                               // §10.2.2.3.2
	S3InCall                                     // §10.2.2.3.3
	S4PendingNoConfirm                           // §10.2.2.3.4
	S5PendingConfirm                             // §10.2.2.3.5
	S6Ignoring                                   // §10.2.2.3.6
	S7PostRelease                                // §10.2.2.3.7
)

func (s OffNetCallState) String() string {
	switch s {
	case S1StartStop:
		return "S1:start-stop"
	case S2WaitAnnounce:
		return "S2:waiting-for-announcement"
	case S3InCall:
		return "S3:in-call"
	case S4PendingNoConfirm:
		return "S4:pending-no-confirm"
	case S5PendingConfirm:
		return "S5:pending-with-confirm"
	case S6Ignoring:
		return "S6:ignoring"
	case S7PostRelease:
		return "S7:post-release"
	}
	return "?"
}

// ── Events ──

// User-driven ("U:" in the spec) + received-on-wire ("R:") + timers.
const (
	oncEvUInitiate = iota // U:initiate call
	oncEvUAccept          // U:call accepted (S4/S5 only)
	oncEvUReject          // U:call rejected (S4/S5 only)
	oncEvURelease         // U:release call (S3 only, also usable in S4/S5)

	oncEvRAnnounceAckNotRequired // R:GROUP CALL ANNOUNCEMENT with ack-not-required
	oncEvRAnnounceNoConfirm      // R:GROUP CALL ANNOUNCEMENT without confirm indication
	oncEvRAnnounceWithConfirm    // R:GROUP CALL ANNOUNCEMENT with confirm indication
	oncEvRAnnounceGeneric        // other announcements (S2/S6/S7 context)

	oncEvTimerTFG1 // wait-for-announcement expired
	oncEvTimerTFG2 // periodic announcement (not driving state directly)
	oncEvTimerTFG3 // post-release cooldown
	oncEvTimerTFG4 // pending-user-action
	oncEvTimerTFG5 // confirm-indication display
	oncEvTimerTFG6 // max call duration

	oncEvQuerySnapshot
)

const (
	tfg1 = "TFG1"
	tfg2 = "TFG2"
	tfg3 = "TFG3"
	tfg4 = "TFG4"
	tfg5 = "TFG5"
	tfg6 = "TFG6"
)

// ── Config ──

// OffNetCallConfig carries the timer values for a call. Fields left
// at zero use the defaults in defaultOffNetConfig below; those
// defaults are placeholders and should be overridden from operator
// policy (24.379 Annex F) in real deployments.
type OffNetCallConfig struct {
	TFG1 time.Duration // wait for announcement
	TFG2 time.Duration // periodic announcement interval
	TFG3 time.Duration // post-release cooldown
	TFG4 time.Duration // pending user action timeout
	TFG5 time.Duration // confirm-indication display
	TFG6 time.Duration // max call duration
}

var defaultOffNetConfig = OffNetCallConfig{
	TFG1: 3 * time.Second,
	TFG2: 4 * time.Second,
	TFG3: 10 * time.Second,
	TFG4: 15 * time.Second,
	TFG5: 20 * time.Second,
	TFG6: 30 * time.Minute,
}

func (c OffNetCallConfig) withDefaults() OffNetCallConfig {
	d := defaultOffNetConfig
	if c.TFG1 > 0 {
		d.TFG1 = c.TFG1
	}
	if c.TFG2 > 0 {
		d.TFG2 = c.TFG2
	}
	if c.TFG3 > 0 {
		d.TFG3 = c.TFG3
	}
	if c.TFG4 > 0 {
		d.TFG4 = c.TFG4
	}
	if c.TFG5 > 0 {
		d.TFG5 = c.TFG5
	}
	if c.TFG6 > 0 {
		d.TFG6 = c.TFG6
	}
	return d
}

// ── OffNetCall ──

// SendWireFn is invoked when the FSM wants to emit a PC5 message
// (GROUP CALL ANNOUNCEMENT / ACCEPT / PROBE). The implementation
// actually serialises the message to the air interface. Runs on
// the FSM goroutine.
type SendWireFn func(msgType string, payload map[string]any)

// OffNetCall is a per-group, per-user MCPTT off-network call FSM.
type OffNetCall struct {
	GroupID string
	MCPTTID string
	Config  OffNetCallConfig

	// "Pieces of information associated with the state machine"
	// (§10.2.2.2). Written only by the handler.
	callID       string
	refreshMs    int
	sdp          string
	originatorID string

	SendWire SendWireFn

	fsm      *fsm.Machine
	stopOnce sync.Once
}

// NewOffNetCall creates and starts a call FSM in S1. Config zero
// fields take the package defaults (clearly placeholder values —
// see 24.379 Annex F for real numbers).
func NewOffNetCall(groupID, mcpttID string, cfg OffNetCallConfig, send SendWireFn) *OffNetCall {
	c := &OffNetCall{
		GroupID:  groupID,
		MCPTTID:  mcpttID,
		Config:   cfg.withDefaults(),
		SendWire: send,
	}
	c.fsm = fsm.New("onc:"+groupID+":"+mcpttID, S1StartStop, c.handle, 32)
	c.fsm.Start()
	return c
}

func (c *OffNetCall) Stop() { c.stopOnce.Do(func() { c.fsm.Stop() }) }

func (c *OffNetCall) State() OffNetCallState {
	s, _ := c.fsm.State().(OffNetCallState)
	return s
}

// ── Public event wrappers ──

// InitiateCall: U-initiate event from S1. Per §10.2.2.4.3 the client
// transmits GROUP CALL PROBE / ANNOUNCEMENT and moves to S2.
func (c *OffNetCall) InitiateCall(callID, sdp string) OffNetCallState {
	return c.syncEvent(oncEvUInitiate, map[string]any{"call_id": callID, "sdp": sdp})
}

// AcceptCall: U-accept event from S4 or S5 → S3.
func (c *OffNetCall) AcceptCall() OffNetCallState {
	return c.syncEvent(oncEvUAccept, nil)
}

// RejectCall: U-reject event from S4 or S5 → S6.
func (c *OffNetCall) RejectCall() OffNetCallState {
	return c.syncEvent(oncEvUReject, nil)
}

// ReleaseCall: U-release event from S3/S4/S5 → S7 or S6.
func (c *OffNetCall) ReleaseCall() OffNetCallState {
	return c.syncEvent(oncEvURelease, nil)
}

// ReceiveAnnouncement: R-event with the announcement's "confirm
// indication" and "ack required" flags.
func (c *OffNetCall) ReceiveAnnouncement(callID, originator, sdp string,
	withConfirm, ackRequired bool) OffNetCallState {

	var evType int
	switch {
	case !ackRequired:
		evType = oncEvRAnnounceAckNotRequired
	case withConfirm:
		evType = oncEvRAnnounceWithConfirm
	default:
		evType = oncEvRAnnounceNoConfirm
	}
	return c.syncEvent(evType, map[string]any{
		"call_id":    callID,
		"originator": originator,
		"sdp":        sdp,
	})
}

// Snapshot goes through the loop for a consistent view.
func (c *OffNetCall) Snapshot() map[string]any {
	reply := make(chan any, 1)
	c.fsm.Send(fsm.Event{Type: oncEvQuerySnapshot, Reply: reply})
	res, _ := (<-reply).(map[string]any)
	return res
}

func (c *OffNetCall) syncEvent(t int, data map[string]any) OffNetCallState {
	reply := make(chan any, 1)
	c.fsm.Send(fsm.Event{Type: t, Data: data, Reply: reply})
	<-reply
	return c.State()
}

// ── Handler (loop goroutine) ──

func (c *OffNetCall) handle(st fsm.State, ev fsm.Event) fsm.Action {
	cs, _ := st.(OffNetCallState)

	if ev.Type == oncEvQuerySnapshot {
		return fsm.Action{Reply: c.snapshot(cs)}
	}

	switch cs {
	case S1StartStop:
		return c.onS1(ev)
	case S2WaitAnnounce:
		return c.onS2(ev)
	case S3InCall:
		return c.onS3(ev)
	case S4PendingNoConfirm:
		return c.onS4(ev)
	case S5PendingConfirm:
		return c.onS5(ev)
	case S6Ignoring:
		return c.onS6(ev)
	case S7PostRelease:
		return c.onS7(ev)
	}
	return fsm.Action{Reply: nil}
}

// §10.2.2.3.1 / §10.2.2.4.3: S1 accepts U:initiate (caller path) and
// three flavours of R:ANNOUNCEMENT (callee path).
func (c *OffNetCall) onS1(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case oncEvUInitiate:
		// §10.2.2.4.3: client transmits GROUP CALL PROBE /
		// ANNOUNCEMENT and waits TFG1 for responses.
		c.callID, _ = ev.Data["call_id"].(string)
		c.sdp, _ = ev.Data["sdp"].(string)
		c.originatorID = c.MCPTTID
		c.emitWire("GROUP_CALL_ANNOUNCEMENT", map[string]any{
			"call_id":    c.callID,
			"originator": c.MCPTTID,
			"sdp":        c.sdp,
		})
		return fsm.Action{
			Next: S2WaitAnnounce,
			Timers: []fsm.TimerOp{{
				ID: tfg1, After: c.Config.TFG1,
				OnFire: fsm.Event{Type: oncEvTimerTFG1},
			}},
			Reply: true,
		}
	case oncEvRAnnounceAckNotRequired:
		c.absorbAnnouncement(ev)
		return fsm.Action{Next: S3InCall, Timers: c.startTFG6(), Reply: true}
	case oncEvRAnnounceNoConfirm:
		c.absorbAnnouncement(ev)
		return fsm.Action{
			Next: S4PendingNoConfirm,
			Timers: []fsm.TimerOp{{
				ID: tfg4, After: c.Config.TFG4,
				OnFire: fsm.Event{Type: oncEvTimerTFG4},
			}},
			Reply: true,
		}
	case oncEvRAnnounceWithConfirm:
		c.absorbAnnouncement(ev)
		return fsm.Action{
			Next: S5PendingConfirm,
			Timers: []fsm.TimerOp{
				{ID: tfg4, After: c.Config.TFG4, OnFire: fsm.Event{Type: oncEvTimerTFG4}},
				{ID: tfg5, After: c.Config.TFG5, OnFire: fsm.Event{Type: oncEvTimerTFG5}},
			},
			Reply: true,
		}
	}
	return fsm.Action{Reply: true}
}

// §10.2.2.3.2 S2: waiting for announcement after initiating.
func (c *OffNetCall) onS2(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case oncEvRAnnounceAckNotRequired, oncEvRAnnounceNoConfirm, oncEvRAnnounceWithConfirm, oncEvRAnnounceGeneric:
		// Our announcement has been heard (or another party announced).
		// Move to S3 and start the call-duration timer.
		c.absorbAnnouncement(ev)
		return fsm.Action{
			Next: S3InCall,
			Timers: append(c.startTFG6(),
				fsm.TimerOp{ID: tfg1, Cancel: true}),
			Reply: true,
		}
	case oncEvTimerTFG1:
		// §10.2.2.3.2: TFG1 expiry also moves to S3 (we become the
		// announcer ourselves).
		return fsm.Action{
			Next:   S3InCall,
			Timers: c.startTFG6(),
			Reply:  true,
		}
	case oncEvURelease:
		return fsm.Action{
			Next: S7PostRelease,
			Timers: []fsm.TimerOp{
				{ID: tfg1, Cancel: true},
				{ID: tfg3, After: c.Config.TFG3, OnFire: fsm.Event{Type: oncEvTimerTFG3}},
			},
			Reply: true,
		}
	}
	return fsm.Action{Reply: true}
}

// §10.2.2.3.3 S3: in-call. Drives to S7 on release/TFG6.
func (c *OffNetCall) onS3(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case oncEvURelease, oncEvTimerTFG6:
		return fsm.Action{
			Next: S7PostRelease,
			Timers: []fsm.TimerOp{
				{ID: tfg6, Cancel: true},
				{ID: tfg3, After: c.Config.TFG3, OnFire: fsm.Event{Type: oncEvTimerTFG3}},
			},
			Reply: true,
		}
	}
	return fsm.Action{Reply: true}
}

// §10.2.2.3.4 S4: pending accept/reject, no UI confirm required.
func (c *OffNetCall) onS4(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case oncEvUAccept:
		return fsm.Action{
			Next: S3InCall,
			Timers: append(c.startTFG6(),
				fsm.TimerOp{ID: tfg4, Cancel: true}),
			Reply: true,
		}
	case oncEvUReject, oncEvURelease, oncEvTimerTFG4:
		return fsm.Action{
			Next:   S6Ignoring,
			Timers: []fsm.TimerOp{{ID: tfg4, Cancel: true}},
			Reply:  true,
		}
	}
	return fsm.Action{Reply: true}
}

// §10.2.2.3.5 S5: pending, UI confirm required.
func (c *OffNetCall) onS5(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case oncEvUAccept:
		return fsm.Action{
			Next: S3InCall,
			Timers: append(c.startTFG6(),
				fsm.TimerOp{ID: tfg4, Cancel: true},
				fsm.TimerOp{ID: tfg5, Cancel: true}),
			Reply: true,
		}
	case oncEvUReject, oncEvURelease, oncEvTimerTFG4, oncEvTimerTFG5:
		return fsm.Action{
			Next: S6Ignoring,
			Timers: []fsm.TimerOp{
				{ID: tfg4, Cancel: true},
				{ID: tfg5, Cancel: true},
			},
			Reply: true,
		}
	}
	return fsm.Action{Reply: true}
}

// §10.2.2.3.6 S6: user has opted out — absorb announcements silently.
// An explicit release on the call transitions to S7, otherwise we
// sit here until a new user action.
func (c *OffNetCall) onS6(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case oncEvURelease:
		return fsm.Action{
			Next: S7PostRelease,
			Timers: []fsm.TimerOp{{
				ID: tfg3, After: c.Config.TFG3, OnFire: fsm.Event{Type: oncEvTimerTFG3},
			}},
			Reply: true,
		}
	}
	// Any received announcement just gets absorbed.
	return fsm.Action{Reply: true}
}

// §10.2.2.3.7 S7: cooldown after release — TFG3 returns us to S1.
func (c *OffNetCall) onS7(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case oncEvTimerTFG3:
		return fsm.Action{
			Next:   S1StartStop,
			Timers: []fsm.TimerOp{{ID: tfg3, Cancel: true}},
			Reply:  true,
		}
	}
	return fsm.Action{Reply: true}
}

// ── Helpers (handler-only) ──

func (c *OffNetCall) startTFG6() []fsm.TimerOp {
	return []fsm.TimerOp{{
		ID: tfg6, After: c.Config.TFG6, OnFire: fsm.Event{Type: oncEvTimerTFG6},
	}}
}

func (c *OffNetCall) absorbAnnouncement(ev fsm.Event) {
	if cid, ok := ev.Data["call_id"].(string); ok {
		c.callID = cid
	}
	if sdp, ok := ev.Data["sdp"].(string); ok {
		c.sdp = sdp
	}
	if orig, ok := ev.Data["originator"].(string); ok {
		c.originatorID = orig
	}
}

func (c *OffNetCall) emitWire(msg string, payload map[string]any) {
	if c.SendWire != nil {
		c.SendWire(msg, payload)
	}
}

func (c *OffNetCall) snapshot(cs OffNetCallState) map[string]any {
	return map[string]any{
		"group_id":     c.GroupID,
		"mcptt_id":     c.MCPTTID,
		"state":        cs.String(),
		"call_id":      c.callID,
		"originator":   c.originatorID,
		"refresh_ms":   c.refreshMs,
	}
}

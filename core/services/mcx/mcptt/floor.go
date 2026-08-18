// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Floor control server state machine — TS 24.380 §6.3.5 (basic floor
// control operation towards the floor participant). PDF:
// specs/3gpp/ts_124380v190100p.pdf.
//
// Architecture: this is an event-loop FSM built on libs/fsm. One
// goroutine per FloorController is the only mutator of controller
// state; callers (RequestFloor, ReleaseFloor, AddParticipant,
// RemoveParticipant) synchronously reply-channel through Send()
// into that loop. Timers fire through the same loop.
//
// States modelled (5 of the 8 named in §6.3.5):
//
//   StateFloorIdle        — no holder; new requests grant immediately
//   StateFloorTaken       — a holder exists; new requests queue /
//                           preempt / deny per §4.1.1.4
//   StateReleasing        — session tear-down underway
//
//   (The three controller-level states above collapse the per-
//   participant §6.3.5.3 / §6.3.5.4 / §6.3.5.5 / §6.3.5.9 views —
//   each participant's FloorServerState is computed from controller
//   state + whether it is the holder.)
//
// Not modelled: §6.3.5.6 'U: pending Floor Revoke' (preempt-revoke
// outcome of §4.1.1.4), §6.3.5.7 'U: not permitted but sends media',
// §6.3.5.10 'U: not permitted and initiating'.
package mcptt

import (
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/libs/fsm"
)

// ── Controller state (passed through libs/fsm) ──

type callState int

const (
	StateIdle      callState = iota // §6.3.5.3 view: "U: not permitted and Floor Idle" (no holder)
	StateTaken                      // §6.3.5.4 view: "U: not permitted and Floor Taken" (holder present) + §6.3.5.5 for the holder
	StateReleasing                  // §6.3.5.9 "Releasing"
)

func (s callState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateTaken:
		return "taken"
	case StateReleasing:
		return "releasing"
	}
	return "unknown"
}

// FloorServerState is the per-participant state projection for
// callers and tests (TS 24.380 §6.3.5 state names). Computed from
// (controller state, is-holder) at read time.
type FloorServerState int

const (
	StateStartStop  FloorServerState = iota // §6.3.5.2
	StateFloorIdle                          // §6.3.5.3
	StateFloorTaken                         // §6.3.5.4
	StatePermitted                          // §6.3.5.5
	StateFloorReleasing                     // §6.3.5.9
)

func (s FloorServerState) String() string {
	switch s {
	case StateStartStop:
		return "start-stop"
	case StateFloorIdle:
		return "floor-idle"
	case StateFloorTaken:
		return "floor-taken"
	case StatePermitted:
		return "permitted"
	case StateFloorReleasing:
		return "releasing"
	}
	return "unknown"
}

// ── Events ──

const (
	evAddParticipant    = iota // Data: "id" string, "priority" int
	evRemoveParticipant        // Data: "id" string
	evRequestFloor             // Data: "id" string, "priority" *int (optional override)
	evReleaseFloor             // Data: "id" string
	evQueryStatus              // no payload; Reply returns full status snapshot
	evSessionRelease           // call teardown
)

// ── Participant ──

type FloorParticipant struct {
	MCPTTID    string           `json:"mcptt_id"`
	Priority   int              `json:"priority"`
	State      FloorServerState `json:"-"` // derived view (updated on every transition)
	HasFloor   bool             `json:"has_floor"`
	Requesting bool             `json:"requesting"`
	ReqTime    float64          `json:"-"`
}

// ── Controller ──

// FloorController is the public handle. All fields are read-only from
// outside the loop except through the synchronous Send path; the loop
// goroutine is the only writer.
type FloorController struct {
	CallID string
	State  string // "idle" / "taken" / "releasing" — updated by the loop on every transition for callers that read it

	// Observable snapshots. Updated only from within the loop goroutine.
	Holder       *FloorParticipant
	Queue        []*FloorParticipant
	Participants map[string]*FloorParticipant

	// EventCb is fired from the loop goroutine — still serialized,
	// but keep callbacks non-blocking.
	EventCb func(callID, event string, data map[string]interface{})

	fsm *fsm.Machine

	// stopOnce guards Stop() idempotency.
	stopOnce sync.Once
}

// NewFloorController creates and starts a controller in StateIdle.
// Callers should Stop() when the call ends to release the goroutine.
func NewFloorController(callID string) *FloorController {
	fc := &FloorController{
		CallID:       callID,
		State:        StateIdle.String(),
		Participants: make(map[string]*FloorParticipant),
	}
	fc.fsm = fsm.New("floor:"+callID, StateIdle, fc.handle, 64)
	fc.fsm.Start()
	return fc
}

// Stop signals the event loop to shut down and waits for it. Safe to
// call multiple times.
func (fc *FloorController) Stop() {
	fc.stopOnce.Do(func() { fc.fsm.Stop() })
}

// ── Public API (synchronous via reply channel) ──

func (fc *FloorController) AddParticipant(id string, priority int) {
	fc.sendSync(fsm.Event{
		Type: evAddParticipant,
		Data: map[string]any{"id": id, "priority": priority},
	})
}

func (fc *FloorController) RemoveParticipant(id string) {
	fc.sendSync(fsm.Event{
		Type: evRemoveParticipant,
		Data: map[string]any{"id": id},
	})
}

func (fc *FloorController) RequestFloor(id string, priority *int) map[string]interface{} {
	reply := fc.send(fsm.Event{
		Type: evRequestFloor,
		Data: map[string]any{"id": id, "priority": priority},
	})
	if m, ok := reply.(map[string]interface{}); ok {
		return m
	}
	return errorResult("no_reply")
}

func (fc *FloorController) ReleaseFloor(id string) map[string]interface{} {
	reply := fc.send(fsm.Event{
		Type: evReleaseFloor,
		Data: map[string]any{"id": id},
	})
	if m, ok := reply.(map[string]interface{}); ok {
		return m
	}
	return errorResult("no_reply")
}

func (fc *FloorController) GetStatus() map[string]interface{} {
	reply := fc.send(fsm.Event{Type: evQueryStatus})
	if m, ok := reply.(map[string]interface{}); ok {
		return m
	}
	return map[string]interface{}{"call_id": fc.CallID, "state": "unknown"}
}

// send posts an event with a reply channel and waits for the reply.
func (fc *FloorController) send(ev fsm.Event) any {
	reply := make(chan any, 1)
	ev.Reply = reply
	fc.fsm.Send(ev)
	return <-reply
}

// sendSync posts an event and waits for the (nil) reply, ensuring
// the transition has been applied before the caller returns.
func (fc *FloorController) sendSync(ev fsm.Event) {
	fc.send(ev)
}

// ── Handler (runs only on the loop goroutine; no locks needed) ──

func (fc *FloorController) handle(st fsm.State, ev fsm.Event) fsm.Action {
	cs, _ := st.(callState)

	var action fsm.Action
	switch ev.Type {
	case evAddParticipant:
		action = fc.onAdd(cs, ev)
	case evRemoveParticipant:
		action = fc.onRemove(cs, ev)
	case evRequestFloor:
		action = fc.onRequest(cs, ev)
	case evReleaseFloor:
		action = fc.onRelease(cs, ev)
	case evQueryStatus:
		action = fsm.Action{Reply: fc.snapshot(cs)}
		return action // snapshot already refreshes projections
	case evSessionRelease:
		action = fsm.Action{Next: StateReleasing}
	}

	// Single post-dispatch refresh: project the final controller
	// state onto each participant's observable State field.
	final := cs
	if action.Next != nil {
		if ns, ok := action.Next.(callState); ok {
			final = ns
		}
	}
	fc.refreshParticipantStates(final)
	return action
}

// §6.3.5.2.2 "SIP Session initiated" — StartStop → FloorIdle (no
// holder) / FloorTaken (holder exists).
func (fc *FloorController) onAdd(cs callState, ev fsm.Event) fsm.Action {
	id := ev.Data["id"].(string)
	pri := ev.Data["priority"].(int)
	if _, exists := fc.Participants[id]; exists {
		return fsm.Action{}
	}
	p := &FloorParticipant{MCPTTID: id, Priority: pri}
	fc.Participants[id] = p
	return fsm.Action{}
}

// RemoveParticipant: any state → participant removed. If the leaver
// was the holder, synthesise release (§6.3.5.4.5 path).
func (fc *FloorController) onRemove(cs callState, ev fsm.Event) fsm.Action {
	id := ev.Data["id"].(string)
	p := fc.Participants[id]
	if p == nil {
		return fsm.Action{}
	}
	var next callState = cs
	if fc.Holder != nil && fc.Holder.MCPTTID == id {
		next = fc.doRelease(cs)
	}
	kept := fc.Queue[:0]
	for _, q := range fc.Queue {
		if q.MCPTTID != id {
			kept = append(kept, q)
		}
	}
	fc.Queue = kept
	delete(fc.Participants, id)
	return fsm.Action{Next: next}
}

// §6.3.5.3.4 (from Idle) / §6.3.5.4.4 (from Taken).
func (fc *FloorController) onRequest(cs callState, ev fsm.Event) fsm.Action {
	id := ev.Data["id"].(string)
	p := fc.Participants[id]
	if p == nil {
		return fsm.Action{Reply: errorResult("not_participant")}
	}
	if priPtr, _ := ev.Data["priority"].(*int); priPtr != nil {
		p.Priority = *priPtr
	}
	recordFloorEvent(fc.CallID, id, "request", p.Priority)

	switch cs {
	case StateReleasing:
		return fsm.Action{Reply: errorResult("bad_state")}

	case StateIdle:
		// §6.3.5.3.4 → §6.3.5.3.5 "Send Floor Granted" + §6.3.5.3.3
		// "Send Floor Taken" to every other participant.
		fc.doGrant(p)
		return fsm.Action{
			Next:  StateTaken,
			Reply: map[string]interface{}{"result": "granted", "mcptt_id": id},
		}

	case StateTaken:
		// Holder re-requesting: idempotent granted.
		if fc.Holder != nil && fc.Holder.MCPTTID == id {
			return fsm.Action{Reply: map[string]interface{}{"result": "granted", "mcptt_id": id}}
		}
		// §4.1.1.4 outcome 1: preempt-override when priority beats holder.
		if fc.Holder != nil && canPreempt(p.Priority, fc.Holder.Priority) {
			old := fc.Holder.MCPTTID
			fc.doPreempt(p)
			return fsm.Action{
				Reply: map[string]interface{}{
					"result":    "preempted",
					"mcptt_id":  id,
					"preempted": old,
				},
			}
		}
		// Queue, with deny when at cap.
		if len(fc.Queue) >= MaxFloorQueue {
			recordFloorEvent(fc.CallID, id, "denied", p.Priority)
			return fsm.Action{Reply: map[string]interface{}{"result": "denied", "reason": "queue_full"}}
		}
		p.Requesting = true
		p.ReqTime = float64(time.Now().UnixMilli()) / 1000
		fc.enqueue(p)
		return fsm.Action{
			Reply: map[string]interface{}{"result": "queued", "position": fc.queuePosition(p)},
		}
	}
	return fsm.Action{Reply: errorResult("bad_state")}
}

// §6.3.5.5 → §6.3.5.3/.4 — holder releases floor; next-in-queue
// (if any) is granted, else controller returns to StateIdle.
func (fc *FloorController) onRelease(cs callState, ev fsm.Event) fsm.Action {
	id := ev.Data["id"].(string)
	if fc.Holder == nil || fc.Holder.MCPTTID != id {
		return fsm.Action{Reply: errorResult("not_holder")}
	}
	next := fc.doRelease(cs)
	return fsm.Action{Next: next, Reply: map[string]interface{}{"result": "released"}}
}

// ── Transition mechanics (handler-only) ──

// doGrant moves p into StatePermitted, records, and fires callback.
// §6.3.5.3.5 (Send Floor Granted) + §6.3.5.3.3 (Send Floor Taken).
func (fc *FloorController) doGrant(p *FloorParticipant) {
	p.HasFloor = true
	p.Requesting = false
	fc.Holder = p
	updateCallFloorHolder(fc.CallID, p.MCPTTID)
	recordFloorEvent(fc.CallID, p.MCPTTID, "granted", p.Priority)
	if fc.EventCb != nil {
		go fc.EventCb(fc.CallID, "floor_granted", map[string]interface{}{"mcptt_id": p.MCPTTID})
	}
}

// doRelease clears holder, grants next-in-queue (if any), and
// returns the resulting controller state.
// §6.3.5.4.5 (Receive Floor Release → Send Floor Idle) + auto-grant.
func (fc *FloorController) doRelease(cs callState) callState {
	if fc.Holder != nil {
		old := fc.Holder
		old.HasFloor = false
		recordFloorEvent(fc.CallID, old.MCPTTID, "release", 0)
		if fc.EventCb != nil {
			go fc.EventCb(fc.CallID, "floor_released", map[string]interface{}{"mcptt_id": old.MCPTTID})
		}
	}
	fc.Holder = nil
	updateCallFloorHolder(fc.CallID, "")
	if len(fc.Queue) > 0 {
		next := fc.Queue[0]
		fc.Queue = fc.Queue[1:]
		next.Requesting = false
		fc.doGrant(next)
		return StateTaken
	}
	return StateIdle
}

// doPreempt: §4.1.1.4 outcome 1 — immediate override.
func (fc *FloorController) doPreempt(req *FloorParticipant) {
	if fc.Holder != nil {
		old := fc.Holder
		old.HasFloor = false
		recordFloorEvent(fc.CallID, old.MCPTTID, "preempted", old.Priority)
		if fc.EventCb != nil {
			go fc.EventCb(fc.CallID, "floor_preempted", map[string]interface{}{
				"mcptt_id": old.MCPTTID, "by": req.MCPTTID,
			})
		}
	}
	fc.Holder = nil
	fc.doGrant(req)
}

func (fc *FloorController) enqueue(p *FloorParticipant) {
	fc.Queue = append(fc.Queue, p)
	for i := len(fc.Queue) - 1; i > 0; i-- {
		if fc.Queue[i].Priority < fc.Queue[i-1].Priority {
			fc.Queue[i], fc.Queue[i-1] = fc.Queue[i-1], fc.Queue[i]
		} else {
			break
		}
	}
}

func (fc *FloorController) queuePosition(p *FloorParticipant) int {
	for i, q := range fc.Queue {
		if q.MCPTTID == p.MCPTTID {
			return i + 1
		}
	}
	return 0
}

// refreshParticipantStates projects controller state + holder onto
// each participant's public State field for observability. Called
// after every transition that changes holder or controller state.
func (fc *FloorController) refreshParticipantStates(cs callState) {
	for _, p := range fc.Participants {
		switch cs {
		case StateReleasing:
			p.State = StateFloorReleasing
		case StateIdle:
			p.State = StateFloorIdle
		case StateTaken:
			if fc.Holder != nil && fc.Holder.MCPTTID == p.MCPTTID {
				p.State = StatePermitted
			} else {
				p.State = StateFloorTaken
			}
		}
	}
	fc.State = cs.String()
}

func (fc *FloorController) snapshot(cs callState) map[string]interface{} {
	fc.refreshParticipantStates(cs)
	holder, holderPri := "", 0
	if fc.Holder != nil {
		holder = fc.Holder.MCPTTID
		holderPri = fc.Holder.Priority
	}
	q := make([]string, len(fc.Queue))
	for i, qp := range fc.Queue {
		q[i] = qp.MCPTTID
	}
	ps := make([]map[string]interface{}, 0, len(fc.Participants))
	for _, p := range fc.Participants {
		ps = append(ps, map[string]interface{}{
			"mcptt_id":   p.MCPTTID,
			"priority":   p.Priority,
			"state":      p.State.String(),
			"has_floor":  p.HasFloor,
			"requesting": p.Requesting,
		})
	}
	return map[string]interface{}{
		"call_id":         fc.CallID,
		"state":           cs.String(),
		"holder":          holder,
		"holder_priority": holderPri,
		"queue":           q,
		"participants":    ps,
	}
}

func errorResult(reason string) map[string]interface{} {
	return map[string]interface{}{"result": "error", "reason": reason}
}

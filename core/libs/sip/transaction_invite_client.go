// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// INVITE client transaction — RFC 3261 §17.1.1. Spec anchor:
// specs/ietf/rfc3261.txt.
package sip

import (
	"time"

	"github.com/mmt/mmt-studio-core/libs/fsm"
)

// ICState — INVITE client transaction states (§17.1.1.2).
type ICState int

const (
	ICCalling    ICState = iota // initial: INVITE sent, Timer A retransmitting
	ICProceeding                // received a 1xx
	ICCompleted                 // received 3xx-6xx, ACK sent, Timer D running
	ICTerminated                // final
)

func (s ICState) String() string {
	switch s {
	case ICCalling:
		return "Calling"
	case ICProceeding:
		return "Proceeding"
	case ICCompleted:
		return "Completed"
	case ICTerminated:
		return "Terminated"
	}
	return "?"
}

// Events for the INVITE client FSM.
const (
	icEvStart        = iota // send first INVITE, arm A and B
	icEvTimerA              // retransmit
	icEvTimerB              // timeout → Terminated
	icEvTimerD              // leave Completed → Terminated
	icEvProvisional         // 1xx received
	icEv2xx                 // 2xx received → Terminated (TU handles ACK for 2xx)
	icEvFinalError          // 3xx-6xx received → send ACK, goto Completed
	icEvTransportErr        // transport failure → Terminated
	icEvQueryLast           // internal: return LastResponse via Reply
)

// Timer IDs.
const (
	icTimerA = "A"
	icTimerB = "B"
	icTimerD = "D"
)

// InviteClientTxn is the INVITE client transaction FSM.
type InviteClientTxn struct {
	branch string
	method string // always "INVITE"
	req    *SipRequest
	last   *SipResponse

	// SendRequest is called when the FSM wants to put the INVITE
	// (or ACK for 3xx-6xx) on the wire. Callback runs on the FSM
	// loop goroutine.
	SendRequest func(*SipRequest)

	// OnTerminated fires once when the transaction reaches
	// Terminated. Useful for the TU to free up resources.
	OnTerminated func()

	// retxInterval is the current Timer A duration — starts at T1
	// and doubles on each retransmit.
	retxInterval time.Duration

	// reliableTransport suppresses Timer A (retransmits) and sets
	// Timer D to 0 per §17.1.1.2.
	reliableTransport bool

	fsm *fsm.Machine
}

// NewInviteClientTxn creates and starts an INVITE client transaction.
// Call Fire() to send the initial INVITE and arm timers A and B.
func NewInviteClientTxn(req *SipRequest, reliable bool, send func(*SipRequest)) *InviteClientTxn {
	branch := extractBranch(req.GetHeader(HdrVia))
	t := &InviteClientTxn{
		branch:            branch,
		method:            "INVITE",
		req:               req,
		SendRequest:       send,
		retxInterval:      T1,
		reliableTransport: reliable,
	}
	t.fsm = fsm.New("ict:"+branch, ICCalling, t.handle, 16)
	t.fsm.Start()
	return t
}

// Branch returns the §17.1.3 matching key branch component.
func (t *InviteClientTxn) Branch() string { return t.branch }

// Method returns the method (always "INVITE").
func (t *InviteClientTxn) Method() string { return t.method }

// State returns the current state string.
func (t *InviteClientTxn) State() string { return t.fsm.State().(ICState).String() }

// Stop shuts down the FSM goroutine.
func (t *InviteClientTxn) Stop() { t.fsm.Stop() }

// Fire kicks off the transaction: send the initial INVITE and arm
// Timers A (retransmit) and B (timeout). Must be called exactly once.
func (t *InviteClientTxn) Fire() { t.fsm.Send(fsm.Event{Type: icEvStart}) }

// ReceiveResponse is the TU pushing a response into the FSM. The
// response travels through the event channel — the handler is the
// only writer of t.last, so there's no race with concurrent callers.
func (t *InviteClientTxn) ReceiveResponse(resp *SipResponse) {
	var evType int
	switch {
	case resp.StatusCode < 200:
		evType = icEvProvisional
	case resp.StatusCode < 300:
		evType = icEv2xx
	default:
		evType = icEvFinalError
	}
	t.fsm.Send(fsm.Event{Type: evType, Data: map[string]any{"resp": resp}})
}

// SignalTransportError tells the FSM the transport couldn't send.
func (t *InviteClientTxn) SignalTransportError() {
	t.fsm.Send(fsm.Event{Type: icEvTransportErr})
}

// LastResponse returns the most recent response seen, fetched
// synchronously through the FSM loop so it never races with a
// concurrent ReceiveResponse.
func (t *InviteClientTxn) LastResponse() *SipResponse {
	reply := make(chan any, 1)
	t.fsm.Send(fsm.Event{Type: icEvQueryLast, Reply: reply})
	res, _ := (<-reply).(*SipResponse)
	return res
}

// ── Handler (loop goroutine only) ──

func (t *InviteClientTxn) handle(st fsm.State, ev fsm.Event) fsm.Action {
	// Record the response carried on the event, if any, before
	// dispatching. This is the only place t.last is written.
	if r, _ := ev.Data["resp"].(*SipResponse); r != nil {
		t.last = r
	}
	if ev.Type == icEvQueryLast {
		return fsm.Action{Reply: t.last}
	}

	cs, _ := st.(ICState)
	switch cs {
	case ICCalling:
		return t.onCalling(ev)
	case ICProceeding:
		return t.onProceeding(ev)
	case ICCompleted:
		return t.onCompleted(ev)
	case ICTerminated:
		return fsm.Action{}
	}
	return fsm.Action{}
}

// §17.1.1.2 Calling: INVITE has been sent (or is being sent), Timer A
// retransmits every T1*2^n (up to timeout by Timer B).
func (t *InviteClientTxn) onCalling(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case icEvStart:
		t.send(t.req) // initial send
		var timers []fsm.TimerOp
		if !t.reliableTransport {
			timers = append(timers, fsm.TimerOp{
				ID: icTimerA, After: t.retxInterval,
				OnFire: fsm.Event{Type: icEvTimerA},
			})
		}
		timers = append(timers, fsm.TimerOp{
			ID: icTimerB, After: 64 * T1,
			OnFire: fsm.Event{Type: icEvTimerB},
		})
		return fsm.Action{Timers: timers}

	case icEvTimerA:
		// Retransmit INVITE; reschedule A at 2x current interval.
		t.send(t.req)
		t.retxInterval *= 2
		return fsm.Action{Timers: []fsm.TimerOp{{
			ID: icTimerA, After: t.retxInterval,
			OnFire: fsm.Event{Type: icEvTimerA},
		}}}

	case icEvTimerB:
		return t.terminate("timeout", false)

	case icEvProvisional:
		// Cancel A (no more retransmits), keep B running to cap total time.
		return fsm.Action{
			Next:   ICProceeding,
			Timers: []fsm.TimerOp{{ID: icTimerA, Cancel: true}},
		}

	case icEv2xx:
		// §17.1.1.2: on 2xx the transaction terminates — TU handles
		// ACK for 2xx directly (it's a separate transaction).
		return t.terminate("2xx", true)

	case icEvFinalError:
		// 3xx-6xx: send ACK, enter Completed, arm Timer D.
		t.sendAckForError()
		d := 32 * time.Second
		if t.reliableTransport {
			d = 0
		}
		timers := []fsm.TimerOp{
			{ID: icTimerA, Cancel: true},
			{ID: icTimerB, Cancel: true},
		}
		if d > 0 {
			timers = append(timers, fsm.TimerOp{
				ID: icTimerD, After: d, OnFire: fsm.Event{Type: icEvTimerD},
			})
		}
		next := ICCompleted
		if d == 0 {
			next = ICTerminated
		}
		act := fsm.Action{Next: next, Timers: timers}
		if next == ICTerminated {
			t.fireTerminated()
		}
		return act

	case icEvTransportErr:
		return t.terminate("transport_error", false)
	}
	return fsm.Action{}
}

// §17.1.1.2 Proceeding: TU has received 1xx. Further 1xx stay here;
// final responses drive to Completed/Terminated.
func (t *InviteClientTxn) onProceeding(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case icEvProvisional:
		return fsm.Action{} // stay
	case icEv2xx:
		return t.terminate("2xx", true)
	case icEvFinalError:
		t.sendAckForError()
		d := 32 * time.Second
		if t.reliableTransport {
			d = 0
		}
		timers := []fsm.TimerOp{
			{ID: icTimerB, Cancel: true},
		}
		if d > 0 {
			timers = append(timers, fsm.TimerOp{
				ID: icTimerD, After: d, OnFire: fsm.Event{Type: icEvTimerD},
			})
		}
		next := ICCompleted
		if d == 0 {
			next = ICTerminated
		}
		act := fsm.Action{Next: next, Timers: timers}
		if next == ICTerminated {
			t.fireTerminated()
		}
		return act
	case icEvTimerB:
		return t.terminate("timeout", false)
	case icEvTransportErr:
		return t.terminate("transport_error", false)
	}
	return fsm.Action{}
}

// §17.1.1.2 Completed: ACK has been sent for 3xx-6xx; retransmitted
// final responses are absorbed by resending ACK.
func (t *InviteClientTxn) onCompleted(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case icEvFinalError:
		// Retransmitted final response — just resend ACK.
		t.sendAckForError()
		return fsm.Action{}
	case icEvTimerD, icEvTransportErr:
		return t.terminate("done", false)
	}
	return fsm.Action{}
}

// terminate moves to Terminated, cancels all timers, fires callback.
func (t *InviteClientTxn) terminate(_ string, _ bool) fsm.Action {
	t.fireTerminated()
	return fsm.Action{
		Next: ICTerminated,
		Timers: []fsm.TimerOp{
			{ID: icTimerA, Cancel: true},
			{ID: icTimerB, Cancel: true},
			{ID: icTimerD, Cancel: true},
		},
	}
}

func (t *InviteClientTxn) send(req *SipRequest) {
	if t.SendRequest != nil {
		t.SendRequest(req)
	}
}

// sendAckForError sends ACK for a 3xx-6xx within this transaction
// (§17.1.1.3). Building the ACK from the INVITE + response headers
// is a detail for the TU / message builder; here we just notify the
// SendRequest callback with an ACK-shaped SipRequest copied from
// the INVITE with Method swapped. Real implementations would also
// copy the To-tag from the response.
func (t *InviteClientTxn) sendAckForError() {
	if t.req == nil {
		return
	}
	ack := &SipRequest{
		SipMessage: SipMessage{Headers: copyHeaders(t.req.Headers), Body: ""},
		Method:     "ACK",
		RequestURI: t.req.RequestURI,
	}
	if t.last != nil {
		if totag := extractParam(t.last.GetHeader(HdrTo), "tag"); totag != "" {
			ack.SetHeader(HdrTo, t.last.GetHeader(HdrTo))
		}
	}
	t.send(ack)
}

func (t *InviteClientTxn) fireTerminated() {
	if t.OnTerminated != nil {
		t.OnTerminated()
	}
}

// copyHeaders returns a deep copy of a SIP header map.
func copyHeaders(h map[string][]string) map[string][]string {
	out := make(map[string][]string, len(h))
	for k, v := range h {
		vs := make([]string, len(v))
		copy(vs, v)
		out[k] = vs
	}
	return out
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// INVITE server transaction — RFC 3261 §17.2.1. Spec anchor:
// specs/ietf/rfc3261.txt.
package sip

import (
	"time"

	"github.com/mmt/mmt-studio-core/libs/fsm"
)

// ISState — INVITE server states (§17.2.1).
type ISState int

const (
	ISProceeding ISState = iota // initial: 100 Trying sent, 1xx sent optionally
	ISCompleted                 // 3xx-6xx sent, retransmit on Timer G until ACK
	ISConfirmed                 // ACK received in Completed, Timer I running
	ISTerminated
)

func (s ISState) String() string {
	switch s {
	case ISProceeding:
		return "Proceeding"
	case ISCompleted:
		return "Completed"
	case ISConfirmed:
		return "Confirmed"
	case ISTerminated:
		return "Terminated"
	}
	return "?"
}

const (
	isEvSendProvisional = iota // TU wants to send a 1xx
	isEvSend2xx                // TU wants to send a 2xx
	isEvSendFinalErr           // TU wants to send 3xx-6xx
	isEvAck                    // ACK received
	isEvRetransmitReq          // retransmitted INVITE received (absorb)
	isEvTimerG                 // retransmit 3xx-6xx
	isEvTimerH                 // ACK timeout
	isEvTimerI                 // leave Confirmed
	isEvTransportErr
)

const (
	isTimerG = "G"
	isTimerH = "H"
	isTimerI = "I"
)

type InviteServerTxn struct {
	branch, method string
	req            *SipRequest

	// Last response sent by the TU — needed for retransmits.
	lastResp *SipResponse

	SendResponse func(*SipResponse)
	OnTerminated func()

	retxInterval      time.Duration
	reliableTransport bool

	fsm *fsm.Machine
}

// NewInviteServerTxn creates and starts an INVITE server transaction.
// The TU should immediately call SendProvisional / Send2xx / SendFinalError
// to drive state.
func NewInviteServerTxn(req *SipRequest, reliable bool, send func(*SipResponse)) *InviteServerTxn {
	branch := extractBranch(req.GetHeader(HdrVia))
	t := &InviteServerTxn{
		branch:            branch,
		method:            "INVITE",
		req:               req,
		SendResponse:      send,
		retxInterval:      T1,
		reliableTransport: reliable,
	}
	t.fsm = fsm.New("ist:"+branch, ISProceeding, t.handle, 16)
	t.fsm.Start()
	return t
}

func (t *InviteServerTxn) Branch() string { return t.branch }
func (t *InviteServerTxn) Method() string { return t.method }
func (t *InviteServerTxn) State() string  { return t.fsm.State().(ISState).String() }
func (t *InviteServerTxn) Stop()          { t.fsm.Stop() }

// Request returns the original INVITE that opened this server
// transaction. Needed by callers that have to construct a response
// against the INVITE — e.g. RFC 3261 §9.2 CANCEL handling, where the
// 487 Request Terminated must echo the INVITE's To / From / CSeq /
// Call-ID / Via, not the CANCEL's.
func (t *InviteServerTxn) Request() *SipRequest { return t.req }

// SendProvisional tells the FSM the TU has produced a 1xx response.
// The response travels through the event channel so t.lastResp is
// only ever written by the handler goroutine.
func (t *InviteServerTxn) SendProvisional(resp *SipResponse) {
	t.fsm.Send(fsm.Event{Type: isEvSendProvisional, Data: map[string]any{"resp": resp}})
}

// Send2xx tells the FSM the TU has produced a 2xx response.
func (t *InviteServerTxn) Send2xx(resp *SipResponse) {
	t.fsm.Send(fsm.Event{Type: isEvSend2xx, Data: map[string]any{"resp": resp}})
}

// SendFinalError tells the FSM the TU has produced a 3xx-6xx response.
func (t *InviteServerTxn) SendFinalError(resp *SipResponse) {
	t.fsm.Send(fsm.Event{Type: isEvSendFinalErr, Data: map[string]any{"resp": resp}})
}

// ReceiveAck tells the FSM an ACK for the current transaction arrived.
func (t *InviteServerTxn) ReceiveAck() { t.fsm.Send(fsm.Event{Type: isEvAck}) }

// ReceiveRetransmit tells the FSM a duplicate INVITE arrived (absorb by
// resending the last response).
func (t *InviteServerTxn) ReceiveRetransmit() { t.fsm.Send(fsm.Event{Type: isEvRetransmitReq}) }

func (t *InviteServerTxn) SignalTransportError() { t.fsm.Send(fsm.Event{Type: isEvTransportErr}) }

func (t *InviteServerTxn) handle(st fsm.State, ev fsm.Event) fsm.Action {
	if r, _ := ev.Data["resp"].(*SipResponse); r != nil {
		t.lastResp = r
	}
	cs, _ := st.(ISState)
	switch cs {
	case ISProceeding:
		return t.onProceeding(ev)
	case ISCompleted:
		return t.onCompleted(ev)
	case ISConfirmed:
		return t.onConfirmed(ev)
	}
	return fsm.Action{}
}

func (t *InviteServerTxn) onProceeding(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case isEvSendProvisional:
		t.sendLast()
		return fsm.Action{}
	case isEvSend2xx:
		// §17.2.1: 2xx terminates INVITE server transaction immediately;
		// ACK for 2xx is handled outside the transaction.
		t.sendLast()
		return t.terminate()
	case isEvSendFinalErr:
		t.sendLast()
		timers := []fsm.TimerOp{
			{ID: isTimerH, After: 64 * T1, OnFire: fsm.Event{Type: isEvTimerH}},
		}
		if !t.reliableTransport {
			timers = append(timers, fsm.TimerOp{
				ID: isTimerG, After: t.retxInterval, OnFire: fsm.Event{Type: isEvTimerG},
			})
		}
		return fsm.Action{Next: ISCompleted, Timers: timers}
	case isEvRetransmitReq:
		// Duplicate INVITE: resend last response if one was issued.
		t.sendLast()
		return fsm.Action{}
	case isEvTransportErr:
		return t.terminate()
	}
	return fsm.Action{}
}

func (t *InviteServerTxn) onCompleted(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case isEvTimerG:
		// Retransmit the 3xx-6xx response; schedule next G.
		t.sendLast()
		t.retxInterval *= 2
		if t.retxInterval > T2 {
			t.retxInterval = T2
		}
		return fsm.Action{Timers: []fsm.TimerOp{{
			ID: isTimerG, After: t.retxInterval, OnFire: fsm.Event{Type: isEvTimerG},
		}}}
	case isEvRetransmitReq:
		// Retransmitted INVITE in Completed: resend 3xx-6xx.
		t.sendLast()
		return fsm.Action{}
	case isEvAck:
		timers := []fsm.TimerOp{
			{ID: isTimerG, Cancel: true},
			{ID: isTimerH, Cancel: true},
		}
		i := T4
		if t.reliableTransport {
			i = 0
		}
		if i > 0 {
			timers = append(timers, fsm.TimerOp{
				ID: isTimerI, After: i, OnFire: fsm.Event{Type: isEvTimerI},
			})
			return fsm.Action{Next: ISConfirmed, Timers: timers}
		}
		return t.terminateWithTimers(timers)
	case isEvTimerH:
		return t.terminate()
	case isEvTransportErr:
		return t.terminate()
	}
	return fsm.Action{}
}

func (t *InviteServerTxn) onConfirmed(ev fsm.Event) fsm.Action {
	// Further retransmits or ACKs are silently absorbed.
	if ev.Type == isEvTimerI {
		return t.terminate()
	}
	return fsm.Action{}
}

func (t *InviteServerTxn) sendLast() {
	if t.lastResp != nil && t.SendResponse != nil {
		t.SendResponse(t.lastResp)
	}
}

func (t *InviteServerTxn) terminate() fsm.Action {
	return t.terminateWithTimers(nil)
}

func (t *InviteServerTxn) terminateWithTimers(extra []fsm.TimerOp) fsm.Action {
	if t.OnTerminated != nil {
		t.OnTerminated()
	}
	timers := append(extra,
		fsm.TimerOp{ID: isTimerG, Cancel: true},
		fsm.TimerOp{ID: isTimerH, Cancel: true},
		fsm.TimerOp{ID: isTimerI, Cancel: true},
	)
	return fsm.Action{Next: ISTerminated, Timers: timers}
}

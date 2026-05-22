// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Non-INVITE server transaction — RFC 3261 §17.2.2. Spec anchor:
// specs/ietf/rfc3261.txt.
package sip

import (
	"github.com/mmt/mmt-studio-core/libs/fsm"
)

// NISState — non-INVITE server states (§17.2.2).
type NISState int

const (
	NISTrying     NISState = iota
	NISProceeding                 // 1xx sent
	NISCompleted                  // final response sent, Timer J running
	NISTerminated
)

func (s NISState) String() string {
	switch s {
	case NISTrying:
		return "Trying"
	case NISProceeding:
		return "Proceeding"
	case NISCompleted:
		return "Completed"
	case NISTerminated:
		return "Terminated"
	}
	return "?"
}

const (
	nisEvSendProvisional = iota
	nisEvSendFinal
	nisEvRetransmitReq
	nisEvTimerJ
	nisEvTransportErr
)

const (
	nisTimerJ = "J"
)

type NonInviteServerTxn struct {
	branch, method string
	req            *SipRequest

	lastResp *SipResponse

	SendResponse func(*SipResponse)
	OnTerminated func()

	reliableTransport bool

	fsm *fsm.Machine
}

func NewNonInviteServerTxn(req *SipRequest, reliable bool, send func(*SipResponse)) *NonInviteServerTxn {
	branch := extractBranch(req.GetHeader(HdrVia))
	t := &NonInviteServerTxn{
		branch:            branch,
		method:            req.Method,
		req:               req,
		SendResponse:      send,
		reliableTransport: reliable,
	}
	t.fsm = fsm.New("nis:"+branch, NISTrying, t.handle, 16)
	t.fsm.Start()
	return t
}

func (t *NonInviteServerTxn) Branch() string { return t.branch }
func (t *NonInviteServerTxn) Method() string { return t.method }
func (t *NonInviteServerTxn) State() string  { return t.fsm.State().(NISState).String() }
func (t *NonInviteServerTxn) Stop()          { t.fsm.Stop() }

// SendProvisional tells the FSM the TU has produced a 1xx. The
// response travels through the event channel — only the handler
// writes t.lastResp.
func (t *NonInviteServerTxn) SendProvisional(resp *SipResponse) {
	t.fsm.Send(fsm.Event{Type: nisEvSendProvisional, Data: map[string]any{"resp": resp}})
}

// SendFinal tells the FSM the TU has produced a 2xx-6xx response.
func (t *NonInviteServerTxn) SendFinal(resp *SipResponse) {
	t.fsm.Send(fsm.Event{Type: nisEvSendFinal, Data: map[string]any{"resp": resp}})
}

// ReceiveRetransmit tells the FSM a duplicate request arrived.
func (t *NonInviteServerTxn) ReceiveRetransmit() {
	t.fsm.Send(fsm.Event{Type: nisEvRetransmitReq})
}

func (t *NonInviteServerTxn) SignalTransportError() {
	t.fsm.Send(fsm.Event{Type: nisEvTransportErr})
}

func (t *NonInviteServerTxn) handle(st fsm.State, ev fsm.Event) fsm.Action {
	if r, _ := ev.Data["resp"].(*SipResponse); r != nil {
		t.lastResp = r
	}
	cs, _ := st.(NISState)
	switch cs {
	case NISTrying:
		return t.onTrying(ev)
	case NISProceeding:
		return t.onProceeding(ev)
	case NISCompleted:
		return t.onCompleted(ev)
	}
	return fsm.Action{}
}

func (t *NonInviteServerTxn) onTrying(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case nisEvSendProvisional:
		t.sendLast()
		return fsm.Action{Next: NISProceeding}
	case nisEvSendFinal:
		t.sendLast()
		return t.goCompleted()
	case nisEvRetransmitReq:
		// §17.2.2: in Trying, retransmitted requests are absorbed
		// silently — no 100 Trying is mandated for non-INVITE.
		return fsm.Action{}
	case nisEvTransportErr:
		return t.terminate()
	}
	return fsm.Action{}
}

func (t *NonInviteServerTxn) onProceeding(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case nisEvSendProvisional:
		t.sendLast()
		return fsm.Action{}
	case nisEvSendFinal:
		t.sendLast()
		return t.goCompleted()
	case nisEvRetransmitReq:
		// Resend last provisional.
		t.sendLast()
		return fsm.Action{}
	case nisEvTransportErr:
		return t.terminate()
	}
	return fsm.Action{}
}

func (t *NonInviteServerTxn) onCompleted(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case nisEvRetransmitReq:
		// Resend final response.
		t.sendLast()
		return fsm.Action{}
	case nisEvTimerJ, nisEvTransportErr:
		return t.terminate()
	}
	return fsm.Action{}
}

func (t *NonInviteServerTxn) goCompleted() fsm.Action {
	if t.reliableTransport {
		// §17.2.2: J = 0 over reliable transports → direct to Terminated.
		return t.terminate()
	}
	return fsm.Action{
		Next: NISCompleted,
		Timers: []fsm.TimerOp{{
			ID: nisTimerJ, After: 64 * T1, OnFire: fsm.Event{Type: nisEvTimerJ},
		}},
	}
}

func (t *NonInviteServerTxn) terminate() fsm.Action {
	if t.OnTerminated != nil {
		t.OnTerminated()
	}
	return fsm.Action{
		Next:   NISTerminated,
		Timers: []fsm.TimerOp{{ID: nisTimerJ, Cancel: true}},
	}
}

func (t *NonInviteServerTxn) sendLast() {
	if t.lastResp != nil && t.SendResponse != nil {
		t.SendResponse(t.lastResp)
	}
}

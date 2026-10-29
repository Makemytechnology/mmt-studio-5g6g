// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Non-INVITE client transaction — RFC 3261 §17.1.2. Spec anchor:
// specs/ietf/rfc3261.txt.
package sip

import (
	"time"

	"github.com/mmt/mmt-studio-core/libs/fsm"
)

// NICState — non-INVITE client states (§17.1.2.2).
type NICState int

const (
	NICTrying     NICState = iota // initial, request sent, Timer E retransmitting
	NICProceeding                 // received 1xx
	NICCompleted                  // received final response, Timer K running
	NICTerminated
)

func (s NICState) String() string {
	switch s {
	case NICTrying:
		return "Trying"
	case NICProceeding:
		return "Proceeding"
	case NICCompleted:
		return "Completed"
	case NICTerminated:
		return "Terminated"
	}
	return "?"
}

const (
	nicEvStart       = iota
	nicEvTimerE             // retransmit
	nicEvTimerF             // timeout
	nicEvTimerK             // leave Completed
	nicEvProvisional        // 1xx
	nicEvFinal              // 2xx-6xx
	nicEvTransportErr
	nicEvQueryLast
)

const (
	nicTimerE = "E"
	nicTimerF = "F"
	nicTimerK = "K"
)

type NonInviteClientTxn struct {
	branch, method string
	req            *SipRequest
	last           *SipResponse

	SendRequest  func(*SipRequest)
	OnTerminated func()

	// Timer E retransmit interval — starts at T1, doubles up to T2.
	retxInterval time.Duration

	reliableTransport bool

	fsm *fsm.Machine
}

func NewNonInviteClientTxn(req *SipRequest, reliable bool, send func(*SipRequest)) *NonInviteClientTxn {
	branch := extractBranch(req.GetHeader(HdrVia))
	t := &NonInviteClientTxn{
		branch:            branch,
		method:            req.Method,
		req:               req,
		SendRequest:       send,
		retxInterval:      T1,
		reliableTransport: reliable,
	}
	t.fsm = fsm.New("nic:"+branch, NICTrying, t.handle, 16)
	t.fsm.Start()
	return t
}

func (t *NonInviteClientTxn) Branch() string        { return t.branch }
func (t *NonInviteClientTxn) Method() string        { return t.method }
func (t *NonInviteClientTxn) State() string         { return t.fsm.State().(NICState).String() }
func (t *NonInviteClientTxn) Stop()                 { t.fsm.Stop() }
func (t *NonInviteClientTxn) Fire()                 { t.fsm.Send(fsm.Event{Type: nicEvStart}) }
func (t *NonInviteClientTxn) SignalTransportError() { t.fsm.Send(fsm.Event{Type: nicEvTransportErr}) }

// LastResponse goes through the loop to avoid racing with
// ReceiveResponse.
func (t *NonInviteClientTxn) LastResponse() *SipResponse {
	reply := make(chan any, 1)
	t.fsm.Send(fsm.Event{Type: nicEvQueryLast, Reply: reply})
	res, _ := (<-reply).(*SipResponse)
	return res
}

func (t *NonInviteClientTxn) ReceiveResponse(r *SipResponse) {
	evType := nicEvFinal
	if r.StatusCode < 200 {
		evType = nicEvProvisional
	}
	t.fsm.Send(fsm.Event{Type: evType, Data: map[string]any{"resp": r}})
}

func (t *NonInviteClientTxn) handle(st fsm.State, ev fsm.Event) fsm.Action {
	if r, _ := ev.Data["resp"].(*SipResponse); r != nil {
		t.last = r
	}
	if ev.Type == nicEvQueryLast {
		return fsm.Action{Reply: t.last}
	}
	cs, _ := st.(NICState)
	switch cs {
	case NICTrying:
		return t.onTrying(ev)
	case NICProceeding:
		return t.onProceeding(ev)
	case NICCompleted:
		return t.onCompleted(ev)
	}
	return fsm.Action{}
}

func (t *NonInviteClientTxn) onTrying(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case nicEvStart:
		if t.SendRequest != nil {
			t.SendRequest(t.req)
		}
		var timers []fsm.TimerOp
		if !t.reliableTransport {
			timers = append(timers, fsm.TimerOp{
				ID: nicTimerE, After: t.retxInterval,
				OnFire: fsm.Event{Type: nicEvTimerE},
			})
		}
		timers = append(timers, fsm.TimerOp{
			ID: nicTimerF, After: 64 * T1,
			OnFire: fsm.Event{Type: nicEvTimerF},
		})
		return fsm.Action{Timers: timers}

	case nicEvTimerE:
		if t.SendRequest != nil {
			t.SendRequest(t.req)
		}
		// §17.1.2.2: double E interval up to T2.
		t.retxInterval *= 2
		if t.retxInterval > T2 {
			t.retxInterval = T2
		}
		return fsm.Action{Timers: []fsm.TimerOp{{
			ID: nicTimerE, After: t.retxInterval,
			OnFire: fsm.Event{Type: nicEvTimerE},
		}}}

	case nicEvTimerF:
		return t.terminate()

	case nicEvProvisional:
		return fsm.Action{Next: NICProceeding}

	case nicEvFinal:
		return t.goCompleted()

	case nicEvTransportErr:
		return t.terminate()
	}
	return fsm.Action{}
}

func (t *NonInviteClientTxn) onProceeding(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case nicEvTimerE:
		// Continue retransmitting at T2 cadence in Proceeding.
		if t.SendRequest != nil {
			t.SendRequest(t.req)
		}
		return fsm.Action{Timers: []fsm.TimerOp{{
			ID: nicTimerE, After: T2,
			OnFire: fsm.Event{Type: nicEvTimerE},
		}}}
	case nicEvTimerF:
		return t.terminate()
	case nicEvProvisional:
		return fsm.Action{}
	case nicEvFinal:
		return t.goCompleted()
	case nicEvTransportErr:
		return t.terminate()
	}
	return fsm.Action{}
}

func (t *NonInviteClientTxn) onCompleted(ev fsm.Event) fsm.Action {
	switch ev.Type {
	case nicEvTimerK, nicEvTransportErr:
		return t.terminate()
	}
	// Retransmitted final responses in Completed are silently absorbed.
	return fsm.Action{}
}

func (t *NonInviteClientTxn) goCompleted() fsm.Action {
	k := T4
	if t.reliableTransport {
		k = 0
	}
	timers := []fsm.TimerOp{
		{ID: nicTimerE, Cancel: true},
		{ID: nicTimerF, Cancel: true},
	}
	if k > 0 {
		timers = append(timers, fsm.TimerOp{
			ID: nicTimerK, After: k, OnFire: fsm.Event{Type: nicEvTimerK},
		})
		return fsm.Action{Next: NICCompleted, Timers: timers}
	}
	if t.OnTerminated != nil {
		t.OnTerminated()
	}
	return fsm.Action{Next: NICTerminated, Timers: timers}
}

func (t *NonInviteClientTxn) terminate() fsm.Action {
	if t.OnTerminated != nil {
		t.OnTerminated()
	}
	return fsm.Action{
		Next: NICTerminated,
		Timers: []fsm.TimerOp{
			{ID: nicTimerE, Cancel: true},
			{ID: nicTimerF, Cancel: true},
			{ID: nicTimerK, Cancel: true},
		},
	}
}

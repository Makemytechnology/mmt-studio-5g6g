// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SIP dialog state machine — RFC 3261 §12. Spec anchor:
// specs/ietf/rfc3261.txt.
//
// One libs/fsm.Machine per dialog: the handler is the only writer
// of dialog state, including LocalCSeq (which every in-dialog
// request has to increment atomically). External callers drive
// transitions through On…() / CreateRequest() / Terminate() which
// go through the event channel.
//
// States (§12.1 / §12.2):
//
//   DialogInit       — created, no to-tag learned yet
//   DialogEarly      — 1xx with to-tag received (or sent) → dialog
//                      ID stabilises
//   DialogConfirmed  — 2xx received (or sent)
//   DialogTerminated — BYE sent/received or fatal error
//
// The dialog ID tuple (Call-ID, local-tag, remote-tag) only becomes
// definitive once a response carrying a to-tag arrives; in
// DialogInit the remote-tag is empty and the dialog is referred to
// as a "pre-dialog" by the RFC.
package sip

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mmt/mmt-studio-core/libs/fsm"
)

// DialogState — dialog life-cycle per §12.1.
type DialogState int

const (
	DialogInit DialogState = iota
	DialogEarly
	DialogConfirmed
	DialogTerminated
)

func (s DialogState) String() string {
	switch s {
	case DialogInit:
		return "INIT"
	case DialogEarly:
		return "EARLY"
	case DialogConfirmed:
		return "CONFIRMED"
	case DialogTerminated:
		return "TERMINATED"
	}
	return "UNKNOWN"
}

// Events for the dialog FSM.
const (
	diEvProvisional   = iota // 1xx with a to-tag → Early
	diEv2xx                  // 2xx final → Confirmed
	diEvErrorResp            // 3xx-6xx → Terminated
	diEvTerminate            // BYE sent / received → Terminated
	diEvCreateRequest        // build an in-dialog request; Reply = *SipRequest
	diEvQuerySnapshot
)

// SipDialog is one SIP dialog per §12.
type SipDialog struct {
	// Immutable after construction.
	CallID   string
	LocalURI string
	LocalTag string

	// Mutable — written only by the handler goroutine.
	RemoteTag  string
	RemoteURI  string
	RouteSet   []string
	LocalCSeq  int
	RemoteCSeq int
	state      DialogState

	fsm      *fsm.Machine
	stopOnce sync.Once
}

// NewDialog creates a UAC-side dialog seeded with the local URI and
// tag. Remote URI / tag / route set are filled in by on-the-wire
// responses via OnProvisional / On2xx.
func NewDialog(callID, localURI, remoteURI, localTag string) *SipDialog {
	d := &SipDialog{
		CallID: callID, LocalURI: localURI, RemoteURI: remoteURI,
		LocalTag: localTag, state: DialogInit, LocalCSeq: 0,
	}
	d.fsm = fsm.New("dlg:"+callID, DialogInit, d.handle, 16)
	d.fsm.Start()
	return d
}

// NewDialogFromRequest creates a UAS-side dialog from an incoming
// INVITE (§12.1.1). The caller supplies the local-tag to be placed
// on outgoing responses.
func NewDialogFromRequest(req *SipRequest, localTag string) *SipDialog {
	callID := req.GetHeader(HdrCallID)
	fromHdr := req.GetHeader(HdrFrom)

	fromTag := ""
	for _, part := range strings.Split(fromHdr, ";") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, "tag=") {
			fromTag = strings.SplitN(p, "=", 2)[1]
		}
	}
	fromURI := ""
	if idx := strings.Index(fromHdr, "<"); idx >= 0 {
		if end := strings.Index(fromHdr[idx:], ">"); end > 0 {
			fromURI = fromHdr[idx+1 : idx+end]
		}
	}
	routeSet := req.GetHeaderValues(HdrRecordRoute)

	d := &SipDialog{
		CallID:    callID,
		LocalURI:  req.RequestURI,
		LocalTag:  localTag,
		RemoteTag: fromTag,
		RemoteURI: fromURI,
		RouteSet:  routeSet,
		LocalCSeq: 1,
		state:     DialogInit,
	}
	d.fsm = fsm.New("dlg:"+callID, DialogInit, d.handle, 16)
	d.fsm.Start()
	return d
}

// Stop releases the FSM goroutine.
func (d *SipDialog) Stop() { d.stopOnce.Do(func() { d.fsm.Stop() }) }

// State returns the current dialog state.
func (d *SipDialog) State() DialogState {
	s, _ := d.fsm.State().(DialogState)
	return s
}

// DialogID returns the §12.1 identifier tuple.
func (d *SipDialog) DialogID() [3]string {
	snap := d.Snapshot()
	return [3]string{
		snap["call_id"].(string),
		snap["local_tag"].(string),
		snap["remote_tag"].(string),
	}
}

// Snapshot returns a consistent view of the dialog state.
func (d *SipDialog) Snapshot() map[string]any {
	reply := make(chan any, 1)
	d.fsm.Send(fsm.Event{Type: diEvQuerySnapshot, Reply: reply})
	res, _ := (<-reply).(map[string]any)
	return res
}

// OnProvisional handles a 1xx response that carries a to-tag
// (§12.1.2). First such response moves Init → Early. Synchronous:
// returns after the transition has been applied.
func (d *SipDialog) OnProvisional(resp *SipResponse) DialogState {
	return d.syncTransition(diEvProvisional, map[string]any{"resp": resp})
}

// On2xx handles a 2xx final response — dialog becomes Confirmed.
// Synchronous.
func (d *SipDialog) On2xx(resp *SipResponse) DialogState {
	return d.syncTransition(diEv2xx, map[string]any{"resp": resp})
}

// OnError handles a 3xx-6xx final response — dialog terminates.
// Synchronous.
func (d *SipDialog) OnError(resp *SipResponse) DialogState {
	return d.syncTransition(diEvErrorResp, map[string]any{"resp": resp})
}

// syncTransition sends the event, waits for dispatch to complete,
// and returns the resulting state.
func (d *SipDialog) syncTransition(t int, data map[string]any) DialogState {
	reply := make(chan any, 1)
	d.fsm.Send(fsm.Event{Type: t, Data: data, Reply: reply})
	<-reply
	return d.State()
}

// Terminate ends the dialog (BYE sent or received, or operator decision).
func (d *SipDialog) Terminate() {
	reply := make(chan any, 1)
	d.fsm.Send(fsm.Event{Type: diEvTerminate, Reply: reply})
	<-reply
}

// CreateRequest builds an in-dialog request (BYE, re-INVITE, …) with
// an atomically-incremented local CSeq. Returns nil if the dialog is
// terminated.
func (d *SipDialog) CreateRequest(method string) *SipRequest {
	reply := make(chan any, 1)
	d.fsm.Send(fsm.Event{
		Type:  diEvCreateRequest,
		Data:  map[string]any{"method": method},
		Reply: reply,
	})
	res, _ := (<-reply).(*SipRequest)
	return res
}

// ── Handler (loop goroutine) ──

func (d *SipDialog) handle(st fsm.State, ev fsm.Event) fsm.Action {
	cs, _ := st.(DialogState)

	switch ev.Type {
	case diEvProvisional:
		return d.onProvisional(cs, ev)
	case diEv2xx:
		return d.on2xx(cs, ev)
	case diEvErrorResp:
		return d.onError(cs)
	case diEvTerminate:
		return fsm.Action{Next: DialogTerminated, Reply: true}
	case diEvCreateRequest:
		return d.onCreateRequest(cs, ev)
	case diEvQuerySnapshot:
		return fsm.Action{Reply: d.snapshot(cs)}
	}
	return fsm.Action{}
}

// §12.1.2: a 1xx with a to-tag establishes an early dialog. Second
// and subsequent provisionals just update the remote tag if it changed
// (rare but allowed for forking).
func (d *SipDialog) onProvisional(cs DialogState, ev fsm.Event) fsm.Action {
	resp, _ := ev.Data["resp"].(*SipResponse)
	if resp == nil {
		return fsm.Action{}
	}
	if cs == DialogTerminated {
		return fsm.Action{}
	}
	if tag := extractParam(resp.GetHeader(HdrTo), "tag"); tag != "" {
		d.RemoteTag = tag
	}
	if cs == DialogInit {
		return fsm.Action{Next: DialogEarly}
	}
	return fsm.Action{}
}

// §12.1.2: a 2xx final response confirms the dialog.
func (d *SipDialog) on2xx(cs DialogState, ev fsm.Event) fsm.Action {
	resp, _ := ev.Data["resp"].(*SipResponse)
	if resp == nil {
		return fsm.Action{}
	}
	if cs == DialogTerminated {
		return fsm.Action{}
	}
	if tag := extractParam(resp.GetHeader(HdrTo), "tag"); tag != "" {
		d.RemoteTag = tag
	}
	// Record-Route from 2xx forms the route set §12.1.2.
	if rrs := resp.GetHeaderValues(HdrRecordRoute); len(rrs) > 0 {
		d.RouteSet = rrs
	}
	return fsm.Action{Next: DialogConfirmed}
}

func (d *SipDialog) onError(cs DialogState) fsm.Action {
	if cs == DialogTerminated {
		return fsm.Action{}
	}
	return fsm.Action{Next: DialogTerminated}
}

// onCreateRequest builds an in-dialog request with CSeq bumped
// atomically. §12.2.1.1 "Generating the Request".
func (d *SipDialog) onCreateRequest(cs DialogState, ev fsm.Event) fsm.Action {
	if cs == DialogTerminated {
		return fsm.Action{Reply: (*SipRequest)(nil)}
	}
	method, _ := ev.Data["method"].(string)
	d.LocalCSeq++
	req := &SipRequest{
		SipMessage: SipMessage{Headers: make(map[string][]string)},
		Method:     method,
		RequestURI: d.RemoteURI,
	}
	req.SetHeader(HdrCallID, d.CallID)
	req.SetHeader(HdrFrom, BuildFrom("", d.LocalURI, d.LocalTag))
	req.SetHeader(HdrTo, BuildTo("", d.RemoteURI, d.RemoteTag))
	req.SetHeader(HdrCSeq, fmt.Sprintf("%d %s", d.LocalCSeq, method))
	req.SetHeader(HdrMaxForwards, "70")
	for _, r := range d.RouteSet {
		req.AddHeader(HdrRoute, r, false)
	}
	return fsm.Action{Reply: req}
}

func (d *SipDialog) snapshot(cs DialogState) map[string]any {
	return map[string]any{
		"call_id":     d.CallID,
		"local_uri":   d.LocalURI,
		"remote_uri":  d.RemoteURI,
		"local_tag":   d.LocalTag,
		"remote_tag":  d.RemoteTag,
		"state":       cs.String(),
		"local_cseq":  d.LocalCSeq,
		"route_set":   append([]string(nil), d.RouteSet...),
	}
}

// ── DialogManager ──

// DialogManager tracks active dialogs. It doesn't run its own FSM —
// the per-dialog FSMs do that. The manager is a concurrent-safe
// index keyed by the §12.1 identifier tuple.
type DialogManager struct {
	mu      sync.RWMutex
	dialogs map[[3]string]*SipDialog
}

func NewDialogManager() *DialogManager {
	return &DialogManager{dialogs: make(map[[3]string]*SipDialog)}
}

func (dm *DialogManager) Add(d *SipDialog) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.dialogs[d.DialogID()] = d
}

func (dm *DialogManager) Get(callID, localTag, remoteTag string) *SipDialog {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.dialogs[[3]string{callID, localTag, remoteTag}]
}

func (dm *DialogManager) FindByCallID(callID string) []*SipDialog {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	var out []*SipDialog
	for _, d := range dm.dialogs {
		if d.CallID == callID {
			out = append(out, d)
		}
	}
	return out
}

// Remove stops the dialog FSM and drops the index entry.
func (dm *DialogManager) Remove(d *SipDialog) {
	dm.mu.Lock()
	delete(dm.dialogs, d.DialogID())
	dm.mu.Unlock()
	d.Stop()
}

// ListActive returns non-terminated dialogs.
func (dm *DialogManager) ListActive() []*SipDialog {
	dm.mu.RLock()
	all := make([]*SipDialog, 0, len(dm.dialogs))
	for _, d := range dm.dialogs {
		all = append(all, d)
	}
	dm.mu.RUnlock()
	var out []*SipDialog
	for _, d := range all {
		if d.State() != DialogTerminated {
			out = append(out, d)
		}
	}
	return out
}

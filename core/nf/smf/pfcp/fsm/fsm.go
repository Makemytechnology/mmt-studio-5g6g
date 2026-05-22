// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Key identifies one PFCP session on the SMF side. We key by
// (UPF-node-id, SEID) — the remote UPF's node + the Session Endpoint
// ID assigned by the UPF on establishment. For now UPFNode carries the
// UPF's ID string (`upfmgr.Manager.UPFID`); SEID comes from the UPF's
// establishment response.
type Key struct {
	UPFNode string
	IMSI    string
	PDUSessionID uint8
}

func (k Key) String() string {
	return fmt.Sprintf("%s@%s/%d", k.UPFNode, k.IMSI, k.PDUSessionID)
}

// Context carries per-event data into Actions.
type Context struct {
	Key     Key
	Event   Event
	SEID    uint64 // set by the UPF on EstablishResponse
	Cause   uint8  // PFCP Cause IE on response
	Reason  error
}

// Action runs on a transition; non-nil error aborts the advance.
type Action func(c *Context) error

// TimerSpec declares a per-session PFCP retransmission timer.
type TimerSpec struct {
	Name          string
	Duration      time.Duration
	OnExpiry      Event
	Retransmit    timers.Callback
	MaxRetransmit int
	Interval      time.Duration

	// Log-only metadata forwarded to the timer manager so retransmit /
	// expiry log lines name the PFCP request being awaited
	// (TS 29.244 §7.5 message types). Empty disables.
	Description string
	Awaiting    string
}

// Transition is one arrow in the PFCP session graph.
type Transition struct {
	From        State
	Event       Event
	To          State
	Guard       func(c *Context) bool
	Action      Action
	StartTimers []TimerSpec
	StopTimers  []string
}

// FSM is the per-PFCP-session engine.
type FSM struct {
	mu       sync.Mutex
	state    State
	key      Key
	seid     uint64 // assigned by UPF on establish
	table    []Transition
	timerMgr *timers.Manager
	log      *logger.Logger
}

var (
	defaultTableMu sync.RWMutex
	defaultTable   []Transition
)

// SetDefaultTable installs the transition graph. Called from the
// owning smf/pfcp package at init.
func SetDefaultTable(t []Transition) {
	defaultTableMu.Lock()
	defaultTable = t
	defaultTableMu.Unlock()
}

// New returns an Inactive FSM for the given PFCP session key.
func New(k Key) *FSM {
	defaultTableMu.RLock()
	t := defaultTable
	defaultTableMu.RUnlock()
	return &FSM{
		state:    StateInactive,
		key:      k,
		table:    t,
		timerMgr: timers.M,
		log:      logger.Get("smf.pfcp.fsm"),
	}
}

// State returns the current state.
func (f *FSM) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// SEID returns the UPF-assigned Session Endpoint ID (0 until the UPF
// responds to Establish).
func (f *FSM) SEID() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seid
}

// Fire processes one event. A "no transition" match logs a WARN for
// operator visibility (PFCP procedure collision / out-of-order reply).
func (f *FSM) Fire(c *Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	cur := f.state
	for i := range f.table {
		t := &f.table[i]
		if t.From != cur || t.Event != c.Event {
			continue
		}
		if t.Guard != nil && !t.Guard(c) {
			continue
		}
		if t.Action != nil {
			if err := t.Action(c); err != nil {
				f.log.WithIMSI(f.key.IMSI).Warnf("PFCP FSM action %s(%s → %s) upf=%s pduSessID=%d failed: %v",
					c.Event, cur, t.To, f.key.UPFNode, f.key.PDUSessionID, err)
				return err
			}
		}
		for _, name := range t.StopTimers {
			f.timerMgr.Cancel(name, f.timerKey())
		}
		// Capture SEID when the UPF issues one.
		if c.SEID != 0 {
			f.seid = c.SEID
		}
		f.state = t.To
		for _, spec := range t.StartTimers {
			f.startTimer(spec)
		}
		if t.From == t.To {
			f.log.WithIMSI(f.key.IMSI).Debugf("PFCP upf=%s pduSessID=%d @ %s on %s",
				f.key.UPFNode, f.key.PDUSessionID, cur, c.Event)
		} else {
			f.log.WithIMSI(f.key.IMSI).Infof("PFCP upf=%s pduSessID=%d: %s → %s on %s",
				f.key.UPFNode, f.key.PDUSessionID, cur, t.To, c.Event)
		}
		return nil
	}
	f.log.WithIMSI(f.key.IMSI).Warnf("PFCP procedure collision upf=%s pduSessID=%d: rejected %s in state %s",
		f.key.UPFNode, f.key.PDUSessionID, c.Event, cur)
	return fmt.Errorf("no PFCP transition for %s in state %s (%s)", c.Event, cur, f.key)
}

// FireTimer feeds a timer-expiry event back into the FSM.
func (f *FSM) FireTimer(ev Event) {
	_ = f.Fire(&Context{Key: f.key, Event: ev})
}

func (f *FSM) timerKey() string {
	return fmt.Sprintf("pfcp#%s/%s/%d", f.key.UPFNode, f.key.IMSI, f.key.PDUSessionID)
}

func (f *FSM) startTimer(spec TimerSpec) {
	if spec.Name == "" || spec.Duration == 0 {
		return
	}
	ev := spec.OnExpiry
	opts := timers.Options{
		Retransmit:    spec.Retransmit,
		MaxRetransmit: spec.MaxRetransmit,
		MaxInterval:   spec.Interval,
		Description:   spec.Description,
		Awaiting:      spec.Awaiting,
	}
	f.timerMgr.Start(spec.Name, f.timerKey(), spec.Duration,
		func() { f.FireTimer(ev) }, opts)
}

// ─── Per-session registry ────────────────────────────────────────────

var (
	fsmRegMu sync.RWMutex
	fsmReg   = map[Key]*FSM{}
)

// Of returns the FSM for this key, creating a fresh Inactive one on
// first access.
func Of(k Key) *FSM {
	fsmRegMu.RLock()
	f, ok := fsmReg[k]
	fsmRegMu.RUnlock()
	if ok {
		return f
	}
	fsmRegMu.Lock()
	defer fsmRegMu.Unlock()
	if f, ok = fsmReg[k]; ok {
		return f
	}
	f = New(k)
	fsmReg[k] = f
	return f
}

// Drop removes the FSM entry (call after Delete is confirmed).
func Drop(k Key) {
	fsmRegMu.Lock()
	delete(fsmReg, k)
	fsmRegMu.Unlock()
}

// All returns every active PFCP session FSM for /api/smf/pfcp and
// debugging.
func All() []*FSM {
	fsmRegMu.RLock()
	defer fsmRegMu.RUnlock()
	out := make([]*FSM, 0, len(fsmReg))
	for _, f := range fsmReg {
		out = append(out, f)
	}
	return out
}

// Key exposes the session key from outside.
func (f *FSM) KeyOf() Key { return f.key }

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Key identifies a single PDU session in the SMF. Matches the
// (IMSI, PDUSessionID) tuple the existing session.Store already uses;
// kept here as a value type (not a pointer into session) so the FSM
// package can stay import-free of nf/smf/session and avoid cycles.
type Key struct {
	IMSI         string
	PDUSessionID uint8
}

// String renders the key for log lines.
func (k Key) String() string {
	return fmt.Sprintf("%s/%d", k.IMSI, k.PDUSessionID)
}

// Context is handed to every Action as the FSM processes an event.
// Msg / Payload carry any event-specific data the Action needs (raw
// 5GSM bytes, PFCP response body, gNB TEID, …).
type Context struct {
	Key     Key
	Event   Event
	Msg     interface{}
	Payload []byte
	Reason  error
}

// Action runs when a transition fires. A non-nil return aborts the
// state advance — the FSM stays in the From-state and the transition's
// timer operations are skipped.
type Action func(c *Context) error

// TimerSpec declares a per-session timer to arm on transition entry.
// OnExpiry is the event re-fed into the FSM when the timer fires.
type TimerSpec struct {
	Name          string
	Duration      time.Duration
	OnExpiry      Event
	Retransmit    timers.Callback
	MaxRetransmit int
	Interval      time.Duration

	// Log-only metadata forwarded to the timer manager so retransmit /
	// expiry log lines name the procedure (TS 24.501 §10.3 5GSM
	// timers). Empty disables.
	Description string
	Awaiting    string
}

// Transition is one arrow in the 5GSM FSM graph.
type Transition struct {
	From        State
	Event       Event
	To          State
	Guard       func(c *Context) bool
	Action      Action
	StartTimers []TimerSpec
	StopTimers  []string
}

// FSM is the per-PDU-session engine. One instance per (IMSI,
// PDUSessionID); look up via Of.
type FSM struct {
	mu       sync.Mutex
	state    State
	key      Key
	table    []Transition
	timerMgr *timers.Manager
	log      *logger.Logger
}

var (
	defaultTableMu sync.RWMutex
	defaultTable   []Transition
)

// SetDefaultTable installs the transition graph every subsequent Of
// call will use. Called from nf/smf/session (the owning package) at
// init time; nil clears.
func SetDefaultTable(t []Transition) {
	defaultTableMu.Lock()
	defaultTable = t
	defaultTableMu.Unlock()
}

// New returns an Inactive FSM for the given PDU session key.
func New(k Key) *FSM {
	defaultTableMu.RLock()
	t := defaultTable
	defaultTableMu.RUnlock()
	return &FSM{
		state:    StateInactive,
		key:      k,
		table:    t,
		timerMgr: timers.M,
		log:      logger.Get("smf.session.fsm"),
	}
}

// State returns the current state (cheap read).
func (f *FSM) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Fire runs one event through the transition table.
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
				f.log.WithIMSI(f.key.IMSI).Warnf("5GSM FSM action %s(%s → %s) pduSessID=%d failed: %v",
					c.Event, cur, t.To, f.key.PDUSessionID, err)
				return err
			}
		}
		for _, name := range t.StopTimers {
			f.timerMgr.Cancel(name, f.timerKey())
		}
		f.state = t.To
		for _, spec := range t.StartTimers {
			f.startTimer(spec)
		}
		f.log.WithIMSI(f.key.IMSI).Infof("5GSM pduSessID=%d: %s → %s on %s",
			f.key.PDUSessionID, cur, t.To, c.Event)
		return nil
	}
	return fmt.Errorf("no transition for %s in state %s (session %s)", c.Event, cur, f.key)
}

// FireTimer feeds a timer-expiry event back into the FSM.
func (f *FSM) FireTimer(ev Event) {
	_ = f.Fire(&Context{Key: f.key, Event: ev})
}

func (f *FSM) timerKey() string {
	return fmt.Sprintf("%s/%d", f.key.IMSI, f.key.PDUSessionID)
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

// ─── Per-session FSM registry ────────────────────────────────────────

var (
	fsmRegMu sync.RWMutex
	fsmReg   = map[Key]*FSM{}
)

// Of returns the FSM for this key, creating a fresh StateInactive one
// on first access.
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

// Drop removes the FSM for a session — called once the session is
// fully released and the Store entry is gone.
func Drop(k Key) {
	fsmRegMu.Lock()
	delete(fsmReg, k)
	fsmRegMu.Unlock()
}

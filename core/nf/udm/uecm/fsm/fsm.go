// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Key identifies one UECM association. TS 29.503 §6.2.6 keys
// registrations by SUPI in the URI
// /nudm-uecm/v1/{supi}/registrations/amf-3gpp-access — we use the
// bare IMSI (SUPI digits) so the FSM registry matches the rest of the
// in-process state (amfUeCtx, sessionfsm, smpolicyfsm).
type Key struct {
	IMSI string
}

// String renders the key for log lines.
func (k Key) String() string { return k.IMSI }

// Context is handed to every Action as the FSM processes an event.
// AmfUeNgapID / AmfName are carried on Register events so an Action
// can read them without a separate map lookup.
type Context struct {
	Key         Key
	Event       Event
	AmfUeNgapID int64
	AmfName     string
	Reason      error
}

// Action runs when a transition fires. Non-nil return aborts the
// advance; the FSM stays in the From-state.
type Action func(c *Context) error

// Transition is one arrow in the UECM FSM graph.
type Transition struct {
	From   State
	Event  Event
	To     State
	Guard  func(c *Context) bool
	Action Action
}

// FSM is the per-UE engine. One instance per IMSI; looked up via Of.
type FSM struct {
	mu    sync.Mutex
	state State
	key   Key
	table []Transition
	log   *logger.Logger
}

var (
	defaultTableMu sync.RWMutex
	defaultTable   []Transition
)

// SetDefaultTable installs the transition graph used by every
// subsequent Of. Called from nf/udm/uecm/transitions.go init.
func SetDefaultTable(t []Transition) {
	defaultTableMu.Lock()
	defaultTable = t
	defaultTableMu.Unlock()
}

// New returns a Deregistered FSM for the given association key.
func New(k Key) *FSM {
	defaultTableMu.RLock()
	t := defaultTable
	defaultTableMu.RUnlock()
	return &FSM{
		state: StateDeregistered,
		key:   k,
		table: t,
		log:   logger.Get("udm.uecm.fsm"),
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
				f.log.WithIMSI(f.key.IMSI).Warnf("UECM FSM action %s(%s → %s) failed: %v",
					c.Event, cur, t.To, err)
				return err
			}
		}
		f.state = t.To
		f.log.WithIMSI(f.key.IMSI).Infof("UECM: %s → %s on %s", cur, t.To, c.Event)
		return nil
	}
	return fmt.Errorf("no transition for %s in state %s (association %s)", c.Event, cur, f.key)
}

// ─── Per-association FSM registry ────────────────────────────────────

var (
	fsmRegMu sync.RWMutex
	fsmReg   = map[Key]*FSM{}
)

// Of returns the FSM for this key, creating a fresh StateDeregistered
// one on first access.
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

// Drop removes the FSM for an association — called once Deregistration
// completes.
func Drop(k Key) {
	fsmRegMu.Lock()
	delete(fsmReg, k)
	fsmRegMu.Unlock()
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Key identifies one AF session. TS 29.514 keys by an opaque
// appSessionId URI allocated by the PCF on Create; the in-process
// port reuses the AF's sessionID string (e.g. "af-sess-00042") so
// the FSM registry lines up with the AFSessionManager's map.
type Key struct {
	SessionID string
}

// String renders the key for log lines.
func (k Key) String() string { return k.SessionID }

// Context is handed to every Action as the FSM processes an event.
// Decision / Reason are carried on outcome events for future Actions.
type Context struct {
	Key    Key
	Event  Event
	Reason error
}

// Action runs when a transition fires. Non-nil return aborts the
// advance; the FSM stays in the From-state.
type Action func(c *Context) error

// Transition is one arrow in the AF session FSM graph.
type Transition struct {
	From   State
	Event  Event
	To     State
	Guard  func(c *Context) bool
	Action Action
}

// FSM is the per-session engine.
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
// subsequent Of. Called from the parent af package's init.
func SetDefaultTable(t []Transition) {
	defaultTableMu.Lock()
	defaultTable = t
	defaultTableMu.Unlock()
}

// New returns a StateInitial FSM for the given session key.
func New(k Key) *FSM {
	defaultTableMu.RLock()
	t := defaultTable
	defaultTableMu.RUnlock()
	return &FSM{
		state: StateInitial,
		key:   k,
		table: t,
		log:   logger.Get("af.fsm"),
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
				f.log.Warnf("AF FSM action %s(%s → %s) for %s failed: %v",
					c.Event, cur, t.To, f.key, err)
				return err
			}
		}
		f.state = t.To
		f.log.Infof("AF %s: %s → %s on %s", f.key, cur, t.To, c.Event)
		return nil
	}
	return fmt.Errorf("no transition for %s in state %s (af session %s)", c.Event, cur, f.key)
}

// ─── Per-session FSM registry ────────────────────────────────────────

var (
	fsmRegMu sync.RWMutex
	fsmReg   = map[Key]*FSM{}
)

// Of returns the FSM for this key, creating a fresh StateInitial one
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

// Drop removes the FSM for a session — called once Delete completes.
func Drop(k Key) {
	fsmRegMu.Lock()
	delete(fsmReg, k)
	fsmRegMu.Unlock()
}

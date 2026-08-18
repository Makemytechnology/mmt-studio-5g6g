// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Key identifies one SM Policy Association. TS 29.512 keys the
// association by an opaque smPolicyCtxRef URI allocated by the PCF
// on Create (§4.2.2.2 step 2); this in-process port uses the same
// (IMSI, PDUSessionID) pair that indexes the 5GSM FSM at
// nf/smf/session/fsm so the two state machines can be looked up
// from the same context without a separate ref map.
type Key struct {
	IMSI         string
	PDUSessionID uint8
}

// String renders the key for log lines.
func (k Key) String() string {
	return fmt.Sprintf("%s/%d", k.IMSI, k.PDUSessionID)
}

// Context is handed to every Action as the FSM processes an event.
// Decision carries the SmPolicyDecision-equivalent payload on
// Create/Update/UpdateNotify (left as interface{} to avoid an
// import cycle with the pcf package).
type Context struct {
	Key      Key
	Event    Event
	Decision interface{}
	Reason   error
}

// Action runs when a transition fires. A non-nil return aborts the
// state advance.
type Action func(c *Context) error

// TimerSpec declares a per-association timer to arm on transition
// entry. The Revalidation Timer (TS 29.512 §4.2.2.4) is the only
// spec-defined timer on this FSM today; the value is dynamic (comes
// from the PCF's revalidationTime attribute), so StartTimers is
// usually armed programmatically via ArmTimer rather than declared
// statically — kept here for symmetry with session/fsm.
type TimerSpec struct {
	Name     string
	Duration time.Duration
	OnExpiry Event

	// Log-only metadata forwarded to the timer manager so retransmit /
	// expiry log lines name the SM-Policy procedure being awaited
	// (TS 29.512 §4.2). Empty disables.
	Description string
	Awaiting    string
}

// Transition is one arrow in the SM Policy FSM graph.
type Transition struct {
	From        State
	Event       Event
	To          State
	Guard       func(c *Context) bool
	Action      Action
	StartTimers []TimerSpec
	StopTimers  []string
}

// FSM is the per-association engine. One instance per (IMSI,
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
// call will use. The owning nf/pcf/smpolicy package calls this at
// init.
func SetDefaultTable(t []Transition) {
	defaultTableMu.Lock()
	defaultTable = t
	defaultTableMu.Unlock()
}

// New returns a StateNone FSM for the given association key.
func New(k Key) *FSM {
	defaultTableMu.RLock()
	t := defaultTable
	defaultTableMu.RUnlock()
	return &FSM{
		state:    StateNone,
		key:      k,
		table:    t,
		timerMgr: timers.M,
		log:      logger.Get("pcf.smpolicy.fsm"),
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
				f.log.WithIMSI(f.key.IMSI).Warnf("SMPolicy FSM action %s(%s → %s) pduSessID=%d failed: %v",
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
		f.log.WithIMSI(f.key.IMSI).Infof("SMPolicy pduSessID=%d: %s → %s on %s",
			f.key.PDUSessionID, cur, t.To, c.Event)
		return nil
	}
	return fmt.Errorf("no transition for %s in state %s (association %s)", c.Event, cur, f.key)
}

// FireTimer feeds a timer-expiry event back into the FSM.
func (f *FSM) FireTimer(ev Event) {
	_ = f.Fire(&Context{Key: f.key, Event: ev})
}

// ArmRevalidationTimer starts the TS 29.512 §4.2.2.4 Revalidation
// Timer with the duration carried by the PCF in the
// revalidationTime attribute. Cancels any previous instance.
func (f *FSM) ArmRevalidationTimer(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timerMgr.Cancel("T_Revalidation", f.timerKey())
	if d <= 0 {
		return
	}
	f.timerMgr.Start("T_Revalidation", f.timerKey(), d,
		func() { f.FireTimer(EvRevalidationTimerExpired) },
		timers.Options{
			Description: "PCF revalidation guard (TS 29.512 §4.2.2.4)",
			Awaiting:    "PCF-initiated SMPolicyControlUpdate trigger",
		})
}

// CancelRevalidationTimer removes the timer without firing.
func (f *FSM) CancelRevalidationTimer() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.timerMgr.Cancel("T_Revalidation", f.timerKey())
}

func (f *FSM) timerKey() string {
	return fmt.Sprintf("%s/%d", f.key.IMSI, f.key.PDUSessionID)
}

func (f *FSM) startTimer(spec TimerSpec) {
	if spec.Name == "" || spec.Duration == 0 {
		return
	}
	ev := spec.OnExpiry
	f.timerMgr.Start(spec.Name, f.timerKey(), spec.Duration,
		func() { f.FireTimer(ev) },
		timers.Options{Description: spec.Description, Awaiting: spec.Awaiting})
}

// ─── Per-association FSM registry ────────────────────────────────────

var (
	fsmRegMu sync.RWMutex
	fsmReg   = map[Key]*FSM{}
)

// Of returns the FSM for this key, creating a fresh StateNone one on
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

// Drop removes the FSM for an association — called once Delete
// completes.
func Drop(k Key) {
	fsmRegMu.Lock()
	delete(fsmReg, k)
	fsmRegMu.Unlock()
}

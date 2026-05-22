// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package fsm

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Context carries everything an Action needs to process one event. Kept
// as a pointer so Actions can attach outbound payloads for the caller
// (NAS bytes, NGAP bytes, timer overrides) without fanning the Action
// signature wider every time a new need appears.
type Context struct {
	UE      *uectx.AmfUeCtx
	Event   Event
	Msg     interface{} // decoded NAS message when the event was inbound-NAS
	Inner   []byte      // raw plain NAS bytes
	MsgType uint8       // raw 5GMM message type byte
	Reason  error       // set by Guards that want to communicate rejection cause

	// GnbKey is the gNB association the event arrived on (empty for
	// timer-driven events). Actions that need to look up the live
	// *gnbctx.GnbCtx import gnbctx directly — we don't put the struct
	// pointer here because ctx.fsm shouldn't import gnbctx (it would
	// pull half the NGAP stack into a table).
	GnbKey string
}

// Action runs as part of a transition. Returning a non-nil error aborts
// the state advance: the FSM logs, stays in the from-state, and does
// not apply the transition's timer operations.
type Action func(c *Context) error

// TimerSpec declares a timer to start when a transition lands in its
// "to" state. OnExpiry is the event re-fed into the FSM when the timer
// fires; MaxRetransmit + Retransmit configure the retransmission stream
// (see infra/timers Options).
//
// Duration here is the *per-shot* cadence (T3560=6s etc.). When
// MaxRetransmit>0 the effective expiry is Duration×(MaxRetransmit+1)
// — this is what matches TS 24.501 §10.2 Table 10.2.1 ("N retransmits
// at T seconds each"). startTimer below does that math so transition
// rows stay readable.
type TimerSpec struct {
	Name          string
	Duration      time.Duration
	OnExpiry      Event
	Retransmit    UERetransmit // per-retransmit hook, invoked with the UE
	MaxRetransmit int          // 0 = no retransmissions
	Interval      time.Duration // retransmit cadence (0 = Duration)

	// Log-only metadata. Forwarded into timers.Options so the
	// timer-manager retransmit / expiry log lines are self-explanatory
	// per-UE — see infra/timers/manager.go logEvent + the pairing
	// "Spec citations live in code" convention. Empty disables.
	Description string // e.g. "Registration Accept retransmit guard (TS 24.501 §5.5.1.2.4)"
	Awaiting    string // e.g. "Registration Complete from UE"
}

// UERetransmit is the per-shot hook the FSM invokes up to MaxRetransmit
// times before declaring the timer expired. Receives the UE the timer
// is armed for so the closure can re-emit the original NAS PDU stored
// on uectx.
type UERetransmit func(*uectx.AmfUeCtx)

// Transition is one arrow in the FSM graph.
type Transition struct {
	From   State
	Event  Event
	To     State
	Guard  func(c *Context) bool // optional; when nil, transition always fires
	Action Action                // optional; when nil, state just advances
	// Start these timers on entering `To`.
	StartTimers []TimerSpec
	// Cancel these timers on leaving `From` (by timer name).
	StopTimers []string
}

// FSM is the per-UE state machine. One instance per AmfUeCtx; looked up
// via Of(ue). Protected by its own mutex — the GMM dispatcher fires
// events serially per UE, but NGAP / timer callbacks can race.
type FSM struct {
	mu       sync.Mutex
	state    State
	ue       *uectx.AmfUeCtx
	table    []Transition
	timerMgr *timers.Manager
	log      *logger.Logger
}

// defaultTable is the process-wide transition graph. Registered by the
// owning package (gmm) at init via SetDefaultTable — keeps the FSM
// engine a pure mechanism and lets the actual NAS actions live in gmm
// where they can import dlnas, session, etc. without cycling back.
var (
	defaultTableMu sync.RWMutex
	defaultTable   []Transition
)

// SetDefaultTable installs the transition graph every subsequent Of(ue)
// call will use. Safe to call multiple times (tests); the last call wins.
func SetDefaultTable(t []Transition) {
	defaultTableMu.Lock()
	defaultTable = t
	defaultTableMu.Unlock()
}

// New returns a DEREGISTERED FSM for the given UE.
func New(ue *uectx.AmfUeCtx) *FSM {
	defaultTableMu.RLock()
	t := defaultTable
	defaultTableMu.RUnlock()
	return &FSM{
		state:    StateDeregistered,
		ue:       ue,
		table:    t,
		timerMgr: timers.M,
		log:      logger.Get("amf.gmm.fsm"),
	}
}

// State returns the current state (read lock).
func (f *FSM) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Fire drives the FSM with an inbound event. It:
//
//  1. Finds the first transition whose From matches current state, whose
//     Event matches, and whose Guard (if any) returns true.
//  2. Runs the Action. On error, logs and returns without advancing.
//  3. Cancels StopTimers, advances state, starts StartTimers.
//  4. Logs the transition (from → to, event, ms elapsed).
//
// Returns an error if no transition matched. Callers typically log and
// swallow — an unexpected event in a state is usually a UE misbehaviour,
// not an AMF bug. (Procedure-collision is the one exception; see the
// EvRegistrationRequest transitions which are legal from multiple states.)
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
		// Run action first so if it fails we never leave the from-state.
		if t.Action != nil {
			if err := t.Action(c); err != nil {
				f.log.WithIMSI(f.ue.IMSI).Warnf("FSM action %s(%s → %s) failed: %v",
					c.Event, cur, t.To, err)
				return err
			}
		}
		// Commit the transition.
		for _, name := range t.StopTimers {
			f.timerMgr.Cancel(name, f.ueKey())
		}
		f.state = t.To
		// Mirror into the legacy uectx fields so the GUI / other readers
		// that still grep for ue.GMMProc see the new state. Purely
		// informational — source of truth is f.state.
		f.mirrorToUECtx(t.To)
		for _, spec := range t.StartTimers {
			f.startTimer(spec)
		}
		f.log.WithIMSI(f.ue.IMSI).Infof("FSM %s → %s on %s",
			cur, t.To, c.Event)
		return nil
	}
	return fmt.Errorf("no transition for %s in state %s", c.Event, cur)
}

// FireTimer is a convenience for timer callbacks to feed the FSM an
// expiry event without constructing a Context manually.
func (f *FSM) FireTimer(ev Event) {
	_ = f.Fire(&Context{UE: f.ue, Event: ev})
}

// ueKey returns the timer-manager key for this FSM's UE (matches what
// handlers previously used as the second argument to timers.M.Start).
func (f *FSM) ueKey() string {
	return fmt.Sprintf("%d", f.ue.AmfUeNGAPID)
}

// startTimer installs one TimerSpec. OnExpiry → FireTimer(ev) loops the
// expiry back into the FSM, turning "timer fired" into a first-class
// event the transition table can react to.
func (f *FSM) startTimer(spec TimerSpec) {
	if spec.Name == "" || spec.Duration == 0 {
		return
	}
	ev := spec.OnExpiry

	// TS 24.501 §10.2: each NAS retransmit timer fires every Duration
	// seconds for MaxRetransmit shots then declares final expiry —
	// total = Duration × (MaxRetransmit + 1). Let transitions name the
	// per-shot cadence; compute the real timer-manager expiry here so
	// the retransmit branch in timers.tick() actually gets MaxRetransmit
	// ticks before the expires check trips.
	interval := spec.Interval
	if interval == 0 {
		interval = spec.Duration
	}
	total := spec.Duration
	var retxCB timers.Callback
	if spec.MaxRetransmit > 0 && spec.Retransmit != nil {
		total = interval * time.Duration(spec.MaxRetransmit+1)
		ue := f.ue
		hook := spec.Retransmit
		retxCB = func() { hook(ue) }
	}
	opts := timers.Options{
		Retransmit:    retxCB,
		MaxRetransmit: spec.MaxRetransmit,
		MaxInterval:   interval,
		Description:   spec.Description,
		Awaiting:      spec.Awaiting,
	}
	f.timerMgr.Start(spec.Name, f.ueKey(), total,
		func() { f.FireTimer(ev) }, opts)
}

// mirrorToUECtx keeps the legacy RM/CM/GMMProc/GMMSub fields in rough
// sync with the new FSM state so the existing GUI, logs, and tests
// continue to render something sensible. Will be removed once every
// consumer reads FSMOf(ue).State() directly.
func (f *FSM) mirrorToUECtx(s State) {
	switch s {
	case StateDeregistered:
		f.ue.RM = uectx.RMDeregistered
		f.ue.GMMProc = uectx.GMMProcNone
		f.ue.GMMSub = uectx.GMMSubNone
	case StateIdentification:
		f.ue.GMMProc = uectx.GMMProcRegistration
		f.ue.GMMSub = uectx.GMMSubIdentification
	case StateAuthentication:
		f.ue.GMMProc = uectx.GMMProcRegistration
		f.ue.GMMSub = uectx.GMMSubAuthentication
	case StateSecurityMode:
		f.ue.GMMProc = uectx.GMMProcRegistration
		f.ue.GMMSub = uectx.GMMSubSecurityMode
	case StateRegisteredInitiated:
		f.ue.GMMProc = uectx.GMMProcRegistration
		f.ue.GMMSub = uectx.GMMSubNone
	case StateRegistered:
		f.ue.RM = uectx.RMRegistered
		f.ue.GMMProc = uectx.GMMProcNone
		f.ue.GMMSub = uectx.GMMSubNone
	case StateDeregistrationInitiated:
		f.ue.GMMProc = uectx.GMMProcDeregistration
		f.ue.GMMSub = uectx.GMMSubNone
	case StateMTDeregPending:
		f.ue.GMMProc = uectx.GMMProcDeregistration
		f.ue.GMMSub = uectx.GMMSubNone
	}
}

// ─── Per-UE FSM registry ──────────────────────────────────────────────

var (
	fsmRegMu sync.RWMutex
	fsmReg   = map[*uectx.AmfUeCtx]*FSM{}
)

// Of returns the FSM for this UE, creating a fresh StateDeregistered one
// on first access. The FSM is pinned to the UE pointer — callers should
// keep the same AmfUeCtx for the UE's lifetime (which the existing
// uectx.Registry already guarantees).
func Of(ue *uectx.AmfUeCtx) *FSM {
	fsmRegMu.RLock()
	f, ok := fsmReg[ue]
	fsmRegMu.RUnlock()
	if ok {
		return f
	}
	fsmRegMu.Lock()
	defer fsmRegMu.Unlock()
	if f, ok = fsmReg[ue]; ok {
		return f
	}
	f = New(ue)
	fsmReg[ue] = f
	return f
}

// Drop removes the FSM for a UE when the context is being released.
// Safe to call even if no FSM was created.
func Drop(ue *uectx.AmfUeCtx) {
	fsmRegMu.Lock()
	delete(fsmReg, ue)
	fsmRegMu.Unlock()
}

// ResetTo forces the FSM into state `s` without running any transition
// action or timer adjustments. It is the spec-side-channel used by the
// NAS lower-layer-failure abort path (TS 24.501 §5.5.1.2.8(a) and
// §5.5.1.3.8(a)) which call for a state change *outside* the regular
// FSM event flow — both clauses prescribe a state the AMF "shall enter"
// after locally aborting the in-flight procedure, with no inbound NAS
// event to fire a transition on.
//
// mirrorToUECtx still runs so legacy ue.RM / ue.GMMProc readers see the
// new state. No Fire log line is emitted; the caller is expected to
// log the spec citation that motivated the reset.
func (f *FSM) ResetTo(s State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = s
	f.mirrorToUECtx(s)
}

// init registers Drop as a removal hook on the default UE registry so
// every uectx.Default.Remove(ue) / RemoveAllForGnb automatically wipes
// the per-UE GMM FSM entry. Without this, fsmReg kept a live pointer
// to every removed ctx indefinitely — a memory leak across the UE
// lifecycle (measured in N × registration cycles).
func init() {
	uectx.Default.RegisterRemoveHook(Drop)
}

// Snapshot is a point-in-time view of a GMM FSM suitable for JSON.
type Snapshot struct {
	IMSI        string `json:"imsi"`
	AmfUeNGAPID int64  `json:"amf_ue_ngap_id"`
	State       string `json:"state"`
}

// AllSnapshots returns current state of every live GMM FSM.
func AllSnapshots() []Snapshot {
	fsmRegMu.RLock()
	defer fsmRegMu.RUnlock()
	out := make([]Snapshot, 0, len(fsmReg))
	for ue, f := range fsmReg {
		out = append(out, Snapshot{
			IMSI: ue.IMSI, AmfUeNGAPID: ue.AmfUeNGAPID,
			State: f.State().String(),
		})
	}
	return out
}

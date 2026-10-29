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

// Key identifies a single NGAP UE association — a combination of the
// gNB (by its NGAP association ID / connection key) and the AMF-UE-
// NGAP-ID allocated on that association. The same UE that handed over
// to a different gNB would get a fresh Key (new association) — the
// old one is torn down via the mobility procedures, not this FSM.
type Key struct {
	GnbKey      string
	AMFUENGAPID int64
}

// String renders the key compactly.
func (k Key) String() string {
	return fmt.Sprintf("%s/amfUeID=%d", k.GnbKey, k.AMFUENGAPID)
}

// Context is what an Action sees.
type Context struct {
	Key     Key
	Event   Event
	Msg     interface{}
	Payload []byte
	Reason  error
	// PDUSessionID lets procedures like PDU Session Resource Setup,
	// which can fork per-session from the same UE FSM state, tell
	// actions which session the event refers to. Zero when the event
	// is UE-wide (ICS, release, paging).
	PDUSessionID uint8
}

// Action runs on a transition. Returning non-nil aborts the state
// advance.
type Action func(c *Context) error

// TimerSpec declares a timer to arm on transition entry. OnExpiry is
// fed back into the FSM as an Event.
type TimerSpec struct {
	Name          string
	Duration      time.Duration
	OnExpiry      Event
	Retransmit    timers.Callback
	MaxRetransmit int
	Interval      time.Duration

	// Log-only metadata forwarded to the timer manager so retransmit /
	// expiry log lines are self-explanatory per-UE. See
	// infra/timers/manager.go logEvent. Empty disables.
	Description string // e.g. "InitialContextSetup response wait (TS 38.413 §8.3.1)"
	Awaiting    string // e.g. "InitialContextSetupResponse from gNB"
}

// Transition is one arrow in the NGAP UE-association graph.
type Transition struct {
	From        State
	Event       Event
	To          State
	Guard       func(c *Context) bool
	Action      Action
	StartTimers []TimerSpec
	StopTimers  []string
}

// FSM is the per-association engine.
type FSM struct {
	mu              sync.Mutex
	state           State
	key             Key
	table           []Transition
	timerMgr        *timers.Manager
	log             *logger.Logger
	pendingSessions int // ref-count for parallel PDU Session Resource forks
}

var (
	defaultTableMu sync.RWMutex
	defaultTable   []Transition
)

// SetDefaultTable installs the transition graph — called from the
// owning ngap package at init.
func SetDefaultTable(t []Transition) {
	defaultTableMu.Lock()
	defaultTable = t
	defaultTableMu.Unlock()
}

// New returns a NotEstablished FSM for the given UE association.
func New(k Key) *FSM {
	defaultTableMu.RLock()
	t := defaultTable
	defaultTableMu.RUnlock()
	return &FSM{
		state:    StateNotEstablished,
		key:      k,
		table:    t,
		timerMgr: timers.M,
		log:      logger.Get("amf.ngap.fsm"),
	}
}

// State returns the current state.
func (f *FSM) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// PendingSessions returns the count of in-flight PDU Session Resource
// Setup procedures (race-safe counter tracked alongside state).
func (f *FSM) PendingSessions() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pendingSessions
}

// TrackResourceSetupRequest increments the pending-session counter
// and returns the new count. PDU Session Resource Setup can run in
// parallel across sessions for the same UE; the FSM enters
// RESOURCE_SETUP_PENDING on the first and returns to ESTABLISHED on
// the last. Callers use this helper instead of firing the transition
// blindly so state advances happen on the right edges.
func (f *FSM) TrackResourceSetupRequest() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pendingSessions++
	return f.pendingSessions
}

// UntrackResourceSetupResponse decrements the counter; returns the
// new count (caller fires the Response event that may move the FSM
// out of RESOURCE_SETUP_PENDING when count reaches zero).
func (f *FSM) UntrackResourceSetupResponse() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pendingSessions > 0 {
		f.pendingSessions--
	}
	return f.pendingSessions
}

// Fire processes one event. If no transition matches the current state
// it logs a WARN and returns an error — that's the "procedure collision"
// signal operators care about (e.g. PDUSessionResourceSetupResponse
// arrived while we're in CTX_RELEASE_PENDING, or ICS Response turned
// up for a UE that never sent an ICS Request).
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
				imsiLogger(f.log, f.key.AMFUENGAPID).Warnf(
					"NGAP FSM action %s(%s → %s) for %s failed: %v",
					c.Event, cur, t.To, f.key, err)
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
		// Self-loop transitions are noisy; log at Debug when the state
		// doesn't actually change (e.g. UL NAS / parallel PDU Setup).
		//
		// Resolve IMSI via the UE registry (this FSM is keyed by (gNB,
		// AMFUENGAPID) rather than holding the UE ctx directly, so we
		// look it up at log time — O(1) byAmfID map hit). The GMM FSM
		// carries the ctx and uses WithIMSI directly; this is the
		// NGAP-side parity fix so log lines like
		// "NGAP 192.168.1.92/amfUeID=1: NOT_ESTABLISHED → ICS_PENDING"
		// land with [IMSI:...] once the UE has been identified.
		log := imsiLogger(f.log, f.key.AMFUENGAPID)
		if t.From == t.To {
			log.Debugf("NGAP %s @ %s on %s", f.key, cur, c.Event)
		} else {
			log.Infof("NGAP %s: %s → %s on %s", f.key, cur, t.To, c.Event)
		}
		return nil
	}
	// Procedure collision or unexpected event — log prominently so
	// operators can see what the gNB did that the spec didn't
	// anticipate in this state.
	imsiLogger(f.log, f.key.AMFUENGAPID).Warnf(
		"NGAP procedure collision %s: rejected %s in state %s",
		f.key, c.Event, cur)
	return fmt.Errorf("no NGAP transition for %s in state %s (%s)", c.Event, cur, f.key)
}

// FireTimer feeds a timer-expiry event back into the FSM.
func (f *FSM) FireTimer(ev Event) {
	_ = f.Fire(&Context{Key: f.key, Event: ev})
}

func (f *FSM) timerKey() string {
	return fmt.Sprintf("%s#%d", f.key.GnbKey, f.key.AMFUENGAPID)
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

// ─── Per-association registry ─────────────────────────────────────────

var (
	fsmRegMu sync.RWMutex
	fsmReg   = map[Key]*FSM{}
)

// Of returns the FSM for this UE association, creating a fresh
// NotEstablished one on first access.
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

// Drop removes the FSM entry (call once the association is terminally
// Released and you're sure nothing will reference it again).
func Drop(k Key) {
	fsmRegMu.Lock()
	delete(fsmReg, k)
	fsmRegMu.Unlock()
}

// init registers an auto-drop hook on uectx.Default so every UE
// context removal also wipes the matching NGAP per-UE FSM entry.
// Without this, fsmReg accumulated entries across the UE lifecycle
// because the only Drop call sites were the SCTP cascade and the
// UEContextRelease Complete handler — other teardown paths (auth
// reject, SMC reject, registration reject, FSM-driven abort) left
// the NGAP FSM pointer live.
func init() {
	uectx.Default.RegisterRemoveHook(func(ue *uectx.AmfUeCtx) {
		Drop(Key{GnbKey: ue.GnbKey, AMFUENGAPID: ue.AmfUeNGAPID})
	})
}

// imsiLogger returns a logger derived from base that has the
// [IMSI:...] prefix set to the UE's IMSI, looking it up in the
// uectx registry by AMF-UE-NGAP-ID. Returns the base logger unchanged
// when the UE ctx is absent (pre-Identity-procedure, post-Remove) OR
// when the ctx exists but IMSI hasn't been resolved yet (e.g. the
// NOT_ESTABLISHED → ICS_PENDING log fires after auth + SMC so the
// IMSI is already populated; the DEREGISTERED → AUTHENTICATION FSM
// transition on a fresh SUCI, by contrast, fires before SUPI
// de-conceal and will get an empty prefix, which WithIMSI("") omits).
func imsiLogger(base *logger.Logger, amfUeID int64) *logger.Logger {
	if ue := uectx.Default.LookupByAmfID(amfUeID); ue != nil {
		return base.WithIMSI(ue.IMSI)
	}
	return base
}

// Snapshot is a point-in-time view of a per-UE NGAP association FSM.
type Snapshot struct {
	GnbKey       string `json:"gnb_key"`
	AMFUENGAPID  int64  `json:"amf_ue_ngap_id"`
	State        string `json:"state"`
	PendingPDU   int    `json:"pending_pdu_setup"`
}

// AllSnapshots returns state of every live NGAP per-UE FSM.
func AllSnapshots() []Snapshot {
	fsmRegMu.RLock()
	defer fsmRegMu.RUnlock()
	out := make([]Snapshot, 0, len(fsmReg))
	for k, f := range fsmReg {
		out = append(out, Snapshot{
			GnbKey: k.GnbKey, AMFUENGAPID: k.AMFUENGAPID,
			State: f.State().String(), PendingPDU: f.PendingSessions(),
		})
	}
	return out
}

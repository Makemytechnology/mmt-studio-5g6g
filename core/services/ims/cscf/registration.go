// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// S-CSCF per-IMPI registration state machine (TS 24.229 §5.4.1).
// PDF: specs/3gpp/ts_124229v190600p.pdf.
//
// One Registration instance per IMPI, each owning its own libs/fsm
// event-loop goroutine. The CSCF container holds a map[IMPI]*Registration
// and routes incoming REGISTER requests to the right FSM.
//
// States modelled (§5.4.1):
//
//   StateNotRegistered   — no active registration for this IMPI.
//                          Reachable: initial; after 401 timeout;
//                          after successful deregistration; after
//                          reg-expires timer.
//   StateChallenged      — S-CSCF has sent 401 with an AKA challenge
//                          (AV cached), waiting for the protected
//                          REGISTER response. Covers §5.4.1.2.1 →
//                          §5.4.1.2.1A flow.
//   StateRegistered      — authentication succeeded; registration
//                          active with reg-expires timer armed.
//                          §5.4.1.2.2F "Successful registration".
//
// Transitions (spec citations on each):
//
//   NotRegistered + unprotected REGISTER → Challenged (§5.4.1.2.1)
//   Challenged    + protected REGISTER   → Registered (§5.4.1.2.2)
//   Challenged    + challenge timeout    → NotRegistered
//   Registered    + unprotected REGISTER → Challenged (rereg)
//   Registered    + REGISTER Expires=0   → NotRegistered (§5.4.1.4)
//   Registered    + reg-expires timer    → NotRegistered
//   any           + network-initiated    → NotRegistered (§5.4.1.5)
//
// Authentication mechanisms other than IMS-AKA (SIP digest,
// NASS-IMS-bundled, GPRS-IMS-bundled) are accepted by the FSM the
// same way — the dispatch at §5.4.1.1 that picks the mechanism
// belongs one level up (in the CSCF request router), not here.
// The actual AKA RES/XRES comparison is not performed in this FSM
// — callers pass `authOK bool` based on having validated elsewhere
// (TS 33.203 §6.1 "Authentication and key agreement" —
// specs/3gpp/ts_133203v190100p.pdf). §5.4.1.2.2 auth-vector
// generation is likewise caller-owned.
package cscf

import (
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/libs/fsm"
)

// ── States ──

type regState int

const (
	StateNotRegistered regState = iota
	StateChallenged
	StateRegistered
)

func (s regState) String() string {
	switch s {
	case StateNotRegistered:
		return "not-registered"
	case StateChallenged:
		return "challenged"
	case StateRegistered:
		return "registered"
	}
	return "unknown"
}

// ── Events ──

const (
	evUnprotectedRegister = iota // Data: impu, contact, expires, av
	evProtectedRegister          // Data: impu, contact, expires, authOK
	evDeregister                 // REGISTER Expires: 0
	evChallengeTimeout           // fired by challenge timer
	evRegExpireTimeout           // fired by reg-expires timer
	evNetworkDeregister          // network-initiated dereg
	evQuerySnapshot              // read-only state snapshot
	evQueryAV                    // read-only cached-AV snapshot (RFC 3310 §3.3 verification)
)

// Timer IDs inside the FSM.
const (
	timerChallenge  = "challenge"
	timerRegExpires = "reg-expires"
)

// Timer durations. Real values are operator policy; these are
// reasonable defaults used when the caller doesn't supply one.
const (
	defaultChallengeTimeout = 30 * time.Second
	defaultRegExpires       = 600 * time.Second
)

// ── Result type ──

// RegResult is what the S-CSCF wants to send back on the wire for a
// REGISTER request. The FSM computes code + reason + optional AKA
// challenge vector; the caller serialises it into a SIP response.
type RegResult struct {
	Code      int                 // 200 / 401 / 403 / 423 / 500
	Reason    string              // "OK" / "Unauthorized" / …
	Challenge map[string][]byte   // non-nil for 401 — AV fields to carry in WWW-Authenticate
}

// ── Registration instance ──

// Registration owns one IMPI's state machine. Public fields are
// updated only from the loop goroutine and are safe to read via
// Snapshot() (which goes through the loop) or directly with the
// caveat that direct reads may see a transient value; use Snapshot
// for consistency.
type Registration struct {
	IMPI string

	// Observable per-registration data, updated by the loop.
	IMPU    string
	Contact string
	Expires int // seconds

	// av holds the authentication vector while in StateChallenged.
	// Zeroed on successful registration, abort, or deregister.
	av map[string][]byte

	fsm      *fsm.Machine
	stopOnce sync.Once
}

// NewRegistration creates and starts a per-IMPI registration FSM.
func NewRegistration(impi string) *Registration {
	r := &Registration{IMPI: impi}
	r.fsm = fsm.New("reg:"+impi, StateNotRegistered, r.handle, 32)
	r.fsm.Start()
	return r
}

// Stop releases the FSM goroutine. Idempotent.
func (r *Registration) Stop() {
	r.stopOnce.Do(func() { r.fsm.Stop() })
}

// State returns the current state.
func (r *Registration) State() regState {
	s, _ := r.fsm.State().(regState)
	return s
}

// ── Public event wrappers ──

// OnUnprotectedRegister handles a REGISTER without an
// integrity-protected=yes parameter (§5.4.1.2.1). If av is nil, the
// caller didn't supply a challenge and the FSM returns 500.
func (r *Registration) OnUnprotectedRegister(impu, contact string, expires int, av map[string][]byte) RegResult {
	return r.dispatch(evUnprotectedRegister, map[string]any{
		"impu": impu, "contact": contact, "expires": expires, "av": av,
	})
}

// OnProtectedRegister handles a REGISTER with integrity-protected=yes
// (§5.4.1.2.2). authOK is true iff the caller has already validated
// the RES against the cached AV.
func (r *Registration) OnProtectedRegister(impu, contact string, expires int, authOK bool) RegResult {
	return r.dispatch(evProtectedRegister, map[string]any{
		"impu": impu, "contact": contact, "expires": expires, "authOK": authOK,
	})
}

// OnDeregister handles a REGISTER with Expires: 0 (§5.4.1.4).
func (r *Registration) OnDeregister() RegResult {
	return r.dispatch(evDeregister, nil)
}

// NetworkDeregister triggers server-initiated deregistration (§5.4.1.5).
func (r *Registration) NetworkDeregister() {
	reply := make(chan any, 1)
	r.fsm.Send(fsm.Event{Type: evNetworkDeregister, Reply: reply})
	<-reply
}

// Snapshot returns a consistent view of the IMPI's registration
// state (state name + IMPU + contact + expires + whether a challenge
// is outstanding). Goes through the FSM loop so observed fields are
// always consistent with state.
func (r *Registration) Snapshot() map[string]any {
	reply := make(chan any, 1)
	r.fsm.Send(fsm.Event{Type: evQuerySnapshot, Reply: reply})
	res, _ := (<-reply).(map[string]any)
	return res
}

// CachedAV returns the AV currently cached in StateChallenged, or nil
// when the FSM is not in StateChallenged. Used by VerifyAuth to
// reach the XRES + nonce material needed for RFC 3310 §3.3 digest
// verification. Goes through the FSM loop so it never races with a
// concurrent re-challenge.
func (r *Registration) CachedAV() map[string][]byte {
	reply := make(chan any, 1)
	r.fsm.Send(fsm.Event{Type: evQueryAV, Reply: reply})
	if got, ok := (<-reply).(map[string][]byte); ok {
		return got
	}
	return nil
}

func (r *Registration) dispatch(t int, data map[string]any) RegResult {
	reply := make(chan any, 1)
	r.fsm.Send(fsm.Event{Type: t, Data: data, Reply: reply})
	res, _ := (<-reply).(RegResult)
	return res
}

// ── Handler (loop goroutine only) ──

func (r *Registration) handle(st fsm.State, ev fsm.Event) fsm.Action {
	rs, _ := st.(regState)

	switch ev.Type {
	case evUnprotectedRegister:
		return r.onUnprotected(rs, ev)
	case evProtectedRegister:
		return r.onProtected(rs, ev)
	case evDeregister:
		return r.onDeregister(rs)
	case evChallengeTimeout:
		return r.onChallengeTimeout(rs)
	case evRegExpireTimeout:
		return r.onRegExpireTimeout(rs)
	case evNetworkDeregister:
		return r.onNetworkDeregister(rs)
	case evQuerySnapshot:
		return fsm.Action{Reply: r.snapshot(rs)}
	case evQueryAV:
		// Defensive copy — callers must not be able to mutate the
		// FSM's cached AV.
		if r.av == nil {
			return fsm.Action{Reply: map[string][]byte(nil)}
		}
		out := make(map[string][]byte, len(r.av))
		for k, v := range r.av {
			cp := make([]byte, len(v))
			copy(cp, v)
			out[k] = cp
		}
		return fsm.Action{Reply: out}
	}
	return fsm.Action{}
}

// §5.4.1.2.1 "Unprotected REGISTER" — issue 401 challenge with AV.
// Accepted from NotRegistered (initial) and Registered (reregistration).
func (r *Registration) onUnprotected(rs regState, ev fsm.Event) fsm.Action {
	av, _ := ev.Data["av"].(map[string][]byte)
	if av == nil || len(av) == 0 {
		return fsm.Action{Reply: RegResult{Code: 500, Reason: "no_av_supplied"}}
	}
	if rs != StateNotRegistered && rs != StateRegistered {
		return fsm.Action{Reply: RegResult{Code: 500, Reason: "bad_state"}}
	}
	r.av = av
	return fsm.Action{
		Next: StateChallenged,
		Timers: []fsm.TimerOp{{
			ID:     timerChallenge,
			After:  defaultChallengeTimeout,
			OnFire: fsm.Event{Type: evChallengeTimeout},
		}},
		Reply: RegResult{Code: 401, Reason: "Unauthorized", Challenge: av},
	}
}

// §5.4.1.2.2 / §5.4.1.2.2F "Successful registration" — on protected
// REGISTER with valid auth response, emit 200 OK and arm reg-expires.
func (r *Registration) onProtected(rs regState, ev fsm.Event) fsm.Action {
	if rs != StateChallenged {
		return fsm.Action{Reply: RegResult{Code: 403, Reason: "not_challenged"}}
	}
	authOK, _ := ev.Data["authOK"].(bool)
	if !authOK {
		// §5.4.1.2.3A "Abnormal cases – IMS AKA": auth fails, drop AV,
		// return to NotRegistered, signal 403.
		r.av = nil
		return fsm.Action{
			Next: StateNotRegistered,
			Timers: []fsm.TimerOp{
				{ID: timerChallenge, Cancel: true},
			},
			Reply: RegResult{Code: 403, Reason: "auth_failed"},
		}
	}
	impu, _ := ev.Data["impu"].(string)
	contact, _ := ev.Data["contact"].(string)
	expires, _ := ev.Data["expires"].(int)
	if expires <= 0 {
		expires = int(defaultRegExpires / time.Second)
	}
	r.IMPU = impu
	r.Contact = contact
	r.Expires = expires
	r.av = nil
	return fsm.Action{
		Next: StateRegistered,
		Timers: []fsm.TimerOp{
			{ID: timerChallenge, Cancel: true},
			{ID: timerRegExpires, After: time.Duration(expires) * time.Second,
				OnFire: fsm.Event{Type: evRegExpireTimeout}},
		},
		Reply: RegResult{Code: 200, Reason: "OK"},
	}
}

// §5.4.1.4 "User-initiated deregistration" — REGISTER with Expires 0.
func (r *Registration) onDeregister(rs regState) fsm.Action {
	r.IMPU = ""
	r.Contact = ""
	r.Expires = 0
	r.av = nil
	return fsm.Action{
		Next: StateNotRegistered,
		Timers: []fsm.TimerOp{
			{ID: timerChallenge, Cancel: true},
			{ID: timerRegExpires, Cancel: true},
		},
		Reply: RegResult{Code: 200, Reason: "OK"},
	}
}

// Challenge timer expiry: UE never returned the protected REGISTER.
func (r *Registration) onChallengeTimeout(rs regState) fsm.Action {
	if rs != StateChallenged {
		return fsm.Action{}
	}
	r.av = nil
	return fsm.Action{Next: StateNotRegistered}
}

// Registration-expires timer: UE didn't reregister in time.
func (r *Registration) onRegExpireTimeout(rs regState) fsm.Action {
	if rs != StateRegistered {
		return fsm.Action{}
	}
	r.IMPU = ""
	r.Contact = ""
	r.Expires = 0
	return fsm.Action{Next: StateNotRegistered}
}

// §5.4.1.5 "Network-initiated deregistration": wipe state and cancel
// timers regardless of current state.
func (r *Registration) onNetworkDeregister(rs regState) fsm.Action {
	r.IMPU = ""
	r.Contact = ""
	r.Expires = 0
	r.av = nil
	return fsm.Action{
		Next: StateNotRegistered,
		Timers: []fsm.TimerOp{
			{ID: timerChallenge, Cancel: true},
			{ID: timerRegExpires, Cancel: true},
		},
	}
}

func (r *Registration) snapshot(rs regState) map[string]any {
	return map[string]any{
		"impi":       r.IMPI,
		"state":      rs.String(),
		"impu":       r.IMPU,
		"contact":    r.Contact,
		"expires":    r.Expires,
		"challenged": rs == StateChallenged,
	}
}

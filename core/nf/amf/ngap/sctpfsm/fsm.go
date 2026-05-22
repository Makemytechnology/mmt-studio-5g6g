// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package sctpfsm — per-SCTP-association state machine.
//
// Models the transport layer underneath NGAP per RFC 4960 §4 "Connection
// Management" + TS 38.412 §7 "SCTP usage". One instance per gNB
// association; state transitions fire on SCTP notifications lifted out
// of the recvmsg stream (MSG_NOTIFICATION flag) and parsed against the
// struct sctp_notification layout in <linux/sctp.h>.
//
// Why this exists as a separate FSM from the NGAP per-UE one:
//
//   * Transport state is orthogonal to the N-many UE contexts riding
//     on a single association. When SCTP goes away, every UE goes
//     too (cascade NGReset). When one UE context is released, SCTP
//     stays up.
//
//   * The transitions cited here come straight from RFC 4960, not
//     TS 38.413 — putting them in nf/amf/ngap/fsm would mix spec
//     sources and confuse future readers.
//
//   * Timers / counters (HB interval, path_max_rxt, asocmaxrxt) live
//     at the SCTP layer and are operator-tunable via SCTP_PEER_ADDR_
//     PARAMS + SCTP_ASSOCINFO — separate FSM keeps them owned here.
package sctpfsm

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// State follows RFC 4960 §4 + §9.
type State int

const (
	StateClosed            State = iota
	StateCookieWait              // INIT sent, awaiting INIT-ACK
	StateCookieEchoed            // INIT-ACK received, COOKIE-ECHO sent
	StateEstablished             // COMM_UP delivered by kernel
	StateShutdownPending         // Local user called close()
	StateShutdownSent            // SHUTDOWN chunk sent
	StateShutdownReceived        // SHUTDOWN chunk received from peer
	StateShutdownAckSent         // SHUTDOWN-ACK sent, awaiting COMPLETE
	StateFailed                  // ABORT received OR asocmaxrxt reached
)

// String renders the RFC 4960 name for log lines.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateCookieWait:
		return "COOKIE_WAIT"
	case StateCookieEchoed:
		return "COOKIE_ECHOED"
	case StateEstablished:
		return "ESTABLISHED"
	case StateShutdownPending:
		return "SHUTDOWN_PENDING"
	case StateShutdownSent:
		return "SHUTDOWN_SENT"
	case StateShutdownReceived:
		return "SHUTDOWN_RECEIVED"
	case StateShutdownAckSent:
		return "SHUTDOWN_ACK_SENT"
	case StateFailed:
		return "FAILED"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Event — every notification type emitted by the Linux SCTP stack that
// can drive a state transition, plus the few timer expiries we keep
// track of at the transport layer.
type Event int

const (
	// RFC 4960 §4 (state diagram) / RFC 6458 §6.1.1 — SCTP_ASSOC_CHANGE sub-events.
	EvCommUp       Event = iota // sac_state = SCTP_COMM_UP
	EvCommLost                  // sac_state = SCTP_COMM_LOST (peer-triggered or local RTO exhaustion)
	EvRestart                   // sac_state = SCTP_RESTART
	EvShutdownRx                // sac_state = SCTP_SHUTDOWN_COMP (peer did graceful close)
	EvAbort                     // sac_state = SCTP_CANT_STR_ASSOC — couldn't establish

	// RFC 4960 §9 — graceful close initiated by local user.
	EvShutdownTx // we called close() on an ESTABLISHED association

	// RFC 6458 §6.1.3 — SCTP_SHUTDOWN_EVENT (peer sent SHUTDOWN chunk).
	EvShutdownChunkRx

	// RFC 6458 §6.1.5 — SCTP_SEND_FAILED_EVENT.
	EvSendFailed

	// RFC 6458 §6.1.4 — SCTP_REMOTE_ERROR.
	EvRemoteError

	// Timer expiries (TS 38.412 §7 leaves tuning to the implementation).
	EvTInitExpired // INIT retransmit limit reached
	EvTAssocRxExhausted
)

// String renders the event name.
func (e Event) String() string {
	switch e {
	case EvCommUp:
		return "COMM_UP"
	case EvCommLost:
		return "COMM_LOST"
	case EvRestart:
		return "SCTP_RESTART"
	case EvShutdownRx:
		return "SHUTDOWN_COMP"
	case EvAbort:
		return "ABORT"
	case EvShutdownTx:
		return "local_close"
	case EvShutdownChunkRx:
		return "SHUTDOWN_EVENT"
	case EvSendFailed:
		return "SEND_FAILED"
	case EvRemoteError:
		return "REMOTE_ERROR"
	case EvTInitExpired:
		return "T_INIT_expired"
	case EvTAssocRxExhausted:
		return "asocmaxrxt_exhausted"
	}
	return fmt.Sprintf("Event(%d)", int(e))
}

// Key identifies one SCTP association on the AMF side.
// GnbIP is the peer address the kernel handed us at accept time;
// AssocID is the kernel-assigned sctp_assoc_t.
type Key struct {
	GnbIP   string
	AssocID int32
}

func (k Key) String() string { return fmt.Sprintf("%s#%d", k.GnbIP, k.AssocID) }

// Context is Action input. Reason / Cause carry kernel-reported details
// from the sctp_assoc_change / sctp_send_failed notifications so
// observability / alarm text has the actual cause, not a generic string.
type Context struct {
	Key    Key
	Event  Event
	Cause  uint16 // sac_error / ssf_error (host byte order after parse)
	Reason string
}

// Action runs on a transition; non-nil error aborts the advance.
type Action func(c *Context) error

type TimerSpec struct {
	Name     string
	Duration time.Duration
	OnExpiry Event

	// Log-only metadata forwarded to the timer manager so retransmit /
	// expiry log lines name the SCTP procedure being awaited. Empty
	// disables.
	Description string
	Awaiting    string
}

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

// SetDefaultTable installs the graph.
func SetDefaultTable(t []Transition) {
	defaultTableMu.Lock()
	defaultTable = t
	defaultTableMu.Unlock()
}

// New returns a CLOSED FSM.
func New(k Key) *FSM {
	defaultTableMu.RLock()
	t := defaultTable
	defaultTableMu.RUnlock()
	return &FSM{
		state:    StateClosed,
		key:      k,
		table:    t,
		timerMgr: timers.M,
		log:      logger.Get("amf.ngap.sctp.fsm"),
	}
}

// State returns current state.
func (f *FSM) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Fire drives the FSM with an event. Unknown transitions log WARN with
// "SCTP procedure collision" so operators recognise them the same way
// as NGAP collisions.
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
				f.log.Warnf("SCTP FSM action %s(%s → %s) for %s failed: %v",
					c.Event, cur, t.To, f.key, err)
				return err
			}
		}
		for _, name := range t.StopTimers {
			f.timerMgr.Cancel(name, f.timerKey())
		}
		f.state = t.To
		for _, spec := range t.StartTimers {
			if spec.Name == "" || spec.Duration == 0 {
				continue
			}
			ev := spec.OnExpiry
			f.timerMgr.Start(spec.Name, f.timerKey(), spec.Duration,
				func() { _ = f.Fire(&Context{Key: f.key, Event: ev}) },
				timers.Options{Description: spec.Description, Awaiting: spec.Awaiting})
		}
		if t.From == t.To {
			f.log.Debugf("SCTP %s @ %s on %s", f.key, cur, c.Event)
		} else {
			f.log.Infof("SCTP %s: %s → %s on %s", f.key, cur, t.To, c.Event)
		}
		return nil
	}
	f.log.Warnf("SCTP procedure collision %s: rejected %s in state %s",
		f.key, c.Event, cur)
	return fmt.Errorf("no SCTP transition for %s in state %s (%s)", c.Event, cur, f.key)
}

func (f *FSM) timerKey() string {
	return fmt.Sprintf("sctp#%s#%d", f.key.GnbIP, f.key.AssocID)
}

// ─── Registry ────────────────────────────────────────────────────────

var (
	regMu sync.RWMutex
	reg   = map[Key]*FSM{}
)

// Of returns the FSM for this association; creates on first touch.
func Of(k Key) *FSM {
	regMu.RLock()
	f, ok := reg[k]
	regMu.RUnlock()
	if ok {
		return f
	}
	regMu.Lock()
	defer regMu.Unlock()
	if f, ok = reg[k]; ok {
		return f
	}
	f = New(k)
	reg[k] = f
	return f
}

// Drop removes the FSM entry (after CLOSED / FAILED is terminal).
func Drop(k Key) {
	regMu.Lock()
	delete(reg, k)
	regMu.Unlock()
}

// Snapshot is a point-in-time view of one association for
// /api/amf/ngap/sctp and Prometheus gauges.
type Snapshot struct {
	GnbIP   string `json:"gnb_ip"`
	AssocID int32  `json:"assoc_id"`
	State   string `json:"state"`
}

// AllSnapshots returns live snapshot of every SCTP FSM.
func AllSnapshots() []Snapshot {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Snapshot, 0, len(reg))
	for k, f := range reg {
		out = append(out, Snapshot{
			GnbIP: k.GnbIP, AssocID: k.AssocID, State: f.State().String(),
		})
	}
	return out
}

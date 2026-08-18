// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package fsm — generic event-loop state machine.
//
// Mission-critical 3GPP specs (TS 24.229 for IMS, TS 24.379 for MCPTT
// call control, TS 24.380 for MCPTT floor control) model protocol
// behaviour as state machines driven by received messages, user
// actions, and timer expiries. Implementing that with ad-hoc methods
// + mutexes leaks concurrency bugs and makes timer events hard to
// reason about. This package provides the single-goroutine event
// loop that every spec FSM in the repo should use.
//
// Each Machine has:
//
//   - one goroutine that is the only mutator of state (no locks
//     needed inside the Handler);
//   - one event channel that serializes inputs from any number of
//     external goroutines;
//   - a timer table owned by the loop — an expired timer fires an
//     Event onto the same channel, so timers compose with user
//     events uniformly;
//   - a Handler function: (state, event) -> Action.
//
// Callers either fire-and-forget (Send) or synchronously await a
// reply by including a Reply channel in the Event.
package fsm

import (
	"fmt"
	"sync"
	"time"
)

// State is whatever concrete type the caller defines, usually an int
// enum with a String() method for logging and tests.
type State interface {
	fmt.Stringer
}

// Event flows into the machine. Type is caller-defined (typically an
// int enum). Data carries event payload. Reply, if non-nil, gives
// the sender a one-shot channel the Machine will write Action.Reply
// into after dispatch.
type Event struct {
	Type  int
	Data  map[string]any
	Reply chan<- any
}

// TimerOp arms or cancels a timer. OnFire is the event that will be
// enqueued when the timer expires (the expiry routes through the
// same event channel as everything else).
type TimerOp struct {
	ID     string
	Cancel bool          // if true, After/OnFire ignored; timer is cancelled
	After  time.Duration // duration from now
	OnFire Event         // event to dispatch on expiry
}

// Action is the return value of a Handler. The Machine applies these
// effects in order after the Handler returns:
//
//  1. Next — if non-nil, the new state.
//  2. Timers — each arm/cancel applied.
//  3. Emit — each event enqueued for later dispatch.
//  4. Reply — written to the original Event.Reply, if any.
type Action struct {
	Next   State
	Timers []TimerOp
	Emit   []Event
	Reply  any
}

// Handler is the transition function. It runs on the Machine's loop
// goroutine — so any side effects it performs (logging, DB writes,
// callbacks) are serialized with every other transition. Prefer the
// Action return value over direct side effects for new behaviour;
// keep Handlers side-effect-light and avoid blocking calls (network
// I/O, long-held locks) because they stall the whole queue.
type Handler func(state State, ev Event) Action

// Machine is the event loop.
type Machine struct {
	name    string
	handler Handler
	events  chan Event
	done    chan struct{}
	stopped chan struct{}

	mu     sync.RWMutex
	state  State
	timers map[string]*time.Timer
}

// New creates an unstarted Machine. Call Start() to spin up the loop.
// bufSize is the event-channel buffer; 64 is a reasonable default.
func New(name string, initial State, h Handler, bufSize int) *Machine {
	if bufSize < 1 {
		bufSize = 64
	}
	return &Machine{
		name:    name,
		handler: h,
		state:   initial,
		events:  make(chan Event, bufSize),
		done:    make(chan struct{}),
		stopped: make(chan struct{}),
		timers:  make(map[string]*time.Timer),
	}
}

// Start spins up the event loop goroutine. Safe to call exactly once.
func (m *Machine) Start() {
	go m.run()
}

// Send enqueues an event from any goroutine. Blocks only if the
// event buffer is full. Returns immediately without enqueuing if
// the Machine has been stopped.
func (m *Machine) Send(ev Event) {
	select {
	case m.events <- ev:
	case <-m.done:
	}
}

// Stop signals the loop to exit, cancels all timers, and waits for
// the goroutine to finish. Idempotent.
func (m *Machine) Stop() {
	select {
	case <-m.done:
		// already stopped
	default:
		close(m.done)
	}
	<-m.stopped
}

// State returns a snapshot of the current state. Safe from any
// goroutine.
func (m *Machine) State() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

// Name returns the machine's label (used in logs / tests).
func (m *Machine) Name() string { return m.name }

func (m *Machine) run() {
	defer close(m.stopped)
	for {
		select {
		case <-m.done:
			m.cancelAllTimers()
			return
		case ev := <-m.events:
			m.dispatch(ev)
		}
	}
}

func (m *Machine) dispatch(ev Event) {
	m.mu.RLock()
	st := m.state
	m.mu.RUnlock()

	act := m.handler(st, ev)

	if act.Next != nil {
		m.mu.Lock()
		m.state = act.Next
		m.mu.Unlock()
	}
	for _, t := range act.Timers {
		m.applyTimer(t)
	}
	for _, emit := range act.Emit {
		select {
		case m.events <- emit:
		case <-m.done:
			return
		}
	}
	if ev.Reply != nil {
		// Always reply when the caller asked for one, even if the
		// Handler left Reply as nil — otherwise the caller hangs.
		select {
		case ev.Reply <- act.Reply:
		default:
			// Buffer should be 1; if full the caller set it up wrong.
		}
	}
}

func (m *Machine) applyTimer(t TimerOp) {
	if existing, ok := m.timers[t.ID]; ok {
		existing.Stop()
		delete(m.timers, t.ID)
	}
	if t.Cancel {
		return
	}
	ev := t.OnFire
	m.timers[t.ID] = time.AfterFunc(t.After, func() {
		m.Send(ev)
	})
}

func (m *Machine) cancelAllTimers() {
	for id, t := range m.timers {
		t.Stop()
		delete(m.timers, id)
	}
}

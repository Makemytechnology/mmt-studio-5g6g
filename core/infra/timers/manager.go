// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 3GPP timer manager — Go port of infra/timers/timer_manager.py.
//
// One background goroutine ticks every 500 ms and fires expired timers.
// Timers are keyed by (name, ueID). Starting a timer with the same key
// cancels the previous one (implicit restart).
//
// Usage:
//
//	timers.M.Start("T3516", ueID, timers.T3516, onAuthTimeout)
//	timers.M.Cancel("T3516", ueID)               // auth response arrived
package timers

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Callback is invoked on expiry (and on each retransmit slot when configured).
type Callback func()

// Options configures an optional retransmission stream before final expiry.
type Options struct {
	// Retransmit is called up to MaxRetransmit times at MaxInterval cadence
	// before the final Start-duration timer fires.
	Retransmit    Callback
	MaxRetransmit int
	MaxInterval   time.Duration

	// Description is a short procedure-level label that explains what the
	// timer is guarding ("Registration Accept retransmit guard" etc.). It
	// is purely cosmetic — appended to the retransmit / expiry log lines
	// so operators don't have to cross-reference timer numbers against
	// TS 24.501 §10.2 from memory. Empty disables.
	Description string

	// Awaiting names the message the timer is waiting for ("Registration
	// Complete from UE" etc.). Same purpose as Description; kept separate
	// so the log line reads naturally.
	Awaiting string
}

type entry struct {
	name, ueID      string
	expires         time.Time
	cb              Callback
	retransmitCB    Callback
	retransmitCount int
	maxRetransmit   int
	retransmitEvery time.Duration
	nextRetransmit  time.Time
	cancelled       bool

	// Log-only metadata copied from Options at Start time.
	description string
	awaiting    string
}

// Manager is the public handle. A package-global M is provided below.
type Manager struct {
	mu       sync.Mutex
	timers   map[string]*entry
	imsis    map[string]string // ueID → IMSI for log enrichment (RegisterUE)
	running  bool
	stopCh   chan struct{}
	doneCh   chan struct{}
	log      *logger.Logger
	tickRate time.Duration
}

// NewManager returns a Manager with a 500ms tick. Callers typically use
// the package singleton M rather than constructing their own.
func NewManager() *Manager {
	return &Manager{
		timers:   make(map[string]*entry),
		imsis:    make(map[string]string),
		log:      logger.Get("infra.timers"),
		tickRate: 500 * time.Millisecond,
	}
}

// M is the process-wide singleton.
var M = NewManager()

// Start registers a timer. If a timer with the same (name, ueID) is
// already running it is cancelled — this is the 3GPP "implicit restart"
// semantic used by Registration / Authentication / SM procedures.
func (m *Manager) Start(name, ueID string, duration time.Duration, cb Callback, opts ...Options) {
	e := &entry{
		name:    name,
		ueID:    ueID,
		expires: time.Now().Add(duration),
		cb:      cb,
	}
	if len(opts) > 0 {
		o := opts[0]
		e.retransmitCB = o.Retransmit
		e.maxRetransmit = o.MaxRetransmit
		e.retransmitEvery = o.MaxInterval
		if o.MaxInterval > 0 {
			e.nextRetransmit = time.Now().Add(o.MaxInterval)
		}
		e.description = o.Description
		e.awaiting = o.Awaiting
	}
	key := timerKey(name, ueID)
	m.mu.Lock()
	if old := m.timers[key]; old != nil {
		old.cancelled = true
	}
	m.timers[key] = e
	m.mu.Unlock()
	m.log.Debugf("Timer %s started: ue=%s duration=%s retx=%d", name, ueID, duration, e.maxRetransmit)
}

// Cancel returns true if a timer existed and was cancelled.
func (m *Manager) Cancel(name, ueID string) bool {
	key := timerKey(name, ueID)
	m.mu.Lock()
	e, ok := m.timers[key]
	if ok {
		e.cancelled = true
		delete(m.timers, key)
	}
	m.mu.Unlock()
	if ok {
		m.log.Debugf("Timer %s cancelled: ue=%s", name, ueID)
	}
	return ok
}

// CancelAllForUE removes every timer for a UE (e.g., context release).
// Also drops the UE→IMSI registry entry so future timer logs for a
// recycled ueID don't pick up a stale IMSI.
func (m *Manager) CancelAllForUE(ueID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, e := range m.timers {
		if e.ueID == ueID {
			e.cancelled = true
			delete(m.timers, k)
			n++
		}
	}
	delete(m.imsis, ueID)
	if n > 0 {
		m.log.Debugf("Cancelled %d timers for ue=%s", n, ueID)
	}
	return n
}

// RegisterUE associates an IMSI with a ueID for log enrichment. Each
// timer-fired / timer-expired log line for this ueID will then carry
// the IMSI tag via logger.WithIMSI, matching the format other modules
// emit and keeping SACORE_LOG_IMSI filter behaviour consistent.
//
// Idempotent — calling with the same (ueID, imsi) twice is a no-op.
// Calling with a new imsi for the same ueID overwrites (the registry
// keeps the most recent association).
func (m *Manager) RegisterUE(ueID, imsi string) {
	if ueID == "" || imsi == "" {
		return
	}
	m.mu.Lock()
	m.imsis[ueID] = imsi
	m.mu.Unlock()
}

// UnregisterUE drops the IMSI association. Called from UE-context
// removal paths so a recycled ueID doesn't inherit a stale IMSI.
func (m *Manager) UnregisterUE(ueID string) {
	if ueID == "" {
		return
	}
	m.mu.Lock()
	delete(m.imsis, ueID)
	m.mu.Unlock()
}

// imsiFor returns the registered IMSI for a ueID, or "" if none is
// associated. Lock taken internally; callers must NOT hold m.mu.
func (m *Manager) imsiFor(ueID string) string {
	m.mu.Lock()
	imsi := m.imsis[ueID]
	m.mu.Unlock()
	return imsi
}

// IsRunning reports whether a timer with the given key is active.
func (m *Manager) IsRunning(name, ueID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.timers[timerKey(name, ueID)]
	return ok && !e.cancelled
}

// Remaining returns time-to-expiry, or 0 if the timer isn't running.
func (m *Manager) Remaining(name, ueID string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.timers[timerKey(name, ueID)]
	if !ok || e.cancelled {
		return 0
	}
	r := time.Until(e.expires)
	if r < 0 {
		return 0
	}
	return r
}

// Active is a monitoring snapshot.
type Active struct {
	Name            string        `json:"name"`
	UEID            string        `json:"ue_id"`
	Remaining       time.Duration `json:"remaining"`
	RetransmitCount int           `json:"retransmit_count"`
	MaxRetransmit   int           `json:"max_retransmit"`
}

// ActiveTimers returns a snapshot of every non-cancelled timer.
func (m *Manager) ActiveTimers() []Active {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Active, 0, len(m.timers))
	now := time.Now()
	for _, e := range m.timers {
		if e.cancelled {
			continue
		}
		rem := e.expires.Sub(now)
		if rem < 0 {
			rem = 0
		}
		out = append(out, Active{
			Name: e.name, UEID: e.ueID, Remaining: rem,
			RetransmitCount: e.retransmitCount, MaxRetransmit: e.maxRetransmit,
		})
	}
	return out
}

// ── Background loop ─────────────────────────────────────────────────────

// StartManager spawns the tick goroutine. Idempotent.
func (m *Manager) StartManager() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.stopCh = make(chan struct{})
	m.doneCh = make(chan struct{})
	m.mu.Unlock()
	go m.run()
	m.log.Info("Timer manager started")
}

// StopManager halts the tick goroutine. Waits up to 2s for it to drain.
func (m *Manager) StopManager() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	close(m.stopCh)
	done := m.doneCh
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func (m *Manager) run() {
	defer close(m.doneCh)
	ticker := time.NewTicker(m.tickRate)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

func (m *Manager) tick() {
	now := time.Now()
	var expired, retx []*entry

	m.mu.Lock()
	for k, e := range m.timers {
		if e.cancelled {
			delete(m.timers, k)
			continue
		}
		if e.retransmitCB != nil && e.retransmitEvery > 0 &&
			e.retransmitCount < e.maxRetransmit && !e.nextRetransmit.After(now) {
			retx = append(retx, e)
			e.retransmitCount++
			e.nextRetransmit = now.Add(e.retransmitEvery)
			continue
		}
		if !e.expires.After(now) {
			expired = append(expired, e)
			delete(m.timers, k)
		}
	}
	m.mu.Unlock()

	for _, e := range retx {
		if e.cancelled {
			continue
		}
		m.logEvent(e, "retransmit", e.retransmitCount, e.maxRetransmit)
		safeCall(e.retransmitCB, m.log, e.name, "retransmit")
	}
	for _, e := range expired {
		if e.cancelled {
			continue
		}
		m.logEvent(e, "expired", e.retransmitCount, e.maxRetransmit)
		safeCall(e.cb, m.log, e.name, "expiry")
	}
}

// logEvent emits one tick log line for a timer event ("retransmit" or
// "expired"). When an IMSI is registered for the ueID, the line is
// emitted via logger.WithIMSI so the standard `[IMSI:…]` tag appears
// — same format every other module uses — and SACORE_LOG_IMSI
// filtering applies uniformly. Description / Awaiting (if populated
// via Options) are appended so the line is self-explanatory:
//
//	[IMSI:…] T3550 retransmit #1: ue=1 desc="…" awaiting="…"
//	[IMSI:…] T3550 expired: ue=1 retx=4/4 desc="…" awaiting="…"
func (m *Manager) logEvent(e *entry, kind string, count, maxCount int) {
	imsi := m.imsiFor(e.ueID)
	log := m.log
	if imsi != "" {
		log = log.WithIMSI(imsi)
	}
	suffix := ""
	if e.description != "" {
		suffix += fmt.Sprintf(` desc=%q`, e.description)
	}
	if e.awaiting != "" {
		suffix += fmt.Sprintf(` awaiting=%q`, e.awaiting)
	}
	switch kind {
	case "retransmit":
		log.Infof("%s retransmit #%d: ue=%s%s", e.name, count, e.ueID, suffix)
	case "expired":
		log.Infof("%s expired: ue=%s retx=%d/%d%s", e.name, e.ueID, count, maxCount, suffix)
	}
}

func safeCall(cb Callback, log *logger.Logger, name, kind string) {
	defer func() {
		if r := recover(); r != nil {
			log.Warnf("Timer %s %s callback panic: %v", name, kind, r)
		}
	}()
	if cb != nil {
		cb()
	}
}

func timerKey(name, ueID string) string { return name + "|" + ueID }

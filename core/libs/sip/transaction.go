// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SIP transaction layer — four state machines per RFC 3261 §17.
//
// Spec anchor: specs/ietf/rfc3261.txt. Section numbers and timer
// values below were verified against that copy (Appendix A "Table
// of Timer Values" lists T1=500ms, T2=4s, T4=5s, and the Timer
// A–K mapping to §17.x sections).
//
// The RFC defines four distinct transaction state machines, each
// with its own state set and timer rules:
//
//   §17.1.1  INVITE client       (Calling / Proceeding / Completed / Terminated)
//            Timers A (retransmit, T1 doubling)
//                   B (timeout, 64*T1)
//                   D (wait in Completed, ≥32s for unreliable)
//
//   §17.1.2  non-INVITE client   (Trying / Proceeding / Completed / Terminated)
//            Timers E (retransmit, T1 doubling up to T2)
//                   F (timeout, 64*T1)
//                   K (wait in Completed, T4 for unreliable, 0 for reliable)
//
//   §17.2.1  INVITE server       (Proceeding / Completed / Confirmed / Terminated)
//            Timers G (retransmit 3xx-6xx, T1 doubling up to T2)
//                   H (wait for ACK, 64*T1)
//                   I (wait in Confirmed, T4 or 0)
//
//   §17.2.2  non-INVITE server   (Trying / Proceeding / Completed / Terminated)
//            Timer  J (wait for retransmit, 64*T1 or 0 for reliable)
//
// Each machine is implemented as its own Go type with a dedicated
// libs/fsm.Machine — a single goroutine per transaction is the only
// mutator of that transaction's state. Retransmit timers fire into
// the same event loop, so the Send callback (which writes to
// transport) runs on the loop goroutine and is serialised with any
// user-driven events (e.g. an incoming response).
//
// Shared helpers (timer defaults, transaction matching per §17.1.3,
// the transaction manager) live in this file; the four state
// machines live in transaction_invite_client.go,
// transaction_noninvite_client.go, transaction_invite_server.go,
// and transaction_noninvite_server.go.
package sip

import (
	"sync"
	"time"
)

// RFC 3261 default timer values (Appendix A "Table of Timer Values
// Used in this Specification"). T1 is the estimated RTT; T2 is the
// maximum retransmit interval for non-INVITE requests; T4 is the
// maximum duration a message stays in the network. Spec text:
// specs/ietf/rfc3261.txt.
const (
	T1 = 500 * time.Millisecond
	T2 = 4 * time.Second
	T4 = 5 * time.Second
)

// Transaction is the common interface implemented by all four
// transaction FSM types. It's enough for the TransactionManager to
// track, inspect, and shut down a transaction without needing to
// know which of the four kinds it is.
type Transaction interface {
	Branch() string
	Method() string
	State() string
	Stop()
}

// TransactionManager tracks active transactions indexed by the
// §17.1.3 matching key: (topmost-Via branch, CSeq method). Clients
// and servers share the key namespace here; the Go type carries
// the client/server distinction.
type TransactionManager struct {
	mu   sync.RWMutex
	txns map[[2]string]Transaction
}

func NewTransactionManager() *TransactionManager {
	return &TransactionManager{txns: make(map[[2]string]Transaction)}
}

// Add registers a transaction. Replaces any existing entry with the
// same key (which should not normally happen — a duplicate would
// indicate a branch collision).
func (tm *TransactionManager) Add(t Transaction) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.txns[[2]string{t.Branch(), t.Method()}] = t
}

// Get returns the transaction matching the §17.1.3 key, or nil.
func (tm *TransactionManager) Get(branch, method string) Transaction {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.txns[[2]string{branch, method}]
}

// Remove stops and drops the matching transaction.
func (tm *TransactionManager) Remove(branch, method string) {
	tm.mu.Lock()
	t := tm.txns[[2]string{branch, method}]
	delete(tm.txns, [2]string{branch, method})
	tm.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// All returns a snapshot of the current transactions.
func (tm *TransactionManager) All() []Transaction {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	out := make([]Transaction, 0, len(tm.txns))
	for _, t := range tm.txns {
		out = append(out, t)
	}
	return out
}

// ── Parameter parsing helpers (unchanged from the pre-FSM code) ──

// extractBranch pulls the "branch" parameter out of a Via header.
func extractBranch(viaHeader string) string {
	return extractParam(viaHeader, "branch")
}

func extractParam(header, param string) string {
	for _, part := range splitSemicolon(header) {
		p := trimSpace(part)
		if len(p) > len(param)+1 && p[:len(param)+1] == param+"=" {
			return p[len(param)+1:]
		}
	}
	return ""
}

func splitSemicolon(s string) []string {
	var parts []string
	start := 0
	for i, c := range s {
		if c == ';' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}

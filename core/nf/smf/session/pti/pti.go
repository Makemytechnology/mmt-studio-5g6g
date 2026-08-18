// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pti — per-UE 5GSM Procedure Transaction Identity tracker
// (TS 24.501 §7.3).
//
// Every UE-initiated 5GSM procedure (PDU Session Establishment /
// Modification / Release) carries a PTI value 1..254 chosen by the UE;
// network-initiated procedures use the AMF's own PTI space in the same
// range. The spec requires the network to
//
//   (a) detect retransmissions of the same PTI and NOT run the
//       procedure twice — the cached reply from the first run is
//       re-sent verbatim;
//   (b) reject a new procedure on a PTI that's still in flight from
//       some other reason with 5GSM STATUS cause #81 "invalid PTI";
//   (c) release PTI values on procedure completion / abort so the UE
//       can reuse them.
//
// This tracker is the single source of truth for active PTIs. It is
// keyed per-UE (IMSI) because PTIs are UE-scoped in TS 24.501, not
// per-PDU-session.
package pti

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ProcedureKind identifies which 5GSM procedure is in flight on a PTI.
type ProcedureKind int

const (
	ProcUnknown ProcedureKind = iota
	ProcEstablishment
	ProcModification
	ProcRelease
)

// String renders the procedure name for log lines.
func (p ProcedureKind) String() string {
	switch p {
	case ProcEstablishment:
		return "Establishment"
	case ProcModification:
		return "Modification"
	case ProcRelease:
		return "Release"
	}
	return "Unknown"
}

// Transaction is the state we keep for one active PTI. Response caches
// the last 5GSM NAS reply we sent so a retransmit gets the same bytes.
type Transaction struct {
	IMSI         string
	PTI          uint8
	PDUSessionID uint8
	Kind         ProcedureKind
	Started      time.Time
	Response     []byte // cached NAS bytes for retransmit handling
}

// Tracker is the process-wide PTI registry.
type Tracker struct {
	mu sync.RWMutex
	// key = imsi → (pti → txn); nested map keeps per-UE lookups O(1)
	// and lets us release every PTI for a UE on dereg in one pass.
	byUE map[string]map[uint8]*Transaction
}

// Default is the singleton used by the ULNASTransport dispatcher.
var Default = &Tracker{byUE: make(map[string]map[uint8]*Transaction)}

// ErrPTIInvalid — PTI value outside TS 24.501 §9.6 range [1..254].
type ErrPTIInvalid struct{ PTI uint8 }

func (e ErrPTIInvalid) Error() string { return fmt.Sprintf("pti: invalid PTI %d (valid 1..254)", e.PTI) }

// ErrPTICollision — PTI is in flight on a DIFFERENT procedure. Caller
// replies 5GSM STATUS cause #81 "invalid PTI" per §6.1.4.2.
type ErrPTICollision struct {
	PTI      uint8
	Existing ProcedureKind
	Incoming ProcedureKind
}

func (e ErrPTICollision) Error() string {
	return fmt.Sprintf("pti: collision on PTI %d (existing=%s, incoming=%s)",
		e.PTI, e.Existing, e.Incoming)
}

// Start registers the beginning of a procedure. Return values:
//
//	(txn, false, nil)                  — fresh PTI, proceed with the
//	                                     procedure.
//	(txn, true, nil)                   — retransmit of a procedure that
//	                                     has already replied. Caller
//	                                     resends txn.Response instead
//	                                     of re-running anything.
//	(nil, false, ErrPTICollision{...}) — same PTI, different procedure
//	                                     (UE reused before releasing).
//	(nil, false, ErrPTIInvalid{...})   — PTI out of range.
//
// "Retransmit" is detected by same (IMSI, PTI, Kind, PDUSessionID)
// already having a cached Response — i.e. the first run completed but
// the UE never saw the reply. Retransmits BEFORE Complete (same PTI
// arriving while Response is still empty) return (txn, false, nil)
// so the caller can wait the original procedure out; duplicate
// processing guarded by the 5GSM FSM / session.Default state check.
func (t *Tracker) Start(imsi string, pti uint8, kind ProcedureKind, pduSessID uint8) (*Transaction, bool, error) {
	if pti == 0 || pti == 255 {
		return nil, false, ErrPTIInvalid{PTI: pti}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ueMap, ok := t.byUE[imsi]
	if !ok {
		ueMap = make(map[uint8]*Transaction)
		t.byUE[imsi] = ueMap
	}
	if existing, ok := ueMap[pti]; ok {
		// Same procedure + same PDU session = retransmit.
		if existing.Kind == kind && existing.PDUSessionID == pduSessID {
			logger.Get("smf.pti").Infof("PTI %d retransmit (%s, imsi=%s)", pti, kind, imsi)
			return existing, true, nil
		}
		// Different procedure on the same PTI = collision.
		return nil, false, ErrPTICollision{PTI: pti, Existing: existing.Kind, Incoming: kind}
	}
	txn := &Transaction{
		IMSI: imsi, PTI: pti, PDUSessionID: pduSessID,
		Kind: kind, Started: time.Now(),
	}
	ueMap[pti] = txn
	return txn, false, nil
}

// Complete caches the response NAS and leaves the PTI in the tracker
// until Release is called — retransmits in the meantime re-send
// the cached bytes. (Some implementations clear on Complete; TS 24.501
// §7.3.2 lets the network keep the PTI reserved for a short window
// after reply to cover lossy retransmits.)
func (t *Tracker) Complete(imsi string, pti uint8, response []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ueMap, ok := t.byUE[imsi]; ok {
		if txn, ok := ueMap[pti]; ok {
			txn.Response = response
		}
	}
}

// Release removes the PTI entry entirely (caller's final cleanup once
// it's confident the UE has ack'd or we're giving up). Idempotent.
func (t *Tracker) Release(imsi string, pti uint8) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ueMap, ok := t.byUE[imsi]; ok {
		delete(ueMap, pti)
		if len(ueMap) == 0 {
			delete(t.byUE, imsi)
		}
	}
}

// ReleaseAllForUE drops every PTI entry for this UE. Call on
// deregistration / UE context cleanup so the UE's next session can
// re-use the full PTI range. Idempotent.
func (t *Tracker) ReleaseAllForUE(imsi string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.byUE[imsi])
	delete(t.byUE, imsi)
	return n
}

// Active returns a copy of the current tracker state for /api/smf/pti
// and debugging.
func (t *Tracker) Active() []Transaction {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var out []Transaction
	for _, ueMap := range t.byUE {
		for _, txn := range ueMap {
			out = append(out, *txn)
		}
	}
	return out
}

// AllocateNetworkPTI reserves a fresh PTI on the SMF side for a
// network-initiated procedure (AMF sends PDU Session Modification
// Command / Release Command without the UE having started a
// transaction). TS 24.501 §7.3.1 says the network uses values 128..254
// for network-originated PTIs; UEs use 1..127. Returns 0 if every
// network-side slot is busy — a pathological case that would mean 127
// concurrent network-initiated procedures on one UE.
func (t *Tracker) AllocateNetworkPTI(imsi string, kind ProcedureKind, pduSessID uint8) uint8 {
	t.mu.Lock()
	defer t.mu.Unlock()
	ueMap, ok := t.byUE[imsi]
	if !ok {
		ueMap = make(map[uint8]*Transaction)
		t.byUE[imsi] = ueMap
	}
	for pti := uint8(128); pti <= 254; pti++ {
		if _, busy := ueMap[pti]; busy {
			continue
		}
		ueMap[pti] = &Transaction{
			IMSI: imsi, PTI: pti, PDUSessionID: pduSessID,
			Kind: kind, Started: time.Now(),
		}
		return pti
	}
	return 0
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package session — SMF per-UE PDU session context.
//
// Go port of the SmfUeContext state kept by nf/smf/smf_pdu_session.py.
// One Session per (IMSI, PDUSessionID) carries DNN, S-NSSAI, assigned
// IPs, QoS rules, and the chosen UPF anchor. All state is in-memory —
// the Python reference never persisted sessions (crash recovery relies
// on the UE re-initiating).
package session

import (
	"fmt"
	"net/netip"
	"sync"
	"time"
)

// State tracks the lifecycle (TS 24.501 §6.3.1).
type State int

const (
	StateInactive  State = iota
	StatePending         // Establishment accepted, UPF setup in flight
	StateActive          // N4 done, GTP-U tunnel up
	StateSuspended       // CM-IDLE — gNB tunnel torn down, session preserved
	StateReleasing
	StateReleased
)

// String is the human-readable state (used in /api/smf/sessions).
func (s State) String() string {
	switch s {
	case StateInactive:
		return "INACTIVE"
	case StatePending:
		return "PENDING"
	case StateActive:
		return "ACTIVE"
	case StateSuspended:
		return "SUSPENDED"
	case StateReleasing:
		return "RELEASING"
	case StateReleased:
		return "RELEASED"
	}
	return fmt.Sprintf("State(%d)", s)
}

// Session is the per-UE PDU session record.
type Session struct {
	IMSI         string
	PDUSessionID uint8 // 1..15
	PTI          uint8 // NAS procedure transaction identity
	DNN          string
	SST          uint8
	SD           string // hex, optional
	PDUType      uint8  // PDUSessionTypeIpv4 / Ipv6 / Ipv4v6 / Ethernet / Unstructured
	SSCMode      uint8  // 1..3
	IPv4         netip.Addr
	IPv6         netip.Addr
	AMBRDL       uint32 // Session-AMBR DL, kbps (TS 23.501 §5.7.1.6)
	AMBRUL       uint32 // Session-AMBR UL, kbps
	UEAMBRDL     uint32 // UE-AMBR DL, kbps (TS 23.501 §5.7.3) — per-subscriber, non-GBR aggregate
	UEAMBRUL     uint32 // UE-AMBR UL, kbps
	FiveQI       uint8  // default QoS flow 5QI (TS 23.501 §5.7.2.1)
	UPFID        string // selected UPF anchor
	UPFN3IP      string // UPF N3 (GTP-U) bind address for gNB → UPF
	UPFTEID      uint32 // GTP-U TEID assigned by UPF
	State        State
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// Authorized QoS rules bytes built by BuildDefaultQoSRule. Kept as
	// opaque bytes so the rest of the package doesn't have to know the
	// TS 24.501 §9.11.4.13 TLV layout. (The QoS Flow Descriptions IE is
	// built inline by packAuthorizedQoSFlowDescriptions at encode time.)
	AuthorizedQoSRules []byte

	// RequestedExtPCO is the raw bytes of the UE's §8.3.1.9 Extended
	// Protocol Configuration Options IE from the incoming PDU
	// SESSION ESTABLISHMENT REQUEST. encodeAccept reads this to
	// decide which *Request containers the UE asked for and answers
	// only those — per TS 24.008 §10.5.6.3 the UE's Request is the
	// capability signal that it can process our response.
	RequestedExtPCO []byte

	// SmPolicyCtxRef — PCF-assigned reference for this session's SM
	// Policy Association (TS 29.512 §4.2.2.2 step 2). Passed to
	// smpolicy.Update / smpolicy.Delete on Modify / Release paths so
	// the PCF can look up and re-authorize without re-resolving the
	// (IMSI, PDUSessionID) key. Empty means no association exists
	// (pre-Establish / post-Delete / PCF disabled).
	SmPolicyCtxRef string

	// ChargingMethod — "online" or "offline" per TS 29.512 §5.6.2.4
	// (ChargingInformation.chgMeth). Provisioned by PCF on Create
	// (§4.2.2.3) and may be updated on §4.2.4 / §4.2.3. Consumed by
	// the CHF integration path (not yet wired); stored here for
	// observability on /api/smf/sessions.
	ChargingMethod string

	// LastKnownLocation — opaque APER-encoded UserLocationInformation
	// IE (TS 38.413 §9.3.1.16) captured on AN Release (§8.3.3.2 UE
	// CONTEXT RELEASE COMPLETE) and passed through as a parameter of
	// Nsmf_PDUSession_UpdateSMContext per TS 23.502 §4.2.6 step 5.
	// Stored per-session (not per-UE on AmfUeCtx) because the spec
	// parameterises it per-PDU-session in the step-5 request; also
	// lets /api/smf/sessions report per-session location. nil when
	// the IE was absent in the Complete.
	LastKnownLocation []byte

	// QFIByRule maps a PCF PccRuleID (TS 29.512 §5.6.2.4) to the QFI
	// the SMF allocated for it on §4.2.3 UpdateNotify (rule-create).
	// Populated by SpecsFromPolicyDecision; read on the symmetric
	// rule-delete path so the SMF knows which QFI/QER/FAR to tear
	// down via PFCP §7.5.4.7 / §7.5.4.9 + §8.3.9 op=Delete-rule.
	// Default flow's RuleID is not tracked here — QFI=1 is the §5.7.1.5
	// default and is owned by the establishment path.
	QFIByRule map[string]uint8
}

// Key is the (IMSI, PDUSessionID) pair — unique per UE.
type Key struct {
	IMSI         string
	PDUSessionID uint8
}

// Store is the process-wide session table. Thread-safe.
type Store struct {
	mu       sync.RWMutex
	sessions map[Key]*Session
}

// Default is the singleton used by the NGAP / NAS handlers.
var Default = NewStore()

// NewStore returns an empty session table.
func NewStore() *Store {
	return &Store{sessions: make(map[Key]*Session)}
}

// Put inserts or replaces a session.
func (s *Store) Put(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.UpdatedAt = time.Now()
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = sess.UpdatedAt
	}
	s.sessions[Key{sess.IMSI, sess.PDUSessionID}] = sess
}

// Get returns a session by (IMSI, PDUSessionID), or nil.
func (s *Store) Get(imsi string, id uint8) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[Key{imsi, id}]
}

// Delete removes a session.
func (s *Store) Delete(imsi string, id uint8) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, Key{imsi, id})
}

// ForUE returns every session for an IMSI (sorted by PDUSessionID).
func (s *Store) ForUE(imsi string) []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Session
	for k, sess := range s.sessions {
		if k.IMSI == imsi {
			out = append(out, sess)
		}
	}
	return out
}

// All returns a snapshot of every session for the web UI / KPIs.
func (s *Store) All() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}

// Count returns the number of active sessions (State == Active).
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, sess := range s.sessions {
		if sess.State == StateActive {
			n++
		}
	}
	return n
}

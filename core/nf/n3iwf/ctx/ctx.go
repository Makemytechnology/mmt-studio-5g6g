// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ctx — N3IWF per-UE context.
//
// One UEContext per non-3GPP UE traversing the N3IWF, mapping the
// IKEv2 SA (RFC 7296) ↔ EAP-5G session (TS 24.502 §7.3.3) ↔ NAS
// state (TS 24.501) ↔ N2 / N3 plumbing (TS 23.501 §6.3.1) all in
// one place. The handler package mutates this; the transport layer
// looks UEs up by source IP, by IKE Initiator SPI, or by inner IP.
//
// State machine (TS 24.502 §7.3 + RFC 7296 §1.2):
//
//	StateInit       — UE seen but no IKE SA yet
//	StateIKEInit    — IKE_SA_INIT exchange complete, SK_* derived
//	StateEAP5G      — IKE_AUTH started, EAP-5G in progress (NAS via AMF)
//	StateRegistered — EAP-Success returned, signalling IPsec SA up
//	StatePDUActive  — at least one user-plane child SA established
//	StateReleased   — IKE SA torn down (TS 24.502 §7.4)
package ctx

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/nf/n3iwf/ikev2"
)

// State is the high-level UE state at the N3IWF.
type State string

const (
	StateInit       State = "INIT"
	StateIKEInit    State = "IKE_INIT"
	StateEAP5G      State = "EAP_5G"
	// StateEAPSuccess: N3IWF has sent EAP-Success in IKE_AUTH (the
	// AMF's InitialContextSetupRequest landed; Knh is on the UE
	// context). The next IKE_AUTH from the UE is expected to carry
	// AUTH only per RFC 7296 §2.16, completing the IKEv2 mutual
	// authentication and establishing the signalling IPsec SA per
	// TS 24.502 §7.3.2.2.
	StateEAPSuccess State = "EAP_SUCCESS"
	StateRegistered State = "REGISTERED"
	StatePDUActive  State = "PDU_ACTIVE"
	StateReleased   State = "RELEASED"
)

// UEContext holds everything we know about one non-3GPP UE.
//
// Locking convention: the Manager holds a mutex for the map; each
// UEContext is mutated only by code that owns the IKE message
// currently being processed for that UE — IKEv2 is half-duplex
// per RFC 7296 §2.1 ("an implementation MUST NOT generate more
// than one outstanding request"), so per-UE state changes serialise
// naturally on the message-receive path.
type UEContext struct {
	UEID         int
	UEAddrIP     string // wifi-side source IP (for re-association)
	UEAddrPort   int
	State        State

	// IKE SA — RFC 7296.
	IKEInitiator [8]byte // SPIi (set from received IKE_SA_INIT request)
	IKEResponder [8]byte // SPIr (we mint this on IKE_SA_INIT reply)
	IKENonceI    []byte  // §3.9 nonces
	IKENonceR    []byte
	DH           *ikev2.DH        // negotiated group
	DHPriv       []byte            // our private exponent
	DHPub        []byte            // our public value
	SharedKey    []byte            // §2.10 g^ir
	Keys         *ikev2.IKESAKeys  // §2.14 7-key tuple
	PRFID        uint16            // negotiated §3.3.2 PRF transform
	IntegID      uint16            // negotiated INTEG transform
	EncrID       uint16            // negotiated ENCR transform
	EncrKeyLen   int               // octets, e.g. 32 for AES-256

	// Per-direction cipher suites — built once Keys is known.
	// SuiteI uses SK_ai + SK_ei (initiator-side, i.e. UE → N3IWF
	// direction); SuiteR uses SK_ar + SK_er (N3IWF → UE).
	SuiteI *ikev2.CipherSuite
	SuiteR *ikev2.CipherSuite

	// Message ID tracking — RFC 7296 §2.2.
	NextRecvMsgID uint32 // next request we expect (incl. retransmits)
	NextSendMsgID uint32 // next response/initiated-msg ID

	// EAP-5G session state — TS 24.502 §7.3.3.
	EAPID       uint8  // §RFC 3748 §4.1 Identifier
	IMSI        string // resolved after EAP-5G NAS Registration
	SUPI        string

	// AMF-side N2 mapping — populated when InitialUEMessage goes out.
	AMFUeNgapID *int64
	RANUeNgapID *int64

	// Knh is the 32-octet (256-bit) KgNB-equivalent the AMF lands
	// in InitialContextSetupRequest's SecurityKey IE per TS 33.501
	// §6.5.2. The IPsec child-SA KDF (TS 24.502 §7.4) chains off
	// this — task #20.
	Knh []byte

	// PendingDLNAS holds a downlink NAS PDU the AMF piggybacked on
	// InitialContextSetupRequest (typically Registration Accept) —
	// per TS 24.502 §7.3.2.2 it should be delivered to the UE on
	// the signalling IPsec SA after EAP-Success / IKE_AUTH closes.
	// Until that path is wired, this just holds the bytes.
	PendingDLNAS []byte

	// IKEInitResponse caches the bytes we sent in our IKE_SA_INIT
	// response — needed verbatim as RealMessage2 in §2.15
	// ResponderSignedOctets when computing the AUTH payload of the
	// IKE_AUTH success response.
	IKEInitResponse []byte

	// IKEInitRequest caches the bytes the UE sent in its
	// IKE_SA_INIT request — RealMessage1 in §2.15 InitiatorSigned
	// Octets. Used to verify the UE's AUTH on the IKE_AUTH-final
	// message (RFC 7296 §2.16).
	IKEInitRequest []byte

	// IDiBody is the IDi payload BODY (not the generic header) the
	// UE sent in its first IKE_AUTH — preserved verbatim because
	// MACedIDForI = prf(SK_pi, RestOfInitIDPayload) where
	// "RestOfInitIDPayload" is exactly this body (§2.15 + §3.5).
	IDiBody []byte

	// ChildSAs holds the IPsec child SAs negotiated via §1.3
	// CREATE_CHILD_SA exchanges. The first entry is the signalling
	// SA (NWu §7.4); subsequent entries are per-PDU-session
	// user-plane SAs.
	ChildSAs []ChildSA

	// WaitDLNAS unblocks an in-flight IKE_AUTH response when the
	// AMF lands a DownlinkNASTransport for this UE. The handler
	// forwards the UE's EAP-Response/5G-NAS bytes to the AMF, then
	// blocks reading this channel (with timeout) so the eventual
	// IKE_AUTH response wraps the AMF-supplied NAS PDU as
	// EAP-Request/5G-NAS per TS 24.502 §7.3.3 / §9.3.2.2.3.
	//
	// Buffered (1) so a fast AMF reply isn't dropped if the handler
	// hasn't yet hit its receive site. Re-bound on every UL forward
	// so stale replies from a previous round-trip don't bleed in.
	WaitDLNAS chan []byte

	// NWu IPsec child SAs (signalling + per-PDU-session UP).
	// Filled out in later phases.
	InnerIP     string // address allocated to the UE inside the tunnel

	// Bookkeeping.
	CreatedAt    time.Time
	LastActivity time.Time
}

// HexSPIi returns the IKE Initiator SPI as a hex string for log /
// map-key purposes.
func (u *UEContext) HexSPIi() string { return hex.EncodeToString(u.IKEInitiator[:]) }

// Manager — process-wide UE registry.
type Manager struct {
	mu          sync.Mutex
	byID        map[int]*UEContext
	byAddr      map[string]int
	bySPIi      map[string]int
	byIMSI      map[string]int
	nextUEID    int
	nextRANUeID int64
	nextTEID    uint32
}

// NewManager builds a fresh manager. Tests usually want their own.
func NewManager() *Manager {
	return &Manager{
		byID:        make(map[int]*UEContext),
		byAddr:      make(map[string]int),
		bySPIi:      make(map[string]int),
		byIMSI:      make(map[string]int),
		nextUEID:    1,
		nextRANUeID: 1,
		// TEID 0 is reserved by TS 29.281 §5.1 ("the GTP-U entity
		// shall not assign the value 'all zeros' to its own TEID");
		// counter starts at 1 so AllocateTEID never hands out zero.
		nextTEID: 1,
	}
}

// Default is the process-wide manager. The handler package writes
// to it; webservice/api routes read from it.
var Default = NewManager()

// Create registers a new UE seen at addrIP:addrPort. Returns the
// fresh context — caller fills in IKE SA fields once the message
// is parsed.
func (m *Manager) Create(addrIP string, addrPort int) *UEContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextUEID
	m.nextUEID++
	now := time.Now()
	u := &UEContext{
		UEID:         id,
		UEAddrIP:     addrIP,
		UEAddrPort:   addrPort,
		State:        StateInit,
		CreatedAt:    now,
		LastActivity: now,
	}
	m.byID[id] = u
	m.byAddr[fmt.Sprintf("%s:%d", addrIP, addrPort)] = id
	return u
}

// LookupByAddr finds a UE by its (ip, port) source pair (used when
// we get a follow-up IKE message from the same address before the
// SPIi mapping is committed).
func (m *Manager) LookupByAddr(addrIP string, addrPort int) *UEContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.byAddr[fmt.Sprintf("%s:%d", addrIP, addrPort)]; ok {
		return m.byID[id]
	}
	return nil
}

// LookupByID finds a UE by its allocated UEID. Used by code paths
// that already have the ID (e.g. external N2 callbacks landing on a
// previously-stashed handle) rather than walking the SPI map.
func (m *Manager) LookupByID(id int) *UEContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byID[id]
}

// LookupBySPIi finds a UE by IKE Initiator SPI — the canonical key
// once IKE_SA_INIT has been processed.
func (m *Manager) LookupBySPIi(spii [8]byte) *UEContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.bySPIi[hex.EncodeToString(spii[:])]; ok {
		return m.byID[id]
	}
	return nil
}

// RegisterSPIi binds an Initiator SPI to an existing UE. Called by
// the handler once the IKE_SA_INIT request has been parsed.
func (m *Manager) RegisterSPIi(u *UEContext) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bySPIi[u.HexSPIi()] = u.UEID
}

// AllocateRANUeID returns a fresh RAN-UE-NGAP-ID for AMF-side
// signalling. N3IWF acts as a RAN node toward AMF (TS 23.501
// §6.3.1) so it owns the RAN-side ID space, just like a gNB does.
func (m *Manager) AllocateRANUeID() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextRANUeID
	m.nextRANUeID++
	return id
}

// AllocateTEID returns a fresh TEID for an inbound GTP-U tunnel —
// the value the N3IWF puts in PDU Session Resource Setup Response
// IEs and that the UPF will use as the destination TEID on G-PDUs
// flowing UPF→N3IWF. TS 29.281 §5.1 requires non-zero TEIDs.
//
// We use a sequential counter rather than a random draw — the spec
// only mandates non-predictability for PGW S5/S8/S2a/S2b (per
// TS 33.250); N3 toward the UPF is internal traffic.
func (m *Manager) AllocateTEID() uint32 {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.nextTEID
	m.nextTEID++
	if m.nextTEID == 0 {
		// 32-bit wrap — skip 0, jump to 1 next.
		m.nextTEID = 1
	}
	return t
}

// All returns a snapshot of every active UE for the operator API.
func (m *Manager) All() []*UEContext {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*UEContext, 0, len(m.byID))
	for _, u := range m.byID {
		out = append(out, u)
	}
	return out
}

// Remove tears down a UE context (called on TS 24.502 §7.4 IKE SA
// deletion or on inactivity timeout).
func (m *Manager) Remove(ueID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[ueID]
	if !ok {
		return
	}
	delete(m.byID, ueID)
	delete(m.byAddr, fmt.Sprintf("%s:%d", u.UEAddrIP, u.UEAddrPort))
	delete(m.bySPIi, u.HexSPIi())
	if u.IMSI != "" {
		delete(m.byIMSI, u.IMSI)
	}
}

// FreshSPIr generates a non-zero 8-octet Responder SPI per RFC 7296
// §3.1 ("Responder's SPI ... MUST be zero in the first message of an
// IKE initial exchange" implies any value MUST be unique afterwards;
// we mint a 64-bit random value).
func FreshSPIr() ([8]byte, error) {
	var spi [8]byte
	for {
		if _, err := rand.Read(spi[:]); err != nil {
			return spi, err
		}
		// §3.1 forbids zero SPI on initial; pick again on the
		// vanishingly-unlikely all-zero draw.
		var nonzero bool
		for _, b := range spi {
			if b != 0 {
				nonzero = true
				break
			}
		}
		if nonzero {
			return spi, nil
		}
	}
}

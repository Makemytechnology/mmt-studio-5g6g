// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package uectx

import (
	"sync"
	"sync/atomic"
	"time"
)

// SecurityCtx holds the UE's NAS security context per TS 33.501 §6.2.
// Hex fields are stored as raw bytes (decoders turn them into bytes once).
type SecurityCtx struct {
	// Auth vectors — TS 33.501 §6.1.3
	RAND     []byte
	XRESStar []byte
	AUTN     []byte
	KAUSF    []byte

	// UE capabilities
	UESecCap []byte
	ABBA     []byte // default: 2×0x00

	// Derived keys — TS 33.501 §6.2. K_gNB is NOT kept here — it is
	// derived just-in-time by nf/amf/security.DeriveKgNB inside
	// initialctxsetup.Send using the current UL NAS COUNT per
	// TS 33.501 §6.8.1.2.2 (security/doc.go invariant I4 — "stale
	// K_gNB is un-expressible when the key can't be stashed").
	KSEAF   []byte
	KAMF    []byte
	KNASEnc []byte
	KNASInt []byte

	// NAS header / algorithm IDs — TS 33.501 §6.7.2
	SecHdr uint8
	EEA    uint8 // ciphering algorithm ID (0..3)
	EIA    uint8 // integrity algorithm ID (0..3)

	// NAS counts — TS 24.501 §4.4.3.1 (32-bit: overflow(24) | sqn(8))
	ULNasCount uint32
	DLNasCount uint32

	AuthDone       bool
	AuthRetryCount int // TS 24.501 section 5.4.1.3: auth retry counter (max 3)

	// NGKSI — AMF-assigned 5G NAS key set identifier (TS 24.501 §9.11.3.32).
	// Value range 0..6; 7 is reserved for "no key available" and is sent by
	// the UE in RegistrationRequest when it has no active NAS context. The
	// AMF MUST pick a value different from the one the UE currently holds
	// (ue.NASKSI) — otherwise the UE returns Authentication Failure with
	// cause #71 "ngKSI already in use" (TS 24.501 §5.4.1.3.7). NGKSIAssigned
	// distinguishes "initial selection needed" from "rotated on retry".
	NGKSI         uint8
	NGKSIAssigned bool

	// Activated flips to true after Security Mode Complete has been
	// received and validated. Pre-SMC we send DL NAS plain (SHT=0 —
	// AUTHENTICATION REQUEST, IDENTITY REQUEST, SECURITY MODE COMMAND
	// itself are listed in TS 24.501 §4.4.4.3). Post-SMC, TS 24.501
	// §4.4.4.1 mandates integrity+ciphering (SHT=2) on every DL NAS.
	Activated bool

	// ── Pending (non-current) NAS security context — TS 24.501 v19.6.2 ──
	//
	// §4.4.2.1 General, verbatim:
	//   "The UE and the AMF need to be able to maintain two 5G NAS
	//    security contexts simultaneously, i.e. a current 5G NAS security
	//    context and a non-current 5G NAS security context, since:
	//     a) after a 5G re-authentication, the UE and the AMF can have
	//        both a current 5G NAS security context and a non-current
	//        5G NAS security context which has not yet been taken into
	//        use (i.e. a partial native 5G NAS security context); …"
	//
	// The non-current ctx is "taken into use" by a SECURITY MODE CONTROL
	// procedure (§4.4.2.1 + §5.4.2.4). Until then the current ctx
	// protects all signalling. §5.4.2.4 verbatim:
	//   "The AMF shall, upon receipt of the SECURITY MODE COMPLETE
	//    message, stop timer T3560. From this time onward the AMF shall
	//    integrity protect and encipher all signalling messages with the
	//    selected 5GS integrity and ciphering algorithms."
	// And §5.4.2.5 (SMC Reject revert) verbatim:
	//   "Both the UE and the AMF shall apply the 5G NAS security context
	//    in use before the initiation of the security mode control
	//    procedure, if any …"
	//
	// Implementation:
	//   - ActivateCtx writes the new (non-current) keys/counts here.
	//   - SMC DL (SHT=3) and SMC Complete UL (SHT=4) use Pending*.
	//   - SHT=1/2 traffic uses the operative (current) fields above.
	//   - SMC Complete success → PromoteContext(): Pending* → operative.
	//   - SMC Reject / abort → DiscardPending(): Pending* dropped; the
	//     operative ctx stays in use per §5.4.2.5.
	Pending           bool
	PendingKNASEnc    []byte
	PendingKNASInt    []byte
	PendingEEA        uint8
	PendingEIA        uint8
	PendingNGKSI      uint8
	PendingULNasCount uint32
	PendingDLNasCount uint32
}

// NewSecurityCtx returns a freshly initialized security context with
// the Python reference defaults (ABBA=2×0x00).
func NewSecurityCtx() *SecurityCtx {
	return &SecurityCtx{ABBA: []byte{0x00, 0x00}}
}

// AmfUeCtx is the per-UE AMF context (TS 23.502 §5.2.2.2). Only the fields
// currently wired by ported procedures are included — additional fields
// land as the corresponding Python procedure is ported.
type AmfUeCtx struct {
	mu sync.RWMutex

	// ── NGAP identity ──
	AmfUeNGAPID int64 // allocated by the AMF (1..2^32-1)
	RanUeNGAPID int64 // from the gNB

	// ── Subscriber identity ──
	IMSI   string
	MSISDN string

	// ── Transport ──
	// GnbKey uniquely identifies the serving gNB (typically the gNB IP).
	// Keeping the gNB as an opaque string avoids a cyclic import with gnbctx.
	GnbKey string

	// ── NAS security ──
	Security *SecurityCtx

	// ── Registration parameters (TS 24.501 §5.5.1) ──
	RegistrationType string // initial | mobility | periodic | emergency
	AccessType       string // 3GPP | non-3GPP | both
	FollowOnRequest  bool
	RegistrationTime time.Time
	NASKSI           int   // TS 24.501 §9.11.3.32
	T3512Value       uint8 // GPRS Timer 3 encoded byte (TS 24.501 §9.11.2.5)

	// MICORequested captures whether the UE's RegistrationRequest
	// included a MICO indication IE (TS 24.501 §9.11.3.31). When true
	// and AMF policy accepts MICO, sendRegistrationAccept echoes the
	// IE so the UE knows it's granted. Reset between registrations.
	MICORequested bool

	// LastRegRequestPDU holds the inner plaintext bytes of the most
	// recent RegistrationRequest we processed for this UE, used by
	// checkCollision to implement TS 24.501 §5.5.1.2.8 d/e — "if IEs
	// differ, abort and restart the procedure; if IEs do not differ,
	// resend Registration Accept and restart T3550" (d.2) / "ignore
	// the duplicate" (e.2). Raw-bytes compare is spec-conservative
	// since the plain RR body carries no sequence-number / timestamp.
	LastRegRequestPDU []byte

	// InitialRRCleartextOnly captures whether the RR that opened this
	// registration arrived without a NAS Message Container IE (i.e.
	// §4.4.6 case (a): UE had no valid 5G NAS security context, sent
	// cleartext IEs only). When true, the AMF sets the RINMR bit in
	// Security Mode Command (TS 24.501 §4.4.6 + §5.4.2.2) so the UE
	// will include its entire RR in the SMC Complete NAS Message
	// Container, and applyContainerRRToUE enriches ue from that inner.
	InitialRRCleartextOnly bool

	// TMSI5G is the 32-bit 5G-TMSI assigned to this UE for the 5G-GUTI
	// (TS 23.003 §2.10.1). Allocated on first Registration Accept and
	// reused on retransmissions so the UE sees the same GUTI.
	TMSI5G uint32

	// ── State machines ──
	RM       RMState
	CM       CMState
	GMMProc  GMMProcedure
	GMMSub   GMMSubStep
	NGAPProc NGAPProcedure

	// RRC reflects the RAN-layer RRC state of the UE as last
	// reported by the NG-RAN node via TS 38.413 §8.3.5 "RRC Inactive
	// Transition Report" (§9.2.2.10 message). Default RRCConnected
	// until the gNB explicitly reports RRCInactive. Read by the
	// CN-based MT communication-handling path (TS 23.502 §4.8.1.1a)
	// — once gap B is wired the SMF/UPF FAR will be switched to
	// buffer-with-notify when this is RRCInactive.
	RRC RRCState

	// RRCTransitionAt records when the last RRC transition was
	// observed. Mirrors §9.3.1.92 RRC State IE wall-clock arrival
	// — useful for paging-attempt timeouts (TS 23.502 §4.8.2.2b).
	RRCTransitionAt time.Time

	// HardRemoveOnComplete arms the UE Context Release Complete handler
	// (uectxrelease.handleComplete) to call Registry.Remove instead of
	// Registry.ClearVolatile when the §8.3.3.2 Complete arrives. Set by
	// callers driving full-removal cases per TS 33.501 §6.8.1.1.1 — case
	// 1 (registration reject) and case 2.c (UDM "subscription withdrawn")
	// — where ALL security parameters must be erased. Other RM=DEREG
	// paths (UE-initiated non-switch-off dereg, AMF-initiated explicit /
	// implicit dereg, switch-off-with-context-retained) leave this false
	// so handleComplete keeps the cached 5G NAS security context for the
	// next §4.4 reuse path. Replaces the prior pattern of Remove'ing the
	// ctx eagerly inside abortRegistrationAndReleaseN1, which dropped the
	// ctx between sending UE CONTEXT RELEASE COMMAND and receiving the
	// gNB's COMPLETE, so the COMPLETE landed on a stale ctx and the AMF
	// emitted §10.x Error Indication ("unknown-local-UE-NGAP-ID").
	HardRemoveOnComplete bool

	// ── NGAP UE context at gNB (TS 38.413 §8.3/§8.6) ──
	GnbContextEstablished bool
	UEContextRequest      bool

	// UserLocation* carry the most recent NR cell + tracking area the UE
	// was seen at. Updated on every NAS-carrying NGAP message (Initial UE
	// Message, Uplink NAS Transport) per TS 38.413 §9.2.2.2
	// UserLocationInformation. Used for:
	//   - mobility decisions (compare against TAI list)
	//   - paging target selection (NRCGI → last-known cell)
	//   - OAM mobility reports.
	// Empty until the first NAS-carrying NGAP message arrives.
	UserLocationPLMN  []byte // 3-byte BCD PLMN (from TAI.PLMNIdentity)
	UserLocationTAC   []byte // 3-byte TAC (from TAI.TAC)
	UserLocationNRCGI []byte // 5-byte NR Cell Identity (36-bit right-shifted to 5 bytes)

	// ── Pending NAS PDU (sent after InitialContextSetupResponse) ──
	PendingNasPdu []byte
	NasPdu        []byte

	// ── Last retransmittable NAS command bytes ──
	// Populated by the sender right before the FSM transition that arms
	// its guard timer (T3550 / T3560 / T3570 etc.). The FSM's
	// Retransmit hook re-emits these bytes up to NASMaxRetransmit times
	// per TS 24.501 §10.2 Table 10.2.1. Single slot because only one
	// AMF→UE retransmit timer is ever armed at a time (states are
	// mutually exclusive); overwritten on each new sender fire.
	RetxNASPDU []byte

	// ── UE Radio Capability ────────────────────────────────────────
	// Populated by the gNB via UE RADIO CAPABILITY INFO INDICATION
	// (TS 38.413 §8.14.1). ASN.1 types (NGAP-IEs.asn):
	//   UERadioCapability                 ::= OCTET STRING       (line 16785)
	//   UERadioCapabilityForPaging        ::= SEQUENCE { ... }   (line 16787)
	//   XrDeviceWith2Rx                   ::= ENUMERATED { true, ... } (line 17689)
	//
	// Storage + lifecycle rules per TS 23.501 §5.4.4.1 "UE radio
	// capability information storage in the AMF":
	//   "the AMF shall store the UE Radio Capability information
	//    during CM-IDLE state for the UE and RM-REGISTERED state for
	//    the UE and the AMF shall if it is available, send its most
	//    up to date UE Radio Capability information to the RAN in the
	//    N2 REQUEST message, i.e. INITIAL CONTEXT SETUP REQUEST or UE
	//    RADIO CAPABILITY CHECK REQUEST."
	//
	// Replace-not-merge semantics per TS 38.413 §8.14.1.2:
	//   "The UE radio capability information received by the AMF
	//    shall replace previously stored corresponding UE radio
	//    capability information in the AMF for the UE, as described
	//    in TS 23.501."
	//
	// Empty until the gNB reports them.
	UERadioCapability          []byte // IE 117 — RRC-format NR UE capability container (TS 38.413 §9.3.1.74)
	UERadioCapabilityForPaging []byte // IE 118 — paging subset (TS 38.413 §9.3.1.68), raw APER kept for replay
	UERadioCapabilityEUTRA     []byte // IE 265 — E-UTRA-format container (TS 38.413 §9.3.1.74a)
	// XrDeviceWith2Rx = IE 428. No standalone §9.3.1.x clause — defined
	// inline as `ENUMERATED (true, …)` inside the §9.2.13.1 UE RADIO
	// CAPABILITY INFO INDICATION message IE table; the IE table's
	// semantics column cross-refs TS 38.300 [8] ("Indicates the UE is
	// a 2Rx XR UE as defined in TS 38.300 [8]"). TS 38.300 not vendored
	// locally — we store the enum value verbatim and don't act on it
	// until XR optimisations land. §8.14.1.2 mandate:
	//   "If the UE RADIO CAPABILITY INFO INDICATION message includes
	//    the XR Device with 2Rx IE, the AMF shall, if supported,
	//    store this information and use it accordingly."
	// ENUMERATED{true, ...} → nil (not present) vs non-nil pointer to
	// the enum value. Go int64-ptr keeps the extension-index accurate
	// when the gNB sends a future-spec value.
	XRDeviceWith2Rx *int64

	// ── PDU sessions (pdu_session_id → *AmfPduSession) ──
	PDUSessions map[int]*AmfPduSession

	// ── NSSAI (TS 23.502 §4.2.2.2.2 step 4 — NSSF selection) ──
	// Stored as opaque any to avoid an import cycle with nf/nssf.
	// Actual type: []nssf.SNSSAI / []nssf.RejectedSNSSAI.
	AllowedNSSAI    any
	RejectedNSSAI   any
	SubscribedNSSAI any

	// RecommendedCellsForPaging stores the opaque APER bytes of the
	// InfoOnRecommendedCellsAndRANNodesForPaging IE (§9.3.1.100)
	// that the NG-RAN may return in UE CONTEXT RELEASE COMPLETE.
	//
	// TS 38.413 §8.3.3.2 (verbatim): "If the Information on
	//   Recommended Cells and RAN Nodes for Paging IE is included
	//   in the UE CONTEXT RELEASE COMPLETE message, the AMF shall,
	//   if supported, store it and may use it for subsequent paging."
	//
	// On the next Paging procedure (§9.2.3.1) the stored value is
	// re-emitted as part of the Assistance Data for Paging IE
	// (§9.3.1.69) → Assistance Data for Recommended Cells
	// (§9.3.1.70) → Recommended Cells for Paging (§9.3.1.71).
	//
	// Stored as []byte (APER-encoded) so this package stays free of
	// asn1go imports; the paging builder decodes on use.
	RecommendedCellsForPaging []byte

	// PendingN1N2Sessions records PDU session IDs for which the SMF
	// has invoked Namf_Communication_N1N2MessageTransfer (TS 23.502
	// §4.2.3.3 step 3a) while the UE was CM-IDLE. Each entry
	// represents a suspended PDU session with DL data waiting at
	// the UPF; when the UE returns CM-CONNECTED via Service Request
	// (§4.2.3.2) the AMF sends PDU Session Resource Setup Request
	// per ID to reactivate the N3 tunnel. Cleared after reactivation
	// or on explicit Release.
	PendingN1N2Sessions []uint8

	// PendingReleasePDUList holds the PDU Session ID(s) the NG-RAN
	// reported as having active N3 user plane in UE CONTEXT RELEASE
	// REQUEST (TS 38.413 §9.2.2.4 IE 133 "PDU Session Resource
	// List for Context Release Request"). Per TS 23.502 §4.2.6
	// step 1, that list is the authoritative source for "which
	// sessions had active N3 at the gNB at release time"; step 4's
	// same-named IE in UE CONTEXT RELEASE COMPLETE may be empty or
	// omitted when the gNB already tore the N3 down before
	// acknowledging. suspendPDUSessions (handleComplete) uses the
	// step-4 list when present; when absent this stashed step-1b
	// list is the fallback — strict per-spec handling instead of
	// blanket-deactivate-all. Cleared on Release Complete.
	PendingReleasePDUList []uint8

	// LastKnownLocation stores the opaque APER bytes of the
	// UserLocationInformation IE (§9.3.1.16) the NG-RAN may return
	// in UE CONTEXT RELEASE COMPLETE.
	//
	// TS 38.413 §8.3.3.2 (verbatim): "If the User Location
	//   Information IE is included in the UE CONTEXT RELEASE
	//   COMPLETE message, the AMF shall handle this information as
	//   specified in TS 23.502 [10]."
	//
	// TS 23.502 §4.2.6 step 5 (verbatim): "the AMF invokes
	//   Nsmf_PDUSession_UpdateSMContext Request (PDU Session ID,
	//   PDU Session Deactivation, Cause, Operation Type, User
	//   Location Information, Age of Location Information, …)."
	//
	// i.e. the UL Info is a parameter of the per-session Deactivate
	// call; the SMF then includes it in the N4 Session Modification
	// (§4.2.6 step 6a) and, when CHF charging is wired, forwards to
	// Nchf. Storage on the UE ctx also enables Location-based
	// Services (Namf_Location) to fetch a cached last-known value.
	LastKnownLocation []byte
}

// AmfPduSession is the AMF's view of a PDU session. The authoritative session
// state lives in SMF — AMF only tracks enough to route downlink NAS + coordinate
// NGAP PDU Session Resource procedures. Full port pending SMF work.
type AmfPduSession struct {
	PDUSessionID int
	DNN          string
	SST          int
	SD           string
	State        string // "ACTIVE" | "RELEASING" | etc.
}

// New returns a fresh context with default states + a blank security block.
func New(amfUeID, ranUeID int64, gnbKey, imsi string) *AmfUeCtx {
	return &AmfUeCtx{
		AmfUeNGAPID: amfUeID,
		RanUeNGAPID: ranUeID,
		GnbKey:      gnbKey,
		IMSI:        imsi,
		AccessType:  "3GPP",
		Security:    NewSecurityCtx(),
		RM:          RMDeregistered,
		CM:          CMIdle,
		GMMProc:     GMMProcNone,
		GMMSub:      GMMSubNone,
		NGAPProc:    NGAPProcNone,
		// TS 38.413 §9.3.1.92 RRC State default: until the gNB
		// explicitly reports inactive via §8.3.5, the UE on N1 is
		// served as RRCConnected. Initialising here prevents the
		// §8.3.5 handler from logging a "" → INACTIVE transition.
		RRC:         RRCConnected,
		PDUSessions: make(map[int]*AmfPduSession),
	}
}

// ── Registry ────────────────────────────────────────────────────────────

// Registry holds all active UE contexts. Thread-safe.
type Registry struct {
	mu        sync.RWMutex
	byAmfID   map[int64]*AmfUeCtx
	byRanKey  map[string]int64 // "gnbKey|ranUeID" → amfUeID
	byIMSI    map[string]int64
	nextAmfID atomic.Int64 // AMF-UE-NGAP-ID allocator (1..2^32-1)

	// removeHooks fire after every Remove(). Used by GMM / NGAP FSM
	// packages to drop their per-UE map entries so removed contexts
	// don't leak across the UE lifecycle.
	removeHooks []func(*AmfUeCtx)
}

// NewRegistry returns an empty registry with AMF-UE-NGAP-ID starting at 1.
func NewRegistry() *Registry {
	r := &Registry{
		byAmfID:  make(map[int64]*AmfUeCtx),
		byRanKey: make(map[string]int64),
		byIMSI:   make(map[string]int64),
	}
	r.nextAmfID.Store(0)
	return r
}

// Default is the process-wide registry used by the ngap handlers.
var Default = NewRegistry()

// AllocateAmfID returns a fresh AMF-UE-NGAP-ID (1..2^32-1).
func (r *Registry) AllocateAmfID() int64 {
	for {
		id := r.nextAmfID.Add(1)
		if id >= 1<<32 {
			// Extremely unlikely to wrap in practice — but clamp to the 32-bit
			// range defined by TS 38.413 §9.3.3.1.
			r.nextAmfID.Store(0)
			continue
		}
		r.mu.RLock()
		_, exists := r.byAmfID[id]
		r.mu.RUnlock()
		if !exists {
			return id
		}
	}
}

// Insert adds a context to every index. Idempotent on the (ranUe, gnbKey) pair.
func (r *Registry) Insert(ue *AmfUeCtx) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byAmfID[ue.AmfUeNGAPID] = ue
	if ue.GnbKey != "" {
		r.byRanKey[ranKey(ue.GnbKey, ue.RanUeNGAPID)] = ue.AmfUeNGAPID
	}
	if ue.IMSI != "" {
		r.byIMSI[ue.IMSI] = ue.AmfUeNGAPID
	}
}

// LookupByAmfID returns the context or nil.
func (r *Registry) LookupByAmfID(id int64) *AmfUeCtx {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byAmfID[id]
}

// LookupByRanKey returns the context matching a (ranUeID, gnbKey) pair, or nil.
func (r *Registry) LookupByRanKey(gnbKey string, ranUeID int64) *AmfUeCtx {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id, ok := r.byRanKey[ranKey(gnbKey, ranUeID)]; ok {
		return r.byAmfID[id]
	}
	return nil
}

// LookupByIMSI returns the context or nil.
func (r *Registry) LookupByIMSI(imsi string) *AmfUeCtx {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if id, ok := r.byIMSI[imsi]; ok {
		return r.byAmfID[id]
	}
	return nil
}

// LookupByTMSI returns the first cached UE whose assigned 5G-TMSI
// matches the supplied value, or nil. Used to resolve a UE's SUPI
// from a 5G-GUTI (TS 24.501 §9.11.3.4 figure 9.11.3.4.3 — TMSI field
// is the last 4 octets) when a Mobility / Periodic Registration
// Request arrives and ue.IMSI isn't yet populated. Linear scan —
// acceptable because the TMSI→IMSI lookup only runs once at RR time
// per UE and the active-UE count is bounded by AMF capacity.
// TODO(spec: TS 23.501 §5.9.4 + §5.9.4a "5G-GUTI and 5G-S-TMSI") —
//
//	a proper AMF would keep a byTMSI index so this is O(1). The
//	byIMSI map is the template; wire a parallel byTMSI on Insert()
//	once TMSI5G allocation is stable across the code-base.
func (r *Registry) LookupByTMSI(tmsi uint32) *AmfUeCtx {
	if tmsi == 0 {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ue := range r.byAmfID {
		if ue.TMSI5G == tmsi {
			return ue
		}
	}
	return nil
}

// ClearVolatile wipes all CM-CONNECTED / RM-REGISTERED-transient
// state from a UE context while preserving the 5G NAS security
// context, IMSI, and 5G-TMSI so the next registration can take the
// cached context into use per TS 24.501 §4.4 without re-running
// primary authentication.
//
// Spec anchor — TS 33.501 §6.8.1.1.1 "Transition from RM-REGISTERED
// to RM-DEREGISTERED" (PDF: specs/3gpp/ts_133501v190600p.pdf):
//
//	"2. Deregistration:
//	    a. UE-initiated
//	       i.  If the reason is switch off then all the remaining
//	           security parameters shall be removed from the UE and
//	           AMF with the exception of the current native 5G NAS
//	           security context … which should remain stored in the
//	           AMF and UE.
//	       ii. If the reason is not switch off then AMF and UE shall
//	           keep all the remaining security parameters.
//	    b. AMF-initiated
//	       i.  Explicit: all the remaining security parameters shall
//	           be kept in the UE and AMF if the de-registration type
//	           is 're-registration required'.
//	       ii. Implicit: all the remaining security parameters shall
//	           be kept in the UE and AMF."
//
// Additional cleanup — TS 23.501 §5.4.4.1:
//
//	"The AMF deletes the UE radio capability when the UE RM state
//	 in the AMF transitions to RM-DEREGISTERED."
//
// What this method clears vs keeps:
//
//	CLEARED (volatile state tied to the signalling connection that
//	         just ended)
//	  CM / RM / GMMProc / GMMSub / NGAPProc
//	  GnbContextEstablished, UEContextRequest
//	  PDUSessions map
//	  RetxNASPDU, LastRegRequestPDU
//	  InitialRRCleartextOnly
//	  UE Radio Capability (NR / EUTRA-Format / Paging / XrDeviceWith2Rx)
//
//	PRESERVED (the "current native 5G NAS security context" per
//	           §6.8.1.1.1, plus the identifiers that let a future
//	           RR find this ctx)
//	  AmfUeNGAPID, RanUeNGAPID, GnbKey
//	  IMSI, MSISDN
//	  TMSI5G (→ LookupByTMSI hit on next RR's 5G-GUTI)
//	  Security (KAMF, KNASInt, KNASEnc, KSEAF, KAUSF, UESecCap,
//	            UL/DL NAS COUNTs, NGKSI, NGKSIAssigned, EEA, EIA,
//	            Activated, AuthDone)
//	  Subscribed / Allowed / Rejected NSSAI (lifecycle tied to the
//	            subscription, not the signalling connection)
//
// This fires the same remove-hooks as Remove() — timer cancel, GMM
// FSM drop, NGAP FSM drop, PTI release — so the FSM and timer state
// reset cleanly and the next registration starts from
// StateDeregistered on a fresh FSM instance. The ctx stays indexed
// by AmfUeNGAPID / RanUeNGAPID / IMSI so the caller can continue to
// reference it.
//
// Use Remove() (not this) for TS 33.501 §6.8.1.1.1 case 1
// (registration reject) and case 2.c (UDM subscription-withdrawn):
// those paths require removing ALL security params, including the
// current native 5G NAS security context.
func (r *Registry) ClearVolatile(ue *AmfUeCtx) {
	if ue == nil {
		return
	}
	// Fire hooks without unregistering from the maps. Hooks live
	// outside this package and take their own locks; we must not
	// hold r.mu during the callback (hook → FSM lock → timer-mgr
	// lock ordering cannot be violated here any more than in
	// Remove()).
	r.mu.RLock()
	hooks := append([]func(*AmfUeCtx){}, r.removeHooks...)
	r.mu.RUnlock()
	for _, h := range hooks {
		h(ue)
	}
	// Reset volatile state. Security ctx, IMSI, TMSI5G, and the
	// registry indices are deliberately untouched.
	ue.mu.Lock()
	ue.CM = CMIdle
	ue.RM = RMDeregistered
	ue.GMMProc = GMMProcNone
	ue.GMMSub = GMMSubNone
	ue.NGAPProc = NGAPProcNone
	ue.GnbContextEstablished = false
	ue.UEContextRequest = false
	ue.PDUSessions = map[int]*AmfPduSession{}
	ue.RetxNASPDU = nil
	ue.LastRegRequestPDU = nil
	ue.InitialRRCleartextOnly = false
	// TS 23.501 §5.4.4.1 — delete UE radio capability on
	// transition to RM-DEREGISTERED.
	ue.UERadioCapability = nil
	ue.UERadioCapabilityForPaging = nil
	ue.UERadioCapabilityEUTRA = nil
	ue.XRDeviceWith2Rx = nil
	ue.mu.Unlock()
}

// Remove deletes a context from every index. Safe if ue is unknown.
// Use this ONLY for the §6.8.1.1.1 paths that remove all security
// params (registration reject, UDM subscription-withdrawn,
// switch-off if the current native 5G NAS security context cannot be
// retained for other reasons). For the common MO / MT dereg paths
// use ClearVolatile to satisfy §6.8.1.1.1 cases 2.a.ii / 2.b.
func (r *Registry) Remove(ue *AmfUeCtx) {
	if ue == nil {
		return
	}
	r.mu.Lock()
	delete(r.byAmfID, ue.AmfUeNGAPID)
	if ue.GnbKey != "" {
		delete(r.byRanKey, ranKey(ue.GnbKey, ue.RanUeNGAPID))
	}
	if ue.IMSI != "" {
		delete(r.byIMSI, ue.IMSI)
	}
	hooks := append([]func(*AmfUeCtx){}, r.removeHooks...)
	r.mu.Unlock()

	// Invoke cleanup hooks without holding r.mu — hooks (GMM FSM drop,
	// timer cancel, etc.) take their own locks and we must never
	// deadlock on a cross-package lock order.
	for _, h := range hooks {
		h(ue)
	}
}

// RegisterRemoveHook installs a callback fired after every successful
// Remove(ue). Used by the GMM FSM package to drop its per-UE map entry
// (otherwise fsmReg keeps a live pointer to the removed ctx, leaking
// memory across the UE's lifecycle). Callers register at init time.
func (r *Registry) RegisterRemoveHook(h func(*AmfUeCtx)) {
	if h == nil {
		return
	}
	r.mu.Lock()
	r.removeHooks = append(r.removeHooks, h)
	r.mu.Unlock()
}

// RemoveAllForGnb releases every UE associated with a gNB key. Returns count.
func (r *Registry) RemoveAllForGnb(gnbKey string) int {
	r.mu.Lock()
	var victims []*AmfUeCtx
	for _, ue := range r.byAmfID {
		if ue.GnbKey == gnbKey {
			victims = append(victims, ue)
		}
	}
	for _, ue := range victims {
		delete(r.byAmfID, ue.AmfUeNGAPID)
		delete(r.byRanKey, ranKey(ue.GnbKey, ue.RanUeNGAPID))
		if ue.IMSI != "" {
			delete(r.byIMSI, ue.IMSI)
		}
	}
	hooks := append([]func(*AmfUeCtx){}, r.removeHooks...)
	r.mu.Unlock()

	// Fire hooks outside r.mu — bulk teardown (gNB disconnect cascade)
	// would otherwise deadlock because the GMM FSM Drop hook takes
	// fsmRegMu while we hold r.mu.
	for _, ue := range victims {
		for _, h := range hooks {
			h(ue)
		}
	}
	return len(victims)
}

// Count returns the number of active UE contexts.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byAmfID)
}

// Snapshot returns a copy of every active UE pointer — safe to iterate
// without holding the registry lock. Used by the web UI / /api/amf/ues.
func (r *Registry) Snapshot() []*AmfUeCtx {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*AmfUeCtx, 0, len(r.byAmfID))
	for _, ue := range r.byAmfID {
		out = append(out, ue)
	}
	return out
}

// SnapshotForGnb returns UE contexts associated with a specific gNB.
func (r *Registry) SnapshotForGnb(gnbIP string) []*AmfUeCtx {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*AmfUeCtx
	for _, ue := range r.byAmfID {
		if ue.GnbKey == gnbIP {
			out = append(out, ue)
		}
	}
	return out
}

// RegisteredCount returns contexts in RM-REGISTERED state.
func (r *Registry) RegisteredCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, ue := range r.byAmfID {
		if ue.RM == RMRegistered {
			n++
		}
	}
	return n
}

func ranKey(gnbKey string, ranUeID int64) string {
	// "gnbKey|ranUeID" — simple, non-colliding since ranUeID is 40 bits.
	const hexdigits = "0123456789abcdef"
	var buf [24]byte
	for i := 0; i < 16; i++ {
		buf[15-i] = hexdigits[byte(ranUeID>>(i*4))&0x0f]
	}
	return gnbKey + "|" + string(buf[:16])
}

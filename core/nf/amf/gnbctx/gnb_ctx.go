// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package gnbctx — per-gNB context tracked by the AMF per TS 38.413 §5 / TS 38.412 §7.
//
// Go port of nf/amf/ngap/ngap_gnb_ctx.py. One GnbCtx per SCTP association.
// Populated on NG Setup Request with the gNB identity, supported TAs,
// broadcast PLMNs, and slice support lists.
//
// The network-facing side (SCTPConn) is abstracted via the Conn interface so
// the package builds on platforms without SCTP (development on Windows/macOS).
// Linux production builds wire a real sctp.SCTPConn implementation.
package gnbctx

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// BroadcastPLMN represents one entry in the BroadcastPLMNList
// (TS 38.413 §9.3.1.12) — a PLMN + the slices it advertises on the TA.
type BroadcastPLMN struct {
	PLMN   []byte // 3 bytes, BCD-packed (TS 23.003 §2.2)
	Slices []Slice
}

// Slice is a single (S-NSSAI, optional GINs) entry. Raw bytes match the
// wire/ASN.1 encoding handed back by the NGAP decoder — callers convert.
type Slice struct {
	SNSSAIRaw []byte // encoded S-NSSAI octets
}

// SupportedTAItem — TS 38.413 §9.2.6.1.
//
//	{TAC, list of BroadcastPLMNItem}
type SupportedTAItem struct {
	TAC             []byte // 3 bytes, TS 23.003 §19.4.2.3
	BroadcastPLMNs  []BroadcastPLMN
}

// Conn is the minimal transport surface needed by the AMF NGAP layer.
// A Linux build swaps this for the real SCTP conn; tests / dev use a fake.
type Conn interface {
	// Send writes a complete NGAP PDU on the given SCTP stream. Stream 0 is
	// reserved for non-UE signalling (TS 38.412 §7).
	Send(data []byte, stream int) error
	// RemoteAddr returns "ip:port" for logging.
	RemoteAddr() string
	// Close terminates the SCTP association.
	Close() error
}

// GnbCtx is the per-gNB state. Created on SCTP accept, populated on NG Setup.
type GnbCtx struct {
	mu sync.RWMutex

	conn         Conn
	GnbIP        string    // canonical dotted-quad / IPv6 string
	GnbName      string    // set by NG Setup (TS 38.413 §9.3.1.6)
	GnbID        string    // Global gNB ID from NG Setup (§9.3.1.5)
	PagingDRX    string    // TS 38.413 §9.3.1.9
	Connected    bool
	ConnectedAt  time.Time
	NumSCTPStreams int     // negotiated via SCTP INIT — default 2 until COMM_UP delivers the real count

	// SupportedTAList — TS 38.413 §9.2.6.1.
	SupportedTAs []SupportedTAItem

	// superseded is set when a fresh SCTP association from the same gNB
	// IP has arrived and the accept loop has handed authority to the new
	// GnbCtx. The prior accept goroutine still owns its conn, but its
	// Recv unblocks (Supersede closes the conn) and its deferred cleanup
	// short-circuits — the new association already cleared the prior
	// UE state via cascadeNGResetForGnb at supersede time, per
	// TS 38.413 §8.7.1.1 (NG Setup "re-initialises the NGAP UE-related
	// contexts and erases all related signalling connections").
	superseded bool
}

// New creates a GnbCtx for a freshly-accepted association.
// The gNB identity fields remain zero until SetGnbInfo is called from NG Setup.
func New(conn Conn, gnbIP string) *GnbCtx {
	return &GnbCtx{
		conn:           conn,
		GnbIP:          gnbIP,
		Connected:      true,
		ConnectedAt:    time.Now(),
		// Start conservative: 2 streams ⇒ stream 0 (non-UE) + stream 1 (all
		// UE-associated). SCTP INIT negotiates ≥ 2 with any compliant peer,
		// so UEStream always picks stream 1 which is guaranteed safe.
		// The real negotiated count arrives in the SCTP_COMM_UP notification
		// (RFC 6458 §6.1.1 sac_outbound_streams) and bumps this up.
		NumSCTPStreams: 2,
	}
}

// SetNumSCTPStreams updates the negotiated outbound stream count. Called
// from the SCTP_COMM_UP notification handler and from the accept path
// after SCTP_STATUS succeeds. Safe for concurrent use with UEStream.
func (g *GnbCtx) SetNumSCTPStreams(n int) {
	if n < 1 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.NumSCTPStreams = n
}

// SetGnbInfo populates the context from an NG Setup Request.
func (g *GnbCtx) SetGnbInfo(name, id, pagingDRX string, tas []SupportedTAItem) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.GnbName = name
	g.GnbID = id
	g.PagingDRX = pagingDRX
	g.SupportedTAs = append(g.SupportedTAs[:0], tas...)
}

// ErrNoTransport means the gNB entry still exists but its SCTP fd
// has been closed (peer SHUTDOWN / reset). Callers can match via
// errors.Is to decide whether to log at WARN (expected teardown
// race) vs ERROR (real send failure).
var ErrNoTransport = errors.New("gNB: no transport")

// Send writes a PDU on the stream. Caller picks stream 0 for non-UE signalling
// or UEStream(amfUeID) for UE-associated signalling.
func (g *GnbCtx) Send(data []byte, stream int) error {
	g.mu.RLock()
	conn := g.conn
	g.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("%w: %s", ErrNoTransport, g.GnbIP)
	}
	return conn.Send(data, stream)
}

// IsConnected reports whether the gNB association is still live. Used
// by handlers that want to skip long-running setup work (PFCP, IP
// alloc) when the SCTP transport has already gone away.
func (g *GnbCtx) IsConnected() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.Connected && g.conn != nil
}

// UEStream returns the SCTP stream for a UE-associated message.
//
// Spec (TS 38.412 v19.0.0 §7, local PDF specs/3gpp/ts_138412v190000p.pdf):
//   "A single pair of stream identifiers shall be reserved over at least
//   one SCTP association for the sole use of NGAP elementary procedures
//   that utilize non UE-associated signalling.
//   At least one pair of stream identifiers over one or several SCTP
//   associations shall be reserved for the sole use of NGAP elementary
//   procedures that utilize UE-associated signallings."
//
// The spec does NOT mandate specific stream IDs — only that separate
// streams be reserved for each category. Our implementation convention
// picks stream 0 for non-UE and streams 1..(N-1) for UE-associated.
// UE → stream mapping is modulo the AMF-UE-NGAP-ID so per-UE ordering
// is preserved (spec: "the NG-RAN node shall use one SCTP association
// and one SCTP stream … [that] should not be changed during the
// communication of the UE-associated signalling").
func (g *GnbCtx) UEStream(amfUeID int64) int {
	g.mu.RLock()
	n := g.NumSCTPStreams
	g.mu.RUnlock()
	if n <= 1 {
		return 0
	}
	return int(amfUeID%int64(n-1)) + 1
}

// CloseConn closes the transport connection to unblock any blocked Recv.
func (g *GnbCtx) CloseConn() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn != nil {
		_ = g.conn.Close()
	}
}

// MarkDisconnected flips the Connected flag and closes the transport.
func (g *GnbCtx) MarkDisconnected() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Connected = false
	if g.conn != nil {
		_ = g.conn.Close()
		g.conn = nil
	}
}

// Supersede marks this context as replaced by a fresh SCTP association
// from the same gNB IP and closes its transport so the prior accept
// goroutine's Recv loop unblocks and its defer can short-circuit. The
// new association is already authoritative in the registry by the time
// this is called. Safe to call multiple times.
//
// Per TS 38.413 §8.7.1.1: a successful NG Setup "re-initialises the
// NGAP UE-related contexts (if any) and erases all related signalling
// connections in the two nodes like an NG Reset procedure would do."
// Applied here at SCTP-accept time as a stricter implementation —
// the prior signalling connection is the SCTP we're closing.
func (g *GnbCtx) Supersede() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.superseded = true
	g.Connected = false
	if g.conn != nil {
		_ = g.conn.Close()
		g.conn = nil
	}
}

// IsSuperseded reports whether this context was replaced by a newer
// SCTP association from the same gNB IP.
func (g *GnbCtx) IsSuperseded() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.superseded
}

// Conn returns the transport connection, or nil if disconnected.
func (g *GnbCtx) Conn() Conn {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.conn
}

// ── Query helpers ───────────────────────────────────────────────────────

// PrimaryTAC returns the first TAC as an uppercase hex string, or "".
func (g *GnbCtx) PrimaryTAC() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.SupportedTAs) == 0 {
		return ""
	}
	return hex.EncodeToString(g.SupportedTAs[0].TAC)
}

// PrimaryTA returns the first PLMN (3 BCD bytes) + TAC (3 BE bytes) served
// by the gNB, or (nil, nil) when it hasn't advertised a Supported TA yet.
// Used by the AMF to populate TS 24.501 §9.11.3.9 TAI List IEs.
func (g *GnbCtx) PrimaryTA() (plmn, tac []byte) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.SupportedTAs) == 0 || len(g.SupportedTAs[0].BroadcastPLMNs) == 0 {
		return nil, nil
	}
	ta := g.SupportedTAs[0]
	plmn = append([]byte(nil), ta.BroadcastPLMNs[0].PLMN...)
	tac = append([]byte(nil), ta.TAC...)
	return
}

// PrimaryPLMNHex returns the first broadcast PLMN as an uppercase hex string.
func (g *GnbCtx) PrimaryPLMNHex() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if len(g.SupportedTAs) == 0 || len(g.SupportedTAs[0].BroadcastPLMNs) == 0 {
		return ""
	}
	return hex.EncodeToString(g.SupportedTAs[0].BroadcastPLMNs[0].PLMN)
}

// AllTACs returns every TAC served by this gNB (uppercase hex strings).
func (g *GnbCtx) AllTACs() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]string, 0, len(g.SupportedTAs))
	for _, ta := range g.SupportedTAs {
		out = append(out, hex.EncodeToString(ta.TAC))
	}
	return out
}

// AllPLMNs returns every broadcast PLMN (deduplicated, hex-encoded).
func (g *GnbCtx) AllPLMNs() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, ta := range g.SupportedTAs {
		for _, bp := range ta.BroadcastPLMNs {
			seen[hex.EncodeToString(bp.PLMN)] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// ── Registry ────────────────────────────────────────────────────────────

// Registry is the process-wide gNB table keyed by gNB IP.
type Registry struct {
	mu    sync.RWMutex
	items map[string]*GnbCtx
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{items: make(map[string]*GnbCtx)}
}

// Default is the package-level singleton used by the SCTP server.
var Default = NewRegistry()

// Add stores the gNB under its IP (overwriting on reconnect).
func (r *Registry) Add(g *GnbCtx) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[g.GnbIP] = g
}

// Remove deletes a gNB by IP. Safe when absent.
func (r *Registry) Remove(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, ip)
}

// GetByIP returns the context or nil.
func (r *Registry) GetByIP(ip string) *GnbCtx {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.items[ip]
}

// All returns a snapshot of every gNB context.
func (r *Registry) All() []*GnbCtx {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*GnbCtx, 0, len(r.items))
	for _, g := range r.items {
		out = append(out, g)
	}
	return out
}

// Count returns the number of connected gNBs.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}

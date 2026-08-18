// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// transport.go — PFCP UDP transport with sequence-number correlation
// and T1 retransmit.
//
// Authoritative spec: TS 29.244 v19.5.0 (PDF:
// specs/3gpp/ts_129244v190500p.pdf).
//
//	§6.1    UDP/8805 destination port (verbatim): "PFCP shall use
//	        UDP as transport protocol and the registered UDP port
//	        8805 as the destination port (see clause 7.6)."
//
//	§7.2.2.4.1 Sequence Number (verbatim): "The sequence number
//	        shall be a 24-bit value. Each PFCP entity shall maintain
//	        a counter for the sequence number … and increment it by
//	        one for each new request message."
//
//	§7.6.2  Retransmission timer T1 (verbatim): "A PFCP request
//	        message shall be retransmitted upon the expiry of the
//	        T1 timer if a response has not been received. … The
//	        default value of T1 is 3 seconds and the default number
//	        of retransmissions N1 is 4."
//
// This module owns:
//   - seq allocator per Transport (monotonic, wraps at 24 bits)
//   - pending-request map keyed by seq for response correlation
//   - per-request T1 retransmit up to N1 times (§7.6.2)
//   - one rx goroutine per socket, fan-out via the pending map
//
// NOT handled here (separate module/TODOs):
//
//	TODO(spec: TS 29.244 §7.2.2.3 message duplicate detection) —
//	  dedup inbound duplicate requests by (peer, sequence number);
//	  on duplicate, re-send the cached response.
//
//	TODO(spec: TS 29.244 §7.6.3 Heartbeat procedure) — periodic
//	  §7.4.2 Heartbeat Request/Response goroutine + peer-alive
//	  state; detect peer restart via §8.2.3 Recovery Time Stamp.
//
//	TODO(spec: TS 29.244 §8.2.1 full Cause table) — currently we
//	  return the raw response bytes to the caller; caller is
//	  responsible for IE parsing incl. Cause. A small convenience
//	  wrapper that extracts Cause (§8.2.1 value 1 = accepted, etc.)
//	  would land here.
package pfcp

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Default T1 retransmission cadence + N1 retry count per §7.6.2.
// Configurable — operators with long-RTT N4 links set T1 higher.
const (
	DefaultT1 = 3 * time.Second
	DefaultN1 = 4
)

// ErrT1Exhausted signals the peer didn't respond after N1
// retransmissions. TS 29.244 §7.6.2: "If no response is received
// after N1 retransmissions, the PFCP entity shall consider the
// request as unanswered and take appropriate action".
var ErrT1Exhausted = errors.New("pfcp transport: T1 retransmit exhausted (§7.6.2)")

// ErrClosed is returned by pending operations when the transport
// is torn down before a response lands.
var ErrClosed = errors.New("pfcp transport: closed")

// EncodedMessage is what a producer hands to the transport.
// MsgType + SEID + IE bytes are assembled into a full PFCP PDU by
// Transport.SendRequest via the §7.2.2.1 header encoder. Sequence
// Number is allocated inside Transport — callers never choose their
// own to avoid seq collisions.
type EncodedMessage struct {
	MsgType uint8
	SEID    uint64 // 0 for node-level messages (§7.2.2.4.2); S flag flips accordingly
	IEs     []byte // payload bytes (what generated msg.Encode() returns)
}

// pendingRequest tracks one in-flight request awaiting its response.
type pendingRequest struct {
	seq      uint32
	peer     *net.UDPAddr
	pdu      []byte     // full wire bytes, for T1 retransmit
	ch       chan []byte // response payload lands here (single-shot)
	t1       *time.Timer
	attempts int
	deadline time.Time
}

// Transport owns one UDP socket + the sequence-number correlation
// machinery. Safe for concurrent use from multiple goroutines.
// Both client (SMF PfcpBridge) and server (UPF PFCP receiver)
// can wrap a Transport — the direction is just "who calls
// SendRequest vs Handle".
type Transport struct {
	conn *net.UDPConn

	seq atomic.Uint32 // allocator; wraps at 24 bits per §7.2.2.4.1

	mu      sync.Mutex
	pending map[uint32]*pendingRequest

	t1 time.Duration
	n1 int

	closed atomic.Bool
	log    *logger.Logger

	// handler is invoked on every inbound non-response PDU. Set by
	// the consumer (e.g. pfcp.Server wraps Transport for its
	// request-handling side). Optional — nil means drop-with-log.
	handler func(hdr *runtime.Header, payload []byte, peer *net.UDPAddr)
}

// NewTransport binds a UDP socket on addr (pass "" or "0.0.0.0:0"
// for an ephemeral port; "127.0.0.1:8805" for the well-known
// server port on loopback; or the UPF's service IP for real CUPS).
func NewTransport(addr string) (*Transport, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("pfcp transport: resolve %q: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("pfcp transport: bind %q: %w", addr, err)
	}
	t := &Transport{
		conn:    conn,
		pending: make(map[uint32]*pendingRequest),
		t1:      DefaultT1,
		n1:      DefaultN1,
		log:     logger.Get("pfcp.transport"),
	}
	t.seq.Store(0) // §7.2.2.4.1 starts at 0; first alloc yields 1.
	go t.readLoop()
	return t, nil
}

// LocalAddr returns the UDP socket's bound address (useful after
// "0.0.0.0:0" to discover the ephemeral port — typical for client
// transports or integration tests).
func (t *Transport) LocalAddr() *net.UDPAddr {
	if t.conn == nil {
		return nil
	}
	return t.conn.LocalAddr().(*net.UDPAddr)
}

// SetHandler installs the inbound non-response callback. Invoked
// for every message whose type is a Request or unsolicited PDU
// (not correlated to a pending sequence number).
func (t *Transport) SetHandler(h func(*runtime.Header, []byte, *net.UDPAddr)) {
	t.mu.Lock()
	t.handler = h
	t.mu.Unlock()
}

// SetT1 overrides the retransmission cadence (§7.6.2 default 3s).
// Zero keeps the current value. Values shorter than 1s are rejected
// — accidental tight retry loops stress the correlation map.
func (t *Transport) SetT1(d time.Duration) {
	if d < time.Second {
		return
	}
	t.mu.Lock()
	t.t1 = d
	t.mu.Unlock()
}

// Close tears down the socket. Pending requests unblock with
// ErrClosed. Idempotent.
func (t *Transport) Close() error {
	if !t.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := t.conn.Close()
	t.mu.Lock()
	defer t.mu.Unlock()
	for seq, p := range t.pending {
		if p.t1 != nil {
			p.t1.Stop()
		}
		close(p.ch)
		delete(t.pending, seq)
	}
	return err
}

// allocateSeq returns the next 24-bit sequence number. Wraps at
// 0xFFFFFF per §7.2.2.4.1. Collision with an in-flight request
// under heavy load is a theoretical concern at 16M messages of
// wrap depth — mitigated by the per-request Pending map eviction
// on response + a panic-if-collision defensive check in Send.
func (t *Transport) allocateSeq() uint32 {
	for {
		v := t.seq.Add(1) & 0x00FF_FFFF
		if v != 0 {
			return v
		}
		// wrapped to 0 — skip: 0 is reserved sentinel per §7.2.2.4.1.
	}
}

// SendRequest wraps msg in a PFCP header (§7.2.2.1), sends to
// peer, and blocks until the response arrives OR T1 × N1 expires.
// Returns the response's IE payload bytes (header stripped) on
// success.
//
// §7.6.2 Retransmission rule (verbatim): "the PFCP entity shall
// retransmit a PFCP request message … upon the expiry of the T1
// timer … up to N1 times." Our timer fires every t1; on each fire
// we re-send the same bytes (same Sequence Number — the peer is
// expected to dedup per §7.2.2.3).
func (t *Transport) SendRequest(peer *net.UDPAddr, msg EncodedMessage) ([]byte, error) {
	if t.closed.Load() {
		return nil, ErrClosed
	}
	if peer == nil {
		return nil, errors.New("pfcp transport: nil peer")
	}

	seq := t.allocateSeq()
	hdr := &runtime.Header{
		Version:        1, // TS 29.244 §7.2.2.1: PFCP version 1
		HasSEID:        msg.SEID != 0,
		MessageType:    msg.MsgType,
		SEID:           msg.SEID,
		SequenceNumber: seq,
	}
	// §7.2.2.1: Length excludes the first 4 octets of the basic
	// header. HeaderSize accounts for the SEID-optional portion.
	hdr.Length = uint16(hdr.HeaderSize() - 4 + len(msg.IEs))
	pdu := append(hdr.Encode(), msg.IEs...)

	p := &pendingRequest{
		seq:      seq,
		peer:     peer,
		pdu:      pdu,
		ch:       make(chan []byte, 1),
		deadline: time.Now().Add(time.Duration(t.n1+1) * t.t1),
	}

	t.mu.Lock()
	if _, clash := t.pending[seq]; clash {
		t.mu.Unlock()
		return nil, fmt.Errorf("pfcp transport: seq %d collision (wrap under load)", seq)
	}
	t.pending[seq] = p
	pollInterval := t.t1
	t.mu.Unlock()

	if _, err := t.conn.WriteToUDP(pdu, peer); err != nil {
		t.removePending(seq)
		return nil, fmt.Errorf("pfcp transport: send: %w", err)
	}

	// Arm T1 retransmit. Each fire counts against N1; after N1+1
	// total sends with no response we give up.
	p.t1 = time.AfterFunc(pollInterval, func() { t.onT1Expiry(seq) })

	select {
	case payload, ok := <-p.ch:
		if !ok {
			return nil, ErrClosed
		}
		return payload, nil
	case <-time.After(time.Until(p.deadline) + 100*time.Millisecond):
		t.removePending(seq)
		return nil, ErrT1Exhausted
	}
}

// onT1Expiry fires every t1 while the request is pending. §7.6.2:
// retransmit the same bytes; increment attempt; on attempt == N1,
// let the deadline timer in SendRequest finish failing.
func (t *Transport) onT1Expiry(seq uint32) {
	t.mu.Lock()
	p, ok := t.pending[seq]
	if !ok {
		t.mu.Unlock()
		return
	}
	p.attempts++
	if p.attempts >= t.n1 {
		// No more retransmits; let SendRequest's deadline select
		// hit ErrT1Exhausted.
		t.mu.Unlock()
		return
	}
	pdu := p.pdu
	peer := p.peer
	nextT1 := t.t1
	t.mu.Unlock()

	_, _ = t.conn.WriteToUDP(pdu, peer)
	t.log.Debugf("PFCP T1 retx seq=%d attempt=%d/%d peer=%s (§7.6.2)",
		seq, p.attempts, t.n1, peer)
	p.t1.Reset(nextT1)
}

func (t *Transport) removePending(seq uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if p, ok := t.pending[seq]; ok {
		if p.t1 != nil {
			p.t1.Stop()
		}
		delete(t.pending, seq)
	}
}

// readLoop drains the socket. Every inbound PDU is either:
//   - a response matching a pending request (seq → pending.ch)
//   - an unsolicited PDU (request / report) → Transport.handler
func (t *Transport) readLoop() {
	buf := make([]byte, 65536)
	for {
		if t.closed.Load() {
			return
		}
		n, from, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			if t.closed.Load() {
				return
			}
			t.log.Warnf("pfcp transport: read: %v", err)
			continue
		}
		// Copy — buf is reused on next iteration.
		pdu := make([]byte, n)
		copy(pdu, buf[:n])
		go t.dispatch(pdu, from)
	}
}

func (t *Transport) dispatch(pdu []byte, from *net.UDPAddr) {
	hdr, off, err := runtime.ParseHeader(pdu)
	if err != nil {
		t.log.Warnf("pfcp transport: header decode from %s: %v", from, err)
		return
	}
	payload := pdu[off:]

	// Responses → correlation map. The spec doesn't have a
	// request/response bit in the header; disambiguation is by
	// message type (odd = request in §7.2 enumeration, even =
	// response — though that pattern isn't universal). We take
	// the simpler approach: look up the seq in the pending map.
	// If found, it's a response; else it's a new inbound PDU.
	t.mu.Lock()
	p, isResponse := t.pending[hdr.SequenceNumber]
	if isResponse {
		delete(t.pending, hdr.SequenceNumber)
	}
	handler := t.handler
	t.mu.Unlock()

	if isResponse {
		if p.t1 != nil {
			p.t1.Stop()
		}
		p.ch <- payload
		return
	}
	if handler != nil {
		handler(hdr, payload, from)
		return
	}
	t.log.Debugf("pfcp transport: unsolicited %s (type=%d) from %s — no handler",
		messageTypeName(hdr.MessageType), hdr.MessageType, from)
}

// SendResponse writes a response PDU — the sequence number and
// SEID are echoed from the request. Used by server handlers after
// they've processed a request.
func (t *Transport) SendResponse(peer *net.UDPAddr, msgType uint8,
	seid uint64, seq uint32, payload []byte) error {
	if t.closed.Load() {
		return ErrClosed
	}
	hdr := &runtime.Header{
		Version:        1,
		HasSEID:        seid != 0,
		MessageType:    msgType,
		SEID:           seid,
		SequenceNumber: seq,
	}
	hdr.Length = uint16(hdr.HeaderSize() - 4 + len(payload))
	pdu := append(hdr.Encode(), payload...)
	_, err := t.conn.WriteToUDP(pdu, peer)
	return err
}

// messageTypeName mirrors the minimal table in messages.go for log
// output; kept local to avoid a dependency inversion (transport
// shouldn't need to know about every message type).
func messageTypeName(t uint8) string {
	switch t {
	case genpfcp.MessageTypeHeartbeatRequest:
		return "HeartbeatReq"
	case genpfcp.MessageTypeHeartbeatResponse:
		return "HeartbeatResp"
	case genpfcp.MessageTypeAssociationSetupRequest:
		return "AssocSetupReq"
	case genpfcp.MessageTypeAssociationSetupResponse:
		return "AssocSetupResp"
	case genpfcp.MessageTypeSessionEstablishmentRequest:
		return "SessEstabReq"
	case genpfcp.MessageTypeSessionEstablishmentResponse:
		return "SessEstabResp"
	case genpfcp.MessageTypeSessionModificationRequest:
		return "SessModReq"
	case genpfcp.MessageTypeSessionModificationResponse:
		return "SessModResp"
	case genpfcp.MessageTypeSessionDeletionRequest:
		return "SessDelReq"
	case genpfcp.MessageTypeSessionDeletionResponse:
		return "SessDelResp"
	case genpfcp.MessageTypeSessionReportRequest:
		return "SessReportReq"
	case genpfcp.MessageTypeSessionReportResponse:
		return "SessReportResp"
	}
	return fmt.Sprintf("type=%d", t)
}

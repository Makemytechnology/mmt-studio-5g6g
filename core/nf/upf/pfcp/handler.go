// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// handler.go — session-aware PFCP handler (UPF-side).
//
// Wraps a Transport with generated-codec decoders for the §7.5
// session-related messages and produces minimal-but-spec-compliant
// responses. Deliberately separate from the legacy Server in pfcp.go
// (which uses the hand-rolled messages.go codec) so the two paths
// can coexist during the CUPS rollout; the legacy server is kept
// only for the Heartbeat / Association Setup no-IE ack path.
//
// Authoritative spec: TS 29.244 v19.5.0 — see pfcp.go file header.
//
// Layout — one file per spec message family (debuggability lever:
// `git blame`-ing a §7.x.y bug means opening one file, not scrolling
// 1700 lines). Behaviour is identical to the pre-split monolith;
// the dispatch switch below is the single entry point that fans
// the wire types out to the per-family files:
//
//	handler.go                       — this file: types, dispatch,
//	                                   shared helpers, traceHandler.
//	handler_heartbeat.go             — §7.4.2  Heartbeat.
//	handler_association.go           — §7.4.4  Setup / Release;
//	                                   §7.4.6  Session Set Deletion.
//	handler_session_establish.go     — §7.5.2  Establishment +
//	                                   applyCreate{PDR,FAR,QER,URR}ToHook.
//	handler_session_modify.go        — §7.5.4  Modification (Update /
//	                                   Remove / Query) + Usage Report.
//	handler_session_delete.go        — §7.5.6  Deletion + tearDownSession.
//
// What is deliberately NOT wired (flagged TODO at each site):
//
//	TODO(spec: TS 29.244 §7.5.3 Created PDR IE with UP-allocated F-TEID) —
//	  on Establish Response we MUST return the UP-allocated F-TEID per
//	  PDR so the peer can complete its tunnel plumbing. Today we return
//	  Response without Created PDR.
//
//	TODO(spec: TS 29.244 §7.5.7.2 Usage Report on Session Deletion
//	  Response) — return final per-URR usage (charging anchor); this is
//	  the sync reporting path. Today the response carries only Cause.
package pfcp

import (
	"net"
	"sync"
	"sync/atomic"
	"time"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Handler is the UPF-side PFCP handler wrapped around a Transport.
// Owns the session table keyed by UP-SEID (§7.2.2.4.2); each
// Establishment Request allocates a fresh UP-SEID and stores the
// CP-SEID for response routing.
type Handler struct {
	t        *Transport
	log      *logger.Logger
	mu       sync.Mutex
	sessions map[uint64]*HandlerSession // keyed by UP-SEID

	// Reverse index for the §7.5.8 Downlink Data Report path: when
	// the C dataplane buffers a DL packet and enqueues a report, the
	// report carries (imsi, pduSessID) — not UP-SEID. Drain goroutines
	// look up the HandlerSession here to recover the UP-SEID + Peer
	// needed to build a Session Report Request.
	byIMSI map[imsiPduKey]*HandlerSession

	nextSEID atomic.Uint64

	// Manager hook (injected) — if non-nil, the handler calls
	// into the UPF session manager on Establishment / Deletion.
	// Nil is OK for the wire-only path where the cgo bridge
	// still drives the dataplane. Struct so future handlers
	// (AddFAR, AddURR…) bolt on cleanly.
	mgr ManagerHook
}

// imsiPduKey is the reverse-index key for byIMSI.
type imsiPduKey struct {
	IMSI         string
	PDUSessionID uint8
}

// HandlerSession mirrors the legacy PFCPSession but is owned by the
// session-aware Handler. Tracks both SEIDs + peer address for the
// sync-query / async-report paths that will follow (§7.5.4.10 /
// §7.5.8).
type HandlerSession struct {
	UPSEID       uint64
	CPSEID       uint64
	IMSI         string // decoded from §8.2.142 UserID SUPIF subfield
	PDUSessionID uint8  // decoded from §8.2.142 UserID NAIF subfield
	Peer         *net.UDPAddr
	CreatedAt    time.Time

	// PDRKeys maps PDR-ID → the (TEID, UE-IPv4) reverse-map keys
	// that this PDR's PDI installed at the UP function under
	// §7.5.2.2 / §7.5.4.17. Required so:
	//   - §7.5.4.6 Remove PDR can release exactly the keys this rule
	//     owned, no full-session sweep on every modify.
	//   - §7.5.6 Session Deletion can sweep every remaining entry
	//     and return the F-TEID (§5.5.1) and UE IP (§8.2.62)
	//     resources the UP function held — TS 29.244 v19.5.0 §7.5.6:
	//     UP "shall delete the PFCP session", which includes its
	//     allocated transport resources, not just the rule contexts.
	// Without this, the dataplane teid_hash / ueip_hash leak slots
	// and eventually saturate at MAX_TEID_MAP=8192.
	//
	// PDRKeys with value zero on a side (no F-TEID, or no UE-IP)
	// are skipped at unregister time. Map mutations happen only
	// from the Handler dispatch goroutine — no concurrent writers,
	// no per-session lock needed.
	PDRKeys map[uint16]PDRReverseKey

	// URR IDs the session installed via §7.5.2.4 Create URR. Used at
	// §7.5.6 deletion to fetch final per-URR vol/pkt counters via
	// hook.URRStats so the operator can confirm bytes actually flowed
	// through the user plane during the session — distinguishing
	// "signaling worked, data did not" from "data flowed normally".
	URRIDs []uint32
}

// PDRReverseKey records the F-TEID (§8.2.3) and UE-IP (§8.2.62)
// reverse-map entries a single Create PDR installed at the UP function.
// On §7.5.4.6 Remove PDR / §7.5.6 Session Deletion the entries are
// individually returned via hook.UnregisterTEID / hook.UnregisterUEIP
// so the dataplane teid_hash / ueip_hash slots are reclaimed.
type PDRReverseKey struct {
	TEID uint32 // host-order; 0 means no F-TEID was registered for this PDR
	UEIP uint32 // host-order; 0 means no UE-IP was registered for this PDR
}

// ManagerHook is the interface the Handler uses to drive the UPF
// dataplane backend (libupf_dp.so via cgo in integrated-PFCP mode)
// from decoded PFCP messages. Kept here (not importing nf/upf
// directly) to avoid an import cycle — nf/upf/pfcp is a sub-package
// of nf/upf. Implementers (nf/upf/upfloop) provide the adapter that
// forwards to upf.UPFBridge.
//
// Method signatures mirror upf.UPFBridge exactly where they overlap
// so an adapter is a straight-through delegation. Fields follow
// TS 29.244 §8.2 IE encodings:
//
//	AddPDR        — §8.2.9 PDR-ID, §8.2.11 Precedence, §8.2.12 PDI
//	                (SourceInterface §8.2.10, UE IP §8.2.7, SDF §8.2.5),
//	                §8.2.16 QFI, §8.2.18 FAR-ID, §8.2.19 URR-ID, §8.2.14 QER-ID
//	AddFAR        — §8.2.18 FAR-ID, §8.2.26 Apply Action, §8.2.24 DestIface,
//	                §8.2.56 Outer Header Creation (TEID + peer IP/port)
//	AddQER        — §8.2.14 QER-ID, §8.2.16 QFI, §8.2.8 Gate Status,
//	                §8.2.6 MBR, §8.2.7 GBR
//	AddURR        — §8.2.19 URR-ID, §8.2.2 Measurement Method,
//	                §8.2.4 Reporting Triggers, §8.2.13 Vol Thresholds
//	UpdateFAR     — §7.5.4.3 Update FAR: new Apply Action +
//	                Update Forwarding Parameters (new Outer Header Creation)
//	DeactivateDLFAR — §7.5.4.3 Update FAR with Apply Action=BUFF (§8.2.26)
type ManagerHook interface {
	CreateSession(imsi string, pduSessionID uint8, dnn string, sst uint8, sd, ueAddr uint32, pdnType uint8) error
	DeleteSession(imsi string, pduSessionID uint8) error

	// CommitSession finalises a §7.5.2 establishment after all
	// Create-PDR/FAR/QER/URR + RegisterTEID/UEIP have been applied
	// on this hook. For the cgo-backed adapter this is the trigger
	// that flushes a per-session buffer down to the C dataplane in
	// one EAL-thread excursion (docs/PERFORMANCE.md round-2 #1 — collapses
	// 11 cgo round-trips/session into 1). For test/stub backends it
	// can be a no-op. Spec-neutral: same C entry points run, just
	// fewer dispatch hops.
	CommitSession(imsi string, pduSessionID uint8) error

	AddPDR(imsi string, pduSessionID uint8, pdrID uint16, precedence uint32,
		pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
		ueIPv4, teid, n3IPv4 uint32) error
	AddFAR(imsi string, pduSessionID uint8, farID uint32, action, dstIface uint8,
		teid, peerAddr uint32, peerPort uint16, ohcType uint8) error
	AddQER(imsi string, pduSessionID uint8, qerID uint32,
		qfi, gateUL, gateDL uint8,
		mbrUL, mbrDL, gbrUL, gbrDL uint64) error
	AddURR(imsi string, pduSessionID uint8, urrID uint32,
		measMethod, reportTrigger uint8,
		volThreshUL, volThreshDL uint64, timeThresh uint32) error

	UpdateFAR(imsi string, pduSessionID uint8,
		farID, teid, peerAddr uint32, peerPort uint16) error
	DeactivateDLFAR(imsi string, pduSessionID uint8, farID uint32) error

	// UpdatePDR / UpdateQER / UpdateURR — TS 29.244 v19.5.0
	// §7.5.4.2 / .5 / .4. Wholesale-replace by ID semantics on the
	// dataplane (rule must already exist; -1 if not). Caller passes
	// the full desired-state field set decoded from the Update IE.
	UpdatePDR(imsi string, pduSessionID uint8, pdrID uint16, precedence uint32,
		pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
		ueIPv4, teid, n3IPv4 uint32) error
	UpdateQER(imsi string, pduSessionID uint8, qerID uint32,
		qfi, gateUL, gateDL uint8,
		mbrUL, mbrDL, gbrUL, gbrDL uint64) error
	UpdateURR(imsi string, pduSessionID uint8, urrID uint32,
		measMethod, reportTrigger uint8,
		volThreshUL, volThreshDL uint64, timeThresh uint32) error

	// Remove* — TS 29.244 v19.5.0 §7.5.4.6/.7/.8/.9. Each Remove *
	// IE within the §7.5.4 Modification Request "shall identify the
	// {PDR|FAR|URR|QER} to be deleted" by its mandatory ID IE. The
	// dataplane flips active=false; the classifier already short-
	// circuits inactive rules, so removal takes effect immediately.
	RemovePDR(imsi string, pduSessionID uint8, pdrID uint16) error
	RemoveFAR(imsi string, pduSessionID uint8, farID uint32) error
	RemoveQER(imsi string, pduSessionID uint8, qerID uint32) error
	RemoveURR(imsi string, pduSessionID uint8, urrID uint32) error

	SetSessionAMBR(imsi string, pduSessionID uint8, ambrUL, ambrDL uint64) error

	RegisterTEID(teid uint32, imsi string, pduSessionID uint8) error
	RegisterUEIP(ueAddr uint32, imsi string, pduSessionID uint8) error

	// UnregisterTEID / UnregisterUEIP release a previously-registered
	// reverse-map slot at the UP function. Called by handleSessionDeletion
	// for every TEID/UE-IP this Handler captured during the matching
	// §7.5.2 Establishment, so TS 29.244 v19.5.0 §7.5.6 actually returns
	// the F-TEID (§5.5.1) and UE IP (§8.2.62) resources. Idempotent.
	UnregisterTEID(teid uint32) error
	UnregisterUEIP(ueAddr uint32) error

	// UnregisterSessionKeys is the batched form — same §7.5.6 +
	// §5.5.1 + §8.2.62 release semantics, but one cgo round-trip
	// walks both slices at the dataplane EAL thread. Used by
	// handleSessionDeletion at scale to avoid the per-PDR-key
	// sequential dispatch that went super-linear past 64
	// concurrent cascades (docs/PERFORMANCE.md Run 4). Returns count of
	// keys actually released.
	UnregisterSessionKeys(teids []uint32, ueips []uint32) (int, error)

	// URRStats reads the UP function's per-URR counters (§8.2.41
	// Volume Measurement / §8.2.42 Duration Measurement). Used at
	// §7.5.6 deletion to log final per-session totals — actionable
	// when distinguishing "no throughput in this session" from
	// "signaling completed but data flowed elsewhere". Returns
	// zero counters and a non-nil error if the URR is absent or the
	// dataplane has no stats source. Best-effort: callers log and
	// proceed regardless.
	URRStats(imsi string, pduSessionID uint8, urrID uint32) (volUL, volDL, pktUL, pktDL uint64, err error)
}

// NewHandler wraps a Transport with the session-aware codec path.
// Safe to install the handler before or after Transport.readLoop
// starts.
func NewHandler(t *Transport, mgr ManagerHook) *Handler {
	h := &Handler{
		t:        t,
		log:      logger.Get("upf.pfcp.handler"),
		sessions: make(map[uint64]*HandlerSession),
		byIMSI:   make(map[imsiPduKey]*HandlerSession),
		mgr:      mgr,
	}
	// §7.2.2.4.2: SEID 0 is reserved. Use a non-zero seed so
	// accidental zero-handling is immediately noticeable.
	h.nextSEID.Store(0x2000_0001)
	t.SetHandler(h.dispatch)
	return h
}

// dispatch is invoked from Transport on every inbound unsolicited
// PDU. Routes by Message Type. Per-handler implementation lives in
// the matching handler_*.go file (see package doc).
func (h *Handler) dispatch(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	switch hdr.MessageType {

	case genpfcp.MessageTypeHeartbeatRequest:
		h.handleHeartbeat(hdr, payload, peer)

	case genpfcp.MessageTypeAssociationSetupRequest:
		h.handleAssociationSetup(hdr, payload, peer)

	case genpfcp.MessageTypeAssociationReleaseRequest:
		// TS 29.244 v19.5.0 §7.4.4.5 — bulk teardown of every PFCP
		// session on the association, per §6.2.8.3.
		h.handleAssociationRelease(hdr, payload, peer)

	case genpfcp.MessageTypeSessionSetDeletionRequest:
		// TS 29.244 v19.5.0 §7.4.6 — multi-session release scoped
		// to one or more §8.2.61 FQ-CSIDs. Without CSID tracking
		// at §7.5.2 the handler returns Cause=1 zero-match
		// (conformant — see TODO inline).
		h.handleSessionSetDeletion(hdr, payload, peer)

	case genpfcp.MessageTypeSessionEstablishmentRequest:
		h.handleSessionEstablishment(hdr, payload, peer)

	case genpfcp.MessageTypeSessionModificationRequest:
		h.handleSessionModification(hdr, payload, peer)

	case genpfcp.MessageTypeSessionDeletionRequest:
		h.handleSessionDeletion(hdr, payload, peer)

	case genpfcp.MessageTypePFDManagementRequest:
		// TS 29.244 §6.2.5 — operator-curated PFD set pushed from CP.
		// UPF stores per Application ID; PDR matching consumes the
		// cache (today: classifier in security/dpi mirrors the same
		// data via the DB; once the C dataplane reads the cache the
		// match becomes wire-driven instead of DB-driven).
		h.handlePFDManagement(hdr, payload, peer)

	default:
		// TS 29.244 §7.2.2.1 — unknown / unsupported type.
		// TODO(spec: TS 29.244 §7.2 Version Not Supported) — on
		//   version mismatch send type=2 response. Today we drop
		//   + log only.
		h.log.Warnf("PFCP unhandled %s (type=%d) from %s — dropped",
			messageTypeName(hdr.MessageType), hdr.MessageType, peer)
	}
}

// traceHandler is the per-handler timing helper. Every handle*()
// opens with `defer h.traceHandler(name, peer)()` which logs the
// elapsed wall time at debug level on return. Cost when debug is
// off: one time.Now() and one closure per message — both negligible
// against the cgo dispatch the handler is about to do. Lets the
// operator narrow "where is PFCP slow" without re-running benchmarks
// — flip nf/upf/pfcp/handler log level to debug, watch the elapsed
// distribution by name, identify the laggard family.
func (h *Handler) traceHandler(name string, peer *net.UDPAddr) func() {
	start := time.Now()
	return func() {
		h.log.Debugf("PFCP %s from %s took %s",
			name, peer, time.Since(start))
	}
}

// sendSessionReject shortcuts a Cause-only error response for the
// §7.5.x session message family. cause per §8.2.1-1.
func (h *Handler) sendSessionReject(peer *net.UDPAddr, msgType uint8,
	seid uint64, seq uint32, cause uint8) {
	switch msgType {
	case genpfcp.MessageTypeSessionEstablishmentResponse:
		resp := &genpfcp.SessionEstablishmentResponse{
			SEID: seid, SequenceNumber: seq,
			NodeID: genpfcp.NodeID{
				Type: 0,
				IPv4: net.ParseIP("127.0.0.1").To4(),
			},
			Cause: genpfcp.Cause{Value: cause},
		}
		if out, err := stripHeader(resp); err == nil {
			_ = h.t.SendResponse(peer, msgType, seid, seq, out)
		}
	case genpfcp.MessageTypeSessionModificationResponse:
		resp := &genpfcp.SessionModificationResponse{
			SEID: seid, SequenceNumber: seq,
			Cause: genpfcp.Cause{Value: cause},
		}
		if out, err := stripHeader(resp); err == nil {
			_ = h.t.SendResponse(peer, msgType, seid, seq, out)
		}
	case genpfcp.MessageTypeSessionDeletionResponse:
		resp := &genpfcp.SessionDeletionResponse{
			SEID: seid, SequenceNumber: seq,
			Cause: genpfcp.Cause{Value: cause},
		}
		if out, err := stripHeader(resp); err == nil {
			_ = h.t.SendResponse(peer, msgType, seid, seq, out)
		}
	}
}

// FindByIMSI returns the session indexed under (imsi, pduSessionID),
// or nil if no §7.5.2 Establishment has been completed for that pair.
// Used by the §7.5.8 Downlink Data Report forwarder in nf/upf/upfloop
// to recover UP-SEID + peer address from the (imsi, pduSessID)
// tuple the UPF C dataplane produces.
func (h *Handler) FindByIMSI(imsi string, pduSessionID uint8) *HandlerSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byIMSI[imsiPduKey{imsi, pduSessionID}]
}

// stripHeader converts a generated message's Encode() output — which
// is a full PFCP PDU (header + IE payload per §7.2.2.1) — into the
// IE-only bytes that Transport wraps with its own header. The
// transport owns §7.2.2.4.1 Sequence Number allocation; the generated
// header is therefore redundant and must be stripped before sending.
func stripHeader(m interface{ Encode() ([]byte, error) }) ([]byte, error) {
	pdu, err := m.Encode()
	if err != nil {
		return nil, err
	}
	_, off, err := runtime.ParseHeader(pdu)
	if err != nil {
		return nil, err
	}
	return pdu[off:], nil
}

// parseDecUint8 is a tiny decimal parser for NAI values we emit.
// Doesn't pull in strconv — the whole path is 1-2 digits in practice.
func parseDecUint8(s string) uint8 {
	var n uint32
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + uint32(c-'0')
		if n > 255 {
			return 0
		}
	}
	return uint8(n)
}

// readOHCGTPUv4 extracts (TEID, IPv4 host-order uint32) from a typed
// runtime.OuterHeaderCreation, ONLY when Description bit 5/1 is set
// (GTP-U/UDP/IPv4 — the only OHC shape this dataplane consumes).
// All other Description combinations return zeros.
func readOHCGTPUv4(o *genpfcp.OuterHeaderCreation) (teid, peerAddr uint32) {
	if o == nil || o.Description&runtime.OHCDescGTPUUDPIPv4 == 0 {
		return 0, 0
	}
	teid = o.TEID
	if v4 := o.IPv4.To4(); v4 != nil {
		peerAddr = uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
	}
	return
}

// formatNodeID renders a Node ID IE (§8.2.38) — FQDN or IPv4/IPv6
// — to a short string for log output.
func formatNodeID(n *genpfcp.NodeID) string {
	if n == nil {
		return "?"
	}
	switch n.Type {
	case 0: // IPv4
		return n.IPv4.String()
	case 1: // IPv6
		return n.IPv6.String()
	case 2: // FQDN
		return n.FQDN
	}
	return "unknown"
}

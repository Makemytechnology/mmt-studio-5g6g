// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package upfclient — SMF-side UPFBridge implementation that reaches
// the UPF over PFCP/N4 (TS 29.244) instead of the in-process cgo
// path. See nf/upf/cgo_bridge.go for the dual-impl seam rationale.
//
// Authoritative specs (PDFs):
//
//	TS 29.244 v19.5.0 — PFCP (wire protocol)
//	  §6.1     UDP/8805 transport
//	  §6.4     Reliable Delivery of PFCP Messages (T1/N1 retransmit)
//	  §7.2.2   Message header + Sequence Number
//	  §7.4.2   Heartbeat
//	  §7.4.4.1 PFCP Association Setup Request
//	  §7.4.4.2 PFCP Association Setup Response
//	  §7.4.4.5 PFCP Association Release Request
//	  §7.5.2   Session Establishment Request
//	  §7.5.4   Session Modification Request
//	  §7.5.6   Session Deletion Request
//	  §7.5.8   Session Report Request (inbound on this side)
//
//	TS 23.501 v19.7.0 §5.8 — Control and User Plane Separation.
//
// Three modes when complete:
//
//	cgo        — co-located, libupf_dp.so direct (today's default)
//	pfcp-loop  — integrated: SMF + UPF in one binary, 127.0.0.1:8805
//	pfcp-dist  — distributed CUPS: UPF at a remote service IP
//
// The last two share this implementation; only the target address
// differs. See nf/upf/cgo_bridge.go dual-impl header.
//
// Implementation status of the 13 UPFBridge methods:
//
//	SessionCreate         ✅ §7.5.2 encode + transport.SendRequest
//	SessionDelete         ✅ §7.5.6 encode + transport.SendRequest
//	UpdateFAR             ✅ §7.5.4 with Update FAR IE §7.5.4.3
//	DeactivateDLFAR       ✅ §7.5.4 with Update FAR Apply Action=BUFF
//	others (Add*, Set*)   ⚠️ no-op at pfcp boundary — nested in §7.5.2
//	                          Create PDR/FAR/URR/QER IEs of the
//	                          Establishment Request built by
//	                          SessionCreate. AddPDR/AddFAR when
//	                          called post-create translate to §7.5.4
//	                          Update* IEs. Scaffold: see TODOs.
//	Init / Cleanup /
//	  SetMaxSessions /
//	  SetPMDTuning        — UPF-local; no wire IE; no-op.
//	PktIO* / Register*    — UPF-local; no-op.
//	GetURRStats /
//	  GetQERStats         — sync query (§7.5.4.10 Query URR +
//	                        §7.5.5.2 Usage Report response).
//	                        ⚠️ Not yet implemented.
//	GetIOStats /
//	  SessionCount        — not on the wire; zero.
//	Slice*                — UPF-local.
//	DrainReports /
//	  ReportsDropped      — §7.5.8 inbound — parked (0) until
//	                        handler receives the report PDUs.
package upfclient

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"

	"github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/nf/upf/pfcp"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// stripHeader converts a generated message's Encode() output — which
// is a full PFCP PDU (header + IE payload per §7.2.2.1) — into the
// IE-only bytes that pfcp.Transport wraps with its own header. The
// transport owns §7.2.2.4.1 Sequence Number allocation, so the
// generated header's seq field is ignored; we only need the IEs.
func stripHeader(m interface{ Encode() ([]byte, error) }) ([]byte, error) {
	pdu, err := m.Encode()
	if err != nil {
		return nil, err
	}
	_, off, err := runtime.ParseHeader(pdu)
	if err != nil {
		return nil, fmt.Errorf("stripHeader: parse generated PDU: %w", err)
	}
	return pdu[off:], nil
}

// ntpEpochOffset is the seconds between NTP epoch (1900-01-01) and
// Unix epoch (1970-01-01). TS 29.244 §8.2.3 Recovery Time Stamp is
// NTP-format.
const ntpEpochOffset = 2208988800

// processStartNTP snapshots the bridge's start time in NTP format
// once. The peer uses Recovery Time Stamp stability to detect our
// restart (value changes = we restarted). Exported through
// processStartNTPValue so tests can override.
var processStartNTPValue = uint32(time.Now().Unix()) + ntpEpochOffset

func processStartNTP() uint32 { return processStartNTPValue }

// ErrNotImplemented is returned by methods whose PFCP encoding is
// still pending. Scaffolding errors rather than panics so the
// runtime binding surfaces gaps clearly.
var ErrNotImplemented = errors.New("upfclient/pfcp_bridge: not yet implemented (see TODO anchors)")

// PfcpBridge implements upf.UPFBridge over PFCP wire. One instance
// per target UPF (address + transport + session state). Thread-safe:
// the underlying pfcp.Transport serialises sends and correlates
// responses by Sequence Number.
type PfcpBridge struct {
	t      *pfcp.Transport
	remote *net.UDPAddr
	log    *logger.Logger

	// SEID allocator — the CP-side keeps its own SEID namespace per
	// §7.2.2.4.2 "Session Endpoint Identifier" (verbatim): "The SEID
	// is allocated by each UP function and sent to the CP function
	// in the F-SEID IE in the PFCP Session Establishment Response."
	// The CP allocates its own SEID for its side of the association
	// and includes it in §8.2.37 F-SEID of the Establishment Request.
	cpSeid uint64

	// (imsi, pduSessID) → allocated CP-SEID / peer's UP-SEID.
	// SessionCreate inserts; SessionDelete looks up + removes.
	mu       sync.Mutex
	sessions map[sessionKey]*sessionState

	// pending captures AddPDR/AddFAR/AddQER/AddURR/SetSessionAMBR
	// calls issued BEFORE SessionCreate. They are flushed into
	// the §7.5.2 Establishment Request's Create-* IE lists at
	// SessionCreate time. Post-SessionCreate calls go out as §7.5.4
	// Modification with Create-* IEs (addOrModify).
	pending map[sessionKey]*pendingRules

	// §7.5.8 Session Report Request inbound ring — populated by the
	// Transport handler below, drained by DrainReports. Buffered so
	// a slow consumer can't stall the Transport readLoop; overflows
	// increment reportsDropped for observability.
	reports        chan upf.Report
	reportsDropped atomic.Uint64

	// cpSEIDIndex maps CP-SEID → sessionKey so the §7.5.8 inbound
	// handler can route a Session Report Request back to the SMF's
	// (imsi, pduSessID) tuple. Per TS 29.244 §7.2.2.4.2, the
	// destination SEID in a UPF→SMF message is the CP-SEID (the
	// peer's SEID — here, ours). Kept in sync with p.sessions.
	cpSEIDIndex map[uint64]sessionKey

	// statsPeer is an in-process shortcut to the UPF-side dataplane
	// bridge (typically the cgo dpdkBridge) used ONLY for telemetry
	// reads — GetIOStats / GetURRStats / GetQERStats. Wired by
	// upfloop.Enable when CP and UP run in the same binary so the
	// GUI gets live counters without a §7.5.4 Modification + Query
	// URR round-trip per refresh.
	//
	// In a real CUPS split deployment this stays nil and the proper
	// PFCP path applies (TS 29.244 §7.5.4.10 Query URR + §7.5.5.2
	// Usage Report). The PFCP-wire control flow (§7.5.2/§7.5.4/
	// §7.5.6) is unaffected either way.
	statsPeer upf.UPFBridge
}

// pendingRules stashes what the Manager issued per session before
// SessionCreate so PfcpBridge can emit the §7.5.2 Establishment
// Request with a full IE body. See TS 29.244 §7.5.2.2 for the
// grouped IEs these map into.
type pendingRules struct {
	pdrs       []pendingPDR
	fars       []pendingFAR
	qers       []pendingQER
	urrs       []pendingURR
	sessAMBRUL uint64
	sessAMBRDL uint64
	ueAMBRUL   uint64
	ueAMBRDL   uint64

	// Session-create metadata stashed by SessionCreate, drained by
	// CommitSession into the §7.5.2 Establishment Request. Empty
	// means SessionCreate hasn't been called yet for this session.
	hasCreate bool
	cpSEID    uint64
	dnn       string
	sst       uint8
	sd        uint32
	ueAddr    uint32
	pdnType   uint8 // §8.2.79: 1=IPv4, 2=IPv6, 3=IPv4v6, 4=Non-IP, 5=Ethernet
}

type pendingPDR struct {
	pdrID      uint16
	precedence uint32
	pdiSource  uint8
	qfi        uint8
	farID      uint32
	qerID      uint32
	urrID      uint32
	sdfRules   string

	// PDI fast-path keys carried in §7.5.2.2 PDI:
	//   ueIPv4 — DL match, encoded as §8.2.62 UE IP Address
	//   teid + n3IPv4 — UL match, encoded as §8.2.3 F-TEID
	// Zero means "no key" (SDF-only matching).
	ueIPv4 uint32
	teid   uint32
	n3IPv4 uint32
}

type pendingFAR struct {
	farID    uint32
	action   uint8 // §8.2.26 bitmap: FORW=0x02, BUFF=0x04, DROP=0x01, NOCP=0x08, DUPL=0x10
	dstIface uint8 // §8.2.24 Destination Interface
	teid     uint32
	peerAddr uint32
	peerPort uint16
	ohcType  uint8 // 1=GTP-U/UDP/IPv4, 2=GTP-U/UDP/IPv6 etc.
}

type pendingQER struct {
	qerID          uint32
	qfi            uint8
	gateUL, gateDL uint8
	mbrUL, mbrDL   uint64
	gbrUL, gbrDL   uint64
	// dscp is the 6-bit Differentiated Services codepoint (RFC 2474)
	// applied to the DL flow when set non-zero. Encoded into the QER
	// as a DLFlowLevelMarking IE (TS 29.244 §8.2.41) so the UPF marks
	// the outer IP ToS / Traffic Class on egress. Sourced from the
	// detected app's qos_profile (TS 23.501 §5.8.2 traffic detection).
	dscp uint8
}

type pendingURR struct {
	urrID                     uint32
	measMethod, reportTrigger uint8
	volThreshUL, volThreshDL  uint64
	timeThresh                uint32
}

type sessionKey struct {
	imsi         string
	pduSessionID uint8
}

type sessionState struct {
	cpSEID uint64 // this bridge allocated
	upSEID uint64 // peer (UPF) allocated — from §7.5.3 Response
}

// Dial connects to a remote UPF at addr (e.g. "127.0.0.1:8805" for
// integrated, "10.0.0.10:8805" for distributed). Performs §7.3.4
// Association Setup before returning so subsequent session ops are
// valid per spec.
func Dial(addr string) (*PfcpBridge, error) {
	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("upfclient: resolve %q: %w", addr, err)
	}
	// Client-side transport: bind ephemeral port on loopback (or
	// whatever default route's local IP for distributed).
	tport, err := pfcp.NewTransport("0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("upfclient: transport: %w", err)
	}
	b := &PfcpBridge{
		t:           tport,
		remote:      remote,
		log:         logger.Get("smf.upfclient.pfcp"),
		cpSeid:      0x1000_0000, // non-zero seed per §7.2.2.4.2
		sessions:    make(map[sessionKey]*sessionState),
		pending:     make(map[sessionKey]*pendingRules),
		cpSEIDIndex: make(map[uint64]sessionKey),
		// 1024-entry ring is ample for typical paging bursts (§7.5.8
		// is rare — one per idle UE DL arrival). Overflow → drop +
		// increment counter rather than back-pressure the transport.
		reports: make(chan upf.Report, 1024),
	}
	// Install the inbound §7.5.8 / other unsolicited-PDU handler.
	tport.SetHandler(b.handleInbound)

	// §7.3.4 Association Setup — CP-initiated per §7.3.4.1.
	// Mandatory Request IEs: Node ID (§8.2.38), Recovery Time
	// Stamp (§8.2.3).
	// TODO(spec: TS 29.244 §8.2.65 Recovery Time Stamp
	//   persistence) — use a stable process-start timestamp so
	//   the peer can detect our restart via §8.2.3.
	// TODO(spec: TS 29.244 §7.6.3 Heartbeat goroutine) — spawn
	//   periodic §7.4.2 Heartbeat Request, detect peer restart
	//   via Recovery Time Stamp change.
	if err := b.setupAssociation(); err != nil {
		_ = tport.Close()
		return nil, fmt.Errorf("upfclient: association setup: %w", err)
	}
	return b, nil
}

// Close tears down the transport. Idempotent.
//
// Graceful path per TS 29.244 v19.5.0 §7.4.4.5 PFCP Association
// Release Request: send the Release Request to the UPF and wait for
// the §7.4.4.6 Response (Cause + NodeID) before dropping the UDP
// socket. Per §6.2.8.3 the UP function "shall delete all the PFCP
// sessions related to that PFCP association locally" on receipt —
// so this single PDU is the spec-correct way to release every session
// the SMF holds with this UPF in one wire exchange.
//
// Best-effort: any failure on the request path (transport closed,
// timeout, peer not responding) just falls through to the local
// transport close. The UPF will eventually time out the association
// on heartbeat loss anyway (§7.4.2 / §7.6.3).
func (p *PfcpBridge) Close() error {
	if p.t == nil {
		return nil
	}
	if err := p.releaseAssociation(); err != nil {
		// Non-fatal — log and continue closing the socket.
		p.log.Warnf("PFCP Association Release graceful path failed: %v — closing transport anyway", err)
	}
	return p.t.Close()
}

// releaseAssociation emits the §7.4.4.5 Release Request and waits for
// the §7.4.4.6 Response. Mirrors setupAssociation's wire pattern.
// Mandatory IE per §7.4.4.5: NodeID (§8.2.38).
func (p *PfcpBridge) releaseAssociation() error {
	req := &genpfcp.AssociationReleaseRequest{
		NodeID: genpfcp.NodeID{
			Type: 0, // §8.2.38 IPv4
			IPv4: p.localNodeIPv4(),
		},
	}
	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("encode release req: %w", err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeAssociationReleaseRequest, 0, payload))
	if err != nil {
		return err
	}
	var resp genpfcp.AssociationReleaseResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("decode release resp: %w", err)
	}
	p.log.Infof("PFCP Association Released with %s cause=%d (TS 29.244 §7.4.4.5 / §7.4.4.6)",
		p.remote, resp.Cause.Value)
	return nil
}

// setupAssociation drives the §7.3.4 Setup handshake. Minimal IEs
// (Node ID + Recovery Time Stamp) — the peer's Response Cause and
// advertised UP Function Features (§8.2.25) are parsed but not yet
// acted upon (see TODOs).
func (p *PfcpBridge) setupAssociation() error {
	req := &genpfcp.AssociationSetupRequest{
		NodeID: genpfcp.NodeID{
			Type: 0, // §8.2.38 IPv4
			IPv4: p.localNodeIPv4(),
		},
		RecoveryTimeStamp: genpfcp.RecoveryTimeStamp{
			Value: processStartNTP(),
		},
	}
	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("encode setup req: %w", err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeAssociationSetupRequest, 0, payload))
	if err != nil {
		return err
	}
	var resp genpfcp.AssociationSetupResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("decode setup resp: %w", err)
	}
	if resp.Cause.Value != 1 {
		return fmt.Errorf("association setup rejected: cause=%d (§8.2.1)", resp.Cause.Value)
	}
	p.log.Infof("PFCP Association established with %s cause=%d (§7.3.4)",
		p.remote, resp.Cause.Value)
	// TODO(spec: TS 29.244 §8.2.25 UP Function Features) —
	//   inspect resp.UPFunctionFeatures bits and adapt behaviour
	//   (BUCP = UPF can buffer for DLDR; EMPU = Ethernet PDU
	//   sessions supported; …).
	return nil
}

// ── upf.UPFBridge implementation ─────────────────────────────────

// Init / Cleanup — UPF-local EAL init, not on the wire.
func (p *PfcpBridge) Init(argc int, argv []string) error { return nil }
func (p *PfcpBridge) Cleanup()                           { _ = p.Close() }

// SetMaxSessions / SetPMDTuning — UPF-local tuning, not on the wire.
func (p *PfcpBridge) SetMaxSessions(n uint32) error { return nil }
func (p *PfcpBridge) SetPMDTuning(mbufPoolSize uint32, rxRingSize, txRingSize uint16) error {
	return nil
}

// SessionCreate stashes the session metadata (CP-SEID, DNN, S-NSSAI,
// UE address) into pendingRules. Callers then issue
// AddPDR/AddFAR/AddQER/AddURR/SetSessionAMBR which append to the
// same pending bucket. CommitSession finally builds and sends ONE
// §7.5.2 Establishment Request carrying every Create-* IE.
//
// Rationale: the previous "send empty Establishment then a
// Modification per rule" pattern produced 8+ PFCP messages per PDU
// session. Spec §7.5.2.2 groups Create-PDR/FAR/QER/URR into a
// single Establishment exactly to avoid this.
func (p *PfcpBridge) SessionCreate(imsi string, pduSessionID uint8,
	dnn string, sst uint8, sd, ueAddr uint32, pdnType uint8) error {

	cpSEID := p.nextCPSEID()

	key := sessionKey{imsi, pduSessionID}
	p.mu.Lock()
	r := p.pending[key]
	if r == nil {
		r = &pendingRules{}
		p.pending[key] = r
	}
	r.hasCreate = true
	r.cpSEID = cpSEID
	r.dnn = dnn
	r.sst = sst
	r.sd = sd
	r.ueAddr = ueAddr
	r.pdnType = pdnType
	p.mu.Unlock()
	return nil
}

// CommitSession sends the single §7.5.2 Establishment Request with
// every queued Create-* IE. Per §7.5.2.2 the Establishment carries
// (mandatory):
//
//	NodeID                         — §8.2.38 CP identity
//	CP F-SEID                      — §8.2.37 CP-allocated SEID + CP IP
//
// Plus (optional, populated from pendingRules):
//
//	CreatePDR / CreateFAR / CreateQER / CreateURR  — §7.5.2.2-5
//	UserID                         — §8.2.101 SUPI + NAI
//
// On success p.sessions[key] is populated with the UP-allocated SEID
// for subsequent post-establishment Modification / Update / Release
// to address.
func (p *PfcpBridge) CommitSession(imsi string, pduSessionID uint8) error {
	key := sessionKey{imsi, pduSessionID}
	p.mu.Lock()
	pend := p.pending[key]
	delete(p.pending, key)
	p.mu.Unlock()
	if pend == nil || !pend.hasCreate {
		return fmt.Errorf("CommitSession: no SessionCreate for imsi=%s pduSessID=%d", imsi, pduSessionID)
	}

	req := &genpfcp.SessionEstablishmentRequest{
		SEID:           0, // §7.5.2.1: Establishment carries SEID=0
		SequenceNumber: 0, // Transport overrides in encodedMessage
		NodeID: genpfcp.NodeID{
			Type: 0, IPv4: p.localNodeIPv4(),
		},
		CPFSEID: genpfcp.FSEID{
			SEID: pend.cpSEID,
			IPv4: p.localNodeIPv4(),
		},
		// §8.2.101 User ID: SUPI=IMSI + NAI=pduSessionID (decimal).
		// Lets the UPF log per-UE and key its session table on the
		// SMF's (imsi, pduSessID) — not a synthetic UP-SEID hash.
		UserID: &genpfcp.UserID{
			SUPI: imsi,
			NAI:  fmt.Sprintf("%d", pduSessionID),
		},
	}
	// §8.2.79 PDN Type — §7.5.2 NOTE: "PDN Type IE shall be set if
	// the PDU Session is for IP traffic." 0=unset (skipped).
	if pend.pdnType != 0 {
		req.PDNType = &genpfcp.PDNType{Value: pend.pdnType}
	}
	// §8.2.117 APN/DNN — optional but useful for ops. Encoding
	// follows TS 23.003 §9.1 (DNS-label format) — runtime.APNDNN
	// owns the wire conversion.
	if pend.dnn != "" {
		req.APNDNN = &genpfcp.APNDNN{Value: pend.dnn}
	}
	// §8.2.144 S-NSSAI (IE type 231) — TS 29.244 v19.5.0:
	//   octet 5     SST  (1 octet, bin)
	//   octet 6..8  SD   (3 octets, big-endian; 0xFFFFFF = unset)
	// Populated from the pendingRules SST/SD stashed by SessionCreate
	// so the UPF anchor can scope per-slice policy / accounting per
	// TS 23.501 §5.15. Skipped when SST==0 (no slice carried at NAS).
	if pend.sst != 0 {
		sd := pend.sd
		if sd == 0 {
			sd = 0xFFFFFF
		}
		req.SNSSAI = &genpfcp.SNSSAI{
			Value: []byte{
				pend.sst,
				byte(sd >> 16), byte(sd >> 8), byte(sd),
			},
		}
	}
	for _, r := range pend.pdrs {
		req.CreatePDR = append(req.CreatePDR, buildCreatePDR(r))
	}
	for _, r := range pend.fars {
		req.CreateFAR = append(req.CreateFAR, buildCreateFAR(r))
	}
	for _, r := range pend.qers {
		req.CreateQER = append(req.CreateQER, buildCreateQER(r))
	}
	for _, r := range pend.urrs {
		req.CreateURR = append(req.CreateURR, buildCreateURR(r))
	}
	// Session-AMBR per TS 23.501 §5.7.2.6 rides an extra session-
	// scope QER (qerID=0xFFFFFFFE — reserved session slot). Emit
	// only when non-zero so tests without AMBR aren't disturbed.
	if pend.sessAMBRUL != 0 || pend.sessAMBRDL != 0 {
		req.CreateQER = append(req.CreateQER, buildCreateQER(pendingQER{
			qerID: 0xFFFFFFFE,
			mbrUL: pend.sessAMBRUL, mbrDL: pend.sessAMBRDL,
		}))
	}
	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("CommitSession encode: %w", err)
	}

	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeSessionEstablishmentRequest, 0, payload))
	if err != nil {
		return fmt.Errorf("CommitSession send: %w", err)
	}
	var resp genpfcp.SessionEstablishmentResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("CommitSession response decode: %w", err)
	}
	if resp.Cause.Value != 1 {
		return fmt.Errorf("CommitSession rejected: cause=%d (§8.2.1, §7.5.3)", resp.Cause.Value)
	}

	upSEID := uint64(0)
	if resp.UPFSEID != nil {
		upSEID = resp.UPFSEID.SEID
	}

	p.mu.Lock()
	p.sessions[key] = &sessionState{cpSEID: pend.cpSEID, upSEID: upSEID}
	p.cpSEIDIndex[pend.cpSEID] = key
	p.mu.Unlock()

	p.log.WithIMSI(imsi).Infof("PFCP Session Established pduSessID=%d CP-SEID=%#x UP-SEID=%#x rules: %d PDR / %d FAR / %d QER / %d URR (§7.5.2)",
		pduSessionID, pend.cpSEID, upSEID,
		len(pend.pdrs), len(pend.fars), len(req.CreateQER), len(pend.urrs))
	return nil
}

// SessionDelete implements TS 29.244 §7.5.6 PFCP Session Deletion
// Request. Target SEID in the header is the UP-SEID the peer
// returned on Establishment.
func (p *PfcpBridge) SessionDelete(imsi string, pduSessionID uint8) error {
	p.mu.Lock()
	s, ok := p.sessions[sessionKey{imsi, pduSessionID}]
	if ok {
		delete(p.sessions, sessionKey{imsi, pduSessionID})
		delete(p.cpSEIDIndex, s.cpSEID)
	}
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("SessionDelete: no session for imsi=%s pduSessID=%d", imsi, pduSessionID)
	}

	// TS 29.244 §7.5.6: "…sent … by the CP function to the UP
	// function to delete an existing PFCP session at the UP
	// function." No IEs mandatory on the CP→UP direction beyond
	// the header SEID.
	req := &genpfcp.SessionDeletionRequest{
		SEID: s.upSEID, // header SEID = peer's SEID (UP-SEID)
	}
	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("SessionDelete encode: %w", err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeSessionDeletionRequest, s.upSEID, payload))
	if err != nil {
		return fmt.Errorf("SessionDelete send: %w", err)
	}
	var resp genpfcp.SessionDeletionResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("SessionDelete response decode: %w", err)
	}
	if resp.Cause.Value != 1 {
		return fmt.Errorf("SessionDelete rejected: cause=%d (§7.5.7)", resp.Cause.Value)
	}

	// TODO(spec: TS 29.244 §7.5.7.2 Usage Report IE in Session
	//   Deletion Response) — when resp.UsageReport is populated,
	//   surface the final per-URR usage to the charging pipeline
	//   (Nchf_ConvergentCharging, TS 32.255). Today this loses
	//   the final reporting window.
	_ = resp.UsageReport

	p.log.WithIMSI(imsi).Infof("PFCP Session Deleted pduSessID=%d UP-SEID=%#x (§7.5.6)",
		pduSessionID, s.upSEID)
	return nil
}

// UpdateFAR implements TS 29.244 §7.5.4 PFCP Session Modification
// Request with an Update FAR IE (§7.5.4.3).
func (p *PfcpBridge) UpdateFAR(imsi string, pduSessionID uint8,
	farID, teid, peerAddr uint32, peerPort uint16) error {
	p.mu.Lock()
	s, ok := p.sessions[sessionKey{imsi, pduSessionID}]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("UpdateFAR: no session for imsi=%s pduSessID=%d", imsi, pduSessionID)
	}

	// TS 29.244 §7.5.4.3 Update FAR IE:
	//   FAR ID (mandatory, §8.2.18)
	//   Apply Action (optional, §8.2.26) — FORW=1, BUFF=2, DROP=0x04
	//   Update Forwarding Parameters (optional, §8.2.4)
	//     Destination Interface (§8.2.24), Outer Header Creation
	//     (§8.2.56 — new TEID + peer IP for N3 tunnel reactivation)
	aa := genpfcp.ApplyAction{FORW: 1}
	ufp := &genpfcp.UpdateForwardingParameters{
		DestinationInterface: &genpfcp.DestinationInterface{Value: 0}, // Access
	}
	if teid != 0 || peerAddr != 0 {
		ufp.OuterHeaderCreation = ohcGTPUv4(teid, peerAddr, peerPort)
	}
	req := &genpfcp.SessionModificationRequest{
		SEID: s.upSEID,
		UpdateFAR: []genpfcp.UpdateFAR{{
			FARID:                      genpfcp.FARID{Value: farID},
			ApplyAction:                &aa,
			UpdateForwardingParameters: ufp,
		}},
	}

	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("UpdateFAR encode: %w", err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeSessionModificationRequest, s.upSEID, payload))
	if err != nil {
		return fmt.Errorf("UpdateFAR send: %w", err)
	}
	var resp genpfcp.SessionModificationResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("UpdateFAR response decode: %w", err)
	}
	if resp.Cause.Value != 1 {
		return fmt.Errorf("UpdateFAR rejected: cause=%d (§7.5.5)", resp.Cause.Value)
	}
	p.log.WithIMSI(imsi).Infof("PFCP FAR updated pduSessID=%d farID=%d teid=%#x peer=%#x UP-SEID=%#x (§7.5.4.3 Update FAR)",
		pduSessionID, farID, teid, peerAddr, s.upSEID)
	return nil
}

// DeactivateDLFAR sends §7.5.4 Modification with an Update FAR
// carrying Apply Action=BUFF (§8.2.26 BUFF bit). Used by AN-Release
// (TS 23.502 §4.2.6 step 6a) to park DL traffic at the UPF until
// the UE is reactivated by a Service Request (handled by UpdateFAR
// with a fresh TEID/peer).
// UpdatePDR / UpdateQER / UpdateURR — TS 29.244 v19.5.0 §7.5.4.2/.4/.5.
// PfcpBridge talks N4 to a separate UPF; a real wire-side implementation
// would build §7.5.4 Modification with Update * IEs. Today the SMF
// modification path is scaffolded only — these methods are no-ops so
// the integrated-PFCP loopback path doesn't error out. SMF-side
// wire-side modification remains a TODO.
func (p *PfcpBridge) UpdatePDR(imsi string, pduSessionID uint8, pdrID uint16,
	precedence uint32, pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
	ueIPv4, teid, n3IPv4 uint32) error {
	return nil
}
func (p *PfcpBridge) UpdateQER(imsi string, pduSessionID uint8, qerID uint32,
	qfi, gateUL, gateDL uint8, mbrUL, mbrDL, gbrUL, gbrDL uint64) error {
	return nil
}
func (p *PfcpBridge) UpdateURR(imsi string, pduSessionID uint8, urrID uint32,
	measMethod, reportTrigger uint8, volThreshUL, volThreshDL uint64, timeThresh uint32) error {
	return nil
}

// Remove* — TS 29.244 v19.5.0 §7.5.4.6/.7/.8/.9 Remove PDR/FAR/URR/QER.
// PfcpBridge talks N4 to a separate UPF; a fully-formed Remove * IE
// would normally ride a §7.5.4 Modification Request on the wire. The
// SMF-side modification path here is currently scaffolded only —
// these methods are no-ops so the local dispatch from upfloop /
// integrated-PFCP doesn't error out. Real wire-side modification
// remains a TODO for the SMF Modification path.
func (p *PfcpBridge) RemovePDR(imsi string, pduSessionID uint8, pdrID uint16) error {
	return nil
}
func (p *PfcpBridge) RemoveFAR(imsi string, pduSessionID uint8, farID uint32) error {
	return nil
}
func (p *PfcpBridge) RemoveQER(imsi string, pduSessionID uint8, qerID uint32) error {
	return nil
}
func (p *PfcpBridge) RemoveURR(imsi string, pduSessionID uint8, urrID uint32) error {
	return nil
}

func (p *PfcpBridge) DeactivateDLFAR(imsi string, pduSessionID uint8, farID uint32) error {
	p.mu.Lock()
	s, ok := p.sessions[sessionKey{imsi, pduSessionID}]
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("DeactivateDLFAR: no session for imsi=%s pduSessID=%d", imsi, pduSessionID)
	}

	aa := genpfcp.ApplyAction{BUFF: 1}
	req := &genpfcp.SessionModificationRequest{
		SEID: s.upSEID,
		UpdateFAR: []genpfcp.UpdateFAR{{
			FARID:       genpfcp.FARID{Value: farID},
			ApplyAction: &aa,
			// No UpdateForwardingParameters — we're deactivating,
			// not reassigning the tunnel.
		}},
	}
	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("DeactivateDLFAR encode: %w", err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeSessionModificationRequest, s.upSEID, payload))
	if err != nil {
		return fmt.Errorf("DeactivateDLFAR send: %w", err)
	}
	var resp genpfcp.SessionModificationResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("DeactivateDLFAR response decode: %w", err)
	}
	if resp.Cause.Value != 1 {
		return fmt.Errorf("DeactivateDLFAR rejected: cause=%d (§7.5.5)", resp.Cause.Value)
	}
	p.log.WithIMSI(imsi).Infof("PFCP FAR buffered (Apply Action=BUFF) pduSessID=%d farID=%d UP-SEID=%#x (§7.5.4, §8.2.26)",
		pduSessionID, farID, s.upSEID)
	return nil
}

// AddPDR / AddFAR / AddURR / AddQER either batch (pre-SessionCreate,
// flushed into the §7.5.2 Establishment CreatePDR/FAR/URR/QER lists)
// or emit a §7.5.4 Session Modification carrying a single Create-*
// IE (post-SessionCreate — spec-legal per TS 29.244 §7.5.4.2, which
// allows Create-* in Modification for rules added after Establishment).
// SMF's installUPFRules uses the post-create pattern; the batch path
// remains for future callers that provision rules up front.
func (p *PfcpBridge) AddPDR(imsi string, pduSessionID uint8, pdrID uint16,
	precedence uint32, pdiSource, qfi uint8, farID, qerID, urrID uint32,
	sdfRules string, ueIPv4, teid, n3IPv4 uint32) error {
	rec := pendingPDR{
		pdrID: pdrID, precedence: precedence, pdiSource: pdiSource,
		qfi: qfi, farID: farID, qerID: qerID, urrID: urrID,
		sdfRules: sdfRules,
		ueIPv4:  ueIPv4,
		teid:    teid,
		n3IPv4:  n3IPv4,
	}
	return p.addOrModify(imsi, pduSessionID,
		func(r *pendingRules) { r.pdrs = append(r.pdrs, rec) },
		func(req *genpfcp.SessionModificationRequest) {
			req.CreatePDR = append(req.CreatePDR, buildCreatePDR(rec))
		},
		fmt.Sprintf("PDR-%d", pdrID),
	)
}
func (p *PfcpBridge) AddFAR(imsi string, pduSessionID uint8, farID uint32,
	action, dstIface uint8, teid, peerAddr uint32, peerPort uint16,
	ohcType uint8) error {
	rec := pendingFAR{
		farID: farID, action: action, dstIface: dstIface,
		teid: teid, peerAddr: peerAddr, peerPort: peerPort,
		ohcType: ohcType,
	}
	return p.addOrModify(imsi, pduSessionID,
		func(r *pendingRules) { r.fars = append(r.fars, rec) },
		func(req *genpfcp.SessionModificationRequest) {
			req.CreateFAR = append(req.CreateFAR, buildCreateFAR(rec))
		},
		fmt.Sprintf("FAR-%d", farID),
	)
}
func (p *PfcpBridge) AddQER(imsi string, pduSessionID uint8, qerID uint32,
	qfi, gateUL, gateDL uint8, mbrUL, mbrDL, gbrUL, gbrDL uint64) error {
	rec := pendingQER{
		qerID: qerID, qfi: qfi, gateUL: gateUL, gateDL: gateDL,
		mbrUL: mbrUL, mbrDL: mbrDL, gbrUL: gbrUL, gbrDL: gbrDL,
	}
	return p.addOrModify(imsi, pduSessionID,
		func(r *pendingRules) { r.qers = append(r.qers, rec) },
		func(req *genpfcp.SessionModificationRequest) {
			req.CreateQER = append(req.CreateQER, buildCreateQER(rec))
		},
		fmt.Sprintf("QER-%d", qerID),
	)
}
func (p *PfcpBridge) AddURR(imsi string, pduSessionID uint8, urrID uint32,
	measMethod, reportTrigger uint8, volThreshUL, volThreshDL uint64,
	timeThresh uint32) error {
	rec := pendingURR{
		urrID: urrID, measMethod: measMethod, reportTrigger: reportTrigger,
		volThreshUL: volThreshUL, volThreshDL: volThreshDL,
		timeThresh: timeThresh,
	}
	return p.addOrModify(imsi, pduSessionID,
		func(r *pendingRules) { r.urrs = append(r.urrs, rec) },
		func(req *genpfcp.SessionModificationRequest) {
			req.CreateURR = append(req.CreateURR, buildCreateURR(rec))
		},
		fmt.Sprintf("URR-%d", urrID),
	)
}

// addOrModify is the common path for AddPDR/AddFAR/AddQER/AddURR:
//
//	pre-SessionCreate  → stash into the pending-rules bucket via
//	                     batch (flushed in SessionCreate)
//	post-SessionCreate → send a §7.5.4 Modification with exactly
//	                     one Create-* IE built by `mod`
//
// `label` is a short tag ("FAR-2", "PDR-1", etc.) for log lines.
func (p *PfcpBridge) addOrModify(imsi string, pduSessionID uint8,
	batch func(*pendingRules),
	mod func(*genpfcp.SessionModificationRequest),
	label string,
) error {
	key := sessionKey{imsi, pduSessionID}
	p.mu.Lock()
	s, established := p.sessions[key]
	if !established {
		r, ok := p.pending[key]
		if !ok {
			r = &pendingRules{}
			p.pending[key] = r
		}
		batch(r)
		p.mu.Unlock()
		return nil
	}
	upSEID := s.upSEID
	p.mu.Unlock()

	req := &genpfcp.SessionModificationRequest{SEID: upSEID}
	mod(req)
	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("upfclient: Add %s encode: %w", label, err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeSessionModificationRequest, upSEID, payload))
	if err != nil {
		return fmt.Errorf("upfclient: Add %s send: %w", label, err)
	}
	var resp genpfcp.SessionModificationResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("upfclient: Add %s response decode: %w", label, err)
	}
	if resp.Cause.Value != 1 {
		return fmt.Errorf("upfclient: Add %s rejected: cause=%d (§7.5.5)",
			label, resp.Cause.Value)
	}
	p.log.WithIMSI(imsi).Infof("PFCP rule installed post-create pduSessID=%d %s UP-SEID=%#x (§7.5.4 Create-* in Modification)",
		pduSessionID, label, upSEID)
	return nil
}

// ApplyModifyBatch coalesces a set of post-establishment changes
// (Create-* / Update-* / Remove-*) into a SINGLE TS 29.244 §7.5.4
// Session Modification Request on the wire. Replaces N sequential
// per-IE round-trips with one — the PCF→SMF UpdateNotify path that
// installs (QER, FAR) per new media flow drops from 2 round-trips
// per flow to 1 total per Modification.
//
// Pre-establishment behaviour: until CommitSession has run, IEs in
// the batch fold into the pending Establishment buffer instead of
// the wire. This matches AddPDR/AddFAR/AddQER/AddURR's invariant —
// see addOrModify above for the per-call analogue.
//
// Spec invariants preserved:
//   * §7.5.4.2 IE list cardinality (each list is independent 0..N)
//   * §7.5.4.3 Update FAR Apply Action mapping (peerAddr!=0 → FORW
//                  with new §8.2.56 OHC; peerAddr==0 → BUFF)
//   * §7.5.4.7-9 Remove PDR/FAR/QER/URR (ID-only; idempotent)
//   * §8.2.28 Session-AMBR via reserved QER 0xFFFFFFFE
//   * Cause IE: one per Modification Response — propagated to
//     the caller as an error if cause != 1 (Request accepted).
func (p *PfcpBridge) ApplyModifyBatch(imsi string, pduSessionID uint8,
	batch upf.ModifyBatch) error {
	if batch.IsEmpty() {
		return nil
	}

	key := sessionKey{imsi, pduSessionID}
	p.mu.Lock()
	s, established := p.sessions[key]
	if !established {
		// Session still pre-Establishment — fold the Create-* IEs
		// into the pending bucket so CommitSession picks them up.
		// Update-/Remove- entries are senseless on a not-yet-created
		// session, so drop them with a debug log (matches what the
		// per-call paths do silently).
		r, ok := p.pending[key]
		if !ok {
			r = &pendingRules{}
			p.pending[key] = r
		}
		for _, x := range batch.CreatePDRs {
			r.pdrs = append(r.pdrs, pendingPDR{
				pdrID: x.PDRID, precedence: x.Precedence,
				pdiSource: x.PDISource, qfi: x.QFI,
				farID: x.FARID, qerID: x.QERID, urrID: x.URRID,
				sdfRules: x.SDFRules,
				ueIPv4:   x.UEIPv4, teid: x.TEID, n3IPv4: x.N3IPv4,
			})
		}
		for _, x := range batch.CreateFARs {
			r.fars = append(r.fars, pendingFAR{
				farID: x.FARID, action: x.Action, dstIface: x.DstIface,
				teid: x.TEID, peerAddr: x.PeerAddr, peerPort: x.PeerPort,
			})
		}
		for _, x := range batch.CreateQERs {
			r.qers = append(r.qers, pendingQER{
				qerID: x.QERID, qfi: x.QFI,
				gateUL: x.GateUL, gateDL: x.GateDL,
				mbrUL: x.MBRUL, mbrDL: x.MBRDL,
				gbrUL: x.GBRUL, gbrDL: x.GBRDL,
			})
		}
		for _, x := range batch.CreateURRs {
			r.urrs = append(r.urrs, pendingURR{
				urrID:      x.URRID,
				measMethod: x.MeasMethod, reportTrigger: x.ReportTrigger,
				volThreshUL: x.VolThreshUL, volThreshDL: x.VolThreshDL,
				timeThresh: x.TimeThresh,
			})
		}
		if batch.SessionAMBR != nil {
			r.sessAMBRUL = batch.SessionAMBR.UL
			r.sessAMBRDL = batch.SessionAMBR.DL
		}
		p.mu.Unlock()
		return nil
	}
	upSEID := s.upSEID
	p.mu.Unlock()

	req := &genpfcp.SessionModificationRequest{SEID: upSEID}

	// §7.5.4.17 Create-* IEs ride INSIDE Modification when a new
	// rule is added after the session is up.
	for _, x := range batch.CreatePDRs {
		req.CreatePDR = append(req.CreatePDR, buildCreatePDR(pendingPDR{
			pdrID: x.PDRID, precedence: x.Precedence,
			pdiSource: x.PDISource, qfi: x.QFI,
			farID: x.FARID, qerID: x.QERID, urrID: x.URRID,
			sdfRules: x.SDFRules,
			ueIPv4:   x.UEIPv4, teid: x.TEID, n3IPv4: x.N3IPv4,
		}))
	}
	for _, x := range batch.CreateFARs {
		req.CreateFAR = append(req.CreateFAR, buildCreateFAR(pendingFAR{
			farID: x.FARID, action: x.Action, dstIface: x.DstIface,
			teid: x.TEID, peerAddr: x.PeerAddr, peerPort: x.PeerPort,
		}))
	}
	for _, x := range batch.CreateQERs {
		req.CreateQER = append(req.CreateQER, buildCreateQER(pendingQER{
			qerID: x.QERID, qfi: x.QFI,
			gateUL: x.GateUL, gateDL: x.GateDL,
			mbrUL: x.MBRUL, mbrDL: x.MBRDL,
			gbrUL: x.GBRUL, gbrDL: x.GBRDL,
		}))
	}
	for _, x := range batch.CreateURRs {
		req.CreateURR = append(req.CreateURR, buildCreateURR(pendingURR{
			urrID:      x.URRID,
			measMethod: x.MeasMethod, reportTrigger: x.ReportTrigger,
			volThreshUL: x.VolThreshUL, volThreshDL: x.VolThreshDL,
			timeThresh: x.TimeThresh,
		}))
	}

	// §7.5.4.3 Update FAR — per-FARID OHC refresh + Apply Action.
	for _, uf := range batch.UpdateFARs {
		aa := genpfcp.ApplyAction{}
		ufp := &genpfcp.UpdateForwardingParameters{
			DestinationInterface: &genpfcp.DestinationInterface{Value: 0}, // Access
		}
		if uf.PeerAddr != 0 {
			aa.FORW = 1
			ufp.OuterHeaderCreation = ohcGTPUv4(uf.TEID, uf.PeerAddr, uf.PeerPort)
		} else {
			aa.BUFF = 1
		}
		req.UpdateFAR = append(req.UpdateFAR, genpfcp.UpdateFAR{
			FARID:                      genpfcp.FARID{Value: uf.FARID},
			ApplyAction:                &aa,
			UpdateForwardingParameters: ufp,
		})
	}

	// §7.5.4.6/.7/.8/.9 Remove-* — ID-only.
	for _, id := range batch.RemovePDRs {
		req.RemovePDR = append(req.RemovePDR, genpfcp.RemovePDR{
			PDRID: genpfcp.PacketDetectionRuleID{Value: id},
		})
	}
	for _, id := range batch.RemoveFARs {
		req.RemoveFAR = append(req.RemoveFAR, genpfcp.RemoveFAR{
			FARID: genpfcp.FARID{Value: id},
		})
	}
	for _, id := range batch.RemoveQERs {
		req.RemoveQER = append(req.RemoveQER, genpfcp.RemoveQER{
			QERID: genpfcp.QERID{Value: id},
		})
	}
	for _, id := range batch.RemoveURRs {
		req.RemoveURR = append(req.RemoveURR, genpfcp.RemoveURR{
			URRID: genpfcp.URRID{Value: id},
		})
	}

	// §8.2.28 Session-AMBR refresh rides a Create QER on the
	// reserved session-scope QER ID. Mirrors SetSessionAMBR's
	// post-establishment encoding.
	if batch.SessionAMBR != nil {
		req.CreateQER = append(req.CreateQER, buildCreateQER(pendingQER{
			qerID: 0xFFFFFFFE,
			mbrUL: batch.SessionAMBR.UL,
			mbrDL: batch.SessionAMBR.DL,
		}))
	}

	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("ApplyModifyBatch encode: %w", err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeSessionModificationRequest, upSEID, payload))
	if err != nil {
		return fmt.Errorf("ApplyModifyBatch send: %w", err)
	}
	var resp genpfcp.SessionModificationResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("ApplyModifyBatch response decode: %w", err)
	}
	if resp.Cause.Value != 1 {
		return fmt.Errorf("ApplyModifyBatch rejected: cause=%d (§7.5.5)",
			resp.Cause.Value)
	}
	p.log.WithIMSI(imsi).Infof("PFCP §7.5.4 batched modify pduSessID=%d UP-SEID=%#x: createPDR=%d createFAR=%d createQER=%d createURR=%d updateFAR=%d removePDR=%d removeFAR=%d removeQER=%d removeURR=%d ambr=%v",
		pduSessionID, upSEID,
		len(req.CreatePDR), len(req.CreateFAR), len(req.CreateQER), len(req.CreateURR),
		len(req.UpdateFAR),
		len(req.RemovePDR), len(req.RemoveFAR), len(req.RemoveQER), len(req.RemoveURR),
		batch.SessionAMBR != nil)
	return nil
}

// SetSessionAMBR Session-AMBR rides a session-scope QER per
// TS 23.501 §5.7.2.6 / TS 29.244 §8.2.28. Same batch-or-modify
// pattern as AddPDR/FAR/URR/QER. We emit a distinct QER with
// the reserved ID 0xFFFFFFFE so it doesn't collide with operator-
// allocated QER IDs.
func (p *PfcpBridge) SetSessionAMBR(imsi string, pduSessionID uint8,
	ambrUL, ambrDL uint64) error {
	rec := pendingQER{
		qerID: 0xFFFFFFFE,
		mbrUL: ambrUL, mbrDL: ambrDL,
	}
	return p.addOrModify(imsi, pduSessionID,
		func(r *pendingRules) {
			r.sessAMBRUL, r.sessAMBRDL = ambrUL, ambrDL
		},
		func(req *genpfcp.SessionModificationRequest) {
			req.CreateQER = append(req.CreateQER, buildCreateQER(rec))
		},
		"Session-AMBR",
	)
}

// SetUEAMBR is intentionally absent from PfcpBridge. UE-AMBR is
// enforced by the (R)AN per TS 23.501 v19.7.0 §5.7.1.6 / §5.7.2.6,
// not the UPF; TS 29.244 v19.5.0 carries no UE-AMBR IE. The AMF
// stamps UE-AMBR onto NGAP UEAggregateMaximumBitRate (see
// nf/amf/ngap/pdusetup) — that is the ONLY place UE-AMBR reaches
// an enforcement point.

// PktIO* — UPF-local dataplane I/O, not on the wire.
func (p *PfcpBridge) PktIOInit(n3Addr string, n3Port uint16, tunName, tunAddr string) error {
	return nil
}
func (p *PfcpBridge) PktIORun() error { return nil }
func (p *PfcpBridge) PktIOStop()      {}

// RegisterTEID / RegisterUEIP — UPF-local fast-path indices.
func (p *PfcpBridge) RegisterTEID(teid uint32, imsi string, pduSessionID uint8) error {
	return nil
}
func (p *PfcpBridge) RegisterUEIP(ueAddr uint32, imsi string, pduSessionID uint8) error {
	return nil
}

// UnregisterTEID / UnregisterUEIP / UnregisterSessionKeys —
// companion releases for §7.5.6 PFCP Session Deletion. PfcpBridge
// talks N4 to a separate UPF, so the actual reverse-map cleanup
// happens UP-side when the UPF processes the §7.5.6 Deletion
// Request — there is no extra wire IE for these. No-op here,
// mirroring the Register* counterparts.
func (p *PfcpBridge) UnregisterTEID(teid uint32) error  { return nil }
func (p *PfcpBridge) UnregisterUEIP(ueAddr uint32) error { return nil }
func (p *PfcpBridge) UnregisterSessionKeys(teids []uint32, ueips []uint32) (int, error) {
	return 0, nil
}

// GetURRStats / GetQERStats — sync query. TS 29.244 §7.5.4.10
// Query URR IE in a §7.5.4 Modification Request. Response carries
// Usage Report IE (§7.5.5.2).
//
// TODO(spec: TS 29.244 §7.5.4.10 Query URR + §7.5.5.2) — build
//
//	a §7.5.4 Modification Request with a Query URR IE naming the
//	urrID; parse resp.UsageReport for measurements.
func (p *PfcpBridge) GetURRStats(imsi string, pduSessionID uint8, urrID uint32) (volUL, volDL, pktUL, pktDL uint64, err error) {
	if p.statsPeer != nil {
		return p.statsPeer.GetURRStats(imsi, pduSessionID, urrID)
	}
	return 0, 0, 0, 0, ErrNotImplemented
}
func (p *PfcpBridge) GetQERStats(imsi string, pduSessionID uint8, qerID uint32) (dropPktsUL, dropPktsDL, dropBytesUL, dropBytesDL uint64, err error) {
	if p.statsPeer != nil {
		return p.statsPeer.GetQERStats(imsi, pduSessionID, qerID)
	}
	return 0, 0, 0, 0, ErrNotImplemented
}

// SetStatsPeer wires an in-process UPF dataplane bridge as the
// telemetry source for GetIOStats / GetURRStats / GetQERStats.
// Used by upfloop when CP and UP share a binary; control-plane
// PFCP traffic is unaffected. Pass nil to clear.
func (p *PfcpBridge) SetStatsPeer(peer upf.UPFBridge) { p.statsPeer = peer }

// GetIOStats / SessionCount — UPF-local aggregates. No PFCP
// representation.
func (p *PfcpBridge) GetIOStats() upf.IOStats {
	if p.statsPeer != nil {
		return p.statsPeer.GetIOStats()
	}
	return upf.IOStats{}
}
func (p *PfcpBridge) SessionCount() uint32 {
	p.mu.Lock()
	n := uint32(len(p.sessions))
	p.mu.Unlock()
	return n
}

// Slice* — UPF-local DPDK slice abstraction.
func (p *PfcpBridge) SliceInit(sliceID, sst uint8, name string) error { return nil }
func (p *PfcpBridge) SliceDestroy(sliceID uint8)                      {}
func (p *PfcpBridge) SliceSessionCreate(sliceID uint8, imsi string, pduSessionID uint8,
	dnn string, sst uint8, sd, ueAddr uint32) error {
	return nil
}

// DrainReports copies up to len(buf) §7.5.8 reports from the Go
// ring populated by handleInbound. Matches the upf.UPFBridge contract
// the cgo path satisfies via rte_ring dequeue. Non-blocking — returns
// 0 immediately when the ring is empty.
func (p *PfcpBridge) DrainReports(buf []upf.Report) int {
	n := 0
	for n < len(buf) {
		select {
		case r := <-p.reports:
			buf[n] = r
			n++
		default:
			return n
		}
	}
	return n
}

// ReportsDropped is the monotonic count of §7.5.8 messages that
// arrived after the ring hit its 1024-entry cap. Pair with the
// dpdkBridge-side ReportsDropped (rte_ring SP enqueue failure) for
// end-to-end back-pressure observability.
func (p *PfcpBridge) ReportsDropped() uint64 { return p.reportsDropped.Load() }

// handleInbound is the pfcp.Transport handler callback for
// unsolicited PDUs landing on this bridge's socket. In integrated-
// PFCP mode the only unsolicited inbound from UPF→SMF is §7.5.8
// Session Report Request (Downlink Data Report, Usage Report, Error
// Indication Report, TSC Management Info). We decode, convert to
// upf.Report records, push to the ring, and immediately ACK with a
// §7.5.8 Session Report Response (Cause=1).
func (p *PfcpBridge) handleInbound(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	if hdr.MessageType != genpfcp.MessageTypeSessionReportRequest {
		p.log.Warnf("PFCP inbound unhandled msgType=%d from %s (§7.2.2.1)",
			hdr.MessageType, peer)
		return
	}

	var req genpfcp.SessionReportRequest
	if err := req.Decode(payload); err != nil {
		p.log.Warnf("§7.5.8 Session Report Request decode from %s: %v", peer, err)
		return
	}

	// Map CP-SEID (which the UPF put in the Request header per
	// §7.2.2.4.2 — "destination SEID shall be set to the SEID
	// received in the F-SEID IE of the request", which for UPF→SMF
	// is the CP-F-SEID we sent at Establishment) back to the SMF's
	// (imsi, pduSessID) tuple. If unknown the session may have been
	// locally released mid-flight — log + drop, still ACK so the UPF
	// stops retransmitting (§7.6.2).
	p.mu.Lock()
	key, known := p.cpSEIDIndex[hdr.SEID]
	p.mu.Unlock()

	if known && req.ReportType.DLDR == 1 && req.DownlinkDataReport != nil {
		// §7.5.8.2 Downlink Data Report. Build one upf.Report per
		// PDR ID in the grouped IE (spec mandates at least one;
		// multiple PDRs can trigger from a single buffered packet
		// but that's rare — the common case is one DL PDR).
		for _, pdr := range req.DownlinkDataReport.PDRID {
			r := upf.Report{
				Type:         upf.ReportDLDR,
				IMSI:         key.imsi,
				PDUSessionID: key.pduSessionID,
				SEID:         hdr.SEID, // CP-SEID; upf.Report documents SEID
				Timestamp:    time.Now(),
				DLDR: &upf.DLDRPayload{
					// QFI / DSCP ride in DownlinkDataServiceInformation
					// when the UPF supports §7.5.8.2 Paging Policy
					// Differentiation. TODO(spec: extract when the
					// upfloop forwarder emits them).
				},
			}
			_ = pdr // PDR-ID in-message only; upf.Report keys on IMSI
			select {
			case p.reports <- r:
				p.log.WithIMSI(key.imsi).Infof("§7.5.8 DLDR received pduSessID=%d CP-SEID=%#x — queued for paging",
					key.pduSessionID, hdr.SEID)
			default:
				p.reportsDropped.Add(1)
				p.log.WithIMSI(key.imsi).Warnf("§7.5.8 DLDR ring full (1024) — dropping report pduSessID=%d",
					key.pduSessionID)
			}
		}
	} else if known && req.ReportType.USAR == 1 {
		// §7.5.8.3 Usage Report — CHF charging path. Skeleton:
		// upf.Report has UsagePayload but we don't populate every
		// volume measurement IE here (no CHF consumer yet). Drop
		// but don't warn (spec-legal, just unhandled downstream).
	} else if !known {
		p.log.Warnf("§7.5.8 Session Report for unknown UP-SEID=%#x from %s — stale session; ACKing anyway",
			hdr.SEID, peer)
	}

	// §7.5.9 Session Report Response (Cause=1) — required regardless
	// of whether we could route the report. Failure to ACK would make
	// the UPF retransmit per §7.6.2.
	//
	// Destination SEID per §7.2.2.4.2: this response is SMF→UPF, so
	// the SEID shall be the UP-SEID (the SEID the UPF allocated and
	// returned in the §7.5.3 Establishment Response's F-SEID). We
	// stored it on sessionState.upSEID; fall back to echoing hdr.SEID
	// only on unknown sessions (best-effort ACK, same fallback the
	// reject paths use).
	respSEID := hdr.SEID
	if known {
		p.mu.Lock()
		if s, ok := p.sessions[key]; ok {
			respSEID = s.upSEID
		}
		p.mu.Unlock()
	}
	resp := &genpfcp.SessionReportResponse{
		SEID:           respSEID,
		SequenceNumber: hdr.SequenceNumber,
		Cause:          genpfcp.Cause{Value: 1}, // §8.2.1 Request accepted
	}
	out, err := stripHeader(resp)
	if err != nil {
		p.log.Warnf("§7.5.9 Session Report Response encode: %v", err)
		return
	}
	_ = p.t.SendResponse(peer, genpfcp.MessageTypeSessionReportResponse,
		respSEID, hdr.SequenceNumber, out)
}

// Compile-time check that PfcpBridge satisfies upf.UPFBridge.
var _ upf.UPFBridge = (*PfcpBridge)(nil)

// ─── helpers ─────────────────────────────────────────────────────

func (p *PfcpBridge) nextCPSEID() uint64 {
	p.mu.Lock()
	p.cpSeid++
	v := p.cpSeid
	p.mu.Unlock()
	return v
}

// localNodeIPv4 returns the source IP for Node ID / F-SEID IEs.
// For loopback deployment we send 127.0.0.1; for distributed, the
// transport's local socket address is the right answer.
func (p *PfcpBridge) localNodeIPv4() net.IP {
	if la := p.t.LocalAddr(); la != nil && la.IP != nil {
		if v4 := la.IP.To4(); v4 != nil {
			return v4
		}
	}
	return net.ParseIP("127.0.0.1").To4()
}

// User ID encode/decode is owned by the codec runtime
// (codecs/tlv-3gpp-pfcp/pfcpgen/pkg/runtime UserID) — the typed
// SUPI/NAI/IMSI/IMEI/MSISDN/GPSI/PEI fields handle §8.2.101
// flag-conditional layout. Consumers populate the typed fields
// directly. NAI carries the SMF-allocated PDU Session ID as
// decimal text — repurposed because §8.2.101 doesn't define a
// PDU-session-ID subfield and we need the UPF C dataplane to
// key its session table on (IMSI, pduSessID).

// SDF Filter encode/decode is owned by the codec runtime
// (codecs/tlv-3gpp-pfcp/pfcpgen/pkg/runtime SDFFilter) — the
// generator emits SDFFilter as a runtime alias and consumers
// populate the typed FlowDescription / ToS / SPI / FlowLabel /
// SDFFilterID / SMMII fields directly. No hand-written helpers
// here; the spec layout (TS 29.244 §8.2.5 Figure 8.2.5-1) lives
// in one place at the codec source.

// ohcGTPUv4 builds the §8.2.56 OuterHeaderCreation typed struct for
// the GTP-U/UDP/IPv4 case (Description bit 5/1 set). Per spec the
// Port Number is implicit (2152) for GTP-U and not carried in the
// IE — `port` parameter retained for API symmetry but unused.
//
// The byte-level layout is owned by runtime.OuterHeaderCreation.Encode;
// this helper just populates the typed fields.
func ohcGTPUv4(teid, peerAddr uint32, port uint16) *genpfcp.OuterHeaderCreation {
	_ = port
	return &genpfcp.OuterHeaderCreation{
		Description: runtime.OHCDescGTPUUDPIPv4,
		TEID:        teid,
		IPv4: net.IPv4(
			byte(peerAddr>>24), byte(peerAddr>>16),
			byte(peerAddr>>8), byte(peerAddr),
		).To4(),
	}
}

// MBR / GBR encoding lives in runtime.MBR (40-bit big-endian per
// TS 29.244 §8.2.8 / §8.2.9). Consumers populate UL / DL uint64
// kbps fields on the typed struct.

// VolumeThreshold (§8.2.13) and TimeThreshold (§8.2.14) layouts are
// owned by the codec runtime / generator — consumers populate the
// typed fields (TotalVolume / UplinkVolume / DownlinkVolume / Seconds)
// directly. No hand-written helpers here.

// buildCreatePDR / buildCreateFAR / buildCreateQER / buildCreateURR
// convert our stashed pending* structs into generated-codec grouped
// IE types ready to drop into SessionEstablishmentRequest fields.
func buildCreatePDR(p pendingPDR) genpfcp.CreatePDR {
	out := genpfcp.CreatePDR{
		PDRID:      genpfcp.PacketDetectionRuleID{Value: p.pdrID},
		Precedence: genpfcp.Precedence{Value: p.precedence},
		PDI: genpfcp.PDI{
			SourceInterface: genpfcp.SourceInterface{Value: p.pdiSource & 0x0F},
		},
	}
	if p.qfi != 0 {
		out.PDI.QFI = []genpfcp.QFI{{Value: p.qfi & 0x3F}}
	}
	if p.sdfRules != "" {
		// Typed SDF Filter — runtime.SDFFilter owns the §8.2.5
		// flag-conditional encode (FD, TTC, SPI, FL, BID, SMMII).
		out.PDI.SDFFilter = []genpfcp.SDFFilter{{FlowDescription: p.sdfRules}}
	}
	// §8.2.62 UE IP Address — populate when set (DL PDR src=Core).
	// Typed runtime.UEIPAddress owns the §8.2.62 flag layout; we
	// just populate the IPv4 + S/D=Destination semantics.
	if p.ueIPv4 != 0 {
		ipv4 := net.IPv4(
			byte(p.ueIPv4>>24), byte(p.ueIPv4>>16),
			byte(p.ueIPv4>>8), byte(p.ueIPv4),
		).To4()
		out.PDI.UEIPAddress = []genpfcp.UEIPAddress{{
			IPv4:                ipv4,
			SourceOrDestination: true, // PDI use: 1 = Destination
		}}
	}
	// §8.2.3 F-TEID — populate when set (UL PDR src=Access).
	// runtime.FTEID.Encode handles the flag byte + TEID + IPv4
	// layout per the IE definition.
	if p.teid != 0 {
		ipv4 := net.IPv4(
			byte(p.n3IPv4>>24), byte(p.n3IPv4>>16),
			byte(p.n3IPv4>>8), byte(p.n3IPv4),
		).To4()
		out.PDI.FTEID = &genpfcp.FTEID{TEID: p.teid, IPv4: ipv4}
	}
	if p.farID != 0 {
		id := genpfcp.FARID{Value: p.farID}
		out.FARID = &id
	}
	if p.qerID != 0 {
		out.QERID = []genpfcp.QERID{{Value: p.qerID}}
	}
	if p.urrID != 0 {
		out.URRID = []genpfcp.URRID{{Value: p.urrID}}
	}
	return out
}

func buildCreateFAR(f pendingFAR) genpfcp.CreateFAR {
	// §8.2.26 Apply Action bits: FORW=1, DROP=0, BUFF=2, NOCP=3, DUPL=4.
	// Our Manager-facing `action` arg matches libupf_dp's scalar:
	//   0x01 = FORW, 0x02 = BUFF, 0x04 = DROP (but Manager sends 1=forward,
	//   2=buffer, see cgo_bridge_linux.go). Map into ApplyAction struct bits.
	aa := genpfcp.ApplyAction{}
	switch f.action {
	case 1:
		aa.FORW = 1
	case 2:
		aa.BUFF = 1
	case 3:
		aa.DROP = 1
	default:
		aa.FORW = 1 // default forward
	}
	fp := &genpfcp.ForwardingParameters{
		DestinationInterface: genpfcp.DestinationInterface{Value: f.dstIface & 0x0F},
	}
	if f.teid != 0 || f.peerAddr != 0 {
		fp.OuterHeaderCreation = ohcGTPUv4(f.teid, f.peerAddr, f.peerPort)
	}
	return genpfcp.CreateFAR{
		FARID:                genpfcp.FARID{Value: f.farID},
		ApplyAction:          aa,
		ForwardingParameters: fp,
	}
}

func buildCreateQER(q pendingQER) genpfcp.CreateQER {
	out := genpfcp.CreateQER{
		QERID: genpfcp.QERID{Value: q.qerID},
		GateStatus: genpfcp.GateStatus{
			ULGate: q.gateUL & 0x03,
			DLGate: q.gateDL & 0x03,
		},
	}
	if q.qfi != 0 {
		qfi := genpfcp.QFI{Value: q.qfi & 0x3F}
		out.QFI = &qfi
	}
	if q.mbrUL != 0 || q.mbrDL != 0 {
		out.MBR = &genpfcp.MBR{UL: q.mbrUL, DL: q.mbrDL}
	}
	if q.gbrUL != 0 || q.gbrDL != 0 {
		out.GBR = &genpfcp.GBR{UL: q.gbrUL, DL: q.gbrDL}
	}
	if q.dscp != 0 {
		// TS 29.244 §8.2.41 DL Flow Level Marking — TTC flag carries
		// {ToS value, ToS mask}. Mask=0xFC selects the DSCP bits
		// (RFC 2474 — top 6 of 8 in the IPv4 ToS / IPv6 Traffic Class
		// byte), so the UPF rewrites only DSCP and leaves ECN intact.
		out.DLFlowLevelMarking = &genpfcp.DLFlowLevelMarking{
			Value: EncodeDLFlowLevelMarking(q.dscp),
		}
	}
	return out
}

// EncodeDLFlowLevelMarking returns the wire-bytes payload for one
// TS 29.244 §8.2.41 DL Flow Level Marking IE carrying a DSCP value.
// Exposed publicly so the OAM panel + tester can verify the encoder
// without re-implementing the bit layout.
//
// §8.2.41 octets:
//
//	Octet 5: flags  TTC SCI  (bit 1 TTC, bit 2 SCI)
//	Octet 6: spare
//	If TTC: 2 octets — ToS value + ToS mask
//	If SCI: 2 octets — Service Class Indicator
//
// We set TTC only and clear SCI; the value is dscp shifted into the
// upper 6 bits of the ToS byte (RFC 2474 — DSCP occupies bits 7..2,
// leaving ECN in bits 1..0). Mask=0xFC matches the same six bits.
func EncodeDLFlowLevelMarking(dscp uint8) []byte {
	tos := dscp << 2
	return []byte{0x01, 0x00, tos, 0xFC}
}

// DSCPForQoSProfile maps a security/dpi qos_profile string to the
// RFC 4594 / 5GS-recommended DSCP value. Unknown / empty profiles
// return 0 (Best-Effort) which signals the caller to skip the
// DLFlowLevelMarking IE entirely.
//
// Mapping (RFC 4594 §3 Forwarding Class Definitions):
//   - "voice", "voip", "voice-call"    → EF   (46) — TS 23.501 5QI=1
//   - "video"                          → AF41 (34) — TS 23.501 5QI=2
//   - "low-latency", "interactive"     → AF31 (26)
//   - "iot", "mtc"                     → AF11 (10)
//   - "" / "general" / "default" / etc → 0 (BE)
func DSCPForQoSProfile(profile string) uint8 {
	switch profile {
	case "voice", "voip", "voice-call":
		return 46
	case "video":
		return 34
	case "low-latency", "interactive":
		return 26
	case "iot", "mtc":
		return 10
	default:
		return 0
	}
}

func buildCreateURR(u pendingURR) genpfcp.CreateURR {
	out := genpfcp.CreateURR{
		URRID: genpfcp.URRID{Value: u.urrID},
		MeasurementMethod: genpfcp.MeasurementMethod{
			DURAT: (u.measMethod >> 0) & 1,
			VOLUM: (u.measMethod >> 1) & 1,
			EVENT: (u.measMethod >> 2) & 1,
		},
		ReportingTriggers: genpfcp.ReportingTriggers{Flags: uint16(u.reportTrigger)},
	}
	if u.volThreshUL != 0 || u.volThreshDL != 0 {
		// Typed §8.2.13 — generator emits VolumeThreshold with
		// flag-conditional uint64 fields. We always set both
		// directions when either is non-zero (TOVOL stays clear
		// because we don't aggregate).
		ul, dl := u.volThreshUL, u.volThreshDL
		out.VolumeThreshold = &genpfcp.VolumeThreshold{
			UplinkVolume:   &ul,
			DownlinkVolume: &dl,
		}
	}
	if u.timeThresh != 0 {
		out.TimeThreshold = &genpfcp.TimeThreshold{Seconds: u.timeThresh}
	}
	return out
}

// pfcpRequest is a tiny adapter so the upfclient package can call
// into the transport's encodedMessage type without re-exporting
// its internals. Lives here until the transport surfaces a public
// constructor.
//
// TODO — expose pfcp.NewMessage(msgType, seid, ies) []byte helper
//
//	so this forwarder isn't needed. Leaving inline for now keeps
//	the transport API surface minimal.
func pfcpRequest(msgType uint8, seid uint64, ies []byte) pfcp.EncodedMessage {
	return pfcp.EncodedMessage{MsgType: msgType, SEID: seid, IEs: ies}
}


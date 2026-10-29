// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// cgo_bridge.go — UPFBridge interface: the abstract seam between
// the SMF control plane and the UPF user plane.
//
// Authoritative specs (PDFs under codecs/tlv-3gpp-pfcp/ and
// specs/3gpp/):
//
//	TS 29.244 v19.5.0 — PFCP Protocol (N4)
//	  §6.1   PFCP/UDP transport, port 8805
//	  §7.5.2 PFCP Session Establishment Request
//	  §7.5.4 PFCP Session Modification Request
//	  §7.5.6 PFCP Session Deletion Request
//	  §7.5.8 PFCP Session Report Request (async, UPF-initiated)
//
//	TS 23.501 v19.7.0 §5.8 — Control and User Plane Separation (CUPS)
//	  architectural rationale. The CUPS design says SMF (CP) and
//	  UPF (UP) are distinct NFs connected via the N4 reference
//	  point (PFCP). This interface is the seam that enables that
//	  split without the rest of the SMF caring about transport.
//
// Two implementations of UPFBridge are anticipated:
//
//  1. cgoBridge  — co-located, zero-wire. Direct cgo into libupf_dp.so.
//     Lives in cgo_bridge_linux.go. Used today. Fastest path;
//     suitable for dev / lab / CI.
//
//  2. pfcpBridge — integrated or distributed over PFCP/UDP. Encodes
//     each method call as a §7.5.x PFCP message, sends on UDP:8805,
//     awaits response. Lives in nf/smf/upfclient/ (future). Enables:
//     - "Integrated PFCP": single binary, loopback socket (127.0.0.1
//     :8805) — exercises real PFCP in CI without deployment cost.
//     - "Distributed CUPS": separate SMF and UPF binaries / hosts
//     (TS 23.501 §5.8). SMF config just points at remote UPF.
//
// Both implementations expose the same Go surface. Runtime selection
// is via infra_config.upf_mode (planned: "cgo" | "pfcp"). Callers
// (nf/smf/session/*.go) NEVER import implementation packages; they
// only see this interface.
//
// The mmt-studio-core-go repo is self-contained — DPDK 25.11 sources
// ship under libs/dpdk-25.11/ for the cgo impl; the generated PFCP
// codec at codecs/tlv-3gpp-pfcp/pfcpgen/generated/ (23 messages,
// 252 IEs) backs the pfcp impl. No external dependencies.
package upf

// UPFBridge is the abstract interface the Manager calls into the
// user-plane function. Each method maps 1:1 to a §7.5.x PFCP message
// (for the pfcp impl) or to a function in upf_dp_api.h (for the cgo
// impl) — see the table in the file header.
//
// On Linux the default is the cgo implementation: dpdkBridge in
// cgo_bridge_linux.go sets `bridge = &dpdkBridge{}` at init.
//
// Backwards-compat alias CgoBridge is kept during the transition —
// prefer UPFBridge in new code.
type UPFBridge interface {
	Init(argc int, argv []string) error
	Cleanup()
	SetMaxSessions(n uint32) error
	SetPMDTuning(mbufPoolSize uint32, rxRingSize, txRingSize uint16) error

	SessionCreate(imsi string, pduSessionID uint8, dnn string, sst uint8, sd, ueAddr uint32, pdnType uint8) error
	SessionDelete(imsi string, pduSessionID uint8) error

	// CommitSession finalises a session whose rules were declared via
	// SessionCreate + AddPDR/FAR/QER/URR/SetSessionAMBR. For the cgo
	// bridge this is a no-op (each call already mutated the C dataplane).
	// For the PFCP bridge this triggers the single §7.5.2 Session
	// Establishment Request carrying every Create-* IE in one round
	// trip, instead of one empty Establishment + N per-rule
	// Modifications. Callers MUST issue CommitSession after all rule
	// declarations and before any post-establishment Update-* /
	// DeactivateDLFAR.
	CommitSession(imsi string, pduSessionID uint8) error

	// AddPDR carries the PDR rule plus the optional PDI fast-path
	// match keys per TS 29.244 v19.5.0 §7.5.2.2 + §8.2.3 (F-TEID) +
	// §8.2.62 (UE IP Address). Zero ueIPv4/teid/n3IPv4 means "no
	// fast-path PDI key" (SDF-only matching). When non-zero:
	//   - DL PDR (pdiSource=1): ueIPv4 is encoded as PDI.UEIPAddress
	//     with V4=1 + S/D=1 (destination match).
	//   - UL PDR (pdiSource=0): teid + n3IPv4 are encoded as
	//     PDI.FTEID with V4=1 + the UPF-allocated UL TEID.
	AddPDR(imsi string, pduSessionID uint8, pdrID uint16, precedence uint32,
		pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
		ueIPv4, teid, n3IPv4 uint32) error
	AddFAR(imsi string, pduSessionID uint8, farID uint32, action, dstIface uint8,
		teid, peerAddr uint32, peerPort uint16, ohcType uint8) error
	UpdateFAR(imsi string, pduSessionID uint8, farID, teid, peerAddr uint32, peerPort uint16) error

	// UpdatePDR / UpdateURR / UpdateQER — TS 29.244 v19.5.0
	// §7.5.4.2 / .4 / .5. Today the C dataplane wholesale-replaces
	// the rule by ID (rule MUST already exist; -1 if not). Caller is
	// responsible for passing the full desired-state field set; an
	// SMF that wants partial Update should also supply the unchanged
	// fields. See applyUpdatePDRToHook etc. for the Go-side decode
	// of the conditional Update IEs into these calls.
	UpdatePDR(imsi string, pduSessionID uint8, pdrID uint16, precedence uint32,
		pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
		ueIPv4, teid, n3IPv4 uint32) error
	UpdateQER(imsi string, pduSessionID uint8, qerID uint32,
		qfi, gateUL, gateDL uint8,
		mbrUL, mbrDL, gbrUL, gbrDL uint64) error
	UpdateURR(imsi string, pduSessionID uint8, urrID uint32,
		measMethod, reportTrigger uint8,
		volThreshUL, volThreshDL uint64, timeThresh uint32) error
	// DeactivateDLFAR flips a DL FAR Apply Action FORW → BUFF and
	// clears the gNB tunnel info at the dataplane. Mirrors
	// TS 23.502 §4.2.6 step 6a (N4 Session Modification on AN
	// Release) + TS 29.244 §8.2.26 BUFF bit.
	DeactivateDLFAR(imsi string, pduSessionID uint8, farID uint32) error

	// Remove* implements TS 29.244 v19.5.0 §7.5.4.6 / .7 / .8 / .9
	// Remove PDR / FAR / URR / QER IEs. Each Remove * IE in a §7.5.4
	// Modification Request "shall identify the {PDR|FAR|URR|QER} to
	// be deleted" by its mandatory ID. The dataplane flips active=false
	// on the matching slot; the classifier short-circuits inactive
	// slots so removal takes effect for every subsequent packet.
	// Idempotent: returns nil if rule already absent.
	RemovePDR(imsi string, pduSessionID uint8, pdrID uint16) error
	RemoveFAR(imsi string, pduSessionID uint8, farID uint32) error
	RemoveQER(imsi string, pduSessionID uint8, qerID uint32) error
	RemoveURR(imsi string, pduSessionID uint8, urrID uint32) error
	AddQER(imsi string, pduSessionID uint8, qerID uint32, qfi, gateUL, gateDL uint8,
		mbrUL, mbrDL, gbrUL, gbrDL uint64) error
	AddURR(imsi string, pduSessionID uint8, urrID uint32, measMethod, reportTrigger uint8,
		volThreshUL, volThreshDL uint64, timeThresh uint32) error
	SetSessionAMBR(imsi string, pduSessionID uint8, ambrUL, ambrDL uint64) error
	// UE-AMBR is intentionally absent from this interface. Per
	// TS 23.501 v19.7.0 §5.7.1.6: "The (R)AN shall enforce UE-AMBR
	// (see clause 5.7.2.6) in UL and DL per UE for Non-GBR QoS
	// Flows." UE-AMBR is not a UPF responsibility, and TS 29.244
	// v19.5.0 has no UE-AMBR IE; the AMF carries UE-AMBR to the
	// gNB in NGAP UEAggregateMaximumBitRate (see
	// nf/amf/ngap/pdusetup).

	PktIOInit(n3Addr string, n3Port uint16, tunName, tunAddr string) error
	PktIORun() error
	PktIOStop()

	RegisterTEID(teid uint32, imsi string, pduSessionID uint8) error
	RegisterUEIP(ueAddr uint32, imsi string, pduSessionID uint8) error

	// UnregisterTEID / UnregisterUEIP release a previously-registered
	// reverse-map entry. Required for TS 29.244 v19.5.0 §7.5.6 PFCP
	// Session Deletion to actually return the F-TEID (§5.5.1) and UE
	// IP (§8.2.62) resources the UP function held for the session;
	// without them the dataplane teid_hash / ueip_hash leak slots and
	// eventually saturate, silently breaking new-session classification.
	// Idempotent: returns nil if the key was absent.
	UnregisterTEID(teid uint32) error
	UnregisterUEIP(ueAddr uint32) error

	// UnregisterSessionKeys batches the per-PDR (TEID, UE-IP)
	// release that §7.5.6 deletion drives. One cgo round-trip
	// walks both arrays at the dataplane EAL thread instead of
	// 2×N sequential round-trips. Spec semantics unchanged: TS
	// 29.244 v19.5.0 §7.5.6 + §5.5.1 + §8.2.62. Idempotent.
	// Returns count of keys actually released.
	UnregisterSessionKeys(teids []uint32, ueips []uint32) (int, error)

	// ApplyModifyBatch coalesces a set of post-establishment §7.5.4
	// Session Modification IEs into ONE PFCP Modification Request on
	// the wire. TS 29.244 v19.5.0 §7.5.4.2 explicitly allows any
	// combination of Create-* / Update-* / Remove-* IEs in a single
	// Modification Request — emitting them per-IE is spec-legal but
	// expensive (each round-trip is a UDP send + handler dispatch +
	// response). The PCF→SMF UpdateNotify path that lands per SIP
	// INVITE installs a (QER, FAR) pair per new flow; for a 4-call
	// VoNR fan-out that's 8 sequential round-trips reduced to 1.
	//
	// Bridges that don't speak wire PFCP (cgoBridge, goBridge) MAY
	// implement this by iterating their existing Add*/Update*/Remove*
	// methods — the latency saved is on the wire, not in-process.
	// Spec semantics are identical either way.
	ApplyModifyBatch(imsi string, pduSessionID uint8, batch ModifyBatch) error

	GetURRStats(imsi string, pduSessionID uint8, urrID uint32) (volUL, volDL, pktUL, pktDL uint64, err error)
	GetQERStats(imsi string, pduSessionID uint8, qerID uint32) (dropPktsUL, dropPktsDL, dropBytesUL, dropBytesDL uint64, err error)
	GetIOStats() IOStats
	SessionCount() uint32

	SliceInit(sliceID, sst uint8, name string) error
	SliceDestroy(sliceID uint8)
	SliceSessionCreate(sliceID uint8, imsi string, pduSessionID uint8, dnn string, sst uint8, sd, ueAddr uint32) error

	// Report framework — TS 29.244 §7.5.8 PFCP Session Report
	// Request. DrainReports pulls up to len(buf) records from the
	// lockless rte_ring populated by DPDK lcores on the C side;
	// ReportsDropped is a monotonic counter of enqueue overflows
	// for back-pressure monitoring. See nf/upf/report.go + C header
	// dataplane/include/upf_report.h for the full design.
	DrainReports(buf []Report) int
	ReportsDropped() uint64
}

// CgoBridge is a transitional alias preserved for existing import
// paths. New code should reference UPFBridge. Once all call sites
// switch (grep: `CgoBridge`), this alias can retire.
type CgoBridge = UPFBridge

// bridge is set to dpdkBridge by cgo_bridge_linux.go init() on Linux.
// The Manager checks bridge != nil before every operation and delegates
// to the chosen implementation (cgo today; pfcp once nf/smf/upfclient
// lands). See the interface doc in this file for the dual-impl model.
var bridge UPFBridge

// SetBridge injects an alternate UPFBridge implementation at startup
// — invoked by the webservice main when infra_config.upf_bridge_mode
// is set to "pfcp-loop" so the Integrated-PFCP bootstrap
// (nf/upf/upfloop) can replace the default cgo dpdkBridge with a
// PfcpBridge dialing 127.0.0.1:8805.
//
// Must be called BEFORE Manager.Init() — Init reads `bridge` only
// once and drives all further calls through whatever was installed.
// Passing nil disables the bridge entirely (Manager calls become
// no-ops; useful for tests that don't need a dataplane).
func SetBridge(b UPFBridge) { bridge = b }

// Bridge returns the installed UPFBridge. nil when no cgo driver
// compiled in and no SetBridge call happened yet. Exposed for tests
// and for the upfloop bootstrap to detect double-install.
func Bridge() UPFBridge { return bridge }

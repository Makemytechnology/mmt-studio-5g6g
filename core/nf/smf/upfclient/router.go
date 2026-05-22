// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// router.go — RouterBridge: a UPFBridge that fronts N PfcpBridge
// instances, one per UPF anchor, and dispatches per-session calls
// to the right PFCP wire endpoint based on the slice (S-NSSAI) /
// DNN UPF-selection result.
//
// Architectural rationale (TS 23.501 v19.7.0 §6.3.3 — UPF selection
// for a PDU session):
//
//	"The SMF selects the UPF that supports the requested S-NSSAI,
//	 DNN, …"
//
// Each (S-NSSAI, DNN) tuple may map to a distinct UPF anchor; the
// SMF therefore needs N parallel PFCP associations, one per UPF, and
// must route every per-session message to the UPF the session was
// anchored on. RouterBridge keeps a (imsi, pduSessionID) → upfID
// map populated at SessionCreate time (when the registry returns
// the chosen Instance) and dispatches every subsequent call by
// looking that mapping up.
//
// PFCP/N4 (TS 29.244 v19.5.0):
//
//	§7.3.4    PFCP Association Setup — one per UPF; established by
//	          the inner PfcpBridge.Dial() at registration time.
//	§7.5.x    Session-scoped messages — header SEID is allocator-
//	          local (§7.2.2.4.2), so per-UPF demux is mandatory:
//	          a CP-SEID allocated by bridge A is meaningless to
//	          bridge B.
//
// Existing single-UPF tests / call paths keep working: when only
// one bridge is registered, every session lands on it (the registry
// fallback in upf.Select returns "any UPF").

package upfclient

import (
	"fmt"
	"strings"
	"sync"

	smfupf "github.com/mmt/mmt-studio-core/nf/smf/upf"
	"github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// RouterBridge implements upf.UPFBridge by dispatching to one of N
// real PfcpBridge instances, keyed by the SMF's UPF-selection
// (TS 23.501 §6.3.3).
type RouterBridge struct {
	mu       sync.RWMutex
	bridges  map[string]*PfcpBridge // upfID → bridge (one per UPF anchor)
	sessUPF  map[sessionKey]string  // (imsi,pduSessionID) → upfID
	fallback string                 // upfID used when registry returns empty / no match
	log      *logger.Logger
}

// NewRouterBridge returns an empty router. Bridges are added by
// RegisterBridge as upfloop.EnableMulti dials each UPF anchor.
func NewRouterBridge() *RouterBridge {
	return &RouterBridge{
		bridges: make(map[string]*PfcpBridge),
		sessUPF: make(map[sessionKey]string),
		log:     logger.Get("smf.upfclient.router"),
	}
}

// RegisterBridge installs br as the routing destination for upfID.
// First registration becomes the fallback (used when the per-session
// registry.Select returns an empty/unknown UPFID — preserves the
// single-UPF behaviour for legacy callers and tests).
func (r *RouterBridge) RegisterBridge(upfID string, br *PfcpBridge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bridges[upfID] = br
	if r.fallback == "" {
		r.fallback = upfID
	}
}

// BridgeOf returns the PfcpBridge registered for upfID (nil if none).
// Exposed for tests and for telemetry endpoints that want per-UPF
// stats peers.
func (r *RouterBridge) BridgeOf(upfID string) *PfcpBridge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bridges[upfID]
}

// Bridges returns a snapshot of (upfID → bridge). Used by upfloop's
// shutdown path to close every dialed bridge in turn.
func (r *RouterBridge) Bridges() map[string]*PfcpBridge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*PfcpBridge, len(r.bridges))
	for k, v := range r.bridges {
		out[k] = v
	}
	return out
}

// pickForCreate runs UPF selection per TS 23.501 §6.3.3 and binds
// the (imsi, pduSessionID) to the resulting upfID. Subsequent calls
// for the same key are routed to that bridge.
func (r *RouterBridge) pickForCreate(imsi string, pduSessionID uint8, dnn string, sst uint8) (*PfcpBridge, string, error) {
	// upf.Select compares supported_sst entries case-insensitively
	// (CSV strings in DB), so format SST as upper-case hex to match.
	sstHex := fmt.Sprintf("%02X", sst)
	inst, err := smfupf.Select(dnn, sstHex)
	upfID := ""
	if err == nil && inst != nil {
		upfID = inst.UPFID
	}

	r.mu.Lock()
	br, ok := r.bridges[upfID]
	if !ok {
		// registry empty / no match — fall back to the first bridge
		// registered. Single-UPF deployments hit this every time.
		upfID = r.fallback
		br = r.bridges[upfID]
	}
	if br != nil {
		r.sessUPF[sessionKey{imsi, pduSessionID}] = upfID
	}
	r.mu.Unlock()

	if br == nil {
		return nil, "", fmt.Errorf("upfclient/router: no bridge registered (registry select=%v)", err)
	}
	return br, upfID, nil
}

// dispatch returns the bridge a session was bound to at SessionCreate
// time. Falls back to the default bridge when the mapping is missing
// (e.g. a stale Modify after a process restart, or a test that calls
// post-create methods without going through SessionCreate).
func (r *RouterBridge) dispatch(imsi string, pduSessionID uint8) (*PfcpBridge, error) {
	r.mu.RLock()
	upfID, ok := r.sessUPF[sessionKey{imsi, pduSessionID}]
	if !ok {
		upfID = r.fallback
	}
	br := r.bridges[upfID]
	r.mu.RUnlock()
	if br == nil {
		return nil, fmt.Errorf("upfclient/router: no bridge for imsi=%s pduSessID=%d (upfID=%q)", imsi, pduSessionID, upfID)
	}
	return br, nil
}

// dispatchAll returns every registered bridge (used for fan-out
// methods like Slice* / DrainReports that aren't keyed by session).
func (r *RouterBridge) dispatchAll() []*PfcpBridge {
	r.mu.RLock()
	out := make([]*PfcpBridge, 0, len(r.bridges))
	for _, b := range r.bridges {
		out = append(out, b)
	}
	r.mu.RUnlock()
	return out
}

// ── upf.UPFBridge implementation ─────────────────────────────────
//
// Init / Cleanup / SetMaxSessions / SetPMDTuning / PktIO* /
// Slice* are UPF-local concepts (DPDK EAL bring-up, packet I/O
// init). The SMF-side bridges are no-ops for them on the wire,
// matching the existing PfcpBridge stubs.

func (r *RouterBridge) Init(argc int, argv []string) error { return nil }

func (r *RouterBridge) Cleanup() {
	for _, b := range r.dispatchAll() {
		b.Cleanup()
	}
}

func (r *RouterBridge) SetMaxSessions(n uint32) error { return nil }
func (r *RouterBridge) SetPMDTuning(mbufPoolSize uint32, rxRingSize, txRingSize uint16) error {
	return nil
}

func (r *RouterBridge) SessionCreate(imsi string, pduSessionID uint8,
	dnn string, sst uint8, sd, ueAddr uint32, pdnType uint8) error {
	br, upfID, err := r.pickForCreate(imsi, pduSessionID, dnn, sst)
	if err != nil {
		return err
	}
	r.log.WithIMSI(imsi).Infof("UPF anchor selected upfID=%s for SST=%d SD=%#x DNN=%s pduSessID=%d (TS 23.501 §6.3.3)",
		upfID, sst, sd, dnn, pduSessionID)
	return br.SessionCreate(imsi, pduSessionID, dnn, sst, sd, ueAddr, pdnType)
}

func (r *RouterBridge) CommitSession(imsi string, pduSessionID uint8) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.CommitSession(imsi, pduSessionID)
}

func (r *RouterBridge) SessionDelete(imsi string, pduSessionID uint8) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	delErr := br.SessionDelete(imsi, pduSessionID)
	// Drop the (imsi,pduSessionID) → upfID binding regardless of
	// the outcome — even on a §7.5.7 error the SMF treats the
	// session as gone (the standard recovery is re-establishment,
	// which will re-bind via pickForCreate).
	r.mu.Lock()
	delete(r.sessUPF, sessionKey{imsi, pduSessionID})
	r.mu.Unlock()
	return delErr
}

func (r *RouterBridge) AddPDR(imsi string, pduSessionID uint8, pdrID uint16, precedence uint32,
	pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
	ueIPv4, teid, n3IPv4 uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.AddPDR(imsi, pduSessionID, pdrID, precedence,
		pdiSource, qfi, farID, qerID, urrID, sdfRules,
		ueIPv4, teid, n3IPv4)
}

func (r *RouterBridge) AddFAR(imsi string, pduSessionID uint8, farID uint32, action, dstIface uint8,
	teid, peerAddr uint32, peerPort uint16, ohcType uint8) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.AddFAR(imsi, pduSessionID, farID, action, dstIface,
		teid, peerAddr, peerPort, ohcType)
}

func (r *RouterBridge) UpdateFAR(imsi string, pduSessionID uint8, farID, teid, peerAddr uint32, peerPort uint16) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.UpdateFAR(imsi, pduSessionID, farID, teid, peerAddr, peerPort)
}

func (r *RouterBridge) UpdatePDR(imsi string, pduSessionID uint8, pdrID uint16, precedence uint32,
	pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
	ueIPv4, teid, n3IPv4 uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.UpdatePDR(imsi, pduSessionID, pdrID, precedence,
		pdiSource, qfi, farID, qerID, urrID, sdfRules,
		ueIPv4, teid, n3IPv4)
}

func (r *RouterBridge) UpdateQER(imsi string, pduSessionID uint8, qerID uint32,
	qfi, gateUL, gateDL uint8, mbrUL, mbrDL, gbrUL, gbrDL uint64) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.UpdateQER(imsi, pduSessionID, qerID, qfi, gateUL, gateDL,
		mbrUL, mbrDL, gbrUL, gbrDL)
}

func (r *RouterBridge) UpdateURR(imsi string, pduSessionID uint8, urrID uint32,
	measMethod, reportTrigger uint8, volThreshUL, volThreshDL uint64, timeThresh uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.UpdateURR(imsi, pduSessionID, urrID,
		measMethod, reportTrigger, volThreshUL, volThreshDL, timeThresh)
}

func (r *RouterBridge) DeactivateDLFAR(imsi string, pduSessionID uint8, farID uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.DeactivateDLFAR(imsi, pduSessionID, farID)
}

func (r *RouterBridge) RemovePDR(imsi string, pduSessionID uint8, pdrID uint16) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.RemovePDR(imsi, pduSessionID, pdrID)
}

func (r *RouterBridge) RemoveFAR(imsi string, pduSessionID uint8, farID uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.RemoveFAR(imsi, pduSessionID, farID)
}

func (r *RouterBridge) RemoveQER(imsi string, pduSessionID uint8, qerID uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.RemoveQER(imsi, pduSessionID, qerID)
}

func (r *RouterBridge) RemoveURR(imsi string, pduSessionID uint8, urrID uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.RemoveURR(imsi, pduSessionID, urrID)
}

func (r *RouterBridge) AddQER(imsi string, pduSessionID uint8, qerID uint32, qfi, gateUL, gateDL uint8,
	mbrUL, mbrDL, gbrUL, gbrDL uint64) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.AddQER(imsi, pduSessionID, qerID, qfi, gateUL, gateDL,
		mbrUL, mbrDL, gbrUL, gbrDL)
}

func (r *RouterBridge) AddURR(imsi string, pduSessionID uint8, urrID uint32, measMethod, reportTrigger uint8,
	volThreshUL, volThreshDL uint64, timeThresh uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.AddURR(imsi, pduSessionID, urrID,
		measMethod, reportTrigger, volThreshUL, volThreshDL, timeThresh)
}

func (r *RouterBridge) SetSessionAMBR(imsi string, pduSessionID uint8, ambrUL, ambrDL uint64) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.SetSessionAMBR(imsi, pduSessionID, ambrUL, ambrDL)
}

func (r *RouterBridge) ApplyModifyBatch(imsi string, pduSessionID uint8, batch upf.ModifyBatch) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return err
	}
	return br.ApplyModifyBatch(imsi, pduSessionID, batch)
}

// PktIO* — UPF-local. No-ops on the SMF side.
func (r *RouterBridge) PktIOInit(n3Addr string, n3Port uint16, tunName, tunAddr string) error {
	return nil
}
func (r *RouterBridge) PktIORun() error { return nil }
func (r *RouterBridge) PktIOStop()      {}

// Register* / Unregister* — UPF-local fast-path indices. The PfcpBridge
// implementations are no-ops; we forward anyway so behaviour matches a
// single-bridge install if a future implementation populates them.
func (r *RouterBridge) RegisterTEID(teid uint32, imsi string, pduSessionID uint8) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return nil // pre-SessionCreate ordering is allowed
	}
	return br.RegisterTEID(teid, imsi, pduSessionID)
}

func (r *RouterBridge) RegisterUEIP(ueAddr uint32, imsi string, pduSessionID uint8) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		return nil
	}
	return br.RegisterUEIP(ueAddr, imsi, pduSessionID)
}

func (r *RouterBridge) UnregisterTEID(teid uint32) error {
	// Reverse-map cleanup is UP-side per §7.5.6 — fan-out to all
	// bridges so any one that holds the slot releases it.
	for _, b := range r.dispatchAll() {
		_ = b.UnregisterTEID(teid)
	}
	return nil
}

func (r *RouterBridge) UnregisterUEIP(ueAddr uint32) error {
	for _, b := range r.dispatchAll() {
		_ = b.UnregisterUEIP(ueAddr)
	}
	return nil
}

func (r *RouterBridge) UnregisterSessionKeys(teids []uint32, ueips []uint32) (int, error) {
	total := 0
	for _, b := range r.dispatchAll() {
		n, _ := b.UnregisterSessionKeys(teids, ueips)
		total += n
	}
	return total, nil
}

func (r *RouterBridge) GetURRStats(imsi string, pduSessionID uint8, urrID uint32) (volUL, volDL, pktUL, pktDL uint64, err error) {
	br, derr := r.dispatch(imsi, pduSessionID)
	if derr != nil {
		return 0, 0, 0, 0, derr
	}
	return br.GetURRStats(imsi, pduSessionID, urrID)
}

func (r *RouterBridge) GetQERStats(imsi string, pduSessionID uint8, qerID uint32) (dropPktsUL, dropPktsDL, dropBytesUL, dropBytesDL uint64, err error) {
	br, derr := r.dispatch(imsi, pduSessionID)
	if derr != nil {
		return 0, 0, 0, 0, derr
	}
	return br.GetQERStats(imsi, pduSessionID, qerID)
}

// GetIOStats reports the dataplane-level aggregate. Every PfcpBridge's
// statsPeer points at the same in-process dpdkBridge (one C session
// table fan-out from upfloop), so reading from any one bridge is
// equivalent — pick the fallback to keep the call cheap.
func (r *RouterBridge) GetIOStats() upf.IOStats {
	r.mu.RLock()
	br := r.bridges[r.fallback]
	r.mu.RUnlock()
	if br == nil {
		return upf.IOStats{}
	}
	return br.GetIOStats()
}

// SessionCount sums every bridge's view. Each bridge's session set is
// disjoint (a session is bound to exactly one UPF anchor at
// SessionCreate), so summing is safe.
func (r *RouterBridge) SessionCount() uint32 {
	var n uint32
	for _, b := range r.dispatchAll() {
		n += b.SessionCount()
	}
	return n
}

// Slice* — UPF-local. Forward to all bridges so a slice operation
// reaches every UPF anchor.
func (r *RouterBridge) SliceInit(sliceID, sst uint8, name string) error {
	for _, b := range r.dispatchAll() {
		if err := b.SliceInit(sliceID, sst, name); err != nil {
			return err
		}
	}
	return nil
}

func (r *RouterBridge) SliceDestroy(sliceID uint8) {
	for _, b := range r.dispatchAll() {
		b.SliceDestroy(sliceID)
	}
}

func (r *RouterBridge) SliceSessionCreate(sliceID uint8, imsi string, pduSessionID uint8,
	dnn string, sst uint8, sd, ueAddr uint32) error {
	br, err := r.dispatch(imsi, pduSessionID)
	if err != nil {
		// Pre-SessionCreate ordering: pick by SST and bind now.
		br, _, err = r.pickForCreate(imsi, pduSessionID, dnn, sst)
		if err != nil {
			return err
		}
	}
	return br.SliceSessionCreate(sliceID, imsi, pduSessionID, dnn, sst, sd, ueAddr)
}

// DrainReports / ReportsDropped — fan-in across all bridges. Each
// PfcpBridge owns its own §7.5.8 ring; we copy from each in turn until
// the caller's buffer is full.
func (r *RouterBridge) DrainReports(buf []upf.Report) int {
	if len(buf) == 0 {
		return 0
	}
	n := 0
	for _, b := range r.dispatchAll() {
		if n >= len(buf) {
			break
		}
		n += b.DrainReports(buf[n:])
	}
	return n
}

func (r *RouterBridge) ReportsDropped() uint64 {
	var sum uint64
	for _, b := range r.dispatchAll() {
		sum += b.ReportsDropped()
	}
	return sum
}

// String renders the routing table for log lines / panic dumps.
func (r *RouterBridge) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	parts := make([]string, 0, len(r.bridges))
	for id := range r.bridges {
		parts = append(parts, id)
	}
	return fmt.Sprintf("RouterBridge[%s, fallback=%s]", strings.Join(parts, ","), r.fallback)
}

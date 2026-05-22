// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
//go:build linux

// cgo_bridge_linux.go — Real cgo bridge to the DPDK-based UPF dataplane.
//
// All DPDK C API calls execute on a single OS thread that called
// rte_eal_init(). This is required because DPDK hugepage memory
// mappings and internal per-thread state (rte_hash, rte_meter) are
// bound to the EAL init thread. Go's goroutine scheduler migrates
// goroutines between OS threads, so direct cgo calls would SIGSEGV.
//
// Pattern: a dedicated goroutine locked to one OS thread (via
// runtime.LockOSThread) calls rte_eal_init AND handles all subsequent
// C API calls received through a channel. This mirrors Python's GIL.
//
// Rebuild the C data plane with `go generate ./nf/upf/...` (or
// `make -C nf/upf/dataplane`). The Makefile writes libupf_dp.so next
// to the C sources — the single canonical path this file links and
// rpaths to; each build overwrites the previous .so in place.

//go:generate make -C dataplane
package upf

/*
#cgo CFLAGS: -I${SRCDIR}/dataplane/include
#cgo LDFLAGS: -L${SRCDIR}/dataplane -lupf_dp -L${SRCDIR}/../../libs/dpdk-25.11/build/lib -Wl,--disable-new-dtags -Wl,-rpath,${SRCDIR}/dataplane -Wl,-rpath,${SRCDIR}/../../libs/dpdk-25.11/build/lib

#include "upf_dp_api.h"
#include "upf_report.h"
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"
)

// dpdkCmd is a command sent to the DPDK dispatch goroutine.
type dpdkCmd struct {
	fn   func() error
	done chan error
}

// dpdkDispatch is the channel through which all C calls are serialized
// to the EAL init thread.
var dpdkDispatch chan dpdkCmd

// pending* are the per-rule buffers used by dpdkBridge to collapse
// the per-session establishment cgo round-trips into a single
// dispatch. docs/PERFORMANCE.md round-2 #1: each Create-* + RegisterTEID
// + RegisterUEIP call accumulates into the per-session pendingSession
// instead of immediately hitting dpdkDispatch; CommitSession then
// drains the whole structure under one EAL-thread excursion.
//
// Spec-neutral: every C entry point called inside CommitSession is
// the same one a single-shot path used to call (upf_dp_session_create,
// upf_dp_add_pdr, etc.). Only the cgo-boundary trip count changes —
// 11 → 1 for a default-bearer establishment.

type pendingPDR struct {
	pdrID                uint16
	precedence           uint32
	pdiSource, qfi       uint8
	farID, qerID, urrID  uint32
	sdfRules             string
}

type pendingFAR struct {
	farID            uint32
	action, dstIface uint8
	teid, peerAddr   uint32
	peerPort         uint16
	ohcType          uint8
}

type pendingQER struct {
	qerID                       uint32
	qfi, gateUL, gateDL         uint8
	mbrUL, mbrDL, gbrUL, gbrDL  uint64
}

type pendingURR struct {
	urrID                       uint32
	measMethod, reportTrigger   uint8
	volThreshUL, volThreshDL    uint64
	timeThresh                  uint32
}

type pendingSession struct {
	imsi       string
	pduSessID  uint8
	dnn        string
	sst        uint8
	sd, ueAddr uint32
	pdnType    uint8

	pdrs []pendingPDR
	fars []pendingFAR
	qers []pendingQER
	urrs []pendingURR

	sessionAmbrUL, sessionAmbrDL uint64
	sessionAmbrSet               bool

	teids []uint32 // RegisterTEID requests collected during establishment
	ueIPs []uint32 // RegisterUEIP requests collected during establishment
}

type dpdkBridge struct {
	pendingMu sync.Mutex
	pending   map[sessionKey]*pendingSession
}

// pendingFor returns the buffer for (imsi, id) if establishment is in
// progress, else nil. Callers that hit nil fall through to immediate
// dispatch — that path covers §7.5.4 mid-session Create-* rule adds
// (which arrive after the establishment Commit) and any direct cgo
// callers that don't go through the SessionCreate→…→CommitSession
// sequence.
func (d *dpdkBridge) pendingFor(imsi string, id uint8) *pendingSession {
	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()
	return d.pending[sessionKey{imsi, id}]
}

func init() {
	bridge = &dpdkBridge{
		pending: make(map[sessionKey]*pendingSession),
	}
}

// dispatch sends a C call to the EAL thread and waits for the result.
func dispatch(fn func() error) error {
	if dpdkDispatch == nil {
		return fn() // pre-init calls (SetMaxSessions etc.) run inline
	}
	done := make(chan error, 1)
	dpdkDispatch <- dpdkCmd{fn: fn, done: done}
	return <-done
}

func (d *dpdkBridge) Init(argc int, argv []string) error {
	// Start the dispatch goroutine. rte_eal_init() runs INSIDE it so
	// that EAL init and all subsequent C calls share the same OS thread.
	//
	// Channel capacity: large enough that concurrent control-plane
	// work (parallel registrations, parallel PFCP §7.5.x) doesn't
	// queue up against the channel send. docs/PERFORMANCE.md Run 4 (128
	// UEs) hit the previous 64-deep buffer's tail and the dispatcher
	// p95 widened. 1024 absorbs the burst from a single SCTP cascade
	// of N UEs (§7.5.6 deletion + reverse-map sweep) plus mid-call
	// signalling without serialising on the channel itself.
	dpdkDispatch = make(chan dpdkCmd, 1024)
	initDone := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		// This goroutine owns this OS thread for its entire lifetime.
		// rte_eal_init happens HERE, on this locked thread.

		cArgv := make([]*C.char, len(argv))
		for i, a := range argv {
			cArgv[i] = C.CString(a)
		}
		// Note: rte_eal_init() modifies argv in place (reorders, may
		// free entries). Do NOT free cArgv after — intentional leak of
		// a few small strings at init time.
		rc := C.upf_dp_init(C.int(argc), &cArgv[0])

		if rc != 0 {
			initDone <- fmt.Errorf("upf_dp_init failed: %d", rc)
			return
		}
		initDone <- nil

		// Dispatch loop — all subsequent C calls execute here,
		// on the same OS thread that called rte_eal_init().
		for cmd := range dpdkDispatch {
			cmd.done <- cmd.fn()
		}
	}()

	err := <-initDone
	if err != nil {
		// Dispatch goroutine has exited without entering the loop, so
		// any later dispatch(fn) would buffer into dpdkDispatch and then
		// block forever on <-done. Reset to nil so dispatch() takes the
		// inline fn() path. Without this the PFCP handler deadlocks at
		// CommitSession and the SMF trips its TS 29.244 §7.6.2 T1/N1
		// retransmit budget (default 3s × 4).
		dpdkDispatch = nil
	}
	return err
}

func (d *dpdkBridge) Cleanup() {
	dispatch(func() error { C.upf_dp_cleanup(); return nil })
	if dpdkDispatch != nil {
		close(dpdkDispatch)
		dpdkDispatch = nil
	}
}

func (d *dpdkBridge) SetMaxSessions(n uint32) error {
	if C.upf_dp_set_max_sessions(C.uint32_t(n)) != 0 {
		return fmt.Errorf("upf_dp_set_max_sessions failed")
	}
	return nil
}

func (d *dpdkBridge) SetPMDTuning(mbufPoolSize uint32, rxRingSize, txRingSize uint16) error {
	if C.upf_dp_set_pmd_tuning(C.uint32_t(mbufPoolSize), C.uint16_t(rxRingSize), C.uint16_t(txRingSize)) != 0 {
		return fmt.Errorf("upf_dp_set_pmd_tuning failed")
	}
	return nil
}

// CommitSession finalises the buffered establishment by running every
// C entry point in sequence under ONE dispatch — single EAL-thread
// excursion drains the whole pendingSession structure (session_create
// → add_far → add_urr → add_qer → add_pdr → set_session_ambr →
// register_teid → register_ueip). For a default-bearer this collapses
// 11 cgo round-trips into 1.
//
// docs/PERFORMANCE.md round-2 #1: Run 6 (128 UEs post-round-1) showed PFCP
// §7.5.2 p50 widening to 770 ms because the 1024-deep dispatch channel
// removed the back-pressure that previously rate-limited inflow. The
// structural fix is to stop sending 11 trips/session in the first
// place. Each individual C call is microseconds; the dominating cost
// was the goroutine wakeup + channel hop per call.
//
// Idempotent if no pending entry exists (e.g., test paths that don't
// touch SessionCreate). Errors abort the install and best-effort-
// delete the partially-installed session via upf_dp_session_delete
// to avoid orphan slots in the C session_pool.
func (d *dpdkBridge) CommitSession(imsi string, pduSessionID uint8) error {
	d.pendingMu.Lock()
	s := d.pending[sessionKey{imsi, pduSessionID}]
	delete(d.pending, sessionKey{imsi, pduSessionID})
	d.pendingMu.Unlock()
	if s == nil {
		return nil // no buffered establishment — nothing to flush
	}

	return dispatch(func() error {
		cIMSI := C.CString(s.imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		cDNN := C.CString(s.dnn)
		defer C.free(unsafe.Pointer(cDNN))

		// 1. Create the session. session_create allocates the
		//    session_pool slot + meter slots; everything below
		//    addresses that slot via (imsi, pduSessID).
		if C.upf_dp_session_create(cIMSI, C.uint8_t(s.pduSessID), cDNN,
			C.uint8_t(s.sst), C.uint32_t(s.sd), C.uint32_t(s.ueAddr)) == nil {
			return fmt.Errorf("upf_dp_session_create returned NULL")
		}

		// rollback runs upf_dp_session_delete on any error past
		// session_create — keeps the C session_pool clean even if
		// a downstream rule install fails.
		rollback := func(reason error) error {
			C.upf_dp_session_delete(cIMSI, C.uint8_t(s.pduSessID))
			return reason
		}

		// 2. FARs first — PDRs reference far_id, so install order
		//    matters at the C side (find_far walks sess->far[]).
		for i := range s.fars {
			f := &s.fars[i]
			if C.upf_dp_add_far(cIMSI, C.uint8_t(s.pduSessID),
				C.uint32_t(f.farID), C.uint8_t(f.action), C.uint8_t(f.dstIface),
				C.uint32_t(f.teid), C.uint32_t(f.peerAddr), C.uint16_t(f.peerPort),
				C.uint8_t(f.ohcType)) != 0 {
				return rollback(fmt.Errorf("upf_dp_add_far farID=%d failed", f.farID))
			}
		}

		// 3. URRs — referenced by PDRs' urr_id.
		for i := range s.urrs {
			u := &s.urrs[i]
			if C.upf_dp_add_urr(cIMSI, C.uint8_t(s.pduSessID), C.uint32_t(u.urrID),
				C.uint8_t(u.measMethod), C.uint8_t(u.reportTrigger),
				C.uint64_t(u.volThreshUL), C.uint64_t(u.volThreshDL),
				C.uint32_t(u.timeThresh)) != 0 {
				return rollback(fmt.Errorf("upf_dp_add_urr urrID=%d failed", u.urrID))
			}
		}

		// 4. QERs — referenced by PDRs' qer_id.
		for i := range s.qers {
			q := &s.qers[i]
			if C.upf_dp_add_qer(cIMSI, C.uint8_t(s.pduSessID), C.uint32_t(q.qerID),
				C.uint8_t(q.qfi), C.uint8_t(q.gateUL), C.uint8_t(q.gateDL),
				C.uint64_t(q.mbrUL), C.uint64_t(q.mbrDL),
				C.uint64_t(q.gbrUL), C.uint64_t(q.gbrDL)) != 0 {
				return rollback(fmt.Errorf("upf_dp_add_qer qerID=%d failed", q.qerID))
			}
		}

		// 5. PDRs — depend on FAR / URR / QER existing.
		for i := range s.pdrs {
			p := &s.pdrs[i]
			var cSDF *C.char
			if p.sdfRules != "" {
				cSDF = C.CString(p.sdfRules)
			}
			rc := C.upf_dp_add_pdr(cIMSI, C.uint8_t(s.pduSessID),
				C.uint16_t(p.pdrID), C.uint32_t(p.precedence),
				C.uint8_t(p.pdiSource), C.uint8_t(p.qfi),
				C.uint32_t(p.farID), C.uint32_t(p.qerID), C.uint32_t(p.urrID),
				cSDF)
			if cSDF != nil {
				C.free(unsafe.Pointer(cSDF))
			}
			if rc != 0 {
				return rollback(fmt.Errorf("upf_dp_add_pdr pdrID=%d failed", p.pdrID))
			}
		}

		// 6. Session-AMBR (TS 23.501 §5.7.1.6) — also reconfigures
		//    the per-session rte_meter token bucket on the C side.
		if s.sessionAmbrSet {
			if C.upf_dp_set_session_ambr(cIMSI, C.uint8_t(s.pduSessID),
				C.uint64_t(s.sessionAmbrUL), C.uint64_t(s.sessionAmbrDL)) != 0 {
				return rollback(fmt.Errorf("upf_dp_set_session_ambr failed"))
			}
		}

		// 7. Reverse-map registration. TEID + UE-IP keys go into the
		//    teid_hash / ueip_hash for fast UL/DL classification per
		//    TS 29.244 §8.2.3 / §8.2.62. Failures are logged but
		//    don't roll back — the session's PDRs may still match
		//    via SDF without the fast-path key.
		for _, t := range s.teids {
			if C.upf_dp_register_teid(C.uint32_t(t), cIMSI, C.uint8_t(s.pduSessID)) != 0 {
				// Best-effort: swallow and continue (matches the
				// non-batched RegisterTEID path which logs at the
				// caller).
			}
		}
		for _, ip := range s.ueIPs {
			if C.upf_dp_register_ueip(C.uint32_t(ip), cIMSI, C.uint8_t(s.pduSessID)) != 0 {
				// Best-effort.
			}
		}
		return nil
	})
}

// SessionCreate ALWAYS opens a buffer for (imsi, id) — it doesn't
// dispatch to the C side. The actual upf_dp_session_create runs at
// CommitSession time. Every subsequent Add* / Register* call between
// SessionCreate and CommitSession appends to the buffer; failure to
// CommitSession leaves an orphan in d.pending which a follow-up
// SessionDelete cleans up.
func (d *dpdkBridge) SessionCreate(imsi string, pduSessionID uint8, dnn string, sst uint8, sd, ueAddr uint32, pdnType uint8) error {
	_ = pdnType // C dataplane currently treats the session as IPv4 — PDN Type passed through to PFCP only.
	d.pendingMu.Lock()
	d.pending[sessionKey{imsi, pduSessionID}] = &pendingSession{
		imsi:      imsi,
		pduSessID: pduSessionID,
		dnn:       dnn,
		sst:       sst,
		sd:        sd,
		ueAddr:    ueAddr,
		pdnType:   pdnType,
	}
	d.pendingMu.Unlock()
	return nil
}

// SessionDelete tears down both the pending buffer (if establishment
// was in flight but never committed) AND the C-side session (if it
// was committed). Either path is safe — pending-only delete needs no
// cgo trip; C-side delete dispatches normally.
func (d *dpdkBridge) SessionDelete(imsi string, pduSessionID uint8) error {
	d.pendingMu.Lock()
	_, hadPending := d.pending[sessionKey{imsi, pduSessionID}]
	delete(d.pending, sessionKey{imsi, pduSessionID})
	d.pendingMu.Unlock()
	if hadPending {
		// Establishment was buffered but never committed — there's
		// no C state to release. Drop and we're done.
		return nil
	}
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_session_delete(cIMSI, C.uint8_t(pduSessionID)) != 0 {
			return fmt.Errorf("upf_dp_session_delete failed")
		}
		return nil
	})
}

// AddPDR — buffers when an establishment is in flight (between
// SessionCreate and CommitSession); falls through to immediate cgo
// dispatch otherwise (covers §7.5.4 mid-session Create-PDR which
// arrives after the establishment Commit).
func (d *dpdkBridge) AddPDR(imsi string, pduSessionID uint8, pdrID uint16, precedence uint32,
	pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
	ueIPv4, teid, n3IPv4 uint32) error {
	if s := d.pendingFor(imsi, pduSessionID); s != nil {
		s.pdrs = append(s.pdrs, pendingPDR{
			pdrID: pdrID, precedence: precedence,
			pdiSource: pdiSource, qfi: qfi,
			farID: farID, qerID: qerID, urrID: urrID,
			sdfRules: sdfRules,
		})
		_ = ueIPv4 // PDI fast-path keys are registered separately
		_ = teid
		_ = n3IPv4
		return nil
	}
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		var cSDF *C.char
		if sdfRules != "" {
			cSDF = C.CString(sdfRules)
			defer C.free(unsafe.Pointer(cSDF))
		}
		if C.upf_dp_add_pdr(cIMSI, C.uint8_t(pduSessionID), C.uint16_t(pdrID),
			C.uint32_t(precedence), C.uint8_t(pdiSource), C.uint8_t(qfi),
			C.uint32_t(farID), C.uint32_t(qerID), C.uint32_t(urrID), cSDF) != 0 {
			return fmt.Errorf("upf_dp_add_pdr failed")
		}
		_ = ueIPv4
		_ = teid
		_ = n3IPv4
		return nil
	})
}

// AddFAR — same buffer-or-fallback pattern as AddPDR.
func (d *dpdkBridge) AddFAR(imsi string, pduSessionID uint8, farID uint32, action, dstIface uint8,
	teid, peerAddr uint32, peerPort uint16, ohcType uint8) error {
	if s := d.pendingFor(imsi, pduSessionID); s != nil {
		s.fars = append(s.fars, pendingFAR{
			farID: farID, action: action, dstIface: dstIface,
			teid: teid, peerAddr: peerAddr, peerPort: peerPort,
			ohcType: ohcType,
		})
		return nil
	}
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_add_far(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(farID),
			C.uint8_t(action), C.uint8_t(dstIface),
			C.uint32_t(teid), C.uint32_t(peerAddr), C.uint16_t(peerPort), C.uint8_t(ohcType)) != 0 {
			return fmt.Errorf("upf_dp_add_far failed")
		}
		return nil
	})
}

func (d *dpdkBridge) UpdateFAR(imsi string, pduSessionID uint8, farID, teid, peerAddr uint32, peerPort uint16) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_update_far(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(farID),
			C.uint32_t(teid), C.uint32_t(peerAddr), C.uint16_t(peerPort)) != 0 {
			return fmt.Errorf("upf_dp_update_far failed")
		}
		return nil
	})
}

// UpdatePDR — TS 29.244 v19.5.0 §7.5.4.2. Wholesale replace via the
// idempotent Add path on the C side; rule MUST already exist.
func (d *dpdkBridge) UpdatePDR(imsi string, pduSessionID uint8, pdrID uint16,
	precedence uint32, pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
	ueIPv4, teid, n3IPv4 uint32) error {
	_ = ueIPv4 // C side stores ue_addr from session, not per-update
	_ = teid
	_ = n3IPv4
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		var cSDF *C.char
		if sdfRules != "" {
			cSDF = C.CString(sdfRules)
			defer C.free(unsafe.Pointer(cSDF))
		}
		if C.upf_dp_update_pdr(cIMSI, C.uint8_t(pduSessionID), C.uint16_t(pdrID),
			C.uint32_t(precedence), C.uint8_t(pdiSource), C.uint8_t(qfi),
			C.uint32_t(farID), C.uint32_t(qerID), C.uint32_t(urrID), cSDF) != 0 {
			return fmt.Errorf("upf_dp_update_pdr failed")
		}
		return nil
	})
}

// UpdateQER — TS 29.244 v19.5.0 §7.5.4.5.
func (d *dpdkBridge) UpdateQER(imsi string, pduSessionID uint8, qerID uint32,
	qfi, gateUL, gateDL uint8, mbrUL, mbrDL, gbrUL, gbrDL uint64) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_update_qer(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(qerID),
			C.uint8_t(qfi), C.uint8_t(gateUL), C.uint8_t(gateDL),
			C.uint64_t(mbrUL), C.uint64_t(mbrDL), C.uint64_t(gbrUL), C.uint64_t(gbrDL)) != 0 {
			return fmt.Errorf("upf_dp_update_qer failed")
		}
		return nil
	})
}

// UpdateURR — TS 29.244 v19.5.0 §7.5.4.4.
func (d *dpdkBridge) UpdateURR(imsi string, pduSessionID uint8, urrID uint32,
	measMethod, reportTrigger uint8, volThreshUL, volThreshDL uint64, timeThresh uint32) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_update_urr(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(urrID),
			C.uint8_t(measMethod), C.uint8_t(reportTrigger),
			C.uint64_t(volThreshUL), C.uint64_t(volThreshDL), C.uint32_t(timeThresh)) != 0 {
			return fmt.Errorf("upf_dp_update_urr failed")
		}
		return nil
	})
}

func (d *dpdkBridge) DeactivateDLFAR(imsi string, pduSessionID uint8, farID uint32) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_deactivate_dl(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(farID)) != 0 {
			return fmt.Errorf("upf_dp_deactivate_dl failed")
		}
		return nil
	})
}

// RemovePDR / RemoveFAR / RemoveQER / RemoveURR — TS 29.244 v19.5.0
// §7.5.4.6 / .7 / .8 / .9. C side flips active=false on the matching
// rule. Returns nil for "not found" (idempotent against retransmits
// of the §7.5.4 Modification Request); only logs warnings on
// dataplane errors that aren't "rule absent".
func (d *dpdkBridge) RemovePDR(imsi string, pduSessionID uint8, pdrID uint16) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		// -1 from C is the "no such rule" path; treat as no-op.
		C.upf_dp_remove_pdr(cIMSI, C.uint8_t(pduSessionID), C.uint16_t(pdrID))
		return nil
	})
}

func (d *dpdkBridge) RemoveFAR(imsi string, pduSessionID uint8, farID uint32) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		C.upf_dp_remove_far(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(farID))
		return nil
	})
}

func (d *dpdkBridge) RemoveQER(imsi string, pduSessionID uint8, qerID uint32) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		C.upf_dp_remove_qer(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(qerID))
		return nil
	})
}

func (d *dpdkBridge) RemoveURR(imsi string, pduSessionID uint8, urrID uint32) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		C.upf_dp_remove_urr(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(urrID))
		return nil
	})
}

// DrainReports — TS 29.244 §7.5.8 PFCP Session Report Request pull
// path. Scaffolded: the C rte_ring implementation (upf_report_init /
// _enqueue / _drain in upf_report.c) hasn't been wired into the
// dataplane yet. Returns 0 until the hot-path producers land. When
// wired, this will call into upf_report_drain() with the record
// union defined in dataplane/include/upf_report.h and marshal each
// record into a Go Report{}.
//
// TODO(spec: TS 29.244 §7.5.8 C rte_ring wiring) —
//   - implement upf_report.c with rte_ring_create + SC/MC helpers
//   - producers: upf_pkt_io.c DLDR enqueue on first-buffer; URR
//     trigger in upf_qos_meter.c for Usage; GTP-U rx in upf_gtpu.c
//     for ErrInd
//   - this function uses unsafe.Pointer + C.upf_report_drain +
//     reads out up to len(buf) records per call
//   - Go side: marshal to Go Report struct, respect trigger bits
//     (§8.2.41) and measurement IEs (§8.2.20/§8.2.21).
func (d *dpdkBridge) DrainReports(buf []Report) int {
	if len(buf) == 0 {
		return 0
	}
	// Batch into stack-sized C scratch buffer to avoid one cgo
	// call per record. 64 is the same batch the consumeLoop uses
	// (nf/upf/report.go batchSize) so we never need a second
	// round-trip for a typical drain.
	const cbatch = 64
	var crpt [cbatch]C.upf_report_t
	want := len(buf)
	if want > cbatch {
		want = cbatch
	}
	got := int(C.upf_report_drain(&crpt[0], C.unsigned(want)))
	for i := 0; i < got; i++ {
		buf[i] = Report{
			Type:         ReportType(crpt[i]._type),
			PDUSessionID: uint8(crpt[i].pdu_session_id),
			IMSI:         C.GoString(&crpt[i].imsi[0]),
			SEID:         uint64(crpt[i].seid),
		}
		switch buf[i].Type {
		case ReportDLDR:
			// Mirror upf_dldr_payload_t. `u` is a C union; we
			// read the first bytes as the DLDR struct because
			// the type tag already discriminates.
			dldr := (*C.upf_dldr_payload_t)(unsafe.Pointer(&crpt[i].u[0]))
			buf[i].DLDR = &DLDRPayload{
				QFI:  uint8(dldr.qfi),
				DSCP: uint8(dldr.dscp),
			}
		}
	}
	return got
}

func (d *dpdkBridge) ReportsDropped() uint64 {
	return uint64(C.upf_report_dropped())
}

// AddQER — buffer-or-fallback (see AddPDR).
func (d *dpdkBridge) AddQER(imsi string, pduSessionID uint8, qerID uint32, qfi, gateUL, gateDL uint8,
	mbrUL, mbrDL, gbrUL, gbrDL uint64) error {
	if s := d.pendingFor(imsi, pduSessionID); s != nil {
		s.qers = append(s.qers, pendingQER{
			qerID: qerID, qfi: qfi,
			gateUL: gateUL, gateDL: gateDL,
			mbrUL: mbrUL, mbrDL: mbrDL,
			gbrUL: gbrUL, gbrDL: gbrDL,
		})
		return nil
	}
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_add_qer(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(qerID),
			C.uint8_t(qfi), C.uint8_t(gateUL), C.uint8_t(gateDL),
			C.uint64_t(mbrUL), C.uint64_t(mbrDL), C.uint64_t(gbrUL), C.uint64_t(gbrDL)) != 0 {
			return fmt.Errorf("upf_dp_add_qer failed")
		}
		return nil
	})
}

// AddURR — buffer-or-fallback.
func (d *dpdkBridge) AddURR(imsi string, pduSessionID uint8, urrID uint32, measMethod, reportTrigger uint8,
	volThreshUL, volThreshDL uint64, timeThresh uint32) error {
	if s := d.pendingFor(imsi, pduSessionID); s != nil {
		s.urrs = append(s.urrs, pendingURR{
			urrID: urrID,
			measMethod: measMethod, reportTrigger: reportTrigger,
			volThreshUL: volThreshUL, volThreshDL: volThreshDL,
			timeThresh: timeThresh,
		})
		return nil
	}
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_add_urr(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(urrID),
			C.uint8_t(measMethod), C.uint8_t(reportTrigger),
			C.uint64_t(volThreshUL), C.uint64_t(volThreshDL), C.uint32_t(timeThresh)) != 0 {
			return fmt.Errorf("upf_dp_add_urr failed")
		}
		return nil
	})
}

// SetSessionAMBR — buffer-or-fallback. Reconfigures the per-session
// rte_meter on the C side at install time.
func (d *dpdkBridge) SetSessionAMBR(imsi string, pduSessionID uint8, ambrUL, ambrDL uint64) error {
	if s := d.pendingFor(imsi, pduSessionID); s != nil {
		s.sessionAmbrUL = ambrUL
		s.sessionAmbrDL = ambrDL
		s.sessionAmbrSet = true
		return nil
	}
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_set_session_ambr(cIMSI, C.uint8_t(pduSessionID),
			C.uint64_t(ambrUL), C.uint64_t(ambrDL)) != 0 {
			return fmt.Errorf("upf_dp_set_session_ambr failed")
		}
		return nil
	})
}

func (d *dpdkBridge) PktIOInit(n3Addr string, n3Port uint16, tunName, tunAddr string) error {
	return dispatch(func() error {
		var cN3, cTun, cTunAddr *C.char
		if n3Addr != "" {
			cN3 = C.CString(n3Addr)
			defer C.free(unsafe.Pointer(cN3))
		}
		if tunName != "" {
			cTun = C.CString(tunName)
			defer C.free(unsafe.Pointer(cTun))
		}
		if tunAddr != "" {
			cTunAddr = C.CString(tunAddr)
			defer C.free(unsafe.Pointer(cTunAddr))
		}
		if C.upf_dp_pkt_io_init(cN3, C.uint16_t(n3Port), cTun, cTunAddr) != 0 {
			return fmt.Errorf("upf_dp_pkt_io_init failed")
		}
		return nil
	})
}

func (d *dpdkBridge) PktIORun() error {
	// PktIORun blocks in the C packet processing loop. It runs on its
	// own OS-thread-locked goroutine. It does NOT go through dispatch
	// because it would block the dispatcher forever.
	runtime.LockOSThread()
	rc := C.upf_dp_pkt_io_run()
	if rc != 0 {
		return fmt.Errorf("upf_dp_pkt_io_run exited: %d", rc)
	}
	return nil
}

func (d *dpdkBridge) PktIOStop() {
	C.upf_dp_pkt_io_stop()
}

// RegisterTEID — buffer-or-fallback. Buffered TEID registrations
// drain at CommitSession after every PDR/FAR/QER/URR is in place.
func (d *dpdkBridge) RegisterTEID(teid uint32, imsi string, pduSessionID uint8) error {
	if s := d.pendingFor(imsi, pduSessionID); s != nil {
		s.teids = append(s.teids, teid)
		return nil
	}
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_register_teid(C.uint32_t(teid), cIMSI, C.uint8_t(pduSessionID)) != 0 {
			return fmt.Errorf("upf_dp_register_teid failed")
		}
		return nil
	})
}

// RegisterUEIP — buffer-or-fallback.
func (d *dpdkBridge) RegisterUEIP(ueAddr uint32, imsi string, pduSessionID uint8) error {
	if s := d.pendingFor(imsi, pduSessionID); s != nil {
		s.ueIPs = append(s.ueIPs, ueAddr)
		return nil
	}
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		if C.upf_dp_register_ueip(C.uint32_t(ueAddr), cIMSI, C.uint8_t(pduSessionID)) != 0 {
			return fmt.Errorf("upf_dp_register_ueip failed")
		}
		return nil
	})
}

// UnregisterTEID releases the TEID reverse-map slot installed by a
// prior RegisterTEID. Idempotent: a missing key returns nil.
//
// Per TS 29.244 v19.5.0 §7.5.6 PFCP Session Deletion, the UP function
// must release every F-TEID it allocated for the session under §5.5.1.
// The C side runs the actual rte_hash_del_key + deferred
// rte_hash_free_key_with_position so RW_CONCURRENCY_LF readers stay
// safe across reuse.
func (d *dpdkBridge) UnregisterTEID(teid uint32) error {
	return dispatch(func() error {
		// -1 from C is the "key not present" path — treat as no-op,
		// not an error. The Handler may call us for TEIDs that were
		// never installed (e.g., DL-only PDR with no F-TEID).
		C.upf_dp_unregister_teid(C.uint32_t(teid))
		return nil
	})
}

// UnregisterUEIP releases the UE-IP reverse-map slot installed by a
// prior RegisterUEIP. Idempotent.
//
// Per TS 29.244 v19.5.0 §7.5.6 + §8.2.62 (UE IP Address) — the IP
// allocated by the UP function for a session must be released on
// deletion so a subsequent §7.5.2 with the same/different UE IP
// doesn't compete for an exhausted ueip_hash.
func (d *dpdkBridge) UnregisterUEIP(ueAddr uint32) error {
	return dispatch(func() error {
		C.upf_dp_unregister_ueip(C.uint32_t(ueAddr))
		return nil
	})
}

// UnregisterSessionKeys batches the §7.5.6 reverse-map release of
// every (TEID, UE-IP) pair the session installed under §7.5.2 /
// §7.5.4 Create PDR. One cgo round-trip walks both arrays at the
// EAL thread; this is the structural fix for the cascade super-
// linearity docs/PERFORMANCE.md Run 4 surfaced (33 ms → 197 ms going
// 64→128 UEs because every PDR-key release was a separate dispatch).
//
// Empty inputs are valid (returns 0 released). Spec ground:
// TS 29.244 v19.5.0 §7.5.6 deletion + §5.5.1 F-TEID release + §8.2.62
// UE IP release — the wire-side semantics don't change with batching.
func (d *dpdkBridge) UnregisterSessionKeys(teids []uint32, ueips []uint32) (int, error) {
	if len(teids) == 0 && len(ueips) == 0 {
		return 0, nil
	}
	var released int
	err := dispatch(func() error {
		var teidPtr *C.uint32_t
		var ueipPtr *C.uint32_t
		if len(teids) > 0 {
			teidPtr = (*C.uint32_t)(unsafe.Pointer(&teids[0]))
		}
		if len(ueips) > 0 {
			ueipPtr = (*C.uint32_t)(unsafe.Pointer(&ueips[0]))
		}
		released = int(C.upf_dp_unregister_batch(
			teidPtr, C.int(len(teids)),
			ueipPtr, C.int(len(ueips))))
		return nil
	})
	return released, err
}

func (d *dpdkBridge) GetURRStats(imsi string, pduSessionID uint8, urrID uint32) (volUL, volDL, pktUL, pktDL uint64, err error) {
	err = dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		var cVolUL, cVolDL, cPktUL, cPktDL C.uint64_t
		if C.upf_dp_get_urr_stats(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(urrID),
			&cVolUL, &cVolDL, &cPktUL, &cPktDL) != 0 {
			return fmt.Errorf("upf_dp_get_urr_stats failed")
		}
		volUL = uint64(cVolUL)
		volDL = uint64(cVolDL)
		pktUL = uint64(cPktUL)
		pktDL = uint64(cPktDL)
		return nil
	})
	return
}

func (d *dpdkBridge) GetQERStats(imsi string, pduSessionID uint8, qerID uint32) (dropPktsUL, dropPktsDL, dropBytesUL, dropBytesDL uint64, err error) {
	err = dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		var cPU, cPD, cBU, cBD C.uint64_t
		if C.upf_dp_get_qer_stats(cIMSI, C.uint8_t(pduSessionID), C.uint32_t(qerID),
			&cPU, &cPD, &cBU, &cBD) != 0 {
			return fmt.Errorf("upf_dp_get_qer_stats failed")
		}
		dropPktsUL = uint64(cPU)
		dropPktsDL = uint64(cPD)
		dropBytesUL = uint64(cBU)
		dropBytesDL = uint64(cBD)
		return nil
	})
	return
}

func (d *dpdkBridge) GetIOStats() IOStats {
	var stats IOStats
	dispatch(func() error {
		var cs C.upf_dp_io_stats_t
		C.upf_dp_get_io_stats(&cs)
		stats = IOStats{
			ULPkts:     uint64(cs.ul_pkts),
			ULBytes:    uint64(cs.ul_bytes),
			DLPkts:     uint64(cs.dl_pkts),
			DLBytes:    uint64(cs.dl_bytes),
			ULDropped:  uint64(cs.ul_dropped),
			DLDropped:  uint64(cs.dl_dropped),
			ULNoSess:   uint64(cs.ul_no_session),
			DLNoSess:   uint64(cs.dl_no_session),
			ULMetered:  uint64(cs.ul_metered),
			DLMetered:  uint64(cs.dl_metered),
			GTPUErrors: uint64(cs.gtpu_errors),
		}
		return nil
	})
	return stats
}

func (d *dpdkBridge) SessionCount() uint32 {
	var n uint32
	dispatch(func() error {
		n = uint32(C.upf_dp_session_count())
		return nil
	})
	return n
}

func (d *dpdkBridge) SliceInit(sliceID, sst uint8, name string) error {
	return dispatch(func() error {
		cName := C.CString(name)
		defer C.free(unsafe.Pointer(cName))
		if C.upf_dp_slice_init(C.uint8_t(sliceID), C.uint8_t(sst), cName) != 0 {
			return fmt.Errorf("upf_dp_slice_init failed")
		}
		return nil
	})
}

func (d *dpdkBridge) SliceDestroy(sliceID uint8) {
	dispatch(func() error {
		C.upf_dp_slice_destroy(C.uint8_t(sliceID))
		return nil
	})
}

func (d *dpdkBridge) SliceSessionCreate(sliceID uint8, imsi string, pduSessionID uint8,
	dnn string, sst uint8, sd, ueAddr uint32) error {
	return dispatch(func() error {
		cIMSI := C.CString(imsi)
		defer C.free(unsafe.Pointer(cIMSI))
		cDNN := C.CString(dnn)
		defer C.free(unsafe.Pointer(cDNN))
		sess := C.upf_dp_slice_session_create(C.uint8_t(sliceID), cIMSI, C.uint8_t(pduSessionID),
			cDNN, C.uint8_t(sst), C.uint32_t(sd), C.uint32_t(ueAddr))
		if sess == nil {
			return fmt.Errorf("upf_dp_slice_session_create returned NULL")
		}
		return nil
	})
}

// ApplyModifyBatch — cgoBridge has no wire round-trip to coalesce
// (every Add/Update/Remove is a direct cgo call into libupf_dp.so),
// so iterate via existing per-IE methods. Spec semantics are
// identical to a single PFCP §7.5.4 Modification with the same IE
// set; this exists to satisfy the UPFBridge interface and keep the
// integrated-PFCP path's bridgeHook a straight pass-through.
func (d *dpdkBridge) ApplyModifyBatch(imsi string, pduSessionID uint8, batch ModifyBatch) error {
	for _, p := range batch.CreatePDRs {
		if err := d.AddPDR(imsi, pduSessionID, p.PDRID, p.Precedence,
			p.PDISource, p.QFI, p.FARID, p.QERID, p.URRID, p.SDFRules,
			p.UEIPv4, p.TEID, p.N3IPv4); err != nil {
			return err
		}
	}
	for _, f := range batch.CreateFARs {
		if err := d.AddFAR(imsi, pduSessionID, f.FARID, f.Action,
			f.DstIface, f.TEID, f.PeerAddr, f.PeerPort, 0); err != nil {
			return err
		}
	}
	for _, q := range batch.CreateQERs {
		if err := d.AddQER(imsi, pduSessionID, q.QERID, q.QFI,
			q.GateUL, q.GateDL, q.MBRUL, q.MBRDL, q.GBRUL, q.GBRDL); err != nil {
			return err
		}
	}
	for _, u := range batch.CreateURRs {
		if err := d.AddURR(imsi, pduSessionID, u.URRID, u.MeasMethod,
			u.ReportTrigger, u.VolThreshUL, u.VolThreshDL, u.TimeThresh); err != nil {
			return err
		}
	}
	for _, uf := range batch.UpdateFARs {
		if err := d.UpdateFAR(imsi, pduSessionID, uf.FARID, uf.TEID,
			uf.PeerAddr, uf.PeerPort); err != nil {
			return err
		}
	}
	for _, id := range batch.RemovePDRs {
		if err := d.RemovePDR(imsi, pduSessionID, id); err != nil {
			return err
		}
	}
	for _, id := range batch.RemoveFARs {
		if err := d.RemoveFAR(imsi, pduSessionID, id); err != nil {
			return err
		}
	}
	for _, id := range batch.RemoveQERs {
		if err := d.RemoveQER(imsi, pduSessionID, id); err != nil {
			return err
		}
	}
	for _, id := range batch.RemoveURRs {
		if err := d.RemoveURR(imsi, pduSessionID, id); err != nil {
			return err
		}
	}
	if batch.SessionAMBR != nil {
		if err := d.SetSessionAMBR(imsi, pduSessionID,
			batch.SessionAMBR.UL, batch.SessionAMBR.DL); err != nil {
			return err
		}
	}
	return nil
}

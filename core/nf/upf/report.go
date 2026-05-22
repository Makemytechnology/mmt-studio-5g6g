// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// report.go — extensible UPF → SMF report framework, Go side.
//
// Authoritative spec: TS 29.244 §7.5.8 "PFCP Session Report Request"
// (PDF: specs/3gpp/ts_129244v190500p.pdf).
//
// Mirrors the C-side scaffold at nf/upf/dataplane/include/upf_report.h.
// Every §7.5.8.x report type (DLDR, Usage, Error Indication, TSC,
// generic Session Report) crosses the cgo boundary with the same
// shape: a tagged record identifying the session plus a type-
// specific payload. One goroutine consumes them, dispatches by
// type to registered handlers.
//
// Design decisions captured here (not on the hot path):
//   - MPMC rte_ring on the C side so DPDK lcore producers are
//     lockless. Details in upf_report.h header comment.
//   - No cgo callbacks (//export) — DPDK lcore → Go runtime is
//     fragile; prefer async hand-off.
//   - Overflow = drop + counter increment. Hot path never blocks.
//   - Go consumer drains in batches, sleeps when empty.
//
// Extensibility recipe for a new §7.5.8.x report type:
//  1. Add enum upf_report_type value in upf_report.h
//  2. Add payload struct to the C union + to the Go Report{} here.
//  3. Add the dispatch case in consume().
//  4. Register a handler from the consumer package (SMF / CHF /
//     paging / ...).
//
// Current consumers:
//   - ReportDLDR  → nf/smf/session.HandleDLDataNotification
//     (wired in this file's init)
//
// Planned consumers:
//   - ReportUsage → CHF (Nchf_ConvergentCharging) for volume /
//     time / event thresholds (§8.2.41 Usage Report Trigger).
//   - ReportErrInd → session reset / teardown per TS 29.281 §7.3
//     GTP-U Error Indication.
package upf

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// ReportType is the discriminator in the tagged Report record. The
// integer values MUST match the enum upf_report_type in
// dataplane/include/upf_report.h. Stable — append new types, never
// renumber.
type ReportType uint8

const (
	ReportNone    ReportType = 0
	ReportDLDR    ReportType = 1 // §7.5.8.2 Downlink Data Report
	ReportUsage   ReportType = 2 // §7.5.8.3 Usage Report (URR / charging)
	ReportErrInd  ReportType = 3 // §7.5.8.4 Error Indication Report
	ReportTSC     ReportType = 4 // §7.5.8.5 TSC Management Information
	ReportSessRep ReportType = 5 // §7.5.8.6 generic Session Report
)

// String renders the report type for log output.
func (t ReportType) String() string {
	switch t {
	case ReportDLDR:
		return "DLDR"
	case ReportUsage:
		return "Usage"
	case ReportErrInd:
		return "ErrInd"
	case ReportTSC:
		return "TSC"
	case ReportSessRep:
		return "SessRep"
	}
	return "None"
}

// DLDRPayload mirrors the C-side upf_dldr_payload_t. Carries just
// enough for the SMF to route §4.2.3.3 step 3a: the QFI identifies
// the affected QoS flow, DSCP drives the Paging Policy Indicator
// per TS 23.501 §5.4.3.
type DLDRPayload struct {
	QFI  uint8
	DSCP uint8
}

// UsagePayload mirrors upf_usage_payload_t. TS 29.244 §8.2.41
// Usage Report Trigger bitmap + §8.2.20 volume measurements +
// §8.2.21 duration. Consumed by CHF (TS 32.255 5G Charging) when
// charging integration lands.
//
// Trigger bit names (from §8.2.41):
//
//	bit 0  PERIO   periodic report
//	bit 1  VOLTH   volume threshold
//	bit 2  TIMTH   time threshold
//	bit 3  QUHTI   quota-holding time
//	bit 4  START   traffic started
//	bit 5  STOPT   traffic stopped
//	bit 6  DROTH   dropped DL packets threshold
//	bit 7  LIUSA   linked usage
//	bit 8  VOLQU   volume quota
//	bit 9  TIMQU   time quota
//	bit 10 EVETH   event threshold
//	bit 11 MACAR   MAC address reporting
//	bit 12 ENVCL   envelope close
//	bit 13 MONIT   monitoring time
//	bit 14 TERMR   termination
//	bit 15 EVEQU   event quota
//	(see §8.2.41 table for the full list)
type UsagePayload struct {
	URRID       uint32
	Trigger     uint32
	VolUL       uint64 // uplink bytes
	VolDL       uint64 // downlink bytes
	PktUL       uint64
	PktDL       uint64
	DurationSec uint32
}

// ErrIndPayload mirrors upf_errind_payload_t. GTP-U Error Indication
// per TS 29.281 §7.3 — the remote peer told us our TEID is stale.
type ErrIndPayload struct {
	RemoteTEID uint32
	RemoteAddr uint32 // host byte order
	RemotePort uint16
}

// TSCPayload is a scaffold for §7.5.8.5 TSC Management Information.
// Expand when TSN / TSC integration lands.
type TSCPayload struct {
	EventID uint32
}

// SessRepPayload is the generic §7.5.8.6 slot.
type SessRepPayload struct {
	ReportCode uint32
}

// Report is the unified record type. Exactly one of the *Payload
// pointers is non-nil, selected by Type.
type Report struct {
	Type         ReportType
	IMSI         string
	PDUSessionID uint8
	SEID         uint64
	Timestamp    time.Time

	DLDR    *DLDRPayload
	Usage   *UsagePayload
	ErrInd  *ErrIndPayload
	TSC     *TSCPayload
	SessRep *SessRepPayload
}

// ReportHandler processes one report. Handlers run serially on the
// consumer goroutine — long-running work should spawn its own
// goroutine. nil-safe: dispatch skips unregistered types.
type ReportHandler func(*Report)

var (
	handlersMu sync.RWMutex
	handlers   = map[ReportType]ReportHandler{}

	// consumerActive guards against double-start and allows tests
	// to assert the consumer was spun up.
	consumerActive atomic.Bool

	// reportDrops counts reports the Go consumer explicitly ignored
	// (handler missing / session gone / decode failure). Distinct
	// from C-side ring-full drops (bridge.ReportsDropped below).
	reportDrops atomic.Uint64
)

// RegisterReportHandler installs or replaces the handler for one
// report type. Replacing is allowed (hot-swap / test injection).
// Callable at init time or later — each report draws the current
// handler at dispatch.
func RegisterReportHandler(t ReportType, h ReportHandler) {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	if h == nil {
		delete(handlers, t)
		return
	}
	handlers[t] = h
}

// ReportDrops returns the Go-side counter (handler missing, etc.).
// Pair with bridge.ReportsDropped() for the C-side ring-full count.
func ReportDrops() uint64 { return reportDrops.Load() }

// StartReportConsumer spawns the consumer goroutine. Idempotent:
// repeated calls are no-ops. Stops on ctx cancellation. Called from
// Manager.Init after the C-side ring is ready.
func StartReportConsumer(ctx context.Context) {
	if !consumerActive.CompareAndSwap(false, true) {
		return
	}
	go consumeLoop(ctx)
}

// consumeLoop drains reports in batches and dispatches to handlers.
// Sleeps 10 ms between empty drains so DPDK lcores aren't polling
// against an idle consumer. The sleep is tunable; for latency-
// sensitive Usage Reports an eventfd-wake signal can be added
// without touching the handler interface.
func consumeLoop(ctx context.Context) {
	log := logger.Get("upf.report")
	log.Info("UPF report consumer goroutine started (TS 29.244 §7.5.8)")
	defer func() {
		consumerActive.Store(false)
		log.Info("UPF report consumer goroutine stopped")
	}()

	const batchSize = 64
	buf := make([]Report, batchSize)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if bridge == nil {
				continue
			}
			n := bridge.DrainReports(buf)
			for i := 0; i < n; i++ {
				dispatchReport(&buf[i])
			}
		}
	}
}

// dispatchReport routes one report to its registered handler.
// Metrics + warn-log on missing handler so operators notice
// unwired types. Renamed to avoid collision with the cgo bridge's
// Go→C dispatch() helper in cgo_bridge_linux.go.
func dispatchReport(r *Report) {
	handlersMu.RLock()
	h, ok := handlers[r.Type]
	handlersMu.RUnlock()

	log := logger.Get("upf.report").WithIMSI(r.IMSI)
	log.Debugf("§7.5.8 report type=%s pduSessID=%d seid=%#x",
		r.Type, r.PDUSessionID, r.SEID)

	pm.Inc(upfReportCounter(r.Type), 1)

	if !ok || h == nil {
		reportDrops.Add(1)
		log.Warnf("§7.5.8 report type=%s has no handler — dropping", r.Type)
		return
	}
	h(r)
}

// upfReportCounter returns the pm constant for a given report type.
// Keeps the dispatch hot path free of a switch.
func upfReportCounter(t ReportType) string {
	switch t {
	case ReportDLDR:
		return pm.UPFReportDLDR
	case ReportUsage:
		return pm.UPFReportUsage
	case ReportErrInd:
		return pm.UPFReportErrInd
	case ReportTSC:
		return pm.UPFReportTSC
	case ReportSessRep:
		return pm.UPFReportSessRep
	}
	return pm.UPFReportOther
}

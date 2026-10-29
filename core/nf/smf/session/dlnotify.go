// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// dlnotify.go — SMF DL-data-notification path (Network Triggered Service Request).
//
// Authoritative spec: TS 23.502 v19.7.0 §4.2.3.3 "Network Triggered
// Service Request" (PDF: specs/3gpp/ts_123502v190700p.pdf).
//
// Step 1 (verbatim): "When a UPF receives downlink data for a PDU
//
//	Session and there is no AN Tunnel Info stored in UPF for the PDU
//	Session, based on the instruction from the SMF (as described in
//	clause 5.8.3 of TS 23.501 [2]), the UPF may buffer the downlink
//	data (steps 2a and 2b), or forward the downlink data to the SMF
//	(step 2c)."
//
// Step 2a (verbatim): "UPF to SMF: Data Notification (N4 Session ID,
//
//	Information to identify the QoS Flow for the DL data packet,
//	DSCP). On arrival of the first downlink data packet for any QoS
//	Flow, the UPF shall send Data Notification message to the SMF…"
//
// Step 3a (verbatim): "SMF to AMF: Namf_Communication_N1N2Message
//
//	Transfer (SUPI, PDU Session ID, N1 SM container (SM message),
//	N2 SM information (QFI(s), QoS profile(s), CN N3 Tunnel Info,
//	S-NSSAI), Area of validity for N2 SM information, ARP, Paging
//	Policy Indicator, 5QI, N1N2TransferFailure Notification Target
//	Address, Extended Buffering support)…"
//
// The underlying PFCP signalling is TS 29.244 §7.5.8 "PFCP Session
// Report Request" with a Downlink Data Report (DLDR) IE. Our DPDK
// dataplane buffers per upf_pkt_io.c:522 (action=BUFF); the hook
// from C → Go on first-buffered-packet is the next tranche. For
// now HandleDLDataNotification is the in-process entry point
// callable from tests + any future UPF notify adapter.
package session

import (
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smpolicyfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	upfmgr "github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

func init() {
	// Register DLDR handler with the extensible UPF Report framework
	// (nf/upf/report.go). When the C-side rte_ring lands and starts
	// enqueueing first-buffer DL events, the consumer goroutine
	// routes them here — adapts the §7.5.8.2 DLDR payload to the
	// §4.2.3.3 step 2a entry point. New report types land with the
	// same pattern: register a handler in the consumer package's
	// init.
	upfmgr.RegisterReportHandler(upfmgr.ReportDLDR, func(r *upfmgr.Report) {
		HandleDLDataNotification(r.IMSI, r.PDUSessionID)
	})
}

// N1N2TransferFunc is the §4.2.3.3 step 3a hook the AMF installs so
// the SMF can reach it without an import cycle. Signature mirrors
// the Nsmf → Namf SBI call: the AMF resolves the UE ctx from IMSI
// and decides paging vs. direct forwarding based on CM state.
//
// imsi is the SUPI-less digits (AMF looks it up via
// uectx.Default.LookupByIMSI). pduSessionID names the affected PDU
// session whose DL data has arrived.
type N1N2TransferFunc func(imsi string, pduSessionID uint8)

// N1N2Transfer is the hook set by nf/amf at bootstrap. When nil
// (e.g. in tests that don't bring up the AMF side), HandleDLData
// Notification logs and returns — no-op.
var N1N2Transfer N1N2TransferFunc

// HandleDLDataNotification is the SMF entry point for the §4.2.3.3
// DL-data-arrived path. In a split deployment it's invoked by the
// PFCP Session Report Request handler (TS 29.244 §7.5.8) after the
// UPF buffers the first DL packet on a suspended PDU session. In
// this in-process port the UPF cgo bridge can invoke it directly
// when we wire the C→Go notify callback; today it's called from
// tests and future admin tooling.
//
// Per §4.2.3.3 the AMF's behaviour branches on CM state — we let
// the AMF do that via the N1N2Transfer hook.
func HandleDLDataNotification(imsi string, pduSessionID uint8) {
	log := logger.Get("smf.dlnotify").WithIMSI(imsi)
	pm.Inc(pm.SMDLNotify, 1)

	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		log.Warnf("DL notify for unknown session pduSessID=%d — dropping", pduSessionID)
		return
	}

	// §4.2.3.3 step 2a is only meaningful when the PDU session has
	// been deactivated (the UE is CM-IDLE + AN Tunnel Info was
	// removed per §4.2.6 step 6a). If the session is still Active
	// the UP is already up and the DL packet would've forwarded on
	// the existing tunnel — the notify is a bug on the caller's side.
	if sess.State != StateSuspended {
		log.Debugf("DL notify for session state=%s (pduSessID=%d) — user plane already active, ignoring",
			sess.State, pduSessionID)
		return
	}

	// Belt-and-braces: the SM Policy Association should still be
	// Active at the PCF (preserved across CM-IDLE per TS 23.502
	// §4.2.6 — only UP resources go down). If it's missing, skip
	// the notify — the AMF can't route an N1N2 transfer for a
	// session that no longer exists in the 5GC.
	if decision := smpolicy.GetAssociation(smpolicyfsm.Key{IMSI: imsi, PDUSessionID: pduSessionID}); decision == nil {
		log.Warnf("DL notify pduSessID=%d: no SM Policy Association — skipping N1N2",
			pduSessionID)
		return
	}

	log.Infof("DL data notification pduSessID=%d dnn=%s — invoking §4.2.3.3 step 3a Namf_Communication_N1N2MessageTransfer",
		pduSessionID, sess.DNN)

	if N1N2Transfer != nil {
		N1N2Transfer(imsi, pduSessionID)
		return
	}
	log.Warnf("N1N2Transfer hook not wired — AMF reachability missing; DL will continue to buffer at UPF until the UE reconnects on its own")
}

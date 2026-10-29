// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// bridge_hook.go — adapter that makes upf.UPFBridge satisfy the
// pfcp.ManagerHook interface so the integrated-PFCP Handler can
// drive the real UPF dataplane (libupf_dp.so via cgo).
//
// In pfcp-loop mode, main.go hands us a reference to the pre-swap
// dpdkBridge AFTER upf.Manager.Init has completed EAL + PktIO
// bring-up. We install this adapter as the Handler's ManagerHook;
// every Create-*/Update-* IE the Handler decodes reaches DPDK by
// way of this pass-through.
package upfloop

import (
	"github.com/mmt/mmt-studio-core/nf/upf"
)

// bridgeHook adapts upf.UPFBridge → pfcp.ManagerHook.
//
// The dp field is the UPF-side dataplane driver (dpdkBridge on
// Linux). It is NOT upf.Bridge() at request time — by then upfloop
// has swapped the global to PfcpBridge. We captured dp before the
// swap so hook calls keep reaching DPDK instead of looping back
// onto our own PFCP transport.
type bridgeHook struct {
	dp upf.UPFBridge
}

func (h *bridgeHook) CreateSession(imsi string, pduSessionID uint8,
	dnn string, sst uint8, sd, ueAddr uint32, pdnType uint8) error {
	return h.dp.SessionCreate(imsi, pduSessionID, dnn, sst, sd, ueAddr, pdnType)
}

func (h *bridgeHook) DeleteSession(imsi string, pduSessionID uint8) error {
	return h.dp.SessionDelete(imsi, pduSessionID)
}

// CommitSession — round-2 #1 batched-establishment trigger. Forwards
// to the underlying UPFBridge.CommitSession, which on dpdkBridge
// flushes the per-session buffer (SessionCreate + Add* + Register*)
// into one cgo round-trip. Spec-neutral: every C entry point that
// runs inside Commit is the same one a single-shot path used to call.
func (h *bridgeHook) CommitSession(imsi string, pduSessionID uint8) error {
	return h.dp.CommitSession(imsi, pduSessionID)
}

func (h *bridgeHook) AddPDR(imsi string, pduSessionID uint8, pdrID uint16,
	precedence uint32, pdiSource, qfi uint8,
	farID, qerID, urrID uint32, sdfRules string,
	ueIPv4, teid, n3IPv4 uint32) error {
	return h.dp.AddPDR(imsi, pduSessionID, pdrID, precedence,
		pdiSource, qfi, farID, qerID, urrID, sdfRules,
		ueIPv4, teid, n3IPv4)
}

func (h *bridgeHook) AddFAR(imsi string, pduSessionID uint8, farID uint32,
	action, dstIface uint8, teid, peerAddr uint32, peerPort uint16,
	ohcType uint8) error {
	return h.dp.AddFAR(imsi, pduSessionID, farID, action, dstIface,
		teid, peerAddr, peerPort, ohcType)
}

func (h *bridgeHook) AddQER(imsi string, pduSessionID uint8, qerID uint32,
	qfi, gateUL, gateDL uint8, mbrUL, mbrDL, gbrUL, gbrDL uint64) error {
	return h.dp.AddQER(imsi, pduSessionID, qerID, qfi, gateUL, gateDL,
		mbrUL, mbrDL, gbrUL, gbrDL)
}

func (h *bridgeHook) AddURR(imsi string, pduSessionID uint8, urrID uint32,
	measMethod, reportTrigger uint8,
	volThreshUL, volThreshDL uint64, timeThresh uint32) error {
	return h.dp.AddURR(imsi, pduSessionID, urrID,
		measMethod, reportTrigger, volThreshUL, volThreshDL, timeThresh)
}

func (h *bridgeHook) UpdateFAR(imsi string, pduSessionID uint8,
	farID, teid, peerAddr uint32, peerPort uint16) error {
	return h.dp.UpdateFAR(imsi, pduSessionID, farID, teid, peerAddr, peerPort)
}

func (h *bridgeHook) DeactivateDLFAR(imsi string, pduSessionID uint8, farID uint32) error {
	return h.dp.DeactivateDLFAR(imsi, pduSessionID, farID)
}

// Remove* — TS 29.244 v19.5.0 §7.5.4.6/.7/.8/.9. Straight pass-through
// to the UPF dataplane (dpdkBridge), which flips active=false on the
// matching slot in the session's PDR/FAR/QER/URR array.
func (h *bridgeHook) RemovePDR(imsi string, pduSessionID uint8, pdrID uint16) error {
	return h.dp.RemovePDR(imsi, pduSessionID, pdrID)
}

func (h *bridgeHook) RemoveFAR(imsi string, pduSessionID uint8, farID uint32) error {
	return h.dp.RemoveFAR(imsi, pduSessionID, farID)
}

func (h *bridgeHook) RemoveQER(imsi string, pduSessionID uint8, qerID uint32) error {
	return h.dp.RemoveQER(imsi, pduSessionID, qerID)
}

func (h *bridgeHook) RemoveURR(imsi string, pduSessionID uint8, urrID uint32) error {
	return h.dp.RemoveURR(imsi, pduSessionID, urrID)
}

// UpdatePDR / UpdateQER / UpdateURR — TS 29.244 v19.5.0 §7.5.4.2/.5/.4.
// Pass-through to the UPF dataplane.
func (h *bridgeHook) UpdatePDR(imsi string, pduSessionID uint8, pdrID uint16,
	precedence uint32, pdiSource, qfi uint8, farID, qerID, urrID uint32, sdfRules string,
	ueIPv4, teid, n3IPv4 uint32) error {
	return h.dp.UpdatePDR(imsi, pduSessionID, pdrID, precedence,
		pdiSource, qfi, farID, qerID, urrID, sdfRules, ueIPv4, teid, n3IPv4)
}

func (h *bridgeHook) UpdateQER(imsi string, pduSessionID uint8, qerID uint32,
	qfi, gateUL, gateDL uint8, mbrUL, mbrDL, gbrUL, gbrDL uint64) error {
	return h.dp.UpdateQER(imsi, pduSessionID, qerID, qfi, gateUL, gateDL,
		mbrUL, mbrDL, gbrUL, gbrDL)
}

func (h *bridgeHook) UpdateURR(imsi string, pduSessionID uint8, urrID uint32,
	measMethod, reportTrigger uint8, volThreshUL, volThreshDL uint64, timeThresh uint32) error {
	return h.dp.UpdateURR(imsi, pduSessionID, urrID,
		measMethod, reportTrigger, volThreshUL, volThreshDL, timeThresh)
}

func (h *bridgeHook) SetSessionAMBR(imsi string, pduSessionID uint8,
	ambrUL, ambrDL uint64) error {
	return h.dp.SetSessionAMBR(imsi, pduSessionID, ambrUL, ambrDL)
}

func (h *bridgeHook) RegisterTEID(teid uint32, imsi string, pduSessionID uint8) error {
	return h.dp.RegisterTEID(teid, imsi, pduSessionID)
}

func (h *bridgeHook) RegisterUEIP(ueAddr uint32, imsi string, pduSessionID uint8) error {
	return h.dp.RegisterUEIP(ueAddr, imsi, pduSessionID)
}

func (h *bridgeHook) UnregisterTEID(teid uint32) error {
	return h.dp.UnregisterTEID(teid)
}

func (h *bridgeHook) UnregisterUEIP(ueAddr uint32) error {
	return h.dp.UnregisterUEIP(ueAddr)
}

// UnregisterSessionKeys — batched §7.5.6 reverse-map release in one
// cgo trip (see UPFBridge.UnregisterSessionKeys for the rationale).
func (h *bridgeHook) UnregisterSessionKeys(teids []uint32, ueips []uint32) (int, error) {
	return h.dp.UnregisterSessionKeys(teids, ueips)
}

// URRStats reads final per-URR vol/pkt counters from the DPDK
// dataplane. Called by the PFCP handler at §7.5.6 deletion to log
// session totals into sacore.log — turns "no throughput" complaints
// into a concrete signal (UL/DL bytes seen by the data plane vs. 0).
//
// Best-effort: returns whatever the bridge's GetURRStats returns,
// including its error. The handler logs and proceeds either way.
func (h *bridgeHook) URRStats(imsi string, pduSessionID uint8, urrID uint32) (volUL, volDL, pktUL, pktDL uint64, err error) {
	return h.dp.GetURRStats(imsi, pduSessionID, urrID)
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// handler_session_modify.go — TS 29.244 §7.5.4 Session Modification.
//
// Holds:
//   - handleSessionModification          — top-level §7.5.4 Request handler.
//   - applyUpdate{PDR,FAR,QER,URR}ToHook — §7.5.4.2/.3/.4/.5 Update IEs.
//   - applyRemove{PDR,FAR,QER,URR}ToHook — §7.5.4.6/.7/.8/.9 Remove IEs.
//   - buildUsageReportForQuery           — §7.5.4.10 Query URR → §7.5.5.2
//                                          Usage Report IE in the response.
//
// Create-* IEs (legal in §7.5.4 too — rules added after Establishment)
// reuse applyCreate*ToHook from handler_session_establish.go.
package pfcp

import (
	"net"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// handleSessionModification implements TS 29.244 §7.5.4.
//
// §7.5.4.1 (verbatim): "The PFCP Session Modification Request
// shall be sent … by the CP function to the UP function to
// modify an existing PFCP session at the UP function."
//
// Decode + log IE counts + return Accepted.
func (h *Handler) handleSessionModification(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	defer h.traceHandler("session_modification", peer)()

	var req genpfcp.SessionModificationRequest
	if err := req.Decode(payload); err != nil {
		h.log.Warnf("session modify decode from %s: %v — sending Cause=#68",
			peer, err)
		h.sendSessionReject(peer, genpfcp.MessageTypeSessionModificationResponse,
			hdr.SEID, hdr.SequenceNumber, 68)
		return
	}

	// Look up session by UP-SEID to recover (imsi, pduSessionID)
	// stored at Establish — so Modification logs carry
	// [IMSI:…] and the hook sees the same keys as CreateSession.
	h.mu.Lock()
	sess := h.sessions[hdr.SEID]
	h.mu.Unlock()
	var imsi string
	var pduSessionID uint8
	if sess != nil {
		imsi = sess.IMSI
		pduSessionID = sess.PDUSessionID
	}
	slog := h.log.WithIMSI(imsi)
	slog.Infof("PFCP Session Modification pduSessID=%d UP-SEID=%#x from %s createPDR=%d createFAR=%d createURR=%d createQER=%d updatePDR=%d updateFAR=%d updateURR=%d updateQER=%d removePDR=%d removeFAR=%d removeURR=%d removeQER=%d queryURR=%d (TS 29.244 §7.5.4)",
		pduSessionID, hdr.SEID, peer,
		len(req.CreatePDR), len(req.CreateFAR),
		len(req.CreateURR), len(req.CreateQER),
		len(req.UpdatePDR), len(req.UpdateFAR),
		len(req.UpdateURR), len(req.UpdateQER),
		len(req.RemovePDR), len(req.RemoveFAR),
		len(req.RemoveURR), len(req.RemoveQER),
		len(req.QueryURR))

	// §7.5.4.2 allows Create-* IEs inside a Modification Request for
	// rules added AFTER Establishment. Drive the hook with the same
	// dep-first order as Establishment (FAR/URR/QER then PDR).
	// §7.5.4.3 Update FAR → hook (AN-Release BUFF / Service Request
	// FORW w/ new Outer Header Creation) flows through here too.
	if h.mgr != nil {
		for i := range req.CreateFAR {
			applyCreateFARToHook(slog, h.mgr, imsi, pduSessionID, &req.CreateFAR[i])
		}
		for i := range req.CreateURR {
			applyCreateURRToHook(slog, h.mgr, imsi, pduSessionID, &req.CreateURR[i], sess)
		}
		for i := range req.CreateQER {
			applyCreateQERToHook(slog, h.mgr, imsi, pduSessionID, &req.CreateQER[i])
		}
		for i := range req.CreatePDR {
			applyCreatePDRToHook(slog, h.mgr, imsi, pduSessionID, &req.CreatePDR[i], sess)
		}
		for i := range req.UpdateFAR {
			applyUpdateFARToHook(slog, h.mgr, imsi, pduSessionID, &req.UpdateFAR[i])
		}
		// §7.5.4.2 Update PDR (after Update FAR so a PDR's new FAR
		// reference points at an updated rule). §7.5.4.4 Update URR
		// and §7.5.4.5 Update QER follow.
		for i := range req.UpdatePDR {
			applyUpdatePDRToHook(slog, h.mgr, imsi, pduSessionID, &req.UpdatePDR[i], sess)
		}
		for i := range req.UpdateURR {
			applyUpdateURRToHook(slog, h.mgr, imsi, pduSessionID, &req.UpdateURR[i])
		}
		for i := range req.UpdateQER {
			applyUpdateQERToHook(slog, h.mgr, imsi, pduSessionID, &req.UpdateQER[i])
		}

		// §7.5.4.6 Remove PDR — remove BEFORE Remove FAR so PDRs that
		// pointed at the soon-removed FAR get torn down first; the
		// classifier's find_far returning NULL on a still-present PDR
		// is a packet drop window we'd rather avoid.
		// §7.5.4.7 Remove FAR / .8 Remove URR / .9 Remove QER follow.
		for i := range req.RemovePDR {
			applyRemovePDRToHook(slog, h.mgr, imsi, pduSessionID, &req.RemovePDR[i], sess)
		}
		for i := range req.RemoveFAR {
			applyRemoveFARToHook(slog, h.mgr, imsi, pduSessionID, &req.RemoveFAR[i])
		}
		for i := range req.RemoveURR {
			applyRemoveURRToHook(slog, h.mgr, imsi, pduSessionID, &req.RemoveURR[i], sess)
		}
		for i := range req.RemoveQER {
			applyRemoveQERToHook(slog, h.mgr, imsi, pduSessionID, &req.RemoveQER[i])
		}
	}

	// TS 29.244 v19.5.0 §7.5.4.10 Query URR — for each Query URR IE,
	// the UP function returns final-current usage in a §7.5.5.2
	// Usage Report IE inside the §7.5.5 Modification Response. Each
	// Usage Report carries §8.2.71 UR-SEQN, §8.2.41 Usage Report
	// Trigger (IMMER bit 8 set — "immediate report reported on CP
	// function … request"), and §8.2.44 Volume Measurement with all
	// six counters (TOVOL/ULVOL/DLVOL/TONOP/ULNOP/DLNOP) populated.
	//
	// TODO(spec: TS 29.244 §7.5.4 PFCPSMReq-Flags QAURR bit 3) —
	//   when set, dump ALL URR usage in response (today we only
	//   honour explicit Query URR IEs).
	var usageReports []genpfcp.UsageReportSessionModificationResponse
	if h.mgr != nil {
		for i := range req.QueryURR {
			urrID := req.QueryURR[i].URRID.Value
			volUL, volDL, pktUL, pktDL, err := h.mgr.URRStats(imsi, pduSessionID, urrID)
			if err != nil {
				slog.Debugf("Query URR-%d: stats unavailable: %v", urrID, err)
				continue
			}
			usageReports = append(usageReports,
				buildUsageReportForQuery(urrID, volUL, volDL, pktUL, pktDL))
			slog.Infof("  UPF Query URR-%d pduSessID=%d → UL=%d B / %d pkts  DL=%d B / %d pkts (TS 29.244 §7.5.4.10 + §8.2.44)",
				urrID, pduSessionID, volUL, pktUL, volDL, pktDL)
		}
	}

	// §7.2.2.4.2 (verbatim): "the destination SEID value shall be set
	// to the SEID received in the F-SEID IE of the request from the
	// corresponding peer". For UPF→SMF responses that's the CP-SEID
	// the SMF allocated and put in §8.2.37 CP-F-SEID at Establishment.
	// We stored it on HandlerSession.CPSEID; fall back to hdr.SEID
	// (UP-SEID echo) only when the session is unknown — same fallback
	// the §7.5.6 Deletion path uses for unknown SEIDs.
	respSEID := hdr.SEID
	if sess != nil {
		respSEID = sess.CPSEID
	}
	resp := &genpfcp.SessionModificationResponse{
		SEID:           respSEID,
		SequenceNumber: hdr.SequenceNumber,
		Cause:          genpfcp.Cause{Value: 1},
		UsageReport:    usageReports,
	}
	out, err := stripHeader(resp)
	if err != nil {
		h.log.Warnf("session modify response encode: %v", err)
		return
	}
	_ = h.t.SendResponse(peer, genpfcp.MessageTypeSessionModificationResponse,
		respSEID, hdr.SequenceNumber, out)
}

// applyUpdateFARToHook decodes §7.5.4.3 Update FAR and dispatches
// either hook.UpdateFAR (activation/FORW with new Outer Header) or
// hook.DeactivateDLFAR (BUFF, no new tunnel). Apply Action bit
// semantics per §8.2.26.
func applyUpdateFARToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, uf *genpfcp.UpdateFAR) {
	farID := uf.FARID.Value

	// Deactivate path: BUFF set and no new UpdateForwardingParameters
	// → TS 23.502 §4.2.6 AN-Release.
	if uf.ApplyAction != nil && uf.ApplyAction.BUFF == 1 && uf.UpdateForwardingParameters == nil {
		if err := hook.DeactivateDLFAR(imsi, pduSessionID, farID); err != nil {
			log.Warnf("hook.DeactivateDLFAR pduSessID=%d farID=%d: %v",
				pduSessionID, farID, err)
			return
		}
		log.Infof("  UPF deactivated DL FAR-%d (Apply Action=BUFF) pduSessID=%d",
			farID, pduSessionID)
		return
	}

	// Activation / reactivation path: FORW (possibly) + new Outer
	// Header Creation with gNB TEID + peer IP. TS 23.502 §4.2.3.2.
	var teid, peerAddr uint32
	var peerPort uint16
	if ufp := uf.UpdateForwardingParameters; ufp != nil {
		if ufp.OuterHeaderCreation != nil {
			teid, peerAddr = readOHCGTPUv4(ufp.OuterHeaderCreation)
			if teid != 0 || peerAddr != 0 {
				peerPort = 2152
			}
		}
	}
	if err := hook.UpdateFAR(imsi, pduSessionID, farID, teid, peerAddr, peerPort); err != nil {
		log.Warnf("hook.UpdateFAR pduSessID=%d farID=%d: %v",
			pduSessionID, farID, err)
		return
	}
	log.Infof("  UPF updated FAR-%d pduSessID=%d teid=%#x peer=%#x",
		farID, pduSessionID, teid, peerAddr)
}

// applyUpdatePDRToHook implements TS 29.244 v19.5.0 §7.5.4.2.
//
// §7.5.4.2: the Update PDR IE "shall identify the PDR among all the
// PDRs configured for that PFCP session" via the mandatory PDR ID;
// every other field is conditional ("shall be present if it needs to
// be changed"). The PDI sub-IE, if present, "shall replace the PDI
// previously stored in the UP function for this PDR".
//
// Today's UP-side semantic: wholesale replace by ID. The Go decode
// reads each present field; absent fields fall to zero — which the
// SMF must not rely on if it wants a partial Update. Conditional
// fields actually exercised by the SMF in this repo (Precedence,
// FAR-ID, QER-ID, URR-ID, PDI) are all decoded below.
//
// PDI rebinding: when the new PDI carries a different F-TEID or
// UE-IP than the previously-tracked PDRKey for this PDR-ID, the old
// reverse-map entry is unregistered and the new one registered, so
// the dataplane teid_hash / ueip_hash never holds a stale binding.
func applyUpdatePDRToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, up *genpfcp.UpdatePDR,
	sess *HandlerSession) {
	pdrID := up.PDRID.Value

	var precedence uint32
	if up.Precedence != nil {
		precedence = up.Precedence.Value
	}
	var farID, qerID, urrID uint32
	if up.FARID != nil {
		farID = up.FARID.Value
	}
	if len(up.QERID) > 0 {
		qerID = up.QERID[0].Value
	}
	if len(up.URRID) > 0 {
		urrID = up.URRID[0].Value
	}

	// PDI replacement (§7.5.4.2: "this IE shall replace the PDI
	// previously stored ... for this PDR"). Only fields present
	// in the new PDI are carried; the rest fall to zero.
	var pdiSource, qfi uint8
	var sdf string
	var ueIPv4, teid, n3IPv4 uint32
	if up.PDI != nil {
		pdiSource = up.PDI.SourceInterface.Value
		if len(up.PDI.QFI) > 0 {
			qfi = up.PDI.QFI[0].Value
		}
		if len(up.PDI.SDFFilter) > 0 {
			sdf = up.PDI.SDFFilter[0].FlowDescription
		}
		for _, ue := range up.PDI.UEIPAddress {
			if v4 := ue.IPv4.To4(); v4 != nil {
				ueIPv4 = uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
				break
			}
		}
		if up.PDI.FTEID != nil && !up.PDI.FTEID.CH {
			teid = up.PDI.FTEID.TEID
			if v4 := up.PDI.FTEID.IPv4; v4 != nil {
				if v4 := v4.To4(); v4 != nil {
					n3IPv4 = uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
				}
			}
		}
	}

	if err := hook.UpdatePDR(imsi, pduSessionID, pdrID, precedence,
		pdiSource, qfi, farID, qerID, urrID, sdf,
		ueIPv4, teid, n3IPv4); err != nil {
		log.Warnf("hook.UpdatePDR pdrID=%d: %v", pdrID, err)
		return
	}

	// PDI rebind: if the F-TEID / UE-IP changed, unregister the old
	// reverse-map keys and register the new ones so the dataplane
	// hash tables track the current PDI exactly. Same code path as
	// applyRemovePDRToHook + applyCreatePDRToHook combined.
	if sess != nil {
		old := sess.PDRKeys[pdrID]
		var newKey PDRReverseKey
		if up.PDI != nil {
			if old.TEID != teid {
				if old.TEID != 0 {
					_ = hook.UnregisterTEID(old.TEID)
				}
				if teid != 0 {
					if err := hook.RegisterTEID(teid, imsi, pduSessionID); err == nil {
						newKey.TEID = teid
					}
				}
			} else {
				newKey.TEID = old.TEID
			}
			if old.UEIP != ueIPv4 {
				if old.UEIP != 0 {
					_ = hook.UnregisterUEIP(old.UEIP)
				}
				if ueIPv4 != 0 {
					if err := hook.RegisterUEIP(ueIPv4, imsi, pduSessionID); err == nil {
						newKey.UEIP = ueIPv4
					}
				}
			} else {
				newKey.UEIP = old.UEIP
			}
		} else {
			// No PDI in the Update — keep prior reverse-map keys.
			newKey = old
		}
		sess.PDRKeys[pdrID] = newKey
	}

	log.Infof("  UPF updated PDR-%d pduSessID=%d (TS 29.244 §7.5.4.2)",
		pdrID, pduSessionID)
}

// applyUpdateURRToHook implements TS 29.244 v19.5.0 §7.5.4.4.
//
// §7.5.4.4: the Update URR IE carries Measurement Method, Reporting
// Triggers, Volume Threshold, Time Threshold etc. as conditional
// fields ("shall be present if ... needs to be modified"). Today's
// dataplane wholesale-replaces; counters are reset.
func applyUpdateURRToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, uu *genpfcp.UpdateURR) {
	urrID := uu.URRID.Value
	var measMethod, reportTrigger uint8
	var volUL, volDL uint64
	var timeSec uint32
	if uu.MeasurementMethod != nil {
		measMethod = (uu.MeasurementMethod.EVENT&1)<<2 |
			(uu.MeasurementMethod.VOLUM&1)<<1 |
			(uu.MeasurementMethod.DURAT & 1)
	}
	if uu.ReportingTriggers != nil {
		reportTrigger = uint8(uu.ReportingTriggers.Flags & 0xFF)
	}
	if uu.VolumeThreshold != nil {
		if uu.VolumeThreshold.UplinkVolume != nil {
			volUL = *uu.VolumeThreshold.UplinkVolume
		}
		if uu.VolumeThreshold.DownlinkVolume != nil {
			volDL = *uu.VolumeThreshold.DownlinkVolume
		}
	}
	if uu.TimeThreshold != nil {
		timeSec = uu.TimeThreshold.Seconds
	}
	if err := hook.UpdateURR(imsi, pduSessionID, urrID,
		measMethod, reportTrigger, volUL, volDL, timeSec); err != nil {
		log.Warnf("hook.UpdateURR urrID=%d: %v", urrID, err)
		return
	}
	log.Infof("  UPF updated URR-%d pduSessID=%d measMethod=%#x trigger=%#x volThresh=%d/%d timeThresh=%ds (TS 29.244 §7.5.4.4)",
		urrID, pduSessionID, measMethod, reportTrigger,
		volUL, volDL, timeSec)
}

// applyUpdateQERToHook implements TS 29.244 v19.5.0 §7.5.4.5.
//
// §7.5.4.5: the Update QER IE carries QFI, Gate Status, MBR, GBR
// etc. as conditional fields. Today's dataplane wholesale-replaces;
// the rte_meter token bucket is reconfigured fresh.
func applyUpdateQERToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, uq *genpfcp.UpdateQER) {
	qerID := uq.QERID.Value
	var qfi uint8
	var gateUL, gateDL uint8
	var mbrUL, mbrDL, gbrUL, gbrDL uint64
	if uq.QFI != nil {
		qfi = uq.QFI.Value
	}
	if uq.GateStatus != nil {
		gateUL = uq.GateStatus.ULGate
		gateDL = uq.GateStatus.DLGate
	}
	if uq.MBR != nil {
		mbrUL, mbrDL = uq.MBR.UL, uq.MBR.DL
	}
	if uq.GBR != nil {
		gbrUL, gbrDL = uq.GBR.UL, uq.GBR.DL
	}
	if err := hook.UpdateQER(imsi, pduSessionID, qerID, qfi,
		gateUL, gateDL, mbrUL, mbrDL, gbrUL, gbrDL); err != nil {
		log.Warnf("hook.UpdateQER qerID=%d: %v", qerID, err)
		return
	}
	log.Infof("  UPF updated QER-%d pduSessID=%d QFI=%d gateUL=%d gateDL=%d MBR=%d/%d GBR=%d/%d (kbps) (TS 29.244 §7.5.4.5)",
		qerID, pduSessionID, qfi, gateUL, gateDL,
		mbrUL, mbrDL, gbrUL, gbrDL)
}

// applyRemovePDRToHook implements TS 29.244 v19.5.0 §7.5.4.6.
//
// §7.5.4.6: the Remove PDR IE "shall identify the PDR to be deleted"
// by its mandatory PDR ID IE. Order on the wire: spec is silent;
// caller already orders Remove PDR before Remove FAR/QER/URR so a
// removed-PDR doesn't briefly point to a soon-removed FAR.
//
// This function:
//   1. Looks up the PDR's tracked (TEID, UE-IP) reverse-map keys
//      and unregisters them via hook.UnregisterTEID/UnregisterUEIP
//      so the dataplane teid_hash / ueip_hash slots are released
//      (otherwise they leak until §7.5.6 Session Deletion).
//   2. Calls hook.RemovePDR which flips the C side's pdr->active=false.
//   3. Drops the PDR from sess.PDRKeys so later Remove or §7.5.6
//      sweep doesn't double-unregister.
func applyRemovePDRToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, rp *genpfcp.RemovePDR,
	sess *HandlerSession) {
	pdrID := rp.PDRID.Value

	if sess != nil {
		if k, ok := sess.PDRKeys[pdrID]; ok {
			if k.TEID != 0 {
				if err := hook.UnregisterTEID(k.TEID); err != nil {
					log.Warnf("hook.UnregisterTEID teid=0x%08X pdrID=%d: %v",
						k.TEID, pdrID, err)
				}
			}
			if k.UEIP != 0 {
				if err := hook.UnregisterUEIP(k.UEIP); err != nil {
					log.Warnf("hook.UnregisterUEIP ueIPv4=0x%08X pdrID=%d: %v",
						k.UEIP, pdrID, err)
				}
			}
			delete(sess.PDRKeys, pdrID)
		}
	}

	if err := hook.RemovePDR(imsi, pduSessionID, pdrID); err != nil {
		log.Warnf("hook.RemovePDR pdrID=%d pduSessID=%d: %v",
			pdrID, pduSessionID, err)
		return
	}
	log.Infof("  UPF removed PDR-%d pduSessID=%d (TS 29.244 §7.5.4.6)",
		pdrID, pduSessionID)
}

// applyRemoveFARToHook implements TS 29.244 v19.5.0 §7.5.4.7.
//
// §7.5.4.7: the Remove FAR IE "shall identify the FAR to be deleted"
// by its mandatory FAR ID IE. The dataplane's classifier returns
// action=0 (drop) for any still-present PDR that referenced this
// FAR — fail-closed posture for orphan PDRs the SMF didn't remove
// first.
func applyRemoveFARToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, rf *genpfcp.RemoveFAR) {
	farID := rf.FARID.Value
	if err := hook.RemoveFAR(imsi, pduSessionID, farID); err != nil {
		log.Warnf("hook.RemoveFAR farID=%d pduSessID=%d: %v",
			farID, pduSessionID, err)
		return
	}
	log.Infof("  UPF removed FAR-%d pduSessID=%d (TS 29.244 §7.5.4.7)",
		farID, pduSessionID)
}

// applyRemoveQERToHook implements TS 29.244 v19.5.0 §7.5.4.9.
//
// §7.5.4.9: the Remove QER IE "shall identify the QER to be deleted"
// by its mandatory QER ID IE. PDRs that referenced this QER will
// see find_qer()=NULL on subsequent packets — no Gate Status / no
// MBR enforcement at the per-flow level. Session-AMBR / UE-AMBR
// metering is unaffected.
func applyRemoveQERToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, rq *genpfcp.RemoveQER) {
	qerID := rq.QERID.Value
	if err := hook.RemoveQER(imsi, pduSessionID, qerID); err != nil {
		log.Warnf("hook.RemoveQER qerID=%d pduSessID=%d: %v",
			qerID, pduSessionID, err)
		return
	}
	log.Infof("  UPF removed QER-%d pduSessID=%d (TS 29.244 §7.5.4.9)",
		qerID, pduSessionID)
}

// applyRemoveURRToHook implements TS 29.244 v19.5.0 §7.5.4.8.
//
// §7.5.4.8: the Remove URR IE "shall identify the URR to be deleted"
// by its mandatory URR ID IE. Counter values held in the C dataplane
// for this URR are lost as soon as the slot is flipped inactive —
// the SMF SHOULD have issued §7.5.4.10 Query URR first to harvest
// final usage if the URR was a charging anchor. Today's UP function
// does NOT yet emit §7.5.5.2 Usage Report IEs in the §7.5.5
// Modification Response — see TODO at handleSessionModification.
//
// Drops the URR from sess.URRIDs so the §7.5.6 deletion final-stats
// log doesn't try to read counters from a now-inactive slot.
func applyRemoveURRToHook(log *logger.Logger, hook ManagerHook,
	imsi string, pduSessionID uint8, ru *genpfcp.RemoveURR,
	sess *HandlerSession) {
	urrID := ru.URRID.Value
	if err := hook.RemoveURR(imsi, pduSessionID, urrID); err != nil {
		log.Warnf("hook.RemoveURR urrID=%d pduSessID=%d: %v",
			urrID, pduSessionID, err)
		return
	}
	if sess != nil {
		for i, id := range sess.URRIDs {
			if id == urrID {
				sess.URRIDs = append(sess.URRIDs[:i], sess.URRIDs[i+1:]...)
				break
			}
		}
	}
	log.Infof("  UPF removed URR-%d pduSessID=%d (TS 29.244 §7.5.4.8)",
		urrID, pduSessionID)
}

// buildUsageReportForQuery composes a §7.5.5.2 Usage Report IE for a
// §7.5.4.10 Query URR response, with TS 29.244 v19.5.0 §8.2.41 Usage
// Report Trigger = IMMER (bit 8 = 0x80, "immediate report reported on
// CP function ... request") and §8.2.44 Volume Measurement carrying
// all six counters (TOVOL/ULVOL/DLVOL/TONOP/ULNOP/DLNOP). Total
// volume / total packets are derived as UL+DL.
//
// VolumeMeasurement is a generic []byte payload in the generated
// codec — encode the §8.2.44 layout explicitly:
//
//	Octet 5: flags  TOVOL ULVOL DLVOL TONOP ULNOP DLNOP (bits 1..6)
//	Octets 6-13:  Total Volume     (TOVOL=1)
//	Octets 14-21: Uplink Volume    (ULVOL=1)
//	Octets 22-29: Downlink Volume  (DLVOL=1)
//	Octets 30-37: Total Packets    (TONOP=1)
//	Octets 38-45: Uplink Packets   (ULNOP=1)
//	Octets 46-53: Downlink Packets (DLNOP=1)
func buildUsageReportForQuery(urrID uint32, volUL, volDL, pktUL, pktDL uint64) genpfcp.UsageReportSessionModificationResponse {
	const (
		flagTOVOL = 1 << 0
		flagULVOL = 1 << 1
		flagDLVOL = 1 << 2
		flagTONOP = 1 << 3
		flagULNOP = 1 << 4
		flagDLNOP = 1 << 5
	)
	vm := make([]byte, 1+8*6)
	vm[0] = flagTOVOL | flagULVOL | flagDLVOL | flagTONOP | flagULNOP | flagDLNOP
	put := func(off int, v uint64) {
		vm[off+0] = byte(v >> 56)
		vm[off+1] = byte(v >> 48)
		vm[off+2] = byte(v >> 40)
		vm[off+3] = byte(v >> 32)
		vm[off+4] = byte(v >> 24)
		vm[off+5] = byte(v >> 16)
		vm[off+6] = byte(v >> 8)
		vm[off+7] = byte(v)
	}
	put(1, volUL+volDL) // Total Volume
	put(9, volUL)       // Uplink Volume
	put(17, volDL)      // Downlink Volume
	put(25, pktUL+pktDL)
	put(33, pktUL)
	put(41, pktDL)

	// §8.2.71 UR-SEQN — 4-byte sequence number per (UP-SEID, URR).
	// We're not yet keeping a per-URR sequence counter, so emit 0
	// for now. A proper charging anchor needs a monotonic counter
	// surviving session restart.
	urSeqN := []byte{0, 0, 0, 0}
	return genpfcp.UsageReportSessionModificationResponse{
		URRID:  genpfcp.URRID{Value: urrID},
		URSEQN: genpfcp.URSEQN{Value: urSeqN},
		UsageReportTrigger: genpfcp.UsageReportTrigger{
			Flags: 0x80, // IMMER (§8.2.41 bit 8): immediate report on CP request
		},
		VolumeMeasurement: &genpfcp.VolumeMeasurement{Value: vm},
	}
}

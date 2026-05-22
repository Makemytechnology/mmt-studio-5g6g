// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// handler_session_delete.go — TS 29.244 §7.5.6 Session Deletion.
//
// Holds the per-§7.5.6 wire handler and the tearDownSession helper
// shared with §7.4.4.5 Association Release (handler_association.go).
package pfcp

import (
	"net"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"
)

// handleSessionDeletion implements TS 29.244 §7.5.6.
//
// §7.5.6.1 (verbatim): "The PFCP Session Deletion Request shall
// be sent … by the CP function to the UP function to delete an
// existing PFCP session at the UP function."
func (h *Handler) handleSessionDeletion(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	defer h.traceHandler("session_deletion", peer)()

	var req genpfcp.SessionDeletionRequest
	if err := req.Decode(payload); err != nil {
		h.log.Warnf("session delete decode from %s: %v", peer, err)
	}
	_ = req

	// Local session table — the peer addresses us via UP-SEID
	// (which it received as CP-SEID-equivalent in the §7.5.3
	// Establishment Response's UP-F-SEID IE).
	h.mu.Lock()
	sess, ok := h.sessions[hdr.SEID]
	if ok {
		delete(h.sessions, hdr.SEID)
		if sess.IMSI != "" {
			delete(h.byIMSI, imsiPduKey{sess.IMSI, sess.PDUSessionID})
		}
	}
	h.mu.Unlock()

	if !ok {
		h.log.Warnf("PFCP Session Deletion for unknown UP-SEID=%#x from %s — sending Cause=#65 (Session context not found)",
			hdr.SEID, peer)
		h.sendSessionReject(peer, genpfcp.MessageTypeSessionDeletionResponse,
			hdr.SEID, hdr.SequenceNumber, 65)
		return
	}
	slog := h.log.WithIMSI(sess.IMSI)
	slog.Infof("PFCP Session Deletion pduSessID=%d UP-SEID=%#x CP-SEID=%#x from %s (TS 29.244 §7.5.6)",
		sess.PDUSessionID, sess.UPSEID, sess.CPSEID, peer)

	h.tearDownSession(sess)

	// TODO(spec: TS 29.244 §7.5.7.2 Usage Report IE in Session
	//   Deletion Response) — gather final per-URR usage + include
	//   Usage Report IEs in the response (charging anchor).
	//   Without this, CHF under-counts the last reporting window.

	resp := &genpfcp.SessionDeletionResponse{
		SEID:           sess.CPSEID,
		SequenceNumber: hdr.SequenceNumber,
		Cause:          genpfcp.Cause{Value: 1},
	}
	out, err := stripHeader(resp)
	if err != nil {
		h.log.Warnf("session delete response encode: %v", err)
		return
	}
	_ = h.t.SendResponse(peer, genpfcp.MessageTypeSessionDeletionResponse,
		sess.CPSEID, hdr.SequenceNumber, out)
}

// tearDownSession runs the §7.5.6 per-session teardown body shared by
// both the §7.5.6 single-session path and the §7.4.4.5 Association
// Release path:
//
//  1. Final per-URR vol/pkt log (TS 29.244 v19.5.0 §8.2.41 Volume
//     Measurement) so "no throughput" cases are post-mortem-actionable
//     from sacore.log even when the session ends via cascade.
//  2. Batched §7.5.6 reverse-map release (TS 29.244 v19.5.0 §5.5.1
//     F-TEID + §8.2.62 UE IP) — one cgo round-trip walks both slices
//     at the dataplane EAL thread.
//  3. Dataplane mgr.DeleteSession to release the C session_pool slot.
//
// Caller owns h.sessions / h.byIMSI map removal (which is per-key
// and the deletion path is the only place that mutates those maps).
// Best-effort — errors are logged, not propagated.
func (h *Handler) tearDownSession(sess *HandlerSession) {
	if h.mgr == nil {
		return
	}
	slog := h.log.WithIMSI(sess.IMSI)

	// 1. Final URR snapshot.
	for _, urrID := range sess.URRIDs {
		volUL, volDL, pktUL, pktDL, err := h.mgr.URRStats(sess.IMSI, sess.PDUSessionID, urrID)
		if err != nil {
			slog.Debugf("URRStats urrID=%d pduSessID=%d: %v",
				urrID, sess.PDUSessionID, err)
			continue
		}
		slog.Infof("  UPF final URR-%d pduSessID=%d UL=%d B / %d pkts  DL=%d B / %d pkts (TS 29.244 §8.2.41 Volume Measurement)",
			urrID, sess.PDUSessionID, volUL, pktUL, volDL, pktDL)
	}

	// 2. Reverse-map release. Order matters: the session must outlive
	// the unregister so a concurrent reader resolving a stale
	// TEID/UE-IP into upf_session_get sees active=true and bails on
	// the empty PDR list, not a use-after-free.
	teids := make([]uint32, 0, len(sess.PDRKeys))
	ueips := make([]uint32, 0, len(sess.PDRKeys))
	for _, k := range sess.PDRKeys {
		if k.TEID != 0 {
			teids = append(teids, k.TEID)
		}
		if k.UEIP != 0 {
			ueips = append(ueips, k.UEIP)
		}
	}
	if len(teids) > 0 || len(ueips) > 0 {
		if _, err := h.mgr.UnregisterSessionKeys(teids, ueips); err != nil {
			slog.Warnf("hook.UnregisterSessionKeys pduSessID=%d teid=%d ueip=%d: %v",
				sess.PDUSessionID, len(teids), len(ueips), err)
		}
		slog.Infof("  UPF released %d reverse-map entries (%d TEID + %d UE-IP) for pduSessID=%d (TS 29.244 §7.5.6)",
			len(teids)+len(ueips), len(teids), len(ueips), sess.PDUSessionID)
	}

	// 3. Dataplane session delete.
	if err := h.mgr.DeleteSession(sess.IMSI, sess.PDUSessionID); err != nil {
		slog.Warnf("manager.DeleteSession pduSessID=%d UP-SEID=%#x: %v",
			sess.PDUSessionID, sess.UPSEID, err)
	}
}

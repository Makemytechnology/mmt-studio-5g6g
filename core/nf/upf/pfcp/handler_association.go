// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// handler_association.go — PFCP node-level (non-session) handlers:
//   §7.4.4.1  Association Setup Request           (handleAssociationSetup)
//   §7.4.4.5  Association Release Request         (handleAssociationRelease)
//   §7.4.6    Session Set Deletion Request        (handleSessionSetDeletion)
//
// These three share the property that they either establish, release,
// or operate at the granularity of the PFCP association — not a single
// SEID — so they live together away from the per-§7.5 session handlers.
package pfcp

import (
	"net"
	"time"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"
)

// handleAssociationSetup implements TS 29.244 §7.4.4.1.
//
// §7.3.4.1 (verbatim): "The PFCP Association Setup procedure is
// used to setup an association between two PFCP entities."
//
// Mandatory Request IEs (§7.3.4.2): Node ID (§8.2.38), Recovery
// Time Stamp (§8.2.3). Response mirrors + adds Cause (§8.2.1).
func (h *Handler) handleAssociationSetup(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	defer h.traceHandler("association_setup", peer)()

	var req genpfcp.AssociationSetupRequest
	if err := req.Decode(payload); err != nil {
		h.log.Warnf("association setup decode from %s: %v", peer, err)
		return
	}
	h.log.Infof("PFCP Association Setup from %s nodeID=%s (TS 29.244 §7.4.4.1)",
		peer, formatNodeID(&req.NodeID))

	// TODO(spec: TS 29.244 §8.2.65 Recovery Time Stamp
	//   validation) — store the CP's Recovery Time Stamp +
	//   detect peer-restart on subsequent Association Setup (it
	//   should be "Association Update" if the peer is still
	//   alive, per §7.3.5). Today we accept anyway.
	// TODO(spec: TS 29.244 §7.4.4.1 UP Function Features §8.2.25) —
	//   advertise supported features bitmap (BUCP for DLDR
	//   buffering, EMPU for Ethernet, …). Without features IE,
	//   peer applies conservative defaults.

	resp := &genpfcp.AssociationSetupResponse{
		SequenceNumber: hdr.SequenceNumber,
		NodeID: genpfcp.NodeID{
			Type: 0, // §8.2.38 IPv4
			IPv4: net.ParseIP("127.0.0.1").To4(),
		},
		Cause: genpfcp.Cause{Value: 1}, // §8.2.1 "Request accepted (success)"
		RecoveryTimeStamp: genpfcp.RecoveryTimeStamp{
			Value: uint32(time.Now().Unix()) + 2208988800,
		},
	}
	out, err := stripHeader(resp)
	if err != nil {
		h.log.Warnf("association setup response encode: %v", err)
		return
	}
	_ = h.t.SendResponse(peer, genpfcp.MessageTypeAssociationSetupResponse, 0,
		hdr.SequenceNumber, out)
}

// handleAssociationRelease implements TS 29.244 v19.5.0 §7.4.4.5
// PFCP Association Release Request, with the receiver-side bulk-
// session-delete semantics mandated by §6.2.8.3:
//
//   "When the UP function receives a PFCP Association Release Request,
//    the UP function shall delete all the PFCP sessions related to that
//    PFCP association locally, unless the MPAS feature (clause 5.22.3)
//    is used … shall send a PFCP Association Release Response with a
//    successful cause."
//
// We don't advertise MPAS (§5.22.3) so the simple per-association
// teardown path applies. One association per peer in our model
// (HandlerSession.Peer carries the §7.5.2 establish source); match by
// peer IP and tear down every session whose Peer matches.
//
// Cause=#1 (Request accepted) is correct even if the request matches
// zero local sessions — §6.2.8.3 doesn't condition the success cause
// on a non-empty match. Response shape per §7.4.4.6 is NodeID + Cause.
func (h *Handler) handleAssociationRelease(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	defer h.traceHandler("association_release", peer)()

	var req genpfcp.AssociationReleaseRequest
	if err := req.Decode(payload); err != nil {
		h.log.Warnf("association release decode from %s: %v", peer, err)
		return
	}
	h.log.Infof("PFCP Association Release Request from %s nodeID=%s (TS 29.244 §7.4.4.5)",
		peer, formatNodeID(&req.NodeID))

	// Snapshot the matching sessions under the lock, drop them from
	// the maps, then run teardown without the lock held (each
	// tearDownSession call dispatches into the cgo bridge which can
	// block — never hold h.mu across a cgo dispatch).
	var victims []*HandlerSession
	h.mu.Lock()
	for upSEID, sess := range h.sessions {
		if sess.Peer != nil && peer != nil && sess.Peer.IP.Equal(peer.IP) {
			victims = append(victims, sess)
			delete(h.sessions, upSEID)
			if sess.IMSI != "" {
				delete(h.byIMSI, imsiPduKey{sess.IMSI, sess.PDUSessionID})
			}
		}
	}
	h.mu.Unlock()

	for _, v := range victims {
		h.tearDownSession(v)
	}
	h.log.Infof("PFCP Association Release: tore down %d session(s) for peer %s (TS 29.244 §6.2.8.3)",
		len(victims), peer)

	resp := &genpfcp.AssociationReleaseResponse{
		SequenceNumber: hdr.SequenceNumber,
		NodeID: genpfcp.NodeID{
			Type: 0, // §8.2.38 IPv4
			IPv4: net.ParseIP("127.0.0.1").To4(),
		},
		Cause: genpfcp.Cause{Value: 1}, // §8.2.1 "Request accepted (success)"
	}
	out, err := stripHeader(resp)
	if err != nil {
		h.log.Warnf("association release response encode: %v", err)
		return
	}
	_ = h.t.SendResponse(peer, genpfcp.MessageTypeAssociationReleaseResponse, 0,
		hdr.SequenceNumber, out)
}

// handleSessionSetDeletion implements TS 29.244 v19.5.0 §7.4.6 PFCP
// Session Set Deletion Request:
//
//   "The PFCP Session Set Deletion Request shall be sent … by the CP
//    function to request the UP function to delete the PFCP sessions
//    affected by a partial failure."
//
// The carried §8.2.61 FQ-CSID list identifies which sessions to
// release, scoped to NF-level identifiers (SGW-C / PGW-C/SMF / UPF /
// TWAN / ePDG / MME — there is no (R)AN variant). Today our SMF does
// not allocate FQ-CSIDs at §7.5.2 Establishment, so we have no
// per-session CSID index to match against — the conformant response
// is Cause=#1 with zero sessions matched (§7.4.6 does not condition
// success on non-empty match). When CSID tracking lands at §7.5.2
// per the TODO below, the matching logic populates the same victims
// loop as handleAssociationRelease.
//
// Response shape per §7.4.6.2 is NodeID + Cause.
//
// TODO(spec: TS 29.244 v19.5.0 §8.2.61 FQ-CSID + §7.5.2.2 CP-F-SEID
//
//	emission) — at session establishment, accept the SMF-side
//	CP-FQ-CSID(s) IE and store them on HandlerSession; at this
//	handler iterate h.sessions matching any carried FQ-CSID and
//	tear them down via tearDownSession. Until then, conformant
//	zero-match.
func (h *Handler) handleSessionSetDeletion(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	defer h.traceHandler("session_set_deletion", peer)()

	var req genpfcp.SessionSetDeletionRequest
	if err := req.Decode(payload); err != nil {
		h.log.Warnf("session set deletion decode from %s: %v", peer, err)
		return
	}
	h.log.Infof("PFCP Session Set Deletion Request from %s nodeID=%s fqcsids=%d (TS 29.244 §7.4.6.1) — CSID-tracking TODO §8.2.61, zero-match conformant per §7.4.6",
		peer, formatNodeID(&req.NodeID), len(req.FQCSID))

	resp := &genpfcp.SessionSetDeletionResponse{
		SequenceNumber: hdr.SequenceNumber,
		NodeID: genpfcp.NodeID{
			Type: 0, // §8.2.38 IPv4
			IPv4: net.ParseIP("127.0.0.1").To4(),
		},
		Cause: genpfcp.Cause{Value: 1}, // §8.2.1 "Request accepted (success)"
	}
	out, err := stripHeader(resp)
	if err != nil {
		h.log.Warnf("session set deletion response encode: %v", err)
		return
	}
	_ = h.t.SendResponse(peer, genpfcp.MessageTypeSessionSetDeletionResponse, 0,
		hdr.SequenceNumber, out)
}

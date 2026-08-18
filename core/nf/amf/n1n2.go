// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// n1n2.go — Namf_Communication_N1N2MessageTransfer handler.
//
// Authoritative spec: TS 23.502 v19.7.0 §4.2.3.3 "Network Triggered
// Service Request" (PDF: specs/3gpp/ts_123502v190700p.pdf).
// Message-flow anchor: Figure 4.2.3.3-1 step 3a.
//
// Step 3a (verbatim): "SMF to AMF: Namf_Communication_N1N2Message
//
//	Transfer (SUPI, PDU Session ID, N1 SM container (SM message),
//	N2 SM information …). … If the AMF has determined the UE is
//	unreachable … the AMF rejects the request from the SMF."
//
// Step 3b (verbatim excerpts): "If the UE is in CM-IDLE state at
//
//	the AMF and the AMF is able to page the UE the AMF sends a
//	Namf_Communication_N1N2MessageTransfer response to the SMF
//	immediately … If the UE is in CM-CONNECTED state at the AMF
//	then the AMF sends a Namf_Communication_N1N2MessageTransfer
//	response to the SMF immediately to indicate that the N1/N2
//	message has been sent out."
//
// TS 29.518 "Namf Services" is the SBI spec for the Namf reference
// point (PDF: specs/3gpp/ts_129518v190600p.pdf); when the SBI
// layer matures this handler becomes an HTTP endpoint. In-process
// today: the SMF calls HandleN1N2MessageTransfer via the
// session.N1N2Transfer hook (wired in hooks.go).
package amf

import (
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	ngaproot "github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// HandleN1N2MessageTransfer implements the AMF side of §4.2.3.3
// step 3a. Branches on CM state:
//
//   - CM-IDLE: store pduSessionID on the UE ctx as pending, invoke
//     NGAP PAGING (§8.7.1 procedureCode 24). Paging retransmits
//     under T3513 (TS 24.501 §10.2 Table 10.2.1); when the UE
//     responds with Service Request the GMM handler cancels T3513
//     and reactivates the pending PDU sessions via PDU Session
//     Resource Setup Request.
//
//   - CM-CONNECTED: no paging needed — the N3 tunnel should already
//     be up or being brought up. This is the "asynchronous MT
//     delivery" ack path; we log and return.
//
// When the UE is unknown or in a state incompatible with paging
// (RM-DEREGISTERED, or no serving gNB on record), the spec mandates
// rejection; the log-only stub below captures this with a TODO for
// the ProblemDetails reject path that lands with the SBI layer.
func HandleN1N2MessageTransfer(imsi string, pduSessionID uint8) {
	log := logger.Get("amf.n1n2")
	pm.Inc(pm.SMN1N2, 1)

	ue := uectx.Default.LookupByIMSI(imsi)
	if ue == nil {
		log.Warnf("N1N2 transfer imsi=%s: unknown UE — SMF should receive 'UE not reachable' per §4.2.3.3 step 3b (TODO: ProblemDetails reject over Namf SBI)",
			imsi)
		return
	}

	log.WithIMSI(ue.IMSI).Infof("N1N2 transfer amfUeID=%d pduSessID=%d CM=%s RM=%s",
		ue.AmfUeNGAPID, pduSessionID, ue.CM, ue.RM)

	// §4.2.3.3 step 3b: reject when UE is unreachable.
	if ue.RM != uectx.RMRegistered {
		log.WithIMSI(ue.IMSI).Warnf("N1N2 transfer rejected: UE RM=%s, not REGISTERED (§4.2.3.3 step 3b unreachable)", ue.RM)
		return
	}

	// Record the pending session so the Service Request reactivation
	// path can enumerate what to bring back up.
	appendPendingSession(ue, pduSessionID)

	switch ue.CM {
	case uectx.CMIdle:
		// §4.2.3.3 step 4: trigger Paging. SendPaging arms T3513 +
		// retransmits per TS 24.501 §10.2 Table 10.2.1 (N3513=4).
		// The UE's Service Request cancels the timer (see
		// nf/amf/gmm/service.go:ngap.CancelT3513ForUE).
		n := ngaproot.SendPaging(ue, gnbctx.Default)
		log.WithIMSI(ue.IMSI).Infof("N1N2 → Paging fan-out: %d gNB(s) paged (pending pduSessID=%d)",
			n, pduSessionID)
	case uectx.CMConnected:
		// TS 23.502 v19.7.0 §4.8.2.2b step 2 (verbatim, page 238):
		//   "When the AMF determines that the UE is reachable, the
		//   AMF sends a RAN Paging Request message to NG-RAN with
		//   the request for the UE's RRC connection to be resumed."
		// In CM-CONNECTED + RRC_INACTIVE the radio is suspended; we
		// MUST page so the gNB can resume the RRC connection. The
		// same SendPaging fan-out applies — the NGAP Paging IE is
		// what triggers either CM-IDLE Service Request OR RRC_INACTIVE
		// Connection Resume on the UE side (TS 38.300 §9.2.5).
		if ue.RRC == uectx.RRCInactive {
			n := ngaproot.SendPaging(ue, gnbctx.Default)
			log.WithIMSI(ue.IMSI).Infof("§4.8.2.2b: CM-CONNECTED + RRC-INACTIVE → Paging fan-out: %d gNB(s) paged (pending pduSessID=%d)",
				n, pduSessionID)
			return
		}
		// CM-CONNECTED + RRC-CONNECTED: UE is fully active — no
		// paging needed. Reactivation here would be redundant if
		// the user-plane is up; if it's down, the session's
		// existing N3 setup applies.
		// TODO(spec: TS 23.502 §4.2.3.3 step 3b CM-CONNECTED) —
		//   when UE is CM-CONNECTED but target PDU session is
		//   Suspended (e.g. SMF retries), trigger per-session
		//   PDU Session Resource Setup Request directly.
		log.WithIMSI(ue.IMSI).Infof("N1N2 transfer: UE CM-CONNECTED + RRC-CONNECTED; reactivation deferred to Service Request / existing N3")
	default:
		log.WithIMSI(ue.IMSI).Warnf("N1N2 transfer: unexpected CM state %s", ue.CM)
	}
}

// appendPendingSession records a PDU session ID on the UE's pending
// list, deduplicating so repeated §4.2.3.3 step 3a invocations (one
// per new DL flow or Paging Policy Indicator change) don't bloat
// the list.
func appendPendingSession(ue *uectx.AmfUeCtx, pduSessionID uint8) {
	for _, id := range ue.PendingN1N2Sessions {
		if id == pduSessionID {
			return
		}
	}
	ue.PendingN1N2Sessions = append(ue.PendingN1N2Sessions, pduSessionID)
}

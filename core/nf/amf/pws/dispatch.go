// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pws — AMF-side Public Warning System orchestration.
//
// Authoritative spec: TS 38.413 v19.2.0 §8.9 Warning Message
// Transmission Procedures (PDF: specs/3gpp/ts_138413v190200p.pdf).
// TS 23.041 (Cell Broadcast Service architecture) defines the
// upstream CBC interface (Nbf / SBc-AP) — out of scope for this
// package; we cover the N2 (NGAP) fan-out only.
//
// PWS uses non-UE-associated signalling per §8.9.1.1, §8.9.2.1,
// §8.9.3.1, §8.9.4.1 — a Warning Message is fundamentally an
// "every-gNB" broadcast. The per-procedure senders in
// nf/amf/ngap/writereplace and nf/amf/ngap/pwscancel take a single
// *gnbctx.GnbCtx; this package is the orchestration layer that turns
// "broadcast this alert" into N parallel Sends, one per gNB, and
// aggregates the per-gNB outcome.
package pws

import (
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pwscancel"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/writereplace"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// GnbResult is the per-gNB outcome of one fan-out attempt. A nil Err
// means the request was handed to SCTP successfully; the broadcast
// outcome (Broadcast Completed Area List per §8.9.1.2 line 7681-7682
// / Broadcast Cancelled Area List per §8.9.2.2 line 7733) lands later
// asynchronously in writereplace.HandleResponse / pwscancel.HandleResponse.
type GnbResult struct {
	GnbIP string
	GnbID string
	Err   error
}

// BroadcastToAll fans a §9.2.8.1 WRITE-REPLACE WARNING REQUEST out to
// every currently-registered gNB (gnbctx.Default.All). Per §8.9.1.1
// the procedure is non UE-associated: each gNB independently
// schedules the broadcast on the cells it serves and ACKs back via
// §9.2.8.2 WRITE-REPLACE WARNING RESPONSE.
func BroadcastToAll(p writereplace.Params) []GnbResult {
	log := logger.Get("amf.pws")
	gnbs := gnbctx.Default.All()
	if len(gnbs) == 0 {
		log.Warnf("BroadcastToAll msgID=%d serial=%d: no gNBs registered — Write-Replace Warning has no targets",
			p.MessageIdentifier, p.SerialNumber)
		return nil
	}
	results := make([]GnbResult, 0, len(gnbs))
	for _, g := range gnbs {
		err := writereplace.Send(g, p)
		results = append(results, GnbResult{GnbIP: g.GnbIP, GnbID: g.GnbID, Err: err})
		if err != nil {
			log.Warnf("BroadcastToAll gNB=%s msgID=%d: Send failed: %v",
				g.GnbIP, p.MessageIdentifier, err)
		}
	}
	log.Infof("BroadcastToAll msgID=%d serial=%d: dispatched to %d gNB(s) (TS 38.413 §8.9.1)",
		p.MessageIdentifier, p.SerialNumber, len(results))
	return results
}

// CancelToAll fans a §9.2.8.3 PWS CANCEL REQUEST out to every
// currently-registered gNB. Per §8.9.2.1 non UE-associated; gNBs ACK
// asynchronously via §9.2.8.4 PWS CANCEL RESPONSE consumed by
// pwscancel.HandleResponse.
func CancelToAll(p pwscancel.Params) []GnbResult {
	log := logger.Get("amf.pws")
	gnbs := gnbctx.Default.All()
	if len(gnbs) == 0 {
		log.Warnf("CancelToAll msgID=%d serial=%d: no gNBs registered",
			p.MessageIdentifier, p.SerialNumber)
		return nil
	}
	results := make([]GnbResult, 0, len(gnbs))
	for _, g := range gnbs {
		err := pwscancel.Send(g, p)
		results = append(results, GnbResult{GnbIP: g.GnbIP, GnbID: g.GnbID, Err: err})
		if err != nil {
			log.Warnf("CancelToAll gNB=%s msgID=%d: Send failed: %v",
				g.GnbIP, p.MessageIdentifier, err)
		}
	}
	log.Infof("CancelToAll msgID=%d serial=%d: dispatched to %d gNB(s) (TS 38.413 §8.9.2)",
		p.MessageIdentifier, p.SerialNumber, len(results))
	return results
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// handler_heartbeat.go — TS 29.244 §7.4.2 Heartbeat Request handler.
package pfcp

import (
	"net"
	"time"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"
)

// handleHeartbeat implements TS 29.244 §7.4.2.
//
// §7.4.2.1 (verbatim): "The PFCP Heartbeat Request message shall
// be sent by a PFCP entity to another PFCP entity to find out if
// the peer PFCP entity is alive."
//
// Mandatory IE: §8.2.3 Recovery Time Stamp (NTP). We echo with
// our own Recovery Time Stamp — on the response the peer reads
// it to detect local restart.
func (h *Handler) handleHeartbeat(hdr *runtime.Header, _ []byte, peer *net.UDPAddr) {
	defer h.traceHandler("heartbeat", peer)()

	// Build §7.4.2 Response via generated codec. The Recovery
	// Time Stamp IE wants NTP-format seconds (§8.2.3); the
	// runtime helpers expose an NTP-now alias.
	// TODO(spec: TS 29.244 §8.2.3 Recovery Time Stamp) — use a
	//   stable "process-start" NTP timestamp rather than now(),
	//   so the peer can actually detect our restart by the
	//   Recovery Time Stamp changing. Today we send now() which
	//   always changes.
	resp := &genpfcp.HeartbeatResponse{
		SequenceNumber: hdr.SequenceNumber,
		RecoveryTimeStamp: genpfcp.RecoveryTimeStamp{
			Value: uint32(time.Now().Unix()) + 2208988800, // NTP epoch offset
		},
	}
	payload, err := stripHeader(resp)
	if err != nil {
		h.log.Warnf("heartbeat response encode: %v", err)
		return
	}
	_ = h.t.SendResponse(peer, genpfcp.MessageTypeHeartbeatResponse, 0,
		hdr.SequenceNumber, payload)
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NAS retransmit hook. TS 24.501 §10.2 Table 10.2.1 — every AMF→UE
// command timer (T3513, T3522, T3550, T3555, T3560, T3570) retransmits
// the original message up to 4 times at the timer's cadence before
// declaring failure. T3521 is UE-side and intentionally absent from
// this list. The FSM's TimerSpec.Retransmit closure calls
// retransmitLastNAS, which re-emits the bytes the sender stashed on
// uectx.RetxNASPDU right before the transition that armed the timer.
//
// We keep the re-send in a single helper so every retransmitting timer
// (and any future one) re-uses the same error paths: gNB gone, dlnas
// failure, empty buffer. Each of those gets a WARN so operators can
// tell a real packet-loss retransmit from a config/lookup bug.
package gmm

import (
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// retransmitLastNAS re-sends whatever NAS PDU the sender last cached on
// ue.RetxNASPDU. Safe to call from the timer-manager goroutine; dlnas.Send
// takes its own lock on the gnb transport.
func retransmitLastNAS(tag string) func(*uectx.AmfUeCtx) {
	log := logger.Get("amf.gmm.retx")
	return func(ue *uectx.AmfUeCtx) {
		if ue == nil {
			return
		}
		pdu := ue.RetxNASPDU
		if len(pdu) == 0 {
			log.Warnf("[%s] retransmit skipped: no cached PDU for amfUeID=%d",
				tag, ue.AmfUeNGAPID)
			return
		}
		gnb := gnbctx.Default.GetByIP(ue.GnbKey)
		if gnb == nil {
			log.Warnf("[%s] retransmit skipped: gNB %q gone for amfUeID=%d",
				tag, ue.GnbKey, ue.AmfUeNGAPID)
			return
		}
		// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
		if err := dlnas.Send(gnb, ue, pdu); err != nil {
			log.Warnf("[%s] retransmit dlnas.Send amfUeID=%d: %v",
				tag, ue.AmfUeNGAPID, err)
			return
		}
		log.WithIMSI(ue.IMSI).Infof("[%s] retransmitted %dB to amfUeID=%d",
			tag, len(pdu), ue.AmfUeNGAPID)
	}
}

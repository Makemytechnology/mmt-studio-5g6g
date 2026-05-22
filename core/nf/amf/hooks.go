// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

// Package-level hook wiring for UE context removal. Declared in nf/amf
// (not in nf/amf/uectx) so it can import cross-subsystem cleanup
// helpers (timers manager + PTI tracker) without pulling those
// dependencies into the leaf uectx package.

package amf

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/nf/smf/session/pti"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// init attaches global UE-cleanup hooks on uectx.Default. Every hook
// fires exactly once per removed UE, outside the registry lock (see
// uectx.Registry.Remove / RemoveAllForGnb).
//
// The GMM FSM + NGAP per-UE FSM drop hooks live with their respective
// fsm packages (gmm/fsm/fsm.go, ngap/fsm/fsm.go). The rest go here:
//
//  1. timers.M.CancelAllForUE — cancel any per-UE NAS retx / idle timer
//     still armed. Every NAS timer is keyed by fmt.Sprint(amfUeID), so
//     passing the same formatted id drops all of them.
//
//  2. pti.Default.ReleaseAllForUE — release stale 5GSM PTI slots
//     that linger after a UE's registration torn down mid-session.
//     Keyed by IMSI, so no-op when IMSI wasn't resolved yet.
//
// Individual teardown paths (dereg, auth reject, SMC reject, …) still
// call these helpers directly for clarity + immediate cleanup before
// the Remove lands. The hooks are a belt-and-braces net catching any
// removal path that forgets.
func init() {
	uectx.Default.RegisterRemoveHook(func(ue *uectx.AmfUeCtx) {
		timers.M.CancelAllForUE(fmt.Sprintf("%d", ue.AmfUeNGAPID))
	})
	uectx.Default.RegisterRemoveHook(func(ue *uectx.AmfUeCtx) {
		if ue.IMSI != "" {
			pti.Default.ReleaseAllForUE(ue.IMSI)
		}
	})

	// Wire the SMF → AMF hook for Namf_Communication_N1N2MessageTransfer
	// (TS 23.502 §4.2.3.3 step 3a) so session.HandleDLDataNotification
	// can reach HandleN1N2MessageTransfer without an import cycle
	// between nf/smf/session and nf/amf.
	session.N1N2Transfer = HandleN1N2MessageTransfer

	// Wire the SMF → AMF hook for the network-initiated PDU SESSION
	// MODIFICATION COMMAND DL path (TS 23.502 §4.3.3 step 4 / TS
	// 38.413 §8.6.2 DownlinkNASTransport). Lookup-and-send: SMF
	// hands us (IMSI, dlNAS); we resolve UE + serving gNB and ship
	// over the UE-associated SCTP stream.
	session.DLNASByIMSI = sendDLNASByIMSI
}

// sendDLNASByIMSI is the function nf/smf/session.DLNASByIMSI points
// at. Best-effort: a missing UE / gNB is logged at WARN, not an error,
// so the PCF UpdateNotify FSM still completes (the SBI port will
// surface delivery failures via HTTP status when it lands).
func sendDLNASByIMSI(imsi string, dlNAS []byte) error {
	log := logger.Get("amf.dlnas.bridge").WithIMSI(imsi)
	ue := uectx.Default.LookupByIMSI(imsi)
	if ue == nil {
		log.Warnf("DLNASByIMSI: no AMF UE context — UE not registered? (%d B dropped)", len(dlNAS))
		return nil
	}
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.Warnf("DLNASByIMSI: no gNB for GnbKey=%q — connection lost? (%d B dropped)", ue.GnbKey, len(dlNAS))
		return nil
	}
	return dlnas.Send(gnb, ue, dlNAS)
}

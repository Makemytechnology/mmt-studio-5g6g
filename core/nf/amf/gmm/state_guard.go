// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

// State-guard helpers for GMM handlers. Per TS 24.501 v19.6.2 §7.4
// (verbatim, line 54231-54233):
//
//	"If the UE receives a message not compatible with the protocol
//	 state, the UE shall return a status message (5GMM STATUS or
//	 5GSM STATUS depending on the EPD) with cause #98 'message type
//	 not compatible with protocol state'."
//
// The network direction is "implementation dependent" per the same
// clause line 54235-54236. We choose the helpful-to-the-peer
// behaviour: log + send 5GMM STATUS #98 + drop. Without these
// guards, a late-arriving AUTH RESPONSE (e.g. retransmit after we've
// already moved past StateAuthentication) would still run
// handleAuthenticationResponse end-to-end — mutating state the FSM
// considers finalised. Worst case: double-driving into
// StateSecurityMode from a no-longer-valid AUTH Response.

package gmm

import (
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// allowedIn reports whether the UE's current GMM FSM state is in the
// list of spec-expected states for the message being handled. On false,
// callers MUST drop the message; allowedIn also sends 5GMM STATUS
// cause #98 on the rejection so the UE knows its message was
// out-of-state (per §7.4 — the network's implementation-dependent
// reaction is opt-in informative).
func allowedIn(ue *uectx.AmfUeCtx, msgName string, allowed ...fsm.State) bool {
	if ue == nil {
		return false
	}
	cur := fsm.Of(ue).State()
	for _, s := range allowed {
		if s == cur {
			return true
		}
	}
	log := logger.Get("amf.gmm.state_guard")
	log.WithIMSI(ue.IMSI).Warnf("%s received in state %s (allowed: %v) amfUeID=%d — dropping + sending 5GMM STATUS #98 (TS 24.501 §7.4)",
		msgName, cur, allowed, ue.AmfUeNGAPID)
	Send5GMMStatus(ue, Cause5GMMNotCompatibleWithState)
	return false
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GMMSTATUS handler — TS 24.501 v19.6.2 §5.4.6 (procedure) + §8.2.29
// (message definition). Earlier comments cited §5.4.5 + §8.2.24 —
// wrong: those are "NAS transport procedure(s)" and "Notification
// response" respectively. §-clause numbers verified against
// /tmp/ts24501.txt lines 23415 and 57317.
//
// Per §5.4.6.1 "5GMM status procedure": 5GMM STATUS is sent by either
// peer at any time to report abnormal conditions (cause values from
// Table 9.11.3.2.1).
//
// Per §5.4.6.3 (receive-side, line 23451-23454):
//   "On receipt of a 5GMM STATUS message in the AMF, no state
//    transition and no specific action shall be taken as seen from
//    the radio interface, i.e. local actions are possible."
//
// We log the cause for operator visibility but do not mutate state —
// the FSM self-loop transition wired in fsm_transitions.go covers the
// FSM side; this handler provides log coverage on every other state
// the UE might send 5GMMSTATUS from. The send-side counterpart lives
// in status_send.go.
package gmm

import (
	nas "github.com/mmt/nasgen/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

func init() {
	Register(Msg5GMMStatus, handle5GMMStatus)
}

func handle5GMMStatus(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.status")

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		// Per §5.4.6.3 we don't even fail: UE reporting a status
		// message that we can't decode is itself an abnormal case,
		// but the spec says "no specific action shall be taken".
		log.Warnf("5GMMSTATUS from amfUeID=%d: decode failed: %v",
			ue.AmfUeNGAPID, err)
		return
	}
	st, ok := msg.(*nas.FivegmmStatus)
	if !ok {
		log.Warnf("5GMMSTATUS from amfUeID=%d: unexpected type %T",
			ue.AmfUeNGAPID, msg)
		return
	}
	log.WithIMSI(ue.IMSI).Warnf("5GMMSTATUS received amfUeID=%d cause=%d (TS 24.501 Table 9.11.3.2.1)",
		ue.AmfUeNGAPID, st.Cause5GMM.Value)
	// State unchanged per §5.4.6.3 — self-loop transition records the event.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvStatus5GMM, Inner: inner})
}

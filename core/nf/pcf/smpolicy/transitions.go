// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SM Policy Association transition table (TS 29.512 §4.2.x).
// Handler-driven: Create/Update/Delete in smpolicy.go run the work
// and then fire the outcome events that advance state.
package smpolicy

import (
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// revalidationExpiredAction fires on T_Revalidation expiry. Per
// TS 29.512 §4.2.4.3 "Request the policy based on revalidation time"
// the SMF must call Npcf_SMPolicyControl_Update with the "RE_TIMEOUT"
// Policy Control Request Trigger. We run it in a goroutine because
// Update → Fire would otherwise reenter the FSM mutex currently held
// by Fire (the caller of this Action).
func revalidationExpiredAction(c *smfsm.Context) error {
	log := logger.Get("pcf.smpolicy").WithIMSI(c.Key.IMSI)
	log.Infof("T_Revalidation expired pduSessID=%d — firing §4.2.4 Update with RE_TIMEOUT", c.Key.PDUSessionID)
	pm.Inc(pm.PCFSmPolicyRevalidate, 1)
	go func(k smfsm.Key) {
		if _, err := Update(k, SmPolicyContextDataUpdate{Triggers: []string{"RE_TIMEOUT"}}); err != nil {
			log.Warnf("T_Revalidation Update failed pduSessID=%d: %v", k.PDUSessionID, err)
		}
	}(c.Key)
	return nil
}

func init() {
	smfsm.SetDefaultTable([]smfsm.Transition{
		// §4.2.2 Create ───────────────────────────────────────────────
		{From: smfsm.StateNone, Event: smfsm.EvCreateRequest, To: smfsm.StateCreatePending},
		{From: smfsm.StateCreatePending, Event: smfsm.EvCreateResponse, To: smfsm.StateActive},
		{From: smfsm.StateCreatePending, Event: smfsm.EvCreateReject, To: smfsm.StateNone},

		// §4.2.4 Update (SMF → PCF) — self-loop on Active.
		{From: smfsm.StateActive, Event: smfsm.EvUpdateRequest, To: smfsm.StateActive},
		{From: smfsm.StateActive, Event: smfsm.EvUpdateResponse, To: smfsm.StateActive},
		{From: smfsm.StateActive, Event: smfsm.EvUpdateReject, To: smfsm.StateActive},

		// §4.2.3 UpdateNotify (PCF → SMF) — brief UpdatePending while
		// awaiting SMF enforcement ack. Reserved for the AF-triggered
		// / Npcf_PolicyAuthorization path (nf/pcf/pcf.go
		// NotifySMFPolicyUpdate) — not fired in the current in-process
		// skeleton since there's no AF producer yet.
		{From: smfsm.StateActive, Event: smfsm.EvUpdateNotifySent, To: smfsm.StateUpdatePending},
		{From: smfsm.StateUpdatePending, Event: smfsm.EvUpdateNotifyAck, To: smfsm.StateActive},
		{From: smfsm.StateUpdatePending, Event: smfsm.EvUpdateNotifyFailure, To: smfsm.StateActive},

		// §4.2.2.4 / §4.2.3.4 Revalidation Timer. On expiry we
		// self-loop in state but Action calls Update with the
		// "RE_TIMEOUT" Policy Control Request Trigger per §4.2.4.3.
		{
			From: smfsm.StateActive, Event: smfsm.EvRevalidationTimerExpired, To: smfsm.StateActive,
			Action: revalidationExpiredAction,
		},

		// §4.2.5 Delete ───────────────────────────────────────────────
		// StopTimers cancels T_Revalidation on association teardown so
		// a late expiry doesn't fire Update against a deleted
		// association.
		{
			From: smfsm.StateActive, Event: smfsm.EvDeleteRequest, To: smfsm.StateTerminating,
			StopTimers: []string{"T_Revalidation"},
		},
		{
			From: smfsm.StateUpdatePending, Event: smfsm.EvDeleteRequest, To: smfsm.StateTerminating,
			StopTimers: []string{"T_Revalidation"},
		},
		{From: smfsm.StateTerminating, Event: smfsm.EvDeleteResponse, To: smfsm.StateTerminated},

		// Early-dereg safety: allow Delete from CreatePending if the
		// SMF abandons a half-built association (e.g. UPF failure
		// during Establish). §4.2.5 is permissive on the source state.
		{From: smfsm.StateCreatePending, Event: smfsm.EvDeleteRequest, To: smfsm.StateTerminating},
	})
}

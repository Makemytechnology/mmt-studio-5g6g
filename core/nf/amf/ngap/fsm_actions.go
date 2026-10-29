// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NGAP FSM action stubs. Log-only in this stage; the procedure
// modules (initialctxsetup, pdusetup, uectxrelease) still do the
// real send/receive work.
package ngap

import (
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

func actLogNGAPTransition(c *ngapfsm.Context) error {
	return logNGAPTransition("transition", c)
}

func actOnICSResponse(c *ngapfsm.Context) error {
	return logNGAPTransition("ics-response", c)
}

func actOnICSFailure(c *ngapfsm.Context) error {
	return logNGAPTransition("ics-failure", c)
}

// SendCtxReleaseCmdHook is the door between the NGAP FSM actions and
// the uectxrelease sub-package. Assigned in uectxrelease/init so the
// action can ship a UEContextReleaseCommand without importing
// uectxrelease (which would cycle — uectxrelease already depends on
// the ngap root package for its procedure registration).
//
// Signature: gNB key + AMF-UE-NGAP-ID + NGAP cause code (uint8 from
// CauseRadioNetwork enum per TS 38.413 §9.3.1.2 Table 1). Hook returns
// the send error for the caller to log; nil means "no handler wired"
// and the FSM just logs the transition.
var SendCtxReleaseCmdHook func(gnbIP string, amfUeID int64, cause uint8) error

func actOnICSTimeout(c *ngapfsm.Context) error {
	log := logger.Get("amf.ngap.fsm.action")
	// TS 38.413 §8.3.1.3: if InitialContextSetupResponse is not
	// received within the implementation timer (Twait-ICS), the AMF
	// SHALL initiate UE Context Release by sending UEContextRelease
	// Command with cause Radio-Connection-With-UE-Lost (21). Without
	// this step the gNB keeps the radio context, keeps forwarding UL
	// NAS for this UE, and eventually sends its delayed ICS Response
	// to an AMF that already dropped the state — every one of those
	// lands as a "procedure collision" WARN.
	if SendCtxReleaseCmdHook != nil {
		if err := SendCtxReleaseCmdHook(c.Key.GnbKey, c.Key.AMFUENGAPID, 21); err != nil {
			log.Warnf("Twait-ICS expiry: UEContextReleaseCommand send failed %s: %v",
				c.Key, err)
		}
	}
	return logNGAPTransition("Twait-ICS-expired", c)
}

// actSendReleaseCommand — real release send lives in the uectxrelease
// module today. FSM records the transition; uectxrelease.SendCommand
// ships the NGAP PDU.
func actSendReleaseCommand(c *ngapfsm.Context) error {
	return logNGAPTransition("release-command", c)
}

func actOnReleaseComplete(c *ngapfsm.Context) error {
	return logNGAPTransition("release-complete", c)
}

func actOnReleaseTimeout(c *ngapfsm.Context) error {
	return logNGAPTransition("Twait-ue-ctx-release-expired", c)
}

func actLogErrorIndication(c *ngapfsm.Context) error {
	return logNGAPTransition("error-indication", c)
}

func actOnNGReset(c *ngapfsm.Context) error {
	return logNGAPTransition("ng-reset", c)
}

func logNGAPTransition(tag string, c *ngapfsm.Context) error {
	log := logger.Get("amf.ngap.fsm.action")
	log.Debugf("[%s] %s event=%s pdu-session=%d", tag, c.Key, c.Event, c.PDUSessionID)
	return nil
}

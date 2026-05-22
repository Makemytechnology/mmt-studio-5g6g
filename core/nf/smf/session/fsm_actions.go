// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GSM FSM action stubs. Same intent as the GMM FSM's first stage:
// Actions log the transition, while the existing session.Establish /
// session.Release / session.Modify bodies continue to perform PFCP +
// NGAP work. Future stages fold the NAS-build / UPF-talk paths into
// these Action functions, letting the per-PDU-session FSM own the
// procedure end-to-end.
package session

import (
	"github.com/mmt/mmt-studio-core/nf/smf/session/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

func actEnterEstablishmentPending(c *fsm.Context) error {
	return logTransition("enter-ESTABLISHMENT_PENDING", c)
}

func actAcceptReadyToShip(c *fsm.Context) error {
	return logTransition("pfcp-response-received", c)
}

func actActivated(c *fsm.Context) error {
	return logTransition("session-ACTIVE", c)
}

func actEnterModificationPending(c *fsm.Context) error {
	return logTransition("enter-MODIFICATION_PENDING", c)
}

func actModificationComplete(c *fsm.Context) error {
	return logTransition("modification-complete", c)
}

func actOnModificationTimeout(c *fsm.Context) error {
	return logTransition("T3591-expired-modification-abort", c)
}

// actEstablishmentFailedAtPFCP — TS 29.244 §7.5.3 PFCP Session
// Establishment Response returned a non-accept cause. Handler has
// already released the IP + removed session store entry before
// firing EvPFCPEstablishFailure.
func actEstablishmentFailedAtPFCP(c *fsm.Context) error {
	return logTransition("establish-failed-at-pfcp", c)
}

// actEstablishmentFailedAtNGAP — TS 38.413 §8.2.1.3 gNB returned a
// PDUSessionResourceSetupFailure. Handler tore down PFCP + IP before
// firing EvResourceSetupFailure.
func actEstablishmentFailedAtNGAP(c *fsm.Context) error {
	return logTransition("establish-failed-at-ngap", c)
}

// actEstablishmentRejected — TS 24.501 §6.4.1.4 SMF decided to
// reject the PDU session establishment and shipped PDU SESSION
// ESTABLISHMENT REJECT (type 195). Handler already encoded the
// reject with a §9.11.4.2 cause before firing.
func actEstablishmentRejected(c *fsm.Context) error {
	return logTransition("establishment-rejected", c)
}

// actModificationRejected — TS 24.501 §6.4.2.5 UE replied with
// PDU SESSION MODIFICATION REJECT. Session stays Active with the
// pre-modification parameters.
func actModificationRejected(c *fsm.Context) error {
	return logTransition("modification-rejected-by-ue", c)
}

func actEnterReleasePending(c *fsm.Context) error {
	return logTransition("enter-RELEASE_PENDING", c)
}

func actReleased(c *fsm.Context) error {
	return logTransition("session-RELEASED", c)
}

func logTransition(tag string, c *fsm.Context) error {
	log := logger.Get("smf.session.fsm.action")
	log.Debugf("[%s] %s event=%s", tag, c.Key, c.Event)
	return nil
}

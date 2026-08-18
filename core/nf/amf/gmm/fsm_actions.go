// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// GMM FSM actions — functions invoked when a spec transition fires.
//
// In this first-stage migration, Actions are intentionally thin: they
// log the transition and leave the existing NAS-send logic in the
// per-message handler bodies (handleRegistrationRequest,
// handleAuthenticationResponse, …). The FSM is running in parallel
// with the handler code so we can a) assert that every inbound event
// has a legal source state and b) let the declarative timer graph in
// fsm_transitions.go take over T3560/T3550/T3570 management from the
// inline `timers.M.Start / Cancel` calls scattered across handlers.
//
// Subsequent commits will move the actual NAS-build + dlnas.Send
// bodies into these Actions, stripping the timer+state-mutation code
// from the handlers until the handlers shrink to bare "decode +
// fire event" shims.
package gmm

import (
	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/uectxrelease"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/security/li"
)

// Historical note — an earlier design shipped `guardAuthResponseValid`
// here so the transition table could decode AUTHENTICATION RESPONSE
// itself and route to StateSecurityMode or StateDeregistered from a
// single EvAuthenticationResponse event. That guard was removed when
// the package moved to handler-driven outcome events (EvAuthResponseValid
// / EvAuthResponseInvalid, fired from handleAuthenticationResponse). See
// gmm/dispatch.go for the ordering discussion.

// actEnterAuthentication — Registration Request / mobility trigger
// landed the FSM in AUTHENTICATION. The real auth-request build is
// still in auth.go:startAuthentication; this action just records the
// transition for now.
func actEnterAuthentication(c *fsm.Context) error {
	return logTransition("enter-AUTHENTICATION", c)
}

// actSendRegistrationAcceptReused — TS 24.501 §4.4 skip-auth path:
// the UE presented an ngKSI matching a cached valid native 5G NAS
// security context. handleRegistrationRequest has already migrated
// the cached security state onto the new UE ctx, re-derived KgNB
// (TS 33.501 §A.9), and issued ICS + Registration Accept before
// firing this event. Action is a no-op log; the real work runs in
// the handler ahead of the Fire() call, mirroring the pattern used
// by actEnterAuthentication / actEnterIdentification.
func actSendRegistrationAcceptReused(c *fsm.Context) error {
	return logTransition("reused-context-skip-auth-smc", c)
}

// actEnterSecurityMode — valid Auth Response → FSM moves to
// SECURITY_MODE. SMC build is still in smc.go:startSecurityMode.
func actEnterSecurityMode(c *fsm.Context) error {
	return logTransition("enter-SECURITY_MODE", c)
}

// actFinaliseRegistration — SMC Complete received. ICS Request +
// Registration Accept sent by smc.go:handleSecurityModeComplete.
func actFinaliseRegistration(c *fsm.Context) error {
	return logTransition("finalise-registration", c)
}

// actOnRegistrationComplete — UE acked Reg Accept, REGISTERED.
//
// LI hook: TS 33.128 §6.2.1 "Registration to 5GS Event". Per the
// LI architecture (TS 33.127 §7.4.2) the AMF acts as the IRI-POI
// for mobility-management events; the operator-curated warrant
// list (security/li) decides whether this IMSI is under
// interception. li.CaptureIRI is a no-op when no warrant matches,
// so the hot path stays unaffected for non-targeted UEs.
func actOnRegistrationComplete(c *fsm.Context) error {
	if c != nil && c.UE != nil && c.UE.IMSI != "" {
		li.CaptureIRI("REGISTER", c.UE.IMSI, map[string]interface{}{
			"event":         "registration_complete",
			"amf_ue_ngapid": c.UE.AmfUeNGAPID,
			"gnb":           c.UE.GnbKey,
		})
	}
	return logTransition("registered", c)
}

// actEnterDeregistration — MO Deregistration Request received.
func actEnterDeregistration(c *fsm.Context) error {
	return logTransition("enter-DEREGISTRATION_INITIATED", c)
}

// actFinaliseDeregistration — UE Context Release Complete received OR
// Twait-ue-ctx-release expired; GMM context is now torn down.
// actFinaliseDeregistration fires when the deregistration procedure
// completes from any non-happy path: T3522 final expiry (UE never sent
// DEREG ACCEPT to our MT-initiated DEREG REQ, §5.5.2.3) or the AMF's
// local Twait-ue-ctx-release guard fires (UE Context Release Complete
// not returned by the gNB in time after a clean MO dereg).
//
// Spec (§5.5.2.3 MT case): "after the fifth expiry of timer T3522 …
// the AMF shall enter 5GMM-DEREGISTERED and release the N1 NAS
// signalling connection." Both paths that land here (MT dereg
// finalising, Twait-ue-ctx-release guard expiry after MO dereg) are
// AMF-initiated or UE-initiated-non-switch-off — TS 33.501
// §6.8.1.1.1 cases 2.a.ii / 2.b both mandate keeping the remaining
// security parameters. Use ClearVolatile so the cached 5G NAS
// security context survives for §4.4 reuse on the next registration;
// the same remove-hooks cascade (GMM FSM drop, NGAP FSM drop, timer
// cancellation, PTI release) fires without unregistering the ctx.
func actFinaliseDeregistration(c *fsm.Context) error {
	_ = logTransition("deregistered", c)
	if c == nil || c.UE == nil {
		return nil
	}
	ue := c.UE
	// LI hook: TS 33.128 §6.2.1 "De-registration from 5GS Event"
	// (mirrors §6.2.1 Registration). Only fires when the UE is
	// covered by an active iri/iri+cc warrant.
	if ue.IMSI != "" {
		li.CaptureIRI("DEREGISTER", ue.IMSI, map[string]interface{}{
			"event":         "deregistration_complete",
			"amf_ue_ngapid": ue.AmfUeNGAPID,
		})
	}
	if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
		log := logger.Get("amf.gmm.fsm.action")
		log.WithIMSI(ue.IMSI).Infof("dereg finalised amfUeID=%d — releasing N1 NAS, retaining security context (TS 24.501 §5.5.2.3 + TS 33.501 §6.8.1.1.1 case 2.a.ii/2.b)",
			ue.AmfUeNGAPID)
		// TODO(arch: event: UE-Context-Release to NGAP — see gmm/doc.go)
		_ = uectxrelease.SendCommand(gnb, ue,
			uectxrelease.CauseNAS(genngap.CauseNasDeregister))
	}
	// Nudm_UECM_Deregistration (TS 29.503 §5.3.2.4) — tell UDM we no
	// longer serve this UE. Idempotent on missing registrations so the
	// abnormal-path finalisations (T3522 expiry, Twait-ue-ctx-release)
	// are safe to call without checking prior state.
	if ue.IMSI != "" {
		udm.DeregisterAMF(ue.IMSI)
	}
	uectx.Default.ClearVolatile(ue)
	return nil
}

// actNoopREGISTERED — steady-state event that doesn't change state
// (ULNASTransport, ServiceRequest, ConfigUpdate Complete, 5GMMStatus).
// Real payload processing happens in the corresponding handler.
func actNoopREGISTERED(c *fsm.Context) error {
	return logTransition("registered-event", c)
}

// ─── Unhappy-path actions ─────────────────────────────────────────────

func actOnAuthenticationRejected(c *fsm.Context) error {
	return logTransition("auth-rejected", c)
}

func actOnAuthenticationFailure(c *fsm.Context) error {
	return logTransition("auth-failure", c)
}

// actOnAuthenticationTimeout — T3560 final expiry on the AUTH leg.
//
// TS 24.501 §5.4.1.3.7 b:
//
//	"on the fifth expiry of timer T3560, the network shall abort the 5G
//	 AKA based primary authentication and key agreement procedure and
//	 any ongoing 5GMM specific procedure and release the N1 NAS
//	 signalling connection."
//
// The FSM transition already advances to StateDeregistered (abort). Here
// we additionally release the N1 NAS signalling connection via NGAP
// UE Context Release Command (TS 38.413 §8.3.3) with CauseNas =
// "unspecified" so the gNB tears down the per-UE NGAP context too.
//
// TS 24.501 §5.5.2.1 (verbatim): "If the de-registration procedure for
// 5GS services is performed, a local release of the PDU sessions over
// the indicated access(es), if any, for this particular UE is
// performed." The FSM ends in 5GMM-DEREGISTERED, so any active PDU
// session is released via SMF (PFCP §7.5.6) before the N1 release —
// otherwise SMF/UPF state outlives the UE context.
func actOnAuthenticationTimeout(c *fsm.Context) error {
	_ = logTransition("T3560-auth-expired", c)
	if c == nil || c.UE == nil {
		return nil
	}
	ue := c.UE
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return nil
	}
	log := logger.Get("amf.gmm.fsm.action")
	log.WithIMSI(ue.IMSI).Infof("T3560 auth-leg final expiry amfUeID=%d — releasing N1 NAS connection (TS 24.501 §5.4.1.3.7 b)",
		ue.AmfUeNGAPID)
	releaseAllPDUSessions(ue, log)
	// TODO(arch: event: UE-Context-Release to NGAP — see gmm/doc.go)
	_ = uectxrelease.SendCommand(gnb, ue,
		uectxrelease.CauseNAS(genngap.CauseNasUnspecified))
	return nil
}

func actOnSecurityModeReject(c *fsm.Context) error {
	return logTransition("smc-rejected", c)
}

// actOnSecurityModeTimeout — T3560 final expiry on the SMC leg.
//
// TS 24.501 §5.4.2.7 b:
//
//	"on the fifth expiry of timer T3560, the procedure shall be aborted."
//
// Spec is less explicit than the auth-leg case about N1 NAS release, but
// since the procedure is aborted mid-security-activation, the UE's view
// of security is inconsistent with the AMF's — releasing the N1 NAS
// connection forces the UE to re-register and renegotiate cleanly.
//
// TS 24.501 §5.5.2.1 binds here too: the FSM ends in 5GMM-DEREGISTERED,
// so PDU sessions get a local release via SMF (PFCP §7.5.6) before the
// N1 release.
func actOnSecurityModeTimeout(c *fsm.Context) error {
	_ = logTransition("T3560-expired", c)
	if c == nil || c.UE == nil {
		return nil
	}
	ue := c.UE
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return nil
	}
	log := logger.Get("amf.gmm.fsm.action")
	log.WithIMSI(ue.IMSI).Infof("T3560 SMC-leg final expiry amfUeID=%d — releasing N1 NAS connection (TS 24.501 §5.4.2.7 b)",
		ue.AmfUeNGAPID)
	releaseAllPDUSessions(ue, log)
	// TODO(arch: event: UE-Context-Release to NGAP — see gmm/doc.go)
	_ = uectxrelease.SendCommand(gnb, ue,
		uectxrelease.CauseNAS(genngap.CauseNasUnspecified))
	return nil
}

func actOnRegistrationAcceptTimeout(c *fsm.Context) error {
	return logTransition("T3550-expired", c)
}

// actOnImplicitDeregistration — mobile-reachable timer (= T3512 + 4
// min slack, TS 24.501 §5.3.7) expired: UE failed to perform periodic
// registration. Spec calls for:
//
//	"After expiry of the mobile reachable timer, the AMF shall …
//	 start the implicit deregistration timer. Upon expiry of the
//	 implicit deregistration timer, the AMF shall implicitly
//	 deregister the UE."
//
// We collapse the two-stage pattern onto a single periodicRegTimer
// (T3512 + 4 min) because our T3512 window already carries the slack
// the spec calls for. The cleanup at this point mirrors MO dereg
// minus the NAS goodbye (UE is out of reach anyway):
//
//   - release every PDU session (UPF + SMF teardown)
//   - NGAP UEContextReleaseCommand to the serving gNB
//     (cause NAS/NormalRelease — no authentication-failure style)
//   - Remove the UE ctx; the uectx.Registry remove-hook cascades to
//     GMM FSM drop, NGAP per-UE FSM drop, timers.CancelAllForUE,
//     and PTI release. See nf/amf/hooks.go.
//
// TODO(spec: TS 24.501 §5.3.7 two-stage timer) — some operators need
//
//	the explicit implicit-dereg timer (T-implicit-dereg-N) separate
//	from the mobile-reachable stage so they can probe the UE via
//	paging before final cleanup. Model as a second TimerSpec on a
//	new StateMobileReachableExpired intermediate state.
func actOnImplicitDeregistration(c *fsm.Context) error {
	_ = logTransition("T3512-expired-implicit-dereg", c)
	if c == nil || c.UE == nil {
		return nil
	}
	ue := c.UE
	log := logger.Get("amf.gmm.fsm.action")
	log.WithIMSI(ue.IMSI).Infof("implicit dereg (T3512 expired) amfUeID=%d — tearing down",
		ue.AmfUeNGAPID)

	// PDU session + SMF state teardown. releaseAllPDUSessions lives in
	// dereg.go (same package); it's the same helper the MO dereg path
	// uses, so behaviour matches.
	releaseAllPDUSessions(ue, log)

	// NGAP UEContextReleaseCommand to the gNB so it drops radio state.
	// Uses CauseNAS=NormalRelease — the UE isn't in a failure scenario
	// from the gNB's perspective, just unreachable.
	if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
		// TODO(arch: event: UE-Context-Release to NGAP — see gmm/doc.go)
		_ = uectxrelease.SendCommand(gnb, ue,
			uectxrelease.CauseNAS(genngap.CauseNasNormalRelease))
	}

	// Implicit deregistration (T3512 expired / mobile-reachable timer)
	// is AMF-initiated implicit per TS 33.501 §6.8.1.1.1 case 2.b.ii
	// "Implicit: all the remaining security parameters shall be kept
	// in the UE and AMF." ClearVolatile fires the hooks (timer cancel,
	// FSM drop, PTI release) but leaves the ctx + security params
	// indexed for §4.4 reuse when the UE eventually comes back.
	uectx.Default.ClearVolatile(ue)
	return nil
}

// actEnterIdentification — AMF sent Identity Request, T3570 armed.
func actEnterIdentification(c *fsm.Context) error {
	return logTransition("enter-IDENTIFICATION", c)
}

// actLogConfigUpdateCommandSent — AMF sent Configuration Update
// Command; T3555 armed to bound the wait for Complete.
func actLogConfigUpdateCommandSent(c *fsm.Context) error {
	return logTransition("config-update-command-sent", c)
}

// actLogConfigUpdateTimeout — T3555 expired; UE never ack'd. Stay in
// REGISTERED; procedure simply stops.
func actLogConfigUpdateTimeout(c *fsm.Context) error {
	return logTransition("T3555-expired", c)
}

// actHandleConfigUpdateComplete — UE ack'd the config update.
func actHandleConfigUpdateComplete(c *fsm.Context) error {
	return logTransition("config-update-complete", c)
}

// actEnterMTDeregPending — AMF sent network-initiated Dereg Request.
// T3522 armed; Accept from UE or T3522 expiry transitions to DEREGISTERED.
func actEnterMTDeregPending(c *fsm.Context) error {
	return logTransition("enter-MT_DEREG_PENDING", c)
}

// actOnIdentityTimeout — T3570 final expiry on the IDENTIFICATION leg.
//
// TS 24.501 §5.4.3.5 (Abnormal cases on the network side is not spelled
// out for §5.4.3, but §5.5.1.2.8 style is applied): on the 5th expiry
// of T3570 the UE never returned an IDENTITY RESPONSE. Abort the
// triggering registration procedure with cause #9 "UE identity cannot
// be derived by the network" and release the N1 NAS signalling
// connection so resources are freed.
//
// TS 24.501 §5.5.2.1 binds here too: the FSM ends in 5GMM-DEREGISTERED,
// so PDU sessions get a local release via SMF (PFCP §7.5.6) before the
// N1 release.
func actOnIdentityTimeout(c *fsm.Context) error {
	_ = logTransition("T3570-expired", c)
	if c == nil || c.UE == nil {
		return nil
	}
	ue := c.UE
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return nil
	}
	log := logger.Get("amf.gmm.fsm.action")
	log.WithIMSI(ue.IMSI).Infof("T3570 final expiry amfUeID=%d — aborting Identification + releasing N1 NAS (TS 24.501 §5.4.3)",
		ue.AmfUeNGAPID)
	releaseAllPDUSessions(ue, log)
	// TODO(arch: event: UE-Context-Release to NGAP — see gmm/doc.go)
	_ = uectxrelease.SendCommand(gnb, ue,
		uectxrelease.CauseNAS(genngap.CauseNasUnspecified))
	return nil
}

// logTransition is a stub observer used until per-transition NAS logic
// migrates into Actions proper. Single log line per event so operators
// can watch the FSM drive each UE through the registration ladder in
// real time.
func logTransition(tag string, c *fsm.Context) error {
	log := logger.Get("amf.gmm.fsm.action")
	if c == nil || c.UE == nil {
		return nil
	}
	log.WithIMSI(c.UE.IMSI).Debugf("[%s] amfUeID=%d event=%s",
		tag, c.UE.AmfUeNGAPID, c.Event)
	return nil
}

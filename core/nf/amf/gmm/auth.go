// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5G-AKA Authentication procedure (TS 24.501 §5.4.1) — AMF side.
//
// Port of nf/amf/gmm/gmm_authentication.py. The AMF is the SEAF in this
// deployment; the AUSF lives in-process (nf/ausf) but the 3GPP boundary
// is respected — the AMF calls ausf.GenerateAV and never touches subscriber
// K/OP directly. Flow:
//
//  1. startAuthentication → ausf.GenerateAV returns (RAND, AUTN, XRES*,
//     KAUSF, KSEAF, KAMF); AMF sends Authentication Request NAS message
//     and the FSM arms T3560 (TS 24.501 Table 10.2.1) with N3560=4
//     retransmits before final expiry.
//  2. handleAuthenticationResponse → compare UE RES* with stored XRES*
//     (constant-time), cancel T3560, transition to Security Mode Command.
//  3. handleAuthenticationFailure → log + abort registration; synch
//     failure bumps the TS 28.552 AUTH.FailSQN counter so operators can
//     see SQN re-syncs in /api/kpis.
//
// ── Spec-compliance gaps (§5.4.1.3.7 Abnormal cases) ─────────────────
//
// TODO(spec: TS 24.501 §5.4.1.3.7 a) — Lower layer failure before
//   AUTH RESPONSE received: the network shall abort the procedure.
//   We rely on the gNB-disconnect path (NGAP SCTP FSM fires
//   EvCommLost → cascade) to tear down UE state; the spec-correct
//   hook is more specific — abort the 5G-AKA procedure and release
//   the N1 NAS signalling connection.
//
// TODO(spec: TS 24.501 §5.4.1.3.7 b) — T3560 expiry handling:
//   on the 5th (final) expiry the network shall abort the 5G-AKA
//   procedure AND any ongoing 5GMM procedure AND release the N1 NAS
//   signalling connection. Today FSM TimerSpec.MaxRetransmit=4
//   gives us 4 retransmits but the "release N1 NAS" step on final
//   expiry isn't wired; we just drop back to StateDeregistered.
//
// TODO(spec: TS 24.501 §5.4.1.3.7 c, d) — On AUTH FAILURE #20 or
//   #26, the AMF MAY initiate IDENTIFICATION (§5.4.3) to re-verify
//   the 5G-GUTI ↔ SUPI mapping before deciding to reject or retry.
//   Current implementation unconditionally retries — see the
//   per-cause TODOs in handleAuthenticationFailure below.
//
// ── Spec-compliance gaps (TS 33.501 — security) ──────────────────────
//
// TODO(spec: TS 33.501 §6.1.2) — SUCI vs SUPI choice in
//   Nausf_UEAuthentication_Authenticate Request: the SEAF shall
//   include SUPI only when it has a valid 5G-GUTI and is
//   re-authenticating; otherwise SUCI. Our ausf.GenerateAV takes
//   ue.IMSI (SUPI) always — we've already de-concealed SUCI upstream
//   so the AUSF never sees the SUCI. Correct for an in-process AUSF,
//   but breaks when we split AUSF to N12: the boundary needs to
//   carry SUCI, not SUPI, on first registration.
//
// TODO(spec: TS 33.501 §6.1.3.2 step 9) — SEAF-side HRES* check: the
//   SEAF shall compute HRES* from RES* (Annex A.5) and compare
//   against HXRES* from the AUSF. We bypass this because the
//   in-process AUSF returns XRES* directly and we compare RES*==XRES*
//   (auth.go:226). When AUSF splits over N12 the SEAF must do
//   SHA-256(RAND||RES*)-truncate comparison per A.5 instead.
//
// TODO(spec: TS 33.501 §6.1.3.2 final + Annex A.7) — SEAF derives KAMF
//   from KSEAF, ABBA, SUPI; the AUSF only returns KSEAF. Our AUSF
//   returns KAMF pre-derived as a convenience. Correct in-process;
//   move to strict A.7 KDF in the SEAF once AUSF is a distinct NF.
//
// TODO(spec: TS 33.501 §6.1.3.2 step 11 + §6.1.4.1a) — home-network
//   auth confirmation: after successful RES* verification the AUSF
//   shall invoke Nudm_UEAuthentication_ResultConfirmation to the
//   UDM linking the auth result to subsequent procedures. Required
//   for "increased home control" (fraud prevention). Not wired.
//
// TODO(spec: TS 33.501 §6.1.3.3.2) — sync-failure flow proper split:
//   SEAF sends Nausf_UEAuthentication_Authenticate with
//   "synchronisation failure indication" + RAND + AUTS, waits for
//   AUSF response BEFORE initiating new auth. We call
//   ausf.UpdateSQNOnSyncFailure then immediately startAuthentication
//   synchronously in-process; the ordering constraint becomes
//   material once AUSF is over N12.
package gmm

import (
	"bytes"
	"encoding/hex"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	nas "github.com/mmt/nasgen/generated"
	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/uectxrelease"
	"github.com/mmt/mmt-studio-core/nf/amf/security"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/ausf"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

func init() {
	Register(MsgAuthenticationResponse, handleAuthenticationResponse)
	Register(MsgAuthenticationFailure, handleAuthenticationFailure)
}

// startAuthentication runs the full Nausf_UEAuthentication round-trip and
// ships the Authentication Request to the UE.
func startAuthentication(ue *uectx.AmfUeCtx) {
	log := logger.Get("amf.gmm.authentication")
	if ue.IMSI == "" {
		log.Errorf("startAuthentication amfUeID=%d: no IMSI", ue.AmfUeNGAPID)
		return
	}

	// ABBA is 2 bytes, default 0x0000 (TS 33.501 §A.7).
	abba := []byte{0x00, 0x00}
	ue.Security.ABBA = abba

	// TS 24.501 v19.6.2 §5.4.1.3.2 — the AMF "initiates the 5G AKA
	// based primary authentication and key agreement procedure by
	// sending an AUTHENTICATION REQUEST message to the UE and starting
	// the timer T3560". §5.4.1.3.4 acknowledges the network may
	// "initiate a new 5G AKA based primary authentication and key
	// agreement procedure" while a prior round's ngKSI is still
	// stored. Each new round is by definition not yet "complete" —
	// AuthDone (the implementation flag that gates duplicate-response
	// dedup in handleAuthenticationResponse) must be cleared so the
	// new round's AUTHENTICATION RESPONSE is processed instead of
	// being silently dropped.
	ue.Security.AuthDone = false

	mcc, mnc, err := splitPLMN(ue.IMSI)
	if err != nil {
		log.Errorf("startAuthentication amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	sn := sacrypto.ServingNetworkName(mcc, mnc)
	// TODO(arch: sbi-N12: Nausf_UEAuthentication_Authenticate) —
	//   specs/3gpp/ts_129509v190500p.pdf §5.2 "UEAuthentications"
	//   collection: POST /ue-authentications returns a Nausf
	//   authentication context (authType="5G_AKA", RAND/AUTN/XRES*HASH).
	//   The AUSF is a separate NF per TS 23.501 §6.2.6. This in-process
	//   function call must become an HTTP/2 SBI invocation once AUSF
	//   is split out. See doc.go "Architectural policy".
	av, err := ausf.GenerateAV(ue.IMSI, sn, abba)
	if err != nil {
		log.WithIMSI(ue.IMSI).Errorf("ausf.GenerateAV amfUeID=%d: %v",
			ue.AmfUeNGAPID, err)
		pm.Inc(pm.AuthFail, 1)
		// TS 24.501 §5.5.1.2.5 + §5.4.1.2 — subscriber unknown / no
		// credentials = "Illegal UE" (cause #3). Python reference sends
		// REGISTRATION REJECT cause #3 and tears down the N1 NAS
		// signalling connection; we mirror here.
		abortRegistrationAndReleaseN1(ue, CauseIllegalUE,
			genngap.CauseNasAuthenticationFailure)
		return
	}

	// Park the vector on the UE context for the matching response handler.
	ue.Security.RAND = av.RAND
	ue.Security.AUTN = av.AUTN
	ue.Security.XRESStar = av.XRESStar
	ue.Security.KAUSF = av.KAUSF
	ue.Security.KSEAF = av.KSEAF
	ue.Security.KAMF = av.KAMF
	ue.GMMProc = uectx.GMMProcRegistration
	ue.GMMSub = uectx.GMMSubAuthentication

	// NGKSI selection per TS 24.501 v19.6.2 — two SHALL clauses apply
	// to the outbound AUTHENTICATION REQUEST's ngKSI value:
	//
	//   §5.4.1.3.2 (Authentication initiation by the network) verbatim:
	//     "If an ngKSI is contained in an initial NAS message during
	//      a 5GMM procedure, the network shall include a different
	//      ngKSI value in the AUTHENTICATION REQUEST message when it
	//      initiates a 5G AKA based primary authentication and key
	//      agreement procedure."
	//
	//   §5.4.1.3.4 (Authentication completion by the network) verbatim:
	//     "If the 5G AKA based primary authentication and key
	//      agreement procedure has been completed successfully and the
	//      related ngKSI is stored in the 5G NAS security context of
	//      the network, the network shall include a different ngKSI
	//      value in the AUTHENTICATION REQUEST message when it
	//      initiates a new 5G AKA based primary authentication and key
	//      agreement procedure."
	//
	// Both clauses apply to real key IDs only (0..6). Value 7 "no key
	// is available" (§9.11.3.32) is not a key identifier — when the
	// UE sends ngKSI=7 there is no §5.4.1.3.2 constraint, and when no
	// prior round has been taken into use (Activated=false) there is
	// no §5.4.1.3.4 constraint.
	//
	// §5.4.1.3.7 item (e) "ngKSI already in use" (cause #71) is the
	// UE's enforcement of the §5.4.1.3.2 obligation; honouring it on
	// emission prevents that abnormal-case round-trip.
	//
	// The chosen ngKSI is stored on the non-current (pending) slot
	// per §4.4.2.1 — the operative ctx's NGKSI is the "stored ngKSI"
	// that §5.4.1.3.4 refers to and must remain visible to this
	// rotation decision; PromoteContext copies pending → operative on
	// SECURITY MODE COMPLETE per §5.4.2.4.
	ue.Security.PendingNGKSI = chooseNewNGKSI(ue)
	ue.Security.NGKSIAssigned = true

	// Build Authentication Request (TS 24.501 §8.2.1).
	req := &nas.AuthenticationRequest{
		NgKSI: nas.NASKeySetIdentifier{TSC: 0, KeySetIdentifier: ue.Security.PendingNGKSI},
		ABBA:  nas.ABBA{Value: abba},
	}
	rand := nas.AuthenticationParameterRAND{RAND: av.RAND}
	autn := nas.AuthenticationParameterAUTN{AUTN: av.AUTN}
	req.AuthenticationParameterRAND = &rand
	req.AuthenticationParameterAUTN = &autn

	encoded, err := req.Encode()
	if err != nil {
		log.Errorf("AuthenticationRequest encode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.Errorf("startAuthentication amfUeID=%d: gNB %q gone", ue.AmfUeNGAPID, ue.GnbKey)
		return
	}
	// TODO(arch: event: DL-NAS to NGAP) — DownlinkNASTransport is an
	//   NGAP procedure (TS 38.413 §8.6.2). Today we call dlnas.Send
	//   directly; the spec-shape is: GMM fires a "Send DL NAS" event
	//   carrying {gnb, ue, nasBytes}, NGAP consumes it and builds the
	//   DownlinkNASTransport PDU. Same across every dlnas.Send in gmm/*.
	if err := dlnas.Send(gnb, ue, encoded); err != nil {
		log.Errorf("DL Authentication Request amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	// Cache for T3560 auth-leg retransmit (TS 24.501 §10.2 N3560=4).
	ue.RetxNASPDU = encoded
	pm.Inc(pm.AuthAtt, 1)
	log.WithIMSI(ue.IMSI).Infof("Authentication Request sent amfUeID=%d ngKSI=%d (UE-NASKSI=%d, prior-stored=%d/activated=%t) rand=%s",
		ue.AmfUeNGAPID, ue.Security.PendingNGKSI,
		ue.NASKSI, ue.Security.NGKSI, ue.Security.Activated,
		hex.EncodeToString(av.RAND))

	// T3560 auth-leg (TS 24.501 §5.4.1.3 / Table 10.2.1) is armed
	// declaratively by the GMM FSM on entry to StateAuthentication —
	// see fsm_transitions.go. Expiry surfaces as EvT3560AuthExpired.
}

func handleAuthenticationResponse(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.authentication")
	// TS 24.501 §5.1.3 — AUTHENTICATION RESPONSE is only valid in
	// StateAuthentication. Anything else = out-of-order UE transmit,
	// most likely a late retransmit after we've already progressed
	// past auth. Dropping is safer than re-running startSecurityMode
	// against stale ue.Security.XRESStar.
	if !allowedIn(ue, "AUTHENTICATION RESPONSE", fsm.StateAuthentication) {
		return
	}

	// TS 24.501 §5.4.1.3.4 (Authentication completion by the network)
	// specifies only "stop timer T3560 and check RES*" on receipt of
	// an Auth Response; it is silent on duplicate arrivals. Silent
	// drop is an implementation-defined idempotency choice — and the
	// right one here: re-running startSecurityMode would ship a
	// second SMC with a fresh DL NAS COUNT (§4.4.3.1 mandates count
	// increment per transmission, incl. retransmits) while the UE has
	// already committed to the first SMC's count. Net effect: the
	// UE's expected DL count drifts from ours and later UL NAS MAC
	// verification fails on the AMF side.
	if ue.Security != nil && ue.Security.AuthDone {
		log.WithIMSI(ue.IMSI).Debugf("AuthenticationResponse (dup) amfUeID=%d — auth already done, ignoring",
			ue.AmfUeNGAPID)
		return
	}

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("AuthenticationResponse decode: %v", err)
		pm.Inc(pm.AuthFail, 1)
		// TS 24.501 §5.5.1.2.8 b — protocol error on inbound NAS.
		// Record the state transition before the abort helper removes
		// the ctx so operators see "AUTHENTICATION → DEREGISTERED on
		// AuthenticationResponse(Invalid)" in logs.
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthResponseInvalid, Inner: inner})
		abortRegistrationAndReleaseN1(ue, CauseProtocolError,
			genngap.CauseNasUnspecified)
		return
	}
	ar, ok := msg.(*nas.AuthenticationResponse)
	if !ok {
		log.Errorf("AuthenticationResponse: unexpected type %T", msg)
		pm.Inc(pm.AuthFail, 1)
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthResponseInvalid, Inner: inner})
		abortRegistrationAndReleaseN1(ue, CauseProtocolError,
			genngap.CauseNasUnspecified)
		return
	}
	if ar.AuthenticationResponseParameter == nil {
		log.Warnf("AuthenticationResponse missing response parameter — MAC failure? amfUeID=%d",
			ue.AmfUeNGAPID)
		pm.Inc(pm.AuthFailMAC, 1)
		// TS 24.501 section 5.4.1.3.2: send Auth Reject if verification not possible
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthResponseInvalid, Inner: inner})
		sendAuthenticationReject(ue)
		return
	}

	ueRESStar := ar.AuthenticationResponseParameter.Value
	if !bytes.Equal(ueRESStar, ue.Security.XRESStar) {
		log.WithIMSI(ue.IMSI).Warnf("RES* mismatch amfUeID=%d", ue.AmfUeNGAPID)
		// TS 33.501 section 6.1.3.2: RES* verification failed
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthResponseInvalid, Inner: inner})
		sendAuthenticationReject(ue)
		return
	}

	// K_SEAF / K_AMF were derived by the AUSF and already parked on the UE
	// context at startAuthentication time. Just flip the "auth done" flag.
	ue.Security.AuthDone = true
	pm.Inc(pm.AuthSucc, 1)
	log.WithIMSI(ue.IMSI).Infof("Authentication successful amfUeID=%d", ue.AmfUeNGAPID)

	// Send SMC first, THEN advance the FSM. T3560-smc is armed by the
	// EvAuthResponseValid transition (see fsm_transitions.go); firing
	// after startSecurityMode ensures ue.RetxNASPDU already holds the
	// SMC bytes when the timer's retransmit callback is wired.
	startSecurityMode(ue)
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthResponseValid, Inner: inner})
}

// maxAuthRetries is the limit before sending AuthenticationReject.
const maxAuthRetries = 3

func handleAuthenticationFailure(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	if !allowedIn(ue, "AUTHENTICATION FAILURE", fsm.StateAuthentication) {
		return
	}
	log := logger.Get("amf.gmm.authentication")

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("AuthenticationFailure decode: %v", err)
		pm.Inc(pm.AuthFail, 1)
		pm.Inc(pm.RegFail, 1)
		// Decode failure on AUTH FAILURE — treat as terminal. The
		// EvAuthenticationFailure transition moves FSM to DEREGISTERED
		// and cancels T3560.
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthenticationFailure, Inner: inner})
		ue.GMMProc = uectx.GMMProcNone
		ue.GMMSub = uectx.GMMSubNone
		return
	}
	af, ok := msg.(*nas.AuthenticationFailure)
	if !ok {
		log.Errorf("AuthenticationFailure: unexpected type %T", msg)
		pm.Inc(pm.AuthFail, 1)
		pm.Inc(pm.RegFail, 1)
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthenticationFailure, Inner: inner})
		ue.GMMProc = uectx.GMMProcNone
		return
	}

	cause := af.Cause5GMM.Value
	log.Warnf("AuthenticationFailure amfUeID=%d cause=0x%02X", ue.AmfUeNGAPID, cause)

	// TS 24.501 section 5.4.1.3: retry limit
	ue.Security.AuthRetryCount++
	if ue.Security.AuthRetryCount >= maxAuthRetries {
		log.Errorf("Auth retry limit (%d) reached amfUeID=%d — sending AuthenticationReject",
			maxAuthRetries, ue.AmfUeNGAPID)
		// Terminal outcome — reject + ctx removal. Fire the terminal
		// event so the FSM drops to DEREGISTERED before the ctx is
		// removed by sendAuthenticationReject → abortAuthAndReleaseN1.
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthenticationFailure, Inner: inner})
		sendAuthenticationReject(ue)
		return
	}

	// Retry path — startAuthentication ships a fresh AUTH REQUEST, then
	// we fire EvAuthRetry so the FSM swaps T3560-auth (cancel → re-arm)
	// on the self-loop StateAuthentication → StateAuthentication.
	switch cause {
	case 20: // MAC failure — TS 24.501 §5.4.1.3.7 c)
		// TODO(spec: TS 24.501 §5.4.1.3.7 c) — On first receipt of AUTH
		//   FAILURE #20, the network MAY initiate the IDENTIFICATION
		//   procedure (§5.4.3) to obtain SUCI and verify the 5G-GUTI ↔ SUPI
		//   mapping. If the mapping was wrong, re-send AUTHENTICATION
		//   REQUEST with the correct SUPI. If it was right, terminate
		//   with AUTHENTICATION REJECT (§5.4.1.3.5). Today we just retry,
		//   which is subtly wrong when the cause root is a GUTI mismatch.
		pm.Inc(pm.AuthFail, 1)
		startAuthentication(ue)

	case 21: // Synch failure — TS 24.501 §5.4.1.3.7 f)
		pm.Inc(pm.AuthFailSQN, 1)
		// TS 33.102 §6.3.5: AUTS IE carries SQN_ms resync.
		// TODO(spec: TS 24.501 §5.4.1.3.7 f + NOTE 4) — After two
		//   consecutive AUTH FAILURE #21, the network may terminate with
		//   AUTHENTICATION REJECT. We retry forever up to maxAuthRetries
		//   regardless. Also the spec requires deleting unused AVs for
		//   the SUPI on SQN re-sync; UpdateSQNOnSyncFailure does not
		//   yet surface that state back to our local cache.
		if af.AuthenticationFailureParameter != nil && len(af.AuthenticationFailureParameter.AUTS) >= 14 {
			// TODO(arch: sbi-N12: Nausf_UEAuthentication_Authenticate
			//   "confirmation" — TS 29.509) — SQN re-sync is sent as a
			//   UE Authentication Ctx request with AUTS and expected
			//   back via Nausf over N12, not an in-process helper.
			if err := ausf.UpdateSQNOnSyncFailure(ue.IMSI, af.AuthenticationFailureParameter.AUTS, ue.Security.RAND); err != nil {
				log.Warnf("SQN resync failed amfUeID=%d: %v", ue.AmfUeNGAPID, err)
			}
		}
		// Re-initiate authentication with fresh AV
		startAuthentication(ue)

	case 26: // non-5G authentication unacceptable — TS 24.501 §5.4.1.3.7 d)
		// TODO(spec: TS 24.501 §5.4.1.3.7 d) — Same treatment as cause
		//   #20: MAY initiate identification, verify GUTI ↔ SUPI, then
		//   re-auth or reject. Today we fall through to generic retry.
		pm.Inc(pm.AuthFail, 1)
		startAuthentication(ue)

	case 71: // ngKSI already in use — TS 24.501 v19.6.2 §5.4.1.3.7 item (e)
		// Verbatim spec text: "Upon the first receipt of an
		// AUTHENTICATION FAILURE message from the UE with 5GMM cause
		// #71 'ngKSI already in use', the network performs necessary
		// actions to select a new ngKSI and send the same 5G
		// authentication challenge to the UE."
		//
		// This is the *primary* path. NOTE 3 of the same clause permits
		// the alternative ("re-initiate the 5G AKA based primary
		// authentication and key agreement procedure") which would
		// trigger a fresh AUSF AV. We take the primary path: same
		// RAND/AUTN/XRES* parked on ue.Security from the prior round,
		// only the ngKSI changes. Side-effect: no AUSF round-trip on
		// retry, and the operative ctx remains untouched per §4.4.2.1.
		pm.Inc(pm.AuthFail, 1)
		rejected := ue.Security.PendingNGKSI
		ue.Security.PendingNGKSI = chooseNewNGKSI(ue, rejected)
		log.WithIMSI(ue.IMSI).Infof("ngKSI %d rejected as 'already in use' — retrying with ngKSI %d (same 5G authentication challenge per §5.4.1.3.7 e) amfUeID=%d",
			rejected, ue.Security.PendingNGKSI, ue.AmfUeNGAPID)
		if err := resendAuthRequestSameChallenge(ue, log); err != nil {
			log.Errorf("§5.4.1.3.7 e: failed to resend AUTH REQUEST amfUeID=%d: %v", ue.AmfUeNGAPID, err)
			// Can't continue the procedure without delivering the new
			// AUTH REQUEST — drive the FSM to its terminal failure
			// transition so T3560 is cancelled and the ctx tears down.
			_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthenticationFailure, Inner: inner})
			return
		}

	default:
		// TODO(spec: TS 24.501 Table 9.11.3.2.1) — other 5GMM causes
		//   legitimately arriving on AUTHENTICATION FAILURE (e.g. #22
		//   congestion, #23 UE security capabilities mismatch, #111
		//   protocol error) need distinct handling. Generic retry risks
		//   infinite loops on unrecoverable errors.
		log.Warnf("Unhandled auth failure cause %d amfUeID=%d — retrying", cause, ue.AmfUeNGAPID)
		startAuthentication(ue)
	}
	// Retry survived the switch — fire EvAuthRetry so the FSM swaps T3560.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvAuthRetry, Inner: inner})
}

// sendAuthenticationReject builds and sends AUTHENTICATION REJECT
// (TS 24.501 §8.2.3) then releases the N1 NAS signalling connection.
//
// Spec (§5.4.1.3.5):
//   "the network should send an AUTHENTICATION REJECT message to the UE.
//    The network shall maintain, if any, the 5GMM-context and 5G NAS
//    security context of the UE unchanged."
//
// Per the UE-side receipt rules (§5.4.1.3.5), upon AUTH REJECT the UE
// will enter 5U3 ROAMING NOT ALLOWED and wipe its 5G-GUTI / TAI list /
// ngKSI — i.e. it will not come back with this GUTI. We therefore
// release the N1 NAS connection and mark our ctx deregistered.
//
// TODO(spec: TS 24.501 §5.4.1.3.5 "5G-GUTI was used") — when the initial
//   NAS identity was 5G-GUTI (not SUCI), the network SHOULD initiate an
//   identification procedure first to retrieve the SUCI and restart
//   authentication with it; only after a second failure do we reject.
//   We don't distinguish GUTI vs SUCI auth-trigger today.
func sendAuthenticationReject(ue *uectx.AmfUeCtx) {
	log := logger.Get("amf.gmm.authentication")
	pm.Inc(pm.AuthFail, 1)
	pm.Inc(pm.RegFail, 1)

	reject := &nas.AuthenticationReject{}
	encoded, err := reject.Encode()
	if err != nil {
		log.Errorf("AuthenticationReject encode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		// Fall through to the teardown below so we don't leak the ctx.
	} else if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
		// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
		if err := dlnas.Send(gnb, ue, encoded); err != nil {
			log.Errorf("DL AuthenticationReject amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		}
	}
	log.WithIMSI(ue.IMSI).Warnf("Authentication Reject sent amfUeID=%d", ue.AmfUeNGAPID)

	if ue.Security != nil {
		ue.Security.AuthDone = false
	}
	// TS 24.501 v19.6.2 §5.4.1.3.5 (AUTH REJECT) + §4.4.2.1 — auth
	// reject aborts the procedure; any non-current ctx that ActivateCtx
	// may have prepared must be discarded so a subsequent registration
	// starts from the operative ctx alone.
	security.DiscardPending(ue)
	// N1 NAS release + ctx removal. No REGISTRATION REJECT is sent here —
	// AUTHENTICATION REJECT already signalled the outcome to the UE.
	abortAuthAndReleaseN1(ue)
}

// abortAuthAndReleaseN1 is the §5.4.1.3.5 N1-NAS teardown tail.
// Separated from abortRegistrationAndReleaseN1 so callers can choose
// between "send AUTH REJECT then release" and the full registration
// abort path.
func abortAuthAndReleaseN1(ue *uectx.AmfUeCtx) {
	if ue == nil {
		return
	}
	if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
		// TODO(arch: event: UE-Context-Release to NGAP — see gmm/doc.go)
		_ = uectxrelease.SendCommand(gnb, ue,
			uectxrelease.CauseNAS(genngap.CauseNasAuthenticationFailure))
	}
	ue.GMMProc = uectx.GMMProcNone
	ue.GMMSub = uectx.GMMSubNone
	uectx.Default.Remove(ue)
}

// splitPLMN lifts MCC/MNC off the IMSI prefix. Operator MNC width isn't
// carried in the digit string itself — the 2-digit split is the lab
// default (matches the Python reference's fallback for 001/01 deployments).
func splitPLMN(imsi string) (mcc, mnc string, err error) {
	if len(imsi) < 5 {
		return "", "", fmt.Errorf("IMSI %q too short to extract MCC/MNC (need >= 5 digits)", imsi)
	}
	return imsi[:3], imsi[3:5], nil
}

// chooseNewNGKSI picks the ngKSI value to carry in the next outbound
// AUTHENTICATION REQUEST per TS 24.501 v19.6.2:
//
//	§5.4.1.3.2: "If an ngKSI is contained in an initial NAS message
//	             during a 5GMM procedure, the network shall include a
//	             different ngKSI value in the AUTHENTICATION REQUEST
//	             message …"
//	§5.4.1.3.4: "If the 5G AKA based primary authentication and key
//	             agreement procedure has been completed successfully
//	             and the related ngKSI is stored in the 5G NAS
//	             security context of the network, the network shall
//	             include a different ngKSI value in the AUTHENTICATION
//	             REQUEST message when it initiates a new 5G AKA based
//	             primary authentication and key agreement procedure."
//
// Both clauses apply to real key IDs (0..6). Value 7 "no key is
// available" (§9.11.3.32) is not a key identifier and is never sent
// in this position.
//
// alsoExclude lets the caller add transient exclusions — used by the
// AUTH FAILURE cause #71 retry path (§5.4.1.3.7 item (e)) to also
// avoid the just-rejected value.
//
// The decision is read-only on ue.Security; the caller is responsible
// for parking the result on ue.Security.PendingNGKSI per §4.4.2.1 so
// the operative ("current") ctx remains in use until PromoteContext
// runs on SECURITY MODE COMPLETE (§5.4.2.4).
func chooseNewNGKSI(ue *uectx.AmfUeCtx, alsoExclude ...uint8) uint8 {
	excluded := map[uint8]bool{}
	if ue.NASKSI < 7 {
		excluded[uint8(ue.NASKSI)] = true // §5.4.1.3.2
	}
	if ue.Security != nil && ue.Security.Activated &&
		ue.Security.NGKSIAssigned && ue.Security.NGKSI < 7 {
		excluded[ue.Security.NGKSI] = true // §5.4.1.3.4
	}
	for _, k := range alsoExclude {
		if k <= 6 {
			excluded[k] = true
		}
	}
	for i := uint8(0); i <= 6; i++ {
		if !excluded[i] {
			return i
		}
	}
	// At most three values can be excluded (NASKSI, stored, rejected);
	// the 0..6 range has seven, so the loop above always finds one.
	// Fall through is a defensive default for hypothetical future
	// callers adding more exclusions than the range can absorb.
	return 0
}

// resendAuthRequestSameChallenge rebuilds the AUTHENTICATION REQUEST
// with the SAME 5G authentication challenge (RAND/AUTN/ABBA already
// parked on ue.Security) and the NEW ngKSI carried in
// ue.Security.PendingNGKSI. This implements the primary path of
// TS 24.501 v19.6.2 §5.4.1.3.7 item (e) — verbatim: "the network
// performs necessary actions to select a new ngKSI and send the same
// 5G authentication challenge to the UE."
//
// No AUSF round-trip happens here; XRESStar/KAUSF/KSEAF/KAMF stay
// valid because they're derived from RAND/AUTN which are unchanged.
// Caller MUST ensure ue.Security.PendingNGKSI has been updated to the
// new value BEFORE invoking this helper.
func resendAuthRequestSameChallenge(ue *uectx.AmfUeCtx, log *logger.Logger) error {
	if ue.Security == nil || len(ue.Security.RAND) == 0 || len(ue.Security.AUTN) == 0 {
		return fmt.Errorf("no cached 5G authentication challenge on ctx")
	}
	req := &nas.AuthenticationRequest{
		NgKSI: nas.NASKeySetIdentifier{TSC: 0, KeySetIdentifier: ue.Security.PendingNGKSI},
		ABBA:  nas.ABBA{Value: ue.Security.ABBA},
	}
	rand := nas.AuthenticationParameterRAND{RAND: ue.Security.RAND}
	autn := nas.AuthenticationParameterAUTN{AUTN: ue.Security.AUTN}
	req.AuthenticationParameterRAND = &rand
	req.AuthenticationParameterAUTN = &autn

	encoded, err := req.Encode()
	if err != nil {
		return fmt.Errorf("AUTH REQUEST encode: %w", err)
	}
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return fmt.Errorf("gNB %q gone", ue.GnbKey)
	}
	if err := dlnas.Send(gnb, ue, encoded); err != nil {
		return fmt.Errorf("DL AUTH REQUEST: %w", err)
	}
	ue.RetxNASPDU = encoded
	pm.Inc(pm.AuthAtt, 1)
	log.WithIMSI(ue.IMSI).Infof("Authentication Request resent amfUeID=%d ngKSI=%d (§5.4.1.3.7 e: same challenge)",
		ue.AmfUeNGAPID, ue.Security.PendingNGKSI)
	return nil
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GMM Registration procedure (TS 24.501 §5.5.1).
//
// Port of nf/amf/gmm/gmm_registration.py — happy path only. The AMF:
//
//  1. Decodes the Registration Request NAS message.
//  2. Extracts the 5GSMobileIdentity — either an IMSI-SUCI (null-scheme)
//     or a 5G-GUTI pointing at a prior context.
//  3. When SUPI is known → startAuthentication (see auth.go).
//     When SUPI is unknown → send Identity Request and wait for response.
//  4. Registration Complete moves the UE to RM-REGISTERED.
//
// The "send Registration Accept" step is called from smc.go after Security
// Mode Complete — see sendRegistrationAccept() below.
//
// ── Spec-compliance gaps (§5.5.1.2.8 Abnormal cases on the network side) ──
//
// TODO(spec: TS 24.501 §5.5.1.2.8 a) — Lower layer failure before
//
//	Registration Complete: AMF shall locally abort the procedure, enter
//	5GMM-REGISTERED, NOT retransmit Registration Accept, and keep both
//	old and new 5G-GUTIs valid. We currently rely on the FSM fallthrough
//	and don't track the "both GUTIs valid" window.
//
// TODO(spec: TS 24.501 §5.5.1.2.8 b) — Protocol error on Registration
//
//	Request: AMF shall reject with one of causes #96 (invalid mandatory
//	information), #99 (IE non-existent or not implemented), #100
//	(conditional IE error), #111 (protocol error, unspecified). Our
//	decode-error path just logs and increments RegFail without sending
//	a REGISTRATION REJECT.
//
// TODO(spec: TS 24.501 §5.5.1.2.8 c) — T3550 retransmit policy:
//
//	on the first 4 expirations retransmit Registration Accept; on the
//	5th expiration abort and enter 5GMM-REGISTERED while keeping both
//	GUTIs valid. The FSM's TimerSpec.MaxRetransmit + OnExpiry handles
//	the N3550=4 retransmit loop declaratively, but the dual-GUTI-valid
//	"abort" behaviour is not coded.
//
// TODO(spec: TS 24.501 §5.5.1.2.8 d) — Duplicate RR between Accept and
//
//	Complete: if IEs differ, abort and run the new procedure; if IEs
//	are identical, resend Accept and restart T3550 without incrementing
//	the retransmit counter. No IE-level diff today.
//
// TODO(spec: TS 24.501 §5.5.1.2.8 e) — Duplicate RR before Accept/Reject:
//
//	same IE-diff check; abort-and-restart or ignore.
//
// TODO(spec: TS 24.501 §5.5.1.2.8 f) — RR in 5GMM-REGISTERED: run the
//
//	common procedures again; if the UE is the same, delete 5GMM context
//	and progress the new RR; otherwise treat as non-genuine. We simply
//	overwrite the ctx today.
//
// TODO(spec: TS 24.501 §5.5.1.2.8 g..j) — rare cases (authentication
//
//	failure with specific causes, implicit dereg during registration, etc.)
//	not audited in detail here.
//
// ── Spec-compliance gaps (TS 33.501 — security) ──────────────────────
//
// TODO(spec: TS 33.501 §6.8.1.3) — current-vs-non-current context:
//
//	"For the case that this security context is non-current in the
//	 AMF, the AMF shall delete any existing current 5G security
//	 context and make the used 5G NAS security context the current
//	 5G security context." When a RR protects its container with a
//	 native context whose ngKSI is cached but not the "current" one
//	 for this UE in this PLMN, we should swap current↔cached. Today
//	 we only detect the reuse case via canReuseCachedContext and the
//	 notion of "current vs non-current native context" isn't
//	 modelled — the ue.Security context is treated as monolithic.
//
// TODO(spec: TS 33.501 §6.8.1.1.2.2) — full-native-context reuse:
//
//	when a full native 5G NAS security context is available in the
//	AMF, AMF policy MAY decide to skip primary auth and SMC. We do
//	this via §4.4 (sendRegistrationAcceptReusedContext) but per
//	§6.8.1.1.2.2 the AMF MAY also run a fresh primary authentication
//	+ NAS SMC anyway "based on AMF policy" — no hook today for that
//	policy; a policy knob (e.g. `amf_policy.force_reauth_on_reuse`)
//	belongs in network_config.
//
// TODO(spec: TS 33.501 §6.4.2.2) — parallel NAS connections in the
//
//	same PLMN: separate UL/DL NAS COUNTs per access type
//	(3GPP / non-3GPP). We only track one UL/DL pair per UE. Required
//	when non-3GPP access is wired.
package gmm

import (
	"bytes"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/ctx"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/kpi"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/security"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	nas "github.com/mmt/nasgen/generated"
	"github.com/mmt/nasgen/pkg/runtime"
)

func init() {
	Register(MsgRegistrationRequest, handleRegistrationRequest)
	Register(MsgRegistrationComplete, handleRegistrationComplete)
}
func handleRegistrationRequest(ue *uectx.AmfUeCtx, _ uint8, inner []byte, outerPDU []byte) {
	log := logger.Get("amf.gmm.registration")
	pm.Inc(pm.RegAtt, 1)

	// TS 24.501 §5.5.1.2.8 d/e — duplicate RR handling.
	// The dispatcher's checkCollision has already run; by the time we
	// get here, if GMMProc was set, it's been reset to let the new RR
	// progress. We still need to handle the two "same-IE" sub-cases:
	//
	//   d.2) RR arrives after Accept, before Complete, with IEs
	//        identical to the previous RR → resend the cached
	//        Registration Accept and "restart T3550 without
	//        incrementing the retx counter".
	//   e.2) RR arrives before Accept/Reject, with IEs identical →
	//        ignore the duplicate.
	//
	// Diff is on the inner plaintext bytes. The 5GMM body has no
	// sequence number (that's in the outer security wrapper) so byte-
	// equality is a sound proxy for IE-equality. On differing bytes,
	// the current abort-and-restart behaviour (initiated by
	// checkCollision) stands.
	if len(ue.LastRegRequestPDU) > 0 && bytes.Equal(inner, ue.LastRegRequestPDU) {
		switch ue.RM {
		case uectx.RMRegistered:
			// RM-REGISTERED already, nothing to resend — spec §5.5.1.2.8 f
			// covers RR in REGISTERED (re-run common procedures); leave
			// it to handlers' normal path. Fall through.
		default:
			if len(ue.RetxNASPDU) > 0 {
				log.WithIMSI(ue.IMSI).Infof("duplicate RegistrationRequest (identical IEs) amfUeID=%d — resending cached Accept per TS 24.501 §5.5.1.2.8 d.2",
					ue.AmfUeNGAPID)
				if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
					// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
					_ = dlnas.Send(gnb, ue, ue.RetxNASPDU)
				}
				return
			}
			log.WithIMSI(ue.IMSI).Infof("duplicate RegistrationRequest (identical IEs) amfUeID=%d — ignoring per TS 24.501 §5.5.1.2.8 e.2",
				ue.AmfUeNGAPID)
			return
		}
	}

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		// TS 24.501 §5.5.1.2.8(b) — protocol error: AMF shall send
		// REGISTRATION REJECT with one of #96 / #99 / #100 / #111.
		// causeForNASDecodeError walks the NASDecodeError chain to
		// pick the most specific cause; falls back to #111 when the
		// failure can't be discriminated.
		cause := causeForNASDecodeError(err)
		log.Errorf("RegistrationRequest decode amfUeID=%d: %v — aborting cause #%d",
			ue.AmfUeNGAPID, err, cause)
		pm.Inc(pm.RegFail, 1)
		// Mark the procedure as registration so the abort helper sends
		// REGISTRATION REJECT (the UE is clearly in the middle of
		// registration — it sent an RR).
		ue.GMMProc = uectx.GMMProcRegistration
		abortRegistrationAndReleaseN1(ue, cause,
			genngap.CauseNasUnspecified)
		return
	}
	rr, ok := msg.(*nas.RegistrationRequest)
	if !ok {
		// TS 24.501 §5.5.1.2.8 b — the inner decoded to something
		// other than a RegistrationRequest. Protocol error #111.
		log.Errorf("RegistrationRequest: unexpected type %T — aborting cause #%d",
			msg, CauseProtocolError)
		pm.Inc(pm.RegFail, 1)
		ue.GMMProc = uectx.GMMProcRegistration
		abortRegistrationAndReleaseN1(ue, CauseProtocolError,
			genngap.CauseNasUnspecified)
		return
	}

	// Record key registration parameters so the UI can surface them.
	ue.RegistrationType = regTypeName(rr.RegistrationType5GS.RegistrationType)
	ue.NASKSI = int(rr.NgKSI.KeySetIdentifier)
	ue.GMMProc = uectx.GMMProcRegistration

	// TS 24.501 v19.6.2 §9.11.3.7 Table 9.11.3.7.1 — the
	// RegistrationType5GS half-octet IE carries the 3-bit Registration
	// type (bits 3..1) AND the 1-bit Follow-on request indicator (FOR,
	// bit 4). Network-side semantics per §5.5.1.2.6 verbatim:
	// "If the UE has set the Follow-on request indicator to 'Follow-on
	//  request pending' in the REGISTRATION REQUEST message, or the
	//  network has downlink signalling pending, the AMF shall not
	//  immediately release the NAS signalling connection after the
	//  completion of the registration procedure."
	// Our current N1 release path is gNB-driven (UEContextReleaseRequest
	// on radio inactivity — see nf/amf/ngap/uectxrelease/ctxrelease.go),
	// so we implicitly satisfy the "shall not immediately release" rule
	// for every FOR value. The flag is captured here for observability
	// + future proactive-release-on-FOR=0 policy decisions.
	ue.FollowOnRequest = rr.RegistrationType5GS.FollowOnRequestPending ==
		nas.RegistrationType5GSFollowOnRequestPending

	// UE security capabilities (NEA0..3, NIA0..3). Stored raw; the SMC
	// replay IE reuses these bytes (TS 24.501 §8.2.25).
	if rr.UESecurityCapability != nil {
		ue.Security.UESecCap = rr.UESecurityCapability.Value
	}

	// TS 24.501 §5.5.1.2.4 "MICO mode": capture the UE's request so
	// sendRegistrationAccept can echo it when AMF policy accepts.
	ue.MICORequested = rr.MICOIndication != nil

	// TS 24.501 §4.4.6 — detect "case (a)": UE had no valid 5G NAS
	// security context and sent the RR with cleartext IEs only (no
	// NAS Message Container IE). On this registration, the AMF must
	// set the RINMR bit in SECURITY MODE COMMAND (§5.4.2.2 "Retransmission
	// of the initial NAS message requested") so the UE packs the full
	// RR into SMC Complete's NASMessageContainer. Case (b.1) had the
	// container on the outer RR — dispatch already re-dispatched on
	// the inner before reaching us, so rr.NASMessageContainer is nil
	// on both paths here; we distinguish via a hint from dispatch.
	//
	// For now: flip to true when RequestedNSSAI and other non-cleartext
	// IEs are empty. Better hint in dispatch is a future refinement.
	ue.InitialRRCleartextOnly = (rr.RequestedNSSAI == nil && rr.MICOIndication == nil &&
		rr.PDUSessionStatus == nil && rr.UplinkDataStatus == nil &&
		rr.UEStatus == nil)

	// ── Inbound IE processing gaps ────────────────────────────────────
	// The following Registration Request IEs (TS 24.501 Table 8.2.6.1.1)
	// are parsed by the codec into `rr` but are not acted on yet. Each
	// TODO names the spec clause that says what the AMF shall do with
	// the IE when present. Leaving `rr.<Field>` untouched means the AMF
	// accepts whatever default the rest of the procedure implies.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "MICO mode") — rr.MICOIndication:
	//   when the UE requests MICO mode, decide whether to accept and
	//   echo in Registration Accept. If rejected, the UE reverts to
	//   non-MICO. Today we ignore the request entirely.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "LADN indication") — rr.LADNIndication:
	//   if present, determine LADN DNN(s) to provide in Registration Accept
	//   per subscription and registration area intersection.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "T3512 value") — rr.RequestedDRXParameters:
	//   the UE may request DRX cycle values; the AMF shall negotiate.
	//   Today we use a hardcoded T3512=6h.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "UE radio capability ID") — rr.UERadioCapabilityID:
	//   if the UE presents a known capability ID the AMF MAY use it; if
	//   unknown, the AMF shall request Capability via NGAP UE Radio
	//   Capability Check (TS 38.413 §8.14.1).
	//
	// TODO(spec: TS 24.501 §5.5.1.3 mobility) — rr.LastVisitedRegisteredTAI:
	//   for mobility updating, cross-check TAC and flag service-area
	//   restrictions; emit Service area list IE in Registration Accept.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "PDU session status") — rr.PDUSessionStatus:
	//   compare against AMF's PDU session list and release the ones
	//   the UE has already torn down. Sync AMF↔UE state.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "UplinkDataStatus") — rr.UplinkDataStatus:
	//   used to resume user-plane resources only for the PDU sessions
	//   the UE has pending UL data on. Drives ICS's PDUSessionResourceSetup
	//   IE list; today we always request setup for all active sessions.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "AllowedPDUSessionStatus") — rr.AllowedPDUSessionStatus:
	//   for emergency registration, carries the UE's existing emergency
	//   PDU session. We don't support emergency registration yet.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.2 "5GMM capability") — rr.MMCapability5G:
	//   ~40 capability bits (S1 mode, HO attach, MPSI, UP CIoT, V2X,
	//   ProSe, UAS, MINT, …). Stash on ue for later feature-gating
	//   decisions (emergency, CIoT, V2X, ProSe).
	//
	// TODO(spec: TS 24.501 §5.5.1.2.2 "S1 UE network capability") — rr.S1UENetworkCapability:
	//   if N26 interworking is configured, propagate to MME via
	//   Forward-Relocation / Handover Request.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "UE usage setting") — rr.UEUsageSetting:
	//   voice-centric vs data-centric. Voice-centric affects IMS-voice
	//   fallback decisions; we don't fall back to EPS today.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "UEParametersUpdateStatus") — rr.UEParametersUpdateStatus:
	//   echo of UPU status; used by UDM to confirm UPU acknowledgement.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "RequestedMappedNSSAI") — rr.RequestedMappedNSSAI:
	//   for EPS→5GS interworking, map E-UTRA S-NSSAIs to 5GS; NSSF
	//   selection shall honour the mapping.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "AdditionalInformationRequested") — rr.AdditionalInformationRequested:
	//   UE is asking for specific additional info (e.g. Ciphering Key
	//   Data). AMF shall include when available.

	// Resolve SUPI BEFORE the summary log so [IMSI:...] is on the
	// RegistrationRequest line too. For SUCI-null-scheme this is a
	// plain MSIN extraction; for SUCI-ECIES it's a KDB lookup +
	// decrypt — still sub-millisecond. When resolution fails
	// (ErrNoSUPI / other) the log still fires without the IMSI
	// prefix (logger.WithIMSI("") omits the tag, not a placeholder).
	supi, err := ResolveSUPI(rr.MobileIdentity5GS, ue.IMSI)
	if err == nil {
		ue.IMSI = supi
		// Pair the IMSI write with timer-manager registration so
		// future T3550 / T3560 / etc. log lines carry [IMSI:…]
		// (infra/timers/manager.go logEvent).
		timers.M.RegisterUE(fmt.Sprintf("%d", ue.AmfUeNGAPID), supi)
	}

	log.WithIMSI(ue.IMSI).Infof("RegistrationRequest amfUeID=%d type=%s FOR=%v NGKSI=%d identityType=%s",
		ue.AmfUeNGAPID, ue.RegistrationType, ue.FollowOnRequest, ue.NASKSI,
		identityTypeName(rr.MobileIdentity5GS))

	// KPI hook (TS 28.554 §6 RM-RegSR/RM-RegMeanTime). Idempotent on
	// retransmissions for the same amfUeID — first call wins for
	// latency timing. Paired with RecordSuccess/RecordFailure at the
	// FSM terminal transitions below.
	kpi.RecordAttempt(ue.AmfUeNGAPID)

	switch err {
	case nil:
		log.WithIMSI(supi).Debugf("SUPI resolved amfUeID=%d from %s",
			ue.AmfUeNGAPID, identityTypeName(rr.MobileIdentity5GS))

		// NAS Message Container expansion (TS 24.501 §4.4.6) happens at
		// the dispatch layer (see expandNASMessageContainer in dispatch.go)
		// BEFORE both the FSM event and this handler fire — so the `rr`
		// we decoded above is already the inner RegistrationRequest when
		// a container IE was present on the wire. No unwrap here.

		// Stash the inner for §5.5.1.2.8 d/e duplicate detection on the
		// next RR arrival.
		ue.LastRegRequestPDU = append(ue.LastRegRequestPDU[:0], inner...)

		// TS 24.501 §5.5.1.2.5 + TS 23.502 §4.2.2.2.2 — subscriber
		// existence gate ahead of NSSF selection. UDM is the
		// authoritative store for provisioned SUPIs; a nil result
		// (subscriber not provisioned) must surface as cause #3
		// "Illegal UE", not #62 "No network slices available".
		// Without this gate, an unknown UE whose Requested NSSAI
		// happened not to intersect AMF/gNB allowed slices was
		// rejected with the misleading #62 — the requested NSSAI
		// can't be evaluated for an identity the network has no
		// record of, and #62 is reserved for "subscriber known but
		// no slice acceptable" (§5.5.1.2.5 line 7256-7258).
		// startAuthentication has the same check on its slow path
		// (ausf.GenerateAV → udm.GetAuthData); this fast path keeps
		// the rejection cause stable regardless of NSSAI overlap.
		if creds, gerr := udm.GetAuthData(supi); gerr != nil || creds == nil {
			log.WithIMSI(supi).Warnf("RegistrationRequest: subscriber not provisioned in UDM — aborting cause #3 per TS 24.501 §5.5.1.2.5")
			pm.Inc(pm.RegFail, 1)
			ue.GMMProc = uectx.GMMProcRegistration
			abortRegistrationAndReleaseN1(ue, CauseIllegalUE,
				genngap.CauseNasAuthenticationFailure)
			return
		}

		runNSSFSelection(ue, rr)

		// TS 24.501 v19.6.2 §5.5.1.2.5 (Initial registration not
		// accepted) + §9.11.3.2 cause #62 "No network slices
		// available". When NSSF returns Allowed=[] even after the
		// post-3bd0c43 default-subscribed fallback (no requested
		// S-NSSAI was permitted AND no subscribed default could be
		// served), abort with #62 rather than driving auth/SMC/ICS
		// against an empty AllowedNSSAI. Reached only for KNOWN
		// subscribers — unknown SUPIs were already rejected with
		// #3 by the UDM gate above.
		// §5.5.1.2.5 line 7256-7258 explicitly permits sending
		// RegReject #62 unprotected when no NAS context exists.
		if allowedNSSAIEmpty(ue) {
			log.WithIMSI(ue.IMSI).Warnf("RegistrationRequest: NSSF returned empty Allowed NSSAI — aborting cause #62 per TS 24.501 §5.5.1.2.5")
			pm.Inc(pm.RegFail, 1)
			ue.GMMProc = uectx.GMMProcRegistration
			abortRegistrationAndReleaseN1(ue, CauseNoNetworkSlicesAvailable,
				genngap.CauseNasUnspecified)
			return
		}

		// TS 24.501 §4.4 — If the UE presents an ngKSI that matches a
		// cached valid native 5G NAS security context for this SUPI,
		// take that context into use on the new N1 NAS signalling
		// connection WITHOUT a new primary authentication and WITHOUT
		// an SMC round-trip. Full text at pdf §4.4 (v19.6.2):
		// "The 5G NAS security context which is indicated by an ngKSI
		// can be taken into use … when a new N1 NAS signalling
		// connection is established without executing a new primary
		// authentication and key agreement procedure."
		// Remove any lingering UE ctx under this SUPI before we (re)insert.
		// Without this, a re-registration that doesn't qualify for the
		// §4.4 cached-context reuse path (e.g. NGKSI mismatch, no cached
		// AuthDone) overwrites byIMSI[supi] on the Insert below and
		// orphans the old ctx in byAmfID. The reuse-success branch
		// migrates state then calls Remove itself before Insert, so this
		// is harmless there (Remove is idempotent against an already-
		// removed ctx).
		existing := uectx.Default.LookupByIMSI(supi)
		// Two reuse sources per TS 24.501 v19.6.2 §4.4 "5G NAS security
		// contexts":
		//   (i)  existing != ue — a distinct prior ctx found by SUPI;
		//        new ctx from InitialUEMessage needs security state
		//        migrated from the prior ctx.
		//   (ii) existing == ue — same ctx, already populated (typical
		//        after initialue.go's 5G-S-TMSI lookup resolved the
		//        prior ctx directly). No migration needed.
		// Both cases follow §4.4's "The 5G NAS security context which
		// is indicated by an ngKSI can be taken into use … without
		// executing a new primary authentication and key agreement
		// procedure" rule and must MAC-verify the inbound RR.
		if existing == nil {
			existing = ue
		}
		if canReuseCachedContext(existing, ue.NASKSI) {
			// TS 24.501 §4.4.4.3 + §5.4.3 + TS 33.501 §6.4.4 — before
			// accepting the RegistrationRequest on the strength of the
			// cached context, verify the integrity MAC the UE attached
			// using the cached KNASInt, and advance the cached
			// ULNasCount to match the received sequence number. Skips
			// verification only when the PDU arrived plain (SHT=0) —
			// which the UE MAY send per §4.4.4.3 when it holds no
			// current context, but here we already found a cached one,
			// so a plain re-registration is a key-desync signal → fall
			// through to full primary auth.
			//
			// Two migration cases (see comment above on `existing`):
			//   (i)  existing != ue — ue is freshly allocated by
			//        initialue (no keys). dispatch.stripSecurity saw
			//        len(ue.Security.KNASInt)==0 and skipped the MAC
			//        path, so we MUST run secureUnwrap(existing, …)
			//        here to verify with the cached keys.
			//   (ii) existing == ue — the 5G-S-TMSI reuse path in
			//        initialue.go resolved the prior ctx directly. It
			//        already carries the full NAS security state, so
			//        dispatch.stripSecurity ALREADY ran secureUnwrap
			//        and advanced ULNasCount by 1. A second call here
			//        would re-verify against a now-advanced count and
			//        falsely fail — skip it. The first verify is
			//        authoritative; §4.4's "verify on the strength of
			//        the cached context" requirement is met by it.
			if existing == ue {
				log.WithIMSI(supi).Infof("reusing cached 5G NAS security context amfUeID=%d (ngKSI=%d) — MAC already verified at dispatch, skipping primary auth (TS 24.501 §4.4)",
					ue.AmfUeNGAPID, ue.Security.NGKSI)
				if err := sendRegistrationAcceptReusedContext(ue); err != nil {
					log.Errorf("reuse-context registration amfUeID=%d: %v — falling back to full auth",
						ue.AmfUeNGAPID, err)
					ue.Security.NGKSIAssigned = false
					startAuthentication(ue)
					_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvRegistrationRequest, Inner: inner})
					return
				}
				// sendRegistrationAcceptReusedContext fires
				// EvRegRequestContextValid itself — do not re-fire.
				reconcilePDUSessionsOnRegistration(ue, rr)
				return
			}
			if sht := securityHeaderType(outerPDU); sht != 0 {
				if verr := security.Reuse(existing, outerPDU); verr != nil {
					log.WithIMSI(supi).Warnf("cached-context MAC verify failed amfUeID=%d (prev amfUeID=%d): %v — discarding cached ctx, running primary auth",
						ue.AmfUeNGAPID, existing.AmfUeNGAPID, verr)
					if existing != ue {
						uectx.Default.Remove(existing)
						uectx.Default.Insert(ue)
					}
					startAuthentication(ue)
					// TS 24.501 v19.6.2 §5.4.1.2 — the AUTHENTICATION
					// REQUEST that startAuthentication just shipped
					// puts the procedure into the "Authentication
					// procedure initiated" state at the network
					// (§5.4.1.3). The receiving-state guard for the
					// UE's AUTHENTICATION RESPONSE (§5.1.3.2) is the
					// GMM FSM's StateAuthentication. Fire the same
					// EvRegistrationRequest event used by the
					// fresh-auth fall-through below so the FSM
					// advances; otherwise the Auth Response lands
					// while the FSM is still at StateRegistered and
					// gets dropped by state_guard.
					_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvRegistrationRequest, Inner: inner})
					return
				}
				log.WithIMSI(supi).Infof("reusing cached 5G NAS security context amfUeID=%d (prev amfUeID=%d ngKSI=%d) — MAC verified, skipping primary auth (TS 24.501 §4.4)",
					ue.AmfUeNGAPID, existing.AmfUeNGAPID, existing.Security.NGKSI)
				if existing != ue {
					// Cross-ctx migration (case i). Same-ctx (case ii)
					// already has the security state on `ue`, so the
					// migrate+remove+insert dance is a no-op-equivalent
					// but expensive; skip it.
					migrateSecurityContext(existing, ue)
					uectx.Default.Remove(existing)
					uectx.Default.Insert(ue)
				}
				if err := sendRegistrationAcceptReusedContext(ue); err != nil {
					log.Errorf("reuse-context registration amfUeID=%d: %v — falling back to full auth",
						ue.AmfUeNGAPID, err)
					ue.Security.NGKSIAssigned = false // let startAuthentication re-pick
					startAuthentication(ue)
					return
				}
				// TS 23.502 v19.7.0 §4.2.2.2.2 step 17 —
				// "If the List Of PDU Sessions To Be Activated is
				// included in the Registration Request in step 1,
				// the AMF sends Nsmf_PDUSession_UpdateSMContext
				// Request to SMF(s) associated with the PDU
				// Session(s) in order to activate User Plane
				// connections of these PDU Session(s). Steps from
				// step 5 onwards described in clause 4.2.3.2 are
				// executed to complete the User Plane connection
				// activation…" — and mirror-clause for release when
				// UE's PDU Session status says the session is
				// INACTIVE at the UE.
				reconcilePDUSessionsOnRegistration(ue, rr)
				return
			}
			log.WithIMSI(supi).Infof("cached context found amfUeID=%d but RR arrived plain (SHT=0) — running primary auth per TS 24.501 §4.4.4.3",
				ue.AmfUeNGAPID)
			uectx.Default.Remove(existing)
		}

		// Fall-through (non-reuse) orphan guard: if an existing ctx was
		// found but we didn't take the reuse path, drop it now so the
		// Insert below doesn't orphan it in byAmfID.
		if existing != nil && existing != ue {
			uectx.Default.Remove(existing)
		}
		uectx.Default.Insert(ue)
		startAuthentication(ue)
		// Fresh-auth path — fire EvRegistrationRequest so the FSM moves
		// the current state → StateAuthentication and arms T3560. The
		// transition table has a row from every live state (Deregistered,
		// Registered, RegisteredInitiated, Identification, SecurityMode,
		// Authentication self-loop).
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvRegistrationRequest, Inner: inner})
	case ErrNoSUPI:
		// Need an Identity Request round-trip before we can authenticate.
		sendIdentityRequest(ue, IdentityTypeSUCI)
	default:
		log.Errorf("ResolveSUPI amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		// TS 24.501 §5.5.1.2.5: reject with cause #9 "UE identity
		// cannot be derived by the network"; release N1 NAS so the
		// UE has to set up a fresh connection for the retry.
		abortRegistrationAndReleaseN1(ue, CauseUEIdentityCannotBeDerived,
			genngap.CauseNasUnspecified)
	}
}
func handleRegistrationComplete(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	if !allowedIn(ue, "REGISTRATION COMPLETE", fsm.StateRegisteredInitiated) {
		return
	}
	log := logger.Get("amf.gmm.registration")
	ueKey := fmt.Sprintf("%d", ue.AmfUeNGAPID)
	// T3550 is cancelled by the FSM on leaving StateRegisteredInitiated.

	// TS 24.501 §5.5.1.2.4 specifies "stop timer T3550 and change to
	// state 5GMM-REGISTERED" on receipt; duplicate Registration
	// Complete handling isn't spelled out. Drop duplicates as
	// implementation-defined idempotency — prevents redundant PM
	// counter bumps and any future handler side-effects from running
	// twice when the UE re-ACKs our T3550 retransmit.
	if ue.RM == uectx.RMRegistered {
		log.WithIMSI(ue.IMSI).Debugf("RegistrationComplete (dup) amfUeID=%d — already registered, ignoring",
			ue.AmfUeNGAPID)
		return
	}

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("RegistrationComplete decode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	if _, ok := msg.(*nas.RegistrationComplete); !ok {
		log.Errorf("RegistrationComplete: unexpected type %T", msg)
		return
	}

	ue.RM = uectx.RMRegistered
	ue.CM = uectx.CMConnected
	ue.GMMProc = uectx.GMMProcNone
	ue.GMMSub = uectx.GMMSubNone
	pm.Inc(pm.RegSucc, 1)
	_ = ueKey // retained for any future local timer use; T3512 is FSM-owned now.

	// T3512 (TS 24.501 §5.3.7) is started declaratively by the GMM FSM on
	// entry to StateRegistered (RegistrationComplete transition). Expiry
	// surfaces as EvT3512Expired and drops the FSM back to DEREGISTERED.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "Upon receiving a REGISTRATION COMPLETE") —
	//   on Registration Complete the AMF shall delete any "old TAI list"
	//   and "old allowed NSSAI" cached for prior N1 NAS connections.
	//   We don't maintain those slots, so implicit no-op — leave the
	//   explicit reset here if we ever cache previous values.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "LP-WUS disabled status") —
	//   if the REGISTRATION COMPLETE contains the LP-WUS status IE
	//   the AMF shall store "LP-WUS disabled" in the 5GMM context of
	//   the UE (or delete it when status = "LP-WUS enabled"). Codec
	//   already parses the IE; we just need to drive state from it.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 + TS 23.502 §4.2.3) — after
	//   Registration Complete the AMF shall trigger SMSF registration
	//   via Nsmsf_SMService_Activate if SMS was granted in the 5GS
	//   registration result. We don't run the SMSF round-trip.
	//
	// TODO(spec: TS 24.501 §5.5.1.2.4 "SOR transparent container") —
	//   when the SoR container was included in Registration Accept,
	//   the UE's Registration Complete MAY include a SOR transparent
	//   container with the UE acknowledgement. The AMF forwards the
	//   ack to the UDM. We neither send nor forward SoR today.

	log.WithIMSI(ue.IMSI).Infof("Registration complete amfUeID=%d — UE REGISTERED",
		ue.AmfUeNGAPID)

	// KPI: terminal success transition. Records latency delta from
	// the RecordAttempt at RegistrationRequest receive time.
	kpi.RecordSuccess(ue.AmfUeNGAPID)

	// Advance FSM: REGISTERED_INITIATED → REGISTERED. T3550 is cancelled,
	// T3512 (periodic reg / implicit dereg) is armed by the transition.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvRegistrationComplete, Inner: inner})
}

// 5GS Registration Type values (TS 24.501 §9.11.3.7 Table 9.11.3.7.1).
// bits 3..1 of the RegistrationType5GS half-octet IE. Bits 4..1 also
// encode the Follow-on request indicator (bit 4); the value field is
// masked to 0x07 before comparison.
const (
	RegTypeInitial   uint8 = 0x01 // Initial registration
	RegTypeMobility  uint8 = 0x02 // Mobility registration updating
	RegTypePeriodic  uint8 = 0x03 // Periodic registration updating
	RegTypeEmergency uint8 = 0x04 // Emergency registration
	RegTypeSNPN      uint8 = 0x05 // SNPN onboarding registration
	RegTypeDisaster  uint8 = 0x06 // Disaster roaming mobility reg update
	RegTypeReserved  uint8 = 0x07 // Reserved
	regTypeMask      uint8 = 0x07
)

func regTypeName(v uint8) string {
	switch v & regTypeMask {
	case RegTypeInitial:
		return "initial"
	case RegTypeMobility:
		return "mobility"
	case RegTypePeriodic:
		return "periodic"
	case RegTypeEmergency:
		return "emergency"
	case RegTypeSNPN:
		return "snpn-onboarding"
	case RegTypeDisaster:
		return "disaster-roaming-mobility"
	case RegTypeReserved:
		return "reserved"
	}
	return fmt.Sprintf("unknown(%d)", v)
}
func identityTypeName(id runtime.MobileIdentity5GS) string {
	switch id.(type) {
	case *runtime.SUCI:
		return "SUCI"
	case *runtime.GUTI5G:
		return "5G-GUTI"
	case *runtime.IMEI:
		return "IMEI"
	case *runtime.STMSI5G:
		return "5G-S-TMSI"
	case *runtime.NoIdentity:
		return "None"
	}
	return fmt.Sprintf("%T", id)
}

// quiet unused warnings for callers imported via init() in tests.
var _ = ctx.Default

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Extracted from registration.go by refactor: split god-file by
// sub-concern. Imports are re-derived by goimports.
package gmm

import (
	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/security"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	nas "github.com/mmt/nasgen/generated"
)

// reconcilePDUSessionsOnRegistration implements TS 23.502 v19.7.0
// §4.2.2.2.2 step 17 — after a successful (mobility / initial with
// cached context) registration, the AMF reconciles its PDU session
// records against the UE's view carried in the RR:
//
//	Uplink data status IE (TS 24.501 §9.11.3.57)  → "List Of PDU
//	   Sessions To Be Activated" (§4.2.2.2.2 step 17, first bullet).
//	   For each PSI with bit=1, we fire a user-plane reactivation
//	   following §4.2.3.2 step 5 onwards: NGAP PDU Session Resource
//	   Setup Request to the gNB (no fresh 5GSM Accept piggyback — the
//	   session already exists at the UE), gNB returns its new DL TEID,
//	   pdusetup.handleResponse calls session.ActivateUserPlane which
//	   flips the UPF FAR BUFF→FORW.
//
//	PDU session status IE (TS 24.501 §9.11.3.44) → §4.2.2.2.2 step 17
//	   second bullet: "If any PDU Session status indicates that it is
//	   released at the UE, the AMF invokes the
//	   Nsmf_PDUSession_ReleaseSMContext service operation towards the
//	   SMF in order to release any network resources related to the
//	   PDU Session." A 0 bit for a PSI the SMF still has → we release
//	   the stale network record.
//
// Sessions the SMF has but the UE has NOT asked to activate in
// UplinkDataStatus stay SUSPENDED — the UE can trigger later via
// Service Request / upper-layer traffic.
func reconcilePDUSessionsOnRegistration(ue *uectx.AmfUeCtx, rr *nas.RegistrationRequest) {
	log := logger.Get("amf.gmm.registration")
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)

	// §9.11.3.44 PDU session status: bit=0 → "PDU SESSION INACTIVE"
	// at the UE. Release at SMF for every PSI the SMF still records.
	if rr.PDUSessionStatus != nil && gnb != nil {
		inactive := inactivePSIsFromStatus(rr.PDUSessionStatus.PSIs)
		for _, psi := range inactive {
			if sess := session.Default.Get(ue.IMSI, psi); sess != nil {
				log.WithIMSI(ue.IMSI).Infof("§4.2.2.2.2 step 17: UE reports pduSessID=%d INACTIVE — releasing SMF/UPF state",
					psi)
				_ = session.Release(ue.IMSI, psi) // returned Release Command not shipped; UE already considers session inactive
			}
		}
	}

	// §9.11.3.57 Uplink data status: bit=1 → "UL data pending AND
	// user-plane resources not established". Reactivate SUSPENDED
	// sessions per §4.2.3.2 step 10-12.
	//
	// Queue the PSIs onto ue.PendingN1N2Sessions so pdusetup.Send is
	// issued from drainPendingReactivations once the NGAP per-UE FSM
	// reaches StateEstablished (the OnContextEstablished hook wired
	// in service.go:76). A synchronous pdusetup.Send here would fire
	// EvPDUResourceSetupRequestSent while the NGAP FSM is still at
	// StateICSPending — the FSM transition table only allows that
	// event from StateEstablished, producing two cascading
	// "NGAP procedure collision" WARNINGs per cycle. The deferred
	// pattern matches the Service Request reactivation path
	// (queueReactivationsFromUplinkDataStatus in service.go:264) —
	// same drainPendingReactivations hook, same dedup behaviour.
	if rr.UplinkDataStatus == nil || gnb == nil {
		return
	}
	toActivate := rr.UplinkDataStatus.PSIs
	for _, psi := range toActivate {
		sess := session.Default.Get(ue.IMSI, psi)
		if sess == nil {
			log.WithIMSI(ue.IMSI).Warnf("§4.2.2.2.2 step 17: UE asked to activate pduSessID=%d but SMF has no record — ignoring",
				psi)
			continue
		}
		if sess.State != session.StateSuspended {
			log.WithIMSI(ue.IMSI).Debugf("§4.2.2.2.2 step 17: pduSessID=%d already in state=%s — skipping reactivate",
				psi, sess.State)
			continue
		}
		// Dedup against any PSI the SMF's §4.2.3.3 N1N2 path already
		// queued (mirrors service.go:269-285).
		already := false
		for _, q := range ue.PendingN1N2Sessions {
			if q == psi {
				already = true
				break
			}
		}
		if already {
			continue
		}
		ue.PendingN1N2Sessions = append(ue.PendingN1N2Sessions, psi)
		log.WithIMSI(ue.IMSI).Infof("§4.2.2.2.2 step 17: pduSessID=%d queued for UP-activate after ICS Response (TS 23.502 §4.2.3.2 step 10-12)",
			psi)
	}
}

// inactivePSIsFromStatus inverts a PDU session status (§9.11.3.44):
// the typed PSIBitmap.PSIs lists PSIs whose bit is SET (=1, not
// inactive). Inactive PSIs are the COMPLEMENT in the 1..15 range.
// PSI 0 is spare and never returned.
func inactivePSIsFromStatus(setPSIs []uint8) []uint8 {
	have := make(map[uint8]bool, len(setPSIs))
	for _, p := range setPSIs {
		have[p] = true
	}
	var psis []uint8
	for psi := uint8(1); psi <= 15; psi++ {
		if !have[psi] {
			psis = append(psis, psi)
		}
	}
	return psis
}

// applyContainerRRToUE processes the inner RegistrationRequest carried
// in a NAS Message Container IE on SECURITY MODE COMPLETE (TS 24.501
// §4.4.6 case a: UE's initial RR was cleartext-only; the "entire"
// RR lands here after security activation). The non-cleartext IEs
// update UE state; RequestedNSSAI re-drives NSSF selection so the
// Registration Accept carries the right AllowedNSSAI/RejectedNSSAI.
//
// Not a state-machine event — the SMC Complete transition has already
// advanced the FSM. This is a pure state-enrichment pass before ICS +
// Registration Accept are shipped.
func applyContainerRRToUE(ue *uectx.AmfUeCtx, containerBytes []byte, log *logger.Logger) {
	// NEA0 (our dev config) is identity ⇒ container is plaintext.
	// NEA1/NEA2/NEA3 are deciphered via security.DecipherContainer.
	if ue.Security != nil && ue.Security.EEA != 0 && len(ue.Security.KNASEnc) == 16 {
		dec, derr := security.DecipherContainer(ue, ue.Security.ULNasCount, containerBytes)
		if derr != nil {
			log.WithIMSI(ue.IMSI).Warnf("SMC Complete NASMessageContainer decipher (EEA=%d) failed: %v — skipping case (a) enrichment",
				ue.Security.EEA, derr)
			return
		}
		containerBytes = dec
	}

	innerMsg, derr := nas.DecodeNASMessage(containerBytes)
	if derr != nil {
		log.WithIMSI(ue.IMSI).Warnf("SMC Complete NASMessageContainer inner decode: %v", derr)
		return
	}
	rr, ok := innerMsg.(*nas.RegistrationRequest)
	if !ok {
		log.WithIMSI(ue.IMSI).Warnf("SMC Complete NASMessageContainer inner is %T, not RegistrationRequest", innerMsg)
		return
	}

	log.WithIMSI(ue.IMSI).Infof("SMC Complete carries §4.4.6 case(a) full RR — applying non-cleartext IEs amfUeID=%d",
		ue.AmfUeNGAPID)

	// MICO indication (§9.11.3.31) — non-cleartext.
	ue.MICORequested = rr.MICOIndication != nil

	// Re-run NSSF selection with the now-visible RequestedNSSAI so
	// sendRegistrationAccept includes the correct AllowedNSSAI and any
	// rejected slices. §4.6.2 Requested ∩ Subscribed ∩ AMF ∩ gNB ∩ TA.
	runNSSFSelection(ue, rr)

	// Same cause-#62 gate as the SUCI-path entry — see the comment at
	// the matching block earlier in this file.
	if allowedNSSAIEmpty(ue) {
		log := logger.Get("amf.gmm.registration")
		log.WithIMSI(ue.IMSI).Warnf("RegistrationRequest (case-a §4.4.6): NSSF returned empty Allowed NSSAI — aborting cause #62 per TS 24.501 §5.5.1.2.5")
		pm.Inc(pm.RegFail, 1)
		abortRegistrationAndReleaseN1(ue, CauseNoNetworkSlicesAvailable,
			genngap.CauseNasUnspecified)
		return
	}

	// TODO(spec: TS 24.501 §5.5.1.2.4) — other non-cleartext IEs to
	//   apply from the case-(a) container: MMCapability5G (5GMM
	//   capability bits), S1UENetworkCapability, UplinkDataStatus,
	//   PDUSessionStatus, AllowedPDUSessionStatus, UEUsageSetting,
	//   RequestedDRXParameters, LADNIndication, UERadioCapabilityID,
	//   UEParametersUpdateStatus, RequestedMappedNSSAI,
	//   AdditionalInformationRequested. Each needs either state
	//   enrichment on ue, or a side-effect (e.g. PDU session
	//   reconciliation). Skipping per scope — Phase 3 covers these.
}

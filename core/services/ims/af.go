// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// IMS-side Application Function (AF) glue — the CSCF acts as the AF
// for the call, mapping the SIP/SDP offered media into a service
// authorization request that the PCF then turns into PCC rule
// installs over N7 to the SMF.
//
// Spec anchors:
//   * TS 23.228 §5.4.7 "Resource reservation flows" — the AF
//     authorizes media resources at session set-up, not before.
//     specs/3gpp/ts_123228v190600p.pdf.
//   * TS 23.503 §6.1.3.10 "Subscription to events for application
//     usage detection and bearer level event reporting" + §6.2.1
//     "PCF" — AF Service Information triggers PCC rule changes.
//   * TS 29.514 §4.2.2 "Npcf_PolicyAuthorization_Create" — the SBI
//     successor to the EPC Diameter Rx AAR. nf/pcf/pcf.go's
//     HandleAARequest is named for legacy-Rx parity with the
//     Python port; semantically it's the §4.2.2 Create.
//   * TS 29.512 §4.2.4 "Npcf_SMPolicyControl_Update" — PCF→SMF
//     N7 push of changed PCC rules.
//   * TS 23.501 §5.7.4 Table 5.7.4-1 — Standardized 5QI mapping
//     (Conversational Voice = 5QI 1, Conversational Video = 5QI 2,
//     IMS Signalling = 5QI 5).
//
// The mapping policy here is hard-wired against the standardized
// service catalogue seeded by db/seed/services.go:
//
//   m=audio  → service "conv_voice"  (5QI 1, GBR)
//   m=video  → service "conv_video"  (5QI 2, GBR)
//
// Operators wanting per-deployment overrides should change the
// services row in the DB; the catalogue lookup happens in
// pcf.HandleAARequest.
package ims

import (
	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
)

// imsPDUSessionID is the conventional PDU Session ID the tester /
// CSCF use for the IMS APN (DNN="ims") — matches psi=2 in the tester
// (mmt_studio_core_tester) and the second APN slot in
// db/seed/upf.go. Real deployments will look this up from the AMF
// UE context (TS 23.501 §5.6) at INVITE time.
const imsPDUSessionID uint8 = 2

// AuthorizeMediaFromSDP translates the offered SDP media list into an
// Npcf_PolicyAuthorization_Create / legacy-Rx AA-Request and fires it
// at the PCF for the supplied IMSI.
//
// Returns true on a clean PCF accept (decision was consumed and PCC
// rules were activated). False on lookup miss / PCF reject — the
// caller decides whether to still answer 200 OK (best-effort media)
// or to map the failure to a SIP error response (e.g. 488 Not
// Acceptable Here per RFC 3261 §13.3.1.3).
func AuthorizeMediaFromSDP(imsi string, sdp string) bool {
	if imsi == "" || sdp == "" {
		return false
	}
	media := ParseSDP(sdp)
	mediaTypes := MediaTypes(media)
	if len(mediaTypes) == 0 {
		imsLog.Infof("AF: no usable media in SDP — skipping PCF authorization (IMSI=%s)", imsi)
		return false
	}

	// Pass the per-media-line descriptors through to the PCF in the
	// generic map[string]any form HandleAARequest already accepts.
	// The PCF logs them for trace and may use bandwidth/proto in
	// future per-flow GBR shaping (TS 29.514 §5.6.2 MediaComponent).
	descriptors := make([]map[string]interface{}, 0, len(media))
	for _, m := range media {
		dir := m.Direction
		if dir == "" {
			// RFC 4566 §6: "sendrecv is the default if none of the
			// attributes is specified" at the session level. For media
			// without an explicit override we report sendrecv to the
			// PCF for hold/resume tracking parity.
			dir = DirSendRecv
		}
		descriptors = append(descriptors, map[string]interface{}{
			"type":           m.Type,
			"port":           m.Port,
			"proto":          m.Proto,
			"formats":        m.Formats,
			"bandwidth_kbps": m.BandwidthKbps,
			// RFC 4566 §6 direction — used by PCF on re-INVITE to
			// flip §8.2.7 Gate Status (TODO: end-to-end wiring of
			// direction → PCC rule GateUL/GateDL → §8.2.7 IE on the
			// SMF-side QER lives in nf/pcf + nf/smf/session).
			"direction": dir,
		})
	}

	imsLog.Infof("AF→PCF Npcf_PolicyAuthorization_Create: IMSI=%s media=%v", imsi, mediaTypes)
	if !pcf.HandleAARequest(imsi, mediaTypes, descriptors) {
		return false
	}

	// AF→PCF Create succeeded → fire the §4.2.4 N7 Update so the SMF
	// re-evaluates the SM Policy and issues a TS 24.501 §6.4.2 PDU
	// SESSION MODIFICATION COMMAND with the new GBR QoS Flow.
	//
	// The "AF_CHARGING_IDENTIFIER" trigger isn't quite right (that's
	// for charging-side triggers per TS 29.512 §5.6.3.32 PolicyControl
	// RequestTrigger); the closer match is "PLMN_CH" / "RES_MO_RE"
	// when the request is UE-initiated. For the AF-initiated case
	// the canonical trigger is implicit in the Npcf_SMPolicyControl
	// _UpdateNotify push the PCF MAY emit (§4.2.6.4 "Subscription to
	// rule installation/removal triggers"). We drive Update here
	// with no specific trigger to ask the PCF to recompute and let
	// PushNotify ship the resulting decision.
	key := smfsm.Key{IMSI: imsi, PDUSessionID: imsPDUSessionID}
	decision, err := smpolicy.Update(key, smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{"RES_MO_RE"},
	})
	if err != nil {
		// SM Policy Association may not exist yet (UE hasn't done a
		// PDU establishment) — that's a misconfig, not our failure.
		// Rules are activated locally; the next session establishment
		// will pull them via CreatePolicy.
		imsLog.Warnf("AF: Npcf_SMPolicyControl_Update for IMSI=%s pduSessID=%d: %v (rules active locally)",
			imsi, imsPDUSessionID, err)
		return true
	}

	// §4.2.3 PCF→SMF UpdateNotify push.
	if err := smpolicy.PushNotify(key, decision); err != nil {
		imsLog.Warnf("AF: Npcf_SMPolicyControl_UpdateNotify for IMSI=%s: %v", imsi, err)
		return true
	}
	imsLog.Infof("AF: Npcf_SMPolicyControl_UpdateNotify pushed to SMF for IMSI=%s pduSessID=%d (rules=%d)",
		imsi, imsPDUSessionID, len(decision.PccRules))
	return true
}

// ReleaseMedia maps to the AF Delete / Diameter STR — used when a SIP
// session terminates (BYE) and we want PCC rules deactivated.
// TS 29.514 §4.2.4 Npcf_PolicyAuthorization_Delete.
//
// Symmetric to AuthorizeMediaFromSDP: after the AF Delete deactivates
// the PCC rules at the PCF, fire a §4.2.4 SMPolicyControl_Update
// followed by §4.2.3 UpdateNotify push so the SMF can:
//
//   - PFCP §7.5.4 Modify on the UPF to remove QER + UL/DL FARs for
//     each torn-down QFI,
//   - §8.3.9 PDU SESSION MODIFICATION COMMAND with op=Delete-rule +
//     Delete-flow-description so the UE retires the GBR flow back
//     to the default 5QI=9.
//
// Captures `active` *before* termination so we know which services
// the PCF just removed; the resulting RemovedPccRules carries that
// list to the SMF (its QFIByRule map keys on ServiceName).
func ReleaseMedia(imsi string) bool {
	if imsi == "" {
		return false
	}
	imsLog.Infof("AF→PCF Npcf_PolicyAuthorization_Delete: IMSI=%s", imsi)

	// Snapshot the active rules' service names *before* deactivation.
	// HandleSessionTermination flips them to INACTIVE in the rule
	// manager, so a later GetActiveServiceNames returns nothing.
	activeBefore := pcf.DefaultPccRuleManager.GetActiveServiceNames(imsi, "ims")

	if !pcf.HandleSessionTermination(imsi) {
		return false
	}
	if len(activeBefore) == 0 {
		// No rules were active (e.g. unauthorized BYE before INVITE
		// flow) — nothing to tear down on the SMF side.
		return true
	}

	// Build the RemovedPccRules list. Only ServiceName is required;
	// the SMF resolves QFI via its QFIByRule map populated at Create.
	removed := make([]pcf.PCCRule, 0, len(activeBefore))
	for _, name := range activeBefore {
		removed = append(removed, pcf.PCCRule{ServiceName: name})
	}

	// TS 29.512 §4.2.4 Update — RES_RELEASE trigger ("Resource
	// release" per Table §4.2.3.5-1) fits AF-driven dynamic-rule
	// teardown.
	key := smfsm.Key{IMSI: imsi, PDUSessionID: imsPDUSessionID}
	decision, err := smpolicy.Update(key, smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{"RES_RELEASE"},
	})
	if err != nil {
		// SM Policy Association may not exist (UE never established a
		// PDU session) — rules are still deactivated locally; nothing
		// further to push.
		imsLog.Warnf("AF: Npcf_SMPolicyControl_Update for IMSI=%s pduSessID=%d: %v (rules deactivated locally)",
			imsi, imsPDUSessionID, err)
		return true
	}
	decision.RemovedPccRules = removed

	// §4.2.3 PCF→SMF UpdateNotify push.
	if err := smpolicy.PushNotify(key, decision); err != nil {
		imsLog.Warnf("AF: Npcf_SMPolicyControl_UpdateNotify(remove) for IMSI=%s: %v", imsi, err)
		return true
	}
	imsLog.Infof("AF: Npcf_SMPolicyControl_UpdateNotify(remove) pushed to SMF for IMSI=%s pduSessID=%d (removed=%d)",
		imsi, imsPDUSessionID, len(removed))
	return true
}

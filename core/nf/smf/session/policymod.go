// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SMF receiver for PCF-pushed Npcf_SMPolicyControl_UpdateNotify
// (TS 29.512 §4.2.3) — the in-process equivalent of the SMF's
// notification HTTP endpoint. When the PCF emits an UpdateNotify
// (e.g. because the AF authorized a new media flow at SIP INVITE
// time, see services/ims/af.go), this hook translates the new
// SmPolicyDecision into a network-initiated PDU SESSION
// MODIFICATION COMMAND and ships it down to the UE via the AMF
// DL NAS path.
//
// Spec anchors:
//
//	TS 24.501 §6.4.2 "UE-requested PDU session modification" — same
//	  message types apply on the network-initiated path (§6.3.2):
//	  PDU SESSION MODIFICATION COMMAND (type 203, §8.3.9) carrying
//	  the new Authorized QoS Rules + Flow Descriptions + Session-AMBR.
//	  PDF: specs/3gpp/ts_124501v190602p.pdf.
//
//	TS 23.502 §4.3.3 "PDU Session Modification" — stage-2 flow
//	  (PCF→SMF UpdateNotify [step 1] → SMF→AMF Namf_Communication
//	  _N1N2MessageTransfer with N1 SM container [step 2] → AMF→
//	  NG-RAN PDU Session Resource Modify Request [step 3] → AMF→UE
//	  DL NAS Transport carrying the §8.3.9 NAS [step 4]). PDF:
//	  specs/3gpp/ts_123502v190700p.pdf.
//
//	TS 23.501 §5.7 "QoS model" — the §5.7.1.6 Session-AMBR and the
//	  §5.7.4 Standardized 5QI to QoS Characteristics mapping that
//	  the PCF's SmPolicyDecision references.
//
//	TS 38.413 §8.6.2 "DownlinkNASTransport" — NGAP envelope used by
//	  the AMF DLNAS hook (set by nf/amf/hooks.go) to ship the §8.3.9
//	  NAS bytes to the serving gNB on the UE-associated SCTP stream.
//
// Hook pattern (DLNASByIMSI) keeps nf/smf/session out of the AMF
// package graph — nf/amf/ngap → nf/smf/session already exists, so a
// reverse import would be a cycle.
//
// What's wired here:
//
//   - PCF→SMF UpdateNotify         ✅ (smpolicy.OnUpdateNotify hook)
//   - SMF: build §8.3.9 NAS bytes   ✅ (with refreshed Session-AMBR)
//   - SMF→AMF (Namf_Communication
//     _N1N2MessageTransfer)         ✅ in-process via DLNASByIMSI hook
//   - AMF→gNB (DLNAS Transport)     ✅ TS 38.413 §8.6.2
//   - gNB→UE (RRC DL Information)   handled by gNB
//   - UE→AMF→SMF MOD COMPLETE       ⏳ next milestone (5GSM dispatch
//     of NAS type 204 → smpolicy.Update completion event)
package session

import (
	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	upfmgr "github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/oam/logger"

	nas "github.com/mmt/nasgen/generated"
)

// DLNASByIMSIFunc is the AMF-supplied "ship a 5GMM-wrapped NAS PDU
// to UE <imsi>" entry point. Set by nf/amf/hooks.go at init() to a
// function that does the uectx.LookupByIMSI + gnbctx.GetByIP +
// dlnas.Send dance. nil → SMF logs a warning and the §8.3.9 stays
// unsent (UE keeps its old QoS Flow until the next bearer-state event
// re-syncs).
type DLNASByIMSIFunc func(imsi string, dlNAS []byte) error

// DLNASByIMSI is set by nf/amf/hooks.go init().
var DLNASByIMSI DLNASByIMSIFunc

func init() {
	smpolicy.OnUpdateNotify = handlePolicyUpdateNotify
}

// handlePolicyUpdateNotify is the in-process delivery of the
// Npcf_SMPolicyControl_UpdateNotify (TS 29.512 §4.2.3) — runs on the
// PCF's goroutine.
//
// Steps (TS 23.502 §4.3.3):
//
//  1. Find the matching SMF session and refresh its locally-cached
//     Session-AMBR / Charging Method from the new decision.
//  2. Build the §8.3.9 PDU SESSION MODIFICATION COMMAND with the
//     refreshed Session-AMBR (PTI=0 because this is network-initiated
//     per §6.3.2; UE replies with PTI=0 in §8.3.10 COMPLETE).
//  3. Wrap in DL NAS TRANSPORT (TS 24.501 §8.2.11) and hand to the
//     AMF DL hook for shipment over NGAP DL NAS Transport (§8.6.2).
//
// Best-effort: a failure on any leg is logged, not propagated. The
// PCF FSM still transitions UpdateNotifySent → UpdateNotifyAck because
// the in-process channel always "delivers" (no transport drop).
// Future SBI port replaces this with proper HTTP ack semantics.
func handlePolicyUpdateNotify(k smfsm.Key, decision smpolicy.SmPolicyDecision) {
	log := logger.Get("smf.policymod").WithIMSI(k.IMSI)

	sess := Default.Get(k.IMSI, k.PDUSessionID)
	if sess == nil {
		log.Debugf("UpdateNotify: no SMF session for pduSessID=%d — UE not active", k.PDUSessionID)
		return
	}
	if sess.State != StateActive && sess.State != StateSuspended {
		log.Infof("UpdateNotify: session pduSessID=%d in state %s — skipping NAS modification",
			k.PDUSessionID, sess.State)
		return
	}

	// Step 1 — refresh local cache (§4.2.3.4 mandatory IEs).
	if decision.SessionAMBRDL > 0 {
		sess.AMBRDL = uint32(decision.SessionAMBRDL)
	}
	if decision.SessionAMBRUL > 0 {
		sess.AMBRUL = uint32(decision.SessionAMBRUL)
	}
	if decision.ChargingMethod != "" {
		sess.ChargingMethod = decision.ChargingMethod
	}

	// Step 1b — TS 29.244 §7.5.4 PFCP Session Modification Request (N4).
	// §7.5.4.17 lets the SMF carry "Create" IE groups inside a Modify;
	// the IE bodies follow the Establishment definitions (§7.5.2.5
	// Create QER, §7.5.2.3 Create FAR). We add one of each per new
	// QFI so the UPF dataplane has the per-flow enforcement rules
	// before the UE's UL/DL packets start hitting them. FAR / QER IDs
	// are derived from the QFI so they're stable across repeated
	// modifications. Default flow QFI=1 was provisioned at session
	// Establishment time (§7.5.2); we only add the new flows here.
	//
	// Coalesced into ONE §7.5.4 Modification per UpdateNotify via
	// upfmgr.ApplyModifyBatch — TS 29.244 §7.5.4.2 explicitly allows
	// any combination of Create-* / Update-* / Remove-* IEs in a
	// single Modification Request, and emitting one wire round-trip
	// instead of N saves ~(N-1)*RTT on the integrated-PFCP loopback
	// (RTT≈0.5ms) and on the real-CUPS N4 (RTT 1-10ms typical).
	ruleSpecs, flowSpecs, qfiByService := SpecsFromPolicyDecision(decision.PccRules, sess.QFIByRule)
	if len(qfiByService) > 0 {
		if sess.QFIByRule == nil {
			sess.QFIByRule = make(map[string]uint8, len(qfiByService))
		}
		for name, qfi := range qfiByService {
			sess.QFIByRule[name] = qfi
		}
	}
	var batch upfmgr.ModifyBatch
	for i, fs := range flowSpecs {
		// Pull GBR/MBR from the PCF rule alongside the 5QI so the
		// QER's §8.2.8 MBR / §8.2.9 GBR IEs reflect the TS 23.501
		// §5.7.4 standardized service rate for this 5QI. Zero on a
		// dimension → unmetered for that direction (UPF convention).
		gbrUL, gbrDL, mbrUL, mbrDL := pccRateLimits(decision.PccRules, fs.FiveQI)
		qer := upfmgr.QER{
			QERID: 100 + uint32(fs.QFI), // distinct from default QER=1
			QFI:   fs.QFI,
			// Gate Status §8.2.7 — 0=OPEN in both UL and DL.
			GateUL: 0, GateDL: 0,
			MBRUL: mbrUL, MBRDL: mbrDL, // §8.2.8
			GBRUL: gbrUL, GBRDL: gbrDL, // §8.2.9
		}
		batch.CreateQERs = append(batch.CreateQERs, qer)
		// Create FAR (§7.5.2.3 / §7.5.4.17) — UL forward to N6 with
		// Apply Action §8.2.26 = FORWARD (bit 1). DL FAR for the QFI
		// is deferred until the gNB activates the new flow and supplies
		// per-QFI DL TNL info via TS 38.413 §8.2.3 PDU Session Resource
		// Modify Response — until then the existing default DL FAR
		// keeps DL flowing, and adding the UL FAR alone is enough for
		// the UPF meter to classify the new flow.
		ulFAR := upfmgr.FAR{
			FARID:    100 + uint32(fs.QFI),
			Action:   1, // §8.2.26 FORWARD bit
			DstIface: 1, // §8.2.24 Destination Interface = Core (N6)
		}
		batch.CreateFARs = append(batch.CreateFARs, ulFAR)
		log.Infof("PFCP §7.5.4 batched: QFI=%d QER=%d 5QI=%d GBR(UL/DL)=%d/%d kbps MBR(UL/DL)=%d/%d kbps (rule %d/%d)",
			fs.QFI, qer.QERID, fs.FiveQI, gbrUL, gbrDL, mbrUL, mbrDL, i+1, len(flowSpecs))
	}

	// Step 1c — symmetric removal path. RemovedPccRules carries the
	// null-valued §5.6.2.4 map<string,null> entries from the AF Delete
	// (BYE) / subscription-tear-down. For each, we look up the QFI
	// allocated at install time (sess.QFIByRule) and:
	//
	//   - PFCP §7.5.4.7 Remove FAR (UL FARID=100+QFI, DL FARID=200+QFI)
	//   - PFCP §7.5.4.9 Remove QER (QERID=100+QFI)
	//   - §8.3.9 PDU SESSION MODIFICATION COMMAND with op=Delete-rule
	//     + op=Delete-flow-description so the UE retires the GBR
	//     bearer back to the default 5QI=9 best-effort flow.
	//
	// QFIByRule entry is deleted on success so a subsequent Add for
	// the same ServiceName allocates a fresh QFI (matching real PCF
	// behaviour). Removes ride the SAME §7.5.4 Modification as the
	// adds — one wire round-trip carries Create+Remove together.
	var removeRuleSpecs []QoSRuleSpec
	var removeFlowSpecs []QoSFlowSpec
	for _, rr := range decision.RemovedPccRules {
		if rr.ServiceName == "" {
			continue
		}
		qfi, ok := sess.QFIByRule[rr.ServiceName]
		if !ok {
			log.Debugf("UpdateNotify(remove): no QFI mapped for service %q — skipping (already removed?)", rr.ServiceName)
			continue
		}
		// PFCP §7.5.4.7 Remove FAR — both directions. Idempotent on
		// missing FARs (the UPF bridge accepts the DL FAR being absent
		// when the gNB never reported per-QFI TNL info).
		batch.RemoveFARs = append(batch.RemoveFARs, 100+uint32(qfi), 200+uint32(qfi))
		// PFCP §7.5.4.9 Remove QER.
		batch.RemoveQERs = append(batch.RemoveQERs, 100+uint32(qfi))
		log.Infof("PFCP §7.5.4 batched(remove): service=%s QFI=%d QER=%d FAR(UL/DL)=%d/%d",
			rr.ServiceName, qfi, 100+uint32(qfi), 100+uint32(qfi), 200+uint32(qfi))
		// §9.11.4.13.2 Delete-rule + §9.11.4.12.2 Delete-flow-description.
		// RuleID matches the install path (RuleID=QFI per
		// SpecsFromPolicyDecision).
		removeRuleSpecs = append(removeRuleSpecs, QoSRuleSpec{
			RuleID: qfi, QFI: qfi, Delete: true,
		})
		removeFlowSpecs = append(removeFlowSpecs, QoSFlowSpec{
			QFI: qfi, Delete: true,
		})
		delete(sess.QFIByRule, rr.ServiceName)
	}

	// Single coalesced §7.5.4 Modification (TS 29.244 §7.5.4.2 — any
	// mix of Create-/Update-/Remove- IEs in one request).
	if !batch.IsEmpty() {
		if err := upfmgr.Default.ApplyModifyBatch(k.IMSI, k.PDUSessionID, batch); err != nil {
			log.Warnf("PFCP §7.5.4 batched modify failed: %v", err)
		}
	}

	// Step 2 — TS 24.501 §8.3.9 PDU SESSION MODIFICATION COMMAND.
	// PTI=0 marks the network as the initiator (§4.3.2 PTI rules).
	// We emit:
	//
	//   §8.3.9.3  Session-AMBR        — refreshed §5.7.1.6 aggregate
	//   §8.3.9.6  Authorized QoS rules — §9.11.4.13 (one rule per
	//                                    AF-activated PCC rule, no
	//                                    packet filters yet)
	//   §8.3.9.8  Authorized QoS flow — §9.11.4.12 (one flow desc
	//             descriptions          per rule, 5QI parameter only)
	//
	// GFBR / MFBR / Averaging Window / Packet filters are the next
	// milestone (need bandwidth flow-through from PCF + SDF filter
	// derivation per TS 29.513).
	ambr := packSessionAMBR(sess.AMBRDL, sess.AMBRUL)
	cmd := &nas.PDUSessionModificationCommand{
		PDUSessionID: k.PDUSessionID,
		PTI:          0,
		SessionAMBR:  &ambr,
	}
	// Merge Create + Delete specs into a single IE — §9.11.4.13 / §9.11.4.12
	// allow multiple per-entry op codes within one IE (the encoder writes
	// them back-to-back, the UE walks the byte stream).
	allRuleSpecs := append([]QoSRuleSpec{}, ruleSpecs...)
	allRuleSpecs = append(allRuleSpecs, removeRuleSpecs...)
	allFlowSpecs := append([]QoSFlowSpec{}, flowSpecs...)
	allFlowSpecs = append(allFlowSpecs, removeFlowSpecs...)
	if len(allRuleSpecs) > 0 {
		cmd.AuthorizedQoSRules = &nas.AuthorizedQoSRules{Value: BuildAuthorizedQoSRules(allRuleSpecs)}
		cmd.AuthorizedQoSFlowDescriptions = &nas.AuthorizedQoSFlowDescriptions{
			Value: BuildAuthorizedQoSFlowDescriptions(allFlowSpecs),
		}
		log.Infof("UpdateNotify: %d Create + %d Delete QoS rule(s) → %d flow description(s) emitted in §8.3.9",
			len(ruleSpecs), len(removeRuleSpecs), len(allFlowSpecs))
	}
	gsmNAS, err := cmd.Encode()
	if err != nil {
		log.Errorf("UpdateNotify: encode §8.3.9 MODIFICATION COMMAND: %v", err)
		return
	}

	// Step 3 — wrap in TS 24.501 §8.2.11 DL NAS TRANSPORT and hand
	// to the AMF for §8.6.2 shipment.
	wrapped, err := wrapInDLNASTransport(gsmNAS, k.PDUSessionID)
	if err != nil {
		log.Errorf("UpdateNotify: DLNAS wrap: %v", err)
		return
	}
	if DLNASByIMSI == nil {
		log.Warnf("UpdateNotify: DLNASByIMSI hook not wired — §8.3.9 not sent (%d B)", len(wrapped))
		return
	}
	if err := DLNASByIMSI(k.IMSI, wrapped); err != nil {
		log.Errorf("UpdateNotify: DLNAS send: %v", err)
		return
	}
	log.Infof("UpdateNotify: §8.3.9 PDU SESSION MODIFICATION COMMAND sent pduSessID=%d AMBR_DL=%dkbps AMBR_UL=%dkbps (%d B)",
		k.PDUSessionID, sess.AMBRDL, sess.AMBRUL, len(wrapped))
}

// pccRateLimits returns (GBR_UL, GBR_DL, MBR_UL, MBR_DL) for the PCC
// rule in `rules` matching `fiveQI`, or all-zeros when no rule is
// found. Used by the policy-driven §7.5.4 path to fill the §8.2.8
// MBR / §8.2.9 GBR IEs in the per-QFI QER. Zero on a dimension is
// interpreted by the UPF as "unmetered for this dimension" — same
// default-bearer QER built in installUPFRules.
func pccRateLimits(rules []pcf.PCCRule, fiveQI uint8) (gbrUL, gbrDL, mbrUL, mbrDL uint64) {
	for _, r := range rules {
		if r.FiveQI != int(fiveQI) {
			continue
		}
		return uint64(r.GBRULKbps), uint64(r.GBRDLKbps), uint64(r.MBRULKbps), uint64(r.MBRDLKbps)
	}
	return 0, 0, 0, 0
}

// HandleModificationComplete is called by the AMF when a UE returns a
// §8.3.10 PDU SESSION MODIFICATION COMPLETE (NAS type 204). Per
// TS 23.502 §4.3.3 step 8 the SMF then notifies the PCF that the
// resource allocation succeeded so the §4.2.3.5 SUCC_RES_ALLO
// PolicyControlRequestTrigger fires.
//
// Plumbed through nf/smf/session so the AMF doesn't import
// nf/pcf/smpolicy directly — same hook discipline as DLNASByIMSI.
func HandleModificationComplete(imsi string, pduSessionID uint8) {
	log := logger.Get("smf.policymod").WithIMSI(imsi)
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		return
	}
	if sess.SmPolicyCtxRef == "" {
		log.Debugf("ModificationComplete pduSessID=%d: no SmPolicyCtxRef cached, skipping PCF trigger", pduSessionID)
		return
	}
	// TS 29.512 §4.2.4 Update with §4.2.3.5 trigger
	// SUCC_RES_ALLO ("Successful resource allocation"). The PCF
	// FSM observes the report; we don't change local rule state
	// here.
	if _, err := smpolicy.Update(
		smfsm.Key{IMSI: imsi, PDUSessionID: pduSessionID},
		smpolicy.SmPolicyContextDataUpdate{Triggers: []string{"SUCC_RES_ALLO"}},
	); err != nil {
		log.Warnf("ModificationComplete pduSessID=%d: PCF Update(SUCC_RES_ALLO): %v", pduSessionID, err)
		return
	}
	log.Infof("ModificationComplete pduSessID=%d: PCF informed (SUCC_RES_ALLO trigger, TS 23.502 §4.3.3 step 8)", pduSessionID)
}

// wrapInDLNASTransport hand-encodes the 5GMM DL NAS TRANSPORT
// envelope (TS 24.501 §8.2.11) carrying a 5GSM payload — same byte
// layout the AMF's pdusetup helper produces for the establishment
// path, duplicated here so the SMF doesn't have to import the AMF
// pdusetup package (which would drag in the NGAP encoder graph).
//
// Layout (TS 24.501 Table 8.2.11.1.1):
//
//	EPD(0x7E) + SHT(0x00) + MsgType(0x68=DL NAS TRANSPORT)
//	+ PayloadContainerType(0x01=N1SM) + Spare(0x00)
//	+ PayloadContainer(LV-E: 2-byte length + 5GSM bytes)
//	+ PDUSessionID(IEI=0x12, 1 byte)
func wrapInDLNASTransport(gsmNAS []byte, pduSessionID uint8) ([]byte, error) {
	buf := make([]byte, 0, len(gsmNAS)+8)
	buf = append(buf, 0x7E)             // 5GMM EPD (§9.2)
	buf = append(buf, 0x00)             // Plain header
	buf = append(buf, 0x68)             // Message type DL NAS TRANSPORT (§9.7)
	buf = append(buf, 0x01)             // PayloadContainerType=N1SM (§9.11.3.40)
	buf = append(buf, byte(len(gsmNAS)>>8), byte(len(gsmNAS)&0xFF))
	buf = append(buf, gsmNAS...)
	buf = append(buf, 0x12, pduSessionID) // PDUSessionID IEI 0x12, TV 2
	return buf, nil
}

// HandleModifyResponseTNL is called by the AMF NGAP receiver when a
// TS 38.413 §8.2.3 PDU SESSION RESOURCE MODIFY RESPONSE arrives from
// the gNB carrying the §9.3.4.10 PDUSessionResourceModifyResponseTransfer.
// The transfer's DL NG-U UP TNL Information (gNB-allocated GTP-U TEID +
// transport-layer address — §9.3.1.16 / §9.3.2.2) is the endpoint the
// SMF must program into the UPF's per-QFI DL FAR so DL packets matching
// each newly-accepted QoS flow get tunneled to the right gNB.
//
// Spec map per QFI:
//
//   - TS 29.244 §7.5.4.17 Create FAR within Session Modification Request
//     (IE body inherits from §7.5.2.3)
//   - §8.2.26 Apply Action = FORW (forward)
//   - §8.2.24 Destination Interface = Access (toward N3)
//   - §8.2.56 Outer Header Creation = GTP-U/UDP/IPv4 with the gNB
//     TEID + IPv4 we just received
//
// FARID for the DL FAR is 200+QFI (UL FAR was 100+QFI, default DL FAR
// for QFI=1 was provisioned at session establishment with FARID=2).
// Stale per-QFI DL FARs from a previous modification cycle are OK —
// the UPF bridge dedupes by FARID.
//
// AdditionalDLQosFlowPerTNLInformation (§9.3.4.10) for multi-tunnel
// DL splitting is NOT yet wired — see TODO below.
func HandleModifyResponseTNL(imsi string, pduSessionID uint8, gnbTEID, gnbAddr uint32, qfis []uint8) {
	log := logger.Get("smf.policymod").WithIMSI(imsi)
	if len(qfis) == 0 {
		log.Debugf("ModifyResponseTNL pduSessID=%d: no accepted QFIs, skipping DL FAR install", pduSessionID)
		return
	}
	if gnbTEID == 0 || gnbAddr == 0 {
		log.Warnf("ModifyResponseTNL pduSessID=%d: missing DL TNL info (TEID=0x%08X addr=0x%08X) — DL FAR not installed for QFIs %v",
			pduSessionID, gnbTEID, gnbAddr, qfis)
		return
	}
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		log.Warnf("ModifyResponseTNL pduSessID=%d: no session, skipping", pduSessionID)
		return
	}
	_ = sess // touched for the QFI=1 (default flow) skip below
	var batch upfmgr.ModifyBatch
	for _, qfi := range qfis {
		if qfi == 1 {
			// Default flow's DL FAR (FARID=2) was set up at
			// session establishment per TS 23.501 §5.7.1.5
			// (QFI=1 is the default QoS flow); ActivateUserPlane
			// re-points it on each modify. Skip duplicate install.
			continue
		}
		dlFAR := upfmgr.FAR{
			FARID:    200 + uint32(qfi),
			Action:   1,       // §8.2.26 FORWARD bit
			DstIface: 0,       // §8.2.24 Access (DL toward gNB)
			TEID:     gnbTEID, // §8.2.56 Outer Header Creation: GTP-U TEID
			PeerAddr: gnbAddr, // §8.2.56 Outer Header Creation: IPv4
			PeerPort: 2152,    // GTP-U UDP port (TS 29.281 §4.3)
		}
		batch.CreateFARs = append(batch.CreateFARs, dlFAR)
		log.Infof("PFCP §7.5.4.17 Create FAR (DL): QFI=%d FARID=%d gnbTEID=0x%08X gnbAddr=0x%08X dst=Access (§8.2.24)",
			qfi, dlFAR.FARID, gnbTEID, gnbAddr)
	}
	if !batch.IsEmpty() {
		if err := upfmgr.Default.ApplyModifyBatch(imsi, pduSessionID, batch); err != nil {
			log.Warnf("PFCP §7.5.4 batched DL-FAR install failed: %v", err)
		}
	}
	// TODO TS 38.413 §9.3.4.10 AdditionalDLQosFlowPerTNLInformation —
	// multi-tunnel DL splitting (one (TEID,addr) per QFI rather than
	// the single session-level DL TNL). When the gNB exposes per-flow
	// tunnels, plumb the per-QFI (TEID,addr) pairs through here and
	// drop the single-tunnel assumption above.
}

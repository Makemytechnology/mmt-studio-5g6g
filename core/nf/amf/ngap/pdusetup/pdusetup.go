// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pdusetup — NGAP PDU Session Resource Setup procedure.
//
// Authoritative specs (verified against in-tree PDFs this turn):
//
//	TS 38.413 §8.2.1 "PDU Session Resource Setup" (PDF:
//	specs/3gpp/ts_138413v190200p.pdf):
//
//	  §8.2.1.1 General (verbatim): "The purpose of the PDU Session
//	    Resource Setup procedure is to assign resources on Uu and
//	    NG-U for one or several PDU sessions and the corresponding
//	    QoS flows, and to setup corresponding DRBs for a given UE.
//	    The procedure uses UE-associated signalling."
//
//	  §8.2.1.2 Successful Operation: AMF → NG-RAN sends PDU SESSION
//	    RESOURCE SETUP REQUEST; NG-RAN replies with PDU SESSION
//	    RESOURCE SETUP RESPONSE carrying the PDU Session Resource
//	    Setup List SU Res + optional PDU Session Resource Failed
//	    to Setup List SU Res.
//
//	  §8.2.1.3 Unsuccessful Operation: NG-RAN replies with PDU
//	    SESSION RESOURCE SETUP FAILURE when the whole request
//	    cannot be honoured.
//
//	TS 38.413 §9.2.1.1 Message definition (ICS-IEs mandatory
//	  PDU Session Resource Setup List SU Req) + §9.3.4.1
//	  "PDU Session Resource Setup Request Transfer" — the N2-SM
//	  container built by the SMF and relayed opaquely by the AMF
//	  to the NG-RAN node. Carries PDU Session AMBR, UP Transport
//	  Layer Info (GTP tunnel endpoint on UPF N3), PDU Session Type
//	  (§9.3.1.69), QoS Flow Setup Request List (QFI + 5QI / ARP).
//
//	TS 23.502 §4.3.2.2.1 steps 10-12 — where this NGAP procedure
//	  sits in the stage-2 PDU session establishment call flow.
//
// The AMF piggybacks the 5GSM PDU SESSION ESTABLISHMENT ACCEPT NAS
// PDU (via §9.3.1.44 NAS-PDU IE of each PDU Session Resource Setup
// Item SU Req) so the UE receives it in the same NGAP exchange the
// gNB uses to bring up the radio bearer (§8.2.1.2: "For each PDU
// session successfully established the NG-RAN node shall pass to
// the UE the PDU Session NAS-PDU IE, if included.").
//
// Go port of nf/amf/ngap/ngap_pdu_session_resource_setup.py.
package pdusetup

import (
	"encoding/binary"
	"fmt"
	"net"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/errind"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	sessionfsm "github.com/mmt/mmt-studio-core/nf/smf/session/fsm"
	upfmgr "github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	nas "github.com/mmt/nasgen/generated"
)

// WrapDL is set by the gmm package at init() to the real NAS security
// wrapper (secureWrap). Pre-SMC it returns the input unchanged.
// Hook pattern avoids the gmm ↔ pdusetup import cycle.
var WrapDL func(ue *uectx.AmfUeCtx, plain []byte) ([]byte, error)

// Send issues PDUSessionResourceSetupRequest for one session. Caller
// provides the UE context, the SMF session (which carries DNN / SST /
// UPF anchor / NAS Accept bytes / AMBR) and the gNB to target. Returns
// the on-wire PDU length for metrics.
//
// acceptNAS is the 5GSM PDU SESSION ESTABLISHMENT ACCEPT bytes to
// piggyback for new-session establishment (§4.3.2.2.1 step 10). Pass
// nil / empty for user-plane *reactivation* of a previously-established
// SUSPENDED session — per TS 23.502 §4.2.3.2 step 10-12 and
// §4.2.2.2.2 step 17, reactivation does NOT carry a fresh 5GSM Accept
// (the session already exists on the UE side); the gNB's response
// brings the new DL TEID so the UPF FAR can flip BUFF→FORW.
//
// N3 tunnel endpoint (UPF-side IP + TEID) comes from session.UPFN3IP /
// session.UPFTEID, populated by the SMF during Establish + installUPFRules.
func Send(gnb *gnbctx.GnbCtx, ue *uectx.AmfUeCtx, sess *session.Session, acceptNAS []byte) (int, error) {
	log := logger.Get("amf.ngap.pdusetup")
	pm.Inc(pm.SMFlowAtt, 1)

	if gnb == nil || ue == nil || sess == nil {
		return 0, fmt.Errorf("pdusetup: nil gnb/ue/session")
	}

	// Procedure-collision guard (TS 38.413 §8.1 / §8.3.3.1). If the
	// UE is already being released, starting a new PDU Session
	// Resource Setup would target a connection that's going away.
	if ok, reason := uectx.CanStartNGAPProcedure(ue.NGAPProc, uectx.NGAPProcPDUSessionResourceSetup); !ok {
		log.WithIMSI(ue.IMSI).Warnf("PDUSessionResourceSetup skipped amfUeID=%d pduSessID=%d: %s",
			ue.AmfUeNGAPID, sess.PDUSessionID, reason)
		pm.Inc(pm.SMFlowFail, 1)
		return 0, fmt.Errorf("pdusetup: blocked by NGAPProc=%s: %s", ue.NGAPProc, reason)
	}
	reactivate := len(acceptNAS) == 0

	amfID := genngap.AMFUENGAPID(ue.AmfUeNGAPID)
	ranID := genngap.RANUENGAPID(ue.RanUeNGAPID)

	// Wrap 5GSM Accept in DL NAS Transport (TS 24.501 §8.2.11), then
	// security-wrap with SHT=2 (integrity + ciphered) when the UE has an
	// active NAS context. TS 24.501 §4.4.4.1 — post-SMC DL NAS must be
	// protected; the hook no-ops pre-SMC.
	//
	// Skipped entirely on reactivation — the NAS-PDU IE is absent from
	// the PDUSessionResourceSetupItemSUReq.
	var dlNas []byte
	plainSize := 0
	var nasIE *genngap.NASPDU
	if !reactivate {
		var err error
		dlNas, err = wrapInDLNASTransport(acceptNAS, sess.PDUSessionID, sess.SST, sess.SD)
		if err != nil {
			return 0, fmt.Errorf("pdusetup: wrap DL NAS Transport: %w", err)
		}
		plainSize = len(dlNas)
		if WrapDL != nil {
			wrapped, werr := WrapDL(ue, dlNas)
			if werr != nil {
				return 0, fmt.Errorf("pdusetup: DL NAS secure wrap: %w", werr)
			}
			dlNas = wrapped
		}
		n := genngap.NASPDU(dlNas)
		nasIE = &n
	}

	// Build PDUSessionResourceSetupRequestTransfer bytes with minimum IEs
	// that the gNB needs: PDU Session AMBR + UP Transport Info + PDU type.
	transferBytes, err := buildTransfer(sess)
	if err != nil {
		pm.Inc(pm.SMFlowFail, 1)
		return 0, fmt.Errorf("pdusetup: build transfer: %w", err)
	}

	// PDUSessionResourceSetupListSUReq — exactly one item per current flow.
	// TS 38.413 §9.3.1.24 S-NSSAI: SST = 1 octet, SD = 3 octets network
	// byte-order. Python encodes SD as int → 3-byte BE. The previous Go
	// path treated sess.SD as a string and sent its ASCII bytes
	// (6 bytes "000001") instead of the 3-byte binary — wireshark simply
	// dropped the malformed SD. Parse the hex string to 3 octets.
	snssai := genngap.SNSSAI{
		SST: genngap.SST{sess.SST},
	}
	if sess.SD != "" {
		sdBytes := snssaiSDHexToBytes(sess.SD)
		if sdBytes != nil {
			sd := genngap.SD(sdBytes)
			snssai.SD = &sd
		}
	}
	item := genngap.PDUSessionResourceSetupItemSUReq{
		PDUSessionID:                           genngap.PDUSessionID(sess.PDUSessionID),
		PDUSessionNASPDU:                       nasIE, // nil on reactivation
		SNSSAI:                                 snssai,
		PDUSessionResourceSetupRequestTransfer: transferBytes,
	}
	list := genngap.PDUSessionResourceSetupListSUReq{item}

	msg := &genngap.PDUSessionResourceSetupRequest{}
	add := func(id int64, crit genngap.Criticality, v genngap.PDUSessionResourceSetupRequestIEsValue) {
		msg.ProtocolIEs = append(msg.ProtocolIEs, genngap.PDUSessionResourceSetupRequestIEsEntry{
			Id:          genngap.ProtocolIEID(id),
			Criticality: crit,
			Value:       v,
		})
	}
	// IE emission order. APER-encoded SEQUENCE OF ProtocolIE-
	// Container has no prescribed wire order — any ordering decodes
	// on a compliant peer. We emit AMF-UE-NGAP-ID then RAN-UE-NGAP-ID
	// first (items 0 and 1) purely for capture-parity with reference
	// stacks, so operators diffing wireshark dumps see the same
	// layout. Remaining IEs follow in the sequence the ASN.1 IES-SET
	// in TS 38.413 §9.3 declares them.
	add(int64(genngap.IdAMFUENGAPID), genngap.CriticalityReject, genngap.PDUSessionResourceSetupRequestIEsValue{
		Present:     genngap.PDUSessionResourceSetupRequestIEsValuePresentAMFUENGAPID,
		AMFUENGAPID: &amfID,
	})
	add(int64(genngap.IdRANUENGAPID), genngap.CriticalityReject, genngap.PDUSessionResourceSetupRequestIEsValue{
		Present:     genngap.PDUSessionResourceSetupRequestIEsValuePresentRANUENGAPID,
		RANUENGAPID: &ranID,
	})
	add(int64(genngap.IdPDUSessionResourceSetupListSUReq), genngap.CriticalityReject,
		genngap.PDUSessionResourceSetupRequestIEsValue{
			Present:                          genngap.PDUSessionResourceSetupRequestIEsValuePresentPDUSessionResourceSetupListSUReq,
			PDUSessionResourceSetupListSUReq: &list,
		})

	// ── IE 110: UEAggregateMaximumBitRate ──
	// TS 38.413 §8.2.1: Conditional-Mandatory whenever at least one PDU
	// session in the request carries a non-GBR QoS flow (5QI 5-9 etc.).
	// TS 23.501 §5.7.3 UE-AMBR comes from the ue_subscription table —
	// sess.UEAMBRUL/DL is populated at Establish time from udm's
	// subscription cache. Emit only when > 0 to match Python's
	// `require(session_info, 'ue_ambr_*')` spec check; this keeps the
	// gNB from seeing a bogus 0-rate UE-AMBR IE.
	if sess.UEAMBRUL > 0 || sess.UEAMBRDL > 0 {
		ueAMBR := &genngap.UEAggregateMaximumBitRate{
			UEAggregateMaximumBitRateDL: genngap.BitRate(uint64(sess.UEAMBRDL) * 1000), // kbps → bps
			UEAggregateMaximumBitRateUL: genngap.BitRate(uint64(sess.UEAMBRUL) * 1000),
		}
		add(int64(genngap.IdUEAggregateMaximumBitRate), genngap.CriticalityIgnore,
			genngap.PDUSessionResourceSetupRequestIEsValue{
				Present:                   genngap.PDUSessionResourceSetupRequestIEsValuePresentUEAggregateMaximumBitRate,
				UEAggregateMaximumBitRate: ueAMBR,
			})
	}

	inner, err := msg.MarshalAPER()
	if err != nil {
		pm.Inc(pm.SMFlowFail, 1)
		return 0, fmt.Errorf("pdusetup: marshal outer: %w", err)
	}
	pdu, err := wire.Encode(&wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ngap.ProcCodePDUSessionResourceSetup,
		Criticality:   wire.CriticalityReject,
		Value:         inner,
	})
	if err != nil {
		pm.Inc(pm.SMFlowFail, 1)
		return 0, fmt.Errorf("pdusetup: envelope: %w", err)
	}
	stream := gnb.UEStream(ue.AmfUeNGAPID)
	if err := gnb.Send(pdu, stream); err != nil {
		pm.Inc(pm.SMFlowFail, 1)
		return 0, fmt.Errorf("pdusetup: gnb send: %w", err)
	}
	ue.NGAPProc = uectx.NGAPProcPDUSessionResourceSetup

	// NGAP per-UE FSM: ref-count the pending sessions for observability,
	// then fire EvPDUResourceSetupRequestSent for every fork. The FSM
	// graph has a self-loop on this event in RESOURCE_SETUP_PENDING so
	// parallel forks don't escape the state — only the final Response
	// (when ref count reaches zero) transitions back to ESTABLISHED.
	fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	f := ngapfsm.Of(fk)
	f.TrackResourceSetupRequest()
	_ = f.Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvPDUResourceSetupRequestSent,
		PDUSessionID: sess.PDUSessionID})

	mode := "establish"
	if reactivate {
		mode = "reactivate"
	}
	log.WithIMSI(ue.IMSI).Infof("PDUSessionResourceSetupRequest sent amfUeID=%d pduSessionID=%d gNB=%s UPF-N3=%s UL-TEID=0x%08X PDU=%dB NAS=%dB (5GSM=%dB, DLNAS+wrap=%dB) mode=%s",
		ue.AmfUeNGAPID, sess.PDUSessionID, gnb.GnbIP, sess.UPFN3IP, sess.UPFTEID,
		len(pdu), len(dlNas), len(acceptNAS), plainSize, mode)
	return len(pdu), nil
}

// handleResponse processes PDU SESSION RESOURCE SETUP RESPONSE per
// TS 38.413 §8.2.1.2 Successful Operation (SuccessfulOutcome of
// procedureCode=29). The response may carry two lists:
//
//   - PDU Session Resource Setup List SU Res — sessions the gNB
//     successfully set up. We walk these, move the 5GSM FSM to
//     Active, update the UPF DL FAR with the gNB tunnel endpoint
//     extracted from the per-session Response Transfer.
//
//   - PDU Session Resource Failed to Setup List SU Res — sessions
//     the gNB could not set up. Per §8.2.1.2: "The NG-RAN node shall
//     not send to the UE the PDU Session NAS PDUs associated to the
//     failed PDU sessions" — so the UE never receives the piggy-
//     backed 5GSM Accept and its Establishment Request stays pending
//     at the NAS layer. Each failed item carries a
//     PDUSessionResourceSetupUnsuccessfulTransfer (§9.3.4.16) with
//     a mandatory Cause IE (§9.3.1.2) and optional Criticality
//     Diagnostics (§9.3.1.3).
//
//     Our rollback for each failed item:
//       1. APER-decode the Unsuccessful Transfer → extract Cause.
//       2. Map NGAP Cause → 5GSM cause (TS 24.501 §9.11.4.2); log the
//          NGAP-side reason verbatim so operators can correlate.
//       3. Send PDU SESSION ESTABLISHMENT REJECT to the UE (TS 24.501
//          §6.4.1.4 "UE-requested PDU session establishment procedure
//          not accepted by the network", §8.3.5 message definition)
//          wrapped in a 5GMM DL NAS Transport. Without this the UE
//          sits in PROCEDURE_TRANSACTION_PENDING waiting for Accept
//          or Reject until T3580 expires.
//       4. Release SMF/UPF side (IP pool return, PFCP delete, PCF
//          policy delete, session record drop) via session.Release.
//       5. Drive the 5GSM FSM via EvResourceSetupFailure
//          (ActivationPending → Inactive) so subsequent
//          (IMSI, PDUSessionID) reuse starts clean.
//
// §8.2.1.3 "Unsuccessful Operation" explicitly says "The unsuccessful
// operation is specified in the successful operation section" — i.e.
// there is NO separate PDU SESSION RESOURCE SETUP FAILURE message;
// whole-procedure failures are reported via a fully-populated Failed
// list. No UnsuccessfulOutcome dispatch needed.
func handleResponse(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.pdusetup")
	var resp genngap.PDUSessionResourceSetupResponse
	if err := resp.UnmarshalAPER(env.Value); err != nil {
		// TS 38.413 v19.2.0 §8.7.5.1 — Error Indication on
		// undecodable inbound message.
		log.Errorf("PDUSessionResourceSetupResponse decode from %s: %v", gnb.GnbIP, err)
		_ = errind.Send(gnb, 0, 0,
			errind.CauseProtocol(genngap.CauseProtocolTransferSyntaxError))
		return
	}
	amfUeID, ranUeID := extractUEIDs(resp.ProtocolIEs)
	ue := locateUE(gnb, amfUeID, ranUeID)
	if ue == nil {
		// §8.7.5.2 — Unknown local UE NGAP ID.
		log.Warnf("PDUSessionResourceSetupResponse for unknown UE amfUeID=%d", amfUeID)
		_ = errind.Send(gnb, amfUeID, ranUeID,
			errind.CauseRadio(genngap.CauseRadioNetworkUnknownLocalUENGAPID))
		return
	}
	ue.NGAPProc = uectx.NGAPProcNone

	// §8.2.1.2 Success list walk — move each session to ACTIVE +
	// update UPF DL FAR with gNB tunnel.
	successCount := 0
	for _, ie := range resp.ProtocolIEs {
		if int64(ie.Id) != int64(genngap.IdPDUSessionResourceSetupListSURes) {
			continue
		}
		if ie.Value.PDUSessionResourceSetupListSURes == nil {
			continue
		}
		for _, item := range *ie.Value.PDUSessionResourceSetupListSURes {
			pduSessID := uint8(item.PDUSessionID)
			sess := session.Default.Get(ue.IMSI, pduSessID)
			if sess == nil {
				continue
			}
			sess.State = session.StateActive
			session.Default.Put(sess)
			// Drive the 5GSM FSM: gNB confirmed the resource setup →
			// session is fully ACTIVE (ACTIVATION_PENDING → ACTIVE).
			sessKey := sessionfsm.Key{IMSI: ue.IMSI, PDUSessionID: pduSessID}
			_ = sessionfsm.Of(sessKey).Fire(&sessionfsm.Context{
				Key: sessKey, Event: sessionfsm.EvResourceSetupResponse,
			})

			// NGAP per-UE FSM: untrack this session's fork. Fire the
			// Response event (RESOURCE_SETUP_PENDING → ESTABLISHED)
			// only when the last pending session returns.
			fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
			fnsm := ngapfsm.Of(fk)
			if fnsm.UntrackResourceSetupResponse() == 0 {
				_ = fnsm.Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvPDUResourceSetupResponse,
					PDUSessionID: pduSessID})
			}

			successCount++
			pm.Inc(pm.SMFlowSucc, 1)

			// Decode PDUSessionResourceSetupResponseTransfer to extract gNB tunnel
			gnbTEID, gnbAddr, err := extractGNBTunnel(item.PDUSessionResourceSetupResponseTransfer)
			if err != nil {
				log.Warnf("PDU Session %d: gNB tunnel extract: %v", pduSessID, err)
				continue
			}
			log.WithIMSI(ue.IMSI).Infof("PDU Session %d: gNB tunnel TEID=0x%08X addr=%s", pduSessID, gnbTEID, gnbAddr)

			gnbIPBytes := net.ParseIP(gnbAddr).To4()
			if gnbIPBytes == nil {
				log.Warnf("PDU Session %d: invalid gNB addr %s", pduSessID, gnbAddr)
				continue
			}
			gnbAddrU32 := binary.BigEndian.Uint32(gnbIPBytes)

			// Two cases share this code path per TS 23.502:
			//
			//	§4.3.2.2.1 step 10 — brand-new session establishment.
			//	  The DL FAR is in Action=FORW with a placeholder TEID
			//	  from installUPFRules; this Update just installs the
			//	  real gNB TEID.
			//
			//	§4.2.3.2 step 12 — Service Request reactivation of a
			//	  previously-suspended session (see §4.2.6 step 6a
			//	  deactivation counterpart in nf/smf/session/establish.go
			//	  DeactivateUserPlane). The DL FAR is in Action=BUFF
			//	  with zeroed TEID; the dataplane upf_dp_update_far
			//	  (TS 29.244 §5.2.1) flips BUFF→FORW and flushes any
			//	  buffered DL packets through the newly-valid tunnel.
			//
			// session.ActivateUserPlane covers both by calling the
			// UPF ActivateDL + refreshing PFCP/session state. It's
			// idempotent on already-FORW sessions so the establish
			// path is unchanged.
			if n := session.ActivateUserPlane(ue.IMSI, pduSessID, gnbTEID, gnbAddrU32); n == 0 {
				log.Warnf("PDU Session %d: ActivateUserPlane affected 0 FARs", pduSessID)
			}

			// Register UPF's own TEID for this session in the fast-path lookup
			upfmgr.Default.RegisterTEID(sess.UPFTEID, ue.IMSI, pduSessID)
		}
	}

	// §8.2.1.2 Failed list walk — per-item rollback.
	failCount := 0
	for _, ie := range resp.ProtocolIEs {
		if int64(ie.Id) != int64(genngap.IdPDUSessionResourceFailedToSetupListSURes) {
			continue
		}
		if ie.Value.PDUSessionResourceFailedToSetupListSURes == nil {
			continue
		}
		for _, item := range *ie.Value.PDUSessionResourceFailedToSetupListSURes {
			pduSessID := uint8(item.PDUSessionID)

			// §9.3.4.16 PDU Session Resource Setup Unsuccessful
			// Transfer → Cause IE (§9.3.1.2). The gNB states why the
			// setup failed — radioNetwork / transport / nas /
			// protocol / misc choice, then a sub-enumeration. We log
			// the verbatim enum name so operators can correlate with
			// the gNB logs.
			ngapCauseStr := "(no transfer)"
			gsmCause := session.CauseRequestRejectedUnspecified
			if len(item.PDUSessionResourceSetupUnsuccessfulTransfer) > 0 {
				var transfer genngap.PDUSessionResourceSetupUnsuccessfulTransfer
				if derr := transfer.UnmarshalAPER(item.PDUSessionResourceSetupUnsuccessfulTransfer); derr != nil {
					ngapCauseStr = fmt.Sprintf("(transfer decode: %v)", derr)
				} else {
					ngapCauseStr = causeString(transfer.Cause)
					gsmCause = mapNGAPCauseToGSM(transfer.Cause)
				}
			}
			log.WithIMSI(ue.IMSI).Warnf("PDU Session %d setup FAILED at gNB — NGAP cause: %s; rolling back with 5GSM cause #%d (TS 24.501 §9.11.4.2)",
				pduSessID, ngapCauseStr, gsmCause)

			// TS 24.501 §6.4.1.4 — send PDU SESSION ESTABLISHMENT
			// REJECT so the UE exits PROCEDURE_TRANSACTION_PENDING.
			// Wrap the 5GSM Reject in 5GMM DL NAS Transport (§8.2.11)
			// via the same helper used for the Accept path, then push
			// through dlnas.Send (applies WrapDL security hook).
			pti := uint8(0)
			if sess := session.Default.Get(ue.IMSI, pduSessID); sess != nil {
				pti = sess.PTI
			}
			if rejectNAS := session.BuildEstablishReject(pduSessID, gsmCause, pti); rejectNAS != nil {
				dlWrap, werr := wrapInDLNASTransport(rejectNAS, pduSessID, 0, "")
				if werr != nil {
					log.WithIMSI(ue.IMSI).Warnf("PDU Session %d: wrap reject in DL NAS Transport: %v",
						pduSessID, werr)
				} else if serr := dlnas.Send(gnb, ue, dlWrap); serr != nil {
					log.WithIMSI(ue.IMSI).Warnf("PDU Session %d: DL NAS Transport (reject) send: %v",
						pduSessID, serr)
				} else {
					log.WithIMSI(ue.IMSI).Infof("PDU Session %d: sent 5GSM PDU SESSION ESTABLISHMENT REJECT (cause #%d, PTI=%d)",
						pduSessID, gsmCause, pti)
				}
			}

			// 5GSM FSM: ActivationPending → Inactive.
			sessKey := sessionfsm.Key{IMSI: ue.IMSI, PDUSessionID: pduSessID}
			_ = sessionfsm.Of(sessKey).Fire(&sessionfsm.Context{
				Key: sessKey, Event: sessionfsm.EvResourceSetupFailure,
			})

			// Untrack the NGAP per-UE fork — same ref-count bookkeeping
			// as the success path so parallel forks settle correctly.
			fk := ngapfsm.Key{GnbKey: gnb.GnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
			fnsm := ngapfsm.Of(fk)
			if fnsm.UntrackResourceSetupResponse() == 0 {
				_ = fnsm.Fire(&ngapfsm.Context{Key: fk, Event: ngapfsm.EvPDUResourceSetupResponse,
					PDUSessionID: pduSessID})
			}

			// SMF/UPF teardown — IP return, PFCP delete, PCF policy
			// delete, session record drop. We ignore Release's
			// returned Release Command NAS bytes: the UE never got
			// into the Active state that a Release Command
			// (§6.4.4.2) targets; the Reject we already sent is the
			// right NAS response for a failed establishment.
			_ = session.Release(ue.IMSI, pduSessID)

			failCount++
			pm.Inc(pm.SMFlowFail, 1)
		}
	}

	log.WithIMSI(ue.IMSI).Infof("PDUSessionResourceSetupResponse amfUeID=%d successes=%d failures=%d",
		ue.AmfUeNGAPID, successCount, failCount)
}

// extractGNBTunnel APER-decodes a PDUSessionResourceSetupResponseTransfer
// and returns the gNB's GTP-U TEID and transport-layer address.
func extractGNBTunnel(transferBytes []byte) (teid uint32, addr string, err error) {
	var transfer genngap.PDUSessionResourceSetupResponseTransfer
	if err = transfer.UnmarshalAPER(transferBytes); err != nil {
		return 0, "", fmt.Errorf("APER decode: %w", err)
	}
	gtp := transfer.DLQosFlowPerTNLInformation.UPTransportLayerInformation.GTPTunnel
	if gtp == nil {
		return 0, "", fmt.Errorf("no GTPTunnel in response transfer")
	}
	// TEID: 4-byte octet string → uint32
	if len(gtp.GTPTEID) >= 4 {
		teid = binary.BigEndian.Uint32(gtp.GTPTEID[:4])
	}
	// TransportLayerAddress: BIT STRING containing the IPv4 address bytes
	tla := gtp.TransportLayerAddress
	if len(tla.Bytes) >= 4 {
		addr = net.IP(tla.Bytes[:4]).String()
	} else {
		return 0, "", fmt.Errorf("TransportLayerAddress too short (%d bytes)", len(tla.Bytes))
	}
	return teid, addr, nil
}

// buildTransfer encodes PDUSessionResourceSetupRequestTransfer (TS 38.413
// §9.3.4.1). Line-for-line port of Python's
// _encode_pdu_session_resource_setup_request_transfer
// (nf/amf/ngap/ngap_pdu_session_resource_setup.py:142).
//
// IE emission order — tuned for capture-parity with the operator
// reference trace: AMBR stays first, UL-NGU moved up to Item 1.
// APER-encoded SEQUENCE OF ProtocolIE-Field has no spec-mandated
// order (TS 38.413 §9.1 / ASN.1 SEQUENCE OF), so any permutation
// decodes on a compliant peer; the order below is pure
// capture-parity:
//
//	130 PDUSessionAggregateMaximumBitRate (reject)
//	139 UL-NGU-UP-TNLInformation          (reject)
//	134 PDUSessionType                    (reject)
//	136 QosFlowSetupRequestList           (reject)
func buildTransfer(sess *session.Session) ([]byte, error) {
	transfer := &genngap.PDUSessionResourceSetupRequestTransfer{}
	add := func(id int64, crit genngap.Criticality, v genngap.PDUSessionResourceSetupRequestTransferIEsValue) {
		transfer.ProtocolIEs = append(transfer.ProtocolIEs, genngap.PDUSessionResourceSetupRequestTransferIEsEntry{
			Id:          genngap.ProtocolIEID(id),
			Criticality: crit,
			Value:       v,
		})
	}

	// ── IE 130: PDUSessionAggregateMaximumBitRate ──
	// Python publishes kbps × 1000 (→ bps).
	ambr := &genngap.PDUSessionAggregateMaximumBitRate{
		PDUSessionAggregateMaximumBitRateDL: genngap.BitRate(uint64(sess.AMBRDL) * 1000),
		PDUSessionAggregateMaximumBitRateUL: genngap.BitRate(uint64(sess.AMBRUL) * 1000),
	}
	add(int64(genngap.IdPDUSessionAggregateMaximumBitRate), genngap.CriticalityReject,
		genngap.PDUSessionResourceSetupRequestTransferIEsValue{
			Present:                           genngap.PDUSessionResourceSetupRequestTransferIEsValuePresentPDUSessionAggregateMaximumBitRate,
			PDUSessionAggregateMaximumBitRate: ambr,
		})

	// ── IE 139: UL-NGU-UP-TNLInformation ──
	// GTP-U tunnel endpoint on the UPF side (collocated). Address is
	// cfg.amf_ip in Python; Go uses sess.UPFN3IP from upf.Select.
	tla := parseIPv4(sess.UPFN3IP)
	teid := make([]byte, 4)
	binary.BigEndian.PutUint32(teid, sess.UPFTEID)
	upTL := &genngap.UPTransportLayerInformation{
		Present: genngap.UPTransportLayerInformationPresentGTPTunnel,
		GTPTunnel: &genngap.GTPTunnel{
			TransportLayerAddress: genngap.TransportLayerAddress{Bytes: tla, BitLength: 32},
			GTPTEID:               genngap.GTPTEID(teid),
		},
	}
	add(int64(genngap.IdULNGUUPTNLInformation), genngap.CriticalityReject,
		genngap.PDUSessionResourceSetupRequestTransferIEsValue{
			Present:                     genngap.PDUSessionResourceSetupRequestTransferIEsValuePresentUPTransportLayerInformation,
			UPTransportLayerInformation: upTL,
		})

	// ── IE 134: PDUSessionType ──
	// NAS and NGAP use different integer codes for the same PDU-session
	// type. Both sets are generated constants — never use raw ints.
	// (NAS values: nas/ie_pdusessiontype.go · NGAP values: asn1-go/protocols/ngap/generated/ngap_ies.go)
	var pduType genngap.PDUSessionType
	switch sess.PDUType {
	case nas.PDUSessionTypeIpv4:
		pduType = genngap.PDUSessionTypeIpv4
	case nas.PDUSessionTypeIpv6:
		pduType = genngap.PDUSessionTypeIpv6
	case nas.PDUSessionTypeIpv4v6:
		pduType = genngap.PDUSessionTypeIpv4v6
	case nas.PDUSessionTypeUnstructured:
		pduType = genngap.PDUSessionTypeUnstructured
	case nas.PDUSessionTypeEthernet:
		pduType = genngap.PDUSessionTypeEthernet
	default:
		pduType = genngap.PDUSessionTypeIpv4
	}
	add(int64(genngap.IdPDUSessionType), genngap.CriticalityReject,
		genngap.PDUSessionResourceSetupRequestTransferIEsValue{
			Present:        genngap.PDUSessionResourceSetupRequestTransferIEsValuePresentPDUSessionType,
			PDUSessionType: &pduType,
		})

	// ── IE 136: QosFlowSetupRequestList ──
	// Default bearer only — matches Python when qos_flows has one entry.
	// Python pulls qfi/fiveqi/arp_* per flow from session_info; the Go
	// side currently builds a single default flow until per-flow table
	// lookup lands here.
	fiveQI := genngap.FiveQI(int64(sess.FiveQI))
	if fiveQI == 0 {
		fiveQI = 9 // TS 23.501 Table 5.7.4-1 default non-GBR
	}
	qos := genngap.QosFlowSetupRequestList{
		{
			QosFlowIdentifier: genngap.QosFlowIdentifier(1),
			QosFlowLevelQosParameters: genngap.QosFlowLevelQosParameters{
				QosCharacteristics: genngap.QosCharacteristics{
					Present: genngap.QosCharacteristicsPresentNonDynamic5QI,
					NonDynamic5QI: &genngap.NonDynamic5QIDescriptor{
						FiveQI: fiveQI,
					},
				},
				AllocationAndRetentionPriority: genngap.AllocationAndRetentionPriority{
					// Matches Python fallback when no service binding exists
					// (smf_pdu_session.py:367-371): arp_priority=8,
					// arp_pcap=0 (shall-not-trigger), arp_pvuln=1 (pre-emptable).
					PriorityLevelARP:        genngap.PriorityLevelARP(8),
					PreEmptionCapability:    genngap.PreEmptionCapabilityShallNotTriggerPreEmption,
					PreEmptionVulnerability: genngap.PreEmptionVulnerabilityPreEmptable,
				},
			},
		},
	}
	add(int64(genngap.IdQosFlowSetupRequestList), genngap.CriticalityReject,
		genngap.PDUSessionResourceSetupRequestTransferIEsValue{
			Present:                 genngap.PDUSessionResourceSetupRequestTransferIEsValuePresentQosFlowSetupRequestList,
			QosFlowSetupRequestList: &qos,
		})

	return transfer.MarshalAPER()
}

func extractUEIDs(ies []genngap.PDUSessionResourceSetupResponseIEsEntry) (amf, ran int64) {
	for i := range ies {
		ie := &ies[i]
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				amf = int64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				ran = int64(*ie.Value.RANUENGAPID)
			}
		}
	}
	return
}

func locateUE(gnb *gnbctx.GnbCtx, amfUeID, ranUeID int64) *uectx.AmfUeCtx {
	if amfUeID != 0 {
		if ue := uectx.Default.LookupByAmfID(amfUeID); ue != nil {
			return ue
		}
	}
	if ranUeID != 0 {
		return uectx.Default.LookupByRanKey(gnb.GnbIP, ranUeID)
	}
	return nil
}

// wrapInDLNASTransport wraps a 5GSM NAS PDU in a DL NAS Transport message
// (TS 24.501 §8.2.11) so the UE receives it as a proper 5GMM container.
//
// TS 24.501 §8.2.11.1 Table: DL NAS TRANSPORT carries only these outer IEs
// past the payload container:
//
//	0x12 PDU session ID (O, TV 2)
//	0x24 Additional information (O, TLV)
//	0x58 5GMM cause (O, TV 2)
//	0x37 Back-off timer value (O, TLV 3)
//	0x39 LADN information (O, TLV-E)
//	(A-) MA PDU session information (O, TV 1)
//	(F-) Release assistance indication (O, TV 1)
//
// IEI 0x22 (S-NSSAI) is NOT a DL NAS TRANSPORT IE — it belongs to the
// inner PDU SESSION ESTABLISHMENT ACCEPT (§8.3.2.16), which the 5GSM
// layer already encodes before handing us gsmNAS. A previous version
// appended 0x22 here by mistake; wireshark flagged it as "Extraneous
// Data, dissector bug or later version spec". Removed.
//
// Layout:
//
//	EPD(0x7E) + SHT(0x00) + MsgType(0x68=DLNASTransport)
//	+ PayloadContainerType(0x01=N1SM) + Spare(0x00)
//	+ PayloadContainer(LV-E: 2-byte length + gsm bytes)
//	+ PDUSessionID(IEI=0x12, 1 byte)
func wrapInDLNASTransport(gsmNAS []byte, pduSessionID uint8, sst uint8, sd string) ([]byte, error) {
	// sst/sd parameters retained for call-site compatibility; the S-NSSAI
	// is carried in the NGAP s-NSSAI field and the inner PDU Session
	// Establishment Accept, not here.
	_ = sst
	_ = sd
	var buf []byte
	buf = append(buf, 0x7E) // EPD: 5GMM
	buf = append(buf, 0x00) // Security header: plain
	buf = append(buf, 0x68) // Message type: DL NAS Transport
	buf = append(buf, 0x01) // PayloadContainerType=N1SM (low nibble) + Spare (high)
	buf = append(buf, byte(len(gsmNAS)>>8), byte(len(gsmNAS)&0xFF))
	buf = append(buf, gsmNAS...)
	buf = append(buf, 0x12, pduSessionID) // PDU Session ID 2 (TV 2)
	return buf, nil
}

// snssaiSDHexToBytes parses a hex SD string like "000001" / "010203" into
// the 3-byte big-endian encoding required by TS 38.413 §9.3.1.24.
// Returns nil for empty or the wildcard ("FFFFFF") so the caller can
// omit the optional SD field entirely.
func snssaiSDHexToBytes(s string) []byte {
	if s == "" {
		return nil
	}
	// Pad / truncate to 6 hex digits (3 octets).
	for len(s) < 6 {
		s = "0" + s
	}
	if len(s) > 6 {
		s = s[len(s)-6:]
	}
	b := make([]byte, 3)
	for i := 0; i < 3; i++ {
		hi, lo := hexNibble(s[2*i]), hexNibble(s[2*i+1])
		if hi < 0 || lo < 0 {
			return nil
		}
		b[i] = byte(hi<<4 | lo)
	}
	// Wildcard SD = "FFFFFF" means "no SD" — omit the IE.
	if b[0] == 0xFF && b[1] == 0xFF && b[2] == 0xFF {
		return nil
	}
	return b
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// parseIPv4 converts a dotted-quad string to a 4-byte big-endian slice.
// Returns [0,0,0,0] on parse failure (keeps the encoder from panicking,
// but the gNB won't be able to reach the UPF — check session.UPFN3IP).
func parseIPv4(s string) []byte {
	ip := net.ParseIP(s)
	if ip == nil {
		return []byte{0, 0, 0, 0}
	}
	if v4 := ip.To4(); v4 != nil {
		return []byte{v4[0], v4[1], v4[2], v4[3]}
	}
	return []byte{0, 0, 0, 0}
}

// causeString renders a TS 38.413 §9.3.1.2 Cause choice + sub-value
// as a human-readable token for log lines. Enum names match the ASN.1
// identifiers in TS 38.413 §9.4.5 so operators can grep the spec.
func causeString(c genngap.Cause) string {
	switch c.Present {
	case genngap.CausePresentRadioNetwork:
		if c.RadioNetwork != nil {
			return fmt.Sprintf("radioNetwork(%d)=%s", int64(*c.RadioNetwork), radioNetworkName(*c.RadioNetwork))
		}
	case genngap.CausePresentTransport:
		if c.Transport != nil {
			return fmt.Sprintf("transport(%d)", int64(*c.Transport))
		}
	case genngap.CausePresentNas:
		if c.Nas != nil {
			return fmt.Sprintf("nas(%d)", int64(*c.Nas))
		}
	case genngap.CausePresentProtocol:
		if c.Protocol != nil {
			return fmt.Sprintf("protocol(%d)", int64(*c.Protocol))
		}
	case genngap.CausePresentMisc:
		if c.Misc != nil {
			return fmt.Sprintf("misc(%d)", int64(*c.Misc))
		}
	}
	return fmt.Sprintf("unknown(present=%d)", c.Present)
}

// radioNetworkName maps the CauseRadioNetwork enumerated values most
// commonly seen on the PDU Session Resource Setup failure path to
// their spec identifiers (TS 38.413 §9.3.1.2 ASN.1 module). Values
// outside the list fall back to the numeric form.
func radioNetworkName(v genngap.CauseRadioNetwork) string {
	switch v {
	case genngap.CauseRadioNetworkUnspecified:
		return "unspecified"
	case genngap.CauseRadioNetworkReleaseDueToNgranGeneratedReason:
		return "release-due-to-ngran-generated-reason"
	case genngap.CauseRadioNetworkReleaseDueTo5gcGeneratedReason:
		return "release-due-to-5gc-generated-reason"
	case genngap.CauseRadioNetworkUserInactivity:
		return "user-inactivity"
	case genngap.CauseRadioNetworkRadioConnectionWithUeLost:
		return "radio-connection-with-ue-lost"
	}
	return fmt.Sprintf("value=%d", int64(v))
}

// mapNGAPCauseToGSM picks a 5GSM cause (TS 24.501 §9.11.4.2) for the
// UE-facing PDU SESSION ESTABLISHMENT REJECT based on the NGAP Cause
// the gNB returned. The spec does not mandate a fixed mapping; these
// choices follow the most common operator-core practice:
//
//	radioNetwork   → #26 Insufficient resources (gNB could not allocate
//	                 Uu/DRB resources — matches the enum names
//	                 user-inactivity / radio-connection-with-ue-lost /
//	                 radio-resources-not-available / etc.)
//	transport      → #38 Network failure
//	nas / protocol → #31 Request rejected, unspecified
//	misc / unknown → #31 Request rejected, unspecified
func mapNGAPCauseToGSM(c genngap.Cause) uint8 {
	switch c.Present {
	case genngap.CausePresentRadioNetwork:
		return session.CauseInsufficientResources
	case genngap.CausePresentTransport:
		return session.CauseNetworkFailure
	}
	return session.CauseRequestRejectedUnspecified
}

// Register installs the Response handler (AMF receives
// SuccessfulOutcome after sending PDUSessionResourceSetupRequest).
func Register() {
	ngap.Register(ngap.ProcCodePDUSessionResourceSetup, handleResponse)
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// UL NAS Transport (TS 24.501 §8.2.10) — carries 5GSM (PDU Session
// Management) payloads from the UE to the SMF, plus a handful of
// AMF-terminated use cases (SMS-over-NAS, steering-of-roaming ack).
//
// For N1-SM (PayloadContainerType=1 — PDU session management) the handler
// decodes the inner 5GSM message, extracts PDU Session ID / DNN / S-NSSAI,
// and delegates to the SMF for:
//
//	PDUSessionEstablishmentRequest  → session.Establish + pdusetup.Send
//	PDUSessionModificationRequest   → session.Modify  (TODO)
//	PDUSessionReleaseRequest        → session.Release
//
// Port of nf/amf/gmm/gmm_ul_nas_tranport.py.
package gmm

import (
	"errors"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pdurelease"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/pdusetup"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/nssf"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/nf/smf/session/pti"
	"github.com/mmt/mmt-studio-core/nf/smsf"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	nas "github.com/mmt/nasgen/generated"
)

// PayloadContainerType values (TS 24.501 §9.11.3.40).
const (
	PayloadTypeN1SMInfo        = 1
	PayloadTypeSMS             = 2
	PayloadTypeLPPMessage      = 3
	PayloadTypeSORTransparent  = 4
	PayloadTypeUEPolicy        = 5
	PayloadTypeUEParametersUpd = 6
	PayloadTypeLocationService = 7
	PayloadTypeCIoTUserData    = 8
)

func init() {
	Register(MsgULNASTransport, handleULNASTransport)
}

func handleULNASTransport(ue *uectx.AmfUeCtx, _ uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.ulnas")

	log.Debugf("ULNASTransport raw NAS amfUeID=%d (%d bytes): % x", ue.AmfUeNGAPID, len(inner), inner)
	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("ULNASTransport decode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}
	ul, ok := msg.(*nas.ULNASTransport)
	if !ok {
		log.Errorf("ULNASTransport: unexpected type %T", msg)
		return
	}

	containerType := ul.PayloadContainerType.Encode()
	containerBytes := ul.PayloadContainer.EncodeBytes()
	log.WithIMSI(ue.IMSI).Infof("ULNASTransport amfUeID=%d container=0x%X size=%d",
		ue.AmfUeNGAPID, containerType, len(containerBytes))

	switch containerType {
	case PayloadTypeN1SMInfo:
		handleN1SMPayload(ue, ul, containerBytes)
	case PayloadTypeSMS:
		handleSMSPayload(ue, containerBytes)
	default:
		log.Debugf("Unhandled payload container 0x%X amfUeID=%d", containerType, ue.AmfUeNGAPID)
	}

	// Self-loop on REGISTERED — logs the event, state unchanged.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvULNASTransport, Inner: inner})
}

// handleN1SMPayload routes the inner 5GSM message to the appropriate
// SMF handler. The AMF acts as a relay between the UE (NAS) and the
// SMF (N11 / Nsmf_PDUSession service) per TS 23.502 §4.3.2.2.1 steps
// 4-6 and §4.3.3.2 / §4.3.4.2 for modify / release.
//
// 5GSM payload layout — TS 24.501 §9.1.1 "5GS session management
// message" (PDF: specs/3gpp/ts_124501v190602p.pdf):
//
//	EPD(0x2E) | PDUSessionID(1) | PTI(1) | MsgType(1) | payload...
//
// The outer UL NAS Transport also carries AMF-routing IEs the UE
// attaches for first-establishment: DNN (§9.11.2.1A), S-NSSAI
// (§9.11.2.8), Request Type (§9.11.3.47). We extract those first,
// then decode the 5GSM inner and dispatch.
//
// 5GSM message types handled (TS 24.501 §9.7, Table 9.7.1):
//
//	193 PDU SESSION ESTABLISHMENT REQUEST  — §8.3.1, §6.4.1.2
//	197 PDU SESSION MODIFICATION REQUEST   — §8.3.7, §6.4.2.2 (forwarded)
//	204 PDU SESSION MODIFICATION COMPLETE  — §8.3.10 (completes §6.4.2.3)
//	209 PDU SESSION RELEASE REQUEST        — §8.3.13, §6.4.3.2
//	212 PDU SESSION RELEASE COMPLETE       — §8.3.15 (completes §6.4.3.3)
//	214 5GSM STATUS                        — §8.3.17, §6.7
func handleN1SMPayload(ue *uectx.AmfUeCtx, ul *nas.ULNASTransport, gsm []byte) {
	log := logger.Get("amf.gmm.ulnas")

	// Pull PDU Session ID + DNN + S-NSSAI from the outer UL NAS Transport IEs
	// (these are provided by the UE alongside the container for AMF routing).
	var pduSessionID uint8
	if ul.PDUSessionID != nil {
		pduSessionID = ul.PDUSessionID.Value
	}
	var dnn string
	if ul.DNN != nil {
		dnn = ul.DNN.Value
	}
	var sst uint8
	var sd string
	if ul.SNSSAI != nil && len(ul.SNSSAI.Value) >= 1 {
		sst = ul.SNSSAI.Value[0]
		if len(ul.SNSSAI.Value) >= 4 {
			sd = fmt.Sprintf("%X", ul.SNSSAI.Value[1:4])
		}
	}

	// Decode the 5GSM message inside the container.
	decoded, err := nas.DecodeNASMessage(gsm)
	if err != nil {
		log.Warnf("5GSM inner decode amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	switch sm := decoded.(type) {
	case *nas.PDUSessionEstablishmentRequest:
		if pduSessionID == 0 {
			pduSessionID = sm.PDUSessionID
		}
		if dnn == "" && sm.SMPDUDN != nil {
			dnn = sm.SMPDUDN.Value
		}

		// ── S-NSSAI resolution (TS 23.502 §4.3.2.2.1 step 2) ──
		// If the NAS message doesn't contain an S-NSSAI, AMF selects from
		// Allowed NSSAI — single entry if only one, else default-marked
		// from subscription, else first.
		if sst == 0 {
			sst, sd = resolveSNSSAI(ue)
		}

		// ── DNN resolution (TS 23.501 §5.6.1) ──
		// When UE doesn't provide DNN, AMF selects default DNN for the
		// chosen S-NSSAI from UE's subscription (defaultDnnIndicator).
		dnnMode := "ue-provided"
		if dnn == "" {
			dnnMode = "amf-selected"
			// TODO(arch: sbi-N8: Nudm_SDM_Get) —
			//   specs/3gpp/ts_129503v190600p.pdf §5.2 "Resource Tree":
			//   GET /{supi}/sm-data returns the SM subscription data
			//   (SmfSelectionData, DnnConfigurations, default-DNN).
			//   Same pattern for udm.GetDefaultSNSSAI below (Nudm_SDM
			//   "nssai" resource) and every other udm.* call in gmm/.
			if d, ok := udm.GetDefaultDNN(ue.IMSI, int(sst), sd); ok {
				dnn = d
				log.WithIMSI(ue.IMSI).Infof("AMF resolved default DNN=%s for SST=%d SD=%s",
					dnn, sst, sd)
			} else {
				// No default configured — last-resort fallback
				dnn = "internet"
				log.WithIMSI(ue.IMSI).Warnf("No default DNN for SST=%d SD=%s — using 'internet'",
					sst, sd)
			}
		}

		log.WithIMSI(ue.IMSI).Infof("PDUSessionEstablishmentRequest amfUeID=%d pduSessID=%d dnn=%s sst=%d mode=%s",
			ue.AmfUeNGAPID, pduSessionID, dnn, sst, dnnMode)

		// TS 24.501 v19.6.2 §9.11.4.11 (PDU session type) defines
		// values 1-5 (IPv4, IPv6, IPv4v6, Unstructured, Ethernet);
		// every other 3-bit value is reserved. When the IE is present
		// and out of range, reject with §6.4.1.5 5GSM cause #28
		// "unknown PDU session type". The IE itself is Optional in
		// PDU Session Establishment Request (Table 8.3.1.1.1) — we
		// only validate the value when present, not its presence.
		if sm.PDUSessionType != nil {
			v := sm.PDUSessionType.Value
			if v < nas.PDUSessionTypeIpv4 || v > nas.PDUSessionTypeEthernet {
				log.WithIMSI(ue.IMSI).Warnf("PDU Establishment unknown PDU session type=%d amfUeID=%d — sending 5GSM cause #28 (TS 24.501 §9.11.4.11)",
					v, ue.AmfUeNGAPID)
				rej := session.BuildEstablishReject(pduSessionID,
					session.CauseUnknownPDUSessionType, sm.PTI)
				if rej != nil {
					if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
						_ = dlnas.Send(gnb, ue, rej)
					}
				}
				pm.Inc(pm.SMSessFail, 1)
				return
			}
		}

		// TS 24.501 §9.11.4.16 (SSC mode) defines values 1-3; every
		// other 3-bit value is reserved. SSC mode IE is Optional in
		// the Establishment Request; out-of-range value when present
		// is a §5.5.1.2.8(b) "conditional IE error" → 5GSM cause
		// #100 (we don't have a SMF-side conditional-IE-error const
		// today; CauseRequestRejectedUnspecified=#31 is the closest
		// already-wired reject — TODO when more 5GSM causes land).
		if sm.SSCMode != nil {
			v := sm.SSCMode.Value
			if v < nas.SSCModeSscMode1 || v > nas.SSCModeSscMode3 {
				log.WithIMSI(ue.IMSI).Warnf("PDU Establishment unknown SSC mode=%d amfUeID=%d — sending 5GSM cause #31 (TS 24.501 §9.11.4.16)",
					v, ue.AmfUeNGAPID)
				rej := session.BuildEstablishReject(pduSessionID,
					session.CauseRequestRejectedUnspecified, sm.PTI)
				if rej != nil {
					if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
						_ = dlnas.Send(gnb, ue, rej)
					}
				}
				pm.Inc(pm.SMSessFail, 1)
				return
			}
		}

		// TS 24.501 §7.3 — PTI dedup. Retransmit of the same PTI for
		// the same PDU session replays the cached Accept; a collision
		// (same PTI, different procedure) must be answered with a
		// PDU SESSION ESTABLISHMENT REJECT carrying 5GSM cause #35
		// "PTI already in use" per §6.4.1.4 (cause list) + Annex D
		// (cause semantics: "the PTI included by the UE is already in
		// use by another active UE requested procedure for this UE").
		txn, retx, err := pti.Default.Start(ue.IMSI, sm.PTI, pti.ProcEstablishment, pduSessionID)
		if err != nil {
			var col pti.ErrPTICollision
			if errors.As(err, &col) {
				log.WithIMSI(ue.IMSI).Warnf("PTI %d collision (existing=%s, incoming=%s) amfUeID=%d — sending ESTABLISHMENT REJECT cause #35 (TS 24.501 §6.4.1.4 + Annex D)",
					sm.PTI, col.Existing, col.Incoming, ue.AmfUeNGAPID)
				rej := session.BuildEstablishReject(pduSessionID,
					session.CausePTIAlreadyInUse, sm.PTI)
				if rej != nil {
					if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
						if err := dlnas.Send(gnb, ue, rej); err != nil {
							log.Errorf("DL NAS send EstablishReject(#35) amfUeID=%d: %v", ue.AmfUeNGAPID, err)
						}
					}
				}
				pm.Inc(pm.SMSessFail, 1)
				return
			}
			log.Errorf("PTI %d start failed amfUeID=%d: %v", sm.PTI, ue.AmfUeNGAPID, err)
			pm.Inc(pm.SMSessFail, 1)
			return
		}
		if retx && len(txn.Response) > 0 {
			log.WithIMSI(ue.IMSI).Infof("PDU Establishment retx on PTI=%d — replaying cached Accept",
				sm.PTI)
			// Cached path: re-ship the prior Accept bytes without re-running
			// session.Establish or installing UPF rules twice.
			if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
				if sess := session.Default.Get(ue.IMSI, pduSessionID); sess != nil {
					_, _ = pdusetup.Send(gnb, ue, sess, txn.Response)
				}
			}
			return
		}

		// Early abort: if the gNB already disconnected (tester SHUTDOWN
		// / kernel reset) there's no point spending UPF + PFCP cycles
		// on a PDU session whose Resource-Setup-Request can't be
		// delivered. Check here; a second check after Establish catches
		// the race where the gNB dies during PFCP.
		if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb == nil || !gnb.IsConnected() {
			log.WithIMSI(ue.IMSI).Warnf("PDU Establish skipped amfUeID=%d: gNB %q not connected",
				ue.AmfUeNGAPID, ue.GnbKey)
			pm.Inc(pm.SMSessFail, 1)
			return
		}

		// TODO(arch: sbi-N11: Nsmf_PDUSession_CreateSMContext — TS 29.502 §5.2.2.1) —
		//   POST /sm-contexts with the PDU Session Establishment
		//   Request as smContextCreateData. Session.Establish is the
		//   in-process equivalent; every session.* helper in this file
		//   maps to one of the Nsmf_PDUSession operations (Create /
		//   Update / Release) in the same way.
		// §8.3.1.9 Extended Protocol Configuration Options — the
		// UE's list of *Request containers (P-CSCF / DNS / MTU /
		// TFT support indicator). The SMF answers only what was
		// asked (TS 24.008 §10.5.6.3).
		var reqEPCO []byte
		if sm.ExtendedProtocolConfigurationOptions != nil {
			reqEPCO = sm.ExtendedProtocolConfigurationOptions.Value
		}
		// Request type (TS 24.501 §9.11.3.47) lives on the outer UL NAS
		// TRANSPORT per §8.2.10.4: "The UE shall include this IE when
		// the PDU session ID IE is included and the Payload container
		// IE contains the PDU SESSION ESTABLISHMENT REQUEST message".
		// Drives §6.4.1.7 collision semantics at the SMF.
		var reqType uint8
		if ul.RequestType != nil {
			reqType = ul.RequestType.Value
		}
		out, err := session.Establish(session.EstablishInput{
			IMSI:             ue.IMSI,
			PDUSessionID:     pduSessionID,
			PTI:              sm.PTI,
			DNN:              dnn,
			SST:              sst,
			SD:               sd,
			RequestedPDUType: reqPDNType(sm),
			RequestType:      reqType,
			RequestedExtPCO:  reqEPCO,
		})
		if errors.Is(err, session.ErrPDUSessionIDInUse) {
			// TS 24.501 §6.4.1.7 item (c) was not applicable (Request
			// type wasn't "initial request") and the PSI is in use at
			// the SMF. Reject per §6.4.1.4 with §9.11.4.2 cause #43
			// (via §7.3.2 mismatched-ID path).
			log.WithIMSI(ue.IMSI).Warnf("PDU session id %d in use — sending ESTABLISHMENT REJECT cause #43 (TS 24.501 §6.4.1.7 non-initial-request path)",
				pduSessionID)
			pm.Inc(pm.SMSessFail, 1)
			rej := session.BuildEstablishReject(pduSessionID,
				session.CauseInvalidPDUSessionIdentity, sm.PTI)
			if rej != nil {
				if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
					if err := dlnas.Send(gnb, ue, rej); err != nil {
						log.Errorf("DL NAS send EstablishReject amfUeID=%d: %v", ue.AmfUeNGAPID, err)
					}
				}
				pti.Default.Complete(ue.IMSI, sm.PTI, rej)
			}
			return
		}
		if errors.Is(err, session.ErrPDUSessionDoesNotExist) {
			// TS 24.501 §6.4.1.7 item (d) — Request type was "existing
			// PDU session" / "existing emergency PDU session" but no
			// SMF record. Reject with 5GSM cause #54 per spec verbatim.
			log.WithIMSI(ue.IMSI).Warnf("PDU session %d not found for Request type=existing — sending ESTABLISHMENT REJECT cause #54 (TS 24.501 §6.4.1.7 item d)",
				pduSessionID)
			pm.Inc(pm.SMSessFail, 1)
			rej := session.BuildEstablishReject(pduSessionID,
				session.CausePDUSessionDoesNotExist, sm.PTI)
			if rej != nil {
				if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
					if err := dlnas.Send(gnb, ue, rej); err != nil {
						log.Errorf("DL NAS send EstablishReject(#54) amfUeID=%d: %v", ue.AmfUeNGAPID, err)
					}
				}
				pti.Default.Complete(ue.IMSI, sm.PTI, rej)
			}
			return
		}
		if err != nil {
			log.Errorf("session.Establish amfUeID=%d: %v", ue.AmfUeNGAPID, err)
			pm.Inc(pm.SMSessFail, 1)
			return
		}

		// Track on the AMF-side UE context.
		ue.PDUSessions[int(pduSessionID)] = &uectx.AmfPduSession{
			PDUSessionID: int(pduSessionID),
			DNN:          dnn,
			SST:          int(sst),
			SD:           sd,
			State:        "ACTIVE",
		}

		// Ship to gNB via NGAP PDUSessionResourceSetupRequest with the
		// 5GSM Accept piggybacked.
		gnb := gnbctx.Default.GetByIP(ue.GnbKey)
		if gnb == nil {
			log.Errorf("gNB %q gone — cannot send PDU session setup", ue.GnbKey)
			return
		}
		if _, err := pdusetup.Send(gnb, ue, out.Session, out.AcceptNAS); err != nil {
			// ErrNoTransport during teardown is an expected race: PFCP
			// Establish Response arrived after the gNB SHUTDOWN. Log at
			// WARN (not ERROR) so operators scanning for real failures
			// aren't misled. Cascade release will clean up the orphan
			// UPF session.
			if errors.Is(err, gnbctx.ErrNoTransport) {
				log.WithIMSI(ue.IMSI).Warnf("pdusetup.Send amfUeID=%d: %v (gNB disconnected during PFCP)",
					ue.AmfUeNGAPID, err)
			} else {
				log.Errorf("pdusetup.Send amfUeID=%d: %v", ue.AmfUeNGAPID, err)
			}
		}

		// Cache the Accept bytes against the PTI so a UE retransmit gets
		// the identical reply instead of re-running the procedure.
		pti.Default.Complete(ue.IMSI, sm.PTI, out.AcceptNAS)

	case *nas.PDUSessionReleaseRequest:
		if pduSessionID == 0 {
			pduSessionID = sm.PDUSessionID
		}
		log.WithIMSI(ue.IMSI).Infof("PDUSessionReleaseRequest amfUeID=%d pduSessID=%d", ue.AmfUeNGAPID, pduSessionID)

		// PTI dedup for Release (§7.3). On retransmit, re-ship the
		// cached Release Command instead of releasing again. On
		// collision (same PTI, different procedure), spec §6.4.3.4
		// lists 5GSM cause #35 "PTI already in use" among the
		// "typical" causes for PDU SESSION RELEASE REJECT.
		txn, retx, err := pti.Default.Start(ue.IMSI, sm.PTI, pti.ProcRelease, pduSessionID)
		if err != nil {
			var col pti.ErrPTICollision
			if errors.As(err, &col) {
				log.WithIMSI(ue.IMSI).Warnf("PTI %d collision (existing=%s, incoming=%s) amfUeID=%d — sending RELEASE REJECT cause #35 (TS 24.501 §6.4.3.4 + Annex D)",
					sm.PTI, col.Existing, col.Incoming, ue.AmfUeNGAPID)
				rej := session.BuildReleaseReject(pduSessionID,
					session.CausePTIAlreadyInUse, sm.PTI)
				if rej != nil {
					if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
						if err := dlnas.Send(gnb, ue, rej); err != nil {
							log.Errorf("DL NAS send ReleaseReject(#35) amfUeID=%d: %v", ue.AmfUeNGAPID, err)
						}
					}
				}
				return
			}
			log.Errorf("PTI %d start failed amfUeID=%d: %v", sm.PTI, ue.AmfUeNGAPID, err)
			return
		}
		if retx && len(txn.Response) > 0 {
			log.WithIMSI(ue.IMSI).Infof("PDU Release retx on PTI=%d — replaying cached Command",
				sm.PTI)
			if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
				_ = pdurelease.SendCommand(gnb, ue, []uint8{pduSessionID}, txn.Response,
					pdurelease.CauseNAS(genngap.CauseNasNormalRelease))
			}
			return
		}

		// TS 24.501 §6.2.1: echo UE's PTI in Release Command.
		// TODO(arch: sbi-N11: Nsmf_PDUSession_UpdateSMContext / ReleaseSMContext — TS 29.502)
		releaseCmdBytes := session.ReleaseWithCause(ue.IMSI, pduSessionID,
			session.CauseRegularDeactivation, sm.PTI)
		delete(ue.PDUSessions, int(pduSessionID))
		// Ship via NGAP PDU SESSION RESOURCE RELEASE COMMAND (§8.2.2)
		// with the 5GSM Release Command piggybacked as NAS-PDU IE so
		// the gNB tears down DRB + NG-U tunnel in the same message.
		if releaseCmdBytes != nil {
			if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
				if err := pdurelease.SendCommand(gnb, ue, []uint8{pduSessionID}, releaseCmdBytes,
					pdurelease.CauseNAS(genngap.CauseNasNormalRelease)); err != nil {
					log.Errorf("pdurelease.SendCommand amfUeID=%d: %v", ue.AmfUeNGAPID, err)
				}
			}
			pti.Default.Complete(ue.IMSI, sm.PTI, releaseCmdBytes)
		}

	case *nas.PDUSessionModificationRequest:
		if pduSessionID == 0 {
			pduSessionID = sm.PDUSessionID
		}
		log.WithIMSI(ue.IMSI).Infof("PDUSessionModificationRequest amfUeID=%d pduSessID=%d dnn=%s",
			ue.AmfUeNGAPID, pduSessionID, dnn)
		pm.Inc(pm.SMModAtt, 1)

		// PTI dedup for Modification (§7.3). On collision (same PTI,
		// different procedure), spec §6.4.2.4 lists 5GSM cause #35
		// "PTI already in use" among the "typical" causes for PDU
		// SESSION MODIFICATION REJECT.
		txn, retx, err := pti.Default.Start(ue.IMSI, sm.PTI, pti.ProcModification, pduSessionID)
		if err != nil {
			var col pti.ErrPTICollision
			if errors.As(err, &col) {
				log.WithIMSI(ue.IMSI).Warnf("PTI %d collision (existing=%s, incoming=%s) amfUeID=%d — sending MODIFICATION REJECT cause #35 (TS 24.501 §6.4.2.4 + Annex D)",
					sm.PTI, col.Existing, col.Incoming, ue.AmfUeNGAPID)
				rej := session.BuildModifyReject(pduSessionID,
					session.CausePTIAlreadyInUse, sm.PTI)
				if rej != nil {
					if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
						if err := dlnas.Send(gnb, ue, rej); err != nil {
							log.Errorf("DL NAS send ModifyReject(#35) amfUeID=%d: %v", ue.AmfUeNGAPID, err)
						}
					}
				}
				return
			}
			log.Errorf("PTI %d start failed amfUeID=%d: %v", sm.PTI, ue.AmfUeNGAPID, err)
			return
		}
		if retx && len(txn.Response) > 0 {
			log.WithIMSI(ue.IMSI).Infof("PDU Modification retx on PTI=%d — replaying cached Command",
				sm.PTI)
			if gnb := gnbctx.Default.GetByIP(ue.GnbKey); gnb != nil {
				// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
				_ = dlnas.Send(gnb, ue, txn.Response)
			}
			return
		}

		// TODO(arch: sbi-N11: Nsmf_PDUSession_UpdateSMContext — TS 29.502)
		modCmdBytes, err := session.Modify(ue.IMSI, pduSessionID, sm.PTI)
		if err != nil {
			log.Errorf("session.Modify amfUeID=%d pduSessID=%d: %v",
				ue.AmfUeNGAPID, pduSessionID, err)
			return
		}
		// Send PDU Session Modification Command via DL NAS Transport.
		if modCmdBytes != nil {
			gnb := gnbctx.Default.GetByIP(ue.GnbKey)
			if gnb != nil {
				// TODO(arch: event: DL-NAS to NGAP — see gmm/doc.go)
				if err := dlnas.Send(gnb, ue, modCmdBytes); err != nil {
					log.Errorf("DL NAS send ModificationCommand amfUeID=%d: %v",
						ue.AmfUeNGAPID, err)
				}
			}
			pti.Default.Complete(ue.IMSI, sm.PTI, modCmdBytes)
		}

	case *nas.PDUSessionReleaseComplete:
		// TS 24.501 §6.4.3.3: UE's final ack of PDU SESSION RELEASE
		// COMMAND. The session has already been torn down locally when
		// the Release Command was built (establish.go fires
		// EvReleaseRequest + EvReleaseComplete synchronously). The NGAP
		// §8.2.2 PDU SESSION RESOURCE RELEASE RESPONSE from the gNB
		// tears down the radio side. This message closes the PTI and is
		// otherwise observational.
		if pduSessionID == 0 {
			pduSessionID = sm.PDUSessionID
		}
		log.WithIMSI(ue.IMSI).Infof("PDUSessionReleaseComplete amfUeID=%d pduSessID=%d PTI=%d",
			ue.AmfUeNGAPID, pduSessionID, sm.PTI)
		pti.Default.Release(ue.IMSI, sm.PTI)

	case *nas.PDUSessionModificationComplete:
		// TS 24.501 §6.4.2.3: UE ack of PDU SESSION MODIFICATION
		// COMMAND (§8.3.10, type 204) — modification is now
		// installed end-to-end. Close the PTI and let the 5GSM FSM
		// advance via §6.4.2.3's "the SMF shall consider the PDU
		// session modification procedure as completed".
		if pduSessionID == 0 {
			pduSessionID = sm.PDUSessionID
		}
		log.WithIMSI(ue.IMSI).Infof("PDUSessionModificationComplete amfUeID=%d pduSessID=%d PTI=%d (TS 24.501 §6.4.2.3 — modification installed)",
			ue.AmfUeNGAPID, pduSessionID, sm.PTI)
		pti.Default.Release(ue.IMSI, sm.PTI)
		pm.Inc(pm.SMModSucc, 1)
		// TS 23.502 §4.3.3 step 8: SMF→PCF via Npcf_SMPolicy
		// Control_Update with §4.2.3.5 trigger SUCC_RES_ALLO
		// "Successful resource allocation". Today we just log;
		// firing the trigger requires a SmPolicy ctxRef lookup
		// from session.Get(imsi, pduSessID). Plumbed in
		// session.HandleModificationComplete so the AMF doesn't
		// have to import smpolicy directly.
		session.HandleModificationComplete(ue.IMSI, pduSessionID)

	default:
		// TODO(spec: TS 24.501 v19.6.2 §7.4) — "If the network
		//   receives a message with message type not defined for the
		//   EPD or not implemented by the receiver, it shall ignore
		//   the message except that it should return a status message
		//   (5GMM STATUS or 5GSM STATUS depending on the EPD) with
		//   cause #97 'message type non-existent or not implemented'."
		//   "should" not "shall" — the AMF is permitted to drop
		//   silently. Wiring 5GSM STATUS requires building a DL NAS
		//   Transport envelope with Payload Container Type = "N1 SM
		//   information" (§9.11.3.40) wrapping a FivegsmStatus body.
		//   Deferred until a concrete tester scenario exercises it.
		log.Warnf("Unhandled 5GSM type %T amfUeID=%d (silent drop permitted by §7.4 'should')",
			decoded, ue.AmfUeNGAPID)
	}
}

// handleSMSPayload is the AMF side of the MO-SMS-over-NAS path
// (TS 23.502 §4.13.3.5). The Payload Container we receive carries
// the SM-CP message per TS 24.501 §9.11.3.39 / TS 24.011 §7.2.
//
// Steps (mapping to TS 23.502 §4.13.3.5):
//
//	step 4 (received): UE has just sent UL NAS Transport(SMS, CP-DATA).
//	step 5: AMF must immediately ACK the CP-Layer with a CP-ACK
//	        wrapped in DL NAS Transport(SMS) — TS 24.011 §7.2.2.
//	step 6: SMSF processes the inner RP-DATA(SMS-SUBMIT) and the AMF
//	        sends a follow-up DL NAS Transport(SMS, CP-DATA(RP-ACK))
//	        once the SC has accepted the message.
//
// Both DL responses re-use the same wrapInDLNASTransport-equivalent
// path used for N1SM (see pdusetup.go) but with PayloadContainerType
// = 2 (SMS) per TS 24.501 §9.11.3.40.
func handleSMSPayload(ue *uectx.AmfUeCtx, payload []byte) {
	log := logger.Get("amf.gmm.ulnas").WithIMSI(ue.IMSI)

	resp, err := smsf.ProcessMOSMSFromNAS(ue.IMSI, payload)
	if err != nil {
		log.Warnf("SMSF MO process amfUeID=%d: %v", ue.AmfUeNGAPID, err)
		return
	}

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.Warnf("SMS over NAS amfUeID=%d: no gNB ctx for %s — dropping DL responses",
			ue.AmfUeNGAPID, ue.GnbKey)
		return
	}

	// TS 23.502 §4.13.3.5 step 5 — CP-ACK back to UE.
	if len(resp.CPAck) > 0 {
		dlNAS := wrapInDLNASTransportSMS(resp.CPAck)
		if err := dlnas.Send(gnb, ue, dlNAS); err != nil {
			log.Warnf("SMS over NAS amfUeID=%d: send CP-ACK: %v",
				ue.AmfUeNGAPID, err)
		}
	}

	// TS 23.502 §4.13.3.5 step 6 — CP-DATA(RP-ACK / RP-ERROR).
	var followup []byte
	switch {
	case len(resp.RPAckCPData) > 0:
		followup = resp.RPAckCPData
	case len(resp.RPErrorCPData) > 0:
		followup = resp.RPErrorCPData
	}
	if len(followup) > 0 {
		dlNAS := wrapInDLNASTransportSMS(followup)
		if err := dlnas.Send(gnb, ue, dlNAS); err != nil {
			log.Warnf("SMS over NAS amfUeID=%d: send RP follow-up: %v",
				ue.AmfUeNGAPID, err)
		}
	}

	if resp.MO.OK {
		log.Infof("MO-SMS amfUeID=%d ok=%v segments=%d encoding=%s status=%s",
			ue.AmfUeNGAPID, resp.MO.OK, resp.MO.Segments, resp.MO.Encoding, resp.MO.Status)
	}
}

// wrapInDLNASTransportSMS wraps a Payload-Container-format SMS PDU
// in a 5GMM DL NAS Transport message per TS 24.501 §8.2.11 with
// PayloadContainerType = 2 (SMS) per §9.11.3.40 Table 9.11.3.40.1.
//
// Layout (§8.2.11 + §9.11.3.39 / §9.11.3.40):
//
//	EPD(0x7E) + SHT(0x00) + MsgType(0x68=DL NAS Transport)
//	+ PayloadContainerType(0x02=SMS, low nibble) + Spare(high nibble = 0)
//	+ PayloadContainer(LV-E: 2-byte length + sms bytes) — §9.11.3.39
//
// No PDUSessionID, DNN or S-NSSAI IEs are appended: §8.2.11.2 says
// "The AMF shall include this IE when the Payload container type IE
// is set to 'N1 SM information' or 'CIoT user data container'" — for
// SMS payloads the IE is absent.
func wrapInDLNASTransportSMS(smsPayload []byte) []byte {
	var buf []byte
	buf = append(buf, 0x7E) // EPD: 5GMM
	buf = append(buf, 0x00) // Security header type: plain
	buf = append(buf, 0x68) // Message type: DL NAS Transport
	buf = append(buf, 0x02) // PayloadContainerType=SMS; spare=0 in high nibble
	buf = append(buf, byte(len(smsPayload)>>8), byte(len(smsPayload)&0xFF))
	buf = append(buf, smsPayload...)
	return buf
}

// DNN decoding lives in the codec runtime (runtime.DNN) — consumers
// read the typed Value string directly.

// reqPDNType pulls the requested PDU session type from the 5GSM request.
// Returns 0 when the IE is absent (let SMF decide).
func reqPDNType(sm *nas.PDUSessionEstablishmentRequest) uint8 {
	if sm.PDUSessionType != nil {
		return sm.PDUSessionType.Value
	}
	return 0
}

// resolveSNSSAI picks the S-NSSAI for a PDU Session when the UE didn't
// include one. TS 23.502 §4.3.2.2.1 step 2:
//
//  1. If Allowed NSSAI has exactly one entry → use it.
//  2. If multiple → use the default-marked one from subscription (UDM).
//  3. Else → first entry of Allowed NSSAI.
//
// Returns (sst, sdHex) where sdHex is "" for no-SD.
func resolveSNSSAI(ue *uectx.AmfUeCtx) (uint8, string) {
	allowed, _ := ue.AllowedNSSAI.([]nssf.SNSSAI)
	if len(allowed) == 0 {
		return 0, ""
	}
	if len(allowed) == 1 {
		return allowed[0].SST, sdHexFromUint32(allowed[0].SD)
	}
	// Multiple — ask UDM for the default from subscription
	// TODO(arch: sbi-N8: Nudm_SDM_Get — see gmm/doc.go)
	if sst, sdHex, ok := udm.GetDefaultSNSSAI(ue.IMSI); ok {
		return uint8(sst), sdHex
	}
	return allowed[0].SST, sdHexFromUint32(allowed[0].SD)
}

func sdHexFromUint32(sd uint32) string {
	if sd == 0 || sd == 0xFFFFFF {
		return ""
	}
	return fmt.Sprintf("%06X", sd)
}

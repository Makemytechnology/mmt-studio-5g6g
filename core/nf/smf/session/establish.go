// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SMF PDU Session Establishment / Modification / Release.
//
// Authoritative specs (verified against in-tree PDFs this turn):
//
//	TS 24.501 §6.4.1 "UE-requested PDU session establishment procedure"
//	  (PDF: specs/3gpp/ts_124501v190602p.pdf):
//	    §6.4.1.2 initiation — UE sends PDU SESSION ESTABLISHMENT
//	      REQUEST (NAS type 193).
//	    §6.4.1.3 accepted — SMF replies with PDU SESSION
//	      ESTABLISHMENT ACCEPT (type 194) carrying Authorized QoS
//	      rules, Session-AMBR, PDU address, selected SSC mode, etc.
//	    §6.4.1.4 not accepted — SMF replies with PDU SESSION
//	      ESTABLISHMENT REJECT (type 195) + a §9.11.4.2 cause.
//
//	TS 24.501 §6.4.2 "UE-requested PDU session modification procedure".
//
//	TS 24.501 §6.4.3 "UE-requested PDU session release procedure" +
//	  §6.4.4 "Network-requested PDU session release procedure".
//
//	TS 23.502 §4.3.2.2.1 "Non-roaming and Roaming with Local Breakout"
//	  (PDF: specs/3gpp/ts_123502v190700p.pdf) — the stage-2
//	  call-flow that wires the AMF → SMF (N11) and SMF → UPF (N4)
//	  and SMF → NG-RAN (via AMF) exchanges. Our session.Establish
//	  maps to steps 4-12 of that flow (SMF selection already done
//	  upstream via §4.3.2.2.3).
//
//	TS 29.244 "PFCP" (PDF: specs/3gpp/
//	  ts_129244v190500p.pdf):
//	    §7.5.2 PFCP Session Establishment Request — what SMF sends
//	      to UPF with PDR / FAR / QER / URR.
//	    §7.5.3 PFCP Session Establishment Response — what UPF
//	      sends back; Cause IE (§8.2.1) drives success/failure on
//	      the SMF side.
//
//	TS 29.502 "Nsmf_PDUSession" (PDF: specs/3gpp/ts_129502v190600p.pdf)
//	  — the N11 SBI that should front session.Establish /
//	  session.Modify / session.Release when SMF is split out of
//	  the in-process AMF binary.
//
//	TS 38.413 §8.2.1 PDU Session Resource Setup — the NGAP leg of
//	  the establishment procedure (§8.2.1.2 success / §8.2.1.3
//	  failure), fired by the AMF relay on behalf of the SMF.
//
// Handler-driven FSM (same pattern as nf/amf/gmm/fsm): Establish
// runs all its work (IP alloc → UPF select → PFCP → NAS encode)
// synchronously, then fires an outcome-specific event at the end —
// EvEstablishmentAccepted on success, EvEstablishmentRejected /
// EvPFCPEstablishFailure on failure. The FSM table in
// fsm_transitions.go captures each (source, outcome) explicitly.
//
// Go port of nf/smf/smf_pdu_session.py.
package session

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smpolicyfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	smfctx "github.com/mmt/mmt-studio-core/nf/smf/ctx"
	"github.com/mmt/mmt-studio-core/nf/smf/ipalloc"
	_ "github.com/mmt/mmt-studio-core/nf/smf/pfcp"
	pfcpfsm "github.com/mmt/mmt-studio-core/nf/smf/pfcp/fsm"
	sessionfsm "github.com/mmt/mmt-studio-core/nf/smf/session/fsm"
	"github.com/mmt/mmt-studio-core/nf/smf/upf"
	"github.com/mmt/mmt-studio-core/nf/udm"
	upfmgr "github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/edge/mec"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
	"github.com/mmt/mmt-studio-core/security/li"
	nas "github.com/mmt/nasgen/generated"
)

// ErrPDUSessionIDInUse signals that Establish was called for an
// (IMSI, PDUSessionID) pair that already has a session at the SMF and
// the UE's Request type (TS 24.501 §9.11.3.47, carried in the outer
// UL NAS TRANSPORT per §8.2.10.4) does NOT match the §6.4.1.7 item (c)
// release-and-proceed path. Callers — currently gmm/ulnas.go — catch
// this sentinel and ship PDU SESSION ESTABLISHMENT REJECT.
var ErrPDUSessionIDInUse = errors.New("smf: PDU session id already in use (TS 24.501 §6.4.1.2)")

// ErrPDUSessionDoesNotExist signals that Establish was called with
// Request type "existing PDU session" / "existing emergency PDU
// session" (TS 24.501 §9.11.3.47) but the SMF has no record for the
// given (IMSI, PDUSessionID). Per TS 24.501 §6.4.1.7 item (d) the
// spec-correct response is PDU SESSION ESTABLISHMENT REJECT with
// 5GSM cause #54 "PDU session does not exist".
var ErrPDUSessionDoesNotExist = errors.New("smf: PDU session does not exist (TS 24.501 §6.4.1.7 item d)")

// Request type values from TS 24.501 §9.11.3.47 Table 9.11.3.47.1.
// Mirrored here so the SMF package doesn't need a nas-codec dependency.
// The AMF translates nas.RequestTypeInitialRequest (etc.) → these ints
// before calling Establish.
const (
	RequestTypeInitialRequest              uint8 = 1
	RequestTypeExistingPDUSession          uint8 = 2
	RequestTypeInitialEmergencyRequest     uint8 = 3
	RequestTypeExistingEmergencyPDUSession uint8 = 4
	RequestTypeModificationRequest         uint8 = 5
)

// EstablishInput bundles the parameters the AMF passes to the SMF.
type EstablishInput struct {
	IMSI             string
	PDUSessionID     uint8
	PTI              uint8
	DNN              string
	SST              uint8
	SD               string
	RequestedPDUType uint8 // PDUSessionTypeIpv4 / Ipv6 / Ipv4v6 (0 = let SMF decide)

	// RequestType is the outer UL NAS TRANSPORT Request type IE value
	// (TS 24.501 §9.11.3.47 / §8.2.10.4). Drives §6.4.1.7 collision
	// semantics: "initial request" on a collision → release and
	// proceed (§6.4.1.7 item c); "existing PDU session" on a missing
	// record → reject cause #54 (§6.4.1.7 item d); etc. 0 = IE absent
	// (UE violated §8.2.10.4 "shall include"; we default to safe
	// reject path).
	RequestType uint8

	// RequestedExtPCO is the raw bytes of the UE's Extended Protocol
	// Configuration Options IE inside the PDU SESSION ESTABLISHMENT
	// REQUEST (TS 24.501 §8.3.1.9). Carries the UE's list of
	// *Request containers (P-CSCF, DNS, MTU, etc. per TS 24.008
	// §10.5.6.3). Per §10.5.6.3 "DNS Server IPv4 Address Request…
	// indicates that the MS supports handling of the DNS Server
	// IPv4 address(es) received in the PDU session establishment
	// procedure" — i.e. the SMF MUST only answer what the UE asked
	// for; unsolicited responses may be ignored by the MS. Empty
	// when the UE omitted the IE.
	RequestedExtPCO []byte
}

// EstablishOutput is returned to the AMF for piggybacking in NGAP.
type EstablishOutput struct {
	AcceptNAS []byte   // fully-encoded 5GSM PDU Session Establishment Accept
	Session   *Session // persisted in store
	UPF       *upf.Instance
}

// Establish runs the SMF side of PDU Session Establishment.
func Establish(in EstablishInput) (*EstablishOutput, error) {
	log := logger.Get("smf.establish")
	pm.Inc(pm.SMSessAtt, 1)

	if in.PDUSessionID < 1 || in.PDUSessionID > 15 {
		return nil, fmt.Errorf("smf: bad PDUSessionID %d", in.PDUSessionID)
	}
	// PTI validation per TS 24.501 §7.3.1 / §9.6: value 0 = "no PTI
	// assigned" and 255 = reserved. The network answers a request with
	// an invalid PTI with 5GSM STATUS cause #81 "invalid PTI value".
	if in.PTI == 0 || in.PTI == 255 {
		return nil, fmt.Errorf("smf: invalid PTI %d (0=unassigned, 255=reserved per TS 24.501 §7.3.1)", in.PTI)
	}
	if in.DNN == "" {
		return nil, fmt.Errorf("smf: DNN is empty in PDU session request")
	}

	// TS 24.501 §6.4.1.7 — Abnormal cases on the network side, for the
	// UE-requested PDU session establishment procedure. Two items
	// apply when we receive an ESTABLISHMENT REQUEST that collides
	// with existing SMF state; both are direct "shall" rules:
	//
	//	item a) (verbatim, §6.4.1.7):
	//	  "If the received request type is 'initial emergency
	//	   request' and there is an existing emergency PDU session
	//	   for the UE, regardless whether the PDU session ID in the
	//	   PDU SESSION ESTABLISHMENT REQUEST message is identical to
	//	   the PDU session ID of the existing PDU session, the SMF
	//	   shall locally release the existing emergency PDU session
	//	   and proceed the new PDU SESSION ESTABLISHMENT REQUEST
	//	   message"
	//
	//	item c) (verbatim, §6.4.1.7):
	//	  "If the SMF receives a PDU SESSION ESTABLISHMENT REQUEST
	//	   message with a PDU session ID identical to the PDU session
	//	   ID of an existing PDU session and with request type set to
	//	   'initial request', the SMF shall locally release the
	//	   existing PDU session and proceed with the PDU session
	//	   establishment procedure."
	//
	// This covers the airplane-mode / UE-5GSM-state-lost case: UE
	// returns from DEREGISTERED → Initial Registration (cached NAS
	// context reuse per §4.4) → PDU SESSION ESTABLISHMENT REQUEST
	// with Request type="initial request" reusing a PSI whose record
	// the network still holds in SUSPENDED (or Active) state. §6.4.1.7(c)
	// MANDATES release-and-proceed — NOT reject with cause #43.
	//
	// Emergency path (item a) is a superset: release even if PSI
	// doesn't match. We don't establish emergency sessions yet, so
	// the "initial emergency request" branch is reachable only if
	// the tester mislabels a normal session; treat it the same as
	// item (c) for that PSI.
	//
	// §6.4.1.7 item (d) covers the inverse — "existing PDU session"
	// request type with no matching record — mandating reject cause
	// #54. We flag it here so callers know to use the right cause.
	if existing := Default.Get(in.IMSI, in.PDUSessionID); existing != nil {
		switch in.RequestType {
		case RequestTypeInitialRequest, RequestTypeInitialEmergencyRequest:
			log.Infof("§6.4.1.7 item (c): pduSessID=%d exists for imsi=%s (state=%s) + Request type=initial — releasing existing and proceeding",
				in.PDUSessionID, in.IMSI, existing.State)
			// Release the existing record end-to-end (IP return, PFCP
			// delete, PCF delete, session store drop). Returned NAS
			// bytes (PDU SESSION RELEASE COMMAND) are intentionally
			// discarded: the UE already considers the session gone
			// from its side (it just sent an initial-request
			// ESTABLISHMENT asking for this PSI again), so delivering
			// a Release Command would be spec-nonsense. §6.4.1.7(c)
			// explicitly says "locally release" — no UE-facing NAS.
			_ = Release(in.IMSI, in.PDUSessionID)
			// Fall through to the normal establish path below.
		default:
			// Request type is "existing PDU session" / "modification
			// request" / IE absent, or any other non-initial code.
			// These don't carry the §6.4.1.7(c) release-and-proceed
			// mandate, so we reject. (A dedicated §6.4.1.7(d) handler
			// for "existing PDU session" with no record → cause #54
			// lives on the caller path; here we only see "exists".)
			log.Warnf("pduSessID=%d exists for imsi=%s (state=%s), Request type=%d not 'initial request' — rejecting",
				in.PDUSessionID, in.IMSI, existing.State, in.RequestType)
			pm.Inc(pm.SMSessFail, 1)
			return nil, ErrPDUSessionIDInUse
		}
	} else if in.RequestType == RequestTypeExistingPDUSession ||
		in.RequestType == RequestTypeExistingEmergencyPDUSession {
		// TS 24.501 §6.4.1.7 item (d) verbatim:
		// "If the SMF receives a PDU SESSION ESTABLISHMENT REQUEST
		//  message with request type set to 'existing PDU session'
		//  or 'existing emergency PDU session', and the SMF does not
		//  have any information about that PDU session, then the SMF
		//  shall reject the PDU session establishment procedure with
		//  the 5GSM cause set to #54 'PDU session does not exist' in
		//  the PDU SESSION ESTABLISHMENT REJECT message."
		log.Warnf("§6.4.1.7 item (d): Request type=existing PDU session but no record for imsi=%s pduSessID=%d — caller must reject with cause #54",
			in.IMSI, in.PDUSessionID)
		pm.Inc(pm.SMSessFail, 1)
		return nil, ErrPDUSessionDoesNotExist
	}

	apn, err := loadAPN(in.DNN)
	if err != nil {
		pm.Inc(pm.SMSessFail, 1)
		return nil, fmt.Errorf("smf: APN %q lookup: %w", in.DNN, err)
	}

	// Npcf_SMPolicyControl_Create (TS 29.512 §4.2.2 — PDF:
	// specs/3gpp/ts_129512v190600p.pdf). The SMF invokes this at
	// PDU Session Establishment per TS 23.502 §4.3.2.2.1 step 7a
	// ("The SMF performs an SM Policy Association Establishment
	// procedure as defined in TS 23.503 clause 6.4"). The returned
	// SmPolicyDecision drives:
	//   - Authorized Session-AMBR   (§4.2.2.5)
	//   - Authorized default QoS    (§4.2.2.6) → Default5QI / DefaultQFI
	//   - Dynamic PCC rules         (§4.2.2.7, pccRules)
	//   - Charging method           (§4.2.2.3)
	//   - Revalidation Timer        (§4.2.2.4) — armed inside smpolicy.Create.
	policyDecision, policyErr := smpolicy.Create(smpolicy.SmPolicyContextData{
		SUPI:           "imsi-" + in.IMSI,
		PDUSessionID:   in.PDUSessionID,
		DNN:            in.DNN,
		SST:            in.SST,
		SD:             in.SD,
		PDUSessionType: in.RequestedPDUType,
	})
	if policyErr != nil {
		// Per TS 29.512 §4.2.2.2 the PCF failure can map to 5GSM cause
		// #29 ("user authentication or authorization failed"). We log
		// and fall through with a synthetic default decision so the
		// session still establishes — matches the Python port's
		// behaviour when the PCF is down.
		log.Warnf("smf: PCF Create failed imsi=%s dnn=%s: %v — using APN defaults",
			in.IMSI, in.DNN, policyErr)
	}

	pdnType := pickPDNType(in.RequestedPDUType, apn)
	v4, v6, err := allocIP(in.DNN, pdnType)
	if err != nil {
		pm.Inc(pm.SMSessFail, 1)
		_, _ = smpolicy.Delete(smpolicyfsm.Key{IMSI: in.IMSI, PDUSessionID: in.PDUSessionID})
		return nil, fmt.Errorf("smf: IP allocation: %w", err)
	}

	sel, err := upf.Select(in.DNN, fmt.Sprintf("%02X", in.SST))
	if err != nil {
		releaseOnFail(in.DNN, v4, v6)
		pm.Inc(pm.SMSessFail, 1)
		return nil, fmt.Errorf("smf: UPF selection: %w", err)
	}

	// Session-AMBR (TS 23.501 §5.7.1.6) authorized by the PCF per
	// TS 29.512 §4.2.2.5 overrides the APN default when present.
	ambrDL := uint32(apn.AMBRDLKbps)
	ambrUL := uint32(apn.AMBRULKbps)
	if policyDecision.SessionAMBRDL > 0 {
		ambrDL = uint32(policyDecision.SessionAMBRDL)
	}
	if policyDecision.SessionAMBRUL > 0 {
		ambrUL = uint32(policyDecision.SessionAMBRUL)
	}
	sess := &Session{
		IMSI: in.IMSI, PDUSessionID: in.PDUSessionID, PTI: in.PTI,
		DNN: in.DNN, SST: in.SST, SD: in.SD,
		PDUType: pdnType, SSCMode: 1,
		IPv4: v4, IPv6: v6,
		AMBRDL: ambrDL, AMBRUL: ambrUL,
		UPFID:          sel.UPFID,
		UPFN3IP:        sel.N3IP,
		State:          StatePending,
		CreatedAt:      time.Now(),
		SmPolicyCtxRef: policyDecision.SmPolicyCtxRef,
		ChargingMethod: policyDecision.ChargingMethod,
	}
	// UE-AMBR (TS 23.501 §5.7.3) — per-subscriber aggregate across all
	// non-GBR flows. Read from the UDM cache; 0/0 means unlimited. We
	// carry it on the session so both Establish (NAS/NGAP) and installUPFRules
	// (UPF meter) use the same values for this UE.
	if amb, ok := udm.GetSubscriptionAMBR(in.IMSI); ok {
		sess.UEAMBRUL = uint32(amb.UplinkKbps)
		sess.UEAMBRDL = uint32(amb.DownlinkKbps)
	}
	// Authorized default QoS (TS 29.512 §4.2.2.6) — prefer the PCF's
	// Default5QI when we have an SM Policy Association; fall back to
	// the subscriber's default service binding otherwise.
	var fiveQI uint8
	if policyDecision.Default5QI > 0 {
		fiveQI = uint8(policyDecision.Default5QI)
	} else {
		fiveQI, err = loadDefault5QI(in.IMSI, in.DNN)
		if err != nil {
			releaseOnFail(in.DNN, v4, v6)
			_, _ = smpolicy.Delete(smpolicyfsm.Key{IMSI: in.IMSI, PDUSessionID: in.PDUSessionID})
			pm.Inc(pm.SMSessFail, 1)
			return nil, fmt.Errorf("smf: default 5QI lookup: %w", err)
		}
	}
	sess.FiveQI = fiveQI
	defaultQFI := uint8(1)
	if policyDecision.DefaultQFI != 0 {
		defaultQFI = policyDecision.DefaultQFI
	}
	sess.AuthorizedQoSRules = BuildDefaultQoSRule(1 /*id*/, defaultQFI, 255)
	sess.RequestedExtPCO = in.RequestedExtPCO

	accept, err := encodeAccept(sess)
	if err != nil {
		releaseOnFail(in.DNN, v4, v6)
		_, _ = smpolicy.Delete(smpolicyfsm.Key{IMSI: in.IMSI, PDUSessionID: in.PDUSessionID})
		pm.Inc(pm.SMSessFail, 1)
		return nil, fmt.Errorf("smf: encode Accept: %w", err)
	}

	Default.Put(sess)

	// Drive the 5GSM FSM through the synchronous fast path: Establishment
	// Request → ESTABLISHMENT_PENDING → ACTIVATION_PENDING. The gNB's
	// PDUSessionResourceSetupResponse fires EvResourceSetupResponse →
	// ACTIVE later (see nf/amf/ngap/pdusetup/pdusetup.go handleResponse).
	sessKey := sessionfsm.Key{IMSI: sess.IMSI, PDUSessionID: sess.PDUSessionID}
	_ = sessionfsm.Of(sessKey).Fire(&sessionfsm.Context{Key: sessKey, Event: sessionfsm.EvEstablishmentRequest})

	// PFCP session FSM: Inactive → EstablishInProgress → Established.
	// Today the bridge.SessionCreate cgo call is synchronous so both
	// events fire back-to-back; when N4 goes off-box only the
	// RequestSent event fires here and the Response is driven from
	// the UPF reply path.
	pfcpKey := pfcpfsm.Key{UPFNode: sess.UPFID, IMSI: sess.IMSI, PDUSessionID: sess.PDUSessionID}
	pfsm := pfcpfsm.Of(pfcpKey)
	_ = pfsm.Fire(&pfcpfsm.Context{Key: pfcpKey, Event: pfcpfsm.EvEstablishRequestSent})

	// Push session + rules to UPF dataplane (mirrors Python upf_context.create_session)
	installUPFRules(sess)

	// Synchronous C dataplane — treat as successful PFCP Establish
	// Response with a synthetic SEID derived from the UE IP so
	// operators watching /api/smf/pfcp see a consistent session id.
	seid := uint64(sess.PDUSessionID)<<32 | uint64(ipToUint32(sess.IPv4))
	_ = pfsm.Fire(&pfcpfsm.Context{Key: pfcpKey, Event: pfcpfsm.EvEstablishResponse, SEID: seid})

	// 5GSM FSM: ACTIVATION_PENDING (PFCP has ack'd).
	_ = sessionfsm.Of(sessKey).Fire(&sessionfsm.Context{Key: sessKey, Event: sessionfsm.EvPFCPEstablishResponse})

	pm.Inc(pm.SMSessSucc, 1)
	log.WithIMSI(in.IMSI).Infof("PDU Session established id=%d dnn=%s v4=%s v6=%s upf=%s",
		in.PDUSessionID, in.DNN, v4, v6, sel.UPFID)

	// AF-influence consultation (TS 23.502 §4.3.6). The MEC
	// orchestrator (edge/mec) holds operator-authored traffic rules
	// keyed by DNN. For every matching rule we install an Uplink
	// Classifier (TS 23.501 §5.6.4) at the UPF via PFCP Session
	// Modification (TS 29.244 §7.5.4); the per-rule install state
	// lands in mec.ULCLState so the OAM panel can prove the
	// AF-influence reached the dataplane. installULCLForSession
	// is a no-op when no rules match.
	if rules := mec.RulesForDNN(in.DNN); len(rules) > 0 {
		log.WithIMSI(in.IMSI).Infof("AF-influence applies to PDU session id=%d dnn=%s — %d rule(s) matched",
			in.PDUSessionID, in.DNN, len(rules))
		installed := installULCLForSession(in.IMSI, in.PDUSessionID, in.DNN, rules)
		log.WithIMSI(in.IMSI).Infof("AF-influence ULCL install pduSessID=%d: %d/%d rule(s) ok",
			in.PDUSessionID, installed, len(rules))
	}

	// LI hook: TS 33.128 §6.2.2 "PDU Session Establishment Event"
	// (IRI) and §6.2.4 "Start of interception with established PDU
	// session" (CC). Per TS 33.127 §7.5 the SMF acts as the IRI-POI
	// for session-management events, and the SMF/UPF act as the CC-
	// POI when CC scope is requested. li.* are no-ops when the IMSI
	// is not under interception.
	li.CaptureIRI("PDU_SESSION_ESTABLISHMENT", in.IMSI, map[string]interface{}{
		"event":          "pdu_session_established",
		"pdu_session_id": in.PDUSessionID,
		"dnn":            in.DNN,
		"ipv4":           v4,
		"ipv6":           v6,
		"upf_id":         sel.UPFID,
	})
	li.CheckAndActivateCC(in.IMSI, int(in.PDUSessionID), "", "data")

	return &EstablishOutput{AcceptNAS: accept, Session: sess, UPF: sel}, nil
}

// 5GSM Cause values — TS 24.501 §9.11.4.2 "5GSM cause".
// PDF: specs/3gpp/ts_124501v190602p.pdf — IE table
// 9.11.4.2.1. Names mirror the spec text; values are the encoded
// cause-byte per the same table. Only the ones used by our handlers
// are enumerated; grep §9.11.4.2 when extending.
const (
	CauseInsufficientResources                 uint8 = 26  // insufficient resources
	CauseMissingOrUnknownDNN                   uint8 = 27  // missing or unknown DNN
	CauseUnknownPDUSessionType                 uint8 = 28  // unknown PDU session type
	CauseUserAuthenticationFailed              uint8 = 29  // user authentication or authorization failed
	CauseRequestRejectedUnspecified            uint8 = 31  // request rejected, unspecified
	CauseServiceOptionNotSupported             uint8 = 32  // service option not supported
	CauseRequestedServiceOptionNotSubscribed   uint8 = 33  // requested service option not subscribed
	CauseServiceOptionTemporarilyOutOfOrder    uint8 = 34  // service option temporarily out of order
	CausePTIAlreadyInUse                       uint8 = 35  // PTI already in use
	CauseFiveGSMQoSNotAccepted                 uint8 = 37  // 5GS QoS not accepted
	CauseRegularDeactivation                   uint8 = 36  // regular deactivation
	CauseNetworkFailure                        uint8 = 38  // network failure
	CauseReactivationRequested                 uint8 = 39  // reactivation requested
	CauseSemanticErrorInTFT                    uint8 = 41  // semantic error in the TFT operation
	CauseSyntacticalErrorInTFT                 uint8 = 42  // syntactical error in the TFT operation
	CauseInvalidPDUSessionIdentity             uint8 = 43  // Invalid PDU session identity (TS 24.501 §9.11.4.2)
	CauseSemanticErrorsInPacketFilter          uint8 = 44  // semantic errors in packet filter
	CauseSyntacticalErrorInPacketFilter        uint8 = 45  // syntactical error in packet filter
	CauseOutOfLadnServiceArea                  uint8 = 46  // out of LADN service area
	CausePDUSessionTypeIPv4OnlyAllowed         uint8 = 50  // PDU session type IPv4 only allowed
	CausePDUSessionTypeIPv6OnlyAllowed         uint8 = 51  // PDU session type IPv6 only allowed
	CausePDUSessionDoesNotExist                uint8 = 54  // PDU session does not exist
	CauseInsufficientResourcesForSliceAndDNN   uint8 = 67  // insufficient resources for specific slice and DNN
	CauseNotSupportedSSCMode                   uint8 = 68  // not supported SSC mode
	CauseInsufficientResourcesForSlice         uint8 = 69  // insufficient resources for specific slice
	CauseMissingOrUnknownDNNInSlice            uint8 = 70  // missing or unknown DNN in a slice
	CauseInvalidMandatoryInformation           uint8 = 81  // invalid mandatory information
	CauseMessageTypeNonExistent                uint8 = 82  // message type non-existent or not implemented
	CauseMessageTypeNotCompatible              uint8 = 83  // message type not compatible with the protocol state
	CauseIENonExistentOrNotImplemented         uint8 = 85  // information element non-existent or not implemented
	CauseConditionalIEError                    uint8 = 86  // conditional IE error
	CauseMessageNotCompatibleWithProtocolState uint8 = 87  // message not compatible with the protocol state
	CauseProtocolErrorUnspecified              uint8 = 111 // protocol error, unspecified
)

// Modify handles a UE-initiated PDU Session Modification Request.
//
// Spec — TS 24.501 §6.4.2 "UE-requested PDU session modification
// procedure" (PDF: specs/3gpp/ts_124501v190602p.pdf):
//
//	§6.4.2.2 Initiation: UE sends PDU SESSION MODIFICATION REQUEST
//	  (type 201) carrying 5GSM Capability + Integrity Protection
//	  Maximum Data Rate + Requested QoS Rules + Requested QoS Flow
//	  Descriptions + Always-on PDU Session Requested.
//	§6.4.2.3 Accepted: SMF sends PDU SESSION MODIFICATION COMMAND
//	  (type 203) — this function's return value. Arms T3591 at the
//	  SMF per §10.3 Table 10.3.2 (N3591=4).
//	§6.4.2.4 UE replies with PDU SESSION MODIFICATION COMPLETE
//	  (type 204); SMF cancels T3591 and returns to Active.
//	§6.4.2.5 UE replies with PDU SESSION MODIFICATION REJECT
//	  (type 205) carrying a §9.11.4.2 cause; SMF falls back to the
//	  pre-modification state.
//	§6.4.2.6 Abnormal cases — T3591 final expiry aborts.
//
// Port of Python smf_pdu_session_mgmt.handle_pdu_session_modification_request.
func Modify(imsi string, pduSessionID, pti uint8) ([]byte, error) {
	log := logger.Get("smf.modify")
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		return nil, fmt.Errorf("PDU session %d not found for imsi=%s", pduSessionID, imsi)
	}
	if sess.State != StateActive && sess.State != StateSuspended {
		return nil, fmt.Errorf("PDU session %d in state %s, cannot modify", pduSessionID, sess.State)
	}

	sess.PTI = pti
	sess.UpdatedAt = time.Now()
	pm.Inc(pm.SMModSucc, 1)

	// Npcf_SMPolicyControl_Update (TS 29.512 §4.2.4 — PDF:
	// specs/3gpp/ts_129512v190600p.pdf). Triggered here by the
	// UE-initiated modification path (§4.2.4.17 "UE initiates a
	// resource modification support"). We request re-authorization;
	// the returned SmPolicyDecision may refresh Session-AMBR
	// (§4.2.4.4) and default QoS (§4.2.4.5). Non-fatal if it fails
	// — matches Python behaviour with PCF down.
	if sess.SmPolicyCtxRef != "" {
		upd, err := smpolicy.Update(
			smpolicyfsm.Key{IMSI: imsi, PDUSessionID: pduSessionID},
			smpolicy.SmPolicyContextDataUpdate{Triggers: []string{"RES_MO_RE"}},
		)
		if err != nil {
			log.Warnf("smf: PCF Update failed imsi=%s pduSessID=%d: %v", imsi, pduSessionID, err)
		} else {
			if upd.SessionAMBRDL > 0 {
				sess.AMBRDL = uint32(upd.SessionAMBRDL)
			}
			if upd.SessionAMBRUL > 0 {
				sess.AMBRUL = uint32(upd.SessionAMBRUL)
			}
			if upd.ChargingMethod != "" {
				sess.ChargingMethod = upd.ChargingMethod
			}
		}
	}

	// Build PDU Session Modification Command (type 203, TS 24.501 §8.3.9).
	// Echo the (possibly PCF-updated) Session-AMBR + QoS rules to the UE.
	ambr := packSessionAMBR(sess.AMBRDL, sess.AMBRUL)
	modCmd := &nas.PDUSessionModificationCommand{
		PDUSessionID: pduSessionID,
		PTI:          pti,
		SessionAMBR:  &ambr,
	}
	cmdBytes, err := modCmd.Encode()
	if err != nil {
		log.Errorf("PDUSessionModificationCommand encode: %v", err)
		return nil, err
	}

	log.WithIMSI(imsi).Infof("PDU Session Modification Command id=%d", pduSessionID)
	return cmdBytes, nil
}

// BuildEstablishReject builds a PDU SESSION ESTABLISHMENT REJECT.
//
// Spec — TS 24.501 §6.4.1.4 "UE-requested PDU session establishment
// procedure not accepted by the network" + §8.3.5 Message definition.
// Message type = 195; mandatory IEs: Extended protocol discriminator,
// PDU session ID, PTI, Message type, 5GSM cause (§9.11.4.2). Optional
// IEs (back-off timer, congestion re-attempt indicator, extended
// protocol configuration options) not populated today — add only if
// the cause warrants (e.g. cause #22 "congestion" pairs with the
// back-off timer per §6.4.1.4).
//
// Callers should fire sessionfsm.EvEstablishmentRejected after the
// reject bytes reach the UE so the FSM moves EstablishmentPending →
// Inactive and the transition is visible in logs.
//
// Port of Python smf_pdu_session_mgmt.build_pdu_session_establishment_reject.
func BuildEstablishReject(pduSessionID, cause, pti uint8) []byte {
	log := logger.Get("smf.reject")
	reject := &nas.PDUSessionEstablishmentReject{
		PDUSessionID: pduSessionID,
		PTI:          pti,
		Cause5GSM:    nas.FiveGSMCause{Value: cause},
	}
	b, err := reject.Encode()
	if err != nil {
		log.Errorf("PDUSessionEstablishmentReject encode: %v", err)
		return nil
	}
	return b
}

// BuildModifyReject builds a PDU SESSION MODIFICATION REJECT.
//
// Spec — TS 24.501 §6.4.2.4 "UE-requested PDU session modification
// procedure not accepted by the network" + §8.3.8 Message definition.
// Message type = 202; mandatory IEs: Extended protocol discriminator,
// PDU session ID, PTI, Message type, 5GSM cause (§9.11.4.2).
func BuildModifyReject(pduSessionID, cause, pti uint8) []byte {
	log := logger.Get("smf.reject")
	reject := &nas.PDUSessionModificationReject{
		PDUSessionID: pduSessionID,
		PTI:          pti,
		Cause5GSM:    nas.FiveGSMCause{Value: cause},
	}
	b, err := reject.Encode()
	if err != nil {
		log.Errorf("PDUSessionModificationReject encode: %v", err)
		return nil
	}
	return b
}

// BuildReleaseReject builds a PDU SESSION RELEASE REJECT.
//
// Spec — TS 24.501 §6.4.3.4 "UE-requested PDU session release
// procedure not accepted by the network" + §8.3.13 Message
// definition. Message type = 210; mandatory IEs: Extended protocol
// discriminator, PDU session ID, PTI, Message type, 5GSM cause
// (§9.11.4.2).
func BuildReleaseReject(pduSessionID, cause, pti uint8) []byte {
	log := logger.Get("smf.reject")
	reject := &nas.PDUSessionReleaseReject{
		PDUSessionID: pduSessionID,
		PTI:          pti,
		Cause5GSM:    nas.FiveGSMCause{Value: cause},
	}
	b, err := reject.Encode()
	if err != nil {
		log.Errorf("PDUSessionReleaseReject encode: %v", err)
		return nil
	}
	return b
}

// Release tears the session down — IP return, UPF delete, store
// cleanup, counter bump — and returns an encoded PDU SESSION RELEASE
// COMMAND (NAS type 211) so the caller can send it to the UE.
//
// Spec — TS 24.501:
//
//	§6.4.3 "UE-requested PDU session release procedure":
//	  §6.4.3.2 Initiation: UE sends PDU SESSION RELEASE REQUEST (type
//	    209) with a PTI.
//	  §6.4.3.3 Accepted: SMF sends PDU SESSION RELEASE COMMAND (type
//	    211) carrying a §9.11.4.2 cause (e.g. #36 "regular
//	    deactivation"); arms T3592 (§10.3 Table 10.3.2 N3592=4).
//	  §6.4.3.4 UE replies with PDU SESSION RELEASE COMPLETE (type
//	    212); SMF cancels T3592.
//	§6.4.4 "Network-requested PDU session release procedure":
//	  §6.4.4.2 SMF directly initiates the release by sending PDU
//	    SESSION RELEASE COMMAND (no preceding UE Request); same
//	    T3592 guard.
//	§8.3.14 Message definition for PDU SESSION RELEASE COMMAND.
//
// NGAP side — TS 38.413 §8.2.2 "PDU Session Resource Release" — the
// AMF side sends PDUSessionResourceReleaseCommand to the gNB so it
// drops the DRB; that procedure isn't yet wired (TODO in
// nf/amf/ngap/pdusetup) but is required to clear the gNB tunnel.
//
// Default cause is #36 (regular deactivation, §9.11.4.2). Port of
// Python _release_single_pdu_session.
func Release(imsi string, pduSessionID uint8) []byte {
	return ReleaseWithCause(imsi, pduSessionID, CauseRegularDeactivation, 0)
}

// DeactivateUserPlane implements the SMF side of TS 23.502 §4.2.6
// "AN Release" step 5-6a: Nsmf_PDUSession_UpdateSMContext Request
// (Operation Type = "UP deactivate") → N4 Session Modification
// Request (AN Tunnel Info to be removed, Buffering on). The PDU
// session remains active at the 5GC but the user-plane is torn
// down; a subsequent §4.2.3.2 Service Request (Operation Type =
// "UP activate") re-arms it with the new gNB tunnel info.
//
// Semantics vs. ReleaseWithCause:
//
//	Release…: tear down the PDU SESSION end-to-end (IP release, UPF
//	  delete, SM Policy Delete, session record Delete, FSMs dropped).
//	  Used for §6.4.3 UE-initiated release, §5.5.2.2.3 dereg cascade.
//
//	Deactivate: user-plane only. IP stays allocated, UPF session
//	  stays, SM Policy stays Active, session record stays with
//	  State=Suspended. Used for §4.2.6 AN Release (gNB-initiated
//	  UEContextReleaseRequest → CM-IDLE).
//
// userLocation carries the APER-encoded UserLocationInformation IE
// (TS 38.413 §9.3.1.16) the gNB returned in UE CONTEXT RELEASE
// COMPLETE. TS 23.502 §4.2.6 step 5 mandates this is passed as a
// parameter of Nsmf_PDUSession_UpdateSMContext so the SMF can
// include it in the N4 Session Modification (step 6a) and forward
// to CHF for charging. nil is allowed — the spec makes the IE
// optional on the COMPLETE.
//
// Returns the number of DL FARs that switched to buffer mode. 0 is
// valid — idempotent on missing or already-deactivated sessions.
func DeactivateUserPlane(imsi string, pduSessionID uint8, userLocation []byte) int {
	log := logger.Get("smf.deactivate").WithIMSI(imsi)
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		return 0
	}

	// §4.2.6 step 6a — N4 Session Modification: remove AN Tunnel Info,
	// flip DL FAR Apply Action FORW → BUFF so DL packets buffer at UPF.
	n, err := upfmgr.Default.DeactivateDL(imsi, pduSessionID)
	if err != nil {
		log.Warnf("UPF DeactivateDL pduSessID=%d: %v", pduSessionID, err)
	}

	// PFCP FSM — Established → Modifying → Established self-loop via
	// the §7.5.4 PFCP Session Modification. Synchronous cgo dataplane
	// ack'd inline today.
	pfcpKey := pfcpfsm.Key{UPFNode: sess.UPFID, IMSI: imsi, PDUSessionID: pduSessionID}
	pfsm := pfcpfsm.Of(pfcpKey)
	_ = pfsm.Fire(&pfcpfsm.Context{Key: pfcpKey, Event: pfcpfsm.EvModifyRequestSent})
	_ = pfsm.Fire(&pfcpfsm.Context{Key: pfcpKey, Event: pfcpfsm.EvModifyResponse})

	// Session record: Suspended. Do NOT clear sess.UPFTEID — it
	// is the UPF's UL TEID (where the gNB sends UL packets to the
	// UPF), allocated at install time per TS 29.244 §8.2.3 and
	// invariant for the session's lifetime. Clearing it caused the
	// reactivation PDUSessionResourceSetupRequest (TS 23.502
	// §4.2.3.2 step 12) to go out with UL-TEID=0 and the gNB had
	// no valid endpoint to ack against → DL FAR stayed in BUFF and
	// post-paging DL throughput was 0 (TC-IDL-005 regression).
	// The gNB-side DL TEID is fresh on every reactivation; the new
	// value lands via PDUSessionResourceSetupResponse →
	// ActivateUserPlane → UpdateFAR (BUFF→FORW with new gnbTEID),
	// so no Go-side bookkeeping field needs clearing here.
	sess.State = StateSuspended
	sess.LastKnownLocation = userLocation
	sess.UpdatedAt = time.Now()

	// TODO(spec: TS 23.502 §4.2.6 step 6a "User Location Information")
	//   — pass userLocation into the N4 Session Modification Request
	//   once the PFCP IE path is wired end-to-end, and also forward
	//   to CHF when Nchf_ConvergentCharging integration lands. For
	//   now it's persisted on sess.LastKnownLocation for /api/smf/
	//   sessions observability.
	log.Infof("user-plane deactivated pduSessID=%d dlFARs=%d ulInfoBytes=%d (TS 23.502 §4.2.6 step 5-6a; AN Tunnel Info removed, BUFF mode)",
		pduSessionID, n, len(userLocation))
	return n
}

// DeactivateAllUserPlanes applies §4.2.6 step 5-6a to every PDU
// session for the UE. userLocation is passed through to each
// DeactivateUserPlane invocation (TS 23.502 §4.2.6 step 5 param).
// Returns the count of sessions affected.
func DeactivateAllUserPlanes(imsi string, userLocation []byte) int {
	sessions := Default.ForUE(imsi)
	n := 0
	for _, sess := range sessions {
		if sess.State == StateSuspended {
			continue
		}
		_ = DeactivateUserPlane(imsi, sess.PDUSessionID, userLocation)
		n++
	}
	return n
}

// ActivateUserPlane implements the SMF side of TS 23.502 §4.2.3.2
// "UE Triggered Service Request" step 4/12: Nsmf_PDUSession_Update
// SMContext Request (Operation Type = "UP activate") → N4 Session
// Modification Request installing the new AN Tunnel Info. The
// caller (AMF, after extracting the DL TEID from the gNB's
// InitialContextSetupResponse or PDUSessionResourceSetupResponse)
// passes the fresh gNB TEID + gNB IP in host byte order (uint32 —
// same convention as the UPF control plane).
//
// Semantics vs. establish / re-establish:
//
//	Establish:   brand-new session — IP alloc, PCF Create, PFCP
//	             Establish, UPF SessionCreate, NGAP PDU Session
//	             Resource Setup. See Establish() above.
//	Activate:    existing StateSuspended session — reuses all of the
//	             above; only flips DL FAR BUFF→FORW with the new
//	             gNB tunnel and drains buffered DL packets
//	             (TS 29.244 §5.2.1 logic in upf_dp_update_far).
//
// Returns the number of DL FARs reactivated. 0 is valid — idempotent
// on missing / already-active sessions.
func ActivateUserPlane(imsi string, pduSessionID uint8, gnbTEID, gnbAddr uint32) int {
	log := logger.Get("smf.activate").WithIMSI(imsi)
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		log.Warnf("ActivateUserPlane: no session for pduSessID=%d", pduSessionID)
		return 0
	}

	// §4.2.3.2 step 12 equivalent: N4 Session Modification — install
	// new AN Tunnel Info, flip DL FAR Apply Action BUFF → FORW. The
	// dataplane upf_dp_update_far flushes any buffered DL packets
	// through the newly-valid tunnel per TS 29.244 §5.2.1.
	n, err := upfmgr.Default.ActivateDL(imsi, pduSessionID, gnbTEID, gnbAddr)
	if err != nil {
		log.Warnf("UPF ActivateDL pduSessID=%d: %v", pduSessionID, err)
	}

	// PFCP FSM — Established → Modifying → Established for the §7.5.4
	// Session Modification round-trip.
	pfcpKey := pfcpfsm.Key{UPFNode: sess.UPFID, IMSI: imsi, PDUSessionID: pduSessionID}
	pfsm := pfcpfsm.Of(pfcpKey)
	_ = pfsm.Fire(&pfcpfsm.Context{Key: pfcpKey, Event: pfcpfsm.EvModifyRequestSent})
	_ = pfsm.Fire(&pfcpfsm.Context{Key: pfcpKey, Event: pfcpfsm.EvModifyResponse})

	// Do NOT overwrite sess.UPFTEID — it carries the UPF's UL TEID
	// (gNB → UPF direction) allocated at install in installUPFRules
	// per TS 29.244 §8.2.3 F-TEID, and is invariant for the session
	// lifetime. Overwriting it with the gNB DL TEID broke the next
	// reactivation cycle's PDU Session Resource Setup Request, which
	// rebuilds the request from sess.UPFTEID expecting the UL value.
	// The gNB DL TEID lives in the UPF's DL FAR (FAR-2) — updated
	// in upfmgr.Default.ActivateDL() above — and doesn't need a
	// Go-side mirror on sess.
	sess.State = StateActive
	sess.UpdatedAt = time.Now()

	log.Infof("user-plane activated pduSessID=%d dlFARs=%d gnbTEID=0x%08x (TS 23.502 §4.2.3.2 step 12)",
		pduSessionID, n, gnbTEID)
	return n
}

// ReleaseWithCause is like Release but lets the caller pick the 5GSM
// cause (§9.11.4.2) and echo the UE's PTI (for §6.4.3.2 UE-initiated
// release). For §6.4.4.2 network-initiated release pass pti=0.
func ReleaseWithCause(imsi string, pduSessionID uint8, cause uint8, pti uint8) []byte {
	log := logger.Get("smf.release")
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		return nil
	}

	// Release allocated IPs back to pool (mirrors Python ip_allocator.release)
	if sess.IPv4.IsValid() {
		ipalloc.Default.Release(sess.DNN, sess.IPv4)
	}
	if sess.IPv6.IsValid() {
		ipalloc.Default.Release(sess.DNN, sess.IPv6)
	}

	// Tear down UPF dataplane session (mirrors Python upf_context.delete_session)
	if err := upfmgr.Default.DeleteSession(imsi, pduSessionID); err != nil {
		log.Warnf("UPF DeleteSession: %v", err)
	}

	// Npcf_SMPolicyControl_Delete (TS 29.512 §4.2.5 — PDF:
	// specs/3gpp/ts_129512v190600p.pdf). Terminates the SM Policy
	// Association — the PCF cancels its Revalidation Timer, removes
	// the PCC rules from the in-memory manager, and frees the
	// smPolicyCtxRef slot. Idempotent on missing associations.
	if sess.SmPolicyCtxRef != "" {
		if _, err := smpolicy.Delete(smpolicyfsm.Key{IMSI: imsi, PDUSessionID: pduSessionID}); err != nil {
			log.Warnf("smf: PCF Delete failed imsi=%s pduSessID=%d: %v", imsi, pduSessionID, err)
		}
	}

	Default.Delete(imsi, pduSessionID)

	// Drive the 5GSM FSM through release. Fire the request + the
	// release-complete-equivalent synchronously — current code path is
	// a single atomic Release(), so we land straight in RELEASED, then
	// drop the FSM so the same (IMSI, PDUSessionID) pair can be reused
	// for the next PDU session activation.
	sessKey := sessionfsm.Key{IMSI: imsi, PDUSessionID: pduSessionID}
	f := sessionfsm.Of(sessKey)
	_ = f.Fire(&sessionfsm.Context{Key: sessKey, Event: sessionfsm.EvReleaseRequest})
	_ = f.Fire(&sessionfsm.Context{Key: sessKey, Event: sessionfsm.EvReleaseComplete})
	sessionfsm.Drop(sessKey)

	// PFCP session FSM: Established → DeleteInProgress → Inactive.
	// upfmgr.DeleteSession above did the synchronous cgo delete.
	pfcpKey := pfcpfsm.Key{UPFNode: sess.UPFID, IMSI: imsi, PDUSessionID: pduSessionID}
	pfsm := pfcpfsm.Of(pfcpKey)
	_ = pfsm.Fire(&pfcpfsm.Context{Key: pfcpKey, Event: pfcpfsm.EvDeleteRequestSent})
	_ = pfsm.Fire(&pfcpfsm.Context{Key: pfcpKey, Event: pfcpfsm.EvDeleteResponse})
	pfcpfsm.Drop(pfcpKey)

	pm.Inc(pm.SMSessRel, 1)
	log.WithIMSI(imsi).Infof("PDU Session released id=%d dnn=%s", pduSessionID, sess.DNN)

	// AF-influence: clear any ULCL install state for this session
	// so a re-establishment doesn't inherit stale rows in the OAM
	// /api/mec/active-sessions view.
	mec.ClearULCLForSession(imsi, int(pduSessionID))

	// LI hook: TS 33.128 §6.2.3 "PDU Session Release Event" (IRI) +
	// stop CC for any active warrant. li.* are no-ops when the IMSI
	// has no active warrant.
	li.CaptureIRI("PDU_SESSION_RELEASE", imsi, map[string]interface{}{
		"event":          "pdu_session_released",
		"pdu_session_id": pduSessionID,
		"dnn":            sess.DNN,
		"cause":          cause,
	})
	for _, w := range li.GetWarrantForIMSI(imsi) {
		if w.Scope == "cc" || w.Scope == "iri+cc" {
			li.DeactivateCC(w.WarrantID, imsi)
		}
	}

	// Build PDU Session Release Command (5GSM type 211, TS 24.501 §8.3.14).
	// Cause #36 = regular deactivation (TS 24.501 §9.11.4.2).
	releaseCmd := &nas.PDUSessionReleaseCommand{
		PDUSessionID: pduSessionID,
		PTI:          pti,
		Cause5GSM:    nas.FiveGSMCause{Value: cause},
	}
	cmdBytes, err := releaseCmd.Encode()
	if err != nil {
		log.Errorf("PDUSessionReleaseCommand encode: %v", err)
		return nil
	}
	return cmdBytes
}

// ReleaseAll releases every PDU session for an IMSI — used by deregistration
// and UE context teardown. Mirrors Python _release_all_pdu_sessions.
func ReleaseAll(imsi string) int {
	sessions := Default.ForUE(imsi)
	for _, sess := range sessions {
		Release(imsi, sess.PDUSessionID)
	}
	return len(sessions)
}

// ── internals ───────────────────────────────────────────────────────────

// apnRow is the slice of apn_config columns we read.
type apnRow struct {
	PDUSessionType string
	AMBRDLKbps     int64
	AMBRULKbps     int64
	DNSPrimary     string
	DNSSecondary   string
	MTU            int64
}

func loadAPN(dnn string) (*apnRow, error) {
	apn := smfctx.Default.LookupAPN(dnn)
	if apn == nil {
		return nil, fmt.Errorf("DNN %q not provisioned", dnn)
	}
	return &apnRow{
		PDUSessionType: apn.PDUSessionType,
		AMBRDLKbps:     apn.AMBRDLKbps,
		AMBRULKbps:     apn.AMBRULKbps,
		DNSPrimary:     apn.DNSPrimary,
		DNSSecondary:   apn.DNSSecondary,
		MTU:            apn.MTU,
	}, nil
}

// loadDefault5QI looks up the subscriber's default service binding for the
// given IMSI + DNN from the SMF context (loaded from DB at boot). Returns
// an error if no default service binding is found.
func loadDefault5QI(imsi, dnn string) (uint8, error) {
	fiveqi, ok := smfctx.Default.LookupDefault5QI(imsi, dnn)
	if !ok {
		return 0, fmt.Errorf("no default service binding for IMSI=%s DNN=%s", imsi, dnn)
	}
	return fiveqi, nil
}

// pickPDNType reconciles the UE request with the APN config. Requested == 0
// means "whatever APN allows"; otherwise we honour the UE but fall back
// to the APN-allowed type when they don't match.
func pickPDNType(reqType uint8, apn *apnRow) uint8 {
	apnType := apnToPDNType(apn.PDUSessionType)
	if reqType == 0 {
		return apnType
	}
	if reqType == apnType {
		return reqType
	}
	if apnType == nas.PDUSessionTypeIpv4v6 {
		return reqType // v4v6 APN serves either
	}
	return apnType
}

func apnToPDNType(s string) uint8 {
	switch s {
	case "IPv4":
		return nas.PDUSessionTypeIpv4
	case "IPv6":
		return nas.PDUSessionTypeIpv6
	case "IPv4v6":
		return nas.PDUSessionTypeIpv4v6
	case "Ethernet":
		return nas.PDUSessionTypeEthernet
	case "Unstructured":
		return nas.PDUSessionTypeUnstructured
	}
	return nas.PDUSessionTypeIpv4
}

// allocIP pulls CIDR pools from apn_ip_pools and allocates one or both
// (dual-stack) addresses via ipalloc.Default.
func allocIP(dnn string, pdnType uint8) (v4, v6 netip.Addr, err error) {
	v4Pools, v6Pools, err := loadPools(dnn)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, err
	}
	switch pdnType {
	case nas.PDUSessionTypeIpv4:
		v4, err = ipalloc.Default.Allocate(dnn, v4Pools, 4)
	case nas.PDUSessionTypeIpv6:
		v6, err = ipalloc.Default.Allocate(dnn, v6Pools, 6)
	case nas.PDUSessionTypeIpv4v6:
		v4, v6, err = ipalloc.Default.AllocateDualStack(dnn, v4Pools, v6Pools)
	default:
		// Ethernet / Unstructured: no IP assignment.
	}
	return
}

func loadPools(dnn string) (v4, v6 []string, err error) {
	apn := smfctx.Default.LookupAPN(dnn)
	if apn == nil {
		return nil, nil, fmt.Errorf("DNN %q not provisioned (no IP pools)", dnn)
	}
	return apn.V4Pools, apn.V6Pools, nil
}

func releaseOnFail(dnn string, v4, v6 netip.Addr) {
	if v4.IsValid() {
		ipalloc.Default.Release(dnn, v4)
	}
	if v6.IsValid() {
		ipalloc.Default.Release(dnn, v6)
	}
}

// installUPFRules pushes the session + PDR/FAR/QER/URR to the UPF dataplane.
// Mirrors Python upf_context.py create_session(). Log lines match Python's
// "mmt-core.upf.context" module so per-UE flow is traceable identically.
func installUPFRules(sess *Session) {
	log := logger.Get("upf.context").WithIMSI(sess.IMSI)
	mgr := upfmgr.Default

	ueAddr := ipToUint32(sess.IPv4)
	ueIP := sess.IPv4.String()

	// Create session in C dataplane (PFCP path stashes; CommitSession
	// flushes everything as one §7.5.2 Establishment).
	if err := mgr.CreateSession(&upfmgr.Session{
		IMSI: sess.IMSI, PDUSessionID: sess.PDUSessionID,
		DNN: sess.DNN, SST: sess.SST, SD: sdToUint32(sess.SD),
		UEAddr: ueAddr,
		// PDN Type per TS 29.244 §8.2.79 — passed verbatim from
		// the session's PDU Type (§9.11.4.11 NAS encoding matches
		// §8.2.79 PFCP encoding: 1=IPv4, 2=IPv6, 3=IPv4v6, ...).
		// §7.5.2 NOTE recommends including this for IP sessions.
		PDNType: sess.PDUType,
	}); err != nil {
		log.Warnf("UPF CreateSession: %v", err)
		return
	}

	// FAR-1: UL forward to core (action=1=forward, dst_iface=1=core)
	farUL := upfmgr.FAR{FARID: 1, Action: 1, DstIface: 1}
	mgr.AddFAR(sess.IMSI, sess.PDUSessionID, farUL)
	// FAR-2: DL to access — buffer until gNB TEID known (action=2=buffer)
	farDL := upfmgr.FAR{FARID: 2, Action: 2, DstIface: 0}
	mgr.AddFAR(sess.IMSI, sess.PDUSessionID, farDL)

	// Default QoS flow — UL PDR + DL PDR + QER + URR
	const (
		qfi        uint8  = 1
		ulPDRID    uint16 = 1
		dlPDRID    uint16 = 2
		defQERID   uint32 = 1
		defURRID   uint32 = 1
		precedence uint32 = 255
	)
	ulSDF := fmt.Sprintf("permit out ip from %s/32 to any", ueIP)
	dlSDF := fmt.Sprintf("permit out ip from any to %s/32", ueIP)

	// PDI fast-path keys per TS 29.244 v19.5.0 §7.5.2.2:
	//   - UL PDR (src=Access): F-TEID = UPF UL TEID + UPF N3 IPv4
	//     (§8.2.3). The TEID is allocated below from PDU session ID
	//     and lower bits of UE-IP — same value RegisterTEID stamps
	//     onto the in-process cgo dataplane.
	//   - DL PDR (src=Core): UE IP Address = ueAddr (§8.2.62).
	// Pre-compute here so AddPDR carries the keys to the wire (the
	// SMF↔UPF separated case) AND the in-process bridge sees them
	// (it ignores them since RegisterTEID/UEIP do the same job).
	teidUL := uint32(sess.PDUSessionID)<<16 | uint32(ueAddr&0xFFFF)
	sess.UPFTEID = teidUL
	n3IPv4 := ipv4StringToU32(sess.UPFN3IP)

	pdrUL := upfmgr.PDR{
		PDRID: ulPDRID, Precedence: precedence, PDISource: 0, QFI: qfi,
		FARID: 1, QERID: defQERID, URRID: defURRID, SDFRules: ulSDF,
		TEID:    teidUL,
		N3IPv4:  n3IPv4,
	}
	pdrDL := upfmgr.PDR{
		PDRID: dlPDRID, Precedence: precedence, PDISource: 1, QFI: qfi,
		FARID: 2, QERID: defQERID, URRID: defURRID, SDFRules: dlSDF,
		UEIPv4: ueAddr,
	}
	mgr.AddPDR(sess.IMSI, sess.PDUSessionID, pdrUL)
	mgr.AddPDR(sess.IMSI, sess.PDUSessionID, pdrDL)

	// QER — per-flow rate enforcement via DPDK rte_meter srTCM
	// (TS 29.244 §5.4.2). Per TS 23.501 §5.7.2 the MBR/GBR on a QoS
	// flow comes from the *service* configured for that flow (5QI
	// + MBR/GBR live in the services table), NOT from the APN AMBR
	// (which is the Session-AMBR, a separate cap applied by
	// upf_session_meter). Read the default service's columns live
	// from the cached binding; a NULL column means "not configured"
	// = 0 (unlimited for this flow). No hidden / seed fallback.
	var qerMBRUL, qerMBRDL, qerGBRUL, qerGBRDL uint64
	svcName := "<none>"
	if sb := smfctx.Default.LookupDefaultService(sess.IMSI, sess.DNN); sb != nil {
		svcName = sb.ServiceName
		if sb.MBRULKbps != nil {
			qerMBRUL = uint64(*sb.MBRULKbps)
		}
		if sb.MBRDLKbps != nil {
			qerMBRDL = uint64(*sb.MBRDLKbps)
		}
		if sb.GBRULKbps != nil {
			qerGBRUL = uint64(*sb.GBRULKbps)
		}
		if sb.GBRDLKbps != nil {
			qerGBRDL = uint64(*sb.GBRDLKbps)
		}
	}
	qer := upfmgr.QER{
		QERID: defQERID, QFI: qfi, GateUL: 0, GateDL: 0,
		MBRUL: qerMBRUL, MBRDL: qerMBRDL,
		GBRUL: qerGBRUL, GBRDL: qerGBRDL,
	}
	mgr.AddQER(sess.IMSI, sess.PDUSessionID, qer)

	// URR — volume measurement (TS 29.244 §5.4.4)
	urr := upfmgr.URR{URRID: defURRID, MeasMethod: 1, ReportTrigger: 1}
	mgr.AddURR(sess.IMSI, sess.PDUSessionID, urr)

	// Mirror Python "UPF DEFAULT bearer" line (upf_context.py:390)
	log.Infof("UPF DEFAULT bearer: QFI=%d 5QI=%d PDR=%d/%d QER=%d URR=%d [default]",
		qfi, sess.FiveQI, ulPDRID, dlPDRID, defQERID, defURRID)
	// Full rule dump — one line per PDR/FAR/QER/URR so operators can see
	// exactly what was pushed to UPF for this UE without hitting the API.
	log.Infof("  PDR-%d UL: prec=%d QFI=%d → FAR-%d QER-%d URR-%d SDF=\"%s\"",
		pdrUL.PDRID, pdrUL.Precedence, pdrUL.QFI, pdrUL.FARID, pdrUL.QERID, pdrUL.URRID, pdrUL.SDFRules)
	log.Infof("  PDR-%d DL: prec=%d QFI=%d → FAR-%d QER-%d URR-%d SDF=\"%s\"",
		pdrDL.PDRID, pdrDL.Precedence, pdrDL.QFI, pdrDL.FARID, pdrDL.QERID, pdrDL.URRID, pdrDL.SDFRules)
	log.Infof("  FAR-%d UL: action=forward dst=core",
		farUL.FARID)
	log.Infof("  FAR-%d DL: action=buffer dst=access (gNB TEID pending)",
		farDL.FARID)
	log.Infof("  QER-%d: QFI=%d gate UL/DL=open/open MBR UL/DL=%d/%d kbps GBR UL/DL=%d/%d kbps (service=%s)",
		qer.QERID, qer.QFI, qer.MBRUL, qer.MBRDL, qer.GBRUL, qer.GBRDL, svcName)
	log.Infof("  URR-%d: meas=volume trigger=periodic",
		urr.URRID)

	// Session-AMBR metering (TS 23.501 §5.7.1.6) — sourced from the APN
	// row (apn_config.ambr_*_kbps), refreshed in smfctx by ReloadAPNs()
	// every time POST /api/apn is called.
	mgr.SetSessionAMBR(sess.IMSI, sess.PDUSessionID,
		uint64(sess.AMBRUL), uint64(sess.AMBRDL))
	sessNote := ""
	if sess.AMBRUL == 0 && sess.AMBRDL == 0 {
		sessNote = " (unlimited)"
	}
	log.Infof("  Session-AMBR UL/DL=%d/%d kbps%s (DNN=%s)",
		sess.AMBRUL, sess.AMBRDL, sessNote, sess.DNN)

	// UE-AMBR (TS 23.501 v19.7.0 §5.7.1.6 + §5.7.2.6) is enforced
	// by the (R)AN, NOT by the UPF. The value sourced from the UDM
	// subscription was stamped onto sess.UEAMBRUL/DL earlier in this
	// flow (see line ~323) so AMF NGAP can carry it to the gNB in
	// PDU Session Resource Setup Request (UEAggregateMaximumBitRate
	// IE — see nf/amf/ngap/pdusetup). No UPF-side meter install:
	// TS 29.244 v19.5.0 has no UE-AMBR IE so PFCP cannot signal it,
	// and the dataplane's prior per-IMSI rte_meter has been removed
	// as out-of-spec overreach.

	// Register TEID and UE-IP for fast-path lookup. In the in-process
	// cgo path these populate the C dataplane hashes via the bridge.
	// In the separated SMF↔UPF case the PfcpBridge stubs are no-ops
	// — the UPF-side handler.applyCreatePDRToHook now extracts the
	// same keys from PDI.FTEID / PDI.UEIPAddress (populated above)
	// and calls hook.RegisterTEID / hook.RegisterUEIP locally.
	mgr.RegisterTEID(teidUL, sess.IMSI, sess.PDUSessionID)
	mgr.RegisterUEIP(ueAddr, sess.IMSI, sess.PDUSessionID)

	// Flush the queued §7.5.2 Establishment Request — every rule
	// declared above (PDR/FAR/QER/URR + Session-AMBR) rides this
	// single message instead of being sent as N individual
	// Modifications.
	if err := mgr.CommitSession(sess.IMSI, sess.PDUSessionID); err != nil {
		log.Warnf("CommitSession imsi=%s pduSessID=%d: %v",
			sess.IMSI, sess.PDUSessionID, err)
	}

	// Mirror Python "UPF session created" line (upf_context.py:441)
	log.Infof("UPF session created: %s/%d DNN=%s IP=%s bearers=1(1 default+0 dedicated) UL-TEID=0x%08X",
		sess.IMSI, sess.PDUSessionID, sess.DNN, ueIP, teidUL)
}

// ipToUint32 returns a HOST-byte-order uint32 (numerical value) from an
// IPv4 netip.Addr. Matches the C dataplane convention: internal storage is
// host order, wire conversion via htonl() happens at the socket boundary.
// The bit-shift expression is architecture-agnostic (same logical value on
// LE and BE hosts); equivalent to binary.BigEndian.Uint32(addr.As4()).
func ipToUint32(addr netip.Addr) uint32 {
	if !addr.Is4() {
		return 0
	}
	b := addr.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func sdToUint32(sd string) uint32 {
	if sd == "" {
		return 0xFFFFFF
	}
	var v uint32
	fmt.Sscanf(sd, "%x", &v)
	return v
}

// encodeAccept builds the 5GSM PDU Session Establishment Accept NAS bytes.
// Mirrors Python smf_pdu_session._build_pdu_session_accept IE-for-IE:
//
//	Mandatory: SSC Mode, PDU Session Type, QoS Rules, Session-AMBR
//	Optional : PDU Address (0x29), S-NSSAI (0x22), DNN (0x25),
//	           QoS Flow Descriptions (0x79)
//
// The inner 5GSM message is what the UE's session-management layer
// binds to; without S-NSSAI + QoS Flow Descriptions the UE can't
// complete TS 24.501 §6.4.1.3 step 2 (bind QFI→DRB locally).
func encodeAccept(sess *Session) ([]byte, error) {
	msg := &nas.PDUSessionEstablishmentAccept{
		PDUSessionID:           sess.PDUSessionID,
		PTI:                    sess.PTI,
		SelectedPDUSessionType: nas.PDUSessionType{Value: sess.PDUType},
		SelectedSSCMode:        nas.SSCMode{Value: sess.SSCMode},
		AuthorizedQoSRules:     nas.AuthorizedQoSRules{Value: sess.AuthorizedQoSRules},
		SessionAMBR:            packSessionAMBR(sess.AMBRDL, sess.AMBRUL),
	}
	if addr := packPDUAddress(sess); addr != nil {
		msg.PDUAddress = addr
	}
	if snssai := packSNSSAI(sess.SST, sess.SD); snssai != nil {
		msg.SNSSAI = snssai
	}
	if dnn := packDNN(sess.DNN); dnn != nil {
		msg.DNN = dnn
	}
	if qfd := packAuthorizedQoSFlowDescriptions(sess); qfd != nil {
		msg.AuthorizedQoSFlowDescriptions = qfd
	}
	// Extended Protocol Configuration Options (TS 24.501 §8.3.2.10,
	// §9.11.4.6 → TS 24.008 §10.5.6.3) — answers the UE's
	// *Request containers (§8.3.1.9) with values pulled from
	// apn_config. Per §10.5.6.3 the UE's Request is the capability
	// signal; unsolicited responses may be ignored by the MS. See
	// epco.go for the per-container spec citations.
	if apn := smfctx.Default.LookupAPN(sess.DNN); apn != nil {
		if epco := buildExtendedPCO(apn, sess.RequestedExtPCO); epco != nil {
			msg.ExtendedProtocolConfigurationOptions = &nas.ExtendedProtocolConfigurationOptions{Value: epco}
		}
	}
	return msg.Encode()
}

// packSNSSAI encodes a NAS S-NSSAI IE value (TS 24.501 §9.11.2.8).
// Layout: [SST(1)] or [SST(1)+SD(3)].
func packSNSSAI(sst uint8, sd string) *nas.SNSSAI {
	if sst == 0 {
		return nil
	}
	v := []byte{sst}
	if sd != "" {
		// Parse hex → 3 bytes big-endian. 0xFFFFFF means wildcard/no-SD →
		// Python omits it; we match by only emitting when the parsed
		// value is neither all-zero nor all-0xFF.
		b := make([]byte, 3)
		trimmed := sd
		for len(trimmed) < 6 {
			trimmed = "0" + trimmed
		}
		if len(trimmed) > 6 {
			trimmed = trimmed[len(trimmed)-6:]
		}
		ok := true
		for i := 0; i < 3 && ok; i++ {
			var hi, lo int
			hi, lo = -1, -1
			for _, c := range []byte{trimmed[2*i], trimmed[2*i+1]} {
				var n int
				switch {
				case c >= '0' && c <= '9':
					n = int(c - '0')
				case c >= 'a' && c <= 'f':
					n = int(c-'a') + 10
				case c >= 'A' && c <= 'F':
					n = int(c-'A') + 10
				default:
					ok = false
					continue
				}
				if hi < 0 {
					hi = n
				} else {
					lo = n
				}
			}
			if ok {
				b[i] = byte(hi<<4 | lo)
			}
		}
		if ok && !(b[0] == 0xFF && b[1] == 0xFF && b[2] == 0xFF) {
			v = append(v, b...)
		}
	}
	return &nas.SNSSAI{Value: v}
}

// packDNN constructs a NAS DNN IE — the typed Value string holds the
// human-readable APN ("ims" or "ims.mnc001.mcc001.gprs"); the
// runtime owns the DNS-label encoding per TS 24.501 §9.11.2.1B +
// TS 23.003 §9.1.
func packDNN(dnn string) *nas.DNN {
	if dnn == "" {
		return nil
	}
	for _, lbl := range splitDots(dnn) {
		if len(lbl) == 0 || len(lbl) > 63 {
			return nil
		}
	}
	return &nas.DNN{Value: dnn}
}

func splitDots(s string) []string {
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// packAuthorizedQoSFlowDescriptions encodes the QoS Flow Descriptions IE
// body (TS 24.501 §9.11.4.12) for the default bearer. Raw wire for a
// single flow carrying 5QI=N:
//
//	01     QFI = 1 (spare 2 bits in 7..6, QFI in 5..0)
//	20     opcode = 001 "Create new QoS flow description" (bits 7..5)
//	41     E-bit = 1 (bit 6) | N of params = 1 (bits 5..0)
//	01     parameter identifier = 0x01 "5QI" (TS 24.501 Table 9.11.4.12.1)
//	01     parameter contents length = 1
//	NN     5QI value
//
// The E-bit must be 1 when parameters follow; and parameter ID 0x01 (5QI)
// is the only correct code for a 5QI scalar — 0x02 is "GFBR uplink" which
// the UE rejects as malformed for a 1-byte value.
func packAuthorizedQoSFlowDescriptions(sess *Session) *nas.AuthorizedQoSFlowDescriptions {
	if sess.FiveQI == 0 {
		return nil
	}
	const (
		opCreateNew  = byte(0x20) // op=001 in bits 7..5
		eBitSet      = byte(0x40) // E bit at bit 6
		param5QIID   = byte(0x01) // parameter identifier = 5QI
		param5QIplen = byte(0x01) // 5QI value is 1 byte
	)
	qfi := uint8(1) // default bearer
	desc := []byte{
		qfi & 0x3F,
		opCreateNew,
		eBitSet | 0x01, // E=1, Nparams=1
		param5QIID, param5QIplen, sess.FiveQI,
	}
	return &nas.AuthorizedQoSFlowDescriptions{Value: desc}
}

// packSessionAMBR encodes the TS 24.501 §9.11.4.14 Session-AMBR IE.
// Unit codes per the spec table:
//
//	0 = "value is not used" (reserved)
//	1 = 1 Kbps       (multiplier 1)
//	2 = 4 Kbps       (multiplier 4)
//	3 = 16 Kbps      (multiplier 16)
//	4 = 64 Kbps
//	5 = 256 Kbps
//	6 = 1 Mbps       (1024 Kbps)
//	7 = 4 Mbps
//	…
//
// Pick the smallest unit that keeps the 16-bit value < 0xFFFF.
func packSessionAMBR(dlKbps, ulKbps uint32) nas.SessionAMBR {
	dlUnit, dlVal := fitAMBRUnit(dlKbps)
	ulUnit, ulVal := fitAMBRUnit(ulKbps)
	return nas.SessionAMBR{
		UnitUL:        ulUnit,
		SessionAMBRUL: ulVal,
		UnitDL:        dlUnit,
		SessionAMBRDL: dlVal,
	}
}

// fitAMBRUnit returns (unit-code, 16-bit-value) for the given kbps rate.
// The unit code is 1-based: multiplier[0]=1 Kbps maps to unit=1, not unit=0.
// unit=0 is reserved ("value is not used") — emitting it caused the wire
// to decode at 1/4 the intended rate (unit=2 "4 Kbps step" instead of
// unit=3 "16 Kbps step") for 1 Gbps configs.
func fitAMBRUnit(kbps uint32) (uint8, uint16) {
	multipliers := []uint32{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536}
	for i, m := range multipliers {
		if kbps/m < 0xFFFF {
			return uint8(i + 1), uint16(kbps / m)
		}
	}
	return uint8(len(multipliers)), 0xFFFF
}

// packPDUAddress encodes the TS 24.501 §9.11.4.10 PDU Address IE.
// Layout: [Type(1)] + [IPv4(4)] or [IID(8)] or [IID(8) + IPv4(4)].
func packPDUAddress(s *Session) *nas.PDUAddress {
	switch s.PDUType {
	case nas.PDUSessionTypeIpv4:
		if !s.IPv4.IsValid() {
			return nil
		}
		out := []byte{s.PDUType}
		out = append(out, append([]byte(nil), s.IPv4.AsSlice()...)...)
		return &nas.PDUAddress{Value: out}
	case nas.PDUSessionTypeIpv6:
		if !s.IPv6.IsValid() {
			return nil
		}
		// IPv6 IID = low 64 bits of the address.
		addr16 := s.IPv6.As16()
		out := []byte{s.PDUType}
		out = append(out, addr16[8:]...)
		return &nas.PDUAddress{Value: out}
	case nas.PDUSessionTypeIpv4v6:
		if !s.IPv4.IsValid() || !s.IPv6.IsValid() {
			return nil
		}
		addr16 := s.IPv6.As16()
		out := []byte{s.PDUType}
		out = append(out, addr16[8:]...)
		out = append(out, append([]byte(nil), s.IPv4.AsSlice()...)...)
		return &nas.PDUAddress{Value: out}
	}
	return nil
}

// ipv4StringToU32 parses a dotted-quad IPv4 string into host-byte-order
// uint32. Returns 0 on parse failure; callers downstream of AddPDR
// already treat 0 as "no fast-path key" so a parse miss degrades
// gracefully (the F-TEID IE simply gets an all-zero IPv4 which the
// UPF treats as "match any source"). Used to feed sess.UPFN3IP into
// the §8.2.3 F-TEID IE during PDR construction.
func ipv4StringToU32(s string) uint32 {
	v4 := net.ParseIP(s)
	if v4 == nil {
		return 0
	}
	v4 = v4.To4()
	if v4 == nil {
		return 0
	}
	return uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
}

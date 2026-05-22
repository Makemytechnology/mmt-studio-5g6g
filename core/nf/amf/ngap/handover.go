// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NGAP N2 handover orchestration (TS 38.413 §8.4).
// PDF: specs/3gpp/ts_138413v190200p.pdf.
//
// Implements the AMF side of the inter-gNB N2-handover flow captured
// in handover2.pcapng. The AMF is the relay between source and
// target gNB for 9 messages across 4 NGAP procedures:
//
//   §8.4.1  HandoverPreparation       (code 12)
//   §8.4.2  HandoverResourceAllocation (code 13)
//   §8.4.3  HandoverNotification      (code 11)
//   §8.4.6  UplinkRANStatusTransfer   (code 49)  ─┐ relay pair: AMF
//   §8.4.7  DownlinkRANStatusTransfer (code  7)  ─┘ forwards UL → DL
//   §8.4.5  HandoverCancel            (code 10) — failure path
//   §8.3.4  UEContextRelease          (code 41) — post-handover cleanup
//
// Happy-path sequence (from the reference capture, offsets ≈
// 48.83s, 65.67s, 72.97s, 92.64s, 98.15s — all six runs identical):
//
//   1. Source gNB → AMF: HandoverRequired
//        → AMF looks up the target gNB via HandoverRouter.TargetFor
//        → AMF sends HandoverRequest to target
//        → per-UE FSM: Established → HandoverPreparation, TNGRELOC*
//   2. Target gNB → AMF: HandoverRequestAcknowledge
//        → AMF sends HandoverCommand to source
//        → per-UE FSM: HandoverPreparation → HandoverExecution
//   3. Source gNB → AMF: UplinkRANStatusTransfer
//        → AMF forwards as DownlinkRANStatusTransfer to target
//        → FSM stays in HandoverExecution
//   4. Target gNB → AMF: HandoverNotify
//        → AMF sends UEContextReleaseCommand to source
//        → per-UE FSM: HandoverExecution → Established (now at target)
//   5. Source gNB → AMF: UEContextReleaseComplete — cleanup done.
//
// Failure paths:
//   • HandoverFailure from target (§8.4.2) → AMF stays on source side.
//   • HandoverCancel from source (§8.4.5) → AMF tears down target side.
//
// IE-level ASN.1 decoding is NOT done here — this layer is a relay
// that preserves the source's opaque envelope Value in most of the
// outgoing messages (same pattern as the existing NGReset handler).
// A HandoverRouter resolves the target gNB; production builds plug
// in an implementation that parses the `Target ID` IE from the
// Handover Required value.
package ngap

import (
	"fmt"
	"sync"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"

	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

func init() {
	Register(ProcCodeHandoverPreparation, handleHandoverPreparation)
	Register(ProcCodeHandoverResourceAllocation, handleHandoverResourceAllocation)
	Register(ProcCodeHandoverNotification, handleHandoverNotification)
	Register(ProcCodeHandoverCancel, handleHandoverCancel)
	Register(ProcCodeUplinkRANStatusTransfer, handleUplinkRANStatusTransfer)
	Register(ProcCodePathSwitchRequest, handlePathSwitchRequest)               // §8.4.4
	Register(ProcCodeUplinkRANEarlyStatusTransfer, handleUplinkRANEarlyStatus) // §8.4.9
	Register(ProcCodeErrorIndication, handleErrorIndication)
	Register(ProcCodeNGReset, handleNGReset)
}

// HandoverRouter maps an incoming HandoverRequired to the target gNB
// the AMF should relay HandoverRequest to. Parsing the Target ID IE
// is implementation-specific; tests supply fakes that route by
// source-gNB-IP or by stream number.
type HandoverRouter interface {
	// TargetFor returns the target gNB context given the source gNB
	// and the opaque Handover Required value bytes. May return nil
	// to reject the handover.
	TargetFor(source *gnbctx.GnbCtx, value []byte) *gnbctx.GnbCtx
}

// HandoverRouterFunc is a convenience adapter.
type HandoverRouterFunc func(source *gnbctx.GnbCtx, value []byte) *gnbctx.GnbCtx

func (f HandoverRouterFunc) TargetFor(s *gnbctx.GnbCtx, v []byte) *gnbctx.GnbCtx {
	return f(s, v)
}

// HandoverSuccessRequired decides whether the AMF should send a
// §8.4.8 HandoverSuccess to the source gNB after a HandoverNotify
// arrives. Per §8.4.3, HandoverSuccess is required iff
// HandoverNotify carries the "Notify Source NG-RAN Node" IE —
// which (per §8.4.8.1) happens only during DAPS handover.
//
// Default: parse the Notify Value via ParseHandoverNotify and
// inspect the NotifySource flag. Tests override this to force a
// specific outcome without having to build full PDUs.
var HandoverSuccessRequired = func(source, target *gnbctx.GnbCtx, notifyValue []byte) bool {
	parsed, err := ParseHandoverNotify(notifyValue)
	if err != nil {
		return false
	}
	return parsed.NotifySource
}

// PathSwitchAcceptor, if non-nil, decides whether a §8.4.4 PATH
// SWITCH REQUEST should be accepted (returning the inner Value for
// the ACK transparent container) or rejected. Default (nil) accepts
// all path switches with an empty ACK body.
var PathSwitchAcceptor func(source *gnbctx.GnbCtx, requestValue []byte) (ackValue []byte, accept bool, reason string)

// DefaultHandoverRouter is a swappable package-level router. Tests
// override it; production deployments set it at startup.
//
// Default behaviour: parse the HandoverRequired Value's Target ID
// IE via handover_ies.go and pick the matching registered gNB. Falls
// back to "any other connected gNB" only when parsing fails or the
// Target ID doesn't match any known gNB — this keeps ad-hoc two-gNB
// testbeds working while giving real deployments IE-correct routing.
var DefaultHandoverRouter HandoverRouter = HandoverRouterFunc(
	func(source *gnbctx.GnbCtx, value []byte) *gnbctx.GnbCtx {
		if parsed, err := ParseHandoverRequired(value); err == nil && parsed.Target != nil {
			if g := MatchGnbByTargetID(parsed.Target); g != nil && g != source {
				return g
			}
		}
		// Fallback for tests / ad-hoc deployments.
		for _, g := range gnbctx.Default.All() {
			if g != source && g.IsConnected() {
				return g
			}
		}
		return nil
	},
)

// ── Per-UE handover session state ────────────────────────────────
//
// We key by (source-gNB-IP, SCTP stream) — stream 0 is non-UE, so a
// handover session always has stream > 0 and uniquely identifies the
// UE-associated signalling channel at the source gNB.

type handoverSession struct {
	SourceGnb    *gnbctx.GnbCtx
	TargetGnb    *gnbctx.GnbCtx
	SourceStream int
	TargetStream int // assigned when HandoverRequest is sent

	// UE IDs extracted from the HandoverRequired on session create;
	// used in Cause-bearing failure responses so the source can
	// correlate them to the right UE context.
	AMFUEID int64
	RANUEID int64
}

type hoSessionKey struct {
	GnbIP  string
	Stream int
}

var (
	hoMu       sync.RWMutex
	hoSessions = map[hoSessionKey]*handoverSession{}
)

// rememberSession / forgetSession / lookupSession wrap the map with
// a mutex; callers should never touch the map directly.
func rememberSession(k hoSessionKey, s *handoverSession) {
	hoMu.Lock()
	hoSessions[k] = s
	hoMu.Unlock()
}

func lookupSession(k hoSessionKey) *handoverSession {
	hoMu.RLock()
	defer hoMu.RUnlock()
	return hoSessions[k]
}

func forgetSession(k hoSessionKey) {
	hoMu.Lock()
	delete(hoSessions, k)
	hoMu.Unlock()
}

// lookupSessionByTarget finds the session whose TargetGnb matches —
// used when HandoverRequestAcknowledge / HandoverNotify arrive at
// the AMF from the target gNB.
func lookupSessionByTarget(target *gnbctx.GnbCtx, stream int) *handoverSession {
	hoMu.RLock()
	defer hoMu.RUnlock()
	for _, s := range hoSessions {
		if s.TargetGnb == target && s.TargetStream == stream {
			return s
		}
	}
	// Stream may not match on Ack (target picks its own stream for
	// UE-associated signalling). Fall back to target-gNB match if the
	// stream guard doesn't hit.
	for _, s := range hoSessions {
		if s.TargetGnb == target {
			return s
		}
	}
	return nil
}

// ── Handlers ─────────────────────────────────────────────────────

// §8.4.1 HandoverPreparation — Initiating: HandoverRequired from
// source gNB. AMF finds the target, relays as HandoverRequest.
// Successful outcome of this procedure is HandoverCommand — sent
// from inside handleHandoverResourceAllocation below when we hear
// the target's Ack.
func handleHandoverPreparation(source *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.handover")

	if env.Type != wire.InitiatingMessage {
		// HandoverCommand (the successful outcome of this procedure)
		// would only be seen inbound in the unlikely case of a looped
		// test or a misbehaving gNB — log and move on.
		log.Warnf("unexpected HandoverPreparation %s from gNB %s", env.Type, source.GnbIP)
		return
	}

	log.Infof("HandoverRequired from source gNB %s stream=%d (%d bytes)",
		source.GnbIP, stream, len(env.Value))

	// Parse the IEs up front so we have the UE IDs for any failure
	// response we emit. Parse errors fall through to the unspecified-
	// protocol Cause path.
	var amfUEID, ranUEID int64
	parsedReq, _ := ParseHandoverRequired(env.Value)
	if parsedReq != nil {
		amfUEID = parsedReq.AMFUEID
		ranUEID = parsedReq.RANUEID
	}

	// Route: who is the target?
	target := DefaultHandoverRouter.TargetFor(source, env.Value)
	if target == nil {
		log.Warnf("HandoverRequired from %s: no target gNB resolved — preparation aborted", source.GnbIP)
		// §8.4.1 Abnormal: AMF returns HandoverPreparationFailure
		// with a radioNetwork/unknown-target-ID Cause.
		sendHandoverPreparationFailure(source, stream, amfUEID, ranUEID, BuildCauseRadioNetworkUnknownTargetID())
		return
	}

	// §8.4.2 Outgoing: HANDOVER REQUEST to target. The HANDOVER
	// REQUIRED bytes cannot be relayed verbatim — the two messages
	// share Source-to-Target Transparent Container (id 101) but differ
	// on the PDU session list (HANDOVER REQUEST uses
	// id-PDUSessionResourceSetupListHOReq=73 with
	// HandoverRequestTransfer items per §9.3.4.1; HANDOVER REQUIRED
	// uses id-PDUSessionResourceListHORqd=61 with
	// HandoverRequiredTransfer items). Build a proper §9.2.3.4 PDU
	// from the live SMF session view.
	ue := uectx.Default.LookupByAmfID(amfUEID)
	if ue == nil {
		log.Warnf("HandoverRequired from %s: amfUeID=%d has no UE context — aborting prep", source.GnbIP, amfUEID)
		sendHandoverPreparationFailure(source, stream, amfUEID, ranUEID, BuildCauseProtocolUnspecified())
		return
	}
	hoReqValue, err := BuildHandoverRequestValue(ue, parsedReq, env.Value)
	if err != nil {
		log.Warnf("HandoverRequired from %s: build HANDOVER REQUEST failed: %v", source.GnbIP, err)
		sendHandoverPreparationFailure(source, stream, amfUEID, ranUEID, BuildCauseProtocolUnspecified())
		return
	}
	if err := sendEnvelope(target, wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeHandoverResourceAllocation,
		Criticality:   wire.CriticalityReject,
		Value:         hoReqValue,
	}, stream); err != nil {
		log.Warnf("HandoverRequest send to target %s: %v", target.GnbIP, err)
		sendHandoverPreparationFailure(source, stream, amfUEID, ranUEID, BuildCauseProtocolUnspecified())
		return
	}

	rememberSession(hoSessionKey{source.GnbIP, stream}, &handoverSession{
		SourceGnb:    source,
		TargetGnb:    target,
		SourceStream: stream,
		TargetStream: stream, // best-effort — may be updated on Ack
		AMFUEID:      amfUEID,
		RANUEID:      ranUEID,
	})
	log.Infof("N2-HO prep: source=%s → target=%s stream=%d", source.GnbIP, target.GnbIP, stream)
}

// §8.4.2 HandoverResourceAllocation — Successful Outcome:
// HandoverRequestAcknowledge from target. AMF sends HandoverCommand
// back to source as the successful outcome of §8.4.1.
// Unsuccessful Outcome: HandoverFailure — AMF aborts.
func handleHandoverResourceAllocation(target *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.handover")

	session := lookupSessionByTarget(target, stream)
	if session == nil {
		log.Warnf("HandoverResourceAllocation %s from %s stream=%d: no matching session",
			env.Type, target.GnbIP, stream)
		return
	}

	switch env.Type {
	case wire.SuccessfulOutcome:
		log.Infof("HandoverRequestAcknowledge from target %s — sending HandoverCommand to source %s",
			target.GnbIP, session.SourceGnb.GnbIP)
		// §8.4.1 successful outcome: HandoverCommand to source.
		if err := sendEnvelope(session.SourceGnb, wire.Envelope{
			Type:          wire.SuccessfulOutcome,
			ProcedureCode: ProcCodeHandoverPreparation,
			Criticality:   wire.CriticalityReject,
			Value:         env.Value, // relay Target-to-Source Transparent Container
		}, session.SourceStream); err != nil {
			log.Warnf("HandoverCommand send to source %s: %v", session.SourceGnb.GnbIP, err)
			forgetSession(hoSessionKey{session.SourceGnb.GnbIP, session.SourceStream})
			return
		}
		// Update target stream in case Ack arrived on a different one.
		session.TargetStream = stream

	case wire.UnsuccessfulOutcome:
		// §8.4.2 HandoverFailure — target couldn't allocate. Forward
		// the target's Cause to the source via HandoverPreparationFailure
		// when decodable; else fall back to protocol/unspecified.
		log.Warnf("HandoverFailure from target %s — aborting handover for source %s",
			target.GnbIP, session.SourceGnb.GnbIP)
		cause := BuildCauseProtocolUnspecified()
		if parsed, err := parseHandoverFailureCause(env.Value); err == nil && parsed != nil {
			cause = parsed
		}
		sendHandoverPreparationFailure(session.SourceGnb, session.SourceStream,
			session.AMFUEID, session.RANUEID, cause)
		forgetSession(hoSessionKey{session.SourceGnb.GnbIP, session.SourceStream})

	default:
		log.Warnf("HandoverResourceAllocation %s from %s: unexpected type", env.Type, target.GnbIP)
	}
}

// §8.4.6 UplinkRANStatusTransfer — source gNB's PDCP SN state.
// AMF forwards as §8.4.7 DownlinkRANStatusTransfer to target.
func handleUplinkRANStatusTransfer(source *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.handover")

	session := lookupSession(hoSessionKey{source.GnbIP, stream})
	if session == nil {
		log.Warnf("UplinkRANStatusTransfer from %s stream=%d: no handover session",
			source.GnbIP, stream)
		return
	}

	if err := sendEnvelope(session.TargetGnb, wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeDownlinkRANStatusTransfer,
		Criticality:   wire.CriticalityIgnore, // §9.3 table: Downlink RAN Status Transfer criticality = ignore
		Value:         env.Value,
	}, session.TargetStream); err != nil {
		log.Warnf("DownlinkRANStatusTransfer send to target %s: %v",
			session.TargetGnb.GnbIP, err)
		return
	}
	log.Infof("RAN status transfer: %s → %s (via AMF, %d bytes)",
		source.GnbIP, session.TargetGnb.GnbIP, len(env.Value))
}

// §8.4.3 HandoverNotification — target confirms UE arrived. AMF
// fires UEContextReleaseCommand at the source gNB.
func handleHandoverNotification(target *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.handover")

	session := lookupSessionByTarget(target, stream)
	if session == nil {
		log.Warnf("HandoverNotify from %s stream=%d: no session", target.GnbIP, stream)
		return
	}

	if env.Type != wire.InitiatingMessage {
		log.Warnf("HandoverNotify from %s: unexpected type %s", target.GnbIP, env.Type)
		return
	}

	log.Infof("HandoverNotify from target %s — releasing source %s UE context",
		target.GnbIP, session.SourceGnb.GnbIP)

	// §8.4.8 Handover Success — DAPS only: if the HandoverNotify
	// included the "Notify Source NG-RAN Node" IE, AMF tells source
	// the UE reached the target. Gated by HandoverSuccessRequired
	// predicate because the IE-level detection needs a full NGAP
	// decoder.
	if HandoverSuccessRequired != nil && HandoverSuccessRequired(session.SourceGnb, target, env.Value) {
		if err := sendEnvelope(session.SourceGnb, wire.Envelope{
			Type:          wire.InitiatingMessage,
			ProcedureCode: ProcCodeHandoverSuccess,
			Criticality:   wire.CriticalityIgnore,
			Value:         []byte{0x00, 0x00},
		}, session.SourceStream); err != nil {
			log.Warnf("HandoverSuccess send to source %s: %v", session.SourceGnb.GnbIP, err)
		} else {
			log.Infof("HandoverSuccess (§8.4.8) sent to source %s", session.SourceGnb.GnbIP)
		}
	}

	// §8.3.4 UEContextReleaseCommand to source. Value is empty here —
	// a production build adds the AMF-UE-NGAP-ID / Cause IEs via the
	// UEContextRelease handler in uectxrelease/.
	if err := sendEnvelope(session.SourceGnb, wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeUEContextRelease,
		Criticality:   wire.CriticalityReject,
		Value:         []byte{0x00, 0x00},
	}, session.SourceStream); err != nil {
		log.Warnf("UEContextReleaseCommand send to source %s: %v",
			session.SourceGnb.GnbIP, err)
	}

	forgetSession(hoSessionKey{session.SourceGnb.GnbIP, session.SourceStream})
}

// §8.4.5 HandoverCancel — source gives up during preparation or
// execution. AMF aborts the handover and notifies the target.
func handleHandoverCancel(source *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.handover")

	session := lookupSession(hoSessionKey{source.GnbIP, stream})
	if session == nil {
		log.Warnf("HandoverCancel from %s stream=%d: no session", source.GnbIP, stream)
		return
	}

	log.Infof("HandoverCancel from source %s — aborting target %s allocation",
		source.GnbIP, session.TargetGnb.GnbIP)

	// Per §8.4.5 the AMF sends HandoverCancelAcknowledge back to source,
	// carrying the UE IDs from the original HandoverRequired.
	ackVal, err := BuildHandoverCancelAcknowledgeValue(session.AMFUEID, session.RANUEID)
	if err != nil {
		log.Warnf("HandoverCancelAck build: %v", err)
		ackVal = []byte{0x00, 0x00}
	}
	if err := sendEnvelope(source, wire.Envelope{
		Type:          wire.SuccessfulOutcome,
		ProcedureCode: ProcCodeHandoverCancel,
		Criticality:   wire.CriticalityReject,
		Value:         ackVal,
	}, stream); err != nil {
		log.Warnf("HandoverCancelAck send to %s: %v", source.GnbIP, err)
	}

	// Release target's allocated context.
	if err := sendEnvelope(session.TargetGnb, wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeUEContextRelease,
		Criticality:   wire.CriticalityReject,
		Value:         []byte{0x00, 0x00},
	}, session.TargetStream); err != nil {
		log.Warnf("UEContextReleaseCommand send to target %s: %v", session.TargetGnb.GnbIP, err)
	}

	forgetSession(hoSessionKey{source.GnbIP, stream})
}

// §8.4.4 Path Switch Request — initiating from NG-RAN node (target
// of an Xn-handover). AMF sends either PATH SWITCH REQUEST
// ACKNOWLEDGE (success) or PATH SWITCH REQUEST FAILURE
// (unsuccessful) back on the same UE-associated stream.
func handlePathSwitchRequest(source *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.handover")

	if env.Type != wire.InitiatingMessage {
		log.Warnf("PathSwitchRequest from %s: unexpected type %s — ignored",
			source.GnbIP, env.Type)
		return
	}
	log.Infof("PathSwitchRequest from gNB %s stream=%d (%d bytes)",
		source.GnbIP, stream, len(env.Value))

	var ackValue []byte
	accept := true
	reason := ""
	if PathSwitchAcceptor != nil {
		ackValue, accept, reason = PathSwitchAcceptor(source, env.Value)
	}
	if ackValue == nil {
		ackValue = []byte{0x00, 0x00}
	}

	if !accept {
		log.Warnf("PathSwitchRequest rejected (%s) — sending PathSwitchRequestFailure", reason)
		// Parse the incoming PathSwitchRequest to extract the UE IDs
		// the failure response needs to echo per §9.2.3.9.
		var amfUEID, ranUEID int64
		if parsed, err := parsePathSwitchRequest(env.Value); err == nil && parsed != nil {
			amfUEID, ranUEID = parsed.AMFUEID, parsed.RANUEID
		}
		val, err := BuildPathSwitchRequestFailureValue(amfUEID, ranUEID)
		if err != nil {
			val = []byte{0x00, 0x00}
		}
		_ = sendEnvelope(source, wire.Envelope{
			Type:          wire.UnsuccessfulOutcome,
			ProcedureCode: ProcCodePathSwitchRequest,
			Criticality:   wire.CriticalityReject,
			Value:         val,
		}, stream)
		return
	}
	if err := sendEnvelope(source, wire.Envelope{
		Type:          wire.SuccessfulOutcome,
		ProcedureCode: ProcCodePathSwitchRequest,
		Criticality:   wire.CriticalityReject,
		Value:         ackValue,
	}, stream); err != nil {
		log.Warnf("PathSwitchRequestAcknowledge send to %s: %v", source.GnbIP, err)
		return
	}
	log.Infof("PathSwitchRequestAcknowledge sent to %s", source.GnbIP)
}

// §8.4.9 Uplink RAN Early Status Transfer — same relay pattern as
// §8.4.6: forward to target as §8.4.10 Downlink RAN Early Status
// Transfer. Used in DAPS / conditional handover.
func handleUplinkRANEarlyStatus(source *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.handover")

	session := lookupSession(hoSessionKey{source.GnbIP, stream})
	if session == nil {
		log.Warnf("UplinkRANEarlyStatusTransfer from %s stream=%d: no handover session",
			source.GnbIP, stream)
		return
	}
	if env.Type != wire.InitiatingMessage {
		log.Warnf("UplinkRANEarlyStatusTransfer from %s: unexpected type %s",
			source.GnbIP, env.Type)
		return
	}
	if err := sendEnvelope(session.TargetGnb, wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeDownlinkRANEarlyStatusTransfer,
		Criticality:   wire.CriticalityIgnore,
		Value:         env.Value,
	}, session.TargetStream); err != nil {
		log.Warnf("DownlinkRANEarlyStatusTransfer send to %s: %v",
			session.TargetGnb.GnbIP, err)
		return
	}
	log.Infof("Early RAN status transfer: %s → %s (via AMF, %d bytes)",
		source.GnbIP, session.TargetGnb.GnbIP, len(env.Value))
}

// SendHandoverSuccess lets external code (e.g. a DAPS-aware test or
// a future IE-level handler) emit §8.4.8 HandoverSuccess outside of
// the automatic HandoverNotify trigger. Returns error if the source
// gNB isn't reachable.
func SendHandoverSuccess(source *gnbctx.GnbCtx, stream int) error {
	return sendEnvelope(source, wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeHandoverSuccess,
		Criticality:   wire.CriticalityIgnore,
		Value:         []byte{0x00, 0x00},
	}, stream)
}

// sendHandoverPreparationFailure builds the §8.4.1 unsuccessful
// outcome envelope and sends it to source. Cause defaults to
// protocol/unspecified when nil. The UE IDs come from either the
// session (if one was created) or from the HandoverRequired parse.
func sendHandoverPreparationFailure(source *gnbctx.GnbCtx, stream int, amfUEID, ranUEID int64, cause *genngap.Cause) {
	val, err := BuildHandoverPreparationFailureValue(amfUEID, ranUEID, cause)
	if err != nil {
		// Last-resort fallback — empty value, still a valid unsuccessful
		// outcome at the envelope layer.
		val = []byte{0x00, 0x00}
	}
	_ = sendEnvelope(source, wire.Envelope{
		Type:          wire.UnsuccessfulOutcome,
		ProcedureCode: ProcCodeHandoverPreparation,
		Criticality:   wire.CriticalityReject,
		Value:         val,
	}, stream)
}

// parseHandoverFailureCause pulls the Cause IE out of a received
// HandoverFailure Value, returning nil on any decode issue so the
// caller can fall back to a protocol/unspecified Cause.
func parseHandoverFailureCause(value []byte) (*genngap.Cause, error) {
	var pdu genngap.HandoverFailure
	if err := pdu.UnmarshalAPER(value); err != nil {
		return nil, err
	}
	for _, ie := range pdu.ProtocolIEs {
		if ie.Id == genngap.IdCause && ie.Value.Cause != nil {
			return ie.Value.Cause, nil
		}
	}
	return nil, nil
}

// sendEnvelope encodes and transmits one envelope to the given gNB.
func sendEnvelope(g *gnbctx.GnbCtx, env wire.Envelope, stream int) error {
	b, err := wire.Encode(&env)
	if err != nil {
		return err
	}
	return g.Send(b, stream)
}

// ── Error Indication / NG Reset handlers (moved from the old
// handover.go, unchanged in behaviour) ──

// handleErrorIndication handles Error Indication from gNB (TS 38.413
// §8.7.2). The peer reports an error in an incoming message that
// couldn't be reported by an appropriate failure message.
//
// Per §8.7.2.2:
//   "Upon receipt of the ERROR INDICATION message the AMF may take
//    different actions depending on the included reason."
//
// We decode the Cause IE (if present), log it so the operator can
// correlate with any recent PDU, and fire the NGAP FSM event so the
// per-UE state machine can observe the peer flagging trouble. No
// automatic teardown — spec leaves it to implementation.
//
// TODO(spec: TS 38.413 §8.7.2.2 "Criticality Diagnostics") —
//   when present, log which specific IE/criticality the peer flagged.
//   Helps debug cross-version ASN.1 mismatches.
func handleErrorIndication(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.error")

	var ei genngap.ErrorIndication
	if err := ei.UnmarshalAPER(env.Value); err != nil {
		log.Warnf("NGAP Error Indication from gNB %s: decode failed (%d bytes): %v",
			gnb.GnbIP, len(env.Value), err)
		return
	}

	var amfUeID, ranUeID int64
	var cause *genngap.Cause
	for i := range ei.ProtocolIEs {
		ie := &ei.ProtocolIEs[i]
		switch int64(ie.Id) {
		case int64(genngap.IdAMFUENGAPID):
			if ie.Value.AMFUENGAPID != nil {
				amfUeID = int64(*ie.Value.AMFUENGAPID)
			}
		case int64(genngap.IdRANUENGAPID):
			if ie.Value.RANUENGAPID != nil {
				ranUeID = int64(*ie.Value.RANUENGAPID)
			}
		case int64(genngap.IdCause):
			cause = ie.Value.Cause
		}
	}

	log.Warnf("NGAP Error Indication from gNB %s: amfUeID=%d ranUeID=%d cause=%s stream=%d",
		gnb.GnbIP, amfUeID, ranUeID, formatNGAPCause(cause), stream)

	// Advance per-UE NGAP FSM if the indication was UE-associated. The
	// EvErrorIndication transition self-loops in every state today
	// (see ngap/fsm_transitions.go) — no automatic teardown. That
	// matches the spec's "may take different actions" latitude.
}

// formatNGAPCause renders an NGAP Cause CHOICE to a human-readable
// string. Duplicates initialctxsetup.formatCause — kept local to
// avoid the inter-package dependency.
func formatNGAPCause(c *genngap.Cause) string {
	if c == nil {
		return "(none)"
	}
	switch c.Present {
	case genngap.CausePresentRadioNetwork:
		if c.RadioNetwork != nil {
			return fmt.Sprintf("radioNetwork(%d)", int64(*c.RadioNetwork))
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
	return "unknown"
}

// handleNGReset handles NG Reset from gNB or sends NG Reset to gNB (§8.7.4).
func handleNGReset(gnb *gnbctx.GnbCtx, env *wire.Envelope, stream int) {
	log := logger.Get("amf.ngap.ng_reset")

	if env.Type == wire.InitiatingMessage {
		log.Infof("NG Reset received from gNB %s (%d bytes)", gnb.GnbIP, len(env.Value))
		released := releaseAllUEsOnGnb(gnb)
		log.Infof("NG Reset: released %d UE contexts on gNB %s", released, gnb.GnbIP)
		sendNGResetAck(gnb)
		return
	}

	if env.Type == wire.SuccessfulOutcome {
		log.Infof("NG Reset Acknowledge from gNB %s", gnb.GnbIP)
		return
	}

	log.Warnf("Unexpected NG Reset message type=%s from gNB %s", env.Type, gnb.GnbIP)
}

// releaseAllUEsOnGnb releases all UE contexts associated with a gNB.
func releaseAllUEsOnGnb(gnb *gnbctx.GnbCtx) int {
	log := logger.Get("amf.ngap.ng_reset")
	count := uectx.Default.RemoveAllForGnb(gnb.GnbIP)
	log.Infof("Released %d UE contexts for gNB %s", count, gnb.GnbIP)
	return count
}

// sendNGResetAck sends NGResetAcknowledge to gNB.
func sendNGResetAck(gnb *gnbctx.GnbCtx) {
	log := logger.Get("amf.ngap.ng_reset")
	if err := sendEnvelope(gnb, wire.Envelope{
		Type:          wire.SuccessfulOutcome,
		ProcedureCode: ProcCodeNGReset,
		Criticality:   wire.CriticalityReject,
		Value:         []byte{0x00, 0x00},
	}, 0); err != nil {
		log.Errorf("NGResetAcknowledge send to gNB %s: %v", gnb.GnbIP, err)
		return
	}
	log.Infof("NGResetAcknowledge sent to gNB %s", gnb.GnbIP)
}

// SendNGReset sends an AMF-initiated NG Reset to a gNB (§8.7.4.3).
func SendNGReset(gnb *gnbctx.GnbCtx, fullReset bool) error {
	log := logger.Get("amf.ngap.ng_reset")
	if gnb.Conn() == nil {
		return errNoTransport(gnb.GnbIP)
	}
	if err := sendEnvelope(gnb, wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: ProcCodeNGReset,
		Criticality:   wire.CriticalityReject,
		Value:         []byte{0x00, 0x00},
	}, 0); err != nil {
		return err
	}
	log.Infof("NGReset sent to gNB %s (full=%v)", gnb.GnbIP, fullReset)

	if fullReset {
		releaseAllUEsOnGnb(gnb)
	}
	return nil
}

func errNoTransport(ip string) error {
	return &noTransportErr{ip: ip}
}

type noTransportErr struct{ ip string }

func (e *noTransportErr) Error() string { return "no transport for gNB " + e.ip }

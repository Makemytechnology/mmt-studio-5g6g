// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GMM NAS PDU dispatcher — Go port of nf/amf/gmm/gmm_nas_pdu_handler.py.
//
// `Dispatch(ue, pdu)` is the single entry point from the NGAP layer
// (Initial UE Message, Uplink NAS Transport). It:
//
//  1. Peels off the 5GMM security wrapper if present (TS 24.501 §9.3).
//     For the skeleton we do not yet verify MAC / decrypt — we extract
//     the inner NAS bytes and defer crypto until SecurityModeCommand lands.
//  2. Reads the message-type byte (TS 24.501 §9.7).
//  3. Applies the procedure-collision guard (TS 24.501 §5.1.3.2).
//  4. Looks up a registered handler and calls it.
//
// Procedure handlers register themselves at init time via Register.
// Each handler ports one Python file under nf/amf/gmm/ in follow-up commits;
// the skeleton ships stubs that log + bump a PM counter.
package gmm

import (
	"errors"
	"fmt"
	"sync"

	nas "github.com/mmt/nasgen/generated"
	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/uectxrelease"
	"github.com/mmt/mmt-studio-core/nf/amf/security"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

func init() {
	// TS 24.501 v19.6.2 §5.4.1.3.6 item (a) verbatim — "Lower layer
	// failure: Upon receipt of a lower layer failure indication from
	// the N1 NAS signalling connection before the AUTHENTICATION
	// RESPONSE is received, the network shall … abort any ongoing
	// 5GMM specific procedure." §5.5.1.2.8 / §5.5.1.3.8 / §5.6.1.6
	// carry equivalent abort-on-release rules for Initial Reg,
	// Mobility Reg and Service Request respectively.
	//
	// The NGAP UEContextReleaseRequest handler in
	// nf/amf/ngap/uectxrelease observes the lower-layer failure
	// first; we register this hook so the gmm-side cleanup (cancel
	// T3550/T3560/T3570 via timers.CancelAllForUE, clear
	// RetxNASPDU, drop the per-UE GMM FSM so its state doesn't
	// leak into the next procedure) runs before the NGAP release
	// continues. abortCurrentProcedure already exists for the
	// same-connection abort case (new RR during auth, dereg
	// during SMC, etc., handled by dispatch.checkCollision); this
	// hook reuses it for the lower-layer-failure case too.
	uectxrelease.OnNASLowerLayerFailure = func(ue *uectx.AmfUeCtx) {
		abortCurrentProcedure(ue)

		// Spec-aligned post-abort RM state per TS 24.501 v19.6.2:
		//
		//   §5.5.1.2.8(a) (initial registration, network-side abnormal
		//                  case "Lower layer failure" — verbatim):
		//     "If a lower layer failure occurs before the REGISTRATION
		//      COMPLETE message has been received from the UE and timer
		//      T3550 is running, the AMF shall locally abort the
		//      registration procedure for initial registration, enter
		//      state 5GMM-REGISTERED and shall not resend the
		//      REGISTRATION ACCEPT message."
		//
		//   §5.5.1.3.8(a) (mobility/periodic registration, network-side
		//                  abnormal case "Lower layer failure" —
		//                  verbatim): "If a lower layer failure occurs
		//      before the message REGISTRATION COMPLETE has been
		//      received from the UE and timer T3550 is running, the
		//      AMF shall abort the procedure, enter 5GMM-IDLE mode."
		//
		// Net effect: when the prior RM state was 5GMM-REGISTERED (the
		// UE was already registered before this procedure — mobility
		// reg, ServiceRequest, ConfigurationUpdate, etc.), the FSM
		// must end up in StateRegistered, NOT dropped to
		// StateDeregistered. Dropping erases the AMF's record of a
		// successful registration and would force the next mobility
		// RR to look like a fresh DEREGISTERED → REGISTERED_INITIATED
		// transition — observably wrong against spec.
		//
		// When the prior RM state was 5GMM-DEREGISTERED (initial reg
		// in flight, sub-procedure failed before Reg Accept was sent
		// — e.g. lower-layer failure during AUTHENTICATION /
		// IDENTIFICATION / SMC), the AMF context never finalised; the
		// FSM is correctly dropped so the next Of() rebuilds clean at
		// StateDeregistered.
		if ue.RM == uectx.RMRegistered {
			fsm.Of(ue).ResetTo(fsm.StateRegistered)
		} else {
			fsm.Drop(ue) // next Of() rebuilds at StateDeregistered
		}
	}
}

// Handler handles one 5GMM message type.
//
//	inner    — the PDU with the security wrapper stripped (EPD + SHT=0
//	           + msgType + body) when one was present; otherwise the
//	           original PDU unchanged.
//	outerPDU — the most recent SECURITY-PROTECTED outer wire PDU seen
//	           for this Dispatch invocation, or nil when the top-level
//	           call arrived plain. Needed only by handlers that run
//	           TS 24.501 §4.4 cross-ctx MAC verify via security.Reuse
//	           (registration); all others pass it through as `_`.
type Handler func(ue *uectx.AmfUeCtx, msgType uint8, inner []byte, outerPDU []byte)

var (
	handlersMu sync.RWMutex
	handlers   = map[uint8]Handler{}
)

// Register installs a handler for a 5GMM message type. Calling with the
// same msgType replaces the previous handler (useful for tests).
func Register(msgType uint8, h Handler) {
	handlersMu.Lock()
	defer handlersMu.Unlock()
	handlers[msgType] = h
}

// Dispatch is the top-level NAS PDU entry point called by the NGAP layer.
//
// Returns an error only for completely unparseable input (too short, bad
// extended protocol discriminator). Unknown message types are logged and
// dropped — a production build would also emit a 5GMMStatus per §8.2.24.
func Dispatch(ue *uectx.AmfUeCtx, pdu []byte) error {
	var outer []byte
	if len(pdu) >= 2 && pdu[1] != 0 {
		outer = pdu
	}
	return dispatchWithOuter(ue, pdu, outer)
}

// dispatchWithOuter is the internal recursion target. outer is the most
// recent security-protected wire PDU (SHT != 0); it's preserved across
// the TS 24.501 §4.4.6 NAS-Message-Container re-dispatch so the
// registration handler can run §4.4 cross-ctx MAC verify via
// security.Reuse on the ORIGINAL outer bytes, even though the handler
// itself is driven by the inner (SHT=0 plain) re-dispatched PDU.
func dispatchWithOuter(ue *uectx.AmfUeCtx, pdu, outer []byte) error {
	log := logger.Get("amf.gmm.dispatch")
	if len(pdu) < 2 {
		return errors.New("nas pdu too short")
	}
	if pdu[0] != 0x7E {
		// TS 24.007 §11.2.3.1.1A: Extended Protocol Discriminator for 5GS
		// Mobility Management = 0x7E. Anything else is a 5GSM PDU or junk.
		return errors.New("not a 5GMM PDU (wrong EPD)")
	}

	// TS 24.501 §9.3 + §4.4.3.3 + §4.4.4.3 + §4.4.5 — single-owner
	// unwrap at the AMF. Returns plain inner bytes + metadata; advances
	// ULNasCount only on successful MAC verify per §4.4.3.1 para 6.
	// Handlers receive plaintext and never touch ue.Security.* keys
	// or counts (security/doc.go invariants I1, I2, I5).
	inner, _, err := security.RxNAS(ue, pdu)
	if err != nil {
		return err
	}
	if len(inner) < 3 {
		return errors.New("nas: inner too short after RxNAS")
	}
	msgType := inner[2]

	// TS 24.501 §4.4.6 — Protection of initial NAS signalling messages.
	// When the UE has a valid 5G NAS security context and needs to send
	// non-cleartext IEs, it packs the entire RegistrationRequest /
	// DeregistrationRequest / ServiceRequest into a NAS message container
	// IE (type 0x71, §9.11.3.33). The spec mandates:
	//
	//   "the AMF shall consider the NAS message that is obtained from
	//   the NAS message container IE as the initial NAS message that
	//   triggered the procedure"  (TS 24.501 v19.6.2 §4.4.6)
	//
	// We implement that by re-dispatching the inner as if it were the
	// first message in the chain: the outer never reaches the FSM or
	// handler; only the inner drives state, collision guard, timers,
	// and PM counters. This guarantees one wire message ⇒ one effective
	// NAS event, with the inner IE set visible throughout.
	if expanded := expandNASMessageContainer(ue, msgType, inner, log); expanded != nil {
		log.Debugf("NAS Message Container expanded — re-dispatching inner (TS 24.501 §4.4.6)")
		// Preserve outer so the inner's handler can run §4.4 case-i
		// MAC verify on the original security-protected bytes.
		return dispatchWithOuter(ue, expanded, outer)
	}

	// Procedure collision guard — see TS 24.501 §5.1.3.2.
	if !checkCollision(ue, msgType, log) {
		return nil
	}

	handlersMu.RLock()
	h := handlers[msgType]
	handlersMu.RUnlock()
	if h == nil {
		name := MsgName(msgType)
		if name == "" {
			name = "unknown"
		}
		// TS 24.501 v19.6.2 §7.4 verbatim (line 54221-54226):
		//   "if the network receives a message with message type not
		//    defined for the EPD or not implemented by the receiver,
		//    it shall ignore the message except that it should return
		//    a status message (5GMM STATUS or 5GSM STATUS depending
		//    on the EPD) with cause #97 'message type non-existent or
		//    not implemented'."
		log.WithIMSI(ue.IMSI).Warnf("No handler for 5GMM type=0x%02X (%s) — sending 5GMM STATUS #97 per §7.4",
			msgType, name)
		Send5GMMStatus(ue, Cause5GMMUnknownOrNotImpl)
		return nil
	}

	// Handlers own the FSM. Each handler fires an outcome-specific
	// event (e.g. EvAuthResponseValid vs EvAuthResponseInvalid) at the
	// point it knows the outcome — the dispatch layer has no idea
	// whether RES* matched XRES*, so having it fire a generic
	// EvAuthenticationResponse and relying on Guards to disambiguate
	// proved fragile (see the git-log note on the EvAuthResponseValid
	// split). See fsm_transitions.go for the table and the outcome
	// events each handler fires.
	h(ue, msgType, inner, outer)
	return nil
}

// expandNASMessageContainer implements TS 24.501 §4.4.6: when the outer
// initial NAS message (RegistrationRequest / DeregistrationRequest /
// ServiceRequest) carries a NAS Message Container IE (0x71), return the
// inner NAS message bytes. The inner is a standalone 5GMM PDU starting
// with EPD (0x7E) | SHT (0 = plain) | MsgType | body.
//
// Deciphering: the UE ciphers the container value with its current
// KNASEnc + UL NAS COUNT + NEA algorithm (§4.4.6 case b.1). We use
// ue.Security's keys when present (post-SMC state) or the freshest
// cached KNASEnc we can find via the outer MobileIdentity5GS. For NEA0
// no cipher is applied so the bytes are plaintext and pass through.
//
// Returns nil when the container is absent, malformed, or cannot be
// deciphered — caller falls back to the outer PDU and may see an
// empty non-cleartext IE set.
func expandNASMessageContainer(ue *uectx.AmfUeCtx, msgType uint8, outerInner []byte, log *logger.Logger) []byte {
	// Only applies to the three initial NAS messages per §4.4.6.
	if msgType != MsgRegistrationRequest &&
		msgType != MsgDeregistrationRequestMO &&
		msgType != MsgServiceRequest {
		return nil
	}

	outerMsg, derr := nas.DecodeNASMessage(outerInner)
	if derr != nil {
		return nil
	}
	var containerBytes []byte
	var supiHint string
	switch m := outerMsg.(type) {
	case *nas.RegistrationRequest:
		if m.NASMessageContainer != nil {
			containerBytes = m.NASMessageContainer.Value
		}
		supiHint = mobileIdentityHint(m.MobileIdentity5GS)
	case *nas.ServiceRequest:
		// TS 24.501 v19.6.2 §4.4.6 line 4856-4858 (verbatim):
		//   "When the AMF receives an integrity protected initial NAS
		//    message which includes a NAS message container IE, the AMF
		//    shall decipher the value part of the NAS message container
		//    IE. If the received initial NAS message is a REGISTRATION
		//    REQUEST, DEREGISTRATION REQUEST, or a SERVICE REQUEST
		//    message, the AMF shall consider the NAS message that is
		//    obtained from the NAS message container IE as the initial
		//    NAS message that triggered the procedure."
		// Per §4.4.6 line 4814-4834, the cleartext IEs on the outer SR
		// are EPD/SHT/Spare/MsgType/ngKSI/Service type/5G-S-TMSI; every
		// non-cleartext IE (PDU session status §9.11.3.44, Uplink data
		// status §9.11.3.57, Allowed PDU session status §9.11.3.13)
		// lives only inside the container and is invisible to the
		// handler unless we re-dispatch the inner.
		if m.NASMessageContainer != nil {
			containerBytes = m.NASMessageContainer.Value
		}
		supiHint = mobileIdentityHint(m.STMSI5G)
	// DeregistrationRequestUEOriginating handling to be added when the
	// codec exposes its NASMessageContainer field; §4.4.6 covers it
	// identically, so the same extractor will apply.
	default:
		return nil
	}
	if len(containerBytes) == 0 {
		return nil
	}

	// If the UE context already holds a current NAS security context (we
	// activated it on this same connection earlier) use it directly. This
	// is the rare "repeat RR within same N1 connection" case.
	if ue.Security != nil && ue.Security.EEA != 0 && len(ue.Security.KNASEnc) == 16 {
		dec, err := security.DecipherContainer(ue, ue.Security.ULNasCount, containerBytes)
		if err != nil {
			log.Debugf("NASMessageContainer decipher (ue.Security EEA=%d) failed: %v", ue.Security.EEA, err)
			return nil
		}
		containerBytes = dec
	} else if supiHint != "" {
		// Typical path for a fresh N1 connection: the UE ctx passed here
		// is freshly-allocated with no keys. Look up the cached context
		// for this subscriber (indexed by IMSI/SUPI) and use its KNASEnc.
		if cached := uectx.Default.LookupByIMSI(supiHint); cached != nil &&
			cached.Security != nil && cached.Security.EEA != 0 &&
			len(cached.Security.KNASEnc) == 16 {
			dec, err := security.DecipherContainer(cached, cached.Security.ULNasCount, containerBytes)
			if err != nil {
				log.Debugf("NASMessageContainer decipher (cached EEA=%d) failed: %v", cached.Security.EEA, err)
				return nil
			}
			containerBytes = dec
		}
		// NEA0 or no cached keys: containerBytes is already plaintext.
	}

	// Sanity: inner must look like a 5GMM PDU.
	if len(containerBytes) < 3 || containerBytes[0] != 0x7E {
		return nil
	}
	return containerBytes
}

// mobileIdentityHint returns a best-effort IMSI/SUPI string derived from
// the cleartext mobile identity IE, used as a key into the UE registry
// for cached-context lookup. Returns "" when the identity type doesn't
// carry a directly-usable SUPI (e.g. SUCI with concealed MSIN — AUSF
// de-conceal is required and is too heavy for the dispatch hot path;
// the handler still extracts the container downstream with full keys
// once ResolveSUPI has run).
func mobileIdentityHint(_ interface{}) string {
	// Intentional stub: SUCI de-conceal and 5G-GUTI → IMSI lookup live in
	// ResolveSUPI (registration.go). Wiring those into the dispatch hot
	// path requires decoupling the type; left as a dispatch-time hint
	// sentinel so the logic is visible but unused. Inner-RR plaintext
	// containers (NEA0, the common dev config) succeed without a lookup.
	return ""
}

// checkCollision enforces the "only one pending procedure at a time" rule
// from TS 24.501 §5.1.3.2. Returns true if the dispatcher should continue
// to the handler, false to drop.
//
// When we abort a pending procedure (RR or MO-Dereg received mid-flow),
// we must cancel the armed NAS timers (T3560 / T3550 / T3570 / T3555 /
// T3522) — leaving them armed means the timer-manager will keep
// retransmitting ue.RetxNASPDU (now stale) in parallel with the new
// procedure. Noticed during the NGAP audit: prior behaviour reset
// GMMProc but left T3550/T3560 ticking.
func checkCollision(ue *uectx.AmfUeCtx, msgType uint8, log *logger.Logger) bool {
	if _, isResp := ResponseTypes[msgType]; isResp {
		return true
	}
	if ue.GMMProc == uectx.GMMProcNone {
		return true
	}
	switch msgType {
	case MsgRegistrationRequest:
		log.WithIMSI(ue.IMSI).Infof("Registration Request during %s — aborting current procedure",
			ue.GMMProc)
		abortCurrentProcedure(ue)
		return true
	case MsgDeregistrationRequestMO:
		log.WithIMSI(ue.IMSI).Infof("Deregistration during %s — aborting current procedure",
			ue.GMMProc)
		abortCurrentProcedure(ue)
		return true
	case MsgServiceRequest:
		log.WithIMSI(ue.IMSI).Warnf("Service Request rejected — %s in progress", ue.GMMProc)
		return false
	}
	return true
}

// abortCurrentProcedure cancels every NAS-leg timer armed on this UE
// and resets GMMProc/GMMSub so the new procedure starts from a clean
// slate. RetxNASPDU is also wiped so any in-flight timer callback
// that fires before cancel lands ships nothing.
func abortCurrentProcedure(ue *uectx.AmfUeCtx) {
	timers.M.CancelAllForUE(fmt.Sprintf("%d", ue.AmfUeNGAPID))
	ue.GMMProc = uectx.GMMProcNone
	ue.GMMSub = uectx.GMMSubNone
	ue.RetxNASPDU = nil
	// TS 24.501 v19.6.2 §4.4.2.1 + §5.4.2.5 — an aborted procedure
	// must not leave a half-installed non-current context around. The
	// operative (current) ctx stays in use; the pending slot is
	// discarded. (§5.4.2.5: "apply the 5G NAS security context in use
	// before the initiation of the security mode control procedure".)
	security.DiscardPending(ue)
}

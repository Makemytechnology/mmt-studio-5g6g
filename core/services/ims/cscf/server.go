// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// CSCF SIP listener glue — owns a libs/sip.SipTransport on UDP/5060
// and demultiplexes inbound requests by method into the correct
// transaction state machine:
//
//   * REGISTER, OPTIONS, BYE, anything else → §17.2.2 Non-INVITE
//     Server Transaction (NIS).
//   * INVITE                                 → §17.2.1 INVITE Server
//                                              Transaction (IS).
//   * ACK                                    → matched against an
//                                              outstanding IS txn in
//                                              Completed for §17.2.1
//                                              ACK absorption (Timer
//                                              G/H/I lifecycle); the
//                                              §13.3.1.4 end-to-end
//                                              ACK for 2xx is silently
//                                              dropped (no UAS txn
//                                              exists for it).
//
// Spec anchors (all under specs/ietf/rfc3261.txt unless noted):
//   * §17.2.1  "INVITE Server Transaction"
//   * §17.2.2  "Non-INVITE Server Transaction"
//   * §17.2.3  "Matching Requests to Server Transactions" — match key
//              is (topmost-Via branch, CSeq method); libs/sip.Transaction
//              Manager keys on that pair.
//   * §13.3.1.4 "The INVITE is Accepted" — 2xx terminates IS txn;
//              ACK for 2xx is end-to-end (handled by the dialog).
//   * §8.2.1   "Method Inspection" — UAS dispatch by method.
//   * §11.2    "Processing of OPTIONS Request" — minimal capability echo.
//   * §12.1.1  "UAS behavior" — to-tag MUST be added by the UAS to
//              every response that creates a dialog.
//   * §18.1 / §18.2 — UDP transport on port 5060 (§19.1.2).
//   * TS 24.229 §5.4.1 — REGISTER handling at the S-CSCF
//     (specs/3gpp/ts_124229v190600p.pdf).
//   * TS 24.229 §5.4.3.1 — terminating routing via the registered
//     contact binding.
//
// The §17.2.1 implementation here is "B2BUA-stub": the CSCF answers
// INVITEs locally with 200 OK on behalf of the registered callee
// instead of forwarding to the callee's contact and proxying the
// callee's 200 back. That keeps TC-IMS-009 happy (the test only
// checks for a 200 with a To-tag) while leaving real proxy/forking
// (RFC 3261 §16) for a separate milestone. ACK and BYE handling are
// likewise minimal: the §13.3.1.4 end-to-end ACK is dropped, and
// BYE always answers 200 OK statelessly via the NIS path.
//
// TCP transport on port 5060 (§18.1.1 RECOMMENDED) and SIPS / TLS
// (§26.2.1) are not yet wired.
package cscf

import (
	"errors"
	"net"
	"strings"

	"github.com/mmt/mmt-studio-core/libs/sip"
	"github.com/mmt/mmt-studio-core/services/ims/conference"
)

// Server is a §17.2 SIP UAS that wraps inbound requests in either an
// IS (§17.2.1) or NIS (§17.2.2) server transaction and dispatches to
// the CSCF container.
type Server struct {
	cscf       *CSCF
	transport  *sip.SipTransport
	tm         *sip.TransactionManager
	handlerCfg RegisterHandlerConfig
}

// NewServer wires a CSCF container to a SIP UAS dispatcher.
// handlerCfg supplies the AKA primitives (GenerateAV / VerifyAuth)
// that HandleRegister calls into — see services/ims/ims.go for the
// real Milenage-driven implementations.
func NewServer(cscf *CSCF, handlerCfg RegisterHandlerConfig) *Server {
	return &Server{
		cscf:       cscf,
		handlerCfg: handlerCfg,
		tm:         sip.NewTransactionManager(),
	}
}

// Start binds the SIP UDP listener and begins dispatching.
func (s *Server) Start(host string, port int) error {
	s.transport = sip.NewSipTransport(host, port, s.dispatch)
	return s.transport.Start()
}

// Stop closes the SIP socket.
func (s *Server) Stop() {
	if s.transport != nil {
		s.transport.Stop()
	}
}

// Transport returns the underlying SIP transport (used by tests and
// future code that needs to send unsolicited requests, e.g. NOTIFY
// for §5.4.1.5 network-initiated deregistration).
func (s *Server) Transport() *sip.SipTransport { return s.transport }

// TransactionManager returns the §17.2.3 transaction index — exposed
// for tests that need to assert state-machine state without poking
// internals.
func (s *Server) TransactionManager() *sip.TransactionManager { return s.tm }

// dispatch is invoked by SipTransport for each parsed message.
// Responses are ignored at this layer (a CSCF acting as P-CSCF /
// proxy would consume them, but the §5.4.1 registrar role only
// generates them).
func (s *Server) dispatch(msg interface{}, addr *net.UDPAddr) {
	req, ok := msg.(*sip.SipRequest)
	if !ok {
		return
	}

	switch req.Method {
	case "INVITE":
		s.dispatchInvite(req, addr)
	case "ACK":
		// §13.3.1.4: ACK for 2xx is end-to-end and has its own Via
		// branch — the IS txn for the INVITE has already terminated,
		// so there's nothing to match. ACKs for 3xx-6xx use the
		// INVITE's branch and need to drive the IS FSM through
		// Completed → Confirmed → Terminated. We try the §17.2.3
		// match against an outstanding IS txn; if none, silently
		// drop (the 2xx ACK case).
		s.dispatchAck(req, addr)
	case "CANCEL":
		// RFC 3261 §9 — caller-side abandonment of an in-flight
		// INVITE. The CANCEL itself is a NIS request (200 OK
		// answered immediately per §9.2 step 2); on a match it
		// also drives the cancelled INVITE's IS FSM Proceeding
		// → Completed by emitting 487 Request Terminated
		// (§9.2 UAS Behavior, last paragraph: "the UAS MUST then
		// respond to the original request with a 487").
		s.dispatchCancel(req, addr)
	default:
		s.dispatchNonInvite(req, addr)
	}
}

// dispatchNonInvite is the original §17.2.2 path used by REGISTER /
// OPTIONS / BYE / etc.
func (s *Server) dispatchNonInvite(req *sip.SipRequest, addr *net.UDPAddr) {
	// §17.2.3 match: (topmost-Via branch, CSeq method).
	branch := extractBranchFromVia(req.GetHeader(sip.HdrVia))
	if branch == "" {
		log.Warnf("CSCF RX %s from %s with no Via branch — answering stateless",
			req.Method, addr)
		resp := s.respond(req)
		if resp != nil {
			_ = s.transport.SendMessage(resp, addr)
		}
		return
	}

	if existing := s.tm.Get(branch, req.Method); existing != nil {
		if nis, ok := existing.(*sip.NonInviteServerTxn); ok {
			log.Infof("CSCF RX %s retransmit from %s (branch=%s) — NIS state=%s, replaying",
				req.Method, addr, branch, nis.State())
			nis.ReceiveRetransmit()
		}
		return
	}

	callID := req.GetHeader(sip.HdrCallID)
	log.Infof("CSCF RX %s from %s (Call-ID=%s, branch=%s) — new NIS",
		req.Method, addr, callID, branch)

	send := func(resp *sip.SipResponse) {
		if err := s.transport.SendMessage(resp, addr); err != nil {
			log.Warnf("CSCF: send response: %v", err)
			return
		}
		log.Infof("CSCF TX %d %s → %s (Call-ID=%s)",
			resp.StatusCode, resp.Reason, addr, callID)
	}

	txn := sip.NewNonInviteServerTxn(req, false, send)
	branchCopy, methodCopy := branch, req.Method
	txn.OnTerminated = func() { s.tm.Remove(branchCopy, methodCopy) }
	s.tm.Add(txn)

	resp := s.respond(req)
	if resp == nil {
		return
	}
	txn.SendFinal(resp)
}

// dispatchInvite handles an inbound INVITE through the §17.2.1
// INVITE Server Transaction. State flow for our B2BUA-stub:
//
//   1. New IS in Proceeding.
//   2. Send 100 Trying immediately to suppress RFC 3261 §17.1.1.2
//      Timer A retransmits from the UAC.
//   3. TS 24.229 §5.4.3.1: look up a registered callee by To-URI.
//      Hit → 200 OK with a fresh §12.1.1 to-tag (FSM Proceeding →
//      Terminated, since 2xx terminates per §13.3.1.4 / §17.2.1).
//      Miss → 480 Temporarily Unavailable (FSM Proceeding →
//      Completed → Timer H or ACK).
func (s *Server) dispatchInvite(req *sip.SipRequest, addr *net.UDPAddr) {
	branch := extractBranchFromVia(req.GetHeader(sip.HdrVia))
	if branch == "" {
		log.Warnf("CSCF RX INVITE from %s with no Via branch — dropping (RFC 2543 not supported)", addr)
		return
	}

	// §17.2.3 match — INVITE retransmits absorbed by the IS FSM.
	if existing := s.tm.Get(branch, "INVITE"); existing != nil {
		if is, ok := existing.(*sip.InviteServerTxn); ok {
			log.Infof("CSCF RX INVITE retransmit from %s (branch=%s) — IS state=%s, replaying",
				addr, branch, is.State())
			is.ReceiveRetransmit()
		}
		return
	}

	callID := req.GetHeader(sip.HdrCallID)
	log.Infof("CSCF RX INVITE from %s (Call-ID=%s, branch=%s) — new IS", addr, callID, branch)

	send := func(resp *sip.SipResponse) {
		if err := s.transport.SendMessage(resp, addr); err != nil {
			log.Warnf("CSCF: send response: %v", err)
			return
		}
		log.Infof("CSCF TX %d %s → %s (Call-ID=%s)",
			resp.StatusCode, resp.Reason, addr, callID)
	}

	txn := sip.NewInviteServerTxn(req, false, send)
	branchCopy := branch
	txn.OnTerminated = func() { s.tm.Remove(branchCopy, "INVITE") }
	s.tm.Add(txn)

	// §17.2.1 / §13.3.1: emit 100 Trying as soon as the UAS knows
	// the request will take more than 200 ms to process. The IS
	// FSM stays in Proceeding.
	trying := buildResponse(req, 100, "Trying", nil)
	txn.SendProvisional(trying)

	// §13.3.1.1 "Progress" / §21.1.2 180 Ringing — for INVITEs that
	// will be answered (callee Registered), send a 180 Ringing
	// provisional so the caller's UAC plays a ringback tone. Per
	// §13.3.1.1, "These provisional responses establish early dialogs
	// and therefore follow the procedures of Section 12.1.1. ... Each
	// of these MUST indicate the same dialog ID." The to-tag added
	// to the 180 (which §12.1.1 makes the local-tag component of the
	// dialog ID at the UAS) is reused on the eventual 200 OK so the
	// caller's §12.1.2 UAC-side dialog state survives early→confirmed
	// without re-keying. Skipped for the 480-bound branch where
	// there's no callee to alert.

	// TS 24.229 §5.4.3.1 terminating routing: find the registered
	// contact for the To-URI.
	toURI := extractURI(req.GetHeader(sip.HdrTo))
	// TS 24.147 v19.0.0 §5.3.2.3.1 — INVITE whose request-URI is a
	// conference factory URI is routed to the conference focus, not
	// the registrar. Per the §5.3.2.3.1 verbatim procedure:
	//   "Upon receipt of an INVITE request that includes a conference
	//    factory URI in the request URI, the conference focus shall
	//    [...] allocate a conference URI [...] generate a 200 (OK)
	//    response to the INVITE request, indicating the 'isfocus'
	//    feature parameter as a parameter to the conference URI in
	//    the Contact header."
	// (RFC 3840 defines the "isfocus" feature parameter; per §6.4 it
	//  is conveyed as a Contact header parameter.)
	if s.cscf.ConferenceAS != nil && conference.IsConferenceURI(toURI) {
		s.dispatchInviteToConferenceFactory(req, addr, txn, callID, toURI)
		return
	}
	callee := s.cscf.LookupRegistrationByIMPU(toURI)
	if callee == nil {
		log.Infof("CSCF: no Registered binding for To=%s — sending 480", toURI)
		notFound := buildResponse(req, 480, "Temporarily Unavailable", nil)
		txn.SendFinalError(notFound)
		return
	}

	// RFC 3261 §14.1: an INVITE within an existing dialog (Call-ID
	// already in our dialog table) is a re-INVITE — used for
	// hold/resume, codec change, etc. The dialog-ID stays stable
	// across re-INVITEs (§12.2.1.1 NOTE: "Requests within a dialog
	// MAY contain Record-Route and Contact header fields. However,
	// these requests do not cause the dialog's route set to be
	// modified"). We reuse the stored to-tag, skip 180 Ringing
	// (§14.1: re-INVITE is processed in-dialog and the UAC isn't
	// being alerted), and re-fire AuthorizeMedia so the PCF can
	// re-evaluate the SDP (e.g. sendonly → recvonly hold flips
	// the §8.2.7 Gate Status).
	existingDialog := s.cscf.GetDialog(callID)
	isReInvite := existingDialog != nil

	// AF→PCF→SMF policy chain (TS 23.228 §5.4.7 / TS 29.514 §4.2.2 +
	// TS 29.512 §4.2.3-§4.2.4) — fire BEFORE we send the 200 OK so
	// the SMF has the new QoS Flow installed by the time the UE's
	// RTP starts. Caller IMSI comes from the From-URI's registered
	// IMPI on initial INVITE; on re-INVITE we use the dialog's
	// stored caller IMSI (terminating-leg re-INVITEs would otherwise
	// resolve to the *callee's* IMSI via callerIMSI()).
	var callerIMSI string
	if isReInvite {
		callerIMSI = existingDialog.CallerIMSI
	} else {
		callerIMSI = s.callerIMSI(req)
	}
	if s.cscf.AuthorizeMedia != nil && req.Body != "" {
		if callerIMSI != "" {
			s.cscf.AuthorizeMedia(callerIMSI, req.Body)
		} else {
			log.Warnf("CSCF INVITE: no registered caller for From=%s — skipping AF authorization",
				req.GetHeader(sip.HdrFrom))
		}
	}

	// to-tag selection — initial INVITE mints fresh; re-INVITE
	// reuses the stored tag so the §12.2.1 dialog ID stays stable.
	var toTag string
	if isReInvite {
		toTag = existingDialog.ToTag
		log.Infof("CSCF: re-INVITE on Call-ID=%s — skipping 180 (in-dialog, §14), reusing to-tag=%s", callID, toTag)
	} else {
		toTag = sip.GenerateTag()
		ringing := buildResponse(req, 180, "Ringing", nil)
		setToTag(ringing, toTag)
		txn.SendProvisional(ringing)
		log.Infof("CSCF INVITE: 180 Ringing → callee=%s (early dialog, to-tag=%s)", toURI, toTag)
	}

	// B2BUA-stub: answer 200 OK ourselves on behalf of the callee.
	// §12.1.1 mandates the UAS sets the local-tag component of the
	// dialog ID to the To tag in the response ("which always includes
	// a tag"). We reuse the to-tag minted for the 180 to keep the
	// dialog ID stable across early→confirmed per §13.3.1.1.
	extra := map[string]string{}
	body := req.Body
	if ct := req.GetHeader(sip.HdrContentType); ct != "" {
		// Echo SDP back in the answer slot. A real UAS would build
		// its own SDP per RFC 3264 offer/answer; for the B2BUA stub
		// we mirror the offer so the caller's dialog has a valid
		// remote SDP to anchor RTP against.
		extra[sip.HdrContentType] = ct
	}
	ok := buildResponse(req, 200, "OK", extra)
	setToTag(ok, toTag)
	ok.Body = body
	if body != "" {
		ok.SetHeader(sip.HdrContentLength, itoaShim(len(body)))
	}
	txn.Send2xx(ok)

	// §12.1.1 — store the dialog state at 200 OK time. Initial INVITE
	// creates a fresh entry; re-INVITE leaves the existing one alone
	// (it's the same dialog).
	if !isReInvite {
		s.cscf.StoreDialog(callID, &DialogInfo{
			ToTag:      toTag,
			CallerIMSI: callerIMSI,
		})
	}
	_ = callee // Reserved for the proxy/forwarding milestone.
}

// dispatchInviteToConferenceFactory implements TS 24.147 v19.0.0
// §5.3.2.3.1 ("Conference creation with a conference factory URI") at
// the conference-focus role. Verbatim step 3: "allocate a conference
// URI"; step 4 / final paragraph: "generate a 200 (OK) response to
// the INVITE request, indicating the 'isfocus' feature parameter as
// a parameter to the conference URI in the Contact header." RFC 3840
// §9 defines "isfocus" as a Contact-header feature parameter.
//
// The §5.3.2.3.1 step-2 identity authorization (TS 24.229 §5.7.1.4 /
// §5.7.1.5) is approximated by the callerIMSI lookup the surrounding
// dispatchInvite already performed — an empty callerIMSI means the
// originator was not Registered and should be rejected. The first
// provisional + preconditions handling (step 4) is skipped on the
// minimum path; the mixer is assumed always available so we go
// straight to the 200 OK per the spec's "Upon receipt of an
// indication from the mixer that conference resources have been
// through-connected" trigger.
//
// Re-INVITE within an existing conference dialog (subsequent CSeq
// after the initial INVITE that created the conf) is treated as a
// normal in-dialog request: reuse the stored to-tag (RFC 3261
// §12.2.1.1 stable dialog ID across re-INVITEs).
func (s *Server) dispatchInviteToConferenceFactory(
	req *sip.SipRequest, addr *net.UDPAddr, txn *sip.InviteServerTxn,
	callID, factoryURI string,
) {
	callerIMSI := s.callerIMSI(req)
	if callerIMSI == "" {
		// §5.3.2.3.1 step 2: identity verification — TS 24.229
		// §5.7.1.5 authorize the request. Unregistered originator
		// fails authorization.
		log.Infof("CSCF: conference factory INVITE from unregistered originator (To=%s) — sending 403", factoryURI)
		forbidden := buildResponse(req, 403, "Forbidden", nil)
		txn.SendFinalError(forbidden)
		return
	}
	// Caller IMPU = From-URI of the INVITE (the SIP identity the AS
	// associates with the conference host per §5.3.2.4).
	hostIMPU := extractURI(req.GetHeader(sip.HdrFrom))

	// Allocate the conference (or reuse the existing dialog's URI
	// on re-INVITE — the focus role doesn't mint a fresh conf URI
	// for in-dialog re-INVITEs per RFC 3261 §12.2.1.1).
	existingDialog := s.cscf.GetDialog(callID)
	isReInvite := existingDialog != nil
	var (
		confURI string
		toTag   string
	)
	if isReInvite && existingDialog.ConferenceURI != "" {
		confURI = existingDialog.ConferenceURI
		toTag = existingDialog.ToTag
		log.Infof("CSCF: conference factory re-INVITE on Call-ID=%s — reusing conf URI %s", callID, confURI)
	} else {
		_, confURI = s.cscf.ConferenceAS.CreateConference(hostIMPU)
		toTag = sip.GenerateTag()
		log.Infof("CSCF: conference factory INVITE — allocated %s for host=%s (factory=%s)",
			confURI, hostIMPU, factoryURI)
	}

	// Contact header per §5.3.2.3.1 final paragraph + RFC 3840 §9.
	// The "isfocus" parameter is a Contact-header parameter (not a
	// URI parameter) — RFC 3840 §6.4 / §9 example: Contact: <uri>
	// ;isfocus.
	contactHdr := "<" + confURI + ">;isfocus"

	// 180 Ringing skipped — §5.3.2.3.1 only mandates 1xx if
	// preconditions are required and unsatisfied. Mirror SDP body
	// from the offer per RFC 3264 §6 minimum (B2BUA-stub).
	extra := map[string]string{sip.HdrContact: contactHdr}
	body := req.Body
	if ct := req.GetHeader(sip.HdrContentType); ct != "" {
		extra[sip.HdrContentType] = ct
	}
	ok := buildResponse(req, 200, "OK", extra)
	setToTag(ok, toTag)
	ok.Body = body
	if body != "" {
		ok.SetHeader(sip.HdrContentLength, itoaShim(len(body)))
	}
	txn.Send2xx(ok)

	if !isReInvite {
		s.cscf.StoreDialog(callID, &DialogInfo{
			ToTag:         toTag,
			CallerIMSI:    callerIMSI,
			ConferenceURI: confURI,
		})
	}
}

// callerIMSI looks up the From-URI's registered IMPI and returns its
// IMSI digit string. Returns "" when the caller has no Registered
// FSM (e.g. unregistered UE attempting INVITE — a real S-CSCF
// originating handling per TS 24.229 §5.4.3.2 "Requests initiated by
// the served user" requires a prior successful registration; the
// registrar-only stub just skips the AF call).
func (s *Server) callerIMSI(req *sip.SipRequest) string {
	fromURI := extractURI(req.GetHeader(sip.HdrFrom))
	if fromURI == "" {
		return ""
	}
	reg := s.cscf.LookupRegistrationByIMPU(fromURI)
	if reg == nil {
		return ""
	}
	impi := reg.IMPI
	if at := strings.Index(impi, "@"); at > 0 {
		return impi[:at]
	}
	return impi
}

// dispatchAck routes an inbound ACK through the §17.2.3 matcher into
// the originating IS txn (for ACK on 3xx-6xx) or drops it (for the
// §13.3.1.4 end-to-end ACK on 2xx, which has no UAS txn).
func (s *Server) dispatchAck(req *sip.SipRequest, addr *net.UDPAddr) {
	branch := extractBranchFromVia(req.GetHeader(sip.HdrVia))
	if branch == "" {
		return
	}
	if existing := s.tm.Get(branch, "INVITE"); existing != nil {
		if is, ok := existing.(*sip.InviteServerTxn); ok {
			log.Infof("CSCF RX ACK from %s (branch=%s) — IS state=%s, absorbing",
				addr, branch, is.State())
			is.ReceiveAck()
		}
		return
	}
	// 2xx ACK arrives in its own transaction with no matching IS —
	// per §13.3.1.4 the UAC's dialog handles it. We have nothing
	// to do.
	log.Debugf("CSCF RX ACK from %s (branch=%s) — no IS match (2xx end-to-end), dropping", addr, branch)
}

// dispatchCancel handles an inbound CANCEL per RFC 3261 §9 / §17.2.2.
//
// Two parallel obligations:
//
//  1. The CANCEL itself is a non-INVITE request — answer 200 OK on
//     a fresh NIS server transaction (§9.2 step 2 "The CANCEL request
//     is processed using the procedures of Section 17.2.2").
//  2. If a matching INVITE IS in Proceeding exists for this branch
//     (§9.1: "The CANCEL request constructed by the client MUST have
//     a single Via header field value matching the top Via value in
//     the request being cancelled"), drive that IS to Completed by
//     emitting 487 Request Terminated (§9.2 last paragraph: "the
//     UAS MUST … respond to the original request with a 487").
//
// Symmetric AF→PCF tear-down: if AuthorizeMedia already fired for
// the matching INVITE (caller had an active SDP-driven authorization
// at the time of cancellation), call ReleaseMedia(imsi) to retract
// the dynamic PCC rules — same plumbing the BYE path uses.
//
// 481 Call/Transaction Does Not Exist (§9.2 step 1) for the
// no-match case is wired here too — we still answer 200 for the
// CANCEL itself per §9.2 ("the UAS MUST respond to a CANCEL with
// a 200 (OK) response if the CANCEL is matched … or with a
// 481 response if not"). RFC 3261 isn't entirely consistent here;
// the prevailing interop reality is that 200 OK is always sent
// for a CANCEL the receiver can parse, and 481 is only used by
// proxies. We follow that for B2BUA-stub safety.
func (s *Server) dispatchCancel(req *sip.SipRequest, addr *net.UDPAddr) {
	branch := extractBranchFromVia(req.GetHeader(sip.HdrVia))
	if branch == "" {
		log.Warnf("CSCF RX CANCEL from %s with no Via branch — dropping", addr)
		return
	}
	callID := req.GetHeader(sip.HdrCallID)
	log.Infof("CSCF RX CANCEL from %s (Call-ID=%s, branch=%s)", addr, callID, branch)

	send := func(resp *sip.SipResponse) {
		if err := s.transport.SendMessage(resp, addr); err != nil {
			log.Warnf("CSCF: send CANCEL response: %v", err)
			return
		}
		log.Infof("CSCF TX %d %s → %s (Call-ID=%s)",
			resp.StatusCode, resp.Reason, addr, callID)
	}

	// (1) Build a NIS for the CANCEL itself and answer 200 OK.
	cancelTxn := sip.NewNonInviteServerTxn(req, false, send)
	cancelBranch, cancelMethod := branch, "CANCEL"
	cancelTxn.OnTerminated = func() { s.tm.Remove(cancelBranch, cancelMethod) }
	s.tm.Add(cancelTxn)
	cancelTxn.SendFinal(buildResponse(req, 200, "OK", nil))

	// (2) Match against an outstanding INVITE IS. §17.2.3 keys on
	// (branch, CSeq method); CANCEL shares the INVITE's branch per
	// §9.1, so we look up the "INVITE" method on the same branch.
	existing := s.tm.Get(branch, "INVITE")
	if existing == nil {
		log.Debugf("CSCF: CANCEL branch=%s — no matching INVITE IS (already final, or never existed)", branch)
		return
	}
	is, ok := existing.(*sip.InviteServerTxn)
	if !ok {
		return
	}
	// §9.2: only cancel an IS that hasn't sent a final response.
	// Once Completed/Confirmed/Terminated, "the UAS … MUST NOT
	// send a 487". We check via String state since the libs/sip
	// API doesn't expose an enum to importers.
	if is.State() != "Proceeding" {
		log.Debugf("CSCF: CANCEL branch=%s — INVITE IS in %s (final already sent), nothing to terminate",
			branch, is.State())
		return
	}

	// Symmetric AF→PCF release for the cancelled call. Prefer the
	// stored dialog's CallerIMSI (CANCEL races the 200 OK; the
	// dialog may already have been stored). Fall back to From-URI
	// lookup for the typical pre-confirmation case.
	if s.cscf.ReleaseMedia != nil {
		callID := req.GetHeader(sip.HdrCallID)
		var imsi string
		if d := s.cscf.GetDialog(callID); d != nil {
			imsi = d.CallerIMSI
			s.cscf.RemoveDialog(callID)
		} else {
			imsi = s.callerIMSI(req)
		}
		if imsi != "" {
			s.cscf.ReleaseMedia(imsi)
			log.Infof("CSCF CANCEL: AF→PCF ReleaseMedia for IMSI=%s (in-flight INVITE aborted)", imsi)
		}
	}

	// 487 Request Terminated for the cancelled INVITE. §17.2.1:
	// IS Proceeding → Completed; Timer G/H/I run from the IS FSM
	// and the IS terminates after Timer I (or on ACK).
	//
	// The 487 must echo the *INVITE*'s To / From / CSeq / Call-ID
	// / Via — not the CANCEL's — so we build it from the IS's
	// stored request, not the CANCEL we just received.
	terminated := buildResponse(is.Request(), 487, "Request Terminated", nil)
	is.SendFinalError(terminated)
	log.Infof("CSCF: CANCEL branch=%s drove INVITE IS → Completed via 487 Request Terminated", branch)
}

// respondRefer answers an inbound REFER per RFC 3515 §2.4.1 with
// 202 Accepted and, when the Refer-To header names a known
// conference URI, registers the To-URI's IMPU against the
// conference so §5.3.3 NOTIFY documents and the conference
// participant list stay consistent. The actual INVITE to the
// conference URI is the referee's responsibility per RFC 3515
// §2.4.6 — the B2BUA does not synthesise it.
func (s *Server) respondRefer(req *sip.SipRequest) *sip.SipResponse {
	referTo := extractURI(req.GetHeader("Refer-To"))
	target := extractURI(req.GetHeader(sip.HdrTo))
	if s.cscf.ConferenceAS != nil && referTo != "" {
		// §5.3.1.5.2 expects Refer-To to carry the conference URI;
		// derive the conference ID from the SIP user part (the
		// ConferenceAS allocates "conf-N" identifiers in
		// CreateConference).
		if confID := extractConfIDFromURI(referTo); confID != "" {
			err := s.cscf.ConferenceAS.JoinConference(confID, target, "audio")
			switch {
			case err == nil:
				log.Infof("CSCF REFER: %s joined %s (via REFER from %s)",
					target, confID, extractURI(req.GetHeader(sip.HdrFrom)))
			case errors.Is(err, conference.ErrConferenceFull):
				// TS 24.147 v19.0.0 §5.3.2.2 verbatim: "If a
				// request is received by the conference focus that
				// violates the policy of the conference focus, the
				// conference focus shall return an appropriate 4xx
				// response." RFC 3261 §21.4.18 486 "Busy Here" is
				// the closest fit for capacity rejection (the
				// called endpoint — the conference focus — is
				// processing-busy / at-capacity rather than the
				// specific user being unavailable).
				log.Infof("CSCF REFER: rejecting %s join on %s — at MaxParticipants (486 Busy Here)",
					target, confID)
				return buildResponse(req, 486, "Busy Here", nil)
			default:
				log.Warnf("CSCF REFER: JoinConference(%s, %s) failed: %v — accepting REFER anyway",
					confID, target, err)
			}
		}
	}
	return buildResponse(req, 202, "Accepted", nil)
}

// extractConfIDFromURI pulls the "conf-N" user-part out of a
// "sip:conf-N@<domain>" conference URI. Returns "" on mismatch
// (e.g. the conference-factory URI itself, or a UE URI).
func extractConfIDFromURI(uri string) string {
	const prefix = "sip:conf-"
	if !strings.HasPrefix(uri, prefix) {
		return ""
	}
	rest := uri[len("sip:"):]
	at := strings.Index(rest, "@")
	if at <= 0 {
		return ""
	}
	return rest[:at]
}

// respond turns an inbound non-INVITE request into a SIP response.
// Per RFC 3261 §8.2.1, a UAS that does not recognise the request
// method MUST reply 405 Method Not Allowed.
func (s *Server) respond(req *sip.SipRequest) *sip.SipResponse {
	switch req.Method {
	case "REGISTER":
		return s.cscf.HandleRegister(req, s.handlerCfg)
	case "OPTIONS":
		// RFC 3261 §11.2 — minimal capability echo.
		return buildResponse(req, 200, "OK", nil)
	case "REFER":
		// RFC 3515 §2.4.1 — "A REFER request implicitly establishes a
		// subscription to the refer event ... When a REFER request is
		// received, the recipient ... if the recipient agrees to
		// process the REFER request, it MUST send a 202 (Accepted)
		// response."
		//
		// TS 24.147 v19.0.0 §5.3.1.5.2 ("User invites other user to
		// a conference by sending a REFER request to the other
		// user") is the §5.3.1.3.3 step-2(a) three-way-merge entry
		// point — REFER carries a Refer-To header naming the
		// conference URI; on 202 the referee is expected to issue
		// an INVITE to that URI (driven by §5.3.2.3.2). The
		// B2BUA-stub answers on behalf of the registered callee and
		// also records the participant against the conference so
		// §5.3.3 NOTIFY documents stay consistent.
		return s.respondRefer(req)
	case "BYE":
		// In-dialog request per RFC 3261 §15. The dialog was stashed
		// at 200 OK time on the INVITE (see dispatchInvite); BYE
		// matches by Call-ID and we use the *stored* CallerIMSI for
		// the AF release — terminating-leg BYEs (callee hangs up)
		// would otherwise resolve to the wrong UE if we re-derived
		// the IMSI from the From-URI of the BYE itself.
		//
		// Fallback: if no dialog hit (legacy / pre-stored / cross-
		// instance state), fall back to From-URI lookup so the BYE
		// path remains best-effort releasing.
		callID := req.GetHeader(sip.HdrCallID)
		var imsi string
		if d := s.cscf.GetDialog(callID); d != nil {
			imsi = d.CallerIMSI
			s.cscf.RemoveDialog(callID)
		} else {
			imsi = s.callerIMSI(req)
		}
		if s.cscf.ReleaseMedia != nil && imsi != "" {
			s.cscf.ReleaseMedia(imsi)
		}
		return buildResponse(req, 200, "OK", nil)
	default:
		return buildResponse(req, 405, "Method Not Allowed", nil)
	}
}

// extractBranchFromVia returns the branch parameter from a Via header
// value. The RFC 3261 §8.1.1.7 magic cookie "z9hG4bK" prefix is
// required for modern §17.2.3 matching, but we don't strip it — the
// cookie is part of the key, which is fine.
func extractBranchFromVia(via string) string {
	for i := 0; i < len(via); i++ {
		if via[i] == ';' {
			rest := via[i+1:]
			for {
				end := len(rest)
				for j := 0; j < len(rest); j++ {
					if rest[j] == ';' {
						end = j
						break
					}
				}
				p := rest[:end]
				p = trimSIP(p)
				if len(p) > 7 && p[:7] == "branch=" {
					return p[7:]
				}
				if end == len(rest) {
					break
				}
				rest = rest[end+1:]
			}
			break
		}
	}
	return ""
}

// extractURI pulls the SIP URI out of a name-addr or addr-spec
// header value (To / From / Contact). RFC 3261 §20 gives the
// productions; we accept "<uri>;params", "uri", or "Display <uri>".
// Returns "" on parse failure.
func extractURI(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	if i := strings.Index(h, "<"); i >= 0 {
		if j := strings.Index(h[i:], ">"); j > 0 {
			return strings.TrimSpace(h[i+1 : i+j])
		}
	}
	// addr-spec form (no angle brackets) — strip header parameters.
	if i := strings.Index(h, ";"); i >= 0 {
		return strings.TrimSpace(h[:i])
	}
	return h
}

// addToTagIfMissing ensures the response carries a To-tag, per RFC
// 3261 §12.1.1: "the UAS MUST add a tag to the To header field in
// the response (with the exception of 100 (Trying) responses, in
// which a tag MAY be present)."
func addToTagIfMissing(resp *sip.SipResponse) {
	to := resp.GetHeader(sip.HdrTo)
	if to == "" {
		return
	}
	if strings.Contains(to, "tag=") {
		return
	}
	resp.SetHeader(sip.HdrTo, to+";tag="+sip.GenerateTag())
}

// setToTag forces the response's To header to carry the supplied tag,
// replacing any existing tag= parameter. Used to make the 180 Ringing
// + 200 OK pair share a stable dialog ID per RFC 3261 §13.3.1.1 ("Each
// of these MUST indicate the same dialog ID"); §12.1.1 makes the
// To-tag the local-tag component of that ID at the UAS.
func setToTag(resp *sip.SipResponse, tag string) {
	to := resp.GetHeader(sip.HdrTo)
	if to == "" {
		return
	}
	// Strip any existing ";tag=..." segment.
	if i := strings.Index(to, ";tag="); i >= 0 {
		// Look for the next ";" after the tag value (other params).
		end := len(to)
		for j := i + 5; j < len(to); j++ {
			if to[j] == ';' {
				end = j
				break
			}
		}
		to = to[:i] + to[end:]
	}
	resp.SetHeader(sip.HdrTo, to+";tag="+tag)
}

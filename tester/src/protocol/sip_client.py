# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Minimal SIP/IMS client for VoNR testing.

Sends SIP REGISTER and INVITE through the IMS PDU session to the P-CSCF.
TS 24.229 — SIP/SDP procedures for IMS.
TS 33.203 — IMS security (IPsec SA negotiation via Security-Client).

This is a test client — it does not implement full SIP dialog state.
"""

import socket
import time
import uuid
import hashlib
import base64
import logging
import threading
from collections import OrderedDict

log = logging.getLogger("tester.sip")


def _gen_call_id():
    return f"{uuid.uuid4().hex[:16]}@sa-tester"


def _gen_tag():
    return uuid.uuid4().hex[:8]


def _gen_branch():
    return f"z9hG4bK-{uuid.uuid4().hex[:12]}"


class SipClient:
    """Minimal SIP UA (User Agent) for IMS testing.

    Sends SIP messages over UDP to the P-CSCF via the IMS PDU session TUN interface.
    """

    def __init__(self, local_ip, pcscf_ip, pcscf_port=5060, imsi=None, msisdn=None,
                 domain=None, sim=None):
        """
        Args:
            local_ip: UE's IMS IP (from IMS PDU session, bound to TUN interface)
            pcscf_ip: P-CSCF IP address (from PCO in PDU Session Accept)
            pcscf_port: P-CSCF SIP port (default 5060)
            imsi: UE's IMSI
            msisdn: UE's MSISDN (phone number) — used as IMPU per TS 23.003
            domain: IMS domain (e.g., ims.mnc001.mcc001.3gppnetwork.org)
            sim: SimCard namedtuple with k, opc, op_type for IMS-AKA auth
        """
        self.local_ip = local_ip
        self.pcscf_ip = pcscf_ip
        self.pcscf_port = pcscf_port
        self.imsi = imsi or "001010000000000"
        self.msisdn = msisdn or ""
        self.domain = domain or "ims.mnc001.mcc001.3gppnetwork.org"
        self.sim = sim  # for IMS-AKA authentication
        self.local_port = 5080
        self._sock = None
        self._tag = _gen_tag()
        self._cseq = 0
        self._responses = []  # list of {status_code, call_id, headers_raw}
        self._incoming_requests = []  # incoming SIP requests (e.g., terminating INVITE)
        self._rx_thread = None
        self._stop = threading.Event()
        self.tun_device = None  # set to TUN name to force GTP-U path

        # Dialog state — populated from INVITE 200 OK response
        self._dialogs = {}  # call_id → {remote_tag, route_set, remote_target}

        # Provisional responses observed per call_id, in arrival order.
        # Tests assert on this to verify §13.3.1.1 progress provisionals
        # (e.g. 100 Trying, 180 Ringing) actually came back from the
        # CSCF before the final response.
        # call_id → list[int] (status codes 1xx)
        self.provisionals_seen = {}

        # Branch used by the last in-progress INVITE per call_id.
        # CANCEL must reuse the same Via branch as the request being
        # cancelled (RFC 3261 §9.1: "the CANCEL request constructed
        # by the client MUST have a single Via header field value
        # matching the top Via value in the request being cancelled").
        # call_id → branch
        self._invite_branch = {}

    @property
    def impi(self):
        """IMS Private Identity (TS 23.003 §13.3) — used for authentication."""
        return f"{self.imsi}@{self.domain}"

    @property
    def impu(self):
        """IMS Public User Identity (TS 23.003 §13.4) — the SIP URI.

        Per TS 23.003 §13.4: IMPU is derived from MSISDN when provisioned.
        Format: sip:+MSISDN@ims.domain (tel URI in sip form).
        Fallback to IMSI-based URI if MSISDN not provisioned.
        Must match what HSS has provisioned.
        """
        if self.msisdn:
            msisdn = self.msisdn.lstrip('+')
            return f"sip:+{msisdn}@{self.domain}"
        return f"sip:{self.imsi}@{self.domain}"

    @property
    def contact_uri(self):
        """Contact header URI (TS 24.229 §5.1.1.2) — where this UE can be reached.

        Uses IMPU user part + UE's transport address.
        """
        if self.msisdn:
            user = f"+{self.msisdn.lstrip('+')}"
        else:
            user = self.imsi
        return f"<sip:{user}@{self.local_ip}:{self.local_port}>"

    def _make_target_uri(self, msisdn=None, imsi=None):
        """Build SIP Request-URI from MSISDN (E.164) or IMSI fallback.

        Per TS 23.003 §13.4: use sip:+MSISDN@domain (E.164 with + prefix).
        Per RFC 3261 §19.1.4: S-CSCF does exact userinfo match.
        """
        if msisdn:
            return f"sip:+{msisdn.lstrip('+')}@{self.domain}"
        if imsi:
            return f"sip:{imsi}@{self.domain}"
        return f"sip:{self.imsi}@{self.domain}"

    def start(self):
        """Bind UDP socket to IMS IP and start receiver."""
        self._sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self._sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        if self.tun_device:
            try:
                self._sock.setsockopt(socket.SOL_SOCKET, socket.SO_BINDTODEVICE,
                                      self.tun_device.encode() + b'\0')
            except OSError as e:
                log.warning("SO_BINDTODEVICE %s failed: %s", self.tun_device, e)
        self._sock.bind((self.local_ip, self.local_port))
        self._sock.settimeout(1.0)
        self._stop.clear()
        self._rx_thread = threading.Thread(target=self._recv_loop, daemon=True, name="sip-rx")
        self._rx_thread.start()
        log.info("SIP client started on %s:%d", self.local_ip, self.local_port)

    def stop(self):
        """Close socket and stop receiver."""
        self._stop.set()
        if self._sock:
            self._sock.close()
            self._sock = None
        if self._rx_thread:
            self._rx_thread.join(timeout=3)

    def register(self, expires=3600, timeout=10):
        """Send SIP REGISTER to P-CSCF with IMS-AKA auth. Returns status code or None."""
        self._cseq += 1
        call_id = _gen_call_id()
        branch = _gen_branch()

        msg = (
            f"REGISTER sip:{self.domain} SIP/2.0\r\n"
            f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch}\r\n"
            f"From: <{self.impu}>;tag={self._tag}\r\n"
            f"To: <{self.impu}>\r\n"
            f"Call-ID: {call_id}\r\n"
            f"CSeq: {self._cseq} REGISTER\r\n"
            f"Contact: {self.contact_uri}\r\n"
            f"Expires: {expires}\r\n"
            f"Max-Forwards: 70\r\n"
            f"User-Agent: SA-Tester/1.0\r\n"
            f"Content-Length: 0\r\n"
            f"\r\n"
        )
        status = self._send_and_wait(msg, call_id, timeout // 2, cseq_method="REGISTER")

        # Handle 401 Unauthorized — IMS-AKA challenge
        if status == 401 and self.sim:
            log.info("SIP 401 received — computing IMS-AKA response")
            www_auth = self._get_last_header("WWW-Authenticate")
            if www_auth:
                auth_header = self._compute_aka_response(www_auth, "REGISTER",
                                                          f"sip:{self.domain}")
                if auth_header:
                    self._cseq += 1
                    branch2 = _gen_branch()
                    msg2 = (
                        f"REGISTER sip:{self.domain} SIP/2.0\r\n"
                        f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch2}\r\n"
                        f"From: <{self.impu}>;tag={self._tag}\r\n"
                        f"To: <{self.impu}>\r\n"
                        f"Call-ID: {call_id}\r\n"
                        f"CSeq: {self._cseq} REGISTER\r\n"
                        f"Contact: {self.contact_uri}\r\n"
                        f"Authorization: {auth_header}\r\n"
                        f"Expires: {expires}\r\n"
                        f"Max-Forwards: 70\r\n"
                        f"User-Agent: SA-Tester/1.0\r\n"
                        f"Content-Length: 0\r\n"
                        f"\r\n"
                    )
                    status = self._send_and_wait(msg2, call_id, timeout // 2, cseq_method="REGISTER")
        return status

    def invite(self, target_impu=None, target_imsi=None, target_msisdn=None,
               media_types=None, timeout=10):
        """Send SIP INVITE to P-CSCF. Returns (status_code, call_id) or (None, None).

        Target resolution per TS 24.229:
          - target_impu: full SIP URI (highest priority)
          - target_msisdn: phone number → sip:+MSISDN@domain
          - target_imsi: fallback → sip:IMSI@domain
        """
        media_types = media_types or ["audio"]
        self._cseq += 1
        call_id = _gen_call_id()
        branch = _gen_branch()

        # Build SDP body
        sdp = self._build_sdp(media_types)

        # Resolve target URI per TS 23.003 §13.4
        if target_impu:
            target_uri = target_impu
        else:
            target_uri = self._make_target_uri(target_msisdn, target_imsi)
        msg = (
            f"INVITE {target_uri} SIP/2.0\r\n"
            f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch}\r\n"
            f"From: <{self.impu}>;tag={self._tag}\r\n"
            f"To: <{target_uri}>\r\n"
            f"Call-ID: {call_id}\r\n"
            f"CSeq: {self._cseq} INVITE\r\n"
            f"Contact: {self.contact_uri}\r\n"
            f"Max-Forwards: 70\r\n"
            f"Content-Type: application/sdp\r\n"
            f"User-Agent: SA-Tester/1.0\r\n"
            f"Content-Length: {len(sdp)}\r\n"
            f"\r\n"
            f"{sdp}"
        )
        # Record the INVITE branch + reset the provisional list so a
        # subsequent CANCEL can reuse it (§9.1) and tests can inspect
        # the §13.3.1.1 progress sequence after the call attempt.
        self._invite_branch[call_id] = branch
        self.provisionals_seen[call_id] = []
        status = self._send_and_wait_any(msg, call_id, timeout, cseq_method="INVITE")
        return (status, call_id)

    def reinvite(self, call_id, target_imsi=None, target_msisdn=None,
                  media_types=None, timeout=10):
        """Send SIP re-INVITE for mid-call media change (TS 24.229 §5.1.3).

        Uses dialog state from initial INVITE 200 OK:
          - To-tag from remote party (RFC 3261 §12.2.1.1)
          - Route set from Record-Route headers
          - +g.3gpp.mid-call feature tag in Contact

        The P-CSCF detects new/removed m=video and triggers Rx/N5 → PCF → SMF
        → PDU Session Modification (adds/removes 5QI=2 video bearer).
        """
        media_types = media_types or ["audio"]
        self._cseq += 1
        branch = _gen_branch()

        target_uri = self._make_target_uri(target_msisdn, target_imsi)

        sdp = self._build_sdp(media_types)

        # Get dialog state from initial INVITE response
        dialog = self._dialogs.get(call_id, {})
        remote_tag = dialog.get("remote_tag", "")
        route_set = dialog.get("route_set", [])

        # To header must include remote tag for in-dialog request (RFC 3261 §12.2.1.1)
        to_hdr = f"<{target_uri}>"
        if remote_tag:
            to_hdr += f";tag={remote_tag}"

        # Contact with +g.3gpp.mid-call feature tag
        contact = f"<{self.contact_uri}>;+g.3gpp.mid-call"

        # Build headers
        headers = [
            f"INVITE {target_uri} SIP/2.0",
            f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch}",
            f"From: <{self.impu}>;tag={self._tag}",
            f"To: {to_hdr}",
            f"Call-ID: {call_id}",
            f"CSeq: {self._cseq} INVITE",
            f"Contact: {contact}",
        ]

        # Add Route headers from dialog (RFC 3261 §12.2.1.1)
        for route in route_set:
            headers.append(f"Route: {route}")

        headers.extend([
            "Max-Forwards: 70",
            "Content-Type: application/sdp",
            "User-Agent: SA-Tester/1.0",
            f"Content-Length: {len(sdp)}",
        ])

        msg = "\r\n".join(headers) + "\r\n\r\n" + sdp

        log.info("SIP re-INVITE: call=%s media=%s to_tag=%s routes=%d (+g.3gpp.mid-call)",
                 call_id, media_types, remote_tag or "NONE", len(route_set))
        if not remote_tag:
            log.warning("re-INVITE without To-tag — P-CSCF may reject as out-of-dialog")

        # Log the actual message being sent for debugging
        first_lines = msg.split("\r\n")[:8]
        log.info("re-INVITE message:\n  %s", "\n  ".join(first_lines))

        status = self._send_and_wait_any(msg, call_id, timeout, cseq_method="INVITE")
        log.info("re-INVITE response: status=%s", status)
        return status

    def bye(self, call_id, target_imsi=None, target_msisdn=None, timeout=5):
        """Send SIP BYE to end a call (in-dialog, RFC 3261 §15.1.1).

        Per RFC 3261 §12.2.1.1: Request-URI for in-dialog requests
        is the remote target (Contact from 2xx), not the original To URI.
        """
        self._cseq += 1
        branch = _gen_branch()

        # Use dialog state for To-tag, Route, and remote target
        dialog = self._dialogs.get(call_id, {})
        remote_tag = dialog.get("remote_tag", "")
        route_set = dialog.get("route_set", [])
        # RFC 3261 §12.2.1.1: Request-URI = remote target from dialog
        remote_target = dialog.get("remote_target", "")
        if remote_target:
            target_uri = remote_target.strip().strip('<').split('>')[0]
        else:
            target_uri = self._make_target_uri(target_msisdn, target_imsi)

        to_hdr = f"<{target_uri}>"
        if remote_tag:
            to_hdr += f";tag={remote_tag}"

        headers = [
            f"BYE {target_uri} SIP/2.0",
            f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch}",
            f"From: <{self.impu}>;tag={self._tag}",
            f"To: {to_hdr}",
            f"Call-ID: {call_id}",
            f"CSeq: {self._cseq} BYE",
        ]
        for route in route_set:
            headers.append(f"Route: {route}")
        headers.extend([
            "Max-Forwards: 70",
            "User-Agent: SA-Tester/1.0",
            "Content-Length: 0",
        ])

        msg = "\r\n".join(headers) + "\r\n\r\n"
        return self._send_and_wait(msg, call_id, timeout, cseq_method="BYE")

    def cancel(self, call_id, target_imsi=None, target_msisdn=None, timeout=5):
        """Send SIP CANCEL for an in-flight INVITE (RFC 3261 §9).

        Per §9.1: the CANCEL request constructed by the client MUST
        have a single Via header field value matching the top Via
        value in the request being cancelled. We replay the branch
        recorded by invite() / reinvite() under the same call_id so
        the S-CSCF's §17.2.3 match against the outstanding INVITE IS
        succeeds and the IS goes Proceeding → Completed via 487
        Request Terminated.

        CSeq number for CANCEL must equal the cancelled INVITE's
        CSeq number (§9.1: "The CSeq header field in the CANCEL
        request will have the same value as the INVITE being
        cancelled"); the method differs ("CANCEL").

        Returns the CSCF's status for the CANCEL itself (200 OK on a
        match, 481 if the proxy can't find the txn). The 487 sent for
        the INVITE arrives separately and lands on this same call_id —
        callers can read it from provisionals_seen / dialog state.
        """
        # CANCEL reuses the INVITE's CSeq number, NOT a new one.
        invite_cseq = self._cseq
        branch = self._invite_branch.get(call_id)
        if not branch:
            log.warning("CANCEL: no recorded INVITE branch for call=%s", call_id)
            return None
        target_uri = self._make_target_uri(target_msisdn, target_imsi)

        msg = (
            f"CANCEL {target_uri} SIP/2.0\r\n"
            f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch}\r\n"
            f"From: <{self.impu}>;tag={self._tag}\r\n"
            f"To: <{target_uri}>\r\n"
            f"Call-ID: {call_id}\r\n"
            f"CSeq: {invite_cseq} CANCEL\r\n"
            f"Max-Forwards: 70\r\n"
            f"User-Agent: SA-Tester/1.0\r\n"
            f"Content-Length: 0\r\n"
            f"\r\n"
        )
        log.info("SIP CANCEL → %s (call=%s, replay branch=%s, CSeq=%d)",
                 target_uri, call_id, branch, invite_cseq)
        return self._send_and_wait(msg, call_id, timeout, cseq_method="CANCEL")

    def hold(self, call_id, target_imsi=None, target_msisdn=None, timeout=5):
        """Put a call on hold — in-dialog re-INVITE with a=sendonly.

        Spec anchors:
          * TS 24.229 §5.1.4 — IMS in-dialog media modification.
          * RFC 3264 §5.1   — a=sendonly on offer signals caller-on-hold.
          * RFC 4566 §6     — direction attributes (sendonly / sendrecv /
                              recvonly / inactive); session-level default
                              is sendrecv.
        """
        return self._inDialogReinvite(call_id, "a=sendonly\r\n",
                                      target_imsi, target_msisdn, timeout)

    def resume(self, call_id, target_imsi=None, target_msisdn=None, timeout=5):
        """Resume a held call — in-dialog re-INVITE with a=sendrecv.

        Symmetric to hold(); flips the direction attribute back to
        the RFC 4566 §6 default. The S-CSCF re-fires AuthorizeMedia
        on the new SDP so the PCF can lift the gate-status close it
        applied for the hold (TS 29.244 §8.2.7 Gate Status — TODO:
        end-to-end direction → gate flip on QER).
        """
        return self._inDialogReinvite(call_id, "a=sendrecv\r\n",
                                      target_imsi, target_msisdn, timeout)

    def _inDialogReinvite(self, call_id, direction_line,
                          target_imsi=None, target_msisdn=None, timeout=5):
        """Build + send an in-dialog re-INVITE with the supplied a=
        direction line. Reuses dialog state (To-tag, Route set, branch
        replay) per RFC 3261 §12.2.1.1 so the dialog ID stays stable.
        """
        self._cseq += 1
        branch = _gen_branch()
        target_uri = self._make_target_uri(target_msisdn, target_imsi)

        # Dialog state (To-tag, Route set) — required for in-dialog re-INVITE.
        dialog = self._dialogs.get(call_id, {})
        remote_tag = dialog.get("remote_tag", "")
        route_set = dialog.get("route_set", [])

        to_hdr = f"<{target_uri}>"
        if remote_tag:
            to_hdr += f";tag={remote_tag}"

        sdp = (
            f"v=0\r\n"
            f"o=sa-tester 2 2 IN IP4 {self.local_ip}\r\n"
            f"s=VoNR Call\r\n"
            f"c=IN IP4 {self.local_ip}\r\n"
            f"t=0 0\r\n"
            f"m=audio 20000 RTP/AVP 96\r\n"
            f"a=rtpmap:96 AMR-WB/16000/1\r\n"
            + direction_line
        )

        headers = [
            f"INVITE {target_uri} SIP/2.0",
            f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch}",
            f"From: <{self.impu}>;tag={self._tag}",
            f"To: {to_hdr}",
            f"Call-ID: {call_id}",
            f"CSeq: {self._cseq} INVITE",
            f"Contact: {self.contact_uri}",
        ]
        for route in route_set:
            headers.append(f"Route: {route}")
        headers.extend([
            "Max-Forwards: 70",
            "Content-Type: application/sdp",
            "User-Agent: SA-Tester/1.0",
            f"Content-Length: {len(sdp)}",
        ])

        msg = "\r\n".join(headers) + "\r\n\r\n" + sdp
        # Branch replay window: a CANCEL of the re-INVITE itself would
        # need to use this branch.
        self._invite_branch[call_id] = branch
        # Re-INVITE provisionals are observed for the same call_id;
        # drop any stale entries from the initial INVITE so tests can
        # assert on this round only.
        self.provisionals_seen[call_id] = []
        status = self._send_and_wait_any(msg, call_id, timeout, cseq_method="INVITE")
        if status:
            log.info("re-INVITE (%s) → %s status=%d",
                     direction_line.strip(), target_uri, status)
        return status

    # _hold retained for backwards-compat with any caller still using
    # the underscore-prefixed name; new code should use hold()/resume().
    def _hold(self, call_id, target_imsi=None, target_msisdn=None, timeout=5):
        return self.hold(call_id, target_imsi, target_msisdn, timeout)

    def _create_conference(self, conf_factory_uri, call_id_1, call_id_2,
                           media_types=None, timeout=10):
        """Create a conference by INVITE to conference factory URI.

        TS 24.147 §5.3.1.3.2 verbatim: 'set the request URI of the
        INVITE request to the conference factory URI [...] On
        receiving a 200 (OK) response to the INVITE request with the
        "isfocus" feature parameter indicated in Contact header, the
        conference participant shall store the content of the
        received Contact header as the conference URI.'

        Returns (status_code, conf_call_id, conf_uri) where conf_uri
        is extracted from the 200 OK Contact header.
        """
        media_types = media_types or ["audio"]
        self._cseq += 1
        call_id = _gen_call_id()
        branch = _gen_branch()

        sdp = self._build_sdp(media_types)

        msg = (
            f"INVITE {conf_factory_uri} SIP/2.0\r\n"
            f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch}\r\n"
            f"From: <{self.impu}>;tag={self._tag}\r\n"
            f"To: <{conf_factory_uri}>\r\n"
            f"Call-ID: {call_id}\r\n"
            f"CSeq: {self._cseq} INVITE\r\n"
            f"Contact: {self.contact_uri}\r\n"
            f"Max-Forwards: 70\r\n"
            f"Content-Type: application/sdp\r\n"
            f"User-Agent: SA-Tester/1.0\r\n"
            f"Content-Length: {len(sdp)}\r\n"
            f"\r\n"
            f"{sdp}"
        )
        status = self._send_and_wait_any(msg, call_id, timeout, cseq_method="INVITE")

        # Extract conference URI from 200 OK Contact header (RFC 3261 §8.1.1.8)
        # Per TS 24.147 §5.3.1.3.2: 'store the content of the received
        # Contact header as the conference URI' — this is the URI used
        # for subsequent REFERs that invite participants.
        conf_uri = None
        if status == 200:
            dialog = self._dialogs.get(call_id, {})
            contact = dialog.get("remote_target", "")
            # Contact may be: <sip:conf-xxx@domain> or sip:conf-xxx@domain
            if contact:
                conf_uri = contact.strip().strip('<').split('>')[0]

            # Check raw response stored by _send_and_wait_any
            if not conf_uri and hasattr(self, '_last_response') and self._last_response:
                raw = self._last_response.get("headers_raw", "")
                ct = (self._extract_header(raw, "contact")
                      or self._extract_header(raw, "m") or "")
                if ct:
                    conf_uri = ct.strip().strip('<').split('>')[0]
                else:
                    log.warning("Conference 200 OK missing Contact header "
                                "(RFC 3261 §8.1.1.8 requires Contact in 2xx to INVITE). "
                                "Response:\n%s", raw[:800])

            log.info("Conference created: status=%d conf_uri=%s (factory=%s)",
                     status, conf_uri, conf_factory_uri)
        else:
            log.warning("Conference factory INVITE failed: status=%s (factory=%s)",
                        status, conf_factory_uri)

        return status, call_id, conf_uri

    def refer(self, call_id, refer_to_uri, target_msisdn=None, target_imsi=None, timeout=10):
        """Send SIP REFER to add a participant to a conference.

        RFC 3515 — The SIP REFER Method.
        TS 24.147 §5.3.1.5.2 — 'User invites other user to a
        conference by sending a REFER request to the other user.'
        Used in three-way merge per §5.3.1.3.3 step 2(a) — after the
        conference is created, REFER each held party to the conf URI.

        The REFER is sent in-dialog (with To-tag, Route set).
        Refer-To header contains the conference URI.

        Returns status code (202 Accepted per RFC 3515 §2.4.3).
        """
        self._cseq += 1
        branch = _gen_branch()
        target_uri = self._make_target_uri(target_msisdn, target_imsi)

        # Dialog state (in-dialog REFER per RFC 3515 §2.1)
        dialog = self._dialogs.get(call_id, {})
        remote_tag = dialog.get("remote_tag", "")
        route_set = dialog.get("route_set", [])

        to_hdr = f"<{target_uri}>"
        if remote_tag:
            to_hdr += f";tag={remote_tag}"

        headers = [
            f"REFER {target_uri} SIP/2.0",
            f"Via: SIP/2.0/UDP {self.local_ip}:{self.local_port};branch={branch}",
            f"From: <{self.impu}>;tag={self._tag}",
            f"To: {to_hdr}",
            f"Call-ID: {call_id}",
            f"CSeq: {self._cseq} REFER",
            f"Contact: {self.contact_uri}",
            f"Refer-To: <{refer_to_uri}>",
            f"Referred-By: <{self.impu}>",
        ]
        for route in route_set:
            headers.append(f"Route: {route}")
        headers.extend([
            "Max-Forwards: 70",
            "User-Agent: SA-Tester/1.0",
            "Content-Length: 0",
        ])

        msg = "\r\n".join(headers) + "\r\n\r\n"
        status = self._send_and_wait_any(msg, call_id, timeout, cseq_method="REFER")
        log.info("REFER: %s → %s (status=%s)", target_uri, refer_to_uri, status)
        return status

    def _build_sdp(self, media_types):
        """Build minimal SDP for INVITE (TS 26.114)."""
        lines = [
            "v=0",
            f"o=sa-tester 1 1 IN IP4 {self.local_ip}",
            "s=VoNR Call",
            f"c=IN IP4 {self.local_ip}",
            "t=0 0",
        ]
        rtp_port = 20000
        for media in media_types:
            if media == "audio":
                # AMR-WB (PT=96), AMR (PT=97), telephone-event (PT=100)
                lines.append(f"m=audio {rtp_port} RTP/AVP 96 97 100")
                lines.append("a=rtpmap:96 AMR-WB/16000/1")
                lines.append("a=fmtp:96 mode-change-capability=2; max-red=0")
                lines.append("a=rtpmap:97 AMR/8000/1")
                lines.append("a=rtpmap:100 telephone-event/8000")
                lines.append("a=sendrecv")
                rtp_port += 2
            elif media == "video":
                lines.append(f"m=video {rtp_port} RTP/AVP 99")
                lines.append("a=rtpmap:99 H264/90000")
                lines.append("a=fmtp:99 profile-level-id=42e00c; packetization-mode=1")
                lines.append("a=sendrecv")
                rtp_port += 2
        return "\r\n".join(lines) + "\r\n"

    def _send_and_wait(self, msg_str, call_id, timeout, cseq_method=None):
        """Send SIP message and wait for final response (≥200) matching Call-ID.

        When cseq_method is set, only responses whose CSeq method matches
        are consumed — required when one Call-ID has multiple outstanding
        transactions (RFC 3261 §9.2: a CANCEL gets 200 OK keyed by the
        CANCEL CSeq, while the cancelled INVITE gets 487 keyed by the
        INVITE CSeq, both sharing one Call-ID). Without the filter the
        two threads waiting on the same Call-ID race and steal each
        other's responses.
        """
        if not self._sock:
            log.error("SIP socket is None — cannot send")
            return None
        data = msg_str.encode("utf-8")
        try:
            sent = self._sock.sendto(data, (self.pcscf_ip, self.pcscf_port))
            log.info("SIP TX → %s:%d (%d/%d bytes sent)", self.pcscf_ip, self.pcscf_port, sent, len(data))
        except Exception as e:
            log.error("SIP sendto failed: %s", e)
            return None

        want_method = cseq_method.upper() if cseq_method else None
        deadline = time.time() + timeout
        while time.time() < deadline:
            for resp in list(self._responses):
                if resp.get("call_id") != call_id:
                    continue
                if want_method and resp.get("cseq_method") and resp.get("cseq_method") != want_method:
                    continue
                status = resp.get("status_code", 0)
                if status >= 200:
                    # Final response — return it
                    self._responses.remove(resp)
                    self._last_response = resp
                    return status
                else:
                    # Provisional (1xx) — record per RFC 3261
                    # §13.3.1.1, then keep waiting.
                    self._responses.remove(resp)
                    self.provisionals_seen.setdefault(call_id, []).append(status)
                    log.info("SIP provisional: %d (waiting for final response)", status)
            time.sleep(0.1)
        return None

    def _send_and_wait_any(self, msg_str, call_id, timeout, cseq_method=None):
        """Send SIP message and wait for ANY response (request or response) matching Call-ID.

        Like _send_and_wait, but also surfaces incoming SIP requests on
        the same Call-ID (used by the loopback path where the originating
        INVITE returns as a terminating INVITE on the callee leg).

        cseq_method (when set) demuxes responses by transaction so that
        a parallel CANCEL on the same Call-ID doesn't steal this thread's
        INVITE response (RFC 3261 §9.2: 200 to CANCEL keys on CANCEL CSeq;
        487 to INVITE keys on INVITE CSeq; both share Call-ID).
        """
        if not self._sock:
            log.error("SIP socket is None — cannot send")
            return None

        want_method = cseq_method.upper() if cseq_method else None

        # Flush any stale responses/requests for this call_id before sending
        # (prevents re-INVITE from consuming leftover 200 OK from initial INVITE).
        # When cseq_method is set, only flush entries that match this method —
        # otherwise a parallel CANCEL/487 already in the queue from a sibling
        # thread would be wrongly discarded.
        for r in list(self._responses):
            if r.get("call_id") != call_id:
                continue
            if want_method and r.get("cseq_method") and r.get("cseq_method") != want_method:
                continue
            self._responses.remove(r)
            log.debug("Flushed stale response: %d for call=%s method=%s",
                      r.get("status_code", 0), call_id, r.get("cseq_method") or "?")
        for r in list(self._incoming_requests):
            if r.get("call_id") == call_id:
                self._incoming_requests.remove(r)
                log.debug("Flushed stale request for call=%s", call_id)

        data = msg_str.encode("utf-8")
        try:
            sent = self._sock.sendto(data, (self.pcscf_ip, self.pcscf_port))
            log.info("SIP TX → %s:%d (%d/%d bytes sent) from %s:%d",
                     self.pcscf_ip, self.pcscf_port, sent, len(data),
                     self.local_ip, self.local_port)
        except Exception as e:
            log.error("SIP sendto failed: %s (sock=%s, dst=%s:%d, src=%s:%d)",
                      e, self._sock.fileno() if self._sock else "closed",
                      self.pcscf_ip, self.pcscf_port,
                      self.local_ip, self.local_port)
            return None

        deadline = time.time() + timeout
        while time.time() < deadline:
            # Check responses — skip provisional (1xx), wait for final (≥200)
            for resp in list(self._responses):
                if resp.get("call_id") != call_id:
                    continue
                if want_method and resp.get("cseq_method") and resp.get("cseq_method") != want_method:
                    continue
                status = resp.get("status_code", 0)
                if status >= 200:
                    self._responses.remove(resp)
                    self._last_response = resp
                    return status
                else:
                    # Provisional (1xx) — record per RFC 3261
                    # §13.3.1.1 (UAS may send any number of 1xx
                    # before the final), then keep waiting for ≥200.
                    self._responses.remove(resp)
                    self.provisionals_seen.setdefault(call_id, []).append(status)
                    log.info("SIP provisional: %d (waiting for final)", status)
            # Check incoming requests (e.g., terminating INVITE for loopback)
            for req in list(self._incoming_requests):
                if req.get("call_id") == call_id:
                    self._incoming_requests.remove(req)
                    return req.get("status_code", 200)
            time.sleep(0.1)
        return None

    def _get_last_header(self, header_name):
        """Get a header from the last received response."""
        resp = getattr(self, '_last_response', None)
        if not resp:
            return None
        for line in resp.get("headers_raw", "").split("\r\n"):
            if line.lower().startswith(header_name.lower() + ":"):
                return line.split(":", 1)[1].strip()
        return None

    def _compute_aka_response(self, www_auth, method, uri):
        """Compute IMS-AKA Digest response from WWW-Authenticate challenge.

        RFC 3310 — HTTP Digest AKAv1-MD5.
        nonce = base64(RAND(16) || AUTN(16))
        password = RES (raw bytes from Milenage)
        """
        if not self.sim:
            return None

        # Parse WWW-Authenticate parameters
        params = {}
        auth_str = www_auth
        if auth_str.lower().startswith("digest "):
            auth_str = auth_str[7:]
        for part in auth_str.split(","):
            part = part.strip()
            if "=" in part:
                k, _, v = part.partition("=")
                params[k.strip()] = v.strip().strip('"')

        nonce = params.get("nonce", "")
        realm = params.get("realm", self.domain)
        qop = params.get("qop", "auth")

        if not nonce:
            log.warning("No nonce in WWW-Authenticate")
            return None

        try:
            # Decode nonce: base64(RAND(16) || AUTN(16))
            nonce_bytes = base64.b64decode(nonce)
            if len(nonce_bytes) < 32:
                log.warning("AKA nonce too short: %d bytes", len(nonce_bytes))
                return None
            rand = nonce_bytes[:16]
            autn = nonce_bytes[16:32]

            # Run raw Milenage (not 5G-AKA) to get RES for IMS-AKA
            from sa_crypto.milenage import Milenage
            from sa_crypto.utils import xor_buf

            K, OPc, op_type = self.sim.k, self.sim.opc, self.sim.op_type
            mil = Milenage(None)
            if op_type == "OPC":
                mil.set_opc(OPc)
                xres, ck, ik, ak = mil.f2345(K, rand)
                mac_check = mil.f1(K, rand, xor_buf(autn[:6], ak), autn[6:8])
            else:
                xres, ck, ik, ak = mil.f2345(K, rand, OPc)
                mac_check = mil.f1(K, rand, xor_buf(autn[:6], ak), autn[6:8], OPc)

            # Verify MAC
            if mac_check != autn[8:16]:
                log.warning("IMS-AKA MAC verification failed")
                return None

            res = xres  # Raw Milenage RES (8 bytes) for IMS-AKA Digest
            log.info("IMS-AKA: RES=%s (%d bytes), AUTN_MAC=%s, expected_MAC=%s",
                     res.hex(), len(res), autn[8:16].hex(), mac_check.hex())

            # Compute Digest-AKAv1-MD5 (RFC 3310 §3)
            # Username = IMPI (TS 23.003 §13.3)
            username = self.impi
            nc = "00000001"
            cnonce = uuid.uuid4().hex[:16]

            def md5_hex(data):
                if isinstance(data, str):
                    data = data.encode("utf-8")
                return hashlib.md5(data).hexdigest()

            # HA1 = MD5(username:realm:RES_bytes)
            ha1_input = username.encode() + b":" + realm.encode() + b":" + res
            ha1 = hashlib.md5(ha1_input).hexdigest()
            ha2 = md5_hex(f"{method}:{uri}")

            if qop:
                response = md5_hex(f"{ha1}:{nonce}:{nc}:{cnonce}:{qop}:{ha2}")
            else:
                response = md5_hex(f"{ha1}:{nonce}:{ha2}")

            log.info("IMS-AKA: HA1(raw)=%s HA2=%s response=%s", ha1, ha2, response)
            log.info("IMS-AKA: username=%s realm=%s uri=%s nc=%s cnonce=%s qop=%s",
                     username, realm, uri, nc, cnonce, qop)

            auth = (
                f'Digest username="{username}", realm="{realm}", '
                f'nonce="{nonce}", uri="{uri}", response="{response}", '
                f'algorithm=AKAv1-MD5, cnonce="{cnonce}", '
                f'nc={nc}, qop={qop}'
            )
            log.info("IMS-AKA Digest computed for %s", username)
            return auth

        except Exception as e:
            log.error("IMS-AKA computation failed: %s", e)
            return None

    def _recv_loop(self):
        """Receive SIP responses and incoming requests."""
        while not self._stop.is_set():
            try:
                data, addr = self._sock.recvfrom(65535)
            except socket.timeout:
                continue
            except OSError:
                break
            text = data.decode("utf-8", errors="replace")
            log.debug("SIP RX ← %s:%d (%d bytes)", addr[0], addr[1], len(data))

            if text.startswith("SIP/2.0"):
                # SIP Response
                parts = text.split("\r\n", 1)
                sl = parts[0].split(None, 2)
                status_code = int(sl[1]) if len(sl) > 1 else 0
                call_id = self._extract_header(text, "call-id")
                # CSeq method (RFC 3261 §8.1.1.5: "<seq-number> SP method")
                # — needed to demux responses when one Call-ID has multiple
                # outstanding transactions (notably CANCEL: per §9.2 the
                # 200 OK targets the CANCEL CSeq while the 487 targets the
                # cancelled INVITE's CSeq, both with the same Call-ID).
                cseq_hdr = self._extract_header(text, "cseq") or ""
                cseq_method = ""
                cseq_parts = cseq_hdr.split(None, 1)
                if len(cseq_parts) >= 2:
                    cseq_method = cseq_parts[1].strip().upper()
                self._responses.append({
                    "status_code": status_code,
                    "call_id": call_id,
                    "cseq_method": cseq_method,
                    "headers_raw": text,
                })
                log.info("SIP RX: %d %s (Call-ID=%s)", status_code,
                         sl[2] if len(sl) > 2 else "", call_id or "?")

                # Store dialog state from 2xx responses (RFC 3261 §12.1.2)
                if status_code >= 200 and status_code < 300 and call_id:
                    to_hdr = self._extract_header(text, "to") or ""
                    remote_tag = ""
                    if ";tag=" in to_hdr:
                        remote_tag = to_hdr.split(";tag=")[1].split(";")[0].strip()
                    # Collect Record-Route headers (reversed for Route set)
                    route_set = []
                    for line in text.split("\r\n"):
                        if line.lower().startswith("record-route:"):
                            route_set.append(line.split(":", 1)[1].strip())
                    route_set.reverse()  # RFC 3261 §12.1.2
                    # RFC 3261 §7.3.3: compact form 'm' = 'Contact'
                    contact = (self._extract_header(text, "contact")
                               or self._extract_header(text, "m")
                               or "")
                    # Parse SDP from 2xx response for MRFP media info
                    sdp_info = self._parse_sdp_media(text)
                    self._dialogs[call_id] = {
                        "remote_tag": remote_tag,
                        "route_set": route_set,
                        "remote_target": contact,
                        "remote_sdp": sdp_info,
                    }
                    if remote_tag:
                        log.debug("Dialog stored: call=%s remote_tag=%s routes=%d contact=%s",
                                  call_id, remote_tag, len(route_set), contact or "(none)")
            else:
                # SIP Request (e.g., incoming INVITE for terminating call)
                parts = text.split("\r\n", 1)
                sl = parts[0].split(None, 2)
                method = sl[0] if sl else "?"
                call_id = self._extract_header(text, "call-id")
                log.info("SIP RX Request: %s (Call-ID=%s) from %s:%d", method, call_id, addr[0], addr[1])

                # Auto-respond to incoming INVITE with 200 OK
                if method == "INVITE":
                    self._auto_answer_invite(text, addr)
                    self._incoming_requests.append({
                        "method": method, "call_id": call_id, "status_code": 200,
                    })
                    # Track incoming dialog for teardown
                    if not hasattr(self, '_incoming_dialogs'):
                        self._incoming_dialogs = []
                    self._incoming_dialogs.append(call_id)
                    # Parse SDP from incoming INVITE for MRFP media info
                    sdp_info = self._parse_sdp_media(text)
                    if sdp_info:
                        if not hasattr(self, '_remote_media'):
                            self._remote_media = {}
                        self._remote_media[call_id] = sdp_info
                elif method == "BYE":
                    # RFC 3261 §15.1.2: UAS responds 200 OK to BYE
                    self._auto_answer_bye(text, addr)
                elif method == "ACK":
                    pass  # ACK is end-to-end, no response needed

    @staticmethod
    def _parse_sdp_media(sip_text):
        """Parse SDP from SIP message to extract remote media IP and ports.

        Returns dict with 'ip', 'audio_port', 'video_port' or None.
        Used to discover MRFP media endpoints for conference calls.
        """
        # SDP is after the blank line separator
        parts = sip_text.split("\r\n\r\n", 1)
        if len(parts) < 2:
            return None
        sdp = parts[1]
        if not sdp.strip():
            return None

        result = {}
        # c= line: connection IP
        for line in sdp.split("\r\n"):
            line = line.strip()
            if line.startswith("c=IN IP4 "):
                result['ip'] = line.split()[-1]
            elif line.startswith("m=audio "):
                result['audio_port'] = int(line.split()[1])
            elif line.startswith("m=video "):
                result['video_port'] = int(line.split()[1])

        return result if result.get('ip') else None

    @staticmethod
    def _extract_header(text, header_name):
        for line in text.split("\r\n"):
            if line.lower().startswith(header_name.lower() + ":"):
                return line.split(":", 1)[1].strip()
        return None

    def _auto_answer_invite(self, invite_text, addr):
        """Auto-answer an incoming INVITE with 200 OK.

        Per RFC 3261 §12.1.1: UAS stores dialog state from incoming INVITE.
        The From URI becomes the remote target for subsequent in-dialog
        requests (BYE). The From-tag becomes the remote_tag.
        """
        via = self._extract_header(invite_text, "via")
        from_hdr = self._extract_header(invite_text, "from")
        to_hdr = self._extract_header(invite_text, "to")
        call_id = self._extract_header(invite_text, "call-id")
        cseq = self._extract_header(invite_text, "cseq")
        contact = self._extract_header(invite_text, "contact") or ""

        # Add To-tag for the response
        if to_hdr and ";tag=" not in to_hdr:
            to_hdr += f";tag={_gen_tag()}"

        resp = (
            f"SIP/2.0 200 OK\r\n"
            f"Via: {via}\r\n"
            f"From: {from_hdr}\r\n"
            f"To: {to_hdr}\r\n"
            f"Call-ID: {call_id}\r\n"
            f"CSeq: {cseq}\r\n"
            f"Contact: {self.contact_uri}\r\n"
            f"Content-Length: 0\r\n"
            f"\r\n"
        )
        self._sock.sendto(resp.encode(), addr)
        log.info("SIP TX: 200 OK (auto-answer INVITE)")

        # Store dialog state for incoming INVITE (RFC 3261 §12.1.1)
        # remote_target = Contact or From URI from the incoming INVITE
        # remote_tag = From-tag (caller's tag)
        remote_tag = ""
        if from_hdr and ";tag=" in from_hdr:
            remote_tag = from_hdr.split(";tag=")[1].split(";")[0].strip()
        remote_target = contact or ""
        if not remote_target and from_hdr:
            # Extract URI from From header as fallback
            if '<' in from_hdr:
                remote_target = from_hdr.split('<')[1].split('>')[0]
        self._dialogs[call_id] = {
            "remote_tag": remote_tag,
            "route_set": [],
            "remote_target": remote_target,
        }

    def _auto_answer_bye(self, bye_text, addr):
        """Auto-answer an incoming BYE with 200 OK (RFC 3261 §15.1.2)."""
        via = self._extract_header(bye_text, "via")
        from_hdr = self._extract_header(bye_text, "from")
        to_hdr = self._extract_header(bye_text, "to")
        call_id = self._extract_header(bye_text, "call-id")
        cseq = self._extract_header(bye_text, "cseq")

        resp = (
            f"SIP/2.0 200 OK\r\n"
            f"Via: {via}\r\n"
            f"From: {from_hdr}\r\n"
            f"To: {to_hdr}\r\n"
            f"Call-ID: {call_id}\r\n"
            f"CSeq: {cseq}\r\n"
            f"Content-Length: 0\r\n"
            f"\r\n"
        )
        self._sock.sendto(resp.encode(), addr)
        log.info("SIP TX: 200 OK (auto-answer BYE for Call-ID=%s)", call_id)

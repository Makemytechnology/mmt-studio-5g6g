# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""gNB NGAP state machine — uses protocol.ngap + protocol.sctp.

States: IDLE → NG_SETUP_SENT → READY → ERROR
"""

import sys
import logging
import queue
import threading
import time

from src.protocol.ngap import NgapCodec
from src.protocol.sctp import SctpClient
from src.config import GNB_DEFAULTS

log = logging.getLogger("tester.gnb")

def _console(msg):
    sys.stderr.write(msg + "\n")
    sys.stderr.flush()

IDLE = "IDLE"
NG_SETUP_SENT = "NG_SETUP_SENT"
READY = "READY"
ERROR = "ERROR"


class GnbStateMachine:
    """Emulated 5G gNB — SCTP + NGAP state machine."""

    _id_counter = 0
    _id_lock = threading.Lock()

    def __init__(self, amf_ip, amf_port, gnb_id=None, gnb_name=None,
                 mcc=None, mnc=None, tac=None, slices=None, source_ip=None,
                 gtpu_manager=None):
        with GnbStateMachine._id_lock:
            GnbStateMachine._id_counter += 1
            idx = GnbStateMachine._id_counter

        self.amf_ip = amf_ip
        self.amf_port = amf_port
        self.gnb_id = gnb_id or GNB_DEFAULTS["gnb_id_base"] + idx
        self.gnb_name = gnb_name or f"{GNB_DEFAULTS['gnb_name_prefix']}-{idx:02d}"
        self.mcc = mcc or GNB_DEFAULTS["mcc"]
        self.mnc = mnc or GNB_DEFAULTS["mnc"]
        self.tac = tac or GNB_DEFAULTS["tac"]
        self.slices = slices or GNB_DEFAULTS["slices"]
        self.source_ip = source_ip  # bind to specific local IP (for multi-gNB)
        self.gnb_ip = None

        # State
        self.state = IDLE
        self._state_event = threading.Event()
        self._state_lock = threading.Lock()

        # Transport
        self._sctp = SctpClient()
        self._stop = threading.Event()
        self._rx_thread = None

        # Slow-handler worker. The SCTP recv thread MUST NOT block on
        # pycrate ASN.1 decode of the PSR transfer bytes or on sync NAS
        # parse inside ue.on_nas_pdu (both hold pycrate's global lock
        # for 5-30 ms per call). When a burst of PSR Setup messages
        # arrives from a fast AMF we used to see intermittent SCTP
        # aborts because recv was stalled long enough for the kernel to
        # give up on the association.
        # Recv thread now enqueues heavy handlers here; this single
        # daemon drains the queue serially (pycrate's global lock means
        # a pool wouldn't help anyway).
        self._handler_q: "queue.Queue" = queue.Queue(maxsize=4096)
        self._handler_thread = None

        # UE map
        self.ue_map = {}   # ran_ue_ngap_id → UeStateMachine
        self._ran_ue_counter = 0
        self._gtp_teid_counter = 0x10000

        # GTP-U data plane
        self._gtpu = gtpu_manager
        self._pending_tunnels = {}  # psi -> {local_teid, remote_teid, upf_ip}

        # Handover state
        self._ho_context = None     # Target gNB: pending HO context from AMF
        self._ho_command_event = None  # Source gNB: signals HandoverCommand received
        self._ho_prep_failure = None   # Source gNB: cause if AMF returned HandoverPreparationFailure
        self._ho_cancel_event = None   # Source gNB: signals HandoverCancelAcknowledge received
        self._path_switch_event = None  # Target gNB: signals PathSwitchAck received
        self._path_switch_failed = False
        self._last_dl_status_transfer = None  # Target gNB: last RANStatusTransfer container received
        # Fault-injection: when set on a target gNB, the next HandoverRequest is
        # rejected with HandoverFailure(cause) instead of acknowledged.
        self.force_ho_failure = None   # str cause value or None

        # Paging state
        self._paging_event = None   # Signals Paging received from AMF
        self._last_paging = None    # Last paging info

        # NG Setup observability — captured in _on_ngap_recv when proc=21
        # arrives. TS 38.413 §9.2.6.2 (RESPONSE) / §9.2.6.3 (FAILURE) IEs
        # by id; TCs assert presence / value without parsing the wire.
        self.ng_setup_response_ies = None  # dict[ie_id] → value, or None
        self.ng_setup_failure_ies = None   # dict[ie_id] → value, or None

        # Logs
        self.log_entries = []

        # Protocol trace — captures TX/RX NGAP messages for test analysis
        self.protocol_trace = []   # [{dir, time, proc, msg_type, hex, size, detail}]
        self._trace_enabled = True

    # ─── Logging & State ───

    def _log(self, msg, *args):
        formatted = msg % args if args else msg
        self.log_entries.append({"time": time.time(), "msg": formatted})
        if len(self.log_entries) > 500:
            self.log_entries = self.log_entries[-300:]
        log.info("[%s] %s", self.gnb_name, formatted)

    def _set_state(self, new_state):
        with self._state_lock:
            old = self.state
            self.state = new_state
            self._state_event.set()
            self._state_event = threading.Event()
        self._log("State: %s -> %s", old, new_state)

    def wait_for_state(self, target, timeout=10):
        deadline = time.time() + timeout
        while time.time() < deadline:
            with self._state_lock:
                if self.state == target:
                    return True
                evt = self._state_event
            evt.wait(timeout=min(deadline - time.time(), 1.0))
        return self.state == target

    # ─── Connection Lifecycle ───

    def connect(self):
        """Connect SCTP + send NG Setup Request."""
        if self.state != IDLE:
            return False
        try:
            self.gnb_ip = self._sctp.connect(self.amf_ip, self.amf_port,
                                              source_ip=self.source_ip)
            self._log("SCTP connected to %s:%d", self.amf_ip, self.amf_port)
            _console(f"[gNB] {self.gnb_name}: SCTP connected to {self.amf_ip}:{self.amf_port}")
        except Exception as e:
            self._log("SCTP connect FAILED: %s", e)
            _console(f"[gNB] {self.gnb_name}: SCTP connect FAILED - {e}")
            self._set_state(ERROR)
            return False

        self._stop.clear()
        # Hook the SCTP send-worker so a sendall failure marks gNB ERROR
        # and unblocks any UE FSM waiters. This used to depend on the
        # recv thread catching BrokenPipeError mid-dispatch; now sends
        # happen on the worker, so we route failure notification here.
        self._sctp.set_send_failure_cb(self._on_sctp_send_failed)
        # Start the slow-handler worker before we start the recv thread
        # so anything the recv thread enqueues is guaranteed to have a
        # consumer.
        self._handler_thread = threading.Thread(
            target=self._handler_loop, daemon=True,
            name=f"gnb-hdlr-{self.gnb_name}")
        self._handler_thread.start()
        self._rx_thread = threading.Thread(target=self._sctp.recv_loop,
                                            args=(self._on_ngap_recv, self._stop),
                                            daemon=True, name=f"gnb-rx-{self.gnb_name}")
        self._rx_thread.start()

        self._set_state(NG_SETUP_SENT)
        data = NgapCodec.build_ng_setup_request(
            self.gnb_id, self.gnb_name, self.mcc, self.mnc, self.tac, self.slices)
        self._trace_tx(data, "NGSetupRequest", 21)
        # Stream 0 — non-UE-associated (TS 38.412 §7).
        self._sctp.send(data, stream=0)
        return True

    def disconnect(self):
        # Flip state to IDLE FIRST so every workflow using self.state as a
        # bail signal (tunnel-setup worker, _send_success/_send_failure
        # closures, UE FSM waiters) sees the change before _sctp.disconnect
        # nulls the socket. Previously we transitioned at the very end, so
        # for ~300 ms in the middle of disconnect() the gNB was "READY"
        # (per state) while the socket was already gone (per _sock=None) —
        # in-flight worker jobs then got a noisy "SCTP not connected"
        # error chain they couldn't anticipate.
        if self.state not in (IDLE, ERROR):
            self._set_state(IDLE)
        self._stop.set()

        # Drop any tunnel-setup work still queued in the GtpuManager
        # worker — the SCTP assoc is going away (or already gone), and
        # there's no point creating TUNs we'd immediately tear down +
        # whose PSR Setup Response we couldn't deliver anyway. Already-
        # running create_tunnel calls finish; only queued ones drop.
        if self._gtpu:
            try:
                self._gtpu.cancel_pending_setups()
            except Exception as e:
                log.debug("cancel_pending_setups raised: %s", e)
            # Destroy all GTP-U tunnels for UEs on this gNB
            for ue in list(self.ue_map.values()):
                for psi, sess in list(ue.pdu_sessions.items()):
                    local_teid = sess.get('local_teid')
                    if local_teid:
                        self._gtpu.destroy_tunnel(local_teid)
        # Poke the handler worker so it wakes from queue.get() and sees
        # _stop. Sentinel = None.
        try:
            self._handler_q.put_nowait(None)
        except queue.Full:
            pass
        self._sctp.disconnect()
        if self._handler_thread is not None and self._handler_thread.is_alive():
            self._handler_thread.join(timeout=2.0)
            self._handler_thread = None
        self._log("Disconnected")

    # ─── NGAP Dispatch (receive callback) ───

    # NGAP procedure code → message name mapping
    _PROC_NAMES = {
        21: {
            "successfulOutcome": "NGSetupResponse",
            "unsuccessfulOutcome": "NGSetupFailure",
            "initiatingMessage": "NGSetupRequest",
        },
        12: {
            "initiatingMessage": "HandoverRequired",
            "successfulOutcome": "HandoverCommand",
            "unsuccessfulOutcome": "HandoverPreparationFailure",
        },
        13: {
            "initiatingMessage": "HandoverRequest",
            "successfulOutcome": "HandoverRequestAcknowledge",
            "unsuccessfulOutcome": "HandoverFailure",
        },
        11: {"initiatingMessage": "HandoverNotify"},
        10: {
            "initiatingMessage": "HandoverCancel",
            "successfulOutcome": "HandoverCancelAcknowledge",
        },
        7: {"initiatingMessage": "DownlinkRANStatusTransfer"},
        49: {"initiatingMessage": "UplinkRANStatusTransfer"},
        24: {"initiatingMessage": "Paging"},
        25: {
            "initiatingMessage": "PathSwitchRequest",
            "successfulOutcome": "PathSwitchRequestAcknowledge",
            "unsuccessfulOutcome": "PathSwitchRequestFailure",
        },
        4: {"initiatingMessage": "DownlinkNASTransport"},
        14: {"initiatingMessage": "InitialContextSetupRequest"},
        15: {"initiatingMessage": "InitialUEMessage"},
        29: {"initiatingMessage": "PDUSessionResourceSetupRequest"},
        26: {"initiatingMessage": "PDUSessionModifyRequest"},
        41: {"initiatingMessage": "UEContextReleaseCommand"},
        46: {"initiatingMessage": "UplinkNASTransport"},
    }

    def _on_ngap_recv(self, data):
        """Called by SctpClient.recv_loop for each NGAP PDU."""
        try:
            category, proc_code, ies = NgapCodec.decode(data)
            msg_name = self._PROC_NAMES.get(proc_code, {}).get(category, f"proc_{proc_code}")
            self._trace_rx(data, category, proc_code, msg_name)
            log.debug("[%s] RX NGAP: %s proc=%d", self.gnb_name, category, proc_code)

            if category == 'successfulOutcome':
                if proc_code == 21:
                    # TS 38.413 §9.2.6.2 NG SETUP RESPONSE — stash the IE
                    # dict so TCs can assert AMF Name (M), Served GUAMI
                    # List (M), Relative AMF Capacity (M), PLMN Support
                    # List (M), and any optional IEs the AMF chose to send.
                    self.ng_setup_response_ies = dict(ies)
                    self._set_state(READY)  # NG Setup Response
                elif proc_code == 12:
                    self._handle_handover_command(ies)  # HandoverCommand from AMF
                elif proc_code == 25:
                    self._handle_path_switch_ack(ies)  # PathSwitchRequestAck
                elif proc_code == 10:
                    self._handle_handover_cancel_ack(ies)  # HandoverCancelAcknowledge
            elif category == 'unsuccessfulOutcome':
                if proc_code == 21:
                    # TS 38.413 §9.2.6.3 NG SETUP FAILURE — stash IEs so
                    # TCs can assert the mandatory Cause IE (and optional
                    # Time to Wait / Criticality Diagnostics) per spec.
                    self.ng_setup_failure_ies = dict(ies)
                    self._log("NG Setup FAILED")
                    self._set_state(ERROR)
                elif proc_code == 12:
                    self._handle_handover_preparation_failure(ies)
                elif proc_code == 13:
                    self._handle_handover_failure(ies)  # AMF echoes target's HandoverFailure
                elif proc_code == 25:
                    self._handle_path_switch_failure(ies)
            elif category == 'initiatingMessage':
                if proc_code == 4:
                    self._handle_dl_nas_transport(ies)
                elif proc_code == 14:
                    self._handle_initial_context_setup(ies)
                elif proc_code == 7:
                    self._handle_dl_ran_status_transfer(ies)
                elif proc_code == 29:
                    # Offload to the handler worker. pycrate APER decode of
                    # the inner PSR transfer bytes + sync NAS parse in
                    # ue.on_nas_pdu both hold pycrate's global lock for
                    # 5-30 ms per message. Running them here would stall
                    # the SCTP recv thread for 40-240 ms on an 8-UE burst
                    # — enough for the kernel to abort the association.
                    try:
                        self._handler_q.put_nowait(
                            (self._handle_pdu_session_setup, ies))
                    except queue.Full:
                        self._log("Handler queue full — dropping PSR Setup "
                                  "(AMF will retry); check for worker stall")
                elif proc_code == 41:
                    self._handle_ue_context_release(ies)
                elif proc_code == 26:
                    self._handle_pdu_session_modify(ies)
                elif proc_code == 13:
                    self._handle_handover_request(ies)  # HandoverRequest from AMF (target gNB)
                elif proc_code == 24:
                    self._handle_paging(ies)  # Paging from AMF
                else:
                    self._log("Unhandled initiatingMessage proc=%d", proc_code)
        except BrokenPipeError as e:
            # Peer closed the SCTP association. No point logging per-message —
            # trip the state to ERROR, unblock UE state-waiters, stop the
            # recv thread. The test will surface the real failure reason
            # (whatever caused the peer to tear down) via logs from before.
            if self.state not in (IDLE, ERROR):
                self._log("SCTP send failed (EPIPE) — peer closed the "
                          "association; marking gNB ERROR and stopping recv")
                self._set_state(ERROR)
                self._stop.set()
                # Best-effort: wake any UE FSM waiters stuck on state events
                for ue in list(self.ue_map.values()):
                    try:
                        if hasattr(ue, "_state_event"):
                            ue._state_event.set()
                    except Exception:
                        pass
        except (ConnectionResetError, ConnectionError) as e:
            # ConnectionError now also fires when SctpClient.send() detects a
            # dead worker or full queue (post-send-worker refactor). Same
            # remediation as the peer-reset case: stop, ERROR, wake waiters.
            if self.state not in (IDLE, ERROR):
                self._log("SCTP %s — marking gNB ERROR",
                          "connection reset by peer"
                          if isinstance(e, ConnectionResetError) else f"send error: {e}")
                self._set_state(ERROR)
                self._stop.set()
                for ue in list(self.ue_map.values()):
                    try:
                        if hasattr(ue, "_state_event"):
                            ue._state_event.set()
                    except Exception:
                        pass
        except Exception as e:
            self._log("NGAP dispatch error: %s (raw %d bytes: %s)",
                      e, len(data), data.hex())

    def _handler_loop(self):
        """Drain self._handler_q, calling offloaded handlers serially.

        Runs on its own daemon thread. Serial is correct — pycrate's
        global lock serializes NAS/NGAP decode across the whole process
        anyway, so a worker pool would just burn memory without
        shortening critical sections.
        """
        while not self._stop.is_set():
            try:
                item = self._handler_q.get(timeout=1.0)
            except queue.Empty:
                continue
            if item is None:
                return
            fn, arg = item
            try:
                fn(arg)
            except Exception as e:
                self._log("handler %s failed: %s",
                          getattr(fn, "__name__", str(fn)), e)

    def _stream_for_ue(self, ran_ue_id):
        """Pick the SCTP outbound stream for a NGAP message.

        TS 38.412 §7: stream 0 is reserved for non-UE-associated procedures
        (NG Setup, NG Reset, AMF Configuration Update reply, OverloadStart,
        Paging without an associated UE context, …). Per-UE messages
        (UplinkNASTransport, ContextSetupResponse, PSR Setup Response, etc)
        go on streams 1..(out_streams-1), hashed from RAN-UE-NGAP-ID so a
        single slow UE only stalls its own stream — not the whole assoc.

        Returns 0 when ran_ue_id is None (non-UE-associated).
        """
        if ran_ue_id is None:
            return 0
        usable = max(self._sctp.out_streams - 1, 1)
        return (int(ran_ue_id) % usable) + 1

    def _on_sctp_send_failed(self, err):
        """Send-worker callback — sendall() raised, association is dead."""
        if self.state in (IDLE, ERROR):
            return
        self._log("SCTP send-worker failed (%s) — marking gNB ERROR", err)
        self._set_state(ERROR)
        self._stop.set()
        for ue in list(self.ue_map.values()):
            try:
                if hasattr(ue, "_state_event"):
                    ue._state_event.set()
            except Exception:
                pass

    # ─── NGAP Handlers ───

    def _find_ue(self, ies):
        """Find UE by RAN-UE-NGAP-ID or AMF-UE-NGAP-ID."""
        ran_id = ies.get(85)
        ue = self.ue_map.get(ran_id)
        if ue and ies.get(10) is not None:
            ue.amf_ue_ngap_id = ies[10]
        return ue

    def _handle_dl_nas_transport(self, ies):
        """DownlinkNASTransport — forward NAS to UE."""
        ue = self._find_ue(ies)
        nas_pdu = ies.get(38)
        if ue and nas_pdu:
            ue.on_nas_pdu(bytes(nas_pdu) if not isinstance(nas_pdu, bytes) else nas_pdu)

    def _handle_initial_context_setup(self, ies):
        """InitialContextSetupRequest — respond + forward NAS."""
        ue = self._find_ue(ies)
        amf_id, ran_id = ies.get(10), ies.get(85)
        if amf_id is not None and ran_id is not None:
            rsp = NgapCodec.build_initial_context_setup_response(amf_id, ran_id)
            self._trace_tx(rsp, "InitialContextSetupResponse", 14)
            self._sctp.send(rsp, stream=self._stream_for_ue(ran_id))
        nas_pdu = ies.get(38)
        if ue and nas_pdu:
            ue.on_nas_pdu(bytes(nas_pdu) if not isinstance(nas_pdu, bytes) else nas_pdu)

    def _handle_pdu_session_setup(self, ies):
        """PDUSessionResourceSetupRequest — defer the NGAP response until the
        local GTP-U tunnel is actually up.

        Old behaviour sent PduSessionResourceSetupResponse(success) immediately
        from the recv thread, *before* the TUN/route was built. If netlink
        later failed (or the worker was queued behind N other UEs), AMF and
        UPF believed the session was good and started pushing DL into a
        black hole — which several core implementations then escalate into
        an SCTP-level reset.

        New behaviour:
          1. Parse out everything needed to build the response *or* a
             failure response (AMF/RAN IDs, allocated gNB-side TEID,
             remote UPF IP/TEID).
          2. Forward the inner NAS PDU to the UE (this is independent of
             the NGAP response and lets the UE allocate its IP).
          3. Stash the build context in self._pending_tunnels[psi] —
             _on_pdu_session_accepted (called from the UE FSM) consumes
             it, the GtpuManager worker builds the TUN, and only then
             does the worker enqueue success-or-failure on the SCTP
             send-worker.
        """
        ue = self._find_ue(ies)
        amf_id, ran_id = ies.get(10), ies.get(85)
        for item in ies.get(74, []):
            psi = item.get('pDUSessionID', 1)
            self._gtp_teid_counter += 1
            local_teid = self._gtp_teid_counter

            self._log("PDU Session item keys: %s", list(item.keys()))
            transfer_bytes = item.get('pDUSessionResourceSetupRequestTransfer')
            if transfer_bytes is None:
                for k, v in item.items():
                    if 'transfer' in k.lower() or 'Transfer' in k:
                        transfer_bytes = v
                        self._log("Found transfer bytes under key: %s", k)
                        break

            upf_ip = upf_teid = None
            if transfer_bytes is not None and self._gtpu:
                upf_ip, upf_teid = self._extract_upf_tunnel(transfer_bytes)

            # Always build a pending entry — even if UPF info is missing —
            # so _on_pdu_session_accepted can send a failure response with
            # the correct cause instead of just silently swallowing the
            # request and letting AMF time out.
            self._pending_tunnels[psi] = {
                'amf_id': amf_id,
                'ran_id': ran_id,
                'local_teid': local_teid,
                'remote_teid': upf_teid,
                'upf_ip': upf_ip,
            }
            if upf_ip and upf_teid:
                self._log("GTP-U pending: PSI=%d UPF=%s TEID=0x%08X (response deferred until TUN built)",
                          psi, upf_ip, upf_teid)
            else:
                self._log("GTP-U pending: PSI=%d UPF/TEID UNDETERMINED — will send PSR FAILURE",
                          psi)

            nas_pdu = item.get('pDUSessionNAS-PDU')
            if ue and nas_pdu:
                ue.on_nas_pdu(bytes(nas_pdu) if not isinstance(nas_pdu, bytes) else nas_pdu)
            elif ue and nas_pdu is None:
                # TS 23.502 v19.7.0 §4.2.3.2 step 12 — "UP activate" /
                # reactivation path: the AMF re-issues PDU SESSION
                # RESOURCE SETUP REQUEST to bring up the user plane for
                # a session the UE already has at the NAS layer, so the
                # request carries NO 5GSM Accept (no NAS PDU). The
                # normal flow defers the NGAP response until the UE FSM
                # calls _on_pdu_session_accepted on NAS receipt — but
                # with NAS absent, that callback never fires.
                #
                # For the reactivate case we synthesise the callback
                # immediately so _on_pdu_session_accepted builds the
                # GTP-U tunnel and emits PDUSessionResourceSetupResponse
                # with the freshly-allocated gNB DL TEID. The UE-side
                # IP is reused from the existing pdu_sessions entry
                # (still valid — the session was Suspended, not
                # Released).
                ue_ip = (ue.pdu_sessions.get(psi, {}) or {}).get('ip')
                self._log("Reactivate PSR (no NAS): synthesising _on_pdu_session_accepted "
                          "PSI=%d IMSI=%s ue_ip=%s (TS 23.502 §4.2.3.2 step 12)",
                          psi, ue.imsi, ue_ip)
                self._on_pdu_session_accepted(ue, psi, ue_ip)

    def _handle_pdu_session_modify(self, ies):
        """PDUSessionResourceModifyRequest — TS 38.413 §8.2.3.

        Handles dedicated bearer activation (VoNR/ViNR) triggered by PCF Rx → N7 → SMF.
        Responds with PDUSessionResourceModifyResponse and forwards NAS PDU
        (PDU Session Modification Command) to the UE FSM.
        """
        ue = self._find_ue(ies)
        amf_id, ran_id = ies.get(10), ies.get(85)

        # IE 64: PDUSessionResourceModifyListModReq
        modify_list = ies.get(64, [])
        for item in modify_list:
            psi = item.get('pDUSessionID', 1)
            self._log("PDU Session Modify: PSI=%d keys=%s", psi, list(item.keys()))

            # Send PDUSessionResourceModifyResponse
            rsp = NgapCodec.build_pdu_session_resource_modify_response(amf_id, ran_id, psi)
            self._trace_tx(rsp, "PDUSessionResourceModifyResponse", 26)
            self._sctp.send(rsp, stream=self._stream_for_ue(ran_id))

            # Find and forward NAS PDU to UE
            # NAS PDU can be in several places depending on pycrate decode:
            # - Top-level 'nAS-PDU' or 'pDUSessionNAS-PDU'
            # - Inside the transfer IE as nested structure
            nas_bytes = None
            for key in ('nAS-PDU', 'pDUSessionNAS-PDU', 'nas-PDU'):
                val = item.get(key)
                if val is not None:
                    nas_bytes = bytes(val) if not isinstance(val, bytes) else val
                    self._log("NAS PDU from key '%s' (%d bytes)", key, len(nas_bytes))
                    break

            if nas_bytes is None:
                # NAS PDU might be in the NGAP-level IE (not inside the modify item)
                # Check top-level IEs for NAS-PDU (IE 38)
                nas_ie = ies.get(38)
                if nas_ie is not None:
                    nas_bytes = bytes(nas_ie) if not isinstance(nas_ie, bytes) else nas_ie
                    self._log("NAS PDU from top-level IE 38 (%d bytes)", len(nas_bytes))

            if nas_bytes is None:
                # Walk item values looking for bytes that look like NAS
                for k, v in item.items():
                    if k in ('pDUSessionID', 'pDUSessionResourceModifyRequestTransfer', 's-NSSAI'):
                        continue
                    if isinstance(v, (bytes, bytearray)) and len(v) > 2:
                        nas_bytes = bytes(v)
                        self._log("NAS PDU from key '%s' (%d bytes)", k, len(nas_bytes))
                        break
                    elif isinstance(v, list) and v:
                        # pycrate might decode NAS as a list
                        try:
                            nas_bytes = bytes(v)
                            self._log("NAS PDU from key '%s' (list->bytes, %d bytes)", k, len(nas_bytes))
                            break
                        except Exception:
                            pass

            if ue and nas_bytes:
                self._log("Forwarding PDU Session Modification Command to UE (%d bytes)", len(nas_bytes))
                ue.on_nas_pdu(nas_bytes)
                # Update GTP-U tunnel with new QoS rules from the Modification
                if self._gtpu:
                    session = ue.pdu_sessions.get(psi, {})
                    local_teid = session.get('local_teid')
                    qos_rules = session.get('qos_flows', [])
                    if local_teid and qos_rules:
                        self._gtpu.update_qos_rules(local_teid, qos_rules)
            elif ue:
                self._log("PDU Session Modify: no NAS PDU found — UE cannot send Modification Complete")

    def _on_pdu_session_accepted(self, ue, psi, ue_ip):
        """Called by UE FSM when PDU session accept is received.

        Builds the GTP-U tunnel on the GtpuManager worker thread and only
        then enqueues the PduSessionResourceSetupResponse (success or
        FailedToSetup, depending on whether the TUN came up). The recv
        thread never sends the response — see _handle_pdu_session_setup
        for the full rationale.
        """
        pending = self._pending_tunnels.pop(psi, None)
        if not pending:
            self._log("PDU Session %d accepted but NO pending context for IMSI=%s "
                      "— cannot send NGAP response", psi, ue.imsi)
            return

        amf_id = pending.get('amf_id')
        ran_id = pending.get('ran_id')
        local_teid = pending['local_teid']
        remote_teid = pending.get('remote_teid')
        upf_ip = pending.get('upf_ip')
        gnb_ip = self.gnb_ip or "127.0.0.1"

        # Helpers — close over the IDs above so the worker doesn't touch
        # self._pending_tunnels (already popped) or recv-thread state.
        # Both helpers re-check self.state at send time: between the
        # _build_tunnel_then_respond entry check and the actual send,
        # ~100 ms of netlink work elapses, during which disconnect() may
        # have fired and nulled the SCTP socket. A late state check
        # turns that race into a clean skip instead of a noisy
        # "SCTP not connected" log.
        def _send_failure(cause_value, log_msg):
            if self.state in (IDLE, ERROR):
                return
            self._log("PSR Setup FAILED: PSI=%d IMSI=%s — %s "
                      "(NGAP cause: transport/%s)", psi, ue.imsi, log_msg, cause_value)
            try:
                rsp = NgapCodec.build_pdu_session_resource_setup_response_failed(
                    amf_id, ran_id, psi,
                    cause_group="transport", cause_value=cause_value)
                self._trace_tx(rsp, "PDUSessionResourceSetupResponse(failed)", 29)
                self._sctp.send(rsp, stream=self._stream_for_ue(ran_id))
            except Exception as e:
                self._log("Failed to encode/send PSR failure response: %s", e)

        def _send_success():
            if self.state in (IDLE, ERROR):
                return
            try:
                rsp = NgapCodec.build_pdu_session_resource_setup_response(
                    amf_id, ran_id, psi, local_teid, gnb_ip)
                self._trace_tx(rsp, "PDUSessionResourceSetupResponse", 29)
                self._sctp.send(rsp, stream=self._stream_for_ue(ran_id))
            except Exception as e:
                self._log("Failed to encode/send PSR success response: %s", e)

        # Pre-flight: missing UPF info means we can't build a tunnel at all.
        if not (upf_ip and remote_teid):
            self._gtpu.submit_setup(lambda: _send_failure(
                "transport-resource-unavailable",
                "UPF tunnel info missing from PSR Setup Request"))
            return
        if not (self._gtpu and self._gtpu.available):
            self._gtpu.submit_setup(lambda: _send_failure(
                "transport-resource-unavailable",
                "GTP-U manager unavailable (no CAP_NET_ADMIN?)"))
            return

        qos_rules = ue.pdu_sessions.get(psi, {}).get('qos_flows', [])

        def _build_tunnel_then_respond():
            # If the SCTP assoc is already gone (peer SHUTDOWN, send-worker
            # failure, test teardown) skip the netlink/TUN work entirely —
            # we couldn't deliver the response anyway, and the AMF doesn't
            # know about this PSI either. Avoids ~10 wasted TUN creates +
            # leaked routes when AMF SHUTDOWNs mid-burst.
            if self.state in (ERROR, IDLE):
                self._log("Skipping tunnel build for IMSI=%s PSI=%d — "
                          "gNB no longer connected (state=%s)",
                          ue.imsi, psi, self.state)
                return
            try:
                tun = self._gtpu.create_tunnel(
                    imsi=ue.imsi, ue_ip=ue_ip,
                    local_teid=local_teid, remote_teid=remote_teid,
                    upf_ip=upf_ip, qos_rules=qos_rules)
            except Exception as e:
                _send_failure("transport-resource-unavailable",
                              f"create_tunnel raised: {e}")
                return
            if not tun:
                _send_failure("transport-resource-unavailable",
                              "create_tunnel returned None — TUN/route setup failed")
                return
            sess = ue.pdu_sessions.get(psi)
            if sess is not None:
                sess['tun'] = tun
                sess['local_teid'] = local_teid
            self._log("GTP-U tunnel active: PSI=%d TUN=%s UE_IP=%s", psi, tun, ue_ip)
            _send_success()

        # Serialize on GtpuManager's single worker. The worker thread does
        # the netlink/TUN work AND sends the NGAP response — so AMF only
        # ever sees a response that matches reality.
        self._gtpu.submit_setup(_build_tunnel_then_respond)

    def _extract_upf_tunnel(self, transfer_data):
        """Extract UPF IP and TEID from PDUSessionResourceSetupRequestTransfer.

        Handles both pre-decoded (tuple/dict from pycrate) and raw APER bytes.
        Returns (upf_ip, upf_teid) or (None, None).
        """
        import socket as _sock
        import struct as _struct

        # Case 1: pycrate pre-decoded as tuple ('TypeName', {protocolIEs: [...]})
        if isinstance(transfer_data, tuple) and len(transfer_data) == 2:
            _, val = transfer_data
            if isinstance(val, dict):
                return self._parse_transfer_ies(val.get('protocolIEs', []))

        # Case 2: already a dict with protocolIEs
        if isinstance(transfer_data, dict):
            return self._parse_transfer_ies(transfer_data.get('protocolIEs', []))

        # Case 3: raw APER bytes
        if isinstance(transfer_data, (bytes, bytearray)):
            return NgapCodec.decode_pdu_session_setup_request_transfer(transfer_data)

        return (None, None)

    def _parse_transfer_ies(self, ies_list):
        """Parse protocolIEs list from decoded PDUSessionResourceSetupRequestTransfer."""
        import socket as _sock
        import struct as _struct

        for ie in ies_list:
            ie_id = ie.get('id')
            # IE 139 = UL-NGU-UP-TNLInformation
            if ie_id == 139:
                ie_val = ie.get('value')
                if ie_val is None:
                    continue
                # Unwrap tuple layers: ('UPTransportLayerInformation', ('gTPTunnel', {...}))
                tunnel_info = ie_val
                while isinstance(tunnel_info, tuple) and len(tunnel_info) == 2:
                    tunnel_info = tunnel_info[1]
                if isinstance(tunnel_info, dict):
                    addr_val = tunnel_info.get('transportLayerAddress')
                    teid_val = tunnel_info.get('gTP-TEID')
                    if addr_val is not None and teid_val is not None:
                        # transportLayerAddress: (int, bit_length) or int
                        ip_int = addr_val[0] if isinstance(addr_val, tuple) else addr_val
                        upf_ip = _sock.inet_ntoa(ip_int.to_bytes(4, 'big'))
                        # gTP-TEID: bytes or int
                        if isinstance(teid_val, (bytes, bytearray)):
                            upf_teid = _struct.unpack('>I', teid_val)[0]
                        else:
                            upf_teid = int(teid_val)
                        return (upf_ip, upf_teid)
        return (None, None)

    # ─── Handover Handlers (TS 38.413 §8.4) ───

    def _handle_handover_request(self, ies):
        """HandoverRequest — AMF → target gNB (TS 38.413 §8.4.2.2).

        AMF asks this (target) gNB to prepare for an incoming UE.
        We allocate resources and respond with HandoverRequestAcknowledge.
        """
        amf_id = ies.get(10)
        self._log("HandoverRequest received: AMF-UE=%s, IEs=%s", amf_id, list(ies.keys()))

        # Allocate new RAN-UE-NGAP-ID for the incoming UE on target
        self._ran_ue_counter += 1
        ran_id = self._ran_ue_counter
        self._log("HandoverRequest: AMF-UE=%s, new RAN-UE=%d", amf_id, ran_id)

        # Extract PDU sessions from the request.
        # TS 38.413 v19.2.0 §9.2.3.4 HANDOVER REQUEST mandatory IE:
        #   id-PDUSessionResourceSetupListHOReq  ProtocolIE-ID ::= 73
        # Each item is a PDUSessionResourceSetupItemHOReq (TS 38.413 §9.2.3.4):
        #   { PDUSessionID, S-NSSAI, HandoverRequestTransfer (OCTET STRING
        #     containing PDU Session Resource Setup Request Transfer per
        #     §9.3.4.1), ... }
        pdu_list = ies.get(73, [])
        if not pdu_list:
            self._log("IE 73 (PDUSessionResourceSetupListHOReq) absent — available IEs: %s",
                      list(ies.keys()))

        pdu_sessions = []
        gnb_gtp_teids = {}
        for item in pdu_list:
            psi = item.get('pDUSessionID', 1)
            self._gtp_teid_counter += 1
            gnb_gtp_teids[psi] = self._gtp_teid_counter
            pdu_sessions.append({'psi': psi})
            self._log("HO PDU Session: PSI=%d new TEID=0x%08X", psi, self._gtp_teid_counter)

            # Extract UPF tunnel info from handoverRequestTransfer
            transfer_bytes = item.get('handoverRequestTransfer')
            if transfer_bytes is None:
                # Try alternate key names
                for k in ('pDUSessionResourceSetupRequestTransfer', 'handoverResourceSetupRequestTransfer'):
                    if item.get(k):
                        transfer_bytes = item[k]
                        break
            if transfer_bytes and self._gtpu:
                upf_ip, upf_teid = self._extract_upf_tunnel(transfer_bytes)
                if upf_ip and upf_teid:
                    self._pending_tunnels[psi] = {
                        'local_teid': self._gtp_teid_counter,
                        'remote_teid': upf_teid,
                        'upf_ip': upf_ip,
                    }

        if not pdu_sessions:
            # No PDU sessions found — create a default one so we can still respond
            self._gtp_teid_counter += 1
            pdu_sessions = [{'psi': 1}]
            gnb_gtp_teids = {1: self._gtp_teid_counter}
            self._log("No PDU sessions in HandoverRequest — using default PSI=1")

        # Fault injection: reject the request with HandoverFailure (TS 38.413 §8.4.2.3).
        if self.force_ho_failure:
            cause = self.force_ho_failure
            rsp = NgapCodec.build_handover_failure(amf_id, cause_value=cause)
            self._trace_tx(rsp, "HandoverFailure", 13)
            self._sctp.send(rsp, stream=self._stream_for_ue(ran_id))
            self._log("Sent HandoverFailure (cause=%s) — fault injection", cause)
            self._ho_context = None
            return

        # Store handover context for when UE "arrives"
        self._ho_context = {
            'amf_id': amf_id,
            'ran_id': ran_id,
            'pdu_sessions': pdu_sessions,
            'gnb_gtp_teids': gnb_gtp_teids,
        }

        # Send HandoverRequestAcknowledge
        rsp = NgapCodec.build_handover_request_acknowledge(
            amf_id, ran_id, pdu_sessions,
            self.gnb_ip or "127.0.0.1", gnb_gtp_teids)
        self._trace_tx(rsp, "HandoverRequestAcknowledge", 13)
        self._sctp.send(rsp, stream=self._stream_for_ue(ran_id))
        self._log("Sent HandoverRequestAcknowledge with %d PDU sessions", len(pdu_sessions))

    def _handle_handover_command(self, ies):
        """HandoverCommand — AMF → source gNB (TS 38.413 §8.4.1.2).

        AMF tells source gNB to execute the handover (send RRC Reconfiguration to UE).
        In our emulator, we signal the handover_event so the test case can proceed.
        """
        amf_id = ies.get(10)
        ran_id = ies.get(85)
        self._log("HandoverCommand received: AMF-UE=%s RAN-UE=%s", amf_id, ran_id)

        # Signal that handover command was received — test case will complete the HO
        if hasattr(self, '_ho_command_event'):
            self._ho_command_event.set()

    def _handle_path_switch_ack(self, ies):
        """PathSwitchRequestAcknowledge — AMF → target gNB (TS 38.413 §8.4.4.3).

        AMF confirms the path switch is complete — UPF has been updated.
        """
        amf_id = ies.get(10)
        ran_id = ies.get(85)
        self._log("PathSwitchRequestAck: AMF-UE=%s RAN-UE=%s — UPF path switched", amf_id, ran_id)
        if hasattr(self, '_path_switch_event'):
            self._path_switch_event.set()

    def _handle_path_switch_failure(self, ies):
        """PathSwitchRequestFailure — AMF → target gNB (TS 38.413 §8.4.4.4)."""
        cause = ies.get(15)
        self._log("PathSwitchRequestFailure received: cause=%s", cause)
        self._path_switch_failed = True
        if self._path_switch_event is not None:
            self._path_switch_event.set()

    def _handle_handover_preparation_failure(self, ies):
        """HandoverPreparationFailure — AMF → source gNB (TS 38.413 §8.4.1.3).

        AMF rejects HandoverRequired (no target available, target rejected, ...).
        Unblocks initiate_handover by signalling _ho_command_event with a
        failure flag so the caller returns False with the cause.
        """
        cause = ies.get(15)
        self._log("HandoverPreparationFailure received: cause=%s", cause)
        self._ho_prep_failure = cause or "unspecified"
        if self._ho_command_event is not None:
            self._ho_command_event.set()

    def _handle_handover_failure(self, ies):
        """HandoverFailure — AMF → source gNB.

        AMF forwards the target's HandoverFailure (proc 13 unsuccessful) when
        target couldn't allocate resources. Treated identically to
        HandoverPreparationFailure on the source side.
        """
        cause = ies.get(15)
        self._log("HandoverFailure received from AMF: cause=%s", cause)
        self._ho_prep_failure = cause or "ho-target-not-allowed"
        if self._ho_command_event is not None:
            self._ho_command_event.set()

    def _handle_handover_cancel_ack(self, ies):
        """HandoverCancelAcknowledge — AMF → source gNB (TS 38.413 §8.4.5.3)."""
        amf_id = ies.get(10)
        ran_id = ies.get(85)
        self._log("HandoverCancelAck received: AMF-UE=%s RAN-UE=%s", amf_id, ran_id)
        if self._ho_cancel_event is not None:
            self._ho_cancel_event.set()

    def _handle_dl_ran_status_transfer(self, ies):
        """DownlinkRANStatusTransfer — AMF → target gNB (TS 38.413 §8.4.6.3).

        Target receives PDCP COUNT values from source via AMF; in this
        emulator we only record the container for inspection.
        """
        amf_id = ies.get(10)
        ran_id = ies.get(85)
        container = ies.get(84)
        self._last_dl_status_transfer = container
        self._log("DownlinkRANStatusTransfer received: AMF-UE=%s RAN-UE=%s container=%s",
                  amf_id, ran_id, "present" if container else "missing")

    def initiate_handover(self, ue, target_gnb):
        """Source gNB initiates N2 handover for a UE to target gNB.

        Sends HandoverRequired to AMF and waits for HandoverCommand.
        Returns True if HandoverCommand received within timeout.
        """
        if ue.amf_ue_ngap_id is None:
            self._log("Cannot handover: UE has no AMF context")
            return False

        # Collect only active PDU sessions (must have IP assigned)
        pdu_sessions = []
        for psi, sess in ue.pdu_sessions.items():
            ip = sess.get('ip', '')
            if ip and ip != 'unknown':
                pdu_sessions.append({'psi': psi})
                self._log("HO PDU session: PSI=%d IP=%s TEID=0x%08X",
                          psi, ip, sess.get('local_teid', 0))

        if not pdu_sessions:
            self._log("Cannot handover: UE %s has no active PDU sessions", ue.imsi)
            return False

        self._ho_command_event = threading.Event()
        self._ho_prep_failure = None

        self._log("Initiating N2 Handover: UE %s → target gNB %s (%d PDU sessions)",
                  ue.imsi, target_gnb.gnb_name, len(pdu_sessions))
        self._log("  Source: %s gnb_id=0x%X ip=%s",
                  self.gnb_name, self.gnb_id, self.gnb_ip)
        self._log("  Target: %s gnb_id=0x%X ip=%s",
                  target_gnb.gnb_name, target_gnb.gnb_id, target_gnb.gnb_ip)

        data = NgapCodec.build_handover_required(
            ue.amf_ue_ngap_id, ue.ran_ue_ngap_id,
            self.gnb_id, target_gnb.gnb_id,
            self.mcc, self.mnc, self.tac,
            pdu_sessions)
        self._trace_tx(data, "HandoverRequired", 12)
        self._sctp.send(data, stream=self._stream_for_ue(ue.ran_ue_ngap_id))
        return True

    def complete_handover(self, ue, source_gnb, timeout=10):
        """Target gNB completes handover — UE has 'arrived'.

        Sends HandoverNotify to AMF, creates GTP-U tunnels, adopts UE.
        Returns True if successful.
        """
        ho_ctx = getattr(self, '_ho_context', None)
        if not ho_ctx:
            self._log("No handover context — cannot complete")
            return False

        amf_id = ho_ctx['amf_id']
        ran_id = ho_ctx['ran_id']
        pdu_sessions = ho_ctx['pdu_sessions']
        gnb_gtp_teids = ho_ctx['gnb_gtp_teids']

        # Move UE from source to target gNB
        source_gnb.detach_ue(ue)
        ue.ran_ue_ngap_id = ran_id
        ue.gnb = self
        self.ue_map[ran_id] = ue
        self._log("UE %s adopted: RAN-UE=%d AMF-UE=%s", ue.imsi, ran_id, amf_id)

        # Destroy old GTP-U tunnels on source gNB
        if source_gnb._gtpu:
            for psi, sess in list(ue.pdu_sessions.items()):
                old_teid = sess.get('local_teid')
                if old_teid:
                    source_gnb._gtpu.destroy_tunnel(old_teid)
                    self._log("Destroyed old GTP-U tunnel: PSI=%d TEID=0x%08X", psi, old_teid)

        # Create new GTP-U tunnels on target gNB
        for sess in pdu_sessions:
            psi = sess['psi']
            pending = self._pending_tunnels.pop(psi, None)
            if pending and self._gtpu and self._gtpu.available:
                ue_ip = ue.pdu_sessions.get(psi, {}).get('ip', 'unknown')
                qos_rules = ue.pdu_sessions.get(psi, {}).get('qos_flows', [])
                tun = self._gtpu.create_tunnel(
                    imsi=ue.imsi, ue_ip=ue_ip,
                    local_teid=pending['local_teid'],
                    remote_teid=pending['remote_teid'],
                    upf_ip=pending['upf_ip'],
                    qos_rules=qos_rules)
                if tun:
                    ue.pdu_sessions[psi]['tun'] = tun
                    ue.pdu_sessions[psi]['local_teid'] = pending['local_teid']
                    self._log("New GTP-U tunnel: PSI=%d TUN=%s TEID=0x%08X",
                              psi, tun, pending['local_teid'])

        # Send HandoverNotify to AMF
        data = NgapCodec.build_handover_notify(
            amf_id, ran_id, self.mcc, self.mnc, self.tac, self.gnb_id)
        self._trace_tx(data, "HandoverNotify", 11)
        self._sctp.send(data, stream=self._stream_for_ue(ran_id))
        self._log("HandoverNotify sent — UPF path switch requested")

        self._ho_context = None
        return True

    def send_uplink_ran_status_transfer(self, ue, drb_id=1,
                                          ul_pdcp_sn=0, dl_pdcp_sn=0):
        """Source gNB → AMF: PDCP COUNT for in-sequence resume on target.

        Called between HandoverCommand and HandoverNotify (TS 38.413 §8.4.6).
        Returns True on send.
        """
        if ue.amf_ue_ngap_id is None or ue.ran_ue_ngap_id is None:
            self._log("Cannot send UplinkRANStatusTransfer: UE has no NGAP IDs")
            return False
        data = NgapCodec.build_uplink_ran_status_transfer(
            ue.amf_ue_ngap_id, ue.ran_ue_ngap_id,
            drb_id=drb_id, ul_pdcp_sn=ul_pdcp_sn, dl_pdcp_sn=dl_pdcp_sn)
        self._trace_tx(data, "UplinkRANStatusTransfer", 49)
        self._sctp.send(data, stream=self._stream_for_ue(ue.ran_ue_ngap_id))
        self._log("UplinkRANStatusTransfer sent (DRB=%d, UL SN=%d, DL SN=%d)",
                  drb_id, ul_pdcp_sn, dl_pdcp_sn)
        return True

    def cancel_handover(self, ue, cause_value="handover-cancelled", timeout=5):
        """Source gNB → AMF: cancel a handover that hasn't completed yet.

        TS 38.413 §8.4.5. Sends HandoverCancel and waits for
        HandoverCancelAcknowledge. Returns True on ack within timeout.
        """
        if ue.amf_ue_ngap_id is None or ue.ran_ue_ngap_id is None:
            self._log("Cannot cancel handover: UE has no NGAP IDs")
            return False
        self._ho_cancel_event = threading.Event()
        data = NgapCodec.build_handover_cancel(
            ue.amf_ue_ngap_id, ue.ran_ue_ngap_id, cause_value=cause_value)
        self._trace_tx(data, "HandoverCancel", 10)
        self._sctp.send(data, stream=self._stream_for_ue(ue.ran_ue_ngap_id))
        self._log("HandoverCancel sent (cause=%s) — waiting for Ack", cause_value)
        ok = self._ho_cancel_event.wait(timeout=timeout)
        if not ok:
            self._log("HandoverCancelAck not received within %ds", timeout)
        return ok

    def send_end_markers(self, ue):
        """Send GTP-U End Marker (TS 29.281 §5.1) on each PDU session's
        old uplink tunnel toward UPF. Called from source gNB after
        HandoverCommand to mark "no more uplink coming on this TEID".

        Returns count of End Marker packets sent.
        """
        if not self._gtpu or not getattr(self._gtpu, '_udp_sock', None):
            return 0
        sent = 0
        for psi, sess in ue.pdu_sessions.items():
            local_teid = sess.get('local_teid')
            if not local_teid:
                continue
            tun_info = self._gtpu._tunnels.get(local_teid) if hasattr(self._gtpu, '_tunnels') else None
            if not tun_info:
                continue
            upf_ip = tun_info.get('upf_ip')
            upf_teid = tun_info.get('remote_teid')
            if not upf_ip or not upf_teid:
                continue
            # GTP-U End Marker: flags=0x30 (V=1,PT=1,no ext), msg_type=0xFE,
            # length=0, TEID=peer's TEID. No payload. (TS 29.281 §5.1.)
            import struct as _struct
            pkt = _struct.pack('!BBHI', 0x30, 0xFE, 0, upf_teid & 0xFFFFFFFF)
            try:
                self._gtpu._udp_sock.sendto(pkt, (upf_ip, 2152))
                sent += 1
                self._log("Sent GTP-U End Marker: PSI=%d → %s TEID=0x%08X",
                          psi, upf_ip, upf_teid)
            except Exception as e:
                self._log("End Marker send failed for PSI=%d: %s", psi, e)
        return sent

    # ─── UE Context Release / RLF / Inactivity ───

    def request_ue_context_release(self, ue, cause_group="radioNetwork",
                                    cause_value="radio-connection-with-ue-lost"):
        """gNB requests AMF to release UE context (TS 38.413 §8.3.2).

        Used for: RLF, inactivity timeout, AN release, etc.
        AMF will respond with UEContextReleaseCommand.
        """
        if ue.amf_ue_ngap_id is None:
            self._log("Cannot release: UE has no AMF context")
            return False
        self._log("UE Context Release Request: %s cause=%s/%s",
                  ue.imsi, cause_group, cause_value)
        data = NgapCodec.build_ue_context_release_request(
            ue.amf_ue_ngap_id, ue.ran_ue_ngap_id,
            cause_group, cause_value)
        self._trace_tx(data, "UEContextReleaseRequest", 42)
        self._sctp.send(data, stream=self._stream_for_ue(ue.ran_ue_ngap_id))
        return True

    def send_error_indication(self, ue=None, cause_group="radioNetwork",
                               cause_value="unspecified"):
        """gNB sends ErrorIndication to AMF (TS 38.413 §8.7.5).

        Reports protocol errors or abnormal conditions.
        """
        amf_id = ue.amf_ue_ngap_id if ue else None
        ran_id = ue.ran_ue_ngap_id if ue else None
        self._log("ErrorIndication: cause=%s/%s UE=%s",
                  cause_group, cause_value, ue.imsi if ue else "none")
        data = NgapCodec.build_error_indication(amf_id, ran_id, cause_group, cause_value)
        self._trace_tx(data, "ErrorIndication", 9)
        # ErrorIndication may be UE-associated (ran_id present) or non-UE
        # (ran_id=None) — _stream_for_ue handles both.
        self._sctp.send(data, stream=self._stream_for_ue(ran_id))

    def report_rrc_inactive(self, ue, rrc_state="inactive"):
        """gNB reports RRC state transition to AMF (TS 38.413 §8.7.4).

        rrc_state: 'inactive' or 'connected'
        """
        if ue.amf_ue_ngap_id is None:
            return
        self._log("RRC Inactive Transition: %s → %s", ue.imsi, rrc_state)
        data = NgapCodec.build_rrc_inactive_transition_report(
            ue.amf_ue_ngap_id, ue.ran_ue_ngap_id,
            rrc_state, self.mcc, self.mnc, self.tac, self.gnb_id)
        self._trace_tx(data, "RRCInactiveTransitionReport", 37)
        self._sctp.send(data, stream=self._stream_for_ue(ue.ran_ue_ngap_id))

    # ─── Paging / Service Request ───

    def _handle_paging(self, ies):
        """Paging — AMF → gNB (TS 38.413 §8.5.2).

        AMF pages a UE in RRC Inactive/Idle state.
        We signal the paging event so the test case can trigger Service Request.
        """
        # IE 115 = UEPagingIdentity (5G-S-TMSI)
        paging_id = ies.get(115)
        # IE 50 = TAIListForPaging
        tai_list = ies.get(50, [])
        self._log("Paging received: identity=%s TAIs=%d", paging_id, len(tai_list))

        # Signal paging event for test case
        if hasattr(self, '_paging_event') and self._paging_event:
            self._paging_event.set()
        # Store paging info
        self._last_paging = {'identity': paging_id, 'tai_list': tai_list}

    def send_service_request(self, ue, service_type=1):
        """UE sends NAS Service Request via gNB (TS 24.501 §8.2.15).

        Used when UE transitions from RRC Inactive/Idle to Connected.
        service_type: 0=signalling, 1=data, 2=mobile-terminated
        """
        from src.protocol.nas import NasBuilder
        nas_pdu = NasBuilder.service_request(service_type=service_type)
        self._log("UE %s Service Request (type=%d)", ue.imsi, service_type)

        # Send as InitialUEMessage (UE re-establishing RRC connection)
        if ue.ran_ue_ngap_id is None:
            self.attach_ue(ue)
        data = NgapCodec.build_initial_ue_message(
            ue.ran_ue_ngap_id, nas_pdu, self.mcc, self.mnc, self.tac, self.gnb_id)
        self._trace_tx(data, "InitialUEMessage(ServiceRequest)", 15)
        self._sctp.send(data, stream=self._stream_for_ue(ue.ran_ue_ngap_id))
        return True

    def _handle_ue_context_release(self, ies):
        """UEContextReleaseCommand — respond + transition UE."""
        amf_id, ran_id = None, None
        pair = ies.get(114)
        if pair:
            if isinstance(pair, tuple):
                pair = pair[1]
            if isinstance(pair, dict):
                amf_id = pair.get('aMF-UE-NGAP-ID')
                ran_id = pair.get('rAN-UE-NGAP-ID')
        amf_id = amf_id or ies.get(10)
        ran_id = ran_id or ies.get(85)

        ue = self.ue_map.get(ran_id)
        if ue is None and amf_id:
            for u in self.ue_map.values():
                if u.amf_ue_ngap_id == amf_id:
                    ue = u
                    ran_id = u.ran_ue_ngap_id
                    break

        if amf_id is not None and ran_id is not None:
            rsp = NgapCodec.build_ue_context_release_complete(amf_id, ran_id)
            self._trace_tx(rsp, "UEContextReleaseComplete", 41)
            self._sctp.send(rsp, stream=self._stream_for_ue(ran_id))

        if ue:
            # Destroy GTP-U tunnels for this UE
            if self._gtpu:
                for psi, sess in list(ue.pdu_sessions.items()):
                    local_teid = sess.get('local_teid')
                    if local_teid:
                        self._gtpu.destroy_tunnel(local_teid)
            ue._set_state("DEREGISTERED")
            self.ue_map.pop(ran_id, None)
            # TS 38.413 v19.2.0 §8.3.3.2 (UE CONTEXT RELEASE COMMAND,
            # Successful Operation) verbatim: "Upon reception of the UE
            # CONTEXT RELEASE COMMAND message, the NG-RAN node shall
            # release all related signalling and user data transport
            # resources and reply with the UE CONTEXT RELEASE COMPLETE
            # message." Combined with §9.3.3.2 ("RAN UE NGAP ID — This
            # IE uniquely identifies the UE association over the NG
            # interface within the NG-RAN node") and §8.6.1.2 ("The
            # NG-RAN node shall allocate a unique RAN UE NGAP ID to be
            # used for the UE" on every INITIAL UE MESSAGE): the ID is
            # bound to the now-released UE-associated logical NG-
            # connection. Keeping it on the UE-state object would (a)
            # break "uniquely identifies" if any future InitialUEMessage
            # were to reuse it, and (b) leave send_initial_ue_message's
            # `if ue.ran_ue_ngap_id is None: self.attach_ue(ue)` guard
            # bypassed — so the gNB would send InitialUEMessage with a
            # released ID, and the AMF's per-association demux on
            # subsequent DownlinkNASTransport would never reach the UE.
            ue.ran_ue_ngap_id = None
            ue.amf_ue_ngap_id = None

    # ─── UE Management ───

    def attach_ue(self, ue):
        self._ran_ue_counter += 1
        ue.ran_ue_ngap_id = self._ran_ue_counter
        ue.gnb = self
        self.ue_map[ue.ran_ue_ngap_id] = ue
        self._log("UE attached: IMSI=%s RAN-UE-ID=%d", ue.imsi, ue.ran_ue_ngap_id)

    def detach_ue(self, ue):
        self.ue_map.pop(ue.ran_ue_ngap_id, None)

    def send_initial_ue_message(self, ue, nas_pdu):
        if ue.ran_ue_ngap_id is None:
            self.attach_ue(ue)
        data = NgapCodec.build_initial_ue_message(
            ue.ran_ue_ngap_id, nas_pdu, self.mcc, self.mnc, self.tac, self.gnb_id)
        self._trace_tx(data, "InitialUEMessage", 15)
        self._sctp.send(data, stream=self._stream_for_ue(ue.ran_ue_ngap_id))

    def send_uplink_nas_transport(self, ue, nas_pdu):
        if ue.amf_ue_ngap_id is None:
            self._log("WARN: AMF-UE-NGAP-ID not assigned for IMSI=%s", ue.imsi)
            return
        data = NgapCodec.build_uplink_nas_transport(
            ue.amf_ue_ngap_id, ue.ran_ue_ngap_id, nas_pdu,
            self.mcc, self.mnc, self.tac, self.gnb_id)
        self._trace_tx(data, "UplinkNASTransport", 46)
        self._sctp.send(data, stream=self._stream_for_ue(ue.ran_ue_ngap_id))

    # ─── Protocol Trace ───

    def _trace_tx(self, data, msg_type, proc_code):
        """Record outgoing NGAP message in protocol trace."""
        if not self._trace_enabled:
            return
        entry = {
            "dir": "TX", "time": time.time(),
            "proc": proc_code, "msg_type": msg_type,
            "size": len(data), "hex": data[:64].hex(),
        }
        self.protocol_trace.append(entry)
        if len(self.protocol_trace) > 200:
            self.protocol_trace = self.protocol_trace[-150:]
        log.debug("[%s] TX NGAP: %s (proc=%d, %d bytes)", self.gnb_name, msg_type, proc_code, len(data))

    def _trace_rx(self, data, category, proc_code, msg_type):
        """Record incoming NGAP message in protocol trace."""
        if not self._trace_enabled:
            return
        entry = {
            "dir": "RX", "time": time.time(),
            "proc": proc_code, "msg_type": msg_type,
            "category": category, "size": len(data),
            "hex": data[:64].hex(),
        }
        self.protocol_trace.append(entry)
        if len(self.protocol_trace) > 200:
            self.protocol_trace = self.protocol_trace[-150:]
        log.debug("[%s] RX NGAP: %s (proc=%d, %d bytes)", self.gnb_name, msg_type, proc_code, len(data))

    def get_trace(self, after_time=0):
        """Return protocol trace entries after given timestamp."""
        if after_time:
            return [e for e in self.protocol_trace if e["time"] > after_time]
        return list(self.protocol_trace)

    def clear_trace(self):
        """Clear the protocol trace buffer."""
        self.protocol_trace.clear()

    # ─── Serialization ───

    def to_dict(self):
        return {
            "gnb_name": self.gnb_name, "gnb_id": hex(self.gnb_id),
            "state": self.state, "amf": f"{self.amf_ip}:{self.amf_port}",
            "plmn": f"{self.mcc}/{self.mnc}", "tac": self.tac,
            "ue_count": len(self.ue_map),
            "ues": [u.imsi for u in self.ue_map.values()],
        }

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""SCTP client — kernel SCTP (Linux) via pysctp.

Uses pysctp's sctpsocket_tcp wrapper (one-to-one SCTP) so we can:
  - subscribe to SCTP_ASSOC_CHANGE / SHUTDOWN / SEND_FAILED / REMOTE_ERROR
    notifications and read them via sctp_recv(); a kernel-side teardown
    now produces a logged reason code (state=COMM_LOST + error code)
    instead of a silent socket.recv() hang.
  - keep using sendall()/connect()/bind() — pysctp's sctpsocket_tcp
    inherits from socket.socket so the rest of the API is unchanged.

Requires the kernel-tuning sysctl block from install.sh (which bumps
net.core.rmem_max/wmem_max to 16 MiB). On a stock kernel where
rmem_max=212 KB the setsockopt call below silently clamps to that
cap; the try/except below keeps the call a no-op on such hosts
instead of treating it as fatal.
"""

import sys
import socket
import struct
import threading
import queue
import logging

import sctp as pysctp  # python-sctp (pysctp 0.7.x)

log = logging.getLogger("tester.sctp")

IPPROTO_SCTP = 132

# TS 38.412 §7 — NGAP shall be carried with this SCTP Payload Protocol
# Identifier (assigned by IANA, referenced by §7 Transport layer). Sending
# PPID=0 (the socket-default) is a spec violation that strict AMFs may
# treat as malformed.
NGAP_PPID = 60

# Outbound SCTP streams to request in INIT. AMF will negotiate down if
# its inbound max is smaller. With 64 outbound + stream 0 reserved per
# TS 38.412 §7 for non-UE-associated procedures, we have 63 UE streams,
# enough to round-robin tens of thousands of UEs without head-of-line
# blocking pinning two unrelated UEs into the same stream.
NUM_OSTREAMS = 64

# From Linux's <linux/sctp.h>. pysctp's own constants are broken on at
# least one install (_sctp.getconstant("SPP_HB_DISABLED") returns 0)
# so sock.set_paddrparams(pp) with pp.flags=SPP_HB_DISABLE silently
# no-ops. We use raw setsockopt against IPPROTO_SCTP instead.
_SOL_SCTP = 132              # IPPROTO_SCTP (==SOL_SCTP on Linux)
_SCTP_PEER_ADDR_PARAMS = 9   # sockopt number — net/sctp/socket.c
_SPP_HB_ENABLE  = 0x01
_SPP_HB_DISABLE = 0x02
_SPP_HB_DEMAND  = 0x04
_SPP_PMTUD_ENABLE  = 0x08
_SPP_PMTUD_DISABLE = 0x10


def _console(msg):
    sys.stderr.write(msg + "\n")
    sys.stderr.flush()


# Notification → human-readable mapping (logged when the kernel signals a
# state change so silent teardowns become visible).
_ASSOC_STATE = {
    pysctp.assoc_change.state_COMM_UP: "COMM_UP",
    pysctp.assoc_change.state_COMM_LOST: "COMM_LOST",
    pysctp.assoc_change.state_RESTART: "RESTART",
    pysctp.assoc_change.state_SHUTDOWN_COMP: "SHUTDOWN_COMP",
    pysctp.assoc_change.state_CANT_STR_ASSOC: "CANT_START_ASSOC",
}
_ASSOC_ERR = {
    pysctp.assoc_change.error_FAILED_THRESHOLD: "FAILED_THRESHOLD",
    pysctp.assoc_change.error_HEARTBEAT_SUCCESS: "HEARTBEAT_SUCCESS",
    pysctp.assoc_change.error_INTERNAL_ERROR: "INTERNAL_ERROR",
    pysctp.assoc_change.error_PEER_FAULTY: "PEER_FAULTY",
    pysctp.assoc_change.error_RECEIVED_SACK: "RECEIVED_SACK",
    pysctp.assoc_change.error_RESPONSE_TO_USER_REQ: "RESPONSE_TO_USER_REQ",
    pysctp.assoc_change.error_SHUTDOWN_GUARD_EXPIRES: "SHUTDOWN_GUARD_EXPIRES",
}


class SctpClient:
    """Thread-safe SCTP client using kernel SCTP."""

    def __init__(self, recv_buf_size=65536):
        self._buf_size = recv_buf_size
        self._sock = None
        self._send_lock = threading.Lock()
        self.local_ip = None
        # Negotiated outbound stream count, learned at connect time via
        # SCTP_STATUS. Caller (gNB FSM) reads it to hash UEs onto streams
        # 1..(out_streams-1). Stream 0 is reserved for non-UE-associated
        # procedures per TS 38.412 §7.
        self.out_streams = 1
        # Send-worker: every NGAP PDU is enqueued and a single dedicated
        # daemon thread drains the queue with sendall(). Two reasons:
        # (1) The SCTP recv thread previously called sendall() inline from
        #     _on_ngap_recv → _handle_pdu_session_setup. A full kernel
        #     SCTP send buffer would block the recv thread, the kernel
        #     SCTP recv buffer would fill, and the AMF would ABORT.
        # (2) UE-actor sends and recv-thread sends used to fight over
        #     _send_lock, with the recv thread holding the loser.
        # The worker keeps both off the wire-event hot path.
        self._send_q: "queue.Queue[bytes | None]" = queue.Queue(maxsize=4096)
        self._send_thread = None
        self._send_failed = threading.Event()
        self._send_failure_cb = None  # set by gNB FSM via set_send_failure_cb()

    def connect(self, remote_ip, remote_port, timeout=5, source_ip=None):
        """Connect SCTP. Returns local IP. Raises on failure.

        Args:
            source_ip: Optional local IP to bind to. Used when multiple gNBs
                       need distinct source addresses (TS 23.501 §5.2.1).
        """
        # pysctp's sctpsocket_tcp gives us sctp_recv() + event subscription
        # while still being a socket.socket subclass (sendall/connect/etc work).
        self._sock = pysctp.sctpsocket_tcp(socket.AF_INET)
        self._sock.settimeout(timeout)
        # Bump requested outbound streams BEFORE connect so the INIT we send
        # advertises the higher count. AMF negotiates the actual outbound
        # to min(our request, AMF's max_instreams).
        try:
            self._sock.initparams.num_ostreams = NUM_OSTREAMS
            self._sock.initparams.flush()
        except Exception as e:
            log.warning("SCTP initparams.num_ostreams=%d failed: %s",
                        NUM_OSTREAMS, e)
        # Widen the per-socket buffers. install.sh sets
        # net.core.{r,w}mem_max = 16 MiB so the 8 MiB request below
        # actually sticks. On an untuned host the setsockopt silently
        # clamps to rmem_max — the assoc still works, just with a smaller
        # window, which at 10k UEs may re-introduce the stalls we're
        # avoiding. Log the effective buffer so operators can see if
        # their tuning didn't take.
        try:
            self._sock.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 8 * 1024 * 1024)
            self._sock.setsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF, 8 * 1024 * 1024)
        except OSError as e:
            log.warning("SCTP buffer tune failed: %s", e)
        _eff_rcv = self._sock.getsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF)
        _eff_snd = self._sock.getsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF)
        # Kernel reports doubled value (skbuff overhead). Anything below
        # 4 MiB *reported* (2 MiB usable) means rmem_max is unpatched.
        if _eff_rcv < 4 * 1024 * 1024:
            log.warning("SCTP SO_RCVBUF effective=%d — looks like "
                        "net.core.rmem_max wasn't tuned by install.sh; run "
                        "`sudo sysctl --system` or re-run install.sh",
                        _eff_rcv)
        # Subscribe to the notification stream so kernel-side teardowns
        # (COMM_LOST / SHUTDOWN_COMP / SEND_FAILED) become visible to the
        # recv loop instead of silent socket.recv() hangs.
        try:
            self._sock.events.clear()  # start from a known state, leave data_io on
            self._sock.events.association = True
            self._sock.events.shutdown = True
            self._sock.events.send_failure = True
            self._sock.events.peer_error = True
        except Exception as e:
            log.warning("SCTP event subscribe failed: %s", e)
        if source_ip:
            self._sock.bind((source_ip, 0))
        log.info("SCTP connecting to %s:%d ...", remote_ip, remote_port)
        _console(f"[SCTP] Connecting to {remote_ip}:{remote_port} ...")
        self._sock.connect((remote_ip, int(remote_port)))
        self._sock.settimeout(None)
        self.local_ip = self._sock.getsockname()[0]
        # Read negotiated outbound stream count. AMF may have lowered our
        # request of NUM_OSTREAMS to whatever its inbound supports.
        try:
            status = self._sock.get_status()
            self.out_streams = max(int(getattr(status, "outstrms", 0)), 1)
        except Exception as e:
            log.warning("SCTP get_status failed (out_streams stays at 1): %s", e)
            self.out_streams = 1
        log.info("SCTP connected (local=%s) — negotiated out_streams=%d",
                 self.local_ip, self.out_streams)
        _console(f"[SCTP] Connected (local={self.local_ip}) "
                 f"out_streams={self.out_streams}")
        # Multi-homing defence: if the peer advertised extra IPs in its
        # INIT-ACK (common with Go cores that bind to 0.0.0.0 inside
        # Docker — we've seen 172.17.0.1 and 10.45.0.1 sneak in), the
        # kernel adds them as peer paths and sends HEARTBEATs to them.
        # Those secondary paths are typically un-routable from the
        # tester, so their HBs fail, the kernel marks them down, and
        # worst case retransmits user data through them → instant
        # association teardown. Disable HB + mark path immediately
        # failed on every peer address that isn't the one we dialed.
        self._restrict_to_primary_path(remote_ip)
        # Start the send-worker now that the socket is live.
        self._send_failed.clear()
        # Drain anything stale (paranoia — _send_q should be empty here).
        while True:
            try:
                self._send_q.get_nowait()
            except queue.Empty:
                break
        self._send_thread = threading.Thread(
            target=self._send_loop, daemon=True, name="sctp-send")
        self._send_thread.start()
        return self.local_ip

    @staticmethod
    def _pack_paddrparams(ip_str: str, port: int, hbinterval: int,
                           pathmaxrxt: int, flags: int) -> bytes:
        """Pack a `struct sctp_paddrparams` matching Linux's layout.

          sctp_assoc_t spp_assoc_id;            s32, 4 bytes
          struct sockaddr_storage spp_address;  128 bytes
          __u32 spp_hbinterval;                 4 bytes
          __u16 spp_pathmaxrxt;                 2 bytes
          __u32 spp_pathmtu;                    4 bytes (2 pad bytes before)
          __u32 spp_sackdelay;                  4 bytes
          __u32 spp_flags;                      4 bytes
        = 152 bytes total. Kernel accepts the smaller layout (without
        spp_ipv6_flowlabel / spp_dscp extensions).
        """
        sa = bytearray(128)
        sa[0:2] = struct.pack('<H', socket.AF_INET)   # sin_family (host BO)
        sa[2:4] = struct.pack('>H', port)              # sin_port  (network BO)
        sa[4:8] = socket.inet_aton(ip_str)             # sin_addr  (network BO)
        return (struct.pack('=i', 0) + bytes(sa) +
                struct.pack('=IHHIII', hbinterval, pathmaxrxt, 0, 0, 0, flags))

    @staticmethod
    def _parse_paddrparams_flags(buf: bytes) -> int:
        """Return spp_flags from a paddrparams getsockopt result."""
        if len(buf) < 152:
            return -1
        return struct.unpack('=I', buf[148:152])[0]

    def _restrict_to_primary_path(self, primary_ip: str) -> None:
        """Disable HB + mark all non-primary peer paths immediately failed.

        Raw setsockopt(SCTP_PEER_ADDR_PARAMS) — we do NOT use pysctp's
        set_paddrparams wrapper because its flag constants are broken
        on some installs (SPP_HB_DISABLED literal == 0, making any
        flag-based request a silent no-op). Confirmed via tmp4.pcapng:
        HEARTBEATs to 10.45.0.1 still went out on a 30 s cadence even
        after the wrapper-based 'fix' ran.

        After setting, we read back via getsockopt and log the
        before/after flags so operators can verify the kernel accepted
        the disable.

        Purpose: Go cores running in Docker typically bind 0.0.0.0 and
        advertise every interface's IP in INIT-ACK — Docker bridge
        (172.17.0.1), UE-pool gateway (10.45.0.1), etc. Our kernel adds
        them as peer paths and heartbeats them, but they're unreachable
        from the tester namespace, so HB failures mount up, path gets
        marked failed, retransmitted user data may be routed through
        the dead path, and the whole association dies.
        """
        try:
            peers = self._sock.getpaddrs(0)
        except Exception as e:
            log.debug("getpaddrs failed, skipping multi-homing defence: %s", e)
            return
        if not peers:
            return
        others = [p for p in peers if p[0] != primary_ip]
        if not others:
            log.debug("SCTP peer advertised single address (%s) — "
                      "multi-homing defence not needed", primary_ip)
            return
        log.warning("SCTP peer advertised %d paths: primary=%s extra=%s "
                    "— disabling HB + pathmaxrxt=1 on extras (else HBs "
                    "blackhole and take the assoc down)",
                    len(peers), primary_ip, [p[0] for p in others])

        fd = self._sock.fileno()
        for addr in others:
            ip_str, port = addr[0], addr[1]
            # Diagnostic read-back of the current flags. CPython's
            # socket.getsockopt(level, optname[, buflen]) has no input-
            # buffer form, so the 4-arg shape below raises TypeError on
            # vanilla Python (only an exotic pysctp build with a custom
            # __getattr__ would route the bytes input into libc). Catch
            # both TypeError and OSError so a missing readback never
            # blocks the actual fix (the setsockopt that follows).
            try:
                before = self._sock.getsockopt(
                    _SOL_SCTP, _SCTP_PEER_ADDR_PARAMS,
                    self._pack_paddrparams(ip_str, port, 0, 0, 0), 152)
                before_flags = self._parse_paddrparams_flags(before)
            except (TypeError, OSError) as e:
                log.debug("SCTP get_paddrparams(%s) skipped: %s "
                          "(diagnostic only — setsockopt still runs)",
                          ip_str, e)
                before_flags = -1
            # Apply: HB disabled + pathmaxrxt=1
            try:
                self._sock.setsockopt(
                    _SOL_SCTP, _SCTP_PEER_ADDR_PARAMS,
                    self._pack_paddrparams(ip_str, port, 0, 1, _SPP_HB_DISABLE))
            except OSError as e:
                log.warning("SCTP set_paddrparams(%s) raw setsockopt failed: %s",
                            ip_str, e)
                continue
            # Read back to confirm — same caveat as the before-readback.
            try:
                after = self._sock.getsockopt(
                    _SOL_SCTP, _SCTP_PEER_ADDR_PARAMS,
                    self._pack_paddrparams(ip_str, port, 0, 0, 0), 152)
                after_flags = self._parse_paddrparams_flags(after)
            except (TypeError, OSError):
                after_flags = -1
            if after_flags < 0:
                log.info("SCTP path %-15s: setsockopt(HB_DISABLE) issued "
                         "(readback unsupported on this Python — assumed OK)",
                         ip_str)
            else:
                hb_on_after = bool(after_flags & _SPP_HB_ENABLE)
                hb_off_after = bool(after_flags & _SPP_HB_DISABLE)
                log.info("SCTP path %-15s: flags 0x%04x -> 0x%04x  "
                         "(HB now %s)", ip_str, before_flags, after_flags,
                         "OFF" if hb_off_after and not hb_on_after else
                         ("ON" if hb_on_after else "unclear"))

    def set_send_failure_cb(self, cb):
        """Called by the send-worker the first time sendall() fails.

        Lets the gNB FSM mark itself ERROR and unblock UE waiters. The
        callback runs on the send-worker thread — keep it short and
        non-blocking.
        """
        self._send_failure_cb = cb

    def disconnect(self):
        """Close the SCTP association gracefully (SHUTDOWN, not ABORT).

        Linux SCTP quirk: close() on a still-open socket-t sends ABORT, not
        SHUTDOWN. shutdown(SHUT_WR) starts the SHUTDOWN 3-way handshake but
        returns immediately; if close() fires before the peer's SHUTDOWN-ACK
        arrives, the kernel aborts the association to reclaim state. The AMF
        then logs the tester as "sending ABORT" even though we *meant* a
        clean close.

        Two defenses:
          1. Set SO_LINGER (on, 3s) so close() waits for the SHUTDOWN
             handshake to finish.
          2. After shutdown(SHUT_WR), drain a couple of reads so the kernel
             processes the peer's SHUTDOWN-ACK before we close.
        """
        sock, self._sock = self._sock, None
        self.local_ip = None
        # Stop the send-worker first so it can't call sendall() on a
        # half-closed socket. Sentinel = None.
        send_thread = self._send_thread
        self._send_thread = None
        if send_thread is not None and send_thread.is_alive():
            try:
                self._send_q.put_nowait(None)
            except queue.Full:
                pass
            send_thread.join(timeout=2.0)
        if not sock:
            return
        try:
            import struct as _struct
            # struct linger { int l_onoff; int l_linger; }
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER,
                            _struct.pack('ii', 1, 3))
        except Exception:
            pass
        try:
            sock.shutdown(socket.SHUT_WR)  # start graceful close
        except Exception:
            pass
        # Drain the peer's SHUTDOWN-ACK (or any last message) before close()
        # so the kernel transitions SHUTDOWN-SENT → CLOSED cleanly instead of
        # being forced to ABORT in close().
        try:
            sock.settimeout(0.5)
            for _ in range(6):  # up to ~3s
                try:
                    data = sock.recv(4096)
                    if not data:
                        break
                except socket.timeout:
                    break
                except Exception:
                    break
        except Exception:
            pass
        try:
            sock.close()
        except Exception:
            pass

    def send(self, data, stream=0, ppid=NGAP_PPID):
        """Enqueue an NGAP PDU for the send-worker to deliver.

        Non-blocking — returns as soon as the work is queued, which is what
        the SCTP recv thread needs so it can keep draining the kernel
        SCTP recv buffer.

        Args:
            data:    raw NGAP PDU bytes.
            stream:  SCTP outbound stream id (TS 38.412 §7).
                     0 for non-UE-associated procedures (NG Setup, NG Reset,
                     AMF Configuration Update reply, …); 1..(out_streams-1)
                     for UE-associated, hashed by the caller from
                     RAN-UE-NGAP-ID. Caller is responsible for clamping
                     to self.out_streams; we do not silently truncate.
            ppid:    SCTP Payload Protocol Identifier. Defaults to 60
                     (NGAP) per TS 38.412 §7; only NG Setup-time setups
                     would override.
        """
        if self._sock is None:
            raise ConnectionError("SCTP not connected")
        if self._send_failed.is_set():
            raise ConnectionError("SCTP send-worker has failed; association is dead")
        if stream >= self.out_streams:
            log.warning("SCTP send: requested stream=%d >= negotiated out=%d "
                        "— wrapping to stream 0 (non-UE)",
                        stream, self.out_streams)
            stream = 0
        try:
            self._send_q.put((data, stream, ppid), timeout=2.0)
        except queue.Full:
            raise ConnectionError("SCTP send queue full (worker stalled)")

    def _send_loop(self):
        """Drain self._send_q; call sctp_send(); on error notify and exit."""
        while True:
            try:
                item = self._send_q.get()
            except Exception:
                continue
            if item is None:
                return
            data, stream, ppid = item
            sock = self._sock
            if sock is None:
                return
            try:
                with self._send_lock:
                    # sctp_send carries PPID + stream as ancillary cmsg —
                    # this is the spec-correct path. sendall() drops both
                    # to socket defaults (PPID=0, stream=0).
                    sock.sctp_send(data, ppid=ppid, stream=stream)
            except (BrokenPipeError, ConnectionResetError, OSError) as e:
                self._send_failed.set()
                log.error("SCTP send failed: %s — association is dead", e)
                cb = self._send_failure_cb
                if cb is not None:
                    try:
                        cb(e)
                    except Exception as cb_e:
                        log.debug("send_failure_cb raised: %s", cb_e)
                return

    def recv_loop(self, callback, stop_event):
        """Blocking receive loop. Calls callback(data) for each PDU."""
        if self._sock:
            self._kernel_recv_loop(callback, stop_event)
        else:
            raise ConnectionError("SCTP not connected")

    @property
    def connected(self):
        return self._sock is not None

    def _kernel_recv_loop(self, callback, stop_event):
        """Blocking receive loop using pysctp.sctp_recv().

        Returns (fromaddr, flags, msg, notif). When FLAG_NOTIFICATION is
        set we got an event (COMM_LOST/SHUTDOWN/SEND_FAILED/REMOTE_ERROR
        etc.) instead of data. We log it with the kernel-supplied state
        + error code and exit the loop on terminal events — that's the
        whole point: silent kernel-side teardowns now produce a single
        log line you can grep for instead of a 14 s timeout.
        """
        while not stop_event.is_set():
            try:
                fromaddr, flags, msg, notif = self._sock.sctp_recv(self._buf_size)
            except socket.timeout:
                continue
            except OSError as e:
                if not stop_event.is_set():
                    log.error("SCTP recv error: %s", e)
                break

            if flags & pysctp.FLAG_NOTIFICATION:
                terminal = self._handle_notification(notif)
                if terminal:
                    # Wake up any send() callers stuck behind a dead socket.
                    self._send_failed.set()
                    cb = self._send_failure_cb
                    if cb is not None:
                        try:
                            cb(ConnectionResetError("SCTP assoc terminated by kernel"))
                        except Exception as cb_e:
                            log.debug("send_failure_cb raised: %s", cb_e)
                    break
                continue

            if not msg:
                # Empty data + no notification = peer closed (TCP-style).
                log.info("SCTP connection closed by remote")
                break
            try:
                callback(msg)
            except Exception as e:
                # Per-PDU failure shouldn't kill the recv loop.
                log.error("SCTP recv callback raised: %s", e, exc_info=True)

    def _handle_notification(self, notif) -> bool:
        """Log an SCTP notification. Return True if the assoc is gone."""
        ntype = getattr(notif, "type", None)
        if isinstance(notif, pysctp.assoc_change):
            state = _ASSOC_STATE.get(notif.state, f"state={notif.state}")
            err = _ASSOC_ERR.get(notif.error, f"error={notif.error}")
            log.warning("SCTP ASSOC_CHANGE: %s (%s) — assoc_id=%d",
                        state, err, getattr(notif, "assoc_id", -1))
            return notif.state in (
                pysctp.assoc_change.state_COMM_LOST,
                pysctp.assoc_change.state_SHUTDOWN_COMP,
                pysctp.assoc_change.state_CANT_STR_ASSOC,
            )
        if isinstance(notif, pysctp.shutdown_event):
            log.warning("SCTP SHUTDOWN_EVENT — peer initiated shutdown")
            return True
        if isinstance(notif, pysctp.send_failed):
            log.error("SCTP SEND_FAILED — error=%d flags=0x%x len=%d",
                      getattr(notif, "error", -1),
                      getattr(notif, "flags", 0),
                      getattr(notif, "length", 0))
            return False
        if isinstance(notif, pysctp.remote_error):
            log.error("SCTP REMOTE_ERROR — peer reported err=%d",
                      getattr(notif, "error", -1))
            return False
        log.warning("SCTP notification type=%s (unhandled)", ntype)
        return False

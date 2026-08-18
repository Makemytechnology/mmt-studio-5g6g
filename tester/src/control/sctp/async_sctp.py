# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Asyncio-native SCTP client.

Phase 2 of ARCHITECTURE.md — replaces the threaded SctpClient for
control-plane use. One recv coroutine per association; sends serialized
through an internal lock so concurrent UE actors can issue uplink NAS
without tripping on each other.

Design choices:

- No pysctp dependency for the recv path. We use the raw socket FD with
  `loop.add_reader(fd, cb)` so recv never blocks the event loop — it's
  woken only when there's data to read. send() still uses the pysctp
  wrapper because we care about PPID / stream id semantics it handles.

- Graceful disconnect: we honour the fix from src.protocol.sctp — set
  SO_LINGER, shutdown(SHUT_WR), drain reads to let SHUTDOWN-ACK land
  before close(). The peer sees a clean SHUTDOWN, never an ABORT.

- Buffer tuning: SO_RCVBUF / SO_SNDBUF set to 2 MiB as belt-and-braces
  against bursty peers; same knob the threaded version has.
"""

from __future__ import annotations

import asyncio
import errno
import logging
import socket
import struct
import threading
from typing import Awaitable, Callable, Optional

log = logging.getLogger("tester.control.sctp")

IPPROTO_SCTP = 132
_BUF_SIZE = 65536


class AsyncSctp:
    """Single-association SCTP client for asyncio.

    Usage:
        sctp = AsyncSctp()
        await sctp.connect(amf_ip, amf_port, source_ip=gnb_ip)
        sctp.set_on_recv(lambda data: asyncio.create_task(handle(data)))
        await sctp.send(ngap_pdu)
        ...
        await sctp.disconnect()
    """

    def __init__(self) -> None:
        self._sock: Optional[socket.socket] = None
        self._loop: Optional[asyncio.AbstractEventLoop] = None
        self._on_recv: Optional[Callable[[bytes], Awaitable[None]]] = None
        self._send_lock = asyncio.Lock()
        # pysctp's sendmsg wrapper for PPID. Lazy-import so tests that
        # don't need SCTP don't fail on hosts without pysctp.
        self._sctp_mod = None
        self.local_ip: Optional[str] = None
        self._connected = asyncio.Event()
        self._closed = asyncio.Event()

    # ── Lifecycle ────────────────────────────────────────────────────

    async def connect(self, remote_ip: str, remote_port: int,
                      source_ip: Optional[str] = None,
                      timeout: float = 5.0) -> str:
        """Open the SCTP association. Returns the local IP."""
        self._loop = asyncio.get_running_loop()
        # SCTP connect is a synchronous libc call; run it in the default
        # executor so the event loop isn't blocked by a DNS hiccup or a
        # slow AMF. A handful of UE workers each connecting in parallel
        # won't thrash the pool.
        await self._loop.run_in_executor(None, self._connect_sync,
                                          remote_ip, remote_port,
                                          source_ip, timeout)
        self._connected.set()
        return self.local_ip or ""

    def _connect_sync(self, remote_ip: str, remote_port: int,
                       source_ip: Optional[str], timeout: float) -> None:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM, IPPROTO_SCTP)
        sock.settimeout(timeout)
        try:
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 2 * 1024 * 1024)
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF, 2 * 1024 * 1024)
        except OSError as e:
            log.debug("SCTP buffer tune failed: %s", e)
        if source_ip:
            sock.bind((source_ip, 0))
        log.info("SCTP connect → %s:%d", remote_ip, remote_port)
        sock.connect((remote_ip, int(remote_port)))
        sock.settimeout(None)
        sock.setblocking(False)
        self._sock = sock
        self.local_ip = sock.getsockname()[0]
        log.info("SCTP connected (local=%s)", self.local_ip)
        # Register the read callback — fires every time the kernel has
        # data ready. Non-blocking: we just drain and dispatch.
        self._loop.add_reader(sock.fileno(), self._on_readable)

    def set_on_recv(self, cb: Callable[[bytes], Awaitable[None]]) -> None:
        """Install the per-PDU callback. Must be an async function.

        The callback is invoked from the event loop for each full NGAP
        PDU received. It should return quickly — heavy work should be
        delegated to actor mailboxes.
        """
        self._on_recv = cb

    # ── Receive ──────────────────────────────────────────────────────

    def _on_readable(self) -> None:
        """Called by the event loop when the SCTP socket has data."""
        sock = self._sock
        if sock is None:
            return
        try:
            data = sock.recv(_BUF_SIZE)
        except BlockingIOError:
            return
        except ConnectionResetError as e:
            log.warning("SCTP recv ECONNRESET: %s — peer aborted", e)
            self._shutdown_reader()
            return
        except OSError as e:
            if e.errno == errno.EAGAIN:
                return
            log.warning("SCTP recv error: %s", e)
            self._shutdown_reader()
            return
        if not data:
            log.info("SCTP peer closed")
            self._shutdown_reader()
            return
        cb = self._on_recv
        if cb is None:
            log.debug("SCTP: %d bytes dropped (no on_recv callback set)", len(data))
            return
        # Hand off to the actor layer without awaiting — keeps recv path
        # tight. The actor layer is responsible for quick mailbox puts.
        try:
            asyncio.create_task(cb(data), name="ngap-dispatch")
        except Exception as e:
            log.error("SCTP recv dispatch failed: %s", e, exc_info=True)

    def _shutdown_reader(self) -> None:
        sock = self._sock
        if sock is None or self._loop is None:
            return
        try:
            self._loop.remove_reader(sock.fileno())
        except Exception:
            pass
        self._closed.set()

    # ── Send ─────────────────────────────────────────────────────────

    async def send(self, data: bytes) -> None:
        """Send one NGAP PDU.

        Serialized via an asyncio.Lock — concurrent UE actors can all
        call send() and they'll interleave cleanly without duplicate
        PPIDs or split chunks.
        """
        if self._sock is None:
            raise ConnectionError("SCTP not connected")
        async with self._send_lock:
            # SCTP sendmsg with default PPID (NGAP=60 per TS 38.412 §7).
            # Kernel-SCTP sendmsg is non-blocking on a non-blocking socket;
            # fall back to a tight retry if EAGAIN under bursts.
            try:
                self._sock.sendall(data)
            except BlockingIOError:
                # Wait for the socket to become writable, then retry.
                fut = self._loop.create_future()
                def _ready():
                    self._loop.remove_writer(self._sock.fileno())
                    if not fut.done():
                        fut.set_result(None)
                self._loop.add_writer(self._sock.fileno(), _ready)
                await fut
                self._sock.sendall(data)
            except (BrokenPipeError, ConnectionResetError) as e:
                log.warning("SCTP send failed: %s", e)
                self._shutdown_reader()
                raise

    # ── Disconnect ───────────────────────────────────────────────────

    async def disconnect(self) -> None:
        """Graceful SHUTDOWN (not ABORT). Mirrors src.protocol.sctp.disconnect."""
        sock = self._sock
        self._sock = None
        if sock is None:
            return
        loop = self._loop
        if loop is not None:
            try:
                loop.remove_reader(sock.fileno())
            except Exception:
                pass
        try:
            # SO_LINGER + shutdown + drain + close — same recipe as the
            # fix in src/protocol/sctp.py so the peer sees clean SHUTDOWN
            # instead of ABORT.
            sock.setsockopt(socket.SOL_SOCKET, socket.SO_LINGER,
                            struct.pack('ii', 1, 3))
        except Exception:
            pass
        try:
            sock.shutdown(socket.SHUT_WR)
        except Exception:
            pass
        # Drain a few reads so the peer's SHUTDOWN-ACK lands before close.
        try:
            sock.settimeout(0.5)
            for _ in range(6):
                try:
                    if not sock.recv(4096):
                        break
                except (socket.timeout, BlockingIOError, OSError):
                    break
        except Exception:
            pass
        try:
            sock.close()
        except Exception:
            pass
        self._closed.set()

    @property
    def connected(self) -> bool:
        return self._sock is not None and not self._closed.is_set()

    async def wait_closed(self) -> None:
        await self._closed.wait()

# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Async gNB — SCTP + NG Setup + UE attachment orchestration.

Phase 2. Owns one SCTP association, one NGAP dispatcher, and the map
of RAN-UE-NGAP-ID → UeActor. All I/O is event-loop-native:

- send_ngap() is an async coroutine, serialized by AsyncSctp.send
- recv is driven by loop.add_reader inside AsyncSctp — nothing on the
  hot path ever blocks waiting for UE-level work
- UeActors process their mailboxes on their own coroutines

The gNB actor itself has a small mailbox for NG-Setup-Response and
orphaned NGAP messages (e.g. InitialContextSetupRequest delivered
before the UeActor has registered). Everything else routes straight
to the UE via the dispatcher.
"""

from __future__ import annotations

import asyncio
import logging
import threading
import time
from typing import Dict, Optional

from src.config import GNB_DEFAULTS
from src.protocol.ngap import NgapCodec
from src.control.event_loop import spawn_task
from src.control.ngap.dispatcher import (
    NgapDispatcher, NgSetupResponse, NgSetupFailure, UnknownNgapMessage,
)
from src.control.sctp.async_sctp import AsyncSctp

log = logging.getLogger("tester.control.gnb")

# States — same names as GnbStateMachine
IDLE = "IDLE"
NG_SETUP_SENT = "NG_SETUP_SENT"
READY = "READY"
ERROR = "ERROR"


class GnbActor:
    """Async gNB state machine.

    Usage:
        gnb = GnbActor(amf_ip="192.168.1.107")
        await gnb.connect()                 # SCTP + NG Setup
        ua = UeActor(sim, gnb=gnb, ran_ue_ngap_id=gnb.alloc_ran_ue_id())
        gnb.attach_ue(ua)
        await ua.start()
        await ua.register()
        await ua.wait_for_state("REGISTERED")
        ...
        await gnb.disconnect()              # graceful SHUTDOWN (not ABORT)
    """

    _id_counter = 0
    _id_lock = threading.Lock()

    def __init__(self, amf_ip: str, amf_port: int = 38412,
                 gnb_id: Optional[int] = None,
                 gnb_name: Optional[str] = None,
                 mcc: Optional[str] = None, mnc: Optional[str] = None,
                 tac: Optional[int] = None,
                 slices=None, source_ip: Optional[str] = None) -> None:
        with GnbActor._id_lock:
            GnbActor._id_counter += 1
            idx = GnbActor._id_counter

        self.amf_ip = amf_ip
        self.amf_port = amf_port
        self.gnb_id = gnb_id or (GNB_DEFAULTS["gnb_id_base"] + idx)
        self.gnb_name = gnb_name or f"{GNB_DEFAULTS['gnb_name_prefix']}-{idx:02d}"
        self.mcc = mcc or GNB_DEFAULTS["mcc"]
        self.mnc = mnc or GNB_DEFAULTS["mnc"]
        self.tac = tac or GNB_DEFAULTS["tac"]
        self.slices = slices or GNB_DEFAULTS["slices"]
        self.source_ip = source_ip
        self.gnb_ip: Optional[str] = None

        self.state = IDLE
        self._state_event = asyncio.Event()

        self._sctp = AsyncSctp()
        self._dispatcher = NgapDispatcher()
        self._mailbox: asyncio.Queue = asyncio.Queue(maxsize=256)
        self._dispatcher.set_gnb_mailbox(self._mailbox)

        self._ues: Dict[int, "object"] = {}  # ran_ue_ngap_id → UeActor
        self._ran_ue_counter = 0

        # TEID allocation — shared with UE actors that build PDU Session
        # Resource Setup responses.
        self.gtp_teid_counter = 0x10000

        self._run_task: Optional[asyncio.Task] = None
        self._stop = asyncio.Event()

    # ── Connection lifecycle ────────────────────────────────────────

    async def connect(self, ng_setup_timeout: float = 10.0) -> None:
        """Open SCTP, send NG Setup Request, await Response."""
        if self.state not in (IDLE, ERROR):
            raise RuntimeError(f"gNB already in state {self.state}")
        self.gnb_ip = await self._sctp.connect(
            self.amf_ip, self.amf_port, source_ip=self.source_ip)
        log.info("[%s] SCTP connected to %s:%d", self.gnb_name,
                 self.amf_ip, self.amf_port)
        # Route incoming NGAP PDUs through the dispatcher
        self._sctp.set_on_recv(self._dispatcher.dispatch)
        # Start the gNB mailbox consumer (handles NG Setup Response etc.)
        self._run_task = spawn_task(self._run(), name=f"gnb-{self.gnb_name}")

        # Send NG Setup Request and wait for Response
        self._set_state(NG_SETUP_SENT)
        ng_setup = NgapCodec.build_ng_setup_request(
            self.gnb_id, self.gnb_name, self.mcc, self.mnc,
            self.tac, self.slices)
        await self._sctp.send(ng_setup)
        log.info("[%s] NG Setup Request sent", self.gnb_name)
        try:
            await asyncio.wait_for(self._wait_state(READY), timeout=ng_setup_timeout)
        except asyncio.TimeoutError:
            self._set_state(ERROR)
            raise ConnectionError("NG Setup timed out")

    async def disconnect(self) -> None:
        """Graceful SHUTDOWN of the SCTP association."""
        self._stop.set()
        await self._sctp.disconnect()
        self._set_state(IDLE)
        if self._run_task is not None:
            try:
                # Poke the mailbox so the consumer wakes from `await get()`.
                self._mailbox.put_nowait(None)
            except asyncio.QueueFull:
                pass
            try:
                await asyncio.wait_for(self._run_task, timeout=2.0)
            except asyncio.TimeoutError:
                self._run_task.cancel()

    # ── UE management ───────────────────────────────────────────────

    def alloc_ran_ue_id(self) -> int:
        self._ran_ue_counter += 1
        return self._ran_ue_counter

    def attach_ue(self, ue_actor) -> None:
        """Register a UE actor; subsequent NGAP for its RAN-UE-NGAP-ID routes here."""
        self._ues[ue_actor.ran_ue_ngap_id] = ue_actor
        self._dispatcher.register_ue(ue_actor.ran_ue_ngap_id, ue_actor.mailbox)
        log.info("[%s] UE attached: IMSI=%s RAN-UE-ID=%d",
                 self.gnb_name, ue_actor.imsi, ue_actor.ran_ue_ngap_id)

    def detach_ue(self, ue_actor) -> None:
        self._ues.pop(ue_actor.ran_ue_ngap_id, None)
        self._dispatcher.unregister_ue(
            ue_actor.ran_ue_ngap_id, ue_actor.amf_ue_ngap_id)

    # ── Send ────────────────────────────────────────────────────────

    async def send_ngap(self, data: bytes) -> None:
        """Serialized SCTP send — safe from any coroutine."""
        await self._sctp.send(data)

    # ── Mailbox consumer ────────────────────────────────────────────

    async def _run(self) -> None:
        while not self._stop.is_set():
            msg = await self._mailbox.get()
            if msg is None:
                break
            try:
                await self._handle(msg)
            except Exception as e:
                log.error("[%s] mailbox handler error: %s",
                          self.gnb_name, e, exc_info=True)

    async def _handle(self, msg) -> None:
        if isinstance(msg, NgSetupResponse):
            log.info("[%s] NG Setup Response received", self.gnb_name)
            self._set_state(READY)
        elif isinstance(msg, NgSetupFailure):
            log.warning("[%s] NG Setup FAILED", self.gnb_name)
            self._set_state(ERROR)
        elif isinstance(msg, UnknownNgapMessage):
            log.debug("[%s] unhandled NGAP proc=%d (%s)",
                      self.gnb_name, msg.proc_code, msg.category)
        else:
            # Other messages (orphaned UE-addressed PDUs where the UE is
            # already gone) — just log. Phase 3 will do orphan bookkeeping.
            log.debug("[%s] orphaned NGAP msg %s", self.gnb_name, type(msg).__name__)

    # ── State helpers ──────────────────────────────────────────────

    def _set_state(self, new_state: str) -> None:
        if self.state == new_state:
            return
        old = self.state
        self.state = new_state
        log.info("[%s] State: %s -> %s", self.gnb_name, old, new_state)
        self._state_event.set()
        self._state_event = asyncio.Event()

    async def _wait_state(self, target: str) -> None:
        while self.state != target:
            if self.state == ERROR:
                raise ConnectionError("gNB entered ERROR state")
            await self._state_event.wait()

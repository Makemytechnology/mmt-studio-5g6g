# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Decode NGAP PDUs and route them to the correct actor's mailbox.

Phase 2. Single coroutine reads SCTP → NgapDispatcher.dispatch() → UE
actor mailbox. The dispatcher is the only place that touches the full
NGAP registry; actors see typed message dataclasses.

Routing rules (TS 38.413):

- `successfulOutcome` / `unsuccessfulOutcome` of proc 21 (NG Setup)
  → gNB actor
- `initiatingMessage` of proc 4 (DownlinkNASTransport)
  → look up AMF-UE-NGAP-ID + RAN-UE-NGAP-ID, route to UE actor
- `initiatingMessage` of proc 14 (InitialContextSetupRequest)
  → route to UE actor by RAN-UE-NGAP-ID
- `initiatingMessage` of proc 29 (PDUSessionResourceSetupRequest)
  → route to UE actor; gNB actor also inspects for UPF TEID
- `initiatingMessage` of proc 41 (UEContextReleaseCommand)
  → route to UE actor
- `initiatingMessage` of proc 26 (PDUSessionResourceModifyRequest)
  → route to UE actor

The dispatcher does NOT own any state machine transitions — it only
translates bytes into actor-bound messages. This keeps the hot path
small and the actor boundary clean.
"""

from __future__ import annotations

import asyncio
import logging
from dataclasses import dataclass
from typing import Any, Dict, Optional

from src.protocol.ngap import NgapCodec

log = logging.getLogger("tester.control.dispatcher")


# ── Typed messages delivered to actors ─────────────────────────────────

@dataclass
class NgSetupResponse:
    ies: Dict[str, Any]


@dataclass
class NgSetupFailure:
    ies: Dict[str, Any]


@dataclass
class DownlinkNas:
    """Proc 4 — DL NAS Transport. Payload goes to per-UE actor."""
    amf_ue_ngap_id: int
    ran_ue_ngap_id: int
    nas_pdu: bytes
    raw_ies: Dict[str, Any]


@dataclass
class InitialContextSetup:
    """Proc 14 — from AMF. UE actor responds with setup-response."""
    amf_ue_ngap_id: int
    ran_ue_ngap_id: int
    nas_pdu: Optional[bytes]
    raw_ies: Dict[str, Any]


@dataclass
class PduSessionResourceSetup:
    """Proc 29 — contains UPF tunnel info + NAS PDU for UE."""
    amf_ue_ngap_id: int
    ran_ue_ngap_id: int
    raw_ies: Dict[str, Any]


@dataclass
class UeContextRelease:
    amf_ue_ngap_id: int
    ran_ue_ngap_id: int
    raw_ies: Dict[str, Any]


@dataclass
class PduSessionResourceModify:
    amf_ue_ngap_id: int
    ran_ue_ngap_id: int
    raw_ies: Dict[str, Any]


@dataclass
class UnknownNgapMessage:
    category: str
    proc_code: int
    raw_bytes_len: int


# ── Dispatcher ────────────────────────────────────────────────────────

class NgapDispatcher:
    """Route decoded NGAP PDUs to actors.

    The gNB actor registers itself to receive NG Setup Response / Failure.
    UE actors register themselves (by RAN-UE-NGAP-ID) before any
    InitialUEMessage is sent; once the AMF assigns an AMF-UE-NGAP-ID, the
    dispatcher back-fills that lookup from the first DownlinkNASTransport.
    """

    def __init__(self) -> None:
        self._gnb_mailbox: Optional[asyncio.Queue] = None
        self._ue_by_ran: Dict[int, asyncio.Queue] = {}
        self._ue_by_amf: Dict[int, asyncio.Queue] = {}
        self._stats = {"dispatched": 0, "dropped": 0, "decode_err": 0}

    # ── Registration ────────────────────────────────────────────────

    def set_gnb_mailbox(self, mailbox: asyncio.Queue) -> None:
        self._gnb_mailbox = mailbox

    def register_ue(self, ran_ue_ngap_id: int, mailbox: asyncio.Queue) -> None:
        self._ue_by_ran[ran_ue_ngap_id] = mailbox

    def associate_amf_ue_id(self, ran_ue_ngap_id: int,
                             amf_ue_ngap_id: int) -> None:
        """Called by the UE actor once it learns its AMF-UE-NGAP-ID."""
        mbox = self._ue_by_ran.get(ran_ue_ngap_id)
        if mbox is not None:
            self._ue_by_amf[amf_ue_ngap_id] = mbox

    def unregister_ue(self, ran_ue_ngap_id: int,
                       amf_ue_ngap_id: Optional[int] = None) -> None:
        self._ue_by_ran.pop(ran_ue_ngap_id, None)
        if amf_ue_ngap_id is not None:
            self._ue_by_amf.pop(amf_ue_ngap_id, None)

    def stats(self) -> Dict[str, int]:
        return dict(self._stats)

    # ── Dispatch ────────────────────────────────────────────────────

    async def dispatch(self, data: bytes) -> None:
        """Decode one NGAP PDU from the wire and route the resulting message.

        Never blocks — actors receive messages through `put_nowait` with
        a drop-oldest policy to prevent any single actor from stalling
        the pipeline.
        """
        try:
            category, proc_code, ies = NgapCodec.decode(data)
        except Exception as e:
            self._stats["decode_err"] += 1
            log.warning("NGAP decode failed (%d bytes): %s", len(data), e)
            return

        msg = self._classify(category, proc_code, ies)
        target = self._route(msg, ies)
        if target is None:
            self._stats["dropped"] += 1
            log.debug("NGAP %s proc=%d: no route", category, proc_code)
            return
        self._deliver(target, msg)
        self._stats["dispatched"] += 1

    # ── Internals ───────────────────────────────────────────────────

    def _classify(self, category: str, proc_code: int, ies: Dict[str, Any]):
        if category == "successfulOutcome" and proc_code == 21:
            return NgSetupResponse(ies=ies)
        if category == "unsuccessfulOutcome" and proc_code == 21:
            return NgSetupFailure(ies=ies)
        if category != "initiatingMessage":
            return UnknownNgapMessage(category=category, proc_code=proc_code,
                                       raw_bytes_len=0)

        amf_id = ies.get(10)  # AMF-UE-NGAP-ID
        ran_id = ies.get(85)  # RAN-UE-NGAP-ID
        if proc_code == 4:
            nas = ies.get(38)  # NAS-PDU
            return DownlinkNas(amf_ue_ngap_id=amf_id, ran_ue_ngap_id=ran_id,
                                nas_pdu=bytes(nas) if nas else b"",
                                raw_ies=ies)
        if proc_code == 14:
            nas = ies.get(38)
            return InitialContextSetup(amf_ue_ngap_id=amf_id,
                                        ran_ue_ngap_id=ran_id,
                                        nas_pdu=bytes(nas) if nas else None,
                                        raw_ies=ies)
        if proc_code == 29:
            return PduSessionResourceSetup(amf_ue_ngap_id=amf_id,
                                            ran_ue_ngap_id=ran_id,
                                            raw_ies=ies)
        if proc_code == 41:
            return UeContextRelease(amf_ue_ngap_id=amf_id,
                                     ran_ue_ngap_id=ran_id, raw_ies=ies)
        if proc_code == 26:
            return PduSessionResourceModify(amf_ue_ngap_id=amf_id,
                                             ran_ue_ngap_id=ran_id, raw_ies=ies)
        return UnknownNgapMessage(category=category, proc_code=proc_code,
                                    raw_bytes_len=0)

    def _route(self, msg, ies) -> Optional[asyncio.Queue]:
        if isinstance(msg, (NgSetupResponse, NgSetupFailure)):
            return self._gnb_mailbox
        if isinstance(msg, UnknownNgapMessage):
            return self._gnb_mailbox  # gNB gets the unhandled fallback
        # UE-targeted messages
        ran_id = getattr(msg, "ran_ue_ngap_id", None)
        amf_id = getattr(msg, "amf_ue_ngap_id", None)
        if ran_id is not None and ran_id in self._ue_by_ran:
            # First message after InitialUEMessage — cache the AMF-UE-NGAP-ID
            # so later messages that come in with only AMF-UE-NGAP-ID still route.
            if amf_id is not None and amf_id not in self._ue_by_amf:
                self._ue_by_amf[amf_id] = self._ue_by_ran[ran_id]
            return self._ue_by_ran[ran_id]
        if amf_id is not None and amf_id in self._ue_by_amf:
            return self._ue_by_amf[amf_id]
        # Fallback: gNB actor may want to log / inspect orphaned messages
        return self._gnb_mailbox

    def _deliver(self, mailbox: asyncio.Queue, msg) -> None:
        try:
            mailbox.put_nowait(msg)
            return
        except asyncio.QueueFull:
            # Drop-oldest policy — per the backpressure rule in ARCHITECTURE.md.
            try:
                mailbox.get_nowait()
            except asyncio.QueueEmpty:
                pass
            try:
                mailbox.put_nowait(msg)
            except asyncio.QueueFull:
                self._stats["dropped"] += 1
                log.warning("actor mailbox still full after drop-oldest")

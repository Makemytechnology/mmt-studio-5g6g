# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Async UE FSM — actor model: one coroutine per UE with its own mailbox.

Phase 2 of ARCHITECTURE.md. Replaces threaded UeStateMachine for the
registration flow. All NAS parsing, 5G crypto, and message builders are
reused verbatim from src/protocol/ — this module owns only the
event-loop wiring, state transitions, and mailbox consumption.

Only the per-UE _run() coroutine touches self.state / self.security_ctx,
so no locks are needed. Tens of thousands of UeActors can coexist in a
single event loop; each is a few hundred bytes of Python state plus a
tiny asyncio.Queue mailbox.

Flow implemented in this phase (matches UeStateMachine semantics):

    DEREGISTERED
        → register()             # send InitialUEMessage
    REG_INITIATED
        → DL NAS: Auth Request   → TX Auth Response
    AUTH_PENDING
        → DL NAS: Sec Mode Cmd   → TX Security Mode Complete
    SEC_MODE_PENDING
        → Initial Context Setup Request → TX response
        → DL NAS: Registration Accept  → TX Registration Complete
    REGISTERED

PDU session establishment + traffic are stubbed — the actor ACKs a
PDUSessionResourceSetupRequest at the NGAP layer so the AMF doesn't
time out, but the GTP-U tunnel creation is left for Phase 3.
"""

from __future__ import annotations

import asyncio
import logging
import time
from binascii import hexlify
from typing import Optional

from src.config import UE_DEFAULTS
from src.protocol.nas import NasBuilder, NasParser
from src.protocol.nas_security import wrap_nas_security, unwrap_nas_security
from src.protocol.crypto import (
    ue_authenticate, get_snn, derive_kamf, derive_nas_keys, derive_kgnb,
)
from src.protocol.ngap import NgapCodec
from src.control.event_loop import spawn_task
from src.control.ngap.dispatcher import (
    DownlinkNas, InitialContextSetup, PduSessionResourceSetup,
    UeContextRelease, PduSessionResourceModify,
)

log = logging.getLogger("tester.control.ue")

# States — same strings as UeStateMachine so downstream assertions match.
DEREGISTERED = "DEREGISTERED"
REG_INITIATED = "REG_INITIATED"
AUTH_PENDING = "AUTH_PENDING"
SEC_MODE_PENDING = "SEC_MODE_PENDING"
REGISTERED = "REGISTERED"
DEREGISTERING = "DEREGISTERING"
ERROR = "ERROR"


class UeActor:
    """Async UE state machine — one asyncio task per UE.

    Lifecycle:
        ua = UeActor(sim, gnb=gnb_actor, ran_ue_ngap_id=gnb.alloc_ran_ue_id())
        gnb.attach_ue(ua)
        await ua.start()
        await ua.register()
        ok = await ua.wait_for_state(REGISTERED, timeout=15)
        ...
        await ua.stop()
    """

    def __init__(self, sim, gnb, ran_ue_ngap_id: int,
                 mailbox_size: int = 64) -> None:
        self.sim = sim
        self.imsi = sim.imsi
        self.mcc = sim.imsi[:3]
        self.mnc = sim.imsi[3:5]
        self.gnb = gnb
        self.ran_ue_ngap_id = ran_ue_ngap_id
        self.amf_ue_ngap_id: Optional[int] = None

        self.state = DEREGISTERED
        self._state_event = asyncio.Event()
        self._mailbox: asyncio.Queue = asyncio.Queue(maxsize=mailbox_size)
        self._task: Optional[asyncio.Task] = None
        self._stop = asyncio.Event()
        self.last_error: Optional[str] = None

        # Security / identity context — same layout as UeStateMachine.
        self.security_ctx = {
            "KAMF": None, "KSEAF": None,
            "knasenc": None, "knasint": None, "kgnb": None,
            "eea": 0, "eia": 0,
            "ul_nas_count": 0, "dl_nas_count": 0,
        }
        self._reg_request_bytes: Optional[bytes] = None

        self.pdu_sessions = {}   # psi -> dict (populated in Phase 3)
        self.t_created = time.monotonic()
        self.t_registered: Optional[float] = None

    # ── Public API ──────────────────────────────────────────────────

    @property
    def mailbox(self) -> asyncio.Queue:
        return self._mailbox

    async def start(self) -> None:
        if self._task is None:
            self._task = spawn_task(self._run(), name=f"ue-{self.imsi}")

    async def stop(self) -> None:
        self._stop.set()
        try:
            self._mailbox.put_nowait(None)
        except asyncio.QueueFull:
            pass
        if self._task is not None:
            try:
                await asyncio.wait_for(self._task, timeout=2.0)
            except asyncio.TimeoutError:
                self._task.cancel()

    async def register(self) -> None:
        """Begin attach: send InitialUEMessage carrying a Registration Request."""
        self._set_state(REG_INITIATED)
        nas = NasBuilder.registration_request(
            self.imsi, self.mcc, self.mnc,
            UE_DEFAULTS["ue_sec_cap"],
            UE_DEFAULTS.get("requested_nssai"))
        self._reg_request_bytes = nas
        ngap_pdu = NgapCodec.build_initial_ue_message(
            self.ran_ue_ngap_id, nas,
            self.gnb.mcc, self.gnb.mnc, self.gnb.tac, self.gnb.gnb_id)
        await self.gnb.send_ngap(ngap_pdu)

    async def wait_for_state(self, target: str, timeout: float = 15.0) -> bool:
        """Wait for state == target. Bails fast if gNB enters ERROR."""
        deadline = time.monotonic() + timeout
        while True:
            if self.state == target:
                return True
            if getattr(self.gnb, "state", None) == ERROR:
                return False
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                return False
            try:
                await asyncio.wait_for(self._state_event.wait(), timeout=remaining)
            except asyncio.TimeoutError:
                return False

    # ── Mailbox consumer ────────────────────────────────────────────

    async def _run(self) -> None:
        while not self._stop.is_set():
            msg = await self._mailbox.get()
            if msg is None:
                break
            try:
                await self._handle(msg)
            except Exception as e:
                log.exception("[%s] handler failed on %s: %s",
                              self.imsi, type(msg).__name__, e)
                self.last_error = str(e)
                self._set_state(ERROR)

    async def _handle(self, msg) -> None:
        if isinstance(msg, DownlinkNas):
            await self._on_downlink_nas(msg)
        elif isinstance(msg, InitialContextSetup):
            await self._on_initial_context_setup(msg)
        elif isinstance(msg, PduSessionResourceSetup):
            await self._on_pdu_session_resource_setup(msg)
        elif isinstance(msg, PduSessionResourceModify):
            log.debug("[%s] PDU session modify — not in Phase 2", self.imsi)
        elif isinstance(msg, UeContextRelease):
            log.debug("[%s] UE Context Release received", self.imsi)
            self._set_state(DEREGISTERED)
        else:
            log.debug("[%s] unhandled mailbox msg %s",
                      self.imsi, type(msg).__name__)

    # ── NGAP handlers ──────────────────────────────────────────────

    async def _on_downlink_nas(self, msg: DownlinkNas) -> None:
        if self.amf_ue_ngap_id is None and msg.amf_ue_ngap_id is not None:
            self.amf_ue_ngap_id = msg.amf_ue_ngap_id
        await self._process_nas(msg.nas_pdu)

    async def _on_initial_context_setup(self, msg: InitialContextSetup) -> None:
        if self.amf_ue_ngap_id is None and msg.amf_ue_ngap_id is not None:
            self.amf_ue_ngap_id = msg.amf_ue_ngap_id
        # Respond first, then process any embedded NAS PDU.
        rsp = NgapCodec.build_initial_context_setup_response(
            self.amf_ue_ngap_id or 0, self.ran_ue_ngap_id)
        await self.gnb.send_ngap(rsp)
        if msg.nas_pdu:
            await self._process_nas(msg.nas_pdu)

    async def _on_pdu_session_resource_setup(self,
                                              msg: PduSessionResourceSetup) -> None:
        if self.amf_ue_ngap_id is None and msg.amf_ue_ngap_id is not None:
            self.amf_ue_ngap_id = msg.amf_ue_ngap_id
        for item in msg.raw_ies.get(74, []):  # PDUSessionResourceSetupListSUReq
            psi = item.get("pDUSessionID", 1)
            self.gnb.gtp_teid_counter += 1
            rsp = NgapCodec.build_pdu_session_resource_setup_response(
                self.amf_ue_ngap_id or 0, self.ran_ue_ngap_id, psi,
                self.gnb.gtp_teid_counter, self.gnb.gnb_ip or "127.0.0.1")
            await self.gnb.send_ngap(rsp)
            nas = item.get("pDUSessionNAS-PDU")
            if nas:
                await self._process_nas(bytes(nas) if not isinstance(nas, bytes) else nas)
        log.debug("[%s] PDU setup NGAP-acked (Phase 2 — no tunnel yet)", self.imsi)

    # ── NAS processing (reuses src/protocol) ───────────────────────

    async def _process_nas(self, nas_bytes: bytes) -> None:
        if not nas_bytes:
            return
        msg, err = NasParser.parse(nas_bytes)
        if err:
            log.warning("[%s] NAS parse failed: %s", self.imsi, err)
            return

        # If integrity-protected, unwrap first.
        if NasParser.is_secured(msg):
            ctx = self.security_ctx
            msg, new_dl, mac_ok = unwrap_nas_security(
                msg, ctx.get("knasenc"), ctx.get("knasint"),
                ctx.get("eea", 0), ctx.get("eia", 0),
                ctx.get("dl_nas_count", 0), direction=1)
            ctx["dl_nas_count"] = new_dl
            if not mac_ok:
                log.warning("[%s] NAS MAC verify failed", self.imsi)

        msg_type = NasParser.get_message_type(msg)
        if msg_type is None:
            log.debug("[%s] NAS msg with no type", self.imsi)
            return
        log.info("[%s] RX NAS type=%d (0x%02X)", self.imsi, msg_type, msg_type)

        if msg_type == 0x56:          # Authentication Request
            await self._handle_auth_request(msg)
        elif msg_type == 0x5D:        # Security Mode Command
            await self._handle_sec_mode_command(msg)
        elif msg_type == 0x42:        # Registration Accept
            await self._handle_registration_accept(msg)
        elif msg_type in (0x44,):     # Registration Reject
            log.warning("[%s] Registration Reject", self.imsi)
            self._set_state(DEREGISTERED)
        else:
            log.debug("[%s] NAS type=%d unhandled in Phase 2", self.imsi, msg_type)

    async def _handle_auth_request(self, msg) -> None:
        self._set_state(AUTH_PENDING)
        try:
            rand = bytes(msg["RAND"]["V"])
            autn = bytes(msg["AUTN"]["AUTN"])
        except Exception as e:
            log.warning("[%s] Auth Request parse error: %s", self.imsi, e)
            return
        log.info("[%s] Auth Request: RAND=%s...", self.imsi,
                 hexlify(rand).decode()[:16])

        result = ue_authenticate(self.sim, rand, autn,
                                  get_snn(self.mcc, self.mnc))
        if result is None:
            log.warning("[%s] MAC verify failed", self.imsi)
            fail = NasBuilder.authentication_failure(cause=20)
            await self._send_plain(fail)
            return
        if result.get("sync_failure"):
            log.info("[%s] SQN resync needed", self.imsi)
            fail = NasBuilder.authentication_failure(
                cause=21, auts=result["AUTS"])
            await self._send_plain(fail)
            return

        self.security_ctx["KSEAF"] = result["KSEAF"]
        self.security_ctx["KAMF"] = derive_kamf(result["KSEAF"], self.imsi)
        resp = NasBuilder.authentication_response(result["RESstar"])
        await self._send_plain(resp)
        log.info("[%s] TX Auth Response", self.imsi)

    async def _handle_sec_mode_command(self, msg) -> None:
        self._set_state(SEC_MODE_PENDING)
        try:
            algo_bytes = bytes(msg["NASSecAlgo"]["NASSecAlgo"])
        except Exception as e:
            log.warning("[%s] SMC parse error: %s", self.imsi, e)
            return
        eea = (algo_bytes[0] >> 4) & 0x0F
        eia = algo_bytes[0] & 0x0F
        log.info("[%s] SMC: EEA%d / EIA%d", self.imsi, eea, eia)

        ctx = self.security_ctx
        ctx["eea"] = eea
        ctx["eia"] = eia
        knasenc, knasint = derive_nas_keys(ctx["KAMF"], eea, eia)
        ctx["knasenc"] = knasenc
        ctx["knasint"] = knasint
        ctx["ul_nas_count"] = 0
        ctx["dl_nas_count"] = 0

        plain = NasBuilder.security_mode_complete(self._reg_request_bytes)
        await self._send_secured(plain, sec_hdr=4)
        # Derive KgNB with the SMC-complete COUNT (mirrors UeStateMachine).
        smc_count = (ctx["ul_nas_count"] - 1) & 0xFFFFFFFF
        ctx["kgnb"] = derive_kgnb(ctx["KAMF"], smc_count)
        log.info("[%s] TX Security Mode Complete", self.imsi)

    async def _handle_registration_accept(self, msg) -> None:
        log.info("[%s] Registration Accept received", self.imsi)
        plain = NasBuilder.registration_complete()
        await self._send_secured(plain)
        log.info("[%s] TX Registration Complete", self.imsi)
        self._set_state(REGISTERED)
        self.t_registered = time.monotonic()

    # ── Outbound NAS helpers ──────────────────────────────────────

    async def _send_plain(self, nas: bytes) -> None:
        """Send an unprotected NAS PDU over UplinkNASTransport."""
        ngap_pdu = NgapCodec.build_uplink_nas_transport(
            self.amf_ue_ngap_id or 0, self.ran_ue_ngap_id, nas,
            self.gnb.mcc, self.gnb.mnc, self.gnb.tac, self.gnb.gnb_id)
        await self.gnb.send_ngap(ngap_pdu)

    async def _send_secured(self, plain: bytes, sec_hdr: int = 2) -> None:
        """Wrap in NAS security and send."""
        ctx = self.security_ctx
        secured, new_count = wrap_nas_security(
            plain, ctx["knasenc"], ctx["knasint"],
            ctx.get("eea", 0), ctx.get("eia", 0),
            ctx.get("ul_nas_count", 0), sec_hdr=sec_hdr)
        ctx["ul_nas_count"] = new_count
        await self._send_plain(secured)

    # ── State helpers ──────────────────────────────────────────────

    def _set_state(self, new_state: str) -> None:
        if self.state == new_state:
            return
        old = self.state
        self.state = new_state
        log.info("[%s] State: %s -> %s", self.imsi, old, new_state)
        # Wake all waiters; they'll re-check self.state.
        self._state_event.set()
        self._state_event = asyncio.Event()

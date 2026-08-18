# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""MCX floor-control state machine — tester-side mirror.

Mirrors the Go core's services/mcx/mcptt/floor.go floor-control
server state machine. Pure logic (no I/O, no live core state)
suitable for round-tripping fixtures and verifying §-cited spec
fidelity ahead of an end-to-end Robot run.

Spec anchors (PDFs under specs/common/):

  * TS 24.380 §6.3.5      Floor server state machine — basic floor
                          control operation towards the floor
                          participant. The five core states modelled:
                          Start-Stop / Floor Idle / Floor Taken /
                          Permitted / Floor Releasing.
  * TS 24.380 §6.2.4      Floor participant state transitions —
                          counterpart from the UE side; this module
                          is the server side, but exposes
                          per-participant state names so a Robot
                          test can replay against either perspective.
  * TS 24.380 §4.1.1.4    "Determine on-network effective priority" —
                          the preempt-override outcome is honoured
                          (emergency wins; lower-numeric priority
                          wins). Other outcomes (preempt-revoke,
                          queued, rejected) are partial — see TODO.
  * TS 24.379 §6.2.8.1.15 Resource-Priority namespace and values are
                          retrieved from the MCPTT service config
                          (TS 24.484); this module's int priorities
                          are the local FloorController ranking
                          only, NOT the wire Resource-Priority.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import IntEnum
from typing import Optional


# ─── TS 24.380 §6.3.5 floor-server states (controller view) ─────
class CallState(IntEnum):
    IDLE = 0       # §6.3.5.3 view: no holder
    TAKEN = 1      # §6.3.5.4 / §6.3.5.5: a holder exists
    RELEASING = 2  # §6.3.5.9: releasing


# ─── TS 24.380 §6.3.5 per-participant projection ────────────────
class FloorServerState(IntEnum):
    START_STOP = 0       # §6.3.5.2
    FLOOR_IDLE = 1       # §6.3.5.3
    FLOOR_TAKEN = 2      # §6.3.5.4
    PERMITTED = 3        # §6.3.5.5 (the holder)
    FLOOR_RELEASING = 4  # §6.3.5.9


# Local FloorController priority constants (lower number wins).
# Per TS 24.379 §6.2.8.1.15 these are NOT the wire-level
# Resource-Priority values; that namespace comes from TS 24.484.
PRIORITY_EMERGENCY = 1
PRIORITY_NORMAL = 5
MAX_FLOOR_QUEUE = 10  # TS 24.380 §6.3.5.4 queueing branch cap


@dataclass
class Participant:
    mcptt_id: str
    priority: int = PRIORITY_NORMAL
    state: FloorServerState = FloorServerState.START_STOP
    has_floor: bool = False
    requesting: bool = False


# ─── TS 24.380 §4.1.1.4 outcome 1 "preempt-override" ────────────
def can_preempt(req: int, holder: int) -> bool:
    """Local-policy collapse of the §4.1.1.4 decision matrix; only
    the priority-based "preempt-override" outcome is implemented.

    TODO(spec: TS 24.380 §4.1.1.4): wire the remaining six §4.1.1.4
    inputs (user-priority lookup, participant type, call type, etc.)
    and the preempt-revoke outcome (§6.3.5.6 'U: pending Floor
    Revoke'). Today only emergency + lower-numeric priority wins.
    """
    if req == PRIORITY_EMERGENCY:
        return True
    return req < holder


@dataclass
class FloorController:
    """Mirror of the Go FloorController. Single-threaded — the
    Python-side tester drives requests serially, so we don't model
    the libs/fsm event loop here."""

    call_id: str
    state: CallState = CallState.IDLE
    holder: Optional[Participant] = None
    queue: list[Participant] = field(default_factory=list)
    participants: dict[str, Participant] = field(default_factory=dict)

    # ── §6.3.5.2.2 SIP Session initiated ────────────────────────
    def add_participant(self, mcptt_id: str, priority: int = PRIORITY_NORMAL) -> None:
        if mcptt_id in self.participants:
            return
        self.participants[mcptt_id] = Participant(
            mcptt_id=mcptt_id, priority=priority,
        )
        self._refresh_states()

    def remove_participant(self, mcptt_id: str) -> None:
        p = self.participants.get(mcptt_id)
        if p is None:
            return
        if self.holder is not None and self.holder.mcptt_id == mcptt_id:
            self._do_release()
        self.queue = [q for q in self.queue if q.mcptt_id != mcptt_id]
        del self.participants[mcptt_id]
        self._refresh_states()

    # ── §6.3.5.3.4 (from Idle) / §6.3.5.4.4 (from Taken) ───────
    def request_floor(
        self, mcptt_id: str, priority: Optional[int] = None,
    ) -> dict:
        p = self.participants.get(mcptt_id)
        if p is None:
            return {"result": "error", "reason": "not_participant"}
        if priority is not None:
            p.priority = priority

        if self.state == CallState.RELEASING:
            return {"result": "error", "reason": "bad_state"}

        if self.state == CallState.IDLE:
            # §6.3.5.3.5 Send Floor Granted + §6.3.5.3.3 Send Floor Taken.
            self._do_grant(p)
            self.state = CallState.TAKEN
            self._refresh_states()
            return {"result": "granted", "mcptt_id": mcptt_id}

        # state == TAKEN
        if self.holder is not None and self.holder.mcptt_id == mcptt_id:
            return {"result": "granted", "mcptt_id": mcptt_id}

        if self.holder is not None and can_preempt(p.priority, self.holder.priority):
            old = self.holder.mcptt_id
            self._do_preempt(p)
            self._refresh_states()
            return {
                "result": "preempted",
                "mcptt_id": mcptt_id,
                "preempted": old,
            }

        if len(self.queue) >= MAX_FLOOR_QUEUE:
            return {"result": "denied", "reason": "queue_full"}

        p.requesting = True
        self._enqueue(p)
        return {"result": "queued", "position": self._queue_position(p)}

    # ── §6.3.5.5 → §6.3.5.3/.4 — release ────────────────────────
    def release_floor(self, mcptt_id: str) -> dict:
        if self.holder is None or self.holder.mcptt_id != mcptt_id:
            return {"result": "error", "reason": "not_holder"}
        self._do_release()
        self._refresh_states()
        return {"result": "released"}

    # ── Snapshot for tests / observers ──────────────────────────
    def snapshot(self) -> dict:
        self._refresh_states()
        return {
            "call_id": self.call_id,
            "state": self.state.name.lower(),
            "holder": self.holder.mcptt_id if self.holder else "",
            "holder_priority": self.holder.priority if self.holder else 0,
            "queue": [q.mcptt_id for q in self.queue],
            "participants": [
                {
                    "mcptt_id": p.mcptt_id,
                    "priority": p.priority,
                    "state": p.state.name.lower(),
                    "has_floor": p.has_floor,
                    "requesting": p.requesting,
                }
                for p in self.participants.values()
            ],
        }

    # ── Transition mechanics ───────────────────────────────────
    def _do_grant(self, p: Participant) -> None:
        # §6.3.5.3.5 Send Floor Granted + §6.3.5.3.3 Send Floor Taken.
        p.has_floor = True
        p.requesting = False
        self.holder = p

    def _do_release(self) -> None:
        # §6.3.5.4.5 Receive Floor Release → Send Floor Idle, then
        # auto-grant the next-in-queue if any.
        if self.holder is not None:
            self.holder.has_floor = False
        self.holder = None
        if self.queue:
            nxt = self.queue.pop(0)
            nxt.requesting = False
            self._do_grant(nxt)
            self.state = CallState.TAKEN
        else:
            self.state = CallState.IDLE

    def _do_preempt(self, req: Participant) -> None:
        # §4.1.1.4 outcome 1 — immediate override.
        if self.holder is not None:
            self.holder.has_floor = False
        self.holder = None
        self._do_grant(req)

    def _enqueue(self, p: Participant) -> None:
        self.queue.append(p)
        # Stable insert so lower-numeric (better) priorities migrate
        # toward the head; preserves arrival order within a tier.
        i = len(self.queue) - 1
        while i > 0 and self.queue[i].priority < self.queue[i - 1].priority:
            self.queue[i - 1], self.queue[i] = self.queue[i], self.queue[i - 1]
            i -= 1

    def _queue_position(self, p: Participant) -> int:
        for i, q in enumerate(self.queue):
            if q.mcptt_id == p.mcptt_id:
                return i + 1
        return 0

    def _refresh_states(self) -> None:
        for p in self.participants.values():
            if self.state == CallState.RELEASING:
                p.state = FloorServerState.FLOOR_RELEASING
            elif self.state == CallState.IDLE:
                p.state = FloorServerState.FLOOR_IDLE
            else:  # TAKEN
                if self.holder is not None and self.holder.mcptt_id == p.mcptt_id:
                    p.state = FloorServerState.PERMITTED
                else:
                    p.state = FloorServerState.FLOOR_TAKEN


# ─── TS 23.281 §7.7 transmission control (single-transmitter) ───
@dataclass
class TransmissionController:
    """Simplified MCVideo transmission controller, mirroring the
    Go services/mcx/mcvideo/mcvideo.go TransmissionController.

    Only the §7.7.1 single-transmitter happy path is modelled.
    """

    call_id: str
    transmitter: str = ""
    participants: set[str] = field(default_factory=set)

    def add_participant(self, mcptt_id: str) -> None:
        self.participants.add(mcptt_id)

    def remove_participant(self, mcptt_id: str) -> None:
        self.participants.discard(mcptt_id)
        if self.transmitter == mcptt_id:
            self.transmitter = ""

    def request_transmission(self, mcptt_id: str) -> dict:
        if mcptt_id not in self.participants:
            return {"result": "error", "reason": "not_participant"}
        if self.transmitter == "":
            self.transmitter = mcptt_id
            return {"result": "granted"}
        if self.transmitter == mcptt_id:
            return {"result": "already_transmitting"}
        return {
            "result": "denied",
            "reason": "busy",
            "current_transmitter": self.transmitter,
        }

    def release_transmission(self, mcptt_id: str) -> dict:
        if self.transmitter != mcptt_id:
            return {"result": "error", "reason": "not_transmitter"}
        self.transmitter = ""
        return {"result": "released"}


# TODO(spec: TS 23.281 §7.7.1 / §7.7.2): the §7.7.1.x preempt
# branches (priority-based override of an active transmitter) are
# not modelled. Mirroring the Go core's TODO: today the controller
# denies a contending request unconditionally.
#
# TODO(spec: TS 24.281): stage-3 transmission-control protocol
# packets (the MCVideo equivalent of the MCPTT floor protocol in
# TS 24.380) are not encoded. TS 24.281 is not yet in-tree.
#
# TODO(spec: TS 24.282): MCData stage-3 SDS payload framing is not
# generated here; tests drive the high-level core REST API.

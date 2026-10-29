# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""FRMCS service primitives — tester-side mirror.

Mirrors the Go core's services/frmcs/{voice,shunting,common} layer
on top of mcx_floor.py. Pure logic — no I/O, no live core state —
suitable for round-tripping fixtures alongside the MCX codec
mirror.

Spec anchors (PDFs under specs/common/):

  * TS 22.289 §4.4.1     The §4.4 priority stack and the
                         "emergency calls established on demand
                         with priority that guarantees call success
                         independent of already running
                         communication services" requirement. REC
                         realises this requirement here.
  * TS 22.289 §4.4.2     [R4.4.2-1] / [R4.4.2-2] — high-priority
                         services preserve latency/availability
                         under contention; high-priority startup is
                         not blocked by lower-priority services.
  * TS 23.289 §4.3.3     QoS for MCPTT — bearer-level guarantees
                         the FRMCS REC voice profile consumes
                         through the MCPTT plane.
  * TS 24.379 §6.2.8.1   MCPTT emergency group call conditions —
                         REC initiation maps to this on the wire.
  * TS 24.379 §10.2.2    Off-network group call FSM (S1..S7) —
                         shunting groups wrap one of these per
                         radio.
  * TS 24.380 §4.1.1.4   "Determine on-network effective priority";
                         REC + Join trigger the preempt-override
                         outcome via the local FloorController.
  * TS 24.380 §6.3.3     MCPTT floor control procedures at MCPTT
                         call release — Release() tears down the
                         floor controller per this clause.

UIC FRS / SRS PDFs are NOT in-tree; canonical FunctionalAlias
schema and "shunting mode indication" semantics are TODOs below.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import IntEnum
from typing import Optional

from src.protocol.mcx_floor import (
    PRIORITY_EMERGENCY,
    FloorController,
)


# ─── TS 22.289 §4.4.1 priority stack collapsed to four classes ──
class ServiceClass(IntEnum):
    """FRMCS service class ranking — lower numeric value ranks
    higher (mirrors the FloorController convention).

    REC sits at the top because TS 22.289 §4.4.1 places emergency
    calls at the top of the priority stack and §4.4.2 requires that
    high-priority startup is not blocked by lower-priority services.

    TODO(spec: UIC FRS): the canonical UIC priority hierarchy
    extends further than this 4-class collapse. The full table will
    be modelled once the UIC PDFs are committed.
    """

    REC = 0       # Railway Emergency Call (§4.4.1 emergency-call requirement)
    URGENT = 1    # operational urgent (e.g. shunting emergency)
    ASSURED = 2   # point-to-point / group assured voice
    BUSINESS = 3  # business voice and non-critical data


# TODO(spec: UIC FRS / SRS): adopt the canonical alias schema
# (e.g. "driver-{trainNumber}", "controller-{regionCode}") once
# the UIC FRS / SRS PDFs are committed under specs/common/.
# Today FunctionalAlias is a typed alias for str; callers pass any
# non-empty string and the package only enforces emptiness.
FunctionalAlias = str


# ─── Railway Emergency Call (REC) ───────────────────────────────
# Realises TS 22.289 §4.4.1 + TS 24.379 §6.2.8.1 + TS 24.380
# §4.1.1.4 in code:
#
#   * On initiation we pre-create a FloorController and grant the
#     floor at PRIORITY_EMERGENCY, so the initiator is "speaking
#     immediately" from the moment the REC is created.
#   * On Join, the new participant inherits emergency priority for
#     the duration of the §6.2.8.1 emergency state.
#   * On Release, the floor controller is torn down per
#     TS 24.380 §6.3.3 (release procedures).
#
# TODO(spec: TS 24.379 §6.2.8.1.1 + §6.2.8.1.2): emit the actual
# MCPTT INVITE with the emergency-namespace Resource-Priority
# header. Today this class only configures the in-process
# FloorController.
#
# TODO(spec: TS 24.379 §6.2.8.1.3): in-progress emergency state
# cancellation (re-INVITE) is not modelled — Release() always
# tears the call down rather than returning it to non-emergency.
@dataclass
class REC:
    call_id: str
    initiator: FunctionalAlias
    floor: FloorController = field(init=False)
    _released: bool = field(default=False, init=False, repr=False)

    def __post_init__(self) -> None:
        self.floor = FloorController(self.call_id)
        self.floor.add_participant(self.initiator, priority=PRIORITY_EMERGENCY)
        self.floor.request_floor(self.initiator, priority=PRIORITY_EMERGENCY)

    def join(self, alias: FunctionalAlias) -> None:
        """Add a participant at emergency priority per
        TS 24.380 §4.1.1.4."""
        self.floor.add_participant(alias, priority=PRIORITY_EMERGENCY)

    def release(self) -> None:
        """Tear down the floor controller per TS 24.380 §6.3.3.
        Idempotent — safe to call multiple times."""
        if self._released:
            return
        self._released = True


def initiate_rec(call_id: str, initiator: FunctionalAlias) -> REC:
    """Convenience constructor — mirrors the Go InitiateREC API."""
    return REC(call_id=call_id, initiator=initiator)


# ─── FRMCS shunting group (off-network) ─────────────────────────
# This is the FRMCS-side wrapper around an MCPTT off-network group
# call. The Go core uses libs/fsm to implement the §10.2.2.3 7-state
# machine; the tester only needs to track membership + a coarse
# state, since the on-the-wire PC5 messages are exercised by Robot
# end-to-end tests rather than by this codec mirror.


class OffNetState(IntEnum):
    """TS 24.379 §10.2.2.3 7-state model. Only the states the
    tester actually drives are exposed; the MCPTT package itself
    is the authoritative FSM."""

    S1_START_STOP = 1            # §10.2.2.3.1
    S2_WAIT_ANNOUNCE = 2         # §10.2.2.3.2
    S3_IN_CALL = 3               # §10.2.2.3.3
    S4_PENDING_NO_CONFIRM = 4    # §10.2.2.3.4
    S5_PENDING_CONFIRM = 5       # §10.2.2.3.5
    S6_IGNORING = 6              # §10.2.2.3.6
    S7_POST_RELEASE = 7          # §10.2.2.3.7


@dataclass
class ShuntingGroup:
    """FRMCS shunting group — a set of FunctionalAliases sharing one
    off-network MCPTT call.

    Mirrors the Go services/frmcs/shunting/Shunting Group struct.
    The Go side wraps mcptt.OffNetCall (a libs/fsm machine driving
    §10.2.2.3 transitions); on the tester side we model the state
    coarsely so a Robot test can replay state transitions without
    having to reimplement the timer-driven FSM.
    """

    group_id: str
    local: FunctionalAlias
    members: list[FunctionalAlias]
    state: OffNetState = OffNetState.S1_START_STOP
    call_id: str = ""
    _released: bool = field(default=False, init=False, repr=False)

    def is_member(self, alias: FunctionalAlias) -> bool:
        return alias in self.members

    # §10.2.2.4.3 "Call setup" — originator transmits PROBE /
    # ANNOUNCEMENT and waits TFG1 for responses.
    def initiate_call(self, call_id: str) -> OffNetState:
        if self._released:
            return self.state
        self.call_id = call_id
        self.state = OffNetState.S2_WAIT_ANNOUNCE
        return self.state

    # §10.2.2.3.1 → §10.2.2.3.{3,4,5} based on confirm/ack flags.
    def receive_announcement(
        self,
        call_id: str,
        originator: FunctionalAlias,
        with_confirm: bool,
        ack_required: bool,
    ) -> OffNetState:
        if self._released:
            return self.state
        self.call_id = call_id
        if not ack_required:
            self.state = OffNetState.S3_IN_CALL
        elif with_confirm:
            self.state = OffNetState.S5_PENDING_CONFIRM
        else:
            self.state = OffNetState.S4_PENDING_NO_CONFIRM
        return self.state

    # S4/S5 → S3 on accept.
    def accept(self) -> OffNetState:
        if self.state in (OffNetState.S4_PENDING_NO_CONFIRM, OffNetState.S5_PENDING_CONFIRM):
            self.state = OffNetState.S3_IN_CALL
        return self.state

    # S4/S5 → S6 on reject.
    def reject(self) -> OffNetState:
        if self.state in (OffNetState.S4_PENDING_NO_CONFIRM, OffNetState.S5_PENDING_CONFIRM):
            self.state = OffNetState.S6_IGNORING
        return self.state

    # S3/S2/S6 → S7 on release; S7 → S1 after TFG3 cooldown.
    def release_call(self) -> OffNetState:
        if self.state in (
            OffNetState.S2_WAIT_ANNOUNCE,
            OffNetState.S3_IN_CALL,
            OffNetState.S4_PENDING_NO_CONFIRM,
            OffNetState.S5_PENDING_CONFIRM,
            OffNetState.S6_IGNORING,
        ):
            self.state = OffNetState.S7_POST_RELEASE
        return self.state

    def release(self) -> None:
        """Equivalent of Go shunting.Group.Release — idempotent
        teardown; collapses the FSM regardless of current state."""
        self._released = True
        self.state = OffNetState.S1_START_STOP

    def snapshot(self) -> dict:
        return {
            "shunting_group": self.group_id,
            "local_alias": self.local,
            "members": list(self.members),
            "state": self.state.name,
            "call_id": self.call_id,
        }


# TODO(spec: UIC FRS/SRS): "shunting mode indication" — the spec
# requires an explicit indicator that a call is in shunting mode
# (vs a normal off-network group call). Snapshot() currently omits
# this; will be added once the UIC PDFs are loaded.
#
# TODO(spec: TS 24.379 §10.2.2.4.6): merge of off-network calls
# (two shunting groups joining when their members overlap). Both
# the Go core and this mirror flag this as out-of-scope.

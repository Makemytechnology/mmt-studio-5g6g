# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""AF (Application Function) primitives — tester-side mirror.

Mirrors the Go core's nf/af package. Pure dataclasses + in-memory
managers; no live core, no DB, no network. The goal is to give pytest
a black-box surface that exercises the same enumerations (af_type,
event types, FSM states) and lifecycle behaviour (FSM walk, IMSI-only
notify filter, idempotent delete) as the Go side.

Spec anchors (verifiable against local PDFs):

  * TS 23.501 §6.2.10  Application Function — AF role and 5GC
                       interactions (PCF via N5, NEF via N33).
  * TS 29.514 §4.2     Npcf_PolicyAuthorization service operations.
  * TS 29.514 §4.2.2   Create — AF authorization request.
  * TS 29.514 §4.2.3   Update — AF modifies an existing authorization.
  * TS 29.514 §4.2.4   Delete — AF terminates the authorization.
  * TS 29.514 §4.2.5   Notify — PCF → AF push.
  * TS 29.517 §4.1     Naf_EventExposure service description.
  * TS 29.517 §4.2     Naf_EventExposure operations (Subscribe /
                       Unsubscribe / Notify).
  * TS 29.517 §5.6     Naf_EventExposure data model.
  * TS 29.522 §4.4.7   Procedures for Traffic Influence (NEF-side).
  * TS 29.522 §5.4     TrafficInfluence API.
  * TS 23.548 §6.6     AF Guidance to PCF Determination of URSP Rules.

Deferred (not implemented in the mirror, documented as TODO(spec:)):

  * TODO(spec: TS 29.514 §4.2.6) Subscribe service operation —
                       AF-as-consumer subscription to PCF events.
  * TODO(spec: TS 29.514 §4.2.7) Unsubscribe service operation.
  * TODO(spec: TS 29.522 §4.4.14) Analytics Information Exposure.
  * TODO(spec: TS 29.522 §4.4.33) Media Streaming Event Exposure.
  * TODO(spec: TS 29.522 §4.4.47) IMS Event Exposure Management.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import Enum
from typing import Optional


# ════════════════════════════════════════════════════════════════════
# AF type — must mirror nf/af/af.go ValidAFTypes 1:1
# ════════════════════════════════════════════════════════════════════

AF_TYPE_IMS = "ims"
AF_TYPE_MEC = "mec"
AF_TYPE_THIRD_PARTY = "third_party"

VALID_AF_TYPES = frozenset({AF_TYPE_IMS, AF_TYPE_MEC, AF_TYPE_THIRD_PARTY})

# Session status values (mirror of af.AFSession.Status string literals).
STATUS_CREATED = "created"
STATUS_ACTIVE = "active"
STATUS_FAILED = "failed"
STATUS_TERMINATED = "terminated"


# ════════════════════════════════════════════════════════════════════
# AF Session FSM — TS 29.514 §4.2 + nf/af/fsm/state.go
# ════════════════════════════════════════════════════════════════════


class AFState(Enum):
    """Mirror of nf/af/fsm.State enum."""

    INITIAL = "INITIAL"
    AUTH_PENDING = "AUTH_PENDING"
    ACTIVE = "ACTIVE"
    UPDATE_PENDING = "UPDATE_PENDING"
    TERMINATED = "TERMINATED"
    FAILED = "FAILED"


# Static transition table — mirror of nf/af/af_transitions.go.
# (from_state, event) → to_state.
AF_TRANSITIONS: dict[tuple[AFState, str], AFState] = {
    # §4.2.2 Create
    (AFState.INITIAL, "create_request"): AFState.AUTH_PENDING,
    (AFState.AUTH_PENDING, "authorized"): AFState.ACTIVE,
    (AFState.AUTH_PENDING, "auth_rejected"): AFState.FAILED,
    # §4.2.3 Update
    (AFState.ACTIVE, "update_request"): AFState.UPDATE_PENDING,
    (AFState.UPDATE_PENDING, "authorized"): AFState.ACTIVE,
    # §4.2.3: rejected Update keeps prior auth — fall back to ACTIVE.
    (AFState.UPDATE_PENDING, "auth_rejected"): AFState.ACTIVE,
    # §4.2.5 Notify (observational self-loop)
    (AFState.ACTIVE, "notify_received"): AFState.ACTIVE,
    # §4.2.4 Delete
    (AFState.ACTIVE, "delete_request"): AFState.TERMINATED,
    (AFState.UPDATE_PENDING, "delete_request"): AFState.TERMINATED,
    (AFState.FAILED, "delete_request"): AFState.TERMINATED,
    # Early-abort: cancel during AUTH_PENDING.
    (AFState.AUTH_PENDING, "delete_request"): AFState.TERMINATED,
}


def af_fire(state: AFState, event: str) -> AFState:
    """Apply one transition; raises KeyError if no transition is defined.

    Mirrors the strict "no implicit self-loops" behaviour of the Go FSM.
    """
    return AF_TRANSITIONS[(state, event)]


# ════════════════════════════════════════════════════════════════════
# Dataclasses
# ════════════════════════════════════════════════════════════════════


@dataclass
class AFSession:
    """Mirror of nf/af/af.go AFSession."""

    session_id: str
    af_id: str
    af_type: str
    imsi: str = ""
    dnn: str = ""
    pdu_session_id: int = 0
    media_components: list[dict] = field(default_factory=list)
    traffic_filters: list[dict] = field(default_factory=list)
    status: str = STATUS_CREATED
    state: AFState = AFState.INITIAL
    created_at: Optional[datetime] = None
    updated_at: Optional[datetime] = None


# Event types — must mirror nf/af/af.go constants 1:1.
EVENT_UE_REACHABILITY = "UE_REACHABILITY"
EVENT_LOCATION_REPORT = "LOCATION_REPORT"
EVENT_LOSS_OF_CONNECTIVITY = "LOSS_OF_CONNECTIVITY"
EVENT_COMMUNICATION_FAILURE = "COMMUNICATION_FAILURE"
EVENT_PDU_SESSION_STATUS = "PDU_SESSION_STATUS"
EVENT_QOS_MONITORING = "QOS_MONITORING"

VALID_EVENTS = frozenset({
    EVENT_UE_REACHABILITY,
    EVENT_LOCATION_REPORT,
    EVENT_LOSS_OF_CONNECTIVITY,
    EVENT_COMMUNICATION_FAILURE,
    EVENT_PDU_SESSION_STATUS,
    EVENT_QOS_MONITORING,
})


@dataclass
class EventSubscription:
    """Mirror of nf/af/af.go EventSubscription."""

    sub_id: str
    af_id: str
    event_type: str
    imsi: str = ""           # empty == wildcard
    callback_url: str = ""
    status: str = "active"
    notification_count: int = 0


# ════════════════════════════════════════════════════════════════════
# AF Session Manager — pure-Python mirror
# ════════════════════════════════════════════════════════════════════


class AFSessionManager:
    """Mirror of nf/af.AFSessionManager.

    Behaviour parity:
      - CreateSession rejects blank af_id and unknown af_type up front.
      - IMS authorization fails if imsi is empty (TS 29.514 §4.2.2.2
        ProblemDetails).
      - Delete is idempotent; second Delete returns False.
      - Update on unknown id returns False.
    """

    def __init__(self) -> None:
        self.sessions: dict[str, AFSession] = {}
        self._next_id = 0

    def create_session(self, af_id: str, af_type: str, imsi: str, dnn: str,
                       pdu_session_id: int = 0,
                       media_components: Optional[list[dict]] = None,
                       traffic_filters: Optional[list[dict]] = None) -> tuple[str, bool]:
        if not af_id:
            return ("", False)
        if af_type not in VALID_AF_TYPES:
            return ("", False)

        self._next_id += 1
        sid = f"af-sess-{self._next_id:05d}"
        now = datetime.now(timezone.utc)
        s = AFSession(
            session_id=sid, af_id=af_id, af_type=af_type, imsi=imsi, dnn=dnn,
            pdu_session_id=pdu_session_id,
            media_components=list(media_components or []),
            traffic_filters=list(traffic_filters or []),
            status=STATUS_CREATED, state=AFState.INITIAL,
            created_at=now, updated_at=now,
        )
        self.sessions[sid] = s

        # FSM walk: Initial → AuthPending → {Active|Failed}.
        s.state = af_fire(s.state, "create_request")

        if af_type == AF_TYPE_IMS:
            ok = bool(imsi)  # TS 29.514 §4.2.2.2 — IMSI required.
        else:
            ok = True

        if ok:
            s.status = STATUS_ACTIVE
            s.state = af_fire(s.state, "authorized")
        else:
            s.status = STATUS_FAILED
            s.state = af_fire(s.state, "auth_rejected")
        return (sid, ok)

    def update_session(self, sid: str,
                       media_components: Optional[list[dict]] = None,
                       traffic_filters: Optional[list[dict]] = None) -> bool:
        s = self.sessions.get(sid)
        if s is None:
            return False
        if media_components is not None:
            s.media_components = list(media_components)
        if traffic_filters is not None:
            s.traffic_filters = list(traffic_filters)
        s.updated_at = datetime.now(timezone.utc)
        # Active → UpdatePending → Active (per §4.2.3 happy path).
        if s.state == AFState.ACTIVE:
            s.state = af_fire(s.state, "update_request")
        if s.af_type == AF_TYPE_IMS and not s.imsi:
            s.state = af_fire(s.state, "auth_rejected")
            return False
        s.state = af_fire(s.state, "authorized")
        return True

    def delete_session(self, sid: str) -> bool:
        s = self.sessions.pop(sid, None)
        if s is None:
            return False
        # Mirror the Go behaviour: drop FSM after firing terminal event.
        try:
            s.state = af_fire(s.state, "delete_request")
        except KeyError:
            pass
        s.status = STATUS_TERMINATED
        return True

    def get_session(self, sid: str) -> Optional[AFSession]:
        return self.sessions.get(sid)

    def get_sessions(self, af_type: str = "") -> list[AFSession]:
        if af_type:
            return [s for s in self.sessions.values() if s.af_type == af_type]
        return list(self.sessions.values())

    def get_sessions_for_ue(self, imsi: str) -> list[AFSession]:
        return [s for s in self.sessions.values() if s.imsi == imsi and s.status == STATUS_ACTIVE]


# ════════════════════════════════════════════════════════════════════
# Event Exposure Manager — TS 29.517 §4.2
# ════════════════════════════════════════════════════════════════════


class EventExposureManager:
    """Mirror of nf/af.EventExposureManager."""

    def __init__(self) -> None:
        self.subscriptions: dict[str, EventSubscription] = {}
        self._next_id = 0

    def subscribe(self, af_id: str, event_type: str, imsi: str = "", callback_url: str = "") -> str:
        if not af_id:
            return ""
        if event_type not in VALID_EVENTS:
            return ""
        self._next_id += 1
        sub_id = f"evt-sub-{self._next_id:04d}"
        self.subscriptions[sub_id] = EventSubscription(
            sub_id=sub_id, af_id=af_id, event_type=event_type,
            imsi=imsi, callback_url=callback_url, status="active",
        )
        return sub_id

    def unsubscribe(self, sub_id: str) -> bool:
        s = self.subscriptions.pop(sub_id, None)
        if s is None:
            return False
        s.status = "terminated"
        return True

    def notify(self, event_type: str, imsi: str, event_data: Optional[dict] = None) -> int:
        """Fan-out one event. Returns the number of subscriptions notified."""
        fired = 0
        for s in self.subscriptions.values():
            if s.status != "active":
                continue
            if s.event_type != event_type:
                continue
            if s.imsi and s.imsi != imsi:
                continue
            s.notification_count += 1
            fired += 1
        return fired

    def get_subscriptions(self, af_id: str = "") -> list[EventSubscription]:
        if af_id:
            return [s for s in self.subscriptions.values() if s.af_id == af_id]
        return list(self.subscriptions.values())


# ════════════════════════════════════════════════════════════════════
# Traffic Influence helpers — TS 29.522 §4.4.7
# ════════════════════════════════════════════════════════════════════


def request_traffic_influence(mgr: AFSessionManager, af_id: str, imsi: str, dnn: str,
                              target_ip: str = "", target_fqdn: str = "",
                              target_port: int = 0, edge_site_id: str = "") -> tuple[str, bool]:
    """Mirror of nf/af.RequestTrafficInfluence — wraps a 'mec' AF session."""
    return mgr.create_session(
        af_id=af_id, af_type=AF_TYPE_MEC, imsi=imsi, dnn=dnn,
        traffic_filters=[{
            "ip": target_ip, "fqdn": target_fqdn,
            "port": target_port, "edge_site_id": edge_site_id,
        }],
    )


def revoke_traffic_influence(mgr: AFSessionManager, sid: str) -> bool:
    """Mirror of nf/af.RevokeTrafficInfluence."""
    return mgr.delete_session(sid)

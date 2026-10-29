# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Public-safety primitives — tester-side mirror.

Mirrors the Go core's safety/{pws,iops,disaster_roaming,access}
packages. Pure functions + dataclasses + in-memory stores; no live
core, no DB, no network. Suitable for round-trip fixtures alongside
live behaviour tests of the core.

Spec anchors (§-checked by speccheck against PDFs in specs/common/):

  PWS — TS 23.501 §4.4.1, §5.16.1
  ----
  * TS 23.501 §4.4.1       PWS architecture in 5GS (defers wire-level
                           realisation to TS 23.041).
  * TS 23.501 §5.16.1      PWS functional description.
  * TS 38.413 §8.9         NGAP Warning Message Transmission Procedures.

  IOPS — TS 23.401 Annex K
  ------
  * TS 23.401 §K.1         General description of the IOPS concept.
  * TS 23.401 §K.2         Operation of isolated public safety networks.
  * TS 23.401 §K.2.1       General Description.
  * TS 23.401 §K.2.2       UE configuration for IOPS.
  * TS 23.401 §K.2.3       IOPS network configuration (cached AKA tuples).
  * TS 23.401 §K.2.4       IOPS network establishment / termination.
  * TS 23.401 §K.2.5       UE mobility within / out of IOPS.

  Disaster Roaming — TS 22.261 §6.31, TS 23.501 §5.40
  ----------------
  * TS 22.261 §6.31        Minimization of Service Interruption.
  * TS 22.261 §6.31.1      Description.
  * TS 22.261 §6.31.2      Requirements.
  * TS 22.261 §6.31.2.2    Disaster Condition definition.
  * TS 22.261 §6.31.2.3    Disaster Roaming policy.
  * TS 23.501 §5.40        Support of Disaster Roaming (5GS arch).
  * TS 23.501 §5.40.1      General — admission rules.
  * TS 23.501 §5.40.2      UE configuration and provisioning.
  * TS 23.501 §5.40.4      Registration for Disaster Roaming service.
  * TS 23.501 §5.40.5      Handling when a Disaster Condition ends.
  * TS 23.501 §5.40.6      Prevention of signalling overload.

  Access Restriction — TS 24.501 §4.5 / §5.3.13 / §5.3.13A
  ------------------
  * TS 24.501 §4.5         Unified access control framework.
  * TS 24.501 §5.3.13      Lists of 5GS forbidden tracking areas.
  * TS 24.501 §5.3.13A     Forbidden PLMN lists.
  * TS 24.501 §5.3.20      UE behaviour on non-integrity-protected reject.

Deferred (TODOs at unimplemented surfaces):

  * TODO(spec: TS 23.041)  CBS / SBc-AP wire-level realisation
                           (page packing, msg-id allocation rules);
                           tester defers, mirrors metadata only.
  * TODO(spec: TS 22.346)  IOPS service requirements (mission-critical
                           voice/data prioritisation; USIM-based local AKA).
  * TODO(spec: TS 22.011)  Service accessibility — Access Class Barring
                           legacy semantics (UAC §4.5 supersedes in 5GS).
  * TODO(spec: TS 23.122)  UE-side NAS / PLMN-selection responses to
                           the rejects emitted by the operator gates.
"""

from __future__ import annotations

import random
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Optional


# ════════════════════════════════════════════════════════════════════
# PWS — TS 23.501 §4.4.1, §5.16.1
# ════════════════════════════════════════════════════════════════════

PWS_ALERT_TYPES = frozenset({"etws", "cmas", "eu_alert", "test"})
PWS_SEVERITIES = frozenset({"extreme", "severe", "moderate", "minor", "unknown"})
PWS_URGENCIES = frozenset({"immediate", "expected", "future", "past", "unknown"})
PWS_STATUSES = frozenset({"draft", "broadcasting", "completed", "cancelled"})

# CBS page payload size — GSM 7-bit packed, TS 23.041 limit. Used only
# for the page-count preview in encode_cbs_message.
PWS_CBS_PAGE_CHARS = 82
PWS_CBS_MAX_PAGES = 15


@dataclass
class PWSAlert:
    """Mirrors the pws_alerts row produced by safety/pws.CreateAlert."""
    id: int
    message_id: int
    serial_number: int
    alert_type: str = "cmas"
    message_text: str = ""
    language: str = "en"
    severity: str = "unknown"
    urgency: str = "unknown"
    category: str = "safety"
    target_areas: Optional[list] = None
    number_of_broadcasts: int = 10
    repetition_period_s: int = 60
    status: str = "draft"
    broadcast_at: Optional[str] = None
    completed_at: Optional[str] = None


def create_alert(store: dict, message_text: str, *,
                 alert_type: str = "cmas",
                 severity: str = "unknown",
                 urgency: str = "unknown",
                 category: str = "safety",
                 language: str = "en",
                 target_areas: Optional[list] = None,
                 number_of_broadcasts: int = 10,
                 repetition_period_s: int = 60) -> PWSAlert:
    """TS 23.501 §5.16.1 — create a PWS alert in 'draft' state.

    The store is a plain dict {id: PWSAlert} acting as the in-memory
    table. Validation mirrors the SQL CHECK constraints on the
    pws_alerts table on the Go side.
    """
    if not message_text:
        raise ValueError("message_text is required")
    if alert_type not in PWS_ALERT_TYPES:
        raise ValueError(f"invalid alert_type: {alert_type!r}")
    if severity not in PWS_SEVERITIES:
        raise ValueError(f"invalid severity: {severity!r}")
    if urgency not in PWS_URGENCIES:
        raise ValueError(f"invalid urgency: {urgency!r}")
    next_id = (max(store) + 1) if store else 1
    alert = PWSAlert(
        id=next_id,
        message_id=random.randint(0, 65535),
        serial_number=random.randint(0, 65535),
        alert_type=alert_type,
        message_text=message_text,
        language=language,
        severity=severity,
        urgency=urgency,
        category=category,
        target_areas=list(target_areas) if target_areas else None,
        number_of_broadcasts=number_of_broadcasts,
        repetition_period_s=repetition_period_s,
        status="draft",
    )
    store[next_id] = alert
    return alert


def broadcast_alert(store: dict, alert_id: int) -> PWSAlert:
    """TS 23.501 §5.16.1 — flip draft → broadcasting."""
    alert = store.get(alert_id)
    if alert is None:
        raise KeyError(f"alert {alert_id} not found")
    if alert.status != "draft":
        raise ValueError(f"alert {alert_id} not in draft state")
    alert.status = "broadcasting"
    alert.broadcast_at = datetime.now(tz=timezone.utc).isoformat()
    return alert


def cancel_alert(store: dict, alert_id: int) -> PWSAlert:
    """TS 38.413 §8.9.2 — Cancel transitions our row to 'cancelled'
    before the AMF fan-out runs the NGAP cancel procedure."""
    alert = store.get(alert_id)
    if alert is None:
        raise KeyError(f"alert {alert_id} not found")
    if alert.status in ("draft", "broadcasting"):
        alert.status = "cancelled"
    return alert


def complete_alert(store: dict, alert_id: int) -> PWSAlert:
    """TS 23.501 §5.16.1 — broadcasting → completed when the operator
    declares the warning over (without a Cancel that would Kill any
    cached message in the UE)."""
    alert = store.get(alert_id)
    if alert is None:
        raise KeyError(f"alert {alert_id} not found")
    if alert.status == "broadcasting":
        alert.status = "completed"
        alert.completed_at = datetime.now(tz=timezone.utc).isoformat()
    return alert


def encode_cbs_message(text: str, message_id: int, serial_number: int) -> dict:
    """TS 23.041 placeholder — return CBS-page metadata only.

    Real packed payload (GSM 7-bit packing into 82-char pages, max 15
    pages) is the CBC's job. We surface page count + IDs so the
    operator panel can preview "this alert will take N pages".

    TODO(spec: TS 23.041) — produce actual packed bytes (CB-DATA pages
    with header + GSM 7-bit body) once the SBc-AP layer is in.
    """
    pages = max(1, (len(text) + PWS_CBS_PAGE_CHARS) // (PWS_CBS_PAGE_CHARS + 1))
    pages = min(pages, PWS_CBS_MAX_PAGES)
    return {
        "message_id": message_id,
        "serial_number": serial_number,
        "pages": pages,
        "text_length": len(text),
        "encoding": "gsm7",  # placeholder
    }


# ════════════════════════════════════════════════════════════════════
# IOPS — TS 23.401 Annex K
# ════════════════════════════════════════════════════════════════════

IOPS_STATES = ("normal", "backhaul_lost", "iops_activated", "restoring", "failed")

# Valid forward transitions — TS 23.401 §K.2.4 lifecycle.
IOPS_VALID_TRANSITIONS: dict[str, frozenset[str]] = {
    "normal":         frozenset({"backhaul_lost"}),
    "backhaul_lost":  frozenset({"iops_activated", "normal", "failed"}),
    "iops_activated": frozenset({"restoring", "failed"}),
    "restoring":      frozenset({"normal", "iops_activated"}),
    "failed":         frozenset({"normal"}),
}

# Mapping target-state → iops_events.event_type accepted by the schema.
IOPS_STATE_TO_EVENT: dict[str, str] = {
    "backhaul_lost":  "backhaul_lost",
    "iops_activated": "iops_activated",
    "restoring":      "restoring",
    "normal":         "restored",
    "failed":         "failed",
}

IOPS_DEFAULT_LOCAL_SERVICES = (
    {"name": "emergency", "enabled": True, "priority": 1, "rate_limit_kbps": 0},
    {"name": "ptt",       "enabled": True, "priority": 2, "rate_limit_kbps": 0},
    {"name": "voice",     "enabled": True, "priority": 3, "rate_limit_kbps": 64},
    {"name": "data",      "enabled": True, "priority": 4, "rate_limit_kbps": 256},
)


def iops_can_transition(current: str, target: str) -> bool:
    """TS 23.401 §K.2.4 — return True iff `current → target` is allowed."""
    if current not in IOPS_VALID_TRANSITIONS:
        return False
    return target in IOPS_VALID_TRANSITIONS[current]


@dataclass
class IOPSGnbState:
    """Per-gNB state cell. `events` is the chronological event_type log."""
    gnb_id: str
    state: str = "normal"
    events: list = field(default_factory=list)


def iops_transition(gnb: IOPSGnbState, target: str, reason: str = "") -> bool:
    """Attempt one transition. Returns True iff applied; False on a
    rejected transition (gnb unchanged)."""
    if not iops_can_transition(gnb.state, target):
        return False
    gnb.state = target
    gnb.events.append({"event_type": IOPS_STATE_TO_EVENT[target], "reason": reason})
    return True


@dataclass
class IOPSCachedCredential:
    """TS 23.401 §K.2.3 — pre-computed AKA challenge tuple cached for
    the Local EPC to replay during IOPS."""
    gnb_id: str
    imsi: str
    rand_hex: str
    autn_hex: str
    xres_star_hex: str
    kseaf_hex: str
    expires_at: datetime  # UTC


def iops_cache_credential(cache: dict, c: IOPSCachedCredential) -> None:
    """UPSERT an AKA tuple into the Local EPC cache scoped to (gnb_id, imsi)."""
    if not c.gnb_id or not c.imsi:
        raise ValueError("gnb_id and imsi are required")
    cache[(c.gnb_id, c.imsi)] = c


def iops_local_authenticate(cache: dict, gnb_id: str, imsi: str,
                            now: Optional[datetime] = None) -> dict:
    """Look up a cached AKA tuple. Returns dict with `allowed` + reason."""
    now = now or datetime.now(tz=timezone.utc)
    c = cache.get((gnb_id, imsi))
    if c is None:
        return {"allowed": False, "reason": "no fresh cached credential"}
    exp = c.expires_at
    if exp.tzinfo is None:
        exp = exp.replace(tzinfo=timezone.utc)
    if exp <= now:
        return {"allowed": False, "reason": "no fresh cached credential"}
    return {
        "allowed": True, "gnb_id": gnb_id, "imsi": imsi,
        "method": "cached_aka", "expires_at": exp.isoformat(),
    }


def iops_check_service_available(state: str, service_name: str) -> bool:
    """TS 23.401 §K.2.4 — under non-normal state only the curated set
    of services is admissible (TODO(spec: TS 22.346) for real priority)."""
    if state == "normal":
        return True
    for svc in IOPS_DEFAULT_LOCAL_SERVICES:
        if svc["name"] == service_name and svc["enabled"]:
            return True
    return False


# ════════════════════════════════════════════════════════════════════
# Disaster Roaming — TS 22.261 §6.31, TS 23.501 §5.40
# ════════════════════════════════════════════════════════════════════


@dataclass
class DisasterDeclaration:
    """Mirror of disaster_declarations row.

    Per TS 22.261 §6.31.2.2 the declaration is the precondition for any
    Disaster-Roaming admission decision.
    """
    id: int
    name: str
    reason: str = ""
    affected_areas: str = ""
    status: str = "active"
    declared_by: str = "system"
    declared_at: Optional[str] = None
    ended_at: Optional[str] = None


@dataclass
class DisasterAdmissionResult:
    """Outcome of disaster_check_admission — mirrors AdmissionResult."""
    allowed: bool
    reason: str
    disaster_active: bool
    declaration_id: int = 0
    normal_roaming: bool = False


def declare_disaster(store: list, name: str, reason: str = "",
                     affected_areas: str = "",
                     declared_by: str = "system") -> DisasterDeclaration:
    """TS 22.261 §6.31.2 — record a new active Disaster Condition."""
    if not name:
        raise ValueError("name is required")
    next_id = (store[-1].id + 1) if store else 1
    decl = DisasterDeclaration(
        id=next_id,
        name=name,
        reason=reason,
        affected_areas=affected_areas,
        status="active",
        declared_by=declared_by or "system",
        declared_at=datetime.now(tz=timezone.utc).isoformat(),
    )
    store.append(decl)
    return decl


def end_disaster(store: list) -> int:
    """TS 23.501 §5.40.5 — close every active declaration. Returns
    the number of rows ended. Ended rows survive in history."""
    n = 0
    now = datetime.now(tz=timezone.utc).isoformat()
    for d in store:
        if d.status == "active":
            d.status = "ended"
            d.ended_at = now
            n += 1
    return n


def end_disaster_by_id(store: list, decl_id: int) -> bool:
    """End one specific active declaration; leave others alone."""
    for d in store:
        if d.id == decl_id and d.status == "active":
            d.status = "ended"
            d.ended_at = datetime.now(tz=timezone.utc).isoformat()
            return True
    return False


def is_disaster_active(store: list) -> bool:
    return any(d.status == "active" for d in store)


def get_active_declaration(store: list) -> Optional[DisasterDeclaration]:
    """Return the most recently declared still-active Condition (newest by id)."""
    actives = [d for d in store if d.status == "active"]
    return max(actives, key=lambda d: d.id) if actives else None


def disaster_check_admission(store: list, normal_agreements: set,
                             imsi: str, hplmn: str) -> DisasterAdmissionResult:
    """TS 23.501 §5.40.1 — admit a UE iff a Disaster Condition is
    active. Tag whether the UE's HPLMN already had normal roaming.

    `normal_agreements` is a set of HPLMN strings with active normal
    roaming (mirrors a SELECT from roaming_agreements).
    """
    decl = get_active_declaration(store)
    if decl is None:
        return DisasterAdmissionResult(
            allowed=False, reason="no active disaster declaration",
            disaster_active=False,
        )
    normal = hplmn in normal_agreements
    reason = (
        f"disaster active but PLMN {hplmn} already has normal roaming agreement"
        if normal else
        f"disaster roaming: PLMN {hplmn} admitted (no agreement)"
    )
    return DisasterAdmissionResult(
        allowed=True, reason=reason, disaster_active=True,
        declaration_id=decl.id, normal_roaming=normal,
    )


# ════════════════════════════════════════════════════════════════════
# Access Restriction — TS 24.501 §4.5, §5.3.13, §5.3.13A
# ════════════════════════════════════════════════════════════════════


ACCESS_DECISIONS = frozenset({"allow", "deny"})


@dataclass
class AccessForbiddenTAI:
    """Mirror of access_forbidden_tai row (TS 24.501 §5.3.13)."""
    plmn_id: str
    tac: str
    reason: str = ""
    added_by: str = "operator"


@dataclass
class AccessForbiddenPLMN:
    """Mirror of access_forbidden_plmn row (TS 24.501 §5.3.13A)."""
    plmn_id: str
    reason: str = ""
    added_by: str = "operator"


@dataclass
class AccessUACRule:
    """Mirror of access_uac_barring row (TS 24.501 §4.5).

    barring_factor in [0.0, 1.0] — probability of barring.
    barring_time_s — back-off seconds the UE must wait after a bar.
    """
    access_category: int
    barring_factor: float = 1.0
    barring_time_s: int = 0
    enabled: bool = True


@dataclass
class AccessCheckResult:
    """Mirror of CheckResult — what CheckAccess() returns."""
    allowed: bool
    reason: str
    cause_ref: str = ""


def add_forbidden_tai(store: dict, plmn_id: str, tac: str,
                      reason: str = "", added_by: str = "operator") -> None:
    """TS 24.501 §5.3.13 — UPSERT a (plmn_id, tac) onto the deny-list."""
    if not plmn_id or not tac:
        raise ValueError("plmn_id and tac are required")
    store[(plmn_id, tac)] = AccessForbiddenTAI(
        plmn_id=plmn_id, tac=tac, reason=reason,
        added_by=added_by or "operator",
    )


def is_forbidden_tai(store: dict, plmn_id: str, tac: str) -> bool:
    return (plmn_id, tac) in store


def add_forbidden_plmn(store: dict, plmn_id: str,
                       reason: str = "", added_by: str = "operator") -> None:
    """TS 24.501 §5.3.13A — UPSERT a PLMN onto the deny-list."""
    if not plmn_id:
        raise ValueError("plmn_id is required")
    store[plmn_id] = AccessForbiddenPLMN(
        plmn_id=plmn_id, reason=reason, added_by=added_by or "operator",
    )


def is_forbidden_plmn(store: dict, plmn_id: str) -> bool:
    return plmn_id in store


def set_uac_barring(store: dict, category: int, barring_factor: float,
                    barring_time_s: int, enabled: bool = True) -> None:
    """TS 24.501 §4.5 — UPSERT a barring rule for one access category."""
    if not 0 <= category <= 63:
        raise ValueError("access_category must be in [0, 63]")
    if not 0.0 <= barring_factor <= 1.0:
        raise ValueError("barring_factor must be in [0.0, 1.0]")
    store[category] = AccessUACRule(
        access_category=category, barring_factor=barring_factor,
        barring_time_s=barring_time_s, enabled=enabled,
    )


def evaluate_uac_barring(store: dict, category: int,
                         rng: Optional[random.Random] = None) -> tuple:
    """TS 24.501 §4.5 — return (barred, backoff_seconds)."""
    rule = store.get(category)
    if rule is None or not rule.enabled:
        return (False, 0)
    if rule.barring_factor >= 1.0:
        return (True, rule.barring_time_s)
    if rule.barring_factor <= 0.0:
        return (False, 0)
    r = rng or random
    if r.random() < rule.barring_factor:
        return (True, rule.barring_time_s)
    return (False, 0)


def check_access(forbidden_plmn_store: dict,
                 forbidden_tai_store: dict,
                 uac_store: dict,
                 imsi: str, plmn_id: str, tac: str,
                 category: int = -1,
                 rng: Optional[random.Random] = None) -> AccessCheckResult:
    """Composite gate — Forbidden-PLMN then Forbidden-TAI then UAC.

    First failure short-circuits with a §-cited cause. Empty `tac`
    skips the TAI check; negative `category` skips UAC.
    """
    if plmn_id and is_forbidden_plmn(forbidden_plmn_store, plmn_id):
        return AccessCheckResult(
            allowed=False,
            reason=f"PLMN {plmn_id} is on operator forbidden list",
            cause_ref="TS 24.501 §5.3.13A",
        )
    if plmn_id and tac and is_forbidden_tai(forbidden_tai_store, plmn_id, tac):
        return AccessCheckResult(
            allowed=False,
            reason=f"TAI {plmn_id}/{tac} is on operator forbidden list",
            cause_ref="TS 24.501 §5.3.13",
        )
    if category >= 0:
        barred, backoff = evaluate_uac_barring(uac_store, category, rng)
        if barred:
            extra = f" (back-off {backoff}s)" if backoff > 0 else ""
            return AccessCheckResult(
                allowed=False,
                reason=f"UAC barred access_category={category}{extra}",
                cause_ref="TS 24.501 §4.5",
            )
    return AccessCheckResult(allowed=True, reason="no operator gate matched")

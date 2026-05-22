# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""SEAL primitives — tester-side mirror.

Mirrors the Go core's services/seal package as a pure-codec
dataclass module. No live SEAL servers; suitable for round-trip
fixtures alongside the live behaviour tests.

Spec anchors (§-checked by speccheck against PDFs in specs/common/):

  * TS 23.434 §6            SEAL functional model — common SEAL
                            services bus + per-service functions.
  * TS 23.434 §9            Location management (LMS).
  * TS 23.434 §10           Group management (GMS).
  * TS 23.434 §10.3         Group / membership procedures + info
                            flows (create / update / delete /
                            notification).
  * TS 23.434 §11           Configuration management (CMS).
  * TS 23.434 §12           Identity management (IdMS) — VAL user
                            identity allocation / OpenID Connect.
  * TS 24.546 §5            SEAL CM protocol (UE ↔ CMS).
  * TS 24.547 §5            SEAL IdMS protocol (UE ↔ IdMS).
  * TS 24.548 §5            SEAL GM protocol (UE ↔ GMS).

Deferred (TODO):

  * TS 23.434 §9.3          On-demand "report-now" location request.
  * TS 23.434 §12           Federated VAL identity + OAuth2 tokens.
  * TS 23.434 §13           Key management (KMS) — TS 33.180.
  * TS 23.434 §14           Network Resource Management.
  * TS 24.546 §6            CMS notification channel for live UEs.
  * TS 24.547 §6            OAuth2 token refresh + revocation.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional


VALID_ROLES = frozenset({"admin", "member", "viewer"})
VALID_TARGET_TYPES_LOC = frozenset({"imsi", "group"})
VALID_TARGET_TYPES_CFG = frozenset({"imsi", "group", "app"})


# ─── Group + members (TS 23.434 §10) ─────────────────────────────


@dataclass
class Group:
    name: str
    description: Optional[str] = None
    app_id: Optional[str] = None
    config_json: Optional[str] = None


def create_group(name: str, description: str = "",
                 app_id: str = "",
                 config_json: Optional[str] = None) -> Group:
    """TS 23.434 §10.3 — GMS-side create (Group creation request)."""
    if not name:
        raise ValueError("group name is required")
    return Group(name=name,
                 description=description or None,
                 app_id=app_id or None,
                 config_json=config_json)


@dataclass
class Member:
    group_name: str
    imsi: str
    role: str = "member"


def add_member(group: Group, imsi: str, role: str = "") -> Member:
    """TS 23.434 §10.3 — add a VAL participant (Group membership
    update request)."""
    if not role:
        role = "member"
    if role not in VALID_ROLES:
        raise ValueError(f"invalid role: {role!r}")
    if not imsi:
        raise ValueError("imsi is required")
    return Member(group_name=group.name, imsi=imsi, role=role)


# ─── Location subscriptions (TS 23.434 §9 LMS) ───────────────────


@dataclass
class LocationSub:
    target_type: str
    target_id: str
    callback_url: str
    interval_s: int = 60
    active: bool = True


def create_location_sub(target_type: str, target_id: str,
                        callback_url: str,
                        interval_s: int = 0) -> LocationSub:
    """TS 23.434 §9 — periodic location-push subscription.

    TODO(TS 23.434 §9.3): on-demand "report-now" location request
    is not modelled — only periodic push.
    """
    if target_type not in VALID_TARGET_TYPES_LOC:
        raise ValueError(f"target_type must be one of {VALID_TARGET_TYPES_LOC}")
    if not target_id:
        raise ValueError("target_id is required")
    if not callback_url:
        raise ValueError("callback_url is required")
    if interval_s <= 0:
        interval_s = 60
    return LocationSub(target_type=target_type, target_id=target_id,
                       callback_url=callback_url,
                       interval_s=interval_s, active=True)


def deactivate_location_sub(sub: LocationSub) -> LocationSub:
    sub.active = False
    return sub


# ─── Config items (TS 23.434 §11 CMS) ────────────────────────────


@dataclass
class ConfigItem:
    target_type: str
    target_id: str
    config_key: str
    config_value: Optional[str] = None


def set_config(items: dict, target_type: str, target_id: str,
               config_key: str, config_value: str) -> ConfigItem:
    """Mirrors safety/seal.SetConfig — UPSERT semantics.

    ``items`` is the test-local dict mirror of the seal_configs table,
    keyed by ``(target_type, target_id, config_key)``.

    TODO(TS 24.546 §6): notify live VAL UEs that their config changed
    (the spec defines a notification channel on SEAL-CM).
    """
    if target_type not in VALID_TARGET_TYPES_CFG:
        raise ValueError(f"target_type must be one of {VALID_TARGET_TYPES_CFG}")
    if not config_key:
        raise ValueError("config_key is required")
    item = ConfigItem(target_type=target_type, target_id=target_id,
                      config_key=config_key, config_value=config_value)
    items[(target_type, target_id, config_key)] = item
    return item


def get_config(items: dict, target_type: str, target_id: str,
               config_key: str) -> Optional[ConfigItem]:
    return items.get((target_type, target_id, config_key))


# ─── VAL user identity (TS 23.434 §12 IdMS) ──────────────────────


@dataclass
class VALUser:
    val_user_id: str
    imsi: str
    app_id: Optional[str] = None


def map_val_user(users: dict, val_user_id: str, imsi: str,
                 app_id: Optional[str] = None) -> VALUser:
    """TS 23.434 §12 — bind a VAL user identity to a UE identity.

    TODO(TS 24.547 §5 / §6): OAuth2 token issuance + refresh +
    revocation for VAL user authentication — today we only model the
    underlying val_user_id ↔ IMSI binding.
    """
    if not val_user_id or not imsi:
        raise ValueError("val_user_id and imsi required")
    user = VALUser(val_user_id=val_user_id, imsi=imsi, app_id=app_id)
    users[val_user_id] = user
    return user


def resolve_val_user(users: dict, val_user_id: str) -> Optional[VALUser]:
    return users.get(val_user_id)


def resolve_imsi(users: dict, imsi: str) -> list[VALUser]:
    return [u for u in users.values() if u.imsi == imsi]


def unmap_val_user(users: dict, val_user_id: str) -> bool:
    return users.pop(val_user_id, None) is not None

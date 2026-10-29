# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""5G ProSe (D2D) primitives — tester-side mirror.

Mirrors the Go core's services/prose package as a pure-codec
dataclass module. No live PCF, no DB; suitable for round-trip
fixtures alongside the live behaviour tests.

Spec anchors (§-checked by speccheck against PDFs in specs/common/):

  * TS 22.278 §6            5G ProSe service requirements (direct
                            discovery, direct communication,
                            UE-to-NW relay, UE-to-UE relay).
  * TS 23.304 §4.2          Reference architecture for 5G ProSe.
  * TS 23.304 §5.1          5G ProSe authorization & policy
                            provisioning (PCF → UE).
  * TS 23.304 §5.2          5G ProSe Direct Discovery (Models A & B).
  * TS 23.304 §5.3          5G ProSe Direct Communication —
                            broadcast (§5.3.2), groupcast (§5.3.3),
                            unicast (§5.3.4) on PC5.
  * TS 23.304 §5.4          UE-to-Network relay (Layer-3 5G ProSe).
  * TS 23.304 §5.5          UE-to-UE relay.
  * TS 24.554 §5            5G ProSe NAS-layer procedures (PC3a / PC3).
  * TS 24.555 §5            5G ProSe PC5 signalling protocol —
                            link establishment / modification / release.

Deferred (TODO):

  * TS 23.304 §5.2.4        Restricted (closed) discovery — today we
                            model only "open" discovery.
  * TS 23.304 §5.2.6        Discovery message protection (5G ProSe
                            code material).
  * TS 24.555 §6            PC5 security activation (PC5 RRC keys).
  * TS 33.503               5G ProSe security — credential delivery.
"""

from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Optional


# ─── UE config (TS 23.304 §5.1 policy provisioning) ──────────────


@dataclass
class UEConfig:
    """Mirrors prose_ue_config — the per-feature authorization flags
    the PCF carries to a 5G ProSe UE."""

    imsi: str
    authorized: bool = False
    discovery_enabled: bool = True
    communication_enabled: bool = True
    relay_capable: bool = False
    relay_enabled: bool = False


def authorize_ue(cfg: Optional[UEConfig], service: str) -> bool:
    """TS 23.304 §5.1 authorization predicate (per-service gating).

    Service must be one of: ``discovery``, ``communication``, ``relay``.
    Relay requires BOTH ``relay_capable`` and ``relay_enabled``.
    """
    if cfg is None or not cfg.authorized:
        return False
    if service == "discovery":
        return cfg.discovery_enabled
    if service == "communication":
        return cfg.communication_enabled
    if service == "relay":
        return cfg.relay_enabled and cfg.relay_capable
    return False


# ─── Discovery (TS 23.304 §5.2) ──────────────────────────────────


@dataclass
class Announcement:
    """One Model-A "I am here" announcement."""

    imsi: str
    app_code: str
    metadata: dict = field(default_factory=dict)
    announced_at: float = 0.0
    expires_at: float = 0.0


def announce(cfg: UEConfig, app_code: str, validity_s: int = 3600,
             metadata: Optional[dict] = None,
             now: Optional[float] = None) -> Optional[Announcement]:
    """TS 23.304 §5.2 Model A — "I am here"."""
    if not authorize_ue(cfg, "discovery"):
        return None
    if validity_s <= 0:
        validity_s = 3600
    n = now if now is not None else time.time()
    return Announcement(
        imsi=cfg.imsi, app_code=app_code,
        metadata=metadata or {},
        announced_at=n, expires_at=n + validity_s,
    )


def monitor(cfg: UEConfig, announcements: list[Announcement], *,
            exclude_self: bool = True,
            app_code_filter: Optional[str] = None,
            now: Optional[float] = None) -> list[Announcement]:
    """TS 23.304 §5.2 Model B — "Who is there?".

    Filters out expired and (by default) self-announcements.
    """
    if not authorize_ue(cfg, "discovery"):
        return []
    n = now if now is not None else time.time()
    out: list[Announcement] = []
    for a in announcements:
        if a.expires_at < n:
            continue
        if exclude_self and a.imsi == cfg.imsi:
            continue
        if app_code_filter and a.app_code != app_code_filter:
            continue
        out.append(a)
    return out


# ─── Communication (TS 23.304 §5.3) ──────────────────────────────


@dataclass
class Session:
    session_type: str   # 'unicast' | 'groupcast' | 'broadcast' | 'relay'
    source_imsi: str
    target_imsi: Optional[str] = None
    group_id: Optional[str] = None
    relay_imsi: Optional[str] = None
    service: Optional[str] = None
    status: str = "active"


def setup_unicast(src: UEConfig, tgt: UEConfig,
                  service: str = "") -> Optional[Session]:
    """TS 23.304 §5.3.4 — Direct Communication, unicast (procedure
    detail at TS 23.304 §6.4.3.1).

    TODO(TS 24.555 §5): real PC5-S Direct Link Establishment Request /
    Accept exchange — today we just construct the session row.
    """
    if not authorize_ue(src, "communication"):
        return None
    if not authorize_ue(tgt, "communication"):
        return None
    return Session(session_type="unicast",
                   source_imsi=src.imsi, target_imsi=tgt.imsi,
                   service=service or None, status="active")


def setup_groupcast(src: UEConfig, group_id: str,
                    service: str = "") -> Optional[Session]:
    """TS 23.304 §5.3.3 — Direct Communication, groupcast."""
    if not authorize_ue(src, "communication"):
        return None
    return Session(session_type="groupcast",
                   source_imsi=src.imsi, group_id=group_id,
                   service=service or None, status="active")


def release_session(s: Session) -> Session:
    """TS 23.304 §6.4.3.3 — Layer-2 link release over PC5."""
    if s.status == "active":
        s.status = "released"
    return s


# ─── Relay (TS 23.304 §5.4) ──────────────────────────────────────


@dataclass
class RelayEntry:
    imsi: str
    service_code: str
    connectivity: str = "5gc"
    expires_at: float = 0.0


def register_relay(cfg: UEConfig, service_code: str,
                   connectivity: str = "5gc",
                   validity_s: float = 1800.0,
                   now: Optional[float] = None) -> Optional[RelayEntry]:
    """TS 23.304 §5.4 — Layer-3 5G ProSe relay registration."""
    if not authorize_ue(cfg, "relay"):
        return None
    n = now if now is not None else time.time()
    return RelayEntry(imsi=cfg.imsi, service_code=service_code,
                      connectivity=connectivity or "5gc",
                      expires_at=n + validity_s)


def discover_relays(cfg: UEConfig, registry: list[RelayEntry], *,
                    service_code: Optional[str] = None,
                    now: Optional[float] = None) -> list[RelayEntry]:
    """TS 23.304 §5.4 — relay discovery (uses Direct Discovery)."""
    if not authorize_ue(cfg, "discovery"):
        return []
    n = now if now is not None else time.time()
    return [e for e in registry
            if e.expires_at >= n
            and e.imsi != cfg.imsi
            and (not service_code or e.service_code == service_code)]


def connect_via_relay(remote: UEConfig,
                      relay: Optional[RelayEntry],
                      now: Optional[float] = None) -> Optional[Session]:
    """TS 23.304 §5.4 — UE-to-Network relay session establishment."""
    if not authorize_ue(remote, "communication"):
        return None
    if relay is None:
        return None
    n = now if now is not None else time.time()
    if relay.expires_at < n:
        return None
    return Session(session_type="relay",
                   source_imsi=remote.imsi, target_imsi=relay.imsi,
                   relay_imsi=relay.imsi, service=relay.service_code,
                   status="active")

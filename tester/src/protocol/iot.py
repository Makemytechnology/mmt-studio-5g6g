# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Cellular IoT primitives — tester-side mirror.

Mirrors the Go core's iot/{nbiot,nidd,ambient,redcap} packages.
Pure logic — no I/O, no live core state — suitable for round-tripping
fixtures alongside the SCEF / NIDD / PSM behaviour tests.

Spec anchors (PDFs under specs/common/):

  * TS 23.401 §4.3.17    Machine-Type Communications support over
                         the EPS — the umbrella for the CIoT Control
                         Plane / User Plane optimisations the SCEF
                         and NIDD surfaces ride on.
  * TS 23.401 §4.3.17.8  Non-IP Data Delivery (NIDD) using SCEF —
                         the CP-only data path NB-IoT uses when no
                         PDN connection is available.
  * TS 23.401 §4.3.22    UE Power Saving Mode (PSM) — T3324
                         (Active Time) + T3412-extended (Periodic
                         TAU) negotiation; UE unreachable while in
                         PSM until the next TAU wakes it.
  * TS 23.401 §5.13a     Extended idle-mode DRX (eDRX) — eDRX cycle
                         + Paging Time Window (PTW) negotiation.
  * TS 23.682 §5.13      NIDD procedures (T6a/T6b establishment,
                         configuration, MO/MT NIDD, release).
  * TS 23.682 §5.13.3    MT NIDD with high-latency communication —
                         buffering at SCEF when UE is in PSM /
                         unreachable; deliver on UE next contact.
  * TS 23.682 §5.13.4    MO NIDD — UE to AS, immediate forwarding
                         from SCEF.
  * TS 22.369 §4.2       Ambient IoT characteristics — energy-
                         harvested or capacitor-storage devices.
  * TS 22.369 §4.4       Ambient IoT communication topologies
                         (1: BS↔Tag, 2: BS↔UE↔Tag, 3: BS↔Node↔Tag).
  * TS 22.369 §5.2       Tag class A/B/C distinction.
  * TS 22.369 §6.2       Inventory KPIs.
  * TS 23.501 §5.41      NR RedCap and NR eRedCap differentiation
                         in the CN — RAT-type identifier values
                         and Dual-Connectivity prohibition.

Deferred / TODO:

  * TS 24.301 §5.5.1.2.4 — NB-S1 capability container decoding (the
                           NAS IE that reports CE level / multi-tone
                           / CP-CIoT / UP-CIoT capability bits).
  * TS 29.122            — SCEF T8 northbound API (App Server
                           registration, callback delivery).
  * TS 24.250 §6         — RDS PDU wrapping for reliable delivery
                           of CIoT NIDD payloads.
  * TS 38.306            — NR UE-Capability per-band RedCap bits.
  * TS 23.369            — Stage-2 architecture for Ambient IoT
                           (not yet at Rel-19 floor).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional


# ─── NB-IoT PSM (TS 23.401 §4.3.22) ──────────────────────────────


# PSM lifecycle states. The state strings mirror what the Go core
# persists in iot_psm_state.state; keep these stable so dashboards
# bind to the same vocabulary.
PSM_STATES = frozenset({"active", "sleeping", "unreachable"})


@dataclass
class PSMState:
    """TS 23.401 §4.3.22 PSM timer + state row."""

    imsi: str
    t3324_sec: int           # Active Time
    t3412_ext_sec: int       # Periodic TAU
    state: str = "active"    # active | sleeping | unreachable


def set_psm(imsi: str, t3324: int, t3412_ext: int) -> PSMState:
    """Negotiate PSM timers. Both timers must be > 0 — the MME has
    already validated the NAS IE values before this layer sees them.
    """
    if not imsi:
        raise ValueError("imsi is required")
    if t3324 <= 0:
        raise ValueError("t3324_sec must be > 0")
    if t3412_ext <= 0:
        raise ValueError("t3412_ext_sec must be > 0")
    return PSMState(imsi=imsi, t3324_sec=t3324, t3412_ext_sec=t3412_ext)


def enter_sleep(s: PSMState) -> PSMState:
    """active → sleeping. Computes next-wakeup implicitly (the test
    side only tracks the state transition; the Go core stamps the
    next_wakeup column with now + T3412_ext)."""
    if s.t3412_ext_sec <= 0:
        raise ValueError("PSM not configured (t3412_ext=0)")
    s.state = "sleeping"
    return s


def mark_unreachable(s: PSMState) -> PSMState:
    """sleeping → unreachable. From any other state, no-op (the UE
    that never went to sleep can't be unreachable in PSM sense)."""
    if s.state == "sleeping":
        s.state = "unreachable"
    return s


def wake(s: PSMState) -> PSMState:
    """sleeping/unreachable → active. Clears any sleep timestamps."""
    s.state = "active"
    return s


# ─── NB-IoT eDRX (TS 23.401 §5.13a) ──────────────────────────────


@dataclass
class EDRXConfig:
    """TS 23.401 §5.13a eDRX cycle + PTW row."""

    imsi: str
    device_type: str           # nbiot | ltem
    edrx_cycle_sec: float
    ptw_sec: float


def set_edrx(imsi: str, device_type: str, cycle_sec: float,
             ptw_sec: float) -> EDRXConfig:
    """Persist an eDRX configuration. cycle_sec > ptw_sec is a
    physical-layer invariant — the PTW lives inside the eDRX cycle."""
    if not imsi:
        raise ValueError("imsi is required")
    if cycle_sec <= 0 or ptw_sec <= 0:
        raise ValueError("eDRX cycle / PTW must be > 0")
    if ptw_sec >= cycle_sec:
        raise ValueError("PTW must be < eDRX cycle")
    return EDRXConfig(imsi=imsi, device_type=device_type,
                      edrx_cycle_sec=cycle_sec, ptw_sec=ptw_sec)


# ─── NB-IoT capability container (TODO TS 24.301 §5.5.1.2.4) ─────


@dataclass
class Capabilities:
    """NB-IoT UE capability bits. CE level 0..2 per the NB-S1
    capability container (TS 24.301 §5.5.1.2.4 — TODO: full
    decoder)."""

    imsi: str
    multi_tone: bool = False
    ce_level: int = 0           # 0..2
    cp_ciot_supported: bool = False
    up_ciot_supported: bool = False
    data_over_nas: bool = False


def set_capabilities(c: Capabilities) -> Capabilities:
    """Round-trip the capability bits with a CE level gate."""
    if c.ce_level < 0 or c.ce_level > 2:
        raise ValueError(f"ce_level must be 0..2 (got {c.ce_level})")
    return c


# ─── NIDD sessions + data path (TS 23.682 §5.13) ─────────────────


SESSION_STATES = frozenset({"active", "suspended", "terminated"})


@dataclass
class NiddSession:
    """TS 23.682 §5.13.2 NIDD configuration row."""

    session_id: str
    imsi: str
    apn: str
    app_server_url: str
    status: str = "active"


def create_session(imsi: str, session_id: str, apn: str,
                   app_server_url: str) -> NiddSession:
    """TS 23.682 §5.13.2 — persist a NIDD configuration with all
    required fields. Empty IMSI / APN / URL must error."""
    if not imsi:
        raise ValueError("imsi is required")
    if not apn:
        raise ValueError("apn is required")
    if not app_server_url:
        raise ValueError("app_server_url is required")
    if not session_id:
        session_id = f"nidd-{imsi}-{apn}"
    return NiddSession(session_id=session_id, imsi=imsi, apn=apn,
                       app_server_url=app_server_url)


def suspend_session(s: NiddSession) -> NiddSession:
    if s.status == "terminated":
        raise ValueError("cannot suspend a terminated session")
    s.status = "suspended"
    return s


def terminate_session(s: NiddSession) -> NiddSession:
    s.status = "terminated"
    return s


# Direction enum for the data log + CP path.
NIDD_DIRECTIONS = frozenset({"UL", "DL"})

# UE state values that the SCEF reads to decide whether to deliver
# now or buffer (TS 23.682 §5.13.3 high-latency communication).
UE_REACHABLE = frozenset({"active"})
UE_UNREACHABLE = frozenset({"sleeping", "unreachable"})


@dataclass
class DataLog:
    """TS 23.682 §5.13.3 / §5.13.4 NIDD data log entry."""

    session_id: str
    direction: str             # UL | DL
    data_hex: str
    data_length: int
    status: str                # delivered | buffered
    delivered: bool


def send_mo(s: NiddSession, payload: bytes) -> DataLog:
    """TS 23.682 §5.13.4 MO NIDD — UE → SCEF → AS, hex-encoded into
    the log row, immediately marked 'delivered'."""
    if s.status != "active":
        raise ValueError(f"cannot send MO on {s.status!r} session")
    if not payload:
        raise ValueError("payload is required")
    return DataLog(
        session_id=s.session_id, direction="UL",
        data_hex=payload.hex(), data_length=len(payload),
        status="delivered", delivered=True,
    )


def send_mt(s: NiddSession, payload: bytes, ue_state: str) -> DataLog:
    """TS 23.682 §5.13.3 MT NIDD — buffers when the UE is in PSM
    (sleeping/unreachable); delivers immediately when active."""
    if s.status != "active":
        raise ValueError(f"cannot send MT on {s.status!r} session")
    if not payload:
        raise ValueError("payload is required")
    delivered = ue_state in UE_REACHABLE
    return DataLog(
        session_id=s.session_id, direction="DL",
        data_hex=payload.hex(), data_length=len(payload),
        status="delivered" if delivered else "buffered",
        delivered=delivered,
    )


def flush_buffered(logs: list[DataLog]) -> int:
    """When the UE wakes from PSM, the SCEF flushes all buffered DL
    rows for this session (TS 23.682 §5.13.3 high-latency delivery).
    Returns the number of rows transitioned."""
    flushed = 0
    for d in logs:
        if d.direction == "DL" and d.status == "buffered":
            d.status = "delivered"
            d.delivered = True
            flushed += 1
    return flushed


# ─── CP CIoT data path (TS 23.401 §4.3.17.8) ─────────────────────


@dataclass
class CPData:
    """TS 23.401 §4.3.17.8 CP CIoT NAS-borne small-data payload."""

    imsi: str
    direction: str             # UL | DL
    payload_hex: str
    apn: Optional[str] = None
    delivered: bool = False


def append_cp(imsi: str, direction: str, payload: bytes,
              apn: Optional[str] = None) -> CPData:
    """Append a CP-CIoT data row (TS 23.401 §4.3.17.8). Direction
    must be UL or DL; empty payload errors."""
    if direction not in NIDD_DIRECTIONS:
        raise ValueError(f"direction must be UL or DL (got {direction!r})")
    if not payload:
        raise ValueError("payload is required")
    return CPData(imsi=imsi, direction=direction,
                  payload_hex=payload.hex(), apn=apn)


def mark_cp_delivered(d: CPData) -> CPData:
    d.delivered = True
    return d


# ─── App Server registry (SCEF T8 — TODO TS 29.122) ──────────────


@dataclass
class AppServer:
    """SCEF T8 callback endpoint (TODO TS 29.122 — full T8 surface)."""

    app_server_id: str
    name: str
    callback_url: str
    auth_token: Optional[str] = None


def register_app_server(app_server_id: str, name: str,
                        callback_url: str,
                        auth_token: Optional[str] = None) -> AppServer:
    if not app_server_id:
        raise ValueError("app_server_id is required")
    if not callback_url:
        raise ValueError("callback_url is required")
    return AppServer(app_server_id=app_server_id, name=name,
                     callback_url=callback_url, auth_token=auth_token)


# ─── Ambient IoT (TS 22.369) ─────────────────────────────────────


# TS 22.369 §5.2 tag classes — energy availability + duplex support.
VALID_TAG_CLASSES = frozenset({"A", "B", "C"})

# TS 22.369 §6.2 / §6.3 / §6.5 inventory event categories.
INVENTORY_EVENT_TYPES = frozenset({
    "inventory", "sensor_read", "actuator", "track",
})


@dataclass
class AmbientTag:
    """TS 22.369 §5.2 — ambient IoT tag with class A/B/C."""

    tag_id: str
    tag_class: str = "A"
    tag_type: str = "asset"
    group_id: Optional[str] = None
    owner: Optional[str] = None
    last_seen_at: Optional[str] = None
    last_reader_id: Optional[str] = None


def register_tag(tag_id: str, tag_class: str = "A",
                 tag_type: str = "asset",
                 group_id: Optional[str] = None,
                 owner: Optional[str] = None) -> AmbientTag:
    """TS 22.369 §5.2 — register a tag; class gate rejects D/E/etc."""
    if not tag_id:
        raise ValueError("tag_id is required")
    if not tag_class:
        tag_class = "A"
    if tag_class not in VALID_TAG_CLASSES:
        raise ValueError(f"tag_class must be A, B, or C (got {tag_class!r})")
    if not tag_type:
        tag_type = "asset"
    return AmbientTag(tag_id=tag_id, tag_class=tag_class,
                      tag_type=tag_type, group_id=group_id, owner=owner)


@dataclass
class AmbientReader:
    """TS 22.369 §4.4 — reader / interrogator. gnb_ip captures the
    gNB or Intermediate-node attach (Topology 1/2/3)."""

    reader_id: str
    gnb_ip: Optional[str] = None
    status: str = "active"
    last_heartbeat: Optional[str] = None


def register_reader(reader_id: str,
                    gnb_ip: Optional[str] = None) -> AmbientReader:
    if not reader_id:
        raise ValueError("reader_id is required")
    return AmbientReader(reader_id=reader_id, gnb_ip=gnb_ip)


@dataclass
class InventoryEvent:
    """TS 22.369 §6.2 inventory KPI capture."""

    reader_id: str
    event_type: str            # one of INVENTORY_EVENT_TYPES
    tags_found: int
    result_json: Optional[str] = None


def log_inventory(reader_id: str, event_type: str,
                  tags_found: int,
                  result_json: Optional[str] = None) -> InventoryEvent:
    if not reader_id:
        raise ValueError("reader_id is required")
    if not event_type:
        event_type = "inventory"
    return InventoryEvent(reader_id=reader_id, event_type=event_type,
                          tags_found=tags_found, result_json=result_json)


# ─── RedCap RAT-type helpers (TS 23.501 §5.41) ───────────────────


# TS 23.501 §5.41 — CN-only sub-types of NR RAT used to identify
# RedCap / eRedCap UEs.
RAT_TYPE_NR = "NR"
RAT_TYPE_NR_REDCAP = "NR_REDCAP"
RAT_TYPE_NR_EREDCAP = "NR_EREDCAP"


def is_redcap(rat_type: str) -> bool:
    """TS 23.501 §5.41 — true for NR_REDCAP and NR_EREDCAP."""
    return rat_type in (RAT_TYPE_NR_REDCAP, RAT_TYPE_NR_EREDCAP)


def is_dual_connectivity_allowed(rat_type: str) -> bool:
    """TS 23.501 §5.41: 'In this Release of the specification, the
    Dual Connectivity function does not apply to the NR RedCap UE.'
    eRedCap is grouped under the same prohibition pending a future
    Release relaxation."""
    return not is_redcap(rat_type)


def is_allowed_as_primary_rat(rat_type: str,
                              subscription_allows_as_primary: bool) -> bool:
    """TS 23.501 §5.41 — RedCap primary-RAT permission is a
    SUBSCRIPTION-derived gate, not a hard spec mandate. Default
    allow=True for non-RedCap; RedCap defers to the bit."""
    if not is_redcap(rat_type):
        return True
    return subscription_allows_as_primary

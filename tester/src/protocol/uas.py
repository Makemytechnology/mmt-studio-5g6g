# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""UAS / UAV primitives — tester-side mirror.

Mirrors the Go core's services/uas package as a pure-codec dataclass
module. No live UTM / USS, no DB; suitable for round-trip fixtures
alongside the live behaviour tests.

Spec anchors (§-checked by speccheck against PDFs in specs/common/):

  * TS 22.125 §5            UAS service requirements (UAV identity,
                            command/control, remote identification,
                            flight authorization).
  * TS 23.256 §4.2          Reference architecture for UAS (UAV-C2
                            between UAV and UAV-C; UAS via 5GS).
  * TS 23.256 §5.2.1        UAV authentication & authorization (UAA).
  * TS 23.256 §5.2.3        Pairing authorization between UAV and
                            UAV-C (the C2 pairing model).
  * TS 23.256 §5.2.4        UAV flight authorization with USS/UTM
                            (admit/deny + restrictions).
  * TS 23.256 §5.2.5        Remote identification (Net-RID).
  * TS 23.256 §5.2.6        UAV location reporting / tracking.
  * TS 23.256 §5.5          C2 communication — DCC, default 5QI=3.
  * ASTM F3411-22a §4       Remote ID broadcast message format
                            (informative; Net-RID references ASTM
                            F3411 / RFC 9153).

Deferred (TODO):

  * TS 23.256 §5.2.2        UAV-Map / DAA exchange with USS.
  * TS 23.256 §5.6          C2 link recovery (alternate DRB switch).
  * ASTM F3411 §5           Direct broadcast Remote ID (BLE / Wi-Fi NaN).
"""

from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Optional


# Default 5QI for UAV C2 PDU sessions per TS 23.256 §5.5 →
# TS 23.501 §5.7.4 / Table 5.7.4-1 (V2X / DCC characteristic).
C2_DEFAULT_5QI = 3


# ─── UAV registry (TS 23.256 §5.2.1) ─────────────────────────────


@dataclass
class UAV:
    """Mirrors uas_registry — registered UAV descriptor."""

    uav_id: str
    imsi: Optional[str] = None
    serial_number: Optional[str] = None
    manufacturer: Optional[str] = None
    model: Optional[str] = None
    max_speed_mps: float = 20.0
    max_altitude_m: float = 120.0   # FAA Part 107 / EASA Open default
    status: str = "registered"


def register_uav(imsi: str, uav_id: str, serial_number: str = "",
                 manufacturer: str = "", model: str = "",
                 max_speed_mps: float = 20.0,
                 max_altitude_m: float = 120.0) -> UAV:
    """TS 23.256 §5.2.1 UAA precondition — register a UAV."""
    if not uav_id:
        uav_id = f"UAV-{int(time.time() * 1e6) & 0xFFFFFFFF:08X}"
    return UAV(
        uav_id=uav_id, imsi=imsi or None,
        serial_number=serial_number or None,
        manufacturer=manufacturer or None,
        model=model or None,
        max_speed_mps=max_speed_mps,
        max_altitude_m=max_altitude_m,
    )


# ─── No-fly zones (TS 23.256 §5.2.4) ─────────────────────────────


@dataclass
class NoFlyZone:
    """USS-published forbidden volume (lat/lon box + optional alt cap)."""

    name: str
    lat_min: float
    lat_max: float
    lon_min: float
    lon_max: float
    alt_max_m: Optional[float] = None
    reason: Optional[str] = None


def waypoint_in_zone(wp: dict, zone: NoFlyZone) -> bool:
    """Return True if a waypoint dict {lat, lon, alt_m} hits a zone."""
    lat = wp.get("lat", 0.0)
    lon = wp.get("lon", 0.0)
    alt = wp.get("alt_m", 0.0)
    if not (zone.lat_min <= lat <= zone.lat_max):
        return False
    if not (zone.lon_min <= lon <= zone.lon_max):
        return False
    if zone.alt_max_m is None or zone.alt_max_m <= 0:
        return True
    return alt <= zone.alt_max_m


def check_no_fly_violations(plan: dict, zones: list[NoFlyZone]) -> list[dict]:
    """TS 23.256 §5.2.4 — UTM/USS check of flight plan vs no-fly map."""
    violations = []
    for wp in plan.get("waypoints", []):
        for z in zones:
            if waypoint_in_zone(wp, z):
                violations.append({"zone_name": z.name, "reason": z.reason})
    return violations


# ─── Flight authorization (TS 23.256 §5.2.4) ─────────────────────


def authorize_flight(uav: Optional[UAV], plan: dict,
                     zones: Optional[list[NoFlyZone]] = None) -> dict:
    """TS 23.256 §5.2.4 — local USS stand-in.

    Returns the same shape that the Go core's AuthorizeFlight does:
      ``{"authorized": bool, "error": str?, "violations": [...]?,
         "flight_id": str?, "restrictions": [...]?}``.
    """
    if uav is None:
        return {"authorized": False, "error": "UAV not found"}
    if uav.status == "deregistered":
        return {"authorized": False, "error": "UAV deregistered"}
    if uav.status == "grounded":
        return {"authorized": False, "error": "UAV grounded"}

    violations = check_no_fly_violations(plan, zones or [])
    if violations:
        return {"authorized": False, "error": "no-fly zone violation",
                "violations": violations}

    flight_id = f"FLT-{int(time.time() * 1e6) & 0xFFFFFFFF:08X}"
    restrictions = [
        f"max_speed={uav.max_speed_mps:.1f}m/s",
        f"max_alt={uav.max_altitude_m:.1f}m",
    ]
    return {"authorized": True, "flight_id": flight_id,
            "restrictions": restrictions}


# ─── Position + anomaly detection (TS 23.256 §5.2.6) ─────────────


@dataclass
class Position:
    uav_id: str
    latitude: float
    longitude: float
    altitude_m: float
    heading_deg: float = 0.0
    speed_mps: float = 0.0
    timestamp: str = ""


def detect_anomaly(uav: UAV, pos: Optional[Position],
                   has_active_flight: bool,
                   zones: Optional[list[NoFlyZone]] = None) -> dict:
    """TS 23.256 §5.2.6 — envelope-deviation detection."""
    if pos is None:
        return {"anomaly": False, "details": ["No position data"]}
    if not has_active_flight:
        return {"anomaly": False, "details": ["No active flight plan"]}

    details: list[str] = []
    if pos.altitude_m > uav.max_altitude_m:
        details.append(
            f"Altitude {pos.altitude_m:.1f}m exceeds max {uav.max_altitude_m:.1f}m")
    if pos.speed_mps > uav.max_speed_mps:
        details.append(
            f"Speed {pos.speed_mps:.1f}m/s exceeds max {uav.max_speed_mps:.1f}m/s")
    for z in zones or []:
        if z.lat_min <= pos.latitude <= z.lat_max and z.lon_min <= pos.longitude <= z.lon_max:
            details.append(f"Inside no-fly zone: {z.name}")

    return {"anomaly": bool(details), "details": details}


# ─── Net-RID (TS 23.256 §5.2.5) ──────────────────────────────────


def remote_id_broadcast(uav: UAV, pos: Optional[Position],
                        flight_id: Optional[str] = None) -> dict:
    """Build a Network Remote ID frame (TS 23.256 §5.2.5).

    Field semantics borrowed from ASTM F3411-22a §4 (UAType, IDType,
    Operator ID, authenticated Location/Vector). Direct (broadcast)
    Remote ID over BLE / Wi-Fi NaN — ASTM F3411 §5 — is a UAV-local
    responsibility (TODO at module level).
    """
    uas_id = uav.serial_number or uav.uav_id
    rid = {
        "ua_type": "Rotorcraft",
        "id_type": "serial_number",
        "uas_id": uas_id,
        "operator_id": uav.imsi or "",
        "timestamp_utc": "1970-01-01T00:00:00Z",  # caller fills with now()
    }
    if pos is not None:
        rid.update({
            "latitude": pos.latitude,
            "longitude": pos.longitude,
            "geodetic_altitude_m": pos.altitude_m,
            "height_agl_m": pos.altitude_m,
            "direction_deg": pos.heading_deg,
            "speed_horizontal_mps": pos.speed_mps,
            "timestamp": pos.timestamp,
        })
    if flight_id:
        rid["flight_id"] = flight_id
        rid["flight_status"] = "authorized"
    else:
        rid["flight_id"] = None
        rid["flight_status"] = "none"
    return rid


# ─── C2 link (TS 23.256 §5.5) ────────────────────────────────────


@dataclass
class C2Session:
    uav_id: str
    controller_id: str
    qos_5qi: int = C2_DEFAULT_5QI
    status: str = "active"


def establish_c2(uav_id: str, controller_id: str,
                 qos_5qi: int = 0) -> C2Session:
    """TS 23.256 §5.2.3 + §5.5 — pair UAV with UAV-C and create C2."""
    if not uav_id:
        raise ValueError("uav_id is required")
    if not controller_id:
        raise ValueError("controller_id is required")
    if qos_5qi <= 0:
        qos_5qi = C2_DEFAULT_5QI
    return C2Session(uav_id=uav_id, controller_id=controller_id,
                     qos_5qi=qos_5qi, status="active")


def terminate_c2(session: C2Session) -> C2Session:
    if session.status == "active":
        session.status = "terminated"
    return session


def failover_c2(session: C2Session) -> dict:
    """TODO(TS 23.256 §5.6): real recovery (alternate DRB / redundant
    DN switchover) — today we just flag the session for manual action."""
    session.status = "failed"
    return {
        "c2_session_id": id(session),
        "uav_id": session.uav_id,
        "controller_id": session.controller_id,
        "status": "failed",
        "action": "manual_failover_required",
    }

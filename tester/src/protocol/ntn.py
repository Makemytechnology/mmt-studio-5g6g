# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""NTN (Non-Terrestrial Network) primitives — tester-side mirror.

Mirrors the Go core's access/ntn package. Pure logic — no I/O, no
live core state — suitable for round-tripping fixtures alongside
the satellite-coverage / TAI / feeder-link behaviour tests.

Spec anchors (PDFs under specs/common/):

  * TS 22.261 §6.3.2.3   Satellite access as one of the multiple
                         access technologies (service requirement).
  * TS 23.501 §5.4.10    Identification + restriction of using NR
                         satellite access (RAT-type gating).
  * TS 23.501 §5.4.11    Integrating NR satellite access into 5GS
                         — the umbrella architecture clause.
  * TS 23.501 §5.4.11.4  Verification of UE location (drives the
                         TAIManager / GeographicTAI mapping).
  * TS 23.501 §5.4.11.7  Tracking Area handling for NR satellite
                         access (geographic TAI rows).
  * TS 23.501 §5.4.11.9  N2 + connection management for the
                         regenerative satellite payload.
  * TS 23.501 §5.4.13    Discontinuous network coverage for
                         satellite access (LEO pass gaps); drives
                         the DL buffering surface.
  * TS 23.501 §5.4.14    UE-Satellite-UE communication.
  * TS 23.501 §5.43      5G Satellite Backhaul.
  * TS 38.300 §16.14     NR support for non-terrestrial networks
                         (NG-RAN-side overall architecture).
  * TS 38.821 §4.1       NTN overview (transparent / regenerative).
  * TS 38.821 §5.1       Transparent satellite-based NG-RAN.
  * TS 38.821 §5.2       Regenerative satellite-based NG-RAN.
  * TS 38.821 §6.2.5     Impact of feeder link switch.
  * TS 38.821 §6.3       UL timing advance / RACH (RTT model).

NAS-timer extension (4× max-RTT) is operator policy informed by
TS 38.821 §6.3 latency analysis but not §-mandated.

Deferred / TODO:

  * TS 38.821 (S&F) — store-and-forward operation; not a normative
                      clause in TR v16.2. Surface here is operator
                      policy pending a Rel-19+ landing.
  * TS 38.821 (ISL) — Inter-Satellite Links discussed across §5.x
                      architecture variants without a single-clause
                      normative anchor in TR v16.2.
  * TS 38.331 §6.3.x — NTN-specific RRC IEs (epoch time, ephemeris
                       parameter set) not yet decoded; the
                       SatelliteConfig holds local orbital
                       parameters only.
  * TS 23.502 §4.x   — Satellite-aware Registration / Service
                       Request signalling not yet wired into the
                       AMF; the TAIManager is consulted in
                       isolation.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from typing import Optional


EARTH_RADIUS_KM = 6371.0
SPEED_OF_LIGHT_KMS = 299_792.458
GRAVITATIONAL_PARAMETER = 398_600.4418  # km³/s²

# Orbit-type enum the satellite registry uses.
VALID_ORBIT_TYPES = frozenset({"LEO", "MEO", "GEO", "HAPS"})


# ─── Satellite + ground-station registry ─────────────────────────


@dataclass
class SatelliteConfig:
    """A single satellite or HAPS in the operator's constellation."""

    sat_id: str
    name: str
    orbit_type: str
    altitude_km: float
    inclination_deg: float = 0.0
    longitude_deg: float = 0.0
    beam_count: int = 0
    beam_diameter_km: float = 0.0
    min_rtt_ms: float = 0.0
    max_rtt_ms: float = 0.0


def new_satellite_config(sat_id: str, name: str, orbit_type: str,
                         altitude_km: float, inclination_deg: float,
                         longitude_deg: float, beam_count: int,
                         beam_diameter_km: float) -> SatelliteConfig:
    """Build a satellite config with min/max RTT pre-computed.

    Min RTT — direct overhead path (2 · h / c).
    Max RTT — slant-range path at 10° elevation + min RTT (the same
    convention the Go core uses).
    """
    if orbit_type not in VALID_ORBIT_TYPES:
        raise ValueError(f"orbit_type must be one of {VALID_ORBIT_TYPES}")
    one_way_ms = (altitude_km / SPEED_OF_LIGHT_KMS) * 1000
    min_rtt = round(2 * one_way_ms * 100) / 100
    el_rad = math.radians(10)
    R = EARTH_RADIUS_KM
    h = altitude_km
    slant = (math.sqrt((R + h) ** 2 - (R * math.cos(el_rad)) ** 2)
             - R * math.sin(el_rad))
    max_one_way = (slant / SPEED_OF_LIGHT_KMS) * 1000
    max_rtt = round((2 * max_one_way + min_rtt) * 100) / 100
    return SatelliteConfig(
        sat_id=sat_id, name=name, orbit_type=orbit_type,
        altitude_km=altitude_km, inclination_deg=inclination_deg,
        longitude_deg=longitude_deg, beam_count=beam_count,
        beam_diameter_km=beam_diameter_km,
        min_rtt_ms=min_rtt, max_rtt_ms=max_rtt,
    )


@dataclass
class GroundStationConfig:
    gs_id: str
    name: str
    latitude: float
    longitude: float
    connected_gnb_ip: str = ""
    active: bool = True


# ─── Ephemeris (TS 38.331 §6.3.x — TODO: decode RRC IEs) ─────────


@dataclass
class SatellitePosition:
    latitude: float
    longitude: float
    altitude_km: float
    timestamp: float


def get_satellite_position(sat: SatelliteConfig, at_time: float = 0.0
                           ) -> SatellitePosition:
    """Compute satellite sub-satellite point at a UNIX timestamp.

    GEO is stationary; HAPS hovers at its declared point; LEO/MEO
    use a simplified circular-orbit projection (matches the Go
    core's local heuristic — not §-mandated).
    """
    import time as _time
    if at_time == 0:
        at_time = float(int(_time.time()))
    if sat.orbit_type == "GEO":
        return SatellitePosition(0.0, sat.longitude_deg,
                                 sat.altitude_km, at_time)
    if sat.orbit_type == "HAPS":
        return SatellitePosition(sat.inclination_deg, sat.longitude_deg,
                                 sat.altitude_km, at_time)
    r = EARTH_RADIUS_KM + sat.altitude_km
    period = 2 * math.pi * math.sqrt(r ** 3 / GRAVITATIONAL_PARAMETER)
    fraction = (at_time % period) / period
    angle = 2 * math.pi * fraction
    incl_rad = math.radians(sat.inclination_deg)
    lat = math.degrees(math.asin(math.sin(incl_rad) * math.sin(angle)))
    earth_rot_deg_s = 360.0 / 86400.0
    lon_offset = fraction * 360.0
    earth_offset = (at_time % 86400) * earth_rot_deg_s
    lon = (sat.longitude_deg + lon_offset - earth_offset) % 360
    if lon > 180:
        lon -= 360
    return SatellitePosition(round(lat, 4), round(lon, 4),
                             sat.altitude_km, at_time)


def compute_visibility(pos: SatellitePosition, ue_lat: float,
                       ue_lon: float, min_elev_deg: float = 10.0
                       ) -> tuple[bool, float, float]:
    """Return (visible, elevation_deg, slant_range_km).

    Spec context: TS 38.821 §6.3 uses the elevation / slant-range
    geometry to bound the UL TA correction. Visibility threshold
    is operator policy (§-side §6.1.3 link-budget analyses use
    10° as a typical lower bound).
    """
    lat1 = math.radians(ue_lat)
    lon1 = math.radians(ue_lon)
    lat2 = math.radians(pos.latitude)
    lon2 = math.radians(pos.longitude)
    delta_sigma = math.acos(
        max(-1.0, min(1.0,
            math.sin(lat1) * math.sin(lat2)
            + math.cos(lat1) * math.cos(lat2) * math.cos(lon2 - lon1))))
    R = EARTH_RADIUS_KM
    h = pos.altitude_km
    rho = R / (R + h)
    elev_rad = math.atan2(math.cos(delta_sigma) - rho, math.sin(delta_sigma))
    elev_deg = math.degrees(elev_rad)
    if math.cos(elev_rad) > 0:
        slant = R * math.sin(delta_sigma) / math.cos(elev_rad)
    else:
        slant = float("inf")
    return elev_deg >= min_elev_deg, round(elev_deg, 2), round(slant, 2)


# ─── Coverage + DL buffering (TS 23.501 §5.4.13) ─────────────────


@dataclass
class _DLEntry:
    timestamp: float
    data: object


class CoverageManager:
    """LEO coverage gaps are intrinsic; CoverageManager buffers DL
    packets while the serving satellite is out of view (TS 23.501
    §5.4.13 discontinuous coverage)."""

    def __init__(self, max_buffer_per_ue: int = 100,
                 buffer_ttl_s: float = 3600):
        self._buffer: dict[str, list[_DLEntry]] = {}
        self._max = max_buffer_per_ue
        self._ttl = buffer_ttl_s

    def buffer_dl_packet(self, imsi: str, data: object) -> None:
        import time as _time
        buf = self._buffer.setdefault(imsi, [])
        if len(buf) >= self._max:
            buf.pop(0)
        buf.append(_DLEntry(timestamp=float(int(_time.time())), data=data))

    def flush_dl_buffer(self, imsi: str) -> list[_DLEntry]:
        import time as _time
        buf = self._buffer.pop(imsi, [])
        now = float(int(_time.time()))
        return [e for e in buf if now - e.timestamp < self._ttl]

    def buffer_status(self, imsi: Optional[str] = None) -> dict:
        if imsi:
            return {"imsi": imsi,
                    "buffered_packets": len(self._buffer.get(imsi, []))}
        total = sum(len(b) for b in self._buffer.values())
        return {"total_ues_buffered": len(self._buffer),
                "total_packets": total}


# ─── Feeder Link Manager (TS 38.821 §6.2.5) ──────────────────────


class FeederLinkManager:
    """Tracks the (sat → gNB) binding and logs each switch event."""

    def __init__(self):
        self._active: dict[str, dict] = {}
        self._history: list[dict] = []

    def register(self, sat_id: str, gs_id: str, gnb_ip: str) -> Optional[dict]:
        """Register or update a feeder link. Returns the previous
        binding when this is a switch (different GS), else None."""
        import time as _time
        old = self._active.get(sat_id)
        self._active[sat_id] = {
            "gs_id": gs_id, "gnb_ip": gnb_ip,
            "since": int(_time.time()),
        }
        if old is not None and old["gs_id"] != gs_id:
            self._history.append({
                "sat_id": sat_id, "from_gs": old["gs_id"],
                "to_gs": gs_id, "timestamp": int(_time.time()),
            })
            return old
        return None

    def gnb_for_satellite(self, sat_id: str) -> str:
        link = self._active.get(sat_id)
        return link["gnb_ip"] if link else ""

    def initiate_switch(self, sat_id: str, new_gs_id: str,
                        new_gnb_ip: str) -> dict:
        old = self.register(sat_id, new_gs_id, new_gnb_ip)
        if old is None:
            return {"switched": False,
                    "reason": "No previous link or same ground station"}
        return {"switched": True, "sat_id": sat_id,
                "from_gs": old["gs_id"], "to_gs": new_gs_id}

    def history(self, limit: int = 20) -> list[dict]:
        if limit <= 0:
            limit = 20
        return self._history[-limit:]


# ─── Geographic TAI Manager (TS 23.501 §5.4.11.7) ────────────────


@dataclass
class GeographicTAI:
    """TS 23.501 §5.4.11.7 — TA derived from UE-reported location
    rather than the (moving) satellite cell identity."""

    tai_id: str
    mcc: str
    mnc: str
    tac: str
    center_lat: float
    center_lon: float
    radius_km: float

    def contains(self, lat: float, lon: float) -> bool:
        return haversine_km(self.center_lat, self.center_lon,
                            lat, lon) <= self.radius_km


class TAIManager:
    def __init__(self):
        self._tais: dict[str, GeographicTAI] = {}

    def add(self, t: GeographicTAI) -> None:
        self._tais[t.tai_id] = t

    def lookup(self, lat: float, lon: float) -> Optional[GeographicTAI]:
        for t in self._tais.values():
            if t.contains(lat, lon):
                return t
        return None

    def has_tai_changed(self, old_tac: str, new_lat: float,
                        new_lon: float) -> bool:
        t = self.lookup(new_lat, new_lon)
        if t is None:
            return True
        return t.tac != old_tac


# ─── Timing (TS 38.821 §6.3) ─────────────────────────────────────


def compute_propagation_delay(sat: SatelliteConfig,
                              ue_lat: Optional[float] = None,
                              ue_lon: Optional[float] = None) -> dict:
    """One-way and RTT propagation delays for the UE→sat→ground
    path (TS 38.821 §6.3 timing model). When the UE is not given,
    the satellite altitude is used as a worst-case proxy."""
    pos = get_satellite_position(sat, 0)
    feeder_ms = (sat.altitude_km / SPEED_OF_LIGHT_KMS) * 1000
    if ue_lat is not None and ue_lon is not None:
        vis, elev, slant = compute_visibility(pos, ue_lat, ue_lon, 0)
        if not vis or elev < 0:
            return {"service_link_ms": None,
                    "feeder_link_ms": round(feeder_ms, 3),
                    "total_one_way_ms": None, "rtt_ms": None,
                    "visible": False, "elevation_deg": elev}
        service_ms = (slant / SPEED_OF_LIGHT_KMS) * 1000
        total = service_ms + feeder_ms
        return {"service_link_ms": round(service_ms, 3),
                "feeder_link_ms": round(feeder_ms, 3),
                "total_one_way_ms": round(total, 3),
                "rtt_ms": round(2 * total, 3), "visible": True}
    total = 2 * feeder_ms
    return {"service_link_ms": round(feeder_ms, 3),
            "feeder_link_ms": round(feeder_ms, 3),
            "total_one_way_ms": round(total / 2, 3),
            "rtt_ms": round(total, 3), "visible": True}


def adjusted_nas_timers(sat: SatelliteConfig) -> dict:
    """NAS T35xx extended by 4× max-RTT guard band (operator policy
    informed by TS 38.821 §6.3). T3512 is doubled for GEO because
    of the much longer service-link path."""
    max_rtt_s = sat.max_rtt_ms / 1000
    guard = 4 * max_rtt_s
    base = {"T3510": 15, "T3511": 10, "T3512": 3240,
            "T3517": 15, "T3521": 15}
    out: dict = {}
    for timer, b in base.items():
        if timer == "T3512":
            out[timer] = b * 2 if sat.orbit_type == "GEO" else b
        else:
            out[timer] = round((b + guard) * 10) / 10
    return out


# ─── Helpers ─────────────────────────────────────────────────────


def haversine_km(lat1: float, lon1: float,
                 lat2: float, lon2: float) -> float:
    dlat = math.radians(lat2 - lat1)
    dlon = math.radians(lon2 - lon1)
    a = (math.sin(dlat / 2) ** 2
         + math.cos(math.radians(lat1)) * math.cos(math.radians(lat2))
         * math.sin(dlon / 2) ** 2)
    return EARTH_RADIUS_KM * 2 * math.atan2(math.sqrt(a), math.sqrt(1 - a))

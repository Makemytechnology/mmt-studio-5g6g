# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""5G positioning primitives — tester-side mirror.

Mirrors the Go core's nf/lmf and nf/gmlc packages. Pure functions —
no I/O, no live core state — suitable for round-trip fixtures
alongside the LMF / GMLC behaviour tests.

Spec anchors (PDFs under specs/common/):

  * TS 23.273 §4.3.3     GMLC functional description.
  * TS 23.273 §4.3.8     LMF functional description.
  * TS 23.273 §4.4.1     Le reference point (LCS-Client interface).
  * TS 23.273 §6.2       5GC-MT-LR procedure (typical Mobile-
                         Terminating Location Request).
  * TS 29.572 §5.2.2.2   Nlmf_Location_DetermineLocation — the
                         downstream SBI operation.
  * TS 29.572 §5.2.2.4   Nlmf_Location_CancelLocation.
  * TS 38.305 §8.1       GNSS positioning methods (A-GNSS).
  * TS 38.305 §8.9       NR Enhanced cell ID (NR E-CID).
  * TS 38.305 §8.10      Multi-RTT positioning.
  * TS 38.305 §8.11      DL-AoD positioning.
  * TS 38.305 §8.12      DL-TDOA positioning.
  * TS 38.305 §8.13      UL-TDOA positioning.
  * TS 38.305 §8.14      UL-AoA positioning.
  * TS 38.211 §7.4.1.7   Positioning Reference Signal (PRS) —
                         combSize ∈ {2,4,6,12}, numSymbols·combSize
                         ≤ 12, periodicity table, numRB ∈ [24,272].

Hybrid methods (GNSS+E-CID, RTT+AoA) and the QoS-driven method
selection function are operator policy, not §-mandated.

Deferred / TODO:

  * TS 29.572 §6   — wire the HTTP/2 + JSON Nlmf_Location SBI.
  * TS 29.515     — Ngmlc service operations (proper GMLC SBI).
  * TS 38.455 §8.2 — NRPPa Location Information Transfer Procedures
                     (E-CID Measurement Initiation / Report,
                     Multi-RTT, TDOA, AoA wire codec).
  * TS 37.355 §6   — LPP message envelope (NR + LTE assistance /
                     measurement messages).
  * TS 23.273 §6.2 — full 5GC-MT-LR signalling chain (LCS Service
                     Request → AMF → LMF → NRPPa / LPP).
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from typing import Optional


# ─── Method names (TS 38.305 §4.3 / §8.x) ────────────────────────


METHOD_ECID = "ecid"             # TS 38.305 §8.9 NR E-CID
METHOD_MULTI_RTT = "multi_rtt"   # TS 38.305 §8.10 Multi-RTT
METHOD_DL_TDOA = "dl_tdoa"       # TS 38.305 §8.12 DL-TDOA
METHOD_UL_TDOA = "ul_tdoa"       # TS 38.305 §8.13 UL-TDOA
METHOD_DL_AOD = "dl_aod"         # TS 38.305 §8.11 DL-AoD
METHOD_UL_AOA = "ul_aoa"         # TS 38.305 §8.14 UL-AoA
METHOD_AGNSS = "agnss"           # TS 38.305 §8.1 GNSS positioning
METHOD_HYBRID_GNSS_ECID = "hybrid_gnss_ecid"
METHOD_HYBRID_RTT_AOA = "hybrid_rtt_aoa"

VALID_METHODS = frozenset({
    METHOD_ECID, METHOD_MULTI_RTT, METHOD_DL_TDOA, METHOD_UL_TDOA,
    METHOD_DL_AOD, METHOD_UL_AOA, METHOD_AGNSS,
    METHOD_HYBRID_GNSS_ECID, METHOD_HYBRID_RTT_AOA,
})


# Session lifecycle states — matches lmf.Session.State.
VALID_STATES = frozenset({
    "PENDING", "ACTIVE", "COMPLETED", "FAILED", "CANCELLED",
})


# ─── Geometry helpers ────────────────────────────────────────────


EARTH_R_M = 6371000.0
M_PER_DEG_LAT = 111320.0


def m_per_deg_lon(lat_deg: float) -> float:
    return M_PER_DEG_LAT * math.cos(math.radians(lat_deg))


def haversine_distance_m(lat1: float, lon1: float,
                         lat2: float, lon2: float) -> float:
    """Great-circle distance in metres between two lat/lon pairs."""
    dlat = math.radians(lat2 - lat1)
    dlon = math.radians(lon2 - lon1)
    a = (math.sin(dlat / 2) ** 2
         + math.cos(math.radians(lat1)) * math.cos(math.radians(lat2))
         * math.sin(dlon / 2) ** 2)
    return EARTH_R_M * 2 * math.atan2(math.sqrt(a), math.sqrt(1 - a))


def offset_position(lat: float, lon: float, bearing_deg: float,
                    distance_m: float) -> tuple[float, float]:
    """Project (lat, lon) along bearing for distance metres."""
    d = distance_m / EARTH_R_M
    brng = math.radians(bearing_deg)
    lat1 = math.radians(lat)
    lon1 = math.radians(lon)
    lat2 = math.asin(math.sin(lat1) * math.cos(d)
                     + math.cos(lat1) * math.sin(d) * math.cos(brng))
    lon2 = lon1 + math.atan2(
        math.sin(brng) * math.sin(d) * math.cos(lat1),
        math.cos(d) - math.sin(lat1) * math.sin(lat2),
    )
    return math.degrees(lat2), math.degrees(lon2)


def trilaterate(circles: list[tuple[float, float, float]]) -> tuple[float, float]:
    """Linear-least-squares circle trilateration. Each tuple is
    (lat, lon, radius_m). Returns the estimated UE (lat, lon).

    Uses circle 0 as reference and subtracts in a local Cartesian
    frame:
       2·xi·x + 2·yi·y = r0² − ri² + xi² + yi²
    """
    if len(circles) < 3:
        return circles[0][0], circles[0][1]

    ref_lat, ref_lon, r0 = circles[0]
    mlat = M_PER_DEG_LAT
    mlon = m_per_deg_lon(ref_lat)

    a_rows: list[tuple[float, float]] = []
    b_vals: list[float] = []
    for lat_i, lon_i, ri in circles[1:]:
        xi = (lon_i - ref_lon) * mlon
        yi = (lat_i - ref_lat) * mlat
        a_rows.append((2 * xi, 2 * yi))
        b_vals.append(r0 * r0 - ri * ri + xi * xi + yi * yi)

    if len(a_rows) < 2:
        return ref_lat, ref_lon

    a11, a12 = a_rows[0]
    a21, a22 = a_rows[1]
    b1, b2 = b_vals[0], b_vals[1]
    det = a11 * a22 - a12 * a21
    if abs(det) < 1e-10:
        return ref_lat, ref_lon
    x = (b1 * a22 - b2 * a12) / det
    y = (a11 * b2 - a21 * b1) / det
    return ref_lat + y / mlat, ref_lon + x / mlon


# ─── Method selection (operator policy) ──────────────────────────


def select_method(qos: Optional[dict],
                  has_antenna_info: bool = False) -> str:
    """Pick a positioning method from a QoS dict.

    Matches lmf.selectMethod in the Go core; not §-mandated. The
    QoS dict may contain accuracy_m and response_time_s keys.
    """
    if not qos:
        return METHOD_ECID
    accuracy = qos.get("accuracy_m") or 100.0
    rt = qos.get("response_time_s", 0.0)
    if accuracy <= 3:
        return METHOD_MULTI_RTT if rt >= 5 else METHOD_HYBRID_RTT_AOA
    if accuracy <= 5:
        return METHOD_HYBRID_RTT_AOA
    if accuracy <= 10:
        return METHOD_DL_AOD if has_antenna_info else METHOD_DL_TDOA
    if accuracy <= 15:
        return METHOD_HYBRID_GNSS_ECID
    if accuracy <= 30:
        return METHOD_UL_AOA
    return METHOD_ECID


# ─── PRS resource (TS 38.211 §7.4.1.7) ───────────────────────────


# §7.4.1.7.4 Table 7.4.1.7.4-1 — valid PRS periodicities (slots).
VALID_PRS_PERIODICITIES = frozenset({
    4, 5, 8, 10, 16, 20, 32, 40, 64, 80, 160, 320, 640,
    1280, 2560, 5120, 10240,
})

# §7.4.1.7.3 — comb sizes K_PRS.
VALID_PRS_COMB_SIZES = frozenset({2, 4, 6, 12})

# §7.4.1.7.3 — symbol counts L_PRS (consistent with Go core clamp).
VALID_PRS_NUM_SYMBOLS = frozenset({2, 4, 6, 12})

PRS_NUM_RB_MIN = 24
PRS_NUM_RB_MAX = 272


@dataclass
class PRSResource:
    """TS 38.211 §7.4.1.7 PRS resource configuration."""

    gnb_id: str
    frequency_layer: int
    periodicity_ms: int
    num_rb: int
    num_symbols: int
    comb_size: int


def allocate_prs(gnb_id: str, frequency_layer: int,
                 periodicity_ms: int, num_rb: int,
                 num_symbols: int, comb_size: int) -> PRSResource:
    """Allocate a PRS resource. Out-of-range parameters are clamped
    to safe defaults — same operator-friendly contract as the Go
    core (TS 38.211 §7.4.1.7 invariants enforced by clamping)."""
    if periodicity_ms not in VALID_PRS_PERIODICITIES:
        periodicity_ms = 20
    if comb_size not in VALID_PRS_COMB_SIZES:
        comb_size = 2
    if num_symbols not in VALID_PRS_NUM_SYMBOLS:
        num_symbols = 2
    # K_PRS · L_PRS ≤ 12 (§7.4.1.7.3 RE-mapping invariant).
    if num_symbols * comb_size > 12:
        num_symbols = 12 // comb_size
    num_rb = max(PRS_NUM_RB_MIN, min(PRS_NUM_RB_MAX, num_rb))
    return PRSResource(
        gnb_id=gnb_id, frequency_layer=frequency_layer,
        periodicity_ms=periodicity_ms, num_rb=num_rb,
        num_symbols=num_symbols, comb_size=comb_size,
    )


# ─── E-CID (TS 38.305 §8.9) ──────────────────────────────────────


def ta_to_distance_m(ta_units: float) -> float:
    """Convert NR Timing Advance units to one-way distance (metres).

    TS 38.213 §4.2 — TA step is roughly 8.14e-9 s (16/(64·30·15kHz)
    family); the Go core uses the same 8.14e-9 constant. Half the
    round-trip path = TA · c / 2.
    """
    return ta_units * 8.14e-9 * 3e8 / 2


def beam_bearing_deg(boresight_deg: float, beamwidth_deg: float,
                     num_beams: int, beam_index: int) -> float:
    """Compute the centre bearing of a single beam in a uniform
    azimuthal sweep (operator-side beamforming model)."""
    if num_beams <= 1:
        return boresight_deg
    spacing = beamwidth_deg / num_beams
    offset = (beam_index - (num_beams - 1) / 2.0) * spacing
    return (boresight_deg + offset + 360) % 360


@dataclass
class ECIDFix:
    lat: float
    lon: float
    uncertainty_m: float
    confidence: int


def ecid_fix(gnb_lat: float, gnb_lon: float,
             ta_units: Optional[float] = None,
             beam_bearing: Optional[float] = None) -> ECIDFix:
    """Return an NR E-CID fix (TS 38.305 §8.9). With TA + beam
    bearing, project; with TA alone, fall back to a coarse circle
    centred on the gNB; with neither, return the gNB position with
    a wide uncertainty."""
    dist = ta_to_distance_m(ta_units or 0.0)
    if beam_bearing is not None and dist > 0:
        lat, lon = offset_position(gnb_lat, gnb_lon, beam_bearing, dist)
        return ECIDFix(lat=lat, lon=lon,
                       uncertainty_m=max(dist * 0.3, 30), confidence=68)
    return ECIDFix(lat=gnb_lat, lon=gnb_lon,
                   uncertainty_m=max(dist, 50.0) if dist > 0 else 500.0,
                   confidence=68 if dist > 0 else 50)


# ─── Multi-RTT (TS 38.305 §8.10) ─────────────────────────────────


def rtt_ns_to_distance_m(rtt_ns: float) -> float:
    """Round-trip nanoseconds to one-way metres."""
    return rtt_ns * 1e-9 * 3e8 / 2


def multi_rtt_fix(measurements: list[dict]) -> Optional[tuple[float, float]]:
    """TS 38.305 §8.10 — Multi-RTT fix. Each measurement is
    {gnb_lat, gnb_lon, rtt_ns}. Returns None if fewer than 3
    measurements (caller should fall back)."""
    if len(measurements) < 3:
        return None
    circles = [(m["gnb_lat"], m["gnb_lon"], rtt_ns_to_distance_m(m["rtt_ns"]))
               for m in measurements]
    return trilaterate(circles)


# ─── A-GNSS (TS 38.305 §8.1) ─────────────────────────────────────


def agnss_fix(gnss_data: dict) -> tuple[float, float, float, int]:
    """TS 38.305 §8.1 — UE-supplied GNSS fix is taken as-is. Returns
    (lat, lon, uncertainty_m, confidence)."""
    lat = float(gnss_data["lat"])
    lon = float(gnss_data["lon"])
    acc = float(gnss_data.get("accuracy_m") or 10.0)
    return lat, lon, acc, 95


# ─── Hybrid GNSS + E-CID (operator policy fusion) ────────────────


def fuse_gnss_ecid(gnss_lat: float, gnss_lon: float, gnss_unc: float,
                   ecid_lat: float, ecid_lon: float, ecid_unc: float
                   ) -> tuple[float, float, float]:
    """Inverse-variance fusion of A-GNSS + NR E-CID. Operator policy,
    not §-mandated. Returns (lat, lon, fused_uncertainty_m)."""
    w_gnss = 1.0 / (gnss_unc * gnss_unc)
    w_ecid = 1.0 / (ecid_unc * ecid_unc)
    total_w = w_gnss + w_ecid
    lat = (gnss_lat * w_gnss + ecid_lat * w_ecid) / total_w
    lon = (gnss_lon * w_gnss + ecid_lon * w_ecid) / total_w
    unc = 1.0 / math.sqrt(total_w)
    return lat, lon, unc


# ─── Session shape (TS 29.572 §5.2.2.2 / §5.2.2.4) ───────────────


@dataclass
class LocationSession:
    """LMF positioning session row (TS 29.572 §5.2.2.2 / §5.2.2.4)."""

    session_id: str
    imsi: str
    method: str
    state: str = "PENDING"
    latitude: Optional[float] = None
    longitude: Optional[float] = None
    altitude: Optional[float] = None
    uncertainty_m: Optional[float] = None
    confidence: Optional[int] = None
    qos: dict = field(default_factory=dict)


def new_session(session_id: str, imsi: str, method: str,
                qos: Optional[dict] = None) -> LocationSession:
    if not session_id:
        raise ValueError("session_id is required")
    if not imsi:
        raise ValueError("imsi is required")
    if method not in VALID_METHODS:
        raise ValueError(f"unknown method: {method!r}")
    return LocationSession(session_id=session_id, imsi=imsi,
                           method=method, qos=qos or {})


def cancel_session(s: LocationSession) -> LocationSession:
    """TS 29.572 §5.2.2.4 — Nlmf_Location_CancelLocation."""
    s.state = "CANCELLED"
    return s


def complete_session(s: LocationSession, lat: float, lon: float,
                     uncertainty_m: float, confidence: int,
                     altitude: Optional[float] = None) -> LocationSession:
    s.latitude = lat
    s.longitude = lon
    s.altitude = altitude
    s.uncertainty_m = uncertainty_m
    s.confidence = confidence
    s.state = "COMPLETED"
    return s


# ─── GMLC LCS-Client surface (TS 23.273 §4.3.3 / §4.4.1) ─────────


# TS 23.273 §4.3.2 LCS Client classes.
LCS_CLIENT_TYPES = frozenset({
    "commercial", "emergency", "lawful_intercept", "value_added",
})


def normalize_client_type(client_type: Optional[str]) -> str:
    """Default to 'commercial' when caller passes empty/None.
    Matches gmlc.RequestLocation in the Go core."""
    return client_type or "commercial"

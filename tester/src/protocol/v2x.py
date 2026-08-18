# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""V2X primitives — tester-side mirror.

Mirrors the Go core's services/v2x package as a pure-codec dataclass
module — no DB, no network — suitable for round-trip fixtures.

Spec anchors (§-checked by speccheck against PDFs in specs/common/):

  * TS 22.186 §5            5G V2X service requirements (high-level
                            use cases — platooning, advanced driving,
                            extended sensors, remote driving).
  * TS 23.287 §4.2          Reference architecture for V2X over 5GS.
  * TS 23.287 §5.1.2        V2X policy / parameter provisioning
                            (PCF → UE).
  * TS 23.287 §5.2          V2X authorization (subscription → UE
                            authorized for V2X over PC5/Uu).
  * TS 23.287 §5.4          PC5 QoS framework (PQI, ARP, PFI, PDB,
                            PER, max data burst, averaging window).
  * TS 23.287 §5.4.4        Standardized PQI values (Table 5.4.4-1).
  * TS 23.287 §5.5          V2X subscription data (PC5 AMBR, UE type).
  * TS 24.587 §5            5G NAS procedures for V2X over PC5
                            (V2X policy delivery via UE Policy
                            Container).
  * TS 24.588 §5            PC5 signalling protocol procedures.

Deferred (TODO):

  * TS 23.287 §5.3          V2X service authorisation in roaming.
  * TS 23.287 §5.6          UE-to-Network relay for V2X over Uu.
  * TS 24.588 §6            PC5 unicast link security establishment.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional


# Standardized PQI → 5G QoS mapping seed from TS 23.287 §5.4.4
# Table 5.4.4-1. Mirrors the rows the Go core seeds into
# v2x_service_types so a Python test can cross-check Go's
# GetServiceType(PQI) result without a live DB.
PQI_TABLE: dict[int, dict] = {
    21: {"name": "platooning_higher",       "resource_type": "GBR",        "priority": 3, "pdb_ms": 20,  "per": "1e-4", "fiveqi_uu": 3},
    22: {"name": "sensor_sharing_higher",   "resource_type": "GBR",        "priority": 4, "pdb_ms": 50,  "per": "1e-2", "fiveqi_uu": 3},
    23: {"name": "info_sharing_driving",    "resource_type": "GBR",        "priority": 3, "pdb_ms": 100, "per": "1e-4", "fiveqi_uu": 3},
    55: {"name": "coop_lane_change_higher", "resource_type": "NonGBR",     "priority": 3, "pdb_ms": 10,  "per": "1e-4", "fiveqi_uu": 3},
    56: {"name": "platooning_informative",  "resource_type": "NonGBR",     "priority": 6, "pdb_ms": 20,  "per": "1e-1", "fiveqi_uu": 3},
    57: {"name": "coop_lane_change_lower",  "resource_type": "NonGBR",     "priority": 5, "pdb_ms": 25,  "per": "1e-1", "fiveqi_uu": 3},
    58: {"name": "sensor_sharing_lower",    "resource_type": "NonGBR",     "priority": 4, "pdb_ms": 100, "per": "1e-2", "fiveqi_uu": 3},
    59: {"name": "platooning_reporting",    "resource_type": "NonGBR",     "priority": 6, "pdb_ms": 500, "per": "1e-1", "fiveqi_uu": 3},
    90: {"name": "collision_avoidance",     "resource_type": "DelCritGBR", "priority": 3, "pdb_ms": 10,  "per": "1e-4", "fiveqi_uu": 3},
    91: {"name": "emergency_trajectory",    "resource_type": "DelCritGBR", "priority": 2, "pdb_ms": 3,   "per": "1e-5", "fiveqi_uu": 3},
}


# ─── V2X subscription (TS 23.287 §5.5) ───────────────────────────


@dataclass
class V2XSubscription:
    """V2X subscription container per TS 23.287 §5.5.

    The PCF reads this from the UDM and feeds it into the policy
    provisioning procedure of §5.1.2.
    """

    v2x_authorized: bool = False
    v2x_ue_type: str = "vehicle"     # 'vehicle' | 'pedestrian' (TS 23.287 §5.2)
    v2x_pc5_ambr_kbps: int = 0


def is_authorized(sub: Optional[V2XSubscription]) -> bool:
    """TS 23.287 §5.2 — V2X authorization predicate."""
    return sub is not None and bool(sub.v2x_authorized)


# ─── PQI lookup (TS 23.287 §5.4.4) ───────────────────────────────


def get_pqi(pqi: int) -> Optional[dict]:
    """Return the QoS row for the given PQI value, or None.

    Mirrors safety/v2x.GetServiceType — TS 23.287 Table 5.4.4-1.
    """
    return PQI_TABLE.get(pqi)


def list_pqis() -> list[int]:
    """Return all standardized PQI values, sorted (TS 23.287 §5.4.4)."""
    return sorted(PQI_TABLE.keys())


# ─── V2X policy provisioning (TS 23.287 §5.1.2 → TS 24.587 §5) ───


def build_v2x_policy(sub: Optional[V2XSubscription], *,
                     authorized_plmns: Optional[list[str]] = None,
                     v2x_frequencies: Optional[list[int]] = None,
                     pc5_rats: Optional[list[str]] = None) -> Optional[dict]:
    """Build the V2X policy block delivered to a UE.

    TS 23.287 §5.1.2 specifies the V2X policy / parameter provisioning
    procedure. The PCF builds this body and the AMF wraps it in a
    UE Policy Container per TS 24.587 §5 (which references the
    TS 24.501 §D.6.1 container format).

    Returns ``None`` if the UE is not authorized — gating per
    TS 23.287 §5.2.
    """
    if not is_authorized(sub):
        return None
    if pc5_rats is None:
        pc5_rats = ["nr"]
    return {
        "auth_policy": {
            "authorized_plmns": authorized_plmns or [],
            "ue_type": sub.v2x_ue_type,
            "pc5_rats": pc5_rats,
            "pc5_ambr_kbps": sub.v2x_pc5_ambr_kbps,
        },
        "pc5_qos_params": [
            {"pqi": pqi, **row} for pqi, row in sorted(PQI_TABLE.items())
        ],
        "v2x_frequencies": v2x_frequencies or [],
    }

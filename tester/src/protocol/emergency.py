# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Emergency-services primitives — tester-side mirror.

Mirrors the Go core's safety/emergency package. Pure functions +
dataclasses; no live core, no DB, no network — suitable for round-trip
fixtures alongside live behaviour tests.

Spec anchors (§-checked by speccheck against PDFs in specs/common/):

  * TS 22.101 §10           Emergency Calls (umbrella service requirements).
  * TS 22.101 §10.4         Emergency calls in IM CN subsystem (IMS-CN).
  * TS 22.101 §10.6         Location Availability for Emergency Calls.
  * TS 23.501 §5.16.4       Emergency Services architecture (5G core).
  * TS 23.501 §5.16.4.6     QoS for Emergency Services (5QI / ARP).
  * TS 23.501 §5.16.4.8     IP Address Allocation for emergency PDUs.
  * TS 23.501 §5.16.4.9     Handling of PDU Sessions for Emergency
                            Services — request_type "Emergency Request"=3.
  * TS 23.167 §6.2.2        Emergency-CSCF (E-CSCF) functional entity.
  * TS 23.167 §7.1          High Level Procedures for IMS Emergency
                            Services.
  * TS 23.167 §7.5          Interworking with PSAP.
  * TS 24.501 §5.5.1.2.6    Initial Registration for Emergency services.
  * TS 24.501 §5.5.1.2.6A   Initial Registration for emergency services
                            when authentication is not performed.
  * RFC 5031 §4.2           Sub-Services for the 'sos' Service.

Deferred (TODO at unimplemented surfaces):

  * TS 23.167 §6.2.3        LRF / RDF location retrieval at session setup.
  * TS 23.167 §6.2.6        EATF — Emergency Access Transfer Function
                            (SRVCC of an active emergency call to CS).
  * TS 23.167 §7.4          IMS Emergency Session Establishment without
                            Registration (today we assume registered UEs).
  * TS 22.101 §10.1.3       Call-Back Requirements (PSAP → UE callback).
  * TS 23.501 §5.16.4.10    Support of eCall Only Mode.
  * TS 23.501 §5.16.4.11    Emergency Services Fallback (EPS fallback).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Optional


# RFC 5031 §4.2 registered sub-services for the 'sos' service.
SOS_SUB_SERVICES = frozenset({
    "ambulance", "animal-control", "fire", "gas", "marine",
    "mountain", "physician", "poison", "police",
})

# emergency_sessions.status vocabulary (mirrors the DB CHECK constraint
# on safety side: 'active','released','failed').
SESSION_STATUSES = frozenset({"active", "released", "failed"})


# ─── Config ──────────────────────────────────────────────────────


@dataclass
class EmergencyConfig:
    """Mirrors the singleton emergency_config row.

    Defaults match the Go-side schema (db/schemas/domains.go) so that a
    test fixture doesn't drift from the real DB initial state.
    """

    enabled: bool = True
    auth_required: bool = False  # TS 24.501 §5.5.1.2.6A path is default
    emergency_dnn: str = "sos"
    ip_pool_v4: str = "10.99.0.0/24"
    ip_pool_v6: str = ""
    psap_sip_uri: str = ""
    psap_ip: str = ""
    psap_port: int = 5060
    emergency_qfi: int = 5
    voice_qfi: int = 1
    arp_priority: int = 1   # 1 = highest (TS 23.501 §5.16.4.6)
    max_sessions: int = 100


def is_emergency_enabled(cfg: Optional[EmergencyConfig]) -> bool:
    """TS 22.101 §10.1 — emergency support is mandatory unless the
    operator explicitly disables (regulatory-driven; defaults true)."""
    if cfg is None:
        return True
    return bool(cfg.enabled)


def is_auth_required(cfg: Optional[EmergencyConfig]) -> bool:
    """TS 24.501 §5.5.1.2.6A — defines the unauthenticated emergency
    registration path; some regulators require auth nonetheless."""
    if cfg is None:
        return False
    return bool(cfg.auth_required)


# ─── Emergency PDU Session (TS 23.501 §5.16.4) ───────────────────


def is_emergency_pdu_request(request_type: int, dnn: str) -> bool:
    """TS 23.501 §5.16.4.9 — two equally-valid signals:
      (a) Request type = "Emergency Request" (value 3), or
      (b) DNN matches the operator-configured emergency DNN
          ("sos" by default per TS 23.003 DNN guidance).
    """
    return request_type == 3 or (dnn or "").lower() == "sos"


def get_emergency_qos(cfg: Optional[EmergencyConfig] = None) -> dict:
    """TS 23.501 §5.16.4.6 — emergency services use a dedicated QoS
    profile with high ARP priority and pre-emption capability.
    Concrete 5QI value comes from operator config (defaults 5/1)."""
    qfi = 5
    arp = 1
    if cfg is not None:
        qfi = cfg.emergency_qfi or 5
        arp = cfg.arp_priority or 1
    return {"qfi": qfi, "fiveqi": qfi, "arp_priority": arp,
            "resource_type": "NonGBR"}


# ─── E-CSCF / PSAP routing (TS 23.167 §6.2.2 / §7.5) ─────────────


def check_emergency_urn(request_uri: str) -> bool:
    """RFC 5031 §4.2 — Sub-Services for the 'sos' Service.

    Accepts bare 'urn:service:sos' and any 'urn:service:sos.<sub>'.
    Case-insensitive; trims surrounding whitespace.
    """
    if not request_uri:
        return False
    return request_uri.strip().lower().startswith("urn:service:sos")


def parse_emergency_urn(request_uri: str) -> Optional[dict]:
    """Decompose a 'urn:service:sos[.<sub-service>]' URI.

    Returns ``{"service": "sos", "sub_service": <name or None>}`` or
    ``None`` if the URI is not an emergency URN. Sub-service names are
    NOT validated against RFC 5031 §4.2's IANA registry — caller can
    cross-check against ``SOS_SUB_SERVICES`` if strict membership is
    required.
    """
    if not check_emergency_urn(request_uri):
        return None
    body = request_uri.strip().lower()[len("urn:service:"):]
    parts = body.split(".", 1)
    sub = parts[1] if len(parts) == 2 else None
    return {"service": parts[0], "sub_service": sub}


# TODO(TS 23.167 §6.2.2): full E-CSCF behaviour — selection of PSAP
# based on UE location (LRF/RDF lookup, TS 23.167 §6.2.3), P-Asserted
# Identity rewrite, anonymous-caller handling for unregistered UEs.
#
# TODO(TS 23.167 §7.5.1 / §7.5.2): GSTN PSAP via MGCF and IMS PSAP via
# IBCF — today we assume an IP PSAP reachable over UDP/SIP.


def route_emergency_call(cfg: Optional[EmergencyConfig],
                         _imsi: str,
                         _sip_invite: bytes) -> bool:
    """Stub mirror of safety/emergency.RouteEmergencyCall.

    Pure-codec mirror returns ``True`` iff a PSAP IP is configured
    (the Go side actually opens a UDP socket and forwards the INVITE
    — see safety/emergency/emergency_test.go for the live path test).
    """
    if cfg is None or not cfg.psap_ip:
        return False
    return True


# ─── Session model (TS 23.501 §5.16.4.9) ─────────────────────────


@dataclass
class EmergencySession:
    """Mirrors a row of the emergency_sessions table."""

    imsi: str
    imei: str
    pdu_session_id: int
    ip_addr: str
    gnb_ip: str
    tac: str
    cell_id: str
    status: str = "active"
    called_number: Optional[str] = None


def new_emergency_session(imsi: str, imei: str, pdu_session_id: int,
                          ip_addr: str, gnb_ip: str, tac: str,
                          cell_id: str,
                          called_number: Optional[str] = None) -> EmergencySession:
    if not imsi:
        raise ValueError("imsi is required")
    if pdu_session_id < 1 or pdu_session_id > 15:
        # TS 24.501 PDU Session ID is 4 bits; valid range 1..15
        # (0 is reserved, see TS 24.501 §9.4).
        raise ValueError("pdu_session_id must be 1..15")
    return EmergencySession(
        imsi=imsi, imei=imei, pdu_session_id=pdu_session_id,
        ip_addr=ip_addr, gnb_ip=gnb_ip, tac=tac, cell_id=cell_id,
        called_number=called_number,
    )


def release_session(session: EmergencySession) -> EmergencySession:
    if session.status not in SESSION_STATUSES:
        raise ValueError(f"unknown session status: {session.status!r}")
    session.status = "released"
    return session


def session_stats(sessions: list[EmergencySession],
                  cfg: Optional[EmergencyConfig] = None) -> dict:
    active = sum(1 for s in sessions if s.status == "active")
    return {
        "enabled": is_emergency_enabled(cfg),
        "auth_required": is_auth_required(cfg),
        "active_sessions": active,
        "total_sessions": len(sessions),
        "psap_configured": bool(cfg and (cfg.psap_ip or cfg.psap_sip_uri)),
    }

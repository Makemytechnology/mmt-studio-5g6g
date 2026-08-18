# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Edge Computing primitives — tester-side mirror.

Mirrors the Go core's edge/{eas,mec} packages. Pure logic — no I/O,
no live core state — suitable for round-tripping fixtures alongside
the EAS Discovery and MEC orchestrator codecs.

Spec anchors (PDFs under specs/common/):

  * TS 23.501 §5.6.5     Local Area Data Network — "A LADN service
                         area is a set of Tracking Areas. LADN is a
                         service provided by the serving PLMN…".
                         An EdgeSite owns the TAI list that defines
                         this service area for a PSA-UPF anchor.
  * TS 23.502 §4.3.6     Application Function influence on traffic
                         routing, service-function chaining and
                         handling of payload. TrafficRule rows
                         carry the AF-request fields the SMF
                         applies to the local PSA-UPF / ULCL.
  * TS 23.548 §6.2       EAS Discovery and Re-discovery — the
                         function the Discover() method implements.
  * TS 23.548 §6.2.2.2   EAS Discovery Procedure (Distributed
                         Anchor model) — the variant Discover()
                         covers; SMF picks a DNS server for the PDU
                         Session and the EAS is selected close to
                         the UE topologically.
  * TS 23.548 §5.1       EASDF — the function FindAppByFQDN models
                         (DNS lookup of an EAS FQDN to A/AAAA).
  * TS 23.548 §6.2.3.2.2 EAS Discovery Procedure with EASDF — the
                         Session-Breakout connectivity-model
                         variant. NOT yet covered by Discover();
                         see TODO at the call site.
  * TS 23.548 §6.8       Mapping between EAS address Information
                         and DNAI — DnaiMapping rows.

Discovery scoring weights (DNAI +50, DNN +30, S-NSSAI +20, capacity
+0..20, proximity +0..30) are operator policy. TS 23.548 §6.2 only
mandates "EAS as close as possible to the UE"; the ranking function
is left to the AF / EES implementation. Don't read these constants
as spec-derived.

EAS lifecycle (register/update/deregister/deploy) is anchored to
the TS 23.558 EDGEAPP architecture:
  * TS 23.558 §8.4.3.2.2     EAS registration procedure
  * TS 23.558 §8.4.3.2.3     EAS registration update procedure
  * TS 23.558 §8.4.3.2.4     EAS de-registration procedure
  * TS 23.558 §8.4.3.4.{2,3,4}  Eees_EASRegistration_*
                                Request / Update / Deregister APIs
  * TS 23.558 §8.5            EAS discovery procedure
  * TS 23.558 §8.12           Dynamic EAS instantiation triggering
                              (the "deploy" surface above)

Beyond Edge:

  * TS 22.137 §4.1 / §5.2     ISAC sensing-service primitives
                              (operator-defined session enums; spec
                              describes capability, not API).
  * TS 23.586 §5.3 / §6.8     Ranging / SL Positioning control —
                              session-level start/stop/measure.
  * TS 23.586 §5.1 / §6.2     Authorisation gate (privacy policy).
  * TS 23.501 §5.27.0         5GS-as-TSN-bridge model (DS-TT / NW-TT
                              translator ports).
  * TS 23.501 §5.27.2         TSCAI traffic-pattern hints
                              (interval / max frame / class).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from math import asin, cos, radians, sin, sqrt
from typing import Optional


# ─── EAS-side ────────────────────────────────────────────────────


@dataclass
class DiscoveryCriteria:
    """Inputs to the EAS Discovery procedure (TS 23.548 §6.2.2.2)."""

    imsi: str
    app_id: str
    dnn: Optional[str] = None
    sst: Optional[int] = None
    dnai: Optional[str] = None
    ue_lat: Optional[float] = None
    ue_lon: Optional[float] = None


@dataclass
class EAS:
    """An EAS row in the registry. Mirrors edge/eas/eas.go EAS struct."""

    eas_id: int
    app_id: str
    endpoint_url: str
    dnai: Optional[str] = None
    latitude: Optional[float] = None
    longitude: Optional[float] = None
    supported_dnns: list[str] = field(default_factory=list)
    supported_slices: list[int] = field(default_factory=list)
    capacity: int = 100
    active_connections: int = 0
    status: str = "active"
    distance_km: Optional[float] = None
    score: Optional[float] = None


@dataclass
class DnaiMapping:
    """TS 23.548 §6.8 — EAS address ↔ DNAI mapping."""

    dnai: str
    description: Optional[str] = None
    location_hint: Optional[str] = None
    upf_instance: Optional[str] = None


def haversine_km(lat1: float, lon1: float, lat2: float, lon2: float) -> float:
    """Great-circle distance between two lat/lon pairs in km."""
    r = 6371.0
    dlat = radians(lat2 - lat1)
    dlon = radians(lon2 - lon1)
    a = sin(dlat / 2) ** 2 + cos(radians(lat1)) * cos(radians(lat2)) * sin(dlon / 2) ** 2
    return 2 * r * asin(sqrt(a))


def score_eas(e: EAS, c: DiscoveryCriteria) -> float:
    """Rank an EAS against discovery criteria. Local policy — see
    package docstring for why these weights are not spec-derived."""
    score = 0.0
    if c.dnai and e.dnai == c.dnai:
        score += 50
    if c.dnn and c.dnn in e.supported_dnns:
        score += 30
    if c.sst is not None and c.sst in e.supported_slices:
        score += 20
    if e.capacity > 0:
        avail = max(0.0, (e.capacity - e.active_connections) / e.capacity)
        score += avail * 20
    if e.distance_km is not None and e.distance_km < 99999:
        from math import exp
        score += 30 * exp(-e.distance_km / 30)
    return round(score, 2)


def discover(candidates: list[EAS], c: DiscoveryCriteria) -> list[EAS]:
    """TS 23.548 §6.2.2.2 — EAS Discovery (Distributed Anchor model).

    For the Session-Breakout connectivity model (TS 23.548 §6.2.3.2.2),
    use discover_via_easdf below — EASDF-driven DNS resolution,
    SMF inserts ULCL/BP/L-PSA from the response.
    """
    matched = [e for e in candidates if e.app_id == c.app_id and e.status == "active"]
    for e in matched:
        if c.ue_lat is not None and c.ue_lon is not None and e.latitude is not None and e.longitude is not None:
            e.distance_km = round(haversine_km(c.ue_lat, c.ue_lon, e.latitude, e.longitude), 2)
        e.score = score_eas(e, c)
    matched.sort(key=lambda e: (e.score or 0), reverse=True)
    return matched


@dataclass
class EASDFAnswer:
    """EASDF response to a UE DNS Query (TS 23.548 §6.2.3.2.2).

    The SMF uses the result to insert ULCL/BP/Local-PSA towards
    the selected EAS. The DNAI carries the EAS↔DNAI mapping
    (TS 23.548 §6.8) the SMF feeds the L-PSA placement decision.
    """

    fqdn: str
    eas: EAS
    dnai: Optional[str] = None
    ue_ip_hint: Optional[str] = None  # EDNS Client Subnet (RFC 7871)


def discover_via_easdf(
    candidates: list[EAS], fqdn: str, c: DiscoveryCriteria
) -> Optional[EASDFAnswer]:
    """TS 23.548 §6.2.3.2.2 — EAS Discovery Procedure with EASDF
    (Session-Breakout connectivity model).

    Substring-match FQDN against endpoint_url; on miss, fall back
    to AppID-narrowed candidates (matching what TS 23.548 §6.2.3.4
    AF-provided EAS Deployment Information would do at the SMF).
    """
    if not fqdn or not fqdn.strip():
        raise ValueError("fqdn is required")

    fqdn_lower = fqdn.lower()
    matched = [
        e for e in candidates
        if e.status == "active" and fqdn_lower in e.endpoint_url.lower()
    ]
    if not matched and c.app_id:
        matched = [e for e in candidates if e.status == "active" and e.app_id == c.app_id]
    if not matched:
        return None  # NXDOMAIN-equivalent

    # Score with proximity dropped — Session-Breakout uses DNAI
    # for L-PSA placement, not UE-EAS topological distance.
    c2 = DiscoveryCriteria(
        imsi=c.imsi, app_id=c.app_id, dnn=c.dnn,
        sst=c.sst, dnai=c.dnai,
    )
    for e in matched:
        e.distance_km = None
        e.score = score_eas(e, c2)
    matched.sort(key=lambda e: (e.score or 0), reverse=True)

    pick = matched[0]
    return EASDFAnswer(fqdn=fqdn, eas=pick, dnai=pick.dnai)


# ─── EAS → DNAI mapping (TS 23.548 §6.8) ─────────────────────────


def map_eas_to_dnai(e: Optional[EAS]) -> str:
    """Return the DNAI assigned to an EAS row (TS 23.548 §6.8 explicit
    mapping). Empty string when no EAS or no DNAI assigned.

    TODO TS 23.548 §6.8.2: bidirectional N6/N9 routing translation
    (DNAI → UPF instance + N6 routing-info) is the SMF/SMSF's job;
    this helper just exposes the EAS-side half.
    """
    if e is None or not e.dnai:
        return ""
    return e.dnai


def resolve_dnai_for_fqdn(candidates: list[EAS], fqdn: str,
                          c: DiscoveryCriteria) -> str:
    """Compose EASDF resolve + §6.8 mapping in one call.

    Returns the L-PSA / ULCL DNAI the SMF should target for a UE
    DNS Query, or "" when no EAS matches or the matching EAS has
    no DNAI assigned.
    """
    ans = discover_via_easdf(candidates, fqdn, c)
    if ans is None or not ans.dnai:
        return ""
    return ans.dnai


# ─── ISAC sensing-session lifecycle (TS 22.137) ──────────────────


# Operator-defined enum keys; TS 22.137 §4.1 names use cases in
# narrative form but does not list these exact strings as normative.
VALID_ISAC_TYPES = frozenset({
    "presence_detection",
    "object_tracking",
    "environment_monitoring",
    "gesture_recognition",
    "intrusion_detection",
})


@dataclass
class IsacSession:
    """5G wireless sensing session (TS 22.137 §5.2.2 authorisation
    persisted as a session row)."""

    session_id: int
    sensing_type: str
    target_area: Optional[str] = None
    resolution: Optional[str] = None
    report_interval_s: int = 1
    status: str = "created"  # created | active | completed | cancelled


def isac_create(session_id: int, sensing_type: str, *,
                target_area: Optional[str] = None,
                resolution: Optional[str] = None,
                report_interval_s: int = 1) -> IsacSession:
    """TS 22.137 §5.2.2 — configure/authorise a sensing session."""
    if sensing_type not in VALID_ISAC_TYPES:
        raise ValueError(f"invalid sensing_type: {sensing_type}")
    return IsacSession(
        session_id=session_id, sensing_type=sensing_type,
        target_area=target_area, resolution=resolution,
        report_interval_s=max(1, report_interval_s),
    )


def isac_activate(s: IsacSession) -> IsacSession:
    """created → active. TS 22.137 §5.2.1 activation requirement."""
    if s.status != "created":
        raise ValueError(f"cannot activate from state {s.status!r}")
    s.status = "active"
    return s


def isac_complete(s: IsacSession) -> IsacSession:
    """active → completed."""
    if s.status != "active":
        raise ValueError(f"cannot complete from state {s.status!r}")
    s.status = "completed"
    return s


def isac_cancel(s: IsacSession) -> IsacSession:
    """created/active → cancelled. completed/cancelled stays."""
    if s.status in ("completed", "cancelled"):
        raise ValueError(f"already {s.status}")
    s.status = "cancelled"
    return s


# ─── Ranging / SL Positioning (TS 23.586) ────────────────────────


VALID_RANGING_METHODS = frozenset({"RTT", "AoA", "multi_RTT"})
VALID_RANGING_POLICIES = frozenset({"allow_all", "deny_all", "contacts_only"})


def set_privacy(privacy_db: dict, imsi: str, policy: str,
                allowed_contacts: Optional[list[str]] = None) -> 'RangingPrivacy':
    """TS 23.586 §5.1 — UPSERT a UE's ranging privacy policy.

    ``privacy_db`` is the test-local dict mirror of the
    ranging_privacy table, keyed by IMSI. allowed_contacts is only
    meaningful when policy='contacts_only' — for any other policy
    we wipe the contacts list to avoid stale-list confusion (mirrors
    the Go-side SetPrivacy behaviour).
    """
    if not imsi:
        raise ValueError("imsi is required")
    if policy not in VALID_RANGING_POLICIES:
        raise ValueError(f"invalid policy: {policy!r}")
    if policy != "contacts_only":
        allowed_contacts = None
    entry = RangingPrivacy(imsi=imsi, policy=policy,
                           allowed_contacts=list(allowed_contacts or []))
    privacy_db[imsi] = entry
    return entry


def get_privacy(privacy_db: dict, imsi: str) -> Optional['RangingPrivacy']:
    return privacy_db.get(imsi)


def delete_privacy(privacy_db: dict, imsi: str) -> bool:
    return privacy_db.pop(imsi, None) is not None


@dataclass
class RangingPrivacy:
    """TS 23.586 §5.1 / §6.2 — UE-side authorisation for being
    ranged. policy = allow_all | deny_all | contacts_only."""

    imsi: str
    policy: str = "allow_all"
    allowed_contacts: list[str] = field(default_factory=list)


@dataclass
class RangingResult:
    """TS 23.586 §6.8 — outcome of a control-plane Initiate. Empty
    measurement when ok=False."""

    ok: bool
    error: Optional[str] = None
    distance_m: Optional[float] = None
    azimuth_deg: Optional[float] = None
    elevation_deg: Optional[float] = None
    accuracy_m: Optional[float] = None


def check_privacy(p: Optional[RangingPrivacy], source_imsi: str) -> tuple[bool, str]:
    """Authorisation gate — TS 23.586 §5.1. Missing policy = allow."""
    if p is None or p.policy == "allow_all":
        return True, ""
    if p.policy == "deny_all":
        return False, "target denies all ranging"
    if p.policy == "contacts_only":
        if source_imsi not in p.allowed_contacts:
            return False, "source not in target's allowed contacts"
        return True, ""
    return False, f"unknown policy: {p.policy}"


def initiate_ranging(source_imsi: str, target_imsi: str, method: str,
                     target_privacy: Optional[RangingPrivacy] = None,
                     measurement: Optional[dict] = None) -> RangingResult:
    """TS 23.586 §6.8 — start a ranging session. The 'measurement'
    arg is the upstream measurement provider's output (RSPP / LMF
    response) — the spec's measurement transport is not modelled
    here. Defaults to a deterministic stub for tests."""
    if method == "":
        method = "RTT"
    if method not in VALID_RANGING_METHODS:
        return RangingResult(ok=False, error="invalid method")

    ok, reason = check_privacy(target_privacy, source_imsi)
    if not ok:
        return RangingResult(ok=False, error="authorization_denied")

    m = measurement or {
        "distance_m": 12.5, "azimuth_deg": 90.0,
        "elevation_deg": 0.0, "accuracy_m": 0.2,
    }
    return RangingResult(
        ok=True,
        distance_m=m.get("distance_m"),
        azimuth_deg=m.get("azimuth_deg"),
        elevation_deg=m.get("elevation_deg"),
        accuracy_m=m.get("accuracy_m"),
    )


# ─── TSN integration (TS 23.501 §5.27) ───────────────────────────


@dataclass
class TsnBridge:
    """TS 23.501 §5.27.0 — 5GS as TSN bridge with DS-TT/NW-TT ports."""

    bridge_id: str
    name: str
    ds_tt_port: str
    nw_tt_port: str
    vlan_id: Optional[int] = None
    status: str = "active"


@dataclass
class TsnStream:
    """TS 23.501 §5.27.2 TSCAI parameters + §5.27.3 5QI mapping."""

    stream_id: str
    bridge_id: str
    traffic_class: int
    priority: int
    max_frame_size: int = 1522  # default Ethernet+VLAN
    interval_us: int = 1000     # default 1ms
    mapped_5qi: Optional[int] = None
    pdb_ms: Optional[float] = None


@dataclass
class ClockDomain:
    """TS 23.501 §5.27.1 gPTP clock domain."""

    domain_id: str
    gm_identity: str
    sync_accuracy_ns: int
    holdover_capability_s: int
    status: str = "freerun"  # freerun | locked | synced



# ─── MEC-side ────────────────────────────────────────────────────


@dataclass
class EdgeSite:
    """An edge site = a LADN service area (TS 23.501 §5.6.5).

    `tais` is the LADN service area: the set of Tracking Area
    identities the SMF uses to pick this site's PSA-UPF.
    """

    site_id: str
    name: str
    tais: list[str]
    local_dn_ip: str
    local_dn_cidr: str
    capacity: int = 100
    status: str = "active"  # active | maintenance | offline


@dataclass
class TrafficRule:
    """TS 23.502 §4.3.6 AF traffic-routing influence rule.

    One rule binds an (app_id, site_id, dnn) triple to a steering
    target so the SMF programs the local PSA-UPF / ULCL accordingly.
    The authoritative AF-influence procedure (NEF
    Nnef_TrafficInfluence, PCF Npcf_PolicyAuthorization) is out of
    scope; the rule structure here covers only the AF-request fields
    the SMF consumes.
    """

    rule_id: str
    app_id: str
    site_id: str
    dnn: str
    target_ip: Optional[str] = None
    target_fqdn: Optional[str] = None
    target_port: int = 0
    priority: int = 0


def find_site_by_tai(sites: list[EdgeSite], tai: str) -> Optional[EdgeSite]:
    """Pick the active site whose LADN service area covers `tai`
    (TS 23.501 §5.6.5)."""
    for s in sites:
        if s.status != "active":
            continue
        if tai in s.tais:
            return s
    return None


def find_app_by_fqdn(apps_by_fqdn: dict[str, str], fqdn: str) -> Optional[str]:
    """Models the EASDF lookup output (TS 23.548 §6.2.3.2.2). DNS
    names are case-insensitive by convention, so we fold case.
    Returns the app_id or None."""
    return apps_by_fqdn.get(fqdn.lower())

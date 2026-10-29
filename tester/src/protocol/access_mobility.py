# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Access + Mobility primitives — tester-side mirror.

Mirrors the Go core's:

  * security/ran_sharing  — NG-RAN Sharing operator surface.
  * nf/amf/n26            — AMF-side N26 audit log.
  * access/epc/mme/n26    — MME-side N26 mapped-context cache.
  * infra/roaming         — Inter-PLMN roaming agreement + sessions.
  * access/wifi_offload   — Non-3GPP (WLAN) admission + attach table.
  * access/ntn (Phase 2)  — Backhaul / S&F / ISL operator stubs.

Pure dataclasses + functions + in-memory stores; no live core, no
DB, no network. Suitable for round-trip fixtures alongside live
behaviour tests of the core.

Spec anchors (§-checked by speccheck against PDFs in specs/common/):

  RAN Sharing — TS 22.261 §6.21, TS 23.501 §5.17.4
  -----------
  * TS 22.261 §6.21        NG-RAN Sharing umbrella.
  * TS 22.261 §6.21.2.2    Indirect network sharing.
  * TS 23.501 §5.17.4      Network sharing visible to the 5GC.

  N26 Handover — TS 23.501 §5.17.2.x, TS 23.502 §4.11
  ------------
  * TS 23.501 §5.17.2      Interworking with EPC umbrella.
  * TS 23.501 §5.17.2.2    Interworking with N26 interface.
  * TS 23.501 §5.17.2.2.1  General — single-registration.
  * TS 23.501 §5.17.2.2.2  Mobility for single-registration UEs.
  * TS 23.502 §4.11        End-to-end procedure flow.

  Roaming — TS 23.501 §5.6.3, §5.7.1.11
  -------
  * TS 23.501 §5.6.3       Session Management — Roaming (HR/LBO).
  * TS 23.501 §5.7.1.11    QoS aspects of home-routed roaming.

  WiFi Offload — TS 23.501 §4.2.7, §4.2.8, §6.2.9, §6.2.9A
  ------------
  * TS 23.501 §4.2.7       Reference points (incl. N3/Y1/Y2).
  * TS 23.501 §4.2.8       Support of non-3GPP access (umbrella).
  * TS 23.501 §5.10.2      Security model for non-3GPP.
  * TS 23.501 §6.2.9       N3IWF (untrusted gateway).
  * TS 23.501 §6.2.9A      TNGF (trusted gateway).

  NTN Phase 2 — TS 23.501 §5.43, TS 22.261 §6.3.2.3
  -----------
  * TS 23.501 §5.43        Support for 5G Satellite Backhaul.
  * TS 22.261 §6.3.2.3     Service requirements for satellite access.

Deferred (TODO at unimplemented surfaces):

  * TODO(spec: TS 23.251)  NG-RAN Sharing Stage 2 — broadcast list
                           of multi-PLMN-IDs is the gNB's job.
  * TODO(spec: TS 38.821)  S&F + ISL normative anchors are pending.
  * TODO(spec: TS 23.501)  ATSSS multi-access PDU split.
  * TODO TS 23.501 §4.2.8.5  TWIF for non-NAS WLAN devices.
  * TODO TS 23.501 §5.17.2.3 Interworking without N26.
"""

from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Optional


# ════════════════════════════════════════════════════════════════════
# RAN Sharing — TS 22.261 §6.21, TS 23.501 §5.17.4
# ════════════════════════════════════════════════════════════════════

RAN_SHARING_TYPES = frozenset({"MORAN", "MOCN"})
RAN_AGREEMENT_STATUSES = frozenset({"active", "inactive", "pending"})


@dataclass
class RANSharingAgreement:
    """Mirror of ran_sharing_agreements row."""
    id: int
    name: str
    sharing_type: str
    participating_plmns: str  # comma- or space-separated PLMN list
    capacity_split: dict = field(default_factory=dict)
    priority_rules: dict = field(default_factory=dict)
    status: str = "pending"


@dataclass
class RANSharingGnBMap:
    """Mirror of ran_sharing_gnb_map row — per-gNB allocation."""
    agreement_id: int
    gnb_id: str
    allocated_capacity_pct: int = 0


@dataclass
class RANAccessResult:
    """Mirror of CheckAccess result on the Go side."""
    allowed: bool
    reason: str = ""
    agreement_id: int = 0
    agreement_name: str = ""
    sharing_type: str = ""
    capacity_pct: int = 0


def create_ran_agreement(store: list, name: str, sharing_type: str,
                         plmns: str, *, capacity_split: Optional[dict] = None,
                         priority_rules: Optional[dict] = None) -> RANSharingAgreement:
    """TS 22.261 §6.21 — register a new sharing agreement.

    `store` is a plain list mirroring the SQL table. The new
    agreement defaults to status='pending' (operator must Activate).
    """
    if not name:
        raise ValueError("agreement name is required")
    if sharing_type not in RAN_SHARING_TYPES:
        raise ValueError(f"sharing_type must be MORAN or MOCN, got {sharing_type!r}")
    if not plmns:
        raise ValueError("participating_plmns is required")
    next_id = (store[-1].id + 1) if store else 1
    agr = RANSharingAgreement(
        id=next_id, name=name, sharing_type=sharing_type,
        participating_plmns=plmns,
        capacity_split=dict(capacity_split or {}),
        priority_rules=dict(priority_rules or {}),
        status="pending",
    )
    store.append(agr)
    return agr


def activate_ran_agreement(store: list, agr_id: int) -> Optional[RANSharingAgreement]:
    for a in store:
        if a.id == agr_id:
            a.status = "active"
            return a
    return None


def upsert_ran_gnb_map(gnb_store: list, agreement_id: int,
                       gnb_id: str, capacity_pct: int) -> RANSharingGnBMap:
    if not 0 <= capacity_pct <= 100:
        raise ValueError("capacity_pct must be in [0, 100]")
    for m in gnb_store:
        if m.agreement_id == agreement_id and m.gnb_id == gnb_id:
            m.allocated_capacity_pct = capacity_pct
            return m
    m = RANSharingGnBMap(
        agreement_id=agreement_id, gnb_id=gnb_id,
        allocated_capacity_pct=capacity_pct,
    )
    gnb_store.append(m)
    return m


def _plmn_in_list(plmn: str, plmns: str) -> bool:
    """Exact match against comma/space/semicolon-separated PLMN list."""
    if not plmn:
        return False
    s = plmns
    for sep in (";", " ", "\t"):
        s = s.replace(sep, ",")
    return plmn in {p.strip() for p in s.split(",") if p.strip()}


def ran_check_access(store: list, gnb_store: list,
                     plmn: str, gnb_id: str) -> RANAccessResult:
    """Composite admission gate — first matching active agreement wins.

    MOCN admits on PLMN match alone; MORAN requires an explicit gNB
    map row (the per-PLMN spectrum/capacity slice).
    """
    for agr in store:
        if agr.status != "active":
            continue
        if not _plmn_in_list(plmn, agr.participating_plmns):
            continue
        for m in gnb_store:
            if m.agreement_id == agr.id and m.gnb_id == gnb_id:
                return RANAccessResult(
                    allowed=True, reason="matched per-gNB allocation",
                    agreement_id=agr.id, agreement_name=agr.name,
                    sharing_type=agr.sharing_type,
                    capacity_pct=m.allocated_capacity_pct,
                )
        if agr.sharing_type == "MOCN":
            return RANAccessResult(
                allowed=True, reason="MOCN agreement (no per-gNB cap)",
                agreement_id=agr.id, agreement_name=agr.name,
                sharing_type="MOCN",
            )
    return RANAccessResult(allowed=False, reason="no matching active agreement")


# ════════════════════════════════════════════════════════════════════
# N26 Handover — TS 23.501 §5.17.2.x, TS 23.502 §4.11
# ════════════════════════════════════════════════════════════════════

N26_RATS = frozenset({"4G", "5G"})
N26_STATUSES = frozenset({"initiated", "completed", "failed"})


@dataclass
class N26HandoverRow:
    """Mirror of n26_handover_log row."""
    id: int
    imsi: str
    source_rat: str
    target_rat: str
    status: str = "initiated"


def n26_log_handover(store: list, imsi: str, source: str, target: str,
                     status: str) -> N26HandoverRow:
    """TS 23.501 §5.17.2.2.2 — record one step of an N26 handover."""
    if not imsi:
        raise ValueError("imsi is required")
    if source not in N26_RATS or target not in N26_RATS:
        raise ValueError("source / target RAT must be 4G or 5G")
    if status not in N26_STATUSES:
        raise ValueError("status must be initiated|completed|failed")
    next_id = (store[-1].id + 1) if store else 1
    row = N26HandoverRow(
        id=next_id, imsi=imsi, source_rat=source,
        target_rat=target, status=status,
    )
    store.append(row)
    return row


def n26_mark_completed(store: list, row_id: int) -> bool:
    for r in store:
        if r.id == row_id and r.status == "initiated":
            r.status = "completed"
            return True
    return False


def n26_mark_failed(store: list, row_id: int) -> bool:
    for r in store:
        if r.id == row_id and r.status == "initiated":
            r.status = "failed"
            return True
    return False


# ──── MME-side mapped-context cache (TS 23.501 §5.17.2.2.1) ────


N26_TTL_S = 120


@dataclass
class N26MappedContext:
    imsi: str
    kasme: bytes = b""
    eps_bearers: list = field(default_factory=list)
    ue_info: dict = field(default_factory=dict)
    timestamp: float = 0.0
    used: bool = False


def n26_receive_context(cache: dict, imsi: str, kasme: bytes,
                        bearers: list, ue_info: dict) -> dict:
    """AMF→MME push of a mapped context (TS 23.502 §4.11)."""
    cache[imsi] = N26MappedContext(
        imsi=imsi, kasme=kasme, eps_bearers=list(bearers or []),
        ue_info=dict(ue_info or {}), timestamp=time.time(),
    )
    return {"status": "stored", "imsi": imsi}


def n26_get_context(cache: dict, imsi: str,
                    now: Optional[float] = None) -> Optional[N26MappedContext]:
    """Return the cached context if fresh + unused; else None."""
    now = now or time.time()
    ctx = cache.get(imsi)
    if ctx is None:
        return None
    if (now - ctx.timestamp) >= N26_TTL_S:
        del cache[imsi]
        return None
    if ctx.used:
        return None
    return ctx


def n26_consume_context(cache: dict, imsi: str) -> Optional[N26MappedContext]:
    """Mark consumed and remove."""
    return cache.pop(imsi, None)


# ════════════════════════════════════════════════════════════════════
# Roaming — TS 23.501 §5.6.3, §5.7.1.11
# ════════════════════════════════════════════════════════════════════

ROAMING_DIRECTIONS = frozenset({"inbound", "outbound", "both"})
ROAMING_MODES = frozenset({"hr", "lbo", "both"})


@dataclass
class RoamingAgreement:
    """Mirror of roaming_agreements row."""
    partner_plmn_id: str
    partner_name: str = ""
    direction: str = "both"
    roaming_mode: str = "hr"
    max_ues: int = 0
    enabled: bool = True


@dataclass
class RoamingDetectResult:
    """Mirror of DetectResult on the Go side."""
    is_roaming: bool
    home_plmn_id: str
    agreement: Optional[RoamingAgreement] = None
    roaming_mode: str = ""


def upsert_roaming_agreement(store: dict, partner_plmn_id: str, *,
                             partner_name: str = "",
                             direction: str = "both",
                             roaming_mode: str = "hr",
                             max_ues: int = 0,
                             enabled: bool = True) -> RoamingAgreement:
    """TS 23.501 §5.6.3 — UPSERT a roaming agreement."""
    if direction not in ROAMING_DIRECTIONS:
        raise ValueError(f"direction must be one of {ROAMING_DIRECTIONS}")
    if roaming_mode not in ROAMING_MODES:
        raise ValueError(f"roaming_mode must be one of {ROAMING_MODES}")
    a = RoamingAgreement(
        partner_plmn_id=partner_plmn_id, partner_name=partner_name,
        direction=direction, roaming_mode=roaming_mode,
        max_ues=max_ues, enabled=enabled,
    )
    store[partner_plmn_id] = a
    return a


def is_roaming_allowed(store: dict, home_plmn_id: str) -> Optional[RoamingAgreement]:
    """Return the agreement iff inbound roaming is admissible."""
    a = store.get(home_plmn_id)
    if a is None or not a.enabled:
        return None
    if a.direction not in ("inbound", "both"):
        return None
    return a


def detect_roaming(store: dict, imsi: str) -> Optional[RoamingDetectResult]:
    """TS 23.501 §5.6.3 — derive HPLMN from IMSI, look up agreement.

    Tries 3-digit MNC then 2-digit (matches the Go code path).
    """
    if len(imsi) < 5:
        return None
    mcc = imsi[:3]
    for mnc_len in (3, 2):
        if len(imsi) < 3 + mnc_len:
            continue
        mnc = imsi[3:3 + mnc_len]
        candidate = f"{mcc}-{mnc}"
        a = store.get(candidate)
        if a is not None and a.enabled:
            allowed = is_roaming_allowed(store, candidate)
            if allowed is None:
                return RoamingDetectResult(
                    is_roaming=True, home_plmn_id=candidate,
                )
            return RoamingDetectResult(
                is_roaming=True, home_plmn_id=candidate,
                agreement=allowed, roaming_mode=allowed.roaming_mode,
            )
    return RoamingDetectResult(
        is_roaming=True, home_plmn_id=f"{mcc}-{imsi[3:5]}",
    )


# ════════════════════════════════════════════════════════════════════
# WiFi Offload — TS 23.501 §4.2.8, §6.2.9, §6.2.9A
# ════════════════════════════════════════════════════════════════════

WIFI_ACCESS_TYPES = frozenset({"untrusted", "trusted", "wireline"})
WIFI_OFFLOAD_PREFS = frozenset({
    "5g_first", "wlan_first", "5g_only", "wlan_only", "atsss",
})


@dataclass
class WifiAccessPolicy:
    """Mirror of wifi_access_policy row."""
    dnn: str
    access_type: str = "untrusted"
    offload_pref: str = "5g_first"
    enabled: bool = True


@dataclass
class WifiAttachedUE:
    """Mirror of wifi_attached_ues row."""
    imsi: str
    access_type: str
    n3iwf_id: str = ""
    inner_ip: str = ""
    outer_ip: str = ""


@dataclass
class WifiAdmissionResult:
    """Mirror of CheckOffload result on the Go side."""
    allowed: bool
    reason: str
    access_type: str = ""
    offload_pref: str = ""


def set_wifi_policy(store: dict, dnn: str, access_type: str,
                    offload_pref: str, enabled: bool) -> WifiAccessPolicy:
    """TS 23.501 §4.2.8 — UPSERT the per-DNN access policy."""
    if not dnn:
        raise ValueError("dnn is required")
    if access_type not in WIFI_ACCESS_TYPES:
        raise ValueError(f"invalid access_type: {access_type!r}")
    if offload_pref not in WIFI_OFFLOAD_PREFS:
        raise ValueError(f"invalid offload_pref: {offload_pref!r}")
    p = WifiAccessPolicy(
        dnn=dnn, access_type=access_type,
        offload_pref=offload_pref, enabled=enabled,
    )
    store[dnn] = p
    return p


def attach_wifi_ue(table: dict, imsi: str, access_type: str, *,
                   n3iwf_id: str = "", inner_ip: str = "",
                   outer_ip: str = "") -> WifiAttachedUE:
    """UPSERT key = (imsi, access_type)."""
    if not imsi:
        raise ValueError("imsi is required")
    if access_type not in WIFI_ACCESS_TYPES:
        raise ValueError(f"invalid access_type: {access_type!r}")
    ue = WifiAttachedUE(
        imsi=imsi, access_type=access_type,
        n3iwf_id=n3iwf_id, inner_ip=inner_ip, outer_ip=outer_ip,
    )
    table[(imsi, access_type)] = ue
    return ue


def detach_wifi_ue(table: dict, imsi: str, access_type: str) -> bool:
    return table.pop((imsi, access_type), None) is not None


def check_wifi_offload(policy_store: dict, imsi: str, dnn: str,
                       access_type: str) -> WifiAdmissionResult:
    """Composite admission gate — same rules as the Go core."""
    if access_type not in WIFI_ACCESS_TYPES:
        return WifiAdmissionResult(
            allowed=False, reason=f"invalid access_type {access_type!r}",
        )
    pol = policy_store.get(dnn)
    if pol is None:
        if access_type != "untrusted":
            return WifiAdmissionResult(
                allowed=False,
                reason="no policy for DNN; default refuses non-untrusted access",
                access_type="untrusted", offload_pref="5g_first",
            )
        return WifiAdmissionResult(
            allowed=True,
            reason="default policy (no DNN row): untrusted N3IWF, 5g_first",
            access_type="untrusted", offload_pref="5g_first",
        )
    if not pol.enabled:
        return WifiAdmissionResult(
            allowed=False, reason=f"policy for DNN {dnn!r} is disabled",
        )
    if pol.offload_pref == "5g_only":
        return WifiAdmissionResult(
            allowed=False,
            reason="DNN policy is 5g_only; WLAN access not permitted",
            offload_pref=pol.offload_pref,
        )
    if pol.offload_pref == "wlan_only" and pol.access_type != access_type:
        return WifiAdmissionResult(
            allowed=False,
            reason="DNN policy is wlan_only and access_type does not match",
            offload_pref=pol.offload_pref,
        )
    return WifiAdmissionResult(
        allowed=True,
        reason=f"admitted under DNN {dnn!r} policy",
        access_type=pol.access_type, offload_pref=pol.offload_pref,
    )


# ════════════════════════════════════════════════════════════════════
# NTN Phase 2 — TS 23.501 §5.43, TS 22.261 §6.3.2.3
# ════════════════════════════════════════════════════════════════════


@dataclass
class NTNBackhaulLink:
    """Mirror of BackhaulLink — one terrestrial-gNB → satellite link."""
    gnb_id: str
    satellite_id: str
    capacity_mbps: float
    current_mbps: float = 0.0
    active: bool = True


def ntn_provision_backhaul(store: dict, gnb_id: str, sat_id: str,
                           capacity_mbps: float) -> NTNBackhaulLink:
    """TS 23.501 §5.43 — register a satellite backhaul link."""
    if not gnb_id or not sat_id:
        raise ValueError("gnb_id and satellite_id are required")
    if capacity_mbps <= 0:
        raise ValueError("capacity_mbps must be > 0")
    link = NTNBackhaulLink(
        gnb_id=gnb_id, satellite_id=sat_id,
        capacity_mbps=capacity_mbps, current_mbps=0.0, active=True,
    )
    store[gnb_id] = link
    return link


def ntn_update_backhaul_usage(store: dict, gnb_id: str, mbps: float) -> bool:
    link = store.get(gnb_id)
    if link is None:
        return False
    link.current_mbps = max(0.0, mbps)
    return True


def ntn_backhaul_stats(store: dict) -> dict:
    total_cap = sum(l.capacity_mbps for l in store.values())
    total_cur = sum(l.current_mbps for l in store.values())
    active = sum(1 for l in store.values() if l.active)
    util = (total_cur / total_cap) * 100.0 if total_cap > 0 else 0.0
    return {
        "total_links": len(store), "active_links": active,
        "total_capacity_mbps": total_cap,
        "total_usage_mbps": total_cur,
        "utilization_pct": util,
    }


# ──── Store-and-Forward (TODO TS 38.821) ────


@dataclass
class NTNSAFQueue:
    """Mirror of SAFQueue — per-IMSI on-board buffer counter."""
    imsi: str
    queued_bytes: int = 0


def ntn_saf_enqueue(store: dict, imsi: str, num_bytes: int) -> None:
    if not imsi:
        raise ValueError("imsi is required")
    num_bytes = max(0, num_bytes)
    q = store.get(imsi)
    if q is None:
        q = NTNSAFQueue(imsi=imsi)
        store[imsi] = q
    q.queued_bytes += num_bytes


def ntn_saf_drain(store: dict, imsi: str, num_bytes: int) -> None:
    if not imsi:
        raise ValueError("imsi is required")
    num_bytes = max(0, num_bytes)
    q = store.get(imsi)
    if q is None:
        return
    q.queued_bytes = max(0, q.queued_bytes - num_bytes)


# ──── Inter-Satellite Links (TODO TS 38.821) ────


@dataclass
class NTNISLLink:
    """Mirror of ISLLink — directed (from → to) operator-visible hop."""
    src: str
    dst: str
    bandwidth_mbps: float
    active: bool = True


def ntn_add_isl(store: dict, src: str, dst: str, bw_mbps: float) -> NTNISLLink:
    if not src or not dst:
        raise ValueError("src and dst are required")
    if src == dst:
        raise ValueError("self-loop links are not allowed")
    if bw_mbps <= 0:
        raise ValueError("bandwidth_mbps must be > 0")
    link = NTNISLLink(src=src, dst=dst, bandwidth_mbps=bw_mbps, active=True)
    store[(src, dst)] = link
    return link


def ntn_isl_neighbours(store: dict, src: str) -> list:
    """Return active downstream neighbours of `src` in stable order."""
    return sorted(
        link.dst for (s, _), link in store.items()
        if s == src and link.active
    )

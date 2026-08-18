# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""DPI / Application Detection — tester-side mirror.

Mirrors the Go core's security/dpi package. Pure dataclasses + in-memory
stores; no live core, no DB, no network. The goal is to exercise the
same enumerations (detection types) and classifier semantics (SNI glob,
DNS suffix-match, IP CIDR, port range, DNS-pin cache) so a contract
drift on either side trips a test on this side.

Spec anchors (verifiable against local PDFs):

  * TS 23.501 §5.8.2.4    Traffic Detection — the operator surface this
                          file mirrors (per-app classification of UP traffic).
  * TS 23.501 §5.8.2.4.1  General — Application identifier matching
                          against PFDs is the model here.
  * TS 23.501 §5.8.2.4.2  Traffic Detection Information — what SMF
                          provisions to UPF (PDR + PFD set).
  * TS 23.501 §5.8.2.6    Charging and Usage Monitoring Handling —
                          per-app UL/DL counters.
  * TS 23.501 §5.8.2.8.4  Support of PFD Management — PFD lifecycle.
  * TS 23.502 §4.4.3.5    N4 PFD management Procedure — Stage 2.
  * TS 29.244 §6.2.5      PFCP PFD Management Procedure — Stage 3.
  * TS 29.244 §6.2.5.1    General.
  * TS 29.244 §6.2.5.2    CP Function Behaviour.
  * TS 29.244 §6.2.5.3    UP Function Behaviour.
  * TS 29.244 §7.4.3      PFCP PFD Management messages.
  * TS 29.244 §5.4.5      DL Flow Level Marking for Application Detection.

Deferred (PDFs not loaded; cited as TODO(spec:) prose):

  * TODO(spec: TS 23.503)  PCC framework — PFD push from NEF/PFDF into
                           SMF + NWDAF "Determination analytics".
  * TODO(spec: TS 29.122)  T8 reference point — AF→NEF northbound for
                           posting PFDs into the network.
"""

from __future__ import annotations

import fnmatch
import ipaddress
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Optional


# ════════════════════════════════════════════════════════════════════
# Detection types — must mirror dpi_pfd_rules.detection_type CHECK
# ════════════════════════════════════════════════════════════════════

DETECTION_SNI = "sni"
DETECTION_DNS = "dns"
DETECTION_IP_RANGE = "ip_range"
DETECTION_HOST = "host"
DETECTION_PORT_RANGE = "port_range"

DETECTION_TYPES = (
    DETECTION_SNI,
    DETECTION_DNS,
    DETECTION_IP_RANGE,
    DETECTION_HOST,
    DETECTION_PORT_RANGE,
)


# ════════════════════════════════════════════════════════════════════
# Dataclasses
# ════════════════════════════════════════════════════════════════════


@dataclass
class Application:
    """Mirror of dpi_applications row."""

    app_id: str
    app_name: str
    category: str = "general"
    qos_profile: str = ""
    charging_profile: str = ""
    priority: int = 100
    enabled: bool = True


def validate_application(a: Application) -> Optional[str]:
    if not a.app_id or not a.app_name:
        return "app_id and app_name required"
    return None


@dataclass
class PFDRule:
    """Mirror of dpi_pfd_rules row."""

    app_id: str
    detection_type: str
    pattern: str
    rule_id: int = 0  # auto-assigned by the Store


def validate_pfd_rule(r: PFDRule) -> Optional[str]:
    if not r.app_id:
        return "app_id required"
    if r.detection_type not in DETECTION_TYPES:
        return f"invalid detection_type {r.detection_type!r}"
    if not r.pattern:
        return "pattern required"
    return None


@dataclass
class DNSCacheEntry:
    domain: str
    resolved_ip: str
    app_id: str = ""
    cached_at: Optional[datetime] = None
    ttl_sec: int = 300


@dataclass
class DetectionLogEntry:
    """Mirror of dpi_detection_log row."""

    imsi: str
    app_id: str
    pdu_session_id: int = 0
    bytes_ul: int = 0
    bytes_dl: int = 0
    first_seen: Optional[datetime] = None
    last_seen: Optional[datetime] = None
    log_id: int = 0


# ════════════════════════════════════════════════════════════════════
# Pure classifiers (no DB) — match the Go behaviour exactly
# ════════════════════════════════════════════════════════════════════


def match_sni(sni: str, pattern: str) -> bool:
    """Mirror of matchGlob in Go — case-insensitive fnmatch."""
    return fnmatch.fnmatch(sni.lower(), pattern.lower())


def match_dns(domain: str, pattern: str) -> bool:
    """Mirror of the DNS suffix-match logic.

    'youtube.com' matches:
      youtube.com, www.youtube.com, m.youtube.com,
      anything.youtube.com.

    Does NOT match:
      fakeyoutube.com  (no dot boundary before pattern).
    """
    dom = domain.lower().rstrip(".")
    pat = pattern.lower()
    return dom == pat or dom.endswith("." + pat)


def parse_port_range(s: str) -> Optional[tuple[int, int]]:
    """Mirror of parsePortRange — accepts 'lo' or 'lo-hi'."""
    s = s.strip()
    if not s:
        return None
    parts = s.split("-", 1)
    try:
        lo = int(parts[0])
    except ValueError:
        return None
    if lo < 0 or lo > 65535:
        return None
    if len(parts) == 1:
        return (lo, lo)
    try:
        hi = int(parts[1])
    except ValueError:
        return None
    if hi < lo or hi > 65535:
        return None
    return (lo, hi)


# ════════════════════════════════════════════════════════════════════
# In-memory store mirror — black-box equivalent of the Go DB surface
# ════════════════════════════════════════════════════════════════════


class DPIStore:
    """In-memory mirror of the security/dpi package."""

    DEDUP_WINDOW = timedelta(hours=1)

    def __init__(self) -> None:
        self.apps: dict[str, Application] = {}
        self.pfd_rules: list[PFDRule] = []
        self._next_rule_id = 1
        self.dns_cache: dict[tuple[str, str], DNSCacheEntry] = {}
        self.detection_log: list[DetectionLogEntry] = []
        self._next_log_id = 1

    # ─── Application CRUD ──────────────────────────────────────────

    def upsert_app(self, a: Application) -> Optional[str]:
        err = validate_application(a)
        if err:
            return err
        existing = self.apps.get(a.app_id)
        if existing:
            existing.app_name = a.app_name
            existing.category = a.category
            existing.qos_profile = a.qos_profile
            existing.charging_profile = a.charging_profile
            existing.priority = a.priority
        else:
            self.apps[a.app_id] = a
        return None

    def delete_app(self, app_id: str) -> None:
        self.apps.pop(app_id, None)
        # Cascade — same as the FK in dpi_pfd_rules.
        self.pfd_rules = [r for r in self.pfd_rules if r.app_id != app_id]

    def list_apps(self) -> list[Application]:
        return sorted(self.apps.values(), key=lambda a: (a.priority, a.app_id))

    def set_enabled(self, app_id: str, enabled: bool) -> bool:
        a = self.apps.get(app_id)
        if a is None:
            return False
        a.enabled = enabled
        return True

    # ─── PFD CRUD ──────────────────────────────────────────────────

    def add_pfd_rule(self, r: PFDRule) -> Optional[str]:
        err = validate_pfd_rule(r)
        if err:
            return err
        r.rule_id = self._next_rule_id
        self._next_rule_id += 1
        self.pfd_rules.append(r)
        return None

    def delete_pfd_rule(self, rule_id: int) -> bool:
        before = len(self.pfd_rules)
        self.pfd_rules = [r for r in self.pfd_rules if r.rule_id != rule_id]
        return len(self.pfd_rules) < before

    def get_pfd_rules(self, app_id: str = "") -> list[PFDRule]:
        if app_id:
            return [r for r in self.pfd_rules if r.app_id == app_id]
        return list(self.pfd_rules)

    # ─── Detection ─────────────────────────────────────────────────

    def detect_by_sni(self, sni: str) -> str:
        if not sni:
            return ""
        for r in self.pfd_rules:
            if r.detection_type == DETECTION_SNI and match_sni(sni, r.pattern):
                return r.app_id
        return ""

    def detect_by_dns(self, domain: str) -> str:
        if not domain:
            return ""
        # 'dns' suffix-match first.
        for r in self.pfd_rules:
            if r.detection_type == DETECTION_DNS and match_dns(domain, r.pattern):
                return r.app_id
        # 'sni' glob fallback.
        for r in self.pfd_rules:
            if r.detection_type == DETECTION_SNI and match_sni(domain.lower().rstrip("."), r.pattern):
                return r.app_id
        return ""

    def detect_by_ip(self, ip_addr: str) -> str:
        if not ip_addr:
            return ""
        # DNS-pin cache fast path.
        for entry in self.dns_cache.values():
            if entry.resolved_ip == ip_addr and entry.app_id:
                return entry.app_id
        # CIDR walk.
        try:
            parsed = ipaddress.ip_address(ip_addr)
        except ValueError:
            return ""
        for r in self.pfd_rules:
            if r.detection_type != DETECTION_IP_RANGE:
                continue
            try:
                net = ipaddress.ip_network(r.pattern, strict=False)
            except ValueError:
                continue
            if parsed in net:
                return r.app_id
        return ""

    def detect_by_port(self, port: int) -> str:
        if port <= 0 or port > 65535:
            return ""
        for r in self.pfd_rules:
            if r.detection_type != DETECTION_PORT_RANGE:
                continue
            rng = parse_port_range(r.pattern)
            if rng is None:
                continue
            if rng[0] <= port <= rng[1]:
                return r.app_id
        return ""

    # ─── DNS cache ─────────────────────────────────────────────────

    def cache_dns(self, domain: str, resolved_ips: list[str], ttl: int = 300, *, now: Optional[datetime] = None) -> None:
        now = now or datetime.now(timezone.utc)
        app_id = self.detect_by_dns(domain)
        for ip in resolved_ips:
            self.dns_cache[(domain, ip)] = DNSCacheEntry(
                domain=domain, resolved_ip=ip, app_id=app_id,
                cached_at=now, ttl_sec=ttl,
            )

    def dns_cache_stats(self) -> dict[str, int]:
        total = len(self.dns_cache)
        matched = sum(1 for e in self.dns_cache.values() if e.app_id)
        return {"total_entries": total, "app_matched": matched}

    def purge_expired_dns(self, *, now: Optional[datetime] = None) -> int:
        now = now or datetime.now(timezone.utc)
        before = len(self.dns_cache)
        self.dns_cache = {
            k: e for k, e in self.dns_cache.items()
            if (e.cached_at or now) + timedelta(seconds=e.ttl_sec) >= now
        }
        return before - len(self.dns_cache)

    # ─── Detection log ─────────────────────────────────────────────

    def log_detection(self, imsi: str, app_id: str, pdu_session_id: int, bytes_ul: int, bytes_dl: int, *, now: Optional[datetime] = None) -> None:
        if not imsi or not app_id:
            return
        bytes_ul = max(bytes_ul, 0)
        bytes_dl = max(bytes_dl, 0)
        now = now or datetime.now(timezone.utc)
        cutoff = now - self.DEDUP_WINDOW
        # Coalesce within the dedup window.
        for e in reversed(self.detection_log):
            if e.imsi == imsi and e.app_id == app_id and (e.last_seen or now) > cutoff:
                e.bytes_ul += bytes_ul
                e.bytes_dl += bytes_dl
                e.last_seen = now
                return
        self.detection_log.append(DetectionLogEntry(
            imsi=imsi, app_id=app_id, pdu_session_id=pdu_session_id,
            bytes_ul=bytes_ul, bytes_dl=bytes_dl,
            first_seen=now, last_seen=now,
            log_id=self._next_log_id,
        ))
        self._next_log_id += 1

    def get_detection_log(self, imsi: str = "", app_id: str = "", limit: int = 100) -> list[DetectionLogEntry]:
        out = self.detection_log
        if imsi:
            out = [e for e in out if e.imsi == imsi]
        if app_id:
            out = [e for e in out if e.app_id == app_id]
        # Newest-first per Go side.
        return sorted(out, key=lambda e: (e.last_seen or datetime.min.replace(tzinfo=timezone.utc), e.log_id), reverse=True)[:limit]

    def app_usage_summary(self) -> list[dict]:
        agg: dict[str, dict] = {}
        for e in self.detection_log:
            row = agg.setdefault(e.app_id, {"app_id": e.app_id, "users": set(), "total_dl": 0, "total_ul": 0})
            row["users"].add(e.imsi)
            row["total_dl"] += e.bytes_dl
            row["total_ul"] += e.bytes_ul
        out = []
        for r in agg.values():
            out.append({
                "app_id": r["app_id"],
                "users": len(r["users"]),
                "total_dl": r["total_dl"],
                "total_ul": r["total_ul"],
            })
        out.sort(key=lambda r: r["total_dl"], reverse=True)
        return out

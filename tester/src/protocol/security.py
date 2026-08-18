# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Security primitives — tester-side mirror.

Mirrors the Go core's security/{core_security,li} packages. Pure
dataclasses + in-memory stores; no live core, no DB, no network. The
goal is to give pytest a black-box surface that exercises the same
shapes (scopes, severities, protocol classes, default policies) as the
Go side, so a contract drift on either side is caught fast.

Spec anchors (verifiable against local PDFs):

  Core security — TS 33.501 §5.9, §9.2/§9.3/§9.5/§9.9
  --------------
  * TS 33.501 §5.9         Core network security (umbrella).
  * TS 33.501 §5.9.1       Trust boundaries — known-gNB roster /
                           blocked-IP list.
  * TS 33.501 §5.9.4       Requirements for monitoring 5GC signaling
                           traffic — IDS / audit-log basis.
  * TS 33.501 §9.2         N2/NGAP interface security.
  * TS 33.501 §9.3         N3/GTP-U interface security.
  * TS 33.501 §9.5         DIAMETER/GTP interfaces.
  * TS 33.501 §9.9         Non-SBA inter-PLMN interface security.
  * TS 23.501 §5.10        Security aspects (architecture).
  * TS 23.501 §5.10.3      PDU Session User Plane Security.

  Lawful Intercept — TS 33.501 §5.9 NOTE 3
  ----------------
  * TS 33.501 §5.9         The only verifiable LI mention in the
                           local security spec — SUPI is mixed into
                           KAMF derivation explicitly to support LI.

Deferred (TS PDFs not loaded; TODOs documented prose-only so the
speccheck regex won't try to ground them):

  * TODO(spec: TS 33.117)  Audit-log signing / chain-of-custody and
                           hardening checklist; not implemented.
  * TODO(spec: TS 33.127)  ADMF / POI / TF / MDF role split + the
                           X1/X2/X3 transport interfaces. The mirror
                           collapses ADMF + POI into one surface.
  * TODO(spec: TS 33.128)  Stage-3 IRI-EVENT-RECORD / CC-PDU
                           encodings; mirror keeps a JSON blob.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Optional


# ════════════════════════════════════════════════════════════════════
# Core security — TS 33.501 §5.9
# ════════════════════════════════════════════════════════════════════

# Severity tier — mirrors the DB CHECK constraint on
# security_audit_log.severity (DEBUG/INFO/WARNING/ERROR/CRITICAL).
SEVERITIES = ("DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL")

# Firewall protocol classes — mirror security_firewall_rules.protocol.
# TS 33.501 §9.2/§9.3/§9.5 cover NGAP/NAS/GTPU/SBI; "any" is the catch-all.
PROTOCOLS = ("ngap", "nas", "gtpu", "sbi", "any")

# Firewall actions — mirror security_firewall_rules.action.
ACTIONS = ("allow", "deny", "rate_limit")

# IDS severities — narrower than audit severities (no DEBUG).
IDS_SEVERITIES = ("INFO", "WARNING", "ERROR", "CRITICAL")


@dataclass
class FirewallRule:
    """Mirror of security/core_security.FirewallRule."""

    name: str
    protocol: str
    action: str
    src_cidr: str = ""
    dst_cidr: str = ""
    port_range: str = ""
    rate_limit: int = 0
    window_s: int = 0
    enabled: bool = True
    priority: int = 100


def validate_firewall_rule(r: FirewallRule) -> Optional[str]:
    """Return None if valid, else an error string."""
    if not r.name:
        return "name required"
    if r.protocol not in PROTOCOLS:
        return f"invalid protocol {r.protocol!r}"
    if r.action not in ACTIONS:
        return f"invalid action {r.action!r}"
    return None


@dataclass
class IDSSignature:
    """Mirror of security/core_security.IDSSignature."""

    name: str
    pattern: str
    severity: str = "WARNING"
    threshold: int = 1
    window_s: int = 60
    enabled: bool = True


def validate_ids_signature(s: IDSSignature) -> Optional[str]:
    if not s.name or not s.pattern:
        return "name and pattern required"
    if s.severity not in IDS_SEVERITIES:
        return f"invalid severity {s.severity!r}"
    return None


@dataclass
class BlockedIP:
    ip: str
    reason: str = ""
    added_by: str = "system"


@dataclass
class KnownGnB:
    ip: str
    gnb_id: str = ""
    added_by: str = "system"


@dataclass
class SecurityPolicy:
    """Mirror of security/core_security.SecurityPolicy."""

    name: str
    enabled: bool = True
    rate_limit_req_per_sec: int = 0
    rate_limit_window_sec: int = 0
    block_on_failure_count: int = 0


def default_policies() -> list[SecurityPolicy]:
    """Mirror of DefaultPolicies — must stay in lock-step with Go."""
    return [
        SecurityPolicy("ngap_signalling", True, 100, 10, 10),
        SecurityPolicy("nas_auth", True, 5, 60, 5),
        SecurityPolicy("gtpu_traffic", True, 10000, 1, 0),
        SecurityPolicy("s1ap_signalling", True, 100, 10, 10),
    ]


# ─── In-memory rate limiter (mirror of CheckRateLimit) ───────────


@dataclass
class _Bucket:
    count: int
    reset_at: datetime


class RateLimiter:
    """Mirror of the core_security per-key rate limiter."""

    def __init__(self) -> None:
        self._buckets: dict[str, _Bucket] = {}

    def check(self, key: str, max_per_window: int, window_s: int, *, now: Optional[datetime] = None) -> bool:
        now = now or datetime.now(timezone.utc)
        b = self._buckets.get(key)
        if b is None or now > b.reset_at:
            self._buckets[key] = _Bucket(count=1, reset_at=now + timedelta(seconds=window_s))
            return True
        b.count += 1
        return b.count <= max_per_window

    def reset(self) -> None:
        self._buckets.clear()


# ─── GTP-U perimeter checks (mirror CheckGTPUPacket) ─────────────


def check_gtpu_packet(teid: int, source_ip: str, dest_ip: str, packet_size: int) -> bool:
    """Return True if the GTP-U frame passes the perimeter guard.

    Spec anchor: TS 33.501 §9.3 (N3 perimeter slice).
    """
    if teid == 0:
        return False
    if packet_size > 9000:
        return False
    return True


# ════════════════════════════════════════════════════════════════════
# Lawful Intercept — TS 33.501 §5.9
# ════════════════════════════════════════════════════════════════════

# Warrant scope — mirror li_warrants.scope CHECK constraint.
# TS 33.127 (not loaded) defines IRI vs CC; iri+cc is the conjunction.
SCOPES = ("iri", "cc", "iri+cc")
WARRANT_STATUSES = ("active", "expired", "revoked")


@dataclass
class Warrant:
    """Mirror of li.Warrant row."""

    warrant_id: str
    authority: str
    case_reference: str
    target_imsi: str
    target_msisdn: str = ""
    scope: str = "iri"
    start_time: Optional[datetime] = None
    end_time: Optional[datetime] = None
    status: str = "active"
    mdf_endpoint: str = ""
    created_by: str = "system"


def validate_warrant(w: Warrant) -> Optional[str]:
    if not w.warrant_id or not w.authority or not w.case_reference or not w.target_imsi:
        return "warrant_id, authority, case_reference, target_imsi all required"
    if w.scope not in SCOPES:
        return f"invalid scope {w.scope!r}"
    return None


@dataclass
class IRIEvent:
    """Mirror of li_iri_events row."""

    warrant_id: str
    event_type: str
    target_imsi: str
    event_data: dict = field(default_factory=dict)
    timestamp: Optional[datetime] = None
    delivered: bool = False


@dataclass
class CCSession:
    """Mirror of li_cc_sessions row."""

    warrant_id: str
    target_imsi: str
    session_type: str = "data"
    pdu_session_id: int = 0
    call_id: str = ""
    status: str = "active"


# ─── In-memory ADMF + POI mirror ────────────────────────────────
#
# Behaviour mirrors li.go: warrants live in a dict keyed by warrant_id,
# the IMSI→active-warrants map is rebuilt on every CRUD op, and IRI/CC
# captures filter by scope. No DB, no MDF transport.


class LIStore:
    """Pure in-memory mirror of the LI ADMF + POI surface."""

    def __init__(self) -> None:
        self.warrants: dict[str, Warrant] = {}
        self.iri_events: list[IRIEvent] = []
        self.cc_sessions: list[CCSession] = []
        self._active_targets: dict[str, list[Warrant]] = {}

    def create_warrant(self, w: Warrant, *, now: Optional[datetime] = None) -> Optional[str]:
        err = validate_warrant(w)
        if err:
            return err
        now = now or datetime.now(timezone.utc)
        if w.start_time is None:
            w.start_time = now
        if w.end_time is None:
            w.end_time = now + timedelta(days=30)
        self.warrants[w.warrant_id] = w
        self._refresh_targets(now=now)
        return None

    def revoke_warrant(self, warrant_id: str) -> None:
        w = self.warrants.get(warrant_id)
        if w:
            w.status = "revoked"
            self._refresh_targets()

    def expire_warrants(self, *, now: Optional[datetime] = None) -> list[str]:
        now = now or datetime.now(timezone.utc)
        expired = []
        for wid, w in self.warrants.items():
            if w.status == "active" and w.end_time and w.end_time < now:
                w.status = "expired"
                expired.append(wid)
        if expired:
            self._refresh_targets(now=now)
        return expired

    def warrants_for_imsi(self, imsi: str) -> list[Warrant]:
        return list(self._active_targets.get(imsi, ()))

    def capture_iri(self, event_type: str, imsi: str, event_data: Optional[dict] = None) -> int:
        """Capture an IRI event for every iri/iri+cc warrant on imsi.

        Returns the number of rows actually appended.
        """
        appended = 0
        for w in self.warrants_for_imsi(imsi):
            if w.scope in ("iri", "iri+cc"):
                self.iri_events.append(
                    IRIEvent(
                        warrant_id=w.warrant_id,
                        event_type=event_type,
                        target_imsi=imsi,
                        event_data=dict(event_data or {}),
                        timestamp=datetime.now(timezone.utc),
                    )
                )
                appended += 1
        return appended

    def check_and_activate_cc(self, imsi: str, pdu_session_id: int = 0, call_id: str = "", session_type: str = "data") -> int:
        appended = 0
        for w in self.warrants_for_imsi(imsi):
            if w.scope in ("cc", "iri+cc"):
                self.cc_sessions.append(
                    CCSession(
                        warrant_id=w.warrant_id,
                        target_imsi=imsi,
                        session_type=session_type or "data",
                        pdu_session_id=pdu_session_id,
                        call_id=call_id,
                        status="active",
                    )
                )
                appended += 1
        return appended

    def deactivate_cc(self, warrant_id: str, imsi: str) -> int:
        flipped = 0
        for s in self.cc_sessions:
            if s.warrant_id == warrant_id and s.target_imsi == imsi and s.status == "active":
                s.status = "stopped"
                flipped += 1
        return flipped

    def active_cc_sessions(self, imsi: str = "") -> list[CCSession]:
        return [s for s in self.cc_sessions if s.status == "active" and (not imsi or s.target_imsi == imsi)]

    def delivery_stats(self) -> dict:
        total = len(self.iri_events)
        delivered = sum(1 for e in self.iri_events if e.delivered)
        return {"total": total, "delivered": delivered, "pending": total - delivered}

    def mark_delivered(self, warrant_id: str) -> int:
        flipped = 0
        for e in self.iri_events:
            if e.warrant_id == warrant_id and not e.delivered:
                e.delivered = True
                flipped += 1
        return flipped

    # ─── internals ──────────────────────────────────────────────

    def _refresh_targets(self, *, now: Optional[datetime] = None) -> None:
        now = now or datetime.now(timezone.utc)
        out: dict[str, list[Warrant]] = {}
        for w in self.warrants.values():
            if w.status != "active":
                continue
            if w.start_time and w.start_time > now:
                continue
            if w.end_time and w.end_time <= now:
                continue
            out.setdefault(w.target_imsi, []).append(w)
        self._active_targets = out


# ─── small helpers shared with tests ────────────────────────────

# 5G IMSI shape: 14-15 digits. Used for shallow validation of warrant
# targets in tests; not enforced by the store itself.
_IMSI_RE = re.compile(r"^\d{14,15}$")


def looks_like_imsi(s: str) -> bool:
    return bool(_IMSI_RE.match(s))

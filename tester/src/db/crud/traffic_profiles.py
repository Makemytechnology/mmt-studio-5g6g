# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Traffic profile, group, and PDU-session-flow CRUD.
#
# A profile is a reusable declarative test spec (DNN × 5QI × protocol × rates).
# A group binds a profile to a set of UEs with a run pattern.
# pdu_session_flows is a runtime snapshot so the engine can resolve
# (imsi, dnn, 5qi) → (tun, ue_ip) without hunting through the UE FSM pool.

import logging
from typing import Dict, List, Optional

from src.db.engine import get_db

log = logging.getLogger("tester.db.traffic_profiles")


# ──────────────────────────────────────────────────────────────────────────
# 5QI ↔ DSCP mapping (3GPP TS 23.501 Annex A — default recommendations).
# Override per-profile by setting dscp explicitly; -1 means "derive from 5qi".
# iperf3 --tos takes the full 8-bit TOS byte, so we shift dscp<<2 at use-time.

FIVE_QI_TO_DSCP = {
    # Non-GBR
    5:  40,   # IMS signaling          → CS5
    6:  32,   # Video buffered streaming → CS4
    7:  24,   # Voice/video live       → CS3
    8:  16,   # TCP app                 → CS2
    9:   0,   # Default internet       → BE
    # GBR
    1:  46,   # Conversational voice   → EF
    2:  34,   # Conversational video   → AF41
    3:  30,   # Real-time gaming
    4:  38,   # Non-conv buffered video
    # Mission-critical
    65: 46,   # MC-PTT voice            → EF
    66: 46,
    67: 46,
    69: 40,   # MC signaling            → CS5
    70: 40,
    # V2X
    79: 30,   # V2X messages
    80: 26,
    82: 40,   # Discrete automation
    83: 46,
    84: 40,
    85: 46,
}


def dscp_for_five_qi(five_qi: int) -> int:
    """Return the recommended DSCP value for a 5QI, or 0 if unmapped."""
    return FIVE_QI_TO_DSCP.get(int(five_qi), 0)


def tos_for_dscp(dscp: int) -> int:
    """Convert DSCP (6 bits) to TOS byte (8 bits, ECN=00)."""
    return (int(dscp) & 0x3F) << 2


# ──────────────────────────────────────────────────────────────────────────
# Profiles

_PROFILE_FIELDS = [
    "id", "dnn", "five_qi", "protocol", "direction",
    "bandwidth", "duration", "length", "codec", "dscp",
    "sla_min_kbps", "sla_max_jitter_ms", "sla_max_loss_pct", "sla_max_latency_ms",
    "notes",
]


def profile_list() -> List[Dict]:
    with get_db() as conn:
        rows = conn.execute(
            "SELECT * FROM traffic_profiles ORDER BY dnn, five_qi, id"
        ).fetchall()
        return [dict(r) for r in rows]


def profile_get(profile_id: str) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute(
            "SELECT * FROM traffic_profiles WHERE id=?", (profile_id,)
        ).fetchone()
        return dict(row) if row else None


def profile_add(profile: Dict) -> Dict:
    pid = (profile.get("id") or "").strip()
    if not pid:
        raise ValueError("profile id required")
    if profile_get(pid):
        raise ValueError(f"profile {pid!r} already exists")
    cols, vals = [], []
    for f in _PROFILE_FIELDS:
        if f in profile:
            cols.append(f)
            vals.append(profile[f])
    if "id" not in cols:
        cols.insert(0, "id")
        vals.insert(0, pid)
    placeholders = ",".join("?" * len(cols))
    with get_db() as conn:
        conn.execute(
            f"INSERT INTO traffic_profiles ({','.join(cols)}) VALUES ({placeholders})",
            vals)
        conn.commit()
    return profile_get(pid)


def profile_update(profile_id: str, updates: Dict) -> Dict:
    if not profile_get(profile_id):
        raise ValueError(f"profile {profile_id!r} not found")
    cols, vals = [], []
    for f in _PROFILE_FIELDS:
        if f == "id":
            continue
        if f in updates:
            cols.append(f"{f}=?")
            vals.append(updates[f])
    if not cols:
        return profile_get(profile_id)
    vals.append(profile_id)
    with get_db() as conn:
        conn.execute(
            f"UPDATE traffic_profiles SET {', '.join(cols)} WHERE id=?", vals)
        conn.commit()
    return profile_get(profile_id)


def profile_delete(profile_id: str) -> bool:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM traffic_profiles WHERE id=?", (profile_id,))
        conn.commit()
        return cur.rowcount > 0


def profile_resolved_dscp(profile: Dict) -> int:
    """Return the DSCP actually used for this profile.

    dscp >= 0 → use profile's override.
    dscp < 0  → derive from 5QI via FIVE_QI_TO_DSCP.
    """
    d = int(profile.get("dscp", -1))
    if d >= 0:
        return d
    return dscp_for_five_qi(profile.get("five_qi", 9))


# ──────────────────────────────────────────────────────────────────────────
# Groups + membership

_GROUP_FIELDS = ["id", "profile_id", "pattern", "ramp_s", "period_s",
                  "concurrency_limit", "notes"]


def group_list() -> List[Dict]:
    with get_db() as conn:
        rows = conn.execute(
            "SELECT g.*, "
            "       (SELECT COUNT(*) FROM traffic_group_members m WHERE m.group_id=g.id) AS member_count "
            "FROM traffic_groups g ORDER BY g.id"
        ).fetchall()
        return [dict(r) for r in rows]


def group_get(group_id: str) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute(
            "SELECT * FROM traffic_groups WHERE id=?", (group_id,)
        ).fetchone()
        if not row:
            return None
        g = dict(row)
        g["members"] = [
            r["ue_imsi"] for r in conn.execute(
                "SELECT ue_imsi FROM traffic_group_members WHERE group_id=? ORDER BY ue_imsi",
                (group_id,)).fetchall()
        ]
        return g


def group_add(group: Dict) -> Dict:
    gid = (group.get("id") or "").strip()
    if not gid:
        raise ValueError("group id required")
    if not group.get("profile_id"):
        raise ValueError("profile_id required")
    if group_get(gid):
        raise ValueError(f"group {gid!r} already exists")
    cols, vals = [], []
    for f in _GROUP_FIELDS:
        if f in group:
            cols.append(f)
            vals.append(group[f])
    if "id" not in cols:
        cols.insert(0, "id"); vals.insert(0, gid)
    placeholders = ",".join("?" * len(cols))
    with get_db() as conn:
        conn.execute(
            f"INSERT INTO traffic_groups ({','.join(cols)}) VALUES ({placeholders})",
            vals)
        conn.commit()
    return group_get(gid)


def group_update(group_id: str, updates: Dict) -> Dict:
    if not group_get(group_id):
        raise ValueError(f"group {group_id!r} not found")
    cols, vals = [], []
    for f in _GROUP_FIELDS:
        if f == "id":
            continue
        if f in updates:
            cols.append(f"{f}=?"); vals.append(updates[f])
    if not cols:
        return group_get(group_id)
    vals.append(group_id)
    with get_db() as conn:
        conn.execute(f"UPDATE traffic_groups SET {', '.join(cols)} WHERE id=?", vals)
        conn.commit()
    return group_get(group_id)


def group_delete(group_id: str) -> bool:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM traffic_groups WHERE id=?", (group_id,))
        conn.commit()
        return cur.rowcount > 0


def group_set_members(group_id: str, imsis: List[str]) -> List[str]:
    """Replace the full membership list for a group."""
    with get_db() as conn:
        conn.execute("DELETE FROM traffic_group_members WHERE group_id=?", (group_id,))
        for imsi in imsis:
            if not imsi:
                continue
            conn.execute(
                "INSERT OR IGNORE INTO traffic_group_members (group_id, ue_imsi) VALUES (?, ?)",
                (group_id, imsi))
        conn.commit()
    return list(imsis)


def group_add_member(group_id: str, imsi: str) -> bool:
    with get_db() as conn:
        cur = conn.execute(
            "INSERT OR IGNORE INTO traffic_group_members (group_id, ue_imsi) VALUES (?, ?)",
            (group_id, imsi))
        conn.commit()
        return cur.rowcount > 0


def group_remove_member(group_id: str, imsi: str) -> bool:
    with get_db() as conn:
        cur = conn.execute(
            "DELETE FROM traffic_group_members WHERE group_id=? AND ue_imsi=?",
            (group_id, imsi))
        conn.commit()
        return cur.rowcount > 0


# ──────────────────────────────────────────────────────────────────────────
# PDU session flows (runtime snapshot)

def flow_upsert(imsi: str, dnn: str, five_qi: int,
                qfi: int = 0, tun_device: str = "",
                ue_ip: str = "", dn_ip: str = "") -> Dict:
    with get_db() as conn:
        conn.execute("""
            INSERT INTO pdu_session_flows (imsi, dnn, five_qi, qfi, tun_device, ue_ip, dn_ip, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
            ON CONFLICT(imsi, dnn, five_qi) DO UPDATE SET
              qfi=excluded.qfi, tun_device=excluded.tun_device,
              ue_ip=excluded.ue_ip, dn_ip=excluded.dn_ip,
              updated_at=CURRENT_TIMESTAMP
        """, (imsi, dnn, int(five_qi), int(qfi), tun_device, ue_ip, dn_ip))
        conn.commit()
    return flow_get(imsi, dnn, five_qi)


def flow_get(imsi: str, dnn: str, five_qi: int) -> Optional[Dict]:
    with get_db() as conn:
        row = conn.execute(
            "SELECT * FROM pdu_session_flows WHERE imsi=? AND dnn=? AND five_qi=?",
            (imsi, dnn, int(five_qi))).fetchone()
        return dict(row) if row else None


def flow_list_for_ue(imsi: str) -> List[Dict]:
    with get_db() as conn:
        rows = conn.execute(
            "SELECT * FROM pdu_session_flows WHERE imsi=? ORDER BY dnn, five_qi",
            (imsi,)).fetchall()
        return [dict(r) for r in rows]


def flow_list_by_dnn(dnn: str, five_qi: int = None) -> List[Dict]:
    with get_db() as conn:
        if five_qi is None:
            rows = conn.execute(
                "SELECT * FROM pdu_session_flows WHERE dnn=? ORDER BY imsi", (dnn,)
            ).fetchall()
        else:
            rows = conn.execute(
                "SELECT * FROM pdu_session_flows WHERE dnn=? AND five_qi=? ORDER BY imsi",
                (dnn, int(five_qi))).fetchall()
        return [dict(r) for r in rows]


def flow_clear_for_ue(imsi: str) -> int:
    with get_db() as conn:
        cur = conn.execute("DELETE FROM pdu_session_flows WHERE imsi=?", (imsi,))
        conn.commit()
        return cur.rowcount


# ──────────────────────────────────────────────────────────────────────────
# Seed default profiles (3GPP-inspired)

_DEFAULT_PROFILES = [
    # id, dnn, five_qi, protocol, direction, bandwidth, duration, length, codec, dscp (-1=auto), slas, notes
    dict(id="voice_ims_5qi1",      dnn="ims",      five_qi=1,  protocol="rtp-audio", direction="bidir",
         bandwidth="24k", duration=60, length=160, codec="amr-wb", dscp=-1,
         sla_max_jitter_ms=40, sla_max_loss_pct=1.0,
         notes="Conversational voice (AMR-WB) over IMS — 5QI=1 GBR, EF"),
    dict(id="video_ims_5qi2",      dnn="ims",      five_qi=2,  protocol="rtp-video", direction="bidir",
         bandwidth="4M", duration=60, length=1200, codec="h264", dscp=-1,
         sla_max_jitter_ms=30, sla_max_loss_pct=1.0,
         notes="Conversational video (H.264) — 5QI=2 GBR, AF41"),
    dict(id="ims_signaling_5qi5",  dnn="ims",      five_qi=5,  protocol="udp", direction="ul",
         bandwidth="100k", duration=30, length=128, codec="", dscp=-1,
         sla_max_latency_ms=100, sla_max_loss_pct=0.1,
         notes="IMS signaling (SIP-ish keep-alive) — 5QI=5 non-GBR"),
    dict(id="video_live_5qi7",     dnn="internet", five_qi=7,  protocol="rtp-video", direction="dl",
         bandwidth="2M", duration=60, length=1200, codec="h264", dscp=-1,
         sla_max_jitter_ms=50, sla_max_loss_pct=2.0,
         notes="Live video / voice buffered — 5QI=7 non-GBR, CS3"),
    dict(id="best_effort_5qi9",    dnn="internet", five_qi=9,  protocol="tcp", direction="bidir",
         bandwidth="50M", duration=30, length=0, codec="", dscp=-1,
         sla_min_kbps=10000,
         notes="Default internet best-effort — 5QI=9 non-GBR, BE"),
    dict(id="gaming_5qi3",         dnn="internet", five_qi=3,  protocol="udp", direction="bidir",
         bandwidth="1M", duration=60, length=200, codec="", dscp=-1,
         sla_max_latency_ms=50, sla_max_jitter_ms=15,
         notes="Real-time gaming — 5QI=3 GBR"),
    dict(id="mcptt_5qi65",         dnn="mcx",      five_qi=65, protocol="rtp-audio", direction="bidir",
         bandwidth="24k", duration=60, length=160, codec="amr-wb", dscp=-1,
         sla_max_latency_ms=75, sla_max_jitter_ms=20, sla_max_loss_pct=0.5,
         notes="Mission-critical push-to-talk — 5QI=65 GBR, EF"),
    dict(id="v2x_5qi79",           dnn="mcx",      five_qi=79, protocol="udp", direction="ul",
         bandwidth="1M", duration=30, length=300, codec="", dscp=-1,
         sla_max_latency_ms=50,
         notes="V2X messages (CAM/DENM) — 5QI=79"),
]


def seed_default_profiles() -> int:
    """Insert the standard 3GPP-inspired profiles if they don't already exist.

    Idempotent. Returns the number of rows actually inserted.
    """
    inserted = 0
    for p in _DEFAULT_PROFILES:
        try:
            if not profile_get(p["id"]):
                profile_add(p)
                inserted += 1
        except Exception as e:
            log.warning("failed to seed profile %s: %s", p.get("id"), e)
    if inserted:
        log.info("seeded %d default traffic profiles", inserted)
    return inserted

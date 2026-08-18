# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""SIM card database — loads UE credentials from SQLite or from
baseline.yaml via src.baseline.sim_entries(). GUI mutations are
applied to an in-memory copy; baseline.yaml is the only on-disk
source of truth."""

import os
import sqlite3
from collections import namedtuple
from binascii import unhexlify

SimCard = namedtuple("SimCard", [
    "imsi", "k", "opc", "op_type", "sqn", "amf", "msisdn", "gnb_name",
    # SUCI identity fields (TS 23.003 §2.2B, TS 33.501 §6.12)
    "supi_type",          # "supi" or "suci"
    "routing_indicator",  # 4 digits, default "0000"
    "protection_scheme",  # 0=null, 1=ECIES-Profile-A, 2=ECIES-Profile-B
    "home_nw_pub_key_id", # Home Network Public Key ID (integer)
    "home_nw_pub_key",    # Home Network Public Key (hex string, 32 bytes for X25519)
], defaults=["", "", "supi", "0000", 0, 0, ""])


def load_sim(imsi, db_path):
    """Load SIM data for a single IMSI from SQLite. Returns SimCard or None."""
    if not db_path or not os.path.exists(db_path):
        return None
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    try:
        row = conn.execute(
            "SELECT u.imsi, a.k, a.op, a.op_type, a.sqn, a.amf "
            "FROM ue u JOIN ue_auth_data a ON u.auth_id = a.id "
            "WHERE u.imsi = ?", (imsi,),
        ).fetchone()
        if not row:
            return None
        return SimCard(
            imsi=row["imsi"], k=unhexlify(row["k"]), opc=unhexlify(row["op"]),
            op_type=row["op_type"], sqn=row["sqn"] or 0,
            amf=unhexlify(row["amf"]) if row["amf"] else b"\x80\x00",
        )
    finally:
        conn.close()


def load_all_sims(db_path):
    """Load all SIM entries from SQLite. Returns list of SimCard."""
    if not db_path or not os.path.exists(db_path):
        return []
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    try:
        rows = conn.execute(
            "SELECT u.imsi, a.k, a.op, a.op_type, a.sqn, a.amf "
            "FROM ue u JOIN ue_auth_data a ON u.auth_id = a.id"
        ).fetchall()
        return [
            SimCard(
                imsi=r["imsi"], k=unhexlify(r["k"]), opc=unhexlify(r["op"]),
                op_type=r["op_type"], sqn=r["sqn"] or 0,
                amf=unhexlify(r["amf"]) if r["amf"] else b"\x80\x00",
            )
            for r in rows
        ]
    finally:
        conn.close()


def load_sims_auto(db_path=None):
    """Build the runtime SIM list.

    Priority:
      1. SQLite (db_path) — production-mode lookup against sa_core's DB
         when the tester is configured to read directly. Returns as-is
         if non-empty.
      2. Otherwise return the in-memory state (lazy-init from
         baseline.yaml + any operator GUI deltas).
    """
    if db_path and os.path.exists(db_path):
        sims = load_all_sims(db_path)
        if sims:
            return sims

    # In-memory state is the runtime source of truth for non-SQLite mode.
    # _sims() lazy-initializes from baseline.yaml; GUI mutations add /
    # remove / clone are applied directly on it.
    result = []
    for e in _sims():
        result.append(SimCard(
            imsi=e["imsi"], k=unhexlify(e["k"]), opc=unhexlify(e["opc"]),
            op_type=e.get("op_type", "OPC"), sqn=e.get("sqn", 0),
            amf=unhexlify(e.get("amf", "8000")),
            msisdn=e.get("msisdn", ""),
            gnb_name=e.get("gnb_name", ""),
            supi_type=e.get("supi_type", "supi"),
            routing_indicator=e.get("routing_indicator", "0000"),
            protection_scheme=e.get("protection_scheme", 0),
            home_nw_pub_key_id=e.get("home_nw_pub_key_id", 0),
            home_nw_pub_key=e.get("home_nw_pub_key", ""),
        ))
    return result


# ============================================================
#  In-memory SIM CRUD (operator GUI mutations)
# ============================================================
#
# baseline.yaml is the only on-disk source of truth for the roster.
# GUI add/clone/update/delete operations mutate this in-memory list;
# they don't persist across tester restarts. That matches the broader
# contract: every test starts from a known-clean state (core's
# reset_to_baseline + tester's baseline.yaml load), and ad-hoc
# operator-added UEs are deliberately session-scoped so they can't
# silently bleed into the next test run.

import threading

_sims_state = None
_sims_lock = threading.Lock()


def _sims():
    """Return the live in-memory list of UE dicts. Lazy-initialized
    from baseline.yaml on first access. Failures (missing yaml, parse
    error, schema violation) propagate — there is no silent fallback."""
    global _sims_state
    if _sims_state is None:
        with _sims_lock:
            if _sims_state is None:
                from src import baseline
                _sims_state = list(baseline.sim_entries())
    return _sims_state


def sim_db_reload():
    """Drop GUI deltas and re-read baseline.yaml. Useful after editing
    the manifest in place on a long-running tester."""
    global _sims_state
    with _sims_lock:
        _sims_state = None
    return _sims()


def sim_db_list():
    """Return all SIM entries as list of dicts (copy — safe to mutate)."""
    return [dict(e) for e in _sims()]


def sim_db_get(imsi):
    """Get a single SIM entry by IMSI. Returns dict or None."""
    for e in _sims():
        if e["imsi"] == imsi:
            return dict(e)
    return None


def sim_db_add(entry):
    """Add a new SIM entry. Raises ValueError if IMSI already exists."""
    entries = _sims()
    if any(e["imsi"] == entry["imsi"] for e in entries):
        raise ValueError(f"IMSI {entry['imsi']} already exists")
    entry.setdefault("msisdn", "")
    entry.setdefault("op_type", "OPC")
    entry.setdefault("sqn", 0)
    entry.setdefault("amf", "8000")
    entry.setdefault("gnb_name", "")
    entry.setdefault("supi_type", "supi")
    entry.setdefault("routing_indicator", "0000")
    entry.setdefault("protection_scheme", 0)
    entry.setdefault("home_nw_pub_key_id", 0)
    entry.setdefault("home_nw_pub_key", "")
    entries.append(entry)
    return entry


def sim_db_update(imsi, updates):
    """Update fields on an existing SIM entry. Returns updated dict."""
    for e in _sims():
        if e["imsi"] == imsi:
            e.update(updates)
            # Don't allow IMSI change via updates (use clone instead)
            e["imsi"] = imsi
            return e
    raise ValueError(f"IMSI {imsi} not found")


def sim_db_delete(imsi):
    """Delete a SIM entry by IMSI. Returns True if found."""
    entries = _sims()
    for i, e in enumerate(entries):
        if e["imsi"] == imsi:
            del entries[i]
            return True
    return False


def sim_db_clone(source_imsi, new_imsi):
    """Clone a SIM entry with a new IMSI. Returns new entry dict."""
    entries = _sims()
    source = None
    for e in entries:
        if e["imsi"] == source_imsi:
            source = e
            break
    if source is None:
        raise ValueError(f"Source IMSI {source_imsi} not found")
    if any(e["imsi"] == new_imsi for e in entries):
        raise ValueError(f"Target IMSI {new_imsi} already exists")
    new_entry = dict(source)
    new_entry["imsi"] = new_imsi
    entries.append(new_entry)
    return new_entry


def _inc_dec_str(b):
    """Increment a numeric ASCII string in place (returned as new string).
    Preserves width — raises ValueError on overflow past 999...9.
    Mirrors db/crud/ue.go::incDecStr in mmt-studio-core-go so the tester
    and the core SMF agree on how IMSIs increment.
    """
    arr = bytearray(b.encode())
    for i in range(len(arr) - 1, -1, -1):
        if arr[i] < ord('9'):
            arr[i] += 1
            return arr.decode()
        arr[i] = ord('0')
    raise ValueError("IMSI increment overflowed width")


def sim_db_clone_range(source_imsi, start_imsi, count):
    """Clone `count` sequential SIM entries from source_imsi, starting
    at start_imsi (and walking +1 each iteration). MSISDN for each new
    entry is the last 10 digits of the new IMSI (matches the seed
    convention used by mmt-studio-core-go's UECloneRange).

    Returns the count of entries actually created. An error
    short-circuits the loop — earlier clones remain in memory.
    """
    if not source_imsi or not start_imsi:
        raise ValueError("source_imsi and start_imsi required")
    if count <= 0:
        raise ValueError("count must be > 0")
    if not start_imsi.isdigit():
        raise ValueError("start_imsi must be numeric")
    if len(source_imsi) != len(start_imsi):
        raise ValueError("source_imsi and start_imsi must have the same width")

    created = 0
    cur = start_imsi
    for _ in range(count):
        new_imsi = cur
        try:
            new_entry = sim_db_clone(source_imsi, new_imsi)
            if len(new_imsi) > 5:
                new_entry["msisdn"] = new_imsi[5:]
                sim_db_update(new_imsi, {"msisdn": new_imsi[5:]})
            created += 1
        except ValueError:
            # IMSI already exists at this slot — skip and continue so
            # re-running a partial clone is idempotent.
            pass
        cur = _inc_dec_str(cur)
    return created


def sim_db_delete_range(start_imsi, count):
    """Delete `count` sequential SIM entries starting at start_imsi.
    Mirrors db/crud/ue.go::UEDeleteRange — IMSI is incremented numerically
    and missing IMSIs in the range are silently skipped (idempotent).

    Returns the count of entries actually removed.
    """
    if not start_imsi:
        raise ValueError("start_imsi required")
    if count <= 0:
        raise ValueError("count must be > 0")
    if not start_imsi.isdigit():
        raise ValueError("start_imsi must be numeric")

    deleted = 0
    cur = start_imsi
    for _ in range(count):
        if sim_db_delete(cur):
            deleted += 1
        cur = _inc_dec_str(cur)
    return deleted


def sim_db_import(new_entries, overwrite=False):
    """Import a list of SIM entries. Returns count of imported entries."""
    entries = _sims()
    existing_imsis = {e["imsi"] for e in entries}
    count = 0
    for ne in new_entries:
        if ne["imsi"] in existing_imsis:
            if overwrite:
                for i, e in enumerate(entries):
                    if e["imsi"] == ne["imsi"]:
                        del entries[i]
                        break
                entries.append(ne)
                count += 1
        else:
            entries.append(ne)
            existing_imsis.add(ne["imsi"])
            count += 1
    return count

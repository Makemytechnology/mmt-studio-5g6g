# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Baseline reader — single source of truth for the tester's roster.

Reads config/baseline.yaml (vendored locally in the tester repo;
historically copied from core/db/seed/baseline.yaml but the two repos
own their copies independently — same content is fine, divergent
content is also fine since each test provisions UEs into core via the
API).

Exposes strongly-typed accessors so tests don't hardcode IMSI/SST/SD/
DNN constants, plus sim_entries() which builds the 128-UE list the
gNB simulator consumes at startup (no pre-baked sim_db.json, no
regenerator script — edits to baseline.yaml take effect on restart).

Typical use:
    from src import baseline

    imsi   = baseline.imsi("embb-bulk", idx=0)        # "001011234560001"
    embb   = baseline.slice_by_name("eMBB")           # Slice(sst=1, sd="000001", ...)
    inet   = baseline.dnn("internet")                 # DNN(pool="10.45.0.0/16", ...)
    k, opc = baseline.k(imsi), baseline.opc(imsi)     # derived (matches core)

Strict at load: any missing required field raises BaselineError, which
beats letting a typo surface as 'None' at test #312 of a nightly run.

Phase 3 note: tests that genuinely need off-roster UEs (e.g., N26
inter-system error cases, ranging sidelink pairs) should NOT extend
the baseline. They live in src/delta.py and add UEs at runtime via
core's admin API; reset-to-baseline cleans them up automatically.
"""

from __future__ import annotations

import hashlib
import logging
import os
import threading
from dataclasses import dataclass

log = logging.getLogger("tester.baseline")


class BaselineError(Exception):
    """Raised when baseline.yaml is missing, malformed, or self-inconsistent.

    Always fail loud at load time — a manifest typo should not become a
    silent None in a test's middle.
    """


# ── On-disk lookup ────────────────────────────────────────────────────
# Tester owns its own copy of baseline.yaml — independent of core. The
# two repos may carry identical content but neither path-reads the
# other; sync is an explicit copy. Rationale: removes the brittle
# sibling-checkout dependency, lets the tester ship self-contained in
# its docker image, and matches the runtime contract (each test
# provisions UEs into core via the API; core's seed only matters for
# standalone-GUI use).
_BASELINE_CANDIDATES = (
    # In-tree vendored copy — the authoritative source for the tester.
    "../config/baseline.yaml",
)


def _locate_baseline_yaml() -> str:
    here = os.path.dirname(os.path.abspath(__file__))
    for rel in _BASELINE_CANDIDATES:
        path = os.path.normpath(os.path.join(here, rel))
        if os.path.exists(path):
            return path
    tried = ", ".join(
        os.path.normpath(os.path.join(here, r)) for r in _BASELINE_CANDIDATES
    )
    raise BaselineError(f"baseline.yaml not found; tried: {tried}")


# ── YAML parsing ──────────────────────────────────────────────────────
# PyYAML is mandatory — baseline.yaml uses flow-style mappings (e.g.
# `- { sst: 1, sd: "000001" }`) that no minimal stdlib parser handles
# correctly. We import at module load so a missing PyYAML fails the
# tester startup with a clear ImportError rather than silently
# misparsing rows into ints and crashing later inside _load_locked.
import yaml  # type: ignore


def _parse_yaml(path: str) -> dict:
    with open(path, "r", encoding="utf-8") as f:
        return yaml.safe_load(f) or {}


# ── Dataclasses ───────────────────────────────────────────────────────
# Frozen + slots-like — read-only after load; tests must not mutate.

@dataclass(frozen=True)
class Bucket:
    name: str
    count: int
    imsis: tuple                  # (str, ...) — full enumerated list
    msisdns: tuple                # (str, ...) — same length as imsis
    slices: tuple                 # (int, ...) — SST values
    dnns: tuple                   # (str, ...)
    default_dnn: str
    ue_ambr_dl_kbps: int
    ue_ambr_ul_kbps: int


@dataclass(frozen=True)
class Slice:
    sst: int
    sd: str                       # hex string, e.g. "000001"
    name: str                     # e.g. "eMBB"
    description: str


@dataclass(frozen=True)
class DNN:
    name: str
    pool: str                     # CIDR
    default_5qi: int
    session_ambr_dl_kbps: int
    session_ambr_ul_kbps: int
    slices: tuple                 # (int, ...) — SST values that allow this DNN


@dataclass(frozen=True)
class IMSSubscriber:
    imsi: str
    msisdn: str
    impi: str
    impu: str
    realm: str
    password: str                 # derived via sha256(imsi||'MMT-IMS-PWD-'||kdf_version)[:8]


@dataclass(frozen=True)
class PLMN:
    mcc: str
    mnc: str


@dataclass(frozen=True)
class GUAMI:
    region_id: int
    set_id: int
    pointer: int


@dataclass(frozen=True)
class TAC:
    tac: int
    mcc: str
    mnc: str


@dataclass(frozen=True)
class GNB:
    id: str
    address: str
    tac: int
    description: str


@dataclass(frozen=True)
class UPF:
    id: str
    address: str                  # PFCP listen IP — N4 control (upf_ip).
                                  # TS 29.244 §6.1: PFCP runs UDP/8805.
    n3_address: str               # GTP-U N3 endpoint advertised to the gNB
                                  # in PDU Session Resource Setup Request
                                  # Transfer IE 139 (UL-NGU-UP-TNLInformation,
                                  # TS 38.413 §9.3.2.2). Must be an IP the
                                  # gNB can reach — distinct from `address`
                                  # whenever SMF and UPF are co-located on
                                  # a loopback for PFCP. No fallback: leaving
                                  # this empty would silently advertise the
                                  # loopback to the gNB and the tunnel
                                  # would route to the gNB's own loopback.
    port: int                     # PFCP port (TS 29.244 §6.1)
    supported_sst: tuple          # tuple[int, ...]
    supported_dnns: tuple         # tuple[str, ...]


# ── Internal loaded state ─────────────────────────────────────────────

_lock = threading.RLock()
_state: dict = {
    "loaded": False,
    "path": None,
    "kdf_version": None,
    "amf": None,
    "initial_sqn": 0,
    "plmn": None,               # PLMN
    "guami": None,              # GUAMI
    "served_tacs": (),          # tuple[TAC, ...]
    "gnbs": (),                 # tuple[GNB, ...]
    "buckets_by_name": {},      # str -> Bucket
    "imsi_to_bucket": {},       # imsi -> bucket name (for fast reverse lookup)
    "imsi_to_msisdn": {},       # imsi -> msisdn
    "slices_by_name": {},       # name -> Slice
    "slices_by_sst": {},        # sst (int) -> Slice
    "dnns_by_name": {},         # name -> DNN
    "ims_subscribers": (),      # tuple[IMSSubscriber, ...]
    "upfs": (),                 # tuple[UPF, ...]
}


def _load_locked() -> None:
    """Load and validate the manifest. Holds _lock."""
    path = _locate_baseline_yaml()
    m = _parse_yaml(path)
    if not isinstance(m, dict):
        raise BaselineError(f"{path}: top-level must be a mapping")

    # ── PLMN ──
    raw_plmn = m.get("plmn", {}) or {}
    if not raw_plmn.get("mcc") or not raw_plmn.get("mnc"):
        raise BaselineError(f"{path}: plmn.mcc / plmn.mnc are required")
    plmn_obj = PLMN(mcc=str(raw_plmn["mcc"]), mnc=str(raw_plmn["mnc"]))

    # ── AMF (GUAMI + served TAIs) ──
    raw_amf = m.get("amf", {}) or {}
    g = raw_amf.get("guami", {}) or {}
    guami_obj = GUAMI(
        region_id=int(g.get("region_id", 1)),
        set_id=int(g.get("set_id", 1)),
        pointer=int(g.get("pointer", 0)),
    )
    served = []
    for raw_tai in raw_amf.get("served_tai", []) or []:
        served.append(TAC(
            tac=int(raw_tai["tac"]),
            mcc=str(raw_tai.get("mcc", plmn_obj.mcc)),
            mnc=str(raw_tai.get("mnc", plmn_obj.mnc)),
        ))

    # ── gNB allow-list ──
    gnb_list = []
    for raw_g in m.get("gnbs", []) or []:
        gnb_list.append(GNB(
            id=str(raw_g["id"]),
            address=str(raw_g.get("address", "")),
            tac=int(raw_g.get("tac", served[0].tac if served else 1)),
            description=str(raw_g.get("description", "")),
        ))

    # ── UPF anchors — minimum the SMF needs in upf_instances for
    # Select() routing (TS 23.501 §6.3.3). Re-pushed by the tester
    # after /api/admin/drop-db-data wipes the table; integrated-CUPS
    # upfloop still owns the in-process PFCP/N3 bridges.
    upf_list = []
    for raw_u in m.get("upfs", []) or []:
        # n3_address is mandatory — silent default would put the loopback
        # into the gNB's UL-NGU-UP-TNLInformation IE and packets would
        # never reach the UPF (see UPF dataclass docstring).
        if "n3_address" not in raw_u:
            raise BaselineError(
                f"{path}: upfs[{raw_u.get('id', '?')}].n3_address is "
                f"required (the gNB-reachable GTP-U IP advertised in "
                f"TS 38.413 §9.3.2.2 PSR Transfer)"
            )
        upf_list.append(UPF(
            id=str(raw_u["id"]),
            address=str(raw_u.get("address", "127.0.0.1")),
            n3_address=str(raw_u["n3_address"]),
            port=int(raw_u.get("port", 8805)),
            supported_sst=tuple(int(x) for x in raw_u.get("supported_sst", []) or []),
            supported_dnns=tuple(str(x) for x in raw_u.get("supported_dnns", []) or []),
        ))

    # ── UE credentials ──
    creds = m.get("ue_credentials", {}) or {}
    kdf_version = creds.get("kdf_version", "v1")
    amf = creds.get("amf", "8000")
    initial_sqn = int(creds.get("initial_sqn", 0))

    # ── Slices ──
    slices_by_name: dict = {}
    slices_by_sst: dict = {}
    for raw in m.get("slices", []) or []:
        s = Slice(
            sst=int(raw["sst"]),
            sd=str(raw.get("sd", "") or ""),
            name=str(raw.get("name", "") or ""),
            description=str(raw.get("description", "") or ""),
        )
        slices_by_name[s.name] = s
        slices_by_sst[s.sst] = s

    # ── DNNs ──
    dnns_by_name: dict = {}
    for raw in m.get("dnns", []) or []:
        d = DNN(
            name=str(raw["name"]),
            pool=str(raw.get("pool", "") or ""),
            default_5qi=int(raw.get("default_5qi", 9)),
            session_ambr_dl_kbps=int(raw.get("session_ambr_dl_kbps", 0)),
            session_ambr_ul_kbps=int(raw.get("session_ambr_ul_kbps", 0)),
            slices=tuple(int(x) for x in raw.get("slices", []) or []),
        )
        dnns_by_name[d.name] = d

    # ── UE buckets — enumerate IMSIs / MSISDNs eagerly ──
    buckets_by_name: dict = {}
    imsi_to_bucket: dict = {}
    imsi_to_msisdn: dict = {}
    for raw in m.get("ue_buckets", []) or []:
        name = str(raw["name"])
        count = int(raw["count"])
        if count <= 0:
            raise BaselineError(f"bucket {name!r}: count must be > 0")
        imsi_start = str(raw["imsi_start"])
        msisdn_start = str(raw["msisdn_start"])
        imsi_int = int(imsi_start)
        msisdn_int = int(msisdn_start)
        msisdn_width = len(msisdn_start)
        imsis = tuple(f"{imsi_int + i:015d}" for i in range(count))
        msisdns = tuple(
            f"{msisdn_int + i:0{msisdn_width}d}" for i in range(count)
        )
        b = Bucket(
            name=name,
            count=count,
            imsis=imsis,
            msisdns=msisdns,
            slices=tuple(int(x) for x in raw.get("slices", []) or []),
            dnns=tuple(str(x) for x in raw.get("dnns", []) or []),
            default_dnn=str(raw.get("default_dnn", "") or ""),
            ue_ambr_dl_kbps=int(raw.get("ue_ambr_dl_kbps", 0)),
            ue_ambr_ul_kbps=int(raw.get("ue_ambr_ul_kbps", 0)),
        )
        buckets_by_name[name] = b
        for imsi, msisdn in zip(imsis, msisdns):
            if imsi in imsi_to_bucket:
                raise BaselineError(
                    f"IMSI {imsi} appears in buckets {imsi_to_bucket[imsi]!r} "
                    f"and {name!r} — manifest has overlapping ranges"
                )
            imsi_to_bucket[imsi] = name
            imsi_to_msisdn[imsi] = msisdn

    # ── IMS subscribers ──
    ims = m.get("ims_subscribers", {}) or {}
    ims_subs = []
    if ims:
        first_imsi = str(ims.get("imsi_start", ""))
        cnt = int(ims.get("count", 0))
        realm = str(ims.get("realm", "ims.local"))
        impi_tpl = str(ims.get("impi_template", "{imsi}@{realm}"))
        impu_tpl = str(ims.get("impu_template", "sip:+1{msisdn}@{realm}"))
        if first_imsi and cnt > 0:
            start = int(first_imsi)
            for i in range(cnt):
                imsi = f"{start + i:015d}"
                msisdn = imsi_to_msisdn.get(imsi, "")
                pwd = hashlib.sha256(
                    (imsi + "MMT-IMS-PWD-" + kdf_version).encode()
                ).hexdigest()[:8]
                ims_subs.append(
                    IMSSubscriber(
                        imsi=imsi,
                        msisdn=msisdn,
                        realm=realm,
                        impi=impi_tpl.format(imsi=imsi, realm=realm,
                                             msisdn=msisdn),
                        impu=impu_tpl.format(imsi=imsi, realm=realm,
                                             msisdn=msisdn),
                        password=pwd,
                    )
                )

    _state.update(
        loaded=True,
        path=path,
        kdf_version=kdf_version,
        amf=amf,
        initial_sqn=initial_sqn,
        plmn=plmn_obj,
        guami=guami_obj,
        served_tacs=tuple(served),
        gnbs=tuple(gnb_list),
        buckets_by_name=buckets_by_name,
        imsi_to_bucket=imsi_to_bucket,
        imsi_to_msisdn=imsi_to_msisdn,
        slices_by_name=slices_by_name,
        slices_by_sst=slices_by_sst,
        dnns_by_name=dnns_by_name,
        ims_subscribers=tuple(ims_subs),
        upfs=tuple(upf_list),
    )
    log.info(
        "Baseline loaded: %s (kdf=%s, %d buckets, %d IMSIs, %d slices, %d DNNs, %d IMS subs)",
        path, kdf_version, len(buckets_by_name),
        len(imsi_to_bucket), len(slices_by_name), len(dnns_by_name),
        len(ims_subs),
    )


def _ensure_loaded() -> None:
    with _lock:
        if not _state["loaded"]:
            _load_locked()


def reload() -> None:
    """Re-read baseline.yaml. Use after editing the manifest in-place
    during a long-running tester process."""
    with _lock:
        _load_locked()


# ── Public accessors ──────────────────────────────────────────────────

def path() -> str:
    """Filesystem path of the loaded baseline.yaml."""
    _ensure_loaded()
    return _state["path"]


def kdf_version() -> str:
    _ensure_loaded()
    return _state["kdf_version"]


def amf() -> str:
    """Authentication Management Field (hex string, default '8000')."""
    _ensure_loaded()
    return _state["amf"]


def initial_sqn() -> int:
    _ensure_loaded()
    return _state["initial_sqn"]


# ── Buckets / IMSIs ──

def bucket(name: str) -> Bucket:
    _ensure_loaded()
    b = _state["buckets_by_name"].get(name)
    if b is None:
        known = sorted(_state["buckets_by_name"].keys())
        raise BaselineError(f"unknown bucket {name!r}; known: {known}")
    return b


def bucket_names() -> tuple:
    _ensure_loaded()
    return tuple(sorted(_state["buckets_by_name"].keys()))


def imsis(bucket_name: str) -> tuple:
    """Full enumerated tuple of IMSIs in the bucket."""
    return bucket(bucket_name).imsis


def imsi(bucket_name: str, idx: int = 0) -> str:
    """Shortcut for the idx-th IMSI in a bucket."""
    b = bucket(bucket_name)
    if idx < 0 or idx >= b.count:
        raise BaselineError(
            f"bucket {bucket_name!r} has {b.count} UEs; idx={idx} out of range"
        )
    return b.imsis[idx]


def msisdn_for(imsi_value: str) -> str:
    _ensure_loaded()
    m = _state["imsi_to_msisdn"].get(imsi_value)
    if m is None:
        raise BaselineError(f"IMSI {imsi_value!r} is not in baseline roster")
    return m


def bucket_of(imsi_value: str) -> Bucket:
    """Reverse lookup: which bucket contains this IMSI."""
    _ensure_loaded()
    name = _state["imsi_to_bucket"].get(imsi_value)
    if name is None:
        raise BaselineError(f"IMSI {imsi_value!r} is not in baseline roster")
    return _state["buckets_by_name"][name]


def is_baseline_imsi(imsi_value: str) -> bool:
    """Cheap predicate — useful in delta.py to refuse double-add."""
    _ensure_loaded()
    return imsi_value in _state["imsi_to_bucket"]


# ── Slices ──

def slice_by_name(name: str) -> Slice:
    _ensure_loaded()
    s = _state["slices_by_name"].get(name)
    if s is None:
        known = sorted(_state["slices_by_name"].keys())
        raise BaselineError(f"unknown slice name {name!r}; known: {known}")
    return s


def slice_by_sst(sst: int) -> Slice:
    _ensure_loaded()
    s = _state["slices_by_sst"].get(int(sst))
    if s is None:
        known = sorted(_state["slices_by_sst"].keys())
        raise BaselineError(f"unknown SST {sst!r}; known: {known}")
    return s


def all_slices() -> tuple:
    _ensure_loaded()
    return tuple(_state["slices_by_name"].values())


# ── DNNs ──

def dnn(name: str) -> DNN:
    _ensure_loaded()
    d = _state["dnns_by_name"].get(name)
    if d is None:
        known = sorted(_state["dnns_by_name"].keys())
        raise BaselineError(f"unknown DNN {name!r}; known: {known}")
    return d


def all_dnns() -> tuple:
    _ensure_loaded()
    return tuple(_state["dnns_by_name"].values())


# ── KDF — reproduces core's DeriveK / DeriveOPc byte-for-byte ──
# core/db/seed/manifest.go: K   = hex(sha256(imsi || "MMT-K-"   || kdf_version)[:16])
#                          OPc = hex(sha256(imsi || "MMT-OPc-" || kdf_version)[:16])

def k(imsi_value: str) -> str:
    """Per-IMSI K (32 hex chars, 16 bytes). Matches the core seeder."""
    _ensure_loaded()
    h = hashlib.sha256(
        (imsi_value + "MMT-K-" + _state["kdf_version"]).encode()
    ).digest()
    return h[:16].hex()


def opc(imsi_value: str) -> str:
    """Per-IMSI OPc (32 hex chars, 16 bytes). Matches the core seeder."""
    _ensure_loaded()
    h = hashlib.sha256(
        (imsi_value + "MMT-OPc-" + _state["kdf_version"]).encode()
    ).digest()
    return h[:16].hex()


# ── IMS subscribers ──

def ims_subscribers() -> tuple:
    _ensure_loaded()
    return _state["ims_subscribers"]


def ims_subscriber(idx: int = 0) -> IMSSubscriber:
    subs = ims_subscribers()
    if not subs:
        raise BaselineError("baseline has no ims_subscribers configured")
    if idx < 0 or idx >= len(subs):
        raise BaselineError(
            f"baseline has {len(subs)} IMS subscriber(s); idx={idx} out of range"
        )
    return subs[idx]


# ── gNB-simulator SIM rows ────────────────────────────────────────────
# Returns the 128-UE roster as a list of dicts in the exact shape the
# gNB simulator expects (matches the historical config/sim_db.json
# layout). Replaces sim_db_gen.py: no derived file, no commit churn,
# no chance of drift between yaml and json — edits to baseline.yaml
# take effect on tester restart.
#
# Off-roster (GUI-added) UEs live in config/sim_db.json as deltas and
# are merged on top by protocol/sim_db.load_sims_auto.

# ── PLMN / GUAMI / TAC / gNB accessors (for provisioner.sync_all) ────

def plmn() -> PLMN:
    _ensure_loaded()
    return _state["plmn"]


def plmn_id() -> str:
    """Stable PLMN identifier used in core API URLs (e.g. '001-01')."""
    p = plmn()
    return f"{p.mcc}-{p.mnc}"


def guami() -> GUAMI:
    _ensure_loaded()
    return _state["guami"]


def served_tacs() -> tuple:
    _ensure_loaded()
    return _state["served_tacs"]


def gnbs() -> tuple:
    _ensure_loaded()
    return _state["gnbs"]


def upfs() -> tuple:
    _ensure_loaded()
    return _state["upfs"]


def sim_entries(gnb_name: str = "tester-gnb-00") -> list:
    """Build the 128-UE SIM list from baseline.yaml.

    Each dict carries the same keys the legacy sim_db.json did so
    downstream code (load_sims_auto, ue_fsm.py, provisioner.py)
    needs zero shape changes.

    msisdn is prefixed with '+' to match the legacy file's E.164 form.
    """
    _ensure_loaded()
    amf_hex = _state["amf"]
    sqn0 = _state["initial_sqn"]
    out = []
    # Preserve yaml-declared order so downstream indexing (sims[127] etc.)
    # matches the human-readable bucket sequence in baseline.yaml.
    for b in _state["buckets_by_name"].values():
        for ue_imsi, ue_msisdn in zip(b.imsis, b.msisdns):
            out.append({
                "imsi": ue_imsi,
                "msisdn": "+" + ue_msisdn,
                "k": k(ue_imsi),
                "opc": opc(ue_imsi),
                "op_type": "OPC",
                "sqn": sqn0,
                "amf": amf_hex,
                "gnb_name": gnb_name,
                "supi_type": "supi",
                "routing_indicator": "0000",
                "protection_scheme": 0,
                "home_nw_pub_key_id": 0,
                "home_nw_pub_key": "",
            })
    return out

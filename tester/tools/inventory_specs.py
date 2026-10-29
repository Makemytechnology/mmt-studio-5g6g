#!/usr/bin/env python3
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Inventory all TestCase classes and report SPEC conformance.

Usage:
    python3 tools/inventory_specs.py                # writes JSON + prints table
    python3 tools/inventory_specs.py --json-only    # JSON only
    python3 tools/inventory_specs.py --output FILE  # custom path

The tool:
  1. Discovers every TestCase subclass via src.testcases.registry.
  2. For each TC, extracts file:line + current legacy fields.
  3. If SPEC is present and valid, records it.
  4. If SPEC is absent, INFERS a TestSpec from legacy fields and
     source-body grep, flagging every field that needs operator review.
  5. Writes data/test_inventory.json (or --output PATH).
  6. Prints a per-domain summary table to stdout.

The inference is best-effort: domain comes from the file path's
sub-directory, spec citation comes from the legacy `category` string,
NFs come from a grep over the source body for /api/<nf>/ prefixes.
Every inferred field is annotated needs_review=true; nothing in the
tool's output should be committed as-is without a human pass.

Run in *transition* mode so legacy TCs without SPEC still import
(they're the whole point of the tool); the env var sticks for this
process only.
"""

from __future__ import annotations

import argparse
import ast
import inspect
import json
import logging
import os
import re
import sys
import time

# Repo root = parent of tools/ — make `import src.*` work when running
# this script from anywhere.
_HERE = os.path.dirname(os.path.abspath(__file__))
_REPO = os.path.dirname(_HERE)
if _REPO not in sys.path:
    sys.path.insert(0, _REPO)

# Stay in transition mode so legacy TCs (no SPEC) don't crash the walk.
os.environ.setdefault("TESTER_SPEC_STRICT", "0")
# Quiet the per-class transitional warnings — the tool itself reports
# them as structured data.
logging.basicConfig(level=logging.ERROR, format="%(message)s")

from src.testcases.base import TestCase  # noqa: E402  late import (env first)
from src.testcases.registry import discover_all  # noqa: E402
from src.testcases.spec import (  # noqa: E402
    TestSpec, TestSpecError, Domain, NF, Slice, Severity, Setup,
)


# ── Inference rules ──────────────────────────────────────────────────

# Filesystem directory → Domain best guess. None means "ambiguous;
# infer per-file from class category / module name instead". Keep
# this conservative: a mis-mapped default is harder to spot than a
# missing one.
DIR_TO_DOMAIN = {
    "core":       None,  # contains registration, pdu_session, idle_mode, slicing …
    "access":     None,  # access, mobility, interworking, multi-access
    "security":   Domain.SECURITY,
    "ims":        Domain.IMS,
    "traffic":    Domain.TRAFFIC,
    "oam":        Domain.OAM,
    "infra":      Domain.INFRA,
    "edge":       None,  # ranging/MEC/PROSE/TSN — varies
    "safety":     Domain.SAFETY,
    "regulatory": None,
    "vertical":   None,  # V2X/PROSE/UAS/PIN/SEAL/IoT — varies
    "vas":        Domain.VAS,
}

# Module-name (basename) → Domain. More specific than directory. The
# tool prefers this over DIR_TO_DOMAIN when both match. Add entries
# as new TC files are seen.
MODULE_TO_DOMAIN = {
    "tc_registration":     Domain.REGISTRATION,
    "tc_deregistration":   Domain.DEREGISTRATION,
    "tc_authentication":   Domain.AUTHENTICATION,
    "tc_pdu_session":      Domain.PDU_SESSION,
    "tc_multi_dnn":        Domain.PDU_SESSION,
    "tc_idle_mode":        Domain.IDLE_MODE,
    "tc_release":          Domain.MOBILITY,
    "tc_handover":         Domain.HANDOVER,
    "tc_ng_setup":         Domain.NG_SETUP,
    "tc_slicing":          Domain.SLICING,
    "tc_slice_catalog":    Domain.SLICING,
    "tc_traffic":          Domain.TRAFFIC,
    "tc_metering":         Domain.QOS,
    "tc_ims":              Domain.IMS,
    "tc_voice":            Domain.VOICE,
    "tc_sms":              Domain.SMS,
    "tc_emergency":        Domain.EMERGENCY,
    "tc_chf_oam":          Domain.CHARGING,
    "tc_trace":            Domain.OAM,
    "tc_trace_correlation": Domain.OAM,
    "tc_otel":             Domain.OAM,
    "tc_nwdaf_exposure":   Domain.NWDAF,
    "tc_pcf_oam":          Domain.OAM,
    "tc_lawful":           Domain.LAWFUL_INTERCEPT,
    "tc_li":               Domain.LAWFUL_INTERCEPT,
    "tc_positioning":      Domain.POSITIONING,
    "tc_npn":              Domain.SECURITY,
    "tc_core_security":    Domain.SECURITY,
    "tc_n26":              Domain.INTERWORKING,
    "tc_wifi_offload":     Domain.INTERWORKING,
    "tc_v2x":              Domain.V2X,
    "tc_uas":              Domain.V2X,    # UAV identification rides V2X spec stack
    "tc_prose":            Domain.PROSE,
    "tc_ranging":          Domain.PROSE,
    "tc_seal":             Domain.MCX,
    "tc_mcx":              Domain.MCX,
    "tc_pin":              Domain.IOT,
    "tc_iot":              Domain.IOT,
    "tc_ntn":              Domain.NTN,
    "tc_ntn_phase2":       Domain.NTN,
    "tc_mec":              Domain.MEC,
    "tc_esim_oam":         Domain.ESIM,
    "tc_musim":            Domain.MOBILITY,
    "tc_musim_oam":        Domain.MOBILITY,
    "tc_ursp_oam":         Domain.OAM,
    "tc_mbs":              Domain.SAFETY,
    "tc_iops":             Domain.SAFETY,
    "tc_racs":             Domain.SAFETY,
    "tc_disaster_roaming": Domain.ROAMING,
    "tc_roaming":          Domain.ROAMING,
    "tc_nsacf":            Domain.SLICING,
    "tc_tsn":              Domain.QOS,
    "tc_vas_oam":          Domain.VAS,
}

# /api/<nf>/ path prefixes → NF. The grep is a 1:1 mapping.
API_TO_NF = {
    "/api/amf/":    NF.AMF,
    "/api/smf/":    NF.SMF,
    "/api/upf/":    NF.UPF,
    "/api/ausf/":   NF.AUSF,
    "/api/udm/":    NF.UDM,
    "/api/udr/":    NF.UDR,
    "/api/pcf/":    NF.PCF,
    "/api/nrf/":    NF.NRF,
    "/api/nssf/":   NF.NSSF,
    "/api/nsacf/":  NF.NSACF,
    "/api/nef/":    NF.NEF,
    "/api/chf/":    NF.CHF,
    "/api/nwdaf/":  NF.NWDAF,
    "/api/lmf/":    NF.LMF,
    "/api/smsf/":   NF.SMSF,
    "/api/n3iwf/":  NF.N3IWF,
    "/api/ims/":    NF.CSCF,
    "/api/cscf/":   NF.CSCF,
    "/api/p-cscf/": NF.PCSCF,
    "/api/i-cscf/": NF.ICSCF,
    "/api/s-cscf/": NF.SCSCF,
}

# Source-text hints → NF set. Triggered when no /api/<nf>/ match.
SOURCE_HINT_TO_NFS = [
    (re.compile(r"\bregister_ue\b|\bue\.register\(\)"),
        (NF.AMF, NF.AUSF, NF.UDM)),
    (re.compile(r"\bestablish_pdu(_session)?\b"),
        (NF.SMF, NF.UPF)),
    (re.compile(r"\bdeestablish_pdu|\brelease_pdu\b|\bpdu_release\b"),
        (NF.SMF, NF.UPF)),
    (re.compile(r"\bn3iwf|wi-?fi", re.I),
        (NF.N3IWF, NF.AMF)),
    (re.compile(r"\bsip\b|\bregister\s+sip\b|\bcscf\b", re.I),
        (NF.CSCF,)),
]

# Source-text hints → Slice / DNN guesses. Best-effort.
SOURCE_SLICE_HINTS = {
    re.compile(r"sst\s*=\s*1\b|\beMBB\b", re.I):  Slice.EMBB,
    re.compile(r"sst\s*=\s*2\b|\bURLLC\b", re.I): Slice.URLLC,
    re.compile(r"sst\s*=\s*3\b|\bm[iI]oT\b"):     Slice.MIOT,
}
SOURCE_DNN_HINTS = {
    re.compile(r'dnn\s*=\s*["\']internet["\']'): "internet",
    re.compile(r'dnn\s*=\s*["\']ims["\']'):      "ims",
    re.compile(r'dnn\s*=\s*["\']mcx["\']'):      "mcx",
    re.compile(r'dnn\s*=\s*["\']iot["\']'):      "iot",
}

# Spec parser — pulls "TS 24.501 §5.5" out of "(TS 24.501 §5.5)" or
# similar. Permissive on whitespace; conservative on the TS/RFC/SGP
# family list to avoid false positives like "(TS-N)" or random parens.
SPEC_FROM_CATEGORY_RE = re.compile(
    r"(?:^|\(|\s)"
    r"(?P<full>"
    r"(?:TS|TR|RFC|SGP\.?\s*\d+)"
    r"\s+[0-9A-Za-z.]+"
    r"(?:\s+§[^\s)]+)?"        # §section — anything up to whitespace or close-paren
    r")"
)


# ── Inference engine ─────────────────────────────────────────────────

def _file_and_line(cls) -> tuple:
    """Best-effort file:line for the class definition."""
    try:
        src_file = inspect.getsourcefile(cls) or "<unknown>"
        try:
            src_line = inspect.getsourcelines(cls)[1]
        except Exception:
            src_line = 0
        return src_file, src_line
    except Exception:
        return "<unknown>", 0


def _module_basename(cls) -> str:
    return cls.__module__.rsplit(".", 1)[-1]


def _module_dir(cls) -> str:
    # src.testcases.access.tc_n26 → 'access'
    parts = cls.__module__.split(".")
    if len(parts) >= 3:
        return parts[2]
    return ""


def _infer_spec_text(category: str) -> str:
    if not category:
        return ""
    m = SPEC_FROM_CATEGORY_RE.search(category)
    return m.group("full").strip() if m else ""


def _infer_domain(cls) -> tuple:
    """Return (Domain or None, reason)."""
    base = _module_basename(cls)
    if base in MODULE_TO_DOMAIN:
        return MODULE_TO_DOMAIN[base], f"from module name {base!r}"
    d = _module_dir(cls)
    mapped = DIR_TO_DOMAIN.get(d)
    if mapped is not None:
        return mapped, f"from directory {d!r}"
    return None, f"no rule matched (module={base!r}, dir={d!r}) — manual review"


def _infer_nfs(cls) -> tuple:
    """Return (tuple of NF enums, reason)."""
    try:
        src = inspect.getsource(cls)
    except (OSError, TypeError):
        try:
            mod = sys.modules.get(cls.__module__)
            src = inspect.getsource(mod) if mod else ""
        except Exception:
            src = ""
    nfs = set()
    matched_via = []
    for path_prefix, nf in API_TO_NF.items():
        if path_prefix in src:
            nfs.add(nf)
            matched_via.append(path_prefix)
    if not nfs:
        for pat, nf_tuple in SOURCE_HINT_TO_NFS:
            if pat.search(src):
                nfs.update(nf_tuple)
                matched_via.append(pat.pattern)
    nfs_sorted = tuple(sorted(nfs, key=lambda n: n.value))
    if not nfs_sorted:
        return (), "no API path or behavior hint matched — manual review"
    return nfs_sorted, "matched: " + ", ".join(matched_via[:4])


def _infer_slice(cls) -> tuple:
    try:
        src = inspect.getsource(cls)
    except Exception:
        return Slice.NONE, "no source available"
    for pat, sl in SOURCE_SLICE_HINTS.items():
        if pat.search(src):
            return sl, f"matched {pat.pattern!r}"
    return Slice.NONE, "no slice hint matched (default NONE)"


def _infer_dnn(cls) -> tuple:
    try:
        src = inspect.getsource(cls)
    except Exception:
        return "", "no source available"
    for pat, dnn in SOURCE_DNN_HINTS.items():
        if pat.search(src):
            return dnn, f"matched {pat.pattern!r}"
    return "", "no dnn hint matched"


def _infer_tc_id(cls) -> tuple:
    legacy = getattr(cls, "tc_id", "") or ""
    if re.match(r"^TC-[A-Z][A-Z0-9]{1,7}-\d{3,4}$", legacy):
        return legacy, "from class.tc_id"
    # Try to synthesise from name: tc_pin_create_network → TC-PIN-???
    name = getattr(cls, "name", "") or ""
    short = re.sub(r"^tc_", "", name).upper()[:4] or "GEN"
    return f"TC-{short}-???", f"synthesised; class had tc_id={legacy!r}, name={name!r}"


def _infer_title(cls) -> tuple:
    desc = getattr(cls, "description", "") or ""
    if desc.strip():
        first = desc.strip().splitlines()[0].strip()
        if first.lower().startswith("tc-"):
            # e.g. "TC-REG-001: Initial registration over 3GPP access."
            after = first.split(":", 1)
            if len(after) == 2:
                return after[1].strip().rstrip("."), "from description first line (after colon)"
        return first.rstrip(".")[:160], "from description first line"
    doc = inspect.getdoc(cls) or ""
    if doc:
        return doc.strip().splitlines()[0].rstrip("."), "from docstring"
    return cls.__name__, "from class name (no description / docstring)"


def _build_inferred_spec(cls) -> dict:
    tc_id, tc_id_reason       = _infer_tc_id(cls)
    title, title_reason       = _infer_title(cls)
    spec_text                 = _infer_spec_text(getattr(cls, "category", "") or "")
    domain, domain_reason     = _infer_domain(cls)
    nfs, nfs_reason           = _infer_nfs(cls)
    slice_v, slice_reason     = _infer_slice(cls)
    dnn_v, dnn_reason         = _infer_dnn(cls)

    needs_review = []
    if "???" in tc_id:                 needs_review.append("tc_id")
    if not spec_text:                  needs_review.append("spec")
    if domain is None:                 needs_review.append("domain")
    if not nfs:                        needs_review.append("nfs")
    # severity / tags always need manual review (no automated inference)
    needs_review += ["severity", "tags"]

    return {
        "tc_id":    tc_id,
        "title":    title,
        "spec":     spec_text or "TS 23.501 §?",
        "domain":   domain.value if domain else None,
        "nfs":      [n.value for n in nfs],
        "slice":    slice_v.value,
        "dnn":      dnn_v,
        "severity": "major",     # default suggestion
        "tags":     [],
        "setup":    "baseline",
        "inferred_from": {
            "tc_id":    tc_id_reason,
            "title":    title_reason,
            "spec":     "from category text" if spec_text else "not found in category",
            "domain":   domain_reason,
            "nfs":      nfs_reason,
            "slice":    slice_reason,
            "dnn":      dnn_reason,
            "severity": "default — needs operator review",
            "tags":     "default empty — needs operator review",
        },
        "needs_review": sorted(set(needs_review)),
    }


def _ok_spec_payload(spec: TestSpec) -> dict:
    return {**spec.to_dict(), "needs_review": []}


# ── Walk + emit ──────────────────────────────────────────────────────

def inventory() -> dict:
    tcs = discover_all()
    entries = []
    n_with = 0
    n_without = 0
    for cls in tcs:
        spec = getattr(cls, "SPEC", None)
        file_, line = _file_and_line(cls)
        rel_file = os.path.relpath(file_, _REPO) if file_ != "<unknown>" else file_
        legacy = {
            "tc_id":       getattr(cls, "tc_id", "") or "",
            "name":        getattr(cls, "name", "") or "",
            "description": (getattr(cls, "description", "") or "").splitlines()[:1],
            "category":    getattr(cls, "category", "") or "",
        }
        if isinstance(spec, TestSpec):
            n_with += 1
            entries.append({
                "status":  "ok",
                "module":  cls.__module__,
                "class":   cls.__name__,
                "file":    rel_file,
                "line":    line,
                "current": legacy,
                "spec":    _ok_spec_payload(spec),
            })
        else:
            n_without += 1
            entries.append({
                "status":  "missing_spec",
                "module":  cls.__module__,
                "class":   cls.__name__,
                "file":    rel_file,
                "line":    line,
                "current": legacy,
                "spec":    _build_inferred_spec(cls),
            })

    return {
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "summary": {
            "total":         len(tcs),
            "with_spec":     n_with,
            "missing_spec":  n_without,
        },
        "entries": sorted(entries, key=lambda e: (e["spec"].get("domain") or "zzz",
                                                  e["module"], e["class"])),
    }


def print_table(report: dict) -> None:
    by_domain: dict = {}
    needs_per_field: dict = {}
    for e in report["entries"]:
        d = e["spec"].get("domain") or "<unmapped>"
        by_domain.setdefault(d, {"ok": 0, "missing": 0})
        if e["status"] == "ok":
            by_domain[d]["ok"] += 1
        else:
            by_domain[d]["missing"] += 1
        for field in e["spec"].get("needs_review", []):
            needs_per_field[field] = needs_per_field.get(field, 0) + 1

    s = report["summary"]
    print(f"\n=== TestCase SPEC Inventory ({report['generated_at']}) ===")
    print(f"  total TCs:        {s['total']}")
    print(f"  with SPEC:        {s['with_spec']}")
    print(f"  missing SPEC:     {s['missing_spec']}")
    print()
    print("  By domain (after inference):")
    print(f"    {'domain':<22} {'OK':>5} {'NEEDS-SPEC':>11}")
    print(f"    {'-'*22:<22} {'-'*5:>5} {'-'*11:>11}")
    for d in sorted(by_domain.keys()):
        c = by_domain[d]
        print(f"    {d:<22} {c['ok']:>5} {c['missing']:>11}")
    print()
    if needs_per_field:
        print("  Fields that need operator review (counts across all missing-SPEC TCs):")
        for k in sorted(needs_per_field.keys()):
            print(f"    {k:<10} {needs_per_field[k]}")
    print()


def main():
    ap = argparse.ArgumentParser(description=__doc__.strip().splitlines()[0])
    ap.add_argument("--output", default=os.path.join(_REPO, "data", "test_inventory.json"),
                    help="JSON output path (default: data/test_inventory.json)")
    ap.add_argument("--json-only", action="store_true",
                    help="Suppress the stdout summary table")
    args = ap.parse_args()

    report = inventory()
    os.makedirs(os.path.dirname(args.output), exist_ok=True)
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump(report, f, indent=2)
    print(f"wrote {args.output} "
          f"({report['summary']['total']} TCs: "
          f"{report['summary']['with_spec']} ok, "
          f"{report['summary']['missing_spec']} need SPEC)",
          file=sys.stderr)

    if not args.json_only:
        print_table(report)


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
#
"""
Extract the NGAP ASN.1 definitions (section 9.4) from TS 38.413 v19.2.0.

For each `-- ASN1START` / `-- ASN1STOP` block in the PDF:
  * strip ETSI / 3GPP page headers and footers
  * identify the module name from the "NGAP-Xxx {" declaration
  * write the cleaned ASN.1 to protocols/ngap/asn.1/NGAP-Xxx.asn
"""

import os
import re
import sys

import fitz  # pymupdf


HERE = os.path.dirname(os.path.abspath(__file__))
# Repo-wide spec layout: every 3GPP TS PDF lives at <repo>/specs/3gpp/.
# Walk up from codecs/asn1-go/scripts/ to the repo root, then in.
REPO_ROOT = os.path.abspath(os.path.join(HERE, "..", "..", ".."))
PDF_PATH = os.path.abspath(os.path.join(
    REPO_ROOT, "specs", "3gpp", "ts_138413v190200p.pdf",
))
OUT_DIR = os.path.abspath(os.path.join(
    HERE, "..", "protocols", "ngap", "asn.1",
))

# PDF page header/footer patterns (one line each).
HEADER_PATTERNS = [
    re.compile(r"^\s*ETSI\s*$"),
    re.compile(r"^\s*ETSI TS \d+ \d+ V[\d.]+ \(\d{4}-\d{2}\)\s*$"),
    re.compile(r"^\s*3GPP TS \d+\.\d+ version [\d.]+ Release \d+\s*$"),
    # section-number footer variations
    re.compile(r"^\s*\d+\s*$"),
]


def scrub_page_chrome(text: str) -> str:
    """Remove repeated ETSI/3GPP page-header cruft. Keep indentation otherwise."""
    out = []
    for line in text.split("\n"):
        if any(p.match(line) for p in HEADER_PATTERNS):
            continue
        out.append(line)
    return "\n".join(out)


def extract_blocks(pdf_path: str):
    """Yield (start_page, end_page, raw_text) for every ASN1START..ASN1STOP block."""
    doc = fitz.open(pdf_path)
    full = ""
    # Collect page offsets so we can report which pages a block spans.
    page_offsets = [0]
    for i in range(doc.page_count):
        full += scrub_page_chrome(doc[i].get_text()) + "\n"
        page_offsets.append(len(full))

    def page_of(off):
        # binary-ish search
        for i in range(len(page_offsets) - 1):
            if page_offsets[i] <= off < page_offsets[i + 1]:
                return i + 1
        return doc.page_count

    start_re = re.compile(r"-- ASN1START")
    stop_re = re.compile(r"-- ASN1STOP")

    pos = 0
    while True:
        m_start = start_re.search(full, pos)
        if not m_start:
            return
        m_stop = stop_re.search(full, m_start.end())
        if not m_stop:
            print(f"[warn] unterminated ASN1START at offset {m_start.start()}",
                  file=sys.stderr)
            return
        raw = full[m_start.end():m_stop.start()]
        yield page_of(m_start.start()), page_of(m_stop.start()), raw
        pos = m_stop.end()


MODULE_DECL_RE = re.compile(
    r"^\s*(NGAP-[A-Za-z0-9\-]+)\s*\{", re.MULTILINE
)


_UNI_TO_ASCII = str.maketrans({
    "\u201c": '"', "\u201d": '"',  # curly double quotes
    "\u2018": "'", "\u2019": "'",  # curly single quotes
    "\u2013": "-",                  # en-dash
    "\u2014": "--",                 # em-dash
    "\u00a0": " ",                  # non-breaking space
    "\u00ad": "",                   # soft hyphen
    "\u2026": "...",                # ellipsis
})


def ascii_normalize(text: str) -> str:
    """Convert typographic Unicode to ASCII that the ASN.1 lexer understands."""
    out = text.translate(_UNI_TO_ASCII)
    # Any remaining non-ASCII: drop and warn (shouldn't happen for NGAP).
    bad = [c for c in out if ord(c) > 0x7F]
    if bad:
        print(f"[warn] dropping {len(bad)} non-ASCII chars: {set(bad)}", file=sys.stderr)
        out = "".join(c for c in out if ord(c) <= 0x7F)
    return out


_HYPHEN_WRAP = re.compile(r"([A-Za-z0-9])-\s*$")
_IDENT_START = re.compile(r"^[A-Za-z]")


def rejoin_hyphen_wraps(lines):
    """
    PyMuPDF breaks long identifiers across PDF line boundaries, leaving a
    hyphen at end-of-line and the rest on the next line:

        ... OF Aerial-UE-
        FlightInformationReportingControlItem

    ASN.1 allows hyphens inside identifiers, so rejoin those without a space.
    Only applied when the next non-blank line starts with a letter (an
    identifier continuation rather than a new declaration).
    """
    out = []
    i = 0
    while i < len(lines):
        cur = lines[i]
        m = _HYPHEN_WRAP.search(cur)
        if m and i + 1 < len(lines):
            # find next non-blank line
            j = i + 1
            while j < len(lines) and lines[j].strip() == "":
                j += 1
            if j < len(lines) and _IDENT_START.match(lines[j].lstrip()):
                # join current line (keeping the trailing '-') with next line's content
                joined = cur.rstrip() + lines[j].lstrip()
                out.append(joined)
                i = j + 1
                continue
        out.append(cur)
        i += 1
    return out


def clean_asn1(text: str) -> str:
    """
    Normalise PyMuPDF's output:
      * translate Unicode typography to ASCII
      * rejoin hyphen-wrapped identifiers split across PDF lines
      * collapse trailing whitespace on each line
      * drop stray blank lines that PyMuPDF inserts for PDF line boxes
      * preserve real blank lines between sections (keep max one)
    """
    text = ascii_normalize(text)
    lines = [ln.rstrip() for ln in text.split("\n")]
    lines = rejoin_hyphen_wraps(lines)
    out = []
    prev_blank = False
    for ln in lines:
        if ln.strip() == "":
            if prev_blank:
                continue
            prev_blank = True
            out.append("")
        else:
            prev_blank = False
            out.append(ln)
    # strip leading/trailing blanks
    while out and out[0] == "":
        out.pop(0)
    while out and out[-1] == "":
        out.pop()
    return "\n".join(out) + "\n"


def main():
    if not os.path.exists(PDF_PATH):
        print(f"PDF not found: {PDF_PATH}", file=sys.stderr)
        sys.exit(1)
    os.makedirs(OUT_DIR, exist_ok=True)

    blocks = list(extract_blocks(PDF_PATH))
    print(f"found {len(blocks)} ASN1START/STOP blocks")

    for start_pg, end_pg, raw in blocks:
        m = MODULE_DECL_RE.search(raw)
        if not m:
            print(f"[warn] block pages {start_pg}-{end_pg}: no module decl, skipping",
                  file=sys.stderr)
            continue
        mod_name = m.group(1)
        wrapped = (
            "-- ASN1START\n"
            + raw
            + "-- ASN1STOP\n"
        )
        cleaned = clean_asn1(wrapped)
        out_path = os.path.join(OUT_DIR, f"{mod_name}.asn")
        with open(out_path, "w", encoding="utf-8") as f:
            f.write(cleaned)
        print(f"  {mod_name:30s}  pages {start_pg:>3}-{end_pg:>3}  {len(cleaned):>7d} bytes  -> {out_path}")


if __name__ == "__main__":
    main()

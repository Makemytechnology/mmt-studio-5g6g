#!/usr/bin/env python3
"""
Add a MakeMyTechnology copyright header to every source file we own.

Skips:
  * Third-party material (3GPP PDFs, ETSI/3GPP-derived ASN.1, pycrate-derived
    fixture JSON, the LICENSE file itself).
  * Tooling-managed files (go.mod, go.sum, .gitignore).
  * Binary files.
  * Files that already have the header (idempotent).

Comment style is picked from the file extension:
  * Go                     -> //   (placed above an existing `// Code generated`
                                   banner if present, else at the very top)
  * Python / YAML / sh     -> #    (placed after the shebang line if present)
  * Markdown               -> <!-- Copyright... -->  (top of file)
"""

import os
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

HOLDER = "MakeMyTechnology"
YEAR = "2026"
TAGLINE = f"Copyright (c) {YEAR} {HOLDER}. All rights reserved."

# Files/directories we do NOT own — skipped entirely.
SKIP_PATH_SUFFIXES = (
    # 3GPP spec PDFs
    ".pdf",
    # ASN.1 files derived from 3GPP specs (ETSI / 3GPP copyright)
    ".asn",
    ".asn1",
    # pycrate-derived fixture outputs (derivative of reference implementation)
    "pycrate_fixtures.json",
)

SKIP_FULL_BASENAMES = {
    "go.mod", "go.sum",
    ".gitignore",
    "LICENSE", "LICENSE.txt", "LICENSE.md",
    "add_copyright.py",  # this script
}

# Subtrees whose contents are *all* third-party (belt and braces).
SKIP_DIR_COMPONENTS = (
    "standards",
    # NGAP asn.1 is derived from the 3GPP spec. Treat the directory as
    # third-party even though extract_ngap_asn1.py is ours.
    "asn.1",
    # common vendor dirs
    "vendor",
    "node_modules",
)

# Per-extension handlers.
STYLES = {
    ".go":   "slash",
    ".py":   "hash",
    ".yaml": "hash",
    ".yml":  "hash",
    ".sh":   "hash",
    ".md":   "html",
}


def should_skip(path: str) -> bool:
    base = os.path.basename(path)
    if base in SKIP_FULL_BASENAMES:
        return True
    if path.endswith(SKIP_PATH_SUFFIXES):
        return True
    # any subdir-component match
    parts = path.replace("\\", "/").split("/")
    for comp in SKIP_DIR_COMPONENTS:
        if comp in parts:
            return True
    return False


def already_has_header(content: str) -> bool:
    # Look only in the first ~20 lines so we don't match stray occurrences.
    head = "\n".join(content.split("\n", 25)[:25])
    return HOLDER in head and "Copyright" in head


def add_slash_header(content: str) -> str:
    """Go: // Copyright ... — placed above existing 'Code generated' banner
    if present, else at the very top."""
    header = f"// {TAGLINE}\n//\n"
    lines = content.split("\n", 1)
    first = lines[0] if lines else ""
    # Avoid inserting before a build-constraint //go:build line or a //go: directive.
    if first.startswith("//go:build") or first.startswith("// +build"):
        # preserve constraint on line 1
        rest = lines[1] if len(lines) > 1 else ""
        return first + "\n\n" + header + rest
    return header + content


def add_hash_header(content: str) -> str:
    """Python / YAML / shell: # Copyright ... — placed after a shebang line if
    present, else at the very top."""
    header = f"# {TAGLINE}\n#\n"
    if content.startswith("#!"):
        i = content.find("\n")
        if i == -1:
            return content + "\n" + header
        return content[: i + 1] + header + content[i + 1:]
    return header + content


def add_html_header(content: str) -> str:
    """Markdown: HTML comment wrapper, top of file."""
    header = f"<!-- {TAGLINE} -->\n\n"
    return header + content


APPLIERS = {
    "slash": add_slash_header,
    "hash":  add_hash_header,
    "html":  add_html_header,
}


def walk():
    for dirpath, dirnames, filenames in os.walk(ROOT):
        # prune skipped dirs in-place so we don't descend
        dirnames[:] = [d for d in dirnames if d not in SKIP_DIR_COMPONENTS and d != ".git"]
        for fn in filenames:
            path = os.path.join(dirpath, fn)
            if should_skip(path):
                continue
            _, ext = os.path.splitext(fn)
            style = STYLES.get(ext.lower())
            if not style:
                continue
            yield path, style


def main():
    updated = 0
    skipped_already = 0
    total = 0
    for path, style in walk():
        total += 1
        try:
            with open(path, "r", encoding="utf-8") as f:
                content = f.read()
        except (UnicodeDecodeError, OSError):
            continue
        if already_has_header(content):
            skipped_already += 1
            continue
        new_content = APPLIERS[style](content)
        with open(path, "w", encoding="utf-8") as f:
            f.write(new_content)
        updated += 1
        rel = os.path.relpath(path, ROOT)
        print(f"  + {rel}")
    print(f"\n{updated} file(s) updated, {skipped_already} already had header, {total} candidates examined")


if __name__ == "__main__":
    main()

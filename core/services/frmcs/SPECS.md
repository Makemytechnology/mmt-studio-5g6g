# FRMCS source specifications

This directory holds the normative source PDFs that the `services/frmcs`
code is implemented against. Per the project-wide rule (feedback memo
`feedback_spec_cite_local_pdf.md`):

1. Clause-level citations (`§x.y.z`, direct quotes) MUST be anchored to
   a PDF present in this directory.
2. TS **document numbers** themselves must be verified before being
   listed as source specs — do not extrapolate from the 22.x/23.x/24.x
   numbering convention.
3. TS **titles** must be taken from the PDF title page, not inferred
   from the number.

## In-tree 3GPP PDFs

| TS     | Version  | PDF file                       | Title (from title page)                            | Used by      |
|--------|----------|--------------------------------|----------------------------------------------------|--------------|
| 22.289 | v19.0.1  | `ts_122289v190001p.pdf`        | Mobile communication system for railways (Stage 1) | frmcs, common|
| 23.289 | v19.7.0  | `ts_123289v190700p.pdf`        | Mission Critical services over 5G System; Stage 2  | frmcs, voice |

Note: **TS 23.289 is the MCX-over-5GS Stage 2 doc, not an
FRMCS-specific document.** FRMCS sits on MCX-over-5GS, so its
architecture is directly relevant.

## MCX-family specs (inherited)

FRMCS rides on MCX, so the MCPTT / MCData / MCVideo / MCX-common specs
are not tracked here — they live under `specs/3gpp/`.
Refer to that directory's README for the authoritative list and
in-tree status. FRMCS source files that cite an MCX clause should
reference those PDFs by path.

**TS 24.289 does not exist.** 3GPP does not assign an FRMCS-specific
Stage 3 document — FRMCS stage-3 behaviour is carried by Change
Requests to the MCX stage-3 specs (24.379 / 24.282 / 24.281). There
is also no TS 24.280 ("MCX common Stage 3"); MCX stage-3 signalling
lives in those per-service docs.

## UIC (industry)

UIC owns the rail-specific architecture and the FRMCS FRS/SRS. The
exact document codes for the PDFs we need (FRS, SRS, on-board
architecture, FIS/FFFIS) should be filled in by a contributor who has
the UIC catalogue in front of them — these codes are intentionally not
listed here to avoid fabrication.

## Adding a spec

1. Drop the PDF into this directory using the 3GPP naming convention
   (`ts_NNNNNNvNNNNNNp.pdf`) or the verbatim UIC document code.
2. Open the title page and record the exact title — do not infer it
   from the number.
3. Grep the PDF (`pdftotext` if needed) to verify any clause text
   before quoting it in source.
4. Move its row from "not yet in-tree" to the "in-tree" table above,
   filling in version, filename, verified title, and consumer package.

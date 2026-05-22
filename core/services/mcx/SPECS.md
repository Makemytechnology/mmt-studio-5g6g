# MCX source specifications

Normative source PDFs for `services/mcx` (MCPTT, MCData, MCVideo, MCX
common). Also referenced from `services/frmcs`, which rides on top of
MCX.

Per the project-wide rule (`feedback_spec_cite_local_pdf.md`):

1. Clause-level citations (`§x.y.z`, direct quotes) MUST be anchored
   to a PDF present in this directory.
2. TS **document numbers** must be verified before being listed — do
   not extrapolate from the 22.x/23.x/24.x numbering convention.
3. TS **titles** must be taken from the PDF title page, not inferred
   from the number.

## In-tree 3GPP PDFs

| TS     | Version  | PDF file                 | Title (from title page) | Used by  |
|--------|----------|--------------------------|-------------------------|----------|
| 23.280 | v19.10.0 | `ts_123280v191000p.pdf`  | Common functional architecture to support mission critical services; Stage 2 | common/ |
| 23.379 | v19.10.0 | `ts_123379v191000p.pdf`  | Functional architecture and information flows to support Mission Critical Push To Talk (MCPTT); Stage 2 | mcptt/ |
| 24.379 | v19.6.0  | `ts_124379v190600p.pdf`  | Mission Critical Push To Talk (MCPTT) call control; Protocol specification (Stage 3) | mcptt/, services/frmcs/voice |
| 24.380 | v19.1.0  | `ts_124380v190100p.pdf`  | Mission Critical Push To Talk (MCPTT) media plane control; Protocol specification | mcptt/ (floor control), services/frmcs/voice |

## 3GPP specs not yet in-tree

Priority specs for current MCX code:

| TS       | Title                          | Used by               |
|----------|--------------------------------|-----------------------|
| 24.282   | MCData — Stage 3               | mcdata/               |
| 23.282   | MCData — Stage 2               | mcdata/               |
| 24.281   | MCVideo — Stage 3              | mcvideo/              |
| 23.281   | MCVideo — Stage 2              | mcvideo/              |
| 33.180   | MCX security / MIKEY-SAKKE     | future kms consumer   |

Note: there is no dedicated **TS 24.280** ("MCX common Stage 3") — MCX
stage-3 signalling lives in the per-service docs (24.379 / 24.282 /
24.281). Verify every TS number against the 3GPP catalogue before
listing it.

## Adding a spec

1. Drop the PDF into this directory using the 3GPP naming convention
   (`ts_NNNNNNvNNNNNNp.pdf`).
2. Open the title page and record the exact title — do not infer it
   from the number.
3. Grep the PDF (`pdftotext` if needed) to verify any clause text
   before quoting it in source.
4. Move its row from "not yet in-tree" into the "in-tree" table
   above, filling in version, filename, verified title, and consumer
   package.

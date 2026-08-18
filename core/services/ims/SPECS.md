# IMS source specifications

Normative source PDFs / RFCs for `services/ims` (CSCF, HSS, conference,
MRFP, media, and the SIP consumer glue that sits on top of `libs/sip`).

Per the project-wide rule (`feedback_spec_cite_local_pdf.md`):

1. Clause-level citations (`§x.y.z`, direct quotes) MUST be anchored
   to a PDF/text present under `specs/`.
2. TS **document numbers** must be verified before being listed — do
   not extrapolate from the 22.x/23.x/24.x numbering convention.
3. TS **titles** must be taken from the PDF title page, not inferred
   from the number.

## In-tree 3GPP PDFs

| TS     | Version | PDF file | Title (from title page) | Used by |
|--------|---------|----------|-------------------------|---------|
| 23.003 | v19.6.0 | `ts_123003v190600p.pdf` | Numbering, addressing and identification | cscf/ |
| 23.228 | v19.6.0 | `ts_123228v190600p.pdf` | IP Multimedia Subsystem (IMS); Stage 2   | cscf/, hss |
| 24.147 | v19.0.0 | `ts_124147v190000p.pdf` | Conferencing using the IP Multimedia (IM) Core Network (CN) subsystem; Stage 3 | conference/ |
| 24.229 | v19.6.0 | `ts_124229v190600p.pdf` | IP multimedia call control protocol based on Session Initiation Protocol (SIP) and Session Description Protocol (SDP); Stage 3 | cscf/ |
| 33.203 | v19.1.0 | `ts_133203v190100p.pdf` | 3G security; Access security for IP-based services | cscf/ (IMS-AKA) |
| 24.279 | v19.0.0 | `ts_124279v190000p.pdf` | Combining Circuit Switched (CS) and IP Multimedia Subsystem (IMS) services; Stage 3 | future cs-ims interworking |

## In-tree IETF RFCs

| RFC  | Title                                                | File                        | Used by  |
|------|------------------------------------------------------|-----------------------------|----------|
| 3261 | SIP: Session Initiation Protocol                     | `specs/ietf/rfc3261.txt`    | libs/sip, cscf/ |
| 3310 | HTTP Digest Authentication Using AKA (AKAv1-MD5)     | `specs/ietf/rfc3310.txt`    | cscf/ (IMS-AKA challenge encoder) |
| 3550 | RTP: A Transport Protocol for Real-Time Applications | `specs/ietf/rfc3550.txt`    | media/ |
| 4169 | HTTP Digest Authentication Using AKAv2               | `specs/ietf/rfc4169.txt`    | cscf/ (IMS-AKA challenge encoder, SHA-256 variant) |
| 4566 | SDP: Session Description Protocol                    | `specs/ietf/rfc4566.txt`    | libs/sdp (planned), cscf/ |

## 3GPP / IETF specs not yet in-tree

Priority specs for current IMS code:

| Doc      | Title                                              | Used by          |
|----------|----------------------------------------------------|------------------|
| TS 29.228 | IP Multimedia (IM) Subsystem Cx and Dx interfaces | cscf/ (HSS)      |
| RFC 3262  | Reliability of Provisional Responses (100rel)     | libs/sip         |
| RFC 3263  | SIP: Locating SIP Servers                         | libs/sip         |
| RFC 3264  | An Offer/Answer Model with SDP                    | libs/sdp         |
| RFC 3265  | SIP-Specific Event Notification                   | libs/sip (future)|

SIP / SDP protocol-level RFCs ideally belong under `libs/sip/` and
`libs/sdp/` (the latter doesn't exist yet); RFC 3261 / 3550 / 4566
are listed here while the library-side standards directories don't
yet exist.

## Adding a spec

1. Drop the PDF into `specs/3gpp/` using the 3GPP naming convention
   (`ts_NNNNNNvNNNNNNp.pdf`), or a plaintext RFC into `specs/ietf/`
   (`rfcNNNN.txt`).
2. Open the title page (or `head` of the RFC) and record the exact
   title — do not infer it from the number.
3. Grep the file (`pdftotext -layout` for PDFs) to verify any clause
   text before quoting it in source.
4. Move its row from "not yet in-tree" into the "in-tree" table
   above, filling in version, filename, verified title, and consumer
   package.

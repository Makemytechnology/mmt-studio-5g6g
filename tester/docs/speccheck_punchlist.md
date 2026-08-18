# Speccheck punch list

Baseline (current): **259 citations** scanned across `src/`. **189 VERIFIED, 10 MISSING, 60 UNLOADED across 19 docs.**

Initial baseline was 184/15/60 — five obvious typos (3 SCTP `§5.2`/`§7.1`, 2 QoS-rule `§9.11.4.13.4`) corrected in the same commit that introduced speccheck.

Run locally with:

```sh
python3 -m pytest tests/speccheck -v
SPECCHECK_LOOSE=1 python3 -m pytest tests/speccheck -v   # tolerate gaps in flight
```

## MISSING (10 outstanding, 5 fixed in baseline commit)

These are real bugs (typos, hallucinated subsections, or section drift across spec
revisions). Each must be either re-targeted to an existing clause or the source
fact must be re-derived from the spec.

### Outstanding (10)

| # | Citation | Site | Diagnosis | Suggested fix | In scope for current audit? |
|---|---|---|---|---|---|
| 1 | `TS 23.501 §5.4.10.3` | `src/testcases/vertical/tc_ntn.py:109` | §5.4.10 exists ("Support for identification and restriction of using NR satellite access"), no `.3` subsection | Re-target to `§5.4.10` or check whether content moved to `§5.4.10a/b` | NTN, out of scope |
| 2 | `TS 23.501 §5.4.10.2` | `src/testcases/vertical/tc_ntn.py:272` | same — no `.2` subsection | same | NTN, out of scope |
| 3 | `TS 23.501 §5.4.10.2` | `src/testcases/vertical/tc_ntn.py:304` | same | same | NTN, out of scope |
| 4 | `TS 23.501 §5.4.10.3` | `src/testcases/vertical/tc_ntn.py:340` | same | same | NTN, out of scope |
| 5 | `TS 23.501 §5.4.10.4` | `src/testcases/vertical/tc_ntn.py:370` | same — no `.4` subsection | same | NTN, out of scope |
| 6 | `TS 23.228 §4.7.4` | `src/testcases/ims/tc_ims.py:766` | §4.7 exists ("Multimedia Resource Function"), `§4.7a` exists, but no `§4.7.4` | Re-target to `§4.7` or `§4.7a` per content | IMS, out of scope |
| 7 | `TS 23.228 §4.7.4` | `src/testcases/ims/tc_ims.py:1351` | same | same | IMS, out of scope |
| 8 | `TS 38.413 §8.1.3` | `src/statemachine/gnb_fsm.py:1099` | §8.1 exists ("List of NGAP Elementary Procedures"); §8.1.3 not in TOC at this depth — likely a procedure index (e.g. INITIAL CONTEXT SETUP is §9.2.2.1, not §8.1.3) | Likely should be `§9.2.2.x`; verify against intended procedure | **Yes — gNB FSM** |
| 9 | `TS 33.501 §6.1.3.4` | `src/statemachine/ue_fsm.py:228` | §6.1.3 exists with .1 (EAP-AKA'), .2 (5G AKA), .3 (Sync/MAC failure); no `.4` | Re-target to `.3` (sync/MAC failure path) if that's the intent | **Yes — UE FSM** |
| 10 | `TS 38.413 §8.1.3.2` | `src/protocol/ngap.py:307` | same as #8 | verify against intended procedure | **Yes — NGAP protocol** |

### Fixed in baseline commit (5)

| Citation | Site | Fix |
|---|---|---|
| `TS 38.412 §5.2` → `§7` | `src/protocol/sctp.py:32` | TS 38.412 §5 is flat ("Data link layer"); PPID is in §7 |
| `TS 38.412 §5.2` → `§7` | `src/protocol/sctp.py:395` | same |
| `TS 38.412 §7.1` → `§7` | `src/control/sctp/async_sctp.py:170` | TS 38.412 §7 is flat ("Transport layer"); no `.1` |
| `TS 24.501 §9.11.4.13.4` → `§9.11.4.13` | `src/statemachine/ue_fsm.py:700` | `.4` is a figure number, not a subsection |
| `TS 24.501 §9.11.4.13.4` → `§9.11.4.13` | `src/protocol/gtpu.py:41` | same |

## UNLOADED (60 citations across 19 docs)

PDFs not present in `specs/common/`. Citation count per doc, sorted:

| Doc | Citations | Notes |
|---|---|---|
| `TS 24.147` | 9 | IMS conferencing |
| `TS 38.821` | 6 | NTN study |
| `TS 38.415` | 6 | NR; NG-RAN PDU session UP protocol |
| `TS 24.301` | 5 | EPS NAS — interworking citations |
| `TS 38.305` | 5 | NR positioning |
| `TS 23.401` | 4 | EPS architecture |
| `RFC 3515` | 3 | SIP REFER |
| `RFC 3550` | 3 | RTP |
| `TS 38.211` | 3 | NR PHY |
| `TS 23.271` | 3 | LCS |
| `TS 22.369` | 2 | A-IoT requirements |
| `TS 23.682` | 2 | EPS service capability |
| `TS 29.572` | 2 | LMF SBI |
| `TS 38.213` | 2 | NR PHY procedures |
| `RFC 3310` | 1 | HTTP Digest AKA |
| `TS 23.273` | 1 | 5G LCS |
| `TS 24.610` | 1 | IMS communication hold |
| `TS 36.321` | 1 | LTE MAC |
| `TS 38.455` | 1 | NRPPa |

**Triage policy:** for each, decide whether to (a) load the PDF and add a `DOC_MAP`
entry, or (b) drop the citation if the fact is already covered by a loaded doc.
The PHY / LCS / IMS-conferencing references are likely (a); the EPS-interworking
ones (TS 23.401, 24.301, 23.682) are likely (b) since 5GS specs supersede them
for what we're testing.

## Process

- Strict-by-default. **Do not merge** with `SPECCHECK_LOOSE=1` set.
- New citations to a doc not in `DOC_MAP` must either add the PDF + map entry,
  or re-target to a loaded doc.
- When fixing a MISSING entry, verify the new clause exists by running speccheck
  before committing — same loop the Go side uses.

# TC-NGS-010: NGAP APER Encode/Decode Validation

## Overview
This test validates the NGAP APER encoding and decoding of the NGSetupRequest message. It builds an NGSetupRequest from configuration, encodes it to APER hex, decodes it back, and verifies all mandatory IEs are preserved. This is a protocol-level test that catches encoding bugs before they reach the network.

## 3GPP Background
NGAP messages are encoded using ASN.1 APER (Aligned Packed Encoding Rules, ITU-T X.691). APER is a compact binary encoding that preserves bit-level alignment for efficient parsing. The NGSetupRequest is encoded as an initiatingMessage within the NGAP-PDU CHOICE type.

The encoding process: ASN.1 schema -> structured data -> APER binary -> hex string. Key encoding rules for the IEs:
- **procedureCode** (INTEGER 0..255): 1 byte, value 21 for NG Setup
- **GlobalRANNodeID** (CHOICE): gNB-ID CHOICE with BIT STRING 22..32 bits
- **RANNodeName** (PrintableString 1..150): length-determinant + character bytes
- **SupportedTAList** (SEQUENCE OF): count + TAI entries
- **DefaultPagingDRX** (ENUMERATED): 2-bit value (v32=0, v64=1, v128=2, v256=3)

Round-trip validation (encode -> decode -> verify) catches: incorrect bit packing, wrong length fields, missing optional IEs, incorrect CHOICE selections, and ASN.1 constraint violations.

**Network functions involved:** gNB (encoder/decoder)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 9.4.1 | NGSetupRequest message structure |
| TS 38.413 | 9.3 | IE definitions |
| ITU-T X.691 | * | APER encoding rules |
| TS 38.413 | Annex | ASN.1 schema |

## Problem Statement
- What if the APER encoder produces invalid bit alignment?
- What if optional IEs are missing from the encoded output?
- What if the decoded message loses information (lossy encoding)?
- What if the procedureCode is wrong, causing the AMF to misinterpret the message?

## Test Procedure (Step-by-Step)
1. Build NGSetupRequest from gNB configuration (gNB ID, name, PLMN, TAC, slices).
2. APER-encode the message to hex string.
3. Verify encoded hex is non-empty.
4. Decode the hex string back to structured format.
5. Verify "NGSetupRequest" appears in the decoded output.
6. Verify procedureCode=21.
7. Verify all mandatory IEs present.

## Expected Behavior
- Encoded hex is a valid APER binary representation.
- Decoded message contains "NGSetupRequest" identifier.
- All mandatory IEs (GlobalRANNodeID, RANNodeName, SupportedTAList, DefaultPagingDRX) present.
- procedureCode=21 in the decoded structure.

## Pass/Fail Criteria
- **Pass:** Non-empty hex; decoded contains NGSetupRequest; all IEs preserved.
- **Fail:** Empty hex; decode failure; missing IEs; wrong procedureCode.

## Key Concepts for Training

### ASN.1 APER Encoding
APER (Aligned Packed Encoding Rules) is used throughout 3GPP for protocol encoding (NGAP, NAS, RRC). Key concepts: (1) Values are packed into minimum bits (an ENUMERATED with 4 values uses 2 bits), (2) Alignment is maintained on byte boundaries for some types, (3) Length determinants precede variable-length fields, (4) SEQUENCE types encode presence bits for optional fields.

### Round-Trip Testing
Encode-decode round-trip testing verifies that the encoder and decoder are consistent. If A = encode(data) and B = decode(A), then B should equal the original data. Any difference indicates a bug in the encoder, decoder, or both. This is more thorough than just testing encoding in isolation.

### NGAP Message Structure
Every NGAP message is wrapped in an NGAP-PDU, which is a CHOICE of: initiatingMessage, successfulOutcome, or unsuccessfulOutcome. Each contains: procedureCode (identifies the procedure), criticality (reject/ignore/notify), and value (the actual message content as a SEQUENCE of IEs).

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Empty encoded output | Encoder returned empty hex | Check ASN.1 schema loading and data binding |
| Decode failure | Cannot parse encoded hex | APER alignment or length error |
| Missing IE | Decoded output lacks SupportedTAList | Check encoder IE ordering |
| Wrong procedureCode | Decoded shows procedureCode != 21 | Fix procedureCode assignment |
| Bit alignment error | AMF rejects with decode error | Check APER alignment rules |

## References
- 3GPP TS 38.413 V17.x -- Section 9.4.1 (NGSetupRequest), Annex (ASN.1)
- ITU-T X.691 -- APER encoding rules
- Related: TC-NGS-001 (live NG Setup), TC-NGS-003 (PLMN encoding), TC-NGS-004 (custom PLMN)

## Quiz Questions
1. What is the procedureCode value for NG Setup in NGAP, and where is it encoded in the APER binary?
   *Answer: procedureCode=21. It is encoded in the initiatingMessage wrapper as a constrained INTEGER (0..255), occupying 1 byte in the APER encoding, near the start of the NGAP-PDU.*

2. Why is round-trip (encode-decode) testing more valuable than testing encoding alone?
   *Answer: Encoding-only testing can't verify the output is correct -- it just produces bytes. Round-trip testing verifies that the encoded bytes accurately represent the input data by decoding them back and comparing. It catches both encoding bugs (wrong bytes) and decoding bugs (wrong interpretation).*

3. What is the difference between APER and UPER encoding, and why does NGAP use APER?
   *Answer: APER (Aligned PER) aligns values on byte boundaries, making it easier to parse but slightly larger. UPER (Unaligned PER) packs bits without alignment, producing smaller output but harder parsing. NGAP uses APER because the control plane prioritizes parsing speed and debugging ease over minimal message size. NR RRC uses UPER because radio interface bandwidth is more constrained.*

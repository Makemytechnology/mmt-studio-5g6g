# TC-NGS-006: NG Setup with RANNodeName IE

## Overview
This test validates that the RANNodeName IE is correctly included in the NGSetupRequest. The RANNodeName provides a human-readable identifier for the gNB, used in AMF logs, network management, and troubleshooting. This test verifies that the IE is correctly encoded and accepted by the AMF.

## 3GPP Background
The RANNodeName (IE id=82) is an optional IE in the NGSetupRequest, defined as a PrintableString with length 1..150 characters (TS 38.413 Section 9.3.1.6). While optional per the spec, most implementations include it as it greatly aids operational visibility.

The RANNodeName allows operators to identify gNBs by meaningful names (e.g., "gNB-Site-Downtown-01") rather than just by GlobalRANNodeID (numeric). The AMF logs the RANNodeName for monitoring, troubleshooting, and configuration management.

PrintableString is a subset of ASCII: A-Z, a-z, 0-9, space, and a few special characters ('()+,-./:=?). No unicode or extended characters.

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 9.3.1.6 | RANNodeName (PrintableString 1..150) |
| TS 38.413 | 8.7.1 | NG Setup procedure |

## Problem Statement
- What if the RANNodeName contains invalid characters (outside PrintableString)?
- What if the name exceeds 150 characters?
- What if the name is empty (0 characters)?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration (uses gNB name as RANNodeName).
2. Connect SCTP, send NG Setup Request with RANNodeName IE.
3. Verify gNB reaches READY state.
4. Teardown: remove gNB.

## Expected Behavior
- RANNodeName IE (id=82) included in NGSetupRequest.
- Name encoded as APER PrintableString.
- AMF accepts the name and reaches READY.

## Pass/Fail Criteria
- **Pass:** gNB READY with RANNodeName accepted.
- **Fail:** NG Setup fails; encoding error.

## Key Concepts for Training

### NGAP Information Elements (IEs)
NGAP messages are composed of Information Elements (IEs), each identified by a numeric ID. Each IE has: ID (integer), criticality (reject/ignore/notify), and value (typed ASN.1 structure). The RANNodeName uses ID=82. IEs are encoded in APER format within the NGAP PDU structure.

### Operational Naming Conventions
In production networks, gNB names follow conventions like: "{Operator}-{Site}-{Sector}-{ID}" (e.g., "ATT-Chicago-Loop-N-001"). Good naming helps operators quickly identify which physical site a gNB represents during alarm correlation, capacity planning, and troubleshooting.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Invalid characters | APER encoding error | Use only PrintableString characters |
| Name too long | Encoding exceeds 150 chars | Truncate to 150 characters |
| Empty name | Encoding error (min length 1) | Ensure non-empty name |

## References
- 3GPP TS 38.413 V17.x -- Section 9.3.1.6 (RANNodeName)
- Related: TC-NGS-001 (basic NG Setup), TC-NGS-010 (encoding validation)

## Quiz Questions
1. What is the maximum length of the RANNodeName IE, and what character set does it use?
   *Answer: 150 characters maximum, using PrintableString (subset of ASCII: A-Z, a-z, 0-9, space, and special chars '()+,-./:=?).*

2. Is the RANNodeName IE mandatory or optional in the NGSetupRequest?
   *Answer: Optional per TS 38.413. However, most implementations include it for operational visibility.*

3. Why might a gNB with the name "gNB_Downtown#1" cause an encoding error?
   *Answer: The '#' character is not part of the PrintableString character set. The underscore '_' is also not in PrintableString. Valid alternatives would be "gNB-Downtown-1" or "gNB Downtown 1".*

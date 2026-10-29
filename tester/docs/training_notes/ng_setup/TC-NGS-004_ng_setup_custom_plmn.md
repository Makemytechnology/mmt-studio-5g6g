# TC-NGS-004: NG Setup with Custom PLMN (MCC=310, MNC=260)

## Overview
This test validates NG Setup with a custom PLMN identity (MCC=310, MNC=260 -- a US operator PLMN). It specifically tests 3-digit MNC encoding, which differs from 2-digit MNC encoding in the BCD format. This ensures the NGAP encoder correctly handles both MNC lengths.

## 3GPP Background
When the MNC has 3 digits, no filler nibble is used. For MCC=310, MNC=260: byte 1 = (MCC2|MCC1) = 0x13, byte 2 = (MNC3|MCC3) = 0x00, byte 3 = (MNC2|MNC1) = 0x62. This differs from 2-digit MNC encoding where byte 2 has a filler F. The AMF must have PLMN 310/260 in its served list to accept this NG Setup.

This test also uses a custom TAC (0x0100 = 256), verifying that non-default TAC values are correctly encoded as 3-octet OCTET STRINGs.

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 9.3.3.5 | PLMNIdentity encoding |
| TS 23.003 | 12.1 | PLMN identity (3-digit MNC) |
| TS 38.413 | 9.3.3.10 | TAC encoding (3 octets) |
| TS 38.413 | 8.7.1 | NG Setup procedure |

## Problem Statement
- What if the encoder uses 2-digit MNC format for a 3-digit MNC?
- What if the AMF does not have PLMN 310/260 configured?
- What if the custom TAC is not configured in the AMF's served TA list?

## Test Procedure (Step-by-Step)
1. Create gNB with custom PLMN (MCC=310, MNC=260, TAC=0100).
2. Connect SCTP, send NG Setup Request with custom PLMN.
3. Verify gNB reaches READY state.
4. Teardown: remove gNB.

## Expected Behavior
- PLMNIdentity encoded with 3-digit MNC (no filler F).
- AMF accepts PLMN 310/260 (if configured).
- Custom TAC 0x0100 accepted.
- gNB reaches READY state.

## Pass/Fail Criteria
- **Pass:** gNB READY with custom PLMN accepted.
- **Fail:** NGSetupFailure; encoding error; PLMN rejected.

## Key Concepts for Training

### 3-Digit MNC Encoding
For 3-digit MNC (e.g., 260), all 6 nibbles are used: MCC=310, MNC=260. Byte 1 = (1|3) = 0x13, byte 2 = (0|0) = 0x00, byte 3 = (6|2) = 0x62. No filler F is needed. The decoder determines MNC length by checking if byte 2's high nibble is F (2-digit) or not (3-digit).

### TAC Encoding
The Tracking Area Code is encoded as a 3-octet OCTET STRING (TS 38.413 Section 9.3.3.10). For TAC=0x0100 (decimal 256), the encoding is simply 0x00 0x01 0x00. The TAC, combined with PLMN, forms a TAI (Tracking Area Identity) used for paging, mobility management, and area-based access control.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Wrong MNC encoding | AMF sees wrong PLMN | Verify 3-digit MNC BCD (no filler F) |
| PLMN not configured | NGSetupFailure | Add 310/260 to AMF served PLMNs |
| TAC not configured | NGSetupFailure | Add TAC 0100 to AMF served TAs |
| Byte order error | PLMN garbled | Check endianness of BCD encoding |

## References
- 3GPP TS 38.413 V17.x -- Section 9.3.3.5 (PLMNIdentity)
- 3GPP TS 23.003 V17.x -- Section 12.1 (PLMN identity)
- Related: TC-NGS-003 (default PLMN), TC-NGS-005 (custom TAC)

## Quiz Questions
1. How does the BCD encoding differ between a 2-digit MNC (01) and a 3-digit MNC (260)?
   *Answer: For 2-digit MNC, byte 2's high nibble is F (filler): e.g., 0xF1. For 3-digit MNC, byte 2's high nibble is the 3rd MNC digit: e.g., 0x00 (digit '0'). The decoder checks this nibble to determine MNC length.*

2. What is the 3-octet BCD encoding for PLMN MCC=310, MNC=260?
   *Answer: 0x13 0x00 0x62. Byte 1: (MCC2=1 | MCC1=3). Byte 2: (MNC3=0 | MCC3=0). Byte 3: (MNC2=6 | MNC1=2).*

3. Why must the AMF's configuration include the exact PLMN and TAC that the gNB sends in NG Setup?
   *Answer: The AMF only serves gNBs within its configured service area (PLMNs and TAs). A gNB with an unrecognized PLMN or TAC could be from a different network or misconfigured. The AMF rejects it with NGSetupFailure to maintain network integrity.*

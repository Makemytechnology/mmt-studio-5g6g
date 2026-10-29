# TC-NGS-003: NG Setup with Default PLMN Identity

## Overview
This test validates NG Setup using the default PLMN identity (MCC=001, MNC=01) from the gNB configuration profile. It verifies that the PLMNIdentity IE is correctly encoded in 3-octet BCD format within the SupportedTAList and that the AMF accepts it.

## 3GPP Background
The PLMNIdentity is a critical IE in the NGSetupRequest. It is encoded as a 3-octet OCTET STRING using BCD (Binary Coded Decimal) with digit swapping per 3GPP conventions (TS 38.413 Section 9.3.3.5). For MCC=001, MNC=01 (a 2-digit MNC), the encoding is: octet 1 = 0x00 (MCC digits 1,2), octet 2 = 0xF1 (MCC digit 3 + filler F), octet 3 = 0x10 (MNC digits 1,2). The PLMNIdentity is carried inside the SupportedTAList -> BroadcastPLMNList -> BroadcastPLMNItem.

The AMF checks the received PLMNIdentity against its list of served PLMNs. If the PLMN is not in the AMF's served list, the AMF sends NGSetupFailure.

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 9.3.3.5 | PLMNIdentity encoding (3 octets BCD) |
| TS 38.413 | 8.7.1 | NG Setup procedure |
| TS 38.413 | 9.3.3.10 | TAC encoding |
| TS 23.003 | 12.1 | PLMN identity structure |

## Problem Statement
- What if the BCD encoding of MCC/MNC is incorrect (wrong digit swapping)?
- What if the filler 'F' nibble is missing for 2-digit MNCs?
- What if the AMF's served PLMN list does not include 001/01?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration (uses default PLMN from profile).
2. Connect SCTP, send NG Setup Request with default PLMN.
3. Verify gNB reaches READY state.
4. Teardown: remove gNB.

## Expected Behavior
- PLMNIdentity encoded correctly as 3 octets BCD.
- AMF accepts the default PLMN.
- gNB reaches READY state.

## Pass/Fail Criteria
- **Pass:** gNB reaches READY state with default PLMN accepted.
- **Fail:** NGSetupFailure due to unknown PLMN; encoding error.

## Key Concepts for Training

### PLMN BCD Encoding
3GPP uses a specific BCD encoding for PLMN: MCC is 3 digits, MNC is 2 or 3 digits. For a 2-digit MNC, a filler nibble 'F' is inserted. The encoding swaps digit pairs: for MCC=001, MNC=01: byte 1 = (MCC2|MCC1) = 0x00, byte 2 = (MNC3_or_F|MCC3) = 0xF1, byte 3 = (MNC2|MNC1) = 0x10. This encoding is inherited from GSM and used throughout 3GPP.

### Test vs. Production PLMNs
MCC=001, MNC=01 is a test PLMN (reserved for testing). Production PLMNs use real country codes (e.g., MCC=310 for US). Test PLMNs are useful for lab environments but must be replaced for production deployment.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Wrong BCD encoding | AMF can't decode PLMN | Verify byte-swapped BCD format |
| Missing filler F | MNC decoded incorrectly | Insert F nibble for 2-digit MNC |
| PLMN not in AMF | NGSetupFailure | Add test PLMN to AMF config |

## References
- 3GPP TS 38.413 V17.x -- Section 9.3.3.5 (PLMNIdentity)
- 3GPP TS 23.003 V17.x -- Section 12.1 (PLMN identity)
- Related: TC-NGS-004 (custom PLMN), TC-NGS-001 (basic NG Setup)

## Quiz Questions
1. How is PLMN MCC=001, MNC=01 encoded in 3-octet BCD format?
   *Answer: 0x00 0xF1 0x10. Byte 1: MCC digits 2,1 (0,0). Byte 2: filler F and MCC digit 3 (F,1). Byte 3: MNC digits 2,1 (1,0).*

2. What is the purpose of the filler 'F' nibble in 2-digit MNC encoding?
   *Answer: The 3-octet encoding always uses 6 nibbles. With MCC (3 digits) + MNC (2 digits) = 5 digits, one nibble is unused. The filler F in byte 2's high nibble indicates a 2-digit MNC and distinguishes it from a 3-digit MNC.*

3. Why would an AMF reject a PLMN it does not recognize?
   *Answer: The AMF only serves specific PLMNs configured by the operator. A gNB with an unknown PLMN cannot be part of the AMF's serving area. The AMF sends NGSetupFailure to prevent unauthorized or misconfigured gNBs from connecting.*

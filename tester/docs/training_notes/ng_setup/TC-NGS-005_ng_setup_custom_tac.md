# TC-NGS-005: NG Setup with Custom Tracking Area Code

## Overview
This test validates NG Setup with a custom TAC (Tracking Area Code) value of 0x00FF. It verifies that the TAC IE is correctly encoded as a 3-octet OCTET STRING and that the AMF accepts non-default TAC values. TAC configuration is essential for paging, mobility management, and area-based access control.

## 3GPP Background
The TAC is a 3-octet value carried in the SupportedTAList IE within the NGSetupRequest. Combined with the PLMNIdentity, it forms a TAI (Tracking Area Identity = PLMN + TAC). The AMF uses TAIs for: paging UEs in idle mode (pages all gNBs in the UE's registered TAI list), tracking area updates (mobility within/between TAs), and access control (restricting UEs to specific areas).

TAC=0x00FF (decimal 255) tests non-trivial encoding -- all bits in the least significant byte are set. This catches byte-order and encoding issues that might not appear with simple TAC values like 0x0001.

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 9.3.3.10 | TAC (3-octet OCTET STRING) |
| TS 38.413 | 8.7.1 | NG Setup procedure |
| TS 23.501 | 5.4.4 | Tracking Area concepts |
| TS 23.502 | 4.2.3.2 | Tracking Area Update |

## Problem Statement
- What if the TAC encoding truncates to 2 bytes instead of 3?
- What if the AMF's served TA list doesn't include TAC 0x00FF?
- What if byte ordering is wrong (big-endian vs. little-endian)?

## Test Procedure (Step-by-Step)
1. Create gNB with custom TAC=00FF.
2. Connect SCTP, send NG Setup with TAC=0x00FF in SupportedTAList.
3. Verify gNB reaches READY state.
4. Teardown: remove gNB.

## Expected Behavior
- TAC encoded as 3 octets: 0x00 0x00 0xFF.
- AMF accepts the custom TAC.
- gNB reaches READY state.

## Pass/Fail Criteria
- **Pass:** gNB READY with custom TAC accepted.
- **Fail:** NGSetupFailure; TAC encoding error.

## Key Concepts for Training

### Tracking Area Identity (TAI)
A TAI = PLMN + TAC uniquely identifies a tracking area worldwide. UEs in idle mode (RRC_IDLE or RRC_INACTIVE) are paged within their registered TAI list. When a UE moves to a new TA not in its list, it performs a Tracking Area Update. TAI design balances: smaller TAs = more paging efficiency but more TAU signaling; larger TAs = less TAU signaling but more paging load.

### TAC in Network Planning
TAC assignment is part of network planning. Each gNB is assigned one or more TACs. Neighboring gNBs may share a TAC (same TA) or have different TACs (different TAs). The AMF's served TA list defines which TAs it manages. TAC=0x00FF (255) is a valid non-default value used in test and production networks.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| 2-byte TAC encoding | AMF sees wrong TAC | Use full 3-octet encoding |
| TAC not in AMF | NGSetupFailure | Add TAC 00FF to AMF served TAs |
| Byte order error | TAC=0xFF0000 instead of 0x0000FF | Check big-endian encoding |

## References
- 3GPP TS 38.413 V17.x -- Section 9.3.3.10 (TAC)
- 3GPP TS 23.501 V17.x -- Section 5.4.4 (Tracking Area)
- Related: TC-NGS-003 (default PLMN), TC-NGS-004 (custom PLMN), TC-NGS-013 (slice config)

## Quiz Questions
1. How is TAC=0x00FF encoded in the SupportedTAList IE?
   *Answer: As a 3-octet OCTET STRING: 0x00 0x00 0xFF. The TAC uses network byte order (big-endian).*

2. What is the relationship between TAC and paging in 5G?
   *Answer: When the AMF needs to page an idle UE, it sends Paging messages to all gNBs in the UE's registered TAI list. Each TAI = PLMN + TAC. The AMF identifies which gNBs belong to each TAC from the SupportedTAList received during NG Setup.*

3. If a UE registered with TAI={001/01, 0x0001} moves to a cell with TAI={001/01, 0x00FF}, what procedure is triggered?
   *Answer: A Tracking Area Update (TAU) / Registration Update. The UE detects that its current TAI is not in its registered TAI list and sends a Registration Request with type=mobility-registration-update to the AMF. The AMF updates the UE's registered TAI list.*

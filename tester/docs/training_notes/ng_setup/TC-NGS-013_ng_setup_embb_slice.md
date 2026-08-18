# TC-NGS-013: NG Setup with eMBB Slice (S-NSSAI SST=1)

## Overview
This test validates NG Setup with S-NSSAI (Single Network Slice Selection Assistance Information) configured for eMBB (Enhanced Mobile Broadband, SST=1). It verifies that slice information is correctly encoded in the SupportedTAList and accepted by the AMF.

## 3GPP Background
Network slicing (TS 23.501 Section 5.15) allows operators to create logically separated networks on shared infrastructure. Each slice is identified by an S-NSSAI containing:
- **SST (Slice/Service Type):** 1 byte. Standardized values: 1=eMBB, 2=URLLC, 3=MIoT, 4=V2X.
- **SD (Slice Differentiator):** 3 bytes, optional. Distinguishes slices with the same SST.

The gNB advertises its supported slices in the SupportedTAList -> BroadcastPLMNList -> SliceSupportList within the NGSetupRequest. The AMF checks if the offered slices match its configured slice support (PLMNSupportList).

eMBB (SST=1) is the default slice type for broadband services: internet browsing, video streaming, and general data. It prioritizes throughput and capacity.

**Network functions involved:** gNB, AMF, NSSF (for slice selection)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 9.3.1.24 | S-NSSAI IE |
| TS 23.501 | 5.15.2 | S-NSSAI and network slicing |
| TS 23.501 | 5.15.2.1 | Standardized SST values |
| TS 38.413 | 8.7.1 | NG Setup with slice support |

## Problem Statement
- What if the slice SST encoding is wrong (e.g., SST=0 instead of SST=1)?
- What if the AMF does not support the eMBB slice?
- What if the SD is included when it should be omitted (or vice versa)?

## Test Procedure (Step-by-Step)
1. Create gNB with eMBB slice configuration (SST=1) from config profile.
2. Connect SCTP, send NG Setup with S-NSSAI in SupportedTAList.
3. Verify gNB reaches READY state.
4. Teardown: remove gNB.

## Expected Behavior
- S-NSSAI with SST=1 (eMBB) included in SupportedTAList.
- AMF accepts the slice configuration.
- gNB reaches READY state.

## Pass/Fail Criteria
- **Pass:** gNB READY with eMBB slice accepted.
- **Fail:** NGSetupFailure due to unsupported slice.

## Key Concepts for Training

### Network Slicing Architecture
A network slice is an end-to-end logical network: RAN resources + core NFs + transport. Each slice has its own: AMF set, SMF, UPF (potentially), QoS policies, and capacity allocation. The S-NSSAI identifies the slice. During UE registration, the AMF uses the Requested NSSAI and Allowed NSSAI to determine which slices the UE can access.

### Standardized SST Values
- **SST=1 (eMBB):** Enhanced Mobile Broadband. High throughput, capacity-optimized. Default for internet and multimedia services.
- **SST=2 (URLLC):** Ultra-Reliable Low-Latency Communication. Sub-1ms latency, high reliability. Factory automation, remote surgery.
- **SST=3 (MIoT):** Massive IoT. Low power, high device density. Sensors, meters.
- **SST=4 (V2X):** Vehicle-to-Everything. Automotive communication.

### Slice Support in NG Setup
The gNB's SliceSupportList tells the AMF which slices this gNB can serve. The AMF's PLMNSupportList (in NGSetupResponse) tells the gNB which slices the AMF supports. The intersection determines which slices are available through this gNB-AMF pair.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Wrong SST encoding | AMF rejects slice | Verify SST=1 (1 byte, value 0x01) |
| Slice not configured | NGSetupFailure | Add eMBB to AMF slice support |
| SD mismatch | Slice rejected | Ensure SD matches or omit if not needed |
| No slice in NG Setup | AMF uses default | Include SliceSupportList IE |

## References
- 3GPP TS 23.501 V17.x -- Section 5.15 (Network Slicing)
- 3GPP TS 38.413 V17.x -- Section 9.3.1.24 (S-NSSAI)
- Related: TC-NGS-001 (basic NG Setup), TC-NGS-003 (PLMN), TC-NGS-005 (TAC)

## Quiz Questions
1. What are the two components of an S-NSSAI, and which is mandatory?
   *Answer: SST (Slice/Service Type, 1 byte) is mandatory. SD (Slice Differentiator, 3 bytes) is optional. SST identifies the slice type; SD differentiates between multiple slices of the same type.*

2. What is the SST value for eMBB, and what service characteristics does it represent?
   *Answer: SST=1. eMBB (Enhanced Mobile Broadband) provides high-throughput, capacity-optimized service for internet browsing, video streaming, and general data applications.*

3. How does the AMF determine which slices a UE can access?
   *Answer: The AMF intersects: (1) UE's Requested NSSAI (from Registration Request), (2) UE's subscribed S-NSSAIs (from UDM), and (3) AMF's configured/supported slices. The result is the Allowed NSSAI, sent to the UE in Registration Accept.*

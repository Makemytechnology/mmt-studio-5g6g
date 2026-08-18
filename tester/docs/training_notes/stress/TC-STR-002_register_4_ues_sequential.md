# TC-STR-002: Register 4 UEs Sequential

## Overview
This test validates the sequential registration of 4 UEs on a single gNB. It exercises the AMF's ability to maintain multiple concurrent UE contexts and the gNB's NGAP multiplexing capability at a small scale. This is the baseline multi-UE scaling test before pushing to higher UE counts.

## 3GPP Background
In a 5G SA network, a single gNB-AMF SCTP association carries signaling for all UEs served by that gNB. Each UE undergoes independent 5G-AKA authentication with unique subscriber credentials. The AMF maintains separate MM contexts, and the gNB assigns unique RAN UE NGAP IDs for each UE.

Per TS 23.501 Section 5.2, the AMF has a configurable maximum capacity (RelativeAMFCapacity, 0-255) that influences load balancing. With only 4 UEs, capacity should not be a concern, but the test validates the foundational multi-UE code path.

Sequential registration means each UE completes the full registration procedure before the next begins. This eliminates concurrency issues and tests the basic ability to accumulate UE contexts.

**Network functions involved:** 4 UEs, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 38.413 | 8.6.1 | InitialUEMessage (per UE) |
| TS 38.413 | 8.2 | UE Context Management |
| TS 23.501 | 5.2.2.1 | AMF serving multiple UEs |
| TS 33.501 | 6.1.3 | Independent 5G-AKA per UE |

## Problem Statement
- What if the AMF's UE context table is limited to fewer than 4 entries?
- What if the gNB's RAN UE NGAP ID space is too small?
- What if the UDM cannot serve authentication vectors for 4 different subscribers?
- What if registering the 4th UE causes the 1st UE's context to be evicted?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. Register UE_1: full 5G-AKA registration. Log success.
3. Register UE_2: full 5G-AKA registration. Log success.
4. Register UE_3: full 5G-AKA registration. Log success.
5. Register UE_4: full 5G-AKA registration. Log success.
6. All 4 UEs registered successfully.

## Expected Behavior
- Each UE receives independent authentication vectors and completes registration.
- All 4 UEs are simultaneously in REGISTERED state.
- The gNB maintains 4 separate UE contexts with unique RAN UE NGAP IDs.
- The AMF maintains 4 separate MM contexts with unique AMF UE NGAP IDs.

## Pass/Fail Criteria
- **Pass:** All 4 UEs reach REGISTERED state.
- **Fail:** Any UE fails to register; timeout exceeded.

## Key Concepts for Training

### Scaling UE Registration
Multi-UE registration testing follows a progression: 4 -> 8 -> 16 -> 32 -> 64 -> 128 UEs. Each step doubles the load and may expose new bottlenecks. At 4 UEs, the test validates basic multi-UE functionality. At higher counts, AMF memory, UDM database throughput, and SCTP buffer limits become factors.

### AMF UE Context Table
The AMF maintains an in-memory table of all active UE contexts. Each context includes: SUPI, 5G-GUTI, NAS security context (KAMF, KNASint, KNASenc, NAS COUNT), registration state, list of PDU sessions, and NGAP IDs. The table must support efficient lookup by SUPI, 5G-GUTI, and NGAP ID pair.

### Sequential vs. Concurrent Registration
Sequential registration tests each UE independently -- if UE_3 fails, it's not because of concurrency with UE_1. This is a simpler test than concurrent registration and helps isolate bugs: is the issue with multi-UE context management (sequential fails) or with concurrent processing (only concurrent fails)?

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| UE context limit reached | 4th UE rejected | Increase AMF max-ue-count config |
| Subscriber not provisioned | Auth failure for one UE | Verify all 4 IMSIs in UDM |
| ID exhaustion | RAN UE NGAP ID collision | Increase ID space or verify no leak |
| Slow sequential processing | Total time > 4x single UE | Check AMF processing pipeline |

## References
- 3GPP TS 38.413 V17.x -- Section 8.2 (UE Context Management)
- 3GPP TS 23.501 V17.x -- Section 5.2 (AMF capacity)
- Related: TC-STR-003 (8 UEs), TC-STR-004 (16 UEs), TC-STR-005 (32 UEs), TC-REG-002 (2 UEs)

## Quiz Questions
1. In sequential registration of 4 UEs, what is the minimum number of authentication vectors the UDM must generate?
   *Answer: 4 -- one per UE. Each UE has unique subscriber credentials (IMSI, K, OPc) and requires its own RAND/AUTN pair.*

2. If all 4 UEs are registered on the same gNB, how many SCTP associations exist between the gNB and AMF?
   *Answer: One. All 4 UEs' NGAP signaling is multiplexed over a single SCTP association. The (RAN UE NGAP ID, AMF UE NGAP ID) pair distinguishes each UE's context.*

3. Why might sequential registration succeed while concurrent registration of the same 4 UEs fails?
   *Answer: Concurrent registration introduces race conditions in the AMF (e.g., simultaneous NGAP ID allocation, concurrent UDM queries, parallel NAS message processing). Sequential registration avoids these by completing one UE fully before starting the next.*

# TC-AUTH-004: Multi-UE Independent Authentication

## Overview
This test validates that two UEs can independently authenticate via 5G-AKA on the same gNB. Each UE has unique subscriber credentials (IMSI, K, OPc) and receives independent authentication vectors. This confirms the AMF and AUSF/UDM correctly handle concurrent subscriber authentication.

## 3GPP Background
Each UE's 5G-AKA is completely independent: different IMSI -> different subscriber profile in UDM -> different K/OPc -> different RAND/AUTN -> different RES* -> different key hierarchy. The AMF maintains separate security contexts per UE, identified by the (RAN UE NGAP ID, AMF UE NGAP ID) pair.

The AUSF generates authentication vectors per subscriber. With 2 concurrent UEs, the AUSF handles 2 parallel Nausf_UEAuthentication requests. The UDM serves 2 different subscriber profiles.

**Network functions involved:** 2 UEs, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.501 | 6.1.3 | 5G-AKA (per UE) |
| TS 38.413 | 8.6.1 | InitialUEMessage (per UE) |
| TS 33.501 | 6.7.1 | NAS security context (per UE) |

## Problem Statement
- What if the AMF uses UE_1's auth vector for UE_2?
- What if the security contexts get mixed up?
- What if the AUSF serializes auth requests, causing delays?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, NG Setup.
2. Register UE_1: full 5G-AKA authentication.
3. Register UE_2: independent 5G-AKA authentication.
4. Verify both UEs are REGISTERED.
5. Verify both UEs have independent security keys.

## Expected Behavior
- Both UEs authenticate independently with unique vectors.
- Both reach REGISTERED state.
- Each UE has its own KNASint and KNASenc.

## Pass/Fail Criteria
- **Pass:** Both UEs REGISTERED with security keys.
- **Fail:** Either UE fails; key contamination between UEs.

## Key Concepts for Training

### Security Context Independence
Each UE's NAS security context is completely independent: unique KAMF, KNASint, KNASenc, NAS uplink/downlink COUNT, ngKSI. The AMF indexes contexts by AMF UE NGAP ID. Any cross-contamination (UE_1's key used for UE_2's message) would cause integrity verification failure and is a severe security bug.

### AUSF Concurrent Processing
The AUSF must handle concurrent authentication requests for different subscribers. Each request triggers a UDM lookup for subscriber credentials and SQN. If the AUSF serializes requests (single-threaded), multi-UE auth is slow but correct. If it parallelizes, it must ensure thread-safe access to subscriber state.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Context mix-up | UE_2 gets UE_1's keys | Check AMF UE NGAP ID indexing |
| Auth vector reuse | Same RAND for both UEs | Verify per-UE RAND generation |
| AUSF bottleneck | Slow second auth | Check AUSF concurrency model |

## References
- 3GPP TS 33.501 V17.x -- Section 6.1.3 (5G-AKA)
- Related: TC-REG-002 (multi-UE reg), TC-AUTH-006 (all UEs), TC-AUTH-001 (single auth)

## Quiz Questions
1. Can two UEs on the same gNB share the same KAMF? Why or why not?
   *Answer: No. Each UE has unique K/OPc credentials and receives unique RAND values. The Milenage output (CK, IK) is different, leading to different KAUSF/KSEAF/KAMF. Even if by coincidence the same RAND were used, different K values produce different results.*

2. How does the AMF ensure it does not apply UE_1's security context to UE_2's NAS messages?
   *Answer: Each NAS message is carried in an NGAP message that includes the (RAN UE NGAP ID, AMF UE NGAP ID) pair. The AMF looks up the security context by AMF UE NGAP ID, ensuring the correct context is used for each UE.*

3. What would happen if the AMF accidentally applied UE_1's KNASint to verify UE_2's NAS message integrity?
   *Answer: The MAC verification would fail because the message was integrity-protected with UE_2's KNASint, not UE_1's. The AMF would detect the MAC mismatch and either discard the message or send a NAS Security Mode Reject. The UE_2 registration would fail.*

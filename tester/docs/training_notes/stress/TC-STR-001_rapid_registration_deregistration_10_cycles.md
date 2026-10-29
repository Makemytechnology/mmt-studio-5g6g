# TC-STR-001: Rapid Registration/Deregistration 10 Cycles

## Overview
This test validates the AMF's ability to handle 10 rapid attach/detach cycles on a single UE without failures. It stresses SQN management, NAS context creation/deletion, NGAP UE context lifecycle, and memory management. Unlike TC-REG-003 (3 cycles), this test pushes the cycle count higher to expose issues that only manifest after repeated operations.

## 3GPP Background
Each registration cycle involves a full 5G-AKA authentication, NAS Security Mode Command, Registration Accept, followed by UE-initiated Deregistration. Over 10 cycles, the SQN (Sequence Number) increments at least 10 times. The AMF must create and destroy the UE's MM context 10 times, and the AUSF/UDM must generate 10 fresh authentication vectors.

Key timing considerations: each cycle involves SCTP round-trips for NGAP, NAS message exchanges for authentication and security, and context management operations. At rapid pace, the AMF's context cleanup from cycle N must complete before cycle N+1 begins.

Per TS 33.102 Annex C.2, the SQN uses a windowing mechanism. If SQN_HE runs too far ahead of SQN_MS (e.g., from failed intermediate attempts), the UE triggers AUTS resynchronization. This test verifies that SQN stays in sync across all 10 cycles.

**Network functions involved:** UE, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 24.501 | 5.5.2.2 | UE-initiated Deregistration |
| TS 33.501 | 6.1.3 | 5G-AKA (per cycle) |
| TS 33.102 | C.2 | SQN management |
| TS 38.413 | 8.3.1 | UE Context Release |

## Problem Statement
- What if SQN drifts out of sync after several cycles, causing permanent auth failure?
- What if the AMF leaks memory or file descriptors on each context create/delete?
- What if the UDM's SQN database becomes inconsistent under rapid update load?
- What if a race condition occurs between deregistration cleanup and re-registration?
- What if the gNB runs out of RAN UE NGAP IDs due to slow ID recycling?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. For each cycle i (1 to 10):
   a. Register UE: attach to gNB, 5G-AKA auth (timeout=10s).
   b. Deregister UE: send Deregistration Request (timeout=10s).
3. All 10 cycles must complete without any failure.

## Expected Behavior
- All 10 registration cycles succeed with valid 5G-AKA authentication.
- All 10 deregistration cycles complete cleanly.
- No SQN resynchronization (AUTS) is triggered.
- UE transitions DEREGISTERED -> REGISTERED -> DEREGISTERED in each cycle.
- AMF handles rapid context turnover without resource exhaustion.

## Pass/Fail Criteria
- **Pass:** All 10 rapid cycles complete successfully.
- **Fail:** Any cycle fails to register or deregister; SQN sync failure; timeout.

## Key Concepts for Training

### Stress Testing Philosophy
Stress tests expose defects that functional tests miss. A single registration always works; 10 rapid cycles may reveal memory leaks (each cycle allocates/frees context structures), SQN drift (each cycle increments SQN), timer conflicts (cleanup timers from cycle N firing during cycle N+1), and concurrency issues in the core network.

### SQN Windowing
The SQN mechanism prevents replay attacks but requires careful management. The UDM maintains SQN_HE (highest SQN ever used). The UE maintains SQN_MS (last accepted SQN). The UE accepts AUTN if SQN is in the range [SQN_MS, SQN_MS + Delta]. If SQN_HE > SQN_MS + Delta, the UE triggers AUTS resync. Under rapid cycling, SQN_HE increments quickly but should remain in the UE's acceptance window.

### Context Lifecycle Management
Each registration creates an MM context (security keys, registration state, session list). Each deregistration destroys it. Proper lifecycle management means: all allocated memory freed, all timers cancelled, all associated PDU sessions released, RAN UE NGAP ID returned to pool. Failure at any step causes resource leaks.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| SQN out of sync | Auth failure with AUTS on cycle 5+ | Check UDM SQN increment and delta |
| Memory leak | AMF OOM after many cycles | Profile AMF memory usage |
| Timer race | Deregistration timer fires during new registration | Ensure timer cleanup on context delete |
| Slow ID recycle | RAN UE NGAP ID exhausted | Verify ID release in UE Context Release |
| UDM lock contention | Slow auth vector generation | Check UDM database concurrency |

## References
- 3GPP TS 33.102 V17.x -- Annex C.2 (SQN management)
- 3GPP TS 24.501 V17.x -- Section 5.5.1, 5.5.2 (Registration/Deregistration)
- Related: TC-REG-003 (3 cycles), TC-AUTH-008 (repeated auth), TC-STR-009 (multi-UE cycles)

## Quiz Questions
1. After 10 registration cycles, how many times has the SQN been incremented in the UDM (minimum)?
   *Answer: At least 10 times (once per authentication). More if the UDM pre-generates vectors or if any resynchronization occurred.*

2. Why is rapid cycling more likely to trigger SQN resynchronization than slow cycling?
   *Answer: Rapid cycling can cause race conditions where the UDM increments SQN_HE before the UE has processed the previous AUTN. If the UDM generates multiple vectors per request (batching), SQN_HE may jump by more than 1 per cycle, potentially exceeding the UE's acceptance window.*

3. What resources must the AMF free when a UE deregisters, and what happens if any are not freed?
   *Answer: The AMF must free: MM context (security keys, state), NAS timers, any pending NAS messages, PDU session contexts (notify SMF), NGAP UE context (send UE Context Release). If not freed: memory leaks (eventual OOM), timer fires on stale context (undefined behavior), IP addresses not returned to pool (pool exhaustion).*

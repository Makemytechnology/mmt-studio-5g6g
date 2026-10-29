# TC-STR-009: Attach/Detach 8 UEs 3 Cycles

## Overview
This test combines multi-UE registration with repeated attach/detach cycling. Each of 8 UEs performs 3 register/deregister cycles, resulting in 24 total registration events and 24 deregistration events. This tests context lifecycle management at scale and validates that SQN synchronization is maintained across multiple UEs and multiple cycles.

## 3GPP Background
This test generates 48 total NAS registration/deregistration events across 8 UEs. Each UE independently cycles through DEREGISTERED -> REGISTERED -> DEREGISTERED three times. The inner loop (per UE) tests SQN management for that subscriber. The outer loop (across UEs) tests AMF concurrent context capacity.

Key concerns at this scale:
- SQN increments 3 times per UE (24 total SQN updates in UDM)
- AMF creates/destroys 8 UE contexts per cycle (24 total context operations)
- gNB allocates/releases 8 RAN UE NGAP IDs per cycle
- AUSF generates 24 authentication vectors total

The test executes UEs sequentially -- each UE completes all 3 cycles before moving to the next. This means the AMF only manages 1 active context at a time, testing lifecycle cleanup rather than concurrent capacity.

**Network functions involved:** 8 UEs, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 24.501 | 5.5.2.2 | UE-initiated Deregistration |
| TS 33.102 | C.2 | SQN management |
| TS 38.413 | 8.3.1 | UE Context Release |
| TS 33.501 | 6.1.3 | 5G-AKA |

## Problem Statement
- What if SQN gets out of sync for UE_5 after its 2nd cycle while other UEs work fine?
- What if context cleanup from UE_3's deregistration is incomplete when UE_4's registration starts?
- What if the AMF accumulates stale timer references from repeated context creation?
- What if the gNB's UE context table grows due to slow ID release?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For each UE (UE_1 through UE_8):
   a. For each cycle (1 to 3):
      - Register UE (timeout=10s).
      - Deregister UE (timeout=10s).
   b. Log completion for this UE.
3. All 8 UEs complete all 3 cycles.

## Expected Behavior
- Each UE completes 3 attach/detach cycles without SQN issues.
- Total: 24 successful registrations and 24 successful deregistrations.
- No stale contexts remain between UEs.
- SQN for each UE increments 3 times (one per authentication).

## Pass/Fail Criteria
- **Pass:** All 8 UEs complete all 3 cycles (48 operations total).
- **Fail:** Any cycle fails for any UE.

## Key Concepts for Training

### Combined Scaling Dimensions
This test combines two scaling dimensions: UE count (8) and cycle count (3). Total operations = UEs x cycles x 2 (register + deregister) = 48. This multiplicative scaling exposes issues that neither dimension alone would trigger.

### Per-UE SQN Isolation
Each UE's SQN is managed independently in the UDM. UE_1's 3 cycles increment UE_1's SQN 3 times. UE_2's SQN is completely separate. This test verifies there is no cross-contamination between subscribers' SQN counters in the UDM database.

### Sequential UE Processing
The test processes each UE's 3 cycles before moving to the next UE. This means the AMF sees: 3 cycles for IMSI_1, then 3 cycles for IMSI_2, etc. This tests cleanup between subscribers (does IMSI_1's context fully clean up before IMSI_2 starts?) rather than concurrent multi-UE processing.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| SQN drift for one UE | Auth failure on cycle 3 for one IMSI | Check per-subscriber SQN in UDM |
| Cross-UE contamination | UE_4 gets UE_3's context | Verify complete context cleanup |
| Accumulated timers | Slow processing for later UEs | Check timer cancellation on deregister |
| Total timeout | Test exceeds overall timeout | Increase test timeout or check AMF speed |

## References
- 3GPP TS 33.102 V17.x -- Annex C.2 (SQN management)
- 3GPP TS 24.501 V17.x -- Section 5.5.1, 5.5.2
- Related: TC-STR-001 (single UE 10 cycles), TC-REG-003 (single UE 3 cycles), TC-AUTH-008 (repeated auth)

## Quiz Questions
1. How many total authentication vectors does the UDM need to generate for this test?
   *Answer: 24 -- 3 per UE x 8 UEs. Each registration cycle requires a fresh authentication vector from the AUSF/UDM.*

2. If UE_5 fails on its 3rd cycle with an AUTS resync, but UEs 1-4 and 6-8 all pass, what is the most likely cause?
   *Answer: UE_5's SQN is out of sync in the UDM. This could be caused by a previous failed authentication attempt that incremented SQN_HE without the UE accepting it, pushing SQN_HE beyond UE_5's acceptance window. The UDM's SQN for IMSI_5 should be checked and potentially reset.*

3. Why does this test process UEs sequentially rather than running all 8 UEs' cycles concurrently?
   *Answer: Sequential processing isolates the test to lifecycle management (create/destroy contexts cleanly) rather than concurrent processing. If a failure occurs, it is easier to identify which UE and which cycle caused it. Concurrent cycling would introduce additional race condition complexity.*

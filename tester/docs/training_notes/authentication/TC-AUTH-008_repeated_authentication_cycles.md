# TC-AUTH-008: Repeated Authentication Cycles (SQN Management)

## Overview
This test performs 3 register/deregister cycles, focusing on SQN (Sequence Number) management across repeated authentications. Each cycle requires fresh 5G-AKA with incremented SQN. This validates that the UDM's SQN tracking remains correct and no AUTS resynchronization is triggered.

## 3GPP Background
Each authentication cycle increments SQN_HE in the UDM. After 3 cycles, SQN_HE should be SQN_initial + 3 (minimum). The UE's SQN_MS should track: after cycle 1, SQN_MS = SQN_1; after cycle 2, SQN_MS = SQN_2 > SQN_1; after cycle 3, SQN_MS = SQN_3 > SQN_2.

Proper SQN management means: (1) SQN always increases (anti-replay), (2) increments stay within the UE's acceptance window, (3) no AUTS resync is triggered (clean increments).

Per TS 33.102 Annex C.2, the SQN management can use either counter-based or time-based approaches. Most implementations use counter-based with a modular window.

**Network functions involved:** UE, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.102 | C.2 | SQN management mechanisms |
| TS 33.501 | 6.1.3 | 5G-AKA |
| TS 33.102 | 6.3.5 | SQN resynchronization |
| TS 24.501 | 5.5.1.2, 5.5.2.2 | Registration/Deregistration |

## Problem Statement
- What if SQN_HE jumps by more than 1 per cycle (batch vector generation)?
- What if SQN_HE exceeds SQN_MS + Delta on cycle 3?
- What if the UDM's SQN update is not persistent (lost on restart)?
- What if the deregistration between cycles causes SQN inconsistency?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, NG Setup.
2. For each cycle (1 to 3):
   a. Full registration with 5G-AKA.
   b. Verify REGISTERED with security keys.
   c. Deregister UE.
   d. Verify DEREGISTERED.
   e. Wait 500ms.
3. All 3 cycles pass without AUTS resync.

## Expected Behavior
- All 3 authentication cycles succeed directly (no AUTS).
- SQN increments monotonically across cycles.
- Security keys are fresh each cycle.
- No SQN sync failures.

## Pass/Fail Criteria
- **Pass:** All 3 cycles complete; UE REGISTERED and DEREGISTERED each cycle; no AUTS triggered.
- **Fail:** Any cycle fails; AUTS resync needed; SQN desynchronization.

## Key Concepts for Training

### SQN Counter Behavior
In counter-based SQN (most common): SQN_HE starts at 0, increments by 1 per auth vector. After 3 cycles: SQN_HE = 3. The UE's acceptance window is typically SQN_MS to SQN_MS + 2^28. Since increments are small (1 per cycle), desync should never occur in this test unless there are implementation bugs.

### SQN Persistence
The UDM must persist SQN_HE to stable storage (database) after each update. If the UDM crashes and restores from a backup with an older SQN_HE, it may generate auth vectors with SQN values the UE has already seen, causing desync. This test indirectly validates SQN persistence by succeeding across 3 cycles.

### Clean vs. Dirty Auth Cycles
A "clean" auth cycle means: no AUTS resync, no retried auth requests, single RAND/AUTN exchange. A "dirty" cycle involves AUTS resync or multiple auth attempts. This test expects all clean cycles, which indicates proper SQN management.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| AUTS on cycle 2 | SQN jumped too far | Reduce batch vector count in UDM |
| AUTS on cycle 3 | Accumulated SQN drift | Check SQN persistence between cycles |
| Auth failure (not AUTS) | Other auth error | Check K/OPc consistency |
| Slow later cycles | Processing delay | Check UDM query performance |

## References
- 3GPP TS 33.102 V17.x -- Annex C.2 (SQN management)
- 3GPP TS 33.501 V17.x -- Section 6.1.3 (5G-AKA)
- Related: TC-AUTH-003 (AUTS), TC-AUTH-005 (re-auth), TC-STR-001 (10 rapid cycles)

## Quiz Questions
1. After 3 clean authentication cycles, what is the minimum value of SQN_HE in the UDM?
   *Answer: SQN_initial + 3. Each cycle generates at least one authentication vector, incrementing SQN_HE by at least 1.*

2. Why does this test expect NO AUTS resynchronization across 3 cycles?
   *Answer: With clean sequential cycles and small SQN increments (1 per cycle), SQN_HE never exceeds SQN_MS + Delta. The UE's acceptance window (typically 2^28) is vastly larger than the 3-step increment. AUTS would indicate a bug in SQN management.*

3. What is the significance of the 500ms pause between deregistration and re-registration?
   *Answer: It allows the AMF to fully release the UE context, the UDM to commit the SQN update to storage, and any pending NAS messages to drain. Without this pause, a race condition could cause the new registration to interact with the old context's cleanup.*

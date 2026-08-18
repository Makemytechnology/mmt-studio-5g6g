# TC-AUTH-003: SQN Resynchronization via AUTS

## Overview
This test validates that authentication succeeds even when the SQN (Sequence Number) may be out of sync between the UE and network. If SQN desynchronization occurs, the UE generates an AUTS (Authentication Synchronization) token, and the network resyncs before retrying authentication. This is a critical resilience mechanism.

## 3GPP Background
The SQN is a 48-bit counter used to prevent replay attacks in authentication. The UDM maintains SQN_HE (highest SQN ever used for this subscriber). The UE maintains SQN_MS (last accepted SQN). The SQN in AUTN must satisfy: SQN_MS < SQN <= SQN_MS + Delta (a configurable window, typically 2^28).

**SQN desynchronization** occurs when SQN_HE > SQN_MS + Delta. Causes: failed auth attempts (SQN_HE incremented but UE didn't update SQN_MS), UDM database restore from backup, or multiple concurrent auth attempts.

**Resynchronization procedure (TS 33.102 Section 6.3.5):**
1. UE receives Auth Request with AUTN containing SQN outside acceptance range.
2. UE sends Authentication Failure with cause=21 (Synch failure) and AUTS parameter.
3. AUTS = SQN_MS xor AK* || MAC-S (computed using f1* and f5* Milenage functions).
4. AMF/AUSF forwards AUTS to UDM.
5. UDM decrypts SQN_MS from AUTS, re-generates RAND/AUTN with SQN > SQN_MS.
6. AMF re-sends Authentication Request with new RAND/AUTN.
7. UE verifies new AUTN, computes RES*, authentication succeeds.

**Network functions involved:** UE, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.501 | 6.1.3.4 | SQN resynchronization |
| TS 33.102 | 6.3.5 | AUTS generation and handling |
| TS 33.102 | C.2 | SQN management mechanisms |
| TS 35.205 | 3.5 | f1* and f5* functions |
| TS 24.501 | 5.4.1.3.4 | Authentication Failure message |

## Problem Statement
- What if AUTS computation is incorrect (wrong f1*/f5* implementation)?
- What if the UDM fails to extract SQN_MS from AUTS?
- What if the UDM generates a new SQN that is still out of range?
- What if the network enters an infinite resync loop?
- What if AUTS replay is possible (reusing old AUTS tokens)?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, NG Setup.
2. Register UE (triggers 5G-AKA authentication).
3. If SQN is in range: normal auth succeeds directly.
4. If SQN is out of range: UE sends Auth Failure with AUTS, network resyncs, retry succeeds.
5. Verify UE reaches REGISTERED state.
6. Verify UE has security keys (KNASint present).

## Expected Behavior
- Authentication succeeds regardless of initial SQN state.
- If SQN resync was needed, it completes transparently.
- UE reaches REGISTERED with valid security keys.

## Pass/Fail Criteria
- **Pass:** UE REGISTERED with security keys, with or without SQN resync.
- **Fail:** Permanent auth failure; infinite resync loop.

## Key Concepts for Training

### SQN Desynchronization Causes
- **Failed auth attempts:** Each auth vector generation increments SQN_HE. If the UE never processes the AUTN (e.g., message lost), SQN_HE advances while SQN_MS stays.
- **Database restore:** If UDM is restored from a backup with older SQN_HE, it may generate SQN values below SQN_MS.
- **Concurrent vectors:** UDM may generate multiple vectors (batching), incrementing SQN_HE by N instead of 1.
- **Multi-device:** If the same subscription is used on multiple devices (eSIM clone), SQN_MS diverges.

### AUTS Token Structure
AUTS is 14 bytes: (SQN_MS xor AK*) || MAC-S. AK* is computed using Milenage f5* function (different from f5 used in normal auth). MAC-S is computed using f1* function. The UDM uses the received RAND (from the failed auth) to compute AK* and recover SQN_MS. Then generates new vectors with SQN > SQN_MS.

### SQN Management Mechanisms
TS 33.102 Annex C.2 defines two SQN management approaches: (1) **Time-based:** SQN encodes a timestamp, eliminating explicit synchronization. (2) **Counter-based:** SQN is a pure counter. Counter-based is simpler but more prone to desync. Both use the same AUTS resync mechanism as recovery.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| AUTS computation error | UDM can't extract SQN_MS | Verify f1* and f5* implementation |
| Infinite resync loop | Auth never succeeds | Check UDM SQN update after resync |
| SQN jumps too far | Immediate resync on every auth | Reduce batch vector generation count |
| AUTS replay | Security concern | UDM should track used AUTS tokens |

## References
- 3GPP TS 33.102 V17.x -- Section 6.3.5 (AUTS), Annex C.2 (SQN management)
- 3GPP TS 33.501 V17.x -- Section 6.1.3.4 (SQN resync)
- 3GPP TS 35.205 V17.x -- Sections 3.5 (f1*, f5*)
- Related: TC-AUTH-001 (normal auth), TC-AUTH-008 (repeated auth), TC-STR-001 (rapid cycles)

## Quiz Questions
1. What are the two Milenage functions unique to SQN resynchronization (not used in normal auth)?
   *Answer: f1* (generates MAC-S for AUTS integrity) and f5* (generates AK* for SQN concealment in AUTS). These are different from f1 (MAC-A) and f5 (AK) used in normal authentication.*

2. Describe the AUTS token structure and how the UDM extracts SQN_MS from it.
   *Answer: AUTS = (SQN_MS xor AK*) || MAC-S (14 bytes total: 6 bytes concealed SQN + 8 bytes MAC). The UDM uses the RAND from the failed auth request and the subscriber's K to compute AK* via f5*. Then: SQN_MS = (first 6 bytes of AUTS) xor AK*. The UDM verifies MAC-S using f1* to confirm AUTS authenticity.*

3. Why can SQN desynchronization happen even in a properly functioning network?
   *Answer: If the UDM pre-generates multiple authentication vectors (e.g., 5 at once), SQN_HE jumps by 5. If only 1 vector is used and the UE accepts it (SQN_MS advances by 1), the remaining 4 vectors are wasted. If those vectors are later served to the UE, their SQN values are stale relative to SQN_MS. Also, transient network issues may cause auth vectors to be generated but never delivered to the UE.*

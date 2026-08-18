# TC-AUTH-005: Re-Authentication After Deregistration

## Overview
This test validates that a UE can successfully re-authenticate after deregistration. It performs: register -> deregister -> re-register, verifying that fresh 5G-AKA authentication vectors are generated for the second registration and that no stale security context interferes.

## 3GPP Background
When a UE deregisters, the AMF deletes the UE's NAS security context (TS 33.501 Section 6.7.3). This means KAMF, KNASint, KNASenc, and NAS COUNTs are erased. On re-registration, the UE cannot reuse the old security context -- it must perform fresh 5G-AKA.

The UDM generates new authentication vectors with an incremented SQN. The re-registration tests that: SQN increments correctly, the old security context is fully cleared, and the new auth vectors are valid.

**Network functions involved:** UE, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.501 | 6.7.3 | Security context handling on deregistration |
| TS 33.501 | 6.1.3 | 5G-AKA (fresh auth) |
| TS 24.501 | 5.5.2.2 | Deregistration |
| TS 24.501 | 5.5.1.2 | Re-registration |

## Problem Statement
- What if the old security context persists and conflicts with new keys?
- What if SQN has drifted too far during the deregistration process?
- What if the AMF tries to use the old 5G-GUTI for the re-registering UE?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, NG Setup.
2. First registration: full 5G-AKA -> REGISTERED.
3. Deregister UE -> DEREGISTERED.
4. Wait 500ms for context cleanup.
5. Re-register UE: fresh 5G-AKA -> REGISTERED.
6. Verify security keys present (KNASint).

## Expected Behavior
- First registration succeeds with valid keys.
- Deregistration clears all security context.
- Re-registration uses fresh authentication vectors.
- New security keys derived, independent of first registration.

## Pass/Fail Criteria
- **Pass:** Re-registration succeeds; UE REGISTERED with fresh keys.
- **Fail:** Re-registration fails; stale context interference.

## Key Concepts for Training

### Security Context Lifecycle
The NAS security context has a defined lifecycle: created during authentication (KAMF derived), activated during Security Mode Command (KNASint/KNASenc derived), used during registered state, deleted on deregistration. Proper deletion prevents: key reuse (security vulnerability), stale COUNT values (NAS message replay), and context confusion.

### Fresh Authentication Requirement
After deregistration, the AMF has no security context for the UE. It cannot verify NAS integrity on subsequent messages. Therefore, re-registration requires fresh 5G-AKA to establish a new security context from scratch.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Stale context | Re-auth uses old keys | Verify context deletion on deregister |
| SQN out of sync | Re-auth fails with AUTS | Check SQN increment on first auth |
| 5G-GUTI reuse | AMF confuses with old context | AMF should assign new 5G-GUTI |

## References
- 3GPP TS 33.501 V17.x -- Section 6.7.3 (Context handling)
- Related: TC-AUTH-001 (first auth), TC-DEREG-001 (deregistration), TC-AUTH-008 (repeated cycles)

## Quiz Questions
1. Why must the UE perform fresh 5G-AKA after deregistration instead of reusing the previous security context?
   *Answer: The AMF deletes the NAS security context on deregistration. Without KAMF/KNASint, the AMF cannot verify NAS integrity on the re-registration request. Fresh 5G-AKA establishes a new security context from scratch.*

2. What happens to the SQN when a UE deregisters and re-registers?
   *Answer: SQN_HE increments on each authentication vector generation. After deregistration, re-registration generates a new vector with SQN_HE+1. The UE's SQN_MS was updated during the first auth. The new SQN should be within the acceptance window.*

3. If the AMF retains the old 5G-GUTI after deregistration, what problem could occur?
   *Answer: If the UE re-registers with the old 5G-GUTI, the AMF might try to retrieve the old security context (which should be deleted) instead of initiating fresh 5G-AKA. This could cause: auth failure (no context found), security violation (reused keys), or context confusion with another UE that was assigned the recycled GUTI.*

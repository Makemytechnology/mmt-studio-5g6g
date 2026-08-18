# TC-AUTH-006: Authenticate All Configured UEs

## Overview
This test authenticates all configured UEs (up to 3) sequentially, verifying that each UE's subscriber profile in the UDM is correct and that independent 5G-AKA sessions succeed. It validates the completeness of subscriber provisioning and per-UE credential management.

## 3GPP Background
Each configured UE has a unique subscriber profile: IMSI, K (permanent key), OPc (derived operator key), and SQN. The UDM stores these credentials. This test iterates through all configured UEs, performing full 5G-AKA for each, confirming that: (1) all subscribers are provisioned, (2) all credentials are correct, (3) all SQN values are in sync.

**Network functions involved:** 3 UEs, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.501 | 6.1.3 | 5G-AKA |
| TS 23.502 | 4.2.2.2 | Registration |

## Problem Statement
- What if one of the 3 UEs has incorrect credentials in the UDM?
- What if subscriber provisioning was incomplete (missing UE_3)?
- What if one UE's SQN is desynchronized while others work fine?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, NG Setup.
2. For each configured UE (UE_1, UE_2, UE_3):
   a. Full registration with 5G-AKA.
   b. Verify REGISTERED state.
   c. Verify security keys present.
3. All UEs authenticated.

## Expected Behavior
- All 3 UEs authenticate successfully.
- Each has independent security keys.
- No credential or SQN issues for any subscriber.

## Pass/Fail Criteria
- **Pass:** All configured UEs REGISTERED with keys.
- **Fail:** Any UE fails authentication.

## Key Concepts for Training

### Subscriber Provisioning Validation
This test serves as a provisioning validation: it confirms that every configured UE in the tester has a matching subscriber record in the UDM. Mismatches between tester config (K, OPc, IMSI) and UDM records are a common source of test failures.

### Per-Subscriber Credential Isolation
Each subscriber's K and OPc are unique and secret. Even a single-bit difference in K produces completely different authentication results. This test confirms credential isolation: UE_1's K cannot authenticate as UE_2, and vice versa.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Missing subscriber | Auth failure for one UE | Provision missing IMSI in UDM |
| Wrong K/OPc | MAC verification fails | Correct credentials in UDM or tester |
| SQN out of sync | AUTS triggered for one UE | Reset SQN in UDM for that subscriber |

## References
- 3GPP TS 33.501 V17.x -- Section 6.1.3 (5G-AKA)
- Related: TC-AUTH-004 (2 UEs), TC-AUTH-001 (single UE), TC-STR-002 (4 UEs reg)

## Quiz Questions
1. Why is testing all configured UEs important beyond just testing one?
   *Answer: Individual subscriber provisioning errors (wrong K, missing record, SQN mismatch) affect specific UEs. Testing all UEs validates the completeness and correctness of the entire subscriber database.*

2. If UE_1 and UE_2 authenticate successfully but UE_3 fails, what is the most likely cause?
   *Answer: UE_3's subscriber record is either missing from the UDM or has incorrect credentials (K, OPc). Since UE_1 and UE_2 work, the AMF/AUSF/UDM pipeline is functional -- the issue is subscriber-specific.*

3. Can all 3 UEs use the same K and OPc values?
   *Answer: Technically, each UE must have a unique IMSI. They could share K/OPc (not recommended), but in practice each subscriber has unique credentials. If they share K/OPc, any compromise affects all three subscribers. Unique credentials ensure security isolation.*

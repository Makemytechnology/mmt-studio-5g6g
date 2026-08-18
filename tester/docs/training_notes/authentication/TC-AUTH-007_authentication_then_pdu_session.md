# TC-AUTH-007: Authentication Then PDU Session End-to-End

## Overview
This end-to-end test validates the complete path from 5G-AKA authentication through PDU session establishment. It confirms that NAS security is active during PDU session signaling and that the authenticated UE can establish a data path.

## 3GPP Background
PDU Session Establishment Request is a NAS message sent after registration. It is protected by the NAS security context established during authentication and Security Mode Command. The AMF verifies the integrity of the PDU Session Establishment Request using KNASint before forwarding it to the SMF.

This test combines authentication (control plane security) with PDU session (data plane connectivity), verifying the integration between NAS security and session management.

**Network functions involved:** UE, gNB, AMF, SMF, UPF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.501 | 6.1.3 | 5G-AKA |
| TS 24.501 | 6.4.1 | PDU Session Establishment |
| TS 33.501 | 6.7.1 | NAS security for PDU session |

## Problem Statement
- What if NAS security keys are derived but not correctly applied to PDU session messages?
- What if the AMF fails to verify integrity on the PDU Session Establishment Request?
- What if PDU session establishment works without security (security bypass)?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, NG Setup.
2. Full registration with 5G-AKA and PDU session (DNN=internet, PSI=1).
3. Verify UE is REGISTERED.
4. Verify security keys present.
5. Record PDU session IP address.

## Expected Behavior
- Authentication succeeds, NAS security established.
- PDU Session Establishment Request is integrity-protected.
- PDU session created with valid UE IP.
- Full end-to-end data path operational.

## Pass/Fail Criteria
- **Pass:** UE REGISTERED with keys; PDU session active with IP.
- **Fail:** Auth fails; PDU session fails; no IP.

## Key Concepts for Training

### NAS Security for Session Management
All NAS messages after Security Mode Complete are integrity-protected (and optionally ciphered). This includes PDU Session Establishment Request/Accept. The AMF verifies the NAS MAC before processing the message. This prevents injection of fake PDU session requests.

### Integration Testing Value
This test catches integration gaps between authentication (AUSF/UDM) and session management (SMF/UPF). A UE might authenticate successfully but fail PDU session because: the NAS COUNT is wrong (security context issue), the AMF doesn't forward the SM message to SMF, or the authenticated session-AMBR doesn't match subscription data.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| NAS integrity check fails | PDU request rejected | Verify NAS COUNT synchronization |
| Auth works, PDU fails | No IP allocated | Check SMF/UPF connectivity |
| Security bypass | PDU session without auth | Verify AMF enforces auth before SM |

## References
- 3GPP TS 33.501 V17.x -- Section 6.7.1 (NAS security)
- 3GPP TS 24.501 V17.x -- Section 6.4.1 (PDU session)
- Related: TC-AUTH-001 (auth), TC-PDU-001 (PDU session), TC-REG-001 (registration)

## Quiz Questions
1. Why is the PDU Session Establishment Request integrity-protected using NAS security?
   *Answer: To prevent unauthorized session establishment. Without integrity protection, an attacker could inject a fake PDU Session Request to steal bandwidth, obtain an IP address, or exploit network resources. NAS integrity ensures only authenticated UEs can establish sessions.*

2. What NAS key is used to integrity-protect the PDU Session Establishment Request?
   *Answer: KNASint, derived from KAMF during the Security Mode Command procedure. The MAC is computed over the entire NAS message using the selected EIA algorithm.*

3. If authentication succeeds but PDU session establishment fails, what should you investigate?
   *Answer: (1) NAS COUNT synchronization between UE and AMF, (2) SMF reachability from AMF (Nsmf interface), (3) UPF reachability from SMF (N4/PFCP), (4) IP pool availability on SMF, (5) DNN configuration matching between UE request and SMF config.*

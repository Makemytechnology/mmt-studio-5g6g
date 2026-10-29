# TC-REG-004: UE-Initiated Deregistration

## Overview
This test validates the UE-initiated deregistration procedure, ensuring the UE can cleanly disconnect from the 5G network. It verifies that the AMF properly releases the UE context and that the UE transitions to the DEREGISTERED state. Clean deregistration is essential for orderly network resource management and preventing ghost UE contexts.

## 3GPP Background
UE-initiated deregistration (TS 24.501 Section 5.5.2.2) allows a UE to explicitly inform the network that it no longer needs service. There are two types:

- **Normal deregistration:** UE sends Deregistration Request and waits for Deregistration Accept from AMF. The UE remains powered on but is not registered.
- **Switch-off deregistration:** UE sends Deregistration Request with the "switch off" flag set and does not wait for a response. Used when the UE is powering down.

Upon receiving a Deregistration Request, the AMF:
1. Sends Deregistration Accept to the UE (for normal type only).
2. Releases any active PDU sessions via SM context release to the SMF.
3. Sends NGAP UE Context Release Command to the gNB.
4. Deletes the UE's NAS security context and MM context.

The gNB releases radio resources and sends UE Context Release Complete.

**Network functions involved:** UE, gNB, AMF, SMF (for PDU session release)
**NAS message types:** Deregistration Request (UE->AMF), Deregistration Accept (AMF->UE)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.2.2 | UE-initiated Deregistration |
| TS 24.501 | 5.5.2.3 | Network-initiated Deregistration |
| TS 38.413 | 8.3.1 | UE Context Release (NGAP) |
| TS 23.502 | 4.2.2.3 | Deregistration procedure call flow |
| TS 33.501 | 6.7.3 | NAS security context handling on deregistration |

## Problem Statement
- What if the AMF does not send Deregistration Accept, leaving the UE in an ambiguous state?
- What if the AMF fails to release the UE context on the gNB, causing resource leaks?
- What if active PDU sessions are not properly torn down during deregistration?
- What if the NAS security context is not cleared, allowing replay attacks?
- What if the gNB retains stale UE context entries after deregistration?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. Register UE via full NAS procedure (5G-AKA, Security Mode, Registration Accept).
3. Verify UE is in REGISTERED state.
4. UE sends Deregistration Request (de-registration type: normal or switch-off).
5. AMF processes the request and sends Deregistration Accept.
6. AMF sends NGAP UE Context Release Command to gNB.
7. gNB releases UE context and responds with UE Context Release Complete.
8. UE FSM transitions to DEREGISTERED state.
9. Verify UE is in DEREGISTERED state.

## Expected Behavior
- Deregistration Accept received from AMF within the timeout period.
- NGAP UE Context Release Command sent by AMF to gNB.
- UE FSM cleanly transitions from REGISTERED to DEREGISTERED.
- All NAS security keys are cleared from the UE context.
- AMF deletes the UE's MM context and any associated PDU session contexts.

## Pass/Fail Criteria
- **Pass:** UE reaches DEREGISTERED state after deregistration; AMF releases context cleanly.
- **Fail:** UE does not transition to DEREGISTERED; timeout waiting for Deregistration Accept; context not released.

## Key Concepts for Training

### Deregistration Types
- **Normal (not switch-off):** UE sends Deregistration Request with switch-off flag = 0. The AMF responds with Deregistration Accept. Used when the UE wants to deregister but stay powered on (e.g., airplane mode).
- **Switch-off:** UE sends Deregistration Request with switch-off flag = 1. The AMF does not send Deregistration Accept because the UE is powering down and cannot receive it. The AMF still performs full context cleanup.

### UE Context Release Procedure
The NGAP UE Context Release is a two-step procedure. The AMF sends UE Context Release Command (specifying the cause, e.g., "deregistration"). The gNB releases all associated resources (radio bearers, RAN UE NGAP ID, buffered NAS messages) and responds with UE Context Release Complete. This ensures both sides agree the UE context is removed.

### Impact on PDU Sessions
When a UE deregisters, all its active PDU sessions must be released. The AMF notifies each relevant SMF via Nsmf_PDUSession_ReleaseSMContext. The SMF instructs the UPF to release PFCP sessions and GTP-U tunnels. If this cleanup fails, the UPF retains stale forwarding rules.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| No Deregistration Accept | UE stuck in limbo state | Check AMF NAS message handling |
| UE Context not released | gNB UE count doesn't decrease | Verify AMF sends UE Context Release Command |
| PDU sessions not cleaned | Stale GTP-U tunnels remain | Check SMF context release procedure |
| Security context persists | Potential replay vulnerability | Verify key deletion on deregistration |
| Timer T3521 expiry | Deregistration retransmissions | AMF may be overloaded or not processing |

## References
- 3GPP TS 24.501 V17.x -- Section 5.5.2 (Deregistration procedures)
- 3GPP TS 38.413 V17.x -- Section 8.3 (UE Context Release)
- 3GPP TS 23.502 V17.x -- Section 4.2.2.3 (Deregistration call flow)
- Related: TC-REG-003 (attach/detach cycle), TC-REG-001 (registration), TC-STR-001 (rapid cycles)

## Quiz Questions
1. What is the difference between "normal" and "switch-off" deregistration, and why does the AMF not send Deregistration Accept for switch-off?
   *Answer: In switch-off deregistration, the UE is powering down immediately after sending the Deregistration Request. Since the UE will not be available to receive a response, the AMF skips sending Deregistration Accept and proceeds directly to context cleanup.*

2. What NGAP procedure does the AMF use to instruct the gNB to release UE resources after deregistration?
   *Answer: The UE Context Release procedure. The AMF sends UE Context Release Command, and the gNB responds with UE Context Release Complete after freeing all UE-associated resources.*

3. If a UE deregisters but the AMF fails to notify the SMF, what is the impact on the network?
   *Answer: The SMF retains the PDU session context, the UPF keeps the PFCP session and GTP-U tunnel active, and the UPF continues to buffer/forward packets for a non-existent UE. This wastes UPF resources and may cause IP address pool exhaustion.*

# TC-NGS-007: NG Setup Disconnect and Reconnect

## Overview
This test validates the gNB's ability to disconnect from the AMF and successfully reconnect with a fresh NG Setup. It verifies the complete lifecycle: connect -> READY -> disconnect -> IDLE -> reconnect -> READY. This tests SCTP association restart handling and gNB FSM recovery.

## 3GPP Background
Per TS 38.413 Section 10.6, when an SCTP association is restarted, the NG-C interface is considered reset. Any existing UE contexts associated with the previous association are lost. The gNB must perform a fresh NG Setup procedure before sending any other NGAP messages.

This is important for resilience scenarios: network glitches, AMF restarts, or planned maintenance may cause SCTP disconnections. The gNB must detect the disconnection, return to IDLE state, and re-establish the NG-C interface when connectivity is restored.

After disconnection, any UEs that were registered via the previous association lose their contexts. The AMF may initiate cleanup, or the stale contexts may time out.

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup procedure |
| TS 38.413 | 10.6 | SCTP association restart |
| TS 38.412 | 7 | SCTP transport lifecycle |
| RFC 4960 | 5 | SCTP association management |

## Problem Statement
- What if the gNB does not return to IDLE state after disconnection?
- What if the AMF retains stale gNB context from the previous association?
- What if the reconnect NG Setup is rejected because the AMF thinks the gNB is still connected?
- What if SCTP TIME_WAIT prevents immediate reconnection?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration.
2. First connection: connect SCTP, NG Setup, verify READY.
3. Disconnect: close SCTP association.
4. Verify state returns to IDLE.
5. Wait 500ms for cleanup.
6. Reconnect: new SCTP association, fresh NG Setup, verify READY.
7. Teardown: remove gNB.

## Expected Behavior
- First NG Setup succeeds, gNB READY.
- Disconnect returns gNB to IDLE.
- Reconnect with fresh NG Setup succeeds.
- Second READY state achieved.

## Pass/Fail Criteria
- **Pass:** Both initial and reconnect NG Setups succeed; state transitions correct.
- **Fail:** Reconnect fails; state not IDLE after disconnect; SCTP reconnect rejected.

## Key Concepts for Training

### SCTP Association Restart
When an SCTP association is closed and a new one is established to the same peer, this is called an "association restart." Per TS 38.413 Section 10.6, an SCTP restart erases all NG interface state. The gNB must re-initialize by sending a fresh NGSetupRequest. The AMF treats the new association as a completely new gNB connection.

### Resilience and Recovery
In production networks, SCTP disconnections happen due to: network path failures (link down, routing change), AMF restarts (software upgrade, crash), scheduled maintenance, or firewall state expiry. The gNB must handle these gracefully: detect disconnect, clean up local state, attempt reconnection with backoff.

### State Cleanup on Disconnect
When the gNB detects SCTP disconnection, it must: (1) transition FSM to IDLE, (2) cancel all pending NAS timers, (3) release all UE contexts (they cannot be used without an NG association), (4) prepare for fresh NG Setup on reconnection.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| State stuck in READY | FSM doesn't detect disconnect | Implement SCTP notification handler |
| SCTP TIME_WAIT | Reconnect fails immediately | Wait for TIME_WAIT expiry or use SO_REUSEADDR |
| AMF stale gNB context | Reconnect rejected | AMF should accept fresh NG Setup |
| UE contexts not cleaned | Ghost UEs after reconnect | Clear UE table on disconnect |

## References
- 3GPP TS 38.413 V17.x -- Section 10.6 (SCTP restart)
- RFC 4960 -- Section 5 (SCTP association management)
- Related: TC-NGS-008 (3 reconnect cycles), TC-NGS-014 (context replacement), TC-NGS-002 (FSM)

## Quiz Questions
1. After an SCTP association restart, why must the gNB perform a fresh NG Setup?
   *Answer: Per TS 38.413 Section 10.6, SCTP restart erases all NG interface state. The AMF considers the previous association and all its UE contexts invalid. A fresh NG Setup re-initializes the interface and allows the AMF to re-learn the gNB's capabilities.*

2. What should happen to UE contexts on the gNB when the SCTP association is lost?
   *Answer: All UE contexts must be released. The UEs can no longer communicate through this gNB without an active NG association. The gNB should clear its UE context table, free RAN UE NGAP IDs, and release any associated radio resources.*

3. Why is a 500ms pause inserted between disconnect and reconnect?
   *Answer: To allow: (1) SCTP TIME_WAIT state to expire or socket cleanup, (2) AMF to process the disconnect and clean up the gNB context, (3) any in-flight NGAP messages to drain, (4) OS to release network resources (ports, sockets).*

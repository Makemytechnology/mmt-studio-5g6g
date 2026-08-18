# TC-NGS-014: NG Setup Context Replacement

## Overview
This test verifies that when a gNB disconnects and reconnects, all previous UE context is cleared. After reconnection and fresh NG Setup, the gNB's UE count should be 0 -- no stale UE associations from the previous connection should persist.

## 3GPP Background
Per TS 38.413 Section 8.7.1.1, if a gNB has already completed NG Setup, it should not initiate another NG Setup on the same association. Instead, it should use the NG Configuration Update procedure. However, on SCTP restart (disconnect + reconnect), the previous context is erased and a fresh NG Setup is required.

This means: after disconnect, all UE contexts associated with the previous SCTP association are invalidated. The AMF also clears UE contexts linked to the lost gNB association. On fresh NG Setup, the gNB starts clean with UE count = 0.

This test validates the cleanup behavior by: (1) performing initial NG Setup, (2) noting the UE count (0), (3) disconnecting, (4) reconnecting with fresh NG Setup, (5) verifying UE count is still 0 (no stale context).

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1.1 | NG Setup rules and restrictions |
| TS 38.413 | 10.6 | SCTP restart and context reset |
| TS 38.413 | 8.7.2 | NG Configuration Update |

## Problem Statement
- What if stale UE contexts persist after reconnection?
- What if the gNB retains RAN UE NGAP IDs from the previous session?
- What if the AMF still routes messages for old UEs to the reconnected gNB?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, NG Setup, verify READY.
2. Record UE count after first setup.
3. Disconnect SCTP, wait 500ms.
4. Reconnect SCTP, fresh NG Setup, verify READY.
5. Verify UE count = 0 after re-setup (context replaced).
6. Teardown: remove gNB.

## Expected Behavior
- First NG Setup: UE count = 0.
- After disconnect and re-setup: UE count = 0 (no stale contexts).
- gNB reaches READY in both connections.

## Pass/Fail Criteria
- **Pass:** UE count = 0 after re-setup; both NG Setups succeed.
- **Fail:** UE count > 0 after re-setup (stale contexts); NG Setup fails.

## Key Concepts for Training

### Context Replacement vs. Context Update
- **Context Replacement** (this test): SCTP restarts -> all context erased -> fresh NG Setup. Used for: gNB reboot, network failure recovery, AMF failover.
- **Context Update** (NG Configuration Update): Same SCTP association -> modify gNB parameters (add/remove TAs, update slices). Does NOT erase UE contexts.

### Stale Context Risks
If stale UE contexts persist after reconnection: (1) the AMF may try to send messages for non-existent UEs, (2) RAN UE NGAP IDs may collide with newly assigned IDs, (3) NAS security contexts for old UEs may be applied to new UEs, (4) billing records may be corrupted.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| UE count > 0 after re-setup | Stale context detected | Fix context cleanup on disconnect |
| Old NGAP IDs in use | ID collision on new UEs | Clear ID table on disconnect |
| AMF routes to old gNB | Messages sent to wrong gNB | AMF should clear gNB context on SCTP loss |

## References
- 3GPP TS 38.413 V17.x -- Section 8.7.1.1 (NG Setup restrictions)
- 3GPP TS 38.413 V17.x -- Section 10.6 (SCTP restart)
- Related: TC-NGS-007 (disconnect/reconnect), TC-NGS-008 (3 cycles)

## Quiz Questions
1. Why should a gNB not send a second NGSetupRequest on the same SCTP association?
   *Answer: Per TS 38.413 Section 8.7.1.1, once NG Setup succeeds, modifications should use the NG Configuration Update procedure. Sending another NG Setup on the same association would confuse the AMF (is it a reset? a reconfiguration?) and violate the protocol.*

2. After SCTP restart, why must the UE count be 0?
   *Answer: SCTP restart erases all NG interface state. UE contexts are bound to the SCTP association -- when it is lost, UEs can no longer signal through that gNB. The gNB must clear all UE contexts, and the fresh NG Setup starts with a clean slate.*

3. What is the difference between NG Setup and NG Configuration Update?
   *Answer: NG Setup initializes the NG interface from scratch (after SCTP establishment). NG Configuration Update modifies parameters on an already-established interface (add/remove TAs, update slices, change capacity). NG Setup erases all context; NG Configuration Update preserves existing UE contexts.*

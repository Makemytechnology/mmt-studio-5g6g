# TC-NGS-002: gNB State Machine Validation (IDLE -> NG_SETUP_SENT -> READY)

## Overview
This test explicitly validates each state transition in the gNB's finite state machine during NG Setup. It verifies that the gNB starts in IDLE, transitions to NG_SETUP_SENT when connecting, and reaches READY upon receiving the NGSetupResponse. This ensures the FSM implementation correctly tracks the NG interface lifecycle.

## 3GPP Background
The gNB's NG-C interface has a well-defined lifecycle described in TS 38.413 Section 8.7.1. The state machine enforces that signaling procedures occur in the correct order:

**IDLE:** No SCTP association exists. The gNB cannot send or receive any NGAP messages. This is the initial state after creation and the state after disconnection.

**NG_SETUP_SENT:** The gNB has established an SCTP association and sent the NGSetupRequest. It is waiting for the AMF's response. During this state, the gNB must not send any other NGAP messages. If no response arrives within a timeout, the gNB may retransmit or abort.

**READY:** The NGSetupResponse has been received and processed. The NG-C interface is fully operational. The gNB can now process UE-associated signaling (InitialUEMessage, DownlinkNASTransport, etc.) and non-UE signaling (Paging, Reset, ErrorIndication).

Per the spec (Figure 8.7.1.1-1), the NG Setup is a single request-response exchange. There is no intermediate negotiation or challenge.

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup procedure |
| TS 38.413 | Figure 8.7.1.1-1 | NG Setup message flow |
| TS 38.412 | 7 | SCTP association lifecycle |

## Problem Statement
- What if the gNB allows UE signaling before reaching READY state?
- What if the gNB skips the NG_SETUP_SENT state (direct IDLE->READY)?
- What if the gNB does not return to IDLE on SCTP disconnection?
- What if a timeout in NG_SETUP_SENT leaves the gNB in a broken state?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration.
2. Verify initial state is IDLE.
3. Initiate SCTP connection (connect gNB). This sends NGSetupRequest.
4. Wait for READY state (NGSetupResponse received).
5. Verify final state is READY.
6. Teardown: remove gNB.

## Expected Behavior
- Initial state: IDLE (no SCTP, no NGAP).
- After connect: transitions through NG_SETUP_SENT.
- After response: final state READY.
- State transitions occur in order: IDLE -> NG_SETUP_SENT -> READY.

## Pass/Fail Criteria
- **Pass:** Initial state = IDLE; final state = READY; transitions in correct order.
- **Fail:** Initial state not IDLE; final state not READY; unexpected state observed.

## Key Concepts for Training

### Finite State Machine (FSM) Design
The gNB FSM ensures protocol correctness. Each state defines which NGAP messages can be sent/received. In IDLE, nothing is allowed. In NG_SETUP_SENT, only NGSetupResponse/Failure is expected. In READY, the full NGAP procedure set is available. This state-driven design prevents out-of-sequence signaling.

### State Transition Guards
Each transition has a guard condition: IDLE->NG_SETUP_SENT requires successful SCTP association. NG_SETUP_SENT->READY requires receiving NGSetupResponse. NG_SETUP_SENT->IDLE occurs on timeout or NGSetupFailure. READY->IDLE occurs on SCTP disconnection. These guards prevent invalid state transitions.

### FSM Testing Methodology
FSM testing verifies: (1) all valid transitions occur correctly, (2) invalid transitions are rejected, (3) the FSM handles error conditions (timeout, failure) gracefully, (4) the FSM resets cleanly (returns to initial state). This test covers the happy path; negative tests (TC-NGS-016 for idle SCTP) cover error paths.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Initial state not IDLE | Assert fails on first check | Verify gNB constructor initializes state |
| Skipped NG_SETUP_SENT | Direct IDLE to READY | FSM implementation missing intermediate state |
| Stuck in NG_SETUP_SENT | Never reaches READY | AMF not responding; check SCTP connectivity |
| READY without response | State set prematurely | FSM must wait for NGSetupResponse |

## References
- 3GPP TS 38.413 V17.x -- Section 8.7.1 (NG Setup), Figure 8.7.1.1-1
- 3GPP TS 38.412 V17.x -- Section 7 (SCTP lifecycle)
- Related: TC-NGS-001 (happy path), TC-NGS-007 (disconnect/reconnect), TC-NGS-014 (context replacement)

## Quiz Questions
1. What three states does the gNB FSM have for the NG-C interface, and what triggers each transition?
   *Answer: IDLE (initial; entered on disconnect/creation), NG_SETUP_SENT (entered when NGSetupRequest is sent after SCTP connect), READY (entered when NGSetupResponse is received). Reverse: READY->IDLE on SCTP disconnect, NG_SETUP_SENT->IDLE on timeout/failure.*

2. Why must the gNB not send InitialUEMessage while in NG_SETUP_SENT state?
   *Answer: The NG interface is not yet established. The AMF has not confirmed it accepts the gNB's configuration (PLMN, TAC, slices). Sending UE signaling before the AMF acknowledges the gNB would result in the AMF dropping the message or sending an ErrorIndication.*

3. What should the gNB do if it receives NGSetupFailure instead of NGSetupResponse?
   *Answer: Transition back to IDLE state. If the Failure contains a TimeToWait IE, the gNB should wait that duration before retrying. The cause IE indicates why the setup was rejected (e.g., overloaded, unknown PLMN), which should be logged for operator action.*

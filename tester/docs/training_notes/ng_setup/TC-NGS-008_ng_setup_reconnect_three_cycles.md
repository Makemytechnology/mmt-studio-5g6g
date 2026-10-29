# TC-NGS-008: NG Setup Resilience -- 3 Reconnect Cycles

## Overview
This test performs 3 consecutive disconnect/reconnect cycles, verifying that the gNB can reliably re-establish the NG-C interface multiple times. It stresses SCTP socket management, AMF connection handling, and gNB FSM reset logic across repeated cycles.

## 3GPP Background
In production, gNBs may experience multiple disconnections (e.g., intermittent network issues, rolling AMF upgrades). Each cycle must: close the SCTP association -> return to IDLE -> establish new SCTP association -> perform fresh NG Setup -> reach READY. The AMF must handle the same gNB reconnecting multiple times.

Per TS 38.413 Section 10.6, each reconnection is treated as a fresh gNB. The AMF discards any state from the previous association.

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup procedure |
| TS 38.413 | 10.6 | SCTP restart handling |
| TS 38.412 | 7 | SCTP transport |

## Problem Statement
- What if SCTP socket resources leak on each disconnect/reconnect?
- What if the AMF accumulates stale gNB entries from repeated connections?
- What if the gNB FSM does not cleanly reset between cycles?
- What if SCTP ephemeral port exhaustion occurs after 3 cycles?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration.
2. For each cycle (1 to 3):
   a. Connect SCTP, perform NG Setup, verify READY.
   b. Disconnect SCTP.
   c. Verify state is IDLE.
   d. Pause 500ms.
3. All 3 cycles pass.
4. Teardown: remove gNB.

## Expected Behavior
- All 3 connect/disconnect cycles complete successfully.
- gNB transitions IDLE -> READY -> IDLE in each cycle.
- No resource leaks across cycles.

## Pass/Fail Criteria
- **Pass:** All 3 cycles succeed with correct state transitions.
- **Fail:** Any cycle fails; state transition incorrect.

## Key Concepts for Training

### Connection Resilience Testing
Resilience tests verify that a system can recover from failures repeatedly. A system that recovers once but fails on the second or third attempt has a resource leak or state corruption bug. Three cycles is the minimum to detect such patterns (first cycle succeeds, second cycle reveals the leak, third cycle confirms it).

### SCTP Socket Lifecycle
Each SCTP association uses kernel resources: socket file descriptor, send/receive buffers, association state, stream state. On disconnect, these must be freed. On reconnect, new resources are allocated. If the old socket is not properly closed (e.g., linger timeout, reference count > 0), the kernel retains the resources, eventually causing exhaustion.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Socket leak | "Too many open files" on cycle 3 | Properly close SCTP socket on disconnect |
| Port exhaustion | Connection refused on reconnect | Use SO_REUSEADDR, wait for TIME_WAIT |
| AMF stale context | NGSetupFailure on reconnect | AMF should clean up on SCTP close |
| FSM not reset | State inconsistency on cycle 2 | Verify FSM reset to IDLE on disconnect |

## References
- 3GPP TS 38.413 V17.x -- Section 10.6 (SCTP restart)
- RFC 4960 -- SCTP association management
- Related: TC-NGS-007 (single reconnect), TC-NGS-014 (context replacement)

## Quiz Questions
1. Why are 3 reconnect cycles better than 1 for detecting resource leaks?
   *Answer: A single reconnect might succeed even with a leak because sufficient resources remain. By cycle 3, accumulated leaks (sockets, memory, timer references) may push the system past its limits, revealing bugs that a single cycle hides.*

2. What SCTP socket options help with rapid reconnection?
   *Answer: SO_REUSEADDR allows binding to the same local port immediately. SO_LINGER with timeout=0 forces immediate socket close (no TIME_WAIT). SCTP_NODELAY reduces reconnection latency.*

3. If cycle 1 and 2 succeed but cycle 3 fails with "Connection refused," what should you investigate?
   *Answer: (1) AMF may have a connection rate limiter, (2) SCTP TIME_WAIT ports accumulating, (3) AMF max gNB connection limit, (4) AMF may blacklist rapidly reconnecting gNBs, (5) Check AMF logs for error messages about the gNB.*

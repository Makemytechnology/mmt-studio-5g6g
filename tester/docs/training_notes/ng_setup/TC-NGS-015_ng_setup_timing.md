# TC-NGS-015: NG Setup Latency Measurement

## Overview
This test measures the total latency of the SCTP + NG Setup procedure, from connection initiation to READY state. It provides a performance baseline for the NG-C interface establishment. Typical local network latency should be under 100ms; the test fails if it exceeds 5000ms.

## 3GPP Background
The total NG Setup latency includes two phases:

**Phase 1 -- SCTP 4-way handshake (RFC 4960 Section 5):**
Client -> INIT -> Server -> INIT-ACK -> Client -> COOKIE-ECHO -> Server -> COOKIE-ACK
Typical latency: 2x RTT (two round-trips).

**Phase 2 -- NG Setup exchange:**
gNB -> NGSetupRequest -> AMF -> NGSetupResponse -> gNB
Typical latency: 1x RTT + AMF processing time.

Total: approximately 3x RTT + AMF processing. On a local network (RTT < 1ms), total should be under 10ms. Over WAN (RTT = 20ms), total would be ~60-80ms.

The 5000ms threshold is generous -- it catches severe issues (AMF overloaded, DNS resolution delays, SCTP retransmission) while not failing on moderate WAN deployments.

**Network functions involved:** gNB, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup procedure |
| RFC 4960 | 5 | SCTP 4-way handshake |
| TS 38.412 | 7 | SCTP transport |

## Problem Statement
- What if AMF processing adds unexpected delay (database lookup, policy check)?
- What if DNS resolution for AMF hostname adds latency?
- What if SCTP retransmission due to packet loss inflates timing?
- What if the AMF is CPU-bound and processes NG Setup slowly?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration.
2. Record start time (epoch).
3. Connect SCTP and perform NG Setup (wait for READY).
4. Record end time (epoch).
5. Calculate elapsed time in milliseconds.
6. Verify gNB is READY.
7. Assert elapsed < 5000ms.
8. Log the latency measurement.
9. Teardown: remove gNB.

## Expected Behavior
- NG Setup completes in < 100ms on local network.
- Total latency < 5000ms even on degraded networks.
- gNB reaches READY state.

## Pass/Fail Criteria
- **Pass:** gNB READY; elapsed < 5000ms.
- **Fail:** Timeout; elapsed >= 5000ms.

## Key Concepts for Training

### Latency Budgeting
The NG Setup latency can be decomposed: SCTP handshake (2x RTT) + NG Setup exchange (1x RTT + processing). Understanding where time is spent helps diagnose performance issues. If SCTP handshake takes 500ms on a 1ms RTT network, SCTP is retransmitting (packet loss). If SCTP is fast but NG Setup is slow, the AMF processing is the bottleneck.

### SCTP Retransmission Impact
If the INIT packet is lost, SCTP retransmits after RTO (Retransmission Timeout, default 1-3 seconds). A single retransmission adds 1-3 seconds to the total latency. Multiple losses compound. SCTP's initial RTO is configurable (sysctl net.sctp.rto_initial).

### Performance Baselining
This test establishes a performance baseline. Subsequent tests (after software updates, configuration changes, or infrastructure modifications) can compare against this baseline to detect regressions. A 10x increase in NG Setup latency often indicates a systemic issue.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| High latency (> 1s) | SCTP retransmission | Check network for packet loss |
| Very high latency (> 5s) | Test fails | AMF overloaded or unreachable |
| Inconsistent timing | Varies widely between runs | Network jitter or AMF GC pauses |
| DNS resolution delay | 500ms+ added to first connect | Use IP address instead of hostname |

## References
- RFC 4960 -- SCTP (Section 5, association setup)
- 3GPP TS 38.413 V17.x -- Section 8.7.1 (NG Setup)
- Related: TC-NGS-001 (basic NG Setup), TC-NGS-012 (SCTP port)

## Quiz Questions
1. What is the theoretical minimum NG Setup latency on a network with 1ms RTT?
   *Answer: Approximately 3ms (3x RTT): 2x RTT for SCTP 4-way handshake + 1x RTT for NGSetupRequest/Response. Plus AMF processing time (typically < 1ms). Total: ~3-4ms.*

2. If NG Setup takes 3200ms but the network RTT is only 5ms, what is the most likely cause?
   *Answer: SCTP retransmission. The SCTP INIT packet was likely lost, triggering a retransmission after the initial RTO (typically 1-3 seconds). The retransmitted INIT succeeded, adding ~3 seconds to the total. Check for packet loss on the network path.*

3. Why is the threshold set at 5000ms rather than a tighter value like 100ms?
   *Answer: The test must work across diverse environments: local lab (< 10ms), remote lab (< 100ms), WAN deployments (< 500ms), and degraded conditions (< 5000ms). A tight threshold would cause false failures in non-local environments. The 5000ms threshold catches only severe issues (AMF down, persistent packet loss, SCTP retransmission storms).*

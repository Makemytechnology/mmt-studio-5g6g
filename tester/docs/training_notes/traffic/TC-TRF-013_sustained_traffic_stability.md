# TC-TRF-013: Sustained TCP Traffic Stability Test

## Overview
This test validates data-plane stability over an extended duration (30 seconds). It runs sustained TCP traffic through the GTP-U tunnel and monitors for throughput consistency, tunnel drops, and TCP retransmits. Stability testing catches issues that only appear under sustained load: buffer overflows, memory leaks, timer issues, and tunnel state corruption.

## 3GPP Background
In production, PDU sessions carry traffic for hours or days continuously. The GTP-U tunnel must remain stable: no TEID changes, no PFCP session modifications, no packet corruption. The UPF's forwarding rules must remain consistent.

Sustained TCP traffic tests: (1) GTP-U tunnel stability (no resets or state changes), (2) UPF forwarding consistency (no rule changes), (3) TUN interface stability (no interface flaps), (4) TCP path health (no retransmits indicating packet loss).

**Network functions involved:** UE, gNB, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 29.281 | 4 | GTP-U tunnel stability |
| TS 23.501 | 5.7 | QoS model (sustained) |

## Problem Statement
- What if the GTP-U tunnel drops after 10 seconds of sustained traffic?
- What if throughput degrades over time (indicating buffer or memory issue)?
- What if TCP retransmits increase over time (indicating progressive packet loss)?
- What if the TUN interface goes down during sustained traffic?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish internet PDU session.
2. Start iperf3 server on SA Core.
3. Run iperf3 TCP client for 30 seconds (sustained).
4. Monitor throughput stability over full duration.
5. Check for TCP retransmits.
6. Verify GTP-U tunnel remains active throughout.

## Expected Behavior
- Stable throughput for full 30 seconds.
- 0 TCP retransmits (clean path).
- GTP-U tunnel remains up throughout.
- No throughput degradation over time.

## Pass/Fail Criteria
- **Pass:** Stable throughput for 30s; 0 retransmits; tunnel stays up.
- **Fail:** Throughput drops; retransmits > 0; tunnel resets.

## Key Concepts for Training

### Stability vs Performance Testing
Performance tests measure maximum throughput (how fast). Stability tests measure consistency over time (how reliable). A system that achieves 1 Gbps for 5 seconds but drops to 0 at second 10 has a performance score of 1 Gbps but a stability score of zero. Production networks need both: high performance AND high stability.

### TCP Retransmits as Path Health Indicator
TCP retransmits occur when packets are lost. The sender detects loss via timeout or duplicate ACKs and retransmits the missing data. Zero retransmits over 30 seconds indicates a clean data path. Increasing retransmits over time suggest progressive degradation (e.g., buffer exhaustion, GTP-U state corruption).

### GTP-U Tunnel Lifetime
A GTP-U tunnel is created during PDU session establishment and should persist until PDU session release. There should be no reason for the tunnel to drop during normal operation. Tunnel drops indicate: PFCP session timeout, SCTP disconnection (if UE context is released), or implementation bugs.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Throughput drops to 0 | Tunnel reset at 15s | Check GTP-U tunnel state, PFCP timers |
| Progressive degradation | Throughput decreases over time | Memory leak or buffer fragmentation |
| Retransmits increasing | TCP retransmits grow | Packet loss in GTP-U path |
| TUN interface flap | Interface goes down/up | Check kernel TUN stability |

## References
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- Related: TC-TRF-001 (short TCP), TC-TRF-003 (bidirectional), TC-STR-011 (multi-UE)

## Quiz Questions
1. Why is 30 seconds chosen as the sustained traffic duration?
   *Answer: 30 seconds is long enough to: (1) expose buffer overflow issues (buffers fill in seconds), (2) trigger timer-based bugs (common timer intervals are 10-30s), (3) stress memory management (allocations accumulate), but short enough to run in a reasonable test time.*

2. What does a TCP retransmit count of 0 indicate about the GTP-U data path?
   *Answer: The data path is completely lossless: no packets were dropped, corrupted, or reordered. Every packet sent through the GTP-U tunnel arrived intact at the destination. This is the ideal result for a controlled test environment.*

3. If throughput is stable at 800 Mbps for 25 seconds then suddenly drops to 0, what are the top three causes?
   *Answer: (1) GTP-U tunnel dropped (PFCP session timeout or SCTP disconnection), (2) TUN interface went down (kernel module issue or file descriptor closed), (3) UPF crashed or restarted (losing all PFCP sessions). Check: GTP-U tunnel state, TUN interface status, UPF process health.*

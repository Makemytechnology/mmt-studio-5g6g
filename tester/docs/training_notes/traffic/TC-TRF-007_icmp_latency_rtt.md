# TC-TRF-007: RTT Latency Measurement via GTP-U Tunnel

## Overview
This test measures round-trip time (RTT) latency through the GTP-U tunnel using ICMP echo requests (ping). RTT is a fundamental network health metric that validates the data path end-to-end. Low RTT is essential for interactive applications and is defined by the PDB (Packet Delay Budget) per 5QI.

## 3GPP Background
ICMP echo requests from the UE traverse the full data path: UE IP -> TUN -> GTP-U encapsulation -> UPF -> gateway IP -> ICMP reply -> UPF -> GTP-U -> TUN -> UE. The RTT includes: TUN processing (2x), GTP-U encapsulation/decapsulation (2x), UDP transport (2x), and UPF forwarding (2x).

For 5QI=9 (default internet bearer), the PDB is 300ms one-way. RTT should be approximately 2x one-way delay. On a local network, RTT through GTP-U should be < 10ms, well within the 600ms round-trip budget.

The ping target is typically the UPF gateway (derived from the UE subnet: if UE IP is 10.45.0.X, gateway is 10.45.0.1).

**Network functions involved:** UE, gNB, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7.3.4 | Packet Delay Budget per 5QI |
| TS 29.281 | 4 | GTP-U data plane |
| TS 23.501 | 5.7.2.1 | 5QI=9 PDB=300ms |

## Problem Statement
- What if RTT exceeds the PDB budget for the assigned 5QI?
- What if ICMP packets are blocked by the UPF or firewall?
- What if the UPF gateway does not respond to ping?
- What if jitter causes high RTT variance?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish internet PDU session.
2. Send 20 ICMP echo requests from UE IP through GTP-U tunnel.
3. Target: UPF gateway IP (e.g., 10.45.0.1).
4. Timeout: 2 seconds per ping.
5. Parse results: min/avg/max RTT, packet loss.

## Expected Behavior
- avg RTT < 100ms on local network (typically < 5ms).
- 0% packet loss (all 20 pings succeed).
- RTT well within 5QI=9 PDB budget of 300ms.

## Pass/Fail Criteria
- **Pass:** avg RTT < 100ms; 0% packet loss.
- **Fail:** High RTT; packet loss; ping timeout.

## Key Concepts for Training

### Packet Delay Budget (PDB)
PDB defines the maximum one-way delay budget between the UE and the UPF (N3/N9 interface). For 5QI=9: PDB=300ms. For 5QI=1 (voice): PDB=100ms. RTT is approximately 2x one-way delay. Ping measures RTT, so acceptable RTT is approximately 2x PDB.

### RTT Components Through GTP-U
RTT = 2 * (TUN processing + GTP-U encapsulation + UDP transport + UPF forwarding + IP routing). On a local network, each component adds < 1ms. Over WAN, the transport component dominates. High RTT with low physical network latency indicates processing delays in the GTP-U or UPF stack.

### Ping as Data Path Validation
Ping is the simplest data path test. If ping works, the complete chain is operational: TUN interface, GTP-U encapsulation, UDP transport, UPF PDR matching, UPF forwarding, and return path. If ping fails, it provides diagnostic information: "destination unreachable" (routing issue), "request timeout" (packet dropped), or "host unreachable" (gateway not configured).

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Ping timeout | 0 responses | Check GTP-U tunnel, UPF gateway config |
| High RTT | > 100ms on local network | Check GTP-U processing latency |
| Packet loss | Some pings lost | Check UPF ICMP handling, QoS |
| ICMP blocked | "Destination unreachable" | Verify UPF allows ICMP forwarding |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7.3.4 (PDB)
- Related: TC-TRF-001 (TCP), TC-TRF-004 (UDP), TC-IMS-005 (voice latency)

## Quiz Questions
1. What is the relationship between RTT (measured by ping) and the 5QI PDB (Packet Delay Budget)?
   *Answer: PDB defines maximum one-way delay. RTT = approximately 2x one-way delay. For 5QI=9 (PDB=300ms), acceptable RTT is approximately 600ms. RTT significantly above 2x PDB indicates the data path violates QoS requirements.*

2. If ping to the UPF gateway succeeds but ping to an external IP fails, what does this indicate?
   *Answer: The GTP-U tunnel and UPF are working (UPF gateway is reachable). The failure is beyond the UPF -- the UPF's N6 interface to the Data Network is the issue: DNS resolution, default route, NAT configuration, or external firewall.*

3. What does a min/avg/max RTT of 1.2/3.5/45.0 ms tell you about the data path?
   *Answer: The typical latency is very low (avg 3.5ms). The max spike to 45ms suggests occasional processing delay (likely OS scheduling jitter, GC pause, or buffer flush). The path is healthy overall, but the outlier could affect latency-sensitive applications. Check for CPU contention or interrupt coalescing.*

# TC-TRF-001: TCP Uplink Throughput Test

## Overview
This test measures TCP uplink throughput through the GTP-U tunnel from the UE to the core network. It validates the complete data-plane path: UE application -> TUN interface -> GTP-U encapsulation -> UDP:2152 -> UPF -> Data Network. This is the fundamental data throughput test.

## 3GPP Background
User-plane data in 5G flows through GTP-U tunnels on the N3 interface (gNB-UPF). Each PDU session has a dedicated GTP-U tunnel identified by TEIDs. Uplink data from the UE is encapsulated with a GTP-U header (containing the uplink TEID) and sent as UDP datagrams to port 2152 on the UPF.

The UPF decapsulates the GTP-U header, matches the TEID against the uplink PDR (Packet Detection Rule), and applies the associated FAR (Forwarding Action Rule) to forward the packet to the Data Network (N6 interface). QoS enforcement (Session-AMBR, per-flow MBR) is applied by the QER (QoS Enforcement Rule).

The default QoS flow uses 5QI=9: non-GBR, best effort, PDB=300ms. TCP throughput is limited by: network bandwidth, GTP-U overhead (~36 bytes per packet), UPF processing speed, and Session-AMBR enforcement.

**Traffic path:** UE App -> TUN -> GTP-U encap -> UDP:2152 -> UPF -> iperf3 server
**Network functions involved:** UE, gNB (tester), UPF, iperf3 server

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 29.281 | 4 | GTP-U protocol |
| TS 23.501 | 5.7 | QoS model |
| TS 23.501 | 5.7.2.1 | 5QI=9 characteristics |
| TS 23.501 | 5.7.2.6 | Session-AMBR |

## Problem Statement
- What if GTP-U encapsulation reduces effective throughput significantly?
- What if the UPF drops packets due to QER enforcement?
- What if the TUN interface MTU is too small, causing fragmentation?
- What if iperf3 cannot bind to the UE IP (SO_BINDTODEVICE issue)?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish internet PDU session.
2. GTP-U tunnel active: TUN interface with UE IP assigned.
3. Start iperf3 server on SA Core network via API.
4. Run iperf3 TCP client on UE bound to UE IP through TUN interface.
5. Traffic flows through GTP-U tunnel for 10 seconds.
6. Collect UPF statistics (ul_pkts, ul_bytes, ul_dropped).
7. Report throughput results.

## Expected Behavior
- iperf3 TCP stream flows through GTP-U tunnel.
- UPF io-stats shows uplink bytes received.
- Throughput > 50 Mbps on local network.
- Zero UPF drops (ul_dropped = 0).
- Zero TCP retransmits (clean data path).

## Pass/Fail Criteria
- **Pass:** Throughput > 0 Mbps; UPF receives uplink data; zero drops.
- **Fail:** No throughput; UPF drops; iperf3 connection failure.

## Key Concepts for Training

### GTP-U Overhead
Each user packet is encapsulated with: IP header (20 bytes) + UDP header (8 bytes) + GTP-U header (8 bytes minimum, 12 with extension) = 36-40 bytes overhead. For a 1500-byte MTU, the effective payload is ~1460 bytes (2.7% overhead). For small packets (64 bytes VoIP), overhead is 56% -- significant.

### Session-AMBR Enforcement
The Session-AMBR (Aggregate Maximum Bit Rate) limits total throughput per PDU session. The SMF configures a QER on the UPF with the AMBR values from the UE's subscription. If the UE sends above the AMBR, the UPF drops or shapes excess packets. UPF io-stats shows ul_dropped or ul_metered when AMBR is enforced.

### UPF IO Statistics
UPF io-stats provide per-session metrics: ul_pkts (uplink packet count), ul_bytes (uplink byte count), ul_dropped (packets dropped by QoS), dl_pkts/dl_bytes/dl_dropped (downlink equivalents). These counters validate that traffic actually traversed the UPF, not just the local TUN interface.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| No throughput | iperf3 connection timeout | Check GTP-U tunnel is active, routing correct |
| Low throughput | < 10 Mbps on GbE | Check MTU, GTP-U encap overhead, CPU |
| High drops | UPF ul_dropped > 0 | Check Session-AMBR, UPF capacity |
| SO_BINDTODEVICE error | iperf3 can't bind to TUN | Run with root privileges, check TUN name |
| Retransmits | TCP retransmissions > 0 | Network loss in GTP-U path |

## References
- 3GPP TS 29.281 V17.x -- Section 4 (GTP-U)
- 3GPP TS 23.501 V17.x -- Section 5.7 (QoS model)
- Related: TC-TRF-002 (downlink), TC-TRF-003 (bidirectional), TC-TRF-004 (UDP)

## Quiz Questions
1. What is the GTP-U encapsulation overhead per packet, and how does it affect throughput?
   *Answer: 36-40 bytes (IP=20 + UDP=8 + GTP-U=8-12). On a 1 Gbps link with 1500-byte MTU, effective throughput is ~973 Mbps. For small packets, overhead is proportionally larger.*

2. What UPF counter confirms that uplink traffic actually traversed the core network?
   *Answer: ul_bytes and ul_pkts in the UPF io-stats. These counters are incremented when the UPF receives and processes GTP-U encapsulated uplink packets. If these are 0, traffic never reached the UPF.*

3. Why must iperf3 bind to the UE IP address (via SO_BINDTODEVICE) rather than any interface?
   *Answer: The tester host has multiple interfaces: the management interface and multiple TUN interfaces for UEs. Without explicit binding, iperf3 traffic may use the management interface instead of the GTP-U tunnel, bypassing the data plane entirely and giving invalid results.*

# TC-TRF-004: UDP Uplink Throughput with QoS Metrics

## Overview
This test measures UDP uplink throughput along with jitter (inter-packet delay variation) and packet loss through the GTP-U tunnel. UDP testing reveals QoS characteristics that TCP masks with retransmission. This is critical for evaluating voice/video service quality.

## 3GPP Background
UDP traffic is essential for real-time services (VoIP, video). Unlike TCP, UDP does not retransmit lost packets, so packet loss and jitter directly impact service quality. The 5QI framework (TS 23.501 Section 5.7.2.1) defines per-flow characteristics:

- **5QI=9** (default internet): non-GBR, PDB=300ms, PER=10^-6
- **5QI=1** (voice): GBR, PDB=100ms, PER=10^-2
- **5QI=2** (video): GBR, PDB=150ms, PER=10^-3

The iperf3 UDP test sends at a target bandwidth (50 Mbps) and measures: actual throughput, jitter (running average of inter-packet delay variation per RFC 3550), and packet loss percentage.

The UPF's QER applies rate limiting. For best-effort traffic (5QI=9), the Session-AMBR is the rate limit. Packets exceeding the AMBR may be dropped (ul_dropped) or marked/metered (ul_metered).

**Traffic path:** UE -> TUN -> GTP-U -> UPF -> iperf3 server

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7.2.1 | 5QI to QoS characteristics |
| TS 23.501 | 5.7.3.4 | Packet Delay Budget |
| TS 29.281 | 4 | GTP-U |
| RFC 3550 | 6.4.1 | Jitter calculation |

## Problem Statement
- What if jitter exceeds the PDB budget for the 5QI?
- What if packet loss is unacceptable for real-time services?
- What if the UPF drops packets due to AMBR enforcement?
- What if GTP-U encapsulation introduces variable delay?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish internet PDU session (5QI=9, QFI=1).
2. Start iperf3 UDP server on SA Core.
3. Run iperf3 UDP client on UE at 50 Mbps target bandwidth.
4. Collect jitter, loss, and throughput metrics.
5. Collect UPF UL stats (ul_pkts, ul_bytes, ul_dropped, ul_metered).

## Expected Behavior
- Jitter < 50ms (well within 5QI=9 PDB=300ms).
- Packet loss < 1%.
- UPF drops = 0 (50 Mbps within typical AMBR).
- Actual throughput close to 50 Mbps target.

## Pass/Fail Criteria
- **Pass:** Jitter < 50ms; loss < 1%; UPF drops = 0.
- **Fail:** Excessive jitter; high loss; UPF dropping packets.

## Key Concepts for Training

### Jitter (Inter-Packet Delay Variation)
Jitter measures the variation in packet arrival times. Per RFC 3550, jitter is computed as a running average: J(i) = J(i-1) + (|D(i-1,i)| - J(i-1))/16, where D is the difference in packet spacing between sender and receiver. Low jitter (< 30ms) is essential for voice quality. High jitter causes choppy audio and requires larger jitter buffers.

### Packet Loss and Real-Time Services
TCP retransmits lost packets; UDP does not. For voice (5QI=1), the PER (Packet Error Rate) target is 10^-2 (1%). For video (5QI=2), it's 10^-3 (0.1%). Packet loss above these thresholds degrades MOS (Mean Opinion Score) for voice and causes visible artifacts in video.

### 5QI Framework
The 5QI (5G QoS Identifier) maps to standardized QoS characteristics. Key parameters per 5QI: resource type (GBR/non-GBR), priority level (1-127), PDB (Packet Delay Budget in ms), PER (Packet Error Rate). The UPF uses these to configure QER rules and scheduling priorities.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| High jitter | > 50ms variation | Check CPU scheduling, GTP-U processing delays |
| Packet loss | > 1% loss | Check socket buffers, UPF capacity, link quality |
| UPF drops | ul_dropped > 0 | Check AMBR setting vs send rate |
| Low throughput | << 50 Mbps target | Check GTP-U tunnel health, MTU |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7.2.1 (5QI characteristics)
- RFC 3550 -- Section 6.4.1 (Jitter calculation)
- Related: TC-TRF-005 (DL UDP), TC-TRF-006 (bidir UDP), TC-TRF-001 (TCP UL)

## Quiz Questions
1. What is the standardized Packet Delay Budget for 5QI=9, and how does this test validate it?
   *Answer: PDB=300ms. The test measures jitter (which contributes to one-way delay). If jitter < 50ms, it's well within the 300ms budget. High jitter would indicate potential PDB violations.*

2. How is jitter calculated per RFC 3550?
   *Answer: J(i) = J(i-1) + (|D(i-1,i)| - J(i-1))/16, where D is the difference in inter-packet spacing at sender vs receiver. It's a smoothed running average with 1/16 weight for each new sample.*

3. Why is UDP testing more revealing of network QoS issues than TCP testing?
   *Answer: TCP masks QoS issues through retransmission, flow control, and congestion avoidance. Lost packets are retransmitted (hiding packet loss), and throughput adapts to available bandwidth (hiding rate limits). UDP reveals raw network behavior: actual loss rate, jitter, and one-way delay without any compensation.*

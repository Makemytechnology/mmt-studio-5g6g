# TC-IMS-005: VoNR Voice Latency -- PDB Compliance

## Overview
This test validates that VoNR voice latency meets the 5QI=1 Packet Delay Budget (PDB) of 100ms. It measures RTT through the IMS GTP-U tunnel and verifies that one-way delay (approximately RTT/2) stays within the PDB. Latency compliance is essential for natural conversational quality.

## 3GPP Background
Per TS 23.501 Section 5.7.3.4, the PDB for 5QI=1 is 100ms one-way between the UE and the UPF. This includes: UE processing, radio access (Uu), gNB processing, transport (N3 GTP-U), and UPF processing. The tester measures the UE-to-UPF segment (TUN -> GTP-U -> UPF).

For conversational voice, ITU-T G.114 recommends one-way mouth-to-ear delay < 150ms for "good" quality. The 5QI=1 PDB of 100ms allocates the majority of this budget to the 5G access network, leaving 50ms for IMS core and far-end processing.

**Network functions involved:** UE, gNB (tester), UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7.3.4 | Packet Delay Budget |
| TS 23.501 | 5.7.2.1 | 5QI=1 PDB=100ms |
| ITU-T G.114 | * | One-way delay recommendation |
| TS 26.114 | 10 | Delay requirements for IMS |

## Problem Statement
- What if one-way delay exceeds 100ms PDB?
- What if jitter causes intermittent PDB violations?
- What if the GTP-U encapsulation adds significant delay?

## Test Procedure (Step-by-Step)
1. Register UE, establish IMS PDU session (DNN=ims, PSI=2).
2. Measure RTT through IMS GTP-U tunnel (ping or UDP echo).
3. One-way delay = approximately RTT/2.
4. Verify one-way delay < 100ms (PDB for 5QI=1).

## Expected Behavior
- avg RTT < 200ms (one-way < 100ms) through IMS tunnel.
- On local network: RTT < 10ms.
- Low jitter (< 30ms for voice quality).

## Pass/Fail Criteria
- **Pass:** One-way delay < 100ms (5QI=1 PDB compliant).
- **Fail:** One-way delay >= 100ms.

## Key Concepts for Training

### Packet Delay Budget (PDB)
PDB defines the maximum acceptable one-way delay between UE and UPF (N3 node). It is a budget, not a guarantee -- the actual delay should be below PDB. The PDB is split between: UE processing, radio access scheduling, backhaul transport, and UPF processing. For voice (PDB=100ms), each component must contribute minimal delay.

### ITU-T G.114 Delay Recommendations
G.114 defines delay quality bands for conversational voice:
- **< 150ms:** Acceptable for most users (good quality)
- **150-400ms:** Acceptable with some degradation (satellite call quality)
- **> 400ms:** Unacceptable (severe echo, talk-over)
The 5QI=1 PDB of 100ms ensures the 5G segment stays well within the "good" range.

### Jitter Buffer and Delay
Voice receivers use a jitter buffer (typically 20-60ms) to absorb arrival time variations. The jitter buffer adds delay but smooths out packet timing. Total one-way delay = propagation + processing + jitter buffer. The PDB must accommodate all components.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| High RTT | > 200ms on local network | Check GTP-U processing, UPF latency |
| Jitter spikes | Occasional > 100ms | Check CPU scheduling, interrupt handling |
| GTP-U delay | Added latency from tunneling | Optimize GTP-U encap/decap path |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7.3.4 (PDB)
- ITU-T G.114 -- One-way delay
- Related: TC-IMS-003 (voice), TC-IMS-011 (call quality), TC-TRF-007 (ICMP RTT)

## Quiz Questions
1. What is the PDB for 5QI=1 and what does it mean for voice quality?
   *Answer: PDB=100ms one-way from UE to UPF. This ensures the 5G network segment contributes < 100ms to total mouth-to-ear delay. Combined with IMS core and far-end delays, total delay should stay under the ITU-T G.114 "good quality" threshold of 150ms.*

2. How is one-way delay estimated from RTT measurement?
   *Answer: One-way delay approximately equals RTT/2, assuming symmetric paths. This is an approximation -- actual paths may be asymmetric (different routing, different processing times for UL vs DL).*

3. If the measured one-way delay is 80ms on a local network (where propagation is < 1ms), where is the delay coming from?
   *Answer: Processing delays: TUN interface processing, GTP-U encapsulation/decapsulation, UPF packet processing, OS kernel scheduling. On a local network, 80ms is excessive and indicates a software processing bottleneck (not a network issue).*

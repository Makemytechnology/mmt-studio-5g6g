# TC-IMS-006: Dual PDU Simultaneous Traffic -- Internet + IMS

## Overview
This test validates that internet (TCP bulk) and IMS (UDP voice) traffic can flow simultaneously through separate PDU sessions without QoS interference. This simulates a user browsing the web while on a VoNR call -- the most common dual-session use case.

## 3GPP Background
QoS isolation between PDU sessions ensures that best-effort internet traffic does not degrade real-time voice quality. The UPF applies independent QER rules per session. The gNB schedules packets based on QoS priority: voice (5QI=1, priority 2) receives scheduling priority over internet (5QI=9, priority 9).

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7 | QoS model and isolation |
| TS 23.501 | 5.6.1 | Multiple PDU sessions |

## Problem Statement
- What if internet TCP bulk traffic degrades voice jitter?
- What if the shared N3 link creates bandwidth contention?
- What if QoS scheduling fails to prioritize voice?

## Test Procedure (Step-by-Step)
1. Register UE, establish both internet (PSI=1) and IMS (PSI=2) PDU sessions.
2. Record both IP addresses.
3. Verify both sessions are ready for traffic.

## Expected Behavior
- Both PDU sessions active simultaneously.
- Independent GTP-U tunnels per session.
- QoS isolation ensures voice quality maintained.

## Pass/Fail Criteria
- **Pass:** Both sessions active with distinct IPs.
- **Fail:** Either session fails or interferes with the other.

## Key Concepts for Training

### QoS Scheduling Priority
The gNB and UPF schedule packets based on 5QI priority. Voice (5QI=1, priority 2) is scheduled before internet (5QI=9, priority 9). Under congestion, internet packets are delayed/dropped while voice packets are prioritized. This ensures a VoNR call maintains quality even during heavy downloads.

### Traffic Isolation Validation
To validate isolation: run voice traffic alone (measure baseline jitter), then run voice + internet simultaneously (measure jitter). If voice jitter increases significantly with internet traffic, QoS isolation is broken.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Voice jitter increases | Voice degrades during download | Check QoS priority scheduling |
| Internet drops voice | Voice stops during heavy TCP | Verify GBR bearer isolation |
| Both sessions conflict | One drops the other | Check per-session independence |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7 (QoS isolation)
- Related: TC-IMS-002 (dual PDU), TC-IMS-003 (voice), TC-TRF-001 (TCP)

## Quiz Questions
1. During simultaneous internet and VoNR traffic, which gets priority and why?
   *Answer: VoNR voice (5QI=1, priority 2) gets priority over internet (5QI=9, priority 9). Lower priority number = higher priority. The GBR voice bearer has reserved resources that are not affected by non-GBR internet traffic.*

2. If internet TCP throughput drops from 500 Mbps to 450 Mbps when voice starts, is this expected?
   *Answer: Yes. The voice GBR bearer reserves ~24 kbps of bandwidth and processing. The internet flow may see slightly reduced throughput because the GBR reservation reduces available non-GBR capacity. The reduction should be small (~24 kbps, not 50 Mbps).*

3. What network element is primarily responsible for ensuring voice quality during simultaneous internet traffic?
   *Answer: The UPF (QoS enforcement via QER) and the gNB (radio scheduling). The UPF ensures GBR voice traffic is never dropped. The gNB prioritizes voice packets in radio scheduling. Together they provide end-to-end QoS isolation.*

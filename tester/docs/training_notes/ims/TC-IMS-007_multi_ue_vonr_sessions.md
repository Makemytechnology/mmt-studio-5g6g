# TC-IMS-007: Multi-UE VoNR Voice Sessions

## Overview
This test validates that multiple UEs can simultaneously maintain VoNR voice-rate UDP sessions through the IMS PDU. It tests the system's ability to handle concurrent voice sessions -- essential for any deployment where multiple users are on calls simultaneously.

## 3GPP Background
In production, a gNB serves many concurrent VoNR calls. Each call consumes GBR resources: ~24 kbps per direction per UE (48 kbps bidirectional). With 100 concurrent calls, the aggregate voice GBR reservation is ~4.8 Mbps -- small bandwidth but requiring 100 independent GBR bearers with strict QoS.

Each UE has its own IMS PDU session, GTP-U tunnel, and GBR bearer. The UPF must maintain independent PFCP sessions for each UE's voice flow.

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.228 | 5.4 | IMS call handling |
| TS 23.501 | 5.7.2.3 | GBR flows (per UE) |

## Problem Statement
- What if concurrent GBR reservations exhaust UPF resources?
- What if voice quality degrades with multiple concurrent sessions?
- What if one UE's voice stream interferes with another's?

## Test Procedure (Step-by-Step)
1. Register UE_1 and UE_2, establish PDU sessions for both.
2. Both UEs have active GTP-U tunnels.
3. Verify both sessions ready for voice-rate traffic.

## Expected Behavior
- Both UEs have active IMS PDU sessions.
- Independent GBR bearers (when voice active).
- No cross-UE interference.

## Pass/Fail Criteria
- **Pass:** Both UEs have active PDU sessions for voice.
- **Fail:** Either UE fails; resource exhaustion.

## Key Concepts for Training

### Concurrent GBR Management
Each VoNR call requires dedicated GBR resources. The PCF/SMF must track total GBR reservations to avoid overcommitting. If total GBR exceeds available capacity, new calls may be rejected (admission control). The AMF/PCF implements Call Admission Control (CAC) to prevent quality degradation.

### Per-UE Voice Quality Isolation
Each UE's voice stream must maintain independent QoS. Cross-UE interference (one UE's voice packets delayed by another UE's traffic) indicates broken QoS isolation. The UPF processes each UE's GBR flow independently through separate PFCP sessions.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| GBR resource exhaustion | New calls rejected | Check admission control limits |
| Cross-UE jitter | Voice degrades with 2nd UE | Verify per-UE QoS isolation |
| PFCP session limit | UPF rejects 2nd session | Increase UPF max session count |

## References
- 3GPP TS 23.228 V17.x -- IMS architecture
- Related: TC-IMS-003 (single voice), TC-IMS-011 (call quality), TC-IMS-014 (conference)

## Quiz Questions
1. What is the aggregate GBR bandwidth requirement for 10 concurrent bidirectional VoNR calls?
   *Answer: 10 calls x 2 directions x 24 kbps = 480 kbps. The bandwidth is small, but each call requires a dedicated GBR bearer with QoS guarantees (10 bearers, 10 PFCP sessions).*

2. Why might the 11th VoNR call fail even though bandwidth is available?
   *Answer: Admission control (CAC) may limit the number of concurrent GBR bearers. Each bearer consumes system resources beyond bandwidth: PFCP session state, PDR/FAR/QER rules, scheduling priority slots. The AMF/PCF enforces admission limits to protect existing call quality.*

3. How does the UPF ensure one UE's voice stream does not affect another UE's?
   *Answer: Separate PFCP sessions per UE with independent PDR/FAR/QER rules. Each UE's voice packets are matched by a unique PDR (by TEID for UL, by IP for DL) and processed by its own QER (GBR guarantee). The processing pipelines are independent.*

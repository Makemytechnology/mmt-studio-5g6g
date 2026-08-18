# TC-IMS-011: VoNR Bidirectional Voice Call Quality (MOS Estimation)

## Overview
This test measures VoNR call quality using bidirectional RTP voice streams between two UEs. It estimates MOS (Mean Opinion Score) using the ITU-T G.107 E-model based on measured jitter, packet loss, and one-way delay. MOS >= 3.5 indicates "good" voice quality.

## 3GPP Background
Voice quality in VoNR is measured by MOS (1-5 scale): 5=Excellent, 4=Good, 3=Fair, 2=Poor, 1=Bad. The ITU-T G.107 E-model computes an R-factor (0-100) from network impairments, which maps to MOS.

**E-model inputs:** one-way delay (Id), jitter (affecting delay), packet loss (Ie-eff from equipment impairment + loss), advantage factor (A for mobility networks = 10).

**R to MOS mapping:** R=93.2 -> MOS=4.5, R=80 -> MOS=4.0, R=70 -> MOS=3.5, R=60 -> MOS=3.1, R=50 -> MOS=2.6.

This test runs 60-second bidirectional RTP at AMR-WB rate between UE_A and UE_B through their respective IMS GTP-U tunnels. Path: UE_A -> TUN_A -> GTP-U_A -> UPF -> GTP-U_B -> TUN_B -> UE_B (and reverse).

**Network functions involved:** 2 UEs, 2 gNBs (tester), UPF, IMS (SIP signaling)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 26.114 | 10 | IMS multimedia quality |
| ITU-T G.107 | * | E-model for voice quality |
| TS 23.501 | 5.7.2.1 | 5QI=1 QoS |
| TS 23.228 | 5.4 | IMS call flow |

## Problem Statement
- What if MOS < 3.5 (unacceptable voice quality)?
- What if jitter exceeds 50ms (choppy audio)?
- What if one-way delay exceeds 100ms (5QI=1 PDB)?
- What if packet loss > 1% (audio gaps)?

## Test Procedure (Step-by-Step)
1. Register both UEs, establish IMS PDU sessions (DNN=ims).
2. SIP REGISTER both UEs.
3. UE_A sends SIP INVITE to UE_B (audio SDP: AMR-WB, port 20000).
4. Dedicated GBR bearer activated (5QI=1).
5. Bidirectional RTP: UE_A <-> UE_B for 60 seconds.
6. Measure jitter, packet loss, estimate one-way delay.
7. Compute MOS using E-model.
8. SIP BYE to tear down.

## Expected Behavior
- MOS >= 3.5 (Good quality).
- Jitter < 50ms.
- Packet loss < 1%.
- One-way delay < 100ms (5QI=1 PDB).
- Clean SIP signaling (INVITE/200 OK/BYE).

## Pass/Fail Criteria
- **Pass:** MOS >= 3.5; jitter < 50ms; loss < 1%.
- **Fail:** MOS < 3.5; excessive jitter or loss.

## Key Concepts for Training

### ITU-T G.107 E-Model
The E-model computes voice quality from network parameters: R = Ro - Is - Id - Ie-eff + A, where:
- **Ro:** Basic signal-to-noise ratio (~93.2 for wideband)
- **Is:** Simultaneous impairment (echo, noise)
- **Id:** Delay impairment (from one-way delay)
- **Ie-eff:** Equipment impairment (codec + packet loss effects)
- **A:** Advantage factor (10 for mobile, users tolerate more)

R is then mapped to MOS: MOS = 1 + 0.035R + R(R-60)(100-R)*7*10^-6.

### RTP Voice Streams
RTP (Real-time Transport Protocol) carries voice frames with headers: SSRC (synchronization source), sequence number, timestamp, payload type. The receiver uses sequence numbers to detect loss and timestamps plus jitter to compute delay variation. RTCP (companion protocol) provides quality feedback.

### MOS Quality Scale
| MOS | Quality | User Perception |
|-----|---------|----------------|
| 4.3+ | Excellent | Toll quality |
| 4.0-4.3 | Good | Minor imperfections |
| 3.5-4.0 | Fair | Noticeable but acceptable |
| 3.0-3.5 | Poor | Annoying |
| < 3.0 | Bad | Very annoying, unusable |

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| MOS < 3.5 | Poor voice quality | Reduce jitter and packet loss |
| High jitter | Choppy audio | Check GTP-U processing, QoS priority |
| Packet loss | Audio gaps | Check GBR bearer enforcement |
| High delay | Echo, talk-over | Check GTP-U tunnel latency |

## References
- ITU-T G.107 -- E-model for voice quality
- 3GPP TS 26.114 V17.x -- IMS multimedia quality
- Related: TC-IMS-009 (call setup), TC-IMS-012 (single direction), TC-IMS-013 (ViNR quality)

## Quiz Questions
1. What MOS score indicates "good" voice quality, and what R-factor does it correspond to?
   *Answer: MOS >= 3.5 (fair to good). This corresponds to R-factor >= 70. MOS 4.0+ (good) corresponds to R >= 80.*

2. In the E-model, what is the "advantage factor" A, and why is it set to 10 for mobile networks?
   *Answer: The advantage factor accounts for user tolerance of impairments in exchange for access convenience. Mobile users accept slightly lower quality because they value mobility. A=0 for fixed lines, A=10 for mobile, A=20 for satellite.*

3. If jitter is 20ms and packet loss is 0.5%, but MOS is only 3.2, what is the likely cause?
   *Answer: High one-way delay (Id impairment). Even with low jitter and loss, delay > 150ms significantly reduces MOS due to echo and conversational difficulty (talking over each other). The GTP-U tunnel path may have high propagation or processing delay.*

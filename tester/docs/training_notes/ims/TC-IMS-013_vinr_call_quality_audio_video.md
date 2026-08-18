# TC-IMS-013: ViNR Bidirectional Audio+Video Call Quality

## Overview
This test measures ViNR (Video over New Radio) call quality with simultaneous audio and video streams in both directions. It runs 4 concurrent RTP streams (audio A->B, audio B->A, video A->B, video B->A) and measures per-stream jitter, loss, and voice MOS. This is the most comprehensive IMS quality test.

## 3GPP Background
A bidirectional ViNR call generates 4 RTP streams through the GTP-U tunnels:
- Audio A->B: AMR-WB, 50 pps, port 20000, 5QI=1
- Audio B->A: AMR-WB, 50 pps, port 20000, 5QI=1
- Video A->B: H.264, 2 Mbps, port 20002, 5QI=2
- Video B->A: H.264, 2 Mbps, port 20002, 5QI=2

Total bandwidth: ~4.05 Mbps GBR. The UPF manages two GBR bearers per UE (audio + video) with independent QoS.

Quality targets: Audio MOS >= 3.5, audio jitter < 50ms, video jitter < 100ms, audio loss < 1%, video loss < 0.1%.

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 26.114 | 7 | Multimedia telephony media |
| ITU-T G.107 | * | E-model for voice MOS |
| TS 23.501 | 5.7.2.1 | 5QI=1 and 5QI=2 |

## Problem Statement
- What if video traffic degrades audio quality (cross-media interference)?
- What if 4 concurrent streams overwhelm the GTP-U processing?
- What if audio MOS drops below 3.5 when video is active?
- What if video jitter exceeds 100ms?

## Test Procedure (Step-by-Step)
1. Register both UEs, establish IMS PDU sessions.
2. SIP REGISTER both, SIP INVITE with audio+video SDP.
3. Two GBR bearers per UE: 5QI=1 (audio) + 5QI=2 (video).
4. 4 concurrent RTP streams for 30 seconds.
5. Measure per-stream jitter, loss.
6. Compute voice MOS from audio metrics.
7. SIP BYE teardown.

## Expected Behavior
- Audio MOS >= 3.5 for both directions.
- Audio jitter < 50ms; video jitter < 100ms.
- Audio loss < 1%; video loss < 0.1%.
- All 4 RTP streams stable for 30 seconds.

## Pass/Fail Criteria
- **Pass:** Audio MOS >= 3.5; all jitter/loss within thresholds.
- **Fail:** MOS < 3.5; excessive jitter or loss on any stream.

## Key Concepts for Training

### Multi-Stream QoS Management
With 4 concurrent streams on 2 GBR bearers, the UPF must: (1) classify each packet to the correct bearer (by QFI/5QI), (2) enforce independent GBR/MBR per bearer, (3) schedule priority correctly (voice 5QI=1 > video 5QI=2 > internet 5QI=9). Cross-bearer interference indicates scheduling bugs.

### Video Quality Metrics
While voice quality is measured by MOS (E-model), video quality uses different metrics: PSNR (Peak Signal-to-Noise Ratio), SSIM (Structural Similarity), or subjective MOS. For this test, video quality is approximated by jitter and loss metrics -- actual PSNR requires decoded video comparison.

### Concurrent Stream Stress
4 streams consume: 200 audio pps (50 pps x 2 x 2 directions) + video packets (variable rate). The GTP-U stack must handle sustained multi-stream processing without introducing jitter. CPU scheduling between audio and video processing is critical.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Audio MOS drops with video | Voice degrades when video active | Check 5QI priority scheduling |
| Video jitter > 100ms | Video stutter | Check 5QI=2 bearer QoS |
| High audio loss | Gaps in voice | Verify 5QI=1 GBR enforcement |
| Stream interference | One stream affects others | Check per-stream GTP-U processing |

## References
- ITU-T G.107 -- E-model
- 3GPP TS 26.114 V17.x -- Multimedia telephony
- Related: TC-IMS-010 (ViNR setup), TC-IMS-011 (voice quality), TC-IMS-004 (video)

## Quiz Questions
1. In a bidirectional ViNR call, how many RTP streams are active simultaneously?
   *Answer: 4 streams: audio A->B, audio B->A, video A->B, video B->A.*

2. Why does video have a stricter packet loss target (0.1%) than audio (1%)?
   *Answer: Video codec artifacts from lost packets (blocky frames, green patches, frozen video) are visually very noticeable. Audio codecs have error concealment that can mask short losses. Users notice video quality issues more than brief audio gaps.*

3. If audio MOS is 4.0 with audio-only but drops to 3.2 when video is added, what is the likely cause?
   *Answer: Cross-media interference. The video stream (2 Mbps) is competing with audio for: GTP-U processing time, UPF scheduling, GBR bearer resources, or shared buffer space. The 5QI priority scheduling should protect audio (5QI=1, priority 2) from video (5QI=2, priority 4), so this suggests a scheduling bug.*

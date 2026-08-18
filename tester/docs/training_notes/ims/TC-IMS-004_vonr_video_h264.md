# TC-IMS-004: VoNR Video Traffic -- H.264

## Overview
This test validates VoNR video traffic at H.264 bitrate (2 Mbps) through the IMS PDU session with 5QI=2. Video calls in VoNR (also called ViNR -- Video over New Radio) carry both audio (AMR-WB, 5QI=1) and video (H.264, 5QI=2) as separate RTP streams on separate GBR bearers.

## 3GPP Background
ViNR video uses 5QI=2: GBR type, priority level 4, PDB=150ms, PER=10^-3 (0.1%). Video is more tolerant of delay than voice (150ms vs 100ms PDB) but less tolerant of packet loss (0.1% vs 1%) because video codec artifacts are visible.

H.264 (AVC) is the standard video codec for IMS multimedia telephony (TS 26.114). At 2 Mbps, it provides SD to low-HD quality video. The video stream uses a separate RTP session (different port than audio) with dynamic payload type and H.264 packetization (RFC 6184).

**Traffic path:** UE -> TUN -> GTP-U (GBR 5QI=2) -> UPF -> Peer UE

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 26.114 | 7 | IMS multimedia telephony |
| TS 23.501 | 5.7.2.1 | 5QI=2 (conversational video) |
| RFC 6184 | * | RTP payload format for H.264 |

## Problem Statement
- What if video jitter exceeds 150ms PDB?
- What if video packet loss exceeds 0.1% (visible artifacts)?
- What if the GBR bearer for video is not established?

## Test Procedure (Step-by-Step)
1. Register UE, establish IMS PDU session (DNN=ims, PSI=2).
2. Generate UDP traffic at 2 Mbps (H.264 video rate) through IMS tunnel.
3. Verify traffic flows at the expected rate.

## Expected Behavior
- 2 Mbps UDP stream flows through IMS GTP-U tunnel.
- Jitter within 5QI=2 PDB (150ms).
- Packet loss < 0.1%.

## Pass/Fail Criteria
- **Pass:** Video-rate traffic flows through IMS PDU session.
- **Fail:** Traffic fails; excessive loss or jitter.

## Key Concepts for Training

### Audio vs Video GBR Bearers
VoNR calls use two GBR bearers: 5QI=1 for audio (~24 kbps) and 5QI=2 for video (~2 Mbps). They have different QoS characteristics reflecting each media's sensitivity. Audio is more delay-sensitive (100ms PDB) but loss-tolerant (1% PER). Video is less delay-sensitive (150ms PDB) but loss-intolerant (0.1% PER).

### H.264 Packetization
H.264 video frames are split into NAL units. Large frames (I-frames) may span multiple RTP packets. Single NAL unit mode sends one NAL per packet. FU-A (Fragmentation Unit) mode splits large NALs across packets. Lost packets may corrupt part of a frame, causing visible glitches.

### Video Bandwidth Impact
At 2 Mbps, video consumes ~80x more bandwidth than voice (24 kbps). This impacts: GBR resource reservation (2 Mbps vs 24 kbps), N3 link utilization, and UPF processing load. Bidirectional video calls require 4 Mbps total GBR reservation.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Video artifacts | Blocky/frozen video | Check packet loss (< 0.1% required) |
| High latency | Video lag behind audio | Check 5QI=2 PDB compliance |
| No video bearer | Video on default bearer | Check PCF bearer activation |
| Bandwidth insufficient | Video stutter | Verify 2 Mbps GBR reservation |

## References
- 3GPP TS 26.114 V17.x -- Multimedia telephony
- RFC 6184 -- H.264 RTP payload
- Related: TC-IMS-003 (voice), TC-IMS-010 (audio+video call), TC-IMS-013 (ViNR quality)

## Quiz Questions
1. What are the QoS differences between 5QI=1 (voice) and 5QI=2 (video)?
   *Answer: 5QI=1: PDB=100ms, PER=10^-2, priority 2. 5QI=2: PDB=150ms, PER=10^-3, priority 4. Voice is more delay-sensitive; video is more loss-sensitive.*

2. Why does video have a stricter PER (0.1%) than voice (1%)?
   *Answer: Lost video packets cause visible artifacts (blocky frames, green patches) that are very noticeable to users. Lost voice packets cause brief audio gaps that can be masked by codec error concealment. Human visual perception is more sensitive to quality degradation than hearing.*

3. During a bidirectional ViNR call, what is the total GBR reservation for audio and video?
   *Answer: Audio: 2 x 24 kbps = 48 kbps (both directions). Video: 2 x 2 Mbps = 4 Mbps. Total: ~4.05 Mbps GBR reserved for the call.*

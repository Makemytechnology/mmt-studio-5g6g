# TC-IMS-010: SIP INVITE Audio+Video ViNR Call

## Overview
This test validates a ViNR (Video over New Radio) call setup with both audio and video SDP offers. The SIP INVITE includes two media lines: audio (AMR-WB) and video (H.264). The IMS triggers two GBR bearers: 5QI=1 for voice and 5QI=2 for video.

## 3GPP Background
A ViNR call extends VoNR with a video component. The SDP offer contains two m= lines:
```
m=audio 20000 RTP/AVP 96
a=rtpmap:96 AMR-WB/16000
m=video 20002 RTP/AVP 97
a=rtpmap:97 H264/90000
```

The PCF triggers two separate GBR bearers via the Rx interface: one for audio (5QI=1, GBR=24 kbps) and one for video (5QI=2, GBR=2 Mbps). Each bearer has independent QoS characteristics.

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.229 | 5.1 | SIP INVITE with multiple media |
| TS 26.114 | 7 | Multimedia telephony |
| TS 23.501 | 5.7.2.1 | 5QI=1 and 5QI=2 |

## Problem Statement
- What if the callee does not support video?
- What if only one GBR bearer is created instead of two?
- What if audio and video RTP ports conflict?

## Test Procedure (Step-by-Step)
1. Register both UEs, establish IMS PDU sessions.
2. Caller sends SIP INVITE with audio+video SDP to callee.
3. Verify SIP response (100/180/200).
4. Two GBR bearers should be activated (5QI=1 + 5QI=2).

## Expected Behavior
- INVITE with dual media accepted.
- Two GBR bearers: audio (5QI=1) and video (5QI=2).
- SDP answer includes both audio and video.

## Pass/Fail Criteria
- **Pass:** INVITE accepted; both media negotiated.
- **Fail:** INVITE rejected; media negotiation fails.

## Key Concepts for Training

### Multi-Media SDP Negotiation
SDP supports multiple media lines. Each m= line describes one media stream. The callee can accept, reject, or modify each independently. Rejecting video (port=0 in answer) while accepting audio creates a voice-only call. Both parties must agree on codec and port for each media type.

### Dual GBR Bearer Activation
The PCF creates separate policy rules for audio and video because they have different QoS requirements. Audio: 5QI=1, GBR=24 kbps, PDB=100ms. Video: 5QI=2, GBR=2 Mbps, PDB=150ms. The SMF creates two QoS flows on the UPF, each with its own QFI and QER.

### Resource Impact of ViNR
A ViNR call consumes significantly more resources than VoNR: ~4 Mbps GBR reservation (bidirectional audio+video) vs ~48 kbps (audio only). This impacts admission control: fewer concurrent ViNR calls are possible compared to VoNR calls.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Video rejected | SDP answer has port=0 for video | Check callee codec support |
| Missing GBR bearer | Only one bearer created | Check PCF multi-media handling |
| Port conflict | RTP streams interfere | Use distinct ports (20000, 20002) |

## References
- 3GPP TS 26.114 V17.x -- Multimedia telephony
- Related: TC-IMS-009 (audio only), TC-IMS-013 (ViNR quality)

## Quiz Questions
1. How many GBR bearers are needed for a bidirectional ViNR call, and what are their 5QI values?
   *Answer: Two GBR bearers: 5QI=1 for audio and 5QI=2 for video. Each bearer carries bidirectional traffic (both call directions share the same bearer per media type).*

2. If the callee supports audio but not video, how does it indicate this in the SDP answer?
   *Answer: The callee sets the video media port to 0 in the SDP answer: "m=video 0 RTP/AVP 97". This indicates rejection of the video stream while accepting audio. The call proceeds as audio-only (VoNR instead of ViNR).*

3. What is the total GBR bandwidth reservation for a bidirectional ViNR call?
   *Answer: Audio: 2 x 24 kbps = 48 kbps. Video: 2 x 2 Mbps = 4 Mbps. Total: ~4.05 Mbps. This is significantly more than VoNR (48 kbps) and impacts admission control decisions.*

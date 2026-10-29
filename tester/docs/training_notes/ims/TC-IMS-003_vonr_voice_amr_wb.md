# TC-IMS-003: VoNR Voice Traffic -- AMR-WB

## Overview
This test validates VoNR (Voice over New Radio) voice traffic at AMR-WB (Adaptive Multi-Rate Wideband) bitrate through the IMS PDU session. AMR-WB at 23.85 kbps is the standard codec for HD Voice in 5G. The traffic flows through a dedicated GBR bearer with 5QI=1.

## 3GPP Background
VoNR voice media uses RTP (Real-time Transport Protocol) over UDP to carry encoded voice frames. AMR-WB (TS 26.171) provides wideband audio (50-7000 Hz bandwidth) at rates from 6.60 to 23.85 kbps. The 23.85 kbps mode provides the highest quality.

For 5QI=1 (conversational voice): GBR type, priority level 2, PDB=100ms (one-way delay budget), PER=10^-2 (1% acceptable packet loss). These parameters ensure voice quality remains acceptable even under network congestion.

RTP packets carry 20ms voice frames with payload type 96 (dynamic). The RTP header is 12 bytes, AMR-WB frame is ~60 bytes at 23.85 kbps. Including UDP/IP/GTP-U overhead, each voice packet is approximately 130 bytes.

**Traffic path:** UE -> TUN (SO_BINDTODEVICE) -> GTP-U -> UPF -> Peer UE (or media server)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 26.114 | 7 | IMS multimedia telephony media handling |
| TS 26.171 | * | AMR-WB codec |
| TS 23.501 | 5.7.2.1 | 5QI=1 (conversational voice) |
| TS 23.228 | 5.4 | IMS call flow |

## Problem Statement
- What if voice packets are delayed beyond the 100ms PDB?
- What if packet loss exceeds 1% (PER for 5QI=1)?
- What if the GBR bearer is not established for voice media?
- What if AMR-WB bitrate doesn't match expected 23.85 kbps?

## Test Procedure (Step-by-Step)
1. Register UE, establish IMS PDU session (DNN=ims, PSI=2).
2. GTP-U tunnel active for IMS.
3. Generate UDP traffic at AMR-WB rate (23.85 kbps) through IMS tunnel.
4. Verify traffic flows correctly.

## Expected Behavior
- UDP voice-rate traffic flows through IMS GTP-U tunnel.
- Data rate matches AMR-WB 23.85 kbps.
- Low jitter and minimal packet loss.

## Pass/Fail Criteria
- **Pass:** VoNR traffic flows at AMR-WB rate through IMS PDU session.
- **Fail:** Traffic fails; wrong rate; excessive loss.

## Key Concepts for Training

### AMR-WB Codec
AMR-WB (Adaptive Multi-Rate Wideband) provides HD Voice quality with 16 kHz sampling rate (vs 8 kHz for narrowband). Nine codec modes from 6.60 to 23.85 kbps allow bitrate adaptation based on network conditions. In VoNR, AMR-WB is negotiated via SDP in the SIP INVITE (a=rtpmap:96 AMR-WB/16000).

### Voice Packet Timing
AMR-WB generates one frame every 20ms. At 23.85 kbps, each frame is ~60 bytes. RTP sends one frame per packet. The UE transmits 50 packets/second. This low data rate means voice consumes minimal bandwidth (24 kbps) but is very sensitive to jitter and loss.

### 5QI=1 GBR Bearer
Voice media requires a dedicated GBR bearer (5QI=1) with guaranteed resources. This bearer is created dynamically when a VoNR call is established (triggered by PCF Rx interface when P-CSCF processes SIP INVITE). The bearer is released on call termination (SIP BYE).

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| High jitter | Choppy audio | Check GTP-U processing priority |
| Packet loss > 1% | Audio gaps | Check UPF QoS scheduling |
| No GBR bearer | Voice on default bearer | Check PCF Rx / bearer activation |
| Wrong codec rate | Bitrate mismatch | Verify AMR-WB SDP negotiation |

## References
- 3GPP TS 26.114 V17.x -- Section 7 (Media handling)
- 3GPP TS 26.171 V17.x -- AMR-WB codec
- Related: TC-IMS-004 (video), TC-IMS-005 (latency), TC-IMS-011 (call quality)

## Quiz Questions
1. What is the packet rate for AMR-WB voice at 20ms frame interval?
   *Answer: 50 packets per second (1000ms / 20ms = 50). Each packet carries one AMR-WB voice frame.*

2. What are the 5QI=1 QoS characteristics for conversational voice?
   *Answer: GBR type, priority level 2, PDB=100ms (one-way delay), PER=10^-2 (1% acceptable loss). These ensure voice quality remains at "good" or better even under congestion.*

3. Why does VoNR use a dedicated GBR bearer instead of the default IMS signaling bearer?
   *Answer: The default IMS bearer (5QI=5) is non-GBR and designed for SIP signaling, not real-time media. Voice requires guaranteed resources (GBR) to prevent packet loss under congestion, and a tighter PDB (100ms for voice vs 100ms for signaling). A dedicated bearer with 5QI=1 provides these guarantees.*

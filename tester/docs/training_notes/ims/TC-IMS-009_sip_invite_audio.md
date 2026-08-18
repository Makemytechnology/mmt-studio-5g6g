# TC-IMS-009: SIP INVITE Audio VoNR Call

## Overview
This test validates the SIP INVITE procedure for establishing a VoNR audio call between two UEs. The caller sends a SIP INVITE with audio SDP (AMR-WB) to the callee through the IMS core. This tests the complete VoNR call setup signaling path.

## 3GPP Background
The SIP INVITE (TS 24.229 Section 5.1) establishes a SIP dialog for a call. The INVITE carries an SDP (Session Description Protocol) offer describing the media capabilities.

**VoNR call flow:**
1. Caller -> P-CSCF: INVITE (SDP offer: m=audio 20000 RTP/AVP 96, a=rtpmap:96 AMR-WB/16000)
2. P-CSCF -> S-CSCF -> P-CSCF -> Callee: INVITE
3. Callee -> Caller: 100 Trying, 180 Ringing, 200 OK (SDP answer)
4. Caller -> Callee: ACK
5. RTP media flows bidirectionally (AMR-WB voice)
6. Either party: BYE -> 200 OK (call teardown)

When the P-CSCF processes the INVITE, it signals the PCF via the Rx interface. The PCF triggers dedicated GBR bearer creation via N7 -> SMF -> UPF: 5QI=1 (conversational voice) for each call participant.

**Network functions involved:** 2 UEs, P-CSCF, S-CSCF, I-CSCF, PCF, SMF, UPF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.229 | 5.1 | SIP INVITE procedure |
| TS 23.228 | 5.4 | IMS call flow |
| TS 23.501 | 5.7.2.3 | GBR bearer for voice |
| RFC 3261 | 13 | SIP INVITE transaction |
| RFC 4566 | * | SDP format |

## Problem Statement
- What if the SIP INVITE is rejected (403 Forbidden, 486 Busy)?
- What if SDP negotiation fails (no common codec)?
- What if the GBR bearer is not triggered by the PCF?
- What if the callee is not IMS-registered?

## Test Procedure (Step-by-Step)
1. Register both UEs via NAS, establish IMS PDU sessions.
2. SIP REGISTER both UEs to P-CSCF.
3. Caller sends SIP INVITE to callee with audio SDP.
4. Wait for SIP response (100 Trying, 180 Ringing, 200 OK).
5. Send ACK to establish call.
6. Verify signaling path is functional.
7. Send BYE to tear down.

## Expected Behavior
- SIP INVITE accepted (response received: 100/180/200).
- SDP negotiation succeeds (AMR-WB codec agreed).
- GBR bearer activated for voice (5QI=1).
- Call teardown via BYE is clean.

## Pass/Fail Criteria
- **Pass:** SIP INVITE receives response; signaling path verified; clean teardown.
- **Fail:** INVITE rejected; no response; SDP failure.

## Key Concepts for Training

### SDP Offer/Answer Model
The caller's INVITE includes an SDP offer: "I support AMR-WB at 16 kHz, port 20000." The callee's 200 OK includes an SDP answer: "I accept AMR-WB, use my port 30000." After this exchange, both sides know the codec, RTP ports, and IP addresses for media.

### SIP Response Codes
- **100 Trying:** Request received, processing
- **180 Ringing:** Callee's phone is ringing
- **183 Session Progress:** Early media (ringback tone)
- **200 OK:** Call accepted (for INVITE); success (for REGISTER/BYE)
- **4xx:** Client errors (400 Bad Request, 403 Forbidden, 486 Busy Here)
- **5xx:** Server errors (500 Internal Server Error, 503 Service Unavailable)

### PCF-Triggered GBR Bearer
When the P-CSCF receives the INVITE, it signals the PCF via the Rx (Diameter) interface with media description (codec, bandwidth). The PCF maps this to QoS parameters (5QI=1, GBR=24 kbps) and triggers the SMF via N7 to create a dedicated GBR bearer on the UPF. This is called "policy-driven QoS."

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| 403 Forbidden | Call rejected | Check callee's IMS registration |
| SDP failure | No common codec | Ensure both UEs support AMR-WB |
| No GBR bearer | Voice on default bearer | Check PCF Rx interface configuration |
| INVITE timeout | No response from callee | Check SIP routing through IMS core |

## References
- 3GPP TS 24.229 V17.x -- Section 5.1 (SIP procedures)
- RFC 3261 -- SIP specification
- Related: TC-IMS-008 (REGISTER), TC-IMS-010 (audio+video), TC-IMS-011 (quality)

## Quiz Questions
1. What are the three key SIP responses a caller expects after sending INVITE?
   *Answer: 100 Trying (request received), 180 Ringing (callee alerting), 200 OK (call accepted). The caller then sends ACK to complete the 3-way handshake.*

2. How does the IMS trigger a dedicated GBR bearer for a VoNR call?
   *Answer: P-CSCF receives INVITE with SDP -> signals PCF via Rx interface (Diameter AAR) with media description -> PCF determines QoS policy (5QI=1, GBR=24 kbps) -> PCF sends policy to SMF via N7 -> SMF creates dedicated QoS flow (GBR bearer) on UPF via N4 (PFCP).*

3. What information does the SDP offer in a VoNR INVITE contain?
   *Answer: Media type (m=audio), RTP port (20000), codec (RTP/AVP 96), codec parameters (a=rtpmap:96 AMR-WB/16000), connection info (c=IN IP4 ue_ip), and optional attributes (a=ptime:20 for 20ms frames).*

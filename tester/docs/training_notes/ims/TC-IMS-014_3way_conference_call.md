# TC-IMS-014: 3-Way IMS Conference Call

## Overview
This test validates a 3-way IMS conference call. UE_A establishes calls to UE_B and UE_C, then merges both calls into a conference. This exercises advanced IMS features: call hold, second call, conference factory, and MRFP (Media Resource Function Processor) for audio mixing.

## 3GPP Background
The 3-way conference procedure (TS 24.147 Section 5.3.1.2) involves:

1. **First call:** UE_A INVITE -> UE_B. Audio RTP established.
2. **Hold first call:** UE_A re-INVITE to UE_B with "a=sendonly" SDP (puts UE_B on hold).
3. **Second call:** UE_A INVITE -> UE_C. New audio RTP established.
4. **Merge (conference):** UE_A INVITE to conference factory URI (sip:conference-factory@domain). The MRFP creates a media mixer.
5. **Audio mixing:** MRFP receives audio from all 3 participants and sends a mix to each (excluding their own audio to prevent echo).
6. **Teardown:** SIP BYE from any participant leaves the conference.

The conference factory URI is a special SIP URI that the S-CSCF routes to the MRFP. The MRFP creates RTP endpoints for each participant and mixes audio in real-time.

Each participant has an independent GBR bearer (5QI=1) for their voice stream. The MRFP->UPF path also requires GBR resources.

**Network functions involved:** 3 UEs, P-CSCF, S-CSCF, MRFP, UPF, PCF, SMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.147 | 5.3.1.2 | Conference creation by merging |
| TS 24.229 | 5.1 | SIP procedures |
| TS 26.114 | * | Media handling for conferencing |
| TS 23.228 | 5.12 | MRFP architecture |

## Problem Statement
- What if hold/resume SDP negotiation fails?
- What if the conference factory URI is not configured?
- What if the MRFP audio mixing introduces too much delay?
- What if one participant's GBR bearer interferes with another's?
- What if MOS degrades with 3 participants (mixing artifacts)?

## Test Procedure (Step-by-Step)
1. Register 3 UEs (A, B, C), establish IMS PDU sessions.
2. SIP REGISTER all 3 UEs.
3. UE_A INVITE -> UE_B (first call, audio). GBR bearer activated.
4. UE_A holds UE_B (re-INVITE with a=sendonly).
5. UE_A INVITE -> UE_C (second call, audio). Second GBR bearer.
6. UE_A merges calls -> INVITE to conference factory URI.
7. MRFP mixes audio from all 3 participants.
8. 3-way RTP voice: each UE sends AMR-WB to MRFP.
9. Measure per-participant jitter, loss, compute MOS.
10. SIP BYE to tear down conference.

## Expected Behavior
- Conference setup succeeds (hold, second call, merge).
- MOS >= 3.0 for all participants (Fair or better).
- MRFP audio mixing functional.
- All SIP procedures complete cleanly.

## Pass/Fail Criteria
- **Pass:** Conference established; MOS >= 3.0 for all; clean teardown.
- **Fail:** Conference setup fails; MOS < 3.0; incomplete teardown.

## Key Concepts for Training

### SIP Call Hold
Call hold uses SDP renegotiation via re-INVITE. The holding party sends re-INVITE with "a=sendonly" (I'll stop sending, you stop sending to me). The held party responds with "a=recvonly". To resume: re-INVITE with "a=sendrecv". Hold allows the user to initiate a second call without disconnecting the first.

### Conference Factory
The conference factory (sip:conference-factory@domain) is a special SIP URI that, when INVITEd, creates a conference room on the MRFP. The MRFP acts as a B2BUA (Back-to-Back User Agent), terminating each participant's SIP dialog and creating a media mixing bridge. Each participant sends audio to the MRFP, which mixes and redistributes.

### MRFP Audio Mixing
The MRFP (Media Resource Function Processor) receives RTP audio from all participants. For each participant, it mixes the audio from all OTHER participants (excluding their own to prevent echo/feedback). With 3 participants (A, B, C): A receives mix(B+C), B receives mix(A+C), C receives mix(A+B). The mixing adds processing delay (~10-20ms).

### Conference Call Quality
Conference calls typically have lower MOS than point-to-point calls due to: MRFP mixing delay, additional network hops (UE->MRFP->UE instead of UE->UE), potential echo from imperfect self-exclusion, and background noise accumulation from multiple sources. MOS >= 3.0 (Fair) is the target for conference calls.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Hold fails | UE_B not held | Check re-INVITE SDP handling |
| Conference factory not found | 404 Not Found | Configure conference factory URI |
| MRFP delay | High latency | Check MRFP processing capacity |
| Echo in conference | Participants hear themselves | MRFP self-exclusion bug |
| MOS < 3.0 | Poor quality | Check MRFP mixing, network jitter |

## References
- 3GPP TS 24.147 V17.x -- Section 5.3 (Conference procedures)
- 3GPP TS 23.228 V17.x -- Section 5.12 (MRFP)
- Related: TC-IMS-009 (INVITE), TC-IMS-011 (call quality), TC-IMS-007 (multi-UE)

## Quiz Questions
1. What are the three SIP procedures needed to create a 3-way conference from two point-to-point calls?
   *Answer: (1) INVITE to first callee (establish first call), (2) re-INVITE to first callee with hold SDP (put on hold), INVITE to second callee (establish second call), (3) INVITE to conference factory URI (merge both calls). The conference factory creates the MRFP bridge.*

2. How does the MRFP handle audio for 3 conference participants without echo?
   *Answer: For each participant, the MRFP mixes audio from all OTHER participants, excluding the participant's own audio. A receives mix(B+C), B receives mix(A+C), C receives mix(A+B). This prevents echo (hearing your own voice) while allowing each participant to hear all others.*

3. Why is the MOS target for conference calls (>= 3.0) lower than for point-to-point calls (>= 3.5)?
   *Answer: Conference calls inherently have: (1) additional delay from MRFP processing, (2) more network hops (UE->UPF->MRFP->UPF->UE), (3) accumulated background noise from multiple sources, (4) potential mixing artifacts. Users accept slightly lower quality for the convenience of multi-party communication.*

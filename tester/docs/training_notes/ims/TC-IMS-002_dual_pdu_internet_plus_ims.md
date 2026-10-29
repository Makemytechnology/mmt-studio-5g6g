# TC-IMS-002: Dual PDU Sessions -- Internet + IMS

## Overview
This test validates that a UE can maintain both internet (PSI=1) and IMS (PSI=2) PDU sessions simultaneously. This is the standard configuration for VoNR-capable smartphones: internet for data and IMS for voice/video services, with QoS isolation between them.

## 3GPP Background
Per TS 23.501 Section 5.6.1, a UE can maintain up to 15 PDU sessions (PSI values 1-15). The internet and IMS sessions have independent: DNN, UPF (potentially), IP address pool, QoS flows, and GTP-U tunnels.

The internet session (5QI=9, best effort) handles web browsing, streaming, and downloads. The IMS session (5QI=5, priority 1) handles SIP signaling. During a VoNR call, the IMS session additionally carries GBR voice/video flows (5QI=1/2) that are dynamically created by the PCF.

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.6.1 | Multiple PDU sessions |
| TS 23.228 | 5.2 | IMS access via 5GC |

## Problem Statement
- What if the IMS session interferes with the internet session?
- What if both sessions receive the same IP address?

## Test Procedure (Step-by-Step)
1. Register UE via NAS.
2. Establish internet PDU session: DNN=internet, PSI=1. Record IP_inet.
3. Establish IMS PDU session: DNN=ims, PSI=2. Record IP_ims.
4. Verify both active with different IPs.

## Expected Behavior
- Both sessions active simultaneously.
- IP_inet != IP_ims.
- Independent GTP-U tunnels.

## Pass/Fail Criteria
- **Pass:** Both sessions active; different IPs.
- **Fail:** Either session fails; same IP.

## Key Concepts for Training

### QoS Isolation Between Internet and IMS
The UPF provides QoS isolation: internet traffic (5QI=9, priority 9) cannot degrade IMS signaling (5QI=5, priority 1). During a voice call, GBR voice traffic (5QI=1, dedicated bearer) is further isolated. Heavy internet downloads should not cause voice quality degradation.

### Dual-Stack UE Configuration
The UE manages two TUN interfaces (or bearers) simultaneously. Applications must bind to the correct interface: SIP stack binds to the IMS TUN, web browser binds to the internet TUN. On Android/iOS, the OS automatically routes SIP traffic through the IMS PDN connection.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| IP collision | Same IP for both | Check per-DNN pool config |
| Session interference | First drops when second creates | Verify AMF session independence |
| Routing conflict | Traffic on wrong TUN | Check per-TUN host routes |

## References
- 3GPP TS 23.501 V17.x -- Section 5.6.1
- Related: TC-IMS-001 (IMS PDU), TC-PDU-003 (multi-PDU), TC-IMS-006 (dual traffic)

## Quiz Questions
1. Why must the internet and IMS PDU sessions have different IP addresses?
   *Answer: They connect to different DNNs (different UPFs/gateways) and have different QoS profiles. The SIP stack registers with the IMS IP; internet applications use the internet IP. Sharing an IP would confuse routing and SIP registration.*

2. During a VoNR call, how many active QoS flows does the UE typically have?
   *Answer: At least 3: (1) Internet default flow (5QI=9, PSI=1), (2) IMS signaling flow (5QI=5, PSI=2), (3) Voice media flow (5QI=1, GBR, PSI=2). If video is active: a 4th flow (5QI=2, GBR, PSI=2).*

3. What happens to the internet PDU session during a VoNR call?
   *Answer: Nothing -- it continues operating independently. The UE can browse the web while on a voice call because the sessions are isolated. Internet traffic uses the internet PDU (PSI=1, DNN=internet) while voice uses the IMS PDU (PSI=2, DNN=ims). QoS isolation ensures the voice call is not affected.*

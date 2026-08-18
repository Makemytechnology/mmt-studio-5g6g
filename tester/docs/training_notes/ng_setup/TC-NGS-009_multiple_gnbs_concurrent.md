# TC-NGS-009: Multiple gNBs Concurrent NG Setup

## Overview
This test validates that 3 gNBs can simultaneously perform NG Setup with the same AMF. Each gNB establishes its own SCTP association and performs independent NG Setup. This tests the AMF's ability to manage multiple NG-C interfaces concurrently, which is the standard production configuration.

## 3GPP Background
Per TS 23.501 Section 5.2.1, a single AMF serves multiple gNBs. Each gNB-AMF pair has an independent NG-C interface (SCTP association). The AMF maintains separate contexts for each gNB, identified by the GlobalRANNodeID received during NG Setup.

Each gNB gets a unique gNB ID (even if created from the same config profile, the tester assigns unique IDs). The AMF's served gNB list grows with each successful NG Setup.

This test creates 3 gNBs, connects all 3, then waits for all 3 to reach READY state. The AMF must process 3 concurrent NGSetupRequests without confusion or rejection.

**Network functions involved:** 3 gNBs, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup (per gNB) |
| TS 23.501 | 5.2.1 | AMF serving multiple gNBs |
| TS 38.412 | 7 | SCTP (per-gNB associations) |

## Problem Statement
- What if the AMF has a max gNB connection limit below 3?
- What if SCTP port contention occurs with 3 concurrent associations?
- What if the AMF confuses gNB contexts when processing concurrent setups?
- What if gNB ID collision occurs when creating from the same profile?

## Test Procedure (Step-by-Step)
1. Create 3 gNB instances from the same config profile (unique gNB IDs assigned).
2. Connect all 3 gNBs (3 SCTP associations established).
3. Wait for all 3 to reach READY state.
4. Verify each gNB is in READY state.
5. Teardown: remove all 3 gNBs.

## Expected Behavior
- 3 independent SCTP associations established to AMF port 38412.
- 3 NGSetupRequests processed concurrently.
- All 3 gNBs reach READY state.
- AMF maintains 3 separate gNB contexts.

## Pass/Fail Criteria
- **Pass:** All 3 gNBs reach READY state.
- **Fail:** Any gNB fails NG Setup; AMF rejects concurrent connections.

## Key Concepts for Training

### AMF Multi-gNB Architecture
In production, a single AMF instance serves 10-100+ gNBs. Each gNB has its own SCTP association and NG-C interface. The AMF identifies each gNB by GlobalRANNodeID and tracks: its supported TAs, slices, connected UEs, and connection state. This fan-out architecture allows centralized mobility management.

### SCTP Multi-Association
The AMF listens on port 38412 and accepts multiple SCTP associations. Each gNB connects as a separate SCTP client. The AMF uses the SCTP association identifier (socket/file descriptor) to distinguish between gNBs. Multiple associations can share the same server port because SCTP identifies associations by the 4-tuple (local IP, local port, remote IP, remote port).

### gNB ID Uniqueness
Each gNB must have a globally unique GlobalRANNodeID (PLMN + gNB-ID). If two gNBs send the same ID, the AMF will either reject the second or replace the first's context. The tester must assign unique IDs even when creating multiple gNBs from the same config template.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| AMF max gNB limit | 3rd gNB rejected | Increase AMF max-gnb config |
| gNB ID collision | 2nd gNB replaces 1st | Ensure unique gNB IDs per instance |
| Port contention | SCTP connection fails | Verify AMF accepts multiple associations |
| Concurrent processing | Setup responses delayed | Check AMF thread pool for NGAP |

## References
- 3GPP TS 23.501 V17.x -- Section 5.2.1 (AMF-gNB relationship)
- 3GPP TS 38.413 V17.x -- Section 8.7.1 (NG Setup)
- Related: TC-NGS-001 (single gNB), TC-NGS-007 (reconnect), TC-NGS-011 (NG Setup + registration)

## Quiz Questions
1. How does the AMF distinguish between 3 concurrent gNB SCTP connections on the same port?
   *Answer: Each SCTP association is identified by the 4-tuple (local IP, local port, remote IP, remote port). Even though the server port is always 38412, each gNB connects from a different source IP/port. At the application level, each association is a separate socket/file descriptor.*

2. What happens if two gNBs send the same GlobalRANNodeID in their NG Setup Requests?
   *Answer: The AMF treats this as the same gNB reconnecting. It may replace the first gNB's context with the second, or reject the second with NGSetupFailure. This causes the first gNB to lose its NG-C interface without notification.*

3. In a production network with 50 gNBs connected to one AMF, how many SCTP associations does the AMF maintain?
   *Answer: 50 -- one per gNB. Each association is independent with its own streams, buffers, and congestion state. The AMF must have sufficient socket resources and processing capacity for all 50 concurrent associations.*

# TC-STR-004: Register 16 UEs Sequential

## Overview
This test scales UE registration to 16 UEs on a single gNB. At this level, the test begins to stress SCTP stream management, AMF context table performance, and UDM query throughput. Sixteen UEs represents a micro-cell or indoor deployment scenario.

## 3GPP Background
At 16 UEs, the NG-C signaling load becomes more significant. Each UE requires: InitialUEMessage, DownlinkNASTransport (Auth Request), UplinkNASTransport (Auth Response), DownlinkNASTransport (Security Mode Command), UplinkNASTransport (Security Mode Complete), DownlinkNASTransport (Registration Accept), UplinkNASTransport (Registration Complete) -- approximately 7 NGAP messages per UE, totaling ~112 messages for 16 UEs.

The UDM must handle 16 authentication vector requests. If the UDM uses a database backend (MongoDB, PostgreSQL), the query pattern shifts from single-row lookups to batch processing.

**Network functions involved:** 16 UEs, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 24.501 | 5.5.1.2 | Initial Registration |
| TS 23.501 | 5.2 | AMF capacity and load |
| TS 38.413 | 8.6.1 | InitialUEMessage |
| TS 38.412 | 7 | SCTP transport scaling |

## Problem Statement
- What if SCTP send buffer fills up with 16 UEs' signaling queued?
- What if the AMF's NAS processing thread pool is undersized for 16 UEs?
- What if the UDM's SQN update lock creates contention at 16 concurrent updates?
- What if the total registration time scales non-linearly (suggesting O(n^2) processing)?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. For each of 16 UEs (UE_1 through UE_16):
   a. Perform full registration (5G-AKA, Security Mode, Registration Accept).
3. All 16 UEs registered.

## Expected Behavior
- All 16 UEs register successfully.
- Total time scales linearly (approximately 16x single-UE time).
- No SCTP congestion or message loss.
- AMF and UDM handle the load without errors.

## Pass/Fail Criteria
- **Pass:** All 16 UEs reach REGISTERED state.
- **Fail:** Any UE fails; non-linear timing degradation.

## Key Concepts for Training

### Linear vs. Non-linear Scaling
Healthy scaling means total time grows linearly with UE count: 16 UEs should take approximately 16x the time for 1 UE. If 4 UEs take 8s and 16 UEs take 60s instead of 32s, there is a scaling bottleneck. Common causes: O(n) context lookup becoming O(n^2), lock contention in the AMF, database connection pool exhaustion, SCTP congestion.

### SCTP Buffer Management
SCTP maintains send and receive buffers. With 16 UEs generating rapid signaling, the send buffer on the gNB side and receive buffer on the AMF side must be large enough to hold queued messages. Buffer overflow causes SCTP to block or drop messages, leading to registration timeouts.

### UDM Database Performance
The UDM stores subscriber profiles and SQN values. With 16 sequential auth requests, the database sees 16 read operations (fetch auth vectors) and 16 write operations (update SQN). If the database uses row-level locking, different subscribers don't contend. But if there's a global lock (poor implementation), 16 requests serialize and slow down.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| Non-linear timing | 16 UEs take 5x longer than expected | Profile AMF processing for O(n^2) patterns |
| SCTP buffer overflow | Messages dropped, registrations timeout | Increase SCTP send/receive buffer sizes |
| UDM bottleneck | Auth requests slow after UE 10 | Check UDM database connection pool |
| Thread pool saturated | AMF stops processing | Increase AMF worker thread count |

## References
- 3GPP TS 23.501 V17.x -- Section 5.2 (AMF capacity)
- 3GPP TS 38.412 V17.x -- Section 7 (SCTP transport)
- Related: TC-STR-003 (8 UEs), TC-STR-005 (32 UEs), TC-STR-012 (64 UEs)

## Quiz Questions
1. If sequential registration of 1 UE takes 2 seconds and 16 UEs take 40 seconds, what does this suggest?
   *Answer: Non-linear scaling. Expected time is 32 seconds (16 x 2s). The 25% overhead suggests a bottleneck that worsens with UE count, such as O(n) context lookups during each registration (total O(n^2)) or lock contention.*

2. How many NGAP messages are exchanged for registering 16 UEs (approximately)?
   *Answer: Approximately 112 messages (7 NGAP messages per UE x 16 UEs). This includes InitialUEMessage, multiple DownlinkNASTransport/UplinkNASTransport pairs for auth, security mode, and registration.*

3. At 16 UEs, what SCTP parameters should be tuned for optimal performance?
   *Answer: Outbound stream count (at least 8-16 for parallelism), send/receive buffer sizes (SO_SNDBUF/SO_RCVBUF), and max retransmission attempts (to avoid stalling on transient loss).*

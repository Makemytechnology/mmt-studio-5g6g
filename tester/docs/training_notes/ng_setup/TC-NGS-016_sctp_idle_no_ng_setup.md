# TC-NGS-016: SCTP Idle Connection -- No NG Setup Sent

## Overview
This negative test establishes an SCTP association to the AMF but intentionally does NOT send an NGSetupRequest. It observes the AMF's behavior with an idle SCTP connection: does the AMF keep the connection alive via heartbeats, or does it close it after an idle timeout? This tests the AMF's handling of misbehaving or stalled gNBs.

## 3GPP Background
Per TS 38.413 Section 8.7.1, the NG Setup procedure is the first NGAP procedure after SCTP association establishment. A well-behaved gNB immediately sends NGSetupRequest after SCTP connect. An idle SCTP connection (no NG Setup) represents an abnormal condition: a stalled gNB, a port scanner, or a misconfigured device.

SCTP maintains liveness through heartbeats (RFC 4960 Section 8.3). If enabled, SCTP sends heartbeat chunks periodically (default every 30 seconds). If the peer fails to respond to heartbeats after a configurable number of retries (path.max.retrans, default 5), the association is aborted.

The AMF may have an application-level idle timeout: if no NGAP message is received within N seconds after SCTP connect, close the association to free resources.

**Network functions involved:** gNB (SCTP only, no NGAP), AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 38.413 | 8.7.1 | NG Setup as first procedure |
| TS 38.412 | 7 | SCTP transport |
| RFC 4960 | 8.3 | SCTP heartbeat mechanism |

## Problem Statement
- What if the AMF keeps idle connections forever, wasting resources?
- What if the AMF closes the connection before the gNB can send NG Setup (too-aggressive timeout)?
- What if SCTP heartbeats keep the idle connection alive indefinitely?
- What if the AMF logs excessive warnings for idle connections?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration.
2. Establish SCTP association only (no NG Setup sent).
3. Hold the connection idle for 15 seconds.
4. After 15 seconds, check if SCTP is still connected.
5. Log the result (connected=True or False).
6. Teardown: remove gNB.

## Expected Behavior
Two valid outcomes:
- **Connection stays alive:** AMF keeps idle SCTP via heartbeats. The gNB could still send NG Setup later. (SCTP connected = True)
- **Connection closed by AMF:** AMF has an idle timeout and closes the association. (SCTP connected = False)

Both behaviors are acceptable. The test documents the AMF's behavior.

## Pass/Fail Criteria
- **Pass:** Test completes (either outcome is documented). No crash or unexpected behavior.
- **Fail:** Test crashes; SCTP stack error; undefined behavior.

## Key Concepts for Training

### Negative Testing
Negative tests verify system behavior under abnormal conditions. Instead of testing the happy path (NG Setup succeeds), this test verifies what happens when the expected procedure (NG Setup) is NOT performed. Negative tests catch: resource leaks (idle connections consuming memory), security vulnerabilities (unauthorized connections), and edge cases (timeout handling).

### SCTP Heartbeat Mechanism
SCTP heartbeats serve two purposes: (1) Path liveness verification -- confirm the peer is reachable, (2) RTT estimation -- measure round-trip time for retransmission timing. Heartbeats are sent on each idle path (no data sent recently). The interval is configurable. If path.max.retrans heartbeats fail, the path is marked failed.

### AMF Connection Management
The AMF should manage its connection table efficiently. Idle connections that never complete NG Setup waste: socket resources, connection table entries, and potentially SCTP stream resources. Production AMFs typically implement an idle connection timeout (e.g., 30-60 seconds) to clean up misbehaving connections.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| AMF resource leak | Many idle connections accumulate | Implement AMF idle timeout |
| Too-aggressive timeout | gNB can't send NG Setup in time | Increase AMF idle timeout to > 5s |
| Heartbeat failure | Connection drops unexpectedly | Check network path stability |
| SCTP stack error | Test crashes during idle | Check SCTP implementation robustness |

## References
- RFC 4960 -- Section 8.3 (SCTP heartbeats)
- 3GPP TS 38.413 V17.x -- Section 8.7.1 (NG Setup)
- Related: TC-NGS-001 (normal NG Setup), TC-NGS-007 (disconnect/reconnect)

## Quiz Questions
1. Why is an idle SCTP connection (no NG Setup) considered abnormal?
   *Answer: Per TS 38.413, NG Setup is the first NGAP procedure after SCTP association. A well-behaved gNB sends NGSetupRequest immediately. An idle connection could be: a stalled gNB, a port scanner probing port 38412, a misconfigured device, or a gNB that crashed after SCTP connect but before NG Setup.*

2. What are the two acceptable outcomes of this test, and why is neither a "failure"?
   *Answer: (1) Connection stays alive (AMF relies on SCTP heartbeats, no application timeout), (2) Connection closed by AMF (application-level idle timeout). Both are valid design choices. The test documents the behavior without judging which is "correct."*

3. How does the SCTP heartbeat mechanism work, and what parameters control it?
   *Answer: SCTP sends heartbeat chunks on idle paths at a configurable interval (HB.interval). If no heartbeat ACK is received after path.max.retrans attempts, the path is failed. If all paths fail, the association is aborted. Parameters are configurable via sysctl (Linux) or socket options.*

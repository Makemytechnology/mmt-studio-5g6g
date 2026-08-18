# TC-TRF-010: MBR Uplink Enforcement Test

## Overview
This test validates UPF enforcement of MBR for uplink traffic. The UE sends UDP at 100 Mbps (2x the 50 Mbps MBR), and the UPF should limit delivery to 50 Mbps. This complements TC-TRF-009 (MBR DL) and TC-TRF-008 (AMBR UL) by testing per-flow rate limiting in the uplink direction.

## 3GPP Background
UL MBR enforcement occurs at the UPF on the N3 interface. When the gNB forwards GTP-U packets exceeding the flow's MBR, the UPF applies policing via the QER. Excess packets are dropped (ul_dropped) or metered (ul_metered). Using UDP ensures the UE keeps sending at the target rate regardless of drops (no TCP feedback loop).

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7.2.4 | MBR per QoS flow |
| TS 29.244 | 5.4.4 | QER enforcement |

## Problem Statement
- What if UL MBR enforcement differs from DL (asymmetric bugs)?
- What if UL drops are not reported in UPF stats?
- What if the policing rate is calculated incorrectly for UL?

## Test Procedure (Step-by-Step)
1. Set UE subscription MBR_UL to 50 Mbps via SA Core API.
2. Register UE, establish PDU session.
3. Start iperf3 server on SA Core.
4. Collect UPF stats (baseline).
5. Run iperf3 UDP client at 100 Mbps from UE through GTP-U.
6. Collect UPF stats (after 10s), compute delta.
7. Verify delivered UL rate <= 60 Mbps (MBR + 20%).

## Expected Behavior
- UE sends 100 Mbps; UPF delivers <= 60 Mbps.
- ul_dropped > 0 (approximately 50% of packets).
- iperf3 server reports received throughput ~50 Mbps.

## Pass/Fail Criteria
- **Pass:** UL delivered <= MBR + 20%; drops > 0.
- **Fail:** No enforcement; severe over/under-enforcement.

## Key Concepts for Training

### UL vs DL MBR Enforcement Point
UL MBR is enforced when GTP-U packets arrive at the UPF from the gNB. The UPF matches the TEID (uplink PDR), applies the QER, and polices excess traffic. DL MBR is enforced when packets arrive from the DN. Both enforcement points are at the UPF, but on different packet processing paths.

### UDP for Rate Limit Testing
UDP is preferred for testing rate limits because: (1) UDP does not adapt its sending rate to drops (unlike TCP). (2) The exact send rate is controllable (iperf3 -b flag). (3) Packet loss is directly measurable. (4) The UPF sees a constant arrival rate, making policing behavior predictable.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| No UL enforcement | Full 100 Mbps delivered | Check UL QER on UPF |
| UL/DL asymmetric | UL works but DL doesn't | Check QER configuration per direction |
| No drops reported | ul_dropped = 0 but rate limited | Check if UPF uses shaping vs policing |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7.2.4 (MBR)
- Related: TC-TRF-009 (MBR DL), TC-TRF-008 (AMBR UL), TC-TRF-011 (GBR)

## Quiz Questions
1. Why is UDP preferred over TCP for testing MBR enforcement?
   *Answer: UDP maintains a constant send rate regardless of packet drops. TCP would detect drops and reduce its rate (congestion control), making it impossible to sustain 2x MBR and clearly observe enforcement. With UDP, the difference between sent and received directly shows the enforcement behavior.*

2. At 100 Mbps send and 50 Mbps MBR, approximately what percentage of packets should the UPF drop?
   *Answer: Approximately 50%. The UPF must drop half the packets to enforce the 50 Mbps limit. Due to policing algorithm granularity (token bucket timing), the actual drop rate may vary slightly.*

3. If UL MBR enforcement works but DL MBR enforcement does not, where is the likely bug?
   *Answer: The QER for the DL direction on the UPF. Possible causes: (1) DL QER not installed (SMF configuration issue), (2) DL PDR not linked to the QER, (3) UPF bug in DL policing path. The UL and DL enforcement paths are typically separate code paths in the UPF.*

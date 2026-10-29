# TC-TRF-009: MBR Downlink Enforcement Test

## Overview
This test validates UPF enforcement of MBR (Maximum Bit Rate) for downlink traffic. The core sends unlimited TCP to the UE, and the UPF should limit delivery to the configured MBR (50 Mbps DL). MBR is a per-flow rate limit, distinct from Session-AMBR which is per-session aggregate.

## 3GPP Background
MBR (TS 23.501 Section 5.7.2.4) defines the maximum bit rate for a single QoS flow. Unlike AMBR (aggregate across flows), MBR applies to each flow independently. For non-GBR flows, MBR may not be configured (defaulting to AMBR). For GBR flows, MBR limits the peak rate above the guaranteed rate.

The SMF sets MBR in the QER on the UPF. Downlink MBR enforcement occurs when the DN (or core iperf3) sends faster than the MBR. The UPF drops or shapes excess DL packets.

**Network functions involved:** Core iperf3, UPF (QER/MBR enforcement), gNB, UE

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7.2.4 | Maximum Bit Rate (MBR) |
| TS 29.244 | 5.4.4 | QER with MBR |
| TS 23.501 | 5.7.2.6 | MBR vs AMBR |

## Problem Statement
- What if MBR is not enforced, allowing unlimited DL throughput?
- What if MBR and AMBR conflict (MBR > AMBR)?
- What if the UPF's DL policing drops too aggressively?

## Test Procedure (Step-by-Step)
1. Set UE subscription MBR_DL to 50 Mbps via SA Core API.
2. Register UE, establish PDU session.
3. Start iperf3 server on UE side (bound to TUN IP).
4. Collect UPF stats (baseline).
5. Trigger core iperf3 client (unlimited TCP to UE).
6. Collect UPF stats (after 10s), compute delta.
7. Verify delivered rate <= 60 Mbps (MBR + 20%).

## Expected Behavior
- Core sends at maximum rate.
- UPF limits DL to 50 Mbps (+/- 20%).
- dl_dropped or dl_metered > 0.
- UE receives steady throughput at ~50 Mbps.

## Pass/Fail Criteria
- **Pass:** DL delivered <= MBR + 20%; drops/metered > 0.
- **Fail:** No enforcement; over/under enforcement.

## Key Concepts for Training

### MBR vs AMBR
- **MBR:** Per-flow maximum. Applies to individual QoS flows. Can be UL and DL independently.
- **AMBR:** Per-session aggregate. Sum of all non-GBR flows limited to AMBR.
- If a PDU session has one non-GBR flow: effective limit = min(MBR, AMBR).
- If a PDU session has two non-GBR flows: each limited by MBR, combined limited by AMBR.

### TCP Behavior Under Rate Limiting
When the UPF drops packets from a TCP stream, TCP's congestion control detects the loss (via retransmission timeout or duplicate ACKs) and reduces its sending rate. TCP adapts to the MBR limit over a few seconds. The delivered rate stabilizes near the MBR. This adaptation is automatic with TCP, unlike UDP which keeps sending regardless.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| No MBR enforcement | Delivered = full link speed | Verify QER MBR configuration |
| TCP oscillation | Throughput fluctuates wildly | Normal TCP behavior with policing |
| Over-enforcement | Delivered << 50 Mbps | Check policing algorithm parameters |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7.2.4 (MBR)
- Related: TC-TRF-010 (MBR UL), TC-TRF-008 (AMBR), TC-TRF-011 (GBR)

## Quiz Questions
1. What is the relationship between MBR, AMBR, and GBR for a single QoS flow?
   *Answer: GBR <= MBR (guaranteed rate cannot exceed max rate). AMBR is the aggregate limit across all non-GBR flows. For a GBR flow: GBR is the minimum, MBR is the maximum. For a non-GBR flow: MBR is the per-flow max, AMBR is the session aggregate max.*

2. Why does TCP throughput stabilize near the MBR limit even though the sender tries to send faster?
   *Answer: TCP's congestion control detects packet drops (caused by MBR enforcement) and reduces the congestion window. Over several RTTs, TCP's AIMD (Additive Increase, Multiplicative Decrease) algorithm converges the sending rate to match the MBR. This is a feedback loop: send too fast -> drops -> reduce rate -> no drops -> increase rate -> send too fast -> repeat.*

3. If MBR_DL = 50 Mbps but AMBR_DL = 30 Mbps, what is the effective DL rate limit?
   *Answer: 30 Mbps. The AMBR applies to the aggregate of all non-GBR flows. Even though the per-flow MBR allows 50 Mbps, the session-level AMBR limits the total to 30 Mbps. Effective limit = min(MBR, AMBR) for a single non-GBR flow.*

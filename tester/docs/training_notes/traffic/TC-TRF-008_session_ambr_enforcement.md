# TC-TRF-008: Session-AMBR Uplink Enforcement Test

## Overview
This test validates that the UPF correctly enforces Session-AMBR (Aggregate Maximum Bit Rate) for uplink traffic. The UE sends at 100 Mbps (2x the 50 Mbps AMBR), and the UPF should limit delivered traffic to 50 Mbps (+/- 20% tolerance), dropping or shaping excess packets.

## 3GPP Background
Session-AMBR (TS 23.501 Section 5.7.2.6) is the maximum aggregate bit rate across all non-GBR QoS flows in a PDU session. It is defined per direction (UL-AMBR and DL-AMBR) in the UE's subscription data. The SMF configures a QER (QoS Enforcement Rule) on the UPF with the AMBR values.

When the UE sends above the UL-AMBR, the UPF must enforce the limit. Enforcement methods: (1) **Policing (dropping):** Excess packets are dropped. UPF counter: ul_dropped. (2) **Shaping (buffering):** Excess packets are buffered and released at the AMBR rate. UPF counter: ul_metered. (3) **Marking:** Excess packets are marked for potential dropping downstream.

The test sends at 2x AMBR to clearly exceed the limit. The UPF's delivered rate should be at or below the AMBR (+20% tolerance for burst handling).

**Network functions involved:** UE, gNB, UPF (QER enforcement), SMF (QER configuration)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7.2.6 | Session-AMBR definition |
| TS 29.244 | 5.4.4 | QER (QoS Enforcement Rule) |
| TS 23.503 | 6.1.2 | QoS policy control |

## Problem Statement
- What if the UPF does not enforce AMBR at all (unlimited throughput)?
- What if the AMBR enforcement is too aggressive (drops below the limit)?
- What if the subscription AMBR is not correctly propagated from UDM to SMF to UPF?
- What if the UPF's policing algorithm is bursty (allows bursts above AMBR)?

## Test Procedure (Step-by-Step)
1. Set UE subscription AMBR to 50 Mbps UL via SA Core API.
2. Register UE, establish PDU session (AMBR applied by SMF -> UPF QER).
3. Collect UPF stats (baseline).
4. Send UDP traffic at 100 Mbps through GTP-U for 10 seconds.
5. Collect UPF stats (after).
6. Compare: UPF delivered rate should be <= 50 Mbps + 20% tolerance.
7. Verify ul_dropped or ul_metered > 0.

## Expected Behavior
- UE sends at 100 Mbps.
- UPF delivers <= 60 Mbps (50 Mbps AMBR + 20% tolerance).
- ul_dropped or ul_metered > 0 (excess traffic policed).
- No packet corruption or tunnel disruption.

## Pass/Fail Criteria
- **Pass:** Delivered rate <= AMBR + 20%; UPF policing active (drops/metered > 0).
- **Fail:** Delivered rate > AMBR + 20% (no enforcement); or delivered rate << AMBR (over-enforcement).

## Key Concepts for Training

### Session-AMBR vs. MBR vs. GBR
- **Session-AMBR:** Aggregate max rate for ALL non-GBR flows in a PDU session. Applied per PDU session.
- **MBR (Maximum Bit Rate):** Max rate for a SINGLE QoS flow. Applied per flow. Can be tighter than AMBR.
- **GBR (Guaranteed Bit Rate):** Minimum guaranteed rate for a GBR QoS flow. Resources are reserved. GBR flows are NOT counted against AMBR.

### QER on the UPF
The QER is a PFCP rule that the SMF installs on the UPF. It contains: Gate Status (open/closed), MBR (UL/DL), GBR (UL/DL), and QFI. For AMBR enforcement, a QER with MBR = AMBR is created. The UPF applies token bucket or leaky bucket policing to enforce the rate.

### Token Bucket Policing
Common rate-limiting algorithm: tokens are added to a bucket at the AMBR rate. Each packet consumes tokens proportional to its size. If the bucket is empty, the packet is dropped (policing) or queued (shaping). Burst tolerance is controlled by the bucket size (CBS: Committed Burst Size).

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| No AMBR enforcement | Delivered rate = send rate | Check QER installation on UPF |
| Over-enforcement | Delivered rate << AMBR | Check QER rate value, policing algorithm |
| Wrong AMBR value | Enforcement at wrong rate | Verify subscription AMBR in UDM/SMF |
| No drops reported | ul_dropped = 0 but rate limited | UPF may use shaping (check ul_metered) |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7.2.6 (Session-AMBR)
- 3GPP TS 29.244 V17.x -- Section 5.4.4 (QER)
- Related: TC-TRF-009 (MBR DL), TC-TRF-010 (MBR UL), TC-TRF-011 (GBR)

## Quiz Questions
1. What is the difference between Session-AMBR and per-flow MBR?
   *Answer: Session-AMBR is the aggregate maximum across all non-GBR flows in a PDU session. MBR is the maximum for a single QoS flow. Example: AMBR=100 Mbps with two flows -- each flow could have MBR=80 Mbps, but their combined rate is capped at 100 Mbps by AMBR.*

2. When the UE sends at 100 Mbps but AMBR is 50 Mbps, what happens to the excess 50 Mbps?
   *Answer: The UPF's QER drops (polices) or buffers (shapes) the excess packets. In policing mode, approximately 50% of packets are dropped. UPF counters show ul_dropped > 0. The remaining ~50 Mbps is forwarded to the DN.*

3. Are GBR QoS flows (e.g., 5QI=1 voice) subject to Session-AMBR enforcement?
   *Answer: No. GBR flows are NOT counted against Session-AMBR. AMBR only applies to non-GBR flows. GBR flows have dedicated resources (guaranteed rate) and their own MBR limit, independent of AMBR.*

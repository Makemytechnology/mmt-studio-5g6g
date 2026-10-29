# TC-TRF-011: GBR Guaranteed Bit Rate Delivery Test

## Overview
This test validates that a GBR (Guaranteed Bit Rate) QoS flow delivers at least its guaranteed rate with minimal loss and jitter. The UE sends UDP at exactly the GBR rate (10 Mbps), and the UPF must deliver all traffic without drops. GBR flows are essential for voice (5QI=1) and video (5QI=2) services.

## 3GPP Background
GBR QoS flows (TS 23.501 Section 5.7.2.3) have reserved resources that guarantee a minimum bit rate. Unlike non-GBR flows (best effort), GBR flows are not subject to Session-AMBR enforcement. The network reserves bandwidth and processing capacity to ensure the guaranteed rate is always available.

GBR parameters per flow: **GBR** (minimum guaranteed rate), **MBR** (maximum rate, >= GBR), **5QI** (determines PDB, PER, priority). The SMF configures a QER with both GBR and MBR values. The UPF ensures: (1) traffic up to GBR is never dropped due to congestion, (2) traffic between GBR and MBR is delivered on best-effort basis, (3) traffic above MBR is dropped.

Common GBR 5QIs: 5QI=1 (voice, GBR, PDB=100ms), 5QI=2 (video, GBR, PDB=150ms), 5QI=65 (mission-critical voice, GBR, PDB=75ms).

**Network functions involved:** UE, gNB, UPF (GBR enforcement)

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 23.501 | 5.7.2.3 | GBR QoS flows |
| TS 23.501 | 5.7.2.4 | MBR for GBR flows |
| TS 29.244 | 5.4.4 | QER with GBR/MBR |
| TS 23.501 | 5.7.2.1 | 5QI=1 (voice GBR) |

## Problem Statement
- What if the UPF drops GBR traffic under congestion?
- What if GBR reservation is not made, treating it as best-effort?
- What if the delivered rate is < 90% of GBR (guarantee violation)?
- What if jitter is too high for voice-quality GBR flows?

## Test Procedure (Step-by-Step)
1. Create gNB, register UE, establish PDU session with GBR flow (10 Mbps).
2. Start iperf3 server on SA Core.
3. Collect UPF stats (baseline).
4. Run iperf3 UDP client at exactly 10 Mbps GBR rate.
5. Collect UPF stats (after 10s).
6. Verify delivered rate >= 90% of GBR (9 Mbps).
7. Verify jitter < 50ms and loss < 1%.

## Expected Behavior
- All 10 Mbps delivered without drops (UPF ul_dropped = 0).
- Delivered rate >= 9 Mbps (90% of GBR).
- Jitter < 50ms; packet loss < 1%.
- GBR resources reserved at UPF.

## Pass/Fail Criteria
- **Pass:** Delivered >= 90% GBR; jitter < 50ms; loss < 1%; drops = 0.
- **Fail:** Delivered < 90% GBR; excessive loss; UPF drops GBR traffic.

## Key Concepts for Training

### GBR Resource Reservation
When a GBR QoS flow is established, the SMF instructs the UPF to reserve resources: bandwidth allocation, buffer space, and processing priority. The UPF guarantees that traffic up to the GBR rate will not be dropped due to congestion. This is fundamentally different from non-GBR flows where traffic may be dropped under congestion.

### GBR vs Non-GBR
| Feature | GBR Flow | Non-GBR Flow |
|---------|----------|-------------|
| Guaranteed rate | Yes (GBR parameter) | No |
| Subject to AMBR | No | Yes |
| Resource reservation | Yes | No |
| Examples | Voice (5QI=1), Video (5QI=2) | Internet (5QI=9), IMS signaling (5QI=5) |

### Bearer Activation for GBR
GBR flows are typically created dynamically when needed (e.g., during a VoNR call). The PCF/P-CSCF triggers dedicated bearer activation via the Rx interface. The SMF creates the GBR QoS flow with specific QER parameters. After the call ends, the GBR flow is released.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| GBR not reserved | Traffic dropped under load | Verify QER has GBR value set |
| Low delivered rate | < 90% GBR | Check UPF scheduling/priority |
| High jitter | > 50ms | Check UPF processing priority for GBR |
| Drops at GBR rate | ul_dropped > 0 at 10 Mbps | GBR reservation not enforced |

## References
- 3GPP TS 23.501 V17.x -- Section 5.7.2.3 (GBR flows)
- 3GPP TS 29.244 V17.x -- Section 5.4.4 (QER)
- Related: TC-TRF-008 (AMBR), TC-TRF-009/010 (MBR), TC-IMS-003 (voice GBR)

## Quiz Questions
1. What is the key difference between GBR and non-GBR QoS flows?
   *Answer: GBR flows have reserved resources guaranteeing a minimum bit rate (the GBR value). Non-GBR flows are best-effort with no guaranteed rate -- traffic may be dropped under congestion. GBR flows are also exempt from Session-AMBR limits.*

2. If a GBR flow has GBR=10 Mbps and MBR=20 Mbps, what happens at 15 Mbps send rate?
   *Answer: 10 Mbps is guaranteed (never dropped). The additional 5 Mbps (between GBR and MBR) is delivered on a best-effort basis -- it may be dropped under congestion but is allowed under normal conditions. Traffic above 20 Mbps would be dropped regardless.*

3. Why are GBR flows exempt from Session-AMBR enforcement?
   *Answer: Session-AMBR limits aggregate non-GBR traffic to prevent best-effort flows from consuming excessive bandwidth. GBR flows have dedicated reserved resources and do not compete with non-GBR traffic. Including GBR in AMBR would defeat the purpose of the guarantee, as the AMBR limit could cause drops on guaranteed traffic.*

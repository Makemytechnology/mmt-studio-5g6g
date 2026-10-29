# TC-AUTH-002: NAS Security Algorithm Negotiation

## Overview
This test validates that the NAS Security Mode Command procedure correctly negotiates ciphering (EEA) and integrity (EIA) algorithms. It specifically checks that the integrity algorithm is non-null (EIA > 0), which is mandatory per TS 33.501 for initial registration.

## 3GPP Background
After 5G-AKA authentication, the AMF initiates the Security Mode Command procedure (TS 24.501 Section 5.4.2). The AMF selects algorithms based on the intersection of UE capabilities and AMF policy.

**Algorithm identifiers (TS 33.501 Section 6.7.2):**
- EEA0: Null ciphering | EIA0: Null integrity (FORBIDDEN for initial registration)
- EEA1: 128-Snow3G | EIA1: 128-Snow3G-MAC
- EEA2: 128-AES-CTR | EIA2: 128-AES-CMAC
- EEA3: 128-ZUC | EIA3: 128-ZUC-MAC

The Security Mode Command carries: selected NAS ciphering algorithm (EEA), selected NAS integrity algorithm (EIA), ngKSI (key set identifier), and replayed UE security capabilities (anti-bidding-down).

**Network functions involved:** UE, AMF

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.501 | 6.7.2 | NAS algorithm selection |
| TS 24.501 | 5.4.2 | Security Mode Command |
| TS 33.501 | A.8 | KNASint/KNASenc derivation |

## Problem Statement
- What if EIA0 is selected, violating security requirements?
- What if the AMF and UE have no common algorithm?
- What if the replayed UE capabilities do not match, indicating a bidding-down attack?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, NG Setup.
2. Register UE (5G-AKA authentication).
3. During registration, Security Mode Command negotiates EEA/EIA.
4. Extract negotiated algorithms from UE security context.
5. Assert EIA > 0 (non-null integrity).

## Expected Behavior
- Security Mode Command received with valid algorithm selections.
- EIA is 1 (Snow3G), 2 (AES), or 3 (ZUC).
- EEA may be 0 (null) or non-zero.

## Pass/Fail Criteria
- **Pass:** EIA > 0.
- **Fail:** EIA = 0 (null integrity selected).

## Key Concepts for Training

### Algorithm Selection Priority
The AMF maintains an operator-configured priority list. Example: EIA preference = [EIA2, EIA1, EIA3]; EEA preference = [EEA2, EEA1, EEA0]. The AMF selects the highest-priority algorithm that appears in both the AMF policy and UE capabilities. If no match exists, registration fails.

### Anti-Bidding-Down Protection
The Security Mode Command replays the UE's security capabilities (received in the Registration Request). The UE compares the replayed capabilities with what it originally sent. If they differ, a man-in-the-middle may have modified the Registration Request to remove strong algorithms. The UE rejects the SMC in this case.

### NAS Key Derivation with Algorithm ID
KNASint = KDF(KAMF, FC=0x69, algorithm_type=0x02, algorithm_id=EIA_value). KNASenc = KDF(KAMF, FC=0x69, algorithm_type=0x01, algorithm_id=EEA_value). Different algorithm selections produce different keys even from the same KAMF.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| EIA0 selected | Test fails | Configure AMF to mandate EIA1+ |
| No common algorithm | Security Mode Reject | Update UE or AMF algorithm lists |
| Capability mismatch | UE rejects SMC | Verify no middlebox modifying NAS |

## References
- 3GPP TS 33.501 V17.x -- Section 6.7.2 (Algorithm selection)
- 3GPP TS 24.501 V17.x -- Section 5.4.2 (Security Mode Command)
- Related: TC-REG-005 (algorithm verify), TC-AUTH-001 (5G-AKA)

## Quiz Questions
1. Which NAS security algorithm is explicitly forbidden during initial registration and why?
   *Answer: EIA0 (null integrity). Without integrity protection, NAS messages can be tampered with, enabling MITM attacks on critical signaling like Registration Accept/Reject, security capability manipulation, and identity theft.*

2. How does the UE detect a bidding-down attack during Security Mode Command?
   *Answer: The SMC replays the UE's security capabilities (originally sent in the Registration Request). The UE compares the replayed capabilities with its actual capabilities. If they differ, an attacker modified the Registration Request in transit to remove strong algorithms. The UE sends Security Mode Reject.*

3. If the AMF selects EIA2 and EEA1, what key derivation parameters produce KNASint?
   *Answer: KNASint = KDF(KAMF, FC=0x69, algorithm_type_distinguisher=0x02, algorithm_identity=0x02). The algorithm_identity is the EIA value (2 for AES-CMAC). KNASenc uses algorithm_type=0x01 with EEA value (1 for Snow3G).*

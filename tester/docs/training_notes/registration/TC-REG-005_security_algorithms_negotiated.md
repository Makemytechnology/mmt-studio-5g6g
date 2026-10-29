# TC-REG-005: NAS Security Algorithm Verification

## Overview
This test validates that the NAS Security Mode Command procedure correctly negotiates ciphering (EEA) and integrity (EIA) algorithms during registration. It ensures that the AMF selects a non-null integrity algorithm (EIA > 0), which is mandatory per TS 33.501. This test catches misconfiguration of security policies that could leave NAS signaling unprotected.

## 3GPP Background
After successful 5G-AKA authentication, the AMF initiates the NAS Security Mode Command procedure (TS 24.501 Section 5.4.2). The AMF selects algorithms based on: (1) UE security capabilities (reported in the Registration Request as a list of supported EEA/EIA algorithms), and (2) AMF security policy (operator-configured algorithm priority list).

The Security Mode Command message contains:
- Selected NAS security algorithms (EEA for ciphering, EIA for integrity)
- NAS key set identifier (ngKSI) linking to the current security context
- Replayed UE security capabilities (for integrity verification)

Available algorithms:
- **EEA0:** Null ciphering (no encryption) -- allowed but not recommended
- **EEA1:** 128-bit Snow3G (SNOW 3G stream cipher)
- **EEA2:** 128-bit AES-CTR (AES in counter mode)
- **EEA3:** 128-bit ZUC (Chinese stream cipher)
- **EIA0:** Null integrity (no protection) -- FORBIDDEN for initial registration
- **EIA1:** 128-bit Snow3G MAC (SNOW 3G message authentication)
- **EIA2:** 128-bit AES-CMAC (AES-based MAC)
- **EIA3:** 128-bit ZUC MAC

The UE activates NAS security upon receiving the Security Mode Command and responds with Security Mode Complete, which is the first NAS message sent with the new security context.

**Network functions involved:** UE, AMF
**Key derivation:** From KAMF, the AMF and UE derive KNASint and KNASenc using the selected algorithm identifiers.

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.501 | 6.7.2 | NAS security algorithm selection |
| TS 24.501 | 5.4.2 | Security Mode Command procedure |
| TS 33.501 | 6.7.1 | NAS security context |
| TS 33.501 | A.8 | KNASint/KNASenc derivation |
| TS 33.401 | Annex B | Algorithm identifiers (EEA/EIA values) |

## Problem Statement
- What if the AMF selects EIA0 (null integrity), violating TS 33.501 mandatory requirements?
- What if the AMF and UE have no overlapping algorithm support?
- What if KNASint or KNASenc derivation fails due to a key hierarchy error?
- What if the replayed UE security capabilities in SMC don't match what the UE sent?
- What if the operator hasn't configured any EIA/EEA algorithms on the AMF?

## Test Procedure (Step-by-Step)
1. Create gNB from configuration, establish SCTP, complete NG Setup.
2. Register UE via full NAS procedure (5G-AKA authentication).
3. During Security Mode Command, the AMF selects EEA and EIA algorithms.
4. UE activates NAS security and sends Security Mode Complete.
5. Registration completes with Registration Accept.
6. Verify UE has NAS security keys (KNASint must be present).
7. Extract the negotiated EEA and EIA algorithm identifiers from UE security context.
8. Assert that EIA > 0 (null integrity is forbidden).

## Expected Behavior
- Security Mode Command received with valid EEA and EIA algorithm selections.
- EIA algorithm is EIA1, EIA2, or EIA3 (never EIA0 for initial registration).
- KNASint and KNASenc keys are derived and stored in the UE security context.
- Security Mode Complete is sent with integrity protection using the negotiated EIA algorithm.
- Subsequent NAS messages are integrity-protected and optionally ciphered.

## Pass/Fail Criteria
- **Pass:** EIA algorithm > 0 (non-null integrity negotiated); NAS security keys (KNASint) present in UE context.
- **Fail:** EIA = 0 (null integrity); no security keys derived; Security Mode Command not received.

## Key Concepts for Training

### NAS Security Algorithm Selection
The AMF maintains an operator-configured priority list of algorithms (e.g., prefer EIA2 > EIA1 > EIA3). It intersects this list with the UE's reported capabilities and selects the highest-priority match. If no common algorithm exists, the AMF rejects the registration. EIA0 is included in the specification but is explicitly forbidden for initial registration -- only permitted in emergency calls without security.

### Key Derivation for NAS Security
From KAMF (derived during 5G-AKA), the NAS keys are derived using the KDF (Key Derivation Function):
- KNASint = KDF(KAMF, algorithm type distinguisher=0x02, algorithm identity=EIA value)
- KNASenc = KDF(KAMF, algorithm type distinguisher=0x01, algorithm identity=EEA value)

The algorithm identity is the integer value (1 for Snow3G, 2 for AES, 3 for ZUC). This means different algorithm selections produce different keys even from the same KAMF.

### Security Mode Command Integrity
The Security Mode Command is the first NAS message that the UE must verify with integrity protection. It includes a MAC (Message Authentication Code) computed using the newly derived KNASint. The UE verifies this MAC before activating the security context. If MAC verification fails, the UE rejects the SMC.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| EIA0 selected | Test assertion fails on EIA > 0 | Configure AMF to mandate EIA1 or EIA2 |
| No common algorithms | Security Mode Reject from UE | Check UE capability list vs AMF policy |
| Missing KNASint | Security keys not derived | Verify KAMF derivation succeeded in 5G-AKA |
| SMC MAC verification fail | UE sends Security Mode Reject | Check key derivation order and parameters |
| Replayed capabilities mismatch | UE detects bidding-down attack | AMF must replay exact UE capabilities |

## References
- 3GPP TS 33.501 V17.x -- Section 6.7 (NAS security)
- 3GPP TS 24.501 V17.x -- Section 5.4.2 (Security Mode Command)
- 3GPP TS 33.501 V17.x -- Annex A.8 (Key derivation for NAS)
- Related: TC-REG-001 (registration), TC-AUTH-001 (authentication), TC-AUTH-002 (algorithm negotiation)

## Quiz Questions
1. Why is EIA0 (null integrity) forbidden during initial registration per TS 33.501?
   *Answer: NAS integrity protection is mandatory to prevent man-in-the-middle and tampering attacks on critical signaling like Registration Request/Accept. Without integrity protection, an attacker could modify NAS messages to redirect the UE, downgrade security, or inject false identities.*

2. How does the AMF select which EEA/EIA algorithms to use when multiple algorithms are supported by both sides?
   *Answer: The AMF maintains an operator-configured priority list of algorithms. It intersects this list with the UE's reported security capabilities (sent in the Registration Request) and selects the highest-priority algorithm that both sides support.*

3. What is the "bidding-down attack" that replayed UE security capabilities in the Security Mode Command are designed to prevent?
   *Answer: An attacker could modify the UE's Registration Request in transit to remove strong algorithms from the capability list, tricking the AMF into selecting a weaker algorithm. By replaying the capabilities in the integrity-protected SMC, the UE can verify that the AMF received the original, unmodified capability list.*

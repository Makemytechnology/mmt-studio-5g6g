# TC-AUTH-001: 5G-AKA Authentication Success

## Overview
This test validates the complete 5G-AKA (5G Authentication and Key Agreement) procedure. It is the core authentication test that verifies mutual authentication between the UE and network, key derivation, and NAS security establishment. Successful 5G-AKA is a prerequisite for all other 5G services.

## 3GPP Background
5G-AKA (TS 33.501 Section 6.1.3) is the primary authentication method in 5G SA. It provides mutual authentication: the network authenticates the UE (by verifying RES*) and the UE authenticates the network (by verifying AUTN).

**Detailed procedure:**
1. UE sends Registration Request with SUCI (concealed IMSI using ECIES encryption).
2. AMF sends Nausf_UEAuthentication_Authenticate to AUSF with SUCI and serving network name.
3. AUSF sends Nudm_UEAuthentication_Get to UDM to retrieve authentication vectors.
4. UDM decrypts SUCI to SUPI, retrieves K and OPc, generates AV using **Milenage**:
   - Input: K (128-bit permanent key), RAND (128-bit random), OPc (derived operator key), SQN (48-bit sequence number), AMF (16-bit authentication management field)
   - Output: XRES, AUTN, CK, IK
   - Derives: CK' and IK' using serving network name -> KAUSF -> KSEAF -> KAMF
5. AMF sends Authentication Request (NAS) carrying RAND and AUTN to UE.
6. UE (USIM) runs Milenage with same K, OPc, and received RAND:
   - Verifies AUTN: checks MAC (network authentication) and SQN freshness
   - Computes RES, derives CK, IK -> KAUSF -> KSEAF -> KAMF -> KNASint, KNASenc
   - Computes RES* = KDF(CK'||IK', serving_network_name, RAND, RES)
7. UE sends Authentication Response with RES*.
8. AUSF verifies RES* == XRES* (UE authentication).
9. AMF proceeds to Security Mode Command.

**Network functions involved:** UE, gNB, AMF, AUSF, UDM

## Relevant 3GPP Specifications
| Specification | Section | Topic |
|--------------|---------|-------|
| TS 33.501 | 6.1.3 | 5G-AKA procedure |
| TS 33.501 | 6.1.2 | Authentication framework |
| TS 33.501 | A.2-A.8 | Key derivation functions |
| TS 35.205-208 | * | Milenage algorithm set |
| TS 24.501 | 5.4.1 | NAS Authentication procedure |

## Problem Statement
- What if K or OPc in the UE does not match the UDM?
- What if the serving network name is incorrect?
- What if AUTN verification fails (MAC mismatch)?
- What if RES* does not match XRES* (computation error)?
- What if the SUCI decryption fails at the UDM?

## Test Procedure (Step-by-Step)
1. Create gNB, connect SCTP, complete NG Setup.
2. Register UE: send Registration Request with SUCI.
3. Receive Authentication Request (RAND, AUTN).
4. UE verifies AUTN and computes RES*.
5. Send Authentication Response (RES*).
6. Receive Security Mode Command. UE activates NAS security.
7. Send Security Mode Complete.
8. Receive Registration Accept.
9. Verify UE is REGISTERED.
10. Verify UE has NAS security keys (KNASint present).

## Expected Behavior
- AUTN verification succeeds (MAC matches, SQN in range).
- RES* computed and sent successfully.
- Authentication succeeds (AUSF accepts RES*).
- NAS security keys derived: KAMF, KNASint, KNASenc.
- UE reaches REGISTERED state.

## Pass/Fail Criteria
- **Pass:** UE REGISTERED; security keys present; KNASint exists.
- **Fail:** Authentication failure; no keys; UE not registered.

## Key Concepts for Training

### Milenage Algorithm Set
Milenage (TS 35.205-208) is the standard algorithm for 3GPP authentication. It uses AES-128 as the core block cipher. Input: permanent key K (128 bits), operator constant OPc (derived from OP), random challenge RAND (128 bits). Output: five values f1-f5 producing MAC-A/MAC-S (for AUTN verification), RES (response), CK (cipher key), IK (integrity key), AK (anonymity key, for SQN concealment in AUTN).

### 5G Key Hierarchy
The key derivation chain in 5G:
K (USIM permanent) -> CK, IK (Milenage) -> CK', IK' (with serving network) -> KAUSF -> KSEAF -> KAMF -> KNASint (NAS integrity) + KNASenc (NAS ciphering) + KgNB (gNB key) -> KRRCint + KRRCenc + KUPint + KUPenc (AS keys)

Each derivation uses HMAC-SHA-256 as the KDF (Key Derivation Function) with specific input parameters per TS 33.501 Annex A.

### Mutual Authentication
5G-AKA provides mutual authentication: (1) Network authenticates UE by verifying RES* matches XRES*. (2) UE authenticates network by verifying MAC in AUTN -- only a legitimate network with access to K can produce a valid MAC. This prevents rogue base station attacks.

## Common Issues and Troubleshooting
| Issue | Symptom | Resolution |
|-------|---------|------------|
| K mismatch | Authentication Reject | Verify K matches between UE and UDM |
| OPc mismatch | MAC verification failure at UE | Verify OPc (derived from OP and K) |
| SQN out of sync | Auth Failure cause=21 + AUTS | UDM resyncs SQN from AUTS |
| Wrong serving network name | KAUSF mismatch | Verify SN-name format: "5G:mnc{MNC}.mcc{MCC}.3gppnetwork.org" |
| SUCI decryption failure | UDM can't identify subscriber | Check home network public key for SUCI encryption |

## References
- 3GPP TS 33.501 V17.x -- Section 6.1.3 (5G-AKA)
- 3GPP TS 35.205-208 V17.x -- Milenage algorithm
- 3GPP TS 33.501 V17.x -- Annex A (Key derivations)
- Related: TC-AUTH-002 (algorithms), TC-AUTH-003 (SQN resync), TC-REG-001 (registration)

## Quiz Questions
1. In the Milenage algorithm, what are the inputs and what are the five output functions?
   *Answer: Inputs: K (permanent key), RAND (random), OPc (operator constant), SQN (sequence number), AMF (auth management field). Outputs: f1=MAC-A (network auth), f1*=MAC-S (resync), f2=RES (UE auth response), f3=CK (cipher key), f4=IK (integrity key), f5=AK (anonymity key).*

2. How does the UE verify that it is communicating with a legitimate network during 5G-AKA?
   *Answer: The UE verifies the MAC field in AUTN. AUTN = SQN xor AK || AMF || MAC-A. The UE computes expected MAC using its K and received RAND. If expected MAC != received MAC, the network is not legitimate (does not possess K). This is mutual authentication -- the UE authenticates the network.*

3. What is the complete key derivation chain from the USIM permanent key K to the NAS integrity key KNASint?
   *Answer: K -> (Milenage with RAND) -> CK, IK -> (with serving network name) -> CK', IK' -> (KDF) -> KAUSF -> (KDF) -> KSEAF -> (KDF with ABBA) -> KAMF -> (KDF with algorithm type=0x02, algorithm ID=EIA value) -> KNASint.*

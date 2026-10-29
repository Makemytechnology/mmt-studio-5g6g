# AUSF — Authentication Server Function

3GPP TS 33.501 §6.1 / TS 29.509 AUSF; co-located ARPF + SIDF.
~460 LOC at `nf/ausf/`. Generates 5G-AKA authentication vectors and
de-conceals SUCI to recover SUPI.

## 1. Role in 5GC

The AUSF runs primary authentication on behalf of the home network.
It pulls long-term credentials from UDM/UDR, runs Milenage and the 5G
key derivation ladder, and returns a vector to the SEAF/AMF. The
SIDF (Subscription Identifier De-concealing Function) is co-located —
spec puts it in UDM (TS 33.501 §6.12) but this build hosts it in
AUSF for compactness (`nf/ausf/sidf.go:7`).

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Nausf** | AMF (SEAF) | Nausf_UEAuthentication | TS 33.501 §6.1.3.2 / TS 29.509 |
| (intra-NF) | UDM | `udm.GetAuthData` / `udm.UpdateAuthSQN` | TS 29.503 §5.4 |
| (intra-NF) | UDR | `udr.IncrementSQN` (SQN math) | — |

The Nausf REST envelope is not yet modelled — calls land as Go
functions on `package ausf`.

## 2. Architecture

```
       AMF / SEAF                          AUSF                       UDM / UDR
           │                                 │                            │
           │ Nausf_UEAuthenticate            │                            │
           ├────────────────────────────────►│                            │
           │  (imsi, snName, abba)           │                            │
           │                                 │ udm.GetAuthData(imsi) ────►│
           │                                 │◄─── (K, OP/OPC, SQN, AMF) ─│
           │                                 │                            │
           │                                 │ Milenage f1, f2345         │
           │                                 │   → MAC-A, RES, CK, IK, AK │
           │                                 │ AUTN = (SQN⊕AK)‖AMF‖MAC    │
           │                                 │                            │
           │                                 │ KDF chain (TS 33.501 §A):  │
           │                                 │   ConvA2 → KAUSF           │
           │                                 │   ConvA4 → RES* (XRES*)    │
           │                                 │   ConvA6 → KSEAF           │
           │                                 │   ConvA7 → KAMF            │
           │                                 │                            │
           │                                 │ udm.UpdateAuthSQN(SQN+1) ─►│
           │ AV                              │                            │
           │◄────────────────────────────────│                            │
           │  (RAND, AUTN, XRES*,            │                            │
           │   KAUSF, KSEAF, KAMF)           │                            │
```

SEAF and AMF live in the same Go process today, so KSEAF / KAMF are
returned alongside the vector even though they are not strictly
on the Nausf interface (`ausf.go:30-42`).

## 3. Package / file map

| File | Role |
|------|------|
| `nf/ausf/ausf.go` | 5G-AKA AV generation (`GenerateAV`), SQN re-sync (`UpdateSQNOnSyncFailure`) |
| `nf/ausf/sidf.go` | SUCI de-concealing — ECIES Profile A (X25519) / Profile B (secp256r1) decrypt |
| `nf/ausf/ausf_test.go` | Unit tests |

## 4. SBI / non-SBI surface

| Method (Go) | 3GPP operation | Spec |
|-------------|----------------|------|
| `GenerateAV` | Nausf_UEAuthentication_Authenticate (5G-AKA) | TS 33.501 §6.1.3.2 |
| `UpdateSQNOnSyncFailure` | re-synchronisation procedure | TS 33.102 §6.3.5, §C.3.2 |
| `DeconcealSUCI` / `DecryptSUCIFromNAS` | SIDF de-conceal | TS 33.501 §6.12.2 |
| `ExtractECIESParams` | split scheme output (pubkey ‖ ct ‖ MAC) | TS 33.501 §6.12 |
| `DecodeBCDMSIN` | BCD MSIN → digit string | TS 31.102 (BCD wire) |

## 5. Headline lifecycle — 5G-AKA Authenticate

`GenerateAV(imsi, snName, abba)` body in `ausf.go:51-117`:

1. UDM lookup → `*udr.UEAuthData` (K, OP/OPC, SQN, AMF). Returns
   `nil, nil` if subscriber unknown — AUSF surfaces an error.
2. RAND ← `crypto/rand.Read(16)`.
3. Milenage:
   - `m := sacrypto.NewMilenage(creds.OP)`; if `OpType=="OPC"`,
     `SetOPc`.
   - `MAC ← f1(K, RAND, SQN, AMF)`.
   - `RES, CK, IK, AK ← f2345(K, RAND)`.
4. `AUTN = (SQN ⊕ AK) ‖ AMF ‖ MAC` (`ausf.go:81-85`).
5. KDF chain (`libs/sacrypto`, TS 33.501 Annex A):
   - `KAUSF ← ConvA2(CK, IK, snName, SQN⊕AK)`
   - `XRES* ← ConvA4(CK, IK, snName, RAND, RES)`
   - `KSEAF ← ConvA6(KAUSF, snName)`
   - `KAMF  ← ConvA7(KSEAF, IMSI, abba)`
6. Persist SQN+1 via `udm.UpdateAuthSQN(imsi, udr.IncrementSQN(SQN))`
   (write-through; UDM batches via its SQN flusher).

Re-sync (`UpdateSQNOnSyncFailure`, `ausf.go:123-156`) — UE returned
`AUTS = (SQN_ms ⊕ AK*) ‖ MAC-S`:

1. `m.F5Star(K, RAND)` → AK*.
2. `SQN_ms = AUTS[0..5] ⊕ AK*[0..5]`.
3. Persist `next = max(IncrementSQN(SQN_ms), stored+1)`.

SIDF — `DecryptSUCIFromNAS(mcc, mnc, schemeID, hnpkid, schemeOutput, keyLookup)`
in `sidf.go:130-177`:

| protSchemeID | Path | Pubkey len | MAC len |
|--------------|------|------------|---------|
| 0 (Null) | `DecodeBCDMSIN(schemeOutput)` | — | — |
| 1 (Profile A, X25519) | ECIES decrypt | 32 B | 8 B |
| 2 (Profile B, secp256r1 compressed) | ECIES decrypt | 33 B | 8 B |

The HN private key is fetched through a caller-supplied
`HNKeyLookupFunc` (no built-in keystore) — `sidf.go:45`.

## 6. Key types / public API

```go
// 5G-AKA AV (TS 33.501 §6.1.3.2)
type AuthVector struct {
    RAND     []byte // 16 B
    AUTN     []byte // 16 B
    XRESStar []byte // 16 B (HN→SN proof)
    KAUSF    []byte // 32 B
    KSEAF    []byte // 32 B
    KAMF     []byte // 32 B
}
func GenerateAV(imsi, snName string, abba []byte) (*AuthVector, error)
func UpdateSQNOnSyncFailure(imsi string, auts, randBuf []byte) error

// SIDF (TS 33.501 §6.12)
type SIDFProfile byte
const (ProfileA SIDFProfile = 'A'; ProfileB SIDFProfile = 'B')
type HNKeyLookupFunc func(mcc, mnc string, hnpkid uint8) (SIDFProfile, string, error)
func DeconcealSUCI(hnPrivKeyHex string, profile SIDFProfile, uePubKey, ciphertext, macTag []byte) ([]byte, error)
func ExtractECIESParams(raw []byte, profile SIDFProfile) (uePubKey, ciphertext, macTag []byte, err error)
func DecodeBCDMSIN(msinBytes []byte) string
func DecryptSUCIFromNAS(mcc, mnc string, protSchemeID, hnpkid uint8, schemeOutput []byte, keyLookup HNKeyLookupFunc) (mccOut, mncOut, msin string, err error)
```

## 7. What's not implemented — TODOs / stubs

- **EAP-AKA'** (TS 33.501 §6.1.3.1): only 5G-AKA is implemented. There
  is no EAP method dispatch.
- **Nausf SBI HTTP/2**: TS 29.509 envelope is not modelled — `GenerateAV`
  is an in-process Go function. The 5G-AKA *Confirmation* (RES* ↔
  XRES* compare on the AMF side after the UE authenticates) is also
  done in-process by the AMF, not by the AUSF.
- **AKMA / SoR**: TS 33.535 (AKMA) and TS 33.501 §6.14 (SoR) are not
  modelled.
- **No subscriber → "Subscriber not found"** path returns
  `(nil, nil)` from `GenerateAV` (`ausf.go:58-60`) — caller decides
  whether to surface as ProblemDetails.

## 8. References (cited in source)

Verbatim from `nf/ausf/`:

- TS 29.509 (Nausf services)
- TS 33.102 §6.3.5, §C.3.2 (UMTS-AKA SQN management / re-sync)
- TS 33.501 §6.1.3.2 (5G-AKA)
- TS 33.501 §6.12, §6.12.2 (SUCI / SIDF / ECIES)

---
*Last refreshed against commit `13a181d`.*

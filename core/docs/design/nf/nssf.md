# NSSF — Network Slice Selection Function

3GPP TS 29.531 Nnssf_NSSelection. ~380 LOC at `nf/nssf/`. Picks the
serving Allowed NSSAI for a UE during Registration.

## 1. Role in 5GC

Called by the AMF when "the initial AMF cannot serve all the
S-NSSAI(s) from the Requested NSSAI permitted by the subscription
information" (TS 23.502 §4.2.2.2.2 step 4a). In this build the
NSSF is in-process and the conditional gate is elided — every
registration runs through `SelectAllowedNSSAI`.

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Nnssf** | AMF | Nnssf_NSSelection_Get | TS 29.531 §5.2.2.2.2 |
| (intra-NF) | UDM (subscribed NSSAI source) | `crud.SubscribedNSSAIList` | TS 23.501 §5.15.4 |

## 2. Architecture

```
AMF (initial)
   │  Requested NSSAI from UE NAS
   │  AMF PLMNSupportList
   │  gNB SupportedTAList
   │  TAC
   ▼
┌──── nf/nssf ────────────────────────────────┐
│ SelectAllowedNSSAI(imsi, requested,         │
│                   amfSlices, gnbSlices,     │
│                   taPolicyTAC)              │
│   ① loadSubscribedNSSAI (DB → all+defaults) │
│   ② candidates = requested OR defaults      │
│   ③ intersect: subscription ∩ amf ∩ gnb     │
│       per-TA filter (taPolicyAllows)        │
│       fail → Rejected entries with §9.11.3.46
│       cause                                 │
│   ④ all-rejected fallback to default        │
│      subscribed (TS 23.501 §5.15.5.2.1)     │
│   ⑤ cap Allowed/Rejected to 8 entries each  │
└─────────────────────────────────────────────┘
   │ SelectionResult { Allowed, Rejected, Subscribed }
   ▼
AMF Registration Accept
```

## 3. Package / file map

| File | Role |
|------|------|
| `nf/nssf/selection.go` | Entire NSSF: enums, types, selection logic, helpers |

## 4. SBI surface (current shape)

| Method (Go) | 3GPP operation | Spec |
|-------------|----------------|------|
| `SelectAllowedNSSAI(imsi, requested, amfSlices, gnbSlices, taPolicyTAC)` | Nnssf_NSSelection_Get | TS 29.531 §5.2.2.2.2 |

In-process; HTTP/2 + JSON envelope is not wired (TODO in
`selection.go:97-105` and `:123-128`).

## 5. Headline lifecycle — Allowed-NSSAI computation

Source: `selection.go:129-257`. Steps (all anchored on cited §):

1. **Load subscription** via `loadSubscribedNSSAI(imsi)` — splits
   into `all` and `defaults` (rows with `is_default=1`). If no
   defaults are flagged, every subscribed slice is treated as
   default (`selection.go:317-319`).
2. **Pick candidate set**:
   - `requested` if non-empty (UE asked for specific slices), else
   - `defaultSubscribed` (TS 23.501 §5.15.5.2.1 fallback for UEs
     without Configured/Allowed NSSAI).
   - Important: code does NOT fall through to `amfSlices` when both
     are empty — that would be a provisioning bug
     (`selection.go:152-156`).
3. **Intersect**: each candidate must be in subscription ∧ AMF
   support set ∧ gNB support set. SD wildcard rule per TS 24.501
   §9.11.2.8 + TS 23.003 §28.4.2 (`sdMatch`, `selection.go:291-296`):
   `0` and `0xFFFFFF` both wildcard. Failures emit a
   `RejectedSNSSAI` with TS 24.501 §9.11.3.46 cause:
   - `RejectedCauseNotInPLMN = 0`
   - `RejectedCauseNotInRegistrationArea = 1` (per-TA filter fail)
   - `RejectedCauseNSSAAFailedOrRevoked = 2` (TODO)
4. **All-rejected fallback** (TS 23.501 §5.15.5.2.1, verbatim quote
   in `selection.go:200-214`): if no Allowed survives but the UE
   actually requested something, fall back to default subscribed
   slices that pass AMF + gNB + TA filters.
5. **Cap**: Allowed ≤ 8 (TS 23.501 §5.15.4), Rejected ≤ 8
   (TS 24.501 §9.11.3.46 NOTE 0).

## 6. Key types / public API

```go
type SNSSAI struct {
    SST uint8
    SD  uint32   // 0 / 0xFFFFFF = wildcard
}

type RejectedSNSSAI struct {
    SNSSAI
    Cause uint8   // RejectedCauseXxx
}

type SelectionResult struct {
    Allowed    []SNSSAI         // ≤ 8 (TS 23.501 §5.15.4)
    Rejected   []RejectedSNSSAI // ≤ 8 (TS 24.501 §9.11.3.46 NOTE 0)
    Subscribed []SNSSAI
}

const (
    RejectedCauseNotInPLMN             uint8 = 0
    RejectedCauseNotInRegistrationArea uint8 = 1
    RejectedCauseNSSAAFailedOrRevoked  uint8 = 2
)

func SelectAllowedNSSAI(imsi string, requested []SNSSAI,
    amfSlices, gnbSlices []SNSSAI, taPolicyTAC string) SelectionResult
```

## 7. What's not implemented — TODOs / stubs

Captured in source as explicit TODOs:

- **Full Nnssf_NSSelection_Get response** (`selection.go:97-105`):
  no target AMF Set / candidate AMF list, target AMF Service Set,
  Target NSSAI, Mapping Of Allowed NSSAI (HPLMN↔VPLMN for roaming),
  Configured NSSAI, NSI ID(s), NRF(s), `nsagInfos`. Single-PLMN
  deployment doesn't exercise these.
- **Full Nnssf_NSSelection_Get query** (`selection.go:123-128`):
  no PLMN ID of SUPI, TAI, NF type of consumer, Requester ID,
  NSSRG Information, Mapping Of Requested NSSAI, NSAG support
  indication.
- **NSSAA** (TS 33.501 §6.1.4 + TS 23.501 §5.15.10) — Network Slice
  Specific Authentication and Authorization. Slices subject to
  NSSAA should land in a "pending" state and only enter Allowed
  after success; today they're admitted without checks
  (`selection.go:241-247`).
- **Per-TA NSSAI policy** (`selection.go:342-355`): `taPolicyAllows`
  always returns true — the TS 23.501 §5.15.3 ta-nssai policy
  isn't ported yet.

## 8. References (cited in source)

Verbatim from `nf/nssf/selection.go`:

- TS 23.003 §28.4.2 (SD wildcard wire encoding)
- TS 23.501 §5.15.3, §5.15.3.2, §5.15.4, §5.15.5.2, §5.15.5.2.1, §5.15.10
- TS 23.502 §4.2.2.2.2 step 4a (Initial AMF gate)
- TS 24.501 §5.5.1.2.4, §9.11.2.8, §9.11.3.46 (incl. NOTE 0)
- TS 29.531 §5.2.1, §5.2.2.2, §5.2.2.2.2
- TS 33.501 §6.1.4 (NSSAA — TODO)

---
*Last refreshed against commit `13a181d`.*

# NSACF — Network Slice Admission Control Function

3GPP TS 23.501 §5.15.11 / §5.15.12 NSACF. ~850 LOC at `nf/nsacf/`.
Per-slice UE admission quotas + UE-Slice MBR enforcement.

## 1. Role in 5GC

The NSACF gates UE registration into a network slice when the
slice has a quota (max UEs / max PDU sessions). It also tracks
per-(UE, slice) MBR for slice-level rate enforcement.

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Nnsacf** | AMF (UE quota) | Nnsacf_NSACEventExposure / NSACAdmissionControl | TS 23.501 §5.15.11 |
| **Nnsacf** | SMF (PDU-session quota) | (not modelled) | TS 23.501 §5.15.11 |
| (intra-NF) | DB (`db/engine`) | SQLite | — |

Today the AMF / SMF reach the NSACF via in-process Go calls;
there is no Nnsacf REST router.

## 2. Architecture

```
            AMF (UE registration)             SMF (PDU est.)
                  │                                │
                  ▼                                ▼ (not yet)
        ┌──────────────── nf/nsacf ────────────────┐
        │  Context (singleton)                     │
        │   admissions  (sst, sd) → set<IMSI>      │
        │   limits      (sst, sd) → sliceLimit     │
        │      max_ues, reserved_ues,              │
        │      priority_threshold,                 │
        │      preemption_enabled                  │
        │                                          │
        │  RequestAdmission / ReleaseAdmission     │
        │  EvaluateAdmission (priority+preempt)    │
        │  GetSliceStatus / GetAllStatus           │
        │  SetSliceLimit / IsSliceFull             │
        │                                          │
        │  UE Slice MBR (TS 23.501 §5.15.12)       │
        │   SetUESliceMBR / GetUESliceMBR /        │
        │   EnforceSliceMBR                        │
        └─────────┬──────────────────────────────┬─┘
                  │ persist                       │ read
                  ▼                                ▼
            slice_limits, admissions,        admission_log,
            ue_slice_mbr  (SQLite)           charging context
```

## 3. Package / file map

| File | Role |
|------|------|
| `nf/nsacf/nsacf.go` | All admission, MBR, DB, evaluation logic; singleton `Context` |

The package is one file. On first `GetNSACF()` call,
`loadFromDB()` (`nsacf.go:68-114`) reads `slice_limits` and
admission rows into memory.

## 4. Non-SBI surface (current shape)

| Method (Go) | 3GPP operation | Spec |
|-------------|----------------|------|
| `Context.RequestAdmission` / `ReleaseAdmission` | NSACAdmissionControl | TS 23.501 §5.15.11 |
| `EvaluateAdmission` (package-level) | priority + preemption admission policy | TS 23.501 §5.15.11 |
| `Context.GetSliceStatus` / `GetAllStatus` | slice-quota status read | — |
| `Context.SetSliceLimit` | provisioning | — |
| `Context.IsSliceFull` | quota check | — |
| `Context.PreemptUE` / `AdmitWithPriority` | priority preemption | TS 23.501 §5.15.11 |
| `Context.SetUESliceMBR` / `GetUESliceMBR` / `EnforceSliceMBR` | UE-Slice MBR | TS 23.501 §5.15.12 |
| `Admit` / `Release` / `AdmissionCount` | top-level convenience wrappers | — |
| DB CRUD: `GetAllSliceLimits`, `UpsertSliceLimit`, `UpdateSliceLimitByID`, `ListAdmissions`, `InsertAdmission`, `DeleteAdmission`, `GetLowestPriorityAdmission`, `LogAdmissionEvent`, `GetAdmissionLog`, `UpsertUESliceMBR`, `GetUESliceMBRRecord`, `ListUESliceMBR`, `UpdateUESliceMBRUsage` | persistence layer | — |

## 5. Headline lifecycle — UE admission to a slice

`RequestAdmission(imsi, sst, sd)` (`nsacf.go:121-155`):

1. Default empty `sd` → `"000000"`.
2. Already-admitted? return `{allowed:true, reason:"already_admitted"}`.
3. No `slice_limits` row? Auto-admit, persist asynchronously.
4. `len(admissions) < lim.MaxUEs` → admit + persist; reason
   `"capacity_available"`.
5. Otherwise reject; reason `"slice_full"`. (No preemption is
   attempted in this base path — preemption lives in
   `EvaluateAdmission` / `PreemptUE`.)

`EvaluateAdmission(imsi, sst, sd)` (`nsacf.go:374-...`):

- Reads UE priority via `getUEPriority`.
- Compares against the slice's `priority_threshold` and looks at
  reserved-slot count to decide admit / reject / preempt.
- On preempt: pick lowest-priority admission via
  `GetLowestPriorityAdmission`, evict via `PreemptUE`, then admit
  the new UE — see `Context.PreemptUE` (`nsacf.go:294-309`).

UE-Slice MBR (TS 23.501 §5.15.12) — `EnforceSliceMBR` (`nsacf.go:333-367`):

```
mbr := GetUESliceMBRRecord(imsi, sst, sd)
if mbr == nil  → throttle=false
UpdateUESliceMBRUsage(imsi, sst, sd, currentDLKbps, currentULKbps)
exceedDL = currentDL > mbr.dl
exceedUL = currentUL > mbr.ul
return { throttle, direction ∈ {dl, ul, both, nil}, mbr/current values }
```

## 6. Key types / public API

```go
// internal
type sliceKey struct { SST int; SD string }
type sliceLimit struct {
    MaxUEs, ReservedUEs, PriorityThreshold int
    PreemptionEnabled bool
}

// public
type Context struct{/*...*/}
func GetNSACF() *Context

// Admission
func (*Context) RequestAdmission(imsi string, sst int, sd string) map[string]any
func (*Context) ReleaseAdmission(imsi string, sst int, sd string) bool
func (*Context) GetSliceStatus(sst int, sd string) map[string]any
func (*Context) GetAllStatus() []map[string]any
func (*Context) SetSliceLimit(sst int, sd string, maxUEs, reservedUEs, priorityThreshold int, preemptionEnabled bool)
func (*Context) IsSliceFull(sst int, sd string) bool
func (*Context) AdmitWithPriority(imsi string, sst int, sd string, priority int)
func (*Context) PreemptUE(imsi string, sst int, sd, reason string)
func EvaluateAdmission(imsi string, sst int, sd string) map[string]any

// UE Slice MBR
func (*Context) SetUESliceMBR(imsi string, sst int, sd string, mbrDLKbps, mbrULKbps int)
func (*Context) GetUESliceMBR(imsi string, sst int, sd string) map[string]any
func (*Context) EnforceSliceMBR(imsi string, sst int, sd string, currentDLKbps, currentULKbps int) map[string]any

// Convenience
func Admit(imsi string, sst int, sd string) error
func Release(imsi string, sst int, sd string)
func AdmissionCount(sst int, sd string) (int64, error)

// DB CRUD (omitted for brevity — see nsacf.go:504-770)
```

## 7. What's not implemented — TODOs / stubs

The package code does not carry TODO markers, but reading the
surface shows:

- **Nnsacf SBI**: TS 29.536 / Nnsacf REST envelope is not modelled.
  Calls are intra-process Go.
- **PDU-session quota** (TS 23.501 §5.15.11 also limits the number
  of concurrent PDU Sessions per slice): only UE quota is tracked
  here. There is no `pdu_session_admissions` accounting.
- **NSACEventExposure** (subscribe/notify on quota status): no event
  producer.
- **MBR enforcement actuator**: `EnforceSliceMBR` returns a
  `throttle` decision but nothing wires that to the UPF / SMF — the
  SMF would need to apply rate caps on the relevant PFCP QER.

## 8. References (cited in source)

Verbatim from `nf/nsacf/nsacf.go`:

- TS 23.501 §5.15.11 (admission control — `nsacf.go:4`, `:117`)
- TS 23.501 §5.15.12 (UE Slice MBR — `nsacf.go:4`, `:312`)

---
*Last refreshed against commit `13a181d`.*

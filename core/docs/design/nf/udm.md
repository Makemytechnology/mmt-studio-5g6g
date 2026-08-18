# UDM — Unified Data Management (+ ARPF façade)

3GPP TS 29.503 UDM. ~1.1k LOC at `nf/udm/`. The 3GPP-shaped façade
over UDR: AUSF / AMF / SMF call the §-numbered Get operations here
and the UDM forwards to UDR + caches the hot-path tables in memory.

## 1. Role in 5GC

The UDM hosts three SBI services (Nudm_SDM, Nudm_UECM, Nudm_UEAU)
and the ARPF role for 5G-AKA credential lookup. AUSF/AMF/SMF are
forbidden by TS 33.501 §6.1.2 from talking to UDR directly — every
read/write goes through UDM.

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Nudm** | AUSF | Nudm_UEAuthentication (UEAU) | TS 29.503 §5.4 |
| **Nudm** | AMF | Nudm_UEContextManagement (UECM) | TS 29.503 §5.3 |
| **Nudm** | AMF / SMF / NSSF | Nudm_SubscriberDataManagement (SDM) | TS 29.503 §5.2 |
| **Nudr** | UDR | `nf/udr` Go calls | TS 29.504 |

Nudm REST surface is not yet wired — calls are intra-process Go.

## 2. Architecture

```
   AUSF             AMF              SMF, NSSF
    │ Nudm_UEAU      │ Nudm_UECM       │ Nudm_SDM
    ▼                ▼                  ▼
   ┌────────────────── nf/udm ────────────────────────┐
   │  auth.go    GetAuthData / UpdateAuthSQN          │
   │             GetAllSubscribers                    │
   │  sdm.go     GetSubscribedNSSAI / GetDefaultSNSSAI│
   │             GetDefaultDNN / GetSubscriptionData  │
   │  uecm.go    RegisterAMF / DeregisterAMF /        │
   │             GetServingAMF                        │
   │  uecm/fsm/  Deregistered ↔ Registered FSM        │
   │  uecm_transitions.go                             │
   │                                                  │
   │  cache.go              authByIMSI (+ sqnDirty)   │
   │  subscription_cache.go subCache (UE-AMBR)        │
   │  sqn_flusher.go        SQN write-behind goroutine│
   └─────────────────┬────────────────────────────────┘
                     │  cache miss / DB write
                     ▼
                  nf/udr
```

Hot-path design: `GetAuthData`, `GetSubscriptionAMBR` (and all SDM
reads via UDR) are 0-DB on cache hits. SQN updates mutate memory
+ mark dirty; a 2 s ticker flushes batched writes
(`sqn_flusher.go:35-50`). TS 33.102 §C.3.4's tolerated drift
covers the post-crash replay window.

## 3. Package / file map

| File | LOC | Role |
|------|----:|------|
| `nf/udm/auth.go` | 87 | Nudm_UEAU §5.4: `GetAuthData`, `UpdateAuthSQN` |
| `nf/udm/sdm.go` | 135 | Nudm_SDM §5.2: NSSAI / default DNN / full subscription |
| `nf/udm/uecm.go` | 143 | Nudm_UECM §5.3: AMF registration registry + FSM driver |
| `nf/udm/uecm_transitions.go` | 32 | UECM FSM transition table |
| `nf/udm/uecm/fsm/state.go` | 54 | `StateDeregistered` / `StateRegistered` |
| `nf/udm/uecm/fsm/event.go` | 40 | `EvRegisterRequest` / `EvRegisterReject` / `EvDeregisterRequest` / `EvDeregistrationNotificationSent` |
| `nf/udm/uecm/fsm/fsm.go` | 148 | Per-IMSI FSM dispatcher |
| `nf/udm/cache.go` | 126 | Auth cache (`authByIMSI`), `LoadCache` / `ReloadAuth` / `DropAuth` / `bumpSQN` |
| `nf/udm/sqn_flusher.go` | 95 | Background SQN flusher (default 2 s tick) |
| `nf/udm/subscription_cache.go` | 101 | UE-AMBR cache |
| `nf/udm/uecm_test.go` | 136 | UECM unit tests |

## 4. SBI surface (current shape)

| Method (Go) | Spec | Consumer |
|-------------|------|----------|
| `GetAuthData(imsi)` | Nudm_UEAU_Get §5.4.2.2 | AUSF |
| `UpdateAuthSQN(imsi, newSQN)` | (TS 33.102 §C.3.2 carry-over) | AUSF |
| `GetSubscribedNSSAI(imsi)` | Nudm_SDM_Get §5.2.2.2.3 (Access & Mobility) | AMF |
| `GetDefaultSNSSAI(imsi)` | Nudm_SDM_Get §5.2.2.2.3 | AMF |
| `GetDefaultDNN(imsi, sst, sdHex)` | Nudm_SDM_Get §5.2.2.2.5 (Session Management) | SMF |
| `GetSubscriptionData(imsi)` | Nudm_SDM_Get §5.2.2.2.3 | AMF |
| `RegisterAMF(imsi, amfUeNgapID, amfName)` | Nudm_UECM §5.3.2.2 Registration | AMF |
| `DeregisterAMF(imsi)` | Nudm_UECM §5.3.2.4 Deregistration | AMF |
| `GetServingAMF(imsi)` | Nudm_UECM §5.3.2.5 Get | Namf_Communication routing |

## 5. Headline lifecycles

### 5.1 Nudm_UEAU_Get — credential lookup

`auth.go:53-69`:

1. `pm.Inc(UDMUeAuthGet, 1)`.
2. `lookup(imsi)` from cache (`cache.go:92-96`). Miss → log warn,
   return `nil, nil`.
3. Defensive copy of K / OP / AMF slices so AUSF can't mutate.

### 5.2 Nudm_UECM_Registration — AMF claim

`uecm.go:64-102`:

1. Snapshot existing `regs[imsi]` under write lock.
2. Overwrite with `&AMFRegistration{...}`.
3. If a previous AMF existed: log §5.3.2.3
   DeregistrationNotification (no-op in single-AMF deployment),
   bump `UDMUecmDeregNotify`, fire `EvDeregisterRequest` so the
   FSM log shows churn.
4. Fire `EvRegisterRequest` → state `Registered`.
5. Bump `UDMUecmRegister`.

UECM transition table (`uecm_transitions.go:13-31`):

| From | Event | To | Anchor |
|------|-------|----|---------|
| Deregistered | EvRegisterRequest | Registered | §5.3.2.2 |
| Registered | EvRegisterRequest | Registered | re-registration / multi-AMF |
| Deregistered | EvRegisterReject | Deregistered | — |
| Registered | EvDeregisterRequest | Deregistered | §5.3.2.4 |
| Registered | EvDeregistrationNotificationSent | Registered | §5.3.2.3 (self-loop, single-AMF) |

### 5.3 Nudm_SDM_Get — subscription data

`sdm.go`. Each method is a thin pass-through to `nf/udr` plus PM
counter increments. `GetSubscribedNSSAI` + `GetDefaultSNSSAI`
serve the AMF (TS 29.503 §5.2.2.2.3); `GetDefaultDNN` serves the
SMF (§5.2.2.2.5). UE-AMBR comes through `GetSubscriptionData`.

### 5.4 SQN flusher

`sqn_flusher.go:35-...` — `StartSQNFlusher(interval)` spawns one
goroutine. Each tick:

```
takeDirtySnapshot() → map[imsi]int64    (under cacheMu, clears dirty set)
for imsi, sqn := snapshot {
    udr.UpdateUeAuthData(imsi, UEAuthData{SQN: sqn})
}
```

`StopSQNFlusher` drains one final snapshot — graceful shutdown
loses at most a 2 s batch, within TS 33.102 §C.3.4 SQN drift
tolerance (`sqn_flusher.go:10-14`).

## 6. Key types / public API

```go
// auth.go
func GetAuthData(imsi string) (*udr.UEAuthData, error)
func UpdateAuthSQN(imsi string, newSQN int64) error
func GetAllSubscribers() ([]struct{ IMSI string; udr.UEAuthData }, error)

// sdm.go
type SubscribedNSSAI struct { SST int; SD *int; IsDefault bool }
type SubscriptionData struct {
    SubscribedNSSAI []SubscribedNSSAI
    AMBRDLKbps, AMBRULKbps int64
}
func GetSubscribedNSSAI(imsi string) ([]SubscribedNSSAI, error)
func GetDefaultSNSSAI(imsi string) (sst int, sdHex string, ok bool)
func GetDefaultDNN(imsi string, sst int, sdHex string) (string, bool)
func GetSubscriptionData(imsi string) (*SubscriptionData, error)

// uecm.go
type AMFRegistration struct { IMSI, AmfName string; AmfUeNgapID int64; RegisteredAt time.Time }
func RegisterAMF(imsi string, amfUeNgapID int64, amfName string) (*AMFRegistration, error)
func DeregisterAMF(imsi string)
func GetServingAMF(imsi string) *AMFRegistration

// cache.go
func LoadCache() error
func ReloadAuth(imsi string) (bool, error)
func DropAuth(imsi string)

// subscription_cache.go
type AMBR struct { UplinkKbps, DownlinkKbps int64 }
func LoadSubscriptionCache() error
func ReloadSubscription(imsi string) error
func DropSubscription(imsi string)
func GetSubscriptionAMBR(imsi string) (AMBR, bool)

// sqn_flusher.go
func StartSQNFlusher(interval time.Duration)
func StopSQNFlusher()
```

## 7. What's not implemented — TODOs / stubs

Captured by reading the source:

- **§5.5 Nudm_EventExposure** — explicit "not yet implemented"
  marker at `auth.go:19`.
- **Nudm SBI HTTP/2** — every method is in-process Go. The §-shaped
  surfaces are kept so the SBI split is mechanical; no router is
  wired today.
- **Multi-AMF DeregistrationNotification** — `RegisterAMF` only
  *logs* the §5.3.2.3 push (`uecm.go:84-94`); both AMFs are the
  same binary in this build. The FSM has the
  `EvDeregistrationNotificationSent` event for the future.
- **UDR persistence of UECM registry** — explicit "future work for
  multi-AMF deployments" note at `uecm.go:21-24`. In-memory
  `regs` map is the single source of truth.
- **§5.4 challenge confirmation** (RES* / XRES* compare) — done by
  the AMF in-process; not a Nudm op here.
- **§5.4 EAP-AKA' / Auth Method selection** — only 5G-AKA path
  exists upstream of UDM (`nf/ausf`); UDM just returns credentials.

## 8. References (cited in source)

Verbatim from `nf/udm/`:

- TS 23.501 §5.6.1 (defaultDnnIndicator), §5.7.3 (UE-AMBR), §5.15.4 (subscribed S-NSSAIs)
- TS 23.502 §4.2.2.2.2 step 14, §4.3.2.2.1
- TS 24.501 §5.5.2
- TS 29.503 §5.2.{2.2.2,2.2.3,2.2.5}, §5.3.{2.2,2.3,2.4,2.5}, §5.4.2.2, §5.5, §6.2.6, §6.2.6.2
- TS 29.504 (UDR — `auth.go:3` reference)
- TS 33.102 §C.3.2, §C.3.4 (SQN management / drift tolerance)
- TS 33.501 §6.1.2 (NF-to-UDR ban)

---
*Last refreshed against commit `13a181d`.*

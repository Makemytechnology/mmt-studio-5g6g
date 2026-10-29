# UDR — Unified Data Repository

3GPP TS 29.504 Nudr_DataRepository. ~345 LOC at `nf/udr/`. The raw
subscriber data store; everything that touches `ue_auth_data` /
`ue` / `ue_subscribed_nssai` / `ue_slice_dnn` lives here.

## 1. Role in 5GC

Per TS 33.501 §6.1.2 (cited in `nf/udr/auth.go:6-8`), only the UDM
talks to the UDR. NFs reach UDR exclusively through UDM SBI calls;
this package is the storage adapter UDM forwards to.

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Nudr** | UDM | Nudr_DataRepository | TS 29.504 |
| (intra-NF) | `db/crud` | SQLite | — |

The Nudr REST surface is not modelled — UDM uses the Go API directly.

## 2. Architecture

```
   UDM (auth.go / sdm.go / cache.go)
         │
         ▼
   ┌─── nf/udr ──────────────┐
   │ auth.go         UEAuthData rows (K/OP/OPC/AMF/SQN)
   │ subscription.go SubscribedNSSAI / DefaultDNN / AMBR
   │ fiveqi.go       Standardized 5QI table (read-only)
   └─────────┬───────────────┘
             │ db/crud
             ▼
        SQLite (via db/engine)
        ue, ue_auth_data, ue_subscribed_nssai, ue_slice_dnn,
        ue.ambr_ul_kbps / ambr_dl_kbps
```

## 3. Package / file map

| File | LOC | Role |
|------|----:|------|
| `nf/udr/auth.go` | 87 | Authentication data (`ue_auth_data` table). Hex ↔ bytes conversion, SQN math |
| `nf/udr/subscription.go` | 129 | Subscribed NSSAI, default DNN per slice, full subscription bundle |
| `nf/udr/fiveqi.go` | 53 | Standardized 5QI table (TS 23.501 Table 5.7.4-1), in-memory only |

## 4. Non-SBI surface

| Method (Go) | 3GPP role | Spec |
|-------------|-----------|------|
| `GetUeAuthData(imsi)` | Nudr_DataRepository_Query (Authentication subset) | TS 29.504 |
| `GetAllUeAuthData()` | bulk read | — |
| `UpdateUeAuthData(imsi, patch)` | Nudr_DataRepository_Modify | TS 29.504 |
| `DeleteUeAuthData(imsi)` | Nudr_DataRepository_Delete | TS 29.504 |
| `IncrementSQN(sqn)` | TS 33.102 §C.3.2 SQN bump (SEQ only, IND=0) | TS 33.102 |
| `GetSubscribedNSSAI(imsi)` | subscription read | TS 29.504 |
| `GetDefaultDNN(imsi, sst, sdHex)` | per-slice default DNN read (TS 23.501 §5.6.1 defaultDnnIndicator) | — |
| `GetSubscriptionData(imsi)` | NSSAI + AMBR bundle | — |
| `GetFiveQIInfo(fiveQI)` / `FiveQITable()` | standardized 5QI lookup | TS 23.501 Table 5.7.4-1 |

## 5. Headline lifecycles

### 5.1 Authentication credential read

`GetUeAuthData(imsi)` — `auth.go:38-47`:

```
crud.AuthGetByIMSI(imsi)  → *crud.AuthData (hex strings)
fromCRUD                  → *UEAuthData (raw bytes)
   K   ← hex.DecodeString(KHex)            // 16 B
   OP  ← hex.DecodeString(OPHex)           // 16 B (interpret as OP or OPc per OpType)
   AMF ← hex.DecodeString(AMFHex) || 0x80 0x00   // 2 B default
   SQN ← int64                              // 48-bit
```

`UpdateUeAuthData(imsi, patch)` (`auth.go:85-120`) does field-wise
preserve-or-patch: only non-empty/non-zero `patch` fields overwrite;
defaults to existing values otherwise. AMF defaults to `8000`
(Milenage test-vector default).

### 5.2 SQN math

`IncrementSQN` (`auth.go:124-129`) implements TS 33.102 §C.3.2 with
`SEQ‖IND` packing:

```go
const indBits = 5
const mask48  = (1<<48) - 1
step := 1 << indBits     // 32
return (sqn + step) & mask48
```

IND is always 0 — operator-private use of the IND bits is not
modelled.

### 5.3 Subscribed NSSAI

`GetSubscribedNSSAI(imsi)` (`subscription.go:23-62`):

1. Primary source: `crud.SubscribedNSSAIList(imsi)` →
   `ue_subscribed_nssai` rows.
2. Fallback (if primary empty): derive from `crud.SliceDNNList`
   (the `ue_slice_dnn` table). De-duplicates by (sst, sd).

`GetDefaultDNN` (`subscription.go:67-93`): walk `ue_slice_dnn` for
the (imsi, sst, sd) tuple, prefer an `is_default=1` row, else
return the first matching DNN as fallback. Implements
TS 23.501 §5.6.1 `defaultDnnIndicator`.

`GetSubscriptionData` (`subscription.go:104-128`) bundles the
subscription read with UE-AMBR (`crud.SubscriptionGetByIMSI`).
AMBR defaults to `1_000_000` kbps each direction when the row is
missing or zero.

### 5.4 Standardized 5QI table

`fiveqi.go` ports TS 23.501 Table 5.7.4-1 entries:

| 5QI | Resource | Pri | PDB (ms) | PER | Example |
|-----|----------|----:|---------:|------|---------|
| 1 | GBR | 2 | 100 | 1e-2 | Conversational Voice |
| 2 | GBR | 4 | 150 | 1e-3 | Conversational Video (Live) |
| 3 | GBR | 3 | 50 | 1e-3 | Real-Time Gaming |
| 4 | GBR | 5 | 300 | 1e-6 | Buffered Streaming Video |
| 5 | GBR | 1 | 100 | 1e-6 | IMS Signalling |
| 6 | Non-GBR | 6 | 300 | 1e-6 | MCPTT Signalling |
| 7 | Non-GBR | 7 | 100 | 1e-3 | Voice / Video / Interactive Gaming |
| 8 | Non-GBR | 8 | 300 | 1e-6 | Buffered Video Streaming |
| 9 | Non-GBR | 9 | 300 | 1e-6 | General Data |
| 65 | GBR | 0 | 1 | 1e-5 | URLLC |
| 66 | GBR | 2 | 100 | 1e-2 | V2X Messaging |
| 67 | GBR | 3 | 10 | 1e-4 | V2X Sensor Sharing |
| 75 | Non-GBR | 5 | 10 | 1e-6 | Mission-Critical Video |
| 79 | Non-GBR | 6 | 50 | 1e-6 | Mission-Critical Data |

## 6. Key types / public API

```go
// auth.go
type UEAuthData struct {
    K     []byte // 16 B
    SQN   int64  // 48-bit
    OpType string // "OP" | "OPC"
    OP    []byte // 16 B
    AMF   []byte // 2 B (defaults 0x8000)
}
func GetUeAuthData(imsi string) (*UEAuthData, error)
func GetAllUeAuthData() ([]struct { IMSI string; UEAuthData }, error)
func UpdateUeAuthData(imsi string, patch UEAuthData) error
func DeleteUeAuthData(imsi string) error
func IncrementSQN(sqn int64) int64

// subscription.go
type SubscribedNSSAIEntry struct { SST int; SD *int; IsDefault bool }
type SubscriptionData struct {
    SubscribedNSSAI []SubscribedNSSAIEntry
    AMBRDLKbps, AMBRULKbps int64
}
func GetSubscribedNSSAI(imsi string) ([]SubscribedNSSAIEntry, error)
func GetDefaultDNN(imsi string, sst int, sdHex string) (string, bool)
func GetSubscriptionData(imsi string) (*SubscriptionData, error)

// fiveqi.go
type FiveQIInfo struct {
    FiveQI int
    ResourceType string  // "GBR" | "Non-GBR"
    DefaultPriority, PacketDelayBudMs int
    PacketErrorRate, ExampleServices  string
}
func GetFiveQIInfo(fiveQI int) *FiveQIInfo
func FiveQITable() map[int]FiveQIInfo
```

## 7. What's not implemented — TODOs / stubs

The package code does not carry explicit TODO markers, but the
surface shows:

- **Nudr SBI HTTP/2 router** — calls are intra-process Go. UDM
  invokes the package directly.
- **Generic Nudr_DataRepository_{Subscribe,Notify}** — no
  subscribe/notify producers (data-change notifications to UDM /
  policy data subscribers).
- **Application Data / Exposure Data / Policy Data realms** —
  TS 29.519 organizes UDR data into multiple realms (Subscription
  Data, Policy Data, Exposure Data, Application Data, Operator
  Specific Data). Only the Subscription Data realm is partially
  implemented (auth, NSSAI, slice-DNN, AMBR). Policy / Exposure /
  Application realms are not modelled here.
- **No-DB AMBR fallback** — `GetSubscriptionData` returns
  `1_000_000` kbps when the row is absent
  (`subscription.go:109-117`); not a §-mandated default.
- **5QI table is hard-coded** — operator-specific 5QIs in the
  64–127 / 128–254 ranges (TS 23.501 §5.7.4) are not in the table.

## 8. References (cited in source)

Verbatim from `nf/udr/`:

- TS 23.501 §5.6.1 (`subscription.go:65`)
- TS 23.501 Table 5.7.4-1 (`fiveqi.go:3, :19`)
- TS 29.504 / §5.2.3 (`auth.go:3`, `subscription.go:3`)
- TS 33.102 §C.3.2 (`auth.go:122`)
- TS 33.501 §6.1.2 (`auth.go:6-8`)

---
*Last refreshed against commit `13a181d`.*

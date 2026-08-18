# RAN Sharing — MORAN / MOCN agreements + admission gate

NG-RAN Sharing surface for the SA Core: the operator's sharing-
agreement registry (which PLMNs share which gNB, with what capacity
split), a per-gNB allocation map for MORAN, an admission gate
(`CheckAccess`) the AMF / SMF call during Initial Registration, and
a usage-log ledger for per-(PLMN, gNB) accounting.

# Part A — Functional

## A.1 Why RAN sharing?

NG-RAN Sharing lets two or more operators share radio infrastructure
under a contractual agreement. TS 22.261 §6.21 defines two variants:

| Variant | Description | Per-PLMN spectrum? | Admission |
|---------|-------------|--------------------|-----------|
| **MORAN** | Multi-Operator RAN — gNB hardware is shared, each PLMN owns its own carrier / spectrum slice. | Yes (per-gNB capacity split) | Operator must pre-allocate a capacity percentage on each shared gNB. |
| **MOCN** | Multi-Operator Core Network — a single carrier serves multiple PLMNs; the gNB broadcasts every served PLMN-ID. | No (admission-controlled) | Any participating PLMN admits without per-gNB pre-allocation. |

The package exposes both shapes: an Agreement carries
`sharing_type ∈ {MORAN, MOCN}` and a participating-PLMN list, and
MORAN agreements additionally carry a `ran_sharing_gnb_map` row per
shared gNB with an `allocated_capacity_pct ∈ [0, 100]`.

`CheckAccess(plmn, gnb_id)` walks **active** agreements, matches the
participating-PLMN list, and:

- **MORAN**: requires an explicit gNB-map row → returns the per-gNB
  capacity_pct.
- **MOCN**: admits on any gNB the agreement applies to (the gNB
  broadcasts the multi-PLMN-ID list directly — TS 38.413 §9.2.6.x —
  so per-gNB caps aren't needed at the 5GC).

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **NG-RAN PLMN broadcast** | gNB → UE | RRC SIB1 | TS 38.413 §9.2.6.x | RAN-side; this surface only stores the operator-side admission contract. |
| **Initial Registration** | UE → AMF | NAS over NGAP | TS 23.501 §5.17.4 | Admission gate (`CheckAccess`) is consulted before AMF runs UDM lookup. |
| **Operator panel** | OAM → 5GC | REST | OAM-internal | `/api/ran-sharing/*` (this file). |

## A.3 Operator-visible behaviours

### A.3.1 Agreement registry

A `ran_sharing_agreement` row carries:

| Field | Vocabulary | Notes |
|-------|------------|-------|
| `name`          | free text | display label |
| `sharing_type`  | `MORAN` \| `MOCN` | CHECK enforced in DDL |
| `participating_plmns` | comma- or whitespace-separated MCC/MNC ids | exact-match (no substring matching) |
| `capacity_split_json` | JSON blob | per-PLMN spectrum split for MORAN (operator-policy) |
| `priority_rules_json` | JSON blob | per-PLMN QoS / scheduler hints (TS 22.261 §6.21 TODO) |
| `status` | `pending` \| `active` \| `inactive` | default `pending` — operator must `Activate` to admit traffic |

`CreateAgreement` is a plain INSERT (no UPSERT) since the schema
allows multiple agreements per partner-PLMN combination — the
operator can run separate agreements for inbound and outbound flows.

### A.3.2 Per-gNB allocation map (MORAN only)

`ran_sharing_gnb_map` rows hold `(agreement_id, gnb_id) →
allocated_capacity_pct`. The `(agreement_id, gnb_id)` UNIQUE
constraint plus `ON CONFLICT DO UPDATE` makes UpsertGnBMap idempotent;
calling it twice replaces the cap.

### A.3.3 Admission gate

`CheckAccess(plmn, gnb_id)` returns:

```json
{ "allowed": true, "reason": "matched per-gNB allocation",
  "agreement_id": 7, "agreement_name": "OpA-OpB Share",
  "sharing_type": "MORAN", "capacity_pct": 70 }
```

or

```json
{ "allowed": false, "reason": "no matching active agreement" }
```

### A.3.4 Usage log

`InsertUsageLog(agreement_id, plmn, gnb_id, ue_count, throughput_mbps)`
records one bin (typically per minute or per hour, set by the
collector) for billing / audit. Reads via `ListUsageLog` are limited
to the most recent N rows by default (100); operator panels can scope
by `agreement_id`.

## A.4 Operator REST API (`/api/ran-sharing/*`)

All responses use `{ok: bool, ...}` envelopes to match the existing
`ran_sharing.html` GUI panel.

### A.4.1 Stats

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/ran-sharing/stats` | `{ok, stats: {total_agreements, active_agreements, mapped_gnbs, usage_entries}}` |
| GET | `/api/ran-sharing/status` | alias of `/stats` |

### A.4.2 Agreements (TS 22.261 §6.21)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ran-sharing/agreements?status=active` | list (filter by status) |
| POST   | `/api/ran-sharing/agreements` | create (status defaults to `pending`) |
| GET    | `/api/ran-sharing/agreements/{id}` | one agreement |
| PATCH  | `/api/ran-sharing/agreements/{id}` | sparse update — allow-listed columns only |
| DELETE | `/api/ran-sharing/agreements/{id}` | remove (cascades to gnb-map) |
| POST   | `/api/ran-sharing/agreements/{id}/activate` | flip status → `active` |
| POST   | `/api/ran-sharing/agreements/{id}/deactivate` | flip status → `inactive` |

### A.4.3 Per-gNB allocation (MORAN)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ran-sharing/gnb-map?agreement_id=N` | list (filter by agreement) |
| POST   | `/api/ran-sharing/gnb-map` | UPSERT `{agreement_id, gnb_id, allocated_capacity_pct}` |
| DELETE | `/api/ran-sharing/gnb-map/{agreement_id}/{gnb_id}` | remove |

### A.4.4 Admission gate

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/ran-sharing/check-access` | `{plmn, gnb_id}` → admission decision |

### A.4.5 Usage log

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/ran-sharing/usage-log?agreement_id=N&limit=K` | list bins |
| POST   | `/api/ran-sharing/usage-log` | append `{agreement_id, plmn, gnb_id, ue_count, throughput_mbps}` |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| TS 23.251 Stage-2 detail | TS 23.251 | Not loaded locally; CheckAccess enforces the operator-side contract only. |
| MOCN per-PLMN QoS class differentiation | TS 22.261 §6.21 | `priority_rules_json` is stored but not yet consumed by the SMF / PCF. |
| gNB broadcast of multi-PLMN-ID list | TS 38.413 §9.2.6.x | RAN-side; this surface only stores the operator-side admission contract. |

---

# Part B — Design

## B.1 Process layout

```
┌──────────────── security/ran_sharing ────────────────┐
│                                                      │
│  Agreements                  Per-gNB allocations     │
│   ran_sharing_agreements      ran_sharing_gnb_map    │
│   ├── CreateAgreement         ├── UpsertGnBMap       │
│   ├── GetAgreement            ├── ListGnBMap         │
│   ├── ListAgreements          └── DeleteGnBMap       │
│   ├── UpdateAgreement                                │
│   ├── ActivateAgreement                              │
│   ├── DeactivateAgreement                            │
│   └── DeleteAgreement                                │
│                                                      │
│  Admission gate              Usage log               │
│   CheckAccess(plmn, gnb_id)   ran_sharing_usage_log  │
│      ▲                        ├── InsertUsageLog     │
│      │                        └── ListUsageLog       │
│      │                                               │
│   Walks active agreements, matches participating     │
│   PLMNs, then per-gNB allocation (MORAN) or          │
│   open-admission (MOCN).                             │
│                                                      │
└──────────────────────────────────────────────────────┘
                         ▲
                         │ JSON / HTTP via OAM panel
                         │
            webservice/app/routes_ran_sharing.go
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `security/ran_sharing/ran_sharing.go` | ~445 | Agreement CRUD + GnBMap + CheckAccess + UsageLog + Stats. |
| `security/ran_sharing/ran_sharing_test.go` | ~280 | PLMN-list parsing, MORAN/MOCN admission, gnb-map UPSERT idempotency. |
| `db/schemas/domains.go` | (slice) | DDL for `ran_sharing_agreements`, `ran_sharing_gnb_map`, `ran_sharing_usage_log` (already present). |
| `webservice/app/routes_ran_sharing.go` | ~250 | REST surface for §A.4. |

Tests:
- `mmt_studio_core_tester/src/testcases/security/tc_ran_sharing.py` — 5 live integration TCs (relocated from `access/` and the duplicate `infra/` was removed).

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS ran_sharing_agreements (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  name                TEXT NOT NULL,
  sharing_type        TEXT NOT NULL CHECK (sharing_type IN ('MORAN','MOCN')),
  participating_plmns TEXT NOT NULL,
  capacity_split_json TEXT,
  priority_rules_json TEXT,
  status              TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('active','inactive','pending')),
  created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS ran_sharing_gnb_map (
  id                     INTEGER PRIMARY KEY AUTOINCREMENT,
  agreement_id           INTEGER NOT NULL,
  gnb_id                 TEXT NOT NULL,
  allocated_capacity_pct INTEGER NOT NULL DEFAULT 0,
  UNIQUE (agreement_id, gnb_id),
  FOREIGN KEY (agreement_id) REFERENCES ran_sharing_agreements(id)
    ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS ran_sharing_usage_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  agreement_id    INTEGER,
  plmn            TEXT NOT NULL,
  gnb_id          TEXT NOT NULL,
  ue_count        INTEGER NOT NULL DEFAULT 0,
  throughput_mbps REAL NOT NULL DEFAULT 0.0,
  timestamp       TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY (agreement_id) REFERENCES ran_sharing_agreements(id)
    ON DELETE SET NULL
);
```

The `ON DELETE CASCADE` on `gnb_map` keeps the per-gNB rows in lock-
step with the agreement; `ON DELETE SET NULL` on the usage log
preserves the historical log even after the agreement is removed
(audit trail wins over storage tidiness).

## B.4 CheckAccess algorithm

```
CheckAccess(plmn, gnb_id):
   for agreement in ListAgreements(active):
      if plmn not in agreement.participating_plmns: continue
      maps = ListGnBMap(agreement.id)
      for m in maps:
         if m.gnb_id == gnb_id:
            return Allowed{
              agreement_id, agreement_name, sharing_type,
              capacity_pct = m.allocated_capacity_pct,
              reason = "matched per-gNB allocation"
            }
      if agreement.sharing_type == MOCN:
         return Allowed{
           agreement_id, agreement_name, sharing_type=MOCN,
           reason = "MOCN agreement (no per-gNB cap)"
         }
   return Denied{ reason = "no matching active agreement" }
```

PLMN list parsing normalises `,;` and whitespace separators to
commas before splitting, then exact-matches each token. This avoids
the false-positive substring problem (`"310"` matching `"310-260"`
*and* `"23410"`).

## B.5 Public API

```go
const (
    SharingMORAN = "MORAN"
    SharingMOCN  = "MOCN"
    StatusActive   = "active"
    StatusInactive = "inactive"
)

// Agreements
func CreateAgreement(name, sharingType, plmns string,
    capacitySplit, priorityRules map[string]interface{}) (map[string]interface{}, error)
func GetAgreement(id int64) (map[string]interface{}, error)
func ListAgreements(status string) ([]map[string]interface{}, error)
func UpdateAgreement(id int64, fields map[string]interface{}) (map[string]interface{}, error)
func ActivateAgreement(id int64) (map[string]interface{}, error)
func DeactivateAgreement(id int64) (map[string]interface{}, error)
func DeleteAgreement(id int64) bool

// gNB map
func UpsertGnBMap(agreementID int64, gnbID string, capacityPct int) (map[string]interface{}, error)
func ListGnBMap(agreementID int64) ([]map[string]interface{}, error)
func DeleteGnBMap(agreementID int64, gnbID string) error

// Admission gate
type AccessResult struct {
    Allowed       bool
    Reason        string
    AgreementID   int64
    AgreementName string
    SharingType   string
    CapacityPct   int
}
func CheckAccess(plmn, gnbID string) AccessResult
func CheckAccessMap(plmn, gnbID string) map[string]interface{}

// Usage log
func InsertUsageLog(agreementID int64, plmn, gnbID string,
    ueCount int, throughputMbps float64) error
func ListUsageLog(agreementID int64, limit int) ([]map[string]interface{}, error)

// Stats / GUI panel
func GetStats() map[string]interface{}
func List() ([]map[string]any, error)   // alias of ListAgreements("")
func Status() map[string]any            // alias of GetStats()
```

## B.6 Test coverage

### B.6.1 Go unit tests

- `security/ran_sharing/ran_sharing_test.go` — input validation
  (sharing_type CHECK), PLMN-list parsing edge cases (`310`/`310-260`
  separation), MORAN gnb-map UPSERT idempotency, MOCN open-admission
  vs MORAN per-gNB-required, status transitions.

### B.6.2 Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-RANS-001 `ran_sharing_agreement_crud` | Create → GET (default `pending`) → Activate → Deactivate → Activate roundtrip. |
| TC-RANS-002 `ran_sharing_validation` | Invalid `sharing_type` ("WAT") and empty `participating_plmns` both 400. |
| TC-RANS-003 `ran_sharing_moran_gnb_map` | MORAN denies before gnb-map row, admits at correct `capacity_pct` after. |
| TC-RANS-004 `ran_sharing_mocn_admission` | MOCN admits without gnb-map; non-participating PLMN denied. |
| TC-RANS-005 `ran_sharing_usage_log` | Two usage rows inserted both surface in `/usage-log?agreement_id=...`. |

All five are wired into `tc_ran_sharing.py::ALL_RAN_SHARING_TCS` and
pass against the current core build.

## B.7 References

- **TS 22.261**:
  - §6.21 — NG-RAN Sharing (MORAN/MOCN concepts, broadcast obligations,
    admission contract).
  - §6.21.2.2 — Indirect network sharing.
- **TS 23.501** §5.17.4 — Network sharing support and EPS/5GS
  interworking.
- **TS 23.251** — NG-RAN Sharing Stage-2 (TODO; not loaded locally).
- **TS 38.413** §9.2.6.x — gNB broadcast of multi-PLMN-ID list (RAN-
  side; outside the scope of this surface).

# Roaming — HPLMN ↔ VPLMN agreements, sessions, CDRs

Inter-PLMN roaming surface for the SA Core: the operator's
agreement registry (HPLMN ↔ VPLMN, Home-Routed vs Local Break-Out),
the live session tracker, an admission probe used by the AMF on
Initial-Registration, and the wholesale CDR ledger plus a TAP-style
export hook.

# Part A — Functional

## A.1 Why roaming?

A 5G core that only serves its own SUPI prefix is a single-PLMN
deployment. Real-world deployments admit subscribers from partner
PLMNs under a roaming agreement, route their session control plane
back to the HPLMN (Home-Routed) or terminate it locally (Local Break-
Out), and exchange wholesale CDRs through a clearing house. This
package owns the operator-side projection of that flow:

- Maintain the agreement registry and the partner-PLMN NF endpoints
  (AUSF / UDM / SMF / SEPP) the AMF / SMF need at runtime.
- Mark whether inbound, outbound, or both directions are allowed,
  and whether HR / LBO / both is permitted.
- Track active roaming sessions so the GUI dashboard can show in-/
  outbound counts and the AMF can enforce per-agreement caps.
- Expose a `DetectRoaming(imsi)` probe so the Initial-Registration
  path can branch on agreement status before doing AUSF/UDM lookup.
- Persist closed-session CDRs and "export" them in TAP-style batches
  (the wire encoding itself is a TODO; `exported=1` flips on rows
  today).

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **N32-c / N32-f** | V-SEPP ↔ H-SEPP | HTTP/2 + JOSE / PRINS | TS 23.501 §5.6.3, TS 33.501 §13 | Endpoint URI in agreement; on-wire SEPP lives in `infra/roaming/sepp` |
| **Nausf / Nudm (visited)** | V-AMF → H-AUSF / H-UDM | SBI over N32 | TS 29.509 / 29.503 | Operator hard-codes endpoint URI today |
| **Nsmf (HR)** | V-SMF → H-SMF | SBI over N32 | TS 29.502 | Same — endpoint URI in agreement |
| **TAP / RAP** | wholesale clearing house | TAP 3.12 batch | TS 32.298 + GSMA TAP | `exported` flag flip; TAP wire encode is TODO |

## A.3 Operator-visible behaviours

### A.3.1 Agreement registry

A roaming `Agreement` carries:

| Field | Vocabulary | Notes |
|-------|------------|-------|
| `partner_plmn_id` | `MCC-MNC` | UNIQUE; primary key |
| `partner_name`    | free text | display only |
| `direction`       | `inbound` \| `outbound` \| `both` | controls admission for inbound and gating for outbound |
| `roaming_mode`    | `hr` \| `lbo` \| `both` | TS 23.501 §5.6.3 — Home-Routed vs Local Break-Out |
| `max_ues`         | int | per-agreement cap (enforcement TODO) |
| `allowed_sst` / `allowed_dnn` | string | comma-separated whitelists (interp by AMF/SMF) |
| `ausf_endpoint` / `udm_endpoint` / `smf_endpoint` / `sepp_endpoint` | URI | partner-PLMN NF base URLs |
| `enabled`         | 0/1 | soft toggle without delete |

`CreateAgreement` is an UPSERT (ON CONFLICT UPDATE on
`partner_plmn_id`).

### A.3.2 Roaming detection

`DetectRoaming(imsi)` derives the home PLMN from the IMSI prefix —
trying 3-digit MNC first, then 2-digit — and looks up the agreement.
Returns:

```json
{ "is_roaming": true, "home_plmn_id": "310-260",
  "agreement": { ... }, "roaming_mode": "hr" }
```

If no agreement matches, `is_roaming=true` but `agreement=null` —
the AMF / admission layer should reject. If the agreement exists but
direction is wrong, the response carries the agreement metadata
without a `roaming_mode` (the helper returns nil from
`IsRoamingAllowed`).

### A.3.3 Session tracker

Open one row per `(imsi, pdu_session_id?)` when the AMF / SMF admits a
roaming UE. Status starts at `active`; `ReleaseSession` moves it to
`released` with `end_time = NOW()`. The dashboard cards read live
counts from `GetStats()`.

### A.3.4 CDR ledger + export

`CreateCDR(...)` writes a wholesale charging row (per TS 32.298
record fields) on session close. `ExportPendingCDRs()` flips
`exported=1` on every unexported row and returns the count — the
TAP wire encoding itself is the deferred piece.

## A.4 Operator REST API (`/api/roaming/*`)

### A.4.1 Stats / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/roaming/stats` | combined session + CDR counters: `{active_sessions, inbound_active, outbound_active, total_sessions, unexported, total_cdrs, total_bytes}` |

### A.4.2 Agreements (TS 23.501 §5.6.3)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/roaming/agreements?enabled=1` | list (filter to `enabled=1` rows) |
| POST   | `/api/roaming/agreements` | UPSERT |
| GET    | `/api/roaming/agreements/{plmn}` | one agreement |
| DELETE | `/api/roaming/agreements/{plmn}` | remove |
| PATCH  | `/api/roaming/agreements/{plmn}/enabled` | `{enabled: bool}` toggle |

### A.4.3 Sessions

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/roaming/sessions?limit=N` | active sessions |
| GET    | `/api/roaming/sessions/{imsi}` | per-IMSI history |
| POST   | `/api/roaming/sessions` | open a session row |
| POST   | `/api/roaming/sessions/{imsi}/release` | close active rows for IMSI (optional `pdu_session_id` in body) |

### A.4.4 Detection (admission probe)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/roaming/detect/{imsi}` | DetectRoaming probe — used by AMF on Initial-Registration |

### A.4.5 CDRs (TS 32.240 / 32.298)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/roaming/cdrs?limit=N` | CDR list (newest first) |
| GET    | `/api/roaming/cdrs/stats` | `{total_cdrs, unexported, total_bytes}` |
| GET    | `/api/roaming/cdr-stats` | legacy alias of the above (compact shape) |
| POST   | `/api/roaming/cdrs` | insert one CDR |
| POST   | `/api/roaming/cdrs/export` | mark all unexported as exported |
| POST   | `/api/roaming/export-tap` | legacy alias of `/cdrs/export` |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| Nnrf-disc bootstrap of partner-PLMN NF endpoints | TS 29.510 | Operator hard-codes URIs today |
| I-SMF selection in Visited PLMN | TS 23.501 §5.34 | Today the SMF endpoint in an Agreement is the home-PLMN SMF |
| TAP wire encoding | TS 32.298 + GSMA TAP 3.12 | `exported=1` flip only; no batch file is produced |
| Per-agreement `max_ues` enforcement | TS 23.501 §5.17.4 | Field stored, not yet enforced at admission |
| Live SEPP integration with this surface | TS 33.501 §13 | SEPP lives in `infra/roaming/sepp`; agreement endpoint URI is informational here |

---

# Part B — Design

## B.1 Process layout

```
┌─────────── infra/roaming (operator-of-record) ───────────┐
│                                                          │
│  Agreements              Sessions               CDRs     │
│   roaming_agreements      roaming_sessions       roaming_cdrs
│   ├── CreateAgreement     ├── CreateSession      ├── CreateCDR
│   ├── DeleteAgreement     ├── ReleaseSession     ├── ListCDRs
│   ├── GetAgreement        ├── GetActiveSessions  ├── GetCDRStats
│   ├── ListAgreements      └── GetSessionsForIMSI └── ExportPendingCDRs
│   ├── SetAgreementEnabled                                │
│   ├── IsRoamingAllowed                                   │
│   └── GetRoamingMode                                     │
│                                                          │
│  Detection probe (TS 23.501 §5.6.3 admission gate)       │
│   DetectRoaming(imsi) → DetectResult                     │
│       └── derives MCC-MNC from SUPI prefix,              │
│           tries 3-digit then 2-digit MNC,                │
│           returns the in-DB agreement if direction fits. │
│                                                          │
└──────────────────────────────────────────────────────────┘
                                 ▲
                                 │ JSON / HTTP via OAM panel
                                 │
                       webservice/app/routes_roaming.go
```

The SEPP wire surface (`infra/roaming/sepp`) is a separate concern —
this package only stores the SEPP endpoint URI as part of an
agreement; live N32 traffic happens elsewhere.

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `infra/roaming/roaming.go` | ~520 | Agreement / Session / CDR helpers + DetectRoaming. |
| `infra/roaming/roaming_test.go` | ~100 | DetectRoaming, MNC-length fallbacks, agreement lookup. |
| `db/schemas/domains.go` | (slice) | DDL for `roaming_agreements`, `roaming_sessions`, `roaming_cdrs` (already present). |
| `webservice/app/routes_roaming.go` | ~330 | REST surface for §A.4. |

Tests:
- `mmt_studio_core_tester/src/testcases/access/tc_roaming.py` — 5 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS roaming_agreements (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  partner_plmn_id TEXT NOT NULL UNIQUE,
  partner_name    TEXT,
  direction       TEXT NOT NULL CHECK (direction IN ('inbound','outbound','both')),
  roaming_mode    TEXT NOT NULL CHECK (roaming_mode IN ('hr','lbo','both')),
  max_ues         INTEGER NOT NULL DEFAULT 0,
  allowed_sst     TEXT,
  allowed_dnn     TEXT,
  ausf_endpoint   TEXT,
  udm_endpoint    TEXT,
  smf_endpoint    TEXT,
  sepp_endpoint   TEXT,
  enabled         INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS roaming_sessions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  imsi            TEXT NOT NULL,
  home_plmn_id    TEXT NOT NULL,
  visited_plmn_id TEXT NOT NULL,
  direction       TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
  roaming_mode    TEXT NOT NULL CHECK (roaming_mode IN ('hr','lbo')),
  pdu_session_id  INTEGER,
  start_time      TEXT NOT NULL DEFAULT (datetime('now')),
  end_time        TEXT,
  status          TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','released','failed'))
);

CREATE TABLE IF NOT EXISTS roaming_cdrs (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  imsi            TEXT NOT NULL,
  home_plmn_id    TEXT NOT NULL,
  visited_plmn_id TEXT NOT NULL,
  direction       TEXT NOT NULL,
  record_type     TEXT NOT NULL DEFAULT 'session'
                  CHECK (record_type IN ('session','event')),
  dnn             TEXT,
  sst             INTEGER,
  start_time      TEXT NOT NULL DEFAULT (datetime('now')),
  end_time        TEXT,
  bytes_ul        INTEGER NOT NULL DEFAULT 0,
  bytes_dl        INTEGER NOT NULL DEFAULT 0,
  duration_sec    REAL NOT NULL DEFAULT 0,
  cause           TEXT,
  exported        INTEGER NOT NULL DEFAULT 0
);
```

## B.4 Detection algorithm

```
DetectRoaming(imsi):
   if len(imsi) < 5: return nil
   mcc = imsi[:3]
   for mncLen in [3, 2]:
      if len(imsi) < 3+mncLen: continue
      mnc = imsi[3:3+mncLen]
      candidate = mcc + "-" + mnc
      if GetAgreement(candidate) exists:
          if IsRoamingAllowed(candidate):
              return {is_roaming, home_plmn_id, agreement, roaming_mode}
          else:
              return {is_roaming=true, home_plmn_id, agreement=nil}
   # No agreement — still roaming, but unknown HPLMN
   return {is_roaming=true, home_plmn_id=mcc + "-" + imsi[3:5]}
```

The 3-digit-first ordering matches GSMA practice: NA carriers use
3-digit MNCs (`311-480` Verizon, `310-260` T-Mobile US) so trying the
longer form first is essential to avoid a false 2-digit hit.

## B.5 Public API

```go
// Agreements
type Agreement struct {
    ID            int64
    PartnerPLMNID string
    PartnerName   string
    Direction     string  // inbound | outbound | both
    RoamingMode   string  // hr | lbo | both
    MaxUEs        int
    AllowedSST    string
    AllowedDNN    string
    AUSFEndpoint  string
    UDMEndpoint   string
    SMFEndpoint   string
    SEPPEndpoint  string
    Enabled       int
}
func CreateAgreement(plmn, name, dir, mode string, maxUEs int,
    sst, dnn, ausf, udm, smf, sepp string) error
func DeleteAgreement(plmn string) error
func GetAgreement(plmn string) (*Agreement, error)
func ListAgreements(enabledOnly bool) ([]Agreement, error)
func SetAgreementEnabled(plmn string, enabled bool) error
func IsRoamingAllowed(plmn string) (*Agreement, error)
func GetRoamingMode(plmn string) (string, error)

// Detection
type DetectResult struct {
    IsRoaming   bool
    HomePLMNID  string
    Agreement   *Agreement
    RoamingMode string
}
func DetectRoaming(imsi string) *DetectResult

// Sessions
type Session struct{ /* IMSI, HomePLMNID, VisitedPLMNID, … */ }
func CreateSession(imsi, hplmn, vplmn, dir, mode string, pduSessID *int) error
func ReleaseSession(imsi string, pduSessID *int) error
func GetActiveSessions(limit int) ([]Session, error)
func GetSessionsForIMSI(imsi string) ([]Session, error)

// CDRs
type CDR struct{ /* IMSI, HomePLMNID, VisitedPLMNID, BytesUL/DL, … */ }
func CreateCDR(imsi, hplmn, vplmn, dir, recType string,
    dnn *string, sst *int, ulBytes, dlBytes int64,
    durationSec float64, cause *string) (int64, error)
func ListCDRs(limit int) ([]CDR, error)
func GetCDRStats() (*CDRStats, error)
func ExportPendingCDRs() (int, error)

// Stats
type Stats struct{ ActiveSessions, InboundActive, OutboundActive, TotalSessions int }
type CDRStats struct{ TotalCDRs, Unexported int; TotalBytes int64 }
func GetStats() (*Stats, error)
```

## B.6 Test coverage

### B.6.1 Go unit tests

- `infra/roaming/roaming_test.go` — DetectRoaming MNC-length
  fallback, GetAgreement / SetAgreementEnabled, session lifecycle.

### B.6.2 Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-ROAM-001 `roaming_agreement_crud` | Create → GET → PATCH disable → DELETE roundtrip |
| TC-ROAM-002 `roaming_validation` | Bad `direction` and `roaming_mode` values get HTTP 400 |
| TC-ROAM-003 `roaming_detect` | DetectRoaming finds an in-DB agreement and returns the right HPLMN + mode |
| TC-ROAM-004 `roaming_session_lifecycle` | Open session → see in actives → release → no longer active |
| TC-ROAM-005 `roaming_cdr_export` | Insert CDR → unexported=1 → export → unexported=0 |

All five are wired into `tc_roaming.py::ALL_ROAMING_TCS` and pass
against the current core build.

## B.7 References

- **TS 23.501**:
  - §5.6.3 — SM-Roaming (HR vs LBO architecture).
  - §5.7.1.11 — QoS aspects of home-routed roaming.
  - §5.17.4 — Network sharing + EPS/5GS interworking when an
    agreement carries SST/DNN restrictions.
  - §5.34 — TODO; I-SMF selection in Visited PLMN.
- **TS 29.502 / 29.503 / 29.509 / 29.510** — SBI services that ride
  the agreement-supplied AUSF/UDM/SMF endpoint URIs.
- **TS 32.240 / 32.298** — Charging architecture and CDR fields.
- **TS 33.501** §13 — SEPP / N32 security; partner endpoint URIs are
  consumed by the SEPP layer (`infra/roaming/sepp`).
- **GSMA TAP 3.12** — wholesale CDR file format (TODO at the
  `ExportPendingCDRs` boundary).

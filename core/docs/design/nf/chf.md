# CHF — Charging Function

3GPP TS 32.290 / TS 32.291 CHF; converged charging. ~1.5k LOC at
`nf/chf/`. Generates CDRs, runs a rating engine, manages prepaid
balance and online quota grants, generates monthly invoices.

## 1. Role in 5GC

The CHF is the converged charging endpoint. SMF (data) and IMS
(voice/video) trigger it on session lifecycle events; for online
charging it grants and accounts quota in real time. Triggers in this
build come from in-process hooks (no Nchf SBI yet).

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Nchf** | SMF (data CTF) | Nchf_ConvergedCharging | TS 32.290 / TS 32.291 §6.1 |
| **Nchf** | IMS AS / S-CSCF | Nchf_ConvergedCharging | TS 32.260 |
| (intra-NF) | SMF | `OnPDUSessionCreated` / `OnPDUSessionReleased` triggers | TS 32.291 §6.1.3.2 |
| (intra-NF) | IMS / AF | `OnIMSCallEnded` on SIP BYE | TS 32.260 |

## 2. Architecture

```
   SMF                      CHF                       Subscriber DB (SQLite)
    │ PDU established        │                           │
    ├───────────────────────►│ OnPDUSessionCreated       │
    │                        │   charging_sessions row   │
    │                        │   if online: CheckBalance ├─► balances
    │                        │                           │
    │  Volume usage           │                           │
    │ (URR reports drive      │                           │
    │  quota report path —    │                           │
    │  online only)           │                           │
    │                        │  ReportUsage / GrantQuota ├─► quota_grants
    │                        │                           │
    │ PDU released            │                           │
    ├───────────────────────►│ OnPDUSessionReleased      │
    │                        │   GenerateDataCDR         ├─► cdrs (status=pending)
    │                        │   if online: RateCDR +    │
    │                        │              Debit        ├─► rated_cdrs / balances
    │                        │                           │
   IMS AS / S-CSCF            │                           │
    │ SIP BYE                 │                           │
    ├───────────────────────►│ OnIMSCallEnded            │
    │                        │   GenerateVoiceCDR        ├─► cdrs (voice/video)
    │                        │                           │
   ─────────────────────────────────────────────────────────────────────
   Periodic / on-demand       │                           │
                              │ RatePendingCDRs           ├─► rated_cdrs
                              │ GenerateInvoices(period)  ├─► invoices
```

## 3. Package / file map

| File | Role | LOC |
|------|------|-----|
| `nf/chf/cdr.go` | CDR generation (data + voice + video), trigger hooks, invoice generation, CSV export | 558 |
| `nf/chf/rating.go` | Rating engine — applies tariff plans to pending CDRs, produces `rated_cdrs` rows; per-volume / per-time / per-event / flat | 329 |
| `nf/chf/balance_manager.go` | Prepaid balance: GetBalance, Recharge, Debit, CheckBalance | 212 |
| `nf/chf/quota_manager.go` | Online quota: GrantQuota, ReportUsage, CheckQuota, RevokeQuota | 373 |

## 4. SBI / non-SBI surface

### 4.1 Operator REST API (`/api/chf/*`)

Wired in `webservice/app/routes_chf.go` against the helpers in
`nf/chf/api.go`. Before this surface landed, `/api/chf/*` was a
7-line stub block in `routes_nsaas.go` returning empty objects;
the package's full machinery was unreachable from the panel and
tester. All responses follow the `{ok: true, ...}` envelope.

| Method | Path | Purpose | Spec |
|--------|------|---------|------|
| GET    | `/api/chf/stats` | `{active_sessions, total_cdrs, pending_cdrs, rated_cdrs, active_quotas}` | — |
| GET    | `/api/chf/charging-data?status=&limit=` | List charging sessions; envelope contains both `sessions` and panel-side `items` alias. | TS 32.290 §6.2 |
| POST   | `/api/chf/charging-data` | `{imsi, pdu_session_id?, service_name?, charging_method?}` → Initial Charging Data Request. | TS 32.291 §6.1 |
| GET    | `/api/chf/charging-data/{session_id}` | Single session row; 404 on unknown. | — |
| PUT    | `/api/chf/charging-data/{session_id}` | `{usage:{volume_uplink, volume_downlink, duration_s}, used_units?}` → Update. | TS 32.291 §6.1 |
| POST   | `/api/chf/charging-data/{session_id}/release` | `{final_usage:{...}}?` → Termination + final CDR. | TS 32.291 §6.1 |
| GET    | `/api/chf/cdrs?imsi=&type=&status=&limit=` | CDR list with filters. | TS 32.298 |
| POST   | `/api/chf/cdrs/export` | `{imsi?, limit?}` → CSV string + row count. | — |
| GET    | `/api/chf/quotas?imsi=&status=&limit=` | Quota grant list. | TS 32.291 §6.1.3.2 |
| POST   | `/api/chf/quotas/grant` | `{imsi, service, requested_units}`. | TS 32.291 §6.1.3.2 |
| POST   | `/api/chf/quotas/report` | `{imsi, service, used_units}`. | TS 32.291 §6.1.3.2 |
| POST   | `/api/chf/quotas/check` | `{imsi, service}`. | TS 32.291 §6.1.3.2 |
| POST   | `/api/chf/quotas/revoke` | `{imsi, service}` → `{revoked:N}`. | TS 32.291 §6.1.3.2 |
| GET    | `/api/chf/balances/{imsi}` | All balance rows for an IMSI. | TS 32.291 §6.1 |
| POST   | `/api/chf/balances/recharge` | `{imsi, amount>0, balance_type?, reference?}`. | — |

The session_id format is `<imsi>-<pdu_session_id>` synthesised
inside `OnPDUSessionCreated`; `nf/chf/api.go::splitSessionID`
parses it back when the operator-API release path needs to
reach the SMF-trigger code-path.

### 4.2 Internal Go calls

Operations are Go calls keyed to the §-shaped service operations:

| Method (Go) | 3GPP operation | Spec § |
|-------------|----------------|--------|
| `GenerateDataCDR` / `GenerateVoiceCDR` | CHF CDR generation | TS 32.298 (record format guidance) |
| `OnPDUSessionCreated` / `OnPDUSessionReleased` | Nchf_ConvergedCharging trigger (Initial/Termination) | TS 32.291 §6.1.3.2 |
| `OnIMSCallEnded` | IMS charging trigger | TS 32.260 |
| `GrantQuota` | CHF grants service units to CTF | TS 32.291 §6.1.3.2 |
| `ReportUsage` | CTF reports consumed units | TS 32.291 §6.1.3.2 |
| `RevokeQuota` | revoke on session release / policy change | TS 32.291 §6.1.3.2 |
| `CheckBalance` / `Debit` / `Recharge` | prepaid enforcement | TS 32.291 §6.1 |
| `RateCDR` / `RatePendingCDRs` | rating engine | (impl-defined) |
| `GenerateInvoices` / `GetInvoices` | billing rollup | (impl-defined) |
| `ExportCDRsCSV` | CSV dump | — |

## 5. Headline lifecycle — data session CDR cycle (offline + online)

`OnPDUSessionCreated` (`cdr.go:277-308`):

1. Insert `charging_sessions` row (`status=active`).
2. If `chargingMethod == "online"`, call `CheckBalance(imsi, 0)` —
   reject if exhausted.

Mid-session (online only) — quota grant/report (`quota_manager.go`):

```
GrantQuota(imsi, service, requestedUnits):
  read balances + credit_limit
  read charging_profiles (vol_quota_ul/dl, time_quota_sec)
  unitCost = lookup tariff_plans (per-service or default)
  granted  = min(requested, profile_max, balance/unitCost)
  insert quota_grants(status=active, expires_at = now + validityTime)

ReportUsage(imsi, service, usedUnits):
  load latest active grant
  if expires_at < now → mark expired
  newUsed = currentUsed + usedUnits
  if remaining ≤ 0   → status=exhausted
  else                → update used_units
```

`OnPDUSessionReleased` (`cdr.go:311-359`):

1. Read `charging_method` from `charging_sessions`.
2. Mark session `released`.
3. `GenerateDataCDR(imsi, pduSessID, dnn, ...)` → `cdrs` row,
   `rating_status=pending`. CDR id format
   `CDR-<imsi>-<pduSessID>-<endTime>`.
4. If online: rate the CDR (`RateCDR`) and `Debit(imsi, amount, "main", cdrID)`.

Voice/video (IMS): `OnIMSCallEnded(imsi, callID, callerURI, calleeURI,
start, end, mediaType)` directly inserts a `cdr_type=voice|video`
row (`cdr.go:362-366`), media-type-derived. Charging method is
hard-coded `"offline"` today.

Rating cycle (offline path) — `RatePendingCDRs()` /
`RatePendingCDRsWithTax(taxRate, limit)`:

- Pull `cdrs WHERE rating_status='pending'`.
- For each: `RateCDR(cdr, tariff, taxRate)` →
  `charge = (usage_units / unit_size) * unit_cost`. Supports
  per-volume / per-time / per-event / flat (`rating.go:6-9`).
- Write `rated_cdrs`, set `cdrs.rating_status='rated'`.

Invoice cycle — `GenerateInvoices(periodStart, periodEnd)`
(`cdr.go:384-505`):

- For each IMSI with rated CDRs in window, sum charges by
  `cdr_type` (data / voice / video) + tax → `invoices` row, mark
  source CDRs `rating_status='invoiced'`.
- Invoice number: `INV-YYYYMM-<6-hex>`.

## 6. Key types / public API

```go
// CDR generation (cdr.go)
func GenerateDataCDR(imsi string, pduSessionID int, dnn string,
    startTime, endTime float64, volUL, volDL, pktUL, pktDL int64,
    sst, fiveqi int, chargingMethod string, tariffPlanID *int64) (string, error)
func GenerateVoiceCDR(imsi, callID, callerURI, calleeURI string,
    startTime, endTime float64, mediaType string,
    msisdn, chargingMethod string, tariffPlanID *int64) (string, error)
type CDR struct{ /* full row from cdrs */ }
func GetCDRs(imsi, cdrType, status string, dateFrom, dateTo float64, limit int) ([]CDR, error)
func GetCDRStats() (CDRStats, error)
func ExportCDRsCSV(imsi string, dateFrom, dateTo float64, limit int) (string, error)

// Trigger hooks (cdr.go)
func OnPDUSessionCreated(imsi string, pduSessionID int, dnn, chargingMethod string) bool
func OnPDUSessionReleased(imsi string, pduSessionID int)
func OnIMSCallEnded(imsi, callID, callerURI, calleeURI string,
    startTime, endTime float64, mediaType string)

// Rating (rating.go)
type RatedCDR struct{ /* charge_amount, tax_amount, total_amount, ... */ }
func RateCDR(cdr CDR, tariff map[string]interface{}, taxRate float64) *RatedCDR
func RatePendingCDRs() (int, error)
func RatePendingCDRsWithTax(taxRate float64, limit int) (int, error)

// Balance (balance_manager.go)
type Balance struct{ /* ... */ }
func GetBalance(imsi, balanceType string) *Balance
func GetAllBalances(imsi string) []Balance
func CreateBalance(imsi, balanceType string, amount float64, currency string, ...)
func Recharge(imsi string, amount float64, balanceType, reference string) float64
func Debit(imsi string, amount float64, balanceType, reference string) (bool, float64)
func CheckBalance(imsi string, requiredAmount float64) (bool, float64)

// Quota (quota_manager.go)
type QuotaGrant struct{ /* granted_units, validity_time, ... */ }
type QuotaStatus struct{ /* used_units, remaining_quota, status */ }
func GrantQuota(imsi, service string, requestedUnits int64) QuotaGrant
func ReportUsage(imsi, service string, usedUnits int64) QuotaStatus
func CheckQuota(imsi, service string) QuotaStatus
func RevokeQuota(imsi, service string) int64
func GetAllGrants(imsi, status string, limit int) ([]map[string]interface{}, error)

// Invoicing (cdr.go)
type Invoice struct{ /* ... */ }
func GenerateInvoices(periodStart, periodEnd float64) ([]Invoice, error)
func GetInvoices(imsi, status string, limit int) ([]map[string]interface{}, error)

// Operator-API wrappers (api.go) — used by /api/chf/* routes.
// session_id format is "<imsi>-<pdu_session_id>" so the wrappers
// share the trigger-side OnPDUSessionReleased code-path on close.
func CreateChargingSession(imsi, serviceName, chargingMethod string,
    pduSessionID int) (map[string]any, error)
func UpdateChargingSession(sessionID string, usageVolUL, usageVolDL int64,
    durationSec int, usedUnits int64) (map[string]any, error)
func ReleaseChargingSession(sessionID string,
    finalVolUL, finalVolDL int64, finalDuration int) (map[string]any, error)
func GetChargingSession(sessionID string) (map[string]any, error)
func ListChargingSessions(status string, limit int) ([]map[string]any, error)
func GetStats() map[string]any
```

## 7. What's not implemented — TODOs / stubs

The package code does not carry explicit TODO markers, but reading
the surfaces shows:

- **No Nchf SBI router**: triggers come from in-process Go calls
  (`OnPDUSessionCreated`, `OnPDUSessionReleased`,
  `OnIMSCallEnded`). The TS 32.290 / 32.291 HTTP/2 + JSON envelope
  is not wired.
- **CHF as a service consumer of the CGF**: no GTP'/Diameter Rf path.
  Records are written straight to local SQLite tables (`cdrs`,
  `rated_cdrs`, `invoices`) — there is no §6.1.5 file-based
  CDR transfer to a CGF.
- **Final Unit Indication / Granted-Service-Unit (Diameter Gy
  carryover)**: there is no FUI structure on `QuotaStatus` — the
  CTF can only see `status` + `remaining_quota`, no
  REDIRECT/RESTRICT_ACCESS/TERMINATE action hint.
- **Reauth requests**: no PCF-driven re-authorization signal; quota
  flow is one-shot grant + report cycles.
- **Multi-balance / multi-currency mixing**: `Debit` only walks one
  `balance_type` (default `"main"`).
- **Static MSISDN**: `GenerateVoiceCDR` accepts `msisdn` but
  `OnIMSCallEnded` always passes `""`.
- **5G NEF charging (TS 32.254)**: not modelled.

## 7a. Test coverage

`mmt_studio_core_tester/src/testcases/vas/tc_chf_oam.py` —
operator-API TCs (no UE/gNB needed). All seven pass against the
current core build:

| TC | Coverage |
|----|----------|
| TC-CHF-010 `chf_stats_shape`                   | `/stats` envelope: `active_sessions, total_cdrs, pending_cdrs, rated_cdrs, active_quotas` |
| TC-CHF-011 `chf_charging_data_lifecycle`       | create → interim update (volume_uplink + volume_downlink + duration_s) → release (final_usage) → CDR for that IMSI exists |
| TC-CHF-012 `chf_charging_data_validation`      | missing imsi / bad charging_method → 400; unknown session_id PUT/GET → 404 |
| TC-CHF-013 `chf_quota_grant_report_revoke`     | recharge balance → grant 1000 → report 250 → check → revoke; missing imsi/service → 400 |
| TC-CHF-014 `chf_balance_recharge_and_read`     | recharge → balances list reflects; negative amount / missing imsi → 400 |
| TC-CHF-015 `chf_cdr_export_csv`                | session lifecycle generates CDR → CSV export contains header + row |
| TC-CHF-016 `chf_charging_data_list`            | active session is listed under `?status=active` and unfiltered |

`tc_charging.py` carries the legacy UE/gNB integration TCs
(TC-CHF-001..005) that exercise the surface end-to-end after
`establish_pdu`.

## 8. References (cited in source)

Verbatim from `nf/chf/`:

- TS 32.260 (IMS charging — `cdr.go:71`)
- TS 32.290 / TS 32.291 (`cdr.go:3`)
- TS 32.291 §6.1 (`balance_manager.go:3`)
- TS 32.291 §6.1.3.2 (`quota_manager.go:3, :42, :137, :246`)

---
*Last refreshed against commit `13a181d`.*

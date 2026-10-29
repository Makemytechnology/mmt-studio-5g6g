# KPIs — KPI Dashboard aggregator (TS 28.552 / TS 28.554)

The `/api/kpis` aggregator and its raw siblings (`/api/kpis/raw`,
`/api/kpis/reset-peaks`). One fetch per panel refresh produces the
nested-by-NF JSON shape that `templates/kpis.html` reads directly.

# Part A — Functional

## A.1 Why a separate aggregator?

The KPI panel needs *one* HTTP round-trip per refresh tick; the panel
layout dictates a nested-by-NF shape (AMF / SMF / UPF / IP-pool / IMS /
MCX / FM / Charging / Services). The raw `oam/pm` counter dump is the
right shape for SRE / Prometheus, but it is the wrong shape for the
panel — the panel doesn't want `RM.RegSucc=42` and `SM.SessSucc=17`,
it wants `amf.reg_successes=42` and `smf.sess_successes=17`. So the
aggregator is the *adapter* between TS 28.552 family naming on the
producer side and the panel's nested view on the consumer side.

The aggregator also pulls from sources `oam/pm` cannot see —
`amf.UEs(nil)` for current RM/CM-state counts, the `services` table
for the QoS catalogue, the `ims_dialogs` / `mcx_*` tables for IMS+MCX
counts, `ipalloc.Default.UsageDetail()` for live pool occupancy, and
`upfMgr.Default.GetIOStats()` for the dataplane volume counters.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **PM counters** | NF hot path → `pm.Default` | in-process | TS 28.552 (deferred) | Counter names use TS 28.552 family prefixes; see `oam/pm`. |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/kpis/*` (this file). |
| **Prometheus scrape** | Prometheus → 5GC | HTTP | Prom textfmt | `/metrics` (separate; see `oam/pm`). |

## A.3 Operator REST API (`/api/kpis/*`)

### A.3.1 Panel aggregator (the GET that drives the dashboard)

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/kpis` | Nested-by-NF aggregator; consumed by `templates/kpis.html`. |

The response shape is:

```jsonc
{
  "amf": {                         // TS 28.552 §5.1
    "registered_ue": int,          // RM-state == REGISTERED count (live)
    "connected_ue": int,           // CM-state == CONNECTED count (live)
    "idle_ue": int,                // CM-state == IDLE count (live)
    "total_ue_contexts": int,      // every UE in the AMF registry
    "gnb_count": int,              // SCTP-connected gNBs
    "gnb_total": int,              // every gNB ever seen
    "gnb_distribution": [          // bar-chart payload
      {"name": str, "ue_count": int, "connected": bool}
    ],
    "auth_completed": int,         // AUTH.Succ counter
    "security_established": int,   // SEC.Succ counter
    "reg_attempts" / "reg_successes" / "reg_failures": int,
    "reg_success_rate": float,     // TS 28.554 Mean-of-Ratios %
    "auth_attempts" / "auth_successes" / "auth_failures": int,
    "auth_success_rate": float,
    "ngap_setup_attempts" / "ngap_setup_successes" / "ngap_setup_failures": int
  },
  "smf": {                         // TS 28.552 §5.3
    "total_pdu_sessions": int,
    "active_pdu_sessions": int,
    "total_bearers" / "gbr_bearers" / "nongbr_bearers": int,
    "sess_attempts" / "sess_successes" / "sess_failures": int,
    "sess_success_rate": float,
    "flow_attempts" / "flow_successes" / "flow_failures": int,
    "pdu_per_dnn":   {"<dnn>": count, …},
    "pdu_per_slice": {"<sst>" | "<sst>-<sd>": count, …}
  },
  "upf": {                         // TS 28.552 §5.4
    "sessions" / "running": int / bool,
    "ul_pkts" / "dl_pkts": int,
    "ul_bytes" / "dl_bytes": int,
    "ul_dropped" / "dl_dropped": int,
    "ul_metered" / "dl_metered": int,
    "gtpu_errors": int,
    "packet_loss_rate": float      // dropped / (delivered + dropped) × 100
  },
  "ip_pools": [                    // per-(DNN, IP version) row
    {"dnn": "internet_v4", "allocated": int, "total": int, "utilization_pct": float}
  ],
  "ims": {
    "total_subscribers": int,      // ims_subscribers
    "active_registrations": int,   // CSCF in-process; reported as 0 today
    "active_calls": int,           // ims_dialogs WHERE state != TERMINATED
    "calls_by_state": {"<state>": count, …}
  },
  "mcx": {
    "total_users" / "total_groups": int,    // enabled rows in mcx_user_profiles / mcx_groups
    "active_calls": int,                    // mcx_active_calls WHERE state == 'active'
    "total_messages": int,                  // mcx_messages
    "floor_grants": int,                    // mcx_floor_history WHERE event == 'granted'
    "calls_by_type": {"group" | "private" | "emergency" | "broadcast": count}
  },
  "fm": {                          // TS 28.532
    "Critical" / "Major" / "Minor" / "Warning" / "Indeterminate": int,
    "total": int                   // sum of severity buckets
  },
  "charging": {
    "total_profiles": int,
    "online" / "offline": int,
    "linked_services": int         // services rows with charging_profile NOT NULL
  },
  "services": {
    "total": int,
    "by_5qi": {"<5QI>": count, …}  // grouped from services.fiveqi
  },
  "timestamp": float               // unix seconds (panel uses for delta-rate calc)
}
```

### A.3.2 Raw counter dump (TS 28.552 names, for tester / Prometheus tooling)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/kpis/raw?window_sec=N` | Flat counters / rates / peaks dump. |
| POST   | `/api/kpis/reset-peaks`      | Zero the peak-rate table. |

`/api/kpis/raw` returns:

```jsonc
{
  "counters":          {"RM.RegAtt": 42, "AUTH.Succ": 41, …},
  "rates_per_sec":     {"RM.RegAtt": 0.42, …},   // Rate(name, window)
  "peak_rates":        {"RM.RegAtt": 0.84, …},   // PeakRate since reset
  "reg_success_rate":  float,                    // TS 28.554 Mean-of-Ratios %
  "auth_success_rate": float,
  "sm_success_rate":   float,
  "window_sec":        float                     // honoured echo of ?window_sec=
}
```

`reset-peaks` is the panel's "Reset Peaks" button. Empty body, `{ok: true}`
on success.

## A.4 Operator-visible behaviours

### A.4.1 Mean-of-Ratios success rate (TS 28.554 §6)

`SuccessRate(succName, failName) = succ / (succ + fail) × 100`. Returns
**−1** as the "no attempts yet" sentinel — the panel renders this as `-`
rather than `0%` to distinguish "untested" from "0% success." The
aggregator preserves the −1 through `roundPct()`.

### A.4.2 GBR / non-GBR bearer split (TS 23.501 §5.7.4)

`smf.gbr_bearers` + `smf.nongbr_bearers` always equals
`smf.total_bearers`. The classifier (`isGBR`) consults the standardised
5QI table — values 1, 2, 3, 4, 65, 66, 67, 75 (Standard GBR) and
82–90 (Delay-Critical GBR) are GBR; everything else (including
operator 5QIs 96–127, 248–255) is non-GBR. To re-classify operator
5QIs by the `services.resource_type` column, lookup against the
`services` table inside `buildSMFSection`. Today we keep it standardised
so the classification is deterministic regardless of operator catalogue
state.

### A.4.3 IP-pool utilisation

`ip_pools` is the union of `ipalloc.Default.UsageDetail()` keys (live
allocations) and the (DNN, version) keys derivable from `apn_config`
JOIN `apn_ip_pools`. A pool with capacity but zero allocations still
shows up so operators see the headroom. Capacity calculation mirrors
the allocator — network + broadcast + gateway are non-assignable for
v4 (network + gateway for v6), and the v6 host count is capped at 2²⁰
(matches the allocator's `expandAll` cap).

### A.4.4 Section independence

Every `build*Section` reads from its own data source and degrades to a
zero/empty payload on read failure. A failed DB connection from
`buildIMSSection` doesn't blank `buildAMFSection` — the panel keeps
displaying everything else.

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| TS 28.552 / 28.554 PDFs not in `specs/3gpp/` | — | Counter names follow the family prefixes; clauses are deferred-cited in `oam/pm/perf_counters.go`. |
| `ims.active_registrations` always 0 | — | CSCF stores registrations in-process; no DB table to JOIN against. Wire when the registration table lands. |
| 5QI classifier ignores `services.resource_type` | — | Only consults the standardised TS 23.501 §5.7.4 table; operator 5QIs default to non-GBR. |
| Charging `linked_services` doesn't tell which CHF tier | — | `/api/kpis` reports the count; tier breakdown lives in `/api/charging/*`. |

---

# Part B — Design

## B.1 Process layout

```
┌────────────────── /api/kpis ─────────────────┐
│                                              │
│  buildKPIDashboard()                         │
│   ├── buildAMFSection                        │
│   │     amf.UEs(nil) / amf.Gnbs(nil) +       │
│   │     pm.Default.{Get, SuccessRate}        │
│   ├── buildSMFSection                        │
│   │     session.Default.All() (per-session   │
│   │     DNN/SST/SD/5QI rollups) +            │
│   │     pm.Default.{Get, SuccessRate}        │
│   ├── buildUPFSection                        │
│   │     upfMgr.Default.GetIOStats()          │
│   ├── buildIPPoolsSection                    │
│   │     ipalloc.Default.UsageDetail()        │
│   │     UNION                                │
│   │     SELECT a.name,p.cidr FROM            │
│   │       apn_config a JOIN apn_ip_pools p   │
│   ├── buildIMSSection                        │
│   │     SELECT … FROM ims_subscribers,       │
│   │     ims_dialogs                          │
│   ├── buildMCXSection                        │
│   │     SELECT … FROM mcx_*                  │
│   ├── fm.Default.Counts()                    │
│   ├── buildChargingSection                   │
│   │     SELECT … FROM charging_profiles,     │
│   │     services                             │
│   └── buildServicesSection                   │
│         SELECT fiveqi, COUNT(*) FROM         │
│         services GROUP BY fiveqi             │
└──────────────────────────────────────────────┘
                         ▲
                         │ JSON / HTTP
                         │
                webservice/app/routes_kpis.go
                webservice/templates/kpis.html
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `webservice/app/routes_kpis.go` | ~395 | Aggregator + raw + reset-peaks routes (this surface). |
| `webservice/app/health_route.go` | (slice) | `/api/health`, `/api/live`, `/api/ready`, `/api/faults*`. KPI routes used to live here; now removed. |
| `oam/pm/perf_counters.go` | (slice) | Counter producer + Mean-of-Ratios success-rate helper. |
| `webservice/templates/kpis.html` | (panel) | Consumes the nested-by-NF shape verbatim. |

Tests:
- `mmt_studio_core_tester/src/testcases/oam/tc_kpis.py` — 8 live integration TCs.

## B.3 Key data sources

```go
// PM counters — TS 28.552 family-prefixed names
pm.Default.Get(name string) int64
pm.Default.SuccessRate(succ, fail string) float64   // -1 = no attempts
pm.Default.Rate(name string, window time.Duration) float64
pm.Default.PeakRate(name string) float64

// AMF live state
amf.UEs(nil)  []amf.UESummary    // RM, CM, GnbKey
amf.Gnbs(nil) []amf.GnbSummary

// SMF session table
session.Default.All() []*session.Session   // DNN, SST, SD, FiveQI, State

// SMF IP allocator
ipalloc.Default.UsageDetail() map[string]ipalloc.PoolDetail

// UPF dataplane
upfMgr.Default.GetIOStats() upfMgr.IOStats
upfMgr.Default.SessionCount() int

// FM
fm.Default.Counts() map[string]int  // Critical/Major/Minor/Warning/Indeterminate/total
```

DB queries (read-only) cover IMS dialogs, MCX tables, charging profiles,
service catalogue, and APN IP pool capacity — see `routes_kpis.go` for
exact SQL.

## B.4 isGBR classifier (TS 23.501 §5.7.4 Table 5.7.4-1)

```go
func isGBR(fiveQI uint8) bool {
    switch fiveQI {
    case 1, 2, 3, 4, 65, 66, 67, 75:                      // Standard GBR
        return true
    case 82, 83, 84, 85, 86, 87, 88, 89, 90:              // Delay-Critical GBR
        return true
    }
    return false                                          // Non-GBR (incl. operator 5QIs)
}
```

Standardised values only — operator 5QIs (96-127, 248-255) report as
non-GBR. Re-classify against `services.resource_type` if the operator
catalogue takes precedence.

## B.5 Public API

```go
// Routes
func (s *Server) registerKPIsRoutes()

// Aggregator (re-usable from /metrics or any other surface)
func buildKPIDashboard()    map[string]any
func buildAMFSection()      map[string]any
func buildSMFSection()      map[string]any
func buildUPFSection()      map[string]any
func buildIPPoolsSection()  []map[string]any
func buildIMSSection()      map[string]any
func buildMCXSection()      map[string]any
func buildChargingSection() map[string]any
func buildServicesSection() map[string]any

// Helpers
func isGBR(fiveQI uint8) bool
func roundPct(v float64) float64
func ipPoolCapacities() map[string]int
func assignableHostCount(cidr string, ver int) int
```

## B.6 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-KPIS-001 `kpis_dashboard_shape`        | top-level keys + amf/smf/upf/ip_pools/fm sub-shapes |
| TC-KPIS-002 `kpis_amf_counters`           | TS 28.552 §5.1 RM.* / AUTH.* / NGAP.* surfaces in amf.* |
| TC-KPIS-003 `kpis_smf_gbr_split`          | gbr + nongbr == total_bearers; pdu_per_dnn/_slice maps |
| TC-KPIS-004 `kpis_upf_loss_rate`          | upf.* keys present; packet_loss_rate in [0, 100] or -1 |
| TC-KPIS-005 `kpis_ip_pools`               | per-pool {dnn, allocated, total, utilization_pct} |
| TC-KPIS-006 `kpis_raw_counters_rates`     | /api/kpis/raw shape + window_sec honoured |
| TC-KPIS-007 `kpis_reset_peaks`            | reset zeroes peak_rates |
| TC-KPIS-008 `kpis_fm_counts`              | severity sum equals total |

All eight wired into `tc_kpis.py::ALL_KPIS_TCS` and pass against the
current core build.

## B.7 References

- **TS 28.552** — 5G performance measurements (clauses 5.1 / 5.3 / 5.4).
- **TS 28.554** §6 — 5G end-to-end KPI definitions (Mean-of-Ratios).
- **TS 28.532** — Fault Supervision (severity levels + raise/clear life-cycle).
- **TS 23.501** §5.7.4 Table 5.7.4-1 — standardised 5QI → resource type.
- `docs/design/oam/pm.md` — counter producer / TS 28.552 prefix table.
- `docs/design/oam/fm.md` — fault store consumed by `fm.Default.Counts`.

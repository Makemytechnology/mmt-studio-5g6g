# oam/pm — Performance Management Counters

## 1. Role / scope

`oam/pm` is the in-process counter registry every NF bumps from its
hot path. It is the source of truth for:

- the `/metrics` Prometheus endpoint
  ([OBSERVABILITY.md §2](../../OBSERVABILITY.md#2-prometheus-metrics--grafana)),
- KPI panels in the GUI (Mean-of-Ratios success-rate, peak rate,
  windowed rate),
- the per-procedure counter dump scraped by Grafana.

Naming convention follows the TS 28.552 family prefixes:

| Prefix | Family | Source |
|---|---|---|
| `RM.*` | Registration Management | AMF (TS 28.552 clause 5.1.1, deferred) |
| `MM.*` | Mobility Management | AMF (clauses 5.1.5, 5.1.6) |
| `AUTH.*` | 5G-AKA / EAP-AKA' | AMF + AUSF (clause 5.1.2) |
| `SEC.*` | NAS Security Mode | AMF (clause 5.1.3) |
| `NGAP.*` | NGAP procedures | AMF NGAP server |
| `SM.*` | Session Management | SMF (clause 5.3) |
| `UPF.*` | UPF report types | UPF report drainer (TS 29.244 §7.5.8) |
| `NSSF.*` | Slice Selection | NSSF |
| `AF.*` | Application Function | AF (TS 29.514 / 29.517 / 29.522) |
| `UDM.*` | Nudm_* services | UDM (TS 29.503) |
| `PCF.*` | Npcf_SMPolicyControl | PCF (TS 29.512) |

Counter constants are defined inline at `oam/pm/perf_counters.go:241-365`;
each is a verified §-anchored cite where the spec PDF is in
`specs/3gpp/`, and a deferred prose-only cite (`TODO(spec:)`)
otherwise. The package header at `oam/pm/perf_counters.go:1-28`
explains the split — TS 28.552 itself isn't loaded locally so its
clauses are tracked via TODO; the procedure-owning specs (38.413,
23.502, 29.244, 29.503, 29.512, 29.514, 29.517, 29.522) are loaded
and §-grounded.

## 2. Architecture

```
                  +--------------------------+
NF call site ---> | pm.Inc("AUTH.Att", 1)    |  <-- producer hot path
                  +-----------+--------------+
                              | values[name] += delta (mutex)
                              v
                  +--------------------------+
                  | pm.Default *Counters     |
                  |   values  map[string]int |
                  |   history []sample (ring)|
                  |   peaks   map[string]f64 |
                  +-----------+--------------+
                              ^
            sampleLoop()      |
            time.Tick(1s) ----+   snapshot All() into history,
                                  recompute 1 s peak rates
                              |
                              v
            +-------------------------------+   GET /metrics
            | pm.WritePrometheus(io.Writer) |--> sacore_<family>_<name>{family="..."} N
            +-------------------------------+
```

`Counters` is safe for concurrent use under a single mutex; under the
current AMF call profile (~5k events/sec peak observed) the contention
budget is comfortable. The sampler is opt-in (`StartSampler`) — until
it runs, `Rate()` and `PeakRate()` return zero and only raw counters
are exposed.

## 3. File map

| File | LOC | Role |
|---|---:|---|
| `oam/pm/perf_counters.go` | 365 | `Counters`, sampler, rate maths, all §-grounded counter constants |
| `oam/pm/perf_counters_test.go` | 68 | unit tests for Inc / Reset / Rate / SuccessRate |
| `oam/pm/prometheus.go` | 56 | `WritePrometheus` text-exposition encoder |

## 4. Public API / contracts

### Counter ops (mutex-guarded)

| Method | Effect |
|---|---|
| `Inc(name string, delta int64)` | `delta == 0` -> 1. Bumps `values[name]`. Top-level `pm.Inc` forwards to `Default`. |
| `Get(name) int64` | Current value; 0 if unset. |
| `All() map[string]int64` | Snapshot of every counter. |
| `Prefix(prefix) map[string]int64` | Subset whose name starts with `prefix` (e.g. `pm.Default.Prefix("AUTH.")`). |
| `Reset()` | Zeroes every counter; history preserved so rates don't spike. |

### Rate / peaks (require sampler)

| Method | Effect |
|---|---|
| `StartSampler()` | Spawns a goroutine that snapshots `All()` once per second into a 120-entry ring; updates `peaks[name]` against the previous sample. Idempotent. |
| `StopSampler()` | Joins the sampler goroutine. |
| `Rate(name, window) float64` | events/sec over the last `window`. Default window 5 s. |
| `PeakRate(name) float64` | Highest 1-second rate seen since process start. |
| `ResetPeaks()` | Clears the peak table (UI button). |
| `SuccessRate(success, failure) float64` | `succ / (succ+fail) * 100`; returns -1 with zero attempts. The Mean-of-Ratios shape used by TS 28.552 §6.x KPIs. |

### Counter registry — naming

The table at the top of §1 lists the families. Each constant is a
named string used at the call site. Examples:

```go
pm.Inc(pm.RegAtt, 1)            // RM.RegAtt -- AMF Registration Request received
pm.Inc(pm.AuthFailMAC, 1)       // AUTH.FailMAC -- 5G-AKA MAC verification failure
pm.Inc(pm.SMSessFail, 1)        // SM.SessFail -- PDU session establishment failed
pm.Inc(pm.UPFReportDLDR, 1)     // UPF.ReportDLDR -- §7.5.8.2 DL Data Report
pm.Inc(pm.PCFSmPolicyCreate, 1) // PCF.SmPolicyCreate -- 29.512 §4.2.2
```

The full constant set lives at `oam/pm/perf_counters.go:243-365`.
NGAP-PWS counters (`NGAPPWSWriteReplaceReq`, `NGAPPWSCancelResp`, …)
carry inline TS 38.413 §-cites; AF / UDM / PCF cite TS 29.5xx; UPF
report counters cite TS 29.244 §7.5.8.x.

### Prometheus exposition

`(*Counters).WritePrometheus(w io.Writer)` renders every counter as
Prometheus 0.0.4 text:

```
# HELP sacore_auth_att Internal 3GPP counter AUTH.Att
# TYPE sacore_auth_att counter
sacore_auth_att{family="AUTH"} 17
```

Name mapping (`oam/pm/prometheus.go:43-55`): `"AUTH.SuccBase"` ->
metric `"sacore_auth_succbase"`, label `family="AUTH"`. Dots /
hyphens / other punctuation become underscores. Output is sorted so
two scrapes diff cleanly.

The webservice `/metrics` route writes
`Content-Type: text/plain; version=0.0.4` and calls
`pm.Default.WritePrometheus(w)`.

## 5. Headline flows / lifecycle

**Boot.** `pm.Default = New()` is package-init. Every NF that wants
windowed rates calls `pm.Default.StartSampler()` once during startup.
Tests construct their own `Counters` via `pm.New()` to keep state
isolated.

**Hot path.** Each NF call site does one `pm.Inc(name, 1)`. The map
write is mutex-guarded; under sustained 5k+ events/sec it has not
shown up as a hot spot in profiles. If contention becomes visible the
forward path is sharded counters per family, but defer until benchmarks
warrant.

**Once per second** (when sampler is running):

1. Snapshot `values` map into `history[]`.
2. Trim ring to 120 entries (`historySeconds`).
3. For each counter, compute the 1 s delta against the previous
   sample; bump `peaks[name]` if higher.

`Rate(name, window)` walks the ring backwards from "now" until it
finds the first sample within `window`, and divides delta-by-elapsed.
With a 120-entry ring at 1 s cadence, queries up to 2 minutes always
have data.

**Mean-of-Ratios success rates.** `SuccessRate("RM.RegSucc",
"RM.RegFail")` -> percent. The Reg / Auth / SM / NSSF families all
ship matched succ + fail pairs so KPI panels can compose:

| KPI | success | failure |
|---|---|---|
| Reg success rate | `RM.RegSucc` | `RM.RegFail` |
| Auth success rate | `AUTH.Succ` | `AUTH.Fail` |
| Sec success rate | `SEC.Succ` | `SEC.Fail` |
| PDU session success rate | `SM.SessSucc` | `SM.SessFail` |
| Slice selection success rate | `NSSF.SelSucc` | `NSSF.SelFail` |

## 6. Stubs / TODOs

`grep -n TODO oam/pm/perf_counters.go`:

- `perf_counters.go:5` — TS 28.552 ("5G performance measurements")
  prose-only because the PDF isn't in `specs/3gpp/`. Family naming
  follows the spec so the catalogue is regroundable when added.
- `perf_counters.go:16` — TS 28.554 ("5G end-to-end KPIs"). The
  `SuccessRate()` call is the §6.x Mean-of-Ratios shape but the full
  KPI catalogue isn't emitted yet.

No code TODOs in the body; every constant either carries an inline
§-cite (e.g. `NGAPPWSWriteReplaceReq // TS 38.413 §8.9.1`) or rolls up
under the deferred TS 28.552 banner.

## 7. References

§-grounded inline at the constant blocks (PDFs present in `specs/3gpp/`):

- TS 38.413 — NGAP. PWS counters cite §8.9.1 / §8.9.2 / §8.9.3 / §8.9.4.
- TS 23.502 — Procedures. SM-DLNotify / SM-N1N2 cite §4.2.3.3.
- TS 29.244 — PFCP. UPF.Report* cite §7.5.8.2 through §7.5.8.6.
- TS 29.503 — UDM. UDMUeAuthGet cites §5.4.2.2; SDM cites §5.2.2.2.x;
  UECM cites §5.3.2.x.
- TS 29.512 — PCF. SMPolicy* cite §4.2.2 / §4.2.3 / §4.2.4 / §4.2.5.
- TS 29.514 — PolicyAuthorization. AF Session cites §4.2.2 / §4.2.3 /
  §4.2.4 / §4.2.5.
- TS 29.517 — EventExposure. AF Event cites §4.2.
- TS 29.522 — NEF northbound. TrafficInfluence cites §4.4.7.

Deferred (PDFs not local, prose `TODO(spec:)` only):

- TS 28.552 — 5G performance measurements.
- TS 28.554 — 5G end-to-end KPIs.

Cross-doc:
- [OBSERVABILITY.md §2](../../OBSERVABILITY.md#2-prometheus-metrics--grafana)
  — operator-facing scrape config, alert rules, family table.

---
*Last refreshed against commit `13a181d`.*

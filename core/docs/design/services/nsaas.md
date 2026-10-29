# nsaas — Network Slice as a Service

## 1. Role / scope

`services/nsaas/` is the Network-Slice-as-a-Service tenancy layer:
tenants subscribe to standardised slice templates, instantiate
slices (with config overrides), and have SLA targets evaluated
against measured KPIs. It implements TS 28.531 §8 lifecycle states
and TS 28.541 §6.3.1 SLA targets, on top of four SQLite tables —
`nsaas_tenants`, `nsaas_templates`, `nsaas_slices`, `nsaas_sla` —
plus a row in `nssai_catalog` referenced by `nsaas_slices.nssai_catalog_id`.

The slice lifecycle is the canonical one from **TS 28.531 §8.1**,
encoded as a transition table at `nsaas.go:24-31`:

```
preparing ─▶ provisioned ─▶ active ─▶ decommissioning ─▶ decommissioned
                              │   ▲
                              ▼   │
                          modifying
```

Any other transition is rejected by `changeStateExtra` with
`"invalid transition: X -> Y"`.

## 2. File map

| File | LOC | Role |
|------|-----|------|
| `nsaas.go` | 454 | Tenants / templates / slices / SLA / standard templates |

## 3. Wire / API surface

No external wire (SBI is not modelled here). The Go API the panel
and orchestrator drive:

| Function | Spec § | Notes |
|----------|--------|-------|
| Tenant CRUD | — | `ListTenants` / `CreateTenant` / `DeleteTenant` |
| Template CRUD | — | `ListTemplates` / `CreateTemplate` (SST + optional SD + DNN + qos_profile + sla_defaults JSON) |
| `ProvisionSlice(tplID, tenantID, cfg)` | §8.2 | Inserts in `preparing`, transitions to `provisioned` |
| `ActivateSlice(id)` | §8.1 | `provisioned → active`, stamps `activated_at` |
| `ModifySlice(id, changes)` | §8.1 | `active → modifying`, applies field updates, returns to `active` |
| `DecommissionSlice(id)` | §8.1 | Optional `modifying → active` reset, then `→ decommissioning → decommissioned`, stamps `decommissioned_at` |
| `DefineSLA(sliceID, metrics)` | §8.3 / TS 28.541 §6.3.1 | Upsert per (slice, metric) |
| `CheckSLA(sliceID)` | §8.3 | Returns `compliant` / `violation` summary |
| `UpdateSLAMetric(sliceID, metric, value)` | §8.3 | Recomputes compliance — `latency_ms`, `max_ues` use lower-is-better; everything else higher-is-better (`nsaas.go:380-385`) |
| `SeedStandardTemplates()` | TS 23.501 §5.15.2 | Creates eMBB / URLLC / mMTC / Enterprise / V2X if absent |

### Standard templates (`nsaas.go:410-416`)

| Name | SST | SD | DNN | Notes |
|------|-----|-----|-----|-------|
| eMBB | 1 | 000001 | internet | High throughput, moderate latency |
| URLLC | 2 | 000002 | internet | Critical comms |
| mMTC | 3 | 000003 | iot | Massive IoT |
| Enterprise | 1 | 000010 | enterprise | eMBB with isolation |
| V2X | 4 | 000004 | v2x | TS 23.287 |

## 4. Headline procedures

**Provision slice** (`ProvisionSlice`, `nsaas.go:190-226`). Validates
tenant + template existence, picks SST/SD from template (overridable
by config), serialises config to JSON, inserts row in `preparing`,
then immediately transitions to `provisioned`. Returns the new
slice ID even on the post-insert state-change error so the caller
can clean up.

**Modify slice.** `ModifySlice` enters `modifying` (rejected if not
in `active`), applies any of `name / sst / sd / config_json` updates
via raw `UPDATE`, then returns to `active`.

**Decommission.** `DecommissionSlice` accepts a slice in `active`
(re-arms from `modifying` first), then `→ decommissioning →
decommissioned`. The `decommissioned_at` timestamp is stamped on the
final transition.

**SLA checks** (`CheckSLA`, `nsaas.go:342-366`). Walks all
`nsaas_sla` rows for the slice, counts `compliant=0` rows; if any
violations exist returns `status=violation`, otherwise `compliant`
(or `no_sla` if no metrics defined).

## 5. Key types

```go
type Tenant   { ID, Name, ContactEmail, APIKey, CreatedAt }
type Template { ID, Name, Description, SST, SD, DefaultDNN,
                QoSProfile, SLADefaults, CreatedAt }
type Slice    { ID, TenantID, TemplateID, Name, SST, SD, Status,
                ConfigJSON, NSSAICatalogID, CreatedAt,
                ActivatedAt, DecommissionedAt }
type SLAMetric { ID, SliceID, Metric, TargetValue, CurrentValue,
                 Compliant, CheckedAt }
```

The transition table itself:

```go
var transitions = map[string]map[string]bool{
    "preparing":       {"provisioned": true},
    "provisioned":     {"active": true, "decommissioning": true},
    "active":          {"modifying": true, "decommissioning": true},
    "modifying":       {"active": true, "decommissioning": true},
    "decommissioning": {"decommissioned": true},
    "decommissioned":  {},
}
```

## 6. Stubs / TODOs

No `TODO(spec: ...)` markers in source. Implicit gaps:

- No NRM (Network Resource Model, TS 28.541) export — slices live in
  the local DB only.
- No SBI / CSMF API exposing the slice catalogue externally.
- SLA evaluator is per-metric; no service-level chained KPI
  evaluation.
- `nssai_catalog_id` is a foreign key but the row is not auto-
  populated by `ProvisionSlice`.

## 7. References

- **TS 28.531** §8.1 (lifecycle), §8.2 (provisioning), §8.3 (SLA)
- **TS 28.541** §6.3.1 (SLA metrics)
- **TS 23.501** §5.15.2 (standardised SST values, used in
  `SeedStandardTemplates`)
- **TS 23.287** — referenced from V2X standard template

---

*Last refreshed against commit `13a181d`.*

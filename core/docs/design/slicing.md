# Network Slicing — Design Document

Umbrella design for the slicing surface in mmt-studio-core. Network
slicing is not a single network function — it is a cross-cutting
concept that touches the SBI catalogue, the AMF (selection), the SMF
(UPF anchoring), the UPF (per-slice forwarding/QoS), the OAM/operator
plane (NSaaS, admission, NPN), and the data model that ties it all
together. This doc explains **what each piece does, why it exists,
and how the pieces compose**. Per-NF implementation depth lives in:

- [nf/nssf.md](nf/nssf.md) — Network Slice Selection Function
- [nf/nsacf.md](nf/nsacf.md) — Network Slice Admission Control Function
- [services/nsaas.md](services/nsaas.md) — Network-Slice-as-a-Service
- [security/npn.md](security/npn.md) — Non-Public Networks (SNPN, PNI-NPN, CAG)

---

## 1. What is a network slice?

A **network slice** is an end-to-end logical network instantiated on
top of shared infrastructure that meets a specific service profile
(throughput, latency, reliability, isolation). 3GPP defines it in
**TS 23.501 §5.15**.

Each slice is identified by an **S-NSSAI** (Single Network Slice
Selection Assistance Information):

```
S-NSSAI = SST (8-bit Slice/Service Type)  +  SD (24-bit Slice Differentiator, optional)
```

- **SST** — what *kind* of slice it is. Standardised values come from
  TS 23.501 §5.15.2.2 Table 5.15.2.2-1:
  | SST | Service profile |
  |----:|-----------------|
  | 1   | eMBB — enhanced Mobile Broadband |
  | 2   | URLLC — Ultra-Reliable Low-Latency |
  | 3   | MIoT — massive IoT |
  | 4   | V2X — Vehicle-to-Everything |
  | 5   | HMTC — High-Performance Machine-Type Comms |
  | 6   | HDLLC — High-perf Distributed LLC |
  | 7-127 | operator-specific within the standardised range |
  | 128-255 | operator-specific outside the standardised range |
- **SD** — disambiguates instances of the same SST (e.g., two
  enterprises both want eMBB but with different policy). 6 hex digits
  on the wire (TS 23.003 §28.4.2). Values `0x000000` and `0xFFFFFF`
  wildcard per TS 24.501 §9.11.2.8.

A UE can be **subscribed** to up to 8 S-NSSAIs (TS 23.501 §5.15.4),
**request** a subset on Initial Registration, and ultimately be
**Allowed** a (possibly smaller) subset for use in the current
Registration Area.

### The slicing pipeline (per UE)

```
Subscribed NSSAI         (UDM, per-IMSI; → ue_subscribed_nssai)
       │
       ▼
Requested NSSAI          (UE NAS — what the UE wants right now)
       │
       │   ┌─ AMF PLMN support  ──┐
       ▼   ▼                     │
   NSSF.SelectAllowedNSSAI ◀─────┤  gNB SupportedTAList
       │                         │  TA-NSSAI policy
       ▼                         │
   Allowed NSSAI                 │
       │                         │
       ▼                         │
   NSACF.RequestAdmission        │  per-slice quota (max UEs / max sessions)
       │  (allowed | rejected)   │
       ▼                         │
   PDU Session Establishment     │
       │                         │
       ▼                         │
   SMF.UPFSelect(SST,SD,DNN) ────┘  joins upf_supported_nssai × nssai_catalog
       │
       ▼
   PFCP Session ↦ UPF anchor (per-slice GTP-U + URR/QER)
```

Each arrow is enforced — Subscribed gates Requested, AMF/gNB support
gates Allowed, NSACF gates admission, the UPF join gates PDU session
establishment.

## 2. Slice catalogue — single source of truth

`nssai_catalog` (`db/schemas/core.go:16`) is the **canonical slice
catalogue**. Every other slice consumer is a foreign key into it:

| Table | Column | Cascade | Purpose |
|-------|--------|---------|---------|
| `plmn_nssai` | `nssai_id` | ON DELETE CASCADE | which slices a PLMN advertises |
| `upf_supported_nssai` | `nssai_id` | ON DELETE CASCADE | which slices a UPF anchors |
| `ue_subscribed_nssai` | `nssai_id` | ON DELETE CASCADE | per-UE subscribed S-NSSAIs (TS 23.501 §5.15.4) |
| `nsaas_slices` | `nssai_catalog_id` | (RESTRICT via template) | NSaaS-instantiated slice → catalogue row |

This means: adding/renaming/deleting a slice in the GUI catalog
propagates to every PLMN, UPF, subscriber, and NSaaS-instantiated
slice that referenced it. The CSV columns that pre-date the FK
(`upf_instances.supported_sst`, etc.) are kept for back-compat and
back-filled at boot — the FK join is the runtime truth.

The GUI exposes the catalogue at **Network Config → Network Slicing →
Slice Catalog**. Adds use a TS 23.501 §5.15.2.2 standardised SST
dropdown (auto-fills the slice short name) plus a Custom escape
hatch for operator-specific values.

## 3. Selection — how the AMF picks the Allowed NSSAI

The NSSF (`nf/nssf/selection.go`) computes the Allowed NSSAI for one
UE registration. Functional steps (full detail in
[nf/nssf.md](nf/nssf.md)):

1. Load Subscribed NSSAI from `ue_subscribed_nssai` (split into
   *all* and *defaults* — `is_default=1` rows).
2. Pick a candidate set: the UE-Requested NSSAI if present, else the
   *default* Subscribed set (TS 23.501 §5.15.5.2.1 fallback).
3. Intersect candidates against the AMF PLMN-supported set, the gNB
   SupportedTAList, and the per-TA NSSAI policy
   (`crud.TANssaiPolicyAllows` reads `ta_nssai_policy(tac, sst, sd,
   allowed)`; missing row = default-allow). Failures land in
   `Rejected` with TS 24.501 §9.11.3.46 cause codes
   (`NotInPLMN` / `NotInRegistrationArea` / `NSSAAFailedOrRevoked`).
4. If everything got rejected but the UE asked for something, fall
   back to default Subscribed slices that pass AMF + gNB filters.
5. Cap Allowed/Rejected to 8 entries each (TS 23.501 §5.15.4 +
   TS 24.501 §9.11.3.46 NOTE 0).

Result: `SelectionResult{Allowed, Rejected, Subscribed}` carried in
the Registration Accept N1 message.

## 4. Anchoring — how the SMF picks the UPF for a slice

For each PDU Session Establishment the SMF picks the UPF that
**anchors** the requested S-NSSAI + DNN (TS 23.501 §6.3.3). In this
build:

- `upf_supported_nssai(upf_id, nssai_id)` is the join table that
  declares which slices each UPF can serve.
- `nf/smf/upf/registry.go::Select` joins
  `upf_supported_nssai × nssai_catalog` filtered by `(SST, SD, DNN)`
  and returns the matching UPF row. A CSV-based fallback path remains
  for legacy data.
- The SMF runs **multi-anchor PFCP**: every registered UPF gets its
  own PFCP listener. A `RouterBridge` dispatches each session to the
  selected anchor by `(IMSI, PDU-Session-ID) → upfID`, so different
  slices on the same UE can land on different UPFs. The SNSSAI IE
  (TS 29.244 §8.2.144, type 231) is included on the wire for
  observability.

## 5. Slice as a Service (NSaaS)

### 5.1 What it is

**Network Slice as a Service** is the *tenancy* layer — a way for
external customers (an enterprise, an MVNO, a vertical) to consume
slices from the operator without touching the underlying NFs
themselves. The operator publishes **slice templates**; a tenant
**provisions** a slice from a template (with overrides); the
operator-side machinery walks it through the **3GPP TS 28.531 §8.1
lifecycle** and evaluates **TS 28.541 §6.3.1 SLA targets** against
measured KPIs.

### 5.2 Functional model

Four concepts:

| Concept | Role | Key spec |
|---------|------|----------|
| **Tenant** | A customer with API key + contact email — owner of slices | (operator-defined) |
| **Template** | A reusable slice profile: SST, SD, DNN, QoS profile, default SLA targets | TS 23.501 §5.15.2 standardised SSTs |
| **Slice** | A live instance derived from a template, bound to one tenant | TS 28.531 §8 |
| **SLA metric** | A `(metric, target_value, current_value, compliant)` row | TS 28.541 §6.3.1 |

### 5.3 Lifecycle (TS 28.531 §8.1)

```
preparing ─▶ provisioned ─▶ active ─▶ decommissioning ─▶ decommissioned
                              │   ▲
                              ▼   │
                          modifying
```

Any other transition is rejected. Stamps: `activated_at` on
`→ active`, `decommissioned_at` on `→ decommissioned`.

### 5.4 Standard templates (seeded if absent)

eMBB · URLLC · mMTC · Enterprise (eMBB with isolation) · V2X (TS 23.287).
See [services/nsaas.md §3](services/nsaas.md) for the table.

### 5.5 SLA evaluation

`CheckSLA(sliceID)` walks `nsaas_sla` rows and rolls up to one of
`compliant` / `violation` / `no_sla`. Direction depends on the
metric: `latency_ms` and `max_ues` are lower-is-better, everything
else is higher-is-better. SLA *enforcement* (driving PFCP QER caps,
rerouting traffic, alarming) is out of scope here — NSaaS is the
**book of record**; the actuator surface is the SMF/UPF.

### 5.6 What NSaaS is **not**

- It is **not** the runtime selection path — that is NSSF/SMF.
- It is **not** the admission gate — that is NSACF.
- It does **not** export TS 28.541 NRM XML/JSON. Slices live in the
  local DB; no external CSMF SBI is wired.

## 6. Admission Control (NSACF)

### 6.1 What it is

Slice selection (NSSF) decides *which slice the UE is allowed to use*.
Slice **admission** (NSACF, TS 23.501 §5.15.11) is the next gate: it
decides *whether one more UE / one more PDU session can fit on a
slice right now*, given the slice's quota.

Without admission control a slice with a 100-UE SLA can silently
oversubscribe; with admission control the AMF/SMF gets a clear
allow/reject (and can include the UE in the Rejected NSSAI with a
spec cause).

### 6.2 Functional model

| Object | Fields | Purpose |
|--------|--------|---------|
| `slice_limits[(sst, sd)]` | `max_ues`, `reserved_ues`, `priority_threshold`, `preemption_enabled` | The quota record per slice |
| `admissions[(sst, sd)]` | set of admitted IMSIs | Who's currently consuming the quota |
| `ue_slice_mbr[(imsi, sst, sd)]` | `dl_kbps`, `ul_kbps`, current usage | Per-(UE,slice) MBR (TS 23.501 §5.15.12) |

### 6.3 Decision paths

**Base path** — `RequestAdmission(imsi, sst, sd)`:

1. Default empty `sd → "000000"`.
2. Already admitted → `{allowed:true, reason:"already_admitted"}`.
3. No `slice_limits` row → auto-admit (uncapped slice).
4. `len(admissions) < max_ues` → admit + persist (`capacity_available`).
5. Otherwise reject (`slice_full`).

**Priority path** — `EvaluateAdmission(imsi, sst, sd)`: reads UE
priority, compares against `priority_threshold` and the reserved-slot
count. On `preemption_enabled=true` and a full slice, picks the
lowest-priority current admission via `GetLowestPriorityAdmission`,
evicts via `PreemptUE`, then admits the new UE.

**MBR enforcement** — `EnforceSliceMBR(imsi, sst, sd, dlKbps, ulKbps)`:
returns a `throttle` decision plus the offending direction
(`dl|ul|both`). The decision today is advisory — there is no wiring
to PFCP QER caps.

### 6.4 What NSACF is **not**

- It is **not** the per-PDU-session quota gate (TS 23.501 §5.15.11
  also limits *concurrent PDU sessions* per slice). Only the UE
  count is tracked here.
- It does **not** expose Nnsacf SBI / NSACEventExposure — calls are
  intra-process Go only.
- The MBR enforcer is a **decision** function; an actuator (SMF →
  PFCP QER) is not yet wired.

## 7. Non-Public Networks (NPN)

### 7.1 What it is

A **Non-Public Network** is a 5G deployment that serves a private set
of users — a factory, a campus, a stadium, a railway corridor.
TS 23.501 §5.30 defines two flavours:

| Flavour | Identity | Deployment |
|---------|----------|------------|
| **SNPN** (Standalone NPN) | `(PLMN-ID, NID)` | An entirely separate 5GC. The UE's only access is through the SNPN. |
| **PNI-NPN** (Public-Network-Integrated NPN) | `CAG-ID` (8 hex digits, TS 23.501 §5.30.2) | A *slice* of a public PLMN restricted to a Closed Access Group. The UE roams on the public network but is gated for NPN access. |

The default in this build is **PNI-NPN** (`security/npn/npn.go:26`).

### 7.2 Why NPN sits next to slicing

NPN and slicing are orthogonal in the spec but **operationally
coupled**:

- A PNI-NPN is implemented as a slice (S-NSSAI) with a CAG-restricted
  Allowed-NSSAI policy. The slice catalogue and the NPN/CAG records
  must agree.
- An SNPN is a separate 5GC instance — its own slice catalogue, its
  own subscribers, its own admission policy.

### 7.3 Functional model

| Object | Purpose | Spec |
|--------|---------|------|
| `npn_networks` | NPN instance — type (SNPN/PNI-NPN), PLMN, NID | §5.30 |
| `npn_cag` | Closed Access Group — `cag_id` (8 hex), name | §5.30.2 |
| `npn_cag_members` | `(cag_id, imsi)` — who belongs to a CAG | §5.30.2 |
| `npn_access_log` | (DDL only — INSERT site not wired) | OAM |

### 7.4 Admission verb

`AuthenticateSNPN(imsi, cagID, npnNID)` is what the AMF calls on
Initial Registration. Outcomes:

```go
{"allowed": true,  "cag_id": "ABCD1234", "nid": "..."}
{"allowed": false, "reason": "invalid CAG-ID format"}
{"allowed": false, "reason": "IMSI not in CAG"}
```

Implementation is two checks: `ValidateCAGID(cagID)` (regex on the 8
hex digits) and `CheckMembership(cagID, imsi)` (one JOIN on
`npn_cag_members ⨝ npn_cag`). Every call also writes a row to
`npn_access_log` via `logAccess` (best-effort FK lookup of `npn_id`
by NID and `cag_id` by hex CAG-ID — both NULL on miss). The TS 33.501
§6.1.4 primary authentication anchor is cited but Credentials Holder
authentication (TS 23.501 §5.30.2.10) is not implemented.

### 7.5 What NPN is **not** (today)

- No NID-format validation — the NID is accepted as opaque string.
- No AAA-S / Credentials Holder / DCS interaction.
- No SBI envelope — calls are intra-process Go and exposed over
  `POST /api/npn/authenticate` for tests / GUI integration only.

## 8. How the four pieces compose

```
                    ┌─ nssai_catalog ─┐  ◀── one source of truth
                    │  (SST, SD, name) │
                    └────┬─────────────┘
                         │ FK
        ┌────────────────┼─────────────────┬──────────────────┐
        ▼                ▼                 ▼                  ▼
   plmn_nssai      upf_supported_nssai  ue_subscribed_nssai  nsaas_slices
   (PLMN advert)   (UPF anchors)        (per-IMSI)           (NSaaS instances)
        │                │                 │                  │
        ▼                ▼                 ▼                  ▼
      AMF             SMF.UPFSelect      NSSF                NSaaS
   PLMN support      (TS 23.501 §6.3.3)  (Allowed NSSAI)     lifecycle + SLA
                                         │
                                         ▼
                                       NSACF
                                  (admit / reject /
                                   priority preempt)
                                         │
                                         ▼
                                   PDU session
                                         │
                                         ▼  (PNI-NPN only)
                                       NPN
                                  (CAG membership
                                   gate at registration)
```

Reading the chain bottom-up:

- A UE on an **NPN** must pass the CAG check before it even gets an
  Allowed NSSAI.
- The Allowed NSSAI is computed by **NSSF** from
  Subscribed ∩ Requested ∩ AMF ∩ gNB ∩ TA-policy.
- For each Allowed slice, **NSACF** decides whether one more UE fits.
- For each PDU session, **SMF** picks an anchor UPF from
  `upf_supported_nssai`.
- **NSaaS** instantiates and tracks the slice as a tenant-owned
  product, evaluates SLAs, but does not itself make admission or
  selection decisions.

## 9. GUI surface

`webservice/templates/slices.html` renders **Network Config → Network
Slicing**:

- **Slice Catalog** — CRUD on `nssai_catalog` with the TS 23.501
  §5.15.2.2 SST dropdown. Deletes cascade into PLMN/UPF/subscriber
  joins.
- **Slice ↔ UPF anchor mapping** — read-only join of
  `/api/plmn/supported` × `/api/admin/upf-instances` showing each
  configured S-NSSAI and which UPFs anchor it.
- **Slice overview cards** — per-SST live counters (sessions, UE
  count, anchored-on UPF list, DL/UL bytes/pkts, dropped, metered)
  rolled up by `/api/upf/slice-stats` from URR + QER stats.
- **UE Distribution per Slice** — accordion listing live PDU sessions
  grouped by SST.

Operator-side surfaces for NSaaS, NSACF, and NPN have separate
config pages (referenced in their per-component docs).

## 10. Status of cross-cutting gaps

### 10.1 Closed

| Gap | Status | Where wired |
|-----|--------|-------------|
| **Per-TA NSSAI policy** (TS 23.501 §5.15.3) | ✅ wired | `db/crud/ta_policy.go::TANssaiPolicyAllows`; called from `nf/nssf/selection.go::taPolicyAllows`. Default-allow on missing rows. GUI route `POST /api/tac/tracking-areas/{tac}/nssai` was already there; the read side is what landed. |
| **NPN access logging** | ✅ wired | `security/npn/npn.go::logAccess` writes one row per `AuthenticateSNPN` call into `npn_access_log` (TS 23.501 §5.30 OAM/audit). Best-effort FK lookup of `npn_id`/`cag_id`. Reader exposed at `GET /api/npn/access-log?imsi=&limit=`. |
| **NSaaS → catalog FK auto-population** | ✅ wired | `services/nsaas/nsaas.go::ProvisionSlice` calls `crud.NSSAICatalogGetOrCreate`, then writes the id into `nsaas_slices.nssai_catalog_id` so every NSaaS-instantiated slice shares identity space with PLMN advertisements / UPF anchors / UE subscriptions. |
| **NPN routes wired to backend** | ✅ wired | `webservice/app/routes_nsaas.go` /api/npn/* now call `security/npn` (was dummy stubs returning `{ok:true}`). New `POST /api/npn/authenticate` exposes `AuthenticateSNPN`. |
| **NPN schema/code alignment** | ✅ fixed | `security/npn/npn.go` SQL renamed `npn_cags`→`npn_cag`, `cag_row_id`→`cag_id`, `plmn_id`→`plmn` to match the DDL. CAG CRUD round-trip was failing on every fresh DB before. |
| **NGAP SCTP transport DB-driven** | ✅ wired | `network_config.sctp_port` (default 38412 per TS 38.412 §7 IANA) read at startup; `ensureColumn` migration for old DBs; iptables / ufw / firewalld rules + startup banner all derive from the DB value. `amf_ip` seed flipped from a stale LAN literal to empty so auto-pick runs cleanly. `pickPrimaryIPv4` no longer over-excludes user-defined Docker bridges (was 172.16/12, now narrowed to docker0 = 172.17/16). |
| **SCTP multi-homing tester resilience** | ✅ fixed | `mmt_studio_core_tester/src/protocol/sctp.py::_restrict_to_primary_path` fail-softs on the unreachable 4-arg `getsockopt` readback so the actual `setsockopt(SPP_HB_DISABLE)` still runs. |

### 10.2 Still open

| Gap | Notes |
|-----|-------|
| **NSSAA** (TS 23.501 §5.15.10) | Slices subject to NSSAA should enter a `pending` state and only become Allowed after a successful AAA exchange. Today they're admitted unconditionally (`nf/nssf/selection.go:241-247`). |
| **PDU-session quota** in NSACF | Only the UE count is tracked. TS 23.501 §5.15.11 also limits concurrent PDU sessions per slice; needs a `max_pdu_sessions` column + per-PDU bookkeeping + SMF gate. |
| **MBR actuator** | `EnforceSliceMBR` returns a `{throttle, direction}` decision; nothing wires it to a PFCP QER cap. SMF QER builder is the actuator side that needs to consume the decision. |
| **Nnsacf / Nnssf SBI envelopes** | No HTTP/2 + JSON router; calls are intra-process Go. Multi-NF deployments would need REST routes per TS 29.531 / TS 29.533. |
| **TS 28.541 NRM export** | NSaaS slices live in the local DB only; no external CSMF surface. |
| **Credentials Holder / AAA-S** for SNPN (TS 23.501 §5.30.2.10) | Completely absent. SNPN admission today is just CAG membership + a TS 33.501 §6.1.4 cite. |
| **NID-format validation** | NID is accepted as opaque string. |

Per-component docs ([nssf.md](nf/nssf.md), [nsacf.md](nf/nsacf.md),
[nsaas.md](services/nsaas.md), [npn.md](security/npn.md)) carry the
authoritative per-NF TODO list with `file:line` refs.

## 11. References

3GPP citations grounded in source:

- **TS 23.501** §5.15 (Network Slicing — umbrella), §5.15.2 (S-NSSAI),
  §5.15.2.2 (standardised SST), §5.15.3 (NSSAI configuration),
  §5.15.4 (Subscribed NSSAI), §5.15.5.2.1 (default-fallback),
  §5.15.10 (NSSAA), §5.15.11 (NSACF), §5.15.12 (UE Slice MBR),
  §5.30 (NPN umbrella), §5.30.2 (CAG-ID, Credentials Holder),
  §6.3.3 (UPF selection per S-NSSAI/DNN)
- **TS 23.502** §4.2.2.2.2 step 4a (Initial AMF gate)
- **TS 23.003** §28.4.2 (SD wire encoding)
- **TS 24.501** §9.11.2.8 (S-NSSAI IE, SD wildcard),
  §9.11.3.46 + NOTE 0 (Rejected-NSSAI cause codes & cap)
- **TS 28.531** §8.1 (slice lifecycle), §8.2 (provisioning),
  §8.3 (SLA reporting)
- **TS 28.541** §6.3.1 (SLA metrics)
- **TS 29.244** §8.2.144 (SNSSAI IE on PFCP wire)
- **TS 29.531** Nnssf_NSSelection
- **TS 33.501** §6.1.4 (primary authentication, NSSAA, SNPN anchor)
- **TS 23.287** (V2X, referenced by V2X NSaaS template)

Internal:

- `db/schemas/core.go:16-134` — `nssai_catalog`, `ue_subscribed_nssai`
- `db/schemas/network.go` — `network_config.amf_ip`, `network_config.sctp_port`
- `db/schemas/plmn.go:28-37` — `plmn_nssai` with FK to catalogue
- `db/schemas/smf.go:37-49` — `upf_supported_nssai` join table
- `db/schemas/tactables.go:36-42` — `ta_nssai_policy` per-TA gate
- `db/schemas/domains.go:1298-1356` — NPN tables
- `db/schemas/domains.go:1578-1632` — NSaaS tables
- `db/crud/nssai.go::NSSAICatalogGetOrCreate` — public Get-or-Create wrapper
- `db/crud/ta_policy.go::TANssaiPolicyAllows` — per-TA NSSAI policy lookup
- `nf/nssf/selection.go` — Allowed-NSSAI selection
- `nf/nsacf/nsacf.go` — admission + UE Slice MBR
- `nf/smf/upf/registry.go` — slice-aware UPF anchor selection
- `nf/amf/amf.go` + `nf/amf/ngap/transport_linux.go` — DB-driven NGAP bind
- `services/nsaas/nsaas.go` — tenancy + lifecycle + SLA + catalog FK
- `security/npn/npn.go` — NPN/CAG/membership + SNPN admission + access log
- `webservice/app/routes_nsaas.go` — `/api/npn/*` + NSaaS routes
- `webservice/app/routes_tac.go` — `/api/tac/tracking-areas/*/nssai` (TAC casing)
- `webservice/app/network_config.go` — operator-tunable `sctp_port`
- `webservice/templates/slices.html` — operator GUI

## 12. Test coverage

Comprehensive Python integration tests in `mmt_studio_core_tester` exercise
every closed gap above end-to-end against the live core
(`POST /api/tests/{name}/run` on the tester webservice).

| Suite | File | TCs | What it covers |
|-------|------|-----|----------------|
| Slice catalog | `src/testcases/core/tc_slice_catalog.py` | TC-CATALOG-001..005 | CRUD round-trip, TS 23.501 §5.15.2.2 standardised SST seed, NSaaS-FK auto-population, idempotent Get-or-Create, GUI envelope contract |
| Per-TA NSSAI policy | `src/testcases/core/tc_ta_nssai.py` | TC-TANSSAI-001..005 | default-allow, explicit deny persists, INSERT OR REPLACE override, FK cascade on TAC delete, per-TAC isolation |
| NSaaS lifecycle | `src/testcases/vas/tc_nsaas.py` | TC-NSAAS-001..010 | template seed, tenant CRUD, provision (asserts `nssai_catalog_id` populated), activate, decommission, SLA |
| NPN | `src/testcases/safety/tc_npn.py` | TC-NPN-001..014 | SNPN/PNI-NPN CRUD, full CAG round-trip (proves the npn_cag rename), admit, deny, access-log per call with FK columns, cascade delete |
| Traffic / QoS | `src/testcases/core/tc_pdu_session.py` | TC-TRF-006* | UDP bidirectional throughput — exercises the SCTP single-homing path that the AMF NGAP bind hardening unlocked |

Go unit tests:

- `db/crud/ta_policy_test.go` — TANssaiPolicyAllows default-allow + deny + allow override
- `security/npn/npn_test.go` — TestCAGRoundTrip_AdmitAndDeny + TestAuthenticateSNPN_WritesAccessLog

Run all 27 slicing-related Python tests in one shot:
```bash
for t in catalog_crud_round_trip catalog_standardised_ssts \
         catalog_populated_by_nsaas catalog_idempotent_upsert \
         catalog_endpoint_envelope ta_policy_default_allow \
         ta_policy_explicit_deny ta_policy_allow_override \
         ta_policy_cascade_on_tac_delete ta_policy_independent_tacs \
         nsaas_seed_templates nsaas_create_tenant nsaas_provision_slice \
         nsaas_activate_slice nsaas_decommission \
         nsaas_provision_populates_catalog_fk npn_create_snpn \
         npn_create_pni_npn npn_cag_management npn_authorize \
         npn_deny_unauthorized npn_access_log npn_cag_full_crud \
         npn_authenticate_admits npn_authenticate_denies \
         npn_access_log_persisted npn_cascade_delete; do
  curl -sX POST "http://localhost:5001/api/tests/$t/run" \
       -H 'Content-Type: application/json' -d '{}' >/dev/null
done
```

---

*Last refreshed against commit `447bcee`.*

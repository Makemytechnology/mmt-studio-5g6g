# Wi-Fi Offload — Design Document

Operator-facing surface for **non-3GPP (WLAN) access** into the 5GC.
Per-DNN access policy (trusted / untrusted / wireline), the in-flight
attached-UE table, the admission probe, and the audit log.

Complementary to `nf/n3iwf/` — the **N3IWF NF design doc** is
[`../nf/n3iwf.md`](../nf/n3iwf.md), which carries the IKEv2 + EAP-5G
+ ESP datapath. This package is the operator policy / admission /
audit half; together they describe the WLAN-into-5GC story.

---

## Part A — Functional view

### A.1 What Wi-Fi offload is, in plain terms

A subscriber's phone is on Wi-Fi at home, in a café, or on a train.
The operator wants the same SIM-bound services — voice, messaging,
data — to keep working over that Wi-Fi link, with the same
identity, the same charging, the same lawful-intercept handling, the
same DNN/slice mapping. **Wi-Fi offload** is the operator-side
control plane that decides:

- *which* Wi-Fi paths are allowed (untrusted via N3IWF, trusted via
  TNGF, wireline via TWIF for legacy IoT);
- *for which DNNs* offload is allowed at all, and what preference
  the network expresses to the UE (5G-first, WLAN-first, 5G-only,
  WLAN-only, ATSSS);
- *which UEs are currently attached over WLAN* and through which
  N3IWF / TNGF instance.

This package is **not** the IKEv2 / EAP-5G / ESP datapath — that's
`nf/n3iwf/`. It's the policy layer the N3IWF and the AMF consult.

### A.2 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| **Service continuity off-3GPP** | Voice / SMS / data follow the SIM onto Wi-Fi without a parallel SIP / IMS-only fallback. |
| **Hand-off-grade SLA** | DNN-level policy controls let an operator keep VoLTE-equivalent QoS over Wi-Fi calling. |
| **Cost shifting** | Move traffic off licensed spectrum where it's cheaper to deliver (campus Wi-Fi, FWA, wireline). |
| **Coverage extension** | Indoor / basement coverage gets fixed by the venue's Wi-Fi instead of new cell sites. |
| **Audit trail** | Every admission decision and every WLAN attach / detach is logged — useful for billing disputes, incident reconstruction, regulator response. |
| **One source of truth for DNN policy** | The N3IWF, the AMF, and the operator panel all read the same `wifi_access_policy` table. |

### A.3 Customer use cases (TS 22.261 §6 + TS 23.501 §4.2.8)

| Use case | Profile |
|----------|---------|
| **Wi-Fi calling** | Indoor VoLTE / VoNR carrying over home Wi-Fi via N3IWF; same MSISDN, same charging. |
| **FWA / fixed-wireless** | Home gateway terminates 5GC sessions over a wired/Wi-Fi backhaul (TWIF). |
| **Enterprise BYOD** | Corporate Wi-Fi with `trusted` admission via TNGF; per-DNN slicing for enterprise apps. |
| **Stadium / venue Wi-Fi** | High-density Wi-Fi offloading non-real-time traffic; WLAN-first preference for video. |
| **Smart-home / IoT** | Cameras and appliances on the home Wi-Fi reach the 5GC through TWIF (deferred today). |
| **ATSSS multi-access** | Latency-sensitive apps marked for split / steering across Wi-Fi + 5G simultaneously. |

### A.4 Actors and roles

```
    UE  (Wi-Fi + 5GC NAS over IPsec)
     │
     │ IKEv2 + EAP-5G + ESP
     ▼
  ┌──────────────────────────────────────────────────┐
  │       nf/n3iwf  (untrusted-WLAN gateway)          │
  │       nf/.../tngf  (trusted-WLAN, deferred wire)  │
  └────────────┬─────────────────────────────────────┘
               │
               │ Reads policy + writes attach/detach + rejects
               ▼
  ┌──────────────────────────────────────────────────────┐
  │       access/wifi_offload  (this package)             │
  │                                                       │
  │   wifi_access_policy   ── per-DNN policy table        │
  │   wifi_attached_ues    ── in-flight UE rows           │
  │   wifi_offload_log     ── attach / detach / rejected  │
  └──────────────────────────────────────────────────────┘
                                                ▲
                                                │ /api/wifi-offload/*
                                                │
                                         ┌──────┴──────┐
                                         │  Operator   │
                                         │ REST surface│
                                         └─────────────┘
```

| Actor | Role | Touches this package via |
|-------|------|--------------------------|
| **UE** | Authenticated WLAN client; carries 5GS NAS over IPsec ESP. | (downstream — UE never speaks to this package) |
| **N3IWF / TNGF / TWIF** | Gateways that terminate the WLAN side; consult the policy table on each PDU establish. | `GetPolicy`, `CheckOffload`, `AttachUE`, `DetachUE` |
| **AMF (transparent)** | Carries the NAS over the IPsec tunnel; honours the offload preference the gateway forwards. | (indirect — reads via the gateway) |
| **Operator (OAM)** | Curates per-DNN policy, audits attaches / rejects. | `/api/wifi-offload/*` |

### A.5 Operator workflow

```
   Provisioning (per DNN)
   ──────────────────────
   1.  POST /api/wifi-offload/policies
            { dnn, access_type, offload_pref, enabled }
            access_type ∈ {untrusted, trusted, wireline}
            offload_pref ∈ {5g_first, wlan_first, 5g_only,
                            wlan_only, atsss}

   2.  GET  /api/wifi-offload/policies                 catalog read
       GET  /api/wifi-offload/policies/{dnn}           single
       DELETE /api/wifi-offload/policies/{dnn}         drop → §6.2.9
                                                       default
                                                       (untrusted /
                                                       5g_first)

   Per-attach (called by N3IWF / TNGF)
   ───────────────────────────────────
   3.  POST /api/wifi-offload/admission
            { imsi, dnn, access_type }
            → { allowed, reason, access_type, offload_pref }
            §6.2.9 / §6.2.9A — gates per-DNN policy.

   4.  POST /api/wifi-offload/attached
            { imsi, access_type, n3iwf_id, inner_ip, outer_ip }
            (UPSERT on (imsi, access_type) — re-attach updates IPs.)

   5.  DELETE /api/wifi-offload/attached?imsi=&access_type=
            (detach; logs 'detached'.)

   Visibility
   ──────────
   6.  GET  /api/wifi-offload/status                   counters
       GET  /api/wifi-offload/attached                 in-flight UEs
       GET  /api/wifi-offload/attached/{imsi}?access_type=
                                                       per-UE state
       GET  /api/wifi-offload/audit?limit=             event log
```

### A.6 The admission gate — what `CheckOffload` enforces

```
   CheckOffload(imsi, dnn, access_type)
   ────────────────────────────────────
   • access_type ∉ {untrusted, trusted, wireline} → deny
   • policy row missing:
        access_type == untrusted → ALLOW (5g_first default)
        else                     → DENY  (default refuses non-untrusted)
   • policy.enabled == 0           → DENY (and audit-log 'rejected')
   • policy.offload_pref == 5g_only → DENY  (operator forbids WLAN here)
   • policy.offload_pref == wlan_only AND
          policy.access_type != access_type → DENY
   • else                          → ALLOW
```

Every refusal is logged to `wifi_offload_log` as `action='rejected'`
so the operator can spot a misconfigured DNN policy.

### A.7 What is NOT in scope here

| Thing | Where it lives |
|-------|----------------|
| **IKEv2 + EAP-5G + ESP datapath** | `nf/n3iwf/`. |
| **TNGF (trusted-WLAN gateway) wire** | TS 23.501 §6.2.9A; planned, datapath deferred. |
| **TWIF (wireline / non-NAS UEs)** | TS 23.501 §4.2.8.5; deferred. The schema accepts `access_type='wireline'` so policy can be authored ahead of the gateway. |
| **ATSSS multi-access PDU split** | TS 23.501 §5.32; policy preference here only — the actual MA-PDU split is the SMF / UPF's job. |
| **Y1 / Y2 reference points** | TS 23.501 §4.2.7; transport-only, not modelled. |
| **AAA/EAP server** | EAP termination is in N3IWF / TNGF; this package only reads the policy outcome. |

---

## Part B — Design

### B.1 Architecture

```
   Operator REST  ──────────────────────────────────────────────┐
   /api/wifi-offload/*                                           │
        │                                                        │
        ▼                                                        │
   ┌──────────────────────────────────────────────────────────┐ │
   │   access/wifi_offload/wifi_offload.go                     │ │
   │                                                           │ │
   │   SetPolicy / GetPolicy / ListPolicies / DeletePolicy     │ │
   │   AttachUE / DetachUE / IsAttached / ListAttachedUEs      │ │
   │   CheckOffload (admission gate)                           │ │
   │   GetAuditLog / GetStats                                  │ │
   └──────────┬───────────────────────────────────────────────┘ │
              │                                                  │
              ▼                                                  │
   ┌──────────────────────────────────────────────────────────┐ │
   │             SQLite (db/schemas/access.go::WifiDDL)         │ │
   │                                                           │ │
   │   wifi_access_policy(                                     │ │
   │     dnn PK, access_type CHECK ∈ {untrusted, trusted,      │ │
   │       wireline}, offload_pref CHECK ∈ {5g_first,          │ │
   │       wlan_first, 5g_only, wlan_only, atsss}, enabled,    │ │
   │     updated_at)                                           │ │
   │   wifi_attached_ues(id, imsi, access_type, n3iwf_id,      │ │
   │     inner_ip, outer_ip, attached_at,                      │ │
   │     UNIQUE(imsi, access_type))                            │ │
   │   wifi_offload_log(id, imsi, access_type,                 │ │
   │     action CHECK ∈ {attached, detached, rejected},        │ │
   │     reason, created_at)                                   │ │
   └──────────────────────────────────────────────────────────┘ │
                                                                 │
                                                       wire-format│
                                                       N3IWF /    │
                                                       TNGF /     │
                                                       TWIF       │
                                                       (separate  │
                                                        NF docs)  │
```

### B.2 Field → spec map

| Field / row | Spec § |
|-------------|--------|
| `wifi_access_policy.access_type` | TS 23.501 §6.2.9 (untrusted / N3IWF), §6.2.9A (trusted / TNGF), §4.2.8.5 (wireline / TWIF — deferred) |
| `wifi_access_policy.offload_pref` | TS 23.501 §4.2.8 + §5.32 (ATSSS) |
| `wifi_access_policy.enabled` | Operator policy; gated in `CheckOffload`. |
| `wifi_attached_ues` (UNIQUE on `(imsi, access_type)`) | TS 23.501 §4.2.8 (N3 / Y1 / Y2 reference points) |
| `wifi_offload_log.action` enum | Operator audit; sourced by every state-changing path |
| Default policy on missing row | TS 23.501 §6.2.9 (untrusted / 5g_first) |

### B.3 File map

| File | Role |
|------|------|
| `access/wifi_offload/wifi_offload.go` | All public API + admission gate + SQL access |
| `access/wifi_offload/wifi_offload_test.go` | CRUD + enum-violation + admission tests |
| `db/schemas/access.go::WifiDDL` | DDL: `wifi_access_policy`, `wifi_attached_ues`, `wifi_offload_log` |
| `webservice/app/routes_wifi_offload.go` | REST surface `/api/wifi-offload/*` |
| `webservice/app/domain_routes.go` | Wires `registerWiFiOffloadRoutes()` |

### B.4 REST surface

| Method | Path | Backing | Notes |
|--------|------|---------|-------|
| `GET` | `/api/wifi-offload/status` | `GetStats` | Aggregate counters. |
| `GET` | `/api/wifi-offload/policies` | `ListPolicies` | All DNN rows. |
| `GET` | `/api/wifi-offload/policies/{dnn}` | `GetPolicy` | 404 if missing. |
| `POST` | `/api/wifi-offload/policies` | `SetPolicy` | UPSERT on `dnn`. 400 on bad `access_type` / `offload_pref` enum (§4.2.8). |
| `DELETE` | `/api/wifi-offload/policies/{dnn}` | `DeletePolicy` | Soft revert to §6.2.9 default. |
| `POST` | `/api/wifi-offload/admission` | `CheckOffload` | Returns `{allowed, reason, access_type, offload_pref}`. Rejects audit-logged. |
| `GET` | `/api/wifi-offload/attached` | `ListAttachedUEs` | In-flight WLAN-attached UEs. |
| `POST` | `/api/wifi-offload/attached` | `AttachUE` | UPSERT on `(imsi, access_type)`. Audit-logged. |
| `DELETE` | `/api/wifi-offload/attached?imsi=&access_type=` | `DetachUE` | Audit-logged when row removed. |
| `GET` | `/api/wifi-offload/attached/{imsi}` | `IsAttached` | `{imsi, access_type, attached: bool}`. |
| `GET` | `/api/wifi-offload/audit?limit=` | `GetAuditLog` | Newest first; default limit 100. |

### B.5 Admission gate (TS 23.501 §6.2.9 default + §4.2.8 enum)

```go
// CheckOffload — see Part A.6 for the rules.
// Every deny path calls logAction(imsi, access, "rejected", reason)
// so the operator panel surface (/api/wifi-offload/audit) tells the
// "why was this UE turned away" story without spelunking the gateway
// logs.
```

### B.6 Key types / public API

```go
// AccessType vocabulary (matches schema CHECK)
const (
    AccessUntrusted = "untrusted"  // TS 23.501 §6.2.9
    AccessTrusted   = "trusted"    // TS 23.501 §6.2.9A
    AccessWireline  = "wireline"   // TS 23.501 §4.2.8.5 (TODO)
)

// OffloadPreference vocabulary (matches schema CHECK)
const (
    Pref5GFirst   = "5g_first"
    PrefWLANFirst = "wlan_first"
    Pref5GOnly    = "5g_only"
    PrefWLANOnly  = "wlan_only"
    PrefATSSS     = "atsss"
)

type AdmissionResult struct {
    Allowed     bool
    Reason      string
    AccessType  string  // suggested fallback
    OffloadPref string  // suggested preference
}

// Policy (TS 23.501 §4.2.8)
func SetPolicy(dnn, accessType, offloadPref string, enabled bool) error
func GetPolicy(dnn string) (map[string]interface{}, error)
func ListPolicies() ([]map[string]interface{}, error)
func DeletePolicy(dnn string) error

// Attached UE table
func AttachUE(imsi, accessType, n3iwfID, innerIP, outerIP string) error
func DetachUE(imsi, accessType string) error
func IsAttached(imsi, accessType string) bool
func ListAttachedUEs() ([]map[string]interface{}, error)

// Admission probe (TS 23.501 §6.2.9 / §6.2.9A)
func CheckOffload(imsi, dnn, accessType string) AdmissionResult

// Audit + stats
func GetAuditLog(limit int) ([]map[string]interface{}, error)
func GetStats() map[string]interface{}
```

### B.7 Tester coverage

| Test | Spec § | Asserts |
|------|--------|---------|
| `TC-WIFI-001 wifi_offload_policy_crud` | §4.2.8 | Per-DNN policy upsert / read / update / delete; bad `access_type` → 400; bad `offload_pref` → 400; 404 on get-after-delete. |
| `TC-WIFI-002 wifi_offload_admission_default` | §6.2.9 | No policy row → allow `untrusted`, deny `trusted`. |
| `TC-WIFI-003 wifi_offload_admission_gate` | §6.2.9 / §6.2.9A | `5g_only` refuses WLAN; `wlan_only` mismatch refused; disabled refuses; re-enabled + `5g_first` allows. |
| `TC-WIFI-004 wifi_offload_attached` | §4.2.8 | Attach → IsAttached true → list contains row → audit log carries 'attached' → DELETE → IsAttached false. |

All four currently PASS (`054c0a1` core × `6a4c688` tester).

### B.8 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| Package header (`access/wifi_offload/wifi_offload.go:36-50`) | TS 23.501 §4.2.8.5 — TWIF (wireline / non-NAS UEs); access_type='wireline' accepted in schema but no gateway implementation. |
| Package header | ATSSS — `offload_pref='atsss'` is a policy preference here; the actual MA-PDU split is SMF / UPF (TS 23.501 §5.32). |

### B.9 References

Only specs cited in source:

- **TS 23.501** — System Architecture for the 5G System
  - §4.2.7 Reference points (incl. N3 / Y1 / Y2 for non-3GPP)
  - §4.2.8 Support of non-3GPP access (umbrella)
  - §4.2.8.5 TWIF (deferred wire)
  - §5.10.2 Security Model for non-3GPP access (EAP-5G + IKEv2 + IPsec)
  - §5.32 ATSSS (multi-access PDU sessions; preference policy here)
  - §6.2.9 N3IWF (untrusted-WLAN gateway)
  - §6.2.9A TNGF (trusted-WLAN gateway)
- **TS 22.261 §6** — high-level WLAN-into-5GC service requirements

Cross-link: [`../nf/n3iwf.md`](../nf/n3iwf.md) is the per-NF deep-dive
for the IKEv2 / EAP-5G / ESP datapath that consults this package on
each PDU establish.

---
*Last refreshed against commit `054c0a1`.*

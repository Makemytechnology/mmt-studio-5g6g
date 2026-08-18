# Multi-USIM (MUSIM) — Design Document

3GPP framework for a UE that holds multiple USIM identities — each
with its own SUPI — and switches between them across paging /
service-request boundaries. The package implements the operator-
side state (groups, members, capabilities, paging audit) and a
network-side paging simulator that exercises the busy /
pre-emption decision tree of TS 23.502 §4.2.6.

## 1. Role in the 5GC

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **N1** | UE NAS | MUSIM Allowed Indication IE | TS 24.501 §9.11.3.91 |
| AMF internal | — | Group / member lookup, paging decision | TS 23.501 §5.34; TS 23.502 §4.2.6 |
| Operator REST | Panel + tester | `/api/musim/*` (in routes_musim.go) | — |

The 3GPP architecture for Multi-USIM is fully UE-centric — the
network sees "an IMSI is busy" or "this IMSI just woke up" as
ordinary procedures and doesn't need a separate NF. What the
operator panel **does** need is a place to record:

- which IMSIs share the same physical device (so paging logic can
  predict busy / pre-empt outcomes),
- which one is currently allowed (the "active USIM"),
- per-IMSI MUSIM capability (so the AMF / SMF can serialise paging
  rates correctly),
- an audit log of paging outcomes (delivered / switched / timeout /
  rejected) — TS 23.502 §4.2.6 vocabulary.

That's what `nf/amf/musim` is for.

## 2. Architecture

```
                    ┌───────────────────── nf/amf/musim ──────────────────────┐
                    │                                                         │
panel / tester ────▶│  ListGroups / GetGroup / CreateGroup / UpdateGroup     │
   /api/musim/*    │  / DeleteGroup                                          │
                    │                                                         │
                    │  AddMember / RemoveMember                              │
                    │                                                         │
                    │  ListCapabilities / UpsertCapability (24.501 §9.11.3.91)│
                    │                                                         │
                    │  ListPagingLog / Page                                   │
                    │                  └─ TS 23.502 §4.2.6 outcome simulator  │
                    └─────────────────────────────────────────────────────────┘
                                            │
                                            ▼
                                       db/engine
                                  (musim_* SQLite tables)
```

## 3. Data model

`db/schemas/domains.go::MusimDDL`:

| Table | Role |
|-------|------|
| `musim_groups` | One per MUSIM device. `device_id` is the hardware key; `active_imsi` is the currently allowed USIM. |
| `musim_group_members` | `(group_id, imsi)` tuples + `priority` + `usim_index`. FK CASCADE on group delete. |
| `musim_capabilities` | Per-IMSI: `musim_supported`, `max_usim_count`, `min_paging_interval_ms`. Upserted on conflict. |
| `musim_paging_log` | Audit of paging decisions. CHECK constraint on `outcome ∈ {delivered, switched, timeout, rejected}`. |

The vocabulary is shared between the schema CHECK constraint, the
package's `validOutcomes` map, and the panel's badge classes — so
all three move together.

## 4. Operator REST surface

| Method | Path | Calls | Notes |
|--------|------|-------|-------|
| `GET` | `/api/musim/stats` | `musim.Stats()` | Total groups + members + MUSIM-capable UEs + paging events + by_outcome histogram (delivered / switched / timeout / rejected). |
| `GET` | `/api/musim/groups` | `musim.ListGroups()` | Returns groups with members preloaded. |
| `GET` | `/api/musim/groups/{id}` | `musim.GetGroup` | 404 when nil. |
| `POST` | `/api/musim/groups` | `musim.CreateGroup` | Transactional: inserts group + members in one go; first IMSI becomes the default active. |
| `PATCH` | `/api/musim/groups/{id}` | `musim.UpdateGroup` | Sparse, allow-list `description`/`active_imsi`. `active_imsi` change verifies membership first → 400 if non-member. |
| `DELETE` | `/api/musim/groups/{id}` | `musim.DeleteGroup` | FK CASCADE drops members. |
| `POST` | `/api/musim/groups/{id}/members` | `musim.AddMember` | Adds a USIM to the group with priority + usim_index. |
| `DELETE` | `/api/musim/members/{mid}` | `musim.RemoveMember` | If the removed IMSI was active, `active_imsi` is cleared (operator can re-elect). |
| `GET` | `/api/musim/capabilities` | `musim.ListCapabilities` | All persisted rows. |
| `POST` | `/api/musim/capabilities` | `musim.UpsertCapability` | ON CONFLICT(imsi) upsert; range check `max_usim_count ∈ [1, 8]`. |
| `GET` | `/api/musim/paging-log?device_id=&limit=` | `musim.ListPagingLog` | Newest-first audit listing. |
| `POST` | `/api/musim/page` | `musim.Page` | Network-side paging simulator (see §5). |

All responses use the project's `{ok: true, ...}` envelope.

## 5. Lifecycle — paging an inactive USIM (TS 23.502 §4.2.6)

```
                          POST /api/musim/page { device_id, target_imsi }
                                       │
                                       ▼
                          look up group by device_id
                                       │
                       ┌───────────────┼───────────────┐
                       │               │               │
              target_imsi not    target_imsi          target_imsi
              a group member    is active            is inactive
                       │               │              member
                       ▼               ▼               │
                  rejected         delivered           ▼
                                                  switched
                                                  + active_imsi := target_imsi
                       │               │               │
                       └───────────────┼───────────────┘
                                       ▼
                          INSERT musim_paging_log row
                              (audit + dashboard histogram)
```

This mirrors the 3GPP §4.2.6 decision tree the AMF runs in real
hardware:

- The active USIM picks up paging directly → `delivered`.
- The inactive USIM picks up paging → the UE issues a MUSIM Busy
  Indication, the network either re-pages or pre-empts the active
  USIM. Operator side records the eventual switch.
- The IMSI isn't on the device at all → `rejected` (configuration
  mismatch; would surface as a 404 at the UE NAS layer).

The simulator never times out today; `timeout` is reserved for a
future scheduled-paging path that watches for an N2 PagingResponse
within a deadline.

## 6. Capability semantics (TS 24.501 §9.11.3.91)

`musim_capabilities` is the operator-side cache of the MUSIM
Allowed Indication IE per UE:

| Field | Source | Notes |
|-------|--------|-------|
| `musim_supported` | UE NAS Capability or operator pre-seed | If 0, the AMF should never page an inactive IMSI on this device (and `musim.Page` returns `rejected` if asked to). |
| `max_usim_count` | UE NAS Capability | Range 1..8 enforced (spec doesn't pin a maximum, but practical UE limits sit here). |
| `min_paging_interval_ms` | Operator policy | The AMF spaces re-pages by at least this many milliseconds (used by the busy-indication retry logic). |

Upserts use `ON CONFLICT(imsi) DO UPDATE` so the panel can patch a
single field without a read-modify-write round trip.

## 7. Tester coverage

Operator-API only — no UE / gNB.

| TC ID | File | Spec | Asserts |
|-------|------|------|---------|
| TC-MUSIM-OAM-001 | `oam/tc_musim_oam.py` | TS 23.501 §5.34 | `/stats` envelope; all four `by_outcome` keys. |
| TC-MUSIM-OAM-002 | ″ | TS 23.501 §5.34 | Group Create → Get → Patch → Delete; first IMSI becomes default active. |
| TC-MUSIM-OAM-003 | ″ | TS 23.501 §5.34 | Add member; removing active member clears `active_imsi`. |
| TC-MUSIM-OAM-004 | ″ | TS 23.501 §5.34 | Missing `device_id`, blank IMSI, non-member `active_imsi` patch all 400. |
| TC-MUSIM-OAM-005 | ″ | TS 24.501 §9.11.3.91 | Capability UPSERT collapses rows; `max_usim_count=99` returns 400. |
| TC-MUSIM-OAM-006 | ″ | TS 23.502 §4.2.6 | Paging the active IMSI → `delivered`. |
| TC-MUSIM-OAM-007 | ″ | TS 23.502 §4.2.6 | Paging an inactive member → `switched` and `active_imsi` flips. |
| TC-MUSIM-OAM-008 | ″ | TS 23.502 §4.2.6 | Paging a non-member → `rejected`; group state unchanged. |
| TC-MUSIM-OAM-009 | ″ | TS 23.501 §5.34 | GET / PATCH / DELETE on unknown group + DELETE on unknown member + page on unknown device all return 404. |

## 8. What's not implemented

| Area | Status | Spec |
|------|--------|------|
| N2 NGAP Paging carrying MUSIM Allowed Indication | not wired | TS 24.501 §9.11.3.91 |
| Scheduled-paging timeout path (audit row outcome=`timeout`) | reserved | TS 23.502 §4.2.6.3 |
| Per-USIM Service-Request pre-emption (network-decided pre-empt) | not modelled | TS 23.502 §4.2.6.4 |
| Multi-USIM-aware RAN paging coordination | not wired | TS 23.501 §5.34.4 |

The schema + REST surface are sized for the missing pieces — the
`timeout` outcome is already in the CHECK constraint and the
dashboard histogram, so adding the path is purely runtime work.

## 9. References

- **TS 23.501** §5.34 — System support for Multi-USIM devices.
- **TS 23.502** §4.2.6 — Multi-USIM procedures.
- **TS 24.501** §9.11.3.91 — MUSIM Allowed Indication IE.

---
*Last refreshed: package created (nf/amf/musim/api.go), operator REST
surface added (routes_musim.go), tester coverage map (9 TCs).*

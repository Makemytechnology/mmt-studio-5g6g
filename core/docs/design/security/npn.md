# NPN — Non-Public Networks (TS 23.501 §5.30)

The operator-side surface for Non-Public Networks. Owns the NPN
records, Closed Access Group (CAG) membership, the SNPN admission
gate, and the per-IMSI access audit log.

# Part A — Functional

## A.1 Why NPN?

A 5G deployment that serves a private user set (factory floor, port,
campus, public-safety squad). TS 23.501 §5.30 defines two flavours:

- **SNPN** (Standalone NPN) — a complete 5GC of its own, identified
  by `(PLMN-ID, NID)`. NID is mandatory (TS 23.501 §5.30.3).
- **PNI-NPN** (Public-Network-Integrated NPN) — a public PLMN slice
  restricted to one Closed Access Group via the 32-bit `CAG-ID` (TS
  23.501 §5.30.2). NID does not apply.

This vertical owns the operator records: which NPNs exist, which CAGs
they carry, which IMSIs are members of each CAG, and the admission
verb (`AuthenticateSNPN`) the AMF calls during Initial Registration
to decide whether to accept a UE on the NPN.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **AMF Initial Registration** | UE → AMF | NAS over NGAP | TS 23.502 §4.2.2.2.3 | AMF calls `AuthenticateSNPN(imsi, cag_id, nid)`. |
| **CAG-ID** | gNB → UE | RRC SIB | TS 23.501 §5.30.2 | 8 hex digits (32-bit ID); validated by `ValidateCAGID`. |
| **SNPN-id** | gNB → UE | RRC SIB | TS 23.501 §5.30.3 | `(PLMN, NID)` pair stored on `npn_networks`. |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/npn/*` (this file). |
| **AUSF / SNPN auth** | UE → AUSF | 5G-AKA / EAP-AKA' | TS 33.501 §6.1.4 | NID-anchored credentials (deferred — see §A.5). |

## A.3 Operator-visible behaviours

### A.3.1 Vocabulary CHECKs

| Field | Allowed values |
|---|---|
| `npn_networks.npn_type`   | `SNPN` / `PNI-NPN` |
| `npn_networks.status`     | `active` / `inactive` |
| `npn_access_log.action`   | `admitted` / `denied` / `removed` |

The route layer pre-validates `npn_type`; SNPN-create additionally
requires `nid` (TS 23.501 §5.30.3 — SNPN-id = PLMN-ID + NID).
Schema CHECK is the backstop.

### A.3.2 CAG-ID format

`ValidateCAGID` is `^[0-9A-Fa-f]{8}$` (32 bits as 8 hex digits).
Both the route and the package's `CreateCAG` enforce it; the route
returns 400 with the spec anchor in the message.

### A.3.3 ON DELETE CASCADE

Deleting an NPN cascades into its CAGs (`npn_cag.npn_id` FK), and
deleting a CAG cascades into its members (`npn_cag_members.cag_id`
FK). The audit log (`npn_access_log`) is preserved across deletes
— the row's NPN/CAG FK becomes orphaned (NULL/missing); the
audit trail outlives the operator's edits.

### A.3.4 Admission: AuthenticateSNPN(imsi, cag_id, nid)

Returns `{allowed: bool, reason: string, cag_id?, nid?}` and writes
one `npn_access_log` row. The decision flow:

```
if !validateCAGID(cag_id):  deny "invalid CAG-ID format"
if imsi not in CAG:         deny "IMSI not in CAG"
otherwise:                  admit
```

Each call (allowed or denied) produces an audit row keyed by IMSI for
operator forensics. The legacy `/api/npn/authorize` alias rewrites
`allowed` → `authorized` for older panels; the canonical endpoint is
`/api/npn/authenticate`.

## A.4 Operator REST API (`/api/npn/*`)

### A.4.1 Networks

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/npn/stats`              | `{npn_count, cag_count, member_count, snpn_count, pni_npn_count, cag_groups, authorized_ues}`. |
| GET    | `/api/npn/networks`           | Array of NPN rows (newest last). |
| GET    | `/api/npn/networks/{id}`      | Single NPN; 404 on miss. |
| POST   | `/api/npn/networks`           | `{name, npn_type, plmn, nid?}`; SNPN-NID required by §5.30.3. |
| DELETE | `/api/npn/networks/{id}`      | CASCADE deletes CAGs + members. |

### A.4.2 CAGs (TS 23.501 §5.30.2)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/npn/cag?npn_id=N`              | Array of CAG rows; optional NPN filter. |
| POST   | `/api/npn/cag`                       | `{cag_id, npn_id, name, description?}`; cag_id is 8-hex. |
| DELETE | `/api/npn/cag/{id}`                  | CASCADE deletes members. |

### A.4.3 CAG membership

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/npn/cag/{id}/members`         | `{members: [...]}`. |
| POST   | `/api/npn/cag/{id}/members`         | `{imsi}`; INSERT OR IGNORE (re-add is idempotent). |
| DELETE | `/api/npn/cag/{id}/members/{imsi}`  | Removes. |

### A.4.4 Admission gate (TS 33.501 §6.1.4)

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/npn/authenticate` | `{imsi, cag_id, nid}` → `{allowed, reason, cag_id?, nid?}`. Audit-logged. |
| POST | `/api/npn/authorize`    | Legacy alias (adds `authorized` echo). |
| GET  | `/api/npn/access-log?limit=N&imsi=` | `{items: [...]}` newest first; per-IMSI filter. |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| TS 33.501 §6.1.4 SNPN credentials | — | Admission today is membership-only; primary auth is AUSF's 5G-AKA path. SNPN-specific credential types (per-NID PSK / certificate) are not yet enforced. |
| TS 23.501 §5.30.4 access-control with credentials owned by SNPN | — | Credential-owner separation not modelled. |
| Slice-level NPN scoping (S-NSSAI in CAG) | — | CAG today is IMSI list; S-NSSAI gate is in `/api/slicing`. |
| `npn_access_log.action='removed'` | — | Schema accepts it but the package emits only `admitted`/`denied`. |

---

# Part B — Design

## B.1 Process layout

```
                      ┌───────────────────────┐
                      │ AMF Initial           │
                      │ Registration (TS      │
                      │ 23.502 §4.2.2.2.3)    │
                      └──────────┬────────────┘
                                 │ AuthenticateSNPN(imsi, cag, nid)
                                 ▼
                      ┌───────────────────────┐
                      │ security/npn          │
                      │  ├ ValidateCAGID      │
                      │  ├ CheckMembership    │
                      │  └ logAccess          │
                      └──────────┬────────────┘
                                 │
        ┌────────────────────────┼─────────────────────────┐
        ▼                        ▼                         ▼
┌──────────────┐      ┌──────────────────┐       ┌────────────────┐
│ npn_networks │      │ npn_cag          │       │ npn_access_log │
│ (PLMN, NID,  │ ◀FK─ │ npn_id, cag_id,  │       │ imsi, npn_id,  │
│  npn_type)   │      │ name             │       │ cag_id, action │
└──────────────┘      └────────┬─────────┘       └────────────────┘
                               │ FK
                               ▼
                      ┌──────────────────┐
                      │ npn_cag_members  │
                      │ cag_id, imsi     │
                      └──────────────────┘
                              ▲
                              │ JSON / HTTP
                              │
                  webservice/app/routes_npn.go
                  webservice/templates/npn.html
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `security/npn/npn.go` | ~200 | NPN/CAG/member CRUD; ValidateCAGID; AuthenticateSNPN; logAccess. |
| `db/schemas/domains.go` | (slice) | NpnDDL — four tables + indexes. |
| `webservice/app/routes_npn.go` | ~340 | REST surface for §A.4 (this file). |

Tests:
- `mmt_studio_core_tester/src/testcases/security/tc_npn.py` — 11 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE npn_networks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  npn_type TEXT NOT NULL CHECK (npn_type IN ('SNPN','PNI-NPN')),
  plmn TEXT NOT NULL,
  nid TEXT,
  description TEXT,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive')),
  config_json TEXT,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE npn_cag (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  cag_id TEXT NOT NULL UNIQUE,                  -- 8 hex digits
  npn_id INTEGER NOT NULL REFERENCES npn_networks(id) ON DELETE CASCADE,
  name TEXT NOT NULL, description TEXT, created_at TEXT
);

CREATE TABLE npn_cag_members (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  cag_id INTEGER NOT NULL REFERENCES npn_cag(id) ON DELETE CASCADE,
  imsi TEXT NOT NULL,
  authorized INTEGER NOT NULL DEFAULT 1,
  added_at TEXT,
  UNIQUE (cag_id, imsi)
);

CREATE TABLE npn_access_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  imsi TEXT NOT NULL,
  npn_id INTEGER, cag_id INTEGER,               -- nullable (NPN/CAG may be deleted later)
  action TEXT NOT NULL CHECK (action IN ('admitted','denied','removed')),
  reason TEXT, created_at TEXT
);
```

## B.4 Public API

```go
// CAG-ID format (TS 23.501 §5.30.2)
func ValidateCAGID(cagID string) bool

// Networks
func CreateNPN(name, npnType, plmn, nid string) (int64, error)
func GetNPN(id int64) (map[string]any, error)
func ListNPNs() ([]map[string]any, error)
func DeleteNPN(id int64) error

// CAGs
func CreateCAG(cagID string, npnID int64, name, description string) (int64, error)
func GetCAG(id int64) (map[string]any, error)
func ListCAGs(npnID int64) ([]map[string]any, error)
func DeleteCAG(id int64) error

// Members
func AddMember(cagRowID int64, imsi string) (int64, error)
func RemoveMember(cagRowID int64, imsi string) error
func ListMembers(cagRowID int64) ([]map[string]any, error)
func CheckMembership(cagID, imsi string) bool

// SNPN admission
func AuthenticateSNPN(imsi, cagID, npnNID string) map[string]any

// Status / panel
func GetNPNStatus() map[string]any
func List() ([]map[string]any, error)
func Status() map[string]any

// Routes
func (s *Server) registerNPNRoutes()
```

## B.5 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-NPN-001 `npn_create_snpn`              | SNPN with NID; round-trip; type check |
| TC-NPN-002 `npn_create_pni_npn`           | PNI-NPN without NID |
| TC-NPN-003 `npn_cag_management`           | CAG create + member add + verify membership |
| TC-NPN-004 `npn_authorize`                | legacy `/authorize` alias still works |
| TC-NPN-005 `npn_deny_unauthorized`        | non-member IMSI denied via `/authenticate` |
| TC-NPN-006 `npn_access_log`               | admit + deny rows land in audit log |
| TC-NPN-007 `npn_cag_full_crud`            | CAG create/list/delete; CASCADE removes members |
| TC-NPN-008 `npn_authenticate_admits`      | member IMSI on valid CAG-ID admitted |
| TC-NPN-009 `npn_authenticate_denies`      | bad CAG-ID format → denied with format reason |
| TC-NPN-010 `npn_access_log_persisted`     | audit log survives session restart |
| TC-NPN-011 `npn_delete_cascades_into_cags`| NPN delete removes its CAGs |

All eleven wired into `tc_npn.py::ALL_NPN_TCS` (now under
`testcases/security/`) and pass against the strengthened NPN routes.

## B.6 References

- **TS 23.501** §5.30   — Non-Public Networks (umbrella).
- **TS 23.501** §5.30.2 — Closed Access Group (CAG-ID).
- **TS 23.501** §5.30.3 — SNPN identification (PLMN, NID).
- **TS 23.501** §5.30.4 — Credentials owned by an SNPN (TODO; partial).
- **TS 23.502** §4.2.2.2.3 — SNPN registration procedure.
- **TS 33.501** §6.1.4 — SNPN authentication.
- `docs/design/security/core_security.md` — sibling firewall + IDS surface.
- `docs/design/security/ran_sharing.md` — sibling RAN-share operator records.

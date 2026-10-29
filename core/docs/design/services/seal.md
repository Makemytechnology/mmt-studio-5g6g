# SEAL — Design Document

Service Enabler Architecture Layer — operator-side state for the
**TS 23.434** common SEAL services that vertical applications (MCX,
V2X, UAS, FRMCS) share: Group Management (GMS), Location Management
(LMS), Configuration Management (CMS), and Identity Management
(IdMS).

---

## Part A — Functional view

### A.1 What SEAL is, in plain terms

3GPP defines a stack of **vertical applications** — Mission Critical
voice / data (MCX), V2X, UAS, FRMCS (railways), and so on. Most of
them need the same supporting services: who's in the group, where
is each user, what's their config, what's their identity. Rather
than reinvent these per vertical, 3GPP carved them out into one
common tier — **SEAL** — that every vertical reuses.

This package is the operator-side state machine for the four
most-used SEAL services. It's the single OAM surface that replaces
"VAL group lists scattered across MCX / V2X / UAS systems" with one
table per concept.

### A.2 The four SEAL services modelled here

| SEAL service | Spec § | What it does |
|--------------|--------|--------------|
| **GMS — Group Management Server** | TS 23.434 §10 | Stores groups, their members, and per-group config. The "who can talk to whom" registry shared by every vertical. |
| **LMS — Location Management Server** | TS 23.434 §9 | Lets a VAL server subscribe to UE location updates. Surfaces network-positioning results to the application tier. |
| **CMS — Configuration Management Server** | TS 23.434 §11 | Per-user / per-group key-value config the VAL UE pulls down. |
| **IdMS — Identity Management Server** | TS 23.434 §12 | Maps a VAL user identity (e.g. a Mission-Critical user id) to a 3GPP UE identity (IMSI). |

Other SEAL services (KMS — TS 23.434 §13; NRM — §14) are deferred.

### A.3 Why an operator runs this

| Driver | Concrete payoff |
|--------|----------------|
| **One tier, many verticals** | Don't build group / location / config / identity stacks per vertical app — host them once in SEAL and let MCX / V2X / UAS / FRMCS reuse. |
| **Mission-Critical compliance** | MCPTT / MCData / MCVideo deployments require GMS + IdMS by spec. |
| **Audit & central admin** | Operator owns the user-identity binding (IdMS) and group membership (GMS); changes are logged in one place. |
| **Network-positioning resale** | LMS turns the operator's positioning capability (LMF / GMLC) into a VAL-facing subscription product. |
| **Cross-vertical group reuse** | A single group can serve push-to-talk for MCX *and* fleet messaging for V2X. |
| **Standardised config push** | CMS becomes the single channel for sending per-UE settings down to a VAL UE — no parallel SMS / OTA channel. |

### A.4 Customer use cases (TS 22.280 / TS 22.281 / TS 22.282 + TS 22.186 / TS 22.125 reuse)

| Use case | Profile |
|----------|---------|
| **Mission-Critical Push-to-Talk (MCPTT)** | Talk-groups; group members; per-group config. GMS + IdMS + CMS. |
| **Fleet management** | Group of fleet UEs; periodic location push to dispatcher. GMS + LMS. |
| **Public-safety incident response** | Ad-hoc groups; resolved-IMSI-from-callsign on the fly. GMS + IdMS. |
| **V2X corridor / platoon** | Vehicle group; per-vehicle config pushed to a UE. GMS + CMS. |
| **UAS swarm operations** | Drone group with controller assignment; group-config push. GMS + CMS. |
| **FRMCS railway voice / data** | Train-crew group; per-line config. GMS + CMS. |
| **VAL user federation** | One operator, many VAL apps; IdMS maps each user to the right IMSI. |

### A.5 Actors and roles

```
   VAL UE (MCX / V2X / UAS / FRMCS app)
        │
        │  SEAL-CM / SEAL-GM / SEAL-IdM / SEAL-LM (TS 24.546/.547/.548)
        │  (deferred wire — local stack speaks JSON)
        ▼
   ┌────────────────────────────────────────────────────────────┐
   │                   services/seal  (this package)             │
   │                                                              │
   │   seal_groups + seal_group_members  ── §10 / §10.3 GMS       │
   │   seal_location_subs                ── §9 LMS                 │
   │   seal_configs                      ── §11 CMS                │
   │   seal_val_users                    ── §12 IdMS               │
   └────────────────────────────────────────────────────────────┘
                                                  ▲
                                                  │ /api/seal/*
                                                  │
                                          ┌───────┴───────┐
                                          │  Operator     │
                                          │  REST surface │
                                          └───────────────┘

   ┌──────────────────────────┐         ┌──────────────────────────┐
   │  VAL servers (MCX / V2X  │         │     5GC: LMF / GMLC      │
   │  / UAS / FRMCS) consume  │         │  (positioning_sessions   │
   │  groups / configs /      │         │  feeds SEAL-LMS via      │
   │  IMSI bindings           │         │  GetLocation)            │
   └──────────────────────────┘         └──────────────────────────┘
```

| Actor | Role | Touches this package via |
|-------|------|--------------------------|
| **VAL UE** | Pulls config from CMS, joins/leaves groups via GMS, authenticates via IdMS. | (UE-side; routes serve the data the VAL UE consumes) |
| **VAL server** | The application-tier server (MCPTT controller, V2X orchestrator, etc.). Reads groups / configs / mapped identities. | `/api/seal/*` GETs |
| **Operator (OAM)** | Curates groups, manages config, maintains identity bindings, sets up location subscriptions. | `/api/seal/*` writes |
| **5GC / LMF** | Source of UE position; SEAL-LMS reads `positioning_sessions` to surface the latest fix. | `GetLocation` joins onto `positioning_sessions`. |

### A.6 Operator workflow

```
   Group lifecycle (TS 23.434 §10 / §10.3 GMS)
   ──────────────────────────────────────────
   1.  POST /api/seal/groups                  create with name + optional app_id
   2.  POST /api/seal/groups/{id}/members     bulk add (list of {imsi, role})
                                              role ∈ {admin, member, viewer}
   3.  GET  /api/seal/groups/{id}             group + members
   4.  DELETE /api/seal/groups/{id}/members?imsi= remove member
   5.  DELETE /api/seal/groups/{id}           drop the group

   Location subscriptions (TS 23.434 §9 LMS)
   ─────────────────────────────────────────
   6.  POST /api/seal/location/subscriptions
        { target_type:"imsi", target_id, callback_url, interval_s }
   7.  GET  /api/seal/location/subscriptions       current subscriptions
   8.  DELETE /api/seal/location/subscriptions/{id} deactivate
   9.  GET  /api/seal/location/{imsi}              latest LMF fix passthrough

   Configs (TS 23.434 §11 CMS)
   ──────────────────────────
   10. POST /api/seal/configs                set (target_type, target_id,
                                                  config_key, config_value)
   11. GET  /api/seal/configs?target_id=     filter by target
   12. GET  /api/seal/configs/{type}/{id}/{key}  single (target,key) → value
   13. DELETE /api/seal/configs/{config_id}  drop one row

   Identity (TS 23.434 §12 IdMS)
   ────────────────────────────
   14. POST /api/seal/identity/mappings      bind val_user_id → IMSI (+ app_id?)
   15. GET  /api/seal/identity/resolve?val_user_id=
                                              forward: ID → {IMSI, ...}
   16. GET  /api/seal/identity/resolve?imsi=
                                              reverse: IMSI → all VAL users
   17. DELETE /api/seal/identity/mappings/{val_user_id} unbind
```

### A.7 Member roles — what each one means here

| Role | Meaning |
|------|---------|
| `admin` | Can edit the group config; receives admin-only group notifications. |
| `member` | Default; full participation in group communication. |
| `viewer` | Receives notifications but does not transmit; useful for dispatcher / observer roles. |

Roles are operator-local policy beyond §10.3's "the group has a list
of members"; the SQL CHECK enforces the closed enum.

### A.8 What is NOT in scope here

| Thing | Where it lives |
|-------|----------------|
| **CMS notification channel** (server-push to live VAL UEs when a config row changes) | TS 24.546 §6; deferred. |
| **OAuth2 / OIDC token issuance + refresh + revocation** | TS 24.547 §5/§6; only the val_user_id ↔ IMSI binding row is modelled. |
| **KMS — Key Management** (MIKEY-SAKKE delivery) | TS 23.434 §13; deferred. |
| **NRM — Network Resource Management** (QoS request to PCF on behalf of VAL) | TS 23.434 §14; deferred. |
| **On-demand "report-now" location request** | TS 23.434 §9.3; only periodic push subscription is modelled. |
| **UE-initiated group join** (Group Membership Update Request §10.3.2.6) | Operator-initiated GMS-side `AddMember` is the only entry today. |
| **SEAL-CM / SEAL-GM / SEAL-IdM / SEAL-LM wire** (TS 24.546 / .547 / .548 §5) | Deferred — the local stack speaks JSON, the §5 protocols are not modelled. |

---

## Part B — Design

### B.1 Architecture

```
   Operator REST  ──────────────────────────────────────────────┐
   /api/seal/*                                                   │
        │                                                        │
        ▼                                                        │
   ┌──────────────────────────────────────────────────────────┐ │
   │       services/seal/seal.go  (Go package, this design)    │ │
   │                                                           │ │
   │   GMS  CRUD       ── seal_groups, seal_group_members      │ │
   │   LMS  subs       ── seal_location_subs (active flag)     │ │
   │   LMS  fetch      ── joins onto positioning_sessions      │ │
   │   CMS  CRUD       ── seal_configs (UPSERT on conflict)    │ │
   │   IdMS CRUD       ── seal_val_users (val_user_id PK)      │ │
   └──────────┬───────────────────────────────────────────────┘ │
              │                                                  │
              ▼                                                  │
   ┌──────────────────────────────────────────────────────────┐ │
   │             SQLite (db/schemas/domains.go::SealDDL)       │ │
   │                                                           │ │
   │   seal_groups                                             │ │
   │   seal_group_members(role CHECK ∈ {admin,member,viewer})  │ │
   │   seal_location_subs(active flag, last_notified_at)       │ │
   │   seal_configs UNIQUE(target_type,target_id,config_key)   │ │
   │   seal_val_users(val_user_id UNIQUE)                      │ │
   └──────────────────────────────────────────────────────────┘ │
                                                                 │
                                                       wire-format│
                                                       SEAL-CM /  │
                                                       SEAL-GM /  │
                                                       SEAL-IdM   │
                                                       (TS 24.546 │
                                                       /.547 /.548│
                                                       §5;        │
                                                       deferred)  │
```

### B.2 Field → spec map

| Field / row | Spec § |
|-------------|--------|
| `seal_groups` | TS 23.434 §10 (GMS) |
| `seal_group_members.role` | TS 23.434 §10.3 (membership; role CHECK ∈ {admin, member, viewer} is operator policy) |
| `seal_location_subs.target_type` ∈ {imsi, group} | TS 23.434 §9 (LMS subscription target) |
| `seal_location_subs.interval_s` | §9 (periodic push cadence) |
| `seal_configs(target_type, target_id, config_key)` | TS 23.434 §11 (CMS lookup key) |
| `seal_val_users(val_user_id, imsi)` | TS 23.434 §12 (IdMS binding) |
| `GetLocation(imsi)` query target | TS 23.273 LCS via `positioning_sessions` (cross-NF read) |

### B.3 File map

| File | Role |
|------|------|
| `services/seal/seal.go` | All public API + SQL access |
| `services/seal/seal_test.go` | CRUD + IdMS + LMS coverage |
| `db/schemas/domains.go::SealDDL` | DDL: groups, group_members, location_subs, configs, val_users |
| `webservice/app/routes_seal.go` | REST surface `/api/seal/*` |
| `webservice/app/domain_routes.go` | Wires `registerSEALRoutes()` |

### B.4 REST surface

| Method | Path | Backing | Notes |
|--------|------|---------|-------|
| `GET` | `/api/seal/status` | `GetSEALStats` | Aggregate counters. |
| `GET` | `/api/seal/groups` | `ListGroups` | All groups (no members). |
| `GET` | `/api/seal/groups/{group_id}` | `GetGroup` | Group + members; 404 if missing. |
| `POST` | `/api/seal/groups` | `CreateGroup` | TS 23.434 §10. 400 on missing name. |
| `DELETE` | `/api/seal/groups/{group_id}` | `DeleteGroup` | CASCADE removes members. |
| `GET` | `/api/seal/groups/{group_id}/members` | `ListMembers` | `{group_id, members, count}`. |
| `POST` | `/api/seal/groups/{group_id}/members` | `AddMember` (bulk) | TS 23.434 §10.3 — body is a list of `{imsi, role}`. 400 on bad role enum. |
| `DELETE` | `/api/seal/groups/{group_id}/members?imsi=` | `RemoveMember` | |
| `GET` | `/api/seal/location/subscriptions[?active_only=1]` | `ListLocationSubs` | TS 23.434 §9. |
| `POST` | `/api/seal/location/subscriptions` | `CreateLocationSub` | Defaults `target_type=imsi`. |
| `DELETE` | `/api/seal/location/subscriptions/{sub_id}` | `DeactivateLocationSub` | Soft (`active=0`). |
| `GET` | `/api/seal/location/{imsi}` | `GetLocation` | Joins LMF `positioning_sessions`; 404 if no completed session. |
| `GET` | `/api/seal/configs[?target_id=]` | `ListConfigs` | TS 23.434 §11. |
| `GET` | `/api/seal/configs/{type}/{id}/{key}` | `GetConfig` | Single (target,key) → value. |
| `POST` | `/api/seal/configs` | `SetConfig` | UPSERT on `(target_type, target_id, config_key)`. |
| `DELETE` | `/api/seal/configs/{config_id}` | `DeleteConfig` | |
| `GET` | `/api/seal/identity/mappings` | `ListVALUsers` | TS 23.434 §12. |
| `POST` | `/api/seal/identity/mappings` | `MapVALUser` | UPSERT on `val_user_id`. |
| `DELETE` | `/api/seal/identity/mappings/{val_user_id}` | `UnmapVALUser` | |
| `GET` | `/api/seal/identity/resolve?val_user_id=` | `ResolveVALUser` | Forward; 404 on miss. |
| `GET` | `/api/seal/identity/resolve?imsi=` | `ResolveIMSI` | Reverse — `{imsi, val_users:[…]}` (multiple VAL users may share one IMSI across apps). |

### B.5 Key types / public API

```go
type Group struct {
    ID        int64
    Name, CreatedAt string
    Description, AppID, ConfigJSON *string
    Members []Member  // populated by GetGroup
}

type Member struct {
    ID, GroupID int64
    IMSI, Role  string  // role CHECK ∈ {admin, member, viewer}
    JoinedAt    string
}

type LocationSub struct {
    ID                                          int64
    TargetType, TargetID, CallbackURL, CreatedAt string
    IntervalS, Active                            int
    LastNotifiedAt                              *string
}

type Config struct {
    ID                          int64
    TargetType, TargetID, ConfigKey, UpdatedAt string
    ConfigValue                 *string
}

type VALUser struct {
    ID                       int64
    VALUserID, IMSI, CreatedAt string
    AppID                    *string
}

// GMS (§10 / §10.3)
func ListGroups() ([]Group, error)
func GetGroup(id int64) (*Group, error)
func CreateGroup(name, description, appID string, configJSON *string) (int64, error)
func DeleteGroup(id int64) error
func ListMembers(groupID int64) ([]Member, error)
func AddMember(groupID int64, imsi, role string) (int64, error)
func RemoveMember(groupID int64, imsi string) error

// LMS (§9)
func ListLocationSubs(activeOnly bool) ([]LocationSub, error)
func CreateLocationSub(targetType, targetID, callbackURL string, intervalS int) (int64, error)
func DeactivateLocationSub(id int64) error
func GetLocation(imsi string) map[string]interface{}

// CMS (§11)
func ListConfigs() ([]Config, error)
func GetConfig(targetType, targetID, configKey string) (*Config, error)
func SetConfig(targetType, targetID, configKey, configValue string) error
func DeleteConfig(id int64) error

// IdMS (§12)
func ListVALUsers() ([]VALUser, error)
func MapVALUser(valUserID, imsi string, appID *string) (int64, error)
func UnmapVALUser(valUserID string) error
func ResolveVALUser(valUserID string) (*VALUser, error)
func ResolveIMSI(imsi string) ([]VALUser, error)

// Aggregates
func GetSEALStats() map[string]interface{}
```

### B.6 Tester coverage

| Test | Spec § | Asserts |
|------|--------|---------|
| `TC-SEAL-001 seal_create_group` | §10 / §10.3 | Group create + add admin member + readback. |
| `TC-SEAL-002 seal_group_members` | §10.3 | Bulk-add three members with three roles; list shows all three. |
| `TC-SEAL-003 seal_location` | §9 | Subscription create + list + deactivate. |
| `TC-SEAL-004 seal_config` | §11 | Set key + filter list by `target_id`. |
| `TC-SEAL-005 seal_identity` | §12 | Map val_user → IMSI; resolve forward. |
| `TC-SEAL-006 seal_identity_reverse_lookup` | §12 | Two val_users → same IMSI; reverse lookup returns both. |

All six currently PASS (`9f46df7` core × `83fc485` tester).

### B.7 Stubs / TODOs from grep

| Site | TODO |
|------|------|
| Package header | TS 23.434 §13 (KMS), §14 (NRM); TS 24.546 §6 (CMS notification channel); TS 24.547 §5/§6 (OAuth2 / OIDC token issuance, refresh, revocation). |
| `AddMember` | TS 23.434 §10.3 — UE-initiated Group Membership Update Request / Response / Notification (§10.3.2.6 / .7 / .8) deferred. |
| `CreateLocationSub` | TS 23.434 §9.3 — on-demand "report-now" deferred. |
| `SetConfig` | TS 24.546 §6 — server-push notification on config change deferred. |

### B.8 References

Only specs cited in source:

- **TS 23.434** — Service Enabler Architecture Layer
  - §6 SEAL functional model
  - §9 Location Management Server (LMS)
  - §10 / §10.3 Group Management Server (GMS) + procedures
  - §11 Configuration Management Server (CMS)
  - §12 Identity Management Server (IdMS)
- **TS 24.546 §5** — SEAL CM protocol (deferred wire)
- **TS 24.547 §5** — SEAL IdMS protocol / OAuth2 / OIDC (deferred wire)
- **TS 24.548 §5** — SEAL GM protocol (deferred wire)

Cross-link: `services/v2x/`, `services/uas/`, `services/prose/` are
the verticals SEAL serves; `nf/lmf/` is the source of network-side
position the LMS surfaces to VAL servers via `GetLocation`.

---
*Last refreshed against commit `9f46df7`.*

# Lawful Intercept (LI) — Data Design

This is the **data design** companion to [`li.md`](li.md). It covers
the on-disk schema, the in-memory hot-path cache, the wire-encoding
choices (and the deferred ones), the audit-trail invariants, the
access-control row, and the Go API surface that exposes all of the
above. Read [`li.md`](li.md) first for the operator-level "why".

> Spec anchors: TS 33.127 §5 (provisioning data model), §6
> (X1/X2/X3 transports — deferred), §7 (POI placement), §8 (audit
> requirements). TS 33.128 §6.2 / §7 / §8 (stage-3 IRI and CC
> encodings — deferred). TS 33.501 §5.9 NOTE 3 (the only LI hook
> that lands inside the locally-loaded 5G security spec).

---

## 1. On-disk schema

Four tables live under `db/schemas/domains.go::LiDDL`. SQLite
text-mode timestamps everywhere — every write goes through
`datetime('now')` (DDL default) or `nowISO()` (Go-side), and every
comparison is lexicographic. An earlier version of the code wrote
float64 epoch seconds into the same TEXT columns; that drifted under
`ExpireWarrants` and `refreshTargets` and is the reason the package
header (`security/li/li.go:34-43`) is explicit about the format
contract.

### 1.1 `li_warrants` — the authoritative warrant store

| Column | Type | Constraints | Purpose |
|--------|------|-------------|---------|
| `warrant_id` | TEXT | PRIMARY KEY | Operator-curated unique handle. Often the LEA's case file id. UNIQUE is enforced; `DeleteWarrant` is the only way to free a warrant_id. |
| `authority` | TEXT | NOT NULL | Issuing authority (court, regulator). Audit-trail anchor; never blank for a real warrant. |
| `case_reference` | TEXT | NOT NULL | LEA case id; appears in audit + downstream MDF. |
| `target_imsi` | TEXT | NOT NULL | Hot-path key. The matching key for capture. |
| `target_msisdn` | TEXT | NULLable | Cross-reference; not used for matching. |
| `scope` | TEXT | NOT NULL CHECK ∈ {`iri`, `cc`, `iri+cc`} | What the warrant authorises. The CHECK is the spec firewall — bogus values can never land. |
| `start_time` | TEXT | NOT NULL DEFAULT `datetime('now')` | Active window start. |
| `end_time` | TEXT | NOT NULL | Active window end. ExpireWarrants compares lex-against this. |
| `status` | TEXT | NOT NULL CHECK ∈ {`active`, `expired`, `revoked`} | Lifecycle marker. Hot path filters on `active` + window. |
| `mdf_endpoint` | TEXT | NULLable | Opaque MDF locator, surfaced for the future X2/X3 wire. |
| `created_at` | TEXT | NOT NULL DEFAULT `datetime('now')` | First write only. |
| `created_by` | TEXT | NOT NULL | Operator identity captured at create-time (route layer derives this from `X-LI-Operator` / BasicAuth / RemoteAddr). |

**Index:** `idx_li_warrant_imsi(target_imsi)` — drives the
cache-rebuild query.

**Cascade:** `li_iri_events` and `li_cc_sessions` carry FK
`ON DELETE CASCADE`; `DeleteWarrant` walks them explicitly anyway
so the operation works whether or not the SQLite FK pragma is on.

### 1.2 `li_iri_events` — the IRI queue

| Column | Type | Constraints | Purpose |
|--------|------|-------------|---------|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | Monotone — `MarkDelivered(maxID)` flips the prefix `id <= maxID`. |
| `warrant_id` | TEXT | NOT NULL FK → `li_warrants` | Which warrant this row was captured under. |
| `event_type` | TEXT | NOT NULL | One of `REGISTER`, `DEREGISTER`, `PDU_SESSION_ESTABLISHMENT`, `PDU_SESSION_RELEASE` today. Open-ended for new POIs. |
| `target_imsi` | TEXT | NOT NULL | Denormalised for fast operator queries. |
| `event_data` | TEXT | NOT NULL | JSON blob with the operator-policy payload (see §3). |
| `timestamp` | TEXT | NOT NULL DEFAULT `datetime('now')` | Capture time, not delivery time. |
| `delivered` | INTEGER | NOT NULL DEFAULT 0 | 0 = pending; 1 = MDF acked. The whole "queue" is just `WHERE delivered=0`. |

**Indexes:** `idx_li_iri_warrant(warrant_id)`,
`idx_li_iri_ts(timestamp)`.

### 1.3 `li_cc_sessions` — CC activation metadata + X3 delivery state

| Column | Type | Constraints | Purpose |
|--------|------|-------------|---------|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | |
| `warrant_id` | TEXT | NOT NULL FK → `li_warrants` | Which warrant authorised this CC session. |
| `target_imsi` | TEXT | NOT NULL | Denormalised. |
| `session_type` | TEXT | NOT NULL DEFAULT `'data'` | `data` (PDU session) or `voice` (IMS call) — voice is reserved; only `data` is wired today. |
| `pdu_session_id` | INTEGER | NULLable | The 5GSM PDU session identifier. |
| `call_id` | TEXT | NULLable | IMS Call-ID for the future voice path. |
| `status` | TEXT | NOT NULL DEFAULT `'active'` | `active` while the PDU session is up; `stopped` once `DeactivateCC` runs. |
| `started_at` | TEXT | NOT NULL DEFAULT `datetime('now')` | Activation time. |
| `cc_opened_delivered` | INTEGER | NOT NULL DEFAULT 0 | 0 = OPENED phase still owed to the MDF; 1 = X3 worker delivered it. Added by `ensureColumn`. |
| `cc_closed_delivered` | INTEGER | NOT NULL DEFAULT 0 | 0 = CLOSED phase still owed (only meaningful once `status='stopped'`); 1 = X3 worker delivered it. |

**Index:** `idx_li_cc_sessions_warrant_id(warrant_id)`.

The two delivery flags are tracked independently so a session that
opens and closes inside a single X3 tick gets both events delivered;
the X3 deliverer (§5) flips each flag on its own POST.

The per-packet content fork is still out of scope (`li.md` §8); this
table records that the network was *configured* to intercept and
that the lifecycle phases reached the MDF — the byte stream itself
is the next piece.

### 1.4 `li_audit_log` — the regulator's read-only trail

| Column | Type | Constraints | Purpose |
|--------|------|-------------|---------|
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | Monotone insertion order. |
| `action` | TEXT | NOT NULL | Vocabulary today: `warrant_created`, `warrant_revoked`, `warrant_expired`, `warrant_deleted`, `iri_captured`, `cc_activated`, `cc_deactivated`, `x1_provision`, `x1_modify`, `x1_deactivate`, `x2_delivered`, `x3_delivered`. Open-ended; new POI / handover events extend the vocabulary. |
| `warrant_id` | TEXT | NULLable (system actions can have no warrant) | Cross-reference; FK is **not** declared so an audit row survives a `DeleteWarrant`. |
| `operator` | TEXT | NOT NULL | Identity that performed the action. `system` for tick-driven actions (`warrant_expired`, `iri_captured`). |
| `detail` | TEXT | NULLable | Free-text annotation (e.g. "target=imsi-… scope=iri+cc"). |
| `timestamp` | TEXT | NOT NULL DEFAULT `datetime('now')` | When the action ran. Lex-comparable with the rest of the LI tables. |

**Index:** `idx_li_audit_ts(timestamp)`.

**Append-only invariant:** the application code never UPDATEs or
DELETEs from `li_audit_log`. `DeleteWarrant` removes the matching
`li_warrants` / `li_iri_events` / `li_cc_sessions` rows but leaves
the audit trail intact, so a regulator can prove the warrant existed
even after operator-side cleanup. The DDL has no `WITHOUT ROWID`
trick or trigger guard today; the invariant is "the application is
the only writer; the application never rewrites".

---

## 2. The hot-path cache

The capture path runs inside the AMF/SMF dispatch loops. A SQL
lookup per UE event would be the wrong shape — non-targeted UEs
would all pay the cost. The cache solves that:

```go
var (
    targetMu      sync.Mutex
    activeTargets = make(map[string][]WarrantTarget)
)

type WarrantTarget struct {
    WarrantID   string  // for auditing the capture
    Scope       string  // routes IRI vs. CC
    MDFEndpoint string  // X2/X3 push target
}
```

- **Key:** `target_imsi`. One IMSI can have multiple active warrants
  (different LEAs, different scopes), so the value is a slice.
- **Population:** `refreshTargets()` runs:
  - On every successful `CreateWarrant` / `RevokeWarrant` /
    `ExpireWarrants` / `DeleteWarrant`.
  - On every external tick that calls the exported `RefreshTargets`.
  - Once at process startup (no-op until rows exist).
- **Filter:** rebuild query is
  `status='active' AND start_time <= now AND end_time > now`. So
  warrants outside their window never appear in the cache, even if
  `status` hasn't been flipped yet.
- **Snapshot semantics:** `GetWarrantForIMSI` returns a **copy** of
  the slice so callers can iterate without holding the mutex.

The dirty-read tradeoff: between the DB write and `refreshTargets`
returning, an event for the IMSI could fall on either side of the
window boundary. For the capture-then-stop direction (revoke), the
~tick window where the cache is stale is the only path that could
spuriously capture. The audit trail records both the revoke and the
capture so the regulator sees what happened; the operator can
revoke earlier if a hard cut is required.

---

## 3. Wire-encoding decisions

### 3.1 IRI payload — JSON today, ASN.1 deferred

`li_iri_events.event_data` carries a JSON blob shaped by the POI
that emitted the event:

```jsonc
// REGISTER (AMF, TS 33.128 §6.2.1)
{
  "event":         "registration_complete",
  "amf_ue_ngapid": 1,
  "gnb":           "172.30.0.30"
}

// PDU_SESSION_ESTABLISHMENT (SMF, TS 33.128 §6.2.2)
{
  "event":          "pdu_session_established",
  "pdu_session_id": 1,
  "dnn":            "internet",
  "ipv4":           "10.45.0.3",
  "ipv6":           "",
  "upf_id":         "default-upf"
}
```

The decision: **stage-3 ASN.1 (TS 33.128) is deferred**. The local
spec PDFs for 33.127 / 33.128 are not loaded; rather than guess at
the wire shape, we ship a JSON envelope keyed on the operator-
meaningful fields. The cost is "an external LEMF expecting ASN.1
needs a reformatter on the receive side" — an in-house MDF that
speaks JSON works end-to-end today.

### 3.2 CC delivery — lifecycle phases now, per-packet roadmap

`li_cc_sessions` records that CC was activated; the X3 deliverer
ships an `OPENED` event when the row is inserted (PDU session
establish under a cc/iri+cc warrant) and a `CLOSED` event when the
row's `status` flips to `stopped` (PDU session release). MDF
integrations get a real CC channel today, just one that carries
lifecycle phases instead of frame content.

The per-packet content fork is the next piece. When UPF dataplane
work lands, it will publish frames onto the **same** X3 channel
alongside the existing OPENED/CLOSED events; MDF integrations
built against today's wire keep working without changes.

### 3.3 X1 / X2 / X3 transports — wired

| Spec § | What it does | Wire today | Notes |
|--------|--------------|------------|-------|
| TS 33.127 §6.2 X1 (ADMF→POI) | Provision / modify / deactivate warrants. | HTTPS POST + JSON to `/api/li/x1/{provision,modify,deactivate/{id}}`. | Same `requireLIAuth` token gate as the rest of the surface. Lays an `x1_*` audit row alongside the underlying `warrant_*`. |
| TS 33.127 §6.3 X2 (POI→MDF, IRI) | Push captured IRI events to the MDF. | HTTPS POST + JSON batch to `{warrant.mdf_endpoint}/x2/iri`. | Background goroutine drains `li_iri_events.delivered=0`; HTTP 2xx flips the batch and lays `x2_delivered`. |
| TS 33.127 §6.4 X3 (POI→MDF, CC) | Push CC channel events to the MDF. | HTTPS POST + JSON to `{warrant.mdf_endpoint}/x3/cc`. | Carries `OPENED` / `CLOSED` lifecycle phases today (one HTTP request per phase, sequenced by row id). Per-packet frames roadmap. |

Common shape across all three:

- **Header marker** — every X2 / X3 request carries
  `X-LI-Reference-Point: X2` or `X3` so the MDF can route on a
  single hostname if the operator runs one.
- **Idempotency** — the body carries `sequence` (the
  `li_iri_events.id` or `li_cc_sessions.id`) so the MDF can
  deduplicate retries.
- **Failure mode** — non-2xx leaves the row pending. The
  background loop retries on the next tick. The queue is the
  buffer (TS 33.127 §6.3).
- **Off by default** — `network_config.li_x2_enabled` and
  `li_x3_enabled` ship as `0`. A fresh deployment cannot push
  until the operator enables them.

What is **still deferred**:

- **mTLS** — the operator can put `https://` in `mdf_endpoint`,
  but client-cert authentication is not enforced on the deliverer
  side yet (TS 33.127 §6.5).
- **TS 33.128 ASN.1 stage-3 envelope** — JSON until the spec
  PDFs land.
- **UPF per-packet content fork on X3** — separate work item
  that will publish frames onto the existing X3 channel.

---

## 4. Access control + LI subsystem knobs

Four LI knobs live on the `network_config` singleton — all
DB-driven so an operator can rotate the token and toggle the X2 /
X3 deliverers without a binary restart:

```sql
-- Auth gate (TS 33.127 §5.2)
li_auth_token            TEXT    NOT NULL DEFAULT ''
-- X2 / X3 deliverer toggles (TS 33.127 §6.3 / §6.4) — off by
-- default so a deployment without configured MDFs cannot
-- accidentally exfiltrate.
li_x2_enabled            INTEGER NOT NULL DEFAULT 0
li_x3_enabled            INTEGER NOT NULL DEFAULT 0
-- Cadence shared by both deliverers (poll interval).
li_mdf_poll_interval_ms  INTEGER NOT NULL DEFAULT 1000
```

All four are added via `db/engine/schema.go::ensureColumn` so
existing dev DBs upgrade in place. The
`saveNetworkConfig` whitelist in `webservice/app/network_config.go`
includes them, so the standard `POST /api/network-config` route
patches them under the same auth as the rest of the network panel.

**Empty-token semantics:** if the stored value is `''` the gate is
*open* — any caller passes. This is intentional for dev / CI / first
boot before the operator rotates a real token. Operations:

```sql
UPDATE network_config SET li_auth_token = ? WHERE id = 1;
```

The middleware (`webservice/app/routes_li_auth.go`) uses
`crypto/subtle.ConstantTimeCompare` so timing channels do not leak
the secret on a brute-force probe.

**Operator identity** is **separate** from the auth token. Even an
authenticated request must carry a per-operator marker — the
middleware reads `X-LI-Operator` header > BasicAuth user >
RemoteAddr. The route layer plumbs that string through to
`li.CreateWarrant` / `RevokeWarrant` / `DeleteWarrant`, which
inserts it into the `li_audit_log.operator` column.

---

## 5. Code map (where each surface lives)

```
security/li/                           ←  the operator/POI surface
├── li.go                              ─  core public API
│    ADMF surface ......................  CreateWarrant / RevokeWarrant /
│                                          DeleteWarrant / ExpireWarrants /
│                                          ListWarrants / GetWarrant /
│                                          GetAuditLog / Status
│    Hot-path lookup ...................  GetWarrantForIMSI (cache-served)
│    Cache rebuild .....................  refreshTargets / RefreshTargets
│    IRI POI ...........................  CaptureIRI / GetIRIEvents /
│                                          MarkDelivered
│    CC POI ............................  ActivateCC / DeactivateCC /
│                                          CheckAndActivateCC /
│                                          GetActiveCCSessions
│    Audit + introspection .............  audit (internal) / List
├── x1.go                              ─  TS 33.127 §6.2 ADMF→POI verbs
│                                          X1Provision / X1Modify /
│                                          X1Deactivate (façade over CRUD
│                                          + role-named audit rows)
├── x2.go                              ─  TS 33.127 §6.3 IRI deliverer
│                                          StartX2 / StopX2 / Deliverer
│                                          loop, X2Event/X2Batch envelopes,
│                                          loadX2Config()
├── x3.go                              ─  TS 33.127 §6.4 CC deliverer
│                                          StartX3 / StopX3, X3Event,
│                                          OPENED/CLOSED phase pump
├── lifecycle.go                       ─  StartExpireTicker / StopExpireTicker
│                                          (periodic ExpireWarrants sweep)
└── li_test.go                         ─  Go unit tests for warrant lifecycle,
                                          scope filter, expiry, stats

db/schemas/domains.go ::LiDDL          ─  the four tables + indexes
db/schemas/network.go                  ─  network_config.li_* DDL
                                          (li_auth_token, li_x2_enabled,
                                           li_x3_enabled,
                                           li_mdf_poll_interval_ms)
db/engine/schema.go::applyColumnAdditions
                                       ─  idempotent ensureColumn migration
                                          (network_config.li_* +
                                           li_cc_sessions.cc_*_delivered)

webservice/app/routes_li.go            ─  /api/li/* + /api/li/x1/* route
                                          bindings (extracted from
                                          routes_nsaas.go in 2026-05;
                                          per-domain pattern)
webservice/app/routes_li_auth.go       ─  requireLIAuth middleware +
                                          liOperatorFromRequest helper
webservice/app/network_config.go       ─  saveNetworkConfig whitelist for
                                          the four LI knobs

webservice/cmd/sacore-web/main.go      ─  li.StartX2 / li.StartX3 /
                                          li.StartExpireTicker at boot;
                                          lifecycle.Register stop hooks
                                          drain the goroutines on
                                          graceful shutdown.

nf/amf/gmm/fsm_actions.go              ─  AMF POI hooks
                                          actOnRegistrationComplete    → REGISTER
                                          actFinaliseDeregistration    → DEREGISTER

nf/smf/session/establish.go            ─  SMF POI hooks
                                          Establish (success branch)   → PDU_SESSION_ESTABLISHMENT
                                                                         + CheckAndActivateCC
                                          ReleaseWithCause             → PDU_SESSION_RELEASE
                                                                         + DeactivateCC for active warrants
```

---

## 6. Read paths the OAM panel relies on

| Endpoint | Backing query | Notes |
|----------|---------------|-------|
| `GET /api/li/warrants[?status=]` | `SELECT * FROM li_warrants ORDER BY created_at DESC` (filtered) | Catalog list. |
| `GET /api/li/warrant/{id}/iri[?limit=]` | `SELECT * FROM li_iri_events WHERE warrant_id=? ORDER BY timestamp DESC, id DESC LIMIT ?` | Newest-first; default limit 200. |
| `GET /api/li/cc-sessions[?imsi=]` | `SELECT * FROM li_cc_sessions WHERE status='active' [AND target_imsi=?]` | Only active sessions. |
| `GET /api/li/audit[?warrant_id=&limit=]` | `SELECT * FROM li_audit_log [WHERE warrant_id=?] ORDER BY timestamp DESC, id DESC LIMIT ?` | The regulator-facing surface. |
| `GET /api/li/stats` | `COUNT(*)` on `li_warrants WHERE status='active'` + delivery counts | Drives the dashboard headline. |

All five are gated by `requireLIAuth` (token check + operator
identity capture).

---

## 7. Write paths and audit hooks

| Operation | Tables touched | Audit row inserted |
|-----------|----------------|--------------------|
| `CreateWarrant` | INSERT `li_warrants` | `warrant_created` |
| `RevokeWarrant` | UPDATE `li_warrants.status='revoked'` | `warrant_revoked` |
| `ExpireWarrants` (per id flipped) | UPDATE `li_warrants.status='expired'` | `warrant_expired` |
| `DeleteWarrant` | DELETE `li_iri_events` + `li_cc_sessions` + `li_warrants` | `warrant_deleted` (inserted **first**, before the cascade) |
| `X1Provision` | (delegates to `CreateWarrant`) | `x1_provision` (in addition to `warrant_created`) |
| `X1Modify` | UPDATE `li_warrants` (scope / end_time / mdf_endpoint as supplied) | `x1_modify` |
| `X1Deactivate` | (delegates to `RevokeWarrant`) | `x1_deactivate` (in addition to `warrant_revoked`) |
| `CaptureIRI` | INSERT `li_iri_events` (per matching warrant) | `iri_captured` (per matching warrant) |
| `ActivateCC` | INSERT `li_cc_sessions` | `cc_activated` |
| `DeactivateCC` | UPDATE `li_cc_sessions.status='stopped'` | `cc_deactivated` |
| `MarkDelivered` | UPDATE `li_iri_events.delivered=1 WHERE id<=?` | (no audit row — ack-only operation) |
| X2 deliverer tick (one batch / warrant) | UPDATE `li_iri_events.delivered=1 WHERE warrant_id=? AND id<=?` | `x2_delivered` (per warrant batch, with `count`/`max_id`/`mdf` detail) |
| X3 deliverer tick (per warrant) | UPDATE `li_cc_sessions.cc_opened_delivered=1` / `cc_closed_delivered=1` | `x3_delivered` (per warrant batch, with `opened`/`closed` counts) |

Each `WriteWarrant`-equivalent path (including `X1Modify`) also
calls `refreshTargets()` so the next NF event sees the new state.

---

## 8. Retention and growth assumptions

The four LI tables are **unbounded** in the current build:

- `li_warrants` — operator-controlled count; small (tens to
  hundreds per active operator).
- `li_iri_events` — grows with capture activity. A single targeted
  UE can produce on the order of dozens of events per day.
  Operator should snapshot + trim periodically.
- `li_cc_sessions` — bounded by active PDU sessions of targeted UEs.
- `li_audit_log` — append-only, **never trimmed by application
  code**. A regulator audit may demand the full history; the
  archive policy is operator + regulator agreement, out of scope
  for the application.

Practical advice: production deployments should run `VACUUM` after
operator-driven `DeleteWarrant` batches, and archive `li_audit_log`
via filesystem-level snapshots rather than SQL DELETE.

---

## 9. Migration history

| Migration | What | Where |
|-----------|------|-------|
| Initial DDL | Four tables + four indexes | `db/schemas/domains.go::LiDDL` |
| Timestamp format consolidation | Float64 epoch → ISO TEXT for every write | `security/li/li.go::nowISO` + every INSERT path |
| `network_config.li_auth_token` | Added via `ensureColumn` (idempotent) | `db/engine/schema.go::applyColumnAdditions` + `db/schemas/network.go` |
| `DeleteWarrant` + `/api/li/warrant/{id}/delete` | Hard-delete path for OAM purge + test fixtures | `security/li/li.go` + `webservice/app/routes_li.go` (was `routes_nsaas.go`) |
| X2 / X3 deliverer toggles | `network_config.li_x2_enabled`, `li_x3_enabled`, `li_mdf_poll_interval_ms` (idempotent `ensureColumn`) | `db/engine/schema.go::applyColumnAdditions` + `db/schemas/network.go` |
| X3 per-row delivery flags | `li_cc_sessions.cc_opened_delivered`, `cc_closed_delivered` (idempotent `ensureColumn`) | `db/engine/schema.go::applyColumnAdditions` |

No destructive migrations have ever shipped — every schema change
adds a column or a new table. Existing operator DBs upgrade in
place.

---

## 10. Where the spec deferrals show up

The X1 / X2 / X3 wires are now in scope (TS 33.127 §6.2 / §6.3 /
§6.4 — see §3.3 above). What remains deferred is the encoding +
hardening layer the local PDF set does not document. Each entry
below is anchored by a `TODO(spec: …)` marker in the source so
readers and `speccheck` see the same picture:

| File / line | Spec target | Surface deferred |
|-------------|-------------|------------------|
| `security/li/li.go:17` | TS 33.127 architecture | ADMF / POI / TF / MDF role split (collapsed in-process today; `/api/li/x1/*` lets a future remote ADMF point at this surface). |
| `security/li/li.go:28` | TS 33.128 stage 3 | IRI-EVENT-RECORD / CC-PDU ASN.1 encodings (the wire is JSON until the PDFs land). |
| `security/li/li.go:31` | TS 33.127 buffering | MDF replay across long outage windows (today the `delivered=0` queue grows; no rotation yet). |
| `security/li/x3.go` (file-level) | TS 33.127 §6.4 X3 content fork | Per-packet UPF→MDF stream. Today X3 carries OPENED / CLOSED lifecycle phases only. |
| `routes_li_auth.go` (file-level) | TS 33.127 §6.5 ADMF mTLS | Token-gate substitutes for the full mTLS chain. |

When a TS 33.127 / 33.128 PDF lands in the local spec set, each
deferral becomes a concrete implementation ticket.

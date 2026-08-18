# SEPP — Security Edge Protection Proxy (TS 29.573 + TS 33.501 §13.1)

The PLMN-border proxy that fronts every inter-PLMN SBI request. The
**proxy itself** (`infra/roaming/sepp`) is a transparent N32-f
reverse proxy with TLS termination. The **operator policy** that
governs which peers may talk N32, what topology details they're
allowed to see, and what gets audit-logged lives at
`security/sepp_policy` and is exposed at `/api/sepp/*`.

# Part A — Functional

## A.1 Why SEPP?

5G SBI is HTTP/2 between Network Functions. When a roaming partner's
NF (e.g. AUSF in a visited network) calls our UDM, it doesn't connect
directly — it hits **our** SEPP, which terminates the partner's TLS,
applies our policy (allow-list / topology hiding / audit), and only
then forwards the call to the destination NF. Without that proxy, the
partner could see internal NF FQDNs, callback URLs, and mDNS records
that violate operator topology-hiding policy (TS 29.573 §5.3.x) and
breach the security architecture (TS 33.501 §13.1).

Two layers:

1. **Plumbing** — `infra/roaming/sepp/sepp.go` is a `net/http` reverse
   proxy that consumes the `3gpp-Sbi-Target-apiRoot` header to route.
   It terminates TLS (or runs plaintext in dev mode). No policy.
2. **Policy** — this surface. The proxy *consults* this layer's
   `CheckPeerAccess(plmn, path)` on every inbound request and reads
   the topology-hiding rules during forwarding.

The policy state survives restarts (DB-backed); the proxy is
stateless.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **N32-c** | SEPP ↔ peer SEPP | TLS handshake + capability exchange | TS 29.573 §5.2 | TLS termination at the proxy; capability set (peer FQDN/SAN) lives in `sepp_peers`. |
| **N32-f** | SEPP ↔ peer SEPP | HTTPS reverse proxy | TS 29.573 §5.3 | Proxy in `infra/roaming/sepp`; topology hiding governed by `sepp_topology_hiding`. |
| **SBI border** | NF → SEPP | TLS mutual-auth | TS 33.501 §13.1 | Peer SAN drives the allow-list; default-deny on unknown PLMN. |
| **Operator panel** | Operator → 5GC | REST | OAM-internal | `/api/sepp/*` (this file). |

## A.3 Operator-visible behaviours

### A.3.1 Default-deny on the allow-list

`CheckPeerAccess(plmn_id, path)` returns
`{allowed: false, reason: "unknown peer (default-deny)"}` for any
PLMN-id not in `sepp_peers`. The allow-list pairs each peer with its
canonical FQDN and (optional) SAN — the proxy's TLS termination
verifies the cert; this layer authorises the holder of that cert.

### A.3.2 Status gates admission too

Even a known peer is rejected if its `status ∈ {inactive, blocked}`.
Operators flip status without dropping the row so the audit trail
(per-peer `created_at` and per-row `updated_at`) is preserved.

### A.3.3 Path filter

Each peer carries an optional CSV `allowed_paths`. Empty = all paths
allowed (the common case). Non-empty enforces a **prefix-match**
against the request path so operators can allow `/nudm-uecm/v1`
without listing every sub-path. Mismatch returns `403`-equivalent
denial and logs `action='rejected'`.

### A.3.4 Topology-hiding rules (TS 29.573 §5.3.x)

Per-peer rule (`sepp_topology_hiding`, 1:1 with peer):

| Field | Effect |
|---|---|
| `hide_internal_fqdn` | rewrite Host / Authority that point at internal NF FQDNs |
| `hide_callbacks`     | rewrite callback URLs in request bodies |
| `replace_fqdn`       | what to replace with — typically `sepp.our-network.example` |
| `strip_headers`      | CSV of operator-internal headers to drop (`x-internal-ip`, `x-debug`, …) |

A peer with no rule falls back to the implicit "hide everything"
default per §5.3.x, but the explicit rule lets operators relax it
selectively for trusted partners.

### A.3.5 N32 audit log

Every `CheckPeerAccess` call writes a `sepp_n32_log` row with
`action ∈ {forwarded, rejected, rewritten}` and the rejection reason.
Operators can filter the log by peer, action, or direction. The log
itself is also writable from the OAM panel (`POST /log`) for drills
and synthetic tests.

## A.4 Operator REST API (`/api/sepp/*`)

### A.4.1 Status / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/sepp/status` | Policy stats + proxy runtime (`{total_peers, active_peers, …, proxy: {status, addr, tls}}`). |
| GET | `/api/sepp/stats` | Policy stats only. |

### A.4.2 Peer-PLMN allow-list (TS 33.501 §13.1)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/sepp/peers?status=active` | List, optional status filter. |
| POST   | `/api/sepp/peers` | Add — `{plmn_id, fqdn, public_san?, allowed_paths?, status?, description?}`. |
| GET    | `/api/sepp/peers/{id}` | One peer; 404 on miss. |
| PATCH  | `/api/sepp/peers/{id}` | Sparse update (`fqdn / public_san / allowed_paths / status / description`). |
| DELETE | `/api/sepp/peers/{id}` | CASCADE drops the topology-hiding rule. |

### A.4.3 Topology-hiding rules (TS 29.573 §5.3.x)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/sepp/topology-hiding` | List every per-peer rule. |
| POST   | `/api/sepp/topology-hiding` | UPSERT — `{peer_id, hide_internal_fqdn?, hide_callbacks?, replace_fqdn?, strip_headers?}`. |
| GET    | `/api/sepp/topology-hiding/{peer_id}` | One rule; 404 → fallback to default policy. |
| DELETE | `/api/sepp/topology-hiding/{peer_id}` | Drop — peer falls back to implicit-default. |

### A.4.4 Admission gate + audit log

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/sepp/check-access` | `{plmn_id, path}` → `{access: {allowed, reason, peer_id?, status?}}`. |
| GET  | `/api/sepp/log?peer=&action=&direction=&limit=` | Newest first; filterable. |
| POST | `/api/sepp/log` | Synthetic raise — drills + alert tests. |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| Wire `infra/roaming/sepp/sepp.go::proxyHandler` to call `CheckPeerAccess` | TS 29.573 §5.2 | Policy enforced via /api/sepp surface today; the proxy still default-allows whatever survives TLS. |
| TS 29.573 §5.3 PRINS (Roaming Information Stripping) | — | Topology-hiding policy stored; rewrite at the proxy is TODO. |
| TS 29.573 §5.4 Hosted Universal Public Key Infrastructure | — | TLS cert verification at the proxy uses local trust store; no operator UI for the hosted CA today. |
| Per-peer rate limit | — | `core_security` rate limiter exists but isn't keyed on peer_plmn yet; would slot in cleanly. |

---

# Part B — Design

## B.1 Process layout

```
                     ┌─────────────────────────────────┐
                     │ Peer NF (visited PLMN)          │
                     │  → HTTPS via N32-f (TS 29.573)  │
                     └──────────────┬──────────────────┘
                                    │ TLS terminates at the proxy
                                    ▼
                     ┌─────────────────────────────────┐
                     │ infra/roaming/sepp (proxy)      │
                     │  ├ proxyHandler (transparent)   │
                     │  ├ filter sensitive headers     │
                     │  └ TODO: CheckPeerAccess(plmn,  │
                     │           path) admission       │
                     └──────────────┬──────────────────┘
                                    │
                     ┌──────────────▼──────────────────┐
                     │ security/sepp_policy            │
                     │  ├ CreatePeer / GetPeerByPLMN   │
                     │  ├ UpsertTopologyHiding         │
                     │  ├ CheckPeerAccess              │
                     │  └ LogN32                       │
                     └──────────────┬──────────────────┘
                                    │ persist
                                    ▼
                     ┌─────────────────────────────────┐
                     │ sepp_* tables (SQLite)          │
                     │  ├ sepp_peers                   │
                     │  ├ sepp_topology_hiding (FK PEER)│
                     │  └ sepp_n32_log                 │
                     └─────────────────────────────────┘
                                    ▲
                                    │ JSON / HTTP
                                    │
                     webservice/app/routes_sepp.go
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `security/sepp_policy/sepp_policy.go` | ~430 | Peer / topology-hiding / log CRUD; `CheckPeerAccess` admission verb. |
| `db/schemas/sepp.go` | 70 | Three `sepp_*` tables + indexes. |
| `webservice/app/routes_sepp.go` | ~280 | REST surface for §A.4 (this file). |
| `infra/roaming/sepp/sepp.go` | 226 | Transparent N32-f proxy. Unchanged in this vertical (admission wiring TODO). |

Tests:
- `mmt_studio_core_tester/src/testcases/security/tc_sepp.py` — 8 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE sepp_peers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  plmn_id TEXT NOT NULL UNIQUE,
  fqdn TEXT NOT NULL,
  public_san TEXT,
  allowed_paths TEXT NOT NULL DEFAULT '',          -- CSV; empty = all
  status TEXT NOT NULL DEFAULT 'active'
         CHECK (status IN ('active','inactive','blocked')),
  description TEXT NOT NULL DEFAULT '',
  created_at TEXT, updated_at TEXT
);

CREATE TABLE sepp_topology_hiding (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  peer_id INTEGER NOT NULL UNIQUE
          REFERENCES sepp_peers(id) ON DELETE CASCADE,
  hide_internal_fqdn INTEGER NOT NULL DEFAULT 1,
  hide_callbacks INTEGER NOT NULL DEFAULT 1,
  replace_fqdn TEXT NOT NULL DEFAULT '',
  strip_headers TEXT NOT NULL DEFAULT '',         -- CSV
  updated_at TEXT
);

CREATE TABLE sepp_n32_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  peer_plmn TEXT NOT NULL DEFAULT '',
  direction TEXT NOT NULL CHECK (direction IN ('inbound','outbound')),
  path TEXT NOT NULL, method TEXT NOT NULL DEFAULT '',
  status_code INTEGER NOT NULL DEFAULT 0,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  action TEXT NOT NULL DEFAULT 'forwarded'
         CHECK (action IN ('forwarded','rejected','rewritten')),
  reason TEXT NOT NULL DEFAULT '',
  created_at TEXT
);
```

`peer_id` is `UNIQUE NOT NULL` on `sepp_topology_hiding` so the
relationship is 1:1 — at most one rule per peer; UPSERT replaces.
The CASCADE on peer-delete keeps the rule table free of orphans.

## B.4 CheckPeerAccess algorithm

```
CheckPeerAccess(plmn_id, path):
  if plmn_id is empty:                  reject "empty plmn"
  peer = lookup sepp_peers by plmn_id
  if peer is nil:                       reject "unknown peer (default-deny)"
  if peer.status != 'active':           reject "peer <status>"
  if peer.allowed_paths is non-empty:
    if not pathInList(path, peer.allowed_paths):
      reject "path not in allowed_paths"
  return Allowed{ peer_id, status }
```

Every reject path also writes an `action='rejected'` row to
`sepp_n32_log`; the allow path writes nothing (the proxy will write
`forwarded` / `rewritten` after handling the request).

## B.5 Public API

```go
// Peers
type Peer struct { ID; PlmnID; FQDN; PublicSAN; AllowedPaths;
                   Status; Description; CreatedAt; UpdatedAt }
func CreatePeer(p Peer) (*Peer, error)
func GetPeer(id int64) (*Peer, error)
func GetPeerByPLMN(plmnID string) (*Peer, error)
func ListPeers(status string) ([]Peer, error)
func UpdatePeer(id int64, fields map[string]interface{}) (*Peer, error)
func DeletePeer(id int64) (bool, error)

// Topology hiding
type TopologyHiding struct { ID; PeerID; HideInternalFQDN;
                             HideCallbacks; ReplaceFQDN;
                             StripHeaders; UpdatedAt }
func UpsertTopologyHiding(t TopologyHiding) (*TopologyHiding, error)
func GetTopologyHiding(peerID int64) (*TopologyHiding, error)
func ListTopologyHiding() ([]TopologyHiding, error)
func DeleteTopologyHiding(peerID int64) error

// Admission
type AccessResult struct { Allowed; Reason; PeerID; Status }
func CheckPeerAccess(plmnID, path string) AccessResult

// Audit log
func LogN32(peerPLMN, direction, path, method string,
            statusCode, latencyMs int, action, reason string)
func ListN32Log(peer, action, direction string, limit int) ([]map[string]any, error)

// Stats / vocabulary
func GetStats() map[string]any
func ValidStatus(s string) bool

// Routes
func (s *Server) registerSEPPRoutes()
```

## B.6 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-SEPP-001 `sepp_peer_crud`                | add → list → get → patch (status flip) |
| TC-SEPP-002 `sepp_peer_validation`          | missing plmn_id / fqdn / bad status / bad filter → 400 |
| TC-SEPP-003 `sepp_check_access_default_deny`| unknown PLMN denied; empty plmn_id → 400 |
| TC-SEPP-004 `sepp_check_access_allow`       | active + allowed-path admit; disallowed path deny; blocked status flips to deny |
| TC-SEPP-005 `sepp_topology_hiding`          | upsert + readback + bad peer_id → 400; FK CASCADE on peer delete |
| TC-SEPP-006 `sepp_n32_log`                  | bad direction / missing path → 400; raise + readback + filter |
| TC-SEPP-007 `sepp_status`                   | policy stats + proxy runtime fields all present |
| TC-SEPP-008 `sepp_log_on_unknown_peer`      | check-access on unknown PLMN writes a `rejected` log row |

All eight wired into `tc_sepp.py::ALL_SEPP_TCS` and pass against the
current core build.

## B.7 References

- **TS 29.573** §5.2 — N32-c control plane.
- **TS 29.573** §5.3 — N32-f forwarding plane (topology hiding).
- **TS 29.573** §5.4 — Hosted UPKI (TODO; not loaded locally).
- **TS 33.501** §13.1 — 5GC SBI security at the PLMN border.
- **TS 23.501** §5.36 — Roaming architecture.
- `docs/design/security/core_security.md` — sibling firewall + IDS surface.
- `docs/design/security/npn.md` — sibling NPN admission surface.
- `infra/roaming/sepp/sepp.go` — the proxy plumbing (data plane).

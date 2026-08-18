# Core Network Security — Signalling Firewall + IDS + Audit (TS 33.501 §5.9 / §9.x)

The 5G core signalling-perimeter layer (`security/core_security`) and
its operator REST surface (`/api/security/*`). Owns the firewall
rules, IDS signatures, blocked-IP list, known-gNB allow-list, and the
immutable audit log. **Not** the cryptographic layer — NAS/RRC/UP
ciphering and key derivation live in `nf/amf/security` and below.

# Part A — Functional

## A.1 Why core_security?

The 5G security architecture (TS 33.501) has two orthogonal concerns at
the core network:

1. **Signalling perimeter + monitoring** (§5.9 / §9.x) — trust
   boundaries, anti-flood, intrusion detection, audit.
2. **Cryptographic protection of NAS/RRC/UP** (§6 / §9.x) — NEA / NIA
   algorithm selection, key derivation, MAC-I verification.

This vertical implements (1). It's the IP-perimeter and
monitoring surface — operators land here to manage firewall rules,
add IDS signatures, ban an attacker IP, allow-list a new gNB, or
read the 24h security audit summary. The cryptographic layer is
managed via `/api/security-algorithms` (a separate, NF-internal
surface in `operations_route.go`).

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **Signalling guard** | gNB / consumer NF → 5GC | NGAP / SBI | TS 33.501 §5.9.1 | `CheckSignallingAccess(sourceIP)` — enforced at the NGAP server. |
| **N2 guard** | gNB → AMF | NGAP/SCTP | TS 33.501 §9.2 | `CheckNGAPSource(sourceIP)` — known-gNB allow-list. |
| **N3 guard** | gNB → UPF | GTP-U | TS 33.501 §9.3 | `CheckGTPUPacket(teid, src, dst, size)` — TEID + oversize. |
| **IDS** | NF → audit log | in-process | TS 33.501 §5.9.4 | `DetectIntrusion` + `security_ids_signatures` table. |
| **OAM panel** | Operator → 5GC | REST | OAM-internal | `/api/security/*` (this file). |

Cryptographic surfaces (NAS NEA/NIA selection, gNB security caps) are
deferred to TS 33.501 §6 / §9 sub-clauses and live in `nf/amf/security`
— this surface does not enforce them but logs intrusion attempts via
`DetectIntrusion("AUTH_FAILURE_BURST", …)` etc.

## A.3 Operator-visible behaviours

### A.3.1 Trust-boundary state survives restarts

`security_known_gnbs` and `security_blocked_ips` are persisted; the
in-memory caches are rehydrated on boot via
`core_security.LoadPersistedState()`. The hot-path checks
(`CheckNGAPSource`, `IsBlocked`) read from the in-memory map.

### A.3.2 Firewall rules — vocabulary CHECK at every layer

| Field | Allowed values |
|---|---|
| `protocol` | `ngap` / `nas` / `gtpu` / `sbi` / `any` |
| `action`   | `allow` / `deny` / `rate_limit` |

Both the schema CHECK constraint and the route's pre-validator reject
out-of-vocabulary values; the route returns 400 with a human-readable
message instead of letting SQLite's CHECK violation surface as 500.

### A.3.3 IDS signatures — flexible matcher

A signature with `name=eventType OR pattern ⊂ detail` counts as a
hit; the per-source rate bucket (`threshold`/`window_s`) decides
whether the hit promotes to an `INTRUSION_DETECTED` row. Operators
can extend the catalogue at runtime via `POST /ids/signatures` —
no redeploy.

### A.3.4 Audit log — append-only

Every non-trivial event lands in `security_audit_log` with severity
∈ {DEBUG, INFO, WARNING, ERROR, CRITICAL} (CHECK-constrained). The
panel's status aggregator queries by `event_type` (e.g.
`INTRUSION_DETECTED`, `RATE_LIMITED`, `GTPU_BAD_TEID`,
`UNKNOWN_GNB`, `BLOCKED_IP`) to render alert lists, violation
counts, and the GTP-U firewall stats.

### A.3.5 Synthetic raise (drills, operator-initiated)

`POST /api/security/audit` lets operators simulate an event for
drills or alerting tests. Same vocabulary CHECK applies.

## A.4 Operator REST API (`/api/security/*`)

### A.4.1 Status / dashboard

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/security/status` | One-shot panel aggregator: `{ok, ids, rate_limiter, gtpu, audit_summary, summary}`. |
| GET | `/api/security/audit?limit=N` | `{ok, events: [...]}` — newest first. |
| POST | `/api/security/audit` | Synthetic raise: `{event_type, severity?, source_ip?, imsi?, detail?, extra?}`. |
| GET | `/api/security/policies` | Read-only default rate-limit policies (TS 33.501 §9.x interfaces). |
| POST | `/api/security/rate-limit/reset` | Force-clear every rate bucket. |

### A.4.2 Firewall rules (TS 33.501 §9.x)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/security/firewall/rules`         | List rules ordered by priority. |
| POST   | `/api/security/firewall/rules`         | Upsert by name; vocabulary-validated. |
| DELETE | `/api/security/firewall/rules/{name}`  | 404 on unknown. |

### A.4.3 IDS signatures (TS 33.501 §5.9.4)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/security/ids/signatures`         | List enabled-first by name. |
| POST   | `/api/security/ids/signatures`         | Upsert by name; severity ∈ {INFO,WARNING,ERROR,CRITICAL}. |
| DELETE | `/api/security/ids/signatures/{name}`  | 404 on unknown. |

### A.4.4 Trust boundaries (TS 33.501 §5.9.1 / §9.2)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/security/blocked-ips`        | List blocked sources (carries `expires_at`; sweeps expired rows). |
| POST   | `/api/security/blocked-ips`        | `{ip, reason}` → permanent block (audit-logged). |
| POST   | `/api/security/blocked-ips/ttl`    | `{ip, reason, ttl_s}` → block with expiry (TS 33.501 §5.9.4 escalated event). |
| DELETE | `/api/security/blocked-ips/{ip}`   | Lifts the block; audit-logged. |
| GET    | `/api/security/known-gnbs`         | List allow-list. |
| POST   | `/api/security/known-gnbs`         | `{ip, gnb_id}`. |
| DELETE | `/api/security/known-gnbs/{ip}`    | Removes. |

### A.4.5 Firewall packet evaluator + hit counters

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/security/firewall/eval`     | `{protocol, src_ip, dst_ip, port}` → `{action, rule}` after walking rules in priority order. |
| GET    | `/api/security/firewall/hits`     | Per-rule in-memory match counter (`{count, last_ip, last_hit}`). |

### A.4.6 IDS test harness + hit counters

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/security/ids/test`          | `{event_type, source_ip, detail}` → `{detected}` — drives `DetectIntrusion` from the panel. |
| GET    | `/api/security/ids/hits`          | `{hits, alerts}` — raw match counter + post-threshold alert counter. |
| POST   | `/api/security/hits/reset`        | Clears firewall/IDS hit counters and the IDS sliding-window state. |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| TS 33.117 §4.2.3 audit chaining/signing | — | Plain append-only today; no cryptographic chain. |
| TS 33.117 §4.3 hardening checklist | — | Deployment-side, not in this surface. |
| TS 33.501 §6.4 NAS replay rejection wiring | — | `CheckNASReplay` is a stub — wire to AMF security FSM rejection event. |
| TS 23.501 §5.10.3 UP-security policy in GTP-U guard | — | `CheckGTPUPacket` enforces TEID + oversize, not the per-PDU-session integrity-required gate. |
| Blocked-IP TTL countdown | — | TTL stored as `expires_at` and pruned by `SweepBlockedIPs()`; the panel `remaining_sec` is now derivable. |

---

# Part B — Design

## B.1 Process layout

```
                    ┌─────────────────────────────┐
                    │ NF hot path                 │
                    │  ├ CheckSignallingAccess    │ §5.9.1
                    │  ├ CheckNGAPSource          │ §9.2
                    │  ├ CheckGTPUPacket          │ §9.3
                    │  └ DetectIntrusion          │ §5.9.4
                    └─────────────┬───────────────┘
                                  │ in-memory
                                  ▼
                    ┌─────────────────────────────┐
                    │ core_security maps          │
                    │  ├ knownGnBIPs              │
                    │  ├ blockedIPs               │
                    │  └ rlBuckets                │
                    └─────────────┬───────────────┘
                                  │ persist
                                  ▼
                    ┌─────────────────────────────┐
                    │ security_* tables (SQLite)  │
                    │  ├ security_audit_log       │
                    │  ├ security_firewall_rules  │
                    │  ├ security_ids_signatures  │
                    │  ├ security_blocked_ips     │
                    │  └ security_known_gnbs      │
                    └─────────────────────────────┘
                                  ▲
                                  │ JSON / HTTP
                                  │
                       webservice/app/routes_security.go
                       webservice/templates/security.html
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `security/core_security/core_security.go` | ~670 | Firewall, IDS, audit log, rate limiter, gNB allow-list, blocked-IP list. |
| `db/schemas/security.go` | 73 | Five `security_*` tables + indexes. |
| `webservice/app/routes_security.go` | ~445 | REST surface for §A.4 (this file). |
| `webservice/cmd/sacore-web/main.go` | (slice) | Calls `LoadPersistedState()` at boot. |

Tests:
- `mmt_studio_core_tester/src/testcases/security/tc_core_security.py` — 7 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE security_audit_log (
  id, event_type, severity TEXT CHECK(IN(DEBUG,INFO,WARNING,ERROR,CRITICAL)),
  source_ip, imsi, detail, extra_json, created_at
);
CREATE TABLE security_firewall_rules (
  id, name UNIQUE,
  protocol TEXT CHECK(IN(ngap,nas,gtpu,sbi,any)),
  action   TEXT CHECK(IN(allow,deny,rate_limit)),
  src_cidr, dst_cidr, port_range, rate_limit, window_s, enabled, priority, updated_at
);
CREATE TABLE security_ids_signatures (
  id, name UNIQUE, pattern,
  severity TEXT CHECK(IN(INFO,WARNING,ERROR,CRITICAL)),
  threshold, window_s, enabled,
  auto_block_ttl_s INTEGER NOT NULL DEFAULT 0,
  updated_at
);
CREATE TABLE security_blocked_ips (
  ip PRIMARY KEY, reason, added_at, added_by,
  expires_at TEXT  -- NULL = permanent
);
CREATE TABLE security_known_gnbs  (ip PRIMARY KEY, gnb_id, added_at, added_by);
```

## B.4 Public API

```go
// Audit log
func LogEvent(eventType, detail, sourceIP, imsi, severity string,
              extra map[string]interface{})
func GetAuditLog(limit int) ([]map[string]any, error)

// Rate limiter
func CheckRateLimit(key string, maxPerWindow, windowSec int) bool
func ResetRateLimits()

// Firewall
type FirewallRule struct { … }
func UpsertFirewallRule(r FirewallRule) error
func ListFirewallRules() ([]FirewallRule, error)
func DeleteFirewallRule(name string) bool

// IDS — threshold/window enforcement + auto-block
type IDSSignature struct {
    Name, Pattern, Severity string
    Threshold, WindowS      int
    Enabled                 bool
    AutoBlockTTLS           int  // 0 = no auto-block
}
func UpsertIDSSignature(s IDSSignature) error
func ListIDSSignatures() ([]IDSSignature, error)
func DeleteIDSSignature(name string) bool
func DetectIntrusion(eventType, sourceIP, detail string) bool
func SignatureMatches(s IDSSignature, eventType, detail string) bool

// Hit counters
type HitCounter struct { Count int64; LastIP, LastHit string }
func FirewallHits() map[string]HitCounter
func IDSHits() (raw, alerts map[string]HitCounter)
func ResetHits()
func ResetIDSBuckets()

// Firewall evaluator
func EvalFirewall(protocol, srcIP, dstIP string, port int) EvalResult
func ValidateCIDR(cidr string) error
func ValidatePortRange(spec string) error

// Trust boundary
func RegisterKnownGnB(ip, gnbID string)
func UnregisterGnB(ip string)
func ListKnownGnBs() ([]map[string]any, error)   // added in this vertical
func BlockIP(ip, reason string)
func BlockIPWithTTL(ip, reason string, ttl time.Duration)
func UnblockIP(ip string)
func IsBlocked(ip string) bool                   // lazy TTL prune
func ListBlockedIPs() ([]map[string]any, error)  // sweeps before list
func SweepBlockedIPs()
func StartTTLSweeper(interval time.Duration)
func CheckSignallingAccess(sourceIP string) (bool, string)

// Per-interface guards
func CheckNGAPSource(sourceIP string) bool
func CheckGTPUPacket(teid uint32, src, dst string, sz int) bool

// Persistence + status
func LoadPersistedState() error
func Status() map[string]any
func DefaultPolicies() []SecurityPolicy

// Routes
func (s *Server) registerSecurityRoutes()
func (s *Server) registerSecurityHardeningRoutes()
```

## B.5 Test coverage

### Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-SEC-001 `sec_status_shape`                   | aggregator carries ids/rate_limiter/gtpu/audit_summary/summary |
| TC-SEC-002 `sec_firewall_crud`                  | bad protocol → 400; create → list → delete; second delete → 404 |
| TC-SEC-003 `sec_ids_crud`                       | missing pattern / bad severity → 400; CRUD round-trip |
| TC-SEC-004 `sec_blocked_ips`                    | block → list → status reflects → unblock |
| TC-SEC-005 `sec_known_gnbs`                     | register → list → unregister |
| TC-SEC-006 `sec_audit_event`                    | bad severity / missing event_type → 400; valid raise lands in audit log |
| TC-SEC-007 `sec_policies_reset`                 | default policies present; rate-limit reset succeeds |
| TC-SEC-008 `sec_firewall_eval_priority`         | priority order, first match wins; default-allow when no rule applies; hit counter bumps |
| TC-SEC-009 `sec_firewall_input_validation`      | bad src_cidr / port_range / protocol / action → 400 at write time |
| TC-SEC-010 `sec_ids_threshold_suppress_then_alert` | first N-1 hits suppressed; Nth promotes; raw vs alert counters |
| TC-SEC-011 `sec_ids_auto_block_on_trip`         | signature with `auto_block_ttl_s` adds source to deny list with `expires_at` |
| TC-SEC-012 `sec_blocked_ip_ttl_expiry`          | TTL block expires + ListBlockedIPs sweeps it |
| TC-SEC-013 `sec_ids_regex_pattern`              | `/regex/` pattern form matches against detail (TS 33.501 §5.9.4 anomaly detection) |

All thirteen wired into `tc_core_security.py::ALL_CORE_SECURITY_TCS`
and pass against the current core build.

## B.6 References

- **TS 33.501** §5.9 / §5.9.1 / §5.9.4 / §9.2 / §9.3 / §9.9.
- **TS 33.117** §4.2.3 / §4.3 (TODO; not loaded locally).
- **ITU-T X.733** — Alarm vocabulary.
- W3C Trace Context — used by `oam/otel` to bridge intrusion alerts
  to distributed traces (sibling vertical, see `docs/design/oam/otel.md`).

# Part C — Firewall packet-evaluation algorithm

## C.1 Algorithm

`EvalFirewall(protocol, srcIP, dstIP, port)` walks
`ListFirewallRules()` (already ordered by `priority ASC, name ASC`)
and returns the first rule whose every dimension matches:

| Dimension  | "match anything" sentinel | Match check |
|------------|---------------------------|-------------|
| `protocol` | `"any"`                   | exact equality otherwise |
| `src_cidr` | empty string              | `net.ParseCIDR(cidr).Contains(net.ParseIP(srcIP))` |
| `dst_cidr` | empty string              | same |
| `port_range` | empty string            | exact `N` or inclusive `lo-hi` |
| `enabled`  | n/a                       | rule must be enabled |

When the first matching rule is found, the evaluator:

1. Bumps an in-memory hit counter for that rule (`{count, last_ip,
   last_hit}`), surfaced at `/api/security/firewall/hits`.
2. Returns `EvalResult{Action, Rule}` where `Action ∈ {allow, deny,
   rate_limit}` is the rule's configured action.

If no rule matches, the evaluator returns `{action: "allow", rule: ""}`
— the default is **permissive**. To get default-deny behaviour, the
operator adds a final low-priority `protocol=any, action=deny,
src_cidr=""` rule (TC-SEC-008 covers this pattern).

## C.2 Validation contract

`UpsertFirewallRule` rejects bad inputs at write time so the eval
path can stay branch-free:

- `protocol ∈ {ngap, nas, gtpu, sbi, any}` — DB CHECK + Go check.
- `action ∈ {allow, deny, rate_limit}` — same.
- `src_cidr` / `dst_cidr` — `""` or parseable by `net.ParseCIDR`. Bare
  IPs are rejected; the rule format is CIDR-only.
- `port_range` — `""` or `"N"` (0 ≤ N ≤ 65535) or `"lo-hi"` with
  `lo ≤ hi` and both in `[0, 65535]`.
- `rate_limit ≥ 0`, `window_s ≥ 0`.

A bad value surfaces as a 400 from the route layer rather than as a
SQLite CHECK 500 or a silent eval-time skip.

## C.3 Hit counter semantics

The hit counters are **in-memory only** — observability for the
panel, not durable state. They reset at process restart. This
matches the panel's existing assumption that "hits per rule since
boot" is the useful metric. (Persistent hit counters would need a
write per packet at NF call sites; that's a hot-path budget we don't
spend.)

# Part D — IDS threshold/window + auto-block

## D.1 The promotion algorithm

`DetectIntrusion(eventType, sourceIP, detail)` runs the persisted
catalogue against the event:

```
for s in ListIDSSignatures():
    if not s.Enabled: continue
    if not SignatureMatches(s, eventType, detail): continue
    bumpIDSHit(s.Name, sourceIP)
    count := recordIDSEvent(s.Name, sourceIP, s.WindowS)  # sliding window
    if count < s.Threshold: continue                       # suppress
    bumpIDSAlert(s.Name, sourceIP)
    LogEvent("INTRUSION_DETECTED", …)
    if s.AutoBlockTTLS > 0:
        BlockIPWithTTL(sourceIP, "IDS auto-block: "+s.Name, s.AutoBlockTTLS)
    promoted = true
return promoted
```

Two counters per signature:

- **raw** (`hits`): every event that matches the signature.
- **alerts**: every event that *promotes* (count-in-window ≥ threshold).

A signature with `threshold=3, window_s=60` will record three raw
hits before the first alert; the panel's `5/10` window-progress UI
can be derived from `raw - alerts × threshold`.

## D.2 The sliding-window data structure

Per `(signature_name, source_ip)` we keep a slice of `time.Time`
event timestamps. On each event:

1. Append `now`.
2. Prune entries older than `now - window_s`.
3. Return `len(slice)`.

This is more storage than a counter but the only way to answer the
"how many in the last N seconds?" question without lying when the
window slides past a burst. Memory cost: O(threshold) per active
attacker × signature pair, with implicit cleanup when the window
empties (the bucket key is left dangling but only a slice header).

## D.3 Pattern matching

`SignatureMatches(s, eventType, detail)` accepts three pattern forms:

- **exact event-type match** — `s.Name == eventType`. Used for
  built-in classes (`AUTH_FAILURE_BURST`, etc.) and operator-named
  signatures.
- **substring** — `strings.Contains(detail, s.Pattern)` (legacy).
- **regex** — when `s.Pattern` starts and ends with `/`, the
  inside is compiled as a Go regular expression and run against
  `detail`. Used for anomaly-style signatures (TS 33.501 §5.9.4).

The regex form is opt-in to keep existing literal patterns working.

## D.4 Auto-block lifecycle

When a signature with `auto_block_ttl_s > 0` promotes, the source IP
is added via `BlockIPWithTTL`:

1. In-memory `blockedIPs[ip] = reason` — `IsBlocked` and
   `CheckSignallingAccess` immediately return blocked.
2. DB row in `security_blocked_ips` with
   `expires_at = now + ttl_s` and `added_by='ids'`.
3. Audit event `IP_BLOCKED_TTL` with `{ttl_s, expires_at}` extras.

Two prune paths:

- **Lazy**: every `IsBlocked` reads `expires_at`; if past, deletes the
  row + memory entry + audits `IP_UNBLOCKED reason="ttl expired"`.
- **Sweeper**: `StartTTLSweeper(30 * time.Second)` (booted from
  `main.go`) calls `SweepBlockedIPs()` every interval, batch-deleting
  expired rows so `ListBlockedIPs()` doesn't grow unbounded between
  hot-path calls.

Both paths emit the same `IP_UNBLOCKED reason="ttl expired"` audit
event so the operator can replay the timeline.

## D.5 Trade-offs

- **In-memory window state** — restart-loss is acceptable: a process
  bounce buys an attacker exactly `window_s` of free re-attempts
  before the new bucket fills. We don't persist the window because
  the throughput cost (write-per-event) outweighs the resilience
  gain for a feature designed to detect *bursts*.
- **No cross-source aggregation** — one attacker controlling N
  source IPs gets N × `threshold` events for free. The right mitigation
  is at the BGP / DDoS layer, not in this surface. Operators can add
  a CIDR-scoped firewall rule to widen the deny radius once an
  IP-fronted campaign is identified.
- **Default-permit firewall** — chosen for operational continuity
  during rollout. A well-known operator pattern is to add a final
  `priority=999, action=deny, protocol=any, src_cidr=""` rule once
  the allow-list is populated; tested by TC-SEC-008.


# Trace — Subscriber & Equipment Trace (TS 32.421 / 32.422 / 32.423)

The 5GC signalling-trace subsystem (`oam/trace`) and its operator
REST surface (`/api/trace/*`). NAS / NGAP / NAS-SM / PFCP / SIP
handlers call `trace.Capture(...)` on every signalling event; when an
operator has activated a session via the GUI, those events are
persisted to `trace_records` and exported on demand.

# Part A — Functional

## A.1 Why Trace?

When a UE registration fails, an IMS call drops, or a PDU session
hangs in PENDING, the operator needs the per-message protocol trail —
not just the structured log. Trace gives them that: a per-IMSI (or
catch-all) signalling capture, depth-tunable (minimum / medium /
maximum), interface-filtered (N1, N2, N4, SIP, …), and exportable
as JSON or XML for offline analysis.

The orthogonal channels are: **PM** (`oam/pm`) for hot-path counter
rates; **FM** (`oam/fm`) for raised faults; **logs** for free-form
text. Trace is the *protocol* slice — it answers "what did the wire
say between this UE and the AMF in the 200ms before the
registration reject?"

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **Trace activation** | Operator → 5GC | REST | TS 32.422 (TODO) | `/api/trace/start` (this file). |
| **Capture** | NF handler → `oam/trace` | in-process | TS 32.423 (TODO) | `trace.Capture` no-op when no session active. |
| **Reporting** | 5GC → operator | REST | TS 32.423 §5.6 | Persisted rows + JSON / XML export. |
| **MDT activation** | OAM → AMF | 5GC-MnS | TS 28.531 (TODO) | Not implemented; deferred to OAM provisioning. |

The PDFs for TS 32.421 / 32.422 / 32.423 / 28.531 are **not** loaded
in `specs/3gpp/`, so the package header tracks them as `TODO(spec:)`
prose-only — speccheck would reject §-form citations.

## A.3 Operator-visible behaviours

### A.3.1 Trace Recording Session (TS 32.421)

A row in `trace_sessions` represents one Trace Recording Session.
Fields: `trace_ref` (PRIMARY KEY), optional `imsi` (catch-all when
empty), `gnb_ip`, `depth ∈ {minimum, medium, maximum}` (TS 32.422
§5.6), `interfaces` (CSV: N1, N2, N4, SIP, …), `duration_sec`,
`status ∈ {active, completed, stopped}`, `started_at`, `stopped_at`,
`record_count`. Both `depth` and `status` are CHECK-constrained at
the schema level; the route returns 400 on bad values rather than
letting SQLite's CHECK violation surface as a 500.

### A.3.2 Catch-all vs targeted sessions

A session with `imsi=""` is a catch-all — `Capture` matches it for
every UE event. A session with a specific IMSI matches only that UE.
At most one active session is in effect at any time per IMSI; in
practice operators run one catch-all + zero targeted, or one targeted
+ zero catch-all. The matcher (`SELECT … LIMIT 1` ordered by
`started_at DESC`) prefers the most recently started session.

### A.3.3 Capture is a hot-path no-op when no session is active

`trace.Capture` looks up the active session for the current IMSI; if
none matches, it returns immediately (no DB write). The cost on the
no-trace path is one `SELECT trace_ref FROM trace_sessions …` per
event — bounded by the active-session count, which is ~0–1 typically.

### A.3.4 JSON / XML export (TS 32.423 §6 file exchange)

`/api/trace/{ref}/export/{fmt}` returns the full `trace_records`
list as a downloadable attachment. The JSON shape mirrors the panel
view; XML is a flat envelope (not the canonical TS 32.423 ASN.1 BER)
chosen for grep-ability by operators without an ASN.1 schema
compiler.

## A.4 Operator REST API (`/api/trace/*`)

### A.4.1 Session life-cycle

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/trace/start` | `{trace_ref?, imsi?, gnb_ip?, depth?, interfaces?, duration_sec?}` → `{ok, trace_ref}`. |
| GET | `/api/trace/sessions` | `{ok, sessions: [...]}` — every session, newest first. |
| POST | `/api/trace/{ref}/stop` | flips status → `stopped`; 404 if unknown. |
| DELETE | `/api/trace/{ref}` | hard-deletes session + records (FK CASCADE). |

### A.4.2 Record retrieval

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/trace/{ref}/records?limit=N` | `{ok, trace_ref, records: [...]}` — newest first. |
| GET | `/api/trace/{ref}/export/json` | full JSON download with `Content-Disposition: attachment`. |
| GET | `/api/trace/{ref}/export/xml`  | flat XML download. |

### A.4.3 AI hooks (deferred)

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/trace/{ref}/ai/analyze` | LLM-assisted trace summary; returns "not configured" until `oam/ai` is wired in. |
| GET | `/api/trace/{ref}/ai/bottleneck` | LLM-assisted hotspot detection; same. |

### A.4.4 Correlation index (NGAP ↔ SBI ↔ PFCP bridge)

The correlation surface lets the operator pivot from any one
transport identifier to every other ID tied to the same UE call —
without joining per-NF logs by eye.

| Method | Path | Purpose |
|--------|------|---------|
| POST   | `/api/trace/correlation` | Register/UPSERT a row; `{ok, call_id, row}`. Body: any of `imsi`, `amf_ue_ngap_id`, `ran_ue_ngap_id`, `gnb_id`, `pdu_session_id`, `seid_up`, `seid_cp`, `teid_dl`, `teid_ul`, `otel_trace_id`, `ngap_trace_ref`, `sbi_corr_id`, `call_id`. |
| GET    | `/api/trace/correlation?limit=N` | List, newest-first. |
| GET    | `/api/trace/correlation/{call_id}` | Single row; 404 if unknown. |
| DELETE | `/api/trace/correlation/{call_id}` | Remove one row; 404 if unknown. |
| POST   | `/api/trace/correlation/reset` | Wipe the index. |
| GET    | `/api/trace/correlation/by/imsi/{imsi}` | Pivot by IMSI. |
| GET    | `/api/trace/correlation/by/amf-ue-ngap-id/{id}` | Pivot by AMF-UE-NGAP-ID (TS 38.413). |
| GET    | `/api/trace/correlation/by/seid/{seid}` | Pivot by N4 SEID (matches either UPF or SMF side; TS 23.502 §4.4.1.2). |
| GET    | `/api/trace/correlation/by/otel-trace-id/{tid}` | Pivot by W3C trace_id (closes loop with `/api/otel/spans/{trace_id}`). |
| GET    | `/api/trace/correlation/by/sbi-corr-id/{id}` | Pivot by `3gpp-Sbi-Correlation-Info` (TS 29.500 §6.10.2.5). |

The UPSERT path matches an existing row by IMSI → AMF-UE-NGAP-ID →
SEID → OTEL trace_id → SBI corr_id (in priority order) and merges
new fields with `COALESCE` so producers can fill in identifiers as
they become known across N1/N2/SBI/N4 hops. A POST with no natural
key returns 400 — rows nobody could ever look up are noise.

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| TS 32.421 / 32.422 / 32.423 PDFs not loaded | — | All §-cites tracked as `TODO(spec:)` prose. |
| TS 32.423 §5.6 ASN.1 BER TraceRecord file format | — | We export flat JSON / XML instead. Wire the BER encoder when an operator asks. |
| TS 28.531 (5GC-MnS MDT activation) | — | Not implemented; activation is REST-only today. |
| AI analysis / bottleneck endpoints | — | Returns "not configured" stub until `oam/ai` wires through. |
| Correlation auto-population from NF hot path | — | Producers (NGAP/NAS/PFCP/SBI handlers) don't yet call `RegisterCorrelation` themselves. The operator + tester routes are functional; auto-write hooks will land when the trace.Capture sites are extended. |

---

# Part B — Design

## B.1 Process layout

```
                ┌──────────────────┐
                │ Traces GUI panel │
                └─────────┬────────┘
                          │ POST /api/trace/start
                          v
                ┌──────────────────────┐    ┌─────────────────────┐
                │ trace.StartSession   │--> │ trace_sessions      │
                └──────────────────────┘    │ (PK trace_ref)      │
                                            │ status='active'     │
                                            └────────┬────────────┘
                                                     ^
   NAS / NGAP / NAS-SM / PFCP / SIP                  │
   handlers in nf/amf, nf/smf, nf/upf, services/ims  │
                       │                             │
                       v                             │
              ┌────────────────────┐                 │
              │ trace.Capture(...) │─────────────────┘ matches active session
              └────────┬───────────┘                   (catch-all if imsi=="")
                       │ INSERT trace_records
                       v
              ┌────────────────────┐
              │ trace_records      │
              │ FK trace_ref       │
              └────────────────────┘
                       ^
                       │ JSON / HTTP
                       │
              webservice/app/routes_trace.go
              webservice/templates/traces.html
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `oam/trace/trace.go` | 276 | `StartSession` / `StopSession` / `Capture` / `ListSessions` / `ListRecords`. |
| `oam/trace/trace_test.go` | 184 | round-trip, depth validation, IMSI targeting, gNB-IP folding. |
| `db/schemas/domains.go` | (slice) | `trace_sessions` + `trace_records` DDL. |
| `webservice/app/routes_trace.go` | ~250 | REST surface for §A.4 (this file). |

Tests:
- `mmt_studio_core_tester/src/testcases/oam/tc_trace.py` — 7 live integration TCs.

## B.2.1 Correlation surface

```
   ┌──────────────────────────┐
   │ /api/trace/correlation/* │
   └────────────┬─────────────┘
                │
                v
   ┌─────────────────────────────────────┐
   │ oam/trace/correlation.go            │
   │   RegisterCorrelation (UPSERT)      │
   │   LookupBy{IMSI,AmfNgapID,SEID,     │
   │           OtelTraceID,SbiCorrID}    │
   │   ListCorrelations / Delete / Purge │
   └────────────────┬────────────────────┘
                    │
                    v
   ┌─────────────────────────────────────┐
   │ trace_correlation table             │
   │  PRIMARY KEY id                     │
   │  UNIQUE call_id                     │
   │  imsi · amf_ue_ngap_id · seid_up    │
   │  otel_trace_id · sbi_corr_id        │
   └─────────────────────────────────────┘
```

UPSERT priority on natural keys: `imsi → amf_ue_ngap_id → seid_up →
otel_trace_id → sbi_corr_id`. Indexes on each lookup column.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS trace_sessions (
  trace_ref       TEXT PRIMARY KEY,
  imsi            TEXT,
  gnb_ip          TEXT,
  depth           TEXT NOT NULL DEFAULT 'medium'
                  CHECK (depth IN ('minimum','medium','maximum')),
  interfaces      TEXT NOT NULL DEFAULT 'N1,N2',
  duration_sec    INTEGER NOT NULL DEFAULT 600,
  status          TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active','completed','stopped')),
  started_at      TEXT NOT NULL DEFAULT (datetime('now')),
  stopped_at      TEXT,
  record_count    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS trace_records (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  trace_ref       TEXT NOT NULL,
  timestamp       TEXT NOT NULL DEFAULT (datetime('now')),
  interface       TEXT NOT NULL,
  direction       TEXT NOT NULL,
  msg_type        TEXT NOT NULL,
  msg_code        INTEGER,
  imsi            TEXT,
  summary         TEXT,
  hex_dump        TEXT,
  latency_us      INTEGER,
  FOREIGN KEY (trace_ref) REFERENCES trace_sessions(trace_ref) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS trace_correlation (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  call_id         TEXT NOT NULL UNIQUE,
  imsi            TEXT,
  amf_ue_ngap_id  INTEGER,
  ran_ue_ngap_id  INTEGER,
  gnb_id          TEXT,
  pdu_session_id  INTEGER,
  seid_up         INTEGER,
  seid_cp         INTEGER,
  teid_dl         INTEGER,
  teid_ul         INTEGER,
  otel_trace_id   TEXT,
  ngap_trace_ref  TEXT,
  sbi_corr_id     TEXT,
  started_at      TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
-- Indexes on imsi, amf_ue_ngap_id, seid_up, otel_trace_id, sbi_corr_id.
```

The FK with `ON DELETE CASCADE` is what makes `DELETE /api/trace/{ref}`
safe — records are removed atomically with the session. The
`trace_correlation` table is not joined to `trace_sessions` — it's a
free-standing pivot index keyed by transport identifiers, used by
the panel to walk between disparate per-NF logs.

## B.4 Public API

```go
// Constants
const DepthMinimum = "minimum"
const DepthMedium  = "medium"
const DepthMaximum = "maximum"
const StatusActive    = "active"
const StatusCompleted = "completed"
const StatusStopped   = "stopped"

// Capture (called from NF hot path)
func Capture(iface, direction, msgName string, opts map[string]any)

// Session life-cycle
type SessionInput struct {
    TraceRef    string
    IMSI        string
    GnbIP       string
    Depth       string
    Interfaces  string
    DurationSec int
}
func StartSession(in SessionInput) (string, error)
func StopSession(traceRef string) (bool, error)

// Read
func ListSessions() ([]map[string]any, error)
func ListRecords(limit int) ([]map[string]any, error)

// Correlation index
type CorrelationInput struct {
    CallID, IMSI, GnbID, OtelTraceID, NgapTraceRef, SbiCorrID string
    AmfUeNgapID, RanUeNgapID, PduSessionID, SeidUp, SeidCp,
        TeidDl, TeidUl *int64
}
func RegisterCorrelation(in CorrelationInput) (string, error)
func LookupCallID(callID string) (map[string]any, error)
func LookupByIMSI(imsi string) ([]map[string]any, error)
func LookupByAmfNgapID(id int64) ([]map[string]any, error)
func LookupBySEID(seid int64) ([]map[string]any, error)
func LookupByOtelTraceID(tid string) ([]map[string]any, error)
func LookupBySbiCorrID(id string) ([]map[string]any, error)
func ListCorrelations(limit int) ([]map[string]any, error)
func DeleteCorrelation(callID string) (bool, error)
func PurgeCorrelations() error

// Routes
func (s *Server) registerTraceRoutes()
func (s *Server) registerTraceCorrelationRoutes()
```

## B.5 Producer call sites

`grep -rn 'trace\\.Capture' nf/ services/`:

- `nf/amf/ngap/*.go` — every NGAP RX / TX surface.
- `nf/amf/nas/*.go` — every NAS-MM message.
- `nf/smf/session/*.go` — every NAS-SM message.
- `nf/upf/pfcp/*.go` — every PFCP RX / TX surface.
- `services/ims/cscf/*.go` — SIP RX / TX from the CSCF.

Each call site is a no-op until an operator activates a session.

## B.6 Test coverage

### Live integration tests (Python tester)

`tc_trace.py::ALL_TRACE_TCS` (session life-cycle):

| TC | Coverage |
|----|----------|
| TC-TRACE-001 `trace_start_list_stop`           | start → list → stop life-cycle; status transitions to `stopped` |
| TC-TRACE-002 `trace_depth_validation`          | bad depth / out-of-range duration → 400 |
| TC-TRACE-003 `trace_duplicate_ref_rejected`    | same trace_ref re-used while active → start fails |
| TC-TRACE-004 `trace_records_empty`             | fresh session returns `{ok, records: []}` |
| TC-TRACE-005 `trace_stop_unknown`              | unknown ref stop → 404 |
| TC-TRACE-006 `trace_export_json`               | JSON export has Content-Disposition + parseable body |
| TC-TRACE-007 `trace_delete_cascade`            | DELETE removes session; subsequent stop → 404 |

`tc_trace_correlation.py::ALL_TRACE_CORR_TCS` (NGAP↔SBI↔PFCP bridge):

| TC | Coverage |
|----|----------|
| TC-TRC-001 `trace_corr_register_imsi`          | register row, look up by IMSI |
| TC-TRC-002 `trace_corr_lookup_amf_ngap`        | look up by AMF-UE-NGAP-ID (TS 38.413) |
| TC-TRC-003 `trace_corr_lookup_seid`            | look up by either UPF or SMF SEID (TS 23.502 §4.4.1.2) |
| TC-TRC-004 `trace_corr_lookup_otel`            | look up by W3C OTEL trace_id |
| TC-TRC-005 `trace_corr_upsert_preserves_call_id` | second register on same IMSI merges via COALESCE; call_id stable |
| TC-TRC-006 `trace_corr_unknown_404`            | GET / DELETE on unknown call_id → 404 |
| TC-TRC-007 `trace_corr_list_and_limit`         | `?limit=N` + newest-first ordering |
| TC-TRC-008 `trace_corr_delete_and_reset`       | DELETE one row + /reset wipes all |
| TC-TRC-009 `trace_corr_no_natural_key_400`     | body without IMSI / NGAP / SEID / OTEL / SBI key → 400 |

All sixteen pass against the current core build.

## B.7 References

- **TS 32.421** — Subscriber and equipment trace; trace concepts and
                  requirements (TODO; not loaded locally).
- **TS 32.422** — Subscriber and equipment trace; trace control and
                  configuration management (TODO; not loaded locally).
- **TS 32.423** — Subscriber and equipment trace; trace data
                  definition and management (TODO; not loaded locally).
- **TS 28.531** — 5GC-MnS Management-Based Trace activation
                  (TODO; not loaded locally).
- **TS 29.500 §6.10.2.5** — `3gpp-Sbi-Correlation-Info` HTTP header
                  (used by the correlation index for SBI-side pivots).
- **TS 23.502 §4.4.1.2** — N4 Session Establishment / Modification
                  (SEID pairs that the correlation index records).
- **TS 38.413** — NGAP IEs (AMF-UE-NGAP-ID / RAN-UE-NGAP-ID).
- `OBSERVABILITY.md` — operator guide, "Trace capture" section.

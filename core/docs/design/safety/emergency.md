# Emergency Services — 5GS

5GS Emergency Services operator surface for the SA Core. Owns the
singleton emergency-services configuration, an active emergency
PDU-session ledger, classifier helpers (`Request type` 3 vs DNN=`sos`),
the dedicated QoS profile (TS 23.501 §5.16.4.6), and an E-CSCF →
PSAP SIP routing helper.

# Part A — Functional

## A.1 Why emergency services?

3GPP mandates emergency-call support unless the operator explicitly
disables it for a regulator-driven reason (TS 22.101 §10.1). The
5GC must:

- Admit emergency-PDU requests even from unauthenticated UEs
  (TS 24.501 §5.5.1.2.6 / §5.5.1.2.6A).
- Allocate an IP from a dedicated emergency pool
  (TS 23.501 §5.16.4.8).
- Apply a dedicated QoS profile with high ARP priority and
  pre-emption capability (TS 23.501 §5.16.4.6).
- Route the SIP INVITE — carrying `urn:service:sos[.<sub>]` per
  RFC 5031 §4.2 — to a PSAP via the E-CSCF (TS 23.167 §6.2.2 + §7.5).
- Track active emergency sessions for audit / supervision.

This package is the operator-/AMF-/E-CSCF-facing projection of that
contract. It does not drive on-air NGAP / NAS / SIP itself; it
computes the inputs the AMF / SMF / IMS handlers need.

## A.2 Reference points

| Reference point | Peer | Wire | Spec | Status here |
|-----------------|------|------|------|-------------|
| **Initial Registration** | UE → AMF | NAS over NGAP | TS 24.501 §5.5.1.2.6 / §5.5.1.2.6A | `IsAuthRequired` is the operator-side knob the AMF reads before deciding AKA. |
| **PDU Session Establishment** | UE → SMF | NAS over NGAP | TS 23.501 §5.16.4.9 | `IsEmergencyPDURequest` classifies; `GetEmergencyQoS` returns the 5QI/ARP. |
| **N6 (emergency)** | UPF ↔ PDN | dedicated DNN+pool | TS 23.501 §5.16.4.8 | Pool config in `emergency_config.ip_pool_v4 / ip_pool_v6`; allocation lives in SMF/UPF. |
| **Mw (E-CSCF → PSAP)** | E-CSCF → PSAP | SIP/UDP | TS 23.167 §7.5 | `RouteEmergencyCall` is the final hop; LRF/RDF lookup is a deferred TODO. |

## A.3 Operator-visible behaviours

### A.3.1 Singleton configuration

A single `emergency_config` row carries:

| Field | Default | Notes |
|-------|---------|-------|
| `enabled` | 1 | TS 22.101 §10.1 — defaults true |
| `auth_required` | 0 | TS 24.501 §5.5.1.2.6A — unauthenticated path allowed |
| `emergency_dnn` | `sos` | TS 23.003 DNN naming guidance |
| `ip_pool_v4` | `10.99.0.0/24` | TS 23.501 §5.16.4.8 |
| `ip_pool_v6` | (empty) | optional |
| `psap_sip_uri` / `psap_ip` / `psap_port` | (empty / 5060) | TS 23.167 §7.5 |
| `emergency_qfi` / `voice_qfi` / `arp_priority` | 5 / 1 / 1 | TS 23.501 §5.16.4.6 |
| `max_sessions` | 100 | operator policy |

`UpdateConfig` is allow-listed — only those columns are accepted.

### A.3.2 PDU classifier

`IsEmergencyPDURequest(request_type, dnn)` returns true if either:

- `request_type == 3` ("Emergency Request" per TS 23.501 §5.16.4.9), or
- DNN matches the operator-configured emergency DNN (`sos` by default,
  matched case-insensitively).

Both signals are equally valid; the OR is intentional.

### A.3.3 QoS profile

`GetEmergencyQoS()` returns:

```json
{ "qfi": 5, "fiveqi": 5, "arp_priority": 1, "resource_type": "NonGBR" }
```

`qfi` and `arp_priority` come from `emergency_config`. The
`resource_type=NonGBR` is fixed — TS 23.501 §5.16.4.6 mandates a
non-GBR profile for the signalling-bearer side; GBR voice bearers are
managed separately by the SMF/PCF.

### A.3.4 SIP URN check

`CheckEmergencyURN(uri)` is an RFC 5031 §4.2 prefix match:
case-insensitive `urn:service:sos` (with or without a sub-service tag
like `.ambulance`, `.fire`, `.police`, `.gas`, `.marine`, `.mountain`,
`.physician`, `.poison`).

### A.3.5 PSAP routing

`RouteEmergencyCall(imsi, gnbIP, sipInvite[])` opens a UDP socket to
the configured `psap_ip:psap_port` and writes the SIP INVITE. Returns
false if no PSAP is configured (panel surfaces this as `psap_configured: false`).

This is a thin shim — full E-CSCF behaviour (LRF/RDF lookup per
location, P-Asserted-Identity rewrite, anonymous-caller handling, GSTN
PSAP via MGCF, IMS PSAP via IBCF) is deferred (see §A.5).

### A.3.6 Active session ledger

Every PDU-session establishment for an emergency request creates an
`emergency_sessions` row with `status='active'`. `Release` flips the
row to `released` and stamps `end_time`. The GUI dashboard reads
active rows for live counts; the audit trail is preserved.

## A.4 Operator REST API (`/api/emergency/*`)

| Method | Path | Purpose |
|--------|------|---------|
| GET    | `/api/emergency` | dashboard counters (alias of `/stats`) |
| GET    | `/api/emergency/stats` | `{enabled, auth_required, active_sessions, total_sessions, psap_configured}` |
| GET    | `/api/emergency/config` | full config row |
| POST   | `/api/emergency/config` | sparse update — allow-listed columns only; returns refreshed row |
| GET    | `/api/emergency/sessions` | active session list |
| POST   | `/api/emergency/sessions` | create one (`imei` or `imsi` required) |
| POST   | `/api/emergency/sessions/{id}/release` | mark released |
| GET    | `/api/emergency/classify?request_type=N&dnn=X` | PDU classifier probe |
| GET    | `/api/emergency/qos` | resolved QoS profile |
| GET    | `/api/emergency/check-urn?request_uri=X` | RFC 5031 §4.2 URN check |
| GET    | `/api/emergency/psap` | PSAP target + `configured` flag |

## A.5 Spec gaps / TODOs

| TODO | Anchor | Status |
|------|--------|--------|
| Full E-CSCF behaviour (LRF/RDF lookup per UE location) | TS 23.167 §6.2.2 / §6.2.3 | `RouteEmergencyCall` is a thin SIP-INVITE shim |
| EATF — Emergency Access Transfer Function (SRVCC of an active emergency call to CS access) | TS 23.167 §6.2.6 | Not present (no CS access in this build) |
| IMS Emergency Session Establishment without Registration | TS 23.167 §7.4 | Today we assume registered UEs |
| Call-Back Requirements (PSAP → UE callback path) | TS 22.101 §10.1.3 | Not wired |
| eCall Only Mode | TS 23.501 §5.16.4.10 | Not modelled |
| Emergency Services Fallback (EPS fallback for voice when 5GS-IMS voice isn't available) | TS 23.501 §5.16.4.11 | Not wired (EPS fallback in general lives in nf/amf/gmm) |

---

# Part B — Design

## B.1 Process layout

```
┌──────────── safety/emergency (operator surface) ────────────┐
│                                                             │
│  Singleton config              Active session ledger        │
│   emergency_config              emergency_sessions          │
│   ├── GetConfig                 ├── CreateEmergencySession  │
│   ├── UpdateConfig              ├── ReleaseEmergencySession │
│   ├── IsEmergencyEnabled        └── GetActiveEmergencySessions
│   └── IsAuthRequired                                        │
│                                                             │
│  PDU classifier (§5.16.4.9)    QoS resolver (§5.16.4.6)     │
│   IsEmergencyPDURequest         GetEmergencyQoS             │
│       request_type==3            qfi / fiveqi / arp /       │
│       OR dnn==sos                resource_type=NonGBR       │
│                                                             │
│  SIP URN check (RFC 5031)       E-CSCF helper (§7.5)        │
│   CheckEmergencyURN              RouteEmergencyCall         │
│      urn:service:sos[.sub]        UDP/SIP to                │
│                                   psap_ip:psap_port         │
│                                                             │
└─────────────────────────────────────────────────────────────┘
                              ▲
                              │ JSON / HTTP via OAM panel
                              │
                webservice/app/routes_emergency.go
```

## B.2 File map

| File | LOC | Role |
|------|----:|------|
| `safety/emergency/emergency.go` | ~325 | Config CRUD + session ledger + classifier + QoS + URN + PSAP-route. |
| `safety/emergency/emergency_test.go` | ~150 | Config defaults, classifier branches, URN match, QoS resolution. |
| `db/schemas/domains.go` | (slice) | DDL for `emergency_config` (singleton) + `emergency_sessions`. |
| `webservice/app/routes_emergency.go` | ~165 | REST surface for §A.4. |

Tests:
- `mmt_studio_core_tester/src/testcases/safety/tc_emergency.py` — 5 live integration TCs.

## B.3 DB schema

```sql
CREATE TABLE IF NOT EXISTS emergency_config (
  id              INTEGER PRIMARY KEY CHECK (id = 1),
  enabled         INTEGER NOT NULL DEFAULT 1,
  auth_required   INTEGER NOT NULL DEFAULT 0,
  emergency_dnn   TEXT NOT NULL DEFAULT 'sos',
  ip_pool_v4      TEXT NOT NULL DEFAULT '10.99.0.0/24',
  ip_pool_v6      TEXT NOT NULL DEFAULT '',
  psap_sip_uri    TEXT NOT NULL DEFAULT '',
  psap_ip         TEXT NOT NULL DEFAULT '',
  psap_port       INTEGER NOT NULL DEFAULT 5060,
  emergency_qfi   INTEGER NOT NULL DEFAULT 5,
  voice_qfi       INTEGER NOT NULL DEFAULT 1,
  arp_priority    INTEGER NOT NULL DEFAULT 1,
  max_sessions    INTEGER NOT NULL DEFAULT 100
);

CREATE TABLE IF NOT EXISTS emergency_sessions (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  imsi            TEXT,
  imei            TEXT,
  pdu_session_id  INTEGER,
  ip_addr         TEXT,
  gnb_ip          TEXT,
  tac             TEXT,
  cell_id         TEXT,
  start_time      REAL NOT NULL,
  end_time        REAL,
  called_number   TEXT,
  status          TEXT NOT NULL DEFAULT 'active'
);

CREATE INDEX IF NOT EXISTS idx_emerg_sess_status ON emergency_sessions(status);
```

The `id = 1` CHECK on `emergency_config` enforces the singleton; the
`INSERT OR IGNORE` in `ensureConfig` is the corresponding write
guard.

## B.4 Classifier algorithm

```go
IsEmergencyPDURequest(rt, dnn):
   return rt == 3 || lower(dnn) == "sos"
```

Both signals are equally normative:

- `request_type == 3` is the explicit "Emergency Request" value from
  TS 23.501 §5.16.4.9.
- `DNN == sos` is the operator-configured emergency APN per
  TS 23.003 DNN naming guidance.

A UE may use either; the SMF must accept both. Lower-case the DNN to
tolerate UE-side capitalisation differences.

## B.5 Public API

```go
// Config
func GetConfig() (map[string]interface{}, error)
func UpdateConfig(fields map[string]interface{}) error
func IsEmergencyEnabled() bool
func IsAuthRequired() bool

// Session ledger
func CreateEmergencySession(imsi, imei string, pduSessionID int,
    ipAddr, gnbIP, tac, cellID string) int64
func ReleaseEmergencySession(sessionID int64)
func GetActiveEmergencySessions() ([]map[string]interface{}, error)

// PDU classification + QoS
func IsEmergencyPDURequest(requestType int, dnn string) bool
func GetEmergencyQoS() map[string]interface{}

// SIP URN check + PSAP routing
func CheckEmergencyURN(requestURI string) bool
func RouteEmergencyCall(imsi, gnbIP string, sipInvite []byte) bool

// GUI panel adapters
func List() ([]map[string]any, error)  // alias of GetActiveEmergencySessions
func Status() map[string]any           // alias of GetEmergencyStats
func GetEmergencyStats() map[string]interface{}
```

## B.6 Test coverage

### B.6.1 Go unit tests

`safety/emergency/emergency_test.go` — config defaults / allow-list,
classifier OR-logic, URN prefix-match, QoS resolution from config.

### B.6.2 Live integration tests (Python tester)

| TC | Coverage |
|----|----------|
| TC-EMG-001 `emergency_config_crud` | GET → POST sparse update → GET roundtrip; allow-list ignores unknown fields. |
| TC-EMG-002 `emergency_classifier` | request_type=3 / DNN=sos / DNN=SOS / neither — all four branches. |
| TC-EMG-003 `emergency_urn_check` | RFC 5031 §4.2 sub-services + case-insensitive prefix; non-emergency URIs rejected. |
| TC-EMG-004 `emergency_qos` | QFI/ARP from config persist into the resolved profile; resource_type=NonGBR. |
| TC-EMG-005 `emergency_session_lifecycle` | Empty IMSI/IMEI → 400; create → list active → release → no longer active. |

All five are wired into `tc_emergency.py::ALL_EMERGENCY_TCS` and pass
against the current core build.

## B.7 References

- **TS 22.101**:
  - §10 — Emergency Calls (umbrella).
  - §10.4 — Emergency calls in IM CN subsystem.
  - §10.6 — Location Availability for Emergency Calls.
  - §10.1.3 — TODO; Call-Back Requirements.
- **TS 23.501**:
  - §5.16.4 — Emergency Services architecture.
  - §5.16.4.6 — QoS for Emergency Services.
  - §5.16.4.8 — IP Address Allocation for emergency PDUs.
  - §5.16.4.9 — Handling of PDU Sessions for Emergency Services.
  - §5.16.4.10 — TODO; eCall Only Mode.
  - §5.16.4.11 — TODO; Emergency Services Fallback.
- **TS 23.167**:
  - §6.2.2 — E-CSCF functional entity.
  - §6.2.3 — TODO; LRF/RDF location retrieval.
  - §6.2.6 — TODO; EATF (CS handover of an active emergency call).
  - §7.1 / §7.5 — IMS Emergency procedures + PSAP interworking.
  - §7.4 — TODO; IMS Emergency without Registration.
- **TS 24.501**:
  - §5.5.1.2.6 — Initial Registration for Emergency services.
  - §5.5.1.2.6A — Initial Registration when authentication is not
    performed.
- **RFC 5031** §4.2 — `urn:service:sos[.<sub>]` URN.

# supplementary — IMS Supplementary Services

## 1. Role / scope

`services/supplementary/` is the network-side configuration store for
the IMS-anchored Supplementary Services (CDIV / CW / CB / OIP / OIR /
TIP / TIR) per TS 24.604 / TS 24.611 / TS 24.615 / TS 24.607 /
TS 24.608. It owns:

- `supplementary_services` SQL row store: per-(IMSI, service_type)
  activation flag + service-specific config (forwarding number,
  no-reply timer, barring password, free-form `config_json`).
- MMI string parser (`mmi.go`) — UE-side keypad procedures from
  TS 22.030 §6.5.2 (`*SC*SI#`, `#SC*SI#`, `*#SC*SI#`, `**SC*SI#`,
  `##SC*SI#`).
- A scaffolded layer-3 SS-Operation codec (`codec.go`) — Facility IE
  + SS-Operation op-codes per TS 24.080 §3.6 / §4.5, used for CS
  fall-back and tester fixtures.

The IMS-anchored signalling pathway (XCAP PUT to
`<document-uri>/simservs.xml` per TS 24.623, REGISTER for IMS
provisioning, INVITE 3xx for CDIV invocation) is **not** emitted —
this package is the SQL CRUD + parsing surface that an IMS AS / GMS
caller drives.

## 2. File map

| File | LOC | Role |
|------|-----|------|
| `supplementary.go` | 321 | Service-record CRUD + `Activate` / `Deactivate` / `Interrogate` / `BulkSet` |
| `mmi.go` | 304 | TS 22.030 §6.5.2 MMI parser; SC table; SI/SIB/SIC helpers |
| `codec.go` | 143 | TS 24.080 §3.6 Facility-IE component / op-code scaffolding |
| `mmi_test.go` | — | MMI parse tests |

Total ~ 938 LOC including tests.

## 3. Wire / API surface

### Service record (per `supplementary_services` row)

```go
type ServiceRecord {
    ID, IMSI, ServiceType, Active,
    ForwardingNumber, NoReplyTimer, BarringPassword,
    ConfigJSON, UpdatedAt
}
```

### Service constants (every name carries its TS § in source)

| Const | Spec | Description |
|-------|------|-------------|
| `CFU` | TS 24.604 §4.2.1.2 | Comm. Forwarding Unconditional |
| `CFB` | TS 24.604 §4.2.1.3 | Comm. Forwarding on Busy |
| `CFNRy` | TS 24.604 §4.2.1.4 | Comm. Forwarding on No Reply |
| `CFNRc` | TS 24.604 §4.2.1.5 | Comm. Forwarding on Subscriber Not Reachable |
| `CW` | TS 24.615 §4.5 | Communication Waiting |
| `OIP` / `OIR` | TS 24.607 §4.5 | Originating ID Presentation / Restriction |
| `TIP` / `TIR` | TS 24.608 §4.5 | Terminating ID Presentation / Restriction |
| `BAOC` / `BAOIC` / `BAIC` | TS 24.611 §4.5 | Outgoing / Outgoing Intl / Incoming Barring |

### Public API

| Function | Spec § | Notes |
|----------|--------|-------|
| `ListByIMSI(imsi)` | — | All services for one subscriber |
| `Get(imsi, st)` | — | Single record |
| `Activate(imsi, st, fwd, pwd, cfg, timer)` | TS 24.604 §4.5.1 / TS 24.611 §4.5.1 / TS 24.615 §4.5.2 / TS 24.607 §4.5.1 / TS 24.608 §4.5.1 | Validates E.164 forwarding, 4-digit barring password, no-reply timer 5..30 s |
| `Deactivate(imsi, st)` | (same spec mapping) | Flips `active=0`, preserves stored DN/PW/cfg |
| `Interrogate(imsi, st)` | TS 24.604 §4.5.1b / TS 24.615 §4.5.4 / TS 24.607 §4.5.1 / TS 24.611 §4.5.1 | Reads SQL row directly — Ut/XCAP transport not emitted |
| `BulkSet(imsi, [{service_type, active, ...}])` | (per-row dispatch) | Batch activate/deactivate with per-row error capture |
| `DeleteAll(imsi)` | — | Clears all services |

### Operator REST API (`/api/supplementary/*`)

Wired in `webservice/app/routes_supplementary.go`. Before this
surface landed, no `/api/supplementary/*` routes existed at all —
the package was reachable only from the IMS internal call paths.

| Method | Path                                           | Purpose |
|--------|------------------------------------------------|---------|
| GET    | `/api/supplementary/status`                    | Panel header counters. |
| GET    | `/api/supplementary/services?imsi=<imsi>`      | Per-IMSI list (mandatory imsi). |
| POST   | `/api/supplementary/activate`                  | `{imsi, service_type, forwarding_number?, barring_password?, no_reply_timer?}`. |
| POST   | `/api/supplementary/deactivate`                | `{imsi, service_type}`. |
| GET    | `/api/supplementary/interrogate?imsi=&service_type=` | `{ok, exists, record}`. |
| POST   | `/api/supplementary/bulk`                      | `{imsi, services:[{service_type, active, ...}]}`. |
| DELETE | `/api/supplementary/services?imsi=<imsi>`      | Wipe all services for a subscriber. |
| POST   | `/api/supplementary/mmi`                       | Parse a TS 22.030 §6.5.2 MMI string into `{procedure, service_code, service_name, sia, sib, sic}`. |

The validation layer rejects unknown `service_type` (TS 22.030
Annex B), bad forwarding-number E.164, or non-4-digit barring
password at the route layer with a 400.

## 4. Headline procedures

### MMI parsing (TS 22.030 §6.5.2)

`ParseMMI(s)` (`mmi.go:132-223`) discriminates:

| Prefix | Procedure |
|--------|-----------|
| `*SC*SI#` | Activation (or Registration if SIA carries a forwarded-to DN for a CF service — `mmi.go:215-220`) |
| `**SC*SI#` | Registration (alternative form) |
| `#SC*SI#` | Deactivation |
| `*#SC*SI#` | Interrogation |
| `##SC*SI#` | Erasure |

Trailing `#` is mandatory (`mmi.go:139-141`). SC must be 2-3 digits
and resolve in the in-source `ssCodeName` table — verbatim from
TS 22.030 Annex B Table B.1, listing CF (21/67/61/62/002/004), CW (43),
OIR/OIP (30/31/76/77), barring (33/331/332/35/351/330/333/353), ECT
(96), CCBS (37), CNAP (300). Unknown codes return
`unknown service code "..." (TS 22.030 Annex B)`.

`(*MMIRequest).NoReplyTimer()` reads SIC and validates the
TS 22.030 §6.5 5..30 s range; on miss callers fall back to TS 24.604
§4.5.1 default of 20 s. `BarringPassword()` returns SIA only for
barring SCs. `ForwardedNumber()` returns SIA only for CF SCs.

### Activation validation (`supplementary.go:162-198`)

```
Activate(imsi, st, fwd, pwd, cfg, timer)
   ↓
   forwarding service?  → require fwd, validate E.164 (e164Re)
   ↓
   barring service AND pwd != ""? → validate 4-digit (barringPwRe)
   ↓
   CFNRy?              → clamp timer to 5..30 (else 20)
   ↓
   upsert(active=1, fwd?, pwd?, cfg?, timer)
```

Deactivation (`Deactivate`) flips `active=0` while preserving the
stored DN/PW/cfg/timer so a future Activate restores them
(`supplementary.go:205-232`).

### Layer-3 SS-Operation codec scaffold (`codec.go`)

`EncodeInvokeFrame(invokeID, op, params)` (`codec.go:92-104`) builds a
minimal TS 24.080 §3.6.2 Invoke component:

```
0xA1                  Invoke tag                       §3.6.2
len_invoke            short-form length (X.690)
0x02 0x01 invokeID    Invoke ID INTEGER                §3.6.3
0x02 0x01 opCode      Operation Code INTEGER           §3.6.4
[parameter SEQUENCE]  argument body                    §3.6.5  (caller-supplied)
```

Op-codes (`codec.go:35-55`) — `RegisterSS=10`, `EraseSS=11`,
`ActivateSS=12`, `DeactivateSS=13`, `InterrogateSS=14`, `NotifySS=16`,
`RegisterPassword=17`, `GetPassword=18`,
`ProcessUnstructuredSSData=19`, `ForwardCheckSSIndication=38`,
`ProcessUnstructuredSSReq=59`, `UnstructuredSSRequest=60`,
`UnstructuredSSNotify=61`, `BuildMPTY=30`, `HoldMPTY=31`,
`RetrieveMPTY=32`, `SplitMPTY=33`, `ExplicitCT=53`, `CallDeflection=117`.

Component tags (`codec.go:67-72`): `Invoke=0xA1`, `ReturnResult=0xA2`,
`ReturnError=0xA3`, `Reject=0xA4`. The container IEI inside CS L3
(TS 24.008 §10.5.4.15) is `IEIFacility=0x1C`.

## 5. Key types

```go
type Procedure  int   // ProcUnknown / ProcActivation / ProcDeactivation /
                       // ProcInterrogation / ProcRegistration / ProcErasure
type MMIRequest { Procedure, ServiceCode, ServiceName, SIA, SIB, SIC, Raw }

type Op  int          // 24.080 §3.6.4 op-code value
const ComponentInvoke / ReturnResult / ReturnError / Reject byte
const IEIFacility = 0x1C
```

## 5a. Test coverage

`mmt_studio_core_tester/src/testcases/vas/tc_vas_oam.py` —
operator-API TCs (no UE/gNB needed):

| TC | Coverage |
|----|----------|
| TC-SS-010 `supp_activate_cfu_op_api` | activate CFU → interrogate `active=1` → deactivate → `active=0` |
| TC-SS-011 `supp_activate_validation` | bad service_type / missing forwarding_number / non-E.164 → 400 |
| TC-SS-012 `supp_mmi_parse` | `*21*<DN>#` (CFU + DN) → Registration; `*43#` (CW) → Activation; `*#21#` → Interrogation; trailing-`#` missing → 400 |
| TC-SS-013 `supp_bulk_set_mixed` | `/bulk` activate-some + deactivate-some, per-row `ok` results |

`tc_supplementary.py` carries the legacy UE/gNB integration TCs
(TC-SS-001..004) which exercise the surface end-to-end after
register_ue.

## 6. Stubs / TODOs

From `codec.go:106-143` and `mmi.go:285-303`:

| Location | Spec | Note |
|----------|------|------|
| `codec.go:106-110` | TS 24.080 §3.6.1 | Full Component decoder — only Invoke is built |
| `codec.go:112-115` | TS 24.080 §3.6.5 | ASN.1 SEQUENCE parameter encoding (per-op ARGUMENT types from TS 29.002 §17.6.4) |
| `codec.go:117-120` | TS 24.080 §4.3 | Error responses (`systemFailure(34)`, `illegalSS-Operation(16)`, etc.) |
| `codec.go:122-125` | TS 24.008 §10.5.4.15 | Facility IE container for CS fall-back |
| `codec.go:127-130` | TS 24.090 / TS 24.080 §2.5 | USSD on-air encoding (`processUnstructuredSS-Request` op 59) |
| `codec.go:132-136` | TS 24.623 | XCAP service-config encoding (`<simservs.xml>`) |
| `codec.go:138-142` | TS 24.604 §4.5.1 / .2 | Map Activate/Deactivate to SIP signalling (REGISTER / INVITE 3xx) |
| `mmi.go:285-288` | TS 22.030 §6.5.4 | Password-change procedure `**03*ZZ*OLD*NEW*NEW#` |
| `mmi.go:290-293` | TS 22.030 §6.5.5 | In-call control shortcodes (HOLD/MPTY/ECT) |
| `mmi.go:295-298` | TS 22.030 §6.5.6 | Roaming-state evaluation for `*351*PW#` |
| `mmi.go:300-303` | TS 22.030 Annex C | BSG SIB encoding validation (TS 22.004) |

The package-level guidance (`supplementary.go:17-31`) is that the
in-process CRUD is the IMS-anchored pathway today; XCAP / SIP layer
emission is the responsibility of a future IMS AS that consumes this
package's Activate / Deactivate calls.

## 7. References

All §-cites in source. Primary stack:

- **TS 22.030** §6.5 / §6.5.2 / §6.5.4-6 / Annex B / Annex C — UE
  MMI procedures
- **TS 22.081 / 22.082 / 22.083 / 22.084 / 22.087 / 22.088 / 22.091
  / 22.093 / 22.096** — supplementary-service stage-1 specs
- **TS 24.604** §4.2 / §4.5 — CDIV
- **TS 24.611** §4.5 — CB / ACR
- **TS 24.615** §4.5 — CW
- **TS 24.607** / **TS 24.608** §4.5 — OIP / OIR / TIP / TIR
- **TS 24.080** §3.6 / §4.3 / §4.5 — SS-Operation codec
- **TS 24.008** §10.5.4.15 — CS L3 Facility IE
- **TS 29.002** §17.6.4 — MAP-SS-Operations op-code assignment
- **TS 24.623** — XCAP service config (deferred)

---

*Last refreshed against commit `13a181d`.*

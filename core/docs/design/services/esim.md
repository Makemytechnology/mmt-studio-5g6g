# esim — eSIM / RSP

## 1. Role / scope

`services/esim/` implements the local SM-DP+ Profile Manager for
GSMA SGP.22 Consumer eSIM RSP, plus the eUICC registry and the
encrypted profile body builder. It owns three SQL tables — `esim_profiles`,
`esim_euicc`, `esim_notifications` — plus an ICCID-allocation counter
(`esim_iccid_counter`) and surfaces the canonical SGP.22 lifecycle:

```
available ─▶ reserved ─▶ downloaded ─▶ installed ─▶ enabled  ⇆  disabled
                                                       │
                                                       ▼
                                                    deleted
```

The package is divided into three sub-packages:

- **`esim`** — top-level Profile / EUICC / Notification CRUD against
  the SQL tables and the lifecycle state-transition helper.
- **`esim/profile`** — ICCID allocator (ITU-T E.118 + Luhn), Activation
  Code (`LPA:1$smdp$matchingID`), AES-CBC + HMAC profile crypto,
  USIM profile builder per TS 31.102 §4.2.
- **`esim/smdp`** — SM-DP+ Server implementing GSMA SGP.22 §3 ES9+
  (Initiate Authentication / Authenticate Client / GetBoundProfilePackage
  / HandleNotification).

## 2. Architecture

```
                   ┌─────────────────── esim/smdp ─────────────────────┐
                   │                                                   │
LPA / device ─────▶│  SMDPServer.PrepareProfile     (ES2+ collapsed)   │
   ES9+ HTTP        │                                                  │
       │           │  SMDPServer.InitiateAuthentication  (§3.1.2)      │
       │           │  SMDPServer.AuthenticateClient      (§3.1.3)      │
       │           │  SMDPServer.GetBoundProfilePackage  (§3.3.x)      │
       │           │  SMDPServer.HandleNotification      (§3.5)        │
       │           │                                                   │
       │           │  per-txnID session state                          │
       │           └──────────┬─────────────────────┬──────────────────┘
       │                      │                     │
       │                      ▼                     ▼
       │           ┌─── esim/profile ────────┐  ┌── esim ──────────┐
       │           │ AllocateICCID (E.118)   │  │ Profile / EUICC /│
       │           │ ValidateICCID (Luhn)    │  │ Notification CRUD│
       │           │ Activation Code         │  │ UpdateProfileState│
       │           │ Session keys + AES-CBC  │  │  (state machine)  │
       │           │ + HMAC                  │  └────────┬──────────┘
       │           │ BuildUSIMProfile        │           │
       │           │  (TS 31.102 §4.2)       │           │
       │           └────────────┬────────────┘           │
       │                        │                        │
       └────────────────────────┴────────────────────────┘
                                │
                  ┌─────────────┴─────────────────┐
                  │   SQLite tables               │
                  │   esim_profiles               │
                  │   esim_euicc                  │
                  │   esim_notifications          │
                  │   esim_iccid_counter          │
                  └───────────────────────────────┘
```

## 3. File map

| File | LOC | Role |
|------|-----|------|
| `esim.go` | 329 | Top-level Profile / EUICC / Notification CRUD; lifecycle state transitions |
| `profile/profile.go` | 211 | ICCID alloc + Luhn, Activation Code, AES-CBC + HMAC, USIM profile builder |
| `smdp/smdp.go` | 199 | SM-DP+ Server: PrepareProfile + ES9+ verbs (Initiate / Authenticate / GetBPP / Notify) |
| `esim_test.go` | 200 | |
| `profile/profile_test.go` | 199 | |
| `smdp/smdp_test.go` | 217 | |

Total ~ 1.4k LOC.

## 4. Wire / API surface

### Profile lifecycle (`esim/esim.go`)

| Function | Notes |
|----------|-------|
| `ListProfiles(state)` / `GetProfile(id)` / `GetProfileByICCID` / `GetProfileByActivationCode` / `GetProfileByMatchingID` / `GetProfilesForIMSI` | Lookup variants |
| `CreateProfile(iccid, imsi, name, type, class, ac, mid, smdp)` | Default name `"SA Core"`, type/class `operational` |
| `UpdateProfileState(id, newState)` | Enforces enum {available, reserved, downloaded, installed, enabled, disabled, deleted}; stamps `reserved_at / downloaded_at / installed_at` (`esim.go:224-247`) |
| `DeleteProfile(id)` | DELETE row |

### eUICC registry

| Function | Notes |
|----------|-------|
| `RegisterEUICC(eid, deviceInfo, lpaVersion)` | Inserts `esim_euicc` row |
| `GetEUICC(eid)` / `ListEUICCs()` / `DeleteEUICC(eid)` | CRUD |

### Notifications (ES9+ §3.5)

| Function | Notes |
|----------|-------|
| `LogNotification(iccid, eid, eventType, resultCode)` | Append-only log |
| `ListNotifications(iccid, limit)` | Default 50 |

### ICCID allocator (ITU-T E.118)

| Function | Notes |
|----------|-------|
| `AllocateICCID(issuerID)` | Returns 19-digit `89<issuer><seq>` + Luhn check (`profile/profile.go:71-83`) |
| `ValidateICCID(iccid)` | Length 18..20, all digits, prefix `89`, Luhn check (`profile/profile.go:86-93`) |

### Activation Code (GSMA SGP.22 §4.1)

| Function | Format |
|----------|--------|
| `GenerateMatchingID()` | 32-char hex (16-byte rand) |
| `GenerateActivationCode(smdp, mid)` | `LPA:1$<smdp>$<mid>` |
| `ParseActivationCode(ac)` | Returns `{smdp_address, matching_id}` |

### Profile crypto (GSMA SGP.22 §2.5.3 BPP envelope, structural model)

| Function | Notes |
|----------|-------|
| `GenerateSessionKeys()` | EncKey + MacKey + DEK (each 16 bytes) |
| `EncryptProfile(data, keys)` | AES-CBC + PKCS7, HMAC-SHA256 over (IV ‖ ciphertext); returns hex-encoded `{iv, ciphertext, mac}` |
| `DecryptProfile(enc, keys)` | HMAC-verify → decrypt → strip pad |

### USIM profile (TS 31.102 §4.2)

`BuildUSIMProfile(imsi, kHex, opcHex, iccid, mcc, mnc, opType)`
returns the on-card EF map. Notable: `ef_ad` is built per
TS 31.102 §4.2.18 — three reserved bytes then the MNC-length byte
(`profile/profile.go:185-188`):

```go
"ef_ad": fmt.Sprintf("000000%02x", mncLen)
```

Algorithm is fixed at `"milenage"` and `access_rules` advertise
`["e-utran", "nr"]` plus the home PLMN.

### ES9+ verbs (`smdp/smdp.go`)

| Verb | Spec § | Notes |
|------|--------|-------|
| `PrepareProfile(imsi, name)` | §3.0/§5.6 (collapsed) | Reads `ue_auth_data`, allocates ICCID + matchingID, builds + encrypts USIM profile, INSERT into `esim_profiles` (state=`available`); returns `{iccid, activation_code, matching_id, qr_data: {content, format: "SGP.22-v2.3.1"}}` |
| `InitiateAuthentication(txnID, euiccChallenge)` | §3.1.2 | 16-byte server challenge, stores per-txn session (state=`initiated`) |
| `AuthenticateClient(txnID, euiccSigned1)` | §3.1.3 | Moves session to `authenticated`, upserts EID into `esim_euicc` |
| `GetBoundProfilePackage(txnID, matchingID)` | §3.3.x | Requires `authenticated` state; rejects if profile not in `available`/`reserved`. Updates `profile_state='downloaded'`, stamps `downloaded_at` and binds `eid`, logs notification. Returns BPP body (JSON-wrapped — ASN.1 form deferred) |
| `HandleNotification(iccid, eid, eventType, seq)` | §3.5 | Maps `install→installed`, `enable→enabled`, `disable→disabled`, `delete→deleted` and updates row (`smdp.go:182-187`) |
| `GetProfileStatus(iccid)` | — | Lookup |

## 5. Headline procedures

**Operator-side profile preparation.** `PrepareProfile(imsi, name)` is
the collapsed ES2+ DownloadOrder + ConfirmOrder + GetCertificate
sequence (GSMA SGP.22 §3.0 / §5.6) — SM-DP+ talks to the operator's
auth-data store directly here. It:

1. Reads `op_type, op, k` from `ue_auth_data WHERE
   ue_id=(SELECT id FROM ue WHERE imsi=?)`.
2. Allocates ICCID via the per-process counter (`AllocateICCID`).
3. Generates matching-ID + activation code (`LPA:1$smdp$mid`).
4. Builds the USIM profile (`BuildUSIMProfile`) and serialises it.
5. Generates fresh session keys (`GenerateSessionKeys`) and encrypts
   the profile body.
6. Stores both encrypted body + session keys hex-encoded as
   `profile_blob` JSON in `esim_profiles` (state=`available`).
7. Returns activation code + QR-payload `{content, format:
   "SGP.22-v2.3.1"}` for the LPA.

**Profile download (LPA-side flow).** The LPA scans the QR, sees
`LPA:1$<smdp>$<matchingID>`, and starts ES9+:

```
LPA → SMDP+: InitiateAuthentication(txnID, euiccChallenge)
SMDP+ → LPA: serverChallenge + serverSigned1
LPA → SMDP+: AuthenticateClient(txnID, euiccSigned1{eid, ...})
SMDP+ → LPA: txnID
LPA → SMDP+: GetBoundProfilePackage(txnID, matchingID)
SMDP+ → LPA: boundProfilePackage{iccid, profileName, profileData}
            (state moves available/reserved → downloaded; eid bound;
             notification 'download' logged)
LPA installs / enables on eUICC
LPA → SMDP+: HandleNotification(iccid, eid, "install" / "enable" /
             "disable" / "delete", seq)
            (state moves accordingly)
```

**Lifecycle transitions** (`UpdateProfileState`,
`esim.go:224-247`). Sets `reserved_at` / `downloaded_at` /
`installed_at` columns when transitioning into the corresponding
state. Other transitions (enabled / disabled / deleted) only update
`profile_state` without timestamping in this code path; the SM-DP+
side stamps `downloaded_at` directly on `GetBoundProfilePackage`
(`smdp.go:171`).

## 6. Key types

```go
// esim/esim.go
type Profile { ID, ICCID, IMSI, EID, ProfileState, ActivationCode,
               MatchingID, SMDPAddress, ProfileName, ProfileType,
               ProfileClass, CreatedAt, ReservedAt, DownloadedAt,
               InstalledAt }
type EUICC   { ID, EID, DeviceInfo, LPAVersion, EUICCInfo,
               CurrentICCID, LastContact, RegisteredAt }
type Notification { ID, ICCID, EID, SeqNumber, EventType, ResultCode,
                    Timestamp }

// esim/profile/profile.go
type SessionKeys { EncKey, MacKey, DEK []byte }   // each 16 bytes

// esim/smdp/smdp.go
type SMDPServer { SMDPAddress, sessions map[txnID]*txnSession }
var Server = &SMDPServer{ SMDPAddress: "smdp.sacore.local", ... }
type txnSession { State, EUICCChallenge, ServerChallenge, EID,
                  CreatedAt }
```

## 7. Operator REST surface

Until 2026-05, `/api/esim/*` was three `emptyArrayRoute` stubs in
`routes_misc.go` plus two `{ok:true}` placeholders in
`routes_nsaas.go`; the SM-DP+ ES9+ helpers were unreachable from
the panel and tester. The route block at
`webservice/app/routes_esim.go` exposes the package as
`{ok:true, ...}` JSON.

### 7.1 /api/esim/* — operator panel surface

| Method | Path | Calls | Notes |
|--------|------|-------|-------|
| `GET` | `/api/esim/stats` | `esim.Stats()` | Total profiles + per-state histogram + eUICC count. |
| `GET` | `/api/esim/profiles?state=&imsi=` | `esim.ListProfiles` / `GetProfilesForIMSI` | Optional filters. |
| `GET` | `/api/esim/profile/{iccid}` | `esim.GetProfileByICCID` | 404 when not found. |
| `POST` | `/api/esim/order` | `esim.OrderProfile` | Collapsed ES2+ DownloadOrder + ConfirmOrder (SGP.22 §3.0/§5.6); returns ICCID + LPA Activation Code + QR data in one shot. |
| `PATCH` | `/api/esim/profile/{iccid}/state` | `esim.SetProfileState` | Validates against the SGP.22 state machine (`validTransition`). |
| `POST` | `/api/esim/profile/{iccid}/release` | `esim.ReleaseProfile` | Allow-listed to `available` / `reserved` (anything else → clean 400). |
| `GET` | `/api/esim/euiccs` | `esim.ListEUICCs` | All registered eUICCs. |
| `POST` | `/api/esim/euiccs` | `esim.RegisterEUICC` | Register a new EID. |
| `DELETE` | `/api/esim/euiccs/{eid}` | `esim.DeleteEUICC` | |
| `GET` | `/api/esim/notifications?iccid=&limit=` | `esim.ListNotifications` | SGP.22 §3.5 audit log. |

### 7.2 /api/smdp/* — ES9+ Mutual-Auth + BPP delivery

| Method | Path | Calls | Spec |
|--------|------|-------|------|
| `POST` | `/api/smdp/initiate-auth` | `smdp.Server.InitiateAuthentication` | SGP.22 §3.1.2 |
| `POST` | `/api/smdp/authenticate-client` | `smdp.Server.AuthenticateClient` | SGP.22 §3.1.3 |
| `POST` | `/api/smdp/get-bpp` | `smdp.Server.GetBoundProfilePackage` | SGP.22 §3.3 |
| `POST` | `/api/smdp/notify` | `smdp.Server.HandleNotification` | SGP.22 §3.5 |
| `GET` | `/api/smdp/profile/{iccid}/status` | `smdp.Server.GetProfileStatus` | |

The order endpoint also persists a SGP.22 §3.5 audit-log entry
(`event_type="order"`); release similarly logs `event_type="release"`,
so the operator can rebuild the lifecycle from `/api/esim/notifications`
without trusting the in-memory FSM.

### 7.3 Operator-side state-machine guard

`esim.SetProfileState` consults a transition allow-list
(`validTransition`) before calling the existing
`UpdateProfileState`. The allow-list reflects SGP.22 §3.5:

```
available  → {reserved, deleted}
reserved   → {downloaded, available, deleted}
downloaded → {installed, deleted}
installed  → {enabled, disabled, deleted}
enabled    → {disabled, deleted}
disabled   → {enabled, deleted}
deleted    → {}              # terminal
```

This catches illegal jumps (`available → installed`) that the bare
state validator in `esim.UpdateProfileState` would otherwise accept.

### 7.4 Tester coverage map

Operator-API only — no UE / eUICC card. The legacy UE-integration
TCs in `src/testcases/vas/tc_esim*` cover the LPA-side path.

| TC ID | File | Spec | Asserts |
|-------|------|------|---------|
| TC-ESIM-OAM-001 | `oam/tc_esim_oam.py` | SGP.22 §3 | `/stats` shape + all 7 lifecycle keys. |
| TC-ESIM-OAM-002 | ″ | SGP.22 §3.0 / §4.1 + ITU-T E.118 | Order, list, ICCID starts with `89` and validates Luhn, activation code is `LPA:1$<smdp>$<matchingID>`, release round-trip. |
| TC-ESIM-OAM-003 | ″ | SGP.22 §3.0 | Empty IMSI + unknown IMSI both return 400. |
| TC-ESIM-OAM-004 | ″ | SGP.22 §3.5 | Release rejected in `installed` state; cleanup PATCH `→ deleted` succeeds. |
| TC-ESIM-OAM-005 | ″ | SGP.22 §3 | GET / PATCH / release on unknown ICCID all return 404. |
| TC-ESIM-OAM-006 | ″ | SGP.22 §3 | eUICC register → list → delete round-trip. |
| TC-ESIM-OAM-007 | ″ | SGP.22 §3.1 + §3.3 + §3.5 | Full ES9+ Mutual-Auth + BPP delivery + lifecycle notification chain. |
| TC-ESIM-OAM-008 | ″ | SGP.22 §3.5 | Illegal transitions (`available → enabled`, `available → installed`) return 400; `→ deleted` always allowed. |

## 8. Stubs / TODOs

Package-level TODO inventory (`esim.go:26-35`, `smdp/smdp.go:22-30`,
`profile/profile.go:27-33`):

| Spec | Note |
|------|------|
| GSMA SGP.22 §5.7 | Full ASN.1 BPP wire codec (today: JSON envelope with hex IV / ciphertext / MAC) |
| GSMA SGP.22 §3 | ES2+ operator-side profile order / download trigger workflow (today `CreateProfile` is a direct INSERT) |
| GSMA SGP.22 §2.5.3 | BPP envelope shift from JSON map to spec-mandated ASN.1 |
| GSMA SGP.22 §3.4 | Cancel Session / error paths |
| GSMA SGP.22 §6 | Common Mutual Authentication based on ECKA(ECDSA) — replaced here by opaque challenge / response |
| GSMA SGP.32 | IoT Profile Provisioning (eIM, IPA) |
| TS 31.102 §5.2.1 | Milenage parameter blob layout — SQN delta + AMF not part of profile blob today |
| TS 33.501 §6.12 | SUCI computation off the downloaded K (lives in another package) |

GSMA SGP.* identifiers are **not** §-checked by speccheck (the regex
matches only TS / RFC). All SGP §-cites here are advisory.

## 9. References

§-cites that ARE speccheck-grounded:

- **TS 23.003** §2.2 — IMSI structure
- **TS 31.102** §4.2 / §4.2.2 / §4.2.18 — USIM ADF EF contents (EF_IMSI,
  EF_AD); `BuildUSIMProfile`'s `ef_ad` byte format follows §4.2.18
- **TS 31.102** §6.1 — AKA procedure (K / OPc consumed by AMF / AUSF
  after profile install)
- **TS 33.501** §6.1.3 — 5G AKA

§-cites informative (not speccheck-grounded):

- **GSMA SGP.22** §3 ES9+, §3.1.2 / §3.1.3 / §3.3.x / §3.5 / §4.1 /
  §2.5.3 — Consumer eSIM RSP
- **ITU-T E.118** — ICCID structure + Luhn
- **GSMA SGP.32** — IoT eSIM RSP

---

*Last refreshed: operator REST surface (routes_esim.go), SGP.22 state-machine guard, tester coverage map (8 TCs).*

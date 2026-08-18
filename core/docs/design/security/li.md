# Lawful Intercept (LI) — Functional Design

This is the **functional** view of MMT Studio Core's Lawful Intercept
feature: what LI is for, what the operator is legally obliged to
deliver, what events the network captures, and how a warrant moves
through its lifecycle. Implementation details (schemas, in-memory
caches, wire encodings) live in the companion data design doc:
[`li_data.md`](li_data.md).

> Spec anchors: 3GPP TS 33.126 (LI requirements / stage-1),
> TS 33.127 (LI architecture and functions / stage-2), TS 33.128
> (LI service-specific protocols / stage-3). The only LI hook in
> the locally-loaded TS 33.501 is **§5.9 NOTE 3** — SUPI is mixed
> into KAMF derivation precisely so the LI pipeline can correlate
> per-target events.

---

## 1. Why LI exists in this network

A 5G operator is not allowed to *opt out* of Lawful Intercept. Every
country with a working telecom regulator requires the operator's
network to:

1. **Provision a warrant for a named target** issued by a competent
   authority (court, regulator, designated security agency).
2. **Collect the agreed information** for the duration of the warrant
   — either *signalling-only* (who registered when, with whom they
   talked) or *signalling plus content* (the actual packets).
3. **Deliver that information to a Law-Enforcement Agency** through a
   spec-defined handover interface.
4. **Audit every step** so a regulator can prove the operator only
   intercepted what the warrant authorised, no more.

LI is therefore a **legal compliance feature**, not a product
differentiator. The 3GPP architecture (TS 33.127) splits the work
across four roles, and the network is required to implement enough of
each that a regulator's audit can succeed.

---

## 2. The four LI roles (and where this product sits)

| Role | Spec name | What it does | Where it lives in this product |
|------|-----------|--------------|--------------------------------|
| **ADMF** | Administrative Function | Receives warrants from the LEA, provisions POIs, controls activation/deactivation, holds the audit trail. | In-process inside the core: warrant CRUD + audit log via the OAM panel and `/api/li/*`. |
| **POI** | Point Of Interception | Sits inside a Network Function (AMF, SMF, UPF) and emits an IRI/CC event when a target's traffic is observed. | Hooks inside AMF (registration / deregistration) and SMF (PDU session establish / release) call the LI surface. |
| **TF** | Triggering Function | Tells a POI when to start / stop on a target. | Collapsed into the ADMF here: a single per-IMSI cache (`activeTargets`) rebuilt on every warrant CRUD action drives all POIs in the same process. |
| **MDF / LEMF** | Mediation Function / Law-Enforcement Mediation Function | Reformats events into the spec-defined handover encoding and delivers them to the LEA. | Operator runs a real MDF endpoint; the core's X2 / X3 deliverers POST captured events to the warrant's `mdf_endpoint`. The local stack ships the warrant payload as a JSON envelope; the TS 33.128 ASN.1 stage-3 wire is deferred. |

This product **collapses ADMF + POI into one in-process surface**.
That is acceptable for a single-box deployment; production
multi-vendor deployments with a separate ADMF appliance would
replace the in-process surface with a remote X1 listener — the
operator's POI hooks would be unchanged. The X1 verbs themselves
(provision / modify / deactivate) are exposed today at
`/api/li/x1/*` so OAM scripts and tester runbooks can speak in
spec terms; pointing those routes at a remote ADMF is a deployment
decision, not a code change.

---

## 3. What LI captures

There are two intercept products, and each warrant explicitly chooses
one or both:

| Product | What it is | What gets captured | When the operator can deliver |
|---------|-----------|--------------------|-------------------------------|
| **IRI** (Intercept Related Information) | Signalling metadata. | UE registration / deregistration, PDU session establish / release, location updates, handovers, etc. | On every event, in near real-time. The events themselves are the output. |
| **CC** (Communications Content) | The user-plane bytes themselves. | Packet stream from / to the target's PDU session(s). | Stream-as-it-happens — by spec the UPF duplicates packets to the MDF as they're forwarded. |

The warrant carries a **scope** of `iri`, `cc`, or `iri+cc` depending
on what the issuing authority authorised. A `cc`-only warrant must
**not** also produce IRI rows; the separation is enforced inside the
capture path so a regulator can trust the audit log.

### 3.1 The IRI events the network emits today

Each event below maps to a spec event in TS 33.128 §6.2 and is
emitted automatically when the underlying NF transition happens — no
extra operator action is needed once a warrant exists.

| When the UE does this | The NF that observes it | The IRI event recorded |
|-----------------------|-------------------------|------------------------|
| Registers to 5GS | AMF | `REGISTER` |
| Deregisters | AMF | `DEREGISTER` |
| Establishes a PDU session | SMF | `PDU_SESSION_ESTABLISHMENT` |
| Releases a PDU session | SMF | `PDU_SESSION_RELEASE` |

Each event carries the operator-meaningful payload (IMSI, allocated
UE IP, DNN, gNB id, …). The 3GPP stage-3 ASN.1 encoding (TS 33.128)
remains a deferred surface — the local product ships the payload as a
JSON blob until that wire is in scope.

### 3.2 What CC capture means today

A `cc`-scoped warrant exercises two surfaces:

1. A **session metadata row** in `li_cc_sessions` recording that
   the SMF authorised CC for the target's PDU session. The session
   row carries `OPENED` / `CLOSED` lifecycle markers tied to the
   PDU session lifecycle.
2. The **X3 deliverer** ships those lifecycle phases to the
   warrant's `mdf_endpoint` (TS 33.127 §6.4). An MDF receiving
   `OPENED` knows to start expecting per-packet content for that
   session id; on `CLOSED` it knows to roll the recording up.

The **per-packet content fork** — UPF duplicating user-plane
packets onto X3 — remains roadmap. A regulator reading today's
audit trail sees the warrant authorisation, the session activation,
and the X3 lifecycle delivery; the per-packet bytes are the next
piece. When that piece lands, the same X3 channel will carry frame
events alongside `OPENED` / `CLOSED`, so MDF integrations built
against today's wire keep working.

---

## 4. Warrant lifecycle — operator's day-to-day

### 4.1 Authoring a warrant

A warrant lands in the system through one of two doors that share
the same data path:

- **`POST /api/li/warrant`** — the operator-panel surface. Used by
  the OAM dashboard and runbook scripts.
- **`POST /api/li/x1/provision`** — the role-named ADMF→POI
  surface (TS 33.127 §6.2 X1). Verbs: `provision` / `modify` /
  `deactivate`. Deployments running a remote ADMF point that
  ADMF at this URL.

Either door produces the same `li_warrants` row + `warrant_created`
audit entry; the X1 path additionally lays an `x1_provision` audit
row so the trail distinguishes ADMF-driven calls from operator-
panel calls.

Required and optional fields:

- **`warrant_id`** — a unique handle the audit log references. Often
  an externally-meaningful case file id.
- **`authority`** — the issuing authority (court name, regulator
  reference). Free-text but **must be filled** for a valid warrant.
- **`case_reference`** — the LEA's case id; appears in every audit
  row.
- **`target_imsi`** — the subscriber to intercept. (MSISDN can also
  be supplied for cross-reference but the matching key is IMSI.)
- **`scope`** — `iri`, `cc`, or `iri+cc`.
- **`start_time` / `end_time`** — RFC3339-ish strings; defaulting to
  *now* and *now + 30 days* if blank. Auto-expiry trims the warrant
  the moment its `end_time` passes.
- **`mdf_endpoint`** — HTTPS URL of the operator's MDF receiver. If
  set, the X2 / X3 deliverers will push captured events to that
  endpoint as they happen; if blank, the events stay queued and the
  operator drives delivery via the OAM `mark-delivered` surface.

The catalog visible in the OAM panel reflects exactly what was
authored — there is no hidden state the operator can't see.

### 4.2 Steady-state operation

Once active, the warrant participates in four loops:

1. **Per-event capture loop** — every UE in the network produces NF
   events. The capture path looks up the IMSI in the `activeTargets`
   cache; for non-targeted UEs this is a single map miss and adds
   no measurable latency. For matching UEs, an event row appears in
   the IRI store and (for cc/iri+cc scopes) a CC session row opens.
2. **Per-tick expiry loop** — a periodic task flips warrants past
   their `end_time` from `active` to `expired` and drops them from
   the cache. The transition is auditable.
3. **X2 IRI delivery loop** — when `li_x2_enabled=1`, a background
   worker polls `li_iri_events` for pending rows belonging to
   warrants with a non-empty `mdf_endpoint`, batches them per
   warrant, and POSTs to `{mdf_endpoint}/x2/iri`. On HTTP 2xx the
   batch is flipped to `delivered=1` and an `x2_delivered` audit
   row is laid; on failure the rows stay pending and retry on the
   next tick (the queue is the buffer per TS 33.127 §6.3).
4. **X3 CC delivery loop** — same shape for `li_cc_sessions`:
   `OPENED` and `CLOSED` lifecycle phases ship to
   `{mdf_endpoint}/x3/cc` (TS 33.127 §6.4). Per-row
   `cc_opened_delivered` / `cc_closed_delivered` flags track which
   phases have shipped, so a session that opens and closes inside a
   single tick gets both events delivered.

The X2 / X3 toggles default to **off** so a fresh deployment without
configured MDFs cannot accidentally exfiltrate. Operators flip the
flag in `network_config` once each warrant's `mdf_endpoint` is set.

### 4.3 Revocation and emergency stop

Two ways a warrant ends early:

- **Operator revokes it** (LEA cancelled the case, court order
  vacated). One REST call; the cache is rebuilt the same tick; the
  audit log records `warrant_revoked` with the operator identity.
- **Auto-expiry** when `end_time` passes (the most common path).
  Audit row: `warrant_expired`.

In both cases the next NF event for that IMSI is **not captured** —
the cache no longer matches, so the per-event capture loop returns
without writing.

---

## 5. The audit story (why it matters)

A regulator who walks in and asks "show me everything you did under
this warrant" can read:

```
li_audit_log    — every operator action (create / revoke / delete /
                  expire) with timestamp + operator identity
li_warrants     — the warrants themselves, with status transitions
li_iri_events   — every IRI captured under the warrant
li_cc_sessions  — every CC session that was opened under the warrant
```

The audit log is **append-only** in semantics (the SQL surface is a
plain INSERT; the operator-facing routes never UPDATE or DELETE
audit rows). A future hardening pass can add cryptographic chaining
(TS 33.127 §8 envisions it) — until then the trail is "trust the
DBA, but the application code never lets the GUI / API rewrite the
log".

Every action that touches a warrant carries the **operator
identity**: explicit `X-LI-Operator` header, falling back to BasicAuth
user, falling back to client IP. There is no anonymous LI action.

---

## 6. Access control

The LI surface is gated by an `X-LI-Auth-Token` header checked on
every `/api/li/*` request. The token lives in
`network_config.li_auth_token` — DB-driven, rotated from the
operator panel, never compiled in. Empty stored token = "auth
disabled" (dev / first-boot mode); production deployments rotate the
token immediately.

This is the **minimum-viable enforcement** of TS 33.127 §5.2 "LI
administrative function security". The fuller mTLS-based ADMF/LEMF
chain (TS 33.127 §6) is on the roadmap; the token gate is what stops
an unauthenticated client on the same network from reading warrants
or pulling IRI events today.

---

## 7. Use cases this feature enables

| Use case | What LI provides | What still needs to happen elsewhere |
|----------|------------------|--------------------------------------|
| **Court-ordered surveillance of a specific subscriber** | Per-IMSI warrant with iri+cc scope; per-event IRI capture; CC session activation tied to the warrant; X2 IRI + X3 CC-lifecycle delivery to the operator's MDF. | The TS 33.128 ASN.1 stage-3 wire (the local stack ships JSON envelopes — works against an in-house MDF, needs a reformatter for an external LEMF) and the UPF per-packet content fork on X3. |
| **Regulatory audit ("prove you followed the warrant's scope")** | The full audit log + IRI store + CC session metadata cross-referenced by `warrant_id`. | Cryptographic chaining of audit rows (TS 33.127 §8) for tamper-evidence. |
| **Time-bounded interception with auto-rollback** | `end_time` field + `ExpireWarrants` tick; revocation REST call; cache rebuild on every state transition. | Operator-side scheduler that revokes pre-expiry on LEA cancellation (today the operator must hit the route by hand). |
| **Multi-warrant on one target** | One IMSI can have multiple active warrants (different LEAs, different scopes). The capture path fans events out to every matching warrant. | LEA-side dedup (regulator policy, not the operator's job). |
| **Emergency stop ("revoke this immediately")** | Revoke route → cache rebuild on the next tick → next event is not captured. | None — this works today. |

---

## 8. In scope vs. out of scope

**In scope (delivered today):**

- Operator-curated warrant store with the four required identity
  fields + scope + validity window + MDF endpoint.
- Per-IMSI in-process cache with sub-millisecond lookup so the
  capture path does not penalise non-targeted traffic.
- IRI capture for the four core mobility / session events
  (registration, deregistration, PDU establish, PDU release).
- CC session activation / deactivation tied to scope and PDU
  session lifecycle.
- Audit log covering CRUD verbs + X1 verbs + auto-expire + revoke
  + IRI capture + CC activation + X2 / X3 deliveries.
- Auto-expiry by `end_time`, swept by a periodic ticker.
- Token-gated REST surface with operator-identity capture.
- **X1 (TS 33.127 §6.2 ADMF→POI provisioning)**: role-named
  `provision` / `modify` / `deactivate` verbs at `/api/li/x1/*`,
  on top of the same primitives the operator panel uses.
- **X2 (TS 33.127 §6.3 IRI delivery)**: background worker batches
  pending IRI rows per warrant and POSTs them to the warrant's
  `mdf_endpoint`. Failure leaves rows pending (queue-as-buffer);
  `mark-delivered` remains available as the OAM-driven fallback.
- **X3 (TS 33.127 §6.4 CC delivery — lifecycle phase)**: OPENED /
  CLOSED phases of every CC session ship to the same MDF endpoint
  on a separate channel.
- DB-driven toggles (`li_x2_enabled`, `li_x3_enabled`) and cadence
  (`li_mdf_poll_interval_ms`) — re-read every tick so changes take
  effect without a restart.
- Read-only OAM views: warrant list, IRI stream per warrant, CC
  session list, audit log, delivery stats.

**In scope (delivered, but limited surface area):**

- The X2 / X3 wire envelope is **JSON**, not the TS 33.128 ASN.1
  stage-3 encoding. An in-house MDF that speaks JSON works
  end-to-end today; an external LEMF expecting ASN.1 needs a
  reformatter on the receive side.
- X3 carries **session lifecycle only** (OPENED / CLOSED). Per-
  packet content frames are the next piece; today's MDF gets the
  *fact* of CC activation on the same channel that will later
  carry the bytes.
- **Defensive default**: X2 / X3 ship with the toggles off. A
  fresh deployment must set `mdf_endpoint` on the warrants and
  enable the toggles before anything leaves the box.

**Out of scope today:**

- **UPF content fork** — no actual packet duplication onto X3
  yet. CC scope opens a metadata row + delivers OPENED/CLOSED
  lifecycle markers; per-frame bytes are roadmap.
- **Cryptographic chaining of the audit log** — append-only by
  application convention; not yet hash-chained.
- **mTLS on X1 / X2 / X3** — the token gate stops unauthenticated
  callers, but full mTLS-based ADMF/LEMF authentication
  (TS 33.127 §6.5) is on the roadmap.
- **POIs in NFs other than AMF / SMF** — UPF, IMS-AS, NWDAF, …
  could become POIs (TS 33.127 §7); only the four AMF/SMF events
  are captured today.

The boundary is deliberate: the in-scope surface — including the
three reference points — covers the functional behaviour a
single-box deployment plus an in-house MDF needs end-to-end; the
out-of-scope items are the heavier integrations (UPF dataplane
fork, ASN.1 codecs, mTLS PKI) that a multi-vendor carrier
deployment would commission as separate work packages.

---

## 9. Use-case walk-through (registration of a targeted UE)

A short narrative of what the operator and the LEA see when the
network captures a single registration:

```
LEA delivers warrant W-2026-014:
  authority    = "District Court of Example"
  case_ref     = "EX-2026-0042"
  target_imsi  = 001011234560042
  scope        = iri+cc
  end_time     = 2026-06-09 (30 days)
  mdf_endpoint = https://mdf.operator.example/li

Operator (or remote ADMF) POSTs to /api/li/x1/provision.
  → audit rows: x1_provision, warrant_created, operator=alice
  → activeTargets cache rebuilt; 001011234560042 → W-2026-014.

… some time later, the targeted UE powers up and registers …

AMF runs the registration FSM (auth → SMC → registration accept →
  registration complete). On the REGISTERED transition the AMF's
  POI hook runs:
  → li.CaptureIRI("REGISTER", 001011234560042, {…})
  → looks up activeTargets → matches W-2026-014 → scope is iri+cc →
    write IRI row, audit "iri_captured".

X2 deliverer (running with li_x2_enabled=1) wakes up next tick:
  → SELECT pending IRI for warrants with mdf_endpoint set
  → POST {…}/x2/iri to mdf.operator.example, batch of 1
  → MDF returns 200 OK
  → flip delivered=1, audit "x2_delivered"

Operator dashboard now shows:
  /api/li/warrant/W-2026-014/iri  →  1 record (REGISTER)
  /api/li/stats                    →  pending=0 delivered=1
  /api/li/audit?warrant_id=…       →  x1_provision, warrant_created,
                                       iri_captured, x2_delivered

… UE establishes a PDU session for DNN=internet …

SMF Establish path completes. POI hook fires:
  → li.CaptureIRI("PDU_SESSION_ESTABLISHMENT", …) → IRI row
  → li.CheckAndActivateCC(imsi, …)   → CC session row (status=active),
                                        audit "cc_activated"

X2 ships the new IRI; X3 ships the CC OPENED phase:
  → POST {…}/x2/iri  → 200, delivered=1, audit x2_delivered
  → POST {…}/x3/cc   → 200, cc_opened_delivered=1, audit x3_delivered

… 30 days later, end_time passes …
ExpireWarrants tick:
  → status=active && end_time<now → flip to expired, audit
    "warrant_expired"
  → activeTargets drops the entry; further events for this IMSI are
    no longer captured.
  → if the UE has a live PDU session, the SMF release path emits
    PDU_SESSION_RELEASE IRI + DeactivateCC; X3 ships the CLOSED
    phase before the CC session row goes quiet.
```

Every line in the walk-through has a corresponding audit row, which
is what makes the regulator audit succeed.

---

## 10. Glossary

| Term | Meaning here |
|------|--------------|
| **Warrant** | Operator-stored record authorising LI for a single target IMSI for a single time window. |
| **ADMF** | Administrative Function — the part of the LI architecture that manages warrants and provisions POIs. |
| **POI** | Point Of Interception — a hook inside a Network Function that emits IRI / CC when a target is observed. |
| **TF** | Triggering Function — tells a POI when to start / stop. Collapsed into the ADMF in this product. |
| **MDF / LEMF** | Mediation / Law-Enforcement Mediation Function — receives the IRI/CC stream from the operator's POI and delivers it to the LEA. Operator-supplied; the local stack POSTs JSON to its `mdf_endpoint`. |
| **IRI** | Intercept Related Information — signalling metadata captured under a warrant. |
| **CC** | Communications Content — actual user-plane bytes captured under a warrant. |
| **Scope** | What a warrant covers: `iri`, `cc`, or `iri+cc`. |
| **X1** | ADMF→POI provisioning interface (TS 33.127 §6.2). Wired at `/api/li/x1/{provision,modify,deactivate}` with JSON. |
| **X2** | POI→MDF IRI delivery interface (TS 33.127 §6.3). Wired as HTTPS POST of JSON to `{mdf_endpoint}/x2/iri`; ASN.1 stage-3 (TS 33.128) deferred. |
| **X3** | POI→MDF CC delivery interface (TS 33.127 §6.4). Wired as HTTPS POST of JSON to `{mdf_endpoint}/x3/cc`; today carries OPENED / CLOSED lifecycle phases, per-packet content fork roadmap. |
| **Audit log** | Append-only record of every action against the warrant store; the regulator reads this to prove compliance. |

---

## 11. Spec map (functional reading order)

1. **TS 33.126** — LI requirements (stage 1) — the *why* the operator
   must implement any of this.
2. **TS 33.127 §4** — LI architecture overview (the four roles).
3. **TS 33.127 §5** — Provisioning architecture (warrant lifecycle,
   ADMF surface, security in §5.2).
4. **TS 33.127 §7** — POI / CC distribution within the network
   (which NF emits which event).
5. **TS 33.127 §8** — Security and audit requirements.
6. **TS 33.128 §6.2** — Stage-3 IRI events the network emits.
7. **TS 33.501 §5.9** — the only LI hook surfaced in the local 5G
   security spec (SUPI mixed into KAMF derivation precisely for LI).

For the data model (schemas, in-memory caches, wire status), wire
status and DB schema, see [`li_data.md`](li_data.md).

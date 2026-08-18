# DPI / Application Detection — Functional Design

This is the **functional** view of MMT Studio Core's Deep Packet
Inspection / Application Detection feature: what it does for the
operator, what business outcomes it drives, how a packet is matched,
and how the result reaches the user-plane. Implementation details
(packages, schema, wire encoders) live in a companion doc.

> Spec anchor: 3GPP TS 23.501 §5.8 *User Plane Management*, with the
> Application Detection sub-clauses §5.8.2.4 (classification) and
> §5.8.2.6 (charging / usage monitoring).

---

## 1. Why DPI exists in this network

A 5G core has to do three things with user traffic that pure 5-tuple
forwarding can't do:

1. **Charge per service.** "10 GB free YouTube on weekends" is an
   operator promise, not a transport policy. Someone has to recognise
   a packet as YouTube before the charging system can apply the rule.
2. **Apply per-app QoS.** Voice flows belong on EF (DSCP 46), video on
   AF41, IoT telemetry on AF11 — the radio scheduler and the transport
   network only act on those markings if someone actually marks them.
3. **Enforce per-app policy.** Block social media on a corporate slice;
   throttle file-sharing during congestion; redirect a known-bad
   destination through DPI/security inspection.

All three need the same primitive: **given a packet, return an
application identifier**. That primitive is what the DPI / Application
Detection feature provides.

In 3GPP terms (TS 23.501 §5.8.2.4) the matching itself happens in the
UPF; the rules describing *what to match* are operator-curated and
provisioned to the UPF over PFCP. This product owns both sides — the
operator-facing curation surface and the UPF-side cache that feeds the
classifier.

---

## 2. What the feature delivers (operator view)

| Capability | What the operator gets | Business value |
|------------|------------------------|----------------|
| **Application catalogue** | A named, versioned list of "apps" the network is aware of (YouTube, WhatsApp, Teams, …). | The handle that everything else — charging, QoS, policy, dashboards — references. |
| **Detection rules per app** | Per-app rules in five flavours: TLS SNI, DNS query, IP CIDR, port range, HTTP host. | Each app can be matched the way it actually appears on the wire. Most modern traffic is TLS, so SNI is the workhorse. |
| **Default catalogue seed** | One click to populate eight common consumer apps. | Demos and labs work out-of-the-box; production operators replace it with their own list. |
| **Per-app usage** | Bytes uplink / downlink per (subscriber, app), accumulated over a rolling window. | Feeds zero-rating, fair-use enforcement, and tariff bundles. |
| **DSCP marking** | Each app carries a QoS profile that maps to a DSCP byte the UPF stamps on egress. | Aligns 5G QoS with the IP transport between UPF and the data network so per-app QoS survives outside the GTP tunnel. |
| **Live UPF cache view** | Read-only window into what rules the UPF currently has installed. | OAM can prove that a CRUD action on the catalogue actually reached the dataplane. |

The catalogue is curated by the operator (today) or pushed from a NEF
on the AF→NEF→SMF path (target). Either way, the UPF receives the same
PFCP PFD-Management push.

---

## 3. The four detection types — when each one fires

A modern UE generates a mix of TLS, plaintext HTTP, opaque IP flows,
and quirky UDP services. The matcher tries multiple angles and takes
the strongest signal:

| Trigger visible to UPF | Detection type used | Example pattern | Why it works |
|------------------------|---------------------|-----------------|--------------|
| TLS ClientHello with SNI | **SNI** (`*.youtube.com`) | Glob over the SNI string | SNI is sent in cleartext even on TLS 1.3, so the network sees it without decryption. |
| DNS query / response | **DNS** (`youtube.com`) | Exact-or-suffix match | Watching DNS lets the network *predict* what the next IP-only flow will be (see §4 pinning). |
| HTTP plaintext | **Host** (`api.example.com`) | HTTP `Host:` header | Useful for legacy services and captive portals. |
| IP-only flow, no TLS / no DNS | **IP range** (`157.240.0.0/16`) | CIDR match | Catches CDN egress, fixed enterprise destinations. |
| Service identified only by port | **Port range** (`5060-5061`) | Numeric range | SIP, RTP, custom enterprise protocols. |

Each app can carry multiple rules of different types — "match SNI
*.netflix.com OR DNS netflix.com OR IP range in Netflix's CDN
prefixes". The matcher returns the first hit, ordered by app priority
and rule specificity.

### 3.1 DNS pinning — the bridge for IP-only flows

A typical mobile flow is: UE looks up `fbcdn.net`, gets back several
IPs, then opens a TLS connection to one of them. By the time the TLS
connection arrives, there is **no SNI we trust** (might be private DNS,
might be ECH) and the IP itself is just a CDN address shared with
hundreds of services.

Pinning closes that gap:

1. When the network sees the DNS query / response, it pairs the
   resolved IP with the `app_id` derived from the queried domain.
2. The pair is cached for the DNS TTL.
3. When an IP-only flow arrives at one of those IPs, the matcher reads
   the pin and returns the same `app_id`.

Without pinning, "IP-range" rules would have to carry every CDN prefix
the operator wants to recognise — unmanageable. With pinning, the
operator only authors rules against the human-meaningful domain.

---

## 4. How a packet becomes a charged byte

The end-to-end pipeline an operator should picture:

```
   ┌───────────────┐
   │ UE / RAN      │
   └──────┬────────┘
          │   (GTP-U over N3)
          ▼
   ┌──────────────────────────────────────────────┐
   │ UPF — 5-tuple forwarding *first*             │
   │                                              │
   │  per-flow PDR look-up                        │
   │     │                                        │
   │     ▼                                        │
   │  Application Detection                       │
   │     ├── SNI?  ──→ SNI rule cache             │
   │     ├── DNS?  ──→ DNS rule cache + pin       │
   │     ├── IP?   ──→ pin first, then IP CIDR    │
   │     └── port? ──→ port range cache           │
   │                                              │
   │  Result: app_id, qos_profile, charging_id    │
   │                                              │
   │  ── per-app QoS Enforcement Rule (QER) ──    │
   │  ── DL Flow Level Marking → DSCP byte ──     │
   │  ── per-app Usage Reporting Rule (URR) ──    │
   └──────┬───────────────────────────────────────┘
          │   (IP, marked with DSCP from app profile)
          ▼
     Data Network (Internet / DN) — keeps the DSCP marking,
     so transport routers honour the per-app priority outside
     the GTP tunnel.

   ┌───────────────┐         ┌──────────────────────┐
   │ Detection     │  feeds  │ Charging / Usage     │
   │ event log     │ ──────► │ Summary (per UE,     │
   │ (UE, app,     │         │ per-app, rolling     │
   │  bytes UL/DL) │         │  window)             │
   └───────────────┘         └──────────────────────┘
```

Three things the operator should remember:

- **The classifier is per-flow, not per-packet.** The first packet of a
  flow carries the SNI / DNS query that picks the app; subsequent
  packets ride that decision until the flow ends.
- **The DSCP byte applies to the IP packet that egresses the UPF**, so
  it survives the GTP tunnel and reaches the transport network. This
  is what makes "voice on EF" work end-to-end.
- **The usage summary is collapsed.** Repeated detections of the same
  (UE, app) inside a rolling window accumulate into one row instead of
  flooding the database. Per-call detail is still available in the
  event log if needed.

---

## 5. App lifecycle — operator's day-to-day

### 5.1 Authoring a new app

1. **Create the app** with `app_id`, display name, category (video /
   voice / iot / messaging / general), QoS profile, charging profile,
   and priority.
2. **Add detection rules** — at least one per visible angle (SNI for
   the TLS path, DNS for the resolver path, IP range for fallback).
3. The system **automatically pushes the new rule set to every UPF** in
   the network. There is no separate "deploy" step.
4. The UPF cache is queryable, so the operator can confirm the rules
   landed before sending live traffic.

### 5.2 Editing or disabling an app

- Editing a rule is a delete-then-add; the next push reaches the UPF.
- Disabling an app keeps its rules but stops the matcher from returning
  it — useful for quick A/B tests of "what happens if we stop
  zero-rating WhatsApp".
- Deleting an app prunes its rules and **explicitly tells the UPF to
  forget it** — important so a retired app's stale CDN ranges don't
  keep matching live traffic.

### 5.3 Steady-state observation

The operator dashboard shows three live numbers per app:

- **Detection count** — how often the matcher fired in the last window.
- **Bytes UL / DL** — usage attributed to the app.
- **Top subscribers** — which IMSIs are driving that usage (privacy
  controls apply; this is the same surface charging/zero-rating uses).

A second pane shows the **UPF-side cache** — exactly what the dataplane
believes today. Discrepancies between the catalogue and this view are
the canary that a push didn't land.

---

## 6. Use cases this feature enables

| Use case | What DPI provides | What still needs to happen elsewhere |
|----------|-------------------|--------------------------------------|
| **Zero-rated bundles** ("free WhatsApp on weekends") | Per-app byte counters on the right (UE, app) granularity. | Charging system reads the counters and applies the tariff rule. |
| **Slice-aware app policy** ("Block TikTok on enterprise slice") | The matcher returns `app_id`; the UPF can then drop / redirect by app. | Slice-level policy authoring (slicing.md). |
| **Per-app QoS uplift** ("Voice apps get EF priority end-to-end") | The DSCP byte is set from the app's QoS profile and stamped on egress. | Transport network must honour DSCP — operator has to verify this with the IP backbone team. |
| **Application-aware analytics** ("Which apps are driving congestion?") | Detection events with timestamps; usage summary per app. | NWDAF / analytics consumes the event stream. |
| **Parental controls / regulatory blocking** | Operator can flag a category (e.g. gambling) and use the matcher to detect any member. | Block / redirect action in the UPF's policy stage. |
| **Encrypted-traffic identification fallback** | DNS pinning + SNI when visible; IP-range catalogue for known CDN footprints. | ML-based classification for fully opaque flows is out of scope today. |

---

## 7. What is in scope vs. out of scope

**In scope (delivered today):**

- Operator-curated catalogue + rule store with five detection types.
- Live SMF→UPF rule push over standard PFCP PFD-Management
  (TS 29.244 §6.2.5), with per-app removal so the UPF cache mirrors
  the catalogue exactly.
- Per-app DSCP marking via DL Flow Level Marking on the egress QER.
- Per-(UE, app) byte counters with a rolling-window dedup.
- DNS pinning so IP-only flows still resolve to an app.
- A read-only OAM view of the UPF cache.

**In scope (delivered, but limited surface area):**

- Default seed catalogue (8 well-known consumer apps) for demos / labs.
- Five-detection-type vocabulary covers most consumer + enterprise
  needs; new types would extend the matcher and the UPF cache schema.

**Out of scope today:**

- **Northbound PFD authoring from a third-party AF** (TS 29.122 T8) —
  the catalogue is curated locally; AF→NEF→SMF push is a documented
  spec target, not wired.
- **ML-based encrypted-traffic classification** — only the rule-driven
  matchers above; opaque flows that don't match a rule fall through to
  "unknown" and are charged on the default profile.
- **Per-flow accounting at packet granularity** — the usage summary
  collapses repeats inside a rolling window; per-packet records are
  not retained.
- **Cross-UPF detection state sharing** — each UPF has its own cache;
  there is no inter-UPF gossip if a session moves anchor.

The boundary is deliberate: the in-scope surface is enough to drive
charging, QoS, and OAM; the out-of-scope items are either deferred
(T8) or non-3GPP (ML).

---

## 8. Glossary

| Term | Meaning here |
|------|--------------|
| **App / `app_id`** | Operator-named handle (e.g. `youtube`) that everything else references. |
| **PFD** | Packet Flow Description — a single rule (one detection type + one pattern). |
| **PFCP PFD-Management** | The standard procedure (TS 29.244 §6.2.5) the SMF uses to ship PFDs to the UPF. |
| **DSCP** | Six-bit IP header field carrying per-packet priority. RFC 4594 maps service classes (voice → EF, video → AF41, …) to DSCP values. |
| **QER** | QoS Enforcement Rule — what the UPF applies once the matcher has identified the app. The DSCP byte rides this rule. |
| **URR** | Usage Reporting Rule — counters the UPF maintains for charging input. |
| **DNS pinning** | Caching `(domain → app)` so a later IP-only flow to one of the resolved IPs still returns the right app. |
| **Charging profile** | Operator-named bucket (e.g. `flat-rate`, `zero-rated`) that the charging system uses to decide what to do with the bytes. |
| **QoS profile** | Operator-named bucket (e.g. `voice`, `video`, `low_latency`, `iot`, `default`) that selects the DSCP byte. |

---

## 9. Spec map (functional reading order)

If a reader wants to validate this functional view against 3GPP, the
short reading list is:

1. **TS 23.501 §5.8** — User Plane Management (overall context).
2. **TS 23.501 §5.8.2.4** — Traffic Detection (what the matcher is).
3. **TS 23.501 §5.8.2.4.2** — Traffic Detection at the UP function
   (where the rules live).
4. **TS 23.501 §5.8.2.6** — Charging and Usage Monitoring Handling
   (the per-app counters).
5. **TS 23.501 §5.8.2.8.4** — PFD Management (the lifecycle + the
   NEF/PFDF→SMF→UPF distribution chain).
6. **TS 29.244 §6.2.5** — PFCP PFD Management Procedure (the wire).
7. **TS 23.503** — Policy and Charging Control framework (where the
   charging / QoS profiles ultimately come from).

The companion implementation doc maps each of those clauses to the
exact code, schema, and on-the-wire IE encoders.

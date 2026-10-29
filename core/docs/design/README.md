# MMT Studio Core — Design Docs Index

One design doc per feature. Each doc is grounded in the source: spec
clauses (`TS X.YYY §a.b.c`) only appear when the same citation is in
the corresponding Go/C source. Stubs and TODOs are listed honestly,
with `file:line` refs into the code so the doc and the code stay in
sync.

For runbook/operator material see [`OBSERVABILITY.md`](../OBSERVABILITY.md)
and [`PERFORMANCE.md`](../PERFORMANCE.md).
The original UPF deep-dive lives at [`../../nf/upf/DESIGN.md`](../../nf/upf/DESIGN.md).

## Cross-cutting

| Topic | Doc | Role |
|-------|-----|------|
| Network Slicing | [slicing.md](slicing.md) | Umbrella — catalogue, NSSF/SMF/NSACF/NSaaS/NPN composition |

---

## Network functions (`nf/`)

| NF | Doc | Role |
|----|-----|------|
| AMF | [nf/amf.md](nf/amf.md) | Access & Mobility Mgmt — NGAP, GMM FSM, security single-owner, N1/N2 |
| SMF | [nf/smf.md](nf/smf.md) | Session Mgmt — 5GSM FSM, PFCP client, PTI tracker, IP allocator |
| UPF | [../../nf/upf/DESIGN.md](../../nf/upf/DESIGN.md) | User Plane — PFCP, GTP-U, DPDK dataplane *(separate file)* |
| AUSF | [nf/ausf.md](nf/ausf.md) | 5G-AKA AV generation, SUCI deconcealment |
| UDM | [nf/udm.md](nf/udm.md) | UEAU/UECM/SDM, SQN cache, AMBR |
| UDR | [nf/udr.md](nf/udr.md) | Subscription store, 5QI table |
| PCF | [nf/pcf.md](nf/pcf.md) | PCC rules, Npcf_SMPolicyControl FSM, URSP |
| AF | [nf/af.md](nf/af.md) | Npcf_PolicyAuthorization, traffic influence |
| CHF | [nf/chf.md](nf/chf.md) | CDR generation, online quota cycle |
| NSSF | [nf/nssf.md](nf/nssf.md) | Slice selection — Allowed-NSSAI computation |
| NSACF | [nf/nsacf.md](nf/nsacf.md) | Slice admission quotas, MBR enforcement |
| NWDAF | [nf/nwdaf.md](nf/nwdaf.md) | Analytics ID catalogue, collection loop |
| LMF | [nf/lmf.md](nf/lmf.md) | Positioning kernels (E-CID/MultiRTT/TDOA/AoD/AoA/A-GNSS) |
| GMLC | [nf/gmlc.md](nf/gmlc.md) | Le delegate to LMF |
| SMSF | [nf/smsf.md](nf/smsf.md) | NAS-borne SMS — TS 24.011 / TS 23.040 codecs |
| N3IWF | [nf/n3iwf.md](nf/n3iwf.md) | Untrusted-WLAN attach — IKEv2 + EAP-5G + ESP↔GTP-U |

## Security (`security/`)

| Module | Doc | Role |
|--------|-----|------|
| LI | [security/li.md](security/li.md) | Lawful Intercept — ADMF + POI; X1/X2/X3 deferred |
| DPI | [security/dpi.md](security/dpi.md) | App catalogue, PFD ruleset, four-prong classifier |
| Core security | [security/core_security.md](security/core_security.md) | Signalling-perimeter firewall/IDS/audit (NEA/NIA live in `nf/amf/security/`) |
| NPN | [security/npn.md](security/npn.md) | SNPN / PNI-NPN, CAG admission |
| RAN sharing | [security/ran_sharing.md](security/ran_sharing.md) | MORAN/MOCN agreements, per-gNB allocation |

## Access (`access/`)

| Module | Doc | Role |
|--------|-----|------|
| EPC | [access/epc.md](access/epc.md) | 4G interworking — S1AP shim, EMM/ESM, mapped contexts (peer to `nf/amf/n26`) |
| NTN | [access/ntn.md](access/ntn.md) | Non-terrestrial — constellation, ephemeris, visibility, feeder link |
| Wi-Fi offload | [access/wifi_offload.md](access/wifi_offload.md) | Per-DNN policy + admission probe; ATSSS as preference (datapath in `nf/n3iwf`) |

## OAM (`oam/`)

| Module | Doc | Role |
|--------|-----|------|
| trace | [oam/trace.md](oam/trace.md) | Signalling trace — `trace_records`, /api/traces |
| otel | [oam/otel.md](oam/otel.md) | OpenTelemetry exporter scaffolding |
| pm | [oam/pm.md](oam/pm.md) | Counter registry, ring buffer, peak rates, Prometheus |
| fm | [oam/fm.md](oam/fm.md) | Alarm registry, X.733 vocabulary, dedup correlation |
| ai | [oam/ai.md](oam/ai.md) | AI router (Local/Anthropic/OpenAI/Gemini), closed-loop |
| logger | [oam/logger.md](oam/logger.md) | Ring + drainer, IMSI tagging, JSON sink |
| cpupin | [oam/cpupin.md](oam/cpupin.md) | CPU pinning / NUMA |
| platform | [oam/platform.md](oam/platform.md) | /proc + /sys probes — NUMA / hugepage / NIC / VFIO |
| banner | [oam/banner.md](oam/banner.md) | Startup banner + sysctl checks |

## Services (`services/`)

| Service | Doc | Role |
|---------|-----|------|
| IMS | [services/ims.md](services/ims.md) | P-/I-/S-CSCF, MMTel, conference AS, MRFP mixer |
| MCX | [services/mcx.md](services/mcx.md) | MCPTT/MCData/MCVideo — floor controller, on-/off-network FSM |
| eSIM | [services/esim.md](services/esim.md) | SGP.22 RSP — esim/profile/smdp tiers |
| ProSe | [services/prose.md](services/prose.md) | D2D — Direct Discovery (Models A/B), unicast/groupcast/relay |
| V2X | [services/v2x.md](services/v2x.md) | PQI table, NAS delivery (TS 24.587 §5) |
| FRMCS | [services/frmcs.md](services/frmcs.md) | REC over MCPTT emergency group call |
| UAS | [services/uas.md](services/uas.md) | Drone registry, flight authorization, no-fly, C2 link |
| Supplementary | [services/supplementary.md](services/supplementary.md) | CFU/CFB/CFNRy/CFNRc/CW/OIP/OIR/TIP/TIR/BAOC/BAOIC/BAIC |
| SEAL | [services/seal.md](services/seal.md) | GMS/CMS/LMS/IdMS slice of TS 23.434 |
| NSaaS | [services/nsaas.md](services/nsaas.md) | Slice-as-a-service lifecycle FSM |
| PIN | [services/pin.md](services/pin.md) | Personal IoT Network — registry, gateway, relay |
| USSD | [services/ussd.md](services/ussd.md) | Menu tree FSM, 180s timeout, topup |

## Edge (`edge/`)

**Edge Computing umbrella** — single doc with Part A (functional)
+ Part B (design). Covers EAS + MEC + EASDF + AF-influence + ULCL.
**Does NOT cover positioning or sensing** — those are independent
top-level areas indexed under [Positioning](#positioning-positioning)
and [Sensing](#sensing-sensing) below.

| Doc | Role |
|-----|------|
| [edge/edge_computing.md](edge/edge_computing.md) | **Edge Computing umbrella** — Part A functional / Part B design. |
| [edge/edge_computing_data.md](edge/edge_computing_data.md) | *(redirect)* — content merged into `edge_computing.md`. |

Per-package design docs:
| Module | Doc | Role |
|--------|-----|------|
| EAS | [edge/eas.md](edge/eas.md) | Edge Application Server — TS 23.558 |
| MEC | [edge/mec.md](edge/mec.md) | MEC orchestrator — sites, apps, AF-influence rules, ULCL state (TS 23.501 §5.6.4 + §5.6.5, TS 23.502 §4.3.6) |
| TSN | [edge/tsn.md](edge/tsn.md) | Time-Sensitive Networking — TS 23.501 §5.27 |

## Sensing (`sensing/`)

Service tier for **sensing capabilities** the operator runs on top
of the radio — independent of edge computing. The radio waveform
that carries voice / data also reflects off objects in the
environment; sensing-capable receivers recover range, velocity,
presence, motion from those reflections.

| Doc | Role |
|-----|------|
| [sensing/isac.md](sensing/isac.md) | **ISAC** — Integrated Sensing and Communication (TS 22.137). `sensing/isac/` package + `/api/isac/*` operator surface. Sessions FSM, data path, NEF-side consumer registry, subscriptions. |

## Positioning (`positioning/`)

Network-side LCS (LMF / GMLC) and the operator REST surface that
ties them together — **independent of edge computing**. The PC5
sidelink ranging package (TS 23.586) lives under `positioning/`
in the codebase as well.

| Doc | Role |
|-----|------|
| [positioning/positioning.md](positioning/positioning.md) | **Umbrella** — Part A functional / Part B design. LCS via GMLC, Determine-Location via LMF, gNB / antenna / PRS provisioning, LCS privacy gate (TS 23.271 §9), geofences, the `/api/{location,gnb,prs}/*` operator surface. |
| [positioning/ranging.md](positioning/ranging.md) | UE-to-UE **sidelink** positioning over PC5 (TS 23.586) — `positioning/ranging/` package. Separate architecture from network-side LCS — no on-wire overlap. |
| [nf/lmf.md](nf/lmf.md) | Per-NF — positioning kernels (E-CID / multi-RTT / DL-TDOA / DL-AoD / UL-AoA / A-GNSS / hybrid) |
| [nf/gmlc.md](nf/gmlc.md) | Per-NF — Le-side LCS gateway, QoS shaping, LMF delegate |

## Safety (`safety/`)

| Module | Doc | Role |
|--------|-----|------|
| Emergency | [safety/emergency.md](safety/emergency.md) | E911 / eCall |
| PWS | [safety/pws.md](safety/pws.md) | CMAS / ETWS — operator state; AMF dispatcher in `nf/amf/pws` |
| MBS | [safety/mbs.md](safety/mbs.md) | Multicast/Broadcast — TS 23.247 |
| IOPS | [safety/iops.md](safety/iops.md) | Isolated Ops for Public Safety |
| Disaster roaming | [safety/disaster_roaming.md](safety/disaster_roaming.md) | TS 23.501 §5.40 |
| Access | [safety/access.md](safety/access.md) | Restricted access / lockdown |
| RACS | [safety/racs.md](safety/racs.md) | Restricted Access Control (lockdown levels) — **not** Radio Capabilities Sig |
| TCS | [safety/tcs.md](safety/tcs.md) | Tactical Comms Sync (LWW-CRDT) — **not** Trusted/Critical Services |

## IoT (`iot/`)

| Module | Doc | Role |
|--------|-----|------|
| NB-IoT | [iot/nbiot.md](iot/nbiot.md) | PSM / eDRX / capabilities |
| NIDD | [iot/nidd.md](iot/nidd.md) | Non-IP Data Delivery — sessions, MO/MT, CP CIoT |
| RedCap | [iot/redcap.md](iot/redcap.md) | Reduced-capability NR — RAT-type decisions |
| Ambient IoT | [iot/ambient.md](iot/ambient.md) | TS 22.369 — tag/reader CRUD, inventory log |

## Codecs (`codecs/`)

| Codec | Doc | Role |
|-------|-----|------|
| ASN.1 | [codecs/asn1-go.md](codecs/asn1-go.md) | X.680/X.691 compiler — lexer→parser→AST→resolver→codegen→runtime |
| NAS TLV | [codecs/tlv-3gpp-nas.md](codecs/tlv-3gpp-nas.md) | YAML→codegen — 5GMM/5GSM/EMM/ESM messages |
| PFCP TLV | [codecs/tlv-3gpp-pfcp.md](codecs/tlv-3gpp-pfcp.md) | YAML→codegen — 23 messages × 108 IE types |

## AI engine

| Module | Doc | Role |
|--------|-----|------|
| ai_engine | [ai_engine/ai_engine.md](ai_engine/ai_engine.md) | Pipeline + protocol contracts (mostly stubs) |

---

## Conventions

- **Spec citations grounded in code.** A `TS 23.501 §5.7` cite in a
  doc means that exact `§5.7` cite is in the source file the doc
  describes. Speccheck (`go test ./nf/tools/speccheck/...`) verifies
  every code-side citation against the local PDF.
- **Stubs and TODOs are honest.** Each doc has a "What's not
  implemented" section listing TODOs with `file:line` refs.
- **`Last refreshed against commit <sha>` footer** on every doc — when
  it goes stale, regenerate.

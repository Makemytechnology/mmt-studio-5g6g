<!-- Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later. -->

# mmt-studio-core

**A spec-grounded 5G SA Core network in Go + C/DPDK.**

Single-binary deployment with a web GUI, real-time NGAP/S1AP/NAS signalling on
real kernel SCTP, a DPDK-accelerated UPF data plane, and 1 245 verified §
citations grounded in local 3GPP / IETF specs. Designed for research,
private-network operators, and 3GPP-compliance development — not as a
toy stack.

[![License: AGPL v3](https://img.shields.io/badge/License-AGPL_v3-blue.svg)](LICENSE)
[![3GPP Release](https://img.shields.io/badge/3GPP-Rel--19-success)](specs/3gpp/)
[![DPDK](https://img.shields.io/badge/DPDK-25.11-orange)](libs/dpdk-25.11/)
[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8)](go.mod)
[![Platform](https://img.shields.io/badge/platform-linux--x86__64%20%7C%20linux--aarch64-lightgrey)]()

---

## At a glance

- **5GS control plane** — AMF, SMF, AUSF, UDM, UDR, NSSF, PCF, CHF, SMSF, AF,
  NSACF, LMF, NWDAF, GMLC. Per-NF FSMs (AMF GMM, AMF NGAP-per-UE, SMF session,
  SMF PFCP-per-UPF, PCF SMPolicy, UDM UECM, AF) on a generic FSM kernel.
- **5GS user plane** — Go control + cgo bridge + C/DPDK fast path
  (`libupf_dp.so`). Full TS 29.244 §7.5 PFCP surface (Establish / Modify /
  Delete / Query, plus §7.4.4.5 Association Release and §7.4.6 Session Set
  Deletion).
- **EPC interworking** — MME with S1AP + EMM + ESM + N26 to AMF.
- **Services** — IMS (SIP / CSCF / MRFP), MCX (MCPTT / MCData / MCVideo),
  eSIM, V2X, PROSE, SEAL, UAS, USSD, FrMCS.
- **Real crypto** — Milenage f1/f2345/f5\* (TS 35.205/206/207 Test Set 1
  byte-exact), 5G KDF (TS 33.501 Annex A), AES-CMAC (RFC 4493 all 4
  vectors), NEA2/NIA2 (TS 33.401 Annex B).
- **Spec citation verifier** — `nf/tools/speccheck` resolves every
  `TS NN.NNN §X.Y` and `RFC NNNN §X` mention against a local PDF / RFC
  text. Strict by default. **1 245 / 1 245 grounded** at v2.1.0.
- **Web GUI** — Chi router, pongo2 templates, 69 HTML panels, 549+ REST
  endpoints. Bundled — no CDN.
- **Self-contained** — DPDK 25.11 source ships in-repo; pure-Go SQLite
  (no cgo for DB); SIP, FSM, crypto libs all under `libs/`.

## Documentation

Design + operations docs live in [docs/](docs/). Start with the
design index for a tour of the network functions, the security
stack, OAM, services, edge, sensing, and positioning subsystems.

| Doc | When to read |
|---|---|
| [docs/design/README.md](docs/design/README.md) | design-docs index — NFs, security, access, OAM, services, edge, sensing, positioning |
| [docs/PERFORMANCE.md](docs/PERFORMANCE.md) | measured benchmarks (16/32/64/128-UE control-plane, 8-UE × 1 Mbps data-plane) |
| [docs/OBSERVABILITY.md](docs/OBSERVABILITY.md) | logging, metrics, speccheck citation gate |

## Status

**Pre-release / private preview.** The repository is currently published as
a private repo at `github.com/Makemytechnology/mmt-studio-core` for invited
collaborators and partners. The codebase is functionally complete for the
NFs listed above and runs end-to-end through registration, PDU session
establishment, user-plane data flow at line rate per UE, and graceful
release. See [docs/PERFORMANCE.md](docs/PERFORMANCE.md) for measured benchmarks
(16 / 32 / 64 / 128 UE control-plane runs, 8-UE × 1 Mbps bidirectional
data-plane run).

System-level testing is covered by a separate companion **tester** product,
released alongside the core. The unit tests in this repo are an internal
development asset — they're stripped from public release artefacts.

## Quick start

```bash
git clone https://github.com/Makemytechnology/mmt-studio-core.git
cd mmt-studio-core

# One-command install: DPDK → libupf_dp.so → sacore-web binary → sysctl tuning
./install.sh

# Run (elevates to root for hugepages + SCTP + raw sockets)
./run.sh

# Web GUI:  http://localhost:5000
# NGAP:     :38412 (kernel SCTP)
# PFCP:     :8805 (UDP)
```

Or via `make`:

```bash
make            # Build sacore-web binary
make dpdk       # Build DPDK 25.11 from in-repo source
make upf        # Build UPF C dataplane (libupf_dp.so)
make test       # Run all test packages
make run        # Build + run via run.sh
make package    # dist/mmt-studio-core-YYYYMMDD.tar.gz
make deb        # dist/mmt-studio-core_<ver>_amd64.deb + systemd unit
make docker     # Multi-stage Docker image
```

## Configuration

```bash
# Flags
sacore-web --addr :5000 --ngap-addr :38412

# Environment
SA_CORE_DB_TYPE=sqlite                          # sqlite | postgresql
SA_CORE_DB_FILE=sacore.db                       # SQLite path
LOG_LEVEL=INFO                                  # DEBUG | INFO | WARNING | ERROR
SACORE_LOG_FILE=/var/log/sacore/sacore.log      # Rotating file sink
SACORE_LOG_IMSI=imsi1,imsi2                     # IMSI allow-list filter

# Runtime tuning (via run.sh or environment)
SACORE_HUGEPAGE_COUNT=512                       # 2MB hugepages for DPDK
SACORE_RCVBUF_MB=32                             # GTP-U socket receive buffer
SACORE_SNDBUF_MB=32                             # GTP-U socket send buffer
SACORE_BACKLOG=65536                            # net.core.netdev_max_backlog
SACORE_CPU_PERF=1                               # Set CPU governor to performance
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Web GUI (:5000)                       │
│    69 HTML panels  ·  549+ REST routes  ·  Chi + pongo2      │
└──────────────────────────┬──────────────────────────────────┘
                           │
┌──────────────────────────┴──────────────────────────────────┐
│                   5G Core NFs (nf/, 16 packages)             │
│  AMF · SMF · AUSF · UDM · UDR · NSSF · PCF · CHF · SMSF · AF │
│  NSACF · LMF · NWDAF · GMLC                                  │
├──────────────────────────────────────────────────────────────┤
│  UPF: Go control plane ↔ cgo bridge ↔ C/DPDK dataplane       │
│       PFCP N4 server · PDR/FAR/QER/URR · GTP-U · Slicing     │
├──────────────────────────────────────────────────────────────┤
│  Access: EPC MME (S1AP + EMM + ESM + N26) · N3IWF · NTN      │
│  Services: IMS · MCX · eSIM · V2X · PROSE · SEAL · UAS · USSD│
│  Edge: EAS · ISAC · MEC · Ranging · TSN                      │
│  Safety: TCS · Emergency · IOPS · MBS · PWS · RACS · Disaster│
│  Security: Core · DPI · LI · NPN · RAN Sharing               │
│  IoT: Ambient · NB-IoT · NIDD · RedCap                       │
├──────────────────────────────────────────────────────────────┤
│  Codecs: NGAP+S1AP (APER) · NAS 5G+LTE · PFCP · SIP · ICE    │
│  Crypto: Milenage · KDF · AES-CMAC · NEA2/NIA2               │
│  AMF security: nf/amf/security/ — single owner for integrity │
│  + cipher + NAS COUNT + K_gNB derivation + SMC ctx install   │
├──────────────────────────────────────────────────────────────┤
│  Per-NF finite state machines (libs/fsm):                    │
│    AMF GMM FSM · AMF NGAP FSM (per UE) · SMF session FSM     │
│    SMF PFCP FSM (per UPF) · PCF SMPolicy FSM · UDM UECM FSM  │
│    AF FSM                                                    │
├──────────────────────────────────────────────────────────────┤
│  Infra: Timers · Health · Lifecycle · PLMN · TAC · Context   │
│  OAM: Logger · FM · PM · Platform · Trace · OTEL · CPU Pin   │
│  DB: SQLite · 37 schema/CRUD modules · Seed data             │
│  Verifier: nf/tools/speccheck — 1 245 §cites grounded in PDFs│
└──────────────────────────────────────────────────────────────┘
│                     DPDK 25.11 (in-repo)                     │
└──────────────────────────────────────────────────────────────┘
```

## Design patterns

The codebase relies on a handful of enforced patterns that keep spec
compliance and behavioural correctness auditable:

- **Per-NF finite state machines.** Every NF that owns a non-trivial
  lifecycle has its own `fsm/{state,event,fsm}.go` triplet on top of
  `libs/fsm`. Procedure collisions are caught at the transition table —
  no "what state am I in?" branching in handlers.

- **Single-owner NAS security (`nf/amf/security/`).** Integrity, ciphering,
  NAS COUNT, K_gNB derivation, SMC ctx install all live in one package.
  Handlers receive plaintext + metadata and never touch keys or counts.
  Invariants documented in `nf/amf/security/doc.go`.

- **Spec citations in code.** Every non-trivial handler carries verbatim
  clause quotes in its header or at the implementing line. Unimplemented
  spec requirements ship as TS-numbered TODO blocks at the exact call-site
  — the code is the audit trail.

- **Runtime citation verifier.** `nf/tools/speccheck` walks every
  `TS NN.NNN §X.Y` / `RFC NNNN §X` string in the codebase, resolves it
  against a local PDF under `specs/3gpp/` or `specs/ietf/`, and fails the
  build when a cite is dangling or mis-numbered. Strict by default;
  `SPECCHECK_LOOSE=1` escapes for work-in-progress branches.

- **PTI tracker (`nf/smf/session/pti/`).** Per-UE procedure transaction
  identity registry per TS 24.501 §7.3. Detects retx vs collision; on retx
  replays the cached Accept bytes; on collision surfaces a typed error so
  handlers can ship the correct 5GSM REJECT cause (e.g. #35 "PTI already
  in use").

- **Hook wiring for cycle-free NF ↔ transport layers.** Where a low-level
  transport (`dlnas`, `pdusetup`, `uectxrelease`) needs to call up into a
  higher-level policy (`gmm`, `security`), the low-level package exports
  a `WrapDL` / `OnContextEstablished` / `OnNASLowerLayerFailure` function
  variable and the upper package plugs in at `init()`. No package needs
  to import up-stack; runtime wiring stays visible in one place.

## Project structure

```
mmt-studio-core/
├── install.sh / build.sh / run.sh / Makefile     One-command setup + run
├── go.work                                       17-module Go workspace
├── specs/
│   ├── 3gpp/                                     53 3GPP TS PDFs (citation target)
│   └── ietf/                                     8 IETF RFCs (txt)
├── libs/
│   ├── dpdk-25.11/                               DPDK 25.11.0 source (in-repo)
│   ├── sacrypto/                                 Milenage + 5G KDF + AES-CMAC + NEA/NIA + ECIES
│   ├── sip/                                      SIP parser (shared by services/ims)
│   └── fsm/                                      Generic FSM kernel
├── codecs/
│   ├── asn1-go/                                  ASN.1 APER/UPER compiler + generated codecs
│   │   └── protocols/{ngap,s1ap}/                NGAP (TS 38.413) · S1AP (TS 36.413)
│   ├── tlv-3gpp-nas/nasgen/                      NAS 5G+LTE (TS 24.501/301)
│   └── tlv-3gpp-pfcp/pfcpgen/                    PFCP (TS 29.244)
├── oam/                                          logger · fm · pm · platform · trace · otel
├── db/                                           engine · schemas · crud · seed
├── infra/                                        timers · health · lifecycle · plmn · tac · lb
├── webservice/                                   Chi + pongo2 + 69 HTML + 549+ REST
├── nf/
│   ├── amf/                                      ctx · gmm · ngap · security · musim · n26
│   ├── smf/                                      ctx · session · pfcp · upf · upfclient · ipalloc
│   ├── upf/                                      Go ctrl + cgo + C/DPDK dataplane
│   ├── pcf/ · udm/ · af/ · ausf/ · udr/ · nssf/ · chf/ · smsf/ · nsacf/ · lmf/
│   ├── nwdaf/                                    Analytics + collectors + exposure
│   └── tools/speccheck/                          §citation runtime verifier
├── access/                                       epc/ · n3iwf/ · ntn/
├── services/                                     ims/ · mcx/ · esim/ · v2x/ · prose/ · seal/ · uas/ · ussd/ · pin/ · frmcs/
├── edge/ · safety/ · security/ · iot/            MEC, emergency, LI, NB-IoT etc.
└── tools/                                        Traffic generator, release scripts
```

## Spec coverage

| Surface | Spec | Status |
|---|---|---|
| NGAP (per-UE + non-UE) | TS 38.413 v19.2.0 | All §8 procedures decoded; FSM-driven per UE |
| S1AP (4G interworking) | TS 36.413 v19.1.0 | NG / S1 mode select via N26 (TS 23.502 §4.x) |
| NAS 5G | TS 24.501 v19.6.0 | Registration, Service Request, Dereg, Auth, SMC, ULNAS, DLNAS, 5GSM |
| NAS LTE | TS 24.301 v19.5.0 | EMM + ESM + bearer mgmt |
| PFCP | TS 29.244 v19.5.0 | §7.5.2 / .4 / .6 (full); §7.4.4.5 / §7.4.6 dispatch added |
| 5G crypto | TS 33.501 v19.6.0 | KDF Annex A; SMC §6.7.2; K_gNB §6.8.1.2.2 |
| Milenage | TS 35.205/206/207 | f1 / f2345 / f5* — Test Set 1 byte-exact |
| GTP-U | TS 29.281 v19.2.0 | Encap/decap in C dataplane |
| SIP | RFC 3261 + 3GPP IMS suite | Parser + REGISTER + INVITE flow |
| SCTP | RFC 4960 + RFC 6458 | Real kernel SCTP via syscalls; per-NGAP-association FSM |

## Performance

See [docs/PERFORMANCE.md](docs/PERFORMANCE.md) for measured benchmarks. Headline numbers
on a 4-core i7-1165G7 laptop (laptop-class — Xeon-class production hardware
will scale further):

| Workload | Result |
|---|---|
| Registration p50 (128 UEs) | 2 350 ms |
| PFCP §7.5.2 sustained establishment | 13–15 sessions/s |
| §7.5.6 cascade deletion (128 UEs) | ~60–250 ms (~500–2 100 sess/s) |
| Data plane (8 UE × 1 Mbps bidirectional) | 8.15 Mbps UL / 8.17 Mbps DL aggregate (~98% of target) |

## Building & deploying

`./install.sh` is the canonical entry point. It:

1. Installs build dependencies (`apt-get install` on Debian-derived; manual
   instructions printed on others).
2. Builds DPDK 25.11 from `libs/dpdk-25.11/` if not already built.
3. Compiles `libupf_dp.so` from `nf/upf/dataplane/src/`.
4. Builds the `sacore-web` binary.
5. Runs `go test ./...` per module to confirm the build is clean.
6. Installs `scripts/sysctl/99-sacore.conf` to `/etc/sysctl.d/` and reloads.

For a step-by-step or platform-specific install (RHEL / Debian / Docker /
air-gapped), see [docs/INSTALL.md](docs/INSTALL.md). For SCTP / DPDK kernel
tuning rationale and verification (`scripts/sysctl-check.sh`), see the
[Kernel tuning](#kernel-tuning) section below.

### systemd service (from `make deb`)

```ini
[Service]
ExecStartPre=/sbin/modprobe sctp
ExecStart=/opt/mmt-studio-core/sacore-web --addr :5000 --ngap-addr :38412
Restart=on-failure
LimitMEMLOCK=infinity
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE CAP_IPC_LOCK CAP_SYS_NICE
```

### Docker

```bash
make docker
docker run -p 5000:5000 -p 38412:38412 --privileged ghcr.io/makemytechnology/mmt-studio-core:latest
```

## Kernel tuning

Stock Ubuntu ships `net.core.rmem_max=212992` (208 KB) — every SCTP
association we open asks for **4 MB** SO_RCVBUF in
`nf/amf/ngap/transport_linux.go`, so the kernel silently clamps the
request. Under 16-UE registration bursts this shows up as
`recv: connection reset by peer` or truncated NGAP bundles; the root
cause isn't in the Go code, it's the kernel.

`install.sh` installs `scripts/sysctl/99-sacore.conf` into
`/etc/sysctl.d/` and runs `sysctl --system`, so the values persist
across reboots and survive `apt upgrade`.

| Key | Recommended | Why |
|---|---|---|
| `net.core.rmem_max` / `wmem_max` | 8388608 | 4 MB per-socket buffer request goes through un-clamped |
| `net.core.optmem_max` | 65536 | NGAP cmsg (PPID, SCTP_SNDRCV) needs headroom |
| `net.sctp.sctp_rmem` / `sctp_wmem` | `4096 1048576 8388608` | Auto-tune ceiling matches core max |
| `net.sctp.association_max_retrans` | 30 | Matches infra_config default |
| `net.core.somaxconn` | 65535 | NG-Reset storms reconnect every gNB at once |
| `net.core.netdev_max_backlog` | 65535 | UPF high-pps + bursty signalling share the NIC queue |
| `net.ipv4.ip_local_port_range` | `10240 65535` | PFCP + SBI + Prometheus all burn ephemeral ports |

Verify: `./scripts/sysctl-check.sh` (exit 0 if all OK, exit 1 on any LOW).
The startup banner also reads `/proc/sys/...` at boot and logs a WARN per
drift — so `journalctl -u sacore | grep sysctl` surfaces any key that
regressed silently.

## End-to-end flow

A full registration + PDU establishment + service request cycle runs through
real APER / NAS codecs, real Milenage crypto, and the full per-UE GMM +
per-UE NGAP + per-session 5GSM FSM stack:

```
gNB → NG Setup Request              (APER round-trip; NGAP FSM: IDLE→ACTIVE)
AMF → NG Setup Response              (AMFName, ServedGUAMIList, Capacity)
gNB → Initial UE Message             (piggybacking Registration Request)
AMF → AUSF → UDM → UDR              (subscriber K/OP lookup)
AUSF → Milenage f1/f2345             (real AES-based auth vector)
AMF → Authentication Request         (RAND + AUTN via DL NAS Transport)
UE  → Authentication Response        (RES* verified against XRES*)
AMF → ConvA6/A7 → K_SEAF/K_AMF       (real HMAC-SHA-256 KDF)
AMF → Security Mode Command          (security.TxSMC — SHT=3 per §6.7.2 step 1b)
UE  → Security Mode Complete         (security.RxNAS verifies + advances UL count)
AMF → security.DeriveKgNB            (just-in-time per §6.8.1.2.2 + §A.9)
AMF → Initial Context Setup Request  (K_gNB + UE sec caps → gNB)
AMF → Registration Accept            (via DL NAS Transport)
UE  → PDU Session Establishment Req  (via UL NAS Transport with PTI)
SMF → PCF SMPolicy                   (SMPolicy FSM: NONE → ACTIVE)
SMF → UPF (PFCP N4)                  (PFCP FSM: → ESTABLISHED; PDR/FAR/QER/URR installed)
gNB → PDU Session Resource Setup Resp (session FSM → ACTIVE; FAR BUFF→FORW)
UPF → GTP-U tunnel up                (data plane ready)
```

## Cited specs (local PDFs / RFCs)

```
specs/3gpp/   53 PDFs   TS 23.501, 23.502, 23.503, 24.301, 24.501, 24.587,
                        29.244, 29.501, 29.502, 29.503, 29.510, ...
                        33.102, 33.220, 33.501, 35.205/206/207,
                        36.413, 38.412, 38.413, ...
specs/ietf/   8 RFCs    1332, 1661, 1877, 3261, 4493, 4960, 6458, 7807
```

Every `TS NN.NNN §X.Y` and `RFC NNNN §X` string in the codebase resolves
to a local file. `go test ./nf/tools/speccheck/...` enforces this strictly.

## Contributing

This is currently a private repository for invited collaborators and
partners. If you have access and want to contribute, please follow these
conventions:

- Commit messages start with the affected component and cite specs where
  relevant — e.g. `amf/ngap/sctp: document why gNB cascade stays N × §7.5.6`.
- Every non-trivial handler carries a verbatim spec quote in the header.
- Unimplemented spec requirements ship as `TODO(spec: TS NN.NNN §X.Y ...)`
  blocks at the exact call-site.
- `go test ./...` per module must pass before merge.
- `go test ./nf/tools/speccheck/...` must pass strict (1 245 / 1 245 grounded).

System-level validation runs through the companion **mmt-studio-tester**
product, released alongside the core.

## License

GNU Affero General Public License v3.0 or later (AGPL-3.0-or-later),
matching the licensing model of upstream open-source 5G cores such as
Open5GS. See [LICENSE](LICENSE) for the full text and [NOTICE](NOTICE)
for third-party attributions (DPDK, Go modules, 3GPP / IETF specs).

### Commercial licensing

A commercial licence is also available for parties whose use case is
not compatible with AGPL-3.0 obligations — for example, embedding the
core in a closed-source appliance, or operating it as a managed service
without publishing modifications. Contact **info@makemytechnology.com**
for terms.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the contributor licence
grant that enables this dual-licensing model.

## Acknowledgements

- **3GPP** for publishing comprehensive technical specifications openly.
- **IETF** for SCTP (RFC 4960), SIP (RFC 3261), and the rest of the
  internet plumbing this core depends on.
- **DPDK** project for the user-plane fast path.
- **modernc.org/sqlite** for a pure-Go SQLite driver (no cgo dependency
  for the database layer).

---

*MMT Studio Core — built to be auditable, deployable, and standards-grounded.*

# SA Tester — 5G SA Core Network Tester

Emulates gNBs and UEs to test a 5G SA Core (AMF, SMF, UPF, IMS, LMF, NTN, IoT).

## Quick Start

```bash
./run.sh                         # Auto-elevates to root, starts on port 5000
# Web UI: http://<ip>:5000
```

Zero external dependencies — Python, iperf3, libsctp, and all packages are bundled.

## Documentation

Design docs live in [docs/](docs/). Start with [docs/overview.md](docs/overview.md)
for the 30-minute orientation, then drill into the subsystem you're touching.

| Doc | When to read |
|---|---|
| [docs/overview.md](docs/overview.md) | new to the project |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | refactor goals + phased roadmap (the source of truth) |
| [docs/control-plane.md](docs/control-plane.md) | NGAP / NAS / SCTP / actor model |
| [docs/data-plane.md](docs/data-plane.md) | GTP-U / TUN / Rust DP plan |
| [docs/testcases.md](docs/testcases.md) | how to add and run tests |
| [docs/web-api.md](docs/web-api.md) | FastAPI routes + UI |
| [docs/observability.md](docs/observability.md) | logging, stats, the speccheck citation gate |
| [docs/go_reference_gap.md](docs/go_reference_gap.md) | Go-reference coverage audit |
| [docs/speccheck_punchlist.md](docs/speccheck_punchlist.md) | outstanding 3GPP citation issues |
| [docs/training_notes/](docs/training_notes/) | per-procedure walkthroughs (NG-Setup, Registration, IMS, PDU Session, ...) |

The [docs/README.md](docs/README.md) is the index.

## Features

- **5G NAS/NGAP**: Registration, Authentication (5G-AKA), Security Mode, PDU Session
- **GTP-U Data Plane**: TUN interfaces, QoS classification, per-UE policy routing
- **IMS/VoNR/ViNR**: SIP REGISTER, INVITE, re-INVITE, REFER, BYE with IMS-AKA
- **Conference Calls**: 3-way to 6-way audio/video via MRFP (TS 24.147)
- **Mid-Call Upgrade/Downgrade**: VoNR ↔ ViNR via SIP re-INVITE
- **Traffic Engine**: iperf3 UL/DL, RTP audio (AMR-WB) and video (H.264)
- **Handover**: N2 handover with GTP-U tunnel switching
- **Network Slicing**: eMBB/URLLC/MIoT slice testing
- **Positioning**: E-CID, Multi-RTT, DL-TDOA, A-GNSS, Geofencing (TS 23.273)
- **IoT**: NB-IoT PSM/eDRX, CP CIoT, NIDD/SCEF, Ambient IoT tags (TS 22.369)
- **NTN**: Satellite constellation, coverage, timing advance, TAI, feeder links (TS 38.821)
- **202 test cases** across 19 Robot Framework suites

## Build & Deploy

```bash
# Build self-contained executable
./build/build.sh

# Deploy to target machine
scp build/dist/satester.tar.gz user@target:~/
ssh user@target 'tar xzf satester.tar.gz && sudo ./satester/install.sh'

# Docker
cd build && docker compose up -d
```

## CLI

```bash
python3 -m src.cli run --suite 08_ims --report html      # Run IMS suite
python3 -m src.cli run --report html junit --exit-code    # Full regression
python3 -m src.cli analysis pass-rate                     # Trends
python3 -m src.cli status --run latest                    # Status
```

## Project Structure

```
sa_tester/
├── run.sh              # Entry point (auto-sudo, bundled Python)
├── src/
│   ├── app.py          # FastAPI web UI (port 5000)
│   ├── cli.py          # CLI entry point
│   ├── protocol/       # NGAP, NAS, GTP-U, SIP, RTP, crypto, NTN, IoT, ...
│   ├── statemachine/   # Legacy threaded gNB + UE FSM (drives all testcases today)
│   ├── control/        # New asyncio control plane (Phase 2+) — see in-package README
│   ├── dataplane/      # Python client for the Rust DP (Phase 3 — placeholder)
│   ├── traffic/        # iperf3 / RTP traffic engine + remote agents
│   ├── testcases/      # 47 testcase modules (8 domains)
│   ├── core/           # SA Core REST client (provisioner, admin)
│   ├── db/             # SQLite (schemas, CRUD, runs, reports, analysis)
│   ├── routes/         # FastAPI blueprints (migration in progress)
│   ├── cluster/        # Distributed testing (controller + worker)
│   ├── observability/  # Stats counters
│   └── ai_engine/      # AI-powered analysis (Ollama-backed)
├── robot/              # Robot Framework
│   ├── suites/         # 31 .robot suites grouped by domain
│   │   ├── access/             # registration, NG setup, auth, release, idle
│   │   ├── session/            # PDU session, multi-DNN, slicing
│   │   ├── mobility/           # handover, roaming
│   │   ├── traffic/            # QoS, multi-UE, jumbo, DPI
│   │   ├── voice_media/        # IMS, IMS scale, MCX, emergency
│   │   ├── policy_charging/    # charging, MEC, NWDAF
│   │   ├── regulatory/         # lawful intercept, trace
│   │   ├── diagnostics/        # stress
│   │   └── other/              # positioning, IoT, NTN, V2X, eSIM, sidelink
│   └── resources/      # Shared keywords + variables
├── tests/              # 39 pytest files (codec round-trips, security, speccheck)
│   ├── speccheck/      # 3GPP citation gate
│   ├── bench/          # attach_bench.py — async-CP benchmark
│   └── probes/         # one-off probes
├── dp-rust/            # Rust data-plane workspace (Phase 3 — placeholder)
├── libs/               # Bundled dependencies (zero external deps)
│   ├── python3.12/     # Bundled Python runtime
│   ├── bin/            # iperf3 + libiperf
│   ├── pysctp.libs/    # libsctp
│   ├── pycrate/        # NGAP/NAS codec
│   └── ...             # FastAPI, cryptography, Robot Framework, etc.
├── build/
│   ├── build.sh        # PyInstaller build
│   ├── satester.spec   # PyInstaller spec
│   └── deploy/         # install.sh, systemd service, config
├── config/             # UE + gNB config, cluster, sequences
├── specs/              # 3GPP / IETF specs (PDFs + RFCs) — citation source of truth
├── docs/               # Design documentation (see "Documentation" above)
└── data/               # SQLite DB, logs, test results
```

## Test Suites

| # | Suite | Tests | Coverage |
|---|-------|-------|----------|
| 01 | Registration | 6 | TS 24.501 §5.5.1, TS 33.501 §6.1.3 |
| 02 | PDU Session | 4 | TS 24.501 §6.4, TS 29.244 |
| 04 | Stress | 16 | Multi-UE registration, rapid attach/detach |
| 05 | NG Setup | 16 | TS 38.413 §8.7 |
| 06 | Authentication | 12 | 5G-AKA, SUCI, ECIES |
| 07 | Traffic | 13 | UDP/TCP UL/DL, AMBR, MBR, GBR |
| 08 | IMS | 17 | VoNR, ViNR, conference, mid-call upgrade |
| 09 | Multi-Traffic | 12 | Concurrent UE traffic |
| 10 | IMS Scale | 16 | Multi-UE IMS calls |
| 11 | Multi-DNN | 6 | internet + IMS dual PDU |
| 12 | Handover | 6 | N2 handover, Xn |
| 13 | Jumbo Frames | 6 | MTU 9000 |
| 14 | Release | 12 | UE context release, re-attach |
| 15 | Idle Mode | 8 | CM-IDLE, paging, Service Request |
| 16 | Slicing | 10 | eMBB/URLLC/MIoT slices |
| 17 | Positioning | 10 | E-CID, RTT, TDOA, GNSS, geofence |
| 18 | IoT | 15 | NB-IoT, NIDD/SCEF, Ambient IoT |
| 19 | NTN | 12 | Satellite, coverage, timing, TAI |

## Architecture

```
┌──────────────┐     SCTP/NGAP      ┌──────────────┐
│  SA Tester   │◄──────────────────►│   SA Core    │
│  (gNB + UE)  │     GTP-U/UDP      │  (AMF/SMF/   │
│              │◄──────────────────►│   UPF/IMS)   │
│  Port 5000   │   SIP/RTP/iperf3   │  Port 5000   │
└──────────────┘◄──────────────────►└──────────────┘
  192.168.1.103                       192.168.1.107
```

Target architecture (Phase 4): controller + N gNB workers + Rust data plane.
See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) §3 for the full diagram.

## Configuration

All configuration is from the Web GUI — no hardcoded defaults:
- **gNB Config**: PLMN, TAC, gNB ID, slices
- **UE Config**: IMSI, MSISDN, K/OPc, identity type (SUPI/SUCI)
- **Infrastructure**: AMF IP, Core URL, traffic engine

## Standards Compliance

Strictly follows 3GPP/IMS/RFC standards — no fallbacks or workarounds.
**Tests must FAIL on non-compliant responses.**

Every spec citation in `src/` is verified at test-time by `tests/speccheck`
against PDFs under `specs/common/`. See [docs/observability.md §3](docs/observability.md)
for the policy and [docs/speccheck_punchlist.md](docs/speccheck_punchlist.md)
for the current outstanding items.

Key specifications:
- TS 24.501 (5G NAS), TS 38.413 (NGAP), TS 33.501 (5G Security)
- TS 29.281 (GTP-U), TS 38.415 (PDU Session Container)
- TS 24.229 (SIP/IMS), TS 24.147 (Conference), TS 26.114 (Media)
- RFC 3261 (SIP), RFC 3515 (REFER), RFC 3550 (RTP)
- TS 23.273 (Positioning), TS 38.305 (NR Positioning Methods)
- TS 23.401 (IoT), TS 22.369 (Ambient IoT), TS 38.821 (NTN)

## License

Copyright (c) 2026 MakeMyTechnology.

GNU Affero General Public License v3.0 or later (AGPL-3.0-or-later),
matching the licensing model of upstream open-source 5G cores such as
Open5GS. See [LICENSE](LICENSE) for the full text and [NOTICE](NOTICE)
for third-party attributions.

### Commercial licensing

A commercial licence is also available for parties whose use case is
not compatible with AGPL-3.0 obligations — for example, embedding the
tester in a closed-source product, or operating it as a managed
service without publishing modifications. Contact
**info@makemytechnology.com** for terms.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the contributor licence
grant that enables this dual-licensing model.

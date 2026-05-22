<!-- Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later. -->

# MMT Studio — 5G Core + Tester + Orchestration

Open-source 3GPP-aligned 5G System Architecture (SA) reference
distribution from **MakeMyTechnology**. Ships three components,
laid out as siblings under the repo root:

| Directory       | What it is                                          |
|-----------------|------------------------------------------------------|
| [`core/`](core)               | 5G Core in Go — AMF, SMF, UPF, AUSF, UDM, UDR, PCF, CHF, NRF, NWDAF, IMS CSCF, eSIM SM-DP+, … |
| [`tester/`](tester)           | Python + Robot tester — gNB simulator, UE pool, IMS UAC, Robot suites |
| [`orchestrate/`](orchestrate) | Docker-Compose glue — `run_studio.sh` brings the full stack up |

Each component has its own `README.md` with the gritty detail. This
top-level README is the entry point.

## Install

After bring-up (any platform), the studio is reachable on the host at:

| Service     | Web UI                  | Bridge IP    |
|-------------|-------------------------|--------------|
| `sacore`    | http://localhost:5000   | 172.30.0.10  |
| `satester`  | http://localhost:5001   | 172.30.0.20  |

### Linux (bare metal, VirtualBox guest, or WSL2 Ubuntu)

Tested on Ubuntu 22.04+ / Debian 12+ with Docker Engine 24+.

```bash
git clone https://github.com/Makemytechnology/mmt-studio-5g6g.git
mmt-studio-5g6g/orchestrate/install.sh --role=both
```

The installer self-elevates and asks for your sudo password when it
needs root.

Pick `--role={both|core|tester}`; with `tester`, add
`--core-host=10.0.0.42` to point at a remote core. VirtualBox guests
need ≥ 4 GiB RAM and 4 vCPUs.

### Windows 11 (or Windows 10 build 19041+, WSL2)

From any cmd or PowerShell prompt (no need to "Run as Administrator" —
the `.bat` self-elevates with a UAC prompt):

```cmd
git clone https://github.com/Makemytechnology/mmt-studio-5g6g.git
mmt-studio-5g6g\orchestrate\install_on_windows.bat
```

If Windows asks for a reboot after the WSL2 step, reboot and re-run.

### Run / stop / status / logs

After install, control the stack from the cloned `orchestrate/`
directory. Same actions on both platforms.

#### Linux

```bash
cd mmt-studio-5g6g/orchestrate
./run_studio.sh              # bring up (default)
./run_studio.sh status       # ps + watcher status
./run_studio.sh logs         # follow logs (Ctrl-C detaches)
./run_studio.sh restart      # down + up
./run_studio.sh down         # stop + remove containers + bridge
```

#### Windows

```cmd
cd mmt-studio-5g6g\orchestrate
run_studio.bat               # bring up + per-test Wireshark watcher
run_studio.bat status        # compose ps + watcher status
run_studio.bat logs          # follow logs (Ctrl-C detaches)
run_studio.bat restart       # down + up
run_studio.bat down          # stop stack + Wireshark watcher
```

Both platforms open Wireshark live on every test run by default --
the same window stays open until the next test triggers a fresh
capture. Pass `--no-wireshark` (Linux) or `-NoWireshark` (Windows)
to opt out.

## Quick start (dev, from source)

For development inside this monorepo: builds the images directly
from your working tree, no release tarball involved.

### Linux

`run_studio.sh` is both the build-and-run command and the runtime
controller — same flags as `install.sh` for role selection.

```bash
cd orchestrate
./run_studio.sh                  # full stack (both core + tester)
./run_studio.sh --role=core      # core only  (sacore + satraffic)
./run_studio.sh --role=tester    # tester only (point at a remote core)
./run_studio.sh logs             # follow logs
./run_studio.sh down             # stop + remove containers + bridge
./run_studio.sh reset            # forceful cleanup
```

### Windows

`install_on_windows.bat` doubles as the dev rebuild — it rsyncs
your Windows source tree into WSL, rebuilds the sacore + satester
images, and brings the stack up. After the first run, use
`run_studio.bat` for the up / down / restart cycle.

```cmd
cd orchestrate
install_on_windows.bat                       :: rebuild + restart from source
install_on_windows.bat -Role tester ^
    -CoreHost 10.0.0.42                       :: tester-only, remote core
run_studio.bat logs                           :: follow logs
run_studio.bat down                           :: stop stack + Wireshark watcher
```

For tester-only, point the gNB profile at the remote core's IP via
the tester web UI (`AMF IP` / `UPF IP`), or set `AMF_IP` / `UPF_IP`
in `orchestrate/.env` before bring-up.

### Hugepages prerequisite

The UPF dataplane uses DPDK and requires at least 512 × 2 MiB
hugepages (`vm.nr_hugepages ≥ 512`). Both installers allocate +
persist them automatically on bare metal, VirtualBox, and WSL2.

### Building each component separately

| Component     | Build                                           |
|---------------|--------------------------------------------------|
| `core/`       | `cd core && go build ./...` (Go 1.22+). DPDK under `core/libs/dpdk-25.11/` uses its own meson/ninja convention. |
| `tester/`     | `cd tester && python3 -m venv .venv && .venv/bin/pip install -r build/requirements.txt` |
| `orchestrate/`| `cd orchestrate && ./run_studio.sh up` builds container images on first run |

The Docker path is the easy button — `docker compose build` (or
`run_studio.sh up`) compiles core + tester images without you
needing Go or Python locally.

## Architecture

```
                 ┌──────────────────────────────────────────┐
                 │  orchestrate/  (docker-compose)          │
                 │                                          │
                 │   ┌─────────────┐    ┌──────────────┐   │
                 │   │   sacore    │◄──►│  satraffic   │   │
                 │   │ (5G Core)   │    │ (traffic     │   │
                 │   │             │    │  agent       │   │
                 │   │ from core/  │    │  slave —     │   │
                 │   │             │    │  shares      │   │
                 │   │             │    │  sacore      │   │
                 │   │             │    │  netns)      │   │
                 │   └──────▲──────┘    └──────────────┘   │
                 │          │ NGAP/SCTP + N3/GTP-U          │
                 │          │                              │
                 │   ┌──────▼──────┐                       │
                 │   │  satester   │                       │
                 │   │ (gNB sim,   │                       │
                 │   │  UE pool,   │                       │
                 │   │  Robot      │                       │
                 │   │  suites —   │                       │
                 │   │  from       │                       │
                 │   │  tester/)   │                       │
                 │   └─────────────┘                       │
                 └──────────────────────────────────────────┘
```

## Spec compliance

Every meaningful procedure carries a `§`-cite to a 3GPP TS or IETF
RFC, anchored to the local PDFs under `core/specs/3gpp/`, `core/specs/ietf/`,
and `tester/specs/`. A `speccheck` tool verifies every cited section
actually exists in the referenced document.

Selected anchors covered:

- **N1/N2/N3/N4** — TS 23.501, TS 23.502, TS 24.501, TS 29.281, TS 29.244
- **NGAP** — TS 38.413
- **Policy / Charging** — TS 23.503, TS 29.512, TS 32.290, TS 32.291
- **IMS / SIP** — TS 23.228, TS 24.229, TS 26.114
- **NWDAF** — TS 23.288, TS 29.520, TS 29.522
- **eSIM** — GSMA SGP.22, ITU-T E.118, TS 31.102
- **Multi-USIM** — TS 23.501 §5.34, TS 23.502 §4.2.6, TS 24.501 §9.11.3.91

## Status

This is **reference / research-grade** software. The Go core is
exercised against the in-tree tester for ~545 test cases (313 Robot
+ 232 Python OAM TCs). Suitable for:

- Lab interop, conformance trials, and feature R&D.
- Spec-aligned tutorials and university coursework.
- Operators experimenting with new features before vendor adoption.

Not currently certified for commercial production use — see the
component READMEs for what's wired vs. stubbed.

## Licence

[GNU Affero General Public License v3.0 or later](LICENSE)
(AGPL-3.0-or-later).

If your use case is not compatible with AGPL-3.0 obligations — for
example, embedding the core in a closed-source appliance or operating
it as a managed service without publishing modifications — a separate
commercial licence is available. Contact **info@makemytechnology.com**
for terms.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). All commits must be DCO-signed
(`git commit -s`).

## Code of conduct

See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). Reports go to
**info@makemytechnology.com**.

## Reporting security issues

Please do **not** open public issues for security vulnerabilities.
Email **info@makemytechnology.com** with the details and we will
respond privately.

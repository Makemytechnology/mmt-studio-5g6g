# mmt-studio-orchestrate

Single source of truth for running **mmt-studio-core** (Go) and
**mmt_studio_core_tester** (Python) together as Docker containers on
one host.

Three services on one bridge:

| Service     | Role                                       | Image                  |
|-------------|--------------------------------------------|------------------------|
| `sacore`    | Go core (AMF/SMF/UPF), owns `upfgtp` TUN   | `mmt-studio-core:dev`  |
| `satraffic` | Traffic-agent **slave** in sacore's netns  | `mmt-studio-tester:dev`|
| `satester`  | Python orchestrator (**master**) + gNB sim | `mmt-studio-tester:dev`|

`satraffic` reuses the tester image by design — the traffic-management
code in `mmt_studio_core_tester/src/traffic/` is the single source of
truth for both ends. The slave runs `python3 -m src.traffic.agent_main`,
the master runs the orchestrator. Same files, different entrypoint.
`satraffic` shares `sacore`'s netns (`network_mode: service:sacore`)
so DL iperf3 it generates traverses UPF's `upfgtp` TUN.

Sibling layout expected:

```
work/
├── mmt-studio-core-go/        ← core repo
├── mmt_studio_core_tester/    ← tester repo (also home of the traffic agent)
└── mmt-studio-orchestrate/    ← this repo
    ├── docker-compose.yml
    └── run_studio.sh
```

## Quick start

```bash
cd mmt-studio-orchestrate
./run_studio.sh up        # build + run both, detached
./run_studio.sh ps        # status
./run_studio.sh logs      # tail both
./stop_studio.sh          # stop + remove (alias for `run_studio.sh down`)
```

## All entry points

| Want to | Run | What it does |
|---|---|---|
| Start full stack | `./run_studio.sh up` (here) | sacore + satraffic + satester + bridge |
| Start core side  | `mmt-studio-core-go/run.sh --docker up` | sacore + satraffic |
| Start tester     | `mmt_studio_core_tester/run.sh --docker up` | satester (pulls sacore + satraffic via depends_on) |
| Stop full stack  | `./stop_studio.sh` (here) | all containers + bridge |
| Stop core side   | `mmt-studio-core-go/run.sh --docker down` | rm sacore + satraffic |
| Stop tester only | `mmt_studio_core_tester/run.sh --docker down` | rm satester container |
| Stop everything (including stale containers from other projects) | `docker stop $(docker ps -q)` | nuclear |

All entry points read the same `docker-compose.yml` — single source of truth.
The "core side" always includes the traffic-agent slave (`satraffic`); a
core without its slave can't service DL iperf3 from a tester master.

After bring-up:

| Service     | Web UI / port           | Bridge IP            | Host veth        |
|-------------|-------------------------|----------------------|------------------|
| core        | http://localhost:5000   | 172.30.0.10          | `sacore-veth`    |
| satraffic   | :9100 (in sacore netns) | 172.30.0.10 (shared) | (shares sacore)  |
| tester      | http://localhost:5001   | 172.30.0.20          | `satester-veth`  |

In the tester web UI, set the gNB profile to:

| | |
|--|--|
| AMF IP | `172.30.0.10` |
| AMF SCTP port | `38412` |
| UPF IP | `172.30.0.10` |
| GTP-U port | `2152` |

## Traffic management (UL/DL): tester is master

The Python code under `mmt_studio_core_tester/src/traffic/` runs in
both roles:

- **Master** — `satester` runs the orchestrator + test cases. It
  decides what UL/DL/bidir iperf3 sessions to run and with which QoS.
- **Slave**  — `satraffic` runs `agent_main.py` (FastAPI on :9100).
  No DB, no web UI — pure executor of `engine.py` primitives. Lives
  in `sacore`'s netns so iperf3 packets traverse the UPF.

Master finds the slave through one DB row in `traffic_agents`:

```
base_url = http://172.30.0.10:9100
token    = ${SA_AGENT_TOKEN:-dev-token}
```

Same code → three deployment shapes (single host Docker / split host /
all native) without forking.

UL/DL packet paths:

- **UL** — tester iperf3 client (UE TUN IP) → gNB-sim GTP-U → mmtnet
  → sacore UPF → `upfgtp` → satraffic iperf3 server (sacore netns).
- **DL** — master `POST /api/traffic/start` to satraffic → satraffic
  iperf3 client → kernel routes UE pool via `upfgtp` → UPF GTP-U →
  mmtnet → satester gNB-sim → UE TUN → tester iperf3 server.

## Why this exists

NGAP (SCTP/38412) and GTP-U (UDP/2152) are awkward to NAT through
Docker's userland proxy, and host networking forces both containers
to share the same netns — which makes `0.0.0.0:2152` collide between
core's UPF and tester's gNB simulator.

A custom Docker bridge (`mmtnet`, 172.30.0.0/24) with static IPs per
container gives each its own netns, so both can `bind(0.0.0.0:2152)`
independently. Container-to-container SCTP works through the bridge
unchanged because no docker-proxy NAT is involved.

See the matrix in `docker-compose.yml` for the deployment-mode
trade-offs (host net standalone vs. bridge net paired vs. two
machines).

## Per-repo `--docker` flags

The two sibling repos each expose `./run.sh --docker [up|down|logs]`
which delegates to **this** compose file (single source of truth):

```bash
cd ../core && ./run.sh --docker up      # core only
cd ../tester && ./run.sh --docker up  # tester only
```

`./run_studio.sh up` brings both up at once.

## Wireshark-in-netns (`--debug`)

SMF↔UPF runs over PFCP on `127.0.0.1:8805` inside sacore's netns, so
host-side Wireshark on `mmtnet0` only sees NGAP/GTP-U. To see PFCP
*and* NGAP/GTP-U in one capture, run host Wireshark inside sacore's
netns via `nsenter`:

```bash
./run_studio.sh --debug         # bring stack up + launch Wireshark
./run_studio.sh wireshark       # or: stack already up, just launch it
```

Wireshark opens on the host display (no extra container, no image
pull, no log noise). Pick interface **`any`** to see `lo` (PFCP),
`eth0` (NGAP/GTP-U/web), and `upfgtp` (UPF inner TUN) together.
Needs `sudo` (one prompt) for `nsenter` into the container netns.

## Host prerequisites

| What | How | Why |
|------|-----|-----|
| Hugepages (≥ 256 × 2 MB) | `echo 512 \| sudo tee /sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages` | DPDK in core's UPF |
| SCTP module | `sudo modprobe sctp` | NGAP transport |
| Docker Engine ≥ 24 + Compose v2 | `docker compose version` | uses bridge driver opts |

## License

Apache-2.0. See `LICENSE`.

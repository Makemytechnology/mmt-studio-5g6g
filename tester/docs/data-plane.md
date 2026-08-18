# Data Plane

Covers the GTP-U / TUN path: the legacy Python data plane (live today),
the planned Rust data plane (`dp-rust/`, Phase 3), and the Python
client (`src/dataplane/`) that bridges them. Traffic generation
(iperf3, RTP) sits on top of the data plane and is also covered here.

## 1. Today vs. Phase 3

| | Today: Python DP | Phase 3: Rust DP |
|---|---|---|
| Owner | `src/protocol/gtpu.py` (`GtpuManager`) | `dp-rust/` Rust binary |
| TUN devices | one per UE (`tester-ue-<N>`) | **one per host** (`tester-dp0`) |
| Routing | kernel route table per UE | user-space hash table (ue_ip → flow) |
| GTP-U socket | one UDP/2152 per UE (current) | one UDP/2152 per host, sharded by `hash(teid)` |
| Limit | ~16 UEs before kernel routes thrash | 10k UEs / 10 Gbps (acceptance target) |
| Status | live, used by every testcase | scaffolded — `dp-rust/Cargo.toml.placeholder`, `src/dataplane/` package empty |

The Rust DP is **not optional** for the 10k UE / 10 Gbps target; the
kernel route table approach hits its ceiling well before then. See the
phase-1 verification row in [ARCHITECTURE.md §5](ARCHITECTURE.md):
"16-UE test: 16/16 tunnels in < 1 s, no SCTP aborts" — that was the
floor we cleared by switching `_configure_tun` from `subprocess` to
pyroute2.

## 2. Python data plane (`src/protocol/gtpu.py`)

`GtpuManager` is the in-process owner of all GTP-U state today.

```
   UeStateMachine.on_pdu_session_accept(...)
       │
       ▼
   GtpuManager.create_tunnel(ue_imsi, ue_ip, local_teid, remote_teid, upf_peer_ip)
       │
       ├── pyroute2: ip tuntap add dev tester-ue-<N> mode tun
       ├── pyroute2: ip addr add <ue_ip>/32 dev tester-ue-<N>
       ├── pyroute2: ip route add via <upf_peer_ip> table <N>
       ├── pyroute2: ip rule add from <ue_ip> lookup <N>
       └── socket(UDP, 2152) bind to <gnb_ip>; spawn rx thread
```

All netlink work goes through pyroute2 — no `subprocess.run(['ip',
...])` on the hot path. (Phase 1 of [ARCHITECTURE.md §5](ARCHITECTURE.md)
landed this conversion.) If you spot a remaining `subprocess` in
`gtpu.py` or anywhere downstream of testcase setup, fix it; it's a
silent ~100 ms tax per call.

### Hot-path quirks worth remembering

- **GTP-U PDU Session Container** (TS 38.415 §5.5.2.1) carries the QFI
  for QoS classification. Encoding is in `gtpu_codec.py` so unit tests
  can exercise it without touching real sockets.
- **Per-UE policy routing** is what lets multiple UE TUNs share the
  uplink without collisions. The route table id is derived from the
  TEID — collision-resistant inside one tester, not across testers.
- **End markers** (TS 29.281 §5.1) are emitted on tunnel teardown so
  the UPF can flush its forwarding state cleanly — relevant for
  handover. Verified by `tc_handover.py`.

## 3. Rust data plane (`dp-rust/`, Phase 3)

Source of truth: [`dp-rust/README.md`](../dp-rust/README.md). The
crate is not built yet — only the design and the wire protocol are
fixed.

### 3.1 Process layout (planned)

```
   one tokio runtime, N worker tasks (N = cores - 1)
   ┌────────────────────────────────────────────────┐
   │  unix-ctrl-server  ── /run/sa-tester/dp.sock   │
   │  udp-2152          ── shared via SO_REUSEPORT  │
   │  one TUN           ── tester-dp0               │
   │  flow tables       ── ue_ip ↔ teid (sharded)   │
   │  per-UE stats      ── lock-free slab           │
   └────────────────────────────────────────────────┘
```

Per-core scaling: shard flows by `hash(teid) % N` across N tasks; each
task owns its partition outright. No locks on the hot path. Stats are
`AtomicU64::fetch_add(Relaxed)`.

### 3.2 CP ↔ DP wire protocol

Length-prefixed (u32 BE) CBOR over a Unix stream socket. Request /
reply matched by `seq`. Steady-state traffic never crosses this socket
— it's a control-plane operations channel only.

```cbor
# SetupTunnel
{ "op": "setup", "seq": N,
  "ue_imsi": "001010000000001", "ue_ip": "10.45.0.2",
  "local_teid": 0x00010001, "remote_teid": 0x00010002,
  "upf_peer_ip": "192.168.1.107", "qfi": 1 }
                                                 → { "ok": true }

# TeardownTunnel
{ "op": "teardown", "seq": N, "local_teid": 0x00010001 }
                                                 → { "ok": true }

# GetStats
{ "op": "stats", "seq": N, "local_teid": 0x00010001 }
                                                 → { "ok": true,
                                                     "rx_bytes": 123, "tx_bytes": 456,
                                                     "rx_pkts": 9, "tx_pkts": 8,
                                                     "rx_drops": 0, "tx_drops": 0 }

# Health
{ "op": "health", "seq": N }
                                                 → { "ok": true, "up_since": 1700000000, "flow_count": 16 }
```

Target latency: p99 < 1 ms for setup / teardown / stats. `GetStats`
is **only** for testcase end-of-test assertions — it must not be on
the per-packet path.

### 3.3 Acceptance for Phase 3

- Sustained 1 Gbps **per-UE** iperf3 through the DP.
- 100 concurrent UDP flows.
- Per-phase latency histograms show flat p50 / p99 distribution
  (no GC-style spikes).
- Graceful CP disconnect: the DP drops all flows but stays up so the
  next CP connect can attach without a process restart.

## 4. Python client (`src/dataplane/`, Phase 3)

Source of truth: [`src/dataplane/README.md`](../src/dataplane/README.md).

Planned surface:

```python
from src.dataplane.client import default_client

dp = await default_client()                          # AsyncUnixClient, lazy connect
await dp.setup_tunnel(SetupTunnelReq(
    ue_imsi="001010000000001",
    ue_ip="10.45.0.2",
    local_teid=0x00010001,
    remote_teid=0x00010002,
    upf_peer_ip="192.168.1.107",
    qfi=1,
))
await dp.teardown_tunnel(local_teid=0x00010001)
stats = await dp.get_stats(local_teid=0x00010001)
```

Migration path: control-plane code calls `dp.setup_tunnel(...)` instead
of `GtpuManager.create_tunnel(...)`. The legacy path stays in place
behind a `data_plane = "python"|"rust"` config knob until all 47
testcase modules pass against the Rust DP.

## 5. Traffic engine (`src/traffic/`)

Sits on top of the data plane (Python or Rust). Distinct from the GTP-U
plumbing — its job is to **drive packets through the tunnels** for
throughput / latency / QoS verification.

| File | Role |
|---|---|
| `engine.py` | core orchestrator — owns iperf3 servers, RTP streams |
| `interface.py` | abstract base — `start()`, `stop()`, `stats()` |
| `agent_main.py` / `agent_ui.py` | remote traffic agent runs on the **other side** of the core (trusted helper) for ground-truth UL/DL measurements |
| `groups.py` | grouped flows (e.g. eMBB + URLLC concurrent) |
| `remote.py` | controller-to-agent RPC |
| `generators/` | per-protocol flow producers (iperf3, RTP audio AMR-WB, RTP video H.264) |
| `receivers/` | per-protocol consumers / measurement points |
| `gtpu/` | GTP-U-aware tap for in-tester throughput measurement |
| `stats/` | rolling counters, per-flow rates, jitter / loss |

The traffic agent is launched by `run_traffic_engine.sh` on a separate
host (typically the application server behind the UPF). It exposes a
small REST API the tester driver calls. See `src/routes/traffic_api.py`
and `src/routes/traffic_agent_api.py`.

## 6. Open design points

- **One UDP/2152 socket per UE today vs. one shared with `SO_REUSEPORT`**
  in Phase 3. The shared socket is required for kernel-side flow
  steering; we'll cross that bridge during the Rust implementation.
- **DPDK / AF_XDP** is **explicitly out of scope** until 40 Gbps. tokio
  + plain UDP saturates 10 Gbps on a 2020+ x86 host. (See
  [ARCHITECTURE.md §7](ARCHITECTURE.md) "non-goals".)
- **TUN MTU** defaults to 1400 to leave headroom for GTP-U + UDP + IP
  + Ethernet (8 + 8 + 20 + 14 = 50 bytes). Jumbo-frame testing
  (TS 38.413 informative on MTU) lives in
  `robot/suites/traffic/jumbo*.robot` — that suite explicitly raises
  MTU and verifies the path doesn't fragment.

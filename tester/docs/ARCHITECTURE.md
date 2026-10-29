# SA Tester — Architecture

_Status: design approved. Phase 1 in progress._

## 1. Goals

| Metric | Target |
|---|---|
| UE concurrency per tester host | **10 000** |
| Aggregate data-plane throughput | **10 Gbps** |
| UE attach → active PDU session | **p99 < 500 ms** |
| Control-plane test exit time | **< 5 s after last traffic flow** |
| Restart / crash blast radius | **one gNB only** |
| Mid-test SCTP aborts | **never**, unless we explicitly tear down |

These are the acceptance criteria for the refactor. Missing any one of them invalidates the phase that claimed it.

## 2. Principles

1. **Control plane and data plane are separate processes.** Python owns NGAP/NAS state machines. A Rust binary owns the UDP socket, TUN fds, and GTP-U packets. They communicate over a Unix stream socket; steady-state traffic never crosses that socket.
2. **Async, not threaded, in the control plane.** One Python process = one event loop. Per-UE logic is an async coroutine, not a thread. GIL becomes irrelevant because we are I/O bound.
3. **No subprocess forks on hot paths.** Every `ip / sysctl` replaced with direct netlink (pyroute2 in CP, `netlink-rs` in DP). Per-UE provisioning drops from ~100 ms to sub-ms.
4. **User-space routing, not kernel.** Single TUN per DP process with an internal IP→tunnel hash table. Kernel route tables stay small regardless of UE count.
5. **Sharded gNB workers.** One worker process per simulated gNB (or small group). Controller orchestrates. Zero shared state between workers.
6. **Backpressure is explicit.** Every queue has a bounded depth and a published latency budget. Flow-control decisions are deliberate, not accidental.
7. **Everything measured.** Histograms per phase; metrics exposed at `/metrics`. No more "why did it hang" guesswork.

## 3. Architecture at a glance

```
                                          sa_tester host
  ┌──────────────────────────────────────────────────────────────────────┐
  │                                                                        │
  │   controller (Python asyncio)                                          │
  │   ├── REST / UI                                                        │
  │   ├── test runner                                                      │
  │   └── spawns N gNB worker processes                                    │
  │                                                                        │
  │   ┌──── gnb-worker (Python asyncio process, 1 per gNB) ───────┐       │
  │   │  sctp   : async wrapper over pysctp                        │       │
  │   │  ngap   : pycrate decode → per-UE actor dispatch           │       │
  │   │  actors : per-UE coroutine + mailbox                       │       │
  │   │  netlink: pyroute2 executor pool (address/rule plumbing)   │       │
  │   │                │                                             │       │
  │   │                │ unix stream socket (length-prefixed CBOR)   │       │
  │   │                ▼                                             │       │
  │   │  dp-client: {SetupTunnel, TeardownTunnel, GetStats}         │       │
  │   └──────────────────────────────────────────────────────────────┘       │
  │                         │                                                │
  │                         ▼                                                │
  │   ┌──── sa_dataplane (Rust/tokio, 1 per host) ──────────────────┐       │
  │   │  unix ctrl socket                                             │       │
  │   │  UDP/2152 GTP-U socket (io_uring-friendly)                   │       │
  │   │  ONE TUN device (tester-dp0)                                  │       │
  │   │  flow table:   ue_ip      → { local_teid, remote_teid, peer }│       │
  │   │  flow table:   local_teid → ue_ip                             │       │
  │   │  per-UE stats in a lock-free slab                             │       │
  │   │  N tokio tasks (N = cores − 1), flows sharded by TEID hash    │       │
  │   └───────────────────────────────────────────────────────────────┘       │
  └──────────────────────────────────────────────────────────────────────┘
```

## 4. Component contracts

### Controller ↔ Workers

Already partially exists in `src/cluster/`. One process per gNB; the controller never appears on the hot path. Lifecycle is sufficient:
- `start_worker(gnb_config)`
- `stop_worker(worker_id)`
- `collect_result(worker_id)`

### Worker ↔ Data plane

New. Unix stream socket, length-prefixed CBOR (or protobuf). All messages carry a sequence number + reply channel. Target latency: p99 < 1 ms.

Minimal messages for Phase 3:

```
SetupTunnel     { ue_imsi, ue_ip, local_teid, remote_teid, upf_peer_ip, qfi }
                  → { ok | error }
TeardownTunnel  { local_teid }
                  → { ok | error }
GetStats        { local_teid }
                  → { rx_bytes, tx_bytes, rx_pkts, tx_pkts, rx_drops, tx_drops }
Health          {} → { up_since, flow_count, socket_stats }
```

### Control plane: actor model per UE

Each UE is a coroutine with an `asyncio.Queue` mailbox. NGAP dispatcher deposits decoded messages into the right mailbox and moves on. Only one coroutine touches per-UE state — no locks.

```
  SCTP socket
       │  asyncio read
       ▼
  NgapCodec.decode (pycrate)
       │  route by AMF_UE_NGAP_ID | RAN_UE_NGAP_ID
       ▼
  UeActor.mailbox.put_nowait(msg)       # MUST not block
       │
       │  UeActor coroutine drains mailbox
       ▼
  State machine (async):
       ├── Auth Request  → compute response → gnb.send_nas(ue, resp)
       ├── PDU Accept    → await dp.setup_tunnel(...)
       └── Release       → await dp.teardown_tunnel(...)
```

**Backpressure rule**: if any per-UE mailbox hits its high-water mark, the dispatcher logs a warning, drops the oldest message, and increments a Prometheus counter. No silent stalls.

### Data plane internals

Written in Rust with `tokio` + `tokio-tun`. Skeleton (for reference, Phase 3):

```rust
tokio::select! {
    Ok((pkt, _)) = udp_sock.recv_from(&mut buf) => {
        let teid = parse_gtpu_teid(&pkt);
        if let Some(flow) = flows_by_teid.get(&teid) {
            let inner = strip_gtpu_header(&pkt);
            tun.write_all(inner).await?;
            flow.rx_bytes.fetch_add(inner.len() as u64, Relaxed);
        }
    }
    Ok(n) = tun.read(&mut buf) => {
        let dst = parse_ipv4_dst(&buf[..n]);
        if let Some(flow) = flows_by_ip.get(&dst) {
            send_gtpu(&udp_sock, flow, &buf[..n]).await?;
        }
    }
    Ok(msg) = ctrl_rx.recv() => handle_control_msg(&mut flows, msg).await,
}
```

Per-core scaling: shard flows by `hash(teid) % N` across N worker tasks. No locks on the hot path — each worker owns its partition. Stats updated with `AtomicU64::fetch_add(Relaxed)`.

## 5. Phased roadmap

Every phase must regression-pass all earlier phases. No phase merges until its verification row passes.

| Phase | Scope | Deliverable | Verification |
|---|---|---|---|
| **1 — Netlink** | pyroute2 replaces every `subprocess.run(['ip', ...])` in gtpu.py | `_configure_tun` at 0.5 ms not 100 ms | 16-UE test: 16/16 tunnels in < 1 s, no SCTP aborts |
| **2 — Async CP** | Rewrite `GnbStateMachine` + `UeStateMachine` on asyncio; single SCTP recv coroutine; UE actors with mailboxes | `tests/bench/attach_bench.py` attaches 1000 UEs in < 10 s | 1k UEs register + get PDU; no thread-stack exhaustion |
| **3 — Rust DP** | `dp-rust/` workspace with TUN + GTP-U + Unix ctrl socket. Python CP switches to `dp-client.py` | sustained 1 Gbps per-UE iperf3 | iperf3 ≥ 95% line rate; flat per-phase histograms |
| **4 — Shard** | Controller spawns one gNB-worker process per gNB | 10k UE attach across ~10 workers | cumulative ≥ 10 Gbps; p99 attach < 500 ms |
| **5 — Obs + harden** | Prometheus `/metrics` on CP and DP; backpressure policies; graceful degradation | Load-test report, known limits published | Runbook: "at 12k UEs, worker memory plateaus at X GB, degradation is linear" |

## 6. File / module layout (post-refactor)

```
src/
  control/                    # Phase 2+
    app.py                    # asyncio entry point
    controller.py             # spawns / manages workers
    event_loop.py             # shared loop, shutdown
    sctp/
      async_sctp.py           # asyncio wrapper around pysctp
    ngap/
      codec.py                # pycrate
      dispatcher.py           # route to actors
    fsm/
      gnb_actor.py
      ue_actor.py
    netlink/
      client.py               # pyroute2 ops on a dedicated executor
  dataplane/                  # Phase 3 (Python side)
    client.py                 # Unix socket client
    proto.py                  # wire format
  testcases/                  # unchanged public surface
  protocol/                   # legacy shims removed in Phase 2
  ...

dp-rust/                      # Phase 3 (Rust workspace)
  Cargo.toml
  src/
    main.rs
    tun.rs
    gtpu.rs
    flows.rs
    control.rs
    stats.rs

libs/pycrate/                 # unchanged (golden for ASN.1)
```

## 7. Explicit non-goals

- **Don't rewrite the control plane in Go.** Python asyncio handles 10k coroutines fine; GIL is irrelevant for I/O-bound code; pycrate is valuable.
- **Don't use DPDK / AF_XDP in Phase 3.** tokio + plain UDP sockets saturate 10 Gbps on a 2020+ x86 box. DPDK is relevant past ~40 Gbps.
- **Don't keep per-UE kernel routing.** That's the failure mode at 16 UEs. User-space demux in the Rust DP is both faster and simpler.
- **Don't shard by thread inside Python.** GIL makes it pointless. Shard by process.

## 8. Open questions

These are decisions the first maintainer touching the phase can make freely:

- CBOR vs protobuf for CP↔DP wire format. (CBOR is simpler to hand-write; protobuf is more ecosystem-friendly. Either is fine.)
- Whether the Rust DP owns the UDP socket exclusively or shares via `SO_REUSEPORT` across multiple DP processes. (One DP per host is enough for Phase 3; add sharding only if needed.)
- pyroute2's default `IPRoute()` vs `NDB()` in the netlink executor. (`IPRoute` is lower-level and enough for our operations.)

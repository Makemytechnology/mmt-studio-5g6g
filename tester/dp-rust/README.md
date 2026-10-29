# dp-rust — SA Tester data plane (Phase 3)

Not implemented yet. This workspace will contain the Rust binary that
owns the actual GTP-U data path: one TUN device, one UDP socket on 2152,
one Unix stream socket for control from the Python workers.

See `docs/ARCHITECTURE.md` §3 (Architecture overview), §4 (Component
contracts), §5 row "Phase 3 — Rust DP".

Planned crate layout:

```
dp-rust/
  Cargo.toml
  src/
    main.rs       # tokio runtime bootstrap
    tun.rs        # single TUN owned by the DP
    gtpu.rs       # encap / decap on UDP/2152
    flows.rs      # ue_ip → teid / teid → ue_ip hash tables, sharded by hash(teid)
    control.rs    # Unix stream socket server — length-prefixed CBOR
    stats.rs      # per-UE counters in a lock-free slab
```

## Wire protocol (CP ↔ DP)

Length-prefixed (u32 BE) CBOR. Request/reply matched by `seq` field.

```cbor
# SetupTunnel
{ "op": "setup", "seq": N,
  "ue_imsi": "001010000000001", "ue_ip": "10.45.0.2",
  "local_teid": 0x00010001, "remote_teid": 0x00010002,
  "upf_peer_ip": "192.168.1.107", "qfi": 1 }

# TeardownTunnel
{ "op": "teardown", "seq": N, "local_teid": 0x00010001 }

# GetStats
{ "op": "stats", "seq": N, "local_teid": 0x00010001 }
# → { "seq": N, "ok": true,
#     "rx_bytes": 123, "tx_bytes": 456,
#     "rx_pkts": 9, "tx_pkts": 8, "rx_drops": 0, "tx_drops": 0 }

# Health
{ "op": "health", "seq": N }
# → { "seq": N, "ok": true, "up_since": 1_700_000_000, "flow_count": 16 }
```

## Acceptance

- Sustained 1 Gbps per-UE iperf3 through the DP.
- 100 concurrent UDP flows.
- Per-phase latency histograms show flat p50/p99 distribution.
- Graceful shutdown: control-plane disconnect → DP drops all flows and
  keeps running (does not exit) so next CP connection can attach.

## Dependencies (when Phase 3 opens)

- `tokio` with `rt-multi-thread`, `net`, `io-util`, `fs`
- `tokio-tun` (owns the TUN device)
- `socket2` (for `SO_REUSEPORT`, `SO_BINDTODEVICE`)
- `serde_cbor` or `minicbor` (wire format — decide in Phase 3 PR)
- `dashmap` or sharded `hashbrown::HashMap` (flow tables)
- `metrics` + `metrics-exporter-prometheus` (observability)

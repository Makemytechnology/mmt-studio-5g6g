# src/dataplane — Python client to the Rust data plane (Phase 3)

Not implemented yet. This package will carry the **Python-side** Unix
stream socket client that talks to `dp-rust/` (the Rust data-plane
binary that owns the TUN fds, GTP-U UDP socket, and flow tables).

See `docs/ARCHITECTURE.md` §4 "Worker ↔ Data plane" for the wire protocol.

Planned layout:

```
src/dataplane/
  client.py     # AsyncUnixClient with {setup_tunnel, teardown_tunnel, get_stats}
  proto.py      # length-prefixed CBOR/protobuf message codec
```

The data-plane binary itself lives in the repo-root `dp-rust/` workspace.

Control-plane code will obtain a client instance from
`src.dataplane.client.default_client()` — analogous to how
`TrafficAgent.default()` works today — and switch from calling
`GtpuManager.create_tunnel()` directly to issuing `SetupTunnel` RPCs.

Acceptance (see ARCHITECTURE.md table):
- Sustained 1 Gbps per-UE iperf3 through the Rust DP
- 100 concurrent UDP flows, flat per-phase histograms

# Control Plane

Covers everything between the SCTP socket and the testcase: NGAP/NAS
codecs, the gNB and UE state machines, NAS security primitives, and the
asyncio actor model that's replacing the legacy threaded FSM.

This doc is the design overlay; see [ARCHITECTURE.md §4](ARCHITECTURE.md)
for the formal contracts and the in-package note at
[`src/control/README.md`](../src/control/README.md) for the parts that
have already shipped.

## 1. Two control planes coexist today

| | Legacy (`src/statemachine/`) | New async (`src/control/`) |
|---|---|---|
| Concurrency model | one thread per UE | one coroutine per UE on a single asyncio loop |
| Used by testcases? | **yes — all 47 modules** | not yet — Reg flow only, exercised by `tests/bench/attach_bench.py` |
| SCTP wrapper | `src/protocol/sctp.py` (blocking) | `src/control/sctp/async_sctp.py` (asyncio) |
| Mailbox / dispatch | implicit per-thread | explicit `asyncio.Queue(maxsize=64)` per UE; oldest-message-drop on overflow |
| GIL impact | thread starvation > ~500 UEs | irrelevant for I/O-bound work |
| Migration phase | being deprecated | Phase 2 of [ARCHITECTURE.md §5](ARCHITECTURE.md) |

**Don't** add features to `src/statemachine/`. New work goes into
`src/control/` even if a testcase migration follows in a separate PR.

## 2. Layer cake

```
   testcase
      │
      ▼
   GnbActor / UeActor  (src/control/fsm/*)              ← actor coroutines
      │
      ▼
   NgapDispatcher       (src/control/ngap/dispatcher.py) ← decode + route
      │
      ▼
   AsyncSctp            (src/control/sctp/async_sctp.py) ← asyncio over pysctp
      │
      ▼
   pysctp socket        (libs/pysctp.libs/)
```

NGAP/NAS encoding remains in `src/protocol/` — those files are codec-
only and shared between the legacy and async planes. They are the
single source of truth for spec compliance.

## 3. Protocol modules (`src/protocol/`)

| File | Purpose | Key spec |
|---|---|---|
| `sctp.py` | blocking SCTP transport, NGAP envelope framing, peer registry | TS 38.412 §7 (PPID 60) |
| `ngap.py` | NGAP encode/decode via pycrate, builders for every elementary procedure | TS 38.413 |
| `nas.py` | NAS message construction (Reg Req, Auth Resp, SMC, Service Req, ...) | TS 24.501 |
| `nas_security.py` | NAS wrap/unwrap, MAC compute/verify, count windowing | TS 24.501 §4.4.3 |
| `crypto.py` | NEA1/NEA2/NEA3, NIA1/NIA2/NIA3, MILENAGE/TUAK | TS 33.501 §D, TS 35.205/206 |
| `gtpu.py` | GTP-U encap/decap, TUN device, per-UE policy routing (legacy) | TS 29.281 |
| `gtpu_codec.py` | pure-codec GTP-U headers (no kernel side-effects) — used by unit tests | TS 29.281 |
| `sip_client.py` / `rtp_stream.py` | IMS SIP UAC + RTP audio/video | RFC 3261, RFC 3550 |
| `positioning.py` | LMF positioning methods (E-CID, RTT, TDOA, A-GNSS) | TS 23.273, TS 38.305 |
| `iot.py` / `ntn.py` / `safety.py` / ... | vertical protocols (NB-IoT, satellite, MBS, NPN, MCX, ...) | TS 22.369, TS 38.821, TS 23.246, TS 23.501 §5.30, TS 23.280 |
| `oam.py` | OAM management interface (NWDAF, PM, FM, Trace) | TS 28.532, TS 32.422 |
| `dpi.py` / `af.py` / `ikev2.py` / `esp.py` | DPI store, AF session/EventExposure, untrusted non-3GPP via N3IWF | TS 23.503, TS 29.522, RFC 7296, RFC 4303 |

For each module: codec changes must keep the round-trip pytest under
`tests/test_*.py` green. Spec citations in module-level constants and
docstrings must resolve under `tests/speccheck` — see
[observability.md](observability.md).

## 4. Async control plane internals

### 4.1 SCTP

`src/control/sctp/async_sctp.py`:

- One pysctp socket wrapped with `loop.add_reader()`. The reader callback
  **must not await** — it `asyncio.create_task()` the work and returns.
  Awaiting in the reader re-introduces the receive-side stall that
  forced the AMF to ABORT during the threaded-FSM era.
- Graceful shutdown sequence: `SO_LINGER(1, T)` → `shutdown(SHUT_WR)` →
  drain inbound until EOF → `close()`. Peers see `SHUTDOWN`, not
  `ABORT`. This is regression-tested via `wait_for_state` exiting fast
  on the ERROR transition rather than spinning until timeout.

### 4.2 NGAP dispatcher

`src/control/ngap/dispatcher.py`:

- Decode the envelope using pycrate.
- Route by `(AMF_UE_NGAP_ID, RAN_UE_NGAP_ID)` to the right `UeActor`.
- `mailbox.put_nowait(msg)`. If `QueueFull`, increment the
  `dropped_messages` counter, drop the oldest message, log a warning.
  No silent stalls.

### 4.3 Actor model

`src/control/fsm/{gnb_actor,ue_actor}.py`:

- `GnbActor` owns the SCTP connection, NG Setup, RAN UE NGAP ID
  allocator, and the table of attached UEs.
- `UeActor` owns the NAS state machine. State transitions go through
  `_set_state(new_state)` which **replaces** `self._state_event` — any
  coroutine blocked in `wait_for_state(target)` is released by GC of
  the old event. Don't simplify to a single `Event.set()`; a shared
  event can't be re-armed without a race window.

The Reg flow that's currently coded:

```
Initial UE Message  ─►  Auth Request  ─►  Auth Response (compute via crypto.py)
                    ◄─                ◄─
SMC                 ─►  SMC Complete
                    ◄─
Initial Context Setup  ─►  Initial Context Setup Response
                       ◄─
Registration Accept ─►  Registration Complete
                    ◄─
```

PDU session establishment (the next step) currently stops at NGAP-ACK in
the async plane; the GTP-U tunnel hook is a stub waiting on Phase 3.
See [data-plane.md](data-plane.md).

### 4.4 Netlink

`src/control/netlink/client.py`:

- All pyroute2 work runs on a single dedicated executor thread.
  pyroute2 ≥ 0.9 uses asyncio internally and conflicts with our event
  loop — going through the executor isolates them.
- Replaces ~100 ms `subprocess.run(['ip', 'addr', 'add', ...])` calls
  with sub-millisecond netlink syscalls. This is what unblocks Phase 4
  (per-UE provisioning at 10k UEs).

## 5. Acceptance hooks

`tests/bench/attach_bench.py` is the async-CP benchmark. Acceptance from
[ARCHITECTURE.md §5 Phase 2](ARCHITECTURE.md): 1000 UEs register and get
PDU in under 10 seconds. Run it against the Go AMF or the in-tree mock:

```sh
python3 -m tests.bench.attach_bench --ues 1000 --amf 192.168.1.107
```

The bench is the only end-to-end exercise of `src/control/` today. Until
the testcase migration starts, regressions in the async plane will only
surface here.

## 6. Audit anchors

- [go_reference_gap.md](go_reference_gap.md) — what the Go reference
  covers vs. Python. Five real protocol bugs (Authentication Failure
  field path, Service Request subfield, Path Switch Request mandatory
  IE, illegal-BCD acceptance) are filed as xfails in `tests/test_*.py`
  — fixing one auto-flips its xfail to pass.
- [speccheck_punchlist.md](speccheck_punchlist.md) — three of the
  ten outstanding `MISSING` citations are in control-plane code:
  `gnb_fsm.py:1099` (TS 38.413 §8.1.3), `ue_fsm.py:228`
  (TS 33.501 §6.1.3.4), `ngap.py:307` (TS 38.413 §8.1.3.2). Resolve
  during the next FSM refactor pass.

## 7. Open design questions

These are deliberately not decided — first maintainer touching the
relevant phase decides:

- **CBOR vs protobuf** for the CP↔DP wire (Phase 3). CBOR wins on
  hand-writing simplicity; protobuf wins on schema evolution.
- **Sharding granularity** in cluster mode: one worker per gNB vs. one
  per N gNBs. Phase 4 starts with 1:1, revisit at scale measurements.
- **Mailbox depth** (currently 64 messages). High enough to absorb a
  burst of NGAP retransmissions; low enough that backpressure is
  meaningful. Tune from observed drop rates.

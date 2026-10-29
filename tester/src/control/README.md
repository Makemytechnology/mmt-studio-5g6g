# src/control — Async control plane (Phase 2)

Phase 2a + 2c of `docs/ARCHITECTURE.md` live here. The legacy threaded
state machines under `src/statemachine/` still drive all existing
testcases; this package is the new async implementation that will
replace them over the coming phases.

## What's implemented

```
src/control/
├── event_loop.py           # spawn_task + Shutdown + signal handlers
├── sctp/
│   └── async_sctp.py       # pysctp socket, asyncio reader, graceful SHUTDOWN
├── ngap/
│   └── dispatcher.py       # decode NGAP → route to actor mailbox
├── netlink/
│   └── client.py           # pyroute2 on a dedicated 1-thread executor
└── fsm/
    ├── gnb_actor.py        # SCTP + NG Setup + UE orchestration
    └── ue_actor.py         # async NAS FSM — Auth + SMC + Reg
```

## What works

- One SCTP recv coroutine per gNB. The recv path **cannot stall** on UE
  work — it hands off via mailbox and returns.
- Per-UE actor with `asyncio.Queue(maxsize=64)` mailbox. Backpressure is
  explicit: when the queue fills, the dispatcher drops the oldest
  message and logs, so a single stuck UE doesn't freeze the pipeline.
- Registration flow is complete: InitialUEMessage → Auth Request/Response
  → Security Mode Command/Complete → Initial Context Setup → Registration
  Accept/Complete. All NAS parsing and 5G crypto reuses `src/protocol/`
  unchanged.
- Graceful SCTP disconnect (SO_LINGER + shutdown + drain + close) —
  peers see SHUTDOWN, not ABORT.
- `wait_for_state` bails fast when the gNB enters ERROR, so a peer-side
  abort never holds up the test.

## What's missing (scoped to later commits)

- **Phase 2b/2d** (next commit): a mock AMF that replies to the
  attach flow + `tests/bench/attach_bench.py` measuring 1000 UE attach
  latency. Acceptance: p99 < 10 s. Until the bench exists this work is
  unverifiable in isolation — we're relying on code review + unit-level
  smoke tests for now.
- **Phase 3**: PDU session → GTP-U tunnel creation via the Rust data
  plane. `UeActor._on_pdu_session_resource_setup` currently only ACKs
  the NGAP response; the tunnel hook is a stub.
- **Testcase migration**: none of the existing `src/testcases/*.py`
  calls into `src.control.*` yet. They continue to use the threaded
  implementation. Migration is a separate PR per category (traffic,
  IMS, core, etc.).

## Usage (inside an async function)

```python
from src.control.fsm.gnb_actor import GnbActor
from src.control.fsm.ue_actor import UeActor

async def attach_one(sim):
    gnb = GnbActor(amf_ip="192.168.1.107")
    await gnb.connect()                             # SCTP + NG Setup

    ue = UeActor(sim, gnb=gnb, ran_ue_ngap_id=gnb.alloc_ran_ue_id())
    gnb.attach_ue(ue)
    await ue.start()
    await ue.register()
    ok = await ue.wait_for_state("REGISTERED", timeout=15)
    assert ok

    await ue.stop()
    await gnb.disconnect()                          # clean SHUTDOWN
```

## Design notes worth rereading before editing

- The recv coroutine in `AsyncSctp._on_readable` **never awaits**. It
  `asyncio.create_task(on_recv(data))` and returns. If you add awaits
  there you re-introduce the stall that forced the Go-AMF ABORT earlier.
- All netlink work goes through the single-thread executor in
  `netlink/client.py`. Do not add `pyroute2.IPRoute()` calls directly —
  pyroute2 0.9+ uses asyncio internally and conflicts with our loop.
- `UeActor._set_state` replaces `self._state_event` on every transition
  so any waiter blocked on the old event is released. This is the
  correct pattern for "wake-all" in asyncio; don't simplify to a shared
  `Event.set()` without a replacement strategy.

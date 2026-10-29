# Test Cases

Three layered test surfaces:

1. **Pytest unit tests** (`tests/`) — protocol codec correctness, no
   live AMF needed. Run on every commit.
2. **Python testcases** (`src/testcases/`) — end-to-end flows against
   a live SA Core. Run from the web UI, the CLI, or via Robot.
3. **Robot Framework suites** (`robot/suites/`) — operator-friendly
   regression matrix. Each `.robot` file is a thin wrapper around
   the Python testcases.

This doc describes the contract each layer exposes, how the three
fit together, and the conventions for adding new tests.

## 1. Layer 1 — pytest unit tests (`tests/`)

39 modules. Pure-Python, no live core, no kernel side-effects (TUN /
netlink / SCTP). Used to lock down codec round-trips, security
primitives, and edge-case handling.

| Area | Files |
|---|---|
| NAS | `test_nas_message_round_trips.py`, `test_nas_decode_errors.py`, `test_nas_security_primitives.py` |
| NGAP | `test_ngap_envelope.py`, `test_ngap_handover_messages.py`, `test_ngap_pws.py` |
| GTP-U | `test_gtpu_codec.py`, `test_gtpu_known_vector.py` |
| Crypto / IPSec | `test_security.py`, `test_esp.py`, `test_esp_known_vector.py`, `test_ikev2*.py` (6 files) |
| Codecs | `test_sms_codec.py`, `test_ss_codec.py`, `test_plmn_bcd.py` |
| Verticals | `test_iot.py`, `test_ntn.py`, `test_v2x.py`, `test_uas.py`, `test_prose.py`, `test_safety.py`, `test_emergency.py`, `test_esim.py`, `test_frmcs.py`, `test_mcx_floor.py`, `test_seal.py`, `test_oam.py`, `test_dpi.py`, `test_af.py`, `test_edge.py`, `test_positioning.py` |
| Access / mobility | `test_access_mobility.py`, `test_n3iwf_e2e_userplane.py`, `test_eap5g.py` |
| Speccheck (gate) | `tests/speccheck/` |
| Bench | `tests/bench/attach_bench.py` (asyncio CP benchmark) |

Run:

```sh
.venv/bin/python -m pytest tests/ -v                  # full
.venv/bin/python -m pytest tests/test_nas_*.py -v     # area-scoped
.venv/bin/python -m pytest tests/speccheck -v         # citation gate
```

**xfail policy:** unit tests must be deterministic. An xfail on a unit
test is a *real bug* with a one-line repro, filed for the next
maintenance pass — see the five outstanding xfails in
[go_reference_gap.md](go_reference_gap.md).

## 2. Layer 2 — Python testcases (`src/testcases/`)

47 modules under 8 domains. Each subclasses `TestCase` from
`src/testcases/base.py` and implements `setup()`, `run()`, `teardown()`.

```
src/testcases/
├── base.py                # TestCase, TestResult, _ensure_ip_on_interface
├── registry.py            # discover_all() — auto-imports tc_*.py modules
├── test_runner.py         # TestRunner — register, run, collect results, persist to SQLite
├── robot_parser.py        # parses robot/suites/*.robot into the catalog
├── core/        # 5GC core: registration, auth, NG setup, PDU, slicing, handover, idle, multi-DNN, release  (9 files)
├── traffic/     # iperf3 / RTP throughput, multi-traffic, jumbo, stress, sequences, benchmarks  (7 files)
├── ims/         # VoNR / ViNR, conference, mid-call upgrade, scale  (2 files)
├── edge/        # MEC EAS, ProSe, ranging, positioning, ISAC, TSN  (6 files)
├── safety/      # MBS, NPN, IOPS, MCX, PWS, RACS, disaster roaming  (7 files)
├── vas/         # charging, NWDAF exposure, NIDD, SMS, supplementary, USSD, MUSIM, NSAAS, URSP  (9 files)
├── infra/       # NSACF, RAN sharing, NTN phase 2, resilience  (4 files)
└── vertical/    # IoT (NB/Ambient), NTN, PIN, SEAL, UAS  (5 files)
```

### 2.1 TestCase contract

```python
from src.testcases.base import TestCase, TestResult

class SingleRegistration(TestCase):
    name = "TC-REG-001"
    description = "Single UE registers and reaches REGISTERED state"
    suite = "registration"

    def setup(self, ctx):
        # ctx provides gnb_pool, ue_pool, sim, gnb_config, infra_config
        # Build a UE state machine, attach to gNB, prep TUN if needed
        ...

    def run(self, ctx, result: TestResult):
        # Drive the FSM, assert on intermediate states + final outcome
        # On any spec-compliance violation, result.fail("reason"); return
        ...

    def teardown(self, ctx):
        # Release UE context, close SCTP gracefully, remove TUN, drop policy routes
        ...
```

Constraints:

- `setup` and `teardown` must be idempotent. Test runs may abort
  mid-flight; a leaked TUN or policy route blocks the next run.
- `run` must not catch and swallow exceptions silently. Spec
  violations from the AMF are signal — they must fail the test.
- No fallbacks. If the AMF returns a 5GMM cause we don't expect, the
  test fails. (See "Standards Compliance" in
  [overview.md §5](overview.md).)

### 2.2 Discovery

`registry.discover_all()` walks `src/testcases/`, imports every
`tc_*.py`, and collects either:

1. an `ALL_*_TCS` list/tuple at module level (preferred — explicit), or
2. all `TestCase` subclasses defined in the module (fallback).

`src/app.py:82` registers the discovered set with `TestRunner` at
boot. New modules don't need a manual edit anywhere — drop them in
the right subdirectory and they're picked up.

### 2.3 Runner

`src/testcases/test_runner.py`:

- Owns the in-memory test registry.
- Loads Robot suites (`load_robot_suites(robot_dir)`) so the catalog
  knows which Robot tests map to which Python testcases.
- Persists every run to SQLite under `data/satester.db` — `run_id`,
  per-test result, metrics, attached artifacts. `src/db/runs.py`
  + `src/db/reports.py` are the API. See
  [web-api.md](web-api.md) for how the UI reads them back.

## 3. Layer 3 — Robot Framework (`robot/suites/`)

31 `.robot` files grouped by domain. Each is the **regression face**
of the testcases — what an operator runs to validate a build.

```
robot/suites/
├── access/             # registration, NG setup, auth, release, idle
├── session/            # PDU session, multi-DNN, slicing
├── mobility/           # handover, roaming
├── traffic/            # QoS, multi-UE, jumbo, DPI, stress
├── voice_media/        # IMS, IMS scale, MCX, emergency
├── policy_charging/    # charging, MEC, NWDAF
├── regulatory/         # lawful intercept, trace
├── diagnostics/        # stress
└── other/              # positioning, IoT, NTN, V2X, eSIM, sidelink
```

Robot suites resolve test names by `tc_id` against the testcase
registry — `src/testcases/robot_parser.py` does the parsing. Adding a
new testcase + the corresponding `[Tags] tc_id=TC-...` line in a
`.robot` file is enough for both surfaces to see it.

### Run from CLI

```sh
python3 -m src.cli run --suite 08_ims --report html       # one suite
python3 -m src.cli run --report html junit --exit-code     # full regression
python3 -m src.cli analysis pass-rate                      # historical trends
python3 -m src.cli status --run latest                     # last run status
```

Exit code `--exit-code` is wired for CI: nonzero on any FAIL / ERROR.

## 4. Test catalog — what's covered

| # | Suite | Tests | Coverage |
|---|---|---|---|
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
| 16 | Slicing | 10 | eMBB / URLLC / MIoT slices |
| 17 | Positioning | 10 | E-CID, RTT, TDOA, GNSS, geofence |
| 18 | IoT | 15 | NB-IoT, NIDD/SCEF, Ambient IoT |
| 19 | NTN | 12 | Satellite, coverage, timing, TAI |

Total: **202 test cases across 19 suites**.

## 5. Adding a new test — checklist

1. Drop a `TestCase` subclass into the right subdirectory of
   `src/testcases/` (or extend an existing module). `tc_id` must be
   unique and follow the `TC-<DOMAIN>-NNN` convention.
2. Anchor every assertion to a spec citation. Citations in module
   docstrings / constants / `assert` messages get scanned by
   speccheck — see [observability.md §3](observability.md). Don't
   invent clause numbers; the gate will catch you.
3. Add a Robot wrapper in `robot/suites/<domain>/` referencing
   `tc_id` in `[Tags]`. Operators won't run something that's
   Python-only.
4. If the testcase touches a not-yet-tested codec path, also write a
   pytest unit test under `tests/test_<area>.py`. Codec bugs surface
   faster as unit tests than as end-to-end failures.
5. Do **not** add a fallback for non-compliant AMF behavior. The test
   fails. That's the value the tester delivers — see
   [overview.md §5](overview.md).
6. Record per-suite design notes in `docs/training_notes/<area>/`
   when the test exercises something subtle. Keeps the knowledge
   alongside the source.

## 6. Acceptance & gates

- `pytest tests/` — must pass on every commit.
- `pytest tests/speccheck` — must pass; **do not merge with
  `SPECCHECK_LOOSE=1`** (see [speccheck_punchlist.md](speccheck_punchlist.md)).
- `python3 -m src.cli run --report junit --exit-code` against a known-
  good SA Core — must pass before release. The junit XML is the
  artifact CI consumes.
- `tests/bench/attach_bench.py` — Phase 2 acceptance lives here;
  must clear 1000 UE attach in under 10 s once the async plane is
  the default.

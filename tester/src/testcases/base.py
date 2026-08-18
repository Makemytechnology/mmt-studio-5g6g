# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test case base class and result — composable building blocks.

Metadata model:
    Every concrete TestCase subclass MUST set a class-level
    SPEC = TestSpec(...) — declared in src.testcases.spec. The metadata
    is the canonical source for the tc_id, the title, the 3GPP spec
    citation, the involved NFs, the slice/DNN, severity, tags, and
    the setup contract. GUI groups, run-subsets, and reports all
    derive from SPEC; the filename and category strings of the
    legacy era are no longer load-bearing.

    Enforcement runs in __init_subclass__:
      - Concrete subclasses (the ones registry imports) with no SPEC
        raise TestCaseDefinitionError at import time. A typo or a
        forgotten field surfaces during `pip install`-style discovery,
        not during a nightly run.
      - Abstract intermediates (mixins, partial bases) opt out by
        setting `_abstract = True`.
      - During the migration window, set TESTER_SPEC_STRICT=0 in the
        environment to demote the failure to a WARNING. Defaults to
        strict; flip per-CI-job until every legacy TC is retrofitted.
"""

import os
import time
import logging
import subprocess

from src.testcases.spec import TestSpec, TestSpecError

log = logging.getLogger("tester.testcases")


class TestCaseDefinitionError(Exception):
    """Raised when a TestCase subclass omits / mis-declares its SPEC.

    Hard error by default (TESTER_SPEC_STRICT=1, the default). The
    error names the offending class and the missing/invalid field
    so the operator can fix the source rather than chase an opaque
    "test not registered" later.
    """


# Default enforcement mode. STRICT now that every TC under
# src/testcases/ declares a SPEC (inventory reports 0 missing).
# Concrete subclasses that omit SPEC fail at class-definition time;
# adding a new TC without metadata is impossible to miss.
#
# Operators can opt out per-process via TESTER_SPEC_STRICT=0 for
# bisects or local experiments — but the default is enforcement.
_SPEC_STRICT_DEFAULT = "1"


def _spec_strict() -> bool:
    return os.environ.get("TESTER_SPEC_STRICT", _SPEC_STRICT_DEFAULT) \
        not in ("0", "false", "no")


def _spec_missing(cls) -> None:
    msg = (
        f"{cls.__module__}.{cls.__name__} has no SPEC metadata. "
        "Concrete TestCase subclasses must declare a class-level "
        "SPEC = TestSpec(...) — see src.testcases.spec. Set "
        "`_abstract = True` on the class to opt out (mixin / partial "
        "base only)."
    )
    if _spec_strict():
        raise TestCaseDefinitionError(msg)
    log.warning("[transitional] %s", msg)


def _spec_bad_type(cls, value) -> None:
    msg = (
        f"{cls.__module__}.{cls.__name__}.SPEC is {type(value).__name__}, "
        f"expected TestSpec (see src.testcases.spec). Fix the class "
        f"definition: SPEC = TestSpec(tc_id=..., ...)."
    )
    if _spec_strict():
        raise TestCaseDefinitionError(msg)
    log.warning("[transitional] %s", msg)


def _ensure_ip_on_interface(iface, gnb_ip):
    """Ensure gnb_ip is available on iface. Creates alias if needed.

    If gnb_ip matches the interface's existing IP → no alias needed.
    If gnb_ip differs → create alias (enp0s3:1, enp0s3:2, etc.)
    Returns the device name used (iface or iface:N).
    """
    if not iface or not gnb_ip:
        return None

    # Check if gnb_ip already exists on any interface
    try:
        r = subprocess.run(['ip', '-4', '-o', 'addr', 'show'],
                           capture_output=True, text=True, timeout=5)
        for line in r.stdout.strip().split('\n'):
            if not line.strip():
                continue
            parts = line.split()
            existing_iface = parts[1]
            existing_ip = parts[3].split('/')[0]
            if existing_ip == gnb_ip:
                log.info("IP %s already exists on %s — no alias needed", gnb_ip, existing_iface)
                return existing_iface
    except Exception as e:
        log.warning("Failed to check existing IPs: %s", e)

    # IP not found — create alias on the interface
    # Find next available alias number
    try:
        r = subprocess.run(['ip', '-4', '-o', 'addr', 'show', 'dev', iface],
                           capture_output=True, text=True, timeout=5)
        existing_aliases = set()
        for line in r.stdout.strip().split('\n'):
            if not line.strip():
                continue
            parts = line.split()
            dev = parts[1]
            if ':' in dev:
                try:
                    existing_aliases.add(int(dev.split(':')[1]))
                except ValueError:
                    pass

        alias_num = 1
        while alias_num in existing_aliases:
            alias_num += 1
        alias_dev = f"{iface}:{alias_num}"

        log.info("Creating alias %s with IP %s", alias_dev, gnb_ip)
        # In-container we run as root (NET_ADMIN granted) and `sudo` is
        # not installed; on a developer host we're a normal user and
        # need sudo for `ip addr add`. Detect at call time.
        cmd = ['ip', 'addr', 'add', f'{gnb_ip}/24', 'dev', iface,
               'label', alias_dev]
        if os.geteuid() != 0:
            cmd = ['sudo'] + cmd
        subprocess.run(cmd, capture_output=True, timeout=5, check=True)
        log.info("Created alias %s = %s", alias_dev, gnb_ip)
        return alias_dev
    except subprocess.CalledProcessError as e:
        log.warning("Failed to create alias on %s for %s: %s", iface, gnb_ip, e.stderr)
        return None
    except Exception as e:
        log.warning("Failed to create alias: %s", e)
        return None


class TestResult:
    """Outcome of a test case execution."""

    def __init__(self, test_name):
        self.test_name = test_name
        self.status = "PENDING"   # PASS | FAIL | ERROR | TIMEOUT | RUNNING
        self.duration_ms = 0
        self.details = {}
        self.error = None
        self.timestamp = time.strftime("%Y-%m-%d %H:%M:%S")
        self.logs = []              # [{ts, level, logger, msg}]
        self.protocol_trace = []    # [{dir, time, proc, msg_type, hex, size, gnb}]
        # Set by TestRunner when in-process tcpdump produced a per-
        # test pcap; consumers (UI, AI analyzer) can fetch the file
        # via GET /api/tests/runs/<basename>/pcap. None when capture
        # was skipped (tcpdump absent, no perms, etc).
        self.pcap_path = None
        self.run_id = None          # filename stem of the pcap

    def to_dict(self):
        return {
            "test_name": self.test_name,
            "status": self.status,
            "duration_ms": round(self.duration_ms, 1),
            "details": self.details,
            "error": str(self.error) if self.error else None,
            "timestamp": self.timestamp,
            "logs": self.logs[-100:] if self.logs else [],
            "protocol_trace": self.protocol_trace[-50:] if self.protocol_trace else [],
            "run_id": self.run_id,
            "pcap_path": self.pcap_path,
        }


class TestCase:
    """Base class for all test cases.

    Each concrete subclass declares its metadata via a class-level
    SPEC = TestSpec(...) (see src.testcases.spec). The legacy
    attributes (name, tc_id, description, category) are derived from
    SPEC for backwards compat with the runner and GUI; new code
    should read .SPEC directly.

    Usage::

        from src.testcases.spec import TestSpec, Domain, NF, Severity

        class TC_InitialRegistration(TestCase):
            SPEC = TestSpec(
                tc_id="TC-REG-001",
                title="Initial registration over 3GPP access",
                spec="TS 24.501 §5.5.1.2.4",
                domain=Domain.REGISTRATION,
                nfs=(NF.AMF, NF.AUSF, NF.UDM),
                severity=Severity.BLOCKER,
                tags=("smoke", "conformance"),
            )

            def run(self):
                gnb = self.require_gnb()
                ue  = self.require_ue()
                self.register_ue(ue, gnb)
                self.pass_test(imsi=ue.imsi)

    Abstract intermediates (mixin bases, partial templates) opt out
    of SPEC enforcement by setting `_abstract = True`.
    """

    # Declarative metadata. Concrete subclasses MUST override.
    SPEC: TestSpec = None
    # Set True on abstract bases / mixins to skip SPEC enforcement.
    _abstract: bool = False

    # Legacy fields — populated from SPEC by __init_subclass__ so
    # existing runner / GUI code keeps working unchanged. New code
    # should consume .SPEC directly.
    name: str = ""
    tc_id: str = ""
    description: str = ""
    category: str = ""

    # Registry — populated by __init_subclass__. Keyed by tc_id so
    # the inventory / GUI / report tools have a single source of
    # truth.
    REGISTRY: dict = {}

    def __init_subclass__(cls, **kwargs):
        super().__init_subclass__(**kwargs)
        # Pure mixin / abstract base — skip enforcement. Use the
        # class's OWN __dict__, not getattr, so concrete subclasses
        # of an abstract base don't silently inherit the flag and
        # disappear from the registry. (Hit during ReleaseBase /
        # ImsScaleBase / _MultiDnnBase retrofits — every child was
        # being marked abstract via MRO and never registered.)
        if cls.__dict__.get("_abstract", False):
            return

        spec = cls.__dict__.get("SPEC")  # only THIS class's value
        if spec is None:
            # Subclass that re-uses a parent's SPEC (or inherits None)
            # is treated as abstract for enforcement purposes — it has
            # nothing of its own to validate. This lets a concrete
            # ancestor define SPEC and a re-shape subclass override
            # only run() without re-declaring metadata.
            spec = getattr(cls, "SPEC", None)
            if spec is None:
                _spec_missing(cls)
                return

        if not isinstance(spec, TestSpec):
            _spec_bad_type(cls, spec)
            return

        # Populate legacy fields from SPEC so the existing runner and
        # GUI (which still read tc_id / name / description / category)
        # see a consistent picture. Sourcing from SPEC means the
        # legacy strings can't drift from the canonical metadata.
        cls.tc_id       = spec.tc_id
        cls.name        = spec.tc_id.lower().replace("-", "_")
        cls.description = spec.description or spec.title
        cls.category    = f"{spec.domain.value} ({spec.spec_ts()})"

        # Register. Duplicate tc_id is a hard error — two TCs with the
        # same ID would shadow each other in the GUI and reports.
        existing = TestCase.REGISTRY.get(spec.tc_id)
        if existing is not None and existing is not cls:
            raise TestCaseDefinitionError(
                f"Duplicate tc_id {spec.tc_id!r}: "
                f"{existing.__module__}.{existing.__name__} vs "
                f"{cls.__module__}.{cls.__name__}"
            )
        TestCase.REGISTRY[spec.tc_id] = cls

    def __init__(self, gnb_pool, ue_pool, params=None):
        self.gnb_pool = gnb_pool
        self.ue_pool = ue_pool
        self.params = params or {}
        self.result = TestResult(self.name)
        # Ensure UE pool is populated from config before any test runs
        self._load_ue_pool()

    def run(self):
        """Override in subclass. Must return self.result."""
        raise NotImplementedError

    # ─── Assertions ───

    def pass_test(self, **details):
        self.result.status = "PASS"
        self.result.details.update(details)

    def fail_test(self, error, **details):
        self.result.status = "FAIL"
        self.result.error = error
        self.result.details.update(details)

    # ─── Resource helpers ───

    def cleanup(self):
        """Clean up all resources after test completion.

        Destroys GTP-U tunnels, deregisters UEs, disconnects gNBs.
        Called automatically after each test by the test runner.
        """
        # Disconnect all gNBs (also destroys their GTP-U tunnels)
        for gnb in list(self.gnb_pool):
            try:
                gnb.disconnect()
            except Exception:
                pass
        self.gnb_pool.clear()

        # Reset all UE states (keep the instances — tests like
        # MultiRegistration read self.ue_pool directly after
        # require_gnb() and would see an empty list if we cleared it.
        # `_load_ue_pool` dedups by IMSI, so leaving instances in place
        # is safe across tests too).
        for ue in self.ue_pool:
            try:
                ue.state = "DEREGISTERED"
                ue.ran_ue_ngap_id = None
                ue.amf_ue_ngap_id = None
                ue.gnb = None
                ue.pdu_sessions.clear()
                ue.security_ctx = {
                    'KAMF': None, 'KSEAF': None,
                    'knasenc': None, 'knasint': None, 'kgnb': None,
                    'eea': 0, 'eia': 0,
                    'ul_nas_count': 0, 'dl_nas_count': 0,
                    'ABBA': b'\x00\x00',
                }
            except Exception:
                pass

    def require_gnb(self, state="READY", gnb_name=None, additional=False):
        """Create a fresh gNB from config profile for each test.

        If gnb_name is given, uses that profile. Otherwise looks at UE configs
        to find which gnb_name is needed, then picks the matching profile.
        Cleans up any stale context first (additional=False).

        additional=True supports tests that need >1 concurrent gNB (e.g.,
        same-SUPI-on-two-gNBs scenarios — exercising TS 23.501 §5.3.4
        "AMF Discovery and Selection" and TS 38.413 §8.6.1.2 per-NG-RAN
        node InitialUEMessage paths). With additional=True the existing
        gnb_pool is preserved and a NEW gNB is appended; the caller is
        responsible for `gnb_name=` so a distinct profile is used.
        """
        if not additional:
            self.cleanup()

        # Create fresh gNB from config profile
        try:
            from src.statemachine.gnb_fsm import GnbStateMachine
            from src.protocol.gnb_config import gnb_cfg_list
            import src.config as _cfg

            profiles = gnb_cfg_list(_cfg.GNB_PROFILES_PATH)
            if not profiles:
                self.fail_test("No gNB config profiles found. Configure a gNB profile first.")
                raise StopTest()

            # Find matching profile by gnb_name
            if not gnb_name:
                # Check UE configs for gnb_name to auto-select profile
                try:
                    from src.protocol.sim_db import load_sims_auto
                    sims = load_sims_auto()
                    for s in sims:
                        if getattr(s, 'gnb_name', ''):
                            gnb_name = s.gnb_name
                            break
                except Exception:
                    pass

            profile = profiles[0]
            if gnb_name:
                for p in profiles:
                    if p.get("gnb_name") == gnb_name:
                        profile = p
                        break

            # Validate — all config must come from GUI, no defaults
            required = ["gnb_id", "gnb_name", "amf_ip", "mcc", "mnc", "tac"]
            missing = [f for f in required if not profile.get(f)]
            if missing:
                self.fail_test(f"gNB config '{profile.get('gnb_name', '?')}' missing: {missing}. "
                               f"Configure all fields in gNB Config.")
                raise StopTest()
            if not profile.get("slices"):
                self.fail_test(f"gNB config '{profile['gnb_name']}' missing slices.")
                raise StopTest()

            gnb_id_str = str(profile["gnb_id"])
            gnb_id = int(gnb_id_str, 16) if gnb_id_str.startswith("0x") else int(gnb_id_str)
            slices = profile["slices"]
            for s in slices:
                if isinstance(s.get("sd"), str) and s["sd"].startswith("0x"):
                    s["sd"] = int(s["sd"], 16)

            # Ensure gnb_ip is available on the interface (alias if needed)
            gnb_ip = profile.get("gnb_ip")
            iface = profile.get("interface")
            if gnb_ip and iface:
                _ensure_ip_on_interface(iface, gnb_ip)

            # Share the live GTP-U manager — avoid `import src.app` which
            # re-executes the module (banner, DB init, registry scan, ...)
            # when `-m src.app` was used as the entry point.
            gtpu_mgr = None
            try:
                from src.protocol.gtpu import GtpuManager
                gtpu_mgr = GtpuManager.get_default()
            except Exception:
                pass

            gnb = GnbStateMachine(
                amf_ip=profile["amf_ip"],
                amf_port=profile.get("amf_port", 38412),
                gnb_id=gnb_id,
                gnb_name=profile["gnb_name"],
                mcc=profile["mcc"],
                mnc=profile["mnc"],
                tac=profile["tac"],
                slices=slices,
                source_ip=gnb_ip,
                gtpu_manager=gtpu_mgr,
            )
            ok = gnb.connect()
            if not ok:
                self.fail_test("gNB failed SCTP connect", gnb_name=gnb.gnb_name)
                raise StopTest()
            if not gnb.wait_for_state("READY", timeout=10):
                gnb.disconnect()
                self.fail_test(f"gNB NG Setup failed (state={gnb.state})", gnb_name=gnb.gnb_name)
                raise StopTest()
            self.gnb_pool.append(gnb)
            self._auto_gnb = gnb
            log.info("Created gNB '%s' from config profile for test %s", gnb.gnb_name, self.name)
            return gnb
        except StopTest:
            raise
        except Exception as e:
            self.fail_test(f"Failed to create gNB from config: {e}")
            raise StopTest()

    def _load_ue_pool(self):
        """Load UEs from config into ue_pool if it is empty."""
        if self.ue_pool:
            return
        try:
            from src.protocol.sim_db import load_sims_auto
            from src.statemachine.ue_fsm import UeStateMachine

            sims = load_sims_auto()
            if not sims:
                return
            for s in sims:
                if not any(u.imsi == s.imsi for u in self.ue_pool):
                    self.ue_pool.append(UeStateMachine(s))
            log.info("Loaded %d UE(s) from config for test %s", len(sims), self.name)
        except Exception as e:
            log.warning("Failed to auto-load UE pool: %s", e)

    def require_ue(self, imsi=None):
        """Get UE by IMSI or first available. Loads from UE config if pool is empty."""
        if imsi:
            ue = next((u for u in self.ue_pool if u.imsi == imsi), None)
        else:
            ue = self.ue_pool[0] if self.ue_pool else None

        if ue is None and not self.ue_pool:
            self._load_ue_pool()
            if imsi:
                ue = next((u for u in self.ue_pool if u.imsi == imsi), None)
            else:
                ue = self.ue_pool[0] if self.ue_pool else None

        if ue is None:
            self.fail_test(f"UE not found: {imsi or '(none configured)'}. Check UE Config.")
            raise StopTest()
        return ue

    # ─── Common workflows ───

    def register_ue(self, ue, gnb, timeout=15):
        """Attach + register + wait. Returns True on success, else fails test."""
        gnb.attach_ue(ue)
        ue.register()
        if ue.wait_for_state("REGISTERED", timeout=timeout):
            return True
        self.fail_test(f"Registration failed (state={ue.state})", imsi=ue.imsi)
        return False

    def establish_pdu(self, ue, psi=1, dnn="internet", sst=1, sd=None, timeout=15):
        """Establish PDU session + wait for IP + TUN interface. Returns True on success."""
        ue.establish_pdu_session(dnn=dnn, sst=sst, sd=sd, pdu_session_id=psi)
        deadline = time.time() + timeout
        while time.time() < deadline:
            session = ue.pdu_sessions.get(psi)
            if session and session.get("ip") and session["ip"] != "unknown":
                # Wait for GTP-U TUN interface if tunnel is being created
                tun = session.get("tun")
                if tun:
                    # TUN exists — verify interface is up
                    import subprocess
                    try:
                        r = subprocess.run(["ip", "link", "show", tun],
                                           capture_output=True, timeout=2)
                        if r.returncode == 0 and b"UP" in r.stdout:
                            return True
                    except Exception:
                        pass
                    time.sleep(0.2)
                    continue
                # No TUN info yet — tunnel might still be setting up
                if time.time() < deadline - 1:
                    time.sleep(0.2)
                    continue
                # Near timeout — accept without TUN (non-root mode)
                return True
            time.sleep(0.5)
        self.fail_test(f"PDU session {psi} not established", imsi=ue.imsi)
        return False

    def deregister_ue(self, ue, timeout=15):
        """Deregister + wait. Returns True on success."""
        ue.deregister()
        if ue.wait_for_state("DEREGISTERED", timeout=timeout):
            return True
        self.fail_test(f"Deregistration failed (state={ue.state})", imsi=ue.imsi)
        return False

    def expect_reject(self, ue, gnb, timeout=15, expected_cause=None, **register_kwargs):
        """Drive a register() that is *expected* to be rejected.

        Attaches the UE, calls `ue.register(**register_kwargs)`, then
        polls until either:
          - `ue.last_reject_cause` is set (AMF rejected), or
          - `ue.state == REGISTERED` (AMF accepted — test should fail), or
          - the timeout fires.

        Returns (rejected: bool, cause: int|None). On rejection with the
        wrong cause when `expected_cause` is specified, the test result is
        flagged as FAIL with a clear message. The caller is then free to
        call `self.pass_test(...)` on a clean match or to surface its own
        details on a miss.
        """
        gnb.attach_ue(ue)
        ue.register(**register_kwargs)
        deadline = time.time() + timeout
        while time.time() < deadline:
            if ue.last_reject_cause is not None:
                cause = ue.last_reject_cause
                if expected_cause is not None and cause != expected_cause:
                    self.fail_test(
                        f"Wrong reject cause: got {cause}, expected {expected_cause}",
                        imsi=ue.imsi, got_cause=cause, expected_cause=expected_cause,
                    )
                return (True, cause)
            if ue.state == "REGISTERED":
                self.fail_test(
                    "Expected registration reject, but UE reached REGISTERED",
                    imsi=ue.imsi, state=ue.state,
                )
                return (False, None)
            time.sleep(0.2)
        self.fail_test(
            f"Timed out waiting for registration reject (state={ue.state})",
            imsi=ue.imsi, state=ue.state,
        )
        return (False, None)


class StopTest(Exception):
    """Raised by require_* helpers to abort test on precondition failure."""
    pass

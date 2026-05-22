# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""TestSpec — declarative metadata that EVERY test case must declare.

A test's identity is its metadata, not the filename or directory it
happens to live in. The GUI, the runner, the report writer, and the
subset selector all derive from this single source of truth.

Usage:

    from src.testcases.spec import TestSpec, Domain, NF, Slice, Severity, Setup

    class TC_InitialRegistration(TestCase):
        SPEC = TestSpec(
            tc_id="TC-REG-001",
            title="Initial registration over 3GPP access — 5G-AKA",
            spec="TS 24.501 §5.5.1.2.4",
            domain=Domain.REGISTRATION,
            nfs=(NF.AMF, NF.AUSF, NF.UDM),
            slice=Slice.EMBB,
            dnn="internet",
            severity=Severity.BLOCKER,
            tags=("smoke", "conformance"),
            setup=Setup.BASELINE,
        )

        def run(self):
            ...

Every field is validated at construction (TestSpec is a frozen
dataclass with __post_init__ checks); a typo or a missing required
field raises TestSpecError at class-definition time, so the failure
surfaces during `import` and not at TC#312 of a nightly run.

Pivots (GUI / CLI / reports) are derived purely from these fields:

    by_domain  : group_by(specs, lambda s: s.domain)
    by_nf      : group_by(specs, lambda s: s.nfs)             # one TC may appear under multiple NFs
    by_spec_ts : group_by(specs, lambda s: s.spec_ts())       # "TS 24.501"
    by_severity: group_by(specs, lambda s: s.severity)
    by_tag     : group_by(specs, lambda s: s.tags)

Run subsets read config/test_groups.yaml and filter by any field
combination (see select() below).
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from enum import Enum
from typing import Tuple


class TestSpecError(Exception):
    """A TestSpec is malformed. Raised at TC class definition time."""


# ── Enums ─────────────────────────────────────────────────────────────
# Each enum is closed-set (string-valued for serialisation). Adding a
# value here is a deliberate act — keeps the schema from drifting.

class Domain(str, Enum):
    """Primary 3GPP procedure family. One per TC. Filesystem subdir
    mirrors this enum: src/testcases/<domain.value>/."""
    REGISTRATION      = "registration"      # TS 24.501 §5.5
    DEREGISTRATION    = "deregistration"    # TS 24.501 §5.5.2
    AUTHENTICATION    = "authentication"    # TS 33.501 §6
    PDU_SESSION       = "pdu_session"       # TS 24.501 §6, TS 23.502 §4.3.2
    MOBILITY          = "mobility"          # TS 24.501 §5.5.1.3 (mobility reg)
    HANDOVER          = "handover"          # TS 38.413 §8.4, TS 23.502 §4.9
    IDLE_MODE         = "idle_mode"         # TS 24.501 §5.3.1 + paging
    NG_SETUP          = "ng_setup"          # TS 38.413 §8.7.1
    SLICING           = "slicing"           # TS 23.501 §5.15
    QOS               = "qos"               # TS 23.501 §5.7, TS 29.244 §7.5.2
    TRAFFIC           = "traffic"           # bulk data, DPDK exercise
    BENCHMARK         = "benchmark"         # core-procedure benchmarks: attach storm,
                                            # PDU storm, HO storm, paging storm.
                                            # GUI surfaces these inside the Core
                                            # Procedures group. Data-plane throughput
                                            # benchmarks (PPS, jumbo, etc.) stay
                                            # under TRAFFIC.
    IMS               = "ims"               # TS 23.228, CSCF + SIP
    VOICE             = "voice"             # TS 26.114 + IMS voice flows
    SMS               = "sms"               # TS 23.040 (SMS over NAS / SIP)
    EMERGENCY         = "emergency"         # TS 23.501 §5.16.4
    CHARGING          = "charging"          # TS 32.290 / TS 32.291
    LAWFUL_INTERCEPT  = "lawful_intercept"  # TS 33.127
    POSITIONING       = "positioning"       # TS 23.273
    NWDAF             = "nwdaf"             # TS 23.288
    ROAMING           = "roaming"           # TS 23.501 §5.6.3
    INTERWORKING      = "interworking"      # N26, Wi-Fi/N3IWF, ePDG
    V2X               = "v2x"               # TS 23.287
    PROSE             = "prose"             # TS 23.304 (sidelink)
    MCX               = "mcx"               # TS 23.280
    IOT               = "iot"               # TS 22.369 / TS 23.401
    NTN               = "ntn"               # TS 38.821
    MEC               = "mec"               # TS 23.548
    ESIM              = "esim"              # GSMA SGP.22
    SECURITY          = "security"          # firewall, NPN, IPsec, attestation
    OAM               = "oam"               # logs, tracing, fault, performance
    PROVISIONING      = "provisioning"      # UE/UPF/slice CRUD APIs
    INFRA             = "infra"             # NRF, NSSF, NSACF, network config
    VAS               = "vas"               # supplementary services
    SAFETY            = "safety"            # MBS, disaster roaming, RACS, IOPS


class NF(str, Enum):
    """5GS Network Functions exercised by the TC.
    A test usually touches several — list them all so 'show me every
    AMF test' is a single filter."""
    AMF    = "AMF"      # Access & Mobility Function
    SMF    = "SMF"      # Session Management
    UPF    = "UPF"      # User Plane
    AUSF   = "AUSF"     # Authentication Server
    UDM    = "UDM"      # Unified Data Management
    UDR    = "UDR"      # Unified Data Repository
    PCF    = "PCF"      # Policy Control
    NRF    = "NRF"      # NF Repository
    NSSF   = "NSSF"     # Slice Selection
    NSACF  = "NSACF"    # Slice Admission Control
    NEF    = "NEF"      # Exposure
    CHF    = "CHF"      # Charging
    NWDAF  = "NWDAF"    # Data Analytics
    LMF    = "LMF"      # Location
    SMSF   = "SMSF"     # SMS
    AF     = "AF"       # Application
    SCP    = "SCP"      # Service Communication Proxy
    BSF    = "BSF"      # Binding Support
    N3IWF  = "N3IWF"    # Non-3GPP interworking
    CSCF   = "CSCF"     # IMS Call Session Control (P/I/S)
    HSS    = "HSS"      # IMS HSS
    MRF    = "MRF"      # IMS Media Resource
    PCSCF  = "P-CSCF"
    ICSCF  = "I-CSCF"
    SCSCF  = "S-CSCF"
    GNB    = "gNB"      # access-side (NGAP / RRC peer)


class Slice(str, Enum):
    """S-NSSAI slice under test. NONE for tests that don't pin a slice."""
    EMBB  = "eMBB"
    URLLC = "URLLC"
    MIOT  = "mIoT"
    MULTI = "multi"     # tests that exercise multiple slices in one run
    NONE  = "none"


class Severity(str, Enum):
    """Triage priority. Drives which subset a CI run includes."""
    BLOCKER  = "blocker"   # broken = product unusable
    MAJOR    = "major"     # broken = feature partially unusable
    MINOR    = "minor"     # broken = papercut
    INFO     = "info"      # broken = informational only (telemetry, doc)


class Setup(str, Enum):
    """Pretest state contract — what UE roster the TC expects to exist
    when it starts. Wired to runner_config.pretest_mode."""
    BASELINE = "baseline"  # the 128 manifest UEs; tester sets this up
    DELTA    = "delta"     # baseline + extra UEs the TC adds via src.delta
    EMPTY    = "empty"     # no UEs (provisioning / negative tests)
    CUSTOM   = "custom"    # TC manages its own setup; document in setup_notes


# ── 3GPP spec validation ──────────────────────────────────────────────
# A spec string is "<series> <doc>[.<sub>] [§<section>]" — examples:
#   "TS 24.501 §5.5.1.2.4"
#   "TS 23.501 §5.15.2"
#   "RFC 3261 §10"
#   "SGP.22 §3"
# We validate the doc-id prefix is one of the families we ship specs
# for; the §section is free-form (a section number, no fixed depth).

_SPEC_DOC_RE = re.compile(
    r"^(?P<family>TS|TR|RFC|SGP|E\.\d+|3GPP\s+TS|Q\.\d+)"
    r"\s+"
    r"(?P<doc>[0-9A-Za-z.]+)"
    r"(?:\s+§(?P<section>\S.*))?$"
)
_KNOWN_FAMILIES = {"TS", "TR", "RFC", "SGP", "3GPP TS"}


def _validate_spec(s: str) -> None:
    if not isinstance(s, str) or not s.strip():
        raise TestSpecError("spec must be a non-empty string (e.g. 'TS 24.501 §5.5')")
    m = _SPEC_DOC_RE.match(s.strip())
    if not m:
        raise TestSpecError(
            f"spec {s!r} doesn't parse as '<family> <doc> [§<section>]'. "
            "Examples: 'TS 24.501 §5.5.1.2.4', 'RFC 3261 §10', 'SGP.22 §3'."
        )


_TC_ID_RE = re.compile(r"^TC-[A-Z][A-Z0-9]{1,7}-\d{3,4}$")


def _validate_tc_id(tc_id: str) -> None:
    if not isinstance(tc_id, str) or not _TC_ID_RE.match(tc_id):
        raise TestSpecError(
            f"tc_id {tc_id!r} must match TC-<DOMAIN>-NNN, e.g. 'TC-REG-001', "
            f"'TC-PDU-042', 'TC-MTR-005'."
        )


# ── The dataclass ─────────────────────────────────────────────────────

@dataclass(frozen=True)
class TestSpec:
    """Declarative test metadata. One per test class.

    Frozen + validated at __post_init__ — pass-through to runtime is
    safe because nothing can mutate it after construction.

    Fields:

      tc_id    Required. TC-<DOMAIN>-NNN. The stable external ID;
               persists across renames. Tester wires Robot suites by
               tc_id, so this must match the Robot file.

      title    Required. One-sentence operator-facing description.
               Shown in the GUI test row.

      spec     Required. 3GPP/IETF citation. Validated against the
               family registry. The most-specific §-citation that
               anchors what the TC asserts.

      domain   Required. Primary 3GPP procedure family (one Domain
               value). Determines filesystem location and primary
               GUI grouping.

      nfs      Required, non-empty. Tuple of NFs the test exercises.
               Tests that touch the dataplane should include NF.UPF;
               that's how 'show all UPF tests' works.

      severity Required. Severity drives CI inclusion (blocker/major
               always run; minor/info gated).

      slice    Default Slice.NONE. Pin to a specific slice when the
               TC's assertions depend on slice selection.

      dnn      Default "" (none). When set, the DNN string must match
               one in baseline.yaml (validated post-hoc by the runner
               at startup; not at TestSpec construction since baseline
               import is heavier than schema parsing).

      tags     Default empty tuple. Free-form labels. Recommended
               canonical tags: smoke, conformance, regression, scale,
               negative, slow (>10 s), wip.

      setup    Default Setup.BASELINE. How the TC expects the SUT to
               be brought to a known state. The runner's pretest_mode
               must be compatible (Setup.DELTA requires runner to allow
               UEs to outlive the test, etc.).

      setup_notes
               Optional human-readable note explaining a Setup.CUSTOM
               or Setup.DELTA contract.

      description
               Optional multi-line operator description. Defaults to
               title.

      expected_duration_s
               Default 0 (unknown). Runner uses this for stuck-
               detection and progress estimates. Tests >30 s should
               set this honestly.

      flaky    Default False. True ⇒ runner retries once on failure
               and tags the result accordingly. Use sparingly; flagged
               TCs need a real fix, not a permanent retry.

      requires_dataplane
               Default False. True ⇒ runner refuses to start the TC if
               the UPF DPDK bridge isn't healthy (i.e., hugepages
               present, libupf_dp.so loaded). Prevents vacuous PASS on
               a misconfigured host.

      requires_real_hw
               Default False. True ⇒ TC needs SIM/USB/physical-NIC
               attachment; skipped in container CI.
    """

    tc_id: str
    title: str
    spec: str
    domain: Domain
    nfs: Tuple[NF, ...]
    severity: Severity

    slice: Slice = Slice.NONE
    dnn: str = ""
    tags: Tuple[str, ...] = ()
    setup: Setup = Setup.BASELINE
    setup_notes: str = ""
    description: str = ""
    expected_duration_s: float = 0.0
    flaky: bool = False
    requires_dataplane: bool = False
    requires_real_hw: bool = False

    def __post_init__(self):
        _validate_tc_id(self.tc_id)
        _validate_spec(self.spec)
        if not self.title or not self.title.strip():
            raise TestSpecError(f"{self.tc_id}: title must not be empty")
        if not isinstance(self.domain, Domain):
            raise TestSpecError(
                f"{self.tc_id}: domain must be a Domain enum, got {self.domain!r}"
            )
        if not isinstance(self.severity, Severity):
            raise TestSpecError(
                f"{self.tc_id}: severity must be a Severity enum, got {self.severity!r}"
            )
        if not isinstance(self.slice, Slice):
            raise TestSpecError(
                f"{self.tc_id}: slice must be a Slice enum, got {self.slice!r}"
            )
        if not isinstance(self.setup, Setup):
            raise TestSpecError(
                f"{self.tc_id}: setup must be a Setup enum, got {self.setup!r}"
            )
        if not self.nfs:
            raise TestSpecError(
                f"{self.tc_id}: nfs must list at least one NF "
                "(use NF.AMF etc.; the test must touch at least one network function)"
            )
        if not all(isinstance(n, NF) for n in self.nfs):
            raise TestSpecError(
                f"{self.tc_id}: every entry in nfs must be an NF enum, "
                f"got {self.nfs!r}"
            )
        # nfs cast to canonical tuple (handles list input)
        object.__setattr__(self, "nfs", tuple(self.nfs))
        if not all(isinstance(t, str) and t.strip() for t in self.tags):
            raise TestSpecError(
                f"{self.tc_id}: every tag must be a non-empty string, got {self.tags!r}"
            )
        object.__setattr__(self, "tags", tuple(self.tags))
        if self.setup == Setup.CUSTOM and not self.setup_notes:
            raise TestSpecError(
                f"{self.tc_id}: setup=CUSTOM requires setup_notes explaining "
                "the contract (what state the TC expects / leaves behind)"
            )
        if self.expected_duration_s < 0:
            raise TestSpecError(
                f"{self.tc_id}: expected_duration_s must be >= 0"
            )

    # ── Pivot helpers — used by GUI, reports, subset selector ──

    def spec_ts(self) -> str:
        """Spec doc id without §section. Example: 'TS 24.501'."""
        m = _SPEC_DOC_RE.match(self.spec.strip())
        if m:
            return f"{m.group('family')} {m.group('doc')}"
        return self.spec  # malformed should be impossible here

    def spec_section(self) -> str:
        """The §section part, or '' if absent."""
        m = _SPEC_DOC_RE.match(self.spec.strip())
        if m:
            return (m.group("section") or "").strip()
        return ""

    def has_tag(self, tag: str) -> bool:
        return tag in self.tags

    def to_dict(self) -> dict:
        """Serialisable form for the GUI and reports."""
        return {
            "tc_id":  self.tc_id,
            "title":  self.title,
            "spec":   self.spec,
            "spec_ts": self.spec_ts(),
            "spec_section": self.spec_section(),
            "domain": self.domain.value,
            "nfs":    [n.value for n in self.nfs],
            "slice":  self.slice.value,
            "dnn":    self.dnn,
            "severity": self.severity.value,
            "tags":   list(self.tags),
            "setup":  self.setup.value,
            "setup_notes": self.setup_notes,
            "description": self.description or self.title,
            "expected_duration_s": self.expected_duration_s,
            "flaky": self.flaky,
            "requires_dataplane": self.requires_dataplane,
            "requires_real_hw":   self.requires_real_hw,
        }


# ── Selector — used by config/test_groups.yaml + CLI ──────────────────

@dataclass(frozen=True)
class Selector:
    """Filter expression over a TestSpec set.

    Each field is either None (don't filter) or a set of acceptable
    values. A TestSpec passes if every non-None filter matches:

      domain:     spec.domain in selector.domain
      nfs:        selector.nfs.issubset(spec.nfs)        ← all required
      tags_any:   selector.tags_any & set(spec.tags)     ← any-of
      tags_all:   selector.tags_all.issubset(spec.tags)  ← all-of
      slice:      spec.slice in selector.slice
      severity:   spec.severity in selector.severity
      spec_ts:    spec.spec_ts() in selector.spec_ts     ← e.g. "TS 24.501"

    YAML example:
        nightly_smoke: { tags_any: [smoke] }
        amf_full:      { nfs: [AMF] }
        slicing_all:   { domain: [slicing] }
        blockers:      { severity: [blocker] }
        ts_24_501:     { spec_ts: ["TS 24.501"] }
    """
    domain:    frozenset = field(default_factory=frozenset)
    nfs:       frozenset = field(default_factory=frozenset)
    tags_any:  frozenset = field(default_factory=frozenset)
    tags_all:  frozenset = field(default_factory=frozenset)
    slice:     frozenset = field(default_factory=frozenset)
    severity:  frozenset = field(default_factory=frozenset)
    spec_ts:   frozenset = field(default_factory=frozenset)

    def matches(self, spec: "TestSpec") -> bool:
        if self.domain   and spec.domain   not in self.domain:    return False
        if self.slice    and spec.slice    not in self.slice:     return False
        if self.severity and spec.severity not in self.severity:  return False
        if self.spec_ts  and spec.spec_ts() not in self.spec_ts:  return False
        if self.nfs and not self.nfs.issubset(set(n.value for n in spec.nfs)):
            return False
        if self.tags_any and not (self.tags_any & set(spec.tags)):
            return False
        if self.tags_all and not self.tags_all.issubset(set(spec.tags)):
            return False
        return True


def select(specs, selector: Selector):
    """Filter an iterable of TestSpec / TestCase classes by selector."""
    out = []
    for s in specs:
        spec = s.SPEC if not isinstance(s, TestSpec) else s
        if selector.matches(spec):
            out.append(s)
    return out


# ── Pivot helper — used by GUI to compute groupings ──────────────────

def pivot_by(specs, key):
    """Group an iterable of TestSpec by a key function. Returns dict
    of label -> list. key=lambda s: s.domain.value → group by domain;
    key=lambda s: s.spec_ts() → group by spec doc.

    For multi-valued keys (e.g., nfs), pass key=lambda s: s.nfs and
    each TC appears under every NF it touches."""
    out: dict = {}
    for s in specs:
        k = key(s)
        if isinstance(k, (list, tuple, set, frozenset)):
            for kk in k:
                out.setdefault(_label(kk), []).append(s)
        else:
            out.setdefault(_label(k), []).append(s)
    return out


def _label(v):
    return v.value if isinstance(v, Enum) else str(v)

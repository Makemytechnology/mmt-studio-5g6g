# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# UPF stats — DISABLED.
#
# Architecture: the tester owns the traffic plane on both ends (tester +
# DN-side agent). sa_core is deliberately out of the traffic/observability
# loop during test execution. Historically these helpers hit sa_core's
# /api/upf/* endpoints before and after every test — that coupling is
# now explicitly forbidden, so the functions are stubbed out.
#
# Everything continues to import these names (test_runner.py + ~10
# testcases + the /api/core/upf-stats UI route), so we keep the module
# and function signatures intact. They just return empty dicts — the
# existing `if upf_before and upf_after:` guards in every caller skip
# the logging/details path with zero side-effects.
#
# If you truly need ad-hoc UPF visibility, run it from the UI tab /
# /api/core/upf-stats — but not as part of a test run.

_DISABLED_REASON = (
    "UPF stats disabled: tester-owned traffic plane does not call sa_core. "
    "Use /api/core/upf-stats manually if you need a one-off snapshot."
)


def collect_upf_stats() -> dict:
    """No-op — returns {} so callers' `if upf_before and upf_after:` paths skip."""
    return {}


def compute_upf_delta(before: dict, after: dict) -> dict:
    """No-op — returns {}; every caller guards with truthy checks."""
    return {}

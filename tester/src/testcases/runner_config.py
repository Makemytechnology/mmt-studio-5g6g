# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Tester runner configuration — pretest_mode and other suite-wide knobs.

Loaded from config/runner.json with env-var override (TESTER_PRETEST_MODE).
Kept deliberately small: anything that varies per-test belongs in the
test's own params, not here.
"""

import json
import os
import logging

log = logging.getLogger("tester.runner.config")

PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
RUNNER_CONFIG_PATH = os.path.join(PROJECT_ROOT, "config", "runner.json")

# pretest_mode — controls how the runner brings the SUT to a known
# state before each test. Order-independence (any test, any subset,
# any order yields the same result) is preserved by 'full' and
# 'baseline'; 'delta' trades that guarantee for wall-clock speed.
PRETEST_MODES = ("full", "baseline", "delta")

DEFAULT_CONFIG = {
    # 'full'     — operator-driven cold start: tester assumes the
    #              caller already ran `run_studio.sh up --build` (or
    #              equivalent) so docker images are fresh. Inside the
    #              test loop this behaves like 'baseline' (reset before
    #              each test); the distinction is procedural.
    # 'baseline' — default. POST /api/admin/remove-db-file before
    #              every test. ~5-10 s per test; sa_core exits + docker
    #              restart-policy brings it back, then SeedAll re-seeds
    #              from db/seed/baseline.yaml.
    # 'delta'    — skip the per-test reset. Each test applies only its
    #              own pre-config via API and is expected to clean up
    #              after itself. Fastest; order-sensitive — a test that
    #              forgets to clean up poisons every later test.
    "pretest_mode": "baseline",
}


def load() -> dict:
    """Load runner config. Env var TESTER_PRETEST_MODE wins over JSON."""
    cfg = dict(DEFAULT_CONFIG)
    if os.path.exists(RUNNER_CONFIG_PATH):
        try:
            with open(RUNNER_CONFIG_PATH, "r", encoding="utf-8") as f:
                disk = json.load(f)
            cfg.update({k: v for k, v in disk.items() if not k.startswith("_")})
        except Exception as e:
            log.warning("Failed to load runner config %s: %s — using defaults",
                        RUNNER_CONFIG_PATH, e)

    env = os.environ.get("TESTER_PRETEST_MODE")
    if env:
        cfg["pretest_mode"] = env

    mode = cfg.get("pretest_mode", "baseline")
    if mode not in PRETEST_MODES:
        log.warning("Invalid pretest_mode=%r; falling back to 'baseline'", mode)
        cfg["pretest_mode"] = "baseline"
    return cfg


def pretest_mode() -> str:
    return load()["pretest_mode"]


def save(cfg: dict) -> dict:
    """Persist runner config to config/runner.json. Validates pretest_mode
    before writing; raises ValueError on invalid input."""
    mode = cfg.get("pretest_mode", "baseline")
    if mode not in PRETEST_MODES:
        raise ValueError(f"pretest_mode must be one of {PRETEST_MODES}; got {mode!r}")
    merged = dict(DEFAULT_CONFIG)
    if os.path.exists(RUNNER_CONFIG_PATH):
        try:
            with open(RUNNER_CONFIG_PATH, "r", encoding="utf-8") as f:
                merged.update({k: v for k, v in json.load(f).items() if not k.startswith("_")})
        except Exception:
            pass
    merged.update({k: v for k, v in cfg.items() if not k.startswith("_")})

    os.makedirs(os.path.dirname(RUNNER_CONFIG_PATH), exist_ok=True)
    # Preserve the helpful inline comment header on every write.
    payload = {
        "_comment_pretest_mode": (
            "How the runner brings the SUT to a known state before each test. "
            "'full' = operator just ran run_studio.sh up --build; tester behaves "
            "like 'baseline' in the loop. 'baseline' (default) = POST "
            "/api/admin/remove-db-file before every test; order-independent, "
            "~5-10 s per test. 'delta' = skip the reset; each test applies only "
            "its own pre-config and must clean up after itself; fastest but "
            "order-sensitive."
        ),
        **merged,
    }
    with open(RUNNER_CONFIG_PATH, "w", encoding="utf-8") as f:
        json.dump(payload, f, indent=2)
    log.info("Runner config saved: pretest_mode=%s", merged["pretest_mode"])
    return merged

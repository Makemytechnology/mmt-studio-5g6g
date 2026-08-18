# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/routes/common.py — Shared utilities for route routers
#
# Mirrors sa_core's webservice/routes/common.py pattern.

import logging

from src.db import crud
from src.db import runs
from src.db import reports as report_gen
from src.db import analysis

log = logging.getLogger("tester.routes")


def _mask_imsi(s: str) -> str:
    """Mask middle of IMSI for logging."""
    if not s or len(s) < 8:
        return s or ""
    return s[:4] + "***" + s[-3:]

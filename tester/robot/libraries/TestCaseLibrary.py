# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Robot Framework keyword library — bridge to Python TestCase registry.

Exposes a single keyword `Run Python TestCase` that resolves a TestCase
by tc_id from src.testcases.base.TestCase.REGISTRY (populated via the
SPEC dataclass on every concrete subclass), runs it against the gNB and
UE state machines already created by GnbLibrary / UeLibrary in the
current Robot session, and surfaces the TestResult as a Robot pass /
fail.

The Python TestCase owns the full test logic — registration, PDU
session, iperf3 invocation, UPF stats collection, and pass/fail
assertions. The Robot suite owns the spec-cited documentation block and
chooses when to call this bridge. One source of truth for what each
TC-XYZ-N actually does; one place (Robot) to read the spec citations.
"""

import os
import sys

# Same path bootstrap as GnbLibrary / UeLibrary so `from src.* import …`
# works regardless of where Robot was invoked from.
PROJECT_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
for p in (PROJECT_ROOT, os.path.join(PROJECT_ROOT, "libs")):
    if p not in sys.path:
        sys.path.insert(0, p)

from robot.api.deco import keyword, library
from robot.api import logger
from robot.libraries.BuiltIn import BuiltIn

# Force registry population. Importing the registry module triggers
# discover_all() side-effects via TestCase.__init_subclass__, which fills
# TestCase.REGISTRY for every tc_*.py module that gets imported. We then
# call discover_all() explicitly so the registry is populated before the
# first Run Python TestCase call.
from src.testcases.base import TestCase
from src.testcases.registry import discover_all


@library(scope='GLOBAL', version='1.0')
class TestCaseLibrary:
    ROBOT_LIBRARY_SCOPE = 'GLOBAL'

    def __init__(self):
        # Populate TestCase.REGISTRY by importing every tc_*.py. Cheap
        # one-shot; subsequent imports are no-ops.
        try:
            discover_all()
        except Exception as e:
            logger.warn(f"discover_all() failed during TestCaseLibrary init: {e}")

    @keyword("Run Python TestCase")
    def run_python_testcase(self, tc_id, **params):
        """Run the Python TestCase registered under `tc_id`.

        Pulls the current Robot session's gNB and UE state machines from
        the live GnbLibrary / UeLibrary instances and passes them as the
        Python TestCase's gnb_pool / ue_pool. The Python `run()` method
        does everything (register UE, PDU session, traffic, assertions);
        this bridge surfaces the result as a Robot pass or fail.

        Any kwargs are passed through as the TestCase's `params` dict —
        useful for overriding duration, bandwidth, server, etc. without
        editing the Python source.

        Raises AssertionError (-> Robot FAIL) if the TestCase is missing,
        the run crashes, or `result.status != PASS`.
        """
        cls = TestCase.REGISTRY.get(tc_id)
        if cls is None:
            known = ", ".join(sorted(TestCase.REGISTRY)[:10]) + " …"
            raise AssertionError(
                f"No Python TestCase registered for tc_id={tc_id!r}. "
                f"Implement a TestCase with SPEC.tc_id={tc_id!r} under "
                f"src/testcases/. Sample registered ids: {known}"
            )

        gnb_pool, ue_pool = self._collect_pools()
        logger.info(
            f"Run Python TestCase {tc_id}: "
            f"{len(gnb_pool)} gNB(s), {len(ue_pool)} UE(s), params={params}"
        )

        tc = cls(gnb_pool, ue_pool, params or None)
        try:
            tc.run()
        except Exception as e:
            raise AssertionError(f"{tc_id} crashed during run(): {e}") from e

        result = tc.result
        details = getattr(result, "details", {}) or {}
        status = getattr(result, "status", "ERROR")
        err = getattr(result, "error", None)

        # Surface details into the Robot log for transparency — these
        # show up in robot's output.xml and the GUI's per-test view.
        for k, v in details.items():
            logger.info(f"  {tc_id}.{k} = {v}")

        if status == "PASS":
            logger.info(f"{tc_id} PASS")
            return details
        raise AssertionError(
            f"{tc_id} {status}"
            + (f": {err}" if err else "")
            + (f" — details: {details}" if details else "")
        )

    # ─── Pool collection ─────────────────────────────────────────────
    def _collect_pools(self):
        """Snapshot the GnbLibrary / UeLibrary state-machine maps into
        flat lists matching the Python TestCase's gnb_pool / ue_pool
        contract. Empty lists are valid — a TestCase that needs them
        will call self.require_gnb() / self.require_ue() and fail
        cleanly with a descriptive error.
        """
        bi = BuiltIn()
        gnb_pool = []
        ue_pool = []
        try:
            gnb_lib = bi.get_library_instance("GnbLibrary")
            gnb_pool = list(getattr(gnb_lib, "_gnbs", {}).values())
        except Exception as e:
            logger.warn(f"GnbLibrary not loaded (gnb_pool will be empty): {e}")
        try:
            ue_lib = bi.get_library_instance("UeLibrary")
            ue_pool = list(getattr(ue_lib, "_ues", {}).values())
        except Exception as e:
            logger.warn(f"UeLibrary not loaded (ue_pool will be empty): {e}")
        return gnb_pool, ue_pool

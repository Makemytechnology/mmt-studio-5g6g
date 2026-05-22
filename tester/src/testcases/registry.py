# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Test case auto-discovery registry.

Scans testcases/ subdirectories for tc_*.py modules, imports them,
and collects all TestCase subclasses for registration with TestRunner.
"""

import importlib
import os
import pkgutil
import logging

from src.testcases.base import TestCase

log = logging.getLogger("tester.registry")


def discover_all():
    """Auto-discover all TestCase subclasses from testcases/ subdirs.

    Scans all packages under src/testcases/ (core/, vas/, ims/, edge/,
    safety/, infra/, vertical/, traffic/) and any tc_*.py at the top level.
    Returns a list of TestCase subclasses.
    """
    testcases_dir = os.path.dirname(os.path.abspath(__file__))
    all_tcs = []

    # Walk all subdirectories and top-level tc_*.py files
    for dirpath, dirnames, filenames in os.walk(testcases_dir):
        # Skip __pycache__
        dirnames[:] = [d for d in dirnames if d != "__pycache__"]

        for fname in sorted(filenames):
            if not fname.startswith("tc_") or not fname.endswith(".py"):
                continue

            # Build dotted module path
            rel = os.path.relpath(os.path.join(dirpath, fname),
                                  os.path.dirname(testcases_dir))
            module_path = "src." + rel[:-3].replace(os.sep, ".")

            try:
                mod = importlib.import_module(module_path)
            except Exception as e:
                log.warning("Failed to import %s: %s", module_path, e)
                continue

            # Collect ALL_*_TCS lists first (preferred)
            found_list = False
            for attr_name, attr_val in vars(mod).items():
                if attr_name.startswith("ALL_") and attr_name.endswith("_TCS"):
                    if isinstance(attr_val, (list, tuple)):
                        all_tcs.extend(attr_val)
                        found_list = True

            # Fallback: collect individual TestCase subclasses
            if not found_list:
                for attr_name in dir(mod):
                    obj = getattr(mod, attr_name)
                    if (isinstance(obj, type) and issubclass(obj, TestCase)
                            and obj is not TestCase and hasattr(obj, 'name')
                            and obj.name):
                        all_tcs.append(obj)

    # Deduplicate by class identity
    seen = set()
    unique = []
    for tc in all_tcs:
        if id(tc) not in seen:
            seen.add(id(tc))
            unique.append(tc)

    log.info("Auto-discovered %d test cases from %s", len(unique), testcases_dir)
    return unique

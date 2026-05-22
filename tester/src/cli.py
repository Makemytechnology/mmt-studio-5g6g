# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""SA Tester CLI — run tests from command line for CI/CD integration.

Usage:
  python3 -m src.cli run --test auth_success
  python3 -m src.cli run --suite 08_ims
  python3 -m src.cli regression --report html
  python3 -m src.cli status --run latest
  python3 -m src.cli reports --list
"""

import argparse
import sys
import time
import json
import logging

logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(name)s: %(message)s")
log = logging.getLogger("tester.cli")


def cmd_run(args):
    """Run tests (single, suite, or all)."""
    from src.db.schema import ensure_schema
    from src.db.runs import create_run, complete_run, save_result, save_metrics
    from src.testcases.test_runner import TestRunner
    from src.testcases.robot_parser import parse_all_suites
    import src.config as cfg

    ensure_schema()
    runner = TestRunner()

    # Determine which tests to run
    if args.test:
        test_names = [args.test]
        run_type = "single"
    elif args.suite:
        # Parse robot suite to get test names
        suites = parse_all_suites(os.path.join(cfg.PROJECT_ROOT, "robot", "suites"))
        test_names = [tc["tc_id"] for tc in suites
                      if tc.get("suite") == args.suite or args.suite in tc.get("suite", "")]
        if not test_names:
            # Try by test name pattern
            test_names = [name for name in runner._registry if args.suite in name]
        run_type = "suite"
    else:
        test_names = list(runner._registry.keys())
        run_type = "regression"

    if not test_names:
        print(f"No tests found for: {args.test or args.suite or 'all'}")
        sys.exit(1)

    print(f"Running {len(test_names)} tests ({run_type})...")

    run_id = create_run(run_type=run_type, trigger="cli", suites=[args.suite] if args.suite else None)
    gnb_pool = []
    ue_pool = []
    passed = 0
    failed = 0

    for name in test_names:
        result = runner.run_test(name, gnb_pool, ue_pool)
        if result:
            status = result.status
            rid = save_result(
                run_id=run_id, test_name=name, tc_id=getattr(result, 'tc_id', ''),
                suite=args.suite or '', category='', status=status,
                duration_ms=result.duration_ms, error=str(result.error) if result.error else None,
                details=result.details,
            )
            if status == "PASS":
                passed += 1
            else:
                failed += 1
            print(f"  {status:5s} {name} ({round(result.duration_ms/1000, 1)}s)")

    complete_run(run_id)
    print(f"\nRun {run_id}: {passed} passed, {failed} failed ({len(test_names)} total)")

    # Generate reports
    if args.report:
        from src.db.reports import generate_html_report, generate_json_report, generate_junit_xml
        if "html" in args.report:
            path = generate_html_report(run_id)
            print(f"HTML report: {path}")
        if "json" in args.report:
            path = generate_json_report(run_id)
            print(f"JSON report: {path}")
        if "junit" in args.report or "xml" in args.report:
            path = generate_junit_xml(run_id)
            print(f"JUnit XML: {path}")

    # Exit code
    if args.exit_code:
        sys.exit(0 if failed == 0 else 1)


def cmd_status(args):
    """Show status of a run."""
    from src.db.schema import ensure_schema
    from src.db.runs import get_run, list_runs

    ensure_schema()
    if args.run == "latest":
        runs = list_runs(limit=1)
        if not runs:
            print("No runs found")
            return
        run = get_run(runs[0]["id"])
    else:
        run = get_run(args.run)

    if not run:
        print(f"Run not found: {args.run}")
        return

    print(f"Run: {run['id']} ({run['run_type']}, {run['status']})")
    print(f"  Total: {run['total']}  Pass: {run['passed']}  Fail: {run['failed']}  Error: {run['error']}")
    if run.get("duration_ms"):
        print(f"  Duration: {round(run['duration_ms']/1000)}s")


def cmd_reports(args):
    """List or generate reports."""
    from src.db.schema import ensure_schema
    from src.db.reports import list_reports, generate_html_report, generate_json_report, generate_junit_xml

    ensure_schema()
    if args.list:
        reports = list_reports()
        if not reports:
            print("No reports generated yet")
            return
        for r in reports:
            print(f"  {r['filename']:40s} {r['format']:5s} {r['size']:>8d}B  {r['created']}")
    elif args.generate and args.run:
        fmt = args.generate
        if fmt == "html":
            print(generate_html_report(args.run))
        elif fmt == "json":
            print(generate_json_report(args.run))
        elif fmt in ("junit", "xml"):
            print(generate_junit_xml(args.run))


def cmd_analysis(args):
    """Run analysis queries."""
    from src.db.schema import ensure_schema
    from src.db import analysis

    ensure_schema()
    if args.query == "pass-rate":
        for r in analysis.pass_rate(args.last_n):
            print(f"  {r['run_id']:10s} {r['pass_rate']:5.1f}%  ({r['passed']}/{r['total']})")
    elif args.query == "flaky":
        for r in analysis.flaky_tests(args.last_n):
            print(f"  {r['test_name']:40s} changes={r['status_changes']}  {r['recent_statuses']}")
    elif args.query == "failures":
        for r in analysis.failure_heatmap(args.last_n):
            print(f"  {r['test_name']:40s} fails={r['fail_count']}/{r['total']}  ({r['fail_rate']}%)")
    elif args.query == "suites":
        for r in analysis.suite_summary():
            print(f"  {r['suite']:30s} {r['passed']}/{r['total']}  {r['pass_rate']}%  avg={r['avg_ms']}ms")


def main():
    import os
    parser = argparse.ArgumentParser(description="SA Tester CLI")
    sub = parser.add_subparsers(dest="command")

    # run
    p_run = sub.add_parser("run", help="Run tests")
    p_run.add_argument("--test", help="Single test name")
    p_run.add_argument("--suite", help="Robot suite name")
    p_run.add_argument("--report", nargs="*", default=[], help="Generate reports: html json junit")
    p_run.add_argument("--exit-code", action="store_true", help="Exit 1 if any test fails")

    # status
    p_status = sub.add_parser("status", help="Show run status")
    p_status.add_argument("--run", default="latest", help="Run ID or 'latest'")

    # reports
    p_reports = sub.add_parser("reports", help="Manage reports")
    p_reports.add_argument("--list", action="store_true", help="List generated reports")
    p_reports.add_argument("--generate", help="Generate report: html/json/junit")
    p_reports.add_argument("--run", help="Run ID for report generation")

    # analysis
    p_analysis = sub.add_parser("analysis", help="Run analysis")
    p_analysis.add_argument("query", choices=["pass-rate", "flaky", "failures", "suites"])
    p_analysis.add_argument("--last-n", type=int, default=10, help="Number of recent runs")

    args = parser.parse_args()
    if args.command == "run":
        cmd_run(args)
    elif args.command == "status":
        cmd_status(args)
    elif args.command == "reports":
        cmd_reports(args)
    elif args.command == "analysis":
        cmd_analysis(args)
    else:
        parser.print_help()


if __name__ == "__main__":
    main()

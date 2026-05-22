# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
"""Report Generator — HTML, JSON, JUnit XML test reports."""

import json
import time
import os
import logging
from typing import Dict, List, Optional

from src.db.schema import get_db, PROJECT_ROOT
from src.db.runs import get_run, get_result_detail
from src.db.analysis import suite_summary, regressions, pass_rate

log = logging.getLogger("tester.reports")

REPORTS_DIR = os.path.join(PROJECT_ROOT, "data", "reports")


def _ensure_dir():
    os.makedirs(REPORTS_DIR, exist_ok=True)


def generate_html_report(run_id: str) -> str:
    """Generate HTML test report for a run. Returns file path."""
    _ensure_dir()
    run = get_run(run_id)
    if not run:
        return None

    suites = suite_summary(run_id=run_id)
    total = run.get("total", 0) or 1
    pass_pct = round(run.get("passed", 0) / total * 100, 1)
    duration_s = round((run.get("duration_ms") or 0) / 1000)

    # Build HTML
    results_html = ""
    for r in run.get("results", []):
        status_class = {"PASS": "success", "FAIL": "danger", "ERROR": "warning"}.get(r["status"], "secondary")
        dur = round((r.get("duration_ms") or 0) / 1000, 1)
        error_html = f'<br><small class="text-muted">{r["error"][:100]}</small>' if r.get("error") else ""
        results_html += f"""
        <tr>
          <td><code>{r.get('tc_id', '')}</code></td>
          <td>{r['test_name']}{error_html}</td>
          <td>{r.get('suite', '')}</td>
          <td><span class="badge bg-{status_class}">{r['status']}</span></td>
          <td>{dur}s</td>
        </tr>"""

    suites_html = ""
    for s in suites:
        bar_width = s["pass_rate"]
        suites_html += f"""
        <tr>
          <td>{s['suite']}</td>
          <td>{s['passed']}/{s['total']}</td>
          <td>
            <div class="progress" style="height:20px;">
              <div class="progress-bar bg-success" style="width:{bar_width}%">{s['pass_rate']}%</div>
            </div>
          </td>
          <td>{s['failed']}</td>
          <td>{s['avg_ms']}ms</td>
        </tr>"""

    # Failed tests
    failed_html = ""
    failed = [r for r in run.get("results", []) if r["status"] in ("FAIL", "ERROR")]
    for r in failed:
        failed_html += f"""
        <tr class="table-danger">
          <td><code>{r.get('tc_id', '')}</code></td>
          <td>{r['test_name']}</td>
          <td>{r.get('error', '')[:200]}</td>
        </tr>"""

    started = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(run.get("started_at", 0)))

    html = f"""<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<title>Test Report — {run_id}</title>
<link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">
<style>body{{font-size:13px;}} .progress{{min-width:100px;}}</style>
</head><body class="p-4">
<div class="container-fluid">
  <h2>SA Tester — Test Report</h2>
  <div class="row mb-3">
    <div class="col-md-3"><strong>Run ID:</strong> {run_id}</div>
    <div class="col-md-3"><strong>Type:</strong> {run.get('run_type', 'single')}</div>
    <div class="col-md-3"><strong>Started:</strong> {started}</div>
    <div class="col-md-3"><strong>Duration:</strong> {duration_s}s</div>
  </div>
  <div class="row mb-4">
    <div class="col-md-2"><div class="card text-center p-2"><h3>{run.get('total', 0)}</h3><small>Total</small></div></div>
    <div class="col-md-2"><div class="card text-center p-2 border-success"><h3 class="text-success">{run.get('passed', 0)}</h3><small>Passed</small></div></div>
    <div class="col-md-2"><div class="card text-center p-2 border-danger"><h3 class="text-danger">{run.get('failed', 0)}</h3><small>Failed</small></div></div>
    <div class="col-md-2"><div class="card text-center p-2 border-warning"><h3 class="text-warning">{run.get('error', 0)}</h3><small>Error</small></div></div>
    <div class="col-md-2"><div class="card text-center p-2"><h3>{pass_pct}%</h3><small>Pass Rate</small></div></div>
  </div>

  <h4>Suite Summary</h4>
  <table class="table table-sm table-bordered mb-4">
    <thead class="table-light"><tr><th>Suite</th><th>Pass/Total</th><th>Pass Rate</th><th>Failed</th><th>Avg Duration</th></tr></thead>
    <tbody>{suites_html}</tbody>
  </table>

  {"<h4>Failed Tests</h4><table class='table table-sm table-bordered mb-4'><thead class='table-light'><tr><th>TC-ID</th><th>Test</th><th>Error</th></tr></thead><tbody>" + failed_html + "</tbody></table>" if failed_html else ""}

  <h4>All Results</h4>
  <table class="table table-sm table-bordered table-hover">
    <thead class="table-light"><tr><th>TC-ID</th><th>Test Name</th><th>Suite</th><th>Status</th><th>Duration</th></tr></thead>
    <tbody>{results_html}</tbody>
  </table>

  <hr><small class="text-muted">Generated {time.strftime('%Y-%m-%d %H:%M:%S')} — SA Tester</small>
</div></body></html>"""

    filepath = os.path.join(REPORTS_DIR, f"report_{run_id}.html")
    with open(filepath, "w", encoding="utf-8") as f:
        f.write(html)
    log.info("HTML report generated: %s", filepath)
    return filepath


def generate_json_report(run_id: str) -> str:
    """Generate JSON test report. Returns file path."""
    _ensure_dir()
    run = get_run(run_id)
    if not run:
        return None

    # Enrich results with details
    enriched = []
    for r in run.get("results", []):
        detail = get_result_detail(r["id"])
        enriched.append(detail or r)
    run["results"] = enriched
    run["suites"] = suite_summary(run_id=run_id)

    filepath = os.path.join(REPORTS_DIR, f"report_{run_id}.json")
    with open(filepath, "w", encoding="utf-8") as f:
        json.dump(run, f, indent=2, default=str)
    log.info("JSON report generated: %s", filepath)
    return filepath


def generate_junit_xml(run_id: str) -> str:
    """Generate JUnit XML report for CI/CD integration. Returns file path."""
    _ensure_dir()
    run = get_run(run_id)
    if not run:
        return None

    results = run.get("results", [])
    total = len(results)
    failures = sum(1 for r in results if r["status"] == "FAIL")
    errors = sum(1 for r in results if r["status"] == "ERROR")
    duration = round((run.get("duration_ms") or 0) / 1000, 3)

    xml_lines = [
        '<?xml version="1.0" encoding="UTF-8"?>',
        f'<testsuites tests="{total}" failures="{failures}" errors="{errors}" time="{duration}">',
        f'  <testsuite name="sa_tester" tests="{total}" failures="{failures}" errors="{errors}" time="{duration}">',
    ]

    for r in results:
        tc_time = round((r.get("duration_ms") or 0) / 1000, 3)
        classname = r.get("suite", "sa_tester") or "sa_tester"
        name = r["test_name"]

        if r["status"] == "PASS":
            xml_lines.append(f'    <testcase classname="{classname}" name="{name}" time="{tc_time}"/>')
        elif r["status"] == "FAIL":
            error_msg = (r.get("error") or "").replace("&", "&amp;").replace("<", "&lt;").replace('"', "&quot;")
            xml_lines.append(f'    <testcase classname="{classname}" name="{name}" time="{tc_time}">')
            xml_lines.append(f'      <failure message="{error_msg}"/>')
            xml_lines.append('    </testcase>')
        elif r["status"] == "ERROR":
            error_msg = (r.get("error") or "").replace("&", "&amp;").replace("<", "&lt;").replace('"', "&quot;")
            xml_lines.append(f'    <testcase classname="{classname}" name="{name}" time="{tc_time}">')
            xml_lines.append(f'      <error message="{error_msg}"/>')
            xml_lines.append('    </testcase>')
        elif r["status"] == "SKIP":
            xml_lines.append(f'    <testcase classname="{classname}" name="{name}" time="0">')
            xml_lines.append('      <skipped/>')
            xml_lines.append('    </testcase>')

    xml_lines.append('  </testsuite>')
    xml_lines.append('</testsuites>')

    filepath = os.path.join(REPORTS_DIR, f"report_{run_id}.xml")
    with open(filepath, "w", encoding="utf-8") as f:
        f.write("\n".join(xml_lines))
    log.info("JUnit XML report generated: %s", filepath)
    return filepath


def list_reports() -> List[Dict]:
    """List all generated reports."""
    _ensure_dir()
    reports = []
    for f in sorted(os.listdir(REPORTS_DIR), reverse=True):
        if f.startswith("report_"):
            path = os.path.join(REPORTS_DIR, f)
            ext = f.rsplit(".", 1)[-1]
            run_id = f.replace("report_", "").rsplit(".", 1)[0]
            reports.append({
                "filename": f, "format": ext, "run_id": run_id,
                "size": os.path.getsize(path),
                "created": time.strftime("%Y-%m-%d %H:%M:%S", time.localtime(os.path.getmtime(path))),
            })
    return reports

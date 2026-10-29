# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# Test execution tables — runs, results, metrics, schedules

RUNS_DDL = [
    """
    CREATE TABLE IF NOT EXISTS test_runs (
      id             TEXT PRIMARY KEY,
      run_type       TEXT NOT NULL DEFAULT 'single'
                     CHECK (run_type IN ('single','suite','campaign','regression')),
      status         TEXT NOT NULL DEFAULT 'running'
                     CHECK (status IN ('running','completed','aborted','failed')),
      trigger        TEXT DEFAULT 'manual'
                     CHECK (trigger IN ('manual','scheduled','api','cli')),
      started_at     REAL NOT NULL,
      completed_at   REAL,
      total          INTEGER DEFAULT 0,
      passed         INTEGER DEFAULT 0,
      failed         INTEGER DEFAULT 0,
      error          INTEGER DEFAULT 0,
      skipped        INTEGER DEFAULT 0,
      duration_ms    REAL,
      suites         TEXT,
      notes          TEXT
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_run_status ON test_runs(status)",
    "CREATE INDEX IF NOT EXISTS idx_run_started ON test_runs(started_at)",
]

RESULTS_DDL = [
    """
    CREATE TABLE IF NOT EXISTS test_results (
      id           INTEGER PRIMARY KEY AUTOINCREMENT,
      run_id       TEXT,
      test_name    TEXT NOT NULL,
      tc_id        TEXT,
      suite        TEXT,
      category     TEXT,
      status       TEXT NOT NULL CHECK (status IN ('PASS','FAIL','ERROR','TIMEOUT','SKIP')),
      duration_ms  REAL,
      timestamp    TEXT NOT NULL,
      details_json TEXT,
      error        TEXT,
      upf_stats_json TEXT,
      protocol_trace_json TEXT,
      logs_json    TEXT,
      FOREIGN KEY (run_id) REFERENCES test_runs(id) ON DELETE CASCADE
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_tr_run ON test_results(run_id)",
    "CREATE INDEX IF NOT EXISTS idx_tr_name ON test_results(test_name)",
    "CREATE INDEX IF NOT EXISTS idx_tr_status ON test_results(status)",
    "CREATE INDEX IF NOT EXISTS idx_tr_ts ON test_results(timestamp)",
]

METRICS_DDL = [
    """
    CREATE TABLE IF NOT EXISTS test_metrics (
      id           INTEGER PRIMARY KEY AUTOINCREMENT,
      result_id    INTEGER NOT NULL,
      metric_name  TEXT NOT NULL,
      metric_value REAL NOT NULL,
      unit         TEXT,
      FOREIGN KEY (result_id) REFERENCES test_results(id) ON DELETE CASCADE
    )
    """,
    "CREATE INDEX IF NOT EXISTS idx_metric_result ON test_metrics(result_id)",
    "CREATE INDEX IF NOT EXISTS idx_metric_name ON test_metrics(metric_name)",
]

SCHEDULES_DDL = [
    """
    CREATE TABLE IF NOT EXISTS schedules (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      name        TEXT UNIQUE NOT NULL,
      cron_expr   TEXT NOT NULL,
      run_type    TEXT NOT NULL DEFAULT 'suite',
      suites      TEXT,
      enabled     INTEGER DEFAULT 1,
      last_run_at REAL,
      next_run_at REAL
    )
    """,
]

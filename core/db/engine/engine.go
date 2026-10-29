// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package engine — Database connection engine (multi-backend).
//
// Go port of db/engine.py. Single source of truth for SA Core database
// access, supporting SQLite (standalone) and PostgreSQL (vertical/horizontal).
// Backend selected by SA_CORE_DB_TYPE environment variable:
//
//	SA_CORE_DB_TYPE=sqlite      (default) — file-based, zero config
//	SA_CORE_DB_TYPE=postgresql  — connection pool, concurrent writes
//
// Uses modernc.org/sqlite (pure-Go, no CGO) so builds are portable.
package engine

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// ── Configuration ───────────────────────────────────────────────────────

var (
	DBType     = strings.ToLower(env("SA_CORE_DB_TYPE", "sqlite"))
	DBDSN      = env("SA_CORE_DB_DSN", "")
	InfraMode  = env("SA_CORE_INFRA_MODE", "standalone")
	DBFilePath = defaultSqlitePath()
)

var (
	once sync.Once
	db   *sql.DB
	err  error
)

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

// defaultSqlitePath mirrors the Python layout: <sacore>/webservice/sacore.db
// where <sacore> is the project root (two levels above this file in runtime).
func defaultSqlitePath() string {
	// In Go we cannot introspect the project layout the way Python's __file__
	// trick does. Allow explicit override via env, else use CWD-relative path.
	if v := env("SA_CORE_DB_FILE", ""); v != "" {
		return v
	}
	return filepath.Join("webservice", "sacore.db")
}

// ── Public API ──────────────────────────────────────────────────────────

// Open initializes the database pool (lazy, once). Returns the *sql.DB.
// Safe to call repeatedly; subsequent calls return the cached handle.
func Open() (*sql.DB, error) {
	once.Do(func() {
		switch DBType {
		case "postgresql":
			// Lazy import to avoid a hard dep on lib/pq when only SQLite is used.
			// Pure-Go driver github.com/jackc/pgx/v5/stdlib is preferred when added.
			err = fmt.Errorf("postgresql driver not yet wired — add jackc/pgx/v5/stdlib and set DBType=postgresql")
			return
		default:
			if dir := filepath.Dir(DBFilePath); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
			}
			// modernc.org/sqlite accepts a standard file path; enable WAL + FK.
			// synchronous=NORMAL is the SQLite-recommended pairing for WAL
			// (https://www.sqlite.org/pragma.html#pragma_synchronous):
			//   - WAL+FULL fsyncs on every commit → SeedAll's ~1280 baseline
			//     inserts paid ~14 s on the boot path.
			//   - WAL+NORMAL fsyncs only on checkpoint → SeedAll drops to
			//     ~1-2 s. Worst-case failure is losing the few committed
			//     writes since the last checkpoint on a power cut; the DB
			//     itself stays consistent (WAL guarantees that regardless).
			//     Acceptable here because everything in baseline.yaml is
			//     deterministic and idempotent — we can always re-seed.
			dsn := DBFilePath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
			db, err = sql.Open("sqlite", dsn)
			if err != nil {
				return
			}
			db.SetMaxOpenConns(1) // SQLite WAL allows concurrent reads but one writer
		}
	})
	return db, err
}

// Close tears down the pool. Intended for tests / shutdown.
func Close() error {
	if db != nil {
		e := db.Close()
		db = nil
		once = sync.Once{}
		return e
	}
	return nil
}

// AdaptSQL converts '?' placeholders to '$1,$2,...' for PostgreSQL.
// SQLite supports both '?' and '$N' so this is a no-op on SQLite.
func AdaptSQL(q string) string {
	if DBType != "postgresql" {
		return q
	}
	var sb strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&sb, "$%d", n)
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// Exec runs a non-query statement with paramstyle adaptation.
func Exec(q string, args ...any) (sql.Result, error) {
	h, err := Open()
	if err != nil {
		return nil, err
	}
	return h.Exec(AdaptSQL(q), args...)
}

// Query runs a SELECT with paramstyle adaptation.
func Query(q string, args ...any) (*sql.Rows, error) {
	h, err := Open()
	if err != nil {
		return nil, err
	}
	return h.Query(AdaptSQL(q), args...)
}

// QueryRow runs a single-row SELECT with paramstyle adaptation.
func QueryRow(q string, args ...any) *sql.Row {
	h, err := Open()
	if err != nil {
		// Return a *sql.Row that will surface err on Scan.
		// sql.Row has no public error constructor, so open a failing DB-less query
		// via a closed DB to propagate an error through Scan.
		bad, _ := sql.Open("sqlite", ":memory:")
		_ = bad.Close()
		return bad.QueryRow("SELECT 1")
	}
	return h.QueryRow(AdaptSQL(q), args...)
}

// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Schema for the security/core_security package — firewall rules,
// IDS signatures, blocked-IP list, known-gNB roster, and the
// security audit log.
//
// All four tables are referenced by core_security.go. Without this
// schema, every LogEvent / persisted rule would silently fail.

package schemas

func init() {
	Register("security_core", SecurityCoreDDL)
}

var SecurityCoreDDL = []string{
	`CREATE TABLE IF NOT EXISTS security_audit_log (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      event_type      TEXT NOT NULL,
      severity        TEXT NOT NULL DEFAULT 'INFO'
                      CHECK (severity IN ('DEBUG','INFO','WARNING','ERROR','CRITICAL')),
      source_ip       TEXT NOT NULL DEFAULT '',
      imsi            TEXT NOT NULL DEFAULT '',
      detail          TEXT NOT NULL DEFAULT '',
      extra_json      TEXT NOT NULL DEFAULT '',
      created_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS security_firewall_rules (
      id              INTEGER PRIMARY KEY AUTOINCREMENT,
      name            TEXT NOT NULL UNIQUE,
      protocol        TEXT NOT NULL CHECK (protocol IN ('ngap','nas','gtpu','sbi','any')),
      action          TEXT NOT NULL CHECK (action IN ('allow','deny','rate_limit')),
      src_cidr        TEXT NOT NULL DEFAULT '',
      dst_cidr        TEXT NOT NULL DEFAULT '',
      port_range      TEXT NOT NULL DEFAULT '',
      rate_limit      INTEGER NOT NULL DEFAULT 0,
      window_s        INTEGER NOT NULL DEFAULT 0,
      enabled         INTEGER NOT NULL DEFAULT 1,
      priority        INTEGER NOT NULL DEFAULT 100,
      updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS security_ids_signatures (
      id                INTEGER PRIMARY KEY AUTOINCREMENT,
      name              TEXT NOT NULL UNIQUE,
      pattern           TEXT NOT NULL,
      severity          TEXT NOT NULL DEFAULT 'WARNING'
                        CHECK (severity IN ('INFO','WARNING','ERROR','CRITICAL')),
      threshold         INTEGER NOT NULL DEFAULT 1,
      window_s          INTEGER NOT NULL DEFAULT 60,
      enabled           INTEGER NOT NULL DEFAULT 1,
      auto_block_ttl_s  INTEGER NOT NULL DEFAULT 0,
      updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
    )`,

	// expires_at NULL = permanent; non-NULL is an ISO datetime,
	// pruned by core_security.SweepBlockedIPs() and by lazy filter
	// in IsBlocked / ListBlockedIPs.
	`CREATE TABLE IF NOT EXISTS security_blocked_ips (
      ip              TEXT PRIMARY KEY,
      reason          TEXT NOT NULL DEFAULT '',
      added_at        TEXT NOT NULL DEFAULT (datetime('now')),
      added_by        TEXT NOT NULL DEFAULT 'system',
      expires_at      TEXT
    )`,

	`CREATE TABLE IF NOT EXISTS security_known_gnbs (
      ip              TEXT PRIMARY KEY,
      gnb_id          TEXT NOT NULL DEFAULT '',
      added_at        TEXT NOT NULL DEFAULT (datetime('now')),
      added_by        TEXT NOT NULL DEFAULT 'system'
    )`,

	`CREATE INDEX IF NOT EXISTS idx_secaudit_event ON security_audit_log(event_type, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_secaudit_sev ON security_audit_log(severity, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_secfw_priority ON security_firewall_rules(priority, enabled)`,
}
